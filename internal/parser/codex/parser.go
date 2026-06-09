package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"toktop.unceas.dev/internal/parser/components"
	"toktop.unceas.dev/internal/parser/shared"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

const ParserVersion = "codex/2"

type ParseResult struct {
	Session     trace.Session
	Turns       []trace.Turn
	ParseErrors []trace.ParseError
}

func ParseSession(ctx context.Context, raw source.RawSession) (ParseResult, error) {
	return ParseEvents(ctx, raw, raw.Events())
}

func ParseEvents(ctx context.Context, raw source.RawSession, events iter.Seq2[source.RawEvent, error]) (ParseResult, error) {
	sourceID := trace.SourceID(raw.Provider)
	sourceRootID := trace.SourceRootID(sourceID, raw.SourceRoot)
	session := trace.Session{
		Provider:       trace.InternString(raw.Provider),
		ProjectName:    trace.InternString("unknown"),
		TranscriptPath: raw.SourceFile,
		Status:         trace.StatusUnknown,
	}

	turns := make([]trace.Turn, 0)
	var current *turnBuilder
	var parseErrors []trace.ParseError

	flush := func() {
		if current == nil {
			return
		}
		turns = append(turns, current.finish())
		current = nil
	}

	for rawEvent, eventErr := range events {
		if eventErr != nil {
			return ParseResult{}, eventErr
		}
		if err := ctx.Err(); err != nil {
			return ParseResult{}, fmt.Errorf("parse cancelled: %w", err)
		}
		var event envelope
		if err := json.Unmarshal(rawEvent.RawJSON, &event); err != nil {
			parseErrors = append(parseErrors, trace.ParseError{
				SourceID:      sourceID,
				SourceRootID:  sourceRootID,
				SourceFile:    rawEvent.SourceFile,
				LineNo:        rawEvent.LineNo,
				Message:       err.Error(),
				ParserVersion: ParserVersion,
			})
			continue
		}
		when := trace.ParseEventTime(event.Timestamp)
		shared.UpdateSessionTimes(&session, when)

		switch event.Type {
		case "session_meta":
			meta := decodeSessionMeta(event.Payload)
			if meta.ID != "" {
				session.ExternalID = trace.InternString(meta.ID)
			}
			if meta.CWD != "" {
				session.ProjectPath = meta.CWD
				session.ProjectName = trace.InternString(lastPathSegment(meta.CWD))
				backfillProject(turns, session.ProjectName, session.ProjectPath)
				if current != nil {
					current.turn.ProjectName = session.ProjectName
					current.turn.ProjectPath = session.ProjectPath
				}
			}
		case "turn_context":
			payload := decodeTurnContext(event.Payload)
			flush()
			projectPath := textutil.FirstNonBlank(payload.CWD, session.ProjectPath)
			projectName := session.ProjectName
			if projectPath != "" {
				projectName = lastPathSegment(projectPath)
			}
			current = newTurnBuilder(&session, sourceRootID, len(turns)+1, projectName, projectPath, when)
		case "response_item":
			if current == nil {
				current = newTurnBuilder(&session, sourceRootID, len(turns)+1, session.ProjectName, session.ProjectPath, when)
			}
			current.handleResponseItem(event.Payload, when, rawEvent)
		case "event_msg":
			if current != nil {
				current.applyEventMessage(event.Payload)
			}
		}
	}
	flush()

	session.ID = trace.SessionID(sourceRootID, raw.SourceFile)
	if session.Status == trace.StatusUnknown && len(turns) > 0 {
		session.Status = trace.StatusCompleted
	}
	shared.FinalizeSession(&session, turns)

	result := ParseResult{Session: session, Turns: turns, ParseErrors: parseErrors}
	for i := range result.ParseErrors {
		result.ParseErrors[i].ParserVersion = trace.InternString(result.ParseErrors[i].ParserVersion)
	}
	return result, nil
}

type turnBuilder struct {
	session        *trace.Session
	sourceRootID   string
	turn           trace.Turn
	toolByCallID   map[string]int
	userMessageSet bool // true once an authoritative user_message event set UserMessage
}

func newTurnBuilder(session *trace.Session, sourceRootID string, index int, projectName, projectPath string, when time.Time) *turnBuilder {
	turnID := trace.TurnID(trace.SessionID(sourceRootID, session.TranscriptPath), index)
	return &turnBuilder{
		session:      session,
		sourceRootID: sourceRootID,
		turn: trace.Turn{
			ID:             turnID,
			Provider:       session.Provider,
			ProjectName:    projectName,
			ProjectPath:    projectPath,
			TranscriptPath: session.TranscriptPath,
			Index:          index,
			StartedAt:      when,
			EndedAt:        when,
			Status:         trace.StatusUnknown,
		},
		toolByCallID: make(map[string]int),
	}
}

func (b *turnBuilder) handleResponseItem(raw json.RawMessage, when time.Time, rawEvent source.RawEvent) {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	var payload responsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	switch payload.Type {
	case "message":
		text := shared.DecodeContentText(payload.Content, false)
		switch payload.Role {
		case "user":
			// The first role==user response_item is injected turn context
			// (AGENTS.md instructions, <INSTRUCTIONS>/<turn_aborted> wrappers),
			// not the human prompt. Only use it as a fallback when no
			// authoritative user_message event set UserMessage, and never adopt
			// recognizable injected-context wrappers.
			if text != "" && b.turn.UserMessage == "" && !b.userMessageSet && !isInjectedContext(text) {
				b.turn.UserMessage = text
			}
		case "assistant":
			if text != "" {
				b.turn.AssistantFinal = text
			}
			rawEventID := trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash())
			invocation := trace.Invocation{
				ID:         trace.InvocationID(b.turn.ID, len(b.turn.Invocations)+1),
				Provider:   b.turn.Provider,
				TurnID:     b.turn.ID,
				Index:      len(b.turn.Invocations) + 1,
				StartedAt:  when,
				EndedAt:    when,
				Status:     trace.StatusSuccess,
				RawEventID: rawEventID,
			}
			b.turn.Invocations = append(b.turn.Invocations, invocation)
		}
	case "function_call":
		callIndex := len(b.turn.ToolCalls) + 1
		toolCall := trace.ToolCall{
			ID:        trace.ToolCallID(b.turn.ID, payload.CallID, callIndex),
			TurnID:    b.turn.ID,
			CallIndex: callIndex,
			Kind:      shared.ClassifyToolKind(payload.Name),
			Name:      payload.Name,
			UseID:     payload.CallID,
			Input:     payload.Arguments,
			Status:    trace.StatusPending,
			StartedAt: when,
		}
		if toolCall.Kind == trace.ToolKindMCP {
			toolCall.MCPServer, toolCall.MCPTool = shared.SplitMCPName(toolCall.Name)
		}
		if payload.CallID != "" {
			b.toolByCallID[payload.CallID] = len(b.turn.ToolCalls)
		}
		b.turn.ToolCalls = append(b.turn.ToolCalls, toolCall)
		if len(b.turn.Invocations) > 0 {
			b.turn.ToolCalls[len(b.turn.ToolCalls)-1].InvocationID = b.turn.Invocations[len(b.turn.Invocations)-1].ID
		}
	case "function_call_output":
		idx, ok := b.toolByCallID[payload.CallID]
		if !ok || idx < 0 || idx >= len(b.turn.ToolCalls) {
			return
		}
		// A duplicate function_call_output for an already-resolved call must not
		// clobber the recorded output/status; only a still-pending call accepts one.
		if b.turn.ToolCalls[idx].Status != trace.StatusPending {
			return
		}
		call := &b.turn.ToolCalls[idx]
		output := outputText(payload.Output)
		call.Output = output
		call.OutputBytes = int64(len(output))
		call.EndedAt = when
		if !call.StartedAt.IsZero() && when.After(call.StartedAt) {
			call.DurationMs = when.Sub(call.StartedAt).Milliseconds()
		}
		if failure := outputFailure(output); failure != "" {
			call.Status = trace.StatusFailed
			call.Error = failure
		} else {
			call.Status = trace.StatusSuccess
		}
	}
}

func (b *turnBuilder) applyEventMessage(raw json.RawMessage) {
	var payload eventPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	switch payload.Type {
	case "user_message":
		// The authoritative human prompt arrives as a user_message event, not as
		// the first role==user response_item (which is injected turn context).
		if msg := strings.TrimSpace(payload.Message); msg != "" {
			b.turn.UserMessage = msg
			b.userMessageSet = true
		}
	case "token_count":
		// total_token_usage is cumulative over the whole session and must not be
		// summed per-turn/per-session. Attribute the per-event delta
		// (last_token_usage) to the most recent invocation so that the sum of
		// per-invocation tokens equals the turn total.
		if n := len(b.turn.Invocations); n > 0 {
			// Codex (OpenAI) reports input_tokens INCLUSIVE of the cached prefix,
			// with cached_input_tokens the cached subset — unlike Anthropic, which
			// reports them disjoint. toktop's trace.Tokens.Input is the UNCACHED
			// portion, so subtract the cached count out (clamped at 0) and record
			// it as CacheRead. CacheWrite stays 0: OpenAI automatic prompt caching
			// exposes only a read count, never a separate write/creation count.
			usage := payload.Info.LastTokenUsage
			uncached := max(usage.InputTokens-usage.CachedInputTokens, 0)
			// Accumulate, not assign: a turn can emit several token_count events that
			// all attribute to the same most-recent invocation, and an earlier delta
			// must not be clobbered by a later one.
			b.turn.Invocations[n-1].Tokens.Add(trace.Tokens{
				Input:     uncached,
				CacheRead: usage.CachedInputTokens,
				Output:    usage.OutputTokens,
			})
		}
	case "task_complete":
		if b.turn.Status == trace.StatusUnknown {
			b.turn.Status = trace.StatusSuccess
		}
	}
}

func (b *turnBuilder) finish() trace.Turn {
	turn := b.turn
	if !turn.StartedAt.IsZero() && !turn.EndedAt.IsZero() && turn.EndedAt.After(turn.StartedAt) {
		turn.DurationMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
	}
	turn.InvocationCount = len(turn.Invocations)
	turn.ToolCallCount = len(turn.ToolCalls)

	// turn.Tokens is the sum of per-invocation tokens (each set from a
	// token_count delta), never the cumulative session total.
	turn.Tokens = trace.Tokens{}
	for i := range turn.Invocations {
		turn.Tokens.Add(turn.Invocations[i].Tokens)
	}

	if derived := shared.StatusForTurn(turn); turn.Status == trace.StatusUnknown || derived == trace.StatusFailed {
		turn.Status = derived
	}
	turn.Components = components.FromTools(turn.ID, turn.ToolCalls)
	return turn
}

func backfillProject(turns []trace.Turn, projectName, projectPath string) {
	for i := range turns {
		if turns[i].ProjectName == "" || turns[i].ProjectName == "unknown" {
			turns[i].ProjectName = projectName
		}
		if turns[i].ProjectPath == "" {
			turns[i].ProjectPath = projectPath
		}
	}
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}

type turnContext struct {
	TurnID string `json:"turn_id"`
	CWD    string `json:"cwd"`
}

type responsePayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments string          `json:"arguments"`
	// Output is decoded flexibly: real Codex rollouts emit it as a string, but
	// multimodal results (e.g. view_image) emit a JSON array of content blocks.
	// Keeping it raw avoids aborting the whole responsePayload unmarshal.
	Output json.RawMessage `json:"output"`
}

type eventPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Info    struct {
		TotalTokenUsage tokenUsage `json:"total_token_usage"`
		LastTokenUsage  tokenUsage `json:"last_token_usage"`
	} `json:"info"`
}

type tokenUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

func decodeSessionMeta(raw json.RawMessage) sessionMeta {
	var meta sessionMeta
	_ = json.Unmarshal(raw, &meta)
	return meta
}

func decodeTurnContext(raw json.RawMessage) turnContext {
	var payload turnContext
	_ = json.Unmarshal(raw, &payload)
	return payload
}

// outputText decodes a function_call_output.output value, which may be a JSON
// string, an array of multimodal content blocks, or some other JSON value.
// Non-string outputs never abort the surrounding responsePayload unmarshal.
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"', '[':
		return shared.DecodeContentText(raw, false)
	default:
		return strings.TrimSpace(string(raw))
	}
}

// outputFailure inspects tool output for an explicit failure signal. Codex
// exec commands have no separate success flag; a non-zero exit is only visible
// in the output text (e.g. "Process exited with code 1"). Returns an error
// message when a failure is detected, otherwise "".
func outputFailure(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "process exited with code ") ||
			strings.HasPrefix(lower, "command exited with code ") {
			if code, ok := exitCode(trimmed); ok && code != 0 {
				return trimmed
			}
		}
	}
	return ""
}

// exitCode extracts the trailing integer exit code from a line like
// "Process exited with code 1". Returns false when no integer code is present.
func exitCode(line string) (int, bool) {
	idx := strings.LastIndex(line, "code ")
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[idx+len("code "):])
	rest = strings.TrimRight(rest, ".")
	if rest == "" {
		return 0, false
	}
	code, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return code, true
}

// isInjectedContext reports whether a role==user message is injected turn
// context (AGENTS.md instructions, tag-wrapped system context) rather than a
// human prompt.
func isInjectedContext(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	// Codex injects turn context as a single XML-ish wrapper element
	// (<INSTRUCTIONS>…</INSTRUCTIONS>, <turn_aborted/>). A human prompt that merely
	// starts with "<" (e.g. "<div> not rendering?") is not fully tag-wrapped and
	// must stay a prompt, so require an actual matching close / self-close rather
	// than treating any leading "<" as injected.
	if tag, ok := openingTagName(trimmed); ok &&
		(strings.HasSuffix(trimmed, "/>") || strings.Contains(trimmed, "</"+tag+">")) {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "agents.md") || strings.HasPrefix(lower, "# instructions")
}

// openingTagName returns the element name of a leading "<name>"/"<name ...>" tag
// when text begins with a well-formed XML-ish tag name, else ok=false.
func openingTagName(text string) (string, bool) {
	if !strings.HasPrefix(text, "<") {
		return "", false
	}
	end := strings.IndexAny(text[1:], " \t\r\n>/")
	if end <= 0 {
		return "", false
	}
	name := text[1 : 1+end]
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.'):
		default:
			return "", false
		}
	}
	return name, true
}

func lastPathSegment(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "unknown"
	}
	index := strings.LastIndex(path, "/")
	if index < 0 || index == len(path)-1 {
		return path
	}
	return path[index+1:]
}

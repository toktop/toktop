package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"toktop.unceas.dev/internal/parser/components"
	"toktop.unceas.dev/internal/parser/shared"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

const ParserVersion = "opencode/1"

type ParseResult struct {
	Session     trace.Session
	Turns       []trace.Turn
	ParseErrors []trace.ParseError
}

func ParseSession(ctx context.Context, raw source.RawSession) (ParseResult, error) {
	return ParseEvents(ctx, raw, raw.RawEventList)
}

func ParseEvents(ctx context.Context, raw source.RawSession, events []source.RawEvent) (ParseResult, error) {
	sourceID := trace.SourceID(raw.Provider)
	sourceRootID := trace.SourceRootID(sourceID, raw.SourceRoot)
	session := trace.Session{
		Provider:       trace.InternString(raw.Provider),
		ProjectName:    trace.InternString("unknown"),
		TranscriptPath: raw.SourceFile,
		Status:         trace.StatusUnknown,
	}
	session.ID = trace.SessionID(sourceRootID, raw.SourceFile)
	// Subagent linkage the collector resolved with whole-DB visibility (the parser
	// sees one session at a time): the parent tool_use is the spawning task call.
	if raw.ParentToolUseID != "" {
		session.ParentToolUseID = trace.InternString(raw.ParentToolUseID)
	}

	turns := make([]trace.Turn, 0)
	var current *turnBuilder
	var parseErrors []trace.ParseError

	flush := func() {
		if current == nil {
			return
		}
		if current.empty() {
			current = nil
			return
		}
		turns = append(turns, current.finish())
		current = nil
	}
	addErr := func(rawEvent source.RawEvent, message string) {
		parseErrors = append(parseErrors, trace.ParseError{
			SourceID:      sourceID,
			SourceRootID:  sourceRootID,
			SourceFile:    rawEvent.SourceFile,
			LineNo:        rawEvent.LineNo,
			RawEventID:    trace.RawEventID(sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash()),
			Message:       message,
			ParserVersion: ParserVersion,
		})
	}

	for _, rawEvent := range events {
		if err := ctx.Err(); err != nil {
			return ParseResult{}, fmt.Errorf("parse cancelled: %w", err)
		}
		when := rawEvent.EventTime
		shared.UpdateSessionTimes(&session, when)

		switch rawEvent.EventType {
		case KindSession:
			var env SessionEnvelope
			if err := json.Unmarshal(rawEvent.RawJSON, &env); err != nil {
				addErr(rawEvent, err.Error())
				continue
			}
			applySession(&session, env, sourceRootID, turns, current)
		case KindUser:
			flush()
			current = newTurnBuilder(&session, sourceRootID, len(turns)+1, when)
			current.role = KindUser
		case KindAssistant:
			if current == nil {
				current = newTurnBuilder(&session, sourceRootID, len(turns)+1, when)
			}
			current.role = KindAssistant
			if err := current.startAssistant(rawEvent, when); err != nil {
				addErr(rawEvent, err.Error())
			}
		case KindText, KindTool, KindReasoning, KindStepStart, KindStepFinish, KindFile:
			if current == nil {
				// A part with no enclosing message (shouldn't happen); skip rather
				// than crash, recording it so the gap is visible.
				addErr(rawEvent, "part "+rawEvent.EventType+" outside any message")
				continue
			}
			current.handlePart(rawEvent, when, addErr)
		default:
			addErr(rawEvent, "unknown event type "+rawEvent.EventType)
		}
	}
	flush()

	if session.Status == trace.StatusUnknown && len(turns) > 0 {
		session.Status = trace.StatusCompleted
	}
	shared.FinalizeSession(&session, turns)
	return ParseResult{Session: session, Turns: turns, ParseErrors: parseErrors}, nil
}

func applySession(session *trace.Session, env SessionEnvelope, sourceRootID string, turns []trace.Turn, current *turnBuilder) {
	if env.ID != "" {
		session.ExternalID = trace.InternString(env.ID)
	}
	if t := cleanTitle(env.Title); t != "" {
		session.Title = t
	}
	if env.Directory != "" {
		session.ProjectPath = env.Directory
		session.ProjectName = trace.InternString(lastPathSegment(env.Directory))
		for i := range turns {
			if turns[i].ProjectName == "" || turns[i].ProjectName == "unknown" {
				turns[i].ProjectName = session.ProjectName
			}
			if turns[i].ProjectPath == "" {
				turns[i].ProjectPath = session.ProjectPath
			}
		}
		if current != nil {
			current.turn.ProjectName = session.ProjectName
			current.turn.ProjectPath = session.ProjectPath
		}
	}
	if env.ParentID != "" {
		session.IsSubagent = true
		session.SubagentKind = "agent"
		session.ParentExternalID = trace.InternString(env.ParentID)
		session.AgentType = trace.InternString(env.Agent)
	}
}

type turnBuilder struct {
	session      *trace.Session
	sourceRootID string
	turn         trace.Turn
	role         string   // role of the message whose parts are currently arriving
	userParts    []string // user text parts, joined at finish
}

func newTurnBuilder(session *trace.Session, sourceRootID string, index int, when time.Time) *turnBuilder {
	return &turnBuilder{
		session:      session,
		sourceRootID: sourceRootID,
		turn: trace.Turn{
			ID:             trace.TurnID(session.ID, index),
			Provider:       session.Provider,
			ProjectName:    session.ProjectName,
			ProjectPath:    session.ProjectPath,
			TranscriptPath: session.TranscriptPath,
			Index:          index,
			StartedAt:      when,
			EndedAt:        when,
			Status:         trace.StatusUnknown,
		},
	}
}

// startAssistant creates one invocation per assistant message, attributing the
// message's per-message tokens (NOT cumulative — verified: the per-message sum
// equals the session token columns) directly to it.
func (b *turnBuilder) startAssistant(rawEvent source.RawEvent, when time.Time) error {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	var env MessageEnvelope
	if err := json.Unmarshal(rawEvent.RawJSON, &env); err != nil {
		return err
	}
	var data assistantData
	_ = json.Unmarshal(env.Data, &data)
	invocation := trace.Invocation{
		ID:         trace.InvocationID(b.turn.ID, len(b.turn.Invocations)+1),
		Provider:   b.turn.Provider,
		TurnID:     b.turn.ID,
		Index:      len(b.turn.Invocations) + 1,
		Model:      trace.InternString(data.ModelID),
		StartedAt:  msTime(data.Time.Created),
		EndedAt:    msTime(data.Time.Completed),
		StopReason: trace.InternString(data.Finish),
		Status:     trace.StatusSuccess,
		Tokens: trace.Tokens{
			Input:      data.Tokens.Input,
			Output:     data.Tokens.Output,
			CacheRead:  data.Tokens.Cache.Read,
			CacheWrite: data.Tokens.Cache.Write,
		},
		RawEventID: trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash()),
	}
	if invocation.StartedAt.IsZero() {
		invocation.StartedAt = when
	}
	if invocation.EndedAt.IsZero() {
		invocation.EndedAt = when
	}
	b.turn.Invocations = append(b.turn.Invocations, invocation)
	return nil
}

func (b *turnBuilder) handlePart(rawEvent source.RawEvent, when time.Time, addErr func(source.RawEvent, string)) {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	var env PartEnvelope
	if err := json.Unmarshal(rawEvent.RawJSON, &env); err != nil {
		addErr(rawEvent, err.Error())
		return
	}
	switch rawEvent.EventType {
	case KindText:
		text := decodePartText(env.Data)
		if text == "" {
			return
		}
		if b.role == KindUser {
			lower := strings.ToLower(strings.TrimSpace(text))
			if shared.IsLocalCommandInjection(strings.TrimSpace(text), lower) {
				return
			}
			b.userParts = append(b.userParts, text)
		} else {
			b.turn.AssistantFinal = text
		}
	case KindTool:
		b.recordToolCall(env.Data, when, rawEvent)
	}
	// reasoning / step-start / step-finish / file carry no neutral structural
	// mapping; they remain in raw_events for provenance.
}

func (b *turnBuilder) recordToolCall(data json.RawMessage, when time.Time, rawEvent source.RawEvent) {
	var tp toolPart
	if err := json.Unmarshal(data, &tp); err != nil {
		return
	}
	rawEventID := trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash())
	callIndex := len(b.turn.ToolCalls) + 1
	// opencode tool names are bare builtins here; an MCP tool already named
	// mcp__server__tool classifies as MCP. opencode's exact MCP tool-name shape is
	// unverified against real data, so no provider-specific fold is invented — the
	// name passes through, keeping parity when it already matches the canonical form.
	name := tp.Tool
	kind := shared.ClassifyToolKind(name)
	started := msTime(tp.State.Time.Start)
	if started.IsZero() {
		started = when
	}
	ended := msTime(tp.State.Time.End)
	output := b.resolveOutput(tp)
	call := trace.ToolCall{
		ID:               trace.ToolCallID(b.turn.ID, tp.CallID, callIndex),
		TurnID:           b.turn.ID,
		CallIndex:        callIndex,
		Kind:             kind,
		Name:             name,
		UseID:            tp.CallID,
		Input:            inputText(tp.State.Input),
		Output:           output,
		OutputBytes:      int64(len(output)),
		Status:           toolStatus(tp),
		StartedAt:        started,
		EndedAt:          ended,
		RawUseEventID:    rawEventID,
		RawResultEventID: rawEventID, // input and output are co-located in one part
	}
	if call.Status == trace.StatusFailed {
		call.Error = toolError(tp, output)
	}
	if kind == trace.ToolKindMCP {
		call.MCPServer, call.MCPTool = shared.SplitMCPName(name)
	}
	if !started.IsZero() && !ended.IsZero() && ended.After(started) {
		call.DurationMs = ended.Sub(started).Milliseconds()
	}
	if n := len(b.turn.Invocations); n > 0 {
		call.InvocationID = b.turn.Invocations[n-1].ID
	}
	b.turn.ToolCalls = append(b.turn.ToolCalls, call)
}

// resolveOutput returns a tool call's full output: the inline state.output, or —
// when opencode spilled a truncated output to a file — the full spill-file content
// (state.metadata.outputPath). A spill read error falls back to the inline text.
func (b *turnBuilder) resolveOutput(tp toolPart) string {
	inline := outputText(tp.State.Output)
	if tp.State.Metadata.Truncated && tp.State.Metadata.OutputPath != "" {
		if full, err := os.ReadFile(tp.State.Metadata.OutputPath); err == nil {
			return string(full)
		}
	}
	return inline
}

func (b *turnBuilder) finish() trace.Turn {
	turn := b.turn
	turn.UserMessage = strings.Join(b.userParts, "\n\n")
	if !turn.StartedAt.IsZero() && !turn.EndedAt.IsZero() && turn.EndedAt.After(turn.StartedAt) {
		turn.DurationMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
	}
	turn.InvocationCount = len(turn.Invocations)
	turn.ToolCallCount = len(turn.ToolCalls)
	turn.Tokens = trace.Tokens{}
	for i := range turn.Invocations {
		turn.Tokens.Add(turn.Invocations[i].Tokens)
	}
	turn.Status = shared.ResolveTurnStatus(turn)
	turn.Components = components.FromTools(turn.ID, turn.ToolCalls)
	return turn
}

func (b *turnBuilder) empty() bool {
	return len(b.userParts) == 0 &&
		b.turn.AssistantFinal == "" &&
		len(b.turn.Invocations) == 0 &&
		len(b.turn.ToolCalls) == 0
}

// assistantData is the subset of an opencode assistant message.data the parser
// reads. tokens are per-message (not cumulative).
type assistantData struct {
	ModelID string `json:"modelID"`
	Finish  string `json:"finish"`
	Time    struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
		Cache  struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

// toolPart is the subset of an opencode tool part.data the parser reads.
type toolPart struct {
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Output   json.RawMessage `json:"output"`
		Metadata struct {
			Truncated  bool   `json:"truncated"`
			OutputPath string `json:"outputPath"`
			Exit       *int   `json:"exit"` // bash exit code, when present
		} `json:"metadata"`
		Time struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
	} `json:"state"`
}

// toolStatus maps opencode's tool state.status to a neutral status. Only
// "completed" appears in observed data; the failure mappings (an explicit "error"
// status, a non-zero bash exit) are defensive and UNVERIFIED against a real failing
// tool — treat a non-terminal status as pending rather than guessing success.
func toolStatus(tp toolPart) string {
	if tp.State.Metadata.Exit != nil && *tp.State.Metadata.Exit != 0 {
		return trace.StatusFailed
	}
	switch tp.State.Status {
	case "completed":
		return trace.StatusSuccess
	case "error":
		return trace.StatusFailed
	default:
		return trace.StatusPending
	}
}

func toolError(tp toolPart, output string) string {
	if tp.State.Metadata.Exit != nil && *tp.State.Metadata.Exit != 0 {
		return "exit code " + strconv.Itoa(*tp.State.Metadata.Exit)
	}
	if tp.State.Status == "error" {
		if output != "" {
			return textutil.OneLine(output, 200)
		}
		return "tool reported error"
	}
	return ""
}

// decodePartText extracts the text from a text part's data ({"type":"text","text":…}).
func decodePartText(data json.RawMessage) string {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Text)
}

// inputText renders a tool call's input object as a compact JSON string,
// defaulting to "{}" for an absent/empty input.
func inputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}

// outputText decodes a tool output value, which opencode emits as a JSON string
// (the inline output) or, rarely, another JSON value.
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

// subagentTitleSuffix matches opencode's cosmetic " (@<agent> subagent)" suffix on
// a subagent session's title, which the parser strips to keep titles clean.
var subagentTitleSuffix = regexp.MustCompile(`\s*\(@[\w.-]+\s+subagent\)\s*$`)

func cleanTitle(title string) string {
	return textutil.OneLine(subagentTitleSuffix.ReplaceAllString(title, ""), 200)
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func lastPathSegment(dir string) string {
	base := path.Base(dir)
	if base == "." || base == "/" {
		return "unknown"
	}
	return base
}

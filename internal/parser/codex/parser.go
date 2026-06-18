package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
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
	return ParseEvents(ctx, raw, raw.RawEventList)
}

func ParseEvents(ctx context.Context, raw source.RawSession, events []source.RawEvent) (ParseResult, error) {
	sourceID := trace.SourceID(raw.Provider)
	sourceRootID := trace.SourceRootID(sourceID, raw.SourceRoot)
	session := trace.Session{
		Provider:       trace.InternString(raw.Provider),
		ProjectName:    trace.InternString("unknown"),
		TranscriptPath: raw.SourceFile,
		SourceRoot:     raw.SourceRoot,
		Status:         trace.StatusUnknown,
	}
	session.ID = trace.SessionID(sourceRootID, raw.SourceFile)

	turns := make([]trace.Turn, 0)
	var current *turnBuilder
	var parseErrors []trace.ParseError
	seenTurnContext := false
	// model_context_window arrives once per session in a task_started event_msg
	// (before any turn_context), so it is tracked at session scope and stamped onto
	// every turn's invocations — mirroring claude's per-invocation context window.
	contextWindow := 0

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

	for _, rawEvent := range events {
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
			if spawn := meta.Source.Subagent.ThreadSpawn; spawn != nil && spawn.ParentThreadID != "" {
				// A real spawned codex agent: mark it and link by external id (the store
				// resolves ParentSessionID). Gate on the structural thread_spawn marker
				// (NOT just parent_thread_id — guardian/judge "other" subagent threads
				// also carry a parent_thread_id, and marking those would hide them with no
				// parent to surface them); also require the parent id so a parent-less
				// spawn stays top-level/visible rather than becoming a hidden orphan.
				session.IsSubagent = true
				session.SubagentKind = "agent"
				session.ParentExternalID = trace.InternString(spawn.ParentThreadID)
				session.AgentType = trace.InternString(spawn.AgentRole)
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
			seenTurnContext = true
			projectPath := textutil.FirstNonBlank(payload.CWD, session.ProjectPath)
			projectName := session.ProjectName
			if projectPath != "" {
				projectName = lastPathSegment(projectPath)
			}
			current = newTurnBuilder(&session, sourceRootID, len(turns)+1, projectName, projectPath, payload.Model, contextWindow, when)
		case "response_item":
			if current == nil {
				if !seenTurnContext && isInjectedPreTurnResponseItem(event.Payload) {
					continue
				}
				current = newTurnBuilder(&session, sourceRootID, len(turns)+1, session.ProjectName, session.ProjectPath, "", contextWindow, when)
			}
			parseErrors = append(parseErrors, current.handleResponseItem(event.Payload, when, rawEvent)...)
		case "event_msg":
			var ev eventPayload
			if err := json.Unmarshal(event.Payload, &ev); err == nil {
				if ev.Type == "task_started" && ev.ModelContextWindow > 0 {
					contextWindow = ev.ModelContextWindow
					if current != nil {
						current.contextWindow = contextWindow
					}
				}
				if current != nil {
					current.applyEventMessage(ev)
				}
			}
		}
	}
	flush()

	if session.Status == trace.StatusUnknown && len(turns) > 0 {
		session.Status = trace.StatusCompleted
	}
	shared.FinalizeSession(&session, turns)

	result := ParseResult{Session: session, Turns: turns, ParseErrors: parseErrors}
	return result, nil
}

type turnBuilder struct {
	session        *trace.Session
	sourceRootID   string
	turn           trace.Turn
	toolByCallID   map[string]int
	userMessageSet bool // true once an authoritative user_message event set UserMessage
	pendingTokens  trace.Tokens
	// model and contextWindow come from the turn's turn_context / task_started
	// events and are stamped onto every invocation in the turn (codex records them
	// per turn, unlike claude's per-message model).
	model         string
	contextWindow int
}

func newTurnBuilder(session *trace.Session, sourceRootID string, index int, projectName, projectPath, model string, contextWindow int, when time.Time) *turnBuilder {
	turnID := trace.TurnID(session.ID, index)
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
		toolByCallID:  make(map[string]int),
		model:         trace.InternString(model),
		contextWindow: contextWindow,
	}
}

func (b *turnBuilder) handleResponseItem(raw json.RawMessage, when time.Time, rawEvent source.RawEvent) []trace.ParseError {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	var payload responsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return []trace.ParseError{b.parseError(rawEvent, err.Error())}
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
				ID:                  trace.InvocationID(b.turn.ID, len(b.turn.Invocations)+1),
				Provider:            b.turn.Provider,
				TurnID:              b.turn.ID,
				Index:               len(b.turn.Invocations) + 1,
				Model:               b.model,
				StartedAt:           when,
				EndedAt:             when,
				Status:              trace.StatusSuccess,
				ContextWindowTokens: b.contextWindow,
				Tokens:              b.pendingTokens,
				RawEventID:          rawEventID,
			}
			b.pendingTokens = trace.Tokens{}
			b.turn.Invocations = append(b.turn.Invocations, invocation)
		}
	case "function_call":
		b.recordToolCall(payload.Name, payload.CallID, argumentText(payload.Arguments), payload.Namespace, when, rawEvent)
	case "custom_tool_call":
		// Codex's apply_patch (and other custom tools) arrive as custom_tool_call,
		// not function_call — the call args live under "input", and it carries no
		// namespace. Recording it mirrors the function_call path so file edits and
		// the tool-call count are not silently dropped.
		b.recordToolCall(payload.Name, payload.CallID, argumentText(payload.Input), "", when, rawEvent)
	case "function_call_output", "custom_tool_call_output":
		return b.recordToolOutput(payload.CallID, payload.Output, when, rawEvent)
	}
	return nil
}

// recordToolCall appends a pending ToolCall for a function_call / custom_tool_call.
// namespace is codex's MCP routing prefix (e.g. "mcp__node_repl__") carried apart
// from the bare name ("js"), unlike claude-code which puts mcp__server__tool in the
// name itself; when present it is folded back into the canonical mcp__server__tool
// form — used for classification/splitting AND stored as ToolCall.Name, so the same
// logical MCP tool has an identical neutral name across providers (parity). It is ""
// for custom tools, leaving their bare name unchanged.
func (b *turnBuilder) recordToolCall(name, callID, input, namespace string, when time.Time, rawEvent source.RawEvent) {
	rawEventID := trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash())
	callIndex := len(b.turn.ToolCalls) + 1
	classifyName := name
	if ns := strings.TrimSuffix(namespace, "__"); strings.HasPrefix(ns, "mcp__") {
		classifyName = ns + "__" + name
	}
	kind := shared.ClassifyToolKind(classifyName)
	toolCall := trace.ToolCall{
		ID:            trace.ToolCallID(b.turn.ID, callID, callIndex),
		TurnID:        b.turn.ID,
		CallIndex:     callIndex,
		Kind:          kind,
		Name:          classifyName,
		UseID:         callID,
		Input:         input,
		Status:        trace.StatusPending,
		StartedAt:     when,
		RawUseEventID: rawEventID,
	}
	if kind == trace.ToolKindMCP {
		toolCall.MCPServer, toolCall.MCPTool = shared.SplitMCPName(classifyName)
	}
	if callID != "" {
		b.toolByCallID[callID] = len(b.turn.ToolCalls)
	}
	b.turn.ToolCalls = append(b.turn.ToolCalls, toolCall)
	if len(b.turn.Invocations) > 0 {
		b.turn.ToolCalls[len(b.turn.ToolCalls)-1].InvocationID = b.turn.Invocations[len(b.turn.Invocations)-1].ID
	}
}

// recordToolOutput resolves a pending tool call by call_id with a
// function_call_output / custom_tool_call_output payload, setting output, timing
// and success/failure. Returns a parse error for an unmatched or duplicate output.
func (b *turnBuilder) recordToolOutput(callID string, rawOutput json.RawMessage, when time.Time, rawEvent source.RawEvent) []trace.ParseError {
	idx, ok := b.toolByCallID[callID]
	if !ok || idx < 0 || idx >= len(b.turn.ToolCalls) {
		return []trace.ParseError{b.unmatchedToolOutputError(rawEvent, callID)}
	}
	// A duplicate output for an already-resolved call must not clobber the recorded
	// output/status; only a still-pending call accepts one.
	if b.turn.ToolCalls[idx].Status != trace.StatusPending {
		return []trace.ParseError{b.duplicateToolOutputError(rawEvent, callID)}
	}
	call := &b.turn.ToolCalls[idx]
	output := outputText(rawOutput)
	call.Output = output
	call.OutputBytes = int64(len(output))
	call.EndedAt = when
	call.RawResultEventID = trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash())
	if !call.StartedAt.IsZero() && when.After(call.StartedAt) {
		call.DurationMs = when.Sub(call.StartedAt).Milliseconds()
	}
	if failure := outputFailure(call.Name, output); failure != "" {
		call.Status = trace.StatusFailed
		call.Error = failure
	} else if call.Name == "spawn_agent" && spawnAgentID(output) == "" {
		// A spawn_agent that returned no agent_id never launched an agent (e.g.
		// "Full-history forked agents inherit…"). outputFailure only sees shell
		// exit codes, so without this it would be mis-counted as a completed agent
		// run in the handoff. (Mirrors collector/codex's AgentSpawnChildID — both
		// are codex-local knowledge of the {agent_id} success shape.)
		call.Status = trace.StatusFailed
		call.Error = "spawn_agent returned no agent_id"
	} else {
		call.Status = trace.StatusSuccess
	}
	return nil
}

// spawnAgentID extracts the spawned agent's id from a spawn_agent function_call
// output ({"agent_id":…,"nickname":…} on success; plain error text on failure).
func spawnAgentID(output string) string {
	var out struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal([]byte(output), &out)
	return out.AgentID
}

func (b *turnBuilder) parseError(rawEvent source.RawEvent, message string) trace.ParseError {
	return trace.ParseError{
		SourceID:      trace.SourceID(b.turn.Provider),
		SourceRootID:  b.sourceRootID,
		SourceFile:    rawEvent.SourceFile,
		LineNo:        rawEvent.LineNo,
		RawEventID:    trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash()),
		Message:       message,
		ParserVersion: ParserVersion,
	}
}

func (b *turnBuilder) unmatchedToolOutputError(rawEvent source.RawEvent, callID string) trace.ParseError {
	if callID == "" {
		return b.parseError(rawEvent, "unmatched tool output without call_id")
	}
	return b.parseError(rawEvent, "unmatched tool output")
}

func (b *turnBuilder) duplicateToolOutputError(rawEvent source.RawEvent, callID string) trace.ParseError {
	if callID == "" {
		return b.parseError(rawEvent, "duplicate tool output without call_id")
	}
	return b.parseError(rawEvent, "duplicate tool output")
}

func (b *turnBuilder) applyEventMessage(payload eventPayload) {
	switch payload.Type {
	case "user_message":
		// The authoritative human prompt arrives as a user_message event, not as
		// the first role==user response_item (which is injected turn context).
		if msg := strings.TrimSpace(payload.Message); msg != "" {
			if isInjectedContext(msg) {
				return
			}
			if b.userMessageSet && b.turn.UserMessage != "" {
				b.turn.UserMessage += "\n\n" + msg
			} else {
				b.turn.UserMessage = msg
			}
			b.userMessageSet = true
		}
	case "token_count":
		// total_token_usage is cumulative over the whole session and must not be
		// summed per-turn/per-session. Attribute the per-event delta
		// (last_token_usage) to the most recent invocation so that the sum of
		// per-invocation tokens equals the turn total.
		usage := payload.Info.LastTokenUsage
		delta := trace.Tokens{
			Input:     max(usage.InputTokens-usage.CachedInputTokens, 0),
			CacheRead: usage.CachedInputTokens,
			Output:    usage.OutputTokens,
		}
		if n := len(b.turn.Invocations); n > 0 {
			b.turn.Invocations[n-1].Tokens.Add(delta)
		} else {
			b.pendingTokens.Add(delta)
		}
	case "task_complete":
		if b.turn.Status == trace.StatusUnknown {
			b.turn.Status = trace.StatusSuccess
		}
	case "turn_aborted":
		// Mark the aborted TURN interrupted so it does not masquerade as success in
		// the turns/digest views; shared.ResolveTurnStatus keeps this explicit status
		// over the derivation in finish(). The codex SESSION status is still forced to
		// completed (see ParseEvents below), so session-status consumers are unaffected
		// — but a session ENDING on an aborted turn does surface as interrupted wherever
		// the LAST-turn status leads (the live/status listing's current-status and the
		// handoff workflow_status both read the final turn), which is the intended,
		// more-accurate signal.
		b.turn.Status = trace.StatusInterrupted
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
	// pendingTokens holds token_count deltas that arrived before any invocation
	// existed. It is drained (reset to {}) once an assistant message creates an
	// invocation, so it is non-zero here only for a kept turn that ended without
	// one (interrupted/aborted after its usage event); fold it in so those tokens
	// are not dropped from the turn and session totals.
	turn.Tokens.Add(b.pendingTokens)

	turn.Status = shared.ResolveTurnStatus(turn)
	turn.Components = components.FromTools(turn.ID, turn.ToolCalls)
	return turn
}

func (b *turnBuilder) empty() bool {
	return b.turn.UserMessage == "" &&
		b.turn.AssistantFinal == "" &&
		len(b.turn.Invocations) == 0 &&
		len(b.turn.ToolCalls) == 0
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
	// Subagent linkage (set only on a spawned agent's rollout). A codex subagent is
	// a flat rollout in sessions/ — indistinguishable by path — marked in-file by a
	// nested source.subagent.thread_spawn object. Its PRESENCE is the discriminator:
	// guardian/judge threads instead carry source.subagent.other and must stay
	// top-level even though they too can have a parent_thread_id. thread_spawn carries
	// the launching session's id (the parent's external id) and the spawned agent_role.
	Source struct {
		Subagent struct {
			ThreadSpawn *struct {
				ParentThreadID string `json:"parent_thread_id"`
				AgentRole      string `json:"agent_role"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	} `json:"source"`
}

type turnContext struct {
	CWD   string `json:"cwd"`
	Model string `json:"model"`
}

type responsePayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	// Namespace is codex's MCP routing prefix on a function_call (e.g.
	// "mcp__node_repl__"); the call's Name is the bare tool ("js").
	Namespace string          `json:"namespace"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
	// Input carries a custom_tool_call's arguments (function_call uses Arguments).
	Input json.RawMessage `json:"input"`
	// Output is decoded flexibly: real Codex rollouts emit it as a string, but
	// multimodal results (e.g. view_image) emit a JSON array of content blocks.
	// Keeping it raw avoids aborting the whole responsePayload unmarshal.
	Output json.RawMessage `json:"output"`
}

type eventPayload struct {
	Type               string `json:"type"`
	Message            string `json:"message"`
	ModelContextWindow int    `json:"model_context_window"`
	Info               struct {
		LastTokenUsage tokenUsage `json:"last_token_usage"`
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

func argumentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			text = strings.TrimSpace(text)
			if text == "" {
				return "{}"
			}
			return text
		}
	}
	return strings.TrimSpace(string(raw))
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

// outputFailure inspects a tool's output for an explicit failure signal. Codex tools
// carry no separate success flag, so a failure shows only in the output text, in one
// of a few shapes: a shell exec reports "Process exited with code N", the custom-tool
// runner reports "Exit code: N" (and, in an older rollout format, wraps the result as
// a JSON object with metadata.exit_code), and apply_patch reports "apply_patch
// verification failed: …" with no exit code at all. Returns a short failure message
// when one is detected, otherwise "". A zero exit code is success, never flagged. name
// scopes the exit-code-less apply_patch phrase to the apply_patch tool, so a different
// tool whose output merely echoes that phrase (e.g. a grep/cat of a prior error) is
// not mis-flagged.
func outputFailure(name, output string) string {
	if code, ok := metadataExitCode(output); ok && code != 0 {
		return "exit code " + strconv.Itoa(code)
	}
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if name == "apply_patch" && strings.HasPrefix(strings.ToLower(trimmed), "apply_patch verification failed") {
			return trimmed
		}
		if code, ok := exitCodeLine(trimmed); ok && code != 0 {
			return trimmed
		}
	}
	return ""
}

// exitCodeLine extracts the exit code a single output line reports, matching codex's
// two non-zero-is-failure shapes — the shell exec "process/command exited with code
// N" and the custom-tool runner "Exit code: N" — and returns ok=false for any other
// line, so unrelated text and a success "Exit code: 0" are left to the != 0 check.
func exitCodeLine(line string) (int, bool) {
	lower := strings.ToLower(line)
	var rest string
	switch {
	case strings.HasPrefix(lower, "process exited with code "):
		rest = line[len("process exited with code "):]
	case strings.HasPrefix(lower, "command exited with code "):
		rest = line[len("command exited with code "):]
	case strings.HasPrefix(lower, "exit code:"):
		rest = line[len("exit code:"):]
	default:
		return 0, false
	}
	rest = strings.TrimRight(strings.TrimSpace(rest), ".")
	code, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return code, true
}

// metadataExitCode reads the exit code from the older codex custom-tool output shape,
// a JSON object {"output":…,"metadata":{"exit_code":N}}. ok is true only when output
// parses as such an object carrying a numeric exit_code, so a plain-text output (the
// current shape) or a missing field is left to the line scan. A pointer distinguishes
// an absent exit_code from a real 0.
func metadataExitCode(output string) (int, bool) {
	s := strings.TrimSpace(output)
	if !strings.HasPrefix(s, "{") {
		return 0, false
	}
	var wrap struct {
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(s), &wrap) != nil || wrap.Metadata.ExitCode == nil {
		return 0, false
	}
	return *wrap.Metadata.ExitCode, true
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
	if shared.IsLocalCommandInjection(trimmed, lower) {
		return true
	}
	return strings.Contains(lower, "agents.md") || strings.HasPrefix(lower, "# instructions")
}

func isInjectedPreTurnResponseItem(raw json.RawMessage) bool {
	var payload responsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	if payload.Type != "message" {
		return false
	}
	switch payload.Role {
	case "developer", "system":
		return true
	case "user":
		text := shared.DecodeContentText(payload.Content, false)
		return text == "" || isInjectedContext(text)
	default:
		return false
	}
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

func lastPathSegment(cwd string) string {
	base := path.Base(cwd)
	if base == "." || base == "/" {
		return "unknown"
	}
	return base
}

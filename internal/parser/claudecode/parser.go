package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"toktop.unceas.dev/internal/parser/components"
	"toktop.unceas.dev/internal/parser/shared"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

const ParserVersion = "claudecode/5"

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
		ProjectName:    trace.InternString(raw.ProjectName),
		ProjectPath:    raw.ProjectPath,
		TranscriptPath: raw.SourceFile,
		Status:         trace.StatusUnknown,
	}
	session.ID = trace.SessionID(sourceRootID, raw.SourceFile)
	if raw.IsSubagent {
		// Identity is hashed from this subagent's OWN transcript path (above), so it
		// is distinct from the parent and every sibling — no UUID collision. The
		// parent link comes from the path (the <uuid> dir before subagents/, which IS
		// the parent's external id), so it is set even when the transcript carries no
		// in-file sessionId; the store resolves ParentSessionID from it.
		session.IsSubagent = true
		session.SubagentKind = trace.InternString(raw.SubagentKind)
		session.AgentType = trace.InternString(raw.AgentType)
		session.ParentToolUseID = raw.ParentToolUseID
		session.WorkflowRunID = raw.WorkflowRunID
		session.ParentExternalID = trace.InternString(raw.ParentExternalID)
	}

	turns := make([]trace.Turn, 0)
	var current *turnBuilder
	var parseErrors []trace.ParseError
	// Session-scoped message dedupe: maps a message.id to the invocation it
	// produced, so repeated lines of one API response are recognized both
	// within a turn (continuations) and across turns (rewind/fork history
	// replays, or a queued user prompt splitting one response over a flush).
	seenMsg := make(map[string]msgRef)
	// Session-scoped async completions: a background task (Workflow / background
	// Bash) returns a "launched in background" ack synchronously, then its real
	// result arrives much later — in a different turn — as a <task-notification>
	// correlated by the launching tool_use id. Collected across the whole stream,
	// then reconciled onto the launch in applyAsyncCompletions after flush().
	completions := make(map[string]asyncCompletion)

	flush := func() {
		if current == nil {
			return
		}
		t, errs := current.finish()
		turns = append(turns, t)
		parseErrors = append(parseErrors, errs...)
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
		if session.ExternalID == "" {
			session.ExternalID = trace.InternString(event.SessionID)
		}
		when := trace.ParseEventTime(event.Timestamp)
		shared.UpdateSessionTimes(&session, when)

		msg := decodeMessage(event.Message)
		if useID, comp, ok := parseTaskNotification(event, msg, when, rawEvent); ok {
			// Later inject (event.Type=="user") supersedes the earlier enqueue copy
			// (event.Type=="queue-operation"); both carry identical payloads.
			completions[useID] = comp
		}
		switch event.Type {
		case "user":
			// A canonical tool_result message carries only tool_result blocks and no
			// human text. Attach any results to the current turn, then — should the
			// same message also carry a human text block (non-canonical interleaving)
			// — fall through so that prompt still starts a new turn instead of being
			// dropped.
			if results := msg.toolResults(); len(results) > 0 {
				if current != nil {
					parseErrors = append(parseErrors, current.attachToolResults(results, when, rawEvent)...)
				} else {
					for _, result := range results {
						parseErrors = append(parseErrors, unmatchedToolResultError(sourceID, sourceRootID, rawEvent, result))
					}
				}
			}
			text := msg.text()
			// A background task's completion is injected as a system <task-notification>
			// user event: it opens a real turn (the agent acts on it) but its text is
			// machine XML, not a human prompt — and its payload is already reconciled
			// onto the launching tool call (parseTaskNotification). Keep the turn
			// boundary, drop the redundant XML from the user message.
			if strings.HasPrefix(text, "<task-notification>") {
				text = ""
			} else if text == "" || event.IsMeta || isInjectedContext(text) {
				continue
			}
			flush()
			current = newTurnBuilder(&session, sourceRootID, len(turns)+1, text, when, seenMsg)
		case "assistant":
			if current == nil {
				// Assistant content before any user turn (resumed/forked/sidechain
				// transcripts, or transcripts opening with assistant content). Build a
				// turn with an empty user message so leading invocations/tool calls and
				// their tokens are captured rather than silently dropped.
				current = newTurnBuilder(&session, sourceRootID, len(turns)+1, "", when, seenMsg)
			}
			current.recordAssistantMessage(msg, when, rawEvent)
		}
	}
	flush()

	applyAsyncCompletions(turns, completions, sourceRootID)

	if session.Status == trace.StatusUnknown && len(turns) > 0 {
		session.Status = trace.StatusCompleted
	}
	shared.FinalizeSession(&session, turns)

	result := ParseResult{Session: session, Turns: turns, ParseErrors: parseErrors}
	return result, nil
}

// msgRef locates the invocation a message.id maps to: the turn it lives in
// (trace.Turn.Index) and its position in that turn's Invocations slice.
type msgRef struct {
	turnIndex int
	invIndex  int
}

type turnBuilder struct {
	session      *trace.Session
	sourceRootID string
	turn         trace.Turn
	toolByUseID  map[string]int
	// pendingResults holds tool_results whose tool_use had not been seen yet when
	// the result line was read — out-of-order transcripts where a tool_result
	// precedes its tool_use (sidechain / streaming interleaving). They are
	// re-resolved as later tool_uses in the turn arrive; any still unmatched at
	// finish() become parse errors.
	pendingResults []pendingToolResult
	// seenMsg is shared across all turns of the session (owned by
	// ParseEvents). See recordAssistantMessage.
	seenMsg map[string]msgRef
}

type pendingToolResult struct {
	result   toolResult
	when     time.Time
	rawEvent source.RawEvent
}

func newTurnBuilder(session *trace.Session, sourceRootID string, index int, userText string, when time.Time, seenMsg map[string]msgRef) *turnBuilder {
	turnID := trace.TurnID(session.ID, index)
	return &turnBuilder{
		session:      session,
		sourceRootID: sourceRootID,
		turn: trace.Turn{
			ID:             turnID,
			Provider:       session.Provider,
			SessionID:      "",
			ProjectName:    session.ProjectName,
			ProjectPath:    session.ProjectPath,
			TranscriptPath: session.TranscriptPath,
			Index:          index,
			UserMessage:    userText,
			StartedAt:      when,
			EndedAt:        when,
			Status:         trace.StatusUnknown,
		},
		toolByUseID: make(map[string]int),
		seenMsg:     seenMsg,
	}
}

func (b *turnBuilder) recordAssistantMessage(msg message, when time.Time, rawEvent source.RawEvent) {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	if msg.text() != "" {
		b.turn.AssistantFinal = msg.text()
	}

	// Claude Code writes one JSONL line per content block of an API response,
	// every line repeating the same message.id with an identical usage object
	// and stop_reason (verified across real transcripts: zero variance between
	// lines of one id, ~55% of assistant lines are repeats). One message is one
	// invocation with one usage: a line whose id was already seen in THIS turn
	// is a continuation that only extends the invocation's end time and
	// contributes its content blocks; one seen in an EARLIER turn — rewind/fork
	// history replays, or a queued user prompt splitting a response over a
	// flush — gets a structural zero-token invocation, its usage having counted
	// once for the session. Messages without an id (synthetic or pre-id
	// transcript shapes) never hit the map and keep per-line counting.
	seen, dup := b.seenMsg[msg.ID]
	continuation := dup && seen.turnIndex == b.turn.Index
	toolUses := msg.toolUses()
	if continuation {
		inv := &b.turn.Invocations[seen.invIndex]
		inv.EndedAt = shared.LaterTime(inv.EndedAt, when)
		if len(toolUses) == 0 {
			return
		}
	}
	rawEventID := trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash())

	index := seen.invIndex
	if !continuation {
		index = len(b.turn.Invocations)
		var tokens trace.Tokens
		if !dup {
			tokens = trace.Tokens{
				Input:      msg.Usage.InputTokens,
				Output:     msg.Usage.OutputTokens,
				CacheRead:  msg.Usage.CacheReadInputTokens,
				CacheWrite: msg.Usage.CacheCreationInputTokens,
				// Clamped: a malformed line whose 1h tier exceeds the cache-creation
				// total would otherwise persist long > total, and every consumer
				// deriving the 5m subset as total - long would surface a negative.
				CacheWriteLong: min(msg.Usage.CacheCreation.Ephemeral1h, msg.Usage.CacheCreationInputTokens),
			}
			b.turn.Tokens.Add(tokens)
		}
		b.turn.Invocations = append(b.turn.Invocations, trace.Invocation{
			ID:         trace.InvocationID(b.turn.ID, index+1),
			Provider:   b.turn.Provider,
			TurnID:     b.turn.ID,
			Index:      index + 1,
			Model:      msg.Model,
			StartedAt:  when,
			EndedAt:    when,
			StopReason: msg.StopReason,
			Status:     invocationStatusFor(msg),
			Tokens:     tokens,
			RawEventID: rawEventID,
		})
		if msg.ID != "" {
			b.seenMsg[msg.ID] = msgRef{turnIndex: b.turn.Index, invIndex: index}
		}
	}
	invocationID := b.turn.Invocations[index].ID

	for _, partial := range toolUses {
		callIndex := len(b.turn.ToolCalls) + 1
		toolCall := trace.ToolCall{
			ID:            trace.ToolCallID(b.turn.ID, partial.UseID, callIndex),
			TurnID:        b.turn.ID,
			InvocationID:  invocationID,
			CallIndex:     callIndex,
			Kind:          shared.ClassifyToolKind(partial.Name),
			Name:          partial.Name,
			UseID:         partial.UseID,
			Input:         partial.Input,
			Status:        trace.StatusPending,
			StartedAt:     when,
			RawUseEventID: rawEventID,
		}
		if toolCall.Kind == trace.ToolKindMCP {
			toolCall.MCPServer, toolCall.MCPTool = shared.SplitMCPName(toolCall.Name)
		}
		if partial.UseID != "" {
			b.toolByUseID[partial.UseID] = len(b.turn.ToolCalls)
		}
		b.turn.ToolCalls = append(b.turn.ToolCalls, toolCall)
	}
}

func (b *turnBuilder) attachToolResults(results []toolResult, when time.Time, rawEvent source.RawEvent) []trace.ParseError {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	var parseErrors []trace.ParseError
	for _, result := range results {
		if index := b.resolveToolCall(result.UseID); index >= 0 {
			b.applyResult(index, result, when, rawEvent)
			continue
		}
		// Not resolvable now. A result whose use id simply has not been recorded yet
		// is an out-of-order forward reference (the tool_use line comes later) —
		// buffer it and re-resolve when that tool_use arrives. A blank or
		// already-resolved use id is genuinely unmatched/duplicate.
		if result.UseID != "" {
			if _, seen := b.toolByUseID[result.UseID]; !seen {
				b.pendingResults = append(b.pendingResults, pendingToolResult{result: result, when: when, rawEvent: rawEvent})
				continue
			}
		}
		parseErrors = append(parseErrors, unmatchedToolResultError(trace.SourceID(b.turn.Provider), b.sourceRootID, rawEvent, result))
	}
	return parseErrors
}

// applyResult writes a tool_result's output, status, and timing onto the
// resolved tool call.
func (b *turnBuilder) applyResult(index int, result toolResult, when time.Time, rawEvent source.RawEvent) {
	call := &b.turn.ToolCalls[index]
	call.Output = result.Output
	call.OutputBytes = int64(len(result.Output))
	call.EndedAt = when
	call.RawResultEventID = trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash())
	if !call.StartedAt.IsZero() && when.After(call.StartedAt) {
		call.DurationMs = when.Sub(call.StartedAt).Milliseconds()
	}
	if result.IsError {
		call.Status = trace.StatusFailed
	} else {
		call.Status = trace.StatusSuccess
	}
}

// resolvePendingResults re-attempts the buffered out-of-order tool_results,
// attaching any whose tool_use has since been recorded and keeping the rest
// buffered. Called once from finish(), after every tool_use in the turn exists.
func (b *turnBuilder) resolvePendingResults() {
	kept := b.pendingResults[:0]
	for _, pr := range b.pendingResults {
		if index := b.resolveToolCall(pr.result.UseID); index >= 0 {
			b.applyResult(index, pr.result, pr.when, pr.rawEvent)
			continue
		}
		kept = append(kept, pr)
	}
	b.pendingResults = kept
}

func unmatchedToolResultError(sourceID, sourceRootID string, rawEvent source.RawEvent, result toolResult) trace.ParseError {
	message := "unmatched or duplicate tool_result"
	if result.UseID == "" {
		message = "unmatched tool_result without use_id"
	}
	return trace.ParseError{
		SourceID:      sourceID,
		SourceRootID:  sourceRootID,
		SourceFile:    rawEvent.SourceFile,
		LineNo:        rawEvent.LineNo,
		RawEventID:    trace.RawEventID(sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawEvent.Hash()),
		Message:       message,
		ParserVersion: ParserVersion,
	}
}

// resolveToolCall maps a tool_result to the tool_call it completes. It uses an
// explicit use-id match when one is present, falling back to the next unresolved
// pending tool_call only when the result itself has no use id. Returns -1 when
// no candidate exists.
func (b *turnBuilder) resolveToolCall(useID string) int {
	if useID != "" {
		if index, ok := b.toolByUseID[useID]; ok {
			// Known use id: only a still-pending call accepts this result. A
			// duplicate result for an already-resolved call is dropped (return -1)
			// rather than clobbering it or, via the fallback below, attaching to an
			// unrelated pending call.
			if index >= 0 && index < len(b.turn.ToolCalls) && b.turn.ToolCalls[index].Status == trace.StatusPending {
				return index
			}
			return -1
		}
		return -1
	}
	for i := range b.turn.ToolCalls {
		if b.turn.ToolCalls[i].Status == trace.StatusPending {
			return i
		}
	}
	return -1
}

func (b *turnBuilder) finish() (trace.Turn, []trace.ParseError) {
	// Resolve out-of-order tool_results one final time now that every tool_use in
	// the turn has been recorded (a single O(pending) pass; resolving incrementally
	// after each assistant message would be O(pending × messages)). Whatever stays
	// buffered never found its tool_use and is a genuinely unmatched result.
	b.resolvePendingResults()
	var parseErrors []trace.ParseError
	for _, pr := range b.pendingResults {
		parseErrors = append(parseErrors, unmatchedToolResultError(trace.SourceID(b.turn.Provider), b.sourceRootID, pr.rawEvent, pr.result))
	}
	turn := b.turn
	if !turn.StartedAt.IsZero() && !turn.EndedAt.IsZero() && turn.EndedAt.After(turn.StartedAt) {
		turn.DurationMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
	}
	turn.InvocationCount = len(turn.Invocations)
	turn.ToolCallCount = len(turn.ToolCalls)
	turn.Status = shared.StatusForTurn(turn)
	turn.Components = components.FromTools(turn.ID, turn.ToolCalls)
	return turn, parseErrors
}

type envelope struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Timestamp string          `json:"timestamp"`
	IsMeta    bool            `json:"isMeta"`
	Message   json.RawMessage `json:"message"`
	// Content carries the payload of a queue-operation event (a task-notification
	// enqueue), which has no nested message. User-injected events keep their text
	// under Message instead.
	Content json.RawMessage `json:"content"`
}

// topLevelText decodes a queue-operation event's top-level string content,
// returning "" when it is absent or not a JSON string.
func (e envelope) topLevelText() string {
	if len(e.Content) == 0 || e.Content[0] != '"' {
		return ""
	}
	var s string
	if err := json.Unmarshal(e.Content, &s); err != nil {
		return ""
	}
	return s
}

type message struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model"`
	Usage      usage           `json:"usage"`
	StopReason string          `json:"stop_reason"`

	blocks   []contentBlock
	textBody string
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// CacheCreation breaks the cache-creation total into TTL tiers. Anthropic
	// bills ephemeral_1h higher than ephemeral_5m; the 5m subset is the total
	// minus the 1h tier, so only the 1h tier needs to be carried.
	CacheCreation struct {
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type partialToolUse struct {
	UseID string
	Name  string
	Input string
}

type toolResult struct {
	UseID   string
	Output  string
	IsError bool
}

func decodeMessage(raw json.RawMessage) message {
	if len(raw) == 0 {
		return message{}
	}
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return message{}
	}
	msg.decodeContent()
	return msg
}

func (m *message) decodeContent() {
	if len(m.Content) == 0 {
		return
	}
	if m.Content[0] == '"' {
		var text string
		if err := json.Unmarshal(m.Content, &text); err == nil {
			m.textBody = strings.TrimSpace(text)
			return
		}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return
	}
	m.blocks = blocks
	if len(blocks) == 0 {
		return
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" {
			trimmed := strings.TrimSpace(block.Text)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	if len(parts) > 0 {
		m.textBody = strings.Join(parts, "\n\n")
	}
}

func isInjectedContext(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "<command-name>") ||
		strings.HasPrefix(trimmed, "<command-args>") ||
		strings.HasPrefix(trimmed, "[Request interrupted") {
		return true
	}
	return shared.IsLocalCommandInjection(trimmed, lower)
}

func (m *message) text() string {
	return m.textBody
}

func (m *message) toolUses() []partialToolUse {
	if len(m.blocks) == 0 {
		return nil
	}
	tools := make([]partialToolUse, 0)
	for _, block := range m.blocks {
		if block.Type != "tool_use" {
			continue
		}
		input := string(block.Input)
		if input == "" || input == "null" {
			input = "{}"
		}
		tools = append(tools, partialToolUse{
			UseID: block.ID,
			Name:  block.Name,
			Input: input,
		})
	}
	return tools
}

func (m *message) toolResults() []toolResult {
	if len(m.blocks) == 0 {
		return nil
	}
	results := make([]toolResult, 0)
	for _, block := range m.blocks {
		if block.Type != "tool_result" {
			continue
		}
		results = append(results, toolResult{
			UseID:   block.ToolUseID,
			Output:  shared.DecodeContentText(block.Content, true),
			IsError: block.IsError,
		})
	}
	return results
}

// asyncCompletion is a background task's out-of-band terminal result, delivered
// as a <task-notification> long after the launching tool call's synchronous
// "launched in background" acknowledgement and correlated to it by tool_use id.
type asyncCompletion struct {
	status string
	output string
	when   time.Time
	raw    source.RawEvent
}

// parseTaskNotification extracts the completion a <task-notification> event
// reports, correlated to the originating tool_use id. The notification arrives
// either as an injected user message (text under message) or as the enqueue
// queue-operation (top-level content); both carry the same tagged payload.
func parseTaskNotification(event envelope, msg message, when time.Time, rawEvent source.RawEvent) (string, asyncCompletion, bool) {
	text := msg.text()
	if !strings.HasPrefix(text, "<task-notification>") {
		text = event.topLevelText()
	}
	if !strings.HasPrefix(text, "<task-notification>") {
		return "", asyncCompletion{}, false
	}
	useID := notificationTag(text, "tool-use-id")
	if useID == "" {
		return "", asyncCompletion{}, false
	}
	// The full machine-readable result is authoritative; fall back to the human
	// summary for tasks (e.g. background Bash) that report no inline result.
	output := notificationTag(text, "result")
	if output == "" {
		output = notificationTag(text, "summary")
	}
	return useID, asyncCompletion{
		status: notificationTag(text, "status"),
		output: output,
		when:   when,
		raw:    rawEvent,
	}, true
}

// notificationTag returns the trimmed text of the first <tag>…</tag> in s, or "".
func notificationTag(s, tag string) string {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(s[i:], close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i : i+j])
}

// applyAsyncCompletions reconciles background-task launches with their real
// outcomes. A Workflow / background-Bash call's synchronous tool_result is only a
// "launched in background" ack; the true result arrives later as a
// <task-notification>. The terminal fates a recovering agent must tell apart:
//   - a completion notification supersedes the ack (real output + status + end
//     time) — the task finished (success/failed), or was killed (interrupted);
//   - no notification but a successful TaskStop on its task id → interrupted
//     (deliberately stopped);
//   - neither — launched but never completed or stopped within the transcript →
//     active (in-flight / abandoned).
// Without this, every ack is mistaken for a successful result.
func applyAsyncCompletions(turns []trace.Turn, completions map[string]asyncCompletion, sourceRootID string) {
	stopped := stoppedTaskIDs(turns)
	dirty := make(map[int]bool)
	for ti := range turns {
		for ci := range turns[ti].ToolCalls {
			call := &turns[ti].ToolCalls[ci]
			if comp, ok := completions[call.UseID]; ok {
				status := asyncCompletionStatus(comp.status)
				call.Output = comp.output
				call.OutputBytes = int64(len(comp.output))
				call.Status = status
				call.RawResultEventID = trace.RawEventID(sourceRootID, comp.raw.SourceFile, comp.raw.LineNo, comp.raw.Hash())
				// Only a terminal notification (success/failed/interrupted) ends the
				// call; a non-terminal "running" progress update leaves it active with
				// no fabricated end time.
				if status != trace.StatusActive {
					call.EndedAt = comp.when
					if !call.StartedAt.IsZero() && comp.when.After(call.StartedAt) {
						call.DurationMs = comp.when.Sub(call.StartedAt).Milliseconds()
					}
				}
				dirty[ti] = true
			} else if call.Status == trace.StatusSuccess && isAsyncLaunchAck(call.Output) {
				// Require a parseable task id: a genuine launch ack always carries one
				// ("Task ID: X" / "with ID: X"). Without it, the phrase match alone is
				// not enough to downgrade a successful call — it may be a foreground
				// tool whose output merely begins with that text.
				if id := asyncLaunchTaskID(call.Output); id != "" {
					if stopped[id] {
						call.Status = trace.StatusInterrupted // deliberately stopped (TaskStop)
					} else {
						call.Status = trace.StatusActive // launched, never completed or stopped
					}
					dirty[ti] = true
				}
			}
		}
	}
	for ti := range dirty {
		turns[ti].Status = shared.StatusForTurn(turns[ti])
	}
}

// stoppedTaskIDs collects the task ids a successful TaskStop killed, so an async
// launch for one can be classified as deliberately interrupted rather than merely
// in-flight.
func stoppedTaskIDs(turns []trace.Turn) map[string]bool {
	out := make(map[string]bool)
	for ti := range turns {
		for ci := range turns[ti].ToolCalls {
			call := &turns[ti].ToolCalls[ci]
			if call.Name != "TaskStop" || call.Status != trace.StatusSuccess {
				continue
			}
			var in struct {
				TaskID string `json:"task_id"`
			}
			if json.Unmarshal([]byte(call.Input), &in) == nil && in.TaskID != "" {
				out[in.TaskID] = true
			}
		}
	}
	return out
}

// asyncLaunchTaskID extracts the background task id an async launch ack reports
// ("Workflow launched in background. Task ID: X" / "Command running in background
// with ID: X"), or "" when absent.
func asyncLaunchTaskID(output string) string {
	for _, marker := range []string{"Task ID: ", "with ID: "} {
		if _, rest, ok := strings.Cut(output, marker); ok {
			if j := strings.IndexFunc(rest, func(r rune) bool {
				return r == ' ' || r == '\n' || r == '\t' || r == '.'
			}); j >= 0 {
				return rest[:j]
			}
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// asyncCompletionStatus maps a <task-notification> <status> to a tool-call status.
// Claude Code's task-status vocabulary is pending/running/completed/failed/killed.
// "killed" is a terminal stop (TaskStop or abort) → interrupted; pending/running
// (not yet terminal) and any unknown value → active, conservatively not treated as
// a usable result.
func asyncCompletionStatus(notificationStatus string) string {
	switch notificationStatus {
	case "completed":
		return trace.StatusSuccess
	case "failed":
		return trace.StatusFailed
	case "killed":
		return trace.StatusInterrupted
	default:
		return trace.StatusActive
	}
}

// isAsyncLaunchAck reports whether a tool_result is the synchronous "launched in
// background" acknowledgement of an async tool call (a Workflow, or a Bash run
// with run_in_background) rather than its real result. Claude Code phrases the
// two cases distinctly.
func isAsyncLaunchAck(output string) bool {
	out := strings.TrimSpace(output)
	return strings.HasPrefix(out, "Workflow launched in background") ||
		strings.HasPrefix(out, "Command running in background")
}

func invocationStatusFor(msg message) string {
	switch msg.StopReason {
	case "":
		return trace.StatusUnknown
	case "end_turn", "stop_sequence":
		return trace.StatusSuccess
	case "tool_use":
		return trace.StatusSuccess
	case "max_tokens":
		return trace.StatusInterrupted
	default:
		return trace.StatusUnknown
	}
}

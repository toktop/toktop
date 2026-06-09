package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"time"

	"toktop.unceas.dev/internal/parser/components"
	"toktop.unceas.dev/internal/parser/shared"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

const ParserVersion = "claudecode/2"

type ParseResult struct {
	Session     trace.Session
	Turns       []trace.Turn
	ParseErrors []trace.ParseError
}

func ParseSession(ctx context.Context, raw source.RawSession) (ParseResult, error) {
	return ParseEvents(ctx, raw, raw.Events())
}

func ParseEvents(ctx context.Context, raw source.RawSession, events iter.Seq2[source.RawEvent, error]) (ParseResult, error) {
	sourceRootID := trace.SourceRootID(trace.SourceID(raw.Provider), raw.SourceRoot)
	session := trace.Session{
		Provider:       trace.InternString(raw.Provider),
		ProjectName:    trace.InternString(raw.ProjectName),
		ProjectPath:    raw.ProjectPath,
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
		t := current.finish()
		turns = append(turns, t)
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
				SourceID:      trace.SourceID(raw.Provider),
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
		switch event.Type {
		case "user":
			// A canonical tool_result message carries only tool_result blocks and no
			// human text. Attach any results to the current turn, then — should the
			// same message also carry a human text block (non-canonical interleaving)
			// — fall through so that prompt still starts a new turn instead of being
			// dropped.
			if results := msg.toolResults(); len(results) > 0 && current != nil {
				current.attachToolResults(results, when)
			}
			text := msg.text()
			if text == "" {
				continue
			}
			flush()
			current = newTurnBuilder(&session, sourceRootID, len(turns)+1, text, when)
		case "assistant":
			if current == nil {
				// Assistant content before any user turn (resumed/forked/sidechain
				// transcripts, or transcripts opening with assistant content). Build a
				// turn with an empty user message so leading invocations/tool calls and
				// their tokens are captured rather than silently dropped.
				current = newTurnBuilder(&session, sourceRootID, len(turns)+1, "", when)
			}
			current.recordAssistantMessage(msg, when, rawEvent)
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
	session      *trace.Session
	sourceRootID string
	turn         trace.Turn
	toolByUseID  map[string]int
}

func newTurnBuilder(session *trace.Session, sourceRootID string, index int, userText string, when time.Time) *turnBuilder {
	turnID := trace.TurnID(trace.SessionID(sourceRootID, session.TranscriptPath), index)
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
	}
}

func (b *turnBuilder) recordAssistantMessage(msg message, when time.Time, rawEvent source.RawEvent) {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	if msg.text() != "" {
		b.turn.AssistantFinal = msg.text()
	}

	invocationIndex := len(b.turn.Invocations) + 1
	invocationID := trace.InvocationID(b.turn.ID, invocationIndex)
	rawHash := rawEvent.Hash()
	rawEventID := trace.RawEventID(b.sourceRootID, rawEvent.SourceFile, rawEvent.LineNo, rawHash)
	tokens := trace.Tokens{
		Input:      msg.Usage.InputTokens,
		Output:     msg.Usage.OutputTokens,
		CacheRead:  msg.Usage.CacheReadInputTokens,
		CacheWrite: msg.Usage.CacheCreationInputTokens,
	}
	invocation := trace.Invocation{
		ID:         invocationID,
		Provider:   b.turn.Provider,
		TurnID:     b.turn.ID,
		Index:      invocationIndex,
		Model:      msg.Model,
		StartedAt:  when,
		EndedAt:    when,
		StopReason: msg.StopReason,
		Status:     invocationStatusFor(msg),
		Tokens:     tokens,
		RawEventID: rawEventID,
	}
	b.turn.Invocations = append(b.turn.Invocations, invocation)
	b.turn.Tokens.Add(tokens)

	for _, partial := range msg.toolUses() {
		callIndex := len(b.turn.ToolCalls) + 1
		toolCall := trace.ToolCall{
			ID:            trace.ToolCallID(b.turn.ID, partial.UseID, callIndex),
			TurnID:        b.turn.ID,
			InvocationID:  invocation.ID,
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

func (b *turnBuilder) attachToolResults(results []toolResult, when time.Time) {
	b.turn.EndedAt = shared.LaterTime(b.turn.EndedAt, when)
	for _, result := range results {
		index := b.resolveToolCall(result.UseID)
		if index < 0 {
			continue
		}
		call := &b.turn.ToolCalls[index]
		call.Output = result.Output
		call.OutputBytes = int64(len(result.Output))
		call.EndedAt = when
		if !call.StartedAt.IsZero() && when.After(call.StartedAt) {
			call.DurationMs = when.Sub(call.StartedAt).Milliseconds()
		}
		if result.IsError {
			call.Status = trace.StatusFailed
		} else {
			call.Status = trace.StatusSuccess
		}
	}
}

// resolveToolCall maps a tool_result to the tool_call it completes. It prefers a
// use-id match, falling back to the next unresolved pending tool_call (in call
// order) when the use id is empty or unknown, so results from malformed/partial
// transcripts are not silently dropped. Returns -1 when no candidate exists.
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
	}
	for i := range b.turn.ToolCalls {
		if b.turn.ToolCalls[i].Status == trace.StatusPending {
			return i
		}
	}
	return -1
}

func (b *turnBuilder) finish() trace.Turn {
	turn := b.turn
	if !turn.StartedAt.IsZero() && !turn.EndedAt.IsZero() && turn.EndedAt.After(turn.StartedAt) {
		turn.DurationMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
	}
	turn.InvocationCount = len(turn.Invocations)
	turn.ToolCallCount = len(turn.ToolCalls)
	turn.Status = shared.StatusForTurn(turn)
	turn.Components = components.FromTools(turn.ID, turn.ToolCalls)
	return turn
}

type envelope struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type message struct {
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

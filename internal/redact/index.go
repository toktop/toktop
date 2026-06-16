package redact

import (
	"context"
	"fmt"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/trace"
)

type Policy struct {
	Enabled bool
}

var Disabled = Policy{}

func (p Policy) ApplyToIndex(ctx context.Context, idx *trace.Index) error {
	if !p.Enabled || idx == nil {
		return nil
	}

	if _, err := collector.SafeMapErr(ctx, idx.Turns, func(t *trace.Turn) (struct{}, error) {
		redactTurn(t)
		return struct{}{}, nil
	}); err != nil {
		return fmt.Errorf("redact turns: %w", err)
	}
	// Session titles are persisted-as-served (sessions.title, /v1/sessions, CLI) just
	// like turn text: a claude-code ai-title is model-generated FROM the conversation
	// and a custom-title / codex thread_name is user-authored, so a secret can land in
	// one. Redact both the parser-projected Title and the out-of-band SessionTitles
	// overlay (codex).
	for i := range idx.Sessions {
		idx.Sessions[i].Title = applyKeepEmpty(idx.Sessions[i].Title)
	}
	p.ApplyToTitleMap(idx.SessionTitles)
	return nil
}

// ApplyToTitleMap redacts the out-of-band session-title overlay in place. Exposed
// separately so the codex live / trailing title rounds — which sink a title-only
// Index without going through ApplyToIndex's turn pass — can redact titles without
// re-redacting already-redacted turns.
func (p Policy) ApplyToTitleMap(titles map[string]string) {
	if !p.Enabled {
		return
	}
	for id, title := range titles {
		titles[id] = applyKeepEmpty(title)
	}
}

// redactTurn rewrites the projected text fields in place so the values that are
// persisted-as-served (and serialized straight into the API JSON) carry redacted
// text, never raw secrets. The store columns, the read handlers, BuildTimeline,
// and the external-content FTS index all read these same fields, so redacting at
// the source covers every consumer.
func redactTurn(turn *trace.Turn) {
	turn.UserMessage = applyKeepEmpty(turn.UserMessage)
	turn.AssistantFinal = applyKeepEmpty(turn.AssistantFinal)
	for i := range turn.ToolCalls {
		redactToolCall(&turn.ToolCalls[i])
	}
}

func redactToolCall(call *trace.ToolCall) {
	call.Input = applyKeepEmpty(call.Input)
	call.Output = applyKeepEmpty(call.Output)
	call.Error = applyKeepEmpty(call.Error)
}

func applyKeepEmpty(text string) string {
	if text == "" {
		return ""
	}
	return Apply(text)
}

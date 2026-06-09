package redact

import (
	"context"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/trace"
)

type Policy struct {
	Enabled bool
}

var Disabled = Policy{}

func (p Policy) ApplyToIndex(ctx context.Context, idx *trace.Index) {
	if !p.Enabled || idx == nil {
		return
	}

	_, _ = collector.SafeMapErr(ctx, idx.Turns, func(t *trace.Turn) (struct{}, error) {
		redactTurn(t)
		return struct{}{}, nil
	})
	_, _ = collector.SafeMapErr(ctx, idx.SubagentRuns, func(r *trace.SubagentRun) (struct{}, error) {
		for j := range r.ToolCalls {
			redactToolCall(&r.ToolCalls[j])
		}
		return struct{}{}, nil
	})
	// Tool outputs and context-event evidence are persisted-as-served too
	// (insertToolOutputs/insertContextEvents write these raw), so they must be
	// redacted at the same source. ContentText is raw tool output; Evidence is
	// usually a short label but can still echo free text.
	_, _ = collector.SafeMapErr(ctx, idx.ToolOutputs, func(o *trace.ToolOutput) (struct{}, error) {
		o.ContentText = applyKeepEmpty(o.ContentText)
		return struct{}{}, nil
	})
	_, _ = collector.SafeMapErr(ctx, idx.ContextEvents, func(e *trace.ContextEvent) (struct{}, error) {
		e.Evidence = applyKeepEmpty(e.Evidence)
		return struct{}{}, nil
	})
}

// redactTurn rewrites the projected text fields in place so the values that are
// persisted-as-served (and serialized straight into the API JSON) carry redacted
// text, never raw secrets. The store columns, the read handlers, BuildTimeline,
// and the external-content FTS index all read these same fields, so redacting at
// the source covers every consumer.
func redactTurn(turn *trace.Turn) {
	turn.UserMessage = applyKeepEmpty(turn.UserMessage)
	turn.AssistantFinal = applyKeepEmpty(turn.AssistantFinal)
	turn.Summary = applyKeepEmpty(turn.Summary)
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
	return Apply(text).Redacted
}

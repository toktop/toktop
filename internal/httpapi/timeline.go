package httpapi

import (
	"slices"
	"time"

	"toktop.unceas.dev/internal/trace"
)

type TimelineEntry struct {
	At         time.Time `json:"at,omitzero"`
	Kind       string    `json:"kind"`
	Label      string    `json:"label"`
	Detail     string    `json:"detail,omitzero"`
	DurationMs int64     `json:"duration_ms,omitzero"`
	Status     string    `json:"status,omitzero"`
	// Tokens is an OBSERVED model-accounting count (invocations, subagent runs).
	// TokenEstimate is a derived/inferred figure (context events) kept in its own
	// field with its Confidence, so a consumer never mistakes an estimate for an
	// observed count summed in the same column.
	Tokens        int              `json:"tokens,omitzero"`
	TokenEstimate int              `json:"token_estimate,omitzero"`
	Confidence    trace.Confidence `json:"confidence,omitzero"`
}

type TimelineResponse struct {
	Turn    trace.Turn      `json:"turn"`
	Entries []TimelineEntry `json:"entries"`
}

func BuildTimeline(turn trace.Turn) TimelineResponse {
	entries := make([]TimelineEntry, 0, len(turn.Invocations)+len(turn.ToolCalls)+len(turn.ContextEvents)+len(turn.SubagentRuns)+2)
	if !turn.StartedAt.IsZero() {
		entries = append(entries, TimelineEntry{
			At:     turn.StartedAt,
			Kind:   "user_prompt",
			Label:  "user prompt",
			Detail: truncate(turn.UserMessage, 200),
		})
	}
	for _, inv := range turn.Invocations {
		entries = append(entries, TimelineEntry{
			At:         inv.StartedAt,
			Kind:       "invocation",
			Label:      "invocation " + inv.Model,
			Detail:     inv.StopReason,
			DurationMs: inv.LatencyMs,
			Status:     inv.Status,
			Tokens:     inv.Tokens.Input + inv.Tokens.Output,
		})
	}
	for _, call := range turn.ToolCalls {
		label := call.Name
		if call.Kind == trace.ToolKindMCP {
			label = "mcp:" + call.MCPServer + "/" + call.MCPTool
		}
		entries = append(entries, TimelineEntry{
			At:         call.StartedAt,
			Kind:       "tool_call",
			Label:      label,
			Detail:     truncate(call.Input, 160),
			DurationMs: call.DurationMs,
			Status:     call.Status,
		})
	}
	for _, ev := range turn.ContextEvents {
		entries = append(entries, TimelineEntry{
			Kind:          "context_event",
			Label:         ev.ComponentType + ":" + ev.ComponentName,
			Detail:        ev.Phase,
			TokenEstimate: ev.TokenEstimate,
			Confidence:    ev.Confidence,
		})
	}
	for _, sub := range turn.SubagentRuns {
		entries = append(entries, TimelineEntry{
			At:         sub.StartedAt,
			Kind:       "subagent_run",
			Label:      "subagent " + sub.AgentName,
			Detail:     sub.Model,
			DurationMs: sub.DurationMs,
			Status:     sub.Status,
			Tokens:     sub.Tokens.Input + sub.Tokens.Output,
		})
	}
	if !turn.EndedAt.IsZero() && turn.AssistantFinal != "" {
		entries = append(entries, TimelineEntry{
			At:     turn.EndedAt,
			Kind:   "final_response",
			Label:  "assistant reply",
			Detail: truncate(turn.AssistantFinal, 200),
			Status: turn.Status,
		})
	}
	slices.SortStableFunc(entries, func(a, b TimelineEntry) int {
		az, bz := a.At.IsZero(), b.At.IsZero()
		switch {
		case az && bz:
			return 0
		case az:
			return 1
		case bz:
			return -1
		default:
			return a.At.Compare(b.At)
		}
	})
	return TimelineResponse{Turn: turn, Entries: entries}
}

func truncate(s string, n int) string {
	// Operate on runes, not bytes: a byte slice can split a multibyte UTF-8
	// rune (emoji, accents, CJK) and emit an invalid trailing sequence.
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

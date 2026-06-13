package httpapi

import (
	"slices"
	"time"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

type TimelineEntry struct {
	At         time.Time `json:"at,omitzero"`
	Kind       string    `json:"kind"`
	Label      string    `json:"label"`
	Detail     string    `json:"detail,omitzero"`
	DurationMs int64     `json:"duration_ms,omitzero"`
	Status     string    `json:"status,omitzero"`
	// Tokens is an OBSERVED model-accounting count from invocations.
	Tokens int `json:"tokens,omitzero"`
}

type TimelineResponse struct {
	Turn    trace.Turn      `json:"turn"`
	Entries []TimelineEntry `json:"entries"`
}

func BuildTimeline(turn trace.Turn) TimelineResponse {
	entries := make([]TimelineEntry, 0, len(turn.Invocations)+len(turn.ToolCalls)+2)
	if !turn.StartedAt.IsZero() {
		entries = append(entries, TimelineEntry{
			At:     turn.StartedAt,
			Kind:   "user_prompt",
			Label:  "user prompt",
			Detail: textutil.Truncate(turn.UserMessage, 200),
		})
	}
	for _, inv := range turn.Invocations {
		entries = append(entries, TimelineEntry{
			At:     inv.StartedAt,
			Kind:   "invocation",
			Label:  "invocation " + inv.Model,
			Detail: inv.StopReason,
			Status: inv.Status,
			Tokens: inv.Tokens.Input + inv.Tokens.Output,
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
			Detail:     textutil.Truncate(call.Input, 160),
			DurationMs: call.DurationMs,
			Status:     call.Status,
		})
	}
	if !turn.EndedAt.IsZero() && turn.AssistantFinal != "" {
		entries = append(entries, TimelineEntry{
			At:     turn.EndedAt,
			Kind:   "final_response",
			Label:  "assistant reply",
			Detail: textutil.Truncate(turn.AssistantFinal, 200),
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

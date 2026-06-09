// Package shared holds parsing helpers common to every provider parser, so the
// per-provider parsers (internal/parser/claudecode, internal/parser/codex) call
// one definition instead of carrying byte-identical copies that can silently
// drift apart.
package shared

import (
	"encoding/json"
	"strings"
	"time"

	"toktop.unceas.dev/internal/trace"
)

// ClassifyToolKind reports whether a tool name is an MCP tool (mcp__server__tool)
// or a built-in tool.
func ClassifyToolKind(name string) string {
	if strings.HasPrefix(name, "mcp__") && strings.Count(name, "__") >= 2 {
		return trace.ToolKindMCP
	}
	return trace.ToolKindBuiltin
}

// SplitMCPName splits an mcp__server__tool name into its server and tool parts,
// returning empty strings when the name is not a well-formed MCP name.
func SplitMCPName(name string) (server, tool string) {
	trimmed := strings.TrimPrefix(name, "mcp__")
	parts := strings.SplitN(trimmed, "__", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// UpdateSessionTimes widens a session's [StartedAt, EndedAt] window to include
// when (a no-op for the zero time).
func UpdateSessionTimes(session *trace.Session, when time.Time) {
	if when.IsZero() {
		return
	}
	if session.StartedAt.IsZero() || when.Before(session.StartedAt) {
		session.StartedAt = when
	}
	if when.After(session.EndedAt) {
		session.EndedAt = when
	}
}

// LaterTime returns the later of a and b, treating a zero b as "no upper bound".
func LaterTime(a, b time.Time) time.Time {
	if b.IsZero() || a.After(b) {
		return a
	}
	return b
}

// StatusForTurn derives a turn's status: failed if any tool call failed, success
// if the turn produced an assistant final or any invocations, else unknown.
func StatusForTurn(turn trace.Turn) string {
	for _, call := range turn.ToolCalls {
		if call.Status == trace.StatusFailed {
			return trace.StatusFailed
		}
	}
	if turn.AssistantFinal != "" || len(turn.Invocations) > 0 {
		return trace.StatusSuccess
	}
	return trace.StatusUnknown
}

// FinalizeSession stamps the parent session id onto every turn and its
// invocations and tool calls, stamps each turn's id onto its components, then
// folds the turn's tool-call count and tokens into the session aggregates. Both
// provider parsers call this instead of carrying their own post-parse loop —
// those copies had already drifted (only one stamped Components[*].TurnID).
func FinalizeSession(session *trace.Session, turns []trace.Turn) {
	for i := range turns {
		turns[i].SessionID = session.ID
		for j := range turns[i].Invocations {
			turns[i].Invocations[j].SessionID = session.ID
		}
		for j := range turns[i].ToolCalls {
			turns[i].ToolCalls[j].SessionID = session.ID
		}
		for k := range turns[i].Components {
			turns[i].Components[k].TurnID = turns[i].ID
		}
		session.TurnCount++
		session.ToolCallCount += len(turns[i].ToolCalls)
		session.Tokens.Add(turns[i].Tokens)
	}
}

// DecodeContentText extracts text from a content field that is either a JSON
// string or a JSON array of blocks carrying a "text" member (array parts are
// trimmed, blanks dropped, joined with a blank line). fallbackRaw controls the
// non-string/non-array (or decode-failure) result: false yields "", true yields
// the trimmed raw bytes — the codex vs claudecode behavior, preserved.
func DecodeContentText(raw json.RawMessage, fallbackRaw bool) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return strings.TrimSpace(text)
		}
	case '[':
		var blocks []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blocks); err == nil {
			parts := make([]string, 0, len(blocks))
			for _, block := range blocks {
				if trimmed := strings.TrimSpace(block.Text); trimmed != "" {
					parts = append(parts, trimmed)
				}
			}
			return strings.Join(parts, "\n\n")
		}
	}
	if fallbackRaw {
		return strings.TrimSpace(string(raw))
	}
	return ""
}

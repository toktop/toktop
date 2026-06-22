// Package shared holds parsing helpers common to every provider parser, so the
// per-provider parsers (internal/parser/claudecode, internal/parser/codex) call
// one definition instead of carrying byte-identical copies that can silently
// drift apart.
package shared

import (
	"encoding/json"
	"path"
	"strings"
	"time"

	"toktop.unceas.dev/internal/trace"
)

// LastPathSegment returns the final element of a working-directory path, or
// "unknown" for "."/"/". Shared by provider parsers deriving a session/turn
// ProjectName from a cwd.
func LastPathSegment(dir string) string {
	base := path.Base(dir)
	if base == "." || base == "/" {
		return "unknown"
	}
	return base
}

// OutputText decodes a tool-output value that may be a JSON string, an array of
// content blocks (joined via DecodeContentText), or another JSON value (trimmed).
func OutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"', '[':
		return DecodeContentText(raw, false)
	default:
		return strings.TrimSpace(string(raw))
	}
}

// ToolInputJSON renders a tool call's input as a compact JSON string, defaulting
// to "{}" for absent/empty/null input. A JSON-string input is unwrapped (codex
// emits some args as a quoted string); object inputs (claude-code/opencode) pass
// through unchanged. One definition of the neutral tool-input projection rule.
func ToolInputJSON(raw json.RawMessage) string {
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
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}

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
// Only StatusFailed propagates up to the turn: a StatusRejected call (a user
// declining a plan/prompt) is deliberately NOT a turn failure — the assistant
// still did its work (e.g. presented the declined plan), so such a turn derives by
// its content (success when it produced output). Rejection stays a tool-call-grain
// fact, queryable via tool_calls.status; do not add a StatusRejected case here.
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

// ResolveTurnStatus reconciles a turn's explicitly-set status with the status
// derived from its tool calls and activity. A status StatusForTurn can never itself
// produce — Interrupted or Active, set by a parser from an explicit abort or
// async-launch signal — is authoritative and kept. Otherwise the derived status
// applies, with a derived Failed always winning (a failed tool call fails the turn
// even if it also produced an assistant message). This is the single "explicit
// status wins over derived" rule shared by both provider parsers; do not reintroduce
// a per-status guard at a call site.
func ResolveTurnStatus(turn trace.Turn) string {
	switch turn.Status {
	case trace.StatusInterrupted, trace.StatusActive:
		return turn.Status
	}
	if derived := StatusForTurn(turn); turn.Status == trace.StatusUnknown || derived == trace.StatusFailed {
		return derived
	}
	return turn.Status
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

// IsLocalCommandInjection reports whether a message is one of the local-command /
// slash-command sentinels both providers inject as non-prompt context: the
// <local-command-…> wrapper, a /model directive, or the "do not respond … local
// commands" caveat. trimmed is the TrimSpace'd text; lower is its ToLower form,
// passed in because callers already compute it for their provider-specific checks.
func IsLocalCommandInjection(trimmed, lower string) bool {
	if strings.HasPrefix(trimmed, "<local-command-") ||
		strings.HasPrefix(trimmed, "</local-command-") ||
		strings.HasPrefix(trimmed, "/model") {
		return true
	}
	return strings.Contains(lower, "do not respond") && strings.Contains(lower, "local commands")
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

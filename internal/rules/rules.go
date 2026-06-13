package rules

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"toktop.unceas.dev/internal/handoff"
	"toktop.unceas.dev/internal/trace"
)

// severityRank orders suggestions from most to least severe for deterministic
// output. Unknown severities sort last.
func severityRank(severity string) int {
	switch severity {
	case SeverityWarn:
		return 0
	case SeverityNotice:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 3
	}
}

const (
	SeverityInfo   = "info"
	SeverityWarn   = "warn"
	SeverityNotice = "notice"
)

type Rule interface {
	ID() string
	Evaluate(ctx context.Context, index trace.Index, now time.Time) []trace.Suggestion
}

func All() []Rule {
	return []Rule{
		MCPUnused30d{},
		ToolOutputDominates{},
		RetryLoop{},
		LongSessionDegradation{},
		WorkflowInterrupted{},
	}
}

// WorkflowInterrupted flags a session where subagents ran but no final synthesis
// was recorded — the cross-agent-recovery scenario. It recommends building a
// handoff package rather than re-running the agents. Confidence is inferred: the
// absence of a final message is a heuristic for "interrupted before wrap-up".
type WorkflowInterrupted struct{}

func (WorkflowInterrupted) ID() string { return "workflow_interrupted" }

func (WorkflowInterrupted) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	type agg struct {
		agentRuns     int
		completedRuns int
		lastIndex     int
		lastHasFinal  bool
		lastFailed    bool
	}
	bySession := map[string]*agg{}
	var order []string
	for ti := range index.Turns {
		turn := &index.Turns[ti]
		a, ok := bySession[turn.SessionID]
		if !ok {
			a = &agg{lastIndex: -1}
			bySession[turn.SessionID] = a
			order = append(order, turn.SessionID)
		}
		for _, call := range turn.ToolCalls {
			if handoff.IsAgentTool(call.Name) {
				a.agentRuns++
				if call.Status == trace.StatusSuccess {
					a.completedRuns++
				}
			}
		}
		// Track the session's ending by the highest turn index: the interrupted
		// signal is the closing turn never producing a synthesis, not whether any
		// earlier turn did.
		if turn.Index >= a.lastIndex {
			a.lastIndex = turn.Index
			a.lastHasFinal = strings.TrimSpace(turn.AssistantFinal) != ""
			a.lastFailed = turn.Status == trace.StatusFailed
		}
	}
	var out []trace.Suggestion
	for _, sid := range order {
		a := bySession[sid]
		if a.agentRuns == 0 || (a.lastHasFinal && !a.lastFailed) {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"session_id": sid, "agent_runs": a.agentRuns, "completed_runs": a.completedRuns, "last_turn_failed": a.lastFailed})
		out = append(out, trace.Suggestion{
			RuleID:         "workflow_interrupted",
			Severity:       SeverityWarn,
			Confidence:     trace.ConfidenceInferred,
			ScopeKind:      "session",
			ScopeID:        sid,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("%d agent run(s) completed but the session did not end with a final synthesis; run `toktop handoff create --session %s` to package the results for recovery instead of re-running the agents.", a.completedRuns, sid),
		})
	}
	return out
}

func Run(ctx context.Context, index trace.Index, now time.Time) []trace.Suggestion {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]trace.Suggestion, 0)
	for _, rule := range All() {
		out = append(out, rule.Evaluate(ctx, index, now)...)
	}
	// Deterministic order: rules emit from Go maps (randomized iteration), so
	// sort before returning so identical inputs yield identical stored/displayed
	// order (#88).
	slices.SortStableFunc(out, func(a, b trace.Suggestion) int {
		return cmp.Or(
			cmp.Compare(severityRank(a.Severity), severityRank(b.Severity)),
			cmp.Compare(a.RuleID, b.RuleID),
			cmp.Compare(a.ScopeKind, b.ScopeKind),
			cmp.Compare(a.ScopeID, b.ScopeID),
		)
	})
	return out
}

type MCPUnused30d struct{}

func (MCPUnused30d) ID() string { return "mcp_unused_30d" }

func (MCPUnused30d) Evaluate(_ context.Context, index trace.Index, now time.Time) []trace.Suggestion {
	cutoff := now.Add(-30 * 24 * time.Hour)

	// Availability is derived from the declared/enabled MCP server set, not from
	// per-turn components: the only producer of MCP-server components always sets
	// Relation=Invoked, so deriving availability from components could never yield
	// an "available but never called" signal (#27).
	availability := make(map[string]bool)
	for _, server := range index.MCPServers {
		if server.Enabled {
			availability[server.Name] = true
		}
	}

	// Count observed invocations within the 30-day window. Turns with a zero
	// StartedAt have an unknown time and are treated as outside the window (#91).
	invoked := make(map[string]int)
	for _, turn := range index.Turns {
		if turn.StartedAt.IsZero() || turn.StartedAt.Before(cutoff) {
			continue
		}
		for _, comp := range turn.Components {
			if comp.ComponentKind != trace.ComponentKindMCPServer {
				continue
			}
			if comp.Relation == trace.RelationInvoked || comp.Relation == trace.RelationObservedUsed {
				invoked[comp.ComponentName]++
			}
		}
	}

	var out []trace.Suggestion
	for name := range availability {
		if invoked[name] > 0 {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"server": name})
		out = append(out, trace.Suggestion{
			RuleID:         "mcp_unused_30d",
			Severity:       SeverityInfo,
			Confidence:     trace.ConfidenceInferred,
			ScopeKind:      "global",
			ScopeID:        name,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("MCP server %q is enabled but has not been called in the last 30 days; consider disabling it to reduce context overhead.", name),
		})
	}
	return out
}

type ToolOutputDominates struct{}

func (ToolOutputDominates) ID() string { return "tool_output_dominates" }

func (ToolOutputDominates) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	var out []trace.Suggestion
	for _, turn := range index.Turns {
		if turn.Tokens.Input == 0 {
			continue
		}
		for _, call := range turn.ToolCalls {
			tokenEstimate := call.OutputBytes / 4
			if tokenEstimate == 0 {
				continue
			}
			ratio := float64(tokenEstimate) / float64(turn.Tokens.Input)
			if ratio < 0.5 {
				continue
			}
			ev, _ := json.Marshal(map[string]any{
				"turn_id":           turn.ID,
				"tool_name":         call.Name,
				"output_bytes":      call.OutputBytes,
				"estimated_tokens":  tokenEstimate,
				"turn_input_tokens": turn.Tokens.Input,
				"ratio":             ratio,
			})
			out = append(out, trace.Suggestion{
				RuleID:         "tool_output_dominates",
				Severity:       SeverityNotice,
				Confidence:     trace.ConfidenceEstimated,
				ScopeKind:      "turn",
				ScopeID:        turn.ID,
				EvidenceJSON:   string(ev),
				Recommendation: fmt.Sprintf("Tool %s output is ~%.0f%% of this turn's input tokens; consider trimming the command output (grep/head/tail) or storing the result to disk and re-reading on demand.", call.Name, ratio*100),
			})
			break
		}
	}
	return out
}

type RetryLoop struct{}

func (RetryLoop) ID() string { return "retry_loop" }

func (RetryLoop) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	var out []trace.Suggestion
	for _, turn := range index.Turns {
		if turn.InvocationCount < 4 || turn.Status != trace.StatusSuccess {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"turn_id": turn.ID, "invocations": turn.InvocationCount})
		out = append(out, trace.Suggestion{
			RuleID:         "retry_loop",
			Severity:       SeverityInfo,
			Confidence:     trace.ConfidenceObserved,
			ScopeKind:      "turn",
			ScopeID:        turn.ID,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("Turn took %d model invocations to reach success; consider splitting the request or tightening the prompt to avoid retry loops.", turn.InvocationCount),
		})
	}
	return out
}

type LongSessionDegradation struct{}

func (LongSessionDegradation) ID() string { return "long_session_degradation" }

func (LongSessionDegradation) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	bySession := make(map[string][]trace.Turn, len(index.Sessions))
	for _, turn := range index.Turns {
		bySession[turn.SessionID] = append(bySession[turn.SessionID], turn)
	}
	var out []trace.Suggestion
	for sessionID, turns := range bySession {
		if len(turns) < 6 {
			continue
		}
		// index.Turns arrives ordered by started_at, so a turn with an empty/zero
		// started_at sorts to the front regardless of its real position. Order this
		// session's turns by their turn index before the early/late split so the two
		// halves are genuinely chronological.
		slices.SortStableFunc(turns, func(a, b trace.Turn) int {
			return cmp.Compare(a.Index, b.Index)
		})
		mid := len(turns) / 2
		earlyAvg := avgInputTokens(turns[:mid])
		lateAvg := avgInputTokens(turns[mid:])
		if earlyAvg == 0 {
			continue
		}
		ratio := float64(lateAvg) / float64(earlyAvg)
		if ratio < 1.6 {
			continue
		}
		ev, _ := json.Marshal(map[string]any{
			"session_id": sessionID,
			"early_avg":  earlyAvg,
			"late_avg":   lateAvg,
			"ratio":      ratio,
			"turn_count": len(turns),
		})
		out = append(out, trace.Suggestion{
			RuleID:         "long_session_degradation",
			Severity:       SeverityNotice,
			Confidence:     trace.ConfidenceObserved,
			ScopeKind:      "session",
			ScopeID:        sessionID,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("Later turns in this session use ~%.1fx the input tokens of earlier turns; consider starting a fresh session or running a manual compaction.", ratio),
		})
	}
	return out
}

func avgInputTokens(turns []trace.Turn) int {
	if len(turns) == 0 {
		return 0
	}
	total := 0
	for _, turn := range turns {
		total += turn.Tokens.Input
	}
	return total / len(turns)
}

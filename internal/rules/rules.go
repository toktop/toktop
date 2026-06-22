package rules

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"toktop.unceas.dev/internal/ingest"
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
		ToolOutputBloat{},
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
	var out []trace.Suggestion
	for sid, turns := range turnsBySession(index) {
		agentRuns, completedRuns := 0, 0
		for _, turn := range turns {
			for _, call := range turn.ToolCalls {
				if ingest.IsAgentTool(turn.Provider, call.Name) {
					agentRuns++
					if call.Status == trace.StatusSuccess {
						completedRuns++
					}
				}
			}
		}
		if agentRuns == 0 {
			continue
		}
		// The closing turn is the last one where the assistant actually ran
		// (invocation_count>0). Trailing user-only or synthetic turns — a follow-up
		// prompt, a /clear, a bash-stdout hook turn — carry no assistant output, and
		// reading their empty AssistantFinal as "no synthesis" was the rule's whole
		// false-positive class: the real wrap-up sits one turn earlier. A closing turn
		// that produced a synthesis means the session wrapped up (a failed tool call
		// within it is not an interruption); only its absence signals interruption.
		var closing *trace.Turn
		for _, turn := range slices.Backward(turns) {
			if turn.InvocationCount > 0 {
				closing = turn
				break
			}
		}
		// A real synthesis is non-empty AND actually generated (Tokens.Output>0); a
		// closing turn whose "final" is only an injected stub — a usage-limit notice,
		// a "No response requested." marker — produced no output and is an interruption.
		if closing == nil || (strings.TrimSpace(closing.AssistantFinal) != "" && closing.Tokens.Output > 0) {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"session_id": sid, "agent_runs": agentRuns, "completed_runs": completedRuns, "last_turn_failed": closing.Status == trace.StatusFailed})
		out = append(out, trace.Suggestion{
			RuleID:         "workflow_interrupted",
			Severity:       SeverityWarn,
			Confidence:     trace.ConfidenceInferred,
			ScopeKind:      "session",
			ScopeID:        sid,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("%d agent run(s) ran but the session never produced a final synthesis; run `toktop handoff create --session %s` to recover — it classifies each run (completed/failed/stopped/declined/in-flight) and packages the captured results instead of blindly re-running.", agentRuns, sid),
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
	// Availability is derived from the declared/enabled MCP server set, not from
	// per-turn components: the only producer of MCP-server components always sets
	// Relation=Invoked, so deriving availability from components could never yield
	// an "available but never called" signal (#27). Each enabled server keeps its
	// SourceID so it can be judged against its own provider's history.
	type enabledServer struct{ name, sourceID string }
	var servers []enabledServer
	for _, server := range index.MCPServers {
		if server.Enabled {
			servers = append(servers, enabledServer{server.Name, server.SourceID})
		}
	}
	if len(servers) == 0 {
		return nil // nothing enabled to judge — skip the history scan entirely
	}

	cutoff := now.Add(-30 * 24 * time.Hour)

	// "Unused for 30 days" is only assertable if the server's own provider has 30 days
	// of history. created_at/updated_at are config-import timestamps, not real
	// per-server enable dates, so a recently-added server can't be exempted
	// individually; instead, a server is suppressed when its provider's earliest
	// activity is itself inside the window — a fresh install, or a freshly-added
	// provider in a multi-source home. The earliest is per provider, keyed by the
	// reproducible SourceID, so an old provider never unblocks a newly-added one.
	earliestBySource := make(map[string]time.Time)
	sourceIDByProvider := make(map[string]string) // SourceID hashes; cache so it runs once per provider, not once per turn
	for _, turn := range index.Turns {
		if turn.StartedAt.IsZero() {
			continue
		}
		src, ok := sourceIDByProvider[turn.Provider]
		if !ok {
			src = trace.SourceID(turn.Provider)
			sourceIDByProvider[turn.Provider] = src
		}
		if e, ok := earliestBySource[src]; !ok || turn.StartedAt.Before(e) {
			earliestBySource[src] = turn.StartedAt
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
	for _, server := range servers {
		if invoked[server.name] > 0 {
			continue
		}
		// Suppress unless this server's provider has at least 30 days of history.
		if earliest, ok := earliestBySource[server.sourceID]; !ok || earliest.After(cutoff) {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"server": server.name})
		out = append(out, trace.Suggestion{
			RuleID:         "mcp_unused_30d",
			Severity:       SeverityInfo,
			Confidence:     trace.ConfidenceInferred,
			ScopeKind:      "global",
			ScopeID:        server.name,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("MCP server %q is enabled but has not been called in the last 30 days; consider disabling it to reduce context overhead.", server.name),
		})
	}
	return out
}

// minBloatOutputTokens is the smallest tool output (estimated, ~4 bytes/token) worth
// considering — under ~2k tokens (8 KB) trimming saves little however long it
// lingers. minBloatCarriedCost floors the real cost: estimated output tokens × the
// later turns that re-read it (token-turns of repeated context). Gating on this
// cumulative cost rather than peak size catches a modest output carried across a long
// session while ignoring a one-off output near the session's end, and no tiny output
// qualifies however long it lingers. The old rule compared output to the same turn's
// total_input_tokens, which on cache-heavy turns (near-zero fresh input) flagged
// 13-byte echoes as "dominating".
const (
	minBloatOutputTokens  = 2000
	minBloatCarriedCost   = 100000
	hugeBloatOutputTokens = 12500 // a single output this large (~50 KB) is worth trimming however few turns carry it
)

type ToolOutputBloat struct{}

func (ToolOutputBloat) ID() string { return "tool_output_bloat" }

func (ToolOutputBloat) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	var out []trace.Suggestion
	for _, turns := range turnsBySession(index) {
		// The last turn's output is never re-read, so it costs nothing — skip it.
		for i, turn := range turns[:len(turns)-1] {
			carried := len(turns) - 1 - i // later turns that may still carry this output
			for _, call := range turn.ToolCalls {
				outTokens := int(call.OutputBytes / 4)
				// Flag on cumulative carried cost (tokens × the later turns that re-read
				// it), or on a single output so large it is worth trimming however few
				// turns carry it — the cost product alone would miss a 50 KB+ blob that
				// lands late in a session.
				if outTokens < minBloatOutputTokens ||
					(outTokens*carried < minBloatCarriedCost && outTokens < hugeBloatOutputTokens) {
					continue
				}
				ev, _ := json.Marshal(map[string]any{
					"turn_id":          turn.ID,
					"tool_name":        call.Name,
					"output_bytes":     call.OutputBytes,
					"estimated_tokens": outTokens,
					"carried_turns":    carried,
				})
				out = append(out, trace.Suggestion{
					RuleID:         "tool_output_bloat",
					Severity:       SeverityNotice,
					Confidence:     trace.ConfidenceEstimated,
					ScopeKind:      "turn",
					ScopeID:        turn.ID,
					EvidenceJSON:   string(ev),
					Recommendation: fmt.Sprintf("Tool %s emitted ~%d tokens of output that up to %d later turn(s) re-read from context (unless compacted away); trim it (grep/head/tail) or store it to disk and re-read on demand.", call.Name, outTokens, carried),
				})
				break // one finding per turn (its first qualifying output)
			}
		}
	}
	return out
}

// retryLoopFailures is how many times one tool must fail within a single turn to
// flag it. The old rule fired on invocation_count>=4 regardless of outcome — 35% of
// all *successful* turns, 99% of them with zero failed calls — so it measured task
// size, not failure. Repeated failures of one tool are the real friction signal, and
// they are observed, not inferred.
const retryLoopFailures = 3

type RetryLoop struct{}

func (RetryLoop) ID() string { return "retry_loop" }

func (RetryLoop) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	var out []trace.Suggestion
	for ti := range index.Turns {
		turn := &index.Turns[ti]
		// A user-interrupted turn's tool failures are aborts, not a tool the agent
		// kept hitting — not friction worth flagging.
		if turn.Status == trace.StatusInterrupted {
			continue
		}
		var failsByTool map[string]int
		for _, call := range turn.ToolCalls {
			// Skip agent/subagent dispatches: a failed run is an outcome, not a tool
			// re-hit against bad arguments (provider-neutral via the agent-tool
			// registry). A user *declining* a tool use (rejecting a plan, dismissing a
			// prompt) is not friction either: claude-code records it as StatusRejected,
			// so the StatusFailed gate below already excludes it. LIMITATION: codex and
			// opencode cannot — their on-disk formats record a decline indistinguishably
			// from a genuine tool error, so a decline there still counts as a failure here.
			if call.Status == trace.StatusFailed && !ingest.IsAgentTool(turn.Provider, call.Name) {
				if failsByTool == nil {
					failsByTool = map[string]int{}
				}
				failsByTool[call.Name]++
			}
		}
		// Report the worst-offending tool; ties break by name for deterministic output.
		worstTool, worstFails := "", 0
		for name, n := range failsByTool {
			if n > worstFails || (n == worstFails && name < worstTool) {
				worstTool, worstFails = name, n
			}
		}
		if worstFails < retryLoopFailures {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"turn_id": turn.ID, "tool_name": worstTool, "failures": worstFails})
		out = append(out, trace.Suggestion{
			RuleID:         "retry_loop",
			Severity:       SeverityInfo,
			Confidence:     trace.ConfidenceObserved,
			ScopeKind:      "turn",
			ScopeID:        turn.ID,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("Tool %s failed %d times in this turn; recurring failures of one tool often mean a wrong argument or path, or an approach that isn't converging.", worstTool, worstFails),
		})
	}
	return out
}

type LongSessionDegradation struct{}

func (LongSessionDegradation) ID() string { return "long_session_degradation" }

func (LongSessionDegradation) Evaluate(_ context.Context, index trace.Index, _ time.Time) []trace.Suggestion {
	var out []trace.Suggestion
	for sessionID, turns := range turnsBySession(index) {
		if len(turns) < 6 {
			continue
		}
		mid := len(turns) / 2
		earlyAvg := avgContext(turns[:mid])
		lateAvg := avgContext(turns[mid:])
		if earlyAvg == 0 {
			continue
		}
		ratio := float64(lateAvg) / float64(earlyAvg)
		// Flag a session heavy enough that a fresh start or compaction pays off, which
		// takes a large absolute late context (~150k ≈ ¾ of a 200k window) reached
		// either way: it grew sharply (ratio), OR it started saturated and stayed so —
		// a session that begins heavy never shows a high ratio yet needs the advice
		// most, so keying on growth alone would structurally miss it.
		if lateAvg < lateContextFloor || (ratio < lateContextRatio && lateAvg < lateContextHeavy) {
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
			Confidence:     trace.ConfidenceInferred,
			ScopeKind:      "session",
			ScopeID:        sessionID,
			EvidenceJSON:   string(ev),
			Recommendation: fmt.Sprintf("Later turns in this session average ~%dk tokens of context (~%.1fx the early turns); consider starting a fresh session or compacting.", lateAvg/1000, ratio),
		})
	}
	return out
}

const (
	lateContextRatio = 2.0
	lateContextFloor = 150000
	lateContextHeavy = 300000
)

// turnsBySession groups the index's turns by session, each slice sorted by turn
// Index. index.Turns arrives ordered by started_at, which mis-orders turns whose
// started_at is zero, so any rule that reasons about turn order ("later turns",
// early/late halves) must sort by Index first; the slices hold pointers to avoid
// copying every Turn (with its ToolCalls) into the map.
func turnsBySession(index trace.Index) map[string][]*trace.Turn {
	bySession := make(map[string][]*trace.Turn, len(index.Sessions))
	for i := range index.Turns {
		t := &index.Turns[i]
		bySession[t.SessionID] = append(bySession[t.SessionID], t)
	}
	for _, turns := range bySession {
		slices.SortStableFunc(turns, func(a, b *trace.Turn) int {
			return cmp.Compare(a.Index, b.Index)
		})
	}
	return bySession
}

// turnContextTokens estimates the real context the model processed on a turn — the
// prompt size, not the per-turn token bill. Two corrections over a bare
// total_input_tokens: it adds cache_read (for cache-heavy providers the bulk of the
// context lives there — ~87% of turns carry more cache-read than fresh input), and
// it divides by the invocation count, because the per-turn token columns sum across
// the turn's model calls, so an N-invocation turn counts the (largely static)
// context ~N times. Unnormalized, a turn's "context" conflates prompt size with how
// many times the model was called.
func turnContextTokens(t *trace.Turn) int {
	n := max(t.InvocationCount, 1)
	return (t.Tokens.Input + t.Tokens.CacheRead) / n
}

func avgContext(turns []*trace.Turn) int {
	total, n := 0, 0
	for _, turn := range turns {
		// A turn the model never ran (invocation_count==0: a user-only or synthetic
		// turn) carries no context the model processed; averaging its zero deflates
		// the half and hides a genuinely heavy session.
		if turn.InvocationCount == 0 {
			continue
		}
		total += turnContextTokens(turn)
		n++
	}
	if n == 0 {
		return 0
	}
	return total / n
}

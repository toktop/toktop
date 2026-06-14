// Package handoff assembles an Evidence-based Handoff Package from a single
// ingested session: a read-only, auditable directory another agent (e.g. Codex
// picking up an interrupted Claude Code workflow) can consume without re-deriving
// or re-running the original agents.
//
// Everything here is read-side reconstruction over the provider-neutral trace
// model — no schema changes, no new persisted tables. Agent runs are recovered
// from the agent-spawning tool calls (Task / Agent / Workflow), and every emitted
// fact carries provenance back to the raw transcript (file + raw event id).
package handoff

import (
	"encoding/json"
	"strings"
	"time"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// SchemaVersion is the stable contract a consuming agent/script keys on.
const SchemaVersion = "toktop.handoff.v1"

// agentToolNames are the built-in tools that spawn a subagent whose run we
// reconstruct. Claude Code uses "Task"; this environment also exposes "Agent"
// and the multi-agent "Workflow" orchestrator.
var agentToolNames = map[string]bool{
	"Task":     true,
	"Agent":    true,
	"Workflow": true,
}

// IsAgentTool reports whether a tool name spawns a subagent whose run the handoff
// reconstructs. Shared with the rule engine so the agent-tool list lives in one place.
func IsAgentTool(name string) bool { return agentToolNames[name] }

// Confidence labels how trustworthy an evidence claim is for an agent picking up
// the work. evidence = proven by the transcript; inference = derived/heuristic;
// unknown = could not be determined.
type Confidence string

const (
	ConfidenceEvidence  Confidence = "evidence"
	ConfidenceInference Confidence = "inference"
	ConfidenceUnknown   Confidence = "unknown"
)

// SourcePointer locates a fact in the original transcript so a consumer can
// re-read the raw bytes rather than trust a summary.
type SourcePointer struct {
	Provider      string `json:"provider"`
	SessionID     string `json:"session_id"`
	TurnID        string `json:"turn_id,omitempty"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
	File          string `json:"file,omitempty"`
	UseEventID    string `json:"use_event_id,omitempty"`
	ResultEventID string `json:"result_event_id,omitempty"`
}

// AgentRun is one reconstructed subagent invocation.
type AgentRun struct {
	ID          string        `json:"id"`
	Tool        string        `json:"tool"`                   // Task | Agent | Workflow
	Type        string        `json:"type,omitempty"`         // subagent_type
	Description string        `json:"description,omitempty"`  // short task description
	Prompt      string        `json:"prompt,omitempty"`       // the agent's input
	Result      string        `json:"result,omitempty"`       // the agent's output
	Status      string        `json:"status"`                 // success | failed | ...
	Error       string        `json:"error,omitempty"`
	StartedAt   time.Time     `json:"started_at,omitzero"`
	EndedAt     time.Time     `json:"ended_at,omitzero"`
	DurationMs  int64         `json:"duration_ms,omitempty"`
	OutputBytes int64         `json:"output_bytes,omitempty"`
	Source      SourcePointer `json:"source"`
}

// EvidenceItem is one provenance-carrying fact for the handoff index.
type EvidenceItem struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"` // agent_result | final_answer | failed_agent | incomplete_agent
	Claim      string        `json:"claim"`
	Confidence Confidence    `json:"confidence"`
	Source     SourcePointer `json:"source"`
}

// Manifest is the package entry point: workflow status + counts + where to start.
type Manifest struct {
	Schema                 string    `json:"schema"`
	GeneratedAt            time.Time `json:"generated_at"`
	SessionID              string    `json:"session_id"`
	ExternalSessionID      string    `json:"external_session_id,omitempty"`
	// AmbiguousSessionIDs lists every internal session id the requested id matched
	// when an external id resolved to more than one session; SessionID is the one
	// packaged (the same first match the CLI picks). Empty when the id was
	// unambiguous. Set by the resolving surface, not by Build.
	AmbiguousSessionIDs    []string  `json:"ambiguous_session_ids,omitempty"`
	Provider               string    `json:"provider"`
	Project                string    `json:"project,omitempty"`
	TranscriptPath         string    `json:"transcript_path,omitempty"`
	WorkflowStatus         string    `json:"workflow_status"`
	Turns                  int       `json:"turns"`
	AgentRuns              int       `json:"agent_runs"`
	CompletedAgentRuns     int       `json:"completed_agent_runs"`
	FailedAgentRuns        int       `json:"failed_agent_runs"`
	// InterruptedAgentRuns counts agents deliberately stopped (a successful
	// TaskStop); IncompleteAgentRuns counts those launched but never completed or
	// stopped (in-flight / abandoned). Both lack a captured result, but the
	// recovery differs: reconcile a stop vs resume an in-flight run.
	InterruptedAgentRuns   int       `json:"interrupted_agent_runs,omitempty"`
	IncompleteAgentRuns    int       `json:"incomplete_agent_runs,omitempty"`
	FinalSynthesisPresent  bool      `json:"final_synthesis_present"`
	// RecommendedEntrypoints names the package files in reading order; it is a
	// directory-form (CLI) concept, so the HTTP handler clears it (omitempty) —
	// over HTTP the whole package is one JSON body, not a set of files.
	RecommendedEntrypoints []string  `json:"recommended_entrypoints,omitempty"`
}

// Package is the full assembled handoff, ready to write to a directory (CLI) or
// serve as one JSON object (HTTP GET /v1/sessions/{id}/handoff).
type Package struct {
	Manifest  Manifest       `json:"manifest"`
	Session   trace.Session  `json:"session"`
	Turns     []trace.Turn   `json:"turns"`
	AgentRuns []AgentRun     `json:"agent_runs"`
	Evidence  []EvidenceItem `json:"evidence"`
}

// agentInput is the shared shape of the agent-spawning tools' input_json. Task
// and Agent use description/subagent_type/prompt; Workflow uses name/description.
type agentInput struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
	Prompt       string `json:"prompt"`
	Name         string `json:"name"`
}

// Build reconstructs the handoff package for one session from its turns (which
// must already carry their tool calls — load via query.Service.SessionTurns).
// maxOutputBytes > 0 clips large tool outputs and agent results inlined into the
// package (turns.json / agent-results.ndjson); the raw transcript pointers still
// reach the full bytes. 0 leaves everything full.
func Build(now time.Time, session trace.Session, turns []trace.Turn, maxOutputBytes int) Package {
	// Detect and build evidence from the full content first, then clip what gets
	// inlined — so detection/claims are never weakened by the size cap.
	agents := detectAgentRuns(session, turns)
	evidence := buildEvidence(session, turns, agents)
	manifest := buildManifest(now, session, turns, agents)
	if maxOutputBytes > 0 {
		trace.ClipToolCalls(turns, maxOutputBytes)
		for i := range agents {
			agents[i].Result, _ = textutil.ClipText(agents[i].Result, maxOutputBytes)
			agents[i].Prompt, _ = textutil.ClipText(agents[i].Prompt, maxOutputBytes)
		}
	}
	return Package{
		Manifest:  manifest,
		Session:   session,
		Turns:     turns,
		AgentRuns: agents,
		Evidence:  evidence,
	}
}

func detectAgentRuns(session trace.Session, turns []trace.Turn) []AgentRun {
	// Non-nil so the handoff package's agent_runs serializes as [] (not null) for a
	// session with no agent tool calls — the /v1/sessions/{id}/handoff route.
	runs := make([]AgentRun, 0)
	for ti := range turns {
		turn := &turns[ti]
		for ci := range turn.ToolCalls {
			call := &turn.ToolCalls[ci]
			if !agentToolNames[call.Name] {
				continue
			}
			var in agentInput
			_ = json.Unmarshal([]byte(call.Input), &in)
			runs = append(runs, AgentRun{
				ID:          call.ID,
				Tool:        call.Name,
				Type:        textutil.FirstNonBlank(in.SubagentType, in.Name),
				Description: in.Description,
				Prompt:      in.Prompt,
				Result:      call.Output,
				Status:      call.Status,
				Error:       call.Error,
				StartedAt:   call.StartedAt,
				EndedAt:     call.EndedAt,
				DurationMs:  call.DurationMs,
				OutputBytes: call.OutputBytes,
				Source: SourcePointer{
					Provider:      session.Provider,
					SessionID:     session.ID,
					TurnID:        turn.ID,
					ToolCallID:    textutil.FirstNonBlank(call.UseID, call.ID),
					File:          session.TranscriptPath,
					UseEventID:    call.RawUseEventID,
					ResultEventID: call.RawResultEventID,
				},
			})
		}
	}
	return runs
}

func buildManifest(now time.Time, session trace.Session, turns []trace.Turn, agents []AgentRun) Manifest {
	completed, failed, interrupted, incomplete := 0, 0, 0, 0
	for _, a := range agents {
		switch a.Status {
		case trace.StatusSuccess:
			completed++
		case trace.StatusFailed:
			failed++
		case trace.StatusInterrupted:
			// Deliberately stopped (a successful TaskStop) — its result was never
			// produced, but unlike an in-flight run it was killed on purpose, so a
			// recovering agent should reconcile rather than blindly resume it.
			interrupted++
		default:
			// pending / active / unknown: launched but never completed or stopped —
			// in-flight / abandoned, neither a usable result nor a failure.
			incomplete++
		}
	}
	// "Final synthesis present" means the session ENDED with an assistant final
	// message (turns are ordered by turn_index, so the last is the ending). An
	// earlier final does not count — the headline scenario is agents finishing but
	// the closing synthesis turn never completing.
	finalPresent := len(turns) > 0 && strings.TrimSpace(turns[len(turns)-1].AssistantFinal) != ""
	entrypoints := []string{"README.md", "evidence-index.md"}
	if len(agents) > 0 {
		entrypoints = append(entrypoints, "agent-results.ndjson")
	}
	entrypoints = append(entrypoints, "codex-prompt.md")
	return Manifest{
		Schema:                 SchemaVersion,
		GeneratedAt:            now,
		SessionID:              session.ID,
		ExternalSessionID:      session.ExternalID,
		Provider:               session.Provider,
		Project:                textutil.FirstNonBlank(session.ProjectName, session.ProjectPath),
		TranscriptPath:         session.TranscriptPath,
		WorkflowStatus:         workflowStatus(turns, agents, finalPresent),
		Turns:                  len(turns),
		AgentRuns:              len(agents),
		CompletedAgentRuns:     completed,
		FailedAgentRuns:        failed,
		InterruptedAgentRuns:   interrupted,
		IncompleteAgentRuns:    incomplete,
		FinalSynthesisPresent:  finalPresent,
		RecommendedEntrypoints: entrypoints,
	}
}

// workflowStatus classifies the session for a recovering agent. The headline
// case the feature targets: agents finished but the final synthesis is missing
// (quota/interrupt before wrap-up). A present final synthesis is authoritative
// for "completed" — a failed tool call in the closing turn (a non-zero Bash, a
// grep with no match) does not mean the session was interrupted, and any
// still-in-flight agents are surfaced via IncompleteAgentRuns rather than by
// overriding a wrap-up that genuinely happened.
func workflowStatus(turns []trace.Turn, agents []AgentRun, finalPresent bool) string {
	if len(turns) == 0 {
		return "empty"
	}
	if finalPresent {
		return "completed"
	}
	if len(agents) > 0 {
		// No wrap-up: an agent still in flight (launched, never completed or stopped)
		// means its result was never captured and it may still be running — distinct
		// from agents that reached a terminal state (succeeded, failed, or were
		// deliberately stopped) but whose closing synthesis was lost.
		for _, a := range agents {
			if a.Status == trace.StatusActive || a.Status == trace.StatusPending {
				return "interrupted_agents_in_flight"
			}
		}
		return "interrupted_after_agents_completed"
	}
	if turns[len(turns)-1].Status == trace.StatusFailed {
		return "interrupted"
	}
	return "no_final_synthesis"
}

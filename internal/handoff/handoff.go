// Package handoff assembles an Evidence-based Handoff Package from a single
// ingested session: a read-only, auditable directory another agent can consume
// without re-deriving or re-running the original agents. It is symmetric across
// providers — a claude-code session can be picked up in codex and vice versa.
//
// Everything here is read-side reconstruction over the provider-neutral trace
// model — no new persisted tables, no provider-specific knowledge. Agent runs are
// recovered two ways: from the parent's agent-spawning tool calls (each provider
// declares its own — claude-code Task/Agent/Workflow, codex spawn_agent) for the
// orchestrator view, and from the actual linked subagent sessions for their REAL
// results (what an interrupted run's ack lacks). Every fact carries provenance back
// to the raw transcript (file + raw event id), and the receiver prompt names no
// provider-specific tool, so either side can produce or consume a package.
package handoff

import (
	"strings"
	"time"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// SchemaVersion is the stable contract a consuming agent/script keys on.
const SchemaVersion = "toktop.handoff.v1"

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
	ID string `json:"id"`
	// Tool is the spawning tool's name for a run derived from a parent tool call
	// (claude-code Task/Agent/Workflow, codex spawn_agent); for a subagent session
	// appended with no matching parent call it holds the neutral SubagentKind
	// ("task"/"workflow"/"agent") instead. Prefer Type for display.
	Tool        string        `json:"tool"`
	Type        string        `json:"type,omitempty"`         // subagent_type / agent role
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
	Type       string        `json:"type"` // agent_result | last_assistant_message | failed_agent | stopped_agent | incomplete_agent
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
	Title                  string    `json:"title,omitempty"`
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
	InterruptedAgentRuns   int       `json:"interrupted_agent_runs"`
	IncompleteAgentRuns    int       `json:"incomplete_agent_runs"`
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
	// Digest is a lean, pre-rendered markdown narrative (user→assistant per turn, no
	// tool-call bodies) — the cheap default read path, so a consumer can orient
	// without ingesting the fat Turns. Rendered once in Build; written as digest.md
	// (CLI) and served inline as the "digest" string (HTTP).
	Digest string `json:"digest"`
}

// Build reconstructs the handoff package for one session from its turns (which
// must already carry their tool calls — load via query.Service.SessionTurns).
// maxOutputBytes > 0 clips large tool outputs and agent results inlined into the
// package (turns.json / agent-results.ndjson); the raw transcript pointers still
// reach the full bytes. 0 leaves everything full.
func Build(now time.Time, session trace.Session, turns []trace.Turn, subagentRuns []SubagentRun, maxOutputBytes int) Package {
	// Detect and build evidence from the full content first, then clip what gets
	// inlined — so detection/claims are never weakened by the size cap.
	agents := detectAgentRuns(session, turns)
	// Fold in the actual completed sub-agent runs (ingested as linked sessions):
	// these carry the REAL results an interrupted Workflow's ack lacks — the payoff
	// of capturing subagents. Provider-neutral (works for claude-code and codex).
	agents = mergeSubagentRuns(session, agents, subagentRuns)
	evidence := buildEvidence(session, turns, agents)
	manifest := buildManifest(now, session, turns, agents)
	if maxOutputBytes > 0 {
		trace.ClipToolCalls(turns, maxOutputBytes)
		for i := range agents {
			agents[i].Result, _ = textutil.ClipText(agents[i].Result, maxOutputBytes)
			agents[i].Prompt, _ = textutil.ClipText(agents[i].Prompt, maxOutputBytes)
		}
	}
	pkg := Package{
		Manifest:  manifest,
		Session:   session,
		Turns:     turns,
		AgentRuns: agents,
		Evidence:  evidence,
	}
	// Render the lean digest once from the final (post-clip) turns, so the CLI file
	// (digest.md) and the HTTP inline string are byte-identical and cannot drift.
	pkg.Digest = pkg.digestMD()
	return pkg
}

func detectAgentRuns(session trace.Session, turns []trace.Turn) []AgentRun {
	// Non-nil so the handoff package's agent_runs serializes as [] (not null) for a
	// session with no agent tool calls — the /v1/sessions/{id}/handoff route.
	runs := make([]AgentRun, 0)
	for ti := range turns {
		turn := &turns[ti]
		for ci := range turn.ToolCalls {
			call := &turn.ToolCalls[ci]
			if !ingest.IsAgentTool(session.Provider, call.Name) {
				continue
			}
			typ, description, prompt := ingest.AgentRunInput(session.Provider, call.Name, []byte(call.Input))
			runs = append(runs, AgentRun{
				ID:          call.ID,
				Tool:        call.Name,
				Type:        typ,
				Description: description,
				Prompt:      prompt,
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

// SubagentRun is one completed sub-agent session linked to the packaged parent
// (via parent_session_id), carrying its REAL recovered result — the work an
// interrupted Workflow's ack does not. Provider-neutral; the read layer assembles
// it from the linked subagent sessions (see query.Service.SubagentRuns).
type SubagentRun struct {
	SessionID       string
	ExternalID      string // the subagent's external/thread id (codex spawn_agent output's agent_id)
	TranscriptPath  string
	AgentType       string
	SubagentKind    string
	WorkflowRunID   string
	ParentToolUseID string
	Status          string
	Result          string // the sub-agent's final assistant message
	StartedAt       time.Time
	EndedAt         time.Time
}

// mergeSubagentRuns folds the linked sub-agent sessions into the tool-call-derived
// agent runs. A sub-agent whose ParentToolUseID matches a spawning tool call (a
// claude-code Task/Agent) ENRICHES that run — pointing its source at the sub-agent
// transcript and filling a result the synchronous tool_result lacked. Everything
// else (a Workflow's internal agents, codex spawned agents) is APPENDED as its own
// run: the parent's single spawn/orchestrator call is one run, its agents another.
func mergeSubagentRuns(session trace.Session, agents []AgentRun, subs []SubagentRun) []AgentRun {
	// Two ways a spawn tool call links to its subagent session: the child records the
	// launching tool_use id on its own side (claude-code Task → ParentToolUseID), or
	// the spawn's OUTPUT names the child it launched (codex spawn_agent → child
	// external id, via the provider seam). Either lets us ENRICH the one spawn run
	// instead of double-listing it alongside the real subagent run.
	byUseID := make(map[string]int, len(agents))
	byChildID := make(map[string]int, len(agents))
	for i := range agents {
		if id := agents[i].Source.ToolCallID; id != "" {
			byUseID[id] = i
		}
		if childID := ingest.AgentSpawnChildID(session.Provider, agents[i].Tool, []byte(agents[i].Result)); childID != "" {
			byChildID[childID] = i
		}
	}
	// enrich folds a completed subagent session into its spawn run: the session is the
	// authoritative record, so adopt its terminal status + real result, and repoint the
	// source at its transcript while KEEPING the spawn call's TurnID/ToolCallID (they
	// locate the spawn in turns.json and are file-independent) — dropping the parent-file
	// raw-event ids, which do not apply to the subagent file.
	enrich := func(idx int, s SubagentRun) {
		if st := subagentRunStatus(s.Status); st == trace.StatusSuccess {
			agents[idx].Status = st
		}
		if s.Result != "" {
			agents[idx].Result = s.Result
			agents[idx].OutputBytes = int64(len(s.Result))
		}
		// The parent's Task/Agent tool input often omits subagent_type, so the spawn
		// run's Type is empty; the linked session carries the real agent type.
		if agents[idx].Type == "" {
			agents[idx].Type = s.AgentType
		}
		p := agents[idx].Source
		p.SessionID = s.SessionID
		p.File = s.TranscriptPath
		p.UseEventID = ""
		p.ResultEventID = ""
		agents[idx].Source = p
	}
	for _, s := range subs {
		if s.ParentToolUseID != "" {
			if idx, ok := byUseID[s.ParentToolUseID]; ok {
				enrich(idx, s)
				continue
			}
		}
		if s.ExternalID != "" {
			if idx, ok := byChildID[s.ExternalID]; ok {
				enrich(idx, s)
				continue
			}
		}
		agents = append(agents, AgentRun{
			ID:          "subagent:" + s.SessionID,
			Tool:        s.SubagentKind,
			Type:        s.AgentType,
			Result:      s.Result,
			Status:      subagentRunStatus(s.Status),
			StartedAt:   s.StartedAt,
			EndedAt:     s.EndedAt,
			OutputBytes: int64(len(s.Result)),
			Source:      SourcePointer{Provider: session.Provider, SessionID: s.SessionID, File: s.TranscriptPath},
		})
	}
	return agents
}

// subagentRunStatus maps a subagent SESSION status to the agent-run vocabulary the
// manifest/evidence classify on. Both parsers only ever set a session to `completed`
// (it has turns) or `unknown` (0-turn ghost) — never `failed`/`interrupted` — so this
// maps `completed`→`success` (an authoritative result, not mis-counted as in-flight)
// and lets `unknown` fall through to incomplete. A session that ran to completion but
// whose work failed is not distinguishable at the session level (the concrete codex
// failed-spawn case is caught earlier, at the tool-call status).
func subagentRunStatus(sessionStatus string) string {
	if sessionStatus == trace.StatusCompleted {
		return trace.StatusSuccess
	}
	return sessionStatus
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
	entrypoints := []string{"README.md", "digest.md", "evidence-index.md"}
	if len(agents) > 0 {
		entrypoints = append(entrypoints, "agent-results.ndjson")
	}
	entrypoints = append(entrypoints, "receiver-prompt.md")
	return Manifest{
		Schema:                 SchemaVersion,
		GeneratedAt:            now,
		SessionID:              session.ID,
		ExternalSessionID:      session.ExternalID,
		Title:                  session.Title,
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

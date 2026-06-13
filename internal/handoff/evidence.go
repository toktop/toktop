package handoff

import (
	"slices"
	"strings"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// buildEvidence derives provenance-carrying facts a recovering agent can rely on.
// Every item points back to the raw transcript. Confidence is "evidence" for
// things the transcript states directly (an agent's recorded result, the final
// assistant message); heuristics would be "inference" (none emitted in v1, to
// avoid presenting synthesized claims as authoritative).
func buildEvidence(session trace.Session, turns []trace.Turn, agents []AgentRun) []EvidenceItem {
	items := make([]EvidenceItem, 0, len(agents)+1)

	for i := range agents {
		a := &agents[i]
		typ := "agent_result"
		conf := ConfidenceEvidence
		claim := a.Description
		if claim == "" {
			claim = a.Type + " agent run"
		}
		switch a.Status {
		case trace.StatusSuccess:
			// agent_result / ConfidenceEvidence: the transcript recorded a result.
		case trace.StatusFailed:
			typ = "failed_agent"
			conf = ConfidenceUnknown // a failed run's output is not a reliable result
		default:
			// pending / active: the run was interrupted before its result was
			// recorded, so it is not authoritative evidence — flag it so the
			// recovering agent knows to re-run it rather than trust a blank result.
			typ = "incomplete_agent"
			conf = ConfidenceUnknown
		}
		if summary := strings.TrimSpace(a.Result); summary != "" {
			claim += " — " + oneLine(summary, 200)
		}
		items = append(items, EvidenceItem{
			ID:         "agent:" + a.ID,
			Type:       typ,
			Claim:      claim,
			Confidence: conf,
			Source:     a.Source,
		})
	}

	// The session's final assistant message, if present, is the authoritative
	// "where the work ended" fact.
	for _, turn := range slices.Backward(turns) {
		final := strings.TrimSpace(turn.AssistantFinal)
		if final == "" {
			continue
		}
		items = append(items, EvidenceItem{
			ID:         "final:" + turn.ID,
			Type:       "final_answer",
			Claim:      oneLine(final, 280),
			Confidence: ConfidenceEvidence,
			Source: SourcePointer{
				Provider:  session.Provider,
				SessionID: session.ID,
				TurnID:    turn.ID,
				File:      session.TranscriptPath,
			},
		})
		break
	}

	return items
}

func oneLine(s string, max int) string {
	return textutil.Truncate(strings.Join(strings.Fields(s), " "), max)
}

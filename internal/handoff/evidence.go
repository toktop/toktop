package handoff

import (
	"slices"
	"strings"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// typeLastAssistantMessage is the evidence Type for the session's trailing assistant
// message. Shared by the producer (buildEvidence) and the consumer that gates the
// receiver-prompt on whether the item was actually emitted (receiverPromptMD).
const typeLastAssistantMessage = "last_assistant_message"

// buildEvidence derives provenance-carrying facts a recovering agent can rely on.
// Every item points back to the raw transcript. Confidence is "evidence" for
// things the transcript states directly (an agent's recorded result, the last
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
			claim = textutil.FirstNonBlank(a.Type, a.Tool) + " run"
		}
		switch a.Status {
		case trace.StatusSuccess:
			// agent_result / ConfidenceEvidence: the transcript recorded a result.
		case trace.StatusFailed:
			typ = "failed_agent"
			conf = ConfidenceUnknown // a failed run's output is not a reliable result
		case trace.StatusInterrupted:
			// Deliberately stopped (TaskStop): no result was produced, but it was
			// killed on purpose — flag distinctly so the recovering agent reconciles
			// against the digest rather than blindly resuming.
			typ = "stopped_agent"
			conf = ConfidenceUnknown
		default:
			// pending / active: launched but never completed or stopped — not
			// authoritative evidence; flag it so the recovering agent knows to resume
			// or re-run it rather than trust a blank result.
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

	// The session's last non-empty assistant message. It is provably the last thing
	// the assistant said (ConfidenceEvidence), but NOT necessarily the answer: a
	// session that ended mid-thought (quota/interrupt, or a trailing "let me first
	// check X") leaves a fragment here that is structurally indistinguishable from a
	// real wrap-up (same turn Status). So frame it descriptively, not as an
	// authoritative conclusion — the receiving agent judges it against digest.md.
	for _, turn := range slices.Backward(turns) {
		final := strings.TrimSpace(turn.AssistantFinal)
		if final == "" {
			continue
		}
		items = append(items, EvidenceItem{
			ID:         "final:" + turn.ID,
			Type:       typeLastAssistantMessage,
			Claim:      "last assistant message (may be the conclusion or a mid-thought cutoff — judge against digest.md): " + oneLine(final, 240),
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
	return textutil.OneLine(s, max)
}

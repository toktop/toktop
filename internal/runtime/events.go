package runtime

import (
	"time"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/trace"
)

func liveEventForFullIngest(summary ingest.Summary, reason string) liveevent.Event {
	return liveevent.Event{
		Type:          "ingest.full",
		Provider:      summary.Source,
		SourceID:      trace.SourceID(summary.Source),
		Status:        trace.StatusSuccess,
		Reason:        reason,
		TurnCount:     summary.TurnCount,
		RawEventCount: summary.RawEventCount,
	}
}

func liveEventForSessionIngest(result ingest.Result, path string) liveevent.Event {
	ev := liveevent.Event{
		Type:           "ingest.session",
		Provider:       result.Index.Source,
		SourceID:       trace.SourceID(result.Index.Source),
		File:           path,
		TranscriptPath: path,
		TurnCount:      result.Index.TurnCount,
		RawEventCount:  result.Index.RawEventCount,
		Status:         trace.StatusUnknown,
	}
	if len(result.Index.Sessions) == 0 {
		return ev
	}
	session := result.Index.Sessions[0]
	ev.SessionID = session.ID
	ev.ExternalSessionID = session.ExternalID
	ev.ProjectID = session.ProjectID
	ev.ProjectName = session.ProjectName
	ev.ProjectPath = session.ProjectPath
	ev.TranscriptPath = session.TranscriptPath
	ev.Status = liveStatusFromTrace(session.Status)
	ev.At = latestSessionActivity(session, result.Index.Turns)
	return ev
}

func latestSessionActivity(session trace.Session, turns []trace.Turn) time.Time {
	latest := session.EndedAt
	if session.StartedAt.After(latest) {
		latest = session.StartedAt
	}
	for _, turn := range turns {
		if turn.SessionID != "" && turn.SessionID != session.ID {
			continue
		}
		if turn.EndedAt.After(latest) {
			latest = turn.EndedAt
		}
		if turn.StartedAt.After(latest) {
			latest = turn.StartedAt
		}
	}
	return latest
}

func liveEventForActivity(provider, path string) liveevent.Event {
	ev := liveevent.Event{
		Type:           "session.activity",
		File:           path,
		TranscriptPath: path,
		Status:         trace.StatusActive,
	}
	if provider != "" {
		ev.Provider = provider
		ev.SourceID = trace.SourceID(provider)
	}
	return ev
}

func liveEventForDaemonState(state string) liveevent.Event {
	return liveevent.Event{
		Type:   "daemon.state",
		Status: state,
	}
}

func liveStatusFromTrace(status string) string {
	switch status {
	case trace.StatusCompleted, trace.StatusSuccess:
		return trace.StatusSuccess
	case trace.StatusFailed:
		return trace.StatusFailed
	case trace.StatusPending, trace.StatusActive, trace.StatusAwaitingConfirmation:
		return status
	default:
		return trace.StatusUnknown
	}
}

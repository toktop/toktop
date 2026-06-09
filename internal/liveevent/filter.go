package liveevent

import (
	"fmt"
	"strings"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// Target is an app:session watch filter for the live event stream.
type Target struct {
	Provider string
	SourceID string
	Session  string
}

// ParseWatchTargets parses comma/space-separated "app:session" tokens into
// deduplicated Targets.
func ParseWatchTargets(values []string) ([]Target, error) {
	seen := make(map[string]struct{}, len(values))
	targets := make([]Target, 0, len(values))
	for _, raw := range values {
		for _, part := range textutil.SplitTrim(raw) {
			app, session, ok := strings.Cut(part, ":")
			app = ingest.NormalizeName(app)
			session = strings.TrimSpace(session)
			if !ok || app == "" || session == "" {
				return nil, fmt.Errorf("invalid watch target %q: want app:session", part)
			}
			key := app + "\x00" + session
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, Target{
				Provider: app,
				SourceID: trace.SourceID(app),
				Session:  session,
			})
		}
	}
	return targets, nil
}

// AnyTargetMatches reports whether ev matches any target (an empty target set
// matches everything).
func AnyTargetMatches(targets []Target, ev Event) bool {
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		if target.Matches(ev) {
			return true
		}
	}
	return false
}

// Matches reports whether ev belongs to this target's provider and session.
func (t Target) Matches(ev Event) bool {
	if t.Provider == "" || t.Session == "" {
		return false
	}
	if ev.Provider != t.Provider && ev.SourceID != t.SourceID {
		return false
	}
	for _, candidate := range []string{ev.SessionID, ev.ExternalSessionID, ev.TranscriptPath, ev.File} {
		if strings.TrimSpace(candidate) == t.Session {
			return true
		}
	}
	return false
}

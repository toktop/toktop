package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/textutil"
)

// ErrNonPositivePruneAge is returned by RawPruneCutoff for a zero/negative age:
// the cutoff would land at or after now and match (delete) every raw event.
var ErrNonPositivePruneAge = errors.New("prune age must be a positive duration")

// RawPruneCutoff parses an ad-hoc "older than" duration for an age-based raw-events
// prune and returns the cutoff (now - age). It is the single validation point shared
// by the CLI (`data prune --raw-events-older-than`) and HTTP (`data:prune`) paths: a
// non-positive age is rejected here so neither surface can wipe the whole table by
// putting the cutoff at/after now. (Profile-based prunes don't use this — they go
// through Apply, which only prunes when RawAge > 0.)
func RawPruneCutoff(olderThan string, now time.Time) (time.Time, error) {
	age, err := time.ParseDuration(olderThan)
	if err != nil {
		return time.Time{}, err
	}
	if age <= 0 {
		return time.Time{}, ErrNonPositivePruneAge
	}
	return now.Add(-age), nil
}

// EventLogPruner is the only thing retention needs from the event-log store.
// Declaring it here instead of importing the eventlog package keeps retention
// free of that dependency, so the event log can live behind the daemon's
// package boundary; the concrete store satisfies it structurally. A nil pruner
// (e.g. an offline CLI run with no daemon) skips event-log pruning.
type EventLogPruner interface {
	Prune(ctx context.Context, before time.Time, keepN int) (int, error)
}

type Profile string

const (
	ProfilePrivacy  Profile = "privacy"
	ProfileBalanced Profile = "balanced"
	ProfileArchive  Profile = "archive"
)

type Policy struct {
	Profile           Profile
	RawAge            time.Duration
	RedactRawAfter    time.Duration
	EventLogAge       time.Duration
	EventLogMaxEvents int
}

func PolicyFor(profile Profile) (Policy, error) {
	const (
		defaultEventLogAge       = 7 * 24 * time.Hour
		defaultEventLogMaxEvents = 100_000
	)
	switch profile {
	case ProfilePrivacy:
		return Policy{Profile: profile, RawAge: 7 * 24 * time.Hour, RedactRawAfter: 24 * time.Hour, EventLogAge: defaultEventLogAge, EventLogMaxEvents: defaultEventLogMaxEvents}, nil
	case ProfileBalanced, "":
		return Policy{Profile: ProfileBalanced, RawAge: 90 * 24 * time.Hour, RedactRawAfter: 30 * 24 * time.Hour, EventLogAge: defaultEventLogAge, EventLogMaxEvents: defaultEventLogMaxEvents}, nil
	case ProfileArchive:
		return Policy{Profile: profile, EventLogAge: defaultEventLogAge, EventLogMaxEvents: defaultEventLogMaxEvents}, nil
	default:
		return Policy{}, fmt.Errorf("retention: unknown profile %q (want privacy|balanced|archive)", profile)
	}
}

type Report struct {
	Profile                Profile   `json:"profile"`
	DryRun                 bool      `json:"dry_run"`
	RawEventsAffected      int64     `json:"raw_events_affected"`
	NormalizedRowsRedacted int64     `json:"normalized_rows_redacted"`
	EventLogPruned         int       `json:"event_log_pruned"`
	Now                    time.Time `json:"now"`
}

func Apply(ctx context.Context, store *sqlite.Store, events EventLogPruner, policy Policy, now time.Time, dryRun bool) (Report, error) {
	report := Report{Profile: policy.Profile, DryRun: dryRun, Now: now}
	if policy.RawAge > 0 {
		count, err := store.PruneRawEvents(ctx, now.Add(-policy.RawAge), dryRun)
		if err != nil {
			return report, err
		}
		report.RawEventsAffected = count
	}
	if !dryRun && policy.RedactRawAfter > 0 {
		cutoff := now.Add(-policy.RedactRawAfter)
		normalized, err := store.RedactNormalized(ctx, cutoff)
		if err != nil {
			return report, err
		}
		report.NormalizedRowsRedacted = normalized
	}
	if !dryRun && events != nil && (policy.EventLogAge > 0 || policy.EventLogMaxEvents > 0) {
		var before time.Time
		if policy.EventLogAge > 0 {
			before = now.Add(-policy.EventLogAge)
		}
		deleted, err := events.Prune(ctx, before, policy.EventLogMaxEvents)
		if err != nil {
			return report, err
		}
		report.EventLogPruned = deleted
	}
	return report, nil
}

func FormatProfileList() string {
	var b strings.Builder
	for _, p := range []Profile{ProfilePrivacy, ProfileBalanced, ProfileArchive} {
		policy, _ := PolicyFor(p)
		fmt.Fprintf(&b, "%s\traw=%s\tredact_after=%s\n",
			p, textutil.FormatDuration(policy.RawAge), textutil.FormatDuration(policy.RedactRawAfter))
	}
	return b.String()
}

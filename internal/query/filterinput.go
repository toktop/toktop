package query

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/karrick/tparse/v2"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/trace"
)

func LooksLikeSourceID(v string) bool {
	if len(v) != 16 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func ResolveSourceFilter(v string) string {
	if LooksLikeSourceID(v) {
		return v
	}
	return trace.SourceID(v)
}

// ResolveSourceToken maps a raw --sources / ?source= token to the content-hashed
// source_id the store filters on: a value that already looks like an id passes
// through; otherwise a provider alias is normalized and validated against the
// registry. Shared by the CLI and HTTP filter builders so source alias resolution
// and validation cannot drift between the two surfaces.
func ResolveSourceToken(raw string) (string, error) {
	if LooksLikeSourceID(raw) {
		return raw, nil
	}
	name := ingest.NormalizeName(raw)
	if !ingest.HasProvider(name) {
		return "", fmt.Errorf("unknown source %q (registered: %s)", raw, strings.Join(ingest.SortedProviders(), ", "))
	}
	return ResolveSourceFilter(name), nil
}

// ValidateStatuses rejects any status outside the canonical set, so the CLI and
// HTTP filter builders share one check and one message.
func ValidateStatuses(statuses []string) error {
	valid := trace.StatusValues()
	for _, s := range statuses {
		if !slices.Contains(valid, s) {
			return fmt.Errorf("unknown status %q (want one of: %s)", s, strings.Join(valid, ", "))
		}
	}
	return nil
}

// ValidateToolCallStatuses rejects any status outside the tool-call grain
// (success/failed/rejected/unknown). The tool-call drill-down (`tools calls`,
// `GET /v1/tool-calls`) filters a call's own status, so a turn-only status like
// `interrupted` must error here rather than silently match zero rows. Shared by
// the CLI and HTTP so the check and message can't drift between surfaces.
func ValidateToolCallStatuses(statuses []string) error {
	valid := trace.ToolCallStatusValues()
	for _, s := range statuses {
		if !slices.Contains(valid, s) {
			return fmt.Errorf("unknown tool-call status %q (want one of: %s)", s, strings.Join(valid, ", "))
		}
	}
	return nil
}

// ParseSort splits a sort token like "started_desc" / "turns_asc" into its
// SortBy column and descending flag. A bare token (no _desc/_asc suffix) sorts
// ascending. Shared by the CLI and HTTP filter builders so their sort parsing
// stays in lockstep.
func ParseSort(order string) (sortBy string, desc bool) {
	if rest, ok := strings.CutSuffix(order, "_desc"); ok {
		return rest, true
	}
	if rest, ok := strings.CutSuffix(order, "_asc"); ok {
		return rest, false
	}
	return order, false
}

func ParseSince(value string, now time.Time) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("invalid since %q: want duration like 7d / 24h or RFC3339 timestamp", value)
	}
	if d, err := tparse.AbsoluteDuration(now, value); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("invalid since %q: duration must not be negative", value)
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid since %q: want duration like 7d / 24h or RFC3339 timestamp", value)
}

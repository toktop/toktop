package query

import (
	"fmt"
	"strings"
	"time"

	"github.com/karrick/tparse/v2"

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

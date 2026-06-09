// Package textutil holds tiny, stateless value<->string primitives that several
// layers (cli, httpapi, store, config, redact, liveevent, collectors) each used
// to re-implement byte-for-byte. It imports no other internal package, so every
// layer can depend on it without an import cycle — the neutral home the
// duplicated helpers never had.
package textutil

import (
	"strconv"
	"strings"
	"time"
)

// DedupNonEmpty trims each value, drops blanks, and dedups, preserving first
// occurrence order. Replaces the identical uniqueStrings/uniqueNonEmpty copies.
func DedupNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// SplitTrim splits a single comma-separated value, trims each part, and drops
// blanks — without deduping, so callers that dedup on a derived/normalized key
// (e.g. alias-folded sources, app:session watch targets) keep doing so.
func SplitTrim(value string) []string {
	var out []string
	for part := range strings.SplitSeq(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// FirstNonBlank returns the first value whose TrimSpace is non-empty, returned
// RAW (untrimmed). Callers wanting the trimmed form wrap with strings.TrimSpace,
// so the emptiness test is shared while each call site keeps its return shape.
func FirstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ParseOnOff parses the on/off boolean vocabulary shared by config, the SSE
// query flags, and the redact flag. on/true/yes/1 -> (true, true);
// off/false/no/0 -> (false, true); anything else (including "") -> (_, false).
// The empty-string and one-sided-default policies stay at each call site.
func ParseOnOff(value string) (on, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true, true
	case "off", "false", "no", "0":
		return false, true
	}
	return false, false
}

// FormatDuration renders a duration for human-facing status/diagnostics output:
// 0 -> "forever", a whole number of days -> "Nd", else time.Duration.String().
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "forever"
	}
	if d%(24*time.Hour) == 0 {
		return formatDays(d)
	}
	return d.String()
}

func formatDays(d time.Duration) string {
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

// FormatCount renders a count compactly for human-facing output: values below
// 1000 stay as the raw integer, larger values use one decimal place with a
// k/M/B suffix (1500 -> "1.5k", 14_820_336 -> "14.8M", 3_892_145_008 -> "3.9B").
// JSON/NDJSON and the HTTP API keep raw integers; this is display-only.
func FormatCount(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return formatScaled(n, 1000, "k")
	case n < 1_000_000_000:
		return formatScaled(n, 1_000_000, "M")
	default:
		return formatScaled(n, 1_000_000_000, "B")
	}
}

// formatScaled renders n/unit with one rounded decimal and a suffix, rolling a
// rounded-up fraction (e.g. 1_999_999 -> "2.0M") into the whole part.
func formatScaled(n, unit int, suffix string) string {
	whole := n / unit
	frac := ((n%unit)*10 + unit/2) / unit
	if frac >= 10 {
		whole++
		frac = 0
	}
	return strconv.Itoa(whole) + "." + strconv.Itoa(frac) + suffix
}

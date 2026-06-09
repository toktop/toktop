package redact

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/zricethezav/gitleaks/v8/detect"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

type Category = string

const (
	CategoryEnvSecret   Category = "env-secret"
	CategoryCookie      Category = "cookie-header"
	CategoryDatabaseURL Category = "database-url-password"
)

type Hit struct {
	Category Category

	Hash string
}

type Result struct {
	Original string
	Redacted string
	Hits     []Hit
}

const redactionFailedPlaceholder = "[REDACTED:scan-failed]"

// PolicyFromFlag parses a redact value ("on"/"off" plus tolerant aliases) into a
// Policy. An empty string defaults to enabled. Shared by the config loader and
// the config-set validation so both interpret values identically.
func PolicyFromFlag(flag string) (Policy, error) {
	on, ok := textutil.ParseOnOff(flag)
	if !ok {
		// An empty string defaults to enabled (the documented redact default);
		// any other unrecognized value is an error.
		if strings.TrimSpace(flag) == "" {
			return Policy{Enabled: true}, nil
		}
		return Policy{}, fmt.Errorf("redact: invalid value %q (want on|off)", flag)
	}
	if on {
		return Policy{Enabled: true}, nil
	}
	return Disabled, nil
}

func Apply(text string) Result {
	if text == "" {
		return Result{}
	}

	hits, ok := scanPooled(text)
	if !ok {
		return Result{Original: text, Redacted: redactionFailedPlaceholder}
	}
	if len(hits) == 0 {
		return Result{Original: text, Redacted: text}
	}

	// Replace by unique match string, longest first, replacing every occurrence.
	// Per-hit strings.Replace(..., 1) was order-dependent: redacting a short
	// match first could mutate the text so a longer overlapping match no longer
	// matched (leaking the non-overlapping bytes), and the count of 1 left
	// duplicate secrets in cleartext. Longest-first guarantees a containing
	// match is redacted before any substring, and ReplaceAll covers duplicates;
	// the failure mode is over-redaction (a coincidental literal elsewhere), not
	// a leak.
	type replacement struct {
		match       string
		placeholder string
	}
	seen := make(map[string]struct{}, len(hits))
	repls := make([]replacement, 0, len(hits))
	for _, h := range hits {
		if h.match == "" {
			continue
		}
		if _, dup := seen[h.match]; dup {
			continue
		}
		seen[h.match] = struct{}{}
		repls = append(repls, replacement{
			match:       h.match,
			placeholder: fmt.Sprintf("[REDACTED:%s:%s]", h.Category, h.Hash[:8]),
		})
	}
	slices.SortStableFunc(repls, func(a, b replacement) int {
		return cmp.Compare(len(b.match), len(a.match))
	})
	redacted := text
	for _, r := range repls {
		redacted = strings.ReplaceAll(redacted, r.match, r.placeholder)
	}

	out := make([]Hit, len(hits))
	for i, h := range hits {
		out[i] = Hit{Category: h.Category, Hash: h.Hash}
	}
	return Result{Original: text, Redacted: redacted, Hits: out}
}

type internalHit struct {
	Category Category
	Hash     string
	match    string
}

func scanPooled(text string) (hits []internalHit, ok bool) {
	d := detectorPool.Get().(*detect.Detector)
	// Return the detector to the pool on every path including a panic inside
	// DetectString; otherwise a panicking input churns detector construction
	// (reparsing the whole ruleset under detectorBuildMu) on each call.
	defer detectorPool.Put(d)
	defer func() {
		if r := recover(); r != nil {
			hits, ok = nil, false
		}
	}()
	hits = scanAll(d, text)
	return hits, true
}

type rangeKey struct {
	startLine, startColumn int
	ruleID                 string
}

func scanAll(d *detect.Detector, text string) []internalHit {
	var hits []internalHit
	// Allocated lazily: the overwhelmingly common case is secret-free text with
	// zero findings, where the dedupe map is never needed.
	var seenRanges map[rangeKey]bool

	for _, f := range d.DetectString(text) {
		if f.Secret == "" {
			continue
		}
		key := rangeKey{f.StartLine, f.StartColumn, f.RuleID}
		if seenRanges[key] {
			continue
		}
		if seenRanges == nil {
			seenRanges = map[rangeKey]bool{}
		}
		seenRanges[key] = true
		hits = append(hits, internalHit{
			Category: f.RuleID,
			Hash:     hashSegment(f.Secret),
			match:    f.Match,
		})
	}

	for _, rule := range toktopRules {
		for _, m := range rule.re.FindAllString(text, -1) {
			hits = append(hits, internalHit{
				Category: rule.category,
				Hash:     hashSegment(m),
				match:    m,
			})
		}
	}

	return hits
}

// Warm pre-builds a gitleaks detector and returns it to the pool so the first
// Apply on a latency-sensitive path (the live-event hot path) does not pay the
// one-time detector construction, which parses the full default ruleset (tens
// of ms). Intended to be called once when a server starts serving, off the hot
// path; safe to call concurrently and repeatedly.
func Warm() {
	detectorPool.Put(detectorPool.Get())
}

var detectorBuildMu sync.Mutex

var detectorPool = sync.Pool{
	New: func() any {
		detectorBuildMu.Lock()
		defer detectorBuildMu.Unlock()
		d, err := detect.NewDetectorDefaultConfig()
		if err != nil {

			panic("redact: failed to initialize gitleaks default detector: " + err.Error())
		}
		return d
	},
}

var toktopRules = []struct {
	category Category
	re       *regexp.Regexp
}{
	{CategoryEnvSecret, regexp.MustCompile(`(?im)^\s*[A-Z][A-Z0-9_]*?_(?:SECRET|TOKEN|PASSWORD|KEY)\s*=\s*\S+`)},
	{CategoryCookie, regexp.MustCompile(`(?i)Cookie:\s*[^\r\n]{1,400}`)},
	{CategoryDatabaseURL, regexp.MustCompile(`(?i)(?:postgres|postgresql|mysql|mongodb)(?:\+[a-z]+)?://[^:\s/@]+:[^@\s/]+@`)},
}

func hashSegment(s string) string {
	return trace.HashPayload([]byte(s))
}

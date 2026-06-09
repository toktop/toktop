package cli

import (
	"fmt"
	"strings"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/query"
)

// resolveTokens is the shared split/fold/dedup loop behind every --sources
// resolver. It splits repeat/comma tokens (splitFlagValues), maps each through
// the per-command rule f (alias-fold + that command's validation, or a
// passthrough), and dedups AFTER mapping. f returns the canonical token or an
// error (callers exit 2). An empty input yields an empty slice so callers apply
// their own policy (filter => all rows; scope => auto-detect; write => required).
func resolveTokens(values rootList, f func(raw string) (string, error)) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, raw := range splitFlagValues(values) {
		tok, err := f(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out, nil
}

// foldProvider folds an alias to its canonical provider name and validates it
// against the registry — the per-token rule shared by scope and filter commands.
func foldProvider(raw string) (string, error) {
	name := ingest.NormalizeName(raw)
	if !ingest.HasProvider(name) {
		return "", fmt.Errorf("unknown source %q (registered: %s)", raw, strings.Join(ingest.SortedProviders(), ", "))
	}
	return name, nil
}

// resolveSourceFlag resolves --sources for SCOPE commands: fold + validate each
// name, dedup. Unknown name => error (callers exit 2).
func resolveSourceFlag(values rootList) ([]string, error) {
	return resolveTokens(values, foldProvider)
}

// resolveFilterTokens resolves --sources for FILTER (query) commands: like
// resolveSourceFlag, but a raw 16-hex SourceID passes through unvalidated — the
// sole carve-out, so `--sources <16hexid>` keeps matching instead of exit-2.
func resolveFilterTokens(values rootList) ([]string, error) {
	return resolveTokens(values, func(raw string) (string, error) {
		if query.LooksLikeSourceID(raw) {
			return raw, nil
		}
		return foldProvider(raw)
	})
}

// scopeSources resolves the effective provider set for SCOPE commands (ingest,
// daemon): an explicit --sources wins; otherwise auto-detect every provider
// whose discovery roots exist on disk. It is the single combination of
// "explicit override else auto-detect" so ingest and the daemon stay in lockstep.
func scopeSources(values rootList, snap *config.Snapshot) ([]string, error) {
	explicit, err := resolveSourceFlag(values)
	if err != nil {
		return nil, err
	}
	if len(explicit) > 0 {
		return explicit, nil
	}
	present := ingest.PresentProviders(snap.Roots)
	if len(present) == 0 {
		// Nothing detected on disk (fresh machine, or no provider used yet): fall
		// back to all registered providers. Importing/watching an absent provider
		// discovers nothing and is a safe no-op, so this keeps `toktop ingest` and
		// `toktop daemon` working with zero config — and lets the autostart daemon
		// start (runtime.New requires a non-empty source set) — instead of erroring.
		return ingest.SortedProviders(), nil
	}
	return present, nil
}

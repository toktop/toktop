package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/textutil"
)

// readSessionTitles reads each root's session_index.jsonl — codex's MUTABLE,
// out-of-band thread-name index (NOT part of any rollout transcript; a rename appends
// a new line here, never touches the rollout) — and returns external session id ->
// cleaned title for the indexes that CHANGED since the last ingest, plus their
// (size, mtime, inode) fingerprints and paths.
//
// A root whose index fingerprint matches `known` is skipped: a title in an unchanged
// index was already applied by the run that first saw it, and a reingested rollout
// keeps its title via the store's preserve-on-reinsert — so the only work left for a
// changed index is new titles and renames (both of which grow/alter the file). An
// unchanged index therefore needs no UPDATE, and an idle reconcile costs only a stat.
// (Index entries are named only after a thread's rollout exists, so a brand-new
// session's title never sits in an already-recorded, unchanged index it would miss.)
// The returned fingerprints + paths are persisted via the trailing round's
// ingest_offsets so the skip works next run. A missing/unreadable index is not an error.
func readSessionTitles(ctx context.Context, roots []SourceRoot, known map[string]source.Fingerprint) (titles map[string]string, fingerprints map[string]source.Fingerprint, paths []string) {
	titles = make(map[string]string)
	fingerprints = make(map[string]source.Fingerprint)
	for _, root := range roots {
		path := filepath.Join(root.Path, "session_index.jsonl")
		size, mtimeNS, ino, ok := collector.StatFingerprint(path)
		if !ok {
			continue // absent index is not an error
		}
		fp := source.Fingerprint{Size: size, MtimeNS: mtimeNS, Ino: ino}
		if known[path] == fp {
			continue // unchanged since last ingest — nothing new to apply
		}
		readSessionIndexFile(ctx, path, titles)
		fingerprints[path] = fp
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, nil, nil
	}
	return titles, fingerprints, paths
}

func readSessionIndexFile(ctx context.Context, path string, titles map[string]string) {
	f, err := os.Open(path)
	if err != nil {
		return // absent / unreadable index is not an error
	}
	defer f.Close()
	// Reuse the shared long-line-safe JSONL reader rather than a hand-rolled scanner,
	// so a pathological line never silently truncates the rest of the index.
	_ = collector.ReadJSONLLines(ctx, f, func(_ int, _ int64, line []byte) error {
		var rec struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.ID == "" {
			return nil
		}
		// session_index.jsonl is append-only: a rename appends a new line for the same
		// id, so a later line wins. An empty/garbage name never overwrites a good title
		// (codex never emits an empty thread_name, so there is no "clear" to honor).
		if name := cleanTitle(rec.ThreadName); name != "" {
			titles[rec.ID] = name
		}
		return nil
	})
}

// cleanTitle normalizes a codex thread_name for storage: drops control characters,
// collapses whitespace, strips a TRAILING JSON tail codex's title generator
// occasionally leaks (e.g. "…还是5'} } }]}]} }" -> "…还是5"), and caps length.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return textutil.Truncate(stripLeakedJSONTail(textutil.CollapseSpaces(s)), 200)
}

// stripLeakedJSONTail removes a trailing run of JSON close-delimiters / quotes that
// codex's title generator occasionally leaks (e.g. "…还是5'} } }]}]} }"). It strips
// the trailing run only when the title carries MORE '}'/']' than matching openers —
// the unmatched excess a leaked tail adds — so a brace/bracket-balanced title
// ("set x = {}", "arr[0]") is kept, and a title ending only in ' ` * (never counted)
// is always kept. A title ending in an UNMATCHED ]/} is treated as leakage and the
// run is dropped (titles legitimately ending in a bare closer are vanishingly rare,
// and the field is a display-only projection).
func stripLeakedJSONTail(s string) string {
	trimmed := strings.TrimRight(s, " }]'`*")
	if trimmed == s {
		return s
	}
	if strings.Count(s, "}") > strings.Count(s, "{") || strings.Count(s, "]") > strings.Count(s, "[") {
		return trimmed
	}
	return s
}

package codex

import (
	"context"
	"log/slog"
	"time"

	"toktop.unceas.dev/internal/ingest"
	codexparser "toktop.unceas.dev/internal/parser/codex"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

// driver runs the shared ingest pipeline; only the provider-specific Source,
// ParserVersion, session-file accessors, Collect, and Parse differ.
var driver = ingest.NewDriver(ingest.Spec[SessionFile]{
	Source:        "codex",
	ParserVersion: codexparser.ParserVersion,
	PathOf:        func(f SessionFile) string { return f.Path },
	RootOf:        func(f SessionFile) string { return f.Root.Path },
	Collect:       CollectSessionFile,
	Parse: func(ctx context.Context, raw source.RawSession) (trace.Session, []trace.Turn, []trace.ParseError, error) {
		p, err := codexparser.ParseSession(ctx, raw)
		return p.Session, p.Turns, p.ParseErrors, err
	},
})

func Ingest(ctx context.Context, roots []SourceRoot, redactPolicy redact.Policy, known map[string]source.Fingerprint, sink ingest.BatchSink) (ingest.Summary, error) {
	sessions, err := DiscoverSessions(ctx, roots)
	if err != nil {
		return ingest.Summary{}, err
	}

	metadata := func(fingerprints map[string]source.Fingerprint) (ingest.Result, bool, error) {
		return codexMetadata(ctx, roots, fingerprints)
	}
	summary, err := driver.Stream(ctx, roots, redactPolicy, sessions, known, metadata, sink)
	if err != nil {
		return summary, err
	}
	// Out-of-band thread titles (session_index.jsonl) are applied as a trailing
	// title-only round, AFTER driver.Stream has persisted the rollouts (Stream runs
	// the metadata round BEFORE the session batches, so a title UPDATE there would
	// miss the not-yet-inserted rows on a first ingest). readSessionTitles reads only
	// the indexes that CHANGED since last ingest (new titles / renames grow the file),
	// so an idle reconcile does nothing and a run that merely reingested rollouts does
	// no title work — a reingested rollout keeps its title via the store's
	// preserve-on-reinsert. The Result carries the read indexes' paths + fingerprints
	// (persisted to ingest_offsets so the skip works next run; session_index.jsonl is
	// never discovered as a transcript, so the offset row is inert) and no
	// authoritative flags, so it only UPDATEs sessions.title and never reconciles
	// skills/MCP.
	titles, fingerprints, indexPaths := readSessionTitles(ctx, roots, known)
	if len(indexPaths) > 0 {
		redactPolicy.ApplyToTitleMap(titles)
		if err := sink(ctx, ingest.Result{
			Index:          trace.Index{Source: "codex", SourceRoots: ingest.RootPaths(roots), SessionTitles: titles},
			ProcessedFiles: indexPaths,
			Fingerprints:   fingerprints,
		}); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

func codexMetadata(ctx context.Context, roots []SourceRoot, fingerprints map[string]source.Fingerprint) (ingest.Result, bool, error) {
	meta := trace.Index{GeneratedAt: time.Now().UTC(), Source: "codex", ParserVersion: codexparser.ParserVersion, SourceRoots: ingest.RootPaths(roots)}
	if !attachDeclaredMCPServers(ctx, &meta, roots) {
		return ingest.Result{}, false, nil
	}
	trace.InternIndexStrings(&meta)
	// Codex authoritatively covers both kinds this round: it scanned MCP servers and
	// has no skills concept (the authoritative skill set is empty), so both reconcile
	// — matching the pre-decouple behavior of deleting any stray rows for this source.
	return ingest.Result{
		Index:                   meta,
		ProcessedFiles:          []string{},
		Fingerprints:            fingerprints,
		AuthoritativeSkills:     true,
		AuthoritativeMCPServers: true,
	}, true, nil
}

// attachDeclaredMCPServers appends scanned MCP servers to the metadata index. It
// returns false — skipping the whole metadata round, so no reconcile/delete runs
// — when the scan errored or was incomplete, because the metadata-only save path
// deletes stored rows absent from the scan and a partial scan is not authority.
func attachDeclaredMCPServers(ctx context.Context, index *trace.Index, roots []SourceRoot) bool {
	found, complete, err := scanDeclaredMCPServers(ctx, roots)
	if err != nil {
		slog.Warn("codex mcp scan failed", "err", err)
		return false
	}
	if !complete {
		slog.Warn("skip codex mcp metadata reconcile: scan incomplete")
		return false
	}
	index.MCPServers = append(index.MCPServers, found...)
	return true
}

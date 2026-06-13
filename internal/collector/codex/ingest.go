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
	return driver.Stream(ctx, roots, redactPolicy, sessions, known, metadata, sink)
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

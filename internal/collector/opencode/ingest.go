package opencode

import (
	"context"
	"log/slog"
	"time"

	"toktop.unceas.dev/internal/ingest"
	opencodeparser "toktop.unceas.dev/internal/parser/opencode"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

// driver runs the shared ingest pipeline. opencode is DB-backed, so it supplies
// FingerprintOf (the per-session event_sequence.seq, in Fingerprint.Token) and
// ByteSizeOf (a per-session byte estimate, since the fingerprint Size is 0) in
// addition to the usual Collect/Parse.
var driver = ingest.NewDriver(ingest.Spec[SessionFile]{
	Source:        "opencode",
	ParserVersion: opencodeparser.ParserVersion,
	PathOf:        PathOf,
	RootOf:        RootOf,
	Collect:       CollectSessionFile,
	FingerprintOf: FingerprintOf,
	ByteSizeOf:    ByteSizeOf,
	Parse: func(ctx context.Context, raw source.RawSession) (trace.Session, []trace.Turn, []trace.ParseError, error) {
		p, err := opencodeparser.ParseSession(ctx, raw)
		return p.Session, p.Turns, p.ParseErrors, err
	},
})

func Ingest(ctx context.Context, roots []SourceRoot, redactPolicy redact.Policy, known map[string]source.Fingerprint, sink ingest.BatchSink) (ingest.Summary, error) {
	sessions, err := DiscoverSessions(ctx, roots)
	if err != nil {
		return ingest.Summary{}, err
	}
	metadata := func(fingerprints map[string]source.Fingerprint) (ingest.Result, bool, error) {
		return opencodeMetadata(ctx, roots, fingerprints)
	}
	// Title rides the transcript (session.title column), set by the parser on
	// Session.Title directly — opencode needs no trailing out-of-band title round
	// (that is codex's session_index.jsonl machinery).
	return driver.Stream(ctx, roots, redactPolicy, sessions, known, metadata, sink)
}

func opencodeMetadata(ctx context.Context, roots []SourceRoot, fingerprints map[string]source.Fingerprint) (ingest.Result, bool, error) {
	meta := trace.Index{GeneratedAt: time.Now().UTC(), Source: "opencode", ParserVersion: opencodeparser.ParserVersion, SourceRoots: ingest.RootPaths(roots)}
	if !attachDeclaredMCPServers(ctx, &meta, roots) {
		return ingest.Result{}, false, nil
	}
	trace.InternIndexStrings(&meta)
	// opencode authoritatively covers both kinds this round: the MCP config scan
	// completed, and skills are not yet scanned (authoritative empty set) — so both
	// reconcile, deleting any stray rows for this source, matching codex.
	return ingest.Result{
		Index:                   meta,
		ProcessedFiles:          []string{},
		Fingerprints:            fingerprints,
		AuthoritativeSkills:     true,
		AuthoritativeMCPServers: true,
	}, true, nil
}

// attachDeclaredMCPServers appends scanned MCP servers to the metadata index,
// returning false (skip the whole metadata reconcile) when the scan errored or
// was incomplete — a partial scan is not authority to delete stored rows.
func attachDeclaredMCPServers(ctx context.Context, index *trace.Index, roots []SourceRoot) bool {
	found, complete, err := scanDeclaredMCPServers(ctx, roots)
	if err != nil {
		slog.Warn("opencode mcp scan failed", "err", err)
		return false
	}
	if !complete {
		slog.Warn("skip opencode mcp metadata reconcile: scan incomplete")
		return false
	}
	index.MCPServers = append(index.MCPServers, found...)
	return true
}

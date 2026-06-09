package claudecode

import (
	"context"
	"log/slog"
	"time"

	"toktop.unceas.dev/internal/ingest"
	claudeparser "toktop.unceas.dev/internal/parser/claudecode"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

// driver runs the shared ingest pipeline; only the provider-specific Source,
// ParserVersion, session-file accessors, Collect, and Parse differ.
var driver = ingest.NewDriver(ingest.Spec[SessionFile]{
	Source:        "claude-code",
	ParserVersion: claudeparser.ParserVersion,
	PathOf:        func(f SessionFile) string { return f.Path },
	RootOf:        func(f SessionFile) string { return f.Root.Path },
	Collect:       CollectSessionFile,
	Parse: func(ctx context.Context, raw source.RawSession) (trace.Session, []trace.Turn, []trace.ParseError, error) {
		p, err := claudeparser.ParseSession(ctx, raw)
		return p.Session, p.Turns, p.ParseErrors, err
	},
})

func Ingest(ctx context.Context, roots []SourceRoot, redactPolicy redact.Policy, known map[string]source.Fingerprint, sink ingest.BatchSink) (ingest.Summary, error) {
	sessions, err := DiscoverSessions(ctx, roots)
	if err != nil {
		return ingest.Summary{}, err
	}

	metadata := func(fingerprints map[string]source.Fingerprint) (ingest.Result, bool, error) {
		return claudeMetadata(ctx, roots, sessions, fingerprints)
	}
	return driver.Stream(ctx, roots, redactPolicy, sessions, known, metadata, sink)
}

func claudeMetadata(ctx context.Context, roots []SourceRoot, sessions []SessionFile, fingerprints map[string]source.Fingerprint) (ingest.Result, bool, error) {
	userRoots := ingest.RootPaths(roots)
	meta := trace.Index{GeneratedAt: time.Now().UTC(), Source: "claude-code", ParserVersion: claudeparser.ParserVersion, SourceRoots: userRoots}

	// Derive the unique project-path set once and share it across both scans,
	// instead of building meta.Sessions only to have each scan re-dedup it.
	seen := make(map[string]struct{}, len(sessions))
	projectPaths := make([]string, 0, len(sessions))
	for _, sf := range sessions {
		if sf.ProjectPath == "" {
			continue
		}
		if _, ok := seen[sf.ProjectPath]; ok {
			continue
		}
		seen[sf.ProjectPath] = struct{}{}
		projectPaths = append(projectPaths, sf.ProjectPath)
	}

	attachInstalledSkills(ctx, &meta, userRoots, projectPaths)
	attachDeclaredMCPServers(ctx, &meta, userRoots, projectPaths)
	if len(meta.Skills) == 0 && len(meta.MCPServers) == 0 {
		return ingest.Result{}, false, nil
	}
	trace.InternIndexStrings(&meta)
	return ingest.Result{Index: meta, ProcessedFiles: []string{}, Fingerprints: fingerprints}, true, nil
}

func attachInstalledSkills(ctx context.Context, index *trace.Index, userRoots, projectPaths []string) {
	found, err := scanInstalledSkills(ctx, scanOptions{UserRoots: userRoots, ProjectPaths: projectPaths})
	if err != nil {
		slog.Warn("skill scan failed", "err", err)
		return
	}
	index.Skills = append(index.Skills, found...)
}

func attachDeclaredMCPServers(ctx context.Context, index *trace.Index, userRoots, projectPaths []string) {
	found, err := scanDeclaredMCPServers(ctx, scanOptions{UserRoots: userRoots, ProjectPaths: projectPaths})
	if err != nil {
		slog.Warn("mcp scan failed", "err", err)
		return
	}
	index.MCPServers = append(index.MCPServers, found...)
}

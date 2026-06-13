package claudecode

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"toktop.unceas.dev/internal/collector"
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

	// Derive the project-path set once and share it across both scans. Discovery
	// paths are lossy fallbacks; ~/.claude.json carries declared project paths that
	// may not have transcripts yet and also fixes hyphen-decoded project names.
	projectPaths := make([]string, 0, len(sessions))
	for _, sf := range sessions {
		if sf.ProjectPath == "" {
			continue
		}
		projectPaths = append(projectPaths, sf.ProjectPath)
	}
	var claudeUser *claudeUserConfig
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".claude.json")
		doc, found, ok := loadClaudeUserConfig(path)
		if !ok {
			// ~/.claude.json feeds project-path discovery for BOTH the skills and
			// MCP scans (and declares user MCP servers). An unreadable/malformed one
			// would make every scan a partial set, so reconciling either kind could
			// delete stored rows for claude.json-declared projects. Skip the whole
			// round — claude.json is a shared precondition for both kinds.
			return ingest.Result{}, false, nil
		}
		if found {
			claudeUser = &doc
			projectPaths = append(projectPaths, doc.projectPaths()...)
		}
	}
	projectPaths = collector.UniqueStrings(projectPaths)

	opts := scanOptions{UserRoots: userRoots, ProjectPaths: projectPaths, ClaudeUser: claudeUser}
	// The skills (skill dirs) and MCP (.mcp.json/settings) scans have INDEPENDENT
	// failure sources, so a failure of one must not suppress the other's reconcile.
	// Each kind is reconciled only when its own scan was authoritative (complete).
	authoritativeSkills := attachInstalledSkills(ctx, &meta, opts)
	authoritativeMCP := attachDeclaredMCPServers(ctx, &meta, opts)
	if !authoritativeSkills && !authoritativeMCP {
		return ingest.Result{}, false, nil
	}
	trace.InternIndexStrings(&meta)
	return ingest.Result{
		Index:                   meta,
		ProcessedFiles:          []string{},
		Fingerprints:            fingerprints,
		AuthoritativeSkills:     authoritativeSkills,
		AuthoritativeMCPServers: authoritativeMCP,
	}, true, nil
}

// attachInstalledSkills appends scanned skills to the metadata index. It returns
// false — skipping the whole metadata round, so no reconcile/delete runs — when
// the scan errored or was incomplete, because the metadata-only save path
// deletes stored rows absent from the scan and a partial scan is not authority.
func attachInstalledSkills(ctx context.Context, index *trace.Index, opts scanOptions) bool {
	found, complete, err := scanInstalledSkills(ctx, opts)
	if err != nil {
		slog.Warn("skill scan failed", "err", err)
		return false
	}
	if !complete {
		slog.Warn("skip claude skill metadata reconcile: scan incomplete")
		return false
	}
	index.Skills = append(index.Skills, found...)
	return true
}

func attachDeclaredMCPServers(ctx context.Context, index *trace.Index, opts scanOptions) bool {
	found, complete, err := scanDeclaredMCPServers(ctx, opts)
	if err != nil {
		slog.Warn("mcp scan failed", "err", err)
		return false
	}
	if !complete {
		slog.Warn("skip claude mcp metadata reconcile: scan incomplete")
		return false
	}
	index.MCPServers = append(index.MCPServers, found...)
	return true
}

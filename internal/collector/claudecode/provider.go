package claudecode

import (
	"context"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/source"
)

type provider struct{}

func init() { ingest.Register(provider{}) }

func (provider) Name() string { return "claude-code" }

func (provider) Aliases() []string { return []string{"claude", "claudecode"} }

func (provider) WatchSubdir() string { return "projects" }

func (provider) TranscriptExt() string { return ".jsonl" }

func (provider) ResolveRoots(explicit, file []string) []ingest.SourceRoot {
	// resolveRoots returns []SourceRoot, which is an alias of []ingest.SourceRoot
	// (see discover.go), so no conversion is needed.
	return resolveRoots(explicit, file)
}

func (provider) Ingest(ctx context.Context, roots []string, policy redact.Policy, known map[string]source.Fingerprint, sink ingest.BatchSink) (ingest.Summary, error) {
	return Ingest(ctx, DiscoverRoots(roots), policy, known, sink)
}

func (provider) IngestFile(ctx context.Context, roots []string, policy redact.Policy, path string) (ingest.Result, bool, error) {
	sourceRoots := DiscoverRoots(roots)
	file, ok := SessionFileFromPath(path, sourceRoots)
	if !ok {
		return ingest.Result{}, false, nil
	}
	res, err := driver.IngestSessionFile(ctx, sourceRoots, file, policy)
	if err != nil {
		return ingest.Result{}, true, err
	}
	return res, true, nil
}

package ingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/trace"
)

func LoadFingerprints(ctx context.Context, store *sqlite.Store, sourceName string) (map[string]source.Fingerprint, error) {
	return store.LoadIngestFingerprints(ctx, trace.SourceID(sourceName))
}

func RunFull(ctx context.Context, store *sqlite.Store, opts Options) (Summary, error) {
	provider, ok := ProviderFor(opts.Source)
	if !ok {
		return Summary{}, fmt.Errorf("unsupported source %q", opts.Source)
	}
	known, err := LoadFingerprints(ctx, store, opts.Source)
	if err != nil {
		return Summary{}, fmt.Errorf("load fingerprints: %w", err)
	}

	sink := func(ctx context.Context, batch Result) error {
		return store.SaveIngestPartial(ctx, batch.Index, batch.RawEventList, batch.ProcessedFiles, batch.Fingerprints, batch.AuthoritativeSkills, batch.AuthoritativeMCPServers)
	}
	summary, err := provider.Ingest(ctx, opts.Roots, opts.Policy, known, sink)
	if err != nil {
		return Summary{}, fmt.Errorf("ingest %s: %w", opts.Source, err)
	}

	if err := purgeVanished(ctx, store, opts.Source, known, summary.Fingerprints); err != nil {
		return Summary{}, fmt.Errorf("purge vanished transcripts for %s: %w", opts.Source, err)
	}
	summary.Source = opts.Source
	return summary, nil
}

func purgeVanished(ctx context.Context, store *sqlite.Store, sourceName string, known, present map[string]source.Fingerprint) error {
	var gone []string
	for file := range known {
		if _, ok := present[file]; ok {
			continue
		}
		if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, file)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	return store.DeleteSourceFiles(ctx, sourceName, gone)
}

func RunFile(ctx context.Context, opts Options, path string) (Result, bool, error) {
	provider, ok := ProviderFor(opts.Source)
	if !ok {
		return Result{}, false, fmt.Errorf("unsupported source %q", opts.Source)
	}
	result, claimed, err := provider.IngestFile(ctx, opts.Roots, opts.Policy, path)
	if err != nil {
		return Result{}, claimed, fmt.Errorf("ingest %s file: %w", opts.Source, err)
	}
	return result, claimed, nil
}

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"toktop.unceas.dev/internal/diagnostics"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/paths"
	"toktop.unceas.dev/internal/retention"
	"toktop.unceas.dev/internal/runtime"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/trace"
)

func initSlog(stderr io.Writer) {
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func runIngest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	var sourcesFlag rootList
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&sourcesFlag, "sources", "providers to import (default: auto-detected on-disk providers); may be repeated or comma-separated")
	setFlagUsage(fs, "usage: toktop ingest [flags]", "One-shot import of provider transcripts into the local store (idempotent).")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	loader, err := configFor(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	snap := loader.Current()
	policy := snap.RedactPolicy

	// No --sources: import every provider whose roots exist on disk (auto-detect).
	// A provider whose roots are absent discovers no files, so this never misfires.
	sources, serr := scopeSources(sourcesFlag, snap)
	if serr != nil {
		cliErr(stderr, serr)
		return 2
	}

	store, err := sqlite.Open(ctx, paths.DataDirUnder(home))
	if err != nil {
		cliErrf(stderr, "open store: %v", err)
		return 1
	}
	defer store.Close()

	succeeded := 0
	var firstErr error
	for _, source := range sources {
		summary, err := ingest.RunFull(ctx, store, ingest.Options{Source: source, Roots: snap.Roots[source], Policy: policy})
		if err != nil {
			// One line per provider, with the failure reason. Keep importing the
			// remaining sources so one provider failing doesn't abandon the rest.
			fmt.Fprintf(stderr, "%s: error: %v\n", source, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Fprintln(stdout, formatIngestSummary(summary))
		succeeded++
	}
	// Only a clean failure (every requested source errored) is non-zero; a source
	// that simply found no transcripts is a successful, empty import.
	if succeeded == 0 && firstErr != nil {
		return 1
	}
	return 0
}

func formatIngestSummary(summary ingest.Summary) string {
	// Files counts discovered transcripts, not just changed ones, so zero means
	// nothing is on disk for this provider (not installed, or no sessions yet).
	if summary.Files == 0 {
		return summary.Source + ": no transcripts found"
	}
	line := fmt.Sprintf("%s: %d files, %d sessions, %d turns, %d invocations, %d tool calls, %d raw events",
		summary.Source, summary.Files, summary.SessionCount, summary.TurnCount,
		summary.InvocationCount, summary.ToolCallCount, summary.RawEventCount)
	if summary.ParseErrorCount > 0 {
		line += fmt.Sprintf(", %d parse errors", summary.ParseErrorCount)
	}
	return line
}

// runPrune is the single `data prune` entry point: prune by retention profile
// (--profile, full lifecycle + daemon-aware) OR by an ad-hoc raw-events age
// cutoff (--raw-events-older-than). Exactly one mode is required.
func runPrune(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	profile := ""
	olderThan := ""
	dryRun := false
	token := ""
	noAuth := false
	fs := flag.NewFlagSet("data prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&profile, "profile", profile, "retention profile: privacy|balanced|archive (full lifecycle, daemon-aware)")
	fs.StringVar(&olderThan, "raw-events-older-than", olderThan, "ad-hoc raw-events prune by age, e.g. 720h")
	fs.BoolVar(&dryRun, "dry-run", dryRun, "count rows but do not delete or redact")
	fs.StringVar(&token, "token", token, "bearer token (default: read api-token file)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "do not send a bearer token")
	setFlagUsage(fs, "usage: toktop data prune (--profile <p> | --raw-events-older-than <dur>) [--dry-run]",
		"Prune stored data either by retention profile (full lifecycle) or by an ad-hoc",
		"raw-events age cutoff. Exactly one of --profile / --raw-events-older-than is required.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	switch {
	case profile != "" && olderThan != "":
		cliErrf(stderr, "data prune: use either --profile or --raw-events-older-than, not both")
		return 2
	case profile != "":
		return runProfilePrune(ctx, home, profile, dryRun, token, noAuth, stdout, stderr)
	case olderThan != "":
		return runAgePrune(ctx, home, olderThan, dryRun, stdout, stderr)
	default:
		cliErrf(stderr, "data prune requires --profile <privacy|balanced|archive> or --raw-events-older-than <duration>")
		return 2
	}
}

func runAgePrune(ctx context.Context, home, olderThan string, dryRun bool, stdout, stderr io.Writer) int {
	// Shared validation/cutoff with the HTTP data:prune path — a non-positive age is
	// rejected here so neither surface can wipe the whole raw_events table.
	cutoff, err := retention.RawPruneCutoff(olderThan, time.Now())
	if err != nil {
		cliErrf(stderr, "invalid --raw-events-older-than: %v", err)
		return 2
	}
	store, err := sqlite.Open(ctx, paths.DataDirUnder(home))
	if err != nil {
		cliErrf(stderr, "open store: %v", err)
		return 1
	}
	defer store.Close()
	count, err := store.PruneRawEvents(ctx, cutoff, dryRun)
	if err != nil {
		cliErrf(stderr, "prune: %v", err)
		return 1
	}
	if dryRun {
		fmt.Fprintf(stdout, "raw events matching prune: %d\n", count)
	} else {
		fmt.Fprintf(stdout, "raw events pruned: %d\n", count)
	}
	return 0
}

type rootList []string

type sessionDetail struct {
	Session trace.Session `json:"session"`
	Turns   []trace.Turn  `json:"turns"`
}

func (r *rootList) String() string {
	return strings.Join(*r, ",")
}

func (r *rootList) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func loadIndex(ctx context.Context, home string) (trace.Index, error) {
	store, err := sqlite.Open(ctx, paths.DataDirUnder(home))
	if err != nil {
		return trace.Index{}, err
	}
	defer store.Close()
	// CLI export/summary/suggestions load the full history (zero since); the
	// HTTP recompute path scopes its own window (see query.RecomputeSuggestions).
	return store.LoadIndex(ctx, time.Time{})
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		cliErrf(stderr, "write json: %v", err)
		return 1
	}
	return 0
}

func marshalNDJSON(index trace.Index) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	for _, session := range index.Sessions {
		if err := encoder.Encode(map[string]any{"type": "session", "session": session}); err != nil {
			return nil, err
		}
	}
	for _, turn := range index.Turns {
		if err := encoder.Encode(map[string]any{"type": "turn", "turn": turn}); err != nil {
			return nil, err
		}
	}
	for _, invocation := range index.Invocations {
		if err := encoder.Encode(map[string]any{"type": "invocation", "invocation": invocation}); err != nil {
			return nil, err
		}
	}
	return []byte(strings.TrimRight(builder.String(), "\n")), nil
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func runInit(_ context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setFlagUsage(fs, "usage: toktop init", "Create the local config and data directories under the resolved home.")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}

	configDir := paths.ConfigDirUnder(home)
	dataDir := paths.DataDirUnder(home)
	for _, dir := range []string{configDir, dataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			cliErrf(stderr, "create %s: %v", dir, err)
			return 1
		}
	}

	fmt.Fprintf(stdout, "home dir: %s\n", home)
	fmt.Fprintf(stdout, "config dir: %s\n", configDir)
	fmt.Fprintf(stdout, "data dir: %s\n", dataDir)
	return 0
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	var sourcesFlag rootList
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&sourcesFlag, "sources", "providers to check (default: auto-detected on-disk providers); may be repeated or comma-separated")
	setFlagUsage(fs, "usage: toktop doctor [flags]", "Check local environment readiness (dirs, DB, sqlite/fts5, per-provider hooks/roots).")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}

	providers, ferr := resolveSourceFlag(sourcesFlag)
	if ferr != nil {
		cliErr(stderr, ferr)
		return 2
	}
	explicit := len(providers) > 0

	// Resolve roots through the config loader so config.json roots are reflected
	// in doctor's checks, consistent with the daemon's actual discovery.
	var rootsByProvider map[string][]string
	if loader, lerr := configFor(ctx, home); lerr == nil {
		rootsByProvider = loader.Current().Roots
	}
	// No --sources: check every provider whose roots exist on disk, so doctor
	// never silently skips an installed provider (the original claude-code-only bug).
	if !explicit {
		providers = ingest.PresentProviders(rootsByProvider)
	}

	exitCode := 0
	emit := func(results []diagnostics.CheckResult) {
		for _, result := range results {
			fmt.Fprintf(stdout, "%-5s %-14s %s", result.Status, result.Name, result.Detail)
			if result.Message != "" {
				fmt.Fprintf(stdout, "  %s", result.Message)
			}
			fmt.Fprintln(stdout)
			if result.Status == diagnostics.StatusError {
				exitCode = 1
			}
		}
	}

	// Provider-neutral checks run once, regardless of how many providers exist.
	emit(diagnostics.RunNeutral(ctx, diagnostics.Options{
		ConfigDir: paths.ConfigDirUnder(home),
		DataDir:   paths.DataDirUnder(home),
	}))
	for _, name := range providers {
		fmt.Fprintf(stdout, "-- %s --\n", name)
		emit(diagnostics.RunProvider(ctx, name, runtime.WatchDirs(name, rootsByProvider[name]), hooksInstalled(name)))
	}

	// Never silently default to one provider: when auto-detecting, say what was
	// checked and how to scope it.
	switch {
	case len(providers) == 0:
		fmt.Fprintln(stdout, "no provider transcripts found on disk; run `toktop sources` to inspect roots, or pass --sources")
	case !explicit:
		fmt.Fprintf(stdout, "checked auto-detected providers: %s. pass --sources to scope, or run `toktop sources`.\n", strings.Join(providers, ", "))
	}

	return exitCode
}

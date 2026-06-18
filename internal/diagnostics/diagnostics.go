package diagnostics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3"

	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/retention"
	toktopsqlite "toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/textutil"
)

type Status string

const (
	StatusOK    Status = "OK"
	StatusWarn  Status = "WARN"
	StatusError Status = "ERROR"
	StatusInfo  Status = "INFO"
)

type CheckResult struct {
	Status  Status
	Name    string
	Detail  string
	Message string
}

type Options struct {
	ConfigDir string
	DataDir   string
}

// RunNeutral runs the provider-neutral checks (environment, store, sqlite,
// retention) exactly once per `toktop doctor`, regardless of how many providers
// are present. Provider-specific checks live in RunProvider.
func RunNeutral(ctx context.Context, opts Options) []CheckResult {
	results := make([]CheckResult, 0, 12)
	results = append(results, runtimeInfo())
	results = append(results, checkDirectory(ctx, "config dir", opts.ConfigDir))
	dataResult := checkDirectory(ctx, "data dir", opts.DataDir)
	results = append(results, dataResult)
	if dataResult.Status == StatusOK {
		results = append(results, checkWritable(ctx, "data writable", opts.DataDir))
		results = append(results, checkDatabaseSize(opts.DataDir))
		results = append(results, checkStoreCounts(ctx, opts.DataDir)...)
	}
	results = append(results, checkSQLite(ctx))
	results = append(results, checkFTS5(ctx))
	results = append(results, checkTrigram(ctx))
	results = append(results, checkRetention())
	return results
}

// RunProvider runs the per-provider checks (hooks, source roots, fsnotify) for a
// single source. runDoctor calls it once per resolved provider under a section
// header, so the neutral checks above are never repeated.
func RunProvider(ctx context.Context, source string, sourceDirs []string, hooksInstalled bool) []CheckResult {
	results := make([]CheckResult, 0, 3)
	results = append(results, checkHooks(hooksInstalled))
	results = append(results, checkSourceDirs(ctx, source, sourceDirs)...)
	if ingest.TranscriptExt(source) == "" {
		// A provider with no transcript extension isn't file-watched — the daemon's
		// watcher skips registering it (an empty ext can never pass ShouldIngest), so
		// probing fsnotify here would falsely imply a live watch. Its trace ingest
		// runs via the periodic reconcile instead.
		results = append(results, CheckResult{
			Status:  StatusInfo,
			Name:    "fsnotify",
			Detail:  source,
			Message: "not file-watched (DB-based provider; ingested via periodic reconcile)",
		})
	} else {
		results = append(results, checkFSNotify(ctx, sourceDirs))
	}
	return results
}

func runtimeInfo() CheckResult {
	return CheckResult{
		Status:  StatusInfo,
		Name:    "go runtime",
		Detail:  runtime.Version(),
		Message: fmt.Sprintf("GOMAXPROCS=%d", runtime.GOMAXPROCS(0)),
	}
}

func checkDirectory(ctx context.Context, name, dir string) CheckResult {
	if err := ctx.Err(); err != nil {
		return CheckResult{Status: StatusError, Name: name, Detail: dir, Message: err.Error()}
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return CheckResult{Status: StatusWarn, Name: name, Detail: dir, Message: "not found; run `toktop init`"}
		}
		return CheckResult{Status: StatusError, Name: name, Detail: dir, Message: err.Error()}
	}
	if !info.IsDir() {
		return CheckResult{Status: StatusError, Name: name, Detail: dir, Message: "path exists but is not a directory"}
	}
	return CheckResult{Status: StatusOK, Name: name, Detail: dir, Message: "available"}
}

func checkSQLite(ctx context.Context) CheckResult {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return CheckResult{Status: StatusError, Name: "sqlite", Detail: "driver", Message: err.Error()}
	}
	defer db.Close()
	var version string
	if err := db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		return CheckResult{Status: StatusError, Name: "sqlite", Detail: "query", Message: err.Error()}
	}
	return CheckResult{Status: StatusOK, Name: "sqlite", Detail: version, Message: "available"}
}

func checkFTS5(ctx context.Context) CheckResult {
	if err := toktopsqlite.FTS5Available(ctx); err != nil {
		return CheckResult{Status: StatusError, Name: "sqlite fts5", Detail: "unavailable", Message: "build with -tags sqlite_fts5: " + err.Error()}
	}
	return CheckResult{Status: StatusOK, Name: "sqlite fts5", Detail: "available", Message: "full-text search enabled"}
}

func checkTrigram(ctx context.Context) CheckResult {
	if err := toktopsqlite.TrigramAvailable(ctx); err != nil {
		return CheckResult{Status: StatusWarn, Name: "sqlite trigram", Detail: "unavailable", Message: "substring search falls back to LIKE; rebuild SQLite with FTS5+trigram for best results"}
	}
	return CheckResult{Status: StatusOK, Name: "sqlite trigram", Detail: "available", Message: "substring search enabled"}
}

func checkWritable(ctx context.Context, name, dir string) CheckResult {
	if err := ctx.Err(); err != nil {
		return CheckResult{Status: StatusError, Name: name, Detail: dir, Message: err.Error()}
	}
	file, err := os.CreateTemp(dir, ".toktop-write-check-*")
	if err != nil {
		return CheckResult{Status: StatusError, Name: name, Detail: dir, Message: err.Error()}
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return CheckResult{Status: StatusError, Name: name, Detail: dir, Message: err.Error()}
	}
	if err := os.Remove(path); err != nil {
		return CheckResult{Status: StatusWarn, Name: name, Detail: dir, Message: err.Error()}
	}
	return CheckResult{Status: StatusOK, Name: name, Detail: dir, Message: "writable"}
}

func checkDatabaseSize(dataDir string) CheckResult {
	info, err := os.Stat(toktopsqlite.DBPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return CheckResult{Status: StatusInfo, Name: "database", Detail: toktopsqlite.DBPath(dataDir), Message: "not yet created"}
		}
		return CheckResult{Status: StatusWarn, Name: "database", Detail: toktopsqlite.DBPath(dataDir), Message: err.Error()}
	}
	return CheckResult{Status: StatusInfo, Name: "database", Detail: toktopsqlite.DBPath(dataDir), Message: formatBytes(info.Size())}
}

func checkStoreCounts(ctx context.Context, dataDir string) []CheckResult {
	dbPath := toktopsqlite.DBPath(dataDir)
	if _, err := os.Stat(dbPath); err != nil {
		// Doctor must stay read-only: never materialize a DB as a side effect.
		// checkDatabaseSize already reports the missing/unreadable file.
		return nil
	}
	// Open read-only (mode=ro) so the probe never creates dirs, applies WAL
	// pragmas, or runs migrations the way toktopsqlite.Open would.
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return []CheckResult{{Status: StatusWarn, Name: "store", Detail: dataDir, Message: err.Error()}}
	}
	defer db.Close()
	row := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(imported_at), '') FROM raw_events`)
	var (
		raw    int
		latest string
	)
	if err := row.Scan(&raw, &latest); err != nil {
		return []CheckResult{{Status: StatusWarn, Name: "store", Detail: "raw_events", Message: err.Error()}}
	}
	parseErrors := 0
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM parse_errors`).Scan(&parseErrors)
	out := []CheckResult{
		{Status: StatusInfo, Name: "raw events", Detail: fmt.Sprintf("%d rows", raw), Message: latestImportedMessage(latest)},
		{Status: parseErrorStatus(parseErrors, raw), Name: "parse errors", Detail: fmt.Sprintf("%d rows", parseErrors), Message: parseErrorMessage(parseErrors, raw)},
	}
	return out
}

func latestImportedMessage(latest string) string {
	if latest == "" {
		return "no ingest yet"
	}
	return "last imported_at " + latest
}

func parseErrorStatus(failed, raw int) Status {
	total := failed + raw
	if total == 0 || failed == 0 {
		return StatusInfo
	}
	if float64(failed)/float64(total) > 0.01 {
		return StatusWarn
	}
	return StatusInfo
}

func parseErrorMessage(failed, raw int) string {
	total := failed + raw
	if total == 0 {
		return "no lines ingested yet"
	}
	if failed == 0 {
		return "no parse errors"
	}
	// parse_errors is not a subset of raw_events, so rate against all
	// processed lines (failed + parsed) rather than raw_events alone.
	return fmt.Sprintf("%.2f%% of ingested lines failed to parse", float64(failed)/float64(total)*100)
}

func checkRetention() CheckResult {
	policy, err := retention.PolicyFor(retention.ProfileBalanced)
	if err != nil {
		return CheckResult{Status: StatusWarn, Name: "retention", Detail: string(retention.ProfileBalanced), Message: err.Error()}
	}
	return CheckResult{Status: StatusInfo, Name: "retention", Detail: string(policy.Profile),
		Message: fmt.Sprintf("raw=%s redact_after=%s",
			textutil.FormatDuration(policy.RawAge), textutil.FormatDuration(policy.RedactRawAfter))}
}

func checkHooks(installed bool) CheckResult {
	if installed {
		return CheckResult{Status: StatusOK, Name: "hooks", Detail: "installed", Message: "hook spool active"}
	}
	return CheckResult{Status: StatusInfo, Name: "hooks", Detail: "not installed", Message: "run `toktop hooks install` to enable observer-mode hooks"}
}

func checkSourceDirs(ctx context.Context, source string, dirs []string) []CheckResult {
	if len(dirs) == 0 {
		return []CheckResult{{Status: StatusWarn, Name: "source roots", Detail: source, Message: "no source roots resolved"}}
	}

	// toktop probes several conventional roots (XDG + default); watching any one of
	// them is sufficient, so a missing candidate is only a real problem when *no*
	// root resolves. Downgrade "not found" to INFO when another root is available
	// and reserve WARN for the all-missing case, mirroring checkFSNotify.
	anyAvailable := slices.ContainsFunc(dirs, fsx.DirExists)

	results := make([]CheckResult, 0, len(dirs))
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			results = append(results, CheckResult{Status: StatusError, Name: "source root", Detail: dir, Message: err.Error()})
			continue
		}
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				status, msg := StatusWarn, "not found"
				if anyAvailable {
					status, msg = StatusInfo, "not found (optional; another root is active)"
				}
				results = append(results, CheckResult{Status: status, Name: "source root", Detail: dir, Message: msg})
				continue
			}
			results = append(results, CheckResult{Status: StatusError, Name: "source root", Detail: dir, Message: err.Error()})
			continue
		}
		if !info.IsDir() {
			results = append(results, CheckResult{Status: StatusError, Name: "source root", Detail: dir, Message: "path exists but is not a directory"})
			continue
		}
		results = append(results, CheckResult{Status: StatusOK, Name: "source root", Detail: dir, Message: "available"})
	}
	return results
}

func checkFSNotify(ctx context.Context, dirs []string) CheckResult {
	if err := ctx.Err(); err != nil {
		return CheckResult{Status: StatusError, Name: "fsnotify", Detail: "cancelled", Message: err.Error()}
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return CheckResult{Status: StatusError, Name: "fsnotify", Detail: "new watcher", Message: err.Error()}
	}
	defer watcher.Close()

	// The production watcher (runtime.addRecursive) watches every root *and*
	// every subdirectory beneath it, so probing only the first existing root's
	// top level can pass while the real recursive Add fails (e.g. low inotify
	// limits / ENOSPC, or unreadable subdirs). Probe all roots and at least one
	// nested subdirectory each to approximate the recursive watcher.
	existing := 0
	watched := 0
	for _, dir := range dirs {
		if !fsx.DirExists(dir) {
			continue
		}
		existing++
		if err := watcher.Add(dir); err != nil {
			return CheckResult{Status: StatusWarn, Name: "fsnotify", Detail: dir, Message: err.Error()}
		}
		watched++
		for _, sub := range nestedDirs(dir) {
			if err := watcher.Add(sub); err != nil {
				return CheckResult{Status: StatusWarn, Name: "fsnotify", Detail: sub, Message: "nested watch failed: " + err.Error()}
			}
		}
	}
	if existing == 0 {
		return CheckResult{Status: StatusWarn, Name: "fsnotify", Detail: fmt.Sprintf("%d roots", len(dirs)), Message: "no existing source root to watch"}
	}
	return CheckResult{Status: StatusOK, Name: "fsnotify", Detail: fmt.Sprintf("%d roots", watched), Message: "watchable (incl. nested subdirs)"}
}

// nestedDirs returns up to a couple of immediate subdirectories of dir so the
// fsnotify probe exercises recursive Add the way the production watcher does.
func nestedDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	subs := make([]string, 0, 2)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subs = append(subs, filepath.Join(dir, entry.Name()))
		if len(subs) == 2 {
			break
		}
	}
	return subs
}

func SourceWatchDirs(source string, roots []string) []string {
	// The watch subdir is the provider's own knowledge (claude-code: "projects",
	// codex: "sessions"); read it from the registry so adding a provider needs no
	// edit here. This is load-bearing: the live fsnotify watcher calls through
	// here, so a wrong subdir silently drops all live events for that provider.
	// An unregistered provider falls back to watching the bare root.
	sub := ""
	if p, ok := ingest.ProviderFor(source); ok {
		sub = p.WatchSubdir()
	}
	dirs := make([]string, 0, len(roots))
	for _, root := range roots {
		if sub == "" {
			dirs = append(dirs, root)
			continue
		}
		dirs = append(dirs, filepath.Join(root, sub))
	}
	return dirs
}

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/1024/1024)
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/1024/1024/1024)
	}
}

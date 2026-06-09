package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/mattn/go-sqlite3"
	"github.com/pbnjay/memory"
	"github.com/pressly/goose/v3"
)

const dbFileName = "toktop.db"

var writerCacheKiB, readerCacheKiB, sqliteMmapBytes = memoryBudget(memory.TotalMemory())

func memoryBudget(ramBytes uint64) (writerKiB, readerKiB int, mmapBytes int64) {
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case ramBytes == 0:
		return 64 * 1024, 16 * 1024, 256 * mib
	case ramBytes < 1*gib:
		return 16 * 1024, 8 * 1024, 64 * mib
	case ramBytes < 4*gib:
		return 64 * 1024, 16 * 1024, 256 * mib
	case ramBytes < 16*gib:
		return 128 * 1024, 32 * 1024, 1024 * mib
	default:
		return 256 * 1024, 32 * 1024, 2 * gib
	}
}

const readerDriverName = "sqlite3_toktop_reader"

var readerConnPragmas = []string{
	"PRAGMA query_only = ON",
	"PRAGMA busy_timeout = 5000",
	fmt.Sprintf("PRAGMA cache_size = -%d", readerCacheKiB),
	"PRAGMA temp_store = MEMORY",
	fmt.Sprintf("PRAGMA mmap_size = %d", sqliteMmapBytes),
}

func init() {
	sql.Register(readerDriverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			for _, pragma := range readerConnPragmas {
				if _, err := conn.Exec(pragma, nil); err != nil {
					return fmt.Errorf("apply reader pragma %q: %w", pragma, err)
				}
			}
			return nil
		},
	})
}

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	dataDir string
	writeDB *sql.DB
	readDB  *sql.DB
}

func (s *Store) reader() *sql.DB { return s.readDB }

func (s *Store) writer() *sql.DB { return s.writeDB }

func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbFile := filepath.Join(dataDir, dbFileName)

	writeDSN := "file:" + dbFile + "?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
	writeDB, err := sql.Open("sqlite3", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite (write): %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	if caps := ProbeCapabilities(ctx); caps.FTS5Err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("sqlite FTS5 is required; build with -tags sqlite_fts5: %w", caps.FTS5Err)
	}
	store := &Store{dataDir: dataDir, writeDB: writeDB}
	// Hold an inter-process init lock across the WAL pragma and migrations: both
	// write the freshly-created db file, and two processes opening the same new
	// home at once would otherwise race goose DDL. The lock makes them serialize;
	// the second sees the schema already current and proceeds cleanly.
	releaseInit, err := acquireInitLock(dataDir)
	if err != nil {
		_ = writeDB.Close()
		return nil, err
	}
	if err := applyPragmas(ctx, writeDB); err != nil {
		releaseInit()
		_ = writeDB.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		releaseInit()
		_ = writeDB.Close()
		return nil, err
	}
	releaseInit()

	// The CGO sqlite3 driver creates the db (and -wal/-shm sidecars) at
	// 0666 & ~umask — typically 0644, world-readable. Transcript text, token
	// counts, project names and tool arguments all live here, so tighten to
	// 0600 like every other sensitive file in the tree (bbolt event log,
	// config, api-token, hook spool). Best-effort: migrate has already written,
	// so -wal/-shm exist; chmod of an absent sidecar is harmless.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(dbFile+suffix, 0o600)
	}

	readDSN := "file:" + dbFile + "?_foreign_keys=on&_busy_timeout=5000"
	readDB, err := sql.Open(readerDriverName, readDSN)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open sqlite (read): %w", err)
	}
	readers := max(runtime.GOMAXPROCS(0), 2)
	readDB.SetMaxOpenConns(readers)
	if err := readDB.PingContext(ctx); err != nil {
		_ = readDB.Close()
		_ = writeDB.Close()
		return nil, fmt.Errorf("open sqlite (read): %w", err)
	}
	store.readDB = readDB
	return store, nil
}

func (s *Store) DB() *sql.DB {
	return s.readDB
}

func (s *Store) DataDir() string {
	return s.dataDir
}

func (s *Store) Close() error {
	var errs []error
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func DBPath(dataDir string) string {
	return filepath.Join(dataDir, dbFileName)
}

type Capabilities struct {
	FTS5Err    error
	TrigramErr error
}

// capsOnce caches the capability probe. FTS5/trigram availability is fixed at
// compile time (build tags), so the in-memory probe only needs to run once for
// the whole process instead of on every Store.Open and every `toktop doctor` check.
var capsOnce = sync.OnceValue(func() Capabilities {
	return probeCapabilities(context.Background())
})

// ProbeCapabilities returns the process-constant SQLite capabilities, probing
// once and caching the result. The context is unused (the probe is a trivial,
// cached, in-memory operation) but kept in the signature for callers.
func ProbeCapabilities(context.Context) Capabilities {
	return capsOnce()
}

func probeCapabilities(ctx context.Context) Capabilities {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return Capabilities{FTS5Err: err, TrigramErr: err}
	}
	defer db.Close()
	caps := Capabilities{}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE toktop_fts_check USING fts5(value)`); err != nil {
		caps.FTS5Err = err
	}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE toktop_tri_check USING fts5(value, tokenize='trigram')`); err != nil {
		caps.TrigramErr = err
	}
	return caps
}

func FTS5Available(ctx context.Context) error {
	return ProbeCapabilities(ctx).FTS5Err
}

func TrigramAvailable(ctx context.Context) error {
	return ProbeCapabilities(ctx).TrigramErr
}

func applyPragmas(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		fmt.Sprintf(`PRAGMA cache_size = -%d`, writerCacheKiB),
		`PRAGMA temp_store = MEMORY`,
		fmt.Sprintf(`PRAGMA mmap_size = %d`, sqliteMmapBytes),
	}
	for _, statement := range pragmas {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply pragma %q: %w", statement, err)
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if err := s.ensureGooseOwned(ctx); err != nil {
		return err
	}
	goose.SetBaseFS(migrationFiles)
	goose.SetLogger(gooseSlogLogger{})
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, s.writeDB, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func (s *Store) ensureGooseOwned(ctx context.Context) error {
	hasGoose, err := tableExists(ctx, s.writeDB, "goose_db_version")
	if err != nil {
		return err
	}
	if hasGoose {
		return nil
	}
	hasLegacy, err := anyLegacyTable(ctx, s.writeDB)
	if err != nil {
		return err
	}
	if !hasLegacy {
		return nil
	}
	slog.Warn("dropping legacy toktop schema; goose will rebuild from scratch", "data_dir", s.dataDir)
	return dropAllTables(ctx, s.writeDB)
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	row := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return count > 0, nil
}

func anyLegacyTable(ctx context.Context, db *sql.DB) (bool, error) {
	candidates := []string{"metadata", "source_roots", "raw_events", "sessions", "turns", "tool_calls"}
	for _, name := range candidates {
		ok, err := tableExists(ctx, db, name)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func dropAllTables(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
	`)
	if err != nil {
		return fmt.Errorf("list legacy tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy table: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate legacy tables: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy tables: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for legacy drop: %w", err)
	}
	for _, name := range names {
		if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteIdent(name)); err != nil {
			return fmt.Errorf("drop legacy table %s: %w", name, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable foreign keys: %w", err)
	}
	return nil
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

type gooseSlogLogger struct{}

func (gooseSlogLogger) Fatalf(format string, v ...any) {
	slog.Error(fmt.Sprintf(format, v...))
}

// Printf carries goose's routine chatter ("no migrations to run", "successfully
// migrated"). It is plumbing, not user signal — every analytics command opens the
// store, so logging it at Info spammed stderr on each invocation. Demote to Debug
// (hidden at the default Info level); doctor/summary already surface DB state.
func (gooseSlogLogger) Printf(format string, v ...any) {
	slog.Debug(fmt.Sprintf(format, v...))
}

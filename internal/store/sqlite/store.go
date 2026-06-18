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
	"strconv"
	"strings"
	"sync"

	"github.com/mattn/go-sqlite3"
	"github.com/pbnjay/memory"
	"github.com/pressly/goose/v3"
)

const dbFileName = "toktop.db"

// schemaUserVersion is the projection epoch stamped into PRAGMA user_version.
// Ordinary schema changes belong in additive goose migrations. Bump this only
// for an in-place baseline edit or a parser/projection semantics change that
// requires old normalized rows to be rebuilt from transcripts.
//
// On Open, a database whose stamp differs — older or newer build alike — is
// wiped and rebuilt from scratch before goose migrations run. The DB is a pure
// projection of the transcripts (the source of truth) and ingest is idempotent,
// so a clean rebuild loses no data — it just re-projects on the next
// ingest/reconcile. This is a correctness rebuild trigger, not a substitute for
// normal schema migration.
//
// Must stay nonzero: 0 is both the implicit value of a fresh database file and
// the in-progress-wipe marker wipeSchema sets, so 0 always means "no schema
// built at this epoch".
// Epoch 8: claudecode parser now re-resolves out-of-order tool_results (a
// tool_result whose tool_use appears later in the turn), so old rows where those
// calls had no output / a stale pending status and a spurious parse error must be
// rebuilt.
// Epoch 9: claudecode parser now reconciles background-task launches (Workflow /
// background Bash) with their out-of-band <task-notification> results — the real
// output and status supersede the "launched in background" ack, and a launch with
// no completion becomes active (in-flight), or interrupted when a successful
// TaskStop killed its task. The same notification, when injected as a system user
// event, no longer stores its raw XML as the turn's user message. Old rows holding
// the ack as a successful result (or the notification XML as a user message) must
// be rebuilt.
// Epoch 10: a <task-notification> with status `killed` now projects to interrupted
// (was active), and a non-terminal `running` progress notification no longer
// fabricates an end time. Old rows that classified a killed async run as active
// must be rebuilt.
// Epoch 11: claudecode now ingests subagent transcripts (Task/Agent runs and a
// Workflow's internal agents) as marked+linked sessions (is_subagent + parent
// linkage), where before the whole subagents/ subtree was skipped. A clean rebuild
// from the transcripts backfills the new rows and populates the denormalized
// is_subagent on every turn/raw_event consistently, rather than relying on the
// additive migration's default-0 for pre-existing rows.
// Epoch 12: codex subagents are now ingested too (a spawned thread is a flat
// rollout marked in-file by thread_source/parent_thread_id), and the parent link
// is unified across providers onto parent_external_id (00005 gained that column,
// resolved to parent_session_id by a post-pass) instead of claudecode's path hash.
// The edited 00005 + the new projection require a rebuild of any epoch-11 db.
// Epoch 13: codex subagent marking is now gated on a non-empty parent_thread_id, so
// orphan subagent-sourced rollouts (e.g. judge/guardian threads) stay top-level
// instead of being hidden; old rows that mis-marked them must be rebuilt. 00005 also
// gained the partial unresolved-subagent index.
// Epoch 14: codex subagent marking now keys on the structural source.subagent.
// thread_spawn marker (not parent_thread_id, which guardian "other" threads also
// carry, so they were wrongly hidden); a failed spawn_agent call (no agent_id) now
// projects to failed instead of success; and is_subagent is denormalized onto
// parse_errors + the search index (00005). Old rows must be rebuilt.
// Epoch 15: migrations 00002-00005 were squashed into the 00001 baseline so a
// fresh install builds the final schema in one pass. The resulting schema is
// byte-for-byte the same as the old chain produced (verified by table/index/
// trigger/FK fingerprint diff), but folding deletes the goose history those
// versions recorded, so an existing epoch-14 db is wiped and rebuilt from the
// transcripts to converge its goose_db_version onto the new single baseline.
// Epoch 16: sessions.title added to the baseline (claude-code custom/ai-title from
// the transcript; codex thread_name from the out-of-band session_index.jsonl). Old
// rows lack the column's data, so an existing db is wiped and rebuilt.
// Epoch 17: codex projection semantics changed — a failed apply_patch (custom_tool_call)
// now projects to failed instead of success (its failure text carries no shell exit
// code), so a turn containing one re-derives accordingly. Old rows carry the stale
// success status, so an existing db is wiped and rebuilt from the transcripts.
//
// Epoch 18: ingest_offsets gained fingerprint_token (provider-defined change marker
// for non-file sources). The new NOT NULL column means an old ingest_offsets row
// lacks it, so an existing db is wiped and rebuilt from the transcripts.
const schemaUserVersion = 18

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
	dataDir   string
	writeDB   *sql.DB
	readDB    *sql.DB
	wipeGuard WipeGuard
}

// WipeGuard is consulted immediately before the destructive schema-epoch wipe
// (and only then — steady-state opens never invoke it). Returning an error
// aborts Open. Callers use it to refuse wiping a database that another
// long-lived process still has open: an older-binary daemon would race the
// DDL and then silently repopulate the rebuilt schema with old-parser rows.
// nil means no guard.
type WipeGuard func() error

func (s *Store) reader() *sql.DB { return s.readDB }

func (s *Store) writer() *sql.DB { return s.writeDB }

func Open(ctx context.Context, dataDir string, guard WipeGuard) (*Store, error) {
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
	store := &Store{dataDir: dataDir, writeDB: writeDB, wipeGuard: guard}
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
	current, err := s.ensureSchemaEpoch(ctx)
	if err != nil {
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
	if current {
		// Steady state: already stamped. Skip the re-stamp — PRAGMA user_version
		// writes the DB header (a write transaction) even when the value is
		// unchanged, and an unconditional write on every Open would block behind
		// a live writer's transaction and break read-only commands.
		return nil
	}
	// First successful build at this epoch (fresh database or post-wipe
	// rebuild): stamp it. PRAGMA takes no bind parameters.
	if _, err := s.writeDB.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(schemaUserVersion)); err != nil {
		return fmt.Errorf("stamp schema epoch: %w", err)
	}
	return nil
}

// ensureSchemaEpoch compares the database's stamped schema epoch (PRAGMA
// user_version) against this build's schemaUserVersion and wipes the schema on
// any mismatch — an older stamp (upgrade) and a newer one (downgrade) alike —
// so an in-place edit to migrations/00001_init.sql takes effect without manual
// intervention. A database with no schema objects at all (fresh file, or a
// wipe that committed before the rebuild ran) is left for goose to build. This
// subsumes the older pre-goose legacy drop: any existing schema not at the
// current epoch — including a legacy DB at the implicit user_version 0 — is
// rebuilt from scratch. Safe because the DB is a pure projection of the
// transcripts.
//
// Returns current=true when the stamp already matches, meaning nothing was
// wiped and migrate must not re-stamp.
func (s *Store) ensureSchemaEpoch(ctx context.Context) (current bool, err error) {
	var userVersion int
	if err := s.writeDB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return false, fmt.Errorf("read schema epoch: %w", err)
	}
	if userVersion == schemaUserVersion {
		return true, nil
	}
	empty, err := schemaEmpty(ctx, s.writeDB)
	if err != nil {
		return false, err
	}
	if empty {
		return false, nil
	}
	if s.wipeGuard != nil {
		if err := s.wipeGuard(); err != nil {
			return false, fmt.Errorf("schema epoch mismatch (found %d, want %d) requires a rebuild: %w", userVersion, schemaUserVersion, err)
		}
	}
	slog.Warn("schema epoch mismatch; wiping and rebuilding from transcripts",
		"data_dir", s.dataDir, "found", userVersion, "want", schemaUserVersion)
	return false, wipeSchema(ctx, s.writeDB)
}

// schemaEmpty reports whether the database holds no schema objects at all,
// using the same non-sqlite_% predicate wipeSchema drops by — so any state a
// wipe could produce or leave behind is classified consistently.
func schemaEmpty(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	row := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("count schema objects: %w", err)
	}
	return count == 0, nil
}

// wipeSchema drops every schema object and clears the epoch stamp in one
// transaction, so the wipe is all-or-nothing: a crash mid-wipe rolls back to
// the intact pre-wipe state (still mismatched, retried on the next Open), and
// a committed wipe is unambiguously stamped 0 until the rebuild completes.
// Indexes and triggers drop with their tables.
//
// Foreign keys must be off for the drops: a dropped table's implicit DELETE
// fires FK actions on its referencing tables, whose preparation resolves
// every FK they declare — including ones to already-dropped parents ("no such
// table"). Deferring doesn't help (actions are not deferred), and the pragma
// is a no-op inside a transaction, so toggle it around one on the writer
// connection (a single-connection pool; the toggle is connection state, so a
// crash leaves nothing behind).
func wipeSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for wipe: %w", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema wipe: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		return fmt.Errorf("clear schema epoch: %w", err)
	}
	// sqlite_master order is creation order, so FTS5 virtual tables precede
	// their shadow tables: dropping the virtual table first takes its shadows
	// with it and the later IF EXISTS drops no-op.
	rows, err := tx.QueryContext(ctx, `
		SELECT name, type
		FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
	`)
	if err != nil {
		return fmt.Errorf("list schema objects: %w", err)
	}
	type object struct{ name, kind string }
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.name, &o.kind); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan schema object: %w", err)
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate schema objects: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close schema objects: %w", err)
	}
	for _, o := range objects {
		drop := `DROP TABLE IF EXISTS `
		if o.kind == "view" {
			drop = `DROP VIEW IF EXISTS `
		}
		if _, err := tx.ExecContext(ctx, drop+quoteIdent(o.name)); err != nil {
			return fmt.Errorf("drop %s %s: %w", o.kind, o.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema wipe: %w", err)
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

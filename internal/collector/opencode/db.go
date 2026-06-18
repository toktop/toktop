package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3" // cgo sqlite driver, shared with the store layer

	opencodeparser "toktop.unceas.dev/internal/parser/opencode"
)

// dbFileName is opencode's single SQLite database, sibling to opencode.db-wal /
// opencode.db-shm under the data dir root.
const dbFileName = "opencode.db"

// openReadOnly opens opencode's database read-only with a busy timeout. It is a
// FOREIGN database opencode itself may be writing, so:
//   - mode=ro never writes or creates;
//   - NO immutable=1 — that flag tells SQLite to ignore the -wal, which would
//     return stale reads for a live opencode;
//   - _busy_timeout lets a momentary writer lock retry instead of failing the
//     whole collect with SQLITE_BUSY.
//
// Callers open a short-lived handle per discover/collect and Close it; toktop
// never holds opencode.db open.
func openReadOnly(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open opencode db: %w", err)
	}
	return db, nil
}

// readSessionRow loads one session's full row for the synthetic session event,
// as the wire envelope the parser consumes (one definition of the shape).
func readSessionRow(ctx context.Context, db *sql.DB, sessionID string) (opencodeparser.SessionEnvelope, error) {
	var r opencodeparser.SessionEnvelope
	err := db.QueryRowContext(ctx, `
		SELECT id, COALESCE(parent_id,''), COALESCE(agent,''), title, directory, time_created
		FROM session WHERE id = ?`, sessionID).Scan(
		&r.ID, &r.ParentID, &r.Agent, &r.Title, &r.Directory, &r.TimeCreated)
	if err != nil {
		return opencodeparser.SessionEnvelope{}, fmt.Errorf("read opencode session %s: %w", sessionID, err)
	}
	return r, nil
}

// sessionExists reports whether a session id is still present in the DB — the
// liveness check behind provider.SourceFileExists (os.Stat can't validate a
// synthetic opencode:// key).
func sessionExists(ctx context.Context, db *sql.DB, sessionID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM session WHERE id = ? LIMIT 1`, sessionID).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("check opencode session %s: %w", sessionID, err)
	}
}

// taskCallByChild maps each spawned subagent's session id to the callID of the
// `task` tool part (in its parent session) that spawned it — the cross-session link
// the parser cannot resolve alone. Built in ONE scan of the part table (resolved
// once during discovery rather than a full scan per subagent at collect time). A
// top-level session is simply absent from the map.
func taskCallByChild(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT json_extract(data,'$.state.metadata.sessionId'), json_extract(data,'$.callID')
		FROM part WHERE json_extract(data,'$.tool') = 'task'`)
	if err != nil {
		return nil, fmt.Errorf("read opencode task parts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var child, callID sql.NullString
		if err := rows.Scan(&child, &callID); err != nil {
			return nil, fmt.Errorf("scan opencode task part: %w", err)
		}
		if child.Valid && child.String != "" && callID.Valid {
			out[child.String] = callID.String
		}
	}
	return out, rows.Err()
}

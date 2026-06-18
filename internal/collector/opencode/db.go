package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
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

// parentToolUseID returns the callID of the parent session's `task` tool part that
// spawned childID (its state.metadata.sessionId == childID), or "" when none. This
// is the cross-session link the parser cannot resolve alone (it sees one session at
// a time); the collector has the whole DB.
func parentToolUseID(ctx context.Context, db *sql.DB, childID string) string {
	rows, err := db.QueryContext(ctx, `
		SELECT data FROM part
		WHERE json_extract(data,'$.tool') = 'task'
		  AND json_extract(data,'$.state.metadata.sessionId') = ?
		LIMIT 1`, childID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	if !rows.Next() {
		return ""
	}
	var data []byte
	if err := rows.Scan(&data); err != nil {
		return ""
	}
	var p struct {
		CallID string `json:"callID"`
	}
	_ = json.Unmarshal(data, &p)
	return p.CallID
}

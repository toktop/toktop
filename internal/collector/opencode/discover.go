package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/source"
)

// SourceRoot aliases the shared ingest.SourceRoot so the collector, ingest, and
// store layers speak one type — matching the codex/claudecode collectors.
type SourceRoot = ingest.SourceRoot

// sourceFileScheme prefixes the synthetic source_file key for an opencode
// session. opencode has no per-session transcript file, so the session id is the
// stable identity used for trace.SessionID, ingest_offsets, and DeleteSourceFiles.
const sourceFileScheme = "opencode://"

// SessionFile is one opencode session, addressed inside its single DB rather than
// as a file on disk. It carries only what discovery/ingest needs: identity, the
// per-session change fingerprint (EventSeq = event_sequence.seq), the message+part
// count for batch sizing (MsgParts), and the spawning task tool_use id for a
// subagent (ParentToolUseID, resolved once in discovery). All other session
// metadata is read by the parser from the leading KindSession envelope, not here.
type SessionFile struct {
	Root            SourceRoot
	DBPath          string
	SessionID       string
	EventSeq        int64
	MsgParts        int
	ParentToolUseID string
}

// PathOf is the synthetic source_file key for a session ("opencode://<id>").
func PathOf(f SessionFile) string { return sourceFileScheme + f.SessionID }

// RootOf is the discovery root path (the opencode data dir).
func RootOf(f SessionFile) string { return f.Root.Path }

// FingerprintOf change-detects on opencode's native per-session revision rather
// than a file stat: the seq rides in Fingerprint.Token (Size/Mtime/Ino stay 0).
func FingerprintOf(f SessionFile) (source.Fingerprint, bool) {
	return source.Fingerprint{Token: strconv.FormatInt(f.EventSeq, 10)}, true
}

// ByteSizeOf is the batch-sizing weight. The fingerprint Size is 0 for opencode
// (the change marker is in Token), so a per-session byte estimate keeps batches
// memory-bounded; ~4 KiB per message/part row is a generous upper bound.
func ByteSizeOf(f SessionFile) int64 { return int64(f.MsgParts) * 4096 }

// DiscoverRoots resolves the effective roots given only caller-supplied explicit
// roots (no config-file layer).
func DiscoverRoots(explicit []string) []SourceRoot {
	return resolveRoots(explicit, nil)
}

func resolveRoots(explicit, file []string) []SourceRoot {
	if r := ingest.UniqueSourceRoots(explicit, "manual"); len(r) > 0 {
		return r
	}
	// opencode follows the XDG Base Directory spec: its data dir is
	// $XDG_DATA_HOME/opencode, defaulting to ~/.local/share/opencode.
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		if r := ingest.UniqueSourceRoots([]string{filepath.Join(xdg, "opencode")}, "env"); len(r) > 0 {
			return r
		}
	}
	if r := ingest.UniqueSourceRoots(file, "file"); len(r) > 0 {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []SourceRoot{{Path: filepath.Join(home, ".local", "share", "opencode"), Kind: "default"}}
}

// DiscoverSessions lists every opencode session across the roots' databases. Each
// root's opencode.db is opened read-only for the listing query and closed
// immediately. A 0-event session still ingests (LEFT JOIN ⇒ seq 0).
func DiscoverSessions(ctx context.Context, roots []SourceRoot) ([]SessionFile, error) {
	var sessions []SessionFile
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("discover sessions cancelled: %w", err)
		}
		dbPath := filepath.Join(root.Path, dbFileName)
		if !fsx.FileExists(dbPath) {
			continue
		}
		found, err := discoverInDB(ctx, root, dbPath)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, found...)
	}
	slices.SortFunc(sessions, func(a, b SessionFile) int {
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return sessions, nil
}

// sessionSelect is the shared SessionFile projection — identity, the seq
// fingerprint, and the message+part count for batch sizing — so discoverInDB
// (ORDER BY) and sessionFileByID (WHERE id=?) can't drift. Callers append the tail.
const sessionSelect = `
	SELECT s.id, COALESCE(es.seq,0),
	       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) +
	       (SELECT COUNT(*) FROM part p WHERE p.session_id = s.id)
	FROM session s
	LEFT JOIN event_sequence es ON es.aggregate_id = s.id`

func scanSessionFile(root SourceRoot, dbPath string, scan func(...any) error) (SessionFile, error) {
	f := SessionFile{Root: root, DBPath: dbPath}
	if err := scan(&f.SessionID, &f.EventSeq, &f.MsgParts); err != nil {
		return SessionFile{}, err
	}
	return f, nil
}

func discoverInDB(ctx context.Context, root SourceRoot, dbPath string) ([]SessionFile, error) {
	db, err := openReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, sessionSelect+` ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("list opencode sessions: %w", err)
	}
	defer rows.Close()
	var out []SessionFile
	for rows.Next() {
		f, err := scanSessionFile(root, dbPath, rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan opencode session: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Resolve each subagent's spawning task callID in ONE scan of the part table
	// (a top-level session is absent from the map ⇒ ""), instead of a full scan per
	// subagent inside CollectSessionFile.
	taskByChild, err := taskCallByChild(ctx, db)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ParentToolUseID = taskByChild[out[i].SessionID]
	}
	return out, nil
}

// SessionFileFromPath resolves a synthetic "opencode://<id>" key back to a
// SessionFile for the live single-session ingest path, re-querying the row fresh.
func SessionFileFromPath(ctx context.Context, path string, roots []SourceRoot) (SessionFile, bool) {
	id, ok := strings.CutPrefix(path, sourceFileScheme)
	if !ok || id == "" {
		return SessionFile{}, false
	}
	for _, root := range roots {
		dbPath := filepath.Join(root.Path, dbFileName)
		if !fsx.FileExists(dbPath) {
			continue
		}
		f, ok := sessionFileByID(ctx, root, dbPath, id)
		if ok {
			return f, true
		}
	}
	return SessionFile{}, false
}

func sessionFileByID(ctx context.Context, root SourceRoot, dbPath, id string) (SessionFile, bool) {
	db, err := openReadOnly(dbPath)
	if err != nil {
		return SessionFile{}, false
	}
	defer db.Close()
	f, err := scanSessionFile(root, dbPath, func(dst ...any) error {
		return db.QueryRowContext(ctx, sessionSelect+` WHERE s.id = ?`, id).Scan(dst...)
	})
	if err != nil {
		return SessionFile{}, false
	}
	if taskByChild, err := taskCallByChild(ctx, db); err == nil {
		f.ParentToolUseID = taskByChild[id]
	}
	return f, true
}

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
// as a file on disk. EventSeq is the per-session change fingerprint
// (event_sequence.seq); MsgParts is the message+part count used only for batch
// sizing.
type SessionFile struct {
	Root      SourceRoot
	DBPath    string
	SessionID string
	EventSeq  int64
	ParentID  string
	Agent     string
	Title     string
	Directory string
	ProjectID string
	MsgParts  int
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

func discoverInDB(ctx context.Context, root SourceRoot, dbPath string) ([]SessionFile, error) {
	db, err := openReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, COALESCE(s.parent_id,''), COALESCE(s.agent,''), s.title, s.directory, s.project_id,
		       COALESCE(es.seq,0),
		       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) +
		       (SELECT COUNT(*) FROM part p WHERE p.session_id = s.id)
		FROM session s
		LEFT JOIN event_sequence es ON es.aggregate_id = s.id
		ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("list opencode sessions: %w", err)
	}
	defer rows.Close()
	var out []SessionFile
	for rows.Next() {
		f := SessionFile{Root: root, DBPath: dbPath}
		if err := rows.Scan(&f.SessionID, &f.ParentID, &f.Agent, &f.Title, &f.Directory, &f.ProjectID, &f.EventSeq, &f.MsgParts); err != nil {
			return nil, fmt.Errorf("scan opencode session: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SessionFileFromPath resolves a synthetic "opencode://<id>" key back to a
// SessionFile for the live single-session ingest path, re-querying the row fresh.
func SessionFileFromPath(path string, roots []SourceRoot) (SessionFile, bool) {
	id, ok := strings.CutPrefix(path, sourceFileScheme)
	if !ok || id == "" {
		return SessionFile{}, false
	}
	for _, root := range roots {
		dbPath := filepath.Join(root.Path, dbFileName)
		if !fsx.FileExists(dbPath) {
			continue
		}
		f, ok := sessionFileByID(root, dbPath, id)
		if ok {
			return f, true
		}
	}
	return SessionFile{}, false
}

func sessionFileByID(root SourceRoot, dbPath, id string) (SessionFile, bool) {
	db, err := openReadOnly(dbPath)
	if err != nil {
		return SessionFile{}, false
	}
	defer db.Close()
	f := SessionFile{Root: root, DBPath: dbPath}
	err = db.QueryRow(`
		SELECT s.id, COALESCE(s.parent_id,''), COALESCE(s.agent,''), s.title, s.directory, s.project_id,
		       COALESCE(es.seq,0),
		       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) +
		       (SELECT COUNT(*) FROM part p WHERE p.session_id = s.id)
		FROM session s
		LEFT JOIN event_sequence es ON es.aggregate_id = s.id
		WHERE s.id = ?`, id).Scan(
		&f.SessionID, &f.ParentID, &f.Agent, &f.Title, &f.Directory, &f.ProjectID, &f.EventSeq, &f.MsgParts)
	if err != nil {
		return SessionFile{}, false
	}
	return f, true
}

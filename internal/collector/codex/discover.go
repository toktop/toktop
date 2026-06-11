package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
)

// SourceRoot aliases the shared ingest.SourceRoot so the collector, ingest, and
// store layers speak one type — no per-package definition, no conversion loops.
type SourceRoot = ingest.SourceRoot

type SessionFile struct {
	Root SourceRoot
	Path string
}

// DiscoverRoots resolves the effective roots given only caller-supplied
// explicit roots (no config-file layer). Preserved for callers that don't
// carry config.
func DiscoverRoots(explicit []string) []SourceRoot {
	return resolveRoots(explicit, nil)
}

func resolveRoots(explicit, file []string) []SourceRoot {
	if r := ingest.UniqueSourceRoots(explicit, "manual"); len(r) > 0 {
		return r
	}
	// Upstream Codex CLI treats CODEX_HOME as a single absolute directory, not
	// a comma-separated list. Honor that single-path semantics, and fall
	// through to the config-file / ~/.codex default when the value is
	// empty/whitespace/"." rather than producing zero roots with no diagnostics.
	if r := ingest.UniqueSourceRoots([]string{os.Getenv("CODEX_HOME")}, "env"); len(r) > 0 {
		return r
	}
	if r := ingest.UniqueSourceRoots(file, "file"); len(r) > 0 {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []SourceRoot{{Path: filepath.Join(home, ".codex"), Kind: "default"}}
}

func DiscoverSessions(ctx context.Context, roots []SourceRoot) ([]SessionFile, error) {
	var sessions []SessionFile
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("discover sessions cancelled: %w", err)
		}
		sessionsDir := filepath.Join(root.Path, "sessions")
		info, err := os.Stat(sessionsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", sessionsDir, err)
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(sessionsDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			sessions = append(sessions, SessionFile{Root: root, Path: path})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", sessionsDir, err)
		}
	}
	slices.SortFunc(sessions, func(a, b SessionFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	return sessions, nil
}

func SessionFileFromPath(path string, roots []SourceRoot) (SessionFile, bool) {
	if filepath.Ext(path) != ".jsonl" {
		return SessionFile{}, false
	}
	cleanPath := filepath.Clean(path)
	for _, root := range roots {
		sessionsDir := filepath.Join(filepath.Clean(root.Path), "sessions")
		if !fsx.PathWithin(sessionsDir, cleanPath) {
			continue
		}
		return SessionFile{Root: root, Path: cleanPath}, true
	}
	return SessionFile{}, false
}

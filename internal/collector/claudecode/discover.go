package claudecode

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
	Root        SourceRoot
	Path        string
	ProjectName string
	ProjectPath string
}

// DiscoverRoots resolves the effective roots given only explicit (--root) roots
// (no config-file layer). Preserved for callers that don't carry config.
func DiscoverRoots(explicit []string) []SourceRoot {
	return resolveRoots(explicit, nil)
}

func resolveRoots(flag, file []string) []SourceRoot {
	if r := ingest.UniqueSourceRoots(flag, "manual"); len(r) > 0 {
		return r
	}

	// Only take the CLAUDE_CONFIG_DIR branch when it resolves to at least one
	// valid root. A blank/comma-only value ("", ",", "  ") must fall back to the
	// config-file / home defaults rather than silently disabling all discovery.
	if env := configDirEnvRoots(); len(env) > 0 {
		return ingest.UniqueSourceRoots(env, "env")
	}

	if r := ingest.UniqueSourceRoots(file, "file"); len(r) > 0 {
		return r
	}

	return homeDefaultRoots()
}

// configDirEnvRoots splits and trims CLAUDE_CONFIG_DIR into candidate root
// paths. It returns nil when the variable is unset or contains no usable entry.
func configDirEnvRoots() []string {
	env := os.Getenv("CLAUDE_CONFIG_DIR")
	if env == "" {
		return nil
	}
	var parts []string
	for part := range strings.SplitSeq(env, ",") {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func homeDefaultRoots() []SourceRoot {
	roots := make([]SourceRoot, 0, 2)
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			SourceRoot{Path: filepath.Join(home, ".config", "claude"), Kind: "default"},
			SourceRoot{Path: filepath.Join(home, ".claude"), Kind: "default"},
		)
	}
	return roots
}

func DiscoverSessions(ctx context.Context, roots []SourceRoot) ([]SessionFile, error) {
	var sessions []SessionFile
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("discover sessions cancelled: %w", err)
		}

		projectsDir := filepath.Join(root.Path, "projects")
		info, err := os.Stat(projectsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", projectsDir, err)
		}
		if !info.IsDir() {
			continue
		}

		err = filepath.WalkDir(projectsDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}

			projectName := projectNameFor(projectsDir, path)
			sessions = append(sessions, SessionFile{
				Root:        root,
				Path:        path,
				ProjectName: projectName,
				ProjectPath: decodeProjectName(projectName),
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", projectsDir, err)
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
		projectsDir := filepath.Join(filepath.Clean(root.Path), "projects")
		if !fsx.PathWithin(projectsDir, cleanPath) {
			continue
		}
		projectName := projectNameFor(projectsDir, cleanPath)
		return SessionFile{
			Root:        root,
			Path:        cleanPath,
			ProjectName: projectName,
			ProjectPath: decodeProjectName(projectName),
		}, true
	}
	return SessionFile{}, false
}

func projectNameFor(projectsDir, transcriptPath string) string {
	rel, err := filepath.Rel(projectsDir, transcriptPath)
	if err != nil {
		return "unknown"
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 {
		return "unknown"
	}
	return parts[0]
}

// decodeProjectName best-effort reverses Claude Code's project directory
// encoding (absolute path with "/" replaced by "-"). The decode is lossy: any
// real path segment containing a literal hyphen decodes incorrectly, so the
// authoritative project path is taken from the transcript cwd field during
// collection (see CollectSessionFile). This decode is only a heuristic fallback
// when no cwd is recorded. We refuse to synthesize traversal paths (e.g.
// "-..-..-etc-passwd" → "/etc/passwd") so a malformed directory name can never
// redirect a per-project filesystem scan outside the encoded project root.
func decodeProjectName(name string) string {
	if name == "" || name == "unknown" {
		return ""
	}
	if !strings.HasPrefix(name, "-") {
		return name
	}
	// Check for ".." BEFORE Clean: every name reaching here is hyphen-prefixed,
	// so the replaced string is rooted ("/...") and filepath.Clean would strip a
	// leading "/.." per its rooted-path rule, making a post-Clean check dead. Test
	// the raw replaced segments so a crafted "-..-..-etc" can't decode to "/etc".
	replaced := strings.ReplaceAll(name, "-", string(filepath.Separator))
	if slices.Contains(strings.Split(replaced, string(filepath.Separator)), "..") {
		return ""
	}
	return filepath.Clean(replaced)
}

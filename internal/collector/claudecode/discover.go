package claudecode

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/textutil"
)

// SourceRoot aliases the shared ingest.SourceRoot so the collector, ingest, and
// store layers speak one type — no per-package definition, no conversion loops.
type SourceRoot = ingest.SourceRoot

type SessionFile struct {
	Root        SourceRoot
	Path        string
	ProjectName string
	ProjectPath string

	// Subagent marker, set when Path is a nested transcript under a session's
	// `subagents/` subtree (zero for a top-level session). SubagentKind is "task" or
	// "workflow"; WorkflowRunID is the wf_… run id (workflow only). ParentExternalID
	// is the parent session's external id, read straight from the path (the <uuid>
	// directory that precedes `subagents/`, which IS the parent's external id) — so
	// the link survives even when the transcript carries no in-file sessionId.
	SubagentKind     string
	WorkflowRunID    string
	ParentExternalID string
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
	return textutil.SplitTrim(env)
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
			slog.Warn("skip unreadable claude projects root", "path", projectsDir, "err", err)
			continue
		}
		if !info.IsDir() {
			continue
		}

		err = filepath.WalkDir(projectsDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				slog.Warn("skip unreadable claude project path", "path", path, "err", walkErr)
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
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
			// journal.jsonl (a workflow run's resume journal) lives in the subagents/
			// subtree and is .jsonl but is NOT a transcript (a 0-turn ghost of
			// {started,result} records) — never ingest it.
			if filepath.Base(path) == "journal.jsonl" {
				return nil
			}

			projectName := projectNameFor(projectsDir, path)
			sf := SessionFile{
				Root:        root,
				Path:        path,
				ProjectName: projectName,
				ProjectPath: decodeProjectName(projectName),
			}
			// A transcript under a `subagents/` segment is a nested subagent run; the
			// project stays the parent's (projectNameFor reads the leading segment).
			// Its own (distinct) path hashes to a unique session id; the parent link is
			// resolved downstream by external id (its in-file sessionId is the parent's).
			if kind, runID, parentID, ok := classifySubagentPath(projectsDir, path); ok {
				sf.SubagentKind = kind
				sf.WorkflowRunID = runID
				sf.ParentExternalID = parentID
			}
			sessions = append(sessions, sf)
			return nil
		})
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, fmt.Errorf("discover sessions cancelled: %w", cerr)
			}
			slog.Warn("skip claude projects root after walk error", "path", projectsDir, "err", err)
			continue
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
		// Mirror DiscoverSessions: never ingest a workflow run's journal.jsonl.
		if filepath.Base(cleanPath) == "journal.jsonl" {
			return SessionFile{}, false
		}
		projectName := projectNameFor(projectsDir, cleanPath)
		sf := SessionFile{
			Root:        root,
			Path:        cleanPath,
			ProjectName: projectName,
			ProjectPath: decodeProjectName(projectName),
		}
		if kind, runID, parentID, ok := classifySubagentPath(projectsDir, cleanPath); ok {
			sf.SubagentKind = kind
			sf.WorkflowRunID = runID
			sf.ParentExternalID = parentID
		}
		return sf, true
	}
	return SessionFile{}, false
}

// classifySubagentPath inspects a transcript path under projectsDir and, when it
// lies inside a `subagents/` segment, returns its kind ("task" | "workflow"), the
// wf_… run id (workflow only), and the parent session's external id — the <uuid>
// directory segment just before `subagents/`, which IS the parent's external id.
// ok is false for a top-level transcript. Layout:
//
//	<proj>/<uuid>/subagents/agent-<id>.jsonl                       → task
//	<proj>/<uuid>/subagents/workflows/wf_<id>/agent-<id>.jsonl     → workflow
func classifySubagentPath(projectsDir, transcriptPath string) (kind, workflowRunID, parentExternalID string, ok bool) {
	rel, err := filepath.Rel(projectsDir, transcriptPath)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	idx := slices.Index(parts, "subagents")
	// idx < 1 means either no `subagents/` segment (slices.Index → -1) or it is the
	// leading segment with no parent <uuid> dir before it (idx 0); neither is a
	// linkable nested subagent, so treat the path as top-level.
	if idx < 1 {
		return "", "", "", false
	}
	parentExternalID = parts[idx-1]
	if idx+2 < len(parts) && parts[idx+1] == "workflows" {
		return "workflow", parts[idx+2], parentExternalID, true
	}
	return "task", "", parentExternalID, true
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

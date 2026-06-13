package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/trace"
)

type scanOptions struct {
	UserRoots []string

	ProjectPaths []string
	ClaudeUser   *claudeUserConfig
}

// scanInstalledSkills returns the installed skills and whether the scan was
// complete. complete is false when any existing skill root could not be read, so
// the caller skips a metadata reconcile rather than deleting rows a transient
// failure merely hid.
func scanInstalledSkills(ctx context.Context, opts scanOptions) ([]trace.Skill, bool, error) {
	if len(opts.UserRoots) == 0 {
		opts.UserRoots = defaultClaudeUserRoots()
	}
	seen := make(map[string]struct{})
	out := make([]trace.Skill, 0, 32)
	complete := true

	for _, root := range opts.UserRoots {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}

		skills, err := scanSkillsRoot(filepath.Join(root, "skills"), "user")
		if err != nil {
			slog.Warn("skip unreadable claude skill root", "path", filepath.Join(root, "skills"), "err", err)
			complete = false
		} else {
			collector.AppendUniqueSkills(&out, seen, skills...)
		}

		pluginDirs, err := pluginCacheSkillsDirs(root)
		if err != nil {
			slog.Warn("skip claude plugin skill roots", "root", root, "err", err)
			complete = false
			continue
		}
		for _, dir := range pluginDirs {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			skills, err := scanSkillsRoot(dir, "user")
			if err != nil {
				slog.Warn("skip unreadable claude plugin skill root", "path", dir, "err", err)
				complete = false
				continue
			}
			collector.AppendUniqueSkills(&out, seen, skills...)
		}
	}

	for _, project := range collector.UniqueStrings(opts.ProjectPaths) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		skills, err := scanSkillsRoot(filepath.Join(project, ".claude", "skills"), "project")
		if err != nil {
			slog.Warn("skip unreadable claude project skill root", "project", project, "err", err)
			complete = false
			continue
		}
		collector.AppendUniqueSkills(&out, seen, skills...)
	}

	return out, complete, nil
}

func pluginCacheSkillsDirs(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "plugins", "cache", "*", "*", "*", "skills"))
	if err != nil {
		return nil, fmt.Errorf("glob plugin cache: %w", err)
	}
	return matches, nil
}

func scanSkillsRoot(skillsDir, scope string) ([]trace.Skill, error) {
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", skillsDir, err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", skillsDir, err)
	}
	var out []trace.Skill
	for _, entry := range entries {
		name := entry.Name()
		if !validSkillName(name) {
			continue
		}

		entryPath := filepath.Join(skillsDir, name)
		info, err := os.Stat(entryPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Warn("skip unreadable skill entry", "path", entryPath, "err", err)
			continue
		}
		if !info.IsDir() {
			continue
		}
		path := filepath.Join(entryPath, "SKILL.md")
		content, err := os.ReadFile(path)
		if err != nil {

			if os.IsNotExist(err) {
				continue
			}
			slog.Warn("skip unreadable skill file", "path", path, "err", err)
			continue
		}
		meta := parseSkillFrontMatter(content)
		out = append(out, trace.Skill{
			ID:            collector.SkillID(scope, path),
			Name:          name,
			Scope:         scope,
			SourcePath:    path,
			SourceHash:    collector.HashContent(content),
			Description:   meta.Description,
			Version:       meta.Version,
			ArgumentHint:  meta.ArgumentHint,
			UserInvocable: meta.UserInvocable,
			Triggers:      marshalJSONOrNil(meta.Triggers),
			AllowedTools:  marshalJSONOrNil(meta.AllowedTools),
			Tools:         marshalJSONOrNil(meta.Tools),
			Compatibility: meta.Compatibility,
			License:       meta.License,
		})
	}
	return out, nil
}

func validSkillName(name string) bool {
	return !strings.HasPrefix(name, ".")
}

type skillFrontMatter struct {
	Description   string `yaml:"description"`
	Version       string `yaml:"version"`
	ArgumentHint  string `yaml:"argument-hint"`
	UserInvocable *bool  `yaml:"user-invocable"`
	Triggers      any    `yaml:"triggers"`
	AllowedTools  any    `yaml:"allowed-tools"`
	Tools         any    `yaml:"tools"`
	Compatibility string `yaml:"compatibility"`
	License       string `yaml:"license"`
}

func parseSkillFrontMatter(content []byte) skillFrontMatter {
	text := string(content)
	if !strings.HasPrefix(text, "---") {
		return skillFrontMatter{}
	}
	body, _, found := strings.Cut(text[3:], "\n---")
	if !found {
		return skillFrontMatter{}
	}
	var meta skillFrontMatter
	if err := yaml.Unmarshal([]byte(body), &meta); err != nil {
		return skillFrontMatter{}
	}
	return meta
}

func marshalJSONOrNil(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" {
		return nil
	}
	return raw
}

func defaultClaudeUserRoots() []string {
	// Mirror DiscoverRoots' fallback logic via the shared helpers so the two
	// paths cannot drift: prefer a valid CLAUDE_CONFIG_DIR, otherwise the home
	// defaults. A blank/comma-only env value falls back to the home defaults.
	if env := configDirEnvRoots(); len(env) > 0 {
		return collector.UniqueStrings(env)
	}
	roots := make([]string, 0, 2)
	for _, root := range homeDefaultRoots() {
		roots = append(roots, root.Path)
	}
	return collector.UniqueStrings(roots)
}

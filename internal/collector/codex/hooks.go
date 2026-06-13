package codex

import (
	"fmt"
	"os"
	"path/filepath"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// provider implements the optional ingest.HookInstaller seam: codex owns its
// hooks-file location, event list, entry schema, and event→status map.
var _ ingest.HookInstaller = provider{}
var _ ingest.HookInstallNoter = provider{}

// HookInstallNote warns that codex, unlike claude-code, will not run a freshly
// installed third-party (unmanaged) hook until the user trusts it: codex marks a
// first-seen hook untrusted and only runs trusted ones, and re-prompts whenever
// the hook command changes (it tracks a trusted_hash per hook).
func (provider) HookInstallNote() string {
	return "codex treats this as an untrusted hook and won't run it until you trust it in codex; re-trust it after any toktop upgrade that rewrites the hook entry."
}

func (provider) HookConfigPath(scope string) (string, string, error) {
	switch scope {
	case "user", "":
		// CODEX_HOME is parsed comma-first here, the historical hooks-path
		// behavior. This deliberately differs from resolveRoots, which treats
		// CODEX_HOME as a single path; kept identical to the old codexHooksPath so
		// hooks install is byte-for-byte unchanged.
		if parts := textutil.SplitTrim(os.Getenv("CODEX_HOME")); len(parts) > 0 {
			return filepath.Join(filepath.Clean(parts[0]), "hooks.json"), "hooks", nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		return filepath.Join(home, ".codex", "hooks.json"), "hooks", nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		return filepath.Join(cwd, ".codex", "hooks.json"), "hooks", nil
	default:
		return "", "", fmt.Errorf("unknown scope %q", scope)
	}
}

func (provider) HookEvents() []string {
	return []string{
		"SessionStart",
		"UserPromptSubmit",
		"Stop",
		"PreCompact",
		"PostCompact",
		"SubagentStart",
		"SubagentStop",
		"PreToolUse",
		"PermissionRequest",
		"PostToolUse",
	}
}

func (provider) HookEntry(_, command string) map[string]any {
	return map[string]any{
		"matcher": ".*",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       command,
				"timeout":       2,
				"statusMessage": "toktop observer",
			},
		},
	}
}

func (provider) HookEventStatus(event string) (string, bool) {
	switch event {
	case "PermissionRequest":
		return trace.StatusAwaitingConfirmation, true
	case "Stop", "SubagentStop":
		return trace.StatusSuccess, true
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"SubagentStart", "PreCompact", "PostCompact":
		return trace.StatusActive, true
	}
	return "", false
}

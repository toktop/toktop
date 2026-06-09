package claudecode

import (
	"fmt"
	"os"
	"path/filepath"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/trace"
)

// provider implements the optional ingest.HookInstaller seam: claude-code owns
// its settings-file location, event list, entry schema, and event→status map.
var _ ingest.HookInstaller = provider{}

func (provider) HookConfigPath(scope string) (string, string, error) {
	switch scope {
	case "user", "":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		return filepath.Join(home, ".claude", "settings.json"), "settings", nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		return filepath.Join(cwd, ".claude", "settings.json"), "settings", nil
	default:
		return "", "", fmt.Errorf("unknown scope %q", scope)
	}
}

func (provider) HookEvents() []string {
	return []string{
		"SessionStart",
		"UserPromptSubmit",
		"Stop",
		"StopFailure",
		"PreCompact",
		"PostCompact",
		"SubagentStart",
		"SubagentStop",
		"PreToolUse",
		"PostToolUse",
		"Notification",
	}
}

func (provider) HookEntry(event, command string) map[string]any {
	return map[string]any{
		"matcher":           "*",
		"name":              "toktop-observer-" + event,
		ingest.HookSentinel: true,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
				"async":   true,
			},
		},
	}
}

func (provider) HookEventStatus(event string) (string, bool) {
	switch event {
	case "StopFailure":
		return trace.StatusFailed, true
	case "Notification":
		// Claude Code fires Notification when it needs the user (permission
		// prompt / idle waiting), so it is the "awaiting you" signal.
		return trace.StatusAwaitingConfirmation, true
	case "Stop", "SubagentStop":
		return trace.StatusSuccess, true
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"SubagentStart", "PreCompact", "PostCompact":
		return trace.StatusActive, true
	}
	return "", false
}

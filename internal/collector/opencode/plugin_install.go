package opencode

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/trace"
)

//go:embed plugin/toktop-observer.js
var pluginJS string

// pluginAssetName is the file toktop writes into opencode's auto-loaded plugins/
// dir; opencode loads any file there at startup, so no opencode.json edit is needed.
const pluginAssetName = "toktop-observer.js"

// hookIntakePath is the daemon route the plugin POSTs live status to.
const hookIntakePath = "/v1/hooks:intake"

var (
	_ ingest.PluginInstaller  = provider{}
	_ ingest.HookInstallNoter = provider{}
)

// HookInstallNote tells the user the plugin only takes effect on opencode's next
// launch (opencode loads plugins at startup).
func (provider) HookInstallNote() string {
	return "opencode loads the toktop observer plugin on its next launch; restart any running opencode session to start streaming live status."
}

// PluginConfigPath returns the plugin asset file path for the scope. user is the
// global plugins dir ($XDG_CONFIG_HOME/opencode/plugins, default ~/.config/...);
// project is <cwd>/.opencode/plugins.
func (provider) PluginConfigPath(scope string) (string, string, error) {
	dir, err := pluginsDir(scope)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, pluginAssetName), "plugin", nil
}

func pluginsDir(scope string) (string, error) {
	switch scope {
	case "user", "":
		base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "opencode", "plugins"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".opencode", "plugins"), nil
	default:
		return "", fmt.Errorf("unknown scope %q", scope)
	}
}

// InstallPlugin writes the observer plugin asset with the resolved intake endpoint
// (and, for a TCP endpoint, bearer token) baked in. endpoint is "unix:<sock>" or a
// "http://host:port/v1/hooks:intake" URL; token is empty for a unix endpoint.
func (provider) InstallPlugin(scope, endpoint, token string, dryRun bool) (string, error) {
	path, _, err := provider{}.PluginConfigPath(scope)
	if err != nil {
		return "", err
	}
	url, unix := resolvePluginEndpoint(endpoint)
	content := bakePlugin(url, unix, token)
	if dryRun {
		// Never echo the token value; report only that it was baked.
		tokenNote := "no token (unix socket)"
		if token != "" {
			tokenNote = "bearer token baked (TCP)"
		}
		return fmt.Sprintf("would write %s (endpoint %s; %s)", path, url, tokenNote), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return "installed toktop observer plugin source=opencode in " + path, nil
}

// UninstallPlugin removes the observer plugin asset.
func (provider) UninstallPlugin(scope string, dryRun bool) (string, error) {
	path, _, err := provider{}.PluginConfigPath(scope)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return "no toktop observer plugin source=opencode at " + path, nil
	}
	if dryRun {
		return "would remove " + path, nil
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return "removed toktop observer plugin source=opencode from " + path, nil
}

// PluginEventStatus maps opencode bus event types to neutral live-status values.
// The cases are exactly the JS plugin's WATCH set (plugin/toktop-observer.js) — the
// plugin is the only producer of opencode-named events, so an event it never emits
// would be a dead case here; keep the two lists identical.
func (provider) PluginEventStatus(eventName string) (string, bool) {
	switch eventName {
	case "permission.updated":
		return trace.StatusAwaitingConfirmation, true
	case "session.error":
		return trace.StatusFailed, true
	case "session.idle":
		return trace.StatusSuccess, true
	case "session.created", "session.updated", "message.updated":
		return trace.StatusActive, true
	}
	return "", false
}

// resolvePluginEndpoint splits the install endpoint into the fetch URL the plugin
// calls and the unix socket path (empty for TCP). The toktop intake query
// (?provider=opencode&toktop_observer=…) is appended here so the sentinel lands in
// the asset, letting `hooks status`/uninstall recognize toktop's own file.
func resolvePluginEndpoint(endpoint string) (url, unix string) {
	params := "provider=opencode&toktop_observer=" + ingest.HookSentinel
	if sock, ok := strings.CutPrefix(endpoint, "unix:"); ok {
		return "http://localhost" + hookIntakePath + "?" + params, sock
	}
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + params, ""
}

func bakePlugin(url, unix, token string) string {
	r := strings.NewReplacer(
		"__TOKTOP_ENDPOINT__", url,
		"__TOKTOP_UNIX__", unix,
		"__TOKTOP_TOKEN__", token,
	)
	return r.Replace(pluginJS)
}

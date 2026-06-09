package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/paths"
)

// `toktop hooks` — install/manage the observer hooks that POST live session
// status to /v1/hooks:intake. The CLI owns the shared curl command (transport +
// sentinel); each provider's HookInstaller seam owns its settings-file location,
// event list, and entry schema.

func runHook(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if printUsageForHelp(args, stdout, `usage: toktop hooks <status|install|uninstall> [flags]

Install observer hooks that push live session status into toktop; each hook
POSTs to /v1/hooks:intake. Run install once per provider and scope you watch.

flags:
  --sources   one or more of claude-code, codex (repeat or comma-separated);
              required for install/uninstall (a write op must name its target);
              status without it shows every hook-capable provider
  --scope     user (default) or project
  --endpoint  intake target (default: the daemon's unix socket; pass http://host:port/v1/hooks:intake for a TCP daemon)
  --dry-run   show the planned settings diff without writing

examples:
  toktop hooks install --sources=claude-code   # observe Claude Code, user scope
  toktop hooks install --sources=claude-code,codex  # observe both at once
  toktop hooks status                          # show what is installed (all providers)
  toktop hooks uninstall --sources=claude-code`) {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: toktop hooks <status|install|uninstall> [flags]")
		return 2
	}
	sub := args[0]
	args = args[1:]
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	var sourcesFlag rootList
	scope := "user"
	dryRun := false
	endpoint := "unix:" + paths.SocketPath(home)
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&sourcesFlag, "sources", "hook source providers: claude-code|codex; may be repeated or comma-separated")
	fs.StringVar(&scope, "scope", scope, "user|project")
	fs.BoolVar(&dryRun, "dry-run", dryRun, "show planned diff without writing")
	fs.StringVar(&endpoint, "endpoint", endpoint, "toktop hook intake endpoint")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	// Fold + validate every name up front (all must implement HookInstaller), so
	// a typo'd list errors before any settings file is written.
	sources, err := resolveHookSources(sourcesFlag)
	if err != nil {
		cliErr(stderr, err)
		return 2
	}

	switch sub {
	case "status":
		// Read-only: no --sources lists every hook-capable provider, instead of
		// the old hardcoded single claude-code default.
		if len(sources) == 0 {
			sources = hookCapableProviders()
		}
		exit := 0
		for _, name := range sources {
			if rc := runHookStatus(name, scope, stdout, stderr); rc != 0 {
				exit = rc
			}
		}
		return exit
	case "install", "uninstall":
		// Write op: require an explicit target. Never default or auto-expand —
		// silently editing every provider's settings file is a footgun.
		if len(sources) == 0 {
			cliErrf(stderr, "hooks %s requires --sources (a write op must name its target); hook-capable providers: %s", sub, strings.Join(hookCapableProviders(), ", "))
			return 2
		}
		exit := 0
		for _, name := range sources {
			var rc int
			if sub == "install" {
				rc = runHookInstall(ctx, name, scope, endpoint, home, dryRun, stdout, stderr)
			} else {
				rc = runHookUninstall(name, scope, dryRun, stdout, stderr)
			}
			if rc != 0 {
				exit = rc
			}
		}
		return exit
	}
	cliErrf(stderr, "unknown hook subcommand %q", sub)
	return 2
}

// normalizeHookSource folds a provider alias (via the shared ingest.NormalizeName)
// then gates on the HookInstaller seam, so only hook-capable providers are valid.
func normalizeHookSource(sourceName string) (string, error) {
	n := ingest.NormalizeName(sourceName)
	if _, ok := ingest.HookInstallerFor(n); !ok {
		return "", fmt.Errorf("unsupported hook source %q", sourceName)
	}
	return n, nil
}

// resolveHookSources folds + validates --sources as hook targets (each must
// implement HookInstaller), deduped, via the shared resolveTokens loop. Empty
// input returns an empty slice so the caller applies the per-subcommand policy
// (status => all; write => required).
func resolveHookSources(values rootList) ([]string, error) {
	return resolveTokens(values, normalizeHookSource)
}

// hookCapableProviders lists registered providers that implement HookInstaller,
// sorted — the default target set for read-only `toktop hooks status`.
func hookCapableProviders() []string {
	var out []string
	for _, name := range ingest.SortedProviders() {
		if _, ok := ingest.HookInstallerFor(name); ok {
			out = append(out, name)
		}
	}
	return out
}

// hookInstaller resolves a normalized source name to its hook-install seam.
func hookInstaller(sourceName string) (ingest.HookInstaller, error) {
	hi, ok := ingest.HookInstallerFor(sourceName)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support hook installation", sourceName)
	}
	return hi, nil
}

// hooksInstalled reports whether toktop observer hooks are present in the
// user-scope settings for the given source, so `toktop doctor` reflects the real
// installation state instead of always reporting "not installed".
func hooksInstalled(sourceName string) bool {
	hi, ok := ingest.HookInstallerFor(sourceName)
	if !ok {
		return false
	}
	path, _, err := hi.HookConfigPath("user")
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), ingest.HookSentinel)
}

func runHookStatus(sourceName, scope string, stdout, stderr io.Writer) int {
	hi, err := hookInstaller(sourceName)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	path, pathLabel, err := hi.HookConfigPath(scope)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		cliErr(stderr, err)
		return 1
	}
	// A missing settings file just means not installed; treating it as empty
	// folds both cases into one consistent installed=<bool> line (no special-case
	// "installed=no" vs the file-present "installed=false").
	installed := strings.Contains(string(data), ingest.HookSentinel)
	fmt.Fprintf(stdout, "source=%s scope=%s installed=%v %s=%s\n", sourceName, scope, installed, pathLabel, path)
	if !installed {
		scopeArg := ""
		if scope == "project" {
			scopeArg = " --scope project"
		}
		fmt.Fprintf(stdout, "  → run `toktop hooks install --sources=%s%s` to enable\n", sourceName, scopeArg)
	}
	return 0
}

func runHookInstall(_ context.Context, sourceName, scope, endpoint, home string, dryRun bool, stdout, stderr io.Writer) int {
	hi, err := hookInstaller(sourceName)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	path, _, err := hi.HookConfigPath(scope)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		cliErr(stderr, err)
		return 1
	}
	existing := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &existing); err != nil {
			cliErrf(stderr, "existing hook config is not valid JSON; refusing to overwrite")
			return 1
		}
	}
	hooks, _ := existing["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	// The CLI owns the shared curl command (transport + sentinel query); the
	// provider wraps it in its own per-event entry schema.
	command := toktopHookCommand(sourceName, endpoint)
	for _, event := range hi.HookEvents() {
		upsertToktopHookEntry(hooks, event, hi.HookEntry(event, command))
	}
	existing["hooks"] = hooks
	payload, err := marshalIndentNoEscape(existing)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if dryRun {
		fmt.Fprintf(stdout, "would write %s:\n%s\n", path, payload)
		return 0
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		cliErr(stderr, err)
		return 1
	}
	spool := filepath.Join(paths.DataDirUnder(home), "hooks", "spool")
	_ = os.MkdirAll(spool, 0o700)
	fmt.Fprintf(stdout, "installed toktop observer hooks source=%s in %s\nspool dir: %s\n", sourceName, path, spool)
	return 0
}

func runHookUninstall(sourceName, scope string, dryRun bool, stdout, stderr io.Writer) int {
	hi, err := hookInstaller(sourceName)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	path, _, err := hi.HookConfigPath(scope)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stdout, "no settings file at %s\n", path)
		return 0
	}
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	existing := map[string]any{}
	if err := json.Unmarshal(data, &existing); err != nil {
		cliErr(stderr, err)
		return 1
	}
	hooks, _ := existing["hooks"].(map[string]any)
	for event, entries := range hooks {
		list, ok := entries.([]any)
		if !ok {
			continue
		}
		filtered := list[:0]
		for _, item := range list {
			obj, ok := item.(map[string]any)
			if !ok {
				filtered = append(filtered, item)
				continue
			}
			if isToktopHookEntry(obj) {
				continue
			}
			filtered = append(filtered, item)
		}
		if len(filtered) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = filtered
		}
	}
	if len(hooks) == 0 {
		delete(existing, "hooks")
	} else {
		existing["hooks"] = hooks
	}
	payload, err := marshalIndentNoEscape(existing)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if dryRun {
		fmt.Fprintf(stdout, "would rewrite %s:\n%s\n", path, payload)
		return 0
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		cliErr(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "removed toktop observer hooks source=%s from %s\n", sourceName, path)
	return 0
}

const hookIntakePath = "/v1/hooks:intake"

func toktopHookCommand(sourceName, endpoint string) string {
	if after, ok := strings.CutPrefix(endpoint, "unix:"); ok {
		sock := after
		// Unix socket (default): OS file permissions gate access, so the hook
		// needs no bearer token — nothing secret is written into the settings
		// file (which syncs to cloud / backups / dotfile repos). The URL host is a
		// placeholder; --unix-socket selects the transport.
		target := appendHookQuery("http://localhost"+hookIntakePath, sourceName)
		return fmt.Sprintf(`curl -fsS --max-time 2 -o /dev/null -X POST -H 'Content-Type: application/json' --unix-socket %s --data @- %s 2>/dev/null || true`,
			shellSingleQuote(sock), shellSingleQuote(target))
	}
	target := appendHookQuery(endpoint, sourceName)
	auth := ""
	if tokenPath, ok := apiTokenPath(); ok {
		// TCP downgrade needs a bearer token, but reference the 0600 token file at
		// hook-execution time rather than baking the secret literally into the
		// settings file. The double quotes let the shell expand $(cat ...) when the
		// hook runs; the path is toktop-controlled (no spaces under the config dir).
		auth = fmt.Sprintf(`-H "Authorization: Bearer $(cat %s)" `, shellSingleQuote(tokenPath))
	}
	return fmt.Sprintf(`curl -fsS --max-time 2 -o /dev/null -X POST -H 'Content-Type: application/json' %s--data @- %s 2>/dev/null || true`, auth, shellSingleQuote(target))
}

func appendHookQuery(endpoint, sourceName string) string {
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + "provider=" + sourceName + "&toktop_observer=" + ingest.HookSentinel
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func upsertToktopHookEntry(hooks map[string]any, event string, entry map[string]any) {
	list, _ := hooks[event].([]any)
	filtered := make([]any, 0, len(list)+1)
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || !isToktopHookEntry(obj) {
			filtered = append(filtered, item)
		}
	}
	filtered = append(filtered, entry)
	hooks[event] = filtered
}

func isToktopHookEntry(obj map[string]any) bool {
	if _, ours := obj[ingest.HookSentinel]; ours {
		return true
	}
	return hookCommandContains(obj, ingest.HookSentinel)
}

func hookCommandContains(obj map[string]any, needle string) bool {
	handlers, _ := obj["hooks"].([]any)
	for _, handler := range handlers {
		handlerObj, ok := handler.(map[string]any)
		if !ok {
			continue
		}
		command, _ := handlerObj["command"].(string)
		if strings.Contains(command, needle) {
			return true
		}
	}
	return false
}

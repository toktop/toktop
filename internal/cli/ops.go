package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/httpapi"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/paths"
)

func runSources(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	format := "table"
	fs := flag.NewFlagSet("sources", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&format, "format", format, formatFlagUsage)
	setFlagUsage(fs, "usage: toktop sources [flags]", "List configured providers and their discovery roots (and whether each exists).")
	// `list` is an optional alias for the default listing; accept it regardless
	// of where flags sit, like every other keyworded command.
	if _, rest, firstPos, ok := firstLeafSubcommand(args, valueFlagSet(fs), "list"); ok {
		args = rest
	} else if firstPos != "" {
		cliErrf(stderr, "unknown sources subcommand %q (want list, or flags to list)", firstPos)
		return 2
	}
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	type sourceRoot struct {
		Source string `json:"source"`
		Root   string `json:"root"`
		Exists bool   `json:"exists"`
	}
	rows := []sourceRoot{}
	names := ingest.SortedProviders()
	// Resolve through the config loader so config.json roots show up here too,
	// consistent with `config get` and GET /v1/config.
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	loader, lerr := configFor(ctx, home)
	if lerr != nil {
		cliErr(stderr, lerr)
		return 2
	}
	for _, name := range names {
		roots := loader.Current().Roots[name]
		if len(roots) == 0 {
			rows = append(rows, sourceRoot{Source: name})
			continue
		}
		for _, root := range roots {
			rows = append(rows, sourceRoot{Source: name, Root: root, Exists: fsx.DirExists(root)})
		}
	}
	return writeFormatted(stdout, stderr, format, rows, []string{"source", "root", "exists"}, func(r sourceRoot) []string {
		return []string{r.Source, emptyDash(r.Root), boolYesNo(r.Exists)}
	})
}

// configEntry is one `config get --format json` row: the key, its effective
// value, and where that value came from (default / file config.json / env).
type configEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

func runConfig(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if printUsageForHelp(args, stdout, "usage: toktop config <get|path|set|unset> [key] [value]") {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: toktop config <get|path|set|unset> [key] [value]")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	configDir := paths.ConfigDirUnder(home)
	dataDir := paths.DataDirUnder(home)
	tokenPath := paths.APITokenPathUnder(home)
	cfgPath := filepath.Join(configDir, "config.json")

	switch sub {
	case "path":
		fs := flag.NewFlagSet("config path", flag.ContinueOnError)
		fs.SetOutput(stderr)
		setFlagUsage(fs, "usage: toktop config path", "Print the resolved home / config / data / api-token / config-file paths.")
		if code := parseFlagsNoPositionals(fs, rest, stdout, stderr); code >= 0 {
			return code
		}
		fmt.Fprintf(stdout, "home_dir: %s\n", home)
		fmt.Fprintf(stdout, "config_dir: %s\n", configDir)
		fmt.Fprintf(stdout, "data_dir: %s\n", dataDir)
		fmt.Fprintf(stdout, "api_token: %s\n", tokenPath)
		fmt.Fprintf(stdout, "config_file: %s\n", cfgPath)
		return 0
	case "get":
		format := "table"
		fs := flag.NewFlagSet("config get", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.StringVar(&format, "format", format, "output format: table or json")
		setFlagUsage(fs, "usage: toktop config get [--format table|json] [key]", "Show effective config values with their source (default / file config.json / env).")
		if code := parseFlags(fs, rest, stdout); code >= 0 {
			return code
		}
		if err := validateFormat(format, "table", "json"); err != nil {
			cliErr(stderr, err)
			return 2
		}
		keyArgs := fs.Args()
		if len(keyArgs) > 1 {
			fmt.Fprintln(stderr, "usage: toktop config get [--format table|json] [key]")
			return 2
		}
		var snap *config.Snapshot
		fileErr := ""
		if loader, err := configFor(ctx, home); err != nil {
			fileErr = "file_error: " + err.Error()
		} else {
			snap = loader.Current()
		}
		// wantKey reports whether a row is actually needed: every row in list mode,
		// only the requested key in single-key mode. It gates the eager file I/O
		// (api-token read, keySource / RootsSource config.json reads) so
		// `config get <key>` does not stat/read sources for every other key.
		wantKey := func(key string) bool {
			return len(keyArgs) == 0 || keyArgs[0] == key
		}
		// keySrc attributes an overridable key: file_error wins, then "file
		// config.json" when the key is declared, else "default". config.json is the
		// only source now (home aside), so there is no env column to disagree with.
		keySrc := func(key string) string {
			if !wantKey(key) {
				return ""
			}
			if fileErr != "" {
				return fileErr
			}
			return keySource(cfgPath, key)
		}
		redactVal, autostartVal, idleStopVal := "on", "on", "on"
		tzVal, addrVal, intervalVal := "", "", ""
		if snap != nil {
			redactVal = config.CanonicalRedact(snap.RedactPolicy)
			autostartVal = onOffText(snap.Autostart)
			idleStopVal = onOffText(snap.IdleStop)
			tzVal = snap.Timezone
			addrVal = snap.Addr
			if snap.Interval > 0 {
				intervalVal = snap.Interval.String()
			}
		}
		apiTokenSet := ""
		if wantKey("api_token_set") {
			apiTokenSet = boolYesNo(readAPITokenFile() != "")
		}
		// {key, effective value, source}. The source column shows "file config.json"
		// or "default", or file_error when config.json is unparseable — never
		// silently defaulted.
		rows := [][3]string{
			{"home_dir", home, envSource("TOKTOP_HOME")},
			{"config_dir", configDir, ""},
			{"data_dir", dataDir, ""},
			{"api_token_path", tokenPath, ""},
			{"api_token_set", apiTokenSet, ""},
			{"redact", redactVal, keySrc("redact")},
			{"autostart", autostartVal, keySrc("autostart")},
			{"idle_stop", idleStopVal, keySrc("idle_stop")},
			{"timezone", tzVal, keySrc("timezone")},
			{"addr", addrVal, keySrc("addr")},
			{"interval", intervalVal, keySrc("interval")},
		}
		// roots are per-provider; iterate the registry so a newly added provider
		// shows up automatically, no hard-coded provider names here.
		for _, name := range ingest.SortedProviders() {
			key := "roots." + name
			if !wantKey(key) {
				rows = append(rows, [3]string{key, "", ""})
				continue
			}
			var rootsStr string
			if snap != nil {
				rootsStr = strings.Join(snap.Roots[name], ", ")
			}
			src, serr := config.RootsSource(cfgPath, name)
			if serr != nil {
				src = "file_error: " + serr.Error()
			}
			rows = append(rows, [3]string{key, rootsStr, src})
		}
		if len(keyArgs) == 1 {
			for _, row := range rows {
				if row[0] == keyArgs[0] {
					if format == "json" {
						return writeJSON(stdout, stderr, configEntry{Key: row[0], Value: row[1], Source: row[2]})
					}
					fmt.Fprintln(stdout, row[1])
					return 0
				}
			}
			cliErrf(stderr, "unknown config key %q", keyArgs[0])
			return 2
		}
		if format == "json" {
			entries := make([]configEntry, 0, len(rows))
			for _, row := range rows {
				entries = append(entries, configEntry{Key: row[0], Value: row[1], Source: row[2]})
			}
			return writeJSON(stdout, stderr, entries)
		}
		for _, row := range rows {
			line := fmt.Sprintf("%-22s %s", row[0], emptyDash(row[1]))
			if row[2] != "" {
				line += "  (" + row[2] + ")"
			}
			fmt.Fprintln(stdout, line)
		}
		return 0
	case "set":
		if printUsageForHelp(rest, stdout, "usage: toktop config set <key> <value>") {
			return 0
		}
		if len(rest) != 2 {
			fmt.Fprintln(stderr, "usage: toktop config set <key> <value>")
			return 2
		}
		if err := config.SetKey(cfgPath, rest[0], rest[1]); err != nil {
			cliErr(stderr, err)
			return 2
		}
		notifyDaemonReload(stdout)
		fmt.Fprintf(stdout, "set %s = %s\n", rest[0], rest[1])
		return 0
	case "unset":
		if printUsageForHelp(rest, stdout, "usage: toktop config unset <key>") {
			return 0
		}
		if len(rest) != 1 {
			fmt.Fprintln(stderr, "usage: toktop config unset <key>")
			return 2
		}
		if err := config.UnsetKey(cfgPath, rest[0]); err != nil {
			cliErr(stderr, err)
			return 2
		}
		notifyDaemonReload(stdout)
		fmt.Fprintf(stdout, "unset %s\n", rest[0])
		return 0
	}
	cliErrf(stderr, "unknown config subcommand %q (want get|path|set|unset)", sub)
	return 2
}

// envSource reports whether home came from TOKTOP_HOME or fell back to its
// default. home is the only configuration that lives in an environment variable
// (the config file lives under home); every other key is reported by keySource.
func envSource(name string) string {
	if strings.TrimSpace(os.Getenv(name)) != "" {
		return "env " + name
	}
	return "default"
}

// notifyDaemonReload best-effort POSTs /v1/config:reload so a running daemon on
// the default address applies the change immediately. Non-fatal: a daemon on a
// custom address still picks it up via its config watcher.
func notifyDaemonReload(stdout io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	resp, err := apiRequest(ctx, http.MethodPost, defaultAPIAddr(), "/v1/config:reload", clientToken("", false), nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		fmt.Fprintln(stdout, "daemon: reloaded")
	}
}

func runEmit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	token := ""
	noAuth := false
	evType := ""
	provider := ""
	session := ""
	external := ""
	project := ""
	status := ""
	reason := ""
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&token, "token", token, "bearer token (default: read api-token file)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "do not send a bearer token")
	fs.StringVar(&evType, "type", evType, "event type, e.g. session.active (required)")
	fs.StringVar(&provider, "provider", provider, "provider/app, e.g. claude-code")
	fs.StringVar(&session, "session", session, "session id")
	fs.StringVar(&external, "external-session", external, "external session id")
	fs.StringVar(&project, "project", project, "project name")
	fs.StringVar(&status, "status", status, "status: active|awaiting_confirmation|success|failed")
	fs.StringVar(&reason, "reason", reason, "free-form reason/message")
	setFlagUsage(fs, "usage: toktop emit --type <event_type> [flags]", "Write one live event into a running daemon (POST /v1/events).")
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	if strings.TrimSpace(evType) == "" {
		fmt.Fprintln(stderr, "usage: toktop emit --type <event_type> [--session --provider --status ...]")
		return 2
	}
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	loader, lerr := configFor(ctx, home)
	if lerr != nil {
		cliErr(stderr, lerr)
		return 2
	}
	addr := clientAddr(loader.Current())
	body := map[string]any{"type": evType}
	for k, v := range map[string]string{
		"provider": provider, "session_id": session, "external_session_id": external,
		"project_name": project, "status": status, "reason": reason,
	} {
		if strings.TrimSpace(v) != "" {
			body[k] = v
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	resp, err := apiRequest(ctx, http.MethodPost, addr, "/v1/events", clientToken(token, noAuth), payload)
	if err != nil {
		cliErrf(stderr, "emit: %v", err)
		return 1
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		cliErrf(stderr, "emit: server returned %s: %s", resp.Status, strings.TrimSpace(string(out)))
		return 1
	}
	fmt.Fprintln(stdout, strings.TrimSpace(string(out)))
	return 0
}

// defaultAPIAddr resolves the default client/daemon address: the Unix socket
// under the resolved home (TOKTOP_HOME or ~/.toktop). A daemon told to listen
// elsewhere via the "addr" config key is reached through clientAddr.
func defaultAPIAddr() string {
	home, err := paths.Home()
	if err != nil {
		return "unix:" + paths.SocketPath(".toktop")
	}
	return "unix:" + paths.SocketPath(home)
}

// clientAddr is the address a client command connects to: the configured "addr"
// (so clients follow a daemon told to listen on TCP), else the default socket.
func clientAddr(snap *config.Snapshot) string {
	if snap != nil && snap.Addr != "" {
		return snap.Addr
	}
	return defaultAPIAddr()
}

// daemonControlDesc is the one-line description for each daemon control leaf's
// `-h` usage.
func daemonControlDesc(name string) string {
	switch name {
	case "status":
		return "Show whether a daemon is running for this home and what it is watching."
	case "pause":
		return "Pause the running daemon's ingest loop (the live broker stays up)."
	case "resume":
		return "Resume a paused daemon's ingest loop."
	case "trigger":
		return "Trigger a one-off ingest in the running daemon (--mode full|file|once)."
	}
	return ""
}

func runDaemonControl(ctx context.Context, method, path, name string, args []string, stdout, stderr io.Writer) int {
	token := ""
	noAuth := false
	mode := ""
	triggerPath := ""
	sync := false
	fs := flag.NewFlagSet("daemon "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&token, "token", token, "bearer token (default: read api-token file)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "do not send a bearer token")
	if path == "/v1/daemon:trigger" {
		fs.StringVar(&mode, "mode", mode, "ingest mode: full|file|once")
		fs.StringVar(&triggerPath, "path", triggerPath, "transcript path (required for --mode file)")
		fs.BoolVar(&sync, "sync", sync, "wait for the ingest to finish")
	}
	// Clean `daemon <sub> -h` usage instead of flag's default "Usage of daemon:"
	// header, matching the other leaf commands.
	setFlagUsage(fs, "usage: toktop daemon "+name+" [flags]", daemonControlDesc(name))
	if code := parseFlagsNoPositionals(fs, args, stdout, stderr); code >= 0 {
		return code
	}
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	loader, lerr := configFor(ctx, home)
	if lerr != nil {
		cliErr(stderr, lerr)
		return 2
	}
	addr := clientAddr(loader.Current())
	var payload []byte
	if path == "/v1/daemon:trigger" {
		body := map[string]any{}
		if mode != "" {
			body["mode"] = mode
		}
		if triggerPath != "" {
			body["path"] = triggerPath
		}
		if sync {
			body["sync"] = true
		}
		payload, _ = json.Marshal(body)
	}
	timeout := 10 * time.Second
	if path == "/v1/daemon:trigger" && (sync || mode == "once") {
		timeout = 0
	}
	resp, err := apiRequestWithTimeout(ctx, method, addr, path, clientToken(token, noAuth), payload, timeout)
	if err != nil {
		cliErrf(stderr, "daemon: %v", err)
		return 1
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		cliErrf(stderr, "daemon: server returned %s: %s", resp.Status, strings.TrimSpace(string(out)))
		return 1
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		fmt.Fprintln(stdout, trimmed)
	} else {
		fmt.Fprintf(stdout, "%s\n", resp.Status)
	}
	return 0
}

func clientToken(token string, noAuth bool) string {
	if noAuth {
		return ""
	}
	if strings.TrimSpace(token) != "" {
		return token
	}
	return readAPITokenFile()
}

func readAPITokenFile() string {
	p, err := paths.APITokenPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// apiTokenPath returns the absolute api-token path if the file exists, for hook
// commands that reference it at execution time (so the secret is read from its
// 0600 file rather than baked literally into a settings file).
func apiTokenPath() (string, bool) {
	p, err := paths.APITokenPath()
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// httpClientFor returns an HTTP client and base URL for addr. A unix-socket addr
// yields a client whose transport dials the socket (the URL host is an ignored
// placeholder); a TCP addr yields a normal client. A timeout of 0 means no
// timeout, used for the long-lived SSE stream.
func httpClientFor(addr string, timeout time.Duration) (*http.Client, string) {
	network, address := httpapi.SplitListenAddr(addr)
	if network == "unix" {
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", address)
				},
			},
		}, "http://unix"
	}
	return &http.Client{Timeout: timeout}, "http://" + tcpClientHost(address)
}

func tcpClientHost(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		if strings.HasPrefix(address, ":") {
			return "127.0.0.1" + address
		}
		return address
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func apiRequest(ctx context.Context, method, addr, path, token string, body []byte) (*http.Response, error) {
	return apiRequestWithTimeout(ctx, method, addr, path, token, body, 10*time.Second)
}

func apiRequestWithTimeout(ctx context.Context, method, addr, path, token string, body []byte, timeout time.Duration) (*http.Response, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	client, base := httpClientFor(addr, timeout)
	url := strings.TrimSuffix(base, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOffText(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// marshalIndentNoEscape is json.MarshalIndent without HTML escaping, so &, <, >
// in commands (e.g. the hook curl query string) are written literally instead of
// & / < / > — both in the dry-run preview and the settings file on
// disk. The trailing newline json.Encoder appends is trimmed to match MarshalIndent.
func marshalIndentNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// keySource reports where a non-env, non-roots config key resolves from for a
// `config get` listing: file config.json when present, else default.
func keySource(cfgPath, key string) string {
	if ok, err := config.FileHasKey(cfgPath, key); err != nil {
		return "file_error: " + err.Error()
	} else if ok {
		return "file config.json"
	}
	return "default"
}

// Package config resolves toktop configuration from config.json and publishes
// immutable Snapshots for the hot path. config.json is the single source of
// truth: every persistent setting lives there, and `toktop config set/get/unset`
// is how it is edited. The only configuration that does NOT live in the file is
// TOKTOP_HOME (the file lives under home — chicken/egg) and the upstream
// CLAUDE_CONFIG_DIR / CODEX_HOME provider conventions, read inside collectors.
// viper performs cold-path parsing + defaults only; the hot path reads an
// atomic Snapshot pointer with no locks.
package config

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/textutil"
)

// onOffDefaultOn reads an on/off config value through the shared ParseOnOff
// vocabulary, defaulting to ON for an empty or unrecognized value (the documented
// default for autostart/idle_stop). This matches the write side (normalizeOnOff)
// and the redact key, instead of a one-sided "!= off" literal that read every
// off-synonym ("false"/"no"/"0") and every typo as ON.
func onOffDefaultOn(value string) bool {
	on, ok := textutil.ParseOnOff(value)
	return !ok || on
}

// Snapshot is an immutable view of effective configuration. Read it via
// Loader.Current with a single atomic load — no locks, no allocation. "live"
// fields (Roots, RedactPolicy) are re-read by consumers on every use; the rest
// are consumed once at process startup.
type Snapshot struct {
	RedactPolicy redact.Policy       // parsed from the "redact" config value at reload time
	Roots        map[string][]string // provider name -> resolved root paths
	Autostart    bool                // auto-start a daemon for live commands (config key "autostart", default on)
	IdleStop     bool                // daemon self-stops when idle (config key "idle_stop", default on)
	Timezone     string              // display timezone: "" / "utc" / "local" / IANA name (config key "timezone")
	Addr         string              // daemon address: "" => unix socket; server listens on it, clients connect to it (config key "addr")
	Interval     time.Duration       // reconcile cadence; 0 => runtime default (config key "interval")
}

// Loader resolves configuration and publishes Snapshots. Reload runs on the
// cold path under mu; viper never appears on the hot path.
type Loader struct {
	current atomic.Pointer[Snapshot]
	mu      sync.Mutex
	v       *viper.Viper
	path    string
	subs    []func(*Snapshot)
}

// NewLoader builds a Loader bound to the config file at path.
func NewLoader(path string) (*Loader, error) {
	v := viper.New()
	v.SetConfigType("json")
	// Defaults live here, once. An absent or empty file yields all of these.
	v.SetDefault("redact", "on")
	v.SetDefault("autostart", "on")
	v.SetDefault("idle_stop", "on")
	v.SetDefault("timezone", "")
	v.SetDefault("addr", "")
	v.SetDefault("interval", "")
	l := &Loader{v: v, path: path}
	if err := l.Reload(); err != nil {
		return nil, err
	}
	return l, nil
}

// Current returns the latest Snapshot. Safe for concurrent hot-path use.
func (l *Loader) Current() *Snapshot { return l.current.Load() }

// OnChange registers fn to run after every successful Reload.
func (l *Loader) OnChange(fn func(*Snapshot)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subs = append(l.subs, fn)
}

// Reload re-reads the file, re-resolves the chain, validates, and atomically
// swaps in a new Snapshot. On error the previous Snapshot is kept (fail-safe)
// so a bad edit never disrupts a running broker.
func (l *Loader) Reload() error {
	var snap *Snapshot
	var subs []func(*Snapshot)
	if err := func() error {
		l.mu.Lock()
		defer l.mu.Unlock()

		// Read bytes ourselves: viper.ReadInConfig on an empty/whitespace file
		// returns "unexpected end of JSON input". Treat missing/empty/whitespace as
		// an empty object so defaults apply.
		data, err := os.ReadFile(l.path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("config: read %s: %w", l.path, err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			data = []byte("{}")
		}
		if err := l.v.ReadConfig(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("config: parse %s: %w", l.path, err)
		}

		policy, err := redact.PolicyFromString(l.v.GetString("redact"))
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		// interval is stored as a duration string ("30s", "5m"); empty => runtime
		// default. An unparseable value fails the reload (fail-safe keeps the
		// previous snapshot) rather than silently reverting to the default.
		var interval time.Duration
		if s := strings.TrimSpace(l.v.GetString("interval")); s != "" {
			interval, err = time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("config: interval %q: %w", s, err)
			}
		}

		// Resolve roots for every registered provider so a newly added provider
		// shows up automatically — no hard-coded provider list here. config.json
		// stores roots as {"roots": {"<provider>": [...]}}.
		fileRoots := l.v.GetStringMapStringSlice("roots")
		roots := make(map[string][]string)
		for _, name := range ingest.SortedProviders() {
			roots[name] = ingest.RootPaths(ingest.ResolveRoots(name, nil, fileRoots[name]))
		}
		snap = &Snapshot{
			RedactPolicy: policy,
			Roots:        roots,
			Autostart:    onOffDefaultOn(l.v.GetString("autostart")),
			IdleStop:     onOffDefaultOn(l.v.GetString("idle_stop")),
			Timezone:     strings.TrimSpace(l.v.GetString("timezone")),
			Addr:         strings.TrimSpace(l.v.GetString("addr")),
			Interval:     interval,
		}
		l.current.Store(snap)
		subs = append([]func(*Snapshot){}, l.subs...)
		return nil
	}(); err != nil {
		return err
	}
	for _, fn := range subs {
		fn(snap)
	}
	return nil
}

// Watch monitors the config file's directory and reloads on change, blocking
// until ctx is cancelled. Reload errors are logged, never fatal.
func (l *Loader) Watch(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("config: watcher: %w", err)
	}
	defer w.Close()
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	if err := w.Add(dir); err != nil {
		return fmt.Errorf("config: watch %s: %w", dir, err)
	}
	base := filepath.Base(l.path)
	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		<-debounce.C
	}
	defer debounce.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-w.Events:
			// Watch the directory (not the inode) so atomic rename writes are
			// still seen; filter to our file by basename.
			if filepath.Base(ev.Name) == base {
				debounce.Reset(200 * time.Millisecond)
			}
		case err := <-w.Errors:
			logger.Warn("config watch error", "err", err)
		case <-debounce.C:
			if err := l.Reload(); err != nil {
				logger.Warn("config reload failed; keeping previous snapshot", "err", err)
			} else {
				logger.Info("config reloaded", "path", l.path)
			}
		}
	}
}

// CanonicalRedact renders a redact policy as its canonical "on"/"off" string.
// Exported so the cli/httpapi display surfaces derive it from RedactPolicy
// instead of the Snapshot carrying a second, hand-synced copy.
func CanonicalRedact(p redact.Policy) string {
	if p.Enabled {
		return "on"
	}
	return "off"
}

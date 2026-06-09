package runtime

import (
	"io"
	"log/slog"
	"time"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/redact"
)

const (
	// DefaultInterval is the periodic full-reconcile cadence — a SAFETY NET for
	// fsnotify events that were missed or coalesced, used when the "interval"
	// config key is unset. The primary update path is event-driven (fsnotify +
	// debounce), near-instant and zero-cost when idle. Each reconcile tick walks
	// the tree and stats every file, but unchanged files are skipped by the
	// (size, mtime, inode) fingerprint, so an idle pass is cheap. This is the
	// single source for the default; set "interval" in config.json to override.
	DefaultInterval = 30 * time.Second
	DefaultDebounce = 500 * time.Millisecond

	// DefaultFullIngestTimeout and DefaultFileIngestTimeout are the per-job ingest
	// timeouts that bound a single execIngestJob so a hung filesystem
	// (NFS stall, a SIGSTOP'd writer, a wedged kernel buffer) cannot pin the
	// single ingest worker goroutine — which would fill ingestCh and make
	// enqueueAuto silently drop every later watch/reconcile job while the daemon
	// still reports healthy.
	DefaultFullIngestTimeout = 3 * time.Minute
	DefaultFileIngestTimeout = 30 * time.Second
)

type Config struct {
	Sources []string

	DataDir string

	Interval time.Duration

	Debounce time.Duration

	// FullIngestTimeout / FileIngestTimeout bound a single ingest job (see the
	// Default*IngestTimeout consts). Zero falls back to the default.
	FullIngestTimeout time.Duration
	FileIngestTimeout time.Duration

	Policy redact.Policy

	Logger *slog.Logger

	Emitter liveevent.Emitter

	Stdout io.Writer

	// Provider, when set, supplies hot-reloadable config (daemon/serve). Nil
	// falls back to the static Policy field.
	Provider ConfigProvider
}

// ConfigProvider exposes the current configuration Snapshot and a change hook.
type ConfigProvider interface {
	Current() *config.Snapshot
	OnChange(fn func(*config.Snapshot))
}

func (c Config) interval() time.Duration {
	if c.Interval <= 0 {
		return DefaultInterval
	}
	return c.Interval
}

func (c Config) debounce() time.Duration {
	if c.Debounce <= 0 {
		return DefaultDebounce
	}
	return c.Debounce
}

func (c Config) fullIngestTimeout() time.Duration {
	if c.FullIngestTimeout <= 0 {
		return DefaultFullIngestTimeout
	}
	return c.FullIngestTimeout
}

func (c Config) fileIngestTimeout() time.Duration {
	if c.FileIngestTimeout <= 0 {
		return DefaultFileIngestTimeout
	}
	return c.FileIngestTimeout
}

func (c Config) logger() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

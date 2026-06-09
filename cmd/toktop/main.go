package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"toktop.unceas.dev/internal/cli"

	// Side-effect imports: each collector's init() registers its provider with
	// the ingest registry. main is the single composition root, so the set of
	// built-in providers lives here directly.
	_ "toktop.unceas.dev/internal/collector/claudecode"
	_ "toktop.unceas.dev/internal/collector/codex"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	info := cli.Info{
		Name:    "toktop",
		Version: version,
		Commit:  commit,
		Date:    date,
	}

	// Cancel the root context on SIGINT/SIGTERM so long-running commands
	// (serve, daemon) run their graceful-shutdown path — HTTP Shutdown, SSE
	// drain, store.Close (WAL checkpoint) — instead of being hard-killed. A
	// second signal falls through to the default handler and force-quits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, info)
	stop()
	os.Exit(code)
}

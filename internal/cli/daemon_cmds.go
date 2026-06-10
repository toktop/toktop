package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"toktop.unceas.dev/internal/httpapi"
	"toktop.unceas.dev/internal/paths"
	"toktop.unceas.dev/internal/runtime"
)

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	token := ""
	noAuth := false
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&token, "token", token, "bearer token for TCP; auto-generated to ~/.toktop/config/api-token when empty (unix socket needs none)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "disable bearer token enforcement (TCP loopback only)")
	setFlagUsage(fs, "usage: toktop serve [flags]", "Start the local HTTP API v1 server (no transcript watching; use `daemon serve` for that).")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	loader, err := configFor(ctx, home)
	if err != nil {
		cliErrf(stderr, "load config: %v", err)
		return 1
	}
	snap := loader.Current()
	addr := snap.Addr
	if addr == "" {
		addr = "unix:" + paths.SocketPath(home)
	}
	release, ok, err := acquireDaemonLock(home)
	if err != nil {
		cliErrf(stderr, "daemon lock: %v", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "toktop: a daemon is already running for this home; not starting another")
		return 0
	}
	defer release()
	// A unix socket is gated by file permissions (0600) and needs no token; a TCP
	// listener does (unless --no-auth on loopback).
	if network, _ := httpapi.SplitListenAddr(addr); network == "tcp" && !noAuth && token == "" {
		t, err := ensureAPIToken(stderr)
		if err != nil {
			cliErr(stderr, err)
			return 1
		}
		token = t
		fmt.Fprintln(stdout, "auth: bearer token loaded from ~/.toktop/config/api-token")
	}
	go func() {
		if err := loader.Watch(ctx, slog.Default()); err != nil {
			fmt.Fprintf(stderr, "config watch: %v\n", err)
		}
	}()

	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	server, err := httpapi.NewServer(ctx, httpapi.Options{
		DataDir:           paths.DataDirUnder(home),
		Token:             token,
		WipeGuard:         storeWipeGuard(home),
		ConfigLoader:      loader,
		IdleShutdownAfter: idleShutdownFor(snap.IdleStop),
		OnIdle:            cancelServe,
	})
	if err != nil {
		cliErrf(stderr, "create server: %v", err)
		return 1
	}
	fmt.Fprintln(stdout, listeningMessage(addr))
	if err := server.ListenAndServe(serveCtx, addr); err != nil {
		cliErrf(stderr, "serve: %v", err)
		return 1
	}
	return 0
}

// DefaultIdleShutdown is how long a serving daemon waits with zero SSE
// subscribers before self-stopping. 0 (config idle_stop=off) disables it.
const DefaultIdleShutdown = 60 * time.Second

// idleShutdownFor returns the idle-stop grace: 0 (disabled) when config
// idle_stop=off, else DefaultIdleShutdown.
func idleShutdownFor(idleStop bool) time.Duration {
	if !idleStop {
		return 0
	}
	return DefaultIdleShutdown
}

// listeningMessage describes the bound address for the startup log line.
func listeningMessage(addr string) string {
	network, address := httpapi.SplitListenAddr(addr)
	if network == "unix" {
		return "listening: unix socket " + address
	}
	return "listening: http://" + address
}

func ensureAPIToken(stderr io.Writer) (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	dir := paths.ConfigDirUnder(home)
	tokenPath := paths.APITokenPathUnder(home)
	if data, err := os.ReadFile(tokenPath); err == nil {
		token := strings.TrimSpace(string(data))
		if token != "" {
			return token, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	token, err := httpapi.GenerateToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	cliErrf(stderr, "generated new API token at %s", tokenPath)
	return token, nil
}

func runDaemon(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if printUsageForHelp(args, stdout, "usage: toktop daemon <run|serve|stop|status|pause|resume|trigger> [flags]") {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: toktop daemon <run|serve|stop|status|pause|resume|trigger> [flags]")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "run":
		return runDaemonLoop(ctx, rest, false, stdout, stderr)
	case "serve":
		return runDaemonLoop(ctx, rest, true, stdout, stderr)

	case "status":
		return runDaemonControl(ctx, "GET", "/v1/daemon", rest, stdout, stderr)
	case "pause":
		return runDaemonControl(ctx, "POST", "/v1/daemon:pause", rest, stdout, stderr)
	case "resume":
		return runDaemonControl(ctx, "POST", "/v1/daemon:resume", rest, stdout, stderr)
	case "trigger":
		return runDaemonControl(ctx, "POST", "/v1/daemon:trigger", rest, stdout, stderr)
	case "stop":
		return runDaemonStop(stdout, stderr)
	}
	cliErrf(stderr, "unknown daemon subcommand %q (want run|serve|stop|status|pause|resume|trigger)", sub)
	return 2
}

func runDaemonStop(stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	pidPath := paths.PidPath(home)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "no daemon running")
			return 0
		}
		cliErr(stderr, err)
		return 1
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil || pid <= 0 {
		_ = os.Remove(pidPath)
		fmt.Fprintln(stdout, "no daemon running (stale pidfile cleaned)")
		return 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		cliErr(stderr, err)
		return 1
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		_ = os.Remove(pidPath)
		fmt.Fprintln(stdout, "daemon not running (stale pidfile cleaned)")
		return 0
	}
	// The daemon's signal handler runs graceful shutdown, which removes the
	// socket. Poll for it to confirm.
	sock := paths.SocketPath(home)
	for range 50 {
		if _, statErr := os.Stat(sock); os.IsNotExist(statErr) {
			fmt.Fprintln(stdout, "daemon stopped")
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(stdout, "sent SIGTERM; daemon is shutting down")
	return 0
}

func runDaemonLoop(ctx context.Context, args []string, serveAPIDefault bool, stdout, stderr io.Writer) int {
	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	var sourcesFlag rootList
	once := false
	serveAPI := serveAPIDefault
	token := ""
	noAuth := false
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&once, "once", once, "run one scan and exit")
	fs.Var(&sourcesFlag, "sources", "providers to watch/import (default: auto-detected on-disk providers); may be repeated or comma-separated")
	fs.StringVar(&token, "token", token, "bearer token (serve)")
	fs.BoolVar(&noAuth, "no-auth", noAuth, "disable bearer token enforcement (serve, loopback only)")
	if code := parseFlags(fs, args, stdout); code >= 0 {
		return code
	}
	if serveAPIDefault && once {
		cliErrf(stderr, "daemon serve cannot be combined with --once (the HTTP API and live broker need the long-running loop); use daemon run --once for a one-shot scan")
		return 2
	}
	loader, err := configFor(ctx, home)
	if err != nil {
		cliErrf(stderr, "load config: %v", err)
		return 1
	}
	snap := loader.Current()
	policy := snap.RedactPolicy
	// No --sources: watch/import every provider whose roots exist on disk. The
	// autostart child reaches here too (no flag threaded), so it auto-detects.
	sourceNames, serr := scopeSources(sourcesFlag, snap)
	if serr != nil {
		cliErr(stderr, serr)
		return 2
	}

	// Take the single-instance lock BEFORE opening the store: a second daemon must
	// fail fast here, never reaching sqlite.Open (which runs migrations) — otherwise
	// two daemons race schema setup on a fresh home. Mirrors runServe's ordering.
	release, ok, err := acquireDaemonLock(home)
	if err != nil {
		cliErrf(stderr, "daemon lock: %v", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "toktop: a daemon is already running for this home; not starting another")
		return 0
	}
	defer release()

	store, err := openStore(ctx, home)
	if err != nil {
		cliErrf(stderr, "open store: %v", err)
		return 1
	}
	defer store.Close()

	var liveServer *httpapi.Server
	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()
	var serverDone chan error
	if serveAPI && !once {
		addr := snap.Addr
		if addr == "" {
			addr = "unix:" + paths.SocketPath(home)
		}
		// Unix socket: file permissions gate access, no token. TCP: token required
		// unless --no-auth on loopback.
		if network, _ := httpapi.SplitListenAddr(addr); network == "tcp" && !noAuth && token == "" {
			t, err := ensureAPIToken(stderr)
			if err != nil {
				cliErr(stderr, err)
				return 1
			}
			token = t
			fmt.Fprintln(stdout, "auth: bearer token loaded from ~/.toktop/config/api-token")
		}
		server, err := httpapi.NewServer(ctx, httpapi.Options{
			DataDir:           paths.DataDirUnder(home),
			Token:             token,
			WipeGuard:         storeWipeGuard(home),
			ConfigLoader:      loader,
			IdleShutdownAfter: idleShutdownFor(snap.IdleStop),
			OnIdle:            cancelLoop,
		})
		if err != nil {
			cliErrf(stderr, "create server: %v", err)
			return 1
		}
		liveServer = server
		serverDone = make(chan error, 1)
		go func() {
			err := server.ListenAndServe(loopCtx, addr)
			if err != nil {

				cancelLoop()
			}
			serverDone <- err
		}()
		fmt.Fprintln(stdout, listeningMessage(addr))
	}

	cfg := runtime.Config{
		Sources:  sourceNames,
		DataDir:  paths.DataDirUnder(home),
		Interval: snap.Interval,
		Policy:   policy,
		Stdout:   stdout,
		Provider: loader,
	}
	if liveServer != nil {
		cfg.Emitter = liveServer
	}
	svc, err := runtime.New(store, cfg)
	if err != nil {
		cliErrf(stderr, "create runtime: %v", err)
		return 1
	}
	go func() {
		if err := loader.Watch(loopCtx, slog.Default()); err != nil {
			fmt.Fprintf(stderr, "config watch: %v\n", err)
		}
	}()
	if liveServer != nil {
		liveServer.AttachRuntime(svc)
	}

	var runErr error
	if once {
		runErr = svc.RunOnce(loopCtx)
	} else {
		runErr = svc.Run(loopCtx)
	}

	// The run loop has returned — either normally (loopCtx was cancelled, e.g.
	// by a signal) or with a startup error that did NOT cancel loopCtx. Cancel
	// loopCtx unconditionally so the server goroutine's ListenAndServe unwinds;
	// otherwise the <-serverDone wait below would block forever whenever Run
	// failed without a context cancellation.
	cancelLoop()

	if serverDone != nil {

		if serverErr := <-serverDone; serverErr != nil {
			cliErrf(stderr, "serve: %v", serverErr)
			return 1
		}
	}
	if runErr != nil {
		cliErrf(stderr, "daemon: %v", runErr)
		return 1
	}
	return 0
}

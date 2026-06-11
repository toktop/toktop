package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"

	"toktop.unceas.dev/internal/httpapi"
	"toktop.unceas.dev/internal/paths"
)

const autostartWaitTimeout = 5 * time.Second

// ensureDaemon makes a daemon reachable at addr before a live command connects.
// It returns nil (no-op) when auto-start is disabled, when addr is not the
// default unix socket (the `addr` config key points elsewhere — we don't manage it),
// or when a daemon is already reachable. Otherwise it spawns a detached
// `toktop daemon serve` and waits for the socket to accept connections. On failure
// it returns an error; callers fall back to their existing no-daemon behavior.
func ensureDaemon(ctx context.Context, home, addr string, enabled bool, stderr io.Writer) error {
	if !enabled {
		return nil
	}
	network, address := httpapi.SplitListenAddr(addr)
	if network != "unix" || address != paths.SocketPath(home) {
		return nil
	}
	if daemonReachable(address, 300*time.Millisecond) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate toktop binary: %w", err)
	}
	if err := os.MkdirAll(paths.RunDirUnder(home), 0o700); err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}
	logFile, err := os.OpenFile(paths.DaemonLogPath(home), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	// Pass home via TOKTOP_HOME (there is no --home flag); the child resolves the
	// same home, config, and socket from it.
	cmd := exec.Command(exe, "daemon", "serve")
	cmd.Env = append(os.Environ(), "TOKTOP_HOME="+home)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	fmt.Fprintf(stderr, "toktop: starting daemon in background (%s)\n", paths.DaemonLogPath(home))
	deadline := time.Now().Add(autostartWaitTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if daemonReachable(address, 300*time.Millisecond) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready within %s (see %s)", autostartWaitTimeout, paths.DaemonLogPath(home))
}

func daemonReachable(socketPath string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

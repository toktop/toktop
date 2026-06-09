//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"toktop.unceas.dev/internal/paths"
)

// acquireDaemonLock takes an exclusive, non-blocking advisory lock on the
// per-home lock file and writes the current PID to the pidfile. The returned
// release closes the lock and removes the pidfile; the underlying *os.File stays
// open until then (closing it would drop the flock). ok is false (err nil) when
// another daemon already holds the lock — the caller should exit.
func acquireDaemonLock(home string) (release func(), ok bool, err error) {
	if err := os.MkdirAll(paths.RunDirUnder(home), 0o700); err != nil {
		return nil, false, fmt.Errorf("create run dir: %w", err)
	}
	f, err := os.OpenFile(paths.LockPath(home), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock %s: %w", paths.LockPath(home), err)
	}
	pidPath := paths.PidPath(home)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, false, fmt.Errorf("write pidfile: %w", err)
	}
	return func() {
		_ = os.Remove(pidPath)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// setDetached configures cmd to outlive the spawning CLI process (own session).
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

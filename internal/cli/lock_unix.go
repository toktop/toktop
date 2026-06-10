//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		// The wipe guard's probe (daemonLockedElsewhere) holds a transient
		// LOCK_SH on this file; one short retry keeps that microsecond window
		// from reading as a resident daemon. A real daemon still holds the
		// lock on the retry.
		time.Sleep(20 * time.Millisecond)
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
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

// daemonLockedElsewhere reports whether a process other than this one holds the
// per-home daemon single-instance lock, and that holder's PID (0 if unknown).
// flock conflicts even between fds of the same process, so the holder is
// identified via the pidfile: the daemon's own store open (lock taken before
// sqlite.Open) must not read as "elsewhere".
func daemonLockedElsewhere(home string) (pid int, held bool) {
	f, err := os.Open(paths.LockPath(home))
	if err != nil {
		// Never created (no daemon has ever run for this home) — or unreadable,
		// in which case acquireDaemonLock would fail the same way.
		return 0, false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return 0, false
	}
	if raw, err := os.ReadFile(paths.PidPath(home)); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	if pid == os.Getpid() {
		return pid, false
	}
	return pid, true
}

// setDetached configures cmd to outlive the spawning CLI process (own session).
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

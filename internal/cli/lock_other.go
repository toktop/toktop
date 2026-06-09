//go:build windows

package cli

import "os/exec"

// acquireDaemonLock is a best-effort no-op on Windows (no advisory flock);
// single-instance falls back to the listener bind failing for a second daemon.
func acquireDaemonLock(home string) (release func(), ok bool, err error) {
	return func() {}, true, nil
}

// setDetached is a no-op on Windows; the spawned process is left as-is.
func setDetached(cmd *exec.Cmd) {}

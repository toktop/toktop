//go:build windows

package cli

import "os/exec"

// acquireDaemonLock is a best-effort no-op on Windows (no advisory flock);
// single-instance falls back to the listener bind failing for a second daemon.
func acquireDaemonLock(home string) (release func(), ok bool, err error) {
	return func() {}, true, nil
}

// daemonLockAuthoritative reports whether a daemonLockedElsewhere held==false
// result is trustworthy. On Windows there is no advisory flock to probe, so the
// probe is non-authoritative and callers must not treat held==false as proof no
// daemon is running.
const daemonLockAuthoritative = false

// daemonLockedElsewhere is a best-effort no-op on Windows: with no advisory
// flock there is no lock to probe, matching acquireDaemonLock above.
func daemonLockedElsewhere(string) (pid int, held bool) {
	return 0, false
}

// setDetached is a no-op on Windows; the spawned process is left as-is.
func setDetached(cmd *exec.Cmd) {}

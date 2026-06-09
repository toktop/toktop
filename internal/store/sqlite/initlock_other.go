//go:build windows

package sqlite

// acquireInitLock is a best-effort no-op on Windows (no advisory flock). The
// narrow first-init goose-migration race remains only there; it is harmless
// (transactional DDL rolls back, no corruption) and matches the daemon
// single-instance lock's Windows fallback.
func acquireInitLock(dataDir string) (release func(), err error) {
	return func() {}, nil
}

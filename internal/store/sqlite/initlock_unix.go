//go:build !windows

package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireInitLock takes a blocking exclusive advisory lock on <dataDir>/.init.lock,
// serializing one-time schema setup (the WAL pragma + goose migrations) across
// processes. Two `toktop` commands opening a fresh home at the same instant would
// otherwise race goose DDL: one wins and the loser errors with "database is locked"
// or "table X already exists" (the DDL is transactional, so it rolls back and never
// corrupts — but the message is ugly and the work is wasted). With this lock the
// loser blocks until the winner finishes, then sees the schema already current
// ("goose: no migrations to run") and opens cleanly. init is fast and bounded, so a
// blocking (rather than fail-fast) lock is correct here.
func acquireInitLock(dataDir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dataDir, ".init.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open init lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err == nil {
			break
		}
		if errors.Is(err, syscall.EINTR) { // interrupted by a signal; retry the blocking lock
			continue
		}
		_ = f.Close()
		return nil, fmt.Errorf("flock init lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

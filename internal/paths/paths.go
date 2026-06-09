package paths

import (
	"os"
	"path/filepath"
)

const appDirName = ".toktop"

// Home returns the root toktop directory. It honors the TOKTOP_HOME override and
// otherwise defaults to ~/.toktop. Configuration and data live in the config/ and
// data/ subdirectories beneath this root.
func Home() (string, error) {
	if dir := os.Getenv("TOKTOP_HOME"); dir != "" {
		return filepath.Clean(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, appDirName), nil
}

// ConfigDir returns the directory holding configuration files (api-token):
// <home>/config.
func ConfigDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return ConfigDirUnder(home), nil
}

// DataDir returns the directory holding data files (sqlite db, event log, hook
// spool): <home>/data.
func DataDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return DataDirUnder(home), nil
}

// ConfigDirUnder returns the config directory for an explicit home root, used by
// commands that accept a --home override.
func ConfigDirUnder(home string) string {
	return filepath.Join(home, "config")
}

// DataDirUnder returns the data directory for an explicit home root, used by
// commands that accept a --home override.
func DataDirUnder(home string) string {
	return filepath.Join(home, "data")
}

// SocketPath returns the Unix domain socket the daemon listens on by default:
// <home>/run/toktop.sock. The socket (0600) and its parent dir (0700) give
// OS-enforced, same-user-only access — the default IPC transport.
func SocketPath(home string) string {
	return filepath.Join(home, "run", "toktop.sock")
}

// RunDirUnder returns the runtime directory (socket, lock, pidfile, daemon log):
// <home>/run.
func RunDirUnder(home string) string {
	return filepath.Join(home, "run")
}

// LockPath is the per-home daemon single-instance lock file.
func LockPath(home string) string {
	return filepath.Join(home, "run", "toktop.lock")
}

// PidPath records the running daemon's PID, for `toktop daemon stop`.
func PidPath(home string) string {
	return filepath.Join(home, "run", "toktop.pid")
}

// DaemonLogPath is where an auto-started detached daemon's stdout/stderr go.
func DaemonLogPath(home string) string {
	return filepath.Join(home, "run", "daemon.log")
}

const apiTokenFile = "api-token"

// APITokenPath returns the bearer-token file under the config dir derived from
// home (TOKTOP_HOME / ~/.toktop): <home>/config/api-token. It is the single source
// of this well-known path, alongside the SocketPath/LockPath/… family.
func APITokenPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, apiTokenFile), nil
}

// APITokenPathUnder returns the bearer-token file for an explicit home root.
func APITokenPathUnder(home string) string {
	return filepath.Join(ConfigDirUnder(home), apiTokenFile)
}

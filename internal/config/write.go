package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/textutil"
)

// remoteSettable is the allow-list of config keys that may be set over the HTTP
// API (POST /v1/config:set), and thus from the web UI. Default-deny: addr and
// roots.* are exposure-affecting (network reach / what gets ingested), and any
// unknown or future key is excluded until explicitly opted in — so a local web
// client can never widen the daemon's own attack surface. Those keys require
// shell access via `toktop config set`.
var remoteSettable = map[string]bool{
	"redact":    true,
	"autostart": true,
	"idle_stop": true,
	"timezone":  true,
	"interval":  true,
}

// RemoteSettable reports whether key may be set via the HTTP API.
func RemoteSettable(key string) bool { return remoteSettable[key] }

// SetKey validates+normalizes value for key, then writes it into config.json,
// preserving other keys via a temp-file + rename atomic write. Keys are
// redact, autostart, idle_stop, timezone, addr, interval, or "roots.<provider>"
// (provider must be registered). Invalid values are rejected (never written) so
// a bad value can't break hot reload / startup.
func SetKey(path, key, value string) error {
	m, err := readFileMap(path)
	if err != nil {
		return err
	}
	switch {
	case key == "redact":
		policy, err := redact.PolicyFromString(value)
		if err != nil {
			return err
		}
		m["redact"] = CanonicalRedact(policy)
	case strings.HasPrefix(key, "roots."):
		provider := strings.TrimPrefix(key, "roots.")
		if !ingest.HasProvider(provider) {
			return fmt.Errorf("config: unknown provider %q", provider)
		}
		paths := cleanPaths(value)
		if len(paths) == 0 {
			return fmt.Errorf("config: %s: no non-empty paths in %q", key, value)
		}
		setRoots(m, provider, paths)
	case key == "autostart" || key == "idle_stop":
		v, err := normalizeOnOff(value)
		if err != nil {
			return err
		}
		m[key] = v
	case key == "timezone":
		tz, err := normalizeTimezone(value)
		if err != nil {
			return err
		}
		if tz == "" {
			delete(m, "timezone")
		} else {
			m["timezone"] = tz
		}
	case key == "addr":
		// Any non-empty string is a valid address (see httpapi.SplitListenAddr:
		// unix:/path, tcp://host:port, /path, or bare host:port). Empty reverts
		// to the default unix socket, so treat it as an unset.
		if a := strings.TrimSpace(value); a == "" {
			delete(m, "addr")
		} else {
			m["addr"] = a
		}
	case key == "interval":
		d, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("config: interval %q: %w", value, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: interval must be > 0, got %q", value)
		}
		m["interval"] = strings.TrimSpace(value)
	default:
		return fmt.Errorf("config: unknown key %q (want redact, autostart, idle_stop, timezone, addr, interval, or roots.<provider>)", key)
	}
	return writeFileMap(path, m)
}

// UnsetKey removes key from config.json. Scalar keys revert to their built-in
// defaults; "roots.<provider>" reverts to discovery (the provider's upstream
// env convention such as CLAUDE_CONFIG_DIR/CODEX_HOME, then its default root).
func UnsetKey(path, key string) error {
	m, err := readFileMap(path)
	if err != nil {
		return err
	}
	switch {
	case key == "redact":
		delete(m, "redact")
	case strings.HasPrefix(key, "roots."):
		provider := strings.TrimPrefix(key, "roots.")
		if !ingest.HasProvider(provider) {
			return fmt.Errorf("config: unknown provider %q", provider)
		}
		if roots, ok := m["roots"].(map[string]any); ok {
			delete(roots, provider)
			if len(roots) == 0 {
				delete(m, "roots")
			} else {
				m["roots"] = roots
			}
		}
	case key == "autostart" || key == "idle_stop" || key == "timezone" || key == "addr" || key == "interval":
		delete(m, key)
	default:
		return fmt.Errorf("config: unknown key %q (want redact, autostart, idle_stop, timezone, addr, interval, or roots.<provider>)", key)
	}
	return writeFileMap(path, m)
}

// FileHasKey reports whether config.json declares key (any SetKey key, e.g.
// "redact" or "roots.<provider>"). Returns (false, err) on a parse error so
// callers can surface file_error instead of silently defaulting.
func FileHasKey(path, key string) (bool, error) {
	m, err := readFileMap(path)
	if err != nil {
		return false, err
	}
	if provider, ok := strings.CutPrefix(key, "roots."); ok {
		roots, _ := m["roots"].(map[string]any)
		_, has := roots[provider]
		return has, nil
	}
	_, ok := m[key]
	return ok, nil
}

// cleanPaths splits a comma-separated value into trimmed, non-empty paths.
func cleanPaths(value string) []string {
	return textutil.SplitTrim(value)
}

// setRoots writes paths under m["roots"][provider], creating the nested map.
func setRoots(m map[string]any, provider string, paths []string) {
	roots, _ := m["roots"].(map[string]any)
	if roots == nil {
		roots = map[string]any{}
	}
	roots[provider] = paths
	m["roots"] = roots
}

// RootsSource returns where a provider's roots resolve from for a non-serving
// CLI invocation (no explicit override roots): the Kind of the first resolved
// root (env/file/default), or "default" when none resolve.
func RootsSource(cfgPath, provider string) (string, error) {
	fileRoots, err := fileRootsFor(cfgPath, provider)
	if err != nil {
		return "", err
	}
	roots := ingest.ResolveRoots(provider, nil, fileRoots)
	if len(roots) == 0 {
		return "default", nil
	}
	return roots[0].Kind, nil
}

// fileRootsFor reads config.json's roots[provider] as a string slice.
func fileRootsFor(cfgPath, provider string) ([]string, error) {
	m, err := readFileMap(cfgPath)
	if err != nil {
		return nil, err
	}
	roots, _ := m["roots"].(map[string]any)
	arr, _ := roots[provider].([]any)
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func readFileMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	m := map[string]any{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	return m, nil
}

// normalizeTimezone validates a display-timezone value. It accepts "" (unset),
// "utc"/"local" (case-insensitive), or an IANA name resolvable by
// time.LoadLocation. The trimmed original is returned; resolveDisplayLocation
// interprets it on the read side.
func normalizeTimezone(value string) (string, error) {
	v := strings.TrimSpace(value)
	switch strings.ToLower(v) {
	case "", "utc", "local":
		return v, nil
	}
	if _, err := time.LoadLocation(v); err != nil {
		return "", fmt.Errorf("config: invalid timezone %q: %w", value, err)
	}
	return v, nil
}

// normalizeOnOff validates an on/off config value, accepting common synonyms.
func normalizeOnOff(value string) (string, error) {
	on, ok := textutil.ParseOnOff(value)
	if !ok {
		return "", fmt.Errorf("config: want on/off, got %q", value)
	}
	if on {
		return "on", nil
	}
	return "off", nil
}

func writeFileMap(path string, m map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

package opencode

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/trace"
)

// scanDeclaredMCPServers returns opencode's declared MCP servers and whether the
// scan was complete. opencode declares MCP servers in its JSON config
// (~/.config/opencode/opencode.json), NOT in the session DB, so this is a
// config-FILE scan like codex's config.toml scan. complete is false when an
// existing config could not be read or decoded, so the caller skips a metadata
// reconcile rather than deleting rows a transient/corrupt read merely hid.
func scanDeclaredMCPServers(ctx context.Context, _ []SourceRoot) ([]trace.MCPServer, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path := globalConfigPath()
	servers, ok := scanOpencodeConfigJSON(path)
	slices.SortFunc(servers, func(a, b trace.MCPServer) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ConfigPath, b.ConfigPath))
	})
	return servers, ok, nil
}

// globalConfigPath is opencode's global config file: $XDG_CONFIG_HOME/opencode/
// opencode.json, defaulting to ~/.config/opencode/opencode.json.
func globalConfigPath() string {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "opencode", "opencode.json")
}

// opencodeMCPServer is one entry of opencode's top-level "mcp" config object: a
// discriminated union on type ("local" = stdio command, "remote" = http url).
type opencodeMCPServer struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	URL     string   `json:"url"`
	Enabled *bool    `json:"enabled"`
}

func (s opencodeMCPServer) enabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// scanOpencodeConfigJSON returns the servers declared in an opencode.json and
// whether the read was complete. A genuinely absent file is complete (no
// servers); an unreadable or corrupt one is not.
func scanOpencodeConfigJSON(path string) ([]trace.MCPServer, bool) {
	if path == "" {
		return nil, false
	}
	data, exists, ok := collector.ReadFileState(path)
	if !ok {
		return nil, false
	}
	if !exists {
		return nil, true
	}
	hash := collector.HashContent(data)

	var doc struct {
		MCP map[string]opencodeMCPServer `json:"mcp"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		// A corrupt/half-written config otherwise looks identical to "no servers".
		// Treat it as incomplete so a transient bad read does not delete stored rows.
		slog.Warn("opencode config decode failed", "path", path, "err", err)
		return nil, false
	}

	names := slices.Sorted(maps.Keys(doc.MCP))
	out := make([]trace.MCPServer, 0, len(names))
	for _, name := range names {
		server := doc.MCP[name]
		out = append(out, trace.MCPServer{
			ID:         collector.MCPServerID("user", path, name),
			Name:       name,
			Scope:      "user",
			Transport:  collector.ClassifyMCPTransport(server.Type, server.URL, strings.Join(server.Command, " ")),
			ConfigPath: path,
			ConfigHash: hash,
			Enabled:    server.enabled(),
		})
	}
	return out, true
}

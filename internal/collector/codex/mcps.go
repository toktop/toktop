package codex

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/trace"
)

// scanDeclaredMCPServers returns the declared MCP servers and whether the scan
// was complete. complete is false when an existing config.toml could not be read
// or decoded, so the caller skips a metadata reconcile rather than deleting rows
// a transient/corrupt read merely hid.
func scanDeclaredMCPServers(ctx context.Context, roots []SourceRoot) ([]trace.MCPServer, bool, error) {
	paths := codexConfigPaths(roots)
	seen := make(map[string]struct{})
	out := make([]trace.MCPServer, 0, 8)
	complete := true
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		servers, ok := scanCodexConfigTOML(path)
		if !ok {
			complete = false
		}
		collector.AppendUniqueMCPServers(&out, seen, servers...)
	}
	slices.SortFunc(out, func(a, b trace.MCPServer) int {
		return cmp.Or(
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.ConfigPath, b.ConfigPath),
		)
	})
	return out, complete, nil
}

func codexConfigPaths(roots []SourceRoot) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Join(root.Path, "config.toml"))
	}
	return collector.UniqueStrings(paths)
}

// scanCodexConfigTOML returns the servers declared in a codex config.toml and
// whether the read was complete. A genuinely absent file is complete (no
// servers); an unreadable or corrupt/half-written one is not, so the caller can
// avoid reconciling stored rows away against it.
func scanCodexConfigTOML(path string) ([]trace.MCPServer, bool) {
	data, exists, ok := collector.ReadFileState(path)
	if !ok {
		return nil, false
	}
	if !exists {
		return nil, true
	}
	hash := collector.HashContent(data)

	var doc struct {
		MCPServers map[string]codexMCPServerConfig `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		// A corrupt/half-written config.toml otherwise looks identical to "no
		// servers configured". Treat it as an incomplete scan so a transient bad
		// read does not delete the user's stored servers; surface it too.
		slog.Warn("codex config.toml decode failed", "path", path, "err", err)
		return nil, false
	}

	names := slices.Sorted(maps.Keys(doc.MCPServers))

	out := make([]trace.MCPServer, 0, len(names))
	for _, name := range names {
		server := doc.MCPServers[name]
		out = append(out, trace.MCPServer{
			ID:         collector.MCPServerID("user", path, name),
			Name:       name,
			Scope:      "user",
			Transport:  collector.ClassifyMCPTransport(server.Type, server.URL, server.Command),
			ConfigPath: path,
			ConfigHash: hash,
			Enabled:    server.enabled(),
		})
	}
	return out, true
}

type codexMCPServerConfig struct {
	Type    string `toml:"type"`
	URL     string `toml:"url"`
	Command string `toml:"command"`
	Enabled *bool  `toml:"enabled"`
}

func (c codexMCPServerConfig) enabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

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

func scanDeclaredMCPServers(ctx context.Context, roots []SourceRoot) ([]trace.MCPServer, error) {
	paths := codexConfigPaths(roots)
	seen := make(map[string]struct{})
	out := make([]trace.MCPServer, 0, 8)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		collector.AppendUniqueMCPServers(&out, seen, scanCodexConfigTOML(path)...)
	}
	slices.SortFunc(out, func(a, b trace.MCPServer) int {
		return cmp.Or(
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.ConfigPath, b.ConfigPath),
		)
	})
	return out, nil
}

func codexConfigPaths(roots []SourceRoot) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Join(root.Path, "config.toml"))
	}
	return collector.UniqueStrings(paths)
}

func scanCodexConfigTOML(path string) []trace.MCPServer {
	data, ok := collector.ReadFileOK(path)
	if !ok {
		return nil
	}
	hash := collector.HashContent(data)

	var doc struct {
		MCPServers map[string]codexMCPServerConfig `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		// A corrupt/half-written config.toml otherwise looks identical to "no
		// servers configured". Surface it in diagnostics while still letting
		// ingest continue.
		slog.Warn("codex config.toml decode failed", "path", path, "err", err)
		return nil
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
	return out
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

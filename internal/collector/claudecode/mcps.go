package claudecode

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/trace"
)

func scanDeclaredMCPServers(ctx context.Context, opts scanOptions) ([]trace.MCPServer, error) {
	if len(opts.UserRoots) == 0 {
		opts.UserRoots = defaultClaudeUserRoots()
	}
	seen := make(map[string]struct{})
	out := make([]trace.MCPServer, 0, 16)

	// ~/.claude.json is the primary store for user-scoped mcpServers and the
	// project→mcpServers map. It does not move with CLAUDE_CONFIG_DIR, so scan it
	// regardless of how the roots were resolved (env/manual/default). ReadFileOK
	// makes this a no-op when the file is absent.
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".claude.json")
		collector.AppendUniqueMCPServers(&out, seen, scanClaudeUserJSON(path)...)
		opts.ProjectPaths = append(opts.ProjectPaths, declaredProjectPaths(path)...)
	}

	for _, project := range collector.UniqueStrings(opts.ProjectPaths) {
		if project == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// .mcp.json declares the project MCP servers; settings.json /
		// settings.local.json never carry an mcpServers key (only the
		// enable/disable flags below), so they are not scanned for servers.
		servers := scanMCPJSONShape(filepath.Join(project, ".mcp.json"), "project")
		enablement := loadProjectMCPEnablement(project)
		for i := range servers {
			servers[i].Enabled = enablement.enabled(servers[i].Name)
		}
		collector.AppendUniqueMCPServers(&out, seen, servers...)
	}

	slices.SortFunc(out, func(a, b trace.MCPServer) int {
		return cmp.Or(
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.ConfigPath, b.ConfigPath),
		)
	})
	return out, nil
}

func declaredProjectPaths(path string) []string {
	data, ok := collector.ReadFileOK(path)
	if !ok {
		return nil
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return slices.Collect(maps.Keys(doc.Projects))
}

func scanClaudeUserJSON(path string) []trace.MCPServer {
	data, ok := collector.ReadFileOK(path)
	if !ok {
		return nil
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	hash := collector.HashContent(data)
	out := make([]trace.MCPServer, 0, len(doc.MCPServers))
	for name, raw := range doc.MCPServers {
		out = append(out, makeMCPServer(name, "user", path, hash, raw))
	}
	for projectPath, block := range doc.Projects {
		for name, raw := range block.MCPServers {

			scoped := path + "#" + projectPath
			out = append(out, makeMCPServer(name, "project", scoped, hash, raw))
		}
	}
	return out
}

func scanMCPJSONShape(path, scope string) []trace.MCPServer {
	data, ok := collector.ReadFileOK(path)
	if !ok {
		return nil
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	hash := collector.HashContent(data)
	out := make([]trace.MCPServer, 0, len(doc.MCPServers))
	for name, raw := range doc.MCPServers {
		out = append(out, makeMCPServer(name, scope, path, hash, raw))
	}
	return out
}

// projectMCPEnablement captures the enable/disable state Claude Code applies to
// project .mcp.json servers via settings. settings.local.json overrides
// settings.json. disabledMcpjsonServers always wins over enable signals.
type projectMCPEnablement struct {
	configFound bool
	enableAll   bool
	enabledSet  map[string]struct{}
	disabledSet map[string]struct{}
}

func (e projectMCPEnablement) enabled(name string) bool {
	if _, ok := e.disabledSet[name]; ok {
		return false
	}
	if e.enableAll {
		return true
	}
	if _, ok := e.enabledSet[name]; ok {
		return true
	}
	// No applicable settings were found: preserve the prior assumption that a
	// declared .mcp.json server is enabled. Once settings selectively enable
	// servers, a server absent from the enable set is treated as not enabled.
	return !e.configFound
}

func loadProjectMCPEnablement(project string) projectMCPEnablement {
	out := projectMCPEnablement{
		enabledSet:  make(map[string]struct{}),
		disabledSet: make(map[string]struct{}),
	}
	// settings.json first, then settings.local.json so local overrides.
	for _, name := range []string{"settings.json", "settings.local.json"} {
		mergeMCPEnablement(&out, filepath.Join(project, ".claude", name))
	}
	return out
}

func mergeMCPEnablement(into *projectMCPEnablement, path string) {
	data, ok := collector.ReadFileOK(path)
	if !ok {
		return
	}
	var doc struct {
		EnableAll         *bool    `json:"enableAllProjectMcpServers"`
		EnabledMcpServers []string `json:"enabledMcpjsonServers"`
		DisabledServers   []string `json:"disabledMcpjsonServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	if doc.EnableAll == nil && len(doc.EnabledMcpServers) == 0 && len(doc.DisabledServers) == 0 {
		return
	}
	into.configFound = true
	if doc.EnableAll != nil {
		into.enableAll = *doc.EnableAll
	}
	for _, name := range doc.EnabledMcpServers {
		into.enabledSet[name] = struct{}{}
	}
	for _, name := range doc.DisabledServers {
		into.disabledSet[name] = struct{}{}
	}
}

func makeMCPServer(name, scope, configPath, configHash string, raw json.RawMessage) trace.MCPServer {
	transport := classifyMCPTransport(raw)
	return trace.MCPServer{
		ID:         collector.MCPServerID(scope, configPath, name),
		Name:       name,
		Scope:      scope,
		Transport:  transport,
		ConfigPath: configPath,
		ConfigHash: configHash,
		Enabled:    true,
	}
}

// classifyMCPTransport unmarshals the claude-code JSON server shape and delegates
// the transport decision to the shared collector.ClassifyMCPTransport.
func classifyMCPTransport(raw json.RawMessage) string {
	var probe struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "unknown"
	}
	return collector.ClassifyMCPTransport(probe.Type, probe.URL, probe.Command)
}

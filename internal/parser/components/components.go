package components

import (
	"regexp"
	"strings"
	"sync"

	"toktop.unceas.dev/internal/trace"
)

var skillPathRe = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile("skills/(?:[^\\s\"'`()\\\\]+?/)*?([A-Za-z0-9][A-Za-z0-9._-]+)/SKILL\\.md")
})

func FromTools(turnID string, tools []trace.ToolCall) []trace.TurnComponent {
	result := make([]trace.TurnComponent, 0, len(tools))
	seenSkills := make(map[string]struct{})
	seenMCPServers := make(map[string]struct{})
	seenBuiltins := make(map[string]struct{})
	for _, tool := range tools {
		switch tool.Kind {
		case trace.ToolKindMCP:
			if _, ok := seenMCPServers[tool.MCPServer]; !ok && tool.MCPServer != "" {
				seenMCPServers[tool.MCPServer] = struct{}{}
				result = append(result, trace.TurnComponent{
					TurnID:        turnID,
					ComponentKind: trace.ComponentKindMCPServer,
					ComponentName: tool.MCPServer,
					Relation:      trace.RelationInvoked,
					Confidence:    trace.ConfidenceObserved,
				})
			}
			if tool.MCPServer != "" && tool.MCPTool != "" {
				result = append(result, trace.TurnComponent{
					TurnID:        turnID,
					ComponentKind: trace.ComponentKindMCPTool,
					ComponentName: tool.MCPServer + "." + tool.MCPTool,
					Relation:      trace.RelationInvoked,
					Confidence:    trace.ConfidenceObserved,
				})
			}
		default:
			// Dedup builtin tools per turn like the MCP-server/skill cases above, so
			// N calls to the same builtin emit one component instead of N identical
			// rows in the per-turn component listing.
			if _, ok := seenBuiltins[tool.Name]; !ok {
				seenBuiltins[tool.Name] = struct{}{}
				result = append(result, trace.TurnComponent{
					TurnID:        turnID,
					ComponentKind: trace.ComponentKindBuiltinTool,
					ComponentName: tool.Name,
					Relation:      trace.RelationInvoked,
					Confidence:    trace.ConfidenceObserved,
				})
			}
		}
		for _, skill := range skillNamesFromText(tool.Input) {
			if _, ok := seenSkills[skill]; ok {
				continue
			}
			seenSkills[skill] = struct{}{}
			result = append(result, trace.TurnComponent{
				TurnID:        turnID,
				ComponentKind: trace.ComponentKindSkill,
				ComponentName: skill,
				Relation:      trace.RelationInferredUsed,
				Evidence:      "tool_input_mentions_skills_path",
				Confidence:    trace.ConfidenceInferred,
			})
		}
	}
	return result
}

func skillNamesFromText(value string) []string {
	if value == "" {
		return nil
	}
	matches := skillPathRe().FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		name := m[1]
		if !validSkillName(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func validSkillName(name string) bool {
	if len(name) < 2 {
		return false
	}
	switch name {
	case "skills", "SKILL", "SKILL.md":
		return false
	}
	return !strings.ContainsAny(name, " \t\n\r\"'`<>=()/\\")
}

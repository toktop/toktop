package cli

import (
	"fmt"
	"io"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

// Trace-index lookups and the human-readable rendering for `sessions inspect` /
// `turns inspect`. The list/query commands live in cli.go; this file is only the
// single-entity lookup + pretty-print layer.

func findTurn(index trace.Index, id string) (trace.Turn, bool) {
	for _, turn := range index.Turns {
		if turn.ID == id {
			return turn, true
		}
	}
	return trace.Turn{}, false
}

func findSession(index trace.Index, id string) (trace.Session, bool) {
	for _, session := range index.Sessions {
		if session.ID == id || session.ExternalID == id {
			return session, true
		}
	}
	return trace.Session{}, false
}

// sessionsWithExternalID returns the internal ids of every session whose external
// id equals externalID — used to warn when one provider UUID is ambiguous.
func sessionsWithExternalID(index trace.Index, externalID string) []string {
	var ids []string
	for _, session := range index.Sessions {
		if session.ExternalID == externalID {
			ids = append(ids, session.ID)
		}
	}
	return ids
}

func turnsForSession(index trace.Index, sessionID string) []trace.Turn {
	turns := make([]trace.Turn, 0)
	for _, turn := range index.Turns {
		if turn.SessionID == sessionID {
			turns = append(turns, turn)
		}
	}
	return turns
}

func printSessionDetail(stdout io.Writer, detail sessionDetail) {
	session := detail.Session
	externalID := session.ExternalID
	if externalID == "" {
		externalID = "-"
	}
	fmt.Fprintf(stdout, "Session %s\n", session.ID)
	fmt.Fprintf(stdout, "External ID: %s\n", externalID)
	fmt.Fprintf(stdout, "Provider: %s\n", session.Provider)
	fmt.Fprintf(stdout, "Project: %s\n", session.ProjectName)
	fmt.Fprintf(stdout, "Transcript: %s\n", session.TranscriptPath)
	fmt.Fprintf(stdout, "Turns: %d\n", len(detail.Turns))
	fmt.Fprintf(stdout, "Tool calls: %d\n", session.ToolCallCount)
	fmt.Fprintf(stdout, "Tokens: input %s  output %s  cache read %s  cache write %s\n",
		textutil.FormatCount(session.Tokens.Input), textutil.FormatCount(session.Tokens.Output),
		textutil.FormatCount(session.Tokens.CacheRead), textutil.FormatCacheWrite(session.Tokens.CacheWrite, session.Tokens.CacheWriteLong))

	for _, turn := range detail.Turns {
		fmt.Fprintf(stdout, "\nTurn %d  %s  status=%s\n", turn.Index, turn.ID, turn.Status)
		if turn.UserMessage != "" {
			fmt.Fprintf(stdout, "  User: %s\n", oneLine(turn.UserMessage, 120))
		}
		printTurnComponentSummary(stdout, turn)
	}
}

func printTurnComponentSummary(stdout io.Writer, turn trace.Turn) {
	builtinTools, mcpTools := splitToolCalls(turn.ToolCalls)
	skills := skillsForTurn(turn)

	fmt.Fprintln(stdout, "  Built-in tools:")
	if len(builtinTools) == 0 {
		fmt.Fprintln(stdout, "    none observed")
	} else {
		for _, tool := range builtinTools {
			fmt.Fprintf(stdout, "    %-8s %-20s %s\n", tool.Status, tool.Name, oneLine(tool.Input, 80))
		}
	}

	fmt.Fprintln(stdout, "  MCP tools:")
	if len(mcpTools) == 0 {
		fmt.Fprintln(stdout, "    none observed")
	} else {
		for _, tool := range mcpTools {
			fmt.Fprintf(stdout, "    %-8s %s/%s %s\n", tool.Status, tool.MCPServer, tool.MCPTool, oneLine(tool.Input, 80))
		}
	}

	fmt.Fprintln(stdout, "  Skills:")
	if len(skills) == 0 {
		fmt.Fprintln(stdout, "    none observed from transcript")
		return
	}
	for _, skill := range skills {
		fmt.Fprintf(stdout, "    %-12s %s confidence=%s\n", skill.Relation, skill.ComponentName, skill.Confidence)
	}
}

func printComponentDetails(stdout io.Writer, turn trace.Turn) {
	builtinTools, mcpTools := splitToolCalls(turn.ToolCalls)

	fmt.Fprintln(stdout, "\nBuilt-in tools")
	if len(builtinTools) == 0 {
		fmt.Fprintln(stdout, "  none observed")
	} else {
		for _, tool := range builtinTools {
			printToolCall(stdout, tool)
		}
	}

	fmt.Fprintln(stdout, "\nMCP tools")
	if len(mcpTools) == 0 {
		fmt.Fprintln(stdout, "  none observed")
	} else {
		for _, tool := range mcpTools {
			fmt.Fprintf(stdout, "  %-8s server=%-16s tool=%-20s %s\n", tool.Status, tool.MCPServer, tool.MCPTool, oneLine(tool.Input, 80))
			if tool.Output != "" {
				fmt.Fprintf(stdout, "           output: %s\n", oneLine(tool.Output, 100))
			}
		}
	}

	fmt.Fprintln(stdout, "\nSkills")
	skills := skillsForTurn(turn)
	if len(skills) == 0 {
		fmt.Fprintln(stdout, "  none observed from transcript")
		return
	}
	for _, skill := range skills {
		fmt.Fprintf(stdout, "  %-12s %s confidence=%s\n", skill.Relation, skill.ComponentName, skill.Confidence)
	}
}

func splitToolCalls(tools []trace.ToolCall) ([]trace.ToolCall, []trace.ToolCall) {
	builtin := make([]trace.ToolCall, 0, len(tools))
	mcp := make([]trace.ToolCall, 0)
	for _, tool := range tools {
		if tool.Kind == trace.ToolKindMCP {
			mcp = append(mcp, tool)
			continue
		}
		builtin = append(builtin, tool)
	}
	return builtin, mcp
}

func skillsForTurn(turn trace.Turn) []trace.TurnComponent {
	skills := make([]trace.TurnComponent, 0)
	for _, component := range turn.Components {
		if component.ComponentKind == trace.ComponentKindSkill {
			skills = append(skills, component)
		}
	}
	return skills
}

func printToolCall(stdout io.Writer, tool trace.ToolCall) {
	fmt.Fprintf(stdout, "  %-8s %-20s %s\n", tool.Status, tool.Name, oneLine(tool.Input, 80))
	if tool.Output != "" {
		fmt.Fprintf(stdout, "           output: %s\n", oneLine(tool.Output, 100))
	}
}

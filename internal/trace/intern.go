package trace

import "unique"

func InternString(value string) string {
	if value == "" {
		return ""
	}
	return unique.Make(value).Value()
}

func InternIndexStrings(index *Index) {
	index.Source = InternString(index.Source)
	for i := range index.Sessions {
		internSession(&index.Sessions[i])
	}
	for i := range index.Turns {
		internTurn(&index.Turns[i])
	}
	for i := range index.Invocations {
		internInvocation(&index.Invocations[i])
	}
	for i := range index.SubagentRuns {
		internSubagentRun(&index.SubagentRuns[i])
	}
	for i := range index.ToolOutputs {
		index.ToolOutputs[i].SourceFile = InternString(index.ToolOutputs[i].SourceFile)
		index.ToolOutputs[i].RetentionClass = InternString(index.ToolOutputs[i].RetentionClass)
	}
	for i := range index.ContextEvents {
		internContextEvent(&index.ContextEvents[i])
	}
	for i := range index.TurnComponents {
		internTurnComponent(&index.TurnComponents[i])
	}
	for i := range index.Skills {
		internSkill(&index.Skills[i])
	}
	for i := range index.MCPServers {
		internMCPServer(&index.MCPServers[i])
	}
	for i := range index.ParseErrorList {
		index.ParseErrorList[i].SourceID = InternString(index.ParseErrorList[i].SourceID)
		index.ParseErrorList[i].SourceRootID = InternString(index.ParseErrorList[i].SourceRootID)
		index.ParseErrorList[i].ParserVersion = InternString(index.ParseErrorList[i].ParserVersion)
	}
}

func internSession(session *Session) {
	session.Provider = InternString(session.Provider)
	session.ProjectName = InternString(session.ProjectName)
	session.Status = InternString(session.Status)
}

func internTurn(turn *Turn) {
	turn.Provider = InternString(turn.Provider)
	turn.ProjectName = InternString(turn.ProjectName)
	turn.Status = InternString(turn.Status)
	turn.FailureReason = InternString(turn.FailureReason)
	for i := range turn.ToolCalls {
		internToolCall(&turn.ToolCalls[i])
	}
	for i := range turn.Invocations {
		internInvocation(&turn.Invocations[i])
	}
	for i := range turn.SubagentRuns {
		internSubagentRun(&turn.SubagentRuns[i])
	}
	for i := range turn.Components {
		internTurnComponent(&turn.Components[i])
	}
	for i := range turn.ContextEvents {
		internContextEvent(&turn.ContextEvents[i])
	}
}

func internInvocation(invocation *Invocation) {
	invocation.Provider = InternString(invocation.Provider)
	invocation.Model = InternString(invocation.Model)
	invocation.StopReason = InternString(invocation.StopReason)
	invocation.Status = InternString(invocation.Status)
}

func internToolCall(call *ToolCall) {
	call.Kind = InternString(call.Kind)
	call.Name = InternString(call.Name)
	call.MCPServer = InternString(call.MCPServer)
	call.MCPTool = InternString(call.MCPTool)
	call.Status = InternString(call.Status)
}

func internSubagentRun(run *SubagentRun) {
	run.AgentName = InternString(run.AgentName)
	run.AgentType = InternString(run.AgentType)
	run.Model = InternString(run.Model)
	run.Status = InternString(run.Status)
	for i := range run.Invocations {
		internInvocation(&run.Invocations[i])
	}
	for i := range run.ToolCalls {
		internToolCall(&run.ToolCalls[i])
	}
}

func internContextEvent(event *ContextEvent) {
	event.ComponentType = InternString(event.ComponentType)
	event.ComponentName = InternString(event.ComponentName)
	event.Phase = InternString(event.Phase)
	event.Confidence = Confidence(InternString(string(event.Confidence)))
}

func internTurnComponent(component *TurnComponent) {
	component.ComponentKind = InternString(component.ComponentKind)
	component.ComponentName = InternString(component.ComponentName)
	component.Relation = InternString(component.Relation)
	component.Confidence = Confidence(InternString(string(component.Confidence)))
}

func internSkill(skill *Skill) {
	skill.Name = InternString(skill.Name)
	skill.Scope = InternString(skill.Scope)
	skill.Version = InternString(skill.Version)
	skill.Compatibility = InternString(skill.Compatibility)
	skill.License = InternString(skill.License)
}

func internMCPServer(server *MCPServer) {
	server.Name = InternString(server.Name)
	server.Scope = InternString(server.Scope)
	server.Transport = InternString(server.Transport)
}

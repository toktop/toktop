package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

func CollectSessionFile(ctx context.Context, file SessionFile) (source.RawSession, []trace.ParseError, error) {
	handle, err := os.Open(file.Path)
	if err != nil {
		return source.RawSession{}, nil, fmt.Errorf("open transcript: %w", err)
	}
	defer handle.Close()

	raw := source.RawSession{
		Provider:    "claude-code",
		SourceRoot:  file.Root.Path,
		SourceFile:  file.Path,
		ProjectName: file.ProjectName,
		ProjectPath: file.ProjectPath,
	}
	if file.SubagentKind != "" {
		raw.IsSubagent = true
		raw.SubagentKind = file.SubagentKind
		raw.WorkflowRunID = file.WorkflowRunID
		// Parent link comes straight from the path (the <uuid> dir before subagents/),
		// so it survives even a transcript with no in-file sessionId.
		raw.ParentExternalID = file.ParentExternalID
		// The sibling agent-<id>.meta.json carries {agentType, description, toolUseId}.
		// toolUseId links a Task/Agent run back to its launching tool call (workflow
		// agents have only agentType, so ParentToolUseID stays empty for them).
		raw.AgentType, raw.ParentToolUseID = readSubagentMeta(file.Path)
	}
	var parseErrors []trace.ParseError
	// Authoritative working directory recorded in the transcript. We prefer this
	// over the lossy hyphen→slash decode of the project directory name (see
	// decodeProjectName), which collapses literal hyphens in real path segments
	// and can even synthesize arbitrary absolute paths. The first non-empty cwd wins.
	var transcriptCwd string
	sourceID := trace.SourceID(raw.Provider)
	sourceRootID := trace.SourceRootID(sourceID, raw.SourceRoot)
	if err := collector.ReadJSONLLines(ctx, handle, func(lineNo int, byteOffset int64, line []byte) error {
		event := source.RawEvent{
			Provider:   raw.Provider,
			SourceRoot: raw.SourceRoot,
			SourceFile: raw.SourceFile,
			LineNo:     lineNo,
			ByteOffset: byteOffset,
			RawJSON:    json.RawMessage(line),
			RawHash:    trace.HashPayload(line),
		}
		var env struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Timestamp string `json:"timestamp"`
			Cwd       string `json:"cwd"`
		}
		if jsonErr := json.Unmarshal(line, &env); jsonErr != nil {
			parseErrors = append(parseErrors, trace.ParseError{
				SourceID:     sourceID,
				SourceRootID: sourceRootID,
				SourceFile:   raw.SourceFile,
				LineNo:       lineNo,
				Message:      jsonErr.Error(),
			})
		} else {
			event.EventType = trace.InternString(env.Type)
			event.SessionID = trace.InternString(env.SessionID)
			event.EventTime = trace.ParseEventTime(env.Timestamp)
			if transcriptCwd == "" && env.Cwd != "" {
				transcriptCwd = env.Cwd
			}
		}
		raw.RawEventList = append(raw.RawEventList, event)
		return nil
	}); err != nil {
		return source.RawSession{}, parseErrors, fmt.Errorf("read transcript: %w", err)
	}
	if transcriptCwd != "" {
		raw.ProjectPath = transcriptCwd
	}
	return raw, parseErrors, nil
}

// readSubagentMeta best-effort reads the agent-<id>.meta.json sibling of a
// subagent transcript and returns its agentType and toolUseId. A missing or
// malformed meta file is not fatal — the subagent transcript still ingests with
// empty linkage, so both return "".
func readSubagentMeta(transcriptPath string) (agentType, toolUseID string) {
	metaPath := strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
	data, ok := collector.ReadFileOK(metaPath)
	if !ok {
		return "", ""
	}
	var meta struct {
		AgentType string `json:"agentType"`
		ToolUseID string `json:"toolUseId"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return "", ""
	}
	return meta.AgentType, meta.ToolUseID
}

package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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

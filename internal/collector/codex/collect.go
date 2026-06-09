package codex

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
		return source.RawSession{}, nil, fmt.Errorf("open codex session: %w", err)
	}
	defer handle.Close()

	raw := source.RawSession{
		Provider:   "codex",
		SourceRoot: file.Root.Path,
		SourceFile: file.Path,
	}
	var parseErrors []trace.ParseError
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
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
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
			event.EventTime = trace.ParseEventTime(env.Timestamp)
		}
		raw.RawEventList = append(raw.RawEventList, event)
		return nil
	}); err != nil {
		return source.RawSession{}, parseErrors, fmt.Errorf("read codex session: %w", err)
	}
	return raw, parseErrors, nil
}

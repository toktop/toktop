package source

import (
	"encoding/json"
	"time"

	"toktop.unceas.dev/internal/trace"
)

type RawEvent struct {
	Provider   string `json:"provider"`
	SourceRoot string `json:"source_root"`
	SourceFile string `json:"source_file"`
	LineNo     int    `json:"line_no"`

	ByteOffset int64           `json:"byte_offset"`
	EventType  string          `json:"event_type,omitempty"`
	EventTime  time.Time       `json:"event_time"`
	SessionID  string          `json:"session_id,omitempty"`
	RawJSON    json.RawMessage `json:"raw_json"`

	RawHash string `json:"raw_hash,omitzero"`
}

// Hash is the content-addressing hash of the raw event: the precomputed RawHash
// when present, else a hash of RawJSON. It is the single definition used at
// parse time and at store time, so raw-event IDs stay consistent across the
// pipeline (previously this was hand-copied into both parsers and the store).
func (e RawEvent) Hash() string {
	if e.RawHash != "" {
		return e.RawHash
	}
	return trace.HashPayload(e.RawJSON)
}

type RawSession struct {
	Provider     string     `json:"provider"`
	SourceRoot   string     `json:"source_root"`
	SourceFile   string     `json:"source_file"`
	ProjectName  string     `json:"project_name,omitempty"`
	ProjectPath  string     `json:"project_path,omitempty"`
	RawEventList []RawEvent `json:"raw_events"`

	// Subagent marker, populated by the collector when SourceFile is a nested
	// subagent transcript (empty otherwise). ParentExternalID is the parent
	// session's external id when the collector can derive it without parsing
	// events (claude-code reads it from the subagent's path); the parser may also
	// fill it from the in-file sessionId. The store resolves it to an internal id.
	IsSubagent       bool   `json:"is_subagent,omitempty"`
	ParentExternalID string `json:"parent_external_id,omitempty"`
	ParentToolUseID  string `json:"parent_tool_use_id,omitempty"`
	WorkflowRunID    string `json:"workflow_run_id,omitempty"`
	SubagentKind     string `json:"subagent_kind,omitempty"`
	AgentType        string `json:"agent_type,omitempty"`
}

type Fingerprint struct {
	Size    int64 `json:"size"`
	MtimeNS int64 `json:"mtime_ns"`
	Ino     int64 `json:"inode_no"`
}

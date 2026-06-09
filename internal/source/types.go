package source

import (
	"encoding/json"
	"iter"
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
}

type Fingerprint struct {
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
	Ino     uint64 `json:"inode_no"`
}

func (r RawSession) Events() iter.Seq2[RawEvent, error] {
	return func(yield func(RawEvent, error) bool) {
		for _, event := range r.RawEventList {
			if !yield(event, nil) {
				return
			}
		}
	}
}

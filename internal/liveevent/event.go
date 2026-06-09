package liveevent

import (
	"context"
	"time"
)

type Event struct {
	EventID string `json:"event_id,omitzero"`
	Type    string `json:"type"`
	// RawEventName is the un-normalized provider hook event name (e.g.
	// "StopFailure"), preserved so normalizeLiveEvent can ask the provider to map
	// it to a status. Empty for non-hook events and events replayed from before
	// this field existed (they fall back to the Type heuristic).
	RawEventName      string    `json:"raw_event_name,omitzero"`
	At                time.Time `json:"at,omitzero"`
	Provider          string    `json:"provider,omitzero"`
	SourceID          string    `json:"source_id,omitzero"`
	SessionID         string    `json:"session_id,omitzero"`
	ExternalSessionID string    `json:"external_session_id,omitzero"`
	ProjectID         string    `json:"project_id,omitzero"`
	ProjectName       string    `json:"project_name,omitzero"`
	ProjectPath       string    `json:"project_path,omitzero"`
	TranscriptPath    string    `json:"transcript_path,omitzero"`
	Status            string    `json:"status,omitzero"`
	Reason            string    `json:"reason,omitzero"`
	File              string    `json:"file,omitzero"`
	TurnCount         int       `json:"turn_count,omitzero"`
	RawEventCount     int       `json:"raw_event_count,omitzero"`
	SizeBytes         int       `json:"size_bytes,omitzero"`
}

type Emitter interface {
	Emit(ctx context.Context, ev Event) (Event, error)
}

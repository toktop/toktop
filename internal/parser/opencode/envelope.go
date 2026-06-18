package opencode

import "encoding/json"

// The wire envelope between collector/opencode and parser/opencode. opencode
// stores sessions in a SQLite DB, not a JSONL transcript, so the collector
// synthesizes one source.RawEvent per DB row: RawEvent.EventType is the
// discriminator (one of the Kind* constants) and RawEvent.RawJSON is the matching
// envelope below. These types are the single definition of that contract, shared
// by both packages (the collector marshals them, the parser unmarshals them).

const (
	// KindSession is the leading event carrying the session row, so the parser
	// sets Session fields without a DB handle. RawJSON = SessionEnvelope.
	KindSession = "session"
	// KindUser / KindAssistant carry one message row. RawJSON = MessageEnvelope.
	// These two double as raw_events.role (the store maps user/assistant).
	KindUser      = "user"
	KindAssistant = "assistant"
	// Part kinds carry one part row. RawJSON = PartEnvelope. The value is the
	// opencode part.data.type ("tool","text","reasoning","step-start",
	// "step-finish","file").
	KindTool       = "tool"
	KindText       = "text"
	KindReasoning  = "reasoning"
	KindStepStart  = "step-start"
	KindStepFinish = "step-finish"
	KindFile       = "file"
)

// SessionEnvelope mirrors the opencode session table columns the parser needs.
// time_* are epoch milliseconds; tokens_* are session-level aggregates.
type SessionEnvelope struct {
	ID              string  `json:"id"`
	ParentID        string  `json:"parent_id,omitempty"`
	Agent           string  `json:"agent,omitempty"`
	Title           string  `json:"title,omitempty"`
	Directory       string  `json:"directory,omitempty"`
	ProjectID       string  `json:"project_id,omitempty"`
	Model           string  `json:"model,omitempty"` // JSON blob {id,providerID,variant}
	TokensInput     int     `json:"tokens_input,omitempty"`
	TokensOutput    int     `json:"tokens_output,omitempty"`
	TokensReasoning int     `json:"tokens_reasoning,omitempty"`
	TokensCacheRead int     `json:"tokens_cache_read,omitempty"`
	TokensCacheWrt  int     `json:"tokens_cache_write,omitempty"`
	Cost            float64 `json:"cost,omitempty"`
	TimeCreated     int64   `json:"time_created,omitempty"`
	TimeUpdated     int64   `json:"time_updated,omitempty"`
}

// MessageEnvelope wraps a message row: opencode's message.data JSON plus the row
// id (message.data does not carry its own id).
type MessageEnvelope struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// PartEnvelope wraps a part row: opencode's part.data JSON plus the row id and its
// owning message id (part.data carries neither).
type PartEnvelope struct {
	ID        string          `json:"id"`
	MessageID string          `json:"message_id"`
	Data      json.RawMessage `json:"data"`
}

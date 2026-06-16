package eventlog

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"
)

const fileName = "toktop-events.bbolt"

const DefaultReplayBatchSize = 512

type Event struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload"`
}

type Store interface {
	// AppendWithID stores an event at a caller-assigned monotonic id. The id is
	// owned by the server's in-memory sequence (seeded from LastID at startup),
	// so Emit can assign it and fan out to SSE subscribers before this durable
	// write completes — keeping the bbolt fsync off the hook→SSE hot path.
	AppendWithID(ctx context.Context, id uint64, eventType string, at time.Time, payload []byte) error
	LastID(ctx context.Context) (uint64, error)
	// MinID returns the smallest surviving event ID (0 when the log is empty).
	// IDs come from a gapless sequence and Prune only deletes the oldest prefix,
	// so MinID is the floor below which a reconnecting client's resume point has
	// been pruned and an incremental replay is impossible.
	MinID(ctx context.Context) (uint64, error)
	ReplayRange(ctx context.Context, after, until uint64, limit int) ([]Event, error)
	Prune(ctx context.Context, before time.Time, keepN int) (int, error)
	// SaveLiveSnapshot atomically replaces the live-session snapshot: a generic
	// key→blob set plus a watermark (the event id up to which the snapshot is
	// current). The store does not interpret the keys or blobs — the caller owns
	// their meaning (session key → serialized live state). Written on clean
	// shutdown so a restart can seed live state in O(entries) instead of rescanning
	// the recent event window.
	SaveLiveSnapshot(ctx context.Context, watermark uint64, entries map[string][]byte) error
	// LoadLiveSnapshot returns the saved snapshot. entries is nil when no snapshot
	// exists (first run, or only a crash since the last clean shutdown), signalling
	// the caller to fall back to a full recent-window replay.
	LoadLiveSnapshot(ctx context.Context) (watermark uint64, entries map[string][]byte, err error)
	Close() error
}

func DBPath(dataDir string) string {
	return filepath.Join(dataDir, fileName)
}

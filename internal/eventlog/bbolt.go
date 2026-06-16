package eventlog

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
	bolterr "go.etcd.io/bbolt/errors"
)

var eventsBucket = []byte("events")

// liveSnapshotBucket holds the clean-shutdown live-session snapshot (key→blob);
// liveSnapshotMetaBucket holds its watermark under watermarkKey.
var (
	liveSnapshotBucket     = []byte("live_snapshot")
	liveSnapshotMetaBucket = []byte("live_snapshot_meta")
	watermarkKey           = []byte("watermark")
)

type BoltStore struct {
	db *bolt.DB
}

type storedEvent struct {
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload"`
}

func Open(ctx context.Context, dataDir string) (*BoltStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create event log dir: %w", err)
	}
	db, err := bolt.Open(DBPath(dataDir), 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}

	db.MaxBatchDelay = 1 * time.Millisecond
	store := &BoltStore{db: db}
	if err := store.ensureBuckets(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *BoltStore) ensureBuckets(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(eventsBucket)
		return err
	})
}

func (s *BoltStore) AppendWithID(ctx context.Context, id uint64, eventType string, at time.Time, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if eventType == "" {
		return errors.New("event type is required")
	}
	if id == 0 {
		return errors.New("event id must be non-zero")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	payloadCopy := bytes.Clone(payload)
	record := storedEvent{
		Type:    eventType,
		At:      at.UTC(),
		Payload: json.RawMessage(payloadCopy),
	}
	value, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if err := s.db.Batch(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(eventsBucket)
		if bucket == nil {
			return errors.New("events bucket missing")
		}
		// The caller owns the monotonic sequence (an in-memory atomic counter
		// seeded from LastID at startup). Keep the bucket sequence in step so any
		// future NextSequence-based reader stays monotonic; ids only ever grow.
		if seq := bucket.Sequence(); id > seq {
			_ = bucket.SetSequence(id)
		}
		return bucket.Put(eventKey(id), value)
	}); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func (s *BoltStore) LastID(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var id uint64
	if err := s.db.View(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(eventsBucket)
		if bucket == nil {
			return nil
		}
		key, _ := bucket.Cursor().Last()
		if len(key) == 0 {
			return nil
		}
		id = eventID(key)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("read last event id: %w", err)
	}
	return id, nil
}

func (s *BoltStore) MinID(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var id uint64
	if err := s.db.View(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(eventsBucket)
		if bucket == nil {
			return nil
		}
		key, _ := bucket.Cursor().First()
		if len(key) == 0 {
			return nil
		}
		id = eventID(key)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("read min event id: %w", err)
	}
	return id, nil
}

func (s *BoltStore) ReplayRange(ctx context.Context, after, until uint64, limit int) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if after == math.MaxUint64 {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultReplayBatchSize
	}
	out := make([]Event, 0, limit)
	start := eventKey(after + 1)
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(eventsBucket)
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, value := cursor.Seek(start); key != nil; key, value = cursor.Next() {
			id := eventID(key)
			if until > 0 && id > until {
				return nil
			}
			var record storedEvent
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("unmarshal event %d: %w", id, err)
			}
			out = append(out, Event{
				ID:      id,
				Type:    record.Type,
				At:      record.At,
				Payload: append(json.RawMessage(nil), record.Payload...),
			})
			if len(out) >= limit {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *BoltStore) Prune(ctx context.Context, before time.Time, keepN int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	beforeIsSet := !before.IsZero()
	if !beforeIsSet && keepN <= 0 {
		return 0, nil
	}
	var deleted int
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(eventsBucket)
		if bucket == nil {
			return nil
		}

		// minKeepID is an unconditional retention floor: events with
		// id >= minKeepID are kept regardless of age. Derive it from the
		// keepN-th newest event that actually still exists, walking the
		// cursor backwards so holes left by earlier prunes do not undercount
		// the live window. If fewer than keepN events exist, the floor covers
		// the entire log and nothing may be pruned.
		var (
			minKeepID    uint64
			keepFloorSet bool
			keepAll      bool
		)
		if keepN > 0 {
			cursor := bucket.Cursor()
			n := 0
			for key, _ := cursor.Last(); key != nil; key, _ = cursor.Prev() {
				n++
				if n == keepN {
					minKeepID = eventID(key)
					keepFloorSet = true
					break
				}
			}
			if !keepFloorSet {
				keepAll = true
			}
		}
		if keepAll {
			return nil
		}
		// Prune only the oldest CONTIGUOUS prefix so the gapless-sequence invariant
		// holds: MinID stays the floor and the reconnect gap check (which compares
		// only the oldest id) can never miss an interior hole. The cursor runs
		// ascending; stop at the first event that must be kept rather than scanning
		// on, so a backdated/out-of-order At cannot carve a hole out of the middle.
		var victims [][]byte
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			id := eventID(key)
			if keepFloorSet && id >= minKeepID {
				break // reached the keepN floor: keep this id and every later one
			}
			if beforeIsSet {
				var record storedEvent
				if err := json.Unmarshal(value, &record); err != nil {
					return fmt.Errorf("unmarshal event %d for prune: %w", id, err)
				}
				if !record.At.Before(before) {
					break // first in-retention event: keep it and every later one
				}
			}
			victims = append(victims, bytes.Clone(key))
		}
		for _, k := range victims {
			if err := bucket.Delete(k); err != nil {
				return fmt.Errorf("delete event %d: %w", eventID(k), err)
			}
			deleted++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	return deleted, nil
}

func (s *BoltStore) SaveLiveSnapshot(ctx context.Context, watermark uint64, entries map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Replace the entry bucket wholesale so a shrunk live set (sessions evicted
		// by retention since the last snapshot) never leaves stale keys behind.
		if err := tx.DeleteBucket(liveSnapshotBucket); err != nil && !errors.Is(err, bolterr.ErrBucketNotFound) {
			return fmt.Errorf("reset live snapshot bucket: %w", err)
		}
		bucket, err := tx.CreateBucket(liveSnapshotBucket)
		if err != nil {
			return fmt.Errorf("create live snapshot bucket: %w", err)
		}
		for key, value := range entries {
			if err := bucket.Put([]byte(key), value); err != nil {
				return fmt.Errorf("write live snapshot entry: %w", err)
			}
		}
		meta, err := tx.CreateBucketIfNotExists(liveSnapshotMetaBucket)
		if err != nil {
			return fmt.Errorf("create live snapshot meta bucket: %w", err)
		}
		return meta.Put(watermarkKey, eventKey(watermark))
	})
}

func (s *BoltStore) LoadLiveSnapshot(ctx context.Context) (uint64, map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	var (
		watermark uint64
		entries   map[string][]byte
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		meta := tx.Bucket(liveSnapshotMetaBucket)
		if meta == nil {
			return nil // no snapshot ever saved
		}
		wm := meta.Get(watermarkKey)
		if wm == nil {
			return nil
		}
		watermark = eventID(wm)
		bucket := tx.Bucket(liveSnapshotBucket)
		if bucket == nil {
			// Meta watermark present but the entry bucket is gone (partial state /
			// corruption): leave entries nil so the caller treats it as "no snapshot"
			// and falls back to the recent-window scan, per the documented contract —
			// never a non-nil empty map that would adopt this watermark as the floor.
			return nil
		}
		entries = make(map[string][]byte, bucket.Stats().KeyN)
		return bucket.ForEach(func(k, v []byte) error {
			entries[string(k)] = bytes.Clone(v)
			return nil
		})
	})
	if err != nil {
		return 0, nil, fmt.Errorf("load live snapshot: %w", err)
	}
	return watermark, entries, nil
}

func (s *BoltStore) Close() error {
	return s.db.Close()
}

func eventKey(id uint64) []byte {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], id)
	return key[:]
}

func eventID(key []byte) uint64 {
	if len(key) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(key[:8])
}

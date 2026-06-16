package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"toktop.unceas.dev/internal/eventlog"
	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

const sseSubscriberBuffer = 256

// maxSSESubscribers caps concurrent /v1/stream connections so a reconnect storm
// or a misconfigured client can't exhaust goroutines/FDs. Past it, handleStream
// returns 503 before writing SSE headers.
const maxSSESubscribers = 1024

const sseKeepAliveInterval = 15 * time.Second

// sseWriteTimeout bounds each blocking socket write to a subscriber. The server
// deliberately runs without a global WriteTimeout (long-lived SSE), so a stalled
// client whose TCP send buffer is full would otherwise block its handler
// goroutine forever in writeSSE, leaking the goroutine + connection + FD. Per
// write deadline makes such a write fail so the handler returns and the deferred
// unsubscribeEvents fires.
const sseWriteTimeout = 10 * time.Second

// sseSubscriber is one live SSE connection. overflowed is set (and the channel
// closed) by publishEvent when the 256-slot buffer fills, so the handler can
// tell an overflow drop apart from a normal close and emit a resync_required
// frame. It is read by the handler only after observing the channel closed,
// which the Go memory model orders after the publisher's Store — atomic.Bool
// keeps that explicit and race-free.
type sseSubscriber struct {
	ch         chan event
	overflowed atomic.Bool
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	watchTargets, err := parseWatchTargets(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_watch", err.Error())
		return
	}

	statusOnly := isTruthy(r.URL.Query().Get("status_only"))
	lastEventID, lastIDProvided := reconnectEventID(r)
	lastID, validLastID := parseEventID(lastEventID)
	if lastIDProvided && !validLastID {
		writeError(w, http.StatusBadRequest, "invalid_last_event_id", "Last-Event-ID must be an unsigned integer")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unavailable", "streaming not supported")
		return
	}

	// Register (and enforce the subscriber cap) BEFORE writing the 200/SSE
	// headers, so a rejection can still set a 503 status.
	sub := &sseSubscriber{ch: make(chan event, sseSubscriberBuffer)}
	watermark, admitted := s.subscribeEvents(sub)
	if !admitted {
		writeError(w, http.StatusServiceUnavailable, "too_many_subscribers", "live stream subscriber limit reached; retry shortly")
		return
	}
	defer s.unsubscribeEvents(sub)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Bound every blocking socket write with a per-write deadline so a stalled
	// client cannot pin this handler goroutine forever (see sseWriteTimeout).
	rc := http.NewResponseController(w)
	writeFrame := func(ev event) error {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		return writeSSE(w, flusher, ev)
	}

	if err := writeFrame(event{Type: "hello", Data: map[string]any{"now": time.Now().UTC(), "last_event_id": eventIDString(watermark)}}); err != nil {
		return
	}
	// Treat an explicit Last-Event-ID of 0 as "do not resume": event IDs start
	// at 1, so 0 can never name a delivered event and would otherwise force a
	// full replay of the entire retained log on every such reconnect.
	if lastIDProvided && lastID > 0 && lastID < watermark {
		if err := s.writeReplayEvents(r.Context(), writeFrame, lastID, watermark, watchTargets, statusOnly); err != nil {
			gap, isGap := errors.AsType[*replayGapError](err)
			switch {
			case isGap:
				// The resume point was pruned: events (lastID, oldest) are gone.
				// Don't stream a silent hole — tell the client to resync from the
				// oldest still-recoverable id, then keep tailing live events.
				data := map[string]any{
					"reason":          "gap_in_event_log",
					"oldest_event_id": eventIDString(gap.oldest),
				}
				if gap.missing != 0 {
					data["missing_event_id"] = eventIDString(gap.missing)
				}
				if gap.reason != "" {
					data["gap_reason"] = gap.reason
				}
				_ = writeFrame(event{Type: "resync_required", Data: data})
			default:
				// The replay failure is almost always the same broken socket, so a
				// best-effort replay.error frame would just fail again; only attempt
				// it when the request context is still live.
				if r.Context().Err() == nil {
					_ = writeFrame(event{Type: "replay.error", Data: map[string]any{"message": err.Error()}})
				}
				return
			}
		}
	}
	ticker := time.NewTicker(sseKeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.ch:
			if !ok {
				// Closed by publishEvent because this subscriber overflowed
				// (slow consumer). Tell the client to do a full /v1/status
				// resync: incremental Last-Event-ID recovery is unreliable once
				// events were dropped and may already be GC'd from the log.
				// Best-effort — the socket that made us slow may be dead.
				if sub.overflowed.Load() {
					oldest, _ := s.eventStore.MinID(r.Context())
					_ = writeFrame(event{Type: "resync_required", Data: map[string]any{
						"reason":          "slow_consumer_overflow",
						"oldest_event_id": eventIDString(oldest),
					}})
				}
				return
			}
			if !eventMatchesWatchTargets(ev, watchTargets) {
				continue
			}
			if statusOnly && !eventHasStatus(ev) {
				continue
			}
			if err := writeFrame(ev); err != nil {
				return
			}
		case <-ticker.C:
			if err := writeFrame(event{Type: "ping", Data: time.Now().UTC()}); err != nil {
				return
			}
		}
	}
}

func (s *Server) publishEvent(ev event) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if id, ok := parseEventID(ev.ID); ok && id > s.lastPublishedID {
		s.lastPublishedID = id
	}
	// Fan out while holding eventsMu. Subscriber channels are only ever closed
	// under this same lock (unsubscribeEvents / Server.Close), so a channel can
	// never be closed between the select's readiness check and the send — that
	// race panics with "send on closed channel". Sends are non-blocking (the
	// default branch), so holding the lock during fan-out never stalls. A slow
	// subscriber whose 256-slot buffer is full is dropped and closed in place;
	// deleting the current key mid-range is safe in Go.
	for sub := range s.events {
		select {
		case sub.ch <- ev:
		default:
			// Buffer full: drop the slow subscriber. It cannot be notified
			// in-band (the channel is full), so mark it overflowed before
			// closing — the handler then emits a resync_required frame so the
			// client does a full /v1/status resync instead of trusting an
			// incremental Last-Event-ID recovery across a gap that may already
			// be GC'd from the event log.
			sub.overflowed.Store(true)
			delete(s.events, sub)
			close(sub.ch)
			s.sseSlowSubscriberDropped.Add(1)
			s.logger.Warn("dropped slow SSE subscriber: buffer full",
				"event_type", ev.Type, "event_id", ev.ID)
		}
	}
}

// subscribeEvents registers sub and returns the current watermark, or
// (0, false) when the subscriber cap is reached. The cap is enforced under
// eventsMu so the count is exact.
func (s *Server) subscribeEvents(sub *sseSubscriber) (uint64, bool) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if len(s.events) >= maxSSESubscribers {
		return 0, false
	}
	s.events[sub] = struct{}{}
	return s.lastPublishedID, true
}

// subscriberCount reports the number of live SSE subscribers, for GC logging.
func (s *Server) subscriberCount() int {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return len(s.events)
}

func (s *Server) unsubscribeEvents(sub *sseSubscriber) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if _, ok := s.events[sub]; ok {
		delete(s.events, sub)
		close(sub.ch)
	}
}

// replayGapError signals that the retained event log cannot prove a contiguous
// replay for the requested range. handleStream turns it into resync_required.
type replayGapError struct {
	oldest  uint64
	missing uint64
	reason  string
}

func (e *replayGapError) Error() string {
	return fmt.Sprintf("event-log replay gap (oldest=%d missing=%d reason=%s)", e.oldest, e.missing, e.reason)
}

// waitDurable blocks (bounded, ctx-aware) until the async persister has written
// the event log up to id `until`. Reconnect replay reads from that log, so this
// keeps the replay watermark backed by durable storage.
func (s *Server) waitDurable(ctx context.Context, until uint64) bool {
	if until == 0 {
		return true
	}
	for i := 0; i < 40 && s.durableID.Load() < until; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
	return s.durableID.Load() >= until
}

func (s *Server) writeReplayEvents(ctx context.Context, writeFrame func(event) error, after, until uint64, watchTargets []liveevent.Target, statusOnly bool) error {
	// The durable event-log write is async (off the emit hot path), so the newest
	// published ids may not be in the log yet. Wait briefly for the persister to
	// flush up to `until` before paging: without this, a client reconnecting in
	// the publish→persist window would replay an empty tail and silently drop the
	// newest events (lastPublishedID advances before durability). If the range is
	// still not durable after the bounded wait, force resync instead of paging a
	// partial log and letting the client advance across an unproven gap.
	if gapID, reason, ok := s.replayGapInRange(after, until); ok {
		return s.replayGap(ctx, gapID, reason)
	}
	if !s.waitDurable(ctx, until) {
		missing := s.durableID.Load() + 1
		if missing <= after {
			missing = after + 1
		}
		return s.replayGap(ctx, missing, "not_durable")
	}
	// Page through the log in bounded batches, each read in its own short bbolt
	// txn (ReplayRange copies payloads), and write frames OUTSIDE the txn. A
	// reconnecting client's blocking socket writes must never span a read txn:
	// bbolt is single-writer MVCC, so a long-lived read txn blocks freelist
	// reclamation and lets the event-log file grow unbounded while one slow
	// consumer replays.
	for {
		if gapID, reason, ok := s.replayGapInRange(after, until); ok {
			return s.replayGap(ctx, gapID, reason)
		}
		expected := after + 1
		s.replayMu.RLock()
		oldest, minErr := s.eventStore.MinID(ctx)
		if minErr == nil && oldest > expected {
			s.replayMu.RUnlock()
			return &replayGapError{oldest: oldest, missing: expected, reason: "pruned"}
		}
		batch, err := s.eventStore.ReplayRange(ctx, after, until, eventlog.DefaultReplayBatchSize)
		s.replayMu.RUnlock()
		if minErr != nil {
			return minErr
		}
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			if until > 0 && after < until {
				return s.replayGap(ctx, expected, "missing")
			}
			return nil
		}
		for _, record := range batch {
			if record.ID != expected {
				return &replayGapError{oldest: oldest, missing: expected, reason: "hole"}
			}
			expected = record.ID + 1
			ev := eventFromLog(record)
			if !eventMatchesWatchTargets(ev, watchTargets) {
				continue
			}
			if statusOnly && !eventHasStatus(ev) {
				continue
			}
			if err := writeFrame(ev); err != nil {
				return err
			}
		}
		after = batch[len(batch)-1].ID
		if until > 0 && after >= until {
			return nil
		}
	}
}

func (s *Server) replayGap(ctx context.Context, missing uint64, reason string) error {
	oldest, err := s.eventStore.MinID(ctx)
	if err != nil {
		return err
	}
	if oldest == 0 {
		oldest = missing
	}
	return &replayGapError{oldest: oldest, missing: missing, reason: reason}
}

func reconnectEventID(r *http.Request) (string, bool) {
	if id := strings.TrimSpace(r.Header.Get("Last-Event-ID")); id != "" {
		return id, true
	}
	for _, key := range []string{"last_event_id", "after", "since_event_id"} {
		if id := strings.TrimSpace(r.URL.Query().Get(key)); id != "" {
			return id, true
		}
	}
	return "", false
}

func parseEventID(value string) (uint64, bool) {
	if strings.TrimSpace(value) == "" {
		return 0, true
	}
	id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return id, err == nil
}

func eventIDString(id uint64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

func eventFromLog(record eventlog.Event) event {
	id := eventIDString(record.ID)
	var live LiveEvent
	if err := json.Unmarshal(record.Payload, &live); err == nil && live.Type != "" {
		live.EventID = id
		return event{ID: id, Type: record.Type, Data: live}
	}
	return event{ID: id, Type: record.Type, Data: append(json.RawMessage(nil), record.Payload...)}
}

func (s *Server) PruneEventLog(ctx context.Context) (int, error) {
	if s.eventLogMaxAge <= 0 && s.eventLogMaxEvents <= 0 {
		return 0, nil
	}
	var before time.Time
	if s.eventLogMaxAge > 0 {
		before = time.Now().UTC().Add(-s.eventLogMaxAge)
	}
	// Block while any reconnect replay is in flight (see replayMu): deleting a
	// range mid-replay would silently skip it. Prune runs on a multi-hour GC
	// tick, so waiting for a replay to finish is free.
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	return s.eventStore.Prune(ctx, before, s.eventLogMaxEvents)
}

func (s *Server) runEventLogGC(ctx context.Context) {
	if s.eventLogGCInterval <= 0 {
		return
	}
	s.gcPass(ctx, "startup")
	ticker := time.NewTicker(s.eventLogGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.gcPass(ctx, "interval")
			if active := s.subscriberCount(); active > 0 {
				s.logger.Info("sse subscribers", "active", active, "cap", maxSSESubscribers)
			}
		}
	}
}

func (s *Server) gcPass(ctx context.Context, pass string) {
	n, err := s.PruneEventLog(ctx)
	if err != nil {
		s.logger.Warn("event log gc failed", "err", err)
		return
	}
	if n > 0 {
		s.logger.Info("event log gc", "pruned", n, "pass", pass)
	}
	if evicted := s.pruneLiveSessions(); evicted > 0 {
		s.logger.Info("live sessions gc", "evicted", evicted, "pass", pass)
	}
}

func eventMatchesWatchTargets(ev event, targets []liveevent.Target) bool {
	if len(targets) == 0 {
		return true
	}
	live, ok := ev.Data.(LiveEvent)
	if !ok {
		return false
	}
	return liveevent.AnyTargetMatches(targets, live)
}

func eventHasStatus(ev event) bool {
	live, ok := ev.Data.(LiveEvent)
	if !ok {
		return false
	}
	return live.Status != "" && live.Status != trace.StatusUnknown
}

func isTruthy(value string) bool {
	on, ok := textutil.ParseOnOff(value)
	return ok && on
}

// isFalsy reports only an EXPLICIT falsy token (0/false/no/off); blank or
// unrecognized values are not falsy — so `dryRun := !isFalsy(...)` stays
// "dry-run unless explicitly told otherwise".
func isFalsy(value string) bool {
	on, ok := textutil.ParseOnOff(value)
	return ok && !on
}

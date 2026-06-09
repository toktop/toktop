package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"toktop.unceas.dev/internal/httpapi/internal/eventlog"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/trace"
)

const recentLiveEventScan = 10000

type LiveEvent = liveevent.Event

func (s *Server) loadLiveState(ctx context.Context) error {
	lastID, err := s.eventStore.LastID(ctx)
	if err != nil {
		return err
	}
	if lastID == 0 {
		return nil
	}
	var after uint64
	if lastID > recentLiveEventScan {
		after = lastID - recentLiveEventScan
	}
	for {
		batch, err := s.eventStore.ReplayRange(ctx, after, lastID, eventlog.DefaultReplayBatchSize)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		s.liveMu.Lock()
		for _, record := range batch {
			ev := eventFromLog(record)
			live, ok := ev.Data.(LiveEvent)
			if !ok {
				continue
			}
			live = normalizeLiveEvent(live)
			key := liveEventKey(live)
			if key == "" {
				continue
			}
			if prev, ok := s.liveSessions[key]; ok && !liveEventSupersedes(live, record.ID, prev) {
				continue
			}
			s.liveSessions[key] = live
		}
		s.liveMu.Unlock()
		after = batch[len(batch)-1].ID
	}
}

// pruneLiveSessions evicts cached live-session state older than the event-log
// retention window so liveSessions cannot grow without bound: it is keyed by
// source+session id and is otherwise only ever written, never deleted, so a
// long-running broker would accumulate one permanent entry per distinct session
// id ever seen — inflating memory and the O(sessions) overlayLiveSessions scan
// run on every GET /v1/status. Bounding it to the same window that backs SSE
// replay keeps live state and its event-log backing in step. Returns the number
// of evicted entries.
func (s *Server) pruneLiveSessions() int {
	if s.eventLogMaxAge <= 0 {
		return 0
	}
	cutoff := time.Now().UTC().Add(-s.eventLogMaxAge)
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	evicted := 0
	for key, state := range s.liveSessions {
		if state.At.Before(cutoff) {
			delete(s.liveSessions, key)
			evicted++
		}
	}
	return evicted
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	watchTargets, err := parseWatchTargets(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_watch", err.Error())
		return
	}
	applyWatchTargetsToFilter(&filter, watchTargets)
	page, err := s.service.ListLiveSessions(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_live_sessions_failed", err.Error())
		return
	}
	page.Items = s.overlayLiveSessions(page.Items, filter)
	page.Items = filterLiveSessionItemsByWatch(page.Items, watchTargets)
	page.Total = len(page.Items)
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleEmit(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLiveEventBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("payload exceeds %d bytes", maxLiveEventBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "read_body_failed", err.Error())
		return
	}
	var ev LiveEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if ev.Type == "" {
		writeError(w, http.StatusBadRequest, "missing_type", "type is required")
		return
	}
	stored, err := s.Emit(r.Context(), ev)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "emit_live_event_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "event_id": stored.EventID})
}

func (s *Server) Emit(ctx context.Context, ev LiveEvent) (LiveEvent, error) {
	ev = normalizeLiveEvent(ev)
	// Reason carries free-text hook reason/message that can echo secrets. This
	// is the single chokepoint where every live event (hook intake, /v1/events,
	// and the runtime flusher) is persisted to the replayable event log and
	// broadcast over SSE, so redact here before Append/publish. redact.Apply is
	// a no-op on empty/secret-free text.
	if ev.Reason != "" {
		ev.Reason = redact.Apply(ev.Reason).Redacted
	}
	// Hold emitMu across id-assign + liveSessions update + publish so the
	// monotonic id is fanned out in the same order it was assigned. Without this,
	// two concurrent emitters (hook intake, /v1/events, the runtime flusher) can
	// publish out of id order, and a reconnecting SSE client resuming from the
	// higher id never replays the lower one. The id comes from an in-memory
	// atomic counter (seeded from the event log's LastID at startup), and the
	// durable bbolt write is enqueued to the background persister — so bbolt
	// fsync latency is no longer on the hook→SSE path. Sends are non-blocking, so
	// emitMu never stalls on I/O. Crash semantics are unchanged from the previous
	// NextSequence design: ids of un-fsynced events are reused after a restart,
	// which the SSE hello/resync handshake already tolerates.
	_ = ctx // persistence uses its own background context, not the request ctx
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	id := s.eventSeq.Add(1)
	ev.EventID = eventIDString(id)
	// Marshal once, here, after EventID is set: the same bytes feed both the
	// durable persist job and the SSE fan-out (event.dataJSON), so N subscribers
	// cost N memcpys instead of N re-encodes of this identical payload. The
	// persisted copy now carries event_id too, which replay harmlessly overwrites
	// from the record id (eventFromLog).
	raw, err := json.Marshal(ev)
	if err != nil {
		return LiveEvent{}, fmt.Errorf("marshal live event: %w", err)
	}
	if key := liveEventKey(ev); key != "" {
		s.liveMu.Lock()
		if prev, ok := s.liveSessions[key]; ok && !liveEventSupersedes(ev, id, prev) {
			s.liveMu.Unlock()
			s.publishEvent(event{ID: ev.EventID, Type: ev.Type, Data: ev, dataJSON: raw})
			s.enqueueEventPersist(id, ev.Type, ev.At, raw)
			return ev, nil
		}
		s.liveSessions[key] = ev
		s.liveMu.Unlock()
	} else {
		s.logger.Debug("live event missing session key; not cached in liveSessions",
			"type", ev.Type, "provider", ev.Provider, "event_id", ev.EventID)
	}
	s.publishEvent(event{ID: ev.EventID, Type: ev.Type, Data: ev, dataJSON: raw})
	s.enqueueEventPersist(id, ev.Type, ev.At, raw)
	return ev, nil
}

func liveEventSupersedes(incoming LiveEvent, incomingID uint64, prev LiveEvent) bool {
	if incoming.At.After(prev.At) {
		return true
	}
	if incoming.At.Before(prev.At) {
		return false
	}
	prevID, _ := parseEventID(prev.EventID)
	return incomingID > prevID
}

func normalizeLiveEvent(ev LiveEvent) LiveEvent {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	// Back-fill Provider from SourceID via the registry (SourceID is a stable
	// hash of the provider name), so this works for any registered provider
	// instead of a hardcoded claude-code/codex switch.
	if ev.Provider == "" && ev.SourceID != "" {
		for _, name := range ingest.RegisteredProviders() {
			if trace.SourceID(name) == ev.SourceID {
				ev.Provider = name
				break
			}
		}
	}
	if ev.SourceID == "" && ev.Provider != "" {
		ev.SourceID = trace.SourceID(ev.Provider)
	}
	// Single status-derivation site. Prefer the provider's own hook event→status
	// map (it owns its event vocabulary, e.g. claude StopFailure→failed, codex
	// PermissionRequest→awaiting_confirmation); fall back to the substring
	// heuristic for unknown providers, non-hook events, and events replayed from
	// before RawEventName existed.
	if ev.Status == "" {
		if ev.Provider != "" && ev.RawEventName != "" {
			if hi, ok := ingest.HookInstallerFor(ev.Provider); ok {
				if st, mapped := hi.HookEventStatus(ev.RawEventName); mapped {
					ev.Status = st
				}
			}
		}
		if ev.Status == "" {
			ev.Status = liveStatusForType(firstNonEmpty(ev.RawEventName, ev.Type))
		}
	}
	return ev
}

func liveEventKey(ev LiveEvent) string {
	source := firstNonEmpty(ev.SourceID, ev.Provider)
	id := firstNonEmpty(ev.SessionID, ev.ExternalSessionID, ev.TranscriptPath, ev.File)
	if source == "" || id == "" {
		return ""
	}
	return source + "\x00" + id
}

func (s *Server) overlayLiveSessions(items []sqlite.LiveSessionItem, filter sqlite.Filter) []sqlite.LiveSessionItem {
	s.liveMu.Lock()
	states := make([]LiveEvent, 0, len(s.liveSessions))
	for _, state := range s.liveSessions {
		states = append(states, state)
	}
	s.liveMu.Unlock()

	bySession := make(map[string][]int, len(states))
	byExternal := make(map[string][]int, len(states))
	byPath := make(map[string][]int, len(states))
	for i, state := range states {
		if state.SessionID != "" {
			bySession[state.SessionID] = append(bySession[state.SessionID], i)
		}
		if state.ExternalSessionID != "" {
			byExternal[state.ExternalSessionID] = append(byExternal[state.ExternalSessionID], i)
		}
		if state.TranscriptPath != "" {
			byPath[state.TranscriptPath] = append(byPath[state.TranscriptPath], i)
		}
		if state.File != "" && state.File != state.TranscriptPath {
			byPath[state.File] = append(byPath[state.File], i)
		}
	}

	used := make([]bool, len(states))
	sourceMatches := func(state LiveEvent, item sqlite.LiveSessionItem) bool {
		return state.SourceID == "" || state.SourceID == item.SourceID || state.Provider == item.Provider
	}
	for i := range items {
		seen := make(map[int]struct{})
		apply := func(indices []int) {
			for _, j := range indices {
				if _, ok := seen[j]; ok {
					continue
				}
				seen[j] = struct{}{}
				if !sourceMatches(states[j], items[i]) {
					continue
				}
				applyLiveEventToItem(&items[i], states[j])
				used[j] = true
			}
		}
		if items[i].SessionID != "" {
			apply(bySession[items[i].SessionID])
		}
		if items[i].ExternalSessionID != "" {
			apply(byExternal[items[i].ExternalSessionID])
		}
		if items[i].TranscriptPath != "" {
			apply(byPath[items[i].TranscriptPath])
		}
		// Cross-match the provider id against the toktop id: a hook carries the
		// provider UUID in SessionID, while the ingested row keeps that UUID as
		// ExternalSessionID (its own SessionID is a content hash). Without this a
		// hook-only live state (no transcript_path) fails to correlate and spawns a
		// phantom 0-turn row. apply() dedups via `seen`, so overlap with the matches
		// above is harmless. The reverse (store SessionID vs live ExternalSessionID)
		// can't occur — hooks never carry a content hash — so it is omitted.
		if items[i].ExternalSessionID != "" {
			apply(bySession[items[i].ExternalSessionID])
		}
	}

	for i, state := range states {
		if used[i] || !liveEventMatchesFilter(state, filter) {
			continue
		}
		item := sqlite.LiveSessionItem{
			SourceID:          state.SourceID,
			Provider:          state.Provider,
			SessionID:         state.SessionID,
			ExternalSessionID: state.ExternalSessionID,
			ProjectID:         state.ProjectID,
			ProjectName:       state.ProjectName,
			ProjectPath:       state.ProjectPath,
			TranscriptPath:    firstNonEmpty(state.TranscriptPath, state.File),
			SessionStatus:     trace.StatusUnknown,
			CurrentStatus:     state.Status,
			LastEventType:     state.Type,
			LiveUpdatedAt:     state.At,
			LastActivityAt:    state.At,
		}
		items = append(items, item)
	}
	return items
}

func applyLiveEventToItem(item *sqlite.LiveSessionItem, ev LiveEvent) {
	item.CurrentStatus = firstNonEmpty(ev.Status, item.CurrentStatus)
	item.LastEventType = ev.Type
	item.LiveUpdatedAt = ev.At
	if ev.At.After(item.LastActivityAt) {
		item.LastActivityAt = ev.At
	}
}

func filterLiveSessionItemsByWatch(items []sqlite.LiveSessionItem, targets []liveevent.Target) []sqlite.LiveSessionItem {
	if len(targets) == 0 {
		return items
	}
	filtered := items[:0]
	for _, item := range items {
		ev := liveevent.Event{
			Provider:          item.Provider,
			SourceID:          item.SourceID,
			SessionID:         item.SessionID,
			ExternalSessionID: item.ExternalSessionID,
			TranscriptPath:    item.TranscriptPath,
		}
		if liveevent.AnyTargetMatches(targets, ev) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func liveEventMatchesFilter(ev LiveEvent, filter sqlite.Filter) bool {
	if len(filter.SourceIDs) > 0 {
		if !filterAnyMatch(filter.SourceIDs, ev.SourceID, ev.Provider) {
			return false
		}
	}
	if len(filter.ProjectIDs) > 0 {
		if !filterAnyMatch(filter.ProjectIDs, ev.ProjectID) {
			return false
		}
	}
	if len(filter.SessionIDs) > 0 {
		if !filterAnyMatch(filter.SessionIDs, ev.SessionID, ev.ExternalSessionID, ev.TranscriptPath) {
			return false
		}
	}
	if len(filter.Statuses) > 0 {
		if !filterAnyMatch(filter.Statuses, ev.Status) {
			return false
		}
	}
	if !filter.Since.IsZero() && ev.At.Before(filter.Since) {
		return false
	}
	if !filter.Until.IsZero() && !ev.At.Before(filter.Until) {
		return false
	}
	return true
}

func filterAnyMatch(wanted []string, candidates ...string) bool {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		for _, w := range wanted {
			if c == strings.TrimSpace(w) {
				return true
			}
		}
	}
	return false
}

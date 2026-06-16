package httpapi

import (
	"net/http"

	"toktop.unceas.dev/internal/runtime"
)

// maxControlRequestBytes caps the JSON body of the small control POST routes
// (daemon:trigger, data:prune, retention:prune) — see decodeJSONBody.
const maxControlRequestBytes int64 = 64 << 10

func (s *Server) AttachRuntime(svc *runtime.Service) {
	s.runtime.Store(svc)
}

// Backpressure is the httpapi-owned portion of the broker's lossy/degradation
// surface, surfaced on GET /v1/daemon so a degrading daemon is observable. The
// runtime-owned ingest/emit drop totals ride runtime.Status.Counters.
type Backpressure struct {
	PersistQueueFull         uint64 `json:"persist_queue_full_total"`
	SSESlowSubscriberDropped uint64 `json:"sse_slow_subscriber_dropped_total"`
	SpoolDropped             uint64 `json:"spool_dropped_total"`
	SpoolDroppedBytes        uint64 `json:"spool_dropped_bytes"`
	DurableLag               uint64 `json:"durable_lag"`
	PersistQueueLen          int    `json:"persist_queue_len"`
	LiveSessions             int    `json:"live_sessions"`
}

// DaemonStatus is the GET /v1/daemon response: the runtime ingest/watch status
// plus the broker backpressure snapshot.
type DaemonStatus struct {
	runtime.Status
	Backpressure Backpressure `json:"backpressure"`
}

func (s *Server) handleDaemonStatus(w http.ResponseWriter, r *http.Request) {
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	writeJSON(w, http.StatusOK, DaemonStatus{Status: rt.Status(), Backpressure: s.backpressure()})
}

func (s *Server) handleDaemonIngest(w http.ResponseWriter, r *http.Request) {
	// Decode (and close the body) before the runtime check, so the body is read
	// and closed on every path — matching the other control routes' ordering.
	var req runtime.TriggerRequest
	if !decodeJSONBody(w, r, maxControlRequestBytes, &req) {
		return
	}
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	if req.Mode == "" {
		req.Mode = "full"
	}
	res, err := rt.TriggerIngest(r.Context(), req)
	if err != nil {

		writeError(w, http.StatusBadRequest, "trigger_failed", err.Error())
		return
	}
	status := http.StatusOK
	if !req.Sync {
		status = http.StatusAccepted
	}
	writeJSON(w, status, res)
}

func (s *Server) handleDaemonPause(w http.ResponseWriter, r *http.Request) {
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	if err := rt.Pause(r.Context()); err != nil {
		writeError(w, http.StatusConflict, "pause_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDaemonResume(w http.ResponseWriter, r *http.Request) {
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	if err := rt.Resume(r.Context()); err != nil {
		writeError(w, http.StatusConflict, "resume_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

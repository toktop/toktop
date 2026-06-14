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

func (s *Server) handleDaemonStatus(w http.ResponseWriter, r *http.Request) {
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	writeJSON(w, http.StatusOK, rt.Status())
}

func (s *Server) handleDaemonIngest(w http.ResponseWriter, r *http.Request) {
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	var req runtime.TriggerRequest
	if !decodeJSONBody(w, r, maxControlRequestBytes, &req) {
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

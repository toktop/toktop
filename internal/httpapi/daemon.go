package httpapi

import (
	"encoding/json"
	"net/http"

	"toktop.unceas.dev/internal/runtime"
)

const maxDaemonRequestBytes int64 = 64 << 10

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
	defer r.Body.Close()
	rt := s.runtime.Load()
	if rt == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime not attached")
		return
	}
	body, ok := readBodyCapped(w, r, maxDaemonRequestBytes, "bad_body")
	if !ok {
		return
	}
	var req runtime.TriggerRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
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

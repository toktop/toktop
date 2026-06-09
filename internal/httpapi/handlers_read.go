package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"toktop.unceas.dev/internal/httpapi/internal/eventlog"
	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/query"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/textutil"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"db_path":        sqlite.DBPath(s.store.DataDir()),
		"event_log_path": eventlog.DBPath(s.store.DataDir()),
		"started_at":     time.Now().UTC(),
	})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	summary, err := s.service.Summary(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "summary_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	page, err := s.service.ListSessions(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_sessions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	session, err := s.service.GetSession(r.Context(), id)
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_session_failed", err.Error())
		return
	}
	turns, err := s.service.SessionTurns(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_turns_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "turns": turns})
}

func (s *Server) handleTurns(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	page, err := s.service.ListTurns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_turns_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turn, err := s.service.GetTurn(r.Context(), id)
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "turn not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_turn_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, turn)
}

func (s *Server) handleTurnTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turn, err := s.service.GetTurn(r.Context(), id)
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "turn not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_turn_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, BuildTimeline(turn))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := atoiOr(r.URL.Query().Get("limit"), 20)
	if limit < 1 {
		// Mirror the CLI `search --limit < 1` rejection so both surfaces agree
		// instead of the store silently clamping to a default.
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be >= 1")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	results, err := s.service.Search(r.Context(), q, limit, kind, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "results": results})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	projects, err := s.service.ListProjects(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_projects_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	tools, err := s.service.ListTools(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_tools_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tools)
}

func (s *Server) handleMCPs(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	mcps, err := s.service.ListMCPs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_mcps_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mcps)
}

func (s *Server) handleUnusedMCPs(w http.ResponseWriter, r *http.Request) {
	mcps, err := s.service.ListUnusedMCPs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_unused_mcps_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mcps)
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	skills, err := s.service.ListSkills(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_skills_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Server) handleUnusedSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := s.service.ListUnusedSkills(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_unused_skills_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Server) handleTurnComponents(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("id")
	if turnID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "turn id is required")
		return
	}
	components, err := s.service.ListComponents(r.Context(), turnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_components_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, components)
}

func (s *Server) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	rule := r.URL.Query().Get("rule")
	sugs, err := s.service.Suggestions(r.Context(), rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_suggestions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sugs)
}

func (s *Server) handleSuggestionsRecompute(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.RecomputeSuggestions(r.Context(), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recompute_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	// Optional ?since= scopes the export to a recent window so it does not load
	// the entire history; absent, it exports everything (zero time).
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		cutoff, err := query.ParseSince(raw, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since", err.Error())
			return
		}
		since = cutoff
	}
	index, err := s.service.Snapshot(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, index)
}

func parseFilter(r *http.Request) (sqlite.Filter, error) {
	q := r.URL.Query()
	var sourceIDs []string
	for _, app := range queryValues(q, "source", "sources", "provider", "providers", "app", "apps") {

		sourceIDs = append(sourceIDs, query.ResolveSourceFilter(app))
	}
	filter := sqlite.Filter{
		SourceIDs:  textutil.DedupNonEmpty(sourceIDs),
		ProjectIDs: textutil.DedupNonEmpty(queryValues(q, "project", "projects")),
		SessionIDs: textutil.DedupNonEmpty(queryValues(q, "session", "sessions")),
		Statuses:   textutil.DedupNonEmpty(queryValues(q, "status", "statuses")),
		Limit:      atoiOr(q.Get("limit"), 0),
		Offset:     atoiOr(q.Get("offset"), 0),
		SortBy:     q.Get("sort_by"),
	}
	if order := q.Get("sort"); order != "" {
		filter.SortBy, filter.SortDesc = query.ParseSort(order)
	}
	now := time.Now().UTC()
	if since := q.Get("since"); since != "" {
		cutoff, err := query.ParseSince(since, now)
		if err != nil {
			return sqlite.Filter{}, err
		}
		filter.Since = cutoff
	}
	if until := q.Get("until"); until != "" {
		cutoff, err := query.ParseSince(until, now)
		if err != nil {
			return sqlite.Filter{}, err
		}
		filter.Until = cutoff
	}
	return filter, nil
}

func parseWatchTargets(r *http.Request) ([]liveevent.Target, error) {
	return liveevent.ParseWatchTargets(queryValues(r.URL.Query(), "watch", "watches"))
}

// applyWatchTargetsToFilter narrows the DB scan by the requested (source,
// session) watch pairs. The store combines SourceIDs/SessionIDs as independent
// AND'd IN-lists, so flattening multiple pairs into two lists would form a
// cartesian product matching pairs that were never requested (e.g. watching
// (codex,A) and (claude,B) would also match (codex,B)). That widening is only
// safe when there is a single pair — an exact source+session match. With more
// than one target we leave the DB filter untouched and rely on the exact
// post-filter (filterLiveSessionItemsByWatch) for correctness.
func applyWatchTargetsToFilter(filter *sqlite.Filter, targets []liveevent.Target) {
	if len(targets) != 1 {
		return
	}
	target := targets[0]
	filter.SourceIDs = textutil.DedupNonEmpty(append(filter.SourceIDs, target.SourceID))
	filter.SessionIDs = textutil.DedupNonEmpty(append(filter.SessionIDs, target.Session))
}

func queryValues(q map[string][]string, keys ...string) []string {
	var out []string
	for _, key := range keys {
		for _, raw := range q[key] {
			out = append(out, textutil.SplitTrim(raw)...)
		}
	}
	return out
}

// firstNonEmpty returns the first non-blank value, TRIMMED — the trim-on-return
// normalizes the untrimmed URL-query values this handler feeds it.
func firstNonEmpty(values ...string) string {
	return strings.TrimSpace(textutil.FirstNonBlank(values...))
}

func atoiOr(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

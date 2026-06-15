package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"toktop.unceas.dev/internal/handoff"
	"toktop.unceas.dev/internal/httpapi/internal/eventlog"
	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/query"
	"toktop.unceas.dev/internal/store/sqlite"
	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
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
		writeQueryError(w, err, "invalid_filter")
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
		writeQueryError(w, err, "invalid_filter")
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
	session, turns, ambiguous, ok := s.loadSessionWithTurns(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"session": session, "turns": turns}
	if len(ambiguous) > 0 {
		resp["ambiguous_session_ids"] = ambiguous
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadSessionWithTurns resolves the {id} path value to a session (matching an
// internal or external id) and loads its turns, writing the matching HTTP error
// and returning ok=false on failure. Shared by the session-detail and handoff
// routes so their lookup, 404 wording, and error codes cannot drift apart. The
// returned ambiguous slice lists every matching internal id when an external id
// resolved to more than one session (else nil) — so the caller can signal it
// instead of silently handing back an arbitrary first match, mirroring the CLI's
// disambiguation note.
func (s *Server) loadSessionWithTurns(w http.ResponseWriter, r *http.Request) (trace.Session, []trace.Turn, []string, bool) {
	matches, err := s.service.FindSessions(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "get_session_failed", err.Error())
		}
		return trace.Session{}, nil, nil, false
	}
	// matches is ordered exact-internal-id first, so [0] is the session to use
	// (the same pick the CLI's selectSessionMatch makes).
	session := matches[0]
	var ambiguous []string
	if len(matches) > 1 {
		ambiguous = make([]string, len(matches))
		for i, m := range matches {
			ambiguous[i] = m.ID
		}
	}
	turns, err := s.service.SessionTurns(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_turns_failed", err.Error())
		return trace.Session{}, nil, nil, false
	}
	return session, turns, ambiguous, true
}

func (s *Server) handleSessionHandoff(w http.ResponseWriter, r *http.Request) {
	// ?max_output_bytes=N mirrors the CLI --max-output-bytes flag (0 = full).
	// Validate the cheap query param before the session + turns DB load.
	maxOutputBytes, err := intParam(r.URL.Query(), "max_output_bytes", 0)
	if err != nil {
		writeQueryError(w, err, "invalid_max_output_bytes")
		return
	}
	if maxOutputBytes < 0 {
		// Reject a negative byte cap instead of silently treating it as "full"
		// (0 = full), mirroring search's `limit < 1` rejection.
		writeError(w, http.StatusBadRequest, "invalid_max_output_bytes", "max_output_bytes must be >= 0")
		return
	}
	session, turns, ambiguous, ok := s.loadSessionWithTurns(w, r)
	if !ok {
		return
	}
	subagentRuns, err := s.service.SubagentRuns(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "subagent_runs_failed", err.Error())
		return
	}
	pkg := handoff.Build(time.Now().UTC(), session, turns, subagentRuns, maxOutputBytes)
	// recommended_entrypoints names CLI directory files that don't exist over HTTP
	// (the package is one JSON body here); ambiguous_session_ids mirrors the CLI's
	// note for an external id that matched several sessions.
	pkg.Manifest.RecommendedEntrypoints = nil
	pkg.Manifest.AmbiguousSessionIDs = ambiguous
	writeJSON(w, http.StatusOK, pkg)
}

func (s *Server) handleTurns(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeQueryError(w, err, "invalid_filter")
		return
	}
	page, err := s.service.ListTurns(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_turns_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// loadTurn resolves the {id} path value to a turn, writing the matching HTTP
// error and returning ok=false on failure. Shared by the turn-detail and
// timeline routes so their lookup, 404 wording, and error codes cannot drift.
func (s *Server) loadTurn(w http.ResponseWriter, r *http.Request) (trace.Turn, bool) {
	turn, err := s.service.GetTurn(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "turn not found")
		} else {
			writeError(w, http.StatusInternalServerError, "get_turn_failed", err.Error())
		}
		return trace.Turn{}, false
	}
	return turn, true
}

func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	turn, ok := s.loadTurn(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, turn)
}

func (s *Server) handleTurnTimeline(w http.ResponseWriter, r *http.Request) {
	turn, ok := s.loadTurn(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, BuildTimeline(turn))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	q := strings.TrimSpace(values.Get("q"))
	limit, err := intParam(values, "limit", 20)
	if err != nil {
		writeQueryError(w, err, "invalid_limit")
		return
	}
	if limit < 1 {
		// Mirror the CLI `search --limit < 1` rejection so both surfaces agree
		// instead of the store silently clamping to a default.
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be >= 1")
		return
	}
	kind := strings.TrimSpace(values.Get("kind"))
	source := strings.TrimSpace(values.Get("source"))
	if source != "" {
		// Resolve a provider name to its content-hashed source_id (and reject an
		// unknown one), matching the list-filter convention and what the store's
		// search filter compares against. Passing the raw name silently returned 0.
		resolved, err := resolveSourceFilter(source)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unknown_source", err.Error())
			return
		}
		source = resolved
	}
	includeSubagents, err := boolParam(values, "subagents", false)
	if err != nil {
		writeQueryError(w, err, "invalid_subagents")
		return
	}
	results, err := s.service.Search(r.Context(), q, limit, kind, source, includeSubagents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "results": results})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeQueryError(w, err, "invalid_filter")
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
		writeQueryError(w, err, "invalid_filter")
		return
	}
	tools, err := s.service.ListTools(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_tools_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tools)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeQueryError(w, err, "invalid_filter")
		return
	}
	models, err := s.service.ListModels(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_models_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) handleMCPs(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeQueryError(w, err, "invalid_filter")
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
		writeQueryError(w, err, "invalid_filter")
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
	includeSubagents, err := boolParam(r.URL.Query(), "subagents", false)
	if err != nil {
		writeQueryError(w, err, "invalid_subagents")
		return
	}
	index, err := s.service.Snapshot(r.Context(), since, includeSubagents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, index)
}

// resolveSourceFilter delegates to query.ResolveSourceToken so the HTTP filter
// builder and search agree with the CLI on source alias resolution + validation.
func resolveSourceFilter(app string) (string, error) {
	return query.ResolveSourceToken(app)
}

func parseFilter(r *http.Request) (sqlite.Filter, error) {
	q := r.URL.Query()
	var sourceIDs []string
	for _, app := range queryValues(q, "source", "sources", "provider", "providers", "app", "apps") {
		id, err := resolveSourceFilter(app)
		if err != nil {
			return sqlite.Filter{}, err
		}
		sourceIDs = append(sourceIDs, id)
	}
	statuses := textutil.DedupNonEmpty(queryValues(q, "status", "statuses"))
	if err := query.ValidateStatuses(statuses); err != nil {
		return sqlite.Filter{}, err
	}
	// limit/offset default to 0 (the store's "default page" sentinel); a present
	// but unparseable value is rejected, not silently swallowed (see intParam).
	limit, err := intParam(q, "limit", 0)
	if err != nil {
		return sqlite.Filter{}, err
	}
	offset, err := intParam(q, "offset", 0)
	if err != nil {
		return sqlite.Filter{}, err
	}
	includeSubagents, err := boolParam(q, "subagents", false)
	if err != nil {
		return sqlite.Filter{}, err
	}
	filter := sqlite.Filter{
		SourceIDs:        textutil.DedupNonEmpty(sourceIDs),
		ProjectIDs:       textutil.DedupNonEmpty(queryValues(q, "project", "projects")),
		SessionIDs:       textutil.DedupNonEmpty(queryValues(q, "session", "sessions")),
		Statuses:         statuses,
		Limit:            limit,
		Offset:           offset,
		SortBy:           q.Get("sort_by"),
		IncludeSubagents: includeSubagents,
	}
	if order := q.Get("sort"); order != "" {
		filter.SortBy, filter.SortDesc = query.ParseSort(order)
		if !slices.Contains(validHTTPSorts, filter.SortBy) {
			return sqlite.Filter{}, fmt.Errorf("invalid sort %q", order)
		}
	}
	if filter.SortBy != "" && !slices.Contains(validHTTPSorts, filter.SortBy) {
		return sqlite.Filter{}, fmt.Errorf("invalid sort_by %q", filter.SortBy)
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

var validHTTPSorts = []string{"started", "tokens", "duration", "turns"}

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

// queryParamError carries a per-parameter HTTP error code (invalid_limit,
// invalid_offset, …) so the same bad param surfaces the same code regardless of
// whether it was parsed inline (handleSearch) or inside parseFilter — write it
// via writeQueryError.
type queryParamError struct {
	code string
	msg  string
}

func (e *queryParamError) Error() string { return e.msg }

// writeQueryError responds 400 with the parameter-specific code when err carries
// one, else fallbackCode. Use at every site that surfaces a parse/validation
// error from intParam or parseFilter.
func writeQueryError(w http.ResponseWriter, err error, fallbackCode string) {
	code := fallbackCode
	var pe *queryParamError
	if errors.As(err, &pe) {
		code = pe.code
	}
	writeError(w, http.StatusBadRequest, code, err.Error())
}

// intParam parses query parameter key as an int. An absent value yields fallback;
// a present-but-unparseable value yields a queryParamError (code "invalid_<key>")
// so the handler can reject it with 400 rather than silently swallowing a client
// typo behind a default. Use it for filter/listing/handoff numeric params; SSE
// resume ids go through parseEventID instead (uint64 + tolerant fallback,
// required for reconnect).
func intParam(values url.Values, key string, fallback int) (int, error) {
	raw := values.Get(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &queryParamError{code: "invalid_" + key, msg: key + " must be an integer"}
	}
	return n, nil
}

// boolParam parses query parameter key as a boolean toggle, accepting the common
// truthy/falsy spellings. Absent yields fallback; a present-but-unrecognized value
// is rejected (code "invalid_<key>"), mirroring intParam rather than silently
// swallowing a client typo.
func boolParam(values url.Values, key string, fallback bool) (bool, error) {
	raw := values.Get(key)
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	on, ok := textutil.ParseOnOff(raw)
	if !ok {
		return false, &queryParamError{code: "invalid_" + key, msg: key + " must be a boolean (1/0, true/false, on/off)"}
	}
	return on, nil
}

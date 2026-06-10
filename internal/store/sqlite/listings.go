package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"toktop.unceas.dev/internal/textutil"
	"toktop.unceas.dev/internal/trace"
)

type ProjectListItem struct {
	ID            string    `json:"id"`
	SourceID      string    `json:"source_id"`
	Name          string    `json:"name"`
	Path          string    `json:"path,omitzero"`
	SessionCount  int       `json:"session_count"`
	TurnCount     int       `json:"turn_count"`
	ToolCallCount int       `json:"tool_call_count"`
	LastActivity  time.Time `json:"last_activity,omitzero"`
}

type ToolListItem struct {
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	MCPServer   string    `json:"mcp_server,omitzero"`
	CallCount   int       `json:"call_count"`
	TurnCount   int       `json:"turn_count"`
	FailedCount int       `json:"failed_count"`
	LastUsedAt  time.Time `json:"last_used_at,omitzero"`
}

type MCPListItem struct {
	Server       string    `json:"server"`
	CallCount    int       `json:"call_count"`
	ToolCount    int       `json:"tool_count"`
	TurnCount    int       `json:"turn_count"`
	LastUsedAt   time.Time `json:"last_used_at,omitzero"`
	Availability int       `json:"availability_observed"`
	Declared     bool      `json:"declared"`
	Scope        string    `json:"scope,omitzero"`
	ConfigPath   string    `json:"config_path,omitzero"`
}

type SkillListItem struct {
	Name              string          `json:"name"`
	Scope             string          `json:"scope,omitzero"`
	SourcePath        string          `json:"source_path,omitzero"`
	Description       string          `json:"description,omitzero"`
	Version           string          `json:"version,omitzero"`
	ArgumentHint      string          `json:"argument_hint,omitzero"`
	UserInvocable     *bool           `json:"user_invocable,omitempty"`
	Triggers          json.RawMessage `json:"triggers,omitempty"`
	AllowedTools      json.RawMessage `json:"allowed_tools,omitempty"`
	Tools             json.RawMessage `json:"tools,omitempty"`
	Compatibility     string          `json:"compatibility,omitzero"`
	License           string          `json:"license,omitzero"`
	Installed         bool            `json:"installed"`
	InferredUsedCount int             `json:"inferred_used_count"`
	LastUsedAt        time.Time       `json:"last_used_at,omitzero"`
}

type Filter struct {
	SourceIDs  []string
	ProjectIDs []string
	SessionIDs []string
	Statuses   []string

	Since    time.Time
	Until    time.Time
	Limit    int
	Offset   int
	SortDesc bool
	SortBy   string
}

type Summary struct {
	Sessions             int `json:"sessions"`
	Turns                int `json:"turns"`
	Invocations          int `json:"invocations"`
	ToolCalls            int `json:"tool_calls"`
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadTokens      int `json:"cache_read_tokens"`
	CacheWriteTokens     int `json:"cache_write_tokens"`
	CacheWriteLongTokens int `json:"cache_write_long_tokens"`
	ParseErrors          int `json:"parse_errors"`
	RawEvents            int `json:"raw_events"`
}

type LiveSessionItem struct {
	SourceID          string    `json:"source_id"`
	Provider          string    `json:"provider"`
	SessionID         string    `json:"session_id"`
	ExternalSessionID string    `json:"external_session_id,omitzero"`
	ProjectID         string    `json:"project_id,omitzero"`
	ProjectName       string    `json:"project_name,omitzero"`
	ProjectPath       string    `json:"project_path,omitzero"`
	TranscriptPath    string    `json:"transcript_path,omitzero"`
	SessionStatus     string    `json:"session_status"`
	LastTurnID        string    `json:"last_turn_id,omitzero"`
	LastTurnStatus    string    `json:"last_turn_status,omitzero"`
	CurrentStatus     string    `json:"current_status"`
	StartedAt         time.Time `json:"started_at,omitzero"`
	LastActivityAt    time.Time `json:"last_activity_at,omitzero"`
	TurnCount         int       `json:"turn_count"`
	ToolCallCount     int       `json:"tool_call_count"`
	LastEventType     string    `json:"last_event_type,omitzero"`
	LiveUpdatedAt     time.Time `json:"live_updated_at,omitzero"`
}

func (s *Store) SummaryFiltered(ctx context.Context, f Filter) (Summary, error) {
	f = f.normalized()
	// The reader is a query_only pool (MaxOpenConns=GOMAXPROCS), so the four
	// independent COUNT/SUM scans run concurrently instead of serially. Each
	// goroutine writes a disjoint set of Summary fields; wg.Wait establishes the
	// happens-before that makes those writes visible without a per-field lock.
	var summary Summary
	turnWhere, turnArgs := f.turnWhere()
	sessionWhere, sessionArgs := f.sessionWhere()
	rawWhere, rawArgs := f.rawEventWhere()
	parseWhere, parseArgs := f.parseErrorWhere()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	wg.Add(4)
	go func() {
		defer wg.Done()
		errs[0] = s.reader().QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(total_input_tokens), 0),
			       COALESCE(SUM(total_output_tokens), 0),
			       COALESCE(SUM(cache_read_tokens), 0),
			       COALESCE(SUM(cache_write_tokens), 0),
			       COALESCE(SUM(cache_write_long_tokens), 0),
			       COALESCE(SUM(invocation_count), 0),
			       COALESCE(SUM(tool_call_count), 0)
			FROM turns
			`+turnWhere, turnArgs...).Scan(
			&summary.Turns, &summary.InputTokens, &summary.OutputTokens,
			&summary.CacheReadTokens, &summary.CacheWriteTokens, &summary.CacheWriteLongTokens,
			&summary.Invocations, &summary.ToolCalls,
		)
	}()
	go func() {
		defer wg.Done()
		errs[1] = s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions `+sessionWhere, sessionArgs...).Scan(&summary.Sessions)
	}()
	go func() {
		defer wg.Done()
		errs[2] = s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_events `+rawWhere, rawArgs...).Scan(&summary.RawEvents)
	}()
	go func() {
		defer wg.Done()
		errs[3] = s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM parse_errors `+parseWhere, parseArgs...).Scan(&summary.ParseErrors)
	}()
	wg.Wait()

	names := [4]string{"turns", "sessions", "raw_events", "parse_errors"}
	for i, err := range errs {
		if err != nil {
			return Summary{}, fmt.Errorf("summary %s: %w", names[i], err)
		}
	}
	return summary, nil
}

func (s *Store) ListTurnsFiltered(ctx context.Context, f Filter) ([]trace.Turn, int, error) {
	f = f.normalized()
	wheres, args := f.turnWhere()
	limit, offset := f.pagination(50)
	order := f.turnOrderBy()
	rows, err := s.reader().QueryContext(ctx, turnsBaseQuery+` `+wheres+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list turns: %w", err)
	}
	turns, err := scanTurns(rows)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM turns `+wheres, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count turns: %w", err)
	}
	return turns, total, nil
}

func (s *Store) ListSessionsFiltered(ctx context.Context, f Filter) ([]trace.Session, int, error) {
	f = f.normalized()
	wheres, args := f.sessionWhere()
	limit, offset := f.pagination(50)
	order := f.sessionOrderBy()
	rows, err := s.reader().QueryContext(ctx, sessionsBaseQuery+` `+wheres+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list sessions: %w", err)
	}
	sessions, err := scanSessions(rows)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions `+wheres, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count sessions: %w", err)
	}
	return sessions, total, nil
}

func (s *Store) ListLiveSessions(ctx context.Context, f Filter) ([]LiveSessionItem, int, error) {
	f = f.normalized()
	wheres, args := f.sessionWhere()
	limit, offset := f.pagination(100)
	rows, err := s.reader().QueryContext(ctx, `
		SELECT sessions.source_id, sources.kind, sessions.id,
		       COALESCE(sessions.external_session_id, ''),
		       COALESCE(sessions.project_id, ''),
		       COALESCE(projects.name, ''),
		       COALESCE(projects.path, ''),
		       sessions.transcript_path,
		       sessions.status,
		       COALESCE(sessions.last_turn_id, ''),
		       COALESCE(sessions.last_turn_status, ''),
		       COALESCE(sessions.started_at, ''),
		       COALESCE(
		           NULLIF(sessions.last_turn_at, ''),
		           NULLIF(sessions.ended_at, ''),
		           NULLIF(sessions.started_at, ''),
		           ''
		       ) AS last_activity,
		       sessions.total_turns,
		       sessions.total_tool_calls
		FROM sessions
		JOIN sources ON sources.id = sessions.source_id
		LEFT JOIN projects ON projects.id = sessions.project_id
		`+wheres+`
		ORDER BY last_activity DESC, sessions.id
		LIMIT ? OFFSET ?
	`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list live sessions: %w", err)
	}
	defer rows.Close()
	items := make([]LiveSessionItem, 0)
	for rows.Next() {
		var item LiveSessionItem
		var startedAt, lastActivity sql.NullString
		if err := rows.Scan(
			&item.SourceID, &item.Provider, &item.SessionID,
			&item.ExternalSessionID, &item.ProjectID, &item.ProjectName,
			&item.ProjectPath, &item.TranscriptPath, &item.SessionStatus,
			&item.LastTurnID, &item.LastTurnStatus,
			&startedAt, &lastActivity,
			&item.TurnCount, &item.ToolCallCount,
		); err != nil {
			return nil, 0, fmt.Errorf("scan live session: %w", err)
		}
		item.StartedAt = parseTimeOpt(startedAt)
		item.LastActivityAt = parseTimeOpt(lastActivity)
		item.CurrentStatus = currentStatus(item.SessionStatus, item.LastTurnStatus)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions `+wheres, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count live sessions: %w", err)
	}
	return items, total, nil
}

func (s *Store) ListProjects(ctx context.Context, f Filter) ([]ProjectListItem, error) {
	f = f.normalized()
	var wheres []string
	var args []any
	if ids := f.sourceIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("projects.source_id", ids, &args))
	}
	clause := ""
	if len(wheres) > 0 {
		clause = " WHERE " + strings.Join(wheres, " AND ")
	}
	rows, err := s.reader().QueryContext(ctx, `
		SELECT projects.id, projects.source_id, projects.name, COALESCE(projects.path, ''),
		       COALESCE(COUNT(DISTINCT sessions.id), 0),
		       COALESCE(SUM(sessions.total_turns), 0),
		       COALESCE(SUM(sessions.total_tool_calls), 0),
		       COALESCE(MAX(sessions.ended_at), '')
		FROM projects
		LEFT JOIN sessions ON sessions.project_id = projects.id
		`+clause+`
		GROUP BY projects.id
		ORDER BY MAX(sessions.ended_at) DESC, projects.name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []ProjectListItem
	for rows.Next() {
		var item ProjectListItem
		var lastActivity sql.NullString
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Name, &item.Path,
			&item.SessionCount, &item.TurnCount, &item.ToolCallCount, &lastActivity); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		item.LastActivity = parseTimeOpt(lastActivity)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListTools(ctx context.Context, f Filter) ([]ToolListItem, error) {
	f = f.normalized()
	var args []any
	wheres := f.joinedTurnWhere(&args)
	if !f.Since.IsZero() {
		wheres = append(wheres, `tool_calls.started_at >= ?`)
		args = append(args, timeBound(f.Since))
	}
	clause := whereClause(wheres)
	rows, err := s.reader().QueryContext(ctx, `
		SELECT tool_kind, tool_name, COALESCE(mcp_server, ''),
		       COUNT(*),
		       COUNT(DISTINCT tool_calls.turn_id),
		       COALESCE(SUM(CASE WHEN tool_calls.status = 'failed' THEN 1 ELSE 0 END), 0),
		       COALESCE(MAX(tool_calls.started_at), '')
		FROM tool_calls
		JOIN turns ON turns.id = tool_calls.turn_id
		JOIN sessions ON sessions.id = tool_calls.session_id
		`+clause+`
		GROUP BY tool_kind, tool_name, mcp_server
		ORDER BY COUNT(*) DESC, tool_name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	defer rows.Close()
	var out []ToolListItem
	for rows.Next() {
		var item ToolListItem
		var lastUsed sql.NullString
		if err := rows.Scan(&item.Kind, &item.Name, &item.MCPServer, &item.CallCount, &item.TurnCount, &item.FailedCount, &lastUsed); err != nil {
			return nil, fmt.Errorf("scan tool: %w", err)
		}
		item.LastUsedAt = parseTimeOpt(lastUsed)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListMCPs(ctx context.Context, f Filter) ([]MCPListItem, error) {
	f = f.normalized()
	var args []any
	wheres := f.joinedTurnWhere(&args)
	if !f.Since.IsZero() {
		wheres = append(wheres, `tool_calls.started_at >= ?`)
		args = append(args, timeBound(f.Since))
	}
	clause := whereClause(append([]string{`tool_calls.tool_kind = 'mcp'`}, wheres...))
	// When the caller scoped the query (source/session/project/since/until),
	// only return MCP servers actually observed in that scope; otherwise the
	// unfiltered declared set would leak servers from every source/scope/window.
	onlyObserved := ""
	if f.hasRuntimeConstraint() {
		onlyObserved = "WHERE observed.server IS NOT NULL"
	}
	rows, err := s.reader().QueryContext(ctx, `
		WITH observed AS (
			SELECT COALESCE(tool_calls.mcp_server, '') AS server,
			       COUNT(*) AS calls,
			       COUNT(DISTINCT tool_calls.mcp_tool) AS tools,
			       COUNT(DISTINCT tool_calls.turn_id) AS turns,
			       MAX(tool_calls.started_at) AS last_used
			FROM tool_calls
			JOIN turns ON turns.id = tool_calls.turn_id
			JOIN sessions ON sessions.id = tool_calls.session_id
			`+clause+`
			GROUP BY mcp_server
		),
		declared_ranked AS (
			SELECT name AS server, scope, config_path,
			       ROW_NUMBER() OVER (PARTITION BY name ORDER BY scope, config_path) AS rn
			FROM mcp_servers
		),
		declared AS (
			-- One winning row per name so scope/config_path come from the same
			-- declared row rather than independent MAX() across scopes.
			SELECT server, scope, config_path
			FROM declared_ranked
			WHERE rn = 1
		)
		SELECT COALESCE(observed.server, declared.server) AS server,
		       COALESCE(observed.calls, 0) AS calls,
		       COALESCE(observed.tools, 0) AS tools,
		       COALESCE(observed.turns, 0) AS turns,
		       COALESCE(observed.last_used, '') AS last_used,
		       CASE WHEN declared.server IS NULL THEN 0 ELSE 1 END AS declared,
		       COALESCE(declared.scope, '') AS scope,
		       COALESCE(declared.config_path, '') AS config_path
		FROM observed
		FULL OUTER JOIN declared ON declared.server = observed.server
		`+onlyObserved+`
		ORDER BY calls DESC, server
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list mcps: %w", err)
	}
	defer rows.Close()
	var out []MCPListItem
	for rows.Next() {
		var item MCPListItem
		var lastUsed sql.NullString
		var declared int
		if err := rows.Scan(&item.Server, &item.CallCount, &item.ToolCount, &item.TurnCount, &lastUsed,
			&declared, &item.Scope, &item.ConfigPath); err != nil {
			return nil, fmt.Errorf("scan mcp: %w", err)
		}
		item.LastUsedAt = parseTimeOpt(lastUsed)
		item.Declared = declared == 1
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	availability, err := s.componentAvailability(ctx, trace.ComponentKindMCPServer, f)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Availability = availability[out[i].Server]
	}
	return out, nil
}

func (s *Store) ListUnusedMCPs(ctx context.Context) ([]MCPListItem, error) {
	rows, err := s.reader().QueryContext(ctx, `
		WITH declared_ranked AS (
			SELECT name, scope, config_path,
			       ROW_NUMBER() OVER (PARTITION BY name ORDER BY scope, config_path) AS rn
			FROM mcp_servers
		)
		-- One winning row per name so scope/config_path come from the same
		-- declared row rather than independent MAX() across scopes.
		SELECT name, scope, config_path
		FROM declared_ranked
		WHERE rn = 1
		  AND name NOT IN (
			SELECT DISTINCT mcp_server
			FROM tool_calls
			WHERE tool_kind = 'mcp' AND mcp_server IS NOT NULL AND mcp_server <> ''
		)
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list unused mcps: %w", err)
	}
	defer rows.Close()
	var out []MCPListItem
	for rows.Next() {
		var item MCPListItem
		if err := rows.Scan(&item.Server, &item.Scope, &item.ConfigPath); err != nil {
			return nil, fmt.Errorf("scan unused mcp: %w", err)
		}
		item.Declared = true
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListSkills(ctx context.Context, f Filter) ([]SkillListItem, error) {
	f = f.normalized()
	var args []any
	wheres := append([]string{`turn_components.component_kind = 'skill'`}, f.joinedTurnWhere(&args)...)
	if !f.Since.IsZero() {
		wheres = append(wheres, `turn_components.created_at >= ?`)
		args = append(args, timeBound(f.Since))
	}
	usedClause := whereClause(wheres)
	onlyObserved := ""
	if f.hasRuntimeConstraint() {
		onlyObserved = "WHERE used.name IS NOT NULL"
	}
	rows, err := s.reader().QueryContext(ctx, `
		WITH used AS (
			SELECT turn_components.component_name AS name,
			       COUNT(*) AS count,
			       MAX(turn_components.created_at) AS last_used
			FROM turn_components
			JOIN turns ON turns.id = turn_components.turn_id
			JOIN sessions ON sessions.id = turns.session_id
			`+usedClause+`
			GROUP BY component_name
		),
		installed_ranked AS (
			SELECT name, scope, source_path, description, version, argument_hint,
			       user_invocable, triggers, allowed_tools, tools, compatibility, license,
			       ROW_NUMBER() OVER (PARTITION BY name ORDER BY scope, source_path) AS rn
			FROM skills
		),
		installed AS (
			-- One winning row per name so every attribute comes from the same
			-- skill row rather than independent MAX() across scopes/paths.
			SELECT name, scope, source_path, description, version, argument_hint,
			       user_invocable, triggers, allowed_tools, tools, compatibility, license
			FROM installed_ranked
			WHERE rn = 1
		)
		SELECT COALESCE(used.name, installed.name) AS name,
		       COALESCE(installed.scope, '') AS scope,
		       COALESCE(installed.source_path, '') AS source_path,
		       COALESCE(installed.description, '') AS description,
		       COALESCE(installed.version, '') AS version,
		       COALESCE(installed.argument_hint, '') AS argument_hint,
		       installed.user_invocable AS user_invocable,
		       installed.triggers AS triggers,
		       installed.allowed_tools AS allowed_tools,
		       installed.tools AS tools,
		       COALESCE(installed.compatibility, '') AS compatibility,
		       COALESCE(installed.license, '') AS license,
		       CASE WHEN installed.name IS NULL THEN 0 ELSE 1 END AS installed,
		       COALESCE(used.count, 0) AS count,
		       COALESCE(used.last_used, '') AS last_used
		FROM used
		FULL OUTER JOIN installed ON installed.name = used.name
		`+onlyObserved+`
		ORDER BY count DESC, installed DESC, name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()
	var out []SkillListItem
	for rows.Next() {
		var item SkillListItem
		var lastUsed sql.NullString
		var userInvocable sql.NullInt64
		var triggers, allowedTools, tools sql.NullString
		var installed int
		if err := rows.Scan(
			&item.Name, &item.Scope, &item.SourcePath,
			&item.Description, &item.Version, &item.ArgumentHint, &userInvocable,
			&triggers, &allowedTools, &tools,
			&item.Compatibility, &item.License,
			&installed, &item.InferredUsedCount, &lastUsed,
		); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		item.Installed = installed == 1
		item.LastUsedAt = parseTimeOpt(lastUsed)
		item.UserInvocable = nullInt64ToBoolPtr(userInvocable)
		item.Triggers = nullStringToJSON(triggers)
		item.AllowedTools = nullStringToJSON(allowedTools)
		item.Tools = nullStringToJSON(tools)
		out = append(out, item)
	}
	return out, rows.Err()
}

func nullInt64ToBoolPtr(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	return new(v.Int64 != 0)
}

func nullStringToJSON(v sql.NullString) json.RawMessage {
	if !v.Valid || v.String == "" {
		return nil
	}
	return json.RawMessage(v.String)
}

func (s *Store) ListUnusedSkills(ctx context.Context) ([]SkillListItem, error) {
	rows, err := s.reader().QueryContext(ctx, `
		SELECT skills.name,
		       COALESCE(skills.scope, ''),
		       COALESCE(skills.source_path, ''),
		       COALESCE(skills.description, ''),
		       COALESCE(skills.version, ''),
		       COALESCE(skills.argument_hint, ''),
		       skills.user_invocable,
		       skills.triggers,
		       skills.allowed_tools,
		       skills.tools,
		       COALESCE(skills.compatibility, ''),
		       COALESCE(skills.license, '')
		FROM skills
		WHERE skills.name NOT IN (
			SELECT DISTINCT component_name
			FROM turn_components
			WHERE component_kind = 'skill'
		)
		ORDER BY skills.name
	`)
	if err != nil {
		return nil, fmt.Errorf("list unused skills: %w", err)
	}
	defer rows.Close()
	var out []SkillListItem
	for rows.Next() {
		var item SkillListItem
		var userInvocable sql.NullInt64
		var triggers, allowedTools, tools sql.NullString
		if err := rows.Scan(
			&item.Name, &item.Scope, &item.SourcePath,
			&item.Description, &item.Version, &item.ArgumentHint, &userInvocable,
			&triggers, &allowedTools, &tools,
			&item.Compatibility, &item.License,
		); err != nil {
			return nil, fmt.Errorf("scan unused skill: %w", err)
		}
		item.UserInvocable = nullInt64ToBoolPtr(userInvocable)
		item.Triggers = nullStringToJSON(triggers)
		item.AllowedTools = nullStringToJSON(allowedTools)
		item.Tools = nullStringToJSON(tools)
		item.Installed = true
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListComponentsForTurn(ctx context.Context, turnID string) ([]trace.TurnComponent, error) {
	byTurn, err := s.loadComponentsForTurns(ctx, []string{turnID})
	if err != nil {
		return nil, err
	}
	return byTurn[turnID], nil
}

func (s *Store) ListSuggestions(ctx context.Context, ruleID string) ([]trace.Suggestion, error) {
	var args []any
	clause := ""
	if ruleID != "" {
		clause = " WHERE rule_id = ?"
		args = append(args, ruleID)
	}
	rows, err := s.reader().QueryContext(ctx, `
		SELECT id, rule_id, severity, confidence, COALESCE(scope_kind, ''), COALESCE(scope_id, ''),
		       COALESCE(evidence_json, ''), COALESCE(recommendation, '')
		FROM rule_suggestions
		`+clause+`
		ORDER BY id DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list suggestions: %w", err)
	}
	defer rows.Close()
	var out []trace.Suggestion
	for rows.Next() {
		var sug trace.Suggestion
		var confidence string
		if err := rows.Scan(&sug.ID, &sug.RuleID, &sug.Severity, &confidence, &sug.ScopeKind, &sug.ScopeID, &sug.EvidenceJSON, &sug.Recommendation); err != nil {
			return nil, fmt.Errorf("scan suggestion: %w", err)
		}
		sug.Confidence = trace.Confidence(confidence)
		out = append(out, sug)
	}
	return out, rows.Err()
}

func (s *Store) ReplaceSuggestions(ctx context.Context, suggestions []trace.Suggestion) error {
	tx, err := s.writer().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin suggestions tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rule_suggestions`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear suggestions: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO rule_suggestions(rule_id, severity, confidence, scope_kind, scope_id, evidence_json, recommendation, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare suggestions: %w", err)
	}
	now := nowUTC()
	for _, sug := range suggestions {
		if _, err := stmt.ExecContext(ctx, sug.RuleID, sug.Severity, string(sug.Confidence), sug.ScopeKind, sug.ScopeID, sug.EvidenceJSON, sug.Recommendation, now); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("insert suggestion: %w", err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("close suggestion stmt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit suggestions: %w", err)
	}
	return nil
}

func (s *Store) componentAvailability(ctx context.Context, kind string, f Filter) (map[string]int, error) {
	// Apply the same runtime constraints as the MCP listing so the reported
	// availability_observed counts match the requested source/session/project/
	// time scope instead of being a global tally across every window.
	args := []any{kind}
	wheres := append([]string{`turn_components.component_kind = ?`}, f.joinedTurnWhere(&args)...)
	if !f.Since.IsZero() {
		wheres = append(wheres, `turn_components.created_at >= ?`)
		args = append(args, timeBound(f.Since))
	}
	rows, err := s.reader().QueryContext(ctx, `
		SELECT turn_components.component_name, COUNT(*)
		FROM turn_components
		JOIN turns ON turns.id = turn_components.turn_id
		JOIN sessions ON sessions.id = turns.session_id
		`+whereClause(wheres)+`
		GROUP BY turn_components.component_name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("component availability: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		out[name] = count
	}
	return out, rows.Err()
}

func (f Filter) sessionWhere() (string, []any) {
	var wheres []string
	var args []any
	if ids := f.sourceIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("sessions.source_id", ids, &args))
	}
	if ids := f.projectIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("sessions.project_id", ids, &args))
	}
	if ids := f.sessionIDs(); len(ids) > 0 {
		internal := inClause("sessions.id", ids, &args)
		external := inClause("sessions.external_session_id", ids, &args)
		wheres = append(wheres, "("+internal+" OR "+external+")")
	}
	if ids := f.statuses(); len(ids) > 0 {
		wheres = append(wheres, inClause("sessions.status", ids, &args))
	}
	if !f.Since.IsZero() {
		wheres = append(wheres, "sessions.started_at >= ?")
		args = append(args, timeBound(f.Since))
	}
	if !f.Until.IsZero() {
		wheres = append(wheres, "sessions.started_at < ?")
		args = append(args, timeBound(f.Until))
	}
	return whereClause(wheres), args
}

func (f Filter) turnWhere() (string, []any) {
	var wheres []string
	var args []any
	if ids := f.sourceIDs(); len(ids) > 0 {
		var subArgs []any
		inner := inClause("source_id", ids, &subArgs)
		wheres = append(wheres, "turns.session_id IN (SELECT id FROM sessions WHERE "+inner+")")
		args = append(args, subArgs...)
	}
	if ids := f.projectIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("turns.project_id", ids, &args))
	}
	if ids := f.sessionIDs(); len(ids) > 0 {
		var subArgs []any
		internal := inClause("id", ids, &subArgs)
		external := inClause("external_session_id", ids, &subArgs)
		wheres = append(wheres, "turns.session_id IN (SELECT id FROM sessions WHERE "+internal+" OR "+external+")")
		args = append(args, subArgs...)
	}
	if ids := f.statuses(); len(ids) > 0 {
		wheres = append(wheres, inClause("turns.status", ids, &args))
	}
	if !f.Since.IsZero() {
		wheres = append(wheres, "turns.started_at >= ?")
		args = append(args, timeBound(f.Since))
	}
	if !f.Until.IsZero() {
		wheres = append(wheres, "turns.started_at < ?")
		args = append(args, timeBound(f.Until))
	}
	return whereClause(wheres), args
}

func (f Filter) rawEventWhere() (string, []any) {
	var wheres []string
	var args []any
	if ids := f.sourceIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("raw_events.source_id", ids, &args))
	}
	if ids := f.sessionIDs(); len(ids) > 0 {

		var subArgs []any
		external := inClause("raw_events.session_external_id", ids, &subArgs)
		internal := inClause("id", ids, &subArgs)
		wheres = append(wheres,
			"("+external+" OR raw_events.session_external_id IN (SELECT external_session_id FROM sessions WHERE "+internal+"))")
		args = append(args, subArgs...)
	}
	if !f.Since.IsZero() {
		wheres = append(wheres, "raw_events.event_time >= ?")
		args = append(args, timeBound(f.Since))
	}
	if !f.Until.IsZero() {
		wheres = append(wheres, "raw_events.event_time < ?")
		args = append(args, timeBound(f.Until))
	}
	return whereClause(wheres), args
}

func (f Filter) parseErrorWhere() (string, []any) {
	var wheres []string
	var args []any
	if ids := f.sourceIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("parse_errors.source_id", ids, &args))
	}
	if !f.Since.IsZero() {
		wheres = append(wheres, "parse_errors.created_at >= ?")
		args = append(args, timeBound(f.Since))
	}
	if !f.Until.IsZero() {
		wheres = append(wheres, "parse_errors.created_at < ?")
		args = append(args, timeBound(f.Until))
	}
	return whereClause(wheres), args
}

func (f Filter) joinedTurnWhere(args *[]any) []string {
	var wheres []string
	if ids := f.sourceIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("sessions.source_id", ids, args))
	}
	if ids := f.projectIDs(); len(ids) > 0 {
		wheres = append(wheres, inClause("turns.project_id", ids, args))
	}
	if ids := f.sessionIDs(); len(ids) > 0 {
		internal := inClause("sessions.id", ids, args)
		external := inClause("sessions.external_session_id", ids, args)
		wheres = append(wheres, "("+internal+" OR "+external+")")
	}
	if ids := f.statuses(); len(ids) > 0 {
		wheres = append(wheres, inClause("turns.status", ids, args))
	}
	if !f.Until.IsZero() {
		wheres = append(wheres, "turns.started_at < ?")
		*args = append(*args, timeBound(f.Until))
	}
	return wheres
}

func (f Filter) hasRuntimeConstraint() bool {
	return len(f.sourceIDs()) > 0 ||
		len(f.projectIDs()) > 0 ||
		len(f.sessionIDs()) > 0 ||
		len(f.statuses()) > 0 ||
		!f.Since.IsZero() ||
		!f.Until.IsZero()
}

// normalized returns a copy of f whose id/status filter slices are trimmed,
// blank-dropped, and deduped ONCE. Listing methods call it on entry so the
// per-field accessors below are plain field reads instead of re-running
// DedupNonEmpty a dozen times per request (joinedTurnWhere + hasRuntimeConstraint
// + the componentAvailability sub-query all read the same immutable slices).
func (f Filter) normalized() Filter {
	f.SourceIDs = textutil.DedupNonEmpty(f.SourceIDs)
	f.ProjectIDs = textutil.DedupNonEmpty(f.ProjectIDs)
	f.SessionIDs = textutil.DedupNonEmpty(f.SessionIDs)
	f.Statuses = textutil.DedupNonEmpty(f.Statuses)
	return f
}

func (f Filter) sourceIDs() []string  { return f.SourceIDs }
func (f Filter) projectIDs() []string { return f.ProjectIDs }
func (f Filter) sessionIDs() []string { return f.SessionIDs }
func (f Filter) statuses() []string   { return f.Statuses }

func inClause(column string, values []string, args *[]any) string {
	placeholders := make([]string, 0, len(values))
	for _, value := range values {
		placeholders = append(placeholders, "?")
		*args = append(*args, value)
	}
	return column + " IN (" + strings.Join(placeholders, ",") + ")"
}

func whereClause(wheres []string) string {
	if len(wheres) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(wheres, " AND ")
}

func currentStatus(sessionStatus, lastTurnStatus string) string {
	switch {
	case sessionStatus == trace.StatusFailed || lastTurnStatus == trace.StatusFailed:
		return trace.StatusFailed
	case sessionStatus == trace.StatusActive || lastTurnStatus == trace.StatusActive:
		return trace.StatusActive
	case lastTurnStatus != "":
		return lastTurnStatus
	case sessionStatus != "":
		return sessionStatus
	default:
		return trace.StatusUnknown
	}
}

// maxPageSize bounds a single listing page so one request can't pull the whole
// table into memory; page past it with the --offset flag.
const maxPageSize = 500

func (f Filter) pagination(defaultLimit int) (int, int) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	offset := max(f.Offset, 0)
	return limit, offset
}

func (f Filter) turnOrderBy() string {
	switch f.SortBy {
	case "tokens":
		return order("turns.total_input_tokens + turns.total_output_tokens", f.SortDesc)
	case "duration":
		return order("turns.duration_ms", f.SortDesc)
	default:
		return order("turns.started_at", f.SortDesc) + ", turns.turn_index"
	}
}

func (f Filter) sessionOrderBy() string {
	switch f.SortBy {
	case "turns":
		return order("sessions.total_turns", f.SortDesc)
	default:
		return order("sessions.started_at", f.SortDesc) + ", sessions.id"
	}
}

func order(expr string, desc bool) string {
	if desc {
		return expr + " DESC"
	}
	return expr + " ASC"
}

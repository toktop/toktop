package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"toktop.unceas.dev/internal/trace"
)

// LoadIndex materializes the trace index. A non-zero since selects sessions by
// session effective time, then loads every turn for those sessions. That keeps
// parent sessions and child turns coherent for scoped export/rule snapshots
// instead of independently clipping turns by their own timestamps.
func (s *Store) LoadIndex(ctx context.Context, since time.Time) (trace.Index, error) {
	index := trace.Index{GeneratedAt: time.Now().UTC()}
	if err := s.loadProvidersAndRoots(ctx, &index); err != nil {
		return trace.Index{}, err
	}
	rawCountQuery := `SELECT COUNT(*) FROM raw_events`
	var rawCountArgs []any
	if !since.IsZero() {
		rawCountQuery += ` WHERE ` + rawEventEffectiveTimeExpr + ` >= ?`
		rawCountArgs = append(rawCountArgs, timeBound(since))
	}
	rawCount, err := s.scalarInt(ctx, rawCountQuery, rawCountArgs...)
	if err != nil {
		return trace.Index{}, err
	}
	index.RawEventCount = rawCount

	parseErrors, err := s.loadParseErrors(ctx, since)
	if err != nil {
		return trace.Index{}, err
	}
	index.ParseErrorList = parseErrors

	sessions, err := s.loadAllSessions(ctx, since)
	if err != nil {
		return trace.Index{}, err
	}
	index.Sessions = sessions

	turns, err := s.loadAllTurns(ctx, since)
	if err != nil {
		return trace.Index{}, err
	}
	index.Turns = turns

	if err := s.attachChildrenToTurns(ctx, turns); err != nil {
		return trace.Index{}, err
	}
	for i := range turns {
		index.Invocations = append(index.Invocations, turns[i].Invocations...)
		index.TurnComponents = append(index.TurnComponents, turns[i].Components...)
	}

	// MCP servers feed the mcp_unused_30d rule; skills round out /v1/export.
	// These are config/metadata tables, not time-series, so they load in full
	// regardless of `since`.
	mcpServers, err := s.loadAllMCPServers(ctx)
	if err != nil {
		return trace.Index{}, err
	}
	index.MCPServers = mcpServers

	skills, err := s.loadAllSkills(ctx)
	if err != nil {
		return trace.Index{}, err
	}
	index.Skills = skills

	index.SessionCount = len(sessions)
	index.TurnCount = len(turns)
	index.InvocationCount = len(index.Invocations)
	count := 0
	for _, turn := range turns {
		count += len(turn.ToolCalls)
	}
	index.ToolCallCount = count
	return index, nil
}

func (s *Store) GetTurn(ctx context.Context, turnID string) (trace.Turn, error) {
	rows, err := s.reader().QueryContext(ctx, turnsBaseQuery+` WHERE turns.id = ?`, turnID)
	if err != nil {
		return trace.Turn{}, fmt.Errorf("query turn: %w", err)
	}
	turns, err := scanTurns(rows)
	if err != nil {
		return trace.Turn{}, err
	}
	if len(turns) == 0 {
		return trace.Turn{}, sql.ErrNoRows
	}
	if err := s.attachChildrenToTurns(ctx, turns[:1]); err != nil {
		return trace.Turn{}, err
	}
	return turns[0], nil
}

func (s *Store) GetSession(ctx context.Context, id string) (trace.Session, error) {
	sessions, err := s.FindSessions(ctx, id)
	if err != nil {
		return trace.Session{}, err
	}
	if len(sessions) == 0 {
		return trace.Session{}, sql.ErrNoRows
	}
	return sessions[0], nil
}

func (s *Store) FindSessions(ctx context.Context, id string) ([]trace.Session, error) {
	rows, err := s.reader().QueryContext(ctx, sessionsBaseQuery+`
		WHERE sessions.id = ? OR sessions.external_session_id = ?
		ORDER BY (sessions.id = ?) DESC, sessions.id`, id, id, id)
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}
	sessions, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *Store) TurnsForSession(ctx context.Context, sessionID string) ([]trace.Turn, error) {
	rows, err := s.reader().QueryContext(ctx, turnsBaseQuery+` WHERE session_id = ? ORDER BY turn_index`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query turns for session: %w", err)
	}
	turns, err := scanTurns(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachChildrenToTurns(ctx, turns); err != nil {
		return nil, err
	}
	return turns, nil
}

func (s *Store) loadProvidersAndRoots(ctx context.Context, index *trace.Index) error {
	rows, err := s.reader().QueryContext(ctx, `
		SELECT sources.kind, source_roots.path
		FROM source_roots
		JOIN sources ON sources.id = source_roots.source_id
		ORDER BY sources.kind, source_roots.path
	`)
	if err != nil {
		return fmt.Errorf("load source roots: %w", err)
	}
	defer rows.Close()
	providers := make(map[string]bool)
	for rows.Next() {
		var kind, path string
		if err := rows.Scan(&kind, &path); err != nil {
			return fmt.Errorf("scan source root: %w", err)
		}
		providers[kind] = true
		index.SourceRoots = append(index.SourceRoots, path)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source roots: %w", err)
	}
	switch len(providers) {
	case 0:
		index.Source = ""
	case 1:
		for provider := range providers {
			index.Source = provider
		}
	default:
		index.Source = "all"
	}
	return nil
}

func (s *Store) loadParseErrors(ctx context.Context, since time.Time) ([]trace.ParseError, error) {
	q := `
		SELECT source_id, COALESCE(source_root_id, ''), COALESCE(source_file, ''),
		       COALESCE(line_no, 0), COALESCE(raw_event_id, ''), message, COALESCE(parser_version, '')
		FROM parse_errors
	`
	var args []any
	if !since.IsZero() {
		q += ` WHERE parse_errors.created_at >= ?`
		args = append(args, timeBound(since))
	}
	q += ` ORDER BY id`
	rows, err := s.reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load parse errors: %w", err)
	}
	defer rows.Close()
	var out []trace.ParseError
	for rows.Next() {
		var e trace.ParseError
		if err := rows.Scan(&e.SourceID, &e.SourceRootID, &e.SourceFile, &e.LineNo, &e.RawEventID, &e.Message, &e.ParserVersion); err != nil {
			return nil, fmt.Errorf("scan parse_error: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const sessionsBaseQuery = `
	SELECT sessions.id, sources.kind, COALESCE(sessions.external_session_id, ''),
	       COALESCE(sessions.project_id, ''),
	       COALESCE(projects.name, ''), COALESCE(projects.path, ''),
	       sessions.transcript_path,
	       COALESCE(sessions.started_at, ''), COALESCE(sessions.ended_at, ''),
	       sessions.status, sessions.total_turns, sessions.total_tool_calls,
	       sessions.total_input_tokens, sessions.total_output_tokens,
	       sessions.cache_read_tokens, sessions.cache_write_tokens, sessions.cache_write_long_tokens
	FROM sessions
	JOIN sources ON sources.id = sessions.source_id
	LEFT JOIN projects ON projects.id = sessions.project_id
`

func (s *Store) loadAllSessions(ctx context.Context, since time.Time) ([]trace.Session, error) {
	q := sessionsBaseQuery
	var args []any
	if !since.IsZero() {
		q += ` WHERE ` + sessionEffectiveTimeExpr + ` >= ?`
		args = append(args, timeBound(since))
	}
	q += ` ORDER BY ` + sessionEffectiveTimeExpr + `, sessions.id`
	rows, err := s.reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	return scanSessions(rows)
}

func scanSessions(rows *sql.Rows) ([]trace.Session, error) {
	defer rows.Close()
	var sessions []trace.Session
	for rows.Next() {
		var session trace.Session
		var startedAt, endedAt sql.NullString
		if err := rows.Scan(
			&session.ID, &session.Provider, &session.ExternalID,
			&session.ProjectID, &session.ProjectName, &session.ProjectPath,
			&session.TranscriptPath,
			&startedAt, &endedAt,
			&session.Status, &session.TurnCount, &session.ToolCallCount,
			&session.Tokens.Input, &session.Tokens.Output,
			&session.Tokens.CacheRead, &session.Tokens.CacheWrite, &session.Tokens.CacheWriteLong,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		session.StartedAt = parseTimeOpt(startedAt)
		session.EndedAt = parseTimeOpt(endedAt)
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

const turnsBaseQuery = `
	SELECT turns.id, sources.kind, turns.session_id, COALESCE(turns.project_id, ''),
	       COALESCE(projects.name, ''), COALESCE(projects.path, ''),
	       COALESCE(sessions.transcript_path, ''),
	       turns.turn_index,
	       COALESCE(turns.user_message, ''),
	       COALESCE(turns.assistant_final, ''),
	       COALESCE(turns.started_at, ''), COALESCE(turns.ended_at, ''), turns.duration_ms,
	       turns.status,
	       turns.invocation_count, turns.tool_call_count,
	       turns.total_input_tokens, turns.total_output_tokens,
	       turns.cache_read_tokens, turns.cache_write_tokens, turns.cache_write_long_tokens
	FROM turns
	JOIN sessions ON sessions.id = turns.session_id
	JOIN sources ON sources.id = sessions.source_id
	LEFT JOIN projects ON projects.id = turns.project_id
`

func (s *Store) loadAllTurns(ctx context.Context, since time.Time) ([]trace.Turn, error) {
	q := turnsBaseQuery
	var args []any
	if !since.IsZero() {
		q += ` WHERE turns.session_id IN (
			SELECT sessions.id
			FROM sessions
			WHERE ` + sessionEffectiveTimeExpr + ` >= ?
		)`
		args = append(args, timeBound(since))
	}
	q += ` ORDER BY ` + turnEffectiveTimeExpr + `, turns.turn_index, turns.id`
	rows, err := s.reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load turns: %w", err)
	}
	return scanTurns(rows)
}

func scanTurns(rows *sql.Rows) ([]trace.Turn, error) {
	defer rows.Close()
	var turns []trace.Turn
	for rows.Next() {
		var turn trace.Turn
		var startedAt, endedAt sql.NullString
		if err := rows.Scan(
			&turn.ID, &turn.Provider, &turn.SessionID, &turn.ProjectID,
			&turn.ProjectName, &turn.ProjectPath,
			&turn.TranscriptPath,
			&turn.Index,
			&turn.UserMessage,
			&turn.AssistantFinal,
			&startedAt, &endedAt, &turn.DurationMs,
			&turn.Status,
			&turn.InvocationCount, &turn.ToolCallCount,
			&turn.Tokens.Input, &turn.Tokens.Output, &turn.Tokens.CacheRead, &turn.Tokens.CacheWrite, &turn.Tokens.CacheWriteLong,
		); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turn.StartedAt = parseTimeOpt(startedAt)
		turn.EndedAt = parseTimeOpt(endedAt)
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

func (s *Store) loadAllMCPServers(ctx context.Context) ([]trace.MCPServer, error) {
	rows, err := s.reader().QueryContext(ctx, `
		SELECT id, source_id, name, scope, transport, COALESCE(config_path, ''), COALESCE(config_hash, ''), enabled
		FROM mcp_servers
		ORDER BY source_id, name, config_path
	`)
	if err != nil {
		return nil, fmt.Errorf("load mcp_servers: %w", err)
	}
	defer rows.Close()
	var out []trace.MCPServer
	for rows.Next() {
		var m trace.MCPServer
		if err := rows.Scan(&m.ID, &m.SourceID, &m.Name, &m.Scope, &m.Transport, &m.ConfigPath, &m.ConfigHash, &m.Enabled); err != nil {
			return nil, fmt.Errorf("scan mcp_server: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) loadAllSkills(ctx context.Context) ([]trace.Skill, error) {
	rows, err := s.reader().QueryContext(ctx, `
		SELECT id, source_id, name, scope, COALESCE(source_path, ''), COALESCE(source_hash, ''),
		       COALESCE(description, ''), COALESCE(version, ''), COALESCE(argument_hint, ''),
		       user_invocable, triggers, allowed_tools, tools,
		       COALESCE(compatibility, ''), COALESCE(license, '')
		FROM skills
		ORDER BY source_id, name, scope, source_path
	`)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	defer rows.Close()
	var out []trace.Skill
	for rows.Next() {
		var skill trace.Skill
		var userInvocable sql.NullInt64
		var triggers, allowedTools, tools sql.NullString
		if err := rows.Scan(
			&skill.ID, &skill.SourceID, &skill.Name, &skill.Scope, &skill.SourcePath, &skill.SourceHash,
			&skill.Description, &skill.Version, &skill.ArgumentHint,
			&userInvocable, &triggers, &allowedTools, &tools,
			&skill.Compatibility, &skill.License,
		); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		skill.UserInvocable = nullInt64ToBoolPtr(userInvocable)
		skill.Triggers = nullStringToJSON(triggers)
		skill.AllowedTools = nullStringToJSON(allowedTools)
		skill.Tools = nullStringToJSON(tools)
		out = append(out, skill)
	}
	return out, rows.Err()
}

func (s *Store) scalarInt(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := s.reader().QueryRowContext(ctx, query, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("scalar query: %w", err)
	}
	return n, nil
}

func parseTimeOpt(value sql.NullString) time.Time {
	if !value.Valid || value.String == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

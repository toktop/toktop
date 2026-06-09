package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"toktop.unceas.dev/internal/collector"
	"toktop.unceas.dev/internal/source"
	"toktop.unceas.dev/internal/trace"
)

func (s *Store) LoadIngestFingerprints(ctx context.Context, sourceID string) (map[string]source.Fingerprint, error) {
	rows, err := s.reader().QueryContext(ctx, `
		SELECT ingest_offsets.source_file, ingest_offsets.size_bytes, ingest_offsets.mtime_ns, ingest_offsets.inode_no
		FROM ingest_offsets
		JOIN source_roots ON source_roots.id = ingest_offsets.source_root_id
		WHERE source_roots.source_id = ?
	`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("load ingest fingerprints: %w", err)
	}
	defer rows.Close()
	out := make(map[string]source.Fingerprint)
	for rows.Next() {
		var (
			file    string
			size    int64
			mtimeNS int64
			ino     uint64
		)
		if err := rows.Scan(&file, &size, &mtimeNS, &ino); err != nil {
			return nil, fmt.Errorf("scan ingest fingerprint: %w", err)
		}
		out[file] = source.Fingerprint{Size: size, MtimeNS: mtimeNS, Ino: ino}
	}
	return out, rows.Err()
}

func (s *Store) SaveIngestPartial(ctx context.Context, index trace.Index, rawEvents []source.RawEvent, processedFiles []string, fingerprints map[string]source.Fingerprint) error {
	return s.saveIngestImpl(ctx, index, rawEvents, processedFiles, fingerprints)
}

func (s *Store) DeleteSourceFiles(ctx context.Context, sourceName string, files []string) (err error) {
	if len(files) == 0 {
		return nil
	}
	tx, err := s.writer().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete-source-files transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = deleteSourceFiles(ctx, tx, trace.SourceID(sourceName), files); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete-source-files transaction: %w", err)
	}
	return nil
}

func (s *Store) saveIngestImpl(ctx context.Context, index trace.Index, rawEvents []source.RawEvent, processedFiles []string, fingerprints map[string]source.Fingerprint) (err error) {
	if index.Source == "" {
		return errors.New("ingest: index.Source is required")
	}
	tx, err := s.writer().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ingest transaction: %w", err)
	}

	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	sourceID := trace.SourceID(index.Source)
	if err = upsertSource(ctx, tx, sourceID, index.Source); err != nil {
		return err
	}
	rootIDs, err := upsertSourceRoots(ctx, tx, sourceID, index.SourceRoots)
	if err != nil {
		return err
	}
	if processedFiles == nil {
		if err = deleteProvider(ctx, tx, sourceID); err != nil {
			return err
		}
	} else {
		if err = deleteSourceFiles(ctx, tx, sourceID, processedFiles); err != nil {
			return err
		}
	}
	if err = insertRawEvents(ctx, tx, sourceID, rootIDs, rawEvents, index.ParserVersion); err != nil {
		return err
	}
	if err = insertProjects(ctx, tx, sourceID, index.Sessions); err != nil {
		return err
	}
	sessionProject, err := insertSessions(ctx, tx, sourceID, rootIDs, index.Sessions, index.ParserVersion)
	if err != nil {
		return err
	}

	if err = insertToolOutputs(ctx, tx, index.ToolOutputs); err != nil {
		return err
	}
	if err = insertTurnsAndChildren(ctx, tx, index, sessionProject); err != nil {
		return err
	}
	if err = updateSessionLastTurn(ctx, tx, index.Turns); err != nil {
		return err
	}
	if err = insertSubagentRuns(ctx, tx, index.SubagentRuns); err != nil {
		return err
	}
	if err = insertContextEvents(ctx, tx, index.ContextEvents); err != nil {
		return err
	}
	if err = insertSkills(ctx, tx, index.Skills); err != nil {
		return err
	}
	if err = insertMCPServers(ctx, tx, index.MCPServers); err != nil {
		return err
	}
	if err = insertParseErrors(ctx, tx, sourceID, index.ParseErrorList); err != nil {
		return err
	}
	if err = insertSearchDocuments(ctx, tx, sourceID, index); err != nil {
		return err
	}
	if err = updateIngestOffsets(ctx, tx, rootIDs, rawEvents, processedFiles, fingerprints); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit ingest transaction: %w", err)
	}
	return nil
}

func (s *Store) SaveSessionIngest(ctx context.Context, index trace.Index, rawEvents []source.RawEvent) (err error) {
	if len(rawEvents) == 0 && len(index.Sessions) == 0 {
		return nil
	}
	if index.Source == "" {
		return errors.New("session ingest: index.Source is required")
	}
	sourceFile := ""
	if len(rawEvents) > 0 {
		sourceFile = rawEvents[0].SourceFile
	} else {
		sourceFile = index.Sessions[0].TranscriptPath
	}

	tx, err := s.writer().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session ingest transaction: %w", err)
	}

	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	sourceID := trace.SourceID(index.Source)
	if err = upsertSource(ctx, tx, sourceID, index.Source); err != nil {
		return err
	}
	rootIDs, err := upsertSourceRoots(ctx, tx, sourceID, index.SourceRoots)
	if err != nil {
		return err
	}
	if err = deleteSourceFile(ctx, tx, sourceID, sourceFile); err != nil {
		return err
	}
	if err = insertRawEvents(ctx, tx, sourceID, rootIDs, rawEvents, index.ParserVersion); err != nil {
		return err
	}
	if err = insertProjects(ctx, tx, sourceID, index.Sessions); err != nil {
		return err
	}
	sessionProject, err := insertSessions(ctx, tx, sourceID, rootIDs, index.Sessions, index.ParserVersion)
	if err != nil {
		return err
	}

	if err = insertToolOutputs(ctx, tx, index.ToolOutputs); err != nil {
		return err
	}
	if err = insertTurnsAndChildren(ctx, tx, index, sessionProject); err != nil {
		return err
	}
	if err = updateSessionLastTurn(ctx, tx, index.Turns); err != nil {
		return err
	}
	if err = insertSubagentRuns(ctx, tx, index.SubagentRuns); err != nil {
		return err
	}
	if err = insertContextEvents(ctx, tx, index.ContextEvents); err != nil {
		return err
	}
	if err = insertParseErrors(ctx, tx, sourceID, index.ParseErrorList); err != nil {
		return err
	}
	if err = insertSearchDocuments(ctx, tx, sourceID, index); err != nil {
		return err
	}

	if err = updateIngestOffsets(ctx, tx, rootIDs, rawEvents, []string{sourceFile}, fingerprintFor(sourceFile)); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit session ingest transaction: %w", err)
	}
	return nil
}

func fingerprintFor(path string) map[string]source.Fingerprint {
	size, mtimeNS, ino, ok := collector.StatFingerprint(path)
	if !ok {
		return nil
	}
	return map[string]source.Fingerprint{path: {Size: size, MtimeNS: mtimeNS, Ino: ino}}
}

func upsertSource(ctx context.Context, tx *sql.Tx, sourceID, kind string) error {
	now := nowUTC()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sources(id, kind, display_name, enabled, capabilities, created_at, updated_at)
		VALUES(?, ?, ?, 1, '', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			updated_at = excluded.updated_at
	`, sourceID, kind, kind, now, now)
	if err != nil {
		return fmt.Errorf("upsert source: %w", err)
	}
	return nil
}

func upsertSourceRoots(ctx context.Context, tx *sql.Tx, sourceID string, roots []string) (map[string]string, error) {
	ids := make(map[string]string, len(roots))
	if len(roots) == 0 {
		return ids, nil
	}
	now := nowUTC()
	// Prepare once, exec per root — consistent with every other multi-row insert
	// in this file (insertRawEvents, insertSessions, …) instead of reparsing the
	// statement each iteration.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO source_roots(id, source_id, path, kind, priority, enabled, last_scan_at, created_at, updated_at)
		VALUES(?, ?, ?, 'manual', 0, 1, ?, ?, ?)
		ON CONFLICT(source_id, path) DO UPDATE SET
			last_scan_at = excluded.last_scan_at,
			updated_at   = excluded.updated_at
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare source_roots: %w", err)
	}
	defer stmt.Close()
	for _, root := range roots {
		id := trace.SourceRootID(sourceID, root)
		ids[root] = id
		if _, err := stmt.ExecContext(ctx, id, sourceID, root, now, now, now); err != nil {
			return nil, fmt.Errorf("upsert source root: %w", err)
		}
	}
	return ids, nil
}

func deleteProvider(ctx context.Context, tx *sql.Tx, sourceID string) error {
	statements := []string{
		`DELETE FROM search_documents WHERE source_id = ?`,
		`DELETE FROM parse_errors WHERE source_id = ?`,
		`DELETE FROM raw_events WHERE source_id = ?`,
		`DELETE FROM ingest_offsets WHERE source_root_id IN (SELECT id FROM source_roots WHERE source_id = ?)`,
		`DELETE FROM sessions WHERE source_id = ?`,
		`DELETE FROM projects WHERE source_id = ?`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt, sourceID); err != nil {
			return fmt.Errorf("delete provider data: %w", err)
		}
	}
	return nil
}

const deleteSourceFileChunk = 400

func deleteSourceFiles(ctx context.Context, tx *sql.Tx, sourceID string, files []string) error {
	for start := 0; start < len(files); start += deleteSourceFileChunk {
		end := min(start+deleteSourceFileChunk, len(files))
		chunk := files[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, sourceID)
		for _, f := range chunk {
			args = append(args, f)
		}
		statements := []string{
			`DELETE FROM search_documents WHERE source_id = ? AND source_file IN (` + placeholders + `)`,
			`DELETE FROM raw_events WHERE source_id = ? AND source_file IN (` + placeholders + `)`,
			`DELETE FROM ingest_offsets WHERE source_root_id IN (SELECT id FROM source_roots WHERE source_id = ?) AND source_file IN (` + placeholders + `)`,
			`DELETE FROM sessions WHERE source_id = ? AND transcript_path IN (` + placeholders + `)`,
			`DELETE FROM parse_errors WHERE source_id = ? AND source_file IN (` + placeholders + `)`,
		}
		for _, stmt := range statements {
			if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
				return fmt.Errorf("delete source files data: %w", err)
			}
		}
	}
	return nil
}

func deleteSourceFile(ctx context.Context, tx *sql.Tx, sourceID, sourceFile string) error {
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM search_documents WHERE source_id = ? AND source_file = ?`, []any{sourceID, sourceFile}},
		{`DELETE FROM raw_events WHERE source_id = ? AND source_file = ?`, []any{sourceID, sourceFile}},
		{`DELETE FROM ingest_offsets WHERE source_file = ? AND source_root_id IN (SELECT id FROM source_roots WHERE source_id = ?)`, []any{sourceFile, sourceID}},
		{`DELETE FROM sessions WHERE source_id = ? AND transcript_path = ?`, []any{sourceID, sourceFile}},
		{`DELETE FROM parse_errors WHERE source_id = ? AND source_file = ?`, []any{sourceID, sourceFile}},
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return fmt.Errorf("delete source file data: %w", err)
		}
	}
	return nil
}

func insertRawEvents(ctx context.Context, tx *sql.Tx, sourceID string, rootIDs map[string]string, events []source.RawEvent, parserVersion string) error {
	if len(events) == 0 {
		return nil
	}
	now := nowUTC()

	// INSERT OR IGNORE makes the UNIQUE(source_root_id, source_file, line_no,
	// raw_hash) constraint the active dedup barrier: re-ingesting an unchanged-
	// but-touched file (fsnotify fires on every save) silently no-ops on already
	// stored rows instead of aborting the whole transaction. The call sites still
	// DELETE-first to drop rows that no longer exist; OR IGNORE is the schema-level
	// safety net for the re-seen rows. Chunked multi-row VALUES preserves the per-
	// row OR IGNORE semantics while collapsing one Exec per event into a handful, so
	// the pipelined writer keeps up with parse+redact (see bulk.go / StreamSessions).
	const prefix = `INSERT OR IGNORE INTO raw_events(
		id, source_id, source_root_id, source_kind, source_file,
		byte_offset, line_no, event_time, session_external_id,
		message_external_id, parent_external_id, event_type, role,
		raw_hash, parser_version, parse_status, parse_error, imported_at)`
	// Constant columns (source_kind, message/parent_external_id, parse_error) stay
	// inline literals rather than binds, keeping this at 14 binds/row as the per-row
	// stmt was (mattn binds one parameter per cgocall).
	const rowGroup = `(?, ?, ?, 'transcript', ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?, '', ?)`
	if err := execRows(ctx, tx, prefix, rowGroup, 14, len(events), "", func(i int) []any {
		event := events[i]
		rootID, ok := rootIDs[event.SourceRoot]
		if !ok {
			rootID = trace.SourceRootID(sourceID, event.SourceRoot)
		}
		rawHash := event.Hash()
		eventID := trace.RawEventID(rootID, event.SourceFile, event.LineNo, rawHash)
		eventTime := timeText(event.EventTime)
		role := ""
		if event.EventType == "user" || event.EventType == "assistant" {
			role = event.EventType
		}
		// Collectors leave EventType empty for lines that failed json.Unmarshal
		// (they only set EventType on a successful decode and additionally record
		// a parse_errors row). Reflect that here instead of hardcoding 'parsed',
		// so the parse_status column and idx_raw_events_parse_status are usable.
		parseStatus := "parsed"
		if event.EventType == "" {
			parseStatus = "failed"
		}
		return []any{
			eventID, sourceID, rootID, event.SourceFile,
			event.ByteOffset, event.LineNo, eventTime, event.SessionID,
			event.EventType, role,
			rawHash, parserVersion, parseStatus, now,
		}
	}); err != nil {
		return fmt.Errorf("insert raw events: %w", err)
	}
	return nil
}

func updateIngestOffsets(ctx context.Context, tx *sql.Tx, rootIDs map[string]string, events []source.RawEvent, processedFiles []string, fingerprints map[string]source.Fingerprint) error {
	if len(events) == 0 && len(processedFiles) == 0 {
		return nil
	}
	type position struct {
		rootID, file string
		line         int
		hash         string
	}

	latest := make(map[string]position)
	for _, event := range events {
		current, ok := latest[event.SourceFile]
		if !ok || event.LineNo > current.line {
			rootID, found := rootIDs[event.SourceRoot]
			if !found {
				continue
			}
			latest[event.SourceFile] = position{rootID: rootID, file: event.SourceFile, line: event.LineNo, hash: event.Hash()}
		}
	}

	for _, file := range processedFiles {
		if _, ok := latest[file]; ok {
			continue
		}
		rootID, ok := rootIDForFile(rootIDs, file)
		if !ok {
			continue
		}
		latest[file] = position{rootID: rootID, file: file, line: 0, hash: ""}
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO ingest_offsets(id, source_root_id, source_file, size_bytes, mtime_ns, inode_no, line_no, last_hash, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_root_id, source_file) DO UPDATE SET
			size_bytes = excluded.size_bytes,
			mtime_ns   = excluded.mtime_ns,
			inode_no   = excluded.inode_no,
			line_no    = excluded.line_no,
			last_hash  = excluded.last_hash,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return fmt.Errorf("prepare ingest_offsets: %w", err)
	}
	defer stmt.Close()
	for _, pos := range latest {
		id := trace.SourceRootID(pos.rootID, pos.file)
		fp := fingerprints[pos.file]
		_, err := stmt.ExecContext(ctx, id, pos.rootID, pos.file, fp.Size, fp.MtimeNS, fp.Ino, pos.line, pos.hash, now)
		if err != nil {
			return fmt.Errorf("upsert ingest offset: %w", err)
		}
	}
	return nil
}

func insertProjects(ctx context.Context, tx *sql.Tx, sourceID string, sessions []trace.Session) error {
	if len(sessions) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO projects(id, source_id, source_root_id, name, path, path_hash, created_at, updated_at)
		VALUES(?, ?, NULL, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, name, path) DO UPDATE SET
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return fmt.Errorf("prepare projects: %w", err)
	}
	defer stmt.Close()
	seen := make(map[string]struct{})
	for _, session := range sessions {
		if session.ProjectName == "" {
			continue
		}
		id := trace.ProjectID(sourceID, session.ProjectName, session.ProjectPath)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, err := stmt.ExecContext(ctx, id, sourceID, session.ProjectName, session.ProjectPath, trace.HashPayload([]byte(session.ProjectPath)), now, now); err != nil {
			return fmt.Errorf("insert project: %w", err)
		}
	}
	return nil
}

func insertSessions(ctx context.Context, tx *sql.Tx, sourceID string, rootIDs map[string]string, sessions []trace.Session, parserVersion string) (map[string]string, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sessions(
			id, source_id, source_root_id, project_id,
			external_session_id, transcript_path, started_at, ended_at, status,
			total_turns, total_tool_calls,
			total_input_tokens, total_output_tokens,
			cache_read_tokens, cache_write_tokens,
			parser_version, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare sessions: %w", err)
	}
	defer stmt.Close()
	sessionProject := make(map[string]string, len(sessions))
	for _, session := range sessions {
		rootID := lookupRootID(rootIDs, session)
		projectID := trace.ProjectID(sourceID, session.ProjectName, session.ProjectPath)
		sessionProject[session.ID] = projectID
		_, err := stmt.ExecContext(ctx,
			session.ID, sourceID, sqlNullStr(rootID), sqlNullStr(projectID),
			session.ExternalID, session.TranscriptPath,
			timeText(session.StartedAt), timeText(session.EndedAt), session.Status,
			session.TurnCount, session.ToolCallCount,
			session.Tokens.Input, session.Tokens.Output,
			session.Tokens.CacheRead, session.Tokens.CacheWrite,
			parserVersion, now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert session %s: %w", session.ID, err)
		}
	}
	return sessionProject, nil
}

func insertTurnsAndChildren(ctx context.Context, tx *sql.Tx, index trace.Index, sessionProject map[string]string) error {
	if len(index.Turns) == 0 {
		return nil
	}
	now := nowUTC()
	turns := index.Turns

	// Flatten children across turns, carrying each child's parent turn.ID so the FK
	// bind survives per-table batching. All turns are inserted before any child
	// table, so the invocations/tool_calls/turn_components → turns FKs are satisfied.
	type childRef[T any] struct {
		turnID string
		row    *T
	}
	var invs []childRef[trace.Invocation]
	var calls []childRef[trace.ToolCall]
	var comps []childRef[trace.TurnComponent]
	for ti := range turns {
		turn := &turns[ti]
		for ci := range turn.Invocations {
			invs = append(invs, childRef[trace.Invocation]{turn.ID, &turn.Invocations[ci]})
		}
		for ci := range turn.ToolCalls {
			calls = append(calls, childRef[trace.ToolCall]{turn.ID, &turn.ToolCalls[ci]})
		}
		for ci := range turn.Components {
			comps = append(comps, childRef[trace.TurnComponent]{turn.ID, &turn.Components[ci]})
		}
	}

	const turnPrefix = `INSERT INTO turns(
		id, session_id, project_id, turn_index,
		user_message, assistant_final, summary,
		started_at, ended_at, duration_ms, status, failure_reason,
		invocation_count, tool_call_count, subagent_count,
		total_input_tokens, total_output_tokens, cache_read_tokens, cache_write_tokens,
		created_at, updated_at)`
	if err := execRows(ctx, tx, turnPrefix, placeholders(21), 21, len(turns), "", func(i int) []any {
		turn := &turns[i]
		return []any{
			turn.ID, turn.SessionID, sqlNullStr(sessionProject[turn.SessionID]), turn.Index,
			turn.UserMessage, turn.AssistantFinal, turn.Summary,
			timeText(turn.StartedAt), timeText(turn.EndedAt), turn.DurationMs, turn.Status, turn.FailureReason,
			turn.InvocationCount, turn.ToolCallCount, turn.SubagentCount,
			turn.Tokens.Input, turn.Tokens.Output, turn.Tokens.CacheRead, turn.Tokens.CacheWrite,
			now, now,
		}
	}); err != nil {
		return fmt.Errorf("insert turns: %w", err)
	}

	const invPrefix = `INSERT INTO invocations(
		id, turn_id, session_id, subagent_run_id, invocation_index,
		provider, model,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, context_window_tokens,
		started_at, ended_at, latency_ms, stop_reason, status, raw_event_id, created_at)`
	if err := execRows(ctx, tx, invPrefix, placeholders(19), 19, len(invs), "", func(i int) []any {
		inv := invs[i].row
		return []any{
			inv.ID, invs[i].turnID, inv.SessionID, sqlNullStr(inv.SubagentRunID), inv.Index,
			inv.Provider, inv.Model,
			inv.Tokens.Input, inv.Tokens.Output, inv.Tokens.CacheRead, inv.Tokens.CacheWrite, sqlNullInt(inv.ContextWindowTokens),
			timeText(inv.StartedAt), timeText(inv.EndedAt), inv.LatencyMs, inv.StopReason, inv.Status, sqlNullStr(inv.RawEventID), now,
		}
	}); err != nil {
		return fmt.Errorf("insert invocations: %w", err)
	}

	const toolPrefix = `INSERT INTO tool_calls(
		id, turn_id, session_id, invocation_id, subagent_run_id, call_index,
		tool_kind, tool_name, mcp_server, mcp_tool, use_id,
		input_json, output_text, output_ref, output_bytes,
		status, error, started_at, ended_at, duration_ms,
		raw_use_event_id, raw_result_event_id, created_at)`
	if err := execRows(ctx, tx, toolPrefix, placeholders(23), 23, len(calls), "", func(i int) []any {
		call := calls[i].row
		return []any{
			call.ID, calls[i].turnID, call.SessionID, sqlNullStr(call.InvocationID), sqlNullStr(call.SubagentRunID), call.CallIndex,
			call.Kind, call.Name, sqlNullStr(call.MCPServer), sqlNullStr(call.MCPTool), sqlNullStr(call.UseID),
			call.Input, call.Output, sqlNullStr(call.OutputRef), call.OutputBytes,
			call.Status, call.Error, timeText(call.StartedAt), timeText(call.EndedAt), call.DurationMs,
			sqlNullStr(call.RawUseEventID), sqlNullStr(call.RawResultEventID), now,
		}
	}); err != nil {
		return fmt.Errorf("insert tool calls: %w", err)
	}

	const compPrefix = `INSERT INTO turn_components(turn_id, component_kind, component_id, component_name, relation, token_estimate, evidence, confidence, created_at)`
	if err := execRows(ctx, tx, compPrefix, placeholders(9), 9, len(comps), "", func(i int) []any {
		comp := comps[i].row
		return []any{
			comps[i].turnID, comp.ComponentKind, sqlNullStr(comp.ComponentID), comp.ComponentName, comp.Relation, comp.TokenEstimate, comp.Evidence, string(comp.Confidence), now,
		}
	}); err != nil {
		return fmt.Errorf("insert turn components: %w", err)
	}
	return nil
}

func insertSubagentRuns(ctx context.Context, tx *sql.Tx, runs []trace.SubagentRun) error {
	if len(runs) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO subagent_runs(
			id, parent_turn_id, parent_tool_call_id, agent_name, agent_type, model, transcript_path,
			started_at, ended_at, duration_ms, status,
			total_input_tokens, total_output_tokens, cache_read_tokens, cache_write_tokens,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare subagent_runs: %w", err)
	}
	defer stmt.Close()
	for _, run := range runs {
		_, err = stmt.ExecContext(ctx,
			run.ID, run.ParentTurnID, sqlNullStr(run.ParentToolCallID),
			run.AgentName, run.AgentType, run.Model, run.TranscriptPath,
			timeText(run.StartedAt), timeText(run.EndedAt), run.DurationMs, run.Status,
			run.Tokens.Input, run.Tokens.Output, run.Tokens.CacheRead, run.Tokens.CacheWrite,
			now, now,
		)
		if err != nil {
			return fmt.Errorf("insert subagent_run: %w", err)
		}
	}
	return nil
}

func insertToolOutputs(ctx context.Context, tx *sql.Tx, outputs []trace.ToolOutput) error {
	if len(outputs) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tool_outputs(id, source_file, content_text, content_hash, size_bytes, retention_class, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(content_hash) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("prepare tool_outputs: %w", err)
	}
	defer stmt.Close()
	for _, output := range outputs {
		_, err = stmt.ExecContext(ctx,
			output.ID, output.SourceFile, output.ContentText, output.ContentHash, output.SizeBytes, output.RetentionClass, now,
		)
		if err != nil {
			return fmt.Errorf("insert tool_output: %w", err)
		}
	}
	return nil
}

func insertContextEvents(ctx context.Context, tx *sql.Tx, events []trace.ContextEvent) error {
	if len(events) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO context_events(
			id, session_id, turn_id, invocation_id, subagent_run_id,
			component_type, component_name, source_path, source_hash, phase,
			token_estimate, evidence, confidence, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare context_events: %w", err)
	}
	defer stmt.Close()
	for _, event := range events {
		_, err = stmt.ExecContext(ctx,
			event.ID, sqlNullStr(event.SessionID), sqlNullStr(event.TurnID),
			sqlNullStr(event.InvocationID), sqlNullStr(event.SubagentRunID),
			event.ComponentType, event.ComponentName, event.SourcePath, event.SourceHash, event.Phase,
			event.TokenEstimate, event.Evidence, string(event.Confidence), now,
		)
		if err != nil {
			return fmt.Errorf("insert context_event: %w", err)
		}
	}
	return nil
}

func insertSkills(ctx context.Context, tx *sql.Tx, skills []trace.Skill) error {
	if len(skills) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO skills(
			id, name, scope, source_path, source_hash,
			description, version, argument_hint, user_invocable,
			triggers, allowed_tools, tools, compatibility, license,
			created_at, updated_at
		)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name, scope, source_path) DO UPDATE SET
			source_hash    = excluded.source_hash,
			description    = excluded.description,
			version        = excluded.version,
			argument_hint  = excluded.argument_hint,
			user_invocable = excluded.user_invocable,
			triggers       = excluded.triggers,
			allowed_tools  = excluded.allowed_tools,
			tools          = excluded.tools,
			compatibility  = excluded.compatibility,
			license        = excluded.license,
			updated_at     = excluded.updated_at
	`)
	if err != nil {
		return fmt.Errorf("prepare skills: %w", err)
	}
	defer stmt.Close()
	for _, skill := range skills {
		_, err = stmt.ExecContext(ctx,
			skill.ID, skill.Name, skill.Scope, skill.SourcePath, skill.SourceHash,
			skill.Description, skill.Version, skill.ArgumentHint, nullableBool(skill.UserInvocable),
			nullableJSON(skill.Triggers), nullableJSON(skill.AllowedTools), nullableJSON(skill.Tools), skill.Compatibility, skill.License,
			now, now,
		)
		if err != nil {
			return fmt.Errorf("insert skill: %w", err)
		}
	}
	return nil
}

func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func insertMCPServers(ctx context.Context, tx *sql.Tx, servers []trace.MCPServer) error {
	if len(servers) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO mcp_servers(id, name, scope, transport, config_path, config_hash, enabled, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name, scope, config_path) DO UPDATE SET
			transport   = excluded.transport,
			config_hash = excluded.config_hash,
			enabled     = excluded.enabled,
			updated_at  = excluded.updated_at
	`)
	if err != nil {
		return fmt.Errorf("prepare mcp_servers: %w", err)
	}
	defer stmt.Close()
	for _, server := range servers {
		enabled := 0
		if server.Enabled {
			enabled = 1
		}
		_, err = stmt.ExecContext(ctx, server.ID, server.Name, server.Scope, server.Transport, server.ConfigPath, server.ConfigHash, enabled, now, now)
		if err != nil {
			return fmt.Errorf("insert mcp_server: %w", err)
		}
	}
	return nil
}

func insertParseErrors(ctx context.Context, tx *sql.Tx, sourceID string, errors []trace.ParseError) error {
	if len(errors) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO parse_errors(source_id, source_root_id, source_file, line_no, raw_event_id, message, parser_version, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare parse_errors: %w", err)
	}
	defer stmt.Close()
	for _, parseErr := range errors {
		sid := parseErr.SourceID
		if sid == "" {
			sid = sourceID
		}
		_, err = stmt.ExecContext(ctx,
			sid, sqlNullStr(parseErr.SourceRootID), parseErr.SourceFile, parseErr.LineNo,
			sqlNullStr(parseErr.RawEventID), parseErr.Message, parseErr.ParserVersion, now,
		)
		if err != nil {
			return fmt.Errorf("insert parse_error: %w", err)
		}
	}
	return nil
}

func insertSearchDocuments(ctx context.Context, tx *sql.Tx, sourceID string, index trace.Index) error {
	// Fields are already redacted-or-raw in place by redact.ApplyToIndex, so index
	// them directly. The document text is concatenated in SQL (`? || ' ' || ?`)
	// rather than in Go: the joined per-row string — roughly the whole FTS payload —
	// is never materialized host-side (it was the bulk of this function's allocation),
	// and `||` over the bound TEXT values yields a byte-identical result to the
	// previous `a + " " + b`. Turn docs and tool-call docs are inserted in two
	// batched passes (one row shape each); this reassigns search_documents.rowid
	// relative to the old interleaved order, which is internally consistent (the FTS
	// trigger keys off new.rowid) and unobservable to queries keyed off id/kind.
	turns := index.Turns
	const prefix = `INSERT INTO search_documents(kind, id, source_id, session_id, turn_id, source_file, text)`

	if err := execRows(ctx, tx, prefix, "('turn', ?, ?, ?, ?, ?, ? || ' ' || ?)", 7, len(turns), "", func(i int) []any {
		turn := &turns[i]
		return []any{turn.ID, sourceID, turn.SessionID, turn.ID, turn.TranscriptPath, turn.UserMessage, turn.AssistantFinal}
	}); err != nil {
		return fmt.Errorf("index turn search: %w", err)
	}

	type callDoc struct {
		sessionID, turnID, transcriptPath string
		call                              *trace.ToolCall
	}
	var calls []callDoc
	for ti := range turns {
		turn := &turns[ti]
		for ci := range turn.ToolCalls {
			calls = append(calls, callDoc{turn.SessionID, turn.ID, turn.TranscriptPath, &turn.ToolCalls[ci]})
		}
	}
	if err := execRows(ctx, tx, prefix, "('tool_call', ?, ?, ?, ?, ?, ? || ' ' || ? || ' ' || ?)", 8, len(calls), "", func(i int) []any {
		d := calls[i]
		return []any{d.call.ID, sourceID, d.sessionID, d.turnID, d.transcriptPath, d.call.Name, d.call.Input, d.call.Output}
	}); err != nil {
		return fmt.Errorf("index tool_call search: %w", err)
	}
	return nil
}

func sqlNullStr(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sqlNullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

// timeLayout is a fixed-width canonical timestamp layout. Unlike
// time.RFC3339Nano (which strips trailing zeros), the nanosecond field is
// always rendered at full width, so lexicographic TEXT comparison in SQL
// (retention cutoffs, since/until bounds, ORDER BY) matches chronological
// order at sub-second boundaries.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func timeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(timeLayout)
}

func nowUTC() string {
	return time.Now().UTC().Format(timeLayout)
}

// timeBound renders a time.Time for use as a SQL comparison bound against
// columns written via timeText/nowUTC. It must use the same fixed-width layout
// so lexicographic comparison stays chronological.
func timeBound(value time.Time) string {
	return value.UTC().Format(timeLayout)
}

// pathUnderRoot reports whether file lives under root, requiring a path
// separator boundary so that e.g. "/home/u/proj-archive/x" does not match the
// root "/home/u/proj".
func pathUnderRoot(file, root string) bool {
	if root == "" {
		return false
	}
	if file == root {
		return true
	}
	return strings.HasPrefix(file, root+string(os.PathSeparator))
}

func rootIDForFile(rootIDs map[string]string, file string) (string, bool) {
	bestID := ""
	bestLen := -1
	for root, id := range rootIDs {
		if pathUnderRoot(file, root) && len(root) > bestLen {
			bestID, bestLen = id, len(root)
		}
	}
	if bestLen >= 0 {
		return bestID, true
	}
	for _, id := range rootIDs {
		return id, true
	}
	return "", false
}

func lookupRootID(rootIDs map[string]string, session trace.Session) string {
	bestID := ""
	bestLen := -1
	for root, id := range rootIDs {
		if pathUnderRoot(session.TranscriptPath, root) && len(root) > bestLen {
			bestID, bestLen = id, len(root)
		}
	}
	if bestLen >= 0 {
		return bestID
	}
	for _, id := range rootIDs {
		return id
	}
	return ""
}

func updateSessionLastTurn(ctx context.Context, tx *sql.Tx, turns []trace.Turn) error {
	if len(turns) == 0 {
		return nil
	}
	type tip struct {
		index   int
		id      string
		status  string
		endedAt time.Time
		started time.Time
	}
	latest := make(map[string]tip, 64)
	for _, turn := range turns {
		if turn.SessionID == "" {
			continue
		}
		cur, ok := latest[turn.SessionID]
		if !ok || turn.Index > cur.index {
			latest[turn.SessionID] = tip{
				index: turn.Index, id: turn.ID, status: turn.Status,
				endedAt: turn.EndedAt, started: turn.StartedAt,
			}
		}
	}
	if len(latest) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE sessions SET last_turn_id = ?, last_turn_status = ?, last_turn_at = ?
		WHERE id = ?
	`)
	if err != nil {
		return fmt.Errorf("prepare sessions denorm: %w", err)
	}
	defer stmt.Close()
	for sessionID, t := range latest {
		at := timeText(t.endedAt)
		if at == "" {
			at = timeText(t.started)
		}
		if _, err := stmt.ExecContext(ctx, t.id, t.status, sqlNullStr(at), sessionID); err != nil {
			return fmt.Errorf("update sessions.last_turn for %s: %w", sessionID, err)
		}
	}
	return nil
}

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"toktop.unceas.dev/internal/fsx"
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
			ino     int64
		)
		if err := rows.Scan(&file, &size, &mtimeNS, &ino); err != nil {
			return nil, fmt.Errorf("scan ingest fingerprint: %w", err)
		}
		out[file] = source.Fingerprint{Size: size, MtimeNS: mtimeNS, Ino: ino}
	}
	return out, rows.Err()
}

func (s *Store) SaveIngestPartial(ctx context.Context, index trace.Index, rawEvents []source.RawEvent, processedFiles []string, fingerprints map[string]source.Fingerprint, authoritativeSkills, authoritativeMCP bool) error {
	return s.saveIngestImpl(ctx, index, rawEvents, processedFiles, fingerprints, authoritativeSkills, authoritativeMCP)
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

func (s *Store) saveIngestImpl(ctx context.Context, index trace.Index, rawEvents []source.RawEvent, processedFiles []string, fingerprints map[string]source.Fingerprint, authoritativeSkills, authoritativeMCP bool) (err error) {
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
	// is_subagent is denormalized onto turns (by session id) and raw_events (by
	// source file — raw_events.session_external_id is the shared PARENT uuid and
	// cannot tell a subagent's events apart). Derive both maps once from the marked
	// sessions; a session absent from these maps is top-level (is_subagent = 0).
	subagentSessions, subagentFiles := subagentMaps(index.Sessions)
	if err = upsertSource(ctx, tx, sourceID, index.Source); err != nil {
		return err
	}
	rootIDs, err := upsertSourceRoots(ctx, tx, sourceID, index.SourceRoots)
	if err != nil {
		return err
	}
	// Preserve any out-of-band session title (codex's session_index.jsonl thread_name,
	// which the parser leaves empty because it lives only in the DB) across the
	// delete+reinsert below, so a single-file/live reingest of a rollout never wipes a
	// title a prior ingest applied. Captured before the delete, restored after the
	// insert; updateSessionTitles then overrides it with any newer value from the index.
	preservedTitles, err := captureSessionTitles(ctx, tx, index.Sessions)
	if err != nil {
		return err
	}
	// deleteSourceFiles already clears parse_errors for every processed file, so
	// no separate parse-error delete is needed (every parse-error file is also a
	// processed file).
	if err = deleteSourceFiles(ctx, tx, sourceID, processedFiles); err != nil {
		return err
	}
	if err = insertRawEvents(ctx, tx, sourceID, rootIDs, rawEvents, index.ParserVersion, subagentFiles); err != nil {
		return err
	}
	if err = insertProjects(ctx, tx, sourceID, index.Sessions); err != nil {
		return err
	}
	sessionProject, err := insertSessions(ctx, tx, sourceID, rootIDs, index.Sessions, index.ParserVersion)
	if err != nil {
		return err
	}
	if err = resolveSubagentParents(ctx, tx); err != nil {
		return err
	}
	if err = restoreSessionTitles(ctx, tx, preservedTitles); err != nil {
		return err
	}
	if err = updateSessionTitles(ctx, tx, sourceID, index.SessionTitles); err != nil {
		return err
	}

	if err = insertTurnsAndChildren(ctx, tx, index, sessionProject, subagentSessions); err != nil {
		return err
	}
	if err = updateSessionLastTurn(ctx, tx, index.Turns); err != nil {
		return err
	}
	metadataOnly := len(processedFiles) == 0 && len(rawEvents) == 0 && len(index.Sessions) == 0 && len(index.Turns) == 0 && len(index.ParseErrorList) == 0
	if metadataOnly {
		// Reconcile (delete-stale) a metadata kind only when this round
		// authoritatively scanned it: a partial/failed scan must not delete rows,
		// and one kind's scan failure must not suppress the other's reconcile.
		if authoritativeSkills {
			if err = reconcileSkills(ctx, tx, sourceID, index.Skills); err != nil {
				return err
			}
		}
		if authoritativeMCP {
			if err = reconcileMCPServers(ctx, tx, sourceID, index.MCPServers); err != nil {
				return err
			}
		}
	} else {
		if err = insertSkills(ctx, tx, sourceID, index.Skills); err != nil {
			return err
		}
		if err = insertMCPServers(ctx, tx, sourceID, index.MCPServers); err != nil {
			return err
		}
	}
	if err = insertParseErrors(ctx, tx, sourceID, index.ParseErrorList, subagentFiles); err != nil {
		return err
	}
	if err = insertSearchDocuments(ctx, tx, sourceID, index, subagentSessions); err != nil {
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

func (s *Store) SaveSessionIngest(ctx context.Context, index trace.Index, rawEvents []source.RawEvent, fingerprints map[string]source.Fingerprint) error {
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
	// Session/file ingest is never a metadata-only round, so skill/MCP reconcile
	// never fires here — pass false for both authoritative flags.
	return s.saveIngestImpl(ctx, index, rawEvents, []string{sourceFile}, fingerprints, false, false)
}

func upsertSource(ctx context.Context, tx *sql.Tx, sourceID, kind string) error {
	now := nowUTC()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sources(id, kind, created_at, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			updated_at = excluded.updated_at
	`, sourceID, kind, now, now)
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
		INSERT INTO source_roots(id, source_id, path, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(source_id, path) DO UPDATE SET
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare source_roots: %w", err)
	}
	defer stmt.Close()
	for _, root := range roots {
		id := trace.SourceRootID(sourceID, root)
		ids[root] = id
		if _, err := stmt.ExecContext(ctx, id, sourceID, root, now, now); err != nil {
			return nil, fmt.Errorf("upsert source root: %w", err)
		}
	}
	return ids, nil
}

const deleteSourceFileChunk = 400

func deleteSourceFiles(ctx context.Context, tx *sql.Tx, sourceID string, files []string) error {
	for start := 0; start < len(files); start += deleteSourceFileChunk {
		end := min(start+deleteSourceFileChunk, len(files))
		chunk := files[start:end]
		placeholders := bindMarkers(len(chunk))
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

func insertRawEvents(ctx context.Context, tx *sql.Tx, sourceID string, rootIDs map[string]string, events []source.RawEvent, parserVersion string, subagentFiles map[string]bool) error {
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
		byte_offset, line_no, event_time, session_external_id, is_subagent,
		message_external_id, parent_external_id, event_type, role,
		raw_hash, parser_version, parse_status, parse_error, imported_at)`
	// Constant columns (source_kind, message/parent_external_id, parse_error) stay
	// inline literals rather than binds, keeping this at 15 binds/row (mattn binds
	// one parameter per cgocall). is_subagent must be a bind, not a constant: a
	// batch mixes top-level and subagent files, and a subagent's events carry the
	// shared PARENT external id, so only the source_file distinguishes them.
	const rowGroup = `(?, ?, ?, 'transcript', ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, ?, ?, '', ?)`
	for _, event := range events {
		if _, ok := rootIDs[event.SourceRoot]; !ok {
			return fmt.Errorf("raw event source root %q has no source_roots row", event.SourceRoot)
		}
	}
	if err := execRows(ctx, tx, prefix, rowGroup, 15, len(events), func(i int) []any {
		event := events[i]
		rootID := rootIDs[event.SourceRoot]
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
			event.ByteOffset, event.LineNo, eventTime, event.SessionID, boolInt(subagentFiles[event.SourceFile]),
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
		fp, ok := fingerprints[pos.file]
		if !ok {
			continue
		}
		id := trace.SourceRootID(pos.rootID, pos.file)
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
		INSERT INTO projects(id, source_id, source_root_id, name, path, created_at, updated_at)
		VALUES(?, ?, NULL, ?, ?, ?, ?)
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
		if _, err := stmt.ExecContext(ctx, id, sourceID, session.ProjectName, session.ProjectPath, now, now); err != nil {
			return fmt.Errorf("insert project: %w", err)
		}
	}
	return nil
}

// subagentMaps derives, from the batch's sessions, the lookups the denormalized
// is_subagent writes need: session id → true and transcript path → true, each
// holding only the subagent sessions (a key's absence means top-level).
func subagentMaps(sessions []trace.Session) (byID, byFile map[string]bool) {
	byID = make(map[string]bool)
	byFile = make(map[string]bool)
	for _, s := range sessions {
		if s.IsSubagent {
			byID[s.ID] = true
			byFile[s.TranscriptPath] = true
		}
	}
	return byID, byFile
}

func insertSessions(ctx context.Context, tx *sql.Tx, sourceID string, rootIDs map[string]string, sessions []trace.Session, parserVersion string) (map[string]string, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sessions(
			id, source_id, source_root_id, project_id,
			external_session_id, title, transcript_path, started_at, ended_at, status,
			total_turns, total_tool_calls,
			total_input_tokens, total_output_tokens,
			cache_read_tokens, cache_write_tokens, cache_write_long_tokens,
			is_subagent, parent_external_id, parent_session_id, parent_tool_use_id, workflow_run_id, subagent_kind, agent_type,
			parser_version, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare sessions: %w", err)
	}
	defer stmt.Close()
	sessionProject := make(map[string]string, len(sessions))
	for _, session := range sessions {
		rootID := lookupRootID(rootIDs, session)
		projectID := ""
		if session.ProjectName != "" {
			projectID = trace.ProjectID(sourceID, session.ProjectName, session.ProjectPath)
			sessionProject[session.ID] = projectID
		}
		_, err := stmt.ExecContext(ctx,
			session.ID, sourceID, sqlNullStr(rootID), sqlNullStr(projectID),
			session.ExternalID, sqlNullStr(session.Title), session.TranscriptPath,
			timeText(session.StartedAt), timeText(session.EndedAt), session.Status,
			session.TurnCount, session.ToolCallCount,
			session.Tokens.Input, session.Tokens.Output,
			session.Tokens.CacheRead, session.Tokens.CacheWrite, session.Tokens.CacheWriteLong,
			boolInt(session.IsSubagent), sqlNullStr(session.ParentExternalID), sqlNullStr(session.ParentSessionID), sqlNullStr(session.ParentToolUseID),
			sqlNullStr(session.WorkflowRunID), sqlNullStr(session.SubagentKind), sqlNullStr(session.AgentType),
			parserVersion, now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert session %s: %w", session.ID, err)
		}
	}
	return sessionProject, nil
}

// resolveSubagentParents fills parent_session_id for subagent sessions from their
// parent_external_id, by matching a top-level session's external id within the same
// provider. One mechanism for both providers (claude-code's subagent shares the
// parent's external id; codex carries parent_thread_id). It is run after every
// session insert and only touches still-unresolved rows, so a subagent ingested
// before its parent (a later batch/run) is linked once the parent lands. Idempotent.
// The ORDER BY makes the match deterministic in the rare case several top-level
// sessions share one external id (a resumed/forked session): the earliest wins.
func resolveSubagentParents(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE sessions SET parent_session_id = (
			SELECT p.id FROM sessions p
			WHERE p.external_session_id = sessions.parent_external_id
			  AND p.is_subagent = 0
			  AND p.source_id = sessions.source_id
			ORDER BY p.created_at, p.id
			LIMIT 1
		)
		WHERE is_subagent = 1
		  AND parent_external_id IS NOT NULL AND parent_external_id != ''
		  AND parent_session_id IS NULL
	`)
	if err != nil {
		return fmt.Errorf("resolve subagent parents: %w", err)
	}
	return nil
}

// captureSessionTitles reads the current title of every session about to be
// reinserted whose incoming row carries no title of its own — codex leaves Title
// empty because the title lives only in the DB, set out-of-band from
// session_index.jsonl. Paired with restoreSessionTitles to carry that title across
// the delete+reinsert, so a reingest of a rollout never wipes a title a prior ingest
// applied. Generic: a provider whose title rides the transcript (claude-code) sets
// Title on the incoming row and is skipped here.
func captureSessionTitles(ctx context.Context, tx *sql.Tx, sessions []trace.Session) (map[string]string, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	stmt, err := tx.PrepareContext(ctx, `SELECT title FROM sessions WHERE id = ? AND title IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("prepare capture titles: %w", err)
	}
	defer stmt.Close()
	preserved := make(map[string]string)
	for _, session := range sessions {
		if session.Title != "" {
			continue // incoming row carries its own title; nothing to preserve
		}
		var title string
		switch err := stmt.QueryRowContext(ctx, session.ID).Scan(&title); {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return nil, fmt.Errorf("capture title %s: %w", session.ID, err)
		}
		preserved[session.ID] = title
	}
	return preserved, nil
}

// restoreSessionTitles writes the titles captured by captureSessionTitles back onto
// the freshly reinserted rows that came in title-less, so the out-of-band title
// survives the delete+reinsert. The `title IS NULL` guard leaves a row that already
// has a title (e.g. claude-code's transcript-borne title) untouched; an
// updateSessionTitles round runs after and overrides with any newer index value.
func restoreSessionTitles(ctx context.Context, tx *sql.Tx, preserved map[string]string) error {
	if len(preserved) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE sessions SET title = ? WHERE id = ? AND title IS NULL`)
	if err != nil {
		return fmt.Errorf("prepare restore titles: %w", err)
	}
	defer stmt.Close()
	for id, title := range preserved {
		if _, err := stmt.ExecContext(ctx, title, id); err != nil {
			return fmt.Errorf("restore title %s: %w", id, err)
		}
	}
	return nil
}

// updateSessionTitles applies out-of-band session titles (Index.SessionTitles:
// external id -> title) keyed by external_session_id. It is how codex's
// session_index.jsonl thread_name reaches sessions.title: that title is mutated by a
// rename WITHOUT touching the rollout, so it cannot ride the transcript-fingerprint
// incremental path and is instead refreshed by the codex trailing title round. The
// `title != ?` guard skips rows already at the current title, so an unchanged index
// does not spuriously bump updated_at. Top-level only — a subagent never has its own
// out-of-band title and must not inherit the parent's. The map's only producer
// (readSessionTitles) guarantees non-empty external id -> non-empty title.
func updateSessionTitles(ctx context.Context, tx *sql.Tx, sourceID string, titles map[string]string) error {
	if len(titles) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE sessions SET title = ?, updated_at = ?
		WHERE source_id = ? AND external_session_id = ? AND is_subagent = 0
		  AND (title IS NULL OR title != ?)`)
	if err != nil {
		return fmt.Errorf("prepare session titles: %w", err)
	}
	defer stmt.Close()
	for ext, title := range titles {
		if _, err := stmt.ExecContext(ctx, title, now, sourceID, ext, title); err != nil {
			return fmt.Errorf("update session title %s: %w", ext, err)
		}
	}
	return nil
}

func insertTurnsAndChildren(ctx context.Context, tx *sql.Tx, index trace.Index, sessionProject map[string]string, subagentSessions map[string]bool) error {
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
		user_message, assistant_final,
		started_at, ended_at, duration_ms, status,
		invocation_count, tool_call_count, is_subagent,
		total_input_tokens, total_output_tokens, cache_read_tokens, cache_write_tokens, cache_write_long_tokens,
		created_at, updated_at)`
	if err := execRows(ctx, tx, turnPrefix, placeholders(20), 20, len(turns), func(i int) []any {
		turn := &turns[i]
		return []any{
			turn.ID, turn.SessionID, sqlNullStr(sessionProject[turn.SessionID]), turn.Index,
			turn.UserMessage, turn.AssistantFinal,
			timeText(turn.StartedAt), timeText(turn.EndedAt), turn.DurationMs, turn.Status,
			turn.InvocationCount, turn.ToolCallCount, boolInt(subagentSessions[turn.SessionID]),
			turn.Tokens.Input, turn.Tokens.Output, turn.Tokens.CacheRead, turn.Tokens.CacheWrite, turn.Tokens.CacheWriteLong,
			now, now,
		}
	}); err != nil {
		return fmt.Errorf("insert turns: %w", err)
	}

	const invPrefix = `INSERT INTO invocations(
		id, turn_id, session_id, invocation_index,
		provider, model,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cache_write_long_tokens, context_window_tokens,
		started_at, ended_at, stop_reason, status, raw_event_id, created_at)`
	if err := execRows(ctx, tx, invPrefix, placeholders(18), 18, len(invs), func(i int) []any {
		inv := invs[i].row
		return []any{
			inv.ID, invs[i].turnID, inv.SessionID, inv.Index,
			inv.Provider, inv.Model,
			inv.Tokens.Input, inv.Tokens.Output, inv.Tokens.CacheRead, inv.Tokens.CacheWrite, inv.Tokens.CacheWriteLong, sqlNullInt(inv.ContextWindowTokens),
			timeText(inv.StartedAt), timeText(inv.EndedAt), inv.StopReason, inv.Status, sqlNullStr(inv.RawEventID), now,
		}
	}); err != nil {
		return fmt.Errorf("insert invocations: %w", err)
	}

	const toolPrefix = `INSERT INTO tool_calls(
		id, turn_id, session_id, invocation_id, call_index,
		tool_kind, tool_name, mcp_server, mcp_tool, use_id,
		input_json, output_text, output_bytes,
		status, error, started_at, ended_at, duration_ms,
		raw_use_event_id, raw_result_event_id, created_at)`
	if err := execRows(ctx, tx, toolPrefix, placeholders(21), 21, len(calls), func(i int) []any {
		call := calls[i].row
		return []any{
			call.ID, calls[i].turnID, call.SessionID, sqlNullStr(call.InvocationID), call.CallIndex,
			call.Kind, call.Name, sqlNullStr(call.MCPServer), sqlNullStr(call.MCPTool), sqlNullStr(call.UseID),
			call.Input, call.Output, call.OutputBytes,
			call.Status, call.Error, timeText(call.StartedAt), timeText(call.EndedAt), call.DurationMs,
			sqlNullStr(call.RawUseEventID), sqlNullStr(call.RawResultEventID), now,
		}
	}); err != nil {
		return fmt.Errorf("insert tool calls: %w", err)
	}

	const compPrefix = `INSERT INTO turn_components(turn_id, component_kind, component_id, component_name, relation, token_estimate, evidence, confidence, created_at)`
	if err := execRows(ctx, tx, compPrefix, placeholders(9), 9, len(comps), func(i int) []any {
		comp := comps[i].row
		return []any{
			comps[i].turnID, comp.ComponentKind, sqlNullStr(comp.ComponentID), comp.ComponentName, comp.Relation, comp.TokenEstimate, comp.Evidence, string(comp.Confidence), now,
		}
	}); err != nil {
		return fmt.Errorf("insert turn components: %w", err)
	}
	return nil
}

func reconcileSkills(ctx context.Context, tx *sql.Tx, sourceID string, skills []trace.Skill) error {
	if err := insertSkills(ctx, tx, sourceID, skills); err != nil {
		return err
	}
	return deleteStaleMetadata(ctx, tx, "skills", "id", sourceID, skillIDs(skills))
}

func skillIDs(skills []trace.Skill) []string {
	ids := make([]string, 0, len(skills))
	for _, skill := range skills {
		ids = append(ids, skill.ID)
	}
	return ids
}

func insertSkills(ctx context.Context, tx *sql.Tx, sourceID string, skills []trace.Skill) error {
	if len(skills) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO skills(
			id, source_id, name, scope, source_path, source_hash,
			description, version, argument_hint, user_invocable,
			triggers, allowed_tools, tools, compatibility, license,
			created_at, updated_at
		)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, name, scope, source_path) DO UPDATE SET
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
			skill.ID, sourceID, skill.Name, skill.Scope, skill.SourcePath, skill.SourceHash,
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

func reconcileMCPServers(ctx context.Context, tx *sql.Tx, sourceID string, servers []trace.MCPServer) error {
	if err := insertMCPServers(ctx, tx, sourceID, servers); err != nil {
		return err
	}
	return deleteStaleMetadata(ctx, tx, "mcp_servers", "id", sourceID, mcpServerIDs(servers))
}

func mcpServerIDs(servers []trace.MCPServer) []string {
	ids := make([]string, 0, len(servers))
	for _, server := range servers {
		ids = append(ids, server.ID)
	}
	return ids
}

func deleteStaleMetadata(ctx context.Context, tx *sql.Tx, table, idColumn, sourceID string, keepIDs []string) error {
	if len(keepIDs) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE source_id = ?`, sourceID); err != nil {
			return fmt.Errorf("delete stale %s: %w", table, err)
		}
		return nil
	}
	// The keep-set is bound in one unchunked NOT IN: unlike the additive IN-chunking
	// the rest of the package uses (eachTurnChunk, deleteSourceFiles), a NOT IN
	// cannot be split — "id NOT IN (chunk1)" would delete rows that are in chunk2.
	// This is safe because keepIDs is one source's scanned skills/MCP servers (tens
	// to low hundreds in practice), far under the driver's SQLITE_MAX_VARIABLE_NUMBER
	// (32766; see bulk.go). A source approaching that bound would instead need the
	// set-difference-then-IN-chunk approach.
	args := make([]any, 0, len(keepIDs)+1)
	args = append(args, sourceID)
	for _, id := range keepIDs {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE source_id = ? AND `+idColumn+` NOT IN (`+bindMarkers(len(keepIDs))+`)`, args...); err != nil {
		return fmt.Errorf("delete stale %s: %w", table, err)
	}
	return nil
}

func insertMCPServers(ctx context.Context, tx *sql.Tx, sourceID string, servers []trace.MCPServer) error {
	if len(servers) == 0 {
		return nil
	}
	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO mcp_servers(id, source_id, name, scope, transport, config_path, config_hash, enabled, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, name, scope, config_path) DO UPDATE SET
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
		_, err = stmt.ExecContext(ctx, server.ID, sourceID, server.Name, server.Scope, server.Transport, server.ConfigPath, server.ConfigHash, enabled, now, now)
		if err != nil {
			return fmt.Errorf("insert mcp_server: %w", err)
		}
	}
	return nil
}

func insertParseErrors(ctx context.Context, tx *sql.Tx, sourceID string, errors []trace.ParseError, subagentFiles map[string]bool) error {
	if len(errors) == 0 {
		return nil
	}
	now := nowUTC()
	// is_subagent denormalized by source file (mirrors insertRawEvents): parse_errors
	// has no session link, so the file is the only marker, and an indexable column
	// avoids the NULL-source_file NOT-IN trap a sessions subquery would hit.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO parse_errors(source_id, source_root_id, source_file, line_no, raw_event_id, message, parser_version, is_subagent, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			sqlNullStr(parseErr.RawEventID), parseErr.Message, parseErr.ParserVersion,
			boolInt(subagentFiles[parseErr.SourceFile]), now,
		)
		if err != nil {
			return fmt.Errorf("insert parse_error: %w", err)
		}
	}
	return nil
}

func insertSearchDocuments(ctx context.Context, tx *sql.Tx, sourceID string, index trace.Index, subagentSessions map[string]bool) error {
	// Fields are already redacted-or-raw in place by redact.ApplyToIndex, so index
	// them directly. The document text is concatenated in SQL (`? || ' ' || ?`)
	// rather than in Go: the joined per-row string — roughly the whole FTS payload —
	// is never materialized host-side (it was the bulk of this function's allocation),
	// and `||` over the bound TEXT values yields a byte-identical result to the
	// previous `a + " " + b`. Turn docs and tool-call docs are inserted in two
	// batched passes (one row shape each); this reassigns search_documents.rowid
	// relative to the old interleaved order, which is internally consistent (the FTS
	// trigger keys off new.rowid) and unobservable to queries keyed off id/kind.
	// is_subagent is denormalized by session id so default search excludes subagents
	// via a direct FTS column check instead of a sessions subquery.
	turns := index.Turns
	const prefix = `INSERT INTO search_documents(kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)`

	if err := execRows(ctx, tx, prefix, "('turn', ?, ?, ?, ?, ?, ? || ' ' || ?, ?)", 8, len(turns), func(i int) []any {
		turn := &turns[i]
		return []any{turn.ID, sourceID, turn.SessionID, turn.ID, turn.TranscriptPath, turn.UserMessage, turn.AssistantFinal, boolInt(subagentSessions[turn.SessionID])}
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
	if err := execRows(ctx, tx, prefix, "('tool_call', ?, ?, ?, ?, ?, ? || ' ' || ? || ' ' || ?, ?)", 9, len(calls), func(i int) []any {
		d := calls[i]
		return []any{d.call.ID, sourceID, d.sessionID, d.turnID, d.transcriptPath, d.call.Name, d.call.Input, d.call.Output, boolInt(subagentSessions[d.sessionID])}
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

// boolInt renders a Go bool as the 0/1 a NOT NULL INTEGER column expects.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
	return root != "" && fsx.PathWithin(root, file)
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
	return "", false
}

func lookupRootID(rootIDs map[string]string, session trace.Session) string {
	id, _ := rootIDForFile(rootIDs, session.TranscriptPath)
	return id
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

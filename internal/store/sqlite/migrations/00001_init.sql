-- +goose Up
-- +goose StatementBegin

-- Pragmas live in store init code so they apply to every connection; only schema here.

-- sources: one row per provider kind (claude-code, codex, ...)
CREATE TABLE sources(
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    capabilities  TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- source_roots: discovered local directories per source (env, default, manual, archive)
CREATE TABLE source_roots(
    id            TEXT PRIMARY KEY,
    source_id     TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    path          TEXT NOT NULL,
    kind          TEXT NOT NULL DEFAULT 'manual',
    priority      INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    last_scan_at  TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    UNIQUE(source_id, path)
);

-- raw_events: original provider JSONL/hook events (metadata + small fields)
CREATE TABLE raw_events(
    id                   TEXT PRIMARY KEY,
    source_id            TEXT NOT NULL,
    source_root_id       TEXT NOT NULL REFERENCES source_roots(id) ON DELETE CASCADE,
    source_kind          TEXT NOT NULL DEFAULT 'transcript',
    source_file          TEXT NOT NULL,
    byte_offset          INTEGER NOT NULL DEFAULT 0,
    line_no              INTEGER NOT NULL,
    event_time           TEXT,
    session_external_id  TEXT,
    message_external_id  TEXT,
    parent_external_id   TEXT,
    event_type           TEXT,
    role                 TEXT,
    raw_hash             TEXT NOT NULL,
    parser_version       TEXT NOT NULL,
    parse_status         TEXT NOT NULL DEFAULT 'pending',
    parse_error          TEXT,
    imported_at          TEXT NOT NULL,
    UNIQUE(source_root_id, source_file, line_no, raw_hash)
);

-- No raw_payloads table: the original transcript files are the source of
-- truth. raw_events stores (source_file, byte_offset) so the original JSON
-- line can be re-read on demand from disk instead of copying every event's
-- bytes (and a redacted duplicate) into the DB.

-- ingest_offsets: per-file change-detection fingerprint. (size_bytes, mtime_ns,
-- inode_no) is the skip signal — `toktop ingest` re-reads a transcript only when
-- one of them changes, and inode_no additionally catches rotation (a recreated
-- file reusing the path with a new inode). Byte-level tail reading is NOT
-- implemented: a changed file is re-read in full and de-duplicated by the
-- raw_events UNIQUE constraint, so there is no byte_offset cursor. line_no /
-- last_hash record the last ingested line for diagnostics only.
CREATE TABLE ingest_offsets(
    id             TEXT PRIMARY KEY,
    source_root_id TEXT NOT NULL REFERENCES source_roots(id) ON DELETE CASCADE,
    source_file    TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    mtime_ns       INTEGER NOT NULL DEFAULT 0,
    inode_no       INTEGER NOT NULL DEFAULT 0,
    line_no        INTEGER NOT NULL DEFAULT 0,
    last_hash      TEXT,
    updated_at     TEXT NOT NULL,
    UNIQUE(source_root_id, source_file)
);

-- projects: derived from session transcripts and project configs
CREATE TABLE projects(
    id             TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL,
    source_root_id TEXT REFERENCES source_roots(id) ON DELETE SET NULL,
    name           TEXT NOT NULL,
    path           TEXT,
    path_hash      TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE(source_id, name, path)
);

CREATE TABLE sessions(
    id                   TEXT PRIMARY KEY,
    source_id            TEXT NOT NULL,
    source_root_id       TEXT REFERENCES source_roots(id) ON DELETE SET NULL,
    project_id           TEXT REFERENCES projects(id) ON DELETE SET NULL,
    external_session_id  TEXT,
    transcript_path      TEXT NOT NULL,
    started_at           TEXT,
    ended_at             TEXT,
    status               TEXT NOT NULL DEFAULT 'unknown',
    total_turns          INTEGER NOT NULL DEFAULT 0,
    total_tool_calls     INTEGER NOT NULL DEFAULT 0,
    total_input_tokens   INTEGER NOT NULL DEFAULT 0,
    total_output_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_write_long_tokens INTEGER NOT NULL DEFAULT 0,
    parser_version       TEXT NOT NULL,
    -- last_turn_id / last_turn_status / last_turn_at are denormalized from
    -- the most recent turn (by turn_index). Populated at SaveIngest time so
    -- ListLiveSessions does not need a ROW_NUMBER() OVER(...) window function
    -- on every dashboard refresh.
    last_turn_id         TEXT,
    last_turn_status     TEXT,
    last_turn_at         TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE turns(
    id                       TEXT PRIMARY KEY,
    session_id               TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id               TEXT REFERENCES projects(id) ON DELETE SET NULL,
    turn_index               INTEGER NOT NULL,
    user_message             TEXT,
    assistant_final          TEXT,
    summary                  TEXT,
    started_at               TEXT,
    ended_at                 TEXT,
    duration_ms              INTEGER NOT NULL DEFAULT 0,
    status                   TEXT NOT NULL DEFAULT 'unknown',
    failure_reason           TEXT,
    invocation_count         INTEGER NOT NULL DEFAULT 0,
    tool_call_count          INTEGER NOT NULL DEFAULT 0,
    subagent_count           INTEGER NOT NULL DEFAULT 0,
    total_input_tokens       INTEGER NOT NULL DEFAULT 0,
    total_output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_write_long_tokens  INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    UNIQUE(session_id, turn_index)
);

CREATE TABLE subagent_runs(
    id                   TEXT PRIMARY KEY,
    parent_turn_id       TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    parent_tool_call_id  TEXT,
    agent_name           TEXT,
    agent_type           TEXT,
    model                TEXT,
    transcript_path      TEXT,
    started_at           TEXT,
    ended_at             TEXT,
    duration_ms          INTEGER NOT NULL DEFAULT 0,
    status               TEXT NOT NULL DEFAULT 'unknown',
    total_input_tokens   INTEGER NOT NULL DEFAULT 0,
    total_output_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_write_long_tokens INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE TABLE tool_outputs(
    id              TEXT PRIMARY KEY,
    source_file     TEXT,
    content_text    TEXT,
    content_hash    TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    retention_class TEXT NOT NULL DEFAULT 'full',
    created_at      TEXT NOT NULL,
    UNIQUE(content_hash)
);

CREATE TABLE invocations(
    id                     TEXT PRIMARY KEY,
    turn_id                TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    session_id             TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    subagent_run_id        TEXT REFERENCES subagent_runs(id) ON DELETE SET NULL,
    invocation_index       INTEGER NOT NULL,
    provider               TEXT,
    model                  TEXT,
    input_tokens           INTEGER NOT NULL DEFAULT 0,
    output_tokens          INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_write_long_tokens INTEGER NOT NULL DEFAULT 0,
    context_window_tokens  INTEGER,
    started_at             TEXT,
    ended_at               TEXT,
    latency_ms             INTEGER NOT NULL DEFAULT 0,
    stop_reason            TEXT,
    status                 TEXT NOT NULL DEFAULT 'unknown',
    raw_event_id           TEXT,
    created_at             TEXT NOT NULL
);

CREATE TABLE tool_calls(
    id                  TEXT PRIMARY KEY,
    turn_id             TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    session_id          TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    invocation_id       TEXT REFERENCES invocations(id) ON DELETE SET NULL,
    subagent_run_id     TEXT REFERENCES subagent_runs(id) ON DELETE SET NULL,
    call_index          INTEGER NOT NULL,
    tool_kind           TEXT NOT NULL DEFAULT 'unknown',
    tool_name           TEXT NOT NULL,
    mcp_server          TEXT,
    mcp_tool            TEXT,
    use_id              TEXT,
    input_json          TEXT,
    output_text         TEXT,
    output_ref          TEXT REFERENCES tool_outputs(id) ON DELETE SET NULL,
    output_bytes        INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'unknown',
    error               TEXT,
    started_at          TEXT,
    ended_at            TEXT,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    raw_use_event_id    TEXT,
    raw_result_event_id TEXT,
    created_at          TEXT NOT NULL
);

CREATE TABLE context_events(
    id              TEXT PRIMARY KEY,
    session_id      TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id         TEXT REFERENCES turns(id) ON DELETE CASCADE,
    invocation_id   TEXT REFERENCES invocations(id) ON DELETE CASCADE,
    subagent_run_id TEXT REFERENCES subagent_runs(id) ON DELETE CASCADE,
    component_type  TEXT NOT NULL,
    component_name  TEXT,
    source_path     TEXT,
    source_hash     TEXT,
    phase           TEXT,
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    evidence        TEXT,
    confidence      TEXT NOT NULL DEFAULT 'unknown',
    created_at      TEXT NOT NULL
);

CREATE TABLE skills(
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    scope          TEXT NOT NULL DEFAULT 'unknown',
    source_path    TEXT,
    source_hash    TEXT,
    description    TEXT,
    version        TEXT,
    argument_hint  TEXT,
    user_invocable INTEGER,        -- 0/1, NULL when frontmatter omits it
    triggers       TEXT,           -- JSON array of strings, NULL when absent
    allowed_tools  TEXT,           -- JSON array of strings, NULL when absent
    tools          TEXT,           -- JSON value (string or array, observed in either shape)
    compatibility  TEXT,
    license        TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE(name, scope, source_path)
);

CREATE TABLE mcp_servers(
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL DEFAULT 'unknown',
    transport   TEXT NOT NULL DEFAULT 'unknown',
    config_path TEXT,
    config_hash TEXT,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(name, scope, config_path)
);

CREATE TABLE turn_components(
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id        TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    component_kind TEXT NOT NULL,
    component_id   TEXT,
    component_name TEXT NOT NULL,
    relation       TEXT NOT NULL,
    token_estimate INTEGER NOT NULL DEFAULT 0,
    evidence       TEXT,
    confidence     TEXT NOT NULL DEFAULT 'unknown',
    created_at     TEXT NOT NULL
);

CREATE TABLE rule_suggestions(
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id        TEXT NOT NULL,
    severity       TEXT NOT NULL DEFAULT 'info',
    confidence     TEXT NOT NULL DEFAULT 'unknown',
    scope_kind     TEXT,
    scope_id       TEXT,
    evidence_json  TEXT,
    recommendation TEXT,
    created_at     TEXT NOT NULL
);

CREATE TABLE parse_errors(
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id      TEXT NOT NULL,
    source_root_id TEXT,
    source_file    TEXT,
    line_no        INTEGER,
    raw_event_id   TEXT,
    message        TEXT NOT NULL,
    parser_version TEXT,
    created_at     TEXT NOT NULL
);

-- Search infrastructure.
--
-- search_documents is the content table holding the searchable (redacted,
-- projected) text once. search_fts is an EXTERNAL-CONTENT FTS5 index over it,
-- so FTS5 stores only the inverted index — not a second copy of the text —
-- while snippet() still works by reading from search_documents by rowid.
-- Triggers keep the index in sync (the external-content contract). Only turn
-- and tool_call projections are indexed; raw events are not (they duplicated
-- this text plus JSON noise).
CREATE TABLE search_documents(
    rowid       INTEGER PRIMARY KEY,
    kind        TEXT,
    id          TEXT,
    source_id   TEXT,
    session_id  TEXT,
    turn_id     TEXT,
    source_file TEXT,
    text        TEXT
);
CREATE INDEX idx_search_documents_source ON search_documents(source_id, source_file);

CREATE VIRTUAL TABLE search_fts USING fts5(
    kind         UNINDEXED,
    id           UNINDEXED,
    source_id    UNINDEXED,
    session_id   UNINDEXED,
    turn_id      UNINDEXED,
    source_file  UNINDEXED,
    text,
    content      = 'search_documents',
    content_rowid = 'rowid',
    tokenize = 'unicode61'
);

-- Keep search_fts consistent with search_documents. ingest replaces rows by
-- DELETE + INSERT, so the update trigger is included for completeness.
CREATE TRIGGER search_documents_ai AFTER INSERT ON search_documents BEGIN
    INSERT INTO search_fts(rowid, kind, id, source_id, session_id, turn_id, source_file, text)
    VALUES (new.rowid, new.kind, new.id, new.source_id, new.session_id, new.turn_id, new.source_file, new.text);
END;
CREATE TRIGGER search_documents_ad AFTER DELETE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, kind, id, source_id, session_id, turn_id, source_file, text)
    VALUES ('delete', old.rowid, old.kind, old.id, old.source_id, old.session_id, old.turn_id, old.source_file, old.text);
END;
CREATE TRIGGER search_documents_au AFTER UPDATE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, kind, id, source_id, session_id, turn_id, source_file, text)
    VALUES ('delete', old.rowid, old.kind, old.id, old.source_id, old.session_id, old.turn_id, old.source_file, old.text);
    INSERT INTO search_fts(rowid, kind, id, source_id, session_id, turn_id, source_file, text)
    VALUES (new.rowid, new.kind, new.id, new.source_id, new.session_id, new.turn_id, new.source_file, new.text);
END;

CREATE TABLE symbols(
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_kind TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    symbol_kind TEXT,
    created_at  TEXT NOT NULL,
    UNIQUE(entity_kind, entity_id, symbol)
);

-- Indexes
CREATE INDEX idx_raw_events_source        ON raw_events(source_id);
CREATE INDEX idx_raw_events_time          ON raw_events(event_time);
CREATE INDEX idx_raw_events_session       ON raw_events(session_external_id);
CREATE INDEX idx_raw_events_parse_status  ON raw_events(parse_status);

CREATE INDEX idx_sessions_source          ON sessions(source_id);
CREATE INDEX idx_sessions_project         ON sessions(project_id);
CREATE INDEX idx_sessions_started         ON sessions(started_at);
CREATE INDEX idx_sessions_external        ON sessions(external_session_id);
-- Expression index matching ListLiveSessions' ORDER BY (the live-status poll
-- endpoint), so it scans sessions in last-activity order instead of building a
-- TEMP B-TREE on every call. The COALESCE/NULLIF expression must stay in sync
-- with listings.go:ListLiveSessions.
CREATE INDEX idx_sessions_last_activity   ON sessions(
    COALESCE(NULLIF(last_turn_at, ''), NULLIF(ended_at, ''), NULLIF(started_at, ''), '') DESC,
    id
);

CREATE INDEX idx_turns_session            ON turns(session_id);
CREATE INDEX idx_turns_project            ON turns(project_id);
-- (started_at, turn_index, id) matches loadAllTurns' ORDER BY so equal-timestamp
-- turns sort from the index instead of a TEMP B-TREE.
CREATE INDEX idx_turns_started            ON turns(started_at, turn_index, id);
CREATE INDEX idx_turns_status             ON turns(status);

CREATE INDEX idx_invocations_turn         ON invocations(turn_id);
CREATE INDEX idx_invocations_session      ON invocations(session_id);
CREATE INDEX idx_invocations_model        ON invocations(model);
CREATE INDEX idx_invocations_subagent     ON invocations(subagent_run_id);

CREATE INDEX idx_tool_calls_turn          ON tool_calls(turn_id);
CREATE INDEX idx_tool_calls_session       ON tool_calls(session_id);
CREATE INDEX idx_tool_calls_invocation    ON tool_calls(invocation_id);
CREATE INDEX idx_tool_calls_subagent      ON tool_calls(subagent_run_id);
CREATE INDEX idx_tool_calls_kind          ON tool_calls(tool_kind);
CREATE INDEX idx_tool_calls_name          ON tool_calls(tool_name);
CREATE INDEX idx_tool_calls_mcp           ON tool_calls(mcp_server);

CREATE INDEX idx_subagent_runs_turn       ON subagent_runs(parent_turn_id);

CREATE INDEX idx_context_events_turn      ON context_events(turn_id);
CREATE INDEX idx_context_events_session   ON context_events(session_id);
CREATE INDEX idx_context_events_subagent  ON context_events(subagent_run_id);
CREATE INDEX idx_context_events_type      ON context_events(component_type);

CREATE INDEX idx_turn_components_turn     ON turn_components(turn_id);
CREATE INDEX idx_turn_components_kind     ON turn_components(component_kind);
CREATE INDEX idx_turn_components_relation ON turn_components(relation);
CREATE INDEX idx_turn_components_name     ON turn_components(component_name);

CREATE INDEX idx_rule_suggestions_rule    ON rule_suggestions(rule_id);
CREATE INDEX idx_rule_suggestions_scope   ON rule_suggestions(scope_kind, scope_id);

CREATE INDEX idx_parse_errors_source      ON parse_errors(source_id);

CREATE INDEX idx_symbols_symbol           ON symbols(symbol);
CREATE INDEX idx_symbols_entity           ON symbols(entity_kind, entity_id);

-- Time-range listing filters. ListTools/ListMCPs add `tool_calls.started_at >= ?`
-- and ListSkills/componentAvailability add `turn_components.created_at >= ?` when a
-- --since bound is set; the leading column satisfies the range and the trailing
-- columns cover the GROUP BY / WHERE so the planner avoids a full table scan.
CREATE INDEX idx_tool_calls_started       ON tool_calls(started_at, tool_name, tool_kind);
CREATE INDEX idx_turn_components_created   ON turn_components(created_at, component_kind, component_name);

-- Expression indexes for retention/redaction maintenance. PruneRawEvents and
-- RedactNormalized (retention.go) filter on COALESCE(NULLIF(...)) effective-time
-- expressions that a plain column index cannot satisfy; these mirror those WHERE
-- clauses (the planner matches by parsed expression, so they must stay in sync).
CREATE INDEX idx_raw_events_effective_time ON raw_events(COALESCE(NULLIF(event_time, ''), imported_at));
CREATE INDEX idx_sessions_effective_age    ON sessions(COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), created_at));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sessions_effective_age;
DROP INDEX IF EXISTS idx_raw_events_effective_time;
DROP INDEX IF EXISTS idx_turn_components_created;
DROP INDEX IF EXISTS idx_tool_calls_started;
DROP INDEX IF EXISTS idx_symbols_entity;
DROP INDEX IF EXISTS idx_symbols_symbol;
DROP INDEX IF EXISTS idx_parse_errors_source;
DROP INDEX IF EXISTS idx_rule_suggestions_scope;
DROP INDEX IF EXISTS idx_rule_suggestions_rule;
DROP INDEX IF EXISTS idx_turn_components_name;
DROP INDEX IF EXISTS idx_turn_components_relation;
DROP INDEX IF EXISTS idx_turn_components_kind;
DROP INDEX IF EXISTS idx_turn_components_turn;
DROP INDEX IF EXISTS idx_context_events_type;
DROP INDEX IF EXISTS idx_context_events_subagent;
DROP INDEX IF EXISTS idx_context_events_session;
DROP INDEX IF EXISTS idx_context_events_turn;
DROP INDEX IF EXISTS idx_subagent_runs_turn;
DROP INDEX IF EXISTS idx_tool_calls_mcp;
DROP INDEX IF EXISTS idx_tool_calls_name;
DROP INDEX IF EXISTS idx_tool_calls_kind;
DROP INDEX IF EXISTS idx_tool_calls_subagent;
DROP INDEX IF EXISTS idx_tool_calls_invocation;
DROP INDEX IF EXISTS idx_tool_calls_session;
DROP INDEX IF EXISTS idx_tool_calls_turn;
DROP INDEX IF EXISTS idx_invocations_subagent;
DROP INDEX IF EXISTS idx_invocations_model;
DROP INDEX IF EXISTS idx_invocations_session;
DROP INDEX IF EXISTS idx_invocations_turn;
DROP INDEX IF EXISTS idx_turns_status;
DROP INDEX IF EXISTS idx_turns_started;
DROP INDEX IF EXISTS idx_turns_project;
DROP INDEX IF EXISTS idx_turns_session;
DROP INDEX IF EXISTS idx_sessions_external;
DROP INDEX IF EXISTS idx_sessions_started;
DROP INDEX IF EXISTS idx_sessions_project;
DROP INDEX IF EXISTS idx_sessions_source;
DROP INDEX IF EXISTS idx_raw_events_parse_status;
DROP INDEX IF EXISTS idx_raw_events_session;
DROP INDEX IF EXISTS idx_raw_events_time;
DROP INDEX IF EXISTS idx_raw_events_source;

DROP TABLE IF EXISTS symbols;
DROP TABLE IF EXISTS search_fts;
DROP TABLE IF EXISTS parse_errors;
DROP TABLE IF EXISTS rule_suggestions;
DROP TABLE IF EXISTS turn_components;
DROP TABLE IF EXISTS mcp_servers;
DROP TABLE IF EXISTS skills;
DROP TABLE IF EXISTS context_events;
DROP TABLE IF EXISTS tool_calls;
DROP TABLE IF EXISTS invocations;
DROP TABLE IF EXISTS tool_outputs;
DROP TABLE IF EXISTS subagent_runs;
DROP TABLE IF EXISTS turns;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS ingest_offsets;
DROP TABLE IF EXISTS raw_payloads;
DROP TABLE IF EXISTS raw_events;
DROP TABLE IF EXISTS source_roots;
DROP TABLE IF EXISTS sources;
-- +goose StatementEnd

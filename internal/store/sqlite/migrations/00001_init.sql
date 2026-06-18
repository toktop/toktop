-- +goose Up
-- +goose StatementBegin

-- Squashed baseline. This is the full projected schema as of schema epoch 17 (the
-- title column landed at epoch 16; epoch 17 is a projection-semantics bump with no
-- DDL change) — the original 00001 baseline with the later migrations (projection
-- cleanup, dead-field drops, activity-time indexes, subagent marking + linkage)
-- folded in, so a fresh install builds the final shape in one pass instead of
-- replaying create-then-drop churn. Pre-existing databases at an earlier epoch
-- are wiped and rebuilt from the transcripts (see schemaUserVersion in
-- store.go); the DB is a pure idempotent projection, so the squash is lossless.
--
-- Pragmas live in store init code so they apply to every connection; only schema here.

-- sources: one row per provider kind (claude-code, codex, ...)
CREATE TABLE sources(
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

-- source_roots: discovered local directories per source
CREATE TABLE source_roots(
    id          TEXT PRIMARY KEY,
    source_id   TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
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
    -- Denormalized so the default subagent-exclude is a direct, indexable column
    -- check: raw_events.session_external_id is the PARENT session uuid (shared by a
    -- parent and all its subagents), so it cannot distinguish a subagent's events.
    is_subagent          INTEGER NOT NULL DEFAULT 0,
    UNIQUE(source_root_id, source_file, line_no, raw_hash)
);

-- No raw_payloads table: the original transcript files are the source of
-- truth. raw_events stores (source_file, byte_offset) so the original JSON
-- line can be re-read on demand from disk instead of copying every event's
-- bytes (and a redacted duplicate) into the DB.

-- ingest_offsets: per-source-file change-detection fingerprint. For file-backed
-- providers (claude-code, codex) the skip signal is (size_bytes, mtime_ns,
-- inode_no) — `toktop ingest` re-reads a transcript only when one of them
-- changes, and inode_no additionally catches rotation (a recreated file reusing
-- the path with a new inode). For a DB-backed provider (opencode) whose
-- source_file is a synthetic key, those three are 0 and fingerprint_token carries
-- the provider's native per-session revision (opencode's event_sequence.seq)
-- instead. Byte-level tail reading is NOT implemented: a changed file is re-read
-- in full and de-duplicated by the raw_events UNIQUE constraint, so there is no
-- byte_offset cursor. line_no / last_hash record the last ingested line for
-- diagnostics only.
CREATE TABLE ingest_offsets(
    id                TEXT PRIMARY KEY,
    source_root_id    TEXT NOT NULL REFERENCES source_roots(id) ON DELETE CASCADE,
    source_file       TEXT NOT NULL,
    size_bytes        INTEGER NOT NULL DEFAULT 0,
    mtime_ns          INTEGER NOT NULL DEFAULT 0,
    inode_no          INTEGER NOT NULL DEFAULT 0,
    fingerprint_token TEXT NOT NULL DEFAULT '',
    line_no           INTEGER NOT NULL DEFAULT 0,
    last_hash         TEXT,
    updated_at        TEXT NOT NULL,
    UNIQUE(source_root_id, source_file)
);

-- projects: derived from session transcripts and project configs
CREATE TABLE projects(
    id             TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL,
    source_root_id TEXT REFERENCES source_roots(id) ON DELETE SET NULL,
    name           TEXT NOT NULL,
    path           TEXT,
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
    -- title is the session display name (claude-code custom/ai-title from the
    -- transcript; codex thread_name from the out-of-band session_index.jsonl,
    -- refreshed every ingest). A read-time projection, never authored by a user.
    title                TEXT,
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
    -- Subagent marker + parent linkage (claude-code Task/Agent runs and a
    -- Workflow's internal agents; codex spawned threads). is_subagent + the
    -- parent_* fields stay 0/NULL for every top-level session. parent_external_id
    -- is the link the parser sets (the parent's external id); parent_session_id
    -- is the internal FK resolved from it by a post-pass at ingest time.
    is_subagent          INTEGER NOT NULL DEFAULT 0,
    parent_external_id   TEXT,
    parent_session_id    TEXT,
    parent_tool_use_id   TEXT,
    workflow_run_id      TEXT,
    subagent_kind        TEXT,
    agent_type           TEXT,
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
    started_at               TEXT,
    ended_at                 TEXT,
    duration_ms              INTEGER NOT NULL DEFAULT 0,
    status                   TEXT NOT NULL DEFAULT 'unknown',
    invocation_count         INTEGER NOT NULL DEFAULT 0,
    tool_call_count          INTEGER NOT NULL DEFAULT 0,
    total_input_tokens       INTEGER NOT NULL DEFAULT 0,
    total_output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_write_long_tokens  INTEGER NOT NULL DEFAULT 0,
    -- Denormalized from sessions.is_subagent so the default exclude is a direct
    -- column check rather than a sessions subquery.
    is_subagent              INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    UNIQUE(session_id, turn_index)
);

CREATE TABLE invocations(
    id                     TEXT PRIMARY KEY,
    turn_id                TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    session_id             TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
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
    call_index          INTEGER NOT NULL,
    tool_kind           TEXT NOT NULL DEFAULT 'unknown',
    tool_name           TEXT NOT NULL,
    mcp_server          TEXT,
    mcp_tool            TEXT,
    use_id              TEXT,
    input_json          TEXT,
    output_text         TEXT,
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

CREATE TABLE skills(
    id             TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
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
    UNIQUE(source_id, name, scope, source_path)
);

CREATE TABLE mcp_servers(
    id          TEXT PRIMARY KEY,
    source_id   TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL DEFAULT 'unknown',
    transport   TEXT NOT NULL DEFAULT 'unknown',
    config_path TEXT,
    config_hash TEXT,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(source_id, name, scope, config_path)
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
    -- Denormalized at ingest by source file (parse_errors has no session link, so
    -- the file is the only marker available).
    is_subagent    INTEGER NOT NULL DEFAULT 0,
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
-- this text plus JSON noise). is_subagent is carried (appended AFTER text so
-- text stays column index 6 for snippet() in search.go) so default search
-- excludes subagents via a direct FTS column check.
CREATE TABLE search_documents(
    rowid       INTEGER PRIMARY KEY,
    kind        TEXT,
    id          TEXT,
    source_id   TEXT,
    session_id  TEXT,
    turn_id     TEXT,
    source_file TEXT,
    text        TEXT,
    is_subagent INTEGER NOT NULL DEFAULT 0
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
    is_subagent  UNINDEXED,
    content      = 'search_documents',
    content_rowid = 'rowid',
    tokenize = 'unicode61'
);

-- Keep search_fts consistent with search_documents. ingest replaces rows by
-- DELETE + INSERT, so the update trigger is included for completeness.
CREATE TRIGGER search_documents_ai AFTER INSERT ON search_documents BEGIN
    INSERT INTO search_fts(rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES (new.rowid, new.kind, new.id, new.source_id, new.session_id, new.turn_id, new.source_file, new.text, new.is_subagent);
END;
CREATE TRIGGER search_documents_ad AFTER DELETE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES ('delete', old.rowid, old.kind, old.id, old.source_id, old.session_id, old.turn_id, old.source_file, old.text, old.is_subagent);
END;
CREATE TRIGGER search_documents_au AFTER UPDATE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES ('delete', old.rowid, old.kind, old.id, old.source_id, old.session_id, old.turn_id, old.source_file, old.text, old.is_subagent);
    INSERT INTO search_fts(rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES (new.rowid, new.kind, new.id, new.source_id, new.session_id, new.turn_id, new.source_file, new.text, new.is_subagent);
END;

-- Indexes
CREATE INDEX idx_raw_events_source        ON raw_events(source_id);
CREATE INDEX idx_raw_events_time          ON raw_events(event_time);
CREATE INDEX idx_raw_events_session       ON raw_events(session_external_id);
CREATE INDEX idx_raw_events_parse_status  ON raw_events(parse_status);

CREATE INDEX idx_sessions_source          ON sessions(source_id);
CREATE INDEX idx_sessions_project         ON sessions(project_id);
CREATE INDEX idx_sessions_external        ON sessions(external_session_id);
-- Expression index matching ListLiveSessions' ORDER BY (the live-status poll), so
-- it scans sessions in last-activity order instead of building a TEMP B-TREE on
-- every call. The COALESCE/NULLIF expression must stay in sync with
-- listings.go:ListLiveSessions.
CREATE INDEX idx_sessions_last_activity   ON sessions(
    COALESCE(NULLIF(last_turn_at, ''), NULLIF(ended_at, ''), NULLIF(started_at, ''), '') DESC,
    id
);
-- Recency sort / last-activity display (an empty transcript sorts last, not
-- first). Mirrors sessionActivityTimeExpr in listings.go.
CREATE INDEX idx_sessions_activity_time   ON sessions(COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), ''));
-- Default-exclude (is_subagent=0) listing + recency sort in one composite.
CREATE INDEX idx_sessions_subagent_activity ON sessions(
    is_subagent,
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), '')
);
-- Parent-linkage lookups: all subagents of a session / all agents of a workflow run.
CREATE INDEX idx_sessions_parent_session  ON sessions(parent_session_id);
CREATE INDEX idx_sessions_workflow_run    ON sessions(workflow_run_id);
-- Partial index over only still-unresolved subagents, so resolveSubagentParents'
-- per-batch UPDATE scans just those (empty once everything is linked).
CREATE INDEX idx_sessions_subagent_unresolved ON sessions(parent_external_id)
    WHERE is_subagent = 1 AND parent_session_id IS NULL;

CREATE INDEX idx_turns_session            ON turns(session_id);
CREATE INDEX idx_turns_project            ON turns(project_id);
CREATE INDEX idx_turns_status             ON turns(status);
-- Recency sort. Mirrors turnActivityTimeExpr in listings.go (an activity-less
-- turn sorts last). The trailing turn_index/id let equal-timestamp turns sort
-- from the index.
CREATE INDEX idx_turns_activity_time ON turns(
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), ''),
    turn_index,
    id
);
-- Default-exclude (is_subagent=0) listing + recency sort in one composite.
CREATE INDEX idx_turns_subagent_activity ON turns(
    is_subagent,
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), '')
);

CREATE INDEX idx_invocations_turn         ON invocations(turn_id);
CREATE INDEX idx_invocations_session      ON invocations(session_id);
CREATE INDEX idx_invocations_model        ON invocations(model);

CREATE INDEX idx_tool_calls_turn          ON tool_calls(turn_id);
CREATE INDEX idx_tool_calls_session       ON tool_calls(session_id);
CREATE INDEX idx_tool_calls_invocation    ON tool_calls(invocation_id);
CREATE INDEX idx_tool_calls_kind          ON tool_calls(tool_kind);
CREATE INDEX idx_tool_calls_name          ON tool_calls(tool_name);
CREATE INDEX idx_tool_calls_mcp           ON tool_calls(mcp_server);
-- Time-range (--since/--until) scoped tool listings. Mirrors toolCallActivityTimeExpr;
-- trailing columns cover the GROUP BY.
CREATE INDEX idx_tool_calls_activity_time ON tool_calls(
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), ''),
    tool_name,
    tool_kind
);

CREATE INDEX idx_turn_components_turn     ON turn_components(turn_id);
CREATE INDEX idx_turn_components_kind     ON turn_components(component_kind);
CREATE INDEX idx_turn_components_relation ON turn_components(relation);
CREATE INDEX idx_turn_components_name     ON turn_components(component_name);

CREATE INDEX idx_skills_source            ON skills(source_id);
CREATE INDEX idx_mcp_servers_source       ON mcp_servers(source_id);

CREATE INDEX idx_rule_suggestions_rule    ON rule_suggestions(rule_id);
CREATE INDEX idx_rule_suggestions_scope   ON rule_suggestions(scope_kind, scope_id);

CREATE INDEX idx_parse_errors_source      ON parse_errors(source_id);

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
DROP INDEX IF EXISTS idx_parse_errors_source;
DROP INDEX IF EXISTS idx_rule_suggestions_scope;
DROP INDEX IF EXISTS idx_rule_suggestions_rule;
DROP INDEX IF EXISTS idx_mcp_servers_source;
DROP INDEX IF EXISTS idx_skills_source;
DROP INDEX IF EXISTS idx_turn_components_name;
DROP INDEX IF EXISTS idx_turn_components_relation;
DROP INDEX IF EXISTS idx_turn_components_kind;
DROP INDEX IF EXISTS idx_turn_components_turn;
DROP INDEX IF EXISTS idx_tool_calls_activity_time;
DROP INDEX IF EXISTS idx_tool_calls_mcp;
DROP INDEX IF EXISTS idx_tool_calls_name;
DROP INDEX IF EXISTS idx_tool_calls_kind;
DROP INDEX IF EXISTS idx_tool_calls_invocation;
DROP INDEX IF EXISTS idx_tool_calls_session;
DROP INDEX IF EXISTS idx_tool_calls_turn;
DROP INDEX IF EXISTS idx_invocations_model;
DROP INDEX IF EXISTS idx_invocations_session;
DROP INDEX IF EXISTS idx_invocations_turn;
DROP INDEX IF EXISTS idx_turns_subagent_activity;
DROP INDEX IF EXISTS idx_turns_activity_time;
DROP INDEX IF EXISTS idx_turns_status;
DROP INDEX IF EXISTS idx_turns_project;
DROP INDEX IF EXISTS idx_turns_session;
DROP INDEX IF EXISTS idx_sessions_subagent_unresolved;
DROP INDEX IF EXISTS idx_sessions_workflow_run;
DROP INDEX IF EXISTS idx_sessions_parent_session;
DROP INDEX IF EXISTS idx_sessions_subagent_activity;
DROP INDEX IF EXISTS idx_sessions_activity_time;
DROP INDEX IF EXISTS idx_sessions_last_activity;
DROP INDEX IF EXISTS idx_sessions_external;
DROP INDEX IF EXISTS idx_sessions_project;
DROP INDEX IF EXISTS idx_sessions_source;
DROP INDEX IF EXISTS idx_raw_events_parse_status;
DROP INDEX IF EXISTS idx_raw_events_session;
DROP INDEX IF EXISTS idx_raw_events_time;
DROP INDEX IF EXISTS idx_raw_events_source;

DROP TRIGGER IF EXISTS search_documents_au;
DROP TRIGGER IF EXISTS search_documents_ad;
DROP TRIGGER IF EXISTS search_documents_ai;
DROP TABLE IF EXISTS search_fts;
DROP INDEX IF EXISTS idx_search_documents_source;
DROP TABLE IF EXISTS search_documents;
DROP TABLE IF EXISTS parse_errors;
DROP TABLE IF EXISTS rule_suggestions;
DROP TABLE IF EXISTS turn_components;
DROP TABLE IF EXISTS mcp_servers;
DROP TABLE IF EXISTS skills;
DROP TABLE IF EXISTS tool_calls;
DROP TABLE IF EXISTS invocations;
DROP TABLE IF EXISTS turns;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS ingest_offsets;
DROP TABLE IF EXISTS raw_events;
DROP TABLE IF EXISTS source_roots;
DROP TABLE IF EXISTS sources;
-- +goose StatementEnd

-- +goose Up

-- Claude Code subagent transcripts (a Task/Agent run, or a Workflow's internal
-- agents) are now ingested as first-class but marked+linked sessions, so
-- aggregations (tools/skills/mcps) can include them while default listings
-- exclude them. These columns carry the marker + parent linkage; they stay
-- 0/NULL for every top-level session and every provider without subagents.
ALTER TABLE sessions ADD COLUMN is_subagent        INTEGER NOT NULL DEFAULT 0;
-- parent_external_id is the link the parser sets (claude-code: the subagent's
-- in-file sessionId, which is the parent's external id; codex: parent_thread_id).
-- parent_session_id (the internal FK used by handoff/listings) is resolved from it
-- by a post-pass at ingest time (same provider, top-level parent) — one mechanism
-- for both providers, since claude's nested path and codex's flat layout both
-- reduce to "link by the parent's external id". The resolution subquery matches on
-- external_session_id (idx_sessions_external, 00001); its outer scan of still-
-- unresolved subagents is backed by the partial index below so it stays cheap once
-- everything is linked, even though it runs on every ingest batch.
ALTER TABLE sessions ADD COLUMN parent_external_id TEXT;
ALTER TABLE sessions ADD COLUMN parent_session_id  TEXT;
ALTER TABLE sessions ADD COLUMN parent_tool_use_id TEXT;
ALTER TABLE sessions ADD COLUMN workflow_run_id    TEXT;
ALTER TABLE sessions ADD COLUMN subagent_kind      TEXT;
ALTER TABLE sessions ADD COLUMN agent_type         TEXT;

-- Denormalized onto turns and raw_events so the default-exclude predicate is a
-- direct, indexable column check rather than a sessions subquery. raw_events
-- specifically MUST carry its own marker: its session_external_id is the PARENT
-- session uuid (shared by parent and all its subagents), so it cannot distinguish
-- a subagent's raw events on its own. tool_calls needs no column — every listing
-- that filters tool calls already joins sessions and keys off sessions.is_subagent.
ALTER TABLE turns      ADD COLUMN is_subagent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE raw_events ADD COLUMN is_subagent INTEGER NOT NULL DEFAULT 0;

-- Default listings filter is_subagent = 0 and sort by activity time; the composite
-- (is_subagent, activity_expr) backs both the predicate and the ORDER BY in one
-- index. The plain idx_{sessions,turns}_activity_time (00004) still backs the
-- opt-in --subagents path (no is_subagent predicate). Index expressions mirror
-- sessionActivityTimeExpr / turnActivityTimeExpr in listings.go.
CREATE INDEX idx_sessions_subagent_activity ON sessions(
    is_subagent,
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), '')
);
CREATE INDEX idx_turns_subagent_activity ON turns(
    is_subagent,
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), '')
);

-- Parent-linkage lookups: "all subagents of this session" / "all agents of this
-- workflow run" (the handoff and any subagent drill-down).
CREATE INDEX idx_sessions_parent_session ON sessions(parent_session_id);
CREATE INDEX idx_sessions_workflow_run   ON sessions(workflow_run_id);

-- Partial index over only the still-unresolved subagents, so resolveSubagentParents'
-- per-batch UPDATE scans just those (empty once everything is linked) instead of the
-- whole is_subagent=1 partition. Rows leave the index as parent_session_id is set.
CREATE INDEX idx_sessions_subagent_unresolved ON sessions(parent_external_id)
    WHERE is_subagent = 1 AND parent_session_id IS NULL;

-- Denormalize is_subagent onto parse_errors (by source file at ingest, like
-- raw_events): parse_errors has no session link, so the file is the only marker, and
-- a direct column avoids the NULL-source_file NOT-IN trap a sessions subquery hits.
ALTER TABLE parse_errors ADD COLUMN is_subagent INTEGER NOT NULL DEFAULT 0;

-- Denormalize is_subagent onto the search index too, so default search excludes
-- subagents via a direct FTS column check (search_fts.is_subagent = 0) instead of a
-- sessions subquery — consistent with turns/raw_events. FTS5 tables can't be ALTERed,
-- so search_fts and its sync triggers are dropped and recreated with the new column.
-- It is appended AFTER text so text stays column index 6 (snippet() in search.go).
ALTER TABLE search_documents ADD COLUMN is_subagent INTEGER NOT NULL DEFAULT 0;
DROP TRIGGER search_documents_ai;
DROP TRIGGER search_documents_ad;
DROP TRIGGER search_documents_au;
DROP TABLE search_fts;
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
-- +goose StatementBegin
CREATE TRIGGER search_documents_ai AFTER INSERT ON search_documents BEGIN
    INSERT INTO search_fts(rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES (new.rowid, new.kind, new.id, new.source_id, new.session_id, new.turn_id, new.source_file, new.text, new.is_subagent);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER search_documents_ad AFTER DELETE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES ('delete', old.rowid, old.kind, old.id, old.source_id, old.session_id, old.turn_id, old.source_file, old.text, old.is_subagent);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER search_documents_au AFTER UPDATE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES ('delete', old.rowid, old.kind, old.id, old.source_id, old.session_id, old.turn_id, old.source_file, old.text, old.is_subagent);
    INSERT INTO search_fts(rowid, kind, id, source_id, session_id, turn_id, source_file, text, is_subagent)
    VALUES (new.rowid, new.kind, new.id, new.source_id, new.session_id, new.turn_id, new.source_file, new.text, new.is_subagent);
END;
-- +goose StatementEnd

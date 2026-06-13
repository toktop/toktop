-- +goose Up

-- 00001 is the original baseline. This migration narrows the projected schema
-- to the tables and columns the current store actually produces, and adds the
-- structural fields needed by incremental ingest and provider-scoped metadata.

DROP INDEX IF EXISTS idx_invocations_subagent;
DROP INDEX IF EXISTS idx_tool_calls_subagent;
DROP INDEX IF EXISTS idx_subagent_runs_turn;
DROP INDEX IF EXISTS idx_context_events_turn;
DROP INDEX IF EXISTS idx_context_events_session;
DROP INDEX IF EXISTS idx_context_events_subagent;
DROP INDEX IF EXISTS idx_context_events_type;
-- The bare started_at indexes from 00001 are superseded by the COALESCE
-- effective-time indexes (idx_turns_effective_time / idx_tool_calls_effective_time
-- created below; idx_sessions_effective_age already exists in 00001) that every
-- ORDER BY and since/until range now uses. Drop all three together.
DROP INDEX IF EXISTS idx_tool_calls_started;
DROP INDEX IF EXISTS idx_turns_started;
DROP INDEX IF EXISTS idx_sessions_started;
DROP INDEX IF EXISTS idx_turn_components_created;

ALTER TABLE sources DROP COLUMN display_name;
ALTER TABLE sources DROP COLUMN enabled;
ALTER TABLE sources DROP COLUMN capabilities;

ALTER TABLE source_roots DROP COLUMN kind;
ALTER TABLE source_roots DROP COLUMN priority;
ALTER TABLE source_roots DROP COLUMN enabled;
ALTER TABLE source_roots DROP COLUMN last_scan_at;

ALTER TABLE projects DROP COLUMN path_hash;

ALTER TABLE turns DROP COLUMN failure_reason;
ALTER TABLE turns DROP COLUMN subagent_count;

ALTER TABLE invocations DROP COLUMN subagent_run_id;

ALTER TABLE tool_calls DROP COLUMN subagent_run_id;
ALTER TABLE tool_calls DROP COLUMN output_ref;

DROP TABLE IF EXISTS context_events;
DROP TABLE IF EXISTS tool_outputs;
DROP TABLE IF EXISTS subagent_runs;

CREATE TABLE skills_next(
    id             TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    scope          TEXT NOT NULL DEFAULT 'unknown',
    source_path    TEXT,
    source_hash    TEXT,
    description    TEXT,
    version        TEXT,
    argument_hint  TEXT,
    user_invocable INTEGER,
    triggers       TEXT,
    allowed_tools  TEXT,
    tools          TEXT,
    compatibility  TEXT,
    license        TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE(source_id, name, scope, source_path)
);

INSERT INTO skills_next(
    id, source_id, name, scope, source_path, source_hash,
    description, version, argument_hint, user_invocable,
    triggers, allowed_tools, tools, compatibility, license,
    created_at, updated_at
)
SELECT skills.id,
       (SELECT sources.id FROM sources ORDER BY sources.kind LIMIT 1),
       skills.name, skills.scope, skills.source_path, skills.source_hash,
       skills.description, skills.version, skills.argument_hint, skills.user_invocable,
       skills.triggers, skills.allowed_tools, skills.tools, skills.compatibility, skills.license,
       skills.created_at, skills.updated_at
FROM skills
WHERE EXISTS (SELECT 1 FROM sources);

DROP TABLE skills;
ALTER TABLE skills_next RENAME TO skills;

CREATE TABLE mcp_servers_next(
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

INSERT INTO mcp_servers_next(
    id, source_id, name, scope, transport, config_path, config_hash, enabled, created_at, updated_at
)
SELECT mcp_servers.id,
       (SELECT sources.id FROM sources ORDER BY sources.kind LIMIT 1),
       mcp_servers.name, mcp_servers.scope, mcp_servers.transport,
       mcp_servers.config_path, mcp_servers.config_hash, mcp_servers.enabled,
       mcp_servers.created_at, mcp_servers.updated_at
FROM mcp_servers
WHERE EXISTS (SELECT 1 FROM sources);

DROP TABLE mcp_servers;
ALTER TABLE mcp_servers_next RENAME TO mcp_servers;

CREATE INDEX idx_skills_source      ON skills(source_id);
CREATE INDEX idx_mcp_servers_source ON mcp_servers(source_id);

CREATE INDEX idx_turns_effective_time ON turns(
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), created_at),
    turn_index,
    id
);
CREATE INDEX idx_tool_calls_effective_time ON tool_calls(
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), created_at),
    tool_name,
    tool_kind
);

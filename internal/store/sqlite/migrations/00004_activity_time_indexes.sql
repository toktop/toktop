-- +goose Up

-- Recency sorts and "last activity" displays now order by activity time
-- (COALESCE of started_at/ended_at, '' when absent) instead of the import-floored
-- effective time: a created_at/imported_at floor makes an activity-less row
-- masquerade as the most recent in a DESC sort. The 00002 effective-time indexes
-- carried that floor, so re-create them without it to match the queries.
DROP INDEX IF EXISTS idx_turns_effective_time;
CREATE INDEX idx_turns_activity_time ON turns(
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), ''),
    turn_index,
    id
);
DROP INDEX IF EXISTS idx_tool_calls_effective_time;
CREATE INDEX idx_tool_calls_activity_time ON tool_calls(
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), ''),
    tool_name,
    tool_kind
);

-- Sessions need both: idx_sessions_effective_age (with the created_at floor,
-- from 00001) still backs retention/redaction and since/until range filtering,
-- while this activity-time index backs the sessions/projects recency sort and
-- last-activity display, where an empty transcript must sort last, not first.
CREATE INDEX idx_sessions_activity_time ON sessions(COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), ''));

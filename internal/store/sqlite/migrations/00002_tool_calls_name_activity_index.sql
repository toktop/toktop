-- +goose Up
-- +goose StatementBegin

-- Composite index for the tool-call drill-down (ListToolCalls / `tools calls` /
-- GET /v1/tool-calls): WHERE tool_name = ? ORDER BY <activity-time> DESC LIMIT N.
-- idx_tool_calls_name alone seeks by name but leaves the planner to sort every
-- matching row by the COALESCE(...) activity expression to take the top N; a
-- high-volume builtin (Bash, tens of thousands of calls) sorts the whole set per
-- drill-down. Leading with tool_name then the activity expression (matching
-- toolCallActivityTimeExpr) lets the planner read in order and stop at the LIMIT.
CREATE INDEX idx_tool_calls_name_activity ON tool_calls(
    tool_name,
    COALESCE(NULLIF(started_at, ''), NULLIF(ended_at, ''), '') DESC
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tool_calls_name_activity;
-- +goose StatementEnd

-- +goose Up

-- Drop two projected columns that no provider's source data can fill: turns.summary
-- (no transcript carries a turn summary) and invocations.latency_ms (neither parser
-- records per-invocation latency; start/end are the same instant). Removing them keeps
-- the neutral projection uniformly populated across providers.

ALTER TABLE turns DROP COLUMN summary;
ALTER TABLE invocations DROP COLUMN latency_ms;

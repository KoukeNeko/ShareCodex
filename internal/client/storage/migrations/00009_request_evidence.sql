-- +goose Up
ALTER TABLE events ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN parent_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN effort TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN status TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN aggregated INTEGER NOT NULL DEFAULT 0;

CREATE TABLE limit_events (
    dedupe_key       TEXT PRIMARY KEY,
    provider         TEXT NOT NULL,
    account_ref_hash TEXT NOT NULL,
    occurred_at      INTEGER NOT NULL,
    observed_at      INTEGER NOT NULL,
    session_id       TEXT NOT NULL DEFAULT '',
    request_id       TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL,
    source           TEXT NOT NULL DEFAULT '',
    evidence         TEXT NOT NULL DEFAULT '',
    http_status      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX limit_events_time ON limit_events (occurred_at);

-- +goose Down
DROP TABLE limit_events;
ALTER TABLE events DROP COLUMN aggregated;
ALTER TABLE events DROP COLUMN status;
ALTER TABLE events DROP COLUMN effort;
ALTER TABLE events DROP COLUMN parent_request_id;
ALTER TABLE events DROP COLUMN request_id;

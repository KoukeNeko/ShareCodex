-- +goose Up
ALTER TABLE usage_events ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_events ADD COLUMN parent_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_events ADD COLUMN effort TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_events ADD COLUMN status TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_events ADD COLUMN aggregated BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE limit_events (
    dedupe_key       TEXT PRIMARY KEY,
    account_id       TEXT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    person_id        TEXT NOT NULL REFERENCES persons (id),
    device_id        TEXT NOT NULL REFERENCES devices (id),
    provider         TEXT NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    observed_at      TIMESTAMPTZ NOT NULL,
    session_id       TEXT NOT NULL DEFAULT '',
    request_id       TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL,
    source           TEXT NOT NULL DEFAULT '',
    evidence         TEXT NOT NULL DEFAULT '',
    http_status      INTEGER NOT NULL DEFAULT 0,
    received_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX limit_events_account_time ON limit_events (account_id, occurred_at);

-- +goose Down
DROP TABLE limit_events;
ALTER TABLE usage_events DROP COLUMN aggregated;
ALTER TABLE usage_events DROP COLUMN status;
ALTER TABLE usage_events DROP COLUMN effort;
ALTER TABLE usage_events DROP COLUMN parent_request_id;
ALTER TABLE usage_events DROP COLUMN request_id;

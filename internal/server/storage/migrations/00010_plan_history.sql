-- +goose Up
ALTER TABLE quota_snapshots ADD COLUMN plan_type TEXT NOT NULL DEFAULT '';

CREATE TABLE plan_intervals (
    id           TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    plan_type    TEXT NOT NULL,
    effective_at TIMESTAMPTZ NOT NULL,
    ended_at     TIMESTAMPTZ,
    reason       TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL DEFAULT '',
    precision    TEXT NOT NULL DEFAULT 'confirmed',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ended_at IS NULL OR ended_at > effective_at)
);
CREATE INDEX plan_intervals_account_time ON plan_intervals (account_id, effective_at);

-- +goose Down
DROP TABLE plan_intervals;
ALTER TABLE quota_snapshots DROP COLUMN plan_type;

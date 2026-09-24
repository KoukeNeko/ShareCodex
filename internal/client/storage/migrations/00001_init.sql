-- +goose Up
CREATE TABLE files (
    path        TEXT PRIMARY KEY,
    size        INTEGER NOT NULL,
    mod_time_ns INTEGER NOT NULL
);

-- ref_hash is empty when the device was logged in without a pooled
-- subscription (e.g. an API key); events in that period are not recorded.
CREATE TABLE observations (
    provider    TEXT    NOT NULL,
    ref_hash    TEXT    NOT NULL,
    hint        TEXT    NOT NULL,
    plan_type   TEXT    NOT NULL,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (provider, observed_at)
);

CREATE TABLE events (
    dedupe_key       TEXT PRIMARY KEY,
    account_ref_hash TEXT    NOT NULL,
    provider         TEXT    NOT NULL,
    product          TEXT    NOT NULL,
    originator       TEXT    NOT NULL,
    session_id       TEXT    NOT NULL,
    model            TEXT    NOT NULL,
    occurred_at      INTEGER NOT NULL,
    input            INTEGER NOT NULL,
    cached_input     INTEGER NOT NULL,
    cache_write      INTEGER NOT NULL,
    output           INTEGER NOT NULL,
    reasoning_output INTEGER NOT NULL
);
CREATE INDEX events_account_time ON events (account_ref_hash, occurred_at);

CREATE TABLE snapshots (
    provider         TEXT    NOT NULL,
    account_ref_hash TEXT    NOT NULL,
    source           TEXT    NOT NULL,
    observed_at      INTEGER NOT NULL,
    buckets          TEXT    NOT NULL,
    PRIMARY KEY (provider, account_ref_hash, source, observed_at)
);

CREATE TABLE outbox (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    kind    TEXT NOT NULL,
    payload TEXT NOT NULL
);

-- +goose Down
DROP TABLE outbox;
DROP TABLE snapshots;
DROP TABLE events;
DROP TABLE observations;
DROP TABLE files;

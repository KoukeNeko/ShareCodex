-- +goose Up
CREATE TABLE persons (
    id           TEXT PRIMARY KEY,
    display_name TEXT        NOT NULL UNIQUE,
    is_admin     BOOLEAN     NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE devices (
    id           TEXT PRIMARY KEY,
    person_id    TEXT        NOT NULL REFERENCES persons (id),
    name         TEXT        NOT NULL,
    platform     TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE TABLE invites (
    code_hash  TEXT PRIMARY KEY,
    person_id  TEXT        NOT NULL REFERENCES persons (id),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    device_id  TEXT REFERENCES devices (id)
);

-- Accounts are created on first sight of a provider account reference; an
-- admin only renames them.
CREATE TABLE accounts (
    id         TEXT PRIMARY KEY,
    provider   TEXT        NOT NULL,
    ref_hash   TEXT        NOT NULL,
    hint       TEXT        NOT NULL,
    label      TEXT        NOT NULL,
    plan_type  TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, ref_hash)
);

-- A member with share_weight 0 is removed from allotment but keeps their
-- recorded usage; the row stays so automatic membership does not re-add them.
CREATE TABLE memberships (
    account_id   TEXT             NOT NULL REFERENCES accounts (id),
    person_id    TEXT             NOT NULL REFERENCES persons (id),
    share_weight DOUBLE PRECISION NOT NULL DEFAULT 1 CHECK (share_weight >= 0),
    created_at   TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, person_id)
);

CREATE TABLE observations (
    device_id   TEXT        NOT NULL REFERENCES devices (id),
    account_id  TEXT        NOT NULL REFERENCES accounts (id),
    observed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (device_id, account_id, observed_at)
);

CREATE TABLE usage_events (
    dedupe_key       TEXT PRIMARY KEY,
    account_id       TEXT        NOT NULL REFERENCES accounts (id),
    person_id        TEXT        NOT NULL REFERENCES persons (id),
    device_id        TEXT        NOT NULL REFERENCES devices (id),
    product          TEXT        NOT NULL,
    originator       TEXT        NOT NULL,
    session_id       TEXT        NOT NULL,
    model            TEXT        NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    input            BIGINT      NOT NULL,
    cached_input     BIGINT      NOT NULL,
    cache_write      BIGINT      NOT NULL,
    output           BIGINT      NOT NULL,
    reasoning_output BIGINT      NOT NULL,
    received_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX usage_events_account_time ON usage_events (account_id, occurred_at);

CREATE TABLE quota_snapshots (
    account_id  TEXT        NOT NULL REFERENCES accounts (id),
    device_id   TEXT        NOT NULL REFERENCES devices (id),
    source      TEXT        NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    buckets     JSONB       NOT NULL,
    PRIMARY KEY (account_id, device_id, source, observed_at)
);
CREATE INDEX quota_snapshots_account_time ON quota_snapshots (account_id, observed_at);

-- +goose Down
DROP TABLE quota_snapshots;
DROP TABLE usage_events;
DROP TABLE observations;
DROP TABLE memberships;
DROP TABLE accounts;
DROP TABLE invites;
DROP TABLE devices;
DROP TABLE persons;

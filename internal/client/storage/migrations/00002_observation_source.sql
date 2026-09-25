-- +goose Up
-- Claude Desktop signs in separately from the claude CLI, so each app keeps
-- its own timeline; the CLI's source is empty.
CREATE TABLE observations_new (
    provider    TEXT    NOT NULL,
    source      TEXT    NOT NULL DEFAULT '',
    ref_hash    TEXT    NOT NULL,
    hint        TEXT    NOT NULL,
    plan_type   TEXT    NOT NULL,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (provider, source, observed_at)
);
INSERT INTO observations_new (provider, ref_hash, hint, plan_type, observed_at)
    SELECT provider, ref_hash, hint, plan_type, observed_at FROM observations;
DROP TABLE observations;
ALTER TABLE observations_new RENAME TO observations;

-- +goose Down
CREATE TABLE observations_old (
    provider    TEXT    NOT NULL,
    ref_hash    TEXT    NOT NULL,
    hint        TEXT    NOT NULL,
    plan_type   TEXT    NOT NULL,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (provider, observed_at)
);
INSERT INTO observations_old (provider, ref_hash, hint, plan_type, observed_at)
    SELECT provider, ref_hash, hint, plan_type, observed_at FROM observations WHERE source = '';
DROP TABLE observations;
ALTER TABLE observations_old RENAME TO observations;

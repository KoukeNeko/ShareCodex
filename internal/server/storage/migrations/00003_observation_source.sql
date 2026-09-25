-- +goose Up
-- Claude Desktop signs in separately from the claude CLI, so a device can be
-- on a different account in each; the CLI's source is empty.
ALTER TABLE observations ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE observations DROP CONSTRAINT observations_pkey;
ALTER TABLE observations ADD PRIMARY KEY (device_id, account_id, source, observed_at);
-- The overview looks up every device's latest observation in a recent window.
CREATE INDEX observations_time ON observations (observed_at);

-- +goose Down
DROP INDEX observations_time;
DELETE FROM observations WHERE source <> '';
ALTER TABLE observations DROP CONSTRAINT observations_pkey;
ALTER TABLE observations ADD PRIMARY KEY (device_id, account_id, observed_at);
ALTER TABLE observations DROP COLUMN source;

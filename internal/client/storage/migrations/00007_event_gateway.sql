-- +goose Up
-- The service a third-party request went through, when the log shows it.
ALTER TABLE events ADD COLUMN gateway TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE events DROP COLUMN gateway;

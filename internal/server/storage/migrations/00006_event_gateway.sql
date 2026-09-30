-- +goose Up
-- The service a third-party request went through, when the client's log
-- shows it.
ALTER TABLE usage_events ADD COLUMN gateway TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE usage_events DROP COLUMN gateway;

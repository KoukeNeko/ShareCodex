-- +goose Up
-- Requests from a Claude client to another vendor's model: recorded against
-- the account the client used, never counted in its quota.
ALTER TABLE usage_events ADD COLUMN third_party BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE usage_events DROP COLUMN third_party;

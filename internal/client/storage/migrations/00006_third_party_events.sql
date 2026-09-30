-- +goose Up
-- Requests from a Claude client to another vendor's model, recorded against
-- the client's account but never counted in its quota.
ALTER TABLE events ADD COLUMN third_party INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE events DROP COLUMN third_party;

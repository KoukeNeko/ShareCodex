-- +goose Up
-- The order an admin arranged accounts in; accounts never arranged follow
-- the arranged ones.
ALTER TABLE accounts ADD COLUMN position INTEGER;

-- +goose Down
ALTER TABLE accounts DROP COLUMN position;

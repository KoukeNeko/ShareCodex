-- +goose Up
ALTER TABLE snapshots ADD COLUMN plan_type TEXT NOT NULL DEFAULT '';
ALTER TABLE snapshots ADD COLUMN account_hint TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE snapshots DROP COLUMN plan_type;
ALTER TABLE snapshots DROP COLUMN account_hint;

-- +goose Up
-- Admin access moved to the web console, which uses ADMIN_PASSWORD.
ALTER TABLE persons DROP COLUMN is_admin;

-- +goose Down
ALTER TABLE persons ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT false;

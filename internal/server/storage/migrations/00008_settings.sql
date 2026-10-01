-- +goose Up
-- Server-wide choices an admin makes in the console, such as publishing the
-- dashboard.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;

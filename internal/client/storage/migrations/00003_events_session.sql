-- +goose Up
-- A statusLine reading is attributed through its session's entrypoint.
CREATE INDEX events_session ON events (session_id);

-- +goose Down
DROP INDEX events_session;

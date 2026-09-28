-- +goose Up
-- Claude accounts signed in to ShareCodex itself; their tokens live in the
-- OS credential store, keyed by ref_hash.
CREATE TABLE claude_logins (
    ref_hash  TEXT PRIMARY KEY,
    hint      TEXT NOT NULL,
    plan_type TEXT NOT NULL
);

-- +goose Down
DROP TABLE claude_logins;

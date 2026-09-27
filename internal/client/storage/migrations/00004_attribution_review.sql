-- +goose Up
-- Existing transcript files need one re-read after account attribution changes.
CREATE TABLE attribution_review (
    id INTEGER PRIMARY KEY CHECK (id = 1)
);

-- +goose Down
DROP TABLE attribution_review;

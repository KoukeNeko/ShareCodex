-- +goose Up
-- When the saved plan was observed, so a resync that replays old sign-ins
-- cannot bring back a plan the account has since changed from.
ALTER TABLE accounts ADD COLUMN plan_observed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE accounts DROP COLUMN plan_observed_at;

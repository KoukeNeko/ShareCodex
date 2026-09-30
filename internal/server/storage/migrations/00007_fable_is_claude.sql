-- +goose Up
-- Clients before 0.3.32 took Claude's Fable models for third-party models.
UPDATE usage_events SET third_party = false, gateway = ''
WHERE third_party AND model ~ '^claude-fable-' AND gateway = '';

-- +goose Down
SELECT 1;

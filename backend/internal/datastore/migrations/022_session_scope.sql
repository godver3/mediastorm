-- +goose Up
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions
    DROP COLUMN IF EXISTS scope;

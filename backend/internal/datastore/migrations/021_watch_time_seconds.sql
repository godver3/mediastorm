-- +goose Up
ALTER TABLE watch_history
    ADD COLUMN IF NOT EXISTS watched_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;

ALTER TABLE playback_progress
    ADD COLUMN IF NOT EXISTS watched_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE playback_progress
    DROP COLUMN IF EXISTS watched_seconds;

ALTER TABLE watch_history
    DROP COLUMN IF EXISTS watched_seconds;

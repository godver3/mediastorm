-- +goose Up
ALTER TABLE watch_rooms ADD COLUMN IF NOT EXISTS episode JSONB;
ALTER TABLE watch_room_external_sources ADD COLUMN IF NOT EXISTS episode_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE watch_room_external_sources DROP COLUMN IF EXISTS episode_key;
ALTER TABLE watch_rooms DROP COLUMN IF EXISTS episode;

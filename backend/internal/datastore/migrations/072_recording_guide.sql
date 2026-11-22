-- +goose Up
-- What the guide said about a recorded programme (image, episode title and
-- number, year, categories, rating), kept because the guide moves on.
ALTER TABLE recordings ADD COLUMN IF NOT EXISTS guide JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE recordings DROP COLUMN IF EXISTS guide;

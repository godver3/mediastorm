-- +goose Up
ALTER TABLE recording_rules ADD COLUMN new_only BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE recording_rules DROP COLUMN IF EXISTS new_only;

-- +goose Up
ALTER TABLE users ADD COLUMN IF NOT EXISTS mdblist_account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE users DROP COLUMN IF EXISTS mdblist_scrobbling_enabled;

-- +goose Down
ALTER TABLE users ADD COLUMN IF NOT EXISTS mdblist_scrobbling_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users DROP COLUMN IF EXISTS mdblist_account_id;

-- +goose Up
-- Originally added mdblist_scrobbling_enabled, but 003 replaces it with mdblist_account_id.
-- For fresh installs this creates a column that 003 immediately drops - harmless but redundant.
-- Kept as-is to preserve migration version numbering for existing databases.
ALTER TABLE users ADD COLUMN IF NOT EXISTS mdblist_scrobbling_enabled BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS mdblist_scrobbling_enabled;

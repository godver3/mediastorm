-- +goose Up
ALTER TABLE users ADD COLUMN IF NOT EXISTS simkl_account_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS simkl_account_id;

-- +goose Up
ALTER TABLE remote_access_invites ADD COLUMN IF NOT EXISTS iroh_invite TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE remote_access_invites DROP COLUMN IF EXISTS iroh_invite;

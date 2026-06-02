-- +goose Up
CREATE TABLE IF NOT EXISTS remote_access_invites (
    id TEXT PRIMARY KEY,
    token_hash TEXT UNIQUE NOT NULL,
    connection_code TEXT NOT NULL DEFAULT '',
    iroh_invite TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    peer_name TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    used_by_peer_id TEXT NOT NULL DEFAULT '',
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_remote_access_invites_created_at ON remote_access_invites(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_remote_access_invites_expires_at ON remote_access_invites(expires_at);

-- +goose Down
DROP TABLE IF EXISTS remote_access_invites;

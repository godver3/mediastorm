-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS watchlist_tombstones (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    external_ids JSONB NOT NULL DEFAULT '{}',
    removed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, item_key)
);

CREATE INDEX IF NOT EXISTS idx_watchlist_tombstones_user_id ON watchlist_tombstones(user_id);
CREATE INDEX IF NOT EXISTS idx_watchlist_tombstones_removed_at ON watchlist_tombstones(removed_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS watchlist_tombstones;

-- +goose StatementEnd

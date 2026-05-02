-- +goose Up
CREATE TABLE recordings (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    channel_id TEXT NOT NULL DEFAULT '',
    tvg_id TEXT NOT NULL DEFAULT '',
    channel_name TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    padding_before_seconds INTEGER NOT NULL DEFAULT 0,
    padding_after_seconds INTEGER NOT NULL DEFAULT 0,
    output_path TEXT NOT NULL DEFAULT '',
    output_size_bytes BIGINT NOT NULL DEFAULT 0,
    actual_start_at TIMESTAMPTZ,
    actual_end_at TIMESTAMPTZ,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_recordings_user_id ON recordings(user_id);
CREATE INDEX idx_recordings_status_start_at ON recordings(status, start_at);

-- +goose Down
DROP TABLE IF EXISTS recordings;

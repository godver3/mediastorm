-- +goose Up
-- Sonarr/Radarr-compatible quality presets (quality profile + custom formats)
-- with their upstream, override and sync state as one JSON document.
CREATE TABLE IF NOT EXISTS quality_presets (
    id TEXT PRIMARY KEY,
    app TEXT NOT NULL,
    name TEXT NOT NULL,
    data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Downloaded TRaSH-format snapshot archives, so pinned commits and restarts
-- do not refetch from GitHub.
CREATE TABLE IF NOT EXISTS quality_snapshot_cache (
    source_id TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    archive BYTEA NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, commit_id)
);

-- +goose Down
DROP TABLE IF EXISTS quality_snapshot_cache;
DROP TABLE IF EXISTS quality_presets;

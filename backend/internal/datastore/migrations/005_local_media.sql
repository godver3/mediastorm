-- +goose Up
-- +goose StatementBegin

CREATE TABLE local_media_libraries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    library_type TEXT NOT NULL,
    root_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_scan_started_at TIMESTAMPTZ,
    last_scan_finished_at TIMESTAMPTZ,
    last_scan_status TEXT NOT NULL DEFAULT 'idle',
    last_scan_error TEXT NOT NULL DEFAULT '',
    last_scan_discovered INTEGER NOT NULL DEFAULT 0,
    last_scan_matched INTEGER NOT NULL DEFAULT 0,
    last_scan_low_confidence INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE local_media_items (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES local_media_libraries(id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    file_path TEXT NOT NULL,
    file_name TEXT NOT NULL DEFAULT '',
    library_type TEXT NOT NULL,
    detected_title TEXT NOT NULL DEFAULT '',
    detected_year INTEGER NOT NULL DEFAULT 0,
    season_number INTEGER NOT NULL DEFAULT 0,
    episode_number INTEGER NOT NULL DEFAULT 0,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    match_status TEXT NOT NULL DEFAULT 'unmatched',
    matched_title_id TEXT NOT NULL DEFAULT '',
    matched_media_type TEXT NOT NULL DEFAULT '',
    matched_name TEXT NOT NULL DEFAULT '',
    matched_year INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT 'null',
    probe JSONB NOT NULL DEFAULT 'null',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    modified_at TIMESTAMPTZ,
    last_scanned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (library_id, relative_path)
);

CREATE INDEX idx_local_media_items_library_id ON local_media_items(library_id);
CREATE INDEX idx_local_media_items_match_status ON local_media_items(match_status);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS local_media_items;
DROP TABLE IF EXISTS local_media_libraries;
-- +goose StatementEnd

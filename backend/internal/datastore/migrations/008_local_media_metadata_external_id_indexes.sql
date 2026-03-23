-- +goose Up
-- +goose StatementBegin

CREATE INDEX IF NOT EXISTS idx_local_media_items_metadata_imdb_id
    ON local_media_items ((metadata->>'imdbId'))
    WHERE COALESCE(metadata->>'imdbId', '') <> '';

CREATE INDEX IF NOT EXISTS idx_local_media_items_metadata_tmdb_id
    ON local_media_items (((metadata->>'tmdbId')::bigint))
    WHERE COALESCE(metadata->>'tmdbId', '') ~ '^[0-9]+$';

CREATE INDEX IF NOT EXISTS idx_local_media_items_metadata_tvdb_id
    ON local_media_items (((metadata->>'tvdbId')::bigint))
    WHERE COALESCE(metadata->>'tvdbId', '') ~ '^[0-9]+$';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_local_media_items_metadata_tvdb_id;
DROP INDEX IF EXISTS idx_local_media_items_metadata_tmdb_id;
DROP INDEX IF EXISTS idx_local_media_items_metadata_imdb_id;

-- +goose StatementEnd

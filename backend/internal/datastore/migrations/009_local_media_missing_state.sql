-- +goose Up
-- +goose StatementBegin

ALTER TABLE local_media_items
    ADD COLUMN IF NOT EXISTS is_missing BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS missing_since TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_local_media_items_library_missing
    ON local_media_items(library_id, is_missing);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_local_media_items_library_missing;

ALTER TABLE local_media_items
    DROP COLUMN IF EXISTS missing_since,
    DROP COLUMN IF EXISTS is_missing;

-- +goose StatementEnd

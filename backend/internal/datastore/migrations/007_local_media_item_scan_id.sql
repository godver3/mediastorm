-- +goose Up
-- +goose StatementBegin
ALTER TABLE local_media_items
    ADD COLUMN IF NOT EXISTS last_seen_scan_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_local_media_items_library_scan
    ON local_media_items(library_id, last_seen_scan_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_local_media_items_library_scan;

ALTER TABLE local_media_items
    DROP COLUMN IF EXISTS last_seen_scan_id;
-- +goose StatementEnd

-- +goose Up
-- +goose StatementBegin
ALTER TABLE local_media_libraries
    ADD COLUMN IF NOT EXISTS last_scan_total INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE local_media_libraries
    DROP COLUMN IF EXISTS last_scan_total;
-- +goose StatementEnd

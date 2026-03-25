-- +goose Up
-- +goose StatementBegin

ALTER TABLE local_media_libraries
    ADD COLUMN IF NOT EXISTS filter_out_terms JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS min_file_size_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE local_media_libraries
    DROP COLUMN IF EXISTS min_file_size_bytes,
    DROP COLUMN IF EXISTS filter_out_terms;

-- +goose StatementEnd

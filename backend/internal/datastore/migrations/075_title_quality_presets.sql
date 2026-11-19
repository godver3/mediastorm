-- +goose Up
-- The quality preset a profile last picked for a title on a device; Auto
-- ranks that title with it on that device. client_id is "" when the
-- request named no device.
CREATE TABLE IF NOT EXISTS title_quality_presets (
    user_id    TEXT NOT NULL,
    client_id  TEXT NOT NULL,
    content_id TEXT NOT NULL,
    preset_id  TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, client_id, content_id)
);

-- +goose Down
DROP TABLE IF EXISTS title_quality_presets;

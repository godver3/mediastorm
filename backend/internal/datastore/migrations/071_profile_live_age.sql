-- +goose Up
ALTER TABLE users ADD COLUMN kids_live_max_age INTEGER
    CHECK (kids_live_max_age IN (0, 6, 12, 16, 18));

-- +goose Down
ALTER TABLE users DROP COLUMN kids_live_max_age;

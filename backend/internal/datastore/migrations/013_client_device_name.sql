-- +goose Up
ALTER TABLE clients ADD COLUMN device_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE clients DROP COLUMN device_name;

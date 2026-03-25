-- +goose Up
ALTER TABLE watch_history
ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE watch_history
SET updated_at = CASE
    WHEN watched_at > TIMESTAMPTZ '0001-01-02 00:00:00+00' THEN watched_at
    ELSE now()
END
WHERE updated_at = now();

-- +goose Down
ALTER TABLE watch_history DROP COLUMN updated_at;

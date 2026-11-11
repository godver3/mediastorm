-- +goose Up
-- How each item became watched: 'played', 'imported' or 'marked'.
ALTER TABLE watch_history ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';

-- Backfill. Rows with playback time were played.
UPDATE watch_history SET source = 'played' WHERE watched AND watched_seconds > 0;

-- A bulk "mark watched" writes every episode of a show or season with the
-- same timestamp, so three or more episodes of one series watched in the same
-- second were marked, not played. Other rows stay '' and are treated as real
-- watches, since single marks and dated imports cannot be told apart.
UPDATE watch_history w SET source = 'marked'
FROM (
    SELECT user_id, series_id, date_trunc('second', watched_at) AS ts
    FROM watch_history
    WHERE watched AND source = '' AND media_type = 'episode' AND series_id <> ''
    GROUP BY user_id, series_id, date_trunc('second', watched_at)
    HAVING COUNT(*) >= 3
) batch
WHERE w.watched AND w.source = '' AND w.media_type = 'episode'
  AND w.user_id = batch.user_id AND w.series_id = batch.series_id
  AND date_trunc('second', w.watched_at) = batch.ts;

-- +goose Down
ALTER TABLE watch_history DROP COLUMN IF EXISTS source;

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgWatchHistoryRepo struct {
	pool DB
}

const whCols = `user_id, item_key, media_type, item_id, name, year, watched, watched_at,
	updated_at, external_ids, season_number, episode_number, series_id, series_name`

func (r *pgWatchHistoryRepo) Get(ctx context.Context, userID, itemKey string) (*models.WatchHistoryItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT item_key, media_type, item_id, name, year, watched, watched_at,
		updated_at, external_ids, season_number, episode_number, series_id, series_name
		FROM watch_history WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return scanWatchHistoryItem(row)
}

func (r *pgWatchHistoryRepo) ListByUser(ctx context.Context, userID string) ([]models.WatchHistoryItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT item_key, media_type, item_id, name, year, watched, watched_at,
		updated_at, external_ids, season_number, episode_number, series_id, series_name
		FROM watch_history WHERE user_id = $1 ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list watch history: %w", err)
	}
	defer rows.Close()
	return collectWatchHistory(rows)
}

func (r *pgWatchHistoryRepo) ListAll(ctx context.Context) (map[string][]models.WatchHistoryItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, item_key, media_type, item_id, name, year, watched, watched_at,
		updated_at, external_ids, season_number, episode_number, series_id, series_name
		FROM watch_history ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all watch history: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.WatchHistoryItem)
	for rows.Next() {
		var userID string
		var item models.WatchHistoryItem
		var idsJSON []byte
		if err := rows.Scan(&userID, &item.ID, &item.MediaType, &item.ItemID, &item.Name, &item.Year,
			&item.Watched, &item.WatchedAt, &item.UpdatedAt, &idsJSON,
			&item.SeasonNumber, &item.EpisodeNumber, &item.SeriesID, &item.SeriesName); err != nil {
			return nil, fmt.Errorf("scan watch history: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
		result[userID] = append(result[userID], item)
	}
	return result, rows.Err()
}

func (r *pgWatchHistoryRepo) Upsert(ctx context.Context, userID string, item *models.WatchHistoryItem) error {
	idsJSON, _ := json.Marshal(item.ExternalIDs)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO watch_history (`+whCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (user_id, item_key) DO UPDATE SET
		name=$5, year=$6, watched=$7, watched_at=$8, updated_at=$9, external_ids=$10,
		season_number=$11, episode_number=$12, series_id=$13, series_name=$14`,
		userID, item.ID, item.MediaType, item.ItemID, item.Name, item.Year,
		item.Watched, item.WatchedAt, item.UpdatedAt, idsJSON,
		item.SeasonNumber, item.EpisodeNumber, item.SeriesID, item.SeriesName)
	if err != nil {
		return fmt.Errorf("upsert watch history: %w", err)
	}
	return nil
}

func (r *pgWatchHistoryRepo) Delete(ctx context.Context, userID, itemKey string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watch_history WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return err
}

func (r *pgWatchHistoryRepo) DeleteByUser(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watch_history WHERE user_id = $1`, userID)
	return err
}

func (r *pgWatchHistoryRepo) BulkUpsert(ctx context.Context, userID string, items []models.WatchHistoryItem) error {
	for _, item := range items {
		if err := r.Upsert(ctx, userID, &item); err != nil {
			return err
		}
	}
	return nil
}

func (r *pgWatchHistoryRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM watch_history`).Scan(&count)
	return count, err
}

func scanWatchHistoryItem(row pgx.Row) (*models.WatchHistoryItem, error) {
	var item models.WatchHistoryItem
	var idsJSON []byte
	err := row.Scan(&item.ID, &item.MediaType, &item.ItemID, &item.Name, &item.Year,
		&item.Watched, &item.WatchedAt, &item.UpdatedAt, &idsJSON,
		&item.SeasonNumber, &item.EpisodeNumber, &item.SeriesID, &item.SeriesName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan watch history: %w", err)
	}
	_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
	return &item, nil
}

func collectWatchHistory(rows pgx.Rows) ([]models.WatchHistoryItem, error) {
	var result []models.WatchHistoryItem
	for rows.Next() {
		var item models.WatchHistoryItem
		var idsJSON []byte
		if err := rows.Scan(&item.ID, &item.MediaType, &item.ItemID, &item.Name, &item.Year,
			&item.Watched, &item.WatchedAt, &item.UpdatedAt, &idsJSON,
			&item.SeasonNumber, &item.EpisodeNumber, &item.SeriesID, &item.SeriesName); err != nil {
			return nil, fmt.Errorf("scan watch history: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
		result = append(result, item)
	}
	return result, rows.Err()
}

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgPlaybackProgressRepo struct {
	pool DB
}

func (r *pgPlaybackProgressRepo) Get(ctx context.Context, userID, itemKey string) (*models.PlaybackProgress, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT item_key, media_type, item_id, position, duration, percent_watched, updated_at,
		is_paused, external_ids, season_number, episode_number, series_id, series_name,
		episode_name, movie_name, year, hidden_from_continue_watching
		FROM playback_progress WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return scanPlaybackProgress(row)
}

func (r *pgPlaybackProgressRepo) ListByUser(ctx context.Context, userID string) ([]models.PlaybackProgress, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT item_key, media_type, item_id, position, duration, percent_watched, updated_at,
		is_paused, external_ids, season_number, episode_number, series_id, series_name,
		episode_name, movie_name, year, hidden_from_continue_watching
		FROM playback_progress WHERE user_id = $1 ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list playback progress: %w", err)
	}
	defer rows.Close()
	return collectPlaybackProgress(rows)
}

func (r *pgPlaybackProgressRepo) ListAll(ctx context.Context) (map[string][]models.PlaybackProgress, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, item_key, media_type, item_id, position, duration, percent_watched, updated_at,
		is_paused, external_ids, season_number, episode_number, series_id, series_name,
		episode_name, movie_name, year, hidden_from_continue_watching
		FROM playback_progress ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all playback progress: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.PlaybackProgress)
	for rows.Next() {
		var userID string
		var p models.PlaybackProgress
		var idsJSON []byte
		if err := rows.Scan(&userID, &p.ID, &p.MediaType, &p.ItemID, &p.Position, &p.Duration,
			&p.PercentWatched, &p.UpdatedAt, &p.IsPaused, &idsJSON,
			&p.SeasonNumber, &p.EpisodeNumber, &p.SeriesID, &p.SeriesName,
			&p.EpisodeName, &p.MovieName, &p.Year, &p.HiddenFromContinueWatching); err != nil {
			return nil, fmt.Errorf("scan playback progress: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &p.ExternalIDs)
		result[userID] = append(result[userID], p)
	}
	return result, rows.Err()
}

func (r *pgPlaybackProgressRepo) Upsert(ctx context.Context, userID string, p *models.PlaybackProgress) error {
	idsJSON, _ := json.Marshal(p.ExternalIDs)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO playback_progress (user_id, item_key, media_type, item_id, position, duration,
		percent_watched, updated_at, is_paused, external_ids, season_number, episode_number,
		series_id, series_name, episode_name, movie_name, year, hidden_from_continue_watching)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (user_id, item_key) DO UPDATE SET
		position=$5, duration=$6, percent_watched=$7, updated_at=$8, is_paused=$9,
		external_ids=$10, season_number=$11, episode_number=$12, series_id=$13, series_name=$14,
		episode_name=$15, movie_name=$16, year=$17, hidden_from_continue_watching=$18`,
		userID, p.ID, p.MediaType, p.ItemID, p.Position, p.Duration,
		p.PercentWatched, p.UpdatedAt, p.IsPaused, idsJSON,
		p.SeasonNumber, p.EpisodeNumber, p.SeriesID, p.SeriesName,
		p.EpisodeName, p.MovieName, p.Year, p.HiddenFromContinueWatching)
	if err != nil {
		return fmt.Errorf("upsert playback progress: %w", err)
	}
	return nil
}

func (r *pgPlaybackProgressRepo) Delete(ctx context.Context, userID, itemKey string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM playback_progress WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return err
}

func (r *pgPlaybackProgressRepo) DeleteByUser(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM playback_progress WHERE user_id = $1`, userID)
	return err
}

func (r *pgPlaybackProgressRepo) SetHidden(ctx context.Context, userID, itemKey string, hidden bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE playback_progress SET hidden_from_continue_watching = $3
		WHERE user_id = $1 AND item_key = $2`, userID, itemKey, hidden)
	return err
}

func (r *pgPlaybackProgressRepo) BulkUpsert(ctx context.Context, userID string, items []models.PlaybackProgress) error {
	for _, item := range items {
		if err := r.Upsert(ctx, userID, &item); err != nil {
			return err
		}
	}
	return nil
}

func (r *pgPlaybackProgressRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM playback_progress`).Scan(&count)
	return count, err
}

func scanPlaybackProgress(row pgx.Row) (*models.PlaybackProgress, error) {
	var p models.PlaybackProgress
	var idsJSON []byte
	err := row.Scan(&p.ID, &p.MediaType, &p.ItemID, &p.Position, &p.Duration,
		&p.PercentWatched, &p.UpdatedAt, &p.IsPaused, &idsJSON,
		&p.SeasonNumber, &p.EpisodeNumber, &p.SeriesID, &p.SeriesName,
		&p.EpisodeName, &p.MovieName, &p.Year, &p.HiddenFromContinueWatching)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan playback progress: %w", err)
	}
	_ = json.Unmarshal(idsJSON, &p.ExternalIDs)
	return &p, nil
}

func collectPlaybackProgress(rows pgx.Rows) ([]models.PlaybackProgress, error) {
	var result []models.PlaybackProgress
	for rows.Next() {
		var p models.PlaybackProgress
		var idsJSON []byte
		if err := rows.Scan(&p.ID, &p.MediaType, &p.ItemID, &p.Position, &p.Duration,
			&p.PercentWatched, &p.UpdatedAt, &p.IsPaused, &idsJSON,
			&p.SeasonNumber, &p.EpisodeNumber, &p.SeriesID, &p.SeriesName,
			&p.EpisodeName, &p.MovieName, &p.Year, &p.HiddenFromContinueWatching); err != nil {
			return nil, fmt.Errorf("scan playback progress: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &p.ExternalIDs)
		result = append(result, p)
	}
	return result, rows.Err()
}

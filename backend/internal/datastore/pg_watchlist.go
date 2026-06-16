package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgWatchlistRepo struct {
	pool DB
}

func (r *pgWatchlistRepo) Get(ctx context.Context, userID, itemKey string) (*models.WatchlistItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT item_key, media_type, item_id, name, overview, year, poster_url, text_poster_url, backdrop_url,
		added_at, external_ids, genres, runtime_minutes, sync_source, synced_at
		FROM watchlist WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return scanWatchlistItem(row)
}

func (r *pgWatchlistRepo) ListByUser(ctx context.Context, userID string) ([]models.WatchlistItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT item_key, media_type, item_id, name, overview, year, poster_url, text_poster_url, backdrop_url,
		added_at, external_ids, genres, runtime_minutes, sync_source, synced_at
		FROM watchlist WHERE user_id = $1 ORDER BY added_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list watchlist: %w", err)
	}
	defer rows.Close()
	return collectWatchlistItems(rows)
}

func (r *pgWatchlistRepo) ListAll(ctx context.Context) (map[string][]models.WatchlistItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, item_key, media_type, item_id, name, overview, year, poster_url, text_poster_url, backdrop_url,
		added_at, external_ids, genres, runtime_minutes, sync_source, synced_at
		FROM watchlist ORDER BY user_id, added_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all watchlist: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.WatchlistItem)
	for rows.Next() {
		var userID, itemKey string
		var item models.WatchlistItem
		var idsJSON, genresJSON []byte
		if err := rows.Scan(&userID, &itemKey, &item.MediaType, &item.ID, &item.Name, &item.Overview, &item.Year,
			&item.PosterURL, &item.TextPosterURL, &item.BackdropURL, &item.AddedAt, &idsJSON, &genresJSON,
			&item.RuntimeMinutes, &item.SyncSource, &item.SyncedAt); err != nil {
			return nil, fmt.Errorf("scan watchlist item: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
		_ = json.Unmarshal(genresJSON, &item.Genres)
		result[userID] = append(result[userID], item)
	}
	return result, rows.Err()
}

func (r *pgWatchlistRepo) ListTombstonesAll(ctx context.Context) (map[string][]models.WatchlistTombstone, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, item_key, media_type, item_id, name, year, external_ids, removed_at
		FROM watchlist_tombstones ORDER BY user_id, removed_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all watchlist tombstones: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.WatchlistTombstone)
	for rows.Next() {
		var userID, itemKey string
		var tombstone models.WatchlistTombstone
		var idsJSON []byte
		if err := rows.Scan(&userID, &itemKey, &tombstone.MediaType, &tombstone.ID, &tombstone.Name,
			&tombstone.Year, &idsJSON, &tombstone.RemovedAt); err != nil {
			return nil, fmt.Errorf("scan watchlist tombstone: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &tombstone.ExternalIDs)
		result[userID] = append(result[userID], tombstone)
	}
	return result, rows.Err()
}

func (r *pgWatchlistRepo) Upsert(ctx context.Context, userID string, item *models.WatchlistItem) error {
	idsJSON, _ := json.Marshal(item.ExternalIDs)
	genresJSON, _ := json.Marshal(item.Genres)
	itemKey := item.Key()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO watchlist (user_id, item_key, media_type, item_id, name, overview, year,
		poster_url, text_poster_url, backdrop_url, added_at, external_ids, genres, runtime_minutes, sync_source, synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (user_id, item_key) DO UPDATE SET
		name=$5, overview=$6, year=$7, poster_url=$8, text_poster_url=$9, backdrop_url=$10,
		external_ids=$12, genres=$13, runtime_minutes=$14, sync_source=$15, synced_at=$16`,
		userID, itemKey, item.MediaType, item.ID, item.Name, item.Overview, item.Year,
		item.PosterURL, item.TextPosterURL, item.BackdropURL, item.AddedAt, idsJSON, genresJSON,
		item.RuntimeMinutes, item.SyncSource, item.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert watchlist item: %w", err)
	}
	return nil
}

func (r *pgWatchlistRepo) UpsertTombstone(ctx context.Context, userID string, tombstone *models.WatchlistTombstone) error {
	idsJSON, _ := json.Marshal(tombstone.ExternalIDs)
	itemKey := tombstone.Key()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO watchlist_tombstones (user_id, item_key, media_type, item_id, name, year, external_ids, removed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (user_id, item_key) DO UPDATE SET
		name=$5, year=$6, external_ids=$7, removed_at=$8`,
		userID, itemKey, tombstone.MediaType, tombstone.ID, tombstone.Name, tombstone.Year,
		idsJSON, tombstone.RemovedAt)
	if err != nil {
		return fmt.Errorf("upsert watchlist tombstone: %w", err)
	}
	return nil
}

func (r *pgWatchlistRepo) Delete(ctx context.Context, userID, itemKey string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watchlist WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return err
}

func (r *pgWatchlistRepo) DeleteTombstone(ctx context.Context, userID, itemKey string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watchlist_tombstones WHERE user_id = $1 AND item_key = $2`, userID, itemKey)
	return err
}

func (r *pgWatchlistRepo) DeleteByUser(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watchlist WHERE user_id = $1`, userID)
	return err
}

func (r *pgWatchlistRepo) DeleteTombstonesByUser(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watchlist_tombstones WHERE user_id = $1`, userID)
	return err
}

func (r *pgWatchlistRepo) DeleteBySyncSource(ctx context.Context, userID, syncSource string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watchlist WHERE user_id = $1 AND sync_source = $2`, userID, syncSource)
	return err
}

func (r *pgWatchlistRepo) BulkUpsert(ctx context.Context, userID string, items []models.WatchlistItem) error {
	for _, item := range items {
		if err := r.Upsert(ctx, userID, &item); err != nil {
			return err
		}
	}
	return nil
}

func (r *pgWatchlistRepo) BulkUpsertTombstones(ctx context.Context, userID string, tombstones []models.WatchlistTombstone) error {
	for _, tombstone := range tombstones {
		if err := r.UpsertTombstone(ctx, userID, &tombstone); err != nil {
			return err
		}
	}
	return nil
}

func (r *pgWatchlistRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM watchlist`).Scan(&count)
	return count, err
}

func scanWatchlistItem(row pgx.Row) (*models.WatchlistItem, error) {
	var item models.WatchlistItem
	var idsJSON, genresJSON []byte
	err := row.Scan(&item.ID, &item.MediaType, &item.ID, &item.Name, &item.Overview, &item.Year,
		&item.PosterURL, &item.TextPosterURL, &item.BackdropURL, &item.AddedAt, &idsJSON, &genresJSON,
		&item.RuntimeMinutes, &item.SyncSource, &item.SyncedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan watchlist item: %w", err)
	}
	_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
	_ = json.Unmarshal(genresJSON, &item.Genres)
	return &item, nil
}

func collectWatchlistItems(rows pgx.Rows) ([]models.WatchlistItem, error) {
	var result []models.WatchlistItem
	for rows.Next() {
		var item models.WatchlistItem
		var idsJSON, genresJSON []byte
		// item_key is scanned but we derive ID from item_id column
		var itemKey string
		if err := rows.Scan(&itemKey, &item.MediaType, &item.ID, &item.Name, &item.Overview, &item.Year,
			&item.PosterURL, &item.TextPosterURL, &item.BackdropURL, &item.AddedAt, &idsJSON, &genresJSON,
			&item.RuntimeMinutes, &item.SyncSource, &item.SyncedAt); err != nil {
			return nil, fmt.Errorf("scan watchlist item: %w", err)
		}
		_ = json.Unmarshal(idsJSON, &item.ExternalIDs)
		_ = json.Unmarshal(genresJSON, &item.Genres)
		result = append(result, item)
	}
	return result, rows.Err()
}

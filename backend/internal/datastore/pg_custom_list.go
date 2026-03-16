package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgCustomListRepo struct {
	pool DB
}

func (r *pgCustomListRepo) GetList(ctx context.Context, listID string) (*models.CustomList, error) {
	var cl models.CustomList
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, created_at, updated_at FROM custom_lists WHERE id = $1`, listID).
		Scan(&cl.ID, &cl.Name, &cl.CreatedAt, &cl.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get custom list: %w", err)
	}
	// Compute item count
	_ = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM custom_list_items WHERE list_id = $1`, listID).Scan(&cl.ItemCount)
	return &cl, nil
}

func (r *pgCustomListRepo) ListByUser(ctx context.Context, userID string) ([]models.CustomList, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cl.id, cl.name, cl.created_at, cl.updated_at,
		       COALESCE((SELECT COUNT(*) FROM custom_list_items cli WHERE cli.list_id = cl.id), 0)
		FROM custom_lists cl WHERE cl.user_id = $1 ORDER BY cl.created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list custom lists: %w", err)
	}
	defer rows.Close()

	var result []models.CustomList
	for rows.Next() {
		var cl models.CustomList
		if err := rows.Scan(&cl.ID, &cl.Name, &cl.CreatedAt, &cl.UpdatedAt, &cl.ItemCount); err != nil {
			return nil, fmt.Errorf("scan custom list: %w", err)
		}
		result = append(result, cl)
	}
	return result, rows.Err()
}

func (r *pgCustomListRepo) CreateList(ctx context.Context, userID string, list *models.CustomList) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO custom_lists (id, user_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)`,
		list.ID, userID, list.Name, list.CreatedAt, list.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create custom list: %w", err)
	}
	return nil
}

func (r *pgCustomListRepo) UpdateList(ctx context.Context, list *models.CustomList) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE custom_lists SET name=$2, updated_at=$3 WHERE id=$1`,
		list.ID, list.Name, list.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update custom list: %w", err)
	}
	return nil
}

func (r *pgCustomListRepo) DeleteList(ctx context.Context, listID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM custom_lists WHERE id = $1`, listID)
	return err
}

func (r *pgCustomListRepo) GetItems(ctx context.Context, listID string) ([]models.WatchlistItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT item_key, media_type, item_id, name, overview, year, poster_url, backdrop_url,
		added_at, external_ids, genres, runtime_minutes, sync_source, synced_at
		FROM custom_list_items WHERE list_id = $1 ORDER BY added_at`, listID)
	if err != nil {
		return nil, fmt.Errorf("get custom list items: %w", err)
	}
	defer rows.Close()
	return collectWatchlistItems(rows)
}

func (r *pgCustomListRepo) UpsertItem(ctx context.Context, listID string, item *models.WatchlistItem) error {
	idsJSON, _ := json.Marshal(item.ExternalIDs)
	genresJSON, _ := json.Marshal(item.Genres)
	itemKey := item.Key()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO custom_list_items (list_id, item_key, media_type, item_id, name, overview, year,
		poster_url, backdrop_url, added_at, external_ids, genres, runtime_minutes, sync_source, synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (list_id, item_key) DO UPDATE SET
		name=$5, overview=$6, year=$7, poster_url=$8, backdrop_url=$9,
		external_ids=$11, genres=$12, runtime_minutes=$13, sync_source=$14, synced_at=$15`,
		listID, itemKey, item.MediaType, item.ID, item.Name, item.Overview, item.Year,
		item.PosterURL, item.BackdropURL, item.AddedAt, idsJSON, genresJSON,
		item.RuntimeMinutes, item.SyncSource, item.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert custom list item: %w", err)
	}
	return nil
}

func (r *pgCustomListRepo) DeleteItem(ctx context.Context, listID, itemKey string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM custom_list_items WHERE list_id = $1 AND item_key = $2`, listID, itemKey)
	return err
}

func (r *pgCustomListRepo) ListUserIDs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT user_id FROM custom_lists ORDER BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("list custom list user ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *pgCustomListRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM custom_lists`).Scan(&count)
	return count, err
}

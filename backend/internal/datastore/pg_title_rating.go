package datastore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
	"novastream/models"
)

type pgTitleRatingRepo struct{ pool *pgxpool.Pool }

func (r *pgTitleRatingRepo) List(ctx context.Context, profile string) ([]models.TitleRating, error) {
	rows, err := r.pool.Query(ctx, `SELECT title_key, aliases, title, rating, updated_at FROM profile_title_ratings WHERE profile_id=$1 ORDER BY title_key`, profile)
	if err != nil {
		return nil, fmt.Errorf("list title ratings: %w", err)
	}
	defer rows.Close()
	result := []models.TitleRating{}
	for rows.Next() {
		var item models.TitleRating
		var title []byte
		if err := rows.Scan(&item.Key, &item.Aliases, &title, &item.Value, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(title, &item.Title); err != nil {
			return nil, fmt.Errorf("decode rated title: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// Serialize preference changes for a profile so provider aliases converge on
// one rating even when two devices change the same title concurrently.
func (r *pgTitleRatingRepo) Put(ctx context.Context, profile string, item models.TitleRating) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "title-ratings:"+profile); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT title_key, aliases FROM profile_title_ratings WHERE profile_id=$1 AND aliases && $2::text[]`, profile, item.Aliases)
	if err != nil {
		return err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		var aliases []string
		if err := rows.Scan(&key, &aliases); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
		item.Aliases = append(item.Aliases, aliases...)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM profile_title_ratings WHERE profile_id=$1 AND title_key=ANY($2::text[])`, profile, keys); err != nil {
		return err
	}
	if item.Value != "none" {
		slices.Sort(item.Aliases)
		item.Aliases = slices.Compact(item.Aliases)
		title, err := json.Marshal(item.Title)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO profile_title_ratings (profile_id,title_key,aliases,title,rating,updated_at) VALUES ($1,$2,$3,$4,$5,$6)`, profile, item.Key, item.Aliases, title, item.Value, item.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

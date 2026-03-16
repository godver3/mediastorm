package datastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type pgPrewarmRepo struct {
	pool DB
}

func (r *pgPrewarmRepo) Get(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM prewarm WHERE id = $1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get prewarm: %w", err)
	}
	return data, nil
}

func (r *pgPrewarmRepo) List(ctx context.Context) ([][]byte, error) {
	rows, err := r.pool.Query(ctx, `SELECT data FROM prewarm`)
	if err != nil {
		return nil, fmt.Errorf("list prewarm: %w", err)
	}
	defer rows.Close()

	var result [][]byte
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan prewarm: %w", err)
		}
		result = append(result, data)
	}
	return result, rows.Err()
}

func (r *pgPrewarmRepo) Upsert(ctx context.Context, id, titleID, userID string, data []byte, expiresAt interface{}) error {
	var exp time.Time
	switch v := expiresAt.(type) {
	case time.Time:
		exp = v
	default:
		exp = time.Now().Add(24 * time.Hour)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO prewarm (id, title_id, user_id, data, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
		title_id=$2, user_id=$3, data=$4, expires_at=$5`,
		id, titleID, userID, data, exp)
	if err != nil {
		return fmt.Errorf("upsert prewarm: %w", err)
	}
	return nil
}

func (r *pgPrewarmRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM prewarm WHERE id = $1`, id)
	return err
}

func (r *pgPrewarmRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM prewarm WHERE expires_at < $1`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired prewarm: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *pgPrewarmRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM prewarm`).Scan(&count)
	return count, err
}

package datastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type pgPrequeueRepo struct {
	pool DB
}

func (r *pgPrequeueRepo) Get(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM prequeue WHERE id = $1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get prequeue: %w", err)
	}
	return data, nil
}

func (r *pgPrequeueRepo) GetByTitleUser(ctx context.Context, titleID, userID string) ([]byte, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM prequeue WHERE title_id = $1 AND user_id = $2`, titleID, userID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get prequeue by title/user: %w", err)
	}
	return data, nil
}

func (r *pgPrequeueRepo) List(ctx context.Context) ([][]byte, error) {
	rows, err := r.pool.Query(ctx, `SELECT data FROM prequeue ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list prequeue: %w", err)
	}
	defer rows.Close()

	var result [][]byte
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan prequeue: %w", err)
		}
		result = append(result, data)
	}
	return result, rows.Err()
}

func (r *pgPrequeueRepo) Upsert(ctx context.Context, id, titleID, userID, status string, data []byte, expiresAt interface{}) error {
	var exp time.Time
	switch v := expiresAt.(type) {
	case time.Time:
		exp = v
	default:
		exp = time.Now().Add(1 * time.Hour)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO prequeue (id, title_id, user_id, status, data, created_at, updated_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, now(), now(), $6)
		ON CONFLICT (id) DO UPDATE SET
		title_id=$2, user_id=$3, status=$4, data=$5, updated_at=now(), expires_at=$6`,
		id, titleID, userID, status, data, exp)
	if err != nil {
		return fmt.Errorf("upsert prequeue: %w", err)
	}
	return nil
}

func (r *pgPrequeueRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM prequeue WHERE id = $1`, id)
	return err
}

func (r *pgPrequeueRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM prequeue WHERE expires_at < $1`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired prequeue: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *pgPrequeueRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM prequeue`).Scan(&count)
	return count, err
}

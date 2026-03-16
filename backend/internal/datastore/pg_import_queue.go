package datastore

import (
	"context"
)

type pgImportQueueRepo struct {
	pool DB
}

func (r *pgImportQueueRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM import_queue`).Scan(&count)
	return count, err
}

type pgFileHealthRepo struct {
	pool DB
}

func (r *pgFileHealthRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM file_health`).Scan(&count)
	return count, err
}

type pgMediaFileRepo struct {
	pool DB
}

func (r *pgMediaFileRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM media_files`).Scan(&count)
	return count, err
}

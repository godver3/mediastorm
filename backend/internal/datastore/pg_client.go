package datastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgClientRepo struct {
	pool DB
}

const clientCols = `id, user_id, name, device_type, os, app_version, last_seen_at, first_seen_at, filter_enabled`

func (r *pgClientRepo) Get(ctx context.Context, id string) (*models.Client, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+clientCols+` FROM clients WHERE id = $1`, id)
	return scanClient(row)
}

func (r *pgClientRepo) ListByUser(ctx context.Context, userID string) ([]models.Client, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+clientCols+` FROM clients WHERE user_id = $1 ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()
	return collectClients(rows)
}

func (r *pgClientRepo) List(ctx context.Context) ([]models.Client, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+clientCols+` FROM clients ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()
	return collectClients(rows)
}

func (r *pgClientRepo) Create(ctx context.Context, c *models.Client) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO clients (`+clientCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, c.UserID, c.Name, c.DeviceType, c.OS, c.AppVersion, c.LastSeenAt, c.FirstSeenAt, c.FilterEnabled)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	return nil
}

func (r *pgClientRepo) Update(ctx context.Context, c *models.Client) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE clients SET user_id=$2, name=$3, device_type=$4, os=$5, app_version=$6,
		last_seen_at=$7, first_seen_at=$8, filter_enabled=$9
		WHERE id=$1`,
		c.ID, c.UserID, c.Name, c.DeviceType, c.OS, c.AppVersion, c.LastSeenAt, c.FirstSeenAt, c.FilterEnabled)
	if err != nil {
		return fmt.Errorf("update client: %w", err)
	}
	return nil
}

func (r *pgClientRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM clients WHERE id = $1`, id)
	return err
}

func (r *pgClientRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM clients`).Scan(&count)
	return count, err
}

func scanClient(row pgx.Row) (*models.Client, error) {
	var c models.Client
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.DeviceType, &c.OS, &c.AppVersion, &c.LastSeenAt, &c.FirstSeenAt, &c.FilterEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan client: %w", err)
	}
	return &c, nil
}

func collectClients(rows pgx.Rows) ([]models.Client, error) {
	var result []models.Client
	for rows.Next() {
		var c models.Client
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.DeviceType, &c.OS, &c.AppVersion,
			&c.LastSeenAt, &c.FirstSeenAt, &c.FilterEnabled); err != nil {
			return nil, fmt.Errorf("scan client: %w", err)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

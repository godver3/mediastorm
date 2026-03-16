package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgClientSettingsRepo struct {
	pool DB
}

func (r *pgClientSettingsRepo) Get(ctx context.Context, clientID string) (*models.ClientFilterSettings, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT settings FROM client_settings WHERE client_id = $1`, clientID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get client settings: %w", err)
	}
	var s models.ClientFilterSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal client settings: %w", err)
	}
	return &s, nil
}

func (r *pgClientSettingsRepo) Upsert(ctx context.Context, clientID string, settings *models.ClientFilterSettings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal client settings: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO client_settings (client_id, settings, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (client_id) DO UPDATE SET settings = $2, updated_at = now()`,
		clientID, data)
	if err != nil {
		return fmt.Errorf("upsert client settings: %w", err)
	}
	return nil
}

func (r *pgClientSettingsRepo) Delete(ctx context.Context, clientID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM client_settings WHERE client_id = $1`, clientID)
	return err
}

func (r *pgClientSettingsRepo) List(ctx context.Context) (map[string]models.ClientFilterSettings, error) {
	rows, err := r.pool.Query(ctx, `SELECT client_id, settings FROM client_settings`)
	if err != nil {
		return nil, fmt.Errorf("list client settings: %w", err)
	}
	defer rows.Close()

	result := make(map[string]models.ClientFilterSettings)
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("scan client settings: %w", err)
		}
		var s models.ClientFilterSettings
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("unmarshal client settings: %w", err)
		}
		result[id] = s
	}
	return result, rows.Err()
}

func (r *pgClientSettingsRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM client_settings`).Scan(&count)
	return count, err
}

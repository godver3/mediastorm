package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgUserSettingsRepo struct {
	pool DB
}

func (r *pgUserSettingsRepo) Get(ctx context.Context, userID string) (*models.UserSettings, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT settings FROM user_settings WHERE user_id = $1`, userID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user settings: %w", err)
	}
	var s models.UserSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal user settings: %w", err)
	}
	return &s, nil
}

func (r *pgUserSettingsRepo) Upsert(ctx context.Context, userID string, settings *models.UserSettings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal user settings: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO user_settings (user_id, settings, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET settings = $2, updated_at = now()`,
		userID, data)
	if err != nil {
		return fmt.Errorf("upsert user settings: %w", err)
	}
	return nil
}

func (r *pgUserSettingsRepo) Delete(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM user_settings WHERE user_id = $1`, userID)
	return err
}

func (r *pgUserSettingsRepo) List(ctx context.Context) (map[string]models.UserSettings, error) {
	rows, err := r.pool.Query(ctx, `SELECT user_id, settings FROM user_settings`)
	if err != nil {
		return nil, fmt.Errorf("list user settings: %w", err)
	}
	defer rows.Close()

	result := make(map[string]models.UserSettings)
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("scan user settings: %w", err)
		}
		var s models.UserSettings
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("unmarshal user settings: %w", err)
		}
		result[id] = s
	}
	return result, rows.Err()
}

func (r *pgUserSettingsRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_settings`).Scan(&count)
	return count, err
}

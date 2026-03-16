package datastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgContentPrefsRepo struct {
	pool DB
}

func (r *pgContentPrefsRepo) Get(ctx context.Context, userID, contentID string) (*models.ContentPreference, error) {
	var p models.ContentPreference
	err := r.pool.QueryRow(ctx, `
		SELECT content_id, content_type, audio_language, subtitle_language, subtitle_mode, updated_at
		FROM content_preferences WHERE user_id = $1 AND content_id = $2`, userID, contentID).
		Scan(&p.ContentID, &p.ContentType, &p.AudioLanguage, &p.SubtitleLanguage, &p.SubtitleMode, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get content preference: %w", err)
	}
	return &p, nil
}

func (r *pgContentPrefsRepo) ListByUser(ctx context.Context, userID string) ([]models.ContentPreference, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, content_type, audio_language, subtitle_language, subtitle_mode, updated_at
		FROM content_preferences WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("list content preferences: %w", err)
	}
	defer rows.Close()

	var result []models.ContentPreference
	for rows.Next() {
		var p models.ContentPreference
		if err := rows.Scan(&p.ContentID, &p.ContentType, &p.AudioLanguage, &p.SubtitleLanguage,
			&p.SubtitleMode, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan content preference: %w", err)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (r *pgContentPrefsRepo) Upsert(ctx context.Context, userID string, pref *models.ContentPreference) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO content_preferences (user_id, content_id, content_type, audio_language,
		subtitle_language, subtitle_mode, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, content_id) DO UPDATE SET
		content_type=$3, audio_language=$4, subtitle_language=$5, subtitle_mode=$6, updated_at=$7`,
		userID, pref.ContentID, pref.ContentType, pref.AudioLanguage,
		pref.SubtitleLanguage, pref.SubtitleMode, pref.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert content preference: %w", err)
	}
	return nil
}

func (r *pgContentPrefsRepo) Delete(ctx context.Context, userID, contentID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM content_preferences WHERE user_id = $1 AND content_id = $2`, userID, contentID)
	return err
}

func (r *pgContentPrefsRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM content_preferences`).Scan(&count)
	return count, err
}

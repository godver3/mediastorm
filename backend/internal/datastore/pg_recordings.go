package datastore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgRecordingRepo struct {
	pool DB
}

func (r *pgRecordingRepo) Get(ctx context.Context, id string) (*models.Recording, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, type, status, channel_id, tvg_id, channel_name, title, description,
		       source_url, start_at, end_at, padding_before_seconds, padding_after_seconds,
		       output_path, output_size_bytes, actual_start_at, actual_end_at, error, created_at, updated_at
		FROM recordings WHERE id = $1`, id)
	return scanRecording(row)
}

func (r *pgRecordingRepo) List(ctx context.Context, filter models.RecordingListFilter) ([]models.Recording, error) {
	args := []any{}
	clauses := []string{}
	if !filter.IncludeAll && strings.TrimSpace(filter.UserID) != "" {
		args = append(args, strings.TrimSpace(filter.UserID))
		clauses = append(clauses, fmt.Sprintf("user_id = $%d", len(args)))
	}
	if len(filter.Statuses) > 0 {
		statuses := make([]string, len(filter.Statuses))
		for i, status := range filter.Statuses {
			statuses[i] = string(status)
		}
		args = append(args, statuses)
		clauses = append(clauses, fmt.Sprintf("status = ANY($%d)", len(args)))
	}
	if filter.OnlyStartBefore != nil {
		args = append(args, *filter.OnlyStartBefore)
		clauses = append(clauses, fmt.Sprintf("(start_at - make_interval(secs => padding_before_seconds)) <= $%d", len(args)))
	}

	query := `
		SELECT id, user_id, type, status, channel_id, tvg_id, channel_name, title, description,
		       source_url, start_at, end_at, padding_before_seconds, padding_after_seconds,
		       output_path, output_size_bytes, actual_start_at, actual_end_at, error, created_at, updated_at
		FROM recordings`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY start_at DESC, created_at DESC"
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list recordings: %w", err)
	}
	defer rows.Close()

	var recordings []models.Recording
	for rows.Next() {
		recording, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		recordings = append(recordings, *recording)
	}
	return recordings, rows.Err()
}

func (r *pgRecordingRepo) Create(ctx context.Context, recording *models.Recording) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO recordings (
			id, user_id, type, status, channel_id, tvg_id, channel_name, title, description, source_url,
			start_at, end_at, padding_before_seconds, padding_after_seconds,
			output_path, output_size_bytes, actual_start_at, actual_end_at, error, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
			$11,$12,$13,$14,
			$15,$16,$17,$18,$19,$20,$21
		)`,
		recording.ID, recording.UserID, string(recording.Type), string(recording.Status), recording.ChannelID, recording.TvgID,
		recording.ChannelName, recording.Title, recording.Description, recording.SourceURL,
		recording.StartAt, recording.EndAt, recording.PaddingBeforeSeconds, recording.PaddingAfterSeconds,
		recording.OutputPath, recording.OutputSizeBytes, recording.ActualStartAt, recording.ActualEndAt, recording.Error,
		recording.CreatedAt, recording.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create recording: %w", err)
	}
	return nil
}

func (r *pgRecordingRepo) Update(ctx context.Context, recording *models.Recording) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE recordings
		SET status = $2,
		    channel_id = $3,
		    tvg_id = $4,
		    channel_name = $5,
		    title = $6,
		    description = $7,
		    source_url = $8,
		    start_at = $9,
		    end_at = $10,
		    padding_before_seconds = $11,
		    padding_after_seconds = $12,
		    output_path = $13,
		    output_size_bytes = $14,
		    actual_start_at = $15,
		    actual_end_at = $16,
		    error = $17,
		    updated_at = $18
		WHERE id = $1`,
		recording.ID, string(recording.Status), recording.ChannelID, recording.TvgID, recording.ChannelName,
		recording.Title, recording.Description, recording.SourceURL, recording.StartAt, recording.EndAt,
		recording.PaddingBeforeSeconds, recording.PaddingAfterSeconds, recording.OutputPath, recording.OutputSizeBytes,
		recording.ActualStartAt, recording.ActualEndAt, recording.Error, recording.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update recording: %w", err)
	}
	return nil
}

func (r *pgRecordingRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM recordings WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete recording: %w", err)
	}
	return nil
}

func (r *pgRecordingRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM recordings`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *pgRecordingRepo) MarkStaleActiveAsFailed(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE recordings
		SET status = 'failed',
		    error = CASE WHEN error = '' THEN 'recording interrupted by backend restart' ELSE error END,
		    actual_end_at = COALESCE(actual_end_at, $1),
		    updated_at = $1
		WHERE status IN ('starting', 'running')`, now)
	if err != nil {
		return 0, fmt.Errorf("mark stale recordings failed: %w", err)
	}
	return tag.RowsAffected(), nil
}

func scanRecording(row interface {
	Scan(dest ...any) error
}) (*models.Recording, error) {
	var recording models.Recording
	var typ string
	var status string
	err := row.Scan(
		&recording.ID,
		&recording.UserID,
		&typ,
		&status,
		&recording.ChannelID,
		&recording.TvgID,
		&recording.ChannelName,
		&recording.Title,
		&recording.Description,
		&recording.SourceURL,
		&recording.StartAt,
		&recording.EndAt,
		&recording.PaddingBeforeSeconds,
		&recording.PaddingAfterSeconds,
		&recording.OutputPath,
		&recording.OutputSizeBytes,
		&recording.ActualStartAt,
		&recording.ActualEndAt,
		&recording.Error,
		&recording.CreatedAt,
		&recording.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan recording: %w", err)
	}
	recording.Type = models.RecordingType(typ)
	recording.Status = models.RecordingStatus(status)
	return &recording, nil
}

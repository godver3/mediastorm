package datastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgSessionRepo struct {
	pool DB
}

func (r *pgSessionRepo) Get(ctx context.Context, token string) (*models.Session, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT token, account_id, is_master, expires_at, created_at, user_agent, ip_address
		FROM sessions WHERE token = $1`, token)
	var s models.Session
	err := row.Scan(&s.Token, &s.AccountID, &s.IsMaster, &s.ExpiresAt, &s.CreatedAt, &s.UserAgent, &s.IPAddress)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan session: %w", err)
	}
	return &s, nil
}

func (r *pgSessionRepo) List(ctx context.Context) ([]models.Session, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT token, account_id, is_master, expires_at, created_at, user_agent, ip_address
		FROM sessions WHERE expires_at > $1 ORDER BY created_at`, time.Now())
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var result []models.Session
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.Token, &s.AccountID, &s.IsMaster, &s.ExpiresAt, &s.CreatedAt, &s.UserAgent, &s.IPAddress); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r *pgSessionRepo) ListByAccount(ctx context.Context, accountID string) ([]models.Session, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT token, account_id, is_master, expires_at, created_at, user_agent, ip_address
		FROM sessions WHERE account_id = $1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var result []models.Session
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(&s.Token, &s.AccountID, &s.IsMaster, &s.ExpiresAt, &s.CreatedAt, &s.UserAgent, &s.IPAddress); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r *pgSessionRepo) Create(ctx context.Context, sess *models.Session) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO sessions (token, account_id, is_master, expires_at, created_at, user_agent, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.Token, sess.AccountID, sess.IsMaster, sess.ExpiresAt, sess.CreatedAt, sess.UserAgent, sess.IPAddress)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *pgSessionRepo) Update(ctx context.Context, sess *models.Session) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE sessions SET account_id=$2, is_master=$3, expires_at=$4, created_at=$5, user_agent=$6, ip_address=$7
		WHERE token=$1`,
		sess.Token, sess.AccountID, sess.IsMaster, sess.ExpiresAt, sess.CreatedAt, sess.UserAgent, sess.IPAddress)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return nil
}

func (r *pgSessionRepo) Delete(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (r *pgSessionRepo) DeleteByAccount(ctx context.Context, accountID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE account_id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("delete sessions by account: %w", err)
	}
	return nil
}

func (r *pgSessionRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *pgSessionRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count)
	return count, err
}

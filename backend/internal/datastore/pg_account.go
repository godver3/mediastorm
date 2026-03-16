package datastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgAccountRepo struct {
	pool DB
}

func (r *pgAccountRepo) Get(ctx context.Context, id string) (*models.Account, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, is_master, max_streams, expires_at, created_at, updated_at
		FROM accounts WHERE id = $1`, id)
	return scanAccount(row)
}

func (r *pgAccountRepo) GetByUsername(ctx context.Context, username string) (*models.Account, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, is_master, max_streams, expires_at, created_at, updated_at
		FROM accounts WHERE username = $1`, username)
	return scanAccount(row)
}

func (r *pgAccountRepo) List(ctx context.Context) ([]models.Account, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, username, password_hash, is_master, max_streams, expires_at, created_at, updated_at
		FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var result []models.Account
	for rows.Next() {
		a, err := scanAccountRows(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *a)
	}
	return result, rows.Err()
}

func (r *pgAccountRepo) Create(ctx context.Context, acct *models.Account) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO accounts (id, username, password_hash, is_master, max_streams, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		acct.ID, acct.Username, acct.PasswordHash, acct.IsMaster, acct.MaxStreams,
		acct.ExpiresAt, acct.CreatedAt, acct.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}

func (r *pgAccountRepo) Update(ctx context.Context, acct *models.Account) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE accounts SET username=$2, password_hash=$3, is_master=$4, max_streams=$5,
		expires_at=$6, updated_at=$7
		WHERE id=$1`,
		acct.ID, acct.Username, acct.PasswordHash, acct.IsMaster, acct.MaxStreams,
		acct.ExpiresAt, acct.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	return nil
}

func (r *pgAccountRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}

func (r *pgAccountRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&count)
	return count, err
}

func scanAccount(row pgx.Row) (*models.Account, error) {
	var a models.Account
	err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.IsMaster, &a.MaxStreams,
		&a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan account: %w", err)
	}
	return &a, nil
}

func scanAccountRows(rows pgx.Rows) (*models.Account, error) {
	var a models.Account
	err := rows.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.IsMaster, &a.MaxStreams,
		&a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan account: %w", err)
	}
	return &a, nil
}

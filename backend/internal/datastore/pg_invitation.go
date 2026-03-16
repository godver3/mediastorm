package datastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgInvitationRepo struct {
	pool DB
}

const invCols = `id, token, created_by, expires_at, account_expires_in_hours, used_at, used_by, created_at`

func (r *pgInvitationRepo) Get(ctx context.Context, id string) (*models.Invitation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invCols+` FROM invitations WHERE id = $1`, id)
	return scanInvitation(row)
}

func (r *pgInvitationRepo) GetByToken(ctx context.Context, token string) (*models.Invitation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invCols+` FROM invitations WHERE token = $1`, token)
	return scanInvitation(row)
}

func (r *pgInvitationRepo) List(ctx context.Context) ([]models.Invitation, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+invCols+` FROM invitations ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()

	var result []models.Invitation
	for rows.Next() {
		var inv models.Invitation
		if err := rows.Scan(&inv.ID, &inv.Token, &inv.CreatedBy, &inv.ExpiresAt,
			&inv.AccountExpiresInHours, &inv.UsedAt, &inv.UsedBy, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		result = append(result, inv)
	}
	return result, rows.Err()
}

func (r *pgInvitationRepo) Create(ctx context.Context, inv *models.Invitation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO invitations (`+invCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		inv.ID, inv.Token, inv.CreatedBy, inv.ExpiresAt,
		inv.AccountExpiresInHours, inv.UsedAt, inv.UsedBy, inv.CreatedAt)
	if err != nil {
		return fmt.Errorf("create invitation: %w", err)
	}
	return nil
}

func (r *pgInvitationRepo) Update(ctx context.Context, inv *models.Invitation) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE invitations SET token=$2, created_by=$3, expires_at=$4,
		account_expires_in_hours=$5, used_at=$6, used_by=$7
		WHERE id=$1`,
		inv.ID, inv.Token, inv.CreatedBy, inv.ExpiresAt,
		inv.AccountExpiresInHours, inv.UsedAt, inv.UsedBy)
	if err != nil {
		return fmt.Errorf("update invitation: %w", err)
	}
	return nil
}

func (r *pgInvitationRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, id)
	return err
}

func (r *pgInvitationRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM invitations`).Scan(&count)
	return count, err
}

func scanInvitation(row pgx.Row) (*models.Invitation, error) {
	var inv models.Invitation
	err := row.Scan(&inv.ID, &inv.Token, &inv.CreatedBy, &inv.ExpiresAt,
		&inv.AccountExpiresInHours, &inv.UsedAt, &inv.UsedBy, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan invitation: %w", err)
	}
	return &inv, nil
}

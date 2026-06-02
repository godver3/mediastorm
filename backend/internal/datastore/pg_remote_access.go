package datastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgRemoteAccessInviteRepo struct {
	pool DB
}

const remoteAccessInviteCols = `id, token_hash, connection_code, iroh_invite, created_by, peer_name, expires_at, used_at, used_by_peer_id, revoked_at, created_at`

func (r *pgRemoteAccessInviteRepo) Get(ctx context.Context, id string) (*models.RemoteAccessInvite, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+remoteAccessInviteCols+` FROM remote_access_invites WHERE id = $1`, id)
	return scanRemoteAccessInvite(row)
}

func (r *pgRemoteAccessInviteRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*models.RemoteAccessInvite, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+remoteAccessInviteCols+` FROM remote_access_invites WHERE token_hash = $1`, tokenHash)
	return scanRemoteAccessInvite(row)
}

func (r *pgRemoteAccessInviteRepo) List(ctx context.Context) ([]models.RemoteAccessInvite, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+remoteAccessInviteCols+` FROM remote_access_invites ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list remote access invites: %w", err)
	}
	defer rows.Close()

	var result []models.RemoteAccessInvite
	for rows.Next() {
		inv, err := scanRemoteAccessInvite(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *inv)
	}
	return result, rows.Err()
}

func (r *pgRemoteAccessInviteRepo) Create(ctx context.Context, inv *models.RemoteAccessInvite) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO remote_access_invites (`+remoteAccessInviteCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		inv.ID, inv.TokenHash, inv.ConnectionCode, inv.IrohInvite, inv.CreatedBy, inv.PeerName, inv.ExpiresAt, inv.UsedAt,
		inv.UsedByPeerID, inv.RevokedAt, inv.CreatedAt)
	if err != nil {
		return fmt.Errorf("create remote access invite: %w", err)
	}
	return nil
}

func (r *pgRemoteAccessInviteRepo) ClaimByTokenHash(ctx context.Context, tokenHash string, peerID string, now time.Time) (*models.RemoteAccessInvite, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE remote_access_invites
		SET used_at = COALESCE(used_at, $3), used_by_peer_id = CASE WHEN used_by_peer_id = '' THEN $2 ELSE used_by_peer_id END
		WHERE token_hash = $1
			AND revoked_at IS NULL
			AND (expires_at > $3 OR used_by_peer_id = $2)
			AND (used_at IS NULL OR used_by_peer_id = $2)
		RETURNING `+remoteAccessInviteCols,
		tokenHash, peerID, now)
	return scanRemoteAccessInvite(row)
}

func (r *pgRemoteAccessInviteRepo) Update(ctx context.Context, inv *models.RemoteAccessInvite) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE remote_access_invites
		SET token_hash=$2, connection_code=$3, iroh_invite=$4, created_by=$5, peer_name=$6, expires_at=$7,
			used_at=$8, used_by_peer_id=$9, revoked_at=$10
		WHERE id=$1`,
		inv.ID, inv.TokenHash, inv.ConnectionCode, inv.IrohInvite, inv.CreatedBy, inv.PeerName, inv.ExpiresAt, inv.UsedAt,
		inv.UsedByPeerID, inv.RevokedAt)
	if err != nil {
		return fmt.Errorf("update remote access invite: %w", err)
	}
	return nil
}

func (r *pgRemoteAccessInviteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM remote_access_invites WHERE id = $1`, id)
	return err
}

func (r *pgRemoteAccessInviteRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM remote_access_invites`).Scan(&count)
	return count, err
}

func scanRemoteAccessInvite(row pgx.Row) (*models.RemoteAccessInvite, error) {
	var inv models.RemoteAccessInvite
	err := row.Scan(&inv.ID, &inv.TokenHash, &inv.ConnectionCode, &inv.IrohInvite, &inv.CreatedBy, &inv.PeerName, &inv.ExpiresAt,
		&inv.UsedAt, &inv.UsedByPeerID, &inv.RevokedAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan remote access invite: %w", err)
	}
	return &inv, nil
}

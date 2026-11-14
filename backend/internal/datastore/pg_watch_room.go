package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"novastream/models"
)

type pgWatchRoomRepo struct{ pool DB }

const watchRoomCols = `r.id, r.creator_profile_id, creator.name, r.title, r.media_type, r.item_id,
	r.poster_url, r.backdrop_url, r.params, r.status, r.waiting_for_ready, r.position, r.duration, r.revision,
	r.updated_by, r.anchor_updated_at, r.created_at, r.expires_at, r.ended_at, r.end_reason`

func (r *pgWatchRoomRepo) Create(ctx context.Context, room *models.WatchRoom, invitees []string, clientID string, capabilities models.WatchRoomClientCapabilities) error {
	params, err := json.Marshal(room.Params)
	if err != nil {
		return fmt.Errorf("marshal watch room params: %w", err)
	}
	tx, err := beginDBTx(ctx, r.pool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO watch_rooms
		(id, creator_profile_id, title, media_type, item_id, poster_url, backdrop_url, params, status,
		 position, duration, revision, updated_by, anchor_updated_at, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		room.ID, room.CreatorProfileID, room.Title, room.MediaType, room.ItemID, room.PosterURL,
		room.BackdropURL, params, room.Status, room.Position, room.Duration, room.Revision,
		room.CreatorProfileID, room.AnchorUpdatedAt, room.CreatedAt, room.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create watch room: %w", err)
	}
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("marshal watch room capabilities: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO watch_room_members
		(room_id, profile_id, client_id, is_creator, ready, joined_at, last_seen_at, capabilities)
		VALUES ($1,$2,$3,true,true,$4,$4,$5)`, room.ID, room.CreatorProfileID, clientID, room.CreatedAt, capabilitiesJSON)
	if err != nil {
		return fmt.Errorf("add room creator: %w", err)
	}
	for _, profileID := range invitees {
		if profileID == room.CreatorProfileID {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO watch_room_invites (room_id, profile_id, invited_at)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, room.ID, profileID, room.CreatedAt); err != nil {
			return fmt.Errorf("create room invitation: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// beginDBTx is limited to repositories backed by pgx pool/transaction.
func beginDBTx(ctx context.Context, db DB) (pgx.Tx, error) {
	type beginner interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	b, ok := db.(beginner)
	if !ok {
		return nil, errors.New("watch room transaction unavailable")
	}
	return b.Begin(ctx)
}

func (r *pgWatchRoomRepo) Get(ctx context.Context, roomID string) (*models.WatchRoom, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+watchRoomCols+` FROM watch_rooms r JOIN users creator ON creator.id=r.creator_profile_id WHERE r.id=$1`, roomID)
	room, err := scanWatchRoom(row)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, nil
	}
	room.Members, err = r.listMembers(ctx, roomID)
	return room, err
}

func (r *pgWatchRoomRepo) ListInvitations(ctx context.Context, profileID string, now time.Time) ([]models.WatchRoom, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+watchRoomCols+` FROM watch_rooms r
		JOIN users creator ON creator.id=r.creator_profile_id
		WHERE (r.creator_profile_id=$1 OR EXISTS (
			SELECT 1 FROM watch_room_invites i WHERE i.room_id=r.id AND i.profile_id=$1
		)) AND r.expires_at>$2 AND r.status<>'ended' ORDER BY r.created_at DESC`, profileID, now)
	if err != nil {
		return nil, fmt.Errorf("list watch room invitations: %w", err)
	}
	defer rows.Close()
	rooms := make([]models.WatchRoom, 0)
	for rows.Next() {
		room, err := scanWatchRoomRow(rows)
		if err != nil {
			return nil, err
		}
		room.Members, err = r.listMembers(ctx, room.ID)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, *room)
	}
	return rooms, rows.Err()
}

func (r *pgWatchRoomRepo) IsInvited(ctx context.Context, roomID, profileID string) (bool, error) {
	var allowed bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM watch_rooms r WHERE r.id=$1 AND (r.creator_profile_id=$2 OR EXISTS(
			SELECT 1 FROM watch_room_invites i WHERE i.room_id=r.id AND i.profile_id=$2)))`, roomID, profileID).Scan(&allowed)
	return allowed, err
}

func (r *pgWatchRoomRepo) Join(ctx context.Context, roomID, profileID, clientID string, capabilities models.WatchRoomClientCapabilities, now time.Time) error {
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("marshal watch room capabilities: %w", err)
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO watch_room_members
		(room_id, profile_id, client_id, joined_at, last_seen_at, capabilities) VALUES ($1,$2,$3,$4,$4,$5)
		ON CONFLICT (room_id, profile_id) DO UPDATE SET client_id=EXCLUDED.client_id,
		last_seen_at=EXCLUDED.last_seen_at, capabilities=EXCLUDED.capabilities`, roomID, profileID, clientID, now, capabilitiesJSON)
	return err
}

func (r *pgWatchRoomRepo) SetReady(ctx context.Context, roomID, profileID string, ready bool, now time.Time) error {
	_, err := r.pool.Exec(ctx, `WITH member_update AS (
		UPDATE watch_room_members SET ready=$3,last_seen_at=$4 WHERE room_id=$1 AND profile_id=$2
	), release AS (
		SELECT $3
			AND NOT EXISTS (SELECT 1 FROM watch_room_members WHERE room_id=$1 AND profile_id<>$2 AND NOT ready)
			AND NOT EXISTS (SELECT 1 FROM watch_room_guest_members WHERE room_id=$1 AND NOT ready)
			AS all_ready
	)
	UPDATE watch_rooms SET waiting_for_ready=false,status='playing',revision=revision+1,updated_by=$2,anchor_updated_at=$4
	WHERE id=$1 AND waiting_for_ready AND (SELECT all_ready FROM release)`, roomID, profileID, ready, now)
	return err
}

func (r *pgWatchRoomRepo) UpdateState(ctx context.Context, roomID, profileID, status string, position, duration float64, expectedRevision *int64, now time.Time) (bool, error) {
	var updated bool
	err := r.pool.QueryRow(ctx, `WITH candidate AS MATERIALIZED (
		SELECT id, status='lobby' AND $3='playing' AS starting
		FROM watch_rooms
		WHERE id=$1 AND status<>'ended' AND ($7::bigint IS NULL OR revision=$7)
		FOR UPDATE
	), room_update AS (
		UPDATE watch_rooms r SET status=$3,
			waiting_for_ready=CASE WHEN candidate.starting THEN true ELSE r.waiting_for_ready END,position=$4,
			duration=GREATEST(r.duration,$5),revision=r.revision+1,updated_by=$2,anchor_updated_at=$6
		FROM candidate WHERE r.id=candidate.id
		RETURNING r.id, candidate.starting
	), reset_ready AS (
		UPDATE watch_room_members m SET ready=false
		FROM room_update WHERE room_update.starting AND m.room_id=room_update.id
	), reset_guest_ready AS (
		UPDATE watch_room_guest_members g SET ready=false
		FROM room_update WHERE room_update.starting AND g.room_id=room_update.id
	)
	SELECT EXISTS(SELECT 1 FROM room_update)`, roomID, profileID, status, position, duration, now, expectedRevision).Scan(&updated)
	return updated, err
}

func (r *pgWatchRoomRepo) Heartbeat(ctx context.Context, roomID, profileID, clientID string, buffering bool, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE watch_room_members SET client_id=$3,buffering=$4,last_seen_at=$5 WHERE room_id=$1 AND profile_id=$2`, roomID, profileID, clientID, buffering, now)
	return err
}

func (r *pgWatchRoomRepo) Leave(ctx context.Context, roomID, profileID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watch_room_members WHERE room_id=$1 AND profile_id=$2 AND NOT is_creator`, roomID, profileID)
	return err
}

func (r *pgWatchRoomRepo) End(ctx context.Context, roomID, profileID string, now time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE watch_rooms SET status='ended',revision=revision+1,updated_by=$2,
		anchor_updated_at=$3,ended_at=$3,end_reason='host_ended' WHERE id=$1 AND creator_profile_id=$2 AND status<>'ended'`, roomID, profileID, now)
	return err == nil && tag.RowsAffected() > 0, err
}

func (r *pgWatchRoomRepo) EndExpired(ctx context.Context, roomID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE watch_rooms SET status='ended',revision=revision+1,anchor_updated_at=$2,
		ended_at=COALESCE(ended_at,$2),end_reason=CASE WHEN end_reason='' THEN 'expired' ELSE end_reason END
		WHERE id=$1 AND status<>'ended'`, roomID, now)
	return err
}

func (r *pgWatchRoomRepo) Sweep(ctx context.Context, now, disconnectCutoff, deleteBefore time.Time) (int64, int64, error) {
	expired, err := r.pool.Exec(ctx, `UPDATE watch_rooms SET status='ended',revision=revision+1,
		anchor_updated_at=$1,ended_at=$1,end_reason='expired' WHERE status<>'ended' AND expires_at<=$1`, now)
	if err != nil {
		return 0, 0, err
	}
	inactive, err := r.pool.Exec(ctx, `WITH activity AS (
		SELECT r.id,
			MAX(m.last_seen_at) AS latest_seen,
			MAX(m.last_seen_at) FILTER (WHERE m.profile_id=r.creator_profile_id) AS host_seen
		FROM watch_rooms r LEFT JOIN watch_room_members m ON m.room_id=r.id
		WHERE r.status<>'ended' GROUP BY r.id
	)
	UPDATE watch_rooms r SET status='ended',revision=revision+1,anchor_updated_at=$1,ended_at=$1,
		end_reason=CASE WHEN COALESCE(a.latest_seen,r.created_at)<$2 THEN 'all_left' ELSE 'host_disconnected' END
	FROM activity a WHERE r.id=a.id AND COALESCE(a.host_seen,r.created_at)<$2`, now, disconnectCutoff)
	if err != nil {
		return 0, 0, err
	}
	deleted, err := r.pool.Exec(ctx, `DELETE FROM watch_rooms WHERE ended_at IS NOT NULL AND ended_at<$1`, deleteBefore)
	if err != nil {
		return 0, 0, err
	}
	return expired.RowsAffected() + inactive.RowsAffected(), deleted.RowsAffected(), nil
}

func (r *pgWatchRoomRepo) CreateAccountInvite(ctx context.Context, invite *models.WatchRoomAccountInvite) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO watch_room_account_invites
		(id,room_id,inviter_account_id,invitee_account_id,status,created_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (room_id,invitee_account_id) DO UPDATE SET id=EXCLUDED.id,status='pending',
			accepted_profile_id=NULL,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at,
			responded_at=NULL,revoked_at=NULL
		WHERE watch_room_account_invites.status IN ('declined','revoked')`,
		invite.ID, invite.RoomID, invite.InviterAccountID, invite.InviteeAccountID, invite.Status, invite.CreatedAt, invite.ExpiresAt)
	return err == nil && tag.RowsAffected() > 0, err
}

func (r *pgWatchRoomRepo) ListAccountInvites(ctx context.Context, accountID string, now time.Time) ([]models.WatchRoomAccountInvite, error) {
	rows, err := r.pool.Query(ctx, `SELECT i.id,i.room_id,i.status,creator.name,r.title,r.media_type,r.item_id,
		r.poster_url,r.backdrop_url,i.created_at,i.expires_at,i.responded_at,i.revoked_at
		FROM watch_room_account_invites i
		JOIN watch_rooms r ON r.id=i.room_id
		JOIN users creator ON creator.id=r.creator_profile_id
		WHERE i.invitee_account_id=$1 AND i.status='pending' AND i.expires_at>$2
			AND r.status<>'ended' AND r.expires_at>$2 ORDER BY i.created_at DESC`, accountID, now)
	if err != nil {
		return nil, fmt.Errorf("list watch room account invitations: %w", err)
	}
	defer rows.Close()
	invites := make([]models.WatchRoomAccountInvite, 0)
	for rows.Next() {
		var invite models.WatchRoomAccountInvite
		if err := rows.Scan(&invite.ID, &invite.RoomID, &invite.Status, &invite.CreatorName, &invite.Title,
			&invite.MediaType, &invite.ItemID, &invite.PosterURL, &invite.BackdropURL, &invite.CreatedAt,
			&invite.ExpiresAt, &invite.RespondedAt, &invite.RevokedAt); err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func (r *pgWatchRoomRepo) ListRoomAccountInvites(ctx context.Context, roomID string, now time.Time) ([]models.WatchRoomAccountInvite, error) {
	rows, err := r.pool.Query(ctx, `SELECT i.id,i.room_id,i.invitee_account_id,target.username,COALESCE(i.accepted_profile_id,''),
		i.status,i.created_at,i.expires_at,i.responded_at,i.revoked_at
		FROM watch_room_account_invites i JOIN accounts target ON target.id=i.invitee_account_id
		WHERE i.room_id=$1 AND i.status='pending' AND i.expires_at>$2 ORDER BY i.created_at`, roomID, now)
	if err != nil {
		return nil, fmt.Errorf("list room account invitations: %w", err)
	}
	defer rows.Close()
	invites := make([]models.WatchRoomAccountInvite, 0)
	for rows.Next() {
		var invite models.WatchRoomAccountInvite
		if err := rows.Scan(&invite.ID, &invite.RoomID, &invite.InviteeAccountID, &invite.InviteeUsername,
			&invite.AcceptedProfileID, &invite.Status, &invite.CreatedAt, &invite.ExpiresAt,
			&invite.RespondedAt, &invite.RevokedAt); err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func (r *pgWatchRoomRepo) AcceptAccountInvite(ctx context.Context, inviteID, accountID, profileID string, now time.Time) (string, bool, error) {
	tx, err := beginDBTx(ctx, r.pool)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var roomID string
	err = tx.QueryRow(ctx, `SELECT i.room_id FROM watch_room_account_invites i
		JOIN watch_rooms r ON r.id=i.room_id
		WHERE i.id=$1 AND i.invitee_account_id=$2 AND i.status='pending' AND i.expires_at>$3
			AND r.status<>'ended' AND r.expires_at>$3 FOR UPDATE`, inviteID, accountID, now).Scan(&roomID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO watch_room_invites(room_id,profile_id,invited_at)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, roomID, profileID, now); err != nil {
		return "", false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE watch_room_account_invites SET status='accepted',accepted_profile_id=$2,
		responded_at=$3 WHERE id=$1 AND status='pending'`, inviteID, profileID, now)
	if err != nil || tag.RowsAffected() == 0 {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return roomID, true, nil
}

func (r *pgWatchRoomRepo) DeclineAccountInvite(ctx context.Context, inviteID, accountID string, now time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE watch_room_account_invites SET status='declined',responded_at=$3
		WHERE id=$1 AND invitee_account_id=$2 AND status='pending'`, inviteID, accountID, now)
	return err == nil && tag.RowsAffected() > 0, err
}

func (r *pgWatchRoomRepo) RevokeAccountInvite(ctx context.Context, inviteID, roomID, creatorProfileID string, now time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE watch_room_account_invites i SET status='revoked',revoked_at=$4
		FROM watch_rooms r WHERE i.id=$1 AND i.room_id=$2 AND r.id=i.room_id
			AND r.creator_profile_id=$3 AND i.status='pending'`, inviteID, roomID, creatorProfileID, now)
	return err == nil && tag.RowsAffected() > 0, err
}

func (r *pgWatchRoomRepo) ReplaceExternalInvite(ctx context.Context, invite *models.WatchRoomExternalInvite) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO watch_room_external_invites
		(room_id,token_hash,short_code,active,created_at,expires_at)
		VALUES ($1,$2,$3,true,$4,$5)
		ON CONFLICT (room_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,
			short_code=EXCLUDED.short_code,active=true,created_at=EXCLUDED.created_at,
			expires_at=EXCLUDED.expires_at`, invite.RoomID, invite.TokenHash, invite.ShortCode, invite.CreatedAt, invite.ExpiresAt)
	return err
}

func (r *pgWatchRoomRepo) RevokeExternalInvite(ctx context.Context, roomID, creatorProfileID string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE watch_room_external_invites i SET active=false
		FROM watch_rooms r WHERE i.room_id=$1 AND r.id=i.room_id AND r.creator_profile_id=$2 AND i.active`, roomID, creatorProfileID)
	return err == nil && tag.RowsAffected() > 0, err
}

func (r *pgWatchRoomRepo) GetExternalInviteByTokenHash(ctx context.Context, tokenHash string, now time.Time) (*models.WatchRoomExternalInvite, error) {
	return r.getExternalInvite(ctx, `i.token_hash=$1`, tokenHash, now)
}

func (r *pgWatchRoomRepo) GetExternalInviteByCode(ctx context.Context, shortCode string, now time.Time) (*models.WatchRoomExternalInvite, error) {
	return r.getExternalInvite(ctx, `i.short_code=$1`, shortCode, now)
}

func (r *pgWatchRoomRepo) getExternalInvite(ctx context.Context, predicate string, value string, now time.Time) (*models.WatchRoomExternalInvite, error) {
	row := r.pool.QueryRow(ctx, `SELECT i.room_id,u.account_id,i.token_hash,i.short_code,i.active,i.created_at,i.expires_at
		FROM watch_room_external_invites i
		JOIN watch_rooms r ON r.id=i.room_id
		JOIN users u ON u.id=r.creator_profile_id
		WHERE `+predicate+` AND i.active AND i.expires_at>$2 AND r.status<>'ended' AND r.expires_at>$2`, value, now)
	var invite models.WatchRoomExternalInvite
	if err := row.Scan(&invite.RoomID, &invite.AccountID, &invite.TokenHash, &invite.ShortCode, &invite.Active, &invite.CreatedAt, &invite.ExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &invite, nil
}

func (r *pgWatchRoomRepo) JoinExternalGuest(ctx context.Context, roomID, guestID, name, clientID string, capabilities models.WatchRoomClientCapabilities, now time.Time) error {
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("marshal external guest capabilities: %w", err)
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO watch_room_guest_members
		(room_id,guest_id,name,client_id,joined_at,last_seen_at,capabilities)
		VALUES ($1,$2,$3,$4,$5,$5,$6)
		ON CONFLICT (room_id,guest_id) DO UPDATE SET name=EXCLUDED.name,client_id=EXCLUDED.client_id,
			last_seen_at=EXCLUDED.last_seen_at,capabilities=EXCLUDED.capabilities`, roomID, guestID, name, clientID, now, capabilitiesJSON)
	return err
}

// IsExternalGuest reports whether a guest who already joined may keep using the
// room. It deliberately ignores the invitation's state: disabling or rotating
// the link only stops new joins (JoinExternalGuest and the invitation lookups
// still require an active invitation); guests already in the room stay until
// they leave or the room ends or expires.
func (r *pgWatchRoomRepo) IsExternalGuest(ctx context.Context, roomID, guestID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM watch_room_guest_members g
		JOIN watch_rooms r ON r.id=g.room_id
		WHERE g.room_id=$1 AND g.guest_id=$2
			AND r.status<>'ended' AND r.expires_at>now())`, roomID, guestID).Scan(&exists)
	return exists, err
}

func (r *pgWatchRoomRepo) HeartbeatExternalGuest(ctx context.Context, roomID, guestID, clientID string, buffering bool, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE watch_room_guest_members SET client_id=$3,buffering=$4,last_seen_at=$5
		WHERE room_id=$1 AND guest_id=$2`, roomID, guestID, clientID, buffering, now)
	return err
}

func (r *pgWatchRoomRepo) LeaveExternalGuest(ctx context.Context, roomID, guestID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM watch_room_guest_members WHERE room_id=$1 AND guest_id=$2`, roomID, guestID)
	return err
}

func (r *pgWatchRoomRepo) SetExternalGuestReady(ctx context.Context, roomID, guestID string, ready bool, now time.Time) error {
	_, err := r.pool.Exec(ctx, `WITH member_update AS (
		UPDATE watch_room_guest_members SET ready=$3,last_seen_at=$4 WHERE room_id=$1 AND guest_id=$2
	), release AS (
		SELECT $3
			AND NOT EXISTS (SELECT 1 FROM watch_room_members WHERE room_id=$1 AND NOT ready)
			AND NOT EXISTS (SELECT 1 FROM watch_room_guest_members WHERE room_id=$1 AND guest_id<>$2 AND NOT ready)
			AS all_ready
	)
	UPDATE watch_rooms SET waiting_for_ready=false,status='playing',revision=revision+1,
		updated_by='guest:'||$2,anchor_updated_at=$4
	WHERE id=$1 AND waiting_for_ready AND (SELECT all_ready FROM release)`, roomID, guestID, ready, now)
	return err
}

func (r *pgWatchRoomRepo) BindExternalSource(ctx context.Context, roomID, creatorProfileID, resource string, params map[string]string, now time.Time) (bool, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return false, fmt.Errorf("marshal external watch source params: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `INSERT INTO watch_room_external_sources (room_id,resource,params,bound_at)
		SELECT r.id,$3,$4,$5 FROM watch_rooms r
		WHERE r.id=$1 AND r.creator_profile_id=$2 AND r.status<>'ended'
		ON CONFLICT (room_id) DO NOTHING`, roomID, creatorProfileID, resource, paramsJSON, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	existing, err := r.GetExternalSource(ctx, roomID)
	return err == nil && existing != nil && existing.Resource == resource, err
}

func (r *pgWatchRoomRepo) GetExternalSource(ctx context.Context, roomID string) (*models.WatchRoomExternalSource, error) {
	row := r.pool.QueryRow(ctx, `SELECT room_id,resource,params,bound_at FROM watch_room_external_sources WHERE room_id=$1`, roomID)
	var source models.WatchRoomExternalSource
	var params []byte
	if err := row.Scan(&source.RoomID, &source.Resource, &params, &source.BoundAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(params, &source.Params); err != nil {
		return nil, err
	}
	return &source, nil
}

func (r *pgWatchRoomRepo) listMembers(ctx context.Context, roomID string) ([]models.WatchRoomMember, error) {
	rows, err := r.pool.Query(ctx, `WITH people AS (
			SELECT creator_profile_id AS profile_id FROM watch_rooms WHERE id=$1
			UNION
			SELECT profile_id FROM watch_room_invites WHERE room_id=$1
		)
		SELECT p.profile_id,u.name,u.color,u.icon_url,COALESCE(m.client_id,''),
			(p.profile_id=r.creator_profile_id),COALESCE(m.ready,false),COALESCE(m.buffering,false),
			(m.profile_id IS NOT NULL),COALESCE(m.joined_at,r.created_at),
			COALESCE(m.last_seen_at,r.created_at - interval '1 day'),COALESCE(m.capabilities,'{}'::jsonb)
		FROM people p
		JOIN watch_rooms r ON r.id=$1
		JOIN users u ON u.id=p.profile_id
		LEFT JOIN watch_room_members m ON m.room_id=r.id AND m.profile_id=p.profile_id
		ORDER BY (p.profile_id=r.creator_profile_id) DESC,COALESCE(m.joined_at,r.created_at),u.name`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]models.WatchRoomMember, 0)
	for rows.Next() {
		var m models.WatchRoomMember
		var capabilities []byte
		if err := rows.Scan(&m.ProfileID, &m.Name, &m.Color, &m.IconURL, &m.ClientID, &m.IsCreator, &m.Ready, &m.Buffering, &m.Joined, &m.JoinedAt, &m.LastSeenAt, &capabilities); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(capabilities, &m.Capabilities); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	guestRows, err := r.pool.Query(ctx, `SELECT guest_id,name,client_id,ready,buffering,joined_at,last_seen_at,capabilities
		FROM watch_room_guest_members WHERE room_id=$1 ORDER BY joined_at,name`, roomID)
	if err != nil {
		return nil, err
	}
	defer guestRows.Close()
	for guestRows.Next() {
		var m models.WatchRoomMember
		var guestID string
		var capabilities []byte
		if err := guestRows.Scan(&guestID, &m.Name, &m.ClientID, &m.Ready, &m.Buffering, &m.JoinedAt, &m.LastSeenAt, &capabilities); err != nil {
			return nil, err
		}
		m.ProfileID = "guest:" + guestID
		m.IsGuest = true
		m.Joined = true
		if err := json.Unmarshal(capabilities, &m.Capabilities); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, guestRows.Err()
}

func scanWatchRoom(row pgx.Row) (*models.WatchRoom, error) {
	room, err := scanWatchRoomRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return room, err
}

func scanWatchRoomRow(row pgx.Row) (*models.WatchRoom, error) {
	var room models.WatchRoom
	var params []byte
	if err := row.Scan(&room.ID, &room.CreatorProfileID, &room.CreatorName, &room.Title, &room.MediaType, &room.ItemID,
		&room.PosterURL, &room.BackdropURL, &params, &room.Status, &room.WaitingForReady, &room.Position, &room.Duration, &room.Revision,
		&room.UpdatedBy, &room.AnchorUpdatedAt, &room.CreatedAt, &room.ExpiresAt, &room.EndedAt, &room.EndReason); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(params, &room.Params); err != nil {
		return nil, err
	}
	if room.Params == nil {
		room.Params = map[string]string{}
	}
	return &room, nil
}

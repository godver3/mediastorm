package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgUserRepo struct {
	pool DB
}

const userColumns = `id, account_id, name, color, icon_url, pin_hash, trakt_account_id, plex_account_id,
	mdblist_account_id, simkl_account_id, is_kids_profile, kids_mode, kids_max_rating, kids_max_movie_rating, kids_max_tv_rating,
	kids_allowed_lists, created_at, updated_at`

func (r *pgUserRepo) Get(ctx context.Context, id string) (*models.User, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}

func (r *pgUserRepo) ListByAccount(ctx context.Context, accountID string) ([]models.User, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE account_id = $1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list users by account: %w", err)
	}
	defer rows.Close()
	return collectUsers(rows)
}

func (r *pgUserRepo) List(ctx context.Context) ([]models.User, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	return collectUsers(rows)
}

func (r *pgUserRepo) Create(ctx context.Context, user *models.User) error {
	listsJSON, _ := json.Marshal(user.KidsAllowedLists)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO users (`+userColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		user.ID, user.AccountID, user.Name, user.Color, user.IconURL, user.PinHash,
		user.TraktAccountID, user.PlexAccountID, user.MdblistAccountID, user.SimklAccountID, user.IsKidsProfile,
		user.KidsMode, user.KidsMaxRating, user.KidsMaxMovieRating, user.KidsMaxTVRating,
		listsJSON, user.CreatedAt, user.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (r *pgUserRepo) Update(ctx context.Context, user *models.User) error {
	listsJSON, _ := json.Marshal(user.KidsAllowedLists)
	_, err := r.pool.Exec(ctx, `
		UPDATE users SET account_id=$2, name=$3, color=$4, icon_url=$5, pin_hash=$6,
		trakt_account_id=$7, plex_account_id=$8, mdblist_account_id=$9, simkl_account_id=$10, is_kids_profile=$11,
		kids_mode=$12, kids_max_rating=$13, kids_max_movie_rating=$14, kids_max_tv_rating=$15,
		kids_allowed_lists=$16, updated_at=$17
		WHERE id=$1`,
		user.ID, user.AccountID, user.Name, user.Color, user.IconURL, user.PinHash,
		user.TraktAccountID, user.PlexAccountID, user.MdblistAccountID, user.SimklAccountID, user.IsKidsProfile,
		user.KidsMode, user.KidsMaxRating, user.KidsMaxMovieRating, user.KidsMaxTVRating,
		listsJSON, user.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return nil
}

func (r *pgUserRepo) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

func (r *pgUserRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func scanUser(row pgx.Row) (*models.User, error) {
	var u models.User
	var listsJSON []byte
	err := row.Scan(&u.ID, &u.AccountID, &u.Name, &u.Color, &u.IconURL, &u.PinHash,
		&u.TraktAccountID, &u.PlexAccountID, &u.MdblistAccountID, &u.SimklAccountID, &u.IsKidsProfile,
		&u.KidsMode, &u.KidsMaxRating, &u.KidsMaxMovieRating, &u.KidsMaxTVRating,
		&listsJSON, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	if listsJSON != nil {
		_ = json.Unmarshal(listsJSON, &u.KidsAllowedLists)
	}
	return &u, nil
}

func collectUsers(rows pgx.Rows) ([]models.User, error) {
	var result []models.User
	for rows.Next() {
		var u models.User
		var listsJSON []byte
		err := rows.Scan(&u.ID, &u.AccountID, &u.Name, &u.Color, &u.IconURL, &u.PinHash,
			&u.TraktAccountID, &u.PlexAccountID, &u.MdblistAccountID, &u.SimklAccountID, &u.IsKidsProfile,
			&u.KidsMode, &u.KidsMaxRating, &u.KidsMaxMovieRating, &u.KidsMaxTVRating,
			&listsJSON, &u.CreatedAt, &u.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		if listsJSON != nil {
			_ = json.Unmarshal(listsJSON, &u.KidsAllowedLists)
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"novastream/models"
	"time"
)

type pgTasteOnboardingRepo struct{ pool *pgxpool.Pool }

func readTasteState(ctx context.Context, db DB, profile string) (models.TasteOnboardingState, error) {
	state := models.TasteOnboardingState{Status: "new", Interests: []models.TasteInterest{}}
	var raw []byte
	err := db.QueryRow(ctx, `SELECT status,source,interests,updated_at FROM profile_taste_onboarding WHERE profile_id=$1`, profile).Scan(&state.Status, &state.Source, &raw, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(raw, &state.Interests); err != nil {
		return state, err
	}
	return state, nil
}
func (r *pgTasteOnboardingRepo) Get(ctx context.Context, profile string) (models.TasteOnboardingState, error) {
	return readTasteState(ctx, r.pool, profile)
}
func (r *pgTasteOnboardingRepo) withLock(ctx context.Context, profile string, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "taste-onboarding:"+profile); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgTasteOnboardingRepo) Save(ctx context.Context, profile string, state models.TasteOnboardingState) error {
	return r.withLock(ctx, profile, func(tx pgx.Tx) error {
		previous, err := readTasteState(ctx, tx, profile)
		if err != nil {
			return err
		}
		state.Interests = models.MergeTasteInterests(previous.Interests, state.Interests)
		raw, err := json.Marshal(state.Interests)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO profile_taste_onboarding(profile_id,status,source,interests,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(profile_id) DO UPDATE SET status=EXCLUDED.status,source=EXCLUDED.source,interests=EXCLUDED.interests,updated_at=EXCLUDED.updated_at`, profile, state.Status, state.Source, raw, state.UpdatedAt)
		return err
	})
}
func (r *pgTasteOnboardingRepo) Skip(ctx context.Context, profile string, now time.Time) error {
	return r.withLock(ctx, profile, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO profile_taste_onboarding(profile_id,status,source,interests,updated_at) VALUES($1,'skipped','','[]',$2) ON CONFLICT(profile_id) DO UPDATE SET status='skipped',updated_at=EXCLUDED.updated_at`, profile, now)
		return err
	})
}
func (r *pgTasteOnboardingRepo) Delete(ctx context.Context, profile string) error {
	return r.withLock(ctx, profile, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM profile_taste_onboarding WHERE profile_id=$1`, profile)
		return err
	})
}

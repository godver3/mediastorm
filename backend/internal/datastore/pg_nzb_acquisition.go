package datastore

import (
	"context"
	"time"

	"novastream/internal/nzbfetch"
	"novastream/internal/providerbreaker"
)

type NZBAcquisitionRepository struct{ store *DataStore }

func (ds *DataStore) NZBAcquisition() *NZBAcquisitionRepository {
	return &NZBAcquisitionRepository{store: ds}
}

// ReserveNZB holds a transaction lock only while accounting, never while doing
// HTTP or NNTP work. Separate titles can reserve and download independently.
func (r *NZBAcquisitionRepository) ReserveNZB(ctx context.Context, title, provider string, now time.Time) (time.Time, error) {
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, title); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM nzb_download_attempts WHERE attempted_at <= $1`, now.Add(-nzbfetch.Window)); err != nil {
		return time.Time{}, err
	}
	var count, perIndexer int
	var first, last, firstIndexer *time.Time
	err = tx.QueryRow(ctx, `SELECT count(*), min(attempted_at), max(attempted_at),
        count(*) FILTER (WHERE provider = $2), min(attempted_at) FILTER (WHERE provider = $2)
        FROM nzb_download_attempts WHERE title_key = $1`, title, provider).Scan(&count, &first, &last, &perIndexer, &firstIndexer)
	if err != nil {
		return time.Time{}, err
	}
	var retryAt time.Time
	if last != nil {
		retryAt = last.Add(nzbfetch.Spacing)
	}
	if count >= nzbfetch.TitleLimit && first != nil && first.Add(nzbfetch.Window).After(retryAt) {
		retryAt = first.Add(nzbfetch.Window)
	}
	if perIndexer >= nzbfetch.IndexerLimit && firstIndexer != nil && firstIndexer.Add(nzbfetch.Window).After(retryAt) {
		retryAt = firstIndexer.Add(nzbfetch.Window)
	}
	if retryAt.After(now) {
		return retryAt, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO nzb_download_attempts(title_key, provider, attempted_at) VALUES ($1, $2, $3)`, title, provider, now); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, tx.Commit(ctx)
}

func (r *NZBAcquisitionRepository) LoadCooldowns(ctx context.Context) (map[string]providerbreaker.Cooldown, error) {
	rows, err := r.store.pool.Query(ctx, `SELECT provider, until_at, failures FROM indexer_cooldowns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]providerbreaker.Cooldown)
	for rows.Next() {
		var name string
		var c providerbreaker.Cooldown
		if err := rows.Scan(&name, &c.Until, &c.Failures); err != nil {
			return nil, err
		}
		out[name] = c
	}
	return out, rows.Err()
}

func (r *NZBAcquisitionRepository) SaveCooldown(ctx context.Context, provider string, c providerbreaker.Cooldown) error {
	_, err := r.store.pool.Exec(ctx, `INSERT INTO indexer_cooldowns(provider, until_at, failures) VALUES ($1, $2, $3)
        ON CONFLICT (provider) DO UPDATE SET until_at = GREATEST(indexer_cooldowns.until_at, EXCLUDED.until_at),
        failures = GREATEST(indexer_cooldowns.failures, EXCLUDED.failures)`, provider, c.Until, c.Failures)
	return err
}

func (r *NZBAcquisitionRepository) ClearCooldown(ctx context.Context, provider string, until time.Time) error {
	_, err := r.store.pool.Exec(ctx, `DELETE FROM indexer_cooldowns WHERE provider = $1 AND until_at <= $2`, provider, until)
	return err
}

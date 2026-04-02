package datastore

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"novastream/models"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	// goose needs a *sql.DB, so wrap the pgx pool via stdlib
	db := stdlib.OpenDBFromPool(pool)

	goose.SetBaseFS(embedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	if err := runDataMigrations(ctx, pool); err != nil {
		return fmt.Errorf("data migrations: %w", err)
	}

	return nil
}

func runDataMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS app_data_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("ensure app_data_migrations: %w", err)
	}

	ds := &DataStore{pool: pool}
	migrations := []struct {
		name string
		run  func(context.Context, *DataStore) error
	}{
		{name: "watchlist_reconcile_v1", run: reconcileWatchlistDataMigration},
	}

	for _, migration := range migrations {
		var appliedAt time.Time
		err := pool.QueryRow(ctx, `SELECT applied_at FROM app_data_migrations WHERE name = $1`, migration.name).Scan(&appliedAt)
		if err == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return fmt.Errorf("check data migration %s: %w", migration.name, err)
		}

		if err := migration.run(ctx, ds); err != nil {
			return fmt.Errorf("run %s: %w", migration.name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO app_data_migrations (name) VALUES ($1)`, migration.name); err != nil {
			return fmt.Errorf("record data migration %s: %w", migration.name, err)
		}
	}

	return nil
}

func reconcileWatchlistDataMigration(ctx context.Context, ds *DataStore) error {
	allItems, err := ds.Watchlist().ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list watchlist: %w", err)
	}

	reconciled := make(map[string]map[string]models.WatchlistItem, len(allItems))
	for userID, items := range allItems {
		perUser := make(map[string]models.WatchlistItem, len(items))
		for _, item := range items {
			normalized := normalizeWatchlistItemForMigration(item)
			merged, found := takeMergedWatchlistItemForMigration(perUser, normalized.MediaType, normalized.ID, normalized.ExternalIDs)
			if found {
				normalized = mergeWatchlistItemsForMigration(merged, normalized)
			}
			perUser[normalized.Key()] = normalized
		}
		reconciled[userID] = perUser
	}

	return ds.WithTx(ctx, func(tx *Tx) error {
		existingAll, err := tx.Watchlist().ListAll(ctx)
		if err != nil {
			return err
		}

		dbKeys := make(map[string]map[string]bool, len(existingAll))
		for userID, items := range existingAll {
			keys := make(map[string]bool, len(items))
			for _, item := range items {
				keys[item.Key()] = true
			}
			dbKeys[userID] = keys
		}

		for userID, perUser := range reconciled {
			items := make([]models.WatchlistItem, 0, len(perUser))
			for _, item := range perUser {
				items = append(items, item)
			}
			if err := tx.Watchlist().BulkUpsert(ctx, userID, items); err != nil {
				return err
			}
			if existing, ok := dbKeys[userID]; ok {
				for key := range existing {
					if _, keep := perUser[key]; !keep {
						if err := tx.Watchlist().Delete(ctx, userID, key); err != nil {
							return err
						}
					}
				}
			}
			delete(dbKeys, userID)
		}

		for userID := range dbKeys {
			if err := tx.Watchlist().DeleteByUser(ctx, userID); err != nil {
				return err
			}
		}

		return nil
	})
}

func normalizeWatchlistItemForMigration(item models.WatchlistItem) models.WatchlistItem {
	item.MediaType = strings.ToLower(strings.TrimSpace(item.MediaType))
	item.ExternalIDs = normalizeWatchlistExternalIDsForMigration(item.ExternalIDs)
	item.ID = canonicalWatchlistIDForMigration(item.MediaType, item.ID, item.ExternalIDs)
	if item.AddedAt.IsZero() {
		item.AddedAt = time.Now().UTC()
	}
	return item
}

func takeMergedWatchlistItemForMigration(perUser map[string]models.WatchlistItem, mediaType, canonicalID string, externalIDs map[string]string) (models.WatchlistItem, bool) {
	var merged models.WatchlistItem
	found := false
	for _, key := range watchlistCandidateKeysForMigration(mediaType, canonicalID, externalIDs) {
		existing, ok := perUser[key]
		if !ok {
			continue
		}
		if !found {
			merged = existing
			found = true
		} else {
			merged = mergeWatchlistItemsForMigration(merged, existing)
		}
		delete(perUser, key)
	}
	for key, existing := range perUser {
		if !watchlistItemsEquivalentForMigration(mediaType, canonicalID, externalIDs, existing) {
			continue
		}
		if !found {
			merged = existing
			found = true
		} else {
			merged = mergeWatchlistItemsForMigration(merged, existing)
		}
		delete(perUser, key)
	}
	return merged, found
}

func mergeWatchlistItemsForMigration(base, incoming models.WatchlistItem) models.WatchlistItem {
	base.ExternalIDs = normalizeWatchlistExternalIDsForMigration(base.ExternalIDs)
	incoming.ExternalIDs = normalizeWatchlistExternalIDsForMigration(incoming.ExternalIDs)
	if base.ID == "" {
		base.ID = incoming.ID
	}
	if base.MediaType == "" {
		base.MediaType = incoming.MediaType
	}
	if base.Name == "" {
		base.Name = incoming.Name
	}
	if base.Overview == "" {
		base.Overview = incoming.Overview
	}
	if base.Year == 0 {
		base.Year = incoming.Year
	}
	if base.PosterURL == "" {
		base.PosterURL = incoming.PosterURL
	}
	if base.TextPosterURL == "" {
		base.TextPosterURL = incoming.TextPosterURL
	}
	if base.BackdropURL == "" {
		base.BackdropURL = incoming.BackdropURL
	}
	if base.RuntimeMinutes == 0 {
		base.RuntimeMinutes = incoming.RuntimeMinutes
	}
	if base.AddedAt.IsZero() || (!incoming.AddedAt.IsZero() && incoming.AddedAt.Before(base.AddedAt)) {
		base.AddedAt = incoming.AddedAt
	}
	if strings.TrimSpace(base.SyncSource) == "" {
		base.SyncSource = incoming.SyncSource
	}
	if base.SyncedAt == nil || (incoming.SyncedAt != nil && incoming.SyncedAt.After(*base.SyncedAt)) {
		base.SyncedAt = incoming.SyncedAt
	}
	base.ExternalIDs = mergeWatchlistExternalIDsForMigration(base.ExternalIDs, incoming.ExternalIDs)
	if len(base.Genres) == 0 && len(incoming.Genres) > 0 {
		base.Genres = append([]string{}, incoming.Genres...)
	}
	base.ID = canonicalWatchlistIDForMigration(base.MediaType, base.ID, base.ExternalIDs)
	return base
}

func mergeWatchlistExternalIDsForMigration(base, incoming map[string]string) map[string]string {
	if len(base) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(incoming))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range incoming {
		if strings.TrimSpace(out[k]) == "" && strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}

func normalizeWatchlistExternalIDsForMigration(ids map[string]string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]string, len(ids))
	for k, v := range ids {
		key := strings.ToLower(strings.TrimSpace(k))
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func canonicalWatchlistIDForMigration(mediaType, id string, externalIDs map[string]string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	id = strings.TrimSpace(id)
	switch mediaType {
	case "movie":
		if v := strings.TrimSpace(externalIDs["tvdb"]); v != "" {
			return "tvdb:movie:" + v
		}
		if v := strings.TrimSpace(externalIDs["tmdb"]); v != "" {
			return "tmdb:movie:" + v
		}
		if v := strings.TrimSpace(externalIDs["imdb"]); v != "" {
			return v
		}
	case "series":
		if v := strings.TrimSpace(externalIDs["tvdb"]); v != "" {
			return "tvdb:series:" + v
		}
		if v := strings.TrimSpace(externalIDs["tmdb"]); v != "" {
			return "tmdb:tv:" + v
		}
		if v := strings.TrimSpace(externalIDs["imdb"]); v != "" {
			return v
		}
	}
	return id
}

func watchlistCandidateKeysForMigration(mediaType, canonicalID string, externalIDs map[string]string) []string {
	candidates := make([]string, 0, 8)
	seen := make(map[string]bool)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := strings.ToLower(mediaType + ":" + id)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, mediaType+":"+id)
	}

	add(canonicalID)
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		if v := strings.TrimSpace(externalIDs["tvdb"]); v != "" {
			add("tvdb:movie:" + v)
			add(v)
		}
		if v := strings.TrimSpace(externalIDs["tmdb"]); v != "" {
			add("tmdb:movie:" + v)
			add(v)
		}
	case "series":
		if v := strings.TrimSpace(externalIDs["tvdb"]); v != "" {
			add("tvdb:series:" + v)
			add(v)
		}
		if v := strings.TrimSpace(externalIDs["tmdb"]); v != "" {
			add("tmdb:tv:" + v)
			add(v)
		}
	}
	if v := strings.TrimSpace(externalIDs["imdb"]); v != "" {
		add(v)
		add("imdb:" + v)
	}
	return candidates
}

func watchlistItemsEquivalentForMigration(mediaType, canonicalID string, externalIDs map[string]string, existing models.WatchlistItem) bool {
	if strings.ToLower(strings.TrimSpace(existing.MediaType)) != strings.ToLower(strings.TrimSpace(mediaType)) {
		return false
	}
	incomingTokens := watchlistIdentityTokensForMigration(mediaType, canonicalID, externalIDs)
	if len(incomingTokens) == 0 {
		return false
	}
	for token := range watchlistIdentityTokensForMigration(existing.MediaType, existing.ID, existing.ExternalIDs) {
		if incomingTokens[token] {
			return true
		}
	}
	return false
}

func watchlistIdentityTokensForMigration(mediaType, id string, externalIDs map[string]string) map[string]bool {
	tokens := make(map[string]bool, 8)
	add := func(kind, value string) {
		kind = strings.ToLower(strings.TrimSpace(kind))
		value = strings.ToLower(strings.TrimSpace(value))
		if kind == "" || value == "" {
			return
		}
		tokens[kind+":"+value] = true
	}

	add("id", id)
	for key, value := range normalizeWatchlistExternalIDsForMigration(externalIDs) {
		add(key, value)
	}
	canonicalID := canonicalWatchlistIDForMigration(mediaType, id, externalIDs)
	if canonicalID != "" {
		add("id", canonicalID)
	}
	return tokens
}

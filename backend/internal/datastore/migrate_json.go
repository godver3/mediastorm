package datastore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"novastream/models"
)

// jsonMigration defines a single JSON file → Postgres table migration.
type jsonMigration struct {
	name  string
	file  string
	check func(ctx context.Context) (int64, error)
	run   func(ctx context.Context, store *DataStore, filePath string) error
}

// MigrateFromJSON detects JSON files in cacheDir and imports them into Postgres.
// Each table migration is independent. Skips any table that already contains rows (idempotent).
func MigrateFromJSON(ctx context.Context, store *DataStore, cacheDir string) error {
	log := slog.With("component", "json-migration")

	migrations := []jsonMigration{
		{name: "accounts", file: "accounts.json", check: store.Accounts().Count, run: migrateAccounts},
		{name: "users", file: "users.json", check: store.Users().Count, run: migrateUsers},
		{name: "sessions", file: "sessions.json", check: store.Sessions().Count, run: migrateSessions},
		{name: "invitations", file: "invitations.json", check: store.Invitations().Count, run: migrateInvitations},
		{name: "clients", file: "clients.json", check: store.Clients().Count, run: migrateClients},
		{name: "client_settings", file: "client_settings.json", check: store.ClientSettings().Count, run: migrateClientSettings},
		{name: "user_settings", file: "user_settings.json", check: store.UserSettings().Count, run: migrateUserSettings},
		{name: "watchlist", file: "watchlist.json", check: store.Watchlist().Count, run: migrateWatchlist},
		{name: "custom_lists", file: "custom_lists.json", check: store.CustomLists().Count, run: migrateCustomLists},
		{name: "watch_history", file: "watched_items.json", check: store.WatchHistory().Count, run: migrateWatchHistory},
		{name: "playback_progress", file: "playback_progress.json", check: store.PlaybackProgress().Count, run: migratePlaybackProgress},
		{name: "content_preferences", file: "content_preferences.json", check: store.ContentPreferences().Count, run: migrateContentPreferences},
		{name: "prequeue", file: "prequeue.json", check: store.Prequeue().Count, run: migratePrequeue},
		{name: "prewarm", file: "prewarm.json", check: store.Prewarm().Count, run: migratePrewarm},
	}

	migrated := 0
	var errs []string
	for _, m := range migrations {
		filePath := filepath.Join(cacheDir, m.file)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			continue
		}
		count, err := m.check(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: check failed: %v", m.name, err))
			continue
		}
		if count > 0 {
			log.Info("skipping migration, table has data", "table", m.name, "rows", count)
			continue
		}
		log.Info("migrating json to postgres", "table", m.name, "file", m.file)
		if err := m.run(ctx, store, filePath); err != nil {
			log.Error("migration failed", "table", m.name, "err", err)
			errs = append(errs, fmt.Sprintf("%s: %v", m.name, err))
			continue // continue with remaining tables
		}
		// Rename source file so migration doesn't re-trigger
		if err := os.Rename(filePath, filePath+".migrated"); err != nil {
			log.Warn("could not rename migrated file", "file", filePath, "err", err)
		}
		log.Info("migration complete", "table", m.name)
		migrated++
	}

	if migrated > 0 {
		log.Info("json migration finished", "tables_migrated", migrated)
	}
	if len(errs) > 0 {
		return fmt.Errorf("migration errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// validUserIDs returns the set of user IDs currently in the database.
// Used to skip orphaned references during migration.
func validUserIDs(ctx context.Context, store *DataStore) (map[string]bool, error) {
	users, err := store.Users().List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(users))
	for _, u := range users {
		ids[u.ID] = true
	}
	return ids, nil
}

func readJSONFile(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// --- Per-table migration functions ---

func migrateAccounts(ctx context.Context, store *DataStore, filePath string) error {
	// accounts.json is a JSON array of AccountStorage
	var raw []models.AccountStorage
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read accounts.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for _, as := range raw {
			acct := as.ToAccount()
			if err := tx.Accounts().Create(ctx, &acct); err != nil {
				return fmt.Errorf("insert account %s: %w", acct.ID, err)
			}
		}
		return nil
	})
}

func migrateUsers(ctx context.Context, store *DataStore, filePath string) error {
	// users.json is a JSON array of User
	var raw []models.User
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read users.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for _, u := range raw {
			user := u
			if err := tx.Users().Create(ctx, &user); err != nil {
				return fmt.Errorf("insert user %s: %w", u.ID, err)
			}
		}
		return nil
	})
}

func migrateSessions(ctx context.Context, store *DataStore, filePath string) error {
	// sessions.json is a JSON array of Session
	var raw []models.Session
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read sessions.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for _, s := range raw {
			sess := s
			if err := tx.Sessions().Create(ctx, &sess); err != nil {
				return fmt.Errorf("insert session: %w", err)
			}
		}
		return nil
	})
}

func migrateInvitations(ctx context.Context, store *DataStore, filePath string) error {
	// invitations.json is a JSON array of Invitation
	var raw []models.Invitation
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read invitations.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for _, inv := range raw {
			invitation := inv
			if err := tx.Invitations().Create(ctx, &invitation); err != nil {
				return fmt.Errorf("insert invitation %s: %w", inv.ID, err)
			}
		}
		return nil
	})
}

func migrateClients(ctx context.Context, store *DataStore, filePath string) error {
	log := slog.With("component", "json-migration", "table", "clients")
	var raw map[string]models.Client
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read clients.json: %w", err)
	}
	validUsers, err := validUserIDs(ctx, store)
	if err != nil {
		return fmt.Errorf("list users for client validation: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for id, c := range raw {
			c.ID = id
			if !validUsers[c.UserID] {
				log.Warn("skipping orphaned reference", "client", id, "userId", c.UserID)
				continue
			}
			if err := tx.Clients().Create(ctx, &c); err != nil {
				return fmt.Errorf("insert client %s: %w", id, err)
			}
		}
		return nil
	})
}

func migrateClientSettings(ctx context.Context, store *DataStore, filePath string) error {
	var raw map[string]models.ClientFilterSettings
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read client_settings.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for clientID, settings := range raw {
			if err := tx.ClientSettings().Upsert(ctx, clientID, &settings); err != nil {
				return fmt.Errorf("insert client settings %s: %w", clientID, err)
			}
		}
		return nil
	})
}

func migrateUserSettings(ctx context.Context, store *DataStore, filePath string) error {
	log := slog.With("component", "json-migration", "table", "user_settings")
	var raw map[string]models.UserSettings
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read user_settings.json: %w", err)
	}
	validUsers, err := validUserIDs(ctx, store)
	if err != nil {
		return fmt.Errorf("list users for validation: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for userID, settings := range raw {
			if !validUsers[userID] {
				log.Warn("skipping orphaned reference", "userId", userID)
				continue
			}
			if err := tx.UserSettings().Upsert(ctx, userID, &settings); err != nil {
				return fmt.Errorf("insert user settings %s: %w", userID, err)
			}
		}
		return nil
	})
}

func migrateWatchlist(ctx context.Context, store *DataStore, filePath string) error {
	log := slog.With("component", "json-migration", "table", "watchlist")
	var raw map[string][]models.WatchlistItem
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read watchlist.json: %w", err)
	}
	validUsers, err := validUserIDs(ctx, store)
	if err != nil {
		return fmt.Errorf("list users for validation: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for userID, items := range raw {
			if !validUsers[userID] {
				log.Warn("skipping orphaned reference", "userId", userID)
				continue
			}
			if err := tx.Watchlist().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("insert watchlist for user %s: %w", userID, err)
			}
		}
		return nil
	})
}

// customListsPersisted matches the JSON structure in custom_lists.json
type customListsPersisted struct {
	Lists []models.CustomList              `json:"lists"`
	Items map[string][]models.WatchlistItem `json:"items,omitempty"`
}

func migrateCustomLists(ctx context.Context, store *DataStore, filePath string) error {
	// custom_lists.json is map[userID]customListsPersisted
	var raw map[string]customListsPersisted
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read custom_lists.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for userID, data := range raw {
			for _, list := range data.Lists {
				if err := tx.CustomLists().CreateList(ctx, userID, &list); err != nil {
					return fmt.Errorf("insert custom list %s: %w", list.ID, err)
				}
				if items, ok := data.Items[list.ID]; ok {
					for _, item := range items {
						if err := tx.CustomLists().UpsertItem(ctx, list.ID, &item); err != nil {
							return fmt.Errorf("insert custom list item: %w", err)
						}
					}
				}
			}
		}
		return nil
	})
}

func migrateWatchHistory(ctx context.Context, store *DataStore, filePath string) error {
	log := slog.With("component", "json-migration", "table", "watch_history")
	// watched_items.json is map[userID][]WatchHistoryItem
	var raw map[string][]models.WatchHistoryItem
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read watched_items.json: %w", err)
	}
	validUsers, err := validUserIDs(ctx, store)
	if err != nil {
		return fmt.Errorf("list users for validation: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for userID, items := range raw {
			if !validUsers[userID] {
				log.Warn("skipping orphaned reference", "userId", userID)
				continue
			}
			if err := tx.WatchHistory().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("insert watch history for user %s: %w", userID, err)
			}
		}
		return nil
	})
}

func migratePlaybackProgress(ctx context.Context, store *DataStore, filePath string) error {
	log := slog.With("component", "json-migration", "table", "playback_progress")
	// playback_progress.json is map[userID][]PlaybackProgress
	var raw map[string][]models.PlaybackProgress
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read playback_progress.json: %w", err)
	}
	validUsers, err := validUserIDs(ctx, store)
	if err != nil {
		return fmt.Errorf("list users for validation: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for userID, items := range raw {
			if !validUsers[userID] {
				log.Warn("skipping orphaned reference", "userId", userID)
				continue
			}
			if err := tx.PlaybackProgress().BulkUpsert(ctx, userID, items); err != nil {
				return fmt.Errorf("insert playback progress for user %s: %w", userID, err)
			}
		}
		return nil
	})
}

func migrateContentPreferences(ctx context.Context, store *DataStore, filePath string) error {
	// content_preferences.json is map[userID][]ContentPreference
	var raw map[string][]models.ContentPreference
	if err := readJSONFile(filePath, &raw); err != nil {
		return fmt.Errorf("read content_preferences.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for userID, prefs := range raw {
			for _, pref := range prefs {
				if err := tx.ContentPreferences().Upsert(ctx, userID, &pref); err != nil {
					return fmt.Errorf("insert content preference: %w", err)
				}
			}
		}
		return nil
	})
}

// prequeuePersisted matches the JSON array element shape in prequeue.json
type prequeuePersisted struct {
	ID        string    `json:"id"`
	TitleID   string    `json:"titleId"`
	UserID    string    `json:"userId"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func migratePrequeue(ctx context.Context, store *DataStore, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read prequeue.json: %w", err)
	}
	// Parse just enough to extract index fields
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal prequeue.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for _, raw := range entries {
			var meta prequeuePersisted
			if err := json.Unmarshal(raw, &meta); err != nil {
				return fmt.Errorf("unmarshal prequeue entry: %w", err)
			}
			if err := tx.Prequeue().Upsert(ctx, meta.ID, meta.TitleID, meta.UserID, meta.Status, raw, meta.ExpiresAt); err != nil {
				return fmt.Errorf("insert prequeue %s: %w", meta.ID, err)
			}
		}
		return nil
	})
}

// prewarmPersisted matches the WarmEntry shape in prewarm.json
type prewarmPersisted struct {
	TitleID   string    `json:"titleId"`
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func migratePrewarm(ctx context.Context, store *DataStore, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read prewarm.json: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal prewarm.json: %w", err)
	}
	return store.WithTx(ctx, func(tx *Tx) error {
		for i, raw := range entries {
			var meta prewarmPersisted
			if err := json.Unmarshal(raw, &meta); err != nil {
				return fmt.Errorf("unmarshal prewarm entry: %w", err)
			}
			// Prewarm key is titleID:userID
			id := meta.TitleID + ":" + meta.UserID
			if err := tx.Prewarm().Upsert(ctx, id, meta.TitleID, meta.UserID, raw, meta.ExpiresAt); err != nil {
				return fmt.Errorf("insert prewarm %d: %w", i, err)
			}
		}
		return nil
	})
}

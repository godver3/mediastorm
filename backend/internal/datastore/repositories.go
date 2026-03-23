package datastore

import (
	"context"

	"novastream/models"
)

// AccountRepository manages account persistence.
type AccountRepository interface {
	Get(ctx context.Context, id string) (*models.Account, error)
	GetByUsername(ctx context.Context, username string) (*models.Account, error)
	List(ctx context.Context) ([]models.Account, error)
	Create(ctx context.Context, acct *models.Account) error
	Update(ctx context.Context, acct *models.Account) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// UserRepository manages user profile persistence.
type UserRepository interface {
	Get(ctx context.Context, id string) (*models.User, error)
	ListByAccount(ctx context.Context, accountID string) ([]models.User, error)
	List(ctx context.Context) ([]models.User, error)
	Create(ctx context.Context, user *models.User) error
	Update(ctx context.Context, user *models.User) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// SessionRepository manages session persistence.
type SessionRepository interface {
	Get(ctx context.Context, token string) (*models.Session, error)
	List(ctx context.Context) ([]models.Session, error)
	ListByAccount(ctx context.Context, accountID string) ([]models.Session, error)
	Create(ctx context.Context, sess *models.Session) error
	Delete(ctx context.Context, token string) error
	DeleteByAccount(ctx context.Context, accountID string) error
	DeleteExpired(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// InvitationRepository manages invitation persistence.
type InvitationRepository interface {
	Get(ctx context.Context, id string) (*models.Invitation, error)
	GetByToken(ctx context.Context, token string) (*models.Invitation, error)
	List(ctx context.Context) ([]models.Invitation, error)
	Create(ctx context.Context, inv *models.Invitation) error
	Update(ctx context.Context, inv *models.Invitation) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// ClientRepository manages client/device persistence.
type ClientRepository interface {
	Get(ctx context.Context, id string) (*models.Client, error)
	ListByUser(ctx context.Context, userID string) ([]models.Client, error)
	List(ctx context.Context) ([]models.Client, error)
	Create(ctx context.Context, client *models.Client) error
	Update(ctx context.Context, client *models.Client) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int64, error)
}

// ClientSettingsRepository manages per-client filter settings.
type ClientSettingsRepository interface {
	Get(ctx context.Context, clientID string) (*models.ClientFilterSettings, error)
	Upsert(ctx context.Context, clientID string, settings *models.ClientFilterSettings) error
	Delete(ctx context.Context, clientID string) error
	List(ctx context.Context) (map[string]models.ClientFilterSettings, error)
	Count(ctx context.Context) (int64, error)
}

// UserSettingsRepository manages per-user settings.
type UserSettingsRepository interface {
	Get(ctx context.Context, userID string) (*models.UserSettings, error)
	Upsert(ctx context.Context, userID string, settings *models.UserSettings) error
	Delete(ctx context.Context, userID string) error
	List(ctx context.Context) (map[string]models.UserSettings, error)
	Count(ctx context.Context) (int64, error)
}

// WatchlistRepository manages watchlist items per user.
type WatchlistRepository interface {
	Get(ctx context.Context, userID, itemKey string) (*models.WatchlistItem, error)
	ListByUser(ctx context.Context, userID string) ([]models.WatchlistItem, error)
	ListAll(ctx context.Context) (map[string][]models.WatchlistItem, error)
	Upsert(ctx context.Context, userID string, item *models.WatchlistItem) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	DeleteBySyncSource(ctx context.Context, userID, syncSource string) error
	BulkUpsert(ctx context.Context, userID string, items []models.WatchlistItem) error
	Count(ctx context.Context) (int64, error)
}

// CustomListRepository manages user-created lists and their items.
type CustomListRepository interface {
	GetList(ctx context.Context, listID string) (*models.CustomList, error)
	ListByUser(ctx context.Context, userID string) ([]models.CustomList, error)
	CreateList(ctx context.Context, userID string, list *models.CustomList) error
	UpdateList(ctx context.Context, list *models.CustomList) error
	DeleteList(ctx context.Context, listID string) error

	GetItems(ctx context.Context, listID string) ([]models.WatchlistItem, error)
	UpsertItem(ctx context.Context, listID string, item *models.WatchlistItem) error
	DeleteItem(ctx context.Context, listID, itemKey string) error

	ListUserIDs(ctx context.Context) ([]string, error)
	Count(ctx context.Context) (int64, error)
}

// WatchHistoryRepository manages watch history.
type WatchHistoryRepository interface {
	Get(ctx context.Context, userID, itemKey string) (*models.WatchHistoryItem, error)
	ListByUser(ctx context.Context, userID string) ([]models.WatchHistoryItem, error)
	ListAll(ctx context.Context) (map[string][]models.WatchHistoryItem, error)
	Upsert(ctx context.Context, userID string, item *models.WatchHistoryItem) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	BulkUpsert(ctx context.Context, userID string, items []models.WatchHistoryItem) error
	Count(ctx context.Context) (int64, error)
}

// PlaybackProgressRepository manages playback resume positions.
type PlaybackProgressRepository interface {
	Get(ctx context.Context, userID, itemKey string) (*models.PlaybackProgress, error)
	ListByUser(ctx context.Context, userID string) ([]models.PlaybackProgress, error)
	ListAll(ctx context.Context) (map[string][]models.PlaybackProgress, error)
	Upsert(ctx context.Context, userID string, progress *models.PlaybackProgress) error
	Delete(ctx context.Context, userID, itemKey string) error
	DeleteByUser(ctx context.Context, userID string) error
	SetHidden(ctx context.Context, userID, itemKey string, hidden bool) error
	BulkUpsert(ctx context.Context, userID string, items []models.PlaybackProgress) error
	Count(ctx context.Context) (int64, error)
}

// ContentPreferencesRepository manages per-content audio/subtitle preferences.
type ContentPreferencesRepository interface {
	Get(ctx context.Context, userID, contentID string) (*models.ContentPreference, error)
	ListByUser(ctx context.Context, userID string) ([]models.ContentPreference, error)
	Upsert(ctx context.Context, userID string, pref *models.ContentPreference) error
	Delete(ctx context.Context, userID, contentID string) error
	Count(ctx context.Context) (int64, error)
}

// PrequeueRepository manages prequeue entries.
// Entries are stored as JSONB blobs due to their complex nested structure.
type PrequeueRepository interface {
	Get(ctx context.Context, id string) ([]byte, error) // returns raw JSON
	GetByTitleUser(ctx context.Context, titleID, userID string) ([]byte, error)
	List(ctx context.Context) ([][]byte, error) // returns all entries as raw JSON
	Upsert(ctx context.Context, id, titleID, userID, status string, data []byte, expiresAt interface{}) error
	Delete(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// PrewarmRepository manages prewarm entries.
// Entries are stored as JSONB blobs due to their complex nested structure.
type PrewarmRepository interface {
	Get(ctx context.Context, id string) ([]byte, error) // returns raw JSON
	List(ctx context.Context) ([][]byte, error)         // returns all entries as raw JSON
	Upsert(ctx context.Context, id, titleID, userID string, data []byte, expiresAt interface{}) error
	Delete(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context) (int64, error)
	Count(ctx context.Context) (int64, error)
}

// ImportQueueRepository manages the import queue (migrated from queue.db).
type ImportQueueRepository interface {
	Count(ctx context.Context) (int64, error)
}

// FileHealthRepository manages file health tracking.
type FileHealthRepository interface {
	Count(ctx context.Context) (int64, error)
}

// MediaFileRepository manages media file tracking.
type MediaFileRepository interface {
	Count(ctx context.Context) (int64, error)
}

type LocalMediaRepository interface {
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
	GetLibrary(ctx context.Context, id string) (*models.LocalMediaLibrary, error)
	CreateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error
	UpdateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error
	DeleteLibrary(ctx context.Context, id string) error
	ListItemsByLibrary(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaItemListResult, error)
	ListAllItemsByLibrary(ctx context.Context, libraryID string) ([]models.LocalMediaItem, error)
	UpsertItem(ctx context.Context, item *models.LocalMediaItem) error
	GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error)
	MarkItemsMissingNotSeenInScan(ctx context.Context, libraryID, scanID string, missingSince interface{}) error
	DeleteItem(ctx context.Context, id string) error
}

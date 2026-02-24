package models

import "time"

// WatchlistItem represents a media entry saved by the user for quick access.
type WatchlistItem struct {
	ID          string            `json:"id"`
	MediaType   string            `json:"mediaType"` // movie | series
	Name        string            `json:"name"`
	Overview    string            `json:"overview,omitempty"`
	Year        int               `json:"year,omitempty"`
	PosterURL   string            `json:"posterUrl,omitempty"`
	BackdropURL string            `json:"backdropUrl,omitempty"`
	AddedAt     time.Time         `json:"addedAt"`
	ExternalIDs    map[string]string `json:"externalIds,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	RuntimeMinutes int               `json:"runtimeMinutes,omitempty"`
	SyncSource     string            `json:"syncSource,omitempty"` // e.g., "plex:<accountId>:<taskId>" for synced items
	SyncedAt       *time.Time        `json:"syncedAt,omitempty"`   // when last synced from external source
	WatchState     string            `json:"watchState,omitempty"`     // "none" | "partial" | "complete"
	UnwatchedCount *int              `json:"unwatchedCount,omitempty"` // series only: total - watched
}

// WatchlistUpsert captures data required to insert or update a watchlist item.
type WatchlistUpsert struct {
	ID          string            `json:"id"`
	MediaType   string            `json:"mediaType"`
	Name        string            `json:"name"`
	Overview    string            `json:"overview,omitempty"`
	Year        int               `json:"year,omitempty"`
	PosterURL   string            `json:"posterUrl,omitempty"`
	BackdropURL string            `json:"backdropUrl,omitempty"`
	ExternalIDs    map[string]string `json:"externalIds,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	RuntimeMinutes int               `json:"runtimeMinutes,omitempty"`
	SyncSource     string            `json:"syncSource,omitempty"` // sync source identifier for tracking origin
	SyncedAt       *time.Time        `json:"syncedAt,omitempty"`   // sync timestamp
}

// Key returns a stable identifier for the watchlist item combining media type and ID.
func (w WatchlistUpsert) Key() string {
	return w.MediaType + ":" + w.ID
}

// Key returns a stable identifier for the watchlist item combining media type and ID.
func (w WatchlistItem) Key() string {
	return w.MediaType + ":" + w.ID
}

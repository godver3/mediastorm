package models

import (
	"encoding/json"
	"time"
)

const (
	// DefaultUserID represents the legacy single-user watchlist owner.
	DefaultUserID = "default"
	// DefaultUserName is used when creating the initial profile.
	DefaultUserName = "Primary Profile"
)

// User models a NovaStream profile capable of holding watchlist data.
type User struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"accountId"`                // ID of the owning account
	Name           string    `json:"name"`
	Color          string    `json:"color,omitempty"`
	IconURL        string    `json:"iconUrl,omitempty"`        // Local path to downloaded profile icon image (set via admin UI)
	PinHash        string    `json:"-"`                        // bcrypt hash of PIN, excluded from JSON (security)
	TraktAccountID string    `json:"traktAccountId,omitempty"` // ID of the linked Trakt account (from config.TraktAccount)
	PlexAccountID  string    `json:"plexAccountId,omitempty"`  // ID of the linked Plex account (from config.PlexAccount)
	IsKidsProfile  bool      `json:"isKidsProfile"`            // Whether this is a kids profile with content restrictions
	// Kids profile content restriction settings
	KidsMode           string   `json:"kidsMode,omitempty"`           // "rating", "content_list", "both", or "" (disabled)
	KidsMaxRating      string   `json:"kidsMaxRating,omitempty"`      // Deprecated: use KidsMaxMovieRating/KidsMaxTVRating instead
	KidsMaxMovieRating string   `json:"kidsMaxMovieRating,omitempty"` // Max allowed movie rating: "G", "PG", "PG-13", "R", "NC-17"
	KidsMaxTVRating    string   `json:"kidsMaxTVRating,omitempty"`    // Max allowed TV rating: "TV-Y", "TV-Y7", "TV-G", "TV-PG", "TV-14", "TV-MA"
	KidsAllowedLists   []string `json:"kidsAllowedLists,omitempty"`   // MDBList URLs allowed for content_list mode
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// HasPin returns true if the user has a PIN set.
func (u User) HasPin() bool {
	return u.PinHash != ""
}

// HasIcon returns true if the user has a custom icon set.
func (u User) HasIcon() bool {
	return u.IconURL != ""
}

// MarshalJSON implements custom JSON marshaling to include the computed hasPin field.
func (u User) MarshalJSON() ([]byte, error) {
	type UserAlias User // prevent recursion
	return json.Marshal(&struct {
		UserAlias
		HasPin         bool   `json:"hasPin"`
		HasIcon        bool   `json:"hasIcon"`
		TraktAccountID string `json:"traktAccountId,omitempty"`
		PlexAccountID  string `json:"plexAccountId,omitempty"`
	}{
		UserAlias:      UserAlias(u),
		HasPin:         u.HasPin(),
		HasIcon:        u.HasIcon(),
		TraktAccountID: u.TraktAccountID,
		PlexAccountID:  u.PlexAccountID,
	})
}

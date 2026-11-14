package models

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// DefaultUserID represents the legacy single-user watchlist owner.
	DefaultUserID = "default"
	// DefaultUserName is used when creating the initial profile.
	DefaultUserName = "Primary Profile"
	// ActivityPrivacyNotShared keeps a profile's activity out of shared shelves.
	ActivityPrivacyNotShared = "not_shared"
	// ActivityPrivacySharedAnonymous includes activity without identifying the profile.
	ActivityPrivacySharedAnonymous = "shared_anonymous"
	// ActivityPrivacyShared includes activity with the profile's display name.
	ActivityPrivacyShared = "shared"
)

// User models a NovaStream profile capable of holding watchlist data.
type User struct {
	ID               string `json:"id"`
	AccountID        string `json:"accountId"` // ID of the owning account
	Name             string `json:"name"`
	Color            string `json:"color,omitempty"`
	IconURL          string `json:"iconUrl,omitempty"`          // Local path to downloaded profile icon image (set via admin UI)
	PinHash          string `json:"pinHash,omitempty"`          // bcrypt hash of PIN — persisted to disk, stripped from API responses by MarshalJSON
	PinLength        int    `json:"pinLength,omitempty"`        // Length of the PIN, exposed so clients can render/submit fixed-length entry
	TraktAccountID   string `json:"traktAccountId,omitempty"`   // ID of the linked Trakt account (from config.TraktAccount)
	PlexAccountID    string `json:"plexAccountId,omitempty"`    // ID of the linked Plex account (from config.PlexAccount)
	MdblistAccountID string `json:"mdblistAccountId,omitempty"` // ID of the linked MDBList account (from config.MDBListAccount)
	SimklAccountID   string `json:"simklAccountId,omitempty"`   // ID of the linked Simkl account (from config.SimklAccount)
	ScrobAccountID   string `json:"scrobAccountId,omitempty"`   // ID of the linked self-hosted Scrob account
	IsKidsProfile    bool   `json:"isKidsProfile"`              // Whether this is a kids profile with content restrictions
	AllowShareLinks  bool   `json:"allowShareLinks"`            // Whether this profile may mint shareable playback links (master-controlled, default off)
	// ActivityPrivacy controls whether this profile's watch activity appears in
	// server-wide shelves like "Popular on This Server", "Recently Watched",
	// and the active "Dashboard" shelf.
	// New profiles share anonymously by default. Empty and unknown legacy values
	// remain not_shared so existing profiles are never silently opted in.
	ActivityPrivacy string `json:"activityPrivacy"`
	// Kids profile content restriction settings
	KidsLiveMaxAge     *int      `json:"kidsLiveMaxAge,omitempty"`     // Numeric minimum-age ceiling for rated Live TV; nil blocks Live TV for kids
	KidsMode           string    `json:"kidsMode,omitempty"`           // "rating", "content_list", or "" (disabled)
	KidsMaxRating      string    `json:"kidsMaxRating,omitempty"`      // Deprecated: use KidsMaxMovieRating/KidsMaxTVRating instead
	KidsMaxMovieRating string    `json:"kidsMaxMovieRating,omitempty"` // Max allowed movie rating: "G", "PG", "PG-13", "R", "NC-17"
	KidsMaxTVRating    string    `json:"kidsMaxTVRating,omitempty"`    // Max allowed TV rating: "TV-Y", "TV-Y7", "TV-G", "TV-PG", "TV-14", "TV-MA"
	KidsAllowedLists   []string  `json:"kidsAllowedLists,omitempty"`   // MDBList URLs allowed for content_list mode
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// NormalizeActivityPrivacy returns a safe, canonical privacy value.
func NormalizeActivityPrivacy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ActivityPrivacyShared:
		return ActivityPrivacyShared
	case ActivityPrivacySharedAnonymous:
		return ActivityPrivacySharedAnonymous
	default:
		return ActivityPrivacyNotShared
	}
}

// SharesActivity reports whether this profile explicitly opted into shared shelves.
func (u User) SharesActivity() bool {
	privacy := NormalizeActivityPrivacy(u.ActivityPrivacy)
	return privacy == ActivityPrivacyShared || privacy == ActivityPrivacySharedAnonymous
}

// HasPin returns true if the user has a PIN set.
func (u User) HasPin() bool {
	return u.PinHash != ""
}

// HasIcon returns true if the user has a custom icon set.
func (u User) HasIcon() bool {
	return u.IconURL != ""
}

// MarshalJSON implements custom JSON marshaling to include the computed hasPin field
// and strip the pinHash so it is never exposed in API responses.
func (u User) MarshalJSON() ([]byte, error) {
	type UserAlias User // prevent recursion
	return json.Marshal(&struct {
		UserAlias
		PinHash          *struct{} `json:"pinHash,omitempty"` // shadow to exclude from output (nil + omitempty = dropped)
		HasPin           bool      `json:"hasPin"`
		HasIcon          bool      `json:"hasIcon"`
		TraktAccountID   string    `json:"traktAccountId,omitempty"`
		PlexAccountID    string    `json:"plexAccountId,omitempty"`
		MdblistAccountID string    `json:"mdblistAccountId,omitempty"`
		SimklAccountID   string    `json:"simklAccountId,omitempty"`
		ScrobAccountID   string    `json:"scrobAccountId,omitempty"`
		ActivityPrivacy  string    `json:"activityPrivacy"`
	}{
		UserAlias:        UserAlias(u),
		PinHash:          nil,
		HasPin:           u.HasPin(),
		HasIcon:          u.HasIcon(),
		TraktAccountID:   u.TraktAccountID,
		PlexAccountID:    u.PlexAccountID,
		MdblistAccountID: u.MdblistAccountID,
		SimklAccountID:   u.SimklAccountID,
		ScrobAccountID:   u.ScrobAccountID,
		ActivityPrivacy:  NormalizeActivityPrivacy(u.ActivityPrivacy),
	})
}

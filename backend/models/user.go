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
	Name           string    `json:"name"`
	Color          string    `json:"color,omitempty"`
	PinHash        string    `json:"-"`                        // bcrypt hash of PIN, excluded from JSON (security)
	TraktAccountID string    `json:"traktAccountId,omitempty"` // ID of the linked Trakt account (from config.TraktAccount)
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// HasPin returns true if the user has a PIN set.
func (u User) HasPin() bool {
	return u.PinHash != ""
}

// MarshalJSON implements custom JSON marshaling to include the computed hasPin field.
func (u User) MarshalJSON() ([]byte, error) {
	type UserAlias User // prevent recursion
	return json.Marshal(&struct {
		UserAlias
		HasPin         bool   `json:"hasPin"`
		TraktAccountID string `json:"traktAccountId,omitempty"`
	}{
		UserAlias:      UserAlias(u),
		HasPin:         u.HasPin(),
		TraktAccountID: u.TraktAccountID,
	})
}

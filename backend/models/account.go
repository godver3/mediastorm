package models

import (
	"encoding/json"
	"time"
)

const (
	// DefaultAccountID is the account ID assigned to migrated/legacy profiles.
	DefaultAccountID = "default"
	// MasterAccountUsername is the default username for the master account.
	MasterAccountUsername = "admin"
)

// Account represents a user account that can own multiple profiles.
// Master accounts can manage all profiles and other accounts.
// Regular accounts can only see and manage their own profiles.
type Account struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"` // bcrypt hash, excluded from JSON API responses (security)
	IsMaster     bool      `json:"isMaster"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// MarshalJSON implements custom JSON marshaling to ensure password hash is never exposed in API responses.
func (a Account) MarshalJSON() ([]byte, error) {
	type AccountAlias Account // prevent recursion
	return json.Marshal(&struct {
		AccountAlias
	}{
		AccountAlias: AccountAlias(a),
	})
}

// AccountStorage is the internal representation used for file persistence.
// Unlike Account, this includes the password hash for storage.
type AccountStorage struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"` // Included for storage only
	IsMaster     bool      `json:"isMaster"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ToStorage converts an Account to AccountStorage for persistence.
func (a Account) ToStorage() AccountStorage {
	return AccountStorage{
		ID:           a.ID,
		Username:     a.Username,
		PasswordHash: a.PasswordHash,
		IsMaster:     a.IsMaster,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// ToAccount converts an AccountStorage back to Account.
func (as AccountStorage) ToAccount() Account {
	return Account{
		ID:           as.ID,
		Username:     as.Username,
		PasswordHash: as.PasswordHash,
		IsMaster:     as.IsMaster,
		CreatedAt:    as.CreatedAt,
		UpdatedAt:    as.UpdatedAt,
	}
}

package models

import "time"

// Invitation represents a one-time use invitation link for account creation.
type Invitation struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	CreatedBy string    `json:"createdBy"` // Account ID of the creator
	ExpiresAt time.Time `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	UsedBy    string    `json:"usedBy,omitempty"` // Account ID of the user who used it
	CreatedAt time.Time `json:"createdAt"`
}

// IsValid checks if the invitation is still valid (not expired and not used).
func (i *Invitation) IsValid() bool {
	return i.UsedAt == nil && time.Now().Before(i.ExpiresAt)
}

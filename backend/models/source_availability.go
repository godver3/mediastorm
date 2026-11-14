package models

import "time"

// SourceAvailability describes a filtered search in explicitly selected
// indexers. A found release is a candidate, not proof of playable media.
type SourceAvailability struct {
	SourceType string     `json:"sourceType,omitempty"` // local only after file/track checks
	State      string     `json:"state"`                // unknown, found, not_found
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	Providers  []string   `json:"providers,omitempty"`
	Complete   bool       `json:"complete"` // All selected, authorized providers completed
	Reason     string     `json:"reason,omitempty"`
}

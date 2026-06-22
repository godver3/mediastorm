package models

import "time"

// Session represents an authenticated session for an account.
type Session struct {
	Token     string    `json:"token"`
	AccountID string    `json:"accountId"`
	IsMaster  bool      `json:"isMaster"` // Cached from account for quick access
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	UserAgent string    `json:"userAgent,omitempty"`
	IPAddress string    `json:"ipAddress,omitempty"`
	// Scope restricts what the session may access. Empty means full account
	// access (normal login). "stream" is a one-time share session limited to
	// streaming/playback endpoints only.
	Scope string `json:"scope,omitempty"`
}

// SessionScopeStream restricts a session to streaming/playback endpoints only.
// Used by one-time shareable playback links.
const SessionScopeStream = "stream"

// IsExpired returns true if the session has expired.
func (s Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

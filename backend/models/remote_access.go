package models

import "time"

type RemoteAccessStatus struct {
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	Provider      string `json:"provider"`
	State         string `json:"state"`
	LastError     string `json:"lastError,omitempty"`
	ActiveHosts   int    `json:"activeHosts,omitempty"`
	ActiveInvites int    `json:"activeInvites,omitempty"`
}

type RemoteAccessInvite struct {
	ID             string     `json:"id"`
	Token          string     `json:"token,omitempty"`
	TokenHash      string     `json:"-"`
	ConnectionCode string     `json:"connectionCode,omitempty"`
	IrohInvite     string     `json:"irohInvite,omitempty"`
	CreatedBy      string     `json:"createdBy"`
	PeerName       string     `json:"peerName"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	UsedAt         *time.Time `json:"usedAt,omitempty"`
	UsedByPeerID   string     `json:"usedByPeerId,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func (i *RemoteAccessInvite) IsActive(now time.Time) bool {
	if i.RevokedAt != nil {
		return false
	}
	return i.UsedAt != nil || now.Before(i.ExpiresAt)
}

// IsPendingClaim reports whether the invite is still awaiting its first claim and so
// needs its connection code published to the rendezvous DHT. Once claimed, paired clients
// reconnect via the host's stable iroh NodeID (n0 discovery), so the code-derived record
// is dropped to remove the offline brute-force oracle on the low-entropy code.
func (i *RemoteAccessInvite) IsPendingClaim(now time.Time) bool {
	if i.RevokedAt != nil || i.UsedAt != nil {
		return false
	}
	return now.Before(i.ExpiresAt)
}

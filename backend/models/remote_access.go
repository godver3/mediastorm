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

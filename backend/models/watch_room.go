package models

import (
	"strconv"
	"time"
)

const (
	WatchRoomStatusLobby   = "lobby"
	WatchRoomStatusPlaying = "playing"
	WatchRoomStatusPaused  = "paused"
	WatchRoomStatusEnded   = "ended"
)

// WatchRoom is a persistent, profile-scoped invitation and synchronized
// playback session. Params contains the native details-route parameters needed
// for every participant to resolve their own stream.
type WatchRoom struct {
	ID               string            `json:"id"`
	CreatorProfileID string            `json:"creatorProfileId"`
	CreatorName      string            `json:"creatorName"`
	Title            string            `json:"title"`
	MediaType        string            `json:"mediaType"`
	ItemID           string            `json:"itemId"`
	PosterURL        string            `json:"posterUrl,omitempty"`
	BackdropURL      string            `json:"backdropUrl,omitempty"`
	Params           map[string]string `json:"params"`
	Episode          *WatchRoomEpisode `json:"episode,omitempty"`
	Status           string            `json:"status"`
	WaitingForReady  bool              `json:"waitingForReady"`
	Position         float64           `json:"position"`
	Duration         float64           `json:"duration"`
	Revision         int64             `json:"revision"`
	UpdatedBy        string            `json:"updatedBy,omitempty"`
	AnchorUpdatedAt  time.Time         `json:"anchorUpdatedAt"`
	CreatedAt        time.Time         `json:"createdAt"`
	ExpiresAt        time.Time         `json:"expiresAt"`
	EndedAt          *time.Time        `json:"endedAt,omitempty"`
	EndReason        string            `json:"endReason,omitempty"`
	Members          []WatchRoomMember `json:"members"`
}

const (
	WatchRoomAccountInvitePending  = "pending"
	WatchRoomAccountInviteAccepted = "accepted"
	WatchRoomAccountInviteDeclined = "declined"
	WatchRoomAccountInviteRevoked  = "revoked"
)

// WatchRoomAccountInvite addresses another account without exposing its
// profiles. The recipient chooses a profile when accepting the invitation.
type WatchRoomAccountInvite struct {
	ID                string     `json:"id"`
	RoomID            string     `json:"roomId"`
	InviterAccountID  string     `json:"-"`
	InviteeAccountID  string     `json:"-"`
	InviteeUsername   string     `json:"inviteeUsername,omitempty"`
	AcceptedProfileID string     `json:"acceptedProfileId,omitempty"`
	Status            string     `json:"status"`
	CreatorName       string     `json:"creatorName,omitempty"`
	Title             string     `json:"title,omitempty"`
	MediaType         string     `json:"mediaType,omitempty"`
	ItemID            string     `json:"itemId,omitempty"`
	PosterURL         string     `json:"posterUrl,omitempty"`
	BackdropURL       string     `json:"backdropUrl,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	RespondedAt       *time.Time `json:"respondedAt,omitempty"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
}

type WatchRoomClientCapabilities struct {
	NativePlayback  bool `json:"nativePlayback"`
	StateSync       bool `json:"stateSync"`
	ProtocolVersion int  `json:"protocolVersion"`
}

type WatchRoomMember struct {
	ProfileID    string                      `json:"profileId"`
	Name         string                      `json:"name"`
	IsGuest      bool                        `json:"isGuest,omitempty"`
	Color        string                      `json:"color,omitempty"`
	IconURL      string                      `json:"iconUrl,omitempty"`
	ClientID     string                      `json:"clientId,omitempty"`
	IsCreator    bool                        `json:"isCreator"`
	Ready        bool                        `json:"ready"`
	Buffering    bool                        `json:"buffering"`
	Joined       bool                        `json:"joined"`
	Present      bool                        `json:"present"`
	JoinedAt     time.Time                   `json:"joinedAt"`
	LastSeenAt   time.Time                   `json:"lastSeenAt"`
	Capabilities WatchRoomClientCapabilities `json:"capabilities"`
}

// WatchRoomExternalInvite is a bearer invitation for a guest who does not have
// an account on this server. Only TokenHash is persisted; Token is returned once
// to the room creator so it can be encoded in a QR code.
type WatchRoomExternalInvite struct {
	RoomID    string    `json:"roomId"`
	AccountID string    `json:"-"`
	Token     string    `json:"token,omitempty"`
	TokenHash string    `json:"-"`
	ShortCode string    `json:"shortCode"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// WatchRoomExternalSource is the immutable source selected by the native host
// for external guests. Params contains only web-player-safe display/playback
// options; Resource is never returned to a guest browser.
type WatchRoomExternalSource struct {
	RoomID   string            `json:"roomId"`
	Resource string            `json:"-"`
	Params   map[string]string `json:"-"`
	// EpisodeKey is WatchRoomEpisode.Key() of the episode this source plays.
	EpisodeKey string    `json:"-"`
	BoundAt    time.Time `json:"boundAt"`
}

// WatchRoomEpisode pins a series room to one episode. Only the room creator
// may change it; every member plays this episode rather than their own next-up.
type WatchRoomEpisode struct {
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	EpisodeID     string `json:"episodeId,omitempty"`
	Title         string `json:"title,omitempty"`
}

// Key identifies the episode by season/episode number; metadata such as the
// title does not make it a different episode. A nil episode has key "".
func (e *WatchRoomEpisode) Key() string {
	if e == nil {
		return ""
	}
	return "s" + strconv.Itoa(e.SeasonNumber) + "e" + strconv.Itoa(e.EpisodeNumber)
}

type WatchRoomCreate struct {
	Title             string                      `json:"title"`
	MediaType         string                      `json:"mediaType"`
	ItemID            string                      `json:"itemId"`
	PosterURL         string                      `json:"posterUrl,omitempty"`
	BackdropURL       string                      `json:"backdropUrl,omitempty"`
	Params            map[string]string           `json:"params"`
	Episode           *WatchRoomEpisode           `json:"episode,omitempty"`
	InviteeProfileIDs []string                    `json:"inviteeProfileIds"`
	ClientID          string                      `json:"clientId,omitempty"`
	Capabilities      WatchRoomClientCapabilities `json:"capabilities"`
}

type WatchRoomStateUpdate struct {
	Status           string  `json:"status"`
	Position         float64 `json:"position"`
	Duration         float64 `json:"duration,omitempty"`
	ExpectedRevision *int64  `json:"expectedRevision,omitempty"`
	// Episode, when sent by the host and different from the stored one,
	// switches the room to that episode and holds playback until every
	// member reports ready again.
	Episode *WatchRoomEpisode `json:"episode,omitempty"`
}

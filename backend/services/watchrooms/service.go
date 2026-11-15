package watchrooms

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrNotFound           = errors.New("watch room not found")
	ErrNotInvited         = errors.New("profile is not invited to this watch room")
	ErrNotMember          = errors.New("profile has not joined this watch room")
	ErrNotCreator         = errors.New("only the room creator can end this watch room")
	ErrInvalidMedia       = errors.New("title, mediaType, and itemId are required")
	ErrInvalidState       = errors.New("invalid watch room state")
	ErrRevisionConflict   = errors.New("watch room state changed before this update")
	ErrRoomEnded          = errors.New("watch room has ended")
	ErrIncompatibleClient = errors.New("client does not support Watch Together native playback protocol")
	ErrForeignProfile     = errors.New("invitee profiles must belong to the creator account")
	ErrAccountNotFound    = errors.New("account not found")
	ErrSameAccount        = errors.New("account already belongs to this household")
	ErrInviteUnavailable  = errors.New("watch room invitation is no longer available")
	ErrAlreadyInvited     = errors.New("account is already invited to this watch room")
	ErrShareLinksDisabled = errors.New("share links are not enabled for this profile")
	ErrSourceConflict     = errors.New("watch party source is already bound")
	ErrSourceUnavailable  = errors.New("watch party source is not ready")
)

const (
	roomLifetime       = 24 * time.Hour
	presenceTTL        = 15 * time.Second
	disconnectGrace    = 2 * time.Minute
	endedAuditWindow   = 24 * time.Hour
	protocolVersion    = 1
	externalTokenBytes = 32
)

type profileProvider interface {
	Get(id string) (models.User, bool)
}

type accountProvider interface {
	GetByUsername(username string) (models.Account, bool)
}

type Service struct {
	repo     datastore.WatchRoomRepository
	profiles profileProvider
	accounts accountProvider
	now      func() time.Time
}

func New(repo datastore.WatchRoomRepository, profiles profileProvider, accounts accountProvider) *Service {
	return &Service{repo: repo, profiles: profiles, accounts: accounts, now: time.Now}
}

func supportsWatchTogether(capabilities models.WatchRoomClientCapabilities) bool {
	return capabilities.NativePlayback && capabilities.StateSync && capabilities.ProtocolVersion >= protocolVersion
}

func supportsGuestWatchTogether(capabilities models.WatchRoomClientCapabilities) bool {
	return capabilities.StateSync && capabilities.ProtocolVersion >= protocolVersion
}

func externalInviteTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func generateExternalInviteSecret() (token string, shortCode string, err error) {
	buf := make([]byte, externalTokenBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:5])
	shortCode = code[:4] + "-" + code[4:8]
	return token, shortCode, nil
}

func normalizeExternalInviteCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	var compact strings.Builder
	compact.Grow(8)
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			compact.WriteRune(r)
		}
	}
	if compact.Len() != 8 {
		return ""
	}
	code := compact.String()
	return code[:4] + "-" + code[4:]
}

func (s *Service) Create(ctx context.Context, actorAccountID, creatorID string, in models.WatchRoomCreate) (*models.WatchRoom, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.MediaType = strings.TrimSpace(in.MediaType)
	in.ItemID = strings.TrimSpace(in.ItemID)
	if in.Title == "" || in.MediaType == "" || in.ItemID == "" {
		return nil, ErrInvalidMedia
	}
	creator, ok := s.profiles.Get(creatorID)
	if !ok || creator.AccountID != actorAccountID {
		return nil, ErrNotFound
	}
	if !supportsWatchTogether(in.Capabilities) {
		return nil, ErrIncompatibleClient
	}

	invitees := make([]string, 0, len(in.InviteeProfileIDs))
	seen := map[string]bool{creatorID: true}
	for _, id := range in.InviteeProfileIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		profile, ok := s.profiles.Get(id)
		if !ok {
			continue
		}
		if profile.AccountID != creator.AccountID {
			return nil, ErrForeignProfile
		}
		seen[id] = true
		invitees = append(invitees, id)
	}
	now := s.now().UTC()
	room := &models.WatchRoom{
		ID: uuid.NewString(), CreatorProfileID: creatorID, Title: in.Title,
		MediaType: in.MediaType, ItemID: in.ItemID, PosterURL: strings.TrimSpace(in.PosterURL),
		BackdropURL: strings.TrimSpace(in.BackdropURL), Params: in.Params,
		Status: models.WatchRoomStatusLobby, AnchorUpdatedAt: now, CreatedAt: now,
		ExpiresAt: now.Add(roomLifetime),
	}
	if room.Params == nil {
		room.Params = map[string]string{}
	}
	if err := s.repo.Create(ctx, room, invitees, strings.TrimSpace(in.ClientID), in.Capabilities); err != nil {
		return nil, err
	}
	return s.Get(ctx, room.ID, creatorID)
}

func (s *Service) InviteAccount(ctx context.Context, actorAccountID, creatorProfileID, roomID, username string) (*models.WatchRoomAccountInvite, error) {
	creator, ok := s.profiles.Get(creatorProfileID)
	if !ok || creator.AccountID != actorAccountID {
		return nil, ErrNotFound
	}
	room, err := s.Get(ctx, roomID, creatorProfileID)
	if err != nil {
		return nil, err
	}
	if room.CreatorProfileID != creatorProfileID {
		return nil, ErrNotCreator
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil, ErrRoomEnded
	}
	if s.accounts == nil {
		return nil, ErrAccountNotFound
	}
	account, ok := s.accounts.GetByUsername(strings.TrimSpace(username))
	if !ok || account.IsExpired() {
		return nil, ErrAccountNotFound
	}
	if account.ID == actorAccountID {
		return nil, ErrSameAccount
	}
	now := s.now().UTC()
	invite := &models.WatchRoomAccountInvite{
		ID: uuid.NewString(), RoomID: roomID, InviterAccountID: actorAccountID,
		InviteeAccountID: account.ID, InviteeUsername: account.Username,
		Status: models.WatchRoomAccountInvitePending, CreatedAt: now, ExpiresAt: room.ExpiresAt,
	}
	created, err := s.repo.CreateAccountInvite(ctx, invite)
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, ErrAlreadyInvited
	}
	return invite, nil
}

func (s *Service) AccountInvitations(ctx context.Context, accountID string) ([]models.WatchRoomAccountInvite, error) {
	return s.repo.ListAccountInvites(ctx, accountID, s.now().UTC())
}

func (s *Service) RoomAccountInvitations(ctx context.Context, actorAccountID, creatorProfileID, roomID string) ([]models.WatchRoomAccountInvite, error) {
	creator, ok := s.profiles.Get(creatorProfileID)
	if !ok || creator.AccountID != actorAccountID {
		return nil, ErrNotFound
	}
	room, err := s.Get(ctx, roomID, creatorProfileID)
	if err != nil {
		return nil, err
	}
	if room.CreatorProfileID != creatorProfileID {
		return nil, ErrNotCreator
	}
	return s.repo.ListRoomAccountInvites(ctx, roomID, s.now().UTC())
}

func (s *Service) AcceptAccountInvitation(ctx context.Context, actorAccountID, profileID, inviteID string) (*models.WatchRoom, error) {
	profile, ok := s.profiles.Get(profileID)
	if !ok || profile.AccountID != actorAccountID {
		return nil, ErrNotFound
	}
	roomID, accepted, err := s.repo.AcceptAccountInvite(ctx, inviteID, actorAccountID, profileID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, ErrInviteUnavailable
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) DeclineAccountInvitation(ctx context.Context, actorAccountID, inviteID string) error {
	ok, err := s.repo.DeclineAccountInvite(ctx, inviteID, actorAccountID, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return ErrInviteUnavailable
	}
	return nil
}

func (s *Service) RevokeAccountInvitation(ctx context.Context, actorAccountID, creatorProfileID, roomID, inviteID string) error {
	creator, ok := s.profiles.Get(creatorProfileID)
	if !ok || creator.AccountID != actorAccountID {
		return ErrNotFound
	}
	ok, err := s.repo.RevokeAccountInvite(ctx, inviteID, roomID, creatorProfileID, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return ErrInviteUnavailable
	}
	return nil
}

// CreateExternalInvitation rotates the room's bearer invitation. The raw token
// is returned once; only its SHA-256 hash is persisted.
func (s *Service) CreateExternalInvitation(ctx context.Context, actorAccountID, creatorProfileID, roomID string) (*models.WatchRoomExternalInvite, error) {
	creator, ok := s.profiles.Get(creatorProfileID)
	if !ok || creator.AccountID != actorAccountID {
		return nil, ErrNotFound
	}
	if !creator.AllowShareLinks {
		return nil, ErrShareLinksDisabled
	}
	room, err := s.Get(ctx, roomID, creatorProfileID)
	if err != nil {
		return nil, err
	}
	if room.CreatorProfileID != creatorProfileID {
		return nil, ErrNotCreator
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil, ErrRoomEnded
	}
	token, shortCode, err := generateExternalInviteSecret()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	invite := &models.WatchRoomExternalInvite{
		RoomID: roomID, AccountID: actorAccountID, Token: token,
		TokenHash: externalInviteTokenHash(token), ShortCode: shortCode,
		Active: true, CreatedAt: now, ExpiresAt: room.ExpiresAt,
	}
	if err := s.repo.ReplaceExternalInvite(ctx, invite); err != nil {
		return nil, err
	}
	return invite, nil
}

func (s *Service) RevokeExternalInvitation(ctx context.Context, actorAccountID, creatorProfileID, roomID string) error {
	creator, ok := s.profiles.Get(creatorProfileID)
	if !ok || creator.AccountID != actorAccountID {
		return ErrNotFound
	}
	ok, err := s.repo.RevokeExternalInvite(ctx, roomID, creatorProfileID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInviteUnavailable
	}
	return nil
}

func (s *Service) ResolveExternalInvitation(ctx context.Context, secret string, byCode bool) (*models.WatchRoomExternalInvite, error) {
	secret = strings.TrimSpace(secret)
	if byCode {
		secret = normalizeExternalInviteCode(secret)
		if secret == "" {
			return nil, ErrInviteUnavailable
		}
	}
	var (
		invite *models.WatchRoomExternalInvite
		err    error
	)
	if byCode {
		invite, err = s.repo.GetExternalInviteByCode(ctx, secret, s.now().UTC())
	} else {
		invite, err = s.repo.GetExternalInviteByTokenHash(ctx, externalInviteTokenHash(secret), s.now().UTC())
	}
	if err != nil {
		return nil, err
	}
	if invite == nil {
		return nil, ErrInviteUnavailable
	}
	return invite, nil
}

// GetForExternalInvite returns the room metadata needed to render the claim
// page. Callers must resolve a live invitation before calling it.
func (s *Service) GetForExternalInvite(ctx context.Context, roomID string) (*models.WatchRoom, error) {
	room, err := s.repo.Get(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, ErrNotFound
	}
	s.decorate(room)
	return room, nil
}

func (s *Service) JoinExternalGuest(ctx context.Context, invite *models.WatchRoomExternalInvite, guestID, name, clientID string, capabilities models.WatchRoomClientCapabilities) (*models.WatchRoom, error) {
	if invite == nil || !invite.Active || !invite.ExpiresAt.After(s.now()) {
		return nil, ErrInviteUnavailable
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 || !supportsGuestWatchTogether(capabilities) {
		return nil, ErrIncompatibleClient
	}
	if err := s.repo.JoinExternalGuest(ctx, invite.RoomID, guestID, name, strings.TrimSpace(clientID), capabilities, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.GetForExternalGuest(ctx, invite.RoomID, guestID)
}

func (s *Service) GetForExternalGuest(ctx context.Context, roomID, guestID string) (*models.WatchRoom, error) {
	allowed, err := s.repo.IsExternalGuest(ctx, roomID, guestID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotMember
	}
	room, err := s.repo.Get(ctx, roomID)
	if err != nil || room == nil {
		if err == nil {
			err = ErrNotFound
		}
		return room, err
	}
	s.decorate(room)
	return room, nil
}

func (s *Service) HeartbeatExternalGuest(ctx context.Context, roomID, guestID, clientID string, buffering bool) error {
	if _, err := s.GetForExternalGuest(ctx, roomID, guestID); err != nil {
		return err
	}
	return s.repo.HeartbeatExternalGuest(ctx, roomID, guestID, strings.TrimSpace(clientID), buffering, s.now().UTC())
}

func (s *Service) LeaveExternalGuest(ctx context.Context, roomID, guestID string) error {
	return s.repo.LeaveExternalGuest(ctx, roomID, guestID)
}

func (s *Service) SetExternalGuestReady(ctx context.Context, roomID, guestID string, ready bool) error {
	if _, err := s.GetForExternalGuest(ctx, roomID, guestID); err != nil {
		return err
	}
	return s.repo.SetExternalGuestReady(ctx, roomID, guestID, ready, s.now().UTC())
}

func (s *Service) BindExternalSource(ctx context.Context, actorAccountID, creatorProfileID, roomID, resource string, params map[string]string) (*models.WatchRoomExternalSource, error) {
	creator, ok := s.profiles.Get(creatorProfileID)
	if !ok || creator.AccountID != actorAccountID {
		return nil, ErrNotFound
	}
	if !creator.AllowShareLinks {
		return nil, ErrShareLinksDisabled
	}
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return nil, ErrInvalidMedia
	}
	room, err := s.Get(ctx, roomID, creatorProfileID)
	if err != nil {
		return nil, err
	}
	if room.CreatorProfileID != creatorProfileID {
		return nil, ErrNotCreator
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil, ErrRoomEnded
	}
	// The source is bound whether or not a share link exists yet: it is only
	// ever handed to external guests, who must join through an active invite,
	// and binding up front lets a link created mid-playback start immediately.
	bound, err := s.repo.BindExternalSource(ctx, roomID, creatorProfileID, resource, params, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, ErrSourceConflict
	}
	return s.repo.GetExternalSource(ctx, roomID)
}

func (s *Service) ExternalSourceForGuest(ctx context.Context, roomID, guestID string) (*models.WatchRoomExternalSource, error) {
	if _, err := s.GetForExternalGuest(ctx, roomID, guestID); err != nil {
		return nil, err
	}
	source, err := s.repo.GetExternalSource(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if source == nil || strings.TrimSpace(source.Resource) == "" {
		return nil, ErrSourceUnavailable
	}
	return source, nil
}

// ValidateExternalPlayback rechecks persisted room authority for every media
// request, including already-issued playlist URLs and new HLS session creation.
func (s *Service) ValidateExternalPlayback(ctx context.Context, roomID, guestID, resource string) error {
	room, err := s.GetForExternalGuest(ctx, roomID, guestID)
	if err != nil {
		return err
	}
	if !room.ExpiresAt.After(s.now()) || (room.Status != models.WatchRoomStatusPlaying && room.Status != models.WatchRoomStatusPaused) {
		return ErrRoomEnded
	}
	source, err := s.repo.GetExternalSource(ctx, roomID)
	if err != nil {
		return err
	}
	if source == nil || source.Resource == "" || source.Resource != resource {
		return ErrSourceUnavailable
	}
	return nil
}

func (s *Service) Invitations(ctx context.Context, profileID string) ([]models.WatchRoom, error) {
	rooms, err := s.repo.ListInvitations(ctx, profileID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	for i := range rooms {
		s.decorate(&rooms[i])
	}
	return rooms, nil
}

func (s *Service) Get(ctx context.Context, roomID, profileID string) (*models.WatchRoom, error) {
	allowed, err := s.repo.IsInvited(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	room, err := s.repo.Get(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, ErrNotFound
	}
	if room.Status != models.WatchRoomStatusEnded && !room.ExpiresAt.After(s.now()) {
		if err := s.repo.EndExpired(ctx, roomID, s.now().UTC()); err != nil {
			return nil, err
		}
		room, err = s.repo.Get(ctx, roomID)
		if err != nil || room == nil {
			return room, err
		}
	}
	s.decorate(room)
	return room, nil
}

func (s *Service) Join(ctx context.Context, roomID, profileID, clientID string, capabilities models.WatchRoomClientCapabilities) (*models.WatchRoom, error) {
	if !supportsWatchTogether(capabilities) {
		return nil, ErrIncompatibleClient
	}
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return nil, err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil, ErrRoomEnded
	}
	if err := s.repo.Join(ctx, roomID, profileID, strings.TrimSpace(clientID), capabilities, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) SetReady(ctx context.Context, roomID, profileID string, ready bool) (*models.WatchRoom, error) {
	if err := s.requireMember(ctx, roomID, profileID); err != nil {
		return nil, err
	}
	if err := s.repo.SetReady(ctx, roomID, profileID, ready, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) UpdateState(ctx context.Context, roomID, profileID string, update models.WatchRoomStateUpdate) (*models.WatchRoom, error) {
	if err := s.requireMember(ctx, roomID, profileID); err != nil {
		return nil, err
	}
	switch update.Status {
	case models.WatchRoomStatusPlaying, models.WatchRoomStatusPaused:
	default:
		return nil, ErrInvalidState
	}
	if math.IsNaN(update.Position) || math.IsInf(update.Position, 0) || update.Position < 0 {
		return nil, ErrInvalidState
	}
	if math.IsNaN(update.Duration) || math.IsInf(update.Duration, 0) || update.Duration < 0 {
		return nil, ErrInvalidState
	}
	updated, err := s.repo.UpdateState(ctx, roomID, profileID, update.Status, update.Position, update.Duration, update.ExpectedRevision, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, ErrRevisionConflict
	}
	return s.Get(ctx, roomID, profileID)
}

func (s *Service) Heartbeat(ctx context.Context, roomID, profileID, clientID string, buffering bool) error {
	if err := s.requireMember(ctx, roomID, profileID); err != nil {
		return err
	}
	return s.repo.Heartbeat(ctx, roomID, profileID, strings.TrimSpace(clientID), buffering, s.now().UTC())
}

func (s *Service) Leave(ctx context.Context, roomID, profileID string) error {
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return ErrRoomEnded
	}
	return s.repo.Leave(ctx, roomID, profileID)
}

func (s *Service) End(ctx context.Context, roomID, profileID string) error {
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return nil
	}
	ok, err := s.repo.End(ctx, roomID, profileID, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotCreator
	}
	return nil
}

func (s *Service) Cleanup(ctx context.Context) (ended int64, deleted int64, err error) {
	now := s.now().UTC()
	return s.repo.Sweep(ctx, now, now.Add(-disconnectGrace), now.Add(-endedAuditWindow))
}

func (s *Service) requireMember(ctx context.Context, roomID, profileID string) error {
	room, err := s.Get(ctx, roomID, profileID)
	if err != nil {
		return err
	}
	if room.Status == models.WatchRoomStatusEnded {
		return ErrRoomEnded
	}
	for _, member := range room.Members {
		if member.ProfileID == profileID && member.Joined {
			return nil
		}
	}
	return ErrNotMember
}

func (s *Service) decorate(room *models.WatchRoom) {
	now := s.now().UTC()
	if room.Status == models.WatchRoomStatusPlaying && !room.WaitingForReady {
		room.Position += now.Sub(room.AnchorUpdatedAt).Seconds()
		if room.Duration > 0 && room.Position > room.Duration {
			room.Position = room.Duration
		}
	}
	for i := range room.Members {
		room.Members[i].Present = now.Sub(room.Members[i].LastSeenAt) <= presenceTTL
	}
}

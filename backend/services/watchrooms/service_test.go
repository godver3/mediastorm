package watchrooms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

type fakeProfiles map[string]models.User

func (p fakeProfiles) Get(id string) (models.User, bool) { user, ok := p[id]; return user, ok }

type fakeAccounts map[string]models.Account

func (a fakeAccounts) GetByUsername(username string) (models.Account, bool) {
	for _, account := range a {
		if account.Username == username {
			return account, true
		}
	}
	return models.Account{}, false
}

type fakeRoomRepo struct {
	room           *models.WatchRoom
	invites        map[string]bool
	members        map[string]models.WatchRoomMember
	stateSet       bool
	accountInvites map[string]models.WatchRoomAccountInvite
	externalInvite *models.WatchRoomExternalInvite
	guestMembers   map[string]models.WatchRoomMember
	externalSource *models.WatchRoomExternalSource
}

func (r *fakeRoomRepo) ReplaceExternalInvite(_ context.Context, invite *models.WatchRoomExternalInvite) error {
	copy := *invite
	r.externalInvite = &copy
	return nil
}
func (r *fakeRoomRepo) RevokeExternalInvite(_ context.Context, roomID, creatorProfileID string) (bool, error) {
	if r.externalInvite == nil || r.externalInvite.RoomID != roomID || r.room.CreatorProfileID != creatorProfileID || !r.externalInvite.Active {
		return false, nil
	}
	r.externalInvite.Active = false
	return true, nil
}
func (r *fakeRoomRepo) GetExternalInviteByTokenHash(_ context.Context, tokenHash string, now time.Time) (*models.WatchRoomExternalInvite, error) {
	if r.externalInvite == nil || r.externalInvite.TokenHash != tokenHash || !r.externalInvite.Active || !r.externalInvite.ExpiresAt.After(now) {
		return nil, nil
	}
	copy := *r.externalInvite
	return &copy, nil
}
func (r *fakeRoomRepo) GetExternalInviteByCode(_ context.Context, shortCode string, now time.Time) (*models.WatchRoomExternalInvite, error) {
	if r.externalInvite == nil || r.externalInvite.ShortCode != shortCode || !r.externalInvite.Active || !r.externalInvite.ExpiresAt.After(now) {
		return nil, nil
	}
	copy := *r.externalInvite
	return &copy, nil
}
func (r *fakeRoomRepo) JoinExternalGuest(_ context.Context, _, guestID, name, clientID string, capabilities models.WatchRoomClientCapabilities, now time.Time) error {
	if r.guestMembers == nil {
		r.guestMembers = map[string]models.WatchRoomMember{}
	}
	r.guestMembers[guestID] = models.WatchRoomMember{ProfileID: "guest:" + guestID, Name: name, IsGuest: true, ClientID: clientID, Joined: true, JoinedAt: now, LastSeenAt: now, Capabilities: capabilities}
	return nil
}
func (r *fakeRoomRepo) IsExternalGuest(_ context.Context, roomID, guestID string) (bool, error) {
	_, ok := r.guestMembers[guestID]
	return r.room != nil && r.room.ID == roomID && ok, nil
}
func (r *fakeRoomRepo) HeartbeatExternalGuest(_ context.Context, _, guestID, clientID string, buffering bool, now time.Time) error {
	m := r.guestMembers[guestID]
	m.ClientID, m.Buffering, m.LastSeenAt = clientID, buffering, now
	r.guestMembers[guestID] = m
	return nil
}
func (r *fakeRoomRepo) LeaveExternalGuest(_ context.Context, _, guestID string) error {
	delete(r.guestMembers, guestID)
	return nil
}
func (r *fakeRoomRepo) SetExternalGuestReady(_ context.Context, _, guestID string, ready bool, now time.Time) error {
	m := r.guestMembers[guestID]
	m.Ready, m.LastSeenAt = ready, now
	r.guestMembers[guestID] = m
	return nil
}
func (r *fakeRoomRepo) BindExternalSource(_ context.Context, roomID, creatorProfileID, resource string, params map[string]string, episodeKey string, now time.Time) (bool, error) {
	if r.room == nil || r.room.ID != roomID || r.room.CreatorProfileID != creatorProfileID || r.room.Status == models.WatchRoomStatusEnded {
		return false, nil
	}
	if r.externalSource != nil && r.externalSource.EpisodeKey == episodeKey {
		return r.externalSource.Resource == resource, nil
	}
	r.externalSource = &models.WatchRoomExternalSource{RoomID: roomID, Resource: resource, Params: params, EpisodeKey: episodeKey, BoundAt: now}
	return true, nil
}
func (r *fakeRoomRepo) GetExternalSource(_ context.Context, roomID string) (*models.WatchRoomExternalSource, error) {
	if r.externalSource == nil || r.externalSource.RoomID != roomID {
		return nil, nil
	}
	copy := *r.externalSource
	return &copy, nil
}

func (r *fakeRoomRepo) CreateAccountInvite(_ context.Context, invite *models.WatchRoomAccountInvite) (bool, error) {
	if r.accountInvites == nil {
		r.accountInvites = map[string]models.WatchRoomAccountInvite{}
	}
	for id, existing := range r.accountInvites {
		if existing.RoomID == invite.RoomID && existing.InviteeAccountID == invite.InviteeAccountID {
			if existing.Status != models.WatchRoomAccountInviteDeclined && existing.Status != models.WatchRoomAccountInviteRevoked {
				return false, nil
			}
			delete(r.accountInvites, id)
		}
	}
	r.accountInvites[invite.ID] = *invite
	return true, nil
}
func (r *fakeRoomRepo) ListAccountInvites(_ context.Context, accountID string, now time.Time) ([]models.WatchRoomAccountInvite, error) {
	result := []models.WatchRoomAccountInvite{}
	for _, invite := range r.accountInvites {
		if invite.InviteeAccountID == accountID && invite.Status == models.WatchRoomAccountInvitePending && invite.ExpiresAt.After(now) {
			result = append(result, invite)
		}
	}
	return result, nil
}
func (r *fakeRoomRepo) ListRoomAccountInvites(_ context.Context, roomID string, now time.Time) ([]models.WatchRoomAccountInvite, error) {
	result := []models.WatchRoomAccountInvite{}
	for _, invite := range r.accountInvites {
		if invite.RoomID == roomID && invite.Status == models.WatchRoomAccountInvitePending && invite.ExpiresAt.After(now) {
			result = append(result, invite)
		}
	}
	return result, nil
}
func (r *fakeRoomRepo) AcceptAccountInvite(_ context.Context, inviteID, accountID, profileID string, now time.Time) (string, bool, error) {
	invite, ok := r.accountInvites[inviteID]
	if !ok || invite.InviteeAccountID != accountID || invite.Status != models.WatchRoomAccountInvitePending || !invite.ExpiresAt.After(now) {
		return "", false, nil
	}
	invite.Status, invite.AcceptedProfileID, invite.RespondedAt = models.WatchRoomAccountInviteAccepted, profileID, &now
	r.accountInvites[inviteID] = invite
	r.invites[profileID] = true
	return invite.RoomID, true, nil
}
func (r *fakeRoomRepo) DeclineAccountInvite(_ context.Context, inviteID, accountID string, now time.Time) (bool, error) {
	invite, ok := r.accountInvites[inviteID]
	if !ok || invite.InviteeAccountID != accountID || invite.Status != models.WatchRoomAccountInvitePending {
		return false, nil
	}
	invite.Status, invite.RespondedAt = models.WatchRoomAccountInviteDeclined, &now
	r.accountInvites[inviteID] = invite
	return true, nil
}
func (r *fakeRoomRepo) RevokeAccountInvite(_ context.Context, inviteID, roomID, creatorProfileID string, now time.Time) (bool, error) {
	invite, ok := r.accountInvites[inviteID]
	if !ok || invite.RoomID != roomID || r.room.CreatorProfileID != creatorProfileID || invite.Status != models.WatchRoomAccountInvitePending {
		return false, nil
	}
	invite.Status, invite.RevokedAt = models.WatchRoomAccountInviteRevoked, &now
	r.accountInvites[inviteID] = invite
	return true, nil
}

var supportedCapabilities = models.WatchRoomClientCapabilities{NativePlayback: true, StateSync: true, ProtocolVersion: 1}

func (r *fakeRoomRepo) Create(_ context.Context, room *models.WatchRoom, invitees []string, clientID string, capabilities models.WatchRoomClientCapabilities) error {
	copy := *room
	r.room = &copy
	r.invites = map[string]bool{room.CreatorProfileID: true}
	for _, id := range invitees {
		r.invites[id] = true
	}
	r.members = map[string]models.WatchRoomMember{
		room.CreatorProfileID: {ProfileID: room.CreatorProfileID, IsCreator: true, Ready: true, Joined: true, ClientID: clientID, JoinedAt: room.CreatedAt, LastSeenAt: room.CreatedAt, Capabilities: capabilities},
	}
	return nil
}
func (r *fakeRoomRepo) Get(_ context.Context, roomID string) (*models.WatchRoom, error) {
	if r.room == nil || r.room.ID != roomID {
		return nil, nil
	}
	copy := *r.room
	copy.Members = make([]models.WatchRoomMember, 0, len(r.invites))
	for _, member := range r.members {
		copy.Members = append(copy.Members, member)
	}
	for profileID := range r.invites {
		if _, joined := r.members[profileID]; !joined {
			copy.Members = append(copy.Members, models.WatchRoomMember{ProfileID: profileID})
		}
	}
	for _, member := range r.guestMembers {
		copy.Members = append(copy.Members, member)
	}
	return &copy, nil
}
func (r *fakeRoomRepo) ListInvitations(_ context.Context, profileID string, _ time.Time) ([]models.WatchRoom, error) {
	if r.room == nil || !r.invites[profileID] {
		return []models.WatchRoom{}, nil
	}
	room, _ := r.Get(context.Background(), r.room.ID)
	return []models.WatchRoom{*room}, nil
}
func (r *fakeRoomRepo) IsInvited(_ context.Context, roomID, profileID string) (bool, error) {
	return r.room != nil && r.room.ID == roomID && r.invites[profileID], nil
}
func (r *fakeRoomRepo) Join(_ context.Context, _, profileID, clientID string, capabilities models.WatchRoomClientCapabilities, now time.Time) error {
	r.members[profileID] = models.WatchRoomMember{ProfileID: profileID, ClientID: clientID, Joined: true, JoinedAt: now, LastSeenAt: now, Capabilities: capabilities}
	return nil
}
func (r *fakeRoomRepo) SetReady(_ context.Context, _, profileID string, ready bool, now time.Time) error {
	m := r.members[profileID]
	m.Ready = ready
	m.LastSeenAt = now
	r.members[profileID] = m
	return nil
}
func (r *fakeRoomRepo) UpdateState(_ context.Context, _, profileID, status string, position, duration float64, expectedRevision *int64, episode *models.WatchRoomEpisode, now time.Time) (bool, error) {
	if expectedRevision != nil && r.room.Revision != *expectedRevision {
		return false, nil
	}
	if episode != nil {
		if profileID != r.room.CreatorProfileID {
			return false, nil
		}
		copy := *episode
		r.room.Episode = &copy
		r.room.WaitingForReady = true
		for id, m := range r.members {
			m.Ready = false
			r.members[id] = m
		}
		for id, m := range r.guestMembers {
			m.Ready = false
			r.guestMembers[id] = m
		}
	}
	r.room.Status, r.room.Position, r.room.Duration = status, position, duration
	r.room.Revision++
	r.room.UpdatedBy = profileID
	r.room.AnchorUpdatedAt = now
	r.stateSet = true
	return true, nil
}
func (r *fakeRoomRepo) Heartbeat(_ context.Context, _, profileID, clientID string, buffering bool, now time.Time) error {
	m := r.members[profileID]
	m.ClientID = clientID
	m.Buffering = buffering
	m.LastSeenAt = now
	r.members[profileID] = m
	return nil
}
func (r *fakeRoomRepo) Leave(_ context.Context, _, profileID string) error {
	delete(r.members, profileID)
	return nil
}
func (r *fakeRoomRepo) End(_ context.Context, _, profileID string, now time.Time) (bool, error) {
	if r.room.CreatorProfileID != profileID {
		return false, nil
	}
	r.room.Status = models.WatchRoomStatusEnded
	r.room.AnchorUpdatedAt = now
	r.room.EndedAt = &now
	r.room.EndReason = "host_ended"
	return true, nil
}
func (r *fakeRoomRepo) EndExpired(_ context.Context, _ string, now time.Time) error {
	r.room.Status = models.WatchRoomStatusEnded
	r.room.AnchorUpdatedAt = now
	r.room.EndedAt = &now
	r.room.EndReason = "expired"
	return nil
}
func (r *fakeRoomRepo) Sweep(_ context.Context, now, disconnectCutoff, deleteBefore time.Time) (int64, int64, error) {
	if r.room == nil {
		return 0, 0, nil
	}
	if r.room.EndedAt != nil && r.room.EndedAt.Before(deleteBefore) {
		r.room = nil
		return 0, 1, nil
	}
	if r.room.Status != models.WatchRoomStatusEnded {
		host := r.members[r.room.CreatorProfileID]
		if !host.LastSeenAt.After(disconnectCutoff) {
			latest := host.LastSeenAt
			for _, member := range r.members {
				if member.LastSeenAt.After(latest) {
					latest = member.LastSeenAt
				}
			}
			reason := "host_disconnected"
			if !latest.After(disconnectCutoff) {
				reason = "all_left"
			}
			r.room.Status = models.WatchRoomStatusEnded
			r.room.EndedAt = &now
			r.room.EndReason = reason
			return 1, 0, nil
		}
	}
	return 0, 0, nil
}

func TestCreateJoinAndUpdateWatchRoom(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{
		"host":  {ID: "host", AccountID: "home", Name: "Host"},
		"guest": {ID: "guest", AccountID: "home", Name: "Guest"},
		"other": {ID: "other", AccountID: "home", Name: "Other"},
	}, fakeAccounts{"home": {ID: "home", Username: "home"}})
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "tmdb:movie:1",
		InviteeProfileIDs: []string{"guest", "missing"}, ClientID: "host-tv", Capabilities: supportedCapabilities,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if room.Status != models.WatchRoomStatusLobby || !repo.invites["guest"] || repo.invites["missing"] {
		t.Fatalf("unexpected created room: %#v invites=%v", room, repo.invites)
	}
	if len(room.Members) != 2 {
		t.Fatalf("created room members = %#v, want joined host and invited guest", room.Members)
	}
	foundJoinedHost := false
	foundInvitedGuest := false
	for _, member := range room.Members {
		if member.ProfileID == "host" && member.Joined {
			foundJoinedHost = true
		}
		if member.ProfileID == "guest" && !member.Joined {
			foundInvitedGuest = true
		}
	}
	if !foundJoinedHost || !foundInvitedGuest {
		t.Fatalf("created room did not distinguish joined and invited members: %#v", room.Members)
	}
	otherInvitations, err := svc.Invitations(context.Background(), "other")
	if err != nil {
		t.Fatalf("Invitations(other) error = %v", err)
	}
	if len(otherInvitations) != 0 {
		t.Fatalf("unselected profile received %d invitation(s), want 0", len(otherInvitations))
	}
	hostRooms, err := svc.Invitations(context.Background(), "host")
	if err != nil || len(hostRooms) != 1 || hostRooms[0].ID != room.ID {
		t.Fatalf("Invitations(host) = %#v, %v; want the host's active room", hostRooms, err)
	}

	joined, err := svc.Join(context.Background(), room.ID, "guest", "guest-tv", supportedCapabilities)
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if len(joined.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(joined.Members))
	}
	for _, member := range joined.Members {
		if member.ProfileID == "guest" && !member.Joined {
			t.Fatal("guest remained unjoined after Join")
		}
	}
	if _, err := svc.SetReady(context.Background(), room.ID, "guest", true); err != nil {
		t.Fatalf("SetReady() error = %v", err)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "guest", models.WatchRoomStateUpdate{Status: "playing", Position: 10, Duration: 100}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if !repo.stateSet {
		t.Fatal("state was not persisted")
	}

	now = now.Add(5 * time.Second)
	updated, err := svc.Get(context.Background(), room.ID, "guest")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if updated.Position != 15 {
		t.Fatalf("effective position = %v, want 15", updated.Position)
	}
}

func TestExternalInvitationCoexistsWithLocalProfileInvite(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{
		"host":  {ID: "host", AccountID: "home", Name: "Host", AllowShareLinks: true},
		"local": {ID: "local", AccountID: "home", Name: "Local"},
	}, fakeAccounts{"home": {ID: "home", Username: "home"}})
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "tmdb:movie:1", InviteeProfileIDs: []string{"local"}, Capabilities: supportedCapabilities,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !repo.invites["local"] {
		t.Fatal("local profile invitation was not retained")
	}
	invite, err := svc.CreateExternalInvitation(context.Background(), "home", "host", room.ID)
	if err != nil {
		t.Fatalf("CreateExternalInvitation() error = %v", err)
	}
	if invite.Token == "" || invite.TokenHash == "" || invite.ShortCode == "" || invite.Token == invite.TokenHash {
		t.Fatalf("external invite was not safely generated: %#v", invite)
	}
	resolved, err := svc.ResolveExternalInvitation(context.Background(), invite.Token, false)
	if err != nil || resolved.RoomID != room.ID {
		t.Fatalf("ResolveExternalInvitation() = %#v, %v", resolved, err)
	}
	compactCode := strings.ToLower(strings.ReplaceAll(invite.ShortCode, "-", ""))
	resolvedByCompactCode, err := svc.ResolveExternalInvitation(context.Background(), compactCode, true)
	if err != nil || resolvedByCompactCode.RoomID != room.ID {
		t.Fatalf("ResolveExternalInvitation(compact code) = %#v, %v", resolvedByCompactCode, err)
	}
	guestRoom, err := svc.JoinExternalGuest(context.Background(), resolved, "browser-1", "Outside Guest", "browser", models.WatchRoomClientCapabilities{
		StateSync: true, ProtocolVersion: 1,
	})
	if err != nil {
		t.Fatalf("JoinExternalGuest() error = %v", err)
	}
	found := false
	for _, member := range guestRoom.Members {
		if member.ProfileID == "guest:browser-1" && member.IsGuest && member.Name == "Outside Guest" {
			found = true
		}
	}
	if !found {
		t.Fatalf("external guest missing from room: %#v", guestRoom.Members)
	}
	source, err := svc.BindExternalSource(context.Background(), "home", "host", room.ID, "/movies/movie.mkv", map[string]string{"title": "Movie"}, nil)
	if err != nil || source.Resource != "/movies/movie.mkv" {
		t.Fatalf("BindExternalSource() = %#v, %v", source, err)
	}
	if _, err := svc.BindExternalSource(context.Background(), "home", "host", room.ID, "/movies/other.mkv", nil, nil); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("second source bind err = %v, want %v", err, ErrSourceConflict)
	}
	if err := svc.RevokeExternalInvitation(context.Background(), "home", "host", room.ID); err != nil {
		t.Fatalf("RevokeExternalInvitation() error = %v", err)
	}
	if _, err := svc.ResolveExternalInvitation(context.Background(), invite.Token, false); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatalf("resolved revoked invitation with err %v", err)
	}
}

func TestBindExternalSourceDoesNotRequireShareLink(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{
		"host":  {ID: "host", AccountID: "home", Name: "Host", AllowShareLinks: true},
		"local": {ID: "local", AccountID: "home", Name: "Local", AllowShareLinks: true},
	}, fakeAccounts{})
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	ctx := context.Background()

	room, err := svc.Create(ctx, "home", "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "tmdb:movie:1", InviteeProfileIDs: []string{"local"}, Capabilities: supportedCapabilities,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// A household-only room has no share link; the host's bind must still
	// succeed (and stay idempotent) rather than reporting a conflict.
	for i := 0; i < 2; i++ {
		source, err := svc.BindExternalSource(ctx, "home", "host", room.ID, "/movies/movie.mkv", nil, nil)
		if err != nil || source == nil || source.Resource != "/movies/movie.mkv" {
			t.Fatalf("BindExternalSource() attempt %d = %#v, %v", i+1, source, err)
		}
	}
	if _, err := svc.BindExternalSource(ctx, "home", "local", room.ID, "/movies/movie.mkv", nil, nil); !errors.Is(err, ErrNotCreator) {
		t.Fatalf("invitee bind err = %v, want %v", err, ErrNotCreator)
	}

	// A share link created after playback started can be served immediately.
	invite, err := svc.CreateExternalInvitation(ctx, "home", "host", room.ID)
	if err != nil {
		t.Fatalf("CreateExternalInvitation() error = %v", err)
	}
	if _, err := svc.JoinExternalGuest(ctx, invite, "browser-1", "Outside Guest", "browser", models.WatchRoomClientCapabilities{StateSync: true, ProtocolVersion: 1}); err != nil {
		t.Fatalf("JoinExternalGuest() error = %v", err)
	}
	source, err := svc.ExternalSourceForGuest(ctx, room.ID, "browser-1")
	if err != nil || source.Resource != "/movies/movie.mkv" {
		t.Fatalf("ExternalSourceForGuest() = %#v, %v", source, err)
	}

	if err := svc.End(ctx, room.ID, "host"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	if _, err := svc.BindExternalSource(ctx, "home", "host", room.ID, "/movies/movie.mkv", nil, nil); !errors.Is(err, ErrRoomEnded) {
		t.Fatalf("ended room bind err = %v, want %v", err, ErrRoomEnded)
	}
}

func TestUpdateStateRejectsNonMemberAndInvalidValues(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{"host": {ID: "host", AccountID: "home"}, "guest": {ID: "guest", AccountID: "home"}}, fakeAccounts{})
	svc.now = func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) }
	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{Title: "Movie", MediaType: "movie", ItemID: "one", InviteeProfileIDs: []string{"guest"}, Capabilities: supportedCapabilities})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "guest", models.WatchRoomStateUpdate{Status: "playing"}); err != ErrNotMember {
		t.Fatalf("non-member error = %v, want %v", err, ErrNotMember)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: "lobby"}); err != ErrInvalidState {
		t.Fatalf("invalid state error = %v, want %v", err, ErrInvalidState)
	}
}

func TestUpdateStateRejectsStaleRevision(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{"host": {ID: "host", AccountID: "home"}}, fakeAccounts{})
	svc.now = func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) }
	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{Title: "Movie", MediaType: "movie", ItemID: "one", Capabilities: supportedCapabilities})
	if err != nil {
		t.Fatal(err)
	}
	staleRevision := room.Revision - 1
	_, err = svc.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{
		Status:           "paused",
		Position:         12,
		ExpectedRevision: &staleRevision,
	})
	if err != ErrRevisionConflict {
		t.Fatalf("stale update error = %v, want %v", err, ErrRevisionConflict)
	}
	if repo.room.Status == "paused" {
		t.Fatal("stale update changed room state")
	}
}

func TestDecorateDoesNotAdvanceRoomWhilePlayersAreGettingReady(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 10, 0, time.UTC)
	svc := &Service{now: func() time.Time { return now }}
	room := &models.WatchRoom{
		Status:          models.WatchRoomStatusPlaying,
		WaitingForReady: true,
		Position:        42,
		AnchorUpdatedAt: now.Add(-10 * time.Second),
	}

	svc.decorate(room)

	if room.Position != 42 {
		t.Fatalf("waiting room position = %v, want 42", room.Position)
	}
}

func TestRejectsIncompatibleClients(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{"host": {ID: "host", AccountID: "home"}, "guest": {ID: "guest", AccountID: "home"}}, fakeAccounts{})
	if _, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{Title: "Movie", MediaType: "movie", ItemID: "one"}); err != ErrIncompatibleClient {
		t.Fatalf("Create() error = %v, want %v", err, ErrIncompatibleClient)
	}
	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{Title: "Movie", MediaType: "movie", ItemID: "one", InviteeProfileIDs: []string{"guest"}, Capabilities: supportedCapabilities})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(context.Background(), room.ID, "guest", "guest-tv", models.WatchRoomClientCapabilities{NativePlayback: true}); err != ErrIncompatibleClient {
		t.Fatalf("Join() error = %v, want %v", err, ErrIncompatibleClient)
	}
}

func TestCrossAccountInvitationRequiresRecipientAcceptance(t *testing.T) {
	repo := &fakeRoomRepo{}
	profiles := fakeProfiles{
		"host":   {ID: "host", AccountID: "home", Name: "Host"},
		"local":  {ID: "local", AccountID: "home", Name: "Local"},
		"friend": {ID: "friend", AccountID: "friends", Name: "Friend"},
	}
	accounts := fakeAccounts{
		"home":    {ID: "home", Username: "home"},
		"friends": {ID: "friends", Username: "friends"},
	}
	svc := New(repo, profiles, accounts)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	if _, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "one", InviteeProfileIDs: []string{"friend"}, Capabilities: supportedCapabilities,
	}); err != ErrForeignProfile {
		t.Fatalf("foreign direct invite error = %v, want %v", err, ErrForeignProfile)
	}

	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "one", InviteeProfileIDs: []string{"local"}, Capabilities: supportedCapabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := svc.InviteAccount(context.Background(), "home", "host", room.ID, "friends")
	if err != nil {
		t.Fatal(err)
	}
	if repo.invites["friend"] {
		t.Fatal("recipient profile was exposed before acceptance")
	}
	pending, err := svc.AccountInvitations(context.Background(), "friends")
	if err != nil || len(pending) != 1 || pending[0].ID != invite.ID {
		t.Fatalf("AccountInvitations() = %#v, %v", pending, err)
	}
	if _, err := svc.AcceptAccountInvitation(context.Background(), "home", "friend", invite.ID); err != ErrNotFound {
		t.Fatalf("foreign acceptance error = %v, want %v", err, ErrNotFound)
	}
	accepted, err := svc.AcceptAccountInvitation(context.Background(), "friends", "friend", invite.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ID != room.ID || !repo.invites["friend"] {
		t.Fatalf("accepted room = %#v invites=%v", accepted, repo.invites)
	}
	if _, err := svc.AcceptAccountInvitation(context.Background(), "friends", "friend", invite.ID); err != ErrInviteUnavailable {
		t.Fatalf("replay error = %v, want %v", err, ErrInviteUnavailable)
	}
}

func TestCrossAccountInvitationCanBeDeclinedRevokedAndReissued(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{
		"host":   {ID: "host", AccountID: "home"},
		"friend": {ID: "friend", AccountID: "friends"},
	}, fakeAccounts{
		"home":    {ID: "home", Username: "home"},
		"friends": {ID: "friends", Username: "friends"},
	})
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{
		Title: "Movie", MediaType: "movie", ItemID: "one", Capabilities: supportedCapabilities,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.InviteAccount(context.Background(), "home", "host", room.ID, "friends")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeclineAccountInvitation(context.Background(), "home", first.ID); err != ErrInviteUnavailable {
		t.Fatalf("foreign decline error = %v, want %v", err, ErrInviteUnavailable)
	}
	if err := svc.DeclineAccountInvitation(context.Background(), "friends", first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := svc.InviteAccount(context.Background(), "home", "host", room.ID, "friends")
	if err != nil {
		t.Fatalf("reissue after decline: %v", err)
	}
	if err := svc.RevokeAccountInvitation(context.Background(), "home", "host", room.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := svc.AccountInvitations(context.Background(), "friends"); err != nil || len(pending) != 0 {
		t.Fatalf("pending after revoke = %#v, %v", pending, err)
	}
}

func TestCleanupEndsDisconnectedRoomAndRetainsTerminalResponse(t *testing.T) {
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{"host": {ID: "host", AccountID: "home"}}, fakeAccounts{})
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	room, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{Title: "Movie", MediaType: "movie", ItemID: "one", Capabilities: supportedCapabilities})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(disconnectGrace + time.Second)
	ended, deleted, err := svc.Cleanup(context.Background())
	if err != nil || ended != 1 || deleted != 0 {
		t.Fatalf("Cleanup() = (%d,%d,%v), want (1,0,nil)", ended, deleted, err)
	}
	terminal, err := svc.Get(context.Background(), room.ID, "host")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != models.WatchRoomStatusEnded || terminal.EndReason != "all_left" || terminal.EndedAt == nil {
		t.Fatalf("terminal room = %#v", terminal)
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: "playing"}); err != ErrRoomEnded {
		t.Fatalf("UpdateState() error = %v, want %v", err, ErrRoomEnded)
	}
}

package watchrooms

import (
	"context"
	"errors"
	"testing"
	"time"

	"novastream/models"
)

func newEpisodeRoom(t *testing.T) (*Service, *fakeRoomRepo, *models.WatchRoom) {
	t.Helper()
	repo := &fakeRoomRepo{}
	svc := New(repo, fakeProfiles{
		"host":  {ID: "host", AccountID: "home", Name: "Host", AllowShareLinks: true},
		"guest": {ID: "guest", AccountID: "home", Name: "Guest"},
	}, fakeAccounts{})
	svc.now = func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	room, err := svc.Create(ctx, "home", "host", models.WatchRoomCreate{
		Title: "Show", MediaType: "series", ItemID: "tmdb:tv:1", InviteeProfileIDs: []string{"guest"},
		Episode:      &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 1, EpisodeID: " ep1 ", Title: "Pilot"},
		Capabilities: supportedCapabilities,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := svc.Join(ctx, room.ID, "guest", "guest-client", supportedCapabilities); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateState(ctx, room.ID, "host", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPlaying, Position: 100}); err != nil {
		t.Fatal(err)
	}
	repo.room.WaitingForReady = false
	for id, m := range repo.members {
		m.Ready = true
		repo.members[id] = m
	}
	room, _ = svc.Get(ctx, room.ID, "host")
	return svc, repo, room
}

func TestCreateStoresEpisode(t *testing.T) {
	_, repo, room := newEpisodeRoom(t)
	if room.Episode == nil || room.Episode.Key() != "s1e1" || room.Episode.EpisodeID != "ep1" || room.Episode.Title != "Pilot" {
		t.Fatalf("room episode = %#v", room.Episode)
	}
	if repo.room.Episode == nil {
		t.Fatal("episode not persisted")
	}

	svc := New(&fakeRoomRepo{}, fakeProfiles{"host": {ID: "host", AccountID: "home"}}, fakeAccounts{})
	for _, ep := range []models.WatchRoomEpisode{{SeasonNumber: -1, EpisodeNumber: 1}, {SeasonNumber: 1, EpisodeNumber: 0}} {
		ep := ep
		_, err := svc.Create(context.Background(), "home", "host", models.WatchRoomCreate{Title: "Show", MediaType: "series", ItemID: "x", Episode: &ep, Capabilities: supportedCapabilities})
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Create(%#v) err = %v, want %v", ep, err, ErrInvalidState)
		}
	}
}

func TestHostEpisodeChangeResetsReadiness(t *testing.T) {
	svc, repo, room := newEpisodeRoom(t)
	before := room.Revision
	updated, err := svc.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{
		Status: models.WatchRoomStatusPlaying, Episode: &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 2},
	})
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if updated.Episode.Key() != "s1e2" || updated.Revision != before+1 || !updated.WaitingForReady || updated.Position != 0 {
		t.Fatalf("after change: episode=%#v revision=%d waiting=%v position=%v", updated.Episode, updated.Revision, updated.WaitingForReady, updated.Position)
	}
	for _, m := range repo.members {
		if m.Ready {
			t.Fatalf("member %s still ready", m.ProfileID)
		}
	}
}

func TestGuestCannotChangeEpisode(t *testing.T) {
	svc, repo, room := newEpisodeRoom(t)
	_, err := svc.UpdateState(context.Background(), room.ID, "guest", models.WatchRoomStateUpdate{
		Status: models.WatchRoomStatusPlaying, Episode: &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 2},
	})
	if !errors.Is(err, ErrEpisodeHostOnly) {
		t.Fatalf("guest change err = %v, want %v", err, ErrEpisodeHostOnly)
	}
	if repo.room.Episode.Key() != "s1e1" || repo.room.Revision != room.Revision {
		t.Fatalf("guest change mutated room: %#v rev %d", repo.room.Episode, repo.room.Revision)
	}
	// The guest echoing the current episode (different title) or none is fine.
	for _, ep := range []*models.WatchRoomEpisode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "other"}, nil} {
		got, err := svc.UpdateState(context.Background(), room.ID, "guest", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPaused, Position: 120, Episode: ep})
		if err != nil {
			t.Fatalf("guest same-episode update err = %v", err)
		}
		if got.WaitingForReady || got.Episode.Title != "Pilot" {
			t.Fatalf("same episode triggered change: %#v waiting=%v", got.Episode, got.WaitingForReady)
		}
	}
	if !repo.members["guest"].Ready {
		t.Fatal("same-episode update reset readiness")
	}
	if _, err := svc.UpdateState(context.Background(), room.ID, "host", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPaused, Episode: &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 0}}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid episode err = %v", err)
	}
}

func TestExternalSourceFollowsEpisode(t *testing.T) {
	svc, _, room := newEpisodeRoom(t)
	ctx := context.Background()
	if _, err := svc.BindExternalSource(ctx, "home", "host", room.ID, "/show/e1.mkv", nil, nil); err != nil {
		t.Fatalf("bind e1: %v", err)
	}
	invite, err := svc.CreateExternalInvitation(ctx, "home", "host", room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.JoinExternalGuest(ctx, invite, "web", "Web", "browser", models.WatchRoomClientCapabilities{StateSync: true, ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateState(ctx, room.ID, "host", models.WatchRoomStateUpdate{Status: models.WatchRoomStatusPlaying, Episode: &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 2}}); err != nil {
		t.Fatal(err)
	}
	// Until the host binds episode 2 the old source is stale: not served, and
	// the guest cannot report ready on it.
	if _, err := svc.ExternalSourceForGuest(ctx, room.ID, "web"); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("stale source err = %v", err)
	}
	if err := svc.SetExternalGuestReady(ctx, room.ID, "web", true); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("ready on stale source err = %v", err)
	}
	source, err := svc.BindExternalSource(ctx, "home", "host", room.ID, "/show/e2.mkv", nil, nil)
	if err != nil || source.Resource != "/show/e2.mkv" || source.EpisodeKey != "s1e2" {
		t.Fatalf("rebind = %#v, %v", source, err)
	}
	if _, err := svc.BindExternalSource(ctx, "home", "host", room.ID, "/show/other.mkv", nil, nil); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("same-episode rebind err = %v", err)
	}
	if got, err := svc.ExternalSourceForGuest(ctx, room.ID, "web"); err != nil || got.Resource != "/show/e2.mkv" {
		t.Fatalf("guest source = %#v, %v", got, err)
	}
	if err := svc.SetExternalGuestReady(ctx, room.ID, "web", true); err != nil {
		t.Fatalf("ready: %v", err)
	}
	// Binding ahead of the state update (explicit episode) also works.
	if _, err := svc.BindExternalSource(ctx, "home", "host", room.ID, "/show/e3.mkv", nil, &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 3}); err != nil {
		t.Fatalf("bind ahead: %v", err)
	}
}

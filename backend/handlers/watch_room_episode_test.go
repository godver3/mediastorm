package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/sessions"
	"novastream/services/watchrooms"
)

type episodeProfiles map[string]models.User

func (p episodeProfiles) Get(id string) (models.User, bool) { u, ok := p[id]; return u, ok }

type episodeRoomRepo struct {
	datastore.WatchRoomRepository
	room   *models.WatchRoom
	source *models.WatchRoomExternalSource
	guest  string
}

func (r *episodeRoomRepo) Create(_ context.Context, room *models.WatchRoom, _ []string, _ string, _ models.WatchRoomClientCapabilities) error {
	copy := *room
	r.room = &copy
	return nil
}
func (r *episodeRoomRepo) Get(context.Context, string) (*models.WatchRoom, error) {
	copy := *r.room
	return &copy, nil
}
func (r *episodeRoomRepo) IsInvited(context.Context, string, string) (bool, error) { return true, nil }
func (r *episodeRoomRepo) IsExternalGuest(_ context.Context, _, guest string) (bool, error) {
	return guest == r.guest, nil
}
func (r *episodeRoomRepo) HeartbeatExternalGuest(context.Context, string, string, string, bool, time.Time) error {
	return nil
}
func (r *episodeRoomRepo) GetExternalSource(context.Context, string) (*models.WatchRoomExternalSource, error) {
	return r.source, nil
}

func TestCreateWatchRoomAcceptsEpisode(t *testing.T) {
	repo := &episodeRoomRepo{}
	svc := watchrooms.New(repo, episodeProfiles{"host": {ID: "host", AccountID: "home"}}, nil)
	body := `{"title":"Show","mediaType":"series","itemId":"tmdb:tv:1","params":{},"inviteeProfileIds":[],
		"episode":{"seasonNumber":2,"episodeNumber":5,"episodeId":"tmdb:ep:9","title":"Five"},
		"capabilities":{"nativePlayback":true,"stateSync":true,"protocolVersion":1}}`
	req := httptest.NewRequest(http.MethodPost, "/api/users/host/watch-rooms", strings.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"userID": "host"})
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "home"))
	rec := httptest.NewRecorder()
	NewWatchRoomsHandler(svc).Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var room map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &room)
	ep, _ := room["episode"].(map[string]any)
	if ep["seasonNumber"] != float64(2) || ep["episodeNumber"] != float64(5) || ep["episodeId"] != "tmdb:ep:9" || ep["title"] != "Five" {
		t.Fatalf("episode = %#v", room["episode"])
	}
}

func TestWatchPartySnapshotExposesEpisodeAndSource(t *testing.T) {
	sessionService, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := sessionService.CreateScopedWithResource("family", false, "browser", "192.0.2.1", time.Hour, models.SessionScopeWatchParty, "room")
	if err != nil {
		t.Fatal(err)
	}
	boundAt := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	episode := &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 2}
	repo := &episodeRoomRepo{
		room:   &models.WatchRoom{ID: "room", Status: models.WatchRoomStatusPaused, Episode: episode, ExpiresAt: time.Now().Add(time.Hour)},
		source: &models.WatchRoomExternalSource{RoomID: "room", Resource: "/e2.mkv", EpisodeKey: "s1e2", BoundAt: boundAt},
		guest:  watchPartyGuestID(guest.Token),
	}
	handler := NewWatchPartyHandler(watchrooms.New(repo, nil, nil), sessionService, "")
	call := func(fn http.HandlerFunc) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = mux.SetURLVars(req, map[string]string{"roomID": "room"})
		req.AddCookie(&http.Cookie{Name: watchPartyCookieName, Value: guest.Token})
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}
	rec := call(handler.RoomSnapshot)
	var view watchPartyRoomView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("snapshot %d: %s", rec.Code, rec.Body.String())
	}
	if !view.PlaybackReady || view.SourceBoundAt == nil || !view.SourceBoundAt.Equal(boundAt) || view.Episode.Key() != "s1e2" {
		t.Fatalf("view = %#v", view)
	}
	// A paused room can still hand out the (new episode's) grant.
	if rec := call(handler.PlaybackGrant); rec.Code != http.StatusOK {
		t.Fatalf("paused grant status = %d: %s", rec.Code, rec.Body.String())
	}
	// Host moved on but has not bound the new source yet: not ready, no boundAt.
	repo.room.Episode = &models.WatchRoomEpisode{SeasonNumber: 1, EpisodeNumber: 3}
	rec = call(handler.RoomSnapshot)
	view = watchPartyRoomView{}
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.PlaybackReady || view.SourceBoundAt != nil {
		t.Fatalf("stale source view = %#v", view)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/sessions"
	"novastream/services/watchrooms"
)

type playbackAuthorityRepo struct {
	datastore.WatchRoomRepository
	room    *models.WatchRoom
	guestID string
}

func (r *playbackAuthorityRepo) Get(context.Context, string) (*models.WatchRoom, error) {
	return r.room, nil
}
func (r *playbackAuthorityRepo) IsExternalGuest(_ context.Context, room, guest string) (bool, error) {
	return room == r.room.ID && guest == r.guestID, nil
}
func (r *playbackAuthorityRepo) GetExternalSource(context.Context, string) (*models.WatchRoomExternalSource, error) {
	return &models.WatchRoomExternalSource{RoomID: r.room.ID, Resource: "/movie.mkv"}, nil
}

func TestWatchPartyPlaybackGrantExpiresWithItsRoom(t *testing.T) {
	sessionService, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := sessionService.CreateScopedWithResource("family", false, "browser", "192.0.2.1", time.Hour, models.SessionScopeWatchParty, "room")
	if err != nil {
		t.Fatal(err)
	}
	repo := &playbackAuthorityRepo{room: &models.WatchRoom{ID: "room", Status: models.WatchRoomStatusPlaying, ExpiresAt: time.Now().Add(time.Hour)}, guestID: watchPartyGuestID(guest.Token)}
	roomService := watchrooms.New(repo, nil, nil)
	sessionService.SetWatchPartyPlaybackValidator(roomService.ValidateExternalPlayback)
	handler := NewWatchPartyHandler(roomService, sessionService, "")
	request := httptest.NewRequest(http.MethodGet, "/api/watch-party/rooms/room/playback", nil)
	request = mux.SetURLVars(request, map[string]string{"roomID": "room"})
	request.AddCookie(&http.Cookie{Name: watchPartyCookieName, Value: guest.Token})
	response := httptest.NewRecorder()
	handler.PlaybackGrant(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("grant status = %d", response.Code)
	}
	var body map[string]string
	if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	playback, err := url.Parse(body["url"])
	if err != nil {
		t.Fatal(err)
	}
	token := playback.Query().Get("token")
	before, err := sessionService.Validate(token)
	if err != nil || before.Scope != models.SessionScopeStream || before.ScopeResource != "/movie.mkv" {
		t.Fatal("live room grant lost its source")
	}
	repo.room.Status = models.WatchRoomStatusEnded
	if _, err = sessionService.Validate(token); !errors.Is(err, sessions.ErrSessionNotFound) {
		t.Fatal("issued playback token survived room end")
	}
}

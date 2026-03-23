package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/internal/auth"
	"novastream/models"

	"github.com/gorilla/mux"
)

type fakeLocalMediaPlaybackService struct {
	item *models.LocalMediaItem
	err  error
}

func (f *fakeLocalMediaPlaybackService) GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error) {
	return f.item, f.err
}

type fakeLocalMediaUsersProvider struct {
	allowed bool
}

func (f fakeLocalMediaUsersProvider) BelongsToAccount(profileID, accountID string) bool {
	return f.allowed
}

func TestLocalMediaHandlerGetPlayback(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{
			ID:            "item1",
			FileName:      "Movie.Title.2024.mkv",
			FilePath:      "/srv/media/Movie.Title.2024.mkv",
			MatchedName:   "Movie Title",
			DetectedTitle: "Movie Title",
		},
	}, fakeLocalMediaUsersProvider{allowed: true}, true)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items/item1/playback?profileId=user1", nil)
	req = mux.SetURLVars(req, map[string]string{"itemID": "item1"})
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.GetPlayback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp models.LocalMediaPlaybackResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StreamPath != "localmedia:item1/Movie.Title.2024.mkv" {
		t.Fatalf("StreamPath = %q", resp.StreamPath)
	}
	if !strings.Contains(resp.StreamURL, "/api/video/stream?") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if !strings.Contains(resp.StreamURL, "path=localmedia%3Aitem1%2FMovie.Title.2024.mkv") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if !strings.Contains(resp.StreamURL, "profileId=user1") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if resp.HLSStartURL == "" || !resp.HLSAvailable {
		t.Fatalf("expected HLS response, got %+v", resp)
	}
}

func TestLocalMediaHandlerGetPlaybackRejectsForeignProfile(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{ID: "item1", FileName: "Movie.mkv"},
	}, fakeLocalMediaUsersProvider{allowed: false}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items/item1/playback?profileId=user1", nil)
	req = mux.SetURLVars(req, map[string]string{"itemID": "item1"})
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.GetPlayback(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

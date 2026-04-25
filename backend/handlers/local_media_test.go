package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"novastream/internal/auth"
	"novastream/models"

	"github.com/gorilla/mux"
)

type fakeLocalMediaPlaybackService struct {
	item      *models.LocalMediaItem
	libraries []models.LocalMediaLibrary
	groups    *models.LocalMediaGroupListResult
	matches   []models.LocalMediaMatchedGroup
	err       error
}

func (f *fakeLocalMediaPlaybackService) GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error) {
	return f.item, f.err
}

func (f *fakeLocalMediaPlaybackService) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	return f.libraries, f.err
}

func (f *fakeLocalMediaPlaybackService) ListGroups(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaGroupListResult, error) {
	return f.groups, f.err
}

func (f *fakeLocalMediaPlaybackService) FindMatches(ctx context.Context, query models.LocalMediaMatchQuery) ([]models.LocalMediaMatchedGroup, error) {
	return f.matches, f.err
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
	if !strings.Contains(resp.StreamURL, "transmux=0") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if !strings.Contains(resp.StreamURL, "profileId=user1") {
		t.Fatalf("StreamURL = %q", resp.StreamURL)
	}
	if resp.HLSStartURL == "" || !resp.HLSAvailable {
		t.Fatalf("expected HLS response, got %+v", resp)
	}
}

func TestLocalMediaHandlerGetPlaybackEpisodeUsesEpisodeItemID(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		item: &models.LocalMediaItem{
			ID:               "item1",
			FileName:         "Show.S01E02.mkv",
			FilePath:         "/srv/media/Show.S01E02.mkv",
			LibraryType:      models.LocalMediaLibraryTypeShow,
			MatchedTitleID:   "tvdb:12345",
			MatchedMediaType: "series",
			MatchedName:      "Example Show",
			SeasonNumber:     1,
			EpisodeNumber:    2,
			EpisodeTitle:     "Second Episode",
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

	parsed, err := url.Parse(resp.StreamURL)
	if err != nil {
		t.Fatalf("parse StreamURL %q: %v", resp.StreamURL, err)
	}
	query := parsed.Query()
	if got, want := query.Get("itemId"), "tvdb:12345:s01e02"; got != want {
		t.Fatalf("StreamURL itemId = %q, want %q", got, want)
	}
	if got := query.Get("mediaType"); got != "episode" {
		t.Fatalf("StreamURL mediaType = %q, want episode", got)
	}
	if got := query.Get("seriesName"); got != "Example Show" {
		t.Fatalf("StreamURL seriesName = %q, want Example Show", got)
	}
	if got := query.Get("episodeName"); got != "Second Episode" {
		t.Fatalf("StreamURL episodeName = %q, want Second Episode", got)
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

func TestLocalMediaHandlerListLibraries(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		libraries: []models.LocalMediaLibrary{
			{ID: "lib1", Name: "My Movies", Type: models.LocalMediaLibraryTypeMovie, LastScanStatus: models.LocalMediaScanStatusComplete},
		},
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries", nil)
	rec := httptest.NewRecorder()
	handler.ListLibraries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var libraries []models.LocalMediaLibrary
	if err := json.NewDecoder(rec.Body).Decode(&libraries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(libraries) != 1 || libraries[0].ID != "lib1" {
		t.Fatalf("unexpected libraries: %+v", libraries)
	}
}

func TestLocalMediaHandlerListLibrariesEmpty(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		libraries: nil,
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries", nil)
	rec := httptest.NewRecorder()
	handler.ListLibraries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var libraries []models.LocalMediaLibrary
	if err := json.NewDecoder(rec.Body).Decode(&libraries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if libraries == nil {
		t.Fatal("expected empty slice, got nil")
	}
}

func TestLocalMediaHandlerListGroups(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		groups: &models.LocalMediaGroupListResult{
			Groups: []models.LocalMediaItemGroup{
				{ID: "g1", Title: "Inception", LibraryType: models.LocalMediaLibraryTypeMovie, ItemCount: 1},
			},
			Total: 1,
			Limit: 50,
		},
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/libraries/lib1/groups", nil)
	req = mux.SetURLVars(req, map[string]string{"libraryID": "lib1"})
	rec := httptest.NewRecorder()
	handler.ListGroups(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var result models.LocalMediaGroupListResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Groups) != 1 || result.Groups[0].ID != "g1" {
		t.Fatalf("unexpected groups: %+v", result)
	}
}

func TestLocalMediaHandlerFindMatches(t *testing.T) {
	handler := NewLocalMediaHandler(&fakeLocalMediaPlaybackService{
		matches: []models.LocalMediaMatchedGroup{
			{
				LibraryID:   "lib1",
				LibraryName: "Movies",
				LibraryType: models.LocalMediaLibraryTypeMovie,
				Group: models.LocalMediaItemGroup{
					ID:    "movie:tmdb:movie:123",
					Title: "Inception",
				},
			},
		},
	}, fakeLocalMediaUsersProvider{}, false)

	req := httptest.NewRequest(http.MethodGet, "/api/library/matches?mediaType=movie&tmdbId=123&title=Inception&year=2010", nil)
	rec := httptest.NewRecorder()
	handler.FindMatches(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var matches []models.LocalMediaMatchedGroup
	if err := json.NewDecoder(rec.Body).Decode(&matches); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(matches) != 1 || matches[0].LibraryID != "lib1" || matches[0].Group.ID != "movie:tmdb:movie:123" {
		t.Fatalf("unexpected matches: %+v", matches)
	}
}

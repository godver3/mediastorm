package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/handlers"
	"novastream/models"
)

type fakeHistoryService struct {
	state        models.SeriesWatchState
	items        []models.SeriesWatchState
	watchItems   []models.WatchHistoryItem
	revision     string
	err          error
	hideUserID   string
	hideSeriesID string
}

func (f *fakeHistoryService) RecordEpisode(userID string, payload models.EpisodeWatchPayload) (models.SeriesWatchState, error) {
	return f.state, f.err
}

func (f *fakeHistoryService) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	return f.items, f.err
}

func (f *fakeHistoryService) GetContinueWatchingRevision(userID string) (string, error) {
	return f.revision, f.err
}

func (f *fakeHistoryService) ListSeriesStates(userID string) ([]models.SeriesWatchState, error) {
	return f.items, f.err
}

func (f *fakeHistoryService) GetSeriesWatchState(userID, seriesID string) (*models.SeriesWatchState, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &f.state, nil
}

func (f *fakeHistoryService) ListWatchHistory(userID string) ([]models.WatchHistoryItem, error) {
	return f.watchItems, f.err
}

func (f *fakeHistoryService) GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error) {
	return nil, f.err
}

func (f *fakeHistoryService) ToggleWatched(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	return models.WatchHistoryItem{}, f.err
}

func (f *fakeHistoryService) UpdateWatchHistory(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	return models.WatchHistoryItem{}, f.err
}

func (f *fakeHistoryService) BulkUpdateWatchHistory(userID string, updates []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error) {
	return nil, f.err
}

func (f *fakeHistoryService) IsWatched(userID, mediaType, itemID string) (bool, error) {
	return false, f.err
}

func (f *fakeHistoryService) UpdatePlaybackProgress(userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return models.PlaybackProgress{}, f.err
}

func (f *fakeHistoryService) GetPlaybackProgress(userID, mediaType, itemID string) (*models.PlaybackProgress, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &models.PlaybackProgress{}, nil
}

func (f *fakeHistoryService) ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error) {
	return nil, f.err
}

func (f *fakeHistoryService) DeletePlaybackProgress(userID, mediaType, itemID string) error {
	return f.err
}

func (f *fakeHistoryService) ListAllPlaybackProgress() map[string][]models.PlaybackProgress {
	return nil
}

func TestListWatchHistoryStatusProjection(t *testing.T) {
	handler := handlers.NewHistoryHandler(&fakeHistoryService{
		watchItems: []models.WatchHistoryItem{
			{
				ID:          "movie:tmdb:1",
				MediaType:   "movie",
				ItemID:      "tmdb:1",
				Name:        "Heavy Movie",
				Watched:     true,
				ExternalIDs: map[string]string{"tmdb": "1", "imdb": "tt1"},
			},
			{
				ID:         "episode:tvdb:2:s01e01",
				MediaType:  "episode",
				ItemID:     "tvdb:2:s01e01",
				Name:       "Heavy Episode",
				SeriesName: "Heavy Show",
				Watched:    false,
			},
		},
	}, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/history/watched?fields=status", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	handler.ListWatchHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var items []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0]["mediaType"] != "movie" || items[0]["itemId"] != "tmdb:1" || items[0]["watched"] != true {
		t.Fatalf("unexpected projected item: %+v", items[0])
	}
	if _, ok := items[0]["name"]; ok {
		t.Fatalf("status projection included name: %+v", items[0])
	}
	if _, ok := items[0]["externalIds"]; ok {
		t.Fatalf("status projection included externalIds: %+v", items[0])
	}
}

func TestListWatchHistoryStatusKeysProjection(t *testing.T) {
	handler := handlers.NewHistoryHandler(&fakeHistoryService{
		watchItems: []models.WatchHistoryItem{
			{
				ID:        "movie:tmdb:1",
				MediaType: "movie",
				ItemID:    "tmdb:1",
				Name:      "Heavy Movie",
				Watched:   true,
			},
			{
				ID:         "episode:tvdb:2:s01e01",
				MediaType:  "episode",
				ItemID:     "tvdb:2:s01e01",
				Name:       "Heavy Episode",
				SeriesName: "Heavy Show",
				Watched:    false,
			},
			{
				ID:        "series:TVDB:3",
				MediaType: "series",
				ItemID:    "TVDB:3",
				Name:      "Heavy Show",
				Watched:   true,
			},
		},
	}, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/history/watched?fields=status&format=keys", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	handler.ListWatchHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Watched []string `json:"watched"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := []string{"movie:tmdb:1", "series:tvdb:3"}
	if len(body.Watched) != len(want) {
		t.Fatalf("len(watched) = %d, want %d (%+v)", len(body.Watched), len(want), body.Watched)
	}
	for i := range want {
		if body.Watched[i] != want[i] {
			t.Fatalf("watched[%d] = %q, want %q", i, body.Watched[i], want[i])
		}
	}
}

func (f *fakeHistoryService) HideFromContinueWatching(userID, seriesID string) error {
	f.hideUserID = userID
	f.hideSeriesID = seriesID
	return f.err
}

type fakeUserService struct{}

func (fakeUserService) Exists(id string) bool { return true }

func TestHistoryHandler_RecordEpisode(t *testing.T) {
	svc := &fakeHistoryService{
		state: models.SeriesWatchState{
			SeriesID:    "s1",
			SeriesTitle: "Show",
			UpdatedAt:   time.Now().UTC(),
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
		},
	}
	handler := handlers.NewHistoryHandler(svc, fakeUserService{}, false)

	payload := models.EpisodeWatchPayload{
		SeriesID: "s1",
		Episode:  models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/users/user/history/episodes", bytes.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"userID": "user"})
	rec := httptest.NewRecorder()

	handler.RecordEpisode(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}

	var response models.SeriesWatchState
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.SeriesID != "s1" {
		t.Fatalf("unexpected series id %q", response.SeriesID)
	}
}

func TestHistoryHandler_ListContinueWatching(t *testing.T) {
	expected := []models.SeriesWatchState{{SeriesID: "s1"}}
	svc := &fakeHistoryService{items: expected}
	handler := handlers.NewHistoryHandler(svc, fakeUserService{}, false)

	req := httptest.NewRequest(http.MethodGet, "/users/user/history/continue", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user"})
	rec := httptest.NewRecorder()

	handler.ListContinueWatching(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}

	var response []models.SeriesWatchState
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(response) != 1 || response[0].SeriesID != "s1" {
		t.Fatalf("unexpected response %+v", response)
	}
}

func TestHistoryHandler_GetContinueWatchingRevision(t *testing.T) {
	svc := &fakeHistoryService{revision: "wh:2:123|pp:3:1:456"}
	handler := handlers.NewHistoryHandler(svc, fakeUserService{}, false)

	req := httptest.NewRequest(http.MethodGet, "/users/user/history/continue/revision", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user"})
	rec := httptest.NewRecorder()

	handler.GetContinueWatchingRevision(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}

	var response struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Revision != "wh:2:123|pp:3:1:456" {
		t.Fatalf("unexpected revision %q", response.Revision)
	}
}

func TestHistoryHandler_HideFromContinueWatchingByBody(t *testing.T) {
	svc := &fakeHistoryService{}
	handler := handlers.NewHistoryHandler(svc, fakeUserService{}, false)

	body := bytes.NewBufferString(`{"seriesId":"localmedia:folder/file.mkv"}`)
	req := httptest.NewRequest(http.MethodPost, "/users/user/history/continue/hide", body)
	req = mux.SetURLVars(req, map[string]string{"userID": "user"})
	rec := httptest.NewRecorder()

	handler.HideFromContinueWatchingByBody(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if svc.hideUserID != "user" {
		t.Fatalf("unexpected user id %q", svc.hideUserID)
	}
	if svc.hideSeriesID != "localmedia:folder/file.mkv" {
		t.Fatalf("unexpected series id %q", svc.hideSeriesID)
	}
}

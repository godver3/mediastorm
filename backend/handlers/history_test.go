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
	state models.SeriesWatchState
	items []models.SeriesWatchState
	err   error
}

func (f *fakeHistoryService) RecordEpisode(userID string, payload models.EpisodeWatchPayload) (models.SeriesWatchState, error) {
	return f.state, f.err
}

func (f *fakeHistoryService) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	return f.items, f.err
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
	return nil, f.err
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

func (f *fakeHistoryService) HideFromContinueWatching(userID, seriesID string) error {
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

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
	metadatapkg "novastream/services/metadata"

	"github.com/gorilla/mux"
)

// --- mock services ---

type mockUserSettingsService struct {
	settings    *models.UserSettings
	withDefault models.UserSettings
	err         error
}

func (m *mockUserSettingsService) Get(userID string) (*models.UserSettings, error) {
	return m.settings, m.err
}
func (m *mockUserSettingsService) GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error) {
	if m.err != nil {
		return models.UserSettings{}, m.err
	}
	return m.withDefault, nil
}
func (m *mockUserSettingsService) Update(userID string, settings models.UserSettings) error {
	return nil
}
func (m *mockUserSettingsService) Delete(userID string) error { return nil }

type mockWatchlistService struct {
	items []models.WatchlistItem
	err   error
}

func (m *mockWatchlistService) List(userID string) ([]models.WatchlistItem, error) {
	return m.items, m.err
}
func (m *mockWatchlistService) AddOrUpdate(userID string, input models.WatchlistUpsert) (models.WatchlistItem, error) {
	return models.WatchlistItem{}, nil
}
func (m *mockWatchlistService) UpdateState(userID, mediaType, id string, watched *bool, progress interface{}) (models.WatchlistItem, error) {
	return models.WatchlistItem{}, nil
}
func (m *mockWatchlistService) Remove(userID, mediaType, id string) (bool, error) {
	return false, nil
}

type mockHistoryService struct {
	continueWatching []models.SeriesWatchState
	playbackProgress []models.PlaybackProgress
	watchHistory     []models.WatchHistoryItem
	cwErr            error
	ppErr            error
	whErr            error
}

func (m *mockHistoryService) RecordEpisode(userID string, payload models.EpisodeWatchPayload) (models.SeriesWatchState, error) {
	return models.SeriesWatchState{}, nil
}
func (m *mockHistoryService) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	return m.continueWatching, m.cwErr
}
func (m *mockHistoryService) GetSeriesWatchState(userID, seriesID string) (*models.SeriesWatchState, error) {
	return nil, nil
}
func (m *mockHistoryService) HideFromContinueWatching(userID, seriesID string) error { return nil }
func (m *mockHistoryService) ListWatchHistory(userID string) ([]models.WatchHistoryItem, error) {
	return m.watchHistory, m.whErr
}
func (m *mockHistoryService) GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error) {
	return nil, nil
}
func (m *mockHistoryService) ToggleWatched(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	return models.WatchHistoryItem{}, nil
}
func (m *mockHistoryService) UpdateWatchHistory(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	return models.WatchHistoryItem{}, nil
}
func (m *mockHistoryService) BulkUpdateWatchHistory(userID string, updates []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error) {
	return nil, nil
}
func (m *mockHistoryService) IsWatched(userID, mediaType, itemID string) (bool, error) {
	return false, nil
}
func (m *mockHistoryService) UpdatePlaybackProgress(userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return models.PlaybackProgress{}, nil
}
func (m *mockHistoryService) GetPlaybackProgress(userID, mediaType, itemID string) (*models.PlaybackProgress, error) {
	return nil, nil
}
func (m *mockHistoryService) ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error) {
	return m.playbackProgress, m.ppErr
}
func (m *mockHistoryService) DeletePlaybackProgress(userID, mediaType, itemID string) error {
	return nil
}
func (m *mockHistoryService) ListAllPlaybackProgress() map[string][]models.PlaybackProgress {
	return nil
}

type mockMetadataServiceStartup struct {
	movieItems  []models.TrendingItem
	seriesItems []models.TrendingItem
	movieErr    error
	seriesErr   error
}

func (m *mockMetadataServiceStartup) Trending(ctx context.Context, mediaType string) ([]models.TrendingItem, error) {
	if mediaType == "movie" {
		return m.movieItems, m.movieErr
	}
	return m.seriesItems, m.seriesErr
}

// Stub methods to satisfy metadataService interface
func (m *mockMetadataServiceStartup) Search(context.Context, string, string) ([]models.SearchResult, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) SeriesDetails(context.Context, models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) BatchSeriesDetails(context.Context, []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem {
	return nil
}
func (m *mockMetadataServiceStartup) MovieDetails(context.Context, models.MovieDetailsQuery) (*models.Title, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) BatchMovieReleases(context.Context, []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem {
	return nil
}
func (m *mockMetadataServiceStartup) CollectionDetails(context.Context, int64) (*models.CollectionDetails, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) Similar(context.Context, string, int64) ([]models.Title, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) PersonDetails(context.Context, int64) (*models.PersonDetails, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) Trailers(context.Context, models.TrailerQuery) (*models.TrailerResponse, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) ExtractTrailerStreamURL(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockMetadataServiceStartup) StreamTrailer(_ context.Context, _ string, _ io.Writer) error {
	return nil
}
func (m *mockMetadataServiceStartup) StreamTrailerWithRange(_ context.Context, _ string, _ string, _ io.Writer) error {
	return nil
}
func (m *mockMetadataServiceStartup) GetCustomList(_ context.Context, _ string, _ metadatapkg.CustomListOptions) ([]models.TrendingItem, int, int, error) {
	return nil, 0, 0, nil
}
func (m *mockMetadataServiceStartup) PrequeueTrailer(_ string) (string, error) {
	return "", nil
}
func (m *mockMetadataServiceStartup) GetTrailerPrequeueStatus(_ string) (*metadatapkg.TrailerPrequeueItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceStartup) ServePrequeuedTrailer(_ string, _ http.ResponseWriter, _ *http.Request) error {
	return nil
}

type mockUserServiceStartup struct {
	exists bool
}

func (m *mockUserServiceStartup) Exists(id string) bool { return m.exists }

// --- tests ---

func TestStartupHandler_Success(t *testing.T) {
	cfgManager := config.NewManager(t.TempDir() + "/settings.json")

	h := handlers.NewStartupHandler(
		&mockUserSettingsService{
			withDefault: models.UserSettings{
				Playback: models.PlaybackSettings{PreferredPlayer: "native"},
			},
		},
		&mockWatchlistService{
			items: []models.WatchlistItem{
				{ID: "m1", MediaType: "movie", Name: "Test Movie"},
			},
		},
		&mockHistoryService{
			continueWatching: []models.SeriesWatchState{
				{SeriesID: "s1", SeriesTitle: "Test Series"},
			},
			playbackProgress: []models.PlaybackProgress{
				{ID: "p1", MediaType: "movie", ItemID: "m1", Position: 100, Duration: 200},
			},
			watchHistory: []models.WatchHistoryItem{
				{ID: "wh1", MediaType: "movie", ItemID: "m1", Watched: true},
			},
		},
		&mockMetadataServiceStartup{
			movieItems: []models.TrendingItem{
				{Rank: 1, Title: models.Title{Name: "Trending Movie"}},
			},
			seriesItems: []models.TrendingItem{
				{Rank: 1, Title: models.Title{Name: "Trending Series"}},
			},
		},
		cfgManager,
		&mockUserServiceStartup{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/startup", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetStartup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Check all fields are present
	expectedFields := []string{
		"userSettings", "watchlist", "continueWatching",
		"watchHistory", "trendingMovies", "trendingSeries",
	}
	for _, field := range expectedFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing expected field %q in response", field)
		}
	}

	// Verify watchlist has 1 item
	var watchlist []models.WatchlistItem
	if err := json.Unmarshal(resp["watchlist"], &watchlist); err != nil {
		t.Fatalf("failed to decode watchlist: %v", err)
	}
	if len(watchlist) != 1 || watchlist[0].Name != "Test Movie" {
		t.Errorf("unexpected watchlist: %+v", watchlist)
	}
}

func TestStartupHandler_PartialFailure(t *testing.T) {
	cfgManager := config.NewManager(t.TempDir() + "/settings.json")

	h := handlers.NewStartupHandler(
		&mockUserSettingsService{err: errors.New("settings db error")},
		&mockWatchlistService{err: errors.New("watchlist db error")},
		&mockHistoryService{
			cwErr: errors.New("continue watching error"),
			ppErr: errors.New("progress error"),
			whErr: errors.New("history error"),
		},
		&mockMetadataServiceStartup{
			movieErr:  errors.New("trending movies error"),
			seriesErr: errors.New("trending series error"),
		},
		cfgManager,
		&mockUserServiceStartup{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/startup", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetStartup(rec, req)

	// Should still return 200 even if all services fail
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 despite errors, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// All fields should be present (with null/empty defaults)
	expectedFields := []string{
		"userSettings", "watchlist", "continueWatching",
		"watchHistory", "trendingMovies", "trendingSeries",
	}
	for _, field := range expectedFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing expected field %q in response", field)
		}
	}

	// Watchlist should be empty array, not null
	var watchlist []json.RawMessage
	if err := json.Unmarshal(resp["watchlist"], &watchlist); err != nil {
		t.Fatalf("watchlist should be a valid array: %v", err)
	}
	if len(watchlist) != 0 {
		t.Errorf("expected empty watchlist, got %d items", len(watchlist))
	}
}

func TestStartupHandler_UserNotFound(t *testing.T) {
	cfgManager := config.NewManager(t.TempDir() + "/settings.json")

	h := handlers.NewStartupHandler(
		&mockUserSettingsService{},
		&mockWatchlistService{},
		&mockHistoryService{},
		&mockMetadataServiceStartup{},
		cfgManager,
		&mockUserServiceStartup{exists: false},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/users/unknown/startup", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "unknown"})
	rec := httptest.NewRecorder()

	h.GetStartup(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestStartupHandler_EmptyUserID(t *testing.T) {
	cfgManager := config.NewManager(t.TempDir() + "/settings.json")

	h := handlers.NewStartupHandler(
		&mockUserSettingsService{},
		&mockWatchlistService{},
		&mockHistoryService{},
		&mockMetadataServiceStartup{},
		cfgManager,
		&mockUserServiceStartup{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/users//startup", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": ""})
	rec := httptest.NewRecorder()

	h.GetStartup(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

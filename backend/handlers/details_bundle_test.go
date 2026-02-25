package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/handlers"
	"novastream/models"
	metadatapkg "novastream/services/metadata"

	"github.com/gorilla/mux"
)

// --- mock services for details bundle ---

type mockMetadataServiceDetailsBundle struct {
	seriesDetails *models.SeriesDetails
	movieDetails  *models.Title
	similar       []models.Title
	trailers      *models.TrailerResponse

	seriesDetailsErr error
	movieDetailsErr  error
	similarErr       error
	trailersErr      error
}

func (m *mockMetadataServiceDetailsBundle) SeriesDetails(_ context.Context, _ models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return m.seriesDetails, m.seriesDetailsErr
}
func (m *mockMetadataServiceDetailsBundle) MovieDetails(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	return m.movieDetails, m.movieDetailsErr
}
func (m *mockMetadataServiceDetailsBundle) Similar(_ context.Context, _ string, _ int64) ([]models.Title, error) {
	return m.similar, m.similarErr
}
func (m *mockMetadataServiceDetailsBundle) DiscoverByGenre(_ context.Context, _ string, _ int64, _, _ int) ([]models.TrendingItem, int, error) {
	return nil, 0, nil
}
func (m *mockMetadataServiceDetailsBundle) GetAIRecommendations(_ context.Context, _ []string, _ []string, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) GetAISimilar(_ context.Context, _ string, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) GetAICustomRecommendations(_ context.Context, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) GetAISurprise(_ context.Context, _ string, _ string) (*models.TrendingItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) Trailers(_ context.Context, _ models.TrailerQuery) (*models.TrailerResponse, error) {
	return m.trailers, m.trailersErr
}

// Stub remaining metadataService methods
func (m *mockMetadataServiceDetailsBundle) Trending(_ context.Context, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) Search(_ context.Context, _ string, _ string) ([]models.SearchResult, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) BatchSeriesDetails(_ context.Context, _ []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem {
	return nil
}
func (m *mockMetadataServiceDetailsBundle) BatchSeriesTitleFields(_ context.Context, _ []models.SeriesDetailsQuery, _ []string) []models.BatchSeriesDetailsItem {
	return nil
}
func (m *mockMetadataServiceDetailsBundle) BatchMovieReleases(_ context.Context, _ []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem {
	return nil
}
func (m *mockMetadataServiceDetailsBundle) CollectionDetails(_ context.Context, _ int64) (*models.CollectionDetails, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) PersonDetails(_ context.Context, _ int64) (*models.PersonDetails, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) ExtractTrailerStreamURL(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockMetadataServiceDetailsBundle) StreamTrailer(_ context.Context, _ string, _ io.Writer) error {
	return nil
}
func (m *mockMetadataServiceDetailsBundle) StreamTrailerWithRange(_ context.Context, _ string, _ string, _ io.Writer) error {
	return nil
}
func (m *mockMetadataServiceDetailsBundle) GetCustomList(_ context.Context, _ string, _ metadatapkg.CustomListOptions) ([]models.TrendingItem, int, int, error) {
	return nil, 0, 0, nil
}
func (m *mockMetadataServiceDetailsBundle) PrequeueTrailer(_ string) (string, error) {
	return "", nil
}
func (m *mockMetadataServiceDetailsBundle) GetTrailerPrequeueStatus(_ string) (*metadatapkg.TrailerPrequeueItem, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) ServePrequeuedTrailer(_ string, _ http.ResponseWriter, _ *http.Request) error {
	return nil
}

func (m *mockMetadataServiceDetailsBundle) EnrichSearchCertifications(_ context.Context, _ []models.SearchResult) {
}

func (m *mockMetadataServiceDetailsBundle) GetProgressSnapshot() metadatapkg.ProgressSnapshot {
	return metadatapkg.ProgressSnapshot{}
}

type mockHistoryServiceDetailsBundle struct {
	watchState       *models.SeriesWatchState
	playbackProgress []models.PlaybackProgress

	watchStateErr       error
	playbackProgressErr error
}

func (m *mockHistoryServiceDetailsBundle) GetSeriesWatchState(_, _ string) (*models.SeriesWatchState, error) {
	return m.watchState, m.watchStateErr
}
func (m *mockHistoryServiceDetailsBundle) ListPlaybackProgress(_ string) ([]models.PlaybackProgress, error) {
	return m.playbackProgress, m.playbackProgressErr
}

// Stub remaining historyService methods
func (m *mockHistoryServiceDetailsBundle) RecordEpisode(_ string, _ models.EpisodeWatchPayload) (models.SeriesWatchState, error) {
	return models.SeriesWatchState{}, nil
}
func (m *mockHistoryServiceDetailsBundle) ListContinueWatching(_ string) ([]models.SeriesWatchState, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) ListSeriesStates(_ string) ([]models.SeriesWatchState, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) HideFromContinueWatching(_, _ string) error { return nil }
func (m *mockHistoryServiceDetailsBundle) ListWatchHistory(_ string) ([]models.WatchHistoryItem, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) GetWatchHistoryItem(_, _, _ string) (*models.WatchHistoryItem, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) ToggleWatched(_ string, _ models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	return models.WatchHistoryItem{}, nil
}
func (m *mockHistoryServiceDetailsBundle) UpdateWatchHistory(_ string, _ models.WatchHistoryUpdate) (models.WatchHistoryItem, error) {
	return models.WatchHistoryItem{}, nil
}
func (m *mockHistoryServiceDetailsBundle) BulkUpdateWatchHistory(_ string, _ []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) IsWatched(_, _, _ string) (bool, error) {
	return false, nil
}
func (m *mockHistoryServiceDetailsBundle) UpdatePlaybackProgress(_ string, _ models.PlaybackProgressUpdate) (models.PlaybackProgress, error) {
	return models.PlaybackProgress{}, nil
}
func (m *mockHistoryServiceDetailsBundle) GetPlaybackProgress(_, _, _ string) (*models.PlaybackProgress, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) DeletePlaybackProgress(_, _, _ string) error { return nil }
func (m *mockHistoryServiceDetailsBundle) ListAllPlaybackProgress() map[string][]models.PlaybackProgress {
	return nil
}

type mockContentPrefsServiceDetailsBundle struct {
	pref *models.ContentPreference
	err  error
}

func (m *mockContentPrefsServiceDetailsBundle) Get(_, _ string) (*models.ContentPreference, error) {
	return m.pref, m.err
}
func (m *mockContentPrefsServiceDetailsBundle) Set(_ string, _ models.ContentPreference) error {
	return nil
}
func (m *mockContentPrefsServiceDetailsBundle) Delete(_, _ string) error { return nil }
func (m *mockContentPrefsServiceDetailsBundle) List(_ string) ([]models.ContentPreference, error) {
	return nil, nil
}

type mockUserServiceDetailsBundle struct {
	exists bool
}

func (m *mockUserServiceDetailsBundle) Exists(_ string) bool { return m.exists }

// --- tests ---

func TestDetailsBundleHandler_Series(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			seriesDetails: &models.SeriesDetails{
				Title: models.Title{Name: "Test Series", ID: "tvdb:series:123"},
			},
			similar: []models.Title{
				{Name: "Similar Show", ID: "similar1"},
			},
			trailers: &models.TrailerResponse{
				Trailers: []models.Trailer{{Name: "Trailer 1", URL: "https://example.com/trailer"}},
			},
		},
		&mockHistoryServiceDetailsBundle{
			watchState: &models.SeriesWatchState{
				SeriesID:    "tvdb:series:123",
				SeriesTitle: "Test Series",
			},
			playbackProgress: []models.PlaybackProgress{
				{ID: "p1", MediaType: "episode", ItemID: "tvdb:series:123:S01E01", Position: 300, Duration: 3000},
			},
		},
		&mockContentPrefsServiceDetailsBundle{
			pref: &models.ContentPreference{
				ContentID:     "tvdb:series:123",
				ContentType:   "series",
				AudioLanguage: "jpn",
			},
		},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=series&titleId=tvdb:series:123&tmdbId=456&name=Test+Series&year=2020",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.SeriesDetails == nil {
		t.Error("expected seriesDetails to be non-nil")
	} else if resp.SeriesDetails.Title.Name != "Test Series" {
		t.Errorf("unexpected series name: %s", resp.SeriesDetails.Title.Name)
	}

	if resp.MovieDetails != nil {
		t.Error("expected movieDetails to be nil for series request")
	}

	if len(resp.Similar) != 1 {
		t.Errorf("expected 1 similar item, got %d", len(resp.Similar))
	}

	if resp.Trailers == nil || len(resp.Trailers.Trailers) != 1 {
		t.Error("expected 1 trailer")
	}

	if resp.ContentPreference == nil || resp.ContentPreference.AudioLanguage != "jpn" {
		t.Error("expected content preference with audio=jpn")
	}

	if resp.WatchState == nil || resp.WatchState.SeriesID != "tvdb:series:123" {
		t.Error("expected watch state for the series")
	}

	if len(resp.PlaybackProgress) != 1 {
		t.Errorf("expected 1 playback progress, got %d", len(resp.PlaybackProgress))
	}
}

func TestDetailsBundleHandler_Movie(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			movieDetails: &models.Title{Name: "Test Movie", ID: "tmdb:movie:789"},
			similar: []models.Title{
				{Name: "Similar Movie"},
			},
			trailers: &models.TrailerResponse{
				Trailers: []models.Trailer{{Name: "Movie Trailer"}},
			},
		},
		&mockHistoryServiceDetailsBundle{
			playbackProgress: []models.PlaybackProgress{
				{ID: "p2", MediaType: "movie", ItemID: "tmdb:movie:789", Position: 600, Duration: 7200},
			},
		},
		&mockContentPrefsServiceDetailsBundle{
			pref: nil,
		},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=movie&titleId=tmdb:movie:789&tmdbId=789&name=Test+Movie&year=2023",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.MovieDetails == nil {
		t.Error("expected movieDetails to be non-nil")
	} else if resp.MovieDetails.Name != "Test Movie" {
		t.Errorf("unexpected movie name: %s", resp.MovieDetails.Name)
	}

	if resp.SeriesDetails != nil {
		t.Error("expected seriesDetails to be nil for movie request")
	}

	// Watch state should be nil for movies (no series watch state fetched)
	if resp.WatchState != nil {
		t.Error("expected watchState to be nil for movie request")
	}

	if len(resp.PlaybackProgress) != 1 {
		t.Errorf("expected 1 playback progress, got %d", len(resp.PlaybackProgress))
	}
}

func TestDetailsBundleHandler_PartialFailure(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			seriesDetailsErr: errors.New("series details db error"),
			similarErr:       errors.New("similar content error"),
			trailersErr:      errors.New("trailers error"),
		},
		&mockHistoryServiceDetailsBundle{
			watchStateErr:       errors.New("watch state error"),
			playbackProgressErr: errors.New("progress error"),
		},
		&mockContentPrefsServiceDetailsBundle{
			err: errors.New("content prefs error"),
		},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=series&titleId=tvdb:series:123&tmdbId=456",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	// Should still return 200 even if all services fail
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 despite errors, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Nil pointers are fine, but slices should be empty arrays
	if resp.Similar == nil {
		t.Error("similar should be empty array, not nil")
	}
	if len(resp.Similar) != 0 {
		t.Errorf("expected empty similar, got %d", len(resp.Similar))
	}
	if resp.PlaybackProgress == nil {
		t.Error("playbackProgress should be empty array, not nil")
	}
	if len(resp.PlaybackProgress) != 0 {
		t.Errorf("expected empty playback progress, got %d", len(resp.PlaybackProgress))
	}
}

func TestDetailsBundleHandler_NoTmdbIdSkipsSimilar(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			movieDetails: &models.Title{Name: "Movie No TMDB"},
			trailers:     &models.TrailerResponse{Trailers: []models.Trailer{}},
		},
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	// No tmdbId parameter
	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=movie&titleId=tmdb:movie:1&name=Movie",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	// Similar should be empty since tmdbId=0
	if len(resp.Similar) != 0 {
		t.Errorf("expected empty similar (no tmdbId), got %d", len(resp.Similar))
	}
}

func TestDetailsBundleHandler_UserNotFound(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{},
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: false},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/users/unknown/details-bundle?type=movie", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "unknown"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDetailsBundleHandler_EmptyUserID(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{},
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/users//details-bundle?type=series", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": ""})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

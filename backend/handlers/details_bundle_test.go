package handlers_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/handlers"
	"novastream/models"
	metadatapkg "novastream/services/metadata"

	"github.com/gorilla/mux"
)

// --- mock services for details bundle ---

type mockMetadataServiceDetailsBundle struct {
	seriesDetails *models.SeriesDetails
	seriesLite    *models.SeriesDetails
	movieDetails  *models.Title
	similar       []models.Title
	trailers      *models.TrailerResponse

	seriesDetailsErr error
	seriesLiteErr    error
	movieDetailsErr  error
	similarErr       error
	trailersErr      error

	// Optional per-TMDB-ID similar results and artificial latency.
	similarByTMDBID map[int64][]models.Title
	movieDelay      time.Duration
	similarDelay    time.Duration

	// Capture the TMDB IDs passed to Similar() for verification.
	similarMu    sync.Mutex
	similarCalls []int64
}

func (m *mockMetadataServiceDetailsBundle) similarCalledWith() []int64 {
	m.similarMu.Lock()
	defer m.similarMu.Unlock()
	return append([]int64(nil), m.similarCalls...)
}

func (m *mockMetadataServiceDetailsBundle) SeriesDetails(_ context.Context, _ models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return m.seriesDetails, m.seriesDetailsErr
}
func (m *mockMetadataServiceDetailsBundle) SeriesDetailsLite(_ context.Context, _ models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return m.seriesLite, m.seriesLiteErr
}
func (m *mockMetadataServiceDetailsBundle) MovieDetails(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	time.Sleep(m.movieDelay)
	return m.movieDetails, m.movieDetailsErr
}
func (m *mockMetadataServiceDetailsBundle) Similar(_ context.Context, _ string, tmdbID int64) ([]models.Title, error) {
	m.similarMu.Lock()
	m.similarCalls = append(m.similarCalls, tmdbID)
	m.similarMu.Unlock()
	time.Sleep(m.similarDelay)
	if titles, ok := m.similarByTMDBID[tmdbID]; ok {
		return titles, m.similarErr
	}
	return m.similar, m.similarErr
}
func (m *mockMetadataServiceDetailsBundle) DiscoverByGenre(_ context.Context, _ string, _ int64, _, _ int) ([]models.TrendingItem, int, error) {
	return nil, 0, nil
}
func (m *mockMetadataServiceDetailsBundle) DiscoverByDecade(_ context.Context, _ string, _, _, _ int) ([]models.TrendingItem, int, error) {
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
func (m *mockMetadataServiceDetailsBundle) SearchYouTubeVideos(_ context.Context, _ string, _ int) ([]models.YouTubeVideoSearchResult, error) {
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
func (m *mockMetadataServiceDetailsBundle) CachedMovieReleases(_ context.Context, _ []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem {
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
func (m *mockMetadataServiceDetailsBundle) GetCuratedList(_ context.Context, _ []metadatapkg.CuratedItem, _ string) ([]models.TrendingItem, error) {
	return nil, nil
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

func (m *mockMetadataServiceDetailsBundle) EnrichTrendingCertifications(_ context.Context, _ []models.TrendingItem) {
}

func (m *mockMetadataServiceDetailsBundle) EnrichTitleCertifications(_ context.Context, _ []models.Title) {
}

func (m *mockMetadataServiceDetailsBundle) GetProgressSnapshot() metadatapkg.ProgressSnapshot {
	return metadatapkg.ProgressSnapshot{}
}

func (m *mockMetadataServiceDetailsBundle) MDBListIsEnabled() bool { return false }
func (m *mockMetadataServiceDetailsBundle) GetMDBListAllRatings(_ context.Context, _ string, _ string) ([]models.Rating, error) {
	return nil, nil
}
func (m *mockMetadataServiceDetailsBundle) GetMDBListAllRatingsCached(_ string, _ string) []models.Rating {
	return nil
}
func (m *mockMetadataServiceDetailsBundle) GetTextPosterURL(_ string, _ int64, _ int64) string {
	return ""
}

func (m *mockMetadataServiceDetailsBundle) GetCachedArtworkURLs(_ string, _ int64, _ int64) (string, string, []string) {
	return "", "", nil
}

func (m *mockMetadataServiceDetailsBundle) ApplyLocalizedArtwork(_ context.Context, _ *models.Title) bool {
	return false
}

func (m *mockMetadataServiceDetailsBundle) GetCachedOverview(_ string, _ int64, _ int64) string {
	return ""
}

func (m *mockMetadataServiceDetailsBundle) GetTopTen(_ context.Context, _ string, _ []string) ([]models.TrendingItem, error) {
	return nil, nil
}

type mockHistoryServiceDetailsBundle struct {
	watchState       *models.SeriesWatchState
	playbackProgress []models.PlaybackProgress
	watchHistory     []models.WatchHistoryItem

	watchStateErr       error
	playbackProgressErr error
}

func (m *mockHistoryServiceDetailsBundle) GetSeriesWatchState(_, _ string) (*models.SeriesWatchState, error) {
	return m.watchState, m.watchStateErr
}
func (m *mockHistoryServiceDetailsBundle) GetSeriesOrdering(_ string, _ int64) (string, error) {
	return "", nil
}
func (m *mockHistoryServiceDetailsBundle) SetSeriesOrdering(_ string, _ int64, _ string) error {
	return nil
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
func (m *mockHistoryServiceDetailsBundle) GetContinueWatchingRevision(_ string) (string, error) {
	return "", nil
}
func (m *mockHistoryServiceDetailsBundle) GetWatchHistoryRevision(_ string) (string, error) {
	return "", nil
}
func (m *mockHistoryServiceDetailsBundle) ListSeriesStates(_ string) ([]models.SeriesWatchState, error) {
	return nil, nil
}
func (m *mockHistoryServiceDetailsBundle) HideFromContinueWatching(_, _ string) error { return nil }
func (m *mockHistoryServiceDetailsBundle) ListWatchHistory(_ string) ([]models.WatchHistoryItem, error) {
	return m.watchHistory, nil
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
func (m *mockHistoryServiceDetailsBundle) DeleteWatchHistoryItem(_, _, _ string) error {
	return nil
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

func TestDetailsShellHandler_SeriesPrefersLite(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			seriesLite: &models.SeriesDetails{
				Title: models.Title{
					Name:     "Lite Series",
					ID:       "tvdb:series:123",
					Backdrop: &models.Image{URL: "https://example.com/backdrop.jpg", Type: "backdrop"},
				},
			},
			seriesDetails: &models.SeriesDetails{
				Title: models.Title{Name: "Full Series", ID: "tvdb:series:123"},
			},
		},
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-shell?type=series&titleId=tvdb:series:123&name=Lite+Series",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsShell(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.DetailsShellResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.SeriesDetails == nil {
		t.Fatal("expected seriesDetails in shell response")
	}
	if resp.SeriesDetails.Title.Name != "Lite Series" {
		t.Fatalf("expected lite details, got %q", resp.SeriesDetails.Title.Name)
	}
}

func TestDetailsShellHandler_SeriesFallsBackToFull(t *testing.T) {
	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			seriesLiteErr: errors.New("lite failed"),
			seriesDetails: &models.SeriesDetails{
				Title: models.Title{Name: "Full Series", ID: "tvdb:series:321"},
			},
		},
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-shell?type=series&titleId=tvdb:series:321&name=Full+Series",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsShell(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handlers.DetailsShellResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.SeriesDetails == nil || resp.SeriesDetails.Title.Name != "Full Series" {
		t.Fatalf("expected full fallback details, got %+v", resp.SeriesDetails)
	}
}

func TestDetailsBundleHandler_GzipResponse(t *testing.T) {
	longOverview := strings.Repeat("One Piece overview. ", 800)
	episodes := make([]models.SeriesEpisode, 0, 80)
	for seasonNum := 1; seasonNum <= 4; seasonNum++ {
		for episodeNum := 1; episodeNum <= 20; episodeNum++ {
			episodes = append(episodes, models.SeriesEpisode{
				ID:            "ep",
				Name:          "Episode",
				Overview:      longOverview,
				SeasonNumber:  seasonNum,
				EpisodeNumber: episodeNum,
			})
		}
	}

	h := handlers.NewDetailsBundleHandler(
		&mockMetadataServiceDetailsBundle{
			seriesDetails: &models.SeriesDetails{
				Title: models.Title{Name: "Big Series", ID: "tvdb:series:999"},
				Seasons: []models.SeriesSeason{
					{
						ID:           "season-1",
						Name:         "Season 1",
						Number:       1,
						EpisodeCount: len(episodes),
						Episodes:     episodes,
					},
				},
			},
		},
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=series&titleId=tvdb:series:999&name=Big+Series",
		nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected gzip content encoding, got %q", got)
	}

	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gz.Close()

	payload, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("failed to read gzipped payload: %v", err)
	}

	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("failed to decode gzipped response: %v", err)
	}
	if resp.SeriesDetails == nil || resp.SeriesDetails.Title.Name != "Big Series" {
		t.Fatalf("unexpected gzipped response body: %+v", resp.SeriesDetails)
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

func TestDetailsBundleHandler_UsesResolvedTMDBID(t *testing.T) {
	// The request passes tmdbId=90 (wrong), but the series details resolve TMDBID=3793 (correct).
	// Similar() should be called with the resolved 3793, not the request param 90.
	meta := &mockMetadataServiceDetailsBundle{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{Name: "Invader ZIM", ID: "tvdb:series:75545", TMDBID: 3793},
		},
		similarByTMDBID: map[int64][]models.Title{
			90:   {{Name: "Wrong Show", ID: "wrong"}},
			3793: {{Name: "Similar Show", ID: "similar1"}},
		},
		trailers: &models.TrailerResponse{},
	}
	h := handlers.NewDetailsBundleHandler(
		meta,
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=series&titleId=tvdb:series:75545&tmdbId=90&name=Invader+ZIM",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	calls := meta.similarCalledWith()
	calledResolved := false
	for _, id := range calls {
		calledResolved = calledResolved || id == 3793
	}
	if !calledResolved {
		t.Errorf("expected Similar() called with resolved TMDB ID 3793, got calls %v", calls)
	}

	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Similar) != 1 || resp.Similar[0].ID != "similar1" {
		t.Errorf("expected similar results for resolved TMDB ID 3793, got %+v", resp.Similar)
	}
}

func TestDetailsBundleHandler_FallsBackToRequestTMDBID(t *testing.T) {
	// If details don't resolve a TMDB ID, Similar() should use the request param.
	meta := &mockMetadataServiceDetailsBundle{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{Name: "Some Show", ID: "tvdb:series:999", TMDBID: 0},
		},
		similar: []models.Title{
			{Name: "Similar Show"},
		},
		trailers: &models.TrailerResponse{},
	}
	h := handlers.NewDetailsBundleHandler(
		meta,
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=series&titleId=tvdb:series:999&tmdbId=456&name=Some+Show",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetDetailsBundle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if calls := meta.similarCalledWith(); len(calls) != 1 || calls[0] != 456 {
		t.Errorf("expected one Similar() call with fallback TMDB ID 456, got %v", calls)
	}
}

func TestDetailsBundleHandler_SimilarOverlapsDetails(t *testing.T) {
	// With a request TMDB ID, similar must not wait for details to finish.
	meta := &mockMetadataServiceDetailsBundle{
		movieDetails: &models.Title{Name: "Test Movie", ID: "tmdb:movie:789", TMDBID: 789},
		similar:      []models.Title{{Name: "Similar Movie"}},
		trailers:     &models.TrailerResponse{},
		movieDelay:   200 * time.Millisecond,
		similarDelay: 200 * time.Millisecond,
	}
	h := handlers.NewDetailsBundleHandler(
		meta,
		&mockHistoryServiceDetailsBundle{},
		&mockContentPrefsServiceDetailsBundle{},
		&mockUserServiceDetailsBundle{exists: true},
	)

	req := httptest.NewRequest(http.MethodGet,
		"/api/users/user1/details-bundle?type=movie&titleId=tmdb:movie:789&tmdbId=789&name=Test+Movie",
		nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	started := time.Now()
	h.GetDetailsBundle(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if elapsed >= 380*time.Millisecond {
		t.Fatalf("bundle took %v; similar should overlap the 200ms details fetch (serial would be ~400ms)", elapsed)
	}
	if calls := meta.similarCalledWith(); len(calls) != 1 || calls[0] != 789 {
		t.Errorf("expected one Similar() call for TMDB ID 789, got %v", calls)
	}
	var resp handlers.DetailsBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Similar) != 1 {
		t.Errorf("expected 1 similar title, got %d", len(resp.Similar))
	}
}

func TestInteractiveMetadataPriorityMarksRequestContext(t *testing.T) {
	var interactive bool
	wrapped := handlers.InteractiveMetadataPriority(func(_ http.ResponseWriter, r *http.Request) {
		interactive = metadatapkg.IsInteractivePriority(r.Context())
	})
	wrapped(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/users/user1/details-bundle", nil))
	if !interactive {
		t.Fatal("expected wrapped handler to see an interactive-priority context")
	}
	if metadatapkg.IsInteractivePriority(context.Background()) {
		t.Fatal("expected a plain context not to be interactive")
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

type mockWatchlistMembership struct {
	gotUser, gotType, gotID string
	gotExt                  map[string]string
	contains                bool
}

func (m *mockWatchlistMembership) Contains(userID, mediaType, id string, externalIDs map[string]string) bool {
	m.gotUser, m.gotType, m.gotID, m.gotExt = userID, mediaType, id, externalIDs
	return m.contains
}

func TestDetailsBundleHandler_InWatchlist(t *testing.T) {
	newHandler := func() *handlers.DetailsBundleHandler {
		return handlers.NewDetailsBundleHandler(
			&mockMetadataServiceDetailsBundle{
				movieDetails: &models.Title{Name: "Test Movie", ID: "tmdb:movie:789", TMDBID: 789, IMDBID: "tt0000789"},
			},
			&mockHistoryServiceDetailsBundle{},
			&mockContentPrefsServiceDetailsBundle{},
			&mockUserServiceDetailsBundle{exists: true},
		)
	}
	get := func(h *handlers.DetailsBundleHandler) map[string]json.RawMessage {
		req := httptest.NewRequest(http.MethodGet,
			"/api/users/user1/details-bundle?type=movie&titleId=tmdb:movie:789&tmdbId=789&name=Test+Movie", nil)
		req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
		rec := httptest.NewRecorder()
		h.GetDetailsBundle(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		return raw
	}

	if _, ok := get(newHandler())["inWatchlist"]; ok {
		t.Error("expected inWatchlist to be omitted without a watchlist service")
	}

	for _, contains := range []bool{true, false} {
		membership := &mockWatchlistMembership{contains: contains}
		h := newHandler()
		h.SetWatchlistService(membership)
		raw := get(h)
		if got := string(raw["inWatchlist"]); got != fmt.Sprint(contains) {
			t.Errorf("inWatchlist = %s, want %v", got, contains)
		}
		if membership.gotUser != "user1" || membership.gotType != "movie" || membership.gotID != "tmdb:movie:789" {
			t.Errorf("Contains called with (%q, %q, %q)", membership.gotUser, membership.gotType, membership.gotID)
		}
		// Resolved metadata IDs are passed along with the request's.
		if membership.gotExt["tmdb"] != "789" || membership.gotExt["imdb"] != "tt0000789" {
			t.Errorf("Contains external IDs = %v", membership.gotExt)
		}
	}
}

type mockMovieWatchedHistory struct {
	mockHistoryServiceDetailsBundle
	gotUser, gotID string
	gotExt         map[string]string
	watched        bool
}

func (m *mockMovieWatchedHistory) IsMovieWatched(userID, itemID string, externalIDs map[string]string) bool {
	m.gotUser, m.gotID, m.gotExt = userID, itemID, externalIDs
	return m.watched
}

func TestDetailsBundleHandler_MovieWatched(t *testing.T) {
	metadata := &mockMetadataServiceDetailsBundle{
		movieDetails:  &models.Title{Name: "Test Movie", ID: "tmdb:movie:789", TMDBID: 789, IMDBID: "tt0000789"},
		seriesDetails: &models.SeriesDetails{Title: models.Title{Name: "Test Show", ID: "tmdb:tv:456"}},
	}
	plain := func() *handlers.DetailsBundleHandler {
		return handlers.NewDetailsBundleHandler(metadata, &mockHistoryServiceDetailsBundle{},
			&mockContentPrefsServiceDetailsBundle{}, &mockUserServiceDetailsBundle{exists: true})
	}
	with := func(history *mockMovieWatchedHistory) *handlers.DetailsBundleHandler {
		return handlers.NewDetailsBundleHandler(metadata, history,
			&mockContentPrefsServiceDetailsBundle{}, &mockUserServiceDetailsBundle{exists: true})
	}
	get := func(h *handlers.DetailsBundleHandler, query string) map[string]json.RawMessage {
		req := httptest.NewRequest(http.MethodGet, "/api/users/user1/details-bundle?"+query, nil)
		req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
		rec := httptest.NewRecorder()
		h.GetDetailsBundle(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		return raw
	}
	const movie = "type=movie&titleId=tmdb:movie:789&tmdbId=789&name=Test+Movie"

	if _, ok := get(plain(), movie)["watched"]; ok {
		t.Error("expected watched to be omitted when the history service cannot answer it")
	}
	for _, watched := range []bool{true, false} {
		history := &mockMovieWatchedHistory{watched: watched}
		if got := string(get(with(history), movie)["watched"]); got != fmt.Sprint(watched) {
			t.Errorf("watched = %s, want %v", got, watched)
		}
		if history.gotUser != "user1" || history.gotID != "tmdb:movie:789" {
			t.Errorf("IsMovieWatched called with (%q, %q)", history.gotUser, history.gotID)
		}
		if history.gotExt["tmdb"] != "789" || history.gotExt["imdb"] != "tt0000789" {
			t.Errorf("IsMovieWatched external IDs = %v", history.gotExt)
		}
	}
	// Series carry per-episode watched instead.
	if _, ok := get(with(&mockMovieWatchedHistory{watched: true}), "type=series&titleId=tmdb:tv:456&name=Test+Show")["watched"]; ok {
		t.Error("expected watched to be omitted for series")
	}
}

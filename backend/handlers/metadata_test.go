package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/letterboxd"
	"novastream/services/mdblist"
	"novastream/services/metadata"
	"novastream/services/simkl"
	"novastream/services/trakt"
)

type fakeMetadataService struct {
	trendingResp   []models.TrendingItem
	trendingErr    error
	trendingByType map[string][]models.TrendingItem
	topTenByType   map[string][]models.TrendingItem
	similarByKey   map[string][]models.Title
	searchResp     []models.SearchResult
	searchErr      error
	youtubeResp    []models.YouTubeVideoSearchResult
	youtubeErr     error
	seriesResp     *models.SeriesDetails
	seriesErr      error
	movieResp      *models.Title
	movieErr       error

	discoverByGenreResp  []models.TrendingItem
	discoverByGenreTotal int
	discoverByGenreErr   error

	discoverByDecadeResp  []models.TrendingItem
	discoverByDecadeTotal int
	discoverByDecadeErr   error
	curatedResp           []models.TrendingItem
	customListResp        []models.TrendingItem
	customListTotal       int
	customListUnfiltered  int
	customListErr         error
	cachedArtwork         map[string]struct {
		textPoster   string
		textBackdrop string
		backdrops    []string
	}

	// applyArtworkLang, when non-empty, is stamped onto any title passed to
	// ApplyLocalizedArtwork so tests can assert localization fired.
	applyArtworkLang  string
	applyArtworkCalls int32

	lastTrendingType         string
	lastTrendingOptions      metadata.ShelfLoadOptions
	lastSearchQuery          string
	lastSearchType           string
	lastYouTubeQuery         string
	lastYouTubeLimit         int
	lastSeriesQuery          models.SeriesDetailsQuery
	lastMovieQuery           models.MovieDetailsQuery
	lastDiscoverGenreType    string
	lastDiscoverGenreID      int64
	lastDiscoverGenreLimit   int
	lastDiscoverGenreOffset  int
	lastDiscoverGenreOptions metadata.ShelfLoadOptions

	lastDiscoverDecadeType    string
	lastDiscoverDecade        int
	lastDiscoverDecadeLimit   int
	lastDiscoverDecadeOffset  int
	lastDiscoverDecadeOptions metadata.ShelfLoadOptions
	lastCuratedItems          []metadata.CuratedItem
	lastCuratedLabel          string
	lastCustomListURL         string
	lastCustomListOptions     metadata.CustomListOptions
}

func (f *fakeMetadataService) Trending(_ context.Context, mediaType string) ([]models.TrendingItem, error) {
	f.lastTrendingType = mediaType
	if f.trendingByType != nil {
		if items, ok := f.trendingByType[mediaType]; ok {
			return items, f.trendingErr
		}
	}
	return f.trendingResp, f.trendingErr
}

func (f *fakeMetadataService) TrendingWithOptions(_ context.Context, mediaType string, opts metadata.ShelfLoadOptions) ([]models.TrendingItem, error) {
	f.lastTrendingType = mediaType
	f.lastTrendingOptions = opts
	return f.trendingResp, f.trendingErr
}

func (f *fakeMetadataService) Search(_ context.Context, query, mediaType string) ([]models.SearchResult, error) {
	f.lastSearchQuery = query
	f.lastSearchType = mediaType
	return f.searchResp, f.searchErr
}

func (f *fakeMetadataService) SearchYouTubeVideos(_ context.Context, query string, limit int) ([]models.YouTubeVideoSearchResult, error) {
	f.lastYouTubeQuery = query
	f.lastYouTubeLimit = limit
	return f.youtubeResp, f.youtubeErr
}

func (f *fakeMetadataService) SeriesDetails(_ context.Context, query models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	f.lastSeriesQuery = query
	return f.seriesResp, f.seriesErr
}

func (f *fakeMetadataService) SeriesInfo(_ context.Context, query models.SeriesDetailsQuery) (*models.Title, error) {
	f.lastSeriesQuery = query
	if f.seriesResp != nil {
		return &f.seriesResp.Title, nil
	}
	return nil, f.seriesErr
}

func (f *fakeMetadataService) MovieDetails(_ context.Context, query models.MovieDetailsQuery) (*models.Title, error) {
	f.lastMovieQuery = query
	return f.movieResp, f.movieErr
}

func (f *fakeMetadataService) MovieInfo(_ context.Context, query models.MovieDetailsQuery) (*models.Title, error) {
	// MovieInfo is lightweight version, same as MovieDetails for testing
	return f.MovieDetails(nil, query)
}

func (f *fakeMetadataService) Trailers(_ context.Context, _ models.TrailerQuery) (*models.TrailerResponse, error) {
	return &models.TrailerResponse{Trailers: []models.Trailer{}}, nil
}

func (f *fakeMetadataService) BatchSeriesDetails(_ context.Context, queries []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem {
	results := make([]models.BatchSeriesDetailsItem, len(queries))
	for i, query := range queries {
		results[i].Query = query
		if f.seriesErr != nil {
			results[i].Error = f.seriesErr.Error()
		} else {
			results[i].Details = f.seriesResp
		}
	}
	return results
}

func (f *fakeMetadataService) BatchSeriesTitleFields(_ context.Context, queries []models.SeriesDetailsQuery, fields []string) []models.BatchSeriesDetailsItem {
	results := make([]models.BatchSeriesDetailsItem, len(queries))
	for i, query := range queries {
		results[i].Query = query
		if f.seriesErr != nil {
			results[i].Error = f.seriesErr.Error()
		} else if f.seriesResp != nil {
			// Return only requested fields (mimics real behavior)
			t := models.Title{
				ID:        f.seriesResp.Title.ID,
				Name:      f.seriesResp.Title.Name,
				MediaType: f.seriesResp.Title.MediaType,
				TVDBID:    f.seriesResp.Title.TVDBID,
			}
			for _, field := range fields {
				switch field {
				case "overview":
					t.Overview = f.seriesResp.Title.Overview
				case "year":
					t.Year = f.seriesResp.Title.Year
				case "genres":
					t.Genres = f.seriesResp.Title.Genres
				}
			}
			results[i].Details = &models.SeriesDetails{Title: t}
		}
	}
	return results
}

func (f *fakeMetadataService) BatchMovieReleases(_ context.Context, queries []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem {
	results := make([]models.BatchMovieReleasesItem, len(queries))
	for i, query := range queries {
		results[i].Query = query
	}
	return results
}

func (f *fakeMetadataService) CollectionDetails(_ context.Context, _ int64) (*models.CollectionDetails, error) {
	return nil, nil
}

func (f *fakeMetadataService) GetCustomList(_ context.Context, listURL string, opts metadata.CustomListOptions) ([]models.TrendingItem, int, int, error) {
	f.lastCustomListURL = listURL
	f.lastCustomListOptions = opts
	return f.customListResp, f.customListTotal, f.customListUnfiltered, f.customListErr
}

func (f *fakeMetadataService) GetCuratedList(_ context.Context, items []metadata.CuratedItem, label string) ([]models.TrendingItem, error) {
	f.lastCuratedItems = items
	f.lastCuratedLabel = label
	return f.curatedResp, nil
}

func (f *fakeMetadataService) ExtractTrailerStreamURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeMetadataService) StreamTrailer(_ context.Context, _ string, _ io.Writer) error {
	return nil
}

func (f *fakeMetadataService) StreamTrailerWithRange(_ context.Context, _ string, _ string, _ io.Writer) error {
	return nil
}

func (f *fakeMetadataService) PrequeueTrailer(_ string) (string, error) {
	return "", nil
}

func (f *fakeMetadataService) GetTrailerPrequeueStatus(_ string) (*metadata.TrailerPrequeueItem, error) {
	return nil, nil
}

func (f *fakeMetadataService) ServePrequeuedTrailer(_ string, _ http.ResponseWriter, _ *http.Request) error {
	return nil
}

func (f *fakeMetadataService) PersonDetails(_ context.Context, _ int64) (*models.PersonDetails, error) {
	return nil, nil
}

func (f *fakeMetadataService) similarKey(mediaType string, tmdbID int64) string {
	if mediaType != "movie" {
		mediaType = "series"
	}
	return mediaType + ":" + strconv.FormatInt(tmdbID, 10)
}

func (f *fakeMetadataService) Similar(_ context.Context, mediaType string, tmdbID int64) ([]models.Title, error) {
	if f.similarByKey != nil {
		return f.similarByKey[f.similarKey(mediaType, tmdbID)], nil
	}
	return nil, nil
}

func (f *fakeMetadataService) DiscoverByGenre(_ context.Context, mediaType string, genreID int64, limit, offset int) ([]models.TrendingItem, int, error) {
	f.lastDiscoverGenreType = mediaType
	f.lastDiscoverGenreID = genreID
	f.lastDiscoverGenreLimit = limit
	f.lastDiscoverGenreOffset = offset
	return f.discoverByGenreResp, f.discoverByGenreTotal, f.discoverByGenreErr
}

func (f *fakeMetadataService) DiscoverByGenreWithOptions(_ context.Context, mediaType string, genreID int64, limit, offset int, opts metadata.ShelfLoadOptions) ([]models.TrendingItem, int, error) {
	f.lastDiscoverGenreType = mediaType
	f.lastDiscoverGenreID = genreID
	f.lastDiscoverGenreLimit = limit
	f.lastDiscoverGenreOffset = offset
	f.lastDiscoverGenreOptions = opts
	return f.discoverByGenreResp, f.discoverByGenreTotal, f.discoverByGenreErr
}

func (f *fakeMetadataService) DiscoverByDecade(_ context.Context, mediaType string, decadeStart, limit, offset int) ([]models.TrendingItem, int, error) {
	f.lastDiscoverDecadeType = mediaType
	f.lastDiscoverDecade = decadeStart
	f.lastDiscoverDecadeLimit = limit
	f.lastDiscoverDecadeOffset = offset
	return f.discoverByDecadeResp, f.discoverByDecadeTotal, f.discoverByDecadeErr
}

func (f *fakeMetadataService) DiscoverByDecadeWithOptions(_ context.Context, mediaType string, decadeStart, limit, offset int, opts metadata.ShelfLoadOptions) ([]models.TrendingItem, int, error) {
	f.lastDiscoverDecadeType = mediaType
	f.lastDiscoverDecade = decadeStart
	f.lastDiscoverDecadeLimit = limit
	f.lastDiscoverDecadeOffset = offset
	f.lastDiscoverDecadeOptions = opts
	return f.discoverByDecadeResp, f.discoverByDecadeTotal, f.discoverByDecadeErr
}

func (f *fakeMetadataService) GetAIRecommendations(_ context.Context, _ []string, _ []string, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}

func (f *fakeMetadataService) GetAISimilar(_ context.Context, _ string, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}

func (f *fakeMetadataService) GetAICustomRecommendations(_ context.Context, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}

func (f *fakeMetadataService) GetAISurprise(_ context.Context, _ string, _ string) (*models.TrendingItem, error) {
	return nil, nil
}

func (f *fakeMetadataService) EnrichSearchCertifications(_ context.Context, _ []models.SearchResult) {
	// no-op in tests — certifications are pre-set on test data
}

func (f *fakeMetadataService) EnrichTrendingCertifications(_ context.Context, _ []models.TrendingItem) {
	// no-op in tests — certifications are pre-set on test data
}

func (f *fakeMetadataService) EnrichTitleCertifications(_ context.Context, _ []models.Title) {
	// no-op in tests — certifications are pre-set on test data
}

func (f *fakeMetadataService) GetProgressSnapshot() metadata.ProgressSnapshot {
	return metadata.ProgressSnapshot{}
}

func (f *fakeMetadataService) MDBListIsEnabled() bool {
	return false
}

func (f *fakeMetadataService) GetMDBListAllRatings(_ context.Context, _ string, _ string) ([]models.Rating, error) {
	return nil, nil
}

func (f *fakeMetadataService) GetMDBListAllRatingsCached(_ string, _ string) []models.Rating {
	return nil
}

func (f *fakeMetadataService) GetTextPosterURL(_ string, _ int64, _ int64) string {
	return ""
}

func (f *fakeMetadataService) ApplyLocalizedArtwork(_ context.Context, title *models.Title) bool {
	atomic.AddInt32(&f.applyArtworkCalls, 1)
	if f.applyArtworkLang == "" || title == nil {
		return false
	}
	title.TextPoster = &models.Image{Type: "poster", Language: f.applyArtworkLang}
	return true
}

func (f *fakeMetadataService) GetCachedArtworkURLs(mediaType string, tmdbID int64, tvdbID int64) (string, string, []string) {
	if f.cachedArtwork != nil {
		if artwork, ok := f.cachedArtwork[mediaType+":"+strconv.FormatInt(tmdbID, 10)+":"+strconv.FormatInt(tvdbID, 10)]; ok {
			return artwork.textPoster, artwork.textBackdrop, artwork.backdrops
		}
	}
	return "", "", nil
}

func (f *fakeMetadataService) GetCachedOverview(_ string, _ int64, _ int64) string {
	return ""
}

func (f *fakeMetadataService) GetTopTen(_ context.Context, mediaType string, _ []string) ([]models.TrendingItem, error) {
	if f.topTenByType != nil {
		if items, ok := f.topTenByType[mediaType]; ok {
			return items, f.trendingErr
		}
	}
	return f.trendingResp, f.trendingErr
}

// fakeUsersServiceForSearch implements usersServiceInterface for search handler tests.
type fakeUsersServiceForSearch struct {
	users map[string]models.User
}

func (f *fakeUsersServiceForSearch) Get(id string) (models.User, bool) {
	u, ok := f.users[id]
	return u, ok
}

type fakeAccountsServiceForMetadata struct {
	accounts map[string]models.Account
}

func (f *fakeAccountsServiceForMetadata) Get(id string) (models.Account, bool) {
	account, ok := f.accounts[id]
	return account, ok
}

type fakeMetadataHistoryService struct {
	history  []models.WatchHistoryItem
	progress []models.PlaybackProgress
	err      error
}

func (f *fakeMetadataHistoryService) GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error) {
	return nil, f.err
}

func (f *fakeMetadataHistoryService) ListWatchHistory(userID string) ([]models.WatchHistoryItem, error) {
	return f.history, f.err
}

func (f *fakeMetadataHistoryService) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	return nil, f.err
}

func (f *fakeMetadataHistoryService) ListSeriesStates(userID string) ([]models.SeriesWatchState, error) {
	return nil, f.err
}

func (f *fakeMetadataHistoryService) ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error) {
	return f.progress, f.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testConfigManager(t *testing.T) *config.Manager {
	t.Helper()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "settings.json")
	mgr := config.NewManager(cfgPath)
	if err := os.WriteFile(cfgPath, []byte(`{"server":{},"metadata":{},"cache":{},"homeShelves":{"shelves":[]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestMetadataHandler_DiscoverNew(t *testing.T) {
	fake := &fakeMetadataService{
		trendingResp: []models.TrendingItem{{Rank: 1, Title: models.Title{Name: "Lost", MediaType: "tv"}}},
	}

	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/new?type=Movie&lite=true&artworkLimit=20", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverNew(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastTrendingType != "movie" {
		t.Fatalf("expected media type to normalize to movie, got %q", fake.lastTrendingType)
	}
	if !fake.lastTrendingOptions.Lite || fake.lastTrendingOptions.ArtworkLimit != 20 {
		t.Fatalf("unexpected trending options: %+v", fake.lastTrendingOptions)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type %q", got)
	}

	var payload DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Title.Name != "Lost" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_DiscoverNewError(t *testing.T) {
	fake := &fakeMetadataService{trendingErr: errors.New("tmdb unavailable")}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/new", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverNew(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] == "" {
		t.Fatalf("expected error message, got %v", payload)
	}
}

func TestMetadataHandler_Search(t *testing.T) {
	fake := &fakeMetadataService{
		searchResp: []models.SearchResult{{Score: 99, Title: models.Title{Name: "Foundation", MediaType: "tv"}}},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=foundation&type=Tv", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastSearchQuery != "foundation" || fake.lastSearchType != "tv" {
		t.Fatalf("unexpected captured values query=%q type=%q", fake.lastSearchQuery, fake.lastSearchType)
	}

	var payload []models.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 1 || payload[0].Title.Name != "Foundation" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_SearchAcceptsQueryParam(t *testing.T) {
	fake := &fakeMetadataService{
		searchResp: []models.SearchResult{{Score: 88, Title: models.Title{Name: "Heat", MediaType: "movie"}}},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/search?query=heat&type=movie", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastSearchQuery != "heat" || fake.lastSearchType != "movie" {
		t.Fatalf("unexpected captured values query=%q type=%q", fake.lastSearchQuery, fake.lastSearchType)
	}
}

func TestMetadataHandler_SearchError(t *testing.T) {
	fake := &fakeMetadataService{searchErr: errors.New("search down")}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=x", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] == "" {
		t.Fatalf("expected error message, got %v", payload)
	}
}

func TestMetadataHandler_TraktListWatchlist(t *testing.T) {
	origURL := trakt.GetBaseURLForTest()
	defer trakt.SetBaseURLForTest(origURL)
	trakt.SetBaseURLForTest("https://trakt.test")

	fake := &fakeMetadataService{
		curatedResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{ID: "movie:329865", Name: "Arrival", MediaType: "movie"}},
		},
	}
	mgr := testConfigManager(t)
	settings := config.DefaultSettings()
	settings.Trakt.Accounts = []config.TraktAccount{
		{
			ID:             "trakt-1",
			Name:           "Main Trakt",
			OwnerAccountID: "acct-1",
			ClientID:       "client-id",
			ClientSecret:   "client-secret",
			AccessToken:    "access-token",
			ExpiresAt:      time.Now().Add(24 * time.Hour).Unix(),
		},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewMetadataHandler(fake, mgr)
	traktClient := trakt.NewClient("client-id", "client-secret")
	traktClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/users/me/watchlist" {
				t.Fatalf("unexpected trakt path %s", r.URL.Path)
			}
			body := `[{"type":"movie","listed_at":"` + time.Now().UTC().Format(time.RFC3339) + `","movie":{"title":"Arrival","year":2016,"ids":{"imdb":"tt2543164","tmdb":329865,"trakt":42}}}]`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"X-Pagination-Item-Count": []string{"1"},
				},
				Body: io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	handler.SetTraktClient(traktClient)
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"user-1": {ID: "user-1", AccountID: "acct-1", Name: "Profile"},
		},
	})
	handler.SetAccountsService(&fakeAccountsServiceForMetadata{
		accounts: map[string]models.Account{
			"acct-1": {ID: "acct-1", Username: "account"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/lists/trakt?userId=user-1&accountId=trakt-1&listType=watchlist&name=My+Trakt+Shelf", nil)
	rec := httptest.NewRecorder()

	handler.TraktList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastCuratedLabel != "My Trakt Shelf" {
		t.Fatalf("expected curated label to be forwarded, got %q", fake.lastCuratedLabel)
	}
	if len(fake.lastCuratedItems) != 1 {
		t.Fatalf("expected 1 curated item, got %d", len(fake.lastCuratedItems))
	}
	if fake.lastCuratedItems[0].IMDBID != "tt2543164" || fake.lastCuratedItems[0].MediaType != "movie" {
		t.Fatalf("unexpected curated item: %+v", fake.lastCuratedItems[0])
	}

	var payload TraktShelfResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0].Title.Name != "Arrival" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_SimklList(t *testing.T) {
	fake := &fakeMetadataService{
		curatedResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{ID: "movie:438631", Name: "Dune", MediaType: "movie"}},
		},
	}
	mgr := testConfigManager(t)
	settings := config.DefaultSettings()
	settings.Simkl.Accounts = []config.SimklAccount{
		{ID: "simkl-1", Name: "Main Simkl", ClientID: "client-id", AccessToken: "access-token"},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewMetadataHandler(fake, mgr)
	simklClient := simkl.NewClient()
	simklClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/sync/all-items/movies/plantowatch" {
				t.Fatalf("unexpected simkl path %s", r.URL.Path)
			}
			body := `{"movies":[
				{"status":"plantowatch","movie":{"title":"Dune","year":2021,"ids":{"imdb":"tt1160419","tmdb":"438631"}}}
			]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	handler.SetSimklClient(simklClient)
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"user-1": {ID: "user-1", AccountID: "acct-1", Name: "Profile", SimklAccountID: "simkl-1"},
		},
	})
	handler.SetAccountsService(&fakeAccountsServiceForMetadata{
		accounts: map[string]models.Account{"acct-1": {ID: "acct-1", Username: "account"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/lists/simkl?userId=user-1&accountId=simkl-1&mediaType=movies&listType=plantowatch&name=My+Simkl+Shelf", nil)
	rec := httptest.NewRecorder()

	handler.SimklList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastCuratedLabel != "My Simkl Shelf" {
		t.Fatalf("expected curated label forwarded, got %q", fake.lastCuratedLabel)
	}
	// Only the plantowatch item should be forwarded.
	if len(fake.lastCuratedItems) != 1 {
		t.Fatalf("expected 1 curated item, got %d", len(fake.lastCuratedItems))
	}
	item := fake.lastCuratedItems[0]
	if item.IMDBID != "tt1160419" || item.TMDBID != 438631 || item.MediaType != "movie" {
		t.Fatalf("unexpected curated item: %+v", item)
	}

	var payload CustomListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0].Title.Name != "Dune" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_SimklListRejectsUnlinkedProfile(t *testing.T) {
	fake := &fakeMetadataService{}
	mgr := testConfigManager(t)
	settings := config.DefaultSettings()
	settings.Simkl.Accounts = []config.SimklAccount{
		{ID: "simkl-1", ClientID: "client-id", AccessToken: "access-token"},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewMetadataHandler(fake, mgr)
	handler.SetSimklClient(simkl.NewClient())
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{"user-1": {ID: "user-1", AccountID: "acct-1"}},
	})
	handler.SetAccountsService(&fakeAccountsServiceForMetadata{
		accounts: map[string]models.Account{"acct-1": {ID: "acct-1"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/lists/simkl?userId=user-1&accountId=simkl-1&mediaType=movies", nil)
	rec := httptest.NewRecorder()
	handler.SimklList(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d: %s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestMetadataHandler_CustomListForwardsLiteOption(t *testing.T) {
	fake := &fakeMetadataService{
		customListResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{ID: "movie:1", Name: "Fast Shelf", MediaType: "movie"}},
		},
		customListTotal:      2,
		customListUnfiltered: 3,
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/lists/custom?url=https%3A%2F%2Fmdblist.com%2Flists%2Fsnoak%2Fdisney-plus-top-10-movies%2Fjson&limit=50&lite=true&artworkLimit=20&name=Disney%2B", nil)
	rec := httptest.NewRecorder()

	handler.CustomList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastCustomListURL != "https://mdblist.com/lists/snoak/disney-plus-top-10-movies/json" {
		t.Fatalf("unexpected list URL %q", fake.lastCustomListURL)
	}
	opts := fake.lastCustomListOptions
	if opts.Limit != 50 || !opts.Lite || opts.ArtworkLimit != 20 || opts.Label != "Disney+" {
		t.Fatalf("unexpected custom list options: %+v", opts)
	}

	var payload CustomListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Total != 2 || len(payload.Items) != 1 || payload.Items[0].Title.Name != "Fast Shelf" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_LetterboxdList(t *testing.T) {
	fake := &fakeMetadataService{
		curatedResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{ID: "movie:496243", Name: "Parasite", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	listsClient := mdblist.NewListsClient("api-key")
	listsClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/external/lists/777/items" {
				t.Fatalf("unexpected mdblist path %s", r.URL.Path)
			}
			body := `{"movies":[{"title":"Parasite","release_year":2019,"mediatype":"movie","imdb_id":"tt6751668","tvdb_id":0,"ids":{"imdb":"tt6751668","tmdb":496243}}],"shows":[],"pagination":{}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	handler.SetMDBListListsClient(listsClient)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/letterboxd?listId=777&name=My+LB+Shelf", nil)
	rec := httptest.NewRecorder()

	handler.LetterboxdList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastCuratedLabel != "My LB Shelf" {
		t.Fatalf("expected curated label forwarded, got %q", fake.lastCuratedLabel)
	}
	if len(fake.lastCuratedItems) != 1 {
		t.Fatalf("expected 1 curated item, got %d", len(fake.lastCuratedItems))
	}
	item := fake.lastCuratedItems[0]
	if item.IMDBID != "tt6751668" || item.TMDBID != 496243 || item.Title != "Parasite" || item.Year != 2019 || item.MediaType != "movie" {
		t.Fatalf("unexpected curated item: %+v", item)
	}

	var payload CustomListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 || payload.Items[0].Title.Name != "Parasite" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_LetterboxdListPublicURL(t *testing.T) {
	fake := &fakeMetadataService{
		curatedResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{ID: "movie:1182513", Name: "Patriot", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	client := letterboxd.NewClient()
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://letterboxd.com/godver3/list/test/" {
				t.Fatalf("unexpected letterboxd url %s", r.URL.String())
			}
			body := `<!doctype html><html><head><title>Test</title><meta name="description" content="A list of 500 films compiled on Letterboxd, including Patriot (2026)."></head><body>
				<div data-item-name="Patriot (2026)" data-item-slug="patriot-2026" data-target-link="/film/patriot-2026/"></div>
			</body></html>`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	handler.SetLetterboxdClient(client)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/letterboxd?listUrl=https%3A%2F%2Fletterboxd.com%2Fgodver3%2Flist%2Ftest%2F&name=Public+LB+Shelf", nil)
	rec := httptest.NewRecorder()

	handler.LetterboxdList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastCuratedLabel != "Public LB Shelf" {
		t.Fatalf("expected curated label forwarded, got %q", fake.lastCuratedLabel)
	}
	if len(fake.lastCuratedItems) != 1 {
		t.Fatalf("expected 1 curated item, got %d", len(fake.lastCuratedItems))
	}
	item := fake.lastCuratedItems[0]
	if item.Title != "Patriot" || item.Year != 2026 || item.MediaType != "movie" || item.IMDBID != "" || item.TMDBID != 0 {
		t.Fatalf("unexpected curated item: %+v", item)
	}
	var payload CustomListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Total != 500 || len(payload.Items) != 1 {
		t.Fatalf("unexpected payload total/items: %+v", payload)
	}
}

func TestMetadataHandler_LetterboxdListRequiresListID(t *testing.T) {
	handler := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
	handler.SetMDBListListsClient(mdblist.NewListsClient("api-key"))

	req := httptest.NewRequest(http.MethodGet, "/api/lists/letterboxd", nil)
	rec := httptest.NewRecorder()
	handler.LetterboxdList(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestMetadataHandler_LetterboxdSources(t *testing.T) {
	handler := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
	listsClient := mdblist.NewListsClient("api-key")
	listsClient.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/external/lists/user" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			body := `[{"id":777,"name":"My LB List","source":"letterboxd","items":42},{"id":888,"name":"Netflix Top 10","source":"flixpatrol","items":10}]`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	handler.SetMDBListListsClient(listsClient)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/letterboxd/sources", nil)
	rec := httptest.NewRecorder()
	handler.LetterboxdSources(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload struct {
		Lists []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Items int    `json:"items"`
		} `json:"lists"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	// Only the letterboxd source should be returned.
	if len(payload.Lists) != 1 || payload.Lists[0].ID != "777" || payload.Lists[0].Name != "My LB List" {
		t.Fatalf("unexpected sources payload: %+v", payload.Lists)
	}
}

func TestMetadataHandler_MovieDetails(t *testing.T) {
	fake := &fakeMetadataService{
		movieResp: &models.Title{
			ID:        "tvdb:movie:1",
			Name:      "Example",
			Year:      2024,
			MediaType: "movie",
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/metadata/movies/details?titleId=tvdb:movie:1&name=Example&year=2024&tmdbId=123&tvdbId=456&imdbId=tt123", nil)
	rec := httptest.NewRecorder()

	handler.MovieDetails(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastMovieQuery.TitleID != "tvdb:movie:1" || fake.lastMovieQuery.TMDBID != 123 || fake.lastMovieQuery.TVDBID != 456 {
		t.Fatalf("unexpected movie query captured: %+v", fake.lastMovieQuery)
	}

	var payload models.Title
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Name != "Example" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestMetadataHandler_MovieDetailsError(t *testing.T) {
	fake := &fakeMetadataService{movieErr: errors.New("down")}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/metadata/movies/details?titleId=x", nil)
	rec := httptest.NewRecorder()

	handler.MovieDetails(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] == "" {
		t.Fatalf("expected error payload, got %+v", payload)
	}
}

func TestMetadataHandler_SearchKidsContentListReturnsEmpty(t *testing.T) {
	fake := &fakeMetadataService{
		searchResp: []models.SearchResult{
			{Score: 80, Title: models.Title{Name: "Action Movie", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"kid1": {ID: "kid1", IsKidsProfile: true, KidsMode: "content_list"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=action&type=movie&userId=kid1", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload []models.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 0 {
		t.Fatalf("expected empty results for content_list kids, got %d", len(payload))
	}
}

func TestMetadataHandler_TopTenKidsRatingFilters(t *testing.T) {
	fake := &fakeMetadataService{
		trendingResp: []models.TrendingItem{
			{Title: models.Title{Name: "Kids Movie", MediaType: "movie", Certification: "G"}},
			{Title: models.Title{Name: "Adult Movie", MediaType: "movie", Certification: "R"}},
			{Title: models.Title{Name: "Unrated Movie", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"kid1": {ID: "kid1", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG", KidsMaxTVRating: "TV-PG"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/discover/top-ten?type=movie&userId=kid1", nil)
	rec := httptest.NewRecorder()

	handler.TopTen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	var payload TopTenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Title.Name != "Kids Movie" {
		t.Fatalf("expected only G-rated movie, got %+v", payload.Items)
	}
}

func TestMetadataHandler_TopTenNoFilterForAdult(t *testing.T) {
	fake := &fakeMetadataService{
		trendingResp: []models.TrendingItem{
			{Title: models.Title{Name: "Kids Movie", MediaType: "movie", Certification: "G"}},
			{Title: models.Title{Name: "Adult Movie", MediaType: "movie", Certification: "R"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"adult1": {ID: "adult1", IsKidsProfile: false},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/discover/top-ten?type=movie&userId=adult1", nil)
	rec := httptest.NewRecorder()
	handler.TopTen(rec, req)

	var payload TopTenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("expected no filtering for adult, got %d items", len(payload.Items))
	}
}

func TestMetadataHandler_SearchKidsRatingFilters(t *testing.T) {
	fake := &fakeMetadataService{
		searchResp: []models.SearchResult{
			{Score: 90, Title: models.Title{Name: "Kids Movie", MediaType: "movie", Certification: "G"}},
			{Score: 80, Title: models.Title{Name: "Adult Movie", MediaType: "movie", Certification: "R"}},
			{Score: 70, Title: models.Title{Name: "No Rating", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"kid2": {ID: "kid2", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG", KidsMaxTVRating: "TV-PG"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=movie&type=movie&userId=kid2", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload []models.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	// Only "Kids Movie" (G) should pass — "Adult Movie" (R) exceeds PG, "No Rating" blocked
	if len(payload) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(payload), payload)
	}
	if payload[0].Title.Name != "Kids Movie" {
		t.Fatalf("expected Kids Movie, got %q", payload[0].Title.Name)
	}
}

func TestMetadataHandler_SearchNormalUserUnfiltered(t *testing.T) {
	fake := &fakeMetadataService{
		searchResp: []models.SearchResult{
			{Score: 90, Title: models.Title{Name: "Movie A", MediaType: "movie"}},
			{Score: 80, Title: models.Title{Name: "Movie B", MediaType: "movie"}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"adult1": {ID: "adult1", IsKidsProfile: false},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=movie&type=movie&userId=adult1", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload []models.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("expected 2 unfiltered results for normal user, got %d", len(payload))
	}
}

func TestMetadataHandler_DiscoverByGenre(t *testing.T) {
	fake := &fakeMetadataService{
		discoverByGenreResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{Name: "Action Movie", MediaType: "movie", TMDBID: 100}},
			{Rank: 2, Title: models.Title{Name: "Action Movie 2", MediaType: "movie", TMDBID: 200}},
		},
		discoverByGenreTotal: 42,
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/genre?type=movie&genreId=28&limit=10&offset=0&lite=true&artworkLimit=20", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByGenre(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastDiscoverGenreType != "movie" {
		t.Fatalf("expected media type movie, got %q", fake.lastDiscoverGenreType)
	}
	if fake.lastDiscoverGenreID != 28 {
		t.Fatalf("expected genre ID 28, got %d", fake.lastDiscoverGenreID)
	}
	if fake.lastDiscoverGenreLimit != 10 {
		t.Fatalf("expected limit 10, got %d", fake.lastDiscoverGenreLimit)
	}
	if fake.lastDiscoverGenreOffset != 0 {
		t.Fatalf("expected offset 0, got %d", fake.lastDiscoverGenreOffset)
	}
	if !fake.lastDiscoverGenreOptions.Lite || fake.lastDiscoverGenreOptions.ArtworkLimit != 20 {
		t.Fatalf("unexpected discover genre options: %+v", fake.lastDiscoverGenreOptions)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type %q", got)
	}

	var payload DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(payload.Items))
	}
	if payload.Total != 42 {
		t.Fatalf("expected total 42, got %d", payload.Total)
	}
	if payload.Items[0].Title.Name != "Action Movie" {
		t.Fatalf("unexpected first item name: %q", payload.Items[0].Title.Name)
	}
}

func TestMetadataHandler_DiscoverByGenreMissingGenreID(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/genre?type=movie", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByGenre(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] != "genreId is required" {
		t.Fatalf("expected genreId required error, got %q", payload["error"])
	}
}

func TestMetadataHandler_DiscoverByGenreInvalidGenreID(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/genre?type=movie&genreId=abc", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByGenre(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] != "invalid genreId" {
		t.Fatalf("expected invalid genreId error, got %q", payload["error"])
	}
}

func TestMetadataHandler_DiscoverByGenreError(t *testing.T) {
	fake := &fakeMetadataService{
		discoverByGenreErr: errors.New("tmdb unavailable"),
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/genre?type=series&genreId=16", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByGenre(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] == "" {
		t.Fatalf("expected error message, got %v", payload)
	}
}

func TestMetadataHandler_DiscoverByGenreNilItems(t *testing.T) {
	fake := &fakeMetadataService{
		discoverByGenreResp:  nil,
		discoverByGenreTotal: 0,
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/genre?type=movie&genreId=28", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByGenre(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Items == nil {
		t.Fatalf("expected non-nil items array (should be empty slice)")
	}
	if len(payload.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(payload.Items))
	}
}

func TestMetadataHandler_DiscoverByDecade(t *testing.T) {
	fake := &fakeMetadataService{
		discoverByDecadeResp: []models.TrendingItem{
			{Rank: 1, Title: models.Title{Name: "Eighties Movie", MediaType: "movie", TMDBID: 100}},
			{Rank: 2, Title: models.Title{Name: "Eighties Movie 2", MediaType: "movie", TMDBID: 200}},
		},
		discoverByDecadeTotal: 42,
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/decade?type=movie&decade=1980&limit=10&offset=0&lite=true&artworkLimit=20", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByDecade(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if fake.lastDiscoverDecadeType != "movie" {
		t.Fatalf("expected media type movie, got %q", fake.lastDiscoverDecadeType)
	}
	if fake.lastDiscoverDecade != 1980 {
		t.Fatalf("expected decade 1980, got %d", fake.lastDiscoverDecade)
	}
	if fake.lastDiscoverDecadeLimit != 10 {
		t.Fatalf("expected limit 10, got %d", fake.lastDiscoverDecadeLimit)
	}
	if fake.lastDiscoverDecadeOffset != 0 {
		t.Fatalf("expected offset 0, got %d", fake.lastDiscoverDecadeOffset)
	}
	if !fake.lastDiscoverDecadeOptions.Lite || fake.lastDiscoverDecadeOptions.ArtworkLimit != 20 {
		t.Fatalf("unexpected discover decade options: %+v", fake.lastDiscoverDecadeOptions)
	}

	var payload DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(payload.Items))
	}
	if payload.Total != 42 {
		t.Fatalf("expected total 42, got %d", payload.Total)
	}
}

func TestMetadataHandler_DiscoverByDecadeMissingDecade(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/decade?type=movie", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByDecade(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["error"] != "decade is required" {
		t.Fatalf("expected decade required error, got %q", payload["error"])
	}
}

func TestMetadataHandler_DiscoverByDecadeInvalidDecade(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	for _, decade := range []string{"abc", "1985", "1890"} {
		req := httptest.NewRequest(http.MethodGet, "/api/discover/decade?type=movie&decade="+decade, nil)
		rec := httptest.NewRecorder()

		handler.DiscoverByDecade(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("decade %q: expected %d, got %d", decade, http.StatusBadRequest, rec.Code)
		}

		var payload map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decade %q: decode payload: %v", decade, err)
		}
		if payload["error"] != "invalid decade" {
			t.Fatalf("decade %q: expected invalid decade error, got %q", decade, payload["error"])
		}
	}
}

func TestMetadataHandler_DiscoverByDecadeError(t *testing.T) {
	fake := &fakeMetadataService{
		discoverByDecadeErr: errors.New("tmdb unavailable"),
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/decade?type=series&decade=1990", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverByDecade(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
	}
}

func TestMetadataHandler_GetAIRecommendationsMissingUserId(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/recommendations", nil)
	rec := httptest.NewRecorder()

	handler.GetAIRecommendations(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestMetadataHandler_GetAIRecommendationsEmptyHistory(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	// No history or watchlist services set — should return empty results

	req := httptest.NewRequest(http.MethodGet, "/api/recommendations?userId=user1", nil)
	rec := httptest.NewRecorder()

	handler.GetAIRecommendations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload DiscoverNewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(payload.Items))
	}
}

func TestMetadataHandler_GetPersonalizedRecommendations_UsesSimilarAndExcludesKnownItems(t *testing.T) {
	now := time.Now().UTC()
	title := func(mediaType string, tmdbID int64, name string, popularity float64) models.Title {
		idKind := "movie"
		if mediaType == "series" {
			idKind = "tv"
		}
		return models.Title{
			ID:         "tmdb:" + idKind + ":" + strconv.FormatInt(tmdbID, 10),
			Name:       name,
			MediaType:  mediaType,
			TMDBID:     tmdbID,
			Popularity: popularity,
		}
	}

	fake := &fakeMetadataService{
		similarByKey: map[string][]models.Title{
			"movie:1": {
				title("movie", 101, "Recommended Movie A", 30),
				title("movie", 2, "Progress Movie", 80),
				title("movie", 99, "Old Watched Movie", 90),
			},
			"movie:2": {
				title("movie", 102, "Recommended Movie B", 35),
			},
			"series:10": {
				title("series", 201, "Recommended Series A", 40),
			},
			"series:20": {
				title("series", 202, "Recommended Series B", 45),
			},
		},
		topTenByType: map[string][]models.TrendingItem{
			"all": {
				{Rank: 1, Title: title("movie", 102, "Recommended Movie B", 95)},
				{Rank: 2, Title: title("series", 202, "Recommended Series B", 93)},
			},
			"movie": {
				{Rank: 1, Title: title("movie", 102, "Recommended Movie B", 95)},
			},
			"tv": {
				{Rank: 1, Title: title("series", 202, "Recommended Series B", 93)},
			},
		},
		trendingByType: map[string][]models.TrendingItem{
			"movie": {
				{Rank: 1, Title: title("movie", 103, "Trending Movie", 70)},
			},
			"series": {
				{Rank: 1, Title: title("series", 203, "Trending Series", 70)},
			},
		},
		cachedArtwork: map[string]struct {
			textPoster   string
			textBackdrop string
			backdrops    []string
		}{
			"movie:101:0": {
				textPoster:   "https://example.test/movie-text-poster.jpg",
				textBackdrop: "https://example.test/movie-text-backdrop.jpg",
				backdrops:    []string{"https://example.test/movie-alt-1.jpg", "https://example.test/movie-alt-2.jpg"},
			},
			"series:201:0": {
				textBackdrop: "https://example.test/series-text-backdrop.jpg",
				backdrops:    []string{"https://example.test/series-alt-1.jpg"},
			},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.HistoryService = &fakeMetadataHistoryService{
		history: []models.WatchHistoryItem{
			{
				MediaType:   "movie",
				ItemID:      "tmdb:movie:1",
				Name:        "Watched Movie",
				Watched:     true,
				WatchedAt:   now.Add(-24 * time.Hour),
				ExternalIDs: map[string]string{"tmdb": "1"},
			},
			{
				MediaType:   "episode",
				ItemID:      "episode:1",
				Name:        "Pilot",
				Watched:     true,
				WatchedAt:   now.Add(-48 * time.Hour),
				SeriesID:    "tmdb:tv:10",
				SeriesName:  "Watched Series",
				ExternalIDs: map[string]string{"tmdb": "10"},
			},
			{
				MediaType:   "movie",
				ItemID:      "tmdb:movie:99",
				Name:        "Old Watched Movie",
				Watched:     true,
				WatchedAt:   now.AddDate(0, 0, -60),
				ExternalIDs: map[string]string{"tmdb": "99"},
			},
		},
		progress: []models.PlaybackProgress{
			{
				MediaType:      "movie",
				ItemID:         "tmdb:movie:2",
				MovieName:      "Progress Movie",
				PercentWatched: 50,
				UpdatedAt:      now.Add(-12 * time.Hour),
				ExternalIDs:    map[string]string{"tmdb": "2"},
			},
			{
				MediaType:      "episode",
				ItemID:         "episode:2",
				SeriesID:       "tmdb:tv:20",
				SeriesName:     "Progress Series",
				PercentWatched: 40,
				UpdatedAt:      now.Add(-6 * time.Hour),
				ExternalIDs:    map[string]string{"tmdb": "20"},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/recommendations/personalized?userId=user1&limitPerType=2", nil)
	rec := httptest.NewRecorder()

	handler.GetPersonalizedRecommendations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload PersonalizedRecommendationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Movies) != 2 {
		t.Fatalf("expected 2 movies, got %d (%+v)", len(payload.Movies), payload.Movies)
	}
	if len(payload.Series) != 2 {
		t.Fatalf("expected 2 series, got %d (%+v)", len(payload.Series), payload.Series)
	}
	if len(payload.Items) != 4 {
		t.Fatalf("expected 4 mixed items, got %d (%+v)", len(payload.Items), payload.Items)
	}
	if payload.Explanation == nil {
		t.Fatal("expected explanation payload")
	}
	if !strings.Contains(payload.Explanation.Summary, "Because you watched") {
		t.Fatalf("expected explanation summary to describe watched seeds, got %q", payload.Explanation.Summary)
	}
	if payload.Explanation.SeedCount == 0 || len(payload.Explanation.Seeds) == 0 {
		t.Fatalf("expected explanation seeds, got %+v", payload.Explanation)
	}
	seedSources := map[string]bool{}
	for _, seed := range payload.Explanation.Seeds {
		seedSources[seed.Source] = true
	}
	if !seedSources["watched"] || !seedSources["progress"] {
		t.Fatalf("expected watched and progress explanation seeds, got %+v", payload.Explanation.Seeds)
	}

	seen := map[int64]string{}
	for _, item := range payload.Items {
		seen[item.Title.TMDBID] = item.Title.Name
	}
	for _, tmdbID := range []int64{1, 2, 10, 20, 99} {
		if name, ok := seen[tmdbID]; ok {
			t.Fatalf("known item %d (%s) should have been excluded", tmdbID, name)
		}
	}
	for _, tmdbID := range []int64{101, 102, 201, 202} {
		if _, ok := seen[tmdbID]; !ok {
			t.Fatalf("expected recommendation tmdb=%d in mixed items, got %+v", tmdbID, seen)
		}
	}

	var movieA *models.Title
	var seriesA *models.Title
	for i := range payload.Items {
		switch payload.Items[i].Title.TMDBID {
		case 101:
			movieA = &payload.Items[i].Title
		case 201:
			seriesA = &payload.Items[i].Title
		}
	}
	if movieA == nil || movieA.TextBackdrop == nil || movieA.TextBackdrop.URL != "https://example.test/movie-text-backdrop.jpg" {
		t.Fatalf("expected movie recommendation text backdrop enrichment, got %+v", movieA)
	}
	if movieA.TextPoster == nil || movieA.TextPoster.URL != "https://example.test/movie-text-poster.jpg" {
		t.Fatalf("expected movie recommendation text poster enrichment, got %+v", movieA)
	}
	if len(movieA.Backdrops) != 2 || movieA.Backdrops[0].URL != "https://example.test/movie-alt-1.jpg" {
		t.Fatalf("expected movie recommendation alternate backdrops, got %+v", movieA.Backdrops)
	}
	if seriesA == nil || seriesA.TextBackdrop == nil || seriesA.TextBackdrop.URL != "https://example.test/series-text-backdrop.jpg" {
		t.Fatalf("expected series recommendation text backdrop enrichment, got %+v", seriesA)
	}
	if len(seriesA.Backdrops) != 1 || seriesA.Backdrops[0].URL != "https://example.test/series-alt-1.jpg" {
		t.Fatalf("expected series recommendation alternate backdrops, got %+v", seriesA.Backdrops)
	}
}

func TestMetadataHandler_GetPersonalizedRecommendations_FiltersKidsProfileRatings(t *testing.T) {
	now := time.Now().UTC()
	title := func(mediaType string, tmdbID int64, name, certification string) models.Title {
		idKind := "movie"
		if mediaType == "series" {
			idKind = "tv"
		}
		return models.Title{
			ID:            "tmdb:" + idKind + ":" + strconv.FormatInt(tmdbID, 10),
			Name:          name,
			MediaType:     mediaType,
			TMDBID:        tmdbID,
			Certification: certification,
			Popularity:    50,
		}
	}

	fake := &fakeMetadataService{
		similarByKey: map[string][]models.Title{
			"movie:1": {
				title("movie", 101, "Blocked Movie", "R"),
				title("movie", 102, "Allowed Movie", "PG"),
				title("movie", 103, "Unrated Movie", ""),
				title("movie", 104, "Backfill Movie", "G"),
			},
			"series:10": {
				title("series", 201, "Blocked Series", "TV-MA"),
				title("series", 202, "Allowed Series", "TV-PG"),
				title("series", 203, "Unrated Series", ""),
				title("series", 204, "Backfill Series", "TV-G"),
			},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))
	handler.SetUsersService(&fakeUsersServiceForSearch{
		users: map[string]models.User{
			"kid1": {
				ID:                 "kid1",
				IsKidsProfile:      true,
				KidsMode:           "rating",
				KidsMaxMovieRating: "PG",
				KidsMaxTVRating:    "TV-PG",
			},
		},
	})
	handler.HistoryService = &fakeMetadataHistoryService{
		history: []models.WatchHistoryItem{
			{
				MediaType:   "movie",
				ItemID:      "tmdb:movie:1",
				Name:        "Watched Movie",
				Watched:     true,
				WatchedAt:   now.Add(-24 * time.Hour),
				ExternalIDs: map[string]string{"tmdb": "1"},
			},
		},
		progress: []models.PlaybackProgress{
			{
				MediaType:      "episode",
				ItemID:         "episode:1",
				SeriesID:       "tmdb:tv:10",
				SeriesName:     "Watched Series",
				PercentWatched: 50,
				UpdatedAt:      now.Add(-12 * time.Hour),
				ExternalIDs:    map[string]string{"tmdb": "10"},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/recommendations/personalized?userId=kid1&limitPerType=2", nil)
	rec := httptest.NewRecorder()

	handler.GetPersonalizedRecommendations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload PersonalizedRecommendationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload.Total != 4 || len(payload.Items) != 4 || len(payload.Movies) != 2 || len(payload.Series) != 2 {
		t.Fatalf("expected full allowed recommendations, total=%d movies=%d series=%d items=%+v", payload.Total, len(payload.Movies), len(payload.Series), payload.Items)
	}
	got := map[string]bool{}
	for _, item := range payload.Items {
		got[item.Title.Name] = true
	}
	for _, name := range []string{"Allowed Movie", "Backfill Movie", "Allowed Series", "Backfill Series"} {
		if !got[name] {
			t.Fatalf("expected %q in filtered recommendations, got %+v", name, got)
		}
	}
	for _, name := range []string{"Blocked Movie", "Blocked Series", "Unrated Movie", "Unrated Series"} {
		if got[name] {
			t.Fatalf("did not expect %q in filtered recommendations, got %+v", name, got)
		}
	}
}

func TestMetadataHandler_BatchSeriesDetails_WithFields(t *testing.T) {
	fake := &fakeMetadataService{
		seriesResp: &models.SeriesDetails{
			Title: models.Title{
				ID:       "tvdb:series:123",
				Name:     "Test Show",
				Overview: "A test overview",
				Year:     2020,
				Genres:   []string{"Drama", "Sci-Fi"},
				TVDBID:   123,
			},
			Seasons: []models.SeriesSeason{{Number: 1}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	// Test with fields — should only return requested fields
	body := `{"queries":[{"name":"Test Show","tvdbId":123}],"fields":["overview"]}`
	req := httptest.NewRequest("POST", "/metadata/series/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.BatchSeriesDetails(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp models.BatchSeriesDetailsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	result := resp.Results[0]
	if result.Details == nil {
		t.Fatal("expected details to be non-nil")
	}
	if result.Details.Title.Overview != "A test overview" {
		t.Errorf("expected overview 'A test overview', got %q", result.Details.Title.Overview)
	}
	// Fields not requested should be zero values
	if result.Details.Title.Year != 0 {
		t.Errorf("expected year 0 (not requested), got %d", result.Details.Title.Year)
	}
	if len(result.Details.Title.Genres) != 0 {
		t.Errorf("expected no genres (not requested), got %v", result.Details.Title.Genres)
	}
	// Seasons should be nil (fields mode returns title only)
	if len(result.Details.Seasons) != 0 {
		t.Errorf("expected no seasons in fields mode, got %d", len(result.Details.Seasons))
	}
}

func TestMetadataHandler_BatchSeriesDetails_WithoutFields(t *testing.T) {
	fake := &fakeMetadataService{
		seriesResp: &models.SeriesDetails{
			Title: models.Title{
				ID:       "tvdb:series:123",
				Name:     "Test Show",
				Overview: "A test overview",
				Year:     2020,
				TVDBID:   123,
			},
			Seasons: []models.SeriesSeason{{Number: 1}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	// Test without fields — should return full details
	body := `{"queries":[{"name":"Test Show","tvdbId":123}]}`
	req := httptest.NewRequest("POST", "/metadata/series/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.BatchSeriesDetails(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp models.BatchSeriesDetailsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	result := resp.Results[0]
	if result.Details == nil {
		t.Fatal("expected details to be non-nil")
	}
	// Full mode should return everything including year and seasons
	if result.Details.Title.Year != 2020 {
		t.Errorf("expected year 2020, got %d", result.Details.Title.Year)
	}
	if len(result.Details.Seasons) != 1 {
		t.Errorf("expected 1 season, got %d", len(result.Details.Seasons))
	}
}

func TestMetadataHandler_BatchSeriesDetails_EmptyFields(t *testing.T) {
	fake := &fakeMetadataService{
		seriesResp: &models.SeriesDetails{
			Title: models.Title{
				ID:     "tvdb:series:123",
				Name:   "Test Show",
				Year:   2020,
				TVDBID: 123,
			},
			Seasons: []models.SeriesSeason{{Number: 1}},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	// Empty fields array should use full path (same as no fields)
	body := `{"queries":[{"name":"Test Show","tvdbId":123}],"fields":[]}`
	req := httptest.NewRequest("POST", "/metadata/series/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.BatchSeriesDetails(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp models.BatchSeriesDetailsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result := resp.Results[0]
	if result.Details == nil {
		t.Fatal("expected details to be non-nil")
	}
	// Empty fields = full path, so year and seasons should be present
	if result.Details.Title.Year != 2020 {
		t.Errorf("expected year 2020, got %d", result.Details.Title.Year)
	}
	if len(result.Details.Seasons) != 1 {
		t.Errorf("expected 1 season, got %d", len(result.Details.Seasons))
	}
}

func TestMetadataHandler_TopTen(t *testing.T) {
	items := []models.TrendingItem{
		{Rank: 1, Title: models.Title{Name: "Inception", MediaType: "movie", IMDBID: "tt1375666", Popularity: 90}},
		{Rank: 2, Title: models.Title{Name: "Breaking Bad", MediaType: "series", IMDBID: "tt0903747", Popularity: 85}},
	}
	fake := &fakeMetadataService{trendingResp: items}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/top-ten", nil)
	rec := httptest.NewRecorder()

	handler.TopTen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp DiscoverNewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Items))
	}
	if resp.Total != 2 {
		t.Errorf("expected Total=2, got %d", resp.Total)
	}
}

func TestMetadataHandler_TopTen_EmptyResult(t *testing.T) {
	fake := &fakeMetadataService{trendingResp: nil}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/discover/top-ten?type=movie", nil)
	rec := httptest.NewRecorder()

	handler.TopTen(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp DiscoverNewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(resp.Items))
	}
}

func TestMetadataHandler_SearchYouTubeVideos_MissingQuery(t *testing.T) {
	fake := &fakeMetadataService{}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/youtube/search", nil)
	rec := httptest.NewRecorder()

	handler.SearchYouTubeVideos(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMetadataHandler_SearchYouTubeVideos_ClampsLimit(t *testing.T) {
	fake := &fakeMetadataService{
		youtubeResp: []models.YouTubeVideoSearchResult{
			{ID: "abc123", URL: "https://www.youtube.com/watch?v=abc123", Title: "Test Video"},
		},
	}
	handler := NewMetadataHandler(fake, testConfigManager(t))

	req := httptest.NewRequest(http.MethodGet, "/api/youtube/search?q=test&limit=99", nil)
	rec := httptest.NewRecorder()

	handler.SearchYouTubeVideos(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fake.lastYouTubeQuery != "test" {
		t.Fatalf("expected query test, got %q", fake.lastYouTubeQuery)
	}
	if fake.lastYouTubeLimit != 20 {
		t.Fatalf("expected clamped limit 20, got %d", fake.lastYouTubeLimit)
	}

	var resp []models.YouTubeVideoSearchResult
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 || resp[0].ID != "abc123" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

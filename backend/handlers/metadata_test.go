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
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/services/metadata"
)

type fakeMetadataService struct {
	trendingResp []models.TrendingItem
	trendingErr  error
	searchResp   []models.SearchResult
	searchErr    error
	seriesResp   *models.SeriesDetails
	seriesErr    error
	movieResp    *models.Title
	movieErr     error

	discoverByGenreResp  []models.TrendingItem
	discoverByGenreTotal int
	discoverByGenreErr   error

	lastTrendingType        string
	lastSearchQuery         string
	lastSearchType          string
	lastSeriesQuery         models.SeriesDetailsQuery
	lastMovieQuery          models.MovieDetailsQuery
	lastDiscoverGenreType   string
	lastDiscoverGenreID     int64
	lastDiscoverGenreLimit  int
	lastDiscoverGenreOffset int
}

func (f *fakeMetadataService) Trending(_ context.Context, mediaType string) ([]models.TrendingItem, error) {
	f.lastTrendingType = mediaType
	return f.trendingResp, f.trendingErr
}

func (f *fakeMetadataService) Search(_ context.Context, query, mediaType string) ([]models.SearchResult, error) {
	f.lastSearchQuery = query
	f.lastSearchType = mediaType
	return f.searchResp, f.searchErr
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

func (f *fakeMetadataService) GetCustomList(_ context.Context, _ string, _ metadata.CustomListOptions) ([]models.TrendingItem, int, int, error) {
	return nil, 0, 0, nil
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

func (f *fakeMetadataService) Similar(_ context.Context, _ string, _ int64) ([]models.Title, error) {
	return nil, nil
}

func (f *fakeMetadataService) DiscoverByGenre(_ context.Context, mediaType string, genreID int64, limit, offset int) ([]models.TrendingItem, int, error) {
	f.lastDiscoverGenreType = mediaType
	f.lastDiscoverGenreID = genreID
	f.lastDiscoverGenreLimit = limit
	f.lastDiscoverGenreOffset = offset
	return f.discoverByGenreResp, f.discoverByGenreTotal, f.discoverByGenreErr
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

func (f *fakeMetadataService) GetProgressSnapshot() metadata.ProgressSnapshot {
	return metadata.ProgressSnapshot{}
}

// fakeUsersServiceForSearch implements usersServiceInterface for search handler tests.
type fakeUsersServiceForSearch struct {
	users map[string]models.User
}

func (f *fakeUsersServiceForSearch) Get(id string) (models.User, bool) {
	u, ok := f.users[id]
	return u, ok
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

	req := httptest.NewRequest(http.MethodGet, "/api/discover/new?type=Movie", nil)
	rec := httptest.NewRecorder()

	handler.DiscoverNew(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastTrendingType != "movie" {
		t.Fatalf("expected media type to normalize to movie, got %q", fake.lastTrendingType)
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

	req := httptest.NewRequest(http.MethodGet, "/api/discover/genre?type=movie&genreId=28&limit=10&offset=0", nil)
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

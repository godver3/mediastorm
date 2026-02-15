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

	lastTrendingType string
	lastSearchQuery  string
	lastSearchType   string
	lastSeriesQuery  models.SeriesDetailsQuery
	lastMovieQuery   models.MovieDetailsQuery
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

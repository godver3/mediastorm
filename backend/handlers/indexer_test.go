package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/models"
	"novastream/services/indexer"
)

type fakeIndexerService struct {
	results  []models.NZBResult
	err      error
	lastOpts indexer.SearchOptions
}

type fakeMovieMetadataService struct {
	title *models.Title
	err   error
}

func (f *fakeMovieMetadataService) MovieInfo(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.title, nil
}

func (f *fakeIndexerService) Search(_ context.Context, opts indexer.SearchOptions) ([]models.NZBResult, error) {
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func (f *fakeIndexerService) SearchTest(_ context.Context, opts indexer.SearchOptions) ([]models.ScoredNZBResult, error) {
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	scored := make([]models.ScoredNZBResult, len(f.results))
	for i, r := range f.results {
		scored[i] = models.ScoredNZBResult{NZBResult: r, FilterStatus: "passed"}
	}
	return scored, nil
}

func (f *fakeIndexerService) SearchWithScoring(_ context.Context, opts indexer.SearchOptions) ([]models.ScoredNZBResult, error) {
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	scored := make([]models.ScoredNZBResult, len(f.results))
	for i, r := range f.results {
		scored[i] = models.ScoredNZBResult{NZBResult: r, FilterStatus: "passed"}
	}
	return scored, nil
}

func TestIndexerHandler_Search(t *testing.T) {
	fake := &fakeIndexerService{
		results: []models.NZBResult{{Title: "The Expanse", Indexer: "nzbPlanet", SizeBytes: 1234}},
	}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=The+Expanse&limit=2&cat=5000&cat=5040", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastOpts.Query != "The Expanse" {
		t.Fatalf("unexpected query captured: %q", fake.lastOpts.Query)
	}
	if fake.lastOpts.MaxResults != 2 {
		t.Fatalf("expected limit 2, got %d", fake.lastOpts.MaxResults)
	}
	if len(fake.lastOpts.Categories) != 2 {
		t.Fatalf("expected categories to pass through, got %+v", fake.lastOpts.Categories)
	}

	var payload []models.NZBResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 1 || payload[0].Title != "The Expanse" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestIndexerHandler_SearchDownloadRanking(t *testing.T) {
	fake := &fakeIndexerService{
		results: []models.NZBResult{{Title: "The Expanse", Indexer: "nzbPlanet", SizeBytes: 1234}},
	}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=The+Expanse&downloadRanking=true", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if !fake.lastOpts.UseDownloadRanking {
		t.Fatal("expected UseDownloadRanking=true to be forwarded to search service")
	}
}

func TestIndexerHandler_SearchDefaultLimit(t *testing.T) {
	fake := &fakeIndexerService{results: []models.NZBResult{}}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=expanse&limit=invalid", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastOpts.MaxResults != 5 {
		t.Fatalf("expected default limit 5, got %d", fake.lastOpts.MaxResults)
	}
}

func TestIndexerHandler_SearchError(t *testing.T) {
	fake := &fakeIndexerService{err: errors.New("indexer down")}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=expanse", nil)
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

func TestIndexerHandler_SearchMovieAnimeDetection(t *testing.T) {
	fake := &fakeIndexerService{results: []models.NZBResult{}}
	movieSvc := &fakeMovieMetadataService{
		title: &models.Title{
			Genres:       []string{"Animation", "Fantasy"},
			OriginalName: "千と千尋の神隠し",
		},
	}
	handler := NewIndexerHandler(fake, false)
	handler.SetMovieMetadataService(movieSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=Spirited+Away&mediaType=movie&year=2001", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if !fake.lastOpts.IsAnime {
		t.Fatal("expected IsAnime=true for anime movie, got false")
	}
}

func TestIndexerHandler_SearchMovieNonAnime(t *testing.T) {
	fake := &fakeIndexerService{results: []models.NZBResult{}}
	movieSvc := &fakeMovieMetadataService{
		title: &models.Title{Genres: []string{"Animation", "Family"}},
	}
	handler := NewIndexerHandler(fake, false)
	handler.SetMovieMetadataService(movieSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=John+Wick&mediaType=movie&year=2014", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if fake.lastOpts.IsAnime {
		t.Fatal("expected IsAnime=false for non-anime movie, got true")
	}
}

func TestIndexerHandler_SearchTest(t *testing.T) {
	fake := &fakeIndexerService{
		results: []models.NZBResult{
			{Title: "The Expanse S01E01", Indexer: "nzbPlanet", SizeBytes: 1234},
		},
	}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search-test?q=The+Expanse+S01E01&mediaType=series&limit=50", nil)
	rec := httptest.NewRecorder()

	handler.SearchTest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload []models.ScoredNZBResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("expected 1 result, got %d", len(payload))
	}
	if payload[0].FilterStatus != "passed" {
		t.Fatalf("expected filterStatus=passed, got %q", payload[0].FilterStatus)
	}
	if payload[0].Title != "The Expanse S01E01" {
		t.Fatalf("expected title 'The Expanse S01E01', got %q", payload[0].Title)
	}
}

func TestIndexerHandler_SearchTestDownloadRanking(t *testing.T) {
	fake := &fakeIndexerService{
		results: []models.NZBResult{
			{Title: "The Expanse S01E01", Indexer: "nzbPlanet", SizeBytes: 1234},
		},
	}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search-test?q=The+Expanse+S01E01&mediaType=series&downloadRanking=true", nil)
	rec := httptest.NewRecorder()

	handler.SearchTest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if !fake.lastOpts.UseDownloadRanking {
		t.Fatal("expected UseDownloadRanking=true to be forwarded to indexer service")
	}
}

func TestIndexerHandler_SearchTestError(t *testing.T) {
	fake := &fakeIndexerService{err: errors.New("indexer down")}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search-test?q=test", nil)
	rec := httptest.NewRecorder()

	handler.SearchTest(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
	}
}

func TestIndexerHandler_SearchIncludeFiltered(t *testing.T) {
	fake := &fakeIndexerService{
		results: []models.NZBResult{
			{Title: "Movie 2024", Indexer: "test"},
		},
	}
	handler := NewIndexerHandler(fake, false)

	req := httptest.NewRequest(http.MethodGet, "/api/indexers/search?q=Movie&includeFiltered=true", nil)
	rec := httptest.NewRecorder()

	handler.Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var payload []models.ScoredNZBResult
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("expected 1 result, got %d", len(payload))
	}
	if payload[0].FilterStatus != "passed" {
		t.Fatalf("expected filterStatus=passed, got %q", payload[0].FilterStatus)
	}
}

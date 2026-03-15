package handlers

import (
	"context"
	"testing"
	"time"

	"novastream/models"
)

// mockMetadataForRatings implements the metadataService methods needed by enrichWatchlistRatings.
type mockMetadataForRatings struct {
	metadataService
	enabled      bool
	cachedRatings map[string][]models.Rating // keyed by imdbID — returned by cached lookup
	ratings      map[string][]models.Rating // keyed by imdbID — returned by full fetch (background warm)
}

func (m *mockMetadataForRatings) MDBListIsEnabled() bool {
	return m.enabled
}

func (m *mockMetadataForRatings) GetMDBListAllRatings(_ context.Context, imdbID, _ string) ([]models.Rating, error) {
	if m.ratings == nil {
		return nil, nil
	}
	return m.ratings[imdbID], nil
}

func (m *mockMetadataForRatings) GetMDBListAllRatingsCached(imdbID, _ string) []models.Rating {
	if m.cachedRatings == nil {
		return nil
	}
	return m.cachedRatings[imdbID]
}

func TestEnrichWatchlistRatings_Disabled(t *testing.T) {
	items := []models.WatchlistItem{
		{ID: "1", ExternalIDs: map[string]string{"imdb": "tt1234567"}},
	}
	meta := &mockMetadataForRatings{enabled: false}
	enrichWatchlistRatings(context.Background(), items, meta)

	if items[0].Ratings != nil {
		t.Errorf("expected nil ratings when MDBList disabled, got %v", items[0].Ratings)
	}
}

func TestEnrichWatchlistRatings_NilService(t *testing.T) {
	items := []models.WatchlistItem{
		{ID: "1", ExternalIDs: map[string]string{"imdb": "tt1234567"}},
	}
	enrichWatchlistRatings(context.Background(), items, nil)

	if items[0].Ratings != nil {
		t.Errorf("expected nil ratings with nil service, got %v", items[0].Ratings)
	}
}

func TestEnrichWatchlistRatings_SkipsNoIMDB(t *testing.T) {
	items := []models.WatchlistItem{
		{ID: "1", ExternalIDs: map[string]string{"tmdb": "999"}},
	}
	meta := &mockMetadataForRatings{
		enabled:      true,
		cachedRatings: map[string][]models.Rating{},
	}
	enrichWatchlistRatings(context.Background(), items, meta)

	if items[0].Ratings != nil {
		t.Errorf("expected nil ratings for item without IMDB ID, got %v", items[0].Ratings)
	}
}

func TestEnrichWatchlistRatings_SetsCachedRatings(t *testing.T) {
	items := []models.WatchlistItem{
		{ID: "1", MediaType: "movie", ExternalIDs: map[string]string{"imdb": "tt1234567"}},
		{ID: "2", MediaType: "series", ExternalIDs: map[string]string{"imdb": "tt7654321"}},
		{ID: "3", MediaType: "movie", ExternalIDs: map[string]string{"tmdb": "999"}}, // no IMDB
	}
	meta := &mockMetadataForRatings{
		enabled: true,
		cachedRatings: map[string][]models.Rating{
			"tt1234567": {{Source: "imdb", Value: 7.5, Max: 10}},
			"tt7654321": {{Source: "tmdb", Value: 8.0, Max: 10}, {Source: "trakt", Value: 7.8, Max: 10}},
		},
	}
	enrichWatchlistRatings(context.Background(), items, meta)

	if len(items[0].Ratings) != 1 || items[0].Ratings[0].Value != 7.5 {
		t.Errorf("expected 1 rating with value 7.5 for item 0, got %v", items[0].Ratings)
	}
	if len(items[1].Ratings) != 2 {
		t.Errorf("expected 2 ratings for item 1, got %v", items[1].Ratings)
	}
	if items[2].Ratings != nil {
		t.Errorf("expected nil ratings for item 2 (no IMDB), got %v", items[2].Ratings)
	}
}

func TestEnrichWatchlistRatings_CacheMissTriggersBackgroundWarm(t *testing.T) {
	items := []models.WatchlistItem{
		{ID: "1", MediaType: "movie", ExternalIDs: map[string]string{"imdb": "tt1234567"}},
	}
	meta := &mockMetadataForRatings{
		enabled:      true,
		cachedRatings: map[string][]models.Rating{}, // empty cache = miss
		ratings: map[string][]models.Rating{
			"tt1234567": {{Source: "imdb", Value: 7.5, Max: 10}},
		},
	}
	enrichWatchlistRatings(context.Background(), items, meta)

	// Ratings should NOT be set inline (cache miss)
	if items[0].Ratings != nil {
		t.Errorf("expected nil ratings on cache miss (background warm), got %v", items[0].Ratings)
	}

	// Give the background goroutine time to complete
	time.Sleep(100 * time.Millisecond)
}

func TestEnrichTrendingRatings_SetsCachedRatings(t *testing.T) {
	items := []models.TrendingItem{
		{Rank: 1, Title: models.Title{IMDBID: "tt1234567", MediaType: "movie"}},
		{Rank: 2, Title: models.Title{IMDBID: "tt7654321", MediaType: "series"}},
		{Rank: 3, Title: models.Title{IMDBID: "", MediaType: "movie"}}, // no IMDB
	}
	meta := &mockMetadataForRatings{
		enabled: true,
		cachedRatings: map[string][]models.Rating{
			"tt1234567": {{Source: "imdb", Value: 8.1, Max: 10}},
			"tt7654321": {{Source: "tmdb", Value: 7.0, Max: 10}},
		},
	}
	enrichTrendingRatings(items, meta)

	if len(items[0].Title.Ratings) != 1 || items[0].Title.Ratings[0].Value != 8.1 {
		t.Errorf("expected 1 rating with value 8.1 for item 0, got %v", items[0].Title.Ratings)
	}
	if len(items[1].Title.Ratings) != 1 {
		t.Errorf("expected 1 rating for item 1, got %v", items[1].Title.Ratings)
	}
	if items[2].Title.Ratings != nil {
		t.Errorf("expected nil ratings for item 2 (no IMDB), got %v", items[2].Title.Ratings)
	}
}

func TestEnrichTrendingRatings_Disabled(t *testing.T) {
	items := []models.TrendingItem{
		{Rank: 1, Title: models.Title{IMDBID: "tt1234567", MediaType: "movie"}},
	}
	meta := &mockMetadataForRatings{enabled: false}
	enrichTrendingRatings(items, meta)

	if items[0].Title.Ratings != nil {
		t.Errorf("expected nil ratings when disabled, got %v", items[0].Title.Ratings)
	}
}

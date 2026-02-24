package handlers

import (
	"testing"

	"novastream/models"
)

func TestBuildWatchStateIndex_Empty(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	state, unwatched := idx.compute("movie", "test-id")
	if state != "none" {
		t.Errorf("expected 'none', got %q", state)
	}
	if unwatched != nil {
		t.Errorf("expected nil unwatched, got %v", unwatched)
	}
}

func TestCompute_MovieWatched(t *testing.T) {
	idx := buildWatchStateIndex(
		[]models.WatchHistoryItem{
			{MediaType: "movie", ItemID: "tmdb:movie:123", Watched: true},
		},
		nil, nil,
	)

	state, unwatched := idx.compute("movie", "tmdb:movie:123")
	if state != "complete" {
		t.Errorf("expected 'complete', got %q", state)
	}
	if unwatched != nil {
		t.Errorf("expected nil unwatched for movie, got %v", unwatched)
	}
}

func TestCompute_MoviePartialProgress(t *testing.T) {
	idx := buildWatchStateIndex(
		nil, nil,
		[]models.PlaybackProgress{
			{MediaType: "movie", ItemID: "tmdb:movie:456", PercentWatched: 45.0},
		},
	)

	state, _ := idx.compute("movie", "tmdb:movie:456")
	if state != "partial" {
		t.Errorf("expected 'partial', got %q", state)
	}
}

func TestCompute_MovieCompleteByProgress(t *testing.T) {
	idx := buildWatchStateIndex(
		nil, nil,
		[]models.PlaybackProgress{
			{MediaType: "movie", ItemID: "tmdb:movie:789", PercentWatched: 92.0},
		},
	)

	state, _ := idx.compute("movie", "tmdb:movie:789")
	if state != "complete" {
		t.Errorf("expected 'complete' (>=90%%), got %q", state)
	}
}

func TestCompute_MovieNone(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	state, _ := idx.compute("movie", "tmdb:movie:999")
	if state != "none" {
		t.Errorf("expected 'none', got %q", state)
	}
}

func TestCompute_SeriesMarkedWatched(t *testing.T) {
	idx := buildWatchStateIndex(
		[]models.WatchHistoryItem{
			{MediaType: "series", ItemID: "tvdb:100", Watched: true},
		},
		nil, nil,
	)

	state, _ := idx.compute("series", "tvdb:100")
	if state != "complete" {
		t.Errorf("expected 'complete', got %q", state)
	}
}

func TestCompute_SeriesAllEpisodesWatched(t *testing.T) {
	idx := buildWatchStateIndex(
		nil,
		[]models.SeriesWatchState{
			{SeriesID: "tvdb:200", TotalEpisodeCount: 10, WatchedEpisodeCount: 10},
		},
		nil,
	)

	state, unwatched := idx.compute("series", "tvdb:200")
	if state != "complete" {
		t.Errorf("expected 'complete', got %q", state)
	}
	if unwatched == nil || *unwatched != 0 {
		t.Errorf("expected unwatched=0, got %v", unwatched)
	}
}

func TestCompute_SeriesPartialEpisodes(t *testing.T) {
	idx := buildWatchStateIndex(
		[]models.WatchHistoryItem{
			{MediaType: "episode", SeriesID: "tvdb:300", Watched: true, SeasonNumber: 1},
		},
		[]models.SeriesWatchState{
			{SeriesID: "tvdb:300", TotalEpisodeCount: 20, WatchedEpisodeCount: 5},
		},
		nil,
	)

	state, unwatched := idx.compute("series", "tvdb:300")
	if state != "partial" {
		t.Errorf("expected 'partial', got %q", state)
	}
	if unwatched == nil || *unwatched != 15 {
		t.Errorf("expected unwatched=15, got %v", unwatched)
	}
}

func TestCompute_SeriesPartialFromProgress(t *testing.T) {
	idx := buildWatchStateIndex(
		nil,
		[]models.SeriesWatchState{
			{SeriesID: "tvdb:400", TotalEpisodeCount: 12, WatchedEpisodeCount: 3, PercentWatched: 25.0},
		},
		nil,
	)

	state, _ := idx.compute("series", "tvdb:400")
	if state != "partial" {
		t.Errorf("expected 'partial', got %q", state)
	}
}

func TestCompute_SeriesNone(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	state, unwatched := idx.compute("series", "tvdb:999")
	if state != "none" {
		t.Errorf("expected 'none', got %q", state)
	}
	if unwatched != nil {
		t.Errorf("expected nil unwatched for unknown series, got %v", unwatched)
	}
}

func TestCompute_SpecialEpisodesIgnored(t *testing.T) {
	// Season 0 (specials) should not count as hasWatchedEps
	idx := buildWatchStateIndex(
		[]models.WatchHistoryItem{
			{MediaType: "episode", SeriesID: "tvdb:500", Watched: true, SeasonNumber: 0},
		},
		nil, nil,
	)

	state, _ := idx.compute("series", "tvdb:500")
	if state != "none" {
		t.Errorf("expected 'none' (specials don't count), got %q", state)
	}
}

func TestEnrichWatchlistItems(t *testing.T) {
	idx := buildWatchStateIndex(
		[]models.WatchHistoryItem{
			{MediaType: "movie", ItemID: "m1", Watched: true},
		},
		[]models.SeriesWatchState{
			{SeriesID: "s1", TotalEpisodeCount: 10, WatchedEpisodeCount: 3, PercentWatched: 30.0},
		},
		nil,
	)

	items := []models.WatchlistItem{
		{ID: "m1", MediaType: "movie"},
		{ID: "s1", MediaType: "series"},
		{ID: "m2", MediaType: "movie"},
	}

	enrichWatchlistItems(items, idx)

	if items[0].WatchState != "complete" {
		t.Errorf("movie m1: expected 'complete', got %q", items[0].WatchState)
	}
	if items[1].WatchState != "partial" {
		t.Errorf("series s1: expected 'partial', got %q", items[1].WatchState)
	}
	if items[1].UnwatchedCount == nil || *items[1].UnwatchedCount != 7 {
		t.Errorf("series s1: expected unwatched=7, got %v", items[1].UnwatchedCount)
	}
	if items[2].WatchState != "none" {
		t.Errorf("movie m2: expected 'none', got %q", items[2].WatchState)
	}
}

func TestEnrichTrendingItems(t *testing.T) {
	idx := buildWatchStateIndex(
		nil, nil,
		[]models.PlaybackProgress{
			{MediaType: "movie", ItemID: "tmdb:movie:50", PercentWatched: 95.0},
		},
	)

	items := []models.TrendingItem{
		{Rank: 1, Title: models.Title{ID: "tmdb:movie:50", MediaType: "movie", TMDBID: 50}},
		{Rank: 2, Title: models.Title{ID: "tvdb:60", MediaType: "series", TVDBID: 60}},
	}

	enrichTrendingItems(items, idx)

	if items[0].Title.WatchState != "complete" {
		t.Errorf("movie: expected 'complete', got %q", items[0].Title.WatchState)
	}
	if items[1].Title.WatchState != "none" {
		t.Errorf("series: expected 'none', got %q", items[1].Title.WatchState)
	}
}

func TestCompute_UnknownMediaType(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	state, unwatched := idx.compute("podcast", "id-1")
	if state != "none" {
		t.Errorf("expected 'none' for unknown media type, got %q", state)
	}
	if unwatched != nil {
		t.Errorf("expected nil unwatched for unknown media type, got %v", unwatched)
	}
}

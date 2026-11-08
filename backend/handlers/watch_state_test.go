package handlers

import (
	"testing"

	"novastream/models"
)

type fakeReleasedEpisodeCountProvider struct {
	counts map[string]int
	warmed []models.SeriesDetailsQuery
}

func (f *fakeReleasedEpisodeCountProvider) GetCachedReleasedEpisodeCount(query models.SeriesDetailsQuery) (int, bool) {
	count, ok := f.counts[query.TitleID]
	return count, ok
}

func (f *fakeReleasedEpisodeCountProvider) WarmReleasedEpisodeCount(query models.SeriesDetailsQuery) {
	f.warmed = append(f.warmed, query)
}

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

func TestCompute_SeriesNoneWithKnownEpisodeTotal(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	idx.setSeriesEpisodeTotal("tvdb:999", map[string]string{"tvdb": "999"}, 12)

	state, unwatched := idx.computeWithExternalIDs("series", "tmdb:tv:123", map[string]string{"tvdb": "999"})
	if state != "none" {
		t.Errorf("expected 'none', got %q", state)
	}
	if unwatched == nil || *unwatched != 12 {
		t.Errorf("expected unwatched=12, got %v", unwatched)
	}
}

func TestCompute_SeriesUsesWatchedEpisodeHistoryWithKnownTotal(t *testing.T) {
	idx := buildWatchStateIndex(
		[]models.WatchHistoryItem{
			{MediaType: "episode", SeriesID: "tvdb:999", ExternalIDs: map[string]string{"tvdb": "999"}, Watched: true, SeasonNumber: 1, EpisodeNumber: 1},
			{MediaType: "episode", SeriesID: "tvdb:999", ExternalIDs: map[string]string{"tvdb": "999"}, Watched: true, SeasonNumber: 1, EpisodeNumber: 2},
			// Duplicate aliases should not inflate the watched count.
			{MediaType: "episode", SeriesID: "tmdb:tv:123", ExternalIDs: map[string]string{"tvdb": "999"}, Watched: true, SeasonNumber: 1, EpisodeNumber: 2},
		},
		nil,
		nil,
	)
	idx.setSeriesEpisodeTotal("tvdb:999", map[string]string{"tvdb": "999"}, 12)

	state, unwatched := idx.computeWithExternalIDs("series", "tmdb:tv:123", map[string]string{"tvdb": "999"})
	if state != "partial" {
		t.Errorf("expected 'partial', got %q", state)
	}
	if unwatched == nil {
		t.Fatal("expected unwatched=10, got nil")
	}
	if *unwatched != 10 {
		t.Errorf("expected unwatched=10, got %d", *unwatched)
	}
}

func TestAddCachedWatchlistEpisodeTotals_UsesCacheAndWarmsMisses(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	provider := &fakeReleasedEpisodeCountProvider{counts: map[string]int{"cached": 8}}
	items := []models.WatchlistItem{
		{ID: "cached", MediaType: "series", ExternalIDs: map[string]string{"tvdb": "100"}},
		{ID: "missing", MediaType: "series", ExternalIDs: map[string]string{"tvdb": "200"}},
		{ID: "movie", MediaType: "movie"},
	}

	enrichWatchlistItems(items, idx, provider, true)

	state, unwatched := idx.computeWithExternalIDs("series", "alias", map[string]string{"tvdb": "100"})
	if state != "none" || unwatched == nil || *unwatched != 8 {
		t.Fatalf("cached series = state %q unwatched %v, want none/8", state, unwatched)
	}
	if len(provider.warmed) != 1 || provider.warmed[0].TitleID != "missing" {
		t.Fatalf("warmed queries = %#v, want only missing series", provider.warmed)
	}
}

func TestAddCachedTrendingEpisodeTotals_UsesCacheAndWarmsMisses(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	provider := &fakeReleasedEpisodeCountProvider{counts: map[string]int{"tvdb:100": 8}}
	items := []models.TrendingItem{
		{Title: models.Title{ID: "tvdb:series:100", Name: "Cached", MediaType: "series", TVDBID: 100}},
		{Title: models.Title{ID: "tvdb:series:200", Name: "Missing", MediaType: "series", TVDBID: 200}},
		{Title: models.Title{ID: "tmdb:movie:300", Name: "Movie", MediaType: "movie", TMDBID: 300}},
	}

	enrichTrendingItems(items, idx, provider, true)

	if items[0].Title.WatchState != "none" || items[0].Title.UnwatchedCount == nil || *items[0].Title.UnwatchedCount != 8 {
		t.Fatalf("cached series = state %q unwatched %v, want none/8", items[0].Title.WatchState, items[0].Title.UnwatchedCount)
	}
	if len(provider.warmed) != 1 || provider.warmed[0].TitleID != "tvdb:200" {
		t.Fatalf("warmed queries = %#v, want only missing series", provider.warmed)
	}
}

func TestEnrichSearchResultsWithCachedEpisodeCounts_DoesNotWarmMisses(t *testing.T) {
	idx := buildWatchStateIndex(nil, nil, nil)
	provider := &fakeReleasedEpisodeCountProvider{counts: map[string]int{"tvdb:100": 8}}
	results := []models.SearchResult{
		{Title: models.Title{ID: "tvdb:series:100", Name: "Cached", MediaType: "series", TVDBID: 100}},
		{Title: models.Title{ID: "tvdb:series:200", Name: "Missing", MediaType: "series", TVDBID: 200}},
	}

	enrichSearchResultsWithCachedEpisodeCounts(results, idx, provider)

	if results[0].Title.UnwatchedCount == nil || *results[0].Title.UnwatchedCount != 8 {
		t.Fatalf("cached search result unwatched = %v, want 8", results[0].Title.UnwatchedCount)
	}
	if results[1].Title.UnwatchedCount != nil {
		t.Fatalf("missing search result unwatched = %v, want nil", results[1].Title.UnwatchedCount)
	}
	if len(provider.warmed) != 0 {
		t.Fatalf("search warmed provider queries: %#v", provider.warmed)
	}
}

func TestCachedEpisodeCountsManifestHashChangesWhenWarmCompletes(t *testing.T) {
	items := []models.WatchlistItem{{ID: "series-1", MediaType: "series"}}
	provider := &fakeReleasedEpisodeCountProvider{counts: map[string]int{}}
	missingHash := cachedEpisodeCountsManifestHash(items, provider)

	provider.counts["series-1"] = 8
	cachedHash := cachedEpisodeCountsManifestHash(items, provider)
	if missingHash == cachedHash {
		t.Fatalf("manifest hash did not change after episode count became cached: %q", cachedHash)
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

	enrichWatchlistItems(items, idx, nil, false)

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

	enrichTrendingItems(items, idx, nil, false)

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

// episodeCountMetadataService is a metadata service that also serves cached
// released-episode totals, as the real metadata service does.
type episodeCountMetadataService struct {
	*fakeMetadataService
	*fakeReleasedEpisodeCountProvider
}

func TestQueryTrendingList_WarmsEpisodeCountsOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		include   bool
		wantWarms int
	}{
		{"count off", false, 0},
		{"count on", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewMetadataHandler(&fakeMetadataService{}, testConfigManager(t))
			handler.HistoryService = &fakeMetadataHistoryService{}
			provider := &fakeReleasedEpisodeCountProvider{counts: map[string]int{"tvdb:100": 8}}
			service := episodeCountMetadataService{&fakeMetadataService{}, provider}
			items := []models.TrendingItem{
				{Title: models.Title{ID: "tvdb:series:100", Name: "Cached", MediaType: "series", TVDBID: 100}},
				{Title: models.Title{ID: "tvdb:series:200", Name: "Missing", MediaType: "series", TVDBID: 200}},
			}

			items, _, _ = handler.queryTrendingList("user1", service, items, displayListQueryOptions{IncludeUnwatchedCounts: tc.include})

			if items[0].Title.UnwatchedCount == nil || *items[0].Title.UnwatchedCount != 8 {
				t.Fatalf("cached series unwatched = %v, want 8", items[0].Title.UnwatchedCount)
			}
			if len(provider.warmed) != tc.wantWarms {
				t.Fatalf("warmed = %#v, want %d", provider.warmed, tc.wantWarms)
			}
		})
	}
}

func TestCompute_SeriesUnionsEpisodesAcrossIDForms(t *testing.T) {
	// Season 1 recorded under tvdb-only rows, season 2 under tmdb-only rows:
	// the title carries both IDs, so it must count both seasons, not the larger.
	var history []models.WatchHistoryItem
	for ep := 1; ep <= 3; ep++ {
		history = append(history, models.WatchHistoryItem{
			MediaType: "episode", SeriesID: "tvdb:series:428163", SeasonNumber: 1, EpisodeNumber: ep,
			Watched: true, ExternalIDs: map[string]string{"tvdb": "428163"},
		})
	}
	for ep := 1; ep <= 2; ep++ {
		history = append(history, models.WatchHistoryItem{
			MediaType: "episode", SeriesID: "tmdb:tv:215943", SeasonNumber: 2, EpisodeNumber: ep,
			Watched: true, ExternalIDs: map[string]string{"tmdb": "215943"},
		})
	}
	idx := buildWatchStateIndex(history, nil, nil)
	ext := map[string]string{"tmdb": "215943", "tvdb": "428163"}
	idx.setSeriesEpisodeTotal("tmdb:tv:215943", ext, 6)
	state, unwatched := idx.computeWithExternalIDs("series", "tmdb:tv:215943", ext)
	if state != "partial" || unwatched == nil || *unwatched != 1 {
		t.Fatalf("got state=%q unwatched=%v, want partial/1", state, derefInt(unwatched))
	}
}

func TestCompute_SeriesCountsLegacyBareNumericHistory(t *testing.T) {
	history := []models.WatchHistoryItem{
		{MediaType: "episode", ItemID: "95396:s01e01", SeriesID: "95396", SeasonNumber: 1, EpisodeNumber: 1, Watched: true},
		{MediaType: "episode", ItemID: "95396:s01e02", SeriesID: "95396", SeasonNumber: 1, EpisodeNumber: 2, Watched: true},
	}
	idx := buildWatchStateIndex(history, nil, nil)
	ext := map[string]string{"tmdb": "95396", "tvdb": "371980"}
	idx.setSeriesEpisodeTotal("tmdb:tv:95396", ext, 3)
	_, unwatched := idx.computeWithExternalIDs("series", "tmdb:tv:95396", ext)
	if unwatched == nil || *unwatched != 1 {
		t.Fatalf("unwatched = %v, want 1", derefInt(unwatched))
	}
	// An unrelated title must not pick up the legacy rows.
	idx.setSeriesEpisodeTotal("tmdb:tv:1399", map[string]string{"tmdb": "1399"}, 3)
	if _, other := idx.computeWithExternalIDs("series", "tmdb:tv:1399", map[string]string{"tmdb": "1399"}); other == nil || *other != 3 {
		t.Fatalf("unrelated unwatched = %v, want 3", derefInt(other))
	}
}

func derefInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

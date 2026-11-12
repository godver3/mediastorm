package metadata

import (
	"context"
	"net/http"
	"testing"
	"time"

	"novastream/models"
)

func TestCountReleasedTVDBEpisodes(t *testing.T) {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	episodes := []tvdbEpisode{
		{SeasonNumber: 0, Number: 1, Aired: "2020-01-01"},
		{SeasonNumber: 1, Number: 1, Aired: "2026-08-01"},
		{SeasonNumber: 1, Number: 2, Aired: "2026-08-28"},
		{SeasonNumber: 1, Number: 2, Aired: "2026-08-28"},
		{SeasonNumber: 1, Number: 3, Aired: "2026-08-29"},
		{SeasonNumber: 1, Number: 4},
		{SeasonNumber: 2, Number: 1, Aired: "not-a-date"},
	}

	if got := countReleasedTVDBEpisodes(episodes, now); got != 3 {
		t.Fatalf("released count = %d, want 3", got)
	}
}

func TestReleasedEpisodesCachedWithCount(t *testing.T) {
	svc := &Service{
		cache:  newFileCache(t.TempDir(), 24),
		client: &tvdbClient{language: "eng"},
	}
	details := models.SeriesDetails{Seasons: []models.SeriesSeason{
		{Number: 1, Episodes: []models.SeriesEpisode{
			{SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2020-01-08", AbsoluteEpisodeNumber: 2},
			{SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2020-01-01", AbsoluteEpisodeNumber: 1},
			{SeasonNumber: 1, EpisodeNumber: 3, AiredDate: "2999-01-01"},
		}},
	}}
	if err := svc.cache.set(seriesDetailsCacheKey("eng", 20, ""), details); err != nil {
		t.Fatalf("seed details cache: %v", err)
	}

	want := []models.ReleasedEpisode{{Season: 1, Episode: 1, Absolute: 1}, {Season: 1, Episode: 2, Absolute: 2}}
	for i := 0; i < 2; i++ { // first call derives from details, second hits the count cache
		episodes, ok := svc.GetCachedReleasedEpisodes(models.SeriesDetailsQuery{TVDBID: 20})
		if !ok || len(episodes) != len(want) || episodes[0] != want[0] || episodes[1] != want[1] {
			t.Fatalf("call %d: released episodes = %+v, %v; want %+v", i, episodes, ok, want)
		}
	}
	if count, ok := svc.GetCachedReleasedEpisodeCount(models.SeriesDetailsQuery{TVDBID: 20}); !ok || count != 2 {
		t.Fatalf("count = %d, %v; want 2, true", count, ok)
	}
}

func TestReleasedEpisodeCountCacheUsesProviderAliases(t *testing.T) {
	svc := &Service{cache: newFileCache(t.TempDir(), 24)}
	seed := models.SeriesDetailsQuery{TitleID: "tmdb:tv:10", TMDBID: 10, TVDBID: 20, IMDBID: "tt123"}
	entry := releasedEpisodeCountCacheEntry{Count: 7}
	for _, key := range releasedEpisodeCountCacheKeys(seed) {
		if err := svc.cache.set(key, entry); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}

	for _, query := range []models.SeriesDetailsQuery{
		{TVDBID: 20},
		{TMDBID: 10},
		{IMDBID: "tt123"},
	} {
		count, ok := svc.GetCachedReleasedEpisodeCount(query)
		if !ok || count != 7 {
			t.Fatalf("cache lookup %#v = %d, %v; want 7, true", query, count, ok)
		}
	}
}

func TestReleasedEpisodeCountUsesCachedSeriesDetails(t *testing.T) {
	svc := &Service{
		cache:  newFileCache(t.TempDir(), 24),
		client: &tvdbClient{language: "eng"},
	}
	details := models.SeriesDetails{Seasons: []models.SeriesSeason{
		{Number: 0, Episodes: []models.SeriesEpisode{{SeasonNumber: 0, EpisodeNumber: 1, AiredDate: "2020-01-01"}}},
		{Number: 1, Episodes: []models.SeriesEpisode{
			{SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2020-01-01"},
			{SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2020-01-08"},
		}},
	}}
	if err := svc.cache.set(seriesDetailsCacheKey("eng", 20, ""), details); err != nil {
		t.Fatalf("seed details cache: %v", err)
	}

	count, ok := svc.GetCachedReleasedEpisodeCount(models.SeriesDetailsQuery{TVDBID: 20})
	if !ok || count != 2 {
		t.Fatalf("cached details count = %d, %v; want 2, true", count, ok)
	}
}

func TestReleasedEpisodeCountUsesCachedTMDBSeriesDetails(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		cache:  cache,
		client: &tvdbClient{language: "eng"},
	}
	details := models.SeriesDetails{Seasons: []models.SeriesSeason{
		{Number: 1, Episodes: []models.SeriesEpisode{
			{SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2020-01-01"},
			{SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2020-01-08"},
		}},
	}}
	if err := cache.set(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "10"), details); err != nil {
		t.Fatalf("seed TMDB details: %v", err)
	}

	count, ok := svc.GetCachedReleasedEpisodeCount(models.SeriesDetailsQuery{TMDBID: 10})
	if !ok || count != 2 {
		t.Fatalf("cached TMDB details count = %d, %v; want 2, true", count, ok)
	}
}

func TestWarmReleasedEpisodeCountUsesTMDBWhenTVDBIsUnconfigured(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		cache:  cache,
		client: newTVDBClient("", "eng", &http.Client{}, 24),
		tmdb:   newTMDBClient("tmdb-key", "eng", &http.Client{}, cache),
	}
	details := models.SeriesDetails{
		Title: models.Title{
			ID:            "tmdb:tv:10",
			Name:          "Test Series",
			MediaType:     "series",
			TMDBID:        10,
			IMDBID:        "tt1234567",
			Certification: "TV-PG",
			Credits:       &models.Credits{},
		},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2020-01-01"},
				{SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2020-01-08"},
			}},
		},
	}
	if err := cache.set(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "10"), details); err != nil {
		t.Fatalf("seed TMDB details: %v", err)
	}

	query := models.SeriesDetailsQuery{TitleID: "tmdb:tv:10", TMDBID: 10}
	if err := svc.warmReleasedEpisodeCount(context.Background(), query); err != nil {
		t.Fatalf("warmReleasedEpisodeCount: %v", err)
	}
	count, ok := svc.GetCachedReleasedEpisodeCount(query)
	if !ok || count != 2 {
		t.Fatalf("warmed count = %d, %v; want 2, true", count, ok)
	}
}

func TestReleasedEpisodeCountNegativeCacheSuppressesRetry(t *testing.T) {
	svc := &Service{cache: newFileCache(t.TempDir(), 24)}
	query := models.SeriesDetailsQuery{TitleID: "tmdb:tv:999", TMDBID: 999}
	svc.cacheUnavailableReleasedEpisodeCount(query, time.Hour)

	count, ok := svc.GetCachedReleasedEpisodeCount(query)
	if !ok || count != 0 {
		t.Fatalf("negative cache lookup = %d, %v; want 0, true", count, ok)
	}
}

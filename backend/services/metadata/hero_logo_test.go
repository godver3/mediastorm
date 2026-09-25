package metadata

import (
	"net/http"
	"testing"

	"novastream/models"
)

func TestHeroTitleFieldsHydrateCachedArtwork(t *testing.T) {
	for _, provider := range []string{"series", "tvdb-movie", "tmdb-movie"} {
		t.Run(provider, func(t *testing.T) {
			cache := newFileCache(t.TempDir(), 24)
			rt := &countingRoundTripper{status: http.StatusInternalServerError, body: `{}`}
			svc := &Service{
				client: &tvdbClient{language: "en"},
				tmdb:   newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache),
				cache:  cache,
			}
			title := models.Title{ID: "cached-title", TMDBID: 123, TVDBID: 456, Name: "Cached title"}
			mediaType := "movie"
			switch provider {
			case "series":
				mediaType = "series"
				if err := cache.set(seriesDetailsCacheKey("en", 456, ""), models.SeriesDetails{Title: title}); err != nil {
					t.Fatal(err)
				}
			case "tvdb-movie":
				if err := cache.set(cacheKey("tvdb", "movie", "details", "v6", "en", "456"), title); err != nil {
					t.Fatal(err)
				}
			case "tmdb-movie":
				// Old cache entries can still exist after the details cache version changes.
				if err := cache.set(cacheKey("tmdb", "movie", "details", "v3", "en", "123"), models.Title{ID: "stale"}); err != nil {
					t.Fatal(err)
				}
				if err := cache.set(cacheKey("tmdb", "movie", "details", "v4", "en", "123"), title); err != nil {
					t.Fatal(err)
				}
			}
			logo := &models.Image{URL: "https://example.com/logo.png", Width: 500, Height: 200}
			if err := cache.set(cacheKey("tmdb", "images", "v10", "en", mediaType, "123"), tmdbImagesResult{Logo: logo}); err != nil {
				t.Fatal(err)
			}
			var got *models.Title
			if provider == "series" {
				results := svc.BatchSeriesTitleFields(t.Context(), []models.SeriesDetailsQuery{{TVDBID: 456, TMDBID: 123}}, []string{"name", "logo"})
				if results[0].Details == nil {
					t.Fatalf("missing details: %+v", results[0])
				}
				got = &results[0].Details.Title
			} else {
				query := models.MovieDetailsQuery{TMDBID: 123}
				if provider == "tvdb-movie" {
					query.TVDBID = 456
				}
				results := svc.BatchMovieTitleFields(t.Context(), []models.MovieDetailsQuery{query}, []string{"name", "logo"})
				got = results[0].Title
			}
			if got == nil || got.Name != title.Name || got.Logo == nil || got.Logo.URL != logo.URL {
				t.Fatalf("title = %+v, want cached title with logo", got)
			}
			if calls := rt.callCount(); calls != 0 {
				t.Fatalf("network calls = %d, want 0", calls)
			}
		})
	}
}

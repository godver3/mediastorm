package metadata

import (
	"context"
	"net/http"
	"testing"

	"novastream/models"
)

func TestTMDBSeriesFallbackHydratesRatings(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "fresh"
		if cached {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) {
			cache := newFileCache(t.TempDir(), 24)
			rt := &countingRoundTripper{body: `{"id":1396,"name":"Breaking Bad","external_ids":{"imdb_id":"tt0903747"},"seasons":[]}`}
			svc := &Service{
				client:       &tvdbClient{language: "en"},
				tmdb:         newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache),
				cache:        cache,
				ratingsCache: newFileCache(t.TempDir(), 24),
				mdblist:      newMDBListClient("test-key", []string{"tomatoes", "audience"}, true, 24),
			}
			seed := func(key string, value any) {
				t.Helper()
				if err := cache.set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			seed(cacheKey("tmdb", "images", "v10", "en", "series", "1396"), tmdbImagesResult{})
			seed(cacheKey("tmdb", "credits", "v2", "en", "series", "1396"), models.Credits{})
			seed(cacheKey("tmdb", "tv", "content_rating", "v1", "1396"), "TV-MA")
			if cached {
				seed(cacheKey("tmdb", "series", "details-fallback", "v4", "en", "1396"), models.SeriesDetails{Title: models.Title{Name: "Breaking Bad", TMDBID: 1396, IMDBID: "tt0903747"}})
			}
			if err := svc.ratingsCache.set(ratingsDiskCacheKey("tt0903747", "show"), []models.Rating{
				{Source: "imdb", Value: 9.5, Max: 10},
				{Source: "tomatoes", Value: 96, Max: 100},
				{Source: "audience", Value: 97, Max: 100},
			}); err != nil {
				t.Fatal(err)
			}
			got, err := svc.tmdbSeriesDetailsFallback(context.Background(), models.SeriesDetailsQuery{TMDBID: 1396}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || len(got.Title.Ratings) != 2 || got.Title.Ratings[0].Source != "tomatoes" || got.Title.Ratings[1].Source != "audience" {
				t.Fatalf("expected enabled RT ratings in series details, got %#v", got)
			}
			if cached && rt.callCount() != 0 {
				t.Fatalf("unexpected network calls on cache hit: %d", rt.callCount())
			}
		})
	}
}

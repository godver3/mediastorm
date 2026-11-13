package metadata

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"novastream/models"
)

func TestTMDBSeriesFallbackClassifiesDateBasedReleases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     int64
		genre  string
		cached bool
		old    bool
		want   bool
	}{
		{"fresh talk show", 1489, "Talk", false, false, true},
		{"cached talk show", 1489, "Talk", true, false, true},
		{"fresh drama", 1396, "Drama", false, false, false},
		{"cached drama", 1396, "Drama", true, false, false},
		{"fresh season episode exception", 10160, "Game Show", false, false, false},
		{"cached season episode exception", 10160, "Game Show", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newFileCache(t.TempDir(), 24)
			rt := &countingRoundTripper{body: fmt.Sprintf(`{"id":%d,"name":"Example Show","genres":[{"id":10767,"name":%q}],"external_ids":{"imdb_id":"tt1234567"},"seasons":[]}`, tc.id, tc.genre)}
			svc := &Service{client: &tvdbClient{language: "en"}, cache: cache,
				tmdb: newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache)}
			seed := func(key string, value any) {
				t.Helper()
				if err := cache.set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			id := fmt.Sprint(tc.id)
			seed(cacheKey("tmdb", "images", "v10", "en", "series", id), tmdbImagesResult{})
			seed(cacheKey("tmdb", "credits", "v1", "series", id), models.Credits{})
			seed(cacheKey("tmdb", "tv", "content_rating", "v1", id), "TV-14")
			key := cacheKey("tmdb", "series", "details-fallback", "v4", "en", id)
			if tc.cached {
				seed(key, models.SeriesDetails{Title: models.Title{Name: "Example Show", TMDBID: tc.id,
					IMDBID: "tt1234567", Genres: []string{tc.genre}, IsDaily: tc.old, Credits: &models.Credits{}, Certification: "TV-14"}})
			}
			got, err := svc.tmdbSeriesDetailsFallback(context.Background(), models.SeriesDetailsQuery{TMDBID: tc.id}, nil)
			if err != nil || got == nil {
				t.Fatalf("series fallback: %v, %v", got, err)
			}
			if got.Title.IsDaily != tc.want {
				t.Fatalf("date-based releases = %v, want %v", got.Title.IsDaily, tc.want)
			}
			var persisted models.SeriesDetails
			if ok, err := cache.get(key, &persisted); err != nil || !ok || persisted.Title.IsDaily != tc.want {
				t.Fatalf("cached classification = %v, present = %v, err = %v", persisted.Title.IsDaily, ok, err)
			}
			if tc.cached && rt.callCount() != 0 {
				t.Fatalf("classification backfill made %d network calls", rt.callCount())
			}
		})
	}
}

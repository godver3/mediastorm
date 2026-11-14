package metadata

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegionalDiscoveryRefreshesProviderCacheWithinSixHours(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"page":1,"total_pages":1,"total_results":1,"results":[{"id":1,"title":"Fixture","release_date":"2020-01-01","poster_path":"/poster.jpg"}]}`))}, nil
	})}
	cache := newFileCache(t.TempDir(), 168)
	svc := &Service{client: newTVDBClient("", "ger", client, 168), tmdb: newTMDBClient("synthetic-key", "ger", client, cache), cache: cache}
	svc.tmdb.minInterval = 0
	opts := TMDBListOptions{SourceType: TMDBSourceCustomDiscover, MediaType: "movie", DiscoverQuery: "watch_region=DE&with_watch_providers=8", Limit: 20, DeferArtwork: true}
	if _, _, err := svc.GetTMDBList(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	cold := calls.Load()
	if _, _, err := svc.GetTMDBList(context.Background(), opts); err != nil || calls.Load() != cold {
		t.Fatal("warm provider row performed another request")
	}
	files, err := filepath.Glob(filepath.Join(cache.dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("no provider cache created")
	}
	old := time.Now().Add(-7 * time.Hour)
	for _, file := range files {
		if err = os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := svc.GetTMDBList(context.Background(), opts); err != nil || calls.Load() <= cold {
		t.Fatal("configured week-long metadata TTL kept a regional provider row stale")
	}
}

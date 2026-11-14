package metadata

import (
	"context"
	"fmt"
	"net/http"
	"novastream/config"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegionalDiscoveryLiveProviders(t *testing.T) {
	key := os.Getenv("STRMR_TMDB_BENCH_API_KEY")
	if key == "" {
		t.Skip("set STRMR_TMDB_BENCH_API_KEY for the opt-in live provider check")
	}
	var calls atomic.Int64
	client := &http.Client{Timeout: 15 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.themoviedb.org" {
			return nil, fmt.Errorf("unexpected metadata host")
		}
		calls.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{client: newTVDBClient("", "ger", client, 24), tmdb: newTMDBClient(key, "ger", client, cache), cache: cache, idCache: newFileCache(t.TempDir(), 168), inflightRequests: make(map[string]*inflightRequest)}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, preset := range []string{"netflix-chart", "netflix", "cinema"} {
		shelves, err := config.RegionalDiscoveryPresets("DE", preset)
		if err != nil {
			t.Fatal(err)
		}
		for _, shelf := range shelves {
			opts := TMDBListOptions{SourceType: shelf.TMDBSourceType, MediaType: shelf.TMDBMediaType, DiscoverQuery: shelf.TMDBDiscoverQuery, Sort: shelf.Sort, Limit: 20, DeferArtwork: true}
			started := time.Now()
			items, total, err := svc.GetTMDBList(ctx, opts)
			if err != nil || len(items) == 0 {
				t.Fatalf("%s/%s live source failed: %v", preset, shelf.TMDBMediaType, err)
			}
			cold := time.Since(started)
			before := calls.Load()
			started = time.Now()
			repeated, _, err := svc.GetTMDBList(ctx, opts)
			if err != nil || len(repeated) != len(items) {
				t.Fatal("repeated source load failed")
			}
			if delta := calls.Load() - before; delta != 0 {
				t.Fatalf("warm %s made %d redundant metadata requests", preset, delta)
			}
			if preset == "netflix-chart" && items[0].Title.CardSubtitle == "" {
				t.Fatal("official rank/week missing")
			}
			t.Logf("%s/%s: %d cards, total %d, cold %s, warm %s, warm metadata requests 0", preset, shelf.TMDBMediaType, len(items), total, cold.Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
		}
	}
}

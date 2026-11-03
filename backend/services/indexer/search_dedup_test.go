package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
)

func dedupTestService(t *testing.T, handler http.HandlerFunc) (*Service, config.IndexerConfig) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	return NewService(mgr, nil, nil), config.IndexerConfig{Name: "Test", URL: server.URL, Type: "newznab", Enabled: true}
}

const dedupFeed = `<rss><channel><item><title>Some.Movie.2024.1080p.WEB</title><guid>g1</guid><link>http://example.com/1.nzb</link></item></channel></rss>`

// Identical calls made at the same time reach the indexer once, and a repeat
// within the TTL is answered from the shared response.
func TestIdenticalIndexerCallsAreShared(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	svc, idx := dedupTestService(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		fmt.Fprint(w, dedupFeed)
	})
	opts := SearchOptions{Query: "Some Movie 2024", MediaType: "movie"}

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := svc.searchTorznab(t.Context(), idx, opts)
			if err == nil && len(results) != 1 {
				err = fmt.Errorf("results = %d, want 1", len(results))
			}
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond) // let every caller join the call in flight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("concurrent identical calls hit the indexer %d times, want 1", got)
	}

	results, err := svc.searchTorznab(t.Context(), idx, opts)
	if err != nil || len(results) != 1 {
		t.Fatalf("repeat: results=%d err=%v", len(results), err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("repeat within the TTL hit the indexer again (%d hits)", got)
	}

	// Callers get their own copies.
	results[0].Attributes = map[string]string{"changed": "yes"}
	results[0].Title = "changed"
	again, _ := svc.searchTorznab(t.Context(), idx, opts)
	if again[0].Title == "changed" || again[0].Attributes["changed"] == "yes" {
		t.Fatal("a caller's changes leaked into the shared response")
	}

	// A different query is a different call.
	if _, err := svc.searchTorznab(t.Context(), idx, SearchOptions{Query: "Other Movie 2024", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("different query: hits = %d, want 2", got)
	}
}

// A failed call is not reused: the next identical call asks the indexer again.
func TestFailedIndexerCallIsNotReused(t *testing.T) {
	var hits atomic.Int32
	svc, idx := dedupTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
			return
		}
		fmt.Fprint(w, dedupFeed)
	})
	opts := SearchOptions{Query: "Some Movie 2024", MediaType: "movie"}
	if _, err := svc.searchTorznab(t.Context(), idx, opts); err == nil {
		t.Fatal("first call: want the indexer's error")
	}
	results, err := svc.searchTorznab(t.Context(), idx, opts)
	if err != nil || len(results) != 1 {
		t.Fatalf("retry: results=%d err=%v", len(results), err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2", got)
	}
}

// A shared call keeps the starting caller's deadline: an indexer that never
// answers does not hold the call (or its waiters) open.
func TestSharedIndexerCallKeepsCallerDeadline(t *testing.T) {
	svc, idx := dedupTestService(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	svc.httpc = &http.Client{} // no client timeout
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := svc.searchTorznab(ctx, idx, SearchOptions{Query: "Some Movie 2024", MediaType: "movie"}); err == nil {
		t.Fatal("want a deadline error")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("call outlived its deadline: %v", waited)
	}
}

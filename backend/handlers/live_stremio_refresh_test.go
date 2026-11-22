package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// stremioSlowServer serves a manifest with several catalogs; while gate is set,
// catalog requests wait for it to close.
func stremioSlowServer(t *testing.T, catalogs int, hits *int32, gate *atomic.Value) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		list := ""
		for i := range catalogs {
			if i > 0 {
				list += ","
			}
			list += fmt.Sprintf(`{"type":"sport","id":"c%d","name":"C%d"}`, i, i)
		}
		_, _ = fmt.Fprintf(w, `{"id":"slow.addon","types":["sport"],"catalogs":[%s]}`, list)
	})
	for i := range catalogs {
		mux.HandleFunc(fmt.Sprintf("/catalog/sport/c%d.json", i), func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(hits, 1)
			if ch, _ := gate.Load().(chan struct{}); ch != nil {
				<-ch
			}
			// Every catalog repeats "shared": the first catalog's copy must win.
			_, _ = fmt.Fprintf(w, `{"metas":[{"id":"shared","type":"sport","name":"Shared from C%d"},{"id":"own%d","type":"sport","name":"Own %d"}]}`, i, i, i)
		})
	}
	return httptest.NewServer(mux)
}

func ageStremioCache(h *LiveHandler, by time.Duration) {
	h.stremioMu.Lock()
	for key, entry := range h.stremioCache {
		entry.fetched = entry.fetched.Add(-by)
		h.stremioCache[key] = entry
	}
	h.stremioMu.Unlock()
	// The catalog responses are cached for the same TTL; expire them too.
	h.discovery.mu.Lock()
	clear(h.discovery.entries)
	clear(h.discovery.parsed)
	h.discovery.mu.Unlock()
}

func TestFetchStremioChannelsMergesConcurrentCatalogsInOrder(t *testing.T) {
	var hits int32
	var gate atomic.Value
	srv := stremioSlowServer(t, 6, &hits, &gate)
	defer srv.Close()
	h := newStremioTestHandler(t, srv.URL)

	channels, err := h.fetchStremioChannels(context.Background(), srv.URL+"/manifest.json", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"shared", "own0", "own1", "own2", "own3", "own4", "own5"}
	if len(channels) != len(want) {
		t.Fatalf("channels=%d want %d", len(channels), len(want))
	}
	for i, id := range want {
		if channels[i].ID != id {
			t.Fatalf("channel %d = %q, want %q (manifest order)", i, channels[i].ID, id)
		}
	}
	if channels[0].Name != "Shared from C0" {
		t.Fatalf("duplicate resolved to %q, want the first catalog's", channels[0].Name)
	}
}

func TestFetchStremioChannelsServesStaleWhileRefreshing(t *testing.T) {
	var hits int32
	var gate atomic.Value
	srv := stremioSlowServer(t, 2, &hits, &gate)
	defer srv.Close()
	h := newStremioTestHandler(t, srv.URL)
	manifest := srv.URL + "/manifest.json"
	if _, err := h.fetchStremioChannels(context.Background(), manifest, ""); err != nil {
		t.Fatal(err)
	}

	ageStremioCache(h, stremioChannelsTTL+time.Minute)
	release := make(chan struct{})
	gate.Store(release)
	started := time.Now()
	channels, err := h.fetchStremioChannels(context.Background(), manifest, "")
	if err != nil || len(channels) != 3 {
		t.Fatalf("stale read: %d channels, err=%v", len(channels), err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("stale read waited %s for the addon", waited)
	}
	// A second stale read starts no second refresh.
	if _, err := h.fetchStremioChannels(context.Background(), manifest, ""); err != nil {
		t.Fatal(err)
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		h.stremioMu.Lock()
		var entry stremioChannelsCacheEntry
		for _, e := range h.stremioCache {
			entry = e
		}
		h.stremioMu.Unlock()
		if !entry.refreshing && time.Since(entry.fetched) < time.Minute {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never replaced the stale list")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Fatalf("catalog fetches=%d, want 4 (initial + one refresh)", got)
	}
}

func TestFetchStremioChannelsWaitsPastMaxStale(t *testing.T) {
	var hits int32
	var gate atomic.Value
	srv := stremioSlowServer(t, 2, &hits, &gate)
	defer srv.Close()
	h := newStremioTestHandler(t, srv.URL)
	manifest := srv.URL + "/manifest.json"
	if _, err := h.fetchStremioChannels(context.Background(), manifest, ""); err != nil {
		t.Fatal(err)
	}
	ageStremioCache(h, stremioChannelsMaxStale+time.Minute)
	if _, err := h.fetchStremioChannels(context.Background(), manifest, ""); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Fatalf("catalog fetches=%d, want 4: a list past the max stale age is refetched in the request", got)
	}
}

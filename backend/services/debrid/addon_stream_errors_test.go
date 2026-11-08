package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddonStreamErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		name, desc, url, hash, typ string
		want                       string
		isErr                      bool
	}{
		{"[❌] AIOStreams", "Invalid config\n  bad uuid", "", "", "", "Invalid config bad uuid", true},
		{"AIOStreams", "Torbox key rejected", "", "", "error", "Torbox key rejected", true},
		{"[❌] AIOStreams", "", "", "", "", "[❌] AIOStreams", true},
		{"[❌] AIOStreams", "x", "https://cdn/x.mkv", "", "", "", false},
		{"[❌] AIOStreams", "x", "", "0123", "", "", false},
		{"Torbox 1080p", "release", "", "", "", "", false},
		{"[⚠️] Comet", "Invalid Comet config.", "https://comet.example", "", "", "Invalid Comet config.", true},
		{"[⚠️] Comet", "uncached", "https://comet.example/playback/abc", "", "", "", false},
	} {
		got, isErr := AddonStreamErrorMessage(tc.name, tc.desc, tc.url, tc.hash, tc.typ)
		if got != tc.want || isErr != tc.isErr {
			t.Errorf("%q/%q: got (%q,%v) want (%q,%v)", tc.name, tc.desc, got, isErr, tc.want, tc.isErr)
		}
	}
}

// A misconfigured AIO URL returns only the error entry; Search must fail with
// AIO's message instead of silently returning zero results.
func TestAIOStreamsSearchSurfacesErrorEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":[{"name":"[❌] AIOStreams","description":"Invalid config","externalUrl":"https://aio.example/"}]}`))
	}))
	defer server.Close()

	scraper := NewAIOStreamsScraper(server.URL+"/manifest.json", "MY_AIO", false, server.Client())
	results, err := scraper.Search(context.Background(), SearchRequest{
		IMDBID: "tt0133093",
		Parsed: ParsedQuery{Title: "The Matrix", MediaType: MediaTypeMovie},
	})
	if err == nil || !strings.Contains(err.Error(), "Invalid config") {
		t.Fatalf("expected AIO error to surface, got results=%d err=%v", len(results), err)
	}
}

func TestDirectStremioSearchSurfacesErrorEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":[{"name":"[❌] Addon","description":"Debrid key expired","externalUrl":"https://addon.example/"}]}`))
	}))
	defer server.Close()

	scraper := NewDirectStremioScraper(server.URL+"/manifest.json", "Direct", server.Client())
	results, err := scraper.Search(context.Background(), SearchRequest{
		IMDBID: "tt0133093",
		Parsed: ParsedQuery{Title: "The Matrix", MediaType: MediaTypeMovie},
	})
	if err == nil || !strings.Contains(err.Error(), "Debrid key expired") {
		t.Fatalf("expected addon error to surface, got results=%d err=%v", len(results), err)
	}
}

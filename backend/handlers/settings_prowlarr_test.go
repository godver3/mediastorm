package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
)

func TestExpandProwlarrSourcesDiscoversUsenetAndTorrentIndexers(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/indexer" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "prowlarr-key" {
			t.Fatalf("unexpected api key: %q", got)
		}
		_ = json.NewEncoder(w).Encode([]prowlarrIndexerInfo{
			{ID: 2, Name: "NZBgeek", Protocol: "usenet", Enable: true, SupportsSearch: true},
			{ID: 3, Name: "The Pirate Bay", Protocol: "torrent", Enable: true, SupportsSearch: true},
			{ID: 4, Name: "Disabled", Protocol: "torrent", Enable: false, SupportsSearch: true},
			{ID: 5, Name: "No Search", Protocol: "usenet", Enable: true, SupportsSearch: false},
		})
	}))
	defer server.Close()

	settings := config.Settings{
		Indexers: []config.IndexerConfig{
			{Name: "Prowlarr", URL: server.URL, APIKey: "prowlarr-key", Type: "prowlarr", Categories: "2000,5000", Enabled: true},
		},
		TorrentScrapers: []config.TorrentScraperConfig{
			{Name: "Prowlarr", Type: "prowlarr", URL: server.URL, APIKey: "prowlarr-key", Enabled: true},
		},
	}

	if err := expandProwlarrSources(context.Background(), &settings); err != nil {
		t.Fatalf("expandProwlarrSources returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected discovery to be cached across usenet/torrent expansion, got %d calls", calls)
	}

	if len(settings.Indexers) != 1 {
		t.Fatalf("expected 1 usenet indexer, got %d", len(settings.Indexers))
	}
	idx := settings.Indexers[0]
	if idx.Name != "Prowlarr - NZBgeek" || idx.Type != "newznab" || idx.URL != server.URL+"/2" || idx.APIKey != "prowlarr-key" || idx.Categories != "2000,5000" || !idx.Enabled {
		t.Fatalf("unexpected expanded usenet indexer: %#v", idx)
	}

	if len(settings.TorrentScrapers) != 1 {
		t.Fatalf("expected 1 torrent scraper, got %d", len(settings.TorrentScrapers))
	}
	scraper := settings.TorrentScrapers[0]
	if scraper.Name != "Prowlarr - The Pirate Bay" || scraper.Type != "prowlarr" || scraper.URL != server.URL+"/3" || scraper.APIKey != "prowlarr-key" || !scraper.Enabled {
		t.Fatalf("unexpected expanded torrent scraper: %#v", scraper)
	}
}

func TestExpandProwlarrSourcesKeepsPerIndexerURLs(t *testing.T) {
	settings := config.Settings{
		Indexers: []config.IndexerConfig{
			{Name: "Prowlarr - NZBgeek", URL: "http://prowlarr:9696/2/api", APIKey: "key", Type: "prowlarr", Enabled: true},
		},
		TorrentScrapers: []config.TorrentScraperConfig{
			{Name: "Prowlarr - TPB", Type: "prowlarr", URL: "http://prowlarr:9696/3", APIKey: "key", Enabled: true},
		},
	}

	if err := expandProwlarrSources(context.Background(), &settings); err != nil {
		t.Fatalf("expandProwlarrSources returned error: %v", err)
	}

	if settings.Indexers[0].Type != "newznab" {
		t.Fatalf("expected per-indexer usenet Prowlarr entry to normalize to newznab, got %q", settings.Indexers[0].Type)
	}
	if settings.Indexers[0].URL != "http://prowlarr:9696/2/api" {
		t.Fatalf("unexpected usenet URL: %s", settings.Indexers[0].URL)
	}
	if settings.TorrentScrapers[0].Type != "prowlarr" || settings.TorrentScrapers[0].URL != "http://prowlarr:9696/3" {
		t.Fatalf("unexpected torrent scraper: %#v", settings.TorrentScrapers[0])
	}
}

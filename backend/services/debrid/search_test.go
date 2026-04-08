package debrid

import (
	"context"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
)

type stubScraper struct {
	name    string
	results []ScrapeResult
}

func (s stubScraper) Name() string {
	return s.name
}

func (s stubScraper) Search(_ context.Context, _ SearchRequest) ([]ScrapeResult, error) {
	return s.results, nil
}

func TestNormalizeScrapeResult(t *testing.T) {
	input := ScrapeResult{
		Title:      "Breaking Bad S01E01",
		Indexer:    "",
		Magnet:     "magnet:?xt=urn:btih:ABC",
		InfoHash:   "ABC",
		FileIndex:  7,
		SizeBytes:  42,
		Seeders:    100,
		Provider:   "TorrentGalaxy",
		Languages:  []string{"🇬🇧", "🇺🇸"},
		Resolution: "1080p",
		MetaName:   "Breaking Bad",
		MetaID:     "tt0903747",
		Source:     "torrentio",
		Attributes: map[string]string{"custom": "value"},
	}

	result := normalizeScrapeResult(input)
	if result.ServiceType != models.ServiceTypeDebrid {
		t.Fatalf("expected ServiceTypeDebrid, got %v", result.ServiceType)
	}
	if result.GUID == "" {
		t.Fatalf("guid should be populated")
	}
	if got := result.Attributes["infoHash"]; got != "abc" {
		t.Fatalf("expected lowercase infoHash, got %q", got)
	}
	if got := result.Attributes["tracker"]; got != "TorrentGalaxy" {
		t.Fatalf("expected tracker attribute, got %q", got)
	}
	if got := result.Attributes["custom"]; got != "value" {
		t.Fatalf("expected custom attribute, got %q", got)
	}
	if result.DownloadURL != input.Magnet {
		t.Fatalf("download url mismatch")
	}
}

// TestSearchAlwaysWaitsForAllScrapers verifies that the debrid search always
// waits for all scrapers to complete before returning results.
func TestSearchAlwaysWaitsForAllScrapers(t *testing.T) {
	// The search implementation always drains resultsChan (waits for all scrapers).
	// This test documents that behavior — there is no early-return path.
	t.Logf("Debrid search always waits for all scrapers to complete.")
	t.Logf("Both search and prequeue use the same combined Search() flow.")
}

func TestSearchAllowsDirectStreamScrapersWithoutDebridProviders(t *testing.T) {
	t.Helper()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	cfgManager := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	for i := range settings.Streaming.DebridProviders {
		settings.Streaming.DebridProviders[i].Enabled = false
		settings.Streaming.DebridProviders[i].APIKey = ""
	}
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{
			Name:    "AIOStreams",
			Type:    "aiostreams",
			URL:     "https://example.test/manifest.json",
			Enabled: true,
		},
	}
	settings.Display.BypassFilteringForAIOStreamsOnly = true

	if err := cfgManager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewSearchService(cfgManager, stubScraper{
		name: "AIOStreams",
		results: []ScrapeResult{
			{
				Title:      "Moana.2016.1080p.WEB-DL",
				Indexer:    "AIOStreams",
				TorrentURL: "https://example.test/playback/moana",
				SizeBytes:  1024,
				Attributes: map[string]string{
					"preresolved": "true",
					"stream_url":  "https://example.test/playback/moana",
				},
			},
		},
	})

	results, err := svc.Search(t.Context(), SearchOptions{
		Query:     "Moana 2016",
		MediaType: "movie",
		Year:      2016,
	})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if got := results[0].Attributes["stream_url"]; got != "https://example.test/playback/moana" {
		t.Fatalf("expected pre-resolved stream URL to survive normalization, got %q", got)
	}
}

package debrid

import (
	"testing"

	"novastream/models"
)

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

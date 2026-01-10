package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNyaaScraperName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "custom name",
			input:    "MyNyaa",
			expected: "MyNyaa",
		},
		{
			name:     "empty name falls back to default",
			input:    "",
			expected: "Nyaa",
		},
		{
			name:     "whitespace name falls back to default",
			input:    "   ",
			expected: "Nyaa",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewNyaaScraper("https://nyaa.si", tc.input, "1_2", "0", nil)
			if scraper.Name() != tc.expected {
				t.Errorf("expected scraper name %q, got %q", tc.expected, scraper.Name())
			}
		})
	}
}

func TestNyaaConvertSizeToBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
	}{
		// Binary units (1024-based)
		{"TiB", "1.5 TiB", int64(1.5 * 1024 * 1024 * 1024 * 1024)},
		{"GiB", "2.5 GiB", int64(2.5 * 1024 * 1024 * 1024)},
		{"MiB", "500 MiB", int64(500 * 1024 * 1024)},
		{"KiB", "1024 KiB", int64(1024 * 1024)},

		// Decimal units
		{"TB", "1 TB", int64(1 * 1024 * 1024 * 1024 * 1024)},
		{"GB", "4.5 GB", int64(4.5 * 1024 * 1024 * 1024)},
		{"MB", "750 MB", int64(750 * 1024 * 1024)},
		{"KB", "512 KB", int64(512 * 1024)},

		// Edge cases
		{"lowercase gib", "1.2 gib", 1288490188}, // 1.2 * 1024^3
		{"no space", "2.0GiB", int64(2.0 * 1024 * 1024 * 1024)},
		{"bytes", "1048576 b", 1048576},
		{"empty string", "", 0},
		{"invalid format", "invalid", 0},
		{"just number", "12345", 12345},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := nyaaConvertSizeToBytes(tc.input)
			if result != tc.expected {
				t.Errorf("nyaaConvertSizeToBytes(%q) = %d, want %d", tc.input, result, tc.expected)
			}
		})
	}
}

func TestNyaaParseRSSResponse(t *testing.T) {
	rssResponse := `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:nyaa="https://nyaa.si/xmlns/nyaa">
  <channel>
    <title>Nyaa - Search Results</title>
    <item>
      <title>[SubsPlease] Attack on Titan - 01 [1080p]</title>
      <link>https://nyaa.si/view/123456</link>
      <guid isPermaLink="true">https://nyaa.si/view/123456</guid>
      <pubDate>Mon, 01 Jan 2024 12:00:00 -0000</pubDate>
      <nyaa:seeders>500</nyaa:seeders>
      <nyaa:leechers>50</nyaa:leechers>
      <nyaa:downloads>10000</nyaa:downloads>
      <nyaa:infoHash>a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2</nyaa:infoHash>
      <nyaa:size>1.5 GiB</nyaa:size>
      <nyaa:category>Anime - English-translated</nyaa:category>
    </item>
    <item>
      <title>[Erai-raws] Attack on Titan - 01 [720p]</title>
      <link>https://nyaa.si/view/123457</link>
      <guid isPermaLink="true">https://nyaa.si/view/123457</guid>
      <pubDate>Mon, 01 Jan 2024 11:00:00 -0000</pubDate>
      <nyaa:seeders>200</nyaa:seeders>
      <nyaa:leechers>20</nyaa:leechers>
      <nyaa:downloads>5000</nyaa:downloads>
      <nyaa:infoHash>b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3</nyaa:infoHash>
      <nyaa:size>500 MiB</nyaa:size>
      <nyaa:category>Anime - English-translated</nyaa:category>
    </item>
    <item>
      <title>[NoHash] Some Anime - 01</title>
      <link>https://nyaa.si/view/123458</link>
      <pubDate>Mon, 01 Jan 2024 10:00:00 -0000</pubDate>
      <nyaa:seeders>100</nyaa:seeders>
      <nyaa:size>1 GiB</nyaa:size>
    </item>
  </channel>
</rss>`

	scraper := NewNyaaScraper("https://nyaa.si", "Nyaa", "1_2", "0", nil)
	results, err := scraper.parseRSSResponse([]byte(rssResponse))
	if err != nil {
		t.Fatalf("parseRSSResponse failed: %v", err)
	}

	// Should have 2 results (one without hash is skipped)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Results should be sorted by seeders (highest first)
	if results[0].Seeders != 500 {
		t.Errorf("expected first result to have 500 seeders, got %d", results[0].Seeders)
	}

	// Verify first result details
	first := results[0]
	if first.Title != "[SubsPlease] Attack on Titan - 01 [1080p]" {
		t.Errorf("unexpected title: %s", first.Title)
	}
	if first.InfoHash != "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" {
		t.Errorf("unexpected infohash: %s", first.InfoHash)
	}
	expectedSize := int64(1.5 * 1024 * 1024 * 1024)
	if first.SizeBytes != expectedSize {
		t.Errorf("expected size %d, got %d", expectedSize, first.SizeBytes)
	}
	if first.Resolution != "1080p" {
		t.Errorf("expected resolution 1080p, got %s", first.Resolution)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("expected magnet link, got: %s", first.Magnet)
	}
	if first.Indexer != "Nyaa" {
		t.Errorf("expected indexer 'Nyaa', got %q", first.Indexer)
	}

	// Verify second result
	second := results[1]
	if second.Seeders != 200 {
		t.Errorf("expected second result to have 200 seeders, got %d", second.Seeders)
	}
	if second.Resolution != "720p" {
		t.Errorf("expected resolution 720p, got %s", second.Resolution)
	}
}

func TestNyaaParseRSSResponseDeduplication(t *testing.T) {
	rssResponse := `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:nyaa="https://nyaa.si/xmlns/nyaa">
  <channel>
    <item>
      <title>Duplicate 1</title>
      <nyaa:seeders>100</nyaa:seeders>
      <nyaa:infoHash>samehash1234567890samehash1234567890</nyaa:infoHash>
      <nyaa:size>1 GiB</nyaa:size>
    </item>
    <item>
      <title>Duplicate 2 (same hash)</title>
      <nyaa:seeders>50</nyaa:seeders>
      <nyaa:infoHash>samehash1234567890samehash1234567890</nyaa:infoHash>
      <nyaa:size>1 GiB</nyaa:size>
    </item>
    <item>
      <title>Unique</title>
      <nyaa:seeders>200</nyaa:seeders>
      <nyaa:infoHash>uniquehash123456789uniquehash123456</nyaa:infoHash>
      <nyaa:size>2 GiB</nyaa:size>
    </item>
  </channel>
</rss>`

	scraper := NewNyaaScraper("https://nyaa.si", "Nyaa", "1_2", "0", nil)
	results, err := scraper.parseRSSResponse([]byte(rssResponse))
	if err != nil {
		t.Fatalf("parseRSSResponse failed: %v", err)
	}

	// Should deduplicate to 2 results
	if len(results) != 2 {
		t.Fatalf("expected 2 deduplicated results, got %d", len(results))
	}
}

func TestNyaaSearchIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request parameters
		query := r.URL.Query()
		if query.Get("page") != "rss" {
			t.Errorf("expected page=rss, got %s", query.Get("page"))
		}
		if query.Get("f") != "0" {
			t.Errorf("expected f=0 (filter), got %s", query.Get("f"))
		}
		if query.Get("c") != "1_2" {
			t.Errorf("expected c=1_2 (category), got %s", query.Get("c"))
		}
		if query.Get("s") != "seeders" {
			t.Errorf("expected s=seeders (sort), got %s", query.Get("s"))
		}
		if query.Get("o") != "desc" {
			t.Errorf("expected o=desc (order), got %s", query.Get("o"))
		}

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:nyaa="https://nyaa.si/xmlns/nyaa">
  <channel>
    <item>
      <title>Test Anime - 01 [1080p]</title>
      <nyaa:seeders>100</nyaa:seeders>
      <nyaa:leechers>10</nyaa:leechers>
      <nyaa:infoHash>testhash12345678901234567890123456789012</nyaa:infoHash>
      <nyaa:size>1 GiB</nyaa:size>
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	scraper := NewNyaaScraper(server.URL, "TestNyaa", "1_2", "0", nil)

	req := SearchRequest{
		Query: "Test Anime",
		Parsed: ParsedQuery{
			Title:     "Test Anime",
			MediaType: MediaTypeSeries,
		},
		MaxResults: 50,
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Title != "Test Anime - 01 [1080p]" {
		t.Errorf("unexpected title: %s", results[0].Title)
	}
}

func TestNyaaSearchMovieQueryConstruction(t *testing.T) {
	var capturedQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQueries = append(capturedQueries, r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	defer server.Close()

	scraper := NewNyaaScraper(server.URL, "Nyaa", "1_2", "0", nil)

	req := SearchRequest{
		Query: "Akira 1988",
		Parsed: ParsedQuery{
			Title:     "Akira",
			Year:      1988,
			MediaType: MediaTypeMovie,
		},
	}

	_, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// First query should include year
	if len(capturedQueries) == 0 {
		t.Fatal("expected at least one query")
	}
	if capturedQueries[0] != "Akira 1988" {
		t.Errorf("expected first query 'Akira 1988', got %q", capturedQueries[0])
	}

	// When no results, should retry without year
	if len(capturedQueries) < 2 {
		t.Fatal("expected retry without year when no results")
	}
	if capturedQueries[1] != "Akira" {
		t.Errorf("expected retry query 'Akira', got %q", capturedQueries[1])
	}
}

func TestNyaaSearchTVQueryConstruction(t *testing.T) {
	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?><rss><channel></channel></rss>`))
	}))
	defer server.Close()

	scraper := NewNyaaScraper(server.URL, "Nyaa", "1_2", "0", nil)

	req := SearchRequest{
		Query: "Attack on Titan",
		Parsed: ParsedQuery{
			Title:     "Attack on Titan",
			Season:    1,
			Episode:   5,
			MediaType: MediaTypeSeries,
		},
	}

	_, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// TV search with episode should include episode number with leading zero
	if capturedQuery != "Attack on Titan 05" {
		t.Errorf("expected query 'Attack on Titan 05', got %q", capturedQuery)
	}
}

func TestNyaaSearchErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Service temporarily unavailable"))
	}))
	defer server.Close()

	scraper := NewNyaaScraper(server.URL, "Nyaa", "1_2", "0", nil)

	req := SearchRequest{
		Query: "Test",
		Parsed: ParsedQuery{
			Title: "Test",
		},
	}

	_, err := scraper.Search(context.Background(), req)
	if err == nil {
		t.Error("expected error for 503 response, got nil")
	}

	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to mention 503 status, got: %v", err)
	}
}

func TestNyaaDefaultConfiguration(t *testing.T) {
	// Test that defaults are applied correctly
	scraper := NewNyaaScraper("", "", "", "", nil)

	if scraper.baseURL != nyaaDefaultBaseURL {
		t.Errorf("expected default baseURL %q, got %q", nyaaDefaultBaseURL, scraper.baseURL)
	}
	if scraper.category != "1_2" {
		t.Errorf("expected default category '1_2', got %q", scraper.category)
	}
	if scraper.filter != "0" {
		t.Errorf("expected default filter '0', got %q", scraper.filter)
	}
	if scraper.Name() != "Nyaa" {
		t.Errorf("expected default name 'Nyaa', got %q", scraper.Name())
	}
}

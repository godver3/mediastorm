package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJackettExtractInfoHash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid magnet with 40-char hex hash",
			input:    "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=test",
			expected: "0123456789abcdef0123456789abcdef01234567",
		},
		{
			name:     "valid magnet with uppercase hash",
			input:    "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=test",
			expected: "abcdef0123456789abcdef0123456789abcdef01",
		},
		{
			name:     "valid magnet with 32-char base32 hash",
			input:    "magnet:?xt=urn:btih:MFZWIZLOMVZHI3TUEB2XAYLOMRSXE2LF&dn=test",
			expected: "mfzwizlomvzhi3tueb2xaylomrsxe2lf",
		},
		{
			name:     "not a magnet link",
			input:    "https://example.com/download/123",
			expected: "",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "magnet without infohash",
			input:    "magnet:?dn=test",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := jackettExtractInfoHash(tc.input)
			if result != tc.expected {
				t.Errorf("jackettExtractInfoHash(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestExtractResolution(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"4K", "Movie.Name.2024.2160p.UHD.BluRay.x265", "4K"},
		{"4k lowercase", "movie.name.4k.hdr.x265", "4K"},
		{"UHD", "Movie.Name.2024.UHD.BluRay", "4K"},
		{"1080p", "Movie.Name.2024.1080p.BluRay.x264", "1080p"},
		{"1080i", "Movie.Name.2024.1080i.HDTV", "1080p"},
		{"720p", "Movie.Name.2024.720p.WEB-DL", "720p"},
		{"480p", "Movie.Name.2024.480p.HDTV", "480p"},
		{"SD", "Movie.Name.2024.SD.HDTV", "480p"},
		{"no resolution", "Movie.Name.2024.WEB-DL", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractResolution(tc.input)
			if result != tc.expected {
				t.Errorf("extractResolution(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestBuildMagnetFromHash(t *testing.T) {
	hash := "abcdef1234567890abcdef1234567890abcdef12"
	title := "Test Movie 2024"

	magnet := buildMagnetFromHash(hash, title)

	if magnet == "" {
		t.Error("expected non-empty magnet link")
	}
	if magnet != "magnet:?xt=urn:btih:abcdef1234567890abcdef1234567890abcdef12&dn=Test+Movie+2024" {
		t.Errorf("unexpected magnet format: %s", magnet)
	}
}

func TestJackettScraperName(t *testing.T) {
	scraper := NewJackettScraper("http://localhost:9117", "testkey", "Jackett", nil)
	if scraper.Name() != "Jackett" {
		t.Errorf("expected scraper name 'Jackett', got %q", scraper.Name())
	}
}

func TestJackettParseResponse(t *testing.T) {
	xmlResponse := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>Jackett</title>
    <item>
      <title>Test.Movie.2024.1080p.BluRay.x264-GROUP</title>
      <guid>magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2&amp;dn=Test</guid>
      <link>magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2&amp;dn=Test</link>
      <size>4000000000</size>
      <pubDate>Sun, 01 Dec 2024 12:00:00 +0000</pubDate>
      <torznab:attr name="seeders" value="150"/>
      <torznab:attr name="peers" value="200"/>
      <torznab:attr name="infohash" value="a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"/>
      <torznab:attr name="tracker" value="TestTracker"/>
    </item>
    <item>
      <title>Another.Movie.2024.720p.WEB-DL</title>
      <guid>https://tracker.com/download/123</guid>
      <link>https://tracker.com/download/123</link>
      <size>2000000000</size>
      <torznab:attr name="seeders" value="50"/>
      <torznab:attr name="infohash" value="b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"/>
    </item>
    <item>
      <title>No.Hash.Movie.2024</title>
      <guid>https://tracker.com/download/456</guid>
      <link>https://tracker.com/download/456</link>
      <size>1000000000</size>
    </item>
  </channel>
</rss>`

	scraper := NewJackettScraper("http://localhost:9117", "testkey", "Jackett", nil)
	results, err := scraper.parseResponse([]byte(xmlResponse))
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	// Should have 3 results:
	// - 2 with infohash
	// - 1 without infohash but with torrent URL (now supported)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Check first result (has infohash and magnet)
	first := results[0]
	if first.Title != "Test.Movie.2024.1080p.BluRay.x264-GROUP" {
		t.Errorf("unexpected title: %s", first.Title)
	}
	if first.InfoHash != "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" {
		t.Errorf("unexpected infohash: %s", first.InfoHash)
	}
	if first.SizeBytes != 4000000000 {
		t.Errorf("unexpected size: %d", first.SizeBytes)
	}
	if first.Seeders != 150 {
		t.Errorf("unexpected seeders: %d", first.Seeders)
	}
	if first.Resolution != "1080p" {
		t.Errorf("unexpected resolution: %s", first.Resolution)
	}
	if first.Provider != "TestTracker" {
		t.Errorf("unexpected provider: %s", first.Provider)
	}

	// Check second result (has infohash from attribute)
	second := results[1]
	if second.InfoHash != "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3" {
		t.Errorf("unexpected infohash: %s", second.InfoHash)
	}
	if second.Resolution != "720p" {
		t.Errorf("unexpected resolution: %s", second.Resolution)
	}

	// Check third result (no infohash, but has torrent URL)
	third := results[2]
	if third.Title != "No.Hash.Movie.2024" {
		t.Errorf("unexpected title: %s", third.Title)
	}
	if third.InfoHash != "" {
		t.Errorf("expected empty infohash, got: %s", third.InfoHash)
	}
	if third.TorrentURL != "https://tracker.com/download/456" {
		t.Errorf("expected torrent URL, got: %s", third.TorrentURL)
	}
	if third.Magnet != "" {
		t.Errorf("expected empty magnet, got: %s", third.Magnet)
	}
}

func TestJackettSearchMovie(t *testing.T) {
	// Create a test server that returns mock Jackett response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		query := r.URL.Query()
		if query.Get("t") != "movie" {
			t.Errorf("expected t=movie, got t=%s", query.Get("t"))
		}
		if query.Get("q") != "The Matrix 1999" {
			t.Errorf("expected q='The Matrix 1999', got q=%s", query.Get("q"))
		}
		if query.Get("apikey") != "testkey" {
			t.Errorf("expected apikey=testkey, got apikey=%s", query.Get("apikey"))
		}

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>The.Matrix.1999.1080p.BluRay</title>
      <guid>magnet:?xt=urn:btih:1234567890abcdef1234567890abcdef12345678</guid>
      <size>5000000000</size>
      <torznab:attr name="infohash" value="1234567890abcdef1234567890abcdef12345678"/>
      <torznab:attr name="seeders" value="200"/>
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	scraper := NewJackettScraper(server.URL, "testkey", "Jackett", nil)
	results, err := scraper.searchMovie(context.Background(), "The Matrix", 1999)
	if err != nil {
		t.Fatalf("searchMovie failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Title != "The.Matrix.1999.1080p.BluRay" {
		t.Errorf("unexpected title: %s", results[0].Title)
	}
}

func TestJackettSearchTV(t *testing.T) {
	// Create a test server that returns mock Jackett response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		query := r.URL.Query()
		if query.Get("t") != "tvsearch" {
			t.Errorf("expected t=tvsearch, got t=%s", query.Get("t"))
		}
		if query.Get("q") != "Breaking Bad" {
			t.Errorf("expected q='Breaking Bad', got q=%s", query.Get("q"))
		}
		if query.Get("season") != "5" {
			t.Errorf("expected season=5, got season=%s", query.Get("season"))
		}
		if query.Get("ep") != "16" {
			t.Errorf("expected ep=16, got ep=%s", query.Get("ep"))
		}

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>Breaking.Bad.S05E16.1080p.BluRay</title>
      <guid>magnet:?xt=urn:btih:abcdef1234567890abcdef1234567890abcdef12</guid>
      <size>3000000000</size>
      <torznab:attr name="infohash" value="abcdef1234567890abcdef1234567890abcdef12"/>
      <torznab:attr name="seeders" value="100"/>
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	scraper := NewJackettScraper(server.URL, "testkey", "Jackett", nil)
	results, err := scraper.searchTV(context.Background(), "Breaking Bad", 5, 16)
	if err != nil {
		t.Fatalf("searchTV failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].Title != "Breaking.Bad.S05E16.1080p.BluRay" {
		t.Errorf("unexpected title: %s", results[0].Title)
	}
}

func TestJackettSearch(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>Test.Movie.2024.1080p</title>
      <guid>magnet:?xt=urn:btih:1111111111111111111111111111111111111111</guid>
      <size>4000000000</size>
      <torznab:attr name="infohash" value="1111111111111111111111111111111111111111"/>
      <torznab:attr name="seeders" value="50"/>
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	scraper := NewJackettScraper(server.URL, "testkey", "Jackett", nil)

	// Test movie search
	req := SearchRequest{
		Query:      "Test Movie 2024",
		MaxResults: 10,
		Parsed: ParsedQuery{
			Title:     "Test Movie",
			Year:      2024,
			MediaType: MediaTypeMovie,
		},
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
}

func TestJackettTestConnection(t *testing.T) {
	// Create a test server that simulates capabilities response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("t") != "caps" {
			t.Errorf("expected t=caps, got t=%s", query.Get("t"))
		}

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="Jackett" />
  <searching>
    <search available="yes" />
    <tv-search available="yes" />
    <movie-search available="yes" />
  </searching>
</caps>`))
	}))
	defer server.Close()

	scraper := NewJackettScraper(server.URL, "testkey", "Jackett", nil)
	err := scraper.TestConnection(context.Background())
	if err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}
}

func TestJackettDeduplication(t *testing.T) {
	// XML with duplicate infohash
	xmlResponse := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>Same.Movie.Different.Title</title>
      <guid>magnet:?xt=urn:btih:samehash1234567890abcdef1234567890abcd</guid>
      <size>4000000000</size>
      <torznab:attr name="infohash" value="samehash1234567890abcdef1234567890abcd"/>
    </item>
    <item>
      <title>Same.Movie.Another.Source</title>
      <guid>magnet:?xt=urn:btih:samehash1234567890abcdef1234567890abcd</guid>
      <size>4000000000</size>
      <torznab:attr name="infohash" value="samehash1234567890abcdef1234567890abcd"/>
    </item>
  </channel>
</rss>`

	scraper := NewJackettScraper("http://localhost:9117", "testkey", "Jackett", nil)
	results, err := scraper.parseResponse([]byte(xmlResponse))
	if err != nil {
		t.Fatalf("parseResponse failed: %v", err)
	}

	// Should only have 1 result due to deduplication
	if len(results) != 1 {
		t.Errorf("expected 1 deduplicated result, got %d", len(results))
	}
}

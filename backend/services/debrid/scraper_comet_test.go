package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCometScraperName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "custom name",
			input:    "MyComet",
			expected: "MyComet",
		},
		{
			name:     "empty name falls back to default",
			input:    "",
			expected: "comet",
		},
		{
			name:     "whitespace name falls back to default",
			input:    "   ",
			expected: "comet",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewCometScraper(nil, "", "", tc.input)
			if scraper.Name() != tc.expected {
				t.Errorf("expected scraper name %q, got %q", tc.expected, scraper.Name())
			}
		})
	}
}

func TestCometScraperDefaultURL(t *testing.T) {
	scraper := NewCometScraper(nil, "", "", "")
	if scraper.baseURL != cometDefaultBaseURL {
		t.Errorf("expected default baseURL %q, got %q", cometDefaultBaseURL, scraper.baseURL)
	}
}

func TestCometScraperCustomURL(t *testing.T) {
	customURL := "https://my-comet.example.com"
	scraper := NewCometScraper(nil, customURL, "", "")
	if scraper.baseURL != customURL {
		t.Errorf("expected baseURL %q, got %q", customURL, scraper.baseURL)
	}
}

func TestCometScraperURLNormalization(t *testing.T) {
	tests := []struct {
		name        string
		inputURL    string
		expectedURL string
	}{
		{
			name:        "trailing slash removed",
			inputURL:    "https://comet.example.com/",
			expectedURL: "https://comet.example.com",
		},
		{
			name:        "manifest.json removed",
			inputURL:    "https://comet.example.com/manifest.json",
			expectedURL: "https://comet.example.com",
		},
		{
			name:        "trailing slash and manifest.json removed",
			inputURL:    "https://comet.example.com/config/manifest.json",
			expectedURL: "https://comet.example.com/config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewCometScraper(nil, tc.inputURL, "", "")
			if scraper.baseURL != tc.expectedURL {
				t.Errorf("expected baseURL %q, got %q", tc.expectedURL, scraper.baseURL)
			}
		})
	}
}

func TestCometSearchRequiresIMDBID(t *testing.T) {
	scraper := NewCometScraper(nil, "", "", "")

	req := SearchRequest{
		Query: "The Matrix",
		Parsed: ParsedQuery{
			Title:     "The Matrix",
			MediaType: MediaTypeMovie,
		},
		IMDBID: "", // No IMDB ID
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return empty results without error when no IMDB ID
	if len(results) != 0 {
		t.Errorf("expected empty results without IMDB ID, got %d", len(results))
	}
}

func TestCometSearchWithIMDBID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the URL path contains the IMDB ID
		if !strings.Contains(r.URL.Path, "tt0133093") {
			t.Errorf("expected path to contain IMDB ID tt0133093, got %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "/stream/movie/") {
			t.Errorf("expected path to contain /stream/movie/, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"streams": [
				{
					"name": "Comet\n1080p",
					"title": "The.Matrix.1999.1080p.BluRay.x264\n💾 8.5 GB\n👤 150\n⚙️ YTS",
					"infoHash": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
					"fileIdx": 0
				},
				{
					"name": "Comet\n720p",
					"title": "The.Matrix.1999.720p.BluRay.x264\n💾 2.1 GB\n👤 75\n⚙️ RARBG",
					"infoHash": "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",
					"fileIdx": 0
				}
			]
		}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "TestComet")

	req := SearchRequest{
		Query: "The Matrix",
		Parsed: ParsedQuery{
			Title:     "The Matrix",
			Year:      1999,
			MediaType: MediaTypeMovie,
		},
		IMDBID:     "tt0133093",
		MaxResults: 50,
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Verify first result
	first := results[0]
	if first.InfoHash != "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" {
		t.Errorf("unexpected infohash: %s", first.InfoHash)
	}
	if first.Resolution != "1080p" {
		t.Errorf("expected resolution 1080p, got %s", first.Resolution)
	}
	if first.Indexer != "TestComet" {
		t.Errorf("expected indexer 'TestComet', got %q", first.Indexer)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("expected magnet link, got: %s", first.Magnet)
	}
}

func TestCometSearchTVWithSeasonEpisode(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"streams": []}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	req := SearchRequest{
		Query: "Breaking Bad S01E05",
		Parsed: ParsedQuery{
			Title:     "Breaking Bad",
			Season:    1,
			Episode:   5,
			MediaType: MediaTypeSeries,
		},
		IMDBID:     "tt0903747",
		MaxResults: 50,
	}

	_, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should include season:episode in the path
	expectedPath := "/stream/series/tt0903747:1:5.json"
	if capturedPath != expectedPath {
		t.Errorf("expected path %q, got %q", expectedPath, capturedPath)
	}
}

func TestCometSearchWithOptions(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"streams": []}`))
	}))
	defer server.Close()

	options := "indexers=yts,rarbg|debrid=realdebrid"
	scraper := NewCometScraper(nil, server.URL, options, "Comet")

	req := SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Test",
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt1234567",
	}

	_, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Options should be in the path
	if !strings.Contains(capturedPath, options) {
		t.Errorf("expected path to contain options %q, got %q", options, capturedPath)
	}
}

func TestCometSearchDeduplication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"streams": [
				{
					"name": "Duplicate 1",
					"title": "Same.Movie.1080p\n💾 5 GB",
					"infoHash": "samehash12345678901234567890123456789012"
				},
				{
					"name": "Duplicate 2",
					"title": "Same.Movie.1080p\n💾 5 GB",
					"infoHash": "samehash12345678901234567890123456789012"
				},
				{
					"name": "Unique",
					"title": "Different.Movie.1080p\n💾 3 GB",
					"infoHash": "uniquehash123456789012345678901234567890"
				}
			]
		}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	req := SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Test",
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt1234567",
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should deduplicate to 2 results
	if len(results) != 2 {
		t.Errorf("expected 2 deduplicated results, got %d", len(results))
	}
}

func TestCometSearchSkipsEmptyInfoHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"streams": [
				{
					"name": "No Hash",
					"title": "Movie Without Hash",
					"infoHash": ""
				},
				{
					"name": "With Hash",
					"title": "Movie With Hash",
					"infoHash": "validhash1234567890123456789012345678901"
				}
			]
		}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	req := SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Test",
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt1234567",
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should only have 1 result (the one with valid hash)
	if len(results) != 1 {
		t.Errorf("expected 1 result (skipping empty hash), got %d", len(results))
	}
}

func TestCometSearchErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Service temporarily unavailable"))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	req := SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Test",
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt1234567",
	}

	_, err := scraper.Search(context.Background(), req)
	if err == nil {
		t.Error("expected error for 503 response, got nil")
	}

	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to mention 503 status, got: %v", err)
	}
}

func TestCometTestConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "manifest.json") {
			t.Errorf("expected path to contain manifest.json, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "comet", "name": "Comet", "version": "1.0.0"}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	err := scraper.TestConnection(context.Background())
	if err != nil {
		t.Errorf("TestConnection failed: %v", err)
	}
}

func TestCometTestConnectionWithOptions(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "comet"}`))
	}))
	defer server.Close()

	options := "myconfig"
	scraper := NewCometScraper(nil, server.URL, options, "Comet")

	err := scraper.TestConnection(context.Background())
	if err != nil {
		t.Errorf("TestConnection failed: %v", err)
	}

	expectedPath := "/" + options + "/manifest.json"
	if capturedPath != expectedPath {
		t.Errorf("expected path %q, got %q", expectedPath, capturedPath)
	}
}

func TestCometTestConnectionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	err := scraper.TestConnection(context.Background())
	if err == nil {
		t.Error("expected error for failed connection, got nil")
	}

	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status code, got: %v", err)
	}
}

func TestCometStreamAttributesParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"streams": [
				{
					"name": "Comet\n1080p",
					"title": "Movie.2024.1080p.WEB-DL.x264-GROUP\n💾 4.5 GB\n👤 250\n⚙️ TorrentGalaxy",
					"infoHash": "testhash12345678901234567890123456789012",
					"fileIdx": 2
				}
			]
		}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	req := SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Movie",
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt1234567",
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]

	// Check parsed values
	if result.Resolution != "1080p" {
		t.Errorf("expected resolution 1080p, got %s", result.Resolution)
	}
	if result.FileIndex != 2 {
		t.Errorf("expected fileIdx 2, got %d", result.FileIndex)
	}
	if result.Provider != "TorrentGalaxy" {
		t.Errorf("expected provider TorrentGalaxy, got %s", result.Provider)
	}

	// Check attributes
	if result.Attributes["scraper"] != "comet" {
		t.Errorf("expected scraper attribute 'comet', got %s", result.Attributes["scraper"])
	}
}

func TestCometSearchIMDBIDPrefix(t *testing.T) {
	tests := []struct {
		name           string
		inputIMDBID    string
		expectedInPath string
	}{
		{
			name:           "with tt prefix",
			inputIMDBID:    "tt0133093",
			expectedInPath: "tt0133093",
		},
		{
			name:           "without tt prefix",
			inputIMDBID:    "0133093",
			expectedInPath: "tt0133093",
		},
		{
			name:           "uppercase TT prefix",
			inputIMDBID:    "TT0133093",
			expectedInPath: "tt0133093",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capturedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"streams": []}`))
			}))
			defer server.Close()

			scraper := NewCometScraper(nil, server.URL, "", "Comet")

			req := SearchRequest{
				Parsed: ParsedQuery{
					Title:     "The Matrix",
					MediaType: MediaTypeMovie,
				},
				IMDBID: tc.inputIMDBID,
			}

			_, err := scraper.Search(context.Background(), req)
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}

			if !strings.Contains(capturedPath, tc.expectedInPath) {
				t.Errorf("expected path to contain %q, got %q", tc.expectedInPath, capturedPath)
			}
		})
	}
}

func TestCometSearchMaxResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"streams": [
				{"name": "1", "title": "Movie 1", "infoHash": "hash1234567890123456789012345678901234"},
				{"name": "2", "title": "Movie 2", "infoHash": "hash2234567890123456789012345678901234"},
				{"name": "3", "title": "Movie 3", "infoHash": "hash3234567890123456789012345678901234"},
				{"name": "4", "title": "Movie 4", "infoHash": "hash4234567890123456789012345678901234"},
				{"name": "5", "title": "Movie 5", "infoHash": "hash5234567890123456789012345678901234"}
			]
		}`))
	}))
	defer server.Close()

	scraper := NewCometScraper(nil, server.URL, "", "Comet")

	req := SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Movie",
			MediaType: MediaTypeMovie,
		},
		IMDBID:     "tt1234567",
		MaxResults: 3,
	}

	results, err := scraper.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should respect MaxResults limit
	if len(results) != 3 {
		t.Errorf("expected 3 results (max), got %d", len(results))
	}
}

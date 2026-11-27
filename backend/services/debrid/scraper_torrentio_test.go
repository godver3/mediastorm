package debrid

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTorrentioScraperName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "custom name",
			input:    "MyTorrentio",
			expected: "MyTorrentio",
		},
		{
			name:     "empty name falls back to default",
			input:    "",
			expected: "torrentio",
		},
		{
			name:     "whitespace name falls back to default",
			input:    "   ",
			expected: "torrentio",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewTorrentioScraper(nil, "", tc.input, "")
			if scraper.Name() != tc.expected {
				t.Errorf("expected scraper name %q, got %q", tc.expected, scraper.Name())
			}
		})
	}
}

func TestTorrentioScraperDefaultURL(t *testing.T) {
	scraper := NewTorrentioScraper(nil, "", "", "")
	if scraper.baseURL != torrentioDefaultBaseURL {
		t.Errorf("expected default baseURL %q, got %q", torrentioDefaultBaseURL, scraper.baseURL)
	}
}

func TestTorrentioScraperCustomURL(t *testing.T) {
	customURL := "https://my-torrentio.example.com"
	scraper := NewTorrentioScraper(nil, "", "", customURL)
	if scraper.baseURL != customURL {
		t.Errorf("expected baseURL %q, got %q", customURL, scraper.baseURL)
	}
}

func TestTorrentioScraperURLNormalization(t *testing.T) {
	tests := []struct {
		name        string
		inputURL    string
		expectedURL string
	}{
		{
			name:        "trailing slash removed",
			inputURL:    "https://torrentio.example.com/",
			expectedURL: "https://torrentio.example.com",
		},
		{
			name:        "multiple trailing slashes removed",
			inputURL:    "https://torrentio.example.com///",
			expectedURL: "https://torrentio.example.com",
		},
		{
			name:        "whitespace trimmed",
			inputURL:    "  https://torrentio.example.com  ",
			expectedURL: "https://torrentio.example.com",
		},
		{
			name:        "empty string uses default",
			inputURL:    "",
			expectedURL: torrentioDefaultBaseURL,
		},
		{
			name:        "whitespace-only uses default",
			inputURL:    "   ",
			expectedURL: torrentioDefaultBaseURL,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewTorrentioScraper(nil, "", "", tc.inputURL)
			if scraper.baseURL != tc.expectedURL {
				t.Errorf("expected baseURL %q, got %q", tc.expectedURL, scraper.baseURL)
			}
		})
	}
}

func TestTorrentioScraperFetchStreamsUsesCustomURL(t *testing.T) {
	// Spin up a fake server that returns a valid torrentio response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request path is correct
		if !strings.Contains(r.URL.Path, "/stream/movie/tt0133093.json") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":[{"name":"Torrentio\n4k","title":"Test.Movie.2160p","infoHash":"abc123","fileIdx":0}]}`))
	}))
	defer server.Close()

	scraper := NewTorrentioScraper(server.Client(), "", "", server.URL)
	streams, err := scraper.fetchStreams(context.Background(), "movie", "tt0133093")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(streams) == 0 {
		t.Fatal("expected at least one stream")
	}
}

func TestTorrentioScraperFetchStreamsWithOptionsAndCustomURL(t *testing.T) {
	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":[]}`))
	}))
	defer server.Close()

	scraper := NewTorrentioScraper(server.Client(), "sort=qualitysize", "", server.URL)
	scraper.fetchStreams(context.Background(), "movie", "tt0133093")

	expected := "/sort=qualitysize/stream/movie/tt0133093.json"
	if receivedPath != expected {
		t.Errorf("expected path %q, got %q", expected, receivedPath)
	}
}

func TestTorrentioScraperFetchStreamsWithoutOptions(t *testing.T) {
	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"streams":[]}`))
	}))
	defer server.Close()

	scraper := NewTorrentioScraper(server.Client(), "", "", server.URL)
	scraper.fetchStreams(context.Background(), "movie", "tt0133093")

	expected := "/stream/movie/tt0133093.json"
	if receivedPath != expected {
		t.Errorf("expected path %q, got %q", expected, receivedPath)
	}
}

func TestTorrentioScraperRetriesTransientTimeout(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"streams":[{"name":"Torrentio\\n1080p","title":"One.Piece.1173.1080p","infoHash":"abc123","fileIdx":0}]}`,
			)),
		}, nil
	})}

	scraper := NewTorrentioScraper(client, "", "", "https://torrentio.example.test")
	streams, err := scraper.fetchStreams(t.Context(), MediaTypeSeries, "tt0388629:23:18")
	if err != nil {
		t.Fatalf("fetchStreams returned error after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("request calls = %d, want 2", calls)
	}
	if len(streams) != 1 || streams[0].infoHash != "abc123" {
		t.Fatalf("unexpected streams after retry: %+v", streams)
	}
}

func TestTorrentioScraperFetchStreamsDebridResolveURL(t *testing.T) {
	hash := "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streams":[{"name":"[TB+] Torrentio\n1080p","title":"Movie.2020.1080p.WEB\n💾 2.1 GB ⚙️ YTS","url":"` +
			"http://" + r.Host + `/resolve/torbox/SECRET/` + hash + `/null/3/Movie.2020.1080p.mkv"}]}`))
	}))
	defer server.Close()

	s := NewTorrentioScraper(server.Client(), "torbox=SECRET", "", server.URL)
	streams, err := s.fetchStreams(context.Background(), MediaTypeMovie, "tt1234567")
	if err != nil {
		t.Fatalf("fetchStreams: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}
	if streams[0].infoHash != strings.ToLower(hash) || streams[0].fileIdx != 3 {
		t.Fatalf("got hash=%q idx=%d", streams[0].infoHash, streams[0].fileIdx)
	}
}

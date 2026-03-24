package debrid

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newStubClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestMediaFusionScraperName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "custom name", input: "MyMediaFusion", expected: "MyMediaFusion"},
		{name: "empty name falls back to default", input: "", expected: "mediafusion"},
		{name: "whitespace name falls back to default", input: "   ", expected: "mediafusion"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewMediaFusionScraper(nil, "", tc.input)
			if scraper.Name() != tc.expected {
				t.Errorf("expected scraper name %q, got %q", tc.expected, scraper.Name())
			}
		})
	}
}

func TestMediaFusionScraperDefaultURL(t *testing.T) {
	scraper := NewMediaFusionScraper(nil, "", "")
	if scraper.baseURL != mediafusionDefaultBaseURL {
		t.Errorf("expected default baseURL %q, got %q", mediafusionDefaultBaseURL, scraper.baseURL)
	}
}

func TestMediaFusionScraperURLNormalization(t *testing.T) {
	tests := []struct {
		name        string
		inputURL    string
		expectedURL string
	}{
		{name: "trailing slash removed", inputURL: "https://mediafusion.example.com/", expectedURL: "https://mediafusion.example.com"},
		{name: "manifest removed", inputURL: "https://mediafusion.example.com/manifest.json", expectedURL: "https://mediafusion.example.com"},
		{name: "config path kept", inputURL: "https://mediafusion.example.com/secret/config/manifest.json", expectedURL: "https://mediafusion.example.com/secret/config"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scraper := NewMediaFusionScraper(nil, tc.inputURL, "")
			if scraper.baseURL != tc.expectedURL {
				t.Errorf("expected baseURL %q, got %q", tc.expectedURL, scraper.baseURL)
			}
		})
	}
}

func TestMediaFusionSearchRequiresIMDBID(t *testing.T) {
	scraper := NewMediaFusionScraper(nil, "", "")

	results, err := scraper.Search(context.Background(), SearchRequest{
		Query: "The Matrix",
		Parsed: ParsedQuery{
			Title:     "The Matrix",
			MediaType: MediaTypeMovie,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results without IMDB ID, got %d", len(results))
	}
}

func TestMediaFusionSearchUsesPlaybackEndpoint(t *testing.T) {
	var capturedPath string
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		capturedPath = r.URL.Path
		return jsonResponse(http.StatusOK, `{
			"streams": [
				{
					"name": "MediaFusion 1080p",
					"title": "The.Matrix.1999.1080p.BluRay.x264\n💾 8.5 GB\n👤 150\n⚙️ YTS",
					"infoHash": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
					"fileIdx": 0
				}
			]
		}`), nil
	})

	scraper := NewMediaFusionScraper(client, "https://mediafusion.test", "MediaFusion")
	results, err := scraper.Search(context.Background(), SearchRequest{
		Parsed: ParsedQuery{
			Title:     "The Matrix",
			Year:      1999,
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt0133093",
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedPath != "/playback/movie/tt0133093.json" {
		t.Fatalf("expected playback path, got %q", capturedPath)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].InfoHash != "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2" {
		t.Fatalf("unexpected infohash: %s", results[0].InfoHash)
	}
	if !strings.HasPrefix(results[0].Magnet, "magnet:?xt=urn:btih:") {
		t.Fatalf("expected magnet link, got %q", results[0].Magnet)
	}
}

func TestMediaFusionSearchFallsBackToStreamEndpoint(t *testing.T) {
	var playbackCalls, streamCalls int
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/playback/movie/tt0133093.json":
			playbackCalls++
			return jsonResponse(http.StatusNotFound, `not found`), nil
		case "/stream/movie/tt0133093.json":
			streamCalls++
			return jsonResponse(http.StatusOK, `{
				"streams": [
					{
						"name": "MediaFusion 4K",
						"description": "Movie\n💾 12 GB\n⚙️ Debrid",
						"url": "https://cdn.example.com/video.mkv",
						"behaviorHints": {
							"filename": "Movie.2024.2160p.WEB-DL.mkv",
							"videoSize": 12884901888
						}
					}
				]
			}`), nil
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
			return nil, nil
		}
	})

	scraper := NewMediaFusionScraper(client, "https://mediafusion.test", "")
	results, err := scraper.Search(context.Background(), SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Movie",
			Year:      2024,
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt0133093",
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if playbackCalls != 1 || streamCalls != 1 {
		t.Fatalf("expected one playback call and one stream fallback, got playback=%d stream=%d", playbackCalls, streamCalls)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].TorrentURL != "https://cdn.example.com/video.mkv" {
		t.Fatalf("expected direct URL result, got %q", results[0].TorrentURL)
	}
	if results[0].Attributes["preresolved"] != "true" {
		t.Fatalf("expected preresolved attribute, got %#v", results[0].Attributes)
	}
	if results[0].Resolution != "2160p" {
		t.Fatalf("expected 2160p resolution, got %q", results[0].Resolution)
	}
}

func TestMediaFusionSearchTVWithSeasonEpisode(t *testing.T) {
	var capturedPath string
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		capturedPath = r.URL.Path
		return jsonResponse(http.StatusOK, `{"streams":[]}`), nil
	})

	scraper := NewMediaFusionScraper(client, "https://mediafusion.test", "")
	_, err := scraper.Search(context.Background(), SearchRequest{
		Parsed: ParsedQuery{
			Title:     "Breaking Bad",
			Season:    1,
			Episode:   5,
			MediaType: MediaTypeSeries,
		},
		IMDBID: "tt0903747",
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedPath != "/playback/series/tt0903747:1:5.json" {
		t.Fatalf("expected TV playback path, got %q", capturedPath)
	}
}

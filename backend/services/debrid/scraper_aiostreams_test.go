package debrid

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestAIOStreamsSearchSkipsStatisticAndExternalURLOnlyEntries(t *testing.T) {
	var capturedPath string
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		capturedPath = r.URL.Path
		return jsonResponse(http.StatusOK, `{
			"streams": [
				{
					"name": "🔍 Removal Reasons",
					"description": "📌 Title Matching (4)",
					"externalUrl": "https://github.com/Viren070/AIOStreams",
					"streamData": {"type": "statistic"}
				},
				{
					"name": "External details",
					"description": "Provider details page",
					"externalUrl": "https://example.test/details"
				},
				{
					"name": "AIOStreams 1080p",
					"description": "🎬 The Movie\n📡 Torrentio\n🎥 WEB-DL\n📦 2.5 GB",
					"url": "https://cdn.example.test/movie.mkv",
					"behaviorHints": {
						"filename": "The.Movie.2024.1080p.WEB-DL.mkv",
						"videoSize": 2684354560
					}
				}
			]
		}`), nil
	})

	scraper := NewAIOStreamsScraper("https://aiostreams.test/stremio/user/config/manifest.json", "AIOStreams", false, client)
	results, err := scraper.Search(context.Background(), SearchRequest{
		Parsed: ParsedQuery{
			Title:     "The Movie",
			Year:      2024,
			MediaType: MediaTypeMovie,
		},
		IMDBID: "tt1234567",
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if capturedPath != "/stremio/user/config/stream/movie/tt1234567.json" {
		t.Fatalf("expected AIOStreams stream path, got %q", capturedPath)
	}
	if len(results) != 1 {
		t.Fatalf("expected only the playable URL result, got %d", len(results))
	}
	if results[0].TorrentURL != "https://cdn.example.test/movie.mkv" {
		t.Fatalf("expected playable URL, got %q", results[0].TorrentURL)
	}
	if results[0].Attributes["stream_url"] != "https://cdn.example.test/movie.mkv" {
		t.Fatalf("expected stream_url attribute to be preserved, got %#v", results[0].Attributes)
	}
	if strings.Contains(results[0].Title, "Removal Reasons") {
		t.Fatalf("statistic entry was returned as a playable result: %#v", results[0])
	}
}

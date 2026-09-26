package debrid

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/streamheaders"
	"novastream/models"
)

type directStremioRoundTripFunc func(*http.Request) (*http.Response, error)

func (f directStremioRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func directStremioTestClient(t *testing.T, body string) *http.Client {
	t.Helper()
	return &http.Client{Transport: directStremioRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != `/configured/stream/movie/tt0133093.json` {
			t.Fatalf("request path = %q", req.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
}

func TestDirectStremioSearchParsesPenguStyleResponse(t *testing.T) {
	body := `{"streams":[
		{"name":"support","externalUrl":"https://addon.example/donate"},
		{"name":"PenguPlay 4K","description":"🍿 The Matrix (1999)\n🎞️ 4K • MKV • BluRay • HEVC • HDR • DDP 5.1 • ~36.9 Mbps\n🛰️ Source: 2Peckle\n💾 35.09 GB\n🎧 Audio: English, German","url":"https://addon.example/direct?id=old","behaviorHints":{"filename":"The.Matrix.1999.2160p.mkv4KMKV.pad-2Peckle","videoSize":37677600604,"bingeGroup":"penguplay-2peckle-4k-1","proxyHeaders":{"request":{"Referer":"https://source.example/watch","Authorization":"Bearer forbidden"}}}}
	]}`
	scraper := NewDirectStremioScraper("https://addon.example/configured/manifest.json", "PenguPlay", directStremioTestClient(t, body))
	results, err := scraper.Search(context.Background(), SearchRequest{
		IMDBID: "tt0133093",
		Parsed: ParsedQuery{Title: "The Matrix", MediaType: MediaTypeMovie},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() returned %d results", len(results))
	}
	result := results[0]
	if result.Title != "The.Matrix.1999.2160p.mkv" || result.Resolution != "2160p" || result.Provider != "2Peckle" {
		t.Fatalf("result = %#v", result)
	}
	if result.Attributes["codec"] != "HEVC" || result.Attributes["source"] != "BluRay" || result.Attributes["hdr"] != "HDR" {
		t.Fatalf("technical attributes = %#v", result.Attributes)
	}
	cleanURL, headers := streamheaders.Extract(result.TorrentURL)
	if cleanURL != "https://addon.example/direct?id=old" || headers["Referer"] != "https://source.example/watch" {
		t.Fatalf("stream URL = %q headers = %#v", cleanURL, headers)
	}
	if headers["Authorization"] != "" {
		t.Fatalf("credential header survived: %#v", headers)
	}
}

func TestDirectStremioSearchParsesTitleAndSizeFromStreamTitle(t *testing.T) {
	body := `{"streams":[{"title":"The.Matrix.1999.REMASTERED.1080p.BluRay.REMUX.AVC.DTS-HD.MA.True.mkv [a11 34.2 GB]","url":"https://stream.example/d/opaque"}]}`
	scraper := NewDirectStremioScraper("https://addon.example/configured/manifest.json", "111477", directStremioTestClient(t, body))
	results, err := scraper.Search(context.Background(), SearchRequest{
		IMDBID: "tt0133093",
		Parsed: ParsedQuery{Title: "The Matrix", MediaType: MediaTypeMovie},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() returned %d results", len(results))
	}
	result := results[0]
	if result.Title != "The.Matrix.1999.REMASTERED.1080p.BluRay.REMUX.AVC.DTS-HD.MA.True.mkv" {
		t.Fatalf("Title = %q", result.Title)
	}
	if result.SizeBytes != 34_200_000_000 {
		t.Fatalf("SizeBytes = %d, want 34200000000", result.SizeBytes)
	}
	if result.Attributes["size"] != "34.2 GB" || result.Resolution != "1080p" {
		t.Fatalf("attributes = %#v, resolution = %q", result.Attributes, result.Resolution)
	}
}

func TestRefreshDirectStremioCandidateReplacesSignedURL(t *testing.T) {
	body := `{"streams":[{"name":"PenguPlay 1080p","description":"🍿 The Matrix (1999)\n🎞️ 1080p • MP4","url":"https://addon.example/direct?psig=new","behaviorHints":{"filename":"The.Matrix.1999.1080p.mp4","videoSize":1234,"bingeGroup":"stable-group"}}]}`
	settings := config.Settings{TorrentScrapers: []config.TorrentScraperConfig{{
		Name: "PenguPlay", Type: directStremioType, URL: "https://addon.example/configured/manifest.json", Enabled: true,
	}}}
	candidate := models.NZBResult{
		Title: "The.Matrix.1999.1080p.mp4", Indexer: "PenguPlay", Link: "https://addon.example/direct?psig=old",
		Attributes: map[string]string{
			"scraper": directStremioType, "preresolved": "true", "stream_url": "https://addon.example/direct?psig=old",
			"stremio_config_name": "PenguPlay", "stremio_type": "movie", "stremio_id": "tt0133093",
			"stremio_stream_index": "0", "stremio_binge_group": "stable-group", "stremio_filename_match": "The.Matrix.1999.1080p.mp4",
		},
	}
	refreshed, err := refreshDirectStremioCandidateWithClient(context.Background(), settings, candidate, directStremioTestClient(t, body))
	if err != nil {
		t.Fatalf("refreshDirectStremioCandidateWithClient() error = %v", err)
	}
	cleanURL, _ := streamheaders.Extract(refreshed.Attributes["stream_url"])
	if cleanURL != "https://addon.example/direct?psig=new" || refreshed.Link != refreshed.Attributes["stream_url"] {
		t.Fatalf("refreshed candidate = %#v", refreshed)
	}
	if refreshed.SizeBytes != 1234 {
		t.Fatalf("SizeBytes = %d", refreshed.SizeBytes)
	}
}

func TestDirectStremioSearchSkipsDownloadOnlyEntries(t *testing.T) {
	body := `{"streams":[
		{"name":"4KHDHub 4K","description":"[10Gbps Download Only] [💾 42.16 GB] The Matrix 2160p.mkv","url":"https://download.example/file.mkv","behaviorHints":{"videoSize":45268970000,"notWebReady":true}},
		{"name":"4KHDHub 1080p","description":"[PixelDrain] [💾 6.3 GB] The Matrix 1080p.mkv","url":"https://stream.example/file.mkv","behaviorHints":{"videoSize":6762161523,"notWebReady":true}}
	]}`
	scraper := NewDirectStremioScraper("https://addon.example/configured/manifest.json", "HDHub", directStremioTestClient(t, body))
	results, err := scraper.Search(context.Background(), SearchRequest{
		IMDBID: "tt0133093",
		Parsed: ParsedQuery{Title: "The Matrix", MediaType: MediaTypeMovie},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() returned %d results, want only the streamable entry", len(results))
	}
	if results[0].TorrentURL != "https://stream.example/file.mkv" {
		t.Fatalf("remaining URL = %q", results[0].TorrentURL)
	}
}

func TestDirectStremioSearchUsesDescriptionForOpaqueDirectURL(t *testing.T) {
	body := `{"streams":[{"name":"4KHDHub 1080p","description":"[PixelDrain] [💾 6.3 GB] The Matrix (1999) REMASTERED 1080p 10bit BluRay HEVC x265.mkv\nHindi\npixeldrain | 4KHDHub","url":"https://pixeldrain.dev/api/file/tuFC27VB","behaviorHints":{"videoSize":6762161523,"notWebReady":true}}]}`
	scraper := NewDirectStremioScraper("https://addon.example/configured/manifest.json", "HDHub", directStremioTestClient(t, body))
	results, err := scraper.Search(context.Background(), SearchRequest{
		IMDBID: "tt0133093",
		Parsed: ParsedQuery{Title: "The Matrix", MediaType: MediaTypeMovie},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() returned %d results", len(results))
	}
	want := "The Matrix (1999) REMASTERED 1080p 10bit BluRay HEVC x265.mkv"
	if results[0].Title != want {
		t.Fatalf("Title = %q, want %q", results[0].Title, want)
	}
}

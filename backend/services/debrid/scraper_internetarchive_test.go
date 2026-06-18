package debrid

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestInternetArchiveSearchReturnsPlayablePreResolvedStreams(t *testing.T) {
	var searchQuery string
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/advancedsearch.php":
			searchQuery = r.URL.Query().Get("q")
			return jsonResponse(http.StatusOK, `{
				"response": {
					"docs": [
						{"identifier": "test_item", "title": "The Archive Show", "year": "1962"}
					]
				}
			}`), nil
		case "/metadata/test_item":
			return jsonResponse(http.StatusOK, `{
				"metadata": {
					"title": "The Archive Show",
					"year": "1962",
					"licenseurl": "https://creativecommons.org/publicdomain/mark/1.0/",
					"collection": ["classic_tv", "community"]
				},
				"files": [
					{"name": "test_item_meta.xml", "format": "Metadata", "size": "100"},
					{"name": "The.Archive.Show.S01E02.512kb.mp4", "format": "h.264", "size": "1000"},
					{"name": "The.Archive.Show.S01E02.720p.mp4", "format": "h.264", "size": "2000"}
				]
			}`), nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
			return nil, nil
		}
	})

	scraper := NewInternetArchiveScraper(client, "https://archive.test", "IA", map[string]string{
		"maxItems":        "4",
		"maxFilesPerItem": "2",
	})
	results, err := scraper.Search(context.Background(), SearchRequest{
		Query: "The Archive Show S01E02",
		Parsed: ParsedQuery{
			Title:     "The Archive Show",
			Year:      1962,
			Season:    1,
			Episode:   2,
			MediaType: MediaTypeSeries,
		},
		MaxResults: 5,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if !strings.Contains(searchQuery, "mediatype:movies") || !strings.Contains(searchQuery, `title:("The Archive Show")`) {
		t.Fatalf("unexpected archive search query: %q", searchQuery)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 preferred playable result, got %d", len(results))
	}
	first := results[0]
	if first.TorrentURL != "https://archive.test/download/test_item/The.Archive.Show.S01E02.720p.mp4" {
		t.Fatalf("expected highest quality mp4 first, got %q", first.TorrentURL)
	}
	if first.Attributes["preresolved"] != "true" {
		t.Fatalf("expected preresolved stream, got attrs %#v", first.Attributes)
	}
	if first.Attributes["stream_url"] != first.TorrentURL {
		t.Fatalf("stream_url attr = %q, want %q", first.Attributes["stream_url"], first.TorrentURL)
	}
	if first.Attributes["archiveIdentifier"] != "test_item" {
		t.Fatalf("archive identifier missing: %#v", first.Attributes)
	}
	if first.Attributes["licenseurl"] == "" || first.Attributes["collection"] == "" {
		t.Fatalf("expected archive metadata attrs, got %#v", first.Attributes)
	}
}

func TestInternetArchiveSearchEscapesNestedFilePath(t *testing.T) {
	scraper := NewInternetArchiveScraper(nil, "https://archive.test/", "", nil)

	got := scraper.downloadURL("item id", "folder/My Video 01.mp4")
	want := "https://archive.test/download/item%20id/folder/My%20Video%2001.mp4"
	if got != want {
		t.Fatalf("downloadURL = %q, want %q", got, want)
	}
}

func TestInternetArchiveSearchAddsEpisodeCodeFromItemDescription(t *testing.T) {
	client := newStubClient(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/advancedsearch.php":
			return jsonResponse(http.StatusOK, `{
				"response": {
					"docs": [
						{"identifier": "DragnetTheHumanBomb", "title": "Dragnet: The Human Bomb", "year": "1951"}
					]
				}
			}`), nil
		case "/metadata/DragnetTheHumanBomb":
			return jsonResponse(http.StatusOK, `{
				"metadata": {
					"title": "Dragnet: The Human Bomb",
					"description": "Joe Friday tries to stop a man from blowing up city hall.\n\nEpisode 1, Season 1 (1951)",
					"collection": ["classic_tv"]
				},
				"files": [
					{"name": "Dragnet-theHumanBomb.mp4", "title": "Dragnet-The Human Bomb", "format": "h.264", "size": "158554348"}
				]
			}`), nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
			return nil, nil
		}
	})

	scraper := NewInternetArchiveScraper(client, "https://archive.test", "IA", nil)
	results, err := scraper.Search(context.Background(), SearchRequest{
		Query: "Dragnet (1951) S01E01",
		Parsed: ParsedQuery{
			Title:     "Dragnet",
			Year:      1951,
			Season:    1,
			Episode:   1,
			MediaType: MediaTypeSeries,
		},
		MaxResults: 5,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 playable result, got %d", len(results))
	}
	if !strings.Contains(results[0].Title, "S01E01") {
		t.Fatalf("expected normalized episode title, got %q", results[0].Title)
	}
	if results[0].Attributes["archiveEpisodeMatch"] != "true" {
		t.Fatalf("expected archive episode match attr, got %#v", results[0].Attributes)
	}
}

func TestInternetArchiveResultTitleDoesNotAddWrongEpisodeCode(t *testing.T) {
	title, matched := internetArchiveResultTitle(SearchRequest{
		Query: "Dragnet (1951) S01E01",
		Parsed: ParsedQuery{
			Title:     "Dragnet",
			Season:    1,
			Episode:   1,
			MediaType: MediaTypeSeries,
		},
	}, internetArchiveItemMetadata{
		Title:       internetArchiveText("Dragnet: The Big Actor"),
		Description: internetArchiveText("Season 1, Episode 2 (1952)"),
	}, "", "Dragnet-Big Actor", "Dragnet-bigActor.mp4")

	if matched {
		t.Fatalf("unexpected episode match for title %q", title)
	}
	if strings.Contains(title, "S01E01") {
		t.Fatalf("unexpected normalized episode title: %q", title)
	}
}

func TestInternetArchivePlayableVideoFilter(t *testing.T) {
	files := []internetArchiveFile{
		{Name: "movie_meta.xml", Format: "Metadata"},
		{Name: "movie_files.xml", Format: "Metadata"},
		{Name: "movie.thumbs/movie_000001.jpg", Format: "JPEG Thumb"},
		{Name: "movie.mp4", Format: "h.264"},
		{Name: "movie.mkv", Format: "Matroska"},
	}

	got := selectInternetArchiveVideoFiles(files, SearchRequest{}, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 playable files, got %d: %#v", len(got), got)
	}
}

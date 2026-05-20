package metadata

import (
	"strings"
	"testing"
)

func TestParseYouTubeSearchResults(t *testing.T) {
	input := strings.NewReader(`{"id":"abc123","url":"https://www.youtube.com/watch?v=abc123","title":"Blade Runner 2049 Trailer","description":"Official trailer","duration":151.0,"channel":"Warner Bros.","uploader":"Warner Bros. Pictures","view_count":123456.0,"thumbnails":[{"url":"https://img.example/small.jpg","width":120.0,"height":90.0},{"url":"https://img.example/large.jpg","width":1280.0,"height":720.0}]}
{"id":"def456","title":"No webpage URL","thumbnails":[{"url":"https://img.example/medium.jpg","width":640,"height":360}]}
`)

	results, err := parseYouTubeSearchResults(input)
	if err != nil {
		t.Fatalf("parseYouTubeSearchResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	first := results[0]
	if first.ID != "abc123" || first.Title != "Blade Runner 2049 Trailer" {
		t.Fatalf("unexpected first result: %+v", first)
	}
	if first.ThumbnailURL != "https://img.example/large.jpg" {
		t.Fatalf("expected largest thumbnail, got %q", first.ThumbnailURL)
	}
	if first.Duration != 151 || first.ViewCount != 123456 {
		t.Fatalf("unexpected numeric fields: %+v", first)
	}

	second := results[1]
	if second.URL != "https://www.youtube.com/watch?v=def456" {
		t.Fatalf("expected fallback watch URL, got %q", second.URL)
	}
}

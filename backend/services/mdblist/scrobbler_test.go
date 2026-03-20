package mdblist

import (
	"novastream/models"
	"testing"
)

func TestBuildScrobbleRequest_Movie(t *testing.T) {
	update := models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      "movie-123",
		ExternalIDs: map[string]string{"tmdb": "105", "imdb": "tt0088763"},
	}

	req := BuildScrobbleRequest(update, 50.0)

	if req.Movie == nil {
		t.Fatal("expected movie to be set")
	}
	if req.Movie.IDs.TMDB != 105 {
		t.Errorf("expected TMDB 105, got %d", req.Movie.IDs.TMDB)
	}
	if req.Movie.IDs.IMDB != "tt0088763" {
		t.Errorf("expected IMDB tt0088763, got %s", req.Movie.IDs.IMDB)
	}
	if req.Progress != 50.0 {
		t.Errorf("expected progress 50.0, got %f", req.Progress)
	}
	if req.Show != nil {
		t.Error("expected show to be nil for movie")
	}
}

func TestBuildScrobbleRequest_Episode(t *testing.T) {
	update := models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "ep-456",
		SeasonNumber:  2,
		EpisodeNumber: 5,
		SeriesID:      "tmdb:series:1668",
		ExternalIDs:   map[string]string{"tmdb": "1668", "imdb": "tt0108778"},
	}

	req := BuildScrobbleRequest(update, 25.0)

	if req.Show == nil {
		t.Fatal("expected show to be set")
	}
	if req.Show.IDs.TMDB != 1668 {
		t.Errorf("expected TMDB 1668, got %d", req.Show.IDs.TMDB)
	}
	if req.Show.IDs.IMDB != "tt0108778" {
		t.Errorf("expected IMDB tt0108778, got %s", req.Show.IDs.IMDB)
	}
	if req.Show.Season == nil {
		t.Fatal("expected show.season to be set")
	}
	if req.Show.Season.Number != 2 {
		t.Errorf("expected season number 2, got %d", req.Show.Season.Number)
	}
	if req.Show.Season.Episode == nil {
		t.Fatal("expected show.season.episode to be set")
	}
	if req.Show.Season.Episode.Number != 5 {
		t.Errorf("expected episode number 5, got %d", req.Show.Season.Episode.Number)
	}
	if req.Progress != 25.0 {
		t.Errorf("expected progress 25.0, got %f", req.Progress)
	}
	if req.Movie != nil {
		t.Error("expected movie to be nil for episode")
	}
}

func TestBuildScrobbleRequest_EpisodeFallbackToSeriesID(t *testing.T) {
	update := models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "ep-789",
		SeasonNumber:  1,
		EpisodeNumber: 3,
		SeriesID:      "tmdb:series:12345",
		ExternalIDs:   map[string]string{},
	}

	req := BuildScrobbleRequest(update, 80.0)

	if req.Show == nil {
		t.Fatal("expected show to be set")
	}
	if req.Show.IDs.TMDB != 12345 {
		t.Errorf("expected TMDB 12345 from seriesID fallback, got %d", req.Show.IDs.TMDB)
	}
}

func TestBuildScrobbleRequest_EpisodeTVDBOnlySeriesID(t *testing.T) {
	// TVDB-only seriesID should NOT populate any IDs (MDBList doesn't support TVDB)
	update := models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        "ep-999",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		SeriesID:      "tvdb:series:99999",
		ExternalIDs:   map[string]string{},
	}

	req := BuildScrobbleRequest(update, 50.0)

	if req.Show == nil {
		t.Fatal("expected show to be set")
	}
	if req.Show.IDs.TMDB != 0 || req.Show.IDs.IMDB != "" {
		t.Errorf("expected empty IDs for TVDB-only seriesID, got tmdb=%d imdb=%s", req.Show.IDs.TMDB, req.Show.IDs.IMDB)
	}
}

func TestExternalIDsToScrobbleIDs(t *testing.T) {
	ids := externalIDsToScrobbleIDs(map[string]string{
		"tmdb": "105",
		"imdb": "tt0088763",
		"tvdb": "75897", // should be ignored — MDBList doesn't support TVDB
	})

	if ids.TMDB != 105 {
		t.Errorf("expected TMDB 105, got %d", ids.TMDB)
	}
	if ids.IMDB != "tt0088763" {
		t.Errorf("expected IMDB tt0088763, got %s", ids.IMDB)
	}
}

func TestExternalIDsToScrobbleIDs_Empty(t *testing.T) {
	ids := externalIDsToScrobbleIDs(map[string]string{})

	if ids.TMDB != 0 || ids.IMDB != "" {
		t.Errorf("expected zero values, got tmdb=%d imdb=%s", ids.TMDB, ids.IMDB)
	}
}

func TestSplitSeriesID(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"tvdb:series:12345", []string{"tvdb", "series", "12345"}},
		{"tmdb:movie:678", []string{"tmdb", "movie", "678"}},
		{"single", []string{"single"}},
		{"", []string{""}},
	}

	for _, tt := range tests {
		parts := splitSeriesID(tt.input)
		if len(parts) != len(tt.expected) {
			t.Errorf("splitSeriesID(%q): expected %d parts, got %d", tt.input, len(tt.expected), len(parts))
			continue
		}
		for i, p := range parts {
			if p != tt.expected[i] {
				t.Errorf("splitSeriesID(%q)[%d]: expected %q, got %q", tt.input, i, tt.expected[i], p)
			}
		}
	}
}

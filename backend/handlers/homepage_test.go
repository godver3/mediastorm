package handlers

import (
	"novastream/models"
	"testing"
	"time"
)

func TestFindMatchingProgress_EpisodePreciseMatch(t *testing.T) {
	// When filename contains S##E## pattern, the precise match should win
	// even if other episodes of the same series are in the list.
	progressList := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  3,
			EpisodeNumber: 11,
			Position:      56,
			Duration:      1517,
			UpdatedAt:     time.Date(2026, 1, 11, 19, 58, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  2,
			EpisodeNumber: 2,
			Position:      1000,
			Duration:      1491,
			UpdatedAt:     time.Date(2026, 1, 24, 21, 5, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  1,
			EpisodeNumber: 7,
			Position:      237,
			Duration:      1477,
			UpdatedAt:     time.Date(2026, 2, 23, 19, 53, 0, 0, time.UTC),
		},
	}

	filename := "Record.of.Ragnarok.S01E07.1080p.WEB.H264-SUGOI.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a match, got nil")
	}
	if match.SeasonNumber != 1 || match.EpisodeNumber != 7 {
		t.Errorf("expected S01E07, got S%02dE%02d", match.SeasonNumber, match.EpisodeNumber)
	}
}

func TestFindMatchingProgress_NameOnlyFallbackPicksMostRecent(t *testing.T) {
	// When filename does NOT contain S##E## (e.g. debrid URL without episode in name),
	// the name-only fallback should pick the most recently updated entry.
	progressList := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  3,
			EpisodeNumber: 11,
			Position:      56,
			Duration:      1517,
			UpdatedAt:     time.Date(2026, 1, 11, 19, 58, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  1,
			EpisodeNumber: 7,
			Position:      237,
			Duration:      1477,
			UpdatedAt:     time.Date(2026, 2, 23, 19, 53, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  2,
			EpisodeNumber: 2,
			Position:      1000,
			Duration:      1491,
			UpdatedAt:     time.Date(2026, 1, 24, 21, 5, 0, 0, time.UTC),
		},
	}

	// Filename has series name but no S##E## pattern
	filename := "Record.of.Ragnarok.1080p.WEB.H264-SUGOI.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a match via name-only fallback, got nil")
	}
	// Should pick S1E07 because it has the most recent UpdatedAt
	if match.SeasonNumber != 1 || match.EpisodeNumber != 7 {
		t.Errorf("expected S01E07 (most recent), got S%02dE%02d", match.SeasonNumber, match.EpisodeNumber)
	}
}

func TestFindMatchingProgress_PreciseMatchBeatsNameOnly(t *testing.T) {
	// When filename has S##E##, precise match should win even if a different
	// episode has a more recent UpdatedAt.
	progressList := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  1,
			EpisodeNumber: 7,
			Position:      237,
			Duration:      1477,
			UpdatedAt:     time.Date(2026, 2, 23, 19, 53, 0, 0, time.UTC),
		},
		{
			MediaType:     "episode",
			SeriesName:    "Record of Ragnarok",
			SeasonNumber:  3,
			EpisodeNumber: 12,
			Position:      500,
			Duration:      1500,
			// More recent than S1E07
			UpdatedAt: time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC),
		},
	}

	filename := "Record.of.Ragnarok.S01E07.1080p.WEB.H264-SUGOI.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a match, got nil")
	}
	// Should pick S1E07 via precise match, not S3E12 despite more recent timestamp
	if match.SeasonNumber != 1 || match.EpisodeNumber != 7 {
		t.Errorf("expected S01E07 (precise match), got S%02dE%02d", match.SeasonNumber, match.EpisodeNumber)
	}
}

func TestFindMatchingProgress_MovieNameMatch(t *testing.T) {
	// Movies should match on name only (no season/episode constraint).
	progressList := []models.PlaybackProgress{
		{
			MediaType: "movie",
			MovieName: "Free Guy",
			Position:  2586,
			Duration:  6898,
		},
	}

	filename := "Free.Guy.2021.1080p.BluRay.mkv"
	cleaned := cleanFilenameForMatch(filename)

	match := findMatchingProgress(progressList, cleaned, filename)
	if match == nil {
		t.Fatal("expected a movie match, got nil")
	}
	if match.MovieName != "Free Guy" {
		t.Errorf("expected Free Guy, got %s", match.MovieName)
	}
}

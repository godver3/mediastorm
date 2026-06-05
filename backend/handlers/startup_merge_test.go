package handlers

import (
	"testing"

	"novastream/models"
)

// TestMergeProgressIntoContinueWatching_SameSeriesID covers the common case where
// the playback progress entry and the continue-watching item share a series ID.
func TestMergeProgressIntoContinueWatching_SameSeriesID(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tvdb:series:450033",
			ExternalIDs: map[string]string{"tvdb": "450033"},
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			ItemID:         "tvdb:series:450033:s01e03",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			PercentWatched: 66.6,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 66.6 {
		t.Fatalf("PercentWatched = %v, want 66.6", got)
	}
	if got := merged[0].ResumePercent; got != 66.6 {
		t.Fatalf("ResumePercent = %v, want 66.6", got)
	}
}

// TestMergeProgressIntoContinueWatching_SplitSeriesID reproduces the Spider-Noir
// bug: episodes recorded under one series ID (tvdb:series:450033) while the
// continue-watching item was canonicalised under a different one (tmdb:tv:220102).
// The shared external IDs must still resolve the progress bar.
func TestMergeProgressIntoContinueWatching_SplitSeriesID(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID: "tmdb:tv:220102",
			ExternalIDs: map[string]string{
				"tvdb": "450033",
				"tmdb": "220102",
				"imdb": "tt30460310",
			},
			LastWatched: models.EpisodeReference{
				SeasonNumber:  1,
				EpisodeNumber: 3,
				EpisodeID:     "tvdb:episode:11610258",
				TvdbID:        "11610258",
			},
			NextEpisode: &models.EpisodeReference{
				SeasonNumber:  1,
				EpisodeNumber: 3,
				EpisodeID:     "tvdb:episode:11610258",
				TvdbID:        "11610258",
			},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:     "episode",
			ItemID:        "tvdb:series:450033:s01e03",
			SeriesID:      "tvdb:series:450033",
			SeasonNumber:  1,
			EpisodeNumber: 3,
			ExternalIDs: map[string]string{
				"tvdb":        "450033",
				"imdb":        "tt30460310",
				"episodeTvdb": "11610258",
			},
			PercentWatched: 66.6,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 66.6 {
		t.Fatalf("PercentWatched = %v, want 66.6 (cross-ID match failed)", got)
	}
	if got := merged[0].ResumePercent; got != 66.6 {
		t.Fatalf("ResumePercent = %v, want 66.6 (cross-ID match failed)", got)
	}
}

// TestMergeProgressIntoContinueWatching_EpisodeTvdbFallback verifies the episode
// TVDB id path matches even when no series-level external IDs overlap.
func TestMergeProgressIntoContinueWatching_EpisodeTvdbFallback(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tmdb:tv:220102",
			ExternalIDs: map[string]string{"tmdb": "220102"},
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3, TvdbID: "11610258"},
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3, TvdbID: "11610258"},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			ExternalIDs:    map[string]string{"episodeTvdb": "11610258"},
			PercentWatched: 42.0,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 42.0 {
		t.Fatalf("PercentWatched = %v, want 42.0 (episode tvdb fallback failed)", got)
	}
}

// TestMergeProgressIntoContinueWatching_NoFalseMatch ensures unrelated series do
// not borrow each other's progress via the season/episode-only key space.
func TestMergeProgressIntoContinueWatching_NoFalseMatch(t *testing.T) {
	items := []models.SeriesWatchState{
		{
			SeriesID:    "tmdb:tv:999",
			ExternalIDs: map[string]string{"tmdb": "999"},
			LastWatched: models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
			NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 3},
		},
	}
	progress := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			ExternalIDs:    map[string]string{"tvdb": "450033"},
			PercentWatched: 66.6,
		},
	}

	merged := mergeProgressIntoContinueWatching(items, progress)
	if got := merged[0].PercentWatched; got != 0 {
		t.Fatalf("PercentWatched = %v, want 0 (unrelated series must not match)", got)
	}
}

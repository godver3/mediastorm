package handlers

import (
	"testing"

	"novastream/models"
)

// TestFilterProgressForTitle_ExactSeriesID covers the common case where progress
// entries share the requested title's series ID.
func TestFilterProgressForTitle_ExactSeriesID(t *testing.T) {
	items := []models.PlaybackProgress{
		{MediaType: "episode", ItemID: "tvdb:series:450033:s01e03", SeriesID: "tvdb:series:450033", PercentWatched: 66.6},
		{MediaType: "episode", ItemID: "tvdb:series:999:s01e01", SeriesID: "tvdb:series:999", PercentWatched: 10},
	}

	filtered := filterProgressForTitle(items, "tvdb:series:450033", "series", titleExternalIDs{tvdb: "450033"})
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0].PercentWatched != 66.6 {
		t.Fatalf("PercentWatched = %v, want 66.6", filtered[0].PercentWatched)
	}
}

// TestFilterProgressForTitle_SplitSeriesID reproduces the Spider-Noir bug: the
// details page is canonicalised under tmdb:tv:220102 while the watched episode's
// progress was recorded under tvdb:series:450033. The shared tvdb external ID
// must still surface the entry so the resume modal can find it.
func TestFilterProgressForTitle_SplitSeriesID(t *testing.T) {
	items := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			ItemID:         "tvdb:series:450033:s01e03",
			SeriesID:       "tvdb:series:450033",
			SeasonNumber:   1,
			EpisodeNumber:  3,
			ExternalIDs:    map[string]string{"tvdb": "450033", "imdb": "tt30460310", "episodeTvdb": "11610258"},
			PercentWatched: 66.6,
		},
	}

	// Requested title carries the canonical tmdb ID plus the shared tvdb/imdb IDs.
	filtered := filterProgressForTitle(items, "tmdb:tv:220102", "series", titleExternalIDs{
		imdb: "tt30460310",
		tvdb: "450033",
		tmdb: "220102",
	})
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1 (cross-ID match failed)", len(filtered))
	}
	if filtered[0].PercentWatched != 66.6 {
		t.Fatalf("PercentWatched = %v, want 66.6", filtered[0].PercentWatched)
	}
}

// TestFilterProgressForTitle_ImdbCaseInsensitive verifies imdb IDs match
// regardless of case.
func TestFilterProgressForTitle_ImdbCaseInsensitive(t *testing.T) {
	items := []models.PlaybackProgress{
		{MediaType: "movie", ItemID: "some-other-id", ExternalIDs: map[string]string{"imdb": "TT1234567"}, PercentWatched: 42},
	}

	filtered := filterProgressForTitle(items, "tmdb:movie:555", "movie", titleExternalIDs{imdb: "tt1234567"})
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1 (imdb case-insensitive match failed)", len(filtered))
	}
}

// TestFilterProgressForTitle_NoCrossTypeMatch ensures a movie request does not
// borrow an episode that happens to carry the same numeric tmdb ID.
func TestFilterProgressForTitle_NoCrossTypeMatch(t *testing.T) {
	items := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			ItemID:         "tvdb:series:450033:s01e03",
			SeriesID:       "tvdb:series:450033",
			ExternalIDs:    map[string]string{"tmdb": "220102"},
			PercentWatched: 66.6,
		},
	}

	// Movie request with the same numeric tmdb ID must not match the episode.
	filtered := filterProgressForTitle(items, "tmdb:movie:220102", "movie", titleExternalIDs{tmdb: "220102"})
	if len(filtered) != 0 {
		t.Fatalf("len(filtered) = %d, want 0 (movie must not borrow episode progress)", len(filtered))
	}
}

// TestFilterProgressForTitle_NoExternalIDsNoFalseMatch ensures unrelated series
// without shared IDs are not pulled in.
func TestFilterProgressForTitle_NoExternalIDsNoFalseMatch(t *testing.T) {
	items := []models.PlaybackProgress{
		{
			MediaType:      "episode",
			ItemID:         "tvdb:series:999:s01e01",
			SeriesID:       "tvdb:series:999",
			ExternalIDs:    map[string]string{"tvdb": "999"},
			PercentWatched: 50,
		},
	}

	filtered := filterProgressForTitle(items, "tmdb:tv:220102", "series", titleExternalIDs{
		tvdb: "450033",
		tmdb: "220102",
	})
	if len(filtered) != 0 {
		t.Fatalf("len(filtered) = %d, want 0 (unrelated series must not match)", len(filtered))
	}
}

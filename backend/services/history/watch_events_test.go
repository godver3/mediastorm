package history

import (
	"testing"
	"time"

	"novastream/models"
)

func TestSeriesFullyWatched(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	details := &models.SeriesDetails{Seasons: []models.SeriesSeason{
		{Number: 0, Episodes: []models.SeriesEpisode{{SeasonNumber: 0, EpisodeNumber: 1, AiredDate: "2020-01-01"}}},
		{Number: 1, Episodes: []models.SeriesEpisode{
			{SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2020-01-01"},
			{SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2020-01-08"},
			{SeasonNumber: 1, EpisodeNumber: 3, AiredDate: "2027-01-01"}, // unaired
			{SeasonNumber: 1, EpisodeNumber: 4},                          // no date
		}},
	}}
	watched := func(eps ...int) *SeriesProgressIndex {
		idx := &SeriesProgressIndex{watched: map[string]bool{}}
		for _, e := range eps {
			idx.watched[episodeKey(1, e)] = true
		}
		return idx
	}
	if seriesFullyWatched(details, watched(1), now) {
		t.Fatal("one aired episode unwatched: want false")
	}
	if !seriesFullyWatched(details, watched(1, 2), now) {
		t.Fatal("all aired regular episodes watched (special skipped): want true")
	}
	if seriesFullyWatched(&models.SeriesDetails{}, watched(), now) {
		t.Fatal("no aired episodes: want false")
	}
}

package history

import (
	"fmt"
	"testing"
	"time"

	"novastream/models"
)

// Seasons watched more than a year ago fall outside the continue-watching
// window but must still count as watched, or a long-running show reports its
// older seasons as unwatched (e.g. "36 unwatched" when only one is new).
func TestContinueWatching_WatchedCountIncludesEpisodesOlderThanWindow(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "cw-lifetime-count"
	watched := true
	unwatched := false
	now := time.Now().UTC()
	seasons := make([]models.SeriesSeason, 0, 2)
	for season := 1; season <= 2; season++ {
		episodes := make([]models.SeriesEpisode, 0, 10)
		for ep := 1; ep <= 10; ep++ {
			episodes = append(episodes, models.SeriesEpisode{
				ID:            fmt.Sprintf("traitors-%d-%d", season, ep),
				SeasonNumber:  season,
				EpisodeNumber: ep,
				AiredDate:     "2024-01-01",
			})
		}
		seasons = append(seasons, models.SeriesSeason{Number: season, Episodes: episodes})
	}
	svc.SetMetadataService(&mockMetadataService{
		seriesByID: map[string]*models.SeriesDetails{
			"tmdb:tv:215943": {
				Title:   models.Title{ID: "tmdb:tv:215943", Name: "The Traitors", TMDBID: 215943, TVDBID: 428163},
				Seasons: seasons,
			},
		},
	})

	record := func(season, ep int, state *bool, at time.Time) {
		t.Helper()
		if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
			MediaType: "episode", ItemID: fmt.Sprintf("tmdb:tv:215943:s%02de%02d", season, ep),
			SeriesID: "tmdb:tv:215943", SeriesName: "The Traitors",
			SeasonNumber: season, EpisodeNumber: ep, Watched: state, WatchedAt: at,
			ExternalIDs: map[string]string{"tmdb": "215943", "tvdb": "428163"},
		}); err != nil {
			t.Fatalf("record S%02dE%02d error = %v", season, ep, err)
		}
	}
	// Season 1 watched two years ago, except E10 which was later unwatched.
	old := now.AddDate(-2, 0, 0)
	for ep := 1; ep <= 10; ep++ {
		record(1, ep, &watched, old.Add(time.Duration(ep)*time.Minute))
	}
	record(1, 10, &unwatched, old.Add(time.Hour))
	// Season 2 watched recently up to E9; E10 is the next episode.
	for ep := 1; ep <= 9; ep++ {
		record(2, ep, &watched, now.Add(-time.Duration(10-ep)*time.Hour))
	}

	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one series, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.TotalEpisodeCount != 20 {
		t.Fatalf("TotalEpisodeCount = %d, want 20", got.TotalEpisodeCount)
	}
	if got.WatchedEpisodeCount != 18 {
		t.Fatalf("WatchedEpisodeCount = %d, want 18 (S1E1-9 + S2E1-9)", got.WatchedEpisodeCount)
	}
	if got.NextEpisode == nil || got.NextEpisode.SeasonNumber != 2 || got.NextEpisode.EpisodeNumber != 10 {
		t.Fatalf("NextEpisode = %+v, want S02E10", got.NextEpisode)
	}
}

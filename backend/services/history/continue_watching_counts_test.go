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

// A watched row for an episode the catalog does not list (Love Is Blind's
// S10E13, which TMDB does not have) must not count as watched, or the
// unwatched badge shows one fewer than the season list (2 instead of 3).
func TestContinueWatching_WatchedCountIgnoresEpisodesOutsideCatalog(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "cw-phantom-episode"
	watched := true
	now := time.Now().UTC()
	seasonEpisodes := map[int]int{1: 12, 2: 13}
	seasons := make([]models.SeriesSeason, 0, 2)
	for season := 1; season <= 2; season++ {
		episodes := make([]models.SeriesEpisode, 0, seasonEpisodes[season])
		for ep := 1; ep <= seasonEpisodes[season]; ep++ {
			episodes = append(episodes, models.SeriesEpisode{
				ID:            fmt.Sprintf("lib-%d-%d", season, ep),
				SeasonNumber:  season,
				EpisodeNumber: ep,
				AiredDate:     "2024-01-01",
			})
		}
		seasons = append(seasons, models.SeriesSeason{Number: season, Episodes: episodes})
	}
	svc.SetMetadataService(&mockMetadataService{
		seriesByID: map[string]*models.SeriesDetails{
			"tmdb:tv:99353": {
				Title:   models.Title{ID: "tmdb:tv:99353", Name: "Love Is Blind", TMDBID: 99353},
				Seasons: seasons,
			},
		},
	})

	record := func(season, ep int, at time.Time) {
		t.Helper()
		if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
			MediaType: "episode", ItemID: fmt.Sprintf("tmdb:tv:99353:s%02de%02d", season, ep),
			SeriesID: "tmdb:tv:99353", SeriesName: "Love Is Blind",
			SeasonNumber: season, EpisodeNumber: ep, Watched: &watched, WatchedAt: at,
			ExternalIDs: map[string]string{"tmdb": "99353"},
		}); err != nil {
			t.Fatalf("record S%02dE%02d error = %v", season, ep, err)
		}
	}
	old := now.AddDate(0, -6, 0)
	for ep := 1; ep <= 13; ep++ { // S1E13 is not in the catalog
		record(1, ep, old.Add(time.Duration(ep)*time.Minute))
	}
	for ep := 1; ep <= 10; ep++ {
		record(2, ep, now.Add(-time.Duration(11-ep)*time.Hour))
	}

	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one series, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.TotalEpisodeCount != 25 {
		t.Fatalf("TotalEpisodeCount = %d, want 25", got.TotalEpisodeCount)
	}
	if got.WatchedEpisodeCount != 22 {
		t.Fatalf("WatchedEpisodeCount = %d, want 22 (S1E1-12 + S2E1-10, not the phantom S1E13)", got.WatchedEpisodeCount)
	}
}

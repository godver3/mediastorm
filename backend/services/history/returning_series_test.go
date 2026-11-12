package history

import (
	"fmt"
	"testing"
	"time"

	"novastream/models"
)

// A show with a yearly (or slower) cadence is last watched more than 365 days
// before its next season airs. A new season must bring it back to continue
// watching, but older episodes the user left unwatched must not, and ended
// series stay off.
func TestContinueWatching_ReturningSeriesOutsideRecencyWindow(t *testing.T) {
	now := time.Now().UTC()
	lastWatched := now.AddDate(-1, -3, 0) // outside the 365-day window
	day := func(t time.Time) string { return t.Format("2006-01-02") }

	makeSeries := func(lifecycle string, season2Aired time.Time, season1Eps int) *models.SeriesDetails {
		season := func(n, count int, aired time.Time) models.SeriesSeason {
			eps := make([]models.SeriesEpisode, 0, count)
			for ep := 1; ep <= count; ep++ {
				eps = append(eps, models.SeriesEpisode{
					ID: fmt.Sprintf("s%de%d", n, ep), SeasonNumber: n, EpisodeNumber: ep,
					Name: fmt.Sprintf("S%dE%d", n, ep), AiredDate: day(aired),
				})
			}
			return models.SeriesSeason{Number: n, Episodes: eps}
		}
		return &models.SeriesDetails{
			Title: models.Title{ID: "tvdb:series:100", Name: "Yearly Show", TVDBID: 100, LifecycleStatus: lifecycle},
			Seasons: []models.SeriesSeason{
				season(1, season1Eps, lastWatched.AddDate(0, -2, 0)),
				season(2, 3, season2Aired),
			},
		}
	}

	cases := []struct {
		name      string
		details   *models.SeriesDetails
		watchedS1 int
		lastWatch time.Time
		wantNext  string // "" = absent
	}{
		{"new season after last watch", makeSeries("Continuing", now.AddDate(0, 0, -3), 4), 4, lastWatched, "S2E1"},
		{"returning series with unknown lifecycle", makeSeries("", now.AddDate(0, 0, -3), 4), 4, lastWatched, "S2E1"},
		{"ended series", makeSeries("Ended", now.AddDate(0, 0, -3), 4), 4, lastWatched, ""},
		{"abandoned midway, no new season", makeSeries("Continuing", now.AddDate(0, 0, 30), 4), 2, lastWatched, ""},
		{"last watched beyond the returning window", makeSeries("Continuing", now.AddDate(0, 0, -3), 4), 4, now.AddDate(-4, 0, 0), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := NewService(t.TempDir())
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			svc.SetMetadataService(&mockMetadataService{seriesDetails: tc.details})
			watched := true
			userID := "returning"
			for ep := 1; ep <= tc.watchedS1; ep++ {
				if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
					MediaType: "episode", ItemID: fmt.Sprintf("tvdb:100:s01e%02d", ep),
					SeriesID: "tvdb:100", SeriesName: "Yearly Show",
					SeasonNumber: 1, EpisodeNumber: ep, Watched: &watched,
					WatchedAt:   tc.lastWatch.Add(time.Duration(ep) * time.Minute),
					ExternalIDs: map[string]string{"tvdb": "100"},
				}); err != nil {
					t.Fatalf("record S01E%02d: %v", ep, err)
				}
			}

			items, err := svc.ListContinueWatching(userID)
			if err != nil {
				t.Fatalf("ListContinueWatching() error = %v", err)
			}
			if tc.wantNext == "" {
				if len(items) != 0 {
					t.Fatalf("expected no items, got %+v", items)
				}
				return
			}
			if len(items) != 1 || items[0].NextEpisode == nil {
				t.Fatalf("expected one item with a next episode, got %+v", items)
			}
			next := items[0].NextEpisode
			if got := fmt.Sprintf("S%dE%d", next.SeasonNumber, next.EpisodeNumber); got != tc.wantNext {
				t.Fatalf("next episode = %s, want %s", got, tc.wantNext)
			}
			if items[0].WatchedEpisodeCount != tc.watchedS1 {
				t.Fatalf("WatchedEpisodeCount = %d, want %d", items[0].WatchedEpisodeCount, tc.watchedS1)
			}
		})
	}
}

package history

import (
	"context"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func hiddenProgressRows(t *testing.T, svc *Service, userID string) []models.PlaybackProgress {
	t.Helper()
	progress, err := svc.ListPlaybackProgress(userID)
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	var hidden []models.PlaybackProgress
	for _, p := range progress {
		if p.HiddenFromContinueWatching {
			hidden = append(hidden, p)
		}
	}
	return hidden
}

// continueWatchingID returns the Continue Watching series ID whose title contains name, or "".
func continueWatchingID(t *testing.T, svc *Service, userID, name string) string {
	t.Helper()
	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	for _, item := range items {
		if strings.Contains(item.SeriesTitle, name) {
			return item.SeriesID
		}
	}
	return ""
}

// The reported case: a show in Continue Watching only through watch history
// (next episode up, no playback row) is hidden, then unhidden from the list.
func TestUnhideFromContinueWatching_NextUpSeriesReturns(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc.SetMetadataService(&mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{Name: "Beast Wars: Transformers", Year: 1996, IMDBID: "tt0115108", TVDBID: 73562, TMDBID: 958},
			Seasons: []models.SeriesSeason{
				{Number: 1, Episodes: []models.SeriesEpisode{
					{Name: "Ep1", SeasonNumber: 1, EpisodeNumber: 1},
					{Name: "Ep2", SeasonNumber: 1, EpisodeNumber: 2},
				}},
			},
		},
	})
	userID := "user-1"
	seriesID := "tmdb:tv:958"
	watched := true
	if _, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{{
		MediaType:     "episode",
		ItemID:        seriesID + ":s01e01",
		Name:          "Ep1",
		Watched:       &watched,
		WatchedAt:     time.Now().UTC().Add(-time.Hour),
		SeriesID:      seriesID,
		SeriesName:    "Beast Wars: Transformers",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"imdb": "tt0115108", "tmdb": "958"},
	}}); err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	cwID := continueWatchingID(t, svc, userID, "Beast Wars")
	if cwID == "" {
		t.Fatal("expected the show in continue watching before hiding")
	}
	if err := svc.HideFromContinueWatching(userID, cwID); err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}
	if continueWatchingID(t, svc, userID, "Beast Wars") != "" {
		t.Fatal("show still in continue watching after hiding")
	}

	hidden, err := svc.ListHiddenFromContinueWatching(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListHiddenFromContinueWatching() error = %v", err)
	}
	if len(hidden) != 1 || hidden[0].Name != "Beast Wars: Transformers" || hidden[0].Year != 1996 {
		t.Fatalf("unexpected hidden list %+v", hidden)
	}

	if err := svc.UnhideFromContinueWatching(userID, hidden[0].ID); err != nil {
		t.Fatalf("UnhideFromContinueWatching() error = %v", err)
	}
	if continueWatchingID(t, svc, userID, "Beast Wars") == "" {
		t.Fatal("show did not return to continue watching after unhiding")
	}
}

func TestUnhideFromContinueWatching_MovieReturnsWithProgress(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	userID := "user-1"
	movieID := "tmdb:movie:438631"
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:   "movie",
		ItemID:      movieID,
		MovieName:   "Dune",
		Year:        2021,
		Position:    1200,
		Duration:    9300,
		Timestamp:   time.Now().UTC(),
		ExternalIDs: map[string]string{"tmdb": "438631", "imdb": "tt1160419"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}
	if err := svc.HideFromContinueWatching(userID, movieID); err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}
	hidden, err := svc.ListHiddenFromContinueWatching(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListHiddenFromContinueWatching() error = %v", err)
	}
	if len(hidden) != 1 || hidden[0].MediaType != "movie" || hidden[0].ID != movieID || hidden[0].Name != "Dune" {
		t.Fatalf("unexpected hidden list %+v", hidden)
	}

	// Unhide through an alias ID: the canonical row must still be matched.
	if err := svc.UnhideFromContinueWatching(userID, "tt1160419"); err != nil {
		t.Fatalf("UnhideFromContinueWatching() error = %v", err)
	}
	if rows := hiddenProgressRows(t, svc, userID); len(rows) != 0 {
		t.Fatalf("hidden rows remain after unhide: %+v", rows)
	}
	progress, _ := svc.GetPlaybackProgress(userID, "movie", movieID)
	if progress == nil || progress.Position != 1200 {
		t.Fatalf("unhide lost the movie's progress: %+v", progress)
	}
}

func TestUnhideFromContinueWatching_SeriesRemovesMarkerKeepsEpisodeProgress(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	userID := "user-1"
	seriesID := "tmdb:tv:60625"
	episodeID := seriesID + ":s09e01"
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        episodeID,
		Position:      300,
		Duration:      1300,
		SeriesID:      seriesID,
		SeriesName:    "Rick and Morty",
		SeasonNumber:  9,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"imdb": "tt2861424", "tmdb": "60625", "tvdb": "275274"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}
	if err := svc.HideFromContinueWatching(userID, seriesID); err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}
	if rows := hiddenProgressRows(t, svc, userID); len(rows) != 2 {
		t.Fatalf("expected episode row + series marker hidden, got %+v", rows)
	}

	hidden, err := svc.ListHiddenFromContinueWatching(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListHiddenFromContinueWatching() error = %v", err)
	}
	if len(hidden) != 1 || hidden[0].MediaType != "series" || hidden[0].Name != "Rick and Morty" {
		t.Fatalf("episode row and series marker should collapse into one series entry, got %+v", hidden)
	}

	if err := svc.UnhideFromContinueWatching(userID, hidden[0].ID); err != nil {
		t.Fatalf("UnhideFromContinueWatching() error = %v", err)
	}
	progress, err := svc.ListPlaybackProgress(userID)
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progress) != 1 || progress[0].ItemID != episodeID || progress[0].HiddenFromContinueWatching || progress[0].Position != 300 {
		t.Fatalf("expected only the visible episode row to remain, got %+v", progress)
	}
}

func TestUnhideFromContinueWatching_MarkerOnlyHide(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	userID := "user-1"
	seriesID := "tmdb:tv:68689"
	if err := svc.HideFromContinueWatching(userID, seriesID); err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}
	hidden, err := svc.ListHiddenFromContinueWatching(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListHiddenFromContinueWatching() error = %v", err)
	}
	if len(hidden) != 1 || hidden[0].ID != seriesID || hidden[0].MediaType != "series" {
		t.Fatalf("unexpected hidden list %+v", hidden)
	}

	if err := svc.UnhideFromContinueWatching(userID, seriesID); err != nil {
		t.Fatalf("UnhideFromContinueWatching() error = %v", err)
	}
	progress, _ := svc.ListPlaybackProgress(userID)
	if len(progress) != 0 {
		t.Fatalf("marker should be deleted, got %+v", progress)
	}
	hidden, _ = svc.ListHiddenFromContinueWatching(context.Background(), userID)
	if len(hidden) != 0 {
		t.Fatalf("hidden list should be empty, got %+v", hidden)
	}

	// Unhiding again is a harmless no-op.
	if err := svc.UnhideFromContinueWatching(userID, seriesID); err != nil {
		t.Fatalf("second UnhideFromContinueWatching() error = %v", err)
	}
}

func TestUnhideFromContinueWatching_RequiresIDs(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := svc.UnhideFromContinueWatching("", "tmdb:tv:1"); err != ErrUserIDRequired {
		t.Fatalf("expected ErrUserIDRequired, got %v", err)
	}
	if err := svc.UnhideFromContinueWatching("user", " "); err != ErrSeriesIDRequired {
		t.Fatalf("expected ErrSeriesIDRequired, got %v", err)
	}
}

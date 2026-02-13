package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

// Mock metadata service for testing
type mockMetadataService struct {
	seriesDetails *models.SeriesDetails
	movieDetails  *models.Title
	err           error
}

func (m *mockMetadataService) SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.seriesDetails, nil
}

func (m *mockMetadataService) SeriesDetailsLite(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return m.SeriesDetails(ctx, req)
}

func (m *mockMetadataService) SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.seriesDetails != nil {
		return &m.seriesDetails.Title, nil
	}
	return nil, nil
}

func (m *mockMetadataService) MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.movieDetails, nil
}

func (m *mockMetadataService) MovieInfo(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	// MovieInfo is lightweight version, same as MovieDetails for testing
	return m.MovieDetails(ctx, req)
}

func TestRecordEpisodeAndList(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// Set up mock metadata service with series details
	mockMeta := &mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:       "series-1",
				Name:     "Example Show",
				Overview: "A test show",
				Year:     2024,
				Poster: &models.Image{
					URL: "poster.jpg",
				},
				Backdrop: &models.Image{
					URL: "backdrop.jpg",
				},
			},
			Seasons: []models.SeriesSeason{
				{
					ID:     "season-1",
					Name:   "Season 1",
					Number: 1,
					Episodes: []models.SeriesEpisode{
						{
							ID:            "ep-1",
							Name:          "Pilot",
							SeasonNumber:  1,
							EpisodeNumber: 1,
							Overview:      "First episode",
						},
						{
							ID:            "ep-2",
							Name:          "Second",
							SeasonNumber:  1,
							EpisodeNumber: 2,
							Overview:      "Second episode",
						},
						{
							ID:            "ep-3",
							Name:          "Third",
							SeasonNumber:  1,
							EpisodeNumber: 3,
							Overview:      "Third episode",
						},
					},
				},
			},
		},
	}
	svc.SetMetadataService(mockMeta)

	// Record watching episode 1
	watched := true
	update := models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "series-1",
		Name:          "Pilot",
		Year:          2024,
		Watched:       &watched,
		SeasonNumber:  1,
		EpisodeNumber: 1,
		SeriesID:      "series-1",
		SeriesName:    "Example Show",
		ExternalIDs:   map[string]string{"tvdb": "123456"},
	}

	_, err = svc.UpdateWatchHistory("user-1", update)
	if err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	// List continue watching - should show episode 2 as next
	items, err := svc.ListContinueWatching("user-1")
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 continue item, got %d", len(items))
	}
	if items[0].SeriesTitle != "Example Show" {
		t.Fatalf("unexpected title %q", items[0].SeriesTitle)
	}
	if items[0].NextEpisode == nil {
		t.Fatalf("expected next episode to be set")
	}
	if items[0].NextEpisode.EpisodeNumber != 2 {
		t.Fatalf("expected next episode to be 2, got %d", items[0].NextEpisode.EpisodeNumber)
	}
	if items[0].NextEpisode.Title != "Second" {
		t.Fatalf("expected next episode title to be 'Second', got %q", items[0].NextEpisode.Title)
	}

	// Record episode 2
	update.EpisodeNumber = 2
	update.Name = "Second"
	_, err = svc.UpdateWatchHistory("user-1", update)
	if err != nil {
		t.Fatalf("UpdateWatchHistory() second error = %v", err)
	}

	// Should now show episode 3 as next
	items, err = svc.ListContinueWatching("user-1")
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 continue item, got %d", len(items))
	}
	if items[0].NextEpisode == nil || items[0].NextEpisode.EpisodeNumber != 3 {
		t.Fatalf("expected next episode to be 3, got %#v", items[0].NextEpisode)
	}

	// Record episode 3 (last episode)
	update.EpisodeNumber = 3
	update.Name = "Third"
	_, err = svc.UpdateWatchHistory("user-1", update)
	if err != nil {
		t.Fatalf("UpdateWatchHistory() third error = %v", err)
	}

	// Should now have no continue watching items (series complete)
	items, err = svc.ListContinueWatching("user-1")
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected continue list to be empty after watching all episodes, got %d", len(items))
	}
}

func TestSkippedEpisodes(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// Set up mock metadata service
	mockMeta := &mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:   "series-2",
				Name: "Test Series",
			},
			Seasons: []models.SeriesSeason{
				{
					Number: 1,
					Episodes: []models.SeriesEpisode{
						{ID: "e1", SeasonNumber: 1, EpisodeNumber: 1, Name: "E1"},
						{ID: "e2", SeasonNumber: 1, EpisodeNumber: 2, Name: "E2"},
						{ID: "e3", SeasonNumber: 1, EpisodeNumber: 3, Name: "E3"},
						{ID: "e4", SeasonNumber: 1, EpisodeNumber: 4, Name: "E4"},
					},
				},
			},
		},
	}
	svc.SetMetadataService(mockMeta)

	// Watch episode 1 and 3 (skip episode 2)
	watched := true
	for _, ep := range []int{1, 3} {
		update := models.WatchHistoryUpdate{
			MediaType:     "episode",
			ItemID:        "series-2",
			Name:          "Episode",
			Watched:       &watched,
			SeasonNumber:  1,
			EpisodeNumber: ep,
			SeriesID:      "series-2",
			SeriesName:    "Test Series",
			ExternalIDs:   map[string]string{"tvdb": "999"},
		}
		if _, err := svc.UpdateWatchHistory("user-1", update); err != nil {
			t.Fatalf("UpdateWatchHistory() error = %v", err)
		}
	}

	// Should show episode 2 as next (the skipped one)
	items, err := svc.ListContinueWatching("user-1")
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 continue item, got %d", len(items))
	}
	// Most recent is episode 3, so next unwatched after that is episode 4
	if items[0].NextEpisode == nil || items[0].NextEpisode.EpisodeNumber != 4 {
		t.Fatalf("expected next episode to be 4 (after most recent episode 3), got %#v", items[0].NextEpisode)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// Record an episode via watch history
	watched := true
	update := models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "show",
		Name:          "Pilot",
		Watched:       &watched,
		SeasonNumber:  1,
		EpisodeNumber: 1,
		SeriesID:      "show",
		SeriesName:    "Show",
	}

	if _, err := svc.UpdateWatchHistory("user", update); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	// Check that watch history file exists
	path := filepath.Join(dir, "watched_items.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected watch history file to exist: %v", err)
	}

	// Reload service to ensure persisted data is read back
	reloaded, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() reload error = %v", err)
	}

	// Verify watch history was persisted
	history, err := reloaded.ListWatchHistory("user")
	if err != nil {
		t.Fatalf("ListWatchHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 watch history item, got %d", len(history))
	}
	if history[0].Name != "Pilot" {
		t.Fatalf("unexpected episode name %q", history[0].Name)
	}
	if history[0].SeasonNumber != 1 || history[0].EpisodeNumber != 1 {
		t.Fatalf("unexpected episode numbers: S%dE%d", history[0].SeasonNumber, history[0].EpisodeNumber)
	}
}

func TestContinueWatchingWithoutMetadata(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// Don't set metadata service

	// Record an episode
	watched := true
	update := models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "series-1",
		Name:          "Pilot",
		Watched:       &watched,
		SeasonNumber:  1,
		EpisodeNumber: 1,
		SeriesID:      "series-1",
		SeriesName:    "Example Show",
	}

	if _, err := svc.UpdateWatchHistory("user-1", update); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	// Should return empty list when metadata service is not available
	items, err := svc.ListContinueWatching("user-1")
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty continue watching list without metadata service, got %d", len(items))
	}
}

func TestOldEpisodesNotIncluded(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	mockMeta := &mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{ID: "series-1", Name: "Old Show"},
			Seasons: []models.SeriesSeason{
				{
					Number: 1,
					Episodes: []models.SeriesEpisode{
						{SeasonNumber: 1, EpisodeNumber: 1, Name: "E1"},
						{SeasonNumber: 1, EpisodeNumber: 2, Name: "E2"},
					},
				},
			},
		},
	}
	svc.SetMetadataService(mockMeta)

	// Directly insert old watch history item (watched 2 years ago)
	svc.mu.Lock()
	perUser := svc.ensureWatchHistoryUserLocked("user-1")
	perUser["episode:series-1"] = models.WatchHistoryItem{
		ID:            "episode:series-1",
		MediaType:     "episode",
		ItemID:        "series-1",
		Name:          "Old Episode",
		Watched:       true,
		WatchedAt:     time.Now().UTC().AddDate(-2, 0, 0), // 2 years ago
		SeasonNumber:  1,
		EpisodeNumber: 1,
		SeriesID:      "series-1",
		SeriesName:    "Old Show",
		ExternalIDs:   map[string]string{"tvdb": "123"},
	}
	svc.mu.Unlock()

	// Should not appear in continue watching (older than 365 days)
	items, err := svc.ListContinueWatching("user-1")
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected old episodes to be filtered out, got %d items", len(items))
	}
}

func TestToggleWatchedClearsMovieProgress(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	itemID := "tvdb:movie:36417"
	if _, err := svc.UpdatePlaybackProgress("user-1", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Position:  120,
		Duration:  400,
		MovieName: "FernGully 2",
		Year:      1998,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	progressItems, err := svc.ListPlaybackProgress("user-1")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 1 {
		t.Fatalf("expected 1 in-progress item before toggle, got %d", len(progressItems))
	}

	if _, err := svc.ToggleWatched("user-1", models.WatchHistoryUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Name:      "FernGully 2",
		Year:      1998,
	}); err != nil {
		t.Fatalf("ToggleWatched() error = %v", err)
	}

	progressItems, err = svc.ListPlaybackProgress("user-1")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected playback progress to be cleared after marking watched, got %d items", len(progressItems))
	}
}

func TestUpdateWatchHistoryClearsProgress(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	itemID := "tmdb:movie:12345"
	if _, err := svc.UpdatePlaybackProgress("user-2", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Position:  60,
		Duration:  300,
		MovieName: "Test Movie",
		Year:      2024,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	watched := true
	if _, err := svc.UpdateWatchHistory("user-2", models.WatchHistoryUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Name:      "Test Movie",
		Year:      2024,
		Watched:   &watched,
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	progressItems, err := svc.ListPlaybackProgress("user-2")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected playback progress to be cleared after UpdateWatchHistory, got %d items", len(progressItems))
	}
}

func TestBulkUpdateWatchHistoryClearsEpisodeProgress(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	itemID := "tvdb:series:42:S01E01"
	if _, err := svc.UpdatePlaybackProgress("user-3", models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        itemID,
		Position:      30,
		Duration:      100,
		SeriesID:      "tvdb:series:42",
		SeriesName:    "Example Series",
		EpisodeName:   "Pilot",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	watched := true
	if _, err := svc.BulkUpdateWatchHistory("user-3", []models.WatchHistoryUpdate{
		{
			MediaType:     "episode",
			ItemID:        itemID,
			Name:          "Pilot",
			Watched:       &watched,
			SeasonNumber:  1,
			EpisodeNumber: 1,
			SeriesID:      "tvdb:series:42",
			SeriesName:    "Example Series",
		},
	}); err != nil {
		t.Fatalf("BulkUpdateWatchHistory() error = %v", err)
	}

	progressItems, err := svc.ListPlaybackProgress("user-3")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected playback progress to be cleared after bulk update, got %d items", len(progressItems))
	}
}

func TestProgressClearingIgnoresItemIDCasing(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	progressID := "tmdb:tv:200875:S01E01"
	if _, err := svc.UpdatePlaybackProgress("user-4", models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        progressID,
		Position:      100,
		Duration:      1200,
		SeriesID:      "tmdb:tv:200875",
		SeriesName:    "IT: Welcome to Derry",
		EpisodeName:   "Pilot",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	watched := true
	lowerID := strings.ToLower(progressID)
	if _, err := svc.UpdateWatchHistory("user-4", models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        lowerID,
		Name:          "Pilot",
		SeriesID:      "tmdb:tv:200875",
		SeriesName:    "IT: Welcome to Derry",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		Watched:       &watched,
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	progressItems, err := svc.ListPlaybackProgress("user-4")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected playback progress to be cleared despite casing differences, got %d items", len(progressItems))
	}
}

func TestToggleWatchedClearsProgressWhenMarkingUnwatched(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	itemID := "tmdb:movie:98765"

	// First, mark movie as watched (toggle from unwatched to watched)
	if _, err := svc.ToggleWatched("user-5", models.WatchHistoryUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Name:      "Test Movie",
		Year:      2024,
	}); err != nil {
		t.Fatalf("ToggleWatched() error = %v", err)
	}

	// Verify it's marked as watched
	item, err := svc.GetWatchHistoryItem("user-5", "movie", itemID)
	if err != nil {
		t.Fatalf("GetWatchHistoryItem() error = %v", err)
	}
	if item == nil || !item.Watched {
		t.Fatalf("expected item to be watched after first toggle")
	}

	// Add playback progress
	if _, err := svc.UpdatePlaybackProgress("user-5", models.PlaybackProgressUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Position:  500,
		Duration:  1200,
		MovieName: "Test Movie",
		Year:      2024,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Verify progress exists
	progressItems, err := svc.ListPlaybackProgress("user-5")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 1 {
		t.Fatalf("expected 1 progress item before marking unwatched, got %d", len(progressItems))
	}

	// Toggle to mark as unwatched (this should clear progress)
	if _, err := svc.ToggleWatched("user-5", models.WatchHistoryUpdate{
		MediaType: "movie",
		ItemID:    itemID,
		Name:      "Test Movie",
		Year:      2024,
	}); err != nil {
		t.Fatalf("ToggleWatched() error = %v", err)
	}

	// Verify it's now unwatched
	item, err = svc.GetWatchHistoryItem("user-5", "movie", itemID)
	if err != nil {
		t.Fatalf("GetWatchHistoryItem() error = %v", err)
	}
	if item == nil || item.Watched {
		t.Fatalf("expected item to be unwatched after second toggle")
	}

	// Verify progress is cleared when marking as unwatched
	progressItems, err = svc.ListPlaybackProgress("user-5")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected playback progress to be cleared when marking as unwatched, got %d items", len(progressItems))
	}
}

func TestBulkUpdateWatchHistoryClearsProgressWhenMarkingUnwatched(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	seriesID := "tmdb:tv:12345"
	ep1ID := seriesID + ":S01E01"
	ep2ID := seriesID + ":S01E02"

	// Mark episodes as watched
	watched := true
	if _, err := svc.BulkUpdateWatchHistory("user-6", []models.WatchHistoryUpdate{
		{
			MediaType:     "episode",
			ItemID:        ep1ID,
			Name:          "Episode 1",
			SeriesID:      seriesID,
			SeriesName:    "Test Series",
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Watched:       &watched,
		},
		{
			MediaType:     "episode",
			ItemID:        ep2ID,
			Name:          "Episode 2",
			SeriesID:      seriesID,
			SeriesName:    "Test Series",
			SeasonNumber:  1,
			EpisodeNumber: 2,
			Watched:       &watched,
		},
	}); err != nil {
		t.Fatalf("BulkUpdateWatchHistory() error = %v", err)
	}

	// Add playback progress to both episodes
	if _, err := svc.UpdatePlaybackProgress("user-6", models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        ep1ID,
		Position:      200,
		Duration:      1200,
		SeriesID:      seriesID,
		SeriesName:    "Test Series",
		EpisodeName:   "Episode 1",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	if _, err := svc.UpdatePlaybackProgress("user-6", models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        ep2ID,
		Position:      300,
		Duration:      1200,
		SeriesID:      seriesID,
		SeriesName:    "Test Series",
		EpisodeName:   "Episode 2",
		SeasonNumber:  1,
		EpisodeNumber: 2,
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Verify progress exists for both episodes
	progressItems, err := svc.ListPlaybackProgress("user-6")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 2 {
		t.Fatalf("expected 2 progress items before marking unwatched, got %d", len(progressItems))
	}

	// Mark episodes as unwatched (this should clear progress)
	unwatched := false
	if _, err := svc.BulkUpdateWatchHistory("user-6", []models.WatchHistoryUpdate{
		{
			MediaType:     "episode",
			ItemID:        ep1ID,
			Name:          "Episode 1",
			SeriesID:      seriesID,
			SeriesName:    "Test Series",
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Watched:       &unwatched,
		},
		{
			MediaType:     "episode",
			ItemID:        ep2ID,
			Name:          "Episode 2",
			SeriesID:      seriesID,
			SeriesName:    "Test Series",
			SeasonNumber:  1,
			EpisodeNumber: 2,
			Watched:       &unwatched,
		},
	}); err != nil {
		t.Fatalf("BulkUpdateWatchHistory() error = %v", err)
	}

	// Verify progress is cleared when marking as unwatched
	progressItems, err = svc.ListPlaybackProgress("user-6")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected playback progress to be cleared when marking as unwatched, got %d items", len(progressItems))
	}
}

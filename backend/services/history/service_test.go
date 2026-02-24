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

func TestCrossIDFormatProgressClearing(t *testing.T) {
	// Simulates the scenario where:
	// 1. Player records progress with tmdb:tv:224372 as seriesID
	// 2. Trakt import marks the episode as watched using tvdb:series:433631
	// 3. The progress entry should be cleared via external ID matching (shared tvdb ID)
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// Step 1: Player records progress with tmdb-based IDs
	tmdbSeriesID := "tmdb:tv:224372"
	tmdbEpID := tmdbSeriesID + ":s01e01"
	if _, err := svc.UpdatePlaybackProgress("user-cross-id", models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        tmdbEpID,
		Position:      587,
		Duration:      2512,
		SeriesID:      tmdbSeriesID,
		SeriesName:    "A Knight of the Seven Kingdoms",
		EpisodeName:   "The Hedge Knight",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"imdb": "tt27497448", "tvdb": "433631"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Verify progress exists
	progressItems, err := svc.ListPlaybackProgress("user-cross-id")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 1 {
		t.Fatalf("expected 1 progress item, got %d", len(progressItems))
	}

	// Step 2: Trakt import marks episode as watched with tvdb-based IDs
	watched := true
	tvdbSeriesID := "tvdb:series:433631"
	tvdbEpID := tvdbSeriesID + ":s01e01"
	if _, err := svc.ImportWatchHistory("user-cross-id", []models.WatchHistoryUpdate{
		{
			MediaType:     "episode",
			ItemID:        tvdbEpID,
			Name:          "The Hedge Knight",
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			ExternalIDs:   map[string]string{"tmdb": "224372", "tvdb": "433631", "imdb": "tt23974790"},
			SeasonNumber:  1,
			EpisodeNumber: 1,
			SeriesID:      tvdbSeriesID,
			SeriesName:    "A Knight of the Seven Kingdoms",
		},
	}); err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	// Step 3: Verify progress was cleared despite different ID formats
	progressItems, err = svc.ListPlaybackProgress("user-cross-id")
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progressItems) != 0 {
		t.Fatalf("expected progress to be cleared via cross-ID matching, got %d items: %+v", len(progressItems), progressItems)
	}
}

func TestRecordEpisodeAlwaysBumpsWatchedAt(t *testing.T) {
	// Verifies that RecordEpisode always sets WatchedAt to current time,
	// even if the episode was previously marked as watched with an old timestamp.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc.SetMetadataService(&mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:   "tmdb:tv:99999",
				Name: "Test Show",
			},
			Seasons: []models.SeriesSeason{
				{
					Number: 1,
					Episodes: []models.SeriesEpisode{
						{ID: "ep-1", Name: "Pilot", SeasonNumber: 1, EpisodeNumber: 1},
						{ID: "ep-2", Name: "Second", SeasonNumber: 1, EpisodeNumber: 2},
					},
				},
			},
		},
	})

	// Pre-mark episode as watched with an old timestamp (simulating Trakt import)
	watched := true
	oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := svc.UpdateWatchHistory("user-bump", models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:99999:s01e01",
		Name:          "Pilot",
		Watched:       &watched,
		WatchedAt:     oldTime,
		SeriesID:      "tmdb:tv:99999",
		SeriesName:    "Test Show",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() error = %v", err)
	}

	// Now RecordEpisode (active user watch) should bump WatchedAt
	beforeRecord := time.Now().UTC().Add(-1 * time.Second)
	if _, err := svc.RecordEpisode("user-bump", models.EpisodeWatchPayload{
		SeriesID:    "tmdb:tv:99999",
		SeriesTitle: "Test Show",
		Episode: models.EpisodeReference{
			SeasonNumber:  1,
			EpisodeNumber: 1,
			Title:         "Pilot",
		},
	}); err != nil {
		t.Fatalf("RecordEpisode() error = %v", err)
	}

	// Check that WatchedAt was bumped past the old timestamp
	items, err := svc.ListWatchHistory("user-bump")
	if err != nil {
		t.Fatalf("ListWatchHistory() error = %v", err)
	}

	var found bool
	for _, item := range items {
		if item.SeasonNumber == 1 && item.EpisodeNumber == 1 {
			found = true
			if item.WatchedAt.Before(beforeRecord) {
				t.Errorf("WatchedAt was not bumped: got %v, expected after %v", item.WatchedAt, beforeRecord)
			}
			break
		}
	}
	if !found {
		t.Fatal("episode not found in watch history")
	}
}

// Mock TraktScrobbler that records calls
type mockTraktScrobbler struct {
	movieCalls   int
	episodeCalls int
}

func (m *mockTraktScrobbler) ScrobbleMovie(userID string, tmdbID, tvdbID int, imdbID string, watchedAt time.Time) error {
	m.movieCalls++
	return nil
}

func (m *mockTraktScrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time) error {
	m.episodeCalls++
	return nil
}

func (m *mockTraktScrobbler) IsEnabled() bool {
	return true
}

func (m *mockTraktScrobbler) IsEnabledForUser(userID string) bool {
	return true
}

func TestImportWatchHistory_NoScrobble(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	scrobbler := &mockTraktScrobbler{}
	svc.SetTraktScrobbler(scrobbler)

	watched := true
	updates := []models.WatchHistoryUpdate{
		{
			MediaType:     "movie",
			ItemID:        "tmdb:movie:550",
			Name:          "Fight Club",
			Year:          1999,
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			ExternalIDs:   map[string]string{"tmdb": "550", "imdb": "tt0137523"},
		},
		{
			MediaType:     "episode",
			ItemID:        "tmdb:tv:1399:s01e01",
			Name:          "Winter Is Coming",
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			SeasonNumber:  1,
			EpisodeNumber: 1,
			SeriesID:      "tmdb:tv:1399",
			SeriesName:    "Game of Thrones",
			ExternalIDs:   map[string]string{"tvdb": "121361"},
		},
	}

	imported, err := svc.ImportWatchHistory("user-1", updates)
	if err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}
	if imported != 2 {
		t.Fatalf("expected 2 imported, got %d", imported)
	}

	// Verify scrobbler was never called
	if scrobbler.movieCalls != 0 {
		t.Fatalf("expected 0 movie scrobble calls, got %d", scrobbler.movieCalls)
	}
	if scrobbler.episodeCalls != 0 {
		t.Fatalf("expected 0 episode scrobble calls, got %d", scrobbler.episodeCalls)
	}

	// Verify items were actually recorded
	history, err := svc.ListWatchHistory("user-1")
	if err != nil {
		t.Fatalf("ListWatchHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(history))
	}
}

func TestImportWatchHistory_MostRecentWins(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	watched := true
	newerTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	olderTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Import with newer timestamp first
	_, err = svc.ImportWatchHistory("user-1", []models.WatchHistoryUpdate{
		{
			MediaType:   "movie",
			ItemID:      "tmdb:movie:550",
			Name:        "Fight Club (Newer)",
			Year:        1999,
			Watched:     &watched,
			WatchedAt:   newerTime,
			ExternalIDs: map[string]string{"tmdb": "550"},
		},
	})
	if err != nil {
		t.Fatalf("ImportWatchHistory() first error = %v", err)
	}

	// Try to import with older timestamp — should be skipped
	imported, err := svc.ImportWatchHistory("user-1", []models.WatchHistoryUpdate{
		{
			MediaType:   "movie",
			ItemID:      "tmdb:movie:550",
			Name:        "Fight Club (Older)",
			Year:        1999,
			Watched:     &watched,
			WatchedAt:   olderTime,
			ExternalIDs: map[string]string{"tmdb": "550"},
		},
	})
	if err != nil {
		t.Fatalf("ImportWatchHistory() second error = %v", err)
	}
	if imported != 0 {
		t.Fatalf("expected 0 imported (older item should be skipped), got %d", imported)
	}

	// Verify the newer name is still there
	history, err := svc.ListWatchHistory("user-1")
	if err != nil {
		t.Fatalf("ListWatchHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(history))
	}
	if history[0].Name != "Fight Club (Newer)" {
		t.Fatalf("expected name 'Fight Club (Newer)', got %q", history[0].Name)
	}
}

func TestImportWatchHistory_Dedup(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	watched := true
	watchedAt := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	updates := []models.WatchHistoryUpdate{
		{
			MediaType:   "movie",
			ItemID:      "tmdb:movie:550",
			Name:        "Fight Club",
			Year:        1999,
			Watched:     &watched,
			WatchedAt:   watchedAt,
			ExternalIDs: map[string]string{"tmdb": "550"},
		},
	}

	// First import
	imported, err := svc.ImportWatchHistory("user-1", updates)
	if err != nil {
		t.Fatalf("ImportWatchHistory() first error = %v", err)
	}
	if imported != 1 {
		t.Fatalf("expected 1 imported on first run, got %d", imported)
	}

	// Second import with same data — should return 0 (equal timestamps skipped)
	imported, err = svc.ImportWatchHistory("user-1", updates)
	if err != nil {
		t.Fatalf("ImportWatchHistory() second error = %v", err)
	}
	if imported != 0 {
		t.Fatalf("expected 0 imported on re-run (dedup), got %d", imported)
	}

	// Verify still only 1 item
	history, err := svc.ListWatchHistory("user-1")
	if err != nil {
		t.Fatalf("ListWatchHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history item after dedup, got %d", len(history))
	}
}

func TestHideFromContinueWatching_SurvivesProgressClear(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "user-1"
	seriesID := "tvdb:series:73562"

	// Step 1: Create playback progress for an episode (simulates partially watching)
	_, err = svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        seriesID + ":S01E01",
		Position:      30,
		Duration:      1200,
		SeriesID:      seriesID,
		SeriesName:    "Beast Wars: Transformers",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	})
	if err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Verify progress exists
	progress, err := svc.ListPlaybackProgress(userID)
	if err != nil {
		t.Fatalf("ListPlaybackProgress() error = %v", err)
	}
	if len(progress) != 1 {
		t.Fatalf("expected 1 progress item, got %d", len(progress))
	}

	// Step 2: Hide series from continue watching
	err = svc.HideFromContinueWatching(userID, seriesID)
	if err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}

	// Verify the entry is hidden
	progress, _ = svc.ListPlaybackProgress(userID)
	hiddenCount := 0
	for _, p := range progress {
		if p.HiddenFromContinueWatching && (p.SeriesID == seriesID || p.ItemID == seriesID) {
			hiddenCount++
		}
	}
	if hiddenCount == 0 {
		t.Fatal("expected at least one hidden progress entry for the series")
	}

	// Step 3: Mark the episode as watched (simulates Trakt sync importing it)
	// This calls clearPlaybackProgressEntryLocked internally
	watched := true
	_, err = svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{
		{
			MediaType:     "episode",
			ItemID:        seriesID + ":s01e01",
			Name:          "Beast Wars Pilot",
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			SeriesID:      seriesID,
			SeriesName:    "Beast Wars: Transformers",
			SeasonNumber:  1,
			EpisodeNumber: 1,
		},
	})
	if err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	// Step 4: Verify the hidden state is preserved via a series-level marker
	progress, _ = svc.ListPlaybackProgress(userID)
	hiddenCount = 0
	for _, p := range progress {
		if p.HiddenFromContinueWatching && (p.SeriesID == seriesID || p.ItemID == seriesID) {
			hiddenCount++
		}
	}
	if hiddenCount == 0 {
		t.Fatal("hidden state was lost after progress clear — series-level marker should have been preserved")
	}
}

func TestHideFromContinueWatching_CanonicalIDMismatch(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// Set up mock metadata service so buildContinueWatchingFromHistory can resolve series
	mockMeta := &mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				Name:   "Beast Wars: Transformers",
				Year:   1996,
				IMDBID: "tt0115108",
				TVDBID: 73562,
				TMDBID: 958,
			},
			Seasons: []models.SeriesSeason{
				{Number: 1, Episodes: []models.SeriesEpisode{
					{Name: "Ep1", SeasonNumber: 1, EpisodeNumber: 1},
					{Name: "Ep2", SeasonNumber: 1, EpisodeNumber: 2},
				}},
			},
		},
	}
	svc.SetMetadataService(mockMeta)

	userID := "user-1"
	tvdbSeriesID := "tvdb:series:73562"
	tmdbSeriesID := "tmdb:tv:958"

	// Create playback progress with tvdb series ID and shared IMDB external ID
	_, err = svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        tvdbSeriesID + ":S01E01",
		Position:      30,
		Duration:      1200,
		SeriesID:      tvdbSeriesID,
		SeriesName:    "Beast Wars: Transformers",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"imdb": "tt0115108", "tvdb": "73562"},
	})
	if err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Also add a watched episode via Trakt-style tmdb ID (this creates the canonical link)
	watched := true
	_, err = svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{
		{
			MediaType:     "episode",
			ItemID:        tmdbSeriesID + ":s01e01",
			Name:          "Beast Wars Pilot",
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			SeriesID:      tmdbSeriesID,
			SeriesName:    "Beast Wars: Transformers",
			SeasonNumber:  1,
			EpisodeNumber: 1,
			ExternalIDs:   map[string]string{"imdb": "tt0115108", "tmdb": "958"},
		},
	})
	if err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	// Verify Beast Wars appears in continue watching before hiding
	cwItems, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}
	foundBefore := false
	var cwSeriesID string
	for _, item := range cwItems {
		if strings.Contains(item.SeriesTitle, "Beast Wars") {
			foundBefore = true
			cwSeriesID = item.SeriesID
		}
	}
	if !foundBefore {
		t.Fatal("expected Beast Wars in continue watching before hide")
	}

	// Hide using the seriesID from the CW response (could be canonical tmdb ID)
	err = svc.HideFromContinueWatching(userID, cwSeriesID)
	if err != nil {
		t.Fatalf("HideFromContinueWatching() error = %v", err)
	}

	// Verify Beast Wars is gone from continue watching
	cwItems, err = svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() after hide error = %v", err)
	}
	for _, item := range cwItems {
		if strings.Contains(item.SeriesTitle, "Beast Wars") {
			t.Fatalf("Beast Wars (seriesID=%q) still in continue watching after hiding with %q", item.SeriesID, cwSeriesID)
		}
	}

	// Now play an episode using the tvdb ID (as the player would) — should unhide
	_, err = svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        tvdbSeriesID + ":S01E02",
		Position:      60,
		Duration:      1200,
		SeriesID:      tvdbSeriesID,
		SeriesName:    "Beast Wars: Transformers",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		ExternalIDs:   map[string]string{"imdb": "tt0115108", "tvdb": "73562"},
	})
	if err != nil {
		t.Fatalf("UpdatePlaybackProgress() after hide error = %v", err)
	}

	// Verify the hidden marker was cleared
	progress, _ := svc.ListPlaybackProgress(userID)
	for _, p := range progress {
		if p.HiddenFromContinueWatching && (p.SeriesID == tmdbSeriesID || p.SeriesID == tvdbSeriesID) {
			t.Fatalf("hidden marker still present after playing episode: key=%q seriesID=%q", p.ID, p.SeriesID)
		}
	}
}

// =============================================================================
// Episode state invariant tests
//
// An episode must only ever be in one state: unwatched, watching (has progress),
// or watched. Stale progress entries must be cleaned up when an episode is
// marked as watched, regardless of ID format mismatches.
// =============================================================================

func TestEpisodeState_ImportClearsProgressSameIDs(t *testing.T) {
	// Simplest case: player and Trakt use the same ID format.
	// Progress should be cleared when the episode is imported as watched.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "user-state"
	seriesID := "tvdb:series:100"
	epID := seriesID + ":s01e01"

	// Record playback progress (watching state)
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        epID,
		Position:      300,
		Duration:      2400,
		SeriesID:      seriesID,
		SeriesName:    "Test Series",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"tvdb": "100"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Verify progress exists
	progress, _ := svc.ListPlaybackProgress(userID)
	if len(progress) != 1 {
		t.Fatalf("expected 1 progress, got %d", len(progress))
	}

	// Import as watched with the same IDs
	watched := true
	if _, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{{
		MediaType:     "episode",
		ItemID:        epID,
		Name:          "Pilot",
		Watched:       &watched,
		WatchedAt:     time.Now().UTC(),
		SeriesID:      seriesID,
		SeriesName:    "Test Series",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"tvdb": "100"},
	}}); err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	// Progress must be gone
	progress, _ = svc.ListPlaybackProgress(userID)
	if len(progress) != 0 {
		t.Fatalf("expected progress cleared after import, got %d items: %+v", len(progress), progress)
	}
}

func TestEpisodeState_ImportClearsProgressCrossIDs(t *testing.T) {
	// Grey's Anatomy scenario: player uses numeric ID "1416", Trakt uses "tvdb:series:73762".
	// Progress has externalIDs with tvdb:"73762", so clearEarlierEpisodesProgressLocked
	// should match via external ID fallback.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "user-cross"
	playerSeriesID := "1416"
	traktSeriesID := "tvdb:series:73762"

	// Player records progress with numeric seriesID
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        playerSeriesID + ":s22e08",
		Position:      134,
		Duration:      2548,
		SeriesID:      playerSeriesID,
		SeriesName:    "Grey's Anatomy",
		SeasonNumber:  22,
		EpisodeNumber: 8,
		ExternalIDs:   map[string]string{"imdb": "tt0413573", "tvdb": "73762"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// Trakt imports S22E10 as watched — should clear S22E08 progress (earlier episode)
	watched := true
	if _, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{{
		MediaType:     "episode",
		ItemID:        traktSeriesID + ":s22e10",
		Name:          "Strip That Down",
		Watched:       &watched,
		WatchedAt:     time.Now().UTC(),
		SeriesID:      traktSeriesID,
		SeriesName:    "Grey's Anatomy",
		SeasonNumber:  22,
		EpisodeNumber: 10,
	}}); err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	// S22E08 progress (earlier than S22E10) should be cleaned up
	progress, _ := svc.ListPlaybackProgress(userID)
	for _, p := range progress {
		if p.SeasonNumber == 22 && p.EpisodeNumber == 8 {
			t.Fatalf("S22E08 progress should have been cleared by import of S22E10 (cross-ID match)")
		}
	}
}

func TestEpisodeState_ImportDedupStillCleansProgress(t *testing.T) {
	// When a Trakt sync re-imports an already-recorded episode (dedup skip),
	// it should still clean up stale progress under a different ID format.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "user-dedup"
	playerSeriesID := "1416"
	traktSeriesID := "tvdb:series:73762"

	// Player records progress with numeric ID
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        playerSeriesID + ":s01e05",
		Position:      600,
		Duration:      2400,
		SeriesID:      playerSeriesID,
		SeriesName:    "Grey's Anatomy",
		SeasonNumber:  1,
		EpisodeNumber: 5,
		ExternalIDs:   map[string]string{"tvdb": "73762"},
	}); err != nil {
		t.Fatalf("UpdatePlaybackProgress() error = %v", err)
	}

	// First import: Trakt marks S01E05 watched
	watched := true
	watchedAt := time.Now().UTC()
	update := models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        traktSeriesID + ":s01e05",
		Name:          "Episode 5",
		Watched:       &watched,
		WatchedAt:     watchedAt,
		SeriesID:      traktSeriesID,
		SeriesName:    "Grey's Anatomy",
		SeasonNumber:  1,
		EpisodeNumber: 5,
	}
	if _, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{update}); err != nil {
		t.Fatalf("first ImportWatchHistory() error = %v", err)
	}

	// Re-add the progress (simulating a stale entry that wasn't cleaned)
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        playerSeriesID + ":s01e05",
		Position:      600,
		Duration:      2400,
		SeriesID:      playerSeriesID,
		SeriesName:    "Grey's Anatomy",
		SeasonNumber:  1,
		EpisodeNumber: 5,
		ExternalIDs:   map[string]string{"tvdb": "73762"},
	}); err != nil {
		t.Fatalf("re-add progress error = %v", err)
	}

	// Second import: same data (dedup). Should still clean stale progress.
	if _, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{update}); err != nil {
		t.Fatalf("second ImportWatchHistory() error = %v", err)
	}

	progress, _ := svc.ListPlaybackProgress(userID)
	for _, p := range progress {
		if p.SeasonNumber == 1 && p.EpisodeNumber == 5 {
			t.Fatalf("stale progress should be cleaned even on dedup import, got: %+v", p)
		}
	}
}

func TestEpisodeState_ContinueWatchingSkipsWatchedInProgress(t *testing.T) {
	// When an in-progress episode is also in watch history (watched),
	// continue watching should use the next unwatched episode instead.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	seriesID := "tvdb:series:12345"
	userID := "user-cw"

	svc.SetMetadataService(&mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:     seriesID,
				Name:   "Test Show",
				TVDBID: 12345,
			},
			Seasons: []models.SeriesSeason{{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{ID: "ep-1", Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2025-01-01"},
					{ID: "ep-2", Name: "Ep 2", SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2025-01-08"},
					{ID: "ep-3", Name: "Ep 3", SeasonNumber: 1, EpisodeNumber: 3, AiredDate: "2025-01-15"},
				},
			}},
		},
	})

	// Create stale in-progress for S01E01 (should have been cleaned)
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        seriesID + ":s01e01",
		Position:      300,
		Duration:      2400,
		SeriesID:      seriesID,
		SeriesName:    "Test Show",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"tvdb": "12345"},
	}); err != nil {
		t.Fatal(err)
	}

	// Mark S01E01 and S01E02 as watched in history
	watched := true
	for _, ep := range []int{1, 2} {
		if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
			MediaType:     "episode",
			ItemID:        seriesID + ":s01e0" + string(rune('0'+ep)),
			Name:          "Ep",
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			SeriesID:      seriesID,
			SeriesName:    "Test Show",
			SeasonNumber:  1,
			EpisodeNumber: ep,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Continue watching should show S01E03 (next unwatched), NOT S01E01 (stale progress)
	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}

	for _, item := range items {
		if item.SeriesTitle == "Test Show" {
			if item.NextEpisode == nil {
				t.Fatal("expected a next episode for Test Show")
			}
			if item.NextEpisode.SeasonNumber == 1 && item.NextEpisode.EpisodeNumber == 1 {
				t.Fatal("continue watching should not show S01E01 (already watched) as next episode")
			}
			if item.NextEpisode.SeasonNumber != 1 || item.NextEpisode.EpisodeNumber != 3 {
				t.Fatalf("expected next episode S01E03, got S%02dE%02d",
					item.NextEpisode.SeasonNumber, item.NextEpisode.EpisodeNumber)
			}
			return
		}
	}
	t.Fatal("Test Show not found in continue watching")
}

func TestEpisodeState_ContinueWatchingUpdatedAtUsesLatestTimestamp(t *testing.T) {
	// When an in-progress episode is present, the continue watching updatedAt
	// should reflect the most recent activity (watch history or progress).
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	seriesID := "tvdb:series:55555"
	userID := "user-ts"

	svc.SetMetadataService(&mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:     seriesID,
				Name:   "Timestamp Show",
				TVDBID: 55555,
			},
			Seasons: []models.SeriesSeason{{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{ID: "ep-1", Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2025-01-01"},
					{ID: "ep-2", Name: "Ep 2", SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2025-01-08"},
				},
			}},
		},
	})

	oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recentTime := time.Now().UTC()

	// Create in-progress entry with old timestamp
	if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
		MediaType:     "episode",
		ItemID:        seriesID + ":s01e02",
		Position:      100,
		Duration:      2400,
		Timestamp:     oldTime,
		SeriesID:      seriesID,
		SeriesName:    "Timestamp Show",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		ExternalIDs:   map[string]string{"tvdb": "55555"},
	}); err != nil {
		t.Fatal(err)
	}

	// Manually set the progress updatedAt to old time
	svc.mu.Lock()
	if perUser, ok := svc.playbackProgress[userID]; ok {
		for key, prog := range perUser {
			if prog.SeasonNumber == 1 && prog.EpisodeNumber == 2 {
				prog.UpdatedAt = oldTime
				perUser[key] = prog
			}
		}
	}
	svc.mu.Unlock()

	// Add watched episode with recent timestamp
	watched := true
	if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        seriesID + ":s01e01",
		Name:          "Ep 1",
		Watched:       &watched,
		WatchedAt:     recentTime,
		SeriesID:      seriesID,
		SeriesName:    "Timestamp Show",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}

	for _, item := range items {
		if item.SeriesTitle == "Timestamp Show" {
			// UpdatedAt should be the recent watch history time, not the old progress time
			if item.UpdatedAt.Before(recentTime.Add(-2 * time.Second)) {
				t.Fatalf("updatedAt should reflect recent watch history (%v), got %v",
					recentTime, item.UpdatedAt)
			}
			return
		}
	}
	t.Fatal("Timestamp Show not found in continue watching")
}

func TestEpisodeState_MultipleStaleProgressCleanedOnBuild(t *testing.T) {
	// When multiple stale progress entries exist for the same series,
	// building continue watching should clean them all up.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	seriesID := "tvdb:series:99999"
	userID := "user-multi"

	svc.SetMetadataService(&mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:     seriesID,
				Name:   "Multi Stale Show",
				TVDBID: 99999,
			},
			Seasons: []models.SeriesSeason{{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{ID: "ep-1", Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2025-01-01"},
					{ID: "ep-2", Name: "Ep 2", SeasonNumber: 1, EpisodeNumber: 2, AiredDate: "2025-01-08"},
					{ID: "ep-3", Name: "Ep 3", SeasonNumber: 1, EpisodeNumber: 3, AiredDate: "2025-01-15"},
					{ID: "ep-4", Name: "Ep 4", SeasonNumber: 1, EpisodeNumber: 4, AiredDate: "2025-01-22"},
				},
			}},
		},
	})

	// Create stale progress for S01E01, S01E02, S01E03
	for _, ep := range []int{1, 2, 3} {
		if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
			MediaType:     "episode",
			ItemID:        seriesID + ":s01e0" + string(rune('0'+ep)),
			Position:      float64(ep * 100),
			Duration:      2400,
			SeriesID:      seriesID,
			SeriesName:    "Multi Stale Show",
			SeasonNumber:  1,
			EpisodeNumber: ep,
			ExternalIDs:   map[string]string{"tvdb": "99999"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Mark all 3 as watched in history
	watched := true
	for _, ep := range []int{1, 2, 3} {
		if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
			MediaType:     "episode",
			ItemID:        seriesID + ":s01e0" + string(rune('0'+ep)),
			Name:          "Ep",
			Watched:       &watched,
			WatchedAt:     time.Now().UTC(),
			SeriesID:      seriesID,
			SeriesName:    "Multi Stale Show",
			SeasonNumber:  1,
			EpisodeNumber: ep,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Build continue watching — this should detect and clean stale entries
	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}

	// Verify the show appears with S01E04 as next (not any stale episode)
	for _, item := range items {
		if item.SeriesTitle == "Multi Stale Show" {
			if item.NextEpisode == nil {
				t.Fatal("expected next episode S01E04")
			}
			if item.NextEpisode.EpisodeNumber != 4 {
				t.Fatalf("expected next episode E04, got E%02d", item.NextEpisode.EpisodeNumber)
			}
		}
	}

	// Wait for async cleanup goroutine
	time.Sleep(200 * time.Millisecond)

	// All 3 stale progress entries should be cleaned
	progress, _ := svc.ListPlaybackProgress(userID)
	staleCount := 0
	for _, p := range progress {
		if p.SeriesID == seriesID && p.SeasonNumber > 0 {
			staleCount++
		}
	}
	if staleCount != 0 {
		t.Fatalf("expected all stale progress entries cleaned, got %d remaining", staleCount)
	}
}

func TestEpisodeState_ClearEarlierProgressCrossIDFormat(t *testing.T) {
	// clearEarlierEpisodesProgressLocked should clear progress for earlier
	// episodes even when the seriesIDs use different formats, via external ID fallback.
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	userID := "user-earlier"

	// Create progress entries for S01E01, S01E02, S01E03 under numeric ID
	for _, ep := range []int{1, 2, 3} {
		if _, err := svc.UpdatePlaybackProgress(userID, models.PlaybackProgressUpdate{
			MediaType:     "episode",
			ItemID:        "1416:s01e0" + string(rune('0'+ep)),
			Position:      float64(ep * 100),
			Duration:      2400,
			SeriesID:      "1416",
			SeriesName:    "Grey's Anatomy",
			SeasonNumber:  1,
			EpisodeNumber: ep,
			ExternalIDs:   map[string]string{"tvdb": "73762", "imdb": "tt0413573"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Verify 3 progress entries exist
	progress, _ := svc.ListPlaybackProgress(userID)
	if len(progress) != 3 {
		t.Fatalf("expected 3 progress entries, got %d", len(progress))
	}

	// Import S01E03 as watched using tvdb format (different from numeric "1416")
	watched := true
	if _, err := svc.ImportWatchHistory(userID, []models.WatchHistoryUpdate{{
		MediaType:     "episode",
		ItemID:        "tvdb:series:73762:s01e03",
		Name:          "Winning a Battle",
		Watched:       &watched,
		WatchedAt:     time.Now().UTC(),
		SeriesID:      "tvdb:series:73762",
		SeriesName:    "Grey's Anatomy",
		SeasonNumber:  1,
		EpisodeNumber: 3,
	}}); err != nil {
		t.Fatalf("ImportWatchHistory() error = %v", err)
	}

	// All 3 progress entries should be cleared (E01, E02 are earlier; E03 is the current)
	progress, _ = svc.ListPlaybackProgress(userID)
	for _, p := range progress {
		if p.SeriesName == "Grey's Anatomy" {
			t.Fatalf("progress entry for S01E%02d should have been cleared (cross-ID), got: %+v",
				p.EpisodeNumber, p)
		}
	}
}

func TestEpisodeState_UnreleasedEpisodeFallback(t *testing.T) {
	// When all remaining episodes are unreleased, findNextUnwatchedEpisode
	// should return the first unreleased one (for "coming soon" display).
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	seriesID := "tvdb:series:77777"
	userID := "user-unrel"

	futureDate := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	svc.SetMetadataService(&mockMetadataService{
		seriesDetails: &models.SeriesDetails{
			Title: models.Title{
				ID:     seriesID,
				Name:   "Future Show",
				TVDBID: 77777,
			},
			Seasons: []models.SeriesSeason{{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{ID: "ep-1", Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: "2025-01-01"},
					{ID: "ep-2", Name: "Ep 2", SeasonNumber: 1, EpisodeNumber: 2, AiredDate: futureDate},
				},
			}},
		},
	})

	// Mark S01E01 as watched
	watched := true
	if _, err := svc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        seriesID + ":s01e01",
		Name:          "Ep 1",
		Watched:       &watched,
		WatchedAt:     time.Now().UTC(),
		SeriesID:      seriesID,
		SeriesName:    "Future Show",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := svc.ListContinueWatching(userID)
	if err != nil {
		t.Fatalf("ListContinueWatching() error = %v", err)
	}

	// Should include Future Show with S01E02 as next (unreleased fallback)
	for _, item := range items {
		if item.SeriesTitle == "Future Show" {
			if item.NextEpisode == nil {
				t.Fatal("expected unreleased S01E02 as fallback next episode")
			}
			if item.NextEpisode.EpisodeNumber != 2 {
				t.Fatalf("expected next episode E02, got E%02d", item.NextEpisode.EpisodeNumber)
			}
			if item.NextEpisode.AirDate != futureDate {
				t.Fatalf("expected air date %s, got %s", futureDate, item.NextEpisode.AirDate)
			}
			return
		}
	}
	t.Fatal("Future Show not found in continue watching — unreleased fallback should prevent exclusion")
}

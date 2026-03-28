package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/trakt"
)

type fakeSchedulerUsersProvider struct {
	users map[string]models.User
}

type fakeLocalMediaScanner struct {
	libraries []models.LocalMediaLibrary
	summaries map[string]models.LocalMediaScanSummary
	scanned   []string
}

func (f *fakeLocalMediaScanner) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	return append([]models.LocalMediaLibrary(nil), f.libraries...), nil
}

func (f *fakeLocalMediaScanner) StartScan(ctx context.Context, libraryID string) (models.LocalMediaScanSummary, error) {
	f.scanned = append(f.scanned, libraryID)
	return f.summaries[libraryID], nil
}

func (f *fakeSchedulerUsersProvider) Exists(id string) bool {
	_, ok := f.users[id]
	return ok
}

func (f *fakeSchedulerUsersProvider) ListAll() []models.User {
	result := make([]models.User, 0, len(f.users))
	for _, user := range f.users {
		result = append(result, user)
	}
	return result
}

func TestResolveProfileID(t *testing.T) {
	t.Run("existing profile passes through", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"prof-1": {ID: "prof-1", Name: "Primary Profile"},
				},
			},
		}

		got, err := svc.resolveProfileID("prof-1")
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "prof-1" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "prof-1")
		}
	})

	t.Run("legacy default resolves to sole profile", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: "Only Profile"},
				},
			},
		}

		got, err := svc.resolveProfileID(models.DefaultUserID)
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "uuid-1" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "uuid-1")
		}
	})

	t.Run("legacy default resolves to primary profile by name", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: "Kids"},
					"uuid-2": {ID: "uuid-2", Name: models.DefaultUserName},
				},
			},
		}

		got, err := svc.resolveProfileID(models.DefaultUserID)
		if err != nil {
			t.Fatalf("resolveProfileID() error = %v", err)
		}
		if got != "uuid-2" {
			t.Fatalf("resolveProfileID() = %q, want %q", got, "uuid-2")
		}
	})

	t.Run("unknown non-legacy profile fails", func(t *testing.T) {
		svc := &Service{
			usersService: &fakeSchedulerUsersProvider{
				users: map[string]models.User{
					"uuid-1": {ID: "uuid-1", Name: models.DefaultUserName},
				},
			},
		}

		_, err := svc.resolveProfileID("missing")
		if err == nil {
			t.Fatal("resolveProfileID() error = nil, want error")
		}
		if !strings.Contains(err.Error(), `profile "missing" not found`) {
			t.Fatalf("resolveProfileID() error = %v, want missing profile error", err)
		}
	})
}

func TestIsNewerWatchState_PrefersExplicitUnwatchedOnTimestampTie(t *testing.T) {
	ts := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	watched := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   true,
		WatchedAt: ts,
		UpdatedAt: ts,
	}
	unwatched := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01:manual",
		Watched:   false,
		WatchedAt: ts,
		UpdatedAt: ts,
	}

	if !isNewerWatchState(unwatched, watched) {
		t.Fatal("expected explicit unwatched state to win on equal timestamps")
	}
	if isNewerWatchState(watched, unwatched) {
		t.Fatal("expected watched state to lose on equal timestamps against explicit unwatched state")
	}
}

func TestIsNewerWatchState_PrefersNewerTimestamp(t *testing.T) {
	older := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   false,
		WatchedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
	}
	newer := &models.WatchHistoryItem{
		ID:        "episode:show:s01e01",
		Watched:   true,
		WatchedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
	}

	if !isNewerWatchState(newer, older) {
		t.Fatal("expected newer timestamp to win")
	}
	if isNewerWatchState(older, newer) {
		t.Fatal("expected older timestamp to lose")
	}
}

func TestLatestWatchStateForItem_PrefersNewestStateAcrossIDVariants(t *testing.T) {
	dir := t.TempDir()
	historySvc, err := history.NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	svc := &Service{historyService: historySvc}
	userID := "user-1"
	watched := true
	unwatched := false
	ts := time.Now().UTC().Add(-2 * time.Hour)

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tmdb:tv:100:s03e11",
		Watched:       &watched,
		WatchedAt:     ts,
		SeriesID:      "tmdb:tv:100",
		SeriesName:    "Ragnarok",
		SeasonNumber:  3,
		EpisodeNumber: 11,
		ExternalIDs:   map[string]string{"tmdb": "100", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() watched error = %v", err)
	}

	if _, err := historySvc.UpdateWatchHistory(userID, models.WatchHistoryUpdate{
		MediaType:     "episode",
		ItemID:        "tvdb:series:393810:s03e11",
		Watched:       &unwatched,
		SeriesID:      "tvdb:series:393810",
		SeriesName:    "Ragnarok",
		SeasonNumber:  3,
		EpisodeNumber: 11,
		ExternalIDs:   map[string]string{"tmdb": "100", "tvdb": "393810"},
	}); err != nil {
		t.Fatalf("UpdateWatchHistory() unwatched error = %v", err)
	}

	item, err := svc.latestWatchStateForItem(userID, "episode", []string{
		"tmdb:tv:100:s03e11",
		"tvdb:series:393810:s03e11",
	})
	if err != nil {
		t.Fatalf("latestWatchStateForItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected a watch state")
	}
	if item.Watched {
		t.Fatal("expected newer explicit unwatched state to win across ID variants")
	}
}

func TestExecuteLocalMediaScan_AllLibraries(t *testing.T) {
	scanner := &fakeLocalMediaScanner{
		libraries: []models.LocalMediaLibrary{
			{ID: "lib-1", Name: "Movies"},
			{ID: "lib-2", Name: "Shows"},
		},
		summaries: map[string]models.LocalMediaScanSummary{
			"lib-1": {Discovered: 10, Matched: 8, LowConfidence: 1, Unmatched: 1},
			"lib-2": {Discovered: 20, Matched: 18, LowConfidence: 1, Unmatched: 1},
		},
	}
	svc := &Service{localMediaService: scanner}

	result, err := svc.executeLocalMediaScan(config.ScheduledTask{
		Config: map[string]string{
			"libraryId": config.ScheduledTaskLocalMediaAllLibraries,
		},
	})
	if err != nil {
		t.Fatalf("executeLocalMediaScan() error = %v", err)
	}
	if got, want := len(scanner.scanned), 2; got != want {
		t.Fatalf("scanned %d libraries, want %d", got, want)
	}
	if scanner.scanned[0] != "lib-1" || scanner.scanned[1] != "lib-2" {
		t.Fatalf("scan order = %v, want [lib-1 lib-2]", scanner.scanned)
	}
	if result.Count != 30 {
		t.Fatalf("result.Count = %d, want 30", result.Count)
	}
	if !strings.Contains(result.Message, "completed for 2 libraries") {
		t.Fatalf("result.Message = %q, want aggregated all-libraries summary", result.Message)
	}
}

func TestPlaybackPercentThresholds(t *testing.T) {
	tests := []struct {
		name         string
		percent      float64
		wantProgress bool
		wantStop     bool
		wantWatched  bool
	}{
		{name: "below minimum is ignored", percent: 1, wantProgress: false, wantStop: false, wantWatched: false},
		{name: "mid progress uses pause", percent: 45, wantProgress: true, wantStop: false, wantWatched: false},
		{name: "high incomplete progress uses stop", percent: 87, wantProgress: true, wantStop: true, wantWatched: false},
		{name: "watched threshold becomes history", percent: 90, wantProgress: false, wantStop: false, wantWatched: true},
		{name: "above watched threshold becomes history", percent: 95, wantProgress: false, wantStop: false, wantWatched: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := playbackPercentCountsAsProgress(tt.percent); got != tt.wantProgress {
				t.Fatalf("playbackPercentCountsAsProgress(%v) = %v, want %v", tt.percent, got, tt.wantProgress)
			}
			if got := playbackPercentNeedsStop(tt.percent); got != tt.wantStop {
				t.Fatalf("playbackPercentNeedsStop(%v) = %v, want %v", tt.percent, got, tt.wantStop)
			}
			if got := playbackPercentCountsAsWatched(tt.percent); got != tt.wantWatched {
				t.Fatalf("playbackPercentCountsAsWatched(%v) = %v, want %v", tt.percent, got, tt.wantWatched)
			}
		})
	}
}

func TestTraktPlaybackItemToHistoryUpdate_Episode(t *testing.T) {
	svc := &Service{}
	pausedAt := time.Date(2026, 3, 28, 11, 30, 0, 0, time.UTC)

	update := svc.traktPlaybackItemToHistoryUpdate(trakt.PlaybackItem{
		Progress: 95,
		PausedAt: pausedAt,
		Type:     "episode",
		Show: &trakt.Show{
			Title: "Example Show",
			IDs: trakt.IDs{
				TVDB: 73762,
				IMDB: "tt0413573",
			},
		},
		Episode: &trakt.Episode{
			Season: 22,
			Number: 10,
			Title:  "Strip That Down",
		},
	})

	if update == nil {
		t.Fatal("expected watch history update")
	}
	if update.MediaType != "episode" {
		t.Fatalf("MediaType = %q, want episode", update.MediaType)
	}
	if update.ItemID != "tvdb:series:73762:s22e10" {
		t.Fatalf("ItemID = %q", update.ItemID)
	}
	if update.SeriesID != "tvdb:series:73762" {
		t.Fatalf("SeriesID = %q", update.SeriesID)
	}
	if update.SeriesName != "Example Show" {
		t.Fatalf("SeriesName = %q", update.SeriesName)
	}
	if update.Name != "Strip That Down" {
		t.Fatalf("Name = %q", update.Name)
	}
	if update.Watched == nil || !*update.Watched {
		t.Fatal("expected watched=true")
	}
	if !update.WatchedAt.Equal(pausedAt) {
		t.Fatalf("WatchedAt = %v, want %v", update.WatchedAt, pausedAt)
	}
}

func TestTraktPlaybackItemToHistoryUpdate_Movie(t *testing.T) {
	svc := &Service{}
	pausedAt := time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC)

	update := svc.traktPlaybackItemToHistoryUpdate(trakt.PlaybackItem{
		Progress: 92,
		PausedAt: pausedAt,
		Type:     "movie",
		Movie: &trakt.Movie{
			Title: "The Matrix",
			Year:  1999,
			IDs: trakt.IDs{
				TMDB: 603,
				IMDB: "tt0133093",
			},
		},
	})

	if update == nil {
		t.Fatal("expected watch history update")
	}
	if update.MediaType != "movie" {
		t.Fatalf("MediaType = %q, want movie", update.MediaType)
	}
	if update.ItemID != "tmdb:movie:603" {
		t.Fatalf("ItemID = %q", update.ItemID)
	}
	if update.Name != "The Matrix" {
		t.Fatalf("Name = %q", update.Name)
	}
	if update.Year != 1999 {
		t.Fatalf("Year = %d", update.Year)
	}
	if update.Watched == nil || !*update.Watched {
		t.Fatal("expected watched=true")
	}
	if !update.WatchedAt.Equal(pausedAt) {
		t.Fatalf("WatchedAt = %v, want %v", update.WatchedAt, pausedAt)
	}
}

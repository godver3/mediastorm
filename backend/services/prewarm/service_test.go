package prewarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/playback"
)

// Mock implementations

type mockHistoryProvider struct {
	continueWatching map[string][]models.SeriesWatchState
}

func (m *mockHistoryProvider) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	return m.continueWatching[userID], nil
}

type mockUsersProvider struct {
	users []models.User
}

func (m *mockUsersProvider) ListAll() []models.User {
	return m.users
}

type mockShelfProvider struct {
	items      map[string][]ShelfItem
	lastUserID string
	lastID     string
	lastScope  string
}

func (m *mockShelfProvider) ListPrewarmShelfItems(_ context.Context, userID, shelfID, itemScope string) ([]ShelfItem, error) {
	m.lastUserID = userID
	m.lastID = shelfID
	m.lastScope = itemScope
	return m.items[shelfID], nil
}

func TestParseShelfSelectionsLegacyAndNormalization(t *testing.T) {
	legacy, err := ParseShelfSelections(nil)
	if err != nil {
		t.Fatalf("ParseShelfSelections legacy: %v", err)
	}
	if len(legacy) != 1 || legacy[0].ID != "continue-watching" || legacy[0].PlayedWithinDays != 14 {
		t.Fatalf("unexpected legacy selections: %+v", legacy)
	}

	got, err := ParseShelfSelections(map[string]string{
		PrewarmShelfSelectionsConfigKey: `[{"id":"continue-watching","playedWithinDays":30},{"id":"watchlist"},{"id":"trending-tv","itemScope":"displayed"}]`,
	})
	if err != nil {
		t.Fatalf("ParseShelfSelections configured: %v", err)
	}
	if len(got) != 3 || got[0].PlayedWithinDays != 30 || got[1].ItemScope != PrewarmItemScopeAll || got[2].ItemScope != PrewarmItemScopeDisplayed {
		t.Fatalf("unexpected configured selections: %+v", got)
	}
}

func TestParseShelfSelectionsRejectsInvalidConfig(t *testing.T) {
	tests := []string{
		`[]`,
		`[{"id":"watchlist","itemScope":"visible"}]`,
		`[{"id":"continue-watching","playedWithinDays":3651}]`,
		`[{"id":"watchlist"},{"id":"watchlist"}]`,
	}
	for _, raw := range tests {
		if _, err := ParseShelfSelections(map[string]string{PrewarmShelfSelectionsConfigKey: raw}); err == nil {
			t.Errorf("expected %s to be rejected", raw)
		}
	}
}

func TestRunOnceWithConfigWarmsSelectedShelf(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	provider := &mockShelfProvider{items: map[string][]ShelfItem{
		"watchlist": {{TitleID: "movie-1", TitleName: "Selected Movie", MediaType: "movie", Year: 2025, ImdbID: "tt123"}},
	}}
	workerCalls := 0
	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: map[string][]models.SeriesWatchState{"user1": {}}})
	svc.SetUsersService(&mockUsersProvider{users: []models.User{{ID: "user1", Name: "Alice"}}})
	svc.SetShelfProvider(provider)
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(readyWorkerFn(store, &workerCalls, nil))

	result, err := svc.RunOnceWithConfig(context.Background(), map[string]string{
		PrewarmShelfSelectionsConfigKey: `[{"id":"watchlist","itemScope":"displayed"}]`,
	})
	if err != nil {
		t.Fatalf("RunOnceWithConfig: %v", err)
	}
	if result.Warmed != 1 || workerCalls != 1 {
		t.Fatalf("result=%+v workerCalls=%d, want one warm", result, workerCalls)
	}
	if provider.lastUserID != "user1" || provider.lastID != "watchlist" || provider.lastScope != PrewarmItemScopeDisplayed {
		t.Fatalf("unexpected provider call: user=%q shelf=%q scope=%q", provider.lastUserID, provider.lastID, provider.lastScope)
	}
}

func TestRunOnceWithConfigUsesContinueWatchingPlayedWithinDays(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	workerCalls := 0
	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: map[string][]models.SeriesWatchState{
		"user1": {{
			SeriesID: "series-1", SeriesTitle: "Returning Show", UpdatedAt: time.Now().UTC().Add(-20 * 24 * time.Hour),
			NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 1},
		}},
	}})
	svc.SetUsersService(&mockUsersProvider{users: []models.User{{ID: "user1", Name: "Alice"}}})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(readyWorkerFn(store, &workerCalls, nil))

	result, err := svc.RunOnceWithConfig(context.Background(), map[string]string{
		PrewarmShelfSelectionsConfigKey: `[{"id":"continue-watching","playedWithinDays":30}]`,
	})
	if err != nil {
		t.Fatalf("RunOnceWithConfig: %v", err)
	}
	if result.Warmed != 1 || workerCalls != 1 {
		t.Fatalf("result=%+v workerCalls=%d, want item inside configured 30-day window warmed", result, workerCalls)
	}
}

type mockDebridRefresher struct {
	calls []string
	err   error
}

func (m *mockDebridRefresher) GetDirectURL(ctx context.Context, path string) (string, error) {
	m.calls = append(m.calls, path)
	if m.err != nil {
		return "", m.err
	}
	return "https://debrid.example.com/refreshed/" + path, nil
}

func TestRunOnce_WarmsItems(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
	}

	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				Year:        2008,
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
				ExternalIDs: map[string]string{"imdbId": "tt0903747"},
			},
		},
	}

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		// Simulate creating a prequeue entry
		entry, _ := store.Create(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm")
		store.Update(entry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/debrid/rd/123/456"
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
		})
		return entry.ID, nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if result.Warmed != 1 {
		t.Errorf("expected 1 warmed, got %d", result.Warmed)
	}
	if workerCalls != 1 {
		t.Errorf("expected 1 worker call, got %d", workerCalls)
	}
}

func TestRunOnce_SkipsItemsWithoutRecentActivity(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
	}

	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "old-title",
				SeriesTitle: "Old Show",
				UpdatedAt:   time.Now().UTC().Add(-(continueWatchingPrewarmMaxAge + time.Hour)),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
			},
			{
				SeriesID:    "missing-activity",
				SeriesTitle: "Missing Activity",
				NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			},
		},
	}

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		return "", nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if result.Warmed != 0 {
		t.Errorf("expected 0 warmed, got %d", result.Warmed)
	}
	if result.Skipped != 2 {
		t.Errorf("expected 2 skipped, got %d", result.Skipped)
	}
	if workerCalls != 0 {
		t.Errorf("expected 0 worker calls, got %d", workerCalls)
	}
}

func TestRunOnce_SkipsAlreadyWarmed(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
	}

	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
			},
		},
	}

	// Pre-create a ready entry
	entry, _ := store.Create("title1", "Breaking Bad", "user1", "series", 0, &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3}, "prewarm")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/debrid/rd/123/456"
		e.AudioTracks = []playback.AudioTrackInfo{
			{Index: 0, Language: "eng", Codec: "aac", Title: "English"},
		}
	})

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		return "", nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	// Pre-populate warm entry with the prequeue ID
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		UserID:     "user1",
		PrequeueID: entry.ID,
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if result.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", result.Skipped)
	}
	if workerCalls != 0 {
		t.Errorf("expected 0 worker calls, got %d", workerCalls)
	}
}

// readyWorkerFn returns a worker that records calls and creates a ready prequeue
// entry (with track metadata) targeting the requested episode.
func readyWorkerFn(store *playback.PrequeueStore, calls *int, lastTarget **models.EpisodeReference) playback.PrequeueWorkerFunc {
	return func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		*calls++
		if lastTarget != nil {
			*lastTarget = targetEpisode
		}
		entry, _ := store.Create(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm")
		store.Update(entry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/debrid/rd/123/456"
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
		})
		return entry.ID, nil
	}
}

func TestRunOnce_ReResolvesWhenNextEpisodeAdvanced(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{{ID: "user1", Name: "Alice"}}

	// The user's next-up episode has advanced to S2E4 (e.g. they finished S2E3
	// or a new episode aired), but the previously warmed stream still targets S2E3.
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 4},
			},
		},
	}

	// Pre-create a ready entry for the now-stale episode S2E3.
	stale, _ := store.Create("title1", "Breaking Bad", "user1", "series", 0, &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3}, "prewarm")
	store.Update(stale.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
	})

	workerCalls := 0
	var resolvedTarget *models.EpisodeReference

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(readyWorkerFn(store, &workerCalls, &resolvedTarget))

	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:       "title1",
		UserID:        "user1",
		PrequeueID:    stale.ID,
		TargetEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
		ExpiresAt:     time.Now().Add(12 * time.Hour),
	}

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if workerCalls != 1 {
		t.Fatalf("expected 1 worker call (re-resolve for advanced episode), got %d", workerCalls)
	}
	if result.Warmed != 1 {
		t.Errorf("expected 1 warmed, got %d", result.Warmed)
	}
	if resolvedTarget == nil || resolvedTarget.SeasonNumber != 2 || resolvedTarget.EpisodeNumber != 4 {
		t.Errorf("expected re-resolve to target S2E4, got %+v", resolvedTarget)
	}
}

func TestRunOnce_ReResolvesWhenAbsoluteEpisodeAdvanced(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{{ID: "user1", Name: "Alice"}}

	// One Piece scenario: absolute numbering. Next-up advanced from abs 1163 to 1164.
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "tvdb:series:81797",
				SeriesTitle: "One Piece",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 9, AbsoluteEpisodeNumber: 1164},
			},
		},
	}

	stale, _ := store.Create("tvdb:series:81797", "One Piece", "user1", "series", 1999,
		&models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 8, AbsoluteEpisodeNumber: 1163}, "prewarm")
	store.Update(stale.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "jpn", Codec: "aac"}}
	})

	workerCalls := 0
	var resolvedTarget *models.EpisodeReference

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(readyWorkerFn(store, &workerCalls, &resolvedTarget))

	svc.entries[entryKey("tvdb:series:81797", "user1")] = &WarmEntry{
		TitleID:       "tvdb:series:81797",
		UserID:        "user1",
		PrequeueID:    stale.ID,
		TargetEpisode: &models.EpisodeReference{SeasonNumber: 23, EpisodeNumber: 8, AbsoluteEpisodeNumber: 1163},
		ExpiresAt:     time.Now().Add(12 * time.Hour),
	}

	if _, err := svc.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if workerCalls != 1 {
		t.Fatalf("expected 1 worker call (re-resolve for advanced absolute episode), got %d", workerCalls)
	}
	if resolvedTarget == nil || resolvedTarget.AbsoluteEpisodeNumber != 1164 {
		t.Errorf("expected re-resolve to target abs 1164, got %+v", resolvedTarget)
	}
}

func TestRunOnce_AdoptingExistingPrequeueExtendsTTL(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
	}
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				Year:        2008,
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
			},
		},
	}

	entry, _ := store.Create("title1", "Breaking Bad", "user1", "series", 2008,
		&models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3}, "details")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/webdav/title1.mkv"
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
	})
	store.ForceExpiry(entry.ID, time.Now().Add(5*time.Minute))

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		return "", nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if workerCalls != 0 {
		t.Fatalf("expected existing prequeue adoption without worker call, got %d", workerCalls)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped adoption, got %d", result.Skipped)
	}

	updated, ok := store.Get(entry.ID)
	if !ok {
		t.Fatal("expected adopted prequeue to remain available")
	}
	if time.Until(updated.ExpiresAt) < 11*time.Hour {
		t.Fatalf("expected adopted prequeue TTL to be extended near prewarm max age, expires in %s", time.Until(updated.ExpiresAt))
	}
	warm := svc.GetWarm("title1", "user1")
	if warm == nil || warm.PrequeueID != entry.ID {
		t.Fatalf("expected warm entry to reference adopted prequeue, got %#v", warm)
	}
}

func TestInvalidatePrequeueBacksOffInsteadOfDeletingWarmEntry(t *testing.T) {
	svc := NewService(nil, "")
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		TitleName:  "Breaking Bad",
		UserID:     "user1",
		MediaType:  "series",
		PrequeueID: "pq_bad",
		StreamPath: "/webdav/bad.mkv",
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}

	svc.InvalidatePrequeue("pq_bad")

	entry := svc.entries[entryKey("title1", "user1")]
	if entry == nil {
		t.Fatal("expected invalidated warm entry to remain for retry backoff")
	}
	if entry.Error == "" {
		t.Fatal("expected invalidated warm entry to record an error")
	}
	if entry.PrequeueID != "" || entry.StreamPath != "" {
		t.Fatalf("expected invalidated warm entry to clear stream reference, got prequeue=%q path=%q", entry.PrequeueID, entry.StreamPath)
	}
	if time.Until(entry.ExpiresAt) < 55*time.Minute {
		t.Fatalf("expected invalidated warm entry to back off retries, expires in %s", time.Until(entry.ExpiresAt))
	}
	if warm := svc.GetWarm("title1", "user1"); warm != nil {
		t.Fatalf("expected invalidated warm entry to be hidden from GetWarm, got %#v", warm)
	}
}

func TestRunOnce_RemovesStaleEntries(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
	}

	// Empty continue watching
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {},
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		return "", nil
	})

	// Pre-populate a stale warm entry
	svc.entries[entryKey("old-title", "user1")] = &WarmEntry{
		TitleID:    "old-title",
		UserID:     "user1",
		PrequeueID: "pq_old",
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if result.Removed != 1 {
		t.Errorf("expected 1 removed, got %d", result.Removed)
	}

	if len(svc.entries) != 0 {
		t.Errorf("expected 0 entries after removal, got %d", len(svc.entries))
	}
}

func TestRunOnce_HandlesWorkerFailure(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
	}

	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			},
		},
	}

	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		return "pq_failed", fmt.Errorf("no results found")
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
}

func TestRunOnce_TimesOutStuckWorkerAndContinues(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	users := []models.User{{ID: "user1", Name: "Alice"}}
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Stuck Show",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			},
		},
	}

	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(func(context.Context, string, string, string, string, int, string, *models.EpisodeReference) (string, error) {
		return "", errors.New("unscoped worker should not be called")
	})
	svc.workerTimeout = 25 * time.Millisecond
	svc.SetScopedWorkerFunc(func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID, clientID, settingsScopeKey string, targetEpisode *models.EpisodeReference) (string, error) {
		entry, _ := store.CreateScoped(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm", settingsScopeKey)
		close(workerStarted)
		<-releaseWorker
		return entry.ID, nil
	})

	startedAt := time.Now()
	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("RunOnce remained blocked for %s", elapsed)
	}
	select {
	case <-workerStarted:
	default:
		t.Fatal("worker was not started")
	}
	if result.Failed != 1 || result.Warmed != 0 {
		t.Fatalf("result = %+v, want failed=1 warmed=0", result)
	}
	if _, ok := store.GetByTitleUserScope("title1", "user1", playback.DefaultPrequeueSettingsScopeKey); ok {
		t.Fatal("timed-out prequeue entry was not removed")
	}
	entry := svc.entries[entryKey("title1", "user1")]
	if entry == nil || !strings.Contains(entry.Error, "timed out") {
		t.Fatalf("warm entry error = %#v, want timeout failure", entry)
	}
	close(releaseWorker)
}

func TestRunOnce_RejectsResolvedPrequeueWithoutReusablePreparation(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{{ID: "user1", Name: "Alice"}}
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			},
		},
	}

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		entry, _ := store.Create(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm")
		store.Update(entry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/webdav/not-ready-yet.mkv"
		})
		return entry.ID, nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if workerCalls != 1 {
		t.Fatalf("worker calls = %d, want 1", workerCalls)
	}
	if result.Warmed != 0 || result.Failed != 1 {
		t.Fatalf("result = %+v, want warmed=0 failed=1", result)
	}

	warm := svc.GetWarmScoped("title1", "user1", playback.DefaultPrequeueSettingsScopeKey)
	if warm != nil {
		t.Fatalf("expected no reusable warm ref, got %#v", warm)
	}

	entry := svc.entries[entryKey("title1", "user1")]
	if entry == nil {
		t.Fatal("expected failed warm entry to be retained for retry backoff")
	}
	if entry.Error == "" {
		t.Fatal("expected failed warm entry error")
	}
	if entry.PrequeueID != "" {
		t.Fatalf("PrequeueID = %q, want empty", entry.PrequeueID)
	}
}

func TestRunOnce_NoResultsFailureNearAirDateRetriesQuickly(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	now := time.Now().UTC()
	airDate := now.Format("2006-01-02")

	users := []models.User{{ID: "user1", Name: "Alice"}}
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "tmdb:tv:37854",
				SeriesTitle: "One Piece",
				UpdatedAt:   now,
				Year:        now.Year(),
				NextEpisode: &models.EpisodeReference{
					SeasonNumber:          23,
					EpisodeNumber:         12,
					AbsoluteEpisodeNumber: 1167,
					AirDate:               airDate,
				},
			},
		},
	}

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		return "pq_failed", fmt.Errorf("prequeue failed: no results found")
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected 1 failed, got %d", result.Failed)
	}

	warm := svc.entries[entryKey("tmdb:tv:37854", "user1")]
	if warm == nil {
		t.Fatal("expected failed warm entry to be recorded")
	}
	retryIn := time.Until(warm.ExpiresAt)
	if retryIn < 10*time.Minute || retryIn > 20*time.Minute {
		t.Fatalf("expected no-results release retry around 15m, got %s", retryIn)
	}
	if workerCalls != 1 {
		t.Fatalf("expected 1 worker call, got %d", workerCalls)
	}

	result, err = svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce failed: %v", err)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected failed entry to be skipped until retry, got skipped=%d", result.Skipped)
	}
	if workerCalls != 1 {
		t.Fatalf("expected no retry before retry deadline, got %d worker calls", workerCalls)
	}

	warm.ExpiresAt = time.Now().Add(-time.Minute)
	result, err = svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("third RunOnce failed: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected retry after deadline to fail again, got failed=%d", result.Failed)
	}
	if workerCalls != 2 {
		t.Fatalf("expected retry after deadline, got %d worker calls", workerCalls)
	}
}

func TestPrewarmFailureRetryDelay_NoResultsUsesReleaseWindow(t *testing.T) {
	now := time.Date(2026, 6, 21, 8, 47, 0, 0, time.UTC)

	tests := []struct {
		name          string
		err           error
		targetEpisode *models.EpisodeReference
		year          int
		want          time.Duration
	}{
		{
			name: "date-only today retries in release cadence",
			err:  fmt.Errorf("prequeue failed: no results found"),
			targetEpisode: &models.EpisodeReference{
				SeasonNumber:          23,
				EpisodeNumber:         12,
				AbsoluteEpisodeNumber: 1167,
				AirDate:               "2026-06-21",
			},
			year: now.Year(),
			want: prewarmNoResultsReleaseRetryDelay,
		},
		{
			name: "precise upcoming airtime retries soon",
			err:  fmt.Errorf("no results found"),
			targetEpisode: &models.EpisodeReference{
				SeasonNumber:   1,
				EpisodeNumber:  1,
				AirDateTimeUTC: now.Add(3 * time.Hour).Format(time.RFC3339),
			},
			year: now.Year(),
			want: prewarmNoResultsUpcomingRetryDelay,
		},
		{
			name: "precise release window retries aggressively",
			err:  fmt.Errorf("no results found"),
			targetEpisode: &models.EpisodeReference{
				SeasonNumber:   1,
				EpisodeNumber:  1,
				AirDateTimeUTC: now.Add(-2 * time.Hour).Format(time.RFC3339),
			},
			year: now.Year(),
			want: prewarmNoResultsReleaseRetryDelay,
		},
		{
			name: "current year series without date caps no-results backoff",
			err:  fmt.Errorf("no results found"),
			targetEpisode: &models.EpisodeReference{
				SeasonNumber:  1,
				EpisodeNumber: 1,
			},
			year: now.Year(),
			want: prewarmNoResultsCurrentYearRetryDelay,
		},
		{
			name:          "cancelled worker retries promptly",
			err:           fmt.Errorf("prequeue failed: cancelled"),
			targetEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 5},
			year:          now.Year(),
			want:          prewarmTransientFailureRetryDelay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prewarmFailureRetryDelayAt(tt.err, tt.targetEpisode, tt.year, "series", now)
			if got != tt.want {
				t.Fatalf("prewarmFailureRetryDelayAt() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestTransientPrewarmFailureReadyOverridesLegacyLongBackoff(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	entry := &WarmEntry{
		Error:       "prequeue failed: cancelled",
		LastResolve: now.Add(-2 * time.Minute),
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if !transientPrewarmFailureReady(entry, now) {
		t.Fatal("legacy cancellation should retry after the transient delay despite its persisted long expiry")
	}
	entry.LastResolve = now.Add(-30 * time.Second)
	if transientPrewarmFailureReady(entry, now) {
		t.Fatal("recent cancellation should retain the short retry delay")
	}
}

func TestGetWarm_ReturnsEntry(t *testing.T) {
	svc := NewService(nil, "")
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		UserID:     "user1",
		PrequeueID: "pq_123",
		ExpiresAt:  time.Now().Add(1 * time.Hour),
	}

	warm := svc.GetWarm("title1", "user1")
	if warm == nil {
		t.Fatal("expected warm entry, got nil")
	}
	if warm.PrequeueID != "pq_123" {
		t.Errorf("expected prequeueID pq_123, got %s", warm.PrequeueID)
	}
}

func TestGetWarm_ReturnsNilForMissing(t *testing.T) {
	svc := NewService(nil, "")

	warm := svc.GetWarm("nonexistent", "user1")
	if warm != nil {
		t.Error("expected nil for missing entry")
	}
}

func TestGetWarm_ReturnsNilForExpired(t *testing.T) {
	svc := NewService(nil, "")
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		UserID:     "user1",
		PrequeueID: "pq_123",
		ExpiresAt:  time.Now().Add(-1 * time.Hour),
	}

	warm := svc.GetWarm("title1", "user1")
	if warm != nil {
		t.Error("expected nil for expired entry")
	}
}

func TestGetWarm_ReturnsNilForErrorEntry(t *testing.T) {
	svc := NewService(nil, "")
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		UserID:     "user1",
		PrequeueID: "pq_123",
		Error:      "resolution failed",
		ExpiresAt:  time.Now().Add(1 * time.Hour),
	}

	warm := svc.GetWarm("title1", "user1")
	if warm != nil {
		t.Error("expected nil for error entry")
	}
}

func TestListAll_ReturnsAllEntries(t *testing.T) {
	svc := NewService(nil, "")
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{TitleID: "title1", UserID: "user1"}
	svc.entries[entryKey("title2", "user1")] = &WarmEntry{TitleID: "title2", UserID: "user1"}

	all := svc.ListAll()
	if len(all) != 2 {
		t.Errorf("expected 2 entries, got %d", len(all))
	}
}

func TestRefreshURLs_CallsDebridRefresher(t *testing.T) {
	refresher := &mockDebridRefresher{}

	svc := NewService(nil, "")
	svc.SetDebridStreaming(refresher)

	// Entry with old refresh time
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:     "title1",
		UserID:      "user1",
		StreamPath:  "/debrid/rd/123/456",
		LastRefresh: time.Now().Add(-10 * time.Minute),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	err := svc.RefreshURLs(context.Background())
	if err != nil {
		t.Fatalf("RefreshURLs failed: %v", err)
	}

	if len(refresher.calls) != 1 {
		t.Errorf("expected 1 debrid refresh call, got %d", len(refresher.calls))
	}
	if refresher.calls[0] != "/debrid/rd/123/456" {
		t.Errorf("expected path /debrid/rd/123/456, got %s", refresher.calls[0])
	}
}

func TestRefreshURLs_SkipsExternalProxyURLs(t *testing.T) {
	refresher := &mockDebridRefresher{}

	svc := NewService(nil, "")
	svc.SetDebridStreaming(refresher)

	// Pre-resolved AIOStreams proxy URL — not a debrid path, must not be sent
	// through GetDirectURL (which would log "invalid debrid path format").
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:     "title1",
		UserID:      "user1",
		StreamPath:  "https://aiostreams.example.com/api/v1/proxy/abc123/stream.mkv",
		LastRefresh: time.Now().Add(-10 * time.Minute),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	err := svc.RefreshURLs(context.Background())
	if err != nil {
		t.Fatalf("RefreshURLs failed: %v", err)
	}

	if len(refresher.calls) != 0 {
		t.Errorf("expected 0 debrid refresh calls for external proxy URLs, got %d (%v)", len(refresher.calls), refresher.calls)
	}
}

func TestIsExternalStreamURL(t *testing.T) {
	cases := map[string]bool{
		"https://aiostreams.example.com/api/v1/proxy/x": true,
		"http://comet.example.com/playback/x":           true,
		"  https://leading.space/x  ":                   true,
		"/debrid/rd/123/456":                            false,
		"/webdav/title.mkv":                             false,
		"localmedia:abc":                                false,
		"":                                              false,
	}
	for path, want := range cases {
		if got := isExternalStreamURL(path); got != want {
			t.Errorf("isExternalStreamURL(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestRefreshURLs_SkipsRecentlyRefreshed(t *testing.T) {
	refresher := &mockDebridRefresher{}

	svc := NewService(nil, "")
	svc.SetDebridStreaming(refresher)

	// Entry refreshed just now
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:     "title1",
		UserID:      "user1",
		StreamPath:  "/debrid/rd/123/456",
		LastRefresh: time.Now(),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	err := svc.RefreshURLs(context.Background())
	if err != nil {
		t.Fatalf("RefreshURLs failed: %v", err)
	}

	if len(refresher.calls) != 0 {
		t.Errorf("expected 0 debrid refresh calls, got %d", len(refresher.calls))
	}
}

func TestRefreshURLs_SkipsEntriesWithErrors(t *testing.T) {
	refresher := &mockDebridRefresher{}

	svc := NewService(nil, "")
	svc.SetDebridStreaming(refresher)

	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:     "title1",
		UserID:      "user1",
		StreamPath:  "/debrid/rd/123/456",
		LastRefresh: time.Now().Add(-10 * time.Minute),
		Error:       "failed to resolve",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	err := svc.RefreshURLs(context.Background())
	if err != nil {
		t.Fatalf("RefreshURLs failed: %v", err)
	}

	if len(refresher.calls) != 0 {
		t.Errorf("expected 0 debrid refresh calls for error entries, got %d", len(refresher.calls))
	}
}

func TestRunOnce_MultipleUsersMultipleItems(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	users := []models.User{
		{ID: "user1", Name: "Alice"},
		{ID: "user2", Name: "Bob"},
	}

	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			},
			{
				SeriesID:    "movie1",
				SeriesTitle: "Inception",
				UpdatedAt:   time.Now().UTC(),
				Year:        2010,
			},
		},
		"user2": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				NextEpisode: &models.EpisodeReference{SeasonNumber: 3, EpisodeNumber: 5},
			},
		},
	}

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		entry, _ := store.Create(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm")
		store.Update(entry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/debrid/rd/123/456"
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
		})
		return entry.ID, nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)
	svc.jitterFn = func() time.Duration { return 0 } // no jitter in tests

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// title1:user1 (series), movie1:user1 (movie), title1:user2 (series)
	if result.Warmed != 3 {
		t.Errorf("expected 3 warmed, got %d", result.Warmed)
	}
	if workerCalls != 3 {
		t.Errorf("expected 3 worker calls, got %d", workerCalls)
	}
}

func TestPersistence_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()

	// Create service and add entries
	svc1 := NewService(nil, tmpDir)
	svc1.mu.Lock()
	svc1.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		TitleName:  "Breaking Bad",
		UserID:     "user1",
		MediaType:  "series",
		StreamPath: "/debrid/rd/123/456",
		PrequeueID: "pq_1",
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}
	svc1.entries[entryKey("movie1", "user2")] = &WarmEntry{
		TitleID:    "movie1",
		TitleName:  "Inception",
		UserID:     "user2",
		MediaType:  "movie",
		StreamPath: "/debrid/rd/789/012",
		PrequeueID: "pq_2",
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}
	err := svc1.saveLocked()
	svc1.mu.Unlock()
	if err != nil {
		t.Fatalf("saveLocked failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, "prewarm.json")); os.IsNotExist(err) {
		t.Fatal("prewarm.json was not created")
	}

	// Create new service from same dir — should load persisted data
	svc2 := NewService(nil, tmpDir)
	if len(svc2.entries) != 2 {
		t.Fatalf("expected 2 entries after load, got %d", len(svc2.entries))
	}

	entry := svc2.entries[entryKey("title1", "user1")]
	if entry == nil {
		t.Fatal("expected title1:user1 entry")
	}
	if entry.TitleName != "Breaking Bad" {
		t.Errorf("expected title Breaking Bad, got %s", entry.TitleName)
	}
	if entry.StreamPath != "/debrid/rd/123/456" {
		t.Errorf("expected stream path /debrid/rd/123/456, got %s", entry.StreamPath)
	}
}

func TestPersistence_RestorePrequeueEntries(t *testing.T) {
	tmpDir := t.TempDir()
	store := playback.NewPrequeueStore(30 * time.Minute)

	// Create service with persisted entries
	svc := NewService(nil, tmpDir)
	svc.SetPrequeueStore(store)
	svc.mu.Lock()
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		TitleName:  "Breaking Bad",
		UserID:     "user1",
		MediaType:  "series",
		StreamPath: "/debrid/rd/123/456",
		PrequeueID: "pq_old", // This will be replaced
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}
	_ = svc.saveLocked()
	svc.mu.Unlock()

	// Simulate restart: create new service from same dir
	svc2 := NewService(nil, tmpDir)
	svc2.SetPrequeueStore(store)
	svc2.RestorePrequeueEntries()

	// Should keep the warm entry but defer creating a ready prequeue entry.
	// A fresh warm run will repopulate full track metadata.
	entry := svc2.entries[entryKey("title1", "user1")]
	if entry == nil {
		t.Fatal("expected entry after restore")
	}
	if entry.PrequeueID != "" {
		t.Errorf("expected empty prequeueID after restore defer, got %q", entry.PrequeueID)
	}
	if _, ok := store.GetByTitleUser("title1", "user1"); ok {
		t.Fatal("expected no restored ready prequeue entry in store")
	}
}

func TestReResolveExpired(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	// Create an entry and make it ready
	entry, _ := store.Create("title1", "Breaking Bad", "user1", "series", 2008,
		&models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3}, "prewarm")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/debrid/rd/123/456"
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
	})
	// Force expiry bypassing TTL auto-extension
	store.ForceExpiry(entry.ID, time.Now().Add(-1*time.Minute))

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		newEntry, _ := store.Create(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm")
		store.Update(newEntry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/debrid/rd/789/012"
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
		})
		return newEntry.ID, nil
	}

	svc := NewService(nil, "")
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	// Add a warm entry that references the expired prequeue
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		UserID:     "user1",
		PrequeueID: entry.ID,
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}

	reResolved := svc.reResolveExpired(context.Background())

	if reResolved != 1 {
		t.Errorf("expected 1 re-resolved, got %d", reResolved)
	}
	if workerCalls != 1 {
		t.Errorf("expected 1 worker call, got %d", workerCalls)
	}

	// Warm entry should have been updated with the new prequeue ID
	warmEntry := svc.entries[entryKey("title1", "user1")]
	if warmEntry.PrequeueID == entry.ID {
		t.Error("expected warm entry to be updated with new prequeue ID")
	}
}

func TestGetStableReResolveIntervalFromTaskConfig(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		ScheduledTasks: config.ScheduledTasksSettings{
			Tasks: []config.ScheduledTask{
				{
					Type:   config.ScheduledTaskTypePrewarm,
					Config: map[string]string{"stableReresolveDays": "30"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, "")
	if got := svc.getStableReResolveInterval(); got != 30*24*time.Hour {
		t.Fatalf("getStableReResolveInterval() = %s, want 720h", got)
	}
}

func TestGetStableReResolveIntervalClampsTaskConfig(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want time.Duration
	}{
		{name: "too low", val: "0", want: 24 * time.Hour},
		{name: "too high", val: "31", want: 30 * 24 * time.Hour},
		{name: "invalid uses default", val: "not-a-number", want: 7 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			if err := mgr.Save(config.Settings{
				ScheduledTasks: config.ScheduledTasksSettings{
					Tasks: []config.ScheduledTask{
						{
							Type:   config.ScheduledTaskTypePrewarm,
							Config: map[string]string{"stableReresolveDays": tt.val},
						},
					},
				},
			}); err != nil {
				t.Fatalf("save settings: %v", err)
			}

			svc := NewService(mgr, "")
			if got := svc.getStableReResolveInterval(); got != tt.want {
				t.Fatalf("getStableReResolveInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestAdoptEntry(t *testing.T) {
	svc := NewService(nil, "")

	svc.AdoptEntry("pq_123")

	svc.adhocMu.RLock()
	_, exists := svc.adhocEntries["pq_123"]
	svc.adhocMu.RUnlock()

	if !exists {
		t.Error("expected adopted entry to be tracked")
	}
}

func TestGetWarmScopedFallsBackToSharedScopeAcrossUsers(t *testing.T) {
	svc := NewService(nil, "")
	svc.entries[entryKey("title1", "user1", "global")] = &WarmEntry{
		TitleID:          "title1",
		TitleName:        "Hoppers",
		UserID:           "user1",
		SettingsScopeKey: "global",
		PrequeueID:       "pq_shared",
		LastRefresh:      time.Now(),
		ExpiresAt:        time.Now().Add(time.Hour),
	}

	warm := svc.GetWarmScoped("title1", "user2", "global")
	if warm == nil {
		t.Fatal("expected shared-scope warm entry")
	}
	if warm.PrequeueID != "pq_shared" {
		t.Fatalf("PrequeueID = %q, want pq_shared", warm.PrequeueID)
	}
}

func TestUpdateFromPrequeueRegistersAdoptedAdhocEntry(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	svc := NewService(nil, "")
	svc.SetPrequeueStore(store)

	entry, _ := store.CreateScoped("title1", "Hoppers", "user1", "movie", 2026, nil, "details", "global")
	svc.AdoptEntry(entry.ID)
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/webdav/Hoppers.mkv"
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 1, Codec: "eac3", Language: "eng"}}
	})

	svc.UpdateFromPrequeue(entry.ID)

	warmEntry := svc.entries[entryKey("title1", "user1", "global")]
	if warmEntry == nil {
		t.Fatal("expected adopted ready prequeue to create a warm entry")
	}
	if warmEntry.PrequeueID != entry.ID {
		t.Fatalf("PrequeueID = %q, want %q", warmEntry.PrequeueID, entry.ID)
	}
	if warmEntry.StreamPath != "/webdav/Hoppers.mkv" {
		t.Fatalf("StreamPath = %q, want /webdav/Hoppers.mkv", warmEntry.StreamPath)
	}

	warm := svc.GetWarmScoped("title1", "user2", "global")
	if warm == nil || warm.PrequeueID != entry.ID {
		t.Fatalf("expected adopted warm entry to be reusable by shared scope, got %#v", warm)
	}
}

func TestUpdateFromPrequeueRefreshesExistingWarmEntry(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	svc := NewService(nil, "")
	svc.SetPrequeueStore(store)

	key := entryKey("title1", "user1")
	svc.entries[key] = &WarmEntry{
		TitleID:    "title1",
		TitleName:  "Old Title",
		UserID:     "user1",
		MediaType:  "series",
		PrequeueID: "pq_old",
		StreamPath: "/old/path.mkv",
		ExpiresAt:  time.Now().Add(12 * time.Hour),
	}

	entry, _ := store.Create(
		"title1",
		"New Title",
		"user1",
		"series",
		2026,
		&models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 4},
		"next_episode",
	)
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = "/new/path.mkv"
		e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
	})

	svc.UpdateFromPrequeue(entry.ID)

	warmEntry := svc.entries[key]
	if warmEntry.PrequeueID != entry.ID {
		t.Fatalf("PrequeueID = %q, want %q", warmEntry.PrequeueID, entry.ID)
	}
	if warmEntry.StreamPath != "/new/path.mkv" {
		t.Fatalf("StreamPath = %q, want /new/path.mkv", warmEntry.StreamPath)
	}
	if warmEntry.TitleName != "New Title" {
		t.Fatalf("TitleName = %q, want New Title", warmEntry.TitleName)
	}
	if warmEntry.Year != 2026 {
		t.Fatalf("Year = %d, want 2026", warmEntry.Year)
	}
	if warmEntry.TargetEpisode == nil || warmEntry.TargetEpisode.SeasonNumber != 2 || warmEntry.TargetEpisode.EpisodeNumber != 4 {
		t.Fatalf("TargetEpisode = %#v, want S02E04", warmEntry.TargetEpisode)
	}
}

func TestPruneAdhocEntries_PrunesOldEntries(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	svc := NewService(nil, "")
	svc.SetPrequeueStore(store)

	// Create an ad-hoc entry that's older than 24h
	svc.adhocMu.Lock()
	svc.adhocEntries["pq_old"] = time.Now().Add(-25 * time.Hour)
	svc.adhocMu.Unlock()

	// No active continue-watching keys
	activeKeys := make(map[string]bool)

	pruned := svc.pruneAdhocEntries(activeKeys)
	if pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", pruned)
	}

	svc.adhocMu.RLock()
	_, exists := svc.adhocEntries["pq_old"]
	svc.adhocMu.RUnlock()

	if exists {
		t.Error("expected old ad-hoc entry to be pruned")
	}
}

func TestPruneAdhocEntries_KeepsContinueWatchingEntries(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	// Create the prequeue entry in the store
	entry, _ := store.Create("title1", "Breaking Bad", "user1", "series", 2008, nil, "details")

	svc := NewService(nil, "")
	svc.SetPrequeueStore(store)

	// Adopt it, but set adoption time > 24h ago
	svc.adhocMu.Lock()
	svc.adhocEntries[entry.ID] = time.Now().Add(-25 * time.Hour)
	svc.adhocMu.Unlock()

	// This title+user IS in continue watching
	activeKeys := map[string]bool{
		entryKey("title1", "user1"): true,
	}

	pruned := svc.pruneAdhocEntries(activeKeys)
	if pruned != 0 {
		t.Errorf("expected 0 pruned (in continue watching), got %d", pruned)
	}

	svc.adhocMu.RLock()
	_, exists := svc.adhocEntries[entry.ID]
	svc.adhocMu.RUnlock()

	if !exists {
		t.Error("expected ad-hoc entry to be kept (in continue watching)")
	}
}

func TestPruneAdhocEntries_KeepsRecentEntries(t *testing.T) {
	svc := NewService(nil, "")
	svc.SetPrequeueStore(playback.NewPrequeueStore(30 * time.Minute))

	// Ad-hoc entry adopted 1 hour ago (< 24h)
	svc.adhocMu.Lock()
	svc.adhocEntries["pq_recent"] = time.Now().Add(-1 * time.Hour)
	svc.adhocMu.Unlock()

	pruned := svc.pruneAdhocEntries(make(map[string]bool))
	if pruned != 0 {
		t.Errorf("expected 0 pruned (recent entry), got %d", pruned)
	}
}

func TestPersistence_ExpiredEntriesRemovedOnRestore(t *testing.T) {
	tmpDir := t.TempDir()
	store := playback.NewPrequeueStore(30 * time.Minute)

	// Create service with an expired entry
	svc := NewService(nil, tmpDir)
	svc.mu.Lock()
	svc.entries[entryKey("title1", "user1")] = &WarmEntry{
		TitleID:    "title1",
		TitleName:  "Breaking Bad",
		UserID:     "user1",
		StreamPath: "/debrid/rd/123/456",
		PrequeueID: "pq_old",
		ExpiresAt:  time.Now().Add(-1 * time.Hour), // Expired
	}
	_ = svc.saveLocked()
	svc.mu.Unlock()

	// Simulate restart
	svc2 := NewService(nil, tmpDir)
	svc2.SetPrequeueStore(store)
	svc2.RestorePrequeueEntries()

	if len(svc2.entries) != 0 {
		t.Errorf("expected 0 entries after restoring expired data, got %d", len(svc2.entries))
	}
}

func TestRunOnce_DeduplicatesSameTitleUser(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)

	// Two profiles with the same user ID (simulates the duplicate profile bug)
	users := []models.User{
		{ID: "user1", Name: "Alice"},
		{ID: "user1", Name: "Alice-copy"},
	}

	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
				UpdatedAt:   time.Now().UTC(),
				Year:        2008,
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
			},
		},
	}

	workerCalls := 0
	workerFn := func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
		workerCalls++
		entry, _ := store.Create(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm")
		store.Update(entry.ID, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/debrid/rd/123/456"
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 0, Language: "eng", Codec: "aac"}}
		})
		return entry.ID, nil
	}

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.SetWorkerFunc(workerFn)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Should warm once and skip the duplicate
	if workerCalls != 1 {
		t.Errorf("expected 1 worker call (deduped), got %d", workerCalls)
	}
	if result.Warmed != 1 {
		t.Errorf("expected 1 warmed, got %d", result.Warmed)
	}
	if result.Skipped != 1 {
		t.Errorf("expected 1 skipped (deduped), got %d", result.Skipped)
	}
}

func TestRunOnce_LeavesInFlightClientPrequeueAlone(t *testing.T) {
	store := playback.NewPrequeueStore(30 * time.Minute)
	episode := &models.EpisodeReference{SeasonNumber: 4, EpisodeNumber: 1}
	users := []models.User{{ID: "user1", Name: "Alice"}}
	continueWatching := map[string][]models.SeriesWatchState{
		"user1": {{
			SeriesID:    "title1",
			SeriesTitle: "The Gilded Age",
			UpdatedAt:   time.Now().UTC(),
			NextEpisode: episode,
		}},
	}

	// The user opened the details page: a client prequeue is mid-search.
	details, _ := store.CreateScoped("title1", "The Gilded Age", "user1", "series", 0, episode, "details", playback.DefaultPrequeueSettingsScopeKey)
	store.Update(details.ID, func(e *playback.PrequeueEntry) { e.Status = playback.PrequeueStatusSearching })

	svc := NewService(nil, "")
	svc.SetHistoryService(&mockHistoryProvider{continueWatching: continueWatching})
	svc.SetUsersService(&mockUsersProvider{users: users})
	svc.SetPrequeueStore(store)
	svc.jitterFn = func() time.Duration { return 0 }
	// Mirrors PrequeueHandler.RunWorkerSyncScoped's create step.
	svc.SetScopedWorkerFunc(func(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID, clientID, settingsScopeKey string, targetEpisode *models.EpisodeReference) (string, error) {
		entry, err := store.CreateScopedUnlessInProgress(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm", settingsScopeKey)
		if err != nil {
			return entry.ID, fmt.Errorf("%w: %s", err, entry.ID)
		}
		t.Fatal("prewarm created a replacement entry for an in-flight prequeue")
		return entry.ID, nil
	})

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if result.Failed != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want skipped=1 failed=0", result)
	}
	if _, ok := store.Get(details.ID); !ok {
		t.Fatal("details prequeue was replaced while the client was polling it")
	}
	if entry := svc.entries[entryKey("title1", "user1")]; entry != nil && entry.Error != "" {
		t.Fatalf("recorded failure %q; next cycle would skip instead of adopting", entry.Error)
	}
}

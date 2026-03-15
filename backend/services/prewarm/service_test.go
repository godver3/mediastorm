package prewarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
				NextEpisode: &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3},
			},
		},
	}

	// Pre-create a ready entry
	entry, _ := store.Create("title1", "Breaking Bad", "user1", "series", 0, &models.EpisodeReference{SeasonNumber: 2, EpisodeNumber: 3}, "prewarm")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
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
				NextEpisode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
			},
			{
				SeriesID:    "movie1",
				SeriesTitle: "Inception",
				Year:        2010,
			},
		},
		"user2": {
			{
				SeriesID:    "title1",
				SeriesTitle: "Breaking Bad",
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

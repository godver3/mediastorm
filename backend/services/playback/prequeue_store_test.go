package playback

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

type recordingPrequeueRepository struct {
	mu         sync.Mutex
	upsertIDs  []string
	deletedIDs []string
	listBlobs  [][]byte
	listFunc   func(context.Context) ([][]byte, error)
	listCalls  int
}

func (r *recordingPrequeueRepository) Get(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *recordingPrequeueRepository) GetByTitleUser(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (r *recordingPrequeueRepository) List(ctx context.Context) ([][]byte, error) {
	r.mu.Lock()
	r.listCalls++
	listFunc := r.listFunc
	blobs := append([][]byte(nil), r.listBlobs...)
	r.mu.Unlock()
	if listFunc != nil {
		return listFunc(ctx)
	}
	return blobs, nil
}

func (r *recordingPrequeueRepository) Upsert(_ context.Context, id, _, _, _ string, _ []byte, _ interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upsertIDs = append(r.upsertIDs, id)
	return nil
}

func (r *recordingPrequeueRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedIDs = append(r.deletedIDs, id)
	return nil
}

func (r *recordingPrequeueRepository) DeleteExpired(context.Context) (int64, error) {
	return 0, nil
}

func (r *recordingPrequeueRepository) Count(context.Context) (int64, error) {
	return 0, nil
}

func (r *recordingPrequeueRepository) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.upsertIDs...)
}

func (r *recordingPrequeueRepository) ListCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func (r *recordingPrequeueRepository) DeletedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.deletedIDs...)
}

func TestPrequeueStoreConfiguresPersistenceWithoutBlockingRestore(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	repo := &recordingPrequeueRepository{}
	store.repo = repo

	store.SetStoragePath(t.TempDir())

	if calls := repo.ListCalls(); calls != 0 {
		t.Fatalf("SetStoragePath performed %d restore queries; want 0", calls)
	}
}

func TestPrequeueStoreRestorePreservesNewerRuntimeEntry(t *testing.T) {
	now := time.Now()
	persisted := &PrequeueEntry{
		ID:                    "persisted-old",
		TitleID:               "movie:1",
		TitleName:             "Persisted",
		UserID:                "user1",
		SettingsScopeKey:      DefaultPrequeueSettingsScopeKey,
		MediaType:             "movie",
		Reason:                "details",
		Status:                PrequeueStatusReady,
		StreamPath:            "/debrid/torbox/old/file.mkv",
		SelectedAudioTrack:    -1,
		SelectedSubtitleTrack: -1,
		CreatedAt:             now.Add(-time.Hour),
		ExpiresAt:             now.Add(time.Hour),
	}
	blob, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}

	store := NewPrequeueStore(time.Hour)
	repo := &recordingPrequeueRepository{listBlobs: [][]byte{blob}}
	store.repo = repo
	store.SetStoragePath(t.TempDir())
	runtimeEntry, created := store.Create("movie:1", "Runtime", "user1", "movie", 2026, nil, "details")
	if !created {
		t.Fatal("runtime entry was not created")
	}

	if err := store.RestorePersisted(context.Background()); err != nil {
		t.Fatalf("RestorePersisted: %v", err)
	}

	got, ok := store.Get(runtimeEntry.ID)
	if !ok || got.TitleName != "Runtime" {
		t.Fatalf("newer runtime entry was replaced: %#v", got)
	}
	if _, ok := store.Get(persisted.ID); ok {
		t.Fatal("older persisted entry was restored over runtime state")
	}
	deleted := repo.DeletedIDs()
	if len(deleted) != 1 || deleted[0] != persisted.ID {
		t.Fatalf("deleted IDs = %v, want [%s]", deleted, persisted.ID)
	}
}

func TestPrequeueStoreRestoreHonorsContextDeadline(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	repo := &recordingPrequeueRepository{listFunc: func(ctx context.Context) ([][]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	store.repo = repo
	store.SetStoragePath(t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := store.RestorePersisted(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RestorePersisted error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RestorePersisted ignored deadline; elapsed=%s", elapsed)
	}
}

func TestPrequeueStoreReadyUpdatePersistsOnlyChangedEntry(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	repo := &recordingPrequeueRepository{}
	store.repo = repo

	first, created := store.Create("movie:1", "First", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("first Create returned created=false")
	}
	second, created := store.Create("movie:2", "Second", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("second Create returned created=false")
	}

	store.Update(first.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/debrid/torbox/1/file/0/first.mkv"
	})
	store.Update(second.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/debrid/torbox/2/file/0/second.mkv"
	})
	store.UpdateWorker(first.ID, func(e *PrequeueEntry) {
		e.SelectedAudioTrack = 1
	})

	got := repo.IDs()
	want := []string{first.ID, second.ID, first.ID}
	if len(got) != len(want) {
		t.Fatalf("upsert IDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("upsert IDs = %v, want %v", got, want)
		}
	}
}

func TestManualPrequeueRemainsUntilExplicitlyDeleted(t *testing.T) {
	store := NewPrequeueStore(time.Millisecond)
	entry, _ := store.Create("movie:manual", "Pinned", "user1", "movie", 2024, nil, ManualPrequeueReason)

	if !entry.Persistent || entry.Reason != ManualPrequeueReason {
		t.Fatalf("manual entry was not marked persistent: %#v", entry)
	}
	store.ForceExpiry(entry.ID, time.Now().Add(-time.Hour))
	if !store.MakePersistent(entry.ID) {
		t.Fatal("MakePersistent returned false")
	}
	if _, ok := store.Get(entry.ID); !ok {
		t.Fatal("manual entry expired")
	}

	store.Delete(entry.ID)
	if _, ok := store.Get(entry.ID); ok {
		t.Fatal("manual entry remained after explicit delete")
	}
}

func TestMakePersistentAdoptsExistingDetailsPrequeue(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, _ := store.Create("movie:1", "Existing", "user1", "movie", 2024, nil, "details")

	if !store.MakePersistent(entry.ID) {
		t.Fatal("MakePersistent returned false")
	}
	got, ok := store.Get(entry.ID)
	if !ok || !got.Persistent || got.Reason != ManualPrequeueReason || got.ExpiresAt.Year() != 9999 {
		t.Fatalf("existing entry was not pinned: %#v", got)
	}
}

func TestPersistentPrequeueCarriesForwardWhenEpisodeChanges(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	first, _ := store.Create(
		"series:1",
		"Series",
		"user1",
		"series",
		2024,
		&models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1},
		ManualPrequeueReason,
	)
	second, _ := store.Create(
		"series:1",
		"Series",
		"user1",
		"series",
		2024,
		&models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2},
		"details",
	)

	if first.ID == second.ID {
		t.Fatal("expected the episode replacement to create a new entry")
	}
	if !second.Persistent || second.Reason != ManualPrequeueReason || second.ExpiresAt.Year() != 9999 {
		t.Fatalf("replacement did not carry the manual pin forward: %#v", second)
	}
}

func TestPrequeueStoreValidatesReadyEntryOnLookup(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("tvdb:series:353546", "Bluey", "default", "series", 2018, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/webdav/stale/title.mkv"
	})
	store.streamPathValidated = make(map[string]time.Time)

	var calls int32
	store.SetStreamPathValidator(func(ctx context.Context, streamPath string) error {
		atomic.AddInt32(&calls, 1)
		if streamPath != "/webdav/stale/title.mkv" {
			t.Fatalf("streamPath = %q, want /webdav/stale/title.mkv", streamPath)
		}
		return errors.New("stream not found")
	})

	if got, ok := store.GetByTitleUser("tvdb:series:353546", "default"); ok || got != nil {
		t.Fatalf("GetByTitleUser returned (%v, %t), want nil false", got, ok)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("validator calls = %d, want 1", calls)
	}
	if got, ok := store.Get(entry.ID); ok || got != nil {
		t.Fatalf("Get after validation failure returned (%v, %t), want nil false", got, ok)
	}
}

func TestPrequeueStoreKeepsValidReadyEntryOnLookup(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/webdav/valid/title.mkv"
	})
	store.streamPathValidated = make(map[string]time.Time)

	var calls int32
	store.SetStreamPathValidator(func(ctx context.Context, streamPath string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	if got, ok := store.GetByTitleUser("movie:1", "default"); !ok || got == nil || got.ID != entry.ID {
		t.Fatalf("GetByTitleUser returned (%v, %t), want entry %s", got, ok, entry.ID)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("validator calls = %d, want 1", calls)
	}
	if got, ok := store.GetByTitleUser("movie:1", "default"); !ok || got == nil || got.ID != entry.ID {
		t.Fatalf("second GetByTitleUser returned (%v, %t), want entry %s", got, ok, entry.ID)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("validator calls after cached lookup = %d, want 1", calls)
	}
}

func TestPrequeueStoreDoesNotValidateNonReadyEntry(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}

	store.SetStreamPathValidator(func(ctx context.Context, streamPath string) error {
		t.Fatal("validator should not be called for non-ready entries")
		return nil
	})

	if got, ok := store.Get(entry.ID); !ok || got == nil {
		t.Fatalf("Get returned (%v, %t), want queued entry", got, ok)
	}
}

func TestPrequeueStoreWorkerCannotOverwriteAdoptedEntry(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, created := store.Create("movie:1", "Example", "default", "movie", 2024, nil, "details")
	if !created {
		t.Fatal("Create returned created=false")
	}
	store.Update(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/debrid/manual-selection.mkv"
		e.MigrationAdopted = true
	})

	if updated := store.UpdateWorker(entry.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusFailed
		e.StreamPath = "/downloads/stale-worker.mkv"
	}); updated {
		t.Fatal("UpdateWorker returned true for an adopted entry")
	}

	got, ok := store.Get(entry.ID)
	if !ok || got.StreamPath != "/debrid/manual-selection.mkv" || got.Status != PrequeueStatusReady {
		t.Fatalf("adopted entry was overwritten: %#v", got)
	}
}

func TestPrequeueEntryToResponseIncludesServiceType(t *testing.T) {
	entry := &PrequeueEntry{
		ID:          "pq_test",
		Status:      PrequeueStatusReady,
		StreamPath:  "/debrid/realdebrid/file.mkv",
		ServiceType: "debrid",
	}

	resp := entry.ToResponse()
	if resp.ServiceType != "debrid" {
		t.Fatalf("ServiceType = %q, want debrid", resp.ServiceType)
	}
}

func TestPrequeueEntryToResponseIncludesDebridProvider(t *testing.T) {
	entry := &PrequeueEntry{
		ID:          "pq_test",
		Status:      PrequeueStatusReady,
		StreamPath:  "/debrid/torbox/torrent/file.mkv",
		ServiceType: "debrid",
	}

	resp := entry.ToResponse()
	if resp.DebridProvider != "torbox" {
		t.Fatalf("DebridProvider = %q, want torbox", resp.DebridProvider)
	}
}

func TestPrequeueEntryToResponseIncludesMigrationCandidates(t *testing.T) {
	entry := &PrequeueEntry{
		ID:                  "pq_test",
		Status:              PrequeueStatusReady,
		StreamPath:          "/downloads/usenet/file.mkv",
		SelectedResultIndex: 1,
		SelectedResult: &models.NZBResult{
			Title:   "Selected Release",
			Indexer: "indexer-b",
			GUID:    "guid-b",
		},
		MigrationCandidates: []models.NZBResult{
			{Title: "First Release", Indexer: "indexer-a", GUID: "guid-a"},
			{Title: "Selected Release", Indexer: "indexer-b", GUID: "guid-b"},
			{Title: "Next Release", Indexer: "indexer-c", GUID: "guid-c"},
		},
	}

	resp := entry.ToResponse()
	if resp.SelectedResult == nil || resp.SelectedResult.GUID != "guid-b" {
		t.Fatalf("SelectedResult = %#v, want guid-b", resp.SelectedResult)
	}
	if resp.SelectedResultIndex != 1 {
		t.Fatalf("SelectedResultIndex = %d, want 1", resp.SelectedResultIndex)
	}
	if len(resp.MigrationCandidates) != 3 {
		t.Fatalf("MigrationCandidates length = %d, want 3", len(resp.MigrationCandidates))
	}
}

// TestPrequeueProgressWindowFieldsAreAdditiveAndOmitEmpty pins the API
// contract for the in-flight candidate window: the fields must ride along in
// ToResponse and be omitted from JSON when unset (0), so older OTA'd frontends
// keep parsing the response unchanged and newer frontends can update safely
// against older (admin-deployed) backends.
func TestPrequeueProgressWindowFieldsAreAdditiveAndOmitEmpty(t *testing.T) {
	// Set: both fields flow through ToResponse and into JSON.
	entry := &PrequeueEntry{
		ID:                 "pq_contract",
		Status:             PrequeueStatusResolving,
		ProgressStage:      "resolving_candidate",
		ProgressCurrentMin: 1,
		ProgressCurrentMax: 4,
		ProgressTotal:      21,
	}
	resp := entry.ToResponse()
	if resp.ProgressCurrentMin != 1 || resp.ProgressCurrentMax != 4 {
		t.Fatalf("ToResponse window = %d..%d, want 1..4", resp.ProgressCurrentMin, resp.ProgressCurrentMax)
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(data)
	if !strings.Contains(raw, "\"progressCurrentMin\":1") || !strings.Contains(raw, "\"progressCurrentMax\":4") {
		t.Fatalf("JSON missing in-flight window fields: %s", raw)
	}

	// Unset: older frontend parsers must see the exact same shape they always
	// did — new fields absent, not zero-valued.
	empty := (&PrequeueEntry{ID: "pq_contract", Status: PrequeueStatusResolving}).ToResponse()
	data, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	raw = string(data)
	if strings.Contains(raw, "progressCurrentMin") || strings.Contains(raw, "progressCurrentMax") {
		t.Fatalf("JSON must omit unset window fields: %s", raw)
	}
}

func TestPrequeueEntryToResponseInfersServiceType(t *testing.T) {
	tests := []struct {
		name       string
		streamPath string
		want       string
	}{
		{name: "debrid path", streamPath: "/debrid/realdebrid/file.mkv", want: "debrid"},
		{name: "usenet path", streamPath: "/downloads/usenet/file.mkv", want: "usenet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &PrequeueEntry{
				ID:         "pq_test",
				Status:     PrequeueStatusReady,
				StreamPath: tt.streamPath,
			}

			resp := entry.ToResponse()
			if resp.ServiceType != tt.want {
				t.Fatalf("ServiceType = %q, want %s", resp.ServiceType, tt.want)
			}
		})
	}
}

func TestPrequeueEntryToResponseIncludesProgress(t *testing.T) {
	entry := &PrequeueEntry{
		ID:              "pq_test",
		Status:          PrequeueStatusResolving,
		ProgressStage:   "resolving_candidate",
		ProgressDetail:  "Example.Release.2160p",
		ProgressCurrent: 2,
		ProgressTotal:   7,
	}

	resp := entry.ToResponse()
	if resp.ProgressStage != entry.ProgressStage ||
		resp.ProgressDetail != entry.ProgressDetail ||
		resp.ProgressCurrent != entry.ProgressCurrent ||
		resp.ProgressTotal != entry.ProgressTotal {
		t.Fatalf("progress response = %#v, want progress fields from entry", resp)
	}
}

func TestCreateScopedUnlessInProgress_KeepsResolvingEntry(t *testing.T) {
	store := NewPrequeueStore(30 * time.Minute)
	episode := &models.EpisodeReference{SeasonNumber: 4, EpisodeNumber: 1}
	details, _ := store.CreateScoped("tmdb:tv:1", "Show", "user1", "series", 2022, episode, "details", "scope-a")
	store.Update(details.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusSearching })

	got, err := store.CreateScopedUnlessInProgress("tmdb:tv:1", "Show", "user1", "series", 2022, episode, "prewarm", "scope-a")
	if !errors.Is(err, ErrPrequeueInProgress) {
		t.Fatalf("err = %v, want ErrPrequeueInProgress", err)
	}
	if got == nil || got.ID != details.ID {
		t.Fatalf("returned entry = %#v, want in-flight %s", got, details.ID)
	}
	if _, ok := store.Get(details.ID); !ok {
		t.Fatal("in-flight details prequeue was removed")
	}

	// A different scope is a different key and still gets its own entry.
	other, err := store.CreateScopedUnlessInProgress("tmdb:tv:1", "Show", "user1", "series", 2022, episode, "prewarm", "scope-b")
	if err != nil || other.ID == details.ID {
		t.Fatalf("other scope: entry=%#v err=%v, want a new entry", other, err)
	}
}

func TestCreateScopedUnlessInProgress_ReplacesTerminalEntry(t *testing.T) {
	store := NewPrequeueStore(30 * time.Minute)
	failed, _ := store.CreateScoped("tmdb:tv:1", "Show", "user1", "series", 2022, nil, "details", "")
	store.Update(failed.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusFailed })

	fresh, err := store.CreateScopedUnlessInProgress("tmdb:tv:1", "Show", "user1", "series", 2022, nil, "prewarm", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh.ID == failed.ID {
		t.Fatal("failed entry was not replaced")
	}
	if _, ok := store.Get(failed.ID); ok {
		t.Fatal("replaced failed entry still present")
	}
}

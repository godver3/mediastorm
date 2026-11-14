package playback

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestReadyMetadataMustNotRenewValidation(t *testing.T) {
	for _, worker := range []bool{false, true} {
		store := NewPrequeueStore(time.Hour)
		entry, _ := store.Create("title", "Fixture", "profile", "movie", 2026, nil, "details")
		update := store.Update
		if worker {
			update = store.UpdateWorker
		}
		update(entry.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusReady; e.StreamPath = "/webdav/expired/file.mkv" })
		store.streamPathValidated[streamPathValidationKey(entry.ID, "/webdav/expired/file.mkv")] = time.Now().Add(-6 * time.Minute)
		store.SetStreamPathValidator(func(context.Context, string) error { return errors.New("source registration expired") })
		update(entry.ID, func(e *PrequeueEntry) { e.TitleName = "Updated title" })
		if _, ok := store.Get(entry.ID); ok {
			t.Fatalf("worker=%v: metadata update renewed a stale source's validation", worker)
		}
	}
}

func TestPlaybackReuseRechecksRegistrationWithoutSlowingStatusPolling(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, _ := store.Create("title", "Fixture", "profile", "movie", 2026, nil, "details")
	store.Update(entry.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusReady; e.StreamPath = "/webdav/temporary/file.mkv" })
	ageValidation(store, entry.ID, "/webdav/temporary/file.mkv", time.Minute)
	calls := 0
	registered := true
	store.SetStreamPathValidator(func(context.Context, string) error {
		calls++
		if !registered {
			return errors.New("registration expired")
		}
		return nil
	})
	if _, ok := store.GetForPlayback(entry.ID); !ok {
		t.Fatal("healthy media was rejected")
	}
	if calls != 1 {
		t.Fatal("playback reuse did not verify the registration")
	}
	registered = false
	for i := 0; i < 3; i++ {
		if _, ok := store.Get(entry.ID); !ok {
			t.Fatal("ordinary status polling unexpectedly re-probed media")
		}
	}
	if calls != 1 {
		t.Fatal("ordinary polling must retain its short cache")
	}
	// One minute later: still inside the status-polling cache, but past the
	// short window that covers reuse for playback.
	ageValidation(store, entry.ID, "/webdav/temporary/file.mkv", time.Minute)
	if _, ok := store.GetByTitleUserScopeForPlayback("title", "profile", DefaultPrequeueSettingsScopeKey); ok {
		t.Fatal("dead source reused within the validation TTL")
	}
	if calls != 2 {
		t.Fatal("a new playback must recheck the registration")
	}
	if _, ok := store.Get(entry.ID); ok {
		t.Fatal("dead ready entry was not removed")
	}
}

func TestFailedReadyProbeKeepsNewerManualSelection(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, _ := store.Create("title", "Fixture", "profile", "movie", 2026, nil, "details")
	store.Update(entry.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusReady; e.StreamPath = "/webdav/old.mkv" })
	ageValidation(store, entry.ID, "/webdav/old.mkv", time.Minute)
	store.SetStreamPathValidator(func(context.Context, string) error {
		store.Update(entry.ID, func(e *PrequeueEntry) { e.StreamPath = "/webdav/replacement.mkv"; e.MigrationAdopted = true })
		return errors.New("old registration expired")
	})
	if _, ok := store.GetForPlayback(entry.ID); ok {
		t.Fatal("failed old probe was reused")
	}
	got, ok := store.Get(entry.ID)
	if !ok || got.StreamPath != "/webdav/replacement.mkv" {
		t.Fatal("failed old probe deleted the replacement")
	}
}

// Opening a details page and then pressing Play both reuse the entry through
// POST /prequeue; the second reuse right after a successful probe must not
// probe again.
func TestPlaybackReuseRightAfterProbeDoesNotProbeAgain(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, _ := store.Create("title", "Fixture", "profile", "movie", 2026, nil, "details")
	store.Update(entry.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusReady; e.StreamPath = "/webdav/file.mkv" })
	store.mu.Lock()
	delete(store.streamPathValidated, streamPathValidationKey(entry.ID, "/webdav/file.mkv"))
	store.mu.Unlock()
	calls := 0
	store.SetStreamPathValidator(func(context.Context, string) error { calls++; return nil })
	for i := 0; i < 3; i++ {
		if _, ok := store.GetForPlayback(entry.ID); !ok {
			t.Fatal("healthy media was rejected")
		}
	}
	if calls != 1 {
		t.Fatalf("probes = %d, want 1 within the playback revalidation window", calls)
	}
}

// A probe that runs out of time (slow usenet segment, debrid lookup) proves
// nothing about the source and must not discard the prepared entry.
func TestPlaybackReuseKeepsEntryWhenProbeTimesOut(t *testing.T) {
	store := NewPrequeueStore(time.Hour)
	entry, _ := store.Create("title", "Fixture", "profile", "movie", 2026, nil, "details")
	store.Update(entry.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusReady; e.StreamPath = "/webdav/slow.mkv" })
	store.mu.Lock()
	delete(store.streamPathValidated, streamPathValidationKey(entry.ID, "/webdav/slow.mkv"))
	store.mu.Unlock()
	calls := 0
	store.SetStreamPathValidator(func(context.Context, string) error {
		calls++
		return fmt.Errorf("first segment: %w", context.DeadlineExceeded)
	})
	if _, ok := store.GetForPlayback(entry.ID); !ok {
		t.Fatal("timed-out probe discarded the prepared entry")
	}
	if _, ok := store.GetForPlayback(entry.ID); !ok || calls != 2 {
		t.Fatalf("timed-out probe must leave the entry unvalidated (ok=%v, probes=%d)", ok, calls)
	}

	// External URLs keep the old rule: a failed probe drops the entry.
	external, _ := store.Create("title-2", "Fixture", "profile", "movie", 2026, nil, "details")
	store.Update(external.ID, func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "https://example.invalid/file.mkv"
	})
	store.mu.Lock()
	delete(store.streamPathValidated, streamPathValidationKey(external.ID, "https://example.invalid/file.mkv"))
	store.mu.Unlock()
	if _, ok := store.Get(external.ID); ok {
		t.Fatal("timed-out external URL was kept")
	}
}

// ageValidation makes the entry's last successful probe `age` old. An entry
// that just became ready counts as freshly validated.
func ageValidation(store *PrequeueStore, id, streamPath string, age time.Duration) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.streamPathValidated[streamPathValidationKey(id, streamPath)] = time.Now().Add(-age)
}

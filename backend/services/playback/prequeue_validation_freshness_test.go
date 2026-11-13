package playback

import (
	"context"
	"errors"
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

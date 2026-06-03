package playback

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

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

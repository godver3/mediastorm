package playback

import (
	"testing"
	"time"

	"novastream/models"
)

func TestFindReadyByStreamPathMatchesNormalizedPath(t *testing.T) {
	store := NewPrequeueStore(3 * time.Hour)
	wire := func(e *PrequeueEntry) {
		e.Status = PrequeueStatusReady
		e.StreamPath = "/webdav/usenet/abc/Title file.mkv"
	}

	older, _ := store.CreateScoped("t1", "Older", "u1", "movie", 2020, nil, "details", "")
	older, _ = store.Get(older.ID)
	wire(older)
	time.Sleep(2 * time.Millisecond)

	entry, _ := store.CreateScoped("t1", "Newer", "u1", "movie", 2020, nil, "details", "")
	entry, _ = store.Get(entry.ID)
	wire(entry)

	// Lookup with a *different* but normalized form of the same path (what the
	// HLS start handler sees after it strips the /webdav prefix).
	got, ok := store.FindReadyByStreamPath("usenet/abc/Title file.mkv")
	if !ok {
		t.Fatalf("expected ready entry found")
	}
	if got.ID != entry.ID {
		t.Fatalf("expected the newest entry, got %s (older=%s)", got.ID, older.ID)
	}
}

func TestFindReadyByStreamPathSkipsNonReady(t *testing.T) {
	store := NewPrequeueStore(3 * time.Hour)
	entry, _ := store.CreateScoped("t1", "Queued", "u1", "movie", 2020, nil, "details", "")
	entry, _ = store.Get(entry.ID)
	entry.StreamPath = "/webdav/usenet/abc/file.mkv"

	if _, ok := store.FindReadyByStreamPath("usenet/abc/file.mkv"); ok {
		t.Fatalf("queued entry must not be returned")
	}

	entry.Status = PrequeueStatusReady
	entry.StreamPath = "file.mkv" // no usenet prefix at all
	got, ok := store.FindReadyByStreamPath("file.mkv")
	if !ok || got.ID != entry.ID {
		t.Fatalf("expected exact-match ready entry, got %v ok=%v", got, ok)
	}
}

func TestFindReadyByStreamPathEmpty(t *testing.T) {
	store := NewPrequeueStore(3 * time.Hour)
	if _, ok := store.FindReadyByStreamPath(""); ok {
		t.Fatalf("empty path must not match")
	}
	if _, ok := store.FindReadyByStreamPath("  "); ok {
		t.Fatalf("whitespace path must not match")
	}
}

var _ = models.NZBResult{} // keep models import if assertions change

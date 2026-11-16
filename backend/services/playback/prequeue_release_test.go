package playback

import (
	"sort"
	"sync"
	"testing"
	"time"
)

type releaseRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *releaseRecorder) release(streamPath, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, streamPath)
}

func (r *releaseRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.paths...)
	sort.Strings(out)
	return out
}

func TestPrequeueStoreReleasesStreamPathsOfRemovedEntries(t *testing.T) {
	store := NewPrequeueStore(time.Minute)
	recorder := &releaseRecorder{}
	store.SetStreamPathReleaser(recorder.release)

	ready := func(titleID, path string) string {
		entry, _ := store.Create(titleID, titleID, "user", "movie", 2020, nil, "test")
		store.Update(entry.ID, func(e *PrequeueEntry) { e.StreamPath = path })
		return entry.ID
	}

	deletedID := ready("deleted", "/debrid/alldebrid/1/file/1")
	store.Delete(deletedID)

	ready("replaced", "/debrid/alldebrid/2/file/1")
	// A new prequeue for the same title/user replaces the old entry, which is
	// what happens when prewarm re-resolves an expiring entry.
	ready("replaced", "/debrid/alldebrid/3/file/1")

	expiredID := ready("expired", "/debrid/alldebrid/4/file/1")
	store.ForceExpiry(expiredID, time.Now().Add(-time.Second))
	store.cleanup()

	// An entry that never resolved has nothing to release.
	pending, _ := store.Create("pending", "pending", "user", "movie", 2020, nil, "test")
	store.Delete(pending.ID)

	want := []string{"/debrid/alldebrid/1/file/1", "/debrid/alldebrid/2/file/1", "/debrid/alldebrid/4/file/1"}
	if got := recorder.got(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("released = %v, want %v", got, want)
	}
}

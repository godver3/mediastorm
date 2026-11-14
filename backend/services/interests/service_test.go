package interests

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novastream/models"
)

func recordTitle(t *testing.T, s *Service, profile string, id int64) {
	t.Helper()
	s.Observe(profile, models.Title{TMDBID: id, MediaType: "movie", Name: "Example"})
	item := <-s.queue
	if err := s.record(item); err != nil {
		t.Fatal(err)
	}
}

func TestInterestsAreProfileLocalPersistentAndThrottled(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	recordTitle(t, s, "stefan", 1)
	recordTitle(t, s, "stefan", 1)
	if len(s.List("stefan")) != 1 || len(s.List("ella")) != 0 {
		t.Fatal("profile isolation/dedup failed")
	}
	loaded, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.List("stefan")) != 1 {
		t.Fatal("signals lost after restart")
	}
	info, err := os.Stat(filepath.Join(dir, digest("stefan")))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe file permissions")
	}
}

func TestClearingInterestsInvalidatesQueuedObservations(t *testing.T) {
	s, _ := NewService(t.TempDir())
	s.Observe("stefan", models.Title{TMDBID: 1, MediaType: "movie", Name: "Example"})
	old := <-s.queue
	if err := s.Clear("stefan"); err != nil {
		t.Fatal(err)
	}
	if err := s.record(old); err != nil {
		t.Fatal(err)
	}
	if len(s.List("stefan")) != 0 {
		t.Fatal("queued observation restored cleared data")
	}
	recordTitle(t, s, "stefan", 2)
	if len(s.List("stefan")) != 1 {
		t.Fatal("new observation rejected after clearing")
	}
}

func TestInterestObservationDoesNotWaitForDiskLock(t *testing.T) {
	s, _ := NewService(t.TempDir())
	s.mu.Lock()
	done := make(chan struct{})
	go func() { s.Observe("stefan", models.Title{TMDBID: 1, MediaType: "movie"}); close(done) }()
	select {
	case <-done:
		s.mu.Unlock()
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("search waited for cache disk lock")
	}
	for range 100 {
		s.Observe("stefan", models.Title{TMDBID: 1, MediaType: "movie"})
	}
	if len(s.queue) != 64 {
		t.Fatalf("queue size=%d", len(s.queue))
	}
}

func TestInterestsAreBoundedAndExpiredDataIsRemovedFromDisk(t *testing.T) {
	s, _ := NewService(t.TempDir())
	for p := 0; p < 11; p++ {
		for i := 0; i < MaxPerProfile+5; i++ {
			recordTitle(t, s, fmt.Sprint(p), int64(i+1))
		}
	}
	total := 0
	for _, items := range s.data {
		if len(items) > MaxPerProfile {
			t.Fatal("profile bound exceeded")
		}
		total += len(items)
	}
	if total > MaxTotal {
		t.Fatal("server bound exceeded")
	}
	s.now = func() time.Time { return time.Now().AddDate(0, 0, RetentionDays+1) }
	s.mu.Lock()
	s.trimLocked()
	s.mu.Unlock()
	files, err := os.ReadDir(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 || len(s.data) != 0 {
		t.Fatal("expired signals retained on disk")
	}
}

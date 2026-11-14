package playback

import (
	"testing"
	"time"
)

func TestReportReplacementPreservesPlayingSourceAndDeduplicates(t *testing.T) {
	s := NewPrequeueStore(time.Hour)
	old, _ := s.Create("tmdb:movie:123", "Synthetic Movie", "profile", "movie", 2024, nil, "manual")
	s.Update(old.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusReady; e.StreamPath = "/fixture/old.mp4" })
	next, created, err := s.CreateReportReplacement(old.ID, DefaultPrequeueSettingsScopeKey)
	if err != nil || !created || next.ID == old.ID {
		t.Fatalf("replacement=%+v created=%v err=%v", next, created, err)
	}
	if playing, ok := s.Get(old.ID); !ok || playing.StreamPath != "/fixture/old.mp4" {
		t.Fatal("playing source was removed during preparation")
	}
	again, created, err := s.CreateReportReplacement(old.ID, DefaultPrequeueSettingsScopeKey)
	if err != nil || created || again.ID != next.ID {
		t.Fatal("repeated report started another preparation")
	}
	s.Update(next.ID, func(e *PrequeueEntry) { e.Status = PrequeueStatusFailed })
	retry, created, err := s.CreateReportReplacement(old.ID, DefaultPrequeueSettingsScopeKey)
	if err != nil || !created || retry.ID == next.ID {
		t.Fatal("failed alternative could not be retried")
	}
}

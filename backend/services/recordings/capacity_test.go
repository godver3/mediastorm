package recordings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/internal/livecapacity"
	"novastream/models"
)

type capacityChannels struct {
	manager *livecapacity.Manager
	key     string
}

func (p capacityChannels) RecordingChannels(context.Context, string) ([]Channel, error) {
	return nil, nil
}
func (p capacityChannels) ReserveRecording(context.Context, models.Recording) (func(), error) {
	lease, err := p.manager.Acquire(p.key, "", 1)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

func TestRecordingCapacityConflictWaitsWithoutOpeningOrTruncatingOutput(t *testing.T) {
	m := livecapacity.NewManager()
	viewer, _ := m.Acquire("account", "", 1)
	defer viewer.Release()
	job := newTestRecording(time.Now().Add(time.Minute))
	job.OutputPath = filepath.Join(t.TempDir(), "existing.ts")
	if err := os.WriteFile(job.OutputPath, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := newFakeRecordingRepo(job)
	s := newTestService(repo, "must-not-execute", t.TempDir())
	s.ctx = context.Background()
	s.channels = capacityChannels{m, "account"}
	s.startRecording(job)
	latest, _ := s.Get(job.ID)
	if latest.Status != models.RecordingStatusPending || latest.OutputPath != job.OutputPath || !strings.Contains(latest.Error, "waiting") {
		t.Fatalf("conflict changed output or lost pending job: %+v", latest)
	}
	if m.Current("account") != 1 {
		t.Fatal("recording evicted existing viewer")
	}
	if content, err := os.ReadFile(job.OutputPath); err != nil || string(content) != "preserved" {
		t.Fatal("conflict modified existing data")
	}
	viewer.Release()
	// Once capacity is free, the normal missing-FFmpeg failure is reached.
	// Its input reservation must still be released.
	s.ffmpegPath = ""
	s.startRecording(*latest)
	if m.Current("account") != 0 {
		t.Fatal("failed recorder leaked connection")
	}
	latest, _ = s.Get(job.ID)
	if latest.Status == models.RecordingStatusPending {
		t.Fatal("free capacity did not retry pending recording")
	}
}

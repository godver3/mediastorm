package recordings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/models"
)

type fakeRecordingRepo struct {
	mu         sync.Mutex
	recordings map[string]models.Recording
}

func newFakeRecordingRepo(recordings ...models.Recording) *fakeRecordingRepo {
	repo := &fakeRecordingRepo{recordings: make(map[string]models.Recording, len(recordings))}
	for _, recording := range recordings {
		repo.recordings[recording.ID] = recording
	}
	return repo
}

func (r *fakeRecordingRepo) Get(_ context.Context, id string) (*models.Recording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	recording, ok := r.recordings[id]
	if !ok {
		return nil, nil
	}
	copy := recording
	return &copy, nil
}

func (r *fakeRecordingRepo) List(_ context.Context, _ models.RecordingListFilter) ([]models.Recording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]models.Recording, 0, len(r.recordings))
	for _, recording := range r.recordings {
		out = append(out, recording)
	}
	return out, nil
}

func (r *fakeRecordingRepo) Create(_ context.Context, recording *models.Recording) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordings[recording.ID] = *recording
	return nil
}

func (r *fakeRecordingRepo) Update(_ context.Context, recording *models.Recording) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordings[recording.ID] = *recording
	return nil
}

func (r *fakeRecordingRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.recordings, id)
	return nil
}

func (r *fakeRecordingRepo) Count(_ context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.recordings)), nil
}

func (r *fakeRecordingRepo) MarkStaleActiveAsFailed(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func TestStartRecordingRetriesTransientFailure(t *testing.T) {
	t.Setenv("ATTEMPT_FILE", filepath.Join(t.TempDir(), "attempts.txt"))
	script := writeFakeFFmpeg(t, `#!/bin/sh
count=0
if [ -f "$ATTEMPT_FILE" ]; then
  count=$(wc -l < "$ATTEMPT_FILE")
fi
count=$((count+1))
printf '%s\n' "$count" >> "$ATTEMPT_FILE"
printf 'chunk-%s\n' "$count"
if [ "$count" -eq 1 ]; then
  exit 1
fi
sleep 0.35
exit 0
`)

	originalDelay := recordingRetryDelay
	recordingRetryDelay = 10 * time.Millisecond
	defer func() { recordingRetryDelay = originalDelay }()

	recording := newTestRecording(time.Now().UTC().Add(250 * time.Millisecond))
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, script, t.TempDir())

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.ctx = rootCtx

	svc.startRecording(recording)

	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if latest.Status != models.RecordingStatusCompleted {
		t.Fatalf("Status = %q, want %q (error=%q)", latest.Status, models.RecordingStatusCompleted, latest.Error)
	}
	if latest.OutputSizeBytes == 0 {
		t.Fatal("expected output bytes after retry")
	}
	content, err := os.ReadFile(latest.OutputPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "chunk-1") || !strings.Contains(got, "chunk-2") {
		t.Fatalf("output = %q, want appended chunks from both attempts", got)
	}
}

func TestStartRecordingFailsAfterRetryWindowExpires(t *testing.T) {
	t.Setenv("ATTEMPT_FILE", filepath.Join(t.TempDir(), "attempts.txt"))
	script := writeFakeFFmpeg(t, `#!/bin/sh
count=0
if [ -f "$ATTEMPT_FILE" ]; then
  count=$(wc -l < "$ATTEMPT_FILE")
fi
count=$((count+1))
printf '%s\n' "$count" >> "$ATTEMPT_FILE"
printf 'broken-%s\n' "$count"
exit 1
`)

	originalDelay := recordingRetryDelay
	recordingRetryDelay = 10 * time.Millisecond
	defer func() { recordingRetryDelay = originalDelay }()

	recording := newTestRecording(time.Now().UTC().Add(120 * time.Millisecond))
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, script, t.TempDir())

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.ctx = rootCtx

	svc.startRecording(recording)

	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if latest.Status != models.RecordingStatusFailed {
		t.Fatalf("Status = %q, want %q", latest.Status, models.RecordingStatusFailed)
	}
	if latest.Error == "" {
		t.Fatal("expected final failure error to be preserved")
	}
	if latest.OutputSizeBytes == 0 {
		t.Fatal("expected partial bytes to remain on disk for inspection")
	}
}

func newTestService(repo *fakeRecordingRepo, ffmpegPath, outputDir string) *Service {
	return &Service{
		repo:       repo,
		ffmpegPath: ffmpegPath,
		outputDir:  outputDir,
		active:     make(map[string]context.CancelFunc),
	}
}

func newTestRecording(endAt time.Time) models.Recording {
	now := time.Now().UTC()
	return models.Recording{
		ID:          fmt.Sprintf("rec-%d", now.UnixNano()),
		UserID:      "default",
		Type:        models.RecordingTypeTimeBlock,
		Status:      models.RecordingStatusPending,
		ChannelID:   "channel-1",
		ChannelName: "Animal Planet",
		Title:       "Expedition Mungo",
		SourceURL:   "http://example.invalid/stream.ts",
		StartAt:     now.Add(-time.Minute),
		EndAt:       endAt,
		CreatedAt:   now,
		UpdatedAt:   now,
		OutputPath:  filepath.Join(os.TempDir(), fmt.Sprintf("recording-%d.ts", now.UnixNano())),
	}
}

func writeFakeFFmpeg(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

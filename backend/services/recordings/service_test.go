package recordings

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestCompileRecordingRegexCaseSensitivity(t *testing.T) {
	tests := []struct {
		pattern string
		title   string
		want    bool
	}{
		{pattern: "road", title: "Road Wars", want: true},
		{pattern: "road", title: "Offroad Odyssey", want: true},
		{pattern: "(?-i)road", title: "Road Wars", want: false},
		{pattern: "(?-i)Road", title: "Road Wars", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.title, func(t *testing.T) {
			compiled, err := compileRecordingRegex(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := compiled.MatchString(tt.title); got != tt.want {
				t.Fatalf("pattern %q matching %q = %t, want %t", tt.pattern, tt.title, got, tt.want)
			}
		})
	}
}

func TestStartRecordingRetriesTransientFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary shell script is POSIX-only")
	}
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
# Keep the successful retry alive beyond the recording deadline. The generous
# window gives loaded CI workers time to launch the retry before it expires.
sleep 5.25
exit 0
`)

	originalDelay := recordingRetryDelay
	recordingRetryDelay = 10 * time.Millisecond
	defer func() { recordingRetryDelay = originalDelay }()

	recording := newTestRecording(time.Now().UTC().Add(5 * time.Second))
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
	if runtime.GOOS == "windows" {
		t.Skip("fake binary shell script is POSIX-only")
	}
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

	recording := newTestRecording(time.Now().UTC().Add(2 * time.Second))
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, script, t.TempDir())

	repairs := 0
	svc.remuxRecording = func(path string) error {
		repairs++
		return os.WriteFile(path, []byte("repaired-partial-media"), 0o644)
	}

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
	if repairs != 1 {
		t.Fatalf("repairs = %d, want 1 even after capture failure", repairs)
	}
	if latest.OutputSizeBytes != int64(len("repaired-partial-media")) {
		t.Fatalf("size = %d, want repaired file size", latest.OutputSizeBytes)
	}
}

func TestCancelledRecordingKeepsPlayableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary shell script is POSIX-only")
	}
	script := writeFakeFFmpeg(t, `#!/bin/sh
printf 'recorded-media-data'
exec sleep 10
`)
	recording := newTestRecording(time.Now().UTC().Add(time.Minute))
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, script, t.TempDir())
	svc.ctx = context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.startRecording(recording)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if current, err := svc.Get(recording.ID); err == nil && current.OutputPath != "" {
			if info, statErr := os.Stat(current.OutputPath); statErr == nil && info.Size() > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("recording did not write media before cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := svc.Cancel(recording.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recording did not stop after cancellation")
	}
	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != models.RecordingStatusCancelled || latest.OutputSizeBytes == 0 {
		t.Fatalf("cancelled recording status=%q size=%d", latest.Status, latest.OutputSizeBytes)
	}
	if _, err := os.Stat(latest.OutputPath); err != nil {
		t.Fatalf("cancelled recording file missing: %v", err)
	}
}

func newTestService(repo *fakeRecordingRepo, ffmpegPath, outputDir string) *Service {
	return &Service{
		repo:           repo,
		ffmpegPath:     ffmpegPath,
		outputDir:      outputDir,
		active:         make(map[string]context.CancelFunc),
		remuxRecording: func(string) error { return nil },
	}
}

func TestRetriedRecordingHasContinuousTimeline(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "source.ts")
	output := filepath.Join(dir, "recording.ts")
	cmd := exec.Command(ffmpegPath, "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25", "-t", "3",
		"-c:v", "mpeg2video", "-g", "25", "-f", "mpegts", source)
	if result, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v: %s", err, result)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer server.Close()

	svc := NewService(nil, ffmpegPath, dir)
	recording := models.Recording{SourceURL: server.URL + "/source.ts", OutputPath: output}
	stopAt := time.Now().Add(30 * time.Second)
	for attempt := 1; attempt <= 2; attempt++ {
		// The file source hits EOF long before stopAt, like a dropped live stream.
		if result := svc.runRecordingAttempt(context.Background(), recording, stopAt, attempt); result.err != nil || result.stoppedAtSchedule {
			t.Fatalf("recording attempt %d: %+v", attempt, result)
		}
	}
	duration := func() float64 {
		t.Helper()
		result, err := exec.Command(ffprobePath, "-v", "error", "-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1", output).CombinedOutput()
		if err != nil {
			t.Fatalf("probe recording: %v: %s", err, result)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(result)), 64)
		if err != nil {
			t.Fatalf("parse duration %q: %v", result, err)
		}
		return value
	}
	if got := duration(); got > 4 {
		t.Fatalf("unrepaired duration = %.2fs, want timestamp reset near 3s", got)
	}
	if err := svc.remuxRecording(output); err != nil {
		t.Fatalf("remux recording: %v", err)
	}
	if got := duration(); got < 5.5 || got > 6.5 {
		t.Fatalf("repaired duration = %.2fs, want about 6s", got)
	}
}

func TestStartRecordingStopsAtWallClockStopTime(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("ARGS_FILE", argsFile)
	// Runs until interrupted, like a live source; ignores any -t argument.
	script := writeFakeFFmpeg(t, `#!/bin/sh
printf '%s\n' "$@" > "$ARGS_FILE"
trap 'printf tail; exit 255' INT
printf 'live-media'
while :; do sleep 0.05; done
`)
	endAt := time.Now().UTC().Add(time.Second)
	recording := newTestRecording(endAt)
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, script, t.TempDir())
	svc.ctx = context.Background()

	svc.startRecording(recording)

	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if latest.Status != models.RecordingStatusCompleted {
		t.Fatalf("Status = %q, want %q (error=%q)", latest.Status, models.RecordingStatusCompleted, latest.Error)
	}
	if latest.ActualEndAt == nil || latest.ActualEndAt.Before(endAt) || latest.ActualEndAt.After(endAt.Add(3*time.Second)) {
		t.Fatalf("ActualEndAt = %v, want shortly after stop time %v", latest.ActualEndAt, endAt)
	}
	content, err := os.ReadFile(latest.OutputPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "live-mediatail" {
		t.Fatalf("output = %q, want media flushed after graceful stop", content)
	}
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("ReadFile(args) error = %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	for _, arg := range args {
		if arg == "-t" {
			t.Fatalf("ffmpeg args %q use -t; stop time must be wall-clock", args)
		}
	}
	if !strings.Contains(string(rawArgs), "-rw_timeout\n") {
		t.Fatalf("ffmpeg args %q missing -rw_timeout", args)
	}
}

func TestStartRecordingFailsWhenSourceKeepsEnding(t *testing.T) {
	script := writeFakeFFmpeg(t, `#!/bin/sh
printf 'partial'
exit 0
`)
	originalDelay := recordingRetryDelay
	recordingRetryDelay = 10 * time.Millisecond
	defer func() { recordingRetryDelay = originalDelay }()

	recording := newTestRecording(time.Now().UTC().Add(500 * time.Millisecond))
	repo := newFakeRecordingRepo(recording)
	svc := newTestService(repo, script, t.TempDir())
	svc.remuxRecording = func(string) error { return nil }
	svc.ctx = context.Background()

	svc.startRecording(recording)

	latest, err := svc.Get(recording.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if latest.Status != models.RecordingStatusFailed {
		t.Fatalf("Status = %q, want %q", latest.Status, models.RecordingStatusFailed)
	}
	if latest.Error != "source stream ended before scheduled stop time" {
		t.Fatalf("Error = %q, want source-ended message", latest.Error)
	}
}

func TestRedactSourceURL(t *testing.T) {
	got := redactSourceURL("http://user:pass@iptv.example:8080/live/user/pass/123.ts?token=x")
	if got != "http://iptv.example:8080" {
		t.Fatalf("redactSourceURL() = %q", got)
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
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg shell script is POSIX-only")
	}
	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

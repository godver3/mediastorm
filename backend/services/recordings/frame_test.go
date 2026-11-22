package recordings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func TestFrameOffset(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	ptr := func(t time.Time) *time.Time { return &t }
	cases := []struct {
		name string
		rec  models.Recording
		want time.Duration
	}{
		{"a tenth in", models.Recording{Status: models.RecordingStatusCompleted, ActualStartAt: ptr(at), ActualEndAt: ptr(at.Add(50 * time.Minute))}, 5 * time.Minute},
		{"at most ten minutes", models.Recording{Status: models.RecordingStatusCompleted, ActualStartAt: ptr(at), ActualEndAt: ptr(at.Add(3 * time.Hour))}, 10 * time.Minute},
		{"past the padding", models.Recording{Status: models.RecordingStatusCompleted, PaddingBeforeSeconds: 120, ActualStartAt: ptr(at), ActualEndAt: ptr(at.Add(10 * time.Minute))}, 2 * time.Minute},
		{"never past the middle", models.Recording{Status: models.RecordingStatusCompleted, PaddingBeforeSeconds: 300, ActualStartAt: ptr(at), ActualEndAt: ptr(at.Add(4 * time.Minute))}, 2 * time.Minute},
		{"running: what is recorded so far", models.Recording{Status: models.RecordingStatusRunning, ActualStartAt: ptr(at.Add(-20 * time.Minute)), EndAt: at.Add(time.Hour)}, 2 * time.Minute},
		{"nothing recorded", models.Recording{Status: models.RecordingStatusRunning, ActualStartAt: ptr(at.Add(time.Minute))}, 0},
	}
	for _, c := range cases {
		if got := frameOffset(c.rec, at); got != c.want {
			t.Errorf("%s: offset = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGuideImageURL(t *testing.T) {
	for raw, want := range map[string]string{
		" https://img.example/a.jpg ": "https://img.example/a.jpg",
		"http://img.example/b.png":    "http://img.example/b.png",
		"file:///etc/passwd":          "",
		"/relative.jpg":               "",
		"https:///no-host.jpg":        "",
		"":                            "",
		"https://img.example/" + strings.Repeat("x", 3000): "",
	} {
		if got := guideImageURL(raw); got != want {
			t.Errorf("guideImageURL(%.40q) = %q, want %q", raw, got, want)
		}
	}
}

// A still comes from the recording's own file, taken past its start, scaled
// to the requested width.
func TestFrameRunsFFmpegOnTheRecording(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := writeFakeFFmpeg(t, "#!/bin/sh\nfor a in \"$@\"; do printf '%s\n' \"$a\"; done > "+argsFile+"\nprintf JPEGDATA\n")
	start := time.Now().Add(-time.Hour).UTC()
	end := start.Add(30 * time.Minute)
	rec := models.Recording{ID: "rec-1", Status: models.RecordingStatusCompleted, OutputPath: filepath.Join(dir, "News.ts"), ActualStartAt: &start, ActualEndAt: &end}
	s := newTestService(newFakeRecordingRepo(rec, models.Recording{ID: "pending"}), script, dir)

	jpeg, err := s.Frame(context.Background(), "rec-1", 5000)
	if err != nil || string(jpeg) != "JPEGDATA" {
		t.Fatalf("Frame = %q, %v", jpeg, err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := "\n" + string(raw)
	for _, want := range []string{"\n-ss\n180.0\n", "\n-i\n" + rec.OutputPath + "\n", "\nscale=1280:-2\n", "\npipe:1\n"} {
		if !strings.Contains(args, want) {
			t.Fatalf("ffmpeg args miss %q:%s", want, args)
		}
	}
	if _, err := s.Frame(context.Background(), "pending", 640); err != ErrNoRecordingFile {
		t.Fatalf("recording without a file: %v", err)
	}
	if _, err := s.Frame(context.Background(), "missing", 640); err != ErrRecordingNotFound {
		t.Fatalf("unknown recording: %v", err)
	}
}

package recordings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novastream/models"
	"novastream/services/streaming"
)

func TestParseStreamPath(t *testing.T) {
	cases := []struct {
		path   string
		wantID string
		wantOK bool
	}{
		{"recording:abc123/My Show.ts", "abc123", true},
		{"recording:abc123", "abc123", true},
		{"recording:", "", false},
		{"localmedia:abc123/file.mkv", "", false},
		{"abc123", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		id, ok := ParseStreamPath(tc.path)
		if ok != tc.wantOK || id != tc.wantID {
			t.Errorf("ParseStreamPath(%q) = (%q, %v), want (%q, %v)", tc.path, id, ok, tc.wantID, tc.wantOK)
		}
	}
}

func TestBuildStreamPathRoundTrip(t *testing.T) {
	rec := models.Recording{ID: "rec-1", OutputPath: "/data/recordings/news.ts"}
	path := BuildStreamPath(rec)
	id, ok := ParseStreamPath(path)
	if !ok || id != "rec-1" {
		t.Fatalf("round trip failed: path=%q id=%q ok=%v", path, id, ok)
	}
}

func TestStreamProviderGetDirectURL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "capture.ts")
	if err := os.WriteFile(file, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	repo := newFakeRecordingRepo(models.Recording{ID: "rec-1", OutputPath: file})
	provider := NewStreamProvider(NewService(repo, "ffmpeg", dir))

	url, err := provider.GetDirectURL(context.Background(), BuildStreamPath(models.Recording{ID: "rec-1", OutputPath: file}))
	if err != nil {
		t.Fatalf("GetDirectURL error: %v", err)
	}
	if url != file {
		t.Fatalf("GetDirectURL = %q, want %q", url, file)
	}
}

func TestStreamProviderUnknownRecording(t *testing.T) {
	repo := newFakeRecordingRepo()
	provider := NewStreamProvider(NewService(repo, "ffmpeg", t.TempDir()))

	if _, err := provider.GetDirectURL(context.Background(), "recording:missing/x.ts"); !errors.Is(err, streaming.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStreamProviderNonRecordingPath(t *testing.T) {
	provider := NewStreamProvider(NewService(newFakeRecordingRepo(), "ffmpeg", t.TempDir()))
	if _, err := provider.GetDirectURL(context.Background(), "localmedia:abc/x.mkv"); !errors.Is(err, streaming.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-recording path, got %v", err)
	}
}

func TestStreamProviderStreamServesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "capture.ts")
	content := []byte("hello recording bytes")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	provider := NewStreamProvider(NewService(newFakeRecordingRepo(models.Recording{ID: "rec-1", OutputPath: file}), "ffmpeg", dir))

	resp, err := provider.Stream(context.Background(), streaming.Request{Path: "recording:rec-1/capture.ts", Method: "GET"})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	defer resp.Close()
	if resp.ContentLength != int64(len(content)) {
		t.Fatalf("ContentLength = %d, want %d", resp.ContentLength, len(content))
	}
}

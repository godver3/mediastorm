package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveTimeshiftEndToEnd runs a real timeshift live session on a synthetic live MPEG-TS
// channel (FFmpeg at 1x, 2 s GOPs) with a 70 s window: a "player" fetches the first segment and
// then only sends keepalives, as a paused one does. After ~100 s the session must still be
// running, list about the window (not the ordinary 30 entries, and not everything), and have
// deleted the segments that left it. Needs ffmpeg; runs only when MEDIASTORM_FFMPEG_E2E=1.
func TestLiveTimeshiftEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.CommandContext(r.Context(), "ffmpeg", "-nostdin", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0",
			"-c:a", "aac", "-f", "mpegts", "pipe:1")
		out, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		if cmd.Start() != nil {
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = io.Copy(w, out)
		_ = cmd.Wait()
	}))
	defer channel.Close()

	dir := t.TempDir()
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	window := 70 * time.Second
	session, err := m.CreateLiveSession(context.Background(), channel.URL+"/live.ts", "m3u", "m3u:test", "p", "p",
		"127.0.0.1", "native", LiveTuningSettings{TimeshiftWindow: window}, false)
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	defer m.CleanupSession(session.ID)

	playlist := func() string {
		rec := httptest.NewRecorder()
		m.ServePlaylist(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/stream.m3u8", nil), session.ID)
		return rec.Body.String()
	}
	keepalive := func() {
		m.KeepAlive(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/video/hls/"+session.ID+"/keepalive?paused=true", nil), session.ID)
	}

	// The player joins: the first segment, once it exists.
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(playlist(), "segment0.ts") {
		if time.Now().After(deadline) {
			t.Fatalf("no first segment; playlist:\n%s", playlist())
		}
		time.Sleep(500 * time.Millisecond)
	}
	rec := httptest.NewRecorder()
	m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/segment0.ts", nil), session.ID, "segment0.ts")
	if rec.Code != http.StatusOK {
		t.Fatalf("segment0: %d", rec.Code)
	}

	// Paused: keepalives and playlist polls only, for well past the window.
	for elapsed := time.Duration(0); elapsed < 100*time.Second; elapsed += 5 * time.Second {
		time.Sleep(5 * time.Second)
		keepalive()
		_ = playlist()
	}

	session.mu.RLock()
	completed, fatal := session.Completed, session.FatalError
	session.mu.RUnlock()
	if completed {
		t.Fatalf("session ended while paused (fatal=%q)", fatal)
	}
	snapshot := parseLivePlaylist(playlist())
	listed := 0.0
	for _, entry := range snapshot.Entries {
		listed += entry.Duration
	}
	t.Logf("listed %d entries, %.1f s, first sequence %d", len(snapshot.Entries), listed, snapshot.MediaSequence)
	if len(snapshot.Entries) <= liveNativeHLSListSize {
		t.Fatalf("listed %d entries; a timeshift window must list more than the ordinary %d", len(snapshot.Entries), liveNativeHLSListSize)
	}
	if listed > window.Seconds()+10 || listed < window.Seconds()-10 {
		t.Fatalf("listed %.1f s, want about the %s window", listed, window)
	}
	if snapshot.MediaSequence == 0 {
		t.Fatalf("the playlist still starts at segment 0 after %s", 100*time.Second)
	}
	if _, err := os.Stat(filepath.Join(session.OutputDir, "segment0.ts")); !os.IsNotExist(err) {
		t.Fatalf("segment0.ts should have been pruned once it left the window (err=%v)", err)
	}
	first := filepath.Join(session.OutputDir, fmt.Sprintf("segment%d.ts", snapshot.MediaSequence))
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("the window's first segment must stay on disk: %v", err)
	}
}

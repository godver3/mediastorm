package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/services/streaming"
)

type growingTestProvider struct {
	directURL string
	growing   bool
}

func (p *growingTestProvider) Stream(context.Context, streaming.Request) (*streaming.Response, error) {
	return &streaming.Response{Body: io.NopCloser(strings.NewReader("video"))}, nil
}

func (p *growingTestProvider) GetDirectURL(context.Context, string) (string, error) {
	return p.directURL, nil
}

func (p *growingTestProvider) IsGrowing(context.Context, string) (bool, error) {
	return p.growing, nil
}

// writeArgsRecordingFFmpeg writes a fake ffmpeg that records its arguments.
// The file appears only once complete: callers read it as soon as it exists.
func writeArgsRecordingFFmpeg(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "ffmpeg")
	body := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do printf '%%s\\n' \"$a\"; done > %q && mv %q %q\n", argsFile+".tmp", argsFile+".tmp", argsFile)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return script, argsFile
}

func waitForArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(argsFile); err == nil && len(data) > 0 {
			return strings.Split(strings.TrimSpace(string(data)), "\n")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake ffmpeg was not run")
	return nil
}

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func startGrowingTestSession(t *testing.T, growing bool) (map[string]any, []string) {
	t.Helper()
	inputPath := filepath.Join(t.TempDir(), "recording.ts")
	if err := os.WriteFile(inputPath, []byte("not a real media file"), 0o644); err != nil {
		t.Fatalf("write input fixture: %v", err)
	}
	ffmpeg, argsFile := writeArgsRecordingFFmpeg(t)
	provider := &growingTestProvider{directURL: inputPath, growing: growing}
	handler := NewVideoHandlerWithProvider(true, ffmpeg, "/definitely-missing-ffprobe", t.TempDir(), provider)
	t.Cleanup(handler.hlsManager.Shutdown)

	query := url.Values{
		"path":         {"recording:rec-1/recording.ts"},
		"durationHint": {"300"},
	}
	rec := httptest.NewRecorder()
	handler.StartHLSSession(rec, httptest.NewRequest(http.MethodGet, "/video/hls/start?"+query.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body, waitForArgs(t, argsFile)
}

func TestStartHLSSessionFollowsRunningRecording(t *testing.T) {
	body, args := startGrowingTestSession(t, true)

	if body["growing"] != true {
		t.Fatalf("growing = %v, want true (body=%v)", body["growing"], body)
	}
	if _, ok := body["duration"]; ok {
		t.Fatalf("a running recording must not report a duration, got %v", body["duration"])
	}

	follow := indexOfArg(args, "-follow")
	input := indexOfArg(args, "-i")
	if follow < 0 || input < 0 || follow > input || args[follow+1] != "1" {
		t.Fatalf("expected -follow 1 before -i, args=%v", args)
	}
	if rw := indexOfArg(args, "-rw_timeout"); rw < 0 || rw > input {
		t.Fatalf("expected -rw_timeout before -i, args=%v", args)
	}
	if skip := indexOfArg(args, "-skip_estimate_duration_from_pts"); skip < 0 || skip > input {
		t.Fatalf("expected -skip_estimate_duration_from_pts before -i, args=%v", args)
	}
	if indexOfArg(args, "-t") >= 0 {
		t.Fatalf("a running recording must not be cut off with -t, args=%v", args)
	}
}

func TestStartHLSSessionDoesNotFollowFinishedSource(t *testing.T) {
	body, args := startGrowingTestSession(t, false)

	if _, ok := body["growing"]; ok {
		t.Fatalf("finished source reported growing: %v", body)
	}
	if body["duration"] != float64(300) {
		t.Fatalf("duration = %v, want the 300s hint", body["duration"])
	}
	for _, flag := range []string{"-follow", "-rw_timeout", "-skip_estimate_duration_from_pts"} {
		if indexOfArg(args, flag) >= 0 {
			t.Fatalf("finished source must not get %s, args=%v", flag, args)
		}
	}
}

func TestShouldFollowGrowingInput(t *testing.T) {
	cases := []struct {
		growingSource bool
		input         string
		stillGrowing  bool
		want          bool
	}{
		{true, "/rec/a.ts", true, true},
		{true, "/rec/a.ts", false, false},
		{true, "", true, false}, // pipe / HTTP proxy input cannot follow
		{false, "/rec/a.ts", true, false},
	}
	for _, tc := range cases {
		if got := shouldFollowGrowingInput(tc.growingSource, tc.input, tc.stillGrowing); got != tc.want {
			t.Errorf("shouldFollowGrowingInput(%v, %q, %v) = %v, want %v", tc.growingSource, tc.input, tc.stillGrowing, got, tc.want)
		}
	}
}

const growingTestPlaylist = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:EVENT\n" +
	"#EXTINF:2.000000,\nsegment0.ts\n#EXTINF:2.000000,\nsegment1.ts\n#EXTINF:1.500000,\nsegment2.ts\n"

func serveGrowingTestPlaylist(t *testing.T, session *HLSSession, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	t.Cleanup(manager.Shutdown)
	session.OutputDir = filepath.Join(tmpDir, session.ID)
	if err := os.MkdirAll(session.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.OutputDir, "stream.m3u8"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	session.CreatedAt = time.Now()
	session.LastAccess = time.Now()
	manager.mu.Lock()
	manager.sessions[session.ID] = session
	manager.mu.Unlock()

	rr := httptest.NewRecorder()
	manager.ServePlaylist(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/stream.m3u8", nil), session.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func TestServePlaylistKeepsEventWhileRecordingRuns(t *testing.T) {
	// Even with a (partial-file) duration on the session, a growing source must
	// not be rewritten to VOD with synthesised segments and ENDLIST.
	session := &HLSSession{ID: "growing", Duration: 600, GrowingSource: true, SourceGrowing: true, FinalSegmentCount: -1}
	body := serveGrowingTestPlaylist(t, session, growingTestPlaylist)

	if !strings.Contains(body, "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(body, "VOD") {
		t.Fatalf("expected EVENT playlist, got:\n%s", body)
	}
	if strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("running recording must not have ENDLIST:\n%s", body)
	}
	if strings.Contains(body, "segment3.ts") {
		t.Fatalf("running recording must not list synthesised segments:\n%s", body)
	}
	if !strings.Contains(body, "#EXT-X-START:TIME-OFFSET=0") {
		t.Fatalf("EVENT playlist should start from the beginning:\n%s", body)
	}
}

func TestServePlaylistEndsGrowingSessionKilledWithoutEndlist(t *testing.T) {
	session := &HLSSession{ID: "growing-killed", GrowingSource: true, SourceGrowing: true, Completed: true}
	body := serveGrowingTestPlaylist(t, session, growingTestPlaylist)
	if !strings.HasSuffix(strings.TrimSpace(body), "#EXT-X-ENDLIST") {
		t.Fatalf("completed growing session should end with ENDLIST:\n%s", body)
	}
}

func TestServePlaylistStillRewritesOrdinaryVOD(t *testing.T) {
	session := &HLSSession{ID: "vod", Duration: 10, FinalSegmentCount: -1}
	body := serveGrowingTestPlaylist(t, session, growingTestPlaylist)
	if !strings.Contains(body, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(body, "segment4.ts") || !strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("ordinary VOD should be rewritten with synthesised segments:\n%s", body)
	}
}

func TestPlaylistMediaDuration(t *testing.T) {
	if got := playlistMediaDuration(growingTestPlaylist); got != 5.5 {
		t.Fatalf("playlistMediaDuration = %v, want 5.5", got)
	}
}

func TestDeleteOldSegmentsKeepsGrowingSourceSegments(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	t.Cleanup(manager.Shutdown)
	session := &HLSSession{ID: "keep", OutputDir: tmpDir, GrowingSource: true, EarliestBufferedSegment: 50, LastSegmentServed: 50}
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("segment%d.ts", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manager.deleteOldSegments(session, "segment50.ts")
	if _, err := os.Stat(filepath.Join(tmpDir, "segment0.ts")); err != nil {
		t.Fatalf("growing session segment0 was deleted: %v", err)
	}
}

func TestFMP4AACCopyNeedsADTSConversion(t *testing.T) {
	cases := []struct {
		fmp4  bool
		codec string
		paths []string
		want  bool
	}{
		{true, "aac", []string{"/rec/news.ts"}, true},
		{true, "aac", []string{"", "recording:abc/news.ts"}, true},
		{false, "aac", []string{"/rec/news.ts"}, false},
		{true, "ac3", []string{"/rec/news.ts"}, false},
		{true, "aac", []string{"/movies/film.mkv"}, false},
	}
	for _, tc := range cases {
		if got := fmp4AACCopyNeedsADTSConversion(tc.fmp4, tc.codec, tc.paths...); got != tc.want {
			t.Errorf("fmp4AACCopyNeedsADTSConversion(%v, %q, %v) = %v, want %v", tc.fmp4, tc.codec, tc.paths, got, tc.want)
		}
	}
}

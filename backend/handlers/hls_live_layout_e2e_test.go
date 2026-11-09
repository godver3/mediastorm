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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Pluto TV's stitcher splices programme and ad clips that use different MPEG-TS layouts: the
// programme has its PMT on PID 0x100 with audio 0x101 and video 0x102, the ad slate its PMT on
// 0x1000 with video 0x100 and audio 0x101. FFmpeg announces the ad's video as a new stream it never
// maps, so the live session must restart FFmpeg rather than carry on with audio only.
func TestLiveTranscodingRestartsWhenInputStreamLayoutChanges(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()

	generate := func(name, size string, pmtPID, startPID string, videoFirst bool) []byte {
		t.Helper()
		path := filepath.Join(dir, name)
		args := []string{"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=25",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-t", "4", "-c:v", "libx264", "-g", "25", "-c:a", "aac"}
		if videoFirst {
			args = append(args, "-map", "0:v", "-map", "1:a")
		} else {
			args = append(args, "-map", "1:a", "-map", "0:v")
		}
		args = append(args, "-mpegts_pmt_start_pid", pmtPID, "-mpegts_start_pid", startPID, "-f", "mpegts", path)
		if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
			t.Skipf("FFmpeg cannot generate the fixture (%v): %s", err, output)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	programme := generate("programme.ts", "320x180", "0x100", "0x101", false)
	ad := generate("ad.ts", "256x144", "0x1000", "0x100", true)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			w.Header().Set("Content-Type", "application/x-mpegurl; charset=utf-8")
			io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n"+
				"#EXTINF:4,\nprogramme0.ts\n#EXTINF:4,\nprogramme1.ts\n"+
				"#EXT-X-DISCONTINUITY\n#EXTINF:4,\nad0.ts\n#EXTINF:4,\nad1.ts\n#EXTINF:4,\nad2.ts\n#EXT-X-ENDLIST\n")
		case "/programme0.ts", "/programme1.ts":
			w.Write(programme)
		case "/ad0.ts", "/ad1.ts", "/ad2.ts":
			w.Write(ad)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	session := &HLSSession{
		ID: "layout-live", Path: upstream.URL + "/master.m3u8", LiveProvider: "m3u",
		OutputDir: filepath.Join(dir, "output"), PlaybackTarget: "native", IsLive: true,
		LastSegmentRequest: time.Now(),
	}
	m := &HLSManager{ffmpegPath: ffmpeg}
	err = m.startLiveTranscoding(ctx, session, 0)
	if !session.liveLayout.take() {
		t.Fatalf("FFmpeg was not restarted for the ad's new stream layout (run ended with %v)", err)
	}
	if err == nil {
		t.Fatal("the run stopped for the layout change reported a normal completion")
	}
}

// A layout-change restart is not an upstream failure: the supervisor rebuilds at once and does not
// spend liveMaxRestarts, so a long ad break of differently muxed clips cannot end the session.
func TestSuperviseLiveTranscodingRebuildsLayoutChangesWithoutFailureBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg shell script is POSIX-only")
	}
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "ffmpeg")
	body := fmt.Sprintf("#!/bin/sh\necho run >> %q\necho '[in#0/hls @ 0x1] New video stream with index 2 at pos:1 and DTS:1.4s' >&2\nexec sleep 30\n", runs)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &HLSSession{
		ID: "layout-supervisor", Path: "http://provider.invalid/live.m3u8", LiveProvider: "m3u",
		OutputDir: filepath.Join(dir, "output"), PlaybackTarget: "native", IsLive: true,
		LastSegmentRequest: time.Now(), SegmentRequestCount: 1,
	}
	m := &HLSManager{ffmpegPath: script}
	var finished atomic.Bool
	go func() {
		_ = m.superviseLiveTranscoding(ctx, session)
		finished.Store(true)
	}()

	// liveLayoutRestartLimit restarts, then one more run the cap leaves alone. With the failure
	// budget's growing delays this would take well over a minute, and it would give up after five.
	want := liveLayoutRestartLimit + 1
	deadline := time.Now().Add(20 * time.Second)
	for {
		count := 0
		if data, err := os.ReadFile(runs); err == nil {
			count = strings.Count(string(data), "run")
		}
		if count >= want {
			break
		}
		if finished.Load() {
			t.Fatalf("supervisor gave up after %d runs", count)
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d FFmpeg runs in 20s, want %d immediate layout rebuilds", count, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Past the cap the run is left going rather than cycled again.
	time.Sleep(time.Second)
	data, _ := os.ReadFile(runs)
	if count := strings.Count(string(data), "run"); count != want {
		t.Fatalf("FFmpeg runs = %d after the cap, want %d", count, want)
	}
	if finished.Load() {
		t.Fatal("supervisor ended while the capped run was still going")
	}
}

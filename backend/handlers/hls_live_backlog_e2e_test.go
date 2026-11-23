package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLiveTimeshiftBacklogEndToEnd tunes a timeshift session to a synthetic HLS channel with a DVR
// window and audio as a separate rendition (like ARD/ZDF): the session must list the source's
// window before the tune-in, play a backlog segment muxed from the source's video and audio with
// the source timeline, and continue with its own live encode. Needs ffmpeg/ffprobe; runs only when
// MEDIASTORM_FFMPEG_E2E=1.
func TestLiveTimeshiftBacklogEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	// 70 two-second segments of video and of audio, cut on the same boundaries.
	src := t.TempDir()
	for _, dir := range []string{"v", "a"} {
		if err := os.MkdirAll(filepath.Join(src, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-t", "140", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25",
		"-t", "140", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-map", "0:v", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "0", "-hls_segment_filename", filepath.Join(src, "v", "v%d.ts"), filepath.Join(src, "v", "full.m3u8"),
		"-map", "1:a", "-c:a", "aac", "-ac", "2",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "0", "-hls_segment_filename", filepath.Join(src, "a", "a%d.ts"), filepath.Join(src, "a", "full.m3u8"))
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v\n%s", err, out)
	}

	// The channel lists 45 segments (90 s) at the tune-in and one more every two seconds.
	start := time.Now()
	var requests atomic.Int64
	media := func(prefix string) string {
		visible := min(70, 45+int(time.Since(start)/(2*time.Second)))
		lines := []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:2", "#EXT-X-MEDIA-SEQUENCE:0"}
		for i := 0; i < visible; i++ {
			lines = append(lines, "#EXTINF:2.000,", fmt.Sprintf("%s%d.ts", prefix, i))
		}
		return strings.Join(lines, "\n") + "\n"
	}
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/master.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n"+
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"TV\",LANGUAGE=\"deu\",DEFAULT=YES,URI=\"a/live.m3u8\"\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=320x180,CODECS=\"avc1.42c00d,mp4a.40.2\",AUDIO=\"aud\"\n"+
				"v/live.m3u8\n")
		case "/v/live.m3u8":
			_, _ = fmt.Fprint(w, media("v"))
		case "/a/live.m3u8":
			_, _ = fmt.Fprint(w, media("a"))
		default:
			http.ServeFile(w, r, filepath.Join(src, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/"))))
		}
	}))
	defer channel.Close()

	dir := t.TempDir()
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	session, err := m.CreateLiveSession(context.Background(), channel.URL+"/master.m3u8", "m3u", "m3u:test", "p", "p",
		"127.0.0.1", "native", LiveTuningSettings{TimeshiftWindow: 40 * time.Second}, false)
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	defer m.CleanupSession(session.ID)

	playlist := func() string {
		rec := httptest.NewRecorder()
		m.ServePlaylist(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/stream.m3u8", nil), session.ID)
		return rec.Body.String()
	}
	backlog := 0
	deadline := time.Now().Add(45 * time.Second)
	for {
		if backlog = session.liveBacklogEnd(); backlog > 0 && strings.Contains(playlist(), fmt.Sprintf("segment%d.ts", backlog)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live segment after the backlog (backlog=%d); playlist:\n%s", backlog, playlist())
		}
		time.Sleep(500 * time.Millisecond)
	}
	// The 40 s timeshift window, well inside the 90 s the source lists.
	if backlog != 20 {
		t.Errorf("backlog of %d segments, want 20 (40 s)", backlog)
	}
	listed := playlist()
	if !strings.Contains(listed, fmt.Sprintf("#EXT-X-DISCONTINUITY\n#EXTINF:")) || !strings.Contains(listed, "segment0.ts") {
		t.Fatalf("playlist does not list the backlog before the live encode:\n%s", listed)
	}
	if _, err := os.Stat(filepath.Join(session.OutputDir, "segment5.ts")); !os.IsNotExist(err) {
		t.Fatal("a backlog segment was made before anyone asked for it")
	}

	fetch := func(name string) string {
		rec := httptest.NewRecorder()
		m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/"+name, nil), session.ID, name)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", name, rec.Code, rec.Body.String())
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, rec.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	type probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			StartTime string `json:"start_time"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	probeFile := func(path string) (video, audio bool, startTime, duration float64) {
		out, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", path, err)
		}
		var p probe
		if err := json.Unmarshal(out, &p); err != nil {
			t.Fatal(err)
		}
		for _, s := range p.Streams {
			switch s.CodecType {
			case "video":
				video = true
				startTime, _ = strconv.ParseFloat(s.StartTime, 64)
			case "audio":
				audio = true
			}
		}
		duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
		return
	}

	v5, a5, start5, dur5 := probeFile(fetch("segment5.ts"))
	if !v5 || !a5 {
		t.Fatalf("backlog segment5: video=%v audio=%v", v5, a5)
	}
	if dur5 < 1.5 || dur5 > 2.5 {
		t.Errorf("backlog segment5 lasts %.2f s, want about 2", dur5)
	}
	// The next one follows on the source timeline (it was made ahead of the request).
	_, _, start6, _ := probeFile(fetch("segment6.ts"))
	if gap := start6 - start5; gap < 1.9 || gap > 2.1 {
		t.Errorf("segment6 starts %.3f s after segment5, want 2 s (source timeline)", gap)
	}
	if v, a, _, _ := probeFile(fetch(fmt.Sprintf("segment%d.ts", backlog))); !v || !a {
		t.Errorf("first live segment: video=%v audio=%v", v, a)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	// The web player gets the same backlog: the source is browser-safe H.264, so its live encode
	// copies the video too (hls_live_web_copy.go). ARD and ZDF send their audio rendition as packed
	// audio (ADTS AAC behind an ID3 timestamp) rather than MPEG-TS.
	for _, tc := range []struct {
		name, target string
		packed       bool
	}{{"native", "native", false}, {"web", "web", false}, {"web-packed-audio", "web", true}} {
		t.Run(tc.name, func(t *testing.T) { liveTimeshiftBacklogEndToEnd(t, tc.target, tc.packed) })
	}
}

func liveTimeshiftBacklogEndToEnd(t *testing.T, target string, packed bool) {
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
	audioExt := "ts"
	if packed {
		// Each audio segment as packed audio: its ADTS frames behind an ID3 tag with the segment's
		// MPEG-TS start timestamp, as HLS packagers write it.
		audioExt = "aac"
		for i := 0; i < 70; i++ {
			seg := filepath.Join(src, "a", fmt.Sprintf("a%d.ts", i))
			startOut, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=start_time", "-of", "csv=p=0", seg).Output()
			if err != nil {
				t.Fatalf("probe audio segment %d: %v", i, err)
			}
			startTime, err := strconv.ParseFloat(strings.Fields(string(startOut) + " x")[0], 64)
			if err != nil {
				t.Fatalf("audio segment %d start %q: %v", i, startOut, err)
			}
			adts, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", seg, "-c:a", "copy", "-f", "adts", "-").Output()
			if err != nil {
				t.Fatalf("audio segment %d to ADTS: %v", i, err)
			}
			data := append(packedAudioID3(4, uint64(startTime*90000+0.5)), adts...)
			if err := os.WriteFile(filepath.Join(src, "a", fmt.Sprintf("a%d.aac", i)), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The channel lists 45 segments (90 s) at the tune-in and one more every two seconds.
	start := time.Now()
	var requests atomic.Int64
	media := func(prefix, ext string) string {
		visible := min(70, 45+int(time.Since(start)/(2*time.Second)))
		lines := []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:2", "#EXT-X-MEDIA-SEQUENCE:0"}
		for i := 0; i < visible; i++ {
			lines = append(lines, "#EXTINF:2.000,", fmt.Sprintf("%s%d.%s", prefix, i, ext))
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
			_, _ = fmt.Fprint(w, media("v", "ts"))
		case "/a/live.m3u8":
			_, _ = fmt.Fprint(w, media("a", audioExt))
		default:
			http.ServeFile(w, r, filepath.Join(src, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/"))))
		}
	}))
	defer channel.Close()

	dir := t.TempDir()
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	session, err := m.CreateLiveSession(context.Background(), channel.URL+"/master.m3u8", "m3u", "m3u:test", "p", "p",
		"127.0.0.1", target, LiveTuningSettings{TimeshiftWindow: 40 * time.Second, WebVideoCopy: target == "web"}, false)
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

	seg5 := fetch("segment5.ts")
	v5, a5, start5, dur5 := probeFile(seg5)
	if !v5 || !a5 {
		t.Fatalf("backlog segment5: video=%v audio=%v", v5, a5)
	}
	// The audio sits on the video's timeline (for packed audio: by its ID3 timestamp).
	audioStartOut, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=start_time", "-of", "csv=p=0", seg5).Output()
	if err != nil {
		t.Fatalf("probe segment5 audio: %v", err)
	}
	if audioStart, err := strconv.ParseFloat(strings.Fields(string(audioStartOut) + " x")[0], 64); err != nil || math.Abs(audioStart-start5) > 0.1 {
		t.Errorf("backlog segment5 audio starts at %q, video at %.3f: not lined up", strings.TrimSpace(string(audioStartOut)), start5)
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
	session.mu.RLock()
	encoder := session.VideoEncoder
	session.mu.RUnlock()
	if encoder != "copy" {
		t.Errorf("%s live encode: video encoder %q, want copy", target, encoder)
	}
	if target != "web" || packed {
		return
	}

	// A browser that cannot decode the copy has the session encode instead: the live encode is
	// rebuilt at once, its numbering continuing, and every copied segment (backlog and live) leaves
	// the playlist, which then starts with the encoded part.
	switched, encodedFrom, err := m.ForceLiveVideoEncode(session.ID)
	if err != nil || !switched {
		t.Fatalf("force encode: switched=%v err=%v", switched, err)
	}
	if encodedFrom <= backlog {
		t.Fatalf("encodedFrom=%d, want past the backlog (%d) and the copied live segments", encodedFrom, backlog)
	}
	if again, _, err := m.ForceLiveVideoEncode(session.ID); err != nil || again {
		t.Fatalf("second force encode: switched=%v err=%v, want false", again, err)
	}
	mediaSequence := func(listed string) int {
		for _, line := range strings.Split(listed, "\n") {
			if v, ok := strings.CutPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"); ok {
				n, _ := strconv.Atoi(strings.TrimSpace(v))
				return n
			}
		}
		return -1
	}
	var switchedList string
	deadline = time.Now().Add(30 * time.Second)
	for {
		session.mu.RLock()
		encoder = session.VideoEncoder
		session.mu.RUnlock()
		switchedList = playlist()
		if encoder == "libx264" && mediaSequence(switchedList) >= encodedFrom && strings.Count(switchedList, "#EXTINF:") >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no encoded-only playlist after the switch (encoder %q, encodedFrom %d); playlist:\n%s", encoder, encodedFrom, switchedList)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for n := 0; n < encodedFrom; n++ {
		if strings.Contains(switchedList, fmt.Sprintf("segment%d.ts\n", n)) {
			t.Fatalf("copied segment%d.ts still listed after the switch:\n%s", n, switchedList)
		}
	}
	if !strings.Contains(switchedList, "#EXT-X-START:TIME-OFFSET=-") {
		t.Errorf("no start tag after the switch:\n%s", switchedList)
	}
	last := nextLiveSegmentNumber(session.OutputDir) - 1
	if v, a, _, _ := probeFile(fetch(fmt.Sprintf("segment%d.ts", last))); !v || !a {
		t.Errorf("encoded live segment: video=%v audio=%v", v, a)
	}
}

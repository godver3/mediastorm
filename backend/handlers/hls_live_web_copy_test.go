package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBrowserSafeHLSCodecs(t *testing.T) {
	for _, tc := range []struct {
		codecs      string
		video, safe bool
	}{
		{"avc1.640020,mp4a.40.2", true, true},
		{"avc1.4D401F, mp4a.40.2", true, true},
		{"avc3.42c00d", true, true},
		{"avc1.58A01E", true, false},           // Extended: MSE rejects it
		{"avc1.6e0028,mp4a.40.2", true, false}, // High 10
		{"avc1.7a0028", true, false},           // High 4:2:2
		{"hvc1.2.4.L123.B0,mp4a.40.2", true, false},
		{"mp4a.40.2", false, false},
		{"ec-3", false, false},
		{"avc1", true, false},
		{"avc1.66.30,mp4a.40.2", true, true}, // Apple's legacy decimal form
		{"avc1.100.40", true, true},
		{"avc1.110.40", true, false},
		{"dvh1.05.06,ec-3", true, false},
		{"vvc1.1.L51", true, false},
	} {
		video, safe := browserSafeHLSCodecs(tc.codecs)
		if video != tc.video || safe != tc.safe {
			t.Errorf("%q: video=%v safe=%v, want %v %v", tc.codecs, video, safe, tc.video, tc.safe)
		}
	}
}

func TestLiveWebMasterVideoCopy(t *testing.T) {
	master := func(lines ...string) string { return "#EXTM3U\n" + strings.Join(lines, "\n") + "\n" }
	for _, tc := range []struct {
		name            string
		playlist        string
		decided, copied bool
	}{
		{"ARD-like master", master(
			`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="de",URI="a.m3u8"`,
			`#EXT-X-STREAM-INF:BANDWIDTH=900000,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="aud"`, "low.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=5000000,CODECS="avc1.640020,mp4a.40.2",AUDIO="aud"`, "high.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=64000,CODECS="mp4a.40.2"`, "audio-only.m3u8"), true, true},
		{"one HEVC variant", master(
			`#EXT-X-STREAM-INF:BANDWIDTH=900000,CODECS="avc1.4d401f,mp4a.40.2"`, "low.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=9000000,CODECS="hvc1.2.4.L150.B0,mp4a.40.2"`, "uhd.m3u8"), true, false},
		{"a variant without CODECS", master(
			`#EXT-X-STREAM-INF:BANDWIDTH=900000,CODECS="avc1.4d401f,mp4a.40.2"`, "low.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=5000000`, "high.m3u8"), false, false},
		{"media playlist", "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg1.ts\n", false, false},
		// Video variants mislabelled with only their audio codec: ffprobe has to look.
		{"no variant names video", master(
			`#EXT-X-STREAM-INF:BANDWIDTH=900000,CODECS="mp4a.40.2"`, "low.m3u8",
			`#EXT-X-STREAM-INF:BANDWIDTH=5000000,CODECS="mp4a.40.2"`, "high.m3u8"), false, false},
	} {
		decided, copied := liveWebMasterVideoCopy(tc.playlist)
		if decided != tc.decided || copied != tc.copied {
			t.Errorf("%s: decided=%v copy=%v, want %v %v", tc.name, decided, copied, tc.decided, tc.copied)
		}
	}
}

func TestBrowserSafeProbedVideo(t *testing.T) {
	for _, tc := range []struct {
		out  string
		safe bool
	}{
		{"h264,High,yuv420p,progressive\n", true},
		{"h264,Main,yuvj420p,progressive\nh264,High,yuv420p,progressive\n", true},
		{"h264,High 10,yuv420p10le,progressive\n", false},
		{"h264,High 10,yuv420p,progressive\n", false}, // 8-bit content in a profile browsers do not decode
		{"h264,High 4:4:4 Predictive,yuv420p,progressive\n", false},
		{"h264,Extended,yuv420p,progressive\n", false},
		{"h264,Constrained Baseline,yuv420p,progressive\n", true},
		{"h264,High,yuv420p,progressive\nhevc,Main,yuv420p,progressive\n", false},
		{"mpeg2video,Main,yuv420p,progressive\n", false},
		// Interlaced (or unknown) field order is encoded.
		{"h264,High,yuv420p,tt\n", false},
		{"h264,High,yuv420p,tb\n", false},
		{"h264,High,yuv420p,unknown\n", false},
		{"h264,High,yuv420p\n", false},
		{"", false},
	} {
		if got := browserSafeProbedVideo(tc.out); got != tc.safe {
			t.Errorf("%q: %v, want %v", tc.out, got, tc.safe)
		}
	}
}

func TestLiveWebCopyOutputArgs(t *testing.T) {
	args := strings.Join(liveStereoAACAudio(liveHLSOutputArgsFor(true, "/out/segment%d.ts", "/out/stream.m3u8", 0)), " ")
	for _, want := range []string{"-c:v copy", "-c:a aac -ac 2 -b:a 128k -ar 48000", "-hls_flags temp_file "} {
		if !strings.Contains(args, want) {
			t.Errorf("web copy output lacks %q: %s", want, args)
		}
	}
	for _, unwanted := range []string{"libx264", "-c:a copy", "independent_segments"} {
		if strings.Contains(args, unwanted) {
			t.Errorf("web copy output has %q: %s", unwanted, args)
		}
	}
	// The wrapper keeps choosing by target.
	if web := strings.Join(liveHLSOutputArgs("web", "/out/segment%d.ts", "/out/stream.m3u8"), " "); !strings.Contains(web, "libx264") {
		t.Errorf("liveHLSOutputArgs(web) no longer encodes: %s", web)
	}
}

func TestForceLiveVideoEncode(t *testing.T) {
	m := NewHLSManager(t.TempDir(), "ffmpeg", "ffprobe", nil)
	defer m.Shutdown()
	add := func(id, target string, live bool) *HLSSession {
		s := &HLSSession{ID: id, IsLive: live, PlaybackTarget: target, OutputDir: t.TempDir()}
		m.mu.Lock()
		m.sessions[id] = s
		m.mu.Unlock()
		return s
	}
	if _, _, err := m.ForceLiveVideoEncode("missing"); err != errLiveEncodeNoSession {
		t.Fatalf("unknown session: %v", err)
	}
	add("native", "native", true)
	add("vod", "web", false)
	for _, id := range []string{"native", "vod"} {
		if _, _, err := m.ForceLiveVideoEncode(id); err != errLiveEncodeNotApplicable {
			t.Fatalf("%s: %v, want not applicable", id, err)
		}
	}
	// A web session the web player did not ask to copy (the Jellyfin bridge) already encodes.
	if switched, _, err := m.ForceLiveVideoEncode(add("bridge", "web", true).ID); err != nil || switched {
		t.Fatalf("bridge session: switched=%v err=%v, want false", switched, err)
	}
	// Nor does one whose check never answered (a proxied input, a source slow at tune-in): its
	// run encodes, and stopping it would only spend the restart budget.
	undecided := add("undecided", "web", true)
	undecided.LiveTuning.WebVideoCopy = true
	if switched, _, err := m.ForceLiveVideoEncode(undecided.ID); err != nil || switched || undecided.webEncodePending {
		t.Fatalf("undecided session: switched=%v err=%v pending=%v, want no switch", switched, err, undecided.webEncodePending)
	}
	// A session whose supervisor has ended would never be rebuilt: no switch to wait for.
	ended := add("ended", "web", true)
	ended.LiveTuning.WebVideoCopy = true
	ended.webVideoCopy, ended.webVideoCopyDecided, ended.liveSupervisorDone = true, true, true
	if switched, _, err := m.ForceLiveVideoEncode(ended.ID); err != nil || switched || ended.webEncodePending {
		t.Fatalf("ended session: switched=%v err=%v pending=%v, want no switch", switched, err, ended.webEncodePending)
	}
	web := add("web", "web", true)
	web.LiveTuning.WebVideoCopy = true
	web.webVideoCopy, web.webVideoCopyDecided = true, true
	for _, name := range []string{"segment0.ts", "segment1.ts", "segment2.ts"} {
		if err := os.WriteFile(filepath.Join(web.OutputDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	switched, encodedFrom, err := m.ForceLiveVideoEncode("web")
	if err != nil || !switched || encodedFrom != 3 {
		t.Fatalf("switch: switched=%v encodedFrom=%d err=%v, want true 3", switched, encodedFrom, err)
	}
	if web.webVideoCopy || !web.webVideoCopyDecided || !web.webEncodePending {
		t.Fatalf("decision after the switch: copy=%v decided=%v pending=%v", web.webVideoCopy, web.webVideoCopyDecided, web.webEncodePending)
	}
	if again, _, err := m.ForceLiveVideoEncode("web"); err != nil || again {
		t.Fatalf("second switch: %v %v", again, err)
	}
}

func TestLivePlaylistDropsSegmentsCopiedBeforeTheEncode(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Hour}}
	onDisk := func(from, count int) string {
		lines := []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:6", fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", from)}
		for i := from; i < from+count; i++ {
			if i == 35 || i == 40 { // a source discontinuity among the copies; the encode's start
				lines = append(lines, "#EXT-X-DISCONTINUITY")
			}
			lines = append(lines, "#EXTINF:6.000000,", fmt.Sprintf("segment%d.ts", i))
		}
		return strings.Join(lines, "\n") + "\n"
	}
	// 40 copied segments are listed; then the session switches and its rebuild starts at 40.
	if out := m.buildSeamlessLivePlaylist(session, onDisk(0, 40)); !strings.Contains(out, "segment0.ts") {
		t.Fatalf("copied window not listed:\n%s", out)
	}
	session.mu.Lock()
	session.liveEncodedFrom = 40
	session.mu.Unlock()
	// Before the encode wrote anything (the copy run's playlist still on disk, read again and
	// again while the player waits): an empty playlist, not the copies, and the discontinuity
	// dropped with them counted once.
	for i := 0; i < 3; i++ {
		empty := m.buildSeamlessLivePlaylist(session, onDisk(30, 10))
		if strings.Contains(empty, "#EXTINF") || !strings.Contains(empty, "#EXT-X-MEDIA-SEQUENCE:40\n") ||
			!strings.Contains(empty, "#EXT-X-DISCONTINUITY-SEQUENCE:1\n") {
			t.Fatalf("playlist %d before the first encoded segment:\n%s", i, empty)
		}
	}
	out := m.buildSeamlessLivePlaylist(session, onDisk(40, 3))
	if strings.Contains(out, "segment39.ts") || !strings.Contains(out, "#EXT-X-MEDIA-SEQUENCE:40") || !strings.Contains(out, "segment42.ts") {
		t.Fatalf("playlist after the switch:\n%s", out)
	}
	if !strings.Contains(out, "#EXT-X-DISCONTINUITY-SEQUENCE:1\n#EXT-X-DISCONTINUITY\n#EXTINF:6.000000,\nsegment40.ts") {
		t.Fatalf("the encoded part does not continue the discontinuity sequence:\n%s", out)
	}
	if tag := livePlaylistStartTagAfterBacklog(out, 40); tag == "" {
		t.Fatalf("no start tag inside the encoded part:\n%s", out)
	}
}

// A live player's keepalive reports watch time: mapped to 2 s segments it would run ahead of a
// copied stream's longer segments, and segment cleanup would delete what the player still needs.
func TestLiveKeepAliveDoesNotMapWatchTimeToSegments(t *testing.T) {
	session := &HLSSession{
		ID: "live", IsLive: true, PlaybackTarget: "web", LastSegmentRequest: time.Now(),
		MaxSegmentRequested: 66, LastPlaybackSegment: -1, EarliestBufferedSegment: -1,
	}
	m := &HLSManager{sessions: map[string]*HLSSession{session.ID: session}}
	rec := httptest.NewRecorder()
	m.KeepAlive(rec, httptest.NewRequest(http.MethodPost, "/keepalive?time=400&bufferStart=380", nil), session.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("keepalive: %d", rec.Code)
	}
	if session.MaxSegmentRequested != 66 || session.LastPlaybackSegment != -1 || session.EarliestBufferedSegment != -1 {
		t.Fatalf("live keepalive moved the segment marks: max=%d last=%d earliest=%d",
			session.MaxSegmentRequested, session.LastPlaybackSegment, session.EarliestBufferedSegment)
	}
	if session.PlaybackPosition != 400 {
		t.Fatalf("playback position %v, want 400", session.PlaybackPosition)
	}
}

// A probe decides for the session whatever it finds: a failing or empty probe decides "encode",
// so a restart does not probe again. A non-HLS input is never probed (a second connection to a
// provider that may allow one).
func TestLiveWebVideoCopyAnswers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffprobe shell script is POSIX-only")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "probed")
	probe := func(name, body string) *HLSManager {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch "+marker+"\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return &HLSManager{ffprobePath: path}
	}
	for _, tc := range []struct {
		name, body       string
		copied, answered bool
	}{
		{"safe", "echo h264,High,yuv420p,progressive", true, true},
		{"interlaced", "echo h264,High,yuv420p,tt", false, true},
		{"unsafe", "echo hevc,Main,yuv420p,progressive", false, true},
		{"failing", "exit 1", false, true},
		{"empty", "exit 0", false, true},
	} {
		copied, answered := probe(tc.name, tc.body).liveWebVideoCopy(context.Background(), "s", "", nil, "http://provider.invalid/live.m3u8", nil, true)
		if copied != tc.copied || answered != tc.answered {
			t.Errorf("%s: copy=%v answered=%v, want %v %v", tc.name, copied, answered, tc.copied, tc.answered)
		}
	}
	_ = os.Remove(marker)
	copied, answered := probe("ts", "echo h264,High,yuv420p,progressive").liveWebVideoCopy(context.Background(), "s", "", nil, "http://provider.invalid/live.ts", nil, false)
	if copied || !answered {
		t.Errorf("non-HLS input: copy=%v answered=%v, want false true", copied, answered)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("non-HLS input was probed")
	}
	// A session stopping meanwhile leaves the decision open.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, answered := probe("cancelled", "exit 1").liveWebVideoCopy(ctx, "s", "", nil, "http://provider.invalid/live.m3u8", nil, true); answered {
		t.Errorf("cancelled check answered")
	}
}

// A paused timeshift player in a hidden browser tab keeps alive about once a minute: its session
// must not be stopped after hlsIdleTimeout.
func TestLiveIdleTimeoutForPausedTimeshift(t *testing.T) {
	session := &HLSSession{IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: 30 * time.Minute}}
	if got := liveIdleTimeout(session); got != hlsIdleTimeout {
		t.Fatalf("playing timeshift session: %v, want %v", got, hlsIdleTimeout)
	}
	session.PlaybackPaused = true
	session.lastKeepAlive = time.Now().Add(-time.Minute)
	if got := liveIdleTimeout(session); got != liveIdleTimeoutPaused || got < 3*time.Minute {
		t.Fatalf("paused timeshift session: %v, want %v", got, liveIdleTimeoutPaused)
	}
	// A tab closed while paused sends no more keepalives: the normal timeout applies again.
	session.lastKeepAlive = time.Now().Add(-liveKeepAliveFresh - time.Second)
	if got := liveIdleTimeout(session); got != hlsIdleTimeout {
		t.Fatalf("paused timeshift session without keepalives: %v, want %v", got, hlsIdleTimeout)
	}
	if got := liveIdleTimeout(&HLSSession{IsLive: true, PlaybackPaused: true}); got != hlsIdleTimeout {
		t.Fatalf("paused session without timeshift: %v, want %v", got, hlsIdleTimeout)
	}
}

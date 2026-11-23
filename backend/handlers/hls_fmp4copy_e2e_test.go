package handlers

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStableCopyFMP4EndToEnd runs a real stable copy session in fMP4 on a
// synthetic HEVC Main 10 HDR10 + E-AC-3 5.1 source: the plan, FFmpeg, the
// master/media playlists, init.mp4, segments from the start and after a
// seek (a restarted run), and checks the reassembled fragments with
// ffprobe/ffmpeg. It needs ffmpeg with libx265 and only runs when
// MEDIASTORM_FFMPEG_E2E=1 (e.g. inside the mediastorm container).
func TestStableCopyFMP4EndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "90", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "log-level=error:keyint=48:min-keyint=48:bframes=3:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
		"-c:a", "eac3", "-ac", "6", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/src.mkv"

	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	caps := &HLSClientCaps{ // Edge with an HDR display
		TSVideo: []string{"h264"}, TSAudio: []string{"aac", "ac3", "eac3"},
		FMP4Video: []string{"hevc", "h264"}, FMP4Audio: []string{"aac", "ac3", "eac3"},
		VideoRanges: map[string][]string{"hevc": {"SDR", "HDR10", "HDR10Plus", "HLG"}},
	}
	ctx := withHLSClientCaps(context.Background(), caps)
	session, err := m.CreateSession(ctx, sourceURL, sourceURL, false, "", true, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 90, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer m.CleanupSession(session.ID)
	if !session.usesStableCopyTimeline() || !session.CopyPlan.FMP4 || session.CopyPlan.OutputRange != "PQ" || session.CopyPlan.Tag != "hvc1" {
		t.Fatalf("plan = %+v stable=%v", session.CopyPlan, session.usesStableCopyTimeline())
	}

	get := func(name string, serve func(http.ResponseWriter, *http.Request)) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		serve(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/"+name+"?token=tok", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	segment := func(name string) []byte {
		return get(name, func(w http.ResponseWriter, r *http.Request) { m.ServeSegment(w, r, session.ID, name) })
	}

	master := string(get("master.m3u8", func(w http.ResponseWriter, r *http.Request) { m.ServeMasterPlaylist(w, r, session.ID) }))
	if !strings.Contains(master, `CODECS="hvc1.2.4.`) || !strings.Contains(master, `ec-3"`) || !strings.Contains(master, "VIDEO-RANGE=PQ") || !strings.Contains(master, "stream.m3u8?token=tok") {
		t.Fatalf("master playlist:\n%s", master)
	}
	media := string(get("stream.m3u8", func(w http.ResponseWriter, r *http.Request) { m.ServePlaylist(w, r, session.ID) }))
	if !strings.Contains(media, `#EXT-X-MAP:URI="init.mp4?token=tok"`) || !strings.Contains(media, "segment0.m4s?token=tok") || !strings.Contains(media, "#EXT-X-VERSION:7") {
		t.Fatalf("media playlist:\n%s", media)
	}

	init := segment("init.mp4")
	seek := len(session.CopySegmentStarts) - 3 // far enough to force a restarted run
	order := []int{0, 1, 2, seek, seek + 1}
	parts := map[int][]byte{}
	for _, n := range order {
		parts[n] = segment(fmt.Sprintf("segment%d.m4s", n))
	}

	// Reassembled from the start: one continuous timeline from 0 with the
	// source's codecs, HDR signalling and decodable frames.
	head := filepath.Join(dir, "head.mp4")
	writeConcat(t, head, init, parts[0], parts[1], parts[2])
	streams := ffprobeOut(t, "-show_entries", "stream=codec_name,codec_tag_string,color_transfer,channels", "-of", "csv=p=0", head)
	if !strings.Contains(streams, "hevc,hvc1,smpte2084") || !strings.Contains(streams, "eac3") {
		t.Fatalf("streams:\n%s", streams)
	}
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", head, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("decode head: %v %s", err, out)
	}
	if first := firstVideoPTS(t, head); first > 0.5 {
		t.Fatalf("head starts at %.3f", first)
	}

	// After the seek: the restarted run's fragments sit at their planned
	// position on the same timeline, behind the same init segment.
	tail := filepath.Join(dir, "tail.mp4")
	writeConcat(t, tail, init, parts[seek], parts[seek+1])
	want := session.CopySegmentStarts[seek]
	if first := firstVideoPTS(t, tail); math.Abs(first-want) > 0.5 {
		t.Fatalf("segment %d starts at %.3f, planned %.3f", seek, first, want)
	}
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", tail, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("decode tail: %v %s", err, out)
	}
	session.mu.RLock()
	mismatch := session.copyInitMismatch
	session.mu.RUnlock()
	if mismatch {
		t.Fatal("restarted run produced a different init segment")
	}
}

func writeConcat(t *testing.T, path string, parts ...[]byte) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Join(parts, nil), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ffprobeOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", append([]string{"-v", "error"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %v: %v %s", args, err, out)
	}
	return string(out)
}

func firstVideoPTS(t *testing.T, path string) float64 {
	t.Helper()
	out := ffprobeOut(t, "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-read_intervals", "%+#1", "-of", "csv=p=0", path)
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.Split(out, "\n")[0]), 64)
	if err != nil {
		t.Fatalf("pts %q: %v", out, err)
	}
	return v
}

// TestStableCopyHDRVariantsEndToEnd covers the other HDR decisions on the
// same kind of source: a TV browser that takes HEVC HDR10 in MPEG-TS gets a
// copy (VIDEO-RANGE=PQ, no fMP4), and an SDR-only client gets no copy.
func TestStableCopyHDRVariantsEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "30", "-map", "0:v", "-map", "1:a", "-c:a", "aac",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "log-level=error:keyint=48:min-keyint=48:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/src.mkv"
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)

	tizen := &HLSClientCaps{TSVideo: []string{"h264", "hevc"}, TSAudio: []string{"aac"}, VideoRanges: map[string][]string{"hevc": {"SDR", "HDR10"}}}
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), tizen), sourceURL, sourceURL, false, "", true, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 30, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.CleanupSession(session.ID)
	if !session.usesStableCopyTimeline() || session.CopyPlan.FMP4 || session.CopyPlan.OutputRange != "PQ" {
		t.Fatalf("tizen plan = %+v", session.CopyPlan)
	}
	rec := httptest.NewRecorder()
	m.ServeMasterPlaylist(rec, httptest.NewRequest(http.MethodGet, "/master.m3u8", nil), session.ID)
	if !strings.Contains(rec.Body.String(), "VIDEO-RANGE=PQ") || strings.Contains(rec.Body.String(), "CODECS") {
		t.Fatalf("tizen master:\n%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/segment0.ts", nil), session.ID, "segment0.ts")
	if rec.Code != http.StatusOK {
		t.Fatalf("segment0.ts: %d", rec.Code)
	}
	ts := filepath.Join(dir, "seg0.ts")
	writeConcat(t, ts, rec.Body.Bytes())
	if streams := ffprobeOut(t, "-show_entries", "stream=codec_name,color_transfer", "-of", "csv=p=0", ts); !strings.Contains(streams, "hevc,smpte2084") {
		t.Fatalf("tizen segment: %s", streams)
	}

	sdrOnly := &HLSClientCaps{TSVideo: []string{"h264"}, FMP4Video: []string{"hevc", "h264"}}
	fallback, err := m.CreateSession(withHLSClientCaps(context.Background(), sdrOnly), sourceURL, sourceURL, false, "", true, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 30, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.CleanupSession(fallback.ID)
	if fallback.usesStableCopyTimeline() {
		t.Fatalf("HDR copied for an SDR-only client: %+v", fallback.CopyPlan)
	}
}

// TestStableCopyAV1EndToEnd checks that an AV1 copy plan really copies (the
// fragments carry AV1, not a libx264 re-encode).
func TestStableCopyAV1EndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4") // MP4: exact timestamps (MKV rounds to ms)
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "20", "-map", "0:v", "-map", "1:a",
		"-c:v", "libsvtav1", "-preset", "12", "-g", "48", "-svtav1-params", "verbosity=0", "-c:a", "aac", "-video_track_timescale", "90000", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/src.mp4"
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", src, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Skipf("synthetic AV1 source does not decode cleanly here: %v %s", err, out)
	}
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	caps := &HLSClientCaps{TSVideo: []string{"h264"}, FMP4Video: []string{"hevc", "h264", "av1"}, FMP4Audio: []string{"aac"}}
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), sourceURL, sourceURL, false, "", false, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.CleanupSession(session.ID)
	if session.CopyPlan.Codec != "av1" || !session.CopyPlan.FMP4 || session.CopyPlan.Tag != "av01" {
		t.Fatalf("av1 plan = %+v", session.CopyPlan)
	}
	serve := func(name string) []byte {
		rec := httptest.NewRecorder()
		m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/"+name, nil), session.ID, name)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	head := filepath.Join(dir, "head.mp4")
	writeConcat(t, head, serve("init.mp4"), serve("segment0.m4s"), serve("segment1.m4s"))
	if streams := ffprobeOut(t, "-show_entries", "stream=codec_name,codec_tag_string", "-of", "csv=p=0", head); !strings.Contains(streams, "av1,av01") {
		t.Fatalf("av1 fragments: %s", streams)
	}
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", head, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("decode av1: %v %s", err, out)
	}
}

// TestForcedCopyEndToEnd covers "Disable Transcoding": HEVC HDR10 with a
// TrueHD track for a client that declared only H.264/AAC is still copied,
// video and audio.
func TestForcedCopyEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "20", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "log-level=error:keyint=48:min-keyint=48:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc",
		"-c:a", "truehd", "-strict", "experimental", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode the TrueHD source here: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/src.mkv"
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	caps := &HLSClientCaps{ForceVideoCopy: true, ForceAudioCopy: true, TSVideo: []string{"h264"}, TSAudio: []string{"aac"}}
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), sourceURL, sourceURL, false, "", true, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.CleanupSession(session.ID)
	if !session.usesStableCopyTimeline() || session.CopyPlan.FMP4 {
		t.Fatalf("forced plan = %+v", session.CopyPlan)
	}
	rec := httptest.NewRecorder()
	m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/segment0.ts", nil), session.ID, "segment0.ts")
	if rec.Code != http.StatusOK {
		t.Fatalf("segment0.ts: %d", rec.Code)
	}
	ts := filepath.Join(dir, "seg0.ts")
	writeConcat(t, ts, rec.Body.Bytes())
	streams := ffprobeOut(t, "-show_entries", "stream=codec_name,color_transfer", "-of", "csv=p=0", ts)
	if !strings.Contains(streams, "hevc,smpte2084") || !strings.Contains(streams, "truehd") {
		t.Fatalf("forced copy segment: %s", streams)
	}
}

// TestWebPlayerStableCopyOrFallback covers the /watch web player's request:
// web-stable with its capabilities and webFallback. A browser that plays
// HEVC HDR gets a stable copy that starts at the resume point's segment; one
// that does not gets an ordinary web session, not the stable Cast transcode.
func TestWebPlayerStableCopyOrFallback(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	srt := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:05,000 --> 00:00:07,000\nearly\n\n2\n00:00:40,000 --> 00:00:42,000\nlate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", srt,
		"-t", "60", "-map", "0:v", "-map", "1:a", "-map", "2:s", "-c:s", "srt",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "log-level=error:keyint=48:min-keyint=48:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc",
		"-c:a", "aac", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/src.mkv"
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)

	hevc := &HLSClientCaps{ // Chrome with HEVC Main 10
		TSVideo: []string{"h264"}, TSAudio: []string{"aac"},
		FMP4Video: []string{"h264", "hevc"}, FMP4Audio: []string{"aac"},
		VideoRanges: map[string][]string{"h264": {"SDR"}, "hevc": {"SDR", "HDR10", "HDR10Plus", "HLG"}},
		WebFallback: true,
	}
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), hevc), sourceURL, sourceURL, false, "", true, true, 30, 30, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 60, "")
	if err != nil {
		t.Fatal(err)
	}
	if !session.usesStableCopyTimeline() || session.CopyPlan.Tag != "hvc1" || session.CastMode || session.SegmentStartNumber == 0 {
		t.Fatalf("hevc browser: stable=%v plan=%+v cast=%v start segment=%d", session.usesStableCopyTimeline(), session.CopyPlan, session.CastMode, session.SegmentStartNumber)
	}
	// The player seeks anywhere inside a stable copy: its subtitles cover
	// the whole title on the title clock, though the session started at 30s.
	vtt := filepath.Join(dir, "subs.vtt")
	if err := m.extractSubtitleTrackToVTT(context.Background(), session, 2, vtt); err != nil {
		t.Fatal(err)
	}
	cues, _ := os.ReadFile(vtt)
	// (Cues keep the container's start time, a few ms.)
	if !strings.Contains(string(cues), "00:05.0") || !strings.Contains(string(cues), "00:40.0") {
		t.Fatalf("stable copy subtitles:\n%s", cues)
	}
	if offset, ok := session.subtitleExtractionOffset(2); !ok || offset != 0 {
		t.Fatalf("subtitle extraction offset = %v %v, want 0", offset, ok)
	}
	m.CleanupSession(session.ID)

	h264Only := &HLSClientCaps{
		TSVideo: []string{"h264"}, TSAudio: []string{"aac"},
		FMP4Video: []string{"h264"}, FMP4Audio: []string{"aac"},
		VideoRanges: map[string][]string{"h264": {"SDR"}},
		WebFallback: true,
	}
	session, err = m.CreateSession(withHLSClientCaps(context.Background(), h264Only), sourceURL, sourceURL, false, "", true, true, 30, 30, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 60, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.CleanupSession(session.ID)
	if session.usesStableCopyTimeline() || session.CastMode || session.PlaybackTarget != "web" || session.StartOffset != 30 {
		t.Fatalf("h264-only browser: stable=%v cast=%v target=%q start=%v", session.usesStableCopyTimeline(), session.CastMode, session.PlaybackTarget, session.StartOffset)
	}
}

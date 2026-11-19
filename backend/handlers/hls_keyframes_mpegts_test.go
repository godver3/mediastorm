package handlers

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tsBuild assembles a synthetic transport stream: PAT, PMT (audio before
// video, as DVB muxes often list them), then one video PES per frame
// interleaved with audio PES.
type tsBuild struct {
	fps        int
	gop        int
	frames     int
	startPTS   int64
	openGOP    bool // keyframes are non-IDR I-frames with a recovery point SEI
	raiOnAll   bool // set the random access indicator on every video PES
	m2ts       bool // 192-byte packets with a timecode prefix
	frameBytes int
}

const (
	tsTestPMTPID   = 0x100
	tsTestVideoPID = 0x101
	tsTestAudioPID = 0x102
)

func tsPTSBytes(pts int64) []byte {
	pts &= tsPTSWrap - 1
	return []byte{
		0x21 | byte((pts>>29)&0x0E),
		byte(pts >> 22),
		byte((pts>>14)&0xFE) | 1,
		byte(pts >> 7),
		byte((pts<<1)&0xFE) | 1,
	}
}

func (b tsBuild) packetize(out *bytes.Buffer, pid int, payload []byte, rai bool) {
	for first := true; first || len(payload) > 0; first = false {
		pkt := make([]byte, 188)
		pkt[0] = tsSyncByte
		pkt[1] = byte(pid >> 8 & 0x1F)
		if first {
			pkt[1] |= 0x40
		}
		pkt[2] = byte(pid)
		withRAI := first && rai
		if !withRAI && len(payload) >= 184 {
			pkt[3] = 0x10
			copy(pkt[4:], payload[:184])
			payload = payload[184:]
			b.emit(out, pkt)
			continue
		}
		// Adaptation field: its length byte, then afLen bytes (flags and
		// stuffing), sized so the payload fills the rest of the packet.
		afLen := 184 - 1 - len(payload)
		if withRAI && afLen < 1 {
			afLen = 1
		}
		if afLen < 0 {
			afLen = 0
		}
		pkt[3] = 0x30
		pkt[4] = byte(afLen)
		if afLen > 0 {
			if withRAI {
				pkt[5] = 0x40
			}
			for i := 6; i < 5+afLen; i++ {
				pkt[i] = 0xFF
			}
		}
		n := copy(pkt[5+afLen:], payload)
		payload = payload[n:]
		b.emit(out, pkt)
	}
}

func (b tsBuild) emit(out *bytes.Buffer, pkt []byte) {
	if b.m2ts {
		out.Write([]byte{0, 0, 0, 0})
	}
	out.Write(pkt)
}

func (b tsBuild) bytes(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	pat := []byte{0x00, 0x00, 0xB0, 13, 0x00, 0x01, 0xC1, 0x00, 0x00, 0x00, 0x01, 0xE0 | tsTestPMTPID>>8, tsTestPMTPID & 0xFF, 0, 0, 0, 0}
	pmt := []byte{0x00, 0x02, 0xB0, 23, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x01, 0xF0, 0x00,
		0x0F, 0xE1, 0x02, 0xF0, 0x00, // AAC audio
		0x1B, 0xE1, 0x01, 0xF0, 0x00, // H.264 video
		0, 0, 0, 0}
	ptsStep := int64(90000 / b.fps)
	size := b.frameBytes
	if size == 0 {
		size = 3000
	}
	for f := 0; f < b.frames; f++ {
		if f%(b.fps*2) == 0 {
			b.packetize(&out, 0, pat, false)
			b.packetize(&out, tsTestPMTPID, pmt, false)
		}
		pts := b.startPTS + int64(f)*ptsStep
		key := f%b.gop == 0
		es := []byte{0, 0, 0, 1, 0x09, 0xF0} // AUD
		switch {
		case key && b.openGOP:
			es = append(es, 0, 0, 0, 1, 0x67, 0x64, 0x00, 0x28) // SPS
			es = append(es, 0, 0, 0, 1, 0x06, 0x05, 0x02, 0xAA, 0xBB, 0x06, 0x01, 0xC4, 0x80)
			es = append(es, 0, 0, 1, 0x41) // non-IDR I slice
		case key:
			es = append(es, 0, 0, 0, 1, 0x67, 0x64, 0x00, 0x28, 0, 0, 0, 1, 0x68, 0xEE)
			es = append(es, 0, 0, 1, 0x65)
		default:
			es = append(es, 0, 0, 1, 0x41)
		}
		for len(es) < size {
			es = append(es, 0x5A)
		}
		pes := append([]byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 5}, tsPTSBytes(pts)...)
		b.packetize(&out, tsTestVideoPID, append(pes, es...), key || b.raiOnAll)
		audio := append([]byte{0, 0, 1, 0xC0, 0, 0, 0x80, 0x80, 5}, tsPTSBytes(pts)...)
		b.packetize(&out, tsTestAudioPID, append(audio, make([]byte, 300)...), true)
	}
	return out.Bytes()
}

func checkTSKeyframes(t *testing.T, got []float64, start float64, gopSeconds float64, duration float64) {
	t.Helper()
	if len(got) < int(duration/stableCopyTargetSegment)/2 {
		t.Fatalf("only %d keyframes for %.0fs: %v", len(got), duration, got)
	}
	for i, v := range got {
		phase := math.Mod(v-start, gopSeconds)
		if phase > 1e-6 && gopSeconds-phase > 1e-6 {
			t.Fatalf("time %.4f is not a keyframe (start %.3f, GOP %.1fs)", v, start, gopSeconds)
		}
		if i > 0 && v <= got[i-1] {
			t.Fatalf("times not ascending: %v", got)
		}
	}
	starts, err := groupKeyframeSegments(got, start, duration, false)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if len(starts) < int(duration/10) {
		t.Fatalf("only %d segments over %.0fs", len(starts), duration)
	}
}

func TestMPEGTSKeyframesSampled(t *testing.T) {
	cases := []struct {
		name  string
		build tsBuild
	}{
		{"idr", tsBuild{fps: 25, gop: 50, frames: 25 * 120, startPTS: 126000}},
		{"open gop recovery point", tsBuild{fps: 25, gop: 50, frames: 25 * 120, startPTS: 126000, openGOP: true}},
		{"rai on every pes", tsBuild{fps: 25, gop: 50, frames: 25 * 120, startPTS: 126000, raiOnAll: true}},
		{"m2ts", tsBuild{fps: 25, gop: 25, frames: 25 * 120, startPTS: 126000, m2ts: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.build.bytes(t)
			duration := float64(tc.build.frames) / float64(tc.build.fps)
			got, err := mpegtsKeyframes(context.Background(), memRangeSource(file), duration)
			if err != nil {
				t.Fatal(err)
			}
			checkTSKeyframes(t, got, float64(tc.build.startPTS)/tsPTSHz, float64(tc.build.gop)/float64(tc.build.fps), duration)
			// Sniffing (no extension) reaches the same parser.
			sniffed, err := sniffKeyframes(context.Background(), memRangeSource(file), duration, true)
			if err != nil || len(sniffed) != len(got) {
				t.Fatalf("sniffed %d keyframes (%v), want %d", len(sniffed), err, len(got))
			}
		})
	}
}

func TestMPEGTSKeyframesAcrossPTSWrap(t *testing.T) {
	b := tsBuild{fps: 25, gop: 50, frames: 25 * 60, startPTS: tsPTSWrap - 20*90000}
	file := b.bytes(t)
	got, err := mpegtsKeyframes(context.Background(), memRangeSource(file), 60)
	if err != nil {
		t.Fatal(err)
	}
	checkTSKeyframes(t, got, float64(b.startPTS)/tsPTSHz, 2, 60)
	if last := got[len(got)-1]; last < float64(tsPTSWrap)/tsPTSHz {
		t.Fatalf("times after the wrap were not unwrapped: last %.3f", last)
	}
}

func TestMPEGTSKeyframesRejectsOtherInput(t *testing.T) {
	ctx := context.Background()
	if _, err := mpegtsKeyframes(ctx, memRangeSource(make([]byte, 100000)), 60); err == nil {
		t.Fatal("zeros accepted as MPEG-TS")
	}
	// Video-less stream: only the audio PID in the PMT.
	b := tsBuild{fps: 25, gop: 50, frames: 100}
	file := b.bytes(t)
	noVideo := bytes.ReplaceAll(file, []byte{0x1B, 0xE1, 0x01, 0xF0, 0x00}, []byte{0x0F, 0xE1, 0x03, 0xF0, 0x00})
	if _, err := mpegtsKeyframes(ctx, memRangeSource(noVideo), 4); err == nil {
		t.Fatal("stream without video accepted")
	}
	// Corrupted packets must not panic.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		damaged := append([]byte(nil), file...)
		for j := 0; j < 200; j++ {
			damaged[rng.Intn(len(damaged))] = byte(rng.Intn(256))
		}
		_, _ = mpegtsKeyframes(ctx, memRangeSource(damaged), 4)
	}
}

func TestIsMPEGTSPath(t *testing.T) {
	for path, want := range map[string]bool{
		"recording:abc/NDR - Magazin.ts": true,
		"/media/x.M2TS":                  true,
		"https://h/x.mts?token=1":        true,
		"/media/x.mkv":                   false,
		"/media/x.tsx":                   false,
	} {
		if got := isMPEGTSPath(path); got != want {
			t.Errorf("isMPEGTSPath(%q) = %v", path, got)
		}
	}
}

// TestStableCopyMPEGTSEndToEnd plays a DVB-like recording (H.264 50fps with a
// 2s GOP, AAC, MPEG-TS with a non-zero start time) with video transcoding
// disabled: the sampled keyframes must give a copy timeline, and segments
// from the start and after a far seek (a restarted run) must sit on their
// planned times. Runs only with MEDIASTORM_FFMPEG_E2E=1.
func TestStableCopyMPEGTSEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "Sender - 2026-11-18 0900 - Magazin.ts")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=50",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "600", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "100", "-keyint_min", "100", "-sc_threshold", "0", "-bf", "2",
		"-c:a", "aac", "-output_ts_offset", "1.42", "-f", "mpegts", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/" + url.PathEscape(filepath.Base(src))

	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	caps := &HLSClientCaps{TSVideo: []string{"h264"}, TSAudio: []string{"aac"}, ForceVideoCopy: true}
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), sourceURL, sourceURL, false, "", true, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 600, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer m.CleanupSession(session.ID)
	if !session.usesStableCopyTimeline() {
		t.Fatalf("no copy timeline: %+v", session.CopyPlan)
	}
	// Keyframe PTS minus the container start time (audio starts first): one
	// constant phase on the 2s GOP grid.
	phase0 := math.Mod(session.CopySegmentStarts[1], 2)
	for _, s := range session.CopySegmentStarts[1:] {
		if d := math.Abs(math.Mod(s, 2) - phase0); d > 0.01 && d < 1.99 {
			t.Fatalf("segment start %.3f is not on the 2s keyframe grid: %v", s, session.CopySegmentStarts)
		}
	}
	segment := func(n int) []byte {
		t.Helper()
		name := "segment" + strconv.Itoa(n) + ".ts"
		rec := httptest.NewRecorder()
		m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/"+name, nil), session.ID, name)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	// MPEG-TS segments keep the source clock plus a constant muxer offset;
	// what a player needs is that every segment sits on that one clock at its
	// planned distance, also after a restarted run.
	offset := math.NaN()
	check := func(n int, data []byte) {
		t.Helper()
		path := filepath.Join(dir, "seg"+strconv.Itoa(n)+".ts")
		writeConcat(t, path, data)
		out := ffprobeOut(t, "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-read_intervals", "%+#1", "-of", "csv=p=0", path)
		first, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(strings.Split(out, "\n")[0]), ","), 64)
		if err != nil {
			t.Fatalf("segment %d pts %q: %v", n, out, err)
		}
		if math.IsNaN(offset) {
			offset = first - session.CopySegmentStarts[n]
		}
		if got := first - offset; math.Abs(got-session.CopySegmentStarts[n]) > 0.1 {
			t.Fatalf("segment %d starts at %.3f on the timeline, planned %.3f", n, got, session.CopySegmentStarts[n])
		}
		if out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Fatalf("decode segment %d: %v %s", n, err, out)
		}
	}
	// Like a player: the start, then a far seek (a run restarted at that
	// keyframe, as the first run is still minutes away), then back.
	seg0 := segment(0)
	far := len(session.CopySegmentStarts) - 4
	check(far, segment(far))
	check(far+1, segment(far+1))
	check(1, segment(1))
	check(2, segment(2))
	if len(seg0) == 0 {
		t.Fatal("empty first segment")
	}
}

// A recording of an adaptive IPTV stream keeps every variant; the largest
// plays, addressed by PID. Ordinary files keep 0:v:0, and cover art is never
// the primary video.
func TestProbePicksLargestVideoRendition(t *testing.T) {
	m := &HLSManager{}
	parse := func(format, streams string) *UnifiedProbeResult {
		t.Helper()
		res, err := m.parseUnifiedProbeOutput([]byte(`{"format":{"duration":"60","format_name":"` + format + `"},"streams":[` + streams + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	variants := parse("mpegts", `{"index":0,"id":"0x100","codec_type":"video","codec_name":"h264","width":640,"height":360,"color_transfer":"bt709"},
		{"index":1,"id":"0x101","codec_type":"video","codec_name":"h264","width":1920,"height":1080,"profile":"High","level":40},
		{"index":2,"id":"0x102","codec_type":"video","codec_name":"h264","width":1280,"height":720},
		{"index":3,"id":"0x103","codec_type":"audio","codec_name":"aac"}`)
	if variants.VideoMap != "0:i:257" || variants.VideoStreamPID != 257 || variants.VideoWidth != 1920 || variants.VideoLevel != 40 {
		t.Fatalf("variants: map=%q pid=%d %dx%d level=%d", variants.VideoMap, variants.VideoStreamPID, variants.VideoWidth, variants.VideoHeight, variants.VideoLevel)
	}
	if variants.ColorTransfer != "bt709" {
		t.Fatalf("color transfer from another rendition lost: %q", variants.ColorTransfer)
	}
	if got := variants.videoMapSpec(); got != "0:i:257" {
		t.Fatalf("videoMapSpec = %q", got)
	}

	mkv := parse("matroska,webm", `{"index":0,"codec_type":"video","codec_name":"hevc","width":3840,"height":2160},
		{"index":1,"codec_type":"audio","codec_name":"eac3"},
		{"index":2,"codec_type":"video","codec_name":"mjpeg","width":4000,"height":3000,"disposition":{"attached_pic":1}}`)
	if mkv.VideoMap != "" || mkv.VideoCodec != "hevc" || mkv.videoMapSpec() != "0:v:0" {
		t.Fatalf("cover art chosen: map=%q codec=%s", mkv.VideoMap, mkv.VideoCodec)
	}
	// Outside transport streams a second video track is an angle or a
	// preview: the first one stays the primary video.
	two := parse("matroska,webm", `{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360},
		{"index":1,"codec_type":"video","codec_name":"h264","width":1280,"height":720}`)
	if two.VideoMap != "" || two.VideoWidth != 640 {
		t.Fatalf("matroska: map = %q, %dpx", two.VideoMap, two.VideoWidth)
	}
	// MP4 reports track IDs as "id" too; they are not PIDs.
	if mp4 := parse("mov,mp4,m4a,3gp,3g2,mj2", `{"index":0,"id":"0x1","codec_type":"video","codec_name":"h264","width":1920,"height":1080}`); mp4.VideoStreamPID > 0 || mp4.MPEGTS {
		t.Fatalf("mp4 track id taken for a PID: %d", mp4.VideoStreamPID)
	}
	// A transport stream without PIDs addresses the larger video by index.
	if ts := parse("mpegts", `{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360},{"index":1,"codec_type":"video","codec_name":"h264","width":1280,"height":720}`); ts.VideoMap != "0:1" {
		t.Fatalf("ts without pids: map = %q", ts.VideoMap)
	}
	if (*UnifiedProbeResult)(nil).videoMapSpec() != "0:v:0" {
		t.Fatal("nil probe must map 0:v:0")
	}
}

// TestStableCopyMPEGTSVariantsEndToEnd copies the largest rendition of a
// multi-variant recording (360p listed first, 720p second): the keyframe
// index and FFmpeg both use the 720p PID. Runs with MEDIASTORM_FFMPEG_E2E=1.
func TestStableCopyMPEGTSVariantsEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "variants.ts")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=50",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "120",
		"-filter_complex", "[0:v]split=2[a][b];[a]scale=640:360[small]",
		"-map", "[small]", "-map", "[b]", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "100", "-keyint_min", "100", "-sc_threshold", "0",
		"-c:a", "aac", "-output_ts_offset", "1.42", "-f", "mpegts", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	files := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer files.Close()
	sourceURL := files.URL + "/variants.ts"

	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", nil)
	caps := &HLSClientCaps{TSVideo: []string{"h264"}, TSAudio: []string{"aac"}, ForceVideoCopy: true}
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), sourceURL, sourceURL, false, "", true, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 120, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer m.CleanupSession(session.ID)
	if !session.usesStableCopyTimeline() || session.ProbeData == nil || session.ProbeData.VideoMap == "" || session.ProbeData.VideoWidth != 1280 {
		t.Fatalf("plan=%+v probe=%+v", session.CopyPlan, session.ProbeData)
	}
	for _, n := range []int{0, len(session.CopySegmentStarts) - 3} {
		name := "segment" + strconv.Itoa(n) + ".ts"
		rec := httptest.NewRecorder()
		m.ServeSegment(rec, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/"+name, nil), session.ID, name)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		path := filepath.Join(dir, name)
		writeConcat(t, path, rec.Body.Bytes())
		streams := ffprobeOut(t, "-select_streams", "v", "-show_entries", "stream=width,height", "-show_programs", "-of", "csv=p=0", path)
		if lines := strings.Fields(streams); len(lines) == 0 || strings.Count(strings.Join(lines, " "), "640,360") > 0 || !strings.Contains(streams, "1280,720") {
			t.Fatalf("%s video streams: %q", name, streams)
		}
		if out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Fatalf("decode %s: %v %s", name, err, out)
		}
	}
}

// failingRangeSource fails every read after the first n.
type failingRangeSource struct {
	memRangeSource
	n     int
	reads int
}

func (f *failingRangeSource) ReadRange(ctx context.Context, offset, length int64) ([]byte, error) {
	f.reads++
	if f.reads > f.n {
		return nil, errors.New("upstream timed out")
	}
	return f.memRangeSource.ReadRange(ctx, offset, length)
}

// A read that fails mid-scan is an error, not the end of the file: a list
// cut short there would be taken for the whole recording.
func TestMPEGTSKeyframesFailedReadIsAnError(t *testing.T) {
	b := tsBuild{fps: 25, gop: 50, frames: 25 * 120, startPTS: 126000}
	src := &failingRangeSource{memRangeSource: memRangeSource(b.bytes(t)), n: 20}
	if got, err := mpegtsKeyframes(context.Background(), src, 120); err == nil {
		t.Fatalf("got %d keyframes and no error after the source failed", len(got))
	}
}

// SEI payload sizes count the unescaped bytes: an emulation prevention byte
// must not shift the walk onto a byte that reads as a recovery point.
func TestH264SEIEmulationPrevention(t *testing.T) {
	// user data (type 5) of 4 bytes 00 00 02 06, escaped as 00 00 03 02 06.
	sei := []byte{0x05, 0x04, 0x00, 0x00, 0x03, 0x02, 0x06, 0x80}
	if h264SEIHasRecoveryPoint(nalRBSP(sei)) {
		t.Fatal("user data read as a recovery point")
	}
	recovery := []byte{0x06, 0x01, 0x84, 0x80, 0x00, 0x00, 0x01, 0x65}
	if !h264SEIHasRecoveryPoint(nalRBSP(recovery)) {
		t.Fatal("recovery point not found")
	}
	if got := nalRBSP([]byte{1, 2, 0, 0, 1, 6}); string(got) != string([]byte{1, 2}) {
		t.Fatalf("nalRBSP did not stop at the start code: % x", got)
	}
}

// Long segments (video transcoding disabled) are still bounded.
func TestGroupKeyframeSegmentsLongStillBounded(t *testing.T) {
	if _, err := groupKeyframeSegments([]float64{0, 4}, 0, 4+stableCopyMaxLongSegment+1, true); err == nil {
		t.Fatal("a segment longer than stableCopyMaxLongSegment was accepted")
	}
}

package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
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

	"novastream/services/streaming"
)

// growingFileProvider serves one local file whose growing state the test
// switches, as the recordings provider does for a running recording.
type growingFileProvider struct {
	path    string
	growing atomic.Bool
	// status: 0 answers, 1 fails (a passing error), 2 the recording is gone
	status atomic.Int32
}

func (p *growingFileProvider) Stream(context.Context, streaming.Request) (*streaming.Response, error) {
	return &streaming.Response{Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (p *growingFileProvider) GetDirectURL(context.Context, string) (string, error) {
	return p.path, nil
}

func (p *growingFileProvider) IsGrowing(context.Context, string) (bool, error) {
	switch p.status.Load() {
	case 1:
		return false, errors.New("database unavailable")
	case 2:
		return false, streaming.ErrNotFound
	}
	return p.growing.Load(), nil
}

// appendFile adds data to the end of path, like a recorder writing.
func appendFile(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

// checkStartsOnGrid checks that the starts begin at 0, sit on the 2s GOP and
// are at least the 4s target apart. Sampled parts (the first index) may skip
// a keyframe; scanned parts (exact) are 4s apart from the index fromExact.
func checkStartsOnGrid(t *testing.T, starts []float64, fromExact int) {
	t.Helper()
	if len(starts) == 0 || starts[0] != 0 {
		t.Fatalf("starts = %v", starts)
	}
	for i := 1; i < len(starts); i++ {
		if r := math.Mod(starts[i]+0.005, 2); r > 0.01 {
			t.Fatalf("start %.3f is not a keyframe: %v", starts[i], starts)
		}
		d := starts[i] - starts[i-1]
		if d < 3.99 || d > 8.01 || (i > fromExact && math.Abs(d-4) > 0.01) {
			t.Fatalf("segment %d is %.3fs long: %v", i-1, d, starts)
		}
	}
}

// A recording read while it is written: the first index covers what is on
// disk, later scans add the segments written since (each keyframe in the new
// part is read), and the stop gives the timeline its duration.
func TestGrowingCopyIndexFollowsTheFile(t *testing.T) {
	const startPTS = 900000 // 10s
	full := tsBuild{fps: 25, gop: 50, frames: 25 * 60, startPTS: startPTS}.bytes(t)
	half := len(full) / 2 / 188 * 188
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.ts")
	appendFile(t, path, full[:half])

	provider := &growingFileProvider{path: path}
	provider.growing.Store(true)
	m := NewHLSManager(filepath.Join(dir, "hls"), "", "", provider)
	t.Cleanup(m.Shutdown)
	probe := &UnifiedProbeResult{StartTime: 10, AvgFrameRate: "25/1"}

	starts, index, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", probe, 30)
	if err != nil {
		t.Fatal(err)
	}
	checkStartsOnGrid(t, starts, len(starts))
	if last := starts[len(starts)-1]; last < 20 || last > 30 {
		t.Fatalf("first index ends at %.1fs, the file holds about 30s: %v", last, starts)
	}
	session := &HLSSession{ID: "growing-copy", Path: "recording:r/rec.ts", StableCopy: true, CopySegmentStarts: starts, copyIndex: index, ProbeData: probe}

	// Without new data a scan only reads the rest of what is on disk, which
	// can hold a keyframe the sampler passed over.
	m.extendGrowingCopy(context.Background(), session, true)
	sampled := session.copyStarts()
	checkStartsOnGrid(t, sampled, len(starts)-1)
	if last := sampled[len(sampled)-1]; last > 30 {
		t.Fatalf("index past the written data: %v", sampled)
	}

	// Rate limited: an unforced scan right after does not read the file.
	appendFile(t, path, full[half:])
	m.extendGrowingCopy(context.Background(), session, false)
	if got := session.copyStarts(); len(got) != len(sampled) {
		t.Fatalf("unforced scan inside the interval read the file: %v", got)
	}
	m.extendGrowingCopy(context.Background(), session, true)
	grown := session.copyStarts()
	checkStartsOnGrid(t, grown, len(starts)-1)
	if last := grown[len(grown)-1]; last < 56 {
		t.Fatalf("grown index ends at %.1fs, want the keyframe at 56s: %v", last, grown)
	}
	if !session.growingCopyOpen() || session.Duration != 0 {
		t.Fatalf("still recording: open=%v duration=%v", session.growingCopyOpen(), session.Duration)
	}
	// Every start knows where its keyframe begins in the file, and a run
	// restarted there reads from the program tables just before it.
	if len(index.offsets) != len(grown) {
		t.Fatalf("%d offsets for %d starts", len(index.offsets), len(grown))
	}
	src, err := m.openRangeSource(context.Background(), "recording:r/rec.ts", "")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	for n := 1; n < len(grown); n++ {
		at := index.offsets[n]
		pkt, ok := parseMPEGTSPacket(full[at : at+188])
		pts, _, ptsOK := tsPESPTS(pkt.payload)
		if !ok || pkt.pid != tsTestVideoPID || !pkt.pusi || !ptsOK || math.Abs(index.timelineTime(pts)-grown[n]) > 0.001 {
			t.Fatalf("segment %d offset %d is not its keyframe (pid %d pusi %v)", n, at, pkt.pid, pkt.pusi)
		}
		pat, ok := index.restartOffset(context.Background(), src, n)
		if !ok || pat >= at || at-pat > 2*int64(len(full))/60 {
			t.Fatalf("segment %d: restart offset %d for keyframe at %d", n, pat, at)
		}
		if p, _ := parseMPEGTSPacket(full[pat : pat+188]); p.pid != 0 || !p.pusi {
			t.Fatalf("segment %d: restart offset %d is not a PAT", n, pat)
		}
	}
	if _, ok := index.restartOffset(context.Background(), src, len(grown)); ok {
		t.Fatal("restart offset past the index")
	}

	// A status check that fails says nothing: the timeline stays open.
	provider.growing.Store(false)
	provider.status.Store(1)
	m.extendGrowingCopy(context.Background(), session, true)
	if !session.growingCopyOpen() || session.Duration != 0 {
		t.Fatal("a failed status check finished the timeline")
	}
	provider.status.Store(0)
	m.extendGrowingCopy(context.Background(), session, true)
	if session.growingCopyOpen() {
		t.Fatal("timeline still open after the recording stopped")
	}
	if math.Abs(session.Duration-60) > 0.05 {
		t.Fatalf("duration = %.3f, want 60 (last frame at 59.96 + one frame)", session.Duration)
	}
}

func TestGrowingCopyNeedsSomethingRecorded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.ts")
	appendFile(t, path, tsBuild{fps: 25, gop: 50, frames: 25 * 5}.bytes(t))
	provider := &growingFileProvider{path: path}
	m := NewHLSManager(filepath.Join(dir, "hls"), "", "", provider)
	t.Cleanup(m.Shutdown)
	if _, _, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", nil, 5); err == nil {
		t.Fatal("5s recording gave a growing timeline; its first segment could not close")
	}
	if _, _, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", nil, 0); err == nil {
		t.Fatal("unknown recorded duration accepted")
	}
}

func TestGrowingCopyPlaylist(t *testing.T) {
	session := &HLSSession{ID: "p", StableCopy: true, CopySegmentStarts: []float64{0, 4, 8, 12}, copyIndex: &growingCopyIndex{}}
	body := buildStableCopyPlaylist(session)
	for _, want := range []string{"#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-TARGETDURATION:20", "segment2.ts"} {
		if !strings.Contains(body, want) {
			t.Fatalf("growing playlist misses %q:\n%s", want, body)
		}
	}
	// The last segment has no end yet.
	if strings.Contains(body, "segment3.ts") || strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("growing playlist lists the open segment or ends:\n%s", body)
	}

	session.copyIndex.finished = true
	session.Duration = 14.5
	body = buildStableCopyPlaylist(session)
	for _, want := range []string{"#EXT-X-PLAYLIST-TYPE:VOD", "#EXTINF:2.500000,\nsegment3.ts", "#EXT-X-ENDLIST"} {
		if !strings.Contains(body, want) {
			t.Fatalf("finished playlist misses %q:\n%s", want, body)
		}
	}
}

// The playlist a client fetches is built from the timeline, extended from the
// file, and ends once the recording has stopped.
func TestServePlaylistGrowsWithTheRecording(t *testing.T) {
	full := tsBuild{fps: 25, gop: 50, frames: 25 * 40}.bytes(t)
	half := len(full) / 2 / 188 * 188
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.ts")
	appendFile(t, path, full[:half])
	provider := &growingFileProvider{path: path}
	provider.growing.Store(true)
	m := NewHLSManager(filepath.Join(dir, "hls"), "", "", provider)
	t.Cleanup(m.Shutdown)
	probe := &UnifiedProbeResult{AvgFrameRate: "25/1"}
	starts, index, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", probe, 20)
	if err != nil {
		t.Fatal(err)
	}
	session := &HLSSession{
		ID: "serve-growing", Path: "recording:r/rec.ts", OutputDir: filepath.Join(dir, "hls", "serve-growing"),
		StableCopy: true, CopySegmentStarts: starts, copyIndex: index, ProbeData: probe,
		GrowingSource: true, SourceGrowing: true, FinalSegmentCount: -1, CreatedAt: time.Now(), LastAccess: time.Now(),
	}
	if err := os.MkdirAll(session.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The segment muxer's own list, which the server waits for.
	if err := os.WriteFile(filepath.Join(session.OutputDir, "stream.m3u8"), []byte("#EXTM3U\n#EXTINF:4,\nsegment0.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sessions[session.ID] = session
	m.mu.Unlock()
	serve := func() string {
		rr := httptest.NewRecorder()
		m.ServePlaylist(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/stream.m3u8", nil), session.ID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}
	segments := func(body string) int { return strings.Count(body, "#EXTINF:") }

	first := serve()
	if !strings.Contains(first, "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(first, "#EXT-X-ENDLIST") ||
		!strings.Contains(first, "#EXT-X-START:TIME-OFFSET=0") || segments(first) != len(starts)-1 {
		t.Fatalf("first playlist (%d starts):\n%s", len(starts), first)
	}
	appendFile(t, path, full[half:])
	index.checked = time.Time{} // past the rescan interval
	grown := serve()
	if segments(grown) <= segments(first) || strings.Contains(grown, "#EXT-X-ENDLIST") {
		t.Fatalf("playlist did not grow (%d -> %d segments):\n%s", segments(first), segments(grown), grown)
	}
	provider.growing.Store(false)
	index.checked = time.Time{}
	done := serve()
	if !strings.Contains(done, "#EXT-X-ENDLIST") || segments(done) != segments(grown)+1 {
		t.Fatalf("finished playlist (%d segments before):\n%s", segments(grown), done)
	}
}

// A run started with a shorter cut list leaves its last segment open; a
// request for that segment (or one shortly after) restarts there, and the
// run's own list does not make it count as complete.
func TestGrowingCopyRestartsTheRunsOpenSegment(t *testing.T) {
	dir := t.TempDir()
	session := &HLSSession{ID: "tail", OutputDir: dir, StableCopy: true, CopySegmentStarts: []float64{0, 4, 8, 12, 16, 20}, copyIndex: &growingCopyIndex{}, LastSegmentServed: -1}
	session.setCopyRunSegments(3) // the run cuts at 4 and 8; segment 2 stays open
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("segment0.ts\nsegment1.ts\nsegment2.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &HLSManager{}
	if !m.stableCopySegmentComplete(session, 1) || m.stableCopySegmentComplete(session, 2) {
		t.Fatal("the run's open segment counted as complete, or a closed one did not")
	}
	plan, ok := castSegmentRestartPlan(session, 2, 2)
	if !ok || plan.SegmentStartNumber != 2 || plan.TranscodingOffset != 8 {
		t.Fatalf("request for the open segment: %+v %v", plan, ok)
	}
	plan, ok = castSegmentRestartPlan(session, 3, 2)
	if !ok || plan.SegmentStartNumber != 2 {
		t.Fatalf("request just past the open segment restarts at it: %+v %v", plan, ok)
	}
	if plan, ok := castSegmentRestartPlan(session, 1, 2); ok && plan.SegmentStartNumber >= 2 {
		t.Fatalf("closed segment restarted the run at the tail: %+v", plan)
	}
	// A run given every start has no such segment.
	session.setCopyRunSegments(6)
	if _, open := session.growingCopyTail(); open {
		t.Fatal("run with the full list still has an open tail")
	}
}

// Plays a recording while it is written, through a real FFmpeg: the playlist
// grows, segments past the first run's cut list come from a restarted run on
// the same timeline, and the stop ends the playlist.
func TestGrowingCopyEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	full := filepath.Join(dir, "full.ts")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "120", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-keyint_min", "50", "-sc_threshold", "0", "-bf", "2",
		"-c:a", "aac", "-output_ts_offset", "1.4", "-f", "mpegts", full)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(dir, "Sender - 2026-11-20 2015 - Film.ts")
	cut := len(data) / 3 / 188 * 188 // about 40s recorded
	appendFile(t, rec, data[:cut])
	provider := &growingFileProvider{path: rec}
	provider.growing.Store(true)

	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", provider)
	t.Cleanup(m.Shutdown)
	caps := &HLSClientCaps{TSVideo: []string{"h264"}, TSAudio: []string{"aac"}, ForceVideoCopy: true}
	path := "recording:rec-1/" + filepath.Base(rec)
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), path, path, false, "", false, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer m.CleanupSession(session.ID)
	if !session.usesStableCopyTimeline() || !session.growingCopyOpen() {
		t.Fatalf("no growing copy timeline: plan=%+v starts=%v", session.CopyPlan, session.copyStarts())
	}
	ext := ".ts"
	if session.CopyPlan.FMP4 {
		ext = ".m4s"
	}
	playlist := func() string {
		t.Helper()
		rr := httptest.NewRecorder()
		m.ServePlaylist(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/stream.m3u8", nil), session.ID)
		if rr.Code != http.StatusOK {
			t.Fatalf("playlist: %d %s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}
	init := []byte(nil)
	if session.CopyPlan.FMP4 {
		rr := httptest.NewRecorder()
		m.ServeSegment(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/init.mp4", nil), session.ID, "init.mp4")
		if rr.Code != http.StatusOK {
			t.Fatalf("init: %d", rr.Code)
		}
		init = rr.Body.Bytes()
	}
	segment := func(n int) []byte {
		t.Helper()
		name := "segment" + strconv.Itoa(n) + ext
		rr := httptest.NewRecorder()
		m.ServeSegment(rr, httptest.NewRequest(http.MethodGet, "/video/hls/"+session.ID+"/"+name, nil), session.ID, name)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
		return rr.Body.Bytes()
	}
	// Every segment must decode and start where the timeline says, on one
	// clock across runs.
	offset := math.NaN()
	check := func(n int) {
		t.Helper()
		file := filepath.Join(dir, "seg"+strconv.Itoa(n)+ext)
		writeConcat(t, file, init, segment(n))
		// MPEG-TS packets end the csv line with a comma (side data column).
		out := ffprobeOut(t, "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-read_intervals", "%+#1", "-of", "csv=p=0", file)
		pts, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(strings.Split(out, "\n")[0]), ","), 64)
		if err != nil {
			t.Fatalf("segment %d pts %q: %v", n, out, err)
		}
		start, _ := session.stableCopySegmentStart(n)
		if math.IsNaN(offset) {
			offset = pts - start
		}
		if got := pts - offset; math.Abs(got-start) > 0.1 {
			t.Fatalf("segment %d starts at %.3f on the timeline, planned %.3f", n, got, start)
		}
		if out, err := exec.Command("ffmpeg", "-v", "error", "-i", file, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Fatalf("decode segment %d: %v %s", n, err, out)
		}
	}

	first := playlist()
	if !strings.Contains(first, "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(first, "#EXT-X-ENDLIST") {
		t.Fatalf("first playlist:\n%s", first)
	}
	firstCount := strings.Count(first, "#EXTINF:")
	check(0)
	check(1)
	check(firstCount - 1)

	// The recorder writes the rest in bursts, as IPTV segments arrive.
	for off := cut; off < len(data); {
		end := min(off+len(data)/20/188*188, len(data))
		appendFile(t, rec, data[off:end])
		off = end
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(growingCopyRescanInterval)
	grown := playlist()
	grownCount := strings.Count(grown, "#EXTINF:")
	if grownCount < firstCount+15 || strings.Contains(grown, "#EXT-X-ENDLIST") {
		t.Fatalf("playlist did not grow with the file (%d -> %d):\n%s", firstCount, grownCount, grown)
	}
	// Past the first run's cut list: a new run, same timeline.
	check(firstCount)
	check(firstCount + 1)
	check(grownCount - 1)

	// The recording stops: the playlist ends with every segment listed.
	provider.growing.Store(false)
	time.Sleep(growingCopyRescanInterval)
	done := playlist()
	if !strings.Contains(done, "#EXT-X-ENDLIST") || !strings.Contains(done, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Fatalf("finished playlist:\n%s", done)
	}
	if math.Abs(session.Duration-120) > 0.5 {
		t.Fatalf("session duration = %.2f, want about 120", session.Duration)
	}
	last := strings.Count(done, "#EXTINF:") - 1
	check(last)
}

// The probe of a growing file is cached: an hour into a recording it may
// still give the length it had at 12 minutes. The index measures what the
// file holds instead, so the recorded part is cut into ordinary segments.
func TestGrowingCopyIgnoresStaleProbeDuration(t *testing.T) {
	full := tsBuild{fps: 25, gop: 50, frames: 25 * 60, startPTS: 900000}.bytes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.ts")
	appendFile(t, path, full[:len(full)/2/188*188])
	m := NewHLSManager(filepath.Join(dir, "hls"), "", "", &growingFileProvider{path: path})
	t.Cleanup(m.Shutdown)
	starts, _, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", &UnifiedProbeResult{StartTime: 10}, 3)
	if err != nil {
		t.Fatal(err)
	}
	checkStartsOnGrid(t, starts, len(starts))
	if last := starts[len(starts)-1]; last < 20 || len(starts) < 6 {
		t.Fatalf("index from a stale 3s probe: %v", starts)
	}
}

// A recording deleted while it plays ends its timeline: nothing more will be
// written, and the file cannot be read for an end.
func TestGrowingCopyEndsWhenTheRecordingIsGone(t *testing.T) {
	full := tsBuild{fps: 25, gop: 50, frames: 25 * 30, startPTS: 900000}.bytes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.ts")
	appendFile(t, path, full)
	provider := &growingFileProvider{path: path}
	provider.growing.Store(true)
	m := NewHLSManager(filepath.Join(dir, "hls"), "", "", provider)
	t.Cleanup(m.Shutdown)
	probe := &UnifiedProbeResult{StartTime: 10, AvgFrameRate: "25/1"}
	starts, index, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", probe, 30)
	if err != nil {
		t.Fatal(err)
	}
	session := &HLSSession{ID: "gone", Path: "recording:r/rec.ts", StableCopy: true, CopySegmentStarts: starts, copyIndex: index, ProbeData: probe}
	_ = os.Remove(path)
	provider.status.Store(2)
	m.extendGrowingCopy(context.Background(), session, true)
	last := starts[len(starts)-1]
	if session.growingCopyOpen() || session.Duration != last+stableCopyTargetSegment {
		t.Fatalf("open=%v duration=%v, want finished at %v", session.growingCopyOpen(), session.Duration, last+stableCopyTargetSegment)
	}
}

// After a 33-bit PTS wrap a run is not started by byte offset: it would see
// the small raw timestamps with nothing to unwrap them against.
func TestGrowingCopyRestartsAfterAPTSWrapSeek(t *testing.T) {
	full := tsBuild{fps: 25, gop: 50, frames: 25 * 40, startPTS: tsPTSWrap - 20*90000}.bytes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.ts")
	appendFile(t, path, full)
	provider := &growingFileProvider{path: path}
	provider.growing.Store(true)
	m := NewHLSManager(filepath.Join(dir, "hls"), "", "", provider)
	t.Cleanup(m.Shutdown)
	probe := &UnifiedProbeResult{StartTime: float64(tsPTSWrap-20*90000) / tsPTSHz, AvgFrameRate: "25/1"}
	starts, index, err := m.planGrowingCopySegments(context.Background(), "recording:r/rec.ts", "", probe, 40)
	if err != nil {
		t.Fatal(err)
	}
	session := &HLSSession{ID: "wrap", Path: "recording:r/rec.ts", StableCopy: true, CopySegmentStarts: starts, copyIndex: index, ProbeData: probe}
	m.extendGrowingCopy(context.Background(), session, true)
	src, err := m.openRangeSource(context.Background(), "recording:r/rec.ts", "")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	before, after := 0, 0
	for n, start := range session.copyStarts() {
		if n == 0 {
			continue
		}
		_, ok := index.restartOffset(context.Background(), src, n)
		switch {
		case start < 20 && ok:
			before++
		case start >= 20.5 && !ok:
			after++
		case start >= 20.5 && ok:
			t.Fatalf("segment %d at %.1fs (after the wrap) restarts by byte offset", n, start)
		}
	}
	if before == 0 || after == 0 {
		t.Fatalf("restart offsets: %d before the wrap, %d after: %v", before, after, session.copyStarts())
	}
}

func TestHoldGrowingCopyTailBatchesLiveEdgeRestarts(t *testing.T) {
	session := &HLSSession{copyIndex: &growingCopyIndex{}}
	starts := []float64{0, 6, 12, 18, 24, 30, 36}
	cases := []struct{ run, want int }{
		{0, 7}, // no run yet: everything known is listed
		{7, 7}, // the run cuts every start
		{6, 6}, // 1 past the open segment: held at the run's closed segments
		{5, 5}, // 2 past: still held
		{4, 7}, // 3 past: listed, one restart covers them
	}
	for _, c := range cases {
		session.setCopyRunSegments(c.run)
		if got := len(session.holdGrowingCopyTail(starts)); got != c.want {
			t.Errorf("run=%d: listed starts %d, want %d", c.run, got, c.want)
		}
	}
}

// A running recording the web player cannot get a copy timeline for (here: it refused the copy)
// follows the file as a web session, which converts the recording's AC-3 to stereo AAC in MPEG-TS
// although /watch no longer asks for forceAAC.
func TestGrowingWebFallbackConvertsAudio(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	dir := t.TempDir()
	full := filepath.Join(dir, "full.ts")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "60", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50",
		"-c:a", "ac3", "-ac", "6", "-f", "mpegts", full)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(dir, "Sender - 2026-11-25 2015 - Film.ts")
	appendFile(t, rec, data[:len(data)/2/188*188])
	provider := &growingFileProvider{path: rec}
	provider.growing.Store(true)
	m := NewHLSManager(filepath.Join(dir, "hls"), "ffmpeg", "ffprobe", provider)
	t.Cleanup(m.Shutdown)
	caps := &HLSClientCaps{TSVideo: []string{"h264"}, TSAudio: []string{"aac", "mp3"}, FMP4Video: []string{"h264"}, FMP4Audio: []string{"aac"}, MaxAudioChannels: 2, WebFallback: true, NoVideoCopy: true}
	path := "recording:rec-1/" + filepath.Base(rec)
	session, err := m.CreateSession(withHLSClientCaps(context.Background(), caps), path, path, false, "", false, false, 0, 0, -1, -1, "p", "p", "127.0.0.1", false, "", "web-stable", 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer m.CleanupSession(session.ID)
	if session.usesStableCopyTimeline() {
		t.Fatal("a refused copy still got a copy timeline")
	}
	deadline := time.Now().Add(30 * time.Second)
	var segment string
	for segment == "" {
		for _, name := range []string{"segment0.ts", "segment0.m4s"} {
			if _, err := os.Stat(filepath.Join(session.OutputDir, name)); err == nil {
				segment = name
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no first segment")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if segment != "segment0.ts" {
		t.Fatalf("growing fallback segments are %s, want MPEG-TS", segment)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name,channels", "-of", "csv=p=0", filepath.Join(session.OutputDir, segment)).Output()
	if err != nil || !strings.Contains(string(out), "aac,2") {
		t.Fatalf("growing fallback audio: %q %v, want stereo AAC", out, err)
	}
}

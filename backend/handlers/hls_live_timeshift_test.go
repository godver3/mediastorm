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

func timeshiftTestEntries(count int, duration float64) []livePlaylistEntry {
	entries := make([]livePlaylistEntry, count)
	for i := range entries {
		entries[i] = livePlaylistEntry{
			MediaSequence: i,
			URI:           fmt.Sprintf("segment%d.ts", i),
			ExtInf:        fmt.Sprintf("%.3f,", duration),
			Duration:      duration,
		}
	}
	return entries
}

func TestTrimLivePlaylistWindowKeepsCountWithoutTimeshift(t *testing.T) {
	session := &HLSSession{IsLive: true, livePlaylistWindow: timeshiftTestEntries(100, 2)}
	trimLivePlaylistWindow(session)
	if got := len(session.livePlaylistWindow); got != liveNativeHLSListSize {
		t.Fatalf("ordinary live window = %d entries, want %d", got, liveNativeHLSListSize)
	}
	if first := session.livePlaylistWindow[0].MediaSequence; first != 100-liveNativeHLSListSize {
		t.Fatalf("first entry = %d, want the newest %d kept", first, liveNativeHLSListSize)
	}
}

func TestTrimLivePlaylistWindowKeepsTimeshiftDuration(t *testing.T) {
	// 2 hours of 4 s segments against a 30 minute window: the newest 450 segments stay.
	session := &HLSSession{IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: 30 * time.Minute}}
	session.livePlaylistWindow = timeshiftTestEntries(1800, 4)
	session.livePlaylistWindow[100].Discontinuity = true
	trimLivePlaylistWindow(session)
	if got := len(session.livePlaylistWindow); got != 450 {
		t.Fatalf("timeshift window = %d entries, want 450 (30 min of 4 s segments)", got)
	}
	if session.liveDiscontinuitySequence != 1 {
		t.Fatalf("discontinuity sequence = %d, want 1 for the trimmed discontinuity", session.liveDiscontinuitySequence)
	}
}

func TestTrimLivePlaylistWindowTimeshiftKeepsMinimumEntries(t *testing.T) {
	// Long-GOP segments: even past the window, the ordinary count stays listed.
	session := &HLSSession{IsLive: true, LiveTuning: LiveTuningSettings{TimeshiftWindow: time.Minute}}
	session.livePlaylistWindow = timeshiftTestEntries(100, 14)
	trimLivePlaylistWindow(session)
	if got := len(session.livePlaylistWindow); got != liveNativeHLSListSize {
		t.Fatalf("timeshift window = %d entries, want at least %d", got, liveNativeHLSListSize)
	}
}

func TestApplyTimeshiftListSize(t *testing.T) {
	args := liveHLSOutputArgs("native", "/out/segment%d.ts", "/out/stream.m3u8")
	unchanged := strings.Join(applyTimeshiftListSize(append([]string(nil), args...), 0), " ")
	if !strings.Contains(unchanged, fmt.Sprintf("-hls_list_size %d", liveNativeHLSListSize)) {
		t.Fatalf("no window must keep the ordinary list size: %s", unchanged)
	}
	widened := strings.Join(applyTimeshiftListSize(args, 30*time.Minute), " ")
	want := fmt.Sprintf("-hls_list_size %d", 900+liveNativeHLSListSize)
	if !strings.Contains(widened, want) {
		t.Fatalf("30 min window args missing %q: %s", want, widened)
	}
	if strings.Contains(widened, "delete_segments") {
		t.Fatalf("timeshift must not use delete_segments: %s", widened)
	}
}

func TestApplyTimeshiftAudio(t *testing.T) {
	args := liveHLSOutputArgs("native", "/out/segment%d.ts", "/out/stream.m3u8")
	unchanged := strings.Join(applyTimeshiftAudio(append([]string(nil), args...), 0), " ")
	if !strings.Contains(unchanged, "-c:a copy") {
		t.Fatalf("no window must keep copied audio: %s", unchanged)
	}
	shifted := strings.Join(applyTimeshiftAudio(args, 30*time.Minute), " ")
	if !strings.Contains(shifted, "-c:a aac -ac 2 -b:a 128k -ar 48000") || strings.Contains(shifted, "-c:a copy") {
		t.Fatalf("timeshift must re-encode audio to stereo AAC: %s", shifted)
	}
	if !strings.Contains(shifted, "-c:v copy") {
		t.Fatalf("timeshift must keep copied video: %s", shifted)
	}
	web := strings.Join(applyTimeshiftAudio(liveHLSOutputArgs("web", "/out/segment%d.ts", "/out/stream.m3u8"), 30*time.Minute), " ")
	if strings.Count(web, "-c:a") != 1 {
		t.Fatalf("web args already encode audio and must not change: %s", web)
	}
}

func TestPruneTimeshiftSegmentFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, modTime time.Time) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("ts"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	write("segment1.ts", now)                   // below the cutoff
	write("segment5.ts", now.Add(-2*time.Hour)) // too old
	write("segment10.ts", now)                  // kept
	write("segment11.ts", now)                  // kept
	write("stream.m3u8", now.Add(-2*time.Hour)) // not a segment

	deleted := pruneTimeshiftSegmentFiles(dir, 3, now.Add(-time.Hour))
	if deleted != 2 {
		t.Fatalf("deleted %d files, want 2", deleted)
	}
	for _, name := range []string{"segment10.ts", "segment11.ts", "stream.m3u8"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should remain: %v", name, err)
		}
	}
	// A negative cutoff (nothing listed yet) deletes by age only.
	write("segment0.ts", now)
	if deleted := pruneTimeshiftSegmentFiles(dir, -1, now.Add(-time.Hour)); deleted != 0 {
		t.Fatalf("negative cutoff deleted %d fresh files", deleted)
	}
}

// A native timeshift session over an AC3 5.1 channel must publish stereo AAC with the video copied.
func TestTimeshiftLiveSessionReencodesAC3ToAAC(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fixture := filepath.Join(t.TempDir(), "live.ts")
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "3", "-c:v", "libx264", "-g", "10", "-c:a", "ac3", "-ac", "6", "-f", "mpegts", fixture)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v: %s", err, output)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/live.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:3\n#EXTINF:3,\nsegment.ts\n#EXT-X-ENDLIST\n")
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	session := &HLSSession{ID: "timeshift-ac3", Path: upstream.URL + "/live.m3u8", OutputDir: t.TempDir(),
		PlaybackTarget: "native", IsLive: true, LastSegmentRequest: time.Now(),
		LiveTuning: LiveTuningSettings{TimeshiftWindow: 30 * time.Minute}}
	manager := &HLSManager{ffmpegPath: ffmpeg}
	if err := manager.startLiveTranscoding(ctx, session, 0); err != nil {
		t.Fatalf("transcode: %v", err)
	}
	probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,channels",
		"-of", "csv=p=0", filepath.Join(session.OutputDir, "segment0.ts"))
	output, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v: %s", err, output)
	}
	got := string(output)
	if !strings.Contains(got, "aac,audio,2") || !strings.Contains(got, "h264,video") || strings.Contains(got, "ac3") {
		t.Fatalf("timeshift output streams = %q, want copied h264 video and stereo aac audio", got)
	}
	if plan := session.outputPlan; plan == nil || plan.AudioCopied || plan.AudioCodec != "aac" || !plan.VideoCopied {
		t.Fatalf("output plan = %+v, want copied video and aac audio", plan)
	}
}

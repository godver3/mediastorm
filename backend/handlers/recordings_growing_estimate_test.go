package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

// lockedRecordingService lets a test change the recording while it streams.
type lockedRecordingService struct {
	fakeRecordingService
	mu sync.Mutex
}

func (f *lockedRecordingService) Get(id string) (*models.Recording, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fakeRecordingService.Get(id)
}

func (f *lockedRecordingService) setStatus(status models.RecordingStatus) {
	f.mu.Lock()
	f.recording.Status = status
	f.mu.Unlock()
}

func TestEstimatedRecordingSize(t *testing.T) {
	now := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	rec := models.Recording{StartAt: started, ActualStartAt: &started, EndAt: now.Add(20 * time.Minute), PaddingAfterSeconds: 300}
	// 1 MB/s for 600s so far, 25 more minutes with padding, 10% margin.
	if got, want := estimatedRecordingSize(rec, 600_000_000, now, 0), int64(600_000_000+1_000_000*1500*1.1); got < want-2 || got > want+2 {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
	// Running past its end: a minute ahead.
	rec.EndAt = now.Add(-10 * time.Minute)
	if got, want := estimatedRecordingSize(rec, 600_000_000, now, 0), int64(600_000_000+1_000_000*60*1.1); got < want-2 || got > want+2 {
		t.Fatalf("overrunning estimate = %d, want %d", got, want)
	}
	// In the first minutes the rate is low (measured from before the source
	// delivered anything): twice what it says.
	early := now.Add(-30 * time.Second)
	rec = models.Recording{StartAt: early, ActualStartAt: &early, EndAt: now.Add(100 * time.Second)}
	if got, want := estimatedRecordingSize(rec, 30_000_000, now, 0), int64(30_000_000+1_000_000*100*2); got < want-2 || got > want+2 {
		t.Fatalf("early estimate = %d, want %d", got, want)
	}
	// A recording created 20 minutes into its programme holds the 3 minutes it caught up so far
	// from the source's window (1 MB a second of content), not 20: 87 minutes are still to come.
	backdated := now.Add(-20 * time.Minute)
	rec = models.Recording{StartAt: backdated, ActualStartAt: &backdated, EndAt: now.Add(70 * time.Minute)}
	if got, want := estimatedRecordingSize(rec, 180_000_000, now, 3*time.Minute), int64(180_000_000+1_000_000*87*60*1.1); got < want-2 || got > want+2 {
		t.Fatalf("catching-up estimate = %d, want %d", got, want)
	}
}

// Once offered, a recording's size does not shrink: a player keeps the size
// of its first response and must not be refused below it later.
func TestOfferedRecordingSizeNeverShrinks(t *testing.T) {
	t.Cleanup(func() { growingEstimates.Delete("rec-shrink") })
	now := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	rec := models.Recording{ID: "rec-shrink", StartAt: started, ActualStartAt: &started, EndAt: now.Add(20 * time.Minute)}
	first := offeredRecordingSize(rec, 600_000_000, now, 0)
	// Later the rate turns out lower: the estimate would be smaller.
	if got := offeredRecordingSize(rec, 650_000_000, now.Add(5*time.Minute), 0); got != first {
		t.Fatalf("offered %d after %d", got, first)
	}
}

// HEAD answers with the headers only, at once, even at the recorded end.
func TestStreamGrowingRecordingEstimatedHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.ts")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	old := growingEstimateMinData
	growingEstimateMinData = 1
	t.Cleanup(func() { growingEstimateMinData = old; growingEstimates.Delete("rec-head") })
	started := time.Now().Add(-1000 * time.Second)
	rec := models.Recording{ID: "rec-head", Status: models.RecordingStatusRunning, OutputPath: path,
		StartAt: started, ActualStartAt: &started, EndAt: time.Now().Add(1000 * time.Second)}
	h := NewRecordingsHandler(&lockedRecordingService{fakeRecordingService: fakeRecordingService{recording: rec}}, nil)
	rr := httptest.NewRecorder()
	begin := time.Now()
	if err := h.streamGrowingRecordingEstimated(rr, httptest.NewRequest(http.MethodHead, "/stream?seekable=estimate", nil), rec, path); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || rr.Body.Len() != 0 || rr.Header().Get("Content-Length") == "" || time.Since(begin) > time.Second {
		t.Fatalf("HEAD: %d, %d bytes, length %q, %v", rr.Code, rr.Body.Len(), rr.Header().Get("Content-Length"), time.Since(begin))
	}
}

// followedRecordingService reports a recording's content, as the recordings service does for one
// catching up from its source's DVR window.
type followedRecordingService struct {
	lockedRecordingService
	content atomic.Int64
}

func (f *followedRecordingService) RecordedContent(string) (time.Duration, bool) {
	return time.Duration(f.content.Load()), true
}

// A recording created 20 minutes into its programme is sized by the content it holds, once FFmpeg
// reported it, not by the 20 minutes since its backdated start.
func TestStreamGrowingRecordingEstimatedWhileCatchingUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.ts")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	old := growingEstimateMinData
	growingEstimateMinData = 1
	t.Cleanup(func() { growingEstimateMinData = old; growingEstimates.Delete("rec-catching-up") })
	now := time.Now()
	started := now.Add(-20 * time.Minute)
	rec := models.Recording{ID: "rec-catching-up", Status: models.RecordingStatusRunning, OutputPath: path,
		StartAt: started, ActualStartAt: &started, EndAt: now.Add(70 * time.Minute)}
	service := &followedRecordingService{lockedRecordingService: lockedRecordingService{fakeRecordingService: fakeRecordingService{recording: rec}}}
	time.AfterFunc(300*time.Millisecond, func() { service.content.Store(int64(2 * time.Minute)) })
	h := NewRecordingsHandler(service, nil)
	rr := httptest.NewRecorder()
	if err := h.streamGrowingRecordingEstimated(rr, httptest.NewRequest(http.MethodHead, "/stream?seekable=estimate", nil), rec, path); err != nil {
		t.Fatal(err)
	}
	// 1000 bytes for 2 minutes of content, 88 minutes to come, 10% margin.
	length, _ := strconv.ParseInt(rr.Header().Get("Content-Length"), 10, 64)
	if want := 1000 + 1000.0/120*88*60*1.1; rr.Code != http.StatusOK || float64(length) < want*0.98 || float64(length) > want*1.02 {
		t.Fatalf("HEAD while catching up: %d, length %d, want about %.0f", rr.Code, length, want)
	}
}

// A recording that is deleted while a read waits at its end ends the response.
func TestStreamGrowingRecordingEstimatedDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.ts")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	old := growingEstimateMinData
	growingEstimateMinData = 1
	t.Cleanup(func() { growingEstimateMinData = old; growingEstimates.Delete("rec-deleted") })
	started := time.Now().Add(-1000 * time.Second)
	rec := models.Recording{ID: "rec-deleted", Status: models.RecordingStatusRunning, OutputPath: path,
		StartAt: started, ActualStartAt: &started, EndAt: time.Now().Add(1000 * time.Second)}
	h := NewRecordingsHandler(&fakeRecordingService{recording: models.Recording{ID: "gone"}}, nil) // Get finds nothing
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stream?seekable=estimate", nil)
	req.Header.Set("Range", "bytes=900-")
	done := make(chan error, 1)
	go func() { done <- h.streamGrowingRecordingEstimated(rr, req, rec, path) }()
	select {
	case err := <-done:
		if err != nil || rr.Body.Len() != 100 {
			t.Fatalf("deleted: %v, %d bytes", err, rr.Body.Len())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for a deleted recording")
	}
}

func TestParseSingleRange(t *testing.T) {
	for header, want := range map[string][3]int64{
		"bytes=0-":       {0, 999, 1},
		"bytes=100-199":  {100, 199, 1},
		"bytes=900-5000": {900, 999, 1},
		"bytes=-100":     {900, 999, 1},
		"bytes=5-2":      {0, 0, 0},
		"bytes=0-1,5-9":  {0, 0, 0},
		"items=0-":       {0, 0, 0},
	} {
		s, e, ok := parseSingleRange(header, 1000)
		if ok != (want[2] == 1) || (ok && (s != want[0] || e != want[1])) {
			t.Errorf("%q = %d-%d %v, want %v", header, s, e, ok, want)
		}
	}
}

// A running recording as a seekable file of its estimated final size: what is
// recorded comes at once, a read at the recorded end waits for more, the end
// of the recording ends the response, and ranges far ahead are refused.
func TestStreamGrowingRecordingEstimated(t *testing.T) {
	old := growingEstimateMinData
	growingEstimateMinData = 1
	t.Cleanup(func() { growingEstimateMinData = old; growingEstimates.Delete("rec-1") })
	dir := t.TempDir()
	path := filepath.Join(dir, "recording.ts")
	data := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-1000 * time.Second) // 1 byte/s so far
	rec := models.Recording{ID: "rec-1", Status: models.RecordingStatusRunning, OutputPath: path,
		StartAt: started, ActualStartAt: &started, EndAt: time.Now().Add(1000 * time.Second)}
	service := &lockedRecordingService{fakeRecordingService: fakeRecordingService{recording: rec}}
	h := NewRecordingsHandler(service, nil)
	serve := func(rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/stream?seekable=estimate", nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		rr := httptest.NewRecorder()
		service.mu.Lock()
		current := service.recording
		service.mu.Unlock()
		if err := h.streamGrowingRecordingEstimated(rr, req, current, path); err != nil {
			t.Fatal(err)
		}
		return rr
	}

	rr := serve("bytes=0-99")
	total := int64(0)
	if cr := rr.Header().Get("Content-Range"); rr.Code != http.StatusPartialContent || !strings.HasPrefix(cr, "bytes 0-99/") || rr.Body.Len() != 100 {
		t.Fatalf("first range: %d %q %d bytes", rr.Code, cr, rr.Body.Len())
	} else {
		total, _ = strconv.ParseInt(strings.TrimPrefix(cr, "bytes 0-99/"), 10, 64)
	}
	if total < 2000 || rr.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("estimated total %d (want about 1000 + 1100), accept-ranges %q", total, rr.Header().Get("Accept-Ranges"))
	}
	// The end of the file, as a duration probe reads it: the end of what is
	// recorded, so the player sees the length recorded so far.
	if rr := serve("bytes=" + strconv.FormatInt(total-100, 10) + "-"); rr.Code != http.StatusPartialContent || rr.Body.String() != string(data[900:]) {
		t.Fatalf("duration probe: %d %q", rr.Code, rr.Body.String())
	}
	// From the recorded end on: waits for the recorder, then ends with it.
	go func() {
		time.Sleep(300 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = f.Write(bytes.Repeat([]byte("x"), 200))
		_ = f.Close()
		time.Sleep(700 * time.Millisecond)
		service.setStatus(models.RecordingStatusCompleted)
	}()
	rr = serve("bytes=900-")
	if rr.Code != http.StatusPartialContent || rr.Body.Len() != 300 || !strings.HasSuffix(rr.Body.String(), strings.Repeat("x", 200)) {
		t.Fatalf("growing read: %d, %d bytes", rr.Code, rr.Body.Len())
	}
}

// What a file player (mpv, on FFmpeg's libraries) makes of a running
// recording served this way: the length recorded when it opened (not the few
// minutes it read while opening), and playback that goes on as the recording
// grows and ends with it.
func TestStreamGrowingRecordingEstimatedEndToEnd(t *testing.T) {
	if os.Getenv("MEDIASTORM_FFMPEG_E2E") != "1" {
		t.Skip("set MEDIASTORM_FFMPEG_E2E=1 to run against ffmpeg")
	}
	t.Cleanup(func() { growingEstimates.Delete("rec-1") })
	dir := t.TempDir()
	full := filepath.Join(dir, "full.ts")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "60", "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-b:v", "1M",
		"-c:a", "aac", "-f", "mpegts", full)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	data, _ := os.ReadFile(full)
	path := filepath.Join(dir, "recording.ts")
	third := len(data) / 3 / 188 * 188
	if err := os.WriteFile(path, data[:third], 0o644); err != nil {
		t.Fatal(err)
	}
	// 20s of 60 recorded.
	started := time.Now().Add(-20 * time.Second)
	rec := models.Recording{ID: "rec-1", Status: models.RecordingStatusRunning, OutputPath: path,
		StartAt: started, ActualStartAt: &started, EndAt: started.Add(60 * time.Second)}
	service := &lockedRecordingService{fakeRecordingService: fakeRecordingService{recording: rec}}
	h := NewRecordingsHandler(service, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.mu.Lock()
		current := service.recording
		service.mu.Unlock()
		_ = h.streamGrowingRecordingEstimated(w, r, current, path)
	}))
	defer srv.Close()

	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", srv.URL+"/rec.ts").Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	duration, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if duration < 18 || duration > 23 {
		t.Fatalf("player sees %.1fs, want the 20s recorded so far", duration)
	}

	// The recorder writes the rest over a few seconds, then stops.
	go func() {
		for off := third; off < len(data); {
			end := min(off+len(data)/10, len(data))
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			_, _ = f.Write(data[off:end])
			_ = f.Close()
			off = end
			time.Sleep(300 * time.Millisecond)
		}
		time.Sleep(time.Second)
		service.setStatus(models.RecordingStatusCompleted)
	}()
	play := exec.Command("ffmpeg", "-v", "error", "-progress", "pipe:1", "-i", srv.URL+"/rec.ts", "-map", "0:v", "-c", "copy", "-f", "null", "-")
	var stderr bytes.Buffer
	play.Stderr = &stderr
	// The recording ends short of the estimated size: FFmpeg reports the
	// early end of the stream, which is how it learns that it ended.
	progress, playErr := play.Output()
	played := 0.0
	for _, line := range strings.Split(string(progress), "\n") {
		if v, ok := strings.CutPrefix(line, "out_time_us="); ok {
			if us, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				played = us / 1e6
			}
		}
	}
	if played < 58 {
		t.Fatalf("played %.1fs of 60 (stopped where the recording was when opened?): %v %s", played, playErr, stderr.String())
	}
	t.Logf("played %.1fs; ffmpeg: %v %s", played, playErr, strings.TrimSpace(stderr.String()))
}

// The handler asks its service for a recording's content directly: a service that follows it
// is heard, one that does not reports nothing followed.
func TestRecordedContentComesFromTheServiceInterface(t *testing.T) {
	plain := NewRecordingsHandler(&fakeRecordingService{}, nil)
	if content, followed := plain.recordedContent("x"); followed || content != 0 {
		t.Fatalf("plain service: %v %v", content, followed)
	}
	f := &followedRecordingService{}
	f.content.Store(int64(3 * time.Minute))
	if content, followed := NewRecordingsHandler(f, nil).recordedContent("x"); !followed || content != 3*time.Minute {
		t.Fatalf("followed service: %v %v", content, followed)
	}
}

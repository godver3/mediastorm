package handlers

import (
	"fmt"
	"os"
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

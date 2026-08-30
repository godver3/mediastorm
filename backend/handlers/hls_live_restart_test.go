package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A restart must advance segment numbering rather than starting over at 0, while avoiding append_list
// which corrupts EXT-X-MEDIA-SEQUENCE when mixed with a rolling playlist window.
func TestLiveHLSOutputArgsResumeAdvancesAndMarksDiscontinuity(t *testing.T) {
	fresh := strings.Join(liveHLSOutputArgs("native", "/out/segment%d.ts", "/out/stream.m3u8", 0), " ")
	if strings.Contains(fresh, "append_list") || strings.Contains(fresh, "discont_start") {
		t.Fatalf("a fresh session must not append or mark a discontinuity: %s", fresh)
	}
	if strings.Contains(fresh, "-start_number") {
		t.Fatalf("a fresh session must not set a start number: %s", fresh)
	}

	resumed := strings.Join(liveHLSOutputArgs("native", "/out/segment%d.ts", "/out/stream.m3u8", 207), " ")
	if strings.Contains(resumed, "append_list") {
		t.Fatalf("a restart must not use append_list on rolling live playlists: %s", resumed)
	}
	// Without this the player tries to decode across the seam instead of re-initialising.
	if !strings.Contains(resumed, "discont_start") {
		t.Fatalf("a restart must mark the timeline cut with discont_start: %s", resumed)
	}
	if !strings.Contains(resumed, "-start_number 207") {
		t.Fatalf("a restart must continue the segment numbering: %s", resumed)
	}
	// The copy decision belongs to the target, not the restart.
	if !strings.Contains(resumed, "-c:v copy") {
		t.Fatalf("a native restart must still transmux: %s", resumed)
	}
}

// The number a restart continues from is read off disk, so it stays true even when the restart
// happens in a process that never saw the earlier segments.
func TestNextLiveSegmentNumber(t *testing.T) {
	dir := t.TempDir()
	if got := nextLiveSegmentNumber(dir); got != 0 {
		t.Fatalf("empty dir: got %d, want 0", got)
	}

	for _, name := range []string{"segment0.ts", "segment7.ts", "segment206.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Files that are not numbered segments must not be counted: a playlist, a caption track, and
	// FFmpeg's own temp file all live in this directory.
	for _, name := range []string{"stream.m3u8", "captions.srt", "segment207.ts.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if got := nextLiveSegmentNumber(dir); got != 207 {
		t.Fatalf("got %d, want 207 (one past the highest segment)", got)
	}

	if got := nextLiveSegmentNumber(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing dir: got %d, want 0", got)
	}
}

func TestBuildSeamlessLivePlaylist(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{
		ID:     "test-live-seamless",
		IsLive: true,
	}

	// Initial run: segments 0..2
	initialOnDisk := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:4",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:2.000000,",
		"segment0.ts",
		"#EXTINF:2.000000,",
		"segment1.ts",
		"#EXTINF:2.000000,",
		"segment2.ts",
	}, "\n")

	p1 := m.buildSeamlessLivePlaylist(session, initialOnDisk)
	if !strings.Contains(p1, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Fatalf("expected media sequence 0, got:\n%s", p1)
	}
	if !strings.Contains(p1, "segment0.ts") || !strings.Contains(p1, "segment2.ts") {
		t.Fatalf("expected all initial segments, got:\n%s", p1)
	}

	// Resumed run after restart at segment 3 with discontinuity
	resumedOnDisk := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:4",
		"#EXT-X-MEDIA-SEQUENCE:3",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:2.000000,",
		"segment3.ts",
		"#EXTINF:2.000000,",
		"segment4.ts",
	}, "\n")

	p2 := m.buildSeamlessLivePlaylist(session, resumedOnDisk)
	// The merged playlist must retain the previous window starting at 0
	if !strings.Contains(p2, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Fatalf("expected media sequence 0 to be preserved across restart, got:\n%s", p2)
	}
	// Discontinuity must be placed before segment 3
	if !strings.Contains(p2, "#EXT-X-DISCONTINUITY\n#EXTINF:2.000000,\nsegment3.ts") {
		t.Fatalf("expected discontinuity before segment3.ts, got:\n%s", p2)
	}
	// Both old and new segments must be present in consecutive order
	if !strings.Contains(p2, "segment0.ts") || !strings.Contains(p2, "segment4.ts") {
		t.Fatalf("expected consecutive old and new segments, got:\n%s", p2)
	}
}

func liveTestPlaylist(mediaSequence, count, targetDuration int, discontinuity bool) string {
	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		fmt.Sprintf("#EXT-X-TARGETDURATION:%d", targetDuration),
		fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", mediaSequence),
	}
	for i := 0; i < count; i++ {
		if discontinuity && i == 0 {
			lines = append(lines, "#EXT-X-DISCONTINUITY")
		}
		lines = append(lines, "#EXTINF:2.000000,", fmt.Sprintf("segment%d.ts", mediaSequence+i))
	}
	return strings.Join(lines, "\n")
}

func TestBuildSeamlessLivePlaylistCarriesDiscontinuitySequenceWhenSeamRollsOut(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{ID: "discontinuity-sequence", IsLive: true}

	m.buildSeamlessLivePlaylist(session, liveTestPlaylist(0, liveCompatibilityHLSListSize, 2, false))
	playlist := m.buildSeamlessLivePlaylist(session, liveTestPlaylist(10, 1, 2, true))
	if !strings.Contains(playlist, "#EXT-X-DISCONTINUITY\n#EXTINF:2.000000,\nsegment10.ts") {
		t.Fatalf("restart seam was not retained:\n%s", playlist)
	}

	for sequence := 11; sequence <= 20; sequence++ {
		playlist = m.buildSeamlessLivePlaylist(session, liveTestPlaylist(sequence, 1, 2, false))
	}
	if !strings.Contains(playlist, "#EXT-X-MEDIA-SEQUENCE:11") {
		t.Fatalf("rolling window has the wrong first media sequence:\n%s", playlist)
	}
	if !strings.Contains(playlist, "#EXT-X-DISCONTINUITY-SEQUENCE:1") {
		t.Fatalf("removed seam did not advance discontinuity sequence:\n%s", playlist)
	}
	if strings.Contains(playlist, "#EXT-X-DISCONTINUITY\n") {
		t.Fatalf("a rolled-out seam must not remain in the media entries:\n%s", playlist)
	}
}

func TestBuildSeamlessLivePlaylistIgnoresAStaleConcurrentRead(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{ID: "stale-read", IsLive: true}

	stale := liveTestPlaylist(10, liveCompatibilityHLSListSize, 2, false)
	m.buildSeamlessLivePlaylist(session, stale)
	newer := m.buildSeamlessLivePlaylist(session, liveTestPlaylist(20, 1, 2, false))
	if !strings.Contains(newer, "#EXT-X-MEDIA-SEQUENCE:11") {
		t.Fatalf("new segment did not advance the window:\n%s", newer)
	}
	afterStale := m.buildSeamlessLivePlaylist(session, stale)
	if !strings.Contains(afterStale, "#EXT-X-MEDIA-SEQUENCE:11") || !strings.Contains(afterStale, "segment20.ts") {
		t.Fatalf("stale read moved the playlist backwards:\n%s", afterStale)
	}
}

func TestBuildSeamlessLivePlaylistKeepsTargetDurationStable(t *testing.T) {
	m := &HLSManager{}
	session := &HLSSession{ID: "stable-target", IsLive: true, PlaybackTarget: "native"}

	m.buildSeamlessLivePlaylist(session, liveTestPlaylist(0, 1, 14, false))
	playlist := m.buildSeamlessLivePlaylist(session, liveTestPlaylist(1, 1, 2, false))
	if !strings.Contains(playlist, "#EXT-X-TARGETDURATION:14") {
		t.Fatalf("target duration changed across playlist updates:\n%s", playlist)
	}
}

func TestLivePlaylistStartTagUsesThreeTargetDurations(t *testing.T) {
	compat := liveTestPlaylist(0, 7, 2, false)
	if got := livePlaylistStartTag(compat); got != "#EXT-X-START:TIME-OFFSET=-14,PRECISE=YES" {
		t.Fatalf("compat start tag = %q", got)
	}

	shortNative := strings.Join([]string{
		"#EXTM3U", "#EXT-X-TARGETDURATION:14", "#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:11.633,", "segment0.ts",
		"#EXTINF:8.750,", "segment1.ts",
		"#EXTINF:8.967,", "segment2.ts",
	}, "\n")
	if got := livePlaylistStartTag(shortNative); got != "" {
		t.Fatalf("short native playlist must omit an out-of-window start tag, got %q", got)
	}

	fullNative := shortNative + "\n#EXTINF:14.000,\nsegment3.ts"
	if got := livePlaylistStartTag(fullNative); got != "#EXT-X-START:TIME-OFFSET=-42,PRECISE=YES" {
		t.Fatalf("native start tag = %q", got)
	}
}

func TestLiveStallTimeoutUsesPlaylistTargetDuration(t *testing.T) {
	dir := t.TempDir()
	if got := liveStallTimeoutForOutputDir(dir); got != liveStallTimeoutFloor {
		t.Fatalf("missing playlist timeout = %v, want %v", got, liveStallTimeoutFloor)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(liveTestPlaylist(0, 1, 14, false)), 0o600); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
	if got, want := liveStallTimeoutForOutputDir(dir), 42*time.Second; got != want {
		t.Fatalf("target-derived timeout = %v, want %v", got, want)
	}
}

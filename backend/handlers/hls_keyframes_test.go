package handlers

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memRangeSource []byte

func (m memRangeSource) Size() int64 { return int64(len(m)) }

func (m memRangeSource) Close() error { return nil }

func (m memRangeSource) ReadRange(_ context.Context, offset, length int64) ([]byte, error) {
	if offset >= int64(len(m)) {
		return nil, io.EOF
	}
	end := offset + length
	if end > int64(len(m)) {
		end = int64(len(m))
	}
	return append([]byte(nil), m[offset:end]...), nil
}

// --- EBML builders ---

func ebmlIDBytes(id uint64) []byte {
	switch {
	case id > 0xFFFFFF:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		return []byte{byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id)}
	}
}

func ebmlSizeBytes(n int) []byte {
	// Always 8-byte sizes: simple and valid.
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	b[0] = 0x01
	return b
}

func ebmlEl(id uint64, children ...[]byte) []byte {
	payload := bytes.Join(children, nil)
	return append(append(ebmlIDBytes(id), ebmlSizeBytes(len(payload))...), payload...)
}

func ebmlUintEl(id uint64, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return ebmlEl(id, b)
}

// buildMKV makes a minimal Matroska file: header, segment with Info, Tracks
// (audio track 1, video track 2), one cluster placeholder, and Cues at the end
// reachable through the SeekHead.
func buildMKV(t *testing.T, cueTimesMS []uint64, timecodeScale uint64) []byte {
	t.Helper()
	header := ebmlEl(ebmlIDHeader, ebmlUintEl(0x4286, 1))
	info := ebmlEl(mkvIDInfo, ebmlUintEl(mkvIDTimecodeScale, timecodeScale))
	tracks := ebmlEl(mkvIDTracks,
		ebmlEl(mkvIDTrackEntry, ebmlUintEl(mkvIDTrackNumber, 1), ebmlUintEl(mkvIDTrackType, 2)),
		ebmlEl(mkvIDTrackEntry, ebmlUintEl(mkvIDTrackNumber, 2), ebmlUintEl(mkvIDTrackType, 1)),
	)
	cluster := ebmlEl(mkvIDCluster, make([]byte, 300*1024)) // pushes Cues past the first read
	var points [][]byte
	for _, ms := range cueTimesMS {
		points = append(points, ebmlEl(mkvIDCuePoint,
			ebmlUintEl(mkvIDCueTime, ms),
			ebmlEl(mkvIDCueTrackPos, ebmlUintEl(mkvIDCueTrack, 2)),
		))
		// An audio-only cue must be ignored.
		points = append(points, ebmlEl(mkvIDCuePoint,
			ebmlUintEl(mkvIDCueTime, ms+1),
			ebmlEl(mkvIDCueTrackPos, ebmlUintEl(mkvIDCueTrack, 1)),
		))
	}
	cues := ebmlEl(mkvIDCues, points...)

	// SeekHead size is fixed (8-byte uints), so compute positions in two passes.
	seekHead := func(cuesPos uint64) []byte {
		return ebmlEl(mkvIDSeekHead,
			ebmlEl(mkvIDSeek, ebmlEl(mkvIDSeekID, ebmlIDBytes(mkvIDCues)), ebmlUintEl(mkvIDSeekPosition, cuesPos)),
		)
	}
	sh := seekHead(0)
	cuesPos := uint64(len(sh) + len(info) + len(tracks) + len(cluster))
	segPayload := bytes.Join([][]byte{seekHead(cuesPos), info, tracks, cluster, cues}, nil)
	segment := append(append(ebmlIDBytes(mkvIDSegment), ebmlSizeBytes(len(segPayload))...), segPayload...)
	return append(header, segment...)
}

func TestMatroskaKeyframesFromCues(t *testing.T) {
	file := buildMKV(t, []uint64{0, 2002, 6006, 10010}, 1_000_000)
	times, err := matroskaKeyframes(context.Background(), memRangeSource(file))
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0, 2.002, 6.006, 10.010}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if math.Abs(times[i]-want[i]) > 1e-9 {
			t.Fatalf("times = %v, want %v", times, want)
		}
	}
}

func TestMatroskaWithoutCuesFails(t *testing.T) {
	file := buildMKV(t, nil, 1_000_000)
	if _, err := matroskaKeyframes(context.Background(), memRangeSource(file)); err == nil {
		t.Fatal("expected an error for a file without video cues")
	}
}

// --- MP4 builders ---

func mp4BoxBytes(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func u32s(vals ...uint32) []byte {
	b := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.BigEndian.PutUint32(b[i*4:], v)
	}
	return b
}

func TestMP4KeyframesFromSampleTables(t *testing.T) {
	// 24 fps at timescale 24000: 1000 ticks per frame, 240 frames, keyframes
	// every 48 frames; ctts shifts every frame by +2000 and the edit list
	// removes that 2000-tick delay again.
	hdlr := mp4BoxBytes("hdlr", u32s(0, 0), []byte("vide"), make([]byte, 12))
	mdhd := mp4BoxBytes("mdhd", u32s(0, 0, 0, 24000, 240000), make([]byte, 4))
	stts := mp4BoxBytes("stts", u32s(0, 1, 240, 1000))
	stss := mp4BoxBytes("stss", u32s(0, 5, 1, 49, 97, 145, 193))
	ctts := mp4BoxBytes("ctts", u32s(0, 1, 240, 2000))
	elst := mp4BoxBytes("elst", u32s(0, 1, 240000, 2000, 0x00010000))
	stbl := mp4BoxBytes("stbl", stts, stss, ctts)
	minf := mp4BoxBytes("minf", stbl)
	mdia := mp4BoxBytes("mdia", mdhd, hdlr, minf)
	trak := mp4BoxBytes("trak", mp4BoxBytes("edts", elst), mdia)
	file := bytes.Join([][]byte{
		mp4BoxBytes("ftyp", []byte("isom"), u32s(0)),
		mp4BoxBytes("mdat", make([]byte, 1024)),
		mp4BoxBytes("moov", trak),
	}, nil)

	times, err := mp4Keyframes(context.Background(), memRangeSource(file))
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0, 2, 4, 6, 8}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if math.Abs(times[i]-want[i]) > 1e-9 {
			t.Fatalf("times = %v, want %v", times, want)
		}
	}
	// Sniffing routes an unknown extension to the MP4 parser.
	if sniffed, err := sniffKeyframes(context.Background(), memRangeSource(file), 0, true); err != nil || len(sniffed) != len(want) {
		t.Fatalf("sniffed = %v, %v", sniffed, err)
	}
}

func TestGroupKeyframeSegments(t *testing.T) {
	keyframes := []float64{0, 1.5, 3.0, 4.2, 5.0, 9.1, 12.0, 13.5, 30.0, 33.0}
	starts, err := groupKeyframeSegments(keyframes, 0, 40, false)
	if err != nil {
		t.Fatal(err)
	}
	// 1.5/3.0 are under 4s from the start, 5.0 under 4s from 4.2, 33.0 under 4s from 30.0.
	want := []float64{0, 4.2, 9.1, 13.5, 30.0}
	if len(starts) != len(want) {
		t.Fatalf("starts = %v, want %v", starts, want)
	}
	for i := range want {
		if starts[i] != want[i] {
			t.Fatalf("starts = %v, want %v", starts, want)
		}
	}
	// Probe start time shifts the index onto FFmpeg's -start_at_zero clock.
	shifted, _ := groupKeyframeSegments([]float64{1, 6, 11}, 1, 20, false)
	if len(shifted) != 3 || shifted[1] != 5 || shifted[2] != 10 {
		t.Fatalf("shifted = %v", shifted)
	}
	// A 25s gap is too sparse to seek with.
	if _, err := groupKeyframeSegments([]float64{0, 25}, 0, 30, false); err == nil {
		t.Fatal("expected sparse index to be rejected")
	}
	// The last segment may run longer: a 2160p WEB release of a film had
	// keyframes every 4s up to 5992.2s and ended at 6015.6s.
	var film []float64
	for t := 0.0; t <= 5988; t += 4 {
		film = append(film, t)
	}
	film = append(film, 5992.2)
	if starts, err := groupKeyframeSegments(film, 0, 6015.6, false); err != nil || starts[len(starts)-1] != 5992.2 {
		t.Fatalf("long final segment rejected: %v", err)
	}
	if _, err := groupKeyframeSegments([]float64{0, 4, 8}, 0, 8+stableCopyMaxFinalSegment+1, false); err == nil {
		t.Fatal("expected an over-long final segment to be rejected")
	}
	// With transcoding disabled the gaps are accepted (an episode whose index
	// ended 104s before the end failed to play otherwise).
	if starts, err := groupKeyframeSegments([]float64{0, 25, 30}, 0, 30+104, true); err != nil || len(starts) != 3 || starts[1] != 25 {
		t.Fatalf("long segments with transcoding disabled = %v, %v", starts, err)
	}
	// An index without keyframes stays unusable.
	if _, err := groupKeyframeSegments(nil, 0, 120, true); err == nil {
		t.Fatal("expected an empty index to be rejected")
	}
}

func TestStableCopyPlaylistAndCuts(t *testing.T) {
	session := &HLSSession{StableCopy: true, CopySegmentStarts: []float64{0, 4.2, 9.1, 13.5}, Duration: 20}
	playlist := buildStableCopyPlaylist(session)
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:7\n",
		"#EXTINF:4.200000,\nsegment0.ts\n",
		"#EXTINF:4.900000,\nsegment1.ts\n",
		"#EXTINF:6.500000,\nsegment3.ts\n",
		"#EXT-X-ENDLIST\n",
	} {
		if !strings.Contains(playlist, want) {
			t.Fatalf("playlist missing %q:\n%s", want, playlist)
		}
	}
	for _, absolute := range []bool{false, true} {
		if got := stableCopySegmentTimes(session.CopySegmentStarts, 0, absolute); got != "4.200,9.100,13.500" {
			t.Fatalf("cut times from 0 (absolute %v) = %q", absolute, got)
		}
	}
	// A restarted run: relative to its first segment (FFmpeg 7), or the
	// timeline times themselves (FFmpeg 5).
	if got := stableCopySegmentTimes(session.CopySegmentStarts, 2, false); got != "4.400" {
		t.Fatalf("relative cut times from 2 = %q", got)
	}
	if got := stableCopySegmentTimes(session.CopySegmentStarts, 2, true); got != "13.500" {
		t.Fatalf("absolute cut times from 2 = %q", got)
	}
	if session.stableCopySegmentIndex(10) != 2 || session.stableCopySegmentIndex(0) != 0 || session.stableCopySegmentIndex(19) != 3 {
		t.Fatal("segment index lookup wrong")
	}
}

func TestStableCopyRestartPlanUsesKeyframes(t *testing.T) {
	session := &HLSSession{
		StableCopy:        true,
		CopySegmentStarts: []float64{0, 4.2, 9.1, 13.5, 18.0, 22.0, 26.0},
		Duration:          30,
		LastSegmentServed: 1,
	}
	plan, ok := castSegmentRestartPlan(session, 6, 1)
	if !ok || plan.SegmentStartNumber != 6 || plan.TranscodingOffset != 26.0 || plan.OutputTimestampOffset != 26.0 {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	// Nearby requests wait for the running FFmpeg instead of restarting.
	if _, ok := castSegmentRestartPlan(session, 2, 1); ok {
		t.Fatal("next segment must not restart FFmpeg")
	}
	// Recovery resumes at the first segment of the run that was not closed,
	// ignoring half-written or far-ahead files that inflate the highest number.
	session.OutputDir = t.TempDir()
	session.SegmentStartNumber = 1
	session.copyCompleted = map[int]bool{1: true, 2: true}
	offset, resume := (&HLSManager{}).recoveryResumePoint(session, 5)
	if offset != 13.5 || resume != 3 {
		t.Fatalf("recovery = %.1f, %d", offset, resume)
	}
	// Handing segments to a new run deletes stale files from that point on,
	// so they cannot look like progress of the new run.
	for _, name := range []string{"segment2.ts", "segment3.ts", "segment9.ts"} {
		if err := os.WriteFile(filepath.Join(session.OutputDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	(&HLSManager{}).resetStableCopyFrom(session, 3)
	if _, err := os.Stat(filepath.Join(session.OutputDir, "segment2.ts")); err != nil {
		t.Fatal("segment before the run start was deleted")
	}
	for _, name := range []string{"segment3.ts", "segment9.ts"} {
		if _, err := os.Stat(filepath.Join(session.OutputDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the reset", name)
		}
	}
	// A segment before the current run's start always restarts FFmpeg.
	session.SegmentStartNumber = 4
	session.LastSegmentServed = 2
	if plan, ok := castSegmentRestartPlan(session, 3, 5); !ok || plan.SegmentStartNumber != 3 {
		t.Fatalf("behind-run plan = %+v ok=%v", plan, ok)
	}
}

func TestKeyframeParsersRejectHostileInput(t *testing.T) {
	ctx := context.Background()
	// EBML header whose size points far past the data.
	hostileMKV := append(ebmlIDBytes(ebmlIDHeader), 0x01, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE)
	if _, err := matroskaKeyframes(ctx, memRangeSource(hostileMKV)); err == nil {
		t.Fatal("oversized EBML header accepted")
	}
	// moov whose child claims a 64-bit size near MaxInt64.
	child := []byte{0, 0, 0, 1, 't', 'r', 'a', 'k', 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF0}
	if boxes := mp4Children(child); len(boxes) != 0 {
		t.Fatalf("overflowing box parsed: %v", boxes)
	}
	// stts claiming billions of samples must not allocate per sample.
	hdlr := mp4BoxBytes("hdlr", u32s(0, 0), []byte("vide"), make([]byte, 12))
	mdhd := mp4BoxBytes("mdhd", u32s(0, 0, 0, 1000, 1000), make([]byte, 4))
	stts := mp4BoxBytes("stts", u32s(0, 2, 0xFFFFFFFF, 1, 0xFFFFFFFF, 1))
	stss := mp4BoxBytes("stss", u32s(0, 2, 1, 4000000000))
	trak := mp4BoxBytes("trak", mp4BoxBytes("mdia", mdhd, hdlr, mp4BoxBytes("minf", mp4BoxBytes("stbl", stts, stss))))
	file := append(mp4BoxBytes("ftyp", []byte("isom")), mp4BoxBytes("moov", trak)...)
	times, err := mp4Keyframes(ctx, memRangeSource(file))
	if err != nil || len(times) != 2 || times[1] != 3999999.999 {
		t.Fatalf("times = %v, err = %v", times, err)
	}
}

// A Segment larger than 2 GiB (any long 1080p remux) must still be indexed;
// only its declared size differs from a small file.
func TestMatroskaKeyframesLargeSegment(t *testing.T) {
	file := buildMKV(t, []uint64{0, 2002, 6006}, 1_000_000)
	header := ebmlEl(ebmlIDHeader, ebmlUintEl(0x4286, 1))
	sizeAt := len(header) + len(ebmlIDBytes(mkvIDSegment))
	size := ebmlSizeBytes(0)
	binary.BigEndian.PutUint64(size, 5<<30) // 5 GiB
	size[0] = 0x01
	copy(file[sizeAt:], size)
	times, err := matroskaKeyframes(context.Background(), memRangeSource(file))
	if err != nil || len(times) != 3 {
		t.Fatalf("large segment: times=%v err=%v", times, err)
	}
}

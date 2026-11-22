package handlers

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"
)

// Growing stable copy: a recording that is still being written plays on the
// same keyframe-cut copy timeline as a finished one (hls_keyframes.go), so
// Jellyfin clients get a playlist that starts at 0, can be seeked anywhere
// that has been recorded, and keeps its segment numbering when FFmpeg is
// restarted.
//
//   - The keyframe index is built from the file as it is at session start
//     (sampled like any MPEG-TS), then extended from where the last scan
//     stopped whenever the playlist is fetched (at most every
//     growingCopyRescanInterval).
//   - The playlist is an EVENT playlist without ENDLIST. It lists every
//     segment whose end is known, so the last indexed segment (still open)
//     is left out until the next keyframe after it has been written.
//   - FFmpeg follows the file (hls_growing.go) and cuts at the times known
//     when the run started. The segment the run was given last stays open
//     in that run; a request for it once the index knows its end restarts
//     FFmpeg there with the longer list (see growingCopyTail).
//   - When the recording stops, the index is read to the end of the file,
//     the session gets its real duration and the playlist becomes VOD with
//     ENDLIST.

const (
	// growingCopyRescanInterval limits how often a playlist fetch reads the
	// new part of the file.
	growingCopyRescanInterval = 2 * time.Second
	// growingCopyTailBatch is how many segments past the running FFmpeg
	// run's open segment must be known before the playlist lists it (see
	// holdGrowingCopyTail).
	growingCopyTailBatch = 3
	// growingCopyMinSegments is how many segments the file must already
	// hold: the first one must close (the session waits for that) and the
	// playlist must list something.
	growingCopyMinSegments = 3
	// growingCopyTailScanBytes is how much of the file end is read for the
	// last video timestamp once the recording has stopped.
	growingCopyTailScanBytes = 8 << 20
	// growingCopyPATLookback is how far before a keyframe a restarted run
	// looks for the program tables (sent at least every 0.5s in DVB).
	growingCopyPATLookback = 4 << 20
)

// growingCopyIndex is the keyframe index of a recording still being written.
type growingCopyIndex struct {
	mu sync.Mutex

	path, originalPath string
	// scanner holds the packet layout and video stream found at the start;
	// each scan gives it a fresh source (the file size changes).
	scanner tsScanner
	// startTime is the timeline's zero on the PTS clock (the probe's start
	// time, in seconds); firstPTS is the first indexed keyframe's raw PTS,
	// the reference for 33-bit clock wraps.
	startTime float64
	firstPTS  int64
	// offsets[n] is where segment n's keyframe PES starts in the file (0 for
	// segment 0), so a run can start there without seeking (see
	// restartOffset).
	offsets []int64
	// wraps[n] is true when segment n's keyframe comes after a 33-bit PTS
	// wrap: a run started there by byte offset would see the small raw
	// timestamps with nothing to unwrap them against, so it seeks instead.
	wraps []bool
	// scannedTo is the byte offset the next scan starts at: just past the
	// last keyframe found.
	scannedTo int64
	// last is the timeline time of the last segment start.
	last     float64
	checked  time.Time
	finished bool
}

// tsKeyframeAt is a keyframe's raw PTS and where its PES starts.
type tsKeyframeAt struct {
	pts int64
	at  int64
}

// planGrowingCopySegments indexes the part of a running recording written so
// far. Only MPEG-TS recordings are supported. The file is sampled like any
// transport stream (hls_keyframes_mpegts.go), keeping each keyframe's offset.
func (m *HLSManager) planGrowingCopySegments(ctx context.Context, path, originalPath string, probe *UnifiedProbeResult, writtenDuration float64) (starts []float64, index *growingCopyIndex, err error) {
	defer func() {
		if r := recover(); r != nil {
			starts, index, err = nil, nil, fmt.Errorf("%w: index parser panic: %v", errNoKeyframeIndex, r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, stableCopyIndexTimeout)
	defer cancel()
	src, err := m.openRangeSource(ctx, path, originalPath)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()
	head, err := src.ReadRange(ctx, 0, 64*1024)
	if err != nil {
		return nil, nil, err
	}
	pktSize, off, ok := tsPacketLayout(head)
	if !ok {
		return nil, nil, fmt.Errorf("%w: a growing source must be MPEG-TS", errNoKeyframeIndex)
	}
	syncOff := int64(off)
	if pktSize == 192 {
		syncOff -= 4
		if syncOff < 0 {
			syncOff += 192
		}
	}
	videoPID := 0
	if probe != nil && probe.VideoMap != "" {
		if probe.VideoStreamPID <= 0 {
			return nil, nil, fmt.Errorf("%w: keyframe index of video stream %s not supported", errNoKeyframeIndex, probe.VideoMap)
		}
		videoPID = probe.VideoStreamPID
	}
	scanner := tsScanner{src: src, pktSize: pktSize, syncOff: syncOff}
	if err := scanner.findVideoPID(ctx, videoPID); err != nil {
		return nil, nil, err
	}
	// The probe of a growing file is cached and soon stale (a recording
	// probed at 12 minutes still says 12 an hour later); the file itself
	// says how much it holds.
	if span, ok := writtenSpan(ctx, &scanner, src.Size()); ok {
		writtenDuration = span
	}
	if writtenDuration <= 0 {
		return nil, nil, fmt.Errorf("%w: nothing recorded yet", errNoKeyframeIndex)
	}
	keyframes, err := sampleKeyframesAt(ctx, &scanner, src.Size(), writtenDuration)
	if err != nil {
		return nil, nil, err
	}
	startTime := 0.0
	if probe != nil && probe.StartTime > 0 {
		startTime = probe.StartTime
	}
	index = &growingCopyIndex{
		path: path, originalPath: originalPath,
		scanner:   tsScanner{pktSize: pktSize, syncOff: syncOff, videoPID: scanner.videoPID, codec: scanner.codec},
		startTime: startTime,
		firstPTS:  keyframes[0].pts,
		offsets:   []int64{0},
		wraps:     []bool{false},
		checked:   time.Now(),
	}
	starts = []float64{0}
	for _, k := range keyframes {
		index.add(&starts, index.timelineTime(k.pts), k.at, index.ptsWrapped(k.pts))
	}
	if len(starts) < growingCopyMinSegments {
		return nil, nil, fmt.Errorf("%w: only %d segments recorded so far", errNoKeyframeIndex, len(starts))
	}
	// The sampler reads one keyframe per slot, the last slots end anywhere
	// before the end of the file: continue a little before it, keyframes
	// already indexed are skipped.
	size := src.Size()
	back := int64(float64(size) / writtenDuration * stableCopyTargetSegment * 2)
	index.scannedTo = max(size-back, 0)
	return starts, index, nil
}

// writtenSpan is how much a transport stream holds: from its first to its
// last video timestamp.
func writtenSpan(ctx context.Context, s *tsScanner, size int64) (float64, bool) {
	first := int64(-1)
	_, _ = s.walk(ctx, 0, tsHeadScanBytes, func(pkt mpegtsPacket, _ int64) bool {
		if pkt.pid != s.videoPID || !pkt.pusi {
			return false
		}
		if pts, _, ok := tsPESPTS(pkt.payload); ok {
			first = pts
			return true
		}
		return false
	})
	if first < 0 {
		return 0, false
	}
	span := int64(-1)
	from := max(size-growingCopyTailScanBytes, 0)
	_, _ = s.walk(ctx, from, growingCopyTailScanBytes+tsChunkBytes, func(pkt mpegtsPacket, _ int64) bool {
		if pkt.pid != s.videoPID || !pkt.pusi {
			return false
		}
		if pts, _, ok := tsPESPTS(pkt.payload); ok {
			d := pts - first
			if d < -tsPTSWrap/2 {
				d += tsPTSWrap // the 33-bit clock wrapped
			}
			span = max(span, d)
		}
		return false
	})
	if span <= 0 {
		return 0, false
	}
	return float64(span) / tsPTSHz, true
}

// sampleKeyframesAt samples one keyframe per target segment of playing time,
// like mpegtsKeyframesForPID, keeping where each one starts.
func sampleKeyframesAt(ctx context.Context, s *tsScanner, size int64, duration float64) ([]tsKeyframeAt, error) {
	if size <= 0 {
		return nil, fmt.Errorf("%w: empty file", errNoKeyframeIndex)
	}
	probes := min(max(int(duration/stableCopyTargetSegment), 1), tsMaxProbes)
	step := size / int64(probes)
	maxScan := min(max(int64(float64(size)/duration*12), tsMinGOPScan), tsMaxGOPScan)
	var out []tsKeyframeAt
	next := int64(0)
	for i := 0; i < probes; i++ {
		at := max(int64(i)*step, next)
		if at >= size {
			break
		}
		pts, found, pesAt, end, err := s.nextKeyframeAt(ctx, at, maxScan)
		if err != nil {
			return nil, err
		}
		next = end
		if !found {
			if end <= at {
				break
			}
			continue
		}
		out = append(out, tsKeyframeAt{pts: pts, at: pesAt})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no video keyframes found", errNoKeyframeIndex)
	}
	return out, nil
}

// add appends a keyframe at timeline time t (starting at byte at) to starts
// and the index when it begins a new segment.
func (x *growingCopyIndex) add(starts *[]float64, t float64, at int64, wrapped bool) {
	if t > x.last && t-x.last >= stableCopyTargetSegment {
		*starts = append(*starts, t)
		x.offsets = append(x.offsets, at)
		x.wraps = append(x.wraps, wrapped)
		x.last = t
	}
}

// ptsWrapped reports whether a raw PTS comes after a wrap of the 33-bit clock
// since the first indexed keyframe.
func (x *growingCopyIndex) ptsWrapped(pts int64) bool {
	return pts < x.firstPTS-tsPTSWrap/2
}

// timelineTime converts a raw PTS to the session timeline.
func (x *growingCopyIndex) timelineTime(pts int64) float64 {
	if x.ptsWrapped(pts) {
		pts += tsPTSWrap
	}
	return float64(pts)/tsPTSHz - x.startTime
}

// scan reads the file from scannedTo to its current end and returns the
// segment starts after the last known one (their offsets are added to the
// index). Every keyframe in the new part is read; a keyframe whose PES is
// still being written is found next time.
func (x *growingCopyIndex) scan(ctx context.Context, src rangeSource) ([]float64, error) {
	s := x.scanner
	s.src = src
	size := src.Size()
	var added []float64
	for x.scannedTo < size {
		pts, found, at, end, err := s.nextKeyframeAt(ctx, x.scannedTo, size-x.scannedTo)
		if err != nil {
			return added, err
		}
		if !found {
			break
		}
		x.scannedTo = end
		x.add(&added, x.timelineTime(pts), at, x.ptsWrapped(pts))
	}
	return added, nil
}

// restartOffset is where a run starting at segment n begins reading: the
// last PAT before that segment's keyframe, so the demuxer has the program
// tables before the keyframe arrives. ok is false without a known offset.
func (x *growingCopyIndex) restartOffset(ctx context.Context, src rangeSource, n int) (int64, bool) {
	x.mu.Lock()
	if n <= 0 || n >= len(x.offsets) || x.wraps[n] {
		x.mu.Unlock()
		return 0, false
	}
	keyframeAt := x.offsets[n]
	s := x.scanner
	x.mu.Unlock()
	s.src = src
	from := max(keyframeAt-growingCopyPATLookback, 0)
	pat := int64(-1)
	_, err := s.walk(ctx, from, keyframeAt-from, func(pkt mpegtsPacket, at int64) bool {
		if at >= keyframeAt {
			return true
		}
		if pkt.pid == 0 && pkt.pusi {
			pat = at
		}
		return false
	})
	if err != nil || pat < 0 {
		return 0, false
	}
	return pat, true
}

// endTime returns the timeline time the recording ends at: the last video
// timestamp in the tail of the file plus one frame.
func (x *growingCopyIndex) endTime(ctx context.Context, src rangeSource, frameDuration float64) (float64, bool) {
	s := x.scanner
	s.src = src
	from := max(src.Size()-growingCopyTailScanBytes, 0)
	lastPTS := int64(-1)
	_, err := s.walk(ctx, from, growingCopyTailScanBytes+tsChunkBytes, func(pkt mpegtsPacket, _ int64) bool {
		if pkt.pid != s.videoPID || !pkt.pusi {
			return false
		}
		if pts, _, ok := tsPESPTS(pkt.payload); ok {
			if lastPTS < 0 || x.timelineTime(pts) > x.timelineTime(lastPTS) {
				lastPTS = pts
			}
		}
		return false
	})
	if err != nil || lastPTS < 0 {
		return 0, false
	}
	return x.timelineTime(lastPTS) + frameDuration, true
}

// copyStarts returns the session's segment start times. The slice is never
// modified in place (extensions replace it), so callers may keep it.
func (session *HLSSession) copyStarts() []float64 {
	session.copyMu.RLock()
	defer session.copyMu.RUnlock()
	return session.CopySegmentStarts
}

// copyTimeline returns the segment starts and whether the timeline still
// grows, read together.
func (session *HLSSession) copyTimeline() ([]float64, bool) {
	session.copyMu.RLock()
	defer session.copyMu.RUnlock()
	return session.CopySegmentStarts, session.copyIndex != nil && !session.copyIndex.finished
}

// growingCopyOpen reports whether the session's copy timeline still grows:
// its last segment has no known end and the playlist has no ENDLIST yet.
func (session *HLSSession) growingCopyOpen() bool {
	session.copyMu.RLock()
	defer session.copyMu.RUnlock()
	return session.copyIndex != nil && !session.copyIndex.finished
}

// growingCopyTail returns the segment the current FFmpeg run leaves open: the
// last one of the cut list it was started with, when the index has since
// learned where that segment ends. The run never closes it at the right
// place, so a request for it (or later) needs a new run.
func (session *HLSSession) growingCopyTail() (int, bool) {
	session.copyMu.RLock()
	defer session.copyMu.RUnlock()
	if session.copyIndex == nil || session.copyRunSegments <= 0 {
		return 0, false
	}
	// A run given every start closes its last segment where the file ends,
	// which is that segment's end once the recording has stopped.
	if session.copyRunSegments >= len(session.CopySegmentStarts) {
		return 0, false
	}
	return session.copyRunSegments - 1, true
}

// holdGrowingCopyTail trims a growing timeline's starts so the playlist does
// not list the running FFmpeg run's open segment until growingCopyTailBatch
// segments past it are known. A request for that segment restarts the run, so
// listing it as soon as its end is known would restart FFmpeg for every new
// segment at the live edge; held back, one restart covers the whole batch.
func (session *HLSSession) holdGrowingCopyTail(starts []float64) []float64 {
	session.copyMu.RLock()
	run := session.copyRunSegments
	session.copyMu.RUnlock()
	if run <= 0 || run >= len(starts) {
		return starts
	}
	// starts[:run] lists segments 0..run-2, all closed by the current run.
	if len(starts)-run < growingCopyTailBatch {
		return starts[:run]
	}
	return starts
}

// setCopyRunSegments records how many segments the starting FFmpeg run cuts.
func (session *HLSSession) setCopyRunSegments(n int) {
	session.copyMu.Lock()
	session.copyRunSegments = n
	session.copyMu.Unlock()
}

// extendGrowingCopy brings a growing copy timeline up to date with the file:
// new segments are appended, and once the recording has stopped the index is
// finished and the session gets its duration. force skips the rate limit.
func (m *HLSManager) extendGrowingCopy(ctx context.Context, session *HLSSession, force bool) {
	session.copyMu.RLock()
	x := session.copyIndex
	session.copyMu.RUnlock()
	if x == nil {
		return
	}
	if force {
		x.mu.Lock()
	} else if !x.mu.TryLock() {
		// Another fetch is scanning (possibly a long catch-up after a
		// pause): serve the timeline known now rather than wait on it.
		return
	}
	defer x.mu.Unlock()
	if x.finished || (!force && time.Since(x.checked) < growingCopyRescanInterval) {
		return
	}
	x.checked = time.Now()
	growing, known, gone := m.sourceGrowingState(ctx, session.Path)
	if gone {
		// Deleted while playing: nothing more will be written or found.
		m.finishGrowingCopy(session, x, nil, 0, false)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, stableCopyIndexTimeout)
	defer cancel()
	src, err := m.openRangeSource(ctx, x.path, x.originalPath)
	if err != nil {
		log.Printf("[hls] session %s: growing copy index: %v", session.ID, err)
		return
	}
	defer src.Close()
	added, err := x.scan(ctx, src)
	if err != nil {
		log.Printf("[hls] session %s: growing copy index: %v", session.ID, err)
	}
	// The timeline is finished only on a sure answer that the recording
	// stopped and a scan that reached the end of the file; a failed status
	// check or a scan cut short (a long pause, then gigabytes to read) keeps
	// it open, and the next fetch carries on.
	if !known || err != nil {
		growing = true
	}
	end, endOK := 0.0, false
	if !growing {
		frame := 0.04
		if session.ProbeData != nil {
			if fps, ok := parseVideoFrameRate(session.ProbeData.AvgFrameRate); ok && fps > 0 {
				frame = 1 / fps
			}
		}
		end, endOK = x.endTime(ctx, src, frame)
	}
	if !growing {
		m.finishGrowingCopy(session, x, added, end, endOK)
		return
	}
	if len(added) == 0 {
		return
	}
	session.copyMu.Lock()
	previous := len(session.CopySegmentStarts)
	session.CopySegmentStarts = append(slices.Clone(session.CopySegmentStarts), added...)
	total := len(session.CopySegmentStarts)
	session.copyMu.Unlock()
	session.mu.Lock()
	// The segment that was last has an end now; whatever a run wrote for it
	// was cut before that end was known.
	delete(session.copyCompleted, previous-1)
	session.mu.Unlock()
	log.Printf("[hls] session %s: growing copy timeline +%d segments (%d, up to %.1fs)", session.ID, total-previous, total, added[len(added)-1])
}

// finishGrowingCopy closes a growing timeline: the last segments are added,
// the session gets its duration (end, else a target segment past the last
// start) and the playlist becomes VOD. The duration is set before the
// timeline is marked finished, so a playlist never ends without it.
func (m *HLSManager) finishGrowingCopy(session *HLSSession, x *growingCopyIndex, added []float64, end float64, endOK bool) {
	session.copyMu.RLock()
	starts := session.CopySegmentStarts
	session.copyMu.RUnlock()
	last := starts[len(starts)-1]
	if len(added) > 0 {
		last = added[len(added)-1]
	}
	if !endOK || end <= last {
		end = last + stableCopyTargetSegment
	}
	session.mu.Lock()
	session.Duration = end
	if len(added) > 0 {
		delete(session.copyCompleted, len(starts)-1)
	}
	session.mu.Unlock()

	session.copyMu.Lock()
	if len(added) > 0 {
		session.CopySegmentStarts = append(slices.Clone(session.CopySegmentStarts), added...)
	}
	total := len(session.CopySegmentStarts)
	x.finished = true
	session.copyMu.Unlock()
	log.Printf("[hls] session %s: recording finished; copy timeline has %d segments over %.1fs", session.ID, total, end)
}

// growingRestartOffset returns where a run of a growing copy timeline that
// starts at the session's current segment begins reading, and the file's
// start time (the timeline's zero).
func (m *HLSManager) growingRestartOffset(ctx context.Context, session *HLSSession) (int64, float64, bool) {
	session.copyMu.RLock()
	x := session.copyIndex
	session.copyMu.RUnlock()
	if x == nil {
		return 0, 0, false
	}
	session.mu.RLock()
	n := session.SegmentStartNumber
	session.mu.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, stableCopyIndexTimeout)
	defer cancel()
	src, err := m.openRangeSource(ctx, x.path, x.originalPath)
	if err != nil {
		return 0, 0, false
	}
	defer src.Close()
	at, ok := x.restartOffset(ctx, src, n)
	return at, x.startTime, ok
}

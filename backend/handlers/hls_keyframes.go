package handlers

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/streamheaders"
	"novastream/utils"
)

// Stable copy timelines.
//
// A browser needs a full-length, seekable HLS playlist. The Cast-style stable
// timeline gets one by re-encoding with a keyframe every hlsSegmentDuration, so
// segment N always starts at N*2s. When the source video is already
// browser-playable H.264, re-encoding is wasted work: the same guarantee holds
// if segments are cut exactly at the source's own keyframes. Those positions
// are read up front from the container index (Matroska Cues, MP4 sync-sample
// table) with a handful of range requests (MPEG-TS has no index and is
// sampled, see hls_keyframes_mpegts.go), grouped into ~4s segments, and the
// playlist then carries each segment's real duration. FFmpeg copies the video
// and cuts with the segment muxer at exactly those times; a far seek restarts
// it at the requested segment's keyframe.

const (
	// stableCopyTargetSegment is the minimum segment length when grouping
	// keyframes. Segments run from one chosen keyframe to the next, so they
	// are at least this long unless the GOP itself is shorter at the end.
	stableCopyTargetSegment = 4.0
	// stableCopyMaxSegment rejects indexes too sparse to seek with.
	stableCopyMaxSegment = 20.0
	// stableCopyMaxLongSegment bounds every segment when long segments are
	// allowed (video transcoding disabled).
	stableCopyMaxLongSegment = 120.0
	// stableCopyMaxFinalSegment bounds the last segment instead: from the
	// last keyframe to the end of the file it is often longer (a closing
	// still, or audio that runs past the video), and nobody seeks into it.
	stableCopyMaxFinalSegment = 60.0
	// stableCopyCutTolerance lets the segment muxer cut on a keyframe whose
	// timestamp differs slightly from the indexed one (container rounding).
	stableCopyCutTolerance = 0.05
	// stableCopySeekLead is added to a keyframe time when seeking so FFmpeg's
	// backward keyframe search lands on that keyframe rather than the one before.
	stableCopySeekLead = 0.25
	// stableCopyTSSeekBack: an MPEG-TS seek lands on the first keyframe at or
	// after the target, so a TS run seeks to just before its keyframe instead
	// (past the keyframe it would start one GOP late).
	stableCopyTSSeekBack = 0.1
	// stableCopyIndexTimeout bounds how long playback start may wait on the index.
	stableCopyIndexTimeout = 15 * time.Second
)

// errTSNotSampled: a remote transport stream is transcoded rather than sampled.
var errTSNotSampled = fmt.Errorf("%w: remote MPEG-TS is not sampled while it can be transcoded", errNoKeyframeIndex)

const (
	// stableCopyMaxIndexBytes caps a Cues element or moov box download.
	stableCopyMaxIndexBytes = 64 << 20
)

var errNoKeyframeIndex = errors.New("no usable keyframe index")

// rangeSource reads byte ranges of the media file.
type rangeSource interface {
	ReadRange(ctx context.Context, offset, length int64) ([]byte, error)
	Size() int64
	Close() error
}

// planStableCopySegments reads the source's keyframe index and returns the
// start time of every playlist segment (the first is always 0), on the same
// clock FFmpeg's output uses with -copyts -start_at_zero. allowLongSegments
// accepts sparse indexes (see groupKeyframeSegments).
func (m *HLSManager) planStableCopySegments(ctx context.Context, path, originalPath string, probe *UnifiedProbeResult, duration float64, allowLongSegments bool) (starts []float64, err error) {
	// The index comes from untrusted files; a parser bug must cost a
	// transcode fallback, never the request.
	defer func() {
		if r := recover(); r != nil {
			starts, err = nil, fmt.Errorf("%w: index parser panic: %v", errNoKeyframeIndex, r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, stableCopyIndexTimeout)
	defer cancel()
	src, err := m.openRangeSource(ctx, path, originalPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	var keyframes []float64
	// Sampling a transport stream reads a good part of it: a local file is
	// quick, but over HTTP it is only worth the wait when the stream cannot be
	// transcoded instead.
	_, local := src.(*fileRangeSource)
	sampleTS := local || allowLongSegments
	videoPID := 0
	if probe != nil && probe.VideoMap != "" {
		// The primary video is not the first stream: only an MPEG-TS index
		// can be read for a chosen stream (by its PID).
		if probe.VideoStreamPID <= 0 {
			return nil, fmt.Errorf("%w: keyframe index of video stream %s not supported", errNoKeyframeIndex, probe.VideoMap)
		}
		videoPID = probe.VideoStreamPID
		if !sampleTS {
			return nil, errTSNotSampled
		}
		keyframes, err = mpegtsKeyframesForPID(ctx, src, duration, videoPID)
		if err != nil {
			return nil, err
		}
		startTime := 0.0
		if probe.StartTime > 0 {
			startTime = probe.StartTime
		}
		return groupKeyframeSegments(keyframes, startTime, duration, allowLongSegments)
	}
	switch {
	case isMatroskaPath(path) || isMatroskaPath(originalPath):
		keyframes, err = matroskaKeyframes(ctx, src)
	case isMP4LikePath(path) || isMP4LikePath(originalPath):
		keyframes, err = mp4Keyframes(ctx, src)
	case isMPEGTSPath(path) || isMPEGTSPath(originalPath):
		if !sampleTS {
			return nil, errTSNotSampled
		}
		keyframes, err = mpegtsKeyframes(ctx, src, duration)
	default:
		// Extension unknown (e.g. debrid URLs): sniff the container.
		keyframes, err = sniffKeyframes(ctx, src, duration, sampleTS)
	}
	if err != nil {
		return nil, err
	}
	startTime := 0.0
	if probe != nil && probe.StartTime > 0 {
		startTime = probe.StartTime
	}
	return groupKeyframeSegments(keyframes, startTime, duration, allowLongSegments)
}

func isMP4LikePath(path string) bool {
	lower := strings.ToLower(path)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	for _, ext := range []string{".mp4", ".m4v", ".mov"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func sniffKeyframes(ctx context.Context, src rangeSource, duration float64, sampleTS bool) ([]float64, error) {
	head, err := src.ReadRange(ctx, 0, 1024)
	if err != nil {
		return nil, err
	}
	if len(head) >= 4 && binary.BigEndian.Uint32(head) == 0x1A45DFA3 {
		return matroskaKeyframes(ctx, src)
	}
	if len(head) >= 8 && (string(head[4:8]) == "ftyp" || string(head[4:8]) == "moov") {
		return mp4Keyframes(ctx, src)
	}
	if looksLikeMPEGTS(head) {
		if !sampleTS {
			return nil, errTSNotSampled
		}
		return mpegtsKeyframes(ctx, src, duration)
	}
	return nil, fmt.Errorf("%w: unrecognised container", errNoKeyframeIndex)
}

// groupKeyframeSegments turns keyframe times into segment start times.
// allowLongSegments drops the segment length limits: with transcoding
// disabled, a long segment (a seek inside it lands on its start) beats not
// playing at all. The index must still hold keyframes.
func groupKeyframeSegments(keyframes []float64, startTime, duration float64, allowLongSegments bool) ([]float64, error) {
	if duration <= 0 {
		return nil, fmt.Errorf("%w: unknown duration", errNoKeyframeIndex)
	}
	normalized := make([]float64, 0, len(keyframes))
	for _, t := range keyframes {
		t -= startTime
		if t > 0 && t < duration && !math.IsNaN(t) {
			normalized = append(normalized, t)
		}
	}
	sort.Float64s(normalized)
	starts := []float64{0}
	last := 0.0
	for _, t := range normalized {
		if t-last >= stableCopyTargetSegment {
			starts = append(starts, t)
			last = t
		}
	}
	if len(starts) < 2 && duration > stableCopyMaxSegment {
		return nil, fmt.Errorf("%w: index has no usable keyframes", errNoKeyframeIndex)
	}
	for i := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		limit := stableCopyMaxSegment
		if i == len(starts)-1 {
			limit = stableCopyMaxFinalSegment
		}
		if allowLongSegments {
			// Still bounded: FFmpeg writes a segment whole before it is
			// served, and an index cut short (a scan that stopped early)
			// would leave one segment for the rest of the file.
			limit = max(limit, stableCopyMaxLongSegment)
		}
		if end-starts[i] > limit {
			return nil, fmt.Errorf("%w: %.1fs between indexed keyframes at %.1fs", errNoKeyframeIndex, end-starts[i], starts[i])
		}
	}
	return starts, nil
}

// --- sources -----------------------------------------------------------------

func (m *HLSManager) openRangeSource(ctx context.Context, path, originalPath string) (rangeSource, error) {
	probeSession := &HLSSession{ID: "keyframe-index", Path: path, OriginalPath: originalPath}
	direct, ok := m.getDirectURL(ctx, probeSession)
	if !ok {
		return nil, fmt.Errorf("%w: no direct URL for range reads", errNoKeyframeIndex)
	}
	if !isHTTPDirectURL(direct) {
		f, err := os.Open(strings.TrimSpace(direct))
		if err != nil {
			return nil, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		return &fileRangeSource{f: f, size: st.Size()}, nil
	}
	target, headers := streamheaders.Extract(direct)
	src := &httpRangeSource{target: target, headers: headers, applyAuth: m.applyExternalUsenetWebDAVAuth}
	if _, err := src.ReadRange(ctx, 0, 1); err != nil {
		return nil, err
	}
	return src, nil
}

type fileRangeSource struct {
	f    *os.File
	size int64
}

func (s *fileRangeSource) Size() int64 { return s.size }

func (s *fileRangeSource) Close() error { return s.f.Close() }

func (s *fileRangeSource) ReadRange(_ context.Context, offset, length int64) ([]byte, error) {
	if offset >= s.size {
		return nil, io.EOF
	}
	if offset+length > s.size {
		length = s.size - offset
	}
	buf := make([]byte, length)
	n, err := s.f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

type httpRangeSource struct {
	target    string
	headers   map[string]string
	applyAuth func(*http.Request)
	size      int64
}

func (s *httpRangeSource) Size() int64 { return s.size }

func (s *httpRangeSource) Close() error { return nil }

func (s *httpRangeSource) ReadRange(ctx context.Context, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}
	encoded, err := utils.EncodeURLWithSpaces(s.target)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, encoded, nil)
	if err != nil {
		return nil, err
	}
	streamheaders.Apply(req.Header, s.headers)
	if parsed, perr := url.Parse(s.target); perr == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		req.SetBasicAuth(parsed.User.Username(), password)
	} else if s.applyAuth != nil {
		s.applyAuth(req)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, err := cdnClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("range read returned %s (server must support byte ranges)", resp.Status)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if slash := strings.LastIndex(cr, "/"); slash >= 0 {
			if total, perr := strconv.ParseInt(strings.TrimSpace(cr[slash+1:]), 10, 64); perr == nil {
				s.size = total
			}
		}
	}
	return io.ReadAll(io.LimitReader(resp.Body, length))
}

// --- Matroska -----------------------------------------------------------------

const (
	ebmlIDHeader       = 0x1A45DFA3
	mkvIDSegment       = 0x18538067
	mkvIDSeekHead      = 0x114D9B74
	mkvIDSeek          = 0x4DBB
	mkvIDSeekID        = 0x53AB
	mkvIDSeekPosition  = 0x53AC
	mkvIDInfo          = 0x1549A966
	mkvIDTimecodeScale = 0x2AD7B1
	mkvIDTracks        = 0x1654AE6B
	mkvIDTrackEntry    = 0xAE
	mkvIDTrackNumber   = 0xD7
	mkvIDTrackType     = 0x83
	mkvIDCues          = 0x1C53BB6B
	mkvIDCuePoint      = 0xBB
	mkvIDCueTime       = 0xB3
	mkvIDCueTrackPos   = 0xB7
	mkvIDCueTrack      = 0xF7
	mkvIDCluster       = 0x1F43B675
)

// ebmlElement is one parsed element header.
type ebmlElement struct {
	ID         uint64
	DataOffset int   // offset of the payload within the buffer
	Size       int64 // payload size, -1 when unknown
}

// readEBMLID reads an element ID (marker bits retained).
func readEBMLID(b []byte) (uint64, int, bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	n := 1
	for mask := byte(0x80); n <= 4 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 4 || len(b) < n {
		return 0, 0, false
	}
	var id uint64
	for i := 0; i < n; i++ {
		id = id<<8 | uint64(b[i])
	}
	return id, n, true
}

// readEBMLSize reads an element size; -1 means unknown size.
func readEBMLSize(b []byte) (int64, int, bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	n := 1
	for mask := byte(0x80); n <= 8 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 || len(b) < n {
		return 0, 0, false
	}
	value := uint64(b[0] & (0xFF >> n))
	allOnes := value == uint64(0xFF>>n)
	for i := 1; i < n; i++ {
		value = value<<8 | uint64(b[i])
		allOnes = allOnes && b[i] == 0xFF
	}
	if allOnes {
		return -1, n, true
	}
	return int64(value), n, true
}

func readEBMLElement(b []byte, offset int) (ebmlElement, bool) {
	if offset < 0 || offset >= len(b) {
		return ebmlElement{}, false
	}
	id, idLen, ok := readEBMLID(b[offset:])
	if !ok {
		return ebmlElement{}, false
	}
	// Sizes stay int64: the Segment of a file over 2 GiB is that large. Callers
	// bound a size by their buffer before slicing with it.
	size, sizeLen, ok := readEBMLSize(b[offset+idLen:])
	if !ok {
		return ebmlElement{}, false
	}
	return ebmlElement{ID: id, DataOffset: offset + idLen + sizeLen, Size: size}, true
}

// ebmlChildren iterates the complete child elements of payload.
func ebmlChildren(payload []byte, fn func(el ebmlElement, data []byte)) {
	for off := 0; off < len(payload); {
		el, ok := readEBMLElement(payload, off)
		if !ok || el.Size < 0 || el.Size > int64(len(payload)-el.DataOffset) {
			return
		}
		fn(el, payload[el.DataOffset:el.DataOffset+int(el.Size)])
		off = el.DataOffset + int(el.Size)
	}
}

func ebmlUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func matroskaKeyframes(ctx context.Context, src rangeSource) ([]float64, error) {
	head, err := src.ReadRange(ctx, 0, 256*1024)
	if err != nil {
		return nil, err
	}
	el, ok := readEBMLElement(head, 0)
	if !ok || el.ID != ebmlIDHeader || el.Size < 0 {
		return nil, fmt.Errorf("%w: not a Matroska file", errNoKeyframeIndex)
	}
	if el.Size > int64(len(head)) {
		return nil, fmt.Errorf("%w: oversized EBML header", errNoKeyframeIndex)
	}
	seg, ok := readEBMLElement(head, el.DataOffset+int(el.Size))
	if !ok || seg.ID != mkvIDSegment {
		return nil, fmt.Errorf("%w: Matroska segment not found", errNoKeyframeIndex)
	}
	segmentStart := int64(seg.DataOffset)

	timecodeScale := uint64(1_000_000)
	videoTrack := uint64(0)
	var cuesPos int64 = -1
	var seekHeads []int64

	parseInfo := func(data []byte) {
		ebmlChildren(data, func(c ebmlElement, d []byte) {
			if c.ID == mkvIDTimecodeScale {
				if v := ebmlUint(d); v > 0 {
					timecodeScale = v
				}
			}
		})
	}
	parseTracks := func(data []byte) {
		ebmlChildren(data, func(c ebmlElement, d []byte) {
			if c.ID != mkvIDTrackEntry || videoTrack != 0 {
				return
			}
			var number, kind uint64
			ebmlChildren(d, func(t ebmlElement, td []byte) {
				switch t.ID {
				case mkvIDTrackNumber:
					number = ebmlUint(td)
				case mkvIDTrackType:
					kind = ebmlUint(td)
				}
			})
			if kind == 1 {
				videoTrack = number
			}
		})
	}
	positions := map[uint64]int64{}
	parseSeekHead := func(data []byte) {
		ebmlChildren(data, func(c ebmlElement, d []byte) {
			if c.ID != mkvIDSeek {
				return
			}
			var id uint64
			var pos int64 = -1
			ebmlChildren(d, func(s ebmlElement, sd []byte) {
				switch s.ID {
				case mkvIDSeekID:
					id = ebmlUint(sd)
				case mkvIDSeekPosition:
					pos = int64(ebmlUint(sd))
				}
			})
			if pos < 0 {
				return
			}
			if id == mkvIDSeekHead {
				seekHeads = append(seekHeads, segmentStart+pos)
			} else if _, seen := positions[id]; !seen {
				positions[id] = segmentStart + pos
			}
		})
	}

	// Walk the top-level children present in the first read.
	var cuesInline []byte
	for off := seg.DataOffset; off < len(head); {
		c, ok := readEBMLElement(head, off)
		if !ok || c.Size < 0 || c.ID == mkvIDCluster {
			break
		}
		if c.Size > int64(len(head)-c.DataOffset) {
			break
		}
		end := c.DataOffset + int(c.Size)
		data := head[c.DataOffset:end]
		switch c.ID {
		case mkvIDSeekHead:
			parseSeekHead(data)
		case mkvIDInfo:
			parseInfo(data)
		case mkvIDTracks:
			parseTracks(data)
		case mkvIDCues:
			cuesInline = data
		}
		off = end
	}

	// Elements that did not fit in the first read are fetched by position.
	fetchElement := func(pos int64) (ebmlElement, []byte, error) {
		hdr, err := src.ReadRange(ctx, pos, 16)
		if err != nil {
			return ebmlElement{}, nil, err
		}
		c, ok := readEBMLElement(hdr, 0)
		if !ok || c.Size < 0 || c.Size > stableCopyMaxIndexBytes {
			return ebmlElement{}, nil, fmt.Errorf("%w: bad element at %d", errNoKeyframeIndex, pos)
		}
		data, err := src.ReadRange(ctx, pos+int64(c.DataOffset), c.Size)
		if err != nil {
			return ebmlElement{}, nil, err
		}
		if int64(len(data)) != c.Size {
			return ebmlElement{}, nil, fmt.Errorf("%w: short read at %d", errNoKeyframeIndex, pos)
		}
		return c, data, nil
	}
	for i := 0; i < len(seekHeads) && i < 4; i++ {
		if c, data, err := fetchElement(seekHeads[i]); err == nil && c.ID == mkvIDSeekHead {
			parseSeekHead(data)
		}
	}
	if videoTrack == 0 {
		if pos, ok := positions[mkvIDTracks]; ok {
			if c, data, err := fetchElement(pos); err == nil && c.ID == mkvIDTracks {
				parseTracks(data)
			}
		}
	}
	if pos, ok := positions[mkvIDInfo]; ok && timecodeScale == 1_000_000 {
		if c, data, err := fetchElement(pos); err == nil && c.ID == mkvIDInfo {
			parseInfo(data)
		}
	}
	if videoTrack == 0 {
		return nil, fmt.Errorf("%w: no video track", errNoKeyframeIndex)
	}
	cues := cuesInline
	if cues == nil {
		if pos, ok := positions[mkvIDCues]; ok {
			cuesPos = pos
		}
		if cuesPos < 0 {
			return nil, fmt.Errorf("%w: Matroska file has no Cues", errNoKeyframeIndex)
		}
		c, data, err := fetchElement(cuesPos)
		if err != nil {
			return nil, err
		}
		if c.ID != mkvIDCues {
			return nil, fmt.Errorf("%w: SeekHead points at element %x, not Cues", errNoKeyframeIndex, c.ID)
		}
		cues = data
	}

	var times []float64
	ebmlChildren(cues, func(c ebmlElement, d []byte) {
		if c.ID != mkvIDCuePoint {
			return
		}
		var cueTime uint64
		hasTime, forVideo := false, false
		ebmlChildren(d, func(p ebmlElement, pd []byte) {
			switch p.ID {
			case mkvIDCueTime:
				cueTime, hasTime = ebmlUint(pd), true
			case mkvIDCueTrackPos:
				ebmlChildren(pd, func(t ebmlElement, td []byte) {
					if t.ID == mkvIDCueTrack && ebmlUint(td) == videoTrack {
						forVideo = true
					}
				})
			}
		})
		if hasTime && forVideo {
			times = append(times, float64(cueTime)*float64(timecodeScale)/1e9)
		}
	})
	if len(times) == 0 {
		return nil, fmt.Errorf("%w: Cues list no video keyframes", errNoKeyframeIndex)
	}
	return times, nil
}

// --- MP4 ----------------------------------------------------------------------

type mp4Box struct {
	Type string
	Data []byte
}

// mp4Children splits a box payload into child boxes.
func mp4Children(payload []byte) []mp4Box {
	var boxes []mp4Box
	for off := 0; off+8 <= len(payload); {
		size := int64(binary.BigEndian.Uint32(payload[off:]))
		typ := string(payload[off+4 : off+8])
		hdr := int64(8)
		if size == 1 {
			if off+16 > len(payload) {
				break
			}
			size = int64(binary.BigEndian.Uint64(payload[off+8:]))
			hdr = 16
		} else if size == 0 {
			size = int64(len(payload) - off)
		}
		if size < hdr || size > int64(len(payload)-off) {
			break
		}
		boxes = append(boxes, mp4Box{Type: typ, Data: payload[off+int(hdr) : off+int(size)]})
		off += int(size)
	}
	return boxes
}

func mp4Find(payload []byte, path ...string) []byte {
	cur := payload
	for _, want := range path {
		found := false
		for _, b := range mp4Children(cur) {
			if b.Type == want {
				cur, found = b.Data, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cur
}

func mp4Keyframes(ctx context.Context, src rangeSource) ([]float64, error) {
	// Locate moov by walking top-level box headers.
	var moov []byte
	for off := int64(0); ; {
		hdr, err := src.ReadRange(ctx, off, 16)
		if err != nil || len(hdr) < 8 {
			break
		}
		size := int64(binary.BigEndian.Uint32(hdr))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		if size == 1 && len(hdr) >= 16 {
			size = int64(binary.BigEndian.Uint64(hdr[8:]))
			hdrLen = 16
		} else if size == 0 {
			if src.Size() <= 0 {
				break
			}
			size = src.Size() - off
		}
		if size < hdrLen {
			break
		}
		if typ == "moov" {
			if size > stableCopyMaxIndexBytes {
				return nil, fmt.Errorf("%w: moov too large", errNoKeyframeIndex)
			}
			moov, err = src.ReadRange(ctx, off+hdrLen, size-hdrLen)
			if err != nil {
				return nil, err
			}
			break
		}
		off += size
		if src.Size() > 0 && off >= src.Size() {
			break
		}
	}
	if moov == nil {
		return nil, fmt.Errorf("%w: moov box not found", errNoKeyframeIndex)
	}
	if mp4Find(moov, "mvex") != nil {
		return nil, fmt.Errorf("%w: fragmented MP4", errNoKeyframeIndex)
	}
	for _, trak := range mp4Children(moov) {
		if trak.Type != "trak" {
			continue
		}
		hdlr := mp4Find(trak.Data, "mdia", "hdlr")
		if len(hdlr) < 12 || string(hdlr[8:12]) != "vide" {
			continue
		}
		return mp4TrackKeyframes(trak.Data)
	}
	return nil, fmt.Errorf("%w: no video track", errNoKeyframeIndex)
}

func mp4TrackKeyframes(trak []byte) ([]float64, error) {
	mdhd := mp4Find(trak, "mdia", "mdhd")
	if len(mdhd) < 24 {
		return nil, fmt.Errorf("%w: bad mdhd", errNoKeyframeIndex)
	}
	var timescale uint32
	if mdhd[0] == 1 {
		if len(mdhd) < 32 {
			return nil, fmt.Errorf("%w: bad mdhd", errNoKeyframeIndex)
		}
		timescale = binary.BigEndian.Uint32(mdhd[20:])
	} else {
		timescale = binary.BigEndian.Uint32(mdhd[12:])
	}
	if timescale == 0 {
		return nil, fmt.Errorf("%w: zero timescale", errNoKeyframeIndex)
	}
	stbl := mp4Find(trak, "mdia", "minf", "stbl")
	stts := mp4Find(stbl, "stts")
	stss := mp4Find(stbl, "stss")
	if len(stts) < 8 || len(stss) < 8 {
		return nil, fmt.Errorf("%w: missing stts/stss", errNoKeyframeIndex)
	}
	// Sample tables are run-length encoded; walk them alongside the sorted
	// sync-sample list instead of expanding per-sample arrays (a hostile file
	// could claim billions of samples).
	sttsRuns := mp4Runs(stts, false)
	cttsRuns := mp4Runs(mp4Find(stbl, "ctts"), true)
	// Edit list: the first media time shown (B-frame delay compensation).
	var mediaStart int64
	if elst := mp4Find(trak, "edts", "elst"); len(elst) >= 8 {
		n := int(binary.BigEndian.Uint32(elst[4:]))
		pos := 8
		for i := 0; i < n; i++ {
			var mediaTime int64
			if elst[0] == 1 {
				if pos+20 > len(elst) {
					break
				}
				mediaTime = int64(binary.BigEndian.Uint64(elst[pos+8:]))
				pos += 20
			} else {
				if pos+12 > len(elst) {
					break
				}
				mediaTime = int64(int32(binary.BigEndian.Uint32(elst[pos+4:])))
				pos += 12
			}
			if mediaTime >= 0 { // -1 marks an empty edit
				mediaStart = mediaTime
				break
			}
		}
	}
	syncCount := int(binary.BigEndian.Uint32(stss[4:]))
	if available := (len(stss) - 8) / 4; syncCount > available {
		syncCount = available
	}
	sync := make([]int64, 0, syncCount)
	for i := 0; i < syncCount; i++ {
		if n := int64(binary.BigEndian.Uint32(stss[8+i*4:])) - 1; n >= 0 {
			sync = append(sync, n)
		}
	}
	sort.Slice(sync, func(i, j int) bool { return sync[i] < sync[j] })
	dtsCursor, ctsCursor := mp4RunCursor{runs: sttsRuns}, mp4RunCursor{runs: cttsRuns}
	times := make([]float64, 0, len(sync))
	for _, sample := range sync {
		dts, ok := dtsCursor.decodeTime(sample)
		if !ok {
			break
		}
		cts, _ := ctsCursor.value(sample)
		times = append(times, float64(dts+cts-mediaStart)/float64(timescale))
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("%w: no sync samples", errNoKeyframeIndex)
	}
	return times, nil
}

// --- session helpers -----------------------------------------------------------

// usesStableCopyTimeline reports whether the session serves a keyframe-cut,
// video-copy stable timeline.
func (session *HLSSession) usesStableCopyTimeline() bool {
	return session != nil && session.StableCopy && len(session.CopySegmentStarts) > 0
}

// usesStableTimeline covers both stable timelines (Cast transcode and copy):
// a full-length playlist whose segment N always maps to the same media time.
func (session *HLSSession) usesStableTimeline() bool {
	return session.usesStableCastTimeline() || session.usesStableCopyTimeline()
}

// stableCopySegmentStart returns the media start time of segment n.
func (session *HLSSession) stableCopySegmentStart(n int) (float64, bool) {
	if n < 0 || n >= len(session.CopySegmentStarts) {
		return 0, false
	}
	return session.CopySegmentStarts[n], true
}

// stableCopySegmentIndex returns the segment containing media time t.
func (session *HLSSession) stableCopySegmentIndex(t float64) int {
	starts := session.CopySegmentStarts
	i := sort.Search(len(starts), func(i int) bool { return starts[i] > t }) - 1
	if i < 0 {
		return 0
	}
	return i
}

// stableCopySegmentTimes lists the cut times FFmpeg needs for a run that
// starts at segment startNumber. How the segment muxer reads -segment_times
// depends on the FFmpeg version (see segmentTimesAbsolute): FFmpeg 5 compares
// them with the packet timestamps, which with -copyts are the timeline
// itself; FFmpeg 7 (the jellyfin-ffmpeg build mediastorm ships) measures them
// from the run's first packet, so they are relative to the run's first
// segment.
func stableCopySegmentTimes(starts []float64, startNumber int, absolute bool) string {
	if startNumber < 0 || startNumber >= len(starts) {
		return ""
	}
	base := starts[startNumber]
	if absolute {
		base = 0
	}
	parts := make([]string, 0, len(starts))
	for i := startNumber + 1; i < len(starts); i++ {
		parts = append(parts, strconv.FormatFloat(starts[i]-base, 'f', 3, 64))
	}
	return strings.Join(parts, ",")
}

func buildStableCopyPlaylist(session *HLSSession) string {
	session.mu.RLock()
	starts := append([]float64(nil), session.CopySegmentStarts...)
	duration := session.Duration
	session.mu.RUnlock()

	durations := make([]float64, len(starts))
	maxDuration := 0.0
	for i := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		durations[i] = end - starts[i]
		if durations[i] > maxDuration {
			maxDuration = durations[i]
		}
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmp4 := session.CopyPlan.FMP4
	if fmp4 {
		b.WriteString("#EXT-X-VERSION:7\n")
	} else {
		b.WriteString("#EXT-X-VERSION:3\n")
	}
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(maxDuration))))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	ext := ".ts"
	if fmp4 {
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
		ext = ".m4s"
	}
	for i, d := range durations {
		if d < 0.01 {
			continue
		}
		b.WriteString(fmt.Sprintf("#EXTINF:%.6f,\nsegment%d%s\n", d, i, ext))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// stableCopySegmentComplete reports whether segment n has been fully written.
// The segment muxer writes files in place, so existence alone is not enough;
// it lists a segment in stream.m3u8 only once the segment is closed. Listed
// segments are remembered because a restarted run rewrites that list.
func (m *HLSManager) stableCopySegmentComplete(session *HLSSession, n int) bool {
	session.mu.RLock()
	done := session.copyCompleted[n]
	session.mu.RUnlock()
	if done {
		return true
	}
	content, err := os.ReadFile(session.OutputDir + string(os.PathSeparator) + "stream.m3u8")
	if err != nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.copyCompleted == nil {
		session.copyCompleted = map[int]bool{}
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		var num int
		if _, scanErr := fmt.Sscanf(line, "segment%d.", &num); scanErr == nil {
			session.copyCompleted[num] = true
		}
	}
	return session.copyCompleted[n]
}

func logStableCopyPlan(sessionID string, starts []float64, duration float64) {
	longest := 0.0
	for i := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		longest = math.Max(longest, end-starts[i])
	}
	log.Printf("[hls] session %s: stable copy timeline with %d keyframe-aligned segments (longest %.2fs) over %.1fs",
		sessionID, len(starts), longest, duration)
}

// recoveryResumePoint returns where FFmpeg restarts after an input failure.
// Stable copy resumes at the first segment of the failed run that was not
// closed (the highest file on disk may be half-written, or belong to another
// run further ahead) and keeps its numbering (resumeSegment >= 0). Other
// sessions resume after the fixed-length segments already produced.
func (m *HLSManager) recoveryResumePoint(session *HLSSession, highestSegment int) (offset float64, resumeSegment int) {
	if session.usesStableCopyTimeline() {
		session.mu.RLock()
		next := session.SegmentStartNumber
		session.mu.RUnlock()
		for next < len(session.CopySegmentStarts) && m.stableCopySegmentComplete(session, next) {
			next++
		}
		start, ok := session.stableCopySegmentStart(next)
		if !ok {
			return session.Duration, -1
		}
		m.resetStableCopyFrom(session, next)
		return start, next
	}
	return session.TranscodingOffset + float64(highestSegment+1)*hlsSegmentDuration, -1
}

// mp4Run is one (sample count, value) entry of stts or ctts.
type mp4Run struct {
	count int64
	value int64
}

func mp4Runs(box []byte, signedValues bool) []mp4Run {
	if len(box) < 8 {
		return nil
	}
	signed := signedValues && box[0] == 1
	n := int(binary.BigEndian.Uint32(box[4:]))
	if available := (len(box) - 8) / 8; n > available {
		n = available
	}
	runs := make([]mp4Run, 0, n)
	for i := 0; i < n; i++ {
		raw := binary.BigEndian.Uint32(box[12+i*8:])
		value := int64(raw)
		if signed {
			value = int64(int32(raw))
		}
		runs = append(runs, mp4Run{count: int64(binary.BigEndian.Uint32(box[8+i*8:])), value: value})
	}
	return runs
}

// mp4RunCursor walks run-length tables for ascending sample numbers.
type mp4RunCursor struct {
	runs      []mp4Run
	run       int
	runStart  int64 // first sample number of runs[run]
	timeStart int64 // decode time of runStart (stts only)
}

// decodeTime returns the stts decode time of sample (0-based).
func (c *mp4RunCursor) decodeTime(sample int64) (int64, bool) {
	for c.run < len(c.runs) && sample >= c.runStart+c.runs[c.run].count {
		c.timeStart += c.runs[c.run].count * c.runs[c.run].value
		c.runStart += c.runs[c.run].count
		c.run++
	}
	if c.run >= len(c.runs) {
		return 0, false
	}
	return c.timeStart + (sample-c.runStart)*c.runs[c.run].value, true
}

// value returns the ctts offset of sample (0 when the table is absent).
func (c *mp4RunCursor) value(sample int64) (int64, bool) {
	for c.run < len(c.runs) && sample >= c.runStart+c.runs[c.run].count {
		c.runStart += c.runs[c.run].count
		c.run++
	}
	if c.run >= len(c.runs) {
		return 0, false
	}
	return c.runs[c.run].value, true
}

// resetStableCopyFrom hands segments n and later to a run starting at n: it
// forgets their completion state, deletes their files (leftovers from other
// runs would otherwise look like progress the new run has made, and trigger
// further restarts), and drops the old run's segment list so stale entries
// cannot be re-read as complete. Earlier segments are kept.
func (m *HLSManager) resetStableCopyFrom(session *HLSSession, n int) {
	session.mu.Lock()
	for k := range session.copyCompleted {
		if k >= n {
			delete(session.copyCompleted, k)
		}
	}
	session.mu.Unlock()
	dir := session.OutputDir + string(os.PathSeparator)
	_ = os.Remove(dir + "stream.m3u8")
	files, _ := filepath.Glob(dir + "segment*")
	for _, file := range files {
		var num int
		if _, err := fmt.Sscanf(filepath.Base(file), "segment%d.", &num); err == nil && num >= n {
			_ = os.Remove(file)
		}
	}
}

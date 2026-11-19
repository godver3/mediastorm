package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MPEG-TS keyframe positions.
//
// A transport stream (DVB recordings, .m2ts rips) has no keyframe index, and
// reading every packet of a multi-gigabyte recording before playback starts
// is too slow. A stable copy timeline only needs one keyframe per target
// segment, so the file is sampled instead: the byte range is split into one
// slot per stableCopyTargetSegment of playing time, and from the start of
// each slot the packets are read forward to the next video keyframe, whose
// PTS becomes a candidate segment start. With a 2s GOP that reads about a
// quarter of the file; every chosen time is a real keyframe, so FFmpeg's
// segment muxer cuts exactly there.

const (
	tsSyncByte   = 0x47
	tsChunkBytes = 256 << 10
	// tsFirstChunkBytes is the first read from a slot (over HTTP each read
	// is a range request; most slots find their keyframe early).
	tsFirstChunkBytes = 32 << 10
	tsHeadScanBytes   = 8 << 20
	tsMaxProbes       = 20000
	// tsMaxPESProbe bounds how much of a PES is buffered to classify it.
	tsMaxPESProbe = 32 << 10
	// tsMinGOPScan / tsMaxGOPScan bound the read from one slot to the next
	// keyframe (≈12s of the stream's average bitrate in between).
	tsMinGOPScan = 4 << 20
	tsMaxGOPScan = 64 << 20
	tsPTSWrap    = int64(1) << 33
	tsPTSHz      = 90000.0
)

type tsVideoCodec int

const (
	tsCodecOther tsVideoCodec = iota
	tsCodecH264
	tsCodecHEVC
)

func isMPEGTSPath(path string) bool {
	lower := strings.ToLower(path)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	for _, ext := range []string{".ts", ".m2ts", ".mts"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// tsPacketLayout finds the packet size (188, M2TS 192, or 204 with Reed-
// Solomon) and the offset of the first sync byte.
func tsPacketLayout(head []byte) (size int, offset int, ok bool) {
	for _, size := range []int{188, 192, 204} {
		for off := 0; off < size && off+4*size < len(head); off++ {
			if head[off] == tsSyncByte && head[off+size] == tsSyncByte &&
				head[off+2*size] == tsSyncByte && head[off+3*size] == tsSyncByte &&
				head[off+4*size] == tsSyncByte {
				return size, off, true
			}
		}
	}
	return 0, 0, false
}

func looksLikeMPEGTS(head []byte) bool {
	_, _, ok := tsPacketLayout(head)
	return ok
}

type mpegtsPacket struct {
	pid     int
	pusi    bool
	rai     bool
	payload []byte
}

// parseMPEGTSPacket reads the 188-byte transport packet p.
func parseMPEGTSPacket(p []byte) (mpegtsPacket, bool) {
	if len(p) < 188 || p[0] != tsSyncByte {
		return mpegtsPacket{}, false
	}
	pkt := mpegtsPacket{
		pid:  int(p[1]&0x1F)<<8 | int(p[2]),
		pusi: p[1]&0x40 != 0,
	}
	afc := (p[3] >> 4) & 0x3
	idx := 4
	if afc&0x2 != 0 {
		alen := int(p[4])
		if alen > 0 {
			pkt.rai = p[5]&0x40 != 0
		}
		idx = 5 + alen
	}
	if afc&0x1 == 0 || idx > 188 {
		return pkt, true
	}
	pkt.payload = p[idx:188]
	return pkt, true
}

// tsSection returns the PSI section starting in a PUSI payload.
func tsSection(payload []byte) []byte {
	if len(payload) < 1 {
		return nil
	}
	ptr := int(payload[0])
	if 1+ptr >= len(payload) {
		return nil
	}
	s := payload[1+ptr:]
	if len(s) < 3 {
		return nil
	}
	length := int(s[1]&0x0F)<<8 | int(s[2])
	if 3+length > len(s) || length < 9 {
		return nil
	}
	return s[:3+length]
}

// tsPESPTS returns the PTS of a PES packet start and where its ES data begins.
func tsPESPTS(payload []byte) (pts int64, esStart int, ok bool) {
	if len(payload) < 14 || payload[0] != 0 || payload[1] != 0 || payload[2] != 1 {
		return 0, 0, false
	}
	if payload[7]&0x80 == 0 {
		return 0, 0, false
	}
	b := payload[9:14]
	pts = int64(b[0]>>1&0x07)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
	esStart = 9 + int(payload[8])
	if esStart > len(payload) {
		esStart = len(payload)
	}
	return pts, esStart, true
}

// tsClassifyAccessUnit decides from the start of an access unit whether it
// is a keyframe. decided is false while more data is needed.
func tsClassifyAccessUnit(codec tsVideoCodec, es []byte, complete bool) (key, decided bool) {
	for i := 0; i+3 < len(es); i++ {
		if es[i] != 0 || es[i+1] != 0 || es[i+2] != 1 {
			continue
		}
		nal := es[i+3:]
		switch codec {
		case tsCodecH264:
			switch nal[0] & 0x1F {
			case 5: // IDR slice
				return true, true
			case 1: // non-IDR slice: the picture data has begun
				return false, true
			case 6: // SEI: a recovery point makes an open-GOP I-frame a sync point
				if h264SEIHasRecoveryPoint(nalRBSP(nal[1:])) {
					return true, true
				}
			}
		case tsCodecHEVC:
			if len(nal) < 2 {
				break
			}
			switch t := (nal[0] >> 1) & 0x3F; {
			case t >= 16 && t <= 21: // IRAP (BLA, IDR, CRA)
				return true, true
			case t <= 9: // other slices
				return false, true
			}
		default:
			return false, true
		}
		i += 3
	}
	return false, complete
}

// nalRBSP returns a NAL unit's payload up to the next start code, with the
// emulation prevention bytes (00 00 03) removed, so SEI sizes line up.
func nalRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for i := 0; i < len(b); i++ {
		if zeros >= 2 && b[i] <= 0x01 {
			// 00 00 00 / 00 00 01: the next start code.
			return out[:len(out)-zeros]
		}
		if zeros >= 2 && b[i] == 0x03 {
			zeros = 0
			continue
		}
		out = append(out, b[i])
		if b[i] == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// h264SEIHasRecoveryPoint walks the SEI messages of an H.264 SEI NAL (without
// its header byte) looking for a recovery point (payload type 6).
func h264SEIHasRecoveryPoint(b []byte) bool {
	for i := 0; i < len(b); {
		typ := 0
		for i < len(b) && b[i] == 0xFF {
			typ += 255
			i++
		}
		if i >= len(b) {
			return false
		}
		typ += int(b[i])
		i++
		size := 0
		for i < len(b) && b[i] == 0xFF {
			size += 255
			i++
		}
		if i >= len(b) {
			return false
		}
		size += int(b[i])
		i++
		if typ == 6 {
			return true
		}
		i += size
		if i < len(b) && b[i] == 0x80 { // rbsp trailing bits
			return false
		}
	}
	return false
}

type tsScanner struct {
	src      rangeSource
	pktSize  int
	syncOff  int64
	videoPID int
	codec    tsVideoCodec
}

// walk calls fn with each transport packet from offset (aligned up to the
// packet grid) until fn returns true, maxBytes have been read or the file
// ends. It returns the offset after the last packet handed to fn.
func (s *tsScanner) walk(ctx context.Context, offset, maxBytes int64, fn func(mpegtsPacket) bool) (int64, error) {
	size := int64(s.pktSize)
	if offset < s.syncOff {
		offset = s.syncOff
	} else if rem := (offset - s.syncOff) % size; rem != 0 {
		offset += size - rem
	}
	start := offset
	syncInPkt := 0
	if s.pktSize == 192 {
		syncInPkt = 4 // M2TS: 4-byte timecode before the sync byte
	}
	// The first read is small (the keyframe is often close), then doubles.
	chunkBytes := int64(tsFirstChunkBytes)
	for offset-start < maxBytes {
		if err := ctx.Err(); err != nil {
			return offset, err
		}
		want := (chunkBytes / size) * size
		chunkBytes = min(chunkBytes*2, tsChunkBytes)
		chunk, err := s.src.ReadRange(ctx, offset, want)
		if err != nil {
			if errors.Is(err, io.EOF) || offset >= s.src.Size() {
				return offset, nil // end of file
			}
			// A failed read (timeout, server error) is not the end of the
			// file: a keyframe list cut short there would be taken as whole.
			return offset, err
		}
		if len(chunk) < s.pktSize {
			return offset, nil // end of file
		}
		for i := 0; i+s.pktSize <= len(chunk); {
			pkt, ok := parseMPEGTSPacket(chunk[i+syncInPkt : i+syncInPkt+188])
			if !ok {
				// Lost sync (a damaged recording): find the grid again.
				next := tsResync(chunk, i+1, s.pktSize, syncInPkt)
				if next < 0 {
					i = len(chunk) - len(chunk)%s.pktSize
					break
				}
				i = next
				continue
			}
			i += s.pktSize
			if fn(pkt) {
				return offset + int64(i), nil
			}
		}
		consumed := int64(len(chunk) - len(chunk)%s.pktSize)
		if consumed == 0 {
			return offset, nil
		}
		offset += consumed
		if int64(len(chunk)) < want {
			return offset, nil
		}
	}
	return offset, nil
}

func tsResync(chunk []byte, from, pktSize, syncInPkt int) int {
	for i := from; i+syncInPkt+2*pktSize < len(chunk); i++ {
		j := i + syncInPkt
		if chunk[j] == tsSyncByte && chunk[j+pktSize] == tsSyncByte && chunk[j+2*pktSize] == tsSyncByte {
			return i
		}
	}
	return -1
}

// findVideoPID reads the PAT and PMTs near the start of the file. wantPID
// (when > 0) selects that video stream; otherwise the first video stream of
// the first program is used.
func (s *tsScanner) findVideoPID(ctx context.Context, wantPID int) error {
	var pmtPIDs []int
	isPMT := func(pid int) bool {
		for _, p := range pmtPIDs {
			if p == pid {
				return true
			}
		}
		return false
	}
	s.videoPID = -1
	_, err := s.walk(ctx, 0, tsHeadScanBytes, func(pkt mpegtsPacket) bool {
		if !pkt.pusi {
			return false
		}
		switch {
		case pkt.pid == 0 && len(pmtPIDs) == 0:
			sec := tsSection(pkt.payload)
			if sec == nil || sec[0] != 0x00 {
				return false
			}
			for i := 8; i+4 <= len(sec)-4; i += 4 {
				program := int(sec[i])<<8 | int(sec[i+1])
				if program != 0 {
					pmtPIDs = append(pmtPIDs, int(sec[i+2]&0x1F)<<8|int(sec[i+3]))
					if wantPID <= 0 {
						break // the first program decides
					}
				}
			}
		case isPMT(pkt.pid):
			sec := tsSection(pkt.payload)
			if sec == nil || sec[0] != 0x02 || len(sec) < 12 {
				return false
			}
			i := 12 + (int(sec[10]&0x0F)<<8 | int(sec[11]))
			for i+5 <= len(sec)-4 {
				streamType := sec[i]
				pid := int(sec[i+1]&0x1F)<<8 | int(sec[i+2])
				esInfo := int(sec[i+3]&0x0F)<<8 | int(sec[i+4])
				i += 5 + esInfo
				if wantPID > 0 && pid != wantPID {
					continue
				}
				switch streamType {
				case 0x1B:
					s.videoPID, s.codec = pid, tsCodecH264
				case 0x24:
					s.videoPID, s.codec = pid, tsCodecHEVC
				case 0x01, 0x02, 0x10, 0xEA:
					s.videoPID, s.codec = pid, tsCodecOther
				}
				if s.videoPID >= 0 {
					return true
				}
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	if s.videoPID < 0 {
		switch {
		case len(pmtPIDs) == 0:
			return fmt.Errorf("%w: MPEG-TS program table not found", errNoKeyframeIndex)
		case wantPID > 0:
			return fmt.Errorf("%w: MPEG-TS has no video stream with PID %d", errNoKeyframeIndex, wantPID)
		}
		return fmt.Errorf("%w: MPEG-TS has no video stream", errNoKeyframeIndex)
	}
	return nil
}

// nextKeyframe reads forward from offset to the first video keyframe and
// returns its PTS and the offset to continue from.
func (s *tsScanner) nextKeyframe(ctx context.Context, offset, maxBytes int64) (pts int64, found bool, end int64, err error) {
	var (
		pending  bool
		curPTS   int64
		es       []byte
		foundPTS int64 = -1
	)
	decide := func(complete bool) {
		if !pending {
			return
		}
		key, decided := tsClassifyAccessUnit(s.codec, es, complete || len(es) >= tsMaxPESProbe)
		if decided {
			pending = false
			if key {
				foundPTS = curPTS
			}
		}
	}
	end, err = s.walk(ctx, offset, maxBytes, func(pkt mpegtsPacket) bool {
		if pkt.pid != s.videoPID {
			return false
		}
		if pkt.pusi {
			decide(true)
			if foundPTS >= 0 {
				return true
			}
			pending = false
			p, esStart, ok := tsPESPTS(pkt.payload)
			if !ok {
				return false
			}
			if s.codec == tsCodecOther {
				// No slice parser for this codec: trust the random access
				// indicator. H.264/HEVC are checked on the slice itself, as
				// some muxers set the indicator on every PES.
				if pkt.rai {
					foundPTS = p
					return true
				}
				return false
			}
			curPTS, pending, es = p, true, append(es[:0], pkt.payload[esStart:]...)
		} else if pending && len(es) < tsMaxPESProbe {
			es = append(es, pkt.payload...)
		}
		decide(false)
		return foundPTS >= 0
	})
	if err != nil {
		return 0, false, end, err
	}
	if foundPTS >= 0 {
		return foundPTS, true, end, nil
	}
	return 0, false, end, nil
}

// mpegtsKeyframes samples video keyframe times (seconds, on the PTS clock)
// from a transport stream of the given duration.
func mpegtsKeyframes(ctx context.Context, src rangeSource, duration float64) ([]float64, error) {
	return mpegtsKeyframesForPID(ctx, src, duration, 0)
}

// mpegtsKeyframesForPID is mpegtsKeyframes for the video stream with the
// given PID (0: the first video stream).
func mpegtsKeyframesForPID(ctx context.Context, src rangeSource, duration float64, videoPID int) ([]float64, error) {
	head, err := src.ReadRange(ctx, 0, 64*1024)
	if err != nil {
		return nil, err
	}
	pktSize, off, ok := tsPacketLayout(head)
	if !ok {
		return nil, fmt.Errorf("%w: not an MPEG-TS file", errNoKeyframeIndex)
	}
	syncOff := int64(off)
	if pktSize == 192 {
		syncOff -= 4 // a packet starts at its 4-byte timecode
		if syncOff < 0 {
			syncOff += 192
		}
	}
	size := src.Size()
	if size <= 0 {
		return nil, fmt.Errorf("%w: unknown file size", errNoKeyframeIndex)
	}
	if duration <= 0 {
		return nil, fmt.Errorf("%w: unknown duration", errNoKeyframeIndex)
	}
	s := &tsScanner{src: src, pktSize: pktSize, syncOff: syncOff}
	if err := s.findVideoPID(ctx, videoPID); err != nil {
		return nil, err
	}

	probes := int(duration / stableCopyTargetSegment)
	if probes < 1 {
		probes = 1
	}
	if probes > tsMaxProbes {
		probes = tsMaxProbes
	}
	step := size / int64(probes)
	bytesPerSecond := float64(size) / duration
	maxScan := int64(bytesPerSecond * 12)
	if maxScan < tsMinGOPScan {
		maxScan = tsMinGOPScan
	}
	if maxScan > tsMaxGOPScan {
		maxScan = tsMaxGOPScan
	}

	var times []float64
	lastPTS, wraps := int64(-1), int64(0)
	next := int64(0)
	for i := 0; i < probes; i++ {
		at := int64(i) * step
		if at < next {
			at = next
		}
		if at >= size {
			break
		}
		pts, found, end, err := s.nextKeyframe(ctx, at, maxScan)
		if err != nil {
			return nil, err
		}
		next = end
		if !found {
			if end <= at {
				break // end of file
			}
			continue
		}
		// Unwrap the 33-bit clock (≈26.5h) against the previous keyframe, so
		// any number of wraps is followed.
		if lastPTS >= 0 {
			pts += wraps * tsPTSWrap
			if pts < lastPTS-tsPTSWrap/2 {
				wraps++
				pts += tsPTSWrap
			}
		}
		lastPTS = pts
		times = append(times, float64(pts)/tsPTSHz)
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("%w: no video keyframes found", errNoKeyframeIndex)
	}
	return times, nil
}

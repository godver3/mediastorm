package handlers

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A stable copy session in fMP4 (HEVC/AV1 for browsers, HDR, Dolby Vision)
// lets ffmpeg's segment muxer write every planned segment as a
// self-contained fragmented MP4: tfdt carries the absolute source timeline,
// so runs restarted after a seek line up like the MPEG-TS variant. HLS needs
// one init segment for all of them; the moov boxes only differ in their edit
// lists, which SplitFragmentedMP4 removes. Clients get init.mp4 once and
// segmentN.m4s as styp+moof+mdat.

// stableCopyFMP4MuxerArgs selects the fragmented MP4 segment muxer.
// delay_moov is needed for E-AC-3 (its config comes from the packets);
// bitexact keeps creation times and encoder strings out of the moov.
func stableCopyFMP4MuxerArgs() []string {
	return []string{
		"-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact",
		"-f", "segment", "-segment_format", "mp4",
		"-segment_format_options", "movflags=+frag_keyframe+delay_moov+default_base_moof+frag_discont:video_track_timescale=90000",
	}
}

// stableCopyFMP4Raw maps a client segment name (segmentN.m4s) to the file
// the muxer writes (segmentN.mp4).
func stableCopyFMP4Raw(outputDir, segmentName string) (string, bool) {
	var num int
	if _, err := fmt.Sscanf(segmentName, "segment%d.m4s", &num); err != nil || segmentName != "segment"+strconv.Itoa(num)+".m4s" {
		return "", false
	}
	return filepath.Join(outputDir, fmt.Sprintf("segment%d.mp4", num)), true
}

// fmp4Fragment is what a client gets for one raw segment: styp, the moof
// boxes (read into memory, a few KB) and the mdat payloads, streamed from
// the file so a large remux segment is never held in memory.
type fmp4Fragment struct {
	path   string
	parts  []fmp4Part
	length int64
}

type fmp4Part struct {
	data      []byte // in-memory bytes, or
	off, size int64  // a range of the raw file
}

// WriteTo streams the fragment.
func (f *fmp4Fragment) WriteTo(w io.Writer) (int64, error) {
	file, err := os.Open(f.path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var n int64
	for _, p := range f.parts {
		var written int64
		if p.data != nil {
			k, werr := w.Write(p.data)
			written, err = int64(k), werr
		} else {
			written, err = io.Copy(w, io.NewSectionReader(file, p.off, p.size))
		}
		n += written
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// maxFMP4HeaderBox bounds the boxes read into memory (ftyp, moov, moof...).
const maxFMP4HeaderBox = 16 << 20

// stableCopyFragment plans the client fragment of a finished raw segment and
// derives its init segment. The init is remembered on first use; a later
// segment with a different init means the runs disagree on codec
// configuration, which is logged (the client keeps the first one).
func (m *HLSManager) stableCopyFragment(session *HLSSession, rawPath string) (*fmp4Fragment, error) {
	file, err := os.Open(rawPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	frag := &fmp4Fragment{path: rawPath, parts: []fmp4Part{{data: fmp4Styp}}, length: int64(len(fmp4Styp))}
	var head []byte // ftyp + moov
	moofs := 0
	for off := int64(0); off < size; {
		var hdr [16]byte
		if _, err := file.ReadAt(hdr[:8], off); err != nil {
			return nil, fmt.Errorf("fmp4: box header at %d: %w", off, err)
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		switch boxSize {
		case 0:
			boxSize = size - off
		case 1:
			if _, err := file.ReadAt(hdr[8:16], off+8); err != nil {
				return nil, fmt.Errorf("fmp4: largesize at %d: %w", off, err)
			}
			boxSize, hdrLen = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		}
		if boxSize < hdrLen || boxSize > size-off {
			return nil, fmt.Errorf("fmp4: box %q at %d has invalid size %d", typ, off, boxSize)
		}
		readBox := func() ([]byte, error) {
			if boxSize > maxFMP4HeaderBox {
				return nil, fmt.Errorf("fmp4: %q box of %d bytes", typ, boxSize)
			}
			buf := make([]byte, boxSize)
			_, err := file.ReadAt(buf, off)
			return buf, err
		}
		switch typ {
		case "ftyp", "moov":
			buf, err := readBox()
			if err != nil {
				return nil, err
			}
			head = append(head, buf...)
		case "moof":
			buf, err := readBox()
			if err != nil {
				return nil, err
			}
			if err := checkFMP4Tree(buf[hdrLen:], 1); err != nil {
				return nil, err
			}
			// Explicit tfhd base offsets point into the raw file; move them to
			// the fragment layout (default_base_moof output carries none).
			if err := shiftFMP4BaseDataOffsets(buf, int(hdrLen), frag.length-off); err != nil {
				return nil, err
			}
			frag.parts = append(frag.parts, fmp4Part{data: buf})
			frag.length += boxSize
			moofs++
		case "mdat":
			frag.parts = append(frag.parts, fmp4Part{off: off, size: boxSize})
			frag.length += boxSize
		case "emsg", "prft", "sidx":
			buf, err := readBox()
			if err != nil {
				return nil, err
			}
			frag.parts = append(frag.parts, fmp4Part{data: buf})
			frag.length += boxSize
		}
		off += boxSize
	}
	if moofs == 0 {
		return nil, fmt.Errorf("fmp4: %s has no moof box", filepath.Base(rawPath))
	}
	init, err := fmp4InitSegment(head)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	switch {
	case session.copyInit == nil:
		session.copyInit = init
	case !bytes.Equal(session.copyInit, init) && !session.copyInitMismatch:
		session.copyInitMismatch = true
		log.Printf("[hls] session %s: fMP4 init of %s differs from the first one; playback may fail", session.ID, filepath.Base(rawPath))
	}
	session.mu.Unlock()
	return frag, nil
}

// fmp4InitSegment builds the HLS init segment from ftyp+moov (edts removed),
// the same way SplitFragmentedMP4 does.
func fmp4InitSegment(head []byte) ([]byte, error) {
	boxes, err := readFMP4Boxes(head)
	if err != nil {
		return nil, err
	}
	ftyp, okF := findFMP4Box(boxes, "ftyp")
	moov, okM := findFMP4Box(boxes, "moov")
	if !okF || !okM {
		return nil, fmt.Errorf("fmp4: segment without ftyp/moov")
	}
	if err := checkFMP4Tree(moov.payload(), 1); err != nil {
		return nil, err
	}
	moovOut, err := stripFMP4Edts(moov)
	if err != nil {
		return nil, err
	}
	return append(appendFMP4Raw(nil, ftyp), moovOut...), nil
}

// serveStableCopyInit serves init.mp4, waiting for the first finished
// segment of the session when none has been read yet.
func (m *HLSManager) serveStableCopyInit(w http.ResponseWriter, r *http.Request, session *HLSSession) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		session.mu.RLock()
		init := session.copyInit
		completed := session.Completed
		session.mu.RUnlock()
		if init == nil {
			init = m.stableCopyInitFromDisk(session)
		}
		if init != nil {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(init)))
			w.Header().Set("Cache-Control", "no-store, max-age=0")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			_, _ = w.Write(init)
			return
		}
		if completed || time.Now().After(deadline) || r.Context().Err() != nil {
			http.Error(w, "init segment not available", http.StatusNotFound)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stableCopyInitFromDisk derives the init segment from any finished segment.
func (m *HLSManager) stableCopyInitFromDisk(session *HLSSession) []byte {
	files, _ := filepath.Glob(filepath.Join(session.OutputDir, "segment*.mp4"))
	for _, file := range files {
		var num int
		if _, err := fmt.Sscanf(filepath.Base(file), "segment%d.mp4", &num); err != nil || !m.stableCopySegmentComplete(session, num) {
			continue
		}
		if _, err := m.stableCopyFragment(session, file); err == nil {
			session.mu.RLock()
			init := session.copyInit
			session.mu.RUnlock()
			return init
		}
	}
	return nil
}

// stableCopyNeedsMaster reports whether a stable copy session is announced
// through a master playlist: fMP4 and HDR output need CODECS and VIDEO-RANGE
// so players (Safari's native HLS in particular) set up the right decoder
// and display mode.
func stableCopyNeedsMaster(session *HLSSession) bool {
	if !session.usesStableCopyTimeline() {
		return false
	}
	return session.CopyPlan.FMP4 || (session.CopyPlan.OutputRange != "" && session.CopyPlan.OutputRange != "SDR")
}

// serveStableCopyMaster writes the single-variant master playlist of a
// stable copy session. CODECS comes from the real init segment (waiting for
// the first segment when needed); without it the attribute is left out
// rather than guessed.
func (m *HLSManager) serveStableCopyMaster(w http.ResponseWriter, r *http.Request, session *HLSSession) {
	codecs := ""
	if session.CopyPlan.FMP4 {
		deadline := time.Now().Add(15 * time.Second)
		for {
			session.mu.RLock()
			init := session.copyInit
			session.mu.RUnlock()
			if init == nil {
				init = m.stableCopyInitFromDisk(session)
			}
			if init != nil {
				video, audio, _ := HLSCodecString(init)
				if session.CopyPlan.DV == dvKeep {
					if dv, _, ok := DolbyVisionCodecString(init); ok {
						video = dv
					}
				}
				codecs = strings.Trim(video+","+audio, ",")
				break
			}
			if time.Now().After(deadline) || r.Context().Err() != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	bandwidth := 20_000_000
	attrs := []string{"BANDWIDTH=" + strconv.Itoa(bandwidth)}
	if probe := session.ProbeData; probe != nil && probe.VideoWidth > 0 && probe.VideoHeight > 0 {
		attrs = append(attrs, fmt.Sprintf("RESOLUTION=%dx%d", probe.VideoWidth, probe.VideoHeight))
	}
	if codecs != "" {
		attrs = append(attrs, `CODECS="`+codecs+`"`)
	}
	if rng := session.CopyPlan.OutputRange; rng != "" {
		attrs = append(attrs, "VIDEO-RANGE="+rng)
	}
	version := 3
	if session.CopyPlan.FMP4 {
		version = 7
	}
	content := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-STREAM-INF:%s\nstream.m3u8\n", version, strings.Join(attrs, ","))
	content = appendAuthTokenToPlaylist(content, hlsChildAuthQuery(r, r.URL.Query().Get("token")))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(content))
}

// resetOutputForFilterRestart clears a session's output before FFmpeg restarts
// without a bitstream filter that failed. A stable copy session also forgets
// which segments were complete and its init segment (the filter changes the
// parameter sets in it).
func (m *HLSManager) resetOutputForFilterRestart(session *HLSSession) {
	if session.usesStableCopyTimeline() {
		m.resetStableCopyFrom(session, 0)
		session.mu.Lock()
		session.copyInit, session.copyInitMismatch = nil, false
		session.mu.Unlock()
		return
	}
	files, _ := filepath.Glob(filepath.Join(session.OutputDir, "*"))
	for _, f := range files {
		os.Remove(f)
	}
}

package handlers

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// FFmpeg maps its input streams once, when it starts. A source that later moves its audio or video
// onto another MPEG-TS PID (a new PMT) shows up as a stream FFmpeg announces and then ignores:
//
//	[in#0/hls @ 0x...] New video stream with index 2 at pos:3734056 and DTS:1.4s   (FFmpeg 7+)
//	New video stream 0:2 at pos:3734056 and DTS:1.4s                              (FFmpeg <= 6)
//
// Pluto TV's stitcher does exactly that at every ad break: the programme muxes video on PID 0x102
// under a PMT on 0x100, the ad slate on PID 0x100 under a PMT on 0x1000. The output keeps the
// audio and loses the picture, and with no video keyframes the segmenter stops cutting segments, so
// the player freezes until the starvation watchdog restarts FFmpeg 45 s later (spending the
// session's restart budget, which a multi-ad break can exhaust). Restarting FFmpeg as soon as the
// layout changes lets the new run probe and map the streams that are actually flowing; the
// supervisor resumes the playlist with a discontinuity, so players reinitialise at the seam.
var liveNewInputStreamPattern = regexp.MustCompile(`New (?:video|audio) stream (?:with index \d+|\d+:\d+) at pos:`)

// liveLayoutRestartLimit caps layout-change restarts inside liveRestartWindow. They are not failures
// and do not use liveMaxRestarts, but a source that announces a new stream on every run must not
// cycle FFmpeg forever: past the cap the session is left running as it would have been before.
const liveLayoutRestartLimit = 12

// liveLayoutWatch scans one FFmpeg run's stderr for a late audio/video stream. stderr arrives in
// arbitrary chunks, so the tail of the previous chunk is kept to match a line split across reads.
type liveLayoutWatch struct {
	tail  string
	fired bool
}

const liveLayoutWatchTail = 160

// observe reports true once, for the first chunk that announces a new audio or video stream.
func (w *liveLayoutWatch) observe(chunk string) bool {
	if w == nil || w.fired {
		return false
	}
	text := w.tail + chunk
	if liveNewInputStreamPattern.MatchString(text) {
		w.fired = true
		w.tail = ""
		return true
	}
	if len(text) > liveLayoutWatchTail {
		text = text[len(text)-liveLayoutWatchTail:]
	}
	// Only an unfinished line can complete into a match with the next chunk.
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	w.tail = text
	return false
}

// liveLayoutRestarts is a session's record of layout-change restarts: the request the stderr
// watcher raises before it kills FFmpeg, and the recent restarts that count toward the cap.
type liveLayoutRestarts struct {
	mu        sync.Mutex
	requested bool
	recent    []time.Time
}

// claim records a layout-change restart at now and reports whether it is allowed under
// liveLayoutRestartLimit within liveRestartWindow.
func (r *liveLayoutRestarts) claim(now time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.recent[:0]
	for _, at := range r.recent {
		if now.Sub(at) < liveRestartWindow {
			kept = append(kept, at)
		}
	}
	r.recent = kept
	if len(r.recent) >= liveLayoutRestartLimit {
		return false
	}
	r.recent = append(r.recent, now)
	r.requested = true
	return true
}

// take reports whether the run that just ended was stopped for a layout change, and clears it.
func (r *liveLayoutRestarts) take() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	requested := r.requested
	r.requested = false
	return requested
}

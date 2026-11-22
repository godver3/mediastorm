package jellyfinbridge

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

// Recording pictures. Jellyfin shows a recording with the programme's image
// (or a still from the file) and the channel logo as a small badge. The
// bridge does the same: the guide image stored with the recording, else a
// still mediastorm extracts from the file, else the channel logo.

// recordingFramePrefix marks a recording's image source as a still from its
// file; the rest is "<mediastorm recording ID>|<status>".
const recordingFramePrefix = "recording-frame:"

const (
	recordingFrameWidth = 640
	recordingFrameCap   = 256
	// A running recording's still is taken again after this long: it is
	// taken a tenth into what has been recorded so far.
	recordingFrameRunningTTL = 10 * time.Minute
	// After a failed extraction the logo-less card stays empty a while
	// instead of starting FFmpeg on every request.
	recordingFrameRetry = time.Minute
	// recordingFrameJobs bounds the FFmpeg extractions running at once (a
	// library page of recordings without guide images asks for many).
	recordingFrameJobs = 2
)

type recordingFrame struct {
	jpeg    []byte
	err     bool
	expires time.Time // zero: kept until evicted
}

// recordingFrames caches stills per recording (and status, so a finished
// recording replaces the still taken while it was running).
type recordingFrames struct {
	mu      sync.Mutex
	entries map[string]recordingFrame
	busy    map[string]chan struct{}
}

var frameCache = &recordingFrames{entries: map[string]recordingFrame{}, busy: map[string]chan struct{}{}}

var recordingFrameSlots = make(chan struct{}, recordingFrameJobs)

// recordingImage returns the source and tag of a recording's own picture:
// its guide image, else a still from its file ("" when neither exists).
func (s *Server) recordingImage(rec models.Recording) (source, tag string) {
	if rec.Guide != nil && rec.Guide.Image != "" {
		return rec.Guide.Image, imageTag(rec.Guide.Image)
	}
	if rec.OutputPath == "" || s.deps.RecordingFrame == nil {
		return "", ""
	}
	// The status is part of the source (and so of the tag): once the
	// recording finishes, clients fetch a still of the whole recording
	// instead of the one taken early on.
	source = recordingFramePrefix + rec.ID + "|" + string(rec.Status)
	if rec.Status == models.RecordingStatusRunning || rec.Status == models.RecordingStatusStarting {
		// While recording the still is renewed (recordingFrameRunningTTL);
		// a new tag each period makes clients that cache by tag fetch it.
		source += "|" + strconv.FormatInt(time.Now().Unix()/int64(recordingFrameRunningTTL.Seconds()), 10)
	}
	return source, imageTag(source)
}

func (s *Server) serveRecordingFrame(w http.ResponseWriter, r *http.Request, frame string) {
	recordingID, status, _ := strings.Cut(frame, "|")
	status, _, _ = strings.Cut(status, "|") // drop the period of a running recording
	jpeg, ok := s.recordingFrameJPEG(r.Context(), recordingID, models.RecordingStatus(status))
	if !ok {
		writeError(w, http.StatusNotFound, "Image not available")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(jpeg)))
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write(jpeg)
}

// recordingFrameJPEG returns the cached still or extracts it; concurrent
// requests for one recording share a single extraction.
func (s *Server) recordingFrameJPEG(ctx context.Context, recordingID string, status models.RecordingStatus) ([]byte, bool) {
	if s.deps.RecordingFrame == nil || recordingID == "" {
		return nil, false
	}
	key := recordingID + "|" + string(status)
	c := frameCache
	for {
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && (e.expires.IsZero() || time.Now().Before(e.expires)) {
			c.mu.Unlock()
			return e.jpeg, !e.err
		}
		wait, busy := c.busy[key]
		if !busy {
			c.busy[key] = make(chan struct{})
			c.mu.Unlock()
			break
		}
		c.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, false
		}
	}

	select {
	case recordingFrameSlots <- struct{}{}:
	case <-ctx.Done():
		c.mu.Lock()
		close(c.busy[key])
		delete(c.busy, key)
		c.mu.Unlock()
		return nil, false
	}
	jpeg, err := s.deps.RecordingFrame(context.WithoutCancel(ctx), recordingID, recordingFrameWidth)
	<-recordingFrameSlots
	entry := recordingFrame{jpeg: jpeg}
	switch {
	case err != nil:
		log.Printf("[jellyfin-bridge] recording %s still: %v", recordingID, err)
		entry = recordingFrame{err: true, expires: time.Now().Add(recordingFrameRetry)}
	case status == models.RecordingStatusRunning || status == models.RecordingStatusStarting:
		entry.expires = time.Now().Add(recordingFrameRunningTTL)
	}
	c.mu.Lock()
	if len(c.entries) >= recordingFrameCap {
		for k := range c.entries { // evict an arbitrary entry
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = entry
	close(c.busy[key])
	delete(c.busy, key)
	c.mu.Unlock()
	return entry.jpeg, !entry.err
}

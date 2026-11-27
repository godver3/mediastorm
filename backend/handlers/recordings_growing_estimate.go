package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
	"novastream/services/recordings"
)

// A running recording for players that only play files (mpv in Plezy):
// streamed as the followed file it is (streamGrowingRecording), a player knows
// neither its length nor how to seek in it, and shows only what it has
// buffered. So with ?seekable=estimate the recording is offered as a seekable
// file of the size it will probably have when the recording ends (its data
// rate so far until the scheduled end and padding, plus a margin):
//
//   - bytes already recorded are served at once, so seeks within them work;
//   - a read that reaches the end of what is recorded waits for more;
//   - when the recording ends short of the estimate, the response ends with
//     the file (the player sees the end of the stream);
//   - a range starting well past what is recorded is served from what is
//     recorded: a read near the end of the file (FFmpeg and so mpv read the
//     last megabytes for the duration) gets the end of the recording at the
//     same distance from its end, so the player sees the length recorded so
//     far instead of what it happened to read when opening (minutes); a seek
//     past the recorded part lands shortly before its end. Either way the
//     response goes on as the recording grows.

const (
	growingEstimateMargin   = 1.1
	growingEstimateLiveEdge = 20 * time.Second // ahead of the recorded end a range may start and wait
	growingEstimatePoll     = 500 * time.Millisecond
	growingEstimateLiveLead = 10 * time.Second // how far before the recorded end a seek past it lands
	// growingEstimateTailProbe covers the reads FFmpeg makes at the end of a
	// file for its duration (250 kB, doubled up to 6 times).
	growingEstimateTailProbe = 32 << 20
	// The rate is measured from the recording's start, which comes before
	// the source has delivered anything (connecting, probing). Early on that
	// halves it, so the first minutes are estimated with a wider margin; an
	// estimate too small would end playback while the recording goes on.
	growingEstimateEarly       = 2 * time.Minute
	growingEstimateEarlyMargin = 2.0
	// A recording holding less than growingEstimateMinData is waited for.
	growingEstimateWait = 15 * time.Second
)

// growingEstimateMinData is what a recording has to hold before it is
// estimated (a variable for tests).
var growingEstimateMinData int64 = 1 << 20

// growingEstimates keeps the largest size offered per running recording, so a
// player that keeps the size of its first response never asks past a later,
// smaller estimate.
var growingEstimates sync.Map // recording ID -> int64

// estimatedRecordingSize is the size a running recording will probably have. content is what it
// holds so far when the recorder knows (a recording catching up from its source's DVR window
// fills faster than the clock runs); 0: as much as has passed since its start.
func estimatedRecordingSize(rec models.Recording, size int64, now time.Time, content time.Duration) int64 {
	start := rec.StartAt
	if rec.ActualStartAt != nil {
		start = *rec.ActualStartAt
	}
	end := rec.EndAt.Add(time.Duration(rec.PaddingAfterSeconds) * time.Second)
	recorded, position := now.Sub(start).Seconds(), now
	if content > 0 {
		recorded, position = content.Seconds(), start.Add(content)
	}
	if size <= 0 || recorded < 1 {
		return size
	}
	rate := float64(size) / recorded
	margin := growingEstimateMargin
	if recorded < growingEstimateEarly.Seconds() {
		margin = growingEstimateEarlyMargin
	}
	remaining := max(end.Sub(position).Seconds(), 60) // running past its end: a minute ahead
	return size + int64(rate*remaining*margin)
}

// recordedContent is how much content a running recording holds when the recordings service
// follows it (followed; 0 until FFmpeg's first report): one catching up from its source's DVR
// window fills faster than the clock runs.
func (h *RecordingsHandler) recordedContent(id string) (content time.Duration, followed bool) {
	if follower, ok := h.service.(interface {
		RecordedContent(string) (time.Duration, bool)
	}); ok {
		return follower.RecordedContent(id)
	}
	return 0, false
}

// offeredRecordingSize is estimatedRecordingSize, never smaller than what was
// offered for the recording before.
func offeredRecordingSize(rec models.Recording, size int64, now time.Time, content time.Duration) int64 {
	total := estimatedRecordingSize(rec, size, now, content)
	for {
		prev, loaded := growingEstimates.LoadOrStore(rec.ID, total)
		if !loaded {
			return total
		}
		if prev.(int64) >= total {
			return prev.(int64)
		}
		if growingEstimates.CompareAndSwap(rec.ID, prev, total) {
			return total
		}
	}
}

// parseSingleRange reads "bytes=a-b", "bytes=a-" and "bytes=-n" against total.
func parseSingleRange(header string, total int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" { // suffix: the last n bytes
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return max(total-n, 0), total - 1, true
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end = total - 1
	if b != "" {
		if end, err = strconv.ParseInt(b, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		end = min(end, total-1)
	}
	return start, end, true
}

func (h *RecordingsHandler) streamGrowingRecordingEstimated(w http.ResponseWriter, r *http.Request, rec models.Recording, outputPath string) error {
	file, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < growingEstimateMinData {
		// Just started: too little to estimate from (an empty file would be
		// offered as empty). Wait for some data, or for the recording to end.
		ctx, cancel := context.WithTimeout(r.Context(), growingEstimateWait)
		h.waitForBytes(r.WithContext(ctx), rec.ID, file, growingEstimateMinData)
		cancel()
		if r.Context().Err() != nil {
			return nil
		}
		if info, err = file.Stat(); err != nil {
			return err
		}
	}
	content, followed := h.recordedContent(rec.ID)
	if followed && content <= 0 {
		// Catching up from the source's window without a report yet: the time since its
		// (backdated) start says nothing about what the file holds. Wait for one.
		waitUntil := time.Now().Add(growingEstimateWait)
		for followed && content <= 0 && time.Now().Before(waitUntil) {
			select {
			case <-r.Context().Done():
				return nil
			case <-time.After(growingEstimatePoll):
			}
			content, followed = h.recordedContent(rec.ID)
		}
		if followed && content <= 0 {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "recording has no data yet", http.StatusServiceUnavailable)
			return nil
		}
		if info, err = file.Stat(); err != nil {
			return err
		}
	}
	size := info.Size()
	now := time.Now()
	total := offeredRecordingSize(rec, size, now, content)
	if total <= 0 {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "recording has no data yet", http.StatusServiceUnavailable)
		return nil
	}

	start, end, partial := int64(0), total-1, false
	if header := r.Header.Get("Range"); header != "" {
		var ok bool
		if start, end, ok = parseSingleRange(header, total); !ok || start >= total {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return nil
		}
		partial = true
	}
	readFrom := start
	if start > size {
		// Not recorded yet (see above).
		started := rec.StartAt
		if rec.ActualStartAt != nil {
			started = *rec.ActualStartAt
		}
		recorded := now.Sub(started).Seconds()
		if content > 0 {
			recorded = content.Seconds()
		}
		rate := float64(size) / max(recorded, 1)
		switch {
		case float64(start-size) <= rate*growingEstimateLiveEdge.Seconds():
			if !h.waitForBytes(r, rec.ID, file, start) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return nil
			}
		case total-start <= growingEstimateTailProbe:
			readFrom = max(size-(total-start), 0)
		default:
			readFrom = max(size-int64(rate*growingEstimateLiveLead.Seconds()), 0)
		}
	}
	if _, err := file.Seek(readFrom, io.SeekStart); err != nil {
		return err
	}

	length := end - start + 1
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		return nil
	}
	flusher := http.NewResponseController(w)
	buf := make([]byte, 256*1024)
	var written int64
	for written < length {
		n, readErr := file.Read(buf[:min(int64(len(buf)), length-written)])
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			_ = flusher.Flush() // writers without Flush are fine
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		// At the recorded end: wait for more, or end with the recording.
		if !h.waitForBytes(r, rec.ID, file, readFrom+written+1) {
			return nil
		}
	}
	return nil
}

// waitForBytes waits until the file holds more than offset-1 bytes. It
// reports false when the request is gone or the recording has stopped
// without reaching offset.
func (h *RecordingsHandler) waitForBytes(r *http.Request, recordingID string, file *os.File, offset int64) bool {
	ticker := time.NewTicker(growingEstimatePoll)
	defer ticker.Stop()
	for {
		if info, err := file.Stat(); err == nil && info.Size() >= offset {
			return true
		}
		latest, err := h.service.Get(recordingID)
		if errors.Is(err, recordings.ErrRecordingNotFound) || (err == nil && latest == nil) {
			growingEstimates.Delete(recordingID)
			return false // deleted
		}
		if err == nil && latest.Status != models.RecordingStatusRunning && latest.Status != models.RecordingStatusStarting {
			growingEstimates.Delete(recordingID)
			if info, err := file.Stat(); err == nil && info.Size() >= offset {
				return true
			}
			return false
		}
		select {
		case <-r.Context().Done():
			return false
		case <-ticker.C:
		}
	}
}

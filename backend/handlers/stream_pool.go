package handlers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/services/streaming"
)

const (
	poolMaxSlotsPerFile = 4                // max concurrent CDN connections per file
	poolSlotBufferMax   = 32 * 1024 * 1024 // 32MB sliding window buffer per slot
	poolSlotBufferTrim  = 24 * 1024 * 1024 // trim to 24MB when buffer exceeds max
	poolSlotIdleTimeout = 30 * time.Second  // evict slots with no readers for this long
	poolReaperInterval  = 10 * time.Second  // how often to check for idle slots
	poolSlotReadChunk   = 256 * 1024       // 256KB CDN read chunks
	poolSlotBufferHard  = 128 * 1024 * 1024 // 128MB hard limit per slot (when readers prevent trim)
	poolMaxWaitAhead    = 8 * 1024 * 1024   // reuse slot if CDN reader is within 8MB of target
	poolWaitTimeout     = 10 * time.Second   // max time to wait for CDN reader to reach target
)

// streamPool maintains persistent CDN connections that survive client disconnects.
// This prevents seek storms when players (e.g., KSPlayer) alternate between audio
// and video track positions in non-interleaved MP4 files. Instead of creating a
// new CDN connection for each seek request, the pool keeps reading ahead from the
// CDN in the background, so subsequent requests at nearby positions are served
// instantly from the buffer.
type streamPool struct {
	mu    sync.RWMutex
	files map[string][]*poolSlot
	done  chan struct{}
}

type poolSlot struct {
	mu sync.Mutex

	// File position and buffer
	path      string
	startByte int64  // file offset corresponding to data[0]
	data      []byte // sliding window buffer (grows, trimmed at poolSlotBufferMax)

	// CDN connection state
	cdnDone bool  // background reader finished (EOF or error)
	cdnErr  error // terminal error from CDN
	ctx     context.Context
	cancel  context.CancelFunc

	// Metadata from CDN response
	totalSize   int64  // total file size (from Content-Range header)
	filename    string // filename for display headers
	respStatus  int    // HTTP status from CDN (usually 206)
	contentType string // Content-Type from CDN

	// Usage tracking
	lastAccess time.Time
	readers    int32 // atomic: active reader count

	// Notification: closed when new data is written, then replaced with a fresh channel
	signal chan struct{}
}

func newStreamPool() *streamPool {
	p := &streamPool{
		files: make(map[string][]*poolSlot),
		done:  make(chan struct{}),
	}
	go p.reaper()
	return p
}

func (p *streamPool) close() {
	close(p.done)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, slots := range p.files {
		for _, s := range slots {
			s.cancel()
		}
	}
	p.files = nil
}

func (p *streamPool) reaper() {
	ticker := time.NewTicker(poolReaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.evictIdle()
		}
	}
}

func (p *streamPool) evictIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for path, slots := range p.files {
		live := slots[:0]
		for _, s := range slots {
			s.mu.Lock()
			idle := now.Sub(s.lastAccess) > poolSlotIdleTimeout && atomic.LoadInt32(&s.readers) == 0
			s.mu.Unlock()
			if idle {
				log.Printf("[stream-pool] evicting idle slot: path=%q startByte=%d buffered=%d",
					path, s.startByte, len(s.data))
				s.cancel()
			} else {
				live = append(live, s)
			}
		}
		if len(live) == 0 {
			delete(p.files, path)
		} else {
			p.files[path] = live
		}
	}
}

// serve attempts to serve a range request from the pool. It finds or creates
// a pool slot, then streams data from the slot's buffer to the client.
// Returns (true, err) if handled, (false, nil) if unable to serve.
func (p *streamPool) serve(
	w http.ResponseWriter,
	r *http.Request,
	path string,
	reqStart int64,
	streamer streaming.Provider,
	writeHeaders func(w http.ResponseWriter),
	displayName string,
	accountID string,
) (bool, error) {
	slot, err := p.getOrCreate(path, reqStart, streamer)
	if err != nil {
		return false, nil
	}

	atomic.AddInt32(&slot.readers, 1)
	defer atomic.AddInt32(&slot.readers, -1)

	ctx := r.Context()

	// Wait for initial data to be available at the requested position
	slot.mu.Lock()
	slot.lastAccess = time.Now()
	endPos := slot.startByte + int64(len(slot.data))
	totalSize := slot.totalSize
	filename := slot.filename
	contentType := slot.contentType
	status := slot.respStatus
	ch := slot.signal
	slot.mu.Unlock()

	// If requested position is before the slot's buffer start, can't serve
	if reqStart < slot.startByte {
		log.Printf("[stream-pool] MISS: data trimmed past reqStart=%d slotStart=%d", reqStart, slot.startByte)
		return false, nil
	}

	// If data not yet available at requested position, wait for CDN reader
	if reqStart >= endPos {
		waitDeadline := time.After(poolWaitTimeout)
		for {
			select {
			case <-ch:
				slot.mu.Lock()
				endPos = slot.startByte + int64(len(slot.data))
				done := slot.cdnDone
				ch = slot.signal
				slot.mu.Unlock()
				if reqStart < endPos {
					goto dataReady
				}
				if done {
					log.Printf("[stream-pool] CDN finished before reaching reqStart=%d endPos=%d", reqStart, endPos)
					return false, nil
				}
			case <-waitDeadline:
				log.Printf("[stream-pool] TIMEOUT waiting for data: reqStart=%d endPos=%d", reqStart, endPos)
				return false, nil
			case <-ctx.Done():
				return true, nil
			}
		}
	}

dataReady:
	// Write response headers
	writeHeaders(w)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Accept-Ranges", "bytes")

	if totalSize > 0 && reqStart < totalSize {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", reqStart, totalSize-1, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize-reqStart, 10))
	}

	// Set filename headers for external players
	fn := displayName
	if fn == "" {
		fn = filename
	}
	if fn == "" {
		fn = inferFilenameFromPath(path)
	}
	if fn != "" {
		w.Header().Set("X-Filename", fn)
		w.Header().Set("Content-Disposition", buildInlineContentDisposition(fn))
	}

	if status == 0 {
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return true, nil
	}

	// Flush headers to the network IMMEDIATELY so the client sees the response
	// start before we do any more setup work. This is critical for fast responses.
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Start stream tracking (after header flush to minimize time-to-first-byte)
	tracker := GetStreamTracker()
	expectedLen := int64(0)
	if totalSize > 0 {
		expectedLen = totalSize - reqStart
	}
	streamID, bytesCounter, activityCounter := tracker.StartStreamWithAccount(r, path, expectedLen, reqStart, 0, accountID)
	defer tracker.EndStream(streamID)

	// Stream data from slot buffer to client.
	// IMPORTANT: Write data immediately without checking ctx.Done() first.
	// The player sends rapid-fire requests and cancels old ones within milliseconds.
	// If we check ctx.Done() before writing, we lose the race and deliver 0 bytes.
	// Instead, we attempt the write directly — if the client is gone, Write() will
	// return an error, which is the only reliable signal.
	flusher, _ := w.(http.Flusher)
	pos := reqStart
	var totalWritten int64

	log.Printf("[stream-pool] serving: path=%q reqStart=%d slotStart=%d buffered=%d",
		path, reqStart, slot.startByte, endPos-slot.startByte)

	for {
		slot.mu.Lock()
		// Check if the buffer has been trimmed past our read position
		if pos < slot.startByte {
			slot.mu.Unlock()
			log.Printf("[stream-pool] buffer trimmed past reader: path=%q pos=%d slotStart=%d", path, pos, slot.startByte)
			return true, fmt.Errorf("buffer trimmed past reader position")
		}

		offset := pos - slot.startByte
		available := int64(len(slot.data)) - offset

		if available <= 0 {
			if slot.cdnDone {
				slot.mu.Unlock()
				break
			}
			// Wait for more data from CDN reader — this is the ONLY place
			// we check ctx.Done(), because we'd otherwise block forever.
			ch = slot.signal
			slot.lastAccess = time.Now()
			slot.mu.Unlock()

			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return true, nil
			}
		}

		// Copy ALL available data from buffer in one shot (up to 4MB).
		// Larger writes deliver more data per HTTP response cycle, which is
		// critical when the player cancels connections after each chunk.
		const maxWriteSize = 4 * 1024 * 1024
		n := int(available)
		if n > maxWriteSize {
			n = maxWriteSize
		}
		chunk := make([]byte, n)
		copy(chunk, slot.data[offset:offset+int64(n)])
		slot.lastAccess = time.Now()
		slot.mu.Unlock()

		// Write directly — don't check ctx.Done() beforehand.
		// The write itself is the fastest path to getting data to the client.
		written, writeErr := w.Write(chunk)
		if writeErr != nil {
			if isClientGone(writeErr) && totalWritten > 0 {
				// Only log if we actually sent some data (reduce noise)
				log.Printf("[stream-pool] client gone: path=%q pos=%d written=%d", path, pos, totalWritten)
			}
			return true, nil
		}

		pos += int64(written)
		totalWritten += int64(written)

		if bytesCounter != nil {
			atomic.StoreInt64(bytesCounter, totalWritten)
		}
		if activityCounter != nil {
			atomic.StoreInt64(activityCounter, time.Now().UnixNano())
		}

		if flusher != nil {
			flusher.Flush()
		}
	}

	log.Printf("[stream-pool] stream complete: path=%q written=%d", path, totalWritten)
	return true, nil
}

// findSlot returns an existing pool slot that can serve data at reqPos, or nil.
func (p *streamPool) findSlot(path string, reqPos int64) *poolSlot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	slots := p.files[path]
	var best *poolSlot
	var bestDist int64 = int64(^uint64(0) >> 1) // MaxInt64

	for _, s := range slots {
		s.mu.Lock()
		// Skip slots with terminal CDN errors
		if s.cdnDone && s.cdnErr != nil {
			s.mu.Unlock()
			continue
		}

		endPos := s.startByte + int64(len(s.data))

		if reqPos >= s.startByte && reqPos < endPos {
			// Data already available in buffer — perfect match
			s.mu.Unlock()
			return s
		}

		if reqPos >= s.startByte && !s.cdnDone {
			// CDN reader hasn't reached reqPos yet but might catch up
			dist := reqPos - endPos
			if dist >= 0 && dist < poolMaxWaitAhead && dist < bestDist {
				bestDist = dist
				best = s
			}
		}
		s.mu.Unlock()
	}

	return best
}

// getOrCreate finds an existing slot or creates a new one with a CDN connection.
func (p *streamPool) getOrCreate(path string, reqPos int64, streamer streaming.Provider) (*poolSlot, error) {
	if slot := p.findSlot(path, reqPos); slot != nil {
		slot.mu.Lock()
		buffered := int64(len(slot.data))
		slot.mu.Unlock()
		log.Printf("[stream-pool] REUSE slot: path=%q reqPos=%d slotStart=%d buffered=%d",
			path, reqPos, slot.startByte, buffered)
		return slot, nil
	}

	// Create a new slot — start a fresh CDN connection at reqPos
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	rangeHeader := fmt.Sprintf("bytes=%d-", reqPos)

	resp, err := streamer.Stream(ctx, streaming.Request{
		Path:        path,
		RangeHeader: rangeHeader,
		Method:      http.MethodGet,
	})
	if err != nil {
		cancel()
		return nil, err
	}

	// Parse total file size from Content-Range header
	var totalSize int64
	contentRange := resp.Headers.Get("Content-Range")
	if contentRange != "" {
		totalSize = parseTotalSizeFromContentRange(contentRange)
	}

	contentType := resp.Headers.Get("Content-Type")
	if contentType == "" {
		contentType = "video/mp4"
	}

	slot := &poolSlot{
		path:        path,
		startByte:   reqPos,
		data:        make([]byte, 0, 1024*1024), // start 1MB, grows as needed
		ctx:         ctx,
		cancel:      cancel,
		totalSize:   totalSize,
		filename:    resp.Filename,
		respStatus:  resp.Status,
		contentType: contentType,
		lastAccess:  time.Now(),
		signal:      make(chan struct{}),
	}

	// Register slot, evicting the least recently used slot if at capacity
	p.mu.Lock()
	slots := p.files[path]
	if len(slots) >= poolMaxSlotsPerFile {
		lruIdx := 0
		lruTime := time.Now()
		for i, s := range slots {
			s.mu.Lock()
			la := s.lastAccess
			readers := atomic.LoadInt32(&s.readers)
			s.mu.Unlock()
			// Prefer to evict slots with no active readers; among those, pick oldest
			if readers == 0 && la.Before(lruTime) {
				lruTime = la
				lruIdx = i
			}
		}
		evicted := slots[lruIdx]
		evicted.mu.Lock()
		evictedStart := evicted.startByte
		evicted.mu.Unlock()
		log.Printf("[stream-pool] evicting LRU slot: startByte=%d newPos=%d",
			evictedStart, reqPos)
		evicted.cancel()
		slots = append(slots[:lruIdx], slots[lruIdx+1:]...)
	}
	p.files[path] = append(slots, slot)
	p.mu.Unlock()

	// Start background CDN reader
	go slot.backgroundReader(resp)

	log.Printf("[stream-pool] NEW slot: path=%q startByte=%d totalSize=%d", path, reqPos, totalSize)
	return slot, nil
}

// backgroundReader continuously reads from the CDN response body into the
// slot's buffer. It survives client disconnects and continues buffering
// data for future requests at nearby positions.
func (s *poolSlot) backgroundReader(resp *streaming.Response) {
	defer resp.Close()
	defer func() {
		s.mu.Lock()
		s.cdnDone = true
		s.mu.Unlock()
		s.broadcast()
	}()

	buf := make([]byte, poolSlotReadChunk)
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		// Backpressure: if buffer is at the hard limit (active readers preventing
		// trim), pause reading from CDN until readers catch up or disconnect.
		s.mu.Lock()
		for len(s.data) >= poolSlotBufferHard {
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			s.mu.Lock()
		}
		s.mu.Unlock()

		n, err := resp.Body.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.data = append(s.data, buf[:n]...)
			// Only trim when NO active readers are consuming the buffer.
			// When readers are active, their position may be anywhere in the buffer,
			// and trimming could evict data they haven't read yet.
			// The hard limit above prevents unbounded growth.
			if len(s.data) > poolSlotBufferMax && atomic.LoadInt32(&s.readers) == 0 {
				trimAmount := len(s.data) - poolSlotBufferTrim
				// Copy to new slice to release the old backing array
				remaining := len(s.data) - trimAmount
				newData := make([]byte, remaining, remaining+4*1024*1024)
				copy(newData, s.data[trimAmount:])
				s.data = newData
				s.startByte += int64(trimAmount)
			}
			s.mu.Unlock()
			s.broadcast()
		}
		if err != nil {
			if err != io.EOF {
				s.mu.Lock()
				s.cdnErr = err
				s.mu.Unlock()
				log.Printf("[stream-pool] CDN read error: path=%q err=%v", s.path, err)
			}
			return
		}
	}
}

// broadcast wakes all goroutines waiting for new data from this slot.
func (s *poolSlot) broadcast() {
	s.mu.Lock()
	old := s.signal
	s.signal = make(chan struct{})
	s.mu.Unlock()
	close(old)
}

// parseRangeStart extracts the start byte from a Range header, including
// open-ended ranges like "bytes=12345-" that parseByteRange rejects.
func parseRangeStart(rangeHeader string) (int64, bool) {
	rangeHeader = strings.TrimSpace(rangeHeader)
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, false
	}
	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, false
	}
	startStr := strings.TrimSpace(spec[:dash])
	if startStr == "" {
		return 0, false
	}
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}

// parseTotalSizeFromContentRange extracts the total file size from a
// Content-Range header like "bytes 0-999/5000".
func parseTotalSizeFromContentRange(cr string) int64 {
	cr = strings.TrimSpace(cr)
	if idx := strings.Index(cr, "/"); idx >= 0 {
		totalStr := strings.TrimSpace(cr[idx+1:])
		if totalStr != "*" {
			if total, err := strconv.ParseInt(totalStr, 10, 64); err == nil {
				return total
			}
		}
	}
	return 0
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

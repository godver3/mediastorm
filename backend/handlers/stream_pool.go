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
	poolMaxSlotsPerFile = 4                 // max concurrent CDN connections per file
	poolSlotBufferMax   = 32 * 1024 * 1024  // 32MB sliding window buffer per slot
	poolSlotBufferTrim  = 24 * 1024 * 1024  // trim to 24MB when buffer exceeds max
	poolSlotIdleTimeout = 30 * time.Second  // evict slots with no readers for this long
	poolReaperInterval  = 10 * time.Second  // how often to check for idle slots
	poolSlotReadChunk   = 256 * 1024        // 256KB CDN read chunks
	poolSlotBufferHard  = 128 * 1024 * 1024 // 128MB hard limit per slot (when readers prevent trim)
	poolMaxWaitAhead    = 8 * 1024 * 1024   // reuse slot if CDN reader is within 8MB of target
	poolWaitTimeout     = 10 * time.Second  // max time to wait for CDN reader to reach target
	poolMinPreBuffer    = 4 * 1024 * 1024   // 4MB minimum buffer before serving starts (prevents stalls on 4K)
)

// streamPool maintains persistent CDN connections that survive client disconnects.
// This prevents seek storms when players (e.g., KSPlayer) alternate between audio
// and video track positions in non-interleaved MP4 files. Instead of creating a
// new CDN connection for each seek request, the pool keeps reading ahead from the
// CDN in the background, so subsequent requests at nearby positions are served
// instantly from the buffer.
type streamPool struct {
	mu       sync.RWMutex
	files    map[string][]*poolSlot
	done     chan struct{}
	failures *streamFailureRegistry
}

type poolSlot struct {
	mu sync.Mutex

	// File position and buffer
	path      string
	startByte int64  // file offset corresponding to data[0]
	data      []byte // sliding window buffer (grows, trimmed at poolSlotBufferMax)

	// CDN connection state
	cdnDone  bool  // background reader finished (EOF or error)
	cdnErr   error // terminal error from CDN
	ctx      context.Context
	cancel   context.CancelFunc
	failures *streamFailureRegistry

	// Metadata from CDN response
	totalSize   int64  // total file size (from Content-Range header)
	filename    string // filename for display headers
	respStatus  int    // HTTP status from CDN (usually 206)
	contentType string // Content-Type from CDN

	// Usage tracking
	lastAccess    time.Time
	readers       int32 // atomic: active reader count
	minReaderPos  int64 // lowest active reader position (updated by readers under mu)
	totalRead     int64 // cumulative bytes read from CDN into this slot
	lastReadAt    time.Time
	readStartedAt time.Time

	// Notification: closed when new data is written, then replaced with a fresh channel
	signal chan struct{}
}

func newStreamPool(failures *streamFailureRegistry) *streamPool {
	p := &streamPool{
		files:    make(map[string][]*poolSlot),
		done:     make(chan struct{}),
		failures: failures,
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

	// Register reader and track position for safe buffer trimming
	slot.mu.Lock()
	atomic.AddInt32(&slot.readers, 1)
	if atomic.LoadInt32(&slot.readers) == 1 || reqStart < slot.minReaderPos {
		slot.minReaderPos = reqStart
	}
	slot.mu.Unlock()
	defer func() {
		atomic.AddInt32(&slot.readers, -1)
	}()

	ctx := r.Context()
	requestStartedAt := time.Now()
	rangeHeader := r.Header.Get("Range")

	// Wait for initial data to be available at the requested position
	slot.mu.Lock()
	slot.lastAccess = time.Now()
	endPos := slot.startByte + int64(len(slot.data))
	totalSize := slot.totalSize
	filename := slot.filename
	contentType := slot.contentType
	status := slot.respStatus
	slotTotalRead := slot.totalRead
	slotLastReadAt := slot.lastReadAt
	slotReadStartedAt := slot.readStartedAt
	ch := slot.signal
	slot.mu.Unlock()

	// If requested position is before the slot's buffer start, can't serve
	if reqStart < slot.startByte {
		log.Printf("[stream-pool] MISS: data trimmed past reqStart=%d slotStart=%d", reqStart, slot.startByte)
		return false, nil
	}

	// If data not yet available at requested position, wait for CDN reader.
	// Also wait for minimum pre-buffer to accumulate before serving, so the
	// client has enough data to start playing without immediately stalling
	// (especially important for high-bitrate 4K content over slower connections).
	buffered := endPos - reqStart
	needsWait := reqStart >= endPos || buffered < poolMinPreBuffer
	if needsWait {
		gap := reqStart - endPos
		if gap < 0 {
			gap = 0
		}
		waitStart := time.Now()
		log.Printf("[stream-pool] WAIT-START: path=%q range=%q reqStart=%d endPos=%d gap=%d slotStart=%d cdnDone=%v preBuffer=%d/%d slotTotalRead=%d slotAge=%v sinceLastRead=%v readers=%d",
			path, rangeHeader, reqStart, endPos, gap, slot.startByte, false, buffered, poolMinPreBuffer,
			slotTotalRead, time.Since(slotReadStartedAt).Round(time.Millisecond), time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
		waitDeadline := time.After(poolWaitTimeout)
		for {
			select {
			case <-ch:
				slot.mu.Lock()
				endPos = slot.startByte + int64(len(slot.data))
				done := slot.cdnDone
				slotTotalRead = slot.totalRead
				slotLastReadAt = slot.lastReadAt
				ch = slot.signal
				slot.mu.Unlock()
				buffered = endPos - reqStart
				if reqStart < endPos && (buffered >= poolMinPreBuffer || done) {
					log.Printf("[stream-pool] WAIT-OK: path=%q waited=%v gap=%d newEndPos=%d preBuffer=%d slotTotalRead=%d sinceLastRead=%v readers=%d",
						path, time.Since(waitStart).Round(time.Millisecond), gap, endPos, buffered, slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
					goto dataReady
				}
				if done && reqStart >= endPos {
					log.Printf("[stream-pool] CDN finished before reaching reqStart=%d endPos=%d", reqStart, endPos)
					return false, nil
				}
			case <-waitDeadline:
				slot.mu.Lock()
				currentEnd := slot.startByte + int64(len(slot.data))
				remaining := reqStart - currentEnd
				cdnDone := slot.cdnDone
				slotTotalRead = slot.totalRead
				slotLastReadAt = slot.lastReadAt
				slot.mu.Unlock()
				buffered = currentEnd - reqStart
				if buffered > 0 {
					// Timeout but we have some data — serve what we have rather than failing
					log.Printf("[stream-pool] WAIT-PARTIAL: path=%q waited=%v preBuffer=%d/%d slotTotalRead=%d sinceLastRead=%v readers=%d (serving with partial buffer)",
						path, time.Since(waitStart).Round(time.Millisecond), buffered, poolMinPreBuffer, slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
					endPos = currentEnd
					goto dataReady
				}
				log.Printf("[stream-pool] TIMEOUT waiting for data: path=%q reqStart=%d endPos=%d remaining=%d cdnDone=%v elapsed=%v slotTotalRead=%d sinceLastRead=%v readers=%d",
					path, reqStart, currentEnd, remaining, cdnDone, time.Since(waitStart).Round(time.Millisecond), slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
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

	log.Printf("[stream-pool] serving: path=%q range=%q reqStart=%d slotStart=%d buffered=%d slotTotalRead=%d readers=%d",
		path, rangeHeader, reqStart, slot.startByte, endPos-slot.startByte, slotTotalRead, atomic.LoadInt32(&slot.readers))

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
			waitForDataStart := time.Now()

			select {
			case <-ch:
				if waited := time.Since(waitForDataStart); waited >= 200*time.Millisecond {
					slot.mu.Lock()
					currentEnd := slot.startByte + int64(len(slot.data))
					slotTotalRead = slot.totalRead
					slotLastReadAt = slot.lastReadAt
					slot.mu.Unlock()
					log.Printf("[stream-pool] READER-WAIT: path=%q pos=%d waited=%v currentEnd=%d slotTotalRead=%d sinceLastRead=%v readers=%d",
						path, pos, waited.Round(time.Millisecond), currentEnd, slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
				}
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

		// Update minimum reader position so the background reader
		// knows how far it can safely trim the buffer.
		slot.mu.Lock()
		if pos > slot.minReaderPos {
			slot.minReaderPos = pos
		}
		slot.mu.Unlock()

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

	elapsed := time.Since(requestStartedAt)
	rateMBps := 0.0
	if elapsed > 0 {
		rateMBps = (float64(totalWritten) / 1024.0 / 1024.0) / elapsed.Seconds()
	}
	log.Printf("[stream-pool] stream complete: path=%q written=%d elapsed=%v avgRate=%.2fMBps", path, totalWritten, elapsed.Round(time.Millisecond), rateMBps)
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
		endPos := slot.startByte + buffered
		cdnDone := slot.cdnDone
		slot.mu.Unlock()
		gap := reqPos - endPos
		if gap < 0 {
			gap = 0
		}
		log.Printf("[stream-pool] REUSE slot: path=%q reqPos=%d slotStart=%d buffered=%d endPos=%d gap=%d cdnDone=%v",
			path, reqPos, slot.startByte, buffered, endPos, gap, cdnDone)
		return slot, nil
	}

	// Create a new slot — start a fresh CDN connection at reqPos.
	// The slot lifetime is controlled by the client/reaper paths. Do not put a
	// fixed timeout on this context: long-running playback can legitimately keep
	// a slot alive past 30 minutes, and a context deadline here forces a CDN
	// reconnect that surfaces to the player as buffering.
	ctx, cancel := context.WithCancel(context.Background())
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
		path:          path,
		startByte:     reqPos,
		data:          make([]byte, 0, 1024*1024), // start 1MB, grows as needed
		ctx:           ctx,
		cancel:        cancel,
		failures:      p.failures,
		totalSize:     totalSize,
		filename:      resp.Filename,
		respStatus:    resp.Status,
		contentType:   contentType,
		lastAccess:    time.Now(),
		lastReadAt:    time.Now(),
		readStartedAt: time.Now(),
		signal:        make(chan struct{}),
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

	log.Printf("[stream-pool] NEW slot: path=%q startByte=%d totalSize=%d contentType=%q status=%d", path, reqPos, totalSize, contentType, resp.Status)
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

		// Backpressure: if buffer is at the hard limit, trim data that
		// readers have already consumed. If readers are active, only trim
		// up to minReaderPos to avoid invalidating their read position.
		// If no room can be freed, sleep briefly to apply backpressure
		// to the CDN download (TCP window will close naturally).
		s.mu.Lock()
		if len(s.data) >= poolSlotBufferHard {
			readers := atomic.LoadInt32(&s.readers)
			safeTrimTo := s.minReaderPos
			if readers == 0 {
				// No readers — safe to trim aggressively
				safeTrimTo = s.startByte + int64(len(s.data))
			}
			maxTrim := int(safeTrimTo - s.startByte)
			if maxTrim > 0 {
				// Trim only data already consumed by all readers
				trimAmount := len(s.data) - poolSlotBufferTrim
				if trimAmount > maxTrim {
					trimAmount = maxTrim
				}
				if trimAmount > 0 {
					remaining := len(s.data) - trimAmount
					newData := make([]byte, remaining, remaining+4*1024*1024)
					copy(newData, s.data[trimAmount:])
					oldStart := s.startByte
					s.data = newData
					s.startByte += int64(trimAmount)
					log.Printf("[stream-pool] backpressure trim: path=%q oldStart=%d newStart=%d trimmed=%d readers=%d minReaderPos=%d",
						s.path, oldStart, s.startByte, trimAmount, readers, safeTrimTo)
				}
			}
			if len(s.data) >= poolSlotBufferHard {
				// Still at hard limit — can't trim more without passing readers.
				// Sleep briefly to let readers catch up (backpressure on CDN).
				s.mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				continue
			}
		}
		s.mu.Unlock()

		n, err := resp.Body.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.data = append(s.data, buf[:n]...)
			s.totalRead += int64(n)
			s.lastReadAt = time.Now()
			// Trim when buffer exceeds the sliding window size, but ONLY
			// when no readers are active. When readers are consuming data,
			// let the buffer grow toward the hard limit (poolSlotBufferHard)
			// to avoid trimming past their read position and causing errors.
			if len(s.data) > poolSlotBufferMax && atomic.LoadInt32(&s.readers) == 0 {
				trimAmount := len(s.data) - poolSlotBufferTrim
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
				if s.failures != nil && s.failures.recordIfMissingArticles(s.path, err) {
					log.Printf("[stream-migration] confirmed missing-article stream failure in stream pool path=%q err=%v", s.path, err)
				}
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

// PoolStats holds a snapshot of stream pool memory and slot usage.
type PoolStats struct {
	TotalSlots    int
	ActiveSlots   int   // slots with active readers
	TotalBufferMB int64 // total buffer memory across all slots
}

// Stats returns a snapshot of the pool's current resource usage.
func (p *streamPool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var stats PoolStats
	for _, slots := range p.files {
		for _, s := range slots {
			s.mu.Lock()
			stats.TotalSlots++
			stats.TotalBufferMB += int64(len(s.data))
			if atomic.LoadInt32(&s.readers) > 0 {
				stats.ActiveSlots++
			}
			s.mu.Unlock()
		}
	}
	stats.TotalBufferMB /= 1024 * 1024
	return stats
}

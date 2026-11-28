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
	poolUpstreamStall   = 6 * time.Second   // reopen a body that stops yielding bytes while playback is active
	poolMaxReconnects   = 2                 // consecutive no-progress reconnects before failing the slot
)

func initialPreBufferTarget(reqStart, reqEnd, totalSize int64) int64 {
	target := int64(poolMinPreBuffer)
	if reqEnd >= reqStart {
		if requested := reqEnd - reqStart + 1; requested > 0 && requested < target {
			target = requested
		}
	}
	if totalSize > 0 && reqStart < totalSize {
		if remaining := totalSize - reqStart; remaining > 0 && remaining < target {
			target = remaining
		}
	}
	return target
}

// streamPool maintains persistent CDN connections that survive client disconnects.
// This prevents seek storms when players (e.g., KSPlayer) alternate between audio
// and video track positions in non-interleaved MP4 files. Instead of creating a
// new CDN connection for each seek request, the pool keeps reading ahead from the
// CDN in the background, so subsequent requests at nearby positions are served
// instantly from the buffer.
type streamPool struct {
	mu            sync.RWMutex
	creationMu    sync.Mutex // protects creationGates; never held during provider I/O
	creationGates map[string]*poolCreationGate
	files         map[string][]*poolSlot
	done          chan struct{}
	failures      *streamFailureRegistry
}

type poolCreationGate struct {
	semaphore chan struct{}
	refs      int
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
	streamer streaming.Provider

	// Tests can shorten the watchdog without weakening the production timeout.
	readStallTimeout time.Duration

	// Metadata from CDN response
	totalSize   int64  // total file size (from Content-Range header)
	filename    string // filename for display headers
	respStatus  int    // HTTP status from CDN (usually 206)
	contentType string // Content-Type from CDN

	// Usage tracking
	lastAccess      time.Time
	readers         int32 // atomic: active reader count
	minReaderPos    int64 // lowest active reader position (updated from readerPositions under mu)
	nextReaderID    uint64
	readerPositions map[uint64]int64
	totalRead       int64 // cumulative bytes read from CDN into this slot
	lastReadAt      time.Time
	readStartedAt   time.Time

	// Notification: closed when new data is written, then replaced with a fresh channel
	signal chan struct{}

	// Notification: closed and replaced while holding mu whenever the active
	// reader count changes. The background reader uses this separate channel to
	// pause the upstream connection while nobody is consuming the slot without
	// missing a reconnect that races with entering the wait.
	readerChanged chan struct{}
}

func (s *poolSlot) updateMinReaderPosLocked() {
	if len(s.readerPositions) == 0 {
		s.minReaderPos = s.startByte + int64(len(s.data))
		return
	}
	first := true
	for _, pos := range s.readerPositions {
		if first || pos < s.minReaderPos {
			s.minReaderPos = pos
			first = false
		}
	}
}

// trimConsumedBufferLocked shrinks the sliding window toward targetBytes without
// discarding a byte that any active reader has not consumed. s.mu must be held.
func (s *poolSlot) trimConsumedBufferLocked(targetBytes int) int {
	if targetBytes < 0 || len(s.data) <= targetBytes {
		return 0
	}
	safeTrimTo := s.minReaderPos
	if atomic.LoadInt32(&s.readers) == 0 {
		safeTrimTo = s.startByte + int64(len(s.data))
	}
	maxTrim := int(safeTrimTo - s.startByte)
	if maxTrim <= 0 {
		return 0
	}
	trimAmount := len(s.data) - targetBytes
	if trimAmount > maxTrim {
		trimAmount = maxTrim
	}
	if trimAmount <= 0 {
		return 0
	}
	remaining := len(s.data) - trimAmount
	// Compact in place. Allocating and copying a fresh 24-32 MiB window on
	// every trim creates exactly the kind of transient memory/CPU pressure that
	// can feed back into player delivery while a migration is already active.
	copy(s.data[:remaining], s.data[trimAmount:])
	s.data = s.data[:remaining]
	s.startByte += int64(trimAmount)
	return trimAmount
}

func (s *poolSlot) registerReader(pos int64) uint64 {
	s.mu.Lock()
	readerID := s.registerReaderLocked(pos)
	s.mu.Unlock()
	return readerID
}

// registerReaderLocked pins a slot before the pool membership lock is released,
// preventing the reaper or capacity eviction from cancelling a slot between
// lookup and reader registration. s.mu must be held by the caller.
func (s *poolSlot) registerReaderLocked(pos int64) uint64 {
	s.nextReaderID++
	readerID := s.nextReaderID
	if s.readerPositions == nil {
		s.readerPositions = make(map[uint64]int64)
	}
	s.readerPositions[readerID] = pos
	s.updateMinReaderPosLocked()
	atomic.AddInt32(&s.readers, 1)
	s.notifyReaderChangedLocked()
	return readerID
}

func (s *poolSlot) updateReaderPosition(readerID uint64, pos int64) {
	s.mu.Lock()
	if _, ok := s.readerPositions[readerID]; ok {
		s.readerPositions[readerID] = pos
		s.updateMinReaderPosLocked()
	}
	s.mu.Unlock()
}

func (s *poolSlot) unregisterReader(readerID uint64) {
	s.mu.Lock()
	if _, ok := s.readerPositions[readerID]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.readerPositions, readerID)
	s.updateMinReaderPosLocked()
	atomic.AddInt32(&s.readers, -1)
	s.notifyReaderChangedLocked()
	s.mu.Unlock()
}

// notifyReaderChangedLocked wakes waiters without a gap between publishing the
// replacement channel and closing the old one. s.mu must be held by the caller.
func (s *poolSlot) notifyReaderChangedLocked() {
	if s.readerChanged == nil {
		s.readerChanged = make(chan struct{})
		return
	}
	old := s.readerChanged
	s.readerChanged = make(chan struct{})
	close(old)
}

// waitForActiveReader pauses upstream read-ahead while the slot is orphaned.
// Checking the count and capturing readerChanged under the same lock as
// registerReader prevents a reconnect from being lost between those actions.
func (s *poolSlot) waitForActiveReader() bool {
	for {
		s.mu.Lock()
		if atomic.LoadInt32(&s.readers) > 0 {
			s.mu.Unlock()
			return true
		}
		if s.readerChanged == nil {
			s.readerChanged = make(chan struct{})
		}
		changed := s.readerChanged
		s.mu.Unlock()

		select {
		case <-s.ctx.Done():
			return false
		case <-changed:
		}
	}
}

func (s *poolSlot) upstreamStallTimeout() time.Duration {
	if s.readStallTimeout > 0 {
		return s.readStallTimeout
	}
	return poolUpstreamStall
}

func newStreamPool(failures *streamFailureRegistry) *streamPool {
	p := &streamPool{
		creationGates: make(map[string]*poolCreationGate),
		files:         make(map[string][]*poolSlot),
		done:          make(chan struct{}),
		failures:      failures,
	}
	go p.reaper()
	return p
}

// lockCreation serializes misses only for the same media path. Waiting is
// request-cancellable, and a slow provider open for one file never blocks an
// unrelated playback from opening its own slot.
func (p *streamPool) lockCreation(ctx context.Context, path string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p.creationMu.Lock()
	gate := p.creationGates[path]
	if gate == nil {
		gate = &poolCreationGate{semaphore: make(chan struct{}, 1)}
		p.creationGates[path] = gate
	}
	gate.refs++
	p.creationMu.Unlock()

	releaseRef := func() {
		p.creationMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(p.creationGates, path)
		}
		p.creationMu.Unlock()
	}

	select {
	case gate.semaphore <- struct{}{}:
		return func() {
			<-gate.semaphore
			releaseRef()
		}, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
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
			startByte := s.startByte
			buffered := len(s.data)
			s.mu.Unlock()
			if idle {
				videoTracef("[stream-pool] evicting idle slot: path=%q startByte=%d buffered=%d",
					path, startByte, buffered)
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
	slot, readerID, err := p.acquire(r.Context(), path, reqStart, streamer)
	if err != nil {
		return false, nil
	}

	defer slot.unregisterReader(readerID)

	ctx := r.Context()
	requestStartedAt := time.Now()
	rangeHeader := r.Header.Get("Range")
	reqEnd := int64(-1)
	if start, end, ok := parseByteRange(rangeHeader); ok && start == reqStart {
		reqEnd = end
	}

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
	slotStart := slot.startByte
	ch := slot.signal
	slot.mu.Unlock()

	// If requested position is before the slot's buffer start, can't serve
	if reqStart < slotStart {
		videoTracef("[stream-pool] MISS: data trimmed past reqStart=%d slotStart=%d", reqStart, slotStart)
		return false, nil
	}

	// If data not yet available at requested position, wait for CDN reader.
	// Also wait for minimum pre-buffer to accumulate before serving, so the
	// client has enough data to start playing without immediately stalling
	// (especially important for high-bitrate 4K content over slower connections).
	buffered := endPos - reqStart
	preBufferTarget := initialPreBufferTarget(reqStart, reqEnd, totalSize)
	needsWait := reqStart >= endPos || buffered < preBufferTarget
	if needsWait {
		gap := reqStart - endPos
		if gap < 0 {
			gap = 0
		}
		waitStart := time.Now()
		videoTracef("[stream-pool] WAIT-START: path=%q range=%q reqStart=%d endPos=%d gap=%d slotStart=%d cdnDone=%v preBuffer=%d/%d slotTotalRead=%d slotAge=%v sinceLastRead=%v readers=%d",
			path, rangeHeader, reqStart, endPos, gap, slotStart, false, buffered, preBufferTarget,
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
				if reqStart < endPos && (buffered >= preBufferTarget || done) {
					videoTracef("[stream-pool] WAIT-OK: path=%q waited=%v gap=%d newEndPos=%d preBuffer=%d slotTotalRead=%d sinceLastRead=%v readers=%d",
						path, time.Since(waitStart).Round(time.Millisecond), gap, endPos, buffered, slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
					goto dataReady
				}
				if done && reqStart >= endPos {
					videoTracef("[stream-pool] CDN finished before reaching reqStart=%d endPos=%d", reqStart, endPos)
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
					videoTracef("[stream-pool] WAIT-PARTIAL: path=%q waited=%v preBuffer=%d/%d slotTotalRead=%d sinceLastRead=%v readers=%d (serving with partial buffer)",
						path, time.Since(waitStart).Round(time.Millisecond), buffered, preBufferTarget, slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
					endPos = currentEnd
					goto dataReady
				}
				videoTracef("[stream-pool] TIMEOUT waiting for data: path=%q reqStart=%d endPos=%d remaining=%d cdnDone=%v elapsed=%v slotTotalRead=%d sinceLastRead=%v readers=%d",
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

	responseEnd := totalSize - 1
	if reqEnd >= reqStart && (responseEnd < 0 || reqEnd < responseEnd) {
		responseEnd = reqEnd
	}
	if totalSize > 0 && reqStart < totalSize {
		if responseEnd >= totalSize {
			responseEnd = totalSize - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", reqStart, responseEnd, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(responseEnd-reqStart+1, 10))
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
	normalizeMediaContentType(w, fn, path)

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
	var upstreamWaitWatch pipelineBlockWatch
	stopUpstreamWaitWatch := monitorPipelineStarvation(
		ctx,
		&upstreamWaitWatch,
		pipelineStarvationTimeout,
		pipelineStarvationCheckInterval,
		func(blockedFor time.Duration) bool {
			marked := tracker.MarkPlaybackMigration(streamID, "backend-starvation")
			if marked {
				log.Printf("[stream-migration] stream-pool reader starved waiting for upstream data: path=%q blockedFor=%v streamID=%s",
					path, blockedFor.Round(time.Millisecond), streamID)
			}
			// Metadata-less probes cannot receive a playback signal, but should
			// still be reported at most once for this individual wait.
			return true
		},
	)
	defer stopUpstreamWaitWatch()

	// Stream data from slot buffer to client.
	// IMPORTANT: Write data immediately without checking ctx.Done() first.
	// The player sends rapid-fire requests and cancels old ones within milliseconds.
	// If we check ctx.Done() before writing, we lose the race and deliver 0 bytes.
	// Instead, we attempt the write directly — if the client is gone, Write() will
	// return an error, which is the only reliable signal.
	flusher, _ := w.(http.Flusher)
	pos := reqStart
	var totalWritten int64
	maxWritten := int64(-1)
	if reqEnd >= reqStart {
		maxWritten = reqEnd - reqStart + 1
	}

	videoTracef("[stream-pool] serving: path=%q range=%q reqStart=%d slotStart=%d buffered=%d slotTotalRead=%d readers=%d",
		path, rangeHeader, reqStart, slotStart, endPos-slotStart, slotTotalRead, atomic.LoadInt32(&slot.readers))

	const clientLogInterval = 5 * time.Second
	clientLogAt := time.Now()
	var clientWindowBytes int64
	var clientWindowWrite time.Duration

	for {
		if maxWritten >= 0 && totalWritten >= maxWritten {
			break
		}

		slot.mu.Lock()
		// Check if the buffer has been trimmed past our read position
		if pos < slot.startByte {
			trimmedStart := slot.startByte
			slot.mu.Unlock()
			videoTracef("[stream-pool] buffer trimmed past reader: path=%q pos=%d slotStart=%d", path, pos, trimmedStart)
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
			upstreamWaitWatch.begin()

			select {
			case <-ch:
				upstreamWaitWatch.end()
				if waited := time.Since(waitForDataStart); waited >= 200*time.Millisecond {
					slot.mu.Lock()
					currentEnd := slot.startByte + int64(len(slot.data))
					slotTotalRead = slot.totalRead
					slotLastReadAt = slot.lastReadAt
					slot.mu.Unlock()
					videoTracef("[stream-pool] READER-WAIT: path=%q pos=%d waited=%v currentEnd=%d slotTotalRead=%d sinceLastRead=%v readers=%d",
						path, pos, waited.Round(time.Millisecond), currentEnd, slotTotalRead, time.Since(slotLastReadAt).Round(time.Millisecond), atomic.LoadInt32(&slot.readers))
				}
				continue
			case <-ctx.Done():
				upstreamWaitWatch.end()
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
		if maxWritten >= 0 {
			remaining := maxWritten - totalWritten
			if remaining <= 0 {
				slot.mu.Unlock()
				break
			}
			if int64(n) > remaining {
				n = int(remaining)
			}
		}
		chunk := make([]byte, n)
		copy(chunk, slot.data[offset:offset+int64(n)])
		slot.lastAccess = time.Now()
		slot.mu.Unlock()

		// Write directly — don't check ctx.Done() beforehand.
		// The write itself is the fastest path to getting data to the client.
		// A blocked ResponseWriter write is client backpressure, not evidence that
		// the upstream stream is starved. Native players intentionally stop reading
		// while their local buffer is full, and a low-bitrate stream can take longer
		// than the generic starvation timeout to consume this 4 MiB chunk. Only an
		// upstream read stall combined with player-reported buffer pressure can arm
		// a migration.
		writeStart := time.Now()
		written, writeErr := w.Write(chunk)
		clientWindowWrite += time.Since(writeStart)
		if writeErr != nil {
			if isClientGone(writeErr) && totalWritten > 0 {
				// Only log if we actually sent some data (reduce noise)
				videoTracef("[stream-pool] client gone: path=%q pos=%d written=%d", path, pos, totalWritten)
			}
			return true, nil
		}

		pos += int64(written)
		totalWritten += int64(written)
		clientWindowBytes += int64(written)

		// Update this reader independently so a fast parallel range request
		// cannot move the trim boundary past a slower reader.
		slot.updateReaderPosition(readerID, pos)

		if bytesCounter != nil {
			atomic.StoreInt64(bytesCounter, totalWritten)
		}
		if activityCounter != nil {
			atomic.StoreInt64(activityCounter, time.Now().UnixNano())
		}

		if flusher != nil {
			flusher.Flush()
		}

		// Periodically log the client-write (delivery) leg throughput. Compared
		// against the CDN-read leg, this isolates whether a stall is the source
		// (debrid/CDN) or the client transport (e.g. a remote iroh tunnel).
		if now := time.Now(); now.Sub(clientLogAt) >= clientLogInterval && clientWindowBytes > 0 {
			logStreamThroughput("client-write", path, clientWindowBytes, clientWindowWrite, now.Sub(clientLogAt))
			clientLogAt = now
			clientWindowBytes = 0
			clientWindowWrite = 0
		}
	}

	elapsed := time.Since(requestStartedAt)
	rateMBps := 0.0
	if elapsed > 0 {
		rateMBps = (float64(totalWritten) / 1024.0 / 1024.0) / elapsed.Seconds()
	}
	videoTracef("[stream-pool] stream complete: path=%q written=%d elapsed=%v avgRate=%.2fMBps", path, totalWritten, elapsed.Round(time.Millisecond), rateMBps)
	return true, nil
}

// logStreamThroughput logs throughput for one leg of the stream-pool pipeline.
// activeDur is the time actually spent inside the I/O call (CDN read or client
// write); comparing it against the wall-clock window isolates each leg's true
// rate from time spent blocked on the other leg — a CDN reader backpressured by
// a slow client, or a client reader starved waiting on a slow CDN. Pairing the
// "CDN-read" and "client-write" lines for a file pinpoints the bottleneck:
//   - source slow  -> CDN-read wall rate low, high busy%; client-write starved (low busy%)
//   - client slow  -> client-write wall rate low, high busy%; CDN-read backpressured (low busy%)
func logStreamThroughput(leg, path string, bytes int64, activeDur, wallDur time.Duration) {
	name := path
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 && idx+1 < len(name) {
		name = name[idx+1:]
	}
	mb := float64(bytes) / 1e6
	wallSecs := wallDur.Seconds()
	activeSecs := activeDur.Seconds()
	var wallRate, activeRate, busy float64
	if wallSecs > 0 {
		wallRate = mb / wallSecs
		busy = 100 * activeSecs / wallSecs
	}
	if activeSecs > 0 {
		activeRate = mb / activeSecs
	}
	log.Printf("[stream-pool] %s throughput: file=%q wall=%.1fMbps (%.2fMB/s) active=%.1fMbps (%.2fMB/s) busy=%.0f%% bytes=%d window=%.1fs",
		leg, name, wallRate*8, wallRate, activeRate*8, activeRate, busy, bytes, wallSecs)
}

// findSlotLocked returns an existing slot and optionally registers a reader
// before the caller releases p.mu. The caller must hold p.mu for reading or
// writing; pool membership then cannot change during acquisition.
func (p *streamPool) findSlotLocked(path string, reqPos int64, acquire bool) (*poolSlot, uint64) {
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
			var readerID uint64
			if acquire {
				readerID = s.registerReaderLocked(reqPos)
			}
			s.mu.Unlock()
			return s, readerID
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

	if best == nil {
		return nil, 0
	}

	// The background reader can advance or trim while candidates are scanned.
	// Revalidate the selected slot under its lock before pinning it.
	best.mu.Lock()
	endPos := best.startByte + int64(len(best.data))
	dist := reqPos - endPos
	eligible := !(best.cdnDone && best.cdnErr != nil) && reqPos >= best.startByte &&
		(reqPos < endPos || (!best.cdnDone && dist >= 0 && dist < poolMaxWaitAhead))
	if !eligible {
		best.mu.Unlock()
		return nil, 0
	}
	var readerID uint64
	if acquire {
		readerID = best.registerReaderLocked(reqPos)
	}
	best.mu.Unlock()
	return best, readerID
}

// findSlot returns an unpinned slot for diagnostics and tests. Serving code must
// use acquire so the returned slot cannot be evicted before registration.
func (p *streamPool) findSlot(path string, reqPos int64) *poolSlot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	slot, _ := p.findSlotLocked(path, reqPos, false)
	return slot
}

func logReusedPoolSlot(path string, reqPos int64, slot *poolSlot) {
	if slot == nil {
		return
	}
	slot.mu.Lock()
	buffered := int64(len(slot.data))
	endPos := slot.startByte + buffered
	startByte := slot.startByte
	cdnDone := slot.cdnDone
	slot.mu.Unlock()
	gap := reqPos - endPos
	if gap < 0 {
		gap = 0
	}
	videoTracef("[stream-pool] REUSE slot: path=%q reqPos=%d slotStart=%d buffered=%d endPos=%d gap=%d cdnDone=%v",
		path, reqPos, startByte, buffered, endPos, gap, cdnDone)
}

// getOrCreateInternal finds or creates a slot. When acquire is true, it pins a
// reader while p.mu still protects pool membership and returns its reader ID.
func (p *streamPool) getOrCreateInternal(waitCtx context.Context, path string, reqPos int64, streamer streaming.Provider, acquire bool) (*poolSlot, uint64, error) {
	p.mu.RLock()
	slot, readerID := p.findSlotLocked(path, reqPos, acquire)
	p.mu.RUnlock()
	if slot != nil {
		logReusedPoolSlot(path, reqPos, slot)
		return slot, readerID, nil
	}

	// Only one miss for this media path opens an upstream connection at a time.
	// Recheck after taking the keyed gate so identical concurrent seeks coalesce
	// before any network work, while unrelated playbacks remain independent.
	unlockCreation, err := p.lockCreation(waitCtx, path)
	if err != nil {
		return nil, 0, err
	}
	defer unlockCreation()
	p.mu.RLock()
	slot, readerID = p.findSlotLocked(path, reqPos, acquire)
	if slot == nil && len(p.files[path]) >= poolMaxSlotsPerFile {
		allActive := true
		for _, existing := range p.files[path] {
			existing.mu.Lock()
			active := atomic.LoadInt32(&existing.readers) > 0
			existing.mu.Unlock()
			if !active {
				allActive = false
				break
			}
		}
		if allActive {
			p.mu.RUnlock()
			return nil, 0, fmt.Errorf("stream pool at capacity for %q: all %d slots have active readers", path, poolMaxSlotsPerFile)
		}
	}
	p.mu.RUnlock()
	if slot != nil {
		logReusedPoolSlot(path, reqPos, slot)
		return slot, readerID, nil
	}
	if err := waitCtx.Err(); err != nil {
		return nil, 0, err
	}

	// Create a new slot — start a fresh CDN connection at reqPos.
	// The provider is opened outside p.mu; membership is rechecked after the open
	// so a concurrent acquisition can win without creating a duplicate slot.
	ctx, cancel := context.WithCancel(context.Background())
	rangeHeader := fmt.Sprintf("bytes=%d-", reqPos)

	resp, err := streamer.Stream(ctx, streaming.Request{
		Path:        path,
		RangeHeader: rangeHeader,
		Method:      http.MethodGet,
	})
	if err != nil {
		cancel()
		return nil, 0, err
	}

	// Parse total file size from Content-Range header
	var totalSize int64
	contentRange := resp.Headers.Get("Content-Range")
	if contentRange != "" {
		totalSize = parseTotalSizeFromContentRange(contentRange)
	}

	// Trust the offset the upstream actually served, never the one we asked for.
	// The debrid layer can legitimately shift a window (RAR offset rewriting,
	// parallel-fetch realignment) and a provider may ignore Range entirely. Filing
	// the body under reqPos regardless leaves every reader of this slot reading
	// shifted data while the response advertises the requested range — the client
	// sees a corrupt container, which is exactly how this surfaced on a renderer.
	servedStart := reqPos
	if contentRange != "" {
		if parsed, ok := parseContentRangeStart(contentRange); ok {
			servedStart = parsed
		}
	} else if resp.Status == http.StatusOK {
		// A 200 for "bytes=N-" means the range was ignored: the body starts at 0.
		servedStart = 0
	}
	if servedStart > reqPos {
		// The buffer would begin past what this reader needs, so it can never be
		// satisfied from this slot. Refuse to pool and let the caller fall back to
		// a direct stream rather than serve bytes from the wrong place.
		log.Printf("[stream-pool] refusing slot: upstream served bytes from %d but %d was requested path=%q range=%q",
			servedStart, reqPos, path, contentRange)
		cancel()
		_ = resp.Close()
		return nil, 0, fmt.Errorf("upstream served offset %d for requested %d", servedStart, reqPos)
	}

	contentType := resp.Headers.Get("Content-Type")
	if contentType == "" {
		contentType = "video/mp4"
	}

	slot = &poolSlot{
		path:          path,
		startByte:     servedStart,
		data:          make([]byte, 0, 1024*1024), // start 1MB, grows as needed
		ctx:           ctx,
		cancel:        cancel,
		failures:      p.failures,
		streamer:      streamer,
		totalSize:     totalSize,
		filename:      resp.Filename,
		respStatus:    resp.Status,
		contentType:   contentType,
		lastAccess:    time.Now(),
		lastReadAt:    time.Now(),
		readStartedAt: time.Now(),
		signal:        make(chan struct{}),
		readerChanged: make(chan struct{}),
	}

	// Register slot, evicting the least recently used idle slot if at capacity.
	p.mu.Lock()
	if p.files == nil {
		// Pool was closed while the upstream connection was being opened.
		p.mu.Unlock()
		cancel()
		_ = resp.Close()
		return nil, 0, fmt.Errorf("stream pool closed")
	}
	if existing, existingReaderID := p.findSlotLocked(path, reqPos, acquire); existing != nil {
		p.mu.Unlock()
		cancel()
		_ = resp.Close()
		logReusedPoolSlot(path, reqPos, existing)
		return existing, existingReaderID, nil
	}
	slots := p.files[path]
	if len(slots) >= poolMaxSlotsPerFile {
		lruIdx := -1
		lruTime := time.Now()
		for i, existing := range slots {
			existing.mu.Lock()
			la := existing.lastAccess
			readers := atomic.LoadInt32(&existing.readers)
			existing.mu.Unlock()
			if readers == 0 && la.Before(lruTime) {
				lruTime = la
				lruIdx = i
			}
		}
		if lruIdx < 0 {
			p.mu.Unlock()
			cancel()
			_ = resp.Close()
			return nil, 0, fmt.Errorf("stream pool at capacity for %q: all %d slots have active readers", path, poolMaxSlotsPerFile)
		}
		evicted := slots[lruIdx]
		evicted.mu.Lock()
		evictedStart := evicted.startByte
		evicted.mu.Unlock()
		videoTracef("[stream-pool] evicting LRU slot: startByte=%d newPos=%d", evictedStart, reqPos)
		evicted.cancel()
		slots = append(slots[:lruIdx], slots[lruIdx+1:]...)
	}
	if acquire {
		slot.mu.Lock()
		readerID = slot.registerReaderLocked(reqPos)
		slot.mu.Unlock()
	}
	p.files[path] = append(slots, slot)
	p.mu.Unlock()

	go slot.backgroundReader(resp)

	videoTracef("[stream-pool] NEW slot: path=%q startByte=%d totalSize=%d contentType=%q status=%d", path, reqPos, totalSize, contentType, resp.Status)
	return slot, readerID, nil
}

// acquire returns a slot already pinned by a reader. Reaper/capacity eviction
// cannot invalidate it between lookup and the first byte served.
func (p *streamPool) acquire(ctx context.Context, path string, reqPos int64, streamer streaming.Provider) (*poolSlot, uint64, error) {
	return p.getOrCreateInternal(ctx, path, reqPos, streamer, true)
}

// getOrCreate is retained for pool lifecycle tests and diagnostics.
func (p *streamPool) getOrCreate(path string, reqPos int64, streamer streaming.Provider) (*poolSlot, error) {
	slot, _, err := p.getOrCreateInternal(context.Background(), path, reqPos, streamer, false)
	return slot, err
}

// backgroundReader reads from the CDN response body into the slot's buffer.
// The connection survives client disconnects, but upstream read-ahead pauses
// until another reader registers so orphaned migration candidates do not keep
// consuming bandwidth.
func (s *poolSlot) backgroundReader(resp *streaming.Response) {
	currentResp := resp
	defer func() {
		if currentResp != nil {
			_ = currentResp.Close()
		}
		s.mu.Lock()
		s.cdnDone = true
		s.mu.Unlock()
		s.broadcast()
	}()

	buf := make([]byte, poolSlotReadChunk)
	var upstreamWatch pipelineBlockWatch
	stopStarvationWatch := monitorPipelineStarvation(
		s.ctx,
		&upstreamWatch,
		pipelineStarvationTimeout,
		pipelineStarvationCheckInterval,
		func(blockedFor time.Duration) bool {
			if atomic.LoadInt32(&s.readers) == 0 {
				return false
			}
			// Keep this shared slot watch as transport diagnostics only. A single
			// range can stall while another is healthy, so the request-level watch
			// in serve owns the source-scoped migration signal only when that exact
			// player reader has caught up and is waiting for upstream data.
			log.Printf("[stream-health] upstream read blocked in stream pool: path=%q blockedFor=%v readers=%d",
				s.path, blockedFor.Round(time.Millisecond), atomic.LoadInt32(&s.readers))
			return true
		},
	)
	defer stopStarvationWatch()
	const cdnLogInterval = 5 * time.Second
	cdnLogAt := time.Now()
	var cdnWindowBytes int64
	var cdnWindowRead time.Duration
	consecutiveNoProgressReconnects := 0
	for {
		// A disconnect can race with an in-flight Body.Read, so at most that
		// single chunk is retained before the next iteration parks here. The
		// dedicated readerChanged channel wakes this connection on reconnect.
		if !s.waitForActiveReader() {
			return
		}

		// Backpressure: if buffer is at the hard limit, trim data that
		// readers have already consumed. If readers are active, only trim
		// up to minReaderPos to avoid invalidating their read position.
		// If no room can be freed, sleep briefly to apply backpressure
		// to the CDN download (TCP window will close naturally).
		s.mu.Lock()
		if len(s.data) >= poolSlotBufferHard {
			oldStart := s.startByte
			if trimmed := s.trimConsumedBufferLocked(poolSlotBufferTrim); trimmed > 0 {
				videoTracef("[stream-pool] backpressure trim: path=%q oldStart=%d newStart=%d trimmed=%d readers=%d minReaderPos=%d",
					s.path, oldStart, s.startByte, trimmed, atomic.LoadInt32(&s.readers), s.minReaderPos)
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

		activeResp := currentResp
		if activeResp == nil || activeResp.Body == nil {
			s.mu.Lock()
			s.cdnErr = fmt.Errorf("stream pool upstream response body unavailable")
			s.mu.Unlock()
			return
		}

		// A successful HTTP response can still become half-open while its body is
		// being consumed. net/http's ResponseHeaderTimeout no longer applies at
		// this point, and the streaming client deliberately has no whole-request
		// timeout. Close only this response body after a sustained read stall; the
		// reader below then reopens the same source at the exact next byte.
		var reconnectTriggered atomic.Bool
		stallTimer := time.AfterFunc(s.upstreamStallTimeout(), func() {
			reconnectTriggered.Store(true)
			log.Printf("[stream-pool] upstream body read timed out; closing for same-source reconnect: path=%q timeout=%v readers=%d",
				s.path, s.upstreamStallTimeout(), atomic.LoadInt32(&s.readers))
			_ = activeResp.Close()
		})

		upstreamWatch.begin()
		readStart := time.Now()
		n, err := activeResp.Body.Read(buf)
		upstreamWatch.end()
		stallTimer.Stop()
		cdnWindowRead += time.Since(readStart)
		if n > 0 {
			cdnWindowBytes += int64(n)
			s.mu.Lock()
			s.data = append(s.data, buf[:n]...)
			s.totalRead += int64(n)
			s.lastReadAt = time.Now()
			// Keep the normal window near 32 MiB even with active readers, now
			// that their positions are tracked independently. If a slow reader
			// pins old data, the hard limit still applies backpressure above.
			if len(s.data) > poolSlotBufferMax {
				s.trimConsumedBufferLocked(poolSlotBufferTrim)
			}
			s.mu.Unlock()
			s.broadcast()
		}
		// Periodically log the CDN-read (source) leg throughput so a slow
		// debrid/CDN source can be told apart from a slow client at a glance.
		if now := time.Now(); now.Sub(cdnLogAt) >= cdnLogInterval && cdnWindowBytes > 0 {
			logStreamThroughput("CDN-read", s.path, cdnWindowBytes, cdnWindowRead, now.Sub(cdnLogAt))
			cdnLogAt = now
			cdnWindowBytes = 0
			cdnWindowRead = 0
		}

		if reconnectTriggered.Load() {
			_ = activeResp.Close()
			currentResp = nil
			if n == 0 {
				consecutiveNoProgressReconnects++
			} else {
				consecutiveNoProgressReconnects = 0
			}
			if consecutiveNoProgressReconnects > poolMaxReconnects {
				err = fmt.Errorf("upstream body stalled after %d same-source reconnects", poolMaxReconnects)
				s.mu.Lock()
				s.cdnErr = err
				s.mu.Unlock()
				log.Printf("[stream-pool] same-source reconnect exhausted: path=%q err=%v", s.path, err)
				if s.failures != nil {
					if record, confirmed := s.failures.recordRecognizedFailure(s.path, err); confirmed {
						reportConfirmedStreamFailure(s.path, record)
					}
				}
				return
			}

			if !s.waitForActiveReader() {
				return
			}
			s.mu.Lock()
			resumeAt := s.startByte + int64(len(s.data))
			s.mu.Unlock()
			if s.streamer == nil {
				err = fmt.Errorf("same-source reconnect unavailable at byte %d", resumeAt)
				s.mu.Lock()
				s.cdnErr = err
				s.mu.Unlock()
				return
			}

			replacement, openErr := s.streamer.Stream(s.ctx, streaming.Request{
				Path:        s.path,
				RangeHeader: fmt.Sprintf("bytes=%d-", resumeAt),
				Method:      http.MethodGet,
			})
			if openErr != nil {
				err = fmt.Errorf("same-source reconnect at byte %d: %w", resumeAt, openErr)
				s.mu.Lock()
				s.cdnErr = err
				s.mu.Unlock()
				log.Printf("[stream-pool] same-source reconnect failed: path=%q resumeAt=%d err=%v", s.path, resumeAt, openErr)
				if s.failures != nil {
					if record, confirmed := s.failures.recordRecognizedFailure(s.path, err); confirmed {
						reportConfirmedStreamFailure(s.path, record)
					}
				}
				return
			}

			servedStart := resumeAt
			if contentRange := replacement.Headers.Get("Content-Range"); contentRange != "" {
				if parsed, ok := parseContentRangeStart(contentRange); ok {
					servedStart = parsed
				}
			} else if replacement.Status == http.StatusOK {
				servedStart = 0
			}
			if servedStart != resumeAt {
				_ = replacement.Close()
				err = fmt.Errorf("same-source reconnect requested byte %d but upstream served %d", resumeAt, servedStart)
				s.mu.Lock()
				s.cdnErr = err
				s.mu.Unlock()
				log.Printf("[stream-pool] same-source reconnect rejected: path=%q err=%v", s.path, err)
				return
			}

			currentResp = replacement
			log.Printf("[stream-pool] same-source reconnect opened: path=%q resumeAt=%d status=%d",
				s.path, resumeAt, replacement.Status)
			continue
		}

		if err != nil {
			if err != io.EOF {
				s.mu.Lock()
				s.cdnErr = err
				s.mu.Unlock()
				log.Printf("[stream-pool] CDN read error: path=%q err=%v", s.path, err)
				if s.failures != nil {
					if record, confirmed := s.failures.recordRecognizedFailure(s.path, err); confirmed {
						log.Printf("[stream-migration] confirmed recoverable stream failure in stream pool path=%q err=%v", s.path, err)
						reportConfirmedStreamFailure(s.path, record)
					}
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

// parseContentRangeStart reports the first byte offset an upstream actually
// served, from a "bytes START-END/TOTAL" header. The pool must confirm this
// matches the window it asked for: filing a body that starts somewhere else
// under the requested offset corrupts every later read of that slot.
func parseContentRangeStart(value string) (int64, bool) {
	spec := strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(spec), "bytes ") {
		return 0, false
	}
	spec = strings.TrimSpace(spec[len("bytes "):])
	if slash := strings.IndexByte(spec, '/'); slash >= 0 {
		spec = spec[:slash]
	}
	dash := strings.IndexByte(spec, '-')
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(spec[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

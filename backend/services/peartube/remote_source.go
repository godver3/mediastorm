package peartube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/services/streaming"
)

// remoteSourceIdentityDomain separates the remote byte-identity hash from every
// other digest this package derives.
const remoteSourceIdentityDomain = "peartube.mediastorm.remote-source.v1"

// maxRemoteStreamPathBytes bounds the stable name a remote grant is keyed by.
const maxRemoteStreamPathBytes = 1024

// RemoteRangeReader is the streaming layer's side of a granted remote source.
//
// debrid.CompositeProvider satisfies it, which is the whole point: the address
// behind a debrid path expires in minutes, and Stream is the call that already
// re-resolves it — a lapsed cached URL, a torrent that was deleted and has to be
// re-added, an address that stopped answering. Serving a grant through Stream
// therefore inherits playback's re-resolution instead of reimplementing it, and
// a range asked for an hour after the grant was issued is resolved fresh.
type RemoteRangeReader interface {
	Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error)
}

// RemoteSource names a title the companion may pull by range without ever
// seeing the upstream address.
type RemoteSource struct {
	Reader RemoteRangeReader
	// StreamPath is the stable MediaStorm-internal name for the media, such as
	// /debrid/<provider>/<torrent>/<file>. It is a name, not an address: it
	// carries no credential and no expiry, and re-resolution happens underneath
	// it rather than to it.
	StreamPath string
}

// RemoteSourceETag is the byte identity a companion resumes against.
//
// It is derived from the stable stream path and the authoritative total length,
// and from nothing else. The resolved CDN address, its token, and the time it
// was resolved are deliberately excluded, so re-resolving the same content
// cannot change the tag: an interrupted transfer resumed an hour later sees the
// identical ETag and splices safely onto its own confirmed offset. Different
// content means a different file, torrent, or size, and therefore a different
// tag. The one asymmetry is in the safe direction: the same content re-added
// upstream under a new torrent id yields a new path and so a new tag, which
// costs a re-download but can never splice two sources together.
func RemoteSourceETag(streamPath string, length int64) string {
	digest := sha256.Sum256([]byte(
		remoteSourceIdentityDomain + "\x00" + streamPath + "\x00" + strconv.FormatInt(length, 10)))
	return `"remote-sha256-` + hex.EncodeToString(digest[:]) + `"`
}

// PrepareRemote pins one range-readable remote source and its authoritative
// total length for a later grant.
//
// The length is probed here, once, so it is known before the companion is told
// about the job: a one-byte range whose Content-Range names the total. A source
// that cannot state its total is refused outright rather than granted with an
// unknown length, because the companion derives its block plan and its
// resume-offset validation from that number.
func (r *SourceGrantRegistry) PrepareRemote(ctx context.Context, source RemoteSource, contentType string) (*PreparedSource, error) {
	if err := r.prepareGuard(); err != nil {
		return nil, err
	}
	if source.Reader == nil {
		return nil, errors.New("remote source has no streaming provider")
	}
	streamPath := strings.TrimSpace(source.StreamPath)
	if streamPath == "" || len(streamPath) > maxRemoteStreamPathBytes || !validHeaderText(streamPath) {
		return nil, errors.New("remote source stream path is invalid")
	}
	contentType, err := normalizeSourceContentType(contentType)
	if err != nil {
		return nil, err
	}
	length, err := probeRemoteSourceLength(ctx, source.Reader, streamPath)
	if err != nil {
		return nil, err
	}
	return &PreparedSource{
		backing: newRemoteBacking(source.Reader, streamPath, length),
		length:  length,
		// A whole-file SHA-256 is deliberately absent: computing one would mean
		// downloading the entire title through this process, which is exactly
		// what a granted remote source exists to avoid. The ETag plus the total
		// length carry byte identity, and the companion verifies the bytes it
		// receives against its own merkle tree.
		etag:        RemoteSourceETag(streamPath, length),
		contentType: contentType,
	}, nil
}

// probeRemoteSourceLength asks the streaming layer for one byte and reads the
// total out of the Content-Range it answers with.
func probeRemoteSourceLength(ctx context.Context, reader RemoteRangeReader, streamPath string) (int64, error) {
	response, err := reader.Stream(ctx, streaming.Request{
		Path:        streamPath,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-0",
	})
	if err != nil {
		return 0, fmt.Errorf("probe remote source length: %w", err)
	}
	defer response.Close()
	if response.Status != http.StatusPartialContent {
		return 0, errors.New("remote source does not serve byte ranges")
	}
	_, _, total, ok := parseSourceContentRange(response.Headers.Get("Content-Range"))
	if !ok || total <= 0 {
		return 0, errors.New("remote source did not report a total length")
	}
	return total, nil
}

// remoteSessionIdleTimeout bounds how long a held upstream stream may sit unread
// before it is closed. A companion pulling a file front to back comes back for
// the next range within milliseconds; one that has gone quiet for a minute has
// stalled, and the range it eventually asks for can afford a cold open. Without
// this bound a companion that simply stopped asking would pin a debrid
// connection until the grant expired.
const remoteSessionIdleTimeout = time.Minute

// remoteSessionWindowBytes caps how far ahead one held stream reaches.
//
// It is vastly larger than a range, so reuse still carries a whole title in a
// handful of opens rather than hundreds: a 4.2 GiB episode in 16 MiB ranges
// costs nine opens instead of two hundred and sixty-nine. What the cap buys is
// that a companion which stops mid-file wastes at most one window of CDN
// transfer rather than the rest of the title, that the total the upstream
// reports is re-checked once per window instead of once per grant, and that no
// single upstream connection has to survive multiple gigabytes — which is what
// an expiring debrid address breaks first.
const remoteSessionWindowBytes = 512 * 1024 * 1024

// remoteBacking serves grant ranges through the streaming layer.
//
// It pins no address: every real open goes back through Stream, which is what
// lets a grant outlive the playback that discovered the title and survive an
// address that expired minutes ago. What it does hold, between ranges, is the
// single upstream stream it opened most recently, parked at the offset it has
// been read up to. A companion pulling a file front to back asks for the range
// that begins exactly there, so that stream is read further instead of being
// replaced.
//
// That is the difference between archiving and crawling. Establishing a stream
// costs an unrestrict call, a TLS handshake and a first-byte wait — measured at
// roughly eight seconds, against the 0.16s the same connection needs to move
// 4 MiB. Re-establishing per range therefore spent 98% of the transfer on setup
// and moved 0.48 MB/s, while playback holding one stream open moved 96 MB/s.
type remoteBacking struct {
	reader RemoteRangeReader
	path   string
	length int64

	idleTimeout time.Duration

	// streamCtx scopes held streams. A held stream outlives the request that
	// opened it, so it cannot borrow that request's context: the next range
	// would find the stream cancelled the instant the previous response
	// finished. close cancels it, which is also what unblocks a read still in
	// flight when a grant is revoked underneath it.
	streamCtx context.Context
	cancel    context.CancelFunc

	mu      sync.Mutex
	session *remoteSession
	closed  bool
}

// remoteSession is one upstream stream held open across contiguous ranges.
//
// Ownership follows inUse, which is guarded by remoteBacking.mu: a parked
// session belongs to the backing, a checked-out one belongs to the heldRange
// reading from it. Exactly one of the two closes it, so no response is ever
// closed twice and none is dropped.
type remoteSession struct {
	response *streaming.Response
	// position is the offset of the next byte this stream will yield. A range
	// starting exactly here is a continuation of it.
	position int64
	// limit is one past the last byte this stream can yield. Reading a
	// continuation past it would return EOF inside a window the grant promised
	// bytes for, which reaches the companion as a truncated 206.
	limit int64
	inUse bool
	idle  *time.Timer
}

func newRemoteBacking(reader RemoteRangeReader, path string, length int64) *remoteBacking {
	// Deliberately not derived from the caller's context: the streams this
	// backing holds belong to the grant's lifetime, not to whatever request
	// happened to prepare it.
	streamCtx, cancel := context.WithCancel(context.Background())
	return &remoteBacking{
		reader:      reader,
		path:        path,
		length:      length,
		idleTimeout: remoteSessionIdleTimeout,
		streamCtx:   streamCtx,
		cancel:      cancel,
	}
}

// verify has nothing cheap to check about a remote identity: re-establishing one
// means talking to the upstream, and doing that on a HEAD or DELETE would block
// the callback on a provider. The identity guard lives in open. All this can say
// is whether the backing is still open at all, which matches fileBacking losing
// its descriptor.
func (b *remoteBacking) verify() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errSourceDrift
	}
	return nil
}

func (b *remoteBacking) open(ctx context.Context, start, count int64) (io.ReadCloser, error) {
	held, busy, stale, err := b.checkout(start, count)
	if stale != nil {
		// A held stream that does not begin where this range does is worthless:
		// a resume starting mid-file, or a companion that jumped backwards. Drop
		// it here rather than leaving a debrid connection open for bytes nobody
		// is going to ask for.
		_ = stale.Close()
	}
	if err != nil {
		return nil, err
	}
	if held != nil {
		return held, nil
	}
	if busy {
		// Another range for this same grant is mid-flight on the held stream.
		// Waiting for it would block this request for as long as the companion
		// takes to drain that response, so this range gets its own one-shot
		// stream: exactly the pre-reuse path, correct but without the saving.
		response, streamErr := b.stream(ctx, start, start+count-1)
		if streamErr != nil {
			return nil, streamErr
		}
		return &boundedSourceBody{reader: io.LimitReader(response.Body, count), closer: response}, nil
	}
	return b.openSession(ctx, start, count)
}

// checkout hands out the held stream when this range continues it. It also
// reports whether that stream is busy serving another range, and returns any
// stream it detached so the caller can close it outside the lock.
func (b *remoteBacking) checkout(start, count int64) (io.ReadCloser, bool, *streaming.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false, nil, errSourceDrift
	}
	session := b.session
	switch {
	case session == nil:
		return nil, false, nil, nil
	case session.inUse:
		return nil, true, nil, nil
	case session.position != start || start+count > session.limit:
		b.session = nil
		session.stopIdleLocked()
		return nil, false, session.response, nil
	}
	session.stopIdleLocked()
	session.inUse = true
	return &heldRange{
		backing:   b,
		session:   session,
		reader:    io.LimitReader(session.response.Body, count),
		count:     count,
		remaining: count,
	}, false, nil, nil
}

// sessionEnd is the last byte a stream opened at start will be asked for.
//
// It never covers the whole entity. An upstream may answer a range that happens
// to span everything with a plain 200 and no Content-Range at all, which leaves
// this grant's identity guard nothing to read and stalls the transfer as
// unavailable — and the first range of a fresh archive is exactly that request.
// Keeping every request a genuine sub-range keeps the 206 unambiguous. It costs
// one extra open for the final byte of a file smaller than one window.
func (b *remoteBacking) sessionEnd(start int64) int64 {
	end := b.length - 1
	if window := start + remoteSessionWindowBytes - 1; window < end {
		end = window
	}
	if start == 0 && end == b.length-1 && end > 0 {
		end--
	}
	return end
}

// openSession establishes a stream that runs from start to the end of its window
// and parks it for the ranges that follow.
func (b *remoteBacking) openSession(ctx context.Context, start, count int64) (io.ReadCloser, error) {
	type opened struct {
		response *streaming.Response
		err      error
	}
	// Establishment is allowed to outlive this request. A companion whose client
	// timeout is shorter than a slow unrestrict-plus-handshake would otherwise
	// make its own retry pay for the same setup again; parking the stream when it
	// finally arrives means the retry finds it warm, and the idle timeout reaps
	// it if the retry never comes.
	results := make(chan opened, 1)
	limit := b.sessionEnd(start) + 1
	go func() {
		response, err := b.stream(b.streamCtx, start, limit-1)
		results <- opened{response: response, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			return nil, result.err
		}
		log.Printf("[peartube] granted source %s opened an upstream session for %d-%d of %d",
			b.path, start, limit-1, b.length)
		if reader, parked := b.park(result.response, start, limit, count); parked {
			return reader, nil
		}
		// A concurrent range parked its own stream first. Serve this range
		// straight off this one and let it close with the response.
		return &boundedSourceBody{reader: io.LimitReader(result.response.Body, count), closer: result.response}, nil
	case <-ctx.Done():
		go func() {
			result := <-results
			if result.err != nil {
				return
			}
			if _, parked := b.park(result.response, start, limit, 0); !parked {
				_ = result.response.Close()
			}
		}()
		return nil, fmt.Errorf("%w: %v", ErrSourceUnavailable, ctx.Err())
	}
}

// park installs a freshly established stream as the held session, checking it
// out for the range that established it when count is positive. It reports false
// when the backing already holds a stream or has been closed, in which case the
// caller still owns the response.
func (b *remoteBacking) park(response *streaming.Response, start, limit, count int64) (io.ReadCloser, bool) {
	session := &remoteSession{response: response, position: start, limit: limit}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.session != nil {
		return nil, false
	}
	b.session = session
	if count <= 0 {
		b.armIdleLocked(session)
		return nil, true
	}
	session.inUse = true
	return &heldRange{
		backing:   b,
		session:   session,
		reader:    io.LimitReader(response.Body, count),
		count:     count,
		remaining: count,
	}, true
}

// release takes a checked-out stream back. A range drained to its exact last
// byte leaves the stream splice-ready at start+count, so it is parked for the
// next one. Anything else — a short read, a transport fault, a companion that
// hung up mid-response — leaves it at an offset nothing can be spliced onto, so
// it is closed instead.
func (b *remoteBacking) release(session *remoteSession, consumed int64, drained bool) {
	b.mu.Lock()
	session.inUse = false
	keep := drained && !b.closed && b.session == session
	if keep {
		session.position += consumed
		// A stream read to the end of its window has nothing left to serve.
		keep = session.position < session.limit
	}
	if !keep {
		if b.session == session {
			b.session = nil
		}
		b.mu.Unlock()
		_ = session.response.Close()
		return
	}
	b.armIdleLocked(session)
	b.mu.Unlock()
}

func (b *remoteBacking) armIdleLocked(session *remoteSession) {
	if b.idleTimeout <= 0 {
		return
	}
	session.idle = time.AfterFunc(b.idleTimeout, func() { b.expire(session) })
}

func (session *remoteSession) stopIdleLocked() {
	if session.idle != nil {
		session.idle.Stop()
		session.idle = nil
	}
}

// expire drops a parked stream that nobody came back for. It re-checks under the
// lock, so a timer that fired while a range was being checked out finds the
// session in use and leaves it alone.
func (b *remoteBacking) expire(session *remoteSession) {
	b.mu.Lock()
	if b.session != session || session.inUse {
		b.mu.Unlock()
		return
	}
	b.session = nil
	b.mu.Unlock()
	_ = session.response.Close()
}

// stream establishes one upstream stream for the inclusive window [start, end]
// and re-checks that the upstream is still describing the bytes this grant
// promised. Every real open pays for that check.
func (b *remoteBacking) stream(ctx context.Context, start, end int64) (*streaming.Response, error) {
	response, err := b.reader.Stream(ctx, streaming.Request{
		Path:        b.path,
		Method:      http.MethodGet,
		RangeHeader: fmt.Sprintf("bytes=%d-%d", start, end),
	})
	if err != nil {
		// Every failure the streaming layer surfaces here is one a later attempt
		// can recover from, including a stale torrent: re-resolution is its own
		// job and this range is simply not available yet.
		return nil, fmt.Errorf("%w: %v", ErrSourceUnavailable, err)
	}
	if response.Status != http.StatusPartialContent {
		response.Close()
		return nil, ErrSourceUnavailable
	}
	servedStart, servedEnd, total, ok := parseSourceContentRange(response.Headers.Get("Content-Range"))
	if !ok {
		response.Close()
		return nil, ErrSourceUnavailable
	}
	if total != b.length {
		// The upstream is no longer describing the same bytes. Serving them
		// would splice a different source into the companion's transfer, so this
		// is terminal rather than retryable.
		//
		// Say which numbers disagreed. This surfaces to the relay as an opaque
		// 410 and then as SOURCE_GRANT_UNAVAILABLE, so without the totals an
		// operator cannot tell a re-resolved file apart from a provider that
		// simply describes its length differently per request.
		log.Printf("[peartube] granted source %s length changed under the grant: promised %d, upstream now reports %d",
			b.path, b.length, total)
		response.Close()
		return nil, ErrSourceGone
	}
	if servedStart != start || servedEnd != end {
		response.Close()
		return nil, ErrSourceUnavailable
	}
	if count := end - start + 1; response.ContentLength > 0 && response.ContentLength != count {
		response.Close()
		return nil, ErrSourceUnavailable
	}
	return response, nil
}

// close releases the held stream. It is reached from every path that ends a
// grant: a terminal job status, a consent withdrawal, a lapsed TTL, registry
// shutdown, and a prepared source that was never issued.
func (b *remoteBacking) close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	session := b.session
	b.session = nil
	// A checked-out stream belongs to its reader: cancelling streamCtx unblocks
	// the read still in flight and release closes the response, which is why
	// this call must not. Only a parked stream is this call's to close.
	var parked *streaming.Response
	if session != nil {
		session.stopIdleLocked()
		if !session.inUse {
			parked = session.response
		}
	}
	b.mu.Unlock()
	b.cancel()
	if parked != nil {
		return parked.Close()
	}
	return nil
}

// heldRange serves one range out of a held stream. Its Close is the hinge: it is
// how the backing learns whether the stream is still splice-ready.
type heldRange struct {
	backing   *remoteBacking
	session   *remoteSession
	reader    io.Reader
	count     int64
	remaining int64
	broken    bool
	closed    bool
}

func (h *heldRange) Read(buffer []byte) (int, error) {
	read, err := h.reader.Read(buffer)
	h.remaining -= int64(read)
	if err != nil && (!errors.Is(err, io.EOF) || h.remaining > 0) {
		// The stream faulted, or ended inside the window this grant promised.
		// Either way its offset is no longer known well enough to splice onto.
		h.broken = true
	}
	return read, err
}

func (h *heldRange) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	h.backing.release(h.session, h.count-h.remaining, h.remaining == 0 && !h.broken)
	return nil
}

// boundedSourceBody caps an upstream body at the window that was asked for, so
// an over-long response cannot bleed past the range into the companion's next
// block.
type boundedSourceBody struct {
	reader io.Reader
	closer io.Closer
}

func (b *boundedSourceBody) Read(buffer []byte) (int, error) {
	return b.reader.Read(buffer)
}

func (b *boundedSourceBody) Close() error {
	return b.closer.Close()
}

// parseSourceContentRange reads a "bytes START-END/TOTAL" header. A response
// that cannot state all three is not a response this grant can serve from.
func parseSourceContentRange(value string) (int64, int64, int64, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, false
	}
	span, total, found := strings.Cut(strings.TrimSpace(strings.TrimPrefix(value, "bytes ")), "/")
	if !found {
		return 0, 0, 0, false
	}
	first, last, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	start, startErr := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	end, endErr := strconv.ParseInt(strings.TrimSpace(last), 10, 64)
	length, lengthErr := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if startErr != nil || endErr != nil || lengthErr != nil {
		return 0, 0, 0, false
	}
	if start < 0 || end < start || length <= end {
		return 0, 0, 0, false
	}
	return start, end, length, true
}

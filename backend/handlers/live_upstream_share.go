package handlers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/internal/netproxy"
	"novastream/internal/requestsecurity"
)

// Live upstream sharing.
//
// Every live HLS session runs its own FFmpeg, and each FFmpeg used to open its own connection to
// the provider. Two devices on the same channel therefore meant two connections on one IPTV
// account. Most accounts allow exactly one, so the provider kept dropping the older connection,
// FFmpeg reconnected and dropped the other one, and both viewers saw their picture jump back a few
// seconds every ~5s ("Stream ends prematurely ... Will reconnect", timestamp discontinuity).
//
// The hub keeps one upstream connection per channel and fans its MPEG-TS bytes out to the stdin
// of every session's FFmpeg. Sessions stay independent (their own playlist, segment numbering,
// target-specific output and restart supervision); only the provider connection is shared. The
// connection closes as soon as the last session lets go, so a channel switch frees the account
// slot immediately.

const (
	liveUpstreamReadChunk          = 32 * 1024
	liveUpstreamPrimeBytes         = 4 * 1024 * 1024 // a few seconds a late joiner can start from at once
	liveUpstreamSubscriberLag      = 1024            // chunks queued per subscriber before it is dropped
	liveUpstreamReadTimeout        = 20 * time.Second
	liveUpstreamMaxReconnects      = 3
	liveUpstreamHealthyAfter       = 10 * time.Second // a connection that lasted this long resets the reconnect budget
	mpegTSPacketSize               = 188
	mpegTSSyncByte            byte = 0x47
)

var (
	// errLiveUpstreamNotTS means the source answered with an HLS playlist rather than a
	// transport stream. FFmpeg has to read those itself, so the caller falls back to that.
	errLiveUpstreamNotTS = errors.New("live upstream is not an MPEG-TS stream")
	errLiveUpstreamLag   = errors.New("live upstream subscriber fell behind")
)

type liveUpstreamOpener func(ctx context.Context) (io.ReadCloser, error)

// liveUpstreamSource says how to open a channel's provider connection. Reconnect mirrors FFmpeg's
// -reconnect_streamed for IPTV: a dropped connection is reopened in place so viewers only see a
// discontinuity. Without it (HDHomeRun tuners) the end of the stream ends every subscriber and
// each session's supervisor decides what happens next, as before sharing.
type liveUpstreamSource struct {
	open      liveUpstreamOpener
	reconnect bool
}

type liveUpstreamHub struct {
	mu      sync.Mutex
	streams map[string]*liveUpstream
}

var sharedLiveUpstreams = &liveUpstreamHub{streams: make(map[string]*liveUpstream)}

type liveUpstream struct {
	hub    *liveUpstreamHub
	key    string
	label  string
	source liveUpstreamSource
	ctx    context.Context
	cancel context.CancelFunc

	ready   chan struct{} // closed once the first open finished; openErr is set on failure
	openErr error

	mu          sync.Mutex
	body        io.ReadCloser
	subscribers map[*liveUpstreamSubscriber]struct{}
	prime       [][]byte
	primeBytes  int
	ended       bool
}

type liveUpstreamSubscriber struct {
	upstream *liveUpstream
	chunks   chan []byte
	done     chan struct{}
	pending  []byte

	mu        sync.Mutex
	err       error
	closeOnce sync.Once
}

// liveUpstreamShareKey identifies the provider connection a live session would open, or returns
// "" when the input cannot be relayed as raw bytes (HLS playlists, non-HTTP transports).
func liveUpstreamShareKey(path string, tuning LiveTuningSettings) string {
	if tuning.ForceHLSInput || inputLooksLikeHLS(path) {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(path))
	if err != nil {
		return ""
	}
	if scheme := strings.ToLower(parsed.Scheme); scheme != "http" && scheme != "https" {
		return ""
	}
	headerKeys := make([]string, 0, len(tuning.RequestHeaders))
	for key := range tuning.RequestHeaders {
		headerKeys = append(headerKeys, key)
	}
	sort.Strings(headerKeys)
	var b strings.Builder
	b.WriteString(strings.TrimSpace(path))
	b.WriteString("\x00")
	b.WriteString(strings.TrimSpace(tuning.ProxyURL))
	if tuning.HDHomeRunInput {
		b.WriteString("\x00hdhomerun")
	}
	for _, key := range headerKeys {
		b.WriteString("\x00")
		b.WriteString(strings.ToLower(key))
		b.WriteString("=")
		b.WriteString(tuning.RequestHeaders[key])
	}
	return b.String()
}

// Subscribe attaches to the channel's upstream, opening it when nobody is reading it yet. The
// first subscriber gets the open error directly, matching what a private connection would have
// returned. `shared` reports whether another session already held the connection.
func (h *liveUpstreamHub) Subscribe(key, label string, source liveUpstreamSource) (sub *liveUpstreamSubscriber, shared bool, err error) {
	for {
		h.mu.Lock()
		upstream := h.streams[key]
		if upstream == nil {
			ctx, cancel := context.WithCancel(context.Background())
			upstream = &liveUpstream{
				hub: h, key: key, label: label, source: source, ctx: ctx, cancel: cancel,
				ready:       make(chan struct{}),
				subscribers: make(map[*liveUpstreamSubscriber]struct{}),
			}
			h.streams[key] = upstream
			sub = upstream.newSubscriber()
			h.mu.Unlock()

			body, openErr := source.open(ctx)
			if openErr != nil {
				upstream.openErr = openErr
				upstream.finish(openErr)
				close(upstream.ready)
				return nil, false, openErr
			}
			upstream.mu.Lock()
			upstream.body = body
			upstream.mu.Unlock()
			close(upstream.ready)
			go upstream.run(body)
			return sub, false, nil
		}
		h.mu.Unlock()

		<-upstream.ready
		if upstream.openErr != nil {
			// The opener this one waited on failed; try for itself rather than inherit an error
			// that may have been transient.
			upstream.waitRemoved()
			continue
		}
		if sub := upstream.attach(); sub != nil {
			return sub, true, nil
		}
		// Ended between lookup and attach (last viewer left, upstream died). Open a fresh one.
		upstream.waitRemoved()
	}
}

// Subscribers reports how many sessions currently read the upstream for key.
func (h *liveUpstreamHub) Subscribers(key string) int {
	h.mu.Lock()
	upstream := h.streams[key]
	h.mu.Unlock()
	if upstream == nil {
		return 0
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return len(upstream.subscribers)
}

func (u *liveUpstream) newSubscriber() *liveUpstreamSubscriber {
	sub := &liveUpstreamSubscriber{
		upstream: u,
		chunks:   make(chan []byte, liveUpstreamSubscriberLag),
		done:     make(chan struct{}),
	}
	u.mu.Lock()
	u.subscribers[sub] = struct{}{}
	u.mu.Unlock()
	return sub
}

// attach adds a late subscriber, seeding it with recent bytes so its FFmpeg can probe and cut a
// first segment without waiting for realtime input.
func (u *liveUpstream) attach() *liveUpstreamSubscriber {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.ended {
		return nil
	}
	sub := &liveUpstreamSubscriber{
		upstream: u,
		chunks:   make(chan []byte, liveUpstreamSubscriberLag+len(u.prime)),
		done:     make(chan struct{}),
	}
	for i, chunk := range u.prime {
		if i == 0 {
			chunk = alignToTransportStream(chunk)
		}
		if len(chunk) > 0 {
			sub.chunks <- chunk
		}
	}
	u.subscribers[sub] = struct{}{}
	return sub
}

func (u *liveUpstream) waitRemoved() {
	for {
		u.hub.mu.Lock()
		current := u.hub.streams[u.key]
		u.hub.mu.Unlock()
		if current != u {
			return
		}
		select {
		case <-u.ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (u *liveUpstream) run(body io.ReadCloser) {
	reconnects := 0
	var openErr error
	for {
		started := time.Now()
		received, err := u.pump(body)
		if u.ctx.Err() != nil {
			return
		}
		if openErr != nil {
			err, openErr = openErr, nil
		}
		if !u.source.reconnect {
			u.finish(err)
			return
		}
		// Only a connection that actually held up earns a fresh budget; one that drops right
		// after connecting (account limit, dead channel) must not be reopened forever.
		if received && time.Since(started) >= liveUpstreamHealthyAfter {
			reconnects = 0
		}
		if reconnects >= liveUpstreamMaxReconnects {
			log.Printf("[live-upstream] %s: giving up after %d reconnects: %v", u.label, reconnects, err)
			u.finish(err)
			return
		}
		reconnects++
		delay := time.Duration(reconnects) * 500 * time.Millisecond
		log.Printf("[live-upstream] %s: upstream ended (%v), reconnecting in %v (attempt %d, %d viewer(s))",
			u.label, err, delay, reconnects, u.subscriberCount())
		select {
		case <-u.ctx.Done():
			return
		case <-time.After(delay):
		}
		next, nextErr := u.source.open(u.ctx)
		if nextErr != nil {
			if u.ctx.Err() != nil {
				return
			}
			// An empty body lets the loop count this as one more failed attempt.
			openErr = nextErr
			body = io.NopCloser(bytes.NewReader(nil))
			continue
		}
		u.mu.Lock()
		if u.ended {
			u.mu.Unlock()
			_ = next.Close()
			return
		}
		u.body = next
		u.mu.Unlock()
		body = next
	}
}

// pump copies one upstream connection to every subscriber until it fails or goes silent.
func (u *liveUpstream) pump(body io.ReadCloser) (received bool, err error) {
	defer body.Close()
	watchdog := time.AfterFunc(liveUpstreamReadTimeout, func() { _ = body.Close() })
	defer watchdog.Stop()
	for {
		chunk := make([]byte, liveUpstreamReadChunk)
		n, readErr := body.Read(chunk)
		if n > 0 {
			watchdog.Reset(liveUpstreamReadTimeout)
			received = true
			u.broadcast(chunk[:n])
		}
		if readErr != nil {
			return received, readErr
		}
	}
}

func (u *liveUpstream) broadcast(chunk []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prime = append(u.prime, chunk)
	u.primeBytes += len(chunk)
	for u.primeBytes-len(u.prime[0]) >= liveUpstreamPrimeBytes {
		u.primeBytes -= len(u.prime[0])
		u.prime = u.prime[1:]
	}
	for sub := range u.subscribers {
		select {
		case sub.chunks <- chunk:
		default:
			// A reader that cannot keep up with realtime would otherwise stall everyone else.
			// Dropping it ends that FFmpeg; its supervisor restarts and re-subscribes.
			delete(u.subscribers, sub)
			sub.fail(errLiveUpstreamLag)
		}
	}
}

func (u *liveUpstream) subscriberCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.subscribers)
}

// finish ends the upstream for every subscriber and removes it from the hub.
func (u *liveUpstream) finish(err error) {
	u.hub.mu.Lock()
	// Keep the entry unavailable to a replacement opener until the old
	// connection has actually closed. Removing it before Close allowed a
	// reconnect to overlap the provider's last occupied slot.
	defer u.hub.mu.Unlock()
	if u.hub.streams[u.key] == u {
		delete(u.hub.streams, u.key)
	}

	u.mu.Lock()
	u.ended = true
	subscribers := u.subscribers
	u.subscribers = make(map[*liveUpstreamSubscriber]struct{})
	body := u.body
	u.prime = nil
	u.primeBytes = 0
	u.mu.Unlock()

	u.cancel()
	if body != nil {
		_ = body.Close()
	}
	for sub := range subscribers {
		sub.fail(err)
	}
}

// release drops one subscriber. The last one closes the provider connection before returning, so
// a supervisor restart or channel switch never overlaps it with the next connection.
func (u *liveUpstream) release(sub *liveUpstreamSubscriber) {
	u.mu.Lock()
	if _, ok := u.subscribers[sub]; !ok {
		u.mu.Unlock()
		return
	}
	delete(u.subscribers, sub)
	last := len(u.subscribers) == 0 && !u.ended
	if last {
		// Claimed under the lock so a session attaching right now opens a fresh upstream
		// instead of joining one that is about to close.
		u.ended = true
	}
	u.mu.Unlock()
	if last {
		log.Printf("[live-upstream] %s: last viewer left, closing upstream", u.label)
		u.finish(io.EOF)
	}
}

func (s *liveUpstreamSubscriber) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.done) })
}

func (s *liveUpstreamSubscriber) Read(p []byte) (int, error) {
	if len(s.pending) == 0 {
		select {
		case chunk := <-s.chunks:
			s.pending = chunk
		case <-s.done:
			// Deliver what was queued before the end, then the reason.
			select {
			case chunk := <-s.chunks:
				s.pending = chunk
			default:
				s.mu.Lock()
				err := s.err
				s.mu.Unlock()
				if err == nil {
					err = io.EOF
				}
				return 0, err
			}
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *liveUpstreamSubscriber) Close() error {
	s.upstream.release(s)
	s.fail(io.EOF)
	return nil
}

// alignToTransportStream drops a partial leading packet so a late joiner starts on a TS packet
// boundary. FFmpeg would resync anyway; aligning just keeps the first probe clean.
func alignToTransportStream(chunk []byte) []byte {
	for i := 0; i < len(chunk) && i < mpegTSPacketSize; i++ {
		if chunk[i] != mpegTSSyncByte {
			continue
		}
		if next := i + mpegTSPacketSize; next >= len(chunk) || chunk[next] == mpegTSSyncByte {
			return chunk[i:]
		}
	}
	return chunk
}

// openLiveUpstream describes the provider connection for a session's channel.
func openLiveUpstream(session *HLSSession) liveUpstreamSource {
	path := session.Path
	tuning := session.LiveTuning
	if tuning.HDHomeRunInput {
		return liveUpstreamSource{open: func(ctx context.Context) (io.ReadCloser, error) {
			return openHDHomeRunStream(ctx, path, tuning.ProxyURL)
		}}
	}
	return liveUpstreamSource{reconnect: true, open: func(ctx context.Context) (io.ReadCloser, error) {
		requestCtx, cancelInput := context.WithCancel(ctx)
		// A failover-enabled tune has one startup budget across dial, headers
		// and the initial body. Stop this timer after sniffing data so a healthy
		// long-running provider connection keeps its normal lifetime.
		var startup *time.Timer
		if tuning.StationFailover != nil {
			startup = time.AfterFunc(liveStationStartupTimeout, cancelInput)
		}
		opened := false
		defer func() {
			if startup != nil {
				startup.Stop()
			}
			if !opened {
				cancelInput()
			}
		}()
		client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{ResponseHeaderTimeout: 15 * time.Second}, tuning.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("create live client: %w", err)
		}
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("prepare live request: %w", err)
		}
		req.Header.Set("User-Agent", liveStreamUserAgent)
		applyRequestHeaders(req.Header, tuning.RequestHeaders)
		resp, err := client.Do(req)
		if err != nil {
			client.CloseIdleConnections()
			return nil, fmt.Errorf("open live stream %s: %w", requestsecurity.URLForLog(path), err)
		}
		if resp.StatusCode >= http.StatusBadRequest {
			resp.Body.Close()
			client.CloseIdleConnections()
			return nil, fmt.Errorf("live stream returned status %d", resp.StatusCode)
		}
		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		reader := bufio.NewReaderSize(resp.Body, liveUpstreamReadChunk)
		// Header timeouts do not cover a provider that sends headers but no
		// body. Bound the initial sniff too, so a stopped startup cannot hold
		// its capacity reservation indefinitely.
		sniffTimeout := time.AfterFunc(liveUpstreamReadTimeout, func() { _ = resp.Body.Close() })
		head, sniffErr := reader.Peek(7)
		sniffTimeout.Stop()
		if sniffErr != nil && len(head) == 0 {
			resp.Body.Close()
			client.CloseIdleConnections()
			return nil, fmt.Errorf("read live stream header: %w", sniffErr)
		}
		if strings.Contains(contentType, "mpegurl") || bytes.HasPrefix(head, []byte("#EXTM3U")) {
			resp.Body.Close()
			client.CloseIdleConnections()
			return nil, errLiveUpstreamNotTS
		}
		opened = true
		return liveUpstreamBody{Reader: reader, body: resp.Body, client: client, cancel: cancelInput}, nil
	}}
}

type liveUpstreamBody struct {
	io.Reader
	body   io.Closer
	client *http.Client
	cancel context.CancelFunc
}

func (b liveUpstreamBody) Close() error {
	if b.cancel != nil {
		b.cancel()
	}
	err := b.body.Close()
	b.client.CloseIdleConnections()
	return err
}

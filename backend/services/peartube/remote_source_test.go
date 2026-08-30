package peartube

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/services/streaming"
)

// fakeRemoteReader stands in for the composite streaming provider. It models the
// one behaviour this feature depends on: the resolved address behind a stream
// path dies, and Stream is the call that quietly resolves a new one. addressLife
// is how many ranges one address serves before it has to be re-resolved.
//
// It is safe for concurrent use, because the callback serves HTTP and two ranges
// for one grant can be in flight at once.
type fakeRemoteReader struct {
	content     []byte
	addressLife int

	// total overrides the total this reader claims, so a source that changed
	// identity underneath a live grant can be simulated.
	total int64
	// failures is the number of leading Stream calls that fail outright.
	failures int
	// err fails every call, standing in for a torrent that is really gone.
	err error
	// ignoreRange answers with 200 and the whole body, like an upstream that
	// dropped the Range header.
	ignoreRange bool
	// omitContentRange answers 206 without stating a total.
	omitContentRange bool
	// gate, when set, holds every body's first read until enough of them have
	// arrived. It is how two ranges on one grant are made to genuinely overlap
	// rather than merely be started from two goroutines.
	gate *arrivalBarrier

	mu              sync.Mutex
	addresses       int
	servedOnAddress int
	ranges          []string
	// closes counts the upstream bodies that were closed, which is how a test
	// tells a released session apart from a leaked one.
	closes int
}

func (f *fakeRemoteReader) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ranges = append(f.ranges, req.RangeHeader)
	if f.err != nil {
		return nil, f.err
	}
	if f.failures > 0 {
		f.failures--
		return nil, errors.New("upstream refused the connection")
	}
	if f.addresses == 0 || (f.addressLife > 0 && f.servedOnAddress >= f.addressLife) {
		f.addresses++
		f.servedOnAddress = 0
	}
	f.servedOnAddress++

	total := f.total
	if total == 0 {
		total = int64(len(f.content))
	}
	if f.ignoreRange {
		return &streaming.Response{
			Status:        http.StatusOK,
			Headers:       http.Header{},
			ContentLength: int64(len(f.content)),
			Body:          f.bodyLocked(f.content),
		}, nil
	}
	var start, end int64
	if _, err := fmt.Sscanf(req.RangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		return nil, fmt.Errorf("fake reader got an unusable range %q", req.RangeHeader)
	}
	if start < 0 || end < start || end >= int64(len(f.content)) {
		return nil, fmt.Errorf("fake reader got an out-of-bounds range %q", req.RangeHeader)
	}
	headers := http.Header{}
	if !f.omitContentRange {
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	}
	return &streaming.Response{
		Status:        http.StatusPartialContent,
		Headers:       headers,
		ContentLength: end - start + 1,
		Body:          f.bodyLocked(f.content[start : end+1]),
	}, nil
}

func (f *fakeRemoteReader) bodyLocked(content []byte) io.ReadCloser {
	return &fakeRemoteBody{reader: f, body: bytes.NewReader(content), gate: f.gate}
}

// streamCount is how many upstream sessions this reader was asked to establish,
// which is the number session reuse exists to shrink.
func (f *fakeRemoteReader) streamCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ranges)
}

func (f *fakeRemoteReader) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func (f *fakeRemoteReader) rangeHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ranges...)
}

// fakeRemoteBody is one upstream body. Its close is counted, so a test can prove
// a held stream was released rather than pinned for the life of the process.
type fakeRemoteBody struct {
	reader *fakeRemoteReader
	body   io.Reader
	gate   *arrivalBarrier
	closed bool
}

func (b *fakeRemoteBody) Read(buffer []byte) (int, error) {
	if b.gate != nil {
		b.gate.arrive()
	}
	return b.body.Read(buffer)
}

func (b *fakeRemoteBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	b.reader.mu.Lock()
	b.reader.closes++
	b.reader.mu.Unlock()
	return nil
}

// arrivalBarrier holds every gated body until need of them are inside a read.
type arrivalBarrier struct {
	mu       sync.Mutex
	need     int
	arrived  int
	released chan struct{}
}

func newArrivalBarrier(need int) *arrivalBarrier {
	return &arrivalBarrier{need: need, released: make(chan struct{})}
}

func (a *arrivalBarrier) arrive() {
	a.mu.Lock()
	if a.arrived < a.need {
		a.arrived++
		if a.arrived == a.need {
			close(a.released)
		}
	}
	a.mu.Unlock()
	<-a.released
}

// remoteFixture is a deterministic body big enough for several disjoint ranges
// while staying kilobyte-sized.
func remoteFixture(size int) []byte {
	body := make([]byte, size)
	for index := range body {
		body[index] = byte(index*7 + index/251)
	}
	return body
}

// newTestRemoteRegistry gives a remote-source registry a TTL long enough to hold
// a grant across the multi-minute gaps a real archive has between ranges, and a
// real random source so several grants can be live at once.
func newTestRemoteRegistry(t *testing.T, now func() time.Time, maxRangeBytes int64) *SourceGrantRegistry {
	t.Helper()
	registry, err := NewSourceGrantRegistry(SourceGrantOptions{
		Now:             now,
		Random:          rand.Reader,
		MaxEntries:      8,
		MaxRangeBytes:   maxRangeBytes,
		TTL:             30 * time.Minute,
		MaxClockSkew:    30 * time.Second,
		CompanionID:     "peartube-companion",
		CompanionSecret: testSourceSecret(),
	})
	if err != nil {
		t.Fatalf("NewSourceGrantRegistry: %v", err)
	}
	t.Cleanup(registry.Close)
	return registry
}

func issueTestRemoteSource(t *testing.T, registry *SourceGrantRegistry, reader RemoteRangeReader, streamPath, jobID string, expiresAt time.Time) IssuedSourceGrant {
	t.Helper()
	prepared, err := registry.PrepareRemote(context.Background(), RemoteSource{Reader: reader, StreamPath: streamPath}, "video/x-matroska")
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	grant, err := registry.Issue(prepared, SourceGrantScope{
		CompanionID: "peartube-companion",
		JobID:       jobID,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		prepared.Close()
		t.Fatalf("Issue: %v", err)
	}
	return grant
}

// A granted remote source must serve exact disjoint ranges at whatever pace the
// companion asks for them, including long after the address that served the
// previous range stopped existing. Re-resolution has to happen underneath: the
// companion must never see the failure, because seeing it is what threw away
// forty-two minutes of a 4K transfer.
func TestRemoteSourceGrantServesDisjointRangesAcrossAddressExpiry(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	content := remoteFixture(4096)
	// One address survives two reads: the length probe plus a single range. Every
	// range after that forces the streaming layer to resolve a new one.
	reader := &fakeRemoteReader{content: content, addressLife: 2}
	const streamPath = "/debrid/torbox/12345/file/9/The.Matrix.1999.mkv"
	grant := issueTestRemoteSource(t, registry, reader, streamPath, "ing_remote", now.Add(20*time.Minute))
	secret := testSourceSecret()

	if grant.Length != int64(len(content)) {
		t.Fatalf("granted length = %d, want %d", grant.Length, len(content))
	}

	windows := [][2]int64{{0, 511}, {1024, 1535}, {3584, 4095}}
	for index, window := range windows {
		// Minutes pass between ranges, with no playback in between. That gap is
		// the point: it is what kills an address and what a grant must survive.
		now = now.Add(5 * time.Minute)
		byteRange := fmt.Sprintf("bytes=%d-%d", window[0], window[1])
		request := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_remote", grant.ETag,
			byteRange, "peartube-companion", secret, now, "nonce-remote-range-"+strconv.Itoa(index))
		response := serveSource(registry, request)
		if response.Code != http.StatusPartialContent {
			t.Fatalf("range %s = %d, body = %s", byteRange, response.Code, response.Body.String())
		}
		if !bytes.Equal(response.Body.Bytes(), content[window[0]:window[1]+1]) {
			t.Fatalf("range %s served the wrong bytes", byteRange)
		}
		wantContentRange := fmt.Sprintf("bytes %d-%d/%d", window[0], window[1], len(content))
		if got := response.Header().Get("Content-Range"); got != wantContentRange {
			t.Fatalf("range %s Content-Range = %q, want %q", byteRange, got, wantContentRange)
		}
		if got := response.Header().Get("ETag"); got != grant.ETag {
			t.Fatalf("range %s ETag = %q, want the grant's %q", byteRange, got, grant.ETag)
		}
	}

	// Four upstream reads (one probe, three ranges) against a two-read address
	// life means the address was resolved more than once, and every range still
	// reached the companion as a 206.
	if reader.addresses < 2 {
		t.Fatalf("upstream addresses resolved = %d, want re-resolution", reader.addresses)
	}
	if got := len(reader.ranges); got != len(windows)+1 {
		t.Fatalf("upstream reads = %d, want %d (one probe plus one per range)", got, len(windows)+1)
	}
	if reader.ranges[0] != "bytes=0-0" {
		t.Fatalf("length probe read %q, want a single byte", reader.ranges[0])
	}
}

// The ETag is the companion's only safe resume anchor, so it must not move when
// the address underneath is re-resolved, and it must move when the bytes are not
// the same bytes.
func TestRemoteSourceETagIsStableAcrossReResolutionAndDistinctPerContent(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	content := remoteFixture(2048)
	const streamPath = "/debrid/torbox/12345/file/9/The.Matrix.1999.mkv"
	// Every single read forces a fresh address.
	reader := &fakeRemoteReader{content: content, addressLife: 1}

	first := issueTestRemoteSource(t, registry, reader, streamPath, "ing_etag_first", now.Add(time.Minute))
	secret := testSourceSecret()
	request := signedSourceRequest(t, http.MethodGet, first.Capability, "ing_etag_first", first.ETag,
		"bytes=0-255", "peartube-companion", secret, now, "nonce-etag-range")
	if response := serveSource(registry, request); response.Code != http.StatusPartialContent {
		t.Fatalf("first range = %d, body = %s", response.Code, response.Body.String())
	}
	second := issueTestRemoteSource(t, registry, reader, streamPath, "ing_etag_second", now.Add(time.Minute))

	if reader.addresses < 3 {
		t.Fatalf("upstream addresses resolved = %d, want several", reader.addresses)
	}
	if second.ETag != first.ETag {
		t.Fatalf("ETag moved across re-resolution: %q then %q", first.ETag, second.ETag)
	}
	if !strings.HasPrefix(second.ETag, `"remote-sha256-`) {
		t.Fatalf("remote ETag = %q, want a domain-separated remote identity", second.ETag)
	}

	// Same path, different size, and same size, different path: both are
	// different content and must be different identities.
	if RemoteSourceETag(streamPath, int64(len(content))) != first.ETag {
		t.Fatal("ETag is not a pure function of the path and the length")
	}
	if RemoteSourceETag(streamPath, int64(len(content))+1) == first.ETag {
		t.Fatal("a different length produced the same ETag")
	}
	if RemoteSourceETag(streamPath+".other", int64(len(content))) == first.ETag {
		t.Fatal("a different stream path produced the same ETag")
	}
}

// The companion derives its block plan and its resume-offset validation from the
// total length, so a grant must state it before it hands out a capability, and
// must refuse to exist at all when the upstream cannot.
func TestRemoteSourceGrantStatesTotalLengthBeforeServingAnything(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	content := remoteFixture(3000)
	reader := &fakeRemoteReader{content: content}
	grant := issueTestRemoteSource(t, registry, reader, "/debrid/torbox/1/file/1/movie.mkv", "ing_length", now.Add(time.Minute))

	if grant.Length != int64(len(content)) {
		t.Fatalf("granted length = %d, want %d", grant.Length, len(content))
	}
	if grant.SHA256 != "" {
		t.Fatalf("remote grant claimed a whole-file digest %q it cannot have computed", grant.SHA256)
	}
	head := signedSourceRequest(t, http.MethodHead, grant.Capability, "ing_length", grant.ETag,
		"", "peartube-companion", testSourceSecret(), now, "nonce-remote-head")
	response := serveSource(registry, head)
	if response.Code != http.StatusOK {
		t.Fatalf("HEAD = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Fatalf("HEAD Content-Length = %q, want %d", got, len(content))
	}
	// A HEAD must not have touched the upstream: only the one probe did.
	if got := len(reader.ranges); got != 1 {
		t.Fatalf("upstream reads = %d, want only the length probe", got)
	}

	silent := &fakeRemoteReader{content: content, omitContentRange: true}
	if _, err := registry.PrepareRemote(context.Background(),
		RemoteSource{Reader: silent, StreamPath: "/debrid/torbox/1/file/1/silent.mkv"}, "video/x-matroska"); err == nil {
		t.Fatal("a source that cannot state its total length was granted anyway")
	}
	deaf := &fakeRemoteReader{content: content, ignoreRange: true}
	if _, err := registry.PrepareRemote(context.Background(),
		RemoteSource{Reader: deaf, StreamPath: "/debrid/torbox/1/file/1/deaf.mkv"}, "video/x-matroska"); err == nil {
		t.Fatal("a source that does not serve byte ranges was granted anyway")
	}
}

// Whether a failed range is retryable decides whether the companion keeps or
// destroys its confirmed progress, so the two cases must never be conflated.
func TestRemoteSourceRangeFailuresAreRetryableUnlessTheContentChanged(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	content := remoteFixture(1024)
	secret := testSourceSecret()

	for _, testCase := range []struct {
		name    string
		mutate  func(*fakeRemoteReader)
		want    int
		hasBody bool
	}{
		{
			name:   "transient upstream failure is retryable",
			mutate: func(reader *fakeRemoteReader) { reader.failures = 1 },
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "a source that is really gone is retryable until the grant lapses",
			mutate: func(reader *fakeRemoteReader) { reader.err = errors.New("torbox torrent expired or deleted") },
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "an upstream that stopped honouring ranges is retryable",
			mutate: func(reader *fakeRemoteReader) { reader.ignoreRange = true },
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "a changed total length is terminal",
			mutate: func(reader *fakeRemoteReader) { reader.total = int64(len(content)) + 4096 },
			want:   http.StatusGone,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
			reader := &fakeRemoteReader{content: content}
			grant := issueTestRemoteSource(t, registry, reader, "/debrid/torbox/1/file/1/movie.mkv", "ing_fault", now.Add(time.Minute))
			// The fault is introduced only after the grant exists, which is the
			// real sequence: the grant outlives the address it was probed with.
			testCase.mutate(reader)

			request := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_fault", grant.ETag,
				"bytes=0-255", "peartube-companion", secret, now, "nonce-remote-fault")
			response := serveSource(registry, request)
			if response.Code != testCase.want {
				t.Fatalf("range = %d, want %d", response.Code, testCase.want)
			}
			// The decisive property: never a 206 carrying a short or wrong body,
			// which no companion can tell from real content until much later.
			if bytes.Contains(response.Body.Bytes(), content[:16]) {
				t.Fatal("a failed range leaked media bytes into its error body")
			}
			if testCase.want == http.StatusServiceUnavailable &&
				response.Header().Get("Retry-After") != strconv.Itoa(sourceRetryAfterSeconds) {
				t.Fatalf("retryable range Retry-After = %q", response.Header().Get("Retry-After"))
			}
		})
	}
}

// A grant exists for a job, not for a viewing session. Nothing but a terminal
// job status or a consent withdrawal may end it, and a lapse must be answerable
// with a fresh grant for the very same job.
func TestRemoteSourceGrantOutlivesPlaybackAndOnlyTerminalStatusRevokesIt(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	content := remoteFixture(1024)
	reader := &fakeRemoteReader{content: content, addressLife: 1}
	const streamPath = "/debrid/torbox/777/file/3/GoT.S01E02.mkv"
	secret := testSourceSecret()

	grant := issueTestRemoteSource(t, registry, reader, streamPath, "ing_lifetime", now.Add(20*time.Minute))
	now = now.Add(25 * time.Minute)
	lapsed := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_lifetime", grant.ETag,
		"bytes=0-255", "peartube-companion", secret, now, "nonce-lifetime-lapsed")
	if got := serveSource(registry, lapsed).Code; got != http.StatusUnauthorized {
		t.Fatalf("lapsed range = %d, want %d so the companion re-attaches instead of purging", got, http.StatusUnauthorized)
	}

	// Re-granting the same job needs no playback and no session: the stream path
	// is re-probed and the identity comes out the same, so the companion resumes
	// against the offset it already confirmed.
	regranted := issueTestRemoteSource(t, registry, reader, streamPath, "ing_lifetime", now.Add(20*time.Minute))
	if regranted.ETag != grant.ETag || regranted.Length != grant.Length {
		t.Fatalf("re-granted identity drifted: %q/%d then %q/%d", grant.ETag, grant.Length, regranted.ETag, regranted.Length)
	}
	served := signedSourceRequest(t, http.MethodGet, regranted.Capability, "ing_lifetime", regranted.ETag,
		"bytes=512-767", "peartube-companion", secret, now, "nonce-lifetime-resumed")
	response := serveSource(registry, served)
	if response.Code != http.StatusPartialContent {
		t.Fatalf("resumed range = %d, body = %s", response.Code, response.Body.String())
	}
	if !bytes.Equal(response.Body.Bytes(), content[512:768]) {
		t.Fatal("resumed range served the wrong bytes")
	}

	registry.RevokeJob("ing_lifetime")
	afterTerminal := signedSourceRequest(t, http.MethodGet, regranted.Capability, "ing_lifetime", regranted.ETag,
		"bytes=0-255", "peartube-companion", secret, now, "nonce-lifetime-terminal")
	if got := serveSource(registry, afterTerminal).Code; got != http.StatusGone {
		t.Fatalf("range after terminal status = %d, want %d", got, http.StatusGone)
	}
}

// serveRemoteRange asks one grant for one inclusive window and insists on a 206
// carrying exactly the bytes of that window.
func serveRemoteRange(t *testing.T, registry *SourceGrantRegistry, grant IssuedSourceGrant, jobID string,
	secret [32]byte, now time.Time, nonce string, start, end int64, content []byte) []byte {
	t.Helper()
	byteRange := fmt.Sprintf("bytes=%d-%d", start, end)
	request := signedSourceRequest(t, http.MethodGet, grant.Capability, jobID, grant.ETag,
		byteRange, "peartube-companion", secret, now, nonce)
	response := serveSource(registry, request)
	if response.Code != http.StatusPartialContent {
		t.Fatalf("range %s = %d, body = %s", byteRange, response.Code, response.Body.String())
	}
	served := response.Body.Bytes()
	if !bytes.Equal(served, content[start:end+1]) {
		t.Fatalf("range %s served %d bytes that are not that window", byteRange, len(served))
	}
	return served
}

// Session reuse is the whole difference between archiving at CDN speed and
// crawling. Establishing an upstream session — unrestrict, TLS, first byte — was
// measured at roughly eight seconds against the 0.16s the same connection then
// needed to carry 4 MiB, so a companion pulling a file front to back must cost
// one session for the run rather than one per range.
//
// Reuse is a transport saving and nothing else, so the same windows must come out
// byte-identical whether they were served off one held stream or off a fresh one
// each.
func TestRemoteSourceReusesOneUpstreamSessionAcrossContiguousRanges(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	content := remoteFixture(4096)
	secret := testSourceSecret()
	windows := [][2]int64{{0, 511}, {512, 1023}, {1024, 1535}, {1536, 2047}}

	warm := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	warmReader := &fakeRemoteReader{content: content}
	warmGrant := issueTestRemoteSource(t, warm, warmReader,
		"/debrid/torbox/1/file/1/warm.mkv", "ing_warm", now.Add(20*time.Minute))
	served := make(map[int64][]byte, len(windows))
	for index, window := range windows {
		served[window[0]] = serveRemoteRange(t, warm, warmGrant, "ing_warm", secret, now,
			"nonce-warm-"+strconv.Itoa(index), window[0], window[1], content)
	}
	if got := warmReader.streamCount(); got != 2 {
		t.Fatalf("upstream Stream calls = %d for %d contiguous ranges, want 2: the length probe and one session",
			got, len(windows))
	}
	// One byte short of the whole file on purpose: a range covering the entire
	// entity is the one an upstream may answer with a plain 200 and no
	// Content-Range, which would leave the identity guard nothing to read.
	if got := warmReader.rangeHeaders()[1]; got != "bytes=0-4094" {
		t.Fatalf("the session opened %q, want a tail the ranges after it can read on", got)
	}

	// Asked for back to front, no range continues the previous one, so each pays
	// for its own session.
	cold := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	coldReader := &fakeRemoteReader{content: content}
	coldGrant := issueTestRemoteSource(t, cold, coldReader,
		"/debrid/torbox/1/file/1/cold.mkv", "ing_cold", now.Add(20*time.Minute))
	for index := len(windows) - 1; index >= 0; index-- {
		window := windows[index]
		fresh := serveRemoteRange(t, cold, coldGrant, "ing_cold", secret, now,
			"nonce-cold-"+strconv.Itoa(index), window[0], window[1], content)
		if !bytes.Equal(fresh, served[window[0]]) {
			t.Fatalf("window %d-%d differed between a reused session and a fresh one", window[0], window[1])
		}
	}
	if got := coldReader.streamCount(); got != len(windows)+1 {
		t.Fatalf("upstream Stream calls = %d for %d non-continuing ranges, want %d",
			got, len(windows), len(windows)+1)
	}
}

// Resume is not hypothetical: a companion that already has half a file asks for a
// range starting mid-file, and a companion that lost its place asks backwards. A
// held stream that cannot serve the range must be dropped rather than misread,
// and reuse must pick up again from wherever the new one landed.
func TestRemoteSourceGapsOpenAFreshSessionAndReuseResumesAfterThem(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	content := remoteFixture(4096)
	secret := testSourceSecret()
	registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	reader := &fakeRemoteReader{content: content}
	grant := issueTestRemoteSource(t, registry, reader,
		"/debrid/torbox/1/file/1/resume.mkv", "ing_resume", now.Add(20*time.Minute))

	for index, step := range []struct {
		window      [2]int64
		wantStreams int
	}{
		// A resume mid-file: nothing is held yet, so this opens a session.
		{window: [2]int64{2048, 2559}, wantStreams: 2},
		// Contiguous with it: read on, no new session.
		{window: [2]int64{2560, 3071}, wantStreams: 2},
		// A backwards jump cannot continue that stream.
		{window: [2]int64{512, 1023}, wantStreams: 3},
		// Contiguous with the new one: reuse is back.
		{window: [2]int64{1024, 1535}, wantStreams: 3},
		// A forward gap is exactly as unusable as a backwards one.
		{window: [2]int64{3584, 4095}, wantStreams: 4},
	} {
		serveRemoteRange(t, registry, grant, "ing_resume", secret, now,
			"nonce-resume-"+strconv.Itoa(index), step.window[0], step.window[1], content)
		if got := reader.streamCount(); got != step.wantStreams {
			t.Fatalf("after range %d-%d, upstream Stream calls = %d, want %d",
				step.window[0], step.window[1], got, step.wantStreams)
		}
	}
}

// A held session must not become the place identity checks stopped happening.
// Every real open re-reads the total the upstream reports and refuses to serve a
// grant whose length it no longer agrees with; a continuation needs no such check
// because bytes cannot be substituted mid-connection without the connection
// breaking. So the classification has to survive a session: terminal for an
// identity change, retryable for a transport fault.
func TestRemoteSourceHeldSessionStillClassifiesFailuresOnReopen(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	content := remoteFixture(2048)
	secret := testSourceSecret()

	changed := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	changedReader := &fakeRemoteReader{content: content}
	changedGrant := issueTestRemoteSource(t, changed, changedReader,
		"/debrid/torbox/1/file/1/changed.mkv", "ing_changed", now.Add(20*time.Minute))
	serveRemoteRange(t, changed, changedGrant, "ing_changed", secret, now, "nonce-changed-first", 0, 511, content)
	changedReader.total = int64(len(content)) + 4096
	// Contiguous, so it comes off the stream that was already verified: the
	// upstream is not asked anything and has nothing new to be disbelieved about.
	serveRemoteRange(t, changed, changedGrant, "ing_changed", secret, now, "nonce-changed-warm", 512, 1023, content)
	if got := changedReader.streamCount(); got != 2 {
		t.Fatalf("Stream calls = %d, want 2: a contiguous range must not have re-opened", got)
	}
	// The next gap forces a real open, and that open must see the disagreement.
	gapped := signedSourceRequest(t, http.MethodGet, changedGrant.Capability, "ing_changed", changedGrant.ETag,
		"bytes=1536-2047", "peartube-companion", secret, now, "nonce-changed-gap")
	gappedResponse := serveSource(changed, gapped)
	if gappedResponse.Code != http.StatusGone {
		t.Fatalf("re-open onto a changed total = %d, want %d", gappedResponse.Code, http.StatusGone)
	}
	if bytes.Contains(gappedResponse.Body.Bytes(), content[:16]) {
		t.Fatal("a terminal re-open leaked media bytes into its error body")
	}

	// A transport fault on the re-open stays retryable, and the grant survives it.
	flaky := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	flakyReader := &fakeRemoteReader{content: content}
	flakyGrant := issueTestRemoteSource(t, flaky, flakyReader,
		"/debrid/torbox/1/file/1/flaky.mkv", "ing_flaky", now.Add(20*time.Minute))
	serveRemoteRange(t, flaky, flakyGrant, "ing_flaky", secret, now, "nonce-flaky-first", 0, 511, content)
	flakyReader.failures = 1
	faulted := signedSourceRequest(t, http.MethodGet, flakyGrant.Capability, "ing_flaky", flakyGrant.ETag,
		"bytes=1536-2047", "peartube-companion", secret, now, "nonce-flaky-fault")
	faultedResponse := serveSource(flaky, faulted)
	if faultedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("re-open onto a transport fault = %d, want %d", faultedResponse.Code, http.StatusServiceUnavailable)
	}
	if faultedResponse.Header().Get("Retry-After") != strconv.Itoa(sourceRetryAfterSeconds) {
		t.Fatalf("retryable re-open Retry-After = %q", faultedResponse.Header().Get("Retry-After"))
	}
	serveRemoteRange(t, flaky, flakyGrant, "ing_flaky", secret, now, "nonce-flaky-retry", 1536, 2047, content)
}

// A held session is a live debrid connection. Every way a grant can end has to
// close it, or a finished job leaves an upstream stream pinned until the process
// exits.
func TestRemoteSourceHeldSessionIsClosedWhenTheGrantEnds(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	content := remoteFixture(2048)
	secret := testSourceSecret()

	for _, testCase := range []struct {
		name string
		end  func(registry *SourceGrantRegistry, jobID string)
	}{
		{
			name: "terminal job status",
			end:  func(registry *SourceGrantRegistry, jobID string) { registry.RevokeJob(jobID) },
		},
		{
			name: "consent withdrawal",
			end:  func(registry *SourceGrantRegistry, _ string) { registry.RevokeAll() },
		},
		{
			name: "registry shutdown",
			end:  func(registry *SourceGrantRegistry, _ string) { registry.Close() },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
			reader := &fakeRemoteReader{content: content}
			grant := issueTestRemoteSource(t, registry, reader,
				"/debrid/torbox/1/file/1/held.mkv", "ing_held", now.Add(20*time.Minute))
			// The length probe closes its own one-byte body; the session that
			// serves the range is deliberately still open after it.
			afterProbe := reader.closeCount()
			serveRemoteRange(t, registry, grant, "ing_held", secret, now, "nonce-held-range", 0, 511, content)
			if got := reader.closeCount(); got != afterProbe {
				t.Fatalf("upstream closes = %d after one range, want the session still held at %d", got, afterProbe)
			}
			testCase.end(registry, "ing_held")
			if got := reader.closeCount(); got != afterProbe+1 {
				t.Fatalf("upstream closes = %d after the grant ended, want the held session closed", got)
			}
		})
	}
}

// A companion that simply stops asking must not pin a debrid connection for the
// remaining life of the grant, and the range it eventually does ask for must
// still be served.
func TestRemoteSourceHeldSessionExpiresWhenIdle(t *testing.T) {
	content := remoteFixture(1024)
	reader := &fakeRemoteReader{content: content}
	backing := newRemoteBacking(reader, "/debrid/torbox/1/file/1/idle.mkv", int64(len(content)))
	backing.idleTimeout = 10 * time.Millisecond
	t.Cleanup(func() { _ = backing.close() })

	first, err := backing.open(context.Background(), 0, 256)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := io.ReadAll(first); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := reader.closeCount(); got != 0 {
		t.Fatalf("upstream closes = %d, want the drained session parked for the next range", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for reader.closeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := reader.closeCount(); got != 1 {
		t.Fatalf("upstream closes = %d after the idle timeout, want the parked session dropped", got)
	}

	second, err := backing.open(context.Background(), 256, 256)
	if err != nil {
		t.Fatalf("open after an idle expiry: %v", err)
	}
	body, err := io.ReadAll(second)
	if err != nil {
		t.Fatalf("read after an idle expiry: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close after an idle expiry: %v", err)
	}
	if !bytes.Equal(body, content[256:512]) {
		t.Fatal("the session opened after an idle expiry served the wrong bytes")
	}
	if got := reader.streamCount(); got != 2 {
		t.Fatalf("Stream calls = %d, want 2: one before the expiry and one after", got)
	}
}

// A session covers a bounded window, not the whole rest of the file, so a
// contiguous run has to notice when it reaches the end of one and open the next.
// Reading a continuation past its window returns EOF inside a window the grant
// promised bytes for, and that reaches the companion as a truncated 206 it cannot
// tell from real content until its merkle tree fails hours later.
func TestRemoteSourceContiguousRunCrossesTheSessionWindow(t *testing.T) {
	content := remoteFixture(1024)
	upstream := &fakeRemoteReader{content: content}
	backing := newRemoteBacking(upstream, "/debrid/torbox/1/file/1/window.mkv", int64(len(content)))
	t.Cleanup(func() { _ = backing.close() })

	// The window opened at offset zero deliberately stops one byte short of the
	// file, so a front-to-back run reaches its end on the final range.
	var served []byte
	for start := int64(0); start < int64(len(content)); start += 256 {
		body, err := backing.open(context.Background(), start, 256)
		if err != nil {
			t.Fatalf("open at %d: %v", start, err)
		}
		chunk, readErr := io.ReadAll(body)
		if readErr != nil {
			t.Fatalf("read at %d: %v", start, readErr)
		}
		if err := body.Close(); err != nil {
			t.Fatalf("close at %d: %v", start, err)
		}
		if int64(len(chunk)) != 256 {
			t.Fatalf("range at %d served %d bytes, want 256", start, len(chunk))
		}
		served = append(served, chunk...)
	}
	if !bytes.Equal(served, content) {
		t.Fatal("a run that crossed a session window did not reassemble into the file")
	}
	if got := upstream.streamCount(); got != 2 {
		t.Fatalf("Stream calls = %d, want 2: one window, then one more for the range that crossed its end", got)
	}
}

// The callback serves HTTP, so two ranges for one grant can arrive at once. One
// held stream cannot be read by both, and neither request may be parked behind
// the other's body: a companion draining a 16 MiB response would otherwise stall
// every other range on the same grant for the length of that transfer.
func TestRemoteSourceConcurrentRangesOnOneGrantBothSucceed(t *testing.T) {
	now := time.UnixMilli(1_786_406_400_000)
	content := remoteFixture(4096)
	secret := testSourceSecret()
	registry := newTestRemoteRegistry(t, func() time.Time { return now }, 512)
	reader := &fakeRemoteReader{content: content}
	grant := issueTestRemoteSource(t, registry, reader,
		"/debrid/torbox/1/file/1/concurrent.mkv", "ing_conc", now.Add(20*time.Minute))
	windows := [][2]int64{{0, 511}, {2048, 2559}}
	// Neither body yields a byte until both requests are inside their read loop,
	// so the overlap is forced rather than hoped for. If either request waited on
	// the other, this barrier would never trip and the test would time out.
	reader.gate = newArrivalBarrier(len(windows))

	type outcome struct {
		window [2]int64
		code   int
		body   []byte
	}
	results := make(chan outcome, len(windows))
	for index, window := range windows {
		go func() {
			request := signedSourceRequest(t, http.MethodGet, grant.Capability, "ing_conc", grant.ETag,
				fmt.Sprintf("bytes=%d-%d", window[0], window[1]), "peartube-companion", secret, now,
				"nonce-concurrent-"+strconv.Itoa(index))
			response := serveSource(registry, request)
			results <- outcome{window: window, code: response.Code, body: response.Body.Bytes()}
		}()
	}
	for range windows {
		select {
		case got := <-results:
			if got.code != http.StatusPartialContent {
				t.Fatalf("concurrent range %d-%d = %d", got.window[0], got.window[1], got.code)
			}
			if !bytes.Equal(got.body, content[got.window[0]:got.window[1]+1]) {
				t.Fatalf("concurrent range %d-%d served the wrong bytes", got.window[0], got.window[1])
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a concurrent range on a shared grant never returned: one request is waiting on the other")
		}
	}
}

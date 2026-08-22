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
	"testing"
	"time"

	"novastream/services/streaming"
)

// fakeRemoteReader stands in for the composite streaming provider. It models the
// one behaviour this feature depends on: the resolved address behind a stream
// path dies, and Stream is the call that quietly resolves a new one. addressLife
// is how many ranges one address serves before it has to be re-resolved.
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

	addresses       int
	servedOnAddress int
	ranges          []string
}

func (f *fakeRemoteReader) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
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
			Body:          io.NopCloser(bytes.NewReader(f.content)),
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
		Body:          io.NopCloser(bytes.NewReader(f.content[start : end+1])),
	}, nil
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

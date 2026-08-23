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
		backing: &remoteBacking{
			reader: source.Reader,
			path:   streamPath,
			length: length,
		},
		length: length,
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

// remoteBacking serves grant ranges through the streaming layer. It holds no
// connection, no session and no address between reads, which is what makes a
// grant outlive the playback that discovered the title: every range is resolved
// when it is asked for.
type remoteBacking struct {
	reader RemoteRangeReader
	path   string
	length int64
}

// verify has nothing cheap to check. A remote identity cannot be re-established
// without talking to the upstream, and doing that on a HEAD or DELETE would
// block the callback on a provider. The identity guard lives in open, which
// re-checks the total the upstream reports against the length this grant
// promised on every single range.
func (b *remoteBacking) verify() error {
	return nil
}

func (b *remoteBacking) open(ctx context.Context, start, count int64) (io.ReadCloser, error) {
	end := start + count - 1
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
	if response.ContentLength > 0 && response.ContentLength != count {
		response.Close()
		return nil, ErrSourceUnavailable
	}
	return &boundedSourceBody{reader: io.LimitReader(response.Body, count), closer: response}, nil
}

func (b *remoteBacking) close() error {
	b.reader = nil
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

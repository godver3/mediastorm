package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
)

var ErrNotFound = errors.New("stream not found")

// ErrStaleTorrent indicates the debrid torrent ID no longer exists on the provider.
// Callers should treat the cached stream path as invalid and re-resolve.
var ErrStaleTorrent = errors.New("debrid torrent expired or deleted")

// Request encapsulates a streaming request coming from the handler layer.
type Request struct {
	Path        string
	RangeHeader string
	Method      string
}

// Response wraps the streaming body and metadata needed by the HTTP layer.
type Response struct {
	Body          io.ReadCloser
	Headers       http.Header
	Status        int
	ContentLength int64
	Filename      string // Optional filename for display purposes
}

// Close closes the underlying response body if present.
func (r *Response) Close() error {
	if r == nil || r.Body == nil {
		return nil
	}
	return r.Body.Close()
}

// Provider supplies streaming data for a given request.
// This interface is kept for backward compatibility with the video handler.
type Provider interface {
	Stream(ctx context.Context, req Request) (*Response, error)
}

// DirectURLProvider is an optional interface that providers can implement
// to supply direct HTTP URLs for seekable access (e.g., for FFmpeg input)
type DirectURLProvider interface {
	Provider
	GetDirectURL(ctx context.Context, path string) (string, error)
}

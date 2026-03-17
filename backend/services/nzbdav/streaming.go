package nzbdav

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"novastream/services/streaming"
)

type StreamingProvider struct {
	baseURL    string
	httpClient *http.Client
}

func NewStreamingProvider(baseURL string) *StreamingProvider {
	return &StreamingProvider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 0},
	}
}

func (p *StreamingProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	if !strings.HasPrefix(req.Path, PathPrefix) {
		return nil, streaming.ErrNotFound
	}
	nzbdavPath := strings.TrimPrefix(req.Path, "/nzbdav")
	targetURL := fmt.Sprintf("%s%s", p.baseURL, nzbdavPath)
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	log.Printf("[nzbdav-stream] %s %s range=%q", method, targetURL, req.RangeHeader)
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
	if err != nil {
		return nil, err
	}
	if req.RangeHeader != "" {
		httpReq.Header.Set("Range", req.RangeHeader)
	}
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, streaming.ErrNotFound
	}
	headers := make(http.Header)
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition"} {
		if v := resp.Header.Get(k); v != "" {
			headers.Set(k, v)
		}
	}
	return &streaming.Response{
		Body: resp.Body, Headers: headers, Status: resp.StatusCode, ContentLength: resp.ContentLength,
	}, nil
}

func (p *StreamingProvider) GetDirectURL(_ context.Context, path string) (string, error) {
	if !strings.HasPrefix(path, PathPrefix) {
		return "", streaming.ErrNotFound
	}
	return fmt.Sprintf("%s%s", p.baseURL, strings.TrimPrefix(path, "/nzbdav")), nil
}

package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/services/streaming"
)

// mockProvider is a simple mock implementation of streaming.Provider for testing
type mockProvider struct {
	data []byte
}

func (m *mockProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	headers := make(http.Header)
	headers.Set("Content-Type", "video/x-matroska")
	headers.Set("Accept-Ranges", "bytes")

	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(m.data)),
		Headers:       headers,
		Status:        http.StatusOK,
		ContentLength: int64(len(m.data)),
	}, nil
}

func TestVideoHandlerStreamsFromMetadataProvider(t *testing.T) {
	data := []byte("hello world")
	provider := &mockProvider{data: data}

	handler := NewVideoHandlerWithProvider(false, "", "", "", provider)

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path=movies/title.mkv", nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	res := rr.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("body = %q, want %q", body, data)
	}
}

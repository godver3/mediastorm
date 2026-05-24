package debrid

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/services/streaming"
)

type streamingMockProvider struct {
	name            string
	info            *TorrentInfo
	unrestrictCalls int64
}

func (m *streamingMockProvider) Name() string { return m.name }
func (m *streamingMockProvider) AddMagnet(context.Context, string) (*AddMagnetResult, error) {
	return &AddMagnetResult{ID: "torrent1"}, nil
}
func (m *streamingMockProvider) AddTorrentFile(context.Context, []byte, string) (*AddMagnetResult, error) {
	return &AddMagnetResult{ID: "torrent1"}, nil
}
func (m *streamingMockProvider) GetTorrentInfo(context.Context, string) (*TorrentInfo, error) {
	return m.info, nil
}
func (m *streamingMockProvider) SelectFiles(context.Context, string, string) error { return nil }
func (m *streamingMockProvider) DeleteTorrent(context.Context, string) error       { return nil }
func (m *streamingMockProvider) UnrestrictLink(_ context.Context, link string) (*UnrestrictResult, error) {
	atomic.AddInt64(&m.unrestrictCalls, 1)
	return &UnrestrictResult{DownloadURL: link}, nil
}
func (m *streamingMockProvider) CheckInstantAvailability(context.Context, string) (bool, error) {
	return true, nil
}

func TestStreamingProviderEvictsCachedURLAndRetriesOnRequestFailure(t *testing.T) {
	var freshHits int64
	freshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&freshHits, 1)
		if got := r.Header.Get("Range"); got != "bytes=0-4" {
			t.Errorf("Range = %q, want bytes=0-4", got)
		}
		w.Header().Set("Content-Range", "bytes 0-4/5")
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("hello"))
	}))
	defer freshServer.Close()

	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("dead cached URL should not receive a request after Close")
	}))
	deadURL := deadServer.URL + "/dead"
	deadServer.Close()

	providerName := "testprovider_stream_retry"
	mock := &streamingMockProvider{
		name: providerName,
		info: &TorrentInfo{
			ID:     "torrent1",
			Status: "downloaded",
			Files: []File{
				{ID: 0, Path: "Movie.mkv", Bytes: 5, Selected: 1},
			},
			Links: []string{freshServer.URL + "/fresh"},
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Streaming: config.StreamingSettings{
			DebridProviders: []config.DebridProviderSettings{
				{Provider: providerName, APIKey: "test-key", Enabled: true},
			},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	RegisterProvider(providerName, func(string) Provider { return mock })

	p := NewStreamingProvider(mgr)
	p.setCachedURL(cacheKeyFor("torrent1", "0"), deadURL, "Movie.mkv", 0, 0)

	resp, err := p.Stream(context.Background(), streaming.Request{
		Path:        "/debrid/" + providerName + "/torrent1/file/0/Movie.mkv",
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-4",
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer resp.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q, want hello", string(body))
	}
	if got := atomic.LoadInt64(&mock.unrestrictCalls); got != 1 {
		t.Fatalf("UnrestrictLink calls = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&freshHits); got != 1 {
		t.Fatalf("fresh server hits = %d, want 1", got)
	}
	if cachedURL, _, _, _, found := p.getCachedURL(cacheKeyFor("torrent1", "0")); !found || cachedURL != freshServer.URL+"/fresh" {
		t.Fatalf("cached URL = %q found=%t, want fresh URL", cachedURL, found)
	}
}

func TestStreamingProviderReturnsSourceErrorAfterCachedURLRetryFails(t *testing.T) {
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadServer.URL + "/dead"
	deadServer.Close()

	providerName := "testprovider_stream_retry_failure"
	mock := &streamingMockProvider{
		name: providerName,
		info: &TorrentInfo{
			ID:     "torrent1",
			Status: "downloaded",
			Files: []File{
				{ID: 0, Path: "Movie.mkv", Bytes: 5, Selected: 1},
			},
			Links: []string{deadURL},
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Streaming: config.StreamingSettings{
			DebridProviders: []config.DebridProviderSettings{
				{Provider: providerName, APIKey: "test-key", Enabled: true},
			},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	RegisterProvider(providerName, func(string) Provider { return mock })

	p := NewStreamingProvider(mgr)
	p.setCachedURL(cacheKeyFor("torrent1", "0"), deadURL, "Movie.mkv", 0, 0)

	_, err := p.Stream(context.Background(), streaming.Request{
		Path:        "/debrid/" + providerName + "/torrent1/file/0/Movie.mkv",
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-4",
	})
	if err == nil {
		t.Fatal("Stream() error = nil, want SourceError")
	}
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("error type = %T, want SourceError; err=%v", err, err)
	}
	if !IsProviderUnavailableError(err) {
		t.Fatalf("IsProviderUnavailableError(%v) = false, want true", err)
	}
	if got := atomic.LoadInt64(&mock.unrestrictCalls); got != 1 {
		t.Fatalf("UnrestrictLink calls = %d, want 1", got)
	}
}

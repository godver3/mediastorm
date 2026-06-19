package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/playback"
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

type recordingProvider struct {
	data      []byte
	lastPath  string
	lastRange string
}

func (p *recordingProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	p.lastPath = req.Path
	p.lastRange = req.RangeHeader

	headers := make(http.Header)
	headers.Set("Content-Type", "video/x-matroska")
	headers.Set("Accept-Ranges", "bytes")
	headers.Set("Content-Length", "10")
	headers.Set("Content-Range", "bytes 0-9/10")

	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(p.data)),
		Headers:       headers,
		Status:        http.StatusPartialContent,
		ContentLength: int64(len(p.data)),
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

func TestVideoHandlerBypassesReadAheadPoolsForUsenetPath(t *testing.T) {
	provider := &recordingProvider{data: []byte("0123456789")}
	handler := NewVideoHandlerWithProvider(false, "", "", "", provider)

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path="+url.QueryEscape("/virtual/title.mkv"), nil)
	req.Header.Set("Range", "bytes=0-9")
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	res := rr.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusPartialContent)
	}
	if provider.lastPath != "/virtual/title.mkv" {
		t.Fatalf("provider path = %q, want %q", provider.lastPath, "/virtual/title.mkv")
	}
	if provider.lastRange != "bytes=0-9" {
		t.Fatalf("provider range = %q, want original range", provider.lastRange)
	}
}

func TestIsDebridStreamPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/debrid/realdebrid/torrent/file/1", true},
		{"debrid/realdebrid/torrent/file/1", true},
		{"/webdav/debrid/realdebrid/torrent/file/1", true},
		{"/virtual/title.mkv", false},
		{"localmedia:/movies/title.mkv", false},
	}

	for _, tt := range tests {
		if got := isDebridStreamPath(tt.path); got != tt.want {
			t.Fatalf("isDebridStreamPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestVideoHandlerInvalidatesPrequeueOnExternalURL404(t *testing.T) {
	// Simulate an expired AIOStreams proxy link returning "404 - Link expired".
	expired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "404 - Link expired. Re-search in your client.", http.StatusNotFound)
	}))
	defer expired.Close()

	streamPath := expired.URL + "/api/v1/proxy/abc123/stream.mkv"
	store := playback.NewPrequeueStore(30 * time.Minute)
	entry, _ := store.Create("title1", "Title", "user1", "series", 0,
		&models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1}, "prewarm")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = streamPath
	})

	prewarm := &invalidationPrewarmMock{}
	// Provider is irrelevant — external http URLs are proxied directly.
	handler := NewVideoHandlerWithProvider(false, "", "", "", failingProvider{err: streaming.ErrNotFound})
	handler.SetPrequeueStore(store)
	handler.SetPrewarmService(prewarm)

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path="+url.QueryEscape(streamPath), nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if rr.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Result().StatusCode, http.StatusNotFound)
	}
	if _, ok := store.Get(entry.ID); ok {
		t.Fatal("expected expired external-URL prequeue to be removed")
	}
	if len(prewarm.invalidated) != 1 || prewarm.invalidated[0] != entry.ID {
		t.Fatalf("expected prewarm invalidation for %s, got %#v", entry.ID, prewarm.invalidated)
	}
}

type failingProvider struct {
	err error
}

func (p failingProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	return nil, p.err
}

type invalidationPrewarmMock struct {
	invalidated []string
}

func (m *invalidationPrewarmMock) GetWarm(titleID, userID string) *playback.WarmRef {
	return nil
}

func (m *invalidationPrewarmMock) GetWarmScoped(titleID, userID, settingsScopeKey string) *playback.WarmRef {
	return nil
}

func (m *invalidationPrewarmMock) AdoptEntry(prequeueID string) {}

func (m *invalidationPrewarmMock) UpdateFromPrequeue(prequeueID string) {}

func (m *invalidationPrewarmMock) InvalidatePrequeue(prequeueID string) {
	m.invalidated = append(m.invalidated, prequeueID)
}

func TestVideoHandlerInvalidatesPrequeueOnRecoverableOpenFailure(t *testing.T) {
	streamPath := "/webdav/bad/title.mkv"
	cleanPath := "/bad/title.mkv"
	store := playback.NewPrequeueStore(30 * time.Minute)
	entry, _ := store.Create("title1", "Title", "user1", "series", 0,
		&models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1}, "prewarm")
	store.Update(entry.ID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = streamPath
	})

	prewarm := &invalidationPrewarmMock{}
	handler := NewVideoHandlerWithProvider(false, "", "", "", failingProvider{err: streaming.ErrNotFound})
	handler.SetPrequeueStore(store)
	handler.SetPrewarmService(prewarm)

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path="+url.QueryEscape(streamPath), nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if _, ok := store.Get(entry.ID); ok {
		t.Fatal("expected failed prequeue to be removed")
	}
	if len(prewarm.invalidated) != 1 || prewarm.invalidated[0] != entry.ID {
		t.Fatalf("expected prewarm invalidation for %s, got %#v", entry.ID, prewarm.invalidated)
	}
	if _, confirmed := handler.failures.confirmedRecent(cleanPath, streamFailureConfirmationTTL); !confirmed {
		t.Fatal("expected stream failure to be recorded")
	}
}

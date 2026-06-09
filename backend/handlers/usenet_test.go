package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/internal/httpheaders"
	"novastream/models"
)

type fakeUsenetService struct {
	response *models.NZBHealthCheck
	err      error
	called   bool
}

func (f *fakeUsenetService) CheckHealth(ctx context.Context, candidate models.NZBResult) (*models.NZBHealthCheck, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func TestUsenetHandlerCheckHealth(t *testing.T) {
	svc := &fakeUsenetService{
		response: &models.NZBHealthCheck{Status: "healthy", Healthy: true, CheckedSegments: 3, TotalSegments: 3},
	}

	handler := NewUsenetHandler(svc)

	payload := map[string]any{
		"result": models.NZBResult{DownloadURL: "https://example.com/file.nzb"},
	}
	buf, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/usenet/health", bytes.NewReader(buf))
	rec := httptest.NewRecorder()

	handler.CheckHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content type %q", got)
	}
	if !svc.called {
		t.Fatalf("expected service to be called")
	}

	var resp models.NZBHealthCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Healthy || resp.CheckedSegments != 3 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestUsenetHandlerBadRequest(t *testing.T) {
	handler := NewUsenetHandler(&fakeUsenetService{})

	req := httptest.NewRequest(http.MethodPost, "/api/usenet/health", bytes.NewBufferString("not-json"))
	rec := httptest.NewRecorder()

	handler.CheckHealth(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUsenetHandlerServiceError(t *testing.T) {
	svc := &fakeUsenetService{err: context.DeadlineExceeded}
	handler := NewUsenetHandler(svc)

	payload := map[string]any{
		"result": models.NZBResult{DownloadURL: "https://example.com/file.nzb"},
	}
	buf, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/usenet/health", bytes.NewReader(buf))
	rec := httptest.NewRecorder()

	handler.CheckHealth(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d", rec.Code)
	}
}

func TestUsenetTrackProberFetchNZBSetsDownloadHeaders(t *testing.T) {
	var receivedUserAgent string
	var receivedAccept string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		receivedAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Disposition", `attachment; filename="test.nzb"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb></nzb>`))
	}))
	defer server.Close()

	prober := &usenetTrackProber{httpClient: server.Client()}
	_, _, err := prober.fetchNZB(context.Background(), server.URL+"/test.nzb")
	if err != nil {
		t.Fatalf("fetchNZB returned error: %v", err)
	}
	if receivedUserAgent != httpheaders.NZBDownloadUserAgent {
		t.Fatalf("User-Agent = %q, want %q", receivedUserAgent, httpheaders.NZBDownloadUserAgent)
	}
	if receivedAccept == "" {
		t.Fatal("expected Accept header to be set")
	}
}

// fakeNZBImporter emulates the importer service for track-probe tests. On entry
// it cancels the original request context, then records whether the context it
// actually received is still live — proving the heavy work is detached from
// request cancellation (a client abort must not SIGKILL an in-progress probe).
type fakeNZBImporter struct {
	cancelRequest     func()
	ctxErrAfterCancel error
	called            bool
}

func (f *fakeNZBImporter) ProcessNZBImmediately(ctx context.Context, _ string, _ []byte) (string, error) {
	f.called = true
	if f.cancelRequest != nil {
		f.cancelRequest() // emulate the frontend aborting the request mid-probe
	}
	f.ctxErrAfterCancel = ctx.Err()
	// Return a non-video path so probe() stops before attempting real ffprobe.
	return "/some-directory", nil
}

// TestUsenetTrackProberDetachesFromCanceledRequest verifies that a client abort
// (canceled request context) does not propagate into the NZB processing / ffprobe
// stage. The NZB fetch runs first on the live request context; once we reach the
// heavy work, canceling the request must leave the detached context unaffected.
func TestUsenetTrackProberDetachesFromCanceledRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="test.nzb"`)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><nzb></nzb>`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	importer := &fakeNZBImporter{cancelRequest: cancel}
	prober := &usenetTrackProber{
		importer:   importer,
		httpClient: server.Client(),
	}

	_, _, _ = prober.probe(ctx, models.NZBResult{DownloadURL: server.URL + "/test.nzb"})

	if !importer.called {
		t.Fatal("ProcessNZBImmediately was never reached; fetchNZB likely failed")
	}
	if importer.ctxErrAfterCancel != nil {
		t.Fatalf("processing context was canceled (%v) when the request was aborted; probe work was not detached", importer.ctxErrAfterCancel)
	}
}

func TestUsenetHandlerProbeForTracksNoProber(t *testing.T) {
	// When probeForTracks=true but no prober is configured, the response should
	// indicate tracksProbed=true and include a trackProbeError.
	svc := &fakeUsenetService{
		response: &models.NZBHealthCheck{Status: "healthy", Healthy: true, CheckedSegments: 3, TotalSegments: 3},
	}
	handler := NewUsenetHandler(svc) // no ConfigureTrackProbing called

	payload := map[string]any{
		"result":         models.NZBResult{DownloadURL: "https://example.com/file.nzb"},
		"probeForTracks": true,
	}
	buf, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/usenet/health", bytes.NewReader(buf))
	rec := httptest.NewRecorder()

	handler.CheckHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp models.NZBHealthCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Healthy {
		t.Fatalf("expected healthy response")
	}
	if !resp.TracksProbed {
		t.Fatalf("expected tracksProbed=true")
	}
	if resp.TrackProbeError == "" {
		t.Fatalf("expected trackProbeError to be set when prober is not configured")
	}
}

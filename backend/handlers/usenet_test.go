package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildPerformanceMetrics(t *testing.T) {
	m := buildPerformanceMetrics()

	if m.Goroutines <= 0 {
		t.Errorf("expected goroutines > 0, got %d", m.Goroutines)
	}
	if m.HeapAlloc == 0 {
		t.Error("expected heapAlloc > 0")
	}
	if m.Sys == 0 {
		t.Error("expected sys > 0")
	}
	if m.UptimeSeconds < 0 {
		t.Errorf("expected uptimeSeconds >= 0, got %f", m.UptimeSeconds)
	}
	if m.NumCPU <= 0 {
		t.Errorf("expected numCPU > 0, got %d", m.NumCPU)
	}
	if m.GoVersion == "" {
		t.Error("expected goVersion to be non-empty")
	}
	if m.GOOS == "" {
		t.Error("expected goos to be non-empty")
	}
	if m.GOARCH == "" {
		t.Error("expected goarch to be non-empty")
	}
}

func TestGetPerformanceMetrics(t *testing.T) {
	h := &AdminUIHandler{}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/performance", nil)
	rr := httptest.NewRecorder()

	h.GetPerformanceMetrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var m PerformanceMetrics
	if err := json.NewDecoder(rr.Body).Decode(&m); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if m.Goroutines <= 0 {
		t.Errorf("expected goroutines > 0 in JSON, got %d", m.Goroutines)
	}
	if m.HeapAlloc == 0 {
		t.Error("expected heapAlloc > 0 in JSON")
	}
}

func TestCountOpenFDs(t *testing.T) {
	n := countOpenFDs()
	// On Linux/macOS we expect at least stdin/stdout/stderr (3).
	// In sandboxed environments /dev/fd may not be accessible, returning -1.
	if n == -1 {
		t.Skip("FD counting not available in this environment (sandboxed?)")
	}
	if n < 3 {
		t.Errorf("expected at least 3 open FDs, got %d", n)
	}
}

func TestCPUTrackerSample(t *testing.T) {
	tracker := &cpuTracker{}

	// First sample initialises baseline, returns 0
	pct := tracker.sample()
	if pct != 0 {
		t.Errorf("expected first sample to return 0, got %f", pct)
	}

	// Second sample should return a non-negative value
	pct = tracker.sample()
	if pct < 0 {
		t.Errorf("expected non-negative CPU%%, got %f", pct)
	}
}

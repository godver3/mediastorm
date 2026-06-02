package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeSpeedTestWritesRequestedBytes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/debug/speed-test?bytes=1048577", nil)
	rec := httptest.NewRecorder()

	ServeSpeedTest(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got := res.Header.Get("Content-Length"); got != "1048577" {
		t.Fatalf("Content-Length = %q, want 1048577", got)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 1048577 {
		t.Fatalf("body length = %d, want 1048577", len(body))
	}
}

func TestServeSpeedTestRejectsOversizedRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/debug/speed-test?bytes=2147483649", nil)
	rec := httptest.NewRecorder()

	ServeSpeedTest(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

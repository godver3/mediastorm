package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasePathHandler(t *testing.T) {
	// Inner handler that records the path it received
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Received-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name         string
		basePath     string
		requestPath  string
		wantPath     string
	}{
		{
			name:        "prefix stripped",
			basePath:    "/mediastorm",
			requestPath: "/mediastorm/api/settings",
			wantPath:    "/api/settings",
		},
		{
			name:        "prefix exact match becomes root",
			basePath:    "/mediastorm",
			requestPath: "/mediastorm",
			wantPath:    "/",
		},
		{
			name:        "no prefix passes through",
			basePath:    "/mediastorm",
			requestPath: "/api/settings",
			wantPath:    "/api/settings",
		},
		{
			name:        "trailing slash on basePath normalized",
			basePath:    "/mediastorm/",
			requestPath: "/mediastorm/health",
			wantPath:    "/health",
		},
		{
			name:        "empty basePath is no-op",
			basePath:    "",
			requestPath: "/api/settings",
			wantPath:    "/api/settings",
		},
		{
			name:        "slash-only basePath is no-op",
			basePath:    "/",
			requestPath: "/api/settings",
			wantPath:    "/api/settings",
		},
		{
			name:        "prefix with trailing slash on request",
			basePath:    "/mediastorm",
			requestPath: "/mediastorm/",
			wantPath:    "/",
		},
		{
			name:        "similar prefix not stripped",
			basePath:    "/media",
			requestPath: "/mediastorm/api",
			wantPath:    "/mediastorm/api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := BasePathHandler(tt.basePath, inner)
			req := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			got := rec.Header().Get("X-Received-Path")
			if got != tt.wantPath {
				t.Errorf("BasePathHandler(%q) with path %q: got %q, want %q",
					tt.basePath, tt.requestPath, got, tt.wantPath)
			}
		})
	}
}

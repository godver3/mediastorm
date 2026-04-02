package utils

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestBasePathHandlerPreservesQueryString(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Received-Path", r.URL.Path)
		w.Header().Set("X-Received-Query", r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
	})

	handler := BasePathHandler("/mediastorm", inner)
	req := httptest.NewRequest(http.MethodGet, "/mediastorm/health?check=full&via=pangolin", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Received-Path"); got != "/health" {
		t.Fatalf("received path = %q, want %q", got, "/health")
	}
	if got := rec.Header().Get("X-Received-Query"); got != "check=full&via=pangolin" {
		t.Fatalf("received query = %q, want %q", got, "check=full&via=pangolin")
	}
}

func TestReverseProxyPathPrefixHealth(t *testing.T) {
	backendHandler := BasePathHandler("/mediastorm", NewRouter())
	backendURL, err := url.Parse("http://mediastorm")
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	proxy.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		backendHandler.ServeHTTP(rec, req)
		return rec.Result(), nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy/mediastorm/health", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("body = %q, want %q", string(body), `{"status":"ok"}`)
	}
}

func TestReverseProxyRootMountHealth(t *testing.T) {
	backendHandler := NewRouter()
	backendURL, err := url.Parse("http://mediastorm")
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	proxy.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		backendHandler.ServeHTTP(rec, req)
		return rec.Result(), nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://proxy/health", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("body = %q, want %q", string(body), `{"status":"ok"}`)
	}
}

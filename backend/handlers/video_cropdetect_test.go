package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCropDetect_MissingPath(t *testing.T) {
	h := &VideoHandler{
		metadataCache:   make(map[string]*cachedMetadataEntry),
		cropDetectCache: make(map[string]*cropDetectCacheEntry),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/video/cropdetect", nil)
	w := httptest.NewRecorder()
	h.CropDetect(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCropDetect_Options(t *testing.T) {
	h := &VideoHandler{
		metadataCache:   make(map[string]*cachedMetadataEntry),
		cropDetectCache: make(map[string]*cropDetectCacheEntry),
	}

	req := httptest.NewRequest(http.MethodOptions, "/api/video/cropdetect?path=/test.mkv", nil)
	w := httptest.NewRecorder()
	h.CropDetect(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for OPTIONS, got %d", w.Code)
	}
}

func TestCropDetect_CacheHit(t *testing.T) {
	h := &VideoHandler{
		metadataCache:   make(map[string]*cachedMetadataEntry),
		cropDetectCache: make(map[string]*cropDetectCacheEntry),
	}

	// Pre-populate cache
	h.cropDetectCacheMu.Lock()
	h.cropDetectCache["/test.mkv"] = &cropDetectCacheEntry{
		result:    cropDetectResponse{LetterboxTop: 0.125, LetterboxBottom: 0.125},
		expiresAt: time.Now().Add(1 * time.Hour),
	}
	h.cropDetectCacheMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/video/cropdetect?path=/test.mkv", nil)
	w := httptest.NewRecorder()
	h.CropDetect(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp cropDetectResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.LetterboxTop != 0.125 {
		t.Errorf("expected letterboxTop=0.125, got %f", resp.LetterboxTop)
	}
	if resp.LetterboxBottom != 0.125 {
		t.Errorf("expected letterboxBottom=0.125, got %f", resp.LetterboxBottom)
	}
}

func TestCropDetect_NoProvider(t *testing.T) {
	h := &VideoHandler{
		metadataCache:   make(map[string]*cachedMetadataEntry),
		cropDetectCache: make(map[string]*cropDetectCacheEntry),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/video/cropdetect?path=/test.mkv", nil)
	w := httptest.NewRecorder()
	h.CropDetect(w, req)

	// Should return 200 with 0/0 when no provider is available
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp cropDetectResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.LetterboxTop != 0 || resp.LetterboxBottom != 0 {
		t.Errorf("expected 0/0, got %f/%f", resp.LetterboxTop, resp.LetterboxBottom)
	}
}

func TestCropDetect_WebDAVPathCleaning(t *testing.T) {
	h := &VideoHandler{
		metadataCache:   make(map[string]*cachedMetadataEntry),
		cropDetectCache: make(map[string]*cropDetectCacheEntry),
	}

	// Pre-populate cache with cleaned path
	h.cropDetectCacheMu.Lock()
	h.cropDetectCache["/movies/test.mkv"] = &cropDetectCacheEntry{
		result:    cropDetectResponse{LetterboxTop: 0.1, LetterboxBottom: 0.1},
		expiresAt: time.Now().Add(1 * time.Hour),
	}
	h.cropDetectCacheMu.Unlock()

	// Request with /webdav/ prefix
	req := httptest.NewRequest(http.MethodGet, "/api/video/cropdetect?path=/webdav/movies/test.mkv", nil)
	w := httptest.NewRecorder()
	h.CropDetect(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp cropDetectResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.LetterboxTop != 0.1 {
		t.Errorf("expected letterboxTop=0.1, got %f", resp.LetterboxTop)
	}
}

func TestCropdetectRegex(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "standard cropdetect output",
			input: "[Parsed_cropdetect_0 @ 0x...] x1:0 x2:1919 y1:138 y2:937 w:1920 h:800 x:0 y:140 pts:12345 t:1.234 crop=1920:800:0:140",
			want:  []string{"1920", "800", "0", "140"},
		},
		{
			name:  "no crop",
			input: "no crop here",
			want:  nil,
		},
		{
			name:  "4K content with letterbox",
			input: "crop=3840:1600:0:280",
			want:  []string{"3840", "1600", "0", "280"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := cropdetectRegex.FindStringSubmatch(tt.input)
			if tt.want == nil {
				if m != nil {
					t.Errorf("expected no match, got %v", m)
				}
				return
			}
			if m == nil {
				t.Fatalf("expected match, got nil")
			}
			for i, want := range tt.want {
				if m[i+1] != want {
					t.Errorf("group %d: expected %q, got %q", i+1, want, m[i+1])
				}
			}
		})
	}
}

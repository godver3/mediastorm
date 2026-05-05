package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/services/streaming"
)

// --- generateSessionID tests ---

func TestGenerateSessionID(t *testing.T) {
	// Generate multiple IDs and verify uniqueness
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateSessionID()
		if id == "" {
			t.Error("generateSessionID returned empty string")
		}
		if ids[id] {
			t.Errorf("generateSessionID returned duplicate ID: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateSessionID_Format(t *testing.T) {
	id := generateSessionID()
	// Should be 32 hex characters (16 bytes -> 32 hex chars)
	if len(id) != 32 {
		// Could be fallback format "session-<timestamp>"
		if !strings.HasPrefix(id, "session-") {
			t.Errorf("generateSessionID format unexpected: %s (len=%d)", id, len(id))
		}
	}
}

// --- isMatroskaPath tests ---

func TestIsMatroskaPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{name: "mkv extension", path: "/path/to/video.mkv", expected: true},
		{name: "MKV uppercase", path: "/path/to/video.MKV", expected: true},
		{name: "mk3d extension", path: "movie.mk3d", expected: true},
		{name: "webm extension", path: "clip.webm", expected: true},
		{name: "mka audio", path: "audio.mka", expected: true},
		{name: "mp4 not matroska", path: "video.mp4", expected: false},
		{name: "avi not matroska", path: "video.avi", expected: false},
		{name: "ts not matroska", path: "video.ts", expected: false},
		{name: "no extension", path: "noextension", expected: false},
		{name: "empty path", path: "", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isMatroskaPath(tc.path)
			if result != tc.expected {
				t.Errorf("isMatroskaPath(%q) = %v, want %v", tc.path, result, tc.expected)
			}
		})
	}
}

// --- isTSLikePath tests ---

func TestIsTSLikePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{name: "ts extension", path: "video.ts", expected: true},
		{name: "TS uppercase", path: "video.TS", expected: true},
		{name: "m2ts extension", path: "bluray.m2ts", expected: true},
		{name: "mts extension", path: "camcorder.mts", expected: true},
		{name: "mpg extension", path: "dvd.mpg", expected: true},
		{name: "mpeg extension", path: "video.mpeg", expected: true},
		{name: "vob extension", path: "VIDEO_TS/VTS_01_1.vob", expected: true},
		{name: "mkv not ts", path: "video.mkv", expected: false},
		{name: "mp4 not ts", path: "video.mp4", expected: false},
		{name: "avi not ts", path: "video.avi", expected: false},
		{name: "no extension", path: "noext", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isTSLikePath(tc.path)
			if result != tc.expected {
				t.Errorf("isTSLikePath(%q) = %v, want %v", tc.path, result, tc.expected)
			}
		})
	}
}

// --- supportsPipeRange tests ---

func TestSupportsPipeRange(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		// Matroska files support pipe range
		{name: "mkv supports", path: "video.mkv", expected: true},
		{name: "webm supports", path: "video.webm", expected: true},
		// TS-like files support pipe range
		{name: "ts supports", path: "video.ts", expected: true},
		{name: "m2ts supports", path: "video.m2ts", expected: true},
		{name: "mpg supports", path: "video.mpg", expected: true},
		// Other formats don't
		{name: "mp4 no support", path: "video.mp4", expected: false},
		{name: "avi no support", path: "video.avi", expected: false},
		{name: "mov no support", path: "video.mov", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := supportsPipeRange(tc.path)
			if result != tc.expected {
				t.Errorf("supportsPipeRange(%q) = %v, want %v", tc.path, result, tc.expected)
			}
		})
	}
}

// --- normalizeWebDAVPrefix tests ---

func TestNormalizeWebDAVPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty string", input: "", expected: ""},
		{name: "whitespace only", input: "   ", expected: ""},
		{name: "slash only", input: "/", expected: "/"},
		{name: "webdav without slash", input: "webdav", expected: "/webdav"},
		{name: "webdav with leading slash", input: "/webdav", expected: "/webdav"},
		{name: "webdav with trailing slash", input: "/webdav/", expected: "/webdav"},
		{name: "webdav with both slashes", input: "/webdav/", expected: "/webdav"},
		{name: "multiple trailing slashes", input: "/webdav///", expected: "/webdav"},
		{name: "nested path", input: "/api/webdav", expected: "/api/webdav"},
		{name: "nested path trailing", input: "/api/webdav/", expected: "/api/webdav"},
		{name: "whitespace around", input: "  /webdav  ", expected: "/webdav"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeWebDAVPrefix(tc.input)
			if result != tc.expected {
				t.Errorf("normalizeWebDAVPrefix(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// --- HLSManager tests ---

func TestNewHLSManager(t *testing.T) {
	tmpDir := t.TempDir()

	manager := NewHLSManager(tmpDir, "/usr/bin/ffmpeg", "/usr/bin/ffprobe", nil)
	if manager == nil {
		t.Fatal("NewHLSManager returned nil")
	}
	defer manager.Shutdown()

	if manager.baseDir != tmpDir {
		t.Errorf("baseDir = %q, want %q", manager.baseDir, tmpDir)
	}
	if manager.sessions == nil {
		t.Error("sessions map is nil")
	}
	if manager.probeCache == nil {
		t.Error("probeCache map is nil")
	}
}

func TestNewHLSManager_DefaultBaseDir(t *testing.T) {
	manager := NewHLSManager("", "/usr/bin/ffmpeg", "/usr/bin/ffprobe", nil)
	if manager == nil {
		t.Fatal("NewHLSManager returned nil")
	}
	defer manager.Shutdown()

	expectedBase := filepath.Join("/tmp", "novastream-hls")
	if manager.baseDir != expectedBase {
		t.Errorf("default baseDir = %q, want %q", manager.baseDir, expectedBase)
	}
}

func TestHLSManager_ConfigureLocalWebDAVAccess(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// Configure access
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "user", "pass")

	// Verify configuration
	manager.localAccessMu.RLock()
	baseURL := manager.localWebDAVBaseURL
	prefix := manager.localWebDAVPrefix
	manager.localAccessMu.RUnlock()

	if !strings.Contains(baseURL, "user:pass@localhost:7777") {
		t.Errorf("baseURL should contain credentials, got %q", baseURL)
	}
	if prefix != "/webdav" {
		t.Errorf("prefix = %q, want /webdav", prefix)
	}
}

func TestHLSManager_ConfigureLocalWebDAVAccess_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// First configure, then clear
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "", "")
	manager.ConfigureLocalWebDAVAccess("", "", "", "")

	manager.localAccessMu.RLock()
	baseURL := manager.localWebDAVBaseURL
	prefix := manager.localWebDAVPrefix
	manager.localAccessMu.RUnlock()

	if baseURL != "" {
		t.Errorf("baseURL should be empty after clearing, got %q", baseURL)
	}
	if prefix != "" {
		t.Errorf("prefix should be empty after clearing, got %q", prefix)
	}
}

func TestHLSManager_ConfigureLocalWebDAVAccess_NilManager(t *testing.T) {
	// Should not panic
	var manager *HLSManager
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "", "")
}

func TestHLSManager_GetSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	session, ok := manager.GetSession("nonexistent")
	if ok {
		t.Error("expected session not found")
	}
	if session != nil {
		t.Error("expected nil session for not found")
	}
}

func TestHLSManager_Shutdown(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)

	// Shutdown should not panic
	manager.Shutdown()
	// Note: Shutdown is NOT idempotent - calling twice will panic
}

// --- HLSSession structure tests ---

func TestHLSSession_Fields(t *testing.T) {
	session := &HLSSession{
		ID:           "test-session-id",
		Path:         "/path/to/video.mkv",
		OriginalPath: "/original/path.mkv",
		OutputDir:    "/tmp/hls/test-session",
		CreatedAt:    time.Now(),
		LastAccess:   time.Now(),
		HasDV:        true,
		DVProfile:    "dvhe.08.06",
		HasHDR:       true,
	}

	if session.ID != "test-session-id" {
		t.Errorf("ID = %q, want test-session-id", session.ID)
	}
	if !session.HasDV {
		t.Error("HasDV should be true")
	}
	if session.DVProfile != "dvhe.08.06" {
		t.Errorf("DVProfile = %q, want dvhe.08.06", session.DVProfile)
	}
	if !session.HasHDR {
		t.Error("HasHDR should be true")
	}
}

// --- debugReader tests ---

func TestDebugReader(t *testing.T) {
	data := []byte("test data for debug reader")
	reader := bytes.NewReader(data)
	debugR := newDebugReader(reader, "test-session")

	// Read the data
	buf := make([]byte, len(data))
	n, err := debugR.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read error: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
	if !bytes.Equal(buf[:n], data) {
		t.Errorf("Data mismatch: got %q, want %q", buf[:n], data)
	}

	// Verify bytesRead is tracked
	if debugR.bytesRead != int64(len(data)) {
		t.Errorf("bytesRead = %d, want %d", debugR.bytesRead, len(data))
	}
}

func TestDebugReader_EOF(t *testing.T) {
	data := []byte("small")
	reader := bytes.NewReader(data)
	debugR := newDebugReader(reader, "test-session")

	// Read all data
	buf := make([]byte, 100)
	n, err := debugR.Read(buf)
	if n != len(data) {
		t.Errorf("first read: got %d bytes, want %d", n, len(data))
	}

	// Next read should return EOF
	n2, err := debugR.Read(buf)
	if n2 != 0 || err != io.EOF {
		t.Errorf("second read: got n=%d, err=%v; want n=0, err=EOF", n2, err)
	}

	// Verify closed flag is set
	if !debugR.closed.Load() {
		t.Error("closed flag should be true after EOF")
	}
}

// --- throttledReader tests ---

func TestNewThrottledReader(t *testing.T) {
	data := []byte("test data")
	reader := bytes.NewReader(data)
	session := &HLSSession{
		ID:                  "test",
		OutputDir:           t.TempDir(),
		MaxSegmentRequested: -1, // No segments requested yet
	}

	throttled := newThrottledReader(reader, session)
	if throttled == nil {
		t.Fatal("newThrottledReader returned nil")
	}

	// Should read normally when no segments requested
	buf := make([]byte, len(data))
	n, err := throttled.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read error: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
}

// --- Mock streaming provider for HLS tests ---

type hlsTestProvider struct {
	data    []byte
	headers http.Header
}

func (p *hlsTestProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	headers := p.headers
	if headers == nil {
		headers = make(http.Header)
		headers.Set("Content-Type", "video/x-matroska")
	}
	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(p.data)),
		Headers:       headers,
		Status:        http.StatusOK,
		ContentLength: int64(len(p.data)),
	}, nil
}

// --- HLSManager HTTP handler tests ---

func TestHLSManager_KeepAlive_SessionNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodPost, "/api/hls/keep-alive", nil)
	rr := httptest.NewRecorder()

	manager.KeepAlive(rr, req, "nonexistent-session")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHLSManager_GetSessionStatus_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/api/hls/status", nil)
	rr := httptest.NewRecorder()

	manager.GetSessionStatus(rr, req, "nonexistent-session")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHLSManager_ServePlaylist_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/api/hls/playlist.m3u8", nil)
	rr := httptest.NewRecorder()

	manager.ServePlaylist(rr, req, "nonexistent-session")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHLSManager_ServeSegment_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/api/hls/segment0.m4s", nil)
	rr := httptest.NewRecorder()

	manager.ServeSegment(rr, req, "nonexistent-session", "segment0.m4s")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHLSManager_ServeSubtitles_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/api/hls/subtitles.vtt", nil)
	rr := httptest.NewRecorder()

	manager.ServeSubtitles(rr, req, "nonexistent-session")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHLSManager_ServeMasterPlaylist(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	sessionID := "master-test-session"
	outputDir := filepath.Join(tmpDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	session := &HLSSession{
		ID:                 sessionID,
		OutputDir:          outputDir,
		CreatedAt:          time.Now(),
		LastAccess:         time.Now(),
		CastMode:           true,
		SubtitleTrackIndex: 11,
		ProbeData: &UnifiedProbeResult{
			SubtitleStreams: []subtitleStreamInfo{
				{Index: 11, Codec: "subrip", Language: "eng", Title: "English", IsForced: false},
				{Index: 12, Codec: "ass", Language: "spa", Title: "Spanish", IsForced: true},
				{Index: 13, Codec: "hdmv_pgs_subtitle", Language: "jpn", Title: "PGS", IsForced: false},
			},
		},
	}

	manager.mu.Lock()
	manager.sessions[sessionID] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/video/hls/%s/master.m3u8?token=test-token", sessionID), nil)
	rr := httptest.NewRecorder()

	manager.ServeMasterPlaylist(rr, req, sessionID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Fatalf("master playlist missing subtitle rendition: %s", body)
	}
	if !strings.Contains(body, `URI="subtitle-11.m3u8?token=test-token"`) {
		t.Fatalf("expected selected subtitle URI with token rewrite, got: %s", body)
	}
	if !strings.Contains(body, `DEFAULT=YES`) {
		t.Fatalf("expected selected subtitle to be default, got: %s", body)
	}
	if !strings.Contains(body, `FORCED=YES`) {
		t.Fatalf("expected forced subtitle metadata, got: %s", body)
	}
	if strings.Contains(body, "subtitle-13") {
		t.Fatalf("bitmap subtitle should not be advertised in master playlist: %s", body)
	}
	if !strings.Contains(body, "stream.m3u8?token=test-token") {
		t.Fatalf("expected stream playlist token rewrite, got: %s", body)
	}
}

func TestHLSManager_ServeSubtitlePlaylist(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	sessionID := "subtitle-playlist-test"
	outputDir := filepath.Join(tmpDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	session := &HLSSession{
		ID:         sessionID,
		OutputDir:  outputDir,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		Duration:   120,
		ProbeData: &UnifiedProbeResult{
			SubtitleStreams: []subtitleStreamInfo{
				{Index: 11, Codec: "subrip", Language: "eng", Title: "English"},
			},
		},
	}

	manager.mu.Lock()
	manager.sessions[sessionID] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/video/hls/%s/subtitle-11.m3u8?token=test-token", sessionID), nil)
	rr := httptest.NewRecorder()

	manager.ServeSubtitlePlaylist(rr, req, sessionID, 11)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := rr.Body.String()
	if strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("subtitle playlist should stay refreshable while VTT is growing, got: %s", body)
	}
	if !strings.Contains(body, "subtitles-11.vtt?reload=") || !strings.Contains(body, "&token=test-token") {
		t.Fatalf("expected subtitle segment token rewrite, got: %s", body)
	}
	if !strings.Contains(body, "#EXTINF:120.000,") {
		t.Fatalf("expected full-duration subtitle segment, got: %s", body)
	}
}

func TestHLSManager_Seek_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	req := httptest.NewRequest(http.MethodPost, "/api/hls/seek?position=60", nil)
	rr := httptest.NewRecorder()

	manager.Seek(rr, req, "nonexistent-session")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

// --- Session directory cleanup tests ---

func TestHLSManager_CleanupSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// Should not panic when cleaning up non-existent session
	manager.CleanupSession("nonexistent-session")
}

func TestStopHLSSession_CleanupRemovesSession(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// Manually insert a session
	sessionID := "stop-test-session"
	outputDir := filepath.Join(tmpDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &HLSSession{
		ID:         sessionID,
		OutputDir:  outputDir,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		IsLive:     true,
		Cancel:     cancel,
	}
	_ = ctx

	manager.mu.Lock()
	manager.sessions[sessionID] = session
	manager.mu.Unlock()

	// Verify session exists and is counted
	_, exists := manager.GetSession(sessionID)
	if !exists {
		t.Fatal("session should exist before cleanup")
	}

	// Clean up the session (simulating what StopHLSSession does)
	manager.CleanupSession(sessionID)

	// Verify session is removed
	_, exists = manager.GetSession(sessionID)
	if exists {
		t.Error("session should be removed after cleanup")
	}

	// Verify output directory is removed
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Error("output directory should be removed after cleanup")
	}
}

// --- buildLocalWebDAVURL tests ---

func TestHLSManager_BuildLocalWebDAVURLFromPath(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	// Without configuration, should return empty
	url, ok := manager.buildLocalWebDAVURLFromPath("/test/path.mkv")
	if ok {
		t.Error("expected ok=false without configuration")
	}
	if url != "" {
		t.Errorf("expected empty URL without configuration, got %q", url)
	}

	// Configure WebDAV access
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "", "")

	// Now should return a URL
	url, ok = manager.buildLocalWebDAVURLFromPath("/test/path.mkv")
	if !ok {
		t.Error("expected ok=true with configuration")
	}
	if !strings.Contains(url, "localhost:7777") {
		t.Errorf("URL should contain host, got %q", url)
	}
	if !strings.Contains(url, "/webdav") {
		t.Errorf("URL should contain webdav prefix, got %q", url)
	}
	if !strings.Contains(url, "/test/path.mkv") {
		t.Errorf("URL should contain path, got %q", url)
	}
}

// --- findHighestSegmentNumber tests ---

func TestHLSManager_FindHighestSegmentNumber(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	sessionDir := filepath.Join(tmpDir, "test-session")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	session := &HLSSession{
		ID:        "test-session",
		OutputDir: sessionDir,
	}

	// No segments - should return -1
	result := manager.findHighestSegmentNumber(session)
	if result != -1 {
		t.Errorf("expected -1 with no segments, got %d", result)
	}

	// Create some segment files
	for _, name := range []string{"segment0.m4s", "segment1.m4s", "segment5.m4s", "segment10.m4s"} {
		if err := os.WriteFile(filepath.Join(sessionDir, name), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	result = manager.findHighestSegmentNumber(session)
	if result != 10 {
		t.Errorf("expected 10 as highest segment, got %d", result)
	}
}

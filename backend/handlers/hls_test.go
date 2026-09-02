package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/streaming"
)

func TestCreateSessionRejectsElfHostedPlaceholderRedirect(t *testing.T) {
	originalClient := hlsRedirectHTTPClient
	defer func() { hlsRedirectHTTPClient = originalClient }()

	hlsRedirectHTTPClient = &http.Client{
		Transport: prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			finalReq := r.Clone(r.Context())
			finalReq.URL, _ = url.Parse("https://slate.elfhosted.com/cache/link-expired.mp4")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("placeholder")),
				Header:     make(http.Header),
				Request:    finalReq,
			}, nil
		}),
	}

	manager := NewHLSManager(t.TempDir(), "", "", nil)
	defer manager.Shutdown()

	_, err := manager.CreateSession(
		context.Background(),
		"https://comet.elfhosted.com/playback/expired",
		"https://comet.elfhosted.com/playback/expired",
		false, "", false, false, 0, 0, -1, -1, "", "", "", false, "", "", 0, "",
	)
	if !errors.Is(err, errExternalStreamPlaceholder) {
		t.Fatalf("CreateSession error = %v, want external placeholder error", err)
	}
}

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

func TestThrottlingProxyUsesConfiguredExternalWebDAVAuth(t *testing.T) {
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("webdav-user:webdav-pass"))
	var sawAuthenticatedGet atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Authorization") == expectedAuth {
			sawAuthenticatedGet.Store(true)
		}
		if r.Header.Get("Authorization") != expectedAuth {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Length", "5")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("video"))
		}
	}))
	defer upstream.Close()

	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:           "AltMount",
		Type:           "altmount",
		Enabled:        true,
		WebDAVBaseURL:  upstream.URL + "/webdav",
		WebDAVUsername: "webdav-user",
		WebDAVPassword: "webdav-pass",
	}}
	manager := NewHLSManager(t.TempDir(), "", "", nil)
	manager.SetConfigManager(staticVideoConfigProvider{settings: settings})
	session := &HLSSession{ID: "test-hls-auth", OutputDir: t.TempDir()}

	proxy, localURL, err := newThrottlingProxy(upstream.URL+"/webdav/movie.mkv", session, manager.applyExternalUsenetWebDAVAuth)
	if err != nil {
		t.Fatalf("newThrottlingProxy returned error: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get(localURL)
	if err != nil {
		t.Fatalf("proxy GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy GET status = %d, want 200", resp.StatusCode)
	}
	if !sawAuthenticatedGet.Load() {
		t.Fatalf("upstream GET did not receive configured WebDAV auth")
	}
}

func TestFFmpegHTTPProxyArgs(t *testing.T) {
	tests := []struct {
		name     string
		proxyURL string
		expected []string
	}{
		{name: "empty", proxyURL: "", expected: nil},
		{name: "trimmed http", proxyURL: " http://gluetun:8888 ", expected: []string{"-http_proxy", "http://gluetun:8888"}},
		{name: "https", proxyURL: "https://proxy.example:8443", expected: []string{"-http_proxy", "https://proxy.example:8443"}},
		{name: "socks unsupported by ffmpeg http proxy", proxyURL: "socks5://gluetun:1080", expected: nil},
		{name: "invalid", proxyURL: "://bad", expected: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ffmpegHTTPProxyArgs(tc.proxyURL)
			if strings.Join(got, "\x00") != strings.Join(tc.expected, "\x00") {
				t.Fatalf("ffmpegHTTPProxyArgs(%q) = %#v, want %#v", tc.proxyURL, got, tc.expected)
			}
		})
	}
}

func TestIsHTTPDirectURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "http", input: "http://example.com/video.ts", expected: true},
		{name: "https with whitespace", input: " https://example.com/video.ts ", expected: true},
		{name: "relative recording path", input: "cache/recordings/default/show.ts", expected: false},
		{name: "absolute local path", input: "/var/lib/recordings/show.ts", expected: false},
		{name: "file URL", input: "file:///var/lib/recordings/show.ts", expected: false},
		{name: "missing host", input: "https:///video.ts", expected: false},
		{name: "invalid", input: "://cache/recordings/show.ts", expected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHTTPDirectURL(tc.input); got != tc.expected {
				t.Fatalf("isHTTPDirectURL(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
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

// blockingReader stays blocked until released, standing in for an upstream that
// has accepted the connection and then stopped delivering.
type blockingReader struct {
	release chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

// A stalled upstream read has to be visible to the starvation monitor, otherwise a
// cast session starves against a degraded source with nothing able to act on it.
func TestThrottledReaderExposesBlockedUpstreamRead(t *testing.T) {
	blocking := &blockingReader{release: make(chan struct{})}
	throttled := newThrottledReader(blocking, &HLSSession{
		ID:                  "starved",
		OutputDir:           t.TempDir(),
		MaxSegmentRequested: -1,
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8)
		_, _ = throttled.Read(buf)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for throttled.upstream.startedAt() == 0 {
		if time.Now().After(deadline) {
			close(blocking.release)
			<-done
			t.Fatal("blocked upstream read was never observable to the starvation monitor")
		}
		time.Sleep(time.Millisecond)
	}

	if blocked := throttled.upstream.blockedFor(time.Now()); blocked <= 0 {
		t.Fatalf("blockedFor = %v, want a positive duration while the read is stalled", blocked)
	}

	close(blocking.release)
	<-done

	if started := throttled.upstream.startedAt(); started != 0 {
		t.Fatalf("watch still armed after the read returned: startedAt = %d", started)
	}
}

// The failure that motivated this: a CDN that keeps the connection alive and
// trickles. Every individual read returns promptly, so a blocked-read watch never
// fires, while the transcode starves anyway.
func TestSlowUpstreamTrickleIsReportedAsSlowNotStalled(t *testing.T) {
	var gotActual, gotRequired int64
	calls := 0

	throttled := newThrottledReader(bytes.NewReader(nil), &HLSSession{
		ID:        "trickle",
		OutputDir: t.TempDir(),
	})
	throttled.requiredBps = 20_000_000 // 20Mbps source
	throttled.onSlowUpstream = func(actual, required int64) {
		calls++
		gotActual, gotRequired = actual, required
	}

	// Two consecutive deficient windows: 200KB per second of active reading is
	// 1.6Mbps, the rate measured on the session that prompted this.
	for range hlsUpstreamLowSamplesNeeded {
		throttled.windowStart = time.Now().Add(-2 * hlsUpstreamSampleWindow)
		throttled.sampleUpstreamRate(200_000, time.Second)
	}

	if calls != 1 {
		t.Fatalf("slow upstream callbacks = %d, want 1", calls)
	}
	if gotRequired != 20_000_000 {
		t.Errorf("required = %d, want 20000000", gotRequired)
	}
	if gotActual > 2_000_000 {
		t.Errorf("actual = %d bps, want the measured trickle (~1.6Mbps)", gotActual)
	}
}

// A single deficient window is not enough; a momentary dip must not hand the
// session to another source.
func TestSingleSlowWindowDoesNotRequestMigration(t *testing.T) {
	calls := 0
	throttled := newThrottledReader(bytes.NewReader(nil), &HLSSession{ID: "dip", OutputDir: t.TempDir()})
	throttled.requiredBps = 20_000_000
	throttled.onSlowUpstream = func(int64, int64) { calls++ }

	throttled.windowStart = time.Now().Add(-2 * hlsUpstreamSampleWindow)
	throttled.sampleUpstreamRate(200_000, time.Second)

	if calls != 0 {
		t.Fatalf("slow upstream callbacks = %d, want 0 after one window", calls)
	}
}

// The throttle deliberately stalls reads for many seconds when the transcode runs
// ahead. Because only time inside the read counts, a healthy source stays healthy:
// measuring against wall time here would report every buffered session as slow.
func TestThrottledButFastUpstreamIsNotReportedSlow(t *testing.T) {
	calls := 0
	throttled := newThrottledReader(bytes.NewReader(nil), &HLSSession{ID: "ahead", OutputDir: t.TempDir()})
	throttled.requiredBps = 20_000_000
	throttled.onSlowUpstream = func(int64, int64) { calls++ }

	for range hlsUpstreamLowSamplesNeeded + 1 {
		// A whole minute of wall clock elapsed, nearly all of it throttle sleep,
		// but the source handed over 8MB in the one second it was actually read.
		throttled.windowStart = time.Now().Add(-60 * time.Second)
		throttled.sampleUpstreamRate(8_000_000, time.Second)
	}

	if calls != 0 {
		t.Fatalf("slow upstream callbacks = %d, want 0 for a throttled but fast source", calls)
	}
}

// Without a Content-Length or a probed duration there is no rate to compare
// against, and guessing one would migrate sessions on no evidence.
func TestSourceRequiredBpsNeedsLengthAndDuration(t *testing.T) {
	withDuration := &HLSSession{Duration: 1000}
	if got := hlsSourceRequiredBps(2_500_000_000, withDuration); got != 20_000_000 {
		t.Errorf("requiredBps = %d, want 20000000", got)
	}
	if got := hlsSourceRequiredBps(0, withDuration); got != 0 {
		t.Errorf("requiredBps without a length = %d, want 0", got)
	}
	if got := hlsSourceRequiredBps(2_500_000_000, &HLSSession{}); got != 0 {
		t.Errorf("requiredBps without a duration = %d, want 0", got)
	}
}

// A seek is served as a partial response, so the slice length must not be mistaken
// for the file length: that would understate the required rate on every seek.
func TestUpstreamTotalBytesPrefersContentRangeTotal(t *testing.T) {
	ranged := &http.Response{
		ContentLength: 1_000_000,
		Header:        http.Header{"Content-Range": []string{"bytes 2000000-2999999/2500000000"}},
	}
	if got := hlsUpstreamTotalBytes(ranged); got != 2_500_000_000 {
		t.Errorf("total = %d, want the full file size 2500000000", got)
	}

	whole := &http.Response{ContentLength: 2_500_000_000, Header: http.Header{}}
	if got := hlsUpstreamTotalBytes(whole); got != 2_500_000_000 {
		t.Errorf("total = %d, want 2500000000", got)
	}

	unparseable := &http.Response{
		ContentLength: 1_000_000,
		Header:        http.Header{"Content-Range": []string{"bytes 0-99/*"}},
	}
	if got := hlsUpstreamTotalBytes(unparseable); got != 1_000_000 {
		t.Errorf("total = %d, want the Content-Length fallback", got)
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

type remoteProbeTestProvider struct {
	lastRequest streaming.Request
}

func (p *remoteProbeTestProvider) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	p.lastRequest = req
	return &streaming.Response{
		Body:          io.NopCloser(strings.NewReader("remote media bytes")),
		Headers:       make(http.Header),
		Status:        http.StatusPartialContent,
		ContentLength: 18,
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

func TestHLSManager_ServePlaylist_WaitsForFirstSegment(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	sessionID := "vod-empty-playlist-session"
	outputDir := filepath.Join(tmpDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	playlistPath := filepath.Join(outputDir, "stream.m3u8")
	emptyPlaylist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:EVENT\n"
	if err := os.WriteFile(playlistPath, []byte(emptyPlaylist), 0644); err != nil {
		t.Fatal(err)
	}

	session := &HLSSession{
		ID:         sessionID,
		OutputDir:  outputDir,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
	}
	manager.mu.Lock()
	manager.sessions[sessionID] = session
	manager.mu.Unlock()

	// Write the first segment entry shortly after the request starts. The
	// handler must hold the response until the playlist lists a media segment —
	// native HLS clients (Safari) stall permanently on an initial empty playlist.
	go func() {
		time.Sleep(150 * time.Millisecond)
		withSegment := emptyPlaylist + "#EXTINF:2.000000,\nsegment0.ts\n"
		os.WriteFile(playlistPath, []byte(withSegment), 0644)
	}()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/video/hls/%s/stream.m3u8", sessionID), nil)
	rr := httptest.NewRecorder()
	manager.ServePlaylist(rr, req, sessionID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (body: %s)", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "segment0.ts") {
		t.Fatalf("expected playlist to include first media segment, got: %s", rr.Body.String())
	}
}

func TestHLSManager_ServePlaylist_FailsPromptlyWhenLiveTranscodeStopsBeforePlaylist(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	sessionID := "failed-live-playlist-session"
	outputDir := filepath.Join(tmpDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	session := &HLSSession{
		ID:         sessionID,
		OutputDir:  outputDir,
		CreatedAt:  time.Now(),
		LastAccess: time.Now(),
		IsLive:     true,
		Completed:  true,
		FatalError: "Live stream failed before playback started",
	}
	manager.mu.Lock()
	manager.sessions[sessionID] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/video/hls/%s/stream.m3u8", sessionID), nil)
	rr := httptest.NewRecorder()
	started := time.Now()
	manager.ServePlaylist(rr, req, sessionID)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d (body: %s)", http.StatusBadGateway, rr.Code, rr.Body.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("failed live playlist response took %s, want less than 1s", elapsed)
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

func TestIsBrowserCopyCompatibleVideo(t *testing.T) {
	tests := []struct {
		name  string
		probe *UnifiedProbeResult
		want  bool
	}{
		{
			name:  "h264 8-bit yuv420p",
			probe: &UnifiedProbeResult{VideoCodec: "h264", VideoPixFmt: "yuv420p", VideoProfile: "High"},
			want:  true,
		},
		{
			name:  "h264 high 10",
			probe: &UnifiedProbeResult{VideoCodec: "h264", VideoPixFmt: "yuv420p10le", VideoProfile: "High 10"},
			want:  false,
		},
		{
			name:  "hevc is not broadly browser copy compatible",
			probe: &UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv420p", VideoProfile: "Main"},
			want:  false,
		},
		{
			name:  "missing probe data",
			probe: nil,
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBrowserCopyCompatibleVideo(tc.probe); got != tc.want {
				t.Fatalf("isBrowserCopyCompatibleVideo() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLiveHLSOutputArgsNativeTransmuxPreservesCaptionCarryingTS(t *testing.T) {
	args := liveHLSOutputArgs("native", "/tmp/live/segment%d.ts", "/tmp/live/stream.m3u8")
	joined := strings.Join(args, " ")

	for _, expected := range []string{
		"-c:v copy",
		"-c:a copy",
		"-hls_segment_filename /tmp/live/segment%d.ts",
		"/tmp/live/stream.m3u8",
		// Wider window + no FFmpeg delete_segments so ExoPlayer can fetch segment0
		// before the sliding window advances past it under stream-copy.
		fmt.Sprintf("-hls_list_size %d", liveNativeHLSListSize),
		"-hls_flags temp_file",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("native live args %q missing %q", joined, expected)
		}
	}
	for _, unexpected := range []string{"libx264", "-force_key_frames", "independent_segments", "delete_segments"} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("native live args %q unexpectedly contain %q", joined, unexpected)
		}
	}
}

func TestLiveHLSOutputArgsNativeTargetsAllUseTransmuxNotTranscode(t *testing.T) {
	// Every native client target must stay on copy/copy — only web re-encodes.
	for _, target := range []string{"native", "android", "ios", "tvos", "mpv", "ksplayer", "exoplayer"} {
		args := liveHLSOutputArgs(target, "/tmp/live/segment%d.ts", "/tmp/live/stream.m3u8")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-c:v copy") || !strings.Contains(joined, "-c:a copy") {
			t.Fatalf("target %q must transmux, got %q", target, joined)
		}
		if strings.Contains(joined, "libx264") || strings.Contains(joined, "delete_segments") {
			t.Fatalf("target %q must not transcode or use delete_segments, got %q", target, joined)
		}
	}
}

func TestLiveHLSOutputArgsWebRetainsCompatibilityTranscode(t *testing.T) {
	args := liveHLSOutputArgs("web", "/tmp/live/segment%d.ts", "/tmp/live/stream.m3u8")
	joined := strings.Join(args, " ")

	for _, expected := range []string{
		"-c:v libx264",
		"-c:a aac",
		"-force_key_frames expr:gte(t,n_forced*1)",
		"delete_segments+independent_segments+temp_file",
		"-hls_list_size 10",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("web live args %q missing %q", joined, expected)
		}
	}
	if strings.Contains(joined, "-c:v copy") {
		t.Fatalf("web live args should re-encode, not copy: %q", joined)
	}
}

func TestIsWebBrowserPlaybackTarget(t *testing.T) {
	for _, target := range []string{"web", "WEB", " browser ", "browser"} {
		if !isWebBrowserPlaybackTarget(target) {
			t.Fatalf("expected %q to be a web browser playback target", target)
		}
	}
	for _, target := range []string{"", "native", "ios", "android", "cast", "mpv"} {
		if isWebBrowserPlaybackTarget(target) {
			t.Fatalf("did not expect %q to be a web browser playback target", target)
		}
	}
}

func TestHlsAACTranscodeArgsWebUsesStereo(t *testing.T) {
	for _, target := range []string{"web", "browser"} {
		joined := strings.Join(hlsAACTranscodeArgs(target, "", 0, false), " ")
		for _, expected := range []string{"-c:a aac", "-ac 2", "-channel_layout stereo", "-ar 48000", "-af aresample=async=1000"} {
			if !strings.Contains(joined, expected) {
				t.Fatalf("web AAC args for %q missing %q: %q", target, expected, joined)
			}
		}
		if strings.Contains(joined, "-ac 6") || strings.Contains(joined, "5.1") {
			t.Fatalf("web AAC args for %q must not use 5.1: %q", target, joined)
		}

		indexed := strings.Join(hlsAACTranscodeArgs(target, "indexed0", 0, false), " ")
		for _, expected := range []string{"-c:a:0 aac", "-ac:a:0 2", "-channel_layout:a:0 stereo", "-c:a:1 copy"} {
			if !strings.Contains(indexed, expected) {
				t.Fatalf("web indexed AAC args for %q missing %q: %q", target, expected, indexed)
			}
		}

		// Mid-file web starts without same-pass VTT must reset the audio timeline for MSE.
		seekAF := strings.Join(hlsAACTranscodeArgs(target, "", 120, true), " ")
		if !strings.Contains(seekAF, "asetpts=PTS-STARTPTS") || !strings.Contains(seekAF, "first_pts=0") {
			t.Fatalf("web seek AAC args for %q missing PTS reset: %q", target, seekAF)
		}

		// Same-pass WebVTT must keep plain aresample so cues share the demuxer clock.
		withSubs := strings.Join(hlsAACTranscodeArgs(target, "", 120, false), " ")
		if strings.Contains(withSubs, "asetpts") || strings.Contains(withSubs, "first_pts=0") {
			t.Fatalf("web seek AAC with same-pass VTT must not use asetpts: %q", withSubs)
		}
	}
}

func TestHlsAACTranscodeArgsNonWebKeeps51(t *testing.T) {
	for _, target := range []string{"", "native", "ios", "android", "cast"} {
		joined := strings.Join(hlsAACTranscodeArgs(target, "", 0, false), " ")
		for _, expected := range []string{"-c:a aac", "-ac 6", "-channel_layout 5.1"} {
			if !strings.Contains(joined, expected) {
				t.Fatalf("non-web AAC args for %q missing %q: %q", target, expected, joined)
			}
		}
		if strings.Contains(joined, "-ac 2") || strings.Contains(joined, "stereo") {
			t.Fatalf("non-web AAC args for %q must keep 5.1: %q", target, joined)
		}

		indexed := strings.Join(hlsAACTranscodeArgs(target, "indexed0", 250, true), " ")
		for _, expected := range []string{"-c:a:0 aac", "-ac:a:0 6", "-channel_layout:a:0 5.1", "-c:a:1 copy"} {
			if !strings.Contains(indexed, expected) {
				t.Fatalf("non-web indexed AAC args for %q missing %q: %q", target, expected, indexed)
			}
		}
		// Native/cast mid-file must not get the web-only asetpts graph.
		if strings.Contains(indexed, "asetpts") {
			t.Fatalf("non-web AAC args for %q must not reset PTS via asetpts: %q", target, indexed)
		}
	}
}

func TestWebSeekPTSFiltersNeeded(t *testing.T) {
	if !webSeekPTSFiltersNeeded("web", 100, false) {
		t.Fatal("web mid-file without same-pass VTT should apply setpts/asetpts")
	}
	if webSeekPTSFiltersNeeded("web", 100, true) {
		t.Fatal("web mid-file with same-pass VTT must skip setpts/asetpts")
	}
	if webSeekPTSFiltersNeeded("web", 0, false) {
		t.Fatal("web start-from-zero does not need PTS filters")
	}
	if webSeekPTSFiltersNeeded("native", 100, false) {
		t.Fatal("native mid-file does not use web PTS filters")
	}
}

func TestWithWebSeekVideoPTSReset(t *testing.T) {
	if got := withWebSeekVideoPTSReset(""); got != "setpts=PTS-STARTPTS" {
		t.Fatalf("empty filter: got %q", got)
	}
	if got := withWebSeekVideoPTSReset("format=yuv420p"); got != "setpts=PTS-STARTPTS,format=yuv420p" {
		t.Fatalf("with format: got %q", got)
	}
}

func TestUseAccurateHLSInputSeek(t *testing.T) {
	// Web mid-file resume always needs accurate seek so setpts/asetpts cannot
	// zero A/V onto different content (GOP-length desync).
	if !useAccurateHLSInputSeek("web", 635.87, false, false, false) {
		t.Fatal("web mid-file must use accurate input seek")
	}
	if !useAccurateHLSInputSeek("browser", 100, false, false, false) {
		t.Fatal("browser mid-file must use accurate input seek")
	}
	// Video transcode / cast / same-pass subs also need accurate seek.
	if !useAccurateHLSInputSeek("web", 100, true, false, false) {
		t.Fatal("videoWillTranscode must use accurate input seek")
	}
	if !useAccurateHLSInputSeek("native", 100, true, false, false) {
		t.Fatal("native video transcode must use accurate input seek")
	}
	if !useAccurateHLSInputSeek("web", 100, false, true, false) {
		t.Fatal("subtitle rendition must use accurate input seek")
	}
	if !useAccurateHLSInputSeek("cast", 100, false, false, true) {
		t.Fatal("stable cast must use accurate input seek")
	}
	// Pure video-copy mid-file keeps keyframe-friendly inaccurate seek.
	if useAccurateHLSInputSeek("native", 100, false, false, false) {
		t.Fatal("native video-copy mid-file should keep -noaccurate_seek")
	}
	if useAccurateHLSInputSeek("web", 0, false, false, false) {
		t.Fatal("web start-from-zero does not need accurate mid-file seek")
	}
}

func TestWaitForPlaylistReady(t *testing.T) {
	dir := t.TempDir()
	m := &HLSManager{}
	session := &HLSSession{OutputDir: dir}

	// Empty dir times out quickly.
	ready, size := m.waitForPlaylistReady(session, 40*time.Millisecond)
	if ready || size != 0 {
		t.Fatalf("expected timeout on empty dir, got ready=%v size=%d", ready, size)
	}

	// Header-only playlist is not ready (no media segment line).
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n#EXT-X-VERSION:3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready, size = m.waitForPlaylistReady(session, 40*time.Millisecond)
	if ready || size != 0 {
		t.Fatalf("expected timeout on header-only playlist, got ready=%v size=%d", ready, size)
	}

	// Media segment present → ready.
	body := "#EXTM3U\n#EXTINF:2.0,\nsegment0.ts\n"
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ready, size = m.waitForPlaylistReady(session, time.Second)
	if !ready || size != len(body) {
		t.Fatalf("expected ready playlist, got ready=%v size=%d want %d", ready, size, len(body))
	}
}

func TestSummarizeLiveSegmentDirAndPlaylist(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []int{2, 3, 5} {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("segment%d.ts", n)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	playlist := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-MEDIA-SEQUENCE:2",
		"#EXTINF:2.0,",
		"segment2.ts",
		"#EXTINF:2.0,",
		"segment3.ts",
		"#EXTINF:2.0,",
		"segment5.ts",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(playlist), 0o600); err != nil {
		t.Fatal(err)
	}

	disk := summarizeLiveSegmentDir(dir)
	if !strings.Contains(disk, "onDisk=3") || !strings.Contains(disk, "min=segment2") || !strings.Contains(disk, "max=segment5") || !strings.Contains(disk, "holes=1") {
		t.Fatalf("unexpected disk summary: %s", disk)
	}
	pl := summarizeLivePlaylistState(dir)
	if !strings.Contains(pl, "mediaSeq=2") || !strings.Contains(pl, "first=segment2.ts") || !strings.Contains(pl, "last=segment5.ts") || !strings.Contains(pl, "segs=3") {
		t.Fatalf("unexpected playlist summary: %s", pl)
	}

	empty := t.TempDir()
	if got := summarizeLiveSegmentDir(empty); got != "onDisk=0" {
		t.Fatalf("empty dir summary = %q, want onDisk=0", got)
	}
}

func TestDeleteOldLiveTransmuxSegmentsKeepsBehindWindow(t *testing.T) {
	dir := t.TempDir()
	// Create a run of segment files 0..80
	for i := 0; i <= 80; i++ {
		path := filepath.Join(dir, fmt.Sprintf("segment%d.ts", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mgr := &HLSManager{}
	session := &HLSSession{
		ID:                  "live-test",
		OutputDir:           dir,
		IsLive:              true,
		PlaybackTarget:      "native",
		MaxSegmentRequested: 80,
		LastSegmentServed:   80,
	}

	mgr.deleteOldLiveTransmuxSegments(session, 80)

	// highWater=80, keepBehind=60 → cutoff=20; walk start=0 deletes 0..20
	for i := 0; i <= 20; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("segment%d.ts", i))); !os.IsNotExist(err) {
			t.Fatalf("segment%d.ts should have been deleted", i)
		}
	}
	// Segments above the cutoff must remain
	for i := 21; i <= 80; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("segment%d.ts", i))); err != nil {
			t.Fatalf("segment%d.ts should still exist: %v", i, err)
		}
	}
}

func TestShouldUseAccurateRequestedSeekForWebSubtitle(t *testing.T) {
	textSubs := []subtitleStreamInfo{{Index: 3, Codec: "subrip"}}

	tests := []struct {
		name           string
		playbackTarget string
		probe          *UnifiedProbeResult
		subtitles      []subtitleStreamInfo
		trackIndex     int
		want           bool
	}{
		{
			name:           "web HEVC transcode with selected text subtitle",
			playbackTarget: "web",
			probe:          &UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv420p10le", VideoProfile: "Main 10"},
			subtitles:      textSubs,
			trackIndex:     3,
			want:           true,
		},
		{
			name:           "web H264 copy-compatible video still uses accurate subtitle path",
			playbackTarget: "web",
			probe:          &UnifiedProbeResult{VideoCodec: "h264", VideoPixFmt: "yuv420p", VideoProfile: "High"},
			subtitles:      textSubs,
			trackIndex:     3,
			want:           true,
		},
		{
			name:           "non-web playback does not use web subtitle seek path",
			playbackTarget: "ios",
			probe:          &UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv420p10le", VideoProfile: "Main 10"},
			subtitles:      textSubs,
			trackIndex:     3,
			want:           false,
		},
		{
			name:           "bitmap subtitle does not trigger same-pass text path",
			playbackTarget: "web",
			probe:          &UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv420p10le", VideoProfile: "Main 10"},
			subtitles:      []subtitleStreamInfo{{Index: 3, Codec: "hdmv_pgs_subtitle"}},
			trackIndex:     3,
			want:           false,
		},
		{
			name:           "unselected text subtitle does not trigger path",
			playbackTarget: "web",
			probe:          &UnifiedProbeResult{VideoCodec: "hevc", VideoPixFmt: "yuv420p10le", VideoProfile: "Main 10"},
			subtitles:      textSubs,
			trackIndex:     4,
			want:           false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldUseAccurateRequestedSeekForWebSubtitle(tc.playbackTarget, tc.probe, tc.subtitles, tc.trackIndex)
			if got != tc.want {
				t.Fatalf("shouldUseAccurateRequestedSeekForWebSubtitle() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldForceWebSubtitleVideoTranscode(t *testing.T) {
	textSubs := []subtitleStreamInfo{{Index: 3, Codec: "subrip"}}

	tests := []struct {
		name              string
		playbackTarget    string
		subtitles         []subtitleStreamInfo
		trackIndex        int
		transcodingOffset float64
		want              bool
	}{
		{
			name:              "web selected text subtitle at resume offset",
			playbackTarget:    "web",
			subtitles:         textSubs,
			trackIndex:        3,
			transcodingOffset: 74.51,
			want:              true,
		},
		{
			name:              "cold start does not force video transcode",
			playbackTarget:    "web",
			subtitles:         textSubs,
			trackIndex:        3,
			transcodingOffset: 0,
			want:              false,
		},
		{
			name:              "non-web playback does not force video transcode",
			playbackTarget:    "ios",
			subtitles:         textSubs,
			trackIndex:        3,
			transcodingOffset: 74.51,
			want:              false,
		},
		{
			name:              "bitmap subtitle does not force video transcode",
			playbackTarget:    "web",
			subtitles:         []subtitleStreamInfo{{Index: 3, Codec: "hdmv_pgs_subtitle"}},
			trackIndex:        3,
			transcodingOffset: 74.51,
			want:              false,
		},
		{
			name:              "unselected text subtitle does not force video transcode",
			playbackTarget:    "web",
			subtitles:         textSubs,
			trackIndex:        4,
			transcodingOffset: 74.51,
			want:              false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldForceWebSubtitleVideoTranscode(tc.playbackTarget, tc.subtitles, tc.trackIndex, tc.transcodingOffset)
			if got != tc.want {
				t.Fatalf("shouldForceWebSubtitleVideoTranscode() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldPreferRequestedTranscodingOffset_NilProbeWebSubtitleFallback(t *testing.T) {
	if !shouldPreferRequestedTranscodingOffset("web", nil, nil, 3) {
		t.Fatal("nil-probe web subtitle sessions should prefer requested offset because web video compatibility is unknown")
	}
	if shouldPreferRequestedTranscodingOffset("web", nil, nil, -1) {
		t.Fatal("subtitle-off sessions should not use the subtitle-specific fallback")
	}
	if shouldPreferRequestedTranscodingOffset("ios", nil, nil, 3) {
		t.Fatal("non-web sessions should not use the web subtitle fallback")
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

func TestHLSManager_ServeSubtitleTrackReportsVideoTimestampBase(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()

	const sessionID = "subtitle-timestamp-base-test"
	outputDir := filepath.Join(tmpDir, sessionID)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "subtitles_11.vtt"), []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nHello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	session := &HLSSession{
		ID:                           sessionID,
		OutputDir:                    outputDir,
		CreatedAt:                    time.Now(),
		LastAccess:                   time.Now(),
		SubtitleTrackIndex:           -1,
		SubtitleTimestampBaseSeconds: mpegtsDefaultPreloadSeconds,
	}
	session.setSubtitleExtractionOffset(11, 686.37)
	manager.mu.Lock()
	manager.sessions[sessionID] = session
	manager.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/video/hls/"+sessionID+"/subtitles-11.vtt", nil)
	rr := httptest.NewRecorder()
	manager.ServeSubtitleTrack(rr, req, sessionID, 11, false)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if got := rr.Header().Get("X-Subtitle-Start-Offset"); got != "686.370" {
		t.Fatalf("subtitle start offset = %q, want 686.370", got)
	}
	if got := rr.Header().Get("X-Subtitle-Timestamp-Base"); got != "1.400" {
		t.Fatalf("subtitle timestamp base = %q, want 1.400", got)
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

	session.mu.RLock()
	stopped := session.Stopped
	session.mu.RUnlock()
	if !stopped {
		t.Error("session should be marked stopped to prevent recovery restart")
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

func TestHLSManager_BuildLocalWebDAVURLFromPathSkipsRemoteMedia(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewHLSManager(tmpDir, "", "", nil)
	defer manager.Shutdown()
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "", "")

	for _, path := range []string{
		"plexmedia:item-123",
		"/plexmedia:item-123",
		"jellyfinmedia:item-456",
		"/jellyfinmedia:item-456",
	} {
		t.Run(path, func(t *testing.T) {
			if url, ok := manager.buildLocalWebDAVURLFromPath(path); ok || url != "" {
				t.Fatalf("buildLocalWebDAVURLFromPath(%q) = %q, %v; want empty, false", path, url, ok)
			}
		})
	}
}

func TestHLSManager_ProbeRemoteMediaUsesProviderPipe(t *testing.T) {
	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	ffprobeScript := `#!/bin/sh
cat >/dev/null
printf '%s' '{"format":{"duration":"12.5","start_time":"0"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264"},{"index":1,"codec_type":"audio","codec_name":"aac"}]}'
`
	if err := os.WriteFile(ffprobePath, []byte(ffprobeScript), 0o755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}

	provider := &remoteProbeTestProvider{}
	manager := NewHLSManager(t.TempDir(), "", ffprobePath, provider)
	defer manager.Shutdown()
	manager.ConfigureLocalWebDAVAccess("http://localhost:7777", "/webdav", "", "")

	result, err := manager.probeAllMetadata(context.Background(), "plexmedia:item-123")
	if err != nil {
		t.Fatalf("probeAllMetadata: %v", err)
	}
	if result == nil || result.Duration != 12.5 {
		t.Fatalf("probe result = %#v, want duration 12.5", result)
	}
	if provider.lastRequest.Path != "plexmedia:item-123" {
		t.Fatalf("provider path = %q, want plexmedia:item-123", provider.lastRequest.Path)
	}
	if provider.lastRequest.RangeHeader != "bytes=0-16777215" {
		t.Fatalf("provider range = %q, want 16 MiB probe range", provider.lastRequest.RangeHeader)
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

func TestInputLooksLikeHLS(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://host/live/u/p/31930.ts", false},
		{"http://host/live/u/p/31930.m3u8", true},
		{"http://host/path/playlist.m3u8?token=abc", true},
		{"http://host/path/master.M3U8", true},
		{"https://sports.highfly.dev/playlist/signed-token", true},
		{"https://sports.highfly.dev/playlist/signed-token?expires=123", true},
		{"http://host/stream.mp4", false},
		{"http://host/live/u/p/31930.ts?x=1#frag", false},
		{"", false},
	}
	for _, c := range cases {
		if got := inputLooksLikeHLS(c.url); got != c.want {
			t.Errorf("inputLooksLikeHLS(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestStremioStreamLooksLikeHLSExtensionless(t *testing.T) {
	stream := stremioStream{Name: "M3U8", URL: "https://sports.example/live/signed-token"}
	if !stremioStreamLooksLikeHLS(stream, stream.URL) {
		t.Fatal("M3U8 stream hint should identify extensionless Stremio URL as HLS")
	}
	stream = stremioStream{URL: "https://worker.example/?action=stream&ext=.m3u8"}
	if !stremioStreamLooksLikeHLS(stream, stream.URL) {
		t.Fatal("ext=.m3u8 query hint should identify extensionless Stremio URL as HLS")
	}
	stream = stremioStream{Name: "MPEG-TS", URL: "https://sports.example/live/channel.ts"}
	if stremioStreamLooksLikeHLS(stream, stream.URL) {
		t.Fatal("direct MPEG-TS stream should not be forced through HLS demuxer options")
	}
}

func TestResolvedSessionDuration(t *testing.T) {
	tests := []struct {
		name   string
		probed float64
		hint   float64
		want   float64
	}{
		{name: "probe wins", probed: 120.5, hint: 300, want: 120.5},
		{name: "hint fills missing probe", hint: 300, want: 300},
		{name: "negative hint rejected", hint: -1, want: 0},
		{name: "oversized hint rejected", hint: 8 * 24 * 60 * 60, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvedSessionDuration(tt.probed, tt.hint); got != tt.want {
				t.Fatalf("resolvedSessionDuration(%v, %v)=%v, want %v", tt.probed, tt.hint, got, tt.want)
			}
		})
	}
}

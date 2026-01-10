package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"novastream/services/streaming"
	"novastream/utils"
)

// debugReader wraps an io.Reader to log bytes read and detect EOF
type debugReader struct {
	r           io.Reader
	sessionID   string
	bytesRead   int64
	startTime   time.Time
	lastLogTime time.Time
	logInterval time.Duration
	closed      atomic.Bool
}

func newDebugReader(r io.Reader, sessionID string) *debugReader {
	return &debugReader{
		r:           r,
		sessionID:   sessionID,
		startTime:   time.Now(),
		lastLogTime: time.Now(),
		logInterval: 30 * time.Second, // Log progress every 30 seconds
	}
}

func (d *debugReader) Read(p []byte) (n int, err error) {
	n, err = d.r.Read(p)
	if n > 0 {
		d.bytesRead += int64(n)
		// Log progress periodically
		if time.Since(d.lastLogTime) >= d.logInterval {
			elapsed := time.Since(d.startTime)
			mbRead := float64(d.bytesRead) / 1024 / 1024
			mbPerSec := mbRead / elapsed.Seconds()
			log.Printf("[hls] session %s: SOURCE_STREAM progress - %.2f MB read in %v (%.2f MB/s)",
				d.sessionID, mbRead, elapsed.Round(time.Second), mbPerSec)
			d.lastLogTime = time.Now()
		}
	}
	if err != nil {
		elapsed := time.Since(d.startTime)
		mbRead := float64(d.bytesRead) / 1024 / 1024
		if err == io.EOF {
			log.Printf("[hls] session %s: SOURCE_STREAM EOF - total %.2f MB read in %v",
				d.sessionID, mbRead, elapsed.Round(time.Second))
		} else {
			log.Printf("[hls] session %s: SOURCE_STREAM ERROR after %.2f MB in %v: %v",
				d.sessionID, mbRead, elapsed.Round(time.Second), err)
		}
		d.closed.Store(true)
	}
	return n, err
}

// throttledReader wraps an io.Reader and slows down reads when ffmpeg is
// generating segments faster than the player is consuming them.
// This prevents excessive disk usage from buffered segments.
type throttledReader struct {
	r             io.Reader
	session       *HLSSession
	lastThrottle  time.Time
	throttleCount int64
}

// throttlingProxy is an HTTP server that proxies requests to a remote URL
// with throttling support. It allows FFmpeg to use HTTP Range requests for
// seeking while we control the download speed.
type throttlingProxy struct {
	targetURL string
	session   *HLSSession
	server    *http.Server
	port      int
}

// newThrottlingProxy creates a new throttling proxy for the given URL.
// Returns the proxy and the local URL that FFmpeg should use.
func newThrottlingProxy(targetURL string, session *HLSSession) (*throttlingProxy, string, error) {
	proxy := &throttlingProxy{
		targetURL: targetURL,
		session:   session,
	}

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("failed to find free port: %w", err)
	}
	proxy.port = listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", proxy.handleStream)

	proxy.server = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := proxy.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[hls] session %s: proxy server error: %v", session.ID, err)
		}
	}()

	localURL := fmt.Sprintf("http://127.0.0.1:%d/stream", proxy.port)
	log.Printf("[hls] session %s: started throttling proxy on port %d for URL: %s", session.ID, proxy.port, targetURL)

	return proxy, localURL, nil
}

func (p *throttlingProxy) handleStream(w http.ResponseWriter, r *http.Request) {
	// Encode URL properly (handles spaces and special characters)
	encodedURL, err := utils.EncodeURLWithSpaces(p.targetURL)
	if err != nil {
		log.Printf("[hls] session %s: failed to encode URL: %v", p.session.ID, err)
		http.Error(w, "failed to encode URL", http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, encodedURL, nil)
	if err != nil {
		log.Printf("[hls] session %s: failed to create request: %v", p.session.ID, err)
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	// Extract userinfo from URL and set Basic Auth header
	// Go's http.Client doesn't automatically use URL-embedded credentials
	if parsedURL, parseErr := url.Parse(p.targetURL); parseErr == nil && parsedURL.User != nil {
		password, _ := parsedURL.User.Password()
		req.SetBasicAuth(parsedURL.User.Username(), password)
	}

	// Forward Range header for seeking support
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
		log.Printf("[hls] session %s: proxy forwarding Range: %s", p.session.ID, rangeHeader)
	}

	// Make request to target
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[hls] session %s: proxy request failed: %v", p.session.ID, err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Log upstream response status for debugging
	if resp.StatusCode >= 400 {
		log.Printf("[hls] session %s: proxy upstream returned %d %s for URL: %s", p.session.ID, resp.StatusCode, resp.Status, p.targetURL)
	}

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Wrap with throttled reader and copy to response
	throttled := newThrottledReader(resp.Body, p.session)
	_, err = io.Copy(w, throttled)
	if err != nil && err != context.Canceled {
		log.Printf("[hls] session %s: proxy copy error: %v", p.session.ID, err)
	}
}

func (p *throttlingProxy) Close() {
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p.server.Shutdown(ctx)
		log.Printf("[hls] session %s: stopped throttling proxy on port %d", p.session.ID, p.port)
	}
}

func newThrottledReader(r io.Reader, session *HLSSession) *throttledReader {
	return &throttledReader{
		r:       r,
		session: session,
	}
}

func (t *throttledReader) Read(p []byte) (n int, err error) {
	// Check how far ahead ffmpeg is compared to player requests
	t.session.mu.RLock()
	maxRequested := t.session.MaxSegmentRequested
	sessionID := t.session.ID
	outputDir := t.session.OutputDir
	// TESTING: hasDV/hasHDR unused since we always use .m4s
	_ = t.session.HasDV
	_ = t.session.HasHDR
	t.session.mu.RUnlock()

	// Only throttle if player has started requesting segments
	if maxRequested >= 0 {
		// Check actual segment files on disk (more accurate than SegmentsCreated counter)
		// TESTING: Always use .m4s for all content
		segmentExt := ".m4s"
		// if hasDV || hasHDR {
		// 	segmentExt = ".m4s"
		// }
		pattern := filepath.Join(outputDir, "segment*"+segmentExt)
		segmentFiles, _ := filepath.Glob(pattern)

		// Find highest segment number
		highestSegment := -1
		for _, f := range segmentFiles {
			base := filepath.Base(f)
			var segNum int
			if _, err := fmt.Sscanf(base, "segment%d", &segNum); err == nil {
				if segNum > highestSegment {
					highestSegment = segNum
				}
			}
		}

		if highestSegment >= 0 {
			bufferAhead := highestSegment - maxRequested

			// Start throttling when 15+ segments ahead (~60 seconds at 4s/segment)
			// Apply aggressive delays to prevent runaway buffering
			const throttleStartThreshold = 15
			if bufferAhead > throttleStartThreshold {
				// Calculate delay: scales aggressively with buffer size
				// At 16 segments ahead: 500ms, at 30 ahead: 2000ms+
				excessSegments := bufferAhead - throttleStartThreshold
				delayMs := 500 + (excessSegments * 100) // 500ms base + 100ms per excess segment

				// Cap at 15 seconds to avoid HTTP connection timeouts from the source.
				// Most servers have read timeouts of 30-60s, so 15s should be safe.
				//
				// TODO: If this still causes issues with very fast connections, consider
				// implementing buffered throttling instead: read from source at full speed
				// into a memory buffer, then feed ffmpeg at a controlled rate. This would
				// keep the source connection alive while still controlling disk usage.
				if delayMs > 15000 {
					delayMs = 15000
				}

				time.Sleep(time.Duration(delayMs) * time.Millisecond)
				t.throttleCount++

				// Log throttling periodically (every 10 seconds)
				if time.Since(t.lastThrottle) > 10*time.Second {
					log.Printf("[hls] session %s: THROTTLE - %d segments ahead (highest=%d, requested=%d), delay=%dms, total throttles=%d",
						sessionID, bufferAhead, highestSegment, maxRequested, delayMs, t.throttleCount)
					t.lastThrottle = time.Now()
				}
			}
		}
	}

	return t.r.Read(p)
}

// HLSSession represents an active HLS transcoding session
type HLSSession struct {
	ID           string
	Path         string
	OriginalPath string
	OutputDir    string
	CreatedAt    time.Time
	LastAccess   time.Time
	FFmpegCmd    *exec.Cmd
	Cancel       context.CancelFunc
	mu           sync.RWMutex
	Completed    bool
	HasDV        bool
	DVProfile    string
	DVDisabled          bool // Set to true if DV metadata parsing fails and we fallback to non-DV
	HasHDR              bool // HDR10 content (needs fMP4 segments for iOS compatibility)
	HDRMetadataDisabled bool // Set to true if hevc_metadata filter fails (malformed SEI data)
	Duration          float64 // Total duration in seconds from ffprobe
	StartOffset        float64 // Requested start offset in seconds for session warm starts (never changes, for frontend)
	TranscodingOffset  float64 // Current transcoding position (updated on recovery restarts)
	ActualStartOffset  float64 // Actual start time from fMP4 tfdt box (keyframe-aligned, for subtitle sync)

	// Profile tracking
	ProfileID   string
	ProfileName string
	ClientIP    string

	// Track selection (-1 means use default)
	AudioTrackIndex    int // Selected audio stream index (ffprobe index), -1 = all/default
	SubtitleTrackIndex int // Selected subtitle track index, -1 = none

	// Performance tracking
	StreamStartTime      time.Time
	FirstSegmentTime     time.Time
	BytesStreamed        int64
	SegmentsCreated      int
	FFmpegCPUStart       float64
	FFmpegPID            int
	LastSegmentRequest   time.Time
	SegmentRequestCount  int
	IdleTimeoutTriggered bool

	// Segment tracking for cleanup and rate limiting
	MinSegmentRequested      int // Minimum segment number that has been requested (-1 = none yet)
	MaxSegmentRequested      int // Maximum segment number that has been requested (-1 = none yet)
	MinSegmentAvailable      int // Minimum segment number still available on disk (for playlist filtering)
	LastPlaybackSegment      int // Player's actual playback position from keepalive time reports (-1 = unknown)
	LastSegmentServed        int // Last segment number successfully served to client (-1 = none yet)
	EarliestBufferedSegment  int // Earliest segment still in player's buffer from keepalive (-1 = unknown)
	Paused                   bool // True if FFmpeg is paused (SIGSTOP) waiting for player to catch up

	// Input error recovery (for usenet disconnections)
	InputErrorDetected bool // Set to true when FFmpeg input stream fails (usenet disconnect)
	RecoveryAttempts   int  // Number of times we've attempted to recover this session
	forceAAC           bool // Cached forceAAC setting for recovery restarts
	SeekInProgress     bool // Set to true during user-initiated seek to prevent recovery logic

	// Fatal error tracking (unplayable streams)
	FatalError       string // Set when stream is determined to be unplayable (persistent bitstream errors)

	// Cached probe data from unified probe (avoids multiple ffprobe calls)
	ProbeData *UnifiedProbeResult

	// Per-track extraction tracking (prevents duplicate extractions without blocking session)
	subtitleExtractionMu     sync.Mutex      // Protects subtitleExtracting map
	subtitleExtracting       map[int]bool    // Tracks which subtitle tracks are currently being extracted
	FatalErrorTime   time.Time
	BitstreamErrors  int // Count of bitstream filter errors (to detect persistent issues)

	// Live TV session fields
	IsLive bool // True for live TV streams (no duration, no seeking)
}

const (
	// How long to wait with no segment requests before killing FFmpeg
	hlsIdleTimeout = 30 * time.Second

	// How long to wait for the first segment request before killing FFmpeg
	// (prevents sessions that never receive any requests from lingering)
	hlsStartupTimeout = 30 * time.Second

	// Matroska-specific tuning for pipe-based seeks
	matroskaHeaderPrefixBytes int64 = 2 * 1024 * 1024 // copy 2MB of header metadata
	matroskaSeekBackoffBytes  int64 = 8 * 1024 * 1024 // request a little earlier to land on cluster boundary
	matroskaMaxClusterScan    int64 = 32 * 1024 * 1024

	// Maximum number of input error recovery attempts before giving up
	// This prevents infinite restart loops for persistently broken streams
	hlsMaxRecoveryAttempts = 3

	// HLS segment duration in seconds (must match -hls_time value)
	hlsSegmentDuration = 2.0

	// Rate limiting: pause FFmpeg when buffer gets too far ahead of player
	// Note: Players keep buffering even when paused, so we need generous thresholds
	// Pause when (segmentsOnDisk - maxRequested) exceeds this value
	hlsBufferPauseThreshold = 30 // ~2 minutes of buffer ahead (30 * 4s segments)
	// Resume when buffer drops to this level
	hlsBufferResumeThreshold = 20 // ~80 seconds of buffer ahead
)


// HLSManager manages HLS transcoding sessions
type HLSManager struct {
	sessions           map[string]*HLSSession
	mu                 sync.RWMutex
	baseDir            string
	ffmpegPath         string
	ffprobePath        string
	streamer           streaming.Provider
	cleanupDone        chan struct{}
	localAccessMu      sync.RWMutex
	localWebDAVBaseURL string
	localWebDAVPrefix  string
	// Global probe cache - shared between prequeue (ProbeVideoFull) and HLS (probeAllMetadata)
	probeCache   map[string]*cachedProbeEntry
	probeCacheMu sync.RWMutex
}

// NewHLSManager creates a new HLS session manager
func NewHLSManager(baseDir, ffmpegPath, ffprobePath string, streamer streaming.Provider) *HLSManager {
	if baseDir == "" {
		// Use /tmp for HLS segment storage with proper cleanup
		baseDir = filepath.Join("/tmp", "novastream-hls")
	}

	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		log.Printf("[hls] failed to create base directory %q: %v", baseDir, err)
	}

	manager := &HLSManager{
		sessions:    make(map[string]*HLSSession),
		baseDir:     baseDir,
		ffmpegPath:  ffmpegPath,
		ffprobePath: ffprobePath,
		streamer:    streamer,
		cleanupDone: make(chan struct{}),
		probeCache:  make(map[string]*cachedProbeEntry),
	}

	// Clean up any orphaned directories from previous runs
	manager.cleanupOrphanedDirectories()

	// Start cleanup goroutine
	go manager.cleanupLoop()

	return manager
}

// ConfigureLocalWebDAVAccess allows the manager to build direct URLs against the local WebDAV server.
// baseURL should be something like http://127.0.0.1:7777. prefix is the configured WebDAV prefix (e.g., /webdav).
func (m *HLSManager) ConfigureLocalWebDAVAccess(baseURL, prefix, username, password string) {
	if m == nil {
		return
	}

	base := strings.TrimSpace(baseURL)
	if base == "" {
		m.localAccessMu.Lock()
		m.localWebDAVBaseURL = ""
		m.localWebDAVPrefix = ""
		m.localAccessMu.Unlock()
		return
	}

	parsed, err := url.Parse(base)
	if err != nil {
		log.Printf("[hls] invalid local WebDAV base URL %q: %v", baseURL, err)
		return
	}

	if username != "" {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = nil
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	normalizedBase := strings.TrimRight(parsed.String(), "/")
	normalizedPrefix := normalizeWebDAVPrefix(prefix)

	m.localAccessMu.Lock()
	m.localWebDAVBaseURL = normalizedBase
	m.localWebDAVPrefix = normalizedPrefix
	m.localAccessMu.Unlock()

	log.Printf("[hls] configured local WebDAV direct access: base=%q prefix=%q", normalizedBase, normalizedPrefix)
}

// generateSessionID creates a random session ID
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// resolveExternalURL follows HTTP redirects to get the final direct URL.
// This is important for AIOstreams/Comet URLs which are API endpoints that redirect
// to the actual debrid CDN URL. By resolving once upfront, we avoid repeated redirect
// resolution during probing and FFmpeg input, which can cause timeouts.
func (m *HLSManager) resolveExternalURL(ctx context.Context, externalURL string) (string, error) {
	log.Printf("[hls] resolving external URL: %s", externalURL)

	// Create a client that captures the final URL after redirects
	var finalURL string
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Track the URL we're redirecting to
			finalURL = req.URL.String()
			log.Printf("[hls] following redirect to: %s", finalURL)
			return nil
		},
	}

	// Encode URL properly (handles spaces and special characters)
	encodedURL, err := utils.EncodeURLWithSpaces(externalURL)
	if err != nil {
		return "", fmt.Errorf("encode URL: %w", err)
	}

	// First try HEAD request (faster, no body)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, encodedURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "VLC/3.0.18 LibVLC/3.0.18")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HEAD request failed: %w", err)
	}
	resp.Body.Close()

	// If HEAD succeeded, check for redirects
	if resp.StatusCode < 400 {
		if finalURL != "" && finalURL != externalURL {
			log.Printf("[hls] resolved external URL via HEAD: %s -> %s", externalURL, finalURL)
			return finalURL, nil
		}
		log.Printf("[hls] external URL has no redirects (HEAD): %s", externalURL)
		return externalURL, nil
	}

	// HEAD failed (e.g., 405 Method Not Allowed), try GET with Range header
	// This minimizes data transfer while still following redirects
	log.Printf("[hls] HEAD returned %d, trying GET with Range header", resp.StatusCode)
	finalURL = "" // Reset for new request

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, encodedURL, nil)
	if err != nil {
		return "", fmt.Errorf("create GET request: %w", err)
	}
	req.Header.Set("User-Agent", "VLC/3.0.18 LibVLC/3.0.18")
	req.Header.Set("Range", "bytes=0-0") // Request only 1 byte

	resp, err = client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}
	resp.Body.Close() // Close immediately, we only needed the redirect resolution

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("GET request returned status %d", resp.StatusCode)
	}

	// If we followed redirects, use the final URL
	if finalURL != "" && finalURL != externalURL {
		log.Printf("[hls] resolved external URL via GET: %s -> %s", externalURL, finalURL)
		return finalURL, nil
	}

	// No redirects, use the original URL
	log.Printf("[hls] external URL has no redirects (GET): %s", externalURL)
	return externalURL, nil
}

// getDirectURL attempts to get a direct HTTP URL for the session source
// Returns the URL and true if available, empty string and false otherwise
func (m *HLSManager) getDirectURL(ctx context.Context, session *HLSSession) (string, bool) {
	// If the path is already an external URL, return it directly
	// Note: The URL should already be resolved in CreateSession, so we just return it
	if strings.HasPrefix(session.Path, "http://") || strings.HasPrefix(session.Path, "https://") {
		log.Printf("[hls] path is already an external URL: %s", session.Path)
		return session.Path, true
	}

	// Check if the streaming provider supports direct URLs
	directProvider, ok := m.streamer.(streaming.DirectURLProvider)
	if !ok {
		log.Printf("[hls] streaming provider does not implement DirectURLProvider interface")
		if directURL, ok := m.buildLocalWebDAVURL(session); ok {
			return directURL, true
		}
		return "", false
	}

	log.Printf("[hls] streaming provider supports DirectURLProvider, fetching URL for path: %s", session.Path)

	// Get the direct URL
	url, err := directProvider.GetDirectURL(ctx, session.Path)
	if err != nil {
		log.Printf("[hls] failed to get direct URL for %s: %v", session.Path, err)
		if directURL, ok := m.buildLocalWebDAVURL(session); ok {
			return directURL, true
		}
		return "", false
	}

	log.Printf("[hls] successfully got direct URL for %s: %s", session.Path, url)
	return url, true
}

func (m *HLSManager) buildLocalWebDAVURL(session *HLSSession) (string, bool) {
	if session == nil {
		return "", false
	}

	m.localAccessMu.RLock()
	base := m.localWebDAVBaseURL
	prefix := m.localWebDAVPrefix
	m.localAccessMu.RUnlock()

	if base == "" || prefix == "" {
		return "", false
	}

	original := strings.TrimSpace(session.OriginalPath)
	if original == "" {
		return "", false
	}

	if !strings.HasPrefix(original, "/") {
		original = "/" + original
	}

	if !strings.HasPrefix(original, prefix) {
		return "", false
	}

	full := strings.TrimRight(base, "/") + original
	log.Printf("[hls] using local WebDAV direct URL for session %s: %s", session.ID, full)
	return full, true
}

// buildLocalWebDAVURLFromPath builds a WebDAV URL from just a path (no session required).
// This is used for probing usenet content where we don't have a session yet.
func (m *HLSManager) buildLocalWebDAVURLFromPath(path string) (string, bool) {
	m.localAccessMu.RLock()
	base := m.localWebDAVBaseURL
	prefix := m.localWebDAVPrefix
	m.localAccessMu.RUnlock()

	if base == "" || prefix == "" {
		return "", false
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}

	// Normalize path to start with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// If path starts with /webdav, use it directly; otherwise prepend the prefix
	if !strings.HasPrefix(path, prefix) {
		path = prefix + path
	}

	full := strings.TrimRight(base, "/") + path
	log.Printf("[hls] built local WebDAV URL from path: %s", full)
	return full, true
}

// CreateSession starts a new HLS transcoding session
func (m *HLSManager) CreateSession(ctx context.Context, path string, originalPath string, hasDV bool, dvProfile string, hasHDR bool, forceAAC bool, startOffset float64, transcodingOffset float64, audioTrackIndex int, subtitleTrackIndex int, profileID string, profileName string, clientIP string) (*HLSSession, error) {
	sessionID := generateSessionID()
	outputDir := filepath.Join(m.baseDir, sessionID)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	// Use background context so transcoding continues after HTTP response
	// The original ctx is only used for the initial setup
	bgCtx, cancel := context.WithCancel(context.Background())

	// Check if the path is an external URL (e.g., from AIOStreams pre-resolved streams)
	isExternalURL := strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")

	// For external URLs (like Comet/AIOstreams), resolve redirects upfront to get the actual
	// debrid CDN URL. This is critical because Comet URLs are API endpoints that redirect,
	// and repeatedly following redirects during probing causes timeouts.
	if isExternalURL {
		resolvedURL, err := m.resolveExternalURL(ctx, path)
		if err != nil {
			log.Printf("[hls] session %s: failed to resolve external URL, using original: %v", sessionID, err)
			// Continue with original URL - ffmpeg/ffprobe can follow redirects
		} else if resolvedURL != path {
			log.Printf("[hls] session %s: using resolved URL for probing and FFmpeg: %s", sessionID, resolvedURL)
			path = resolvedURL
		}
	}

	// Unified probe: extract duration, color metadata, audio/subtitle streams in a single ffprobe call
	// This replaces 4 separate ffprobe invocations with 1, significantly reducing playback start time
	var duration float64
	var probeData *UnifiedProbeResult
	if m.ffprobePath != "" && (m.streamer != nil || isExternalURL) {
		log.Printf("[hls] running unified probe for session %s path=%q", sessionID, path)
		if pd, err := m.probeAllMetadata(ctx, path); err == nil && pd != nil {
			probeData = pd
			duration = pd.Duration
			log.Printf("[hls] unified probe for session %s: duration=%.2fs colorTransfer=%q audioStreams=%d",
				sessionID, duration, pd.ColorTransfer, len(pd.AudioStreams))

			// Check for incorrect color tagging on DV Profile 8 content
			// Some re-encodes (e.g., YTS) have DV RPU data but wrong color metadata (bt709 instead of smpte2084)
			// The DV RPU's color transforms are designed for HDR base layer, causing saturated colors when applied to bt709
			// NOTE: Only apply this check for Profile 8 (dvhe.08.xx) with explicit bt709 tagging
			// Profile 5 (dvhe.05.xx) uses dual-layer and may have empty color metadata - that's expected
			isProfile8 := strings.HasPrefix(dvProfile, "dvhe.08")
			if hasDV && isProfile8 && pd.ColorTransfer == "bt709" {
				log.Printf("[hls] session %s: WARNING - DV Profile 8 content has bt709 color tagging, disabling DV to prevent saturated colors", sessionID)
				hasDV = false
				hasHDR = true // DV Profile 8 has HDR10 fallback, enable HDR mode
			}
		} else if err != nil {
			log.Printf("[hls] failed unified probe for session %s: %v", sessionID, err)
		}
	}

	if math.IsNaN(startOffset) || math.IsInf(startOffset, 0) || startOffset < 0 {
		startOffset = 0
	}
	if duration > 0 && startOffset >= duration {
		startOffset = math.Max(duration-4, 0)
	}

	now := time.Now()
	// Use provided transcodingOffset if valid, otherwise default to startOffset
	// transcodingOffset may differ from startOffset when probed keyframe position is used
	actualTranscodingOffset := startOffset
	if transcodingOffset > 0 {
		actualTranscodingOffset = transcodingOffset
	}

	session := &HLSSession{
		ID:                  sessionID,
		Path:                path,
		OriginalPath:        originalPath,
		OutputDir:           outputDir,
		CreatedAt:           now,
		LastAccess:          now,
		Cancel:              cancel,
		HasDV:               hasDV,
		DVProfile:           dvProfile,
		HasHDR:              hasHDR,
		Duration:            duration,
		StartOffset:         startOffset,
		TranscodingOffset:   actualTranscodingOffset, // May differ from StartOffset if keyframe-aligned
		ActualStartOffset:   actualTranscodingOffset, // For subtitle sync
		ProfileID:           profileID,
		ProfileName:         profileName,
		ClientIP:            clientIP,
		AudioTrackIndex:     audioTrackIndex,
		SubtitleTrackIndex:  subtitleTrackIndex,
		StreamStartTime:      now,
		LastSegmentRequest:      now, // Initialize to now to avoid immediate timeout
		MinSegmentRequested:     -1,  // Initialize to -1 (no segments requested yet)
		MaxSegmentRequested:     -1,  // Initialize to -1 (no segments requested yet)
		LastPlaybackSegment:     -1,  // Initialize to -1 (no keepalive time reported yet)
		LastSegmentServed:       -1,  // Initialize to -1 (no segments served yet)
		EarliestBufferedSegment: -1,  // Initialize to -1 (no buffer info reported yet)
		ProbeData:               probeData, // Cache unified probe results for startTranscoding
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Start FFmpeg transcoding in background with background context
	go func() {
		if err := m.startTranscoding(bgCtx, session, forceAAC); err != nil {
			log.Printf("[hls] session %s transcoding failed: %v", sessionID, err)
			session.mu.Lock()
			session.Completed = true
			session.mu.Unlock()
		}
	}()

	log.Printf("[hls] created session %s for path %q (DV=%v, duration=%.2fs, startOffset=%.2fs)", sessionID, path, hasDV, duration, startOffset)

	// Return immediately - modern HLS players (AVPlayer, ExoPlayer) handle empty playlists
	// by polling until segments are available. This eliminates the 5-6 second blocking wait.
	log.Printf("[hls] session %s: returning immediately, FFmpeg transcoding in background", sessionID)

	// Note: For HLS sessions, FFmpeg will always start segment numbering from 0
	// The actual start offset is stored in session.StartOffset for the frontend to use
	// The frontend should seek to the start offset after loading the HLS stream
	return session, nil
}

// CreateLiveSession creates an HLS session for live TV streams
// Unlike VOD sessions, live sessions don't have a known duration and don't support seeking
func (m *HLSManager) CreateLiveSession(ctx context.Context, liveURL string) (*HLSSession, error) {
	sessionID := generateSessionID()
	outputDir := filepath.Join(m.baseDir, sessionID)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	bgCtx, cancel := context.WithCancel(context.Background())

	now := time.Now()
	session := &HLSSession{
		ID:                      sessionID,
		Path:                    liveURL,
		OriginalPath:            liveURL,
		OutputDir:               outputDir,
		CreatedAt:               now,
		LastAccess:              now,
		Cancel:                  cancel,
		IsLive:                  true,
		Duration:                0, // Unknown for live
		StartOffset:             0,
		TranscodingOffset:       0,
		StreamStartTime:         now,
		LastSegmentRequest:      now,
		MinSegmentRequested:     -1,
		MaxSegmentRequested:     -1,
		LastPlaybackSegment:     -1,
		LastSegmentServed:       -1,
		EarliestBufferedSegment: -1,
		AudioTrackIndex:         -1, // Use default
		SubtitleTrackIndex:      -1, // No subtitles for live TV
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Start live transcoding in background
	go func() {
		if err := m.startLiveTranscoding(bgCtx, session); err != nil {
			log.Printf("[hls] live session %s transcoding failed: %v", sessionID, err)
			session.mu.Lock()
			session.Completed = true
			session.mu.Unlock()
		}
	}()

	log.Printf("[hls] created live session %s for URL %q", sessionID, liveURL)
	return session, nil
}

// startLiveTranscoding starts FFmpeg for live TV HLS output
func (m *HLSManager) startLiveTranscoding(ctx context.Context, session *HLSSession) error {
	log.Printf("[hls] live session %s: starting live transcoding for %s", session.ID, session.Path)

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	segmentPattern := filepath.Join(session.OutputDir, "segment%d.ts")

	// Build FFmpeg args optimized for live input
	args := []string{
		"-nostdin",
		"-y",
		"-loglevel", "warning",
		// Live input options
		"-fflags", "+genpts+discardcorrupt",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "3",
		"-i", session.Path,
		// Output options - copy video, transcode audio to AAC for compatibility
		"-c:v", "copy",
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "128k",
		"-ar", "48000",
		// HLS output
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "10", // Keep last 10 segments for live
		"-hls_flags", "delete_segments+append_list",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	}

	log.Printf("[hls] live session %s: starting FFmpeg with args: %v", session.ID, args)

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
	cmd.Dir = session.OutputDir

	// Capture stderr for logging
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	session.mu.Lock()
	session.FFmpegCmd = cmd
	session.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	session.mu.Lock()
	session.FFmpegPID = cmd.Process.Pid
	session.mu.Unlock()

	log.Printf("[hls] live session %s: FFmpeg started (PID=%d)", session.ID, cmd.Process.Pid)

	// Log stderr in background
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				log.Printf("[hls] live session %s: FFmpeg: %s", session.ID, string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
	}()

	// Start idle timeout goroutine - kills FFmpeg if no segments requested for 30s
	idleDone := make(chan struct{})
	go func() {
		defer close(idleDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				session.mu.RLock()
				lastRequest := session.LastSegmentRequest
				segmentCount := session.SegmentRequestCount
				session.mu.RUnlock()

				idleTime := time.Since(lastRequest)

				// Startup timeout: if no segment requests after 30s, kill FFmpeg
				if segmentCount == 0 && idleTime > hlsStartupTimeout {
					log.Printf("[hls] live session %s: STARTUP_TIMEOUT - no segment requests after %v",
						session.ID, idleTime)
					session.mu.Lock()
					session.IdleTimeoutTriggered = true
					session.mu.Unlock()
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					return
				}

				// Idle timeout: if no segment requests for 30s after playback started
				if segmentCount > 0 && idleTime > hlsIdleTimeout {
					log.Printf("[hls] live session %s: IDLE_TIMEOUT - no requests for %v (%d segments served)",
						session.ID, idleTime, segmentCount)
					session.mu.Lock()
					session.IdleTimeoutTriggered = true
					session.mu.Unlock()
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					return
				}
			}
		}
	}()

	// Wait for FFmpeg to complete
	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[hls] live session %s: FFmpeg stopped (context cancelled)", session.ID)
			return nil
		}
		return fmt.Errorf("ffmpeg exited with error: %w", err)
	}

	log.Printf("[hls] live session %s: FFmpeg completed normally", session.ID)
	return nil
}

// waitForFirstSegment polls for the first HLS segment to be available
// This ensures AVPlayer won't get an empty playlist and stall
func (m *HLSManager) waitForFirstSegment(ctx context.Context, session *HLSSession) error {
	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	initPath := filepath.Join(session.OutputDir, "init.mp4")
	segment0Path := filepath.Join(session.OutputDir, "segment0.m4s")
	segment0TsPath := filepath.Join(session.OutputDir, "segment0.ts")

	// Use a longer timeout for external URLs (Real-Debrid etc) since FFmpeg needs to buffer more data
	// Don't use the request context since the client may timeout before we're ready
	isExternalURL := strings.HasPrefix(session.Path, "http://") || strings.HasPrefix(session.Path, "https://")
	timeout := 15 * time.Second
	if isExternalURL {
		timeout = 30 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	pollInterval := 100 * time.Millisecond

	log.Printf("[hls] session %s: waiting for first segment to be ready (timeout=%v, external=%v)", session.ID, timeout, isExternalURL)

	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timeout waiting for first segment")
		default:
		}

		// Check if playlist exists and has content
		playlistInfo, err := os.Stat(playlistPath)
		if err != nil || playlistInfo.Size() < 100 {
			time.Sleep(pollInterval)
			continue
		}

		// For fMP4 (DV/HDR content), check for init.mp4 and segment0.m4s
		if session.HasDV || session.HasHDR {
			initInfo, initErr := os.Stat(initPath)
			segInfo, segErr := os.Stat(segment0Path)

			if initErr == nil && initInfo.Size() > 0 && segErr == nil && segInfo.Size() > 0 {
				log.Printf("[hls] session %s: first fMP4 segment ready (init=%d bytes, seg0=%d bytes, playlist=%d bytes)",
					session.ID, initInfo.Size(), segInfo.Size(), playlistInfo.Size())

				// Fix DV codec tag when init is ready
				if session.HasDV {
					if err := m.fixDVCodecTag(session); err != nil {
						log.Printf("[hls] session %s: warning - failed to fix DV codec tag: %v", session.ID, err)
					}
				}

				// Parse actual start offset from tfdt box for subtitle sync
				// FFmpeg seeks to nearest keyframe, so actual start may differ from requested StartOffset
				if session.StartOffset > 0 {
					actualStart, err := parseActualStartOffset(initPath, segment0Path)
					if err != nil {
						log.Printf("[hls] session %s: warning - could not parse actual start offset: %v (using requested: %.3fs)",
							session.ID, err, session.StartOffset)
						session.ActualStartOffset = session.StartOffset
					} else {
						delta := actualStart - session.StartOffset
						log.Printf("[hls] session %s: actual start offset: %.3fs (requested: %.3fs, delta: %.3fs)",
							session.ID, actualStart, session.StartOffset, delta)
						session.ActualStartOffset = actualStart
					}
				} else {
					session.ActualStartOffset = 0
				}

				return nil
			}
		} else {
			// For regular TS segments
			segInfo, segErr := os.Stat(segment0TsPath)
			if segErr == nil && segInfo.Size() > 0 {
				log.Printf("[hls] session %s: first TS segment ready (seg0=%d bytes, playlist=%d bytes)",
					session.ID, segInfo.Size(), playlistInfo.Size())
				return nil
			}
		}

		time.Sleep(pollInterval)
	}
}

// fixDVCodecTag modifies the init.mp4 to replace hev1 codec tag with dvhe/dvh1
// iOS AVPlayer requires the proper dvhe/dvh1 tag to enable Dolby Vision processing
func (m *HLSManager) fixDVCodecTag(session *HLSSession) error {
	initPath := filepath.Join(session.OutputDir, "init.mp4")

	data, err := os.ReadFile(initPath)
	if err != nil {
		return fmt.Errorf("read init segment: %w", err)
	}

	// Determine target codec tag based on DV profile
	// Profile 5/7: dvhe, Profile 8: dvh1
	var oldTag, newTag []byte
	if strings.HasPrefix(session.DVProfile, "dvhe.05") || strings.HasPrefix(session.DVProfile, "dvhe.07") {
		oldTag = []byte("hev1")
		newTag = []byte("dvhe")
	} else {
		oldTag = []byte("hev1")
		newTag = []byte("dvh1")
	}

	modified := bytes.Replace(data, oldTag, newTag, -1)
	if bytes.Equal(data, modified) {
		log.Printf("[hls] session %s: no hev1 tag found in init segment (may already be correct)", session.ID)
		return nil
	}

	if err := os.WriteFile(initPath, modified, 0644); err != nil {
		return fmt.Errorf("write init segment: %w", err)
	}

	log.Printf("[hls] session %s: fixed DV codec tag (hev1 -> %s)", session.ID, string(newTag))
	return nil
}

// startTranscoding begins FFmpeg HLS transcoding
func (m *HLSManager) startTranscoding(ctx context.Context, session *HLSSession, forceAAC bool) error {
	startTime := time.Now()

	// Cache forceAAC for recovery restarts
	session.mu.Lock()
	session.forceAAC = forceAAC
	session.mu.Unlock()

	log.Printf("[hls] session %s: starting transcoding pipeline", session.ID)
	log.Printf("[hls] session %s: initial memory stats - goroutines=%d", session.ID, runtime.NumGoroutine())

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	log.Printf("[hls] session %s: memory - alloc=%d MB, sys=%d MB, numGC=%d",
		session.ID, memStats.Alloc/1024/1024, memStats.Sys/1024/1024, memStats.NumGC)

	if session.TranscodingOffset > 0 {
		log.Printf("[hls] session %s: applying transcoding offset %.3fs", session.ID, session.TranscodingOffset)
	}

	// Use cached probe data from CreateSession if available, otherwise probe now (recovery case)
	var audioStreams []audioStreamInfo
	var subtitleStreams []subtitleStreamInfo
	var hasTrueHD, hasCompatibleAudio bool

	if session.ProbeData != nil {
		// Use cached unified probe results - no additional ffprobe calls needed
		audioStreams = session.ProbeData.AudioStreams
		subtitleStreams = session.ProbeData.SubtitleStreams
		hasTrueHD = session.ProbeData.HasTrueHD
		hasCompatibleAudio = session.ProbeData.HasCompatibleAudio
		log.Printf("[hls] session %s: using cached probe data (audioStreams=%d, subStreams=%d, hasTrueHD=%v, hasCompatibleAudio=%v)",
			session.ID, len(audioStreams), len(subtitleStreams), hasTrueHD, hasCompatibleAudio)
	} else {
		// Fallback: probe now (for recovery restarts or if unified probe failed)
		log.Printf("[hls] session %s: no cached probe data, probing now", session.ID)
		audioStreams, hasTrueHD, hasCompatibleAudio, _ = m.probeAudioStreams(ctx, session.Path)
		if session.SubtitleTrackIndex >= 0 {
			if streams, err := m.probeSubtitleStreams(ctx, session.Path); err == nil {
				subtitleStreams = streams
			} else {
				log.Printf("[hls] session %s: subtitle probe failed: %v", session.ID, err)
			}
		}
	}

	if hasTrueHD {
		log.Printf("[hls] session %s: TrueHD audio detected, will handle appropriately", session.ID)
		if !hasCompatibleAudio {
			// Force AAC transcoding if no compatible audio found
			log.Printf("[hls] session %s: no compatible audio found, forcing AAC transcoding", session.ID)
			forceAAC = true
		}
	}

	// For fMP4 output (DV/HDR), if audio is not compatible with MP4 container (e.g., pcm_bluray),
	// force AAC transcoding to avoid "codec not currently supported in container" errors
	if !hasCompatibleAudio && (session.HasDV || session.HasHDR) {
		log.Printf("[hls] session %s: fMP4 output with incompatible audio codec, forcing AAC transcoding", session.ID)
		forceAAC = true
	}

	// If audio probe failed (no streams returned) and we're using fMP4 output (DV/HDR),
	// force AAC transcoding to avoid potential TrueHD-in-MP4 experimental codec errors.
	// TrueHD in MP4/fMP4 requires -strict -2 and likely won't play on Apple devices anyway.
	if len(audioStreams) == 0 && (session.HasDV || session.HasHDR) {
		log.Printf("[hls] session %s: audio probe returned no streams and fMP4 output required; forcing AAC transcoding for safety", session.ID)
		forceAAC = true
	}

	// For seeking to work with -c:v copy, we need a seekable input
	// Check if we can get a direct HTTP URL instead of using a pipe
	log.Printf("[hls] session %s: checking for direct URL support (transcodingOffset=%.3f)", session.ID, session.TranscodingOffset)
	directURL, hasDirectURL := m.getDirectURL(ctx, session)
	if hasDirectURL {
		log.Printf("[hls] session %s: got direct URL: %s", session.ID, directURL)
	} else {
		log.Printf("[hls] session %s: no direct URL available, using pipe", session.ID)
	}

	var resp *streaming.Response
	var usingPipe bool
	var headerPrefix []byte
	var requireMatroskaAlign bool
	var proxyURL string       // URL for FFmpeg to use (via throttling proxy)
	var proxy *throttlingProxy // proxy server to close when done

	// For direct URLs, use a throttling proxy so FFmpeg can use HTTP Range requests for seeking
	// while we still control the download speed
	if hasDirectURL {
		log.Printf("[hls] session %s: setting up throttling proxy for direct URL: %s", session.ID, directURL)

		var err error
		proxy, proxyURL, err = newThrottlingProxy(directURL, session)
		if err != nil {
			log.Printf("[hls] session %s: failed to create throttling proxy: %v, falling back to pipe", session.ID, err)
			hasDirectURL = false // Fall through to pipe handling
		} else {
			log.Printf("[hls] session %s: throttling proxy ready at %s", session.ID, proxyURL)
		}
	}

	if !hasDirectURL {
		// Fall back to pipe streaming only if direct URL not available
		providerStartTime := time.Now()
		log.Printf("[hls] session %s: requesting stream from provider", session.ID)

		// Calculate byte offset from time offset for seeking support
		var rangeHeader string
		if session.TranscodingOffset > 0 && session.Duration > 0 {
			// Get file size for byte offset calculation
			headResp, err := m.streamer.Stream(ctx, streaming.Request{
				Path:   session.Path,
				Method: http.MethodHead,
			})
			if err == nil && headResp != nil {
				fileSize := headResp.ContentLength
				headResp.Close()

				if fileSize > 0 {
					// Calculate approximate byte offset: (fileSize / duration) * transcodingOffset
					byteOffset := int64(float64(fileSize) / session.Duration * session.TranscodingOffset)

					if byteOffset > 0 && supportsPipeRange(session.Path) {
						if isMatroskaPath(session.Path) {
							if byteOffset <= matroskaHeaderPrefixBytes {
								log.Printf("[hls] session %s: matroska offset %d too small for ranged pipe; streaming from start", session.ID, byteOffset)
								byteOffset = 0
								headerPrefix = nil
								requireMatroskaAlign = false
							} else {
								headerLen := matroskaHeaderPrefixBytes
								if headerLen > byteOffset {
									headerLen = byteOffset
								}

								if prefix, err := m.fetchHeaderPrefix(ctx, session.Path, headerLen); err != nil {
									log.Printf("[hls] session %s: failed to fetch matroska header prefix: %v (falling back to full stream)", session.ID, err)
									byteOffset = 0
									headerPrefix = nil
									requireMatroskaAlign = false
								} else if len(prefix) == 0 {
									log.Printf("[hls] session %s: matroska header prefix empty, disabling ranged pipe", session.ID)
									byteOffset = 0
									headerPrefix = nil
									requireMatroskaAlign = false
								} else {
									headerPrefix = prefix
									requireMatroskaAlign = true
									log.Printf("[hls] session %s: prefetched %d bytes of matroska header for ranged seek", session.ID, len(prefix))

									if byteOffset > matroskaSeekBackoffBytes {
										byteOffset -= matroskaSeekBackoffBytes
										log.Printf("[hls] session %s: backing off %d bytes to help align matroska cluster (transcodingOffset=%.3fs)",
											session.ID, matroskaSeekBackoffBytes, session.TranscodingOffset)
									} else {
										log.Printf("[hls] session %s: matroska offset %d smaller than backoff; streaming from start", session.ID, byteOffset)
										byteOffset = 0
										headerPrefix = nil
										requireMatroskaAlign = false
									}
								}
							}
						}

						if byteOffset > 0 {
							rangeHeader = fmt.Sprintf("bytes=%d-", byteOffset)
							log.Printf("[hls] session %s: seeking pipe input from byte %d (time %.3fs, fileSize %d, duration %.3fs)",
								session.ID, byteOffset, session.TranscodingOffset, fileSize, session.Duration)
						}
					} else if byteOffset > 0 {
						log.Printf("[hls] session %s: container %q does not support ranged pipe seeks; streaming from start", session.ID, filepath.Ext(session.Path))
					}
				}
			}
		}

		streamResp, err := m.streamer.Stream(ctx, streaming.Request{
			Path:        session.Path,
			Method:      http.MethodGet,
			RangeHeader: rangeHeader,
		})
		if err != nil {
			log.Printf("[hls] session %s: provider stream failed after %v: %v",
				session.ID, time.Since(providerStartTime), err)
			return fmt.Errorf("provider stream: %w", err)
		}
		resp = streamResp
		defer resp.Close()
		usingPipe = true

		log.Printf("[hls] session %s: provider stream established in %v", session.ID, time.Since(providerStartTime))
	}

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	segmentPattern := filepath.Join(session.OutputDir, "segment%d.ts")

	// Build FFmpeg args for HLS output with Dolby Vision support
	args := []string{
		"-nostdin",
		"-y", // Overwrite output files - prevents race condition with on-demand subtitle extraction
		"-loglevel", "error",
		// A/V sync flags: generate PTS if missing, discard corrupt packets
		"-fflags", "+genpts+discardcorrupt",
	}
	// NOTE: -strict unofficial is added AFTER -i as an output option (see below)
	// Placing it before -i doesn't enable writing dvcC boxes to the output

	// Seeking strategy - hybrid approach:
	// - Small seeks (< 30s): OUTPUT seeking (-ss after -i) - faster startup, no HTTP Range delays
	// - Large seeks (>= 30s): INPUT seeking (-ss before -i) - uses HTTP Range to skip data
	const outputSeekThreshold = 30.0 // seconds

	useOutputSeeking := session.TranscodingOffset > 0 && session.TranscodingOffset < outputSeekThreshold

	// For INPUT seeking, add -ss before -i
	if session.TranscodingOffset >= outputSeekThreshold {
		args = append(args, "-ss", fmt.Sprintf("%.3f", session.TranscodingOffset))
		log.Printf("[hls] session %s: using INPUT seeking to %.3fs (HTTP Range, skips data)", session.ID, session.TranscodingOffset)
	}

	// Add input source - use proxy URL if available, otherwise use pipe
	if proxyURL != "" {
		args = append(args, "-i", proxyURL)
		log.Printf("[hls] session %s: FFmpeg input set to proxy URL: %s", session.ID, proxyURL)
	} else {
		args = append(args, "-i", "pipe:0")
		log.Printf("[hls] session %s: FFmpeg input set to pipe:0", session.ID)
	}

	// For OUTPUT seeking, add -ss after -i
	if useOutputSeeking {
		args = append(args, "-ss", fmt.Sprintf("%.3f", session.TranscodingOffset))
		log.Printf("[hls] session %s: using OUTPUT seeking to %.3fs (faster startup, reads from start)", session.ID, session.TranscodingOffset)
	}

	// If we're seeking and know the total duration, tell FFmpeg how much content to expect
	// This ensures the HLS playlist reports the correct remaining duration
	if session.TranscodingOffset > 0 && session.Duration > 0 {
		remainingDuration := session.Duration - session.TranscodingOffset
		if remainingDuration > 0 {
			args = append(args, "-t", fmt.Sprintf("%.3f", remainingDuration))
			log.Printf("[hls] session %s: limiting duration to remaining %.3fs (total=%.3fs, offset=%.3fs)",
				session.ID, remainingDuration, session.Duration, session.TranscodingOffset)
		}
	}

	// Normalize all output stream timestamps to start at 0
	// This ensures A/V sync when transcoding TrueHD/DTS audio (which have variable timing)
	// and helps maintain subtitle sync across seek operations
	args = append(args, "-start_at_zero")

	args = append(args,
		"-map", "0:v:0", // Map primary video stream
	)

	// Audio track selection
	mappedSpecificAudio := false
	if session.AudioTrackIndex >= 0 {
		// Find the requested audio stream in our probed list
		var selectedStream *audioStreamInfo
		for i := range audioStreams {
			if audioStreams[i].Index == session.AudioTrackIndex {
				selectedStream = &audioStreams[i]
				break
			}
		}

		if selectedStream != nil {
			// Check if this is a TrueHD track
			isTrueHD := selectedStream.Codec == "truehd" || selectedStream.Codec == "mlp"

			if isTrueHD {
				// TrueHD selected - we need to transcode it
				log.Printf("[hls] session %s: requested audio track %d is TrueHD; will transcode to AAC", session.ID, session.AudioTrackIndex)
				// Map by absolute stream index and transcode
				audioMap := fmt.Sprintf("0:%d", selectedStream.Index)
				args = append(args, "-map", audioMap)
				mappedSpecificAudio = true
			} else {
				// Compatible codec selected - map it directly by absolute stream index
				// This avoids issues with TrueHD filtering affecting relative indices
				audioMap := fmt.Sprintf("0:%d", selectedStream.Index)
				args = append(args, "-map", audioMap)
				mappedSpecificAudio = true
				log.Printf("[hls] session %s: mapping specific audio stream (streamIndex=%d codec=%s)",
					session.ID, selectedStream.Index, selectedStream.Codec)
			}
		} else if len(audioStreams) > 0 {
			log.Printf("[hls] session %s: requested audio stream index %d not found among %d audio streams; defaulting to automatic mapping",
				session.ID, session.AudioTrackIndex, len(audioStreams))
		} else {
			log.Printf("[hls] session %s: audio stream metadata unavailable for requested index %d; defaulting to automatic mapping",
				session.ID, session.AudioTrackIndex)
		}
	}

	if !mappedSpecificAudio {
		// When no specific audio track is selected, default to the first audio stream
		// This ensures consistent behavior with the frontend's expectations and avoids
		// the Expo Video player defaulting to the first track in a multi-track manifest
		if hasTrueHD && hasCompatibleAudio {
			// Find the first compatible audio stream (excluding TrueHD and commentary tracks)
			log.Printf("[hls] session %s: no specific audio track selected, defaulting to first compatible stream", session.ID)
			compatibleCodecs := map[string]bool{
				"aac":  true,
				"ac3":  true,
				"eac3": true,
				"mp3":  true,
			}
			mappedAudio := false
			// First pass: find compatible non-commentary track
			for _, stream := range audioStreams {
				if compatibleCodecs[stream.Codec] && !isHLSCommentaryTrack(stream.Title) {
					audioMap := fmt.Sprintf("0:%d", stream.Index)
					args = append(args, "-map", audioMap)
					log.Printf("[hls] session %s: mapped first compatible audio stream %d (codec=%s)",
						session.ID, stream.Index, stream.Codec)
					mappedAudio = true
					break
				}
			}
			// Second pass: fallback to any compatible track (including commentary)
			if !mappedAudio {
				for _, stream := range audioStreams {
					if compatibleCodecs[stream.Codec] {
						audioMap := fmt.Sprintf("0:%d", stream.Index)
						args = append(args, "-map", audioMap)
						log.Printf("[hls] session %s: mapped compatible audio stream %d (codec=%s, fallback including commentary)",
							session.ID, stream.Index, stream.Codec)
						break
					}
				}
			}
		} else {
			// Map only the first audio stream
			args = append(args, "-map", "0:a:0")
			log.Printf("[hls] session %s: no specific audio track selected, mapped first audio stream", session.ID)
		}
	}

	args = append(args,
		"-c:v", "copy", // Copy video codec
	)

	// For Dolby Vision and HDR10, we MUST use fMP4 segments (not MPEG-TS)
	// - DV: preserves Dolby Vision metadata
	// - HDR10: iOS AVPlayer can't properly decode HEVC in MPEG-TS segments
	var segmentExt string
	needsFmp4 := session.HasDV || session.HasHDR
	if session.HasDV && !session.DVDisabled {
		segmentExt = ".m4s"
		// Use correct codec tag based on DV profile:
		// - dvh1: Profile 8 with HDR10-compatible base layer (bl_compat_id=1,2)
		// - dvhe: Profile 5, 7 without HDR10-compatible base layer
		dvTag := "dvh1"
		if strings.HasPrefix(session.DVProfile, "dvhe.05") || strings.HasPrefix(session.DVProfile, "dvhe.07") {
			dvTag = "dvhe"
		}
		// For DV content, -strict unofficial enables FFmpeg to write dvcC/dvvC boxes
		// IMPORTANT: -strict unofficial MUST be placed AFTER -i (as output option, not input option)
		// NOTE: hevc_metadata filter is safe to use with DV - it only modifies VUI color parameters
		// and does NOT interfere with dvcC box generation (tested). This fixes sources with
		// incorrect color metadata (e.g., bt709 instead of bt2020/PQ) which cause saturated colors.
		// Do NOT use dovi_rpu filter as it DOES break dvcC generation.
		args = append(args, "-strict", "unofficial", "-tag:v", dvTag, "-bsf:v", "hevc_metadata=colour_primaries=9:transfer_characteristics=16:matrix_coefficients=9")
		log.Printf("[hls] session %s: using %s tag with fMP4 segments for Dolby Vision (profile: %s)", session.ID, dvTag, session.DVProfile)
	} else if session.HasHDR || (session.HasDV && session.DVDisabled) {
		// Also handles DV fallback - DV Profile 8 has HDR10 base layer that plays fine without DV metadata
		segmentExt = ".m4s"
		// Use hevc_metadata to ensure proper BT.2020/PQ color signaling for HDR10 content
		if session.HDRMetadataDisabled {
			// Skip hevc_metadata filter if it failed previously (malformed SEI data)
			// Stream will still play, just without explicit HDR color signaling in fMP4
			args = append(args, "-tag:v", "hvc1")
			log.Printf("[hls] session %s: using hvc1 tag with fMP4 segments WITHOUT hevc_metadata filter (disabled due to malformed SEI)", session.ID)
		} else {
			args = append(args, "-tag:v", "hvc1", "-bsf:v", "hevc_metadata=colour_primaries=9:transfer_characteristics=16:matrix_coefficients=9")
			if session.DVDisabled {
				log.Printf("[hls] session %s: using hvc1 tag with fMP4 segments and HDR10 color metadata (DV disabled, using HDR10 base layer)", session.ID)
			} else {
				log.Printf("[hls] session %s: using hvc1 tag with fMP4 segments with HDR10 color metadata", session.ID)
			}
		}
	} else {
		// TESTING: Use fMP4 for all content (normally SDR uses .ts MPEG-TS segments)
		// This allows testing HLS with react-native-video for SDR content
		// Don't force codec tag - let FFmpeg auto-detect (works for both H.264 and HEVC)
		needsFmp4 = true
		segmentExt = ".m4s"
		log.Printf("[hls] session %s: using fMP4 segments for SDR content (testing, no codec tag forced)", session.ID)
	}

	// Audio handling
	audioCodecHandled := false

	// Check if a specific TrueHD track was selected
	if mappedSpecificAudio && session.AudioTrackIndex >= 0 {
		for i := range audioStreams {
			if audioStreams[i].Index == session.AudioTrackIndex {
				isTrueHD := audioStreams[i].Codec == "truehd" || audioStreams[i].Codec == "mlp"
				if isTrueHD {
					// Transcode the selected TrueHD track to AAC
					// Must specify channel_layout for iOS AVPlayer compatibility (otherwise shows "media may be damaged")
					// TrueHD has variable timing - use aresample filter with async to maintain A/V sync
					// async=1000 allows up to 1000 samples of drift correction per second
					// Note: -start_at_zero (set earlier) normalizes all stream timestamps for proper A/V sync
					log.Printf("[hls] session %s: transcoding selected TrueHD track to AAC", session.ID)
					args = append(args,
						"-af", "aresample=async=1000",
						"-c:a", "aac", "-ac", "6", "-ar", "48000", "-channel_layout", "5.1", "-b:a", "192k")
					audioCodecHandled = true
				}
				break
			}
		}
	}

	if !audioCodecHandled {
		if forceAAC {
			// Transcode first audio to AAC, copy others
			// Must specify channel_layout for iOS AVPlayer compatibility
			// Use aresample filter with async for proper A/V sync during transcoding
			args = append(args,
				"-af", "aresample=async=1000",
				"-c:a:0", "aac", "-ac:a:0", "6", "-ar:a:0", "48000", "-channel_layout:a:0", "5.1", "-b:a:0", "192k",
				"-c:a:1", "copy")
		} else if hasTrueHD && !hasCompatibleAudio {
			// If only TrueHD exists, we must transcode it
			// Must specify channel_layout for iOS AVPlayer compatibility (otherwise shows "media may be damaged")
			// TrueHD has variable timing - use aresample filter with async to maintain A/V sync
			log.Printf("[hls] session %s: transcoding TrueHD to AAC (no compatible alternative)", session.ID)
			args = append(args,
				"-af", "aresample=async=1000",
				"-c:a", "aac", "-ac", "6", "-ar", "48000", "-channel_layout", "5.1", "-b:a", "192k")
		} else {
			// Copy compatible audio
			args = append(args, "-c:a", "copy")
		}
	}

	// Subtitle handling: All subtitles are served via sidecar VTT files for consistent overlay rendering.
	// - fMP4 (Dolby Vision/HDR): Extract ALL text-based tracks upfront as additional ffmpeg outputs
	// - MPEG-TS: On-demand extraction via extractSubtitleTrack when a track is requested
	type sidecarSubtitle struct {
		streamIndex int    // Absolute stream index (for -map 0:N)
		codec       string // Codec name
	}
	var sidecarSubtitles []sidecarSubtitle

	// All subtitles are served via sidecar VTT files (on-demand extraction).
	// For fMP4, we also do upfront extraction as additional ffmpeg outputs.
	// For MPEG-TS, we skip embedding entirely and rely on sidecar extraction.
	// Text-based subtitle codecs that can be converted to WebVTT
	// Using a whitelist approach to avoid unknown bitmap codecs slipping through
	textSubtitleCodecs := map[string]bool{
		"subrip": true, "srt": true, "ass": true, "ssa": true,
		"webvtt": true, "vtt": true, "mov_text": true, "text": true,
		"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
		"mpl2": true, "pjs": true, "realtext": true, "stl": true,
		"subviewer": true, "subviewer1": true, "vplayer": true,
	}

	// Extract ALL text-based subtitle tracks to sidecar VTT files during streaming
	// This allows track switching without re-downloading the source file
	// Works for both fMP4 and MPEG-TS - progressive extraction with flush_packets
	for _, stream := range subtitleStreams {
		if !textSubtitleCodecs[stream.Codec] {
			log.Printf("[hls] session %s: skipping non-text subtitle stream %d (codec=%q) for sidecar extraction",
				session.ID, stream.Index, stream.Codec)
			continue
		}
		sidecarSubtitles = append(sidecarSubtitles, sidecarSubtitle{
			streamIndex: stream.Index,
			codec:       stream.Codec,
		})
	}
	if len(sidecarSubtitles) > 0 {
		log.Printf("[hls] session %s: will extract %d text-based subtitle tracks to sidecar VTT files",
			session.ID, len(sidecarSubtitles))
	} else if len(subtitleStreams) > 0 {
		log.Printf("[hls] session %s: no text-based subtitles found for sidecar extraction (%d non-text streams skipped)",
			session.ID, len(subtitleStreams))
	}

	// Update segment pattern with correct extension
	segmentPattern = filepath.Join(session.OutputDir, "segment%d"+segmentExt)

	// Determine segment start number - normally 0, but for recovery we continue from where we left off
	segmentStartNum := "0"
	session.mu.RLock()
	isRecovery := session.RecoveryAttempts > 0
	session.mu.RUnlock()

	if isRecovery {
		// Find highest existing segment and start from the next one
		highestSegment := m.findHighestSegmentNumber(session)
		if highestSegment >= 0 {
			segmentStartNum = strconv.Itoa(highestSegment + 1)
			log.Printf("[hls] session %s: recovery mode - starting from segment %s", session.ID, segmentStartNum)
		}
	}

	// Increase muxing queue size to prevent A/V desync under load
	// Default is 8 packets which can cause sync issues with variable bitrate streams
	args = append(args, "-max_muxing_queue_size", "1024")

	// HLS output settings
	if needsFmp4 {
		// Use fMP4 segments for Dolby Vision and HDR10
		// iOS AVPlayer requires fMP4 for proper HEVC/HDR playback

		// First output: HLS stream with video and audio only
		args = append(args,
			"-f", "hls",
			"-hls_time", "2",
			"-hls_list_size", "0",
			"-hls_playlist_type", "event",
			"-hls_flags", "independent_segments+temp_file",
			"-hls_segment_type", "fmp4",
			"-hls_fmp4_init_filename", "init.mp4",
			"-hls_segment_filename", segmentPattern,
			"-movflags", "+faststart+frag_keyframe",
			"-start_number", segmentStartNum,
			playlistPath,
		)

		// Additional outputs: Sidecar VTT files for ALL text-based subtitle tracks
		// This allows track switching without re-downloading the source file
		// Each subtitle track is extracted to subtitles_<streamIndex>.vtt
		// Use absolute stream index (0:N) instead of relative subtitle index (0:s:N)
		// because 0:s:N includes bitmap subtitles which are filtered out from our list
		for _, sub := range sidecarSubtitles {
			vttPath := filepath.Join(session.OutputDir, fmt.Sprintf("subtitles_%d.vtt", sub.streamIndex))
			subtitleMap := fmt.Sprintf("0:%d", sub.streamIndex)
			args = append(args,
				"-map", subtitleMap,
				"-c", "webvtt",
				"-f", "webvtt",
				"-flush_packets", "1",
				vttPath,
			)
			log.Printf("[hls] session %s: adding sidecar VTT output at %s (streamIndex=%d codec=%s)",
				session.ID, vttPath, sub.streamIndex, sub.codec)
		}
	} else {
		// Use MPEG-TS segments for non-HDR content
		args = append(args,
			"-f", "hls",
			"-hls_time", "2",
			"-hls_list_size", "0",
			"-hls_playlist_type", "event",
			"-hls_flags", "independent_segments+temp_file",
			"-hls_segment_type", "mpegts",
			"-hls_segment_filename", segmentPattern,
			"-start_number", segmentStartNum,
			playlistPath,
		)

		// Additional outputs: Sidecar VTT files for ALL text-based subtitle tracks
		// Extract progressively during streaming with flush_packets
		for _, sub := range sidecarSubtitles {
			vttPath := filepath.Join(session.OutputDir, fmt.Sprintf("subtitles_%d.vtt", sub.streamIndex))
			subtitleMap := fmt.Sprintf("0:%d", sub.streamIndex)
			args = append(args,
				"-map", subtitleMap,
				"-c", "webvtt",
				"-f", "webvtt",
				"-flush_packets", "1",
				vttPath,
			)
			log.Printf("[hls] session %s: adding sidecar VTT output at %s (streamIndex=%d codec=%s)",
				session.ID, vttPath, sub.streamIndex, sub.codec)
		}
	}

	ffmpegSetupStart := time.Now()
	log.Printf("[hls] session %s: starting FFmpeg with args: %v", session.ID, args)

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
	if usingPipe {
		pipeReader := io.Reader(resp.Body)

		if requireMatroskaAlign {
			alignedReader, dropped, err := alignMatroskaCluster(pipeReader, matroskaMaxClusterScan)
			if err != nil {
				log.Printf("[hls] session %s: failed to locate matroska cluster sync within %d bytes: %v",
					session.ID, matroskaMaxClusterScan, err)
				pipeReader = alignedReader
			} else {
				if dropped > 0 {
					log.Printf("[hls] session %s: aligned matroska cluster after discarding %d bytes", session.ID, dropped)
				}
				pipeReader = alignedReader
			}
		}

		if len(headerPrefix) > 0 {
			log.Printf("[hls] session %s: prepended %d bytes of container header to pipe input", session.ID, len(headerPrefix))
			pipeReader = io.MultiReader(bytes.NewReader(headerPrefix), pipeReader)
		}

		// Wrap with throttled reader to slow down input when buffer gets too far ahead
		// This prevents excessive disk usage from segments generated faster than playback
		throttledPipe := newThrottledReader(pipeReader, session)

		// Wrap with debug reader to track bytes read and detect when source stream ends
		debugPipe := newDebugReader(throttledPipe, session.ID)
		cmd.Stdin = debugPipe
		log.Printf("[hls] session %s: SOURCE_STREAM started (with throttling and debug reader)", session.ID)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[hls] session %s: failed to create stderr pipe: %v", session.ID, err)
		return fmt.Errorf("stderr pipe: %w", err)
	}

	log.Printf("[hls] session %s: starting FFmpeg process", session.ID)
	if err := cmd.Start(); err != nil {
		log.Printf("[hls] session %s: FFmpeg start failed: %v", session.ID, err)
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	session.mu.Lock()
	session.FFmpegCmd = cmd
	session.FFmpegPID = cmd.Process.Pid
	session.mu.Unlock()

	log.Printf("[hls] session %s: FFmpeg started (PID=%d) in %v", session.ID, cmd.Process.Pid, time.Since(ffmpegSetupStart))

	// Channel to signal DV metadata parsing errors (only used when DV is enabled)
	dvErrorCh := make(chan struct{}, 1)
	dvErrorDetected := false

	// Channel to signal hevc_metadata filter errors (malformed SEI data)
	hdrMetadataErrorCh := make(chan struct{}, 1)
	hdrMetadataErrorDetected := false

	// Channel to signal input stream errors (usenet disconnections, broken pipe, etc.)
	inputErrorCh := make(chan struct{}, 1)
	inputErrorDetected := false

	// Log FFmpeg errors with timing
	go func() {
		buf := make([]byte, 4096)
		lastLog := time.Now()
		dvErrorCount := 0
		hdrMetadataErrorCount := 0
		inputErrorCount := 0
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				msg := string(buf[:n])
				log.Printf("[hls] session %s ffmpeg stderr (t+%.1fs): %s",
					session.ID, time.Since(startTime).Seconds(), msg)

				// Detect Dolby Vision RPU parsing errors
				// These indicate malformed DV metadata that we should fall back from
				if session.HasDV && !session.DVDisabled {
					if strings.Contains(msg, "dovi_rpu") && strings.Contains(msg, "Failed") {
						dvErrorCount++
						// Signal after seeing multiple errors to avoid false positives
						if dvErrorCount >= 3 {
							select {
							case dvErrorCh <- struct{}{}:
								log.Printf("[hls] session %s: detected persistent DV metadata parsing errors, will restart without DV", session.ID)
							default:
								// Already signaled
							}
						}
					}
				}

				// Detect hevc_metadata bitstream filter errors
				// These occur when streams have malformed SEI data (missing SPS, corrupt NAL units)
				session.mu.RLock()
				hdrMetadataAlreadyDisabled := session.HDRMetadataDisabled
				session.mu.RUnlock()
				if !hdrMetadataAlreadyDisabled {
					if strings.Contains(msg, "hevc_metadata") && strings.Contains(msg, "Failed") {
						hdrMetadataErrorCount++
						// Signal after seeing multiple errors to avoid false positives
						if hdrMetadataErrorCount >= 3 {
							select {
							case hdrMetadataErrorCh <- struct{}{}:
								log.Printf("[hls] session %s: detected persistent hevc_metadata errors (malformed SEI data), will restart without HDR signaling filter", session.ID)
							default:
								// Already signaled
							}
						}
					}
				}

				// Detect bitstream filter errors (vost/copy errors)
				// These indicate the source stream has malformed data that can't be processed
				// This is a FATAL error - the stream is fundamentally broken and recovery won't help
				isBitstreamError := strings.Contains(msg, "Error applying bitstream filters")
				if isBitstreamError {
					session.mu.Lock()
					session.BitstreamErrors++
					bitstreamCount := session.BitstreamErrors
					alreadyFatal := session.FatalError != ""
					session.mu.Unlock()

					// Mark as fatal after seeing just 3 bitstream errors - this indicates
					// the stream data itself is corrupted, not a transient issue
					if !alreadyFatal && bitstreamCount >= 3 {
						session.mu.Lock()
						session.FatalError = "Stream contains malformed video data that cannot be processed"
						session.FatalErrorTime = time.Now()
						session.mu.Unlock()
						log.Printf("[hls] session %s: FATAL_ERROR - bitstream filter errors indicate corrupted stream data (count: %d)",
							session.ID, bitstreamCount)

						// Kill FFmpeg - no point continuing with a broken stream
						if cmd.Process != nil {
							log.Printf("[hls] session %s: killing FFmpeg due to fatal bitstream error", session.ID)
							_ = cmd.Process.Kill()
						}
					}
				}

				// Detect input stream errors (usenet disconnections, HTTP failures, etc.)
				// These indicate the source stream was interrupted and we should try to recover
				// IMPORTANT: Do NOT treat bitstream filter errors as input errors - they are fatal
				session.mu.RLock()
				inputErrorAlreadyDetected := session.InputErrorDetected
				recoveryAttempts := session.RecoveryAttempts
				session.mu.RUnlock()
				if !inputErrorAlreadyDetected && recoveryAttempts < hlsMaxRecoveryAttempts && !isBitstreamError {
					// Check for various input error patterns (both pipe/usenet and HTTP/debrid)
					// Note: "Invalid data found when processing input" alone could be bitstream errors,
					// so we only match it when NOT preceded by "Error applying bitstream filters"
					isInputError := strings.Contains(msg, "pipe:") && (strings.Contains(msg, "Invalid") || strings.Contains(msg, "Error") || strings.Contains(msg, "end of file")) ||
						(strings.Contains(msg, "Invalid data found when processing input") && !strings.Contains(msg, "bitstream")) ||
						strings.Contains(msg, "Error while decoding stream") ||
						strings.Contains(msg, "av_read_frame") ||
						strings.Contains(msg, "I/O error") ||
						strings.Contains(msg, "Input/output error") ||
						strings.Contains(msg, "Connection reset") ||
						strings.Contains(msg, "Broken pipe") ||
						(strings.Contains(msg, "end of file") && !strings.Contains(msg, "Discarding")) ||
						// HTTP-specific errors (for debrid direct URLs)
						strings.Contains(msg, "HTTP error") ||
						strings.Contains(msg, "Server returned") ||
						strings.Contains(msg, "Connection refused") ||
						strings.Contains(msg, "Connection timed out") ||
						strings.Contains(msg, "Operation timed out") ||
						strings.Contains(msg, "Failed to connect")

					if isInputError {
						inputErrorCount++
						// Signal after seeing the error (no need for multiple, input errors are definitive)
						if inputErrorCount >= 1 {
							select {
							case inputErrorCh <- struct{}{}:
								log.Printf("[hls] session %s: detected input stream error, will attempt recovery (attempt %d/%d)", session.ID, recoveryAttempts+1, hlsMaxRecoveryAttempts)
							default:
								// Already signaled
							}
						}
					}
				}

				// Track if this is progress info (frame=, fps=, etc.)
				if strings.Contains(msg, "frame=") || strings.Contains(msg, "fps=") {
					if time.Since(lastLog) > 5*time.Second {
						log.Printf("[hls] session %s: FFmpeg still processing (PID=%d, elapsed=%v)",
							session.ID, cmd.Process.Pid, time.Since(startTime))
						lastLog = time.Now()
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Monitor for DV errors and kill FFmpeg if detected
	go func() {
		select {
		case <-dvErrorCh:
			dvErrorDetected = true
			session.mu.Lock()
			session.DVDisabled = true
			session.mu.Unlock()
			log.Printf("[hls] session %s: killing FFmpeg due to DV metadata errors", session.ID)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-ctx.Done():
			// Context cancelled, no action needed
		}
	}()

	// Monitor for hevc_metadata errors and kill FFmpeg if detected
	go func() {
		select {
		case <-hdrMetadataErrorCh:
			hdrMetadataErrorDetected = true
			session.mu.Lock()
			session.HDRMetadataDisabled = true
			session.mu.Unlock()
			log.Printf("[hls] session %s: killing FFmpeg due to hevc_metadata errors (malformed SEI)", session.ID)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-ctx.Done():
			// Context cancelled, no action needed
		}
	}()

	// Monitor for input stream errors and kill FFmpeg for recovery
	go func() {
		select {
		case <-inputErrorCh:
			inputErrorDetected = true
			session.mu.Lock()
			session.InputErrorDetected = true
			session.mu.Unlock()
			log.Printf("[hls] session %s: killing FFmpeg due to input stream error (will attempt recovery)", session.ID)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-ctx.Done():
			// Context cancelled, no action needed
		}
	}()

	// Start a goroutine to periodically log performance metrics
	perfDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				session.mu.RLock()
				pid := session.FFmpegPID
				bytesStreamed := session.BytesStreamed
				segmentsCreated := session.SegmentsCreated
				session.mu.RUnlock()

				elapsed := time.Since(startTime)
				var memStats runtime.MemStats
				runtime.ReadMemStats(&memStats)

				log.Printf("[hls] session %s: PERF_CHECK elapsed=%v goroutines=%d memory_alloc=%d MB segments=%d bytes=%d pid=%d",
					session.ID, elapsed, runtime.NumGoroutine(), memStats.Alloc/1024/1024,
					segmentsCreated, bytesStreamed, pid)

				// Try to read /proc/{pid}/stat for CPU usage if available
				if pid > 0 {
					m.logProcessCPU(session.ID, pid)
				}

			case <-perfDone:
				return
			}
		}
	}()

	// Start idle timeout monitoring goroutine
	idleDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second) // Check every 5 seconds
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				session.mu.RLock()
				lastRequest := session.LastSegmentRequest
				segmentCount := session.SegmentRequestCount
				completed := session.Completed
				session.mu.RUnlock()

				// Don't check idle timeout if already completed
				if completed {
					return
				}

				// Don't trigger timeout if we've detected bitstream filter errors (will restart)
				session.mu.RLock()
				hdrMetadataDisabled := session.HDRMetadataDisabled
				dvDisabledForRestart := session.DVDisabled && session.HasDV
				session.mu.RUnlock()
				if hdrMetadataDisabled || dvDisabledForRestart {
					// Skip timeout - error recovery will handle restart
					return
				}

				idleTime := time.Since(lastRequest)
				sessionAge := time.Since(session.CreatedAt)

				// Check for startup timeout: no segments requested within hlsStartupTimeout
				if segmentCount == 0 && sessionAge > hlsStartupTimeout {
					log.Printf("[hls] session %s: STARTUP_TIMEOUT triggered after %v (no segments requested)",
						session.ID, sessionAge)

					session.mu.Lock()
					session.IdleTimeoutTriggered = true
					session.mu.Unlock()

					if session.Cancel != nil {
						session.Cancel()
					}

					if cmd != nil && cmd.Process != nil {
						log.Printf("[hls] session %s: killing startup-timeout FFmpeg process (PID=%d)",
							session.ID, cmd.Process.Pid)
						_ = cmd.Process.Kill()
					}
					return
				}

				// Enforce idle timeout if we've had at least one segment request
				if segmentCount > 0 && idleTime > hlsIdleTimeout {
					log.Printf("[hls] session %s: IDLE_TIMEOUT triggered after %v (last request %v ago, %d segments served)",
						session.ID, hlsIdleTimeout, idleTime, segmentCount)

					session.mu.Lock()
					session.IdleTimeoutTriggered = true
					session.mu.Unlock()

					// Cancel the context to stop FFmpeg
					if session.Cancel != nil {
						session.Cancel()
					}

					// Kill the FFmpeg process if it's still running
					if cmd != nil && cmd.Process != nil {
						log.Printf("[hls] session %s: killing idle FFmpeg process (PID=%d)",
							session.ID, cmd.Process.Pid)
						_ = cmd.Process.Kill()
					}
					return
				}

			case <-idleDone:
				return
			}
		}
	}()

	// Start rate limiting goroutine to pause FFmpeg when too far ahead of player
	rateLimitDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
		defer ticker.Stop()
		var lastSkipLog time.Time

		for {
			select {
			case <-ticker.C:
				session.mu.RLock()
				maxRequested := session.MaxSegmentRequested
				completed := session.Completed
				pid := session.FFmpegPID
				outputDir := session.OutputDir
				// TESTING: hasDV/hasHDR unused since we always use .m4s
				_ = session.HasDV
				_ = session.HasHDR
				session.mu.RUnlock()

				// Don't rate limit if completed or player hasn't requested any segments yet
				if completed || maxRequested < 0 || pid == 0 {
					// Log why we're skipping (every 30 seconds to avoid spam)
					if maxRequested < 0 && time.Since(lastSkipLog) > 30*time.Second {
						log.Printf("[hls] session %s: RATE_LIMIT skipped - no playback position reported (maxRequested=%d). Frontend should send ?time=<seconds> with keepalive.",
							session.ID, maxRequested)
						lastSkipLog = time.Now()
					}
					continue
				}

				// Find segment files on disk
				// TESTING: Always use .m4s for all content
				segmentExt := ".m4s"
				// if hasDV || hasHDR {
				// 	segmentExt = ".m4s"
				// }
				pattern := filepath.Join(outputDir, "segment*"+segmentExt)
				segmentFiles, _ := filepath.Glob(pattern)

				// Find highest segment number from filenames (not just count, since cleanup removes old ones)
				highestSegment := -1
				for _, f := range segmentFiles {
					base := filepath.Base(f)
					var segNum int
					if _, err := fmt.Sscanf(base, "segment%d", &segNum); err == nil {
						if segNum > highestSegment {
							highestSegment = segNum
						}
					}
				}

				// Segment cleanup now happens in ServeSegment after each segment is served.
				// The playlist is filtered to exclude deleted segments.

				// Skip rate limiting if no segments found yet
				if highestSegment < 0 {
					continue
				}

				// SIGSTOP/SIGCONT disabled - using throttledReader for rate limiting instead
				// The throttledReader applies backpressure on the input pipe, which is
				// smoother than pausing/resuming the ffmpeg process
				_ = highestSegment

			case <-rateLimitDone:
				// Ensure FFmpeg is resumed before exiting
				session.mu.RLock()
				paused := session.Paused
				pid := session.FFmpegPID
				session.mu.RUnlock()
				if paused && pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGCONT)
					session.mu.Lock()
					session.Paused = false
					session.mu.Unlock()
					log.Printf("[hls] session %s: resumed FFmpeg on rate limit shutdown", session.ID)
				}
				return
			}
		}
	}()

	// Wait for FFmpeg to complete
	log.Printf("[hls] session %s: waiting for FFmpeg to complete", session.ID)
	waitStart := time.Now()
	err = cmd.Wait()
	waitDuration := time.Since(waitStart)

	// Clean up throttling proxy if we used one
	if proxy != nil {
		log.Printf("[hls] session %s: closing throttling proxy", session.ID)
		proxy.Close()
	}

	// Log FFmpeg exit details
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	thisPid := cmd.Process.Pid
	log.Printf("[hls] session %s: FFMPEG_EXIT - exitCode=%d waitDuration=%v err=%v ctxErr=%v pid=%d",
		session.ID, exitCode, waitDuration, err, ctx.Err(), thisPid)

	// Signal monitoring goroutines to stop
	close(perfDone)
	close(idleDone)
	close(rateLimitDone)

	// Check if this FFmpeg instance is still the current one for this session
	// If not, a seek has replaced this FFmpeg with a new one - just exit quietly
	session.mu.RLock()
	currentPid := session.FFmpegPID
	session.mu.RUnlock()
	if currentPid != thisPid && currentPid != 0 {
		log.Printf("[hls] session %s: FFmpeg (pid=%d) superseded by newer FFmpeg (pid=%d), skipping completion handling",
			session.ID, thisPid, currentPid)
		return nil
	}

	completionTime := time.Since(startTime)

	// Check if we have a fatal error (e.g., bitstream filter errors) - if so, skip ALL recovery
	session.mu.RLock()
	fatalError := session.FatalError
	session.mu.RUnlock()

	if fatalError != "" {
		log.Printf("[hls] session %s: FFmpeg terminated with FATAL ERROR after %v: %s (no recovery possible)",
			session.ID, completionTime, fatalError)
		session.mu.Lock()
		session.Completed = true
		session.mu.Unlock()
		return fmt.Errorf("fatal stream error: %s", fatalError)
	}

	// Check if we killed FFmpeg due to DV errors - if so, restart without DV
	// Use session.DVDisabled (set under lock) to avoid race with the error detection goroutine
	session.mu.RLock()
	dvWasDisabled := session.DVDisabled
	session.mu.RUnlock()

	if dvErrorDetected && dvWasDisabled {
		log.Printf("[hls] session %s: FFmpeg was killed due to DV metadata errors after %v, restarting without DV processing", session.ID, completionTime)

		// Clean up the output directory for fresh start
		files, _ := filepath.Glob(filepath.Join(session.OutputDir, "*"))
		for _, f := range files {
			os.Remove(f)
		}

		// Reset session state for restart
		session.mu.Lock()
		session.FFmpegCmd = nil
		session.FFmpegPID = 0
		session.Completed = false
		session.SegmentsCreated = 0
		session.BytesStreamed = 0
		session.SegmentRequestCount = 0
		session.CreatedAt = time.Now() // Reset so startup timeout doesn't immediately fire
		session.LastSegmentRequest = time.Now()
		session.mu.Unlock()

		// Restart transcoding - DVDisabled is already set to true
		log.Printf("[hls] session %s: restarting transcoding with DV disabled (will use HDR10 base layer)", session.ID)
		return m.startTranscoding(ctx, session, forceAAC)
	}

	// Check if we killed FFmpeg due to hevc_metadata errors - restart without the filter
	session.mu.RLock()
	hdrMetadataWasDisabled := session.HDRMetadataDisabled
	session.mu.RUnlock()

	if hdrMetadataErrorDetected && hdrMetadataWasDisabled {
		log.Printf("[hls] session %s: FFmpeg was killed due to hevc_metadata errors after %v, restarting without HDR signaling filter", session.ID, completionTime)

		// Clean up the output directory for fresh start
		files, _ := filepath.Glob(filepath.Join(session.OutputDir, "*"))
		for _, f := range files {
			os.Remove(f)
		}

		// Reset session state for restart
		session.mu.Lock()
		session.FFmpegCmd = nil
		session.FFmpegPID = 0
		session.Completed = false
		session.SegmentsCreated = 0
		session.BytesStreamed = 0
		session.SegmentRequestCount = 0
		session.CreatedAt = time.Now() // Reset so startup timeout doesn't immediately fire
		session.LastSegmentRequest = time.Now()
		session.mu.Unlock()

		// Restart transcoding - HDRMetadataDisabled is already set to true
		log.Printf("[hls] session %s: restarting transcoding without hevc_metadata filter (stream will still play, but may lack proper HDR color signaling)", session.ID)
		return m.startTranscoding(ctx, session, forceAAC)
	}

	// Check if we killed FFmpeg due to input stream errors (usenet disconnect) - attempt recovery
	session.mu.RLock()
	inputWasErrored := session.InputErrorDetected
	recoveryAttempts := session.RecoveryAttempts
	cachedForceAAC := session.forceAAC
	session.mu.RUnlock()

	if inputErrorDetected && inputWasErrored && recoveryAttempts < hlsMaxRecoveryAttempts {
		// Find the highest segment number to calculate where to resume
		highestSegment := m.findHighestSegmentNumber(session)
		if highestSegment < 0 {
			highestSegment = 0
		}

		// Calculate new transcoding offset based on segments already created
		// Each segment is hlsSegmentDuration seconds
		// Use TranscodingOffset as base (not StartOffset) - StartOffset is the original user position
		newTranscodingOffset := session.TranscodingOffset + float64(highestSegment+1)*hlsSegmentDuration

		// Don't exceed the total duration
		if session.Duration > 0 && newTranscodingOffset >= session.Duration {
			log.Printf("[hls] session %s: input error recovery would exceed duration (offset %.2f >= duration %.2f), marking complete",
				session.ID, newTranscodingOffset, session.Duration)
			session.mu.Lock()
			session.Completed = true
			session.mu.Unlock()
			return nil
		}

		log.Printf("[hls] session %s: input error recovery - highest segment=%d, new transcoding offset=%.2fs (was %.2fs), attempt %d/%d",
			session.ID, highestSegment, newTranscodingOffset, session.TranscodingOffset, recoveryAttempts+1, hlsMaxRecoveryAttempts)

		// DON'T clean up existing segments - we want to keep them for seamless playback
		// Only remove the potentially incomplete last segment and playlist (will be regenerated)
		// Actually, let's keep everything and let FFmpeg overwrite the playlist
		// The existing segments are still valid and can be served

		// Reset session state for restart, but keep track of progress
		// NOTE: We update TranscodingOffset, NOT StartOffset - StartOffset is the original user position
		session.mu.Lock()
		session.FFmpegCmd = nil
		session.FFmpegPID = 0
		session.Completed = false
		session.InputErrorDetected = false // Reset so we can detect new errors
		session.RecoveryAttempts++
		session.TranscodingOffset = newTranscodingOffset // Update transcoding offset to resume position
		session.CreatedAt = time.Now()       // Reset so startup timeout doesn't immediately fire
		session.LastSegmentRequest = time.Now()
		// Keep SegmentsCreated, BytesStreamed, SegmentRequestCount as-is for tracking
		session.mu.Unlock()

		// Create a new background context for the restart (old context may be cancelled)
		newCtx, newCancel := context.WithCancel(context.Background())
		session.mu.Lock()
		session.Cancel = newCancel
		session.mu.Unlock()

		// Brief delay before reconnecting to allow usenet connection to stabilize
		log.Printf("[hls] session %s: waiting 2 seconds before recovery restart", session.ID)
		time.Sleep(2 * time.Second)

		// Restart transcoding from the new offset
		// Subtitles will be re-extracted from TranscodingOffset (same as seek behavior)
		log.Printf("[hls] session %s: restarting transcoding from %.2fs after input error (recovery attempt %d/%d)",
			session.ID, newTranscodingOffset, recoveryAttempts+1, hlsMaxRecoveryAttempts)
		return m.startTranscoding(newCtx, session, cachedForceAAC)
	}

	// Calculate expected vs actual segments for debugging
	// Use TranscodingOffset since that's where FFmpeg started from
	highestSegment := m.findHighestSegmentNumber(session)
	expectedDuration := session.Duration - session.TranscodingOffset
	expectedSegments := 0
	if expectedDuration > 0 {
		expectedSegments = int(expectedDuration / hlsSegmentDuration)
	}
	actualSegments := highestSegment + 1
	completionPercent := 0.0
	if expectedSegments > 0 {
		completionPercent = float64(actualSegments) / float64(expectedSegments) * 100
	}

	session.mu.Lock()
	session.Completed = true
	idleTriggered := session.IdleTimeoutTriggered
	session.mu.Unlock()

	if err != nil && ctx.Err() == nil && !idleTriggered {
		log.Printf("[hls] session %s: FFmpeg failed after %v: %v", session.ID, completionTime, err)
		return fmt.Errorf("ffmpeg wait: %w", err)
	}

	// Detailed completion logging
	log.Printf("[hls] session %s: TRANSCODING_COMPLETE - duration=%.2fs transcodingOffset=%.2fs expectedDuration=%.2fs",
		session.ID, session.Duration, session.TranscodingOffset, expectedDuration)
	log.Printf("[hls] session %s: TRANSCODING_COMPLETE - expectedSegments=%d actualSegments=%d (highest=%d) completion=%.1f%%",
		session.ID, expectedSegments, actualSegments, highestSegment, completionPercent)

	// Check if this completion was due to a user-initiated seek (skip recovery)
	session.mu.RLock()
	seekInProgress := session.SeekInProgress
	session.mu.RUnlock()

	if seekInProgress {
		log.Printf("[hls] session %s: transcoding cancelled for user seek, skipping recovery",
			session.ID)
		return nil
	}

	if idleTriggered {
		log.Printf("[hls] session %s: transcoding stopped due to IDLE_TIMEOUT after %v (bytes streamed: %d, segments: %d)",
			session.ID, completionTime, session.BytesStreamed, session.SegmentsCreated)
	} else if completionPercent < 95 && expectedSegments > 0 && (err != nil || inputErrorDetected) {
		// Only trigger premature completion recovery if there was actual evidence of failure:
		// - FFmpeg exited with non-zero code (err != nil), OR
		// - Input errors were detected (connection issues, etc.)
		// If FFmpeg exited cleanly (code 0) with no errors, the metadata duration was likely wrong
		// and we should trust that FFmpeg processed the complete file.
		log.Printf("[hls] session %s: PREMATURE_COMPLETION at %.1f%% (expected %d segments, got %d)",
			session.ID, completionPercent, expectedSegments, actualSegments)

		// Attempt recovery if we haven't exhausted retries
		if recoveryAttempts < hlsMaxRecoveryAttempts {
			// Calculate new transcoding offset based on segments already created
			// Use TranscodingOffset (not StartOffset) as base - StartOffset is the original user position
			newTranscodingOffset := session.TranscodingOffset + float64(highestSegment+1)*hlsSegmentDuration

			// Don't exceed the total duration
			if session.Duration > 0 && newTranscodingOffset >= session.Duration {
				log.Printf("[hls] session %s: premature completion recovery would exceed duration (offset %.2f >= duration %.2f), marking complete",
					session.ID, newTranscodingOffset, session.Duration)
				return nil
			}

			log.Printf("[hls] session %s: premature completion recovery - highest segment=%d, new transcoding offset=%.2fs (was %.2fs), attempt %d/%d",
				session.ID, highestSegment, newTranscodingOffset, session.TranscodingOffset, recoveryAttempts+1, hlsMaxRecoveryAttempts)

			// Reset session state for restart
			// NOTE: We update TranscodingOffset, NOT StartOffset - StartOffset is the original user position
			// and must remain unchanged so the frontend displays correct times
			session.mu.Lock()
			session.FFmpegCmd = nil
			session.FFmpegPID = 0
			session.Completed = false
			session.RecoveryAttempts++
			session.TranscodingOffset = newTranscodingOffset
			session.CreatedAt = time.Now()
			session.LastSegmentRequest = time.Now()
			session.mu.Unlock()

			// Create a new background context for the restart
			newCtx, newCancel := context.WithCancel(context.Background())
			session.mu.Lock()
			session.Cancel = newCancel
			session.mu.Unlock()

			// Brief delay before reconnecting
			log.Printf("[hls] session %s: waiting 2 seconds before recovery restart", session.ID)
			time.Sleep(2 * time.Second)

			// Restart transcoding from the new offset
			// Subtitles will be re-extracted from TranscodingOffset (same as seek behavior)
			log.Printf("[hls] session %s: restarting transcoding from %.2fs after premature completion (recovery attempt %d/%d)",
				session.ID, newTranscodingOffset, recoveryAttempts+1, hlsMaxRecoveryAttempts)
			return m.startTranscoding(newCtx, session, cachedForceAAC)
		}
		log.Printf("[hls] session %s: premature completion recovery exhausted (%d/%d attempts)",
			session.ID, recoveryAttempts, hlsMaxRecoveryAttempts)
	} else if completionPercent < 95 && expectedSegments > 0 {
		// Segment count mismatch but FFmpeg exited cleanly - likely incorrect metadata duration
		log.Printf("[hls] session %s: transcoding completed in %v with segment mismatch (%.1f%% - expected %d segments, got %d) - FFmpeg exited cleanly so metadata duration was likely incorrect",
			session.ID, completionTime, completionPercent, expectedSegments, actualSegments)
	} else {
		log.Printf("[hls] session %s: transcoding completed successfully in %v (bytes streamed: %d, segments: %d)",
			session.ID, completionTime, session.BytesStreamed, session.SegmentsCreated)
	}
	return nil
}

// findHighestSegmentNumber scans the output directory for segment files and returns the highest segment number found
// Returns -1 if no segments are found
func (m *HLSManager) findHighestSegmentNumber(session *HLSSession) int {
	highest := -1

	// Check for both .ts and .m4s segment files
	patterns := []string{
		filepath.Join(session.OutputDir, "segment*.ts"),
		filepath.Join(session.OutputDir, "segment*.m4s"),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}

		for _, match := range matches {
			base := filepath.Base(match)
			// Extract number from "segment<N>.ts" or "segment<N>.m4s"
			var num int
			if strings.HasSuffix(base, ".ts") {
				_, err = fmt.Sscanf(base, "segment%d.ts", &num)
			} else if strings.HasSuffix(base, ".m4s") {
				_, err = fmt.Sscanf(base, "segment%d.m4s", &num)
			}
			if err == nil && num > highest {
				highest = num
			}
		}
	}

	return highest
}

// logProcessCPU attempts to read CPU usage from /proc/{pid}/stat
func (m *HLSManager) logProcessCPU(sessionID string, pid int) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		// /proc not available (not on Linux) or process ended
		return
	}

	// Parse stat file - fields are space-separated
	// We want utime (14th field) and stime (15th field) in clock ticks
	fields := strings.Fields(string(data))
	if len(fields) < 15 {
		return
	}

	var utime, stime int64
	fmt.Sscanf(fields[13], "%d", &utime) // user time
	fmt.Sscanf(fields[14], "%d", &stime) // system time

	totalTicks := utime + stime
	// CPU usage in seconds (assuming 100 ticks per second)
	cpuSeconds := float64(totalTicks) / 100.0

	log.Printf("[hls] session %s: FFmpeg CPU usage - pid=%d utime=%d stime=%d total_cpu_sec=%.2f",
		sessionID, pid, utime, stime, cpuSeconds)
}

// GetSession retrieves a session by ID and updates last access time
func (m *HLSManager) GetSession(sessionID string) (*HLSSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[sessionID]
	if exists {
		session.mu.Lock()
		session.LastAccess = time.Now()
		session.mu.Unlock()
	}

	return session, exists
}

// KeepAlive updates the last activity time for a session to prevent idle timeout
// This is used by the frontend to keep paused streams alive
// Optional query param: time=<seconds> to report current playback position for rate limiting
func (m *HLSManager) KeepAlive(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.Lock()
	session.LastSegmentRequest = time.Now()

	// If frontend reports playback time, use it to update playback tracking for rate limiting and cleanup
	if timeStr := r.URL.Query().Get("time"); timeStr != "" {
		if playbackTime, err := strconv.ParseFloat(timeStr, 64); err == nil && playbackTime >= 0 {
			// For warm starts, the frontend reports absolute media time but HLS segments start from 0
			// Adjust for StartOffset to get the actual HLS segment number
			hlsTime := playbackTime - session.StartOffset
			if hlsTime < 0 {
				hlsTime = 0
			}
			// Calculate segment number from HLS stream time (hlsSegmentDuration = 4 seconds)
			segmentNum := int(hlsTime / hlsSegmentDuration)
			if segmentNum > session.MaxSegmentRequested {
				session.MaxSegmentRequested = segmentNum
				log.Printf("[hls] session %s: keepalive updated MaxSegmentRequested to %d (mediaTime=%.1fs, hlsTime=%.1fs, startOffset=%.1fs)",
					sessionID, segmentNum, playbackTime, hlsTime, session.StartOffset)
			}
			// Track actual playback position for segment cleanup (only delete what player has watched)
			if segmentNum > session.LastPlaybackSegment {
				session.LastPlaybackSegment = segmentNum
			}
		}
	}

	// If frontend reports buffer start time, use it for safe segment cleanup
	// This is the earliest time still in the player's buffer - we must not delete segments at or after this point
	if bufferStartStr := r.URL.Query().Get("bufferStart"); bufferStartStr != "" {
		if bufferStartTime, err := strconv.ParseFloat(bufferStartStr, 64); err == nil && bufferStartTime >= 0 {
			// Adjust for StartOffset to get the actual HLS segment number
			hlsBufferStart := bufferStartTime - session.StartOffset
			if hlsBufferStart < 0 {
				hlsBufferStart = 0
			}
			bufferStartSegment := int(hlsBufferStart / hlsSegmentDuration)
			session.EarliestBufferedSegment = bufferStartSegment
		}
	}

	// Capture timing info while we have the lock
	startOffset := session.StartOffset
	actualStartOffset := session.ActualStartOffset
	keyframeDelta := actualStartOffset - startOffset
	duration := session.Duration
	session.mu.Unlock()

	log.Printf("[hls] session %s: keepalive received, extended idle timeout", sessionID)

	// Return segment timing info for accurate subtitle sync
	// The frontend can use this to calculate precise media time:
	// mediaTime = startOffset + (segmentIndex * segmentDuration) + positionInSegment
	// keyframeDelta is the offset between actual keyframe and requested position for subtitle sync
	response := struct {
		Status            string  `json:"status"`
		StartOffset       float64 `json:"startOffset"`
		ActualStartOffset float64 `json:"actualStartOffset"`
		KeyframeDelta     float64 `json:"keyframeDelta"`
		SegmentDuration   float64 `json:"segmentDuration"`
		Duration          float64 `json:"duration,omitempty"`
	}{
		Status:            "ok",
		StartOffset:       startOffset,
		ActualStartOffset: actualStartOffset,
		KeyframeDelta:     keyframeDelta,
		SegmentDuration:   hlsSegmentDuration,
		Duration:          duration,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// SeekResponse contains the response data for a seek request
type SeekResponse struct {
	SessionID         string  `json:"sessionId"`
	StartOffset       float64 `json:"startOffset"`
	ActualStartOffset float64 `json:"actualStartOffset"`
	KeyframeDelta     float64 `json:"keyframeDelta"` // Delta between actual keyframe and requested position (negative = earlier)
	Duration          float64 `json:"duration,omitempty"`
	PlaylistURL       string  `json:"playlistUrl"`
}

// Seek seeks within an existing HLS session by restarting transcoding from a new offset
// This is faster than creating a new session since it reuses the existing session structure
// Query param: time=<seconds> specifies the target seek position in absolute media time
func (m *HLSManager) Seek(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Parse target time from query parameter
	timeStr := r.URL.Query().Get("time")
	if timeStr == "" {
		http.Error(w, "missing time parameter", http.StatusBadRequest)
		return
	}

	targetTime, err := strconv.ParseFloat(timeStr, 64)
	if err != nil || targetTime < 0 {
		http.Error(w, "invalid time parameter", http.StatusBadRequest)
		return
	}

	session.mu.RLock()
	duration := session.Duration
	session.mu.RUnlock()

	// Clamp target time to valid range
	if duration > 0 && targetTime >= duration {
		targetTime = duration - 1
	}
	if targetTime < 0 {
		targetTime = 0
	}

	log.Printf("[hls] session %s: seek requested to %.2fs (current offset: %.2fs)", sessionID, targetTime, session.StartOffset)

	// Mark seek in progress to prevent recovery logic from triggering
	session.mu.Lock()
	session.SeekInProgress = true
	if session.Cancel != nil {
		log.Printf("[hls] session %s: cancelling current transcoding for seek", sessionID)
		session.Cancel()
	}
	session.mu.Unlock()

	// Wait briefly for FFmpeg to stop
	time.Sleep(100 * time.Millisecond)

	// Clear all existing segments since they're at the old time offset
	if err := m.clearSessionSegments(session); err != nil {
		log.Printf("[hls] session %s: warning: failed to clear segments for seek: %v", sessionID, err)
	}

	// Probe for the actual keyframe position FFmpeg will seek to BEFORE starting transcoding
	// This is critical for VTT subtitle sync - both video and subtitles must start from the same position
	session.mu.RLock()
	sessionPath := session.Path
	session.mu.RUnlock()

	keyframePos := targetTime // Default to requested time
	if targetTime > 0 {
		keyframePos = m.probeKeyframePosition(r.Context(), sessionPath, targetTime)
		log.Printf("[hls] session %s: seek keyframe probe: requested=%.3fs keyframe=%.3fs delta=%.3fs",
			sessionID, targetTime, keyframePos, keyframePos-targetTime)
	}

	// Reset session state for the new seek position
	session.mu.Lock()
	session.FFmpegCmd = nil
	session.FFmpegPID = 0
	session.Completed = false
	session.StartOffset = targetTime      // User's new position (for frontend display)
	session.TranscodingOffset = keyframePos // FFmpeg starts from keyframe position
	session.ActualStartOffset = keyframePos // For subtitle sync
	session.CreatedAt = time.Now()
	session.LastSegmentRequest = time.Now()
	session.SegmentsCreated = 0
	session.MinSegmentRequested = -1
	session.MaxSegmentRequested = -1
	session.LastPlaybackSegment = 0
	session.EarliestBufferedSegment = 0
	session.RecoveryAttempts = 0 // Reset recovery attempts for new seek position
	session.SeekInProgress = false // Clear seek flag now that we're starting fresh
	cachedForceAAC := session.forceAAC
	session.mu.Unlock()

	// Create a new context for the restarted transcoding
	newCtx, newCancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.Cancel = newCancel
	session.mu.Unlock()

	// Start transcoding from the new offset in background
	go func() {
		if err := m.startTranscoding(newCtx, session, cachedForceAAC); err != nil {
			log.Printf("[hls] session %s: seek transcoding failed: %v", sessionID, err)
			session.mu.Lock()
			session.Completed = true
			session.mu.Unlock()
		}
	}()

	// Wait for the playlist file to be created before returning
	// This prevents the player from trying to load a non-existent playlist
	session.mu.RLock()
	outputDir := session.OutputDir
	session.mu.RUnlock()
	playlistPath := filepath.Join(outputDir, "stream.m3u8")

	maxWait := 10 * time.Second
	pollInterval := 100 * time.Millisecond
	waitStart := time.Now()

	for {
		if _, err := os.Stat(playlistPath); err == nil {
			// Playlist exists, check if it has content
			if data, err := os.ReadFile(playlistPath); err == nil && len(data) > 50 {
				log.Printf("[hls] session %s: playlist ready after %v (%d bytes)", sessionID, time.Since(waitStart), len(data))
				break
			}
		}

		if time.Since(waitStart) > maxWait {
			log.Printf("[hls] session %s: warning: timed out waiting for playlist after %v", sessionID, maxWait)
			break
		}

		time.Sleep(pollInterval)
	}

	// Build playlist URL (without /api/ prefix - frontend adds it)
	playlistURL := fmt.Sprintf("/video/hls/%s/stream.m3u8", sessionID)

	log.Printf("[hls] session %s: seek completed, requested=%.2fs actual=%.2fs (delta=%.3fs)",
		sessionID, targetTime, keyframePos, keyframePos-targetTime)

	response := SeekResponse{
		SessionID:         sessionID,
		StartOffset:       targetTime,
		ActualStartOffset: keyframePos,
		KeyframeDelta:     keyframePos - targetTime,
		Duration:          duration,
		PlaylistURL:       playlistURL,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// clearSessionSegments removes all segment files from a session's output directory
func (m *HLSManager) clearSessionSegments(session *HLSSession) error {
	session.mu.RLock()
	outputDir := session.OutputDir
	session.mu.RUnlock()

	// Remove all segment files (.ts, .m4s) and VTT subtitle files
	// VTT files MUST be cleared on seek to prevent stale subtitle timing
	// The new FFmpeg process will regenerate VTT from the new seek position
	patterns := []string{
		filepath.Join(outputDir, "segment*.ts"),
		filepath.Join(outputDir, "segment*.m4s"),
		filepath.Join(outputDir, "init.mp4"),
		filepath.Join(outputDir, "stream.m3u8"),
		filepath.Join(outputDir, "subtitles_*.vtt"),
	}

	var removeCount int
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if err := os.Remove(match); err == nil {
				removeCount++
			}
		}
	}

	log.Printf("[hls] session %s: cleared %d segment files for seek", session.ID, removeCount)
	return nil
}

// HLSSessionStatus represents the status of an HLS session for frontend polling
type HLSSessionStatus struct {
	SessionID           string  `json:"sessionId"`
	Status              string  `json:"status"` // "active", "completed", "error"
	FatalError          string  `json:"fatalError,omitempty"`
	FatalErrorTime      int64   `json:"fatalErrorTime,omitempty"` // Unix timestamp
	Duration            float64 `json:"duration,omitempty"`
	SegmentsCreated     int     `json:"segmentsCreated"`
	MaxSegmentRequested int     `json:"maxSegmentRequested"` // Highest segment requested by player
	Paused              bool    `json:"paused"`              // True if FFmpeg is paused (rate limited)
	BitstreamErrors     int     `json:"bitstreamErrors"`
	HDRMetadataDisabled bool    `json:"hdrMetadataDisabled"`
	DVDisabled          bool    `json:"dvDisabled"`
	RecoveryAttempts    int     `json:"recoveryAttempts"`
}

// GetSessionStatus returns the current status of an HLS session
func (m *HLSManager) GetSessionStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.RLock()
	status := HLSSessionStatus{
		SessionID:           session.ID,
		Duration:            session.Duration,
		SegmentsCreated:     session.SegmentsCreated,
		MaxSegmentRequested: session.MaxSegmentRequested,
		Paused:              session.Paused,
		BitstreamErrors:     session.BitstreamErrors,
		HDRMetadataDisabled: session.HDRMetadataDisabled,
		DVDisabled:          session.DVDisabled,
		RecoveryAttempts:    session.RecoveryAttempts,
	}

	if session.FatalError != "" {
		status.Status = "error"
		status.FatalError = session.FatalError
		status.FatalErrorTime = session.FatalErrorTime.Unix()
	} else if session.Completed {
		status.Status = "completed"
	} else {
		status.Status = "active"
	}
	session.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("[hls] session %s: failed to encode status response: %v", sessionID, err)
	}
}

// ServePlaylist serves the HLS playlist file with API key in segment URLs
func (m *HLSManager) ServePlaylist(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Update last activity time (playlist requests indicate active playback)
	session.mu.Lock()
	session.LastSegmentRequest = time.Now()
	session.mu.Unlock()

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")

	// Wait for playlist to be created (up to 60 seconds)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, statErr := os.Stat(playlistPath); statErr == nil {
			break
		} else if os.IsNotExist(statErr) {
			if time.Now().After(deadline) {
				log.Printf("[hls] playlist still not ready for session %s after 60s", sessionID)
				http.Error(w, "playlist not ready", http.StatusGatewayTimeout)
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		} else {
			log.Printf("[hls] failed to stat playlist for session %s: %v", sessionID, statErr)
			http.Error(w, "playlist not ready", http.StatusInternalServerError)
			return
		}
	}

	// Read the playlist file
	content, err := os.ReadFile(playlistPath)
	if err != nil {
		log.Printf("[hls] failed to read playlist for session %s: %v", sessionID, err)
		http.Error(w, "playlist not ready", http.StatusInternalServerError)
		return
	}
	log.Printf("[hls] playlist file read successfully for session %s, size=%d bytes", sessionID, len(content))

	// NOTE: We no longer filter the playlist when segments are deleted.
	// Modifying EXT-X-MEDIA-SEQUENCE causes players to re-sync and stutter.
	// Since we only delete segments the player has already watched (based on keepalive reports),
	// the player won't request them anyway. If it does (e.g., seek back), it gets a 404 which is fine.

	// Get auth token from request
	authToken := r.URL.Query().Get("token")
	if authToken == "" {
		// Try Authorization header
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			authToken = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	// Rewrite segment URLs to include auth token and inject HLS tags
	playlistContent := string(content)

	// Build header tags to inject after #EXTM3U
	var headerTags []string

	// Inject EXT-X-VIDEO-RANGE for HDR/DV content - tells iOS AVPlayer to enable HDR mode
	// Without this, iOS treats HDR content as SDR causing color banding and incorrect display
	if (session.HasDV || session.HasHDR) && !strings.Contains(playlistContent, "#EXT-X-VIDEO-RANGE") {
		headerTags = append(headerTags, "#EXT-X-VIDEO-RANGE:PQ")
	}

	// Inject EXT-X-START:TIME-OFFSET=0 to tell iOS to start from the beginning
	// This is critical for EVENT playlists during live transcoding, as iOS otherwise
	// treats them like live streams and starts near the "live edge" (latest segments)
	// Only do this for cold starts (StartOffset=0) - warm starts have their own seek logic
	if session.StartOffset == 0 && !strings.Contains(playlistContent, "#EXT-X-START") {
		headerTags = append(headerTags, "#EXT-X-START:TIME-OFFSET=0,PRECISE=YES")
	}

	// Insert all header tags after #EXTM3U
	if len(headerTags) > 0 {
		injection := "#EXTM3U\n" + strings.Join(headerTags, "\n") + "\n"
		playlistContent = strings.Replace(playlistContent, "#EXTM3U\n", injection, 1)
	}

	// Convert EVENT playlist to VOD when we know the duration
	// This makes iOS show a progress bar in PiP mode instead of "LIVE"
	// We pre-generate entries for all expected segments so the player knows the full duration
	// Segment requests for not-yet-transcoded segments will block until ready
	if session.Duration > 0 && !strings.Contains(playlistContent, "#EXT-X-ENDLIST") {
		playlistContent = strings.Replace(playlistContent, "#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-PLAYLIST-TYPE:VOD", 1)

		// Calculate total expected segments and find highest existing segment
		effectiveDuration := session.Duration - session.TranscodingOffset
		totalSegments := int(math.Ceil(effectiveDuration / hlsSegmentDuration))

		// Find the highest segment number in the current playlist
		highestExisting := -1
		lines := strings.Split(playlistContent, "\n")
		// TESTING: Always use .m4s for all content (normally SDR uses .ts)
		segmentExt := ".m4s"
		// if !session.HasDV && !session.HasHDR {
		// 	segmentExt = ".ts"
		// }
		for _, line := range lines {
			if strings.HasPrefix(line, "segment") && strings.HasSuffix(line, segmentExt) {
				// Extract segment number from "segment0.m4s" or "segment0.ts"
				numStr := strings.TrimPrefix(line, "segment")
				numStr = strings.TrimSuffix(numStr, segmentExt)
				if num, err := strconv.Atoi(numStr); err == nil && num > highestExisting {
					highestExisting = num
				}
			}
		}

		// Add entries for remaining segments that haven't been transcoded yet
		var extraSegments strings.Builder
		for i := highestExisting + 1; i < totalSegments; i++ {
			// Calculate duration for this segment (last segment may be shorter)
			segDuration := hlsSegmentDuration
			segEndTime := float64(i+1) * hlsSegmentDuration
			if segEndTime > effectiveDuration {
				segDuration = effectiveDuration - float64(i)*hlsSegmentDuration
				if segDuration < 0.1 {
					continue // Skip very short final segments
				}
			}
			extraSegments.WriteString(fmt.Sprintf("#EXTINF:%.6f,\nsegment%d%s\n", segDuration, i, segmentExt))
		}

		if extraSegments.Len() > 0 {
			// Insert extra segments before any existing ENDLIST or at the end
			playlistContent = strings.TrimRight(playlistContent, "\n") + "\n" + extraSegments.String()
		}

		// Add ENDLIST to mark playlist as complete
		playlistContent = strings.TrimRight(playlistContent, "\n") + "\n#EXT-X-ENDLIST\n"
	}

	if authToken != "" {
		lines := strings.Split(playlistContent, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			// If line is a segment file (ends with .ts, .m4s, .vtt, or .webvtt)
			if strings.HasSuffix(trimmed, ".ts") || strings.HasSuffix(trimmed, ".m4s") ||
				strings.HasSuffix(trimmed, ".vtt") || strings.HasSuffix(trimmed, ".webvtt") {
				// Append auth token as query parameter
				lines[i] = line + "?token=" + authToken
			} else if strings.Contains(line, "#EXT-X-MAP:URI=") {
				// Rewrite init segment URL in EXT-X-MAP tag
				// Format: #EXT-X-MAP:URI="init.mp4"
				lines[i] = strings.Replace(line, `"init.mp4"`, `"init.mp4?token=`+authToken+`"`, 1)
			} else if strings.Contains(line, "URI=") && (strings.Contains(line, ".vtt") || strings.Contains(line, ".webvtt")) {
				// Rewrite subtitle URLs in #EXT-X-MEDIA tags
				// Format: #EXT-X-MEDIA:TYPE=SUBTITLES,...,URI="subtitle.webvtt"
				lines[i] = strings.ReplaceAll(line, ".vtt\"", ".vtt?token="+authToken+"\"")
				lines[i] = strings.ReplaceAll(lines[i], ".webvtt\"", ".webvtt?token="+authToken+"\"")
			}
		}
		playlistContent = strings.Join(lines, "\n")
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	w.Write([]byte(playlistContent))

	videoRange := "SDR"
	if session.HasDV || session.HasHDR {
		videoRange = "PQ"
	}
	log.Printf("[hls] served playlist for session %s, VIDEO-RANGE=%s, auth token=%v", sessionID, videoRange, authToken != "")
}

// ServeSegment serves an HLS segment file
func (m *HLSManager) ServeSegment(w http.ResponseWriter, r *http.Request, sessionID, segmentName string) {
	requestStart := time.Now()
	log.Printf("[hls] segment request: session=%s segment=%s", sessionID, segmentName)

	session, exists := m.GetSession(sessionID)
	if !exists {
		log.Printf("[hls] session not found: %s", sessionID)
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Parse segment number from filename (e.g., "segment123.ts" -> 123)
	var segmentNum int
	if _, err := fmt.Sscanf(segmentName, "segment%d.", &segmentNum); err == nil {
		// Update tracking for this segment request
		session.mu.Lock()
		if session.MinSegmentRequested < 0 || segmentNum < session.MinSegmentRequested {
			session.MinSegmentRequested = segmentNum
			log.Printf("[hls] session %s: updated MinSegmentRequested to %d", sessionID, segmentNum)
		}
		if segmentNum > session.MaxSegmentRequested {
			session.MaxSegmentRequested = segmentNum
			log.Printf("[hls] session %s: updated MaxSegmentRequested to %d", sessionID, segmentNum)
		}
		session.mu.Unlock()
	}

	// Update last segment request time to prevent idle timeout
	session.mu.Lock()
	session.LastSegmentRequest = time.Now()
	session.SegmentRequestCount++
	requestCount := session.SegmentRequestCount
	session.mu.Unlock()

	log.Printf("[hls] segment request #%d: session=%s segment=%s", requestCount, sessionID, segmentName)

	// Validate segment name to prevent path traversal
	if strings.Contains(segmentName, "..") || strings.Contains(segmentName, "/") {
		log.Printf("[hls] invalid segment name: %s", segmentName)
		http.Error(w, "invalid segment name", http.StatusBadRequest)
		return
	}

	segmentPath := filepath.Join(session.OutputDir, segmentName)

	// Wait for segment to be created (up to 30 seconds for slow transcoding)
	waitStart := time.Now()
	segmentReady := false
	var segmentSize int64
	for i := 0; i < 300; i++ {
		if stat, err := os.Stat(segmentPath); err == nil {
			segmentSize = stat.Size()
			segmentReady = true

			// Track first segment time
			session.mu.Lock()
			if session.FirstSegmentTime.IsZero() {
				session.FirstSegmentTime = time.Now()
				log.Printf("[hls] session %s: FIRST_SEGMENT ready after %v from stream start",
					sessionID, session.FirstSegmentTime.Sub(session.StreamStartTime))
			}
			session.SegmentsCreated++
			session.mu.Unlock()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	waitDuration := time.Since(waitStart)
	if !segmentReady {
		log.Printf("[hls] SEGMENT_TIMEOUT: session=%s segment=%s waited=%v",
			sessionID, segmentName, waitDuration)
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	log.Printf("[hls] segment ready: session=%s segment=%s size=%d bytes wait=%v",
		sessionID, segmentName, segmentSize, waitDuration)

	// Set appropriate content type based on file extension
	contentType := "video/mp2t" // Default for .ts files
	if strings.HasSuffix(segmentName, ".m4s") || strings.HasSuffix(segmentName, ".mp4") {
		contentType = "video/mp4"
	} else if strings.HasSuffix(segmentName, ".vtt") || strings.HasSuffix(segmentName, ".webvtt") {
		contentType = "text/vtt"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Accept-Ranges", "bytes")

	// Set Content-Length explicitly for fMP4 segments (required by iOS/tvOS)
	w.Header().Set("Content-Length", strconv.FormatInt(segmentSize, 10))

	// Track bytes served
	session.mu.Lock()
	session.BytesStreamed += segmentSize
	session.mu.Unlock()

	serveStart := time.Now()
	http.ServeFile(w, r, segmentPath)
	serveDuration := time.Since(serveStart)

	// Update LastSegmentServed after successful serve (parse segment number again)
	var servedSegmentNum int
	if _, err := fmt.Sscanf(segmentName, "segment%d.", &servedSegmentNum); err == nil {
		session.mu.Lock()
		if servedSegmentNum > session.LastSegmentServed {
			session.LastSegmentServed = servedSegmentNum
		}
		session.mu.Unlock()
	}

	totalDuration := time.Since(requestStart)
	log.Printf("[hls] segment served: session=%s segment=%s size=%d bytes serve_time=%v total_time=%v",
		sessionID, segmentName, segmentSize, serveDuration, totalDuration)

	// Clean up old segments to save disk space
	// The playlist is filtered at serve time to exclude deleted segments
	go m.deleteOldSegments(session, segmentName)
}

// ServeSubtitles serves the sidecar VTT file for fMP4/HDR sessions
// The VTT file grows progressively as FFmpeg processes the stream, so we serve whatever is available
// Supports ?track=N query parameter to serve a different subtitle track than the one selected when creating the session
func (m *HLSManager) ServeSubtitles(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Check if a specific track is requested via query parameter
	requestedTrackStr := r.URL.Query().Get("track")
	requestedTrack := session.SubtitleTrackIndex // Default to session's original track

	if requestedTrackStr != "" {
		if parsed, err := strconv.Atoi(requestedTrackStr); err == nil && parsed >= 0 {
			requestedTrack = parsed
		}
	}

	// All subtitle tracks are now extracted upfront to subtitles_<streamIndex>.vtt
	// This naming is consistent for both initially selected and switched tracks
	vttPath := filepath.Join(session.OutputDir, fmt.Sprintf("subtitles_%d.vtt", requestedTrack))

	// Check if file exists - for fMP4/DV sessions, all tracks should be pre-extracted
	// If not found, fall back to on-demand extraction (for MPEG-TS or edge cases)
	if _, err := os.Stat(vttPath); os.IsNotExist(err) {
		// Use per-track extraction tracking to prevent duplicates without blocking the session
		// This avoids a deadlock where subtitle extraction holds session.mu while the
		// transcoding pipeline waits for session.mu.RLock at startup
		session.subtitleExtractionMu.Lock()
		if session.subtitleExtracting == nil {
			session.subtitleExtracting = make(map[int]bool)
		}
		alreadyExtracting := session.subtitleExtracting[requestedTrack]
		if !alreadyExtracting {
			// Double-check file doesn't exist after acquiring lock
			if _, err := os.Stat(vttPath); os.IsNotExist(err) {
				session.subtitleExtracting[requestedTrack] = true
			}
		}
		session.subtitleExtractionMu.Unlock()

		if alreadyExtracting {
			// Another request is already extracting this track, wait and retry
			log.Printf("[hls] subtitle track %d extraction already in progress for session %s, waiting", requestedTrack, sessionID)
			time.Sleep(500 * time.Millisecond)
			// Check if file now exists
			if _, err := os.Stat(vttPath); os.IsNotExist(err) {
				// Still not ready, return empty VTT
				w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Write([]byte("WEBVTT\n\n"))
				return
			}
		} else if _, err := os.Stat(vttPath); os.IsNotExist(err) {
			// We're responsible for extracting this track
			log.Printf("[hls] subtitle track %d not pre-extracted, attempting on-demand extraction for session %s", requestedTrack, sessionID)
			extractErr := m.extractSubtitleTrackToVTT(session, requestedTrack, vttPath)

			// Clear the extraction flag
			session.subtitleExtractionMu.Lock()
			delete(session.subtitleExtracting, requestedTrack)
			session.subtitleExtractionMu.Unlock()

			if extractErr != nil {
				log.Printf("[hls] failed to extract subtitle track %d: %v", requestedTrack, extractErr)
				// Return empty VTT instead of error to avoid breaking playback
				w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Write([]byte("WEBVTT\n\n"))
				return
			}

			// Wait a moment for filesystem to flush (race condition fix)
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Check if file exists (might not be ready yet or no subtitles selected)
	stat, err := os.Stat(vttPath)
	if os.IsNotExist(err) {
		// Return empty VTT header if file doesn't exist yet
		// This allows the frontend to poll without errors
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte("WEBVTT\n\n"))
		return
	} else if err != nil {
		http.Error(w, "failed to check subtitle file", http.StatusInternalServerError)
		return
	}

	// Read the current contents of the VTT file
	// Note: FFmpeg writes progressively, so the file may still be growing
	content, err := os.ReadFile(vttPath)
	if err != nil {
		http.Error(w, "failed to read subtitle file", http.StatusInternalServerError)
		return
	}

	// If we read 0 bytes immediately after extraction, retry once
	// (filesystem buffering race condition)
	if len(content) == 0 && stat.Size() > 0 {
		time.Sleep(100 * time.Millisecond)
		content, _ = os.ReadFile(vttPath)
	}

	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache") // Don't cache since file is growing
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))

	w.Write(content)
	log.Printf("[hls] served subtitles for session %s track %d, size=%d bytes", sessionID, requestedTrack, len(content))
}

// extractSubtitleTrackToVTT extracts a specific subtitle track to a VTT file on-demand
// This allows switching subtitle tracks without recreating the HLS session
// trackIndex is the absolute ffprobe stream index (same as session.SubtitleTrackIndex)
func (m *HLSManager) extractSubtitleTrackToVTT(session *HLSSession, trackIndex int, outputPath string) error {
	ctx := context.Background()

	// Probe subtitle streams to map absolute stream index to relative subtitle index
	subtitleStreams, err := m.probeSubtitleStreams(ctx, session.Path)
	if err != nil {
		return fmt.Errorf("failed to probe subtitle streams: %w", err)
	}

	// Find which subtitle stream matches the requested absolute stream index
	// This is the same logic used in HLS session creation (lines 1350-1356)
	relativeIndex := -1
	var actualStreamIndex int
	var codec string

	for pos, stream := range subtitleStreams {
		if stream.Index == trackIndex {
			relativeIndex = pos
			actualStreamIndex = stream.Index
			codec = stream.Codec
			break
		}
	}

	if relativeIndex < 0 {
		return fmt.Errorf("subtitle stream index %d not found (have %d subtitle streams)", trackIndex, len(subtitleStreams))
	}

	log.Printf("[hls] extracting subtitle track (absoluteIndex=%d relativeIndex=%d streamIndex=%d codec=%s) to %s",
		trackIndex, relativeIndex, actualStreamIndex, codec, outputPath)

	// Text-based subtitle codecs that can be converted to WebVTT
	// Using a whitelist approach to avoid unknown bitmap codecs slipping through
	textSubtitleCodecs := map[string]bool{
		"subrip": true, "srt": true, "ass": true, "ssa": true,
		"webvtt": true, "vtt": true, "mov_text": true, "text": true,
		"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
		"mpl2": true, "pjs": true, "realtext": true, "stl": true,
		"subviewer": true, "subviewer1": true, "vplayer": true,
	}

	// Check for unsupported subtitle codecs (bitmap-based or unknown)
	if !textSubtitleCodecs[codec] {
		return fmt.Errorf("unsupported subtitle codec: %q (bitmap-based or unknown subtitles cannot be converted to VTT)", codec)
	}

	// Get the stream URL (convert virtual path to direct URL if needed)
	// Use getDirectURL which has WebDAV fallback for usenet streams
	streamURL, hasURL := m.getDirectURL(ctx, session)
	if !hasURL {
		// No direct URL available (not debrid, no WebDAV) - cannot extract subtitles
		return fmt.Errorf("no direct URL available for subtitle extraction (usenet streams require WebDAV)")
	}
	log.Printf("[hls] using URL for subtitle extraction: %s", streamURL)

	// Build ffmpeg command to extract subtitle track to VTT
	// If the session has a StartOffset (warm start/seek), we need to:
	// 1. Seek to the start offset so subtitles align with the HLS stream
	// 2. Use -start_at_zero to normalize timestamps to start at 0
	// This matches the behavior of the main transcoding pipeline
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
	}

	// Add input seeking if session has a start offset
	// Use ActualStartOffset (keyframe-aligned) for proper sync with video stream
	// Video seeks to nearest keyframe, so subtitles must start from the same position
	seekOffset := session.ActualStartOffset
	if seekOffset <= 0 {
		seekOffset = session.StartOffset // Fallback to requested offset if actual not set
	}
	if seekOffset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", seekOffset))
		log.Printf("[hls] session %s: subtitle extraction using -ss %.3fs (keyframe-aligned) for sync (requested was %.3fs)",
			session.ID, seekOffset, session.StartOffset)
	}

	args = append(args, "-i", streamURL)

	// Normalize output timestamps to start at 0 (matches main transcoding pipeline)
	if seekOffset > 0 {
		args = append(args, "-start_at_zero")
	}

	args = append(args,
		"-map", fmt.Sprintf("0:%d", actualStreamIndex),
		"-c", "webvtt",
		"-f", "webvtt",
		outputPath,
	)

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)

	// Run extraction synchronously (should be fast for most subtitle tracks)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg extraction failed: %w (output: %s)", err, string(output))
	}

	log.Printf("[hls] successfully extracted subtitle track %d to %s", trackIndex, outputPath)
	return nil
}

func isMatroskaPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mkv", ".mk3d", ".webm", ".mka":
		return true
	default:
		return false
	}
}

func isTSLikePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts", ".m2ts", ".mts", ".mpg", ".mpeg", ".vob":
		return true
	default:
		return false
	}
}

func supportsPipeRange(path string) bool {
	return isMatroskaPath(path) || isTSLikePath(path)
}

func normalizeWebDAVPrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func (m *HLSManager) fetchHeaderPrefix(ctx context.Context, path string, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}

	resp, err := m.streamer.Stream(ctx, streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: fmt.Sprintf("bytes=0-%d", length-1),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, length))
	if err != nil {
		return nil, err
	}

	return data, nil
}

func alignMatroskaCluster(r io.Reader, maxScanBytes int64) (io.Reader, int64, error) {
	if maxScanBytes <= 0 {
		return r, 0, nil
	}

	pattern := []byte{0x1F, 0x43, 0xB6, 0x75} // Cluster element ID
	buffer := make([]byte, 0, maxScanBytes)
	tmp := make([]byte, 64*1024)
	var totalRead int64

	for totalRead < maxScanBytes {
		n, err := r.Read(tmp)
		if n > 0 {
			buffer = append(buffer, tmp[:n]...)
			totalRead += int64(n)
			if idx := bytes.Index(buffer, pattern); idx >= 0 {
				remaining := append([]byte(nil), buffer[idx:]...)
				return io.MultiReader(bytes.NewReader(remaining), r), int64(idx), nil
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			remaining := append([]byte(nil), buffer...)
			return io.MultiReader(bytes.NewReader(remaining), r), 0, err
		}
	}

	remaining := append([]byte(nil), buffer...)
	return io.MultiReader(bytes.NewReader(remaining), r), 0,
		fmt.Errorf("matroska cluster sync not found within %d bytes", maxScanBytes)
}

// CleanupSession removes a session and its files
func (m *HLSManager) CleanupSession(sessionID string) {
	m.mu.Lock()
	session, exists := m.sessions[sessionID]
	if !exists {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	// Log session summary
	session.mu.RLock()
	elapsed := time.Since(session.CreatedAt)
	streamDuration := time.Since(session.StreamStartTime)
	bytesStreamed := session.BytesStreamed
	segmentsCreated := session.SegmentsCreated
	segmentRequestCount := session.SegmentRequestCount
	idleTriggered := session.IdleTimeoutTriggered
	hasFirstSegment := !session.FirstSegmentTime.IsZero()
	var firstSegmentDelay time.Duration
	if hasFirstSegment {
		firstSegmentDelay = session.FirstSegmentTime.Sub(session.StreamStartTime)
	}
	session.mu.RUnlock()

	log.Printf("[hls] SESSION_SUMMARY: id=%s elapsed=%v stream_duration=%v bytes=%d segments_created=%d segments_requested=%d first_segment_delay=%v idle_timeout=%v",
		sessionID, elapsed, streamDuration, bytesStreamed, segmentsCreated, segmentRequestCount, firstSegmentDelay, idleTriggered)

	// Kill FFmpeg process first (more forceful than context cancellation)
	session.mu.Lock()
	ffmpegCmd := session.FFmpegCmd
	session.mu.Unlock()

	if ffmpegCmd != nil && ffmpegCmd.Process != nil {
		log.Printf("[hls] killing FFmpeg process for session %s (PID=%d)", sessionID, ffmpegCmd.Process.Pid)
		// Use Kill() for immediate termination
		if err := ffmpegCmd.Process.Kill(); err != nil {
			log.Printf("[hls] failed to kill FFmpeg process: %v", err)
		}
		// Wait briefly for process to exit to prevent zombie processes
		go func() {
			ffmpegCmd.Wait()
		}()
	}

	// Cancel context after killing process
	if session.Cancel != nil {
		log.Printf("[hls] cancelling context for session %s", sessionID)
		session.Cancel()
	}

	// Remove session directory with retry logic
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		if err := os.RemoveAll(session.OutputDir); err != nil {
			if i < maxRetries-1 {
				log.Printf("[hls] failed to remove session directory %q (attempt %d/%d): %v", session.OutputDir, i+1, maxRetries, err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			log.Printf("[hls] failed to remove session directory %q after %d attempts: %v", session.OutputDir, maxRetries, err)
		} else {
			log.Printf("[hls] removed session directory: %s", session.OutputDir)
			break
		}
	}

	log.Printf("[hls] cleaned up session %s", sessionID)
}

// cleanupLoop periodically removes old sessions
func (m *HLSManager) cleanupLoop() {
	// Run cleanup every 30 seconds for more aggressive cleanup
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupOldSessions()
			m.cleanupProbeCache() // Clean expired probe cache entries
		case <-m.cleanupDone:
			log.Printf("[hls] cleanup loop shutting down")
			return
		}
	}
}

// cleanupOldSessions removes sessions that haven't been accessed in 30 minutes
func (m *HLSManager) cleanupOldSessions() {
	now := time.Now()
	var toCleanup []string

	m.mu.RLock()
	sessionCount := len(m.sessions)
	for id, session := range m.sessions {
		session.mu.RLock()
		lastAccess := session.LastAccess
		completed := session.Completed
		session.mu.RUnlock()

		// Clean up sessions that are either:
		// 1. Inactive for 30 minutes
		// 2. Completed but not accessed in 5 minutes
		inactive := now.Sub(lastAccess) > 30*time.Minute
		completedAndStale := completed && now.Sub(lastAccess) > 5*time.Minute

		if inactive || completedAndStale {
			toCleanup = append(toCleanup, id)
		}
	}
	m.mu.RUnlock()

	if len(toCleanup) > 0 {
		log.Printf("[hls] cleaning up %d inactive sessions (total sessions: %d)", len(toCleanup), sessionCount)
		for _, id := range toCleanup {
			log.Printf("[hls] cleaning up inactive session %s", id)
			m.CleanupSession(id)
		}
	}
}

// Shutdown stops the cleanup loop and cleans up all sessions
func (m *HLSManager) Shutdown() {
	log.Printf("[hls] shutting down HLS manager, cleaning up all sessions")

	close(m.cleanupDone)

	m.mu.Lock()
	sessionIDs := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	m.mu.Unlock()

	log.Printf("[hls] cleaning up %d active sessions", len(sessionIDs))
	for _, id := range sessionIDs {
		m.CleanupSession(id)
	}

	// Final cleanup: remove base directory if empty
	if entries, err := os.ReadDir(m.baseDir); err == nil && len(entries) == 0 {
		if err := os.Remove(m.baseDir); err == nil {
			log.Printf("[hls] removed empty base directory: %s", m.baseDir)
		}
	}

	log.Printf("[hls] shutdown complete")
}

// deleteOldSegments removes old segment files to save disk space, only deleting segments the player no longer needs
func (m *HLSManager) deleteOldSegments(session *HLSSession, justServedSegment string) {
	session.mu.RLock()
	outputDir := session.OutputDir
	// TESTING: hasDV/hasHDR unused since we always use .m4s
	_ = session.HasDV
	_ = session.HasHDR
	sessionID := session.ID
	earliestBuffered := session.EarliestBufferedSegment
	lastServedSegment := session.LastSegmentServed
	session.mu.RUnlock()

	// Use the minimum of EarliestBufferedSegment (from frontend) and LastSegmentServed (from backend)
	// This ensures we don't delete segments that:
	// 1. Haven't been delivered yet (LastSegmentServed protects pending requests)
	// 2. Are still in the player's buffer (EarliestBufferedSegment protects buffered content)
	var safeSegment int
	if earliestBuffered >= 0 && lastServedSegment >= 0 {
		// Use minimum of both for maximum safety
		if earliestBuffered < lastServedSegment {
			safeSegment = earliestBuffered
		} else {
			safeSegment = lastServedSegment
		}
	} else if earliestBuffered >= 0 {
		safeSegment = earliestBuffered
	} else if lastServedSegment >= 0 {
		safeSegment = lastServedSegment
	} else {
		// No info yet, don't delete anything
		return
	}

	// Keep 5 segments behind the safe point for seeking back (~20 seconds at 4s/segment)
	cutoff := safeSegment - 5
	if cutoff < 0 {
		return
	}

	// TESTING: Always use .m4s for all content
	segmentExt := ".m4s"
	// if hasDV || hasHDR {
	// 	segmentExt = ".m4s"
	// }

	// Delete segments older than cutoff (segments the player has already watched)
	deletedCount := 0
	newMinAvailable := cutoff + 1
	for i := 0; i <= cutoff; i++ {
		oldSegment := filepath.Join(outputDir, fmt.Sprintf("segment%d%s", i, segmentExt))
		if err := os.Remove(oldSegment); err == nil {
			deletedCount++
		}
	}

	if deletedCount > 0 {
		// Update MinSegmentAvailable to track what's still on disk
		session.mu.Lock()
		if newMinAvailable > session.MinSegmentAvailable {
			session.MinSegmentAvailable = newMinAvailable
		}
		session.mu.Unlock()
		log.Printf("[hls] session %s: deleted %d old segments (earliestBuffered=%d, lastServed=%d, safeSegment=%d, keeping 5 behind, minAvailable=%d)",
			sessionID, deletedCount, earliestBuffered, lastServedSegment, safeSegment, newMinAvailable)
	}
}

// cleanupOrphanedDirectories removes any leftover session directories from previous runs
func (m *HLSManager) cleanupOrphanedDirectories() {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return // Base dir doesn't exist yet, nothing to clean
		}
		log.Printf("[hls] failed to read base directory for cleanup: %v", err)
		return
	}

	cleaned := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Remove any session directory found at startup (they're all orphaned)
		dirPath := filepath.Join(m.baseDir, entry.Name())
		if err := os.RemoveAll(dirPath); err != nil {
			log.Printf("[hls] failed to remove orphaned directory %q: %v", dirPath, err)
		} else {
			cleaned++
		}
	}

	if cleaned > 0 {
		log.Printf("[hls] cleaned up %d orphaned session directories from previous runs", cleaned)
	}
}

// ============================================================================
// fMP4 Box Parsing for Actual Start Offset Detection
// ============================================================================
//
// When HLS seeks using FFmpeg's input seeking (-ss before -i), FFmpeg seeks to
// the nearest keyframe, not the exact requested time. This causes VTT subtitles
// (which have absolute timestamps) to desync because the frontend uses the
// *requested* time for subtitle offset, not the *actual* keyframe time.
//
// The solution is to parse the tfdt (Track Fragment Decode Time) box from the
// first fMP4 segment to get the actual start time, then use that for subtitles.
//
// fMP4 box structure:
//   init.mp4: ftyp -> moov -> trak -> mdia -> mdhd (contains timescale)
//   segment.m4s: moof -> traf -> tfdt (contains baseMediaDecodeTime)
//
// actualStartSeconds = baseMediaDecodeTime / timescale

// parseTimescaleFromInit extracts the video timescale from init.mp4's mdhd box.
// The timescale is the number of time units per second (commonly 90000 for video).
func parseTimescaleFromInit(initPath string) (uint32, error) {
	data, err := os.ReadFile(initPath)
	if err != nil {
		return 0, fmt.Errorf("read init.mp4: %w", err)
	}

	// Search for mdhd box (media header) - it contains the timescale
	// mdhd can be version 0 (32-bit times) or version 1 (64-bit times)
	// Structure: [size:4][type:4][version:1][flags:3][times...][timescale:4][duration...]
	mdhdMarker := []byte{'m', 'd', 'h', 'd'}
	idx := bytes.Index(data, mdhdMarker)
	if idx == -1 {
		return 0, fmt.Errorf("mdhd box not found in init.mp4")
	}

	// Move past the box type to the box content
	pos := idx + 4
	if pos+20 > len(data) {
		return 0, fmt.Errorf("mdhd box too short")
	}

	version := data[pos]
	var timescaleOffset int
	if version == 0 {
		// Version 0: version(1) + flags(3) + creation_time(4) + modification_time(4) + timescale(4)
		timescaleOffset = pos + 1 + 3 + 4 + 4
	} else {
		// Version 1: version(1) + flags(3) + creation_time(8) + modification_time(8) + timescale(4)
		timescaleOffset = pos + 1 + 3 + 8 + 8
	}

	if timescaleOffset+4 > len(data) {
		return 0, fmt.Errorf("mdhd box truncated at timescale")
	}

	timescale := binary.BigEndian.Uint32(data[timescaleOffset : timescaleOffset+4])
	return timescale, nil
}

// parseTfdtFromSegment extracts the baseMediaDecodeTime from a segment's tfdt box.
// This is the actual start time (in timescale units) that FFmpeg seeked to.
func parseTfdtFromSegment(segmentPath string, timescale uint32) (float64, error) {
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		return 0, fmt.Errorf("read segment: %w", err)
	}

	// Search for tfdt box (track fragment decode time)
	// Structure: [size:4][type:4][version:1][flags:3][baseMediaDecodeTime:4 or 8]
	tfdtMarker := []byte{'t', 'f', 'd', 't'}
	idx := bytes.Index(data, tfdtMarker)
	if idx == -1 {
		return 0, fmt.Errorf("tfdt box not found in segment")
	}

	// Move past the box type to the box content
	pos := idx + 4
	if pos+8 > len(data) {
		return 0, fmt.Errorf("tfdt box too short")
	}

	version := data[pos]
	var baseMediaDecodeTime uint64

	if version == 0 {
		// Version 0: 32-bit baseMediaDecodeTime
		if pos+8 > len(data) {
			return 0, fmt.Errorf("tfdt v0 truncated")
		}
		baseMediaDecodeTime = uint64(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
	} else {
		// Version 1: 64-bit baseMediaDecodeTime
		if pos+12 > len(data) {
			return 0, fmt.Errorf("tfdt v1 truncated")
		}
		baseMediaDecodeTime = binary.BigEndian.Uint64(data[pos+4 : pos+12])
	}

	// Convert to seconds
	if timescale == 0 {
		return 0, fmt.Errorf("timescale is zero")
	}
	actualStartSeconds := float64(baseMediaDecodeTime) / float64(timescale)
	return actualStartSeconds, nil
}

// parseActualStartOffset reads the init.mp4 and first segment to determine
// the actual start time (keyframe-aligned) for subtitle synchronization.
func parseActualStartOffset(initPath, segmentPath string) (float64, error) {
	timescale, err := parseTimescaleFromInit(initPath)
	if err != nil {
		return 0, fmt.Errorf("parse timescale: %w", err)
	}

	actualStart, err := parseTfdtFromSegment(segmentPath, timescale)
	if err != nil {
		return 0, fmt.Errorf("parse tfdt: %w", err)
	}

	return actualStart, nil
}

// WaitForActualStartOffset waits for the first fMP4 segment to be generated
// and parses the tfdt box to get the actual keyframe-aligned start time.
// This should be called after CreateSession for warm start fMP4 sessions.
// Returns the actual start offset, or the requested offset if parsing fails.
func (m *HLSManager) WaitForActualStartOffset(session *HLSSession, timeout time.Duration) float64 {
	session.mu.RLock()
	hasDV := session.HasDV
	hasHDR := session.HasHDR
	startOffset := session.StartOffset
	outputDir := session.OutputDir
	session.mu.RUnlock()

	// Only needed for fMP4 warm starts
	if (!hasDV && !hasHDR) || startOffset <= 0 {
		return startOffset
	}

	initPath := filepath.Join(outputDir, "init.mp4")
	segment0Path := filepath.Join(outputDir, "segment0.m4s")

	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	// Wait for both init.mp4 and segment0.m4s to exist with non-zero size
	for time.Now().Before(deadline) {
		initInfo, initErr := os.Stat(initPath)
		segInfo, segErr := os.Stat(segment0Path)

		if initErr == nil && initInfo.Size() > 0 && segErr == nil && segInfo.Size() > 0 {
			// Files exist, try to parse
			actualStart, err := parseActualStartOffset(initPath, segment0Path)
			if err != nil {
				log.Printf("[hls] session %s: warning - could not parse actual start offset: %v (using requested: %.3fs)",
					session.ID, err, startOffset)
				return startOffset
			}

			delta := actualStart - startOffset
			log.Printf("[hls] session %s: actual start offset: %.3fs (requested: %.3fs, delta: %.3fs)",
				session.ID, actualStart, startOffset, delta)

			session.mu.Lock()
			session.ActualStartOffset = actualStart
			session.mu.Unlock()

			return actualStart
		}

		time.Sleep(pollInterval)
	}

	log.Printf("[hls] session %s: timeout waiting for first segment to parse actual start offset (using requested: %.3fs)",
		session.ID, startOffset)
	return startOffset
}

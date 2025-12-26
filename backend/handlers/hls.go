package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
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
	Duration     float64 // Total duration in seconds from ffprobe
	StartOffset  float64 // Requested start offset in seconds for session warm starts

	// Profile tracking
	ProfileID   string
	ProfileName string

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
	MinSegmentRequested int // Minimum segment number that has been requested (-1 = none yet)
	MaxSegmentRequested int // Maximum segment number that has been requested (-1 = none yet)
	MinSegmentAvailable int // Minimum segment number still available on disk (for playlist filtering)
	Paused              bool // True if FFmpeg is paused (SIGSTOP) waiting for player to catch up

	// Input error recovery (for usenet disconnections)
	InputErrorDetected bool // Set to true when FFmpeg input stream fails (usenet disconnect)
	RecoveryAttempts   int  // Number of times we've attempted to recover this session
	forceAAC           bool // Cached forceAAC setting for recovery restarts

	// Fatal error tracking (unplayable streams)
	FatalError       string // Set when stream is determined to be unplayable (persistent bitstream errors)

	// Cached probe data from unified probe (avoids multiple ffprobe calls)
	ProbeData *UnifiedProbeResult
	FatalErrorTime   time.Time
	BitstreamErrors  int // Count of bitstream filter errors (to detect persistent issues)
}

type audioStreamInfo struct {
	Index int
	Codec string
}

type subtitleStreamInfo struct {
	Index int
	Codec string
}

// UnifiedProbeResult holds all data extracted from a single ffprobe call
type UnifiedProbeResult struct {
	Duration         float64
	ColorTransfer    string              // e.g., "smpte2084" for HDR, "bt709" for SDR
	AudioStreams     []audioStreamInfo
	SubtitleStreams  []subtitleStreamInfo
	HasTrueHD        bool
	HasCompatibleAudio bool
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
	hlsSegmentDuration = 4.0

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

	// First try HEAD request (faster, no body)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, externalURL, nil)
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

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, externalURL, nil)
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

// CreateSession starts a new HLS transcoding session
func (m *HLSManager) CreateSession(ctx context.Context, path string, originalPath string, hasDV bool, dvProfile string, hasHDR bool, forceAAC bool, startOffset float64, audioTrackIndex int, subtitleTrackIndex int, profileID string, profileName string) (*HLSSession, error) {
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
		ProfileID:           profileID,
		ProfileName:         profileName,
		AudioTrackIndex:     audioTrackIndex,
		SubtitleTrackIndex:  subtitleTrackIndex,
		StreamStartTime:     now,
		LastSegmentRequest:  now, // Initialize to now to avoid immediate timeout
		MinSegmentRequested: -1,  // Initialize to -1 (no segments requested yet)
		MaxSegmentRequested: -1,  // Initialize to -1 (no segments requested yet)
		ProbeData:           probeData, // Cache unified probe results for startTranscoding
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

// probeAllMetadata performs a single ffprobe call to extract all metadata needed for HLS transcoding.
// This consolidates what was previously 4 separate ffprobe calls (duration, color, audio, subtitles).
func (m *HLSManager) probeAllMetadata(ctx context.Context, path string) (*UnifiedProbeResult, error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	isExternalURL := strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")

	// For external URLs, probe directly
	if isExternalURL {
		return m.probeAllMetadataFromURL(ctx, path)
	}

	// For provider-backed paths, try direct URL first for better metadata access
	if m.streamer != nil {
		if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
			if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
				log.Printf("[hls] probing all metadata using direct URL for path: %s", path)
				return m.probeAllMetadataFromURL(ctx, directURL)
			}
		}
	}

	// Fall back to pipe-based probe
	if m.streamer == nil {
		return nil, fmt.Errorf("stream provider not configured")
	}

	log.Printf("[hls] probing all metadata using pipe for path: %s", path)
	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return nil, fmt.Errorf("provider stream returned empty body")
	}
	defer resp.Close()

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		io.CopyBuffer(pw, resp.Body, buf)
	}()

	probeCtx, probeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr
	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return nil, fmt.Errorf("ffprobe execution: %w", err)
	}

	return m.parseUnifiedProbeOutput(output)
}

// probeAllMetadataFromURL probes all metadata directly from an external URL
func (m *HLSManager) probeAllMetadataFromURL(ctx context.Context, url string) (*UnifiedProbeResult, error) {
	log.Printf("[hls] probing all metadata from external URL (unified probe)")

	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe execution: %w", err)
	}

	return m.parseUnifiedProbeOutput(output)
}

// parseUnifiedProbeOutput parses the JSON output from ffprobe -show_format -show_streams
func (m *HLSManager) parseUnifiedProbeOutput(output []byte) (*UnifiedProbeResult, error) {
	var probeData struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index         int    `json:"index"`
			CodecType     string `json:"codec_type"`
			CodecName     string `json:"codec_name"`
			ColorTransfer string `json:"color_transfer"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &probeData); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	result := &UnifiedProbeResult{}

	// Parse duration
	if probeData.Format.Duration != "" {
		if dur, err := strconv.ParseFloat(probeData.Format.Duration, 64); err == nil {
			result.Duration = dur
		}
	}

	compatibleCodecs := map[string]bool{
		"aac": true, "ac3": true, "eac3": true, "mp3": true,
	}

	// Process streams
	for _, stream := range probeData.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))

		switch stream.CodecType {
		case "video":
			// Get color_transfer from first video stream
			if result.ColorTransfer == "" && stream.ColorTransfer != "" {
				result.ColorTransfer = stream.ColorTransfer
			}
		case "audio":
			result.AudioStreams = append(result.AudioStreams, audioStreamInfo{
				Index: stream.Index,
				Codec: codec,
			})
			if codec == "truehd" || codec == "mlp" {
				result.HasTrueHD = true
			}
			if compatibleCodecs[codec] {
				result.HasCompatibleAudio = true
			}
		case "subtitle":
			result.SubtitleStreams = append(result.SubtitleStreams, subtitleStreamInfo{
				Index: stream.Index,
				Codec: codec,
			})
		}
	}

	log.Printf("[hls] unified probe results: duration=%.2fs colorTransfer=%q audioStreams=%d subStreams=%d hasTrueHD=%v hasCompatibleAudio=%v",
		result.Duration, result.ColorTransfer, len(result.AudioStreams), len(result.SubtitleStreams),
		result.HasTrueHD, result.HasCompatibleAudio)

	return result, nil
}

// probeAudioStreams inspects audio streams for codec compatibility and exposes their ordering
func (m *HLSManager) probeAudioStreams(ctx context.Context, path string) (streams []audioStreamInfo, hasTrueHD bool, hasCompatibleAudio bool, err error) {
	if m.ffprobePath == "" {
		return nil, false, true, nil // Assume compatible if we can't probe
	}

	// For external URLs, probe directly instead of going through provider
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return m.probeAudioStreamsFromURL(ctx, path)
	}

	if m.streamer == nil {
		return nil, false, true, nil // Assume compatible if no provider
	}

	// Try to get a direct URL for probing (needed for files with moov atom at end)
	// This allows ffprobe to seek instead of relying on the first 16MB containing metadata
	if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
			log.Printf("[hls] probing audio using direct URL for path: %s", path)
			return m.probeAudioStreamsFromURL(ctx, directURL)
		} else if err != nil {
			log.Printf("[hls] failed to get direct URL for audio probe, falling back to pipe: %v", err)
		}
	}

	// Fall back to pipe-based probe with first 16MB
	log.Printf("[hls] probing audio using pipe method for path: %s", path)
	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		log.Printf("[hls] failed to probe audio streams: %v", err)
		return nil, false, true, nil // Assume compatible on error
	}
	if resp.Body == nil {
		resp.Close()
		return nil, false, true, nil
	}
	defer resp.Close()

	// Create pipe to feed data to ffprobe
	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	// Run ffprobe with timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		log.Printf("[hls] ffprobe audio failed: %v", err)
		return nil, false, true, nil // Assume compatible on error
	}

	// Parse JSON output
	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe output: %v", err)
		return nil, false, true, nil
	}

	compatibleCodecs := map[string]bool{
		"aac":  true,
		"ac3":  true,
		"eac3": true,
		"mp3":  true,
	}

	streams = make([]audioStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, audioStreamInfo{Index: stream.Index, Codec: codec})
		if codec == "truehd" || codec == "mlp" {
			hasTrueHD = true
		}
		if compatibleCodecs[codec] {
			hasCompatibleAudio = true
		}
	}

	log.Printf("[hls] audio probe results: hasTrueHD=%v hasCompatibleAudio=%v codecs=%d",
		hasTrueHD, hasCompatibleAudio, len(result.Streams))

	return streams, hasTrueHD, hasCompatibleAudio, nil
}

// probeAudioStreamsFromURL probes audio streams directly from an external URL
func (m *HLSManager) probeAudioStreamsFromURL(ctx context.Context, url string) (streams []audioStreamInfo, hasTrueHD bool, hasCompatibleAudio bool, err error) {
	log.Printf("[hls] probing audio streams from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "a",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[hls] ffprobe audio from URL failed: %v", err)
		return nil, false, true, nil // Assume compatible on error
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe audio output from URL: %v", err)
		return nil, false, true, nil
	}

	compatibleCodecs := map[string]bool{
		"aac":  true,
		"ac3":  true,
		"eac3": true,
		"mp3":  true,
	}

	streams = make([]audioStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, audioStreamInfo{Index: stream.Index, Codec: codec})
		if codec == "truehd" || codec == "mlp" {
			hasTrueHD = true
		}
		if compatibleCodecs[codec] {
			hasCompatibleAudio = true
		}
	}

	log.Printf("[hls] audio probe from URL results: hasTrueHD=%v hasCompatibleAudio=%v codecs=%d",
		hasTrueHD, hasCompatibleAudio, len(result.Streams))

	return streams, hasTrueHD, hasCompatibleAudio, nil
}

// probeSubtitleStreams lists subtitle streams and preserves their ordering for FFmpeg mapping
func (m *HLSManager) probeSubtitleStreams(ctx context.Context, path string) (streams []subtitleStreamInfo, err error) {
	if m.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	// For external URLs, probe directly instead of going through provider
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return m.probeSubtitleStreamsFromURL(ctx, path)
	}

	if m.streamer == nil {
		return nil, fmt.Errorf("streamer not configured")
	}

	// Try to get a direct URL for probing (needed for files with moov atom at end)
	// This allows ffprobe to seek instead of relying on the first 16MB containing metadata
	if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		if directURL, err := directProvider.GetDirectURL(ctx, path); err == nil && directURL != "" {
			log.Printf("[hls] probing subtitles using direct URL for path: %s", path)
			return m.probeSubtitleStreamsFromURL(ctx, directURL)
		} else if err != nil {
			log.Printf("[hls] failed to get direct URL for subtitle probe, falling back to pipe: %v", err)
		}
	}

	// Fall back to pipe-based probe with first 16MB
	log.Printf("[hls] probing subtitles using pipe method for path: %s", path)
	request := streaming.Request{
		Path:        path,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215",
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		log.Printf("[hls] failed to probe subtitle streams: %v", err)
		return nil, err
	}
	if resp.Body == nil {
		resp.Close()
		return nil, fmt.Errorf("subtitle probe returned empty body")
	}
	defer resp.Close()

	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		log.Printf("[hls] ffprobe subtitle failed: %v", err)
		return nil, err
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe subtitle output: %v", err)
		return nil, err
	}

	streams = make([]subtitleStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, subtitleStreamInfo{Index: stream.Index, Codec: codec})
	}

	log.Printf("[hls] subtitle probe results: streams=%d", len(streams))
	return streams, nil
}

// probeSubtitleStreamsFromURL probes subtitle streams directly from an external URL
func (m *HLSManager) probeSubtitleStreamsFromURL(ctx context.Context, url string) (streams []subtitleStreamInfo, err error) {
	log.Printf("[hls] probing subtitle streams from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name",
		"-of", "json",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[hls] ffprobe subtitle from URL failed: %v", err)
		return nil, err
	}

	var result struct {
		Streams []struct {
			Index     int    `json:"index"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[hls] failed to parse ffprobe subtitle output from URL: %v", err)
		return nil, err
	}

	streams = make([]subtitleStreamInfo, 0, len(result.Streams))
	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		streams = append(streams, subtitleStreamInfo{Index: stream.Index, Codec: codec})
	}

	log.Printf("[hls] subtitle probe from URL results: streams=%d", len(streams))
	return streams, nil
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

	if session.StartOffset > 0 {
		log.Printf("[hls] session %s: applying start offset %.3fs", session.ID, session.StartOffset)
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
	log.Printf("[hls] session %s: checking for direct URL support (startOffset=%.3f)", session.ID, session.StartOffset)
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

	// Check if the path is an external URL (e.g., from AIOStreams pre-resolved streams)
	isExternalURL := strings.HasPrefix(session.Path, "http://") || strings.HasPrefix(session.Path, "https://")

	// Always prefer direct URL when available - this allows FFmpeg to seek to find metadata
	// (e.g., moov atom at end of MP4 files) and enables better seeking/error recovery
	if hasDirectURL {
		log.Printf("[hls] session %s: using direct URL: %s (startOffset=%.3f, isExternal=%v)", session.ID, directURL, session.StartOffset, isExternalURL)
		usingPipe = false
	} else {
		// Fall back to pipe streaming only if direct URL not available
		providerStartTime := time.Now()
		log.Printf("[hls] session %s: requesting stream from provider", session.ID)

		// Calculate byte offset from time offset for seeking support
		var rangeHeader string
		if session.StartOffset > 0 && session.Duration > 0 {
			// Get file size for byte offset calculation
			headResp, err := m.streamer.Stream(ctx, streaming.Request{
				Path:   session.Path,
				Method: http.MethodHead,
			})
			if err == nil && headResp != nil {
				fileSize := headResp.ContentLength
				headResp.Close()

				if fileSize > 0 {
					// Calculate approximate byte offset: (fileSize / duration) * startOffset
					byteOffset := int64(float64(fileSize) / session.Duration * session.StartOffset)

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
										log.Printf("[hls] session %s: backing off %d bytes to help align matroska cluster (startOffset=%.3fs)",
											session.ID, matroskaSeekBackoffBytes, session.StartOffset)
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
								session.ID, byteOffset, session.StartOffset, fileSize, session.Duration)
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
		"-loglevel", "error",
		// A/V sync flags: generate PTS if missing, discard corrupt packets
		// This helps prevent audio/video desync issues especially with HLS streams
		"-fflags", "+genpts+discardcorrupt",
	}
	// NOTE: -strict unofficial is added AFTER -i as an output option (see below)
	// Placing it before -i doesn't enable writing dvcC boxes to the output

	// Apply input seeking if we have a seekable source (URL) and startOffset > 0
	// Input seeking (-ss before -i) is most efficient as FFmpeg seeks before decoding
	if !usingPipe && session.StartOffset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", session.StartOffset))
	}

	// Add input source
	if usingPipe {
		args = append(args, "-i", "pipe:0")

		// For pipe inputs with seeking, add output seeking (-ss after -i)
		// This tells FFmpeg to decode and sync to the target time
		// Combined with byte-level seeking, this is efficient and safe:
		// - Byte seeking reduces bandwidth (usenet starts from calculated offset)
		// - Output seeking ensures FFmpeg syncs to the next keyframe
		if session.StartOffset > 0 {
			args = append(args, "-ss", fmt.Sprintf("%.3f", session.StartOffset))
			log.Printf("[hls] session %s: using output seeking to sync to %.3fs after byte offset", session.ID, session.StartOffset)
		}
	} else {
		args = append(args, "-i", directURL)
	}

	// If we're seeking and know the total duration, tell FFmpeg how much content to expect
	// This ensures the HLS playlist reports the correct remaining duration
	if session.StartOffset > 0 && session.Duration > 0 {
		remainingDuration := session.Duration - session.StartOffset
		if remainingDuration > 0 {
			args = append(args, "-t", fmt.Sprintf("%.3f", remainingDuration))
			log.Printf("[hls] session %s: limiting duration to remaining %.3fs (total=%.3fs, offset=%.3fs)",
				session.ID, remainingDuration, session.Duration, session.StartOffset)
		}
	}

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
			// Find the first compatible audio stream (excluding TrueHD)
			log.Printf("[hls] session %s: no specific audio track selected, defaulting to first compatible stream", session.ID)
			compatibleCodecs := map[string]bool{
				"aac":  true,
				"ac3":  true,
				"eac3": true,
				"mp3":  true,
			}
			for _, stream := range audioStreams {
				if compatibleCodecs[stream.Codec] {
					audioMap := fmt.Sprintf("0:%d", stream.Index)
					args = append(args, "-map", audioMap)
					log.Printf("[hls] session %s: mapped first compatible audio stream %d (codec=%s)",
						session.ID, stream.Index, stream.Codec)
					break // Only map the first compatible stream
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
		segmentExt = ".ts"
		// Check if it's HEVC and tag as hvc1
		args = append(args, "-tag:v", "hvc1")
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
					// first_pts=0 ensures audio starts at the beginning of the stream
					log.Printf("[hls] session %s: transcoding selected TrueHD track to AAC", session.ID)
					args = append(args,
						"-af", "aresample=async=1000:first_pts=0",
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
				"-af", "aresample=async=1000:first_pts=0",
				"-c:a:0", "aac", "-ac:a:0", "6", "-ar:a:0", "48000", "-channel_layout:a:0", "5.1", "-b:a:0", "192k",
				"-c:a:1", "copy")
		} else if hasTrueHD && !hasCompatibleAudio {
			// If only TrueHD exists, we must transcode it
			// Must specify channel_layout for iOS AVPlayer compatibility (otherwise shows "media may be damaged")
			// TrueHD has variable timing - use aresample filter with async to maintain A/V sync
			log.Printf("[hls] session %s: transcoding TrueHD to AAC (no compatible alternative)", session.ID)
			args = append(args,
				"-af", "aresample=async=1000:first_pts=0",
				"-c:a", "aac", "-ac", "6", "-ar", "48000", "-channel_layout", "5.1", "-b:a", "192k")
		} else {
			// Copy compatible audio
			args = append(args, "-c:a", "copy")
		}
	}

	// Subtitle handling
	// Note: FFmpeg's HLS muxer has different subtitle support for fMP4 vs MPEG-TS:
	// - fMP4 (Dolby Vision/HDR): Subtitles are extracted to a sidecar VTT file for overlay rendering
	// - MPEG-TS: Supports multiple WebVTT subtitle streams in the HLS playlist
	subtitleHandled := false
	var sidecarSubtitleRelativeIndex int = -1 // Track for sidecar extraction (fMP4 only)

	if session.SubtitleTrackIndex >= 0 {
		relativeIndex := -1
		var selectedCodec string

		for pos, stream := range subtitleStreams {
			if stream.Index == session.SubtitleTrackIndex {
				relativeIndex = pos
				selectedCodec = stream.Codec
				break
			}
		}

		if relativeIndex >= 0 {
			// Check for unsupported subtitle codecs (bitmap-based)
			if selectedCodec == "hdmv_pgs_subtitle" || selectedCodec == "pgs" || selectedCodec == "dvd_subtitle" {
				log.Printf("[hls] session %s: requested subtitle stream %d has unsupported codec %q; skipping mapping",
					session.ID, session.SubtitleTrackIndex, selectedCodec)
				subtitleHandled = true
			} else if needsFmp4 {
				// For fMP4 (DV/HDR), don't mux subtitles into HLS - extract to sidecar VTT instead
				// iOS AVPlayer in fullscreen doesn't properly expose muxed subtitles to react-native-video
				sidecarSubtitleRelativeIndex = relativeIndex
				log.Printf("[hls] session %s: will extract subtitle stream to sidecar VTT (streamIndex=%d relativeIndex=%d codec=%s)",
					session.ID, session.SubtitleTrackIndex, relativeIndex, selectedCodec)
				subtitleHandled = true
			} else {
				// For MPEG-TS, mux subtitles into HLS playlist as usual
				subtitleMap := fmt.Sprintf("0:s:%d", relativeIndex)
				args = append(args, "-map", subtitleMap, "-c:s", "webvtt")
				log.Printf("[hls] session %s: mapping specific subtitle stream (streamIndex=%d relativeIndex=%d codec=%s)",
					session.ID, session.SubtitleTrackIndex, relativeIndex, selectedCodec)
				subtitleHandled = true
			}
		} else if needsFmp4 {
			log.Printf("[hls] session %s: requested subtitle stream index %d not found for fMP4/DV output; skipping mapping",
				session.ID, session.SubtitleTrackIndex)
			subtitleHandled = true
		} else {
			log.Printf("[hls] session %s: requested subtitle stream index %d not found; skipping subtitle mapping",
				session.ID, session.SubtitleTrackIndex)
			subtitleHandled = true
		}
	}

	if !subtitleHandled {
		if !needsFmp4 {
			// For MPEG-TS segments without specific selection, map text-based subtitle streams only
			// Filter out bitmap codecs (PGS, DVD) which can't be converted to WebVTT
			textSubtitleCount := 0
			for pos, stream := range subtitleStreams {
				if stream.Codec == "hdmv_pgs_subtitle" || stream.Codec == "pgs" || stream.Codec == "dvd_subtitle" {
					log.Printf("[hls] session %s: skipping bitmap subtitle stream %d (codec=%s) for auto-mapping",
						session.ID, stream.Index, stream.Codec)
					continue
				}
				subtitleMap := fmt.Sprintf("0:s:%d", pos)
				args = append(args, "-map", subtitleMap)
				textSubtitleCount++
			}
			if textSubtitleCount > 0 {
				args = append(args, "-c:s", "webvtt")
				log.Printf("[hls] session %s: mapping %d text-based subtitle streams with WebVTT codec", session.ID, textSubtitleCount)
			} else if len(subtitleStreams) > 0 {
				log.Printf("[hls] session %s: no text-based subtitles found (%d bitmap streams skipped)", session.ID, len(subtitleStreams))
			}
		} else {
			// For fMP4/DV without specific selection, skip subtitles to avoid errors
			log.Printf("[hls] session %s: skipping subtitle streams for fMP4/DV (no subtitle selected)", session.ID)
		}
	}

	// For fMP4 with a valid subtitle track, add a second output to extract subtitles to sidecar VTT
	// This runs alongside the HLS output, writing cues progressively as FFmpeg processes the stream
	sidecarVTTPath := ""
	if sidecarSubtitleRelativeIndex >= 0 {
		sidecarVTTPath = filepath.Join(session.OutputDir, "subtitles.vtt")
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

		// For fMP4 with sidecar subtitles, we need to structure FFmpeg args carefully:
		// FFmpeg applies -map and codec options to the next output file in sequence.
		// So we first output the HLS stream (video/audio), then the VTT subtitle file.
		if sidecarVTTPath != "" {
			// First output: HLS stream with video and audio only
			args = append(args,
				"-f", "hls",
				"-hls_time", "4",
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

			// Second output: Sidecar VTT file with subtitle track only
			// Map the subtitle stream and output as WebVTT
			// Use -flush_packets 1 to write subtitle cues immediately as they're processed
			subtitleMap := fmt.Sprintf("0:s:%d", sidecarSubtitleRelativeIndex)
			args = append(args,
				"-map", subtitleMap,
				"-c", "webvtt",
				"-f", "webvtt",
				"-flush_packets", "1",
				sidecarVTTPath,
			)
			log.Printf("[hls] session %s: adding sidecar VTT output at %s (subtitle stream %d)", session.ID, sidecarVTTPath, sidecarSubtitleRelativeIndex)
		} else {
			// No sidecar subtitles - just output HLS
			args = append(args,
				"-f", "hls",
				"-hls_time", "4",
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
		}
	} else {
		// Use MPEG-TS segments for non-HDR content
		args = append(args,
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "0",
			"-hls_playlist_type", "event",
			"-hls_flags", "independent_segments+temp_file",
			"-hls_segment_type", "mpegts",
			"-hls_segment_filename", segmentPattern,
			"-hls_subtitle_path", session.OutputDir, // Path for subtitle files
			"-start_number", segmentStartNum,
			playlistPath,
		)
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

		// Wrap with debug reader to track bytes read and detect when source stream ends
		debugPipe := newDebugReader(pipeReader, session.ID)
		cmd.Stdin = debugPipe
		log.Printf("[hls] session %s: SOURCE_STREAM started (wrapped with debug reader)", session.ID)
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
				hasDV := session.HasDV
				hasHDR := session.HasHDR
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
				segmentExt := ".ts"
				if hasDV || hasHDR {
					segmentExt = ".m4s"
				}
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

				// NOTE: Segment cleanup disabled during playback.
				// EVENT playlists require all segments to remain available.
				// Cleanup happens when the session ends instead.

				// Skip rate limiting if no segments found yet
				if highestSegment < 0 {
					continue
				}

				// NOTE: FFmpeg pause/resume disabled - was causing playback issues
				// bufferAhead := highestSegment - maxRequested
				_ = highestSegment // unused now

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

	// Log FFmpeg exit details
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	log.Printf("[hls] session %s: FFMPEG_EXIT - exitCode=%d waitDuration=%v err=%v ctxErr=%v",
		session.ID, exitCode, waitDuration, err, ctx.Err())

	// Signal monitoring goroutines to stop
	close(perfDone)
	close(idleDone)
	close(rateLimitDone)

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

		// Calculate new start offset based on segments already created
		// Each segment is hlsSegmentDuration seconds
		newStartOffset := session.StartOffset + float64(highestSegment+1)*hlsSegmentDuration

		// Don't exceed the total duration
		if session.Duration > 0 && newStartOffset >= session.Duration {
			log.Printf("[hls] session %s: input error recovery would exceed duration (offset %.2f >= duration %.2f), marking complete",
				session.ID, newStartOffset, session.Duration)
			session.mu.Lock()
			session.Completed = true
			session.mu.Unlock()
			return nil
		}

		log.Printf("[hls] session %s: input error recovery - highest segment=%d, new offset=%.2fs (was %.2fs), attempt %d/%d",
			session.ID, highestSegment, newStartOffset, session.StartOffset, recoveryAttempts+1, hlsMaxRecoveryAttempts)

		// DON'T clean up existing segments - we want to keep them for seamless playback
		// Only remove the potentially incomplete last segment and playlist (will be regenerated)
		// Actually, let's keep everything and let FFmpeg overwrite the playlist
		// The existing segments are still valid and can be served

		// Reset session state for restart, but keep track of progress
		session.mu.Lock()
		session.FFmpegCmd = nil
		session.FFmpegPID = 0
		session.Completed = false
		session.InputErrorDetected = false // Reset so we can detect new errors
		session.RecoveryAttempts++
		session.StartOffset = newStartOffset // Update start offset to resume position
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
		log.Printf("[hls] session %s: restarting transcoding from %.2fs after input error (recovery attempt %d/%d)",
			session.ID, newStartOffset, recoveryAttempts+1, hlsMaxRecoveryAttempts)
		return m.startTranscoding(newCtx, session, cachedForceAAC)
	}

	// Calculate expected vs actual segments for debugging
	highestSegment := m.findHighestSegmentNumber(session)
	expectedDuration := session.Duration - session.StartOffset
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
	log.Printf("[hls] session %s: TRANSCODING_COMPLETE - duration=%.2fs startOffset=%.2fs expectedDuration=%.2fs",
		session.ID, session.Duration, session.StartOffset, expectedDuration)
	log.Printf("[hls] session %s: TRANSCODING_COMPLETE - expectedSegments=%d actualSegments=%d (highest=%d) completion=%.1f%%",
		session.ID, expectedSegments, actualSegments, highestSegment, completionPercent)

	if idleTriggered {
		log.Printf("[hls] session %s: transcoding stopped due to IDLE_TIMEOUT after %v (bytes streamed: %d, segments: %d)",
			session.ID, completionTime, session.BytesStreamed, session.SegmentsCreated)
	} else if completionPercent < 95 && expectedSegments > 0 {
		log.Printf("[hls] session %s: WARNING - PREMATURE_COMPLETION at %.1f%% (expected %d segments, got %d)",
			session.ID, completionPercent, expectedSegments, actualSegments)
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

// probeDuration attempts to get the file duration using ffprobe
func (m *HLSManager) probeDuration(ctx context.Context, cleanPath string) (float64, error) {
	if m.ffprobePath == "" {
		return 0, fmt.Errorf("ffprobe not configured")
	}

	// For external URLs, probe directly
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		return m.probeDurationFromURL(ctx, cleanPath)
	}

	if m.streamer == nil {
		return 0, fmt.Errorf("stream provider not configured")
	}

	// Request first 16MB to probe
	request := streaming.Request{
		Path:        cleanPath,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return 0, fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return 0, fmt.Errorf("provider stream returned empty body")
	}
	defer resp.Close()

	// Create pipe to feed data to ffprobe
	pr, pw := io.Pipe()
	copyDone := make(chan error, 1)

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		copyDone <- copyErr
	}()

	// Run ffprobe with timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return 0, fmt.Errorf("ffprobe execution: %w", err)
	}

	// Parse duration from output
	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", durationStr, err)
	}

	return duration, nil
}

// probeDurationFromURL probes duration directly from an external URL
func (m *HLSManager) probeDurationFromURL(ctx context.Context, url string) (float64, error) {
	log.Printf("[hls] probing duration from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe execution: %w", err)
	}

	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", durationStr, err)
	}

	log.Printf("[hls] probed duration from URL: %.2f seconds", duration)
	return duration, nil
}

// probeColorMetadata checks if the video has correct HDR color tagging
// Returns color_transfer value (e.g., "smpte2084" for HDR, "bt709" for SDR/incorrect)
func (m *HLSManager) probeColorMetadata(ctx context.Context, cleanPath string) (string, error) {
	if m.ffprobePath == "" {
		return "", fmt.Errorf("ffprobe not configured")
	}

	// For external URLs, probe directly
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		return m.probeColorMetadataFromURL(ctx, cleanPath)
	}

	if m.streamer == nil {
		return "", fmt.Errorf("stream provider not configured")
	}

	// Request first 16MB to probe
	request := streaming.Request{
		Path:        cleanPath,
		Method:      http.MethodGet,
		RangeHeader: "bytes=0-16777215", // 16MB
	}

	resp, err := m.streamer.Stream(ctx, request)
	if err != nil {
		return "", fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return "", fmt.Errorf("provider stream returned empty body")
	}
	defer resp.Close()

	// Create pipe to feed data to ffprobe
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		buf := make([]byte, 128*1024)
		io.CopyBuffer(pw, resp.Body, buf)
	}()

	// Run ffprobe with timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=color_transfer",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", "pipe:0",
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	cmd.Stdin = pr

	output, err := cmd.Output()
	if err != nil {
		pw.CloseWithError(err)
		return "", fmt.Errorf("ffprobe execution: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// probeColorMetadataFromURL probes color metadata directly from an external URL
func (m *HLSManager) probeColorMetadataFromURL(ctx context.Context, url string) (string, error) {
	log.Printf("[hls] probing color metadata from external URL: %s", url)

	// Use 60 second timeout for external URLs (need to download data over network)
	probeCtx, probeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer probeCancel()

	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=color_transfer",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", url,
	}

	cmd := exec.CommandContext(probeCtx, m.ffprobePath, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe execution: %w", err)
	}

	colorTransfer := strings.TrimSpace(string(output))
	log.Printf("[hls] probed color_transfer from URL: %q", colorTransfer)
	return colorTransfer, nil
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

	// If frontend reports playback time, use it to update MaxSegmentRequested for rate limiting
	if timeStr := r.URL.Query().Get("time"); timeStr != "" {
		if playbackTime, err := strconv.ParseFloat(timeStr, 64); err == nil && playbackTime >= 0 {
			// Calculate segment number from playback time (hlsSegmentDuration = 4 seconds)
			segmentNum := int(playbackTime / hlsSegmentDuration)
			if segmentNum > session.MaxSegmentRequested {
				session.MaxSegmentRequested = segmentNum
				log.Printf("[hls] session %s: keepalive updated MaxSegmentRequested to %d (time=%.1fs)",
					sessionID, segmentNum, playbackTime)
			}
		}
	}
	session.mu.Unlock()

	log.Printf("[hls] session %s: keepalive received, extended idle timeout", sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
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

	// Get API key from request
	apiKey := r.URL.Query().Get("apiKey")
	if apiKey == "" {
		// Try other auth methods
		apiKey = r.Header.Get("X-API-Key")
		if apiKey == "" {
			apiKey = r.Header.Get("X-PIN")
		}
	}

	// Rewrite segment URLs to include API key and inject HLS tags
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

	if apiKey != "" {
		lines := strings.Split(playlistContent, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			// If line is a segment file (ends with .ts, .m4s, .vtt, or .webvtt)
			if strings.HasSuffix(trimmed, ".ts") || strings.HasSuffix(trimmed, ".m4s") ||
				strings.HasSuffix(trimmed, ".vtt") || strings.HasSuffix(trimmed, ".webvtt") {
				// Append API key as query parameter
				lines[i] = line + "?apiKey=" + apiKey
			} else if strings.Contains(line, "#EXT-X-MAP:URI=") {
				// Rewrite init segment URL in EXT-X-MAP tag
				// Format: #EXT-X-MAP:URI="init.mp4"
				lines[i] = strings.Replace(line, `"init.mp4"`, `"init.mp4?apiKey=`+apiKey+`"`, 1)
			} else if strings.Contains(line, "URI=") && (strings.Contains(line, ".vtt") || strings.Contains(line, ".webvtt")) {
				// Rewrite subtitle URLs in #EXT-X-MEDIA tags
				// Format: #EXT-X-MEDIA:TYPE=SUBTITLES,...,URI="subtitle.webvtt"
				lines[i] = strings.ReplaceAll(line, ".vtt\"", ".vtt?apiKey="+apiKey+"\"")
				lines[i] = strings.ReplaceAll(lines[i], ".webvtt\"", ".webvtt?apiKey="+apiKey+"\"")
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
	log.Printf("[hls] served playlist for session %s, VIDEO-RANGE=%s, API key=%v", sessionID, videoRange, apiKey != "")
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

	totalDuration := time.Since(requestStart)
	log.Printf("[hls] segment served: session=%s segment=%s size=%d bytes serve_time=%v total_time=%v",
		sessionID, segmentName, segmentSize, serveDuration, totalDuration)

	// NOTE: Segment cleanup disabled during playback.
	// EVENT playlists require all segments to remain available.
	// Cleanup happens when the session ends instead.
	// go m.deleteOldSegments(session, segmentName)
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

	// Determine which VTT file to serve based on requested track
	var vttPath string
	if requestedTrack == session.SubtitleTrackIndex {
		// Serving the original track that was extracted when session was created
		vttPath = filepath.Join(session.OutputDir, "subtitles.vtt")
	} else {
		// Serving a different track - use track-specific file
		vttPath = filepath.Join(session.OutputDir, fmt.Sprintf("subtitles_%d.vtt", requestedTrack))

		// If this track-specific file doesn't exist yet, extract it on-demand
		// Use session mutex to prevent concurrent extraction of the same track
		if _, err := os.Stat(vttPath); os.IsNotExist(err) {
			session.mu.Lock()
			// Double-check after acquiring lock (another request might have extracted it)
			if _, err := os.Stat(vttPath); os.IsNotExist(err) {
				log.Printf("[hls] extracting subtitle track %d on-demand for session %s", requestedTrack, sessionID)
				if err := m.extractSubtitleTrackToVTT(session, requestedTrack, vttPath); err != nil {
					session.mu.Unlock()
					log.Printf("[hls] failed to extract subtitle track %d: %v", requestedTrack, err)
					// Return empty VTT instead of error to avoid breaking playback
					w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Access-Control-Allow-Origin", "*")
					w.Write([]byte("WEBVTT\n\n"))
					return
				}
			}
			session.mu.Unlock()

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

	// Check for unsupported subtitle codecs (bitmap-based)
	if codec == "hdmv_pgs_subtitle" || codec == "pgs" || codec == "dvd_subtitle" {
		return fmt.Errorf("unsupported subtitle codec: %s (bitmap-based subtitles cannot be converted to VTT)", codec)
	}

	// Get the stream URL (convert virtual path to direct URL if needed)
	streamURL := session.Path
	if directProvider, ok := m.streamer.(streaming.DirectURLProvider); ok {
		if directURL, err := directProvider.GetDirectURL(ctx, session.Path); err == nil && directURL != "" {
			streamURL = directURL
			log.Printf("[hls] using direct URL for subtitle extraction")
		} else if err != nil {
			log.Printf("[hls] failed to get direct URL for subtitle extraction: %v", err)
		}
	}

	// Build ffmpeg command to extract subtitle track to VTT
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-i", streamURL,
		"-map", fmt.Sprintf("0:%d", actualStreamIndex),
		"-c", "webvtt",
		"-f", "webvtt",
		outputPath,
	}

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

// deleteOldSegments removes old segment files to save disk space, keeping only recent segments for buffering
func (m *HLSManager) deleteOldSegments(session *HLSSession, justServedSegment string) {
	// Extract segment number from filename (e.g., "segment123.ts" -> 123)
	var currentSegNum int
	if _, err := fmt.Sscanf(justServedSegment, "segment%d.", &currentSegNum); err != nil {
		return // Can't parse, skip cleanup
	}

	session.mu.RLock()
	outputDir := session.OutputDir
	hasDV := session.HasDV
	hasHDR := session.HasHDR
	sessionID := session.ID
	minSegmentRequested := session.MinSegmentRequested
	session.mu.RUnlock()

	// Only clean up segments if we know the player has started requesting segments
	if minSegmentRequested < 0 {
		log.Printf("[hls] session %s: skipping cleanup - no segments requested yet", sessionID)
		return
	}

	// Keep last 20 segments for player buffering, delete older ones
	// But only delete segments that the player has already progressed past
	cutoff := currentSegNum - 20
	if cutoff < 0 {
		return
	}

	segmentExt := ".ts"
	if hasDV || hasHDR {
		segmentExt = ".m4s"
	}

	// Delete segments older than cutoff, but never delete segments at or after minSegmentRequested
	// This ensures we keep all segments from the earliest point the player has accessed
	deletedCount := 0
	newMinAvailable := 0
	for i := 0; i <= cutoff; i++ {
		// Don't delete segments that are at or after the minimum segment the player has requested
		// This preserves the ability to seek back to the beginning
		if i >= minSegmentRequested {
			newMinAvailable = i
			break
		}
		oldSegment := filepath.Join(outputDir, fmt.Sprintf("segment%d%s", i, segmentExt))
		if err := os.Remove(oldSegment); err == nil {
			deletedCount++
			newMinAvailable = i + 1
		}
	}

	if deletedCount > 0 {
		// Update MinSegmentAvailable to track what's still on disk
		session.mu.Lock()
		if newMinAvailable > session.MinSegmentAvailable {
			session.MinSegmentAvailable = newMinAvailable
		}
		session.mu.Unlock()
		log.Printf("[hls] session %s: deleted %d old segments (keeping last 3, current=%d, minRequested=%d)",
			sessionID, deletedCount, currentSegNum, minSegmentRequested)
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

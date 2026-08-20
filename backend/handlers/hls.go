package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"novastream/services/castcaps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"novastream/internal/dnscache"
	"novastream/internal/netproxy"
	"novastream/internal/requestsecurity"
	"novastream/models"
	"novastream/services/debrid"
	"novastream/services/streaming"
	"novastream/utils"
)

// cdnClient is a shared HTTP client optimized for CDN connections.
// Uses connection pooling with longer idle timeout to reuse connections across seeks.
var cdnClient = newCDNClient()

func newCDNClient() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     120 * time.Second, // Keep connections alive longer
		DisableCompression:  true,              // Video is already compressed
	}
	dnscache.ConfigureTransport(transport, dnscache.DefaultTTL)

	return &http.Client{
		Timeout:   0, // No timeout - we handle this per-request
		Transport: transport,
	}
}

var hlsRedirectHTTPClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: cdnClient.Transport,
}

var errExternalStreamPlaceholder = errors.New("external stream resolved to unavailable-content placeholder")

func isHTTPDirectURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

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
	applyAuth func(*http.Request)
	server    *http.Server
	port      int
}

// newThrottlingProxy creates a new throttling proxy for the given URL.
// Returns the proxy and the local URL that FFmpeg should use.
func newThrottlingProxy(targetURL string, session *HLSSession, applyAuth func(*http.Request)) (*throttlingProxy, string, error) {
	proxy := &throttlingProxy{
		targetURL: targetURL,
		session:   session,
		applyAuth: applyAuth,
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
	log.Printf("[hls] session %s: started throttling proxy on port %d for upstream: %s", session.ID, proxy.port, requestsecurity.URLForLog(targetURL))

	// Pre-warm the CDN connection by making a HEAD request
	// This establishes TCP + TLS before FFmpeg needs it, reducing seek latency
	// Uses cdnClient for connection pooling - the connection will be reused by handleStream
	go func() {
		warmCtx, warmCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer warmCancel()
		req, err := http.NewRequestWithContext(warmCtx, http.MethodHead, targetURL, nil)
		if err != nil {
			return
		}
		proxy.applyRequestAuth(req)
		resp, err := cdnClient.Do(req)
		if err != nil {
			log.Printf("[hls] session %s: CDN warm-up failed (non-fatal): %v", session.ID, err)
			return
		}
		resp.Body.Close()
		log.Printf("[hls] session %s: CDN connection pre-warmed", session.ID)
	}()

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
	p.applyRequestAuth(req)

	// Forward Range header for seeking support
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
		log.Printf("[hls] session %s: proxy forwarding Range: %s", p.session.ID, rangeHeader)
	}

	// Make request to target using cdnClient for connection pooling
	resp, err := cdnClient.Do(req)
	if err != nil {
		log.Printf("[hls] session %s: proxy request failed: %v", p.session.ID, err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Log upstream response status for debugging
	if resp.StatusCode >= 400 {
		log.Printf("[hls] session %s: proxy upstream returned %d %s for target: %s (requestPath=%q fromClient=%q)", p.session.ID, resp.StatusCode, resp.Status, logWebDAVURL(encodedURL), req.URL.Path, r.URL.Path)
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

func (p *throttlingProxy) applyRequestAuth(req *http.Request) {
	if req == nil {
		return
	}
	if parsedURL, parseErr := url.Parse(p.targetURL); parseErr == nil && parsedURL.User != nil {
		password, _ := parsedURL.User.Password()
		req.SetBasicAuth(parsedURL.User.Username(), password)
		return
	}
	if p.applyAuth != nil {
		p.applyAuth(req)
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

	// Check actual segment files on disk (more accurate than SegmentsCreated counter)
	// Glob for both .m4s (fMP4) and .ts (MPEG-TS) segments
	segmentFiles, _ := filepath.Glob(filepath.Join(outputDir, "segment*.m4s"))
	if len(segmentFiles) == 0 {
		segmentFiles, _ = filepath.Glob(filepath.Join(outputDir, "segment*.ts"))
	}

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
		// Treat "no segments requested yet" as requesting segment 0
		// This ensures throttling applies uniformly to all sessions including prequeue
		effectiveMaxRequested := maxRequested
		if effectiveMaxRequested < 0 {
			effectiveMaxRequested = 0
		}

		bufferAhead := highestSegment - effectiveMaxRequested

		// Throttle when 15+ segments ahead (~30 seconds at 2s/segment)
		const throttleStartThreshold = 15
		if bufferAhead > throttleStartThreshold {
			excessSegments := bufferAhead - throttleStartThreshold
			delayMs := 500 + (excessSegments * 100) // 500ms base + 100ms per excess segment

			// Cap at 15 seconds to avoid HTTP connection timeouts from the source
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

	return t.r.Read(p)
}

// HLSSession represents an active HLS transcoding session
type HLSSession struct {
	ID                  string
	Path                string
	OriginalPath        string
	OutputDir           string
	CreatedAt           time.Time
	LastAccess          time.Time
	FFmpegCmd           *exec.Cmd
	Cancel              context.CancelFunc
	mu                  sync.RWMutex
	Completed           bool
	Stopped             bool // Explicit cleanup was requested; no recovery restart is allowed.
	HasDV               bool
	DVProfile           string
	DVDisabled          bool    // Set to true if DV metadata parsing fails and we fallback to non-DV
	HasHDR              bool    // HDR10 content (needs fMP4 segments for iOS compatibility)
	HDRMetadataDisabled bool    // Set to true if hevc_metadata filter fails (malformed SEI data)
	Duration            float64 // Total duration in seconds from ffprobe
	StartOffset         float64 // Requested start offset in seconds for session warm starts (never changes, for frontend)
	TranscodingOffset   float64 // Current transcoding position (updated on recovery restarts)
	ActualStartOffset   float64 // Actual start time from fMP4 tfdt box (keyframe-aligned, for subtitle sync)

	// Profile tracking
	ProfileID      string
	ProfileName    string
	ClientIP       string
	ClientID       string
	ViaShareLink   bool // session authenticated by a one-time share link
	MediaMetadata  StreamMediaMetadata
	SharePosition  float64
	ShareDuration  float64
	SharePercent   float64
	ShareUpdatedAt time.Time
	SharePaused    bool
	ShareBuffering bool

	// Live player state reported by HLS keepalives. This is the authoritative
	// source shared by Active Streams and playback notifications.
	PlaybackPosition  float64
	PlaybackUpdatedAt time.Time
	PlaybackPaused    bool
	PlaybackBuffering bool
	PlaybackEnded     bool

	// Track selection (-1 means use default)
	AudioTrackIndex    int // Selected audio stream index (ffprobe index), -1 = all/default
	SubtitleTrackIndex int // Selected subtitle track index, -1 = none

	// UsesSubtitleRendition is set when the transcode writes the selected subtitle as a
	// same-pass WebVTT sidecar in the same timeline as the web player video output.
	UsesSubtitleRendition bool

	// Performance tracking
	StreamStartTime    time.Time
	FirstSegmentTime   time.Time
	FirstSegmentSentAt time.Time // t4: first playback segment response began streaming
	BytesStreamed      int64
	SegmentsCreated    int
	FFmpegCPUStart     float64

	// Latency correlation back to the prequeue request that spawned this session.
	PrequeueID      string // prequeueId ("" when the session is ad-hoc / live)
	ServiceType     string // "usenet" | "debrid" when known
	ServiceProvider string // indexer / debrid provider when known
	// Rolling throughput sample state (bits/sec), updated atomically.
	throughputLastBytes  int64
	throughputLastNanos  int64
	throughputBps        int64
	FFmpegPID            int
	LastSegmentRequest   time.Time
	SegmentRequestCount  int
	IdleTimeoutTriggered bool

	// Segment tracking for cleanup and rate limiting
	MinSegmentRequested     int  // Minimum segment number that has been requested (-1 = none yet)
	MaxSegmentRequested     int  // Maximum segment number that has been requested (-1 = none yet)
	MinSegmentAvailable     int  // Minimum segment number still available on disk (for playlist filtering)
	LastPlaybackSegment     int  // Player's actual playback position from keepalive time reports (-1 = unknown)
	LastSegmentServed       int  // Last segment number successfully served to client (-1 = none yet)
	EarliestBufferedSegment int  // Earliest segment still in player's buffer from keepalive (-1 = unknown)
	Paused                  bool // True if FFmpeg is paused (SIGSTOP) waiting for player to catch up
	FinalSegmentCount       int  // Highest segment number created when transcoding completed (-1 = still running or unknown)
	// SegmentExt is the extension the transcode plan actually chose (".ts" or ".m4s"), recorded
	// so nothing has to reconstruct it from flags. Empty until the plan runs.
	SegmentExt string

	// Input error recovery (for usenet disconnections)
	InputErrorDetected bool // Set to true when FFmpeg input stream fails (usenet disconnect)
	RecoveryAttempts   int  // Number of times we've attempted to recover this session
	forceAAC           bool // Cached forceAAC setting for recovery restarts
	SeekInProgress     bool // Set to true during user-initiated seek to prevent recovery logic
	SeekGeneration     int  // Increments on each seek so regenerated segment URLs are cache-distinct
	SegmentStartNumber int  // Logical HLS sequence number used by the current FFmpeg run

	// Fatal error tracking (unplayable streams)
	FatalError string // Set when stream is determined to be unplayable (persistent bitstream errors)

	// Cached probe data from unified probe (avoids multiple ffprobe calls)
	ProbeData *UnifiedProbeResult

	// Per-track extraction tracking (prevents duplicate extractions without blocking session)
	subtitleExtractionMu      sync.Mutex                 // Protects subtitle extraction maps
	subtitleExtracting        map[int]bool               // Tracks which subtitle tracks are currently being extracted
	subtitleExtractCancelFunc map[int]context.CancelFunc // Cancels subtitle extractions when seeking
	subtitleExtractIDs        map[int]int64              // Identifies active extraction jobs for cleanup
	subtitleExtractSeq        int64
	subtitleExtractOffsets    map[int]float64 // Actual -ss offset used for each extracted VTT
	FatalErrorTime            time.Time
	BitstreamErrors           int // Count of bitstream filter errors (to detect persistent issues)

	// Live TV session fields
	IsLive       bool               // True for live TV streams (no duration, no seeking)
	LiveProvider string             // Live TV provider identifier ("m3u" or "xtream")
	LiveBucket   string             // Shared stream bucket identifier for limit accounting
	LiveTuning   LiveTuningSettings // FFmpeg tuning settings for live sessions

	// Closed caption support (live TV EIA-608)
	LiveCCExtractionEnabled bool         // Resolved playback.liveClosedCaptionExtraction setting
	HasClosedCaptions       bool         // True if EIA-608 CC detected in stream
	CCDetectionDone         bool         // True once CC detection has completed (regardless of result)
	ccExtractor             *ccExtractor // Running extractor for this session (nil until first captions request)

	// Prequeue tracking
	PrequeueType   string // "", "details" (details page), or "next_episode" (auto-play next)
	CastMode       bool   // True when the session is being prepared for Chromecast-style HLS playback
	DirectCastMode bool   // True when Cast should prefer direct/remux output over legacy compatibility transcode
	PlaybackTarget string // Optional client target hint, e.g. "web"

	// Compatibility Cast output ladder. Sessions start at 1080p; sustained slow
	// segment delivery to the receiver drops the transcode to 720p for the rest
	// of the session (castSlowServes counts the consecutive slow serves that
	// trigger it).
	CastMaxHeight  int
	castSlowServes int

	// The IP address of the Cast receiver, used to consult cached capabilities
	// when deciding whether to copy/remux or fall back to safe transcode limits.
	CastReceiverHost string

	// Cast capability observation. castVariants is the fingerprint of what this
	// session actually asks the receiver to decode, derived once the ffmpeg arg
	// plan is final. The rest is the receiver's own fetch timeline, which the
	// grader reads to turn assumptions into verdicts. castVariantsGraded caps
	// the whole session at one recorded verdict and frees the fetch set.
	castVariants        castVariantFingerprint
	castPlaylistFetched bool
	castFirstFetch      time.Time
	castLastFetch       time.Time
	castLastKeepalive   time.Time
	castFetchedSegments map[int]struct{}
	castVariantsGraded  bool

	// TonemappedToSDR is set when an HDR/DV source was tone mapped down to SDR
	// H.264 during transcode. The HLS playlist must then advertise SDR rather
	// than PQ video range.
	TonemappedToSDR           bool
	VideoEncoder              string
	ToneMapper                string
	HardwareEncode            bool
	HardwareFallbackAttempted bool
	forceSoftwareEncode       bool

	// YouTube HLS sessions are assembled from separate direct video/audio URLs.
	YouTubeVideoURL string
	YouTubeAudioURL string
	YouTubeProxyURL string
}

// LiveTuningSettings contains FFmpeg tuning parameters for live TV sessions.
type LiveTuningSettings struct {
	ProbeSizeMB        int
	AnalyzeDurationSec int
	LowLatency         bool
	RequestHeaders     map[string]string
	// ProxyURL, when set, routes the upstream live fetch through this proxy.
	// SOCKS5 proxies (which ffmpeg cannot use natively) are honored by fetching
	// the stream with the Go HTTP client and piping it into ffmpeg's stdin.
	ProxyURL string
}

// LiveProviderUsageEntry summarizes active usage for a single live provider.
type LiveProviderUsageEntry struct {
	Provider  string `json:"provider"`
	Current   int    `json:"current"`
	Max       int    `json:"max"`
	Available int    `json:"available"`
	AtLimit   bool   `json:"atLimit"`
}

// LiveUsageSummary summarizes current live stream usage and limits.
type LiveUsageSummary struct {
	Provider         string                   `json:"provider"`
	CurrentStreams   int                      `json:"currentStreams"`
	MaxStreams       int                      `json:"maxStreams"`
	AvailableStreams int                      `json:"availableStreams"`
	AtLimit          bool                     `json:"atLimit"`
	Providers        []LiveProviderUsageEntry `json:"providers"`
}

const (
	// How long to wait with no segment requests before killing FFmpeg
	// Set to 60s to be more forgiving during pause/screensaver scenarios
	// Frontend sends keepalives every 10s, so this allows for missed keepalives
	hlsIdleTimeout = 60 * time.Second

	// How long to wait before checking if FFmpeg is stuck (not generating segments)
	// If FFmpeg IS generating segments, we let it run - throttle limits buffering
	// and the 30-minute cleanup handles abandoned sessions
	hlsStartupTimeout = 30 * time.Second

	// YouTube trailer startup should wait for actual media readiness, not a fixed
	// wall-clock window. The timeout keeps expired CDN URLs from blocking fallback.
	youtubeStartupReadyTimeout      = 12 * time.Second
	youtubeStartupReadyPollInterval = 25 * time.Millisecond

	// Timeout for details page prequeue - user opened details but might not play
	// Kill after 2 minutes if they don't start watching
	hlsDetailsPrequeueTimeout = 2 * time.Minute

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
	// Keep enough already-served VOD segments for receivers that re-read startup
	// fragments after fetching the master playlist. Default Media Receiver can
	// request segment0 twice during cast startup; deleting it after segment5 turns
	// a healthy stream into "media failed to load".
	hlsSegmentCleanupRetainBehind = 30

	// Requests close to the current Cast transcode head can wait for sequential
	// generation. Larger jumps restart FFmpeg at the requested logical segment.
	castOnDemandLeadSegments = 3
)

type castSegmentRestart struct {
	SegmentStartNumber    int
	TranscodingOffset     float64
	OutputTimestampOffset float64
}

func castSegmentRestartPlan(session *HLSSession, requestedSegment, highestAvailable int) (castSegmentRestart, bool) {
	if session == nil || !session.usesStableCastTimeline() || requestedSegment < 0 {
		return castSegmentRestart{}, false
	}

	session.mu.RLock()
	startOffset := session.StartOffset
	duration := session.Duration
	currentStart := session.SegmentStartNumber
	lastServed := session.LastSegmentServed
	completed := session.Completed
	session.mu.RUnlock()

	// Let the current FFmpeg run satisfy nearby requests. This covers startup
	// requests and the receiver's normal small amount of read-ahead.
	if !completed &&
		requestedSegment >= currentStart &&
		requestedSegment <= currentStart+castOnDemandLeadSegments {
		return castSegmentRestart{}, false
	}
	if !completed &&
		requestedSegment > highestAvailable &&
		requestedSegment <= highestAvailable+castOnDemandLeadSegments {
		return castSegmentRestart{}, false
	}
	if !completed &&
		lastServed >= 0 &&
		requestedSegment > lastServed &&
		requestedSegment <= lastServed+castOnDemandLeadSegments {
		return castSegmentRestart{}, false
	}

	timelineOffset := float64(requestedSegment) * hlsSegmentDuration
	transcodingOffset := startOffset + timelineOffset
	if duration > 0 && transcodingOffset >= duration {
		return castSegmentRestart{}, false
	}

	return castSegmentRestart{
		SegmentStartNumber:    requestedSegment,
		TranscodingOffset:     transcodingOffset,
		OutputTimestampOffset: timelineOffset,
	}, true
}

func isTextSubtitleCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "subrip", "srt", "ass", "ssa",
		"webvtt", "vtt", "mov_text", "text",
		"ttml", "sami", "microdvd", "jacosub",
		"mpl2", "pjs", "realtext", "stl",
		"subviewer", "subviewer1", "vplayer":
		return true
	default:
		return false
	}
}

func isBrowserCopyCompatibleVideo(probe *UnifiedProbeResult) bool {
	if probe == nil {
		return false
	}

	codec := strings.ToLower(strings.TrimSpace(probe.VideoCodec))
	switch codec {
	case "h264", "avc", "avc1":
	default:
		return false
	}

	pixFmt := strings.ToLower(strings.TrimSpace(probe.VideoPixFmt))
	if pixFmt != "" && pixFmt != "yuv420p" {
		return false
	}

	profile := strings.ToLower(strings.TrimSpace(probe.VideoProfile))
	if strings.Contains(profile, "10") || strings.Contains(profile, "4:2:2") || strings.Contains(profile, "4:4:4") {
		return false
	}

	return true
}

func parseVideoFrameRate(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0/0" {
		return 0, false
	}
	numeratorText, denominatorText, hasDenominator := strings.Cut(value, "/")
	numerator, err := strconv.ParseFloat(numeratorText, 64)
	if err != nil || numerator <= 0 {
		return 0, false
	}
	if !hasDenominator {
		return numerator, true
	}
	denominator, err := strconv.ParseFloat(denominatorText, 64)
	if err != nil || denominator <= 0 {
		return 0, false
	}
	return numerator / denominator, true
}

const (
	legacyCastMaxWidth    = 1920
	legacyCastMaxHeight   = 1080
	legacyCastMaxLevel    = 41
	legacyCastHDMaxWidth  = 1280
	legacyCastHDMaxHeight = 720
	legacyCastMaxFPSHD    = 60
	legacyCastMaxFPSFull  = 30
)

// isLegacyCastCopyCompatibleVideo enforces the first/second-generation
// Chromecast H.264 envelope: Level 4.1, up to 720p60 or 1080p30.
func isLegacyCastCopyCompatibleVideo(probe *UnifiedProbeResult) bool {
	if !isBrowserCopyCompatibleVideo(probe) || probe.VideoWidth <= 0 || probe.VideoHeight <= 0 || probe.VideoLevel <= 0 {
		return false
	}
	if probe.VideoWidth > legacyCastMaxWidth || probe.VideoHeight > legacyCastMaxHeight || probe.VideoLevel > legacyCastMaxLevel {
		return false
	}
	frameRate, ok := parseVideoFrameRate(probe.AvgFrameRate)
	if !ok {
		return false
	}
	maxFrameRate := float64(legacyCastMaxFPSHD)
	if probe.VideoWidth > legacyCastHDMaxWidth || probe.VideoHeight > legacyCastHDMaxHeight {
		maxFrameRate = float64(legacyCastMaxFPSFull)
	}
	return frameRate <= maxFrameRate+0.01
}

func castVideoMustTranscode(probe *UnifiedProbeResult) bool {
	return !isLegacyCastCopyCompatibleVideo(probe)
}

// canAttemptDirectCastCopyVideo reports whether the source may be sent to a
// receiver without re-encoding the video. The bar is the Cast copy envelope
// (H.264, Level 4.1, 1080p30/720p60), not merely "a codec we understand":
// receivers accept a LOAD for 4K HEVC, fetch a few segments, and then stall
// forever with no error, and second-generation Chromecasts cannot decode HEVC
// at any resolution. Anything outside the envelope belongs on the
// compatibility transcode, which every receiver tested so far plays.
// caps widens this per receiver. Widening uses Allows, not Supports: a model
// prior that says "this panel decodes HEVC" is worth an attempt, and the cost
// of being wrong is one session that falls back. Everything whose failure mode
// is a silent stall with no way back - the fMP4 container choice, AC-3
// passthrough, multichannel AAC - still requires proven support.
func canAttemptDirectCastCopyVideo(probe *UnifiedProbeResult, caps *castcaps.Capabilities) bool {
	if probe == nil || strings.TrimSpace(probe.VideoCodec) == "" || IsIncompatibleVideoCodec(probe.VideoCodec) {
		return false
	}
	if !castVideoMustTranscode(probe) {
		return true
	}
	// Outside the legacy envelope. Widening requires an answer about the
	// specific thing being asked for, not merely about the container:
	// VariantFMP4 is an H.264 variant and says nothing about HEVC decode. An
	// unidentified receiver has neither, so it stays inside the envelope.
	if !caps.Allows(castcaps.VariantFMP4) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(probe.VideoCodec)) {
	case "hevc", "h265", "hev1", "hvc1":
		return caps.Allows(castcaps.VariantHEVCFMP4)
	default:
		// H.264 above the legacy box (4K, high level, 1080p60). No variant
		// measures resolution, so this stays on the safe transcode.
		return false
	}
}

func (session *HLSSession) disableUnsafeDirectCast(caps *castcaps.Capabilities) bool {
	if session == nil || !session.CastMode || !session.DirectCastMode || canAttemptDirectCastCopyVideo(session.ProbeData, caps) {
		return false
	}
	log.Printf("[hls] session %s: cast direct video is outside the receiver copy envelope; falling back to deterministic H.264 compatibility transcode", session.ID)
	session.DirectCastMode = false
	if isDirectCastTarget(session.PlaybackTarget, "") {
		session.PlaybackTarget = "web"
	}
	return true
}

func isDirectCastTarget(playbackTarget, castProfile string) bool {
	switch strings.ToLower(strings.TrimSpace(castProfile)) {
	case "direct":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(playbackTarget)) {
	case "cast-direct", "direct-cast":
		return true
	default:
		return false
	}
}

func (session *HLSSession) usesStableCastTimeline() bool {
	return session != nil && session.CastMode && !session.DirectCastMode
}

// legacyCastEncodeLimits returns the compatibility Cast encode box. Sessions
// default to 1080p30 (hardware encoders keep up with a 1080p H.264 re-encode);
// capHeight drops that to 720p after the receiver has proven the link cannot
// sustain 1080p. 720p and smaller sources are allowed 60fps either way, and
// the scale filter never upscales.
func legacyCastEncodeLimits(probe *UnifiedProbeResult, capHeight int) (maxWidth, maxHeight, maxFPS int) {
	maxWidth, maxHeight, maxFPS = legacyCastMaxWidth, legacyCastMaxHeight, legacyCastMaxFPSFull
	if capHeight > 0 && capHeight <= legacyCastHDMaxHeight {
		maxWidth, maxHeight = legacyCastHDMaxWidth, legacyCastHDMaxHeight
	}
	if probe == nil {
		return maxWidth, maxHeight, maxFPS
	}

	if probe.VideoWidth > 0 && probe.VideoWidth <= legacyCastHDMaxWidth &&
		probe.VideoHeight > 0 && probe.VideoHeight <= legacyCastHDMaxHeight {
		maxFPS = legacyCastMaxFPSHD
	}
	return maxWidth, maxHeight, maxFPS
}

// castCanUseMpegTS reports whether a Cast copy path can be muxed into MPEG-TS.
// H.264 is the only Cast video codec that belongs in TS; HEVC/DV need fMP4 and
// only play on receivers that support it anyway.
func castCanUseMpegTS(probe *UnifiedProbeResult) bool {
	if probe == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(probe.VideoCodec)) {
	case "h264", "avc", "avc1":
		return true
	default:
		return false
	}
}

func castPrefersMpegTS(session *HLSSession, caps *castcaps.Capabilities) bool {
	if !castCanUseMpegTS(session.ProbeData) {
		return false
	}
	// Most receivers require MPEG-TS. We only trust fMP4 if the capability probe
	// ran and explicitly confirmed it works on this device.
	return !caps.Supports(castcaps.VariantFMP4)
}

// selectedAudioStream returns the audio track this session will actually send.
// A negative index means "whatever ffmpeg maps first", which is stream 0.
func selectedAudioStream(audioStreams []audioStreamInfo, audioTrackIndex int) (audioStreamInfo, bool) {
	if len(audioStreams) == 0 {
		return audioStreamInfo{}, false
	}
	if audioTrackIndex >= 0 {
		for _, stream := range audioStreams {
			if stream.Index == audioTrackIndex {
				return stream, true
			}
		}
	}
	return audioStreams[0], true
}

// directCastAudioNeedsAAC reports whether a direct Cast session must re-encode
// its audio. Direct copy exists to skip the *video* re-encode; Dolby audio is
// the most common reason a receiver accepts the load and then never starts
// playing (TV-integrated receivers frequently cannot decode AC-3/E-AC-3), and
// re-encoding audio alone is cheap. Multichannel AAC keeps surround intact.
func directCastAudioNeedsAAC(audioStreams []audioStreamInfo, audioTrackIndex int) bool {
	selected, ok := selectedAudioStream(audioStreams, audioTrackIndex)
	if !ok {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(selected.Codec), "aac")
}

func isSelectedAudioDolby(audioStreams []audioStreamInfo, audioTrackIndex int) bool {
	selected, ok := selectedAudioStream(audioStreams, audioTrackIndex)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(selected.Codec)) {
	case "ac3", "ac-3", "eac3", "eac-3":
		return true
	default:
		return false
	}
}

const (
	// A 2s segment must arrive in well under 2s or the receiver's buffer drains.
	// Serves slower than this fraction of real time count as a slow link.
	castSlowServeFraction = 0.7
	// Consecutive slow serves before dropping the ladder. Single slow segments
	// are normal (CDN hiccups, seeks); a streak is a sustained-bandwidth verdict.
	castSlowServeStreak = 3
	// Ignore the first segments of a run: startup fetches overlap the sender's
	// warm-up and say nothing about steady-state bandwidth.
	castSlowServeWarmupSegments = 4
)

// castShouldDropToFallbackHeight reports whether a served segment pushes a
// compatibility Cast session over the slow-link threshold. slowStreak is the
// count including this serve.
func castShouldDropToFallbackHeight(slowStreak int, currentCapHeight int) bool {
	return slowStreak >= castSlowServeStreak && currentCapHeight != legacyCastHDMaxHeight
}

// castServeIsSlow reports whether one segment took so long to deliver that the
// receiver cannot stay ahead at the current bitrate.
func castServeIsSlow(serveDuration time.Duration, segmentBytes int64) bool {
	if segmentBytes <= 0 || serveDuration <= 0 {
		return false
	}
	return serveDuration.Seconds() > hlsSegmentDuration*castSlowServeFraction
}

type castAudioEnvelope struct {
	castSafe          bool
	allowMultichannel bool
}

// sessionCastAudioEnvelope picks channel policy for this session:
// web/browser always stereo (MSE), Cast stereo unless the receiver is known to
// accept multichannel AAC, native/default keeps 5.1.
func sessionCastAudioEnvelope(session *HLSSession, caps *castcaps.Capabilities) castAudioEnvelope {
	if session == nil {
		return castAudioEnvelope{allowMultichannel: true}
	}
	if isWebBrowserPlaybackTarget(session.PlaybackTarget) {
		return castAudioEnvelope{castSafe: false, allowMultichannel: false}
	}
	if session.CastMode {
		allow := caps != nil && caps.Supports(castcaps.VariantTSAACMultichannel)
		return castAudioEnvelope{castSafe: true, allowMultichannel: allow}
	}
	return castAudioEnvelope{castSafe: false, allowMultichannel: true}
}

// appendAACTranscodeArgs writes the AAC encode options including aresample for
// TrueHD/DTS timing and optional web mid-file PTS reset. env.castSafe enforces
// AAC-LC for Cast. Multichannel drops to stereo when env.allowMultichannel is
// false (web MSE, or Cast receivers that reject 5.1 AAC).
// ptsFilters enables first_pts/asetpts for mid-file web A/V without same-pass VTT.
func appendAACTranscodeArgs(args []string, streamSpecifier string, env castAudioEnvelope, playbackTarget string, transcodingOffset float64, ptsFilters bool) []string {
	option := func(name string) string { return name + streamSpecifier }
	channels := castAACChannels(env)
	layout := "5.1"
	if channels == 2 {
		layout = "stereo"
	}
	af := hlsAudioResampleFilter(playbackTarget, transcodingOffset, ptsFilters)
	args = append(args, "-af", af)
	args = append(args,
		option("-c:a"), "aac",
		option("-ac:a"), strconv.Itoa(channels),
		option("-ar:a"), "48000",
		option("-channel_layout:a"), layout,
		option("-b:a"), "192k",
	)
	if env.castSafe {
		args = append(args, option("-profile:a"), "aac_low")
	}
	return args
}

// castAACChannels mirrors the channel count appendAACTranscodeArgs writes, so
// the capability fingerprint describes the same audio the receiver is handed.
func castAACChannels(env castAudioEnvelope) int {
	if !env.allowMultichannel {
		return 2
	}
	return 6
}

func hlsWebVideoWillTranscode(playbackTarget string, probe *UnifiedProbeResult) bool {
	if probe != nil && IsIncompatibleVideoCodec(probe.VideoCodec) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(playbackTarget), "web") && !isBrowserCopyCompatibleVideo(probe)
}

// isWebBrowserPlaybackTarget is true for the HTML5/web player (and the "browser"
// alias). Chrome/Edge MSE often fails to decode multi-channel AAC in HLS, so web
// targets must downmix to stereo when we remux/transcode audio. Cast and native
// clients keep 5.1 for AVPlayer / Chromecast layout expectations.
func isWebBrowserPlaybackTarget(playbackTarget string) bool {
	switch strings.ToLower(strings.TrimSpace(playbackTarget)) {
	case "web", "browser":
		return true
	default:
		return false
	}
}

// webSeekTimelineResetNeeded is true when the browser path starts mid-file.
// Input -ss leaves audio/video PTS starting at different offsets (e.g. audio at
// 1.4s, video at 5.9s in segment0) while the HLS playlist advertises ~0.5s —
// Chrome/Edge MSE then buffers forever without canplay. Reset both timelines.
//
// Must be paired with accurate input seek (see useAccurateHLSInputSeek). With
// -noaccurate_seek, A/V can start on different content (video at the prior
// keyframe, audio nearer the request). Independent setpts/asetpts then zeros
// each clock and produces multi-second content desync equal to the GOP gap
// (observed ~10s on long-GOP HEVC BD rips).
func webSeekTimelineResetNeeded(playbackTarget string, transcodingOffset float64) bool {
	return isWebBrowserPlaybackTarget(playbackTarget) && transcodingOffset > 0
}

// useAccurateHLSInputSeek chooses accurate -ss (decode+discard to the exact
// timestamp) instead of -noaccurate_seek (byte-seek to prior keyframe).
// Accurate seek is required whenever we decode/transcode video or reset web
// mid-file PTS, so audio and video share the same content anchor.
func useAccurateHLSInputSeek(playbackTarget string, transcodingOffset float64, videoWillTranscode, subtitleRenditionWanted, stableCastMode bool) bool {
	if videoWillTranscode || subtitleRenditionWanted || stableCastMode {
		return true
	}
	return webSeekTimelineResetNeeded(playbackTarget, transcodingOffset)
}

// webSeekPTSFiltersNeeded is true when mid-file web A/V should independently
// zero clocks via setpts/asetpts. Disabled when a same-pass WebVTT subtitle is
// muxed in this FFmpeg run: those filters shift video/audio relative to the
// demuxer timeline that WebVTT cues still use, which shows up as ~0.5s late
// subtitles after resume/seek. Accurate -ss + start_at_zero + make_zero already
// share one timeline for video, audio, and the synced VTT.
func webSeekPTSFiltersNeeded(playbackTarget string, transcodingOffset float64, webSubtitleRendition bool) bool {
	return webSeekTimelineResetNeeded(playbackTarget, transcodingOffset) && !webSubtitleRendition
}

// hlsAudioResampleFilter returns the -af graph for AAC remux/transcode.
// ptsFilters controls the mid-file setpts/asetpts path (see webSeekPTSFiltersNeeded).
func hlsAudioResampleFilter(playbackTarget string, transcodingOffset float64, ptsFilters bool) string {
	if ptsFilters && webSeekTimelineResetNeeded(playbackTarget, transcodingOffset) {
		// first_pts=0 + asetpts forces a continuous audio clock from t=0 after -ss.
		return "aresample=async=1000:first_pts=0,asetpts=PTS-STARTPTS"
	}
	return "aresample=async=1000"
}

// withWebSeekVideoPTSReset prefixes setpts so the video clock restarts at 0 after -ss.
func withWebSeekVideoPTSReset(filter string) string {
	const reset = "setpts=PTS-STARTPTS"
	if strings.TrimSpace(filter) == "" {
		return reset
	}
	return reset + "," + filter
}

// hlsAACTranscodeArgs returns FFmpeg flags that encode the selected audio to AAC.
// mode "indexed0" writes the first output audio stream as AAC and copies others
// (-c:a:0 / -c:a:1); the default mode encodes a single mapped audio track.
// ptsFilters enables mid-file web asetpts (false when same-pass WebVTT is active).
func hlsAACTranscodeArgs(playbackTarget string, mode string, transcodingOffset float64, ptsFilters bool) []string {
	channels, layout := "6", "5.1"
	if isWebBrowserPlaybackTarget(playbackTarget) {
		channels, layout = "2", "stereo"
	}
	af := hlsAudioResampleFilter(playbackTarget, transcodingOffset, ptsFilters)
	if mode == "indexed0" {
		return []string{
			"-af", af,
			"-c:a:0", "aac", "-ac:a:0", channels, "-ar:a:0", "48000", "-channel_layout:a:0", layout, "-b:a:0", "192k",
			"-c:a:1", "copy",
		}
	}
	return []string{
		"-af", af,
		"-c:a", "aac", "-ac", channels, "-ar", "48000", "-channel_layout", layout, "-b:a", "192k",
	}
}

func hlsAACChannelLayoutLabel(playbackTarget string) string {
	if isWebBrowserPlaybackTarget(playbackTarget) {
		return "stereo"
	}
	return "5.1"
}

func selectedTextSubtitleStream(subtitleStreams []subtitleStreamInfo, trackIndex int) (subtitleStreamInfo, bool) {
	if trackIndex < 0 {
		return subtitleStreamInfo{}, false
	}
	for _, stream := range subtitleStreams {
		if stream.Index == trackIndex && isTextSubtitleCodec(stream.Codec) {
			return stream, true
		}
	}
	return subtitleStreamInfo{}, false
}

func shouldUseAccurateRequestedSeekForWebSubtitle(playbackTarget string, _ *UnifiedProbeResult, subtitleStreams []subtitleStreamInfo, trackIndex int) bool {
	if !strings.EqualFold(strings.TrimSpace(playbackTarget), "web") {
		return false
	}
	if _, ok := selectedTextSubtitleStream(subtitleStreams, trackIndex); !ok {
		return false
	}
	return true
}

func shouldForceWebSubtitleVideoTranscode(playbackTarget string, subtitleStreams []subtitleStreamInfo, trackIndex int, transcodingOffset float64) bool {
	if transcodingOffset <= 0 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(playbackTarget), "web") {
		return false
	}
	_, ok := selectedTextSubtitleStream(subtitleStreams, trackIndex)
	return ok
}

func shouldPreferRequestedTranscodingOffset(playbackTarget string, probe *UnifiedProbeResult, subtitleStreams []subtitleStreamInfo, trackIndex int) bool {
	if shouldUseAccurateRequestedSeekForWebSubtitle(playbackTarget, probe, subtitleStreams, trackIndex) {
		return true
	}
	return probe == nil && strings.EqualFold(strings.TrimSpace(playbackTarget), "web") && trackIndex >= 0
}

func initialTranscodingOffset(startOffset, probedOffset float64, castMode bool) float64 {
	if castMode {
		// Stable Cast segment numbers are calculated from StartOffset. The Cast
		// path accurately transcodes rather than copying keyframe pre-roll, so
		// segment zero must begin at that exact same anchor.
		return startOffset
	}
	if probedOffset > 0 {
		return probedOffset
	}
	return startOffset
}

func sanitizeHLSLanguage(language string) string {
	trimmed := strings.TrimSpace(language)
	if trimmed == "" {
		return "und"
	}
	return trimmed
}

func sanitizeHLSName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "Subtitles"
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(trimmed)
}

func (m *HLSManager) getSessionSubtitleStreams(session *HLSSession) []subtitleStreamInfo {
	if session == nil || session.ProbeData == nil || len(session.ProbeData.SubtitleStreams) == 0 {
		return nil
	}

	streams := make([]subtitleStreamInfo, 0, len(session.ProbeData.SubtitleStreams))
	for _, stream := range session.ProbeData.SubtitleStreams {
		if !isTextSubtitleCodec(stream.Codec) {
			continue
		}
		streams = append(streams, stream)
	}

	return streams
}

func (m *HLSManager) shouldServeMasterPlaylist(session *HLSSession) bool {
	if session == nil || session.IsLive || !session.CastMode {
		return false
	}
	return len(m.getSessionSubtitleStreams(session)) > 0
}

func (m *HLSManager) buildSessionPlaylistURL(session *HLSSession) string {
	if m.shouldServeMasterPlaylist(session) {
		return fmt.Sprintf("/video/hls/%s/master.m3u8", session.ID)
	}
	return fmt.Sprintf("/video/hls/%s/stream.m3u8", session.ID)
}

func appendAuthTokenToPlaylist(content string, authToken string) string {
	if authToken == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isHLSMediaURI(trimmed) {
			lines[i] = line + tokenSeparator(line) + "token=" + authToken
		} else if strings.Contains(line, "#EXT-X-MAP:URI=") {
			lines[i] = strings.Replace(line, `"init.mp4"`, `"init.mp4?token=`+authToken+`"`, 1)
		} else if strings.Contains(line, "URI=") &&
			(strings.Contains(line, ".vtt") || strings.Contains(line, ".webvtt") || strings.Contains(line, ".m3u8")) {
			lines[i] = appendAuthTokenToQuotedURIs(line, authToken)
		}
	}
	return strings.Join(lines, "\n")
}

func appendSegmentCacheBusterToPlaylist(content string, generation int) string {
	if generation <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	cacheParam := fmt.Sprintf("sg=%d", generation)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isHLSMediaURI(trimmed) && isHLSSegmentURI(trimmed) {
			lines[i] = line + tokenSeparator(line) + cacheParam
		} else if strings.Contains(line, "#EXT-X-MAP:URI=") {
			lines[i] = appendCacheBusterToInitMap(line, cacheParam)
		}
	}
	return strings.Join(lines, "\n")
}

func isHLSSegmentURI(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, ".ts") || strings.Contains(lower, ".m4s") || strings.Contains(lower, ".mp4")
}

func appendCacheBusterToInitMap(line, cacheParam string) string {
	if strings.Contains(line, `"init.mp4?`) {
		return strings.Replace(line, `"init.mp4?`, `"init.mp4?`+cacheParam+`&`, 1)
	}
	return strings.Replace(line, `"init.mp4"`, `"init.mp4?`+cacheParam+`"`, 1)
}

func isHLSMediaURI(line string) bool {
	if line == "" || strings.HasPrefix(line, "#") {
		return false
	}
	path := line
	if idx := strings.IndexAny(path, "?#"); idx >= 0 {
		path = path[:idx]
	}
	return strings.HasSuffix(path, ".ts") ||
		strings.HasSuffix(path, ".m4s") ||
		strings.HasSuffix(path, ".vtt") ||
		strings.HasSuffix(path, ".webvtt") ||
		strings.HasSuffix(path, ".m3u8")
}

func tokenSeparator(uri string) string {
	if strings.Contains(uri, "?") {
		return "&"
	}
	return "?"
}

func appendAuthTokenToQuotedURIs(line string, authToken string) string {
	for _, ext := range []string{".vtt", ".webvtt", ".m3u8"} {
		searchStart := 0
		for {
			if searchStart >= len(line) {
				break
			}
			idx := strings.Index(line[searchStart:], ext)
			if idx < 0 {
				break
			}
			idx += searchStart
			uriEnd := strings.Index(line[idx:], `"`)
			if uriEnd < 0 {
				break
			}
			uriEnd += idx
			if strings.Contains(line[idx:uriEnd], "token=") {
				searchStart = uriEnd + 1
				continue
			}
			separator := tokenSeparator(line[idx:uriEnd])
			line = line[:uriEnd] + separator + "token=" + authToken + line[uriEnd:]
			searchStart = uriEnd + len(separator) + len("token=") + len(authToken) + 1
		}
	}
	return line
}

func waitForSubtitleFile(vttPath string, requireCue bool, maxWait time.Duration) (os.FileInfo, []byte, error) {
	deadline := time.Now().Add(maxWait)

	for {
		stat, err := os.Stat(vttPath)
		if err == nil {
			content, readErr := os.ReadFile(vttPath)
			if readErr == nil {
				if !requireCue || strings.Contains(string(content), "-->") {
					return stat, content, nil
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, err
		}

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil, nil, os.ErrNotExist
}

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
	configManager      ConfigProvider
	playbackObserver   PlaybackActivityObserver
	// Click→first-frame latency instrumentation (optional; nil in reduced setups)
	latencyTracker *PlaybackLatencyTracker
	// Global probe cache - shared between prequeue (ProbeVideoFull) and HLS (probeAllMetadata)
	probeCache   map[string]*cachedProbeEntry
	probeCacheMu sync.RWMutex

	// Hardware-accelerated encode capabilities. The cache is keyed by the
	// configured preference so WebUI changes apply to the next web session.
	hwAccelMu         sync.Mutex
	hwAccel           HWAccelCaps
	hwAccelPref       string
	hwAccelReady      bool
	hwAccelRetryAfter time.Time

	castCapsMu sync.RWMutex
	castCaps   *castcaps.Store
}

// AddPlaybackActivityObserver registers one more consumer of this manager's
// playback activity. See playbackObserverFanout for why there is more than one.
func (m *HLSManager) AddPlaybackActivityObserver(observer PlaybackActivityObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.playbackObserver = addPlaybackObserver(m.playbackObserver, observer)
	m.mu.Unlock()
}

// SetPlaybackLatencyTracker wires the click→first-frame sample sink.
// Safe to call once at startup; nil disables instrumentation.
func (m *HLSManager) SetPlaybackLatencyTracker(t *PlaybackLatencyTracker) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.latencyTracker = t
	m.mu.Unlock()
}

// SetSessionPrequeue links an HLS session back to the prequeue request that
// produced it so first-frame latency can be measured end to end.
func (m *HLSManager) SetSessionPrequeue(sessionID, prequeueID, serviceType, serviceProvider string) {
	if m == nil || prequeueID == "" {
		return
	}
	session, ok := m.GetSession(sessionID)
	if !ok {
		return
	}
	session.mu.Lock()
	session.PrequeueID = prequeueID
	if serviceType != "" {
		session.ServiceType = serviceType
	}
	if serviceProvider != "" {
		session.ServiceProvider = serviceProvider
	}
	session.mu.Unlock()
}

// ClearPlaybackCaches resets everything HLSManager-side that would make a
// repeat transcode warm: the probe cache, hardware-accel detection, and all
// live sessions (killing FFmpeg). Returns the number of sessions torn down.
func (m *HLSManager) ClearPlaybackCaches() (int, error) {
	if m == nil {
		return 0, nil
	}

	m.probeCacheMu.Lock()
	nProbe := len(m.probeCache)
	m.probeCache = make(map[string]*cachedProbeEntry)
	m.probeCacheMu.Unlock()

	m.hwAccelMu.Lock()
	m.hwAccel = HWAccelCaps{}
	m.hwAccelPref = ""
	m.hwAccelReady = false
	m.hwAccelRetryAfter = time.Time{}
	m.hwAccelMu.Unlock()

	m.mu.RLock()
	sessions := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		sessions = append(sessions, id)
	}
	m.mu.RUnlock()
	nSessions := len(sessions)

	log.Printf("[hls] ClearPlaybackCaches: probeCache=%d hwaccelReset sessions=%d killed", nProbe, nSessions)
	for _, id := range sessions {
		// CleanupSession is heavy (kills FFmpeg, removes dirs); run outside the manager lock.
		m.CleanupSession(id)
	}
	return nSessions, nil
}

// UpdateSharePlaybackProgress records live dashboard-only progress for
// share-link HLS sessions without persisting anything to watch history.
func (m *HLSManager) UpdateSharePlaybackProgress(sessionID, profileID, profileName string, update models.PlaybackProgressUpdate) int {
	if m == nil {
		return 0
	}

	percent := update.PercentWatched
	if percent <= 0 && update.Duration > 0 {
		percent = (update.Position / update.Duration) * 100
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	when := update.Timestamp
	if when.IsZero() {
		when = time.Now()
	}

	target := streamMediaIdentity(StreamMediaMetadata{
		MediaType:     update.MediaType,
		ItemID:        update.ItemID,
		SeasonNumber:  update.SeasonNumber,
		EpisodeNumber: update.EpisodeNumber,
		SeriesID:      update.SeriesID,
		ExternalIDs:   update.ExternalIDs,
	})
	if target.MediaType == "" || target.ID == "" {
		return 0
	}
	targetKeys := make(map[string]struct{}, len(target.CandidateKeys)+1)
	targetKeys[target.Key] = struct{}{}
	for _, key := range target.CandidateKeys {
		targetKeys[key] = struct{}{}
	}

	m.mu.RLock()
	candidates := make([]*HLSSession, 0, len(m.sessions))
	if sessionID != "" {
		if session := m.sessions[sessionID]; session != nil {
			candidates = append(candidates, session)
		}
	} else {
		for _, session := range m.sessions {
			candidates = append(candidates, session)
		}
	}
	m.mu.RUnlock()

	matches := 0
	for _, session := range candidates {
		session.mu.Lock()
		if !session.ViaShareLink {
			session.mu.Unlock()
			continue
		}
		if profileID != "" && session.ProfileID != "" && session.ProfileID != profileID {
			session.mu.Unlock()
			continue
		}
		if profileName != "" && session.ProfileName != "" && !strings.EqualFold(session.ProfileName, profileName) {
			session.mu.Unlock()
			continue
		}
		if !streamMetadataMatchesIdentity(session.MediaMetadata, target, targetKeys) {
			session.mu.Unlock()
			continue
		}
		session.SharePosition = update.Position
		session.ShareDuration = update.Duration
		session.SharePercent = percent
		session.ShareUpdatedAt = when
		session.SharePaused = update.IsPaused
		session.ShareBuffering = update.IsBuffering
		session.mu.Unlock()
		matches++
	}
	return matches
}

// inputLooksLikeHLS reports whether a live source URL is an HLS playlist. The
// `-allowed_extensions`, `-allowed_segment_extensions` and `-extension_picky`
// options are private to FFmpeg's HLS demuxer; passing them for a direct
// MPEG-TS (.ts) input — which selects the mpegts demuxer — aborts the whole
// command with "Option <name> not found".
func inputLooksLikeHLS(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	if strings.HasSuffix(lower, ".m3u8") || strings.Contains(lower, ".m3u8") {
		return true
	}
	// Some Stremio live providers resolve stream resources to signed playlist
	// endpoints without a .m3u8 suffix, e.g. /playlist/<token>. FFmpeg still
	// selects the HLS demuxer from the response body, so it needs the HLS
	// demuxer options that permit extensionless or disguised segment URLs.
	return strings.Contains(lower, "/playlist/")
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

// SetConfigManager allows HLS proxy fetches to authenticate against external Usenet WebDAV engines.
func (m *HLSManager) SetConfigManager(cfgManager ConfigProvider) {
	if m == nil {
		return
	}
	m.configManager = cfgManager
}

func (m *HLSManager) applyExternalUsenetWebDAVAuth(req *http.Request) {
	if m == nil || m.configManager == nil || req == nil || req.URL == nil {
		return
	}
	settings, err := m.configManager.Load()
	if err != nil {
		return
	}
	for _, engine := range settings.UsenetEngines {
		if !engine.Enabled || strings.TrimSpace(engine.WebDAVBaseURL) == "" {
			continue
		}
		if !externalURLMatchesBase(req.URL, engine.WebDAVBaseURL) {
			continue
		}
		if engine.WebDAVUsername != "" || engine.WebDAVPassword != "" {
			req.SetBasicAuth(engine.WebDAVUsername, engine.WebDAVPassword)
		}
		return
	}
}

func (m *HLSManager) externalFFmpegHeaders(rawURL string) []string {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsedURL == nil {
		return nil
	}

	req := &http.Request{URL: parsedURL, Header: make(http.Header)}
	if parsedURL.User != nil {
		password, _ := parsedURL.User.Password()
		req.SetBasicAuth(parsedURL.User.Username(), password)
	} else {
		m.applyExternalUsenetWebDAVAuth(req)
	}

	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return nil
	}
	return []string{"-headers", "Authorization: " + authHeader + "\r\n"}
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

	log.Printf("[hls] configured local WebDAV direct access: base=%q prefix=%q", requestsecurity.URLForLog(normalizedBase), normalizedPrefix)
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
	videoTracef("[hls] resolving external URL")

	// Create a request-scoped client that follows redirects while sharing the
	// CDN transport and DNS cache.
	client := *hlsRedirectHTTPClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		videoTracef("[hls] following external URL redirect")
		return nil
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
	m.applyExternalUsenetWebDAVAuth(req)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HEAD request failed: %w", err)
	}
	resp.Body.Close()
	resolvedURL := resp.Request.URL.String()
	if debrid.IsKnownPlaceholderURL(resolvedURL) {
		return "", fmt.Errorf("%w: %s", errExternalStreamPlaceholder, requestsecurity.URLForLog(resolvedURL))
	}

	// If HEAD succeeded, check for redirects
	if resp.StatusCode < 400 {
		if resolvedURL != externalURL {
			videoTracef("[hls] resolved external URL via HEAD")
			return resolvedURL, nil
		}
		videoTracef("[hls] external URL has no redirects (HEAD)")
		return externalURL, nil
	}

	// HEAD failed (e.g., 405 Method Not Allowed), try GET with Range header
	// This minimizes data transfer while still following redirects
	log.Printf("[hls] HEAD returned %d, trying GET with Range header", resp.StatusCode)

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, encodedURL, nil)
	if err != nil {
		return "", fmt.Errorf("create GET request: %w", err)
	}
	req.Header.Set("User-Agent", "VLC/3.0.18 LibVLC/3.0.18")
	req.Header.Set("Range", "bytes=0-0") // Request only 1 byte
	m.applyExternalUsenetWebDAVAuth(req)

	resp, err = client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}
	resp.Body.Close() // Close immediately, we only needed the redirect resolution
	resolvedURL = resp.Request.URL.String()
	if debrid.IsKnownPlaceholderURL(resolvedURL) {
		return "", fmt.Errorf("%w: %s", errExternalStreamPlaceholder, requestsecurity.URLForLog(resolvedURL))
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("GET request returned status %d", resp.StatusCode)
	}

	// If we followed redirects, use the final URL
	if resolvedURL != externalURL {
		videoTracef("[hls] resolved external URL via GET")
		return resolvedURL, nil
	}

	// No redirects, use the original URL
	videoTracef("[hls] external URL has no redirects (GET)")
	return externalURL, nil
}

// getDirectURL attempts to get a direct HTTP URL for the session source
// Returns the URL and true if available, empty string and false otherwise
func (m *HLSManager) getDirectURL(ctx context.Context, session *HLSSession) (string, bool) {
	// If the path is already an external URL, return it directly
	// Note: The URL should already be resolved in CreateSession, so we just return it
	if strings.HasPrefix(session.Path, "http://") || strings.HasPrefix(session.Path, "https://") {
		videoTracef("[hls] path is already an external URL")
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
		if errors.Is(err, streaming.ErrStaleTorrent) {
			log.Printf("[hls] stale torrent for %s — cannot recover", requestsecurity.URLForLog(session.Path))
			return "", false
		}
		log.Printf("[hls] failed to get direct URL for %s: %v", requestsecurity.URLForLog(session.Path), err)
		if directURL, ok := m.buildLocalWebDAVURL(session); ok {
			return directURL, true
		}
		return "", false
	}

	log.Printf("[hls] successfully got direct URL for %s: %s", requestsecurity.URLForLog(session.Path), requestsecurity.URLForLog(url))
	return url, true
}

func (m *HLSManager) buildLocalWebDAVURL(session *HLSSession) (string, bool) {
	if session == nil {
		return "", false
	}
	if isRemoteMediaProviderPath(session.Path) || isRemoteMediaProviderPath(session.OriginalPath) {
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
		log.Printf("[hls] buildLocalWebDAVURL: original %q does not start with prefix %q (base=%s) — dropping to other paths",
			original, prefix, requestsecurity.URLForLog(base))
		return "", false
	}

	full := strings.TrimRight(base, "/") + original
	log.Printf("[hls] buildLocalWebDAVURL: resolved to %s (original=%q)", logWebDAVURL(full), original)
	return full, true
}

// logWebDAVURL logs a WebDAV URL with any embedded credentials masked but the
// path retained, so latency/diagnostic logs show exactly which file is targeted
// without leaking the username/password.
func logWebDAVURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}

// buildLocalWebDAVURLFromPath builds a WebDAV URL from just a path (no session required).
// This is used for probing usenet content where we don't have a session yet.
func (m *HLSManager) buildLocalWebDAVURLFromPath(path string) (string, bool) {
	if isRemoteMediaProviderPath(path) {
		return "", false
	}

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
	log.Printf("[hls] built local WebDAV URL from path: %s", requestsecurity.URLForLog(full))
	return full, true
}

// CreateSession starts a new HLS transcoding session
func (m *HLSManager) CreateSession(ctx context.Context, path string, originalPath string, hasDV bool, dvProfile string, hasHDR bool, forceAAC bool, startOffset float64, transcodingOffset float64, audioTrackIndex int, subtitleTrackIndex int, profileID string, profileName string, clientIP string, castMode bool, prequeueType string, playbackTarget string, durationHint float64, castReceiverHost string) (*HLSSession, error) {
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
			if errors.Is(err, errExternalStreamPlaceholder) {
				cancel()
				_ = os.RemoveAll(outputDir)
				return nil, err
			}
			log.Printf("[hls] session %s: failed to resolve external URL, using original: %v", sessionID, err)
			// Continue with original URL - ffmpeg/ffprobe can follow redirects
		} else if resolvedURL != path {
			log.Printf("[hls] session %s: using resolved URL for probing and FFmpeg: %s", sessionID, requestsecurity.URLForLog(resolvedURL))
			path = resolvedURL
		}
	}

	// Unified probe: extract duration, color metadata, audio/subtitle streams in a single ffprobe call
	// This replaces 4 separate ffprobe invocations with 1, significantly reducing playback start time
	var duration float64
	metadataDuration := durationHint
	if durationProvider, ok := m.streamer.(streaming.DurationProvider); ok {
		if providerDuration, durationErr := durationProvider.GetDuration(ctx, path); durationErr == nil && providerDuration > 0 {
			metadataDuration = providerDuration
		}
	}
	var probeData *UnifiedProbeResult
	if m.ffprobePath != "" && (m.streamer != nil || isExternalURL) {
		log.Printf("[hls] running unified probe for session %s path=%q", sessionID, path)
		if pd, probeErr := m.probeAllMetadata(ctx, path); probeErr != nil && errors.Is(probeErr, streaming.ErrStaleTorrent) {
			cancel()
			os.RemoveAll(outputDir)
			return nil, probeErr
		} else if probeErr == nil && pd != nil {
			probeData = pd
			duration = pd.Duration
			log.Printf("[hls] unified probe for session %s: duration=%.2fs startTime=%.3fs colorTransfer=%q audioStreams=%d",
				sessionID, duration, pd.StartTime, pd.ColorTransfer, len(pd.AudioStreams))

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
		} else if probeErr != nil {
			log.Printf("[hls] failed unified probe for session %s: %v", sessionID, probeErr)
		}
	}
	duration = resolvedSessionDuration(duration, metadataDuration)

	if math.IsNaN(startOffset) || math.IsInf(startOffset, 0) || startOffset < 0 {
		startOffset = 0
	}
	if duration > 0 && startOffset >= duration {
		startOffset = math.Max(duration-4, 0)
	}

	normalizedPlaybackTarget := strings.ToLower(strings.TrimSpace(playbackTarget))
	requestedDirectCastMode := castMode && isDirectCastTarget(normalizedPlaybackTarget, "")
	// Refused for now, whatever the receiver can decode: copying the video is unsafe while
	// playlists assume a fixed segment duration.
	//
	// A copied stream can only be cut at the source's own keyframes, so its segments run whatever
	// length the GOP dictates — 10.4s, 1.6s, 1.1s, 9.8s measured on an ordinary x264 rip. This
	// package derives the segment count as duration/hlsSegmentDuration and synthesises missing
	// entries at that flat duration, so against variable segments the count is roughly double
	// reality and the durations are invented: a receiver plays the real segments, reaches the
	// fiction and stalls, while the server serves instantly and looks healthy.
	//
	// Re-encoding forces a keyframe every hlsSegmentDuration, which is what makes that model
	// true, so the compatibility transcode remains correct. Lifting this means teaching the
	// playlist layer to carry real per-segment durations; the copy envelope below stays intact
	// and tested for that day. Downgrading here rather than deeper keeps DirectCastMode false,
	// so the session is built as compatibility throughout instead of half-converted.
	const directCastPlaylistsSupportVariableSegments = false
	directCastMode := directCastPlaylistsSupportVariableSegments &&
		requestedDirectCastMode &&
		canAttemptDirectCastCopyVideo(probeData, m.lookupCastCapabilities(castReceiverHost))
	if requestedDirectCastMode && !directCastMode {
		// Say so at creation. The startTranscoding-side guard cannot log this
		// case: the session is already built as compatibility by then, so
		// without this line a downgraded direct request is indistinguishable
		// from one the client sent as compatibility.
		codec, width, height := "", 0, 0
		if probeData != nil {
			codec, width, height = probeData.VideoCodec, probeData.VideoWidth, probeData.VideoHeight
		}
		log.Printf("[hls] session %s: direct Cast requested but %s %dx%d is outside the receiver copy envelope (receiver=%q); using compatibility transcode",
			sessionID, codec, width, height, castReceiverHost)
		normalizedPlaybackTarget = "web"
	}
	stableCastMode := castMode && !directCastMode
	subtitleStreamsForSeek := []subtitleStreamInfo(nil)
	if probeData != nil {
		subtitleStreamsForSeek = probeData.SubtitleStreams
	}

	now := time.Now()
	// Use provided transcodingOffset if valid, otherwise default to startOffset
	// transcodingOffset may differ from startOffset when probed keyframe position is used
	actualTranscodingOffset := initialTranscodingOffset(startOffset, transcodingOffset, stableCastMode)
	if startOffset > 0 && shouldPreferRequestedTranscodingOffset(normalizedPlaybackTarget, probeData, subtitleStreamsForSeek, subtitleTrackIndex) {
		actualTranscodingOffset = startOffset
		log.Printf("[hls] session %s: using requested start %.3fs instead of probed keyframe %.3fs for accurate web subtitle seek",
			sessionID, startOffset, transcodingOffset)
	}

	session := &HLSSession{
		ID:                      sessionID,
		Path:                    path,
		OriginalPath:            originalPath,
		OutputDir:               outputDir,
		CreatedAt:               now,
		LastAccess:              now,
		Cancel:                  cancel,
		HasDV:                   hasDV,
		DVProfile:               dvProfile,
		HasHDR:                  hasHDR,
		Duration:                duration,
		StartOffset:             startOffset,
		TranscodingOffset:       actualTranscodingOffset, // May differ from StartOffset if keyframe-aligned
		ActualStartOffset:       actualTranscodingOffset, // For subtitle sync
		ProfileID:               profileID,
		ProfileName:             profileName,
		ClientIP:                clientIP,
		AudioTrackIndex:         audioTrackIndex,
		SubtitleTrackIndex:      subtitleTrackIndex,
		StreamStartTime:         now,
		LastSegmentRequest:      now,       // Initialize to now to avoid immediate timeout
		MinSegmentRequested:     -1,        // Initialize to -1 (no segments requested yet)
		MaxSegmentRequested:     -1,        // Initialize to -1 (no segments requested yet)
		LastPlaybackSegment:     -1,        // Initialize to -1 (no keepalive time reported yet)
		LastSegmentServed:       -1,        // Initialize to -1 (no segments served yet)
		EarliestBufferedSegment: -1,        // Initialize to -1 (no buffer info reported yet)
		FinalSegmentCount:       -1,        // Initialize to -1 (transcoding still running)
		ProbeData:               probeData, // Cache unified probe results for startTranscoding
		subtitleExtractOffsets:  make(map[int]float64),
		CastMode:                castMode,
		DirectCastMode:          directCastMode,
		PrequeueType:            prequeueType, // "", "details", or "next_episode"
		PlaybackTarget:          normalizedPlaybackTarget,
		CastReceiverHost:        castReceiverHost,
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

	// Web warm starts (resume) previously returned before any segment existed. That races the
	// browser attaching hls.js to a not-yet-stable first segment and has been observed to
	// buffer forever without canplay. In-session seek waits for the playlist — do the same
	// for web create-with-offset. Cold starts (offset 0) and native clients still return
	// immediately so startup latency stays low.
	if actualTranscodingOffset > 0 && isWebBrowserPlaybackTarget(normalizedPlaybackTarget) {
		if waited, size := m.waitForPlaylistReady(session, 10*time.Second); waited {
			log.Printf("[hls] session %s: web warm-start playlist ready (%d bytes)", sessionID, size)
		} else {
			log.Printf("[hls] session %s: web warm-start timed out waiting for playlist; returning anyway", sessionID)
		}
	} else {
		// Return immediately - modern HLS players (AVPlayer, ExoPlayer) handle empty playlists
		// by polling until segments are available. This eliminates the 5-6 second blocking wait.
		log.Printf("[hls] session %s: returning immediately, FFmpeg transcoding in background", sessionID)
	}

	// Note: FFmpeg always numbers segments from 0 for the remaining timeline after -ss.
	// session.StartOffset is the absolute timeline position for UI/progress only; the
	// player must keep media currentTime near 0 (hlsPlaybackOffset holds the absolute base).
	return session, nil
}

// waitForPlaylistReady blocks until stream.m3u8 exists with media content, or maxWait elapses.
// Returns whether the playlist was ready and its size in bytes.
func (m *HLSManager) waitForPlaylistReady(session *HLSSession, maxWait time.Duration) (bool, int) {
	if session == nil || maxWait <= 0 {
		return false, 0
	}
	session.mu.RLock()
	outputDir := session.OutputDir
	session.mu.RUnlock()
	playlistPath := filepath.Join(outputDir, "stream.m3u8")
	pollInterval := 25 * time.Millisecond
	waitStart := time.Now()
	for {
		if data, err := os.ReadFile(playlistPath); err == nil && playlistHasMediaSegment(data) {
			return true, len(data)
		}
		if time.Since(waitStart) > maxWait {
			return false, 0
		}
		time.Sleep(pollInterval)
	}
}

func resolvedSessionDuration(probedDuration, durationHint float64) float64 {
	if probedDuration > 0 && !math.IsNaN(probedDuration) && !math.IsInf(probedDuration, 0) {
		return probedDuration
	}
	const maxDurationHint = 7 * 24 * 60 * 60
	if durationHint <= 0 || durationHint > maxDurationHint || math.IsNaN(durationHint) || math.IsInf(durationHint, 0) {
		return 0
	}
	return durationHint
}

// CreateYouTubeSession starts an HLS session from separate YouTube video/audio URLs.
func (m *HLSManager) CreateYouTubeSession(ctx context.Context, videoURL, audioURL, originalURL, proxyURL, profileID, profileName, clientIP string) (*HLSSession, error) {
	sessionID := generateSessionID()
	outputDir := filepath.Join(m.baseDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	session := &HLSSession{
		ID:                      sessionID,
		Path:                    "youtube-hls:" + originalURL,
		OriginalPath:            originalURL,
		OutputDir:               outputDir,
		CreatedAt:               now,
		LastAccess:              now,
		Cancel:                  cancel,
		ProfileID:               profileID,
		ProfileName:             profileName,
		ClientIP:                clientIP,
		AudioTrackIndex:         -1,
		SubtitleTrackIndex:      -1,
		StreamStartTime:         now,
		LastSegmentRequest:      now,
		MinSegmentRequested:     -1,
		MaxSegmentRequested:     -1,
		LastPlaybackSegment:     -1,
		LastSegmentServed:       -1,
		EarliestBufferedSegment: -1,
		FinalSegmentCount:       -1,
		subtitleExtractOffsets:  make(map[int]float64),
		PlaybackTarget:          "youtube",
		YouTubeVideoURL:         videoURL,
		YouTubeAudioURL:         audioURL,
		YouTubeProxyURL:         strings.TrimSpace(proxyURL),
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	log.Printf("[hls-youtube] created session %s for %s proxy=%v video={%s} audio={%s}",
		sessionID,
		originalURL,
		strings.TrimSpace(proxyURL) != "",
		youtubeMediaURLLogSummary(videoURL),
		youtubeMediaURLLogSummary(audioURL))

	startupErr := make(chan error, 1)
	go func() {
		if err := m.startYouTubeTranscoding(bgCtx, session, videoURL, audioURL, proxyURL); err != nil {
			if errors.Is(err, context.Canceled) {
				log.Printf("[hls-youtube] session %s transcoding cancelled", sessionID)
				return
			}
			log.Printf("[hls-youtube] session %s transcoding failed: %v", sessionID, err)
			session.mu.Lock()
			session.Completed = true
			session.FatalError = err.Error()
			session.FatalErrorTime = time.Now()
			session.mu.Unlock()
			select {
			case startupErr <- err:
			default:
			}
		}
	}()

	// yt-dlp URLs can expire or be rejected by the CDN immediately. If FFmpeg
	// fails during startup, surface that through /youtube/hls/start so callers
	// can fall back before navigating to a playlist that will never exist. When
	// FFmpeg is healthy, wait until the playlist references real media and the
	// first two segments exist so TV players do not mount into an immediate
	// segment-boundary rebuffer.
	cleanupStartupFailure := func() {
		cancel()
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
	}
	readyTimer := time.NewTimer(youtubeStartupReadyTimeout)
	defer readyTimer.Stop()
	readyTicker := time.NewTicker(youtubeStartupReadyPollInterval)
	defer readyTicker.Stop()
	lastPlaylistBytes := 0
	var lastSegment0Bytes int64
	var lastSegment1Bytes int64

	for {
		playlistBytes, segment0Bytes, segment1Bytes, ready := youtubeStartupMediaReady(session)
		lastPlaylistBytes = playlistBytes
		lastSegment0Bytes = segment0Bytes
		lastSegment1Bytes = segment1Bytes
		if ready {
			log.Printf("[hls-youtube] session %s startup media ready elapsed=%s playlistBytes=%d segment0Bytes=%d segment1Bytes=%d",
				sessionID,
				time.Since(now).Round(time.Millisecond),
				playlistBytes,
				segment0Bytes,
				segment1Bytes)
			break
		}

		select {
		case err := <-startupErr:
			log.Printf("[hls-youtube] session %s startup failed elapsed=%s playlistBytes=%d segment0Bytes=%d segment1Bytes=%d video={%s} audio={%s}: %v",
				sessionID,
				time.Since(now).Round(time.Millisecond),
				lastPlaylistBytes,
				lastSegment0Bytes,
				lastSegment1Bytes,
				youtubeMediaURLLogSummary(videoURL),
				youtubeMediaURLLogSummary(audioURL),
				err)
			cleanupStartupFailure()
			return nil, err
		case <-readyTicker.C:
			continue
		case <-readyTimer.C:
			log.Printf("[hls-youtube] session %s startup readiness timeout elapsed=%s playlistBytes=%d segment0Bytes=%d segment1Bytes=%d rejecting incomplete session",
				sessionID,
				time.Since(now).Round(time.Millisecond),
				lastPlaylistBytes,
				lastSegment0Bytes,
				lastSegment1Bytes)
			cleanupStartupFailure()
			return nil, fmt.Errorf(
				"youtube HLS startup timed out before two segments were ready (playlistBytes=%d segment0Bytes=%d segment1Bytes=%d)",
				lastPlaylistBytes,
				lastSegment0Bytes,
				lastSegment1Bytes,
			)
		case <-ctx.Done():
			cleanupStartupFailure()
			return nil, ctx.Err()
		}
	}

	return session, nil
}

func youtubeStartupMediaReady(session *HLSSession) (int, int64, int64, bool) {
	if session == nil {
		return 0, 0, 0, false
	}

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	content, err := os.ReadFile(playlistPath)
	if err != nil || !playlistHasMediaSegment(content) {
		return len(content), 0, 0, false
	}

	segment0Info, err := os.Stat(filepath.Join(session.OutputDir, "segment0.ts"))
	if err != nil || segment0Info.Size() <= 0 {
		return len(content), 0, 0, false
	}

	segment1Info, err := os.Stat(filepath.Join(session.OutputDir, "segment1.ts"))
	if err != nil || segment1Info.Size() <= 0 {
		return len(content), segment0Info.Size(), 0, false
	}

	return len(content), segment0Info.Size(), segment1Info.Size(), true
}

func ffmpegHTTPProxyArgs(proxyURL string) []string {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return []string{"-http_proxy", proxyURL}
	default:
		return nil
	}
}

func (m *HLSManager) startYouTubeTranscoding(ctx context.Context, session *HLSSession, videoURL, audioURL, proxyURL string) error {
	if m.ffmpegPath == "" {
		return fmt.Errorf("ffmpeg path not configured")
	}

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	segmentPattern := filepath.Join(session.OutputDir, "segment%d.ts")
	session.mu.RLock()
	startOffset := session.TranscodingOffset
	session.mu.RUnlock()
	args := youtubeTranscodingArgs(videoURL, audioURL, proxyURL, playlistPath, segmentPattern, startOffset)

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	started := time.Now()
	log.Printf("[hls-youtube] session %s: starting FFmpeg segment-safe HLS proxy=%v video={%s} audio={%s}",
		session.ID,
		strings.TrimSpace(proxyURL) != "",
		youtubeMediaURLLogSummary(videoURL),
		youtubeMediaURLLogSummary(audioURL))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	session.mu.Lock()
	session.FFmpegCmd = cmd
	session.FFmpegPID = cmd.Process.Pid
	session.mu.Unlock()
	log.Printf("[hls-youtube] session %s: FFmpeg pid=%d started elapsed=%s", session.ID, cmd.Process.Pid, time.Since(started).Round(time.Millisecond))

	err := cmd.Wait()
	if ctx.Err() != nil {
		session.mu.Lock()
		if session.FFmpegCmd == cmd {
			session.FFmpegPID = 0
		}
		session.mu.Unlock()
		return ctx.Err()
	}
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	highestSegment := -1
	if files, globErr := filepath.Glob(filepath.Join(session.OutputDir, "segment*.ts")); globErr == nil {
		for _, file := range files {
			var segNum int
			if _, scanErr := fmt.Sscanf(filepath.Base(file), "segment%d.ts", &segNum); scanErr == nil && segNum > highestSegment {
				highestSegment = segNum
			}
		}
	}

	session.mu.Lock()
	if session.FFmpegCmd == cmd {
		session.Completed = true
		session.FinalSegmentCount = highestSegment
		session.FFmpegPID = 0
		if err != nil {
			session.FatalError = strings.TrimSpace(stderr.String())
			if session.FatalError == "" {
				session.FatalError = err.Error()
			}
			session.FatalErrorTime = time.Now()
		}
	}
	session.mu.Unlock()

	if err != nil {
		return fmt.Errorf("ffmpeg exited code=%d after %s: %s", exitCode, time.Since(started).Round(time.Millisecond), sanitizeYouTubeMediaURLsInText(strings.TrimSpace(stderr.String())))
	}

	log.Printf("[hls-youtube] session %s: FFmpeg completed segments=%d elapsed=%s", session.ID, highestSegment+1, time.Since(started).Round(time.Millisecond))
	return nil
}

func youtubeTranscodingArgs(videoURL, audioURL, proxyURL, playlistPath, segmentPattern string, startOffset float64) []string {
	args := []string{
		"-nostdin",
		"-y",
		"-loglevel", "error",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-probesize", "1000000",
		"-analyzeduration", "500000",
		"-fflags", "+genpts+discardcorrupt",
	}
	if startOffset > 0 {
		args = append(args, "-noaccurate_seek", "-ss", fmt.Sprintf("%.3f", startOffset))
	}
	args = append(args, ffmpegHTTPProxyArgs(proxyURL)...)
	args = append(args,
		"-user_agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-i", videoURL,
	)
	if startOffset > 0 {
		args = append(args, "-noaccurate_seek", "-ss", fmt.Sprintf("%.3f", startOffset))
	}
	args = append(args, ffmpegHTTPProxyArgs(proxyURL)...)
	args = append(args,
		"-user_agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-i", audioURL,
		"-map", "0:v:0",
		"-map", "1:a:0",
		// Stream-copying arbitrary YouTube GOPs can produce TS segments whose
		// IDR frames do not repeat SPS/PPS. Such segments are not independently
		// decodable even if the playlist advertises EXT-X-INDEPENDENT-SEGMENTS,
		// and KSPlayer/AVPlayer rejects the video track. Re-encode this short
		// trailer path with keyframes aligned to the HLS segment cadence.
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "21",
		"-profile:v", "high",
		"-level:v", "4.1",
		"-pix_fmt", "yuv420p",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%.3f)", hlsSegmentDuration),
		"-sc_threshold", "0",
		"-threads", "0",
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "160k",
		"-ar", "48000",
		"-hls_time", fmt.Sprintf("%.0f", hlsSegmentDuration),
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", segmentPattern,
		"-f", "hls",
		playlistPath,
	)
	return args
}

// CreateLiveSession creates an HLS session for live TV streams
// Unlike VOD sessions, live sessions don't have a known duration and don't support seeking
func (m *HLSManager) CreateLiveSession(ctx context.Context, liveURL, provider, bucketKey, profileID, profileName, clientIP, playbackTarget string, tuning LiveTuningSettings, liveCCExtractionEnabled bool) (*HLSSession, error) {
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
		LiveProvider:            normalizeLiveProvider(provider),
		LiveBucket:              strings.TrimSpace(bucketKey),
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
		FinalSegmentCount:       -1, // Initialize to -1 (transcoding still running)
		AudioTrackIndex:         -1, // Use default
		SubtitleTrackIndex:      -1, // No subtitles for live TV
		ProfileID:               profileID,
		ProfileName:             profileName,
		ClientIP:                clientIP,
		PlaybackTarget:          strings.ToLower(strings.TrimSpace(playbackTarget)),
		LiveTuning:              tuning,
		LiveCCExtractionEnabled: liveCCExtractionEnabled,
		subtitleExtractOffsets:  make(map[int]float64),
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

	// Detect closed captions in background when extraction is enabled.
	// Continuous extraction is lazy — starts on first captions.srt request.
	if liveCCExtractionEnabled {
		m.detectAndSetClosedCaptions(session)
	} else {
		session.mu.Lock()
		session.HasClosedCaptions = false
		session.CCDetectionDone = true
		session.mu.Unlock()
		log.Printf("[hls] live session %s: closed caption extraction disabled by settings", sessionID)
	}

	log.Printf("[hls] created live session %s for host %q", sessionID, requestsecurity.URLForLog(liveURL))
	return session, nil
}

func normalizeLiveProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "xtream":
		return "xtream"
	case "stremio":
		return "stremio"
	default:
		return "m3u"
	}
}

// GetLiveUsage returns concurrent live stream usage for the requested provider.
func (m *HLSManager) GetLiveUsage(provider, bucketKey string, maxStreams int) LiveUsageSummary {
	targetProvider := normalizeLiveProvider(provider)
	targetBucket := strings.TrimSpace(bucketKey)

	m.mu.RLock()
	defer m.mu.RUnlock()
	current := 0
	for _, session := range m.sessions {
		session.mu.RLock()
		isCounted := session.IsLive && !session.Completed
		sessionProvider := normalizeLiveProvider(session.LiveProvider)
		sessionBucket := strings.TrimSpace(session.LiveBucket)
		session.mu.RUnlock()

		if !isCounted {
			continue
		}
		if sessionProvider != targetProvider {
			continue
		}
		if targetBucket != "" && sessionBucket != targetBucket {
			continue
		}
		current++
	}
	atLimit := maxStreams > 0 && current >= maxStreams
	available := 0
	if maxStreams > 0 {
		available = maxStreams - current
		if available < 0 {
			available = 0
		}
	}

	return LiveUsageSummary{
		Provider:         targetProvider,
		CurrentStreams:   current,
		MaxStreams:       maxStreams,
		AvailableStreams: available,
		AtLimit:          atLimit,
		Providers: []LiveProviderUsageEntry{
			{
				Provider:  targetProvider,
				Current:   current,
				Max:       maxStreams,
				Available: available,
				AtLimit:   atLimit,
			},
		},
	}
}

// startLiveTranscoding starts FFmpeg for live TV HLS output
func (m *HLSManager) startLiveTranscoding(ctx context.Context, session *HLSSession) error {
	log.Printf("[hls] live session %s: starting live transcoding for %s", session.ID, session.Path)

	playlistPath := filepath.Join(session.OutputDir, "stream.m3u8")
	segmentPattern := filepath.Join(session.OutputDir, "segment%d.ts")

	// When a proxy is configured we cannot let ffmpeg reach the provider
	// directly: providers reject non-proxy source IPs (401) and redirect .ts
	// requests to CDN nodes that require a User-Agent. ffmpeg also cannot use a
	// SOCKS5 proxy natively. Fetch through the proxied Go client (which sets the
	// User-Agent and follows redirects) and pipe it into ffmpeg's stdin instead.
	var proxyBody io.ReadCloser
	inputArg := session.Path
	if proxyURL := strings.TrimSpace(session.LiveTuning.ProxyURL); proxyURL != "" {
		client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{ResponseHeaderTimeout: 15 * time.Second}, proxyURL)
		if err != nil {
			return fmt.Errorf("create proxied live client: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, session.Path, nil)
		if err != nil {
			return fmt.Errorf("prepare proxied live request: %w", err)
		}
		req.Header.Set("User-Agent", liveStreamUserAgent)
		applyRequestHeaders(req.Header, session.LiveTuning.RequestHeaders)
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("open proxied live stream: %w", err)
		}
		if resp.StatusCode >= http.StatusBadRequest {
			resp.Body.Close()
			return fmt.Errorf("proxied live stream returned status %d", resp.StatusCode)
		}
		proxyBody = resp.Body
		inputArg = "pipe:0"
		log.Printf("[hls] live session %s: streaming upstream via proxy %s", session.ID, requestsecurity.URLForLog(proxyURL))
	}

	// Build FFmpeg args optimized for live input
	args := []string{}
	if proxyBody == nil {
		// -nostdin only matters when not feeding the input over stdin.
		args = append(args, "-nostdin")
	}
	args = append(args,
		"-y",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto,udp,rtp,rtmp",
	)

	// Apply probe/analyze settings (these mirror StreamChannel in live.go)
	if session.LiveTuning.ProbeSizeMB > 0 {
		args = append(args, "-probesize", fmt.Sprintf("%d", session.LiveTuning.ProbeSizeMB*1024*1024))
	}
	if session.LiveTuning.AnalyzeDurationSec > 0 {
		args = append(args, "-analyzeduration", fmt.Sprintf("%d", session.LiveTuning.AnalyzeDurationSec*1000000))
	}

	// Low latency mode: reduce buffering
	if session.LiveTuning.LowLatency {
		args = append(args, "-fflags", "+genpts+nobuffer+discardcorrupt", "-flags", "+low_delay")
	} else {
		args = append(args, "-fflags", "+genpts+discardcorrupt")
	}

	// HTTP-specific input options (User-Agent, reconnection) only apply when
	// ffmpeg reads the provider URL directly. On the proxied path the input is
	// pipe:0 and these options are rejected ("Option reconnect not found").
	if proxyBody == nil {
		if !hasRequestHeader(session.LiveTuning.RequestHeaders, "User-Agent") {
			args = append(args, "-user_agent", liveStreamUserAgent)
		}
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "3",
		)
		if headerArg := ffmpegHeadersArg(session.LiveTuning.RequestHeaders); headerArg != "" {
			args = append(args, "-headers", headerArg)
		}
		// These are HLS-demuxer-private options. Some Stremio/live providers proxy
		// HLS segments through signed URLs without .ts/.m4s extensions, which the
		// HLS demuxer rejects by default. Only apply them for actual .m3u8 inputs —
		// for a direct MPEG-TS (.ts) input the mpegts demuxer is selected and these
		// options abort the command ("Option ... not found").
		if inputLooksLikeHLS(session.Path) {
			args = append(args,
				"-allowed_extensions", "ALL",
				"-allowed_segment_extensions", "ALL",
				"-extension_picky", "0",
			)
		}
	}

	args = append(args, "-i", inputArg)
	session.mu.Lock()
	if isNativeLivePlaybackTarget(session.PlaybackTarget) {
		session.VideoEncoder = "copy"
		log.Printf("[hls] live session %s: using native transmux mode (video=copy audio=copy target=%q)", session.ID, session.PlaybackTarget)
	} else {
		session.VideoEncoder = "libx264"
		log.Printf("[hls] live session %s: using compatibility transcode mode (video=libx264 audio=aac target=%q)", session.ID, session.PlaybackTarget)
	}
	session.mu.Unlock()
	args = append(args, liveHLSOutputArgs(session.PlaybackTarget, segmentPattern, playlistPath)...)

	log.Printf("[hls] live session %s: starting FFmpeg with args: %v", session.ID, args)

	cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
	cmd.Dir = session.OutputDir
	if proxyBody != nil {
		cmd.Stdin = proxyBody
		defer proxyBody.Close()
	}

	// Capture stderr for logging
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	session.mu.Lock()
	session.FFmpegCmd = cmd
	startErr := cmd.Start()
	pid := 0
	if startErr == nil {
		pid = cmd.Process.Pid
		session.FFmpegPID = pid
	}
	session.mu.Unlock()

	if startErr != nil {
		return fmt.Errorf("start ffmpeg: %w", startErr)
	}

	log.Printf("[hls] live session %s: FFmpeg started (PID=%d)", session.ID, pid)

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

	// Start idle timeout goroutine - kills FFmpeg if no segments requested for hlsIdleTimeout
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
				// (Live sessions don't use prequeue, so always apply startup timeout)
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

				// Idle timeout: if no segment requests for hlsIdleTimeout after playback started
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

func isNativeLivePlaybackTarget(playbackTarget string) bool {
	switch strings.ToLower(strings.TrimSpace(playbackTarget)) {
	case "native", "android", "ios", "tvos", "mpv", "ksplayer", "exoplayer":
		return true
	default:
		return false
	}
}

// liveNativeHLSListSize is the sliding playlist window for native transmux live.
// Larger than web because stream-copy can emit segments faster than players fetch
// them, and we intentionally do not use FFmpeg delete_segments for native.
const liveNativeHLSListSize = 30

// liveNativeSegmentKeepBehind is how many completed segment files to retain on disk
// behind the highest served/requested index when cleaning up native live sessions.
// ~2 minutes at 2s segments; covers playlist re-poll and ExoPlayer retry windows.
const liveNativeSegmentKeepBehind = 60

func liveHLSOutputArgs(playbackTarget, segmentPattern, playlistPath string) []string {
	args := make([]string, 0, 40)
	hlsFlags := "delete_segments+independent_segments+temp_file"
	listSize := "10"

	if isNativeLivePlaybackTarget(playbackTarget) {
		// Native apps (ExoPlayer / KSPlayer / MPV) demux/decode IPTV codecs themselves.
		// Always transmux (copy) for those targets — never libx264/aac here. Web browser
		// playback is the only live path that should re-encode for broad codec support.
		//
		// Do not use FFmpeg delete_segments for native live: stream-copy produces segments
		// faster than the player can pull the first URI, so delete_segments removes
		// segment0.ts before ExoPlayer's first request finishes (SEGMENT_TIMEOUT → 404).
		// Keep a wider playlist window and clean old files ourselves after serve.
		args = append(args,
			"-c:v", "copy",
			"-c:a", "copy",
			"-max_muxing_queue_size", "1024",
		)
		// temp_file only: atomic segment publish, no independent_segments (copy cannot
		// force keyframes at segment boundaries), no delete_segments (see above).
		hlsFlags = "temp_file"
		listSize = strconv.Itoa(liveNativeHLSListSize)
	} else {
		// Web and legacy callers retain the compatibility encode with a controlled
		// keyframe cadence. delete_segments is safe here because re-encoding is slower
		// than typical playlist/segment fetch cadence.
		args = append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-tune", "zerolatency",
			"-profile:v", "main",
			"-pix_fmt", "yuv420p",
			"-crf", "23",
			"-max_muxing_queue_size", "1024",
			"-force_key_frames", "expr:gte(t,n_forced*1)",
			"-sc_threshold", "0",
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "128k",
			"-ar", "48000",
		)
	}

	return append(args,
		"-f", "hls",
		"-hls_init_time", "1",
		"-hls_time", "2",
		"-hls_list_size", listSize,
		"-hls_flags", hlsFlags,
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	)
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
	pollInterval := 25 * time.Millisecond // Reduced from 100ms for faster startup

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
	session.disableUnsafeDirectCast(m.castCapabilities(session))
	stableCastMode := session.usesStableCastTimeline()
	if stableCastMode {
		// Legacy Cast output must not depend on receiver/TV Dolby passthrough support.
		forceAAC = true
	}

	// Cache forceAAC for recovery restarts
	session.mu.Lock()
	if session.Stopped {
		session.mu.Unlock()
		log.Printf("[hls] session %s: transcoding start skipped because the session was stopped", session.ID)
		return nil
	}
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

	if len(audioStreams) == 0 && (session.HasDV || session.HasHDR) {
		log.Printf("[hls] session %s: audio probe returned no streams and fMP4 output required; forcing AAC transcoding for safety", session.ID)
		forceAAC = true
	}

	if session.DirectCastMode && !forceAAC && directCastAudioNeedsAAC(audioStreams, session.AudioTrackIndex) {
		caps := m.castCapabilities(session)
		if caps.Supports(castcaps.VariantTSAC3) && isSelectedAudioDolby(audioStreams, session.AudioTrackIndex) {
			log.Printf("[hls] session %s: receiver supports AC-3; copying Dolby audio in direct Cast mode", session.ID)
		} else {
			log.Printf("[hls] session %s: direct Cast audio is not AAC; re-encoding audio only so the receiver can decode it (video stays copied)", session.ID)
			forceAAC = true
			session.mu.Lock()
			session.forceAAC = true
			session.mu.Unlock()
		}
	}

	// For seeking to work with -c:v copy, we need a seekable input
	// Check if we can get a direct HTTP URL instead of using a pipe
	log.Printf("[hls] session %s: checking for direct URL support (transcodingOffset=%.3f)", session.ID, session.TranscodingOffset)
	directURL, hasDirectURL := m.getDirectURL(ctx, session)
	if hasDirectURL {
		videoTracef("[hls] session %s: got direct URL", session.ID)
	} else {
		log.Printf("[hls] session %s: no direct URL available, using pipe", session.ID)
	}

	var resp *streaming.Response
	var usingPipe bool
	var headerPrefix []byte
	var requireMatroskaAlign bool
	var proxyURL string // URL for FFmpeg to use (via throttling proxy)
	var directInputPath string

	var proxy *throttlingProxy // proxy server to close when done

	// For remote direct URLs, use a throttling proxy so FFmpeg can use HTTP Range requests for
	// seeking while we still control the download speed. Local providers return filesystem paths;
	// pass those straight to FFmpeg instead of treating them as HTTP URLs.
	if hasDirectURL {
		if isHTTPDirectURL(directURL) {
			videoTracef("[hls] session %s: setting up throttling proxy for direct URL", session.ID)

			var err error
			proxy, proxyURL, err = newThrottlingProxy(directURL, session, m.applyExternalUsenetWebDAVAuth)
			if err != nil {
				log.Printf("[hls] session %s: failed to create throttling proxy: %v, falling back to pipe", session.ID, err)
				hasDirectURL = false // Fall through to pipe handling
			} else {
				log.Printf("[hls] session %s: throttling proxy ready at %s", session.ID, proxyURL)
			}
		} else {
			directInputPath = strings.TrimSpace(directURL)
			log.Printf("[hls] session %s: FFmpeg will read direct local input: %s", session.ID, directInputPath)
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
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		// Reduce probe/analyze time for faster startup (default is 5MB/5s)
		"-probesize", "1000000", // 1MB
		"-analyzeduration", "500000", // 0.5s
		// A/V sync flags: generate PTS if missing, discard corrupt packets
		"-fflags", "+genpts+discardcorrupt",
	}
	// NOTE: -strict unofficial is added AFTER -i as an output option (see below)
	// Placing it before -i doesn't enable writing dvcC boxes to the output

	// Seeking strategy:
	// - Small seeks (< 30s): OUTPUT seeking (-ss after -i) - accurate but reads from start
	// - Large seeks (>= 30s): INPUT seeking (-ss before -i) - uses HTTP Range to skip data
	const outputSeekThreshold = 30.0 // seconds

	// Determine up-front (these are computed in full detail further below too) whether the video
	// will be transcoded and whether a same-pass subtitle is being muxed, because both change the
	// seek strategy needed to keep the subtitle aligned with the video.
	videoWillTranscode := hlsWebVideoWillTranscode(session.PlaybackTarget, session.ProbeData)
	if stableCastMode {
		// Stable Cast timelines require deterministic two-second keyframe
		// boundaries so logical segment N always maps to the same media time.
		videoWillTranscode = true
	}
	_, subtitleRenditionWanted := selectedTextSubtitleStream(subtitleStreams, session.SubtitleTrackIndex)
	subtitleRenditionWanted = session.PlaybackTarget == "web" && subtitleRenditionWanted
	forceVideoTranscodeForWebSubtitleSeek := shouldForceWebSubtitleVideoTranscode(session.PlaybackTarget, subtitleStreams, session.SubtitleTrackIndex, session.TranscodingOffset)
	if forceVideoTranscodeForWebSubtitleSeek {
		videoWillTranscode = true
	}

	// Build the video encode plan up-front so any hardware-device
	// initialization (-vaapi_device / -init_hw_device) can be injected as a
	// global option BEFORE -i. The plan picks a GPU H.264 encoder when one is
	// detected and working, and tone maps HDR/DV down to SDR for web and Cast.
	var webEncodePlan videoEncodePlan
	useWebEncodePlan := videoWillTranscode && (session.PlaybackTarget == "web" || stableCastMode)
	if useWebEncodePlan {
		session.mu.RLock()
		forceSoftwareEncode := session.forceSoftwareEncode
		session.mu.RUnlock()
		caps := m.hwAccelCaps()
		if forceSoftwareEncode {
			caps = detectHWAccel(m.ffmpegPath, string(HWNone))
		}
		tonemapNeeded := session.HasDV || session.HasHDR
		castMaxWidth, castMaxHeight, castMaxFPS := 0, 0, 0
		if stableCastMode {
			session.mu.RLock()
			capHeight := session.CastMaxHeight
			session.mu.RUnlock()
			castMaxWidth, castMaxHeight, castMaxFPS = legacyCastEncodeLimits(session.ProbeData, capHeight)
			log.Printf("[hls] session %s: compatibility Cast encode box %dx%d@%d (capHeight=%d)",
				session.ID, castMaxWidth, castMaxHeight, castMaxFPS, capHeight)
		}
		sourceTransfer := ""
		if session.ProbeData != nil {
			sourceTransfer = session.ProbeData.ColorTransfer
		}
		webEncodePlan = buildVideoEncodePlanWithLimits(
			caps,
			tonemapNeeded,
			castMaxWidth,
			castMaxHeight,
			castMaxFPS,
			sourceTransfer,
		)
		if len(webEncodePlan.GlobalArgs) > 0 {
			args = append(args, webEncodePlan.GlobalArgs...)
		}
		log.Printf("[hls] session %s: web encode plan kind=%s hwEncode=%v tonemapped=%v filter=%q",
			session.ID, webEncodePlan.Kind, webEncodePlan.HardwareEncode, webEncodePlan.Tonemapped, webEncodePlan.Filter)
		session.mu.Lock()
		session.VideoEncoder = string(webEncodePlan.Kind)
		if webEncodePlan.Kind == HWNone {
			session.VideoEncoder = "libx264"
		}
		session.ToneMapper = webEncodePlan.Tonemap
		session.HardwareEncode = webEncodePlan.HardwareEncode
		session.mu.Unlock()
	}

	// Force INPUT seeking when muxing a same-pass subtitle so the single -ss before -i applies to
	// BOTH the video and the subtitle output. OUTPUT seeking only seeks the first output, which
	// would leave the subtitle starting at the beginning of the file and wildly out of sync.
	useOutputSeeking := session.TranscodingOffset > 0 &&
		session.TranscodingOffset < outputSeekThreshold &&
		!subtitleRenditionWanted &&
		!stableCastMode

	// For INPUT seeking, add -ss before -i.
	// -noaccurate_seek is only for pure video-copy: first packet must be a keyframe.
	// Whenever we decode (transcode, web mid-file PTS reset, same-pass subs, cast),
	// use accurate seek so A/V (and subs) share the requested content time — not the
	// prior keyframe for one stream and the request time for the other.
	if session.TranscodingOffset > 0 && !useOutputSeeking {
		if useAccurateHLSInputSeek(session.PlaybackTarget, session.TranscodingOffset, videoWillTranscode, subtitleRenditionWanted, stableCastMode) {
			args = append(args, "-ss", fmt.Sprintf("%.3f", session.TranscodingOffset))
			log.Printf("[hls] session %s: using accurate INPUT seeking to %.3fs", session.ID, session.TranscodingOffset)
		} else {
			args = append(args, "-noaccurate_seek", "-ss", fmt.Sprintf("%.3f", session.TranscodingOffset))
			log.Printf("[hls] session %s: using INPUT seeking to %.3fs with -noaccurate_seek", session.ID, session.TranscodingOffset)
		}
	}

	// Add input source - use proxy URL or local direct path if available, otherwise use pipe.
	if proxyURL != "" {
		args = append(args, "-i", proxyURL)
		log.Printf("[hls] session %s: FFmpeg input set to proxy URL: %s", session.ID, proxyURL)
	} else if directInputPath != "" {
		args = append(args, "-i", directInputPath)
		log.Printf("[hls] session %s: FFmpeg input set to direct local path: %s", session.ID, directInputPath)
	} else {
		args = append(args, "-i", "pipe:0")
		log.Printf("[hls] session %s: FFmpeg input set to pipe:0", session.ID)
	}

	// For OUTPUT seeking, add -ss after -i
	if useOutputSeeking {
		args = append(args, "-ss", fmt.Sprintf("%.3f", session.TranscodingOffset))
		log.Printf("[hls] session %s: using OUTPUT seeking to %.3fs (reads from start)", session.ID, session.TranscodingOffset)
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

	// Web mid-file starts also force per-stream PTS reset via filters (see below).
	// make_zero keeps the MPEG-TS muxer from emitting negative DTS that MSE rejects.
	if webSeekTimelineResetNeeded(session.PlaybackTarget, session.TranscodingOffset) {
		args = append(args, "-avoid_negative_ts", "make_zero")
		log.Printf("[hls] session %s: web seek/resume timeline reset enabled (setpts/asetpts)", session.ID)
	}

	session.mu.RLock()
	castSegmentStartNumber := session.SegmentStartNumber
	session.mu.RUnlock()
	if stableCastMode && castSegmentStartNumber > 0 {
		// FFmpeg rebases a seeked run to zero. Offset its output timestamps back
		// into the receiver's stable HLS timeline.
		args = append(args, "-output_ts_offset", fmt.Sprintf("%.3f", float64(castSegmentStartNumber)*hlsSegmentDuration))
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
			// Check if this is an incompatible audio codec (TrueHD, DTS, etc.)
			needsTranscode := IsIncompatibleAudioCodec(selectedStream.Codec)

			if needsTranscode {
				// Incompatible codec selected - we need to transcode it
				log.Printf("[hls] session %s: requested audio track %d is %s (incompatible); will transcode to AAC", session.ID, session.AudioTrackIndex, selectedStream.Codec)
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

	// Same-pass WebVTT subtitle rendition (web player only).
	// The selected text subtitle is muxed in THIS ffmpeg pass (sharing accurate -ss,
	// start_at_zero, and make_zero with video/audio) and exposed as a single synced
	// WebVTT file (second -f webvtt output below). Independent setpts/asetpts are
	// disabled in this mode so WebVTT cues stay on the same demuxer clock as A/V;
	// the web overlay renders with a zero offset. Gated to PlaybackTarget=="web".
	webSubtitleRendition := false
	webSubtitleAbsIndex := -1
	if session.PlaybackTarget == "web" && session.SubtitleTrackIndex >= 0 {
		if stream, ok := selectedTextSubtitleStream(subtitleStreams, session.SubtitleTrackIndex); ok {
			webSubtitleRendition = true
			webSubtitleAbsIndex = stream.Index
		}
	}
	if webSubtitleRendition {
		log.Printf("[hls] session %s: same-pass synced WebVTT subtitle enabled for stream %d", session.ID, webSubtitleAbsIndex)
	}
	session.mu.Lock()
	session.UsesSubtitleRendition = webSubtitleRendition
	session.mu.Unlock()

	// Independent setpts/asetpts only when there is no same-pass VTT sharing the demuxer clock.
	applyWebSeekPTSFilters := webSeekPTSFiltersNeeded(session.PlaybackTarget, session.TranscodingOffset, webSubtitleRendition)
	if webSeekTimelineResetNeeded(session.PlaybackTarget, session.TranscodingOffset) && webSubtitleRendition {
		log.Printf("[hls] session %s: skipping setpts/asetpts so same-pass WebVTT stays on the shared demuxer timeline", session.ID)
	}

	// Check if video codec is compatible with iOS (H.264/HEVC only)
	// Legacy codecs like MPEG-4 Part 2 (XviD/DivX), MPEG-2, etc. need transcoding
	needsVideoTranscode := false
	videoCodec := ""
	if session.ProbeData != nil {
		videoCodec = session.ProbeData.VideoCodec
		needsVideoTranscode = IsIncompatibleVideoCodec(videoCodec)
	}
	if stableCastMode {
		needsVideoTranscode = true
		pixFmt, profile := "", ""
		if session.ProbeData != nil {
			pixFmt, profile = session.ProbeData.VideoPixFmt, session.ProbeData.VideoProfile
		}
		log.Printf("[hls] session %s: cast stable timeline requires deterministic H.264 segments; transcoding codec=%q pix_fmt=%q profile=%q",
			session.ID, videoCodec, pixFmt, profile)
	}
	if session.PlaybackTarget == "web" && !isBrowserCopyCompatibleVideo(session.ProbeData) {
		needsVideoTranscode = true
		if session.ProbeData != nil {
			log.Printf("[hls] session %s: web target requires browser-compatible video; transcoding codec=%q pix_fmt=%q profile=%q to H.264",
				session.ID, session.ProbeData.VideoCodec, session.ProbeData.VideoPixFmt, session.ProbeData.VideoProfile)
		} else {
			log.Printf("[hls] session %s: web target has no video probe data; transcoding to H.264", session.ID)
		}
	}
	if forceVideoTranscodeForWebSubtitleSeek {
		needsVideoTranscode = true
		log.Printf("[hls] session %s: web subtitle seek/resume requires video transcode for accurate subtitle sync", session.ID)
	}
	// setpts requires decode; force a web re-encode on mid-file starts even for copy-compatible
	// H.264 so we can reset the A/V clock after input -ss (skipped when same-pass VTT is active).
	if applyWebSeekPTSFilters && !needsVideoTranscode {
		needsVideoTranscode = true
		// Prefer software here: hardware device init must precede -i, and we are past that point.
		useWebEncodePlan = false
		log.Printf("[hls] session %s: web seek/resume forces video transcode for PTS reset", session.ID)
	}

	if needsVideoTranscode {
		// Transcode video to H.264 when the source is incompatible or accurate web subtitle seeking
		// requires decoding away keyframe pre-roll.
		// Use ultrafast preset + zerolatency tune for fastest possible startup
		// Quality is slightly lower than veryfast but startup is significantly faster
		if useWebEncodePlan {
			// Web player path: GPU-accelerated H.264 encode (when available) plus
			// HDR/DV -> SDR tone mapping. webEncodePlan was built above so its
			// device-init globals could precede -i.
			vf := webEncodePlan.Filter
			if applyWebSeekPTSFilters {
				vf = withWebSeekVideoPTSReset(vf)
			}
			if vf != "" {
				args = append(args, "-vf", vf)
			}
			args = append(args, webEncodePlan.EncoderArgs...)
			args = append(args,
				"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%.3f)", hlsSegmentDuration),
				"-threads", "0", // Ignored by hardware encoders; used by libx264.
			)
			session.mu.Lock()
			session.TonemappedToSDR = webEncodePlan.Tonemapped
			session.mu.Unlock()
			log.Printf("[hls] session %s: transcoding to H.264 via %s (hwEncode=%v, tonemapped=%v)",
				session.ID, webEncodePlan.Kind, webEncodePlan.HardwareEncode, webEncodePlan.Tonemapped)
		} else {
			// Native/live transcode of an incompatible codec — CPU H.264, no tone mapping.
			// Also used for late web seek PTS-reset when a hardware plan was not prepared pre-input.
			if forceVideoTranscodeForWebSubtitleSeek {
				log.Printf("[hls] session %s: transcoding video codec %q to H.264 for accurate web subtitle seek/resume (ultrafast)", session.ID, videoCodec)
			} else {
				log.Printf("[hls] session %s: video transcode required for codec %q, transcoding to H.264 (ultrafast)", session.ID, videoCodec)
			}
			if applyWebSeekPTSFilters {
				args = append(args, "-vf", "setpts=PTS-STARTPTS,format=yuv420p")
			}
			args = append(args,
				"-c:v", "libx264",
				"-preset", "ultrafast",
				"-tune", "zerolatency",
				"-crf", "23",
				"-profile:v", "high",
				"-level", "4.1",
				"-pix_fmt", "yuv420p",
				"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%.3f)", hlsSegmentDuration),
				"-threads", "0", // Use all available CPU cores
			)
		}
		// When transcoding video for fMP4, also check if audio needs transcoding
		// MP3 audio doesn't work well in fMP4 containers on iOS - must use AAC
		if len(audioStreams) > 0 && audioStreams[0].Codec == "mp3" {
			log.Printf("[hls] session %s: MP3 audio detected with video transcode (fMP4), forcing AAC transcoding", session.ID)
			forceAAC = true
		}
	} else {
		args = append(args,
			"-c:v", "copy", // Copy video codec (H.264/HEVC compatible)
		)
	}

	// For Dolby Vision and HDR10, we MUST use fMP4 segments (not MPEG-TS)
	// - DV: preserves Dolby Vision metadata
	// - HDR10: iOS AVPlayer can't properly decode HEVC in MPEG-TS segments
	var segmentExt string
	needsFmp4 := session.HasDV || session.HasHDR
	if stableCastMode {
		needsFmp4 = false
		segmentExt = ".ts"
		log.Printf("[hls] session %s: using MPEG-TS segments for cast compatibility", session.ID)
	} else if session.HasDV && !session.DVDisabled {
		segmentExt = ".m4s"
		if needsVideoTranscode {
			log.Printf("[hls] session %s: using fMP4 H.264 output for web-compatible Dolby Vision/HDR source; skipping HEVC DV tag and hevc_metadata filter", session.ID)
		} else {
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
		}
	} else if session.HasHDR || (session.HasDV && session.DVDisabled) {
		// Also handles DV fallback - DV Profile 8 has HDR10 base layer that plays fine without DV metadata
		segmentExt = ".m4s"
		if needsVideoTranscode {
			log.Printf("[hls] session %s: using fMP4 H.264 output for web-compatible HDR source; skipping hvc1 tag and hevc_metadata filter", session.ID)
		} else if session.HDRMetadataDisabled {
			// Use hevc_metadata to ensure proper BT.2020/PQ color signaling for HDR10 content.
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
	} else if session.CastMode && !needsVideoTranscode && castPrefersMpegTS(session, m.castCapabilities(session)) {
		// Cast receivers built into TVs (and older Chromecast firmware) reject
		// HLS in fMP4 outright: the load is accepted, segments are fetched, and
		// playback never starts. MPEG-TS is the universally understood Cast
		// container, and H.264 + AAC/AC-3 remux into it without re-encoding, so
		// direct Cast keeps its zero-transcode video path.
		needsFmp4 = false
		segmentExt = ".ts"
		log.Printf("[hls] session %s: using MPEG-TS segments for direct Cast remux (Chromecast fMP4 HLS is not universally supported)", session.ID)
	} else if forceAAC && !session.DirectCastMode {
		// Non-direct Cast sessions (forceAAC=true): use MPEG-TS for maximum
		// Chromecast compatibility. Direct Cast may still normalize audio to AAC
		// while preserving the original video/container path.
		needsFmp4 = false
		segmentExt = ".ts"
		log.Printf("[hls] session %s: using MPEG-TS segments for cast/forceAAC session (Chromecast compatibility)", session.ID)
	} else {
		// TESTING: Use fMP4 for all content (normally SDR uses .ts MPEG-TS segments)
		// This allows testing HLS with react-native-video for SDR content
		// Don't force codec tag - let FFmpeg auto-detect (works for both H.264 and HEVC)
		needsFmp4 = true
		segmentExt = ".m4s"
		log.Printf("[hls] session %s: using fMP4 segments for SDR content (testing, no codec tag forced)", session.ID)
	}

	// Remember what was actually chosen. Everything downstream that needs to name a segment
	// re-derived this from session flags instead, and got it wrong for direct Cast: that path
	// remuxes to MPEG-TS but satisfies none of the fMP4 exclusions, so a completed playlist was
	// rebuilt advertising `.m4s` files that were never written. The receiver then asked for
	// segment0.m4s, got nothing, and stopped. One recorded fact beats six reconstructions.
	session.mu.Lock()
	session.SegmentExt = segmentExt
	session.mu.Unlock()

	// Audio handling
	audioCodecHandled := false

	aacLayout := hlsAACChannelLayoutLabel(session.PlaybackTarget)
	// The audio the receiver actually decodes. Channels are only known when we
	// encode it ourselves; a copied track's channel count is not probed.
	castPlanAudioCodec, castPlanAudioChannels := "", 0

	// Check if a specific incompatible audio track was selected (TrueHD, DTS, etc.)
	if mappedSpecificAudio && session.AudioTrackIndex >= 0 {
		for i := range audioStreams {
			if audioStreams[i].Index == session.AudioTrackIndex {
				needsTranscode := IsIncompatibleAudioCodec(audioStreams[i].Codec)
				if needsTranscode {

					// Transcode selected incompatible track to AAC.
					// Web: stereo for MSE. Cast: stereo unless receiver allows multichannel.
					// Native: 5.1. Always include aresample for TrueHD/DTS timing.
					log.Printf("[hls] session %s: transcoding selected %s track to AAC (%s)", session.ID, audioStreams[i].Codec, aacLayout)
					caps := m.castCapabilities(session)
					env := sessionCastAudioEnvelope(session, caps)
					args = appendAACTranscodeArgs(args, "", env, session.PlaybackTarget, session.TranscodingOffset, applyWebSeekPTSFilters)
					castPlanAudioCodec, castPlanAudioChannels = "aac", castAACChannels(env)
					audioCodecHandled = true
				}
				break
			}
		}
	}

	if !audioCodecHandled {
		if forceAAC {

			// Transcode first audio to AAC, copy others when not Cast.
			// Channel layout: web stereo, cast capability-aware, else 5.1.
			log.Printf("[hls] session %s: forceAAC transcoding primary audio to AAC (%s)", session.ID, aacLayout)
			caps := m.castCapabilities(session)
			env := sessionCastAudioEnvelope(session, caps)
			args = appendAACTranscodeArgs(args, ":0", env, session.PlaybackTarget, session.TranscodingOffset, applyWebSeekPTSFilters)
			castPlanAudioCodec, castPlanAudioChannels = "aac", castAACChannels(env)
			if !session.CastMode {
				args = append(args, "-c:a:1", "copy")
			}
		} else if hasTrueHD && !hasCompatibleAudio {
			// If only TrueHD exists, we must transcode it.
			log.Printf("[hls] session %s: transcoding TrueHD to AAC (no compatible alternative, %s)", session.ID, aacLayout)
			caps := m.castCapabilities(session)
			env := sessionCastAudioEnvelope(session, caps)
			args = appendAACTranscodeArgs(args, "", env, session.PlaybackTarget, session.TranscodingOffset, applyWebSeekPTSFilters)
			castPlanAudioCodec, castPlanAudioChannels = "aac", castAACChannels(env)
		} else if applyWebSeekPTSFilters {
			// Compatible audio still needs asetpts after mid-file input seek so A/V clocks match.
			log.Printf("[hls] session %s: re-encoding compatible audio to AAC for web seek timeline reset", session.ID)
			caps := m.castCapabilities(session)
			env := sessionCastAudioEnvelope(session, caps)
			args = appendAACTranscodeArgs(args, "", env, session.PlaybackTarget, session.TranscodingOffset, applyWebSeekPTSFilters)
			castPlanAudioCodec, castPlanAudioChannels = "aac", castAACChannels(env)
		} else {
			// Copy compatible audio
			args = append(args, "-c:a", "copy")
			if selected, ok := selectedAudioStream(audioStreams, session.AudioTrackIndex); ok {
				castPlanAudioCodec = selected.Codec
			}
		}
	}

	// Pin the video PTS to a ~0 origin so the same-pass WebVTT (also 0-based) aligns with the
	// video timeline the player exposes. The subtitle itself is added as a second output below.
	if webSubtitleRendition {
		args = append(args, "-muxpreload", "0", "-muxdelay", "0")
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
	// Extract ALL text-based subtitle tracks to sidecar VTT files during streaming
	// This allows track switching without re-downloading the source file
	// Works for both fMP4 and MPEG-TS - progressive extraction with flush_packets
	for _, stream := range subtitleStreams {
		if !isTextSubtitleCodec(stream.Codec) {
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
	configuredSegmentStart := session.SegmentStartNumber
	session.mu.RUnlock()

	if stableCastMode && configuredSegmentStart > 0 {
		segmentStartNum = strconv.Itoa(configuredSegmentStart)
		log.Printf("[hls] session %s: stable cast timeline - starting from logical segment %s", session.ID, segmentStartNum)
	} else if isRecovery {
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
	hlsInitTime := "1"
	if stableCastMode {
		hlsInitTime = fmt.Sprintf("%.0f", hlsSegmentDuration)
	}
	if needsFmp4 {
		// Use fMP4 segments for Dolby Vision and HDR10
		// iOS AVPlayer requires fMP4 for proper HEVC/HDR playback

		// First output: HLS stream with video and audio only
		// Use hls_init_time for shorter first segment (faster initial playback)
		args = append(args,
			"-f", "hls",
			"-hls_init_time", hlsInitTime,
			"-hls_time", "2", // Subsequent segments are 2s
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

		if len(sidecarSubtitles) > 0 {
			log.Printf("[hls] session %s: %d subtitle tracks will be extracted on demand", session.ID, len(sidecarSubtitles))
		}
	} else {
		// Use MPEG-TS segments for non-HDR content
		// Use hls_init_time for shorter first segment (faster initial playback)
		args = append(args,
			"-f", "hls",
			"-hls_init_time", hlsInitTime,
			"-hls_time", "2", // Subsequent segments are 2s
			"-hls_list_size", "0",
			"-hls_playlist_type", "event",
			"-hls_flags", "independent_segments+temp_file",
			"-hls_segment_type", "mpegts",
			"-hls_segment_filename", segmentPattern,
			"-start_number", segmentStartNum,
			playlistPath,
		)

		if len(sidecarSubtitles) > 0 {
			log.Printf("[hls] session %s: %d subtitle tracks will be extracted on demand", session.ID, len(sidecarSubtitles))
		}
	}

	// The output plan is final here, so this is the one place that knows exactly
	// what the receiver will be handed. Record it on the session; the fetch
	// timeline grades it later without ever asking the device anything.
	if session.CastMode {
		castPlanVideoCodec := videoCodec
		if needsVideoTranscode {
			castPlanVideoCodec = "h264"
		}
		fingerprint := castVariantsForPlan(castVariantPlan{
			Fmp4:          needsFmp4,
			VideoCodec:    castPlanVideoCodec,
			AudioCodec:    castPlanAudioCodec,
			AudioChannels: castPlanAudioChannels,
		})
		session.mu.Lock()
		session.castVariants = fingerprint
		session.mu.Unlock()
		if fingerprint.empty() {
			log.Printf("[hls] session %s: cast output (fmp4=%v video=%q audio=%q channels=%d) matches no capability variant; playback will not be graded",
				session.ID, needsFmp4, castPlanVideoCodec, castPlanAudioCodec, castPlanAudioChannels)
		} else {
			log.Printf("[hls] session %s: cast output exercises variant %s (fmp4=%v video=%q audio=%q channels=%d)",
				session.ID, fingerprint.Primary, needsFmp4, castPlanVideoCodec, castPlanAudioCodec, castPlanAudioChannels)
		}
	}

	// Second output: the selected subtitle muxed to a single synced WebVTT file in THIS pass
	// (shares the video's -ss/timestamp rebasing). The web overlay fetches it via
	// subtitles_<index>.vtt and renders it with a zero offset. Named to match the path
	// ServeSubtitleTrack serves and clearSessionSegments clears on seek.
	if webSubtitleRendition {
		syncedVTTPath := filepath.Join(session.OutputDir, fmt.Sprintf("subtitles_%d.vtt", webSubtitleAbsIndex))
		args = append(args,
			"-map", fmt.Sprintf("0:%d", webSubtitleAbsIndex),
			"-c:s", "webvtt",
			"-flush_packets", "1",
			"-f", "webvtt",
			syncedVTTPath,
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

				// Detect output directory missing errors (unexplained directory deletion)
				// This helps debug the mysterious directory deletion issue
				if strings.Contains(msg, "No such file or directory") && strings.Contains(msg, session.OutputDir) {
					log.Printf("[hls] CRITICAL: session %s output directory appears to be missing! dir=%s", session.ID, session.OutputDir)
					// Check if directory actually exists
					if _, statErr := os.Stat(session.OutputDir); os.IsNotExist(statErr) {
						log.Printf("[hls] CONFIRMED: session %s output directory was deleted by external process! dir=%s", session.ID, session.OutputDir)
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

				// Check for startup timeout when no segments have been requested yet
				if segmentCount == 0 {
					var shouldKill bool
					var reason string

					// Check if FFmpeg has generated any segments
					highestGenerated := m.findHighestSegmentNumber(session)

					if highestGenerated < 0 && sessionAge > hlsStartupTimeout {
						// No segments generated after 30s - FFmpeg is truly stuck
						shouldKill = true
						reason = fmt.Sprintf("no segments generated after %v", sessionAge.Round(time.Second))
					} else if session.PrequeueType == "details" && sessionAge > hlsDetailsPrequeueTimeout {
						// Details page prequeue - user opened details but didn't play within 2 min
						shouldKill = true
						reason = fmt.Sprintf("details prequeue not started after %v", sessionAge.Round(time.Second))
					}
					// Next episode prequeue: no timeout - throttle limits buffering, cleanup handles abandonment

					if shouldKill {
						log.Printf("[hls] session %s: STARTUP_TIMEOUT - %s", session.ID, reason)

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

				// Find segment files on disk (check both .m4s and .ts)
				segmentFiles, _ := filepath.Glob(filepath.Join(outputDir, "segment*.m4s"))
				if len(segmentFiles) == 0 {
					segmentFiles, _ = filepath.Glob(filepath.Join(outputDir, "segment*.ts"))
				}

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
	if currentPid != thisPid {
		// Either superseded by a new FFmpeg (currentPid != 0) or reset during seek (currentPid == 0)
		log.Printf("[hls] session %s: FFmpeg (pid=%d) superseded (current pid=%d), skipping completion handling",
			session.ID, thisPid, currentPid)
		return nil
	}

	session.mu.RLock()
	stopped := session.Stopped
	session.mu.RUnlock()
	if stopped {
		log.Printf("[hls] session %s: FFmpeg stopped by explicit cleanup, skipping recovery", session.ID)
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
		session.FinalSegmentCount = -1 // Reset since we're restarting transcoding
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
		session.FinalSegmentCount = -1 // Reset since we're restarting transcoding
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
		session.CreatedAt = time.Now()                   // Reset so startup timeout doesn't immediately fire
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

	session.mu.RLock()
	idleTriggered := session.IdleTimeoutTriggered
	hardwareFallbackAttempted := session.HardwareFallbackAttempted
	session.mu.RUnlock()

	// A tiny probe can succeed while the real graph fails because of source
	// dimensions/pixel format, device loss, or exhausted driver sessions. If
	// hardware fails before producing any media, retry this session once with a
	// fully software encode/tone-map plan and temporarily quarantine the
	// hardware choice for subsequent sessions.
	if useWebEncodePlan && shouldFallbackHardwareEncode(
		err, ctx.Err(), idleTriggered, inputWasErrored, webEncodePlan, actualSegments, hardwareFallbackAttempted,
	) {
		log.Printf("[hls] session %s: hardware encoder %s failed before producing a segment; retrying with software encoding",
			session.ID, webEncodePlan.Kind)
		m.markHWAccelFailed(webEncodePlan.Kind)
		files, _ := filepath.Glob(filepath.Join(session.OutputDir, "*"))
		for _, file := range files {
			_ = os.Remove(file)
		}
		session.mu.Lock()
		session.FFmpegCmd = nil
		session.FFmpegPID = 0
		session.Completed = false
		session.FinalSegmentCount = -1
		session.SegmentsCreated = 0
		session.TonemappedToSDR = false
		session.HardwareFallbackAttempted = true
		session.forceSoftwareEncode = true
		session.CreatedAt = time.Now()
		session.LastSegmentRequest = time.Now()
		session.mu.Unlock()
		return m.startTranscoding(ctx, session, cachedForceAAC)
	}

	session.mu.Lock()
	session.Completed = true
	session.FinalSegmentCount = highestSegment // Track actual highest segment created
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

	now := time.Now()
	session.mu.Lock()
	session.LastSegmentRequest = now
	playbackPosition := session.PlaybackPosition
	hasPlaybackPosition := false

	// If frontend reports playback time, use it to update playback tracking for rate limiting and cleanup
	if timeStr := r.URL.Query().Get("time"); timeStr != "" {
		if playbackTime, err := strconv.ParseFloat(timeStr, 64); err == nil && playbackTime >= 0 {
			playbackPosition = playbackTime
			hasPlaybackPosition = true
			session.PlaybackPosition = playbackTime
			session.PlaybackUpdatedAt = now
			// For warm starts, the frontend reports absolute media time but HLS segments start from 0
			// Adjust for StartOffset to get the actual HLS segment number
			hlsTime := playbackTime - session.StartOffset
			if hlsTime < 0 {
				hlsTime = 0
			}
			// Calculate segment number from HLS stream time
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
	if paused, ok := parseOptionalBoolQuery(r, "paused"); ok {
		session.PlaybackPaused = paused
		session.PlaybackUpdatedAt = now
	}
	if buffering, ok := parseOptionalBoolQuery(r, "buffering"); ok {
		session.PlaybackBuffering = buffering
		session.PlaybackUpdatedAt = now
	}
	if ended, ok := parseOptionalBoolQuery(r, "ended"); ok {
		session.PlaybackEnded = ended
		session.PlaybackUpdatedAt = now
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
	profileID := session.ProfileID
	metadata := session.MediaMetadata
	paused := session.PlaybackPaused
	buffering := session.PlaybackBuffering
	ended := session.PlaybackEnded
	// The internal stream path this session transcodes from. Consumers that have
	// to reach the source again — the p2p auto-seeder re-resolves it to a current
	// URL — cannot recover it from anywhere else on this request.
	sourcePath := session.Path
	session.mu.Unlock()

	// A receiver that accepted the load and then stalled produces no further
	// segment fetches, so the sender's keepalive is the only clock that can
	// notice the silence.
	m.noteCastKeepalive(session, now)

	if hasPlaybackPosition {
		m.mu.RLock()
		observer := m.playbackObserver
		m.mu.RUnlock()
		if observer != nil && metadata.MediaType != "live" {
			update := enrichPlaybackUpdateFromStream(models.PlaybackProgressUpdate{
				MediaType:         metadata.MediaType,
				ItemID:            metadata.ItemID,
				Position:          playbackPosition,
				Duration:          duration,
				Timestamp:         now,
				IsPaused:          paused,
				IsBuffering:       buffering,
				PlaybackEnded:     ended,
				PlaybackSessionID: "hls:" + sessionID,
				SourcePath:        sourcePath,
			}, metadata)
			percent := 0.0
			if duration > 0 {
				percent = (playbackPosition / duration) * 100
			}
			go observer.HandlePlaybackUpdate(profileID, update, percent)
		}
	}

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

func parseOptionalBoolQuery(r *http.Request, key string) (bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return false, false
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return value, true
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
	playbackTarget := session.PlaybackTarget
	subtitleTrackIndex := session.SubtitleTrackIndex
	youtubeVideoURL := session.YouTubeVideoURL
	youtubeAudioURL := session.YouTubeAudioURL
	youtubeProxyURL := session.YouTubeProxyURL
	session.mu.RUnlock()

	if requestedTarget := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target"))); requestedTarget != "" {
		playbackTarget = requestedTarget
	}
	if requestedSubtitleTrack := strings.TrimSpace(r.URL.Query().Get("subtitleTrack")); requestedSubtitleTrack != "" {
		if parsed, err := strconv.Atoi(requestedSubtitleTrack); err == nil && parsed >= -1 {
			subtitleTrackIndex = parsed
		}
	}

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

	// Wait briefly for FFmpeg to stop (reduced from 50ms)
	time.Sleep(25 * time.Millisecond)

	// Clear all existing segments since they're at the old time offset
	if err := m.clearSessionSegments(session); err != nil {
		log.Printf("[hls] session %s: warning: failed to clear segments for seek: %v", sessionID, err)
	}

	// Skip keyframe probing - FFmpeg will find the nearest keyframe automatically with -noaccurate_seek
	// Since subtitles are extracted in the same FFmpeg pipeline with the same -ss, they'll be in sync.
	// Actual start offset will be parsed from fMP4 tfdt box after first segment is ready.
	log.Printf("[hls] session %s: seek to %.3fs (skipping keyframe probe for faster seek)", sessionID, targetTime)

	// Reset session state for the new seek position
	session.mu.Lock()
	session.FFmpegCmd = nil
	session.FFmpegPID = 0
	session.Completed = false
	session.FinalSegmentCount = -1         // Reset since we're restarting transcoding from new position
	session.StartOffset = targetTime       // User's new position (for frontend display)
	session.TranscodingOffset = targetTime // FFmpeg will seek to nearest keyframe
	session.ActualStartOffset = targetTime // Will be updated from fMP4 tfdt after first segment
	session.SeekGeneration++
	session.CreatedAt = time.Now()
	session.LastSegmentRequest = time.Now()
	session.SegmentsCreated = 0
	session.MinSegmentRequested = -1
	session.MaxSegmentRequested = -1
	session.LastPlaybackSegment = 0
	session.EarliestBufferedSegment = 0
	session.PlaybackTarget = playbackTarget
	session.SubtitleTrackIndex = subtitleTrackIndex
	session.RecoveryAttempts = 0   // Reset recovery attempts for new seek position
	session.SeekInProgress = false // Clear seek flag now that we're starting fresh
	cachedForceAAC := session.forceAAC
	session.mu.Unlock()

	// Create a new context for the restarted transcoding
	newCtx, newCancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.Cancel = newCancel
	session.mu.Unlock()

	// Start transcoding from the new offset in background.
	if playbackTarget == "youtube" && youtubeVideoURL != "" && youtubeAudioURL != "" {
		go func() {
			if err := m.startYouTubeTranscoding(newCtx, session, youtubeVideoURL, youtubeAudioURL, youtubeProxyURL); err != nil {
				if errors.Is(err, context.Canceled) {
					log.Printf("[hls-youtube] session %s: seek transcoding cancelled", sessionID)
					return
				}
				log.Printf("[hls-youtube] session %s: seek transcoding failed: %v", sessionID, err)
				session.mu.Lock()
				session.Completed = true
				session.FatalError = err.Error()
				session.FatalErrorTime = time.Now()
				session.mu.Unlock()
			}
		}()
	} else {
		go func() {
			if err := m.startTranscoding(newCtx, session, cachedForceAAC); err != nil {
				log.Printf("[hls] session %s: seek transcoding failed: %v", sessionID, err)
				session.mu.Lock()
				session.Completed = true
				session.mu.Unlock()
			}
		}()
	}

	// Start background keyframe probe for subtitle sync correction
	// This runs in parallel and updates ActualStartOffset when done
	// Frontend will pick up the correction on next keepalive poll
	session.mu.RLock()
	sessionPath := session.Path
	probeData := session.ProbeData
	probePlaybackTarget := session.PlaybackTarget
	probeSubtitleTrack := session.SubtitleTrackIndex
	probeSubtitleStreams := []subtitleStreamInfo(nil)
	if probeData != nil {
		probeSubtitleStreams = probeData.SubtitleStreams
	}
	session.mu.RUnlock()

	if shouldUseAccurateRequestedSeekForWebSubtitle(probePlaybackTarget, probeData, probeSubtitleStreams, probeSubtitleTrack) {
		log.Printf("[hls] session %s: skipping background keyframe probe for accurate web video+subtitle seek", sessionID)
	} else {
		go func() {
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer probeCancel()

			keyframePos := m.probeKeyframePosition(probeCtx, sessionPath, targetTime)
			delta := keyframePos - targetTime

			session.mu.Lock()
			session.ActualStartOffset = keyframePos
			session.mu.Unlock()

			log.Printf("[hls] session %s: background keyframe probe complete: requested=%.3fs actual=%.3fs delta=%.3fs",
				sessionID, targetTime, keyframePos, delta)
		}()
	}

	// Wait for the playlist file to be created before returning
	// This prevents the player from trying to load a non-existent playlist
	waitStart := time.Now()
	if ready, size := m.waitForPlaylistReady(session, 10*time.Second); ready {
		log.Printf("[hls] session %s: playlist ready after %v (%d bytes)", sessionID, time.Since(waitStart), size)
	} else {
		log.Printf("[hls] session %s: warning: timed out waiting for playlist after %v", sessionID, time.Since(waitStart))
	}

	// Build playlist URL (without /api/ prefix - frontend adds it)
	playlistURL := m.buildSessionPlaylistURL(session)

	// NOTE: We skip keyframe probing for faster seeks. Since we use -start_at_zero,
	// the fMP4 tfdt box contains 0 (not the actual keyframe position), so parsing it
	// would give wrong results. Instead, we report ActualStartOffset = targetTime
	// and KeyframeDelta = 0. The actual keyframe position may differ by a few hundred
	// milliseconds, but this is acceptable for subtitle sync and much faster than probing.
	log.Printf("[hls] session %s: seek completed, requested=%.2fs (keyframe probe skipped for speed)",
		sessionID, targetTime)

	response := SeekResponse{
		SessionID:         sessionID,
		StartOffset:       targetTime,
		ActualStartOffset: targetTime, // Use requested time (no probe = no exact keyframe info)
		KeyframeDelta:     0,          // Unknown without probe, assume negligible
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

	session.subtitleExtractionMu.Lock()
	activeSubtitleExtractions := len(session.subtitleExtractCancelFunc)
	for _, cancel := range session.subtitleExtractCancelFunc {
		cancel()
	}
	session.subtitleExtractSeq++
	session.subtitleExtracting = make(map[int]bool)
	session.subtitleExtractCancelFunc = make(map[int]context.CancelFunc)
	session.subtitleExtractIDs = make(map[int]int64)
	session.subtitleExtractOffsets = make(map[int]float64)
	session.subtitleExtractionMu.Unlock()

	// Remove all segment files (.ts, .m4s) and VTT subtitle files
	// VTT files MUST be cleared on seek to prevent stale subtitle timing
	// The web subtitle endpoint will regenerate VTT from the new seek position on demand.
	patterns := []string{
		filepath.Join(outputDir, "segment*.ts"),
		filepath.Join(outputDir, "segment*.m4s"),
		filepath.Join(outputDir, "init.mp4"),
		filepath.Join(outputDir, "stream.m3u8"),
		// Includes the same-pass synced subtitles_<index>.vtt, regenerated from the new offset.
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

	log.Printf("[hls] session %s: cleared %d segment files for seek, canceled %d subtitle extractions", session.ID, removeCount, activeSubtitleExtractions)
	return nil
}

// HLSSessionStatus represents the status of an HLS session for frontend polling
type HLSSessionStatus struct {
	SessionID                 string  `json:"sessionId"`
	Status                    string  `json:"status"` // "active", "completed", "error"
	FatalError                string  `json:"fatalError,omitempty"`
	FatalErrorTime            int64   `json:"fatalErrorTime,omitempty"` // Unix timestamp
	Duration                  float64 `json:"duration,omitempty"`
	SegmentsCreated           int     `json:"segmentsCreated"`
	MaxSegmentRequested       int     `json:"maxSegmentRequested"` // Highest segment requested by player
	Paused                    bool    `json:"paused"`              // True if FFmpeg is paused (rate limited)
	BitstreamErrors           int     `json:"bitstreamErrors"`
	HDRMetadataDisabled       bool    `json:"hdrMetadataDisabled"`
	DVDisabled                bool    `json:"dvDisabled"`
	RecoveryAttempts          int     `json:"recoveryAttempts"`
	VideoEncoder              string  `json:"videoEncoder,omitempty"`
	ToneMapper                string  `json:"toneMapper,omitempty"`
	HardwareEncode            bool    `json:"hardwareEncode"`
	HardwareFallbackAttempted bool    `json:"hardwareFallbackAttempted"`
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
		SessionID:                 session.ID,
		Duration:                  session.Duration,
		SegmentsCreated:           session.SegmentsCreated,
		MaxSegmentRequested:       session.MaxSegmentRequested,
		Paused:                    session.Paused,
		BitstreamErrors:           session.BitstreamErrors,
		HDRMetadataDisabled:       session.HDRMetadataDisabled,
		DVDisabled:                session.DVDisabled,
		RecoveryAttempts:          session.RecoveryAttempts,
		VideoEncoder:              session.VideoEncoder,
		ToneMapper:                session.ToneMapper,
		HardwareEncode:            session.HardwareEncode,
		HardwareFallbackAttempted: session.HardwareFallbackAttempted,
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

// resolveSegmentExt reports the extension the transcode plan chose for this session's segments.
//
// The recorded value is the truth: the plan wrote it down when it built the FFmpeg arguments.
// When it is missing, because the playlist is served before transcoding starts, the playlist
// lines are the next best source, since they name segments that were really written. The lines
// are compared trimmed so the answer does not depend on the writer's line ending.
func resolveSegmentExt(session *HLSSession, playlistLines []string) string {
	session.mu.RLock()
	recorded := session.SegmentExt
	session.mu.RUnlock()
	if recorded != "" {
		return recorded
	}
	for _, line := range playlistLines {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "segment") && strings.HasSuffix(name, ".ts") {
			return ".ts"
		}
	}
	return ".m4s"
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
	m.noteCastReceiverPlaylist(session, r)

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
			time.Sleep(25 * time.Millisecond)
			continue
		} else {
			log.Printf("[hls] failed to stat playlist for session %s: %v", sessionID, statErr)
			http.Error(w, "playlist not ready", http.StatusInternalServerError)
			return
		}
	}

	// Read the playlist file. Do not serve an initial empty playlist (header but
	// no media segments yet): native HLS clients (Safari) stall on it and never
	// re-poll. The window between FFmpeg starting and the first segment landing
	// makes this race common for VOD sessions too, not just live.
	var content []byte
	for {
		var err error
		content, err = os.ReadFile(playlistPath)
		if err != nil {
			log.Printf("[hls] failed to read playlist for session %s: %v", sessionID, err)
			http.Error(w, "playlist not ready", http.StatusInternalServerError)
			return
		}
		if playlistHasMediaSegment(content) {
			break
		}
		if time.Now().After(deadline) {
			log.Printf("[hls] playlist has no media segments for session %s after 60s", sessionID)
			http.Error(w, "playlist not ready", http.StatusGatewayTimeout)
			return
		}
		time.Sleep(25 * time.Millisecond)
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
	session.mu.RLock()
	seekGeneration := session.SeekGeneration
	tonemappedToSDR := session.TonemappedToSDR
	stableCastMode := session.usesStableCastTimeline()
	sessionDuration := session.Duration
	session.mu.RUnlock()

	if stableCastMode && sessionDuration > 0 {
		playlistContent = buildStableCastPlaylist(session)
	}

	// Build header tags to inject after #EXTM3U
	var headerTags []string

	// Inject EXT-X-VIDEO-RANGE for HDR/DV content - tells iOS AVPlayer to enable HDR mode
	// Without this, iOS treats HDR content as SDR causing color banding and incorrect display.
	// Skip when the source was tone mapped down to SDR for the web player — the
	// stream is genuinely SDR H.264 and must not be advertised as PQ.
	if (session.HasDV || session.HasHDR) && !tonemappedToSDR && !strings.Contains(playlistContent, "#EXT-X-VIDEO-RANGE") {
		headerTags = append(headerTags, "#EXT-X-VIDEO-RANGE:PQ")
	}

	// Inject EXT-X-START:TIME-OFFSET=0 to tell iOS to start VOD/EVENT playlists
	// from the beginning. Do not inject this for live IPTV playlists; live playback
	// needs to stay near the moving live edge.
	// Only do this for cold starts (StartOffset=0) - warm starts have their own seek logic
	if !session.IsLive && session.StartOffset == 0 && !strings.Contains(playlistContent, "#EXT-X-START") {
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

		// If transcoding completed early (e.g., actual content shorter than metadata duration),
		// limit playlist to actual segments that were created to avoid 404 errors
		session.mu.RLock()
		completed := session.Completed
		finalSegmentCount := session.FinalSegmentCount
		session.mu.RUnlock()
		if completed && finalSegmentCount >= 0 {
			actualTotalSegments := finalSegmentCount + 1
			if actualTotalSegments < totalSegments {
				log.Printf("[hls] session %s: limiting playlist to %d actual segments (expected %d from metadata)",
					sessionID, actualTotalSegments, totalSegments)
				totalSegments = actualTotalSegments
			}
		}

		// Find the highest segment number in the current playlist
		highestExisting := -1
		lines := strings.Split(playlistContent, "\n")
		// The extension the plan actually chose. Deriving it from flags here is what broke direct
		// Cast: it remuxes to MPEG-TS yet matches none of the fMP4 exclusions below, so this
		// synthesised `.m4s` entries for files that were never written and the receiver stalled on
		// the first one. The playlist itself is the fallback, because it names real segments.
		segmentExt := resolveSegmentExt(session, lines)
		for _, line := range lines {
			name := strings.TrimSpace(line)
			if strings.HasPrefix(name, "segment") && strings.HasSuffix(name, segmentExt) {
				// Extract segment number from "segment0.m4s" or "segment0.ts"
				numStr := strings.TrimPrefix(name, "segment")
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

	playlistContent = appendAuthTokenToPlaylist(playlistContent, authToken)
	playlistContent = appendSegmentCacheBusterToPlaylist(playlistContent, seekGeneration)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	w.Write([]byte(playlistContent))

	videoRange := "SDR"
	if (session.HasDV || session.HasHDR) && !tonemappedToSDR {
		videoRange = "PQ"
	}
	log.Printf("[hls] served playlist for session %s, VIDEO-RANGE=%s, auth token=%v", sessionID, videoRange, authToken != "")
}

func playlistHasMediaSegment(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "segment") && (strings.HasSuffix(line, ".ts") || strings.HasSuffix(line, ".m4s")) {
			return true
		}
	}
	return false
}

func buildStableCastPlaylist(session *HLSSession) string {
	session.mu.RLock()
	duration := session.Duration
	startOffset := session.StartOffset
	session.mu.RUnlock()

	effectiveDuration := duration - startOffset
	if effectiveDuration < 0 {
		effectiveDuration = 0
	}
	totalSegments := int(math.Ceil(effectiveDuration / hlsSegmentDuration))

	var playlist strings.Builder
	playlist.WriteString("#EXTM3U\n")
	playlist.WriteString("#EXT-X-VERSION:3\n")
	playlist.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(hlsSegmentDuration))))
	playlist.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	playlist.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	playlist.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")

	for segmentNum := 0; segmentNum < totalSegments; segmentNum++ {
		segmentDuration := hlsSegmentDuration
		segmentEnd := float64(segmentNum+1) * hlsSegmentDuration
		if segmentEnd > effectiveDuration {
			segmentDuration = effectiveDuration - float64(segmentNum)*hlsSegmentDuration
		}
		if segmentDuration < 0.1 {
			continue
		}
		playlist.WriteString(fmt.Sprintf("#EXTINF:%.6f,\nsegment%d.ts\n", segmentDuration, segmentNum))
	}
	playlist.WriteString("#EXT-X-ENDLIST\n")
	return playlist.String()
}

func (m *HLSManager) ServeMasterPlaylist(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.Lock()
	session.LastSegmentRequest = time.Now()
	session.mu.Unlock()
	m.noteCastReceiverPlaylist(session, r)

	if !m.shouldServeMasterPlaylist(session) {
		http.Redirect(w, r, fmt.Sprintf("/video/hls/%s/stream.m3u8", sessionID), http.StatusTemporaryRedirect)
		return
	}

	subtitleStreams := m.getSessionSubtitleStreams(session)
	authToken := r.URL.Query().Get("token")
	if authToken == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			authToken = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	var builder strings.Builder
	builder.WriteString("#EXTM3U\n")
	builder.WriteString("#EXT-X-VERSION:3\n")

	groupID := "subs"
	defaultTrackName := ""
	defaultTrackLanguage := ""
	defaultTrackIndex := -1
	for _, stream := range subtitleStreams {
		selected := session.SubtitleTrackIndex >= 0 && stream.Index == session.SubtitleTrackIndex
		defaultAttr := "NO"
		autoSelectAttr := "NO"
		if selected {
			defaultAttr = "YES"
			autoSelectAttr = "YES"
			defaultTrackIndex = stream.Index
		}

		name := strings.TrimSpace(stream.Title)
		if name == "" {
			name = sanitizeHLSLanguage(stream.Language)
		}
		if selected {
			defaultTrackName = name
			defaultTrackLanguage = sanitizeHLSLanguage(stream.Language)
		}

		builder.WriteString(fmt.Sprintf(
			"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"%s\",NAME=\"%s\",LANGUAGE=\"%s\",AUTOSELECT=%s,DEFAULT=%s,FORCED=%s,URI=\"subtitle-%d.m3u8\"\n",
			groupID,
			sanitizeHLSName(name),
			sanitizeHLSLanguage(stream.Language),
			autoSelectAttr,
			defaultAttr,
			map[bool]string{true: "YES", false: "NO"}[stream.IsForced],
			stream.Index,
		))
	}

	builder.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,CLOSED-CAPTIONS=NONE,SUBTITLES=\"%s\"\n", 8000000, groupID))
	builder.WriteString("stream.m3u8\n")

	log.Printf("[hls] session %s: master playlist subtitle selection requested=%d defaultIndex=%d defaultName=%q defaultLanguage=%q totalTextTracks=%d",
		sessionID, session.SubtitleTrackIndex, defaultTrackIndex, defaultTrackName, defaultTrackLanguage, len(subtitleStreams))

	playlistContent := appendAuthTokenToPlaylist(builder.String(), authToken)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	_, _ = w.Write([]byte(playlistContent))
}

func (m *HLSManager) ServeSubtitlePlaylist(w http.ResponseWriter, r *http.Request, sessionID string, subtitleTrack int) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.Lock()
	session.LastSegmentRequest = time.Now()
	session.mu.Unlock()

	subtitleStreams := m.getSessionSubtitleStreams(session)
	var selectedStream *subtitleStreamInfo
	for i := range subtitleStreams {
		if subtitleStreams[i].Index == subtitleTrack {
			selectedStream = &subtitleStreams[i]
			break
		}
	}
	if selectedStream == nil {
		http.Error(w, "subtitle track not found", http.StatusNotFound)
		return
	}

	duration := session.Duration - session.TranscodingOffset
	if duration <= 0 {
		duration = session.Duration
	}
	if duration <= 0 {
		duration = hlsSegmentDuration
	}

	authToken := r.URL.Query().Get("token")
	if authToken == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			authToken = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	targetDuration := int(math.Ceil(duration))
	if targetDuration < 1 {
		targetDuration = 1
	}

	playlistContent := fmt.Sprintf(
		"#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:%.3f,\nsubtitles-%d.vtt?reload=%d\n",
		targetDuration,
		duration,
		subtitleTrack,
		time.Now().UnixNano(),
	)
	playlistContent = appendAuthTokenToPlaylist(playlistContent, authToken)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	_, _ = w.Write([]byte(playlistContent))
}

func (m *HLSManager) restartCastTranscodingForSegment(session *HLSSession, requestedSegment int) {
	highestAvailable := m.findHighestSegmentNumber(session)
	plan, shouldRestart := castSegmentRestartPlan(session, requestedSegment, highestAvailable)
	if !shouldRestart {
		return
	}

	segmentPath := filepath.Join(session.OutputDir, fmt.Sprintf("segment%d.ts", requestedSegment))
	if _, err := os.Stat(segmentPath); err == nil {
		return
	}

	session.mu.Lock()
	// Another segment request may already have moved the active run close
	// enough to satisfy this request sequentially.
	if !session.Completed &&
		((requestedSegment >= session.SegmentStartNumber &&
			requestedSegment <= session.SegmentStartNumber+castOnDemandLeadSegments) ||
			(session.LastSegmentServed >= 0 &&
				requestedSegment > session.LastSegmentServed &&
				requestedSegment <= session.LastSegmentServed+castOnDemandLeadSegments)) {
		session.mu.Unlock()
		return
	}

	log.Printf("[hls] session %s: stable Cast timeline jump to segment %d (media %.3fs, source %.3fs, highest available=%d)",
		session.ID, plan.SegmentStartNumber, plan.OutputTimestampOffset, plan.TranscodingOffset, highestAvailable)
	session.mu.Unlock()

	m.restartCastTranscodingAt(session, plan)
}

// restartCastTranscodingAt replaces the running FFmpeg with one that resumes
// the stable Cast timeline at plan.SegmentStartNumber. Callers own the decision
// (timeline jump, quality change); this only performs the swap.
func (m *HLSManager) restartCastTranscodingAt(session *HLSSession, plan castSegmentRestart) {
	session.mu.Lock()
	session.SeekInProgress = true
	if session.Cancel != nil {
		session.Cancel()
	}
	session.FFmpegCmd = nil
	session.FFmpegPID = 0
	session.Completed = false
	session.LastSegmentServed = -1
	session.FinalSegmentCount = -1
	session.TranscodingOffset = plan.TranscodingOffset
	session.SegmentStartNumber = plan.SegmentStartNumber
	session.CreatedAt = time.Now()
	session.LastSegmentRequest = time.Now()
	session.RecoveryAttempts = 0
	cachedForceAAC := session.forceAAC
	youtubeVideoURL := session.YouTubeVideoURL
	youtubeAudioURL := session.YouTubeAudioURL
	youtubeProxyURL := session.YouTubeProxyURL
	session.mu.Unlock()

	// Let the cancelled process release its output files before the replacement
	// process writes the same stable playlist.
	time.Sleep(25 * time.Millisecond)

	newCtx, newCancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.Cancel = newCancel
	session.SeekInProgress = false
	session.mu.Unlock()

	go func(segmentStart int) {
		var err error
		if youtubeVideoURL != "" && youtubeAudioURL != "" {
			err = m.startYouTubeTranscoding(newCtx, session, youtubeVideoURL, youtubeAudioURL, youtubeProxyURL)
		} else {
			err = m.startTranscoding(newCtx, session, cachedForceAAC)
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}

		log.Printf("[hls] session %s: stable Cast segment restart failed: %v", session.ID, err)
		session.mu.Lock()
		if session.SegmentStartNumber == segmentStart {
			session.Completed = true
			session.FatalError = err.Error()
			session.FatalErrorTime = time.Now()
		}
		session.mu.Unlock()
	}(plan.SegmentStartNumber)
}

// isChromecastReceiverRequest reports whether a segment request came from the
// Cast receiver itself rather than the sender app warming the backlog. Only the
// receiver's pull rate says anything about the link that has to sustain
// playback.
func isChromecastReceiverRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(r.Header.Get("User-Agent"), "CrKey")
}

// noteCastSegmentDelivery watches how fast the Cast receiver is actually
// pulling segments and drops a 1080p compatibility transcode to 720p once the
// link has proven it cannot keep up. The switch happens at the next segment the
// receiver will ask for, so the stable Cast timeline stays intact.
func (m *HLSManager) noteCastSegmentDelivery(session *HLSSession, r *http.Request, servedSegment int, segmentBytes int64, serveDuration time.Duration) {
	if session == nil || !session.usesStableCastTimeline() || !isChromecastReceiverRequest(r) {
		return
	}

	session.mu.Lock()
	if servedSegment < session.SegmentStartNumber+castSlowServeWarmupSegments {
		session.castSlowServes = 0
		session.mu.Unlock()
		return
	}
	if !castServeIsSlow(serveDuration, segmentBytes) {
		session.castSlowServes = 0
		session.mu.Unlock()
		return
	}
	session.castSlowServes++
	slowStreak := session.castSlowServes
	capHeight := session.CastMaxHeight
	if !castShouldDropToFallbackHeight(slowStreak, capHeight) {
		session.mu.Unlock()
		log.Printf("[hls] session %s: slow Cast segment delivery %d/%d (segment=%d size=%d serve_time=%v)",
			session.ID, slowStreak, castSlowServeStreak, servedSegment, segmentBytes, serveDuration)
		return
	}
	session.CastMaxHeight = legacyCastHDMaxHeight
	session.castSlowServes = 0
	duration := session.Duration
	startOffset := session.StartOffset
	session.mu.Unlock()

	restartSegment := servedSegment + 1
	timelineOffset := float64(restartSegment) * hlsSegmentDuration
	transcodingOffset := startOffset + timelineOffset
	if duration > 0 && transcodingOffset >= duration {
		log.Printf("[hls] session %s: slow Cast link near end of stream; keeping current quality", session.ID)
		return
	}

	log.Printf("[hls] session %s: Cast receiver cannot sustain 1080p (serve_time=%v for %d bytes); dropping to 720p from segment %d",
		session.ID, serveDuration, segmentBytes, restartSegment)
	m.restartCastTranscodingAt(session, castSegmentRestart{
		SegmentStartNumber:    restartSegment,
		TranscodingOffset:     transcodingOffset,
		OutputTimestampOffset: timelineOffset,
	})
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
	parsedSegmentOK := false
	if _, err := fmt.Sscanf(segmentName, "segment%d.", &segmentNum); err == nil {
		parsedSegmentOK = true
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

	// Grade the receiver's own pull rate against what this session is asking it
	// to decode. Nothing here contacts the device; it only reads the timeline
	// the receiver produces by fetching what the user asked to play.
	m.noteCastReceiverFetch(session, r, segmentName)

	log.Printf("[hls] segment request #%d: session=%s segment=%s", requestCount, sessionID, segmentName)

	// Validate segment name to prevent path traversal
	if strings.Contains(segmentName, "..") || strings.Contains(segmentName, "/") {
		log.Printf("[hls] invalid segment name: %s", segmentName)
		http.Error(w, "invalid segment name", http.StatusBadRequest)
		return
	}

	segmentPath := filepath.Join(session.OutputDir, segmentName)
	if session.usesStableCastTimeline() {
		if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
			m.restartCastTranscodingForSegment(session, segmentNum)
		}
	}

	// Wait for segment to be created.
	// Live native transmux: ~15s for first temp_file rename.
	// VOD re-encode: ~30s. Stable Cast jumps may need a fresh FFmpeg start: ~30s.
	maxWaitIters := 1200 // ~30s
	if session.IsLive {
		maxWaitIters = 600 // ~15s
	}
	if session.usesStableCastTimeline() {
		maxWaitIters = 1200 // cast restart / jump headroom
	}
	waitStart := time.Now()
	segmentReady := false
	var segmentSize int64
	for i := 0; i < maxWaitIters; i++ {
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

		// Check if transcoding has completed - if so, segment will never be created
		// This prevents waiting the full timeout for segments that won't exist (e.g.,
		// when FFmpeg exits early due to shorter actual content than metadata duration)
		if i%20 == 0 { // Check every ~500ms to avoid lock contention
			session.mu.RLock()
			completed := session.Completed
			session.mu.RUnlock()
			if completed {
				log.Printf("[hls] session %s: transcoding completed, segment %s will not be created",
					sessionID, segmentName)
				break
			}
		}

		time.Sleep(25 * time.Millisecond) // Reduced from 100ms for faster segment delivery
	}

	waitDuration := time.Since(waitStart)
	if !segmentReady {
		log.Printf("[hls] SEGMENT_TIMEOUT: session=%s segment=%s waited=%v isLive=%v target=%q %s | %s",
			sessionID, segmentName, waitDuration, session.IsLive, session.PlaybackTarget,
			summarizeLiveSegmentDir(session.OutputDir),
			summarizeLivePlaylistState(session.OutputDir))
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
	} else if strings.HasSuffix(segmentName, ".m3u8") {
		// e.g. stream_vtt.m3u8 (FFmpeg-generated subtitle rendition playlist) hits this handler
		contentType = "application/vnd.apple.mpegurl"
	}

	w.Header().Set("Content-Type", contentType)
	if strings.HasSuffix(segmentName, ".vtt") || strings.HasSuffix(segmentName, ".webvtt") || strings.HasSuffix(segmentName, ".m3u8") {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "no-store, max-age=0")
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Accept-Ranges", "bytes")

	// Set Content-Length explicitly for fMP4 segments (required by iOS/tvOS)
	w.Header().Set("Content-Length", strconv.FormatInt(segmentSize, 10))

	// Track bytes served
	session.mu.Lock()
	session.BytesStreamed += segmentSize
	session.mu.Unlock()

	// First playback frame: when the first media segment response is about to
	// be written, snapshot t4 and emit the click→first-frame sample. Only
	// actual segment requests (segmentN.ext) count — init.mp4 / captions / VTT
	// playlist fetches do not constitute a playback frame. Emits exactly once
	// per session (FirstSegmentSentAt transitions zero → set).
	emitLatency := false
	if parsedSegmentOK {
		session.mu.Lock()
		if session.FirstSegmentSentAt.IsZero() {
			session.FirstSegmentSentAt = time.Now()
			emitLatency = true
		}
		sentAt := session.FirstSegmentSentAt
		readyAt := session.FirstSegmentTime
		createdAt := session.StreamStartTime
		sessionID := session.ID
		prequeueID := session.PrequeueID
		serviceType := session.ServiceType
		serviceProvider := session.ServiceProvider
		session.mu.Unlock()
		if emitLatency && m.latencyTracker != nil && !sentAt.IsZero() {
			requestedAt, prequeueReadyAt := m.latencyTracker.PrequeueTimes(prequeueID)
			m.latencyTracker.Record(PlaybackLatencySample{
				PrequeueID:          prequeueID,
				SessionID:           sessionID,
				ServiceType:         serviceType,
				ServiceProvider:     serviceProvider,
				ClientRequestedAt:   requestedAt,
				PrequeueReadyAt:     prequeueReadyAt,
				HLSSessionCreatedAt: createdAt,
				FirstSegmentReadyAt: readyAt,
				FirstSegmentSentAt:  sentAt,
			})
		}
	}

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

	m.noteCastSegmentDelivery(session, r, servedSegmentNum, segmentSize, serveDuration)

	// Clean up old segments to save disk space.
	// Web live sessions use FFmpeg's delete_segments flag (transcode path).
	// Native live transmux deliberately omits delete_segments (see liveHLSOutputArgs)
	// so we prune served segment files ourselves with a fixed keep-behind window.
	// VOD uses buffer-aware deleteOldSegments.
	if session.IsLive {
		if isNativeLivePlaybackTarget(session.PlaybackTarget) {
			go m.deleteOldLiveTransmuxSegments(session, servedSegmentNum)
		}
	} else {
		go m.deleteOldSegments(session, segmentName)
	}
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

	waitForCue := r.URL.Query().Get("wait") == "1"
	m.ServeSubtitleTrack(w, r, sessionID, requestedTrack, waitForCue)
}

func (m *HLSManager) ServeSubtitleTrack(w http.ResponseWriter, r *http.Request, sessionID string, requestedTrack int, waitForCue bool) {
	session, exists := m.GetSession(sessionID)
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// All subtitle tracks are now extracted upfront to subtitles_<streamIndex>.vtt
	// This naming is consistent for both initially selected and switched tracks
	vttPath := filepath.Join(session.OutputDir, fmt.Sprintf("subtitles_%d.vtt", requestedTrack))

	// For the same-pass synced subtitle (web overlay), the main transcode writes this exact file
	// in the video's timeline. Skip the separate on-demand extraction, which would produce an
	// out-of-sync VTT and race with the transcode's output.
	session.mu.RLock()
	syncedSamePass := session.UsesSubtitleRendition && requestedTrack == session.SubtitleTrackIndex
	syncedSamePassWriting := syncedSamePass && !session.Completed
	session.mu.RUnlock()

	// Check if file exists - for fMP4/DV sessions, all tracks should be pre-extracted
	// If not found, fall back to on-demand extraction (for MPEG-TS or edge cases)
	if _, err := os.Stat(vttPath); os.IsNotExist(err) && !syncedSamePass {
		// Use per-track extraction tracking to prevent duplicates without blocking the session
		// This avoids a deadlock where subtitle extraction holds session.mu while the
		// transcoding pipeline waits for session.mu.RLock at startup
		var (
			extractCtx    context.Context
			extractCancel context.CancelFunc
			extractID     int64
			startExtract  bool
		)

		session.subtitleExtractionMu.Lock()
		if session.subtitleExtracting == nil {
			session.subtitleExtracting = make(map[int]bool)
		}
		if session.subtitleExtractCancelFunc == nil {
			session.subtitleExtractCancelFunc = make(map[int]context.CancelFunc)
		}
		if session.subtitleExtractIDs == nil {
			session.subtitleExtractIDs = make(map[int]int64)
		}
		if session.subtitleExtractOffsets == nil {
			session.subtitleExtractOffsets = make(map[int]float64)
		}
		alreadyExtracting := session.subtitleExtracting[requestedTrack]
		if !alreadyExtracting {
			// Double-check file doesn't exist after acquiring lock
			if _, err := os.Stat(vttPath); os.IsNotExist(err) {
				session.subtitleExtractSeq++
				extractID = session.subtitleExtractSeq
				session.subtitleExtracting[requestedTrack] = true
				extractCtx, extractCancel = context.WithCancel(context.Background())
				session.subtitleExtractCancelFunc[requestedTrack] = extractCancel
				session.subtitleExtractIDs[requestedTrack] = extractID
				startExtract = true
			}
		}
		session.subtitleExtractionMu.Unlock()

		if alreadyExtracting {
			// Another request is already extracting this track, wait and retry
			log.Printf("[hls] subtitle track %d extraction already in progress for session %s, waiting", requestedTrack, sessionID)
		} else if startExtract {
			log.Printf("[hls] starting subtitle track %d extraction on demand for session %s", requestedTrack, sessionID)
			go func() {
				extractErr := m.extractSubtitleTrackToVTT(extractCtx, session, requestedTrack, vttPath)
				session.subtitleExtractionMu.Lock()
				if session.subtitleExtractIDs != nil && session.subtitleExtractIDs[requestedTrack] == extractID {
					delete(session.subtitleExtractCancelFunc, requestedTrack)
					delete(session.subtitleExtracting, requestedTrack)
					delete(session.subtitleExtractIDs, requestedTrack)
				}
				session.subtitleExtractionMu.Unlock()
				if extractErr != nil {
					log.Printf("[hls] subtitle track %d extraction failed for session %s: %v", requestedTrack, sessionID, extractErr)
					return
				}
				log.Printf("[hls] subtitle track %d extraction complete for session %s", requestedTrack, sessionID)
			}()
		}
	}

	// Check if file exists (might not be ready yet or no subtitles selected)
	var (
		stat    os.FileInfo
		content []byte
		err     error
	)
	if waitForCue {
		stat, content, err = waitForSubtitleFile(vttPath, true, 10*time.Second)
	} else {
		stat, content, err = waitForSubtitleFile(vttPath, true, 2*time.Second)
	}
	if os.IsNotExist(err) {
		// Return empty VTT header if file doesn't exist yet
		// This allows the frontend to poll without errors
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if session.subtitleExtractionInProgress(requestedTrack) || syncedSamePass {
			// For synced same-pass subtitles the transcode is still writing the file; signal
			// "extracting" so the overlay polls quickly instead of waiting the long interval.
			w.Header().Set("X-Subtitle-Extracting", "true")
		}
		if syncedSamePass {
			// Muxed in the video's timeline — the overlay must use a zero offset, NOT the
			// separate-extraction start offset (which can leak a stale 0 and break sync).
			w.Header().Set("X-Subtitle-Synced", "true")
		} else if offset, ok := session.subtitleExtractionOffset(requestedTrack); ok {
			w.Header().Set("X-Subtitle-Start-Offset", fmt.Sprintf("%.3f", offset))
		}
		w.Write([]byte("WEBVTT\n\n"))
		return
	} else if err != nil {
		http.Error(w, "failed to check subtitle file", http.StatusInternalServerError)
		return
	}

	// Read the current contents of the VTT file
	// Note: FFmpeg writes progressively, so the file may still be growing
	if content == nil {
		content, err = os.ReadFile(vttPath)
		if err != nil {
			http.Error(w, "failed to read subtitle file", http.StatusInternalServerError)
			return
		}
	}

	// If we read 0 bytes immediately after extraction, retry once
	// (filesystem buffering race condition)
	if len(content) == 0 && stat.Size() > 0 {
		time.Sleep(50 * time.Millisecond) // Reduced from 100ms
		content, _ = os.ReadFile(vttPath)
	}

	// Post-process VTT to merge karaoke character cues (from ASS conversion)
	processedContent := mergeKaraokeCues(string(content))

	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache") // Don't cache since file is growing
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if session.subtitleExtractionInProgress(requestedTrack) || syncedSamePassWriting {
		w.Header().Set("X-Subtitle-Extracting", "true")
	}
	if syncedSamePass {
		w.Header().Set("X-Subtitle-Synced", "true")
	} else if offset, ok := session.subtitleExtractionOffset(requestedTrack); ok {
		w.Header().Set("X-Subtitle-Start-Offset", fmt.Sprintf("%.3f", offset))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(processedContent)))

	w.Write([]byte(processedContent))
	log.Printf("[hls] served subtitles for session %s track %d, size=%d bytes", sessionID, requestedTrack, len(processedContent))
}

func (s *HLSSession) subtitleExtractionOffset(track int) (float64, bool) {
	s.subtitleExtractionMu.Lock()
	defer s.subtitleExtractionMu.Unlock()
	if s.subtitleExtractOffsets == nil {
		return 0, false
	}
	offset, ok := s.subtitleExtractOffsets[track]
	return offset, ok
}

func (s *HLSSession) setSubtitleExtractionOffset(track int, offset float64) {
	s.subtitleExtractionMu.Lock()
	defer s.subtitleExtractionMu.Unlock()
	if s.subtitleExtractOffsets == nil {
		s.subtitleExtractOffsets = make(map[int]float64)
	}
	s.subtitleExtractOffsets[track] = offset
}

func (s *HLSSession) subtitleExtractionInProgress(track int) bool {
	s.subtitleExtractionMu.Lock()
	defer s.subtitleExtractionMu.Unlock()
	return s.subtitleExtracting != nil && s.subtitleExtracting[track]
}

// extractSubtitleTrackToVTT extracts a specific subtitle track to a VTT file on-demand
// This allows switching subtitle tracks without recreating the HLS session
// trackIndex is the absolute ffprobe stream index (same as session.SubtitleTrackIndex)
func (m *HLSManager) extractSubtitleTrackToVTT(ctx context.Context, session *HLSSession, trackIndex int, outputPath string) error {
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
	log.Printf("[hls] using URL for subtitle extraction: %s", requestsecurity.URLForLog(streamURL))

	// Build ffmpeg command to extract subtitle track to VTT
	// If the session has a StartOffset (warm start/seek), we need to:
	// 1. Seek to the start offset so subtitles align with the HLS stream
	// 2. Use -start_at_zero to normalize timestamps to start at 0
	// This matches the behavior of the main transcoding pipeline
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
	}

	// Add input seeking if session has a start offset. Match the main HLS
	// transcoding input seek, not the background keyframe probe result. Some
	// MP4 files have a positive container start_time; FFmpeg includes that
	// lead-in in the HLS media timeline, so sidecar subtitles must start from
	// the same earlier source position and let the web overlay apply the offset.
	seekOffset := session.TranscodingOffset
	if seekOffset <= 0 {
		seekOffset = session.StartOffset
	}
	mediaLeadIn := 0.0
	if session.ProbeData != nil && session.ProbeData.StartTime > 0 {
		mediaLeadIn = session.ProbeData.StartTime
		seekOffset = math.Max(0, seekOffset-mediaLeadIn)
	}
	session.setSubtitleExtractionOffset(trackIndex, seekOffset)
	if seekOffset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", seekOffset))
		log.Printf("[hls] session %s: subtitle extraction using -ss %.3fs for sync (requested was %.3fs, media lead-in %.3fs, actual probe %.3fs)",
			session.ID, seekOffset, session.StartOffset, mediaLeadIn, session.ActualStartOffset)
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
		"-flush_packets", "1",
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
	// Log who is calling cleanup for debugging mysterious directory deletion
	_, file, line, _ := runtime.Caller(1)
	log.Printf("[hls] CleanupSession called for session %s from %s:%d", sessionID, filepath.Base(file), line)

	m.mu.Lock()
	session, exists := m.sessions[sessionID]
	if !exists {
		m.mu.Unlock()
		log.Printf("[hls] CleanupSession: session %s not found, already cleaned up", sessionID)
		return
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	// Mark the session before killing FFmpeg so the transcoding goroutine cannot
	// interpret the forced exit as a recoverable premature completion.
	session.mu.Lock()
	session.Stopped = true
	session.mu.Unlock()

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

	// Stop ccextractor if running (live TV CC extraction)
	session.mu.Lock()
	if session.ccExtractor != nil {
		log.Printf("[hls] stopping ccextractor for session %s", sessionID)
		session.ccExtractor.stop()
		session.ccExtractor = nil
	}
	session.mu.Unlock()

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
		// The transcoding goroutine owns Cmd.Wait and will reap the process.
		// Calling Wait here as well races with that owner and is unsupported.
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
// SampleThroughput refreshes the throughput EWMA for every active HLS session.
// Called by the background throughput sampler so dashboard speeds stay warm even
// when no dashboard is connected.
func (m *HLSManager) SampleThroughput() {
	m.mu.RLock()
	sessions := make([]*HLSSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()

	for _, session := range sessions {
		session.mu.RLock()
		bytes := session.BytesStreamed
		session.mu.RUnlock()
		sampleThroughput(bytes, &session.throughputLastBytes, &session.throughputLastNanos, &session.throughputBps)
	}
}

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

// summarizeLiveSegmentDir reports which segmentN.ts files currently exist under a
// session output dir. Used on SEGMENT_TIMEOUT to distinguish "never generated"
// from "generated then deleted / live edge advanced" without listing every file.
func summarizeLiveSegmentDir(outputDir string) string {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Sprintf("onDisk=err:%v", err)
	}
	var nums []int
	var otherTS int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if m := segmentNumRe.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			nums = append(nums, n)
			continue
		}
		if strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".m4s") {
			otherTS++
		}
	}
	if len(nums) == 0 {
		if otherTS > 0 {
			return fmt.Sprintf("onDisk=0 otherMediaFiles=%d", otherTS)
		}
		return "onDisk=0"
	}
	sort.Ints(nums)
	minN, maxN := nums[0], nums[len(nums)-1]
	// Compact range check: contiguous min..max vs sparse holes.
	expected := maxN - minN + 1
	holes := expected - len(nums)
	return fmt.Sprintf("onDisk=%d min=segment%d max=segment%d holes=%d otherMediaFiles=%d",
		len(nums), minN, maxN, holes, otherTS)
}

// summarizeLivePlaylistState reports media-sequence and the first/last segment
// names currently advertised in stream.m3u8 (if present).
func summarizeLivePlaylistState(outputDir string) string {
	content, err := os.ReadFile(filepath.Join(outputDir, "stream.m3u8"))
	if err != nil {
		return fmt.Sprintf("playlist=err:%v", err)
	}
	mediaSeq := ""
	var segs []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			mediaSeq = strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			continue
		}
		if strings.HasPrefix(line, "segment") && (strings.HasSuffix(line, ".ts") || strings.HasSuffix(line, ".m4s")) {
			// Strip query params if any were written into the on-disk playlist
			if idx := strings.IndexByte(line, '?'); idx >= 0 {
				line = line[:idx]
			}
			segs = append(segs, line)
		}
	}
	if mediaSeq == "" {
		mediaSeq = "?"
	}
	switch len(segs) {
	case 0:
		return fmt.Sprintf("playlistBytes=%d mediaSeq=%s segs=0", len(content), mediaSeq)
	case 1:
		return fmt.Sprintf("playlistBytes=%d mediaSeq=%s segs=1 first=%s", len(content), mediaSeq, segs[0])
	default:
		return fmt.Sprintf("playlistBytes=%d mediaSeq=%s segs=%d first=%s last=%s",
			len(content), mediaSeq, len(segs), segs[0], segs[len(segs)-1])
	}
}

// deleteOldLiveTransmuxSegments removes native live .ts segment files that are far
// behind the playback edge. Native live omits FFmpeg delete_segments so early
// segments stay available for the player's first fetch; without this cleanup a long
// session would accumulate every segment file ever written.
func (m *HLSManager) deleteOldLiveTransmuxSegments(session *HLSSession, justServedSegment int) {
	if session == nil || justServedSegment < 0 {
		return
	}

	session.mu.RLock()
	outputDir := session.OutputDir
	sessionID := session.ID
	maxRequested := session.MaxSegmentRequested
	lastServed := session.LastSegmentServed
	session.mu.RUnlock()

	highWater := justServedSegment
	if lastServed > highWater {
		highWater = lastServed
	}
	if maxRequested > highWater {
		highWater = maxRequested
	}

	cutoff := highWater - liveNativeSegmentKeepBehind
	if cutoff < 0 {
		return
	}

	deletedCount := 0
	// Bound the walk: only scan the keep window trailing edge, not every segment
	// from 0 (O(n) over multi-hour live sessions).
	start := cutoff - liveNativeSegmentKeepBehind
	if start < 0 {
		start = 0
	}
	for i := start; i <= cutoff; i++ {
		path := filepath.Join(outputDir, fmt.Sprintf("segment%d.ts", i))
		if err := os.Remove(path); err == nil {
			deletedCount++
		}
	}
	if deletedCount > 0 {
		log.Printf("[hls] session %s: deleted %d old live transmux segments (highWater=%d, keepBehind=%d, cutoff=%d)",
			sessionID, deletedCount, highWater, liveNativeSegmentKeepBehind, cutoff)
	}
}

// deleteOldSegments removes old VOD segment files to save disk space, only deleting
// segments the player no longer needs (buffer-aware).
func (m *HLSManager) deleteOldSegments(session *HLSSession, justServedSegment string) {
	session.mu.RLock()
	stableCastMode := session.usesStableCastTimeline()
	outputDir := session.OutputDir
	// TESTING: hasDV/hasHDR unused since we always use .m4s
	_ = session.HasDV
	_ = session.HasHDR
	sessionID := session.ID
	earliestBuffered := session.EarliestBufferedSegment
	lastServedSegment := session.LastSegmentServed
	session.mu.RUnlock()
	if stableCastMode {
		// consulting the sender first, so every generated logical segment must
		// remain available for backward seeking.
		return
	}

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

	// Keep recent segments behind the safe point for startup re-reads and short seeks.
	cutoff := safeSegment - hlsSegmentCleanupRetainBehind
	if cutoff < 0 {
		return
	}

	// Delete segments older than cutoff (segments the player has already watched)
	// Try both .m4s (fMP4) and .ts (MPEG-TS) extensions
	deletedCount := 0
	newMinAvailable := cutoff + 1
	for i := 0; i <= cutoff; i++ {
		oldSegment := filepath.Join(outputDir, fmt.Sprintf("segment%d.m4s", i))
		if _, err := os.Stat(oldSegment); os.IsNotExist(err) {
			oldSegment = filepath.Join(outputDir, fmt.Sprintf("segment%d.ts", i))
		}
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
		log.Printf("[hls] session %s: deleted %d old segments (earliestBuffered=%d, lastServed=%d, safeSegment=%d, keeping %d behind, minAvailable=%d)",
			sessionID, deletedCount, earliestBuffered, lastServedSegment, safeSegment, hlsSegmentCleanupRetainBehind, newMinAvailable)
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
	pollInterval := 25 * time.Millisecond // Reduced from 100ms for faster response

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

func (m *HLSManager) SetCastCapabilities(store *castcaps.Store) {
	if m == nil {
		return
	}
	m.castCapsMu.Lock()
	defer m.castCapsMu.Unlock()
	m.castCaps = store
}

// lookupCastCapabilities answers from cache for a receiver address. Session
// creation needs this before an HLSSession exists.
func (m *HLSManager) lookupCastCapabilities(host string) *castcaps.Capabilities {
	if m == nil || strings.TrimSpace(host) == "" {
		return nil
	}
	m.castCapsMu.RLock()
	store := m.castCaps
	m.castCapsMu.RUnlock()
	if store == nil {
		return nil
	}
	return store.Lookup(host)
}

func (m *HLSManager) castCapabilities(session *HLSSession) *castcaps.Capabilities {
	if session == nil {
		return nil
	}
	return m.lookupCastCapabilities(session.CastReceiverHost)
}

// castVariantFingerprint is what a Cast session actually asks its receiver to
// play. It is derived from the ffmpeg arg plan rather than from the source
// file, so it describes the bytes the receiver really has to decode.
type castVariantFingerprint struct {
	// Primary is the variant this session is direct evidence for. A stall is
	// blamed on it and on nothing else.
	Primary castcaps.Variant
	// Implied are variants that sustained playback also proves, because Primary
	// strictly contains them. They are never blamed for a stall.
	Implied []castcaps.Variant
}

func (f castVariantFingerprint) empty() bool { return f.Primary == "" }

// castVariantPlan is the receiver-facing shape of one ffmpeg output.
type castVariantPlan struct {
	Fmp4          bool   // container: fMP4 when true, MPEG-TS when false
	VideoCodec    string // codec the receiver decodes, after any transcode
	AudioCodec    string // codec the receiver decodes, after any transcode
	AudioChannels int    // 0 when audio is copied and the count is unknown
}

// castVariantsForPlan maps an output plan onto the capability matrix. Each
// container has exactly one interesting axis: an fMP4 output is decided by its
// video codec (a receiver that plays fMP4 at all plays H.264 in it), and an
// MPEG-TS output is decided by its audio codec (H.264 in TS is the universal
// Cast baseline). A plan whose deciding axis is unknown - copied audio, whose
// channel count nothing in the probe reports - maps to no variant at all: a
// guess here becomes a cached verdict that silently steers every later session
// against this receiver.
func castVariantsForPlan(plan castVariantPlan) castVariantFingerprint {
	if plan.Fmp4 {
		switch strings.ToLower(strings.TrimSpace(plan.VideoCodec)) {
		case "hevc", "h265", "hev1", "hvc1":
			return castVariantFingerprint{
				Primary: castcaps.VariantHEVCFMP4,
				Implied: []castcaps.Variant{castcaps.VariantFMP4},
			}
		case "h264", "avc", "avc1":
			return castVariantFingerprint{Primary: castcaps.VariantFMP4}
		}
		return castVariantFingerprint{}
	}
	switch strings.ToLower(strings.TrimSpace(plan.AudioCodec)) {
	case "ac3", "ac-3", "eac3", "eac-3":
		return castVariantFingerprint{Primary: castcaps.VariantTSAC3}
	case "aac":
		if plan.AudioChannels > 2 {
			return castVariantFingerprint{
				Primary: castcaps.VariantTSAACMultichannel,
				Implied: []castcaps.Variant{castcaps.VariantTSAACStereo},
			}
		}
		if plan.AudioChannels > 0 {
			return castVariantFingerprint{Primary: castcaps.VariantTSAACStereo}
		}
	}
	return castVariantFingerprint{}
}

const (
	// A receiver that has pulled this much media over this long a wall-clock
	// window is playing it. Anything shorter is indistinguishable from the
	// buffer burst a receiver performs before deciding it cannot decode.
	castProvenMediaSeconds = 20.0
	castProvenElapsed      = 15 * time.Second

	// Accept-then-stall, as measured on real hardware: the receiver takes the
	// playlist and a handful of segments, then goes silent forever with no
	// error while the sender keeps the session alive.
	castStallMaxSegments = 6
	castStallSilence     = 20 * time.Second

	// Without a recent keepalive the sender is gone too, so receiver silence
	// says nothing about what the receiver can decode.
	castStallKeepaliveWindow = 30 * time.Second
)

// castPlaybackObservation is the receiver-side fetch timeline of one cast
// session: what the device pulled, and when.
type castPlaybackObservation struct {
	Now       time.Time
	Alive     bool      // session still running: not stopped, completed, or failed
	Keepalive time.Time // last sender keepalive; zero when none has arrived
	Playlist  bool      // the receiver fetched a media playlist
	First     time.Time // receiver's first segment fetch
	Last      time.Time // receiver's most recent segment fetch
	Segments  int       // distinct segments the receiver fetched
}

// gradeCastPlayback turns a fetch timeline into a capability verdict. It
// answers VerdictUnknown whenever the evidence is ambiguous: a verdict is
// cached and then silently steers every later session on that receiver, so
// saying nothing is always the cheaper mistake.
func gradeCastPlayback(obs castPlaybackObservation) (castcaps.Verdict, string) {
	if obs.Segments <= 0 || obs.First.IsZero() || obs.Last.IsZero() {
		return castcaps.VerdictUnknown, ""
	}

	mediaSeconds := float64(obs.Segments) * hlsSegmentDuration
	elapsed := obs.Last.Sub(obs.First)
	if mediaSeconds >= castProvenMediaSeconds && elapsed > castProvenElapsed {
		return castcaps.VerdictSupported, fmt.Sprintf(
			"receiver fetched %d segments (%.0fs of media) spread over %s",
			obs.Segments, mediaSeconds, elapsed.Round(time.Second))
	}

	if !obs.Alive || !obs.Playlist || obs.Segments >= castStallMaxSegments {
		return castcaps.VerdictUnknown, ""
	}
	if obs.Keepalive.IsZero() || obs.Now.Sub(obs.Keepalive) > castStallKeepaliveWindow {
		return castcaps.VerdictUnknown, ""
	}
	silence := obs.Now.Sub(obs.Last)
	if silence < castStallSilence {
		return castcaps.VerdictUnknown, ""
	}
	return castcaps.VerdictRejected, fmt.Sprintf(
		"receiver took the playlist and %d segment(s) then fetched nothing for %s while the session stayed alive",
		obs.Segments, silence.Round(time.Second))
}

// castRequestIsFromReceiver reports whether an HTTP request came from the Cast
// receiver itself rather than the sender app warming the backlog. Only the
// receiver's own fetches say anything about what it can decode.
func castRequestIsFromReceiver(session *HLSSession, r *http.Request) bool {
	if session == nil || r == nil {
		return false
	}
	session.mu.RLock()
	host := strings.TrimSpace(session.CastReceiverHost)
	castMode := session.CastMode
	session.mu.RUnlock()
	if host == "" || !castMode {
		return false
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if hostOnly, _, err := net.SplitHostPort(remote); err == nil {
		remote = hostOnly
	}
	return remote == host
}

// noteCastReceiverPlaylist records that the receiver pulled a media playlist.
// A stall verdict requires this: a receiver that never even read the playlist
// was never given the chance to reject the stream.
func (m *HLSManager) noteCastReceiverPlaylist(session *HLSSession, r *http.Request) {
	if !castRequestIsFromReceiver(session, r) {
		return
	}
	session.mu.Lock()
	session.castPlaylistFetched = true
	session.mu.Unlock()
}

// noteCastReceiverFetch records one segment fetch by the receiver and regrades
// the session. Fetches are counted at request time, not after a successful
// serve: a request the server could not satisfy is still the receiver asking.
func (m *HLSManager) noteCastReceiverFetch(session *HLSSession, r *http.Request, segmentName string) {
	if !castRequestIsFromReceiver(session, r) {
		return
	}
	var segmentNum int
	if _, err := fmt.Sscanf(segmentName, "segment%d.", &segmentNum); err != nil {
		return
	}

	now := time.Now()
	session.mu.Lock()
	if session.castVariantsGraded {
		session.mu.Unlock()
		return
	}
	if session.castFetchedSegments == nil {
		session.castFetchedSegments = make(map[int]struct{})
	}
	session.castFetchedSegments[segmentNum] = struct{}{}
	if session.castFirstFetch.IsZero() {
		session.castFirstFetch = now
	}
	session.castLastFetch = now
	session.mu.Unlock()

	m.gradeCastSession(session, now)
}

// noteCastKeepalive records that the sender still wants this session, then
// regrades. A stall produces no further segment fetches, so the keepalive is
// the only clock that can ever notice it.
func (m *HLSManager) noteCastKeepalive(session *HLSSession, now time.Time) {
	if session == nil {
		return
	}
	session.mu.Lock()
	skip := session.castVariantsGraded || !session.CastMode || strings.TrimSpace(session.CastReceiverHost) == ""
	if !skip {
		session.castLastKeepalive = now
	}
	session.mu.Unlock()
	if skip {
		return
	}
	m.gradeCastSession(session, now)
}

// gradeCastSession records a verdict for the variants this session exercises,
// at most once per session. Sessions with no receiver address, no cast mode, or
// no recognizable variant are never graded.
func (m *HLSManager) gradeCastSession(session *HLSSession, now time.Time) {
	if m == nil || session == nil {
		return
	}
	m.castCapsMu.RLock()
	store := m.castCaps
	m.castCapsMu.RUnlock()
	if store == nil {
		return
	}

	session.mu.Lock()
	host := strings.TrimSpace(session.CastReceiverHost)
	fingerprint := session.castVariants
	if session.castVariantsGraded || host == "" || !session.CastMode || fingerprint.empty() {
		session.mu.Unlock()
		return
	}
	verdict, evidence := gradeCastPlayback(castPlaybackObservation{
		Now:       now,
		Alive:     !session.Stopped && !session.Completed && session.FatalError == "",
		Keepalive: session.castLastKeepalive,
		Playlist:  session.castPlaylistFetched,
		First:     session.castFirstFetch,
		Last:      session.castLastFetch,
		Segments:  len(session.castFetchedSegments),
	})
	if verdict == castcaps.VerdictUnknown {
		session.mu.Unlock()
		return
	}
	session.castVariantsGraded = true
	// The timeline has done its job and the fetch set only grows from here.
	session.castFetchedSegments = nil
	sessionID := session.ID
	session.mu.Unlock()

	variants := []castcaps.Variant{fingerprint.Primary}
	if verdict == castcaps.VerdictSupported {
		variants = append(variants, fingerprint.Implied...)
	}
	for _, variant := range variants {
		store.Record(host, variant, verdict)
		log.Printf("[hls] session %s: receiver %s recorded %s=%s (%s)",
			sessionID, host, variant, verdict, evidence)
	}
}

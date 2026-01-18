package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"novastream/config"
	"novastream/internal/integration"
	"novastream/models"
	"novastream/services/streaming"

	"github.com/gorilla/mux"
)

var transmuxableExtensions = map[string]struct{}{
	".mkv":  {},
	".ts":   {},
	".m2ts": {},
	".mts":  {},
	".avi":  {},
	".mpg":  {},
	".mpeg": {},
}

var copyableAudioCodecs = map[string]struct{}{
	"aac":  {},
	"ac3":  {},
	"eac3": {},
	"mp3":  {},
}

var browserFriendlyMp4VideoCodecs = map[string]struct{}{
	"h264":  {},
	"avc":   {},
	"avc1":  {},
	"avc2":  {},
	"avc3":  {},
	"avc4":  {},
	"mpeg4": {},
}

var legacyAudioWhitelist = []string{"aac", "ac3", "eac3", "mp3"}

const ffprobeTimeout = 15 * time.Second
const providerProbeSampleBytes int64 = 16 * 1024 * 1024

// VideoHandler handles video streaming requests using the local stream provider.
type VideoHandler struct {
	transmux    bool
	ffmpegPath  string
	ffprobePath string
	streamer    streaming.Provider
	hlsManager  *HLSManager

	// Subtitle extraction for non-HLS streams
	subtitleExtractManager *SubtitleExtractManager

	// Local WebDAV access for ffprobe seeking (usenet paths)
	webdavMu       sync.RWMutex
	webdavBaseURL  string
	webdavPrefix   string

	// User settings for policy checks (e.g., HDR/DV policy)
	userSettingsSvc   UserSettingsProvider
	clientSettingsSvc ClientSettingsProvider
	configManager     ConfigProvider
}

// UserSettingsProvider interface for accessing user settings
type UserSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

// ConfigProvider interface for accessing global config
type ConfigProvider interface {
	Load() (config.Settings, error)
}

// NewVideoHandler creates a new video handler without an attached provider.
func NewVideoHandler(transmuxEnabled bool, ffmpegPath, ffprobePath string) *VideoHandler {
	return newVideoHandler(transmuxEnabled, ffmpegPath, ffprobePath, "", nil)
}

// NewVideoHandlerWithProvider creates a handler that prefers the provided stream source.
func NewVideoHandlerWithProvider(transmuxEnabled bool, ffmpegPath, ffprobePath, hlsTempDir string, provider streaming.Provider) *VideoHandler {
	return newVideoHandler(transmuxEnabled, ffmpegPath, ffprobePath, hlsTempDir, provider)
}

// NewVideoHandlerWithNzbSystem creates a handler that uses NzbSystem for streaming
// NzbSystem handles queue paths through the Stream method, other paths go through WebDAV
func NewVideoHandlerWithNzbSystem(transmuxEnabled bool, ffmpegPath, ffprobePath string, nzbSystem *integration.NzbSystem) *VideoHandler {
	return newVideoHandler(transmuxEnabled, ffmpegPath, ffprobePath, "", nzbSystem)
}

func newVideoHandler(transmuxEnabled bool, ffmpegPath, ffprobePath, hlsTempDir string, provider streaming.Provider) *VideoHandler {
	resolvedFFmpeg := strings.TrimSpace(ffmpegPath)
	if resolvedFFmpeg == "" {
		resolvedFFmpeg = "ffmpeg"
	}

	if transmuxEnabled {
		if path, err := exec.LookPath(resolvedFFmpeg); err == nil {
			resolvedFFmpeg = path
		} else {
			log.Printf("[video] disabling transmux: unable to locate ffmpeg at %q: %v", resolvedFFmpeg, err)
			transmuxEnabled = false
		}
	}

	resolvedFFprobe := strings.TrimSpace(ffprobePath)
	if resolvedFFprobe == "" {
		resolvedFFprobe = "ffprobe"
	}

	if path, err := exec.LookPath(resolvedFFprobe); err == nil {
		resolvedFFprobe = path
	} else {
		log.Printf("[video] warning: ffprobe unavailable at %q: %v", resolvedFFprobe, err)
		resolvedFFprobe = ""
	}

	// Initialize HLS manager if transmux is enabled
	var hlsMgr *HLSManager
	if transmuxEnabled {
		hlsMgr = NewHLSManager(hlsTempDir, resolvedFFmpeg, resolvedFFprobe, provider)
		log.Printf("[video] initialized HLS manager for Dolby Vision streaming (temp dir: %s)", hlsMgr.baseDir)
	}

	// Initialize subtitle extraction manager
	var subtitleMgr *SubtitleExtractManager
	if resolvedFFmpeg != "" && resolvedFFprobe != "" && provider != nil {
		subtitleBaseDir := filepath.Join(os.TempDir(), "strmr-subtitles")
		subtitleMgr = NewSubtitleExtractManager(subtitleBaseDir, resolvedFFmpeg, resolvedFFprobe, provider)
		log.Printf("[video] initialized subtitle extraction manager (base dir: %s)", subtitleBaseDir)
	}

	return &VideoHandler{
		transmux:               transmuxEnabled,
		ffmpegPath:             resolvedFFmpeg,
		ffprobePath:            resolvedFFprobe,
		streamer:               provider,
		hlsManager:             hlsMgr,
		subtitleExtractManager: subtitleMgr,
	}
}

// SetUserSettingsService sets the user settings service for policy checks
func (h *VideoHandler) SetUserSettingsService(svc UserSettingsProvider) {
	h.userSettingsSvc = svc
}

// SetConfigManager sets the config manager for global settings fallback
func (h *VideoHandler) SetConfigManager(cfgManager ConfigProvider) {
	h.configManager = cfgManager
}

// SetClientSettingsService sets the client settings service for per-device policy checks
func (h *VideoHandler) SetClientSettingsService(svc ClientSettingsProvider) {
	h.clientSettingsSvc = svc
}

// StreamVideo serves registered streams via the local provider.
func (h *VideoHandler) StreamVideo(w http.ResponseWriter, r *http.Request) {
	// Handle OPTIONS requests for CORS
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	// Only allow GET and HEAD
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the file path from query parameter
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "Missing path parameter", http.StatusBadRequest)
		return
	}

	// Clean the path: remove /webdav/ prefix but preserve the leading slash for NZB paths
	cleanPath := filePath
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	// Determine whether transmuxing is desired and possible
	ext := detectContainerExt(cleanPath)
	target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	shouldTransmux, overrideTransmux, transmuxReason := h.shouldTransmux(r, cleanPath, ext)
	if transmuxReason != "" {
	}
	forceAAC := target == "web" || target == "browser"
	// Check global setting for forced AAC transcoding (for Bluetooth compatibility)
	if !forceAAC && h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			forceAAC = settings.Streaming.ForceAACTranscoding
		}
	}
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
	rangeSummary := rangeHeader
	if rangeSummary == "" {
		rangeSummary = "full"
	}

	// Debug logging for troubleshooting
	log.Printf("[video] request path=%q clean=%q method=%s target=%q range=%s transmux=%t provider=%t", filePath, cleanPath, r.Method, target, rangeSummary, shouldTransmux, h.streamer != nil)

	// Additional detailed logging for range requests (seek operations)
	if rangeHeader != "" {
		log.Printf("[video] SEEK REQUEST detected: range=%q path=%q method=%s", rangeHeader, cleanPath, r.Method)
	}

	if shouldTransmux {
		if h.streamer == nil {
			http.Error(w, "stream provider not configured", http.StatusServiceUnavailable)
			return
		}

		// For transmux streams, ignore range requests and serve full stream
		// Transmuxed streams don't support seeking due to the real-time transcoding pipeline
		if rangeHeader != "" {
			log.Printf("[video] Ignoring range request for transmux stream (seeking not supported) - range=%q path=%q", rangeHeader, cleanPath)
			// Clear the range header so streamWithTransmuxProvider serves the full stream
			r.Header.Del("Range")
		}

		handled, err := h.streamWithTransmuxProvider(w, r, cleanPath, forceAAC, overrideTransmux)
		if handled {
			if err != nil {
				log.Printf("[video] provider transmux error for %q: %v", cleanPath, err)
			}
			return
		}

		if err != nil {
			log.Printf("[video] provider transmux unavailable for %q: %v", cleanPath, err)
		}
	}

	if h.streamer == nil {
		http.Error(w, "stream provider not configured", http.StatusServiceUnavailable)
		return
	}

	handled, err := h.streamViaProvider(w, r, cleanPath)
	if handled {
		if err != nil {
			log.Printf("[video] provider error for %q: %v", cleanPath, err)
		}
		return
	}

	http.Error(w, "stream not found", http.StatusNotFound)
}

func (h *VideoHandler) streamViaProvider(w http.ResponseWriter, r *http.Request, cleanPath string) (bool, error) {
	// Check if this is a pre-resolved external URL (e.g., from AIOStreams)
	// These URLs should be proxied directly rather than going through the provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		return h.proxyExternalURL(w, r, cleanPath)
	}

	// Create a context with timeout to prevent hanging streams
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	rangeHeader := r.Header.Get("Range")

	// Track this stream for admin monitoring
	tracker := GetStreamTracker()
	var streamID string
	var bytesCounter *int64

	// Log the provider request details
	log.Printf(
		"[video] provider request: path=%q range=%q method=%s rawQuery=%q",
		cleanPath,
		rangeHeader,
		r.Method,
		r.URL.RawQuery,
	)

	resp, err := h.streamer.Stream(ctx, streaming.Request{
		Path:        cleanPath,
		RangeHeader: rangeHeader,
		Method:      r.Method,
	})
	if err != nil {
		log.Printf("[video] provider stream failed path=%q range=%q err=%v", cleanPath, rangeHeader, err)
		if errors.Is(err, streaming.ErrNotFound) {
			return false, nil
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true, err
	}
	defer resp.Close()

	// Log detailed response information
	contentRange := resp.Headers.Get("Content-Range")
	contentLength := resp.Headers.Get("Content-Length")
	acceptRanges := resp.Headers.Get("Accept-Ranges")
	expectedLength := resp.ContentLength
	if expectedLength <= 0 && contentLength != "" {
		if parsed, parseErr := strconv.ParseInt(contentLength, 10, 64); parseErr == nil && parsed >= 0 {
			expectedLength = parsed
		} else if parseErr != nil {
			log.Printf("[video] warning: could not parse provider content length %q for %q: %v", contentLength, cleanPath, parseErr)
		}
	}
	if expectedLength <= 0 && contentRange != "" {
		rangeSpec := strings.TrimSpace(contentRange)
		if strings.HasPrefix(strings.ToLower(rangeSpec), "bytes ") {
			rangeSpec = strings.TrimSpace(rangeSpec[6:])
			if slash := strings.Index(rangeSpec, "/"); slash >= 0 {
				rangeSpec = rangeSpec[:slash]
			}
			if dash := strings.Index(rangeSpec, "-"); dash >= 0 {
				startStr := strings.TrimSpace(rangeSpec[:dash])
				endStr := strings.TrimSpace(rangeSpec[dash+1:])
				if start, err := strconv.ParseInt(startStr, 10, 64); err == nil {
					if end, err := strconv.ParseInt(endStr, 10, 64); err == nil && end >= start {
						expectedLength = end - start + 1
					}
				}
			}
		}
	}
	log.Printf("[video] provider response: path=%q status=%d content-length=%s content-range=%q accept-ranges=%q range-request=%q expected-bytes=%d",
		cleanPath, resp.Status, contentLength, contentRange, acceptRanges, rangeHeader, expectedLength)

	h.writeCommonHeaders(w)
	for key, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Add filename header if available
	if resp.Filename != "" {
		w.Header().Set("X-Filename", resp.Filename)
		log.Printf("[video] setting filename header: %s", resp.Filename)
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}

	// Log the status being written back to client
	log.Printf("[video] writing response to client: path=%q status=%d range=%q", cleanPath, status, rangeHeader)
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return true, nil
	}

	if resp.Body != nil {
		// Start tracking this stream
		var rangeStart, rangeEnd int64
		// Parse range if present (simplified)
		streamID, bytesCounter = tracker.StartStream(r, cleanPath, expectedLength, rangeStart, rangeEnd)
		defer tracker.EndStream(streamID)

		reader := io.Reader(resp.Body)
		if expectedLength > 0 {
			reader = io.LimitReader(resp.Body, expectedLength)
		}

		buf := make([]byte, 512*1024) // 512KB buffer
		var total int64
		flusher, _ := w.(http.Flusher)
		flushCounter := 0
		const flushInterval = 1

		lastLogBytes := int64(0)
		const logInterval = 10 * 1024 * 1024 // Log every 10MB

		log.Printf("[video] starting stream copy: path=%q range=%q streamID=%s", cleanPath, rangeHeader, streamID)

		for {
			// Check if context is cancelled (client disconnected)
			select {
			case <-ctx.Done():
				log.Printf("[video] SEEK ABORT: provider stream cancelled path=%q total=%d range=%q reason=%v", cleanPath, total, rangeHeader, ctx.Err())
				return true, ctx.Err()
			default:
			}

			n, readErr := reader.Read(buf)
			if n > 0 {
				if expectedLength > 0 {
					remaining := expectedLength - total
					if remaining <= 0 {
						if flusher != nil {
							flusher.Flush()
						}
						log.Printf("[video] provider stream complete path=%q total=%d range=%q (expected-bytes=%d)", cleanPath, total, rangeHeader, expectedLength)
						break
					}
					if int64(n) > remaining {
						n = int(remaining)
					}
				}

				written, writeErr := w.Write(buf[:n])
				if writeErr != nil {
					if isClientGone(writeErr) || ctx.Err() == context.Canceled {
						log.Printf("[video] SEEK ABORT: client disconnected path=%q bytes=%d total=%d range=%q", cleanPath, n, total, rangeHeader)
						return true, nil
					}
					log.Printf("[video] SEEK ERROR: provider write error path=%q bytes=%d total=%d range=%q err=%v", cleanPath, n, total, rangeHeader, writeErr)
					return true, writeErr
				}

				total += int64(written)
				// Update stream tracking bytes counter
				if bytesCounter != nil {
					atomic.StoreInt64(bytesCounter, total)
				}
				flushCounter++

				// Periodic progress logging
				if total-lastLogBytes >= logInterval {
					log.Printf("[video] streaming progress: path=%q total=%d range=%q", cleanPath, total, rangeHeader)
					lastLogBytes = total
				}

				// Flush less frequently to improve performance
				if flusher != nil && flushCounter >= flushInterval {
					flusher.Flush()
					flushCounter = 0
				}

				if expectedLength > 0 && total >= expectedLength {
					if flusher != nil {
						flusher.Flush()
					}
					log.Printf("[video] provider stream complete path=%q total=%d range=%q (expected-bytes=%d)", cleanPath, total, rangeHeader, expectedLength)
					break
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					log.Printf("[video] SEEK ERROR: provider read error path=%q total=%d range=%q err=%v", cleanPath, total, rangeHeader, readErr)
					return true, readErr
				}
				// Final flush on EOF
				if flusher != nil {
					flusher.Flush()
				}
				log.Printf("[video] provider stream complete path=%q total=%d range=%q", cleanPath, total, rangeHeader)
				break
			}
		}
	}

	return true, nil
}

// HandleOptions handles CORS preflight requests
func (h *VideoHandler) HandleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Range, Content-Type, Accept, Origin, Authorization, X-API-Key, X-Requested-With",
	)
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, Content-Type, X-Filename")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.WriteHeader(http.StatusOK)
}

func isClientGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if netErr.Err != nil {
			if errors.Is(netErr.Err, syscall.EPIPE) || errors.Is(netErr.Err, syscall.ECONNRESET) || errors.Is(netErr.Err, os.ErrClosed) {
				return true
			}
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "broken pipe") || strings.Contains(strings.ToLower(err.Error()), "connection reset") {
		return true
	}
	return false
}

func (h *VideoHandler) shouldTransmux(r *http.Request, cleanPath, ext string) (bool, bool, string) {
	query := r.URL.Query()
	format := strings.ToLower(strings.TrimSpace(query.Get("format")))
	target := strings.ToLower(strings.TrimSpace(query.Get("target")))
	manualFlag := strings.ToLower(strings.TrimSpace(query.Get("transmux")))
	dvFlag := strings.ToLower(strings.TrimSpace(query.Get("dv"))) == "true"

	// Check for Dolby Vision flag - MUST transmux to preserve DV metadata
	if dvFlag {
		log.Printf("[video] Dolby Vision transmux requested for path=%q", cleanPath)
		return true, true, "dolby vision requested"
	}

	// Check for explicit disable flags first
	if manualFlag == "0" || manualFlag == "false" || manualFlag == "no" || manualFlag == "off" || manualFlag == "skip" {
		return false, false, "manual disable"
	}

	override := manualFlag == "force" || manualFlag == "1" || manualFlag == "true" || manualFlag == "yes"

	if !h.transmux && !override {
		return false, override, "transmux disabled"
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false, override, "unsupported method"
	}

	ext = strings.ToLower(strings.TrimSpace(ext))

	// Never transmux when the source is already a browser-friendly MP4 unless forced
	if ext == ".mp4" || ext == ".m4v" {
		if override {
			return true, override, "override mp4"
		}

		if target == "web" || target == "browser" {
			needs, reason := h.mp4NeedsTransmux(r.Context(), cleanPath)
			if needs {
				return true, override, reason
			}
			if reason != "" && reason != "mp4 codec browser-compatible" {
			}
		}

		return false, override, "already mp4"
	}

	// Explicit overrides
	if manualFlag == "1" || manualFlag == "true" || manualFlag == "yes" || manualFlag == "force" {
		return true, override, "manual flag"
	}
	if format == "mp4" || target == "web" || target == "browser" {
		return true, override, "target mp4"
	}

	// Heuristics based on known container extensions
	if ext == "" {
		if override {
			return true, override, "override without ext"
		}
		return false, override, "unknown ext"
	}
	if _, ok := transmuxableExtensions[ext]; ok {
		return true, override, "transmuxable ext"
	}

	if override {
		return true, override, "override non-transmuxable"
	}

	return false, override, "non-transmuxable ext"
}

func (h *VideoHandler) mp4NeedsTransmux(ctx context.Context, cleanPath string) (bool, string) {
	if h.ffprobePath == "" {
		return false, "ffprobe unavailable for mp4 compatibility"
	}

	var meta *ffprobeOutput
	if h.streamer != nil {
		if m, err := h.runFFProbeFromProvider(ctx, cleanPath); err == nil && m != nil {
			meta = m
		} else if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[video] mp4 codec probe via provider failed path=%q: %v", cleanPath, err)
		}
	}

	if meta == nil {
		return false, "mp4 codec probe unavailable"
	}

	stream := selectPrimaryVideoStream(meta)
	if stream == nil {
		return true, "mp4 missing video track"
	}

	codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
	if codec == "" {
		return true, "mp4 codec unknown"
	}

	if shouldForceMp4CodecTransmux(codec) {
		return true, fmt.Sprintf("mp4 codec %s requires transmux", codec)
	}

	return false, "mp4 codec browser-compatible"
}

func shouldForceMp4CodecTransmux(codec string) bool {
	normalized := strings.ToLower(strings.TrimSpace(codec))
	if normalized == "" {
		return true
	}
	if _, ok := browserFriendlyMp4VideoCodecs[normalized]; ok {
		return false
	}
	if strings.HasPrefix(normalized, "h264") || strings.HasPrefix(normalized, "avc") {
		return false
	}
	return true
}

// detectContainerExt attempts to determine a known container extension from an obfuscated filename
// such as "file.mkv_yEnc_..." by searching for known extensions within the name.
func detectContainerExt(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return ""
	}
	// Direct suffix fast-path
	if ext := strings.ToLower(strings.TrimSpace(path.Ext(lower))); ext != "" {
		// If the direct ext is clearly a known container, return it
		switch ext {
		case ".mp4", ".m4v", ".webm", ".mkv", ".ts", ".m2ts", ".mts", ".avi", ".mpg", ".mpeg", ".m3u8":
			return ext
		}
	}

	// Fallback: scan for known container markers inside the name
	known := []string{".mp4", ".m4v", ".webm", ".mkv", ".ts", ".m2ts", ".mts", ".avi", ".mpg", ".mpeg", ".m3u8"}
	for _, ext := range known {
		if strings.HasSuffix(lower, ext) {
			return ext
		}
		if strings.Contains(lower, ext+"_") || strings.Contains(lower, ext+".") || strings.Contains(lower, ext+"-") {
			return ext
		}
	}
	// Give up: return the naive extension
	return strings.ToLower(strings.TrimSpace(path.Ext(lower)))
}

func (h *VideoHandler) streamWithTransmuxProvider(w http.ResponseWriter, r *http.Request, cleanPath string, forceAAC bool, override bool) (bool, error) {
	if !h.transmux && !override {
		return false, errors.New("transmux disabled")
	}

	if h.streamer == nil {
		return false, fmt.Errorf("stream provider not configured")
	}

	if h.ffmpegPath == "" {
		return false, errors.New("ffmpeg path is not configured")
	}

	// Create a context with timeout for provider transmux operations
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	if r.Method == http.MethodHead {
		h.writeCommonHeaders(w)
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "none")

		if h.ffprobePath != "" {
			if meta, err := h.runFFProbeFromProvider(ctx, cleanPath); err == nil && meta != nil {
				if duration := parseFloat(meta.Format.Duration); duration > 0 {
					dur := fmt.Sprintf("%.3f", duration)
					w.Header().Set("X-Content-Duration", dur)
					w.Header().Set("Content-Duration", dur)
				}
			} else if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[video] provider ffprobe duration lookup failed for %q: %v", cleanPath, err)
			}
		}

		w.WriteHeader(http.StatusOK)
		return true, nil
	}

	var (
		meta           *ffprobeOutput
		fallbackReason = "ffprobe unavailable; using legacy audio mapping"
	)

	if h.ffprobePath != "" {
		probe, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] provider ffprobe failed for %q: %v", cleanPath, err)
			}
			fallbackReason = fmt.Sprintf("ffprobe failed: %v", err)
		} else {
			meta = probe
			fallbackReason = ""
		}

		if meta != nil && parseFloat(meta.Format.Duration) > 0 {
			fallbackReason = ""
		}
	}

	plan := h.buildTransmuxPlan(meta, "pipe:0", forceAAC, fallbackReason)

	resp, err := h.streamer.Stream(ctx, streaming.Request{Path: cleanPath, Method: http.MethodGet})
	if err != nil {
		return false, fmt.Errorf("provider stream: %w", err)
	}
	if resp.Body == nil {
		resp.Close()
		return false, fmt.Errorf("provider stream returned empty body")
	}

	pr, pw := io.Pipe()
	copyErrCh := make(chan error, 1)
	go func() {
		defer resp.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		if copyErr != nil && !errors.Is(copyErr, io.EOF) && !errors.Is(copyErr, io.ErrClosedPipe) {
			copyErrCh <- copyErr
		} else {
			copyErrCh <- nil
		}
		_ = pw.Close()
	}()

	cmd := exec.CommandContext(ctx, h.ffmpegPath, plan.args...)
	cmd.Stdin = pr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = pw.CloseWithError(err)
		return false, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = pw.CloseWithError(err)
		return false, fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = pw.CloseWithError(err)
		return false, fmt.Errorf("ffmpeg start: %w", err)
	}

	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()

	h.writeCommonHeaders(w)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	if plan.duration > 0 {
		durationHeader := fmt.Sprintf("%.3f", plan.duration)
		w.Header().Set("X-Content-Duration", durationHeader)
		w.Header().Set("Content-Duration", durationHeader)
	}
	w.WriteHeader(http.StatusOK)
	started := true

	flusher, _ := w.(http.Flusher)
	var totalWritten int64
	buf := make([]byte, 256*1024) // Larger buffer for provider transmux
	flushCounter := 0
	const flushInterval = 2 // Flush every 2 writes (512KB chunks)

	for {
		// Check if context is cancelled (client disconnected)
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = pw.CloseWithError(ctx.Err())
			log.Printf("[video] provider transmux cancelled path=%q total=%d reason=%v", cleanPath, totalWritten, ctx.Err())
			return started, ctx.Err()
		default:
		}

		n, readErr := stdout.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				_ = cmd.Process.Kill()
				_ = pw.CloseWithError(writeErr)
				if isConnectionError(writeErr) {
					log.Printf("[video] provider transmux connection lost path=%q bytes=%d total=%d err=%v", cleanPath, n, totalWritten, writeErr)
					return started, writeErr
				}
				return started, fmt.Errorf("write response: %w", writeErr)
			}
			totalWritten += int64(written)
			flushCounter++

			// Flush less frequently to improve performance
			if flusher != nil && flushCounter >= flushInterval {
				flusher.Flush()
				flushCounter = 0
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				// Final flush on EOF
				if flusher != nil {
					flusher.Flush()
				}
				break
			}
			_ = cmd.Process.Kill()
			_ = pw.CloseWithError(readErr)
			return started, fmt.Errorf("ffmpeg read: %w", readErr)
		}
	}

	if err := cmd.Wait(); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "signal") && !strings.Contains(strings.ToLower(err.Error()), "broken pipe") {
			return started, fmt.Errorf("ffmpeg wait: %w", err)
		}
	}

	if copyErr := <-copyErrCh; copyErr != nil && !errors.Is(copyErr, context.Canceled) && !errors.Is(copyErr, io.EOF) && !errors.Is(copyErr, io.ErrClosedPipe) {
		log.Printf("[video] provider stream copy error for %q: %v", cleanPath, copyErr)
	}

	log.Printf("[video] provider transmux complete path=%q bytes=%d", cleanPath, totalWritten)
	return started, nil
}

// buildWebDAVURL constructs a WebDAV URL for ffprobe seekable access (usenet paths)
func (h *VideoHandler) buildWebDAVURL(cleanPath string) string {
	h.webdavMu.RLock()
	base := h.webdavBaseURL
	prefix := h.webdavPrefix
	h.webdavMu.RUnlock()

	if base == "" || prefix == "" {
		return ""
	}

	// Path should start with the webdav prefix (e.g., /webdav or /streams)
	pathToUse := cleanPath
	if !strings.HasPrefix(pathToUse, "/") {
		pathToUse = "/" + pathToUse
	}

	// If path doesn't start with prefix, prepend it
	if !strings.HasPrefix(pathToUse, prefix) {
		pathToUse = prefix + pathToUse
	}

	return base + pathToUse
}

func (h *VideoHandler) runFFProbeFromProvider(ctx context.Context, cleanPath string) (*ffprobeOutput, error) {
	// Check if this is already an external URL (e.g., from AIOStreams pre-resolved streams)
	// If so, probe it directly without going through the provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		log.Printf("[video] ffprobe using external URL directly: %s", cleanPath)
		meta, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			return nil, fmt.Errorf("ffprobe external URL failed: %w", err)
		}
		return meta, nil
	}

	if h.streamer == nil {
		return nil, fmt.Errorf("stream provider not configured")
	}

	// Try to get a direct URL first - this allows ffprobe to seek for moov atom at end of file
	if directProvider, ok := h.streamer.(streaming.DirectURLProvider); ok {
		directURL, err := directProvider.GetDirectURL(ctx, cleanPath)
		if err == nil && directURL != "" {
			log.Printf("[video] ffprobe using direct URL for seekable access: %s", cleanPath)
			meta, err := h.runFFProbe(ctx, directURL, nil)
			if err != nil {
				// Log but don't fail - fall through to WebDAV or piped approach
				log.Printf("[video] ffprobe with direct URL failed, trying alternatives: %v", err)
			} else {
				return meta, nil
			}
		} else if err != nil && !errors.Is(err, streaming.ErrNotFound) {
			log.Printf("[video] GetDirectURL failed for %q: %v", cleanPath, err)
		}
	}

	// Try WebDAV URL for usenet paths - allows ffprobe to seek
	if webdavURL := h.buildWebDAVURL(cleanPath); webdavURL != "" {
		log.Printf("[video] ffprobe using WebDAV URL for seekable access: %s", cleanPath)
		meta, err := h.runFFProbe(ctx, webdavURL, nil)
		if err != nil {
			// Log but don't fail - fall through to piped approach
			log.Printf("[video] ffprobe with WebDAV URL failed, falling back to piped probe: %v", err)
		} else {
			return meta, nil
		}
	}

	// Fall back to piped approach (when direct URL and WebDAV fail)
	log.Printf("[video] ffprobe falling back to piped probe for: %s", cleanPath)
	request := streaming.Request{Path: cleanPath, Method: http.MethodGet}
	if providerProbeSampleBytes > 0 {
		request.RangeHeader = fmt.Sprintf("bytes=0-%d", providerProbeSampleBytes-1)
	}

	resp, err := h.streamer.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	if resp.Body == nil {
		resp.Close()
		return nil, fmt.Errorf("provider ffprobe stream returned empty body")
	}

	pr, pw := io.Pipe()
	go func() {
		defer resp.Close()
		buf := make([]byte, 128*1024)
		_, copyErr := io.CopyBuffer(pw, resp.Body, buf)
		if copyErr != nil && !errors.Is(copyErr, io.EOF) && !errors.Is(copyErr, io.ErrClosedPipe) {
			pw.CloseWithError(copyErr)
			return
		}
		pw.Close()
	}()

	meta, err := h.runFFProbe(ctx, "pipe:0", pr)
	if err != nil {
		pw.CloseWithError(err)
		return nil, err
	}
	return meta, nil
}

// ProbeVideo returns lightweight metadata about the requested media without relying on external WebDAV probes.
func (h *VideoHandler) ProbeVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.writeCommonHeaders(w)
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")

	filePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if filePath == "" {
		http.Error(w, "Missing path parameter", http.StatusBadRequest)
		return
	}

	log.Printf("[video] ProbeVideo: received request for path=%q", filePath)

	// Clean the path: remove /webdav/ prefix but preserve the leading slash for NZB paths
	cleanPath := filePath
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	log.Printf("[video] ProbeVideo: after cleaning, path=%q", cleanPath)

	sanitizedPath := cleanPath
	if sanitizedPath == "" {
		sanitizedPath = filePath
	}

	// Extract profile info from query params for DV policy check
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}

	// Get clientID from query param or header
	clientID := r.URL.Query().Get("clientId")
	if clientID == "" {
		clientID = r.Header.Get("X-Client-ID")
	}

	var (
		fileSize int64
		notes    []string
	)

	// Check if this is an external URL (e.g., from AIOStreams)
	isExternalURL := strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://")

	if isExternalURL {
		log.Printf("[video] ProbeVideo: detected external URL, probing directly: %s", cleanPath)

		// For external URLs, try to get file size via HEAD request
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, cleanPath, nil)
		if err == nil {
			headReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
			headResp, headErr := http.DefaultClient.Do(headReq)
			if headErr == nil {
				defer headResp.Body.Close()
				if headResp.ContentLength > 0 {
					fileSize = headResp.ContentLength
				}
			}
		}

		// Probe directly with ffprobe
		var meta *ffprobeOutput
		if h.ffprobePath != "" {
			if m, err := h.runFFProbe(r.Context(), cleanPath, nil); err == nil && m != nil {
				meta = m
			} else if err != nil {
				log.Printf("[video] ProbeVideo: ffprobe external URL failed: %v", err)
				notes = append(notes, "ffprobe could not derive metadata")
			}
		}

		var response videoMetadataResponse
		if meta != nil {
			plan := determineAudioPlan(meta, false)
			response = composeMetadataResponse(meta, sanitizedPath, plan)
			if response.FileSizeBytes == 0 && fileSize > 0 {
				response.FileSizeBytes = fileSize
			}
		} else {
			response = videoMetadataResponse{
				Path:                  sanitizedPath,
				DurationSeconds:       0,
				FileSizeBytes:         fileSize,
				AudioStreams:          []audioStreamSummary{},
				VideoStreams:          []videoStreamSummary{},
				SubtitleStreams:       []subtitleStreamSummary{},
				AudioStrategy:         string(audioPlanNone),
				SelectedAudioIndex:    -1,
				AudioCopySupported:    false,
				NeedsAudioTranscode:   false,
				SelectedSubtitleIndex: -1,
			}
		}

		if len(notes) > 0 {
			response.Notes = append(response.Notes, notes...)
		}

		// Check DV policy violation before returning
		if violation, dvProfile := h.checkDVPolicyViolation(response, profileID, clientID); violation {
			http.Error(w, fmt.Sprintf("DV_PROFILE_INCOMPATIBLE: profile %s has no HDR fallback layer", dvProfile), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("[video] probe encode error for %q: %v", cleanPath, err)
		}
		return
	}

	if h.streamer != nil {
		log.Printf("[video] ProbeVideo: attempting HEAD request for path=%q", cleanPath)
		resp, err := h.streamer.Stream(r.Context(), streaming.Request{
			Path:   cleanPath,
			Method: http.MethodHead,
		})
		if err != nil {
			if errors.Is(err, streaming.ErrNotFound) {
				log.Printf("[video] ProbeVideo: stream not found for path=%q", cleanPath)
				http.Error(w, "stream not found", http.StatusNotFound)
				return
			}
			log.Printf("[video] metadata provider head failed for %q: %v", cleanPath, err)
			notes = append(notes, "stream metadata unavailable")
		} else if resp != nil {
			defer resp.Close()
			fileSize = resp.ContentLength
			if fileSize <= 0 {
				if resp.Headers != nil {
					if raw := resp.Headers.Get("Content-Length"); raw != "" {
						if parsed, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
							fileSize = parsed
						}
					}
				}
			}
		}
	} else {
		notes = append(notes, "stream provider unavailable; metadata limited")
	}

	// Try to derive rich metadata using ffprobe when available
	var meta *ffprobeOutput
	if h.ffprobePath != "" {
		// Prefer probing via provider to avoid full origin fetch, then fall back to WebDAV URL
		if h.streamer != nil {
			if m, err := h.runFFProbeFromProvider(r.Context(), cleanPath); err == nil && m != nil {
				meta = m
			} else if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[video] metadata provider ffprobe failed for %q: %v", cleanPath, err)
			}
		}
	}

	var response videoMetadataResponse
	if meta != nil {
		plan := determineAudioPlan(meta, false)
		response = composeMetadataResponse(meta, sanitizedPath, plan)
		// Prefer probed file size, but backfill from HEAD if missing
		if response.FileSizeBytes == 0 && fileSize > 0 {
			response.FileSizeBytes = fileSize
		}
	} else {
		if h.ffprobePath == "" {
			notes = append(notes, "ffprobe unavailable on server")
		} else {
			notes = append(notes, "ffprobe could not derive metadata")
		}
		response = videoMetadataResponse{
			Path:                  sanitizedPath,
			DurationSeconds:       0,
			FileSizeBytes:         fileSize,
			AudioStreams:          []audioStreamSummary{},
			VideoStreams:          []videoStreamSummary{},
			SubtitleStreams:       []subtitleStreamSummary{},
			AudioStrategy:         string(audioPlanNone),
			SelectedAudioIndex:    -1,
			AudioCopySupported:    false,
			NeedsAudioTranscode:   false,
			SelectedSubtitleIndex: -1,
		}
	}

	if len(notes) > 0 {
		response.Notes = append(response.Notes, notes...)
	}

	// Check DV policy violation before returning
	if violation, dvProfile := h.checkDVPolicyViolation(response, profileID, clientID); violation {
		http.Error(w, fmt.Sprintf("DV_PROFILE_INCOMPATIBLE: profile %s has no HDR fallback layer", dvProfile), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[video] probe encode error for %q: %v", cleanPath, err)
	}
}

func (h *VideoHandler) buildTransmuxPlan(meta *ffprobeOutput, inputSpecifier string, forceAAC bool, fallbackReason string) transmuxPlan {
	plan := transmuxPlan{
		videoMap: "0:v:0",
		audio: audioPlan{
			mode:   audioPlanFallback,
			reason: fallbackReason,
		},
	}

	if strings.TrimSpace(plan.audio.reason) == "" {
		plan.audio.reason = "ffprobe unavailable; using legacy audio mapping"
	}

	plan.movflags = computeMovflags(plan.audio)
	plan.args = buildLegacyArgs(inputSpecifier, plan.movflags, forceAAC, plan.videoCodec, plan.hasDolbyVision, plan.dolbyVisionProfile)
	plan.duration = 0

	if meta == nil {
		if forceAAC && plan.audio.mode == audioPlanFallback {
			plan.audio = audioPlan{mode: audioPlanTranscode, reason: "target requires AAC audio"}
		}
		return plan
	}

	plan.usedProbe = true
	if stream := selectPrimaryVideoStream(meta); stream != nil {
		plan.videoMap = fmt.Sprintf("0:%d", stream.Index)
		plan.videoCodec = strings.ToLower(strings.TrimSpace(stream.CodecName))
		// Detect Dolby Vision
		hasDV, dvProfile, _ := detectDolbyVision(stream)
		plan.hasDolbyVision = hasDV
		plan.dolbyVisionProfile = dvProfile
	} else {
		plan.videoMap = "0:v:0"
		plan.videoCodec = ""
	}

	plan.audio = determineAudioPlan(meta, forceAAC)
	plan.movflags = computeMovflags(plan.audio)
	plan.args = buildArgsWithProbe(inputSpecifier, plan.videoMap, plan.audio, plan.movflags, plan.videoCodec, plan.hasDolbyVision, plan.dolbyVisionProfile)
	plan.duration = parseFloat(meta.Format.Duration)
	return plan
}

func selectPrimaryVideoStream(meta *ffprobeOutput) *ffprobeStream {
	if meta == nil {
		return nil
	}
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		if strings.EqualFold(stream.CodecType, "video") {
			return stream
		}
	}
	return nil
}

func determineAudioPlan(meta *ffprobeOutput, forceAAC bool) audioPlan {
	if meta == nil {
		if forceAAC {
			return audioPlan{mode: audioPlanTranscode, reason: "no metadata; forcing AAC"}
		}
		return audioPlan{mode: audioPlanNone, reason: "no metadata"}
	}

	var firstAudio *ffprobeStream
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		if !strings.EqualFold(stream.CodecType, "audio") {
			continue
		}
		if firstAudio == nil {
			firstAudio = stream
		}
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		if forceAAC {
			if codec == "aac" {
				return audioPlan{mode: audioPlanCopy, stream: stream, reason: "AAC audio already compatible"}
			}
			// Keep scanning in case another track is AAC
			continue
		}
		if _, ok := copyableAudioCodecs[codec]; ok {
			return audioPlan{mode: audioPlanCopy, stream: stream, reason: "copy-compatible audio codec"}
		}
	}

	if firstAudio != nil {
		codec := strings.ToLower(strings.TrimSpace(firstAudio.CodecName))
		if forceAAC {
			return audioPlan{mode: audioPlanTranscode, stream: firstAudio, reason: "target requires AAC audio"}
		}
		return audioPlan{mode: audioPlanTranscode, stream: firstAudio, reason: fmt.Sprintf("audio codec %s requires transcoding", codec)}
	}

	if forceAAC {
		return audioPlan{mode: audioPlanTranscode, reason: "target requires AAC audio but no audio streams detected"}
	}
	return audioPlan{mode: audioPlanNone, reason: "no audio streams detected"}
}

func buildArgsWithProbe(inputURL, videoMap string, plan audioPlan, movflags string, videoCodec string, hasDV bool, dvProfile string) []string {
	args := []string{"-nostdin", "-loglevel", "error", "-i", inputURL}

	if strings.TrimSpace(videoMap) == "" {
		videoMap = "0:v:0"
	}
	args = append(args, "-map", videoMap)

	// Map ALL audio streams instead of just one
	if plan.stream != nil {
		args = append(args, "-map", "0:a")
	}

	// Map text-based subtitle streams that can be converted to mov_text
	// Skip bitmap-based subtitles (pgs, dvdsub, etc.) as they can't be embedded in MP4
	args = append(args, "-map", "0:s:m:codec_name:subrip?", "-map", "0:s:m:codec_name:ass?", "-map", "0:s:m:codec_name:ssa?", "-map", "0:s:m:codec_name:mov_text?", "-dn", "-c:v", "copy")

	if shouldTagHevcAsHvc1(videoCodec) {
		if hasDV {
			// Use dvh1 tag for Dolby Vision HEVC in MP4
			// dvh1 = Dolby Vision with backward-compatible HDR10 base layer
			// -strict unofficial enables dvcC box generation
			// hevc_metadata fixes VUI for sources with incorrect color metadata (e.g., bt709 instead of bt2020/PQ)
			args = append(args, "-strict", "unofficial", "-tag:v", "dvh1", "-bsf:v", "hevc_metadata=colour_primaries=9:transfer_characteristics=16:matrix_coefficients=9")
			log.Printf("[video] Using dvh1 tag for Dolby Vision content (profile: %s)", dvProfile)
		} else {
			args = append(args, "-tag:v", "hvc1")
		}
	}

	switch plan.mode {
	case audioPlanCopy:
		if plan.stream != nil {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-an")
		}
	case audioPlanTranscode:
		if plan.stream != nil {
			// Transcode first audio stream to AAC, copy others
			args = append(args, "-c:a:0", "aac", "-b:a:0", "192k", "-c:a:1", "copy")
		} else {
			args = append(args, "-an")
		}
	case audioPlanNone:
		args = append(args, "-an")
	default:
		args = append(args, "-c:a", "copy")
	}

	// Convert text-based subtitles to mov_text for MP4 compatibility
	// This will only apply to subtitles that were successfully mapped above
	args = append(args, "-c:s", "mov_text", "-disposition:s", "0")

	if strings.TrimSpace(movflags) == "" {
		movflags = computeMovflags(plan)
	}
	args = appendStreamingOutputArgs(args, movflags)
	return args
}

func buildLegacyArgs(inputURL, movflags string, forceAAC bool, videoCodec string, hasDV bool, dvProfile string) []string {
	args := []string{"-nostdin", "-loglevel", "error", "-i", inputURL, "-map", "0:v"}
	if forceAAC {
		// Map all audio streams for AAC mode
		args = append(args, "-map", "0:a")
	} else {
		for _, codec := range legacyAudioWhitelist {
			args = append(args, "-map", fmt.Sprintf("0:a:m:codec_name:%s?", codec))
		}
		args = append(args,
			"-map", "-0:a:m:codec_name:truehd",
			"-map", "-0:a:m:codec_name:dts",
		)
	}
	// Map text-based subtitle streams that can be converted to mov_text
	// Skip bitmap-based subtitles (pgs, dvdsub, etc.) as they can't be embedded in MP4
	args = append(args,
		"-map", "0:s:m:codec_name:subrip?",
		"-map", "0:s:m:codec_name:ass?",
		"-map", "0:s:m:codec_name:ssa?",
		"-map", "0:s:m:codec_name:mov_text?",
		"-dn",
		"-c:v", "copy",
	)
	if shouldTagHevcAsHvc1(videoCodec) {
		if hasDV {
			// -strict unofficial enables dvcC box, hevc_metadata fixes color VUI for sources with wrong metadata
			args = append(args, "-strict", "unofficial", "-tag:v", "dvh1", "-bsf:v", "hevc_metadata=colour_primaries=9:transfer_characteristics=16:matrix_coefficients=9")
			log.Printf("[video] Using dvh1 tag for Dolby Vision content (legacy mode, profile: %s)", dvProfile)
		} else {
			args = append(args, "-tag:v", "hvc1")
		}
	}
	if forceAAC {
		// Transcode first audio to AAC, copy others
		args = append(args, "-c:a:0", "aac", "-b:a:0", "192k", "-c:a:1", "copy")
	} else {
		args = append(args, "-c:a", "copy")
	}
	// Convert text-based subtitles to mov_text for MP4 compatibility
	// This will only apply to subtitles that were successfully mapped above
	args = append(args, "-c:s", "mov_text", "-disposition:s", "0")
	if strings.TrimSpace(movflags) == "" {
		movflags = strings.Join([]string{"frag_keyframe", "separate_moof", "omit_tfhd_offset", "default_base_moof", "empty_moov"}, "+")
	}
	args = appendStreamingOutputArgs(args, movflags)
	return args
}

func appendStreamingOutputArgs(args []string, movflags string) []string {
	flags := strings.TrimSpace(movflags)
	if flags == "" {
		// Use iOS-friendly fragmented MP4 flags
		flags = strings.Join([]string{"frag_keyframe", "empty_moov", "default_base_moof", "isml+dash"}, "+")
	}
	args = append(args,
		"-movflags", flags,
		"-muxdelay", "0",
		"-muxpreload", "0",
		"-frag_duration", "500000", // 500ms fragments for better iOS compatibility
		"-min_frag_duration", "500000",
		"-f", "mp4",
		"pipe:1",
	)
	return args
}

func shouldTagHevcAsHvc1(codec string) bool {
	value := strings.ToLower(strings.TrimSpace(codec))
	if value == "" {
		return false
	}
	if value == "hevc" || value == "h265" {
		return true
	}
	return strings.HasPrefix(value, "hevc")
}

func detectDolbyVision(stream *ffprobeStream) (hasDV bool, dvProfile string, hdrFormat string) {
	if stream == nil {
		return false, "", ""
	}

	codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
	if codec != "hevc" && !strings.HasPrefix(codec, "hevc") && codec != "h265" {
		return false, "", ""
	}

	// Check for Dolby Vision via side data
	for _, sd := range stream.SideDataList {
		sdType := strings.ToLower(strings.TrimSpace(sd.SideDataType))
		if strings.Contains(sdType, "dovi") || strings.Contains(sdType, "dolby") {
			// Log detailed DOVI configuration
			profileStr := fmt.Sprintf("dvhe.%02d.%02d", sd.DVProfile, sd.DVLevel)
			log.Printf("[video] Dolby Vision detected: profile=%d level=%d version=%d.%d rpu=%d el=%d bl=%d bl_compat_id=%d (%s)",
				sd.DVProfile, sd.DVLevel, sd.DVVersionMajor, sd.DVVersionMinor,
				sd.RPUPresentFlag, sd.ELPresentFlag, sd.BLPresentFlag, sd.DVBLSignalCompatibilityID, profileStr)

			// Determine if this profile has HDR10 fallback
			// Profile 8 with bl_compat_id=1 or 2 has HDR10 base layer
			// Profile 5 is dual-layer without HDR10 fallback
			hasHDR10Fallback := sd.DVProfile == 8 && (sd.DVBLSignalCompatibilityID == 1 || sd.DVBLSignalCompatibilityID == 2)
			if hasHDR10Fallback {
				log.Printf("[video] Dolby Vision profile %d has HDR10 compatible base layer (bl_compat_id=%d)",
					sd.DVProfile, sd.DVBLSignalCompatibilityID)
			} else if sd.DVProfile == 5 {
				log.Printf("[video] Dolby Vision profile 5 detected - dual-layer without HDR10 fallback")
			} else if sd.DVProfile == 7 {
				log.Printf("[video] Dolby Vision profile 7 detected - MEL/FEL enhancement layer")
			}

			return true, profileStr, "DV"
		}
	}

	// Check profile for Dolby Vision markers
	profile := strings.ToLower(strings.TrimSpace(stream.Profile))
	if strings.Contains(profile, "dv") || strings.Contains(profile, "dolby") {
		log.Printf("[video] Dolby Vision detected via profile: %s", stream.Profile)
		return true, profile, "DV"
	}

	// Check color transfer for HDR indicators (not DV, but related)
	transfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	if transfer == "smpte2084" {
		// PQ curve - HDR10
		return false, "", "HDR10"
	} else if transfer == "arib-std-b67" {
		// HLG
		return false, "", "HLG"
	}

	return false, "", ""
}

func isDolbyVisionProfile7(profile string) bool {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		return false
	}

	// Match dvhe.07.XX format (new detailed format)
	if strings.HasPrefix(profile, "dvhe.07") {
		return true
	}

	// Fallback for other metadata formats (e.g., "profile 7", "p7")
	if strings.Contains(profile, "profile 7") || strings.Contains(profile, "p7") {
		return true
	}

	return false
}

func computeMovflags(plan audioPlan) string {
	flags := []string{
		"frag_keyframe",
		"separate_moof",
		"omit_tfhd_offset",
		"default_base_moof",
	}
	if shouldIncludeEmptyMoov(plan) {
		flags = append(flags, "empty_moov")
	}
	return strings.Join(flags, "+")
}

func shouldIncludeEmptyMoov(plan audioPlan) bool {
	if plan.mode == audioPlanCopy {
		codec := plan.codec()
		if codec == "ac3" || codec == "eac3" {
			return false
		}
	}
	return true
}

func (h *VideoHandler) runFFProbe(ctx context.Context, inputSpecifier string, reader io.Reader) (*ffprobeOutput, error) {
	if h.ffprobePath == "" {
		return nil, errors.New("ffprobe not configured")
	}

	// Use longer timeout for external URLs (need to download data over network)
	timeout := ffprobeTimeout
	if strings.HasPrefix(inputSpecifier, "http://") || strings.HasPrefix(inputSpecifier, "https://") {
		timeout = 60 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-v", "error", "-print_format", "json", "-show_streams", "-show_format"}
	if reader != nil {
		args = append(args, "-i", "pipe:0")
	} else {
		args = append(args, "-i", inputSpecifier)
	}

	cmd := exec.CommandContext(probeCtx, h.ffprobePath, args...)
	if reader != nil {
		cmd.Stdin = reader
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ffprobe timeout after %s", timeout)
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("ffprobe error: %s", errMsg)
		}
		return nil, err
	}

	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	return &parsed, nil
}

func composeMetadataResponse(meta *ffprobeOutput, sanitizedPath string, plan audioPlan) videoMetadataResponse {
	resp := videoMetadataResponse{
		Path:                sanitizedPath,
		DurationSeconds:     parseFloat(meta.Format.Duration),
		FileSizeBytes:       parseInt64(meta.Format.Size),
		FormatName:          strings.TrimSpace(meta.Format.FormatName),
		FormatLongName:      strings.TrimSpace(meta.Format.FormatLongName),
		FormatBitRate:       parseInt64(meta.Format.BitRate),
		AudioStrategy:       string(plan.mode),
		AudioPlanReason:     plan.reason,
		AudioStreams:        make([]audioStreamSummary, 0),
		VideoStreams:        make([]videoStreamSummary, 0),
		SelectedAudioIndex:  -1,
		SelectedAudioCodec:  "",
		AudioCopySupported:  false,
		NeedsAudioTranscode: plan.mode == audioPlanTranscode,
	}

	if plan.stream != nil {
		resp.SelectedAudioIndex = plan.stream.Index
		resp.SelectedAudioCodec = plan.codec()
	}

	var copyableFound bool
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		switch strings.ToLower(strings.TrimSpace(stream.CodecType)) {
		case "audio":
			summary := audioStreamSummary{
				Index:         stream.Index,
				CodecName:     strings.TrimSpace(stream.CodecName),
				CodecLongName: strings.TrimSpace(stream.CodecLongName),
				Channels:      stream.Channels,
				SampleRate:    parseInt(stream.SampleRate),
				BitRate:       parseInt64(stream.BitRate),
				ChannelLayout: strings.TrimSpace(stream.ChannelLayout),
				Language:      normalizeTag(stream.Tags, "language"),
				Title:         normalizeTag(stream.Tags, "title"),
				Disposition:   stream.Disposition,
			}
			codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
			if _, ok := copyableAudioCodecs[codec]; ok {
				summary.CopySupported = true
				copyableFound = true
			}
			resp.AudioStreams = append(resp.AudioStreams, summary)
		case "video":
			hasDV, dvProfile, hdrFormat := detectDolbyVision(stream)
			summary := videoStreamSummary{
				Index:              stream.Index,
				CodecName:          strings.TrimSpace(stream.CodecName),
				CodecLongName:      strings.TrimSpace(stream.CodecLongName),
				Width:              stream.Width,
				Height:             stream.Height,
				BitRate:            parseInt64(stream.BitRate),
				PixFmt:             strings.TrimSpace(stream.PixFmt),
				Profile:            strings.TrimSpace(stream.Profile),
				AvgFrameRate:       strings.TrimSpace(stream.AvgFrameRate),
				HasDolbyVision:     hasDV,
				DolbyVisionProfile: dvProfile,
				HdrFormat:          hdrFormat,
				ColorTransfer:      strings.TrimSpace(stream.ColorTransfer),
				ColorPrimaries:     strings.TrimSpace(stream.ColorPrimaries),
				ColorSpace:         strings.TrimSpace(stream.ColorSpace),
			}
			resp.VideoStreams = append(resp.VideoStreams, summary)
		case "subtitle":
			// Only include text-based subtitle codecs that can be converted to WebVTT
			// Bitmap subtitles (PGS, DVD, etc.) cannot be played in the web player
			codecName := strings.ToLower(strings.TrimSpace(stream.CodecName))
			textSubtitleCodecs := map[string]bool{
				"subrip": true, "srt": true, "ass": true, "ssa": true,
				"webvtt": true, "vtt": true, "mov_text": true, "text": true,
				"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
				"mpl2": true, "pjs": true, "realtext": true, "stl": true,
				"subviewer": true, "subviewer1": true, "vplayer": true,
			}
			if !textSubtitleCodecs[codecName] {
				// Skip bitmap/unsupported subtitle formats
				continue
			}
			summary := subtitleStreamSummary{
				Index:         stream.Index,
				CodecName:     strings.TrimSpace(stream.CodecName),
				CodecLongName: strings.TrimSpace(stream.CodecLongName),
				Language:      normalizeTag(stream.Tags, "language"),
				Title:         normalizeTag(stream.Tags, "title"),
				Disposition:   stream.Disposition,
			}
			resp.SubtitleStreams = append(resp.SubtitleStreams, summary)
		}
	}

	resp.AudioCopySupported = copyableFound
	if !copyableFound && len(resp.AudioStreams) > 0 {
		resp.Notes = append(resp.Notes, "source audio codec requires transcoding for MP4 playback")
	}
	if len(resp.AudioStreams) == 0 {
		resp.Notes = append(resp.Notes, "no audio streams detected by ffprobe")
	}
	if plan.mode == audioPlanNone {
		resp.Notes = append(resp.Notes, "transmux will proceed without an audio track")
	}

	// Select default subtitle track (prefer forced, then default disposition)
	resp.SelectedSubtitleIndex = -1
	for _, sub := range resp.SubtitleStreams {
		if sub.Disposition != nil {
			if forced, ok := sub.Disposition["forced"]; ok && forced > 0 {
				resp.SelectedSubtitleIndex = sub.Index
				break
			}
		}
	}
	if resp.SelectedSubtitleIndex == -1 {
		for _, sub := range resp.SubtitleStreams {
			if sub.Disposition != nil {
				if def, ok := sub.Disposition["default"]; ok && def > 0 {
					resp.SelectedSubtitleIndex = sub.Index
					break
				}
			}
		}
	}

	return resp
}

func normalizeTag(tags map[string]string, key string) string {
	if tags == nil {
		return ""
	}
	return strings.TrimSpace(tags[key])
}

func parseFloat(value string) float64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseInt(value string) int {
	v, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return v
}

func parseInt64(value string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

type audioPlanMode string

const (
	audioPlanCopy      audioPlanMode = "copy"
	audioPlanTranscode audioPlanMode = "transcode"
	audioPlanNone      audioPlanMode = "none"
	audioPlanFallback  audioPlanMode = "fallback"
)

type audioPlan struct {
	mode   audioPlanMode
	stream *ffprobeStream
	reason string
}

func (p audioPlan) codec() string {
	if p.stream == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p.stream.CodecName))
}

type transmuxPlan struct {
	args               []string
	audio              audioPlan
	videoMap           string
	videoCodec         string
	hasDolbyVision     bool
	dolbyVisionProfile string
	usedProbe          bool
	movflags           string
	duration           float64
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	Index          int               `json:"index"`
	CodecType      string            `json:"codec_type"`
	CodecName      string            `json:"codec_name"`
	CodecLongName  string            `json:"codec_long_name"`
	Channels       int               `json:"channels"`
	SampleRate     string            `json:"sample_rate"`
	BitRate        string            `json:"bit_rate"`
	ChannelLayout  string            `json:"channel_layout"`
	Tags           map[string]string `json:"tags"`
	Disposition    map[string]int    `json:"disposition"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	PixFmt         string            `json:"pix_fmt"`
	Profile        string            `json:"profile"`
	AvgFrameRate   string            `json:"avg_frame_rate"`
	ColorSpace     string            `json:"color_space"`
	ColorTransfer  string            `json:"color_transfer"`
	ColorPrimaries string            `json:"color_primaries"`
	SideDataList   []ffprobeSideData `json:"side_data_list"`
}

type ffprobeSideData struct {
	SideDataType string `json:"side_data_type"`
	// DOVI configuration record fields
	DVVersionMajor            int `json:"dv_version_major,omitempty"`
	DVVersionMinor            int `json:"dv_version_minor,omitempty"`
	DVProfile                 int `json:"dv_profile,omitempty"`
	DVLevel                   int `json:"dv_level,omitempty"`
	RPUPresentFlag            int `json:"rpu_present_flag,omitempty"`
	ELPresentFlag             int `json:"el_present_flag,omitempty"`
	BLPresentFlag             int `json:"bl_present_flag,omitempty"`
	DVBLSignalCompatibilityID int `json:"dv_bl_signal_compatibility_id,omitempty"`
}

type ffprobeFormat struct {
	Filename       string `json:"filename"`
	NbStreams      int    `json:"nb_streams"`
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
	Duration       string `json:"duration"`
	Size           string `json:"size"`
	BitRate        string `json:"bit_rate"`
}

type audioStreamSummary struct {
	Index         int            `json:"index"`
	CodecName     string         `json:"codecName"`
	CodecLongName string         `json:"codecLongName,omitempty"`
	Channels      int            `json:"channels,omitempty"`
	SampleRate    int            `json:"sampleRate,omitempty"`
	BitRate       int64          `json:"bitRate,omitempty"`
	ChannelLayout string         `json:"channelLayout,omitempty"`
	Language      string         `json:"language,omitempty"`
	Title         string         `json:"title,omitempty"`
	Disposition   map[string]int `json:"disposition,omitempty"`
	CopySupported bool           `json:"copySupported"`
}

type videoStreamSummary struct {
	Index              int    `json:"index"`
	CodecName          string `json:"codecName"`
	CodecLongName      string `json:"codecLongName,omitempty"`
	Width              int    `json:"width,omitempty"`
	Height             int    `json:"height,omitempty"`
	BitRate            int64  `json:"bitRate,omitempty"`
	PixFmt             string `json:"pixFmt,omitempty"`
	Profile            string `json:"profile,omitempty"`
	AvgFrameRate       string `json:"avgFrameRate,omitempty"`
	HasDolbyVision     bool   `json:"hasDolbyVision"`
	DolbyVisionProfile string `json:"dolbyVisionProfile,omitempty"`
	HdrFormat          string `json:"hdrFormat,omitempty"`
	// HDR color metadata for HDR10 detection
	ColorTransfer  string `json:"colorTransfer,omitempty"`
	ColorPrimaries string `json:"colorPrimaries,omitempty"`
	ColorSpace     string `json:"colorSpace,omitempty"`
}

type subtitleStreamSummary struct {
	Index         int            `json:"index"`
	CodecName     string         `json:"codecName"`
	CodecLongName string         `json:"codecLongName,omitempty"`
	Language      string         `json:"language,omitempty"`
	Title         string         `json:"title,omitempty"`
	Disposition   map[string]int `json:"disposition,omitempty"`
}

type videoMetadataResponse struct {
	Path                  string                  `json:"path"`
	DurationSeconds       float64                 `json:"durationSeconds"`
	FileSizeBytes         int64                   `json:"fileSizeBytes,omitempty"`
	FormatName            string                  `json:"formatName,omitempty"`
	FormatLongName        string                  `json:"formatLongName,omitempty"`
	FormatBitRate         int64                   `json:"formatBitRate,omitempty"`
	AudioStreams          []audioStreamSummary    `json:"audioStreams"`
	VideoStreams          []videoStreamSummary    `json:"videoStreams"`
	SubtitleStreams       []subtitleStreamSummary `json:"subtitleStreams"`
	AudioStrategy         string                  `json:"audioStrategy"`
	AudioPlanReason       string                  `json:"audioPlanReason,omitempty"`
	SelectedAudioIndex    int                     `json:"selectedAudioIndex"`
	SelectedAudioCodec    string                  `json:"selectedAudioCodec,omitempty"`
	AudioCopySupported    bool                    `json:"audioCopySupported"`
	NeedsAudioTranscode   bool                    `json:"needsAudioTranscode"`
	SelectedSubtitleIndex int                     `json:"selectedSubtitleIndex"`
	Notes                 []string                `json:"notes,omitempty"`
}

func (h *VideoHandler) writeCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Range, Content-Type, Accept, Origin, Authorization, X-API-Key, X-Requested-With",
	)
	w.Header().Set(
		"Access-Control-Expose-Headers",
		"Content-Length, Content-Range, Accept-Ranges, Content-Type, Content-Duration, X-Content-Duration, X-Filename",
	)
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")

	// Add additional headers for better video streaming support
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// isConnectionError checks if the error is a network connection error that indicates
// the client has disconnected or there's a network issue.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	// Check for common connection error patterns
	connectionErrors := []string{
		"connection reset by peer",
		"broken pipe",
		"connection refused",
		"connection aborted",
		"connection timed out",
		"use of closed network connection",
		"write: connection reset",
		"read: connection reset",
	}

	for _, pattern := range connectionErrors {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	// Check for specific error types
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || !netErr.Temporary()
	}

	// Check for syscall errors
	if sysErr, ok := err.(*os.SyscallError); ok {
		switch sysErr.Err {
		case syscall.EPIPE, syscall.ECONNRESET, syscall.ECONNABORTED:
			return true
		}
	}

	return false
}

// StartHLSSession creates a new HLS transcoding session for Dolby Vision content
func (h *VideoHandler) StartHLSSession(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Clean the path
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	// Check for Dolby Vision and HDR10 flags
	hasDV := r.URL.Query().Get("dv") == "true"
	dvProfile := r.URL.Query().Get("dvProfile")
	hasHDR := r.URL.Query().Get("hdr") == "true"
	forceAAC := r.URL.Query().Get("forceAAC") == "true"
	// Check global setting for forced AAC transcoding (for Bluetooth compatibility)
	if !forceAAC && h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			forceAAC = settings.Streaming.ForceAACTranscoding
		}
	}
	// Check both "startOffset" (frontend) and "start" (legacy) parameter names
	startParam := strings.TrimSpace(r.URL.Query().Get("startOffset"))
	if startParam == "" {
		startParam = strings.TrimSpace(r.URL.Query().Get("start"))
	}

	if hasDV && isDolbyVisionProfile7(dvProfile) {
		log.Printf("[video] Dolby Vision profile 7 detected for path=%q; falling back to HDR10-only HLS output", cleanPath)
		hasDV = false
		dvProfile = ""
		hasHDR = true // DV Profile 7 has HDR10 base layer
	}

	startSeconds := 0.0
	if startParam != "" {
		if parsed, err := strconv.ParseFloat(startParam, 64); err == nil && parsed >= 0 {
			startSeconds = parsed
		} else {
			log.Printf("[video] invalid start offset %q for HLS session; defaulting to 0", startParam)
		}
	}

	// Parse selected audio/subtitle track indices
	audioTrackIndex := -1 // -1 means use default (all tracks or first track)
	audioParam := strings.TrimSpace(r.URL.Query().Get("audioTrack"))
	if audioParam != "" {
		if parsed, err := strconv.Atoi(audioParam); err == nil && parsed >= 0 {
			audioTrackIndex = parsed
			log.Printf("[video] HLS session requested audio track: %d", audioTrackIndex)
		}
	}

	subtitleTrackIndex := -1 // -1 means no subtitles
	subtitleParam := strings.TrimSpace(r.URL.Query().Get("subtitleTrack"))
	if subtitleParam != "" {
		if parsed, err := strconv.Atoi(subtitleParam); err == nil && parsed >= 0 {
			subtitleTrackIndex = parsed
			log.Printf("[video] HLS session requested subtitle track: %d", subtitleTrackIndex)
		}
	}

	// Extract profile info from query params
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}
	profileName := r.URL.Query().Get("profileName")

	// Get clientID from query param or header
	clientID := r.URL.Query().Get("clientId")
	if clientID == "" {
		clientID = r.Header.Get("X-Client-ID")
	}

	// Check DV profile compatibility with user's HDR/DV policy
	if hasDV && dvProfile != "" {
		hdrDVPolicy := h.getHDRDVPolicy(profileID, clientID)
		if hdrDVPolicy == models.HDRDVPolicyIncludeHDR {
			// Parse DV profile number from format like "dvhe.05.06"
			dvProfileNum := parseDVProfileNumber(dvProfile)
			if dvProfileNum == 5 {
				log.Printf("[video] DV profile 5 incompatible with 'hdr' policy (no HDR fallback) for path=%q", cleanPath)
				http.Error(w, "DV_PROFILE_INCOMPATIBLE: profile 5 has no HDR fallback layer", http.StatusBadRequest)
				return
			}
			// Strip DV metadata for profiles 7/8 when policy is "hdr"
			// Use HDR10 base layer for safe playback on non-DV devices
			if dvProfileNum == 7 || dvProfileNum == 8 {
				log.Printf("[video] HDRDVPolicy 'hdr': stripping DV metadata for profile %d, using HDR10 base layer for path=%q", dvProfileNum, cleanPath)
				hasDV = false
				dvProfile = ""
				hasHDR = true
			}
		}
	}

	// For warm start sessions, probe for the actual keyframe position FFmpeg will seek to BEFORE creating session
	// This is critical because FFmpeg seeks to the nearest keyframe, not the exact requested time
	// Both video and subtitles must start from the same keyframe position for sync
	transcodingOffset := startSeconds
	if startSeconds > 0 {
		keyframePos := h.hlsManager.probeKeyframePosition(r.Context(), cleanPath, startSeconds)
		transcodingOffset = keyframePos
		log.Printf("[video] warm start: probed keyframe position %.3fs (requested %.3fs, delta %.3fs)",
			keyframePos, startSeconds, keyframePos-startSeconds)
	}

	log.Printf("[video] creating HLS session for path=%q dv=%v dvProfile=%q hdr=%v start=%.3fs transcodingOffset=%.3fs audioTrack=%d subtitleTrack=%d",
		cleanPath, hasDV, dvProfile, hasHDR, startSeconds, transcodingOffset, audioTrackIndex, subtitleTrackIndex)

	session, err := h.hlsManager.CreateSession(r.Context(), cleanPath, path, hasDV, dvProfile, hasHDR, forceAAC, startSeconds, transcodingOffset, audioTrackIndex, subtitleTrackIndex, profileID, profileName, getClientIP(r))
	if err != nil {
		log.Printf("[video] failed to create HLS session: %v", err)
		http.Error(w, fmt.Sprintf("failed to create HLS session: %v", err), http.StatusInternalServerError)
		return
	}

	actualStartOffset := transcodingOffset
	// Delta between actual keyframe position and requested position (negative = keyframe is earlier)
	keyframeDelta := actualStartOffset - startSeconds

	// Return session ID, playlist URL, and duration (if available)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	response := map[string]interface{}{
		"sessionId":         session.ID,
		"playlistUrl":       fmt.Sprintf("/video/hls/%s/stream.m3u8", session.ID),
		"startOffset":       session.StartOffset,
		"actualStartOffset": actualStartOffset,
		"keyframeDelta":     keyframeDelta,
	}

	// Include duration if it was successfully probed
	if session.Duration > 0 {
		response["duration"] = session.Duration
	}

	if session.Duration > 0 && session.StartOffset > 0 {
		remaining := session.Duration - session.StartOffset
		if remaining < 0 {
			remaining = 0
		}
		response["remainingDuration"] = remaining
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[video] failed to encode HLS session response: %v", err)
	}

	log.Printf("[video] created HLS session %s (duration=%.2fs)", session.ID, session.Duration)
}

// StartLiveHLSSession creates a new HLS session for live TV streams
func (h *VideoHandler) StartLiveHLSSession(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	liveURL := r.URL.Query().Get("url")
	if liveURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	// Validate URL scheme
	if !strings.HasPrefix(liveURL, "http://") && !strings.HasPrefix(liveURL, "https://") {
		http.Error(w, "invalid url scheme", http.StatusBadRequest)
		return
	}

	log.Printf("[video] creating live HLS session for URL: %s", liveURL)

	session, err := h.hlsManager.CreateLiveSession(r.Context(), liveURL)
	if err != nil {
		log.Printf("[video] failed to create live HLS session: %v", err)
		http.Error(w, fmt.Sprintf("failed to create live HLS session: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	response := map[string]interface{}{
		"sessionId":   session.ID,
		"playlistUrl": fmt.Sprintf("/video/hls/%s/stream.m3u8", session.ID),
		"isLive":      true,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[video] failed to encode live HLS session response: %v", err)
	}

	log.Printf("[video] created live HLS session %s", session.ID)
}

// ServeHLSPlaylist serves the HLS playlist for a session
func (h *VideoHandler) ServeHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	if sessionID == "" {
		http.Error(w, "missing session ID", http.StatusBadRequest)
		return
	}

	h.hlsManager.ServePlaylist(w, r, sessionID)
}

// ServeHLSSegment serves an HLS segment for a session
func (h *VideoHandler) ServeHLSSegment(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	segmentName := vars["segment"]

	if sessionID == "" || segmentName == "" {
		http.Error(w, "missing session ID or segment name", http.StatusBadRequest)
		return
	}

	h.hlsManager.ServeSegment(w, r, sessionID, segmentName)
}

// ServeHLSSubtitles serves the sidecar VTT subtitle file for an HLS session
func (h *VideoHandler) ServeHLSSubtitles(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	if sessionID == "" {
		http.Error(w, "missing session ID", http.StatusBadRequest)
		return
	}

	h.hlsManager.ServeSubtitles(w, r, sessionID)
}

// KeepAliveHLSSession extends the idle timeout for a paused HLS session
func (h *VideoHandler) KeepAliveHLSSession(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	if sessionID == "" {
		http.Error(w, "missing session ID", http.StatusBadRequest)
		return
	}

	h.hlsManager.KeepAlive(w, r, sessionID)
}

// GetHLSSessionStatus returns the current status of an HLS session
// Used by the frontend to poll for errors during playback
func (h *VideoHandler) GetHLSSessionStatus(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	if sessionID == "" {
		http.Error(w, "missing session ID", http.StatusBadRequest)
		return
	}

	h.hlsManager.GetSessionStatus(w, r, sessionID)
}

// SeekHLSSession seeks within an existing HLS session by restarting transcoding from a new offset
// This is faster than creating a new session since it reuses the existing session structure
func (h *VideoHandler) SeekHLSSession(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]

	if sessionID == "" {
		http.Error(w, "missing session ID", http.StatusBadRequest)
		return
	}

	h.hlsManager.Seek(w, r, sessionID)
}

// Shutdown gracefully shuts down the video handler and cleans up resources
func (h *VideoHandler) Shutdown() {
	if h.hlsManager != nil {
		log.Printf("[video] shutting down HLS manager")
		h.hlsManager.Shutdown()
	}
}

// ConfigureLocalWebDAVAccess passes local WebDAV connection info to the HLS manager
// and stores it for ffprobe seekable access on usenet paths.
func (h *VideoHandler) ConfigureLocalWebDAVAccess(baseURL, prefix, username, password string) {
	if h == nil {
		return
	}

	// Store locally for ffprobe WebDAV URL building
	base := strings.TrimSpace(baseURL)
	if base != "" {
		parsed, err := url.Parse(base)
		if err == nil {
			if username != "" {
				parsed.User = url.UserPassword(username, password)
			}
			parsed.Path = ""
			parsed.RawQuery = ""
			parsed.Fragment = ""

			h.webdavMu.Lock()
			h.webdavBaseURL = strings.TrimRight(parsed.String(), "/")
			h.webdavPrefix = "/" + strings.Trim(prefix, "/")
			h.webdavMu.Unlock()

			log.Printf("[video] configured WebDAV access for ffprobe: base=%q prefix=%q", h.webdavBaseURL, h.webdavPrefix)
		}
	}

	// Pass to HLS manager as well
	if h.hlsManager != nil {
		h.hlsManager.ConfigureLocalWebDAVAccess(baseURL, prefix, username, password)
	}

	// Pass to subtitle extract manager as well
	if h.subtitleExtractManager != nil {
		h.subtitleExtractManager.ConfigureLocalWebDAVAccess(baseURL, prefix, username, password)
	}
}

// GetHLSManager returns the HLS manager for admin/monitoring purposes.
func (h *VideoHandler) GetHLSManager() *HLSManager {
	if h == nil {
		return nil
	}
	return h.hlsManager
}

// GetSubtitleExtractManager returns the subtitle extract manager for pre-extraction.
func (h *VideoHandler) GetSubtitleExtractManager() *SubtitleExtractManager {
	if h == nil {
		return nil
	}
	return h.subtitleExtractManager
}

// CreateHLSSession implements the HLSCreator interface for prequeue.
// This creates an HLS session for HDR content so the frontend can use native player.
func (h *VideoHandler) CreateHLSSession(ctx context.Context, path string, hasDV bool, dvProfile string, hasHDR bool, audioTrackIndex int, subtitleTrackIndex int, profileID string, startOffset float64) (*HLSSessionResult, error) {
	if h == nil {
		return nil, errors.New("video handler is nil")
	}
	if h.hlsManager == nil {
		return nil, errors.New("HLS manager not configured")
	}

	log.Printf("[video] CreateHLSSession: creating session for path=%q hasDV=%v dvProfile=%s hasHDR=%v audioTrack=%d subtitleTrack=%d startOffset=%.2f", path, hasDV, dvProfile, hasHDR, audioTrackIndex, subtitleTrackIndex, startOffset)

	// Check HDR/DV policy and handle DV stripping
	if hasDV && dvProfile != "" {
		hdrDVPolicy := h.getHDRDVPolicy(profileID, "") // clientID not available in prequeue path
		dvProfileNum := parseDVProfileNumber(dvProfile)

		if hdrDVPolicy == models.HDRDVPolicyIncludeHDR && (dvProfileNum == 7 || dvProfileNum == 8) {
			// Strip DV metadata when policy is "hdr" - use HDR10 base layer
			log.Printf("[video] CreateHLSSession: HDRDVPolicy 'hdr': stripping DV metadata for profile %d, using HDR10 base layer for path=%q", dvProfileNum, path)
			hasDV = false
			dvProfile = ""
			hasHDR = true
		} else if isDolbyVisionProfile7(dvProfile) {
			// Unconditional fallback for Profile 7 (enhancement layers incompatible with many devices)
			log.Printf("[video] CreateHLSSession: Dolby Vision profile 7 detected for path=%q; falling back to HDR10-only HLS output", path)
			hasDV = false
			dvProfile = ""
			hasHDR = true
		}
	}

	session, err := h.hlsManager.CreateSession(ctx, path, path, hasDV, dvProfile, hasHDR, false, startOffset, 0, audioTrackIndex, subtitleTrackIndex, profileID, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create HLS session: %w", err)
	}

	return &HLSSessionResult{
		SessionID:   session.ID,
		PlaylistURL: "/video/hls/" + session.ID + "/stream.m3u8",
	}, nil
}

// ProbeVideoPath implements the VideoProber interface for HDR detection.
// This allows the prequeue handler to detect Dolby Vision and HDR10 content.
func (h *VideoHandler) ProbeVideoPath(ctx context.Context, path string) (*VideoProbeResult, error) {
	if h == nil {
		return nil, errors.New("video handler is nil")
	}
	if h.ffprobePath == "" {
		return nil, errors.New("ffprobe not configured")
	}

	// Clean the path (same logic as ProbeVideo HTTP handler)
	// Note: external URLs (http://, https://) are not modified
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	log.Printf("[video] ProbeVideoPath: probing path=%q for HDR detection", cleanPath)

	var meta *ffprobeOutput

	// For external URLs, probe directly without requiring a stream provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		log.Printf("[video] ProbeVideoPath: external URL detected, probing directly")
		m, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] ProbeVideoPath: ffprobe external URL failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else if h.streamer != nil {
		m, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] ProbeVideoPath: ffprobe via provider failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else {
		return nil, errors.New("no stream provider configured")
	}

	if meta == nil {
		return nil, errors.New("ffprobe returned no metadata")
	}

	result := &VideoProbeResult{
		HasDolbyVision:     false,
		HasHDR10:           false,
		DolbyVisionProfile: "",
	}

	// Check the primary video stream for HDR content
	stream := selectPrimaryVideoStream(meta)
	if stream == nil {
		log.Printf("[video] ProbeVideoPath: no video stream found in %q", cleanPath)
		return result, nil
	}

	// Detect Dolby Vision
	hasDV, dvProfile, _ := detectDolbyVision(stream)
	result.HasDolbyVision = hasDV
	result.DolbyVisionProfile = dvProfile

	// Detect HDR10 (PQ transfer with BT.2020)
	colorTransfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	colorPrimaries := strings.ToLower(strings.TrimSpace(stream.ColorPrimaries))
	if colorTransfer == "smpte2084" && colorPrimaries == "bt2020" {
		result.HasHDR10 = true
		log.Printf("[video] ProbeVideoPath: HDR10 detected (PQ + BT.2020)")
	}

	if result.HasDolbyVision {
		log.Printf("[video] ProbeVideoPath: Dolby Vision detected, profile=%s", result.DolbyVisionProfile)
	}

	return result, nil
}

// ProbeVideoMetadata implements the VideoMetadataProber interface for track selection.
// This allows the prequeue handler to get audio/subtitle stream info for preference matching.
func (h *VideoHandler) ProbeVideoMetadata(ctx context.Context, path string) (*VideoMetadataResult, error) {
	if h == nil {
		return nil, errors.New("video handler is nil")
	}
	if h.ffprobePath == "" {
		return nil, errors.New("ffprobe not configured")
	}

	// Clean the path (same logic as ProbeVideo HTTP handler)
	// Note: external URLs (http://, https://) are not modified
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	log.Printf("[video] ProbeVideoMetadata: probing path=%q for track metadata", cleanPath)

	var meta *ffprobeOutput

	// For external URLs, probe directly without requiring a stream provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		log.Printf("[video] ProbeVideoMetadata: external URL detected, probing directly")
		m, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] ProbeVideoMetadata: ffprobe external URL failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else if h.streamer != nil {
		m, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] ProbeVideoMetadata: ffprobe via provider failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else {
		return nil, errors.New("no stream provider configured")
	}

	result := &VideoMetadataResult{
		AudioStreams:    make([]AudioStreamInfo, 0),
		SubtitleStreams: make([]SubtitleStreamInfo, 0),
	}

	// Extract audio and subtitle stream info
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		codecType := strings.ToLower(strings.TrimSpace(stream.CodecType))

		switch codecType {
		case "audio":
			info := AudioStreamInfo{
				Index:    stream.Index,
				Language: normalizeTag(stream.Tags, "language"),
				Title:    normalizeTag(stream.Tags, "title"),
			}
			result.AudioStreams = append(result.AudioStreams, info)

		case "subtitle":
			// Only include text-based subtitle codecs that can be converted to WebVTT
			codecName := strings.ToLower(strings.TrimSpace(stream.CodecName))
			textSubtitleCodecs := map[string]bool{
				"subrip": true, "srt": true, "ass": true, "ssa": true,
				"webvtt": true, "vtt": true, "mov_text": true, "text": true,
				"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
				"mpl2": true, "pjs": true, "realtext": true, "stl": true,
				"subviewer": true, "subviewer1": true, "vplayer": true,
			}
			if !textSubtitleCodecs[codecName] {
				// Skip bitmap/unsupported subtitle formats
				continue
			}
			isForced := false
			isDefault := false
			if stream.Disposition != nil {
				if f, ok := stream.Disposition["forced"]; ok && f > 0 {
					isForced = true
				}
				if d, ok := stream.Disposition["default"]; ok && d > 0 {
					isDefault = true
				}
			}
			info := SubtitleStreamInfo{
				Index:     stream.Index,
				Codec:     codecName,
				Language:  normalizeTag(stream.Tags, "language"),
				Title:     normalizeTag(stream.Tags, "title"),
				IsForced:  isForced,
				IsDefault: isDefault,
			}
			result.SubtitleStreams = append(result.SubtitleStreams, info)
		}
	}

	log.Printf("[video] ProbeVideoMetadata: found %d audio streams, %d subtitle streams", len(result.AudioStreams), len(result.SubtitleStreams))

	return result, nil
}

// ProbeVideoFull performs a single ffprobe call to get both HDR detection and stream metadata.
// This consolidates ProbeVideoPath and ProbeVideoMetadata into one call for efficiency.
// Results are cached in HLSManager.probeCache to avoid redundant probes between prequeue and HLS.
func (h *VideoHandler) ProbeVideoFull(ctx context.Context, path string) (*VideoFullResult, error) {
	if h == nil {
		return nil, errors.New("video handler is nil")
	}
	if h.ffprobePath == "" {
		return nil, errors.New("ffprobe not configured")
	}

	// Clean the path
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}

	// Check shared cache first (via HLSManager)
	if h.hlsManager != nil {
		if cached := h.hlsManager.GetCachedProbe(cleanPath); cached != nil {
			log.Printf("[video] ProbeVideoFull: using cached probe for path=%q", cleanPath)
			return h.unifiedProbeToVideoFull(cached), nil
		}
	}

	log.Printf("[video] ProbeVideoFull: probing path=%q (unified HDR + metadata)", cleanPath)

	var meta *ffprobeOutput

	// For external URLs, probe directly
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		m, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] ProbeVideoFull: ffprobe external URL failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else if h.streamer != nil {
		m, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("[video] ProbeVideoFull: ffprobe via provider failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else {
		return nil, errors.New("no stream provider configured")
	}

	if meta == nil {
		return nil, errors.New("ffprobe returned no metadata")
	}

	result := &VideoFullResult{
		AudioStreams:    make([]AudioStreamInfo, 0),
		SubtitleStreams: make([]SubtitleStreamInfo, 0),
	}

	// Extract duration from format
	if meta.Format.Duration != "" {
		if dur, err := strconv.ParseFloat(meta.Format.Duration, 64); err == nil {
			result.Duration = dur
		}
	}

	// Extract HDR info and video codec from primary video stream
	stream := selectPrimaryVideoStream(meta)
	if stream != nil {
		// Extract video codec for compatibility detection
		result.VideoCodec = strings.ToLower(strings.TrimSpace(stream.CodecName))

		// Detect Dolby Vision
		hasDV, dvProfile, _ := detectDolbyVision(stream)
		result.HasDolbyVision = hasDV
		result.DolbyVisionProfile = dvProfile

		// Detect HDR10 (PQ transfer with BT.2020)
		colorTransfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
		colorPrimaries := strings.ToLower(strings.TrimSpace(stream.ColorPrimaries))
		if colorTransfer == "smpte2084" && colorPrimaries == "bt2020" {
			result.HasHDR10 = true
		}
	}

	// Extract audio and subtitle stream info
	for i := range meta.Streams {
		s := &meta.Streams[i]
		codecType := strings.ToLower(strings.TrimSpace(s.CodecType))

		switch codecType {
		case "audio":
			codec := strings.ToLower(strings.TrimSpace(s.CodecName))
			info := AudioStreamInfo{
				Index:    s.Index,
				Codec:    codec,
				Language: normalizeTag(s.Tags, "language"),
				Title:    normalizeTag(s.Tags, "title"),
			}
			result.AudioStreams = append(result.AudioStreams, info)

			// Detect TrueHD and other incompatible audio codecs
			if codec == "truehd" || codec == "dts" || strings.HasPrefix(codec, "dts-") ||
				codec == "dts_hd" || codec == "dts-hd" || codec == "dtshd" {
				result.HasTrueHD = true
			}
			// Check for compatible codecs
			if _, ok := copyableAudioCodecs[codec]; ok {
				result.HasCompatibleAudio = true
			}

		case "subtitle":
			// Only include text-based subtitle codecs that can be converted to WebVTT
			codecName := strings.ToLower(strings.TrimSpace(s.CodecName))
			textSubtitleCodecs := map[string]bool{
				"subrip": true, "srt": true, "ass": true, "ssa": true,
				"webvtt": true, "vtt": true, "mov_text": true, "text": true,
				"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
				"mpl2": true, "pjs": true, "realtext": true, "stl": true,
				"subviewer": true, "subviewer1": true, "vplayer": true,
			}
			if !textSubtitleCodecs[codecName] {
				// Skip bitmap/unsupported subtitle formats
				continue
			}
			isForced := false
			isDefault := false
			if s.Disposition != nil {
				if f, ok := s.Disposition["forced"]; ok && f > 0 {
					isForced = true
				}
				if d, ok := s.Disposition["default"]; ok && d > 0 {
					isDefault = true
				}
			}
			info := SubtitleStreamInfo{
				Index:     s.Index,
				Codec:     codecName,
				Language:  normalizeTag(s.Tags, "language"),
				Title:     normalizeTag(s.Tags, "title"),
				IsForced:  isForced,
				IsDefault: isDefault,
			}
			result.SubtitleStreams = append(result.SubtitleStreams, info)
		}
	}

	log.Printf("[video] ProbeVideoFull: DV=%v HDR10=%v dvProfile=%q TrueHD=%v compatAudio=%v audioStreams=%d subStreams=%d videoCodec=%s",
		result.HasDolbyVision, result.HasHDR10, result.DolbyVisionProfile,
		result.HasTrueHD, result.HasCompatibleAudio,
		len(result.AudioStreams), len(result.SubtitleStreams), result.VideoCodec)

	// Cache the result for shared use between prequeue and HLS
	if h.hlsManager != nil {
		h.hlsManager.CacheProbe(cleanPath, h.videoFullToUnifiedProbe(result))
	}

	return result, nil
}

// unifiedProbeToVideoFull converts a cached UnifiedProbeResult to VideoFullResult
func (h *VideoHandler) unifiedProbeToVideoFull(cached *UnifiedProbeResult) *VideoFullResult {
	result := &VideoFullResult{
		Duration:           cached.Duration,
		VideoCodec:         cached.VideoCodec,
		HasDolbyVision:     cached.HasDolbyVision,
		HasHDR10:           cached.HasHDR10,
		DolbyVisionProfile: cached.DolbyVisionProfile,
		HasTrueHD:          cached.HasTrueHD,
		HasCompatibleAudio: cached.HasCompatibleAudio,
		AudioStreams:       make([]AudioStreamInfo, 0, len(cached.AudioStreams)),
		SubtitleStreams:    make([]SubtitleStreamInfo, 0, len(cached.SubtitleStreams)),
	}

	// Convert audio streams
	for _, as := range cached.AudioStreams {
		result.AudioStreams = append(result.AudioStreams, AudioStreamInfo{
			Index:    as.Index,
			Codec:    as.Codec,
			Language: as.Language,
			Title:    as.Title,
		})
	}

	// Convert subtitle streams
	for _, ss := range cached.SubtitleStreams {
		result.SubtitleStreams = append(result.SubtitleStreams, SubtitleStreamInfo{
			Index:     ss.Index,
			Codec:     ss.Codec,
			Language:  ss.Language,
			Title:     ss.Title,
			IsForced:  ss.IsForced,
			IsDefault: ss.IsDefault,
		})
	}

	return result
}

// videoFullToUnifiedProbe converts a VideoFullResult to UnifiedProbeResult for caching
func (h *VideoHandler) videoFullToUnifiedProbe(result *VideoFullResult) *UnifiedProbeResult {
	cached := &UnifiedProbeResult{
		Duration:           result.Duration,
		VideoCodec:         result.VideoCodec,
		HasDolbyVision:     result.HasDolbyVision,
		HasHDR10:           result.HasHDR10,
		DolbyVisionProfile: result.DolbyVisionProfile,
		HasTrueHD:          result.HasTrueHD,
		HasCompatibleAudio: result.HasCompatibleAudio,
		AudioStreams:       make([]audioStreamInfo, 0, len(result.AudioStreams)),
		SubtitleStreams:    make([]subtitleStreamInfo, 0, len(result.SubtitleStreams)),
	}

	// Convert audio streams
	for _, as := range result.AudioStreams {
		cached.AudioStreams = append(cached.AudioStreams, audioStreamInfo{
			Index:    as.Index,
			Codec:    as.Codec,
			Language: as.Language,
			Title:    as.Title,
		})
	}

	// Convert subtitle streams
	for _, ss := range result.SubtitleStreams {
		cached.SubtitleStreams = append(cached.SubtitleStreams, subtitleStreamInfo{
			Index:     ss.Index,
			Codec:     ss.Codec,
			Language:  ss.Language,
			Title:     ss.Title,
			IsForced:  ss.IsForced,
			IsDefault: ss.IsDefault,
		})
	}

	return cached
}

// proxyExternalURL proxies a pre-resolved external URL (e.g., from AIOStreams) to the client.
// It supports range requests for seeking and passes through the response from the remote server.
func (h *VideoHandler) proxyExternalURL(w http.ResponseWriter, r *http.Request, externalURL string) (bool, error) {
	log.Printf("[video] proxying external URL: %s", externalURL)

	// Handle URLs with unencoded query parameters (e.g., "?name=The Devil's Plan")
	// Split URL into base and query, properly encode the query parameters
	cleanURL := externalURL
	if qIdx := strings.Index(externalURL, "?"); qIdx >= 0 {
		baseURL := externalURL[:qIdx]
		queryStr := externalURL[qIdx+1:]

		// Parse query params - this handles unencoded values
		params, err := url.ParseQuery(queryStr)
		if err != nil {
			log.Printf("[video] query parse failed, using raw URL: %v", err)
		} else {
			// Re-encode query string properly
			cleanURL = baseURL + "?" + params.Encode()
			log.Printf("[video] external proxy: re-encoded query: %s -> %s", queryStr, params.Encode())
		}
	}

	// Parse the cleaned URL
	parsedURL, err := url.Parse(cleanURL)
	if err != nil {
		log.Printf("[video] URL parse failed: %v", err)
		http.Error(w, "invalid external URL", http.StatusBadRequest)
		return true, fmt.Errorf("parse external URL: %w", err)
	}

	log.Printf("[video] external proxy: final URL: %s (host=%s)", cleanURL, parsedURL.Host)

	// Create HTTP client with reasonable timeout
	client := &http.Client{
		Timeout: 30 * time.Minute, // Long timeout for video streaming
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects but limit the chain
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Copy headers to redirected request
			for key, values := range via[0].Header {
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}
			return nil
		},
	}

	// Create request to external URL
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	proxyReq, err := http.NewRequestWithContext(ctx, r.Method, cleanURL, nil)
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return true, fmt.Errorf("create proxy request: %w", err)
	}

	// Forward range header for seeking support
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		proxyReq.Header.Set("Range", rangeHeader)
		log.Printf("[video] external proxy: forwarding range header: %s", rangeHeader)
	}

	// Add minimal headers - some servers are picky about extra headers
	// Using a simple user agent that looks like a video player
	proxyReq.Header.Set("User-Agent", "VLC/3.0.18 LibVLC/3.0.18")
	proxyReq.Header.Set("Accept", "*/*")
	proxyReq.Header.Set("Accept-Encoding", "identity") // Don't accept compression for video streaming

	// Log request details for debugging
	log.Printf("[video] external proxy request: method=%s host=%s path=%s", proxyReq.Method, proxyReq.URL.Host, proxyReq.URL.Path)

	// Make the request
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[video] external proxy request failed: %v", err)
		http.Error(w, "failed to fetch external stream", http.StatusBadGateway)
		return true, fmt.Errorf("external request: %w", err)
	}
	defer resp.Body.Close()

	// Log response details
	contentLength := resp.Header.Get("Content-Length")
	contentRange := resp.Header.Get("Content-Range")
	acceptRanges := resp.Header.Get("Accept-Ranges")
	location := resp.Header.Get("Location")
	log.Printf("[video] external proxy response: status=%d content-length=%s content-range=%q accept-ranges=%q location=%q",
		resp.StatusCode, contentLength, contentRange, acceptRanges, location)

	// Check for error status codes
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		log.Printf("[video] external proxy error response: %d - %s", resp.StatusCode, string(body))
		// Log all response headers for debugging
		for key, values := range resp.Header {
			log.Printf("[video] external proxy error header: %s=%v", key, values)
		}
		http.Error(w, fmt.Sprintf("external stream error: %d", resp.StatusCode), resp.StatusCode)
		return true, fmt.Errorf("external stream returned %d", resp.StatusCode)
	}

	// Set CORS and common headers
	h.writeCommonHeaders(w)

	// Forward important headers from the external response
	forwardHeaders := []string{
		"Content-Type",
		"Content-Length",
		"Content-Range",
		"Accept-Ranges",
		"Content-Disposition",
		"Last-Modified",
		"ETag",
	}
	for _, header := range forwardHeaders {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}

	// Set content type if not provided
	if w.Header().Get("Content-Type") == "" {
		// Try to detect from URL extension
		ext := detectContainerExt(externalURL)
		switch ext {
		case ".mkv":
			w.Header().Set("Content-Type", "video/x-matroska")
		case ".mp4", ".m4v":
			w.Header().Set("Content-Type", "video/mp4")
		case ".avi":
			w.Header().Set("Content-Type", "video/x-msvideo")
		case ".webm":
			w.Header().Set("Content-Type", "video/webm")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}
	}

	// Write status code
	w.WriteHeader(resp.StatusCode)

	// For HEAD requests, we're done
	if r.Method == http.MethodHead {
		return true, nil
	}

	// Track this stream for admin monitoring
	tracker := GetStreamTracker()
	var expectedLength int64
	if contentLength != "" {
		if parsed, parseErr := strconv.ParseInt(contentLength, 10, 64); parseErr == nil {
			expectedLength = parsed
		}
	}
	streamID, bytesCounter := tracker.StartStream(r, externalURL, expectedLength, 0, 0)
	defer tracker.EndStream(streamID)

	// Stream the response body to the client
	buf := make([]byte, 512*1024) // 512KB buffer
	var total int64
	flusher, _ := w.(http.Flusher)
	flushCounter := 0
	const flushInterval = 1

	lastLogBytes := int64(0)
	const logInterval = 10 * 1024 * 1024 // Log every 10MB

	log.Printf("[video] starting external proxy stream: url=%q streamID=%s", externalURL, streamID)

	for {
		// Check if context is cancelled (client disconnected)
		select {
		case <-ctx.Done():
			log.Printf("[video] external proxy cancelled: url=%q total=%d reason=%v", externalURL, total, ctx.Err())
			return true, ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				if isClientGone(writeErr) || ctx.Err() == context.Canceled {
					log.Printf("[video] external proxy: client disconnected url=%q total=%d", externalURL, total)
					return true, nil
				}
				log.Printf("[video] external proxy write error: url=%q total=%d err=%v", externalURL, total, writeErr)
				return true, writeErr
			}

			total += int64(written)
			// Update stream tracking bytes counter
			if bytesCounter != nil {
				atomic.StoreInt64(bytesCounter, total)
			}
			flushCounter++

			// Periodic progress logging
			if total-lastLogBytes >= logInterval {
				log.Printf("[video] external proxy progress: url=%q total=%d", externalURL, total)
				lastLogBytes = total
			}

			// Flush periodically
			if flusher != nil && flushCounter >= flushInterval {
				flusher.Flush()
				flushCounter = 0
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("[video] external proxy read error: url=%q total=%d err=%v", externalURL, total, readErr)
				return true, readErr
			}
			// Final flush on EOF
			if flusher != nil {
				flusher.Flush()
			}
			log.Printf("[video] external proxy complete: url=%q total=%d", externalURL, total)
			break
		}
	}

	return true, nil
}

// GetDirectURL returns the direct download URL for a given path.
// This is useful for external players like Infuse that don't need our proxy.
// For debrid paths, this unrestricts the link and returns the CDN URL.
func (h *VideoHandler) GetDirectURL(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Check if provider supports direct URLs
	directProvider, ok := h.streamer.(streaming.DirectURLProvider)
	if !ok {
		http.Error(w, "direct URL not supported for this path", http.StatusNotImplemented)
		return
	}

	directURL, err := directProvider.GetDirectURL(r.Context(), path)
	if err != nil {
		if err == streaming.ErrNotFound {
			http.Error(w, "path not found", http.StatusNotFound)
			return
		}
		log.Printf("[video] GetDirectURL error for path=%q: %v", path, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	log.Printf("[video] GetDirectURL: path=%q -> %q", path, directURL)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": directURL,
	})
}

// getHDRDVPolicy returns the effective HDR/DV policy for a user/client
// Priority: client settings > user settings > global settings > default
func (h *VideoHandler) getHDRDVPolicy(userID, clientID string) models.HDRDVPolicy {
	var policy models.HDRDVPolicy

	// Layer 1: Start with global settings
	if h.configManager != nil {
		globalSettings, err := h.configManager.Load()
		if err == nil {
			policy = models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy)
		}
	}

	// Layer 2: User settings override global
	if h.userSettingsSvc != nil && userID != "" {
		userSettings, err := h.userSettingsSvc.Get(userID)
		if err == nil && userSettings != nil && userSettings.Filtering.HDRDVPolicy != "" {
			policy = userSettings.Filtering.HDRDVPolicy
		}
	}

	// Layer 3: Client settings override user
	if h.clientSettingsSvc != nil && clientID != "" {
		clientSettings, err := h.clientSettingsSvc.Get(clientID)
		if err == nil && clientSettings != nil && clientSettings.HDRDVPolicy != nil {
			policy = *clientSettings.HDRDVPolicy
			log.Printf("[video] Using client-specific HDR/DV policy: %s", policy)
		}
	}

	// Default to allowing all content
	if policy == "" {
		policy = models.HDRDVPolicyIncludeHDRDV
	}

	return policy
}

// parseDVProfileNumber extracts the profile number from a DV profile string like "dvhe.05.06"
func parseDVProfileNumber(dvProfile string) int {
	parts := strings.Split(dvProfile, ".")
	if len(parts) >= 2 {
		profile, _ := strconv.Atoi(parts[1])
		return profile
	}
	return 0
}

// checkDVPolicyViolation checks if the response contains DV profile 5 which is incompatible
// with the user's "hdr" policy (SDR + HDR only). Returns true if there's a violation.
func (h *VideoHandler) checkDVPolicyViolation(response videoMetadataResponse, profileID, clientID string) (bool, string) {
	hdrDVPolicy := h.getHDRDVPolicy(profileID, clientID)
	if hdrDVPolicy != models.HDRDVPolicyIncludeHDR {
		return false, ""
	}

	// Check all video streams for DV profile 5
	for _, vs := range response.VideoStreams {
		if vs.HasDolbyVision && vs.DolbyVisionProfile != "" {
			dvProfileNum := parseDVProfileNumber(vs.DolbyVisionProfile)
			if dvProfileNum == 5 {
				log.Printf("[video] ProbeVideo: DV profile 5 incompatible with 'hdr' policy (no HDR fallback)")
				return true, vs.DolbyVisionProfile
			}
		}
	}
	return false, ""
}

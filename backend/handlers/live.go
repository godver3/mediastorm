package handlers

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/config"
	"novastream/internal/netproxy"
	"novastream/internal/requestsecurity"
	"novastream/models"
	"novastream/services/recordings"
)

const (
	defaultPlaylistTimeout   = 2 * time.Minute
	defaultStreamOpenTimeout = 15 * time.Second
	defaultMaxPlaylistSize   = 50 * 1024 * 1024 // 50 MiB
	playlistContentTypePlain = "text/plain; charset=utf-8"
	liveStreamTimeout        = 30 * time.Minute
	defaultCacheTTL          = 24 * time.Hour
	defaultXtreamCacheTTL    = 30 * time.Minute
	defaultXtreamFailureTTL  = 5 * time.Minute
	xtreamCategoryTimeout    = 10 * time.Second
	cacheDir                 = "cache/live"

	// liveStreamUserAgent is sent on all upstream live stream requests. Some
	// providers redirect .ts requests to tokenized CDN nodes that drop any
	// connection lacking a User-Agent, so this must always be set.
	liveStreamUserAgent = "VLC/3.0.20 LibVLC/3.0.20"

	// liveBrowserUserAgent is a fallback for the Xtream metadata API. Some
	// providers whitelist only real browser User-Agents and stall every other
	// request (including VLC) until it times out. When the primary UA fails we
	// retry with this so testing/category fetches still work.
	liveBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// xtreamUserAgents is the ordered list of User-Agents tried when fetching the
// Xtream metadata API: the VLC UA used for stream playback first, then a
// browser UA as a fallback for providers that whitelist only browsers.
var xtreamUserAgents = []string{liveStreamUserAgent, liveBrowserUserAgent}

var liveStreamRequestSequence uint64

// LiveChannel represents a parsed channel from an M3U playlist.
type LiveChannel struct {
	SportsMetadata string `json:"-"` // Provider description used to reject contradictory sports series.
	ID             string `json:"id"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	Logo           string `json:"logo,omitempty"`
	Group          string `json:"group,omitempty"`
	TvgID          string `json:"tvgId,omitempty"`
	TvgName        string `json:"tvgName,omitempty"`
	TvgLanguage    string `json:"tvgLanguage,omitempty"`
	SourceID       string `json:"sourceId,omitempty"`
	SourceName     string `json:"sourceName,omitempty"`
	StreamURL      string `json:"streamUrl,omitempty"`  // Backend-proxied stream URL
	PlaybackID     string `json:"playbackId,omitempty"` // Provider-local ID used for tune-time resolution
}

// LiveSourceOption represents a selectable M3U source exposed to clients.
type LiveSourceOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LiveChannelsResponse is the response for the GetChannels endpoint.
type LiveChannelsResponse struct {
	Channels            []LiveChannel      `json:"channels"`
	TotalBeforeFilter   int                `json:"totalBeforeFilter"`
	Total               int                `json:"total"`
	Offset              int                `json:"offset"`
	Limit               int                `json:"limit"`
	HasMore             bool               `json:"hasMore"`
	AvailableCategories []string           `json:"availableCategories"`
	Sources             []LiveSourceOption `json:"sources,omitempty"`
}

const maxLiveChannelPageSize = 500

const (
	maxLiveChannelsRequestBody = 1 << 20 // 1 MiB
	maxLiveChannelCategories   = 2000
	maxLiveChannelFavoriteIDs  = 10000
	maxLiveChannelFilterBytes  = 512
)

type liveChannelsRequest struct {
	ProfileID     string   `json:"profileId"`
	SourceID      string   `json:"sourceId"`
	Offset        *int     `json:"offset,omitempty"`
	Limit         *int     `json:"limit,omitempty"`
	Filter        string   `json:"filter,omitempty"`
	Categories    []string `json:"categories,omitempty"`
	FavoriteIDs   []string `json:"favoriteIds,omitempty"`
	FavoritesOnly bool     `json:"favoritesOnly,omitempty"`
}

func parseLiveChannelPagination(r *http.Request) (offset, limit int, paginated bool, err error) {
	query := r.URL.Query()
	offsetValue, hasOffset := query["offset"]
	limitValue, hasLimit := query["limit"]
	if !hasOffset && !hasLimit {
		return 0, 0, false, nil
	}

	paginated = true
	limit = 250
	if hasOffset {
		offset, err = strconv.Atoi(strings.TrimSpace(offsetValue[len(offsetValue)-1]))
		if err != nil || offset < 0 {
			return 0, 0, false, errors.New("offset must be a non-negative integer")
		}
	}
	if hasLimit {
		limit, err = strconv.Atoi(strings.TrimSpace(limitValue[len(limitValue)-1]))
		if err != nil || limit <= 0 || limit > maxLiveChannelPageSize {
			return 0, 0, false, fmt.Errorf("limit must be between 1 and %d", maxLiveChannelPageSize)
		}
	}
	return offset, limit, true, nil
}

func parseLiveChannelsRequest(w http.ResponseWriter, r *http.Request) (liveChannelsRequest, bool, error) {
	if r.Method != http.MethodPost {
		offset, limit, paginated, err := parseLiveChannelPagination(r)
		if err != nil {
			return liveChannelsRequest{}, false, err
		}
		query := r.URL.Query()
		request := liveChannelsRequest{
			ProfileID:     query.Get("profileId"),
			SourceID:      query.Get("sourceId"),
			Filter:        query.Get("filter"),
			Categories:    query["category"],
			FavoriteIDs:   query["favoriteId"],
			FavoritesOnly: parseOptionalBool(query.Get("favoritesOnly")),
		}
		if paginated {
			request.Offset = &offset
			request.Limit = &limit
		}
		return normalizeLiveChannelsRequest(request)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLiveChannelsRequestBody)
	decoder := json.NewDecoder(r.Body)
	var request liveChannelsRequest
	if err := decoder.Decode(&request); err != nil {
		return liveChannelsRequest{}, false, fmt.Errorf("invalid JSON request: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return liveChannelsRequest{}, false, err
	}
	return normalizeLiveChannelsRequest(request)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	return nil
}

func normalizeLiveChannelsRequest(request liveChannelsRequest) (liveChannelsRequest, bool, error) {
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.Filter = strings.TrimSpace(request.Filter)

	if len(request.Categories) > maxLiveChannelCategories {
		return liveChannelsRequest{}, false, fmt.Errorf("categories must contain at most %d values", maxLiveChannelCategories)
	}
	if len(request.FavoriteIDs) > maxLiveChannelFavoriteIDs {
		return liveChannelsRequest{}, false, fmt.Errorf("favoriteIds must contain at most %d values", maxLiveChannelFavoriteIDs)
	}
	if len(request.Filter) > maxLiveChannelFilterBytes {
		return liveChannelsRequest{}, false, fmt.Errorf("filter must be at most %d bytes", maxLiveChannelFilterBytes)
	}
	request.Categories = normalizedLiveChannelValues(request.Categories)
	request.FavoriteIDs = normalizedLiveChannelValues(request.FavoriteIDs)

	paginated := request.Offset != nil || request.Limit != nil
	if !paginated {
		return request, false, nil
	}
	offset := 0
	limit := 250
	if request.Offset != nil {
		offset = *request.Offset
		if offset < 0 {
			return liveChannelsRequest{}, false, errors.New("offset must be a non-negative integer")
		}
	}
	if request.Limit != nil {
		limit = *request.Limit
		if limit <= 0 || limit > maxLiveChannelPageSize {
			return liveChannelsRequest{}, false, fmt.Errorf("limit must be between 1 and %d", maxLiveChannelPageSize)
		}
	}
	request.Offset = &offset
	request.Limit = &limit
	return request, true, nil
}

func normalizedLiveChannelValues(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func parseOptionalBool(value string) bool {
	parsed, _ := strconv.ParseBool(value)
	return parsed
}

// CategoryInfo represents category metadata.
type CategoryInfo struct {
	Name         string `json:"name"`
	ChannelCount int    `json:"channelCount"`
}

// CategoriesResponse is the response for the GetCategories endpoint.
type CategoriesResponse struct {
	Categories []CategoryInfo `json:"categories"`
}

// XtreamCategory represents a category from the Xtream API.
type XtreamCategory struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	ParentID     int    `json:"parent_id"`
}

// XtreamStream represents a live stream from the Xtream API.
type XtreamStream struct {
	Num        int    `json:"num"`
	Name       string `json:"name"`
	StreamType string `json:"stream_type"`
	StreamID   int    `json:"stream_id"`
	StreamIcon string `json:"stream_icon"`
	CategoryID string `json:"category_id"`
	EpgID      string `json:"epg_channel_id"`
}

// Regex for parsing M3U attributes
var attributeRegex = regexp.MustCompile(`([a-zA-Z0-9\-]+)="([^"]*)"`)

// splitM3ULine splits an EXTINF line to separate metadata (duration + attributes)
// from the channel display name.
// M3U format: #EXTINF:duration [key="value" ...],Channel Name
// The channel name starts after the comma that follows the last quoted attribute.
func splitM3ULine(line string) (metadata, name string) {
	// Find the position after the last closing quote of an attribute.
	// Attributes are always key="value", so we look for the last '"' and
	// then find the first comma after it.
	lastQuoteIdx := -1
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] == '"' {
			lastQuoteIdx = i
			break
		}
	}

	// Find the first comma after the last quote (or from start if no quotes)
	searchStart := 0
	if lastQuoteIdx != -1 {
		searchStart = lastQuoteIdx + 1
	}

	commaIdx := -1
	for i := searchStart; i < len(line); i++ {
		if line[i] == ',' {
			commaIdx = i
			break
		}
	}

	if commaIdx == -1 {
		// No comma found after attributes, entire line is metadata
		return line, ""
	}

	return line[:commaIdx], line[commaIdx+1:]
}

// LiveUserSettingsProvider is the minimal interface for looking up per-profile settings.
type LiveUserSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

type LiveEPGNowPlayingProvider interface {
	GetNowPlaying(channelIDs []string, timeOffset ...time.Duration) []models.EPGNowPlaying
}

type xtreamChannelsCacheEntry struct {
	channels   []LiveChannel
	expiresAt  time.Time
	retryAfter time.Time
	lastErr    error
}

type xtreamChannelsFetch struct {
	done     chan struct{}
	channels []LiveChannel
	err      error
}

// LiveHandler proxies remote M3U playlists through the backend and can transmux
// individual live channel streams into browser-friendly MP4 fragments.
type LiveHandler struct {
	discoveryOnce      sync.Once
	discovery          *liveDiscovery
	client             *http.Client
	maxSize            int64
	transmuxEnabled    bool
	ffmpegPath         string
	cacheTTL           time.Duration
	cacheMu            sync.RWMutex
	probeSizeMB        int  // FFmpeg probesize in MB (0 = default)
	analyzeDurationSec int  // FFmpeg analyzeduration in seconds (0 = default)
	lowLatency         bool // Enable low-latency mode
	cfgManager         *config.Manager
	userSettingsSvc    LiveUserSettingsProvider
	epgService         LiveEPGNowPlayingProvider

	stremioMu    sync.Mutex
	stremioCache map[string]stremioChannelsCacheEntry

	xtreamMu       sync.Mutex
	xtreamCache    map[string]xtreamChannelsCacheEntry
	xtreamInFlight map[string]*xtreamChannelsFetch
}

// SetEPGService enables pre-pagination channel filtering by current and next
// programme title in addition to channel name.
func (h *LiveHandler) SetEPGService(service LiveEPGNowPlayingProvider) {
	h.epgService = service
}

// NewLiveHandler creates a handler capable of fetching remote playlists.
// The provided client may be nil, in which case a client with sensible
// defaults will be created. cacheTTLHours specifies how long to cache playlists.
func NewLiveHandler(client *http.Client, transmuxEnabled bool, ffmpegPath string, cacheTTLHours int, probeSizeMB int, analyzeDurationSec int, lowLatency bool, cfgManager *config.Manager, userSettingsSvc LiveUserSettingsProvider) *LiveHandler {
	if client == nil {
		client = requestsecurity.NewSafeHTTPClient(defaultPlaylistTimeout, 10, configuredLiveHostPolicy(cfgManager))
	} else {
		client = secureLiveRedirects(client, configuredLiveHostPolicy(cfgManager))
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[live] failed to create cache directory: %v", err)
	}

	cacheTTL := defaultCacheTTL
	if cacheTTLHours > 0 {
		cacheTTL = time.Duration(cacheTTLHours) * time.Hour
	}

	return &LiveHandler{
		client:             client,
		maxSize:            defaultMaxPlaylistSize,
		transmuxEnabled:    transmuxEnabled,
		ffmpegPath:         strings.TrimSpace(ffmpegPath),
		cacheTTL:           cacheTTL,
		probeSizeMB:        probeSizeMB,
		analyzeDurationSec: analyzeDurationSec,
		lowLatency:         lowLatency,
		cfgManager:         cfgManager,
		userSettingsSvc:    userSettingsSvc,
		stremioCache:       make(map[string]stremioChannelsCacheEntry),
		xtreamCache:        make(map[string]xtreamChannelsCacheEntry),
		xtreamInFlight:     make(map[string]*xtreamChannelsFetch),
	}
}

func (h *LiveHandler) FetchPlaylist(w http.ResponseWriter, r *http.Request) {
	targetURL, err := h.parseRemoteURL(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check cache first
	cacheKey := h.getCacheKey(targetURL.String())
	cachedData, contentType, err := h.getFromCache(cacheKey)
	if err == nil && cachedData != nil {
		log.Printf("[live] serving playlist from cache for %s", targetURL.String())
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Cache", "HIT")
		_, _ = w.Write(cachedData)
		return
	}

	// Cache miss or expired, fetch from source
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL.String(), nil)
	if err != nil {
		http.Error(w, "failed to construct playlist request", http.StatusInternalServerError)
		return
	}

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "failed to download playlist", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		http.Error(w, http.StatusText(resp.StatusCode), resp.StatusCode)
		return
	}

	limited := io.LimitReader(resp.Body, h.maxSize+1)
	body, err := io.ReadAll(limited)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		http.Error(w, "failed to read playlist", http.StatusBadGateway)
		return
	}

	if int64(len(body)) > h.maxSize {
		http.Error(w, "playlist exceeds size limit", http.StatusRequestEntityTooLarge)
		return
	}

	contentType = resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = playlistContentTypePlain
	}

	// Store in cache
	if err := h.saveToCache(cacheKey, body, contentType); err != nil {
		log.Printf("[live] failed to cache playlist: %v", err)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Cache", "MISS")
	_, _ = w.Write(body)
}

func (h *LiveHandler) StreamChannel(w http.ResponseWriter, r *http.Request) {
	requestID := atomic.AddUint64(&liveStreamRequestSequence, 1)
	requestStartedAt := time.Now()
	requestTarget := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	requestSourceID := strings.TrimSpace(r.URL.Query().Get("liveSourceId"))
	log.Printf("[live-request] id=%d start method=%s target=%q source=%q range=%q", requestID, r.Method, requestTarget, requestSourceID, r.Header.Get("Range"))

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "none")
		w.WriteHeader(http.StatusOK)
		log.Printf("[live-request] id=%d end mode=head status=200 elapsed=%s", requestID, time.Since(requestStartedAt).Round(time.Millisecond))
		return
	}

	if !h.transmuxEnabled {
		http.Error(w, "live transmuxing disabled", http.StatusNotImplemented)
		return
	}
	if h.ffmpegPath == "" {
		http.Error(w, "ffmpeg is not configured", http.StatusServiceUnavailable)
		return
	}

	targetURL, err := h.parseRemoteURL(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		log.Printf("[live-request] id=%d reject reason=invalid-url elapsed=%s", requestID, time.Since(requestStartedAt).Round(time.Millisecond))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("[live-request] id=%d resolved upstream=%s", requestID, requestsecurity.URLForLog(targetURL.String()))

	ctx, cancel := context.WithTimeout(r.Context(), liveStreamTimeout)
	defer cancel()

	var stremioRequestHeaders map[string]string

	// Stremio sources hand us a stream *resource* URL (.../stream/{type}/{id}.json)
	// rather than a playable URL. Resolve it to a concrete (often expiring) stream
	// URL at tune-in time before handing it to the proxy/transmux paths below.
	if isStremioStreamResourceURL(targetURL) {
		selectedIndex := parseOptionalStremioStreamIndex(r.URL.Query().Get("stremioStreamIndex"))
		resolved, err := h.resolveStremioStream(ctx, targetURL.String(), h.resolveProxyURLForStream(r, targetURL), selectedIndex)
		if err != nil {
			log.Printf("[live] failed to resolve stremio stream %q: %v", targetURL.String(), err)
			http.Error(w, "live stream unavailable", http.StatusBadGateway)
			return
		}
		stremioRequestHeaders = resolved.RequestHeaders
		targetURL, err = h.parseRemoteURL(ctx, resolved.URL)
		if err != nil {
			log.Printf("[live] resolved stremio stream is invalid %q: %v", resolved.URL, err)
			http.Error(w, "live stream unavailable", http.StatusBadGateway)
			return
		}
	}

	if proxyURL := h.resolveProxyURLForStream(r, targetURL); proxyURL != "" && !isWebLiveStreamRequest(r) {
		h.proxyStreamWithHTTPClient(w, r, ctx, targetURL, proxyURL, requestID, requestStartedAt)
		return
	}

	// When a proxy is configured we cannot let ffmpeg reach the provider
	// directly: providers commonly reject non-proxy source IPs (401) and
	// redirect .ts requests to CDN nodes that require a User-Agent. Fetch the
	// stream through the proxied Go client (which sets the User-Agent and
	// follows redirects) and pipe it into ffmpeg's stdin instead.
	//
	// Note: non-web requests with a proxy are already handled above by
	// proxyStreamWithHTTPClient and return early, so this only applies to the
	// web transmux path.
	var proxyBody io.ReadCloser
	inputArg := targetURL.String()
	if proxyURL := h.resolveProxyURLForStream(r, targetURL); proxyURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
		if err != nil {
			http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
			return
		}
		req.Header.Set("User-Agent", liveStreamUserAgent)
		applyRequestHeaders(req.Header, stremioRequestHeaders)
		resp, err := h.liveStreamHTTPClient(proxyURL).Do(req)
		if err != nil {
			log.Printf("[live] proxied stream request failed for %q via %q: %v", targetURL.String(), proxyURL, err)
			http.Error(w, "failed to open proxied live stream", http.StatusBadGateway)
			return
		}
		if resp.StatusCode >= http.StatusBadRequest {
			log.Printf("[live] proxied stream returned status %d for %q via %q", resp.StatusCode, targetURL.String(), proxyURL)
			resp.Body.Close()
			http.Error(w, fmt.Sprintf("live stream returned status %d", resp.StatusCode), http.StatusBadGateway)
			return
		}
		proxyBody = resp.Body
		defer proxyBody.Close()
		inputArg = "pipe:0"
	}

	// Build FFmpeg args with optional buffering settings
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto,udp,rtp,rtmp",
	}

	// Add probesize if configured (value in MB, convert to bytes)
	if h.probeSizeMB > 0 {
		args = append(args, "-probesize", fmt.Sprintf("%d", h.probeSizeMB*1024*1024))
	}

	// Add analyzeduration if configured (value in seconds, convert to microseconds)
	if h.analyzeDurationSec > 0 {
		args = append(args, "-analyzeduration", fmt.Sprintf("%d", h.analyzeDurationSec*1000000))
	}

	// Low latency mode: reduce buffering
	if h.lowLatency {
		args = append(args, "-fflags", "+genpts+nobuffer+discardcorrupt", "-flags", "+low_delay")
	} else {
		args = append(args, "-fflags", "+genpts")
	}

	// HTTP-specific input options (User-Agent, reconnection) only apply when
	// ffmpeg reads the provider URL directly. On the proxied path the input is
	// pipe:0, and these options are rejected ("Option reconnect not found",
	// exit status 8) — the Go client already handles the User-Agent, redirects,
	// and reconnection there.
	if proxyBody == nil {
		if !hasRequestHeader(stremioRequestHeaders, "User-Agent") {
			args = append(args, "-user_agent", liveStreamUserAgent)
		}
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "3",
		)
		if headerArg := ffmpegHeadersArg(stremioRequestHeaders); headerArg != "" {
			args = append(args, "-headers", headerArg)
		}
	}

	args = append(args,
		"-i", inputArg,
		"-c:v", "copy",
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "128k",
		"-ar", "48000",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof+omit_tfhd_offset",
		"-f", "mp4",
		"-reset_timestamps", "1",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, h.ffmpegPath, args...)
	if proxyBody != nil {
		cmd.Stdin = proxyBody
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[live-request] id=%d ffmpeg-start-failed error=%v elapsed=%s", requestID, err, time.Since(requestStartedAt).Round(time.Millisecond))
		http.Error(w, "failed to start transmuxer", http.StatusBadGateway)
		return
	}
	log.Printf("[live-request] id=%d ffmpeg-start pid=%d mode=transmux", requestID, cmd.Process.Pid)

	// Capture the first chunk of ffmpeg stderr so failures are diagnosable; at
	// loglevel warning this stays small. Drain the rest to avoid blocking.
	var ffmpegStderr []byte
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		ffmpegStderr, _ = io.ReadAll(io.LimitReader(stderr, 8192))
		_, _ = io.Copy(io.Discard, stderr)
	}()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)

	tracker := GetStreamTracker()
	streamID, bytesCounter, activityCounter := tracker.StartStream(r, targetURL.String(), 0, 0, 0)
	defer tracker.EndStream(streamID)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 256*1024)
	var total int64
	endReason := "ffmpeg-eof"
	defer func() {
		log.Printf("[live-request] id=%d end mode=transmux reason=%s bytes=%d elapsed=%s", requestID, endReason, total, time.Since(requestStartedAt).Round(time.Millisecond))
	}()

	for {
		select {
		case <-ctx.Done():
			endReason = "request-context-done"
			_ = cmd.Process.Kill()
			return
		default:
		}

		n, readErr := stdout.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				endReason = "client-write-error"
				_ = cmd.Process.Kill()
				if !errors.Is(writeErr, context.Canceled) && !errors.Is(writeErr, io.EOF) && !isConnectionError(writeErr) {
					log.Printf("[live] writer error for %q: %v", targetURL.String(), writeErr)
				}
				return
			}
			total += int64(n)
			atomic.StoreInt64(bytesCounter, total)
			atomic.StoreInt64(activityCounter, time.Now().UnixNano())
			if flusher != nil {
				flusher.Flush()
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			endReason = "ffmpeg-read-error"
			_ = cmd.Process.Kill()
			log.Printf("[live] ffmpeg read error for %q: %v", targetURL.String(), readErr)
			return
		}
	}

	if err := cmd.Wait(); err != nil {
		endReason = "ffmpeg-exit-error"
		if !errors.Is(err, context.Canceled) && !strings.Contains(strings.ToLower(err.Error()), "broken pipe") {
			<-stderrDone
			detail := strings.TrimSpace(string(ffmpegStderr))
			if detail != "" {
				log.Printf("[live] ffmpeg exited with error for %q: %v; stderr: %s", targetURL.String(), err, detail)
			} else {
				log.Printf("[live] ffmpeg exited with error for %q: %v", targetURL.String(), err)
			}
		}
	}
}

func (h *LiveHandler) proxyStreamWithHTTPClient(w http.ResponseWriter, r *http.Request, ctx context.Context, targetURL *url.URL, proxyURL string, requestID uint64, requestStartedAt time.Time) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", liveStreamUserAgent)

	resp, err := h.liveStreamHTTPClient(proxyURL).Do(req)
	if err != nil {
		log.Printf("[live-request] id=%d proxy-open-failed error=%v elapsed=%s", requestID, err, time.Since(requestStartedAt).Round(time.Millisecond))
		log.Printf("[live] proxied stream request failed for %q via %q: %v", targetURL.String(), proxyURL, err)
		http.Error(w, "failed to open proxied live stream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		log.Printf("[live-request] id=%d proxy-upstream-status status=%d elapsed=%s", requestID, resp.StatusCode, time.Since(requestStartedAt).Round(time.Millisecond))
		log.Printf("[live] proxied stream returned status %d for %q via %q", resp.StatusCode, targetURL.String(), proxyURL)
		http.Error(w, fmt.Sprintf("live stream returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "video/mp2t"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)

	tracker := GetStreamTracker()
	streamID, bytesCounter, activityCounter := tracker.StartStream(r, targetURL.String(), 0, 0, 0)
	defer tracker.EndStream(streamID)

	buf := make([]byte, 256*1024)
	var total int64
	endReason := "upstream-eof"
	defer func() {
		log.Printf("[live-request] id=%d end mode=direct-proxy reason=%s bytes=%d elapsed=%s", requestID, endReason, total, time.Since(requestStartedAt).Round(time.Millisecond))
	}()
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				endReason = "client-write-error"
				if !errors.Is(writeErr, context.Canceled) && !errors.Is(writeErr, io.EOF) && !isConnectionError(writeErr) {
					log.Printf("[live] proxied stream writer error for %q: %v", targetURL.String(), writeErr)
				}
				return
			}
			total += int64(n)
			atomic.StoreInt64(bytesCounter, total)
			atomic.StoreInt64(activityCounter, time.Now().UnixNano())
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				endReason = "upstream-read-error"
			}
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, context.Canceled) {
				log.Printf("[live] proxied stream reader error for %q: %v", targetURL.String(), readErr)
			}
			return
		}
	}
}

func isWebLiveStreamRequest(r *http.Request) bool {
	target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	return target == "web" || format == "mp4"
}

func (h *LiveHandler) resolveProxyURLForStream(r *http.Request, targetURL *url.URL) string {
	if h.cfgManager == nil {
		return ""
	}
	settings, err := h.cfgManager.Load()
	if err != nil {
		log.Printf("[live] failed to load settings for stream proxy resolution: %v", err)
		return ""
	}
	src := h.resolveProfileLiveSource(r, settings)
	targetHost := normalizeHost(targetURL.Scheme + "://" + targetURL.Host)
	for _, source := range resolvedLiveSources(src) {
		if strings.TrimSpace(source.ProxyURL) == "" {
			continue
		}
		if liveSourceMatchesStreamHost(source, targetHost) {
			return source.ProxyURL
		}
	}
	return strings.TrimSpace(src.ProxyURL)
}

func liveSourceMatchesStreamHost(source resolvedM3USource, targetHost string) bool {
	for _, rawURL := range []string{source.XtreamHost, source.PlaylistURL, source.ManifestURL} {
		if strings.TrimSpace(rawURL) == "" {
			continue
		}
		if normalizeHost(rawURL) == targetHost {
			return true
		}
	}
	return false
}

func (h *LiveHandler) parseRemoteURL(ctx context.Context, raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("missing url query parameter")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return nil, errors.New("invalid playlist url")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
	if err := requestsecurity.ValidateOutboundURL(ctx, parsed.String(), configuredLiveHostPolicy(h.cfgManager)); err != nil {
		return nil, errors.New("remote URL is not allowed")
	}

	return parsed, nil
}

func (h *LiveHandler) getCacheKey(playlistURL string) string {
	hash := sha256.Sum256([]byte(playlistURL))
	return hex.EncodeToString(hash[:])
}

func (h *LiveHandler) getCacheFilePath(key string) string {
	return filepath.Join(cacheDir, key+".m3u")
}

func (h *LiveHandler) getMetaFilePath(key string) string {
	return filepath.Join(cacheDir, key+".meta")
}

func (h *LiveHandler) getFromCache(key string) ([]byte, string, error) {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()

	cacheFile := h.getCacheFilePath(key)
	metaFile := h.getMetaFilePath(key)

	// Check if cache file exists and is not expired
	stat, err := os.Stat(cacheFile)
	if err != nil {
		return nil, "", err
	}

	// Check if cache is expired
	if time.Since(stat.ModTime()) > h.cacheTTL {
		return nil, "", errors.New("cache expired")
	}

	// Read cached playlist
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, "", err
	}

	// Read content type from meta file
	contentType := playlistContentTypePlain
	if metaData, err := os.ReadFile(metaFile); err == nil {
		contentType = strings.TrimSpace(string(metaData))
	}

	return data, contentType, nil
}

func (h *LiveHandler) saveToCache(key string, data []byte, contentType string) error {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()

	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	cacheFile := h.getCacheFilePath(key)
	metaFile := h.getMetaFilePath(key)

	// Write playlist data
	if err := os.WriteFile(cacheFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	// Write content type to meta file
	if err := os.WriteFile(metaFile, []byte(contentType), 0644); err != nil {
		log.Printf("[live] failed to write meta file: %v", err)
	}

	return nil
}

// ClearCache removes all cached playlists, forcing a fresh fetch on next request.
func (h *LiveHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()

	// Read all files in cache directory
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Cache directory doesn't exist, nothing to clear
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","cleared":0}`))
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":"failed to read cache directory: %v"}`, err), http.StatusInternalServerError)
		return
	}

	cleared := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Only remove .m3u and .meta files
		if strings.HasSuffix(name, ".m3u") || strings.HasSuffix(name, ".meta") {
			path := filepath.Join(cacheDir, name)
			if err := os.Remove(path); err != nil {
				log.Printf("[live] failed to remove cache file %s: %v", name, err)
			} else {
				cleared++
			}
		}
	}

	log.Printf("[live] cleared %d cached playlist files", cleared)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"ok","cleared":%d}`, cleared)))
}

// parseM3UPlaylist parses an M3U playlist and returns a list of channels.
func parseM3UPlaylist(contents string) []LiveChannel {
	if strings.TrimSpace(contents) == "" {
		return nil
	}

	lines := strings.Split(contents, "\n")
	var channels []LiveChannel
	usedIDs := make(map[string]bool)

	assignID := func(baseID string) string {
		sanitized := strings.TrimSpace(baseID)
		if sanitized == "" {
			sanitized = "channel"
		}
		if !usedIDs[sanitized] {
			usedIDs[sanitized] = true
			return sanitized
		}
		suffix := 1
		candidate := fmt.Sprintf("%s-%d", sanitized, suffix)
		for usedIDs[candidate] {
			suffix++
			candidate = fmt.Sprintf("%s-%d", sanitized, suffix)
		}
		usedIDs[candidate] = true
		return candidate
	}

	var pending *LiveChannel
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXTINF") {
			parts := strings.SplitN(line, "#EXTINF:", 2)
			if len(parts) < 2 {
				pending = nil
				continue
			}

			metaAndName := parts[1]
			metadataPart, namePart := splitM3ULine(metaAndName)

			attributes := make(map[string]string)
			matches := attributeRegex.FindAllStringSubmatch(metadataPart, -1)
			for _, match := range matches {
				if len(match) == 3 {
					attributes[strings.ToLower(match[1])] = match[2]
				}
			}

			name := strings.TrimSpace(namePart)
			if name == "" {
				name = strings.TrimSpace(attributes["tvg-name"])
			}
			if name == "" {
				name = "Channel"
			}

			idFallbackSource := strings.TrimSpace(attributes["tvg-id"])
			if idFallbackSource == "" {
				idFallbackSource = name
			}
			if idFallbackSource == "" {
				idFallbackSource = fmt.Sprintf("channel-%d", len(channels)+1)
			}

			pending = &LiveChannel{
				ID:          idFallbackSource,
				Name:        name,
				Logo:        strings.TrimSpace(attributes["tvg-logo"]),
				Group:       strings.TrimSpace(attributes["group-title"]),
				TvgID:       strings.TrimSpace(attributes["tvg-id"]),
				TvgName:     strings.TrimSpace(attributes["tvg-name"]),
				TvgLanguage: strings.TrimSpace(attributes["tvg-language"]),
			}
			continue
		}

		if strings.HasPrefix(line, "#") {
			continue
		}

		if pending != nil {
			assignedID := assignID(pending.ID)
			if pending.Name == "" {
				pending.Name = assignedID
			}
			pending.ID = assignedID
			pending.URL = line
			channels = append(channels, *pending)
			pending = nil
		}
	}

	return channels
}

type resolvedM3USource struct {
	ID                  string
	ConfigIndex         int
	Name                string
	Mode                string
	PlaylistURL         string
	ManifestURL         string
	ProxyURL            string
	XtreamHost          string
	XtreamUsername      string
	XtreamPassword      string
	StalkerPortalURL    string
	StalkerMAC          string
	StalkerSerialNumber string
	StalkerDeviceID     string
	StalkerDeviceID2    string
	StalkerSignature    string
	StalkerModel        string
	StreamFormat        string
	MaxStreams          int
	HasFilterOverride   bool
	Filter              config.LiveTVFilterSettings
}

func resolvedLiveSources(src models.ResolvedLiveSource) []resolvedM3USource {
	var sources []resolvedM3USource
	usedIDs := make(map[string]bool)
	candidates := src.Sources
	if len(candidates) == 0 {
		candidates = src.PlaylistSources
	}
	for i, candidate := range candidates {
		mode := strings.TrimSpace(strings.ToLower(candidate.Mode))
		if mode == "" {
			mode = "m3u"
		}
		if mode == "m3u" && strings.TrimSpace(candidate.PlaylistURL) == "" {
			continue
		}
		if mode == "xtream" && (strings.TrimSpace(candidate.XtreamHost) == "" || strings.TrimSpace(candidate.XtreamUsername) == "" || strings.TrimSpace(candidate.XtreamPassword) == "") {
			continue
		}
		if mode == "stremio" && strings.TrimSpace(candidate.ManifestURL) == "" {
			continue
		}
		if mode == "stalker" && (strings.TrimSpace(candidate.StalkerPortalURL) == "" || strings.TrimSpace(candidate.StalkerMAC) == "") {
			continue
		}
		if candidate.Enabled != nil && !*candidate.Enabled {
			continue
		}
		id := strings.TrimSpace(candidate.ID)
		if id == "" {
			id = stableLiveSourceID(candidate.Name, liveSourceIdentity(candidate), i)
		}
		id = uniqueLiveSourceID(id, usedIDs)
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = fmt.Sprintf("Source %d", len(sources)+1)
		}
		var filter config.LiveTVFilterSettings
		hasFilterOverride := false
		if candidate.Filtering != nil {
			filter.EnabledCategories = candidate.Filtering.EnabledCategories
			if candidate.Filtering.MaxChannels != nil {
				filter.MaxChannels = *candidate.Filtering.MaxChannels
			}
			hasFilterOverride = candidate.Filtering.EnabledCategories != nil || candidate.Filtering.MaxChannels != nil
		} else if candidate.EnabledCategories != nil || candidate.MaxChannels != nil {
			filter.EnabledCategories = candidate.EnabledCategories
			if candidate.MaxChannels != nil {
				filter.MaxChannels = *candidate.MaxChannels
			}
			hasFilterOverride = true
		}
		sources = append(sources, resolvedM3USource{
			ID:                  id,
			ConfigIndex:         i,
			Name:                name,
			Mode:                mode,
			PlaylistURL:         strings.TrimSpace(candidate.PlaylistURL),
			ManifestURL:         strings.TrimSpace(candidate.ManifestURL),
			ProxyURL:            strings.TrimSpace(candidate.ProxyURL),
			XtreamHost:          strings.TrimSpace(candidate.XtreamHost),
			XtreamUsername:      strings.TrimSpace(candidate.XtreamUsername),
			XtreamPassword:      strings.TrimSpace(candidate.XtreamPassword),
			StalkerPortalURL:    strings.TrimSpace(candidate.StalkerPortalURL),
			StalkerMAC:          strings.TrimSpace(candidate.StalkerMAC),
			StalkerSerialNumber: strings.TrimSpace(candidate.StalkerSerialNumber),
			StalkerDeviceID:     strings.TrimSpace(candidate.StalkerDeviceID),
			StalkerDeviceID2:    strings.TrimSpace(candidate.StalkerDeviceID2),
			StalkerSignature:    strings.TrimSpace(candidate.StalkerSignature),
			StalkerModel:        strings.TrimSpace(candidate.StalkerModel),
			StreamFormat:        strings.TrimSpace(candidate.StreamFormat),
			MaxStreams:          candidate.MaxStreams,
			HasFilterOverride:   hasFilterOverride,
			Filter:              filter,
		})
	}
	if len(sources) == 0 && strings.TrimSpace(src.PlaylistURL) != "" {
		sources = append(sources, resolvedM3USource{
			ID:          "default",
			Name:        "Default",
			Mode:        "m3u",
			PlaylistURL: strings.TrimSpace(src.PlaylistURL),
			ProxyURL:    strings.TrimSpace(src.ProxyURL),
			MaxStreams:  src.MaxStreams,
		})
	}
	if len(sources) == 0 &&
		strings.EqualFold(strings.TrimSpace(src.Mode), "stremio") &&
		strings.TrimSpace(src.ManifestURL) != "" {
		sources = append(sources, resolvedM3USource{
			ID:          "default",
			Name:        "Default",
			Mode:        "stremio",
			ManifestURL: strings.TrimSpace(src.ManifestURL),
			ProxyURL:    strings.TrimSpace(src.ProxyURL),
			MaxStreams:  src.MaxStreams,
		})
	}
	if len(sources) == 0 &&
		strings.EqualFold(strings.TrimSpace(src.Mode), "stalker") &&
		strings.TrimSpace(src.StalkerPortalURL) != "" &&
		strings.TrimSpace(src.StalkerMAC) != "" {
		sources = append(sources, resolvedM3USource{
			ID: "default", Name: "Default", Mode: "stalker",
			ProxyURL:            strings.TrimSpace(src.ProxyURL),
			StalkerPortalURL:    strings.TrimSpace(src.StalkerPortalURL),
			StalkerMAC:          strings.TrimSpace(src.StalkerMAC),
			StalkerSerialNumber: strings.TrimSpace(src.StalkerSerialNumber),
			StalkerDeviceID:     strings.TrimSpace(src.StalkerDeviceID),
			StalkerDeviceID2:    strings.TrimSpace(src.StalkerDeviceID2),
			StalkerSignature:    strings.TrimSpace(src.StalkerSignature),
			StalkerModel:        strings.TrimSpace(src.StalkerModel),
			MaxStreams:          src.MaxStreams,
		})
	}
	if len(sources) == 0 &&
		strings.EqualFold(strings.TrimSpace(src.Mode), "xtream") &&
		strings.TrimSpace(src.XtreamHost) != "" &&
		strings.TrimSpace(src.XtreamUsername) != "" &&
		strings.TrimSpace(src.XtreamPassword) != "" {
		sources = append(sources, resolvedM3USource{
			ID:             "default",
			Name:           "Default",
			Mode:           "xtream",
			ProxyURL:       strings.TrimSpace(src.ProxyURL),
			XtreamHost:     strings.TrimSpace(src.XtreamHost),
			XtreamUsername: strings.TrimSpace(src.XtreamUsername),
			XtreamPassword: strings.TrimSpace(src.XtreamPassword),
			MaxStreams:     src.MaxStreams,
		})
	}
	return sources
}

func resolvedM3USources(src models.ResolvedLiveSource) []resolvedM3USource {
	var m3uSources []resolvedM3USource
	for _, source := range resolvedLiveSources(src) {
		if source.Mode == "" || source.Mode == "m3u" {
			m3uSources = append(m3uSources, source)
		}
	}
	return m3uSources
}

func liveSourceIdentity(source models.LivePlaylistSource) string {
	if strings.EqualFold(strings.TrimSpace(source.Mode), "xtream") {
		return strings.TrimSpace(source.XtreamHost) + "|" + strings.TrimSpace(source.XtreamUsername)
	}
	if strings.EqualFold(strings.TrimSpace(source.Mode), "stremio") {
		return strings.TrimSpace(source.ManifestURL)
	}
	if strings.EqualFold(strings.TrimSpace(source.Mode), "stalker") {
		return strings.TrimSpace(source.StalkerPortalURL) + "|" + strings.TrimSpace(source.StalkerMAC)
	}
	return strings.TrimSpace(source.PlaylistURL)
}

func uniqueLiveSourceID(id string, used map[string]bool) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "source"
	}
	if !used[id] {
		used[id] = true
		return id
	}
	suffix := 1
	candidate := fmt.Sprintf("%s-%d", id, suffix)
	for used[candidate] {
		suffix++
		candidate = fmt.Sprintf("%s-%d", id, suffix)
	}
	used[candidate] = true
	return candidate
}

func stableLiveSourceID(name, playlistURL string, index int) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = fmt.Sprintf("source-%d", index+1)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(playlistURL)))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:4]))
}

func liveSourceOptions(sources []resolvedM3USource) []LiveSourceOption {
	options := make([]LiveSourceOption, 0, len(sources))
	for _, src := range sources {
		options = append(options, LiveSourceOption{ID: src.ID, Name: src.Name})
	}
	return options
}

func tagChannelsWithSource(channels []LiveChannel, source resolvedM3USource, includeSourceInID bool) []LiveChannel {
	if len(channels) == 0 {
		return channels
	}
	tagged := make([]LiveChannel, len(channels))
	for i, ch := range channels {
		ch.SourceID = source.ID
		ch.SourceName = source.Name
		if includeSourceInID && source.ID != "" {
			ch.ID = source.ID + ":" + ch.ID
		}
		tagged[i] = ch
	}
	return tagged
}

func selectM3USources(sources []resolvedM3USource, sourceID string) []resolvedM3USource {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || sourceID == "all" {
		return sources
	}
	for _, src := range sources {
		if src.ID == sourceID {
			return []resolvedM3USource{src}
		}
	}
	return nil
}

// selectLiveSourceByConfigIndex preserves the settings list position even when
// disabled or incomplete sources are omitted from the resolved source list.
func selectLiveSourceByConfigIndex(sources []resolvedM3USource, rawIndex string) []resolvedM3USource {
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 {
		return nil
	}
	for _, source := range sources {
		if source.ConfigIndex == index {
			return []resolvedM3USource{source}
		}
	}
	return nil
}

// extractCategories extracts unique categories with their channel counts from a list of channels.
func extractCategories(channels []LiveChannel) []CategoryInfo {
	categoryMap := make(map[string]int)
	for _, ch := range channels {
		if ch.Group != "" {
			categoryMap[ch.Group]++
		}
	}

	return categoryInfosFromCounts(categoryMap)
}

func categoryInfosFromCounts(categoryMap map[string]int) []CategoryInfo {
	categories := make([]CategoryInfo, 0, len(categoryMap))
	for name, count := range categoryMap {
		categories = append(categories, CategoryInfo{
			Name:         name,
			ChannelCount: count,
		})
	}

	sort.Slice(categories, func(i, j int) bool {
		return categories[i].Name < categories[j].Name
	})

	return categories
}

// filterChannels applies the filtering settings to a list of channels.
func filterChannels(channels []LiveChannel, filter config.LiveTVFilterSettings) []LiveChannel {
	filtered := filterChannelsByEnabledCategories(channels, filter.EnabledCategories)
	if filter.MaxChannels > 0 && len(filtered) > filter.MaxChannels {
		filtered = filtered[:filter.MaxChannels]
	}
	return filtered
}

func filterChannelsByEnabledCategories(channels []LiveChannel, enabledCategories []string) []LiveChannel {
	if len(channels) == 0 {
		return channels
	}

	var filtered []LiveChannel
	if len(enabledCategories) > 0 {
		enabledSet := make(map[string]bool)
		for _, cat := range enabledCategories {
			enabledSet[cat] = true
		}
		for _, ch := range channels {
			if enabledSet[ch.Group] {
				filtered = append(filtered, ch)
			}
		}
	} else {
		filtered = channels
	}
	return filtered
}

func filterChannelsByRequestedCategories(channels []LiveChannel, categories, favoriteIDs []string, sourceID string, includeSourceInID bool) []LiveChannel {
	if len(channels) == 0 || len(categories) == 0 {
		return channels
	}
	selected := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		selected[strings.TrimSpace(category)] = struct{}{}
	}
	hasValidSelection := false
	for _, channel := range channels {
		if _, ok := selected[channel.Group]; ok {
			hasValidSelection = true
			break
		}
	}
	if !hasValidSelection {
		return channels
	}
	filtered := make([]LiveChannel, 0, len(channels))
	for _, channel := range channels {
		_, categorySelected := selected[channel.Group]
		favoriteID := channel.ID
		if includeSourceInID && sourceID != "" {
			favoriteID = sourceID + ":" + favoriteID
		}
		favorite := false
		for _, requestedFavoriteID := range favoriteIDs {
			if requestedFavoriteID == favoriteID {
				favorite = true
				break
			}
		}
		if categorySelected || favorite {
			filtered = append(filtered, channel)
		}
	}
	return filtered
}

func filterLiveChannelsByText(
	channels []LiveChannel,
	filterText string,
	favoriteIDs map[string]struct{},
	epgService LiveEPGNowPlayingProvider,
	epgTimeOffset time.Duration,
) []LiveChannel {
	query := strings.ToLower(strings.TrimSpace(filterText))
	if query == "" {
		return channels
	}

	matches := make(map[string]struct{}, len(channels))
	channelIDs := make([]string, 0, len(channels))
	channelIDsToLiveIDs := make(map[string][]string, len(channels))
	for _, channel := range channels {
		if _, favorite := favoriteIDs[channel.ID]; favorite {
			matches[channel.ID] = struct{}{}
			continue
		}
		if strings.Contains(strings.ToLower(channel.Name), query) {
			matches[channel.ID] = struct{}{}
			continue
		}
		if epgService != nil && strings.TrimSpace(channel.TvgID) != "" {
			channelIDs = append(channelIDs, channel.TvgID)
			lookupID := strings.ToLower(channel.TvgID)
			channelIDsToLiveIDs[lookupID] = append(channelIDsToLiveIDs[lookupID], channel.ID)
		}
	}

	if len(channelIDs) > 0 {
		for _, nowPlaying := range epgService.GetNowPlaying(channelIDs, -epgTimeOffset) {
			currentMatches := nowPlaying.Current != nil && strings.Contains(strings.ToLower(nowPlaying.Current.Title), query)
			nextMatches := nowPlaying.Next != nil && strings.Contains(strings.ToLower(nowPlaying.Next.Title), query)
			if !currentMatches && !nextMatches {
				continue
			}
			for _, channelID := range channelIDsToLiveIDs[strings.ToLower(nowPlaying.ChannelID)] {
				matches[channelID] = struct{}{}
			}
		}
	}

	filtered := make([]LiveChannel, 0, len(matches))
	for _, channel := range channels {
		if _, match := matches[channel.ID]; match {
			filtered = append(filtered, channel)
		}
	}
	return filtered
}

func orderFavoriteChannelsFirst(channels []LiveChannel, favoriteIDs map[string]struct{}) []LiveChannel {
	if len(favoriteIDs) == 0 {
		return channels
	}

	ordered := make([]LiveChannel, 0, len(channels))
	for _, channel := range channels {
		if _, favorite := favoriteIDs[channel.ID]; favorite {
			ordered = append(ordered, channel)
		}
	}
	for _, channel := range channels {
		if _, favorite := favoriteIDs[channel.ID]; !favorite {
			ordered = append(ordered, channel)
		}
	}
	return ordered
}

// fetchPlaylistContents fetches the M3U playlist from the given URL.
func (h *LiveHandler) fetchPlaylistContents(ctx context.Context, playlistURL, proxyURL string) (string, error) {
	if strings.TrimSpace(playlistURL) == "" {
		return "", errors.New("no playlist URL configured")
	}

	targetURL, err := h.parseRemoteURL(ctx, playlistURL)
	if err != nil {
		return "", err
	}

	// Check cache first
	cacheKey := h.getCacheKey(targetURL.String() + "\x00" + proxyURL)
	cachedData, _, err := h.getFromCache(cacheKey)
	if err == nil && cachedData != nil {
		log.Printf("[live] serving playlist from cache for channels endpoint")
		return string(cachedData), nil
	}

	// Fetch from source
	log.Printf("[live] fetching playlist from: %s", targetURL.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to construct playlist request: %w", err)
	}
	// Some providers block requests lacking a recognized player/browser
	// User-Agent, so set the same UA used for stream playback.
	req.Header.Set("User-Agent", liveStreamUserAgent)

	resp, err := h.discoveryClient(h.liveHTTPClient(proxyURL), proxyURL, time.Minute).Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download playlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return "", metadataResponseError(resp)
	}

	limited := io.LimitReader(resp.Body, h.maxSize+1)
	body, err := io.ReadAll(limited)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("failed to read playlist: %w", err)
	}

	if int64(len(body)) > h.maxSize {
		return "", errors.New("playlist exceeds size limit")
	}

	// Cache the playlist
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = playlistContentTypePlain
	}
	if err := h.saveToCache(cacheKey, body, contentType); err != nil {
		log.Printf("[live] failed to cache playlist: %v", err)
	}

	return string(body), nil
}

func (h *LiveHandler) fetchM3UCategories(ctx context.Context, playlistURL, proxyURL string) ([]CategoryInfo, error) {
	if strings.TrimSpace(playlistURL) == "" {
		return nil, errors.New("no playlist URL configured")
	}

	targetURL, err := h.parseRemoteURL(ctx, playlistURL)
	if err != nil {
		return nil, err
	}

	cacheKey := h.getCacheKey(targetURL.String())
	if cachedData, _, err := h.getFromCache(cacheKey); err == nil && cachedData != nil {
		return extractCategories(parseM3UPlaylist(string(cachedData))), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to construct playlist request: %w", err)
	}

	resp, err := h.discoveryClient(h.livePlaylistScanHTTPClient(proxyURL), proxyURL, time.Minute).Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download playlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, metadataResponseError(resp)
	}

	counts := make(map[string]int)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	pendingGroup := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF") {
			pendingGroup = ""
			parts := strings.SplitN(line, "#EXTINF:", 2)
			if len(parts) < 2 {
				continue
			}
			metadataPart, _ := splitM3ULine(parts[1])
			matches := attributeRegex.FindAllStringSubmatch(metadataPart, -1)
			for _, match := range matches {
				if len(match) == 3 && strings.EqualFold(match[1], "group-title") {
					pendingGroup = strings.TrimSpace(match[2])
					break
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pendingGroup != "" {
			counts[pendingGroup]++
			pendingGroup = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read playlist categories: %w", err)
	}

	return categoryInfosFromCounts(counts), nil
}

func (h *LiveHandler) liveHTTPClient(proxyURL string) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return h.client
	}
	client, err := netproxy.NewHTTPClient(defaultPlaylistTimeout, proxyURL)
	if err != nil {
		log.Printf("[live] invalid proxy URL %q: %v", proxyURL, err)
		return h.client
	}
	return secureLiveRedirects(client, configuredLiveHostPolicy(h.cfgManager))
}

func (h *LiveHandler) livePlaylistScanHTTPClient(proxyURL string) *http.Client {
	client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
		ResponseHeaderTimeout: defaultPlaylistTimeout,
	}, proxyURL)
	if err != nil {
		log.Printf("[live] invalid playlist scan proxy URL %q: %v", proxyURL, err)
		client, _ = netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
			ResponseHeaderTimeout: defaultPlaylistTimeout,
		}, "")
	}
	return secureLiveRedirects(client, configuredLiveHostPolicy(h.cfgManager))
}

func (h *LiveHandler) liveStreamHTTPClient(proxyURL string) *http.Client {
	return h.liveStreamHTTPClientWithTimeout(proxyURL, defaultStreamOpenTimeout)
}

func (h *LiveHandler) liveStreamHTTPClientWithTimeout(proxyURL string, headerTimeout time.Duration) *http.Client {
	client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
		ResponseHeaderTimeout: headerTimeout,
	}, proxyURL)
	if err != nil {
		log.Printf("[live] invalid stream proxy URL %q: %v", proxyURL, err)
		client, _ = netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
			ResponseHeaderTimeout: headerTimeout,
		}, "")
	}
	return secureLiveRedirects(client, configuredLiveHostPolicy(h.cfgManager))
}

func configuredLiveHostPolicy(manager *config.Manager) requestsecurity.RestrictedHostPolicy {
	if manager == nil {
		return nil
	}
	return configuredProviderHostPolicy(manager)
}

func secureLiveRedirects(client *http.Client, policy requestsecurity.RestrictedHostPolicy) *http.Client {
	if client == nil {
		return requestsecurity.NewSafeHTTPClient(defaultPlaylistTimeout, 10, policy)
	}
	copyClient := *client
	previous := copyClient.CheckRedirect
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if err := requestsecurity.ValidateOutboundURL(req.Context(), req.URL.String(), policy); err != nil {
			return err
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
	return &copyClient
}

// WarmPlaylistCache fetches configured Live TV sources so later category/channel
// requests can use the backend playlist cache.
func (h *LiveHandler) WarmPlaylistCache(ctx context.Context) (int, error) {
	if h.cfgManager == nil {
		return 0, errors.New("settings manager not configured")
	}

	settings, err := h.cfgManager.Load()
	if err != nil {
		return 0, fmt.Errorf("failed to load settings: %w", err)
	}

	src := models.ResolvedLiveSource{
		Mode:                settings.Live.Mode,
		PlaylistURL:         settings.Live.PlaylistURL,
		ManifestURL:         settings.Live.ManifestURL,
		ProxyURL:            settings.Live.ProxyURL,
		XtreamHost:          settings.Live.XtreamHost,
		XtreamUsername:      settings.Live.XtreamUsername,
		XtreamPassword:      settings.Live.XtreamPassword,
		StalkerPortalURL:    settings.Live.StalkerPortalURL,
		StalkerMAC:          settings.Live.StalkerMAC,
		StalkerSerialNumber: settings.Live.StalkerSerialNumber,
		StalkerDeviceID:     settings.Live.StalkerDeviceID,
		StalkerDeviceID2:    settings.Live.StalkerDeviceID2,
		StalkerSignature:    settings.Live.StalkerSignature,
		StalkerModel:        settings.Live.StalkerModel,
		PlaylistSources:     configPlaylistSourcesToModel(settings.Live.PlaylistSources),
		Sources:             configPlaylistSourcesToModel(settings.Live.Sources),
	}

	sources := resolvedLiveSources(src)
	if len(sources) == 0 {
		return 0, errors.New("no Live TV source configured")
	}

	totalChannels := 0
	for _, liveSource := range sources {
		if liveSource.Mode == "xtream" {
			channels, err := h.fetchXtreamChannels(ctx, liveSource.XtreamHost, liveSource.XtreamUsername, liveSource.XtreamPassword, liveSource.ProxyURL)
			if err != nil {
				return totalChannels, fmt.Errorf("failed to warm Xtream source %q: %w", liveSource.ID, err)
			}
			totalChannels += len(channels)
			continue
		}
		if liveSource.Mode == "stremio" {
			channels, err := h.fetchStremioChannels(ctx, liveSource.ManifestURL, liveSource.ProxyURL)
			if err != nil {
				return totalChannels, fmt.Errorf("failed to warm Stremio source %q: %w", liveSource.ID, err)
			}
			totalChannels += len(channels)
			continue
		}
		if liveSource.Mode == "stalker" {
			channels, err := fetchStalkerChannels(ctx, stalkerConfigFromResolvedSource(liveSource))
			if err != nil {
				return totalChannels, fmt.Errorf("failed to warm Stalker source %q: %w", liveSource.ID, err)
			}
			totalChannels += len(channels)
			continue
		}

		contents, err := h.fetchPlaylistContents(ctx, liveSource.PlaylistURL, liveSource.ProxyURL)
		if err != nil {
			return totalChannels, fmt.Errorf("failed to warm M3U source %q: %w", liveSource.ID, err)
		}
		totalChannels += len(parseM3UPlaylist(contents))
	}

	return totalChannels, nil
}

// fetchXtreamWithUAFallback performs a GET against an Xtream metadata URL,
// trying each candidate User-Agent until one returns a usable (non-error)
// response. It returns the live response, the User-Agent that worked (so the
// caller can reuse it for follow-up requests), or the last error encountered.
// The caller owns closing the returned body.
func (h *LiveHandler) fetchXtreamWithUAFallback(ctx context.Context, client *http.Client, requestURL string) (*http.Response, string, error) {
	var lastErr error
	attemptClient := *client
	if attemptClient.Timeout == 0 || attemptClient.Timeout > xtreamCategoryTimeout {
		attemptClient.Timeout = xtreamCategoryTimeout
	}
	for _, ua := range xtreamUserAgents {
		// Stop early if the caller's context is already done — retrying with a
		// different UA won't help a cancelled/expired request.
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, "", lastErr
			}
			return nil, "", err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", ua)

		resp, err := attemptClient.Do(req)
		if err != nil {
			lastErr = xtreamRequestError(err)
			log.Printf("[live] Xtream request failed with UA %q: %v", ua, lastErr)
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode == 401 {
			return resp, ua, nil
		}
		if resp.StatusCode >= http.StatusBadRequest {
			resp.Body.Close()
			lastErr = metadataResponseError(resp)
			log.Printf("[live] Xtream request returned status %d with UA %q", resp.StatusCode, ua)
			continue
		}
		return resp, ua, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no User-Agent candidates configured")
	}
	return nil, "", lastErr
}

// net/http includes the full credential-bearing request URL in *url.Error.
func xtreamRequestError(err error) error {
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		return requestErr.Err
	}
	return err
}

// fetchXtreamChannels fetches live channels from the Xtream Codes API.
func (h *LiveHandler) fetchXtreamChannels(ctx context.Context, host, username, password, proxyURL string) ([]LiveChannel, error) {
	identity := host + "\x00" + username + "\x00" + password + "\x00" + proxyURL
	keyBytes := sha256.Sum256([]byte(identity))
	cacheKey := hex.EncodeToString(keyBytes[:])

	h.xtreamMu.Lock()
	cached, hasCache := h.xtreamCache[cacheKey]
	if hasCache && time.Now().Before(cached.expiresAt) {
		channels := cached.channels
		h.xtreamMu.Unlock()
		log.Printf("[live] serving %d Xtream channels from memory cache", len(channels))
		return channels, nil
	}
	if hasCache && time.Now().Before(cached.retryAfter) {
		h.xtreamMu.Unlock()
		if !cached.expiresAt.IsZero() {
			log.Printf("[live] serving %d stale Xtream channels during provider failure cooldown", len(cached.channels))
			return cached.channels, nil
		}
		return nil, cached.lastErr
	}
	if inFlight, ok := h.xtreamInFlight[cacheKey]; ok {
		h.xtreamMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-inFlight.done:
			return inFlight.channels, inFlight.err
		}
	}
	inFlight := &xtreamChannelsFetch{done: make(chan struct{})}
	h.xtreamInFlight[cacheKey] = inFlight
	h.xtreamMu.Unlock()

	// Finish and retain a catalog refresh even if the initiating app request is
	// cancelled. Other concurrent requests share this fetch, and the next cold
	// app launch can use the populated backend cache.
	go func() {
		fetchCtx, cancel := context.WithTimeout(context.Background(), defaultPlaylistTimeout)
		channels, err := h.fetchXtreamChannelsUncached(fetchCtx, host, username, password, proxyURL)
		cancel()

		h.xtreamMu.Lock()
		if err == nil {
			h.xtreamCache[cacheKey] = xtreamChannelsCacheEntry{
				channels:  channels,
				expiresAt: time.Now().Add(defaultXtreamCacheTTL),
			}
		} else {
			cached := h.xtreamCache[cacheKey]
			cached.retryAfter = time.Now().Add(defaultXtreamFailureTTL)
			cached.lastErr = err
			h.xtreamCache[cacheKey] = cached
			if !cached.expiresAt.IsZero() {
				channels, err = cached.channels, nil
				log.Printf("[live] Xtream refresh failed; serving %d stale channels for %s", len(channels), defaultXtreamFailureTTL)
			} else {
				log.Printf("[live] Xtream refresh failed; suppressing retries for %s", defaultXtreamFailureTTL)
			}
		}
		inFlight.channels = channels
		inFlight.err = err
		delete(h.xtreamInFlight, cacheKey)
		close(inFlight.done)
		h.xtreamMu.Unlock()

	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-inFlight.done:
		return inFlight.channels, inFlight.err
	}
}

func (h *LiveHandler) fetchXtreamChannelsUncached(ctx context.Context, host, username, password, proxyURL string, categoryNames ...string) ([]LiveChannel, error) {
	host = strings.TrimRight(host, "/")

	// Fetch categories first to build a category ID -> name map
	categoriesURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_live_categories",
		host, url.QueryEscape(username), url.QueryEscape(password))

	log.Printf("[live] fetching Xtream categories host=%q", host)

	ttl := time.Duration(0) // The full catalog has its own shared cache.
	if len(categoryNames) > 0 {
		ttl = 5 * time.Minute
	}
	client := h.discoveryClient(h.liveHTTPClient(proxyURL), proxyURL, ttl)

	// Try each candidate User-Agent in turn: some providers whitelist only VLC,
	// others only real browsers, and stall every other request until it times
	// out. Remember whichever UA succeeded so the streams call reuses it instead
	// of paying the fallback cost twice.
	catResp, workingUA, err := h.fetchXtreamWithUAFallback(ctx, client, categoriesURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch categories: %w", err)
	}
	defer catResp.Body.Close()
	if catResp.StatusCode >= 400 {
		return nil, metadataResponseError(catResp)
	}

	var categories []XtreamCategory
	if err := json.NewDecoder(catResp.Body).Decode(&categories); err != nil {
		return nil, fmt.Errorf("failed to decode categories: %w", err)
	}

	// Build category ID -> name map
	categoryMap := make(map[string]string)
	for _, cat := range categories {
		categoryMap[cat.CategoryID] = cat.CategoryName
	}

	// Fetch streams
	streamsURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_live_streams",
		host, url.QueryEscape(username), url.QueryEscape(password))

	log.Printf("[live] fetching Xtream streams host=%q", host)

	endpoints := []string{streamsURL}
	if len(categoryNames) > 0 {
		endpoints = nil
		for _, cat := range categories {
			for _, name := range categoryNames {
				if strings.EqualFold(strings.TrimSpace(cat.CategoryName), strings.TrimSpace(name)) {
					endpoints = append(endpoints, streamsURL+"&category_id="+url.QueryEscape(cat.CategoryID))
					break
				}
			}
		}
	}
	var streams []XtreamStream
	for _, endpoint := range endpoints {
		streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		streamReq.Header.Set("User-Agent", workingUA)
		streamResp, err := client.Do(streamReq)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch streams: %w", xtreamRequestError(err))
		}
		if streamResp.StatusCode >= 400 {
			streamResp.Body.Close()
			return nil, metadataResponseError(streamResp)
		}
		var page []XtreamStream
		err = json.NewDecoder(streamResp.Body).Decode(&page)
		streamResp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to decode streams: %w", err)
		}
		streams = append(streams, page...)
	}

	// Convert to LiveChannel format
	// Stream URL format: http://host:port/username/password/stream_id.ts
	channels := make([]LiveChannel, 0, len(streams))
	for _, stream := range streams {
		if stream.StreamType != "live" {
			continue
		}

		streamURL := fmt.Sprintf("%s/live/%s/%s/%d.ts",
			host, username, password, stream.StreamID)

		channel := LiveChannel{
			ID:      fmt.Sprintf("%d", stream.StreamID),
			Name:    stream.Name,
			URL:     streamURL,
			Logo:    stream.StreamIcon,
			Group:   categoryMap[stream.CategoryID],
			TvgID:   stream.EpgID,
			TvgName: stream.Name,
		}
		channels = append(channels, channel)
	}

	log.Printf("[live] fetched %d channels from Xtream API", len(channels))
	return channels, nil
}

// isXtreamMode checks if the current configuration is using Xtream Codes mode.
func (h *LiveHandler) isXtreamMode() bool {
	if h.cfgManager == nil {
		return false
	}
	settings, err := h.cfgManager.Load()
	if err != nil {
		return false
	}
	return settings.Live.Mode == "xtream" &&
		settings.Live.XtreamHost != "" &&
		settings.Live.XtreamUsername != "" &&
		settings.Live.XtreamPassword != ""
}

// resolveProfileLiveSource resolves the IPTV source for a legacy query-based
// request. If the profile has per-profile IPTV overrides, those are merged
// with the global settings.
func (h *LiveHandler) resolveProfileLiveSource(r *http.Request, globalSettings config.Settings) models.ResolvedLiveSource {
	return h.resolveProfileLiveSourceForID(r.URL.Query().Get("profileId"), globalSettings)
}

func (h *LiveHandler) resolveProfileLiveSourceForID(profileID string, globalSettings config.Settings) models.ResolvedLiveSource {
	globalSettings = config.FilterSettingsForProfile(globalSettings, profileID)
	global := models.ResolvedLiveSource{
		Mode:                    globalSettings.Live.Mode,
		PlaylistURL:             globalSettings.Live.PlaylistURL,
		ManifestURL:             globalSettings.Live.ManifestURL,
		Sources:                 configPlaylistSourcesToModel(globalSettings.Live.Sources),
		PlaylistSources:         configPlaylistSourcesToModel(globalSettings.Live.PlaylistSources),
		XtreamHost:              globalSettings.Live.XtreamHost,
		XtreamUsername:          globalSettings.Live.XtreamUsername,
		XtreamPassword:          globalSettings.Live.XtreamPassword,
		StalkerPortalURL:        globalSettings.Live.StalkerPortalURL,
		StalkerMAC:              globalSettings.Live.StalkerMAC,
		StalkerSerialNumber:     globalSettings.Live.StalkerSerialNumber,
		StalkerDeviceID:         globalSettings.Live.StalkerDeviceID,
		StalkerDeviceID2:        globalSettings.Live.StalkerDeviceID2,
		StalkerSignature:        globalSettings.Live.StalkerSignature,
		StalkerModel:            globalSettings.Live.StalkerModel,
		ProxyURL:                globalSettings.Live.ProxyURL,
		MaxStreams:              globalSettings.Live.MaxStreams,
		PlaylistCacheTTLHours:   globalSettings.Live.PlaylistCacheTTLHours,
		ProbeSizeMB:             globalSettings.Live.ProbeSizeMB,
		AnalyzeDurationSec:      globalSettings.Live.AnalyzeDurationSec,
		LowLatency:              globalSettings.Live.LowLatency,
		StreamFormat:            globalSettings.Live.StreamFormat,
		EnabledCategories:       globalSettings.Live.Filtering.EnabledCategories,
		MaxChannels:             globalSettings.Live.Filtering.MaxChannels,
		EPGEnabled:              globalSettings.Live.EPG.Enabled,
		EPGXmltvUrl:             globalSettings.Live.EPG.XmltvUrl,
		EPGRefreshIntervalHours: globalSettings.Live.EPG.RefreshIntervalHours,
		EPGRetentionDays:        globalSettings.Live.EPG.RetentionDays,
		EPGTimeOffsetMinutes:    globalSettings.Live.EPG.TimeOffsetMinutes,
	}

	if profileID == "" || h.userSettingsSvc == nil {
		return global
	}

	userSettings, err := h.userSettingsSvc.Get(profileID)
	if err != nil || userSettings == nil {
		return global
	}

	return models.ResolveLiveSource(&userSettings.LiveTV, &global)
}

// FetchFilteredChannelsForRequest resolves the caller's configured Live TV sources
// (honoring profileId/sourceId query params the same way GetChannels does) and returns
// the combined, admin-filtered channel list. Used by non-Live-TV callers (e.g. the sports
// game-stream matcher) that need the same channel set a user would see in the Live TV tab.
func (h *LiveHandler) FetchFilteredChannelsForRequest(r *http.Request) ([]LiveChannel, error) {
	settings, err := h.cfgManager.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	src := h.resolveProfileLiveSource(r, settings)
	filter := config.LiveTVFilterSettings{EnabledCategories: src.EnabledCategories, MaxChannels: src.MaxChannels}
	sources := resolvedLiveSources(src)
	selected := selectM3USources(sources, r.URL.Query().Get("sourceId"))
	discovery := sportsDiscoveryFrom(r.Context())
	results := make([][]LiveChannel, len(selected))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for n := 0; n < min(4, len(selected)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				source := selected[index]
				if discovery != nil && !containsSource(discovery.sources, source.ID) {
					continue
				}
				sourceFilter := filter
				if source.HasFilterOverride {
					sourceFilter = source.Filter
				}
				var channels []LiveChannel
				var err error
				switch source.Mode {
				case "xtream":
					if discovery != nil && len(discovery.categories) > 0 && sourceFilter.MaxChannels == 0 {
						channels, err = h.fetchXtreamChannelsUncached(r.Context(), source.XtreamHost, source.XtreamUsername, source.XtreamPassword, source.ProxyURL, discovery.categories...)
					} else {
						channels, err = h.fetchXtreamChannels(r.Context(), source.XtreamHost, source.XtreamUsername, source.XtreamPassword, source.ProxyURL)
					}
				case "stremio":
					if discovery != nil {
						channels, err = h.fetchSportsAddon(r.Context(), source.ManifestURL, source.ProxyURL, discovery, sourceFilter)
					} else {
						channels, err = h.fetchStremioChannels(r.Context(), source.ManifestURL, source.ProxyURL)
					}
				default:
					var contents string
					contents, err = h.fetchPlaylistContents(r.Context(), source.PlaylistURL, source.ProxyURL)
					if err == nil {
						channels = h.parsedDiscoveryPlaylist(contents)
					}
				}
				if discovery != nil {
					discovery.record(source.ID, source.Name, err)
				}
				if err != nil && len(channels) == 0 {
					continue
				}
				visible := tagChannelsWithSource(filterChannels(channels, sourceFilter), source, len(sources) > 1)
				if discovery != nil {
					visible = filterSportsChannelsByScope(visible, nil, discovery.categories)
				}
				results[index] = visible
			}
		}()
	}
	for index := range selected {
		select {
		case jobs <- index:
		case <-r.Context().Done():
			close(jobs)
			workers.Wait()
			return nil, r.Context().Err()
		}
	}
	close(jobs)
	workers.Wait()
	var all []LiveChannel
	for _, channels := range results {
		all = append(all, channels...)
	}
	return all, nil
}

// RecordingChannels resolves the complete visible channel list for a profile.
func (h *LiveHandler) RecordingChannels(ctx context.Context, profileID string) ([]recordings.Channel, error) {
	query := url.Values{}
	query.Set("profileId", profileID)
	request := httptest.NewRequest(http.MethodGet, "/api/live/channels?"+query.Encode(), nil).WithContext(ctx)
	response := httptest.NewRecorder()
	h.GetChannels(response, request)
	if response.Code < http.StatusOK || response.Code >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("load profile Live TV channels: %s", strings.TrimSpace(response.Body.String()))
	}
	var payload LiveChannelsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode profile Live TV channels: %w", err)
	}
	channels := make([]recordings.Channel, 0, len(payload.Channels))
	for _, channel := range payload.Channels {
		channels = append(channels, recordings.Channel{
			ID: channel.ID, TvgID: channel.TvgID, Name: channel.Name, SourceURL: channel.URL,
		})
	}
	return channels, nil
}

// GetChannels returns parsed and filtered channels from the configured playlist.
func (h *LiveHandler) GetChannels(w http.ResponseWriter, r *http.Request) {
	requestStartedAt := time.Now()
	var allChannels []LiveChannel
	var categorySourceChannels []LiveChannel
	stalkerCategoryTotal := -1
	var stalkerAvailableCategories []string
	request, paginated, err := parseLiveChannelsRequest(w, r)
	if err != nil {
		status := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), status)
		return
	}
	offset := 0
	limit := 0
	if paginated {
		offset = *request.Offset
		limit = *request.Limit
	}
	profileID := request.ProfileID
	requestedSourceID := request.SourceID
	log.Printf(
		"[live] GetChannels request start profileId=%q sourceId=%q paginated=%t offset=%d limit=%d categoryCount=%d favoriteCount=%d favoritesOnly=%q filterLength=%d",
		profileID,
		requestedSourceID,
		paginated,
		offset,
		limit,
		len(request.Categories),
		len(request.FavoriteIDs),
		strconv.FormatBool(request.FavoritesOnly),
		len(request.Filter),
	)

	settings, err := h.cfgManager.Load()
	if err != nil {
		log.Printf("[live] GetChannels error loading settings: %v", err)
		http.Error(w, `{"error":"failed to load settings"}`, http.StatusInternalServerError)
		return
	}

	src := h.resolveProfileLiveSourceForID(profileID, settings)
	filter := config.LiveTVFilterSettings{
		EnabledCategories: src.EnabledCategories,
		MaxChannels:       src.MaxChannels,
	}

	sources := resolvedLiveSources(src)
	if len(sources) == 0 {
		log.Printf(
			"[live] GetChannels error: no configured source profileId=%q sourceId=%q duration=%s",
			profileID,
			requestedSourceID,
			time.Since(requestStartedAt).Round(time.Millisecond),
		)
		http.Error(w, `{"error":"No Live TV source is configured for this profile. Add a provider in settings or switch to a configured profile."}`, http.StatusConflict)
		return
	}
	selectedSources := selectM3USources(sources, requestedSourceID)
	if len(selectedSources) == 0 {
		log.Printf(
			"[live] GetChannels error: unknown source profileId=%q sourceId=%q resolvedSourceCount=%d",
			profileID,
			requestedSourceID,
			len(sources),
		)
		http.Error(w, `{"error":"unknown source"}`, http.StatusBadRequest)
		return
	}
	selectedModes := make([]string, 0, len(selectedSources))
	for _, source := range selectedSources {
		selectedModes = append(selectedModes, source.Mode)
	}
	log.Printf(
		"[live] GetChannels sources resolved profileId=%q sourceId=%q resolved=%d selected=%d modes=%v filterCategoryCount=%d maxChannels=%d",
		profileID,
		requestedSourceID,
		len(sources),
		len(selectedSources),
		selectedModes,
		len(filter.EnabledCategories),
		filter.MaxChannels,
	)
	includeSourceInID := len(sources) > 1
	totalBeforeFilter := 0
	successfulSources := 0
	failedSources := 0
	for _, liveSource := range selectedSources {
		if request.FavoritesOnly && !liveSourceMayContainFavorites(liveSource.ID, request.FavoriteIDs, includeSourceInID) {
			continue
		}
		sourceFilter := filter
		if liveSource.HasFilterOverride {
			sourceFilter = liveSource.Filter
		}
		var sourceChannels []LiveChannel
		var availabilityChannels []LiveChannel
		var sourceErr error
		sourceErrorMessage := "failed to fetch channels"
		if liveSource.Mode == "xtream" {
			sourceChannels, sourceErr = h.fetchXtreamChannels(r.Context(), liveSource.XtreamHost, liveSource.XtreamUsername, liveSource.XtreamPassword, liveSource.ProxyURL)
		} else if liveSource.Mode == "stremio" {
			sourceChannels, sourceErr = h.fetchStremioChannels(r.Context(), liveSource.ManifestURL, liveSource.ProxyURL)
		} else if liveSource.Mode == "stalker" {
			if paginated && request.Filter == "" && !request.FavoritesOnly && len(selectedSources) == 1 {
				sourceChannels, stalkerCategoryTotal, stalkerAvailableCategories, sourceErr = fetchStalkerCategoryChannels(
					r.Context(), stalkerConfigFromResolvedSource(liveSource), request.Categories, offset+limit,
				)
			} else {
				sourceChannels, sourceErr = fetchStalkerChannels(r.Context(), stalkerConfigFromResolvedSource(liveSource))
			}
			if stalkerCategoryTotal >= 0 {
				availabilityChannels = make([]LiveChannel, 0, len(stalkerAvailableCategories))
				for _, category := range stalkerAvailableCategories {
					availabilityChannels = append(availabilityChannels, LiveChannel{Group: category})
				}
			}
		} else {
			var contents string
			contents, sourceErr = h.fetchPlaylistContents(r.Context(), liveSource.PlaylistURL, liveSource.ProxyURL)
			if sourceErr == nil {
				sourceChannels = parseM3UPlaylist(contents)
			}
			sourceErrorMessage = "failed to fetch playlist"
		}
		if sourceErr != nil {
			log.Printf("[live] GetChannels %s error for source %q: %v", liveSource.Mode, liveSource.ID, sourceErr)
			failedSources++
			if len(selectedSources) == 1 {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, sourceErrorMessage), http.StatusBadGateway)
				return
			}
			continue
		}
		successfulSources++
		if availabilityChannels == nil {
			availabilityChannels = sourceChannels
		}
		totalBeforeFilter += len(sourceChannels)
		enabledAvailabilityChannels := filterChannelsByEnabledCategories(availabilityChannels, sourceFilter.EnabledCategories)
		categorySourceChannels = append(categorySourceChannels, enabledAvailabilityChannels...)
		enabledChannels := filterChannelsByEnabledCategories(sourceChannels, sourceFilter.EnabledCategories)
		selectedChannels := filterChannelsByRequestedCategories(
			enabledChannels, request.Categories, request.FavoriteIDs, liveSource.ID, includeSourceInID,
		)
		if sourceFilter.MaxChannels > 0 && len(selectedChannels) > sourceFilter.MaxChannels {
			selectedChannels = selectedChannels[:sourceFilter.MaxChannels]
			if stalkerCategoryTotal > sourceFilter.MaxChannels {
				stalkerCategoryTotal = sourceFilter.MaxChannels
			}
		}
		allChannels = append(allChannels, tagChannelsWithSource(selectedChannels, liveSource, includeSourceInID)...)
	}
	if successfulSources == 0 && failedSources > 0 {
		http.Error(w, `{"error":"failed to fetch channels"}`, http.StatusBadGateway)
		return
	}
	if failedSources > 0 {
		log.Printf("[live] GetChannels returned available sources after %d of %d sources failed", failedSources, len(selectedSources))
	}

	filteredChannels := allChannels

	// Extract available categories from filtered channels (only categories with actual channels)
	categoryInfos := extractCategories(categorySourceChannels)
	availableCategories := make([]string, len(categoryInfos))
	for i, cat := range categoryInfos {
		availableCategories[i] = cat.Name
	}

	requestedCategories := make(map[string]struct{})
	for _, category := range request.Categories {
		if category = strings.TrimSpace(category); category != "" {
			requestedCategories[category] = struct{}{}
		}
	}
	requestedFavoriteIDs := make(map[string]struct{})
	for _, channelID := range request.FavoriteIDs {
		if channelID = strings.TrimSpace(channelID); channelID != "" {
			requestedFavoriteIDs[channelID] = struct{}{}
		}
	}
	favoritesOnly := request.FavoritesOnly
	hasValidCategorySelection := false
	for _, category := range availableCategories {
		if _, selected := requestedCategories[category]; selected {
			hasValidCategorySelection = true
			break
		}
	}
	if favoritesOnly || hasValidCategorySelection {
		selected := make([]LiveChannel, 0, len(filteredChannels))
		for _, channel := range filteredChannels {
			_, favorite := requestedFavoriteIDs[channel.ID]
			_, categorySelected := requestedCategories[channel.Group]
			if (favoritesOnly && favorite) || (!favoritesOnly && (favorite || categorySelected)) {
				selected = append(selected, channel)
			}
		}
		filteredChannels = selected
	}
	filteredChannels = filterLiveChannelsByText(
		filteredChannels,
		request.Filter,
		requestedFavoriteIDs,
		h.epgService,
		time.Duration(src.EPGTimeOffsetMinutes)*time.Minute,
	)
	filteredChannels = orderFavoriteChannelsFirst(filteredChannels, requestedFavoriteIDs)

	total := len(filteredChannels)
	responseTotal := total
	if stalkerCategoryTotal >= 0 {
		responseTotal = stalkerCategoryTotal
	}
	pageChannels := filteredChannels
	responseOffset := 0
	responseLimit := total
	if paginated {
		responseOffset = offset
		responseLimit = limit
		start := min(offset, total)
		end := min(start+limit, total)
		pageChannels = filteredChannels[start:end]
	}

	// Note: StreamURL will be set by frontend based on channel.url
	// The frontend calls buildLiveStreamUrl to create the proxied URL

	response := LiveChannelsResponse{
		Channels:            pageChannels,
		TotalBeforeFilter:   totalBeforeFilter,
		Total:               responseTotal,
		Offset:              responseOffset,
		Limit:               responseLimit,
		HasMore:             responseOffset+len(pageChannels) < responseTotal,
		AvailableCategories: availableCategories,
		Sources:             liveSourceOptions(resolvedLiveSources(src)),
	}
	log.Printf(
		"[live] GetChannels response profileId=%q sourceId=%q returned=%d total=%d totalBeforeFilter=%d availableCategories=%d hasMore=%t duration=%s",
		profileID,
		requestedSourceID,
		len(pageChannels),
		total,
		totalBeforeFilter,
		len(availableCategories),
		response.HasMore,
		time.Since(requestStartedAt).Round(time.Millisecond),
	)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[live] GetChannels JSON encode error: %v", err)
	}
}

// GetCategories returns all available categories from the configured playlist.
func (h *LiveHandler) GetCategories(w http.ResponseWriter, r *http.Request) {
	categoryCounts := make(map[string]int)

	settings, err := h.cfgManager.Load()
	if err != nil {
		log.Printf("[live] GetCategories error loading settings: %v", err)
		http.Error(w, `{"error":"failed to load settings"}`, http.StatusInternalServerError)
		return
	}

	src := h.resolveProfileLiveSource(r, settings)

	sources := resolvedLiveSources(src)
	if len(sources) == 0 {
		log.Printf("[live] GetCategories error: no playlist URL configured")
		http.Error(w, `{"error":"failed to fetch playlist"}`, http.StatusBadGateway)
		return
	}
	selectedSources := selectM3USources(sources, r.URL.Query().Get("sourceId"))
	if _, hasIndex := r.URL.Query()["sourceIndex"]; hasIndex {
		selectedSources = selectLiveSourceByConfigIndex(sources, r.URL.Query().Get("sourceIndex"))
	}
	if len(selectedSources) == 0 {
		http.Error(w, `{"error":"unknown source"}`, http.StatusBadRequest)
		return
	}
	for _, liveSource := range selectedSources {
		if liveSource.Mode == "xtream" {
			channels, err := h.fetchXtreamChannels(r.Context(), liveSource.XtreamHost, liveSource.XtreamUsername, liveSource.XtreamPassword, liveSource.ProxyURL)
			if err != nil {
				log.Printf("[live] GetCategories Xtream error for source %q: %v", liveSource.ID, err)
				http.Error(w, `{"error":"failed to fetch categories"}`, http.StatusBadGateway)
				return
			}
			for _, category := range extractCategories(channels) {
				categoryCounts[category.Name] += category.ChannelCount
			}
			continue
		}
		if liveSource.Mode == "stremio" {
			channels, err := h.fetchStremioChannels(r.Context(), liveSource.ManifestURL, liveSource.ProxyURL)
			if err != nil {
				log.Printf("[live] GetCategories Stremio error for source %q: %v", liveSource.ID, err)
				http.Error(w, `{"error":"failed to fetch categories"}`, http.StatusBadGateway)
				return
			}
			for _, category := range extractCategories(channels) {
				categoryCounts[category.Name] += category.ChannelCount
			}
			continue
		}
		if liveSource.Mode == "stalker" {
			channels, err := fetchStalkerChannels(r.Context(), stalkerConfigFromResolvedSource(liveSource))
			if err != nil {
				log.Printf("[live] GetCategories Stalker error for source %q: %v", liveSource.ID, err)
				http.Error(w, `{"error":"failed to fetch categories"}`, http.StatusBadGateway)
				return
			}
			for _, category := range extractCategories(channels) {
				categoryCounts[category.Name] += category.ChannelCount
			}
			continue
		}
		categories, err := h.fetchM3UCategories(r.Context(), liveSource.PlaylistURL, liveSource.ProxyURL)
		if err != nil {
			log.Printf("[live] GetCategories error for source %q: %v", liveSource.ID, err)
			http.Error(w, `{"error":"failed to fetch playlist"}`, http.StatusBadGateway)
			return
		}
		for _, category := range categories {
			categoryCounts[category.Name] += category.ChannelCount
		}
	}

	response := CategoriesResponse{
		Categories: categoryInfosFromCounts(categoryCounts),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[live] GetCategories JSON encode error: %v", err)
	}
}

// Favorites use source-prefixed IDs in multi-source catalogs. Avoid contacting
// unrelated providers just to discard their entire catalogs afterward.
func liveSourceMayContainFavorites(sourceID string, favoriteIDs []string, includeSourceInID bool) bool {
	for _, id := range favoriteIDs {
		id = strings.TrimSpace(id)
		if id != "" && (!includeSourceInID || sourceID == "" || strings.HasPrefix(id, sourceID+":")) {
			return true
		}
	}
	return false
}

package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"novastream/services/castcaps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/integration"
	"novastream/internal/liveusage"
	"novastream/internal/netproxy"
	"novastream/internal/requestsecurity"
	"novastream/internal/ytdlp"
	"novastream/models"
	"novastream/services/credits"
	"novastream/services/debrid"
	"novastream/services/libraryaccess"
	"novastream/services/playback"
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

var googleVideoURLPattern = regexp.MustCompile(`https?://[^\s\]\)]+googlevideo\.com/[^\s\]\)]+`)

var debridFilePathPattern = regexp.MustCompile(`(?i)^/?debrid/[^/]+/[^/]+/file/[^/]+/(.+)$`)
var bluRayStreamPathPattern = regexp.MustCompile(`(?i)^(.*BDMV/)STREAM/([0-9]{5})\.m2ts$`)

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

var debugVideoTraceLogs = envFlag("STRMR_VIDEO_TRACE_LOGS")

func envFlag(name string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func resolveScopedPlaybackPath(r *http.Request, path string) string {
	path = strings.TrimSpace(path)
	if path != models.SessionScopeResourcePlaceholder {
		return path
	}
	session, ok := auth.GetSession(r)
	if !ok || session.Scope != models.SessionScopeStream {
		return path
	}
	return strings.TrimSpace(session.ScopeResource)
}

func videoTracef(format string, args ...any) {
	if debugVideoTraceLogs {
		log.Printf(format, args...)
	}
}

// VideoHandler handles video streaming requests using the local stream provider.
type VideoHandler struct {
	transmux      bool
	ffmpegPath    string
	ffprobePath   string
	streamer      streaming.Provider
	hlsManager    *HLSManager
	failures      *streamFailureRegistry
	prequeueStore *playback.PrequeueStore
	prewarmSvc    PrewarmService
	libraryAccess *libraryaccess.Service

	// castCaps answers "what can this receiver actually decode" from cache.
	// Never probes on this path: a cast start must not wait on a device.
	castCaps *castcaps.Store

	// External playback URLs commonly redirect through an addon before reaching
	// the debrid CDN. Keep one hardened client alive for connection reuse and
	// remember short-lived final redirects so FFmpeg range requests do not pay
	// the addon/redirect latency repeatedly.
	externalProxyHTTPClient *http.Client
	externalRedirectMu      sync.Mutex
	externalRedirects       map[string]cachedExternalRedirect
	externalProxyRequestSeq uint64
	externalPrefixSpool     externalPrefixSpool

	// Subtitle extraction for non-HLS streams
	subtitleExtractManager *SubtitleExtractManager

	// Local WebDAV access for ffprobe seeking (usenet paths)
	webdavMu      sync.RWMutex
	webdavBaseURL string
	webdavPrefix  string
	localBaseURL  string

	// User settings for policy checks (e.g., HDR/DV policy)
	userSettingsSvc   UserSettingsProvider
	clientSettingsSvc ClientSettingsProvider
	configManager     ConfigProvider

	// Metadata response cache for /video/metadata endpoint
	// Prevents repeated ffprobe calls during playback
	metadataCacheMu sync.RWMutex
	metadataCache   map[string]*cachedMetadataEntry

	// In-flight probe deduplication: prevents parallel ffprobe calls for the same path
	// Key: path, Value: channel that closes when probe completes
	probeInFlight sync.Map

	// Read-ahead range cache — prevents seek storms from hammering the debrid CDN.
	// When ExoPlayer seeks rapidly through DV files with interleaved tracks, it generates
	// hundreds of tiny (2-byte) range requests. The cache fetches a larger chunk on the first
	// miss and serves subsequent nearby requests from memory.
	rangeCache rangeBlockCache

	// Concurrent stream pool — maintains persistent CDN connections that survive
	// client disconnects. Prevents seek storms when players alternate between audio
	// and video track positions in non-interleaved MP4 files.
	streamPool *streamPool

	// Credits detection
	creditsDetector *credits.Detector

	// Preview thumbnail generation/cache for seek scrubbing
	thumbnailManager *ThumbnailManager

	// User/account services for stream limit enforcement
	usersSvc    UsersProvider
	accountsSvc AccountsProvider

	// Hardware encode capabilities for the legacy DLNA MPEG-TS output. The HLS
	// manager owns the cached detection, so these are only used when transmux is
	// disabled globally (no manager exists) and a DLNA renderer forces the path.
	dlnaCapsOnce sync.Once
	dlnaCaps     HWAccelCaps
}

const (
	externalRedirectCacheTTL    = 15 * time.Minute
	externalPrefixSpoolTTL      = 60 * time.Second
	externalPrefixSpoolMaxBytes = 4 * 1024 * 1024
)

type cachedExternalRedirect struct {
	url    string
	expiry time.Time
}

// externalPrefixSpool mirrors the beginning of an external stream while it is
// sent to FFmpeg. Startup demuxing often reopens overlapping ranges near the
// beginning of a Matroska file; those reads can be answered from this bounded
// snapshot instead of paying for another CDN connection.
type externalPrefixSpoolEntry struct {
	data               []byte
	totalSize          int64
	contentType        string
	etag               string
	lastModified       string
	contentDisposition string
	expiry             time.Time
}

type externalPrefixSpool struct {
	mu      sync.Mutex
	entries map[string]*externalPrefixSpoolEntry
}

type externalPrefixSpoolHit struct {
	data               []byte
	start              int64
	end                int64
	totalSize          int64
	contentType        string
	etag               string
	lastModified       string
	contentDisposition string
}

func (s *externalPrefixSpool) append(
	key string,
	offset int64,
	data []byte,
	totalSize int64,
	header http.Header,
) {
	if key == "" || len(data) == 0 || offset < 0 || offset >= externalPrefixSpoolMaxBytes {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]*externalPrefixSpoolEntry)
	}
	now := time.Now()
	entry := s.entries[key]
	if entry == nil || now.After(entry.expiry) {
		if offset != 0 {
			return
		}
		entry = &externalPrefixSpoolEntry{}
		s.entries[key] = entry
	}
	if offset != int64(len(entry.data)) {
		return
	}
	remaining := externalPrefixSpoolMaxBytes - offset
	if int64(len(data)) > remaining {
		data = data[:remaining]
	}
	entry.data = append(entry.data, data...)
	entry.totalSize = totalSize
	entry.contentType = header.Get("Content-Type")
	entry.etag = header.Get("ETag")
	entry.lastModified = header.Get("Last-Modified")
	entry.contentDisposition = header.Get("Content-Disposition")
	entry.expiry = now.Add(externalPrefixSpoolTTL)
}

func (s *externalPrefixSpool) get(key, rangeHeader string) (externalPrefixSpoolHit, bool) {
	start, requestedEnd, hasEnd, ok := parseSingleByteRange(rangeHeader)
	if !ok {
		return externalPrefixSpoolHit{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	if entry == nil || time.Now().After(entry.expiry) || start >= int64(len(entry.data)) {
		return externalPrefixSpoolHit{}, false
	}
	end := int64(len(entry.data)) - 1
	if hasEnd && requestedEnd < end {
		end = requestedEnd
	}
	if end < start {
		return externalPrefixSpoolHit{}, false
	}
	data := append([]byte(nil), entry.data[start:end+1]...)
	return externalPrefixSpoolHit{
		data:               data,
		start:              start,
		end:                end,
		totalSize:          entry.totalSize,
		contentType:        entry.contentType,
		etag:               entry.etag,
		lastModified:       entry.lastModified,
		contentDisposition: entry.contentDisposition,
	}, true
}

func parseSingleByteRange(header string) (start, end int64, hasEnd, ok bool) {
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false, false
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	if len(parts) != 2 || parts[0] == "" {
		return 0, 0, false, false
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}
	if parts[1] == "" {
		return start, 0, false, true
	}
	end, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return 0, 0, false, false
	}
	return start, end, true, true
}

func externalResponseTotalSize(contentRange, contentLength string) int64 {
	if slash := strings.LastIndex(contentRange, "/"); slash >= 0 {
		if total, err := strconv.ParseInt(contentRange[slash+1:], 10, 64); err == nil && total > 0 {
			return total
		}
	}
	if total, err := strconv.ParseInt(contentLength, 10, 64); err == nil && total > 0 {
		return total
	}
	return 0
}

// externalProxyMaxReconnects is how many times a single client response may
// reopen its upstream body after a transport failure (idle CDN kill, reset).
const externalProxyMaxReconnects = 8

// isRecoverableExternalProxyReadError reports transport failures that are safe
// to recover from by issuing a fresh ranged GET at the next absolute offset.
// Normal io.EOF (upstream finished cleanly) is not recoverable — it is success.
func isRecoverableExternalProxyReadError(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unexpected eof"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "use of closed network connection"),
		strings.Contains(msg, "server closed idle connection"),
		strings.Contains(msg, "http2: stream closed"),
		strings.Contains(msg, "stream error"):
		return true
	default:
		return false
	}
}

// externalProxyResumeRangeHeader builds the Range value for a mid-stream
// reconnect after `bytesDelivered` have already been written to the client.
// Returns "" when nothing remains to fetch for a finite client range.
func externalProxyResumeRangeHeader(clientStart, clientEnd int64, hasEnd bool, bytesDelivered int64) string {
	if bytesDelivered < 0 {
		bytesDelivered = 0
	}
	next := clientStart + bytesDelivered
	if hasEnd {
		if next > clientEnd {
			return ""
		}
		return fmt.Sprintf("bytes=%d-%d", next, clientEnd)
	}
	return fmt.Sprintf("bytes=%d-", next)
}

// externalProxySkipToOffset discards body bytes so the next Read starts at
// wantStart when the upstream served an earlier Content-Range start.
func externalProxySkipToOffset(body io.Reader, servedStart, wantStart int64) error {
	if wantStart <= servedStart {
		return nil
	}
	skip := wantStart - servedStart
	_, err := io.CopyN(io.Discard, body, skip)
	return err
}

// UsersProvider interface for resolving profile → account
type UsersProvider interface {
	Get(id string) (models.User, bool)
}

// AccountsProvider interface for looking up account stream limits
type AccountsProvider interface {
	Get(id string) (models.Account, bool)
}

// rangeBlockCache stores recently-fetched byte ranges to serve tiny range requests
// from memory instead of making round trips to the debrid CDN.
const (
	rangeCacheMinFetchSize = 2 * 1024 * 1024 // Fetch at least 2MB from CDN (4K DV needs larger prefetch)
	rangeCacheTTL          = 60 * time.Second
	rangeCacheMaxBlocks    = 16 // Max cached blocks per path
)

type rangeCacheBlock struct {
	offset int64
	data   []byte
	expiry time.Time
}

type rangeBlockCache struct {
	mu     sync.Mutex
	blocks map[string][]rangeCacheBlock // keyed by file path

	// Total instance length per path, learned from upstream Content-Range or a
	// HEAD Content-Length. DLNA renderers refuse to seek (and LG webOS aborts
	// playback outright) when a 206 reports "/*" instead of the real size, so
	// every partial response must be able to name the full file length.
	totalMu sync.RWMutex
	totals  map[string]int64

	// In-flight fetch coalescing: when multiple requests land in the same
	// fetch window, only one CDN request is made; others wait on the channel.
	flightMu sync.Mutex
	inFlight map[string]chan struct{} // key: "path:fetchStart"
}

// setTotal records the full file length for a path. First writer wins; the
// value is immutable for a given file so later callers cannot regress it.
func (c *rangeBlockCache) setTotal(path string, total int64) {
	if total <= 0 {
		return
	}
	c.totalMu.Lock()
	defer c.totalMu.Unlock()
	if c.totals == nil {
		c.totals = make(map[string]int64)
	}
	if _, ok := c.totals[path]; !ok {
		c.totals[path] = total
	}
}

// total returns the known full file length, or 0 when it has not been learned.
func (c *rangeBlockCache) total(path string) int64 {
	c.totalMu.RLock()
	defer c.totalMu.RUnlock()
	return c.totals[path]
}

// contentRangeValue formats a Content-Range for a partial response, naming the
// real instance length when known and falling back to "*" only when it is not.
func (c *rangeBlockCache) contentRangeValue(path string, start, end int64) string {
	if total := c.total(path); total > 0 {
		return fmt.Sprintf("bytes %d-%d/%d", start, end, total)
	}
	return fmt.Sprintf("bytes %d-%d/*", start, end)
}

// parseContentRangeTotal extracts the instance length from an upstream
// "bytes START-END/TOTAL" header. Returns 0 when the total is absent or "*".
func parseContentRangeTotal(value string) int64 {
	slash := strings.LastIndexByte(value, '/')
	if slash < 0 {
		return 0
	}
	total, err := strconv.ParseInt(strings.TrimSpace(value[slash+1:]), 10, 64)
	if err != nil || total <= 0 {
		return 0
	}
	return total
}

// parseContentRangeStart lives in stream_pool.go (shared handlers package helper).

func (c *rangeBlockCache) get(path string, start, end int64) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocks == nil {
		return nil, false
	}
	now := time.Now()
	blocks := c.blocks[path]
	for _, b := range blocks {
		if now.After(b.expiry) {
			continue
		}
		blockEnd := b.offset + int64(len(b.data))
		if start >= b.offset && end <= blockEnd {
			s := start - b.offset
			e := end - b.offset
			return b.data[s:e], true
		}
	}
	return nil, false
}

// tryStartFetch attempts to claim ownership of a fetch for the given path+offset.
// Returns true if this caller should perform the fetch (and must call finishFetch when done).
// Returns false if another goroutine is already fetching this window — caller should wait
// on the returned channel, then re-check the cache.
func (c *rangeBlockCache) tryStartFetch(path string, fetchStart int64) (bool, chan struct{}) {
	key := fmt.Sprintf("%s:%d", path, fetchStart)
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	if c.inFlight == nil {
		c.inFlight = make(map[string]chan struct{})
	}
	if ch, ok := c.inFlight[key]; ok {
		return false, ch
	}
	ch := make(chan struct{})
	c.inFlight[key] = ch
	return true, ch
}

// finishFetch signals all waiters that the fetch for path+offset is complete.
func (c *rangeBlockCache) finishFetch(path string, fetchStart int64) {
	key := fmt.Sprintf("%s:%d", path, fetchStart)
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	if ch, ok := c.inFlight[key]; ok {
		close(ch)
		delete(c.inFlight, key)
	}
}

func (c *rangeBlockCache) put(path string, offset int64, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocks == nil {
		c.blocks = make(map[string][]rangeCacheBlock)
	}
	// Evict expired blocks
	now := time.Now()
	blocks := c.blocks[path]
	live := blocks[:0]
	for _, b := range blocks {
		if !now.After(b.expiry) {
			live = append(live, b)
		}
	}
	// Add new block (evict oldest if at capacity)
	if len(live) >= rangeCacheMaxBlocks {
		live = live[1:]
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	c.blocks[path] = append(live, rangeCacheBlock{
		offset: offset,
		data:   buf,
		expiry: now.Add(rangeCacheTTL),
	})
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

	h := &VideoHandler{
		transmux:               transmuxEnabled,
		ffmpegPath:             resolvedFFmpeg,
		ffprobePath:            resolvedFFprobe,
		streamer:               provider,
		hlsManager:             hlsMgr,
		failures:               defaultStreamFailureRegistry,
		subtitleExtractManager: subtitleMgr,
		metadataCache:          make(map[string]*cachedMetadataEntry),
		streamPool:             newStreamPool(defaultStreamFailureRegistry),
		externalRedirects:      make(map[string]cachedExternalRedirect),
	}
	h.externalProxyHTTPClient = requestsecurity.NewSafeHTTPClientWithPolicyProvider(
		0,
		10,
		func() requestsecurity.RestrictedHostPolicy {
			return h.configuredExternalHostPolicy()
		},
	)
	if resolvedFFmpeg != "" {
		h.thumbnailManager = NewThumbnailManager(filepath.Join(os.TempDir(), "strmr-thumbnails"), resolvedFFmpeg)
	}

	// Start background cleanup for metadata cache
	go h.runMetadataCacheCleanup()

	return h
}

func (h *VideoHandler) cachedExternalRedirectURL(origin string) (string, bool) {
	h.externalRedirectMu.Lock()
	defer h.externalRedirectMu.Unlock()
	entry, ok := h.externalRedirects[origin]
	if !ok {
		return origin, false
	}
	if time.Now().After(entry.expiry) {
		delete(h.externalRedirects, origin)
		return origin, false
	}
	return entry.url, true
}

func (h *VideoHandler) rememberExternalRedirect(origin, final string) {
	if origin == "" || final == "" || origin == final {
		return
	}
	h.externalRedirectMu.Lock()
	h.externalRedirects[origin] = cachedExternalRedirect{
		url:    final,
		expiry: time.Now().Add(externalRedirectCacheTTL),
	}
	h.externalRedirectMu.Unlock()
}

func (h *VideoHandler) forgetExternalRedirect(origin string) {
	h.externalRedirectMu.Lock()
	delete(h.externalRedirects, origin)
	h.externalRedirectMu.Unlock()
}

// SetUserSettingsService sets the user settings service for policy checks
func (h *VideoHandler) SetUserSettingsService(svc UserSettingsProvider) {
	h.userSettingsSvc = svc
}

// SetConfigManager sets the config manager for global settings fallback
func (h *VideoHandler) SetConfigManager(cfgManager ConfigProvider) {
	h.configManager = cfgManager
	if h.hlsManager != nil {
		h.hlsManager.SetConfigManager(cfgManager)
	}
}

// SetClientSettingsService sets the client settings service for per-device policy checks
func (h *VideoHandler) SetClientSettingsService(svc ClientSettingsProvider) {
	h.clientSettingsSvc = svc
}

// SetUsersService sets the users service for resolving profile → account.
func (h *VideoHandler) SetUsersService(svc UsersProvider) {
	h.usersSvc = svc
}

// SetAccountsService sets the accounts service for looking up stream limits.
func (h *VideoHandler) SetAccountsService(svc AccountsProvider) {
	h.accountsSvc = svc
}

func (h *VideoHandler) SetLibraryAccessService(svc *libraryaccess.Service) {
	h.libraryAccess = svc
}

func (h *VideoHandler) requireLibraryStreamAccess(w http.ResponseWriter, r *http.Request, streamPath string) bool {
	if h.libraryAccess == nil {
		return true
	}
	accountID := auth.GetAccountID(r)
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID == "" {
		profileID = strings.TrimSpace(r.URL.Query().Get("userId"))
	}
	if profileID != "" {
		if h.usersSvc == nil {
			http.Error(w, "stream not found", http.StatusNotFound)
			return false
		}
		profile, ok := h.usersSvc.Get(profileID)
		if !ok || profile.AccountID != accountID {
			http.Error(w, "stream not found", http.StatusNotFound)
			return false
		}
	}
	recognized, allowed, err := h.libraryAccess.CanAccessStream(r.Context(), streamPath, accountID, profileID, auth.IsMaster(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if recognized && !allowed {
		http.Error(w, "stream not found", http.StatusNotFound)
		return false
	}
	return true
}

// SetPrequeueStore lets playback failures invalidate ready prequeue entries.
func (h *VideoHandler) SetPrequeueStore(store *playback.PrequeueStore) {
	h.prequeueStore = store
}

// SetPrewarmService lets playback failures put warm entries into retry backoff.
func (h *VideoHandler) SetPrewarmService(svc PrewarmService) {
	h.prewarmSvc = svc
}

// LinkHLSSessionPrequeue tags a session created by the prequeue worker with
// its prequeue ID so click→first-frame latency can be measured end to end.
func (h *VideoHandler) LinkHLSSessionPrequeue(sessionID, prequeueID string) {
	if h == nil || h.hlsManager == nil || prequeueID == "" {
		return
	}
	h.hlsManager.SetSessionPrequeue(sessionID, prequeueID, "", "")
}

// linkPreparedSessionToPrequeue correlates an ad-hoc HLS start (POST
// /video/hls/start) back to the ready prequeue entry whose stream it opened,
// carrying service metadata forward for latency reporting.
func (h *VideoHandler) linkPreparedSessionToPrequeue(sessionID, cleanPath string) {
	if h == nil || h.hlsManager == nil || h.prequeueStore == nil {
		return
	}
	entry, ok := h.prequeueStore.FindReadyByStreamPath(cleanPath)
	if !ok || entry == nil {
		return
	}
	serviceType := entry.ServiceType
	if serviceType == "" {
		p := strings.ToLower(strings.TrimSpace(entry.StreamPath))
		if strings.HasPrefix(p, "/debrid/") {
			serviceType = "debrid"
		} else if p != "" {
			serviceType = "usenet"
		}
	}
	provider := entry.DebridProvider
	if serviceType == "usenet" && provider == "" && entry.SelectedResult != nil {
		provider = entry.SelectedResult.Indexer
	}
	h.hlsManager.SetSessionPrequeue(sessionID, entry.ID, serviceType, provider)
}

func (h *VideoHandler) invalidatePrequeuesForFailedPath(streamPath string) {
	if h == nil || h.prequeueStore == nil {
		return
	}
	for _, removed := range h.prequeueStore.DeleteByStreamPath(streamPath) {
		log.Printf("[video] Removed failed prequeue %s for title=%s user=%s path=%q",
			removed.ID, removed.TitleID, removed.UserID, removed.StreamPath)
		if h.prewarmSvc != nil {
			h.prewarmSvc.InvalidatePrequeue(removed.ID)
		}
	}
}

// SetCreditsDetector sets the credits detection service.
func (h *VideoHandler) SetCreditsDetector(d *credits.Detector) {
	h.creditsDetector = d
}

// SetThumbnailCacheDir moves the preview thumbnail cache under the configured app cache directory.
func (h *VideoHandler) SetThumbnailCacheDir(baseDir string) {
	if strings.TrimSpace(baseDir) == "" || h.ffmpegPath == "" {
		return
	}
	h.thumbnailManager = NewThumbnailManager(filepath.Join(baseDir, "thumbnails"), h.ffmpegPath)
}

// DetectCredits triggers async credits detection for a video path.
func (h *VideoHandler) DetectCredits(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	if h.creditsDetector == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
		return
	}

	streamPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if streamPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	if !h.requireAllowedExternalPath(w, r, streamPath) {
		return
	}
	if !h.requireLibraryStreamAccess(w, r, streamPath) {
		return
	}

	durationStr := strings.TrimSpace(r.URL.Query().Get("duration"))
	if durationStr == "" {
		http.Error(w, "missing duration parameter", http.StatusBadRequest)
		return
	}
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil || duration <= 0 {
		http.Error(w, "invalid duration parameter", http.StatusBadRequest)
		return
	}

	// Get direct URL for the video
	directProvider, ok := h.streamer.(streaming.DirectURLProvider)
	if !ok {
		http.Error(w, "direct URL not supported for this path", http.StatusNotImplemented)
		return
	}

	directURL, err := directProvider.GetDirectURL(r.Context(), streamPath)
	if err != nil {
		log.Printf("[credits] failed to get direct URL for path=%q: %v", streamPath, err)
		http.Error(w, "failed to resolve video URL", http.StatusInternalServerError)
		return
	}

	h.creditsDetector.DetectAsync(r.Context(), streamPath, directURL, duration)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "detecting"})
}

// GetCreditsStatus returns the cached credits detection result for a video path.
func (h *VideoHandler) GetCreditsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.HandleOptions(w, r)
		return
	}

	if h.creditsDetector == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
		return
	}

	streamPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if streamPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	result := h.creditsDetector.Get(streamPath)
	w.Header().Set("Content-Type", "application/json")

	if result == nil {
		if h.creditsDetector.IsInflight(streamPath) {
			json.NewEncoder(w).Encode(map[string]string{"status": "detecting"})
		} else {
			json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
		}
		return
	}

	resp := map[string]interface{}{
		"status":   "complete",
		"detected": result.Detected,
	}
	if result.Detected {
		resp["creditsStartSec"] = result.CreditsStartSec
	}
	json.NewEncoder(w).Encode(resp)
}

// resolveAccountID resolves a profile ID from the request into an account ID.
func (h *VideoHandler) resolveAccountID(r *http.Request) string {
	if h.usersSvc == nil {
		return ""
	}
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}
	if profileID == "" {
		return ""
	}
	if user, ok := h.usersSvc.Get(profileID); ok {
		return user.AccountID
	}
	return ""
}

// UpdateSharePlaybackProgress accepts live playback heartbeats from one-time
// share-link sessions. It updates only in-memory stream tracker state for the
// dashboard and never writes to watch history.
func (h *VideoHandler) UpdateSharePlaybackProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !auth.IsShareLinkRequest(r) {
		http.Error(w, "share-link session required", http.StatusForbidden)
		return
	}

	var update models.PlaybackProgressUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid progress payload", http.StatusBadRequest)
		return
	}
	if update.MediaType == "" || update.ItemID == "" {
		http.Error(w, "mediaType and itemId are required", http.StatusBadRequest)
		return
	}
	if update.Duration < 0 || update.Position < 0 {
		http.Error(w, "position and duration must be non-negative", http.StatusBadRequest)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	profileName := r.URL.Query().Get("profileName")
	matched := GetStreamTracker().UpdateSharePlaybackProgress(
		profileID,
		profileName,
		update,
	)
	if h.hlsManager != nil {
		matched += h.hlsManager.UpdateSharePlaybackProgress(
			r.URL.Query().Get("sessionId"),
			profileID,
			profileName,
			update,
		)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"matched": matched,
	})
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
	if !h.requireLibraryStreamAccess(w, r, cleanPath) {
		return
	}

	// Enforce global concurrent stream limit (VOD only).
	if r.Method == http.MethodGet && h.configManager != nil {
		if globalSettings, err := h.configManager.Load(); err == nil && globalSettings.Playback.MaxConcurrentStreams > 0 {
			tracker := GetStreamTracker()
			usage, exceeds := tracker.WouldExceedGlobalLimit(r, cleanPath, globalSettings.Playback.MaxConcurrentStreams)
			if exceeds {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":           "STREAM_LIMIT_REACHED",
					"message":        fmt.Sprintf("Server stream limit reached (%d/%d)", usage.CurrentStreams, usage.MaxStreams),
					"currentStreams": usage.CurrentStreams,
					"maxStreams":     usage.MaxStreams,
					"scope":          "global",
				})
				return
			}
		}
	}

	// Enforce per-profile and per-account concurrent stream limits (VOD only).
	// Only check on GET (not HEAD) to avoid blocking metadata probes.
	if r.Method == http.MethodGet && h.usersSvc != nil && h.accountsSvc != nil {
		profileID := r.URL.Query().Get("profileId")
		if profileID == "" {
			profileID = r.URL.Query().Get("userId")
		}
		accountIDForLimit := auth.GetAccountID(r)
		if profileID != "" {
			user, ok := h.usersSvc.Get(profileID)
			if !ok || (!auth.IsMaster(r) && accountIDForLimit != "" && user.AccountID != accountIDForLimit) {
				http.Error(w, "profile not found", http.StatusNotFound)
				return
			}
			if ok {
				accountIDForLimit = user.AccountID
				// Check per-profile limit first
				if h.userSettingsSvc != nil {
					if settings, err := h.userSettingsSvc.Get(profileID); err == nil && settings != nil {
						if settings.Playback.MaxConcurrentStreams != nil && *settings.Playback.MaxConcurrentStreams > 0 {
							tracker := GetStreamTracker()
							usage, exceeds := tracker.WouldExceedProfileLimit(r, cleanPath, profileID, *settings.Playback.MaxConcurrentStreams)
							if exceeds {
								w.Header().Set("Content-Type", "application/json")
								w.WriteHeader(http.StatusTooManyRequests)
								_ = json.NewEncoder(w).Encode(map[string]interface{}{
									"code":           "STREAM_LIMIT_REACHED",
									"message":        fmt.Sprintf("Profile stream limit reached (%d/%d)", usage.CurrentStreams, usage.MaxStreams),
									"currentStreams": usage.CurrentStreams,
									"maxStreams":     usage.MaxStreams,
									"scope":          "profile",
								})
								return
							}
						}
					}
				}
			}
		}
		// Enforce the authenticated account limit even when optional profile
		// parameters are omitted.
		if accountIDForLimit != "" {
			if account, ok := h.accountsSvc.Get(accountIDForLimit); ok && account.MaxStreams > 0 {
				tracker := GetStreamTracker()
				usage, exceeds := tracker.WouldExceedAccountLimit(r, cleanPath, accountIDForLimit, account.MaxStreams)
				if exceeds {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"code":           "STREAM_LIMIT_REACHED",
						"message":        fmt.Sprintf("Account stream limit reached (%d/%d)", usage.CurrentStreams, usage.MaxStreams),
						"currentStreams": usage.CurrentStreams,
						"maxStreams":     usage.MaxStreams,
						"scope":          "account",
					})
					return
				}
			}
		}
	}

	// Determine whether transmuxing is desired and possible
	ext := detectContainerExt(cleanPath)
	target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	outputContainer := dlnaStreamProfileFromRequest(r).container()
	shouldTransmux, overrideTransmux, transmuxReason := h.shouldTransmux(r, cleanPath, ext)
	if transmuxReason != "" {
	}
	forceAAC := target == "web" || target == "browser"
	// Check global setting for forced AAC transcoding (for Bluetooth compatibility)
	if !forceAAC && h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			forceAAC = settings.Playback.ForceAACTranscoding
		}
	}
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
	rangeSummary := rangeHeader
	if rangeSummary == "" {
		rangeSummary = "full"
	}

	// Debug logging for troubleshooting
	videoTracef("[video] request path=%q clean=%q method=%s target=%q range=%s transmux=%t provider=%t", filePath, cleanPath, r.Method, target, rangeSummary, shouldTransmux, h.streamer != nil)

	// Additional detailed logging for range requests (seek operations)
	if rangeHeader != "" {
		videoTracef("[video] SEEK REQUEST detected: range=%q path=%q method=%s", rangeHeader, cleanPath, r.Method)
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

		handled, err := h.streamWithTransmuxProvider(w, r, cleanPath, forceAAC, overrideTransmux, outputContainer)
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

	isLocalMediaPath := strings.HasPrefix(cleanPath, "localmedia:")
	isDebridPath := isDebridStreamPath(cleanPath)

	// A playback request may legitimately remain open for hours. The request
	// context still cancels provider work as soon as the client disconnects.
	ctx := r.Context()

	rangeHeader := r.Header.Get("Range")
	isPlaybackProbe := strings.TrimSpace(r.URL.Query().Get("_probe")) != ""

	// Read-ahead range cache: serve tiny range requests from memory to prevent
	// seek storms from hammering the debrid CDN. ExoPlayer on Android TV generates
	// hundreds of 2-byte range requests when seeking through DV files with
	// interleaved tracks. Each CDN round trip takes ~1s, causing multi-second stalls.
	if rangeHeader != "" && r.Method == http.MethodGet && isDebridPath && !isLocalMediaPath {
		if rangeStart, rangeEnd, ok := parseByteRange(rangeHeader); ok {
			rangeLen := rangeEnd - rangeStart + 1
			if rangeLen > 0 && rangeLen <= rangeCacheMinFetchSize {
				// Check cache first
				if cached, hit := h.rangeCache.get(cleanPath, rangeStart, rangeEnd+1); hit {
					videoTracef("[video] range cache HIT: path=%q range=%q (%d bytes)", cleanPath, rangeHeader, rangeLen)
					h.writeCommonHeaders(w)
					normalizeMediaContentType(w, "", cleanPath)
					writeDlnaHeaders(w, r)
					w.Header().Set("Content-Length", strconv.FormatInt(int64(len(cached)), 10))
					w.Header().Set("Content-Range", h.rangeCache.contentRangeValue(cleanPath, rangeStart, rangeStart+int64(len(cached))-1))
					w.WriteHeader(http.StatusPartialContent)
					w.Write(cached)
					return true, nil
				}

				// Cache miss for a small request — expand to fetch a larger chunk from CDN,
				// then cache it so subsequent nearby requests are instant.
				// Align fetch window to rangeCacheMinFetchSize boundaries so that
				// concurrent requests for nearby offsets coalesce into one CDN fetch.
				fetchStart := (rangeStart / rangeCacheMinFetchSize) * rangeCacheMinFetchSize
				fetchEnd := fetchStart + rangeCacheMinFetchSize - 1
				expandedRange := fmt.Sprintf("bytes=%d-%d", fetchStart, fetchEnd)

				// Coalesce: if another goroutine is already fetching this window, wait for it
				// then serve from cache instead of making a duplicate CDN request.
				isOwner, flightCh := h.rangeCache.tryStartFetch(cleanPath, fetchStart)
				if !isOwner {
					videoTracef("[video] range cache COALESCE: waiting for in-flight fetch path=%q window=%d", cleanPath, fetchStart)
					select {
					case <-flightCh:
						// Fetch completed — check cache
					case <-r.Context().Done():
						// Client disconnected while waiting
						return true, r.Context().Err()
					}
					if cached, hit := h.rangeCache.get(cleanPath, rangeStart, rangeEnd+1); hit {
						videoTracef("[video] range cache HIT (after coalesce): path=%q range=%q (%d bytes)", cleanPath, rangeHeader, rangeLen)
						h.writeCommonHeaders(w)
						normalizeMediaContentType(w, "", cleanPath)
						writeDlnaHeaders(w, r)
						w.Header().Set("Content-Length", strconv.FormatInt(int64(len(cached)), 10))
						w.Header().Set("Content-Range", h.rangeCache.contentRangeValue(cleanPath, rangeStart, rangeStart+int64(len(cached))-1))
						w.WriteHeader(http.StatusPartialContent)
						w.Write(cached)
						return true, nil
					}
					// Coalesced fetch didn't cover our range — fall through to normal streaming
				} else {
					videoTracef("[video] range cache MISS: expanding %q to %q for path=%q", rangeHeader, expandedRange, cleanPath)

					fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 15*time.Second)
					fetchResp, fetchErr := h.streamer.Stream(fetchCtx, streaming.Request{
						Path:        cleanPath,
						RangeHeader: expandedRange,
						Method:      r.Method,
					})
					if fetchErr == nil {
						// The upstream 206 names the real file length; record it so
						// every spool response can report a true instance length.
						h.rangeCache.setTotal(cleanPath, parseContentRangeTotal(fetchResp.Headers.Get("Content-Range")))

						// Only cache a body the upstream actually served at the window
						// we asked for. Debrid CDNs sometimes ignore Range and answer
						// 200 from byte 0; filing those bytes under fetchStart poisons
						// the block and every later read of it returns data from the
						// wrong offset. The parallel fetcher already rejects non-206
						// for the same reason — this path has to agree.
						servedStart, haveStart := parseContentRangeStart(fetchResp.Headers.Get("Content-Range"))
						honoured := fetchResp.Status == http.StatusPartialContent && haveStart && servedStart == fetchStart

						fetchBuf := make([]byte, rangeCacheMinFetchSize+4096)
						n, _ := io.ReadFull(fetchResp.Body, fetchBuf)
						fetchResp.Close()
						fetchCancel()
						h.rangeCache.finishFetch(cleanPath, fetchStart)
						if !honoured {
							log.Printf("[video] range cache SKIP: upstream ignored window path=%q want=%d status=%d got=%q — falling back to direct stream",
								cleanPath, fetchStart, fetchResp.Status, fetchResp.Headers.Get("Content-Range"))
							n = 0
						}
						if n > 0 {
							fetchBuf = fetchBuf[:n]
							h.rangeCache.put(cleanPath, fetchStart, fetchBuf)
							videoTracef("[video] range cache FILL: path=%q offset=%d size=%d", cleanPath, fetchStart, n)

							// Serve the originally requested bytes from the fetched data
							reqOffset := rangeStart - fetchStart
							reqEnd := reqOffset + rangeLen
							if reqEnd <= int64(n) {
								h.writeCommonHeaders(w)
								normalizeMediaContentType(w, "", cleanPath)
								writeDlnaHeaders(w, r)
								w.Header().Set("Content-Length", strconv.FormatInt(rangeLen, 10))
								w.Header().Set("Content-Range", h.rangeCache.contentRangeValue(cleanPath, reqOffset+fetchStart, reqOffset+fetchStart+rangeLen-1))
								w.WriteHeader(http.StatusPartialContent)
								w.Write(fetchBuf[reqOffset:reqEnd])
								return true, nil
							}
						}
					} else {
						fetchCancel()
						h.rangeCache.finishFetch(cleanPath, fetchStart)
						log.Printf("[video] range cache fetch failed: path=%q err=%v", cleanPath, fetchErr)
					}
				}
				// Fall through to normal streaming if cache fill failed
			}
		}
	}

	// Concurrent stream pool: maintains persistent CDN connections to prevent
	// seek storms when players alternate between audio/video track positions
	// in non-interleaved MP4 files. The pool keeps CDN connections alive after
	// client disconnects, so the next request at a nearby position is served
	// from the buffer instead of making a new CDN round-trip.
	if rangeHeader != "" && r.Method == http.MethodGet && h.streamPool != nil && isDebridPath && !isLocalMediaPath && !isPlaybackProbe {
		if reqStart, ok := parseRangeStart(rangeHeader); ok {
			displayName := sanitizeExternalDisplayName(r.URL.Query().Get("displayName"))
			if displayName == "" {
				displayName = sanitizeExternalDisplayName(mux.Vars(r)["displayName"])
			}
			poolAccountID := h.resolveAccountID(r)
			if served, err := h.streamPool.serve(w, r, cleanPath, reqStart, h.streamer, h.writeCommonHeaders, displayName, poolAccountID); served {
				return true, err
			}
			videoTracef("[stream-pool] falling back to direct streaming: path=%q range=%q", cleanPath, rangeHeader)
		}
	}
	if isPlaybackProbe {
		videoTracef("[stream-pool] bypassing persistent pool for playback probe: path=%q range=%q", cleanPath, rangeHeader)
	}

	// Track this stream for admin monitoring
	tracker := GetStreamTracker()
	var streamID string
	var bytesCounter *int64
	var activityCounter *int64

	// Log the provider request details
	videoTracef(
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
		if record, confirmed := h.failures.recordRecognizedFailure(cleanPath, err); confirmed {
			log.Printf("[stream-migration] confirmed recoverable stream failure during open path=%q range=%q err=%v", cleanPath, rangeHeader, err)
			tracker.MarkPlaybackMigrationForPath(cleanPath, streamFailureMigrationReason(record))
			h.invalidatePrequeuesForFailedPath(cleanPath)
		}
		if errors.Is(err, streaming.ErrNotFound) {
			return false, nil
		}
		if errors.Is(err, streaming.ErrStaleTorrent) {
			log.Printf("[video] stale torrent detected for path=%q — returning 410 Gone", cleanPath)
			http.Error(w, "debrid torrent expired or deleted — please re-resolve", http.StatusGone)
			return true, err
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true, err
	}
	defer resp.Close()

	// Log detailed response information
	contentRange := resp.Headers.Get("Content-Range")
	contentLength := resp.Headers.Get("Content-Length")
	acceptRanges := resp.Headers.Get("Accept-Ranges")
	if total := parseContentRangeTotal(contentRange); total > 0 {
		h.rangeCache.setTotal(cleanPath, total)
	} else if resp.Status == http.StatusOK && resp.ContentLength > 0 {
		// A full-body 200 (including the HEAD probe) names the whole file.
		h.rangeCache.setTotal(cleanPath, resp.ContentLength)
	}
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
	videoTracef("[video] provider response: path=%q status=%d content-length=%s content-range=%q accept-ranges=%q range-request=%q expected-bytes=%d",
		cleanPath, resp.Status, contentLength, contentRange, acceptRanges, rangeHeader, expectedLength)

	h.writeCommonHeaders(w)
	for key, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	displayName := sanitizeExternalDisplayName(r.URL.Query().Get("displayName"))
	if displayName == "" {
		displayName = sanitizeExternalDisplayName(mux.Vars(r)["displayName"])
	}
	filename := displayName
	if filename == "" {
		filename = sanitizeExternalDisplayName(resp.Filename)
	}
	if filename == "" {
		filename = inferFilenameFromPath(cleanPath)
	}

	// Add filename headers for external players (Infuse uses this for friendly naming).
	if filename != "" {
		w.Header().Set("X-Filename", filename)
		if displayName != "" || w.Header().Get("Content-Disposition") == "" {
			w.Header().Set("Content-Disposition", buildInlineContentDisposition(filename))
		}
		videoTracef("[video] setting filename headers: %s", filename)
	}
	if expectedLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(expectedLength, 10))
	}
	// Advertise duration on the direct range proxy so native players (and
	// reverse proxies) can size the stream without waiting for moov.
	if w.Header().Get("Content-Duration") == "" {
		if dur := h.providerOrCachedDuration(ctx, cleanPath); dur > 0 {
			durStr := fmt.Sprintf("%.3f", dur)
			w.Header().Set("X-Content-Duration", durStr)
			w.Header().Set("Content-Duration", durStr)
		}
	}
	normalizeMediaContentType(w, filename, cleanPath)
	writeDlnaHeaders(w, r)

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}

	// Log the status being written back to client
	videoTracef("[video] writing response to client: path=%q status=%d range=%q", cleanPath, status, rangeHeader)
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return true, nil
	}

	if resp.Body != nil {
		// Start tracking this stream
		var rangeStart, rangeEnd int64
		// Parse range if present (simplified)
		accountID := h.resolveAccountID(r)
		streamID, bytesCounter, activityCounter = tracker.StartStreamWithAccount(r, cleanPath, expectedLength, rangeStart, rangeEnd, accountID)
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
		const throughputLogInterval = 5 * time.Second
		throughputLogAt := time.Now()
		var providerWindowBytes int64
		var providerWindowRead time.Duration
		var clientWindowBytes int64
		var clientWindowWrite time.Duration
		var upstreamWatch pipelineBlockWatch
		stopUpstreamStarvationWatch := monitorPipelineStarvation(
			ctx,
			&upstreamWatch,
			pipelineStarvationTimeout,
			pipelineStarvationCheckInterval,
			func(blockedFor time.Duration) bool {
				marked := tracker.MarkPlaybackMigration(streamID, "backend-starvation")
				if marked {
					log.Printf("[stream-migration] upstream starvation detected in provider stream: path=%q blockedFor=%v streamID=%s",
						cleanPath, blockedFor.Round(time.Millisecond), streamID)
				} else {
					log.Printf("[stream-health] upstream read blocked in provider stream without playback metadata: path=%q blockedFor=%v streamID=%s",
						cleanPath, blockedFor.Round(time.Millisecond), streamID)
				}
				return true
			},
		)
		defer stopUpstreamStarvationWatch()

		videoTracef("[video] starting stream copy: path=%q range=%q streamID=%s", cleanPath, rangeHeader, streamID)

		for {
			// Check if context is cancelled (client disconnected)
			select {
			case <-ctx.Done():
				log.Printf("[video] SEEK ABORT: provider stream cancelled path=%q total=%d range=%q reason=%v", cleanPath, total, rangeHeader, ctx.Err())
				return true, ctx.Err()
			default:
			}

			upstreamWatch.begin()
			readStart := time.Now()
			n, readErr := reader.Read(buf)
			upstreamWatch.end()
			providerWindowRead += time.Since(readStart)
			if n > 0 {
				providerWindowBytes += int64(n)
				if expectedLength > 0 {
					remaining := expectedLength - total
					if remaining <= 0 {
						if flusher != nil {
							flusher.Flush()
						}
						videoTracef("[video] provider stream complete path=%q total=%d range=%q (expected-bytes=%d)", cleanPath, total, rangeHeader, expectedLength)
						break
					}
					if int64(n) > remaining {
						n = int(remaining)
					}
				}

				writeStart := time.Now()
				written, writeErr := w.Write(buf[:n])
				clientWindowWrite += time.Since(writeStart)
				if writeErr != nil {
					if isClientGone(writeErr) || ctx.Err() == context.Canceled {
						log.Printf("[video] SEEK ABORT: client disconnected path=%q bytes=%d total=%d range=%q", cleanPath, n, total, rangeHeader)
						return true, nil
					}
					log.Printf("[video] SEEK ERROR: provider write error path=%q bytes=%d total=%d range=%q err=%v", cleanPath, n, total, rangeHeader, writeErr)
					return true, writeErr
				}

				total += int64(written)
				clientWindowBytes += int64(written)
				// Update stream tracking bytes and activity counters
				if bytesCounter != nil {
					atomic.StoreInt64(bytesCounter, total)
				}
				if activityCounter != nil {
					atomic.StoreInt64(activityCounter, time.Now().UnixNano())
				}
				flushCounter++

				// Periodic progress logging
				if total-lastLogBytes >= logInterval {
					videoTracef("[video] streaming progress: path=%q total=%d range=%q", cleanPath, total, rangeHeader)
					lastLogBytes = total
				}

				if now := time.Now(); now.Sub(throughputLogAt) >= throughputLogInterval {
					window := now.Sub(throughputLogAt)
					if providerWindowBytes > 0 {
						logStreamThroughput("provider-read", cleanPath, providerWindowBytes, providerWindowRead, window)
						tracker.ObserveUpstreamThroughput(streamID, providerWindowBytes, providerWindowRead)
					}
					if clientWindowBytes > 0 {
						logStreamThroughput("client-write", cleanPath, clientWindowBytes, clientWindowWrite, window)
					}
					throughputLogAt = now
					providerWindowBytes = 0
					providerWindowRead = 0
					clientWindowBytes = 0
					clientWindowWrite = 0
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
					videoTracef("[video] provider stream complete path=%q total=%d range=%q (expected-bytes=%d)", cleanPath, total, rangeHeader, expectedLength)
					break
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					log.Printf("[video] SEEK ERROR: provider read error path=%q total=%d range=%q err=%v", cleanPath, total, rangeHeader, readErr)
					if record, confirmed := h.failures.recordRecognizedFailure(cleanPath, readErr); confirmed {
						log.Printf("[stream-migration] confirmed missing-article stream failure during read path=%q range=%q total=%d err=%v", cleanPath, rangeHeader, total, readErr)
						tracker.MarkPlaybackMigrationForPath(cleanPath, streamFailureMigrationReason(record))
						h.invalidatePrequeuesForFailedPath(cleanPath)
					}
					return true, readErr
				}
				// Final flush on EOF
				if flusher != nil {
					flusher.Flush()
				}
				videoTracef("[video] provider stream complete path=%q total=%d range=%q", cleanPath, total, rangeHeader)
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

// parseByteRange parses a Range header like "bytes=100-200" into start and end offsets.
// Returns (start, end, true) on success. Both open-ended ranges (bytes=100-) and
// suffix ranges (bytes=-500) return false — only bounded ranges are supported.
func parseByteRange(rangeHeader string) (int64, int64, bool) {
	rangeHeader = strings.TrimSpace(rangeHeader)
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false
	}
	startStr := strings.TrimSpace(spec[:dash])
	endStr := strings.TrimSpace(spec[dash+1:])
	if startStr == "" || endStr == "" {
		return 0, 0, false // open-ended or suffix range
	}
	start, err1 := strconv.ParseInt(startStr, 10, 64)
	end, err2 := strconv.ParseInt(endStr, 10, 64)
	if err1 != nil || err2 != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
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

// transmuxContainer selects the muxer used for a transmuxed response. The zero
// value is the fragmented MP4 output every current caller expects.
type transmuxContainer int

const (
	// outputFmp4 is the fragmented MP4 stream consumed by the apps and browsers.
	outputFmp4 transmuxContainer = iota
	// outputMpegTs is a 188-byte MPEG-TS stream for legacy DLNA renderers that
	// accept neither Matroska, MP4 nor HLS. Declaring it as video/mpeg (rather
	// than video/vnd.dlna.mpeg-tts, which implies 192-byte timestamped packets)
	// is what pre-2012 renderers actually play.
	outputMpegTs
)

func (c transmuxContainer) contentType() string {
	if c == outputMpegTs {
		return "video/mpeg"
	}
	return "video/mp4"
}

// dlnaStreamProfile is the ?dlnaProfile= request value. It names a renderer
// family rather than a codec so the server owns the exact ffmpeg recipe.
type dlnaStreamProfile string

const (
	// dlnaProfileNone leaves streaming behaviour untouched.
	dlnaProfileNone dlnaStreamProfile = ""
	// dlnaProfileAVCTS is H.264 + AC3 in MPEG-TS (DLNA AVC_TS_HD_24_AC3_ISO).
	dlnaProfileAVCTS dlnaStreamProfile = "avc-ts"
)

// parseDLNAStreamProfile accepts the known profile names case-insensitively and
// maps anything else to dlnaProfileNone, so an unknown value never changes the
// behaviour a normal client sees.
func parseDLNAStreamProfile(raw string) dlnaStreamProfile {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(dlnaProfileAVCTS):
		return dlnaProfileAVCTS
	default:
		return dlnaProfileNone
	}
}

func dlnaStreamProfileFromRequest(r *http.Request) dlnaStreamProfile {
	if r == nil || r.URL == nil {
		return dlnaProfileNone
	}
	return parseDLNAStreamProfile(r.URL.Query().Get("dlnaProfile"))
}

func (p dlnaStreamProfile) container() transmuxContainer {
	if p == dlnaProfileAVCTS {
		return outputMpegTs
	}
	return outputFmp4
}

func (h *VideoHandler) shouldTransmux(r *http.Request, cleanPath, ext string) (bool, bool, string) {
	query := r.URL.Query()
	format := strings.ToLower(strings.TrimSpace(query.Get("format")))
	target := strings.ToLower(strings.TrimSpace(query.Get("target")))
	manualFlag := strings.ToLower(strings.TrimSpace(query.Get("transmux")))
	dvFlag := strings.ToLower(strings.TrimSpace(query.Get("dv"))) == "true"

	// A legacy DLNA renderer needs a full H.264/AC3 MPEG-TS transcode whatever
	// the source container is, so the profile overrides every other heuristic —
	// including Dolby Vision, which these SDR panels cannot display.
	if parseDLNAStreamProfile(query.Get("dlnaProfile")) == dlnaProfileAVCTS {
		log.Printf("[video] DLNA avc-ts transcode requested for path=%q", cleanPath)
		return true, true, "dlna avc-ts profile"
	}

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

func (h *VideoHandler) streamWithTransmuxProvider(w http.ResponseWriter, r *http.Request, cleanPath string, forceAAC bool, override bool, container transmuxContainer) (bool, error) {
	if !h.transmux && !override {
		return false, errors.New("transmux disabled")
	}

	if h.streamer == nil {
		return false, fmt.Errorf("stream provider not configured")
	}

	if h.ffmpegPath == "" {
		return false, errors.New("ffmpeg path is not configured")
	}

	// Keep the transmux alive for the full playback session. CommandContext and
	// the provider both stop when the downstream request is cancelled.
	ctx := r.Context()

	if r.Method == http.MethodHead {
		h.writeCommonHeaders(w)
		w.Header().Set("Content-Type", container.contentType())
		w.Header().Set("Accept-Ranges", "none")
		if container == outputMpegTs {
			// DLNA renderers abort when the server never confirms the transfer
			// mode they asked for. Accept-Ranges stays "none": the transcode is
			// progressive and cannot answer a byte-seek.
			writeDlnaMpegTsHeaders(w, r)
		}

		duration := 0.0
		if h.ffprobePath != "" {
			if meta, err := h.runFFProbeFromProvider(ctx, cleanPath); err == nil && meta != nil {
				duration = parseFloat(meta.Format.Duration)
			} else if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[video] provider ffprobe duration lookup failed for %q: %v", cleanPath, err)
			}
		}

		if container == outputMpegTs {
			// The renderer HEADs before it opens the stream, and the answer has
			// to describe the same cut the GET will serve.
			if startOffset := h.resolveMpegTsStartOffset(ctx, cleanPath, parseStartOffsetParam(r), duration); startOffset > 0 {
				w.Header().Set(dlnaStartOffsetHeader, fmt.Sprintf("%.3f", startOffset))
				duration = math.Max(duration-startOffset, 0)
			}
		}

		if duration > 0 {
			dur := fmt.Sprintf("%.3f", duration)
			w.Header().Set("X-Content-Duration", dur)
			w.Header().Set("Content-Duration", dur)
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

	var plan transmuxPlan
	if container == outputMpegTs {
		itemDuration := 0.0
		if meta != nil {
			itemDuration = parseFloat(meta.Format.Duration)
		}
		startOffset := h.resolveMpegTsStartOffset(ctx, cleanPath, parseStartOffsetParam(r), itemDuration)
		plan = h.buildMpegTsPlan(meta, "pipe:0", fallbackReason, startOffset)
		log.Printf("[video] DLNA avc-ts output path=%q start=%.3f remaining=%.3f ffmpeg=%s", cleanPath, plan.startOffset, plan.duration, strings.Join(plan.args, " "))
	} else {
		plan = h.buildTransmuxPlan(meta, "pipe:0", forceAAC, fallbackReason)
	}

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
		if plan.container != outputMpegTs {
			_, _ = io.Copy(io.Discard, stderr)
			return
		}
		// The DLNA arm is the only one that re-encodes video, so surface encoder
		// failures instead of silently serving a stream the renderer drops.
		buf := make([]byte, 4096)
		for {
			n, readErr := stderr.Read(buf)
			if n > 0 {
				if msg := strings.TrimSpace(string(buf[:n])); msg != "" {
					log.Printf("[video] DLNA avc-ts ffmpeg: %s", msg)
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	h.writeCommonHeaders(w)
	w.Header().Set("Content-Type", plan.container.contentType())
	w.Header().Set("Accept-Ranges", "none")
	if plan.container == outputMpegTs {
		writeDlnaMpegTsHeaders(w, r)
		if plan.startOffset > 0 {
			w.Header().Set(dlnaStartOffsetHeader, fmt.Sprintf("%.3f", plan.startOffset))
		}
	}
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	if plan.duration > 0 {
		durationHeader := fmt.Sprintf("%.3f", plan.duration)
		w.Header().Set("X-Content-Duration", durationHeader)
		w.Header().Set("Content-Duration", durationHeader)
	}
	w.WriteHeader(http.StatusOK)
	started := true

	// Track this transmuxed stream
	accountID := h.resolveAccountID(r)
	tracker := GetStreamTracker()
	streamID, bytesCounter, activityCounter := tracker.StartStreamWithAccount(r, cleanPath, 0, 0, 0, accountID)
	defer tracker.EndStream(streamID)

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
			atomic.StoreInt64(bytesCounter, totalWritten)
			atomic.StoreInt64(activityCounter, time.Now().UnixNano())
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
	if isRemoteMediaProviderPath(cleanPath) {
		return ""
	}

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

	return base + (&url.URL{Path: pathToUse}).EscapedPath()
}

// providerOrCachedDuration returns a known duration without blocking on ffprobe
// when possible: catalog DurationProvider first, then in-memory metadata cache.
func (h *VideoHandler) providerOrCachedDuration(ctx context.Context, cleanPath string) float64 {
	if h.streamer != nil {
		if dp, ok := h.streamer.(streaming.DurationProvider); ok {
			if d, err := dp.GetDuration(ctx, cleanPath); err == nil && d > 0 {
				return d
			}
		}
	}
	if cached := h.getCachedMetadata(cleanPath); cached != nil && cached.DurationSeconds > 0 {
		return cached.DurationSeconds
	}
	return 0
}

func (h *VideoHandler) runFFProbeFromProvider(ctx context.Context, cleanPath string) (*ffprobeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Check if this is already an external URL (e.g., from AIOStreams pre-resolved streams)
	// If so, probe it directly without going through the provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		videoTracef("[video] ffprobe using external URL directly: %s", cleanPath)
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
			videoTracef("[video] ffprobe using direct URL for seekable access: %s", cleanPath)
			meta, err := h.runFFProbe(ctx, directURL, nil)
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				// Log but don't fail - fall through to WebDAV or piped approach
				log.Printf("[video] ffprobe with direct URL failed, trying alternatives: %v", err)
			} else {
				h.enrichBluRayStreamLanguages(ctx, cleanPath, meta)
				return meta, nil
			}
		} else if err != nil && errors.Is(err, streaming.ErrStaleTorrent) {
			return nil, fmt.Errorf("%w", streaming.ErrStaleTorrent)
		} else if err != nil && (ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil, err
		} else if err != nil && !errors.Is(err, streaming.ErrNotFound) {
			log.Printf("[video] GetDirectURL failed for %q: %v", cleanPath, err)
		}
	}

	// Try WebDAV URL for usenet paths - allows ffprobe to seek
	if webdavURL := h.buildWebDAVURL(cleanPath); webdavURL != "" {
		videoTracef("[video] ffprobe using WebDAV URL for seekable access: %s", cleanPath)
		meta, err := h.runFFProbe(ctx, webdavURL, nil)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			// Log but don't fail - fall through to piped approach
			log.Printf("[video] ffprobe with WebDAV URL failed, falling back to piped probe: %v", err)
		} else {
			h.enrichBluRayStreamLanguages(ctx, cleanPath, meta)
			return meta, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Fall back to a ranged sample. Progressive MP4s often store moov after
	// mdat; piping only the first N bytes then reports "moov atom not found"
	// and can poison metadata with a truncated Format.Size. Detect that case
	// and probe ftyp+moov via ranged reads while playback stays a direct proxy.
	videoTracef("[video] ffprobe falling back to ranged sample probe for: %s", cleanPath)
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

	totalSize := providerResponseTotalSize(resp)
	if totalSize <= 0 {
		if cr := resp.Headers.Get("Content-Range"); cr != "" {
			totalSize = parseContentRangeTotal(cr)
		}
	}
	// Cap sample read so a misbehaving provider cannot fill memory.
	limit := providerProbeSampleBytes
	if limit <= 0 {
		limit = 16 * 1024 * 1024
	}
	sample, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
	_ = resp.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if len(sample) == 0 {
		return nil, fmt.Errorf("provider ffprobe stream returned empty body")
	}

	if moovOff, ok := mp4MoovAtEndOffset(sample, totalSize); ok {
		meta, err := h.runFFProbeProviderMoovAtEnd(ctx, cleanPath, sample, moovOff, totalSize)
		if err != nil {
			log.Printf("[video] moov-at-end probe failed for %q: %v; trying piped sample", cleanPath, err)
		} else {
			h.enrichBluRayStreamLanguages(ctx, cleanPath, meta)
			return meta, nil
		}
	} else if totalSize > int64(len(sample)) {
		// Sample may be too short to see the full mdat header; still try a
		// tail-based moov lookup when the object is larger than the sample.
		if meta, err := h.runFFProbeProviderMoovAtEnd(ctx, cleanPath, sample, 0, totalSize); err == nil && meta != nil {
			h.enrichBluRayStreamLanguages(ctx, cleanPath, meta)
			return meta, nil
		}
	}

	pr, pw := io.Pipe()
	go func() {
		_, writeErr := pw.Write(sample)
		if writeErr != nil {
			pw.CloseWithError(writeErr)
			return
		}
		pw.Close()
	}()

	meta, err := h.runFFProbe(ctx, "pipe:0", pr)
	if err != nil {
		// Final attempt: moov may sit at EOF even when the prefix parser missed it
		// (e.g. 64-bit mdat size not fully present in the sample).
		if totalSize > int64(len(sample)) {
			if moovMeta, moovErr := h.runFFProbeProviderMoovAtEnd(ctx, cleanPath, sample, 0, totalSize); moovErr == nil && moovMeta != nil {
				h.enrichBluRayStreamLanguages(ctx, cleanPath, moovMeta)
				return moovMeta, nil
			}
		}
		return nil, err
	}
	// Piped sample size is not the media size; restore real length when known.
	if totalSize > 0 && meta != nil {
		if sz := parseInt64(meta.Format.Size); sz <= 0 || sz == int64(len(sample)) || sz < totalSize/2 {
			meta.Format.Size = strconv.FormatInt(totalSize, 10)
		}
	}
	h.enrichBluRayStreamLanguages(ctx, cleanPath, meta)
	return meta, nil
}

const maxCLPIBytes = 1024 * 1024

func bluRayCLPIPath(sourcePath string) (string, bool) {
	matches := debridFilePathPattern.FindStringSubmatch(strings.TrimSpace(sourcePath))
	if len(matches) != 2 {
		return "", false
	}

	relativePath := filepath.ToSlash(matches[1])
	streamMatches := bluRayStreamPathPattern.FindStringSubmatch(relativePath)
	if len(streamMatches) != 3 {
		return "", false
	}

	return streamMatches[1] + "CLIPINF/" + streamMatches[2] + ".clpi", true
}

func parseCLPIStreamLanguages(data []byte) map[int]string {
	languages := make(map[int]string)
	for i := 0; i+6 < len(data); i++ {
		pid := int(data[i])<<8 | int(data[i+1])
		codingInfoLength := int(data[i+2])
		codingType := data[i+3]

		languageOffset := -1
		switch {
		case pid >= 0x1200 && pid <= 0x12ff && codingType == 0x90 && codingInfoLength >= 4:
			languageOffset = i + 4 // Presentation Graphics stream
		case pid >= 0x1100 && pid <= 0x11ff && codingType >= 0x80 && codingType <= 0x86 && codingInfoLength >= 5:
			languageOffset = i + 5 // Audio format/rate byte precedes language
		}
		if languageOffset < 0 || languageOffset+3 > len(data) {
			continue
		}

		languageBytes := data[languageOffset : languageOffset+3]
		valid := true
		for _, value := range languageBytes {
			if value < 'a' || value > 'z' {
				valid = false
				break
			}
		}
		if valid {
			languages[pid] = string(languageBytes)
		}
	}
	return languages
}

func parseFFProbeStreamPID(value string) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	pid, err := strconv.ParseInt(trimmed, 0, 32)
	if err != nil || pid < 0 || pid > 0xffff {
		return 0, false
	}
	return int(pid), true
}

func (h *VideoHandler) enrichBluRayStreamLanguages(ctx context.Context, sourcePath string, meta *ffprobeOutput) {
	if meta == nil || h.streamer == nil {
		return
	}

	needsLanguages := false
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		codecType := strings.ToLower(strings.TrimSpace(stream.CodecType))
		if (codecType == "subtitle" || codecType == "audio") && normalizeTag(stream.Tags, "language") == "" {
			if _, ok := parseFFProbeStreamPID(stream.ID); ok {
				needsLanguages = true
				break
			}
		}
	}
	if !needsLanguages {
		return
	}

	clpiPath, ok := bluRayCLPIPath(sourcePath)
	if !ok {
		return
	}
	relatedProvider, ok := h.streamer.(streaming.RelatedFileProvider)
	if !ok {
		return
	}

	resp, err := relatedProvider.StreamRelatedFile(ctx, sourcePath, clpiPath)
	if err != nil {
		if !errors.Is(err, streaming.ErrNotFound) && !errors.Is(err, context.Canceled) {
			videoTracef("[video] CLPI lookup failed for %q: %v", sourcePath, err)
		}
		return
	}
	if resp == nil || resp.Body == nil {
		if resp != nil {
			_ = resp.Close()
		}
		return
	}
	defer resp.Close()
	if resp.ContentLength > maxCLPIBytes {
		videoTracef("[video] ignoring oversized CLPI companion for %q: %d bytes", sourcePath, resp.ContentLength)
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCLPIBytes+1))
	if err != nil || len(data) > maxCLPIBytes {
		return
	}
	languages := parseCLPIStreamLanguages(data)
	if len(languages) == 0 {
		return
	}

	enriched := 0
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		if normalizeTag(stream.Tags, "language") != "" {
			continue
		}
		pid, ok := parseFFProbeStreamPID(stream.ID)
		if !ok {
			continue
		}
		language, ok := languages[pid]
		if !ok {
			continue
		}
		if stream.Tags == nil {
			stream.Tags = make(map[string]string)
		}
		stream.Tags["language"] = language
		enriched++
	}
	if enriched > 0 {
		log.Printf("[video] enriched %d Blu-ray stream language(s) from %s", enriched, clpiPath)
	}
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

	filePath := strings.TrimSpace(resolveScopedPlaybackPath(r, r.URL.Query().Get("path")))
	if filePath == "" {
		http.Error(w, "Missing path parameter", http.StatusBadRequest)
		return
	}
	if !h.requireAllowedExternalPath(w, r, filePath) {
		return
	}

	videoTracef("[video] ProbeVideo: received request for path=%q", filePath)

	// Clean the path: remove /webdav/ prefix but preserve the leading slash for NZB paths
	cleanPath := filePath
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}
	if !h.requireLibraryStreamAccess(w, r, cleanPath) {
		return
	}

	videoTracef("[video] ProbeVideo: after cleaning, path=%q", cleanPath)

	sanitizedPath := cleanPath
	if sanitizedPath == "" {
		sanitizedPath = filePath
	}

	// Check cache first to avoid repeated ffprobe calls during playback
	if cachedResp := h.getCachedMetadata(cleanPath); cachedResp != nil {
		videoTracef("[video] ProbeVideo: using cached metadata for path=%q", cleanPath)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cachedResp)
		return
	}

	// Deduplicate in-flight probes: if another request is already probing this path, wait for it
	doneChan := make(chan struct{})
	if existing, loaded := h.probeInFlight.LoadOrStore(cleanPath, doneChan); loaded {
		// Another probe is in progress - wait for it to complete
		videoTracef("[video] ProbeVideo: waiting for in-flight probe for path=%q", cleanPath)
		existingChan := existing.(chan struct{})
		select {
		case <-existingChan:
			// Probe completed, result should now be in cache
			if cachedResp := h.getCachedMetadata(cleanPath); cachedResp != nil {
				videoTracef("[video] ProbeVideo: using result from completed in-flight probe for path=%q", cleanPath)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(cachedResp)
				return
			}
			// If not in cache (probe failed?), fall through to probe ourselves
			videoTracef("[video] ProbeVideo: in-flight probe completed but no cache entry, will probe again for path=%q", cleanPath)
		case <-r.Context().Done():
			videoTracef("[video] ProbeVideo: request cancelled while waiting for in-flight probe for path=%q", cleanPath)
			return
		}
	}

	// We're the first request for this path - ensure cleanup when done
	defer func() {
		h.probeInFlight.Delete(cleanPath)
		close(doneChan)
	}()

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

	// Get preferred audio language for track selection
	preferredAudioLang := r.URL.Query().Get("audioLang")

	var (
		fileSize              int64
		notes                 []string
		remoteHeadUnavailable bool
	)

	// Check if this is an external URL (e.g., from AIOStreams)
	isExternalURL := strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://")

	if isExternalURL {
		videoTracef("[video] ProbeVideo: detected external URL, probing directly: %s", cleanPath)

		// For external URLs, try to get file size via HEAD request
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, cleanPath, nil)
		if err == nil {
			headReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
			h.applyExternalUsenetWebDAVAuth(headReq)
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
				videoTracef("[video] ProbeVideo: ffprobe external URL failed: %v", err)
				notes = append(notes, "ffprobe could not derive metadata")
			}
		}

		var response videoMetadataResponse
		if meta != nil {
			plan := determineAudioPlanWithLanguage(meta, false, preferredAudioLang)
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

		// Cache successful probe results to avoid repeated ffprobe calls
		if meta != nil {
			h.setCachedMetadata(cleanPath, &response)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("[video] probe encode error for %q: %v", cleanPath, err)
		}
		return
	}

	if h.streamer != nil {
		videoTracef("[video] ProbeVideo: attempting HEAD request for path=%q", cleanPath)
		resp, err := h.streamer.Stream(r.Context(), streaming.Request{
			Path:   cleanPath,
			Method: http.MethodHead,
		})
		if err != nil {
			if errors.Is(err, streaming.ErrNotFound) {
				// Plex and Jellyfin media endpoints can reject HEAD even when the
				// same item is available through GET. Treat HEAD as advisory for
				// those providers and let the ranged ffprobe request verify the item.
				if !isRemoteMediaProviderPath(cleanPath) {
					videoTracef("[video] ProbeVideo: stream not found for path=%q", cleanPath)
					http.Error(w, "stream not found", http.StatusNotFound)
					return
				}
				videoTracef("[video] ProbeVideo: remote library HEAD unavailable for path=%q; falling back to ranged probe", cleanPath)
				remoteHeadUnavailable = true
				notes = append(notes, "provider HEAD unavailable; metadata derived from ranged stream")
			} else if errors.Is(err, streaming.ErrStaleTorrent) {
				videoTracef("[video] ProbeVideo: stale torrent for path=%q", cleanPath)
				http.Error(w, "debrid torrent expired or deleted — please re-resolve", http.StatusGone)
				return
			} else {
				log.Printf("[video] metadata provider head failed for %q: %v", cleanPath, err)
				notes = append(notes, "stream metadata unavailable")
			}
		} else if resp != nil {
			defer resp.Close()
			fileSize = providerResponseTotalSize(resp)
			if fileSize <= 0 {
				fileSize = resp.ContentLength
			}
			if fileSize <= 0 && resp.Headers != nil {
				if raw := resp.Headers.Get("Content-Length"); raw != "" {
					if parsed, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
						fileSize = parsed
					}
				}
				if fileSize <= 0 {
					if cr := resp.Headers.Get("Content-Range"); cr != "" {
						fileSize = parseContentRangeTotal(cr)
					}
				}
			}
			if fileSize > 0 {
				h.rangeCache.setTotal(cleanPath, fileSize)
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
				if remoteHeadUnavailable && errors.Is(err, streaming.ErrNotFound) {
					videoTracef("[video] ProbeVideo: ranged remote library probe confirmed stream not found for path=%q", cleanPath)
					http.Error(w, "stream not found", http.StatusNotFound)
					return
				}
				log.Printf("[video] metadata provider ffprobe failed for %q: %v", cleanPath, err)
			}
		}
	}

	var response videoMetadataResponse
	if meta != nil {
		plan := determineAudioPlanWithLanguage(meta, false, preferredAudioLang)
		response = composeMetadataResponse(meta, sanitizedPath, plan)
		// HEAD/Content-Range reports the real object size. Probes that sample a
		// prefix (or a synthetic ftyp+moov blob) must not win with a tiny size —
		// native players need the full length to range-seek moov-at-end MP4s.
		if fileSize > 0 && (response.FileSizeBytes == 0 || response.FileSizeBytes < fileSize) {
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

	// Cache successful probe results to avoid repeated ffprobe calls
	if meta != nil {
		h.setCachedMetadata(cleanPath, &response)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[video] probe encode error for %q: %v", cleanPath, err)
	}
}

// buildTransmuxPlan produces the default fragmented MP4 plan: video is stream
// copied and only incompatible audio is re-encoded.
func (h *VideoHandler) buildTransmuxPlan(meta *ffprobeOutput, inputSpecifier string, forceAAC bool, fallbackReason string) transmuxPlan {
	plan := transmuxPlan{
		container: outputFmp4,
		videoMap:  "0:v:0",
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
	return determineAudioPlanWithLanguage(meta, forceAAC, "")
}

// determineAudioPlanWithLanguage selects an audio track, preferring tracks matching the
// specified language. When preferredLanguage is set, it uses the same priority logic as
// track_helper.go: compatible codec + language match > incompatible codec + language match > first compatible.
func determineAudioPlanWithLanguage(meta *ffprobeOutput, forceAAC bool, preferredLanguage string) audioPlan {
	if meta == nil {
		if forceAAC {
			return audioPlan{mode: audioPlanTranscode, reason: "no metadata; forcing AAC"}
		}
		return audioPlan{mode: audioPlanNone, reason: "no metadata"}
	}

	// Collect audio streams for language-aware selection
	var audioStreams []*ffprobeStream
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		if strings.EqualFold(stream.CodecType, "audio") {
			audioStreams = append(audioStreams, stream)
		}
	}

	if len(audioStreams) == 0 {
		if forceAAC {
			return audioPlan{mode: audioPlanTranscode, reason: "target requires AAC audio but no audio streams detected"}
		}
		return audioPlan{mode: audioPlanNone, reason: "no audio streams detected"}
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Helper to check language match
	matchesLang := func(stream *ffprobeStream) bool {
		if normalizedPref == "" {
			return false
		}
		lang := strings.ToLower(strings.TrimSpace(normalizeTag(stream.Tags, "language")))
		title := strings.ToLower(strings.TrimSpace(normalizeTag(stream.Tags, "title")))
		// Exact match
		if lang == normalizedPref || title == normalizedPref {
			return true
		}
		// Partial match
		if lang != "" && (strings.Contains(lang, normalizedPref) || strings.Contains(normalizedPref, lang)) {
			return true
		}
		return false
	}

	// Helper to check if codec is copyable
	isCopyable := func(stream *ffprobeStream) bool {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		_, ok := copyableAudioCodecs[codec]
		return ok
	}

	// Helper to check if this is a commentary track
	isCommentary := func(stream *ffprobeStream) bool {
		title := strings.ToLower(strings.TrimSpace(normalizeTag(stream.Tags, "title")))
		return strings.Contains(title, "commentary") ||
			strings.Contains(title, "isolated score") ||
			strings.Contains(title, "music only") ||
			strings.Contains(title, "score only")
	}

	// If we have a language preference, use language-aware selection
	if normalizedPref != "" {
		// Pass 1: Compatible codec matching language, skipping commentary
		for _, stream := range audioStreams {
			if matchesLang(stream) && isCopyable(stream) && !isCommentary(stream) {
				codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
				videoTracef("[metadata] selected compatible audio track %d (%s) for language %q", stream.Index, codec, preferredLanguage)
				return audioPlan{mode: audioPlanCopy, stream: stream, reason: fmt.Sprintf("compatible codec %s matching language %s", codec, preferredLanguage)}
			}
		}

		// Pass 2: Non-compatible codec matching language, skipping commentary (will need transcode)
		for _, stream := range audioStreams {
			if matchesLang(stream) && !isCommentary(stream) {
				codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
				videoTracef("[metadata] selected incompatible audio track %d (%s) for language %q - needs transcode", stream.Index, codec, preferredLanguage)
				return audioPlan{mode: audioPlanTranscode, stream: stream, reason: fmt.Sprintf("codec %s matching language %s requires transcoding", codec, preferredLanguage)}
			}
		}

		// Pass 3: Compatible codec matching language, including commentary
		for _, stream := range audioStreams {
			if matchesLang(stream) && isCopyable(stream) {
				codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
				videoTracef("[metadata] selected compatible audio track %d (%s, commentary) for language %q", stream.Index, codec, preferredLanguage)
				return audioPlan{mode: audioPlanCopy, stream: stream, reason: fmt.Sprintf("compatible codec %s matching language %s (commentary)", codec, preferredLanguage)}
			}
		}

		// Pass 4: Any codec matching language, including commentary
		for _, stream := range audioStreams {
			if matchesLang(stream) {
				codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
				videoTracef("[metadata] selected audio track %d (%s, commentary) for language %q - needs transcode", stream.Index, codec, preferredLanguage)
				return audioPlan{mode: audioPlanTranscode, stream: stream, reason: fmt.Sprintf("codec %s matching language %s requires transcoding", codec, preferredLanguage)}
			}
		}

		// No language match found, fall through to default selection
		videoTracef("[metadata] no audio track found for language %q, falling back to default selection", preferredLanguage)
	}

	// Default selection: first compatible codec (original behavior)
	for _, stream := range audioStreams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		if forceAAC {
			if codec == "aac" {
				return audioPlan{mode: audioPlanCopy, stream: stream, reason: "AAC audio already compatible"}
			}
			continue
		}
		if isCopyable(stream) {
			return audioPlan{mode: audioPlanCopy, stream: stream, reason: "copy-compatible audio codec"}
		}
	}

	// No copyable codec found, use first audio stream
	firstAudio := audioStreams[0]
	codec := strings.ToLower(strings.TrimSpace(firstAudio.CodecName))
	if forceAAC {
		return audioPlan{mode: audioPlanTranscode, stream: firstAudio, reason: "target requires AAC audio"}
	}
	return audioPlan{mode: audioPlanTranscode, stream: firstAudio, reason: fmt.Sprintf("audio codec %s requires transcoding", codec)}
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
			videoTracef("[video] Using dvh1 tag for Dolby Vision content (profile: %s)", dvProfile)
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
			videoTracef("[video] Using dvh1 tag for Dolby Vision content (legacy mode, profile: %s)", dvProfile)
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

const (
	// dlnaMaxWidth and dlnaMaxHeight bound the H.264 output for legacy DLNA
	// renderers. Their AVC level 4.0 decoders top out at the 1080p box, and a
	// height-only cap would hand an ultra-wide source a 2578-pixel-wide frame.
	dlnaMaxWidth  = 1920
	dlnaMaxHeight = 1080
	// dlnaMaxAudioChannels is the AC3 encoder's ceiling (5.1).
	dlnaMaxAudioChannels = 6
	// dlnaAC3Bitrate is comfortably inside AC3's 640 kbps limit at 5.1.
	dlnaAC3Bitrate = "448k"
)

// mpegTsEncodeInput is the probe-derived shape of one legacy DLNA transcode.
// Zero values are the "probe unavailable" case and stay safe: the scaler is
// applied (it never upscales) and the audio is downmixed.
type mpegTsEncodeInput struct {
	inputURL string
	videoMap string
	// audioMap is empty when the source has no audio stream to carry.
	audioMap       string
	sourceWidth    int
	sourceHeight   int
	audioChannels  int
	hdr            bool
	sourceTransfer string
	// startOffset is where in the item the mux begins, already keyframe-aligned
	// and validated. Zero starts at the beginning.
	startOffset float64
}

// buildMpegTsArgs assembles the complete ffmpeg invocation for the legacy DLNA
// output: H.264 + AC3 in a 188-byte transport stream. Nothing is stream copied —
// these renderers accept exactly one video and one audio codec — so the encoder
// selection, HDR tone mapping and downscale all come from the same hardware
// plan the HLS web path uses.
func buildMpegTsArgs(in mpegTsEncodeInput, caps HWAccelCaps) []string {
	maxWidth, maxHeight := dlnaMaxWidth, dlnaMaxHeight
	if in.sourceWidth > 0 && in.sourceHeight > 0 &&
		in.sourceWidth <= dlnaMaxWidth && in.sourceHeight <= dlnaMaxHeight {
		// Already inside the renderer's decode box, so drop the scaler entirely
		// rather than round-tripping a 720p source through it.
		maxWidth, maxHeight = 0, 0
	}

	encode := buildVideoEncodePlanWithLimits(caps, in.hdr, maxWidth, maxHeight, 0, in.sourceTransfer)
	if in.hdr && !encode.Tonemapped {
		warnMissingTonemapOnce()
	}

	args := make([]string, 0, 40)
	args = append(args, "-nostdin", "-loglevel", "error")
	// Hardware device initialization has to precede -i.
	args = append(args, encode.GlobalArgs...)
	if in.startOffset > 0 {
		// Input seeking, so the demuxer discards everything before the offset
		// instead of the encoder throwing away decoded frames. Accurate seek is
		// left on and costs nothing here: the offset is already a keyframe, and
		// the mux has to begin exactly where the response says it does because
		// the renderer's clock restarts at zero and reports no absolute time.
		args = append(args, "-ss", fmt.Sprintf("%.3f", in.startOffset))
	}
	args = append(args, "-i", in.inputURL)

	videoMap := strings.TrimSpace(in.videoMap)
	if videoMap == "" {
		videoMap = "0:v:0"
	}
	args = append(args, "-map", videoMap)
	if in.audioMap != "" {
		args = append(args, "-map", in.audioMap)
	}
	// MPEG-TS cannot carry the text subtitle codecs these sources ship, and a
	// legacy demuxer gives up on elementary streams it does not recognize, so
	// only video and audio are mapped.
	args = append(args, "-sn", "-dn")

	if encode.Filter != "" {
		args = append(args, "-vf", encode.Filter)
	}
	args = append(args, encode.EncoderArgs...)

	if in.audioMap == "" {
		args = append(args, "-an")
	} else {
		args = append(args, "-c:a", "ac3", "-b:a", dlnaAC3Bitrate)
		if in.audioChannels > dlnaMaxAudioChannels || in.audioChannels <= 0 {
			// The AC3 encoder stops at 5.1: a 7.1 source (or an unknown layout,
			// which may well be 7.1) aborts the whole stream without a downmix.
			args = append(args, "-ac", strconv.Itoa(dlnaMaxAudioChannels))
		}
	}

	return appendMpegTsOutputArgs(args)
}

// appendMpegTsOutputArgs closes out a 188-byte MPEG-TS pipe. This is the
// MPEG-TS sibling of appendStreamingOutputArgs; -movflags is MP4-only and
// FFmpeg warns or errors when it is passed to another muxer.
func appendMpegTsOutputArgs(args []string) []string {
	return append(args,
		// Renderers that parse the stream late still need to find a PAT/PMT.
		"-mpegts_flags", "+resend_headers",
		"-muxdelay", "0",
		"-muxpreload", "0",
		"-f", "mpegts",
		"pipe:1",
	)
}

var missingTonemapWarning sync.Once

// warnMissingTonemapOnce reports the absence of a tone-map filter once per
// process. A washed-out picture beats a refused stream, so the transcode goes
// ahead without tone mapping.
func warnMissingTonemapOnce() {
	missingTonemapWarning.Do(func() {
		log.Printf("[video] no tone-map filter available in this FFmpeg build; HDR sources stream to SDR renderers untone-mapped")
	})
}

// sourceNeedsToneMapping reports whether a video stream carries HDR signalling
// an SDR renderer cannot interpret, along with the source transfer function so
// HLG is tone mapped from the right curve. detectDolbyVision already covers DV
// RPUs plus PQ/HLG on HEVC; the raw transfer check catches HDR10 carried by
// other codecs.
func sourceNeedsToneMapping(stream *ffprobeStream) (bool, string) {
	if stream == nil {
		return false, ""
	}
	transfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	if hasDV, _, hdrFormat := detectDolbyVision(stream); hasDV || hdrFormat != "" {
		return true, transfer
	}
	return transfer == "smpte2084" || transfer == "arib-std-b67", transfer
}

// dlnaEncodeCaps returns the hardware encode capabilities for the DLNA MPEG-TS
// transcode. The HLS manager already caches detection and honours the configured
// preference, so reuse it; when transmux is disabled globally no manager exists,
// so detect once and remember it.
func (h *VideoHandler) dlnaEncodeCaps() HWAccelCaps {
	if h.hlsManager != nil {
		return h.hlsManager.hwAccelCaps()
	}
	h.dlnaCapsOnce.Do(func() {
		h.dlnaCaps = detectHWAccel(h.ffmpegPath, currentHWAccelPreference(h.configManager))
	})
	return h.dlnaCaps
}

// dlnaStartOffsetHeader names the second of the item the transport stream
// begins at. A renderer on this rung restarts its clock at zero and reports no
// absolute time, so this is the only thing that lets a caller turn that clock
// back into a position in the item.
const dlnaStartOffsetHeader = "X-Start-Offset"

// resolveMpegTsStartOffset turns a requested start offset into the one the mux
// will really begin at: clamped inside the item the way an HLS session clamps
// its own, then moved onto the keyframe FFmpeg's input seek lands on. The
// reported offset therefore describes the stream that is served, not the one
// that was asked for.
func (h *VideoHandler) resolveMpegTsStartOffset(ctx context.Context, cleanPath string, requested, duration float64) float64 {
	if requested <= 0 {
		return 0
	}
	if duration > 0 && requested >= duration {
		requested = math.Max(duration-4, 0)
		if requested == 0 {
			return 0
		}
	}
	// Keyframe probing needs a seekable URL for the source, which only the HLS
	// manager knows how to resolve; without it the requested second stands.
	if h.hlsManager == nil {
		return requested
	}
	aligned := h.hlsManager.probeKeyframePosition(ctx, cleanPath, requested)
	if aligned <= 0 || math.IsNaN(aligned) || math.IsInf(aligned, 0) || (duration > 0 && aligned >= duration) {
		return requested
	}
	return aligned
}

// buildMpegTsPlan produces the H.264/AC3 MPEG-TS transcode plan for legacy DLNA
// renderers. startOffset is where in the item the mux begins; the plan's
// duration is then the remainder, because a stream cut at an offset carries
// only what is left of the item.
func (h *VideoHandler) buildMpegTsPlan(meta *ffprobeOutput, inputSpecifier, fallbackReason string, startOffset float64) transmuxPlan {
	plan := transmuxPlan{
		container:   outputMpegTs,
		videoMap:    "0:v:0",
		startOffset: startOffset,
		audio: audioPlan{
			mode:   audioPlanTranscode,
			reason: "dlna avc-ts renderers require ac3 audio",
		},
	}
	in := mpegTsEncodeInput{
		inputURL: inputSpecifier,
		videoMap: plan.videoMap,
		// The "?" keeps ffmpeg from failing outright on a video-only source.
		audioMap:    "0:a:0?",
		startOffset: startOffset,
	}

	if meta == nil {
		if reason := strings.TrimSpace(fallbackReason); reason != "" {
			plan.audio.reason = reason
		}
		plan.args = buildMpegTsArgs(in, h.dlnaEncodeCaps())
		return plan
	}

	plan.usedProbe = true
	plan.duration = math.Max(parseFloat(meta.Format.Duration)-startOffset, 0)

	if stream := selectPrimaryVideoStream(meta); stream != nil {
		plan.videoMap = fmt.Sprintf("0:%d", stream.Index)
		plan.videoCodec = strings.ToLower(strings.TrimSpace(stream.CodecName))
		hasDV, dvProfile, _ := detectDolbyVision(stream)
		plan.hasDolbyVision = hasDV
		plan.dolbyVisionProfile = dvProfile
		in.videoMap = plan.videoMap
		in.sourceWidth = stream.Width
		in.sourceHeight = stream.Height
		in.hdr, in.sourceTransfer = sourceNeedsToneMapping(stream)
	}

	// Reuse the shared track selection so the renderer hears the same audio the
	// app would have picked, then re-encode whatever it lands on to AC3.
	if audio := determineAudioPlan(meta, false); audio.stream != nil {
		plan.audio.stream = audio.stream
		plan.audio.reason = fmt.Sprintf("transcoding audio codec %s to ac3 for dlna renderer", audio.codec())
		in.audioMap = fmt.Sprintf("0:%d", audio.stream.Index)
		in.audioChannels = audio.stream.Channels
	} else {
		plan.audio = audioPlan{mode: audioPlanNone, reason: "no audio streams detected"}
		in.audioMap = ""
	}

	plan.args = buildMpegTsArgs(in, h.dlnaEncodeCaps())
	return plan
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

func extractDolbyVisionConfiguration(stream *ffprobeStream) *models.DolbyVisionConfiguration {
	if stream == nil {
		return nil
	}
	for _, sd := range stream.SideDataList {
		sdType := strings.ToLower(strings.TrimSpace(sd.SideDataType))
		if !strings.Contains(sdType, "dovi") && !strings.Contains(sdType, "dolby") {
			continue
		}
		return &models.DolbyVisionConfiguration{
			StreamIndex:             stream.Index,
			PixelFormat:             strings.TrimSpace(stream.PixFmt),
			VersionMajor:            sd.DVVersionMajor,
			VersionMinor:            sd.DVVersionMinor,
			Profile:                 sd.DVProfile,
			Level:                   sd.DVLevel,
			RPUPresentFlag:          sd.RPUPresentFlag,
			ELPresentFlag:           sd.ELPresentFlag,
			BLPresentFlag:           sd.BLPresentFlag,
			BLSignalCompatibilityID: sd.DVBLSignalCompatibilityID,
		}
	}
	return nil
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
	if config := extractDolbyVisionConfiguration(stream); config != nil {
		profileStr := fmt.Sprintf("dvhe.%02d.%02d", config.Profile, config.Level)
		videoTracef("[video] Dolby Vision detected: profile=%d level=%d version=%d.%d rpu=%d el=%d bl=%d bl_compat_id=%d (%s)",
			config.Profile, config.Level, config.VersionMajor, config.VersionMinor,
			config.RPUPresentFlag, config.ELPresentFlag, config.BLPresentFlag, config.BLSignalCompatibilityID, profileStr)

		// Determine if this profile has HDR10 fallback
		// Profile 8 with bl_compat_id=1 or 2 has HDR10 base layer
		// Profile 5 is dual-layer without HDR10 fallback
		hasHDR10Fallback := config.Profile == 8 && (config.BLSignalCompatibilityID == 1 || config.BLSignalCompatibilityID == 2)
		if hasHDR10Fallback {
			videoTracef("[video] Dolby Vision profile %d has HDR10 compatible base layer (bl_compat_id=%d)",
				config.Profile, config.BLSignalCompatibilityID)
		} else if config.Profile == 5 {
			videoTracef("[video] Dolby Vision profile 5 detected - dual-layer without HDR10 fallback")
		} else if config.Profile == 7 {
			videoTracef("[video] Dolby Vision profile 7 detected - MEL/FEL enhancement layer")
		}

		return true, profileStr, "DV"
	}

	// Check profile for Dolby Vision markers
	profile := strings.ToLower(strings.TrimSpace(stream.Profile))
	if strings.Contains(profile, "dv") || strings.Contains(profile, "dolby") {
		videoTracef("[video] Dolby Vision detected via profile: %s", stream.Profile)
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

	args := []string{
		"-v", "error",
		"-probesize", "1000000", // 1MB (faster startup)
		"-analyzeduration", "500000", // 0.5s (faster startup)
		"-protocol_whitelist", "file,http,https,pipe,tcp,tls,crypto",
		"-print_format", "json",
		"-show_streams",
		"-show_chapters",
		"-show_format",
	}
	if reader == nil {
		if header := h.externalUsenetWebDAVAuthHeader(inputSpecifier); header != "" {
			args = append(args, "-headers", header)
		}
	}
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
		Chapters:            make([]chapterSummary, 0),
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
	for _, chapter := range meta.Chapters {
		start := parseChapterTime(chapter.StartTime, chapter.Start, chapter.TimeBase)
		end := parseChapterTime(chapter.EndTime, chapter.End, chapter.TimeBase)
		if start < 0 || (end > 0 && end <= start) {
			continue
		}
		if end < 0 {
			end = 0
		}
		resp.Chapters = append(resp.Chapters, chapterSummary{
			Title: normalizeTag(chapter.Tags, "title"),
			Start: start,
			End:   end,
		})
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
				BitRate:       getStreamBitrate(stream.BitRate, stream.Tags),
				ChannelLayout: strings.TrimSpace(stream.ChannelLayout),
				Profile:       strings.TrimSpace(stream.Profile),
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
				Index:                    stream.Index,
				CodecName:                strings.TrimSpace(stream.CodecName),
				CodecLongName:            strings.TrimSpace(stream.CodecLongName),
				Width:                    stream.Width,
				Height:                   stream.Height,
				BitRate:                  getStreamBitrate(stream.BitRate, stream.Tags),
				PixFmt:                   strings.TrimSpace(stream.PixFmt),
				Profile:                  strings.TrimSpace(stream.Profile),
				AvgFrameRate:             strings.TrimSpace(stream.AvgFrameRate),
				HasDolbyVision:           hasDV,
				DolbyVisionProfile:       dvProfile,
				DolbyVisionConfiguration: extractDolbyVisionConfiguration(stream),
				HdrFormat:                hdrFormat,
				ColorTransfer:            strings.TrimSpace(stream.ColorTransfer),
				ColorPrimaries:           strings.TrimSpace(stream.ColorPrimaries),
				ColorSpace:               strings.TrimSpace(stream.ColorSpace),
			}
			resp.VideoStreams = append(resp.VideoStreams, summary)
		case "subtitle":
			codecName := strings.ToLower(strings.TrimSpace(stream.CodecName))
			// Text-based subtitle codecs that can be converted to WebVTT
			textSubtitleCodecs := map[string]bool{
				"subrip": true, "srt": true, "ass": true, "ssa": true,
				"webvtt": true, "vtt": true, "mov_text": true, "text": true,
				"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
				"mpl2": true, "pjs": true, "realtext": true, "stl": true,
				"subviewer": true, "subviewer1": true, "vplayer": true,
			}
			// Bitmap subtitle codecs (cannot extract to VTT, but native player can render)
			bitmapSubtitleCodecs := map[string]bool{
				"hdmv_pgs_subtitle": true, "pgssub": true, "pgs": true,
				"dvd_subtitle": true, "dvdsub": true,
				"dvb_subtitle": true, "dvbsub": true,
				"xsub": true,
			}
			isBitmap := bitmapSubtitleCodecs[codecName]
			// Skip completely unknown subtitle formats
			if !textSubtitleCodecs[codecName] && !isBitmap {
				continue
			}
			summary := subtitleStreamSummary{
				Index:         stream.Index,
				CodecName:     strings.TrimSpace(stream.CodecName),
				CodecLongName: strings.TrimSpace(stream.CodecLongName),
				Language:      normalizeTag(stream.Tags, "language"),
				Title:         normalizeTag(stream.Tags, "title"),
				Disposition:   stream.Disposition,
				IsBitmap:      isBitmap,
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

func parseChapterTime(decimal string, ticks int64, timeBase string) float64 {
	if strings.TrimSpace(decimal) != "" {
		if value, err := strconv.ParseFloat(decimal, 64); err == nil && value >= 0 {
			return value
		}
	}
	parts := strings.Split(strings.TrimSpace(timeBase), "/")
	if len(parts) != 2 {
		return -1
	}
	numerator, errNumerator := strconv.ParseFloat(parts[0], 64)
	denominator, errDenominator := strconv.ParseFloat(parts[1], 64)
	if errNumerator != nil || errDenominator != nil || denominator == 0 {
		return -1
	}
	value := float64(ticks) * numerator / denominator
	if value < 0 {
		return -1
	}
	return value
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

// parseStartOffsetParam reads the seconds-into-the-item a stream was asked to
// begin at. "startOffset" is what the frontend sends and "start" is the older
// name; both the HLS session endpoint and the progressive MPEG-TS arm accept
// either. Anything absent, unparseable, negative or non-finite means "from the
// beginning", so a malformed value never changes what the caller receives.
func parseStartOffsetParam(r *http.Request) float64 {
	if r == nil || r.URL == nil {
		return 0
	}
	raw := strings.TrimSpace(r.URL.Query().Get("startOffset"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("start"))
	}
	if raw == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		log.Printf("[video] invalid start offset %q; starting from the beginning", raw)
		return 0
	}
	return parsed
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

// getStreamBitrate extracts bitrate from ffprobe stream data.
// FFprobe doesn't always populate the bit_rate field, but MKV containers
// often have BPS (bits per second) in the stream tags from mkvmerge statistics.
func getStreamBitrate(bitRate string, tags map[string]string) int64 {
	// Try standard bit_rate field first
	if br := parseInt64(bitRate); br > 0 {
		return br
	}
	// Fall back to MKV BPS tag if available
	if tags != nil {
		if bps := parseInt64(tags["BPS"]); bps > 0 {
			return bps
		}
	}
	return 0
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
	args []string
	// container is the muxer the args write to and the Content-Type the response
	// must advertise. Zero value is the fragmented MP4 output.
	container          transmuxContainer
	audio              audioPlan
	videoMap           string
	videoCodec         string
	hasDolbyVision     bool
	dolbyVisionProfile string
	usedProbe          bool
	movflags           string
	// duration is the length of what this plan actually produces: the item's
	// duration less startOffset.
	duration float64
	// startOffset is where in the item the produced stream begins.
	startOffset float64
}

type ffprobeOutput struct {
	Streams  []ffprobeStream  `json:"streams"`
	Chapters []ffprobeChapter `json:"chapters"`
	Format   ffprobeFormat    `json:"format"`
}

type ffprobeChapter struct {
	ID        int               `json:"id"`
	TimeBase  string            `json:"time_base"`
	Start     int64             `json:"start"`
	StartTime string            `json:"start_time"`
	End       int64             `json:"end"`
	EndTime   string            `json:"end_time"`
	Tags      map[string]string `json:"tags"`
}

type ffprobeStream struct {
	Index          int               `json:"index"`
	ID             string            `json:"id"`
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
	Level          int               `json:"level"`
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
	Profile       string         `json:"profile,omitempty"`
	Language      string         `json:"language,omitempty"`
	Title         string         `json:"title,omitempty"`
	Disposition   map[string]int `json:"disposition,omitempty"`
	CopySupported bool           `json:"copySupported"`
}

type videoStreamSummary struct {
	Index                    int                              `json:"index"`
	CodecName                string                           `json:"codecName"`
	CodecLongName            string                           `json:"codecLongName,omitempty"`
	Width                    int                              `json:"width,omitempty"`
	Height                   int                              `json:"height,omitempty"`
	BitRate                  int64                            `json:"bitRate,omitempty"`
	PixFmt                   string                           `json:"pixFmt,omitempty"`
	Profile                  string                           `json:"profile,omitempty"`
	AvgFrameRate             string                           `json:"avgFrameRate,omitempty"`
	HasDolbyVision           bool                             `json:"hasDolbyVision"`
	DolbyVisionProfile       string                           `json:"dolbyVisionProfile,omitempty"`
	DolbyVisionConfiguration *models.DolbyVisionConfiguration `json:"dolbyVisionConfiguration,omitempty"`
	HdrFormat                string                           `json:"hdrFormat,omitempty"`
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
	IsBitmap      bool           `json:"isBitmap,omitempty"` // True for PGS, HDMV, DVD subtitles (can't extract to VTT)
}

type chapterSummary struct {
	Title string  `json:"title,omitempty"`
	Start float64 `json:"start"`
	End   float64 `json:"end,omitempty"`
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
	Chapters              []chapterSummary        `json:"chapters"`
	NeedsAudioTranscode   bool                    `json:"needsAudioTranscode"`
	SelectedSubtitleIndex int                     `json:"selectedSubtitleIndex"`
	Notes                 []string                `json:"notes,omitempty"`
}

// cachedMetadataEntry stores a metadata response with expiration time
type cachedMetadataEntry struct {
	response  *videoMetadataResponse
	expiresAt time.Time
}

// metadataCacheTTL is the TTL for cached metadata responses (2 hours, same as probe cache)
const metadataCacheTTL = 2 * time.Hour

// getCachedMetadata retrieves a cached metadata response if available and not expired
func (h *VideoHandler) getCachedMetadata(path string) *videoMetadataResponse {
	h.metadataCacheMu.RLock()
	defer h.metadataCacheMu.RUnlock()

	entry, exists := h.metadataCache[path]
	if !exists {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		return nil // Expired, will be cleaned up later
	}
	return entry.response
}

// setCachedMetadata stores a metadata response in the cache
func (h *VideoHandler) setCachedMetadata(path string, response *videoMetadataResponse) {
	h.metadataCacheMu.Lock()
	defer h.metadataCacheMu.Unlock()

	h.metadataCache[path] = &cachedMetadataEntry{
		response:  response,
		expiresAt: time.Now().Add(metadataCacheTTL),
	}
	videoTracef("[video] metadata cached for path: %s (expires in %v)", path, metadataCacheTTL)
}

// runMetadataCacheCleanup periodically removes expired entries from the metadata cache
// to prevent unbounded memory growth
func (h *VideoHandler) runMetadataCacheCleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		h.cleanExpiredMetadataCache()
	}
}

// cleanExpiredMetadataCache removes expired entries from the metadata cache
func (h *VideoHandler) cleanExpiredMetadataCache() {
	h.metadataCacheMu.Lock()
	defer h.metadataCacheMu.Unlock()

	now := time.Now()
	expired := 0
	for path, entry := range h.metadataCache {
		if now.After(entry.expiresAt) {
			delete(h.metadataCache, path)
			expired++
		}
	}

	if expired > 0 {
		videoTracef("[video] metadata cache cleanup: removed %d expired entries, %d remaining", expired, len(h.metadataCache))
	}
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

// dlnaContentFeatures advertises byte-seek support (OP=01), no transcode
// (CI=0), and the standard streaming flag set: streaming transfer mode,
// background transfer, connection stalling, DLNA v1.5.
const dlnaContentFeatures = "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"

// writeDlnaHeaders satisfies the DLNA HTTP serving requirements that strict
// renderers enforce. LG webOS negotiates a stream, then aborts with "file
// cannot be recognized" when the server never echoes transferMode.dlna.org or
// confirms contentFeatures.dlna.org, regardless of codec or container. The
// DIDL-Lite we send already advertises DLNA.ORG_OP=01, so the HTTP responses
// have to agree or the renderer treats the resource as inconsistent.
func writeDlnaHeaders(w http.ResponseWriter, r *http.Request) {
	mode := strings.TrimSpace(r.Header.Get("transferMode.dlna.org"))
	if mode == "" {
		mode = "Streaming"
	}
	// Assign the header map directly: Set would canonicalize these to
	// "Transfermode.dlna.org", and DLNA renderers match the spec casing.
	w.Header()["transferMode.dlna.org"] = []string{mode}
	w.Header()["contentFeatures.dlna.org"] = []string{dlnaContentFeatures}
	w.Header().Set("Accept-Ranges", "bytes")
}

// dlnaMpegTsContentFeatures describes the progressive transport stream. OP=00
// because a live transcode cannot byte-seek, which is consistent with the
// Accept-Ranges: none this arm sends; CI=1 because the stream is transcoded.
const dlnaMpegTsContentFeatures = "DLNA.ORG_PN=AVC_TS_HD_24_AC3_ISO;DLNA.ORG_OP=00;DLNA.ORG_CI=1;DLNA.ORG_FLAGS=01700000000000000000000000000000"

// writeDlnaMpegTsHeaders describes the progressive MPEG-TS transcode to a
// renderer. It cannot reuse writeDlnaHeaders, which advertises byte seeking this
// arm does not support. Omitting contentFeatures entirely is not an option: a
// DLNA 1.0 renderer that asked for it and receives nothing cannot confirm the
// profile and rejects SetAVTransportURI with UPnP 714 "Illegal MIME-type"
// (observed on a Sony BRAVIA KDL-46NX700).
func writeDlnaMpegTsHeaders(w http.ResponseWriter, r *http.Request) {
	if r == nil {
		return
	}
	mode := strings.TrimSpace(r.Header.Get("transferMode.dlna.org"))
	if mode == "" {
		mode = "Streaming"
	}
	// Assign the header map directly: Set would canonicalize the keys to
	// "Transfermode.dlna.org", and DLNA renderers match the spec casing.
	w.Header()["transferMode.dlna.org"] = []string{mode}
	w.Header()["contentFeatures.dlna.org"] = []string{dlnaMpegTsContentFeatures}
}

func sanitizeExternalDisplayName(input string) string {
	name := strings.TrimSpace(input)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	// Avoid path traversal characters inside filename contexts.
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	return strings.TrimSpace(name)
}

func isDebridStreamPath(cleanPath string) bool {
	trimmed := strings.TrimSpace(cleanPath)
	trimmed = strings.TrimPrefix(trimmed, "/")
	trimmed = strings.TrimPrefix(trimmed, "webdav/")
	return strings.HasPrefix(strings.ToLower(trimmed), "debrid/")
}

func inferFilenameFromPath(cleanPath string) string {
	raw := strings.TrimSpace(cleanPath)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if parsed, err := url.Parse(raw); err == nil {
			raw = parsed.Path
		}
	}
	base := path.Base(raw)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(base); err == nil && strings.TrimSpace(decoded) != "" {
		base = decoded
	}
	return sanitizeExternalDisplayName(base)
}

func normalizeMediaContentType(w http.ResponseWriter, filename, sourcePath string) {
	current := strings.ToLower(strings.TrimSpace(strings.Split(w.Header().Get("Content-Type"), ";")[0]))
	if current != "" && current != "application/force-download" && current != "application/octet-stream" && current != "binary/octet-stream" {
		return
	}

	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(strings.TrimSpace(sourcePath)))
	}
	if ext == "" && (strings.HasPrefix(sourcePath, "http://") || strings.HasPrefix(sourcePath, "https://")) {
		if parsed, err := url.Parse(sourcePath); err == nil {
			ext = strings.ToLower(filepath.Ext(parsed.Path))
		}
	}

	switch ext {
	case ".mkv":
		w.Header().Set("Content-Type", "video/x-matroska")
	case ".mp4", ".m4v", ".mov":
		w.Header().Set("Content-Type", "video/mp4")
	case ".avi":
		w.Header().Set("Content-Type", "video/x-msvideo")
	case ".webm":
		w.Header().Set("Content-Type", "video/webm")
	case ".ts", ".m2ts", ".mts":
		w.Header().Set("Content-Type", "video/mp2t")
	}
}

func buildInlineContentDisposition(filename string) string {
	safe := sanitizeExternalDisplayName(filename)
	if safe == "" {
		safe = "stream"
	}
	quoted := strings.ReplaceAll(safe, `"`, `'`)
	encoded := url.PathEscape(safe)
	return fmt.Sprintf(`inline; filename="%s"; filename*=UTF-8''%s`, quoted, encoded)
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
	path = resolveScopedPlaybackPath(r, path)
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	if !h.requireAllowedExternalPath(w, r, path) {
		return
	}

	// Clean the path
	cleanPath := path
	if strings.HasPrefix(cleanPath, "/webdav/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/webdav")
	} else if strings.HasPrefix(cleanPath, "webdav/") {
		cleanPath = "/" + strings.TrimPrefix(cleanPath, "webdav/")
	}
	if !h.requireLibraryStreamAccess(w, r, cleanPath) {
		return
	}

	// Check for Dolby Vision and HDR10 flags
	hasDV := r.URL.Query().Get("dv") == "true"
	dvProfile := r.URL.Query().Get("dvProfile")
	hasHDR := r.URL.Query().Get("hdr") == "true"
	forceAAC := r.URL.Query().Get("forceAAC") == "true"
	castMode := r.URL.Query().Get("cast") == "true"
	playbackTarget := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	castProfile := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("castProfile")))
	if castMode && isDirectCastTarget(playbackTarget, castProfile) {
		playbackTarget = "cast-direct"
	}

	var castReceiverHost string
	if castMode {
		if ip := net.ParseIP(strings.TrimSpace(r.URL.Query().Get("castDeviceIp"))); ip != nil {
			castReceiverHost = ip.String()
		}
	}

	durationHint := 0.0
	if durationParam := strings.TrimSpace(r.URL.Query().Get("durationHint")); durationParam != "" {
		if parsed, err := strconv.ParseFloat(durationParam, 64); err == nil {
			durationHint = parsed
		}
	}
	// Check global setting for forced AAC transcoding (for Bluetooth compatibility)
	if !forceAAC && h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			forceAAC = settings.Playback.ForceAACTranscoding
		}
	}

	if hasDV && isDolbyVisionProfile7(dvProfile) {
		videoTracef("[video] Dolby Vision profile 7 detected for path=%q; falling back to HDR10-only HLS output", cleanPath)
		hasDV = false
		dvProfile = ""
		hasHDR = true // DV Profile 7 has HDR10 base layer
	}

	startSeconds := parseStartOffsetParam(r)

	// Support percentage-based resume (from Trakt imports where real duration is unknown).
	// If startPercent is provided and startOffset is not, probe the file duration and compute.
	if startSeconds == 0 {
		if pctParam := strings.TrimSpace(r.URL.Query().Get("startPercent")); pctParam != "" {
			if pct, err := strconv.ParseFloat(pctParam, 64); err == nil && pct > 0 && pct < 100 {
				if dur, err := h.hlsManager.probeDuration(r.Context(), cleanPath); err == nil && dur > 0 {
					startSeconds = (pct / 100) * dur
					videoTracef("[video] Resolved startPercent=%.1f%% to startOffset=%.1fs (duration=%.1fs)", pct, startSeconds, dur)
				}
			}
		}
	}

	// Parse selected audio/subtitle track indices
	audioTrackIndex := -1 // -1 means use default (all tracks or first track)
	audioParam := strings.TrimSpace(r.URL.Query().Get("audioTrack"))
	if audioParam != "" {
		if parsed, err := strconv.Atoi(audioParam); err == nil && parsed >= 0 {
			audioTrackIndex = parsed
			videoTracef("[video] HLS session requested audio track: %d", audioTrackIndex)
		}
	}

	subtitleTrackIndex := -1 // -1 means no subtitles
	subtitleParam := strings.TrimSpace(r.URL.Query().Get("subtitleTrack"))
	if subtitleParam != "" {
		if parsed, err := strconv.Atoi(subtitleParam); err == nil && parsed >= 0 {
			subtitleTrackIndex = parsed
			videoTracef("[video] HLS session requested subtitle track: %d", subtitleTrackIndex)
		}
	}

	// Extract profile info from query params
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}
	profileName := r.URL.Query().Get("profileName")
	mediaMetadata := parseStreamMediaMetadata(r)

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

	// For warm start sessions, probe for the actual keyframe position FFmpeg will seek to
	// BEFORE creating the session. Native clients need this so video and sidecar subtitles
	// share the same keyframe anchor.
	//
	// Web/browser is different: in-session /seek intentionally skips this probe and works,
	// while create-with-probed-offset has been observed to buffer HLS segments without ever
	// reaching canplay (MSE stuck). Match the working seek path for web warm starts.
	transcodingOffset := startSeconds
	if startSeconds > 0 {
		if isWebBrowserPlaybackTarget(playbackTarget) {
			videoTracef("[video] web warm start: skipping keyframe probe (match seek path) start=%.3fs", startSeconds)
		} else {
			keyframePos := h.hlsManager.probeKeyframePosition(r.Context(), cleanPath, startSeconds)
			transcodingOffset = keyframePos
			videoTracef("[video] warm start: probed keyframe position %.3fs (requested %.3fs, delta %.3fs)",
				keyframePos, startSeconds, keyframePos-startSeconds)
		}
	}

	videoTracef("[video] creating HLS session for path=%q dv=%v dvProfile=%q hdr=%v start=%.3fs transcodingOffset=%.3fs audioTrack=%d subtitleTrack=%d",
		cleanPath, hasDV, dvProfile, hasHDR, startSeconds, transcodingOffset, audioTrackIndex, subtitleTrackIndex)

	session, err := h.hlsManager.CreateSession(r.Context(), cleanPath, path, hasDV, dvProfile, hasHDR, forceAAC, startSeconds, transcodingOffset, audioTrackIndex, subtitleTrackIndex, profileID, profileName, getClientIP(r), castMode, "", playbackTarget, durationHint, castReceiverHost)
	if err != nil {
		log.Printf("[video] failed to create HLS session: %v", err)
		if errors.Is(err, streaming.ErrStaleTorrent) {
			http.Error(w, "debrid torrent expired or deleted — please re-resolve", http.StatusGone)
		} else if errors.Is(err, errExternalStreamPlaceholder) {
			h.invalidatePrequeuesForFailedPath(path)
			http.Error(w, "external stream expired — please re-resolve", http.StatusGone)
		} else {
			http.Error(w, fmt.Sprintf("failed to create HLS session: %v", err), http.StatusInternalServerError)
		}
		return
	}
	session.mu.Lock()
	session.MediaMetadata = mediaMetadata
	session.ClientID = clientID
	session.ViaShareLink = auth.IsShareLinkRequest(r)
	session.mu.Unlock()

	// Correlate with the prequeue request that resolved this stream so the
	// first-frame latency sample covers the whole click→frame path.
	h.linkPreparedSessionToPrequeue(session.ID, cleanPath)

	session.mu.RLock()
	actualStartOffset := session.ActualStartOffset
	session.mu.RUnlock()
	// Delta between actual keyframe position and requested position (negative = keyframe is earlier)
	keyframeDelta := actualStartOffset - startSeconds

	// Return session ID, playlist URL, and duration (if available)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	response := map[string]interface{}{
		"sessionId":          session.ID,
		"playlistUrl":        h.hlsManager.buildSessionPlaylistURL(session),
		"startOffset":        session.StartOffset,
		"actualStartOffset":  actualStartOffset,
		"keyframeDelta":      keyframeDelta,
		"stableCastTimeline": session.usesStableCastTimeline() && session.Duration > 0,
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

	videoTracef("[video] created HLS session %s (duration=%.2fs)", session.ID, session.Duration)
}

type youtubeHLSURLs struct {
	videoURL string
	audioURL string
}

type youtubeCaptionTrack struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Language string `json:"language"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind"`
	Ext      string `json:"ext"`
	URL      string `json:"url"`
}

// StartYouTubeHLSSession creates an HLS session from high-quality separate YouTube video/audio streams.
func (h *VideoHandler) StartYouTubeHLSSession(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	videoPageURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if videoPageURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}
	if !isYouTubeURL(videoPageURL) {
		http.Error(w, "only YouTube URLs are supported", http.StatusBadRequest)
		return
	}

	proxyURL := h.ytdlpProxyURL()
	log.Printf("[hls-youtube] start request url=%s proxy=%v", videoPageURL, strings.TrimSpace(proxyURL) != "")
	streams, err := h.extractYouTubeHLSURLs(r.Context(), videoPageURL, true)
	if err != nil {
		log.Printf("[hls-youtube] extract failed url=%s: %v", videoPageURL, err)
		http.Error(w, fmt.Sprintf("failed to extract YouTube streams: %v", err), http.StatusBadGateway)
		return
	}

	type captionResult struct {
		tracks []youtubeCaptionTrack
		err    error
	}
	captionDone := make(chan captionResult, 1)
	go func() {
		tracks, captionErr := h.extractYouTubeCaptionTracks(r.Context(), videoPageURL)
		captionDone <- captionResult{tracks: tracks, err: captionErr}
	}()

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		profileID = r.URL.Query().Get("userId")
	}
	profileName := r.URL.Query().Get("profileName")
	clientIP := getClientIP(r)
	session, err := h.hlsManager.CreateYouTubeSession(r.Context(), streams.videoURL, streams.audioURL, videoPageURL, proxyURL, profileID, profileName, clientIP)
	if err != nil && h.ytdlpCookiesPath() != "" && strings.Contains(err.Error(), "403") {
		log.Printf("[hls-youtube] cookie-backed session failed with 403; retrying without cookies url=%s", videoPageURL)
		retryStreams, retryErr := h.extractYouTubeHLSURLs(r.Context(), videoPageURL, false)
		if retryErr != nil {
			log.Printf("[hls-youtube] no-cookie extract failed url=%s: %v", videoPageURL, retryErr)
		} else {
			retrySession, retryCreateErr := h.hlsManager.CreateYouTubeSession(r.Context(), retryStreams.videoURL, retryStreams.audioURL, videoPageURL, proxyURL, profileID, profileName, clientIP)
			if retryCreateErr == nil {
				streams = retryStreams
				session = retrySession
				err = nil
				log.Printf("[hls-youtube] no-cookie session retry succeeded url=%s session=%s", videoPageURL, session.ID)
			} else {
				log.Printf("[hls-youtube] no-cookie session retry failed url=%s video={%s} audio={%s}: %v",
					videoPageURL,
					youtubeMediaURLLogSummary(retryStreams.videoURL),
					youtubeMediaURLLogSummary(retryStreams.audioURL),
					retryCreateErr)
			}
		}
	}
	if err != nil {
		log.Printf("[hls-youtube] create session failed url=%s video={%s} audio={%s}: %v",
			videoPageURL,
			youtubeMediaURLLogSummary(streams.videoURL),
			youtubeMediaURLLogSummary(streams.audioURL),
			err)
		http.Error(w, fmt.Sprintf("failed to create YouTube HLS session: %v", err), http.StatusInternalServerError)
		return
	}
	session.mu.Lock()
	session.ClientID = requestClientID(r)
	session.MediaMetadata = parseStreamMediaMetadata(r)
	session.mu.Unlock()

	var captionTracks []youtubeCaptionTrack
	select {
	case result := <-captionDone:
		if result.err != nil {
			log.Printf("[hls-youtube] caption extract failed: %v", result.err)
		} else {
			captionTracks = result.tracks
			log.Printf("[hls-youtube] caption extract found %d tracks for %s", len(captionTracks), videoPageURL)
		}
	default:
		log.Printf("[hls-youtube] caption extract still pending for %s; responding without subtitle tracks", videoPageURL)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	response := map[string]interface{}{
		"sessionId":         session.ID,
		"playlistUrl":       h.hlsManager.buildSessionPlaylistURL(session),
		"startOffset":       0,
		"actualStartOffset": 0,
		"keyframeDelta":     0,
	}
	if len(captionTracks) > 0 {
		response["subtitleTracks"] = captionTracks
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[hls-youtube] failed to encode response: %v", err)
	}
}

func (h *VideoHandler) ytdlpPath() (string, error) {
	ytdlpPath := "/usr/local/bin/yt-dlp"
	if _, err := exec.LookPath(ytdlpPath); err != nil {
		ytdlpPath = "yt-dlp"
		if _, err := exec.LookPath(ytdlpPath); err != nil {
			return "", fmt.Errorf("yt-dlp not found")
		}
	}
	return ytdlpPath, nil
}

func (h *VideoHandler) ytdlpProxyURL() string {
	if h.configManager == nil {
		return ""
	}
	settings, err := h.configManager.Load()
	if err != nil {
		log.Printf("[hls-youtube] failed to load yt-dlp proxy setting: %v", err)
		return ""
	}
	return settings.Playback.YouTubeProxyURL
}

func youtubeMediaURLLogSummary(rawURL string) string {
	if strings.TrimSpace(rawURL) == "" {
		return "empty=true"
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "parse_error=true"
	}
	q := parsed.Query()
	fields := []string{
		"host=" + parsed.Hostname(),
		"itag=" + q.Get("itag"),
		"c=" + q.Get("c"),
		"mime=" + q.Get("mime"),
		"clen=" + q.Get("clen"),
		"dur=" + q.Get("dur"),
		"source=" + q.Get("source"),
		"gir=" + q.Get("gir"),
		"rqh=" + q.Get("rqh"),
	}
	if expireRaw := q.Get("expire"); expireRaw != "" {
		if expireUnix, parseErr := strconv.ParseInt(expireRaw, 10, 64); parseErr == nil {
			fields = append(fields, fmt.Sprintf("expireIn=%ds", time.Until(time.Unix(expireUnix, 0)).Round(time.Second)/time.Second))
		} else {
			fields = append(fields, "expire=invalid")
		}
	}
	if q.Get("ip") != "" {
		fields = append(fields, "ip_param=true")
	}
	if q.Get("sig") != "" || q.Get("signature") != "" || q.Get("lsig") != "" || q.Get("spc") != "" {
		fields = append(fields, "signed=true")
	}
	return strings.Join(fields, " ")
}

func sanitizeYouTubeMediaURLsInText(text string) string {
	return googleVideoURLPattern.ReplaceAllStringFunc(text, func(rawURL string) string {
		suffix := ""
		for len(rawURL) > 0 {
			last := rawURL[len(rawURL)-1]
			if last != '.' && last != ',' && last != ';' && last != ':' {
				break
			}
			suffix = string(last) + suffix
			rawURL = rawURL[:len(rawURL)-1]
		}
		return "googlevideo_url{" + youtubeMediaURLLogSummary(rawURL) + "}" + suffix
	})
}

func (h *VideoHandler) extractYouTubeHLSURLs(ctx context.Context, videoPageURL string, allowCookies bool) (youtubeHLSURLs, error) {
	ytdlpPath, err := h.ytdlpPath()
	if err != nil {
		return youtubeHLSURLs{}, err
	}

	extractCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	args := []string{
		"-g",
		"--format", "bestvideo[vcodec^=avc1][height<=1080]+bestaudio[acodec^=mp4a]/bestvideo[height<=1080]+bestaudio",
		"--no-warnings",
		"--no-playlist",
		"--socket-timeout", "10",
		"--retries", "0",
		"--fragment-retries", "0",
	}
	cookiesPath := h.ytdlpCookiesPath()
	proxyURL := h.ytdlpProxyURL()
	usingCookies := allowCookies && cookiesPath != ""
	if usingCookies {
		args = append(args, "--cookies", cookiesPath)
	}
	args = ytdlp.AppendProxyArgs(args, proxyURL)
	args = append(args, videoPageURL)

	cmd := exec.CommandContext(extractCtx, ytdlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	log.Printf("[hls-youtube] extracting media urls url=%s ytdlp=%s cookies=%v proxy=%v",
		videoPageURL,
		ytdlpPath,
		usingCookies,
		strings.TrimSpace(proxyURL) != "")
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return youtubeHLSURLs{}, fmt.Errorf("yt-dlp failed after %s: %s", time.Since(started).Round(time.Millisecond), errMsg)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	urls := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			urls = append(urls, line)
		}
	}
	if len(urls) < 2 {
		return youtubeHLSURLs{}, fmt.Errorf("expected separate video/audio URLs, got %d", len(urls))
	}
	log.Printf("[hls-youtube] extracted media urls url=%s count=%d elapsed=%s video={%s} audio={%s}",
		videoPageURL,
		len(urls),
		time.Since(started).Round(time.Millisecond),
		youtubeMediaURLLogSummary(urls[0]),
		youtubeMediaURLLogSummary(urls[1]))
	return youtubeHLSURLs{videoURL: urls[0], audioURL: urls[1]}, nil
}

func (h *VideoHandler) extractYouTubeCaptionTracks(ctx context.Context, videoPageURL string) ([]youtubeCaptionTrack, error) {
	ytdlpPath, err := h.ytdlpPath()
	if err != nil {
		return nil, err
	}

	extractCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	args := []string{
		"--dump-json",
		"--skip-download",
		"--no-warnings",
		"--no-playlist",
		"--socket-timeout", "10",
		"--retries", "0",
		"--fragment-retries", "0",
	}
	if cookiesPath := h.ytdlpCookiesPath(); cookiesPath != "" {
		args = append(args, "--cookies", cookiesPath)
	}
	args = ytdlp.AppendProxyArgs(args, h.ytdlpProxyURL())
	args = append(args, videoPageURL)

	cmd := exec.CommandContext(extractCtx, ytdlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp caption metadata failed: %s", errMsg)
	}

	var metadata struct {
		Subtitles map[string][]struct {
			Ext  string `json:"ext"`
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"subtitles"`
		AutomaticCaptions map[string][]struct {
			Ext  string `json:"ext"`
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"automatic_captions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &metadata); err != nil {
		return nil, fmt.Errorf("parse yt-dlp caption metadata: %w", err)
	}

	tracks := make([]youtubeCaptionTrack, 0)
	seenLanguages := make(map[string]bool)
	appendTracks := func(kind string, source map[string][]struct {
		Ext  string `json:"ext"`
		URL  string `json:"url"`
		Name string `json:"name"`
	}) {
		languages := make([]string, 0, len(source))
		for language := range source {
			languages = append(languages, language)
		}
		sort.SliceStable(languages, func(i, j int) bool {
			priority := func(language string) int {
				normalized := strings.ToLower(strings.TrimSpace(language))
				switch {
				case normalized == "en" || normalized == "en-us" || normalized == "en-gb":
					return 0
				case strings.HasPrefix(normalized, "en-"):
					return 1
				case !strings.Contains(normalized, "-"):
					return 2
				default:
					return 3
				}
			}
			if priority(languages[i]) != priority(languages[j]) {
				return priority(languages[i]) < priority(languages[j])
			}
			return languages[i] < languages[j]
		})

		for _, language := range languages {
			if len(tracks) >= 20 {
				return
			}
			normalizedLanguage := strings.TrimSpace(strings.ToLower(language))
			if normalizedLanguage == "" || normalizedLanguage == "live_chat" {
				continue
			}
			if seenLanguages[normalizedLanguage] {
				continue
			}

			var selected *struct {
				Ext  string `json:"ext"`
				URL  string `json:"url"`
				Name string `json:"name"`
			}
			for i := range source[language] {
				format := &source[language][i]
				if strings.TrimSpace(format.URL) == "" {
					continue
				}
				if kind == "automatic" && youtubeCaptionFormatIsTranslated(format.URL) {
					continue
				}
				if strings.EqualFold(format.Ext, "vtt") {
					selected = format
					break
				}
				if selected == nil {
					selected = format
				}
			}
			if selected == nil || strings.TrimSpace(selected.URL) == "" {
				continue
			}

			seenLanguages[normalizedLanguage] = true
			name := strings.TrimSpace(selected.Name)
			label := language
			if name != "" && !strings.EqualFold(name, language) {
				label = fmt.Sprintf("%s - %s", language, name)
			}
			if kind == "automatic" {
				label = fmt.Sprintf("%s (auto)", label)
			}
			proxyURL := "/youtube/captions?captionUrl=" + base64.RawURLEncoding.EncodeToString([]byte(selected.URL))
			tracks = append(tracks, youtubeCaptionTrack{
				ID:       fmt.Sprintf("youtube-%s-%s", kind, normalizedLanguage),
				Label:    label,
				Language: language,
				Name:     name,
				Kind:     kind,
				Ext:      strings.TrimSpace(selected.Ext),
				URL:      proxyURL,
			})
		}
	}

	appendTracks("subtitles", metadata.Subtitles)
	appendTracks("automatic", metadata.AutomaticCaptions)
	return tracks, nil
}

func youtubeCaptionFormatIsTranslated(rawURL string) bool {
	captionURL, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.TrimSpace(captionURL.Query().Get("tlang")) != ""
}

func (h *VideoHandler) ProxyYouTubeCaption(w http.ResponseWriter, r *http.Request) {
	rawCaptionURL := strings.TrimSpace(r.URL.Query().Get("url"))
	encodedCaptionURL := strings.TrimSpace(r.URL.Query().Get("captionUrl"))
	if encodedCaptionURL != "" {
		decodedCaptionURL, err := base64.RawURLEncoding.DecodeString(encodedCaptionURL)
		if err != nil {
			http.Error(w, "invalid captionUrl parameter", http.StatusBadRequest)
			return
		}
		rawCaptionURL = string(decodedCaptionURL)
	}
	if rawCaptionURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}
	captionURL, err := url.Parse(rawCaptionURL)
	if err != nil || (captionURL.Scheme != "http" && captionURL.Scheme != "https") {
		http.Error(w, "invalid caption url", http.StatusBadRequest)
		return
	}
	host := strings.ToLower(captionURL.Hostname())
	isYouTubeCaptionHost := host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
	if !isYouTubeCaptionHost {
		http.Error(w, "unsupported caption host", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, captionURL.String(), nil)
	if err != nil {
		http.Error(w, "invalid caption request", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[hls-youtube] caption proxy request failed: %v", err)
		http.Error(w, "failed to fetch caption", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("caption upstream returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		log.Printf("[hls-youtube] caption proxy read failed: %v", err)
		http.Error(w, "failed to read caption", http.StatusBadGateway)
		return
	}
	cleaned := cleanYouTubeVTT(body)
	rawSample := youtubeCaptionDebugSample(body)
	cleanedSample := youtubeCaptionDebugSample(cleaned)
	log.Printf("[hls-youtube] caption proxy sample host=%s lang=%q fmt=%q raw=%q cleaned=%q",
		host,
		captionURL.Query().Get("lang"),
		captionURL.Query().Get("fmt"),
		rawSample,
		cleanedSample,
	)

	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	written, err := w.Write(cleaned)
	if err != nil {
		log.Printf("[hls-youtube] caption proxy write failed: %v", err)
	}
	log.Printf("[hls-youtube] caption proxy served %d bytes for host=%s raw=%d", written, host, len(body))
}

var (
	youtubeVTTInlineTimestampPattern = regexp.MustCompile(`<\d{1,2}:\d{2}(?::\d{2})?\.\d{3}>`)
	youtubeVTTInlineTagPattern       = regexp.MustCompile(`</?[^>\n]+>`)
	youtubeVTTWhitespacePattern      = regexp.MustCompile(`[ \t]{2,}`)
	youtubeVTTCueTimingPattern       = regexp.MustCompile(`^(\d{1,2}:\d{2}(?::\d{2})?\.\d{3})\s+-->\s+(\d{1,2}:\d{2}(?::\d{2})?\.\d{3})(.*)$`)
)

func cleanYouTubeVTT(body []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(trimmed, "WEBVTT") ||
			strings.HasPrefix(trimmed, "NOTE") ||
			strings.HasPrefix(trimmed, "STYLE") ||
			strings.HasPrefix(trimmed, "REGION") ||
			strings.Contains(trimmed, "-->") {
			continue
		}

		cleaned := youtubeVTTInlineTimestampPattern.ReplaceAllString(line, "")
		cleaned = youtubeVTTInlineTagPattern.ReplaceAllString(cleaned, "")
		cleaned = html.UnescapeString(cleaned)
		cleaned = youtubeVTTWhitespacePattern.ReplaceAllString(cleaned, " ")
		lines[i] = strings.TrimSpace(cleaned)
	}
	return normalizeYouTubeVTTCues(lines)
}

func youtubeCaptionDebugSample(body []byte) string {
	const maxSampleRunes = 700
	const maxSampleLines = 8

	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.ToValidUTF8(normalized, "\uFFFD")
	lines := strings.Split(normalized, "\n")

	sample := make([]string, 0, maxSampleLines)
	foundTiming := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "-->") {
			continue
		}
		foundTiming = true
		if i > 0 {
			previous := strings.TrimSpace(lines[i-1])
			if previous != "" &&
				!strings.HasPrefix(previous, "WEBVTT") &&
				!strings.HasPrefix(previous, "Kind:") &&
				!strings.HasPrefix(previous, "Language:") &&
				!strings.HasPrefix(previous, "NOTE") &&
				!strings.HasPrefix(previous, "STYLE") &&
				!strings.HasPrefix(previous, "REGION") {
				sample = append(sample, previous)
			}
		}
		sample = append(sample, trimmed)
		for j := i + 1; j < len(lines) && len(sample) < maxSampleLines; j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" {
				break
			}
			sample = append(sample, next)
		}
		break
	}

	if !foundTiming {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" ||
				strings.HasPrefix(trimmed, "WEBVTT") ||
				strings.HasPrefix(trimmed, "Kind:") ||
				strings.HasPrefix(trimmed, "Language:") ||
				strings.HasPrefix(trimmed, "NOTE") ||
				strings.HasPrefix(trimmed, "STYLE") ||
				strings.HasPrefix(trimmed, "REGION") {
				continue
			}
			sample = append(sample, trimmed)
			if len(sample) >= maxSampleLines {
				break
			}
		}
	}

	if len(sample) == 0 {
		return ""
	}

	out := strings.Join(sample, "\\n")
	runes := []rune(out)
	if len(runes) > maxSampleRunes {
		out = string(runes[:maxSampleRunes]) + "...[truncated]"
	}
	return out
}

type youtubeVTTCue struct {
	prefix []string
	start  string
	end    string
	suffix string
	text   []string
}

func normalizeYouTubeVTTCues(lines []string) []byte {
	cues := make([]youtubeVTTCue, 0)

	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}

		timingIndex := i
		prefix := make([]string, 0, 1)
		timingMatch := youtubeVTTCueTimingPattern.FindStringSubmatch(line)
		if timingMatch == nil && i+1 < len(lines) {
			nextLine := strings.TrimSpace(lines[i+1])
			if nextMatch := youtubeVTTCueTimingPattern.FindStringSubmatch(nextLine); nextMatch != nil {
				prefix = append(prefix, line)
				timingIndex = i + 1
				timingMatch = nextMatch
			}
		}

		if timingMatch == nil {
			i++
			continue
		}

		text := make([]string, 0, 1)
		i = timingIndex + 1
		for i < len(lines) {
			textLine := strings.TrimSpace(lines[i])
			if textLine == "" {
				break
			}
			text = append(text, textLine)
			i++
		}

		collapsedText := collapseYouTubeVTTText(text)
		if len(collapsedText) == 0 {
			continue
		}

		cues = append(cues, youtubeVTTCue{
			prefix: prefix,
			start:  timingMatch[1],
			end:    timingMatch[2],
			suffix: timingMatch[3],
			text:   collapsedText,
		})
	}

	for i := 0; i+1 < len(cues); i++ {
		nextStart := cues[i+1].start
		if nextStart != "" && compareVTTTimestamps(nextStart, cues[i].start) > 0 && compareVTTTimestamps(nextStart, cues[i].end) < 0 {
			cues[i].end = nextStart
		}
	}

	out := []string{"WEBVTT", ""}
	for _, cue := range cues {
		out = append(out, cue.prefix...)
		out = append(out, fmt.Sprintf("%s --> %s%s", cue.start, cue.end, cue.suffix))
		out = append(out, cue.text...)
		out = append(out, "")
	}
	return []byte(strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n")
}

func collapseYouTubeVTTText(lines []string) []string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return []string{line}
		}
	}
	return nil
}

func compareVTTTimestamps(a string, b string) int {
	aMillis, aOK := parseVTTTimestampMillis(a)
	bMillis, bOK := parseVTTTimestampMillis(b)
	if !aOK || !bOK {
		return strings.Compare(a, b)
	}
	switch {
	case aMillis < bMillis:
		return -1
	case aMillis > bMillis:
		return 1
	default:
		return 0
	}
}

func parseVTTTimestampMillis(value string) (int64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secParts := strings.Split(parts[len(parts)-1], ".")
	if len(secParts) != 2 || len(secParts[1]) != 3 {
		return 0, false
	}
	seconds, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	millis, err := strconv.ParseInt(secParts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	minutes, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
	if err != nil {
		return 0, false
	}
	hours := int64(0)
	if len(parts) == 3 {
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, false
		}
	}
	return (((hours*60)+minutes)*60+seconds)*1000 + millis, true
}

func (h *VideoHandler) ytdlpCookiesPath() string {
	if h.configManager == nil {
		return ""
	}
	settings, err := h.configManager.Load()
	if err != nil || strings.TrimSpace(settings.Cache.Directory) == "" {
		return ""
	}
	p := filepath.Join(settings.Cache.Directory, "yt-dlp-cookies.txt")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func resolveStremioLiveStreamResource(ctx context.Context, streamResourceURL, proxyURL string, selectedIndex int) (resolvedStremioStream, error) {
	parsed, err := url.Parse(streamResourceURL)
	if err != nil {
		return resolvedStremioStream{}, err
	}
	if !isStremioStreamResourceURL(parsed) {
		return resolvedStremioStream{URL: streamResourceURL, Index: -1}, nil
	}

	client, err := netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
		ResponseHeaderTimeout: defaultStreamOpenTimeout,
	}, proxyURL)
	if err != nil {
		log.Printf("[video] invalid stremio live proxy URL %q: %v", requestsecurity.URLForLog(proxyURL), err)
		client, _ = netproxy.NewHTTPClientWithOptions(netproxy.HTTPClientOptions{
			ResponseHeaderTimeout: defaultStreamOpenTimeout,
		}, "")
	}

	var resp stremioStreamResponse
	if err := getStremioJSON(ctx, client, streamResourceURL, &resp); err != nil {
		return resolvedStremioStream{}, fmt.Errorf("stremio: resolve stream: %w", err)
	}
	if stream, ok := playableStremioStream(resp.Streams, selectedIndex); ok {
		return maybeRouteStremioStreamThroughAddonRelay(ctx, client, streamResourceURL, stream), nil
	}
	return resolvedStremioStream{}, fmt.Errorf("stremio: no playable stream for %s", streamResourceURL)
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
	if !h.requireAllowedExternalPath(w, r, liveURL) {
		return
	}

	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID != "" && h.usersSvc != nil && !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		profile, ok := h.usersSvc.Get(profileID)
		if !ok || accountID == "" || profile.AccountID != accountID {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	}
	profileName := strings.TrimSpace(r.URL.Query().Get("profileName"))
	if profileName == "" && profileID != "" && h.usersSvc != nil {
		if user, ok := h.usersSvc.Get(profileID); ok {
			profileName = user.Name
		}
	}
	mediaMetadata := parseStreamMediaMetadata(r)
	target := h.resolveLiveStreamTargetForSource(profileID, r.URL.Query().Get("sourceId"))

	if target.MaxStreams > 0 {
		usage := h.buildLiveUsageSummary(target)
		if usage.AtLimit {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code":           "LIVE_STREAM_LIMIT_REACHED",
				"message":        fmt.Sprintf("%s stream limit reached (%d/%d)", strings.ToUpper(target.Provider), usage.CurrentStreams, usage.MaxStreams),
				"provider":       usage.Provider,
				"bucket":         target.BucketName,
				"currentStreams": usage.CurrentStreams,
				"maxStreams":     usage.MaxStreams,
				"available":      usage.AvailableStreams,
				"atLimit":        usage.AtLimit,
			})
			return
		}
	}

	// Determine stream format (default to "hls")
	streamFormat := target.StreamFormat
	if streamFormat == "" {
		streamFormat = "hls"
	}

	forceHLS := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "hls") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("target")), "web")
	// iOS Safari/WebKit cannot play the endless chunked MP4 that direct mode
	// produces via <video src> (fails with MEDIA_ERR_SRC_NOT_SUPPORTED). Clients
	// that explicitly request this HLS endpoint also need the managed HLS path
	// instead of the direct live proxy.
	if streamFormat == "direct" && forceHLS {
		log.Printf("[video] forcing HLS for live client (source configured direct) url=%s", requestsecurity.URLForLog(liveURL))
		streamFormat = "hls"
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Direct mode: return a proxy URL using the existing /live/stream endpoint
	if streamFormat == "direct" {
		proxyParams := url.Values{}
		proxyParams.Set("url", liveURL)
		if profileID != "" {
			proxyParams.Set("profileId", profileID)
		}
		if profileName != "" {
			proxyParams.Set("profileName", profileName)
		}
		if clientID := requestClientID(r); clientID != "" {
			proxyParams.Set("clientId", clientID)
		}
		if targetParam := strings.TrimSpace(r.URL.Query().Get("target")); targetParam != "" {
			proxyParams.Set("target", targetParam)
		}
		if streamIndex := strings.TrimSpace(r.URL.Query().Get("stremioStreamIndex")); streamIndex != "" {
			proxyParams.Set("stremioStreamIndex", streamIndex)
		}
		addStreamMediaMetadataParams(proxyParams, mediaMetadata)
		directURL := fmt.Sprintf("/live/stream?%s", proxyParams.Encode())

		log.Printf("[video] live session using direct proxy for URL: %s (provider=%s profile=%s)", requestsecurity.URLForLog(liveURL), target.Provider, profileID)

		response := map[string]interface{}{
			"streamUrl": directURL,
			"isLive":    true,
			"isDirect":  true,
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("[video] failed to encode direct live session response: %v", err)
		}
		return
	}

	// HLS mode: create a segmented HLS session
	selectedStremioStreamIndex := parseOptionalStremioStreamIndex(r.URL.Query().Get("stremioStreamIndex"))
	var stremioRequestHeaders map[string]string
	stremioHLSInput := false
	resolvedStremioIndex := -1
	var availableStremioIndexes []int
	if resolved, err := resolveStremioLiveStreamResource(r.Context(), liveURL, target.ProxyURL, selectedStremioStreamIndex); err != nil {
		log.Printf("[video] failed to resolve stremio live HLS stream %q: %v", requestsecurity.URLForLog(liveURL), err)
		http.Error(w, "failed to resolve live stream", http.StatusBadGateway)
		return
	} else {
		stremioRequestHeaders = resolved.RequestHeaders
		stremioHLSInput = resolved.IsHLS
		resolvedStremioIndex = resolved.Index
		availableStremioIndexes = resolved.AvailableIndexes
		if resolved.URL != liveURL {
			log.Printf("[video] resolved stremio live HLS stream resource: %s -> %s", requestsecurity.URLForLog(liveURL), requestsecurity.URLForLog(resolved.URL))
			liveURL = resolved.URL
		}
	}
	if !h.requireAllowedExternalPath(w, r, liveURL) {
		return
	}

	log.Printf("[video] creating live HLS session for URL: %s (provider=%s bucket=%s profile=%s)", requestsecurity.URLForLog(liveURL), target.Provider, target.BucketKey, profileID)

	tuning := LiveTuningSettings{
		ProbeSizeMB:        target.ProbeSizeMB,
		AnalyzeDurationSec: target.AnalyzeDurationSec,
		LowLatency:         target.LowLatency,
		ProxyURL:           target.ProxyURL,
		RequestHeaders:     stremioRequestHeaders,
		ForceHLSInput:      stremioHLSInput,
	}
	playbackTarget := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	clientID := requestClientID(r)
	liveCCEnabled := h.liveClosedCaptionExtractionEnabled(profileID, clientID)
	session, err := h.hlsManager.CreateLiveSession(r.Context(), liveURL, target.Provider, target.BucketKey, profileID, profileName, getClientIP(r), playbackTarget, tuning, liveCCEnabled)
	if err != nil {
		log.Printf("[video] failed to create live HLS session: %v", err)
		http.Error(w, fmt.Sprintf("failed to create live HLS session: %v", err), http.StatusInternalServerError)
		return
	}
	session.mu.Lock()
	session.MediaMetadata = mediaMetadata
	session.ClientID = clientID
	session.mu.Unlock()

	response := map[string]interface{}{
		"sessionId":   session.ID,
		"playlistUrl": fmt.Sprintf("/video/hls/%s/stream.m3u8", session.ID),
		"isLive":      true,
		// CC detection runs async — frontend polls /video/hls/{id}/cc-status
		// Include initial state (always false at creation time since detection is background)
		"hasClosedCaptions": false,
	}
	if resolvedStremioIndex >= 0 {
		response["stremioStreamIndex"] = resolvedStremioIndex
		response["stremioStreamIndexes"] = availableStremioIndexes
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[video] failed to encode live HLS session response: %v", err)
	}

	log.Printf("[video] created live HLS session %s", session.ID)
}

// GetLiveUsage returns current live stream usage and limits for the selected provider.
func (h *VideoHandler) GetLiveUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	target := h.resolveLiveStreamTargetForSource(profileID, r.URL.Query().Get("sourceId"))
	usage := h.buildLiveUsageSummary(target)
	_ = json.NewEncoder(w).Encode(usage)
}

// GetStreamUsage returns current VOD stream usage for the requesting profile's account.
func (h *VideoHandler) GetStreamUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	tracker := GetStreamTracker()
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))

	// Try per-profile limit
	if profileID != "" && h.userSettingsSvc != nil {
		if settings, err := h.userSettingsSvc.Get(profileID); err == nil && settings != nil {
			if settings.Playback.MaxConcurrentStreams != nil && *settings.Playback.MaxConcurrentStreams > 0 {
				usage := tracker.GetProfileStreamUsage(profileID, *settings.Playback.MaxConcurrentStreams)
				_ = json.NewEncoder(w).Encode(usage)
				return
			}
		}
	}

	// Try per-account limit
	if profileID != "" && h.usersSvc != nil && h.accountsSvc != nil {
		if user, ok := h.usersSvc.Get(profileID); ok {
			if account, ok := h.accountsSvc.Get(user.AccountID); ok && account.MaxStreams > 0 {
				usage := tracker.GetAccountStreamUsage(user.AccountID, account.MaxStreams)
				_ = json.NewEncoder(w).Encode(usage)
				return
			}
		}
	}

	// Try global limit
	if h.configManager != nil {
		if globalSettings, err := h.configManager.Load(); err == nil && globalSettings.Playback.MaxConcurrentStreams > 0 {
			total := tracker.CountPlaybackSlots()
			max := globalSettings.Playback.MaxConcurrentStreams
			available := max - total
			if available < 0 {
				available = 0
			}
			_ = json.NewEncoder(w).Encode(StreamUsageSummary{
				CurrentStreams:   total,
				MaxStreams:       max,
				AvailableStreams: available,
				AtLimit:          total >= max,
			})
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
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

// ServeHLSMasterPlaylist serves the cast-oriented HLS master playlist for a session.
func (h *VideoHandler) ServeHLSMasterPlaylist(w http.ResponseWriter, r *http.Request) {
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

	h.hlsManager.ServeMasterPlaylist(w, r, sessionID)
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

// ServeHLSSubtitlePlaylist serves the subtitle rendition playlist for a session.
func (h *VideoHandler) ServeHLSSubtitlePlaylist(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	trackStr := vars["track"]

	if sessionID == "" || trackStr == "" {
		http.Error(w, "missing session ID or subtitle track", http.StatusBadRequest)
		return
	}

	track, err := strconv.Atoi(trackStr)
	if err != nil || track < 0 {
		http.Error(w, "invalid subtitle track", http.StatusBadRequest)
		return
	}

	h.hlsManager.ServeSubtitlePlaylist(w, r, sessionID, track)
}

// ServeHLSSubtitleTrack serves a specific HLS subtitle sidecar track.
func (h *VideoHandler) ServeHLSSubtitleTrack(w http.ResponseWriter, r *http.Request) {
	if h.hlsManager == nil {
		http.Error(w, "HLS not enabled", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	sessionID := vars["sessionID"]
	trackStr := vars["track"]

	if sessionID == "" || trackStr == "" {
		http.Error(w, "missing session ID or subtitle track", http.StatusBadRequest)
		return
	}

	track, err := strconv.Atoi(trackStr)
	if err != nil || track < 0 {
		http.Error(w, "invalid subtitle track", http.StatusBadRequest)
		return
	}

	h.hlsManager.ServeSubtitleTrack(w, r, sessionID, track, true)
}

// ServeHLSLiveCaptions serves the WebVTT captions extracted from EIA-608 CC for a live session
func (h *VideoHandler) ServeHLSLiveCaptions(w http.ResponseWriter, r *http.Request) {
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

	h.hlsManager.ServeLiveCaptions(w, r, sessionID)
}

// GetHLSLiveCCStatus returns the CC detection status for a live session
func (h *VideoHandler) GetHLSLiveCCStatus(w http.ResponseWriter, r *http.Request) {
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

	h.hlsManager.ServeLiveCCStatus(w, r, sessionID)
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

// StopHLSSession explicitly stops and cleans up an HLS session.
// Called by the frontend when the player exits to immediately free resources.
func (h *VideoHandler) StopHLSSession(w http.ResponseWriter, r *http.Request) {
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

	log.Printf("[hls] explicit stop requested for session %s", sessionID)
	h.hlsManager.CleanupSession(sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"stopped":true}`))
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

	// Store locally for ffprobe WebDAV URL building and localhost media access.
	base := strings.TrimSpace(baseURL)
	if base != "" {
		if localParsed, err := url.Parse(base); err == nil {
			localParsed.User = nil
			localParsed.Path = ""
			localParsed.RawQuery = ""
			localParsed.Fragment = ""

			h.webdavMu.Lock()
			h.localBaseURL = strings.TrimRight(localParsed.String(), "/")
			h.webdavMu.Unlock()
		}

		if parsed, err := url.Parse(base); err == nil {
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

			log.Printf("[video] configured WebDAV access for ffprobe: base=%q prefix=%q", requestsecurity.URLForLog(h.webdavBaseURL), h.webdavPrefix)
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

func (h *VideoHandler) SetLocalBaseURL(baseURL string) {
	if h == nil {
		return
	}
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	h.webdavMu.Lock()
	h.localBaseURL = strings.TrimRight(parsed.String(), "/")
	h.webdavMu.Unlock()
}

func (h *VideoHandler) buildLocalVideoStreamURL(cleanPath string) string {
	if h == nil {
		return ""
	}
	h.webdavMu.RLock()
	base := h.localBaseURL
	h.webdavMu.RUnlock()
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u.Path = "/api/video/internal-stream"
	q := u.Query()
	q.Set("path", cleanPath)
	q.Set("transmux", "0")
	u.RawQuery = q.Encode()
	return u.String()
}

// GetHLSManager returns the HLS manager for admin/monitoring purposes.
func (h *VideoHandler) GetHLSManager() *HLSManager {
	if h == nil {
		return nil
	}
	return h.hlsManager
}

// GetStreamPoolStats returns current stream pool memory and slot stats.
func (h *VideoHandler) GetStreamPoolStats() PoolStats {
	if h == nil || h.streamPool == nil {
		return PoolStats{}
	}
	return h.streamPool.Stats()
}

func (h *VideoHandler) resolveLiveStreamTarget(profileID string) liveStreamTarget {
	return h.resolveLiveStreamTargetForSource(profileID, "")
}

func (h *VideoHandler) resolveLiveStreamTargetForSource(profileID, sourceID string) liveStreamTarget {
	if h == nil || h.configManager == nil {
		return liveStreamTarget{Provider: "m3u", MaxStreams: 0, BucketKey: "m3u:default", BucketName: "M3U shared"}
	}

	settings, err := h.configManager.Load()
	if err != nil {
		return liveStreamTarget{Provider: "m3u", MaxStreams: 0, BucketKey: "m3u:default", BucketName: "M3U shared"}
	}
	settings = config.FilterSettingsForProfile(settings, profileID)

	global := buildGlobalLiveSource(settings)
	var userSettings *models.UserSettings
	if profileID != "" && h.userSettingsSvc != nil {
		if us, userErr := h.userSettingsSvc.Get(profileID); userErr == nil && us != nil {
			userSettings = us
		}
	}
	return resolveLiveStreamTargetForSource(global, userSettings, sourceID)
}

func (h *VideoHandler) buildLiveUsageSummary(target liveStreamTarget) LiveUsageSummary {
	usage := LiveUsageSummary{
		Provider:         normalizeLiveProvider(target.Provider),
		CurrentStreams:   0,
		MaxStreams:       target.MaxStreams,
		AvailableStreams: 0,
		AtLimit:          false,
		Providers: []LiveProviderUsageEntry{
			{
				Provider:  normalizeLiveProvider(target.Provider),
				Current:   0,
				Max:       target.MaxStreams,
				Available: 0,
				AtLimit:   false,
			},
		},
	}
	if h != nil && h.hlsManager != nil {
		usage = h.hlsManager.GetLiveUsage(target.Provider, target.BucketKey, target.MaxStreams)
	}

	usage.Provider = normalizeLiveProvider(target.Provider)
	usage.MaxStreams = target.MaxStreams
	usage.CurrentStreams += h.countActiveRecordingLiveUsage(target)

	available := 0
	atLimit := false
	if target.MaxStreams > 0 {
		available = target.MaxStreams - usage.CurrentStreams
		if available < 0 {
			available = 0
		}
		atLimit = usage.CurrentStreams >= target.MaxStreams
	}

	usage.AvailableStreams = available
	usage.AtLimit = atLimit
	if len(usage.Providers) == 0 {
		usage.Providers = []LiveProviderUsageEntry{{Provider: usage.Provider}}
	}
	usage.Providers[0].Provider = usage.Provider
	usage.Providers[0].Current = usage.CurrentStreams
	usage.Providers[0].Max = target.MaxStreams
	usage.Providers[0].Available = available
	usage.Providers[0].AtLimit = atLimit
	return usage
}

func (h *VideoHandler) countActiveRecordingLiveUsage(target liveStreamTarget) int {
	if h == nil {
		return 0
	}

	targetProvider := normalizeLiveProvider(target.Provider)
	targetBucket := strings.TrimSpace(target.BucketKey)
	count := 0
	for _, recording := range liveusage.GetTracker().ListRecordings() {
		recordingTarget := h.resolveLiveStreamTarget(recording.ProfileID)
		if normalizeLiveProvider(recordingTarget.Provider) != targetProvider {
			continue
		}
		if targetBucket != "" && strings.TrimSpace(recordingTarget.BucketKey) != targetBucket {
			continue
		}
		count++
	}
	return count
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
func (h *VideoHandler) CreateHLSSession(ctx context.Context, path string, hasDV bool, dvProfile string, hasHDR bool, audioTrackIndex int, subtitleTrackIndex int, profileID string, startOffset float64, prequeueType string) (*HLSSessionResult, error) {
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

	session, err := h.hlsManager.CreateSession(ctx, path, path, hasDV, dvProfile, hasHDR, false, startOffset, 0, audioTrackIndex, subtitleTrackIndex, profileID, "", "", false, prequeueType, "", 0, "")
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

	videoTracef("[video] ProbeVideoPath: probing path=%q for HDR detection", cleanPath)

	var meta *ffprobeOutput

	// For external URLs, probe directly without requiring a stream provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		videoTracef("[video] ProbeVideoPath: external URL detected, probing directly")
		m, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				videoTracef("[video] ProbeVideoPath: ffprobe external URL failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else if h.streamer != nil {
		m, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				videoTracef("[video] ProbeVideoPath: ffprobe via provider failed for %q: %v", cleanPath, err)
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
		videoTracef("[video] ProbeVideoPath: no video stream found in %q", cleanPath)
		return result, nil
	}

	// Detect Dolby Vision
	hasDV, dvProfile, _ := detectDolbyVision(stream)
	result.HasDolbyVision = hasDV
	result.DolbyVisionProfile = dvProfile
	result.DolbyVisionConfiguration = extractDolbyVisionConfiguration(stream)

	// Detect HDR10 (PQ transfer with BT.2020)
	colorTransfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	colorPrimaries := strings.ToLower(strings.TrimSpace(stream.ColorPrimaries))
	if colorTransfer == "smpte2084" && colorPrimaries == "bt2020" {
		result.HasHDR10 = true
		videoTracef("[video] ProbeVideoPath: HDR10 detected (PQ + BT.2020)")
	}

	if result.HasDolbyVision {
		videoTracef("[video] ProbeVideoPath: Dolby Vision detected, profile=%s", result.DolbyVisionProfile)
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

	videoTracef("[video] ProbeVideoMetadata: probing path=%q for track metadata", cleanPath)

	var meta *ffprobeOutput

	// For external URLs, probe directly without requiring a stream provider
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		videoTracef("[video] ProbeVideoMetadata: external URL detected, probing directly")
		m, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				videoTracef("[video] ProbeVideoMetadata: ffprobe external URL failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else if h.streamer != nil {
		m, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				videoTracef("[video] ProbeVideoMetadata: ffprobe via provider failed for %q: %v", cleanPath, err)
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
				Codec:    strings.ToLower(strings.TrimSpace(stream.CodecName)),
				Profile:  strings.TrimSpace(stream.Profile),
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

	videoTracef("[video] ProbeVideoMetadata: found %d audio streams, %d subtitle streams", len(result.AudioStreams), len(result.SubtitleStreams))

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
			videoTracef("[video] ProbeVideoFull: using cached probe for path=%q", cleanPath)
			return h.unifiedProbeToVideoFull(cached), nil
		}
	}

	videoTracef("[video] ProbeVideoFull: probing path=%q (unified HDR + metadata)", cleanPath)

	var meta *ffprobeOutput

	// For external URLs, probe directly
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		m, err := h.runFFProbe(ctx, cleanPath, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				videoTracef("[video] ProbeVideoFull: ffprobe external URL failed for %q: %v", cleanPath, err)
			}
			return nil, err
		}
		meta = m
	} else if h.streamer != nil {
		m, err := h.runFFProbeFromProvider(ctx, cleanPath)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				videoTracef("[video] ProbeVideoFull: ffprobe via provider failed for %q: %v", cleanPath, err)
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

	plan := determineAudioPlanWithLanguage(meta, false, "")
	metadataResponse := composeMetadataResponse(meta, cleanPath, plan)
	h.setCachedMetadata(cleanPath, &metadataResponse)

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
		result.VideoPixFmt = strings.ToLower(strings.TrimSpace(stream.PixFmt))
		result.VideoProfile = strings.ToLower(strings.TrimSpace(stream.Profile))
		result.VideoWidth = stream.Width
		result.VideoHeight = stream.Height
		result.VideoLevel = stream.Level
		result.AvgFrameRate = strings.TrimSpace(stream.AvgFrameRate)

		// Detect Dolby Vision
		hasDV, dvProfile, _ := detectDolbyVision(stream)
		result.HasDolbyVision = hasDV
		result.DolbyVisionProfile = dvProfile
		result.DolbyVisionConfiguration = extractDolbyVisionConfiguration(stream)

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
				Profile:  strings.TrimSpace(s.Profile),
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
			codecName := strings.ToLower(strings.TrimSpace(s.CodecName))
			textSubtitleCodecs := map[string]bool{
				"subrip": true, "srt": true, "ass": true, "ssa": true,
				"webvtt": true, "vtt": true, "mov_text": true, "text": true,
				"ttml": true, "sami": true, "microdvd": true, "jacosub": true,
				"mpl2": true, "pjs": true, "realtext": true, "stl": true,
				"subviewer": true, "subviewer1": true, "vplayer": true,
			}
			bitmapSubtitleCodecs := map[string]bool{
				"hdmv_pgs_subtitle": true, "pgssub": true, "pgs": true,
				"dvd_subtitle": true, "dvdsub": true,
				"dvb_subtitle": true, "dvbsub": true,
				"xsub": true,
			}
			if !textSubtitleCodecs[codecName] && !bitmapSubtitleCodecs[codecName] {
				// Skip completely unknown subtitle formats
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

	videoTracef("[video] ProbeVideoFull: DV=%v HDR10=%v dvProfile=%q TrueHD=%v compatAudio=%v audioStreams=%d subStreams=%d videoCodec=%s",
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
		Duration:                 cached.Duration,
		VideoCodec:               cached.VideoCodec,
		VideoPixFmt:              cached.VideoPixFmt,
		VideoProfile:             cached.VideoProfile,
		VideoWidth:               cached.VideoWidth,
		VideoHeight:              cached.VideoHeight,
		VideoLevel:               cached.VideoLevel,
		AvgFrameRate:             cached.AvgFrameRate,
		HasDolbyVision:           cached.HasDolbyVision,
		HasHDR10:                 cached.HasHDR10,
		DolbyVisionProfile:       cached.DolbyVisionProfile,
		DolbyVisionConfiguration: cached.DolbyVisionConfiguration,
		HasTrueHD:                cached.HasTrueHD,
		HasCompatibleAudio:       cached.HasCompatibleAudio,
		AudioStreams:             make([]AudioStreamInfo, 0, len(cached.AudioStreams)),
		SubtitleStreams:          make([]SubtitleStreamInfo, 0, len(cached.SubtitleStreams)),
	}

	// Convert audio streams
	for _, as := range cached.AudioStreams {
		result.AudioStreams = append(result.AudioStreams, AudioStreamInfo{
			Index:    as.Index,
			Codec:    as.Codec,
			Profile:  as.Profile,
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
		Duration:                 result.Duration,
		VideoCodec:               result.VideoCodec,
		VideoPixFmt:              result.VideoPixFmt,
		VideoProfile:             result.VideoProfile,
		VideoWidth:               result.VideoWidth,
		VideoHeight:              result.VideoHeight,
		VideoLevel:               result.VideoLevel,
		AvgFrameRate:             result.AvgFrameRate,
		HasDolbyVision:           result.HasDolbyVision,
		HasHDR10:                 result.HasHDR10,
		DolbyVisionProfile:       result.DolbyVisionProfile,
		DolbyVisionConfiguration: result.DolbyVisionConfiguration,
		HasTrueHD:                result.HasTrueHD,
		HasCompatibleAudio:       result.HasCompatibleAudio,
		AudioStreams:             make([]audioStreamInfo, 0, len(result.AudioStreams)),
		SubtitleStreams:          make([]subtitleStreamInfo, 0, len(result.SubtitleStreams)),
	}

	// Convert audio streams
	for _, as := range result.AudioStreams {
		cached.AudioStreams = append(cached.AudioStreams, audioStreamInfo{
			Index:    as.Index,
			Codec:    as.Codec,
			Profile:  as.Profile,
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
	videoTracef("[video] proxying external URL")

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
			// Some providers (e.g., Comet) return URLs with "torrent_name" but
			// their playback endpoint expects "name". Add the alias if missing.
			if params.Has("torrent_name") && !params.Has("name") {
				params.Set("name", params.Get("torrent_name"))
			}
			// Re-encode query string properly
			cleanURL = baseURL + "?" + params.Encode()
			videoTracef("[video] external proxy: re-encoded query")
		}
	}

	// Parse the cleaned URL
	parsedURL, err := url.Parse(cleanURL)
	if err != nil {
		log.Printf("[video] URL parse failed: %v", err)
		http.Error(w, "invalid external URL", http.StatusBadRequest)
		return true, fmt.Errorf("parse external URL: %w", err)
	}
	legacyUsername := ""
	legacyPassword := ""
	legacyHasAuth := false
	if parsedURL.User != nil {
		legacyUsername = parsedURL.User.Username()
		legacyPassword, _ = parsedURL.User.Password()
		legacyHasAuth = legacyUsername != "" || legacyPassword != ""
		parsedURL.User = nil
		cleanURL = parsedURL.String()
	}

	videoTracef("[video] external proxy: final URL host=%s", parsedURL.Host)
	allowRestricted := h.configuredExternalHostPolicy()
	requestStartedAt := time.Now()
	startupExperiment := strings.TrimSpace(r.URL.Query().Get("_startupExperiment")) == "1"
	requestID := atomic.AddUint64(&h.externalProxyRequestSeq, 1)
	if err := requestsecurity.ValidateOutboundURL(r.Context(), cleanURL, allowRestricted); err != nil {
		http.Error(w, "external URL is not allowed", http.StatusBadRequest)
		return true, fmt.Errorf("validate external URL: %w", err)
	}
	validatedAt := time.Now()

	// http.Client.Timeout covers the response body as well as connection setup,
	// so a fixed timeout here would terminate healthy long-running playback.
	// Dialing remains bounded by the safe client's transport and this request
	// context cancels the upstream request when the viewer disconnects.
	ctx := r.Context()

	rangeHeader := r.Header.Get("Range")
	if startupExperiment && r.Method == http.MethodGet {
		if hit, ok := h.externalPrefixSpool.get(cleanURL, rangeHeader); ok {
			h.writeCommonHeaders(w)
			if hit.contentType != "" {
				w.Header().Set("Content-Type", hit.contentType)
			}
			if hit.etag != "" {
				w.Header().Set("ETag", hit.etag)
			}
			if hit.lastModified != "" {
				w.Header().Set("Last-Modified", hit.lastModified)
			}
			if hit.contentDisposition != "" {
				w.Header().Set("Content-Disposition", hit.contentDisposition)
			}
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(hit.data)))
			total := "*"
			if hit.totalSize > 0 {
				total = strconv.FormatInt(hit.totalSize, 10)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%s", hit.start, hit.end, total))
			w.Header().Set("X-Strmr-Startup-Spool", "hit")
			w.WriteHeader(http.StatusPartialContent)
			_, writeErr := w.Write(hit.data)
			log.Printf(
				"[video-startup] request=%d range=%q stage=prefix-spool-hit bytes=%d cachedEnd=%d total=%s elapsed=%s",
				requestID,
				rangeHeader,
				len(hit.data),
				hit.end,
				total,
				time.Since(requestStartedAt).Round(time.Millisecond),
			)
			if writeErr != nil && !isClientGone(writeErr) {
				return true, writeErr
			}
			return true, nil
		}
	}
	// Snapshot for startup diagnostics only; live opens re-check the cache.
	_, usedRedirectCache := h.cachedExternalRedirectURL(cleanURL)

	var traceMu sync.Mutex
	var dnsStartedAt time.Time
	var dnsDuration time.Duration
	var connectStartedAt time.Time
	var connectDuration time.Duration
	var tlsStartedAt time.Time
	var tlsDuration time.Duration
	var gotConnAt time.Time
	var connectionReused bool
	var firstResponseByteAt time.Time
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			traceMu.Lock()
			dnsStartedAt = time.Now()
			traceMu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			traceMu.Lock()
			if !dnsStartedAt.IsZero() {
				dnsDuration = time.Since(dnsStartedAt)
			}
			traceMu.Unlock()
		},
		ConnectStart: func(_, _ string) {
			traceMu.Lock()
			connectStartedAt = time.Now()
			traceMu.Unlock()
		},
		ConnectDone: func(_, _ string, _ error) {
			traceMu.Lock()
			if !connectStartedAt.IsZero() {
				connectDuration = time.Since(connectStartedAt)
			}
			traceMu.Unlock()
		},
		TLSHandshakeStart: func() {
			traceMu.Lock()
			tlsStartedAt = time.Now()
			traceMu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			traceMu.Lock()
			if !tlsStartedAt.IsZero() {
				tlsDuration = time.Since(tlsStartedAt)
			}
			traceMu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			traceMu.Lock()
			gotConnAt = time.Now()
			connectionReused = info.Reused
			traceMu.Unlock()
		},
		GotFirstResponseByte: func() {
			traceMu.Lock()
			firstResponseByteAt = time.Now()
			traceMu.Unlock()
		},
	}

	buildProxyRequest := func(target string, rangeValue string) (*http.Request, error) {
		proxyReq, requestErr := http.NewRequestWithContext(
			httptrace.WithClientTrace(ctx, trace),
			r.Method,
			target,
			nil,
		)
		if requestErr != nil {
			return nil, requestErr
		}
		if rangeValue != "" {
			proxyReq.Header.Set("Range", rangeValue)
		}
		proxyReq.Header.Set("User-Agent", "VLC/3.0.18 LibVLC/3.0.18")
		proxyReq.Header.Set("Accept", "*/*")
		proxyReq.Header.Set("Accept-Encoding", "identity")
		if legacyHasAuth {
			proxyReq.SetBasicAuth(legacyUsername, legacyPassword)
		} else {
			h.applyExternalUsenetWebDAVAuth(proxyReq)
		}
		return proxyReq, nil
	}

	// doExternalProxyOpen issues one upstream request, refreshing a stale cached
	// redirect once when needed. Caller owns the response body.
	doExternalProxyOpen := func(rangeValue string) (*http.Response, error) {
		target, fromCache := h.cachedExternalRedirectURL(cleanURL)
		if err := requestsecurity.ValidateOutboundURL(ctx, target, allowRestricted); err != nil {
			h.forgetExternalRedirect(cleanURL)
			target = cleanURL
			fromCache = false
		}
		proxyReq, reqErr := buildProxyRequest(target, rangeValue)
		if reqErr != nil {
			return nil, reqErr
		}
		videoTracef("[video] external proxy request: method=%s host=%s path=%s range=%q redirectCache=%t",
			proxyReq.Method, proxyReq.URL.Host, proxyReq.URL.Path, rangeValue, fromCache)

		resp, doErr := h.externalProxyHTTPClient.Do(proxyReq)
		if doErr != nil && fromCache {
			// A cached signed CDN URL may expire. Retry through the stable addon URL
			// once and refresh the redirect without surfacing a playback error.
			h.forgetExternalRedirect(cleanURL)
			fromCache = false
			proxyReq, doErr = buildProxyRequest(cleanURL, rangeValue)
			if doErr == nil {
				resp, doErr = h.externalProxyHTTPClient.Do(proxyReq)
			}
		}
		if doErr != nil {
			return nil, doErr
		}
		if fromCache && shouldForceReresolveForStatus(resp.StatusCode) {
			_ = resp.Body.Close()
			h.forgetExternalRedirect(cleanURL)
			proxyReq, doErr = buildProxyRequest(cleanURL, rangeValue)
			if doErr != nil {
				return nil, doErr
			}
			resp, doErr = h.externalProxyHTTPClient.Do(proxyReq)
			if doErr != nil {
				return nil, doErr
			}
		}
		return resp, nil
	}

	resp, err := doExternalProxyOpen(rangeHeader)
	if err != nil {
		if startupExperiment {
			log.Printf("[video-startup] request=%d range=%q failed after=%s redirectCache=%t err=%v",
				requestID, rangeHeader, time.Since(requestStartedAt).Round(time.Millisecond), usedRedirectCache, err)
		}
		log.Printf("[video] external proxy request failed: %v", err)
		http.Error(w, "failed to fetch external stream", http.StatusBadGateway)
		return true, fmt.Errorf("external request: %w", err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	headersAt := time.Now()
	finalURL := resp.Request.URL.String()
	if debrid.IsKnownPlaceholderURL(finalURL) {
		h.forgetExternalRedirect(cleanURL)
		h.invalidatePrequeuesForFailedPath(externalURL)
		if cleanURL != externalURL {
			h.invalidatePrequeuesForFailedPath(cleanURL)
		}
		http.Error(w, "external stream expired — please re-resolve", http.StatusGone)
		return true, fmt.Errorf("%w: %s", errExternalStreamPlaceholder, requestsecurity.URLForLog(finalURL))
	}
	if finalURL != cleanURL {
		h.rememberExternalRedirect(cleanURL, finalURL)
	}
	startupTimingLogged := false
	logStartupTiming := func(stage string) {
		if !startupExperiment || startupTimingLogged {
			return
		}
		startupTimingLogged = true
		traceMu.Lock()
		traceDNSDuration := dnsDuration
		traceConnectDuration := connectDuration
		traceTLSDuration := tlsDuration
		traceGotConnAt := gotConnAt
		traceConnectionReused := connectionReused
		traceFirstResponseByteAt := firstResponseByteAt
		traceMu.Unlock()
		gotConnElapsed := time.Duration(0)
		if !traceGotConnAt.IsZero() {
			gotConnElapsed = traceGotConnAt.Sub(requestStartedAt)
		}
		firstResponseByteElapsed := time.Duration(0)
		if !traceFirstResponseByteAt.IsZero() {
			firstResponseByteElapsed = traceFirstResponseByteAt.Sub(requestStartedAt)
		}
		log.Printf(
			"[video-startup] request=%d range=%q stage=%s status=%d redirectCache=%t origin=%q final=%q validate=%s gotConn=%s reused=%t dns=%s connect=%s tls=%s headers=%s firstResponseByte=%s total=%s",
			requestID,
			rangeHeader,
			stage,
			resp.StatusCode,
			usedRedirectCache,
			parsedURL.Host,
			resp.Request.URL.Host,
			validatedAt.Sub(requestStartedAt).Round(time.Millisecond),
			gotConnElapsed.Round(time.Millisecond),
			traceConnectionReused,
			traceDNSDuration.Round(time.Millisecond),
			traceConnectDuration.Round(time.Millisecond),
			traceTLSDuration.Round(time.Millisecond),
			headersAt.Sub(requestStartedAt).Round(time.Millisecond),
			firstResponseByteElapsed.Round(time.Millisecond),
			time.Since(requestStartedAt).Round(time.Millisecond),
		)
	}

	// Log response details
	contentLength := resp.Header.Get("Content-Length")
	contentRange := resp.Header.Get("Content-Range")
	acceptRanges := resp.Header.Get("Accept-Ranges")
	location := resp.Header.Get("Location")
	responseTotalSize := externalResponseTotalSize(contentRange, contentLength)
	rangeStart, rangeEnd, rangeHasEnd, rangeOK := parseSingleByteRange(rangeHeader)
	// Absolute file positions for mid-stream reconnect range requests. When the
	// client did not send a Range header, treat the body as starting at offset 0.
	clientRangeStart := int64(0)
	clientRangeEnd := int64(0)
	clientHasEnd := false
	if rangeOK {
		clientRangeStart = rangeStart
		clientRangeEnd = rangeEnd
		clientHasEnd = rangeHasEnd
	}
	spoolPrefix := startupExperiment &&
		r.Method == http.MethodGet &&
		rangeOK &&
		rangeStart == 0 &&
		(resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent)
	var spoolReadOffset int64
	videoTracef("[video] external proxy response: status=%d content-length=%s content-range=%q accept-ranges=%q location=%q",
		resp.StatusCode, contentLength, contentRange, acceptRanges, location)

	// Check for error status codes
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		log.Printf("[video] external proxy error response: %d - %s", resp.StatusCode, string(body))
		// Log all response headers for debugging
		for key, values := range resp.Header {
			log.Printf("[video] external proxy error header: %s=%v", key, values)
		}
		// A 404/410 from a pre-resolved external URL (e.g. AIOStreams "404 - Link
		// expired") means the addon refreshed and the link is dead. Drop the ready
		// prequeue/prewarm entry so the next resolve re-searches for a fresh stream
		// instead of repeatedly serving the expired link.
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			h.invalidatePrequeuesForFailedPath(cleanURL)
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

	displayName := sanitizeExternalDisplayName(r.URL.Query().Get("displayName"))
	if displayName == "" {
		displayName = sanitizeExternalDisplayName(mux.Vars(r)["displayName"])
	}
	filename := displayName
	if filename == "" {
		filename = sanitizeExternalDisplayName(resp.Header.Get("X-Filename"))
	}
	if filename == "" {
		filename = inferFilenameFromPath(externalURL)
	}
	if filename != "" {
		w.Header().Set("X-Filename", filename)
		if displayName != "" || w.Header().Get("Content-Disposition") == "" {
			w.Header().Set("Content-Disposition", buildInlineContentDisposition(filename))
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
		logStartupTiming("headers")
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
	accountID := h.resolveAccountID(r)
	streamID, bytesCounter, actCounter := tracker.StartStreamWithAccount(r, cleanURL, expectedLength, 0, 0, accountID)
	defer tracker.EndStream(streamID)
	var upstreamWatch pipelineBlockWatch
	stopUpstreamStarvationWatch := monitorPipelineStarvation(
		ctx,
		&upstreamWatch,
		pipelineStarvationTimeout,
		pipelineStarvationCheckInterval,
		func(blockedFor time.Duration) bool {
			marked := tracker.MarkPlaybackMigration(streamID, "backend-starvation")
			if marked {
				log.Printf("[stream-migration] upstream starvation detected in external proxy: host=%q path=%q blockedFor=%v streamID=%s",
					parsedURL.Host, parsedURL.Path, blockedFor.Round(time.Millisecond), streamID)
			} else {
				log.Printf("[stream-health] upstream read blocked in external proxy without playback metadata: host=%q path=%q blockedFor=%v streamID=%s",
					parsedURL.Host, parsedURL.Path, blockedFor.Round(time.Millisecond), streamID)
			}
			return true
		},
	)
	defer stopUpstreamStarvationWatch()

	// Stream the response body to the client
	buf := make([]byte, 512*1024) // 512KB buffer
	var total int64
	flusher, _ := w.(http.Flusher)
	flushCounter := 0
	const flushInterval = 1

	lastLogBytes := int64(0)
	const logInterval = 10 * 1024 * 1024 // Log every 10MB
	const throughputLogInterval = 5 * time.Second
	throughputLogAt := time.Now()
	var upstreamWindowBytes int64
	var upstreamWindowRead time.Duration
	var clientWindowBytes int64
	var clientWindowWrite time.Duration
	throughputPath := parsedURL.Path
	if throughputPath == "" {
		throughputPath = parsedURL.Host
	}

	videoTracef("[video] starting external proxy stream: host=%q streamID=%s", parsedURL.Host, streamID)

	reconnects := 0
	for {
		// Check if context is cancelled (client disconnected)
		select {
		case <-ctx.Done():
			videoTracef("[video] external proxy cancelled: host=%q total=%d reason=%v", parsedURL.Host, total, ctx.Err())
			return true, ctx.Err()
		default:
		}

		// Already delivered everything the original response promised (or the
		// finite client range requested) — treat further upstream errors as done.
		if expectedLength > 0 && total >= expectedLength {
			if flusher != nil {
				flusher.Flush()
			}
			videoTracef("[video] external proxy complete (expected length reached): host=%q total=%d", parsedURL.Host, total)
			logStartupTiming("eof")
			break
		}
		if responseTotalSize > 0 && clientRangeStart+total >= responseTotalSize {
			if flusher != nil {
				flusher.Flush()
			}
			videoTracef("[video] external proxy complete (file end): host=%q total=%d", parsedURL.Host, total)
			logStartupTiming("eof")
			break
		}

		upstreamWatch.begin()
		readStart := time.Now()
		n, readErr := resp.Body.Read(buf)
		upstreamWatch.end()
		upstreamWindowRead += time.Since(readStart)
		if n > 0 {
			upstreamWindowBytes += int64(n)
			if spoolPrefix {
				h.externalPrefixSpool.append(cleanURL, spoolReadOffset, buf[:n], responseTotalSize, resp.Header)
				spoolReadOffset += int64(n)
			}
			// Slow client writes are normal backpressure when the player has buffered
			// ahead. Do not turn them into migration signals; player buffer telemetry
			// determines whether a transient upstream stall is actually harmful.
			writeStart := time.Now()
			written, writeErr := w.Write(buf[:n])
			clientWindowWrite += time.Since(writeStart)
			if writeErr != nil {
				if isClientGone(writeErr) || ctx.Err() == context.Canceled {
					videoTracef("[video] external proxy: client disconnected host=%q total=%d", parsedURL.Host, total)
					return true, nil
				}
				log.Printf("[video] external proxy write error: host=%q total=%d err=%v", parsedURL.Host, total, writeErr)
				return true, writeErr
			}

			total += int64(written)
			if total > 0 {
				logStartupTiming("first-client-byte")
			}
			clientWindowBytes += int64(written)
			// Update stream tracking bytes and activity counters
			if bytesCounter != nil {
				atomic.StoreInt64(bytesCounter, total)
			}
			if actCounter != nil {
				atomic.StoreInt64(actCounter, time.Now().UnixNano())
			}
			flushCounter++

			// Periodic progress logging
			if total-lastLogBytes >= logInterval {
				videoTracef("[video] external proxy progress: host=%q total=%d", parsedURL.Host, total)
				lastLogBytes = total
			}

			if now := time.Now(); now.Sub(throughputLogAt) >= throughputLogInterval {
				window := now.Sub(throughputLogAt)
				if upstreamWindowBytes > 0 {
					logStreamThroughput("external-read", throughputPath, upstreamWindowBytes, upstreamWindowRead, window)
					tracker.ObserveUpstreamThroughput(streamID, upstreamWindowBytes, upstreamWindowRead)
				}
				if clientWindowBytes > 0 {
					logStreamThroughput("client-write", throughputPath, clientWindowBytes, clientWindowWrite, window)
				}
				throughputLogAt = now
				upstreamWindowBytes = 0
				upstreamWindowRead = 0
				clientWindowBytes = 0
				clientWindowWrite = 0
			}

			// Flush periodically
			if flusher != nil && flushCounter >= flushInterval {
				flusher.Flush()
				flushCounter = 0
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			// Final flush on clean EOF
			if flusher != nil {
				flusher.Flush()
			}
			videoTracef("[video] external proxy complete: host=%q total=%d", parsedURL.Host, total)
			logStartupTiming("eof")
			break
		}

		// Headers are already committed. Transport failures mid-body (idle CDN
		// kill after pause, reset) are recovered by a fresh ranged GET at the
		// next absolute file offset so external players keep the same response.
		if !isRecoverableExternalProxyReadError(readErr) || reconnects >= externalProxyMaxReconnects {
			logStartupTiming("upstream-read-error")
			log.Printf("[video] external proxy read error: host=%q total=%d reconnects=%d err=%v",
				parsedURL.Host, total, reconnects, readErr)
			return true, readErr
		}

		nextRange := externalProxyResumeRangeHeader(clientRangeStart, clientRangeEnd, clientHasEnd, total)
		if nextRange == "" {
			if flusher != nil {
				flusher.Flush()
			}
			videoTracef("[video] external proxy complete (finite range exhausted after error): host=%q total=%d err=%v",
				parsedURL.Host, total, readErr)
			logStartupTiming("eof")
			break
		}

		_ = resp.Body.Close()
		resp.Body = nil

		wantStart := clientRangeStart + total
		var reopened *http.Response
		var reopenErr error
		for {
			if reconnects >= externalProxyMaxReconnects {
				logStartupTiming("upstream-read-error")
				log.Printf("[video] external proxy reconnect budget exhausted: host=%q total=%d err=%v",
					parsedURL.Host, total, readErr)
				if reopenErr != nil {
					return true, reopenErr
				}
				return true, readErr
			}
			reconnects++
			log.Printf("[video] external proxy reconnecting after upstream read error: host=%q delivered=%d wantStart=%d reconnect=%d/%d range=%q err=%v",
				parsedURL.Host, total, wantStart, reconnects, externalProxyMaxReconnects, nextRange, readErr)

			backoff := time.Duration(reconnects) * 250 * time.Millisecond
			if backoff > 2*time.Second {
				backoff = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				videoTracef("[video] external proxy cancelled during reconnect: host=%q total=%d", parsedURL.Host, total)
				return true, ctx.Err()
			case <-time.After(backoff):
			}

			reopened, reopenErr = doExternalProxyOpen(nextRange)
			if reopenErr != nil {
				if isRecoverableExternalProxyReadError(reopenErr) {
					log.Printf("[video] external proxy reconnect open failed (will retry): host=%q range=%q err=%v",
						parsedURL.Host, nextRange, reopenErr)
					continue
				}
				logStartupTiming("upstream-read-error")
				log.Printf("[video] external proxy reconnect open failed: host=%q total=%d range=%q err=%v",
					parsedURL.Host, total, nextRange, reopenErr)
				return true, reopenErr
			}
			if reopened.StatusCode >= 400 {
				body, _ := io.ReadAll(io.LimitReader(reopened.Body, 512))
				_ = reopened.Body.Close()
				reopenErr = fmt.Errorf("external stream reconnect returned %d", reopened.StatusCode)
				// Retry transient 5xx; permanent 4xx ends the stream.
				if reopened.StatusCode >= 500 && reopened.StatusCode <= 599 {
					log.Printf("[video] external proxy reconnect status error (will retry): host=%q status=%d body=%q",
						parsedURL.Host, reopened.StatusCode, string(body))
					continue
				}
				logStartupTiming("upstream-read-error")
				log.Printf("[video] external proxy reconnect status error: host=%q total=%d status=%d body=%q",
					parsedURL.Host, total, reopened.StatusCode, string(body))
				return true, reopenErr
			}
			finalReconnectURL := ""
			if reopened.Request != nil && reopened.Request.URL != nil {
				finalReconnectURL = reopened.Request.URL.String()
			}
			if debrid.IsKnownPlaceholderURL(finalReconnectURL) {
				_ = reopened.Body.Close()
				h.forgetExternalRedirect(cleanURL)
				h.invalidatePrequeuesForFailedPath(externalURL)
				if cleanURL != externalURL {
					h.invalidatePrequeuesForFailedPath(cleanURL)
				}
				return true, fmt.Errorf("%w: %s", errExternalStreamPlaceholder, requestsecurity.URLForLog(finalReconnectURL))
			}
			if finalReconnectURL != "" && finalReconnectURL != cleanURL {
				h.rememberExternalRedirect(cleanURL, finalReconnectURL)
			}
			// Prefer Content-Range start; if missing, assume the server honoured Range.
			servedStart, hasServedStart := parseContentRangeStart(reopened.Header.Get("Content-Range"))
			if !hasServedStart {
				servedStart = wantStart
			}
			if servedStart > wantStart {
				_ = reopened.Body.Close()
				logStartupTiming("upstream-read-error")
				log.Printf("[video] external proxy reconnect offset ahead: host=%q wantStart=%d servedStart=%d",
					parsedURL.Host, wantStart, servedStart)
				return true, fmt.Errorf("external reconnect served offset %d ahead of %d", servedStart, wantStart)
			}
			if skipErr := externalProxySkipToOffset(reopened.Body, servedStart, wantStart); skipErr != nil {
				_ = reopened.Body.Close()
				reopenErr = skipErr
				if isRecoverableExternalProxyReadError(skipErr) || errors.Is(skipErr, io.EOF) {
					log.Printf("[video] external proxy reconnect skip failed (will retry): host=%q wantStart=%d err=%v",
						parsedURL.Host, wantStart, skipErr)
					continue
				}
				logStartupTiming("upstream-read-error")
				log.Printf("[video] external proxy reconnect skip failed: host=%q wantStart=%d err=%v",
					parsedURL.Host, wantStart, skipErr)
				return true, skipErr
			}
			if newSize := externalResponseTotalSize(reopened.Header.Get("Content-Range"), reopened.Header.Get("Content-Length")); newSize > 0 {
				responseTotalSize = newSize
			}
			resp = reopened
			reopenErr = nil
			break
		}
	}

	return true, nil
}

// configuredExternalHostPolicy permits private destinations only when their
// host and port are explicitly present in enabled provider configuration or
// the server's advanced private-media origin allowlist.
func (h *VideoHandler) configuredExternalHostPolicy() requestsecurity.RestrictedHostPolicy {
	return configuredProviderHostPolicy(h.configManager)
}

func (h *VideoHandler) requireAllowedExternalPath(w http.ResponseWriter, r *http.Request, source string) bool {
	source = strings.TrimSpace(source)
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return true
	}
	if err := requestsecurity.ValidateOutboundURL(r.Context(), source, h.configuredExternalHostPolicy()); err != nil {
		http.Error(w, "external media URL is not allowed", http.StatusBadRequest)
		return false
	}
	return true
}

func configuredProviderHostPolicy(configManager ConfigProvider) requestsecurity.RestrictedHostPolicy {
	allowed := make(map[string]struct{})
	addURLOrigin := func(raw string) {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed == nil {
			return
		}
		scheme := strings.ToLower(parsed.Scheme)
		if parsed.Hostname() != "" && (scheme == "http" || scheme == "https") {
			port := parsed.Port()
			if port == "" {
				switch scheme {
				case "http":
					port = "80"
				case "https":
					port = "443"
				}
			}
			if port != "" {
				allowed[privateMediaEndpointKey(parsed.Hostname(), port)] = struct{}{}
			}
		}
	}
	if configManager != nil {
		if settings, err := configManager.Load(); err == nil {
			for _, origin := range settings.Server.AllowedPrivateMediaOrigins {
				addURLOrigin(origin)
			}
			for _, engine := range settings.UsenetEngines {
				if engine.Enabled {
					addURLOrigin(engine.BaseURL)
					addURLOrigin(engine.WebDAVBaseURL)
				}
			}
			for _, indexer := range settings.Indexers {
				if indexer.Enabled {
					addURLOrigin(indexer.URL)
				}
			}
			for _, scraper := range settings.TorrentScrapers {
				if scraper.Enabled {
					addURLOrigin(scraper.URL)
				}
			}
			addURLOrigin(settings.Live.PlaylistURL)
			addURLOrigin(settings.Live.ManifestURL)
			addURLOrigin(settings.Live.XtreamHost)
			for _, source := range append(settings.Live.Sources, settings.Live.PlaylistSources...) {
				if source.Enabled == nil || *source.Enabled {
					addURLOrigin(source.PlaylistURL)
					addURLOrigin(source.ManifestURL)
					addURLOrigin(source.XtreamHost)
				}
			}
		}
	}
	return func(hostname, port string) bool {
		_, ok := allowed[privateMediaEndpointKey(hostname, port)]
		return ok
	}
}

func privateMediaEndpointKey(hostname, port string) string {
	hostname = strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(hostname), "[]"), "."))
	return net.JoinHostPort(hostname, strings.TrimSpace(port))
}

func (h *VideoHandler) applyExternalUsenetWebDAVAuth(req *http.Request) {
	if h == nil || h.configManager == nil || req == nil || req.URL == nil {
		return
	}
	settings, err := h.configManager.Load()
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

func (h *VideoHandler) externalUsenetWebDAVAuthHeader(rawURL string) string {
	if h == nil || h.configManager == nil {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	settings, err := h.configManager.Load()
	if err != nil {
		return ""
	}
	for _, engine := range settings.UsenetEngines {
		if !engine.Enabled || strings.TrimSpace(engine.WebDAVBaseURL) == "" {
			continue
		}
		if !externalURLMatchesBase(parsed, engine.WebDAVBaseURL) {
			continue
		}
		if engine.WebDAVUsername == "" && engine.WebDAVPassword == "" {
			return ""
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(engine.WebDAVUsername + ":" + engine.WebDAVPassword))
		return "Authorization: Basic " + encoded + "\r\n"
	}
	return ""
}

func externalURLMatchesBase(candidate *url.URL, baseRaw string) bool {
	if candidate == nil {
		return false
	}
	base, err := url.Parse(strings.TrimSpace(baseRaw))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return false
	}
	if !strings.EqualFold(candidate.Scheme, base.Scheme) || !strings.EqualFold(candidate.Host, base.Host) {
		return false
	}
	basePath := strings.TrimRight(base.EscapedPath(), "/")
	if basePath == "" {
		return true
	}
	candidatePath := strings.TrimRight(candidate.EscapedPath(), "/")
	return candidatePath == basePath || strings.HasPrefix(candidatePath+"/", basePath+"/")
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
	if !h.requireLibraryStreamAccess(w, r, path) {
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
		if errors.Is(err, streaming.ErrStaleTorrent) {
			log.Printf("[video] GetDirectURL: stale torrent for path=%q", path)
			http.Error(w, "debrid torrent expired or deleted — please re-resolve", http.StatusGone)
			return
		}
		log.Printf("[video] GetDirectURL error for path=%q: %v", path, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	videoTracef("[video] GetDirectURL: path=%q resolved direct URL", path)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": directURL,
	})
}

// liveClosedCaptionExtractionEnabled resolves playback.liveClosedCaptionExtraction
// with global → profile → client precedence. Default is enabled.
func (h *VideoHandler) liveClosedCaptionExtractionEnabled(profileID, clientID string) bool {
	enabled := true

	if h.configManager != nil {
		if globalSettings, err := h.configManager.Load(); err == nil {
			enabled = globalSettings.Playback.LiveClosedCaptionExtraction
		}
	}

	if h.userSettingsSvc != nil && profileID != "" {
		if userSettings, err := h.userSettingsSvc.Get(profileID); err == nil && userSettings != nil {
			if userSettings.Playback.LiveClosedCaptionExtraction != nil {
				enabled = *userSettings.Playback.LiveClosedCaptionExtraction
			}
		}
	}

	if h.clientSettingsSvc != nil && clientID != "" && profileID != "" {
		if clientSettings, err := h.clientSettingsSvc.Get(clientID, profileID); err == nil && clientSettings != nil {
			if clientSettings.LiveClosedCaptionExtraction != nil {
				enabled = *clientSettings.LiveClosedCaptionExtraction
			}
		}
	}

	return enabled
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
	if h.clientSettingsSvc != nil && clientID != "" && userID != "" {
		clientSettings, err := h.clientSettingsSvc.Get(clientID, userID)
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
				videoTracef("[video] ProbeVideo: DV profile 5 incompatible with 'hdr' policy (no HDR fallback)")
				return true, vs.DolbyVisionProfile
			}
		}
	}
	return false, ""
}

// ===================== Cropdetect endpoint =====================

type cropDetectResponse struct {
	LetterboxTop    float64 `json:"letterboxTop"`
	LetterboxBottom float64 `json:"letterboxBottom"`
}

var cropdetectRegex = regexp.MustCompile(`crop=(\d+):(\d+):(\d+):(\d+)`)

// CropDetect runs ffmpeg cropdetect on a video to detect letterbox bars.
func (h *VideoHandler) CropDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		h.writeCommonHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusOK)
		return
	}

	h.writeCommonHeaders(w)

	filePath := strings.TrimSpace(resolveScopedPlaybackPath(r, r.URL.Query().Get("path")))
	if filePath == "" {
		http.Error(w, "Missing path parameter", http.StatusBadRequest)
		return
	}
	if !h.requireAllowedExternalPath(w, r, filePath) {
		return
	}

	cleanPath := cleanCropDetectPath(filePath)
	if !h.requireLibraryStreamAccess(w, r, cleanPath) {
		return
	}

	// Resolve a seekable URL for the file
	probeURL, err := h.resolveSeekableURL(r.Context(), cleanPath)
	if err != nil || probeURL == "" {
		log.Printf("[cropdetect] no seekable URL for %q: %v", cleanPath, err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cropDetectResponse{})
		return
	}

	// Get metadata for duration, video dimensions, and HDR status
	var duration float64
	var videoHeight int
	var isHDR bool

	if cached := h.getCachedMetadata(cleanPath); cached != nil {
		duration = cached.DurationSeconds
		if len(cached.VideoStreams) > 0 {
			videoHeight = cached.VideoStreams[0].Height
			ct := strings.ToLower(cached.VideoStreams[0].ColorTransfer)
			isHDR = ct == "smpte2084" || ct == "arib-std-b67"
		}
	}

	// If no cached metadata, run a quick ffprobe
	if duration <= 0 || videoHeight <= 0 {
		probeCtx, probeCancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer probeCancel()
		meta, err := h.runFFProbe(probeCtx, probeURL, nil)
		if err != nil {
			log.Printf("[cropdetect] ffprobe failed for %q: %v", cleanPath, err)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cropDetectResponse{})
			return
		}
		if meta.Format.Duration != "" {
			duration, _ = strconv.ParseFloat(meta.Format.Duration, 64)
		}
		for _, s := range meta.Streams {
			if strings.ToLower(s.CodecType) == "video" {
				videoHeight = s.Height
				ct := strings.ToLower(s.ColorTransfer)
				isHDR = ct == "smpte2084" || ct == "arib-std-b67"
				break
			}
		}
	}

	if duration <= 0 || videoHeight <= 0 {
		log.Printf("[cropdetect] invalid duration (%.1f) or height (%d) for %q", duration, videoHeight, cleanPath)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cropDetectResponse{})
		return
	}

	log.Printf("[cropdetect] probing %q: duration=%.0f height=%d isHDR=%v", cleanPath, duration, videoHeight, isHDR)

	// Sample at 5 minutes and 30 minutes (or 50% if video is shorter than 60 min)
	var sampleTimes []float64
	if duration > 3600 {
		sampleTimes = []float64{300, 1800}
	} else if duration > 600 {
		sampleTimes = []float64{300, duration * 0.5}
	} else {
		sampleTimes = []float64{duration * 0.25, duration * 0.5}
	}
	type cropResult struct {
		cropW, cropH, cropX, cropY int
	}

	results := make([]cropResult, len(sampleTimes))
	sampleErrors := int32(0)

	// Run all samples in parallel — HTTP seek latency dominates, so parallel cuts wall time in half
	var wg sync.WaitGroup
	for i, seekTime := range sampleTimes {
		wg.Add(1)
		go func(i int, seekTime float64) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()

			// HDR content has near-black letterbox bars (pixel values ~80-100) that
			// the standard threshold=24 misses entirely. Use 100 for HDR.
			cropThreshold := 24
			if isHDR {
				cropThreshold = 100
			}
			args := []string{
				"-ss", fmt.Sprintf("%.2f", seekTime),
			}
			if header := h.externalUsenetWebDAVAuthHeader(probeURL); header != "" {
				args = append(args, "-headers", header)
			}
			args = append(args,
				"-i", probeURL,
				"-an",
				"-vframes", "5",
				"-vf", fmt.Sprintf("cropdetect=%d:16:0", cropThreshold),
				"-f", "null",
				"-",
			)
			cmd := exec.CommandContext(ctx, h.ffmpegPath, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				atomic.AddInt32(&sampleErrors, 1)
				stderrText := strings.TrimSpace(stderr.String())
				if len(stderrText) > 1200 {
					stderrText = stderrText[len(stderrText)-1200:]
				}
				log.Printf("[cropdetect] ffmpeg failed at %.1fs for %q: %v stderr_tail=%q", seekTime, cleanPath, err, stderrText)
				return
			}

			// Parse the last crop= line from stderr
			stderrText := stderr.String()
			lines := strings.Split(stderrText, "\n")
			for j := len(lines) - 1; j >= 0; j-- {
				if m := cropdetectRegex.FindStringSubmatch(lines[j]); m != nil {
					cw, _ := strconv.Atoi(m[1])
					ch, _ := strconv.Atoi(m[2])
					cx, _ := strconv.Atoi(m[3])
					cy, _ := strconv.Atoi(m[4])
					results[i] = cropResult{cw, ch, cx, cy}
					return
				}
			}
			atomic.AddInt32(&sampleErrors, 1)
			stderrTail := strings.TrimSpace(stderrText)
			if len(stderrTail) > 1200 {
				stderrTail = stderrTail[len(stderrTail)-1200:]
			}
			log.Printf("[cropdetect] no crop line at %.1fs for %q stderr_tail=%q", seekTime, cleanPath, stderrTail)
		}(i, seekTime)
	}
	wg.Wait()

	if int(sampleErrors) >= len(sampleTimes) {
		log.Printf("[cropdetect] all samples failed for %q", cleanPath)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cropDetectResponse{})
		return
	}

	// Collect valid top/bottom fractions
	var topFractions, bottomFractions []float64
	for _, cr := range results {
		if cr.cropH <= 0 || cr.cropH > videoHeight {
			continue
		}
		// Discard outliers: content height < 50% of frame
		if cr.cropH < videoHeight/2 {
			continue
		}
		topFrac := float64(cr.cropY) / float64(videoHeight)
		bottomFrac := float64(videoHeight-cr.cropY-cr.cropH) / float64(videoHeight)
		topFractions = append(topFractions, topFrac)
		bottomFractions = append(bottomFractions, bottomFrac)
	}

	var resp cropDetectResponse
	if len(topFractions) > 0 {
		sort.Float64s(topFractions)
		sort.Float64s(bottomFractions)
		medianTop := topFractions[len(topFractions)/2]

		medianBottom := bottomFractions[len(bottomFractions)/2]

		// Asymmetry check: if top and bottom differ by more than 3%, discard both
		if math.Abs(medianTop-medianBottom) > 0.03 {
			medianTop = 0
			medianBottom = 0
		}

		resp.LetterboxTop = math.Round(medianTop*1000) / 1000
		resp.LetterboxBottom = math.Round(medianBottom*1000) / 1000
	}

	log.Printf("[cropdetect] result for %q: top=%.3f bottom=%.3f (from %d samples)", cleanPath, resp.LetterboxTop, resp.LetterboxBottom, len(topFractions))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func cleanCropDetectPath(filePath string) string {
	if strings.HasPrefix(filePath, "/webdav/") {
		return strings.TrimPrefix(filePath, "/webdav")
	}
	if strings.HasPrefix(filePath, "webdav/") {
		return "/" + strings.TrimPrefix(filePath, "webdav/")
	}
	return filePath
}

// resolveSeekableURL returns a direct/WebDAV URL suitable for ffmpeg seeking.
// Returns empty string if no seekable URL is available.
func (h *VideoHandler) resolveSeekableURL(ctx context.Context, cleanPath string) (string, error) {
	// External URLs are already seekable
	if strings.HasPrefix(cleanPath, "http://") || strings.HasPrefix(cleanPath, "https://") {
		return cleanPath, nil
	}

	// Try direct URL (debrid CDN)
	if directProvider, ok := h.streamer.(streaming.DirectURLProvider); ok {
		directURL, err := directProvider.GetDirectURL(ctx, cleanPath)
		if err == nil && directURL != "" {
			return directURL, nil
		}
	}

	// Try WebDAV URL (usenet)

	if webdavURL := h.buildWebDAVURL(cleanPath); webdavURL != "" {
		return webdavURL, nil
	}

	return "", fmt.Errorf("no seekable URL available")
}

func (h *VideoHandler) SetCastCapabilities(store *castcaps.Store) {
	if h == nil {
		return
	}
	h.castCaps = store
	if h.hlsManager != nil {
		h.hlsManager.SetCastCapabilities(store)
	}
}

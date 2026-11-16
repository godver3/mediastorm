package debrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/mediaresolve"
	"novastream/internal/streamheaders"
	"novastream/internal/torboxrate"
	"novastream/models"
	"novastream/utils"
)

// trackCacheEntry stores cached track probe results
type trackCacheEntry struct {
	audioTracks    []AudioTrackInfo
	subtitleTracks []SubtitleTrackInfo
	probeError     string
	expiresAt      time.Time
}

type preResolvedHealthCacheEntry struct {
	health    DebridHealthCheck
	expiresAt time.Time
}

const preResolvedPositiveHealthTTL = 2 * time.Minute

// HealthService checks debrid item health by verifying cached status.
type HealthService struct {
	cfg         *config.Manager
	ffprobePath string
	fullProber  PreResolvedFullProber
	// Track cache keyed by info hash
	trackCache   map[string]*trackCacheEntry
	trackCacheMu sync.RWMutex
	// Short-lived cache for pre-resolved scraper streams. AIO-style playback URLs
	// can rotate per request and rate-limit when probed repeatedly.
	preResolvedHealthCache   map[string]*preResolvedHealthCacheEntry
	preResolvedHealthCacheMu sync.RWMutex
	// Track which hashes are currently being probed
	probing   map[string]bool
	probingMu sync.Mutex
	// Track torrents currently in use by active playback sessions.
	// Key: "provider:torrentID", value: true.
	// Health checks skip deletion for torrents in this set.
	activeTorrents   map[string]bool
	activeTorrentsMu sync.Mutex
}

// PreResolvedFullProber supplies the same full media probe consumed by prequeue.
type PreResolvedFullProber interface {
	ProbeVideoFull(ctx context.Context, path string) (*models.VideoFullResult, error)
}

// NewHealthService creates a new debrid health check service.
func NewHealthService(cfg *config.Manager) *HealthService {
	return &HealthService{
		cfg:                    cfg,
		trackCache:             make(map[string]*trackCacheEntry),
		preResolvedHealthCache: make(map[string]*preResolvedHealthCacheEntry),
		probing:                make(map[string]bool),
		activeTorrents:         make(map[string]bool),
	}
}

// SetFFProbePath sets the ffprobe path for probing pre-resolved streams.
func (s *HealthService) SetFFProbePath(path string) {
	s.ffprobePath = path
}

// SetFullProber lets pre-resolved health verification produce reusable playback metadata.
func (s *HealthService) SetFullProber(prober PreResolvedFullProber) {
	s.fullProber = prober
}

// MarkTorrentActive registers a torrent as in-use by playback so that
// concurrent health checks will not delete it.
func (s *HealthService) MarkTorrentActive(provider, torrentID string) {
	key := provider + ":" + torrentID
	s.activeTorrentsMu.Lock()
	s.activeTorrents[key] = true
	s.activeTorrentsMu.Unlock()
	log.Printf("[debrid-health] torrent %s marked active for %s", torrentID, provider)
}

// MarkTorrentInactive removes a torrent from the active set, allowing
// future health checks to clean it up normally.
func (s *HealthService) MarkTorrentInactive(provider, torrentID string) {
	key := provider + ":" + torrentID
	s.activeTorrentsMu.Lock()
	delete(s.activeTorrents, key)
	s.activeTorrentsMu.Unlock()
	log.Printf("[debrid-health] torrent %s marked inactive for %s", torrentID, provider)
}

// isTorrentActive checks whether any torrent for the given provider is active
// for playback. Since TorBox reuses torrent IDs for the same hash, we need to
// check by provider name — the health checker's torrentID may differ from the
// playback torrentID even though they reference the same underlying content.
func (s *HealthService) isTorrentActive(provider, torrentID string) bool {
	key := provider + ":" + torrentID
	s.activeTorrentsMu.Lock()
	active := s.activeTorrents[key]
	s.activeTorrentsMu.Unlock()
	return active
}

// deleteHealthCheckTorrent removes temporary health-check state without
// disrupting playback when a provider reuses an existing torrent ID.
func (s *HealthService) deleteHealthCheckTorrent(ctx context.Context, client Provider, provider, torrentID string) {
	if s.isTorrentActive(provider, torrentID) {
		log.Printf("[debrid-health] skipping delete of torrent %s — active playback session on %s", torrentID, provider)
		return
	}
	if err := client.DeleteTorrent(ctx, torrentID); err != nil {
		log.Printf("[debrid-health] warning: failed to delete torrent %s from %s: %v", torrentID, provider, err)
	}
}

func preResolvedHealthCacheKey(result models.NZBResult) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(result.Attributes["scraper"])),
		strings.ToLower(strings.TrimSpace(result.Attributes["tracker"])),
		strings.ToLower(strings.TrimSpace(result.Attributes["raw_title"])),
		strings.ToLower(strings.TrimSpace(result.Title)),
	}
	for i, part := range parts {
		parts[i] = strings.Join(strings.Fields(part), " ")
	}
	return strings.Join(parts, "|")
}

func cloneDebridHealthCheck(in DebridHealthCheck) DebridHealthCheck {
	out := in
	if in.AudioTracks != nil {
		out.AudioTracks = append([]AudioTrackInfo(nil), in.AudioTracks...)
	}
	if in.SubtitleTracks != nil {
		out.SubtitleTracks = append([]SubtitleTrackInfo(nil), in.SubtitleTracks...)
	}
	out.MediaProbe = cloneVideoFullResult(in.MediaProbe)
	return out
}

func cloneVideoFullResult(in *models.VideoFullResult) *models.VideoFullResult {
	if in == nil {
		return nil
	}
	out := *in
	out.AudioStreams = append([]models.AudioStreamInfo(nil), in.AudioStreams...)
	out.SubtitleStreams = append([]models.SubtitleStreamInfo(nil), in.SubtitleStreams...)
	if in.DolbyVisionConfiguration != nil {
		configuration := *in.DolbyVisionConfiguration
		out.DolbyVisionConfiguration = &configuration
	}
	return &out
}

func (s *HealthService) cachedPreResolvedHealth(key string) (*DebridHealthCheck, bool) {
	if s == nil || key == "" {
		return nil, false
	}
	s.preResolvedHealthCacheMu.RLock()
	entry := s.preResolvedHealthCache[key]
	s.preResolvedHealthCacheMu.RUnlock()
	if entry == nil {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		s.preResolvedHealthCacheMu.Lock()
		if current := s.preResolvedHealthCache[key]; current == entry {
			delete(s.preResolvedHealthCache, key)
		}
		s.preResolvedHealthCacheMu.Unlock()
		return nil, false
	}
	health := cloneDebridHealthCheck(entry.health)
	return &health, true
}

func (s *HealthService) rememberPreResolvedHealth(key string, health *DebridHealthCheck) {
	if s == nil || key == "" || health == nil || !health.Healthy || !health.Cached {
		return
	}
	entry := &preResolvedHealthCacheEntry{
		health:    cloneDebridHealthCheck(*health),
		expiresAt: time.Now().Add(preResolvedPositiveHealthTTL),
	}
	s.preResolvedHealthCacheMu.Lock()
	s.preResolvedHealthCache[key] = entry
	s.preResolvedHealthCacheMu.Unlock()
}

func appendProbeNote(existing, note string) string {
	existing = strings.TrimSpace(existing)
	note = strings.TrimSpace(note)
	if existing == "" {
		return note
	}
	if note == "" {
		return existing
	}
	return existing + "; " + note
}

// DebridHealthCheck represents the health status of a debrid item.
type DebridHealthCheck struct {
	Healthy      bool   `json:"healthy"`
	Status       string `json:"status"`
	Cached       bool   `json:"cached"`
	Provider     string `json:"provider"`
	InfoHash     string `json:"infoHash,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	// Track info (populated when cached)
	AudioTracks     []AudioTrackInfo        `json:"audioTracks,omitempty"`
	SubtitleTracks  []SubtitleTrackInfo     `json:"subtitleTracks,omitempty"`
	TrackProbeError string                  `json:"trackProbeError,omitempty"`
	TracksLoading   bool                    `json:"tracksLoading,omitempty"`
	MediaProbe      *models.VideoFullResult `json:"-"`
}

// AudioTrackInfo contains metadata for an audio track.
type AudioTrackInfo struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	Title    string `json:"title,omitempty"`
}

// SubtitleTrackInfo contains metadata for a subtitle track.
type SubtitleTrackInfo struct {
	Index      int    `json:"index"`
	Language   string `json:"language"`
	Codec      string `json:"codec"`
	Title      string `json:"title,omitempty"`
	Forced     bool   `json:"forced"`
	IsBitmap   bool   `json:"isBitmap"`
	BitmapType string `json:"bitmapType,omitempty"`
}

// CheckHealth verifies if a debrid result is healthy (cached and available).
// For Real-Debrid, this checks instant availability.
// For uncached items, we optionally add+check+remove to verify.
func (s *HealthService) CheckHealth(ctx context.Context, result models.NZBResult, verifyUncached bool) (*DebridHealthCheck, error) {
	return s.checkHealth(ctx, result, verifyUncached, false)
}

// CheckQuickCacheOnly checks cache state only when it can be done without adding
// or removing torrents. It returns status=skipped when no safe quick path exists.
func (s *HealthService) CheckQuickCacheOnly(ctx context.Context, result models.NZBResult) (*DebridHealthCheck, error) {
	return s.checkHealth(ctx, result, false, true)
}

// CheckQuickCacheOnlyBulk checks cache state for multiple results without adding
// or removing torrents. Duplicate info hashes are checked once.
func (s *HealthService) CheckQuickCacheOnlyBulk(ctx context.Context, results []models.NZBResult) ([]*DebridHealthCheck, error) {
	out := make([]*DebridHealthCheck, len(results))
	if len(results) == 0 {
		return out, nil
	}
	if s.cfg == nil {
		return nil, fmt.Errorf("health service not configured")
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if out, ok := s.checkQuickCacheAcrossProvidersBulk(ctx, settings, results); ok {
		return out, nil
	}

	type workItem struct {
		index int
		key   string
	}

	byKey := make(map[string][]int)
	var ordered []workItem
	for i, result := range results {
		key := quickCacheDedupKey(result)
		if key == "" {
			ordered = append(ordered, workItem{index: i})
			continue
		}
		if _, ok := byKey[key]; !ok {
			ordered = append(ordered, workItem{index: i, key: key})
		}
		byKey[key] = append(byKey[key], i)
	}

	var mu sync.Mutex
	queue := make(chan workItem)
	workerCount := 4
	if len(ordered) < workerCount {
		workerCount = len(ordered)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				health, err := s.CheckQuickCacheOnly(ctx, results[item.index])
				if err != nil {
					health = &DebridHealthCheck{
						Healthy:      false,
						Status:       "error",
						Cached:       false,
						ErrorMessage: err.Error(),
					}
				}
				mu.Lock()
				if item.key != "" {
					for _, idx := range byKey[item.key] {
						copied := cloneDebridHealthCheck(*health)
						out[idx] = &copied
					}
				} else {
					out[item.index] = health
				}
				mu.Unlock()
			}
		}()
	}

	for _, item := range ordered {
		select {
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return out, ctx.Err()
		case queue <- item:
		}
	}
	close(queue)
	wg.Wait()

	for i := range out {
		if out[i] == nil {
			out[i] = &DebridHealthCheck{
				Healthy:      false,
				Status:       "skipped",
				Cached:       false,
				ErrorMessage: "quick cache check did not run",
			}
		}
	}
	return out, nil
}

func (s *HealthService) checkQuickCacheAcrossProvidersBulk(ctx context.Context, settings config.Settings, results []models.NZBResult) ([]*DebridHealthCheck, bool) {
	out := make([]*DebridHealthCheck, len(results))
	hashIndexes := make(map[string][]int)
	hashes := make([]string, 0, len(results))
	for i, result := range results {
		infoHash := quickCacheInfoHash(result)
		if result.Attributes["preresolved"] == "true" {
			out[i] = &DebridHealthCheck{
				Healthy:      false,
				Status:       "skipped",
				Cached:       false,
				Provider:     result.Attributes["tracker"],
				ErrorMessage: "quick cache check unavailable for pre-resolved streams",
			}
			continue
		}
		if infoHash == "" {
			out[i] = &DebridHealthCheck{
				Healthy:      false,
				Status:       "skipped",
				Cached:       false,
				ErrorMessage: "quick cache check requires an info hash",
			}
			continue
		}
		if _, ok := hashIndexes[infoHash]; !ok {
			hashes = append(hashes, infoHash)
		}
		hashIndexes[infoHash] = append(hashIndexes[infoHash], i)
	}

	if len(hashes) == 0 {
		return out, true
	}

	type quickProvider struct {
		name   string
		client Provider
	}
	providers := make([]quickProvider, 0, len(settings.Streaming.DebridProviders))
	for i := range settings.Streaming.DebridProviders {
		providerConfig := &settings.Streaming.DebridProviders[i]
		if !providerConfig.Enabled || strings.TrimSpace(providerConfig.APIKey) == "" {
			continue
		}
		client, ok := GetProvider(strings.ToLower(providerConfig.Provider), providerConfig.APIKey)
		if !ok {
			continue
		}
		if configurable, ok := client.(Configurable); ok && providerConfig.Config != nil {
			configurable.Configure(providerConfig.Config)
		}
		providers = append(providers, quickProvider{name: client.Name(), client: client})
	}
	if len(providers) == 0 {
		return nil, false
	}

	type providerResult struct {
		name   string
		cached map[string]bool
		errors map[string]error
	}
	resultCh := make(chan providerResult, len(providers))
	for _, provider := range providers {
		go func(provider quickProvider) {
			result := providerResult{name: provider.name, cached: make(map[string]bool), errors: make(map[string]error)}
			if bulkClient, ok := provider.client.(InstantAvailabilityBulkProvider); ok {
				cached, err := bulkClient.CheckInstantAvailabilityBulk(ctx, hashes)
				if err != nil {
					for _, hash := range hashes {
						result.errors[hash] = err
					}
				} else {
					result.cached = cached
				}
			} else {
				for i, hash := range hashes {
					cached, err := provider.client.CheckInstantAvailability(ctx, hash)
					if err != nil {
						result.errors[hash] = err
						// Every remaining hash would fail the same way: stop instead of
						// spending the caller's budget on calls that cannot succeed.
						if errors.Is(err, ErrInstantAvailabilityUnsupported) || ctx.Err() != nil {
							for _, rest := range hashes[i+1:] {
								result.errors[rest] = err
							}
							break
						}
						continue
					}
					result.cached[hash] = cached
				}
			}
			// A provider without a live lookup still knows the hashes it recently
			// reported as not cached; report those as a definite miss so ranking
			// and "not cached on any provider" stay accurate without an upload.
			for hash := range result.errors {
				if IsRecentlyUncached(provider.name, hash) {
					delete(result.errors, hash)
					result.cached[hash] = false
				}
			}
			logQuickCacheProviderErrors(provider.name, len(hashes), result.errors)
			resultCh <- result
		}(provider)
	}

	winner := make(map[string]string, len(hashes))
	successes := make(map[string]int, len(hashes))
	errorsByHash := make(map[string][]string, len(hashes))
	for range providers {
		providerResult := <-resultCh
		for _, hash := range hashes {
			if err := providerResult.errors[hash]; err != nil {
				errorsByHash[hash] = append(errorsByHash[hash], fmt.Sprintf("%s: %v", providerResult.name, err))
				continue
			}
			successes[hash]++
			if providerResult.cached[strings.ToLower(hash)] && winner[hash] == "" {
				winner[hash] = providerResult.name
			}
		}
	}

	for _, hash := range hashes {
		cachedProvider := winner[hash]
		cached := cachedProvider != ""
		status := "cached"
		errorMessage := ""
		providerName := cachedProvider
		if !cached && successes[hash] == len(providers) {
			status = "not_cached"
			providerName = "all"
		} else if !cached {
			status = "skipped"
			providerName = "multiple"
			errorMessage = "cache status unknown on one or more providers: " + strings.Join(errorsByHash[hash], "; ")
		}
		log.Printf("[debrid-health] multi-provider quick cache check hash=%s cached=%t provider=%s successes=%d/%d", hash, cached, providerName, successes[hash], len(providers))
		for _, index := range hashIndexes[hash] {
			out[index] = &DebridHealthCheck{
				Healthy:      cached,
				Status:       status,
				Cached:       cached,
				Provider:     providerName,
				InfoHash:     hash,
				ErrorMessage: errorMessage,
			}
		}
	}

	return out, true
}

// quickCacheUnsupportedLogged records providers whose missing cache lookup has
// already been logged, so the permanent condition is reported once per process.
var quickCacheUnsupportedLogged sync.Map

// logQuickCacheProviderErrors summarizes one provider's failed quick cache
// lookups. Without it a provider that never answers only shows up as a
// successes=1/2 count on each hash.
func logQuickCacheProviderErrors(provider string, total int, errs map[string]error) {
	var sample error
	for _, err := range errs {
		sample = err
		break
	}
	if sample == nil {
		return
	}
	if errors.Is(sample, ErrInstantAvailabilityUnsupported) {
		if _, seen := quickCacheUnsupportedLogged.LoadOrStore(provider, true); !seen {
			log.Printf("[debrid-health] %s has no quick cache lookup; its cache status stays unknown in quick checks", provider)
		}
		return
	}
	log.Printf("[debrid-health] %s quick cache check failed for %d/%d hash(es): %v", provider, len(errs), total, sample)
}

func (s *HealthService) checkHealth(ctx context.Context, result models.NZBResult, verifyUncached, quickOnly bool) (*DebridHealthCheck, error) {
	if s.cfg == nil {
		return nil, fmt.Errorf("health service not configured")
	}
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	healthCheckTimeout := debridHealthCheckTimeout(settings)

	if quickOnly && result.Attributes["preresolved"] == "true" {
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "skipped",
			Cached:       false,
			Provider:     result.Attributes["tracker"],
			ErrorMessage: "quick cache check unavailable for pre-resolved streams",
		}, nil
	}

	// Check if this is a pre-resolved stream (e.g., from AIOStreams)
	// Pre-resolved streams need to be probed to check if they're actually cached
	if result.Attributes["preresolved"] == "true" {
		streamURL := result.Attributes["stream_url"]
		if streamURL == "" {
			streamURL = result.Link
		}
		requestURL, requestHeaders := streamheaders.Extract(streamURL)
		preResolvedCacheKey := preResolvedHealthCacheKey(result)
		if rawName := strings.TrimSpace(result.Attributes["raw_name"]); rawName != "" {
			log.Printf("[debrid-health] pre-resolved raw display: title=%q raw_name=%q raw_title=%q tracker=%q scraper=%q",
				result.Title,
				rawName,
				strings.TrimSpace(result.Attributes["raw_title"]),
				strings.TrimSpace(result.Attributes["tracker"]),
				strings.TrimSpace(result.Attributes["scraper"]),
			)
		} else {
			log.Printf("[debrid-health] pre-resolved raw display: title=%q raw_title=%q tracker=%q scraper=%q",
				result.Title,
				strings.TrimSpace(result.Attributes["raw_title"]),
				strings.TrimSpace(result.Attributes["tracker"]),
				strings.TrimSpace(result.Attributes["scraper"]),
			)
		}
		if IsKnownPlaceholderURL(streamURL) {
			log.Printf("[debrid-health] pre-resolved stream %s is a known placeholder URL", result.Title)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "not_cached",
				Cached:       false,
				Provider:     result.Attributes["tracker"],
				ErrorMessage: "stream points to a known placeholder URL",
			}, nil
		}
		log.Printf("[debrid-health] checking pre-resolved stream: %s", result.Title)

		// First, do a quick HEAD request to check if the URL is accessible
		// This catches 404s and other HTTP errors quickly without waiting for ffprobe
		if streamURL != "" {
			headCtx, headCancel := context.WithTimeout(ctx, healthCheckTimeout)
			defer headCancel()

			// Encode URL properly (handles spaces and special characters)
			encodedStreamURL, encErr := utils.EncodeURLWithSpaces(requestURL)
			if encErr != nil {
				log.Printf("[debrid-health] failed to encode stream URL: %v", encErr)
				encodedStreamURL = requestURL // Fall back without private fragment metadata
			}

			headReq, err := http.NewRequestWithContext(headCtx, http.MethodHead, encodedStreamURL, nil)
			if err == nil {
				headReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
				streamheaders.Apply(headReq.Header, requestHeaders)
				if resp, err := http.DefaultClient.Do(headReq); err == nil {
					contentLength := resp.ContentLength
					contentType := resp.Header.Get("Content-Type")
					finalURL := resp.Request.URL.String()
					resp.Body.Close()

					log.Printf("[debrid-health] pre-resolved stream %s: HEAD status=%d content-length=%d content-type=%q final-url=%q",
						result.Title, resp.StatusCode, contentLength, contentType, safeURLForLog(finalURL))
					if IsKnownPlaceholderURL(finalURL) {
						log.Printf("[debrid-health] pre-resolved stream %s redirected to known placeholder URL", result.Title)
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: "stream redirected to a known placeholder URL",
						}, nil
					}

					if resp.StatusCode == http.StatusNotFound {
						log.Printf("[debrid-health] pre-resolved stream %s returned 404 - treating as uncached", result.Title)
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: "stream returned 404 (not found)",
						}, nil
					}
					// Some direct media hosts reject HEAD with 403 or 405 while serving
					// ranged GET requests normally. Verify a small range before rejecting
					// the stream; don't treat the HEAD error body's size as media size.
					if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusMethodNotAllowed {
						log.Printf("[debrid-health] pre-resolved stream %s: HEAD returned %d, trying ranged GET", result.Title, resp.StatusCode)
						placeholder, getStatus, getFinalURL, probeErr := probePreResolvedPlaceholderRedirect(ctx, encodedStreamURL, requestHeaders, healthCheckTimeout)
						if probeErr != nil {
							log.Printf("[debrid-health] placeholder redirect probe failed for pre-resolved stream %s: %v", result.Title, probeErr)
						} else {
							log.Printf("[debrid-health] pre-resolved stream %s: ranged GET status=%d final-url=%q placeholder=%t",
								result.Title, getStatus, safeURLForLog(getFinalURL), placeholder)
							if placeholder {
								return &DebridHealthCheck{
									Healthy:      false,
									Status:       "not_cached",
									Cached:       false,
									Provider:     result.Attributes["tracker"],
									ErrorMessage: "stream redirected to an unavailable-content placeholder",
								}, nil
							}
							if getStatus >= 400 {
								return &DebridHealthCheck{
									Healthy:      false,
									Status:       "not_cached",
									Cached:       false,
									Provider:     result.Attributes["tracker"],
									ErrorMessage: fmt.Sprintf("stream returned HTTP %d", getStatus),
								}, nil
							}
						}
					} else if resp.StatusCode >= 500 && isInternetArchiveDirectStream(result, streamURL) {
						log.Printf("[debrid-health] pre-resolved Internet Archive stream %s returned HEAD HTTP %d - trying ranged GET fallback", result.Title, resp.StatusCode)
						if ok, status, contentLength, contentType := probePreResolvedRange(ctx, encodedStreamURL, requestHeaders); ok {
							log.Printf("[debrid-health] pre-resolved Internet Archive stream %s verified by ranged GET: status=%d content-length=%d content-type=%q", result.Title, status, contentLength, contentType)
							return &DebridHealthCheck{
								Healthy:  true,
								Status:   "cached",
								Cached:   true,
								Provider: result.Attributes["tracker"],
							}, nil
						} else {
							log.Printf("[debrid-health] pre-resolved Internet Archive stream %s ranged GET fallback failed: status=%d content-length=%d content-type=%q", result.Title, status, contentLength, contentType)
						}
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: fmt.Sprintf("stream returned HTTP %d", resp.StatusCode),
						}, nil
					} else if resp.StatusCode >= 400 {
						log.Printf("[debrid-health] pre-resolved stream %s returned HTTP %d - treating as uncached", result.Title, resp.StatusCode)
						return &DebridHealthCheck{
							Healthy:      false,
							Status:       "not_cached",
							Cached:       false,
							Provider:     result.Attributes["tracker"],
							ErrorMessage: fmt.Sprintf("stream returned HTTP %d", resp.StatusCode),
						}, nil
					} else {
						// Only check content-length for successful HEAD responses (2xx/3xx)
						// Real video files are typically > 10MB, placeholders are usually < 1MB
						if contentLength > 0 && contentLength < 1*1024*1024 {
							log.Printf("[debrid-health] pre-resolved stream %s has suspiciously small size (%d bytes) - likely a placeholder", result.Title, contentLength)
							return &DebridHealthCheck{
								Healthy:      false,
								Status:       "not_cached",
								Cached:       false,
								Provider:     result.Attributes["tracker"],
								ErrorMessage: fmt.Sprintf("stream too small (%d bytes) - likely a placeholder", contentLength),
							}, nil
						}
					}
				} else {
					log.Printf("[debrid-health] HEAD request failed for pre-resolved stream %s: %v", result.Title, err)
				}
			}
		}

		healthResult := &DebridHealthCheck{
			Healthy:  true,
			Status:   "cached",
			Cached:   true,
			Provider: result.Attributes["tracker"],
		}

		// Prefer the shared full prober so prequeue can reuse the exact result.
		// Retain the local track probe as a fallback for callers that do not wire
		// the video handler into this service.
		if streamURL != "" && (s.fullProber != nil || s.ffprobePath != "") {
			var (
				tracks     *TrackProbeResult
				mediaProbe *models.VideoFullResult
				err        error
			)
			if s.fullProber != nil {
				probeCtx, probeCancel := context.WithTimeout(ctx, healthCheckTimeout)
				mediaProbe, err = s.fullProber.ProbeVideoFull(probeCtx, streamURL)
				probeCancel()
				if err == nil && mediaProbe != nil {
					tracks = trackProbeResultFromMediaProbe(mediaProbe)
				} else if err == nil {
					err = fmt.Errorf("full media probe returned no result")
				}
			} else {
				tracks, err = s.probeAllTracksWithTimeout(ctx, streamURL, healthCheckTimeout)
			}
			if err != nil {
				log.Printf("[debrid-health] probe failed for pre-resolved stream %s: %v", result.Title, err)
				if cached, ok := s.cachedPreResolvedHealth(preResolvedCacheKey); ok {
					cached.TrackProbeError = appendProbeNote(cached.TrackProbeError, fmt.Sprintf("recent probe failed: %v", err))
					log.Printf("[debrid-health] using recent positive pre-resolved health cache for %s after probe failure", result.Title)
					return cached, nil
				}
				return &DebridHealthCheck{
					Healthy:      false,
					Status:       "not_cached",
					Cached:       false,
					Provider:     result.Attributes["tracker"],
					ErrorMessage: fmt.Sprintf("probe failed: %v", err),
				}, nil
			}
			if len(tracks.AudioTracks) == 0 {
				log.Printf("[debrid-health] pre-resolved stream %s has 0 audio streams - treating as uncached placeholder", result.Title)
				return &DebridHealthCheck{
					Healthy:      false,
					Status:       "not_cached",
					Cached:       false,
					Provider:     result.Attributes["tracker"],
					ErrorMessage: "stream appears to be a placeholder (no audio streams)",
				}, nil
			}
			healthResult.AudioTracks = tracks.AudioTracks
			healthResult.SubtitleTracks = tracks.SubtitleTracks
			healthResult.MediaProbe = cloneVideoFullResult(mediaProbe)
			log.Printf("[debrid-health] pre-resolved stream %s verified with %d audio streams", result.Title, len(tracks.AudioTracks))
		}

		s.rememberPreResolvedHealth(preResolvedCacheKey, healthResult)
		return healthResult, nil
	}

	// Extract info hash from result attributes (may be empty for torrent file uploads)
	infoHash := strings.TrimSpace(result.Attributes["infoHash"])
	if infoHash == "" {
		// Try to extract from magnet link
		if strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
			infoHash = extractInfoHashFromMagnet(result.Link)
		}
	}

	// Check if we have a torrent URL (for cases without magnet/infohash)
	torrentURL := strings.TrimSpace(result.Attributes["torrentURL"])

	// We need either infohash/magnet or torrent URL
	hasMagnet := strings.HasPrefix(strings.ToLower(result.Link), "magnet:")
	if infoHash == "" && !hasMagnet && torrentURL == "" {
		status := "error"
		if quickOnly {
			status = "skipped"
		}
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       status,
			Cached:       false,
			ErrorMessage: "missing info hash and no torrent URL available",
		}, nil
	}

	settings = config.FilterSettingsForProfile(settings, strings.TrimSpace(result.Attributes["profileId"]))

	if !verifyUncached && infoHash != "" {
		if quickResults, ok := s.checkQuickCacheAcrossProvidersBulk(ctx, settings, []models.NZBResult{result}); ok && len(quickResults) == 1 {
			return quickResults[0], nil
		}
	}

	if quickOnly {
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "skipped",
			Cached:       false,
			InfoHash:     infoHash,
			ErrorMessage: "quick cache check unavailable for enabled debrid providers",
		}, nil
	}

	return s.checkHealthAcrossProviders(ctx, settings, result, infoHash, torrentURL, verifyUncached)
}

func trackProbeResultFromMediaProbe(probe *models.VideoFullResult) *TrackProbeResult {
	result := &TrackProbeResult{
		AudioTracks:    make([]AudioTrackInfo, 0, len(probe.AudioStreams)),
		SubtitleTracks: make([]SubtitleTrackInfo, 0, len(probe.SubtitleStreams)),
	}
	for _, track := range probe.AudioStreams {
		result.AudioTracks = append(result.AudioTracks, AudioTrackInfo{
			Index: track.Index, Language: track.Language, Codec: track.Codec,
			Profile: track.Profile, Title: track.Title,
		})
	}
	for _, track := range probe.SubtitleStreams {
		bitmapType, isBitmap := bitmapSubtitleCodecs[strings.ToLower(strings.TrimSpace(track.Codec))]
		result.SubtitleTracks = append(result.SubtitleTracks, SubtitleTrackInfo{
			Index: track.Index, Language: track.Language, Codec: track.Codec,
			Title: track.Title, Forced: track.IsForced, IsBitmap: isBitmap, BitmapType: bitmapType,
		})
	}
	return result
}

// checkHealthAcrossProviders performs the mutating add/check/remove health probe
// on every enabled provider. A cached result from any provider makes the item
// cached and playable, regardless of failures or misses elsewhere.
func (s *HealthService) checkHealthAcrossProviders(ctx context.Context, settings config.Settings, result models.NZBResult, infoHash, torrentURL string, verifyUncached bool) (*DebridHealthCheck, error) {
	type configuredProvider struct {
		name   string
		client Provider
	}
	providers := make([]configuredProvider, 0, len(settings.Streaming.DebridProviders))
	for i := range settings.Streaming.DebridProviders {
		providerConfig := &settings.Streaming.DebridProviders[i]
		if !providerConfig.Enabled || strings.TrimSpace(providerConfig.APIKey) == "" {
			continue
		}
		client, ok := GetProvider(strings.ToLower(providerConfig.Provider), providerConfig.APIKey)
		if !ok {
			continue
		}
		if configurable, ok := client.(Configurable); ok && providerConfig.Config != nil {
			configurable.Configure(providerConfig.Config)
		}
		providers = append(providers, configuredProvider{name: client.Name(), client: client})
	}
	if len(providers) == 0 {
		return &DebridHealthCheck{Status: "error", InfoHash: infoHash, ErrorMessage: "no debrid provider configured or enabled"}, nil
	}

	type providerHealthResult struct {
		name   string
		health *DebridHealthCheck
		err    error
	}
	results := make(chan providerHealthResult, len(providers))
	for _, provider := range providers {
		go func(provider configuredProvider) {
			health, err := s.checkProviderHealth(ctx, provider.client, result, infoHash, torrentURL, verifyUncached)
			results <- providerHealthResult{name: provider.name, health: health, err: err}
		}(provider)
	}

	var cached *DebridHealthCheck
	allNotCached := true
	errors := make([]string, 0, len(providers))
	for range providers {
		providerResult := <-results
		if providerResult.err != nil {
			allNotCached = false
			errors = append(errors, fmt.Sprintf("%s: %v", providerResult.name, providerResult.err))
			continue
		}
		if providerResult.health != nil && providerResult.health.Cached {
			if cached == nil {
				cached = providerResult.health
			}
			continue
		}
		if providerResult.health == nil || providerResult.health.Status != "not_cached" {
			allNotCached = false
			if providerResult.health != nil && providerResult.health.ErrorMessage != "" {
				errors = append(errors, fmt.Sprintf("%s: %s", providerResult.name, providerResult.health.ErrorMessage))
			}
		}
	}
	if cached != nil {
		log.Printf("[debrid-health] multi-provider health result cached=true provider=%s", cached.Provider)
		return cached, nil
	}
	if allNotCached {
		return &DebridHealthCheck{Healthy: false, Status: "not_cached", Cached: false, Provider: "all", InfoHash: infoHash}, nil
	}
	return &DebridHealthCheck{
		Healthy:      false,
		Status:       "error",
		Cached:       false,
		Provider:     "multiple",
		InfoHash:     infoHash,
		ErrorMessage: "no provider confirmed a cached copy: " + strings.Join(errors, "; "),
	}, nil
}

func shouldUseQuickTorboxCacheCheck(providers []config.DebridProviderSettings, selected *config.DebridProviderSettings, requestedProvider, infoHash string) bool {
	if selected == nil || strings.TrimSpace(infoHash) == "" {
		return false
	}
	if requestedProvider != "" && !strings.EqualFold(requestedProvider, "torbox") {
		return false
	}
	if !strings.EqualFold(selected.Provider, "torbox") {
		return false
	}

	return selected.Enabled && strings.TrimSpace(selected.APIKey) != ""
}

// QuickCacheKey returns the provider+info-hash key used to dedupe quick cache
// checks, or "" when the result has no info hash (no safe quick check).
func QuickCacheKey(result models.NZBResult) string {
	return quickCacheDedupKey(result)
}

func quickCacheDedupKey(result models.NZBResult) string {
	provider := strings.ToLower(strings.TrimSpace(result.Attributes["provider"]))
	infoHash := quickCacheInfoHash(result)
	if infoHash == "" {
		return ""
	}
	return provider + ":" + infoHash
}

func quickCacheInfoHash(result models.NZBResult) string {
	infoHash := strings.ToLower(strings.TrimSpace(result.Attributes["infoHash"]))
	if infoHash == "" && strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
		infoHash = extractInfoHashFromMagnet(result.Link)
	}
	return infoHash
}

func debridHealthCheckTimeout(settings config.Settings) time.Duration {
	timeoutSec := settings.Streaming.HealthCheckTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 15
	}
	return time.Duration(timeoutSec) * time.Second
}

func isInternetArchiveDirectStream(result models.NZBResult, streamURL string) bool {
	if strings.EqualFold(strings.TrimSpace(result.Attributes["scraper"]), "internetarchive") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(result.Attributes["tracker"]), "archive.org") {
		return true
	}
	parsed, err := url.Parse(streamURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "archive.org" || strings.HasSuffix(host, ".archive.org")
}

func probePreResolvedRange(ctx context.Context, streamURL string, requestHeaders map[string]string) (ok bool, status int, contentLength int64, contentType string) {
	rangeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(rangeCtx, http.MethodGet, streamURL, nil)
	if err != nil {
		return false, 0, 0, ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
	streamheaders.Apply(req.Header, requestHeaders)
	req.Header.Set("Range", "bytes=0-1023")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[debrid-health] ranged GET request failed for pre-resolved stream: %v", err)
		return false, 0, 0, ""
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	contentLength = resp.ContentLength
	contentType = resp.Header.Get("Content-Type")
	if contentLength <= 0 && resp.Header.Get("Content-Range") != "" {
		contentLength = 1024
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false, resp.StatusCode, contentLength, contentType
	}
	if IsKnownPlaceholderURL(resp.Request.URL.String()) {
		return false, resp.StatusCode, contentLength, contentType
	}
	if contentLength > 0 && contentLength < 512 {
		return false, resp.StatusCode, contentLength, contentType
	}
	return true, resp.StatusCode, contentLength, contentType
}

func probePreResolvedPlaceholderRedirect(ctx context.Context, streamURL string, requestHeaders map[string]string, timeout time.Duration) (placeholder bool, status int, finalURL string, err error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, streamURL, nil)
	if err != nil {
		return false, 0, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
	streamheaders.Apply(req.Header, requestHeaders)
	req.Header.Set("Range", "bytes=0-4095")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, 0, "", err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return false, resp.StatusCode, resp.Request.URL.String(), readErr
	}

	finalURL = resp.Request.URL.String()
	return IsKnownPlaceholderResponse(finalURL, body), resp.StatusCode, finalURL, nil
}

func (s *HealthService) checkProviderHealth(ctx context.Context, client Provider, result models.NZBResult, infoHash, torrentURL string, verifyUncached bool) (*DebridHealthCheck, error) {
	providerName := client.Name()

	// Use add+check+remove method to verify cache status
	identifier := infoHash
	if identifier == "" {
		identifier = torrentURL
	}
	identifierForLog := identifier
	if identifier == torrentURL && torrentURL != "" {
		identifierForLog = safeURLForLog(torrentURL)
	}
	log.Printf("[debrid-health] %s checking torrent %s via add+check+remove", providerName, identifierForLog)

	var addResp *AddMagnetResult
	var err error

	// Determine how to add the torrent: magnet link or torrent file upload
	if strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
		// Use magnet link
		log.Printf("[debrid-health] adding magnet to %s", providerName)
		addResp, err = addMagnetUnlessUncached(ctx, client, result.Link)
		if err != nil {
			log.Printf("[debrid-health] %s add magnet failed for %s: %v", providerName, identifierForLog, err)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "error",
				Cached:       false,
				Provider:     providerName,
				InfoHash:     infoHash,
				ErrorMessage: fmt.Sprintf("add magnet failed: %v", err),
			}, nil
		}
	} else if torrentURL != "" {
		// Download and upload torrent file
		log.Printf("[debrid-health] downloading torrent file from %s", safeURLForLog(torrentURL))
		source, downloadErr := downloadTorrentSource(ctx, torrentURL, 60*time.Second)
		if downloadErr != nil {
			log.Printf("[debrid-health] %s download torrent failed for %s: %v", providerName, identifierForLog, downloadErr)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "error",
				Cached:       false,
				Provider:     providerName,
				InfoHash:     infoHash,
				ErrorMessage: fmt.Sprintf("download torrent file failed: %v", downloadErr),
			}, nil
		}
		addResp, err = source.add(ctx, client)
		if source.magnet != "" {
			result.Link = source.magnet
			infoHash = extractInfoHashFromMagnet(source.magnet)
		}
		if err != nil {
			log.Printf("[debrid-health] %s add torrent source failed for %s: %v", providerName, identifierForLog, err)
			return &DebridHealthCheck{
				Healthy:      false,
				Status:       "error",
				Cached:       false,
				Provider:     providerName,
				InfoHash:     infoHash,
				ErrorMessage: fmt.Sprintf("add torrent source failed: %v", err),
			}, nil
		}
	} else {
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: "no magnet link or torrent URL available",
		}, nil
	}

	torrentID := addResp.ID
	providerCacheKnown := addResp.CacheStatusKnown
	providerReportedCached := providerCacheKnown && addResp.Cached
	if strings.HasPrefix(strings.ToLower(result.Link), "magnet:") {
		RegisterMagnet(providerName, torrentID, result.Link)
	}
	log.Printf("[debrid-health] %s torrent added with ID %s, getting file list", providerName, torrentID)
	if providerCacheKnown && !addResp.Cached {
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s add response authoritatively reported torrent %s is not cached", providerName, torrentID)
		return &DebridHealthCheck{
			Healthy:  false,
			Status:   "not_cached",
			Cached:   false,
			Provider: providerName,
			InfoHash: infoHash,
		}, nil
	}

	knownCachedProbeFailure := func(message string) (*DebridHealthCheck, error) {
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		return &DebridHealthCheck{
			Healthy:         true,
			Status:          "cached",
			Cached:          true,
			Provider:        providerName,
			InfoHash:        infoHash,
			TrackProbeError: message,
		}, nil
	}

	// First, get the torrent info to see what files are available
	info, err := client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		if providerReportedCached {
			return knownCachedProbeFailure(fmt.Sprintf("get torrent info failed: %v", err))
		}
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s get initial torrent info failed for %s: %v", providerName, torrentID, err)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("get torrent info failed: %v", err),
		}, nil
	}

	// Select all files for caching, but track the preferred playable target
	selection := selectMediaFiles(info.Files, buildSelectionHints(result, info.Filename))
	if selection == nil {
		if providerReportedCached {
			return knownCachedProbeFailure("no media files found in torrent")
		}
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s torrent %s has no media files", providerName, torrentID)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: "no media files found in torrent",
		}, nil
	}
	if selection.RejectionReason != "" {
		if providerReportedCached {
			return knownCachedProbeFailure(selection.RejectionReason)
		}
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s torrent %s rejected: %s", providerName, torrentID, selection.RejectionReason)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: selection.RejectionReason,
		}, nil
	}
	if len(selection.OrderedIDs) == 0 {
		if providerReportedCached {
			return knownCachedProbeFailure("no media files found in torrent")
		}
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s torrent %s has no media files", providerName, torrentID)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: "no media files found in torrent",
		}, nil
	}

	if selection.PreferredID != "" {
		log.Printf("[debrid-health] primary file candidate: %q (reason: %s, id=%s)", selection.PreferredLabel, selection.PreferredReason, selection.PreferredID)
	}

	fileSelection := strings.Join(selection.OrderedIDs, ",")
	log.Printf("[debrid-health] %s torrent %s selecting %d media files: %s", providerName, torrentID, len(selection.OrderedIDs), fileSelection)

	// Select media files - this is required to trigger the provider to check cache status
	if err := client.SelectFiles(ctx, torrentID, fileSelection); err != nil {
		if providerReportedCached {
			return knownCachedProbeFailure(fmt.Sprintf("select files failed: %v", err))
		}
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s select files failed for %s: %v", providerName, torrentID, err)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("select files failed: %v", err),
		}, nil
	}

	// Check the torrent info again to see if it's cached or needs download
	info, err = client.GetTorrentInfo(ctx, torrentID)
	if err != nil {
		if providerReportedCached {
			return knownCachedProbeFailure(fmt.Sprintf("get torrent info failed: %v", err))
		}
		// Try to clean up even if we got an error
		s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)
		log.Printf("[debrid-health] %s get torrent info failed for %s: %v", providerName, torrentID, err)
		return &DebridHealthCheck{
			Healthy:      false,
			Status:       "error",
			Cached:       false,
			Provider:     providerName,
			InfoHash:     infoHash,
			ErrorMessage: fmt.Sprintf("get torrent info failed: %v", err),
		}, nil
	}

	// Check if the torrent is already downloaded (cached)
	isCached := providerReportedCached || strings.ToLower(info.Status) == "downloaded"
	log.Printf("[debrid-health] %s torrent %s status=%s cached=%t", providerName, torrentID, info.Status, isCached)
	if !isCached {
		RememberUncached(providerName, infoHash, extractInfoHashFromMagnet(result.Link), info.Hash)
	}

	// Prepare the result
	healthResult := &DebridHealthCheck{
		Healthy:  isCached,
		Status:   "not_cached",
		Cached:   isCached,
		Provider: providerName,
		InfoHash: infoHash,
	}
	if isCached {
		healthResult.Status = "cached"
	}

	// Use infoHash as track cache key; fall back to torrentURL when infoHash is unavailable (e.g. Jackett torrent file uploads)
	trackCacheKey := infoHash
	if trackCacheKey == "" && torrentURL != "" {
		trackCacheKey = "url:" + torrentURL
	}

	// If cached and has links, check track cache or start async probe
	if isCached && len(info.Links) > 0 && s.ffprobePath != "" && trackCacheKey != "" {
		// Find the link for the preferred file (not just the first link)
		// Links are ordered by original file ID, not selection order
		preferredLinkIdx := 0
		if selection != nil && selection.PreferredID != "" {
			preferredFileID := 0
			fmt.Sscanf(selection.PreferredID, "%d", &preferredFileID)
			if preferredFileID > 0 {
				// Build list of selected file IDs in order (this matches links order)
				var selectedFileIDs []int
				for _, f := range info.Files {
					if f.Selected == 1 {
						selectedFileIDs = append(selectedFileIDs, f.ID)
					}
				}
				// Find index of preferred file in selected files list
				for idx, fid := range selectedFileIDs {
					if fid == preferredFileID {
						preferredLinkIdx = idx
						break
					}
				}
				log.Printf("[debrid-health] preferred file ID=%d, link index=%d (of %d links)",
					preferredFileID, preferredLinkIdx, len(info.Links))
			}
		}
		// Ensure link index is valid
		if preferredLinkIdx >= len(info.Links) {
			preferredLinkIdx = 0
		}

		// Check track cache first
		s.trackCacheMu.RLock()
		cached, hasCached := s.trackCache[trackCacheKey]
		s.trackCacheMu.RUnlock()

		if hasCached && time.Now().Before(cached.expiresAt) {
			// Return cached tracks
			healthResult.AudioTracks = cached.audioTracks
			healthResult.SubtitleTracks = cached.subtitleTracks
			healthResult.TrackProbeError = cached.probeError
			log.Printf("[debrid-health] track cache HIT for %s: %d audio, %d subtitle",
				trackCacheKey, len(cached.audioTracks), len(cached.subtitleTracks))
		} else {
			// Check if already probing
			s.probingMu.Lock()
			isProbing := s.probing[trackCacheKey]
			if !isProbing {
				s.probing[trackCacheKey] = true
			}
			s.probingMu.Unlock()

			if isProbing {
				// Already probing, return loading state
				healthResult.TracksLoading = true
				log.Printf("[debrid-health] track probe in progress for %s", trackCacheKey)
			} else {
				// Start async probe - need to unrestrict link first (before torrent is deleted)
				unrestricted, err := client.UnrestrictLink(ctx, info.Links[preferredLinkIdx])
				if err != nil {
					log.Printf("[debrid-health] failed to unrestrict link for track probe: %v", err)
					healthResult.TrackProbeError = fmt.Sprintf("unrestrict failed: %v", err)
					// Clear probing state
					s.probingMu.Lock()
					delete(s.probing, trackCacheKey)
					s.probingMu.Unlock()
				} else if unrestricted.DownloadURL != "" {
					// Start async probe with the download URL
					downloadURL := unrestricted.DownloadURL
					healthResult.TracksLoading = true
					go s.probeTracksAsync(trackCacheKey, downloadURL)
					log.Printf("[debrid-health] started async track probe for %s (link %d: %s)",
						trackCacheKey, preferredLinkIdx, unrestricted.Filename)
				}
			}
		}
	}

	// Always remove the torrent after checking - especially important for non-cached torrents
	// which may have started downloading (e.g., Torbox starts downloads immediately).
	// But skip deletion if playback has marked this torrent as active — deleting it would
	// break the in-progress stream (TorBox reuses the same torrent ID for the same hash).
	if !isCached {
		log.Printf("[debrid-health] torrent %s is not cached (status=%s), removing from %s account", torrentID, info.Status, providerName)
	}
	s.deleteHealthCheckTorrent(ctx, client, providerName, torrentID)

	return healthResult, nil
}

// extractInfoHashFromMagnet extracts the info hash from a magnet URI.
func extractInfoHashFromMagnet(magnetURL string) string {
	// magnet:?xt=urn:btih:HASH...
	lower := strings.ToLower(magnetURL)
	xtIndex := strings.Index(lower, "xt=urn:btih:")
	if xtIndex == -1 {
		return ""
	}

	hashStart := xtIndex + len("xt=urn:btih:")
	remaining := magnetURL[hashStart:]

	// Hash ends at & or end of string
	ampIndex := strings.Index(remaining, "&")
	if ampIndex == -1 {
		return strings.ToLower(strings.TrimSpace(remaining))
	}

	return strings.ToLower(strings.TrimSpace(remaining[:ampIndex]))
}

var mediaExtensionPriority = map[string]int{
	".mp4":  0,
	".m4v":  1,
	".mkv":  2,
	".webm": 3,
	".mov":  4,
	".avi":  5,
	".mpg":  6,
	".mpeg": 6,
	".ts":   7,
	".m2ts": 7,
	".mts":  7,
	".wmv":  8,
	".flv":  9,
	".vob":  10,
	".ogv":  11,
	".3gp":  12,
	".divx": 13,
}

type mediaFileSelection struct {
	OrderedIDs      []string
	PreferredID     string
	PreferredLabel  string
	PreferredReason string
	RejectionReason string // Set when selection is rejected (e.g., target episode not found)
}

func (s *mediaFileSelection) promotePreferredToFront() {
	if s == nil {
		return
	}
	if s.PreferredID == "" || len(s.OrderedIDs) == 0 {
		return
	}
	for idx, id := range s.OrderedIDs {
		if id == s.PreferredID {
			if idx != 0 {
				s.OrderedIDs[0], s.OrderedIDs[idx] = s.OrderedIDs[idx], s.OrderedIDs[0]
			}
			return
		}
	}
}

// selectMediaFiles returns a selection structure that includes only media file IDs (for caching)
// while designating a preferred playable file for streaming.
func selectMediaFiles(files []File, hints mediaresolve.SelectionHints) *mediaFileSelection {
	type candidate struct {
		id       string
		label    string
		priority int
		size     int64
	}

	if len(files) == 0 {
		return nil
	}

	orderedIDs := make([]string, 0, len(files))
	var candidates []candidate
	var resolverCandidates []mediaresolve.Candidate
	bestIdx := -1
	isBDMV := false

	for _, file := range files {
		id := fmt.Sprintf("%d", file.ID)

		// Detect BDMV (Blu-ray) structure
		if strings.Contains(strings.ToUpper(file.Path), "/BDMV/STREAM/") {
			isBDMV = true
		}

		ext := strings.ToLower(path.Ext(file.Path))
		priority, ok := mediaExtensionPriority[ext]
		if !ok {
			// Skip non-media files - don't add them to orderedIDs
			continue
		}
		if mediaresolve.IsNonContentCandidate(file.Path) {
			log.Printf("[debrid-playback] excluding non-content media file: %q", file.Path)
			continue
		}

		// Only add media files to the ordered list
		orderedIDs = append(orderedIDs, id)

		candidates = append(candidates, candidate{
			id:       id,
			label:    file.Path,
			priority: priority,
			size:     file.Bytes,
		})
		resolverCandidates = append(resolverCandidates, mediaresolve.Candidate{
			Label:    file.Path,
			Priority: priority,
		})
		idx := len(candidates) - 1
		if bestIdx == -1 || candidates[idx].priority < candidates[bestIdx].priority {
			bestIdx = idx
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	selection := &mediaFileSelection{
		OrderedIDs: orderedIDs,
	}

	if len(candidates) == 1 {
		// For single-file torrents, validate the file matches the target episode
		// This prevents playing the wrong episode when absolute numbering differs from S##E##
		if hints.TargetSeason > 0 && hints.TargetEpisode > 0 {
			targetCode := mediaresolve.EpisodeCode{Season: hints.TargetSeason, Episode: hints.TargetEpisode}
			matchesSeasonEpisode := mediaresolve.CandidateMatchesEpisode(candidates[0].label, targetCode) || mediaresolve.CandidateMatchesEpisodeAlias(candidates[0].label, hints.AlternateEpisodes)
			matchesAbsolute := mediaresolve.CandidateMatchesSelectionAbsolute(candidates[0].label, hints)
			// For daily shows, use exact date match only - no tolerance.
			// Adjacent dates are different episodes, so tolerance would match the WRONG episode.
			matchesDailyDate := hints.IsDaily && hints.TargetAirDate != "" &&
				mediaresolve.CandidateMatchesDailyDate(candidates[0].label, hints.TargetAirDate, 0)

			if !matchesSeasonEpisode && !matchesAbsolute && !matchesDailyDate {
				var rejectionMsg string
				if hints.IsDaily && hints.TargetAirDate != "" {
					rejectionMsg = fmt.Sprintf("single file %q does not match target S%02dE%02d or date %s",
						candidates[0].label, hints.TargetSeason, hints.TargetEpisode, hints.TargetAirDate)
				} else if hints.AbsoluteEpisodeNumber > 0 {
					rejectionMsg = fmt.Sprintf("single file %q does not match target S%02dE%02d or absolute ep %d",
						candidates[0].label, hints.TargetSeason, hints.TargetEpisode, hints.AbsoluteEpisodeNumber)
				} else {
					rejectionMsg = fmt.Sprintf("single file %q does not match target S%02dE%02d",
						candidates[0].label, hints.TargetSeason, hints.TargetEpisode)
				}
				log.Printf("[debrid-playback] rejecting result: %s", rejectionMsg)
				return &mediaFileSelection{
					RejectionReason: rejectionMsg,
				}
			}

			// Log which matching method succeeded
			if matchesDailyDate && !matchesSeasonEpisode && !matchesAbsolute {
				log.Printf("[debrid-playback] single file matched by daily date %s", hints.TargetAirDate)
			} else if matchesAbsolute && !matchesSeasonEpisode {
				log.Printf("[debrid-playback] single file matched by absolute episode %d", hints.AbsoluteEpisodeNumber)
			}
		}

		selection.PreferredID = candidates[0].id
		selection.PreferredLabel = candidates[0].label
		selection.PreferredReason = "only playable file found"
		selection.promotePreferredToFront()
		return selection
	}

	// For BDMV (Blu-ray) structures, select the largest file as it's the main feature.
	// BDMV files are named numerically (00000.m2ts, 00001.m2ts, etc.) so title matching
	// doesn't work. The main movie is always significantly larger than bonus content.
	if isBDMV {
		largestIdx := 0
		for idx, cand := range candidates {
			if cand.size > candidates[largestIdx].size {
				largestIdx = idx
			}
		}
		selection.PreferredID = candidates[largestIdx].id
		selection.PreferredLabel = candidates[largestIdx].label
		selection.PreferredReason = fmt.Sprintf("BDMV largest file (%d MB)", candidates[largestIdx].size/(1024*1024))
		selection.promotePreferredToFront()
		log.Printf("[debrid-playback] BDMV structure detected, selecting largest file: %s (%d bytes)", candidates[largestIdx].label, candidates[largestIdx].size)
		return selection
	}

	selectedIdx, reason := mediaresolve.SelectBestCandidate(resolverCandidates, hints)
	if selectedIdx == -1 {
		// Check if we were looking for a specific episode that wasn't found
		// In this case, reject the result entirely rather than falling back
		if hints.TargetEpisode > 0 && hints.TargetSeason > 0 {
			var rejectionMsg string
			if hints.IsDaily && hints.TargetAirDate != "" {
				rejectionMsg = fmt.Sprintf("target episode S%02dE%02d (date: %s) not found in torrent", hints.TargetSeason, hints.TargetEpisode, hints.TargetAirDate)
			} else if hints.AbsoluteEpisodeNumber > 0 {
				rejectionMsg = fmt.Sprintf("target episode S%02dE%02d (abs: %d) not found in torrent", hints.TargetSeason, hints.TargetEpisode, hints.AbsoluteEpisodeNumber)
			} else {
				rejectionMsg = fmt.Sprintf("target episode S%02dE%02d not found in torrent", hints.TargetSeason, hints.TargetEpisode)
			}
			log.Printf("[debrid-playback] rejecting result: %s", rejectionMsg)
			return &mediaFileSelection{
				RejectionReason: rejectionMsg,
			}
		}
		selectedIdx = bestIdx
		reason = "fallback to extension priority"
	}

	selection.PreferredID = candidates[selectedIdx].id
	selection.PreferredLabel = candidates[selectedIdx].label
	selection.PreferredReason = reason
	selection.promotePreferredToFront()

	return selection
}

// bitmapSubtitleCodecs maps codec names to their display type
var bitmapSubtitleCodecs = map[string]string{
	"hdmv_pgs_subtitle": "PGS",
	"dvd_subtitle":      "VOBSUB",
	"dvdsub":            "VOBSUB",
	"pgssub":            "PGS",
}

// TrackProbeResult holds audio and subtitle track information from probing.
type TrackProbeResult struct {
	AudioTracks    []AudioTrackInfo
	SubtitleTracks []SubtitleTrackInfo
}

// probeAllTracks probes a URL for audio and subtitle track information.
// Unlike the HLS probe, this function includes bitmap subtitles with isBitmap flag.
func (s *HealthService) probeAllTracks(ctx context.Context, streamURL string) (*TrackProbeResult, error) {
	return s.probeAllTracksWithTimeout(ctx, streamURL, 20*time.Second)
}

func (s *HealthService) probeAllTracksWithTimeout(ctx context.Context, streamURL string, timeout time.Duration) (*TrackProbeResult, error) {
	if s.ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe not configured")
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	probeURL, requestHeaders := streamheaders.Extract(streamURL)
	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-analyzeduration", "10000000", // 10 seconds
		"-probesize", "10000000", // 10MB
	}
	if headerValue := streamheaders.FFmpegValue(requestHeaders); headerValue != "" {
		args = append(args, "-headers", headerValue)
	}
	args = append(args, probeURL)

	var stdout, stderr bytes.Buffer
	isTorboxDownload := torboxrate.IsDownloadURL(probeURL)
	for attempt := 0; ; attempt++ {
		if isTorboxDownload {
			if err := torboxrate.Downloads.Wait(probeCtx, probeURL); err != nil {
				return nil, fmt.Errorf("ffprobe cooldown wait: %w", err)
			}
		}

		stdout.Reset()
		stderr.Reset()
		cmd := exec.CommandContext(probeCtx, s.ffprobePath, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			break
		}

		probeError := strings.ToLower(stderr.String())
		if !isTorboxDownload || (!strings.Contains(probeError, "429") && !strings.Contains(probeError, "too many requests")) {
			return nil, fmt.Errorf("ffprobe failed: %w (stderr: %s)", err, stderr.String())
		}
		delay := torboxrate.Downloads.Record(probeURL, "", stderr.Bytes())
		if attempt >= 1 {
			return nil, fmt.Errorf("ffprobe failed after TorBox rate-limit retry: %w (stderr: %s)", err, stderr.String())
		}
		log.Printf("[debrid-health] TorBox CDN rate limited track probe; retrying after %s", delay.Round(time.Millisecond))
	}

	var result struct {
		Streams []struct {
			Index       int               `json:"index"`
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Profile     string            `json:"profile"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	probeResult := &TrackProbeResult{}

	for _, stream := range result.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))

		switch stream.CodecType {
		case "audio":
			lang := ""
			title := ""
			if stream.Tags != nil {
				lang = stream.Tags["language"]
				title = stream.Tags["title"]
			}
			probeResult.AudioTracks = append(probeResult.AudioTracks, AudioTrackInfo{
				Index:    stream.Index,
				Language: lang,
				Codec:    codec,
				Profile:  strings.TrimSpace(stream.Profile),
				Title:    title,
			})

		case "subtitle":
			lang := ""
			title := ""
			isForced := false
			if stream.Tags != nil {
				lang = stream.Tags["language"]
				title = stream.Tags["title"]
			}
			if stream.Disposition != nil {
				isForced = stream.Disposition["forced"] > 0
			}

			// Check if this is a bitmap subtitle
			isBitmap := false
			bitmapType := ""
			if bt, ok := bitmapSubtitleCodecs[codec]; ok {
				isBitmap = true
				bitmapType = bt
			}

			probeResult.SubtitleTracks = append(probeResult.SubtitleTracks, SubtitleTrackInfo{
				Index:      stream.Index,
				Language:   lang,
				Codec:      codec,
				Title:      title,
				Forced:     isForced,
				IsBitmap:   isBitmap,
				BitmapType: bitmapType,
			})
		}
	}

	log.Printf("[debrid-health] track probe: audio=%d subtitle=%d", len(probeResult.AudioTracks), len(probeResult.SubtitleTracks))
	return probeResult, nil
}

// probeTracksAsync probes tracks in the background and caches the results.
func (s *HealthService) probeTracksAsync(infoHash, downloadURL string) {
	defer func() {
		// Clear probing state when done
		s.probingMu.Lock()
		delete(s.probing, infoHash)
		s.probingMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	entry := &trackCacheEntry{
		expiresAt: time.Now().Add(2 * time.Hour), // Cache for 2 hours
	}

	tracks, err := s.probeAllTracks(ctx, downloadURL)
	if err != nil {
		log.Printf("[debrid-health] async track probe failed for %s: %v", infoHash, err)
		entry.probeError = err.Error()
	} else {
		entry.audioTracks = tracks.AudioTracks
		entry.subtitleTracks = tracks.SubtitleTracks
		log.Printf("[debrid-health] async track probe complete for %s: %d audio, %d subtitle",
			infoHash, len(tracks.AudioTracks), len(tracks.SubtitleTracks))
	}

	// Store in cache
	s.trackCacheMu.Lock()
	s.trackCache[infoHash] = entry
	s.trackCacheMu.Unlock()
}

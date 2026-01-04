package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/indexer"
	"novastream/services/playback"
	user_settings "novastream/services/user_settings"
	"novastream/utils/filter"

	"github.com/gorilla/mux"
)

// SeriesDetailsProvider provides series metadata for episode counting
type SeriesDetailsProvider interface {
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
}

// PrequeueHandler handles prequeue requests for pre-loading playback streams
type PrequeueHandler struct {
	store              *playback.PrequeueStore
	indexerSvc         *indexer.Service
	playbackSvc        *playback.Service
	historySvc         *history.Service
	videoProber        VideoProber
	hlsCreator         HLSCreator
	metadataProber     VideoMetadataProber
	fullProber         VideoFullProber // Combined prober for single ffprobe call
	userSettingsSvc    *user_settings.Service
	clientSettingsSvc  ClientSettingsProvider
	configManager      *config.Manager
	metadataSvc        SeriesDetailsProvider // For episode counting
	subtitleExtractor  SubtitlePreExtractor  // For pre-extracting subtitles
	demoMode           bool
}

// ClientSettingsProvider interface for accessing per-client filter settings
type ClientSettingsProvider interface {
	Get(clientID string) (*models.ClientFilterSettings, error)
}

// VideoProber interface for probing video metadata
type VideoProber interface {
	ProbeVideoPath(ctx context.Context, path string) (*VideoProbeResult, error)
}

// VideoProbeResult contains the relevant HDR detection results
type VideoProbeResult struct {
	HasDolbyVision     bool
	HasHDR10           bool
	DolbyVisionProfile string
}

// AudioStreamInfo contains audio stream metadata for track selection
type AudioStreamInfo struct {
	Index    int
	Codec    string
	Language string
	Title    string
}

// SubtitleStreamInfo contains subtitle stream metadata for track selection
type SubtitleStreamInfo struct {
	Index     int
	Codec     string // e.g., "subrip", "ass" - needed for sidecar VTT extraction
	Language  string
	Title     string
	IsForced  bool
	IsDefault bool
}

// VideoMetadataResult contains stream metadata for track selection
type VideoMetadataResult struct {
	AudioStreams    []AudioStreamInfo
	SubtitleStreams []SubtitleStreamInfo
}

// VideoMetadataProber interface for probing video stream metadata
type VideoMetadataProber interface {
	ProbeVideoMetadata(ctx context.Context, path string) (*VideoMetadataResult, error)
}

// VideoFullResult combines HDR detection and stream metadata in a single result
type VideoFullResult struct {
	// HDR detection
	HasDolbyVision     bool
	HasHDR10           bool
	DolbyVisionProfile string
	// Audio codec detection
	HasTrueHD          bool // Audio requires transcoding (TrueHD, DTS-HD, etc.)
	HasCompatibleAudio bool // Audio can be copied without transcoding
	// Stream metadata
	AudioStreams    []AudioStreamInfo
	SubtitleStreams []SubtitleStreamInfo
	// Duration in seconds (for seeking calculations)
	Duration float64
}

// VideoFullProber interface for combined HDR and metadata probing in a single ffprobe call
type VideoFullProber interface {
	ProbeVideoFull(ctx context.Context, path string) (*VideoFullResult, error)
}

// HLSCreator interface for creating HLS sessions
type HLSCreator interface {
	CreateHLSSession(ctx context.Context, path string, hasDV bool, dvProfile string, hasHDR bool, audioTrackIndex int, subtitleTrackIndex int) (*HLSSessionResult, error)
}

// HLSSessionResult contains HLS session info
type HLSSessionResult struct {
	SessionID   string
	PlaylistURL string
}

// SubtitlePreExtractor interface for pre-extracting subtitles
type SubtitlePreExtractor interface {
	StartPreExtraction(ctx context.Context, path string, tracks []SubtitleTrackInfo, startOffset float64) map[int]*SubtitleExtractSession
}

// NewPrequeueHandler creates a new prequeue handler
func NewPrequeueHandler(
	indexerSvc *indexer.Service,
	playbackSvc *playback.Service,
	historySvc *history.Service,
	videoProber VideoProber,
	hlsCreator HLSCreator,
	demoMode bool,
) *PrequeueHandler {
	// 15 minute TTL for prequeue entries (allows time for credits when triggered at 90%)
	store := playback.NewPrequeueStore(15 * time.Minute)

	return &PrequeueHandler{
		store:       store,
		indexerSvc:  indexerSvc,
		playbackSvc: playbackSvc,
		historySvc:  historySvc,
		videoProber: videoProber,
		hlsCreator:  hlsCreator,
		demoMode:    demoMode,
	}
}

// SetVideoProber sets the video prober for HDR detection
func (h *PrequeueHandler) SetVideoProber(prober VideoProber) {
	h.videoProber = prober
}

// SetHLSCreator sets the HLS creator for HDR content
func (h *PrequeueHandler) SetHLSCreator(creator HLSCreator) {
	h.hlsCreator = creator
}

// SetMetadataProber sets the metadata prober for track selection
func (h *PrequeueHandler) SetMetadataProber(prober VideoMetadataProber) {
	h.metadataProber = prober
}

// SetFullProber sets the combined prober for single ffprobe call
func (h *PrequeueHandler) SetFullProber(prober VideoFullProber) {
	h.fullProber = prober
}

// SetUserSettingsService sets the user settings service for track preferences
func (h *PrequeueHandler) SetUserSettingsService(svc *user_settings.Service) {
	h.userSettingsSvc = svc
}

// SetConfigManager sets the config manager for global settings fallback
func (h *PrequeueHandler) SetConfigManager(cfgManager *config.Manager) {
	h.configManager = cfgManager
}

// SetClientSettingsService sets the client settings service for per-device filtering
func (h *PrequeueHandler) SetClientSettingsService(svc ClientSettingsProvider) {
	h.clientSettingsSvc = svc
}

// SetMetadataService sets the metadata service for episode counting
func (h *PrequeueHandler) SetMetadataService(svc SeriesDetailsProvider) {
	h.metadataSvc = svc
}

// SetSubtitleExtractor sets the subtitle extractor for pre-extraction
func (h *PrequeueHandler) SetSubtitleExtractor(extractor SubtitlePreExtractor) {
	h.subtitleExtractor = extractor
}

// Prequeue initiates a prequeue request for a title
func (h *PrequeueHandler) Prequeue(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	var req playback.PrequeueRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.TitleID) == "" {
		http.Error(w, "titleId is required", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.UserID) == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	if mediaType == "" {
		mediaType = "movie"
	}

	titleName := strings.TrimSpace(req.TitleName)
	if titleName == "" {
		http.Error(w, "titleName is required", http.StatusBadRequest)
		return
	}

	// Get client ID from request body or header
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(r.Header.Get("X-Client-ID"))
	}

	log.Printf("[prequeue] Received request: titleId=%s titleName=%q userId=%s clientId=%s mediaType=%s", req.TitleID, titleName, req.UserID, clientID, mediaType)

	// For series, determine the target episode based on watch history
	var targetEpisode *models.EpisodeReference
	if mediaType == "series" || mediaType == "tv" || mediaType == "show" {
		// If episode was explicitly provided, use it
		if req.SeasonNumber > 0 && req.EpisodeNumber > 0 {
			targetEpisode = &models.EpisodeReference{
				SeasonNumber:  req.SeasonNumber,
				EpisodeNumber: req.EpisodeNumber,
			}
			log.Printf("[prequeue] Using explicit episode S%02dE%02d", req.SeasonNumber, req.EpisodeNumber)
		} else if h.historySvc != nil {
			// Try to get next episode from watch history
			watchState, err := h.historySvc.GetSeriesWatchState(req.UserID, req.TitleID)
			if err == nil && watchState != nil && watchState.NextEpisode != nil {
				// Exclude season 0 (specials)
				if watchState.NextEpisode.SeasonNumber > 0 {
					targetEpisode = watchState.NextEpisode
					log.Printf("[prequeue] Using next episode from watch history: S%02dE%02d",
						targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber)
				} else {
					log.Printf("[prequeue] Skipping season 0 episode from watch history")
				}
			}

			// If no next episode, default to S01E01
			if targetEpisode == nil {
				targetEpisode = &models.EpisodeReference{
					SeasonNumber:  1,
					EpisodeNumber: 1,
				}
				log.Printf("[prequeue] Defaulting to S01E01 (no watch history)")
			}
		} else {
			// No history service, default to S01E01
			targetEpisode = &models.EpisodeReference{
				SeasonNumber:  1,
				EpisodeNumber: 1,
			}
			log.Printf("[prequeue] Defaulting to S01E01 (no history service)")
		}
	}

	// Create prequeue entry
	entry, _ := h.store.Create(req.TitleID, titleName, req.UserID, mediaType, req.Year, targetEpisode)

	// Start background worker with all the info needed for search
	go h.runPrequeueWorker(entry.ID, titleName, req.ImdbID, mediaType, req.Year, req.UserID, clientID, targetEpisode, req.StartOffset)

	// Return response
	resp := playback.PrequeueResponse{
		PrequeueID:    entry.ID,
		TargetEpisode: targetEpisode,
		Status:        playback.PrequeueStatusQueued,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetStatus returns the status of a prequeue request
func (h *PrequeueHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	vars := mux.Vars(r)
	prequeueID := strings.TrimSpace(vars["prequeueID"])
	if prequeueID == "" {
		http.Error(w, "prequeueID is required", http.StatusBadRequest)
		return
	}

	entry, exists := h.store.Get(prequeueID)
	if !exists {
		http.Error(w, "prequeue not found or expired", http.StatusNotFound)
		return
	}

	resp := entry.ToResponse()

	// In demo mode, set displayName to hide actual filenames
	if h.demoMode {
		resp.DisplayName = buildDisplayName(entry.TitleName, entry.Year, entry.TargetEpisode)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// buildDisplayName creates a display name from title, year, and episode info
func buildDisplayName(titleName string, year int, episode *models.EpisodeReference) string {
	if titleName == "" {
		return "Media"
	}

	// For series with episode info
	if episode != nil && episode.SeasonNumber > 0 && episode.EpisodeNumber > 0 {
		return fmt.Sprintf("%s S%02dE%02d", titleName, episode.SeasonNumber, episode.EpisodeNumber)
	}

	// For movies with year
	if year > 0 {
		return fmt.Sprintf("%s (%d)", titleName, year)
	}

	return titleName
}

// Options handles CORS preflight
func (h *PrequeueHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// runPrequeueWorker runs the prequeue background task
func (h *PrequeueHandler) runPrequeueWorker(prequeueID, titleName, imdbID, mediaType string, year int, userID, clientID string, targetEpisode *models.EpisodeReference, startOffset float64) {
	// Create cancellable context
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Store cancel func for potential cancellation
	h.store.SetCancelFunc(prequeueID, cancel)

	log.Printf("[prequeue] Starting worker for %s (title=%q)", prequeueID, titleName)

	// Update status to searching
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusSearching
	})

	// Build search query using the title name (like the frontend does)
	query := h.buildSearchQuery(titleName, mediaType, targetEpisode)
	if query == "" {
		h.failPrequeue(prequeueID, "failed to build search query")
		return
	}

	log.Printf("[prequeue] Searching with query: %q", query)

	// Create episode resolver for TV shows to enable accurate pack size filtering
	var episodeResolver *filter.SeriesEpisodeResolver
	if mediaType == "series" && h.metadataSvc != nil {
		episodeResolver = h.createEpisodeResolver(ctx, titleName, year, imdbID)
		if episodeResolver != nil {
			log.Printf("[prequeue] Episode resolver created: %d total episodes, %d seasons", episodeResolver.TotalEpisodes, len(episodeResolver.SeasonEpisodeCounts))
		}
	}

	// Search for results (match manual selection limit for consistent fallback coverage)
	results, err := h.indexerSvc.Search(ctx, indexer.SearchOptions{
		Query:           query,
		MaxResults:      50,
		MediaType:       mediaType,
		IMDBID:          imdbID,
		Year:            year,
		UserID:          userID,
		ClientID:        clientID,
		EpisodeResolver: episodeResolver,
	})
	if err != nil {
		log.Printf("[prequeue] Search failed: %v", err)
		h.failPrequeue(prequeueID, "search failed: "+err.Error())
		return
	}

	if len(results) == 0 {
		h.failPrequeue(prequeueID, "no results found")
		return
	}

	log.Printf("[prequeue] Found %d results", len(results))

	// Update status to resolving
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusResolving
	})

	// Load filter settings for DV profile compatibility checking
	// Priority: client settings > user settings > global settings > default
	var hdrDVPolicy models.HDRDVPolicy

	// Layer 1: Start with global settings
	if h.configManager != nil {
		globalSettings, err := h.configManager.Load()
		if err == nil {
			hdrDVPolicy = models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy)
		}
	}

	// Layer 2: User settings override global
	if h.userSettingsSvc != nil {
		userSettings, err := h.userSettingsSvc.Get(userID)
		if err == nil && userSettings != nil && userSettings.Filtering.HDRDVPolicy != "" {
			hdrDVPolicy = userSettings.Filtering.HDRDVPolicy
		}
	}

	// Layer 3: Client/device settings override user
	if clientID != "" && h.clientSettingsSvc != nil {
		clientSettings, err := h.clientSettingsSvc.Get(clientID)
		if err == nil && clientSettings != nil && clientSettings.HDRDVPolicy != nil {
			hdrDVPolicy = *clientSettings.HDRDVPolicy
			log.Printf("[prequeue] Using client-specific HDR/DV policy: %s", hdrDVPolicy)
		}
	}

	// Default to allowing all content
	if hdrDVPolicy == "" {
		hdrDVPolicy = models.HDRDVPolicyIncludeHDRDV
	}
	needsDVCheck := hdrDVPolicy == models.HDRDVPolicyIncludeHDR
	log.Printf("[prequeue] HDR/DV policy: %s, needsDVCheck: %v", hdrDVPolicy, needsDVCheck)

	// Try to resolve the best result using parallel health checks for usenet
	var resolution *models.PlaybackResolution
	var lastErr error

	// Only health check usenet results within the top N results (not top N usenet results)
	// This way if top results are all debrid, we skip health checks entirely
	const parallelHealthCheckLimit = 10
	topResults := results
	if len(topResults) > parallelHealthCheckLimit {
		topResults = results[:parallelHealthCheckLimit]
	}

	// Count usenet vs debrid in top results
	usenetInTop := 0
	for _, r := range topResults {
		if r.ServiceType != models.ServiceTypeDebrid {
			usenetInTop++
		}
	}
	log.Printf("[prequeue] Top %d results: %d usenet, %d debrid", len(topResults), usenetInTop, len(topResults)-usenetInTop)

	// Run parallel health check on top results (filters for usenet internally)
	var healthResultMap map[string]playback.HealthCheckResult
	if usenetInTop > 0 {
		healthResults := h.playbackSvc.ParallelHealthCheck(ctx, topResults, parallelHealthCheckLimit)
		healthResultMap = make(map[string]playback.HealthCheckResult)
		for _, hr := range healthResults {
			// Use download URL as key since it's unique per result
			key := hr.Candidate.DownloadURL
			if key == "" {
				key = hr.Candidate.Link
			}
			healthResultMap[key] = hr
		}
	}

	// Cached probe result for DV checking (reused later for track selection)
	var cachedProbeResult *VideoFullResult

	// Try to resolve top results in priority order
	for i, result := range topResults {
		select {
		case <-ctx.Done():
			h.failPrequeue(prequeueID, "cancelled")
			return
		default:
		}

		if result.ServiceType == models.ServiceTypeDebrid {
			// Debrid: resolve directly (no health check needed)
			resolution, lastErr = h.playbackSvc.Resolve(ctx, result)
			if lastErr == nil && resolution != nil && resolution.WebDAVPath != "" {
				log.Printf("[prequeue] Resolved debrid result [%d]: %s -> %s", i, result.Title, resolution.WebDAVPath)

				// Check DV profile compatibility if needed (only for "hdr" policy)
				if needsDVCheck && h.fullProber != nil {
					probeResult, probeErr := h.fullProber.ProbeVideoFull(ctx, resolution.WebDAVPath)
					if probeErr != nil {
						log.Printf("[prequeue] Probe failed for %s: %v, trying next result", result.Title, probeErr)
						resolution = nil
						lastErr = probeErr
						continue
					}
					// Check for DV profile 5 (no HDR fallback layer)
					if probeResult != nil && probeResult.HasDolbyVision {
						dvProfileNum := parseDVProfile(probeResult.DolbyVisionProfile)
						if dvProfileNum == 5 {
							log.Printf("[prequeue] DV profile %s (profile %d) incompatible with 'hdr' policy (no HDR fallback), trying next result",
								probeResult.DolbyVisionProfile, dvProfileNum)
							resolution = nil
							lastErr = fmt.Errorf("DV_PROFILE_INCOMPATIBLE: profile 5 has no HDR fallback layer")
							continue
						}
						log.Printf("[prequeue] DV profile %s (profile %d) compatible with 'hdr' policy (has HDR fallback)", probeResult.DolbyVisionProfile, dvProfileNum)
					}
					cachedProbeResult = probeResult
				}
				break
			}
			log.Printf("[prequeue] Failed to resolve debrid %s: %v", result.Title, lastErr)
			resolution = nil
		} else {
			// Usenet: use health check result
			key := result.DownloadURL
			if key == "" {
				key = result.Link
			}
			hr, found := healthResultMap[key]
			if !found {
				log.Printf("[prequeue] No health result for usenet %s, skipping", result.Title)
				continue
			}
			if !hr.Healthy {
				log.Printf("[prequeue] Usenet %s unhealthy, skipping", result.Title)
				continue
			}

			resolution, lastErr = h.playbackSvc.ResolveWithHealthResult(ctx, hr)
			if lastErr == nil && resolution != nil && resolution.WebDAVPath != "" {
				log.Printf("[prequeue] Resolved usenet result [%d]: %s -> %s", i, result.Title, resolution.WebDAVPath)

				// Check DV profile compatibility if needed (only for "hdr" policy)
				if needsDVCheck && h.fullProber != nil {
					probeResult, probeErr := h.fullProber.ProbeVideoFull(ctx, resolution.WebDAVPath)
					if probeErr != nil {
						log.Printf("[prequeue] Probe failed for %s: %v, trying next result", result.Title, probeErr)
						resolution = nil
						lastErr = probeErr
						continue
					}
					// Check for DV profile 5 (no HDR fallback layer)
					if probeResult != nil && probeResult.HasDolbyVision {
						dvProfileNum := parseDVProfile(probeResult.DolbyVisionProfile)
						if dvProfileNum == 5 {
							log.Printf("[prequeue] DV profile %s (profile %d) incompatible with 'hdr' policy (no HDR fallback), trying next result",
								probeResult.DolbyVisionProfile, dvProfileNum)
							resolution = nil
							lastErr = fmt.Errorf("DV_PROFILE_INCOMPATIBLE: profile 5 has no HDR fallback layer")
							continue
						}
						log.Printf("[prequeue] DV profile %s (profile %d) compatible with 'hdr' policy (has HDR fallback)", probeResult.DolbyVisionProfile, dvProfileNum)
					}
					cachedProbeResult = probeResult
				}
				break
			}
			log.Printf("[prequeue] Failed to resolve usenet %s: %v", result.Title, lastErr)
			resolution = nil
		}
	}

	// Fall back to sequential checking for remaining results beyond top N
	if resolution == nil && len(results) > parallelHealthCheckLimit {
		log.Printf("[prequeue] Falling back to sequential resolution for results %d-%d", parallelHealthCheckLimit, len(results)-1)
		for _, result := range results[parallelHealthCheckLimit:] {
			select {
			case <-ctx.Done():
				h.failPrequeue(prequeueID, "cancelled")
				return
			default:
			}

			resolution, lastErr = h.playbackSvc.Resolve(ctx, result)
			if lastErr == nil && resolution != nil && resolution.WebDAVPath != "" {
				log.Printf("[prequeue] Resolved result: %s -> %s", result.Title, resolution.WebDAVPath)

				// Check DV profile compatibility if needed (only for "hdr" policy)
				if needsDVCheck && h.fullProber != nil {
					probeResult, probeErr := h.fullProber.ProbeVideoFull(ctx, resolution.WebDAVPath)
					if probeErr != nil {
						log.Printf("[prequeue] Probe failed for %s: %v, trying next result", result.Title, probeErr)
						resolution = nil
						lastErr = probeErr
						continue
					}
					// Check for DV profile 5 (no HDR fallback layer)
					if probeResult != nil && probeResult.HasDolbyVision {
						dvProfileNum := parseDVProfile(probeResult.DolbyVisionProfile)
						if dvProfileNum == 5 {
							log.Printf("[prequeue] DV profile %s (profile %d) incompatible with 'hdr' policy (no HDR fallback), trying next result",
								probeResult.DolbyVisionProfile, dvProfileNum)
							resolution = nil
							lastErr = fmt.Errorf("DV_PROFILE_INCOMPATIBLE: profile 5 has no HDR fallback layer")
							continue
						}
						log.Printf("[prequeue] DV profile %s (profile %d) compatible with 'hdr' policy (has HDR fallback)", probeResult.DolbyVisionProfile, dvProfileNum)
					}
					cachedProbeResult = probeResult
				}
				break
			}
			log.Printf("[prequeue] Failed to resolve %s: %v", result.Title, lastErr)
			resolution = nil
		}
	}

	if resolution == nil {
		errMsg := "all results failed to resolve"
		if lastErr != nil {
			errMsg = lastErr.Error()
		}
		h.failPrequeue(prequeueID, errMsg)
		return
	}

	// Update with resolution
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusProbing
		e.StreamPath = resolution.WebDAVPath
		e.FileSize = resolution.FileSize
		e.HealthStatus = resolution.HealthStatus
	})

	// Select audio/subtitle tracks based on user preferences
	selectedAudioTrack := -1
	selectedSubtitleTrack := -1

	if h.metadataProber != nil && h.userSettingsSvc != nil {
		// Build defaults from global settings
		var defaults models.UserSettings
		if h.configManager != nil {
			globalSettings, err := h.configManager.Load()
			if err != nil {
				log.Printf("[prequeue] Failed to load global settings: %v", err)
			} else {
				defaults = models.UserSettings{
					Playback: models.PlaybackSettings{
						PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
						PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
						PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
					},
				}
			}
		}

		// Get user settings with global defaults as fallback
		userSettings, err := h.userSettingsSvc.GetWithDefaults(userID, defaults)
		if err != nil {
			log.Printf("[prequeue] Failed to get user settings (non-fatal): %v", err)
		}

		// Use combined prober if available (single ffprobe call), otherwise fall back to separate probes
		var audioStreams []AudioStreamInfo
		var subtitleStreams []SubtitleStreamInfo
		var hasDV, hasHDR10 bool
		var hasTrueHD, hasCompatibleAudio bool
		var dvProfile string

		// Reuse cached probe result if we already probed during DV check
		var duration float64
		if cachedProbeResult != nil {
			audioStreams = cachedProbeResult.AudioStreams
			subtitleStreams = cachedProbeResult.SubtitleStreams
			hasDV = cachedProbeResult.HasDolbyVision
			hasHDR10 = cachedProbeResult.HasHDR10
			dvProfile = cachedProbeResult.DolbyVisionProfile
			hasTrueHD = cachedProbeResult.HasTrueHD
			hasCompatibleAudio = cachedProbeResult.HasCompatibleAudio
			duration = cachedProbeResult.Duration
			log.Printf("[prequeue] Using cached probe result: DV=%v HDR10=%v TrueHD=%v compatAudio=%v audioStreams=%d subStreams=%d duration=%.2fs",
				hasDV, hasHDR10, hasTrueHD, hasCompatibleAudio, len(audioStreams), len(subtitleStreams), duration)
		} else if h.fullProber != nil {
			// Single ffprobe call for both HDR detection and track metadata
			fullResult, err := h.fullProber.ProbeVideoFull(ctx, resolution.WebDAVPath)
			if err != nil {
				log.Printf("[prequeue] Unified probe failed (non-fatal): %v", err)
			} else if fullResult != nil {
				audioStreams = fullResult.AudioStreams
				subtitleStreams = fullResult.SubtitleStreams
				hasDV = fullResult.HasDolbyVision
				hasHDR10 = fullResult.HasHDR10
				dvProfile = fullResult.DolbyVisionProfile
				hasTrueHD = fullResult.HasTrueHD
				hasCompatibleAudio = fullResult.HasCompatibleAudio
				duration = fullResult.Duration
				log.Printf("[prequeue] Unified probe: DV=%v HDR10=%v TrueHD=%v compatAudio=%v audioStreams=%d subStreams=%d duration=%.2fs",
					hasDV, hasHDR10, hasTrueHD, hasCompatibleAudio, len(audioStreams), len(subtitleStreams), duration)
			}
		} else {
			// Fallback: separate probes (legacy path)
			if h.metadataProber != nil {
				metadata, err := h.metadataProber.ProbeVideoMetadata(ctx, resolution.WebDAVPath)
				if err != nil {
					log.Printf("[prequeue] Metadata probe failed (non-fatal): %v", err)
				} else if metadata != nil {
					audioStreams = metadata.AudioStreams
					subtitleStreams = metadata.SubtitleStreams
				}
			}
			if h.videoProber != nil {
				probeResult, err := h.videoProber.ProbeVideoPath(ctx, resolution.WebDAVPath)
				if err != nil {
					log.Printf("[prequeue] Video probe failed (non-fatal): %v", err)
				} else if probeResult != nil {
					hasDV = probeResult.HasDolbyVision
					hasHDR10 = probeResult.HasHDR10
					dvProfile = probeResult.DolbyVisionProfile
				}
			}
		}

		// Process track selection using probe results
		if len(audioStreams) > 0 || len(subtitleStreams) > 0 {
			log.Printf("[prequeue] User track preferences: audioLang=%q, subLang=%q, subMode=%q",
				userSettings.Playback.PreferredAudioLanguage,
				userSettings.Playback.PreferredSubtitleLanguage,
				userSettings.Playback.PreferredSubtitleMode)

			for i, stream := range audioStreams {
				log.Printf("[prequeue] Audio stream[%d]: index=%d codec=%q lang=%q title=%q", i, stream.Index, stream.Codec, stream.Language, stream.Title)
			}

			if userSettings.Playback.PreferredAudioLanguage != "" {
				selectedAudioTrack = h.findAudioTrackByLanguage(audioStreams, userSettings.Playback.PreferredAudioLanguage)
				if selectedAudioTrack >= 0 {
					log.Printf("[prequeue] Selected audio track %d for language %q", selectedAudioTrack, userSettings.Playback.PreferredAudioLanguage)
				} else {
					log.Printf("[prequeue] No audio track found matching language %q", userSettings.Playback.PreferredAudioLanguage)
				}
			} else {
				log.Printf("[prequeue] No preferred audio language set in user settings")
			}

			subMode := userSettings.Playback.PreferredSubtitleMode
			subLang := userSettings.Playback.PreferredSubtitleLanguage
			if subMode != "off" && subMode != "" {
				selectedSubtitleTrack = h.findSubtitleTrackByPreference(subtitleStreams, subLang, subMode)
				if selectedSubtitleTrack >= 0 {
					log.Printf("[prequeue] Selected subtitle track %d for language %q (mode: %s)", selectedSubtitleTrack, subLang, subMode)
				}
			}
		}

		// Store selected tracks and duration
		h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
			e.SelectedAudioTrack = selectedAudioTrack
			e.SelectedSubtitleTrack = selectedSubtitleTrack
			if duration > 0 {
				e.Duration = duration
			}
		})

		// Handle HDR content or incompatible audio (TrueHD, DTS, etc.)
		// When TrueHD/DTS is present, we need transmux to exclude those tracks even if compatible audio exists
		// This is because the player may still encounter the incompatible codec in the container
		needsAudioTranscode := hasTrueHD // Always transcode if TrueHD/DTS present
		needsHLS := hasDV || hasHDR10 || needsAudioTranscode
		if needsHLS {
			h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
				e.HasDolbyVision = hasDV
				e.HasHDR10 = hasHDR10
				e.DolbyVisionProfile = dvProfile
				e.NeedsAudioTranscode = needsAudioTranscode
			})

			reason := "HDR"
			if hasTrueHD {
				if hasCompatibleAudio {
					reason = "TrueHD/DTS present (using compatible track, excluding TrueHD)"
				} else {
					reason = "TrueHD/DTS audio transcoding to AAC"
				}
			}
			log.Printf("[prequeue] Creating HLS session for: %s", reason)

			// Create HLS session for HDR content or incompatible audio
			if h.hlsCreator != nil {
				hlsResult, err := h.hlsCreator.CreateHLSSession(
					ctx,
					resolution.WebDAVPath,
					hasDV,
					dvProfile,
					hasHDR10,
					selectedAudioTrack,
					selectedSubtitleTrack,
				)
				if err != nil {
					log.Printf("[prequeue] HLS session creation failed (non-fatal): %v", err)
				} else if hlsResult != nil {
					h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
						e.HLSSessionID = hlsResult.SessionID
						e.HLSPlaylistURL = hlsResult.PlaylistURL
					})
					log.Printf("[prequeue] Created HLS session: %s", hlsResult.SessionID)
				}
			}
		} else if len(subtitleStreams) > 0 {
			// Non-HLS path (SDR content): Store subtitle track info for lazy extraction
			// Extraction will be triggered by frontend with correct startOffset when user plays
			log.Printf("[prequeue] Storing %d subtitle tracks for lazy extraction (SDR path)", len(subtitleStreams))

			// Convert to SubtitleTrackInfo format
			// Index = relative (0, 1, 2) for frontend track selection
			// AbsoluteIndex = ffprobe stream index (13, 14, 15) for ffmpeg -map
			tracks := make([]playback.SubtitleTrackInfo, len(subtitleStreams))
			for i, s := range subtitleStreams {
				tracks[i] = playback.SubtitleTrackInfo{
					Index:         i,       // Relative index for frontend
					AbsoluteIndex: s.Index, // Absolute ffprobe stream index for ffmpeg -map
					Language:      s.Language,
					Title:         s.Title,
					Codec:         s.Codec,
					Forced:        s.IsForced,
				}
			}

			h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
				e.SubtitleTracks = tracks
			})
			log.Printf("[prequeue] Stored %d subtitle tracks for lazy extraction", len(tracks))
		}
	}

	// Mark as ready
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
	})

	log.Printf("[prequeue] Prequeue %s is ready", prequeueID)
}

// failPrequeue marks a prequeue as failed
func (h *PrequeueHandler) failPrequeue(prequeueID, errMsg string) {
	log.Printf("[prequeue] Prequeue %s failed: %s", prequeueID, errMsg)
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusFailed
		e.Error = errMsg
	})
}

// StartSubtitlesRequest is the request body for starting subtitle extraction
type StartSubtitlesRequest struct {
	StartOffset float64 `json:"startOffset"` // Resume position in seconds
}

// StartSubtitlesResponse is the response with subtitle session info
type StartSubtitlesResponse struct {
	SubtitleSessions map[int]*models.SubtitleSessionInfo `json:"subtitleSessions"`
}

// StartSubtitles starts subtitle extraction for a prequeue with the given offset
// This is called when the user clicks play, after they've chosen resume/start position
func (h *PrequeueHandler) StartSubtitles(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract prequeue ID from URL path using gorilla mux
	vars := mux.Vars(r)
	prequeueID := strings.TrimSpace(vars["prequeueID"])
	if prequeueID == "" {
		http.Error(w, "missing prequeue ID", http.StatusBadRequest)
		return
	}

	// Parse request body
	var req StartSubtitlesRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}

	// Also check query param for startOffset
	if offsetStr := r.URL.Query().Get("startOffset"); offsetStr != "" {
		if offset, err := strconv.ParseFloat(offsetStr, 64); err == nil {
			req.StartOffset = offset
		}
	}

	log.Printf("[prequeue] StartSubtitles called for %s with startOffset=%.3f", prequeueID, req.StartOffset)

	// Get the prequeue entry
	entry, exists := h.store.Get(prequeueID)
	if !exists {
		http.Error(w, "prequeue not found", http.StatusNotFound)
		return
	}

	// Check if prequeue is ready
	if entry.Status != playback.PrequeueStatusReady {
		http.Error(w, "prequeue not ready", http.StatusConflict)
		return
	}

	// Check if we have subtitle tracks to extract
	if len(entry.SubtitleTracks) == 0 {
		// No subtitle tracks - return empty response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StartSubtitlesResponse{
			SubtitleSessions: make(map[int]*models.SubtitleSessionInfo),
		})
		return
	}

	// Check if subtitles already extracted (sessions exist)
	if len(entry.SubtitleSessions) > 0 {
		log.Printf("[prequeue] Subtitles already extracted for %s, returning existing sessions", prequeueID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StartSubtitlesResponse{
			SubtitleSessions: entry.SubtitleSessions,
		})
		return
	}

	// Check if we have the subtitle extractor
	if h.subtitleExtractor == nil {
		http.Error(w, "subtitle extraction not available", http.StatusServiceUnavailable)
		return
	}

	// Convert playback.SubtitleTrackInfo to handlers.SubtitleTrackInfo
	tracks := make([]SubtitleTrackInfo, len(entry.SubtitleTracks))
	for i, t := range entry.SubtitleTracks {
		tracks[i] = SubtitleTrackInfo{
			Index:         t.Index,
			AbsoluteIndex: t.AbsoluteIndex,
			Language:      t.Language,
			Title:         t.Title,
			Codec:         t.Codec,
			Forced:        t.Forced,
		}
	}

	// Start extraction with the provided offset
	log.Printf("[prequeue] Starting subtitle extraction for %s with %d tracks at offset %.3f",
		prequeueID, len(tracks), req.StartOffset)
	sessions := h.subtitleExtractor.StartPreExtraction(r.Context(), entry.StreamPath, tracks, req.StartOffset)

	// Convert sessions to SubtitleSessionInfo
	sessionInfos := make(map[int]*models.SubtitleSessionInfo)
	for relativeIdx, session := range sessions {
		if relativeIdx < 0 || relativeIdx >= len(entry.SubtitleTracks) {
			continue
		}
		track := entry.SubtitleTracks[relativeIdx]
		session.mu.Lock()
		firstCueTime := session.FirstCueTime
		session.mu.Unlock()
		sessionInfos[relativeIdx] = &models.SubtitleSessionInfo{
			SessionID:    session.ID,
			VTTUrl:       "/api/video/subtitles/" + session.ID + "/subtitles.vtt",
			TrackIndex:   relativeIdx,
			Language:     track.Language,
			Title:        track.Title,
			Codec:        track.Codec,
			IsForced:     track.Forced,
			IsExtracting: !session.IsExtractionComplete(),
			FirstCueTime: firstCueTime,
		}
	}

	// Store the sessions in the prequeue entry
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.SubtitleSessions = sessionInfos
	})

	log.Printf("[prequeue] Started subtitle extraction for %d sessions", len(sessionInfos))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(StartSubtitlesResponse{
		SubtitleSessions: sessionInfos,
	})
}

// buildSearchQuery builds the search query for a title (same format as frontend)
func (h *PrequeueHandler) buildSearchQuery(titleName, mediaType string, targetEpisode *models.EpisodeReference) string {
	if strings.TrimSpace(titleName) == "" {
		return ""
	}

	// For series, append episode code (matching frontend buildEpisodeQuery format)
	if targetEpisode != nil && targetEpisode.SeasonNumber > 0 && targetEpisode.EpisodeNumber > 0 {
		return fmt.Sprintf("%s S%sE%s", titleName, padNumber(targetEpisode.SeasonNumber), padNumber(targetEpisode.EpisodeNumber))
	}

	// For movies, just use the title name
	return titleName
}

// padNumber pads a number to 2 digits
func padNumber(n int) string {
	return fmt.Sprintf("%02d", n)
}

// createEpisodeResolver fetches series metadata and creates an episode resolver
// for accurate pack size filtering
func (h *PrequeueHandler) createEpisodeResolver(ctx context.Context, titleName string, year int, imdbID string) *filter.SeriesEpisodeResolver {
	if h.metadataSvc == nil {
		return nil
	}

	// Build query using available identifiers
	query := models.SeriesDetailsQuery{
		Name: titleName,
		Year: year,
	}

	// Fetch series details from metadata service
	details, err := h.metadataSvc.SeriesDetails(ctx, query)
	if err != nil {
		log.Printf("[prequeue] Failed to get series details for episode resolver: %v", err)
		return nil
	}

	if details == nil || len(details.Seasons) == 0 {
		log.Printf("[prequeue] No season data available for episode resolver")
		return nil
	}

	// Build season -> episode count map
	seasonCounts := make(map[int]int)
	for _, season := range details.Seasons {
		// Skip specials (season 0) unless explicitly included
		if season.Number > 0 {
			// Use EpisodeCount if available, otherwise count episodes
			count := season.EpisodeCount
			if count == 0 {
				count = len(season.Episodes)
			}
			seasonCounts[season.Number] = count
		}
	}

	if len(seasonCounts) == 0 {
		log.Printf("[prequeue] No valid seasons found for episode resolver")
		return nil
	}

	return filter.NewSeriesEpisodeResolver(seasonCounts)
}

// compatibleAudioCodecs lists codecs that can be played without transcoding
var compatibleAudioCodecs = map[string]bool{
	"aac": true, "ac3": true, "eac3": true, "mp3": true,
}

// isIncompatibleAudioCodec returns true for codecs that need transcoding (TrueHD, DTS, etc.)
func isIncompatibleAudioCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	return c == "truehd" || c == "dts" || strings.HasPrefix(c, "dts-") ||
		c == "dts_hd" || c == "dtshd" || c == "mlp"
}

// isCommentaryTrack checks if an audio track is a commentary track based on its title
func isCommentaryTrack(title string) bool {
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	commentaryIndicators := []string{
		"commentary",
		"director's commentary",
		"directors commentary",
		"audio commentary",
		"cast commentary",
		"crew commentary",
		"isolated score",
		"music only",
		"score only",
	}
	for _, indicator := range commentaryIndicators {
		if strings.Contains(lowerTitle, indicator) {
			return true
		}
	}
	return false
}

// findAudioTrackByLanguage finds an audio track matching the preferred language
// Prefers compatible audio codecs (AAC, AC3, etc.) over TrueHD/DTS when multiple tracks exist
// Skips commentary tracks unless they are the only option
func (h *PrequeueHandler) findAudioTrackByLanguage(streams []AudioStreamInfo, preferredLanguage string) int {
	if preferredLanguage == "" || len(streams) == 0 {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Helper to check if language matches
	matchesLanguage := func(stream AudioStreamInfo) bool {
		language := strings.ToLower(strings.TrimSpace(stream.Language))
		title := strings.ToLower(strings.TrimSpace(stream.Title))
		// Exact match
		if language == normalizedPref || title == normalizedPref {
			return true
		}
		// Partial match (skip empty strings to avoid false positives)
		if language != "" && (strings.Contains(language, normalizedPref) || strings.Contains(normalizedPref, language)) {
			return true
		}
		if title != "" && (strings.Contains(title, normalizedPref) || strings.Contains(normalizedPref, title)) {
			return true
		}
		return false
	}

	// First pass: find compatible codec (AAC, AC3, etc.) matching language, skipping commentary tracks
	for _, stream := range streams {
		if matchesLanguage(stream) && compatibleAudioCodecs[strings.ToLower(stream.Codec)] && !isCommentaryTrack(stream.Title) {
			log.Printf("[prequeue] Preferred compatible audio track %d (%s) for language %q",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Second pass: find any track matching language (even TrueHD/DTS), skipping commentary
	for _, stream := range streams {
		if matchesLanguage(stream) && !isCommentaryTrack(stream.Title) {
			if isIncompatibleAudioCodec(stream.Codec) {
				log.Printf("[prequeue] Selected incompatible audio track %d (%s) for language %q - will need HLS transcoding",
					stream.Index, stream.Codec, preferredLanguage)
			}
			return stream.Index
		}
	}

	// Third pass: fallback to compatible codec including commentary if nothing else matches
	for _, stream := range streams {
		if matchesLanguage(stream) && compatibleAudioCodecs[strings.ToLower(stream.Codec)] {
			log.Printf("[prequeue] Fallback to compatible audio track %d (%s, commentary) for language %q",
				stream.Index, stream.Codec, preferredLanguage)
			return stream.Index
		}
	}

	// Fourth pass: any matching track including commentary
	for _, stream := range streams {
		if matchesLanguage(stream) {
			if isIncompatibleAudioCodec(stream.Codec) {
				log.Printf("[prequeue] Fallback to incompatible audio track %d (%s, commentary) for language %q - will need HLS transcoding",
					stream.Index, stream.Codec, preferredLanguage)
			}
			return stream.Index
		}
	}

	return -1
}

// findSubtitleTrackByPreference finds a subtitle track matching the preferences
func (h *PrequeueHandler) findSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode string) int {
	if len(streams) == 0 || mode == "off" {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Filter by mode
	var candidateStreams []SubtitleStreamInfo
	if mode == "forced-only" {
		for _, s := range streams {
			if s.IsForced {
				candidateStreams = append(candidateStreams, s)
			}
		}
		if len(candidateStreams) == 0 {
			// No forced subtitles available
			return -1
		}
	} else {
		candidateStreams = streams
	}

	// If language preference is set, try to find a match
	if normalizedPref != "" {
		// Try exact match
		for _, stream := range candidateStreams {
			language := strings.ToLower(strings.TrimSpace(stream.Language))
			title := strings.ToLower(strings.TrimSpace(stream.Title))

			if language == normalizedPref || title == normalizedPref {
				return stream.Index
			}
		}

		// Try partial match (skip empty strings to avoid false positives)
		for _, stream := range candidateStreams {
			language := strings.ToLower(strings.TrimSpace(stream.Language))
			title := strings.ToLower(strings.TrimSpace(stream.Title))

			if language != "" && (strings.Contains(language, normalizedPref) || strings.Contains(normalizedPref, language)) {
				return stream.Index
			}
			if title != "" && (strings.Contains(title, normalizedPref) || strings.Contains(normalizedPref, title)) {
				return stream.Index
			}
		}
	}

	// If mode is 'on' and no language match, return first available
	if mode == "on" && len(candidateStreams) > 0 {
		return candidateStreams[0].Index
	}

	return -1
}

// parseDVProfile extracts the profile number from a Dolby Vision profile string.
// Format: "dvhe.05.06" or "dav1.05.06" where second segment is the profile number.
// Returns 0 if format is not recognized.
func parseDVProfile(dvProfile string) int {
	parts := strings.Split(dvProfile, ".")
	if len(parts) >= 2 {
		profile, _ := strconv.Atoi(parts[1])
		return profile
	}
	return 0
}

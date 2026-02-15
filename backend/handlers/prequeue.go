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
	"novastream/internal/mediaresolve"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/indexer"
	"novastream/services/playback"
	user_settings "novastream/services/user_settings"
	content_preferences "novastream/services/content_preferences"
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
	userSettingsSvc         *user_settings.Service
	contentPreferencesSvc   *content_preferences.Service
	clientSettingsSvc       ClientSettingsProvider
	configManager           *config.Manager
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
	// Video codec detection
	VideoCodec string // e.g., "h264", "hevc", "mpeg4" - used to detect incompatible codecs
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
	CreateHLSSession(ctx context.Context, path string, hasDV bool, dvProfile string, hasHDR bool, audioTrackIndex int, subtitleTrackIndex int, profileID string, startOffset float64, prequeueType string) (*HLSSessionResult, error)
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

// SetContentPreferencesService sets the content preferences service for per-content language preferences
func (h *PrequeueHandler) SetContentPreferencesService(svc *content_preferences.Service) {
	h.contentPreferencesSvc = svc
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
				SeasonNumber:          req.SeasonNumber,
				EpisodeNumber:         req.EpisodeNumber,
				AbsoluteEpisodeNumber: req.AbsoluteEpisodeNumber,
			}
			if req.AbsoluteEpisodeNumber > 0 {
				log.Printf("[prequeue] Using explicit episode S%02dE%02d (abs: %d)", req.SeasonNumber, req.EpisodeNumber, req.AbsoluteEpisodeNumber)
			} else {
				log.Printf("[prequeue] Using explicit episode S%02dE%02d", req.SeasonNumber, req.EpisodeNumber)
			}
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
	entry, _ := h.store.Create(req.TitleID, titleName, req.UserID, mediaType, req.Year, targetEpisode, req.Reason)

	// Start background worker with all the info needed for search
	go h.runPrequeueWorker(entry.ID, req.TitleID, titleName, req.ImdbID, mediaType, req.Year, req.UserID, clientID, targetEpisode, req.StartOffset)

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
func (h *PrequeueHandler) runPrequeueWorker(prequeueID, titleID, titleName, imdbID, mediaType string, year int, userID, clientID string, targetEpisode *models.EpisodeReference, startOffset float64) {
	// Create cancellable context
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Store cancel func for potential cancellation
	h.store.SetCancelFunc(prequeueID, cancel)

	workerStart := time.Now()
	log.Printf("[prequeue] TIMING: worker started for %s (title=%q)", prequeueID, titleName)

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

	log.Printf("[prequeue] TIMING: search starting with query: %q (elapsed: %v)", query, time.Since(workerStart))

	// Create episode resolver for TV shows to enable accurate pack size filtering
	// Also lookup absolute episode number, daily show info, and anime detection if not provided
	var episodeResolver *filter.SeriesEpisodeResolver
	var isDaily bool
	var isAnime bool
	var targetAirDate string
	if mediaType == "series" && h.metadataSvc != nil {
		seriesMeta := h.createEpisodeResolverAndLookupAbsoluteEp(ctx, titleID, titleName, year, imdbID, targetEpisode)
		episodeResolver = seriesMeta.EpisodeResolver
		targetEpisode = seriesMeta.TargetEpisode
		isDaily = seriesMeta.IsDaily
		isAnime = seriesMeta.IsAnime
		targetAirDate = seriesMeta.TargetAirDate
		if episodeResolver != nil {
			log.Printf("[prequeue] Episode resolver created: %d total episodes, %d seasons", episodeResolver.TotalEpisodes, len(episodeResolver.SeasonEpisodeCounts))
		}
		if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
			log.Printf("[prequeue] Target episode S%02dE%02d has absolute episode number: %d",
				targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber, targetEpisode.AbsoluteEpisodeNumber)
		}
	}

	// Search for results using split search (debrid and usenet in parallel)
	// This allows us to start resolving debrid results while usenet search continues
	searchOpts := indexer.SearchOptions{
		Query:           query,
		MaxResults:      50,
		MediaType:       mediaType,
		IMDBID:          imdbID,
		Year:            year,
		UserID:          userID,
		ClientID:        clientID,
		EpisodeResolver: episodeResolver,
		IsDaily:         isDaily,
		IsAnime:         isAnime,
		TargetAirDate:   targetAirDate,
	}
	// Pass absolute episode number for anime matching (if available)
	if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
		searchOpts.AbsoluteEpisodeNumber = targetEpisode.AbsoluteEpisodeNumber
	}

	// Load settings to get service priority
	settings, err := h.configManager.Load()
	if err != nil {
		h.failPrequeue(prequeueID, "failed to load settings: "+err.Error())
		return
	}
	servicePriority := settings.Streaming.ServicePriority
	log.Printf("[prequeue] TIMING: service priority is %q (elapsed: %v)", servicePriority, time.Since(workerStart))

	// Start split search - debrid and usenet run in parallel
	debridChan, usenetChan := h.indexerSvc.SearchSplit(ctx, searchOpts)

	// Wait for search results based on service priority
	var debridResults []models.NZBResult
	var usenetResults []models.NZBResult
	var debridErr, usenetErr error

	switch servicePriority {
	case config.StreamingServicePriorityUsenet:
		// Usenet priority: wait for usenet first, debrid as fallback
		log.Printf("[prequeue] TIMING: usenet priority - waiting for usenet results first")
		select {
		case <-ctx.Done():
			h.failPrequeue(prequeueID, "cancelled")
			return
		case usenetResult, ok := <-usenetChan:
			if ok {
				usenetResults = usenetResult.Results
				usenetErr = usenetResult.Err
				if usenetErr != nil {
					log.Printf("[prequeue] TIMING: usenet search failed: %v (elapsed: %v)", usenetErr, time.Since(workerStart))
				} else {
					log.Printf("[prequeue] TIMING: usenet search complete, got %d results (elapsed: %v)", len(usenetResults), time.Since(workerStart))
				}
			} else {
				log.Printf("[prequeue] TIMING: usenet channel closed (not configured), elapsed: %v", time.Since(workerStart))
			}
		}
		// If no usenet results, wait for debrid as fallback
		if len(usenetResults) == 0 {
			log.Printf("[prequeue] TIMING: no usenet results, waiting for debrid fallback (elapsed: %v)", time.Since(workerStart))
			select {
			case <-ctx.Done():
				h.failPrequeue(prequeueID, "cancelled")
				return
			case debridResult, ok := <-debridChan:
				if ok {
					debridResults = debridResult.Results
					debridErr = debridResult.Err
					if debridErr != nil {
						log.Printf("[prequeue] TIMING: debrid search failed: %v (elapsed: %v)", debridErr, time.Since(workerStart))
					} else {
						log.Printf("[prequeue] TIMING: debrid search complete, got %d results (elapsed: %v)", len(debridResults), time.Since(workerStart))
					}
				} else {
					log.Printf("[prequeue] TIMING: debrid channel closed (not configured), elapsed: %v", time.Since(workerStart))
				}
			}
		}

	case config.StreamingServicePriorityDebrid:
		// Debrid priority: wait for debrid first, usenet as fallback
		log.Printf("[prequeue] TIMING: debrid priority - waiting for debrid results first")
		select {
		case <-ctx.Done():
			h.failPrequeue(prequeueID, "cancelled")
			return
		case debridResult, ok := <-debridChan:
			if ok {
				debridResults = debridResult.Results
				debridErr = debridResult.Err
				if debridErr != nil {
					log.Printf("[prequeue] TIMING: debrid search failed: %v (elapsed: %v)", debridErr, time.Since(workerStart))
				} else {
					log.Printf("[prequeue] TIMING: debrid search complete, got %d results (elapsed: %v)", len(debridResults), time.Since(workerStart))
				}
			} else {
				log.Printf("[prequeue] TIMING: debrid channel closed (not configured), elapsed: %v", time.Since(workerStart))
			}
		}
		// If no debrid results, wait for usenet as fallback
		if len(debridResults) == 0 {
			log.Printf("[prequeue] TIMING: no debrid results, waiting for usenet fallback (elapsed: %v)", time.Since(workerStart))
			select {
			case <-ctx.Done():
				h.failPrequeue(prequeueID, "cancelled")
				return
			case usenetResult, ok := <-usenetChan:
				if ok {
					usenetResults = usenetResult.Results
					usenetErr = usenetResult.Err
					if usenetErr != nil {
						log.Printf("[prequeue] TIMING: usenet search failed: %v (elapsed: %v)", usenetErr, time.Since(workerStart))
					} else {
						log.Printf("[prequeue] TIMING: usenet search complete, got %d results (elapsed: %v)", len(usenetResults), time.Since(workerStart))
					}
				} else {
					log.Printf("[prequeue] TIMING: usenet channel closed (not configured), elapsed: %v", time.Since(workerStart))
				}
			}
		}

	default:
		// No priority (or unknown): race both, use whichever has results first
		log.Printf("[prequeue] TIMING: no priority - racing debrid and usenet")

		// Track which sources have been consumed (closed channels return immediately)
		debridConsumed := false
		usenetConsumed := false

		select {
		case <-ctx.Done():
			h.failPrequeue(prequeueID, "cancelled")
			return
		case debridResult, ok := <-debridChan:
			debridConsumed = true
			if ok {
				debridResults = debridResult.Results
				debridErr = debridResult.Err
				if debridErr != nil {
					log.Printf("[prequeue] TIMING: debrid search failed: %v (elapsed: %v)", debridErr, time.Since(workerStart))
				} else {
					log.Printf("[prequeue] TIMING: debrid search complete (race winner), got %d results (elapsed: %v)", len(debridResults), time.Since(workerStart))
				}
			} else {
				log.Printf("[prequeue] TIMING: debrid channel closed (not configured), elapsed: %v", time.Since(workerStart))
			}
		case usenetResult, ok := <-usenetChan:
			usenetConsumed = true
			if ok {
				usenetResults = usenetResult.Results
				usenetErr = usenetResult.Err
				if usenetErr != nil {
					log.Printf("[prequeue] TIMING: usenet search failed: %v (elapsed: %v)", usenetErr, time.Since(workerStart))
				} else {
					log.Printf("[prequeue] TIMING: usenet search complete (race winner), got %d results (elapsed: %v)", len(usenetResults), time.Since(workerStart))
				}
			} else {
				log.Printf("[prequeue] TIMING: usenet channel closed (not configured), elapsed: %v", time.Since(workerStart))
			}
		}

		// If the winner had no results, wait for the other source (only if not already consumed)
		if len(debridResults) == 0 && len(usenetResults) == 0 {
			log.Printf("[prequeue] TIMING: race winner had no results, waiting for other source (elapsed: %v)", time.Since(workerStart))

			// Only wait for sources that haven't been consumed yet
			if !debridConsumed && !usenetConsumed {
				// Both still available - race again
				select {
				case <-ctx.Done():
					h.failPrequeue(prequeueID, "cancelled")
					return
				case debridResult, ok := <-debridChan:
					if ok {
						debridResults = debridResult.Results
						debridErr = debridResult.Err
						if debridErr != nil {
							log.Printf("[prequeue] TIMING: debrid search failed: %v (elapsed: %v)", debridErr, time.Since(workerStart))
						} else {
							log.Printf("[prequeue] TIMING: debrid search complete, got %d results (elapsed: %v)", len(debridResults), time.Since(workerStart))
						}
					}
				case usenetResult, ok := <-usenetChan:
					if ok {
						usenetResults = usenetResult.Results
						usenetErr = usenetResult.Err
						if usenetErr != nil {
							log.Printf("[prequeue] TIMING: usenet search failed: %v (elapsed: %v)", usenetErr, time.Since(workerStart))
						} else {
							log.Printf("[prequeue] TIMING: usenet search complete, got %d results (elapsed: %v)", len(usenetResults), time.Since(workerStart))
						}
					}
				}
			} else if !debridConsumed {
				// Only debrid left to check
				select {
				case <-ctx.Done():
					h.failPrequeue(prequeueID, "cancelled")
					return
				case debridResult, ok := <-debridChan:
					if ok {
						debridResults = debridResult.Results
						debridErr = debridResult.Err
						if debridErr != nil {
							log.Printf("[prequeue] TIMING: debrid search failed: %v (elapsed: %v)", debridErr, time.Since(workerStart))
						} else {
							log.Printf("[prequeue] TIMING: debrid search complete, got %d results (elapsed: %v)", len(debridResults), time.Since(workerStart))
						}
					} else {
						log.Printf("[prequeue] TIMING: debrid channel closed (not configured), elapsed: %v", time.Since(workerStart))
					}
				}
			} else if !usenetConsumed {
				// Only usenet left to check
				select {
				case <-ctx.Done():
					h.failPrequeue(prequeueID, "cancelled")
					return
				case usenetResult, ok := <-usenetChan:
					if ok {
						usenetResults = usenetResult.Results
						usenetErr = usenetResult.Err
						if usenetErr != nil {
							log.Printf("[prequeue] TIMING: usenet search failed: %v (elapsed: %v)", usenetErr, time.Since(workerStart))
						} else {
							log.Printf("[prequeue] TIMING: usenet search complete, got %d results (elapsed: %v)", len(usenetResults), time.Since(workerStart))
						}
					} else {
						log.Printf("[prequeue] TIMING: usenet channel closed (not configured), elapsed: %v", time.Since(workerStart))
					}
				}
			}
			// else: both consumed, nothing more to wait for
		}
	}

	// Check if we have any results at all
	if len(debridResults) == 0 && len(usenetResults) == 0 {
		errMsg := "no results found"
		if debridErr != nil {
			errMsg = "debrid: " + debridErr.Error()
		}
		if usenetErr != nil {
			if debridErr != nil {
				errMsg += "; "
			}
			errMsg += "usenet: " + usenetErr.Error()
		}
		h.failPrequeue(prequeueID, errMsg)
		return
	}

	log.Printf("[prequeue] TIMING: search phase complete, debrid=%d usenet=%d (elapsed: %v)", len(debridResults), len(usenetResults), time.Since(workerStart))

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

	// Resolution phase - priority aware
	var resolution *models.PlaybackResolution
	var lastErr error
	var selectedResult *models.NZBResult // Track which result was successfully resolved

	resolveStart := time.Now()
	log.Printf("[prequeue] TIMING: starting resolution phase (debrid=%d, usenet=%d, priority=%s, elapsed: %v)",
		len(debridResults), len(usenetResults), servicePriority, time.Since(workerStart))

	// Cached probe result for DV checking (reused later for track selection)
	var cachedProbeResult *VideoFullResult

	// Helper to check episode match
	shouldSkipForEpisode := func(result models.NZBResult, i int) bool {
		if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
			parsedEp, hasEpisode := mediaresolve.ParseAbsoluteEpisodeNumber(result.Title)
			if hasEpisode {
				episodeCode := mediaresolve.EpisodeCode{Season: targetEpisode.SeasonNumber, Episode: targetEpisode.EpisodeNumber}
				matchesSXXEXX := mediaresolve.CandidateMatchesEpisode(result.Title, episodeCode)
				if !matchesSXXEXX && parsedEp != targetEpisode.AbsoluteEpisodeNumber {
					log.Printf("[prequeue] Skipping result [%d] - episode %d doesn't match target (S%02dE%02d/abs:%d): %s",
						i, parsedEp, targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber, targetEpisode.AbsoluteEpisodeNumber, result.Title)
					return true
				}
			}
		}
		return false
	}

	// Helper to check DV compatibility
	checkDVCompatibility := func(result models.NZBResult, res *models.PlaybackResolution) (*VideoFullResult, error) {
		if !needsDVCheck || h.fullProber == nil {
			return nil, nil
		}
		probeResult, probeErr := h.fullProber.ProbeVideoFull(ctx, res.WebDAVPath)
		if probeErr != nil {
			return nil, probeErr
		}
		if probeResult != nil {
			if err := ValidateDVProfile(probeResult.DolbyVisionProfile, "hdr", probeResult.HasDolbyVision); err != nil {
				return nil, err
			}
			if probeResult.HasDolbyVision {
				log.Printf("[prequeue] DV profile %s compatible with 'hdr' policy", probeResult.DolbyVisionProfile)
			}
		}
		return probeResult, nil
	}

	// Helper to try resolving debrid results
	tryDebridResults := func() bool {
		if len(debridResults) == 0 {
			return false
		}
		log.Printf("[prequeue] TIMING: trying debrid resolution (%d results)", len(debridResults))
		for i, result := range debridResults {
			select {
			case <-ctx.Done():
				return false
			default:
			}

			if shouldSkipForEpisode(result, i) {
				continue
			}

			resolution, lastErr = h.playbackSvc.Resolve(ctx, result)
			if lastErr == nil && resolution != nil && resolution.WebDAVPath != "" {
				log.Printf("[prequeue] Resolved debrid result [%d]: %s -> %s", i, result.Title, resolution.WebDAVPath)

				probeResult, probeErr := checkDVCompatibility(result, resolution)
				if probeErr != nil {
					log.Printf("[prequeue] DV check failed for %s: %v, trying next result", result.Title, probeErr)
					resolution = nil
					lastErr = probeErr
					continue
				}
				cachedProbeResult = probeResult
				selectedResult = &result
				log.Printf("[prequeue] TIMING: debrid resolved (resolve took: %v, total elapsed: %v)",
					time.Since(resolveStart), time.Since(workerStart))
				return true
			}
			log.Printf("[prequeue] Failed to resolve debrid %s: %v", result.Title, lastErr)
			resolution = nil
		}
		return false
	}

	// Helper to try resolving usenet results
	tryUsenetResults := func() bool {
		if len(usenetResults) == 0 {
			return false
		}

		// NOTE: Usenet health pre-checking disabled — was adding latency without much benefit.
		// To re-enable, uncomment the health check block below and switch back to ResolveWithHealthResult.
		//
		// healthCheckStart := time.Now()
		// log.Printf("[prequeue] TIMING: starting usenet health check for %d candidates", len(usenetResults))
		// healthResults := h.playbackSvc.ParallelHealthCheck(ctx, usenetResults, 10)
		//
		// select {
		// case <-ctx.Done():
		// 	return false
		// default:
		// }
		//
		// healthMap := make(map[string]playback.HealthCheckResult)
		// for _, hr := range healthResults {
		// 	key := hr.Candidate.DownloadURL
		// 	if key == "" {
		// 		key = hr.Candidate.Link
		// 	}
		// 	healthMap[key] = hr
		// }
		// log.Printf("[prequeue] TIMING: usenet health check complete (took: %v)", time.Since(healthCheckStart))

		// Try usenet results in priority order (no health pre-check)
		log.Printf("[prequeue] TIMING: trying usenet resolution (%d results, no health pre-check)", len(usenetResults))
		for i, result := range usenetResults {
			select {
			case <-ctx.Done():
				return false
			default:
			}

			if shouldSkipForEpisode(result, i) {
				continue
			}

			// Resolve directly without health pre-check
			resolution, lastErr = h.playbackSvc.Resolve(ctx, result)
			if lastErr == nil && resolution != nil && resolution.WebDAVPath != "" {
				log.Printf("[prequeue] Resolved usenet result [%d]: %s -> %s", i, result.Title, resolution.WebDAVPath)

				probeResult, probeErr := checkDVCompatibility(result, resolution)
				if probeErr != nil {
					log.Printf("[prequeue] DV check failed for %s: %v, trying next result", result.Title, probeErr)
					resolution = nil
					lastErr = probeErr
					continue
				}
				cachedProbeResult = probeResult
				selectedResult = &result
				log.Printf("[prequeue] TIMING: usenet resolved (resolve took: %v, total elapsed: %v)",
					time.Since(resolveStart), time.Since(workerStart))
				return true
			}
			log.Printf("[prequeue] Failed to resolve usenet %s: %v", result.Title, lastErr)
			resolution = nil
		}
		return false
	}

	// Resolve based on service priority
	switch servicePriority {
	case config.StreamingServicePriorityUsenet:
		// Usenet priority: try usenet first, debrid as fallback
		log.Printf("[prequeue] TIMING: usenet priority - trying usenet resolution first")
		if !tryUsenetResults() {
			log.Printf("[prequeue] TIMING: usenet resolution failed, trying debrid fallback")
			tryDebridResults()
		}

	case config.StreamingServicePriorityDebrid:
		// Debrid priority: try debrid first, usenet as fallback
		log.Printf("[prequeue] TIMING: debrid priority - trying debrid resolution first")
		if !tryDebridResults() {
			log.Printf("[prequeue] TIMING: debrid resolution failed, trying usenet fallback")
			tryUsenetResults()
		}

	default:
		// No priority: try debrid first (faster), then usenet
		// Note: In "none" priority, debrid is tried first because it doesn't require health checks
		log.Printf("[prequeue] TIMING: no priority - trying debrid first (faster path)")
		if !tryDebridResults() {
			log.Printf("[prequeue] TIMING: debrid resolution failed, trying usenet")
			tryUsenetResults()
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

	log.Printf("[prequeue] TIMING: resolution complete (resolve took: %v, total elapsed: %v)", time.Since(resolveStart), time.Since(workerStart))

	// Update with resolution
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusProbing
		e.StreamPath = resolution.WebDAVPath
		e.FileSize = resolution.FileSize
		e.HealthStatus = resolution.HealthStatus
		// Copy passthrough format data from AIOStreams results
		if selectedResult != nil && selectedResult.Attributes["passthrough_format"] == "true" {
			e.PassthroughName = selectedResult.Attributes["raw_name"]
			e.PassthroughDescription = selectedResult.Attributes["raw_description"]
		}
	})

	// Select audio/subtitle tracks based on user preferences
	selectedAudioTrack := -1
	selectedSubtitleTrack := -1
	probeStart := time.Now()

	if h.metadataProber != nil && h.userSettingsSvc != nil {
		log.Printf("[prequeue] TIMING: starting probe/track selection (elapsed: %v)", time.Since(workerStart))
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

		// Check for per-content language preferences (overrides user settings)
		if h.contentPreferencesSvc != nil {
			// Get the title ID from the prequeue entry
			if entry, ok := h.store.Get(prequeueID); ok && entry != nil {
				contentID := entry.TitleID
				if contentPref, err := h.contentPreferencesSvc.Get(userID, contentID); err == nil && contentPref != nil {
					log.Printf("[prequeue] Found per-content preference for %s: audioLang=%q, subLang=%q, subMode=%q",
						contentID, contentPref.AudioLanguage, contentPref.SubtitleLanguage, contentPref.SubtitleMode)
					// Override user settings with content-specific preferences
					if contentPref.AudioLanguage != "" {
						userSettings.Playback.PreferredAudioLanguage = contentPref.AudioLanguage
					}
					if contentPref.SubtitleLanguage != "" {
						userSettings.Playback.PreferredSubtitleLanguage = contentPref.SubtitleLanguage
					}
					if contentPref.SubtitleMode != "" {
						userSettings.Playback.PreferredSubtitleMode = contentPref.SubtitleMode
					}
				}
			}
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

		// Store audio/subtitle track info for UI display
		if len(audioStreams) > 0 || len(subtitleStreams) > 0 {
			// Convert audio streams to track info
			audioTracks := make([]playback.AudioTrackInfo, len(audioStreams))
			for i, s := range audioStreams {
				audioTracks[i] = playback.AudioTrackInfo{
					Index:    s.Index,
					Language: s.Language,
					Codec:    s.Codec,
					Title:    s.Title,
				}
			}

			// Convert subtitle streams to track info with bitmap detection
			bitmapCodecs := map[string]bool{
				"hdmv_pgs_subtitle": true,
				"dvd_subtitle":      true,
				"dvdsub":            true,
				"pgssub":            true,
			}
			subtitleTracks := make([]playback.SubtitleTrackInfo, len(subtitleStreams))
			for i, s := range subtitleStreams {
				codec := strings.ToLower(s.Codec)
				subtitleTracks[i] = playback.SubtitleTrackInfo{
					Index:         s.Index, // Absolute ffprobe stream index (matches selectedSubtitleTrack)
					AbsoluteIndex: s.Index, // Also stored here for clarity
					Language:      s.Language,
					Title:         s.Title,
					Codec:         s.Codec,
					Forced:        s.IsForced,
					IsBitmap:      bitmapCodecs[codec],
				}
			}

			h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
				e.AudioTracks = audioTracks
				e.SubtitleTracks = subtitleTracks
			})
			log.Printf("[prequeue] Stored %d audio tracks and %d subtitle tracks for UI display", len(audioTracks), len(subtitleTracks))
		}

		// Handle HDR content or incompatible audio (TrueHD, DTS, etc.)
		// When TrueHD/DTS is present, we need transmux to exclude those tracks even if compatible audio exists
		// This is because the player may still encounter the incompatible codec in the container
		needsAudioTranscode := hasTrueHD // Always transcode if TrueHD/DTS present
		// TESTING: Force HLS for all native content to test fMP4 with react-native-video
		needsHLS := true // hasDV || hasHDR10 || needsAudioTranscode
		if needsHLS {
			h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
				e.HasDolbyVision = hasDV
				e.HasHDR10 = hasHDR10
				e.DolbyVisionProfile = dvProfile
				e.NeedsAudioTranscode = needsAudioTranscode
			})

			reason := "SDR (testing fMP4)"
			if hasDV {
				reason = "Dolby Vision"
			} else if hasHDR10 {
				reason = "HDR10"
			} else if hasTrueHD {
				if hasCompatibleAudio {
					reason = "TrueHD/DTS present (using compatible track, excluding TrueHD)"
				} else {
					reason = "TrueHD/DTS audio transcoding to AAC"
				}
			}
			log.Printf("[prequeue] TIMING: probe complete (probe took: %v, total elapsed: %v)", time.Since(probeStart), time.Since(workerStart))
			log.Printf("[prequeue] Creating HLS session for: %s", reason)

			hlsStart := time.Now()
			// Create HLS session for HDR content or incompatible audio
			if h.hlsCreator != nil {
				// Get prequeue reason to determine HLS startup timeout behavior
				prequeueType := "details" // default
				if entry, ok := h.store.Get(prequeueID); ok && entry.Reason != "" {
					prequeueType = entry.Reason
				}

				hlsResult, err := h.hlsCreator.CreateHLSSession(
					ctx,
					resolution.WebDAVPath,
					hasDV,
					dvProfile,
					hasHDR10,
					selectedAudioTrack,
					selectedSubtitleTrack,
					userID,
					startOffset,
					prequeueType, // "details" or "next_episode" - affects startup timeout
				)
				if err != nil {
					log.Printf("[prequeue] HLS session creation failed (non-fatal): %v", err)
				} else if hlsResult != nil {
					h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
						e.HLSSessionID = hlsResult.SessionID
						e.HLSPlaylistURL = hlsResult.PlaylistURL
					})
					log.Printf("[prequeue] TIMING: HLS session created: %s (HLS took: %v, total elapsed: %v)", hlsResult.SessionID, time.Since(hlsStart), time.Since(workerStart))
				}
			}
		}
		// Note: Subtitle tracks for lazy extraction are already stored above for UI display
	}

	// Mark as ready
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
	})

	log.Printf("[prequeue] TIMING: Prequeue %s is ready (TOTAL: %v)", prequeueID, time.Since(workerStart))
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

	// Convert playback tracks to handler format and start extraction
	tracks := ConvertPlaybackTracksToHandler(entry.SubtitleTracks)

	log.Printf("[prequeue] Starting subtitle extraction for %s with %d tracks at offset %.3f",
		prequeueID, len(tracks), req.StartOffset)
	sessions := h.subtitleExtractor.StartPreExtraction(r.Context(), entry.StreamPath, tracks, req.StartOffset)

	// Convert sessions to SubtitleSessionInfo using the playback track metadata
	sessionInfos := ConvertSessionsFromPlaybackTracks(sessions, entry.SubtitleTracks)

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

// SeriesMetadataResult holds series-specific metadata needed for search and file matching
type SeriesMetadataResult struct {
	EpisodeResolver *filter.SeriesEpisodeResolver
	TargetEpisode   *models.EpisodeReference
	IsDaily         bool   // True for daily shows (talk shows, news) that use date-based naming
	TargetAirDate   string // Air date from TVDB in YYYY-MM-DD format
	IsAnime         bool   // True for anime content - requires waiting for Nyaa scraper
}

// createEpisodeResolverAndLookupAbsoluteEp fetches series metadata, creates an episode resolver,
// and looks up the absolute episode number for the target episode if not already set.
// Returns the episode resolver and an updated targetEpisode (with AbsoluteEpisodeNumber set if found).
func (h *PrequeueHandler) createEpisodeResolverAndLookupAbsoluteEp(ctx context.Context, titleID, titleName string, year int, imdbID string, targetEpisode *models.EpisodeReference) *SeriesMetadataResult {
	result := &SeriesMetadataResult{
		TargetEpisode: targetEpisode,
	}

	if h.metadataSvc == nil {
		return result
	}

	// Build query using available identifiers
	query := models.SeriesDetailsQuery{
		TitleID: titleID,
		Name:    titleName,
		Year:    year,
	}

	// Fetch series details from metadata service
	details, err := h.metadataSvc.SeriesDetails(ctx, query)
	if err != nil {
		log.Printf("[prequeue] Failed to get series details for episode resolver: %v", err)
		return result
	}

	if details == nil {
		log.Printf("[prequeue] No series details available")
		return result
	}

	// Check if this is a daily show from the metadata
	result.IsDaily = details.Title.IsDaily
	if result.IsDaily {
		log.Printf("[prequeue] Series %q is a daily show (talk show, news, etc.) - will use date-based matching", details.Title.Name)
	}

	// Check if this is anime content from the genres
	for _, genre := range details.Title.Genres {
		genreLower := strings.ToLower(genre)
		if genreLower == "animation" || genreLower == "anime" {
			result.IsAnime = true
			log.Printf("[prequeue] Series %q is anime (genre=%q) - will wait for all scrapers including Nyaa", details.Title.Name, genre)
			break
		}
	}

	if len(details.Seasons) == 0 {
		log.Printf("[prequeue] No season data available for episode resolver")
		return result
	}

	// Build season -> episode count map AND lookup absolute episode number and air date
	seasonCounts := make(map[int]int)
	var foundAbsoluteEp int
	var foundAirDate string
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

		// Look for the target episode's data if not already set
		if targetEpisode != nil && season.Number == targetEpisode.SeasonNumber {
			for _, ep := range season.Episodes {
				if ep.EpisodeNumber == targetEpisode.EpisodeNumber {
					// Get absolute episode number if not set
					if targetEpisode.AbsoluteEpisodeNumber == 0 && ep.AbsoluteEpisodeNumber > 0 {
						foundAbsoluteEp = ep.AbsoluteEpisodeNumber
						log.Printf("[prequeue] Found absolute episode number %d for S%02dE%02d from TVDB",
							foundAbsoluteEp, targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber)
					}
					// Get air date for daily shows (AiredDate field in SeriesEpisode)
					if ep.AiredDate != "" {
						foundAirDate = ep.AiredDate
						log.Printf("[prequeue] Found air date %s for S%02dE%02d from TVDB",
							foundAirDate, targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber)
					}
					break
				}
			}
		}
	}

	// Update targetEpisode with absolute number if found
	if foundAbsoluteEp > 0 && targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber == 0 {
		// Create a copy to avoid modifying the original
		updatedEpisode := &models.EpisodeReference{
			SeasonNumber:          targetEpisode.SeasonNumber,
			EpisodeNumber:         targetEpisode.EpisodeNumber,
			AbsoluteEpisodeNumber: foundAbsoluteEp,
			EpisodeID:             targetEpisode.EpisodeID,
			TvdbID:                targetEpisode.TvdbID,
			Title:                 targetEpisode.Title,
			Overview:              targetEpisode.Overview,
			RuntimeMinutes:        targetEpisode.RuntimeMinutes,
			AirDate:               targetEpisode.AirDate,
			WatchedAt:             targetEpisode.WatchedAt,
		}
		result.TargetEpisode = updatedEpisode
	}

	// Set the air date for daily show matching
	if foundAirDate != "" {
		result.TargetAirDate = foundAirDate
	}

	if len(seasonCounts) == 0 {
		log.Printf("[prequeue] No valid seasons found for episode resolver")
		return result
	}

	result.EpisodeResolver = filter.NewSeriesEpisodeResolver(seasonCounts)
	return result
}

// findAudioTrackByLanguage wraps the helper function for backward compatibility
func (h *PrequeueHandler) findAudioTrackByLanguage(streams []AudioStreamInfo, preferredLanguage string) int {
	return FindAudioTrackByLanguage(streams, preferredLanguage)
}

// findSubtitleTrackByPreference wraps the helper function for backward compatibility
func (h *PrequeueHandler) findSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode string) int {
	return FindSubtitleTrackByPreference(streams, preferredLanguage, mode)
}

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/history"
	"novastream/services/indexer"
	"novastream/services/playback"
	user_settings "novastream/services/user_settings"

	"github.com/gorilla/mux"
)

// PrequeueHandler handles prequeue requests for pre-loading playback streams
type PrequeueHandler struct {
	store           *playback.PrequeueStore
	indexerSvc      *indexer.Service
	playbackSvc     *playback.Service
	historySvc      *history.Service
	videoProber     VideoProber
	hlsCreator      HLSCreator
	metadataProber  VideoMetadataProber
	userSettingsSvc *user_settings.Service
	configManager   *config.Manager
	demoMode        bool
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
	Language string
	Title    string
}

// SubtitleStreamInfo contains subtitle stream metadata for track selection
type SubtitleStreamInfo struct {
	Index     int
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

// HLSCreator interface for creating HLS sessions
type HLSCreator interface {
	CreateHLSSession(ctx context.Context, path string, hasDV bool, dvProfile string, hasHDR bool, audioTrackIndex int, subtitleTrackIndex int) (*HLSSessionResult, error)
}

// HLSSessionResult contains HLS session info
type HLSSessionResult struct {
	SessionID   string
	PlaylistURL string
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
	// 5 minute TTL for prequeue entries
	store := playback.NewPrequeueStore(5 * time.Minute)

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

// SetUserSettingsService sets the user settings service for track preferences
func (h *PrequeueHandler) SetUserSettingsService(svc *user_settings.Service) {
	h.userSettingsSvc = svc
}

// SetConfigManager sets the config manager for global settings fallback
func (h *PrequeueHandler) SetConfigManager(cfgManager *config.Manager) {
	h.configManager = cfgManager
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

	log.Printf("[prequeue] Received request: titleId=%s titleName=%q userId=%s mediaType=%s", req.TitleID, titleName, req.UserID, mediaType)

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
	go h.runPrequeueWorker(entry.ID, titleName, req.ImdbID, mediaType, req.Year, req.UserID, targetEpisode)

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
func (h *PrequeueHandler) runPrequeueWorker(prequeueID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) {
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

	// Search for results (match manual selection limit for consistent fallback coverage)
	results, err := h.indexerSvc.Search(ctx, indexer.SearchOptions{
		Query:      query,
		MaxResults: 50,
		MediaType:  mediaType,
		IMDBID:     imdbID,
		Year:       year,
		UserID:     userID,
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

	// Try to resolve the best result
	var resolution *models.PlaybackResolution
	var lastErr error
	for _, result := range results {
		select {
		case <-ctx.Done():
			h.failPrequeue(prequeueID, "cancelled")
			return
		default:
		}

		resolution, lastErr = h.playbackSvc.Resolve(ctx, result)
		if lastErr == nil && resolution != nil && resolution.WebDAVPath != "" {
			log.Printf("[prequeue] Resolved result: %s -> %s", result.Title, resolution.WebDAVPath)
			break
		}
		log.Printf("[prequeue] Failed to resolve %s: %v", result.Title, lastErr)
		resolution = nil
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

		// Probe video metadata for available tracks
		metadata, err := h.metadataProber.ProbeVideoMetadata(ctx, resolution.WebDAVPath)
		if err != nil {
			log.Printf("[prequeue] Metadata probe failed (non-fatal): %v", err)
		} else if metadata != nil {
			// Debug: log user settings for track preferences
			log.Printf("[prequeue] User track preferences: audioLang=%q, subLang=%q, subMode=%q",
				userSettings.Playback.PreferredAudioLanguage,
				userSettings.Playback.PreferredSubtitleLanguage,
				userSettings.Playback.PreferredSubtitleMode)

			// Debug: log available audio streams
			for i, stream := range metadata.AudioStreams {
				log.Printf("[prequeue] Audio stream[%d]: index=%d lang=%q title=%q", i, stream.Index, stream.Language, stream.Title)
			}

			// Select audio track based on preferred language
			if userSettings.Playback.PreferredAudioLanguage != "" {
				selectedAudioTrack = h.findAudioTrackByLanguage(metadata.AudioStreams, userSettings.Playback.PreferredAudioLanguage)
				if selectedAudioTrack >= 0 {
					log.Printf("[prequeue] Selected audio track %d for language %q", selectedAudioTrack, userSettings.Playback.PreferredAudioLanguage)
				} else {
					log.Printf("[prequeue] No audio track found matching language %q", userSettings.Playback.PreferredAudioLanguage)
				}
			} else {
				log.Printf("[prequeue] No preferred audio language set in user settings")
			}

			// Select subtitle track based on preferred language and mode
			subMode := userSettings.Playback.PreferredSubtitleMode
			subLang := userSettings.Playback.PreferredSubtitleLanguage
			if subMode != "off" && subMode != "" {
				selectedSubtitleTrack = h.findSubtitleTrackByPreference(metadata.SubtitleStreams, subLang, subMode)
				if selectedSubtitleTrack >= 0 {
					log.Printf("[prequeue] Selected subtitle track %d for language %q (mode: %s)", selectedSubtitleTrack, subLang, subMode)
				}
			}
		}
	}

	// Store selected tracks
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.SelectedAudioTrack = selectedAudioTrack
		e.SelectedSubtitleTrack = selectedSubtitleTrack
	})

	// Probe for HDR if we have a video prober
	if h.videoProber != nil {
		probeResult, err := h.videoProber.ProbeVideoPath(ctx, resolution.WebDAVPath)
		if err != nil {
			log.Printf("[prequeue] Video probe failed (non-fatal): %v", err)
		} else if probeResult != nil {
			h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
				e.HasDolbyVision = probeResult.HasDolbyVision
				e.HasHDR10 = probeResult.HasHDR10
				e.DolbyVisionProfile = probeResult.DolbyVisionProfile
			})

			// If HDR content, create HLS session with selected tracks
			if (probeResult.HasDolbyVision || probeResult.HasHDR10) && h.hlsCreator != nil {
				hlsResult, err := h.hlsCreator.CreateHLSSession(
					ctx,
					resolution.WebDAVPath,
					probeResult.HasDolbyVision,
					probeResult.DolbyVisionProfile,
					probeResult.HasHDR10,
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

// findAudioTrackByLanguage finds an audio track matching the preferred language
func (h *PrequeueHandler) findAudioTrackByLanguage(streams []AudioStreamInfo, preferredLanguage string) int {
	if preferredLanguage == "" || len(streams) == 0 {
		return -1
	}

	normalizedPref := strings.ToLower(strings.TrimSpace(preferredLanguage))

	// Try exact match on language code or title
	for _, stream := range streams {
		language := strings.ToLower(strings.TrimSpace(stream.Language))
		title := strings.ToLower(strings.TrimSpace(stream.Title))

		if language == normalizedPref || title == normalizedPref {
			return stream.Index
		}
	}

	// Try partial match (e.g., "eng" matches "English")
	for _, stream := range streams {
		language := strings.ToLower(strings.TrimSpace(stream.Language))
		title := strings.ToLower(strings.TrimSpace(stream.Title))

		if strings.Contains(language, normalizedPref) ||
			strings.Contains(title, normalizedPref) ||
			strings.Contains(normalizedPref, language) ||
			strings.Contains(normalizedPref, title) {
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

		// Try partial match
		for _, stream := range candidateStreams {
			language := strings.ToLower(strings.TrimSpace(stream.Language))
			title := strings.ToLower(strings.TrimSpace(stream.Title))

			if strings.Contains(language, normalizedPref) ||
				strings.Contains(title, normalizedPref) ||
				strings.Contains(normalizedPref, language) ||
				strings.Contains(normalizedPref, title) {
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

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/internal/mediaresolve"
	"novastream/models"
	content_preferences "novastream/services/content_preferences"
	"novastream/services/debrid"
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

// MovieDetailsProvider provides movie metadata for anime detection
type MovieDetailsProvider interface {
	MovieInfo(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
}

// PrewarmService interface for checking pre-warmed entries and adopting ad-hoc prequeues
type PrewarmService interface {
	GetWarm(titleID, userID string) *playback.WarmRef
	GetWarmScoped(titleID, userID, settingsScopeKey string) *playback.WarmRef
	AdoptEntry(prequeueID string)
	UpdateFromPrequeue(prequeueID string)
	InvalidatePrequeue(prequeueID string)
}

// PrequeueHandler handles prequeue requests for pre-loading playback streams
type PrequeueHandler struct {
	store                 *playback.PrequeueStore
	indexerSvc            *indexer.Service
	playbackSvc           *playback.Service
	historySvc            *history.Service
	videoProber           VideoProber
	hlsCreator            HLSCreator
	metadataProber        VideoMetadataProber
	fullProber            VideoFullProber // Combined prober for single ffprobe call
	userSettingsSvc       *user_settings.Service
	contentPreferencesSvc *content_preferences.Service
	clientSettingsSvc     ClientSettingsProvider
	configManager         *config.Manager
	metadataSvc           SeriesDetailsProvider // For episode counting
	movieMetadataSvc      MovieDetailsProvider  // For movie anime detection
	subtitleExtractor     SubtitlePreExtractor  // For pre-extracting subtitles
	prewarmSvc            PrewarmService        // For checking pre-warmed entries
	failures              *streamFailureRegistry
	externalURLValidator  func(context.Context, string) error
	demoMode              bool
}

func hasTrackMetadata(entry *playback.PrequeueEntry) bool {
	if entry == nil {
		return false
	}
	return len(entry.AudioTracks) > 0 || len(entry.SubtitleTracks) > 0
}

func prequeueEpisodeMatches(requested, existing *models.EpisodeReference) bool {
	return playback.EpisodeReferencesMatch(requested, existing)
}

func isPrequeueInProgress(status playback.PrequeueStatus) bool {
	switch status {
	case playback.PrequeueStatusQueued,
		playback.PrequeueStatusSearching,
		playback.PrequeueStatusResolving,
		playback.PrequeueStatusProbing:
		return true
	default:
		return false
	}
}

func isExternalStreamPath(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

func shouldForceReresolveForStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return true
	default:
		return false
	}
}

func normalizeUnknownTrackPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "audio", "subtitles", "both":
		return strings.ToLower(strings.TrimSpace(policy))
	default:
		return "none"
	}
}

func unknownTrackPolicyNeedsProbe(policy string) bool {
	return normalizeUnknownTrackPolicy(policy) != "none"
}

func trackTextKnown(language, title string) bool {
	return strings.TrimSpace(language) != "" || strings.TrimSpace(title) != ""
}

func hasKnownAudioTrack(streams []AudioStreamInfo) bool {
	if len(streams) == 0 {
		return true
	}
	for _, stream := range streams {
		if trackTextKnown(stream.Language, stream.Title) {
			return true
		}
	}
	return false
}

func hasKnownSubtitleTrack(streams []SubtitleStreamInfo) bool {
	if len(streams) == 0 {
		return true
	}
	for _, stream := range streams {
		if trackTextKnown(stream.Language, stream.Title) {
			return true
		}
	}
	return false
}

func unknownTrackPolicyRejects(policy string, audioStreams []AudioStreamInfo, subtitleStreams []SubtitleStreamInfo) (bool, string) {
	switch normalizeUnknownTrackPolicy(policy) {
	case "audio":
		if !hasKnownAudioTrack(audioStreams) {
			return true, "audio tracks have unknown language metadata"
		}
	case "subtitles":
		if !hasKnownSubtitleTrack(subtitleStreams) {
			return true, "subtitle tracks have unknown language metadata"
		}
	case "both":
		audioUnknown := !hasKnownAudioTrack(audioStreams)
		subtitleUnknown := !hasKnownSubtitleTrack(subtitleStreams)
		switch {
		case audioUnknown && subtitleUnknown:
			return true, "audio and subtitle tracks have unknown language metadata"
		case audioUnknown:
			return true, "audio tracks have unknown language metadata"
		case subtitleUnknown:
			return true, "subtitle tracks have unknown language metadata"
		}
	}
	return false, ""
}

func defaultExternalURLValidator(ctx context.Context, streamURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, streamURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	if shouldForceReresolveForStatus(resp.StatusCode) {
		return fmt.Errorf("external stream validation returned %d", resp.StatusCode)
	}

	return nil
}

func (h *PrequeueHandler) validateReadyEntryForReuse(ctx context.Context, entry *playback.PrequeueEntry) error {
	if entry == nil || !isExternalStreamPath(entry.StreamPath) {
		return nil
	}

	validator := h.externalURLValidator
	if validator == nil {
		validator = defaultExternalURLValidator
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return validator(checkCtx, entry.StreamPath)
}

// ClientSettingsProvider interface for accessing per-client filter settings
type ClientSettingsProvider interface {
	Get(clientID string) (*models.ClientFilterSettings, error)
}

type prequeueScopePlayback struct {
	PreferredAudioLanguage     string `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage  string `json:"preferredSubtitleLanguage,omitempty"`
	PreferredSubtitleMode      string `json:"preferredSubtitleMode,omitempty"`
	ForceAACTranscoding        bool   `json:"forceAacTranscoding,omitempty"`
	IgnoreDVCompatibilityCheck *bool  `json:"ignoreDolbyVisionCompatibilityCheck,omitempty"`
	MaxResultsPerResolution    *int   `json:"maxResultsPerResolution,omitempty"`
}

type prequeueScopeSignature struct {
	Filtering         models.FilterSettings            `json:"filtering"`
	AnimeFiltering    models.AnimeFilteringSettings    `json:"animeFiltering"`
	Ranking           *models.UserRankingSettings      `json:"ranking,omitempty"`
	ClientRanking     *[]models.ClientRankingCriterion `json:"clientRanking,omitempty"`
	Playback          prequeueScopePlayback            `json:"playback"`
	ContentPreference *models.ContentPreference        `json:"contentPreference,omitempty"`
}

func configFilterToUserFilter(f config.FilterSettings) models.FilterSettings {
	return models.FilterSettings{
		MaxSizeMovieGB:         models.FloatPtr(f.MaxSizeMovieGB),
		MaxSizeEpisodeGB:       models.FloatPtr(f.MaxSizeEpisodeGB),
		MaxResolution:          f.MaxResolution,
		HDRDVPolicy:            models.HDRDVPolicy(f.HDRDVPolicy),
		RequiredTerms:          append([]string(nil), f.RequiredTerms...),
		FilterOutTerms:         append([]string(nil), f.FilterOutTerms...),
		PreferredTerms:         append([]string(nil), f.PreferredTerms...),
		NonPreferredTerms:      append([]string(nil), f.NonPreferredTerms...),
		DownloadPreferredTerms: append([]string(nil), f.DownloadPreferredTerms...),
		UnknownTrackPolicy:     string(f.UnknownTrackPolicy),
	}
}

func configAnimeToUserAnime(a config.AnimeFilteringSettings) models.AnimeFilteringSettings {
	return models.AnimeFilteringSettings{
		AnimeLanguageEnabled:   models.BoolPtr(a.AnimeLanguageEnabled),
		AnimePreferredLanguage: models.StringPtr(a.AnimePreferredLanguage),
	}
}

func configPlaybackToUserPlayback(p config.PlaybackSettings) models.PlaybackSettings {
	return models.PlaybackSettings{
		PreferredAudioLanguage:     p.PreferredAudioLanguage,
		PreferredSubtitleLanguage:  p.PreferredSubtitleLanguage,
		PreferredSubtitleMode:      p.PreferredSubtitleMode,
		ForceAACTranscoding:        p.ForceAACTranscoding,
		IgnoreDVCompatibilityCheck: models.BoolPtr(p.IgnoreDVCompatibilityCheck),
		MaxResultsPerResolution:    models.IntPtr(p.MaxResultsPerResolution),
	}
}

func applyClientScopeOverrides(sig *prequeueScopeSignature, clientSettings *models.ClientFilterSettings) {
	if sig == nil || clientSettings == nil {
		return
	}
	if clientSettings.MaxSizeMovieGB != nil {
		sig.Filtering.MaxSizeMovieGB = clientSettings.MaxSizeMovieGB
	}
	if clientSettings.MaxSizeEpisodeGB != nil {
		sig.Filtering.MaxSizeEpisodeGB = clientSettings.MaxSizeEpisodeGB
	}
	if clientSettings.MaxResolution != nil {
		sig.Filtering.MaxResolution = *clientSettings.MaxResolution
	}
	if clientSettings.HDRDVPolicy != nil {
		sig.Filtering.HDRDVPolicy = *clientSettings.HDRDVPolicy
	}
	if clientSettings.RequiredTerms != nil {
		sig.Filtering.RequiredTerms = append([]string(nil), (*clientSettings.RequiredTerms)...)
	}
	if clientSettings.FilterOutTerms != nil {
		sig.Filtering.FilterOutTerms = append([]string(nil), (*clientSettings.FilterOutTerms)...)
	}
	if clientSettings.PreferredTerms != nil {
		sig.Filtering.PreferredTerms = append([]string(nil), (*clientSettings.PreferredTerms)...)
	}
	if clientSettings.NonPreferredTerms != nil {
		sig.Filtering.NonPreferredTerms = append([]string(nil), (*clientSettings.NonPreferredTerms)...)
	}
	if clientSettings.DownloadPreferredTerms != nil {
		sig.Filtering.DownloadPreferredTerms = append([]string(nil), (*clientSettings.DownloadPreferredTerms)...)
	}
	if clientSettings.UnknownTrackPolicy != nil {
		sig.Filtering.UnknownTrackPolicy = *clientSettings.UnknownTrackPolicy
	}
	if clientSettings.AnimeLanguageEnabled != nil {
		sig.AnimeFiltering.AnimeLanguageEnabled = clientSettings.AnimeLanguageEnabled
	}
	if clientSettings.AnimePreferredLanguage != nil {
		sig.AnimeFiltering.AnimePreferredLanguage = clientSettings.AnimePreferredLanguage
	}
	if clientSettings.PreferredAudioLanguage != nil {
		sig.Playback.PreferredAudioLanguage = *clientSettings.PreferredAudioLanguage
	}
	if clientSettings.PreferredSubtitleLanguage != nil {
		sig.Playback.PreferredSubtitleLanguage = *clientSettings.PreferredSubtitleLanguage
	}
	if clientSettings.PreferredSubtitleMode != nil {
		sig.Playback.PreferredSubtitleMode = *clientSettings.PreferredSubtitleMode
	}
	if clientSettings.ForceAACTranscoding != nil {
		sig.Playback.ForceAACTranscoding = *clientSettings.ForceAACTranscoding
	}
	if clientSettings.IgnoreDVCompatibilityCheck != nil {
		sig.Playback.IgnoreDVCompatibilityCheck = clientSettings.IgnoreDVCompatibilityCheck
	}
	if clientSettings.MaxResultsPerResolution != nil {
		sig.Playback.MaxResultsPerResolution = clientSettings.MaxResultsPerResolution
	}
	if clientSettings.RankingCriteria != nil {
		sig.ClientRanking = clientSettings.RankingCriteria
	}
}

func prequeueScopeHash(sig prequeueScopeSignature) string {
	data, err := json.Marshal(sig)
	if err != nil {
		return playback.DefaultPrequeueSettingsScopeKey
	}
	sum := sha256.Sum256(data)
	return "scope_" + hex.EncodeToString(sum[:])[:16]
}

func (h *PrequeueHandler) prequeueSettingsScopeKey(userID, clientID, titleID string) string {
	var global prequeueScopeSignature
	defaults := models.UserSettings{}
	if h.configManager != nil {
		if globalSettings, err := h.configManager.Load(); err == nil {
			defaults.Filtering = configFilterToUserFilter(globalSettings.Filtering)
			defaults.AnimeFiltering = configAnimeToUserAnime(globalSettings.AnimeFiltering)
			defaults.Playback = configPlaybackToUserPlayback(globalSettings.Playback)
			global.Filtering = defaults.Filtering
			global.AnimeFiltering = defaults.AnimeFiltering
			global.Playback = prequeueScopePlayback{
				PreferredAudioLanguage:     defaults.Playback.PreferredAudioLanguage,
				PreferredSubtitleLanguage:  defaults.Playback.PreferredSubtitleLanguage,
				PreferredSubtitleMode:      defaults.Playback.PreferredSubtitleMode,
				ForceAACTranscoding:        defaults.Playback.ForceAACTranscoding,
				IgnoreDVCompatibilityCheck: defaults.Playback.IgnoreDVCompatibilityCheck,
				MaxResultsPerResolution:    defaults.Playback.MaxResultsPerResolution,
			}
		}
	}

	effective := global
	if h.userSettingsSvc != nil {
		if userSettings, err := h.userSettingsSvc.GetWithDefaults(userID, defaults); err == nil {
			effective.Filtering = userSettings.Filtering
			effective.AnimeFiltering = userSettings.AnimeFiltering
			effective.Ranking = userSettings.Ranking
			effective.Playback = prequeueScopePlayback{
				PreferredAudioLanguage:     userSettings.Playback.PreferredAudioLanguage,
				PreferredSubtitleLanguage:  userSettings.Playback.PreferredSubtitleLanguage,
				PreferredSubtitleMode:      userSettings.Playback.PreferredSubtitleMode,
				ForceAACTranscoding:        userSettings.Playback.ForceAACTranscoding,
				IgnoreDVCompatibilityCheck: userSettings.Playback.IgnoreDVCompatibilityCheck,
				MaxResultsPerResolution:    userSettings.Playback.MaxResultsPerResolution,
			}
		} else if err != nil {
			log.Printf("[prequeue] Failed to build profile prequeue settings scope (using global): %v", err)
		}
	}

	if clientID != "" && h.clientSettingsSvc != nil {
		if clientSettings, err := h.clientSettingsSvc.Get(clientID); err == nil {
			applyClientScopeOverrides(&effective, clientSettings)
		} else {
			log.Printf("[prequeue] Failed to build client prequeue settings scope (using profile/global): %v", err)
		}
	}

	if h.contentPreferencesSvc != nil && userID != "" && titleID != "" {
		if pref, err := h.contentPreferencesSvc.Get(userID, titleID); err == nil && pref != nil {
			effective.ContentPreference = pref
		} else if err != nil {
			log.Printf("[prequeue] Failed to include content preference in prequeue scope (non-fatal): %v", err)
		}
	}

	if reflect.DeepEqual(effective, global) {
		return playback.DefaultPrequeueSettingsScopeKey
	}
	return prequeueScopeHash(effective)
}

// PrequeueSettingsScopeKey returns the effective prequeue settings scope for a profile/client/title.
func (h *PrequeueHandler) PrequeueSettingsScopeKey(userID, clientID, titleID string) string {
	return h.prequeueSettingsScopeKey(userID, clientID, titleID)
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
	VideoCodec   string // e.g., "h264", "hevc", "mpeg4" - used to detect incompatible codecs
	VideoPixFmt  string // e.g., "yuv420p", "yuv420p10le" - used for browser compatibility
	VideoProfile string // e.g., "High", "High 10" - used for browser compatibility
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

// sanitizeLanguageCode strips stray quotes and whitespace from language codes.
func sanitizeLanguageCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.Trim(code, "'\"")
	code = strings.TrimSpace(code)
	return code
}

// normalizeSubtitleMode maps legacy subtitle mode values to canonical ones.
func normalizeSubtitleMode(mode string) string {
	switch mode {
	case "auto":
		return "forced-only"
	case "always":
		return "on"
	case "":
		return "off"
	default:
		return mode
	}
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
	// 30 minute TTL for prequeue entries (allows time for credits when triggered at 90%)
	store := playback.NewPrequeueStore(30 * time.Minute)

	return &PrequeueHandler{
		store:       store,
		indexerSvc:  indexerSvc,
		playbackSvc: playbackSvc,
		historySvc:  historySvc,
		videoProber: videoProber,
		hlsCreator:  hlsCreator,
		failures:    defaultStreamFailureRegistry,
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

// SetMovieMetadataService sets the movie metadata service for anime detection
func (h *PrequeueHandler) SetMovieMetadataService(svc MovieDetailsProvider) {
	h.movieMetadataSvc = svc
}

// SetSubtitleExtractor sets the subtitle extractor for pre-extraction
func (h *PrequeueHandler) SetSubtitleExtractor(extractor SubtitlePreExtractor) {
	h.subtitleExtractor = extractor
}

// SetPrewarmService sets the prewarm service for checking pre-warmed entries
func (h *PrequeueHandler) SetPrewarmService(svc PrewarmService) {
	h.prewarmSvc = svc
}

// GetStore returns the prequeue store for external access (e.g., prewarm service, admin viewer)
func (h *PrequeueHandler) GetStore() *playback.PrequeueStore {
	return h.store
}

// RunWorkerSync runs the prequeue worker synchronously and returns the prequeue ID.
// Used by the prewarm service to pre-resolve continue watching items.
func (h *PrequeueHandler) RunWorkerSync(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID string, targetEpisode *models.EpisodeReference) (string, error) {
	settingsScopeKey := h.prequeueSettingsScopeKey(userID, "", titleID)
	return h.RunWorkerSyncScoped(ctx, titleID, titleName, imdbID, mediaType, year, userID, "", settingsScopeKey, targetEpisode)
}

// RunWorkerSyncScoped runs the prequeue worker synchronously for an explicit settings scope.
func (h *PrequeueHandler) RunWorkerSyncScoped(ctx context.Context, titleID, titleName, imdbID, mediaType string, year int, userID, clientID, settingsScopeKey string, targetEpisode *models.EpisodeReference) (string, error) {
	// Create prequeue entry with a long TTL (inherits store TTL, prewarm service will extend)
	entry, _ := h.store.CreateScoped(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm", settingsScopeKey)

	// Run worker synchronously (blocking)
	h.runPrequeueWorker(entry.ID, titleID, titleName, imdbID, mediaType, year, userID, clientID, targetEpisode, 0, true)

	// Check result
	result, exists := h.store.Get(entry.ID)
	if !exists {
		return "", fmt.Errorf("prequeue entry expired during resolution")
	}
	if result.Status == playback.PrequeueStatusFailed {
		return entry.ID, fmt.Errorf("prequeue failed: %s", result.Error)
	}

	return entry.ID, nil
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
		if req.SeasonNumber >= 0 && req.EpisodeNumber > 0 {
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

	settingsScopeKey := h.prequeueSettingsScopeKey(req.UserID, clientID, req.TitleID)
	log.Printf("[prequeue] Effective settings scope for title=%s user=%s client=%s: %s", req.TitleID, req.UserID, clientID, settingsScopeKey)

	// Check for pre-warmed entry before creating a new one
	if h.prewarmSvc != nil {
		if warm := h.prewarmSvc.GetWarmScoped(req.TitleID, req.UserID, settingsScopeKey); warm != nil && warm.PrequeueID != "" {
			if warmEntry, ok := h.store.Get(warm.PrequeueID); ok && warmEntry.Status == playback.PrequeueStatusReady && hasTrackMetadata(warmEntry) {
				if err := h.validateReadyEntryForReuse(r.Context(), warmEntry); err != nil {
					log.Printf("[prequeue] Ignoring pre-warmed entry %s: stale external stream (%v), resolving fresh",
						warm.PrequeueID, err)
					h.store.Delete(warm.PrequeueID)
				} else {
					if prequeueEpisodeMatches(targetEpisode, warmEntry.TargetEpisode) {
						log.Printf("[prequeue] Using pre-warmed entry %s for title=%s user=%s scope=%s", warm.PrequeueID, req.TitleID, req.UserID, settingsScopeKey)
						resp := playback.PrequeueResponse{
							PrequeueID:    warm.PrequeueID,
							TargetEpisode: warmEntry.TargetEpisode,
							Status:        playback.PrequeueStatusReady,
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(resp)
						return
					}
					log.Printf("[prequeue] Pre-warmed entry %s episode mismatch (warm=%v, requested=%v), resolving fresh",
						warm.PrequeueID, warmEntry.TargetEpisode, targetEpisode)
				}
			} else {
				if _, storeOK := h.store.Get(warm.PrequeueID); !storeOK {
					log.Printf("[prequeue] Ignoring pre-warmed entry %s: no longer in store (replaced by newer prequeue), resolving fresh",
						warm.PrequeueID)
				} else {
					log.Printf("[prequeue] Ignoring pre-warmed entry %s: not ready or missing track metadata, resolving fresh",
						warm.PrequeueID)
				}
			}
		}
	}

	// Check for existing entry in the store (covers both prewarm and regular prequeues).
	if existing, ok := h.store.GetByTitleUserScope(req.TitleID, req.UserID, settingsScopeKey); ok {
		episodeMatch := prequeueEpisodeMatches(targetEpisode, existing.TargetEpisode)
		if !episodeMatch {
			log.Printf("[prequeue] Existing entry %s episode mismatch (cached=%v, requested=%v), resolving fresh",
				existing.ID, existing.TargetEpisode, targetEpisode)
		} else if existing.Status == playback.PrequeueStatusReady {
			if existing.StreamPath != "" && hasTrackMetadata(existing) {
				if err := h.validateReadyEntryForReuse(r.Context(), existing); err != nil {
					log.Printf("[prequeue] Discarding ready entry %s: stale external stream (%v), resolving fresh",
						existing.ID, err)
					h.store.Delete(existing.ID)
				} else {
					log.Printf("[prequeue] Reusing existing ready entry %s for title=%s user=%s scope=%s", existing.ID, req.TitleID, req.UserID, settingsScopeKey)
					resp := playback.PrequeueResponse{
						PrequeueID:    existing.ID,
						TargetEpisode: existing.TargetEpisode,
						Status:        playback.PrequeueStatusReady,
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(resp)
					return
				}
			} else {
				log.Printf("[prequeue] Existing ready entry %s missing stream path or track metadata, resolving fresh", existing.ID)
			}
		} else if isPrequeueInProgress(existing.Status) {
			log.Printf("[prequeue] Reusing existing in-progress entry %s status=%s for title=%s user=%s scope=%s",
				existing.ID, existing.Status, req.TitleID, req.UserID, settingsScopeKey)
			resp := playback.PrequeueResponse{
				PrequeueID:    existing.ID,
				TargetEpisode: existing.TargetEpisode,
				Status:        existing.Status,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		} else {
			log.Printf("[prequeue] Existing entry %s status=%s not reusable, resolving fresh", existing.ID, existing.Status)
		}
	}

	// Create prequeue entry
	entry, _ := h.store.CreateScoped(req.TitleID, titleName, req.UserID, mediaType, req.Year, targetEpisode, req.Reason, settingsScopeKey)

	// Register with prewarm so it keeps the entry alive via dynamic TTL
	if h.prewarmSvc != nil {
		h.prewarmSvc.AdoptEntry(entry.ID)
	}

	// Start background worker with all the info needed for search
	go h.runPrequeueWorker(entry.ID, req.TitleID, titleName, req.ImdbID, mediaType, req.Year, req.UserID, clientID, targetEpisode, req.StartOffset, req.SkipHLS)

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
	if episode != nil && episode.SeasonNumber >= 0 && episode.EpisodeNumber > 0 {
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
func (h *PrequeueHandler) runPrequeueWorker(prequeueID, titleID, titleName, imdbID, mediaType string, year int, userID, clientID string, targetEpisode *models.EpisodeReference, startOffset float64, skipHLS bool) {
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

	// Create episode resolver for TV shows to enable accurate pack size filtering
	// Also lookup absolute episode number, daily show info, and anime detection if not provided
	var episodeResolver *filter.SeriesEpisodeResolver
	var isDaily bool
	var isAnime bool
	var targetAirDate string
	var episodeAirYear int
	if mediaType == "series" && h.metadataSvc != nil {
		seriesMeta := h.createEpisodeResolverAndLookupAbsoluteEp(ctx, titleID, titleName, year, imdbID, targetEpisode)
		episodeResolver = seriesMeta.EpisodeResolver
		targetEpisode = seriesMeta.TargetEpisode
		isDaily = seriesMeta.IsDaily
		isAnime = seriesMeta.IsAnime
		targetAirDate = seriesMeta.TargetAirDate
		episodeAirYear = seriesMeta.EpisodeAirYear
		if year == 0 && seriesMeta.Year > 0 {
			year = seriesMeta.Year
			log.Printf("[prequeue] Populated year %d from series metadata", year)
		}
		h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
			e.TargetEpisode = targetEpisode
			e.Year = year
		})
		if episodeResolver != nil {
			log.Printf("[prequeue] Episode resolver created: %d total episodes, %d seasons", episodeResolver.TotalEpisodes, len(episodeResolver.SeasonEpisodeCounts))
		}
		if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
			log.Printf("[prequeue] Target episode S%02dE%02d has absolute episode number: %d",
				targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber, targetEpisode.AbsoluteEpisodeNumber)
		}
	}

	// Build the search query after metadata normalization. This keeps anime absolute-number
	// requests like S23E1162 from being treated as literal season/episode query text.
	query := h.buildSearchQuery(titleName, mediaType, targetEpisode)
	if query == "" {
		h.failPrequeue(prequeueID, "failed to build search query")
		return
	}

	log.Printf("[prequeue] TIMING: search starting with query: %q (elapsed: %v)", query, time.Since(workerStart))

	// For movies, check if the movie is anime by looking at genres
	if mediaType == "movie" && h.movieMetadataSvc != nil {
		movieQuery := models.MovieDetailsQuery{
			TitleID: titleID,
			Name:    titleName,
			Year:    year,
			IMDBID:  imdbID,
		}
		if movieTitle, err := h.movieMetadataSvc.MovieInfo(ctx, movieQuery); err == nil && movieTitle != nil {
			if isAnimeTitle(movieTitle) {
				isAnime = true
				log.Printf("[prequeue] Movie %q is anime (genres=%v originalName=%q language=%q) - applying anime language preferences",
					titleName, movieTitle.Genres, movieTitle.OriginalName, movieTitle.Language)
			}
		}
	}

	// Use the same search path as the regular search UI: wait for all sources
	// (debrid + usenet), combine, rank, and return a single ordered list.
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
		EpisodeAirYear:  episodeAirYear,
	}
	// Pass absolute episode number for anime matching (if available)
	if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
		searchOpts.AbsoluteEpisodeNumber = targetEpisode.AbsoluteEpisodeNumber
	}

	allResults, searchErr := h.indexerSvc.Search(ctx, searchOpts)
	if searchErr != nil || len(allResults) == 0 {
		errMsg := "no results found"
		if searchErr != nil {
			errMsg = searchErr.Error()
		}
		h.failPrequeue(prequeueID, errMsg)
		return
	}
	log.Printf("[prequeue] TIMING: search complete, %d combined results (elapsed: %v)", len(allResults), time.Since(workerStart))

	// Update status to resolving
	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusResolving
	})

	// Load filter settings for DV profile compatibility checking
	// Priority: client settings > user settings > global settings > default
	var hdrDVPolicy models.HDRDVPolicy
	unknownTrackPolicy := "none"

	// Layer 1: Start with global settings
	if h.configManager != nil {
		globalSettings, err := h.configManager.Load()
		if err == nil {
			hdrDVPolicy = models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy)
			unknownTrackPolicy = string(globalSettings.Filtering.UnknownTrackPolicy)
		}
	}

	// Layer 2: User settings override global
	if h.userSettingsSvc != nil {
		userSettings, err := h.userSettingsSvc.Get(userID)
		if err == nil && userSettings != nil && userSettings.Filtering.HDRDVPolicy != "" {
			hdrDVPolicy = userSettings.Filtering.HDRDVPolicy
		}
		if err == nil && userSettings != nil && userSettings.Filtering.UnknownTrackPolicy != "" {
			unknownTrackPolicy = userSettings.Filtering.UnknownTrackPolicy
		}
	}

	// Layer 3: Client/device settings override user
	if clientID != "" && h.clientSettingsSvc != nil {
		clientSettings, err := h.clientSettingsSvc.Get(clientID)
		if err == nil && clientSettings != nil && clientSettings.HDRDVPolicy != nil {
			hdrDVPolicy = *clientSettings.HDRDVPolicy
			log.Printf("[prequeue] Using client-specific HDR/DV policy: %s", hdrDVPolicy)
		}
		if err == nil && clientSettings != nil && clientSettings.UnknownTrackPolicy != nil {
			unknownTrackPolicy = *clientSettings.UnknownTrackPolicy
			log.Printf("[prequeue] Using client-specific unknown track policy: %s", unknownTrackPolicy)
		}
	}

	// Default to allowing all content
	if hdrDVPolicy == "" {
		hdrDVPolicy = models.HDRDVPolicyIncludeHDRDV
	}
	unknownTrackPolicy = normalizeUnknownTrackPolicy(unknownTrackPolicy)
	needsDVCheck := hdrDVPolicy == models.HDRDVPolicyIncludeHDR
	needsUnknownTrackCheck := unknownTrackPolicyNeedsProbe(unknownTrackPolicy)
	log.Printf("[prequeue] HDR/DV policy: %s, needsDVCheck: %v, unknownTrackPolicy: %s, needsUnknownTrackCheck: %v", hdrDVPolicy, needsDVCheck, unknownTrackPolicy, needsUnknownTrackCheck)

	// Resolution phase — iterate through combined ranked results (same order as search UI)
	var resolution *models.PlaybackResolution
	var lastErr error
	var selectedResult *models.NZBResult
	var fallbackResolution *models.PlaybackResolution
	var fallbackSelectedResult *models.NZBResult
	var fallbackProbeResult *VideoFullResult
	var fallbackMetadataResult *VideoMetadataResult

	resolveStart := time.Now()
	log.Printf("[prequeue] TIMING: starting resolution phase (%d results, elapsed: %v)",
		len(allResults), time.Since(workerStart))

	// Cached probe result for DV checking (reused later for track selection)
	var cachedProbeResult *VideoFullResult
	var cachedMetadataResult *VideoMetadataResult

	for i, result := range allResults {
		select {
		case <-ctx.Done():
			h.failPrequeue(prequeueID, "cancelled")
			return
		default:
		}

		// Check episode match for anime absolute numbering
		if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
			if result.EpisodeCount <= 1 {
				parsedEp, hasEpisode := mediaresolve.ParseAbsoluteEpisodeNumber(result.Title)
				if hasEpisode {
					episodeCode := mediaresolve.EpisodeCode{Season: targetEpisode.SeasonNumber, Episode: targetEpisode.EpisodeNumber}
					matchesSXXEXX := mediaresolve.CandidateMatchesEpisode(result.Title, episodeCode)
					if !matchesSXXEXX && parsedEp != targetEpisode.AbsoluteEpisodeNumber {
						log.Printf("[prequeue] Skipping result [%d] - episode %d doesn't match target (S%02dE%02d/abs:%d): %s",
							i, parsedEp, targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber, targetEpisode.AbsoluteEpisodeNumber, result.Title)
						continue
					}
				}
			}
		}

		annotateResultProfile(&result, userID)
		resolution, lastErr = h.playbackSvc.Resolve(ctx, result)
		if lastErr != nil || resolution == nil || resolution.WebDAVPath == "" {
			if debrid.IsBlockedContentError(lastErr) {
				log.Printf("[prequeue] Provider blocked selected file for result [%d] (%s) %s; trying next result: %v", i, result.ServiceType, result.Title, lastErr)
			} else {
				log.Printf("[prequeue] Failed to resolve result [%d] (%s) %s: %v", i, result.ServiceType, result.Title, lastErr)
			}
			resolution = nil
			continue
		}

		log.Printf("[prequeue] Resolved result [%d] (%s): %s -> %s", i, result.ServiceType, result.Title, resolution.WebDAVPath)

		var probeResult *VideoFullResult
		var metadataResult *VideoMetadataResult

		// Check DV compatibility and/or unknown track policy with the least probing possible.
		if (needsDVCheck || needsUnknownTrackCheck) && h.fullProber != nil {
			var probeErr error
			probeResult, probeErr = h.fullProber.ProbeVideoFull(ctx, resolution.WebDAVPath)
			if probeErr != nil {
				log.Printf("[prequeue] Probe check failed for %s: %v, trying next result", result.Title, probeErr)
				resolution = nil
				lastErr = probeErr
				continue
			}
			if needsDVCheck && probeResult != nil {
				if err := ValidateDVProfile(probeResult.DolbyVisionProfile, "hdr", probeResult.HasDolbyVision); err != nil {
					log.Printf("[prequeue] DV profile incompatible for %s: %v, trying next result", result.Title, err)
					resolution = nil
					lastErr = err
					continue
				}
				if probeResult.HasDolbyVision {
					log.Printf("[prequeue] DV profile %s compatible with 'hdr' policy", probeResult.DolbyVisionProfile)
				}
			}
		}

		if needsUnknownTrackCheck && probeResult == nil && h.metadataProber != nil {
			metadata, probeErr := h.metadataProber.ProbeVideoMetadata(ctx, resolution.WebDAVPath)
			if probeErr != nil {
				log.Printf("[prequeue] Track metadata probe failed for %s: %v, trying next result", result.Title, probeErr)
				resolution = nil
				lastErr = probeErr
				continue
			}
			metadataResult = metadata
		}

		if needsUnknownTrackCheck {
			var audioStreams []AudioStreamInfo
			var subtitleStreams []SubtitleStreamInfo
			if probeResult != nil {
				audioStreams = probeResult.AudioStreams
				subtitleStreams = probeResult.SubtitleStreams
			} else if metadataResult != nil {
				audioStreams = metadataResult.AudioStreams
				subtitleStreams = metadataResult.SubtitleStreams
			} else {
				log.Printf("[prequeue] Unknown track policy %q enabled but no track prober is available; keeping result %q", unknownTrackPolicy, result.Title)
			}

			if probeResult != nil || metadataResult != nil {
				if rejected, reason := unknownTrackPolicyRejects(unknownTrackPolicy, audioStreams, subtitleStreams); rejected {
					log.Printf("[prequeue] Result [%d] deprioritized by unknown track policy %q: %s; trying next result: %s", i, unknownTrackPolicy, reason, result.Title)
					if fallbackResolution == nil {
						resultCopy := result
						fallbackResolution = resolution
						fallbackSelectedResult = &resultCopy
						fallbackProbeResult = probeResult
						fallbackMetadataResult = metadataResult
					}
					resolution = nil
					continue
				}
			}
		}

		selectedResult = &result
		cachedProbeResult = probeResult
		cachedMetadataResult = metadataResult
		log.Printf("[prequeue] TIMING: resolved (took: %v, total elapsed: %v)",
			time.Since(resolveStart), time.Since(workerStart))
		break
	}

	if resolution == nil && fallbackResolution != nil {
		resolution = fallbackResolution
		selectedResult = fallbackSelectedResult
		cachedProbeResult = fallbackProbeResult
		cachedMetadataResult = fallbackMetadataResult
		if selectedResult != nil {
			log.Printf("[prequeue] All fully known candidates failed or were unavailable; using first deprioritized unknown-track result: %s", selectedResult.Title)
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
		if e.FileSize == 0 && selectedResult != nil && selectedResult.SizeBytes > 0 {
			e.FileSize = selectedResult.SizeBytes
		}
		e.HealthStatus = resolution.HealthStatus
		// Store magnet link for re-adding expired torrents after restart
		if selectedResult != nil && strings.HasPrefix(strings.ToLower(selectedResult.Link), "magnet:") {
			e.MagnetLink = selectedResult.Link
		}
		// Copy passthrough format data from AIOStreams results
		if selectedResult != nil && selectedResult.Attributes["passthrough_format"] == "true" {
			e.PassthroughName = selectedResult.Attributes["raw_name"]
			e.PassthroughDescription = selectedResult.Attributes["raw_description"]
		}
		// Copy parsed metadata attributes for badge display
		if selectedResult != nil && len(selectedResult.Attributes) > 0 {
			e.ResultAttributes = selectedResult.Attributes
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

		// Log global defaults for diagnostics
		log.Printf("[prequeue] Global defaults: audioLang=%q, subLang=%q, subMode=%q",
			defaults.Playback.PreferredAudioLanguage,
			defaults.Playback.PreferredSubtitleLanguage,
			defaults.Playback.PreferredSubtitleMode)

		// Get user settings with global defaults as fallback
		userSettings, err := h.userSettingsSvc.GetWithDefaults(userID, defaults)
		if err != nil {
			log.Printf("[prequeue] Failed to get user settings (non-fatal): %v", err)
		}

		// Log after user settings merge (before content overrides)
		log.Printf("[prequeue] After user settings merge: audioLang=%q, subLang=%q, subMode=%q",
			userSettings.Playback.PreferredAudioLanguage,
			userSettings.Playback.PreferredSubtitleLanguage,
			userSettings.Playback.PreferredSubtitleMode)

		if clientID != "" && h.clientSettingsSvc != nil {
			if clientSettings, err := h.clientSettingsSvc.Get(clientID); err == nil && clientSettings != nil {
				if clientSettings.PreferredAudioLanguage != nil {
					userSettings.Playback.PreferredAudioLanguage = *clientSettings.PreferredAudioLanguage
				}
				if clientSettings.PreferredSubtitleLanguage != nil {
					userSettings.Playback.PreferredSubtitleLanguage = *clientSettings.PreferredSubtitleLanguage
				}
				if clientSettings.PreferredSubtitleMode != nil {
					userSettings.Playback.PreferredSubtitleMode = *clientSettings.PreferredSubtitleMode
				}
				log.Printf("[prequeue] After client settings merge: audioLang=%q, subLang=%q, subMode=%q",
					userSettings.Playback.PreferredAudioLanguage,
					userSettings.Playback.PreferredSubtitleLanguage,
					userSettings.Playback.PreferredSubtitleMode)
			} else if err != nil {
				log.Printf("[prequeue] Failed to get client settings (non-fatal): %v", err)
			}
		}

		// Check for per-content language preferences (overrides user settings)
		if h.contentPreferencesSvc != nil {
			// Get the title ID from the prequeue entry
			if entry, ok := h.store.Get(prequeueID); ok && entry != nil {
				contentID := entry.TitleID
				if contentPref, err := h.contentPreferencesSvc.Get(userID, contentID); err == nil && contentPref != nil {
					log.Printf("[prequeue] Found per-content preference for %s: audioLang=%q, subLang=%q, subMode=%q",
						contentID, contentPref.AudioLanguage, contentPref.SubtitleLanguage, contentPref.SubtitleMode)
					// Sanitize content preference values
					contentPref.AudioLanguage = sanitizeLanguageCode(contentPref.AudioLanguage)
					contentPref.SubtitleLanguage = sanitizeLanguageCode(contentPref.SubtitleLanguage)
					contentPref.SubtitleMode = strings.TrimSpace(strings.Trim(contentPref.SubtitleMode, "'\""))
					// Override user settings with content-specific preferences
					if contentPref.AudioLanguage != "" {
						log.Printf("[prequeue] Content preference overriding audioLang: %q -> %q", userSettings.Playback.PreferredAudioLanguage, contentPref.AudioLanguage)
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
		} else if cachedMetadataResult != nil {
			audioStreams = cachedMetadataResult.AudioStreams
			subtitleStreams = cachedMetadataResult.SubtitleStreams
			log.Printf("[prequeue] Using cached metadata probe result: audioStreams=%d subStreams=%d", len(audioStreams), len(subtitleStreams))
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

			subMode := normalizeSubtitleMode(userSettings.Playback.PreferredSubtitleMode)
			subLang := userSettings.Playback.PreferredSubtitleLanguage
			if subMode != "off" {
				// Get actual language of selected audio track for audio-aware subtitle selection
				actualAudioLang := userSettings.Playback.PreferredAudioLanguage
				if selectedAudioTrack >= 0 {
					for _, s := range audioStreams {
						if s.Index == selectedAudioTrack {
							actualAudioLang = s.Language
							break
						}
					}
				}
				selectedSubtitleTrack = h.findSubtitleTrackByPreference(subtitleStreams, subLang, subMode, actualAudioLang)
				if selectedSubtitleTrack >= 0 {
					log.Printf("[prequeue] Selected subtitle track %d for language %q (mode: %s, audioLang: %s)", selectedSubtitleTrack, subLang, subMode, actualAudioLang)
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
		needsHLS := hasDV || hasHDR10 || needsAudioTranscode

		// Always store HDR detection results so the frontend can display correct badges,
		// regardless of whether HLS is needed (native clients skip HLS but still need HDR info)
		h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
			e.HasDolbyVision = hasDV
			e.HasHDR10 = hasHDR10
			e.DolbyVisionProfile = dvProfile
			e.NeedsAudioTranscode = needsAudioTranscode
		})

		if skipHLS {
			log.Printf("[prequeue] Skipping HLS session creation (native client)")
			needsHLS = false
		}
		if needsHLS {

			reason := "unknown"
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
	if h.prewarmSvc != nil {
		h.prewarmSvc.UpdateFromPrequeue(prequeueID)
	}

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

// MigrateStreamRequest is the request body for stream migration.
// Self-contained: performs its own search and resolves the next-best alternative.
type MigrateStreamRequest struct {
	TitleID          string  `json:"titleId"`
	TitleName        string  `json:"titleName"`
	MediaType        string  `json:"mediaType"` // "movie" or "series"
	UserID           string  `json:"userId"`
	FailedStreamPath string  `json:"failedStreamPath"` // Path of the stream that failed (to skip it)
	LastPosition     float64 `json:"lastPosition"`     // Playback position at time of failure
	SeasonNumber     int     `json:"seasonNumber,omitempty"`
	EpisodeNumber    int     `json:"episodeNumber,omitempty"`
	IMDBID           string  `json:"imdbId,omitempty"`
	Year             int     `json:"year,omitempty"`
}

// MigrateStreamResponse is the response with the new stream info.
type MigrateStreamResponse struct {
	StreamPath string  `json:"streamPath"`
	FileSize   int64   `json:"fileSize,omitempty"`
	Duration   float64 `json:"duration,omitempty"`
}

// MigrateStream searches for an alternative stream when the current one fails mid-playback.
// It performs a fresh search, skips the failed result, and resolves the next viable one.
func (h *PrequeueHandler) MigrateStream(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	var req MigrateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.TitleName == "" {
		http.Error(w, "titleName is required", http.StatusBadRequest)
		return
	}

	log.Printf("[stream-migration] Starting migration (title=%q, mediaType=%q, S%02dE%02d, failed=%q, position=%.1fs)",
		req.TitleName, req.MediaType, req.SeasonNumber, req.EpisodeNumber, req.FailedStreamPath, req.LastPosition)

	failedPath := strings.TrimSpace(req.FailedStreamPath)
	if failedPath == "" {
		http.Error(w, "failedStreamPath is required", http.StatusBadRequest)
		return
	}

	failures := h.failures
	if failures == nil {
		failures = defaultStreamFailureRegistry
	}
	failure, confirmed := failures.confirmedRecent(failedPath, streamFailureConfirmationTTL)
	if !confirmed {
		log.Printf("[stream-migration] Refusing migration without recent recoverable stream failure confirmation (failed=%q, position=%.1fs)",
			failedPath, req.LastPosition)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code":  "STREAM_FAILURE_NOT_CONFIRMED",
			"error": "stream failure was not confirmed by the server",
		})
		return
	}
	log.Printf("[stream-migration] Confirmed recent recoverable stream failure for %q: reason=%s age=%s",
		failedPath, failure.Reason, time.Since(failure.RecordedAt).Round(time.Millisecond))

	ctx := r.Context()

	// Build target episode for series (mediaType may be "series" or "episode")
	var targetEpisode *models.EpisodeReference
	if req.SeasonNumber >= 0 && req.EpisodeNumber > 0 {
		targetEpisode = &models.EpisodeReference{
			SeasonNumber:  req.SeasonNumber,
			EpisodeNumber: req.EpisodeNumber,
		}
	}
	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType == "episode" || targetEpisode != nil {
		mediaType = "series"
	}

	var episodeResolver *filter.SeriesEpisodeResolver
	var isDaily bool
	var isAnime bool
	var targetAirDate string
	var episodeAirYear int
	var absoluteEpisodeNumber int
	if mediaType == "series" && targetEpisode != nil {
		seriesMeta := h.createEpisodeResolverAndLookupAbsoluteEp(ctx, req.TitleID, req.TitleName, req.Year, req.IMDBID, targetEpisode)
		if seriesMeta != nil {
			episodeResolver = seriesMeta.EpisodeResolver
			targetEpisode = seriesMeta.TargetEpisode
			isDaily = seriesMeta.IsDaily
			isAnime = seriesMeta.IsAnime
			targetAirDate = seriesMeta.TargetAirDate
			episodeAirYear = seriesMeta.EpisodeAirYear
			if req.Year == 0 && seriesMeta.Year > 0 {
				req.Year = seriesMeta.Year
			}
			if targetEpisode != nil {
				absoluteEpisodeNumber = targetEpisode.AbsoluteEpisodeNumber
			}
		}
	}

	// Build search query (same logic as prequeue worker)
	query := h.buildSearchQuery(req.TitleName, mediaType, targetEpisode)
	if query == "" {
		http.Error(w, "failed to build search query", http.StatusBadRequest)
		return
	}

	log.Printf("[stream-migration] Searching with query: %q", query)

	// Search for results
	searchOpts := indexer.SearchOptions{
		Query:                 query,
		MaxResults:            50,
		MediaType:             mediaType,
		IMDBID:                req.IMDBID,
		Year:                  req.Year,
		UserID:                req.UserID,
		EpisodeResolver:       episodeResolver,
		IsDaily:               isDaily,
		IsAnime:               isAnime,
		TargetAirDate:         targetAirDate,
		EpisodeAirYear:        episodeAirYear,
		AbsoluteEpisodeNumber: absoluteEpisodeNumber,
	}

	allResults, searchErr := h.indexerSvc.Search(ctx, searchOpts)
	if searchErr != nil || len(allResults) == 0 {
		log.Printf("[stream-migration] Search returned no results: %v", searchErr)
		http.Error(w, "no alternative streams found", http.StatusNotFound)
		return
	}

	log.Printf("[stream-migration] Found %d results, resolving alternatives (skipping failed path)", len(allResults))

	// Iterate through results, skip the one matching the failed path
	var resolution *models.PlaybackResolution
	var selectedResult *models.NZBResult
	var duration float64

	for i, result := range allResults {
		annotateResultProfile(&result, req.UserID)
		resolution, _ = h.playbackSvc.Resolve(ctx, result)
		if resolution == nil || resolution.WebDAVPath == "" {
			continue
		}

		// Skip if this resolves to the same path that failed
		if normalizeStreamFailurePath(resolution.WebDAVPath) == normalizeStreamFailurePath(req.FailedStreamPath) {
			log.Printf("[stream-migration] Skipping result [%d] — same as failed path: %s", i, result.Title)
			resolution = nil
			continue
		}

		if h.fullProber != nil {
			fullResult, err := h.fullProber.ProbeVideoFull(ctx, resolution.WebDAVPath)
			if err != nil {
				log.Printf("[stream-migration] Skipping result [%d] — probe failed: %s -> %v", i, result.Title, err)
				if failures.recordIfMissingArticles(resolution.WebDAVPath, err) {
					log.Printf("[stream-migration] recorded failed alternative path=%q err=%v", resolution.WebDAVPath, err)
				}
				resolution = nil
				continue
			}
			if fullResult != nil {
				duration = fullResult.Duration
			}
		}

		log.Printf("[stream-migration] Resolved alternative [%d]: %s -> %s", i, result.Title, resolution.WebDAVPath)
		selectedResult = &result
		break
	}

	if resolution == nil || selectedResult == nil {
		log.Printf("[stream-migration] No viable alternatives found")
		http.Error(w, "no alternative streams found", http.StatusNotFound)
		return
	}

	resp := MigrateStreamResponse{
		StreamPath: resolution.WebDAVPath,
		FileSize:   resolution.FileSize,
		Duration:   duration,
	}

	log.Printf("[stream-migration] Migration successful: %s (%.0fs duration)", resp.StreamPath, resp.Duration)
	for _, removed := range h.store.DeleteByStreamPath(failedPath) {
		log.Printf("[stream-migration] Removed failed prequeue %s for title=%s user=%s path=%q",
			removed.ID, removed.TitleID, removed.UserID, removed.StreamPath)
		if h.prewarmSvc != nil {
			h.prewarmSvc.InvalidatePrequeue(removed.ID)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func annotateResultProfile(result *models.NZBResult, userID string) {
	userID = strings.TrimSpace(userID)
	if result == nil || userID == "" {
		return
	}
	if result.Attributes == nil {
		result.Attributes = map[string]string{}
	}
	result.Attributes["profileId"] = userID
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

	// Subtitle extraction disabled — the player handles subtitles natively.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(StartSubtitlesResponse{
		SubtitleSessions: make(map[int]*models.SubtitleSessionInfo),
	})
	return

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
	if targetEpisode != nil && targetEpisode.SeasonNumber >= 0 && targetEpisode.EpisodeNumber > 0 {
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
	EpisodeAirYear  int    // Year the target episode aired, used to allow later-season year tags
	IsAnime         bool   // True for anime content - requires waiting for Nyaa scraper
	Year            int    // Series premiere year from metadata (used when frontend doesn't provide it)
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

	// Populate year from metadata
	if details.Title.Year > 0 {
		result.Year = details.Title.Year
	}

	// Check if this is a daily show from the metadata
	result.IsDaily = details.Title.IsDaily
	if result.IsDaily {
		log.Printf("[prequeue] Series %q is a daily show (talk show, news, etc.) - will use date-based matching", details.Title.Name)
	}

	// Check if this is anime content from the genres
	if isAnimeTitle(&details.Title) {
		result.IsAnime = true
		log.Printf("[prequeue] Series %q is anime (genres=%v originalName=%q language=%q timezone=%q) - will wait for all scrapers including Nyaa",
			details.Title.Name, details.Title.Genres, details.Title.OriginalName, details.Title.Language, details.Title.AirsTimezone)
	}

	if len(details.Seasons) == 0 {
		log.Printf("[prequeue] No season data available for episode resolver")
		return result
	}

	// Build season -> episode count map AND lookup absolute episode number and air date
	seasonCounts := make(map[int]int)
	var foundAbsoluteEp int
	var foundAirDate string
	var foundCanonicalEpisode *models.SeriesEpisode
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
					epCopy := ep
					foundCanonicalEpisode = &epCopy
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

	if targetEpisode != nil && foundCanonicalEpisode == nil && targetEpisode.AbsoluteEpisodeNumber == 0 && targetEpisode.EpisodeNumber > 0 {
		for _, season := range details.Seasons {
			for _, ep := range season.Episodes {
				if ep.AbsoluteEpisodeNumber == targetEpisode.EpisodeNumber {
					epCopy := ep
					foundCanonicalEpisode = &epCopy
					foundAbsoluteEp = ep.AbsoluteEpisodeNumber
					foundAirDate = ep.AiredDate
					log.Printf("[prequeue] Normalized legacy absolute episode S%02dE%02d to S%02dE%02d (abs: %d) from TVDB",
						targetEpisode.SeasonNumber, targetEpisode.EpisodeNumber, ep.SeasonNumber, ep.EpisodeNumber, ep.AbsoluteEpisodeNumber)
					break
				}
			}
			if foundCanonicalEpisode != nil {
				break
			}
		}
	}

	// Update targetEpisode with canonical season/episode and absolute number if found
	if foundCanonicalEpisode != nil && targetEpisode != nil {
		// Create a copy to avoid modifying the original
		updatedEpisode := &models.EpisodeReference{
			SeasonNumber:          foundCanonicalEpisode.SeasonNumber,
			EpisodeNumber:         foundCanonicalEpisode.EpisodeNumber,
			AbsoluteEpisodeNumber: foundAbsoluteEp,
			EpisodeID:             foundCanonicalEpisode.ID,
			TvdbID:                strconv.FormatInt(foundCanonicalEpisode.TVDBID, 10),
			Title:                 foundCanonicalEpisode.Name,
			Overview:              foundCanonicalEpisode.Overview,
			RuntimeMinutes:        foundCanonicalEpisode.Runtime,
			AirDate:               foundCanonicalEpisode.AiredDate,
			WatchedAt:             targetEpisode.WatchedAt,
		}
		if updatedEpisode.AbsoluteEpisodeNumber == 0 {
			updatedEpisode.AbsoluteEpisodeNumber = targetEpisode.AbsoluteEpisodeNumber
		}
		result.TargetEpisode = updatedEpisode
	}

	// Set the air date for daily show matching
	if foundAirDate != "" {
		result.TargetAirDate = foundAirDate
		if len(foundAirDate) >= 4 {
			if airYear, err := strconv.Atoi(foundAirDate[:4]); err == nil && airYear > 0 {
				result.EpisodeAirYear = airYear
			}
		}
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
func (h *PrequeueHandler) findSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode, audioLanguage string) int {
	return FindSubtitleTrackByPreference(streams, preferredLanguage, mode, audioLanguage)
}

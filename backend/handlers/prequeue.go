package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/importer"
	"novastream/internal/mediaidentity"
	"novastream/internal/mediaresolve"
	"novastream/internal/requestsecurity"
	"novastream/models"
	"novastream/services/badstreams"
	content_preferences "novastream/services/content_preferences"
	"novastream/services/debrid"
	"novastream/services/history"
	"novastream/services/indexer"
	"novastream/services/playback"
	"novastream/services/trackselect"
	user_settings "novastream/services/user_settings"
	"novastream/utils/filter"

	"github.com/gorilla/mux"
)

var seriesDisplayLabelRE = regexp.MustCompile(`(?i)\s*[•·]\s*S\d{1,4}E\d{1,5}\b.*$`)

const prequeueResolutionWorkerJoinTimeout = 5 * time.Second

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

type prequeueOwnershipService interface {
	BelongsToAccount(profileID, accountID string) bool
}

// prequeuePlaybackService is the subset of playback.Service the prequeue
// resolution phase depends on, so tests can substitute a controllable fake.
type prequeuePlaybackService interface {
	Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
	QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error)
	PrepareTorrentCandidates(ctx context.Context, candidates []models.NZBResult) []models.NZBResult
}

// PrequeueHandler handles prequeue requests for pre-loading playback streams
type PrequeueHandler struct {
	store                 *playback.PrequeueStore
	indexerSvc            *indexer.Service
	playbackSvc           prequeuePlaybackService
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
	latencyTracker        *PlaybackLatencyTracker
	failures              *streamFailureRegistry
	badStreamsSvc         *badstreams.Service
	externalURLValidator  func(context.Context, string) error
	users                 prequeueOwnershipService
	demoMode              bool
}

func (h *PrequeueHandler) canAccessUser(r *http.Request, userID string) bool {
	if h.users == nil || auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	return accountID != "" && h.users.BelongsToAccount(userID, accountID)
}

func (h *PrequeueHandler) authorizeEntry(w http.ResponseWriter, r *http.Request, entry *playback.PrequeueEntry) bool {
	if entry != nil && h.canAccessUser(r, entry.UserID) {
		return true
	}
	http.Error(w, "prequeue not found or expired", http.StatusNotFound)
	return false
}

func hasReusablePreparation(entry *playback.PrequeueEntry) bool {
	if entry == nil {
		return false
	}
	// Entries persisted before the complete DOVI record was stored can still have
	// HasDolbyVision/profile populated. Reusing one would let fast-start open the
	// stream without the decoder configuration needed to create a DV format
	// description, so force a fresh probe to backfill the record.
	if entry.HasDolbyVision &&
		(entry.DolbyVisionConfiguration == nil ||
			strings.TrimSpace(entry.DolbyVisionConfiguration.PixelFormat) == "") {
		return false
	}
	return entry.MigrationAdopted || len(entry.AudioTracks) > 0 || len(entry.SubtitleTracks) > 0
}

func prequeueEpisodeMatches(requested, existing *models.EpisodeReference) bool {
	return playback.EpisodeReferencesMatch(requested, existing)
}

// canonicalPrequeueTitleID normalizes a client-supplied title ID to its canonical
// provider form using the accompanying external IDs, so the same title opened from
// different shelves (which hand us different ID forms) maps to one prequeue store key.
// Falls back to the original ID when no canonical form can be derived.
func canonicalPrequeueTitleID(mediaType, titleID, imdbID, tmdbID, tvdbID string) string {
	canonical := mediaidentity.CanonicalTitleID(mediaType, titleID, map[string]string{
		"imdb": imdbID,
		"tmdb": tmdbID,
		"tvdb": tvdbID,
	})
	if canonical == "" {
		return titleID
	}
	return canonical
}

func isPrequeueInProgress(status playback.PrequeueStatus) bool {
	return playback.IsPrequeueInProgress(status)
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

func isM2TSStreamPath(streamPath string) bool {
	trimmed := strings.TrimSpace(streamPath)
	if trimmed == "" {
		return false
	}
	if value, _, ok := strings.Cut(trimmed, "?"); ok {
		trimmed = value
	}
	if value, _, ok := strings.Cut(trimmed, "#"); ok {
		trimmed = value
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimRight(trimmed, "/")), ".m2ts")
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

func normalizeAllowedTrackLanguages(languages []string) []string {
	return trackselect.NormalizeAllowedLanguages(languages)
}

func copyOptionalStringSlice(values *[]string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), (*values)...)
}

func findAllowedAudioTrack(streams []AudioStreamInfo, allowedLanguages []string, preferredLanguage string) int {
	return trackselect.FindAllowedAudioTrack(streams, allowedLanguages, preferredLanguage)
}

// DefaultExternalURLValidator probes a pre-resolved external stream URL (e.g.
// AIOStreams/Comet proxy links) and returns an error when the link has expired,
// so callers can drop the stale ready entry and force a fresh re-search. It is
// the single source of truth for external-URL staleness, shared by the prequeue
// reuse path and the prequeue store's stream-path validator.
func DefaultExternalURLValidator(ctx context.Context, streamURL string) error {
	return validateExternalStreamURL(ctx, streamURL, nil)
}

func defaultExternalURLValidator(ctx context.Context, streamURL string) error {
	return validateExternalStreamURL(ctx, streamURL, nil)
}

// ValidateExternalURL validates a cached stream URL using the same network
// policy as playback, including explicitly configured private provider hosts.
func (h *PrequeueHandler) ValidateExternalURL(ctx context.Context, streamURL string) error {
	return validateExternalStreamURL(ctx, streamURL, configuredProviderHostPolicy(h.configManager))
}

func validateExternalStreamURL(ctx context.Context, streamURL string, allowRestricted requestsecurity.RestrictedHostPolicy) error {
	if err := requestsecurity.ValidateOutboundURL(ctx, streamURL, allowRestricted); err != nil {
		return err
	}
	client := requestsecurity.NewSafeHTTPClient(5*time.Second, 5, allowRestricted)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, streamURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	finalURL := resp.Request.URL.String()
	_ = resp.Body.Close()

	if debrid.IsKnownPlaceholderURL(finalURL) {
		return fmt.Errorf("external stream redirected to unavailable-content placeholder")
	}
	if shouldForceReresolveForStatus(resp.StatusCode) {
		return fmt.Errorf("external stream validation returned %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed &&
		!isElfHostedStreamURL(streamURL) &&
		!isElfHostedStreamURL(finalURL) {
		return nil
	}

	rangeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return err
	}
	rangeReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")
	rangeReq.Header.Set("Range", "bytes=0-4095")
	rangeReq.Header.Set("Accept-Encoding", "identity")

	rangeResp, err := client.Do(rangeReq)
	if err != nil {
		return err
	}
	defer rangeResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(rangeResp.Body, 4096))
	if err != nil {
		return err
	}
	if debrid.IsKnownPlaceholderResponse(rangeResp.Request.URL.String(), body) {
		return fmt.Errorf("external stream returned unavailable-content placeholder")
	}
	if shouldForceReresolveForStatus(rangeResp.StatusCode) {
		return fmt.Errorf("external stream validation returned %d", rangeResp.StatusCode)
	}

	return nil
}

func isElfHostedStreamURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "elfhosted.com" || strings.HasSuffix(host, ".elfhosted.com")
}

func (h *PrequeueHandler) validateReadyEntryForReuse(ctx context.Context, entry *playback.PrequeueEntry) error {
	if entry == nil || !isExternalStreamPath(entry.StreamPath) {
		return nil
	}

	validator := h.externalURLValidator
	if validator == nil {
		validator = h.ValidateExternalURL
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return validator(checkCtx, entry.StreamPath)
}

// ClientSettingsProvider interface for accessing per-(device, person) filter settings
type ClientSettingsProvider interface {
	Get(clientID, userID string) (*models.ClientFilterSettings, error)
}

type prequeueScopePlayback struct {
	PreferredAudioLanguage     string   `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage  string   `json:"preferredSubtitleLanguage,omitempty"`
	AllowedTrackLanguages      []string `json:"allowedTrackLanguages,omitempty"`
	PreferredSubtitleMode      string   `json:"preferredSubtitleMode,omitempty"`
	ForceAACTranscoding        bool     `json:"forceAacTranscoding,omitempty"`
	IgnoreDVCompatibilityCheck *bool    `json:"ignoreDolbyVisionCompatibilityCheck,omitempty"`
	MaxResultsPerResolution    *int     `json:"maxResultsPerResolution,omitempty"`
}

type prequeueScopeSignature struct {
	Filtering          models.FilterSettings            `json:"filtering"`
	AnimeFiltering     models.AnimeFilteringSettings    `json:"animeFiltering"`
	Ranking            *models.UserRankingSettings      `json:"ranking,omitempty"`
	ClientRanking      *[]models.ClientRankingCriterion `json:"clientRanking,omitempty"`
	NewestReleaseFirst bool                             `json:"newestReleaseFirst,omitempty"`
	Playback           prequeueScopePlayback            `json:"playback"`
	ContentPreference  *models.ContentPreference        `json:"contentPreference,omitempty"`
}

func configFilterToUserFilter(f config.FilterSettings) models.FilterSettings {
	return models.FilterSettings{
		MaxSizeMovieGB:                         models.FloatPtr(f.MaxSizeMovieGB),
		MaxSizeEpisodeGB:                       models.FloatPtr(f.MaxSizeEpisodeGB),
		MaxResolution:                          models.StringPtr(f.MaxResolution),
		HDRDVPolicy:                            models.HDRDVPolicy(f.HDRDVPolicy),
		RequiredTerms:                          append([]string(nil), f.RequiredTerms...),
		FilterOutTerms:                         append([]string(nil), f.FilterOutTerms...),
		PreferredTerms:                         append([]string(nil), f.PreferredTerms...),
		NonPreferredTerms:                      append([]string(nil), f.NonPreferredTerms...),
		DownloadPreferredTerms:                 append([]string(nil), f.DownloadPreferredTerms...),
		PreferredScraper:                       models.StringPtr(f.PreferredScraper),
		ServicePriority:                        models.StringPtr(string(f.ServicePriority)),
		UnknownTrackPolicy:                     string(f.UnknownTrackPolicy),
		AdaptivePlaybackEnabled:                models.BoolPtr(f.AdaptivePlaybackEnabled),
		AdaptiveTargetBufferFactor:             models.FloatPtr(f.AdaptiveTargetBufferFactor),
		RealDebridRestrictedTermsFilterEnabled: models.BoolPtr(f.RealDebridRestrictedTermsFilterEnabled),
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
		AllowedTrackLanguages:      models.StringSlicePtr(p.AllowedTrackLanguages),
		PreferredSubtitleMode:      p.PreferredSubtitleMode,
		ForceAACTranscoding:        models.BoolPtr(p.ForceAACTranscoding),
		IgnoreDVCompatibilityCheck: models.BoolPtr(p.IgnoreDVCompatibilityCheck),
		CreditsDetectionEnabled:    models.BoolPtr(p.CreditsDetectionEnabled),
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
		sig.Filtering.MaxResolution = models.StringPtr(*clientSettings.MaxResolution)
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
	if clientSettings.RealDebridRestrictedTermsFilterEnabled != nil {
		sig.Filtering.RealDebridRestrictedTermsFilterEnabled = clientSettings.RealDebridRestrictedTermsFilterEnabled
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
	if clientSettings.AllowedTrackLanguages != nil {
		sig.Playback.AllowedTrackLanguages = append([]string(nil), (*clientSettings.AllowedTrackLanguages)...)
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
	if clientSettings.NewestReleaseFirst != nil {
		sig.NewestReleaseFirst = *clientSettings.NewestReleaseFirst
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

// applyAdaptiveScopePolicy keeps capability changes that alter what the prepared
// stream can play (HDR/DV) in the cache identity. Throughput-derived size caps
// are deliberately excluded: a new speed sample should not invalidate a ready
// stream that is still small enough for the new cap.
func applyAdaptiveScopePolicy(filtering *models.FilterSettings, caps models.AdaptiveCaps) {
	if filtering == nil || caps.HDRDVPolicy == nil {
		return
	}
	filtering.HDRDVPolicy = *caps.HDRDVPolicy
}

func readyEntryFitsAdaptiveCaps(entry *playback.PrequeueEntry, caps models.AdaptiveCaps) bool {
	if entry == nil {
		return false
	}
	var capGB *float64
	if strings.EqualFold(entry.MediaType, "movie") {
		capGB = caps.MaxSizeMovieGB
	} else {
		capGB = caps.MaxSizeEpisodeGB
	}
	if capGB == nil || *capGB <= 0 {
		return true
	}
	if entry.FileSize <= 0 {
		return false
	}
	return float64(entry.FileSize)/(1024*1024*1024) <= *capGB
}

func (h *PrequeueHandler) prequeueSettingsScope(userID, clientID, titleID string, throughput *models.AdaptiveThroughputContext) (string, models.AdaptiveCaps) {
	var global prequeueScopeSignature
	var adaptiveCaps models.AdaptiveCaps
	defaults := models.UserSettings{}
	if h.configManager != nil {
		if globalSettings, err := h.configManager.Load(); err == nil {
			defaults.Filtering = configFilterToUserFilter(globalSettings.Filtering)
			defaults.AnimeFiltering = configAnimeToUserAnime(globalSettings.AnimeFiltering)
			defaults.Playback = configPlaybackToUserPlayback(globalSettings.Playback)
			global.Filtering = defaults.Filtering
			global.AnimeFiltering = defaults.AnimeFiltering
			global.NewestReleaseFirst = globalSettings.Ranking.NewestReleaseFirst
			global.Playback = prequeueScopePlayback{
				PreferredAudioLanguage:     defaults.Playback.PreferredAudioLanguage,
				PreferredSubtitleLanguage:  defaults.Playback.PreferredSubtitleLanguage,
				AllowedTrackLanguages:      append([]string(nil), globalSettings.Playback.AllowedTrackLanguages...),
				PreferredSubtitleMode:      defaults.Playback.PreferredSubtitleMode,
				ForceAACTranscoding:        models.BoolVal(defaults.Playback.ForceAACTranscoding, false),
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
			if userSettings.Ranking != nil && userSettings.Ranking.NewestReleaseFirst != nil {
				effective.NewestReleaseFirst = *userSettings.Ranking.NewestReleaseFirst
			}
			effective.Playback = prequeueScopePlayback{
				PreferredAudioLanguage:     userSettings.Playback.PreferredAudioLanguage,
				PreferredSubtitleLanguage:  userSettings.Playback.PreferredSubtitleLanguage,
				AllowedTrackLanguages:      copyOptionalStringSlice(userSettings.Playback.AllowedTrackLanguages),
				PreferredSubtitleMode:      userSettings.Playback.PreferredSubtitleMode,
				ForceAACTranscoding:        models.BoolVal(userSettings.Playback.ForceAACTranscoding, false),
				IgnoreDVCompatibilityCheck: userSettings.Playback.IgnoreDVCompatibilityCheck,
				MaxResultsPerResolution:    userSettings.Playback.MaxResultsPerResolution,
			}
		} else if err != nil {
			log.Printf("[prequeue] Failed to build profile prequeue settings scope (using global): %v", err)
		}
	}

	var clientSettings *models.ClientFilterSettings
	if clientID != "" && userID != "" && h.clientSettingsSvc != nil {
		if cs, err := h.clientSettingsSvc.Get(clientID, userID); err == nil {
			clientSettings = cs
			applyClientScopeOverrides(&effective, cs)
		} else {
			log.Printf("[prequeue] Failed to build client prequeue settings scope (using profile/global): %v", err)
		}
	}

	// Display capability remains part of the scope because it can change the
	// prepared stream format. Throughput caps are checked against a ready entry's
	// actual file size at reuse time, allowing harmless speed changes to retain a
	// valid prewarm while still rejecting entries that are now too large.
	if h.configManager != nil {
		if globalSettings, err := h.configManager.Load(); err == nil {
			var adaptive *models.AdaptivePlaybackSettings
			if clientSettings != nil {
				adaptive = clientSettings.AdaptivePlayback
			}
			adaptiveCaps = models.ComputeAdaptiveCaps(
				models.BoolVal(effective.Filtering.AdaptivePlaybackEnabled, globalSettings.Filtering.AdaptivePlaybackEnabled),
				models.FloatVal(effective.Filtering.AdaptiveTargetBufferFactor, globalSettings.Filtering.AdaptiveTargetBufferFactor),
				models.AdaptiveSettingsForRequest(adaptive, throughput),
				time.Now(),
			)
			applyAdaptiveScopePolicy(&effective.Filtering, adaptiveCaps)
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
		return playback.DefaultPrequeueSettingsScopeKey, adaptiveCaps
	}
	return prequeueScopeHash(effective), adaptiveCaps
}

func (h *PrequeueHandler) prequeueSettingsScopeKey(userID, clientID, titleID string) string {
	scopeKey, _ := h.prequeueSettingsScope(userID, clientID, titleID, nil)
	return scopeKey
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
	HasDolbyVision           bool
	HasHDR10                 bool
	DolbyVisionProfile       string
	DolbyVisionConfiguration *models.DolbyVisionConfiguration
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

// VideoFullResult combines HDR detection and stream metadata in a single result.
type VideoFullResult = models.VideoFullResult

// VideoFullProber interface for combined HDR and metadata probing in a single ffprobe call
type VideoFullProber interface {
	ProbeVideoFull(ctx context.Context, path string) (*VideoFullResult, error)
}

func validatePrequeueVideoProbe(result *VideoFullResult) error {
	if result == nil {
		return fmt.Errorf("metadata probe returned no result")
	}
	if strings.TrimSpace(result.VideoCodec) == "" {
		return fmt.Errorf("metadata probe found no playable video track")
	}
	return nil
}

func probeResolvedCandidate(ctx context.Context, prober VideoFullProber, resolution *models.PlaybackResolution) (*VideoFullResult, error) {
	if resolution != nil && resolution.Probe != nil {
		return resolution.Probe, nil
	}
	if prober == nil || resolution == nil {
		return nil, nil
	}
	return prober.ProbeVideoFull(ctx, resolution.WebDAVPath)
}

func validatePrequeueEpisodeDuration(mediaType string, episode *models.EpisodeReference, durationSeconds float64) error {
	if mediaType != "series" || episode == nil || episode.RuntimeMinutes <= 0 || durationSeconds <= 0 {
		return nil
	}
	expectedSeconds := float64(episode.RuntimeMinutes * 60)
	maximumSeconds := expectedSeconds * 3
	if durationSeconds > maximumSeconds {
		return fmt.Errorf(
			"probed duration %.2fs exceeds 3x the expected %dm episode runtime",
			durationSeconds,
			episode.RuntimeMinutes,
		)
	}
	return nil
}

// HLSCreator interface for creating HLS sessions
type HLSCreator interface {
	CreateHLSSession(ctx context.Context, path string, hasDV bool, dvProfile string, hasHDR bool, audioTrackIndex int, subtitleTrackIndex int, profileID string, startOffset float64, prequeueType string) (*HLSSessionResult, error)
	// LinkHLSSessionPrequeue tags a session created by the prequeue worker with
	// its prequeue ID so end-to-end latency can be measured at first frame.
	LinkHLSSessionPrequeue(sessionID, prequeueID string)
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
	return trackselect.SanitizeLanguageCode(code)
}

// normalizeSubtitleMode maps legacy subtitle mode values to canonical ones.
func normalizeSubtitleMode(mode string) string {
	return trackselect.NormalizeSubtitleMode(mode)
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

// SetUsersService enables authenticated profile ownership enforcement.
func (h *PrequeueHandler) SetUsersService(svc prequeueOwnershipService) {
	h.users = svc
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

// SetPlaybackLatencyTracker wires click→first-frame instrumentation.
func (h *PrequeueHandler) SetPlaybackLatencyTracker(t *PlaybackLatencyTracker) {
	h.latencyTracker = t
}

func (h *PrequeueHandler) SetBadStreamsService(svc *badstreams.Service) {
	h.badStreamsSvc = svc
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
	// Never replace an entry that is still resolving: that is usually the
	// details-page prequeue a client is polling, and replacing it cancels its
	// search and leaves the client polling a deleted ID.
	entry, err := h.store.CreateScopedUnlessInProgress(titleID, titleName, userID, mediaType, year, targetEpisode, "prewarm", settingsScopeKey)
	if err != nil {
		return entry.ID, fmt.Errorf("%w: %s (status=%s)", err, entry.ID, entry.Status)
	}

	// Run worker synchronously (blocking)
	h.runPrequeueWorker(entry.ID, titleID, titleName, imdbID, mediaType, year, userID, clientID, nil, targetEpisode, 0, true, prequeueWorkerScheduledPrewarm)

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

// invalidClientIDs are sentinel values that must never be used as a client ID.
// "unknown" is what react-native-device-info's getUniqueId() returns on
// unsupported platforms (notably web); the rest guard against empty-ish values.
var invalidClientIDs = map[string]struct{}{
	"unknown": {}, "null": {}, "undefined": {}, "0": {},
}

// normalizeClientID trims a candidate client ID and drops sentinel values,
// returning "" when the value should be treated as "no client".
func normalizeClientID(raw string) string {
	id := strings.TrimSpace(raw)
	if _, bad := invalidClientIDs[strings.ToLower(id)]; bad {
		return ""
	}
	return id
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
	if !h.canAccessUser(r, req.UserID) {
		http.Error(w, "user not found", http.StatusNotFound)
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
	if mediaType == "series" || mediaType == "tv" || mediaType == "show" {
		titleName = normalizePrequeueSeriesTitle(titleName)
	}

	// Canonicalize the title ID so the same show resolves to one prequeue key
	// regardless of which shelf (Continue Watching vs Top Ten/Trending, etc.) it
	// was opened from. Different entry points hand us different provider forms
	// (bare TMDB number, tmdb:tv:N, tvdb:series:N, imdb:ttN); without this the
	// store lookup misses and we needlessly re-resolve a stream we already have.
	if canonical := canonicalPrequeueTitleID(mediaType, req.TitleID, req.ImdbID, req.TmdbID, req.TvdbID); canonical != req.TitleID {
		log.Printf("[prequeue] Canonicalized titleId %q -> %q (imdb=%q tmdb=%q tvdb=%q)", req.TitleID, canonical, req.ImdbID, req.TmdbID, req.TvdbID)
		req.TitleID = canonical
	}

	// Get client ID from request body or header. Reject the "unknown" sentinel
	// (emitted by react-native-device-info's getUniqueId() on unsupported
	// platforms) so a bogus, shared client ID never collapses per-client
	// settings into a single junk scope.
	clientID := normalizeClientID(req.ClientID)
	if clientID == "" {
		clientID = normalizeClientID(r.Header.Get("X-Client-ID"))
	}

	log.Printf("[prequeue] Received request: titleId=%s titleName=%q userId=%s clientId=%s mediaType=%s", req.TitleID, titleName, req.UserID, clientID, mediaType)

	// For series, determine the target episode based on watch history
	var targetEpisode *models.EpisodeReference
	if mediaType == "series" || mediaType == "tv" || mediaType == "show" {
		// If episode was explicitly provided, use it
		if req.SeasonNumber >= 0 && req.EpisodeNumber > 0 {
			targetEpisode = &models.EpisodeReference{
				Numbering:             req.Numbering,
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

	throughput := adaptiveThroughputFromRequest(r)
	settingsScopeKey, adaptiveCaps := h.prequeueSettingsScope(req.UserID, clientID, req.TitleID, throughput)
	log.Printf("[prequeue] Effective settings scope for title=%s user=%s client=%s: %s", req.TitleID, req.UserID, clientID, settingsScopeKey)

	// Check for pre-warmed entry before creating a new one
	if h.prewarmSvc != nil {
		if warm := h.prewarmSvc.GetWarmScoped(req.TitleID, req.UserID, settingsScopeKey); warm != nil && warm.PrequeueID != "" {
			if warmEntry, ok := h.store.Get(warm.PrequeueID); ok && warmEntry.Status == playback.PrequeueStatusReady && hasReusablePreparation(warmEntry) {
				if !readyEntryFitsAdaptiveCaps(warmEntry, adaptiveCaps) {
					log.Printf("[prequeue] Ignoring pre-warmed entry %s: file size %d exceeds current adaptive throughput cap, resolving fresh",
						warm.PrequeueID, warmEntry.FileSize)
				} else if err := h.validateReadyEntryForReuse(r.Context(), warmEntry); err != nil {
					log.Printf("[prequeue] Ignoring pre-warmed entry %s: stale external stream (%v), resolving fresh",
						warm.PrequeueID, err)
					h.store.Delete(warm.PrequeueID)
				} else {
					if prequeueEpisodeMatches(targetEpisode, warmEntry.TargetEpisode) {
						if req.Reason == playback.ManualPrequeueReason {
							h.store.MakePersistent(warmEntry.ID)
							h.prewarmSvc.AdoptEntry(warmEntry.ID)
							h.prewarmSvc.UpdateFromPrequeue(warmEntry.ID)
						}
						log.Printf("[prequeue] Using pre-warmed entry %s for title=%s user=%s scope=%s", warm.PrequeueID, req.TitleID, req.UserID, settingsScopeKey)
						h.latencyTracker.NotePrequeueRequested(warm.PrequeueID, req.TitleID, req.UserID, titleName, mediaType)
						h.latencyTracker.NotePrequeueMetadata(warm.PrequeueID, req.ImdbID, req.Year)
						// A reused ready entry costs the client no prequeue wait: stamp
						// t1=now so the measured sample is complete (prequeueMs≈0) instead
						// of complete=false with prequeueMs=-1.
						h.latencyTracker.NotePrequeueReady(warm.PrequeueID)
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
					log.Printf("[prequeue] Ignoring pre-warmed entry %s: not ready or missing reusable preparation metadata, resolving fresh",
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
			if existing.StreamPath != "" && hasReusablePreparation(existing) {
				if !readyEntryFitsAdaptiveCaps(existing, adaptiveCaps) {
					log.Printf("[prequeue] Discarding ready entry %s: file size %d exceeds current adaptive throughput cap, resolving fresh",
						existing.ID, existing.FileSize)
				} else if err := h.validateReadyEntryForReuse(r.Context(), existing); err != nil {
					log.Printf("[prequeue] Discarding ready entry %s: stale external stream (%v), resolving fresh",
						existing.ID, err)
					h.store.Delete(existing.ID)
				} else {
					if req.Reason == playback.ManualPrequeueReason {
						h.store.MakePersistent(existing.ID)
						if h.prewarmSvc != nil {
							h.prewarmSvc.AdoptEntry(existing.ID)
							h.prewarmSvc.UpdateFromPrequeue(existing.ID)
						}
					}
					log.Printf("[prequeue] Reusing existing ready entry %s for title=%s user=%s scope=%s", existing.ID, req.TitleID, req.UserID, settingsScopeKey)
					h.latencyTracker.NotePrequeueRequested(existing.ID, req.TitleID, req.UserID, titleName, mediaType)
					h.latencyTracker.NotePrequeueMetadata(existing.ID, req.ImdbID, req.Year)
					// A reused ready entry costs the client no prequeue wait: stamp
					// t1=now so the measured sample is complete (prequeueMs≈0) instead
					// of complete=false with prequeueMs=-1.
					h.latencyTracker.NotePrequeueReady(existing.ID)
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
				log.Printf("[prequeue] Existing ready entry %s missing stream path or reusable preparation metadata, resolving fresh", existing.ID)
			}
		} else if isPrequeueInProgress(existing.Status) {
			if req.Reason == playback.ManualPrequeueReason {
				h.store.MakePersistent(existing.ID)
			}
			log.Printf("[prequeue] Reusing existing in-progress entry %s status=%s for title=%s user=%s scope=%s",
				existing.ID, existing.Status, req.TitleID, req.UserID, settingsScopeKey)
			h.latencyTracker.NotePrequeueRequested(existing.ID, req.TitleID, req.UserID, titleName, mediaType)
			h.latencyTracker.NotePrequeueMetadata(existing.ID, req.ImdbID, req.Year)
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
	h.latencyTracker.NotePrequeueRequested(entry.ID, req.TitleID, req.UserID, titleName, mediaType)
	h.latencyTracker.NotePrequeueMetadata(entry.ID, req.ImdbID, req.Year)

	// Register with prewarm so it keeps the entry alive via dynamic TTL
	if h.prewarmSvc != nil {
		h.prewarmSvc.AdoptEntry(entry.ID)
	}

	// Start background worker with all the info needed for search
	go h.runPrequeueWorker(entry.ID, req.TitleID, titleName, req.ImdbID, mediaType, req.Year, req.UserID, clientID, throughput, targetEpisode, req.StartOffset, req.SkipHLS, prequeueWorkerInteractive)

	// Return response
	resp := playback.PrequeueResponse{
		PrequeueID:    entry.ID,
		TargetEpisode: targetEpisode,
		Status:        playback.PrequeueStatusQueued,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func normalizePrequeueSeriesTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	cleaned := strings.TrimSpace(seriesDisplayLabelRE.ReplaceAllString(trimmed, ""))
	if cleaned != "" {
		return cleaned
	}
	return trimmed
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
	if !h.authorizeEntry(w, r, entry) {
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

type manualPrequeueStatusResponse struct {
	Prequeued  bool   `json:"prequeued"`
	PrequeueID string `json:"prequeueId,omitempty"`
}

func (h *PrequeueHandler) manualPrequeueEntry(r *http.Request) (*playback.PrequeueEntry, string, bool) {
	query := r.URL.Query()
	userID := strings.TrimSpace(query.Get("userId"))
	titleID := strings.TrimSpace(query.Get("titleId"))
	if userID == "" || titleID == "" || !h.canAccessUser(r, userID) {
		return nil, userID, false
	}
	canonicalID := canonicalPrequeueTitleID(
		query.Get("mediaType"),
		titleID,
		query.Get("imdbId"),
		query.Get("tmdbId"),
		query.Get("tvdbId"),
	)
	for _, entry := range h.store.ListAll() {
		if entry != nil && entry.Persistent && entry.UserID == userID && entry.TitleID == canonicalID {
			return entry, userID, true
		}
	}
	return nil, userID, true
}

// ManualPrequeueStatus reports whether a title is pinned for the requested profile.
func (h *PrequeueHandler) ManualPrequeueStatus(w http.ResponseWriter, r *http.Request) {
	entry, userID, valid := h.manualPrequeueEntry(r)
	if !valid {
		if userID == "" || strings.TrimSpace(r.URL.Query().Get("titleId")) == "" {
			http.Error(w, "userId and titleId are required", http.StatusBadRequest)
			return
		}
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	response := manualPrequeueStatusResponse{Prequeued: entry != nil}
	if entry != nil {
		response.PrequeueID = entry.ID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// RemoveManualPrequeue removes a persistent prequeue without exposing other profiles' entries.
func (h *PrequeueHandler) RemoveManualPrequeue(w http.ResponseWriter, r *http.Request) {
	entry, userID, valid := h.manualPrequeueEntry(r)
	if !valid {
		if userID == "" || strings.TrimSpace(r.URL.Query().Get("titleId")) == "" {
			http.Error(w, "userId and titleId are required", http.StatusBadRequest)
			return
		}
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if entry == nil {
		http.Error(w, "manual prequeue not found", http.StatusNotFound)
		return
	}
	h.store.Delete(entry.ID)
	w.WriteHeader(http.StatusNoContent)
}

type adoptMigrationRequest struct {
	StreamPath          string             `json:"streamPath"`
	Result              models.NZBResult   `json:"result"`
	SelectedResultIndex int                `json:"selectedResultIndex"`
	FileSize            int64              `json:"fileSize,omitempty"`
	HealthStatus        string             `json:"healthStatus,omitempty"`
	MigrationCandidates []models.NZBResult `json:"migrationCandidates,omitempty"`
	PassthroughName     string             `json:"passthroughName,omitempty"`
	PassthroughDesc     string             `json:"passthroughDescription,omitempty"`
	ResultAttributes    map[string]string  `json:"resultAttributes,omitempty"`
}

// AdoptMigration replaces a ready prequeue's stream payload after native playback
// migrates to another candidate. This keeps details-page reuse aligned with the
// stream the player actually handed over to.
func (h *PrequeueHandler) AdoptMigration(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	prequeueID := strings.TrimSpace(vars["prequeueID"])
	if prequeueID == "" {
		http.Error(w, "prequeueID is required", http.StatusBadRequest)
		return
	}
	current, exists := h.store.Get(prequeueID)
	if !exists || !h.authorizeEntry(w, r, current) {
		return
	}

	var req adoptMigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.StreamPath = strings.TrimSpace(req.StreamPath)
	if req.StreamPath == "" {
		http.Error(w, "streamPath is required", http.StatusBadRequest)
		return
	}
	if isM2TSStreamPath(req.StreamPath) {
		http.Error(w, "unsupported .m2ts migration source", http.StatusUnprocessableEntity)
		return
	}
	if isExternalStreamPath(req.StreamPath) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := h.ValidateExternalURL(checkCtx, req.StreamPath)
		cancel()
		if err != nil {
			http.Error(w, "streamPath is not an allowed external URL", http.StatusBadRequest)
			return
		}
	}
	h.store.CancelWork(prequeueID)

	updated := h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.StreamPath = req.StreamPath
		e.HLSSessionID = ""
		e.HLSPlaylistURL = ""
		e.HasDolbyVision = false
		e.HasHDR10 = false
		e.DolbyVisionProfile = ""
		e.DolbyVisionConfiguration = nil
		e.NeedsAudioTranscode = false
		e.SelectedAudioTrack = -1
		e.SelectedSubtitleTrack = -1
		e.TrackSelection = nil
		e.AudioTracks = nil
		e.SubtitleTracks = nil
		e.SubtitleSessions = nil
		e.Error = ""
		e.MigrationAdopted = true

		if req.Result.Title != "" || req.Result.GUID != "" {
			resultCopy := req.Result
			e.SelectedResult = &resultCopy
			e.SelectedResultIndex = req.SelectedResultIndex
			e.ServiceType = string(req.Result.ServiceType)
			e.ResultAttributes = req.Result.Attributes
			if len(req.ResultAttributes) > 0 {
				e.ResultAttributes = req.ResultAttributes
			}
			if req.Result.SizeBytes > 0 {
				e.FileSize = req.Result.SizeBytes
			}
		}
		if e.ServiceType == "" {
			lowerPath := strings.ToLower(req.StreamPath)
			if strings.HasPrefix(lowerPath, "/debrid/") || strings.Contains(lowerPath, "/debrid/") {
				e.ServiceType = "debrid"
			} else {
				e.ServiceType = "usenet"
			}
		}
		if req.FileSize > 0 {
			e.FileSize = req.FileSize
		}
		if req.HealthStatus != "" {
			e.HealthStatus = req.HealthStatus
		} else if e.HealthStatus == "" {
			e.HealthStatus = "migrated"
		}
		if len(req.MigrationCandidates) > 0 {
			e.MigrationCandidates = append([]models.NZBResult(nil), req.MigrationCandidates...)
		}
		attrs := e.ResultAttributes
		if attrs != nil && attrs["passthrough_format"] == "true" {
			e.PassthroughName = attrs["raw_name"]
			e.PassthroughDescription = attrs["raw_description"]
		} else {
			e.PassthroughName = strings.TrimSpace(req.PassthroughName)
			e.PassthroughDescription = strings.TrimSpace(req.PassthroughDesc)
		}
	})
	if !updated {
		http.Error(w, "prequeue not found or expired", http.StatusNotFound)
		return
	}
	h.refreshAdoptedMigrationMetadata(prequeueID, req.StreamPath, normalizeClientID(r.Header.Get("X-Client-ID")))
	h.latencyTracker.NotePrequeueReady(prequeueID)
	if h.prewarmSvc != nil {
		h.prewarmSvc.UpdateFromPrequeue(prequeueID)
	}

	entry, exists := h.store.Get(prequeueID)
	if !exists {
		http.Error(w, "prequeue not found or expired", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry.ToResponse())
}

type adoptedMigrationMetadata struct {
	audioStreams             []AudioStreamInfo
	subtitleStreams          []SubtitleStreamInfo
	hasDolbyVision           bool
	hasHDR10                 bool
	dolbyVisionProfile       string
	dolbyVisionConfiguration *models.DolbyVisionConfiguration
	hasTrueHD                bool
	duration                 float64
	avgFrameRate             string
}

func (h *PrequeueHandler) refreshAdoptedMigrationMetadata(prequeueID, streamPath, clientID string) {
	if h == nil || h.store == nil {
		return
	}
	streamPath = strings.TrimSpace(streamPath)
	if streamPath == "" {
		return
	}
	if h.fullProber == nil && h.metadataProber == nil && h.videoProber == nil {
		return
	}

	entry, ok := h.store.Get(prequeueID)
	if !ok || strings.TrimSpace(entry.StreamPath) != streamPath {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	metadata, err := h.probeAdoptedMigrationMetadata(ctx, streamPath)
	if err != nil {
		log.Printf("[prequeue] adopted migration metadata probe failed for %s: %v", prequeueID, err)
		return
	}

	trackPrefs := h.trackPreferenceResolver().Resolve(entry.UserID, clientID, entry.TitleID)
	trackSelection := trackselect.Select(metadata.audioStreams, metadata.subtitleStreams, trackPrefs)
	selectedAudioTrack := trackSelection.AudioStreamIndex()
	selectedSubtitleTrack := trackSelection.SubtitleStreamIndex()
	audioTracks := playbackAudioTracksFromStreams(metadata.audioStreams)
	subtitleTracks := playbackSubtitleTracksFromStreams(metadata.subtitleStreams)

	h.store.Update(prequeueID, func(e *playback.PrequeueEntry) {
		if strings.TrimSpace(e.StreamPath) != streamPath {
			return
		}
		if metadata.duration > 0 {
			e.Duration = metadata.duration
		}
		e.FrameRate = metadata.avgFrameRate
		e.HasDolbyVision = metadata.hasDolbyVision
		e.HasHDR10 = metadata.hasHDR10
		e.DolbyVisionProfile = metadata.dolbyVisionProfile
		e.DolbyVisionConfiguration = metadata.dolbyVisionConfiguration
		e.NeedsAudioTranscode = metadata.hasTrueHD
		e.SelectedAudioTrack = selectedAudioTrack
		e.SelectedSubtitleTrack = selectedSubtitleTrack
		e.TrackSelection = trackSelection
		e.AudioTracks = audioTracks
		e.SubtitleTracks = subtitleTracks
		e.SubtitleSessions = nil
		e.Error = ""
		e.MigrationAdopted = true
	})
	log.Printf("[prequeue] refreshed adopted migration metadata prequeue=%s audioTracks=%d subtitleTracks=%d selectedAudio=%d selectedSubtitle=%d duration=%.2fs",
		prequeueID, len(audioTracks), len(subtitleTracks), selectedAudioTrack, selectedSubtitleTrack, metadata.duration)
}

func (h *PrequeueHandler) probeAdoptedMigrationMetadata(ctx context.Context, streamPath string) (adoptedMigrationMetadata, error) {
	var metadata adoptedMigrationMetadata
	if h.fullProber != nil {
		fullResult, err := h.fullProber.ProbeVideoFull(ctx, streamPath)
		if err != nil {
			return metadata, err
		}
		if fullResult != nil {
			metadata.audioStreams = fullResult.AudioStreams
			metadata.subtitleStreams = fullResult.SubtitleStreams
			metadata.hasDolbyVision = fullResult.HasDolbyVision
			metadata.hasHDR10 = fullResult.HasHDR10
			metadata.dolbyVisionProfile = fullResult.DolbyVisionProfile
			metadata.dolbyVisionConfiguration = fullResult.DolbyVisionConfiguration
			metadata.hasTrueHD = fullResult.HasTrueHD
			metadata.duration = fullResult.Duration
			metadata.avgFrameRate = fullResult.AvgFrameRate
		}
		return metadata, nil
	}

	var lastErr error
	if h.metadataProber != nil {
		result, err := h.metadataProber.ProbeVideoMetadata(ctx, streamPath)
		if err != nil {
			lastErr = err
		} else if result != nil {
			metadata.audioStreams = result.AudioStreams
			metadata.subtitleStreams = result.SubtitleStreams
		}
	}
	if h.videoProber != nil {
		result, err := h.videoProber.ProbeVideoPath(ctx, streamPath)
		if err != nil {
			lastErr = err
		} else if result != nil {
			metadata.hasDolbyVision = result.HasDolbyVision
			metadata.hasHDR10 = result.HasHDR10
			metadata.dolbyVisionProfile = result.DolbyVisionProfile
			metadata.dolbyVisionConfiguration = result.DolbyVisionConfiguration
		}
	}
	if len(metadata.audioStreams) == 0 && len(metadata.subtitleStreams) == 0 && !metadata.hasDolbyVision && !metadata.hasHDR10 && lastErr != nil {
		return metadata, lastErr
	}
	return metadata, nil
}

// TrackPreferenceResolver returns a track-preference resolver backed by the
// settings services configured on this handler, for sharing with other
// playback entry points (manual resolve).
func (h *PrequeueHandler) TrackPreferenceResolver() *trackselect.Resolver {
	return h.trackPreferenceResolver()
}

// trackPreferenceResolver builds the shared track-preference resolver from
// whichever settings services are configured.
func (h *PrequeueHandler) trackPreferenceResolver() *trackselect.Resolver {
	resolver := &trackselect.Resolver{}
	if h == nil {
		return resolver
	}
	if h.configManager != nil {
		resolver.Config = h.configManager
	}
	if h.userSettingsSvc != nil {
		resolver.UserSettings = h.userSettingsSvc
	}
	if h.clientSettingsSvc != nil {
		resolver.ClientSettings = h.clientSettingsSvc
	}
	if h.contentPreferencesSvc != nil {
		resolver.ContentPreferences = h.contentPreferencesSvc
	}
	return resolver
}

func playbackAudioTracksFromStreams(streams []AudioStreamInfo) []playback.AudioTrackInfo {
	if len(streams) == 0 {
		return nil
	}
	tracks := make([]playback.AudioTrackInfo, len(streams))
	for i, stream := range streams {
		tracks[i] = playback.AudioTrackInfo{
			Index:    stream.Index,
			Language: stream.Language,
			Codec:    stream.Codec,
			Profile:  stream.Profile,
			Title:    stream.Title,
		}
	}
	return tracks
}

func playbackSubtitleTracksFromStreams(streams []SubtitleStreamInfo) []playback.SubtitleTrackInfo {
	if len(streams) == 0 {
		return nil
	}
	tracks := make([]playback.SubtitleTrackInfo, len(streams))
	for i, stream := range streams {
		tracks[i] = playback.SubtitleTrackInfo{
			Index:         stream.Index,
			AbsoluteIndex: stream.Index,
			Language:      stream.Language,
			Title:         stream.Title,
			Codec:         stream.Codec,
			Forced:        stream.IsForced,
			IsBitmap:      trackselect.IsBitmapSubtitleCodec(stream.Codec),
		}
	}
	return tracks
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

type prequeueWorkerMode int

const (
	prequeueWorkerInteractive prequeueWorkerMode = iota
	prequeueWorkerScheduledPrewarm
)

func resolveFirstReadySourceForWorker(configured bool, mode prequeueWorkerMode) bool {
	return configured && mode == prequeueWorkerInteractive
}

// runPrequeueWorker runs the prequeue background task. Interactive prequeues
// honor Resolve First Ready Source; scheduled pre-warm workers always wait for
// the complete globally ranked candidate set before resolving.
func (h *PrequeueHandler) runPrequeueWorker(prequeueID, titleID, titleName, imdbID, mediaType string, year int, userID, clientID string, throughput *models.AdaptiveThroughputContext, targetEpisode *models.EpisodeReference, startOffset float64, skipHLS bool, workerMode prequeueWorkerMode) {
	// Create cancellable context
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Store cancel func for potential cancellation
	h.store.SetCancelFunc(prequeueID, cancel)

	workerStart := time.Now()
	log.Printf("[prequeue] TIMING: worker started for %s (title=%q)", prequeueID, titleName)

	// Update status to searching
	if !h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusSearching
		e.ProgressStage = "metadata"
		e.ProgressDetail = ""
		e.ProgressCurrent = 0
		e.ProgressTotal = 0
	}) {
		log.Printf("[prequeue] Worker stopped before search because entry %s was replaced", prequeueID)
		return
	}

	// Create episode resolver for TV shows to enable accurate pack size filtering
	// Also lookup absolute episode number, daily show info, and anime detection if not provided
	var episodeResolver *filter.SeriesEpisodeResolver
	var isDaily bool
	var isAnime bool
	var targetAirDate string
	var episodeAirYear int
	var seasonPremiereYear int
	var episodeReleased bool
	var countryCode string
	var originalLanguage string
	var tvdbID int64
	var alternateTitles []string
	if mediaType == "series" && h.metadataSvc != nil {
		seriesMeta := h.createEpisodeResolverAndLookupAbsoluteEp(ctx, titleID, titleName, year, imdbID, targetEpisode)
		episodeResolver = seriesMeta.EpisodeResolver
		targetEpisode = seriesMeta.TargetEpisode
		isDaily = seriesMeta.IsDaily
		isAnime = seriesMeta.IsAnime
		targetAirDate = seriesMeta.TargetAirDate
		episodeAirYear = seriesMeta.EpisodeAirYear
		seasonPremiereYear = seriesMeta.SeasonPremiereYear
		episodeReleased = seriesMeta.EpisodeReleased
		countryCode = seriesMeta.CountryCode
		originalLanguage = seriesMeta.OriginalLanguage
		tvdbID = seriesMeta.TVDBID
		if imdbID == "" && seriesMeta.IMDBID != "" {
			imdbID = seriesMeta.IMDBID
			log.Printf("[prequeue] Populated IMDb ID %s from series metadata", imdbID)
		}
		if year == 0 && seriesMeta.Year > 0 {
			year = seriesMeta.Year
			log.Printf("[prequeue] Populated year %d from series metadata", year)
		}
		h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
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
	h.updatePrequeueProgress(prequeueID, "searching", query, 0, 0)

	// For movies, check if the movie is anime by looking at genres
	if mediaType == "movie" && h.movieMetadataSvc != nil {
		movieQuery := models.MovieDetailsQuery{
			TitleID: titleID,
			Name:    titleName,
			Year:    year,
			IMDBID:  imdbID,
		}
		if movieTitle, err := h.movieMetadataSvc.MovieInfo(ctx, movieQuery); err == nil && movieTitle != nil {
			countryCode = strings.TrimSpace(movieTitle.CountryCode)
			originalLanguage = strings.TrimSpace(movieTitle.Language)
			alternateTitles = hydratedMovieSearchTitles(movieTitle)
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
		TitleID:            titleID,
		Query:              query,
		MaxResults:         50,
		MediaType:          mediaType,
		IMDBID:             imdbID,
		TVDBID:             tvdbID,
		AlternateTitles:    alternateTitles,
		Year:               year,
		CountryCode:        countryCode,
		OriginalLanguage:   originalLanguage,
		UserID:             userID,
		ClientID:           clientID,
		AdaptiveThroughput: throughput,
		EpisodeResolver:    episodeResolver,
		IsDaily:            isDaily,
		IsAnime:            isAnime,
		TargetAirDate:      targetAirDate,
		EpisodeAirYear:     episodeAirYear,
		SeasonPremiereYear: seasonPremiereYear,
		EpisodeReleased:    episodeReleased,
		// Concurrent resolution derives its bulk-attempt buckets from the
		// effective Result Order. The indexer remains the source of truth for
		// those lexicographic criterion values.
		IncludeScoreBreakdown: true,
	}
	if targetEpisode != nil {
		searchOpts.Numbering = targetEpisode.Numbering
	}
	// Pass absolute episode number for anime matching (if available)
	if targetEpisode != nil && targetEpisode.AbsoluteEpisodeNumber > 0 {
		searchOpts.AbsoluteEpisodeNumber = targetEpisode.AbsoluteEpisodeNumber
	}

	// Load filter settings for DV profile compatibility checking and track
	// selection. Priority: client settings > user settings > global settings >
	// default. Computed before the resolution phase starts, since resolution now
	// begins while the split search is still feeding.
	var hdrDVPolicy models.HDRDVPolicy
	unknownTrackPolicy := "none"
	var allowedTrackLanguages []string
	var playbackDefaults models.UserSettings
	resolveFirstReadySource := false
	explicitAudioLanguage := ""

	// Layer 1: Start with global settings
	if h.configManager != nil {
		globalSettings, err := h.configManager.Load()
		if err == nil {
			resolveFirstReadySource = resolveFirstReadySourceForWorker(globalSettings.Streaming.ResolveFirstReadySource, workerMode)
			if globalSettings.Streaming.ResolveFirstReadySource && workerMode == prequeueWorkerScheduledPrewarm {
				log.Printf("[prequeue] Scheduled pre-warm forcing complete ranked search (Resolve First Ready Source ignored)")
			}
			hdrDVPolicy = models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy)
			unknownTrackPolicy = string(globalSettings.Filtering.UnknownTrackPolicy)
			allowedTrackLanguages = normalizeAllowedTrackLanguages(globalSettings.Playback.AllowedTrackLanguages)
			playbackDefaults.Playback.AllowedTrackLanguages = models.StringSlicePtr(allowedTrackLanguages)
		}
	}

	// Layer 2: User settings override global
	if h.userSettingsSvc != nil {
		userSettings, err := h.userSettingsSvc.Get(userID)
		if err == nil && userSettings != nil {
			explicitAudioLanguage = strings.TrimSpace(userSettings.Playback.PreferredAudioLanguage)
		}
		if err == nil && userSettings != nil && userSettings.Filtering.HDRDVPolicy != "" {
			hdrDVPolicy = userSettings.Filtering.HDRDVPolicy
		}
		if err == nil && userSettings != nil && userSettings.Filtering.UnknownTrackPolicy != "" {
			unknownTrackPolicy = userSettings.Filtering.UnknownTrackPolicy
		}
		if effectiveSettings, effectiveErr := h.userSettingsSvc.GetWithDefaults(userID, playbackDefaults); effectiveErr == nil {
			allowedTrackLanguages = normalizeAllowedTrackLanguages(copyOptionalStringSlice(effectiveSettings.Playback.AllowedTrackLanguages))
		}
	}

	// Layer 3: Client/device settings override user
	if clientID != "" && userID != "" && h.clientSettingsSvc != nil {
		clientSettings, err := h.clientSettingsSvc.Get(clientID, userID)
		if err == nil && clientSettings != nil && clientSettings.PreferredAudioLanguage != nil {
			explicitAudioLanguage = strings.TrimSpace(*clientSettings.PreferredAudioLanguage)
		}
		if err == nil && clientSettings != nil && clientSettings.HDRDVPolicy != nil {
			hdrDVPolicy = *clientSettings.HDRDVPolicy
			log.Printf("[prequeue] Using client-specific HDR/DV policy: %s", hdrDVPolicy)
		}
		if err == nil && clientSettings != nil && clientSettings.UnknownTrackPolicy != nil {
			unknownTrackPolicy = *clientSettings.UnknownTrackPolicy
			log.Printf("[prequeue] Using client-specific unknown track policy: %s", unknownTrackPolicy)
		}
		if err == nil && clientSettings != nil && clientSettings.AllowedTrackLanguages != nil {
			allowedTrackLanguages = normalizeAllowedTrackLanguages(*clientSettings.AllowedTrackLanguages)
			log.Printf("[prequeue] Using client-specific allowed track languages: %v", allowedTrackLanguages)
		}
	}

	if explicitAudioLanguage != "" && resolveFirstReadySource {
		log.Printf("[prequeue] Explicit audio preference %q requires complete ranked search and sequential resolution; ignoring first-ready-source mode", explicitAudioLanguage)
		resolveFirstReadySource = false
	}

	// Default to allowing all content
	if hdrDVPolicy == "" {
		hdrDVPolicy = models.HDRDVPolicyIncludeHDRDV
	}
	unknownTrackPolicy = normalizeUnknownTrackPolicy(unknownTrackPolicy)
	needsDVCheck := hdrDVPolicy == models.HDRDVPolicyIncludeHDR
	needsUnknownTrackCheck := unknownTrackPolicyNeedsProbe(unknownTrackPolicy)
	log.Printf("[prequeue] HDR/DV policy: %s, needsDVCheck: %v, unknownTrackPolicy: %s, needsUnknownTrackCheck: %v, allowedTrackLanguages: %v", hdrDVPolicy, needsDVCheck, unknownTrackPolicy, needsUnknownTrackCheck, allowedTrackLanguages)

	if h.indexerSvc == nil {
		h.failPrequeue(prequeueID, "search service not configured")
		return
	}

	var (
		candidates   prequeueCandidateSource
		feedState    *prequeueFeedState
		finishSearch = func() {}
	)
	if resolveFirstReadySource {
		// Opt-in latency mode: publish each source's locally ranked candidates as
		// soon as that source completes. Completion order intentionally wins over
		// combined cross-source ranking in this mode.
		feedCtx, cancelFeed := context.WithCancel(ctx)
		feedState = newPrequeueFeedState()
		usenetCh, debridCh := h.indexerSvc.SearchWithScoringSplit(feedCtx, searchOpts)
		streamCandidates := newStreamCandidateSource()
		candidates = streamCandidates
		go h.prequeueSearchFeeder(feedCtx, streamCandidates, usenetCh, debridCh, feedState, prequeueFeederConfig{
			prequeueID:    prequeueID,
			maxCandidates: searchOpts.MaxResults,
			targetEpisode: targetEpisode,
			workerStart:   workerStart,
		})
		finishSearch = func() {
			cancelFeed()
			streamCandidates.Stop()
			select {
			case <-feedState.done:
			case <-ctx.Done():
			}
		}
	} else {
		// Default/original behavior: wait for every enabled source, combine and
		// rank globally, then preserve that complete ordered list for migration.
		allResults, badStreamCount, totalResults, searchErr := h.searchCombinedPrequeueCandidates(ctx, searchOpts, targetEpisode)
		if searchErr != nil {
			h.failPrequeue(prequeueID, searchErr.Error())
			return
		}
		if len(allResults) == 0 {
			errMsg := "no results found"
			if badStreamCount > 0 {
				errMsg = "all results are filtered or marked bad"
			}
			h.failPrequeue(prequeueID, errMsg)
			return
		}
		if h.playbackSvc != nil {
			allResults = h.playbackSvc.PrepareTorrentCandidates(ctx, allResults)
		}
		candidates = newSliceCandidateSource(allResults)
		h.updatePrequeueProgress(prequeueID, "ranking", "", len(allResults), totalResults)
		log.Printf("[prequeue] TIMING: combined scored search complete, %d passed candidate(s) selected from %d total result(s), badStreams=%d (elapsed: %v)",
			len(allResults), totalResults, badStreamCount, time.Since(workerStart))
	}

	// Update status to resolving. Numeric progress from here on is owned by the
	// race (in-flight candidate window); the feeder only advances the stage.
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusResolving
		e.ProgressStage = "preparing_candidates"
		e.ProgressDetail = ""
		e.ProgressCurrent = 0
		e.ProgressTotal = candidates.Total()
	})

	// Resolution phase. First-ready mode races the streaming candidates with a
	// bounded worker pool. The default combined-search mode walks the globally
	// ranked list sequentially and stops at the first fully validated candidate.
	// Deprioritized unknown-track results are kept as a fallback and used only
	// when nothing validates.
	resolveStart := time.Now()
	log.Printf("[prequeue] TIMING: starting resolution phase (first-ready-source=%t, candidates=%d, width=%d, elapsed: %v)",
		resolveFirstReadySource, candidates.Total(), prequeueResolutionWidth(resolveFirstReadySource), time.Since(workerStart))

	choice, resolveErr := h.resolveCandidates(ctx, prequeueID, candidates, prequeueResolutionOptions{
		mediaType:              mediaType,
		targetEpisode:          targetEpisode,
		userID:                 userID,
		hdrDVPolicy:            hdrDVPolicy,
		unknownTrackPolicy:     unknownTrackPolicy,
		needsDVCheck:           needsDVCheck,
		needsUnknownTrackCheck: needsUnknownTrackCheck,
		concurrent:             resolveFirstReadySource,
		workerStart:            workerStart,
	})

	// In first-ready mode this aborts any remaining split search and joins its
	// feeder. In combined mode every source already completed, so it is a no-op.
	finishSearch()

	if choice.resolution == nil {
		if errors.Is(resolveErr, context.Canceled) || errors.Is(resolveErr, context.DeadlineExceeded) {
			h.failPrequeue(prequeueID, "cancelled")
		} else if errors.Is(resolveErr, errNoSearchCandidates) {
			errMsg := "no results found"
			if feedState != nil {
				badStreams, _ := feedState.snapshot()
				if feedState.allSourcesFailed() {
					errMsg = "all search sources failed"
				} else if badStreams > 0 {
					errMsg = "all results are filtered or marked bad"
				}
			}
			h.failPrequeue(prequeueID, errMsg)
		} else {
			errMsg := "all results failed to resolve"
			if resolveErr != nil {
				errMsg = resolveErr.Error()
			}
			h.failPrequeue(prequeueID, errMsg)
		}
		return
	}

	resolution := choice.resolution
	selectedResult := choice.selectedResult
	selectedResultIndex := choice.selectedResultIndex
	cachedProbeResult := choice.probeResult
	cachedMetadataResult := choice.metadataResult
	log.Printf("[prequeue] TIMING: resolution complete (resolve took: %v, total elapsed: %v)", time.Since(resolveStart), time.Since(workerStart))

	// Update with resolution
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusProbing
		e.ProgressStage = "analyzing_media"
		e.ProgressDetail = ""
		e.ProgressCurrent = 0
		e.ProgressTotal = 0
		e.StreamPath = resolution.WebDAVPath
		e.DebridProvider = resolution.DebridProvider
		if selectedResult != nil {
			e.ServiceType = string(selectedResult.ServiceType)
		}
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
		if selectedResult != nil {
			resultCopy := *selectedResult
			e.SelectedResult = &resultCopy
			e.SelectedResultIndex = selectedResultIndex
		}
		if len(choice.migrationCandidates) > 0 {
			e.MigrationCandidates = append([]models.NZBResult(nil), choice.migrationCandidates...)
		}
	})

	// Tag the latency sample with the exact release so benchmark runs can be
	// compared per release (a different release = a different measurement).
	if h.latencyTracker != nil && selectedResult != nil {
		h.latencyTracker.NotePrequeueRelease(prequeueID, selectedResult.Title)
	}

	// Select audio/subtitle tracks based on user preferences
	selectedAudioTrack := -1
	selectedSubtitleTrack := -1
	probeStart := time.Now()

	if h.metadataProber != nil && h.userSettingsSvc != nil {
		h.updatePrequeueProgress(prequeueID, "selecting_tracks", "", 0, 0)
		log.Printf("[prequeue] TIMING: starting probe/track selection (elapsed: %v)", time.Since(workerStart))
		// Effective track preferences: global < profile < client < per-title.
		trackTitleID := titleID
		if entry, ok := h.store.Get(prequeueID); ok && entry != nil && entry.TitleID != "" {
			trackTitleID = entry.TitleID
		}
		trackPrefs := h.trackPreferenceResolver().Resolve(userID, clientID, trackTitleID)
		log.Printf("[prequeue] Effective track preferences: audioLang=%q, allowedTrackLanguages=%v, subLang=%q, subMode=%q",
			trackPrefs.AudioLanguage, trackPrefs.AllowedLanguages, trackPrefs.SubtitleLanguage, trackPrefs.SubtitleMode)

		// Use combined prober if available (single ffprobe call), otherwise fall back to separate probes
		var audioStreams []AudioStreamInfo
		var subtitleStreams []SubtitleStreamInfo
		var hasDV, hasHDR10 bool
		var hasTrueHD, hasCompatibleAudio bool
		var dvProfile string
		var dvConfiguration *models.DolbyVisionConfiguration
		var avgFrameRate string

		// Reuse cached probe result if we already probed during DV check
		var duration float64
		if cachedProbeResult != nil {
			audioStreams = cachedProbeResult.AudioStreams
			subtitleStreams = cachedProbeResult.SubtitleStreams
			hasDV = cachedProbeResult.HasDolbyVision
			hasHDR10 = cachedProbeResult.HasHDR10
			dvProfile = cachedProbeResult.DolbyVisionProfile
			dvConfiguration = cachedProbeResult.DolbyVisionConfiguration
			hasTrueHD = cachedProbeResult.HasTrueHD
			hasCompatibleAudio = cachedProbeResult.HasCompatibleAudio
			duration = cachedProbeResult.Duration
			avgFrameRate = cachedProbeResult.AvgFrameRate
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
				dvConfiguration = fullResult.DolbyVisionConfiguration
				hasTrueHD = fullResult.HasTrueHD
				hasCompatibleAudio = fullResult.HasCompatibleAudio
				duration = fullResult.Duration
				avgFrameRate = fullResult.AvgFrameRate
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
					dvConfiguration = probeResult.DolbyVisionConfiguration
				}
			}
		}

		// Process track selection using probe results (shared with manual resolve)
		var trackSelection *models.TrackSelection
		if len(audioStreams) > 0 || len(subtitleStreams) > 0 {
			for i, stream := range audioStreams {
				log.Printf("[prequeue] Audio stream[%d]: index=%d codec=%q lang=%q title=%q", i, stream.Index, stream.Codec, stream.Language, stream.Title)
			}
			trackSelection = trackselect.Select(audioStreams, subtitleStreams, trackPrefs)
			selectedAudioTrack = trackSelection.AudioStreamIndex()
			selectedSubtitleTrack = trackSelection.SubtitleStreamIndex()
			log.Printf("[prequeue] Selected tracks: audio=%d subtitle=%d (audioLang=%q subLang=%q subMode=%s)",
				selectedAudioTrack, selectedSubtitleTrack, trackPrefs.AudioLanguage, trackPrefs.SubtitleLanguage, trackPrefs.SubtitleMode)
		}

		// Store selected tracks and duration
		h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
			e.SelectedAudioTrack = selectedAudioTrack
			e.SelectedSubtitleTrack = selectedSubtitleTrack
			e.TrackSelection = trackSelection
			if duration > 0 {
				e.Duration = duration
			}
			e.FrameRate = avgFrameRate
		})

		// Store audio/subtitle track info for UI display
		if len(audioStreams) > 0 || len(subtitleStreams) > 0 {
			audioTracks := playbackAudioTracksFromStreams(audioStreams)
			subtitleTracks := playbackSubtitleTracksFromStreams(subtitleStreams)

			h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
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
		h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
			e.HasDolbyVision = hasDV
			e.HasHDR10 = hasHDR10
			e.DolbyVisionProfile = dvProfile
			e.DolbyVisionConfiguration = dvConfiguration
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
			h.updatePrequeueProgress(prequeueID, "preparing_playback", reason, 0, 0)

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
					h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
						e.HLSSessionID = hlsResult.SessionID
						e.HLSPlaylistURL = hlsResult.PlaylistURL
					})
					if h.hlsCreator != nil {
						h.hlsCreator.LinkHLSSessionPrequeue(hlsResult.SessionID, prequeueID)
					}
					log.Printf("[prequeue] TIMING: HLS session created: %s (HLS took: %v, total elapsed: %v)", hlsResult.SessionID, time.Since(hlsStart), time.Since(workerStart))
				}
			}
		}
		// Note: Subtitle tracks for lazy extraction are already stored above for UI display
	}

	// Mark as ready
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusReady
		e.ProgressStage = "ready"
		e.ProgressDetail = ""
		e.ProgressCurrent = 0
		e.ProgressTotal = 0
	})
	h.latencyTracker.NotePrequeueReady(prequeueID)
	if h.prewarmSvc != nil {
		h.prewarmSvc.UpdateFromPrequeue(prequeueID)
	}

	log.Printf("[prequeue] TIMING: Prequeue %s is ready (TOTAL: %v)", prequeueID, time.Since(workerStart))
}

// prequeueResolutionRaceWidth bounds how many prequeue candidates are resolved
// concurrently during the resolution phase. The source-aware scheduler keeps
// one slot available for the peer service while it may still produce work, so
// up to seven candidates from a first-ready source run until both sources are
// represented; all eight slots can then be used concurrently.
const prequeueResolutionRaceWidth = 8

func prequeueResolutionWidth(concurrent bool) int {
	if concurrent {
		return prequeueResolutionRaceWidth
	}
	return 1
}

// These fallbacks mirror StreamingSettings defaults for handlers constructed
// without a config manager. They only affect concurrent resolution; the default
// ResolveFirstReadySource=false path remains ranked and sequential.
const (
	prequeueResolutionSettleWindowDefault = 250
	prequeueResolutionEndRaceEarlyDefault = true
)

// prequeueCandidateProcessor resolves and validates one prequeue candidate.
// accepted is the fully validated candidate (playable + policy-ok);
// deprioritized is a candidate that passed resolution/probing but was
// deprioritized by the unknown-track policy (usable only as a last resort);
// err reports why the candidate cannot be used (nil for accepted, deprioritized
// or silently skipped candidates).
type prequeueCandidateProcessor func(ctx context.Context, index int, candidate models.NZBResult) (accepted, deprioritized *candidateResolution, err error)

// candidateResolution is the outcome of resolving one prequeue candidate.
type candidateResolution struct {
	result         models.NZBResult
	index          int
	bucket         int
	resolution     *models.PlaybackResolution
	probeResult    *VideoFullResult
	metadataResult *VideoMetadataResult
	// migrationSnapshot is a snapshot of the candidates fed so far taken at
	// accept/deprioritize time, so the worker's MigrationCandidates copy never
	// races with a still-aborting loser or a still-feeding source.
	migrationSnapshot []models.NZBResult
}

// prequeueResolutionChoice is what the resolution phase selected; it mirrors the
// variables the serial loop used to feed the rest of the worker.
type prequeueResolutionChoice struct {
	resolution          *models.PlaybackResolution
	selectedResult      *models.NZBResult
	selectedResultIndex int
	probeResult         *VideoFullResult
	metadataResult      *VideoMetadataResult
	// migrationCandidates is the snapshot to persist for later stream adoption
	// (AdoptMigration). Non-nil on every successful resolution.
	migrationCandidates []models.NZBResult
}

// prequeueResolutionOptions configures the resolution phase.
type prequeueResolutionOptions struct {
	mediaType              string
	targetEpisode          *models.EpisodeReference
	userID                 string
	hdrDVPolicy            models.HDRDVPolicy
	unknownTrackPolicy     string
	needsDVCheck           bool
	needsUnknownTrackCheck bool
	concurrent             bool
	workerStart            time.Time
}

// prequeueCandidateSource yields candidates to the resolution race in feed
// order. The race owns the source: at any time exactly one goroutine consumes
// Next (the race's dispenser), so implementations need no read-side locking.
//
// Two shapes cover both resolution modes:
//   - sliceCandidateSource: a fixed, already-final ranked list (tests and
//     derived callers).
//   - streamCandidateSource: candidates arrive as each search source completes,
//     so the race can begin resolving the first-ready source's
//     candidates while other sources are still feeding.
//
// Total reports how many candidates have been fed so far (a slice never grows;
// a stream grows as its feeder publishes). Snapshot returns a copy of everything
// fed so far, for the migration candidate list persisted on the winning
// prequeue entry.
type prequeueCandidateSource interface {
	Next(ctx context.Context) (idx int, candidate models.NZBResult, ok bool)
	Total() int
	Snapshot() []models.NZBResult
}

type prequeueBucketSource interface {
	bucket(index int) int
}

func prequeueCandidateBucket(src prequeueCandidateSource, index int) int {
	if bucketed, ok := src.(prequeueBucketSource); ok {
		return bucketed.bucket(index)
	}
	return 0
}

// streamedCandidate is one candidate handed to the race alongside its 0-based
// feed index (used for the in-flight progress window and fallback ordering).
type streamedCandidate struct {
	idx    int
	cand   models.NZBResult
	bucket int
}

// prequeueRankedCandidate carries the Result Order bucket used only by the
// concurrent prequeue race. Bucket zero is the most preferred cohort. Service
// Priority is intentionally excluded when these buckets are assigned.
type prequeueRankedCandidate struct {
	result models.NZBResult
	bucket int
}

// sliceCandidateSource serves a fixed candidate list in order, preserving
// ranking for the prequeue's derived/tests callers.
type sliceCandidateSource struct {
	ch  chan streamedCandidate
	n   int
	all []models.NZBResult
}

func newSliceCandidateSource(candidates []models.NZBResult) *sliceCandidateSource {
	ch := make(chan streamedCandidate, len(candidates))
	for i, c := range candidates {
		ch <- streamedCandidate{idx: i, cand: c}
	}
	close(ch)
	return &sliceCandidateSource{ch: ch, n: len(candidates), all: candidates}
}

func (s *sliceCandidateSource) Next(ctx context.Context) (int, models.NZBResult, bool) {
	select {
	case <-ctx.Done():
		return 0, models.NZBResult{}, false
	case it, ok := <-s.ch:
		return it.idx, it.cand, ok
	}
}

func (s *sliceCandidateSource) Total() int { return s.n }

func (s *sliceCandidateSource) Snapshot() []models.NZBResult {
	return append([]models.NZBResult(nil), s.all...)
}

// streamCandidateSource hands candidates to the resolution race as search
// sources publish them. The feeder calls Feed (single feeder at a time) until
// it returns false, which happens once the race has concluded (Stop) or every
// source has settled and the feeder called Close. Snapshot reflects everything
// fed so far, so a winner's migration list always contains the winning
// candidate even when later sources were still feeding (their candidates simply
// never reached the queue).
type streamCandidateSource struct {
	ch      chan streamedCandidate
	done    chan struct{}
	mu      sync.Mutex
	total   int
	acc     []models.NZBResult
	buckets []int
	stopped bool
}

func newStreamCandidateSource() *streamCandidateSource {
	return &streamCandidateSource{ch: make(chan streamedCandidate), done: make(chan struct{})}
}

// Feed publishes one candidate in feed order. It returns false when the source
// has been stopped (the race adopted a winner and no longer consumes) or closed
// (no more candidates will arrive). The mutex is never held across the channel
// send, so Stop/Close can always acquire it and unblock an in-flight Feed.
func (s *streamCandidateSource) Feed(cand models.NZBResult) bool {
	return s.feedRanked(prequeueRankedCandidate{result: cand})
}

func (s *streamCandidateSource) feedRanked(cand prequeueRankedCandidate) bool {
	it, ok := s.reserve(cand)
	if !ok {
		return false
	}

	select {
	case s.ch <- it:
		return true
	case <-s.done:
		s.rollback(it)
		return false
	}
}

// reserve commits a candidate to the source before it is published. The
// feeder may roll the tail reservation back when a newly completed search
// source interrupts a blocked handoff, allowing that source to be considered
// immediately without polluting migration snapshots.
func (s *streamCandidateSource) reserve(cand prequeueRankedCandidate) (streamedCandidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return streamedCandidate{}, false
	}
	idx := s.total
	// Commit before publishing. Once Next receives the candidate, a cached
	// resolution can complete immediately and take a migration snapshot; the
	// winning candidate must already be present in that snapshot.
	s.total++
	s.acc = append(s.acc, cand.result)
	s.buckets = append(s.buckets, cand.bucket)
	return streamedCandidate{idx: idx, cand: cand.result, bucket: cand.bucket}, true
}

func (s *streamCandidateSource) rollback(it streamedCandidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Feed is single-writer, so an unpublished reservation is the tail entry.
	if s.total == it.idx+1 && len(s.acc) == it.idx+1 {
		s.total = it.idx
		s.acc = s.acc[:it.idx]
		s.buckets = s.buckets[:it.idx]
	}
}

// Stop unblocks a blocked feeder and makes Next report exhaustion, for when the
// race adopts a winner before all sources have finished feeding.
func (s *streamCandidateSource) Stop() {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.done)
	}
	s.mu.Unlock()
}

// Close marks the source exhausted: the race concludes once every handed
// candidate has been processed. The feeder calls it exactly once after every
// enabled source has settled. Like Stop, it is idempotent — the first caller
// wins — so a later Stop (the worker's unconditional winner/exhaustion teardown)
// is a no-op instead of a double close.
func (s *streamCandidateSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.done)
}

func (s *streamCandidateSource) Next(ctx context.Context) (int, models.NZBResult, bool) {
	select {
	case <-ctx.Done():
		return 0, models.NZBResult{}, false
	case <-s.done:
		return 0, models.NZBResult{}, false
	case it, ok := <-s.ch:
		return it.idx, it.cand, ok
	}
}

func (s *streamCandidateSource) bucket(index int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.buckets) {
		return 0
	}
	return s.buckets[index]
}

func (s *streamCandidateSource) Total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func (s *streamCandidateSource) Snapshot() []models.NZBResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]models.NZBResult(nil), s.acc...)
}

// racePrequeueResolutions resolves candidates concurrently (bounded to width in
// flight at once) and returns the best-ranked fully validated candidate — the
// prequeue analog of the debrid multiprovider's checkFastestMode. Candidates are
// pulled from src in feed order, so a streaming source lets the race begin on
// the first-ready search source's candidates while later sources are still
// feeding. The lowest-indexed deprioritized unknown-track candidate is kept as a
// fallback and returned only when nothing validates.
//
// settle > 0 selects a bounded grace window: once a candidate validates while
// a better-ranked candidate is still in flight, the race keeps the losers alive
// for up to settle and prefers the best-ranked candidate that validates within
// it (so a better-ranked release that lost the finish by milliseconds still
// beats a faster but worse-ranked one). settle <= 0 disables that grace: when
// endEarly is enabled, the first valid candidate wins immediately.
//
// endEarly controls whether the race may conclude before every concurrent
// candidate reports. With endEarly=true, a valid candidate wins immediately
// unless a positive settle window gives a better-ranked in-flight candidate a
// brief grace period. With endEarly=false (the default), settle is ignored and
// the race keeps going until the stream drains, then selects the best-ranked
// valid candidate.
//
// On a winner (settled or not), losing workers are cancelled and released; on
// exhaustion every handed candidate has already reported, so the caller may
// safely observe shared candidate state afterwards. report (optional) receives
// the 0-based in-flight candidate window whenever it changes, so callers can
// publish honest "racing candidates X–Y" progress.
func racePrequeueResolutions(ctx context.Context, src prequeueCandidateSource, width int, process prequeueCandidateProcessor, report func(inFlightMin, inFlightMax int), settle time.Duration, endEarly bool) (winner *candidateResolution, usedFallback bool, err error) {
	return racePrequeueResolutionsWithJoinTimeout(ctx, src, width, process, report, settle, endEarly, prequeueResolutionWorkerJoinTimeout)
}

func racePrequeueResolutionsWithJoinTimeout(ctx context.Context, src prequeueCandidateSource, width int, process prequeueCandidateProcessor, report func(inFlightMin, inFlightMax int), settle time.Duration, endEarly bool, joinTimeout time.Duration) (winner *candidateResolution, usedFallback bool, err error) {
	if width <= 0 {
		width = 1
	}

	raceCtx, cancelRace := context.WithCancel(ctx)
	defer cancelRace()

	type raceReport struct {
		idx           int
		bucket        int
		serviceType   models.ContentServiceType
		accepted      *candidateResolution
		deprioritized *candidateResolution
		err           error
	}
	results := make(chan raceReport, 512)

	// Keep consuming the streaming source even when every runnable slot is busy.
	// This lets a later-ready service reach the scheduler instead of sitting
	// behind an already queued candidate from the first service.
	incoming := make(chan streamedCandidate)
	go func() {
		defer close(incoming)
		for {
			idx, cand, ok := src.Next(raceCtx)
			if !ok {
				return
			}
			select {
			case incoming <- streamedCandidate{idx: idx, cand: cand, bucket: prequeueCandidateBucket(src, idx)}:
			case <-raceCtx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	startCandidate := func(it streamedCandidate) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			accepted, deprioritized, perr := process(raceCtx, it.idx, it.cand)
			select {
			case results <- raceReport{idx: it.idx, bucket: it.bucket, serviceType: it.cand.ServiceType, accepted: accepted, deprioritized: deprioritized, err: perr}:
			case <-raceCtx.Done():
			}
		}()
	}

	var fallback *candidateResolution
	var firstErr error
	firstErrIdx := -1
	handed := 0
	reported := 0
	streamExhausted := false
	pending := make([]streamedCandidate, 0, width)
	activeSet := map[int]struct{}{}
	activeBuckets := map[int]int{}
	activeByService := map[models.ContentServiceType]int{}
	cancelAndJoin := func(reason string) {
		cancelRace()
		if waitForPrequeueResolutionWorkers(&wg, joinTimeout) {
			return
		}
		log.Printf("[prequeue] Timed out after %s waiting for cancelled candidate workers (%s); detaching %d worker(s)", joinTimeout, reason, len(activeSet))
	}

	// Settle state: the first validated candidate that cannot immediately win
	// (a positive grace window applies, or endRaceEarly is off)
	// becomes the incumbent (settledBest) and the race keeps racing rather than
	// cancelling the losers. In bounded mode a settleTimer is armed so a
	// better-ranked candidate can still win within the window but cannot stall
	// forever. With endRaceEarly off there is no timer and the race waits for the
	// batch to drain. Either way the window also closes on caller cancellation or
	// when the stream drains.
	var settledBest *candidateResolution
	var settleTimer <-chan time.Time // non-nil only in bounded mode
	publishWindow := func() {
		if report == nil {
			return
		}
		if len(activeSet) == 0 {
			report(-1, -1) // idle: nothing in flight
			return
		}
		min, max := -1, -1
		for idx := range activeSet {
			if min == -1 || idx < min {
				min = idx
			}
			if max == -1 || idx > max {
				max = idx
			}
		}
		report(min, max)
	}
	startReadyCandidates := func() {
		for len(activeSet) < width && len(pending) > 0 {
			bestBucket := -1
			for _, bucket := range activeBuckets {
				if bestBucket < 0 || bucket < bestBucket {
					bestBucket = bucket
				}
			}
			for _, queued := range pending {
				if bestBucket < 0 || queued.bucket < bestBucket {
					bestBucket = queued.bucket
				}
			}
			pick := -1
			for i, it := range pending {
				if it.bucket != bestBucket {
					continue
				}
				serviceType := it.cand.ServiceType
				peerType := models.ServiceTypeUnknown
				switch serviceType {
				case models.ServiceTypeUsenet:
					peerType = models.ServiceTypeDebrid
				case models.ServiceTypeDebrid:
					peerType = models.ServiceTypeUsenet
				}
				peerQueued := false
				if peerType != models.ServiceTypeUnknown {
					for _, queued := range pending {
						if queued.cand.ServiceType == peerType {
							peerQueued = true
							break
						}
					}
				}
				reservePeerSlot := width > 1 && peerType != models.ServiceTypeUnknown &&
					(!streamExhausted || activeByService[peerType] > 0 || peerQueued)
				if reservePeerSlot && activeByService[serviceType] >= width-1 {
					continue
				}
				pick = i
				break
			}
			if pick < 0 {
				return
			}

			it := pending[pick]
			pending = append(pending[:pick], pending[pick+1:]...)
			handed++
			activeSet[it.idx] = struct{}{}
			activeBuckets[it.idx] = it.bucket
			activeByService[it.cand.ServiceType]++
			publishWindow()
			startCandidate(it)
		}
	}

	for {
		startReadyCandidates()
		select {
		case <-ctx.Done():
			cancelAndJoin("caller cancelled")
			return nil, false, ctx.Err()
		case <-settleTimer:
			// Settle window closed: finalize the best-ranked candidate that
			// validated within it, discarding everything else. settledBest is
			// always non-nil here — the timer is only armed when a validation
			// with a better-ranked candidate in flight set it.
			cancelAndJoin("settle window closed")
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			return settledBest, false, nil
		case it, ok := <-incoming:
			if ok {
				pending = append(pending, it)
				continue
			}
			streamExhausted = true
			// A closed channel is permanently ready. Disable this select arm after
			// observing closure so the owner blocks on actual worker reports instead
			// of spinning at 100% CPU while slow candidates finish.
			incoming = nil
		case r := <-results:
			reported++
			delete(activeSet, r.idx)
			delete(activeBuckets, r.idx)
			activeByService[r.serviceType]--
			publishWindow()
			if r.accepted != nil {
				if settledBest != nil {
					// Settling: keep whichever validated candidate ranks best (the
					// first validation is the incumbent that opened the wait).
					if prequeueCandidatePrecedes(r.bucket, r.idx, settledBest.bucket, settledBest.index) {
						log.Printf("[prequeue] settle: better-ranked candidate %d validated while settling; preferring it over %d", r.idx, settledBest.index)
						r.accepted.bucket = r.bucket
						settledBest = r.accepted
					}
				} else if better, betterBucket := lowestInFlightPreferredCandidate(activeSet, activeBuckets, r.bucket, r.idx); better >= 0 && (!endEarly || settle > 0 || betterBucket < r.bucket) {
					// A better-ranked candidate is still mid-download (e.g. it
					// lost the finish by milliseconds). We must not discard it. In
					// bounded mode (settle > 0 and endEarly enabled) we arm a timer
					// so it can still win within the window but cannot stall forever.
					// With endEarly disabled this is an unbounded wait for the batch.
					r.accepted.bucket = r.bucket
					settledBest = r.accepted
					if betterBucket < r.bucket {
						log.Printf("[prequeue] candidate %d from bucket %d validated while preferred bucket %d candidate %d is still in flight; waiting for preferred bucket", r.idx, r.bucket, betterBucket, better)
					} else if settle > 0 && endEarly {
						settleTimer = time.After(settle)
						log.Printf("[prequeue] candidate %d validated while better-ranked candidate %d still in flight; settling up to %s", r.idx, better, settle)
					} else {
						log.Printf("[prequeue] candidate %d validated while better-ranked candidate %d still in flight; endRaceEarly off, waiting for the batch to drain", r.idx, better)
					}
				} else if endEarly {
					// Fast path: the winner is already the best-ranked candidate in
					// flight and we may end early — adopt it immediately with zero
					// added latency. Losing workers abort on the cancelled context
					// (the feeder's Stop cue is publisher-side, in the prequeue
					// worker). Join in-flight workers, as the exhaustion path does,
					// so a loser's late stage update can't overwrite the adopted
					// state after we return.
					cancelAndJoin("winner adopted")
					return r.accepted, false, nil
				} else {
					// endEarly is off: even though this validation is the best-ranked
					// in flight, keep racing until the stream drains — a streaming
					// source may still feed a better-ranked candidate (cross-source
					// strict ordering). Prefer the best-ranked that resolves.
					r.accepted.bucket = r.bucket
					settledBest = r.accepted
					log.Printf("[prequeue] candidate %d validated as best-ranked in flight; endRaceEarly off, waiting for the batch to drain", r.idx)
				}
			} else if r.deprioritized != nil {
				r.deprioritized.bucket = r.bucket
				if fallback == nil || prequeueCandidatePrecedes(r.bucket, r.deprioritized.index, fallback.bucket, fallback.index) {
					fallback = r.deprioritized
				}
			} else if r.err != nil && (firstErrIdx < 0 || r.idx < firstErrIdx) {
				firstErr = r.err
				firstErrIdx = r.idx
			}
			// Fall through to the exhaustion check below even when a
			// settle-accepted result just landed, so a drained stream finalizes
			// the best candidate immediately instead of idling out the window.
		}

		if settledBest != nil && endEarly && settleTimer == nil {
			if better, _ := lowestInFlightPreferredCandidate(activeSet, activeBuckets, settledBest.bucket, settledBest.index); better < 0 {
				cancelAndJoin("preferred bucket exhausted")
				return settledBest, false, nil
			}
		}

		// Every handed candidate has reported and no further candidates are
		// coming: the race is exhausted. "Handed" counts candidates a worker
		// actually received (not just dispatched), so a still-mid-flight worker
		// can never be cut off by the exhaustion check.
		if streamExhausted && len(pending) == 0 && handed == reported {
			break
		}
	}

	// Nothing validated (or the stream drained during a settle window): if a
	// candidate validated during the settle, prefer it; otherwise fall back to
	// the deprioritized unknown-track result, the lowest-indexed failure
	// observed, or a sentinel when the stream carried no candidates at all.
	cancelRace()
	wg.Wait()
	// A cancellation can land exactly as the exhaustion break fires, after the
	// last result was observed but before the select could surface ctx — never
	// misreport the cancellation as no-results or a failure.
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if settledBest != nil {
		return settledBest, false, nil
	}
	if fallback != nil {
		return fallback, true, nil
	}
	if firstErr != nil {
		return nil, false, fmt.Errorf("all %d candidates failed to resolve (top-ranked failure: %w)", reported, firstErr)
	}
	return nil, false, errNoSearchCandidates
}

func waitForPrequeueResolutionWorkers(wg *sync.WaitGroup, timeout time.Duration) bool {
	if timeout <= 0 {
		wg.Wait()
		return true
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func prequeueCandidatePrecedes(bucket, idx, otherBucket, otherIdx int) bool {
	return bucket < otherBucket || (bucket == otherBucket && idx < otherIdx)
}

// lowestInFlightPreferredCandidate returns the best in-flight candidate that
// precedes the supplied Result Order bucket/index pair.
func lowestInFlightPreferredCandidate(activeSet map[int]struct{}, activeBuckets map[int]int, bucket, idx int) (best, bestBucket int) {
	best = -1
	bestBucket = -1
	for i := range activeSet {
		candidateBucket := activeBuckets[i]
		if prequeueCandidatePrecedes(candidateBucket, i, bucket, idx) &&
			(best == -1 || prequeueCandidatePrecedes(candidateBucket, i, bestBucket, best)) {
			best = i
			bestBucket = candidateBucket
		}
	}
	return best, bestBucket
}

// prequeueCandidateAttempt builds a latency-tracker candidate attempt record
// for the resolution race. index is the 0-based feed order; attempts are
// recorded with 1-based indexes so CSV/log ordering matches the UI's "N of Z".
func prequeueCandidateAttempt(index int, result models.NZBResult, outcome string, start time.Time) PlaybackCandidateAttempt {
	return PlaybackCandidateAttempt{
		Index:       index + 1,
		ReleaseName: result.Title,
		ServiceType: string(result.ServiceType),
		Outcome:     outcome,
		DurationMs:  time.Since(start).Milliseconds(),
	}
}

// prequeueCandidateAbsoluteEpisodeMismatch reports whether a single-episode
// candidate's label carries an episode number that contradicts the target's
// SxxExx and absolute numbering (anime absolute releases such as
// "One Piece - 1153"). It returns the parsed label number for logging.
func prequeueCandidateAbsoluteEpisodeMismatch(result models.NZBResult, target *models.EpisodeReference) (int, bool) {
	if target == nil || target.AbsoluteEpisodeNumber <= 0 || result.EpisodeCount > 1 {
		return 0, false
	}
	parsedEp, hasEpisode := mediaresolve.ParseAbsoluteEpisodeNumber(result.Title)
	if !hasEpisode || parsedEp == target.AbsoluteEpisodeNumber {
		return parsedEp, false
	}
	episodeCode := mediaresolve.EpisodeCode{Season: target.SeasonNumber, Episode: target.EpisodeNumber}
	if result.Attributes["mappedCatalogEpisode"] == fmt.Sprintf("S%02dE%02d", episodeCode.Season, episodeCode.Episode) && result.Attributes["mappedCatalogNumbering"] == models.EpisodeNumberingKey(target.Numbering) {
		if season, err := strconv.Atoi(result.Attributes["targetSeason"]); err == nil && season > 0 {
			episodeCode.Season = season
		}
		if episode, err := strconv.Atoi(result.Attributes["targetEpisode"]); err == nil && episode > 0 {
			episodeCode.Episode = episode
		}
	}
	return parsedEp, !mediaresolve.CandidateMatchesEpisode(result.Title, episodeCode)
}

// resolveCandidates resolves the candidate source and returns the winning
// resolution together with the candidate and probe data the rest of the worker
// needs. In the default mode candidates are attempted sequentially in ranked
// order. With opts.concurrent enabled, the bounded source-aware scheduler races
// candidates and honors the configured settle policy. Deprioritized
// unknown-track results are a fallback only when nothing validates, and
// IsArticleUnavailable failures still mark bad streams. A streaming source is
// only paired with concurrent mode, allowing the first-ready search source to
// begin resolving while later sources are still feeding.
func (h *PrequeueHandler) resolveCandidates(ctx context.Context, prequeueID string, src prequeueCandidateSource, opts prequeueResolutionOptions) (prequeueResolutionChoice, error) {
	resolveStart := time.Now()

	process := func(raceCtx context.Context, i int, result models.NZBResult) (accepted *candidateResolution, deprioritized *candidateResolution, err error) {
		// Record the per-candidate attempt outcome + duration for the latency
		// tracker on every terminal path: probe
		// rejections and article-unavailable failures are the measurable signal.
		attemptStart := time.Now()
		defer func() {
			if h.latencyTracker == nil {
				return
			}
			switch {
			case accepted != nil:
				h.latencyTracker.NotePrequeueCandidate(prequeueID, prequeueCandidateAttempt(i, result, PrequeueCandidateAdopted, attemptStart))
			case deprioritized != nil:
				h.latencyTracker.NotePrequeueCandidate(prequeueID, prequeueCandidateAttempt(i, result, PrequeueCandidateDeprioritized, attemptStart))
			case err != nil && errors.Is(err, playback.ErrUsenetProbeRejected):
				h.latencyTracker.NotePrequeueCandidate(prequeueID, prequeueCandidateAttempt(i, result, PrequeueCandidateProbeRejected, attemptStart))
			case err != nil && importer.IsArticleUnavailable(err):
				h.latencyTracker.NotePrequeueCandidate(prequeueID, prequeueCandidateAttempt(i, result, PrequeueCandidateArticlesUnavailable, attemptStart))
			case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
				h.latencyTracker.NotePrequeueCandidate(prequeueID, prequeueCandidateAttempt(i, result, PrequeueCandidateSuperseded, attemptStart))
			case err != nil:
				h.latencyTracker.NotePrequeueCandidate(prequeueID, prequeueCandidateAttempt(i, result, PrequeueCandidateFailed, attemptStart))
			}
		}()

		if parsedEp, mismatch := prequeueCandidateAbsoluteEpisodeMismatch(result, opts.targetEpisode); mismatch {
			log.Printf("[prequeue] Skipping result [%d] - episode %d doesn't match target (S%02dE%02d/abs:%d): %s",
				i, parsedEp, opts.targetEpisode.SeasonNumber, opts.targetEpisode.EpisodeNumber, opts.targetEpisode.AbsoluteEpisodeNumber, result.Title)
			return nil, nil, nil
		}

		h.updatePrequeueStageDetail(prequeueID, "resolving_candidate", result.Title)
		// Annotate a private clone of the attribute map: racing workers may outlive
		// the winner (aborting on the cancelled context), and the shared map must
		// never be written while other goroutines serialize the entry.
		annotated := result
		if annotated.Attributes != nil {
			cloned := make(map[string]string, len(annotated.Attributes)+1)
			for key, value := range annotated.Attributes {
				cloned[key] = value
			}
			annotated.Attributes = cloned
		}
		annotateResultProfile(&annotated, opts.userID)
		result = annotated

		resolution, resolveErr := h.playbackSvc.Resolve(raceCtx, result)
		if resolveErr == nil && resolution != nil && resolution.QueueID > 0 && strings.TrimSpace(resolution.WebDAVPath) == "" {
			h.updatePrequeueStageDetail(prequeueID, "waiting_provider", result.Title)
			resolution, resolveErr = h.waitForPlaybackQueue(raceCtx, prequeueID, resolution.QueueID, result.Title)
		}
		if err := raceCtx.Err(); err != nil {
			return nil, nil, err
		}
		if resolveErr != nil || resolution == nil || resolution.WebDAVPath == "" {
			// A loser that was still mid-flight when a winner was adopted sees the
			// race context cancelled; label that as superseded rather than a
			// failed release so logs don't read like valid releases were skipped.
			if raceCtx.Err() != nil && (errors.Is(resolveErr, context.Canceled) || errors.Is(resolveErr, context.DeadlineExceeded)) {
				log.Printf("[prequeue] Result [%d] (%s) %s superseded by winner, aborting resolve: %v", i, result.ServiceType, result.Title, resolveErr)
				return nil, nil, resolveErr
			}
			if importer.IsArticleUnavailable(resolveErr) && h.badStreamsSvc != nil {
				provider := result.Attributes["provider"]
				if provider == "" {
					provider = result.Attributes["debridProvider"]
				}
				if _, markErr := h.badStreamsSvc.Mark(badstreams.MarkRequest{
					ReleaseName: result.Title,
					ServiceType: string(result.ServiceType),
					Provider:    provider,
					Reason:      "prequeue:usenet-articles-unavailable",
				}); markErr != nil {
					log.Printf("[prequeue] Failed to mark unavailable Usenet result bad for %s: %v", result.Title, markErr)
				} else {
					log.Printf("[prequeue] Marked unavailable Usenet result bad: %s", result.Title)
				}
			}
			if debrid.IsBlockedContentError(resolveErr) {
				log.Printf("[prequeue] Provider blocked selected file for result [%d] (%s) %s; trying next result: %v", i, result.ServiceType, result.Title, resolveErr)
			} else {
				log.Printf("[prequeue] Failed to resolve result [%d] (%s) %s: %v", i, result.ServiceType, result.Title, resolveErr)
			}
			return nil, nil, resolveErr
		}

		log.Printf("[prequeue] Resolved result [%d] (%s): %s -> %s", i, result.ServiceType, result.Title, requestsecurity.URLForLog(resolution.WebDAVPath))
		rememberStreamRelease(resolution.WebDAVPath, result)
		if isM2TSStreamPath(resolution.WebDAVPath) {
			m2tsErr := fmt.Errorf("prequeue excludes .m2ts streams: %s", resolution.WebDAVPath)
			log.Printf("[prequeue] Skipping result [%d] (%s) %s: %v", i, result.ServiceType, result.Title, m2tsErr)
			return nil, nil, m2tsErr
		}

		var probeResult *VideoFullResult
		var metadataResult *VideoMetadataResult
		h.updatePrequeueStageDetail(prequeueID, "validating_candidate", result.Title)

		// Every resolved prequeue candidate must expose a playable video track.
		// This probe is also reused below for HDR and track selection, so moving it
		// into candidate selection does not add work for the winning candidate.
		// Playback resolution may already carry a probe (pre-resolved scraper
		// results); reuse it instead of probing the same remote URL again.
		probeResult = resolution.Probe
		if probeResult != nil {
			log.Printf("[prequeue] Reusing probe returned by playback resolution for %s", result.Title)
		}

		if probeResult != nil || h.fullProber != nil {
			var probeErr error
			if probeResult == nil {
				probeResult, probeErr = probeResolvedCandidate(raceCtx, h.fullProber, resolution)
				if err := raceCtx.Err(); err != nil {
					return nil, nil, err
				}
				if probeErr != nil {
					if raceCtx.Err() != nil && (errors.Is(probeErr, context.Canceled) || errors.Is(probeErr, context.DeadlineExceeded)) {
						log.Printf("[prequeue] Result [%d] (%s) %s superseded by winner, aborting probe: %v", i, result.ServiceType, result.Title, probeErr)
						return nil, nil, probeErr
					}
					log.Printf("[prequeue] Probe check failed for %s: %v, trying next result", result.Title, probeErr)
					return nil, nil, probeErr
				}
			}
			if probeErr = validatePrequeueVideoProbe(probeResult); probeErr != nil {
				log.Printf("[prequeue] Unplayable probe result for %s: %v, trying next result", result.Title, probeErr)
				h.markBadPrequeueResult(result, resolution, "prequeue:metadata-probe-unplayable")
				return nil, nil, probeErr
			}
			if probeErr = validatePrequeueEpisodeDuration(opts.mediaType, opts.targetEpisode, probeResult.Duration); probeErr != nil {
				log.Printf("[prequeue] Episode duration mismatch for %s: %v, trying next result", result.Title, probeErr)
				h.markBadPrequeueResult(result, resolution, "prequeue:episode-duration-mismatch")
				return nil, nil, probeErr
			}
			if opts.needsDVCheck && probeResult != nil {
				if err := ValidateDVProfile(probeResult.DolbyVisionProfile, "hdr", probeResult.HasDolbyVision); err != nil {
					log.Printf("[prequeue] DV profile incompatible for %s: %v, trying next result", result.Title, err)
					return nil, nil, err
				}
				if probeResult.HasDolbyVision {
					log.Printf("[prequeue] DV profile %s compatible with 'hdr' policy", probeResult.DolbyVisionProfile)
				}
			}
		}

		if opts.needsUnknownTrackCheck && probeResult == nil && h.metadataProber != nil {
			metadata, probeErr := h.metadataProber.ProbeVideoMetadata(raceCtx, resolution.WebDAVPath)
			if err := raceCtx.Err(); err != nil {
				return nil, nil, err
			}
			if probeErr != nil {
				if raceCtx.Err() != nil && (errors.Is(probeErr, context.Canceled) || errors.Is(probeErr, context.DeadlineExceeded)) {
					log.Printf("[prequeue] Result [%d] (%s) %s superseded by winner, aborting metadata probe: %v", i, result.ServiceType, result.Title, probeErr)
					return nil, nil, probeErr
				}
				log.Printf("[prequeue] Track metadata probe failed for %s: %v, trying next result", result.Title, probeErr)
				return nil, nil, probeErr
			}
			metadataResult = metadata
		}
		if err := raceCtx.Err(); err != nil {
			return nil, nil, err
		}

		if opts.needsUnknownTrackCheck {
			var audioStreams []AudioStreamInfo
			var subtitleStreams []SubtitleStreamInfo
			if probeResult != nil {
				audioStreams = probeResult.AudioStreams
				subtitleStreams = probeResult.SubtitleStreams
			} else if metadataResult != nil {
				audioStreams = metadataResult.AudioStreams
				subtitleStreams = metadataResult.SubtitleStreams
			} else {
				log.Printf("[prequeue] Unknown track policy %q enabled but no track prober is available; keeping result %q", opts.unknownTrackPolicy, result.Title)
			}

			if probeResult != nil || metadataResult != nil {
				if rejected, reason := unknownTrackPolicyRejects(opts.unknownTrackPolicy, audioStreams, subtitleStreams); rejected {
					log.Printf("[prequeue] Result [%d] deprioritized by unknown track policy %q: %s; trying next result: %s", i, opts.unknownTrackPolicy, reason, result.Title)
					return nil, &candidateResolution{result: result, index: i, resolution: resolution, probeResult: probeResult, metadataResult: metadataResult, migrationSnapshot: src.Snapshot()}, nil
				}
			}
		}

		return &candidateResolution{result: result, index: i, resolution: resolution, probeResult: probeResult, metadataResult: metadataResult, migrationSnapshot: src.Snapshot()}, nil, nil
	}

	resolutionWidth := prequeueResolutionWidth(opts.concurrent)
	settleWindow := time.Duration(0)
	endEarly := true
	resolutionMode := "sequential"
	if opts.concurrent {
		settleWindow, endEarly = h.resolutionRacePolicy()
		resolutionMode = "concurrent"
	}
	log.Printf("[prequeue] TIMING: starting %s candidate resolution (width=%d, elapsed: %v)", resolutionMode, resolutionWidth, time.Since(resolveStart))
	// Publish the in-flight window as 1-based candidate numbers. During the race
	// only this publisher owns numeric progress; worker updates carry stage and
	// release detail only, so the UI can render "trying streams X–Y of Z". The
	// total is the source's fed count, which grows while a feeder is still
	// publishing debrid results behind an in-flight usenet race.
	reportProgress := func(inFlightMin, inFlightMax int) {
		total := src.Total()
		if inFlightMin < 0 || total == 0 {
			h.updatePrequeueRaceProgress(prequeueID, 0, 0, total)
			return
		}
		h.updatePrequeueRaceProgress(prequeueID, inFlightMin+1, inFlightMax+1, total)
	}
	winner, usedFallback, raceErr := racePrequeueResolutions(ctx, src, resolutionWidth, process, reportProgress, settleWindow, endEarly)
	if winner == nil {
		return prequeueResolutionChoice{}, raceErr
	}
	if usedFallback {
		// The fallback candidate was recorded as deprioritized when it completed;
		// it is now the winner — flip the attempt outcome so samples read truthfully.
		if h.latencyTracker != nil {
			h.latencyTracker.MarkPrequeueCandidateAdopted(prequeueID, winner.index+1)
		}
		log.Printf("[prequeue] All fully known candidates failed or were unavailable; using first deprioritized unknown-track result: %s", winner.result.Title)
	} else {
		log.Printf("[prequeue] TIMING: resolved (took: %v, total elapsed: %v)", time.Since(resolveStart), time.Since(opts.workerStart))
	}
	return prequeueResolutionChoice{
		resolution:          winner.resolution,
		selectedResult:      &winner.result,
		selectedResultIndex: winner.index,
		probeResult:         winner.probeResult,
		metadataResult:      winner.metadataResult,
		migrationCandidates: winner.migrationSnapshot,
	}, nil
}

// resolutionRacePolicy returns the configured grace window and end-early flag.
// Zero means no grace; a positive value bounds how long a better-ranked
// candidate may keep racing after the first validation. These defaults only
// affect callers that explicitly select concurrent resolution.
func (h *PrequeueHandler) resolutionRacePolicy() (settle time.Duration, endEarly bool) {
	if h == nil || h.configManager == nil {
		return time.Duration(prequeueResolutionSettleWindowDefault) * time.Millisecond, prequeueResolutionEndRaceEarlyDefault
	}
	s, err := h.configManager.Load()
	if err != nil {
		log.Printf("[prequeue] failed to load settings for resolution race policy: %v", err)
		return time.Duration(prequeueResolutionSettleWindowDefault) * time.Millisecond, prequeueResolutionEndRaceEarlyDefault
	}
	return time.Duration(s.Streaming.ResolutionSettleWindowMs) * time.Millisecond, s.Streaming.ResolutionEndRaceEarly
}

// markBadPrequeueResult records a bad-stream entry for a candidate that passed
// resolution but failed probe validation.
func (h *PrequeueHandler) markBadPrequeueResult(result models.NZBResult, resolution *models.PlaybackResolution, reason string) {
	if h.badStreamsSvc == nil {
		return
	}
	provider := result.Attributes["provider"]
	if provider == "" {
		provider = result.Attributes["debridProvider"]
	}
	sourcePath := ""
	if resolution != nil {
		sourcePath = resolution.WebDAVPath
	}
	if _, markErr := h.badStreamsSvc.Mark(badstreams.MarkRequest{
		ReleaseName: result.Title,
		ServiceType: string(result.ServiceType),
		Provider:    provider,
		SourcePath:  sourcePath,
		Reason:      reason,
	}); markErr != nil {
		log.Printf("[prequeue] Failed to mark probe result bad for %s: %v", result.Title, markErr)
	}
}

func (h *PrequeueHandler) updatePrequeueProgress(prequeueID, stage, detail string, current, total int) {
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.ProgressStage = stage
		e.ProgressDetail = detail
		e.ProgressCurrent = current
		e.ProgressTotal = total
	})
}

// updatePrequeueStageDetail publishes only the per-candidate stage and detail
// (the release being worked on) without numeric progress, which the racing
// resolution phase owns.
func (h *PrequeueHandler) updatePrequeueStageDetail(prequeueID, stage, detail string) {
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.ProgressStage = stage
		e.ProgressDetail = detail
	})
}

// updatePrequeueRaceProgress publishes the in-flight candidate window (1-based
// min/max) during the bounded-resolution race. Current/Max stay monotonically
// truthful: with 4 workers this reads e.g. min=1 max=4 while candidates 1–4 are
// being tried concurrently.
func (h *PrequeueHandler) updatePrequeueRaceProgress(prequeueID string, minCurrent, maxCurrent, total int) {
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.ProgressCurrentMin = minCurrent
		e.ProgressCurrentMax = maxCurrent
		e.ProgressCurrent = maxCurrent
		e.ProgressTotal = total
	})
}

func logPrequeueCandidateList(scoredResults []models.ScoredNZBResult, source string) {
	limit := len(scoredResults)
	log.Printf("[prequeue] candidate decision list (%s): showing all %d result(s); score is explanatory, order follows ranking priorities", source, limit)
	for i := 0; i < limit; i++ {
		result := scoredResults[i]
		badStream := strings.Contains(strings.ToLower(result.FilterReason), "marked bad stream")
		log.Printf("[prequeue] candidate #%d title=%q provider=%q service=%q status=%q sourceCacheStatus=%q badStream=%v score=%d languages=%q ranking=%+v reason=%q",
			i+1,
			result.Title,
			result.Indexer,
			result.ServiceType,
			result.FilterStatus,
			result.Attributes["sourceCacheStatus"],
			badStream,
			result.TotalScore,
			result.Attributes["languages"],
			result.ScoreBreakdown,
			result.FilterReason,
		)
	}
}

// searchCombinedPrequeueCandidates preserves the original prequeue search
// semantics: all enabled sources complete, their results are ranked together,
// and the final cap is applied only after filtering and global ranking. The
// returned slice is also the complete ordered migration candidate list.
func combinedPrequeueSearchOptions(opts indexer.SearchOptions) indexer.SearchOptions {
	return indexer.SearchOptions{
		TitleID:               opts.TitleID,
		Query:                 opts.Query,
		Categories:            opts.Categories,
		IMDBID:                opts.IMDBID,
		TVDBID:                opts.TVDBID,
		AlternateTitles:       append([]string(nil), opts.AlternateTitles...),
		MediaType:             opts.MediaType,
		Year:                  opts.Year,
		CountryCode:           opts.CountryCode,
		OriginalLanguage:      opts.OriginalLanguage,
		UserID:                opts.UserID,
		ClientID:              opts.ClientID,
		AdaptiveThroughput:    opts.AdaptiveThroughput,
		EpisodeResolver:       opts.EpisodeResolver,
		TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
		AbsoluteEpisodeNumber: opts.AbsoluteEpisodeNumber,
		IsAnime:               opts.IsAnime,
		IsDaily:               opts.IsDaily,
		TargetAirDate:         opts.TargetAirDate,
		EpisodeAirYear:        opts.EpisodeAirYear,
		SeasonPremiereYear:    opts.SeasonPremiereYear,
		EpisodeReleased:       opts.EpisodeReleased,
		IncludeFiltered:       true,
		IncludeScoreBreakdown: true,
	}
}

func (h *PrequeueHandler) searchCombinedPrequeueCandidates(ctx context.Context, opts indexer.SearchOptions, targetEpisode *models.EpisodeReference) ([]models.NZBResult, int, int, error) {
	scoredResults, err := h.indexerSvc.SearchWithScoring(ctx, combinedPrequeueSearchOptions(opts))
	if err != nil {
		return nil, 0, 0, err
	}

	badStreamCount := 0
	if h.badStreamsSvc != nil {
		for i := range scoredResults {
			if !h.badStreamsSvc.IsBad(scoredResults[i].NZBResult) {
				continue
			}
			badStreamCount++
			scoredResults[i].FilterStatus = "filtered"
			if scoredResults[i].FilterReason == "" {
				scoredResults[i].FilterReason = "marked bad stream"
			} else if !strings.Contains(strings.ToLower(scoredResults[i].FilterReason), "marked bad stream") {
				scoredResults[i].FilterReason += "; marked bad stream"
			}
		}
	}
	logPrequeueCandidateList(scoredResults, "combined")

	results := make([]models.NZBResult, 0, len(scoredResults))
	for _, scored := range scoredResults {
		if scored.FilterStatus == "filtered" {
			continue
		}
		candidate := scored.NZBResult
		annotateResultEpisode(&candidate, targetEpisode)
		results = append(results, candidate)
	}
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}
	return results, badStreamCount, len(scoredResults), nil
}

// errNoSearchCandidates signals that the resolution phase received no
// candidates at all (the stream exhausted empty). The worker maps it to the
// user-facing "no results found" / "all search sources failed" messages.
var errNoSearchCandidates = errors.New("no candidates to resolve")

// prequeueFeedState carries the aggregate outcome of the streaming search feeder
// to the worker: how many candidates made it past bad-stream marking, which
// enabled sources failed outright, and a done channel the worker joins after the
// resolution phase so it can read the settled values.
type prequeueFeedState struct {
	done           chan struct{}
	mu             sync.Mutex
	badStreamCount int
	sourceFailures int
	enabledSources int
	fedCount       int
}

func newPrequeueFeedState() *prequeueFeedState {
	return &prequeueFeedState{done: make(chan struct{})}
}

func (s *prequeueFeedState) allSourcesFailed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabledSources > 0 && s.sourceFailures == s.enabledSources
}

func (s *prequeueFeedState) snapshot() (badStreams, fed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.badStreamCount, s.fedCount
}

// prequeueFeederConfig is the stream-feeder's per-prequeue configuration.
type prequeueFeederConfig struct {
	prequeueID    string
	maxCandidates int // cap on total candidates fed (searchOpts.MaxResults)
	targetEpisode *models.EpisodeReference
	workerStart   time.Time
}

type prequeuePreparedSource struct {
	source     string
	candidates []prequeueRankedCandidate
}

// prequeueSearchFeeder consumes and prepares the split sources concurrently,
// then publishes their candidates through one event loop. A blocked candidate
// handoff remains interruptible by a newly prepared source, so the first batch
// cannot delay a later source behind slow resolution attempts. Once both are
// ready, candidates are interleaved while retaining each source's local rank.
func (h *PrequeueHandler) prequeueSearchFeeder(ctx context.Context, src *streamCandidateSource, usenetCh, debridCh <-chan indexer.ScoredSplitSearchResult, state *prequeueFeedState, cfg prequeueFeederConfig) {
	var prepareWG sync.WaitGroup
	defer func() {
		prepareWG.Wait()
		close(state.done)
	}()

	preparedCh := make(chan prequeuePreparedSource, 2)
	prepare := func(ch <-chan indexer.ScoredSplitSearchResult) {
		select {
		case <-ctx.Done():
			return
		case res, ok := <-ch:
			if !ok {
				select {
				case preparedCh <- prequeuePreparedSource{}:
				case <-ctx.Done():
				}
				return
			}
			batch := h.prepareSourceResults(ctx, state, cfg, res)
			select {
			case preparedCh <- prequeuePreparedSource{source: res.Source, candidates: batch}:
			case <-ctx.Done():
			}
		}
	}
	prepareWG.Add(2)
	go func() {
		defer prepareWG.Done()
		prepare(usenetCh)
	}()
	go func() {
		defer prepareWG.Done()
		prepare(debridCh)
	}()

	queues := make(map[string][]prequeueRankedCandidate, 2)
	fedBySource := make(map[string]int, 2)
	order := make([]string, 0, 2)
	nextSource := 0
	settled := 0
	fed := 0
	sourceLimit := cfg.maxCandidates
	if cfg.maxCandidates > 1 {
		sourceLimit = (cfg.maxCandidates + 1) / 2
	}

	addPrepared := func(prepared prequeuePreparedSource) {
		settled++
		if prepared.source == "" || len(prepared.candidates) == 0 {
			return
		}
		if _, exists := queues[prepared.source]; !exists {
			order = append(order, prepared.source)
		}
		queues[prepared.source] = append(queues[prepared.source], prepared.candidates...)
		// A source that completed while another candidate was blocked gets the
		// next handoff. Subsequent picks rotate across both ready sources.
		for i, source := range order {
			if source == prepared.source {
				nextSource = i
				break
			}
		}
	}

	pickCandidate := func() (string, prequeueRankedCandidate, bool) {
		if cfg.maxCandidates > 0 && fed >= cfg.maxCandidates {
			return "", prequeueRankedCandidate{}, false
		}
		scan := func(enforceReservation bool) (string, prequeueRankedCandidate, bool) {
			for offset := 0; offset < len(order); offset++ {
				i := (nextSource + offset) % len(order)
				source := order[i]
				if len(queues[source]) == 0 {
					continue
				}
				if enforceReservation && sourceLimit > 0 && fedBySource[source] >= sourceLimit {
					continue
				}
				nextSource = (i + 1) % len(order)
				return source, queues[source][0], true
			}
			return "", prequeueRankedCandidate{}, false
		}

		// Until both sources settle, retain half the budget for the source still
		// in flight. Once settled, consume both reserved shares before releasing
		// genuinely unused capacity to a source with remaining candidates.
		if cfg.maxCandidates > 1 {
			if source, candidate, ok := scan(true); ok {
				return source, candidate, true
			}
			if settled < 2 {
				return "", prequeueRankedCandidate{}, false
			}
		}
		return scan(false)
	}

	for {
		source, candidate, hasCandidate := pickCandidate()
		if !hasCandidate {
			if settled == 2 || (cfg.maxCandidates > 0 && fed >= cfg.maxCandidates) {
				src.Close()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-src.done:
				return
			case prepared := <-preparedCh:
				addPrepared(prepared)
			}
			continue
		}

		it, ok := src.reserve(candidate)
		if !ok {
			return
		}
		select {
		case <-ctx.Done():
			src.rollback(it)
			return
		case <-src.done:
			src.rollback(it)
			return
		case prepared := <-preparedCh:
			// Do not make a completed source wait for the current source's next
			// worker slot. Roll back this unpublished tail and reprioritize.
			src.rollback(it)
			if prepared.source != "" && len(prepared.candidates) > 0 {
				log.Printf("[prequeue] %s source ready with %d candidate(s); interrupting blocked %s handoff", prepared.source, len(prepared.candidates), source)
			}
			addPrepared(prepared)
		case src.ch <- it:
			queues[source] = queues[source][1:]
			fed++
			fedBySource[source]++
			state.mu.Lock()
			state.fedCount = fed
			state.mu.Unlock()
		}
	}
}

// prepareSourceResults processes one completed search source batch: bad-stream
// marking, candidate decision logging, the deferred debrid preflight, then
// returning the locally ranked surviving candidates. Source preparation occurs
// concurrently, so a slow debrid torrent preflight cannot delay ready Usenet
// candidates.
func (h *PrequeueHandler) prepareSourceResults(ctx context.Context, state *prequeueFeedState, cfg prequeueFeederConfig, res indexer.ScoredSplitSearchResult) []prequeueRankedCandidate {
	if res.Disabled {
		return nil // source not in the active service mode; nothing to count or feed
	}
	state.mu.Lock()
	state.enabledSources++
	if res.Err != nil {
		state.sourceFailures++
	}
	state.mu.Unlock()
	if res.Err != nil {
		log.Printf("[prequeue] %s search failed (streaming): %v", res.Source, res.Err)
		return nil
	}
	h.updatePrequeueStageDetail(cfg.prequeueID, "ranking", "")

	// Bad-stream marking + decision log (mirrors the non-streaming path).
	if h.badStreamsSvc != nil {
		for i := range res.Scored {
			scored := &res.Scored[i]
			if !h.badStreamsSvc.IsBad(scored.NZBResult) {
				continue
			}
			state.mu.Lock()
			state.badStreamCount++
			state.mu.Unlock()
			scored.FilterStatus = "filtered"
			if scored.FilterReason == "" {
				scored.FilterReason = "marked bad stream"
			} else if !strings.Contains(strings.ToLower(scored.FilterReason), "marked bad stream") {
				scored.FilterReason += "; marked bad stream"
			}
		}
	}
	logPrequeueCandidateList(res.Scored, res.Source)

	passed := make([]models.ScoredNZBResult, 0, len(res.Scored))
	for _, scored := range res.Scored {
		if scored.FilterStatus == "filtered" {
			continue
		}
		passed = append(passed, scored)
	}
	buckets, bucketCriterion := prequeuePreferenceBuckets(passed)
	batch := make([]models.NZBResult, 0, len(passed))
	for _, scored := range passed {
		batch = append(batch, scored.NZBResult)
	}
	if len(batch) > 0 {
		log.Printf("[prequeue] %s bulk resolution buckets criterion=%q buckets=%d candidates=%d", res.Source, bucketCriterion, prequeueBucketCount(buckets), len(batch))
	}

	// Deferred debrid torrent preflight (metainfo download for TorBox hash
	// discovery) before this batch can feed. Usenet resolution is already racing
	// in the worker pool, so this never blocks it; on a usenet win the feed
	// context is cancelled and the preflight aborts.
	if res.Source == "debrid" && len(batch) > 0 && h.playbackSvc != nil {
		preflightStart := time.Now()
		batch = h.playbackSvc.PrepareTorrentCandidates(ctx, batch)
		log.Printf("[prequeue] TIMING: deferred debrid candidate preparation complete (%d prepared, elapsed: %v)",
			len(batch), time.Since(preflightStart))
	}
	ranked := make([]prequeueRankedCandidate, len(batch))
	for i := range batch {
		annotateResultEpisode(&batch[i], cfg.targetEpisode)
		bucket := 0
		if i < len(buckets) {
			bucket = buckets[i]
		}
		ranked[i] = prequeueRankedCandidate{result: batch[i], bucket: bucket}
	}
	return ranked
}

// prequeuePreferenceBuckets partitions a locally sorted source batch using the
// first enabled Result Order criterion that actually differentiates candidates.
// Service Priority is deliberately ignored: it still affects normal ordering,
// but must not split otherwise equivalent candidates into separate bulk tries.
func prequeuePreferenceBuckets(scored []models.ScoredNZBResult) ([]int, string) {
	buckets := make([]int, len(scored))
	if len(scored) < 2 {
		return buckets, ""
	}

	criterion := -1
	criterionName := ""
	maxCriteria := 0
	for _, result := range scored {
		if len(result.ScoreBreakdown) > maxCriteria {
			maxCriteria = len(result.ScoreBreakdown)
		}
	}
	for position := 0; position < maxCriteria; position++ {
		name := ""
		firstPoints := 0
		haveFirst := false
		varies := false
		for _, result := range scored {
			if position >= len(result.ScoreBreakdown) {
				continue
			}
			item := result.ScoreBreakdown[position]
			if name == "" {
				name = item.Criterion
			}
			if !haveFirst {
				firstPoints = item.Points
				haveFirst = true
			} else if item.Points != firstPoints {
				varies = true
			}
		}
		if strings.EqualFold(strings.TrimSpace(name), "Service Priority") || !varies {
			continue
		}
		criterion = position
		criterionName = name
		break
	}
	if criterion < 0 {
		return buckets, ""
	}

	// Candidate order can include promotions unrelated to this criterion.
	// Rank distinct scores explicitly so a promoted lower-quality release
	// cannot make its entire preference bucket the first one attempted.
	bucketByPoints := make(map[int]int)
	var scores []int
	for _, result := range scored {
		if criterion >= len(result.ScoreBreakdown) {
			continue
		}
		points := result.ScoreBreakdown[criterion].Points
		if _, ok := bucketByPoints[points]; !ok {
			bucketByPoints[points] = 0
			scores = append(scores, points)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(scores)))
	for bucket, points := range scores {
		bucketByPoints[points] = bucket
	}
	for i, result := range scored {
		if criterion >= len(result.ScoreBreakdown) {
			buckets[i] = len(scores)
			continue
		}
		points := result.ScoreBreakdown[criterion].Points
		buckets[i] = bucketByPoints[points]
	}
	return buckets, criterionName
}

func prequeueBucketCount(buckets []int) int {
	maxBucket := -1
	for _, bucket := range buckets {
		if bucket > maxBucket {
			maxBucket = bucket
		}
	}
	return maxBucket + 1
}

func (h *PrequeueHandler) waitForPlaybackQueue(ctx context.Context, prequeueID string, queueID int64, title string) (*models.PlaybackResolution, error) {
	if h == nil || h.playbackSvc == nil {
		return nil, fmt.Errorf("playback service not configured")
	}
	log.Printf("[prequeue] Waiting for queued playback result queueID=%d title=%q", queueID, title)

	timeout := time.NewTimer(15 * time.Minute)
	defer timeout.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		status, err := h.playbackSvc.QueueStatus(ctx, queueID)
		if err != nil {
			return nil, err
		}
		if status != nil {
			h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
				e.HealthStatus = status.HealthStatus
				if status.FileSize > 0 {
					e.FileSize = status.FileSize
				}
			})
			if strings.TrimSpace(status.WebDAVPath) != "" {
				log.Printf("[prequeue] Queued playback result ready queueID=%d title=%q path=%q", queueID, title, requestsecurity.URLForLog(status.WebDAVPath))
				return status, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, fmt.Errorf("playback queue %d timed out waiting for external usenet engine", queueID)
		case <-ticker.C:
		}
	}
}

// failPrequeue marks a prequeue as failed
func (h *PrequeueHandler) failPrequeue(prequeueID, errMsg string) {
	log.Printf("[prequeue] Prequeue %s failed: %s", prequeueID, errMsg)
	// Emit a failure sample (complete=false, t0→failure, candidates attached) so
	// the all-candidates-dead path is measurable instead
	// of silently absent from the latency window.
	if h.latencyTracker != nil {
		h.latencyTracker.NotePrequeueFailedSample(prequeueID, errMsg)
	}
	h.store.UpdateWorker(prequeueID, func(e *playback.PrequeueEntry) {
		e.Status = playback.PrequeueStatusFailed
		e.ProgressStage = "failed"
		e.ProgressDetail = ""
		e.ProgressCurrent = 0
		e.ProgressTotal = 0
		e.Error = errMsg
	})
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

func annotateResultEpisode(result *models.NZBResult, episode *models.EpisodeReference) {
	if result == nil || episode == nil {
		return
	}

	// Search results may come from the raw-result cache. Clone the attributes so
	// request-specific episode hints do not mutate the cached result shared by
	// other searches.
	attributes := make(map[string]string, len(result.Attributes)+4)
	for key, value := range result.Attributes {
		attributes[key] = value
	}
	result.Attributes = attributes
	// Filtering has already bound a verified source numbering system to this
	// catalog episode. Preserve it through both normal and deferred batches.
	catalogCode := fmt.Sprintf("S%02dE%02d", episode.SeasonNumber, episode.EpisodeNumber)
	if attributes["mappedCatalogEpisode"] == catalogCode && attributes["mappedCatalogNumbering"] == models.EpisodeNumberingKey(episode.Numbering) {
		return
	}
	delete(attributes, "mappedCatalogEpisode")
	delete(attributes, "mappedCatalogNumbering")
	delete(attributes, "episodeMappingSource")
	delete(attributes, "episodeSelectionAliases")

	if episode.SeasonNumber > 0 {
		attributes["targetSeason"] = strconv.Itoa(episode.SeasonNumber)
	}
	if episode.EpisodeNumber > 0 {
		attributes["targetEpisode"] = strconv.Itoa(episode.EpisodeNumber)
	}
	if episode.SeasonNumber > 0 && episode.EpisodeNumber > 0 {
		attributes["targetEpisodeCode"] = fmt.Sprintf("S%02dE%02d", episode.SeasonNumber, episode.EpisodeNumber)
	}
	if episode.AbsoluteEpisodeNumber > 0 {
		attributes["absoluteEpisodeNumber"] = strconv.Itoa(episode.AbsoluteEpisodeNumber)
	}
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
	entry, exists := h.store.Get(prequeueID)
	if !exists || !h.authorizeEntry(w, r, entry) {
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
	EpisodeResolver    *filter.SeriesEpisodeResolver
	TargetEpisode      *models.EpisodeReference
	IsDaily            bool   // True for daily shows (talk shows, news) that use date-based naming
	TargetAirDate      string // Air date from TVDB in YYYY-MM-DD format
	EpisodeAirYear     int    // Year the target episode aired, used to allow later-season year tags
	SeasonPremiereYear int    // Premiere year of the requested season only.
	EpisodeReleased    bool   // True only when metadata confirms the target episode has aired
	IsAnime            bool   // True for anime content - requires waiting for Nyaa scraper
	Year               int    // Series premiere year from metadata (used when frontend doesn't provide it)
	IMDBID             string // Resolved IMDb ID used by ID-aware search providers
	TVDBID             int64  // Resolved TVDB ID used by structured Newznab TV searches
	CountryCode        string // Original production country used to disambiguate regional remakes
	OriginalLanguage   string // Original spoken language; an audio preference matching it needs no pin
}

// createEpisodeResolverAndLookupAbsoluteEp fetches series metadata, creates an episode resolver,
// and looks up the absolute episode number for the target episode if not already set.
// Returns the episode resolver and an updated targetEpisode (with AbsoluteEpisodeNumber set if found).
func (h *PrequeueHandler) createEpisodeResolverAndLookupAbsoluteEp(ctx context.Context, titleID, titleName string, year int, imdbID string, targetEpisode *models.EpisodeReference) *SeriesMetadataResult {
	hasExplicitNumbering := targetEpisode != nil && targetEpisode.Numbering != nil
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
		IMDBID:  imdbID,
	}

	if targetEpisode != nil && targetEpisode.Numbering != nil {
		query.SeasonType = targetEpisode.Numbering.Ordering
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
	result.IMDBID = strings.TrimSpace(details.Title.IMDBID)
	result.TVDBID = details.Title.TVDBID
	result.CountryCode = strings.TrimSpace(details.Title.CountryCode)
	result.OriginalLanguage = strings.TrimSpace(details.Title.Language)

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

	// Never hydrate coordinates from a different provider/order after fallback.
	if targetEpisode != nil {
		if targetEpisode.Numbering != nil && !models.SameEpisodeNumbering(targetEpisode.Numbering, details.Numbering) {
			return result
		}
		copy := *targetEpisode
		copy.Numbering = details.Numbering
		targetEpisode = &copy
		result.TargetEpisode = targetEpisode
	}
	if len(details.Seasons) == 0 {
		log.Printf("[prequeue] No season data available for episode resolver")
		return result
	}

	// Build season -> episode count map AND lookup absolute episode number and air date
	seasonCounts := make(map[int]int)
	seasonNames := make(map[int]string)
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
			seasonNames[season.Number] = season.Name
		}

		// Look for the target episode's data if not already set
		if targetEpisode != nil && season.Number == targetEpisode.SeasonNumber {
			for _, ep := range season.Episodes {
				if ep.EpisodeNumber == targetEpisode.EpisodeNumber {
					epCopy := ep
					foundCanonicalEpisode = &epCopy
					// Keep the provider value as a fallback. Release-style numbering is
					// derived below after all positive-season counts are available.
					if ep.AbsoluteEpisodeNumber > 0 {
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

	if !hasExplicitNumbering && targetEpisode != nil && foundCanonicalEpisode == nil && targetEpisode.AbsoluteEpisodeNumber == 0 && targetEpisode.EpisodeNumber > 0 {
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
		result.EpisodeReleased = models.SeriesEpisodeHasAired(*foundCanonicalEpisode, time.Now())
		providerAbsoluteEp := foundAbsoluteEp
		if providerAbsoluteEp == 0 {
			providerAbsoluteEp = targetEpisode.AbsoluteEpisodeNumber
		}
		if releaseAbsolute := releaseAbsoluteEpisodeNumber(details.Seasons, *foundCanonicalEpisode); releaseAbsolute > 0 {
			foundAbsoluteEp = releaseAbsolute
			if providerAbsoluteEp > 0 && providerAbsoluteEp != releaseAbsolute {
				log.Printf("[prequeue] Using release-style absolute episode %d for S%02dE%02d instead of provider absolute %d",
					releaseAbsolute, foundCanonicalEpisode.SeasonNumber, foundCanonicalEpisode.EpisodeNumber, providerAbsoluteEp)
			}
		} else if foundAbsoluteEp == 0 && targetEpisode.AbsoluteEpisodeNumber == 0 {
			foundAbsoluteEp = inferAbsoluteEpisodeNumber(details.Seasons, *foundCanonicalEpisode)
			if foundAbsoluteEp > 0 {
				log.Printf("[prequeue] Inferred absolute episode number %d for S%02dE%02d from adjacent TVDB episodes",
					foundAbsoluteEp, foundCanonicalEpisode.SeasonNumber, foundCanonicalEpisode.EpisodeNumber)
			}
		}
		// Create a copy to avoid modifying the original
		updatedEpisode := &models.EpisodeReference{
			Numbering:             details.Numbering,
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

	if result.TargetEpisode != nil {
		result.SeasonPremiereYear = models.SeriesSeasonPremiereYear(details.Seasons, result.TargetEpisode.SeasonNumber)
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

	result.EpisodeResolver = filter.NewSeriesEpisodeResolver(seasonCounts).WithSeasonNames(seasonNames)
	return result
}

// inferAbsoluteEpisodeNumber uses known absolute numbers in the target season as
// anchors. Providers can publish a newly aired episode before TVDB fills its
// absoluteEpisodeNumber field, while adjacent episodes already establish the
// season's absolute numbering offset.
func inferAbsoluteEpisodeNumber(seasons []models.SeriesSeason, target models.SeriesEpisode) int {
	inferred := 0
	for _, season := range seasons {
		if season.Number != target.SeasonNumber {
			continue
		}
		for _, episode := range season.Episodes {
			if episode.AbsoluteEpisodeNumber <= 0 || episode.EpisodeNumber <= 0 {
				continue
			}
			candidate := episode.AbsoluteEpisodeNumber + target.EpisodeNumber - episode.EpisodeNumber
			if candidate <= 0 {
				continue
			}
			if inferred != 0 && inferred != candidate {
				return 0
			}
			inferred = candidate
		}
		break
	}
	return inferred
}

// findAudioTrackByLanguage wraps the helper function for backward compatibility
func (h *PrequeueHandler) findAudioTrackByLanguage(streams []AudioStreamInfo, preferredLanguage string) int {
	return FindAudioTrackByLanguage(streams, preferredLanguage)
}

// findSubtitleTrackByPreference wraps the helper function for backward compatibility
func (h *PrequeueHandler) findSubtitleTrackByPreference(streams []SubtitleStreamInfo, preferredLanguage, mode, audioLanguage string) int {
	return FindSubtitleTrackByPreference(streams, preferredLanguage, mode, audioLanguage)
}

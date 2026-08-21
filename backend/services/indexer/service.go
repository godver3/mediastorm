package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"novastream/config"
	"novastream/internal/apiusage"
	"novastream/internal/dnscache"
	"novastream/internal/httpheaders"
	"novastream/internal/mediaresolve"
	"novastream/internal/providerbreaker"
	"novastream/models"
	"novastream/services/debrid"
	"novastream/utils/filter"
	"novastream/utils/language"

	"github.com/mozillazg/go-unidecode"
)

// newznabQuerySanitizer removes special characters that interfere with newznab/torznab search APIs.
// Characters like !, ?, :, &, etc. are often interpreted as search operators or cause empty results.
// Periods are included because release names use them as word separators, so a title like
// "Once Upon a Time... in Hollywood" otherwise sends literal dots that break matching.
// Apostrophes/single quotes are handled separately (stripped without adding spaces) to keep
// contractions intact (e.g. "Don't" → "Dont" not "Don t").
var newznabQuerySanitizer = regexp.MustCompile(`[!?:&,/;"()[\]{}.]+`)

// xmlEntityPattern matches valid XML entity references: &name; &#NNN; &#xHHH;
var xmlEntityPattern = regexp.MustCompile(`^([a-zA-Z]+;|#[0-9]+;|#x[0-9a-fA-F]+;)`)

var (
	resolution2160Pattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(?:2160[pi]?|4k|uhd)([^a-z0-9]|$)`)
	resolution1080Pattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])1080[pi]?([^a-z0-9]|$)`)
	resolution720Pattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])720[pi]?([^a-z0-9]|$)`)
	resolution576Pattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])576[pi]?([^a-z0-9]|$)`)
	resolution480Pattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])480[pi]?([^a-z0-9]|$)`)
	releaseDedupTokenSep  = regexp.MustCompile(`[^a-z0-9]+`)
)

var sportsEventSideContentFilterTerms = []string{
	`/\bearly[\s._-]*prelims?\b/`,
	`/\bprelims?\b/`,
	`/\bembedded\b/`,
	`/\bvlog\b/`,
	`/\bweigh[\s._-]*ins?\b/`,
	`/\bpress[\s._-]*conference\b/`,
	`/\bpost[\s._-]*fight\b/`,
	`/\bcountdown\b/`,
}

const (
	searchResultsCacheTTL        = 15 * time.Minute
	searchResultsCacheMaxEntries = 256
)

// sanitizeXMLAmpersands escapes unescaped ampersands in XML that aren't part of valid entity references.
// This fixes malformed XML from indexers that don't properly escape titles like "Tom & Jerry".
func sanitizeXMLAmpersands(data []byte) ([]byte, int) {
	var result []byte
	fixCount := 0
	i := 0
	for i < len(data) {
		if data[i] == '&' {
			// Check if this is a valid entity reference
			remaining := data[i+1:]
			if xmlEntityPattern.Match(remaining) {
				// Valid entity, keep as-is
				result = append(result, '&')
			} else {
				// Bare ampersand, escape it
				result = append(result, []byte("&amp;")...)
				fixCount++
			}
		} else {
			result = append(result, data[i])
		}
		i++
	}
	return result, fixCount
}

// sanitizeNewznabQuery cleans up a search query for newznab/torznab APIs.
func sanitizeNewznabQuery(query string) string {
	cleaned := normalizeToASCII(query)
	// Strip apostrophes/single quotes without adding spaces (keeps contractions like "Don't" → "Dont")
	cleaned = strings.ReplaceAll(cleaned, "'", "")
	cleaned = strings.ReplaceAll(cleaned, "\u2019", "") // right single quotation mark (curly apostrophe)
	cleaned = strings.ReplaceAll(cleaned, "\u2018", "") // left single quotation mark
	// Remove other problematic special characters (replace with space)
	cleaned = newznabQuerySanitizer.ReplaceAllString(cleaned, " ")
	// Collapse multiple spaces into one and trim
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return cleaned
}

// userSettingsProvider retrieves per-user settings.
type userSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

// clientSettingsProvider retrieves per-(device, person) filter settings.
type clientSettingsProvider interface {
	Get(clientID, userID string) (*models.ClientFilterSettings, error)
}

type (
	debridSearchService interface {
		Search(context.Context, debrid.SearchOptions) ([]models.NZBResult, error)
	}

	debridPlaybackService interface {
		FilterCachedResults(context.Context, []models.NZBResult) []models.NZBResult
	}

	metadataSearchService interface {
		Search(context.Context, string, string) ([]models.SearchResult, error)
	}

	// metadataAliasService is optionally implemented by the metadata service
	// to provide full TVDB aliases (international titles) for a given title.
	metadataAliasService interface {
		FetchAliases(mediaType string, tvdbID int64) []string
		FetchAliasesWithLanguage(mediaType string, tvdbID int64) []models.LanguageAlias
	}
)

type Service struct {
	cfg            *config.Manager
	httpc          *http.Client
	debrid         debridSearchService
	debridPlayback debridPlaybackService
	metadata       metadataSearchService
	userSettings   userSettingsProvider
	clientSettings clientSettingsProvider

	searchCacheMu sync.RWMutex
	searchCache   map[string]searchCacheEntry

	// Usenet search call counters for diagnostics (atomic, safe for concurrent use).
	// Grep logs for [search-stats] to see totals during playback.
	searchCount        atomic.Int64 // top-level Search calls (manual search)
	searchSplitCount   atomic.Int64 // top-level SearchWithScoringSplit calls (prequeue streaming search)
	usenetAPICallCount atomic.Int64 // individual usenet/torznab indexer API calls
	providerBreaker    *providerbreaker.Breaker
}

type searchCacheEntry struct {
	results   []models.NZBResult
	expiresAt time.Time
}

func NewService(cfg *config.Manager, metadataSvc metadataSearchService, debridSvc debridSearchService) *Service {
	if debridSvc == nil {
		debridSvc = debrid.NewSearchService(cfg)
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	dnscache.ConfigureTransport(transport, dnscache.DefaultTTL)

	return &Service{
		cfg: cfg,
		httpc: &http.Client{
			Timeout:   20 * time.Second,
			Transport: transport,
		},
		debrid:          debridSvc,
		debridPlayback:  debrid.NewPlaybackService(cfg, nil),
		metadata:        metadataSvc,
		searchCache:     make(map[string]searchCacheEntry),
		providerBreaker: providerbreaker.Shared(),
	}
}

// ClearSearchCache drops cached Search results. It is called when ranking or
// filtering settings change, and can also be used by tests/admin tools.
func (s *Service) ClearSearchCache() {
	if s == nil {
		return
	}
	s.searchCacheMu.Lock()
	defer s.searchCacheMu.Unlock()
	s.searchCache = make(map[string]searchCacheEntry)
}

// SetUserSettingsProvider sets the user settings provider for per-user filtering.
func (s *Service) SetUserSettingsProvider(provider userSettingsProvider) {
	s.userSettings = provider
}

// SetClientSettingsProvider sets the client settings provider for per-client filtering.
func (s *Service) SetClientSettingsProvider(provider clientSettingsProvider) {
	s.clientSettings = provider
}

// effectiveOverrides holds settings that were relocated from FilterSettings but
// still cascade through the global -> profile -> client override chain.
type effectiveOverrides struct {
	BypassFilteringForAIOStreamsOnly *bool
	MaxResultsPerResolution          *int
}

type effectiveFilterBundle struct {
	Default models.FilterSettings
	Debrid  models.FilterSettings
	Usenet  models.FilterSettings
}

type effectiveRankingBundle struct {
	Default            []config.RankingCriterion
	Debrid             []config.RankingCriterion
	Usenet             []config.RankingCriterion
	NewestReleaseFirst bool
}

func filterSettingsFromConfig(in config.FilterSettings) models.FilterSettings {
	return models.FilterSettings{
		MaxSizeMovieGB:             models.FloatPtr(in.MaxSizeMovieGB),
		MaxSizeEpisodeGB:           models.FloatPtr(in.MaxSizeEpisodeGB),
		MaxResolution:              models.StringPtr(in.MaxResolution),
		HDRDVPolicy:                models.HDRDVPolicy(in.HDRDVPolicy),
		RequiredTerms:              append([]string(nil), in.RequiredTerms...),
		FilterOutTerms:             append([]string(nil), in.FilterOutTerms...),
		PreferredTerms:             append([]string(nil), in.PreferredTerms...),
		NonPreferredTerms:          append([]string(nil), in.NonPreferredTerms...),
		DownloadPreferredTerms:     append([]string(nil), in.DownloadPreferredTerms...),
		PreferredScraper:           models.StringPtr(in.PreferredScraper),
		ServicePriority:            models.StringPtr(string(in.ServicePriority)),
		UnknownTrackPolicy:         string(in.UnknownTrackPolicy),
		AdaptivePlaybackEnabled:    models.BoolPtr(in.AdaptivePlaybackEnabled),
		AdaptiveTargetBufferFactor: models.FloatPtr(in.AdaptiveTargetBufferFactor),
	}
}

func applyUserFilterOverrides(dst *models.FilterSettings, src models.FilterSettings) {
	if src.MaxSizeMovieGB != nil {
		dst.MaxSizeMovieGB = src.MaxSizeMovieGB
	}
	if src.MaxSizeEpisodeGB != nil {
		dst.MaxSizeEpisodeGB = src.MaxSizeEpisodeGB
	}
	if src.MaxResolution != nil {
		dst.MaxResolution = src.MaxResolution
	}
	if src.HDRDVPolicy != "" {
		dst.HDRDVPolicy = src.HDRDVPolicy
	}
	if src.RequiredTerms != nil {
		dst.RequiredTerms = src.RequiredTerms
	}
	if src.FilterOutTerms != nil {
		dst.FilterOutTerms = src.FilterOutTerms
	}
	if src.PreferredTerms != nil {
		dst.PreferredTerms = src.PreferredTerms
	}
	if src.NonPreferredTerms != nil {
		dst.NonPreferredTerms = src.NonPreferredTerms
	}
	if src.DownloadPreferredTerms != nil {
		dst.DownloadPreferredTerms = src.DownloadPreferredTerms
	}
	if src.PreferredScraper != nil {
		dst.PreferredScraper = src.PreferredScraper
	}
	if src.ServicePriority != nil {
		dst.ServicePriority = src.ServicePriority
	}
	if src.UnknownTrackPolicy != "" {
		dst.UnknownTrackPolicy = src.UnknownTrackPolicy
	}
	if src.AdaptivePlaybackEnabled != nil {
		dst.AdaptivePlaybackEnabled = src.AdaptivePlaybackEnabled
	}
	if src.AdaptiveTargetBufferFactor != nil {
		dst.AdaptiveTargetBufferFactor = src.AdaptiveTargetBufferFactor
	}
}

func applyClientFilterOverrides(dst *models.FilterSettings, src *models.ClientFilterSettings) {
	if src == nil {
		return
	}
	if src.MaxSizeMovieGB != nil {
		dst.MaxSizeMovieGB = src.MaxSizeMovieGB
	}
	if src.MaxSizeEpisodeGB != nil {
		dst.MaxSizeEpisodeGB = src.MaxSizeEpisodeGB
	}
	if src.MaxResolution != nil {
		dst.MaxResolution = models.StringPtr(*src.MaxResolution)
	}
	if src.HDRDVPolicy != nil {
		dst.HDRDVPolicy = *src.HDRDVPolicy
	}
	if src.RequiredTerms != nil {
		dst.RequiredTerms = *src.RequiredTerms
	}
	if src.FilterOutTerms != nil {
		dst.FilterOutTerms = *src.FilterOutTerms
	}
	if src.PreferredTerms != nil {
		dst.PreferredTerms = *src.PreferredTerms
	}
	if src.NonPreferredTerms != nil {
		dst.NonPreferredTerms = *src.NonPreferredTerms
	}
	if src.DownloadPreferredTerms != nil {
		dst.DownloadPreferredTerms = *src.DownloadPreferredTerms
	}
	if src.UnknownTrackPolicy != nil {
		dst.UnknownTrackPolicy = *src.UnknownTrackPolicy
	}
}

func filterBundleForService(bundle effectiveFilterBundle, serviceType models.ContentServiceType) models.FilterSettings {
	switch serviceType {
	case models.ServiceTypeDebrid:
		return bundle.Debrid
	case models.ServiceTypeUsenet:
		return bundle.Usenet
	default:
		return bundle.Default
	}
}

// getEffectiveFilterSettings returns the filtering settings to use for a search.
// Settings cascade: Global -> Profile -> Client (client settings win)
func (s *Service) getEffectiveFilterSettings(userID, clientID string, globalSettings config.Settings) (models.FilterSettings, models.AnimeFilteringSettings, effectiveOverrides) {
	// Start with global settings (as pointers)
	filterSettings := models.FilterSettings{
		MaxSizeMovieGB:             models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB:           models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:              models.StringPtr(globalSettings.Filtering.MaxResolution),
		HDRDVPolicy:                models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
		RequiredTerms:              globalSettings.Filtering.RequiredTerms,
		FilterOutTerms:             globalSettings.Filtering.FilterOutTerms,
		PreferredTerms:             globalSettings.Filtering.PreferredTerms,
		NonPreferredTerms:          globalSettings.Filtering.NonPreferredTerms,
		DownloadPreferredTerms:     globalSettings.Filtering.DownloadPreferredTerms,
		PreferredScraper:           models.StringPtr(globalSettings.Filtering.PreferredScraper),
		ServicePriority:            models.StringPtr(string(globalSettings.Filtering.ServicePriority)),
		UnknownTrackPolicy:         string(globalSettings.Filtering.UnknownTrackPolicy),
		AdaptivePlaybackEnabled:    models.BoolPtr(globalSettings.Filtering.AdaptivePlaybackEnabled),
		AdaptiveTargetBufferFactor: models.FloatPtr(globalSettings.Filtering.AdaptiveTargetBufferFactor),
	}
	overrides := effectiveOverrides{
		BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Display.BypassFilteringForAIOStreamsOnly),
		MaxResultsPerResolution:          models.IntPtr(globalSettings.Playback.MaxResultsPerResolution),
	}
	animeSettings := models.AnimeFilteringSettings{
		AnimeLanguageEnabled:   models.BoolPtr(globalSettings.AnimeFiltering.AnimeLanguageEnabled),
		AnimePreferredLanguage: models.StringPtr(globalSettings.AnimeFiltering.AnimePreferredLanguage),
	}

	// Layer 2: Profile settings override global (field-by-field, only if set)
	if userID != "" && s.userSettings != nil {
		userSettings, err := s.userSettings.Get(userID)
		if err != nil {
			log.Printf("[indexer] failed to get user settings for %s: %v", userID, err)
		} else if userSettings != nil {
			log.Printf("[indexer] using per-user filtering settings for user %s", userID)
			profileFiltering := userSettings.Filtering
			if profileFiltering.MaxSizeMovieGB != nil {
				filterSettings.MaxSizeMovieGB = profileFiltering.MaxSizeMovieGB
			}
			if profileFiltering.MaxSizeEpisodeGB != nil {
				filterSettings.MaxSizeEpisodeGB = profileFiltering.MaxSizeEpisodeGB
			}
			if profileFiltering.MaxResolution != nil {
				filterSettings.MaxResolution = profileFiltering.MaxResolution
			}
			if profileFiltering.HDRDVPolicy != "" {
				filterSettings.HDRDVPolicy = profileFiltering.HDRDVPolicy
			}
			if profileFiltering.RequiredTerms != nil {
				filterSettings.RequiredTerms = profileFiltering.RequiredTerms
			}
			if profileFiltering.FilterOutTerms != nil {
				filterSettings.FilterOutTerms = profileFiltering.FilterOutTerms
			}
			if profileFiltering.PreferredTerms != nil {
				filterSettings.PreferredTerms = profileFiltering.PreferredTerms
			}
			if profileFiltering.NonPreferredTerms != nil {
				filterSettings.NonPreferredTerms = profileFiltering.NonPreferredTerms
			}
			if profileFiltering.DownloadPreferredTerms != nil {
				filterSettings.DownloadPreferredTerms = profileFiltering.DownloadPreferredTerms
			}
			applyUserFilterOverrides(&filterSettings, profileFiltering)
			if userSettings.Display.BypassFilteringForAIOStreamsOnly != nil {
				overrides.BypassFilteringForAIOStreamsOnly = userSettings.Display.BypassFilteringForAIOStreamsOnly
			}
			if userSettings.Playback.MaxResultsPerResolution != nil {
				overrides.MaxResultsPerResolution = userSettings.Playback.MaxResultsPerResolution
			}
			profileAnime := userSettings.AnimeFiltering
			if profileAnime.AnimeLanguageEnabled != nil {
				animeSettings.AnimeLanguageEnabled = profileAnime.AnimeLanguageEnabled
			}
			if profileAnime.AnimePreferredLanguage != nil {
				animeSettings.AnimePreferredLanguage = profileAnime.AnimePreferredLanguage
			}
		}
	}

	// Layer 3: Client settings override profile (field-by-field, only if set)
	if clientID != "" && s.clientSettings != nil {
		clientSettings, err := s.clientSettings.Get(clientID, userID)
		if err != nil {
			log.Printf("[indexer] failed to get client settings for %s: %v", clientID, err)
		} else if clientSettings != nil && !clientSettings.IsEmpty() {
			log.Printf("[indexer] applying per-client filtering overrides for client %s", clientID)
			if clientSettings.MaxSizeMovieGB != nil {
				filterSettings.MaxSizeMovieGB = clientSettings.MaxSizeMovieGB
			}
			if clientSettings.MaxSizeEpisodeGB != nil {
				filterSettings.MaxSizeEpisodeGB = clientSettings.MaxSizeEpisodeGB
			}
			if clientSettings.MaxResolution != nil {
				filterSettings.MaxResolution = models.StringPtr(*clientSettings.MaxResolution)
			}
			if clientSettings.HDRDVPolicy != nil {
				filterSettings.HDRDVPolicy = *clientSettings.HDRDVPolicy
			}
			if clientSettings.RequiredTerms != nil {
				filterSettings.RequiredTerms = *clientSettings.RequiredTerms
			}
			if clientSettings.FilterOutTerms != nil {
				filterSettings.FilterOutTerms = *clientSettings.FilterOutTerms
			}
			if clientSettings.PreferredTerms != nil {
				filterSettings.PreferredTerms = *clientSettings.PreferredTerms
			}
			if clientSettings.NonPreferredTerms != nil {
				filterSettings.NonPreferredTerms = *clientSettings.NonPreferredTerms
			}
			if clientSettings.DownloadPreferredTerms != nil {
				filterSettings.DownloadPreferredTerms = *clientSettings.DownloadPreferredTerms
			}
			if clientSettings.UnknownTrackPolicy != nil {
				filterSettings.UnknownTrackPolicy = *clientSettings.UnknownTrackPolicy
			}
			if clientSettings.BypassFilteringForAIOStreamsOnly != nil {
				overrides.BypassFilteringForAIOStreamsOnly = clientSettings.BypassFilteringForAIOStreamsOnly
			}
			if clientSettings.MaxResultsPerResolution != nil {
				overrides.MaxResultsPerResolution = clientSettings.MaxResultsPerResolution
			}
			if clientSettings.AnimeLanguageEnabled != nil {
				animeSettings.AnimeLanguageEnabled = clientSettings.AnimeLanguageEnabled
			}
			if clientSettings.AnimePreferredLanguage != nil {
				animeSettings.AnimePreferredLanguage = clientSettings.AnimePreferredLanguage
			}

			// Layer 4: Adaptive playback overlays transient size/HDR caps derived
			// from this device's reported throughput + display capability. Gated by
			// the global toggle; computed on the fly and never persisted into the
			// flat filter fields.
			models.ComputeAdaptiveCaps(
				models.BoolVal(filterSettings.AdaptivePlaybackEnabled, globalSettings.Filtering.AdaptivePlaybackEnabled),
				models.FloatVal(filterSettings.AdaptiveTargetBufferFactor, globalSettings.Filtering.AdaptiveTargetBufferFactor),
				clientSettings.AdaptivePlayback,
				time.Now(),
			).ApplyTo(&filterSettings)
		}
	}

	return filterSettings, animeSettings, overrides
}

func (s *Service) getEffectiveFilterBundle(userID, clientID string, globalSettings config.Settings) (effectiveFilterBundle, models.AnimeFilteringSettings, effectiveOverrides) {
	base, animeSettings, overrides := s.getEffectiveFilterSettings(userID, clientID, globalSettings)
	bundle := effectiveFilterBundle{Default: base, Debrid: base, Usenet: base}
	var adaptivePlayback *models.AdaptivePlaybackSettings

	splitByService := globalSettings.Filtering.SplitByService
	if splitByService {
		if globalSettings.Filtering.Debrid != nil {
			debridFilter := filterSettingsFromConfig(*globalSettings.Filtering.Debrid)
			applyUserFilterOverrides(&bundle.Debrid, debridFilter)
		}
		if globalSettings.Filtering.Usenet != nil {
			usenetFilter := filterSettingsFromConfig(*globalSettings.Filtering.Usenet)
			applyUserFilterOverrides(&bundle.Usenet, usenetFilter)
		}
	}

	if userID != "" && s.userSettings != nil {
		userSettings, err := s.userSettings.Get(userID)
		if err != nil {
			log.Printf("[indexer] failed to get user split filtering settings for %s: %v", userID, err)
		} else if userSettings != nil {
			if userSettings.Filtering.SplitByService != nil {
				splitByService = *userSettings.Filtering.SplitByService
			}
			if splitByService {
				if userSettings.Filtering.Debrid != nil {
					applyUserFilterOverrides(&bundle.Debrid, *userSettings.Filtering.Debrid)
				}
				if userSettings.Filtering.Usenet != nil {
					applyUserFilterOverrides(&bundle.Usenet, *userSettings.Filtering.Usenet)
				}
			}
		}
	}

	if clientID != "" && s.clientSettings != nil {
		clientSettings, err := s.clientSettings.Get(clientID, userID)
		if err != nil {
			log.Printf("[indexer] failed to get client split filtering settings for %s: %v", clientID, err)
		} else if clientSettings != nil {
			if clientSettings.SplitByService != nil {
				splitByService = *clientSettings.SplitByService
			}
			adaptivePlayback = clientSettings.AdaptivePlayback
			if splitByService {
				applyClientFilterOverrides(&bundle.Debrid, clientSettings.Debrid)
				applyClientFilterOverrides(&bundle.Usenet, clientSettings.Usenet)
			}
		}
	}

	if !splitByService {
		bundle.Debrid = bundle.Default
		bundle.Usenet = bundle.Default
	}

	caps := models.ComputeAdaptiveCaps(
		models.BoolVal(base.AdaptivePlaybackEnabled, globalSettings.Filtering.AdaptivePlaybackEnabled),
		models.FloatVal(base.AdaptiveTargetBufferFactor, globalSettings.Filtering.AdaptiveTargetBufferFactor),
		adaptivePlayback,
		time.Now(),
	)
	caps.ApplyTo(&bundle.Default)
	caps.ApplyTo(&bundle.Debrid)
	caps.ApplyTo(&bundle.Usenet)

	return bundle, animeSettings, overrides
}

// getEffectiveRankingCriteria returns the ranking criteria to use for sorting search results.
// Settings cascade: Global -> Profile -> Client (most specific wins)
func (s *Service) getEffectiveRankingCriteria(userID, clientID string, globalSettings config.Settings) []config.RankingCriterion {
	// Start with global settings
	criteria := make([]config.RankingCriterion, len(globalSettings.Ranking.Criteria))
	copy(criteria, globalSettings.Ranking.Criteria)

	// If no criteria configured, use defaults
	if len(criteria) == 0 {
		criteria = config.DefaultRankingCriteria()
	}

	// Layer 2: Profile settings override global
	if userID != "" && s.userSettings != nil {
		userSettings, err := s.userSettings.Get(userID)
		if err != nil {
			log.Printf("[indexer] failed to get user settings for ranking %s: %v", userID, err)
		} else if userSettings != nil && userSettings.Ranking != nil && len(userSettings.Ranking.Criteria) > 0 {
			log.Printf("[indexer] applying per-user ranking settings for user %s", userID)
			criteria = applyUserRankingOverrides(criteria, userSettings.Ranking.Criteria)
		}
	}

	// Layer 3: Client settings override profile
	if clientID != "" && s.clientSettings != nil {
		clientSettings, err := s.clientSettings.Get(clientID, userID)
		if err != nil {
			log.Printf("[indexer] failed to get client settings for ranking %s: %v", clientID, err)
		} else if clientSettings != nil && clientSettings.RankingCriteria != nil && len(*clientSettings.RankingCriteria) > 0 {
			log.Printf("[indexer] applying per-client ranking settings for client %s", clientID)
			criteria = applyClientRankingOverrides(criteria, *clientSettings.RankingCriteria)
		}
	}

	// Sort by Order field
	sort.SliceStable(criteria, func(i, j int) bool {
		return criteria[i].Order < criteria[j].Order
	})

	return criteria
}

func sortedRankingCriteria(criteria []config.RankingCriterion) []config.RankingCriterion {
	out := append([]config.RankingCriterion(nil), criteria...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Order < out[j].Order
	})
	return out
}

func rankingCriteriaFromConfig(settings config.RankingSettings, fallback []config.RankingCriterion) []config.RankingCriterion {
	if len(settings.Criteria) == 0 {
		return append([]config.RankingCriterion(nil), fallback...)
	}
	return sortedRankingCriteria(settings.Criteria)
}

func (s *Service) getEffectiveRankingBundle(userID, clientID string, globalSettings config.Settings) effectiveRankingBundle {
	base := s.getEffectiveRankingCriteria(userID, clientID, globalSettings)
	bundle := effectiveRankingBundle{
		Default:            base,
		Debrid:             base,
		Usenet:             base,
		NewestReleaseFirst: globalSettings.Ranking.NewestReleaseFirst,
	}

	splitByService := globalSettings.Ranking.SplitByService
	if splitByService {
		if globalSettings.Ranking.Debrid != nil {
			bundle.Debrid = rankingCriteriaFromConfig(*globalSettings.Ranking.Debrid, base)
		}
		if globalSettings.Ranking.Usenet != nil {
			bundle.Usenet = rankingCriteriaFromConfig(*globalSettings.Ranking.Usenet, base)
		}
	}

	if userID != "" && s.userSettings != nil {
		userSettings, err := s.userSettings.Get(userID)
		if err != nil {
			log.Printf("[indexer] failed to get user split ranking settings for %s: %v", userID, err)
		} else if userSettings != nil && userSettings.Ranking != nil {
			if userSettings.Ranking.NewestReleaseFirst != nil {
				bundle.NewestReleaseFirst = *userSettings.Ranking.NewestReleaseFirst
			}
			if userSettings.Ranking.SplitByService != nil {
				splitByService = *userSettings.Ranking.SplitByService
			}
			if splitByService {
				if userSettings.Ranking.Debrid != nil && len(userSettings.Ranking.Debrid.Criteria) > 0 {
					bundle.Debrid = applyUserRankingOverrides(bundle.Debrid, userSettings.Ranking.Debrid.Criteria)
					bundle.Debrid = sortedRankingCriteria(bundle.Debrid)
				}
				if userSettings.Ranking.Usenet != nil && len(userSettings.Ranking.Usenet.Criteria) > 0 {
					bundle.Usenet = applyUserRankingOverrides(bundle.Usenet, userSettings.Ranking.Usenet.Criteria)
					bundle.Usenet = sortedRankingCriteria(bundle.Usenet)
				}
			}
		}
	}

	if clientID != "" && s.clientSettings != nil {
		clientSettings, err := s.clientSettings.Get(clientID, userID)
		if err != nil {
			log.Printf("[indexer] failed to get client split ranking settings for %s: %v", clientID, err)
		} else if clientSettings != nil {
			if clientSettings.NewestReleaseFirst != nil {
				bundle.NewestReleaseFirst = *clientSettings.NewestReleaseFirst
			}
			if clientSettings.RankingSplitByService != nil {
				splitByService = *clientSettings.RankingSplitByService
			}
			if splitByService {
				if clientSettings.DebridRankingCriteria != nil && len(*clientSettings.DebridRankingCriteria) > 0 {
					bundle.Debrid = applyClientRankingOverrides(bundle.Debrid, *clientSettings.DebridRankingCriteria)
					bundle.Debrid = sortedRankingCriteria(bundle.Debrid)
				}
				if clientSettings.UsenetRankingCriteria != nil && len(*clientSettings.UsenetRankingCriteria) > 0 {
					bundle.Usenet = applyClientRankingOverrides(bundle.Usenet, *clientSettings.UsenetRankingCriteria)
					bundle.Usenet = sortedRankingCriteria(bundle.Usenet)
				}
			}
		}
	}

	if !splitByService {
		bundle.Debrid = bundle.Default
		bundle.Usenet = bundle.Default
	}

	return bundle
}

func (s *Service) getEffectiveMetadataLanguage(userID string, globalSettings config.Settings) string {
	primary := globalSettings.Metadata.EffectivePrimaryLanguage()
	if userID == "" || s.userSettings == nil {
		return primary
	}
	userSettings, err := s.userSettings.Get(userID)
	if err != nil {
		log.Printf("[indexer] failed to get user settings for metadata language %s: %v", userID, err)
		return primary
	}
	if userSettings == nil {
		return primary
	}
	profileLanguage := strings.TrimSpace(userSettings.Metadata.PrimaryLanguage)
	if profileLanguage == "" {
		return primary
	}
	for _, lang := range globalSettings.Metadata.Language {
		if strings.EqualFold(strings.TrimSpace(lang), profileLanguage) {
			return strings.TrimSpace(lang)
		}
	}
	return primary
}

// applyUserRankingOverrides applies user-level ranking overrides to the base criteria.
func applyUserRankingOverrides(base []config.RankingCriterion, overrides []models.UserRankingCriterion) []config.RankingCriterion {
	result := make([]config.RankingCriterion, len(base))
	copy(result, base)

	overrideMap := make(map[config.RankingCriterionID]models.UserRankingCriterion)
	for _, o := range overrides {
		overrideMap[o.ID] = o
	}

	for i := range result {
		if override, ok := overrideMap[result[i].ID]; ok {
			if override.Enabled != nil {
				result[i].Enabled = *override.Enabled
			}
			if override.Order != nil {
				result[i].Order = *override.Order
			}
		}
	}

	return result
}

// applyClientRankingOverrides applies client-level ranking overrides to the base criteria.
func applyClientRankingOverrides(base []config.RankingCriterion, overrides []models.ClientRankingCriterion) []config.RankingCriterion {
	result := make([]config.RankingCriterion, len(base))
	copy(result, base)

	overrideMap := make(map[config.RankingCriterionID]models.ClientRankingCriterion)
	for _, o := range overrides {
		overrideMap[o.ID] = o
	}

	for i := range result {
		if override, ok := overrideMap[result[i].ID]; ok {
			if override.Enabled != nil {
				result[i].Enabled = *override.Enabled
			}
			if override.Order != nil {
				result[i].Order = *override.Order
			}
		}
	}

	return result
}

// Comparison functions return -1 if i wins, 0 if tie, 1 if j wins.

func compareServicePriority(i, j models.NZBResult, priority config.StreamingServicePriority) int {
	if priority == config.StreamingServicePriorityNone {
		return 0
	}
	iIsPrioritized := (priority == config.StreamingServicePriorityUsenet && i.ServiceType == models.ServiceTypeUsenet) ||
		(priority == config.StreamingServicePriorityDebrid && i.ServiceType == models.ServiceTypeDebrid)
	jIsPrioritized := (priority == config.StreamingServicePriorityUsenet && j.ServiceType == models.ServiceTypeUsenet) ||
		(priority == config.StreamingServicePriorityDebrid && j.ServiceType == models.ServiceTypeDebrid)

	if iIsPrioritized && !jIsPrioritized {
		return -1
	}
	if !iIsPrioritized && jIsPrioritized {
		return 1
	}
	return 0
}

func comparePreferredTerms(i, j models.NZBResult, terms []filter.CompiledTerm) int {
	if len(terms) == 0 {
		return 0
	}
	iWeight, _ := filter.SumMatchedWeights(i.Title, terms)
	jWeight, _ := filter.SumMatchedWeights(j.Title, terms)
	if iWeight > jWeight {
		return -1
	}
	if iWeight < jWeight {
		return 1
	}
	return 0
}

func compareNonPreferredTerms(i, j models.NZBResult, terms []filter.CompiledTerm) int {
	if len(terms) == 0 {
		return 0
	}
	iWeight, _ := filter.SumMatchedWeights(i.Title, terms)
	jWeight, _ := filter.SumMatchedWeights(j.Title, terms)
	if iWeight > jWeight {
		return 1 // i has more non-preferred weight → sort lower
	}
	if iWeight < jWeight {
		return -1 // j has more non-preferred weight → i sorts higher
	}
	return 0
}

func compareResolution(i, j models.NZBResult) int {
	resI := extractResolutionFromResult(i)
	resJ := extractResolutionFromResult(j)
	if resI > resJ {
		return -1
	}
	if resI < resJ {
		return 1
	}
	return 0
}

func compareLanguage(i, j models.NZBResult, preferredLang string) int {
	if preferredLang == "" {
		return 0
	}
	iHas := language.HasPreferredLanguage(i.Attributes["languages"], preferredLang)
	jHas := language.HasPreferredLanguage(j.Attributes["languages"], preferredLang)
	if iHas && !jHas {
		return -1
	}
	if !iHas && jHas {
		return 1
	}
	return 0
}

func compareSize(i, j models.NZBResult) int {
	iSize := i.EffectiveItemSizeBytes()
	jSize := j.EffectiveItemSizeBytes()
	if iSize > jSize {
		return -1
	}
	if iSize < jSize {
		return 1
	}
	return 0
}

func comparePreferredScraper(i, j models.NZBResult, preferredScraper string) int {
	if preferredScraper == "" {
		return 0
	}
	iMatch := strings.EqualFold(i.Indexer, preferredScraper)
	jMatch := strings.EqualFold(j.Indexer, preferredScraper)
	if iMatch && !jMatch {
		return -1
	}
	if !iMatch && jMatch {
		return 1
	}
	return 0
}

func compareDownloadPreferredTerms(i, j models.NZBResult, terms []filter.CompiledTerm) int {
	if len(terms) == 0 {
		return 0
	}
	iWeight, _ := filter.SumMatchedWeights(i.Title, terms)
	jWeight, _ := filter.SumMatchedWeights(j.Title, terms)
	if iWeight > jWeight {
		return -1
	}
	if iWeight < jWeight {
		return 1
	}
	return 0
}

// compareEpisodeYearMatch gives targeted episode results matching the series
// year precedence only when another passed result has a conflicting year.
func compareEpisodeYearMatch(i, j models.NZBResult) int {
	iMatch := i.Attributes["episodeYearPriority"] == "true"
	jMatch := j.Attributes["episodeYearPriority"] == "true"
	if iMatch && !jMatch {
		return -1
	}
	if !iMatch && jMatch {
		return 1
	}
	return 0
}

func episodeYearWithinTolerance(candidate, expected int) bool {
	if candidate <= 0 || expected <= 0 {
		return false
	}
	difference := candidate - expected
	if difference < 0 {
		difference = -difference
	}
	return difference <= filter.MaxYearDifference
}

// applyEpisodeYearPriority activates series-year precedence only when an
// explicit, conflicting year remains in the passed result set. A target
// episode's air year is valid and must not be treated as a reboot conflict.
func applyEpisodeYearPriority(results []models.NZBResult, seriesYear, episodeAirYear int) {
	for i := range results {
		delete(results[i].Attributes, "episodeYearPriority")
	}
	if seriesYear <= 0 {
		return
	}

	hasConflict := false
	for _, result := range results {
		releaseYear, err := strconv.Atoi(result.Attributes["episodeReleaseYear"])
		if err != nil || releaseYear <= 0 {
			continue
		}
		if episodeYearWithinTolerance(releaseYear, seriesYear) || episodeYearWithinTolerance(releaseYear, episodeAirYear) {
			continue
		}
		hasConflict = true
		break
	}
	if !hasConflict {
		return
	}

	for i := range results {
		if results[i].Attributes["episodeYearMatch"] == "true" {
			results[i].Attributes["episodeYearPriority"] = "true"
		}
	}
}

func compareDeterministicTieBreaker(i, j models.NZBResult) int {
	valuesI := []string{
		strings.ToLower(strings.TrimSpace(i.Title)),
		strings.ToLower(strings.TrimSpace(string(i.ServiceType))),
		strings.ToLower(strings.TrimSpace(i.Indexer)),
		strings.ToLower(strings.TrimSpace(i.GUID)),
	}
	valuesJ := []string{
		strings.ToLower(strings.TrimSpace(j.Title)),
		strings.ToLower(strings.TrimSpace(string(j.ServiceType))),
		strings.ToLower(strings.TrimSpace(j.Indexer)),
		strings.ToLower(strings.TrimSpace(j.GUID)),
	}
	for idx := range valuesI {
		if valuesI[idx] < valuesJ[idx] {
			return -1
		}
		if valuesI[idx] > valuesJ[idx] {
			return 1
		}
	}
	return 0
}

func compareCountryMatch(i, j models.NZBResult) int {
	if i.Attributes["countryMatch"] != j.Attributes["countryMatch"] {
		if i.Attributes["countryMatch"] == "true" {
			return -1
		}
		return 1
	}
	return 0
}

func compareByRankingCriteria(i, j models.NZBResult, scoringCtx ScoringContext) int {
	if cmp := compareCountryMatch(i, j); cmp != 0 {
		return cmp
	}
	if cmp := compareEpisodeYearMatch(i, j); cmp != 0 {
		return cmp
	}

	if scoringCtx.UseDownloadRanking {
		if cmp := compareDownloadPreferredTerms(i, j, scoringCtx.DownloadPreferredTerms); cmp != 0 {
			return cmp
		}
	}

	for _, criterion := range scoringCtx.RankingCriteria {
		if !criterion.Enabled {
			continue
		}

		var cmp int
		switch criterion.ID {
		case config.RankingServicePriority:
			cmp = compareServicePriority(i, j, scoringCtx.ServicePriority)
		case config.RankingPreferredTerms:
			cmp = comparePreferredTerms(i, j, scoringCtx.PreferredTerms)
		case config.RankingNonPreferredTerms:
			cmp = compareNonPreferredTerms(i, j, scoringCtx.NonPreferredTerms)
		case config.RankingResolution:
			cmp = compareResolution(i, j)
		case config.RankingLanguage:
			cmp = compareLanguage(i, j, scoringCtx.PreferredLang)
		case config.RankingSize:
			cmp = compareSize(i, j)
		case config.RankingPreferredScraper:
			cmp = comparePreferredScraper(i, j, scoringCtx.PreferredScraper)
		}
		if cmp != 0 {
			return cmp
		}
	}

	return compareDeterministicTieBreaker(i, j)
}

type rankedServiceGroup struct {
	indices  []int
	criteria []config.RankingCriterion
	position int
}

// rankingOrderByBundle first ranks each service independently with its own
// criteria, then merges the ordered service lists using the shared criteria.
// This preserves the meaning of each service-specific order while allowing the
// overall ranking (especially Service Priority) to control how services interleave.
func rankingOrderByBundle(results []models.NZBResult, baseCtx ScoringContext, rankings effectiveRankingBundle) []int {
	groups := []*rankedServiceGroup{
		{criteria: rankings.Debrid},
		{criteria: rankings.Usenet},
		{criteria: rankings.Default},
	}
	for i := range results {
		switch results[i].ServiceType {
		case models.ServiceTypeDebrid:
			groups[0].indices = append(groups[0].indices, i)
		case models.ServiceTypeUsenet:
			groups[1].indices = append(groups[1].indices, i)
		default:
			groups[2].indices = append(groups[2].indices, i)
		}
	}

	for _, group := range groups {
		ctx := baseCtx
		ctx.RankingCriteria = group.criteria
		sort.SliceStable(group.indices, func(i, j int) bool {
			return compareByRankingCriteria(results[group.indices[i]], results[group.indices[j]], ctx) < 0
		})
	}

	overallCtx := baseCtx
	overallCtx.RankingCriteria = rankings.Default
	order := make([]int, 0, len(results))
	for len(order) < len(results) {
		bestGroup := -1
		for groupIndex, group := range groups {
			if group.position >= len(group.indices) {
				continue
			}
			if bestGroup == -1 {
				bestGroup = groupIndex
				continue
			}
			candidate := results[group.indices[group.position]]
			best := groups[bestGroup]
			bestResult := results[best.indices[best.position]]
			if compareByRankingCriteria(candidate, bestResult, overallCtx) < 0 {
				bestGroup = groupIndex
			}
		}
		if bestGroup == -1 {
			break
		}
		best := groups[bestGroup]
		order = append(order, best.indices[best.position])
		best.position++
	}
	return order
}

func sortResultsByRankingBundle(results []models.NZBResult, baseCtx ScoringContext, rankings effectiveRankingBundle) {
	if len(results) < 2 {
		return
	}
	original := append([]models.NZBResult(nil), results...)
	for position, originalIndex := range rankingOrderByBundle(original, baseCtx, rankings) {
		results[position] = original[originalIndex]
	}
}

func sortScoredResultsByRankingBundle(results []models.ScoredNZBResult, baseCtx ScoringContext, rankings effectiveRankingBundle) {
	if len(results) < 2 {
		return
	}
	plain := make([]models.NZBResult, len(results))
	for i := range results {
		plain[i] = results[i].NZBResult
	}
	original := append([]models.ScoredNZBResult(nil), results...)
	for position, originalIndex := range rankingOrderByBundle(plain, baseCtx, rankings) {
		results[position] = original[originalIndex]
	}
}

// sortResultsNewestReleaseFirst is the final ordering override for release-age
// ranking. Results without a source-supplied timestamp stay behind dated
// results, retaining their existing deterministic order.
func sortResultsNewestReleaseFirst(results []models.NZBResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if cmp := compareCountryMatch(results[i], results[j]); cmp != 0 {
			return cmp < 0
		}
		iMissing := results[i].PublishDate.IsZero()
		jMissing := results[j].PublishDate.IsZero()
		if iMissing != jMissing {
			return !iMissing
		}
		if iMissing {
			return false
		}
		return results[i].PublishDate.After(results[j].PublishDate)
	})
}

func sortScoredResultsNewestReleaseFirst(results []models.ScoredNZBResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if cmp := compareCountryMatch(results[i].NZBResult, results[j].NZBResult); cmp != 0 {
			return cmp < 0
		}
		iMissing := results[i].PublishDate.IsZero()
		jMissing := results[j].PublishDate.IsZero()
		if iMissing != jMissing {
			return !iMissing
		}
		if iMissing {
			return false
		}
		return results[i].PublishDate.After(results[j].PublishDate)
	})
}

func (s *Service) buildScoringContext(opts SearchOptions, settings config.Settings, filterSettings models.FilterSettings, animeSettings models.AnimeFilteringSettings) ScoringContext {
	rankingCriteria := s.getEffectiveRankingCriteria(opts.UserID, opts.ClientID, settings)
	return s.buildScoringContextWithCriteria(opts, settings, filterSettings, animeSettings, rankingCriteria)
}

func (s *Service) buildScoringContextWithCriteria(opts SearchOptions, settings config.Settings, filterSettings models.FilterSettings, animeSettings models.AnimeFilteringSettings, rankingCriteria []config.RankingCriterion) ScoringContext {
	preferredTerms := filter.CompileTerms(filterSettings.PreferredTerms)
	nonPreferredTerms := filter.CompileTerms(filterSettings.NonPreferredTerms)

	if opts.IsAnime && models.BoolVal(animeSettings.AnimeLanguageEnabled, false) {
		langCode := ""
		if animeSettings.AnimePreferredLanguage != nil {
			langCode = *animeSettings.AnimePreferredLanguage
		}
		if langCode == "" {
			langCode = "eng"
		}
		animePref, animeNonPref, _ := filter.GetAnimeLanguageTerms(langCode)
		if len(animePref) > 0 {
			preferredTerms = append(preferredTerms, filter.CompileTerms(animePref)...)
		}
		if len(animeNonPref) > 0 {
			nonPreferredTerms = append(nonPreferredTerms, filter.CompileTerms(animeNonPref)...)
		}
		log.Printf("[indexer] Anime language preference enabled (lang=%s): injected %d preferred + %d non-preferred terms", langCode, len(animePref), len(animeNonPref))
	}

	return ScoringContext{
		RankingCriteria:        rankingCriteria,
		ServicePriority:        config.StreamingServicePriority(models.StringVal(filterSettings.ServicePriority, string(settings.Filtering.ServicePriority))),
		PreferredTerms:         preferredTerms,
		NonPreferredTerms:      nonPreferredTerms,
		DownloadPreferredTerms: filter.CompileTerms(filterSettings.DownloadPreferredTerms),
		UseDownloadRanking:     opts.UseDownloadRanking,
		PreferredLang:          s.getEffectiveMetadataLanguage(opts.UserID, settings),
		PreferredScraper:       models.StringVal(filterSettings.PreferredScraper, settings.Filtering.PreferredScraper),
	}
}

func (s *Service) buildScoringContextForResult(opts SearchOptions, settings config.Settings, filters effectiveFilterBundle, rankings effectiveRankingBundle, animeSettings models.AnimeFilteringSettings, result models.NZBResult) ScoringContext {
	return s.buildScoringContextWithCriteria(opts, settings, filterBundleForService(filters, result.ServiceType), animeSettings, rankingBundleForService(rankings, result.ServiceType))
}

func rankingBundleForService(bundle effectiveRankingBundle, serviceType models.ContentServiceType) []config.RankingCriterion {
	switch serviceType {
	case models.ServiceTypeDebrid:
		return bundle.Debrid
	case models.ServiceTypeUsenet:
		return bundle.Usenet
	default:
		return bundle.Default
	}
}

func (s *Service) sortResultsByScore(results []models.NZBResult, scoringCtx ScoringContext) {
	if len(results) == 0 {
		return
	}

	sort.SliceStable(results, func(i, j int) bool {
		return compareByRankingCriteria(results[i], results[j], scoringCtx) < 0
	})
}

type SearchOptions struct {
	Query                 string
	Categories            []string
	MaxResults            int
	IMDBID                string
	MediaType             string                      // "movie" or "series"
	Year                  int                         // Release year (for movies)
	CountryCode           string                      // Original production country from metadata
	UserID                string                      // Optional: user ID for per-user filtering settings
	ClientID              string                      // Optional: client ID for per-client filtering settings
	TotalSeriesEpisodes   int                         // Deprecated: use EpisodeResolver instead
	EpisodeResolver       filter.EpisodeCountResolver // Optional: resolver for accurate episode counts from metadata
	AbsoluteEpisodeNumber int                         // Optional: absolute episode number for anime (e.g., 1153 for One Piece)
	IsAnime               bool                        // True for anime content - requires waiting for Nyaa scraper
	IsDaily               bool                        // True for daily shows (talk shows, news) that use date-based naming
	TargetAirDate         string                      // For daily shows: air date in YYYY-MM-DD format
	EpisodeAirYear        int                         // Year the target episode aired (for year filter tolerance)
	EpisodeReleased       bool                        // True only when metadata confirms the target episode has aired
	IncludeFiltered       bool                        // When true, return filtered results alongside passed results
	IncludeScoreBreakdown bool                        // When true, attach per-criterion scoring details (admin search tester)
	SkipFilter            bool                        // When true, skip filtering entirely (used by SearchTest)
	UseDownloadRanking    bool                        // When true, apply download-only preferred terms as a final ranking boost
}

type searchCacheKeyPayload struct {
	Mode            string                 `json:"mode"`
	Options         searchCacheOptions     `json:"options"`
	AlternateTitles []string               `json:"alternateTitles,omitempty"`
	Settings        searchRelevantSettings `json:"settings"`
	FilterSettings  models.FilterSettings
	FilterBundle    effectiveFilterBundle
	AnimeSettings   models.AnimeFilteringSettings
	FilterOverrides effectiveOverrides
	RankingSettings searchRankingSettings
	RankingCriteria []config.RankingCriterion
	RankingBundle   effectiveRankingBundle
}

type searchRelevantSettings struct {
	Indexers        []config.IndexerConfig
	TorrentScrapers []config.TorrentScraperConfig
	Streaming       searchStreamingSettings
	Metadata        searchMetadataSettings
	Usenet          []config.UsenetSettings
	DebridProviders []config.DebridProviderSettings
}

type searchStreamingSettings struct {
	ServiceMode               config.StreamingServiceMode
	SearchMode                config.SearchMode
	IndexerTimeoutSec         float64
	MaxAlternateTitleSearches int
}

type searchMetadataSettings struct {
	Language         []string
	PrimaryLanguage  string
	AllowAdultSearch bool
}

type searchRankingSettings struct {
	PreferredScraper string
	ServicePriority  config.StreamingServicePriority
}

type searchCacheOptions struct {
	Query                 string
	Categories            []string
	MaxResults            int
	IMDBID                string
	MediaType             string
	Year                  int
	CountryCode           string
	UserID                string
	TotalSeriesEpisodes   int
	HasEpisodeResolver    bool
	AbsoluteEpisodeNumber int
	IsAnime               bool
	IsDaily               bool
	TargetAirDate         string
	EpisodeAirYear        int
	EpisodeReleased       bool
	IncludeFiltered       bool
	SkipFilter            bool
	UseDownloadRanking    bool
}

func buildSearchCacheOptions(opts SearchOptions) searchCacheOptions {
	return searchCacheOptions{
		Query:                 opts.Query,
		Categories:            append([]string(nil), opts.Categories...),
		MaxResults:            opts.MaxResults,
		IMDBID:                opts.IMDBID,
		MediaType:             opts.MediaType,
		Year:                  opts.Year,
		CountryCode:           opts.CountryCode,
		UserID:                opts.UserID,
		TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
		HasEpisodeResolver:    opts.EpisodeResolver != nil,
		AbsoluteEpisodeNumber: opts.AbsoluteEpisodeNumber,
		IsAnime:               opts.IsAnime,
		IsDaily:               opts.IsDaily,
		TargetAirDate:         opts.TargetAirDate,
		EpisodeAirYear:        opts.EpisodeAirYear,
		EpisodeReleased:       opts.EpisodeReleased,
		IncludeFiltered:       opts.IncludeFiltered,
		SkipFilter:            opts.SkipFilter,
		UseDownloadRanking:    opts.UseDownloadRanking,
	}
}

func buildSearchRelevantSettings(settings config.Settings) searchRelevantSettings {
	return searchRelevantSettings{
		Indexers:        append([]config.IndexerConfig(nil), settings.Indexers...),
		TorrentScrapers: append([]config.TorrentScraperConfig(nil), settings.TorrentScrapers...),
		Streaming: searchStreamingSettings{
			ServiceMode:               settings.Streaming.ServiceMode,
			SearchMode:                settings.Streaming.SearchMode,
			IndexerTimeoutSec:         settings.Streaming.IndexerTimeoutSec,
			MaxAlternateTitleSearches: settings.Streaming.MaxAlternateTitleSearches,
		},
		Metadata: searchMetadataSettings{
			Language:         append([]string(nil), settings.Metadata.Language...),
			PrimaryLanguage:  settings.Metadata.PrimaryLanguage,
			AllowAdultSearch: settings.Metadata.AllowAdultSearch,
		},
		Usenet:          append([]config.UsenetSettings(nil), settings.Usenet...),
		DebridProviders: append([]config.DebridProviderSettings(nil), settings.Streaming.DebridProviders...),
	}
}

func buildSearchRankingSettings(settings config.Settings) searchRankingSettings {
	return searchRankingSettings{
		PreferredScraper: settings.Filtering.PreferredScraper,
		ServicePriority:  settings.Filtering.ServicePriority,
	}
}

func (s *Service) searchCacheKey(mode string, opts SearchOptions, settings config.Settings, alternateTitles []string, filterSettings models.FilterSettings, filterBundle effectiveFilterBundle, animeSettings models.AnimeFilteringSettings, filterOverrides effectiveOverrides, rankingCriteria []config.RankingCriterion, rankingBundle effectiveRankingBundle) string {
	payload := searchCacheKeyPayload{
		Mode:            mode,
		Options:         buildSearchCacheOptions(opts),
		AlternateTitles: append([]string(nil), alternateTitles...),
		Settings:        buildSearchRelevantSettings(settings),
		FilterSettings:  filterSettings,
		FilterBundle:    filterBundle,
		AnimeSettings:   animeSettings,
		FilterOverrides: filterOverrides,
		RankingSettings: buildSearchRankingSettings(settings),
		RankingCriteria: append([]config.RankingCriterion(nil), rankingCriteria...),
		RankingBundle:   rankingBundle,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Service) getCachedSearchResults(key string, now time.Time) ([]models.NZBResult, bool) {
	if s == nil || key == "" {
		return nil, false
	}
	s.searchCacheMu.RLock()
	entry, ok := s.searchCache[key]
	s.searchCacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		s.searchCacheMu.Lock()
		if current, ok := s.searchCache[key]; ok && !now.Before(current.expiresAt) {
			delete(s.searchCache, key)
		}
		s.searchCacheMu.Unlock()
		return nil, false
	}
	return cloneNZBResults(entry.results), true
}

func (s *Service) setCachedSearchResults(key string, results []models.NZBResult, now time.Time) {
	if s == nil || key == "" {
		return
	}
	s.searchCacheMu.Lock()
	defer s.searchCacheMu.Unlock()
	if s.searchCache == nil {
		s.searchCache = make(map[string]searchCacheEntry)
	}
	s.pruneExpiredSearchCacheLocked(now)
	if len(s.searchCache) >= searchResultsCacheMaxEntries {
		s.pruneOldestSearchCacheEntryLocked()
	}
	s.searchCache[key] = searchCacheEntry{
		results:   cloneNZBResults(results),
		expiresAt: now.Add(searchResultsCacheTTL),
	}
}

func (s *Service) pruneExpiredSearchCacheLocked(now time.Time) {
	for key, entry := range s.searchCache {
		if !now.Before(entry.expiresAt) {
			delete(s.searchCache, key)
		}
	}
}

func (s *Service) pruneOldestSearchCacheEntryLocked() {
	var oldestKey string
	var oldestExpiresAt time.Time
	for key, entry := range s.searchCache {
		if oldestKey == "" || entry.expiresAt.Before(oldestExpiresAt) {
			oldestKey = key
			oldestExpiresAt = entry.expiresAt
		}
	}
	if oldestKey != "" {
		delete(s.searchCache, oldestKey)
	}
}

func cloneNZBResults(results []models.NZBResult) []models.NZBResult {
	if len(results) == 0 {
		return nil
	}
	out := make([]models.NZBResult, len(results))
	copy(out, results)
	for i := range out {
		if results[i].Categories != nil {
			out[i].Categories = append([]string(nil), results[i].Categories...)
		}
		if results[i].Attributes != nil {
			out[i].Attributes = make(map[string]string, len(results[i].Attributes))
			for k, v := range results[i].Attributes {
				out[i].Attributes[k] = v
			}
		}
	}
	return out
}

func (s *Service) Search(ctx context.Context, opts SearchOptions) ([]models.NZBResult, error) {
	searchStart := time.Now()
	callNum := s.searchCount.Add(1)
	log.Printf("[search-stats] Search #%d started (query=%q, mediaType=%q, user=%q, client=%q)",
		callNum, opts.Query, opts.MediaType, opts.UserID, opts.ClientID)
	log.Printf("[indexer] TIMING: Search started for query=%q mediaType=%q", opts.Query, opts.MediaType)

	if s.cfg == nil {
		return nil, errors.New("config manager not configured")
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	settings = config.FilterSettingsForProfile(settings, opts.UserID)

	// Get effective filtering settings (cascade: global -> profile -> client)
	filterBundle, animeSettings, filterOverrides := s.getEffectiveFilterBundle(opts.UserID, opts.ClientID, settings)
	filterSettings := filterBundle.Default

	// Inject anime language filter-out terms early (before search/filter calls)
	if opts.IsAnime && models.BoolVal(animeSettings.AnimeLanguageEnabled, false) {
		langCode := ""
		if animeSettings.AnimePreferredLanguage != nil {
			langCode = *animeSettings.AnimePreferredLanguage
		}
		if langCode == "" {
			langCode = "eng"
		}
		_, _, animeFilterOut := filter.GetAnimeLanguageTerms(langCode)
		if len(animeFilterOut) > 0 {
			filterSettings.FilterOutTerms = append(filterSettings.FilterOutTerms, animeFilterOut...)
			filterBundle.Default.FilterOutTerms = append(filterBundle.Default.FilterOutTerms, animeFilterOut...)
			filterBundle.Debrid.FilterOutTerms = append(filterBundle.Debrid.FilterOutTerms, animeFilterOut...)
			filterBundle.Usenet.FilterOutTerms = append(filterBundle.Usenet.FilterOutTerms, animeFilterOut...)
			log.Printf("[indexer] Anime language filter-out: injected %d terms for lang=%s", len(animeFilterOut), langCode)
		}
	}

	includeUsenet := shouldUseUsenet(settings.Streaming.ServiceMode)
	includeDebrid := shouldUseDebrid(settings.Streaming.ServiceMode)

	alternateTitles := s.resolveAlternateTitles(ctx, opts, s.getEffectiveMetadataLanguage(opts.UserID, settings), settings.Streaming.MaxAlternateTitleSearches)
	if len(alternateTitles) > 0 {
		log.Printf("[indexer] resolved %d alternate title(s) for %q: %v", len(alternateTitles), opts.Query, alternateTitles)
	}

	parsedQuery := debrid.ParseQuery(opts.Query)
	searchQueries := buildSearchQueries(opts, parsedQuery, alternateTitles)
	rankingBundle := s.getEffectiveRankingBundle(opts.UserID, opts.ClientID, settings)
	rankingCriteria := rankingBundle.Default
	cacheKey := s.searchCacheKey("ranked", opts, settings, alternateTitles, filterSettings, filterBundle, animeSettings, filterOverrides, rankingCriteria, rankingBundle)
	if cached, ok := s.getCachedSearchResults(cacheKey, searchStart); ok {
		log.Printf("[indexer] search cache hit for query=%q mediaType=%q user=%q client=%q results=%d", opts.Query, opts.MediaType, opts.UserID, opts.ClientID, len(cached))
		log.Printf("[search-stats] Search #%d cache hit: %d results in %v (totals: search=%d, splitSearch=%d, usenetAPICalls=%d)",
			callNum, len(cached), time.Since(searchStart),
			s.searchCount.Load(), s.searchSplitCount.Load(), s.usenetAPICallCount.Load())
		return cached, nil
	}
	sourceOpts := opts
	if rankingBundle.NewestReleaseFirst {
		// Source-level caps can discard newer releases before the final
		// cross-source ordering override has a chance to see them.
		sourceOpts.MaxResults = 0
	}

	// Run usenet and debrid searches in parallel for faster results
	type searchResult struct {
		results []models.NZBResult
		err     error
		source  string
	}

	var wg sync.WaitGroup
	resultsChan := make(chan searchResult, 2)

	// Launch usenet search
	if includeUsenet {
		wg.Add(1)
		go func() {
			defer wg.Done()
			usenetStart := time.Now()
			usenetResults, err := s.searchUsenetWithFilter(ctx, settings, sourceOpts, parsedQuery, alternateTitles, searchQueries, filterBundle.Usenet)
			log.Printf("[indexer] TIMING: usenet search complete (took: %v, results: %d)", time.Since(usenetStart), len(usenetResults))
			if err != nil {
				resultsChan <- searchResult{err: err, source: "usenet"}
				return
			}
			for i := range usenetResults {
				if usenetResults[i].ServiceType == models.ServiceTypeUnknown {
					usenetResults[i].ServiceType = models.ServiceTypeUsenet
				}
			}
			resultsChan <- searchResult{results: usenetResults, source: "usenet"}
		}()
	}

	// Launch debrid search
	if includeDebrid {
		wg.Add(1)
		go func() {
			defer wg.Done()
			debridStart := time.Now()
			if s.debrid == nil {
				resultsChan <- searchResult{err: fmt.Errorf("debrid search service not configured"), source: "debrid"}
				return
			}
			hasResolver := opts.EpisodeResolver != nil
			log.Printf("[indexer] TIMING: debrid search starting (query=%q, hasEpisodeResolver=%v)", opts.Query, hasResolver)
			debOpts := debrid.SearchOptions{
				Query:                 opts.Query,
				Categories:            append([]string{}, opts.Categories...),
				MaxResults:            sourceOpts.MaxResults,
				IMDBID:                opts.IMDBID,
				MediaType:             opts.MediaType,
				Year:                  opts.Year,
				AlternateTitles:       append([]string{}, alternateTitles...),
				UserID:                opts.UserID,
				ClientID:              opts.ClientID,
				TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
				EpisodeResolver:       opts.EpisodeResolver,
				AbsoluteEpisodeNumber: opts.AbsoluteEpisodeNumber,
				IsAnime:               opts.IsAnime,
				IsDaily:               opts.IsDaily,
				TargetAirDate:         opts.TargetAirDate,
				EpisodeAirYear:        opts.EpisodeAirYear,
				EpisodeReleased:       opts.EpisodeReleased,
				SkipFilter:            opts.SkipFilter,
			}
			debridResults, err := s.debrid.Search(ctx, debOpts)
			log.Printf("[indexer] TIMING: debrid search complete (took: %v, results: %d)", time.Since(debridStart), len(debridResults))
			if err != nil {
				resultsChan <- searchResult{err: err, source: "debrid"}
				return
			}
			for i := range debridResults {
				if debridResults[i].ServiceType == models.ServiceTypeUnknown {
					debridResults[i].ServiceType = models.ServiceTypeDebrid
				}
			}
			resultsChan <- searchResult{results: debridResults, source: "debrid"}
		}()
	}

	// Wait for all searches to complete, then close channel
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results from both searches
	var aggregated []models.NZBResult
	var lastErr error

	for sr := range resultsChan {
		if sr.err != nil {
			log.Printf("[indexer] %s search failed: %v", sr.source, sr.err)
			lastErr = sr.err
			continue
		}
		if len(sr.results) > 0 {
			aggregated = append(aggregated, sr.results...)
		}
	}

	if len(aggregated) == 0 && lastErr != nil {
		return nil, lastErr
	}

	expectedYear := opts.Year
	if expectedYear == 0 {
		expectedYear = parsedQuery.Year
	}
	applyEpisodeYearPriority(aggregated, expectedYear, opts.EpisodeAirYear)

	// Check if ranking should be bypassed for AIOStreams-only mode
	// Only bypass when: setting is enabled, AIOStreams is the only scraper, and no usenet results are mixed in
	bypassRanking := shouldBypassAIOStreamsRanking(settings, filterOverrides, includeUsenet)

	var scoringCtx *ScoringContext
	if rankingBundle.NewestReleaseFirst {
		log.Printf("[indexer] Sorting %d results by newest release first; all ranking criteria are ignored", len(aggregated))
		sortResultsNewestReleaseFirst(aggregated)
	} else if bypassRanking {
		log.Printf("[indexer] Bypassing mediastorm ranking - AIOStreams is the only enabled scraper and bypass setting is enabled")
	} else {
		ctx := s.buildScoringContextWithCriteria(opts, settings, filterSettings, animeSettings, rankingBundle.Default)
		scoringCtx = &ctx
		log.Printf("[indexer] Ranking %d results per service, then merging with %d overall criteria, ServicePriority=%q, downloadRanking=%v", len(aggregated), len(scoringCtx.RankingCriteria), scoringCtx.ServicePriority, opts.UseDownloadRanking)
		sortResultsByRankingBundle(aggregated, *scoringCtx, rankingBundle)
	}

	// Debug: log all results after sorting
	for idx := 0; idx < len(aggregated); idx++ {
		res := extractResolutionFromResult(aggregated[idx])
		if scoringCtx != nil {
			ctx := s.buildScoringContextForResult(opts, settings, filterBundle, rankingBundle, animeSettings, aggregated[idx])
			score, _ := ScoreResult(aggregated[idx], ctx)
			log.Printf("[indexer] Result #%d: Score=%d ServiceType=%q Resolution=%d Size=%d Title=%q", idx, score, aggregated[idx].ServiceType, res, aggregated[idx].SizeBytes, aggregated[idx].Title)
		} else {
			log.Printf("[indexer] Result #%d: Score=n/a ServiceType=%q Resolution=%d Size=%d Title=%q", idx, aggregated[idx].ServiceType, res, aggregated[idx].SizeBytes, aggregated[idx].Title)
		}
	}

	// Apply per-resolution limit before global MaxResults truncation
	maxPerRes := models.IntVal(filterOverrides.MaxResultsPerResolution, 0)
	if maxPerRes > 0 {
		resolutionCounts := map[int]int{}
		var limited []models.NZBResult
		for _, r := range aggregated {
			res := extractResolutionFromResult(r)
			if resolutionCounts[res] < maxPerRes {
				limited = append(limited, r)
				resolutionCounts[res]++
			}
		}
		log.Printf("[indexer] Per-resolution limit=%d applied: %d -> %d results", maxPerRes, len(aggregated), len(limited))
		aggregated = limited
	}

	if opts.MaxResults > 0 && len(aggregated) > opts.MaxResults {
		aggregated = aggregated[:opts.MaxResults]
	}

	// Add daily show attributes to all results for file matching
	if opts.IsDaily || opts.TargetAirDate != "" {
		for i := range aggregated {
			if aggregated[i].Attributes == nil {
				aggregated[i].Attributes = make(map[string]string)
			}
			if opts.IsDaily {
				aggregated[i].Attributes["isDaily"] = "true"
			}
			if opts.TargetAirDate != "" {
				aggregated[i].Attributes["targetAirDate"] = opts.TargetAirDate
			}
		}
		log.Printf("[indexer] Added daily show attributes to %d results: isDaily=%v, airDate=%q", len(aggregated), opts.IsDaily, opts.TargetAirDate)
	}

	log.Printf("[indexer] TIMING: Search complete, returning %d results (TOTAL: %v)", len(aggregated), time.Since(searchStart))
	log.Printf("[search-stats] Search #%d complete: %d results in %v (totals: search=%d, splitSearch=%d, usenetAPICalls=%d)",
		callNum, len(aggregated), time.Since(searchStart),
		s.searchCount.Load(), s.searchSplitCount.Load(), s.usenetAPICallCount.Load())
	s.setCachedSearchResults(cacheKey, aggregated, time.Now())
	return aggregated, nil
}

// SearchWithScoring runs Search and wraps results as ScoredNZBResult with filter status.
// When opts.IncludeFiltered is true, filtered results are appended after passed results.
func (s *Service) SearchWithScoring(ctx context.Context, opts SearchOptions) ([]models.ScoredNZBResult, error) {
	if !opts.IncludeFiltered {
		// Standard path: just wrap passed results
		results, err := s.Search(ctx, opts)
		if err != nil {
			return nil, err
		}
		scored := make([]models.ScoredNZBResult, len(results))
		for i, r := range results {
			scored[i] = models.ScoredNZBResult{
				NZBResult:    r,
				FilterStatus: "passed",
			}
		}
		return scored, nil
	}

	// IncludeFiltered path: collect raw results, filter with details, then wrap
	rawOpts := opts
	rawOpts.SkipFilter = true
	rawOpts.IncludeFiltered = false
	// MaxResults is a final presentation/resolution cap. Passing it into raw
	// source fetches can truncate one source before cross-source ranking runs,
	// which makes Details/admin/prequeue disagree on the top candidates.
	rawOpts.MaxResults = 0
	rawResults, err := s.searchRawResults(ctx, rawOpts)
	if err != nil {
		return nil, err
	}

	// Build filter options from effective settings
	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	settings = config.FilterSettingsForProfile(settings, opts.UserID)

	filterBundle, animeSettings, filterOverrides := s.getEffectiveFilterBundle(opts.UserID, opts.ClientID, settings)
	filterSettings := filterBundle.Default
	rankingBundle := s.getEffectiveRankingBundle(opts.UserID, opts.ClientID, settings)
	if shouldBypassAIOStreamsRanking(settings, filterOverrides, shouldUseUsenet(settings.Streaming.ServiceMode)) {
		log.Printf("[indexer] Bypassing mediastorm filtering/ranking - AIOStreams is the only enabled scraper and bypass setting is enabled")
		scored := make([]models.ScoredNZBResult, len(rawResults))
		for i, r := range rawResults {
			scored[i] = models.ScoredNZBResult{
				NZBResult:    markRankingBypassed(r),
				FilterStatus: "passed",
			}
		}
		if rankingBundle.NewestReleaseFirst {
			sortScoredResultsNewestReleaseFirst(scored)
		}
		return capScoredResults(scored, opts.MaxResults), nil
	}
	if opts.IsAnime && models.BoolVal(animeSettings.AnimeLanguageEnabled, false) {
		langCode := ""
		if animeSettings.AnimePreferredLanguage != nil {
			langCode = *animeSettings.AnimePreferredLanguage
		}
		if langCode == "" {
			langCode = "eng"
		}
		_, _, animeFilterOut := filter.GetAnimeLanguageTerms(langCode)
		if len(animeFilterOut) > 0 {
			filterSettings.FilterOutTerms = append(filterSettings.FilterOutTerms, animeFilterOut...)
			filterBundle.Default.FilterOutTerms = append(filterBundle.Default.FilterOutTerms, animeFilterOut...)
			filterBundle.Debrid.FilterOutTerms = append(filterBundle.Debrid.FilterOutTerms, animeFilterOut...)
			filterBundle.Usenet.FilterOutTerms = append(filterBundle.Usenet.FilterOutTerms, animeFilterOut...)
		}
	}
	filterOptsByService := map[models.ContentServiceType]filter.Options{
		models.ServiceTypeDebrid:  s.buildFilterOptions(rawOpts, filterBundle.Debrid),
		models.ServiceTypeUsenet:  s.buildFilterOptions(rawOpts, filterBundle.Usenet),
		models.ServiceTypeUnknown: s.buildFilterOptions(rawOpts, filterBundle.Default),
	}
	detailed := make([]filter.FilteredResult, 0, len(rawResults))
	for _, raw := range rawResults {
		filterOpts, ok := filterOptsByService[raw.ServiceType]
		if !ok {
			filterOpts = filterOptsByService[models.ServiceTypeUnknown]
		}
		resultDetails := filter.ResultsWithDetails([]models.NZBResult{raw}, filterOpts)
		if len(resultDetails) > 0 {
			detailed = append(detailed, resultDetails[0])
		}
	}
	passedResults := make([]models.NZBResult, 0, len(detailed))
	for _, result := range detailed {
		if result.Passed {
			passedResults = append(passedResults, result.Result)
		}
	}
	expectedYear := rawOpts.Year
	if expectedYear == 0 {
		expectedYear = debrid.ParseQuery(rawOpts.Query).Year
	}
	applyEpisodeYearPriority(passedResults, expectedYear, rawOpts.EpisodeAirYear)
	passedIndex := 0
	for i := range detailed {
		if detailed[i].Passed {
			detailed[i].Result = passedResults[passedIndex]
			passedIndex++
		}
	}
	scoringCtx := s.buildScoringContextWithCriteria(opts, settings, filterSettings, animeSettings, rankingBundle.Default)

	// Separate passed and filtered
	var passed, filtered []models.ScoredNZBResult
	for _, fr := range detailed {
		resultCtx := s.buildScoringContextForResult(opts, settings, filterBundle, rankingBundle, animeSettings, fr.Result)
		score, breakdown := ScoreResult(fr.Result, resultCtx)
		sr := models.ScoredNZBResult{
			NZBResult:  fr.Result,
			TotalScore: score,
		}
		if opts.IncludeScoreBreakdown {
			sr.ScoreBreakdown = breakdown
		}
		if fr.Passed {
			sr.FilterStatus = "passed"
			passed = append(passed, sr)
		} else {
			sr.FilterStatus = "filtered"
			sr.FilterReason = fr.RejectReason
			filtered = append(filtered, sr)
		}
	}

	// Sort passed results by the same priority order used by standard ranking.
	if len(passed) > 0 {
		if rankingBundle.NewestReleaseFirst {
			sortScoredResultsNewestReleaseFirst(passed)
		} else {
			sortScoredResultsByRankingBundle(passed, scoringCtx, rankingBundle)
		}

		// Apply per-resolution limit to passed results (same as Search path)
		maxPerRes := models.IntVal(filterOverrides.MaxResultsPerResolution, 0)
		if maxPerRes > 0 {
			resolutionCounts := map[int]int{}
			var limited []models.ScoredNZBResult
			for _, r := range passed {
				res := extractResolutionFromResult(r.NZBResult)
				if resolutionCounts[res] < maxPerRes {
					limited = append(limited, r)
					resolutionCounts[res]++
				}
			}
			log.Printf("[indexer] Per-resolution limit=%d applied to passed results: %d -> %d", maxPerRes, len(passed), len(limited))
			passed = limited
		}
	}
	if rankingBundle.NewestReleaseFirst {
		sortScoredResultsNewestReleaseFirst(filtered)
	}

	// Combine: passed first, then filtered. Cap after ranking so MaxResults is a
	// presentation limit, not a per-source fetch limit.
	result := append(passed, filtered...)
	return capScoredResults(result, opts.MaxResults), nil
}

func capScoredResults(results []models.ScoredNZBResult, max int) []models.ScoredNZBResult {
	if max <= 0 || len(results) <= max {
		return results
	}
	log.Printf("[indexer] MaxResults=%d applied after ranking: %d -> %d", max, len(results), max)
	return results[:max]
}

// ScoredSplitSearchResult is one split search source's completed output. Each
// channel is closed exactly once, once its source settles; an enabled source
// sends exactly one value (Scored on success, Err on failure) before the close,
// and a source outside the active service mode sends one value with
// Disabled=true (so callers never wait on a source that will never run).
//
// Scored carries the source's passed-only, ranked candidates — the same shape
// SearchWithScoring would produce for the combined set, restricted to this
// source — so a caller can start resolving them while other sources are still
// in flight. RawCount/FilteredCount are diagnostics (raw fetched/rejected).
type ScoredSplitSearchResult struct {
	Source        string
	Scored        []models.ScoredNZBResult
	RawCount      int
	FilteredCount int
	Err           error
	Disabled      bool
}

// searchSplitOutcome is the internal per-source completion record used by
// SearchWithScoringSplit both to emit scored results to the caller and to
// reconstruct the aggregated raw set for the shared "raw" search cache.
type searchSplitOutcome struct {
	source     string
	scored     []models.ScoredNZBResult
	raw        []models.NZBResult
	incomplete bool
	filtered   int
	err        error
	disabled   bool
}

// SearchWithScoringSplit runs the usenet and debrid searches concurrently and
// emits each source's filtered+ranked passed candidates as soon as THAT source
// completes, instead of waiting for both (Search/searchRawResults wait for all
// sources via wg.Wait). A usenet-prioritized install can start resolving its
// usenet candidates while debrid scrapers are still in flight.
//
// Each returned channel receives exactly one ScoredSplitSearchResult and is then
// closed; callers must drain both (a disabled source reports Disabled=true so
// both channels always settle). Within-source ranking/merge semantics and the
// search-cache write behavior match the non-split paths: per-source filters,
// per-source scoring/ranking, and a single aggregate "raw" cache write once
// both sources settle (only when neither failed and no source was incomplete).
func (s *Service) SearchWithScoringSplit(ctx context.Context, opts SearchOptions) (usenetCh, debridCh <-chan ScoredSplitSearchResult) {
	callNum := s.searchSplitCount.Add(1)
	searchStart := time.Now()
	log.Printf("[search-stats] SearchSplit #%d started (query=%q, mediaType=%q, user=%q, client=%q)",
		callNum, opts.Query, opts.MediaType, opts.UserID, opts.ClientID)
	log.Printf("[indexer] TIMING: SearchSplit started for query=%q mediaType=%q", opts.Query, opts.MediaType)

	usenetOut := make(chan ScoredSplitSearchResult, 1)
	debridOut := make(chan ScoredSplitSearchResult, 1)
	if s.cfg == nil {
		cfgErr := errors.New("config manager not configured")
		usenetOut <- ScoredSplitSearchResult{Source: "usenet", Err: cfgErr}
		debridOut <- ScoredSplitSearchResult{Source: "debrid", Err: cfgErr}
		close(usenetOut)
		close(debridOut)
		return usenetOut, debridOut
	}

	settings, err := s.cfg.Load()
	if err != nil {
		usenetOut <- ScoredSplitSearchResult{Source: "usenet", Err: fmt.Errorf("load settings: %w", err)}
		debridOut <- ScoredSplitSearchResult{Source: "debrid", Err: fmt.Errorf("load settings: %w", err)}
		close(usenetOut)
		close(debridOut)
		return usenetOut, debridOut
	}
	settings = config.FilterSettingsForProfile(settings, opts.UserID)

	includeUsenet := shouldUseUsenet(settings.Streaming.ServiceMode)
	includeDebrid := shouldUseDebrid(settings.Streaming.ServiceMode)

	filterBundle, animeSettings, filterOverrides := s.getEffectiveFilterBundle(opts.UserID, opts.ClientID, settings)
	filterSettings := filterBundle.Default

	// Inject anime language filter-out terms early (before search/filter calls),
	// mirroring Search/SearchWithScoring.
	if opts.IsAnime && models.BoolVal(animeSettings.AnimeLanguageEnabled, false) {
		langCode := ""
		if animeSettings.AnimePreferredLanguage != nil {
			langCode = *animeSettings.AnimePreferredLanguage
		}
		if langCode == "" {
			langCode = "eng"
		}
		_, _, animeFilterOut := filter.GetAnimeLanguageTerms(langCode)
		if len(animeFilterOut) > 0 {
			filterBundle.Default.FilterOutTerms = append(filterBundle.Default.FilterOutTerms, animeFilterOut...)
			filterBundle.Debrid.FilterOutTerms = append(filterBundle.Debrid.FilterOutTerms, animeFilterOut...)
			filterBundle.Usenet.FilterOutTerms = append(filterBundle.Usenet.FilterOutTerms, animeFilterOut...)
			log.Printf("[indexer] Anime language filter-out: injected %d terms for lang=%s", len(animeFilterOut), langCode)
		}
	}

	alternateTitles := s.resolveAlternateTitles(ctx, opts, s.getEffectiveMetadataLanguage(opts.UserID, settings), settings.Streaming.MaxAlternateTitleSearches)
	if len(alternateTitles) > 0 {
		log.Printf("[indexer] resolved %d alternate title(s) for %q: %v", len(alternateTitles), opts.Query, alternateTitles)
	}

	parsedQuery := debrid.ParseQuery(opts.Query)
	searchQueries := buildSearchQueries(opts, parsedQuery, alternateTitles)
	rankingBundle := s.getEffectiveRankingBundle(opts.UserID, opts.ClientID, settings)
	rankingCriteria := rankingBundle.Default

	// Check the shared raw cache once so a warm search never re-fetches or waits
	// on a slow scraper (same key searchRawResults would use).
	cacheKey := s.searchCacheKey("raw", opts, settings, alternateTitles, filterSettings, filterBundle, animeSettings, filterOverrides, rankingCriteria, rankingBundle)
	if cached, ok := s.getCachedSearchResults(cacheKey, searchStart); ok {
		log.Printf("[indexer] raw search cache hit for query=%q mediaType=%q user=%q client=%q results=%d", opts.Query, opts.MediaType, opts.UserID, opts.ClientID, len(cached))
		// A cache hit carries only the merged aggregate, so partition it by the
		// ServiceType the fetchers stamped and emit each partition as its own
		// source batch (both are immediately available).
		bySource := partitionResultsBySource(cached)
		for _, out := range bySource {
			// A cache hit stores raw results only, so each partitioned source must
			// be filtered/scored/ranked exactly like a freshly-fetched source
			// before it is emitted — otherwise the caller sees zero passed
			// candidates on a warm cache.
			filterOpts := s.buildFilterOptions(opts, filterBundle.Usenet)
			if out.source == "debrid" {
				filterOpts = s.buildFilterOptions(opts, filterBundle.Debrid)
			}
			out.scored, out.filtered = s.scoreSourceCandidates(opts, settings, out.raw, filterOpts, filterBundle, animeSettings, filterOverrides, rankingBundle)
			s.emitSplitSourceBatch(usenetOut, debridOut, settings, opts, out)
		}
		close(usenetOut)
		close(debridOut)
		return usenetOut, debridOut
	}

	sourceOpts := opts
	sourceOpts.MaxResults = 0 // ranking is final-order; source caps would truncate before it

	resultsCh := make(chan searchSplitOutcome, 2)

	// Launch usenet source
	if includeUsenet {
		go func() {
			out := s.splitSearchUsenet(ctx, settings, sourceOpts, parsedQuery, alternateTitles, searchQueries, filterBundle, animeSettings, filterOverrides, rankingBundle)
			resultsCh <- out
		}()
	} else {
		resultsCh <- searchSplitOutcome{source: "usenet", disabled: true}
	}

	// Launch debrid source
	if includeDebrid {
		go func() {
			out := s.splitSearchDebrid(ctx, settings, sourceOpts, opts, alternateTitles, filterBundle, animeSettings, filterOverrides, rankingBundle)
			resultsCh <- out
		}()
	} else {
		resultsCh <- searchSplitOutcome{source: "debrid", disabled: true}
	}

	// Drain the two source outcomes AS EACH completes, emitting that source's
	// scored candidates to the caller immediately — the whole point of the split
	// is that the first-ready source (typically usenet) is published while the
	// other source is still in flight, so the caller can start resolving it. This
	// runs in the background so SearchWithScoringSplit returns the channels
	// right away instead of blocking on the slowest source. The raw aggregate is
	// accumulated for the shared "raw" search cache, written once BOTH sources
	// settle.
	go func() {
		errCount := 0
		incompleteSearch := false
		var aggregated []models.NZBResult
		pending := 2
		for pending > 0 {
			select {
			case <-ctx.Done():
				// The caller stopped consuming (e.g. a winner was adopted before
				// the slow source finished). Leave the channels open — there is no
				// reader — and let the in-flight source goroutines finish or abort
				// on this context; the cache is deliberately not written.
				return
			case out := <-resultsCh:
				pending--
				if out.err != nil {
					log.Printf("[indexer] %s search failed: %v", out.source, out.err)
					errCount++
					// Emit the failure so the caller can count this source as an
					// enabled source that failed (vs. a disabled/absent source).
					s.emitSplitSourceBatch(usenetOut, debridOut, settings, opts, out)
					continue
				}
				if out.disabled {
					// Not part of the active service mode: emit Disabled=true so
					// the caller can treat every channel as carrying one value.
					s.emitSplitSourceBatch(usenetOut, debridOut, settings, opts, out)
					continue
				}
				if len(out.raw) > 0 {
					if out.incomplete {
						incompleteSearch = true
					}
					aggregated = append(aggregated, out.raw...)
				}
				s.emitSplitSourceBatch(usenetOut, debridOut, settings, opts, out)
			}
		}

		// Preserve searchRawResults' cache write semantics: write the aggregate
		// only when every source succeeded and none reported an incomplete search.
		if errCount == 0 && !incompleteSearch {
			s.setCachedSearchResults(cacheKey, aggregated, time.Now())
		} else {
			log.Printf("[indexer] skipping raw search cache for query=%q because one or more providers failed", opts.Query)
		}
		close(usenetOut)
		close(debridOut)

		log.Printf("[search-stats] SearchSplit #%d complete: %d results in %v (totals: search=%d, splitSearch=%d, usenetAPICalls=%d)",
			callNum, len(aggregated), time.Since(searchStart),
			s.searchCount.Load(), s.searchSplitCount.Load(), s.usenetAPICallCount.Load())
	}()

	return usenetOut, debridOut
}

// emitSplitSourceBatch routes one completed source's scored batch to the
// appropriate caller-facing channel (usenet vs debrid).
func (s *Service) emitSplitSourceBatch(usenetOut, debridOut chan ScoredSplitSearchResult, settings config.Settings, opts SearchOptions, out searchSplitOutcome) {
	sr := ScoredSplitSearchResult{
		Source:        out.source,
		Scored:        out.scored,
		RawCount:      len(out.raw),
		FilteredCount: out.filtered,
		Err:           out.err,
		Disabled:      out.disabled,
	}
	if out.source == "debrid" {
		debridOut <- sr
	} else {
		usenetOut <- sr
	}
}

// partitionResultsBySource splits a cached raw aggregate into per-source batches
// by the ServiceType the fetchers stamped onto each result.
func partitionResultsBySource(raw []models.NZBResult) []searchSplitOutcome {
	var usenet, debrid []models.NZBResult
	for _, r := range raw {
		switch r.ServiceType {
		case models.ServiceTypeDebrid:
			debrid = append(debrid, r)
		default:
			usenet = append(usenet, r)
		}
	}
	var out []searchSplitOutcome
	if len(usenet) > 0 {
		out = append(out, searchSplitOutcome{source: "usenet", raw: usenet})
	}
	if len(debrid) > 0 {
		out = append(out, searchSplitOutcome{source: "debrid", raw: debrid})
	}
	return out
}

// splitSearchUsenet fetches and scores the usenet source for the split search.
func (s *Service) splitSearchUsenet(ctx context.Context, settings config.Settings, opts SearchOptions, parsedQuery debrid.ParsedQuery, alternateTitles, searchQueries []string, filterBundle effectiveFilterBundle, animeSettings models.AnimeFilteringSettings, filterOverrides effectiveOverrides, rankingBundle effectiveRankingBundle) searchSplitOutcome {
	usenetStart := time.Now()
	log.Printf("[indexer] TIMING: split usenet search starting (query=%q)", opts.Query)
	out := searchSplitOutcome{source: "usenet"}

	raw, err := s.fetchUsenetResultsAllQueries(ctx, settings, opts, searchQueries)
	if err != nil {
		out.err = err
		return out
	}
	incomplete := false
	for i := range raw {
		if raw[i].ServiceType == models.ServiceTypeUnknown {
			raw[i].ServiceType = models.ServiceTypeUsenet
		}
		if raw[i].Attributes != nil && strings.EqualFold(strings.TrimSpace(raw[i].Attributes["searchIncomplete"]), "true") {
			incomplete = true
		}
	}
	out.raw = raw
	out.incomplete = incomplete
	out.scored, out.filtered = s.scoreSourceCandidates(opts, settings, raw, s.buildFilterOptions(opts, filterBundle.Usenet), filterBundle, animeSettings, filterOverrides, rankingBundle)
	log.Printf("[indexer] TIMING: split usenet search complete (took: %v, raw=%d, passed=%d)", time.Since(usenetStart), len(raw), len(out.scored))
	return out
}

// splitSearchDebrid fetches and scores the debrid source for the split search.
func (s *Service) splitSearchDebrid(ctx context.Context, settings config.Settings, opts SearchOptions, rawOpts SearchOptions, alternateTitles []string, filterBundle effectiveFilterBundle, animeSettings models.AnimeFilteringSettings, filterOverrides effectiveOverrides, rankingBundle effectiveRankingBundle) searchSplitOutcome {
	debridStart := time.Now()
	log.Printf("[indexer] TIMING: split debrid search starting (query=%q)", opts.Query)
	out := searchSplitOutcome{source: "debrid"}
	if s.debrid == nil {
		out.err = fmt.Errorf("debrid search service not configured")
		return out
	}
	debOpts := debrid.SearchOptions{
		Query:                 opts.Query,
		Categories:            append([]string{}, opts.Categories...),
		MaxResults:            rawOpts.MaxResults,
		IMDBID:                opts.IMDBID,
		MediaType:             opts.MediaType,
		Year:                  opts.Year,
		AlternateTitles:       append([]string{}, alternateTitles...),
		UserID:                opts.UserID,
		ClientID:              opts.ClientID,
		TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
		EpisodeResolver:       opts.EpisodeResolver,
		AbsoluteEpisodeNumber: opts.AbsoluteEpisodeNumber,
		IsAnime:               opts.IsAnime,
		IsDaily:               opts.IsDaily,
		TargetAirDate:         opts.TargetAirDate,
		EpisodeAirYear:        opts.EpisodeAirYear,
		EpisodeReleased:       opts.EpisodeReleased,
		SkipFilter:            true,
	}
	raw, err := s.debrid.Search(ctx, debOpts)
	if err != nil {
		out.err = err
		return out
	}
	incomplete := false
	for i := range raw {
		if raw[i].ServiceType == models.ServiceTypeUnknown {
			raw[i].ServiceType = models.ServiceTypeDebrid
		}
		if raw[i].Attributes != nil && strings.EqualFold(strings.TrimSpace(raw[i].Attributes["searchIncomplete"]), "true") {
			incomplete = true
		}
	}
	out.raw = raw
	out.incomplete = incomplete
	out.scored, out.filtered = s.scoreSourceCandidates(opts, settings, raw, s.buildFilterOptions(opts, filterBundle.Debrid), filterBundle, animeSettings, filterOverrides, rankingBundle)
	log.Printf("[indexer] TIMING: split debrid search complete (took: %v, raw=%d, passed=%d)", time.Since(debridStart), len(raw), len(out.scored))
	return out
}

// scoreSourceCandidates runs the same filter→score→rank pipeline SearchWithScoring
// applies to the combined source set, restricted to one source's raw results, and
// returns the passed-only scored list (ranked within the source) plus the count of
// raw results rejected by the filter. Scoring shares the DEFAULT filter settings
// for the context and per-service criteria for each result, exactly like the
// non-split path, so one source's candidates keep the same relative order they
// would have inside the merged list.
func (s *Service) scoreSourceCandidates(opts SearchOptions, settings config.Settings, raw []models.NZBResult, filterOpts filter.Options, filterBundle effectiveFilterBundle, animeSettings models.AnimeFilteringSettings, filterOverrides effectiveOverrides, rankingBundle effectiveRankingBundle) ([]models.ScoredNZBResult, int) {
	expectedYear := opts.Year
	if expectedYear == 0 {
		expectedYear = debrid.ParseQuery(opts.Query).Year
	}

	detailed := make([]filter.FilteredResult, 0, len(raw))
	for _, r := range raw {
		details := filter.ResultsWithDetails([]models.NZBResult{r}, filterOpts)
		if len(details) > 0 {
			detailed = append(detailed, details[0])
		}
	}
	passedResults := make([]models.NZBResult, 0, len(detailed))
	for _, result := range detailed {
		if result.Passed {
			passedResults = append(passedResults, result.Result)
		}
	}
	applyEpisodeYearPriority(passedResults, expectedYear, opts.EpisodeAirYear)
	passedIndex := 0
	for i := range detailed {
		if detailed[i].Passed {
			detailed[i].Result = passedResults[passedIndex]
			passedIndex++
		}
	}

	filteredCount := 0
	passed := make([]models.ScoredNZBResult, 0, len(detailed))
	for _, fr := range detailed {
		if !fr.Passed {
			filteredCount++
			continue
		}
		resultCtx := s.buildScoringContextForResult(opts, settings, filterBundle, rankingBundle, animeSettings, fr.Result)
		score, _ := ScoreResult(fr.Result, resultCtx)
		passed = append(passed, models.ScoredNZBResult{NZBResult: fr.Result, TotalScore: score, FilterStatus: "passed"})
	}
	if len(passed) > 0 {
		scoringCtx := s.buildScoringContextWithCriteria(opts, settings, filterBundle.Default, animeSettings, rankingBundle.Default)
		if rankingBundle.NewestReleaseFirst {
			sortScoredResultsNewestReleaseFirst(passed)
		} else {
			sortScoredResultsByRankingBundle(passed, scoringCtx, rankingBundle)
		}
		maxPerRes := models.IntVal(filterOverrides.MaxResultsPerResolution, 0)
		if maxPerRes > 0 {
			resolutionCounts := map[int]int{}
			var limited []models.ScoredNZBResult
			for _, r := range passed {
				res := extractResolutionFromResult(r.NZBResult)
				if resolutionCounts[res] < maxPerRes {
					limited = append(limited, r)
					resolutionCounts[res]++
				}
			}
			log.Printf("[indexer] Per-resolution limit=%d applied to %s passed results: %d -> %d", maxPerRes, "split", len(passed), len(limited))
			passed = limited
		}
	}
	return passed, filteredCount
}

// SearchTest runs search with full scoring breakdown and filter details for the admin search tester.
func (s *Service) SearchTest(ctx context.Context, opts SearchOptions) ([]models.ScoredNZBResult, error) {
	searchStart := time.Now()
	log.Printf("[indexer] SearchTest started for query=%q mediaType=%q", opts.Query, opts.MediaType)

	searchOpts := opts
	searchOpts.IncludeFiltered = true
	searchOpts.IncludeScoreBreakdown = true
	// Admin tester needs the full scored set, including filtered rejects.
	searchOpts.MaxResults = 0
	result, err := s.SearchWithScoring(ctx, searchOpts)
	if err != nil {
		return nil, err
	}

	passedCount := 0
	filteredCount := 0
	for _, scored := range result {
		if scored.FilterStatus == "filtered" {
			filteredCount++
		} else {
			passedCount++
		}
	}
	log.Printf("[indexer] SearchTest complete: %d total (%d passed, %d filtered) in %v",
		len(result), passedCount, filteredCount, time.Since(searchStart))
	return result, nil
}

// searchRawResults collects unfiltered results from all search sources.
func (s *Service) searchRawResults(ctx context.Context, opts SearchOptions) ([]models.NZBResult, error) {
	searchStart := time.Now()
	if s.cfg == nil {
		return nil, errors.New("config manager not configured")
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	settings = config.FilterSettingsForProfile(settings, opts.UserID)

	includeUsenet := shouldUseUsenet(settings.Streaming.ServiceMode)
	includeDebrid := shouldUseDebrid(settings.Streaming.ServiceMode)

	// Warn if no indexers/scrapers are enabled for the active service mode
	if includeUsenet {
		enabledIndexers := 0
		for _, idx := range settings.Indexers {
			if idx.Enabled {
				enabledIndexers++
			}
		}
		if enabledIndexers == 0 {
			log.Printf("[indexer] WARNING: service mode %q includes usenet but no indexers are enabled", settings.Streaming.ServiceMode)
		}
	}
	if includeDebrid {
		enabledScrapers := 0
		for _, sc := range settings.TorrentScrapers {
			if sc.Enabled {
				enabledScrapers++
			}
		}
		if enabledScrapers == 0 {
			log.Printf("[indexer] WARNING: service mode %q includes debrid but no torrent scrapers are enabled", settings.Streaming.ServiceMode)
		}
	}

	alternateTitles := s.resolveAlternateTitles(ctx, opts, s.getEffectiveMetadataLanguage(opts.UserID, settings), settings.Streaming.MaxAlternateTitleSearches)
	parsedQuery := debrid.ParseQuery(opts.Query)
	searchQueries := buildSearchQueries(opts, parsedQuery, alternateTitles)
	filterBundle, animeSettings, filterOverrides := s.getEffectiveFilterBundle(opts.UserID, opts.ClientID, settings)
	filterSettings := filterBundle.Default
	rankingBundle := s.getEffectiveRankingBundle(opts.UserID, opts.ClientID, settings)
	rankingCriteria := rankingBundle.Default
	cacheKey := s.searchCacheKey("raw", opts, settings, alternateTitles, filterSettings, filterBundle, animeSettings, filterOverrides, rankingCriteria, rankingBundle)
	if cached, ok := s.getCachedSearchResults(cacheKey, searchStart); ok {
		log.Printf("[indexer] raw search cache hit for query=%q mediaType=%q user=%q client=%q results=%d", opts.Query, opts.MediaType, opts.UserID, opts.ClientID, len(cached))
		return cached, nil
	}

	type searchResult struct {
		results []models.NZBResult
		err     error
		source  string
	}

	var wg sync.WaitGroup
	resultsChan := make(chan searchResult, 2)

	if includeUsenet {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if opts.SkipFilter {
				// Fetch raw results without filtering
				usenetResults, err := s.fetchUsenetResultsAllQueries(ctx, settings, opts, searchQueries)
				if err != nil {
					resultsChan <- searchResult{err: err, source: "usenet"}
					return
				}
				for i := range usenetResults {
					if usenetResults[i].ServiceType == models.ServiceTypeUnknown {
						usenetResults[i].ServiceType = models.ServiceTypeUsenet
					}
				}
				resultsChan <- searchResult{results: usenetResults, source: "usenet"}
			} else {
				usenetResults, err := s.searchUsenetWithFilter(ctx, settings, opts, parsedQuery, alternateTitles, searchQueries, filterBundle.Usenet)
				if err != nil {
					resultsChan <- searchResult{err: err, source: "usenet"}
					return
				}
				for i := range usenetResults {
					if usenetResults[i].ServiceType == models.ServiceTypeUnknown {
						usenetResults[i].ServiceType = models.ServiceTypeUsenet
					}
				}
				resultsChan <- searchResult{results: usenetResults, source: "usenet"}
			}
		}()
	}

	if includeDebrid {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.debrid == nil {
				resultsChan <- searchResult{err: fmt.Errorf("debrid search service not configured"), source: "debrid"}
				return
			}
			debOpts := debrid.SearchOptions{
				Query:                 opts.Query,
				Categories:            append([]string{}, opts.Categories...),
				MaxResults:            opts.MaxResults,
				IMDBID:                opts.IMDBID,
				MediaType:             opts.MediaType,
				Year:                  opts.Year,
				AlternateTitles:       append([]string{}, alternateTitles...),
				UserID:                opts.UserID,
				ClientID:              opts.ClientID,
				TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
				EpisodeResolver:       opts.EpisodeResolver,
				AbsoluteEpisodeNumber: opts.AbsoluteEpisodeNumber,
				IsAnime:               opts.IsAnime,
				IsDaily:               opts.IsDaily,
				TargetAirDate:         opts.TargetAirDate,
				EpisodeAirYear:        opts.EpisodeAirYear,
				EpisodeReleased:       opts.EpisodeReleased,
				SkipFilter:            opts.SkipFilter,
			}
			debridResults, err := s.debrid.Search(ctx, debOpts)
			if err != nil {
				resultsChan <- searchResult{err: err, source: "debrid"}
				return
			}
			for i := range debridResults {
				if debridResults[i].ServiceType == models.ServiceTypeUnknown {
					debridResults[i].ServiceType = models.ServiceTypeDebrid
				}
			}
			resultsChan <- searchResult{results: debridResults, source: "debrid"}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var aggregated []models.NZBResult
	var errCount, srcCount int
	incompleteSearch := false

	for sr := range resultsChan {
		srcCount++
		if sr.err != nil {
			log.Printf("[indexer] %s search failed: %v", sr.source, sr.err)
			errCount++
			continue
		}
		if len(sr.results) > 0 {
			if searchResultsIncomplete(sr.results) {
				incompleteSearch = true
			}
			aggregated = append(aggregated, sr.results...)
		}
	}

	// Only return an error when EVERY source failed — a partial scraper failure
	// (e.g. Comet timeout, Nyaa 429) with 0 results should just return empty,
	// not a 504, so the caller can handle "no results" gracefully.
	if srcCount > 0 && errCount == srcCount {
		log.Printf("[indexer] searchRawResults: all %d sources failed, returning error", srcCount)
		return nil, fmt.Errorf("all search sources failed")
	}

	if errCount == 0 && !incompleteSearch {
		s.setCachedSearchResults(cacheKey, aggregated, time.Now())
	} else {
		log.Printf("[indexer] skipping raw search cache for query=%q because one or more providers failed", opts.Query)
	}
	return aggregated, nil
}

func searchResultsIncomplete(results []models.NZBResult) bool {
	for _, result := range results {
		if strings.EqualFold(strings.TrimSpace(result.Attributes["searchIncomplete"]), "true") {
			return true
		}
	}
	return false
}

// fetchUsenetResultsAllQueries fetches raw usenet results from all queries without filtering.
func (s *Service) fetchUsenetResultsAllQueries(ctx context.Context, settings config.Settings, opts SearchOptions, queries []string) ([]models.NZBResult, error) {
	// Filter out empty queries
	var validQueries []string
	for _, q := range queries {
		if trimmed := strings.TrimSpace(q); trimmed != "" {
			validQueries = append(validQueries, trimmed)
		}
	}
	if len(validQueries) == 0 {
		return nil, nil
	}

	// Single query — no parallelization overhead
	if len(validQueries) == 1 {
		queryOpts := opts
		queryOpts.Query = validQueries[0]
		return s.fetchUsenetResults(ctx, settings, queryOpts)
	}

	// Parallelize searches across all alternate queries
	log.Printf("[indexer/usenet] fetching %d queries in parallel (raw)", len(validQueries))

	type searchResult struct {
		results []models.NZBResult
		err     error
	}

	resultsChan := make(chan searchResult, len(validQueries))
	for _, query := range validQueries {
		go func(q string) {
			queryOpts := opts
			queryOpts.Query = q
			results, err := s.fetchUsenetResults(ctx, settings, queryOpts)
			resultsChan <- searchResult{results: results, err: err}
		}(query)
	}

	var allResults []models.NZBResult
	var lastErr error
	successes := 0
	for range validQueries {
		res := <-resultsChan
		if res.err != nil {
			lastErr = res.err
			continue
		}
		successes++
		allResults = append(allResults, res.results...)
	}
	if len(allResults) == 0 && lastErr != nil && successes == 0 {
		return nil, lastErr
	}

	// Deduplicate results that appear across multiple alternate-title queries
	seen := make(map[string]struct{}, len(allResults))
	deduped := make([]models.NZBResult, 0, len(allResults))
	for _, r := range allResults {
		key := usenetResultDedupKey(r)
		if key == "" {
			deduped = append(deduped, r)
			continue
		}
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			deduped = append(deduped, r)
		}
	}
	return deduped, nil
}

// buildFilterOptions constructs filter.Options from SearchOptions and FilterSettings.
func (s *Service) buildFilterOptions(opts SearchOptions, filterSettings models.FilterSettings) filter.Options {
	parsedQuery := debrid.ParseQuery(opts.Query)
	expectedTitle := strings.TrimSpace(parsedQuery.Title)
	if expectedTitle == "" {
		expectedTitle = strings.TrimSpace(opts.Query)
	}

	expectedYear := opts.Year
	if expectedYear == 0 {
		expectedYear = parsedQuery.Year
	}

	isMovie := strings.ToLower(opts.MediaType) == "movie"

	alternateTitles := s.resolveAlternateTitles(context.Background(), opts, "", 0)

	return filter.Options{
		ExpectedTitle:         expectedTitle,
		ExpectedYear:          expectedYear,
		ExpectedCountry:       opts.CountryCode,
		EpisodeAirYear:        opts.EpisodeAirYear,
		IsMovie:               isMovie,
		MaxSizeMovieGB:        models.FloatVal(filterSettings.MaxSizeMovieGB, 0),
		MaxSizeEpisodeGB:      models.FloatVal(filterSettings.MaxSizeEpisodeGB, 0),
		MaxResolution:         models.StringVal(filterSettings.MaxResolution, ""),
		HDRDVPolicy:           filter.HDRDVPolicy(filterSettings.HDRDVPolicy),
		AlternateTitles:       alternateTitles,
		RequiredTerms:         filterSettings.RequiredTerms,
		FilterOutTerms:        filterSettings.FilterOutTerms,
		EpisodeResolver:       opts.EpisodeResolver,
		IsDaily:               opts.IsDaily,
		TargetAirDate:         opts.TargetAirDate,
		TargetSeason:          parsedQuery.Season,
		TargetEpisode:         parsedQuery.Episode,
		TargetAbsoluteEpisode: opts.AbsoluteEpisodeNumber,
	}
}

// sortResults applies ranking sort to results in-place.
func (s *Service) sortResults(results []models.NZBResult, opts SearchOptions, settings config.Settings, filterSettings models.FilterSettings) {
	if len(results) == 0 {
		return
	}

	scoringCtx := s.buildScoringContext(opts, settings, filterSettings, models.AnimeFilteringSettings{})
	s.sortResultsByScore(results, scoringCtx)
}

// SplitSearchResult holds results from either debrid or usenet search
type SplitSearchResult struct {
	Results []models.NZBResult
	Source  string // "debrid" or "usenet"
	Err     error
}

// SearchSplit runs debrid and usenet searches in parallel and returns results via separate channels.
// This allows the caller to process debrid results immediately while usenet search continues.
// The caller is responsible for draining both channels to avoid goroutine leaks.
func (s *Service) SearchSplit(ctx context.Context, opts SearchOptions) (debridChan <-chan SplitSearchResult, usenetChan <-chan SplitSearchResult) {
	callNum := s.searchSplitCount.Add(1)
	log.Printf("[search-stats] SearchSplit #%d started (query=%q, mediaType=%q, user=%q, client=%q)",
		callNum, opts.Query, opts.MediaType, opts.UserID, opts.ClientID)

	debridOut := make(chan SplitSearchResult, 1)
	usenetOut := make(chan SplitSearchResult, 1)

	settings, err := s.cfg.Load()
	if err != nil {
		debridOut <- SplitSearchResult{Err: fmt.Errorf("load settings: %w", err), Source: "debrid"}
		usenetOut <- SplitSearchResult{Err: fmt.Errorf("load settings: %w", err), Source: "usenet"}
		close(debridOut)
		close(usenetOut)
		return debridOut, usenetOut
	}
	settings = config.FilterSettingsForProfile(settings, opts.UserID)

	filterBundle, animeSettings2, filterOverrides := s.getEffectiveFilterBundle(opts.UserID, opts.ClientID, settings)
	filterSettings := filterBundle.Default

	// Inject anime language filter-out terms early (before search/filter calls)
	if opts.IsAnime && models.BoolVal(animeSettings2.AnimeLanguageEnabled, false) {
		langCode := ""
		if animeSettings2.AnimePreferredLanguage != nil {
			langCode = *animeSettings2.AnimePreferredLanguage
		}
		if langCode == "" {
			langCode = "eng"
		}
		_, _, animeFilterOut := filter.GetAnimeLanguageTerms(langCode)
		if len(animeFilterOut) > 0 {
			filterSettings.FilterOutTerms = append(filterSettings.FilterOutTerms, animeFilterOut...)
			filterBundle.Default.FilterOutTerms = append(filterBundle.Default.FilterOutTerms, animeFilterOut...)
			filterBundle.Debrid.FilterOutTerms = append(filterBundle.Debrid.FilterOutTerms, animeFilterOut...)
			filterBundle.Usenet.FilterOutTerms = append(filterBundle.Usenet.FilterOutTerms, animeFilterOut...)
			log.Printf("[indexer] Anime language filter-out: injected %d terms for lang=%s", len(animeFilterOut), langCode)
		}
	}

	alternateTitles := s.resolveAlternateTitles(ctx, opts, s.getEffectiveMetadataLanguage(opts.UserID, settings), settings.Streaming.MaxAlternateTitleSearches)
	parsedQuery := debrid.ParseQuery(opts.Query)
	searchQueries := buildSearchQueries(opts, parsedQuery, alternateTitles)

	includeUsenet := shouldUseUsenet(settings.Streaming.ServiceMode)
	includeDebrid := shouldUseDebrid(settings.Streaming.ServiceMode)
	bypassAIOStreamsRanking := shouldBypassAIOStreamsRanking(settings, filterOverrides, includeUsenet)

	rankingBundle := s.getEffectiveRankingBundle(opts.UserID, opts.ClientID, settings)
	scoringCtx := s.buildScoringContextWithCriteria(opts, settings, filterSettings, animeSettings2, rankingBundle.Default)

	// Helper to inject daily show attributes into results (same as Search path)
	injectDailyAttrs := func(results []models.NZBResult) {
		if !opts.IsDaily && opts.TargetAirDate == "" {
			return
		}
		for i := range results {
			if results[i].Attributes == nil {
				results[i].Attributes = make(map[string]string)
			}
			if opts.IsDaily {
				results[i].Attributes["isDaily"] = "true"
			}
			if opts.TargetAirDate != "" {
				results[i].Attributes["targetAirDate"] = opts.TargetAirDate
			}
		}
	}

	// Helper to apply ranking sort to results
	applyRanking := func(results []models.NZBResult) {
		if len(results) == 0 {
			return
		}
		if bypassAIOStreamsRanking {
			log.Printf("[indexer] Bypassing mediastorm ranking - AIOStreams is the only enabled scraper and bypass setting is enabled")
			return
		}
		sortResultsByRankingBundle(results, scoringCtx, rankingBundle)
	}

	// Launch debrid search
	go func() {
		defer close(debridOut)
		if !includeDebrid || s.debrid == nil {
			return
		}

		debridStart := time.Now()
		log.Printf("[indexer] TIMING: split debrid search starting (query=%q)", opts.Query)

		debOpts := debrid.SearchOptions{
			Query:                 opts.Query,
			Categories:            append([]string{}, opts.Categories...),
			MaxResults:            opts.MaxResults,
			IMDBID:                opts.IMDBID,
			MediaType:             opts.MediaType,
			Year:                  opts.Year,
			AlternateTitles:       append([]string{}, alternateTitles...),
			UserID:                opts.UserID,
			ClientID:              opts.ClientID,
			TotalSeriesEpisodes:   opts.TotalSeriesEpisodes,
			EpisodeResolver:       opts.EpisodeResolver,
			AbsoluteEpisodeNumber: opts.AbsoluteEpisodeNumber,
			IsAnime:               opts.IsAnime,
			IsDaily:               opts.IsDaily,
			TargetAirDate:         opts.TargetAirDate,
			EpisodeAirYear:        opts.EpisodeAirYear,
			EpisodeReleased:       opts.EpisodeReleased,
		}

		debridResults, err := s.debrid.Search(ctx, debOpts)
		if err != nil {
			log.Printf("[indexer] TIMING: split debrid search failed after %v: %v", time.Since(debridStart), err)
			debridOut <- SplitSearchResult{Err: err, Source: "debrid"}
			return
		}

		for i := range debridResults {
			if debridResults[i].ServiceType == models.ServiceTypeUnknown {
				debridResults[i].ServiceType = models.ServiceTypeDebrid
			}
		}

		// Inject daily show attributes for file matching
		injectDailyAttrs(debridResults)

		// Apply ranking sort so prequeue gets results in the same order as manual search
		applyRanking(debridResults)

		log.Printf("[indexer] TIMING: split debrid search complete (took: %v, results: %d)", time.Since(debridStart), len(debridResults))
		debridOut <- SplitSearchResult{Results: debridResults, Source: "debrid"}
	}()

	// Launch usenet search
	go func() {
		defer close(usenetOut)
		if !includeUsenet {
			return
		}

		usenetStart := time.Now()
		log.Printf("[indexer] TIMING: split usenet search starting (query=%q)", opts.Query)

		usenetResults, err := s.searchUsenetWithFilter(ctx, settings, opts, parsedQuery, alternateTitles, searchQueries, filterBundle.Usenet)
		if err != nil {
			log.Printf("[indexer] TIMING: split usenet search failed after %v: %v", time.Since(usenetStart), err)
			usenetOut <- SplitSearchResult{Err: err, Source: "usenet"}
			return
		}

		for i := range usenetResults {
			if usenetResults[i].ServiceType == models.ServiceTypeUnknown {
				usenetResults[i].ServiceType = models.ServiceTypeUsenet
			}
		}

		// Inject daily show attributes for file matching
		injectDailyAttrs(usenetResults)

		// Apply ranking sort so prequeue gets results in the same order as manual search
		applyRanking(usenetResults)

		log.Printf("[indexer] TIMING: split usenet search complete (took: %v, results: %d)", time.Since(usenetStart), len(usenetResults))
		usenetOut <- SplitSearchResult{Results: usenetResults, Source: "usenet"}
	}()

	return debridOut, usenetOut
}

func (s *Service) resolveAlternateTitles(ctx context.Context, opts SearchOptions, metadataLang string, maxAlternates int) []string {
	if s.metadata == nil {
		return nil
	}

	parsed := debrid.ParseQuery(opts.Query)
	query := strings.TrimSpace(parsed.Title)
	if query == "" {
		query = strings.TrimSpace(opts.Query)
	}
	if query == "" {
		return nil
	}

	results, err := s.metadata.Search(ctx, query, opts.MediaType)
	if err != nil {
		log.Printf("[indexer] metadata search for aliases failed query=%q err=%v", query, err)
		return nil
	}
	if len(results) == 0 {
		return nil
	}

	var chosen *models.Title
	imdbID := strings.TrimSpace(opts.IMDBID)
	if imdbID != "" {
		for i := range results {
			if strings.EqualFold(strings.TrimSpace(results[i].Title.IMDBID), imdbID) {
				chosen = &results[i].Title
				break
			}
		}
	}
	if chosen == nil && opts.Year > 0 {
		for i := range results {
			year := results[i].Title.Year
			if year == 0 {
				continue
			}
			diff := opts.Year - year
			if diff < 0 {
				diff = -diff
			}
			if diff <= filter.MaxYearDifference {
				chosen = &results[i].Title
				break
			}
		}
	}
	if chosen == nil {
		chosen = &results[0].Title
	}

	type alternateTitleCandidate struct {
		value                     string
		languageMatch             bool
		releaseReady              bool
		asciiReleaseReady         bool
		romanizedOriginalLanguage bool
	}

	seen := make(map[string]struct{})
	// The canonical/query titles are already searched and should not consume an
	// alternate-title slot when an aliases endpoint returns them again.
	seen[strings.ToLower(query)] = struct{}{}
	if canonical := strings.TrimSpace(chosen.Name); canonical != "" {
		seen[strings.ToLower(canonical)] = struct{}{}
	}

	var candidates []alternateTitleCandidate
	add := func(value string, languageMatch, originalLanguage bool) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		lowered := strings.ToLower(trimmed)
		if _, exists := seen[lowered]; exists {
			return
		}
		seen[lowered] = struct{}{}
		releaseReady := isReleaseFriendlyTitle(trimmed)
		candidates = append(candidates, alternateTitleCandidate{
			value:                     trimmed,
			languageMatch:             languageMatch,
			releaseReady:              releaseReady,
			asciiReleaseReady:         releaseReady && isASCIIString(trimmed),
			romanizedOriginalLanguage: opts.IsAnime && originalLanguage && releaseReady,
		})
	}
	add(chosen.OriginalName, false, true)
	for _, alt := range chosen.AlternateTitles {
		add(alt, false, false)
	}

	// Fetch full TVDB aliases (international titles) if the metadata service
	// supports it. The search API translations are often incomplete — the
	// aliases endpoint has all known alternate titles across languages.
	// Aliases matching the user's metadata language are added first.
	if aliasSvc, ok := s.metadata.(metadataAliasService); ok && chosen.TVDBID > 0 {
		langAliases := aliasSvc.FetchAliasesWithLanguage(chosen.MediaType, chosen.TVDBID)
		lang := strings.ToLower(strings.TrimSpace(metadataLang))
		originalLang := strings.ToLower(strings.TrimSpace(chosen.Language))
		var langMatched, others []string
		aliasLanguages := make(map[string]string, len(langAliases))
		for _, la := range langAliases {
			aliasLanguages[strings.ToLower(strings.TrimSpace(la.Name))] = strings.ToLower(strings.TrimSpace(la.Language))
			if lang != "" && strings.ToLower(strings.TrimSpace(la.Language)) == lang {
				langMatched = append(langMatched, la.Name)
			} else {
				others = append(others, la.Name)
			}
		}
		for _, a := range langMatched {
			add(a, true, originalLang != "" && aliasLanguages[strings.ToLower(strings.TrimSpace(a))] == originalLang)
		}
		for _, a := range others {
			add(a, false, originalLang != "" && aliasLanguages[strings.ToLower(strings.TrimSpace(a))] == originalLang)
		}
	}

	// Alias APIs often return a native-script original before a provider-supplied
	// romanized title. Generic transliteration of CJK text can produce the wrong
	// reading for release searches (for example Japanese kanji transliterated as
	// Chinese). For anime, a Latin-script alias tagged with the title's original
	// language is the strongest release-name signal and outranks translated
	// metadata-language aliases. Otherwise retain metadata-language priority,
	// then prefer release-friendly Latin titles. Stable sorting preserves provider
	// order within each quality tier.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].romanizedOriginalLanguage != candidates[j].romanizedOriginalLanguage {
			return candidates[i].romanizedOriginalLanguage
		}
		if candidates[i].romanizedOriginalLanguage && candidates[i].asciiReleaseReady != candidates[j].asciiReleaseReady {
			return candidates[i].asciiReleaseReady
		}
		if candidates[i].languageMatch != candidates[j].languageMatch {
			return candidates[i].languageMatch
		}
		if candidates[i].releaseReady != candidates[j].releaseReady {
			return candidates[i].releaseReady
		}
		return false
	})

	aliases := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		aliases = append(aliases, candidate.value)
	}

	if maxAlternates > 0 && len(aliases) > maxAlternates {
		log.Printf("[indexer] capping alternate titles from %d to %d for %q", len(aliases), maxAlternates, opts.Query)
		aliases = aliases[:maxAlternates]
	}

	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func isReleaseFriendlyTitle(value string) bool {
	hasLetter := false
	for _, r := range value {
		if !unicode.IsLetter(r) {
			continue
		}
		hasLetter = true
		if !unicode.In(r, unicode.Latin) {
			return false
		}
	}
	return hasLetter
}

func buildSearchQueries(opts SearchOptions, parsed debrid.ParsedQuery, alternateTitles []string) []string {
	seen := make(map[string]struct{})
	var queries []string
	addQuery := func(q string) {
		trimmed := strings.TrimSpace(q)
		if trimmed == "" {
			return
		}
		lowered := strings.ToLower(trimmed)
		if _, exists := seen[lowered]; exists {
			return
		}
		seen[lowered] = struct{}{}
		queries = append(queries, trimmed)
	}

	// For daily shows, prioritize date-based queries over S##E## format
	// Scene releases use format: "Show.Name.2026.01.21.Guest.Name.mkv"
	if opts.IsDaily && opts.TargetAirDate != "" {
		dateParts := strings.Split(opts.TargetAirDate, "-")
		if len(dateParts) == 3 {
			year := dateParts[0]
			month := dateParts[1]
			day := dateParts[2]

			// Add date-format queries FIRST (highest priority)
			addDateQueries := func(title string) {
				title = strings.TrimSpace(title)
				if title == "" {
					return
				}
				// Format: "Title 2026.01.21" (dot-separated, most common in scene releases)
				addQuery(fmt.Sprintf("%s %s.%s.%s", title, year, month, day))
				// Format: "Title 2026 01 21" (space-separated)
				addQuery(fmt.Sprintf("%s %s %s %s", title, year, month, day))
			}

			addDateQueries(parsed.Title)
			for _, alt := range alternateTitles {
				addDateQueries(alt)
			}

			log.Printf("[indexer] Added date-based queries for daily show (priority): date=%s", opts.TargetAirDate)
		}
	}

	if eventQuery := sportsEventSearchQuery(parsed.Title); eventQuery != "" {
		addQuery(eventQuery)
	}

	// Add the original query
	addQuery(opts.Query)

	// Add S##E## variants (for non-daily shows, or as fallback for daily shows)
	addVariants := func(title string) {
		for _, variant := range titleVariants(title) {
			composed := composeQueryForSearch(variant, opts, parsed)
			addQuery(composed)
			for _, absolute := range composeAbsoluteEpisodeQueries(variant, opts) {
				addQuery(absolute)
			}
		}
	}

	addVariants(parsed.Title)
	for _, alt := range alternateTitles {
		addVariants(alt)
	}

	return queries
}

func sportsEventSearchQuery(title string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(title, "：", ":"))
	upper := strings.ToUpper(normalized)
	if !strings.HasPrefix(upper, "UFC ") {
		return ""
	}

	rest := strings.TrimSpace(normalized[4:])
	if rest == "" {
		return ""
	}

	var digits strings.Builder
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		digits.WriteRune(r)
	}
	if digits.Len() == 0 {
		return ""
	}

	return "UFC " + digits.String()
}

func composeQueryForSearch(title string, opts SearchOptions, parsed debrid.ParsedQuery) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}

	parts := []string{title}
	if parsed.Season > 0 && parsed.Episode > 0 {
		parts = append(parts, fmt.Sprintf("S%02dE%02d", parsed.Season, parsed.Episode))
	} else if parsed.Season > 0 && parsed.HasSeasonMatch {
		parts = append(parts, fmt.Sprintf("S%02d", parsed.Season))
	}

	if shouldIncludeYear(opts, parsed) {
		year := opts.Year
		if year == 0 {
			year = parsed.Year
		}
		if year > 0 {
			parts = append(parts, fmt.Sprintf("%d", year))
		}
	}

	return strings.Join(parts, " ")
}

func composeAbsoluteEpisodeQueries(title string, opts SearchOptions) []string {
	title = strings.TrimSpace(title)
	if title == "" || !opts.IsAnime || opts.AbsoluteEpisodeNumber <= 0 {
		return nil
	}

	episode := opts.AbsoluteEpisodeNumber
	return []string{
		fmt.Sprintf("%s %d", title, episode),
		fmt.Sprintf("%s EP%d", title, episode),
		fmt.Sprintf("%s E%d", title, episode),
	}
}

func shouldIncludeYear(opts SearchOptions, parsed debrid.ParsedQuery) bool {
	switch strings.ToLower(strings.TrimSpace(opts.MediaType)) {
	case "movie", "movies", "film", "films":
		return true
	}
	if parsed.MediaType == debrid.MediaTypeMovie {
		return true
	}
	if opts.Year > 0 && parsed.Year == 0 && !filter.ShouldFilter(opts.Query) {
		return true
	}
	return false
}

func titleVariants(title string) []string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var variants []string

	ascii := normalizeToASCII(trimmed)
	if ascii != "" {
		lowered := strings.ToLower(ascii)
		seen[lowered] = struct{}{}
		variants = append(variants, ascii)
	}

	if isASCIIString(trimmed) {
		lowered := strings.ToLower(trimmed)
		if _, exists := seen[lowered]; !exists {
			seen[lowered] = struct{}{}
			variants = append(variants, trimmed)
		}
	}
	return variants
}

func normalizeToASCII(value string) string {
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"¼", " 1/4 ", "½", " 1/2 ", "¾", " 3/4 ",
		"⅐", " 1/7 ", "⅑", " 1/9 ", "⅒", " 1/10 ",
		"⅓", " 1/3 ", "⅔", " 2/3 ", "⅕", " 1/5 ",
		"⅖", " 2/5 ", "⅗", " 3/5 ", "⅘", " 4/5 ",
		"⅙", " 1/6 ", "⅚", " 5/6 ", "⅛", " 1/8 ",
		"⅜", " 3/8 ", "⅝", " 5/8 ", "⅞", " 7/8 ",
		"–", "-", "—", "-", "−", "-",
		"•", " ", "…", " ", "：", ":",
		"，", ",", "！", "!", "？", "?",
		"’", "'", "、", " ",
	)
	ascii := replacer.Replace(value)
	ascii = strings.TrimSpace(unidecode.Unidecode(ascii))
	ascii = strings.Join(strings.Fields(ascii), " ")
	return ascii
}

func isASCIIString(value string) bool {
	for _, r := range value {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return strings.TrimSpace(value) != ""
}

// searchUsenetWithFilter performs usenet search with explicit filter settings (for per-user filtering)
func (s *Service) searchUsenetWithFilter(ctx context.Context, settings config.Settings, opts SearchOptions, baseParsed debrid.ParsedQuery, alternateTitles []string, searchQueries []string, filterSettings models.FilterSettings) ([]models.NZBResult, error) {
	// Filter out empty queries
	var validQueries []string
	for _, query := range searchQueries {
		trimmed := strings.TrimSpace(query)
		if trimmed != "" {
			validQueries = append(validQueries, trimmed)
		}
	}

	if len(validQueries) == 0 {
		return []models.NZBResult{}, nil
	}

	// If only one query, run it directly (no parallelization overhead)
	if len(validQueries) == 1 {
		return s.searchUsenetSingleWithFilter(ctx, settings, opts, baseParsed, alternateTitles, validQueries[0], filterSettings)
	}

	// Parallelize searches across all alternate queries
	log.Printf("[indexer/usenet] searching %d queries in parallel", len(validQueries))

	type searchResult struct {
		query    string
		results  []models.NZBResult
		err      error
		priority int // lower = higher priority (primary query = 0)
	}

	resultsChan := make(chan searchResult, len(validQueries))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Launch all searches in parallel
	for idx, query := range validQueries {
		go func(priority int, q string) {
			queryOpts := opts
			queryOpts.Query = q

			if priority > 0 {
				log.Printf("[indexer/usenet] parallel search with alternate query: %q", q)
			}

			allResults, err := s.fetchUsenetResults(ctx, settings, queryOpts)
			if err != nil {
				resultsChan <- searchResult{query: q, err: err, priority: priority}
				return
			}

			if len(allResults) == 0 {
				resultsChan <- searchResult{query: q, results: nil, priority: priority}
				return
			}

			parsedForQuery := debrid.ParseQuery(q)
			filtered := s.applyUsenetFilteringWithSettings(allResults, opts, baseParsed, parsedForQuery, alternateTitles, filterSettings)
			resultsChan <- searchResult{query: q, results: filtered, priority: priority}
		}(idx, query)
	}

	// Merge results across all queries (deduplicated). The per-query filter
	// above already enforces title/year relevance, so the union is safe. We must
	// NOT keep only the primary (priority-0) query's results: a sparse primary
	// query — e.g. a bare title that returns only the latest uploads — would hide
	// the far richer set an alternate query (like "<title> <year>") found, which
	// is exactly how a Dutch WEB rip ended up beating a 4K HDR DV remux that the
	// "<title> <year>" query had returned.
	seen := make(map[string]struct{})
	var merged []models.NZBResult
	var lastErr error
	successes := 0
	resultsReceived := 0

	for resultsReceived < len(validQueries) {
		select {
		case <-ctx.Done():
			// Context cancelled (timeout / caller abort): return what we have.
			if len(merged) > 0 {
				return merged, nil
			}
			return nil, ctx.Err()
		case res := <-resultsChan:
			resultsReceived++

			if res.err != nil {
				lastErr = res.err
				continue
			}
			successes++

			if len(res.results) == 0 {
				continue
			}

			added := 0
			for _, r := range res.results {
				key := usenetResultDedupKey(r)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				merged = append(merged, r)
				added++
			}
			log.Printf("[indexer/usenet] merged %d/%d results from query %q (priority %d), total unique=%d",
				added, len(res.results), res.query, res.priority, len(merged))
		}
	}

	if len(merged) > 0 {
		return merged, nil
	}

	if lastErr != nil && successes == 0 {
		return nil, lastErr
	}
	return []models.NZBResult{}, nil
}

// usenetResultDedupKey returns a stable identity for an NZB result so the same
// release returned by multiple alternate queries or indexers is counted once.
// Indexer GUIDs and NZB URLs are often source-specific, so release title is the
// primary identity when present.
func usenetResultDedupKey(r models.NZBResult) string {
	if title := normalizedUsenetReleaseTitle(r.Title); title != "" {
		return "title:" + title
	}
	if g := strings.TrimSpace(r.GUID); g != "" {
		return "guid:" + g
	}
	if d := strings.TrimSpace(r.DownloadURL); d != "" {
		return "url:" + d
	}
	if l := strings.TrimSpace(r.Link); l != "" {
		return "link:" + l
	}
	if r.SizeBytes > 0 {
		return fmt.Sprintf("size:%d", r.SizeBytes)
	}
	return ""
}

func normalizedUsenetReleaseTitle(title string) string {
	title = mediaresolve.NormalizeReleasePart(title)
	title = normalizeToASCII(title)
	title = strings.ToLower(title)
	title = releaseDedupTokenSep.ReplaceAllString(title, " ")
	return strings.Join(strings.Fields(title), " ")
}

func (s *Service) searchUsenet(ctx context.Context, settings config.Settings, opts SearchOptions, baseParsed debrid.ParsedQuery, alternateTitles []string, searchQueries []string) ([]models.NZBResult, error) {
	// Use global settings for backwards compatibility
	filterSettings := models.FilterSettings{
		MaxSizeMovieGB:   models.FloatPtr(settings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB: models.FloatPtr(settings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:    models.StringPtr(settings.Filtering.MaxResolution),
		HDRDVPolicy:      models.HDRDVPolicy(settings.Filtering.HDRDVPolicy),
		RequiredTerms:    settings.Filtering.RequiredTerms,
		FilterOutTerms:   settings.Filtering.FilterOutTerms,
	}
	return s.searchUsenetWithFilter(ctx, settings, opts, baseParsed, alternateTitles, searchQueries, filterSettings)
}

// searchUsenetSingleWithFilter performs a single usenet search with explicit filter settings
func (s *Service) searchUsenetSingleWithFilter(ctx context.Context, settings config.Settings, opts SearchOptions, baseParsed debrid.ParsedQuery, alternateTitles []string, query string, filterSettings models.FilterSettings) ([]models.NZBResult, error) {
	queryOpts := opts
	queryOpts.Query = query

	allResults, err := s.fetchUsenetResults(ctx, settings, queryOpts)
	if err != nil {
		return nil, err
	}

	if len(allResults) == 0 {
		return []models.NZBResult{}, nil
	}

	parsedForQuery := debrid.ParseQuery(query)
	filtered := s.applyUsenetFilteringWithSettings(allResults, queryOpts, baseParsed, parsedForQuery, alternateTitles, filterSettings)
	return filtered, nil
}

// searchUsenetSingle performs a single usenet search (non-parallel path)
func (s *Service) searchUsenetSingle(ctx context.Context, settings config.Settings, opts SearchOptions, baseParsed debrid.ParsedQuery, alternateTitles []string, query string) ([]models.NZBResult, error) {
	queryOpts := opts
	queryOpts.Query = query

	allResults, err := s.fetchUsenetResults(ctx, settings, queryOpts)
	if err != nil {
		return nil, err
	}

	if len(allResults) == 0 {
		return []models.NZBResult{}, nil
	}

	parsedForQuery := debrid.ParseQuery(query)
	filtered := s.applyUsenetFiltering(allResults, settings, queryOpts, baseParsed, parsedForQuery, alternateTitles)
	return filtered, nil
}

func (s *Service) fetchUsenetResults(ctx context.Context, settings config.Settings, opts SearchOptions) ([]models.NZBResult, error) {
	// Collect enabled indexers
	var enabled []config.IndexerConfig
	for _, idx := range settings.Indexers {
		if !idx.Enabled {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(idx.Type))
		if t == "" || t == "newznab" || t == "torznab" {
			enabled = append(enabled, idx)
		}
	}

	if len(enabled) == 0 {
		return nil, nil
	}

	// Single indexer — no parallelization overhead
	if len(enabled) == 1 {
		return s.searchTorznab(ctx, enabled[0], opts)
	}

	type indexerResult struct {
		results []models.NZBResult
		err     error
	}

	resultsChan := make(chan indexerResult, len(enabled))
	for _, idx := range enabled {
		go func(ix config.IndexerConfig) {
			results, err := s.searchTorznab(ctx, ix, opts)
			resultsChan <- indexerResult{results: results, err: err}
		}(idx)
	}

	var allResults []models.NZBResult
	var lastErr error
	successes := 0
	for range enabled {
		res := <-resultsChan
		if res.err != nil {
			lastErr = res.err
			continue
		}
		successes++
		allResults = append(allResults, res.results...)
	}
	if len(allResults) == 0 && lastErr != nil && successes == 0 {
		return nil, lastErr
	}
	return allResults, nil
}

// applyUsenetFilteringWithSettings applies filtering using explicit filter settings (for per-user filtering)
func (s *Service) applyUsenetFilteringWithSettings(results []models.NZBResult, opts SearchOptions, baseParsed, queryParsed debrid.ParsedQuery, alternateTitles []string, filterSettings models.FilterSettings) []models.NZBResult {
	expectedTitle := strings.TrimSpace(baseParsed.Title)
	if expectedTitle == "" {
		expectedTitle = strings.TrimSpace(queryParsed.Title)
	}
	if eventQuery := sportsEventSearchQuery(expectedTitle); eventQuery != "" {
		expectedTitle = eventQuery
		alternateTitles = append([]string{eventQuery}, alternateTitles...)
		filterSettings.FilterOutTerms = append(append([]string{}, filterSettings.FilterOutTerms...), sportsEventSideContentFilterTerms...)
	}

	expectedYear := opts.Year
	if expectedYear == 0 {
		if baseParsed.Year > 0 {
			expectedYear = baseParsed.Year
		} else {
			expectedYear = queryParsed.Year
		}
	}

	isMovie := queryParsed.MediaType == debrid.MediaTypeMovie
	if baseParsed.MediaType != debrid.MediaTypeUnknown {
		isMovie = baseParsed.MediaType == debrid.MediaTypeMovie
	}
	if strings.TrimSpace(opts.MediaType) != "" {
		isMovie = strings.ToLower(opts.MediaType) == "movie"
	}

	if expectedTitle == "" && filter.ShouldFilter(opts.Query) {
		expectedTitle = strings.TrimSpace(queryParsed.Title)
	}
	if expectedTitle == "" {
		return results
	}

	filterOpts := filter.Options{
		ExpectedTitle:         expectedTitle,
		ExpectedYear:          expectedYear,
		ExpectedCountry:       opts.CountryCode,
		EpisodeAirYear:        opts.EpisodeAirYear,
		IsMovie:               isMovie,
		MaxSizeMovieGB:        models.FloatVal(filterSettings.MaxSizeMovieGB, 0),
		MaxSizeEpisodeGB:      models.FloatVal(filterSettings.MaxSizeEpisodeGB, 0),
		MaxResolution:         models.StringVal(filterSettings.MaxResolution, ""),
		HDRDVPolicy:           filter.HDRDVPolicy(filterSettings.HDRDVPolicy),
		AlternateTitles:       alternateTitles,
		RequiredTerms:         filterSettings.RequiredTerms,
		FilterOutTerms:        filterSettings.FilterOutTerms,
		EpisodeResolver:       opts.EpisodeResolver,
		TargetSeason:          baseParsed.Season,
		TargetEpisode:         baseParsed.Episode,
		TargetAbsoluteEpisode: opts.AbsoluteEpisodeNumber,
		IsDaily:               opts.IsDaily,
		TargetAirDate:         opts.TargetAirDate,
	}

	log.Printf("[indexer/usenet] Applying filter with title=%q, year=%d, isMovie=%t, isDaily=%t, airDate=%q",
		filterOpts.ExpectedTitle, filterOpts.ExpectedYear, filterOpts.IsMovie, filterOpts.IsDaily, filterOpts.TargetAirDate)

	return filter.Results(results, filterOpts)
}

func (s *Service) applyUsenetFiltering(results []models.NZBResult, settings config.Settings, opts SearchOptions, baseParsed, queryParsed debrid.ParsedQuery, alternateTitles []string) []models.NZBResult {
	// Delegate to the new function with settings converted to FilterSettings
	filterSettings := models.FilterSettings{
		MaxSizeMovieGB:   models.FloatPtr(settings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB: models.FloatPtr(settings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:    models.StringPtr(settings.Filtering.MaxResolution),
		HDRDVPolicy:      models.HDRDVPolicy(settings.Filtering.HDRDVPolicy),
		RequiredTerms:    settings.Filtering.RequiredTerms,
		FilterOutTerms:   settings.Filtering.FilterOutTerms,
	}
	return s.applyUsenetFilteringWithSettings(results, opts, baseParsed, queryParsed, alternateTitles, filterSettings)
}

func shouldUseUsenet(mode config.StreamingServiceMode) bool {
	switch strings.ToLower(string(mode)) {
	case "", string(config.StreamingServiceModeUsenet), string(config.StreamingServiceModeHybrid):
		return true
	default:
		return false
	}
}

func shouldUseDebrid(mode config.StreamingServiceMode) bool {
	switch strings.ToLower(string(mode)) {
	case string(config.StreamingServiceModeDebrid), string(config.StreamingServiceModeHybrid):
		return true
	default:
		return false
	}
}

type rssFeed struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	GUID        string        `xml:"guid"`
	Comments    string        `xml:"comments"`
	PubDate     string        `xml:"pubDate"`
	Categories  []string      `xml:"category"`
	Description string        `xml:"description"`
	Enclosure   enclosure     `xml:"enclosure"`
	Attrs       []torznabAttr `xml:"torznab:attr"`
	NewznabAttr []torznabAttr `xml:"newznab:attr"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (s *Service) searchTorznab(ctx context.Context, idx config.IndexerConfig, opts SearchOptions) ([]models.NZBResult, error) {
	breaker := s.providerBreaker
	if breaker == nil {
		breaker = providerbreaker.Shared()
	}
	allowed, blockedUntil, probe := breaker.Allow(idx.Name)
	if !allowed {
		return nil, fmt.Errorf("indexer %s is cooling down after rate limiting until %s", idx.Name, blockedUntil.Format(time.RFC3339))
	}

	apiCallNum := s.usenetAPICallCount.Add(1)
	log.Printf("[search-stats] usenet API call #%d to indexer=%q (query=%q)", apiCallNum, idx.Name, opts.Query)

	// Apply configured search timeout to the request context
	if s.cfg != nil {
		if settings, err := s.cfg.Load(); err == nil && settings.Streaming.IndexerTimeoutSec > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(settings.Streaming.IndexerTimeoutSec*float64(time.Second)))
			defer cancel()
		}
	}

	endpoint := strings.TrimSpace(idx.URL)
	if endpoint == "" {
		return nil, fmt.Errorf("indexer %s missing url", idx.Name)
	}

	trimmed := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(strings.ToLower(trimmed), "/api") {
		endpoint = trimmed + "/api"
	} else {
		endpoint = trimmed
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse indexer url: %w", err)
	}

	params := url.Values{}
	params.Set("apikey", idx.APIKey)
	params.Set("t", "search")
	if opts.Query != "" {
		// Sanitize query to remove special characters that break newznab/torznab searches
		sanitizedQuery := sanitizeNewznabQuery(opts.Query)
		params.Set("q", sanitizedQuery)
		if sanitizedQuery != opts.Query {
			log.Printf("[indexer/newznab] sanitized query for %s: %q -> %q", idx.Name, opts.Query, sanitizedQuery)
		}
	}
	// Use indexer-specific categories if configured, otherwise fall back to search options
	if cats := strings.TrimSpace(idx.Categories); cats != "" {
		params.Set("cat", cats)
		log.Printf("[indexer/newznab] using configured categories for %s: %s", idx.Name, cats)
	} else if len(opts.Categories) > 0 {
		params.Set("cat", strings.Join(opts.Categories, ","))
	}

	searchURL := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: path.Join(u.Path, "")}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return nil, err
	}
	httpheaders.SetIndexerSearchHeaders(req)
	req.URL.RawQuery = params.Encode()

	resp, err := apiusage.Do(s.httpc, idx.Name, "Newznab search", req)
	if err != nil {
		breaker.ReleaseProbe(idx.Name, probe)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusTooManyRequests {
			until := breaker.RecordRateLimit(idx.Name, providerbreaker.RetryHint(resp.Header, time.Now()))
			log.Printf("[provider-breaker] opened provider=%q after search 429 until=%s", idx.Name, until.Format(time.RFC3339))
		} else {
			breaker.ReleaseProbe(idx.Name, probe)
		}
		return nil, fmt.Errorf("torznab %s search failed: %s: %s", idx.Name, resp.Status, strings.TrimSpace(string(body)))
	}
	breaker.RecordSuccess(idx.Name, probe)

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Sanitize malformed XML: escape unescaped ampersands that break strict XML parsers.
	// Some indexers (especially via NZBHydra2) return titles like "Tom & Jerry" instead of "Tom &amp; Jerry".
	sanitized, fixCount := sanitizeXMLAmpersands(buf)
	if fixCount > 0 {
		log.Printf("[indexer/torznab] sanitized %d unescaped ampersand(s) in XML response from %s", fixCount, idx.Name)
	}

	var feed rssFeed
	if err := xml.Unmarshal(sanitized, &feed); err != nil {
		// Log a snippet of the problematic XML for debugging
		snippet := sanitized
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		log.Printf("[indexer/torznab] XML parse error from %s: %v\nXML snippet: %s", idx.Name, err, string(snippet))
		return nil, fmt.Errorf("decode torznab feed: %w", err)
	}

	results := make([]models.NZBResult, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		attrs := make(map[string]string)
		for _, a := range item.Attrs {
			attrs[strings.ToLower(a.Name)] = a.Value
		}
		for _, a := range item.NewznabAttr {
			attrs[strings.ToLower(a.Name)] = a.Value
		}

		size := parseSize(attrs["size"], item.Enclosure.Length)
		published := parsePubDate(item.PubDate)

		// Torznab indexers serve torrents that go through debrid, not usenet
		svcType := models.ServiceTypeUnknown
		if strings.EqualFold(strings.TrimSpace(idx.Type), "torznab") {
			svcType = models.ServiceTypeDebrid
		}

		result := models.NZBResult{
			Title:       item.Title,
			Indexer:     idx.Name,
			GUID:        item.GUID,
			Link:        item.Link,
			DownloadURL: pickDownloadURL(item, attrs),
			SizeBytes:   size,
			PublishDate: published,
			Categories:  dedupe(append([]string{}, item.Categories...)),
			Attributes:  attrs,
			ServiceType: svcType,
		}
		results = append(results, result)
	}

	if len(results) == 0 && opts.EpisodeReleased {
		parsed := debrid.ParseQuery(opts.Query)
		if parsed.Season > 0 && parsed.Episode > 0 &&
			(strings.EqualFold(strings.TrimSpace(opts.MediaType), "series") || parsed.MediaType == debrid.MediaTypeSeries) {
			fallbackOpts := opts
			fallbackOpts.Query = fmt.Sprintf("%s S%02d", parsed.Title, parsed.Season)
			// The fallback query has no episode component, but clear this flag as
			// an explicit recursion guard if parsing rules change later.
			fallbackOpts.EpisodeReleased = false
			log.Printf("[indexer/torznab] %s returned no results for %q; retrying released episode with season query %q",
				idx.Name, opts.Query, fallbackOpts.Query)
			return s.searchTorznab(ctx, idx, fallbackOpts)
		}
	}

	return results, nil
}

func pickDownloadURL(item rssItem, attrs map[string]string) string {
	if item.Enclosure.URL != "" {
		return item.Enclosure.URL
	}
	if link, ok := attrs["magneturl"]; ok {
		return link
	}
	return item.Link
}

func parseSize(attrSize, enclosureLength string) int64 {
	if attrSize != "" {
		if v, err := strconv.ParseInt(attrSize, 10, 64); err == nil {
			return v
		}
	}
	if enclosureLength != "" {
		if v, err := strconv.ParseInt(enclosureLength, 10, 64); err == nil {
			return v
		}
	}
	return 0
}

func parsePubDate(pubDate string) time.Time {
	layouts := []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, pubDate); err == nil {
			return t
		}
	}
	return time.Time{}
}

func dedupe(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		normalized := strings.TrimSpace(item)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

// extractResolutionFromResult extracts resolution from an NZBResult.
// It first checks the "resolution" attribute (set by scrapers like AIOStreams),
// then falls back to parsing the title.
func extractResolutionFromResult(result models.NZBResult) int {
	// First check the resolution attribute (set by AIOStreams and other scrapers)
	if resAttr := result.Attributes["resolution"]; resAttr != "" {
		res := parseResolutionString(resAttr)
		if res > 0 {
			return res
		}
	}
	// Fall back to extracting from title
	return extractResolution(result.Title)
}

// parseResolutionString converts a resolution string like "2160p", "1080p", "4K" to a numeric value.
func parseResolutionString(res string) int {
	switch {
	case resolution2160Pattern.MatchString(res):
		return 2160
	case resolution1080Pattern.MatchString(res):
		return 1080
	case resolution720Pattern.MatchString(res):
		return 720
	case resolution576Pattern.MatchString(res):
		return 576
	case resolution480Pattern.MatchString(res):
		return 480
	default:
		return 0
	}
}

// extractResolution extracts resolution from the title using simple regex patterns.
// Returns a numeric value representing resolution priority (higher is better).
// Common resolutions: 2160p/4K (2160), 1080p (1080), 720p (720), 480p (480), etc.
func extractResolution(title string) int {
	// Check for 4K/UHD (highest priority)
	if resolution2160Pattern.MatchString(title) {
		return 2160
	}
	// Check for 1080p
	if resolution1080Pattern.MatchString(title) {
		return 1080
	}
	// Check for 720p
	if resolution720Pattern.MatchString(title) {
		return 720
	}
	// Check for 576p (PAL)
	if resolution576Pattern.MatchString(title) {
		return 576
	}
	// Check for 480p (NTSC)
	if resolution480Pattern.MatchString(title) {
		return 480
	}

	// Default (no resolution detected)
	return 0
}

// isOnlyAIOStreamsEnabled returns true if AIOStreams is the only enabled scraper in the config.
func isOnlyAIOStreamsEnabled(scrapers []config.TorrentScraperConfig) bool {
	aioEnabled := false
	otherEnabled := false

	for _, s := range scrapers {
		if !s.Enabled {
			continue
		}
		if strings.ToLower(strings.TrimSpace(s.Type)) == "aiostreams" {
			aioEnabled = true
		} else {
			otherEnabled = true
		}
	}

	return aioEnabled && !otherEnabled
}

func shouldBypassAIOStreamsRanking(settings config.Settings, overrides effectiveOverrides, includeUsenet bool) bool {
	return models.BoolVal(overrides.BypassFilteringForAIOStreamsOnly, false) &&
		isOnlyAIOStreamsEnabled(settings.TorrentScrapers) &&
		!includeUsenet
}

// markRankingBypassed flags a result so consumers know mediastorm did not score/rank it
// (an external scraper such as AIOStreams provided the ordering). The TotalScore on these
// results is meaningless (always 0), so UIs should hide it rather than display "Score 0".
func markRankingBypassed(r models.NZBResult) models.NZBResult {
	if r.Attributes == nil {
		r.Attributes = map[string]string{}
	}
	r.Attributes["ranking_bypassed"] = "true"
	return r
}

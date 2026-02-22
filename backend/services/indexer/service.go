package indexer

import (
	"context"
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
	"time"
	"unicode"

	"novastream/config"
	"novastream/models"
	"novastream/services/debrid"
	"novastream/utils/filter"
	"novastream/utils/language"

	"github.com/mozillazg/go-unidecode"
)

// newznabQuerySanitizer removes special characters that interfere with newznab/torznab search APIs.
// Characters like !, ?, :, &, etc. are often interpreted as search operators or cause empty results.
var newznabQuerySanitizer = regexp.MustCompile(`[!?:&'"()[\]{}]+`)

// xmlEntityPattern matches valid XML entity references: &name; &#NNN; &#xHHH;
var xmlEntityPattern = regexp.MustCompile(`^([a-zA-Z]+;|#[0-9]+;|#x[0-9a-fA-F]+;)`)

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
	// Remove problematic special characters
	cleaned := newznabQuerySanitizer.ReplaceAllString(query, " ")
	// Collapse multiple spaces into one and trim
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return cleaned
}

// userSettingsProvider retrieves per-user settings.
type userSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

// clientSettingsProvider retrieves per-client filter settings.
type clientSettingsProvider interface {
	Get(clientID string) (*models.ClientFilterSettings, error)
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
)

type Service struct {
	cfg            *config.Manager
	httpc          *http.Client
	debrid         debridSearchService
	debridPlayback debridPlaybackService
	metadata       metadataSearchService
	userSettings   userSettingsProvider
	clientSettings clientSettingsProvider
}

func NewService(cfg *config.Manager, metadataSvc metadataSearchService, debridSvc debridSearchService) *Service {
	if debridSvc == nil {
		debridSvc = debrid.NewSearchService(cfg)
	}
	return &Service{
		cfg:            cfg,
		httpc:          &http.Client{Timeout: 20 * time.Second},
		debrid:         debridSvc,
		debridPlayback: debrid.NewPlaybackService(cfg, nil),
		metadata:       metadataSvc,
	}
}

// SetUserSettingsProvider sets the user settings provider for per-user filtering.
func (s *Service) SetUserSettingsProvider(provider userSettingsProvider) {
	s.userSettings = provider
}

// SetClientSettingsProvider sets the client settings provider for per-client filtering.
func (s *Service) SetClientSettingsProvider(provider clientSettingsProvider) {
	s.clientSettings = provider
}

// getEffectiveFilterSettings returns the filtering settings to use for a search.
// Settings cascade: Global -> Profile -> Client (client settings win)
func (s *Service) getEffectiveFilterSettings(userID, clientID string, globalSettings config.Settings) models.FilterSettings {
	// Start with global settings (as pointers)
	filterSettings := models.FilterSettings{
		MaxSizeMovieGB:         models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB:       models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:          globalSettings.Filtering.MaxResolution,
		HDRDVPolicy:            models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
		PrioritizeHdr:          models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
		FilterOutTerms:         globalSettings.Filtering.FilterOutTerms,
		PreferredTerms:         globalSettings.Filtering.PreferredTerms,
		NonPreferredTerms:      globalSettings.Filtering.NonPreferredTerms,
		AnimeLanguageEnabled:   models.BoolPtr(globalSettings.Filtering.AnimeLanguageEnabled),
		AnimePreferredLanguage: models.StringPtr(globalSettings.Filtering.AnimePreferredLanguage),
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
			if profileFiltering.MaxResolution != "" {
				filterSettings.MaxResolution = profileFiltering.MaxResolution
			}
			if profileFiltering.HDRDVPolicy != "" {
				filterSettings.HDRDVPolicy = profileFiltering.HDRDVPolicy
			}
			if profileFiltering.PrioritizeHdr != nil {
				filterSettings.PrioritizeHdr = profileFiltering.PrioritizeHdr
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
			if profileFiltering.BypassFilteringForAIOStreamsOnly != nil {
				filterSettings.BypassFilteringForAIOStreamsOnly = profileFiltering.BypassFilteringForAIOStreamsOnly
			}
			if profileFiltering.AnimeLanguageEnabled != nil {
				filterSettings.AnimeLanguageEnabled = profileFiltering.AnimeLanguageEnabled
			}
			if profileFiltering.AnimePreferredLanguage != nil {
				filterSettings.AnimePreferredLanguage = profileFiltering.AnimePreferredLanguage
			}
		}
	}

	// Layer 3: Client settings override profile (field-by-field, only if set)
	if clientID != "" && s.clientSettings != nil {
		clientSettings, err := s.clientSettings.Get(clientID)
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
				filterSettings.MaxResolution = *clientSettings.MaxResolution
			}
			if clientSettings.HDRDVPolicy != nil {
				filterSettings.HDRDVPolicy = *clientSettings.HDRDVPolicy
			}
			if clientSettings.PrioritizeHdr != nil {
				filterSettings.PrioritizeHdr = clientSettings.PrioritizeHdr
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
			if clientSettings.AnimeLanguageEnabled != nil {
				filterSettings.AnimeLanguageEnabled = clientSettings.AnimeLanguageEnabled
			}
			if clientSettings.AnimePreferredLanguage != nil {
				filterSettings.AnimePreferredLanguage = clientSettings.AnimePreferredLanguage
			}
		}
	}

	return filterSettings
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
		clientSettings, err := s.clientSettings.Get(clientID)
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
	iHas := filter.MatchesAnyTerm(i.Title, terms)
	jHas := filter.MatchesAnyTerm(j.Title, terms)
	if iHas && !jHas {
		return -1
	}
	if !iHas && jHas {
		return 1
	}
	return 0
}

func compareNonPreferredTerms(i, j models.NZBResult, terms []filter.CompiledTerm) int {
	if len(terms) == 0 {
		return 0
	}
	iHas := filter.MatchesAnyTerm(i.Title, terms)
	jHas := filter.MatchesAnyTerm(j.Title, terms)
	if iHas && !jHas {
		return 1 // i has non-preferred term → sort lower
	}
	if !iHas && jHas {
		return -1 // j has non-preferred term → i sorts higher
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

func compareHDR(i, j models.NZBResult, prioritizeHdr bool) int {
	if !prioritizeHdr {
		return 0
	}
	iHasHDR := i.Attributes["hdr"] != ""
	jHasHDR := j.Attributes["hdr"] != ""
	iHasDV := i.Attributes["hasDV"] == "true"
	jHasDV := j.Attributes["hasDV"] == "true"

	// DV > HDR > SDR
	if iHasDV && !jHasDV {
		return -1
	}
	if !iHasDV && jHasDV {
		return 1
	}
	if iHasHDR && !jHasHDR {
		return -1
	}
	if !iHasHDR && jHasHDR {
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
	if i.SizeBytes > j.SizeBytes {
		return -1
	}
	if i.SizeBytes < j.SizeBytes {
		return 1
	}
	return 0
}

type SearchOptions struct {
	Query                 string
	Categories            []string
	MaxResults            int
	IMDBID                string
	MediaType             string                      // "movie" or "series"
	Year                  int                         // Release year (for movies)
	UserID                string                      // Optional: user ID for per-user filtering settings
	ClientID              string                      // Optional: client ID for per-client filtering settings
	TotalSeriesEpisodes   int                         // Deprecated: use EpisodeResolver instead
	EpisodeResolver       filter.EpisodeCountResolver // Optional: resolver for accurate episode counts from metadata
	AbsoluteEpisodeNumber int                         // Optional: absolute episode number for anime (e.g., 1153 for One Piece)
	IsAnime               bool                        // True for anime content - requires waiting for Nyaa scraper
	IsDaily               bool                        // True for daily shows (talk shows, news) that use date-based naming
	TargetAirDate         string                      // For daily shows: air date in YYYY-MM-DD format
}

func (s *Service) Search(ctx context.Context, opts SearchOptions) ([]models.NZBResult, error) {
	searchStart := time.Now()
	log.Printf("[indexer] TIMING: Search started for query=%q mediaType=%q", opts.Query, opts.MediaType)

	if s.cfg == nil {
		return nil, errors.New("config manager not configured")
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	// Get effective filtering settings (cascade: global -> profile -> client)
	filterSettings := s.getEffectiveFilterSettings(opts.UserID, opts.ClientID, settings)

	includeUsenet := shouldUseUsenet(settings.Streaming.ServiceMode)
	includeDebrid := shouldUseDebrid(settings.Streaming.ServiceMode)

	alternateTitles := s.resolveAlternateTitles(ctx, opts)
	if len(alternateTitles) > 0 {
		log.Printf("[indexer] resolved %d alternate title(s) for %q: %v", len(alternateTitles), opts.Query, alternateTitles)
	}

	parsedQuery := debrid.ParseQuery(opts.Query)
	searchQueries := buildSearchQueries(opts, parsedQuery, alternateTitles)

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
			usenetResults, err := s.searchUsenetWithFilter(ctx, settings, opts, parsedQuery, alternateTitles, searchQueries, filterSettings)
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

	// Check if ranking should be bypassed for AIOStreams-only mode
	// Only bypass when: setting is enabled, AIOStreams is the only scraper, and no usenet results are mixed in
	bypassRanking := settings.Filtering.BypassFilteringForAIOStreamsOnly &&
		isOnlyAIOStreamsEnabled(settings.TorrentScrapers) &&
		!includeUsenet

	if bypassRanking {
		log.Printf("[indexer] Bypassing strmr ranking - AIOStreams is the only enabled scraper and bypass setting is enabled")
	} else {
		// Get effective ranking criteria (cascade: global -> profile -> client)
		rankingCriteria := s.getEffectiveRankingCriteria(opts.UserID, opts.ClientID, settings)
		log.Printf("[indexer] Sorting %d results with %d ranking criteria, ServicePriority=%q", len(aggregated), len(rankingCriteria), settings.Streaming.ServicePriority)

		// Cache settings needed for comparison functions
		servicePriority := settings.Streaming.ServicePriority
		preferredTerms := filter.CompileTerms(filterSettings.PreferredTerms)
		nonPreferredTerms := filter.CompileTerms(filterSettings.NonPreferredTerms)

		// Inject anime language terms when enabled and content is anime
		if opts.IsAnime && models.BoolVal(filterSettings.AnimeLanguageEnabled, false) {
			langCode := ""
			if filterSettings.AnimePreferredLanguage != nil {
				langCode = *filterSettings.AnimePreferredLanguage
			}
			if langCode == "" {
				langCode = "eng"
			}
			animePref, animeNonPref := filter.GetAnimeLanguageTerms(langCode)
			if len(animePref) > 0 {
				preferredTerms = append(preferredTerms, filter.CompileTerms(animePref)...)
			}
			if len(animeNonPref) > 0 {
				nonPreferredTerms = append(nonPreferredTerms, filter.CompileTerms(animeNonPref)...)
			}
			log.Printf("[indexer] Anime language preference enabled (lang=%s): injected %d preferred + %d non-preferred terms", langCode, len(animePref), len(animeNonPref))
		}

		prioritizeHdr := models.BoolVal(filterSettings.PrioritizeHdr, false)
		preferredLang := settings.Metadata.Language

		sort.SliceStable(aggregated, func(i, j int) bool {
			for _, criterion := range rankingCriteria {
				if !criterion.Enabled {
					continue
				}

				var result int
				switch criterion.ID {
				case config.RankingServicePriority:
					result = compareServicePriority(aggregated[i], aggregated[j], servicePriority)
				case config.RankingPreferredTerms:
					result = comparePreferredTerms(aggregated[i], aggregated[j], preferredTerms)
				case config.RankingNonPreferredTerms:
					result = compareNonPreferredTerms(aggregated[i], aggregated[j], nonPreferredTerms)
				case config.RankingResolution:
					result = compareResolution(aggregated[i], aggregated[j])
				case config.RankingHDR:
					result = compareHDR(aggregated[i], aggregated[j], prioritizeHdr)
				case config.RankingLanguage:
					result = compareLanguage(aggregated[i], aggregated[j], preferredLang)
				case config.RankingSize:
					result = compareSize(aggregated[i], aggregated[j])
				}

				if result != 0 {
					return result < 0
				}
			}
			return false
		})
	}

	// Debug: log top results after sorting
	for idx := 0; idx < len(aggregated) && idx < 5; idx++ {
		res := extractResolutionFromResult(aggregated[idx])
		log.Printf("[indexer] Result #%d: ServiceType=%q Resolution=%d Size=%d Title=%q", idx, aggregated[idx].ServiceType, res, aggregated[idx].SizeBytes, aggregated[idx].Title)
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
	return aggregated, nil
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

	filterSettings := s.getEffectiveFilterSettings(opts.UserID, opts.ClientID, settings)
	alternateTitles := s.resolveAlternateTitles(ctx, opts)
	parsedQuery := debrid.ParseQuery(opts.Query)
	searchQueries := buildSearchQueries(opts, parsedQuery, alternateTitles)

	includeUsenet := shouldUseUsenet(settings.Streaming.ServiceMode)
	includeDebrid := shouldUseDebrid(settings.Streaming.ServiceMode)

	// Prepare ranking criteria and settings for sorting (same as main Search function)
	rankingCriteria := s.getEffectiveRankingCriteria(opts.UserID, opts.ClientID, settings)
	servicePriority := settings.Streaming.ServicePriority
	preferredTerms := filter.CompileTerms(filterSettings.PreferredTerms)
	nonPreferredTerms := filter.CompileTerms(filterSettings.NonPreferredTerms)

	// Inject anime language terms when enabled and content is anime
	if opts.IsAnime && models.BoolVal(filterSettings.AnimeLanguageEnabled, false) {
		langCode := ""
		if filterSettings.AnimePreferredLanguage != nil {
			langCode = *filterSettings.AnimePreferredLanguage
		}
		if langCode == "" {
			langCode = "eng"
		}
		animePref, animeNonPref := filter.GetAnimeLanguageTerms(langCode)
		if len(animePref) > 0 {
			preferredTerms = append(preferredTerms, filter.CompileTerms(animePref)...)
		}
		if len(animeNonPref) > 0 {
			nonPreferredTerms = append(nonPreferredTerms, filter.CompileTerms(animeNonPref)...)
		}
		log.Printf("[indexer] Anime language preference enabled (lang=%s): injected %d preferred + %d non-preferred terms", langCode, len(animePref), len(animeNonPref))
	}

	prioritizeHdr := models.BoolVal(filterSettings.PrioritizeHdr, false)
	preferredLang := settings.Metadata.Language

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
		sort.SliceStable(results, func(i, j int) bool {
			for _, criterion := range rankingCriteria {
				if !criterion.Enabled {
					continue
				}
				var result int
				switch criterion.ID {
				case config.RankingServicePriority:
					result = compareServicePriority(results[i], results[j], servicePriority)
				case config.RankingPreferredTerms:
					result = comparePreferredTerms(results[i], results[j], preferredTerms)
				case config.RankingNonPreferredTerms:
					result = compareNonPreferredTerms(results[i], results[j], nonPreferredTerms)
				case config.RankingResolution:
					result = compareResolution(results[i], results[j])
				case config.RankingHDR:
					result = compareHDR(results[i], results[j], prioritizeHdr)
				case config.RankingLanguage:
					result = compareLanguage(results[i], results[j], preferredLang)
				case config.RankingSize:
					result = compareSize(results[i], results[j])
				}
				if result != 0 {
					return result < 0
				}
			}
			return false
		})
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

		usenetResults, err := s.searchUsenetWithFilter(ctx, settings, opts, parsedQuery, alternateTitles, searchQueries, filterSettings)
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

func (s *Service) resolveAlternateTitles(ctx context.Context, opts SearchOptions) []string {
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

	seen := make(map[string]struct{})
	var aliases []string
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		lowered := strings.ToLower(trimmed)
		if _, exists := seen[lowered]; exists {
			return
		}
		seen[lowered] = struct{}{}
		aliases = append(aliases, trimmed)
	}
	add(chosen.OriginalName)
	for _, alt := range chosen.AlternateTitles {
		add(alt)
	}

	if len(aliases) == 0 {
		return nil
	}
	return aliases
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

	// Add the original query
	addQuery(opts.Query)

	// Add S##E## variants (for non-daily shows, or as fallback for daily shows)
	addVariants := func(title string) {
		for _, variant := range titleVariants(title) {
			composed := composeQueryForSearch(variant, opts, parsed)
			addQuery(composed)
		}
	}

	addVariants(parsed.Title)
	for _, alt := range alternateTitles {
		addVariants(alt)
	}

	return queries
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
		"–", "-", "—", "-", "−", "-",
		"•", " ", "…", " ", "：", ":",
		"，", ",", "！", "!", "？", "?",
		"’", "'", "、", " ",
	)
	ascii := strings.TrimSpace(unidecode.Unidecode(value))
	ascii = replacer.Replace(ascii)
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

	// Collect results, preferring higher priority (lower index) results
	var bestResult *searchResult
	var lastErr error
	resultsReceived := 0

	for resultsReceived < len(validQueries) {
		select {
		case <-ctx.Done():
			if bestResult != nil && len(bestResult.results) > 0 {
				return bestResult.results, nil
			}
			return nil, ctx.Err()
		case res := <-resultsChan:
			resultsReceived++

			if res.err != nil {
				lastErr = res.err
				continue
			}

			if len(res.results) == 0 {
				continue
			}

			// Keep track of best result (lowest priority number = primary query)
			if bestResult == nil || res.priority < bestResult.priority {
				bestResult = &res
				log.Printf("[indexer/usenet] got %d results from query %q (priority %d)", len(res.results), res.query, res.priority)

				// If we got results from the primary query, we can cancel other searches
				if res.priority == 0 {
					cancel()
				}
			}
		}
	}

	if bestResult != nil && len(bestResult.results) > 0 {
		return bestResult.results, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return []models.NZBResult{}, nil
}

func (s *Service) searchUsenet(ctx context.Context, settings config.Settings, opts SearchOptions, baseParsed debrid.ParsedQuery, alternateTitles []string, searchQueries []string) ([]models.NZBResult, error) {
	// Use global settings for backwards compatibility
	filterSettings := models.FilterSettings{
		MaxSizeMovieGB:   models.FloatPtr(settings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB: models.FloatPtr(settings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:    settings.Filtering.MaxResolution,
		HDRDVPolicy:      models.HDRDVPolicy(settings.Filtering.HDRDVPolicy),
		PrioritizeHdr:    models.BoolPtr(settings.Filtering.PrioritizeHdr),
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
	var allResults []models.NZBResult
	var lastErr error

	for _, idx := range settings.Indexers {
		if !idx.Enabled {
			continue
		}

		switch strings.ToLower(strings.TrimSpace(idx.Type)) {
		case "", "newznab", "torznab":
			results, err := s.searchTorznab(ctx, idx, opts)
			if err != nil {
				lastErr = err
				continue
			}
			allResults = append(allResults, results...)
		default:
			lastErr = fmt.Errorf("unsupported indexer type %q", idx.Type)
		}

		if opts.MaxResults > 0 && len(allResults) >= opts.MaxResults {
			break
		}
	}

	if len(allResults) == 0 && lastErr != nil {
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
		ExpectedTitle:    expectedTitle,
		ExpectedYear:     expectedYear,
		IsMovie:          isMovie,
		MaxSizeMovieGB:   models.FloatVal(filterSettings.MaxSizeMovieGB, 0),
		MaxSizeEpisodeGB: models.FloatVal(filterSettings.MaxSizeEpisodeGB, 0),
		MaxResolution:    filterSettings.MaxResolution,
		HDRDVPolicy:      filter.HDRDVPolicy(filterSettings.HDRDVPolicy),
		PrioritizeHdr:    models.BoolVal(filterSettings.PrioritizeHdr, false),
		AlternateTitles:  alternateTitles,
		FilterOutTerms:   filterSettings.FilterOutTerms,
		IsDaily:          opts.IsDaily,
		TargetAirDate:    opts.TargetAirDate,
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
		MaxResolution:    settings.Filtering.MaxResolution,
		HDRDVPolicy:      models.HDRDVPolicy(settings.Filtering.HDRDVPolicy),
		PrioritizeHdr:    models.BoolPtr(settings.Filtering.PrioritizeHdr),
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
	req.URL.RawQuery = params.Encode()

	resp, err := s.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("torznab %s search failed: %s: %s", idx.Name, resp.Status, strings.TrimSpace(string(body)))
	}

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
		}
		results = append(results, result)
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
	res = strings.ToLower(strings.TrimSpace(res))
	switch {
	case strings.Contains(res, "2160") || strings.Contains(res, "4k") || strings.Contains(res, "uhd"):
		return 2160
	case strings.Contains(res, "1080"):
		return 1080
	case strings.Contains(res, "720"):
		return 720
	case strings.Contains(res, "576"):
		return 576
	case strings.Contains(res, "480"):
		return 480
	default:
		return 0
	}
}

// extractResolution extracts resolution from the title using simple regex patterns.
// Returns a numeric value representing resolution priority (higher is better).
// Common resolutions: 2160p/4K (2160), 1080p (1080), 720p (720), 480p (480), etc.
func extractResolution(title string) int {
	title = strings.ToLower(title)

	// Check for 4K/UHD (highest priority)
	if strings.Contains(title, "2160p") || strings.Contains(title, "4k") || strings.Contains(title, "uhd") {
		return 2160
	}
	// Check for 1080p
	if strings.Contains(title, "1080p") || strings.Contains(title, "1080i") {
		return 1080
	}
	// Check for 720p
	if strings.Contains(title, "720p") || strings.Contains(title, "720i") {
		return 720
	}
	// Check for 576p (PAL)
	if strings.Contains(title, "576p") || strings.Contains(title, "576i") {
		return 576
	}
	// Check for 480p (NTSC)
	if strings.Contains(title, "480p") || strings.Contains(title, "480i") {
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
		if strings.ToLower(s.Type) == "aiostreams" {
			aioEnabled = true
		} else {
			otherEnabled = true
		}
	}

	return aioEnabled && !otherEnabled
}


package debrid

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/utils/filter"
)

// userSettingsProvider retrieves per-user settings.
type userSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

// clientSettingsProvider retrieves per-client filter settings.
type clientSettingsProvider interface {
	Get(clientID string) (*models.ClientFilterSettings, error)
}

// imdbResolver resolves IMDB IDs from title metadata when not available from primary sources.
type imdbResolver interface {
	ResolveIMDBID(ctx context.Context, title string, mediaType string, year int) string
}

// SearchOptions mirrors the indexer search contract but is scoped for debrid providers.
type SearchOptions struct {
	Query               string
	Categories          []string
	MaxResults          int
	IMDBID              string                       // Optional IMDB ID to bypass metadata search
	MediaType           string                       // Optional: "movie" or "series" - helps with filtering
	Year                int                          // Optional: Release year - helps with filtering
	AlternateTitles     []string                     // Optional: alternate or foreign titles for fuzzy filtering
	UserID              string                       // Optional: user ID for per-user filtering settings
	ClientID            string                       // Optional: client ID for per-client filtering settings
	TotalSeriesEpisodes int                          // Deprecated: use EpisodeResolver instead
	EpisodeResolver     filter.EpisodeCountResolver  // Optional: resolver for accurate episode counts from metadata
}

// SearchService coordinates queries against configured debrid providers.
type SearchService struct {
	cfg            *config.Manager
	scrapers       []Scraper
	userSettings   userSettingsProvider
	clientSettings clientSettingsProvider
	imdbResolver   imdbResolver
}

// NewSearchService constructs a new debrid search service.
// If no scrapers are provided, it builds them from config settings.
func NewSearchService(cfg *config.Manager, scrapers ...Scraper) *SearchService {
	if len(scrapers) == 0 {
		scrapers = buildScrapersFromConfig(cfg)
	}
	if len(scrapers) == 0 {
		// Fallback to torrentio if no scrapers configured
		scrapers = []Scraper{NewTorrentioScraper(nil, "", "")}
	}
	return &SearchService{
		cfg:      cfg,
		scrapers: scrapers,
	}
}

// buildScrapersFromConfig creates scrapers based on torrentScrapers config.
func buildScrapersFromConfig(cfg *config.Manager) []Scraper {
	if cfg == nil {
		return nil
	}
	settings, err := cfg.Load()
	if err != nil {
		log.Printf("[debrid] failed to load config for scrapers: %v", err)
		return nil
	}

	var scrapers []Scraper
	for _, scraperCfg := range settings.TorrentScrapers {
		if !scraperCfg.Enabled {
			continue
		}
		switch strings.ToLower(scraperCfg.Type) {
		case "torrentio":
			log.Printf("[debrid] Initializing Torrentio scraper: %s (options: %s)", scraperCfg.Name, scraperCfg.Options)
			scrapers = append(scrapers, NewTorrentioScraper(nil, scraperCfg.Options, scraperCfg.Name))
		case "jackett":
			if scraperCfg.URL == "" || scraperCfg.APIKey == "" {
				log.Printf("[debrid] Skipping Jackett scraper %s: missing URL or API key", scraperCfg.Name)
				continue
			}
			log.Printf("[debrid] Initializing Jackett scraper: %s at %s", scraperCfg.Name, scraperCfg.URL)
			scrapers = append(scrapers, NewJackettScraper(scraperCfg.URL, scraperCfg.APIKey, scraperCfg.Name, nil))
		case "zilean":
			if scraperCfg.URL == "" {
				log.Printf("[debrid] Skipping Zilean scraper %s: missing URL", scraperCfg.Name)
				continue
			}
			log.Printf("[debrid] Initializing Zilean scraper: %s at %s", scraperCfg.Name, scraperCfg.URL)
			scrapers = append(scrapers, NewZileanScraper(scraperCfg.URL, scraperCfg.Name, nil))
		case "aiostreams":
			if scraperCfg.URL == "" {
				log.Printf("[debrid] Skipping AIOStreams scraper %s: missing URL", scraperCfg.Name)
				continue
			}
			passthroughFormat := scraperCfg.Config["passthroughFormat"] == "true"
			log.Printf("[debrid] Initializing AIOStreams scraper: %s at %s (passthrough=%v)", scraperCfg.Name, scraperCfg.URL, passthroughFormat)
			scrapers = append(scrapers, NewAIOStreamsScraper(scraperCfg.URL, scraperCfg.Name, passthroughFormat, nil))
		default:
			log.Printf("[debrid] Unknown scraper type: %s", scraperCfg.Type)
		}
	}
	return scrapers
}

// SetUserSettingsProvider sets the user settings provider for per-user filtering.
func (s *SearchService) SetUserSettingsProvider(provider userSettingsProvider) {
	s.userSettings = provider
}

// SetClientSettingsProvider sets the client settings provider for per-client filtering.
func (s *SearchService) SetClientSettingsProvider(provider clientSettingsProvider) {
	s.clientSettings = provider
}

// SetIMDBResolver sets the IMDB resolver for fallback ID resolution.
func (s *SearchService) SetIMDBResolver(resolver imdbResolver) {
	s.imdbResolver = resolver
}

// ReloadScrapers rebuilds the scraper list from current config.
// This allows hot reloading when torrent scraper settings change.
func (s *SearchService) ReloadScrapers() {
	scrapers := buildScrapersFromConfig(s.cfg)
	if len(scrapers) == 0 {
		// Fallback to torrentio if no scrapers configured
		scrapers = []Scraper{NewTorrentioScraper(nil, "", "")}
	}
	s.scrapers = scrapers
	log.Printf("[debrid] reloaded %d scraper(s)", len(scrapers))
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

// getEffectiveFilterSettings returns the filtering settings to use for a search.
// Settings cascade: Global -> Profile -> Client (client settings win)
func (s *SearchService) getEffectiveFilterSettings(userID, clientID string, globalSettings config.Settings) models.FilterSettings {
	// Start with global settings (as pointers)
	filterSettings := models.FilterSettings{
		MaxSizeMovieGB:   models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB: models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:    globalSettings.Filtering.MaxResolution,
		HDRDVPolicy:      models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
		PrioritizeHdr:    models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
		FilterOutTerms:   globalSettings.Filtering.FilterOutTerms,
		PreferredTerms:   globalSettings.Filtering.PreferredTerms,
	}

	// Layer 2: Profile settings override global (field-by-field, only if set)
	if userID != "" && s.userSettings != nil {
		userSettings, err := s.userSettings.Get(userID)
		if err != nil {
			log.Printf("[debrid] failed to get user settings for %s: %v", userID, err)
		} else if userSettings != nil {
			log.Printf("[debrid] using per-user filtering settings for user %s", userID)
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
			if profileFiltering.BypassFilteringForAIOStreamsOnly != nil {
				filterSettings.BypassFilteringForAIOStreamsOnly = profileFiltering.BypassFilteringForAIOStreamsOnly
			}
		}
	}

	// Layer 3: Client settings override profile (field-by-field, only if set)
	if clientID != "" && s.clientSettings != nil {
		clientSettings, err := s.clientSettings.Get(clientID)
		if err != nil {
			log.Printf("[debrid] failed to get client settings for %s: %v", clientID, err)
		} else if clientSettings != nil && !clientSettings.IsEmpty() {
			log.Printf("[debrid] applying per-client filtering overrides for client %s", clientID)
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
		}
	}

	return filterSettings
}

// Search executes scraper-backed torrent discovery across enabled debrid providers.
func (s *SearchService) Search(ctx context.Context, opts SearchOptions) ([]models.NZBResult, error) {
	if s == nil || s.cfg == nil {
		return nil, errors.New("debrid search service not configured")
	}

	settings, err := s.cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	// Get effective filtering settings (cascade: global -> profile -> client)
	filterSettings := s.getEffectiveFilterSettings(opts.UserID, opts.ClientID, settings)

	if !hasActiveDebridProviders(settings.Streaming.DebridProviders) {
		return []models.NZBResult{}, nil
	}

	parsed := ParseQuery(opts.Query)

	// Use explicit metadata if provided, otherwise use parsed values
	if opts.MediaType != "" {
		parsed.MediaType = MediaType(strings.ToLower(opts.MediaType))
	}
	if opts.Year > 0 {
		parsed.Year = opts.Year
	}

	log.Printf("[debrid] Search called with Query=%q, IMDBID=%q, MediaType=%q, Year=%d, UserID=%q", opts.Query, opts.IMDBID, opts.MediaType, opts.Year, opts.UserID)

	// If no IMDB ID provided, try to resolve it via metadata service (TVDB fallback)
	imdbID := opts.IMDBID
	if imdbID == "" && s.imdbResolver != nil && parsed.Title != "" {
		resolvedID := s.imdbResolver.ResolveIMDBID(ctx, parsed.Title, string(parsed.MediaType), parsed.Year)
		if resolvedID != "" {
			log.Printf("[debrid] Resolved IMDB ID via fallback: %s for %q", resolvedID, parsed.Title)
			imdbID = resolvedID
		}
	}

	req := SearchRequest{
		Query:      opts.Query,
		Categories: append([]string(nil), opts.Categories...),
		MaxResults: opts.MaxResults,
		Parsed:     parsed,
		IMDBID:     imdbID,
	}
	log.Printf("[debrid] Using metadata: Title=%q, Season=%d, Episode=%d, Year=%d, MediaType=%s, IMDBID=%s",
		parsed.Title, parsed.Season, parsed.Episode, parsed.Year, parsed.MediaType, imdbID)

	// scraperResult holds results from a single scraper
	type scraperResult struct {
		name    string
		results []ScrapeResult
		err     error
		elapsed time.Duration
	}

	// Run all scrapers in parallel
	var wg sync.WaitGroup
	resultsChan := make(chan scraperResult, len(s.scrapers))

	for _, scraper := range s.scrapers {
		if scraper == nil {
			continue
		}
		wg.Add(1)
		go func(sc Scraper) {
			defer wg.Done()
			start := time.Now()
			results, err := sc.Search(ctx, req)
			resultsChan <- scraperResult{
				name:    sc.Name(),
				results: results,
				err:     err,
				elapsed: time.Since(start),
			}
		}(scraper)
	}

	// Wait for all scrapers to complete, then close channel
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results from all scrapers
	var (
		aggregate []models.NZBResult
		errs      []error
		seenGuids = make(map[string]struct{})
	)

	for sr := range resultsChan {
		if sr.err != nil {
			log.Printf("[debrid] %s search failed: %v", sr.name, sr.err)
			errs = append(errs, fmt.Errorf("%s scraper: %w", sr.name, sr.err))
			continue
		}
		log.Printf("[debrid] %s search produced %d results for %q in %s", sr.name, len(sr.results), parsed.Title, sr.elapsed.Round(10*time.Millisecond))
		for _, res := range sr.results {
			nzb := normalizeScrapeResult(res)
			decorateResultWithParsedMetadata(&nzb, req.Parsed)
			if nzb.GUID == "" {
				nzb.GUID = fmt.Sprintf("%s:%s:%d", sr.name, strings.ToLower(res.InfoHash), res.FileIndex)
			}
			if nzb.Indexer == "" {
				nzb.Indexer = sr.name
			}
			if _, dup := seenGuids[nzb.GUID]; dup {
				continue
			}
			seenGuids[nzb.GUID] = struct{}{}
			aggregate = append(aggregate, nzb)
		}
	}

	if len(aggregate) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Check if filtering should be bypassed for AIOStreams-only mode
	bypassFiltering := settings.Filtering.BypassFilteringForAIOStreamsOnly && isOnlyAIOStreamsEnabled(settings.TorrentScrapers)
	if bypassFiltering {
		log.Printf("[debrid] Bypassing strmr filtering - AIOStreams is the only enabled scraper and bypass setting is enabled")
	}

	// Apply parsed-based filtering if appropriate (using per-user filter settings)
	if !bypassFiltering && ShouldFilter(parsed) {
		hasResolver := opts.EpisodeResolver != nil
		log.Printf("[debrid] Applying filter with title=%q, year=%d, mediaType=%s, hasEpisodeResolver=%v", parsed.Title, parsed.Year, parsed.MediaType, hasResolver)
		filterOpts := FilterOptions{
			ExpectedTitle:       parsed.Title,
			ExpectedYear:        parsed.Year,
			MediaType:           parsed.MediaType,
			MaxSizeMovieGB:      models.FloatVal(filterSettings.MaxSizeMovieGB, 0),
			MaxSizeEpisodeGB:    models.FloatVal(filterSettings.MaxSizeEpisodeGB, 0),
			MaxResolution:       filterSettings.MaxResolution,
			HDRDVPolicy:         filter.HDRDVPolicy(filterSettings.HDRDVPolicy),
			PrioritizeHdr:       models.BoolVal(filterSettings.PrioritizeHdr, false),
			AlternateTitles:     opts.AlternateTitles,
			FilterOutTerms:      filterSettings.FilterOutTerms,
			TotalSeriesEpisodes: opts.TotalSeriesEpisodes,
			EpisodeResolver:     opts.EpisodeResolver,
		}
		aggregate = FilterResults(aggregate, filterOpts)
	}

	// Apply MaxResults limit after filtering
	if opts.MaxResults > 0 && len(aggregate) > opts.MaxResults {
		aggregate = aggregate[:opts.MaxResults]
	}

	return aggregate, nil
}

func hasActiveDebridProviders(providers []config.DebridProviderSettings) bool {
	for _, provider := range providers {
		if !provider.Enabled {
			continue
		}
		if strings.TrimSpace(provider.APIKey) == "" {
			continue
		}
		return true
	}
	return false
}

func normalizeScrapeResult(res ScrapeResult) models.NZBResult {
	// Determine Link - prefer magnet, fall back to torrent URL
	link := safeString(res.Magnet)
	if link == "" && res.TorrentURL != "" {
		link = safeString(res.TorrentURL)
	}

	result := models.NZBResult{
		Title:       safeString(res.Title),
		Indexer:     safeString(res.Indexer),
		GUID:        "",
		Link:        link,
		DownloadURL: link,
		SizeBytes:   res.SizeBytes,
		Categories:  nil,
		Attributes:  map[string]string{},
		ServiceType: models.ServiceTypeDebrid,
	}

	if res.InfoHash != "" {
		lowered := strings.ToLower(res.InfoHash)
		result.Attributes["infoHash"] = lowered
		result.GUID = fmt.Sprintf("magnet:%s", lowered)
	}
	// Store torrent URL for downloading .torrent file when no magnet/infohash available
	if res.TorrentURL != "" {
		result.Attributes["torrentURL"] = res.TorrentURL
	}
	if res.FileIndex >= 0 {
		result.Attributes["fileIndex"] = fmt.Sprintf("%d", res.FileIndex)
	}
	if res.Provider != "" {
		result.Attributes["tracker"] = res.Provider
	}
	if res.Resolution != "" {
		result.Attributes["resolution"] = res.Resolution
	}
	if res.Seeders > 0 {
		result.Attributes["seeders"] = fmt.Sprintf("%d", res.Seeders)
	}
	if len(res.Languages) > 0 {
		result.Attributes["languages"] = strings.Join(res.Languages, ",")
	}
	if res.MetaName != "" {
		result.Attributes["titleName"] = res.MetaName
	}
	if res.MetaID != "" {
		result.Attributes["titleId"] = res.MetaID
	}
	if res.Source != "" {
		result.Attributes["source"] = res.Source
	}
	if result.GUID == "" && result.Link != "" {
		result.GUID = result.Link
	}
	for key, value := range res.Attributes {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := result.Attributes[key]; exists {
			continue
		}
		result.Attributes[key] = value
	}
	return result
}

func safeString(value string) string {
	return strings.TrimSpace(value)
}

func decorateResultWithParsedMetadata(result *models.NZBResult, parsed ParsedQuery) {
	if result == nil {
		return
	}
	if result.Attributes == nil {
		result.Attributes = map[string]string{}
	}

	if parsed.Title != "" {
		if _, ok := result.Attributes["targetTitle"]; !ok {
			result.Attributes["targetTitle"] = strings.TrimSpace(parsed.Title)
		}
	}
	if parsed.MediaType != "" {
		if _, ok := result.Attributes["targetMediaType"]; !ok {
			result.Attributes["targetMediaType"] = string(parsed.MediaType)
		}
	}
	if parsed.Season > 0 {
		if _, ok := result.Attributes["targetSeason"]; !ok {
			result.Attributes["targetSeason"] = fmt.Sprintf("%d", parsed.Season)
		}
	}
	if parsed.Episode > 0 {
		if _, ok := result.Attributes["targetEpisode"]; !ok {
			result.Attributes["targetEpisode"] = fmt.Sprintf("%d", parsed.Episode)
		}
	}
	if parsed.Season > 0 && parsed.Episode > 0 {
		code := fmt.Sprintf("S%02dE%02d", parsed.Season, parsed.Episode)
		if _, ok := result.Attributes["targetEpisodeCode"]; !ok {
			result.Attributes["targetEpisodeCode"] = code
		}
	}
}

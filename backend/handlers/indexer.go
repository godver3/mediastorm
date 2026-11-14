package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/debrid"
	"novastream/services/indexer"
	"novastream/services/sourcehealth"
	"novastream/services/trackselect"
	"novastream/utils/filter"
)

type indexerService interface {
	Search(context.Context, indexer.SearchOptions) ([]models.NZBResult, error)
	SearchTest(context.Context, indexer.SearchOptions) ([]models.ScoredNZBResult, error)
	SearchWithScoring(context.Context, indexer.SearchOptions) ([]models.ScoredNZBResult, error)
}

var _ indexerService = (*indexer.Service)(nil)

type IndexerHandler struct {
	Service          indexerService
	MetadataSvc      SeriesDetailsProvider
	MovieMetadataSvc MovieDetailsProvider
	DemoMode         bool
	BadStreams       *badstreams.Service
	TrackPreferences *trackselect.Resolver
	SourceHealthSvc  *sourcehealth.Service
}

func NewIndexerHandler(s indexerService, demoMode bool) *IndexerHandler {
	return &IndexerHandler{Service: s, DemoMode: demoMode}
}

// SetMetadataService sets the metadata service for episode counting
func (h *IndexerHandler) SetMetadataService(svc SeriesDetailsProvider) {
	h.MetadataSvc = svc
}

// SetMovieMetadataService sets the movie metadata service for anime detection
func (h *IndexerHandler) SetMovieMetadataService(svc MovieDetailsProvider) {
	h.MovieMetadataSvc = svc
}

func (h *IndexerHandler) SetBadStreamsService(svc *badstreams.Service) {
	h.BadStreams = svc
}

// SetSourceHealthService enables server-side health/cache annotations for
// searches requested with includeSourceHealth=true.
func (h *IndexerHandler) SetSourceHealthService(svc *sourcehealth.Service) {
	h.SourceHealthSvc = svc
}

func (h *IndexerHandler) Search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	categories := r.URL.Query()["cat"]
	imdbID := strings.TrimSpace(r.URL.Query().Get("imdbId"))
	titleID := strings.TrimSpace(r.URL.Query().Get("titleId"))
	numbering := requestEpisodeNumbering(r)
	mediaType := strings.TrimSpace(r.URL.Query().Get("mediaType"))
	query = normalizeDecoratedSeriesQuery(query, mediaType)
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	// Client ID from header (preferred) or query param
	clientID := strings.TrimSpace(r.Header.Get("X-Client-ID"))
	if clientID == "" {
		clientID = strings.TrimSpace(r.URL.Query().Get("clientId"))
	}
	year := 0
	if rawYear := r.URL.Query().Get("year"); rawYear != "" {
		if parsed, err := strconv.Atoi(rawYear); err == nil && parsed > 0 {
			year = parsed
		}
	}
	max := 5
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			max = parsed
		}
	}

	// Get series metadata for TV shows (episode resolver + daily show detection)
	var episodeResolver *filter.SeriesEpisodeResolver
	var isDaily bool
	var isAnime bool
	var targetAirDate string
	var episodeAirYear int
	var seasonPremiereYear int
	var episodeReleased bool
	var absoluteEpisodeNumber int
	var countryCode string
	var originalLanguage string
	var tvdbID int64
	var alternateTitles []string
	if mediaType == "series" && h.MetadataSvc != nil {
		seriesMeta := h.getSeriesSearchMetadata(r.Context(), query, year, imdbID, seriesNumberingContext{TitleID: titleID, Numbering: numbering})
		if seriesMeta != nil {
			if titleID == "" {
				titleID = seriesMeta.TitleID
			}
			numbering = seriesMeta.Numbering
			episodeResolver = seriesMeta.EpisodeResolver
			isDaily = seriesMeta.IsDaily
			isAnime = seriesMeta.IsAnime
			targetAirDate = seriesMeta.TargetAirDate
			episodeAirYear = seriesMeta.EpisodeAirYear
			seasonPremiereYear = seriesMeta.SeasonPremiereYear
			episodeReleased = seriesMeta.EpisodeReleased
			absoluteEpisodeNumber = seriesMeta.AbsoluteEpisodeNumber
			countryCode = seriesMeta.CountryCode
			originalLanguage = seriesMeta.OriginalLanguage
			tvdbID = seriesMeta.TVDBID
			if year == 0 && seriesMeta.Year > 0 {
				year = seriesMeta.Year
				log.Printf("[indexer] Populated year %d from series metadata", year)
			}
			if episodeResolver != nil {
				log.Printf("[indexer] Episode resolver created: %d total episodes, %d seasons",
					episodeResolver.TotalEpisodes, len(episodeResolver.SeasonEpisodeCounts))
			}
			if isDaily {
				log.Printf("[indexer] Daily show detected, targetAirDate=%s", targetAirDate)
			}
		}
	}

	// Detect anime for movies via movie metadata
	if mediaType == "movie" && h.MovieMetadataSvc != nil {
		movieQuery := models.MovieDetailsQuery{
			Name:   strings.TrimSpace(query),
			Year:   year,
			IMDBID: imdbID,
		}
		if movieTitle, err := h.MovieMetadataSvc.MovieInfo(r.Context(), movieQuery); err == nil && movieTitle != nil {
			countryCode = strings.TrimSpace(movieTitle.CountryCode)
			originalLanguage = strings.TrimSpace(movieTitle.Language)
			// Keep the hydrated canonical title in the filter identity set because
			// the incoming query may itself be an alternate title. Outbound alias
			// queries are capped separately from the complete filter-title set.
			alternateTitles = hydratedMovieSearchTitles(movieTitle)
			if isAnimeTitle(movieTitle) {
				isAnime = true
				log.Printf("[indexer] Movie %q is anime (genres=%v originalName=%q language=%q) - applying anime language preferences",
					query, movieTitle.Genres, movieTitle.OriginalName, movieTitle.Language)
			}
		}
	}

	opts := indexer.SearchOptions{
		Numbering:             numbering,
		TitleID:               titleID,
		Query:                 query,
		Categories:            categories,
		MaxResults:            max,
		IMDBID:                imdbID,
		TVDBID:                tvdbID,
		AlternateTitles:       alternateTitles,
		MediaType:             mediaType,
		Year:                  year,
		CountryCode:           countryCode,
		OriginalLanguage:      originalLanguage,
		UserID:                userID,
		ClientID:              clientID,
		AdaptiveThroughput:    adaptiveThroughputFromRequest(r),
		EpisodeResolver:       episodeResolver,
		IsDaily:               isDaily,
		IsAnime:               isAnime,
		TargetAirDate:         targetAirDate,
		EpisodeAirYear:        episodeAirYear,
		SeasonPremiereYear:    seasonPremiereYear,
		EpisodeReleased:       episodeReleased,
		AbsoluteEpisodeNumber: absoluteEpisodeNumber,
	}

	useDownloadRanking := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("downloadRanking")), "true")
	if useDownloadRanking {
		opts.UseDownloadRanking = true
	}
	// "Download Season" resolves every episode from the one selected release,
	// so single-episode results must not be offered.
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("seasonPacks")), "true") && mediaType == "series" {
		opts.SeasonPacksOnly = true
	}

	// Check if caller wants filtered results included
	includeFiltered := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("includeFiltered"))) == "true"

	if includeFiltered {
		opts.IncludeFiltered = true
		includeSummary := r.URL.Query().Get("includeAdaptiveSummary") == "true"
		if includeSummary {
			opts.AdaptiveSummary = &models.AdaptiveSearchSummary{}
		}
		scored, err := h.Service.SearchWithScoring(r.Context(), opts)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			statusCode, errResponse := classifySearchError(err)
			w.WriteHeader(statusCode)
			json.NewEncoder(w).Encode(errResponse)
			return
		}

		markBadScoredResults(scored, h.BadStreams, reportContextForSearch(h.TrackPreferences, opts))
		if h.DemoMode {
			maskedTitle := buildMaskedTitle(query, year, mediaType)
			for i := range scored {
				scored[i].Title = maskedTitle
				scored[i].Indexer = "Demo"
			}
		}
		annotateScoredResultsProfile(scored, userID, titleID)
		for i := range scored {
			annotateReportEpisode(scored[i].Attributes, opts)
		}

		// Ensure we return [] instead of null for empty results
		if scored == nil {
			scored = []models.ScoredNZBResult{}
		}

		// Opt-in: annotate debrid cache / remembered usenet health. Waits only
		// a short inline budget; unfinished checks come back "pending" with a
		// token for GET /indexers/source-health.
		sourceHealthToken := ""
		if h.SourceHealthSvc != nil && wantsSourceHealth(r) {
			sourceHealthToken = h.SourceHealthSvc.Annotate(r.Context(), scored)
		}

		w.Header().Set("Content-Type", "application/json")
		if includeSummary {
			json.NewEncoder(w).Encode(struct {
				Results           []models.ScoredNZBResult      `json:"results"`
				Adaptive          *models.AdaptiveSearchSummary `json:"adaptive"`
				SourceHealthToken string                        `json:"sourceHealthToken,omitempty"`
			}{scored, opts.AdaptiveSummary, sourceHealthToken})
		} else {
			json.NewEncoder(w).Encode(scored)
		}
		return
	}

	results, err := h.Service.Search(r.Context(), opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		statusCode, errResponse := classifySearchError(err)
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(errResponse)
		return
	}

	// Ensure we return [] instead of null for empty results
	if results == nil {
		results = []models.NZBResult{}
	}
	if h.BadStreams != nil {
		ctx := reportContextForSearch(h.TrackPreferences, opts)
		filtered := make([]models.NZBResult, 0, len(results))
		for _, result := range results {
			if h.BadStreams.MatchFor(result, ctx) == nil {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}

	// In demo mode, mask actual filenames with the search query info
	if h.DemoMode {
		maskedTitle := buildMaskedTitle(query, year, mediaType)
		for i := range results {
			results[i].Title = maskedTitle
			results[i].Indexer = "Demo"
		}
	}
	annotateResultsProfile(results, userID, titleID)
	for i := range results {
		annotateReportEpisode(results[i].Attributes, opts)
	}

	w.Header().Set("Content-Type", "application/json")
	if h.SourceHealthSvc != nil && wantsSourceHealth(r) {
		json.NewEncoder(w).Encode(h.annotatePlainResults(r.Context(), results))
		return
	}
	json.NewEncoder(w).Encode(results)
}

func wantsSourceHealth(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("includeSourceHealth")), "true")
}

// annotatedNZBResult is the unscored search result shape plus the optional
// source health annotation (keeps the response free of scoring fields).
type annotatedNZBResult struct {
	models.NZBResult
	SourceHealth *models.SourceHealth `json:"sourceHealth,omitempty"`
}

func (h *IndexerHandler) annotatePlainResults(ctx context.Context, results []models.NZBResult) []annotatedNZBResult {
	scored := make([]models.ScoredNZBResult, len(results))
	for i := range results {
		scored[i].NZBResult = results[i]
	}
	h.SourceHealthSvc.Annotate(ctx, scored)
	out := make([]annotatedNZBResult, len(results))
	for i := range results {
		out[i] = annotatedNZBResult{NZBResult: results[i], SourceHealth: scored[i].SourceHealth}
	}
	return out
}

func annotateResultsProfile(results []models.NZBResult, userID string, titles ...string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	for i := range results {
		attrs := make(map[string]string, len(results[i].Attributes)+2)
		for key, value := range results[i].Attributes {
			attrs[key] = value
		}
		results[i].Attributes = attrs
		if len(titles) > 0 {
			attrs["titleId"] = titles[0]
		}
		results[i].Attributes["profileId"] = userID
	}
}

func annotateScoredResultsProfile(results []models.ScoredNZBResult, userID string, titles ...string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	for i := range results {
		attrs := make(map[string]string, len(results[i].Attributes)+2)
		for key, value := range results[i].Attributes {
			attrs[key] = value
		}
		results[i].Attributes = attrs
		if len(titles) > 0 {
			attrs["titleId"] = titles[0]
		}
		results[i].Attributes["profileId"] = userID
	}
}

// maxSourceHealthWait caps the long-poll wait for GET /indexers/source-health.
const maxSourceHealthWait = 25 * time.Second

// SourceHealth returns the annotations for a pending search token
// (GET /indexers/source-health?token=...&waitMs=...). With waitMs the request
// long-polls until the checks finish or the wait elapses.
func (h *IndexerHandler) SourceHealth(w http.ResponseWriter, r *http.Request) {
	if h.SourceHealthSvc == nil {
		http.Error(w, "source health unavailable", http.StatusServiceUnavailable)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	var wait time.Duration
	if raw := strings.TrimSpace(r.URL.Query().Get("waitMs")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			wait = time.Duration(ms) * time.Millisecond
		}
	}
	if wait > maxSourceHealthWait {
		wait = maxSourceHealthWait
	}
	snapshot, ok := h.SourceHealthSvc.Snapshot(r.Context(), token, wait)
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// SearchTest handles the admin search test endpoint with full scoring breakdown.
func (h *IndexerHandler) SearchTest(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	categories := r.URL.Query()["cat"]
	imdbID := strings.TrimSpace(r.URL.Query().Get("imdbId"))
	titleID := strings.TrimSpace(r.URL.Query().Get("titleId"))
	numbering := requestEpisodeNumbering(r)
	mediaType := strings.TrimSpace(r.URL.Query().Get("mediaType"))
	query = normalizeDecoratedSeriesQuery(query, mediaType)
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	clientID := strings.TrimSpace(r.Header.Get("X-Client-ID"))
	if clientID == "" {
		clientID = strings.TrimSpace(r.URL.Query().Get("clientId"))
	}
	year := 0
	if rawYear := r.URL.Query().Get("year"); rawYear != "" {
		if parsed, err := strconv.Atoi(rawYear); err == nil && parsed > 0 {
			year = parsed
		}
	}
	max := 0 // No limit for search test by default
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			max = parsed
		}
	}
	useDownloadRanking := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("downloadRanking")), "true")

	// Get series metadata for TV shows
	var episodeResolver *filter.SeriesEpisodeResolver
	var isDaily bool
	var isAnime bool
	var targetAirDate string
	var episodeAirYear int
	var seasonPremiereYear int
	var episodeReleased bool
	var absoluteEpisodeNumber int
	var countryCode string
	var originalLanguage string
	var tvdbID int64
	var alternateTitles []string
	if mediaType == "series" && h.MetadataSvc != nil {
		seriesMeta := h.getSeriesSearchMetadata(r.Context(), query, year, imdbID, seriesNumberingContext{TitleID: titleID, Numbering: numbering})
		if seriesMeta != nil {
			if titleID == "" {
				titleID = seriesMeta.TitleID
			}
			numbering = seriesMeta.Numbering
			episodeResolver = seriesMeta.EpisodeResolver
			isDaily = seriesMeta.IsDaily
			isAnime = seriesMeta.IsAnime
			targetAirDate = seriesMeta.TargetAirDate
			episodeAirYear = seriesMeta.EpisodeAirYear
			seasonPremiereYear = seriesMeta.SeasonPremiereYear
			episodeReleased = seriesMeta.EpisodeReleased
			absoluteEpisodeNumber = seriesMeta.AbsoluteEpisodeNumber
			countryCode = seriesMeta.CountryCode
			originalLanguage = seriesMeta.OriginalLanguage
			tvdbID = seriesMeta.TVDBID
			if year == 0 && seriesMeta.Year > 0 {
				year = seriesMeta.Year
			}
		}
	}

	// Detect anime for movies via the same helper used by manual search and prequeue.
	if mediaType == "movie" && h.MovieMetadataSvc != nil {
		movieQuery := models.MovieDetailsQuery{
			Name:   strings.TrimSpace(query),
			Year:   year,
			IMDBID: imdbID,
		}
		if movieTitle, err := h.MovieMetadataSvc.MovieInfo(r.Context(), movieQuery); err == nil && movieTitle != nil {
			countryCode = strings.TrimSpace(movieTitle.CountryCode)
			originalLanguage = strings.TrimSpace(movieTitle.Language)
			alternateTitles = hydratedMovieSearchTitles(movieTitle)
			if isAnimeTitle(movieTitle) {
				isAnime = true
				log.Printf("[indexer] Movie %q is anime (genres=%v originalName=%q language=%q) - applying anime language preferences",
					query, movieTitle.Genres, movieTitle.OriginalName, movieTitle.Language)
			}
		}
	}

	opts := indexer.SearchOptions{
		Numbering:             numbering,
		TitleID:               titleID,
		Query:                 query,
		Categories:            categories,
		MaxResults:            max,
		IMDBID:                imdbID,
		TVDBID:                tvdbID,
		AlternateTitles:       alternateTitles,
		MediaType:             mediaType,
		Year:                  year,
		CountryCode:           countryCode,
		OriginalLanguage:      originalLanguage,
		UserID:                userID,
		ClientID:              clientID,
		AdaptiveThroughput:    adaptiveThroughputFromRequest(r),
		EpisodeResolver:       episodeResolver,
		IsDaily:               isDaily,
		IsAnime:               isAnime,
		TargetAirDate:         targetAirDate,
		EpisodeAirYear:        episodeAirYear,
		SeasonPremiereYear:    seasonPremiereYear,
		EpisodeReleased:       episodeReleased,
		AbsoluteEpisodeNumber: absoluteEpisodeNumber,
		UseDownloadRanking:    useDownloadRanking,
	}

	results, err := h.Service.SearchTest(r.Context(), opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		statusCode, errResponse := classifySearchError(err)
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(errResponse)
		return
	}

	markBadScoredResults(results, h.BadStreams, reportContextForSearch(h.TrackPreferences, opts))

	// Ensure we return [] instead of null for empty results
	if results == nil {
		results = []models.ScoredNZBResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func normalizeDecoratedSeriesQuery(query, mediaType string) string {
	if !strings.EqualFold(strings.TrimSpace(mediaType), "series") || !seriesDisplayLabelRE.MatchString(query) {
		return query
	}
	parsed := debrid.ParseQuery(query)
	if parsed.Title == "" || !parsed.HasSeasonMatch || parsed.Episode <= 0 {
		return query
	}
	return fmt.Sprintf("%s S%02dE%02d", parsed.Title, parsed.Season, parsed.Episode)
}

func markBadScoredResults(results []models.ScoredNZBResult, badStreams *badstreams.Service, contexts ...badstreams.PlaybackContext) {
	var ctx badstreams.PlaybackContext
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	if badStreams == nil {
		return
	}
	for i := range results {
		if badStreams.MatchFor(results[i].NZBResult, ctx) == nil {
			continue
		}
		results[i].FilterStatus = "filtered"
		if results[i].FilterReason == "" {
			results[i].FilterReason = "marked bad stream"
		} else if !strings.Contains(strings.ToLower(results[i].FilterReason), "marked bad stream") {
			results[i].FilterReason += "; marked bad stream"
		}
	}
}

// buildMaskedTitle creates a display name from search parameters
func buildMaskedTitle(query string, year int, mediaType string) string {
	// Parse the query to extract clean title and episode info
	parsed := debrid.ParseQuery(query)
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		title = strings.TrimSpace(query)
	}
	if title == "" {
		return "Media"
	}

	// For series with episode info
	if parsed.Season > 0 && parsed.Episode > 0 {
		return fmt.Sprintf("%s S%02dE%02d", title, parsed.Season, parsed.Episode)
	}

	// For movies or content with year
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}

	return title
}

func (h *IndexerHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// classifySearchError determines the appropriate HTTP status code and response
// for search errors, distinguishing timeouts (504) from other gateway errors (502)
func classifySearchError(err error) (int, map[string]interface{}) {
	errMsg := err.Error()
	isTimeout := false

	// Check for net.Error timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		isTimeout = true
	}

	// Also check error message for common timeout patterns
	// (catches wrapped errors where the net.Error is buried)
	if !isTimeout {
		isTimeout = strings.Contains(errMsg, "timeout") ||
			strings.Contains(errMsg, "context deadline exceeded") ||
			strings.Contains(errMsg, "Timeout exceeded")
	}

	if isTimeout {
		return http.StatusGatewayTimeout, map[string]interface{}{
			"error":   errMsg,
			"code":    "GATEWAY_TIMEOUT",
			"message": "Search timed out. If using Aiostreams, consider increasing the indexer timeout in Settings.",
		}
	}
	if strings.Contains(errMsg, "did not return a Newznab RSS feed") {
		return http.StatusBadGateway, map[string]interface{}{
			"error":   errMsg,
			"code":    "INVALID_INDEXER_FEED",
			"message": "The indexer returned an invalid search response. Check that its URL points to the Newznab API endpoint.",
		}
	}

	return http.StatusBadGateway, map[string]interface{}{
		"error":   errMsg,
		"code":    "BAD_GATEWAY",
		"message": "Search failed due to an upstream error.",
	}
}

// seriesSearchMetadata contains series metadata needed for search
type seriesSearchMetadata struct {
	Numbering             *models.EpisodeNumbering
	TitleID               string
	EpisodeResolver       *filter.SeriesEpisodeResolver
	IsDaily               bool
	IsAnime               bool
	TargetAirDate         string // YYYY-MM-DD format for daily shows
	Year                  int    // Series premiere year from metadata
	EpisodeAirYear        int    // Year the target episode actually aired (may differ from series premiere year)
	SeasonPremiereYear    int    // Premiere year of the requested season only.
	EpisodeReleased       bool   // True only when metadata confirms the target episode has aired
	AbsoluteEpisodeNumber int
	CountryCode           string
	OriginalLanguage      string
	TVDBID                int64
}

// getSeriesSearchMetadata fetches series metadata for search, including episode resolver
// and daily show detection
func (h *IndexerHandler) getSeriesSearchMetadata(ctx context.Context, query string, year int, imdbID string, options ...seriesNumberingContext) *seriesSearchMetadata {
	if h.MetadataSvc == nil {
		return nil
	}

	// Parse title and episode from query (e.g., "ReBoot S03E02" -> "ReBoot", Season=3, Episode=2)
	parsed := debrid.ParseQuery(query)
	titleName := strings.TrimSpace(parsed.Title)
	if titleName == "" {
		titleName = strings.TrimSpace(query)
	}
	if titleName == "" {
		return nil
	}

	// Build query using available identifiers
	metaQuery := models.SeriesDetailsQuery{
		Name:   titleName,
		Year:   year,
		IMDBID: imdbID,
	}

	var numbering *models.EpisodeNumbering
	if len(options) > 0 {
		metaQuery.TitleID = options[0].TitleID
		numbering = options[0].Numbering
	}
	if numbering != nil {
		metaQuery.SeasonType = numbering.Ordering
	}
	// Fetch series details from metadata service
	details, err := h.MetadataSvc.SeriesDetails(ctx, metaQuery)
	if err != nil {
		log.Printf("[indexer] Failed to get series details for search metadata: %v", err)
		return nil
	}

	if details == nil || len(details.Seasons) == 0 {
		log.Printf("[indexer] No season data available for search metadata")
		return nil
	}

	result := &seriesSearchMetadata{
		Numbering:        details.Numbering,
		TitleID:          details.Title.ID,
		IsDaily:          details.Title.IsDaily,
		Year:             details.Title.Year,
		CountryCode:      details.Title.CountryCode,
		OriginalLanguage: strings.TrimSpace(details.Title.Language),
		TVDBID:           details.Title.TVDBID,
	}

	result.IsAnime = isAnimeTitle(&details.Title)
	if numbering != nil && !models.SameEpisodeNumbering(numbering, details.Numbering) {
		result.Numbering = numbering
		return result
	}
	result.SeasonPremiereYear = models.SeriesSeasonPremiereYear(details.Seasons, parsed.Season)

	// Build season -> episode count map for episode resolver
	seasonCounts := make(map[int]int)
	seasonNames := make(map[int]string)
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
	}

	if len(seasonCounts) > 0 {
		result.EpisodeResolver = filter.NewSeriesEpisodeResolver(seasonCounts).WithSeasonNames(seasonNames)
	}

	// Look up the air date of the target episode
	// For daily shows: used for date-based matching
	// For all shows: used to accept results tagged with the episode's air year
	if parsed.Season > 0 && parsed.Episode > 0 {
		for _, season := range details.Seasons {
			if season.Number == parsed.Season {
				for _, ep := range season.Episodes {
					if ep.EpisodeNumber == parsed.Episode {
						result.EpisodeReleased = models.SeriesEpisodeHasAired(ep, time.Now())
						if releaseAbsolute := releaseAbsoluteEpisodeNumber(details.Seasons, ep); releaseAbsolute > 0 {
							result.AbsoluteEpisodeNumber = releaseAbsolute
							if ep.AbsoluteEpisodeNumber > 0 && ep.AbsoluteEpisodeNumber != releaseAbsolute {
								log.Printf("[indexer] Using release-style absolute episode %d for S%02dE%02d instead of provider absolute %d",
									releaseAbsolute, parsed.Season, parsed.Episode, ep.AbsoluteEpisodeNumber)
							} else {
								log.Printf("[indexer] Derived release-style absolute episode number %d for S%02dE%02d",
									releaseAbsolute, parsed.Season, parsed.Episode)
							}
						} else if ep.AbsoluteEpisodeNumber > 0 {
							result.AbsoluteEpisodeNumber = ep.AbsoluteEpisodeNumber
							log.Printf("[indexer] Found absolute episode number %d for S%02dE%02d",
								result.AbsoluteEpisodeNumber, parsed.Season, parsed.Episode)
						} else if inferred := inferAbsoluteEpisodeNumber(details.Seasons, ep); inferred > 0 {
							result.AbsoluteEpisodeNumber = inferred
							log.Printf("[indexer] Inferred absolute episode number %d for S%02dE%02d from adjacent episodes",
								result.AbsoluteEpisodeNumber, parsed.Season, parsed.Episode)
						}
						if ep.AiredDate != "" {
							if result.IsDaily {
								result.TargetAirDate = ep.AiredDate
								log.Printf("[indexer] Found air date %s for S%02dE%02d",
									result.TargetAirDate, parsed.Season, parsed.Episode)
							}
							// Extract year from air date for year filter tolerance
							if parts := strings.SplitN(ep.AiredDate, "-", 2); len(parts) >= 1 {
								if airYear, err := strconv.Atoi(parts[0]); err == nil && airYear > 0 {
									result.EpisodeAirYear = airYear
									log.Printf("[indexer] Episode air year %d for S%02dE%02d",
										airYear, parsed.Season, parsed.Episode)
								}
							}
						}
						break
					}
				}
				break
			}
		}
	}

	return result
}

// createEpisodeResolver is a convenience wrapper for backward compatibility
func (h *IndexerHandler) createEpisodeResolver(ctx context.Context, query string, year int) *filter.SeriesEpisodeResolver {
	meta := h.getSeriesSearchMetadata(ctx, query, year, "")
	if meta == nil {
		return nil
	}
	return meta.EpisodeResolver
}

func requestEpisodeNumbering(r *http.Request) *models.EpisodeNumbering {
	id := strings.TrimSpace(r.URL.Query().Get("episodeNumberingId"))
	if id == "" {
		return nil
	}
	return &models.EpisodeNumbering{SeriesID: id, Ordering: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("episodeOrdering")))}
}

type seriesNumberingContext struct {
	TitleID   string
	Numbering *models.EpisodeNumbering
}

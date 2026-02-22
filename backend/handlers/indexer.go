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

	"novastream/models"
	"novastream/services/debrid"
	"novastream/services/indexer"
	"novastream/utils/filter"
)

type indexerService interface {
	Search(context.Context, indexer.SearchOptions) ([]models.NZBResult, error)
}

var _ indexerService = (*indexer.Service)(nil)

type IndexerHandler struct {
	Service     indexerService
	MetadataSvc SeriesDetailsProvider
	DemoMode    bool
}

func NewIndexerHandler(s indexerService, demoMode bool) *IndexerHandler {
	return &IndexerHandler{Service: s, DemoMode: demoMode}
}

// SetMetadataService sets the metadata service for episode counting
func (h *IndexerHandler) SetMetadataService(svc SeriesDetailsProvider) {
	h.MetadataSvc = svc
}

func (h *IndexerHandler) Search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	categories := r.URL.Query()["cat"]
	imdbID := strings.TrimSpace(r.URL.Query().Get("imdbId"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("mediaType"))
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
	if mediaType == "series" && h.MetadataSvc != nil {
		seriesMeta := h.getSeriesSearchMetadata(r.Context(), query, year)
		if seriesMeta != nil {
			episodeResolver = seriesMeta.EpisodeResolver
			isDaily = seriesMeta.IsDaily
			isAnime = seriesMeta.IsAnime
			targetAirDate = seriesMeta.TargetAirDate
			if episodeResolver != nil {
				log.Printf("[indexer] Episode resolver created: %d total episodes, %d seasons",
					episodeResolver.TotalEpisodes, len(episodeResolver.SeasonEpisodeCounts))
			}
			if isDaily {
				log.Printf("[indexer] Daily show detected, targetAirDate=%s", targetAirDate)
			}
		}
	}

	opts := indexer.SearchOptions{
		Query:           query,
		Categories:      categories,
		MaxResults:      max,
		IMDBID:          imdbID,
		MediaType:       mediaType,
		Year:            year,
		UserID:          userID,
		ClientID:        clientID,
		EpisodeResolver: episodeResolver,
		IsDaily:         isDaily,
		IsAnime:         isAnime,
		TargetAirDate:   targetAirDate,
	}

	results, err := h.Service.Search(r.Context(), opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		statusCode, errResponse := classifySearchError(err)
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(errResponse)
		return
	}

	// In demo mode, mask actual filenames with the search query info
	if h.DemoMode {
		maskedTitle := buildMaskedTitle(query, year, mediaType)
		for i := range results {
			results[i].Title = maskedTitle
			results[i].Indexer = "Demo"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
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

	return http.StatusBadGateway, map[string]interface{}{
		"error":   errMsg,
		"code":    "BAD_GATEWAY",
		"message": "Search failed due to an upstream error.",
	}
}

// seriesSearchMetadata contains series metadata needed for search
type seriesSearchMetadata struct {
	EpisodeResolver *filter.SeriesEpisodeResolver
	IsDaily         bool
	IsAnime         bool
	TargetAirDate   string // YYYY-MM-DD format for daily shows
}

// getSeriesSearchMetadata fetches series metadata for search, including episode resolver
// and daily show detection
func (h *IndexerHandler) getSeriesSearchMetadata(ctx context.Context, query string, year int) *seriesSearchMetadata {
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
		Name: titleName,
		Year: year,
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
		IsDaily: details.Title.IsDaily,
	}

	// Check if this is anime content from the genres
	for _, genre := range details.Title.Genres {
		genreLower := strings.ToLower(genre)
		if genreLower == "animation" || genreLower == "anime" {
			result.IsAnime = true
			break
		}
	}

	// Build season -> episode count map for episode resolver
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

	if len(seasonCounts) > 0 {
		result.EpisodeResolver = filter.NewSeriesEpisodeResolver(seasonCounts)
	}

	// For daily shows, find the air date of the target episode
	if result.IsDaily && parsed.Season > 0 && parsed.Episode > 0 {
		log.Printf("[indexer] Series %q is a daily show, looking up air date for S%02dE%02d",
			titleName, parsed.Season, parsed.Episode)
		for _, season := range details.Seasons {
			if season.Number == parsed.Season {
				for _, ep := range season.Episodes {
					if ep.EpisodeNumber == parsed.Episode && ep.AiredDate != "" {
						result.TargetAirDate = ep.AiredDate
						log.Printf("[indexer] Found air date %s for S%02dE%02d",
							result.TargetAirDate, parsed.Season, parsed.Episode)
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
	meta := h.getSeriesSearchMetadata(ctx, query, year)
	if meta == nil {
		return nil
	}
	return meta.EpisodeResolver
}

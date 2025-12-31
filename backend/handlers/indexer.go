package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

	// Create episode resolver for TV shows to enable accurate pack size filtering
	var episodeResolver *filter.SeriesEpisodeResolver
	if mediaType == "series" && h.MetadataSvc != nil {
		episodeResolver = h.createEpisodeResolver(r.Context(), query, year)
		if episodeResolver != nil {
			log.Printf("[indexer] Episode resolver created: %d total episodes, %d seasons",
				episodeResolver.TotalEpisodes, len(episodeResolver.SeasonEpisodeCounts))
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
	}

	results, err := h.Service.Search(r.Context(), opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
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

// createEpisodeResolver fetches series metadata and creates an episode resolver
// for accurate pack size filtering
func (h *IndexerHandler) createEpisodeResolver(ctx context.Context, query string, year int) *filter.SeriesEpisodeResolver {
	if h.MetadataSvc == nil {
		return nil
	}

	// Parse title from query (e.g., "ReBoot S03E02" -> "ReBoot")
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
		log.Printf("[indexer] Failed to get series details for episode resolver: %v", err)
		return nil
	}

	if details == nil || len(details.Seasons) == 0 {
		log.Printf("[indexer] No season data available for episode resolver")
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
		log.Printf("[indexer] No valid seasons found for episode resolver")
		return nil
	}

	return filter.NewSeriesEpisodeResolver(seasonCounts)
}

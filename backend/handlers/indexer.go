package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"novastream/models"
	"novastream/services/debrid"
	"novastream/services/indexer"
)

type indexerService interface {
	Search(context.Context, indexer.SearchOptions) ([]models.NZBResult, error)
}

var _ indexerService = (*indexer.Service)(nil)

type IndexerHandler struct {
	Service  indexerService
	DemoMode bool
}

func NewIndexerHandler(s indexerService, demoMode bool) *IndexerHandler {
	return &IndexerHandler{Service: s, DemoMode: demoMode}
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

	opts := indexer.SearchOptions{
		Query:      query,
		Categories: categories,
		MaxResults: max,
		IMDBID:     imdbID,
		MediaType:  mediaType,
		Year:       year,
		UserID:     userID,
		ClientID:   clientID,
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

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"novastream/config"
	"novastream/models"
	metadatapkg "novastream/services/metadata"
)

type metadataService interface {
	Trending(context.Context, string, config.TrendingMovieSource) ([]models.TrendingItem, error)
	Search(context.Context, string, string) ([]models.SearchResult, error)
	SeriesDetails(context.Context, models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	BatchSeriesDetails(context.Context, []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem
	MovieDetails(context.Context, models.MovieDetailsQuery) (*models.Title, error)
	Trailers(context.Context, models.TrailerQuery) (*models.TrailerResponse, error)
	GetCustomList(listURL string) ([]models.TrendingItem, error)
}

var _ metadataService = (*metadatapkg.Service)(nil)

// userSettingsProvider retrieves per-user settings.
type userSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

type MetadataHandler struct {
	Service      metadataService
	CfgManager   *config.Manager
	UserSettings userSettingsProvider
}

func NewMetadataHandler(s metadataService, cfgManager *config.Manager) *MetadataHandler {
	return &MetadataHandler{Service: s, CfgManager: cfgManager}
}

// SetUserSettingsProvider sets the user settings provider for per-user settings.
func (h *MetadataHandler) SetUserSettingsProvider(provider userSettingsProvider) {
	h.UserSettings = provider
}

func (h *MetadataHandler) DiscoverNew(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))

	// Get trending movie source - prefer user settings, fall back to global settings
	var trendingMovieSource config.TrendingMovieSource

	// Try user-specific settings first
	if userID != "" && h.UserSettings != nil {
		if userSettings, err := h.UserSettings.Get(userID); err == nil && userSettings != nil {
			if userSettings.HomeShelves.TrendingMovieSource != "" {
				trendingMovieSource = config.TrendingMovieSource(userSettings.HomeShelves.TrendingMovieSource)
			}
		}
	}

	// Fall back to global settings
	if trendingMovieSource == "" {
		if settings, err := h.CfgManager.Load(); err == nil {
			trendingMovieSource = settings.HomeShelves.TrendingMovieSource
		}
	}

	// Default if still not set
	if trendingMovieSource == "" {
		trendingMovieSource = config.TrendingMovieSourceReleased
	}

	items, err := h.Service.Trending(r.Context(), mediaType, trendingMovieSource)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (h *MetadataHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	results, err := h.Service.Search(r.Context(), q, mediaType)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *MetadataHandler) SeriesDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	trimAndParseInt := func(value string) int {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return parsed
	}

	trimAndParseInt64 := func(value string) int64 {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}

	req := models.SeriesDetailsQuery{
		TitleID: strings.TrimSpace(query.Get("titleId")),
		Name:    strings.TrimSpace(query.Get("name")),
		Year:    trimAndParseInt(query.Get("year")),
		TVDBID:  trimAndParseInt64(query.Get("tvdbId")),
		TMDBID:  trimAndParseInt64(query.Get("tmdbId")),
	}

	details, err := h.Service.SeriesDetails(r.Context(), req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}

func (h *MetadataHandler) BatchSeriesDetails(w http.ResponseWriter, r *http.Request) {
	var req models.BatchSeriesDetailsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	results := h.Service.BatchSeriesDetails(r.Context(), req.Queries)

	response := models.BatchSeriesDetailsResponse{
		Results: results,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (h *MetadataHandler) MovieDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	trimAndParseInt := func(value string) int {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return parsed
	}

	trimAndParseInt64 := func(value string) int64 {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}

	req := models.MovieDetailsQuery{
		TitleID: strings.TrimSpace(query.Get("titleId")),
		Name:    strings.TrimSpace(query.Get("name")),
		Year:    trimAndParseInt(query.Get("year")),
		IMDBID:  strings.TrimSpace(query.Get("imdbId")),
		TMDBID:  trimAndParseInt64(query.Get("tmdbId")),
		TVDBID:  trimAndParseInt64(query.Get("tvdbId")),
	}

	details, err := h.Service.MovieDetails(r.Context(), req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}

func (h *MetadataHandler) Trailers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	trimAndParseInt := func(value string) int {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return parsed
	}

	trimAndParseInt64 := func(value string) int64 {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}

	req := models.TrailerQuery{
		MediaType: strings.TrimSpace(query.Get("type")),
		TitleID:   strings.TrimSpace(query.Get("titleId")),
		Name:      strings.TrimSpace(query.Get("name")),
		Year:      trimAndParseInt(query.Get("year")),
		IMDBID:    strings.TrimSpace(query.Get("imdbId")),
		TMDBID:    trimAndParseInt64(query.Get("tmdbId")),
		TVDBID:    trimAndParseInt64(query.Get("tvdbId")),
	}

	response, err := h.Service.Trailers(r.Context(), req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if response == nil {
		response = &models.TrailerResponse{Trailers: []models.Trailer{}}
	} else if response.Trailers == nil {
		response.Trailers = []models.Trailer{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// CustomList fetches items from a custom MDBList URL
func (h *MetadataHandler) CustomList(w http.ResponseWriter, r *http.Request) {
	listURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if listURL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "url parameter required"})
		return
	}

	// Validate URL contains mdblist.com/lists/
	if !strings.Contains(listURL, "mdblist.com/lists/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid MDBList URL format"})
		return
	}

	// Auto-fix: remove trailing slashes and add /json if missing
	listURL = strings.TrimRight(listURL, "/")
	if !strings.HasSuffix(listURL, "/json") {
		listURL = listURL + "/json"
	}

	items, err := h.Service.GetCustomList(listURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

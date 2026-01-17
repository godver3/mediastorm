package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log"
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
	BatchMovieReleases(context.Context, []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem
	Trailers(context.Context, models.TrailerQuery) (*models.TrailerResponse, error)
	ExtractTrailerStreamURL(context.Context, string) (string, error)
	StreamTrailer(context.Context, string, io.Writer) error
	StreamTrailerWithRange(context.Context, string, string, io.Writer) error
	GetCustomList(ctx context.Context, listURL string, limit int) ([]models.TrendingItem, int, error)
	// Trailer prequeue methods for 1080p YouTube trailers
	PrequeueTrailer(videoURL string) (string, error)
	GetTrailerPrequeueStatus(id string) (*metadatapkg.TrailerPrequeueItem, error)
	ServePrequeuedTrailer(id string, w http.ResponseWriter, r *http.Request) error
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

// DiscoverNewResponse wraps trending items with total count for pagination
type DiscoverNewResponse struct {
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"` // Pre-filter total (only set when hideUnreleased is used)
}

func (h *MetadataHandler) DiscoverNew(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"

	// Parse optional pagination parameters
	limit := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

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

	// Track pre-filter total for explore card logic
	unfilteredTotal := len(items)

	// Apply unreleased filter if requested
	if hideUnreleased {
		items = filterUnreleasedItems(items)
	}

	// Apply pagination
	total := len(items)
	if offset > 0 {
		if offset >= total {
			items = []models.TrendingItem{}
		} else {
			items = items[offset:]
		}
	}
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}

	w.Header().Set("Content-Type", "application/json")
	resp := DiscoverNewResponse{Items: items, Total: total}
	if hideUnreleased {
		resp.UnfilteredTotal = unfilteredTotal
	}
	json.NewEncoder(w).Encode(resp)
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

func (h *MetadataHandler) BatchMovieReleases(w http.ResponseWriter, r *http.Request) {
	var req models.BatchMovieReleasesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	results := h.Service.BatchMovieReleases(r.Context(), req.Queries)

	response := models.BatchMovieReleasesResponse{
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
		MediaType:    strings.TrimSpace(query.Get("type")),
		TitleID:      strings.TrimSpace(query.Get("titleId")),
		Name:         strings.TrimSpace(query.Get("name")),
		Year:         trimAndParseInt(query.Get("year")),
		IMDBID:       strings.TrimSpace(query.Get("imdbId")),
		TMDBID:       trimAndParseInt64(query.Get("tmdbId")),
		TVDBID:       trimAndParseInt64(query.Get("tvdbId")),
		SeasonNumber: trimAndParseInt(query.Get("season")),
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

// TrailerStreamResponse contains the extracted stream URL
type TrailerStreamResponse struct {
	StreamURL string `json:"streamUrl"`
	Title     string `json:"title,omitempty"`
	Duration  int    `json:"duration,omitempty"`
}

// TrailerStream extracts a direct stream URL from YouTube using yt-dlp
func (h *MetadataHandler) TrailerStream(w http.ResponseWriter, r *http.Request) {
	videoURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if videoURL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "url parameter required"})
		return
	}

	// Validate it's a YouTube URL
	if !strings.Contains(videoURL, "youtube.com") && !strings.Contains(videoURL, "youtu.be") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "only YouTube URLs are supported"})
		return
	}

	streamURL, err := h.Service.ExtractTrailerStreamURL(r.Context(), videoURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TrailerStreamResponse{StreamURL: streamURL})
}

// TrailerProxy streams a YouTube video through the backend using yt-dlp
// This bypasses iOS restrictions on accessing googlevideo.com URLs directly
func (h *MetadataHandler) TrailerProxy(w http.ResponseWriter, r *http.Request) {
	videoURL := strings.TrimSpace(r.URL.Query().Get("url"))
	rangeHeader := r.Header.Get("Range")
	log.Printf("[trailer-proxy] request for URL: %s, Range: %s", videoURL, rangeHeader)

	if videoURL == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}

	// Validate it's a YouTube URL
	if !strings.Contains(videoURL, "youtube.com") && !strings.Contains(videoURL, "youtu.be") {
		http.Error(w, "only YouTube URLs are supported", http.StatusBadRequest)
		return
	}

	log.Printf("[trailer-proxy] starting stream for: %s", videoURL)

	// Use yt-dlp to stream the video directly to the response
	err := h.Service.StreamTrailerWithRange(r.Context(), videoURL, rangeHeader, w)
	if err != nil {
		log.Printf("[trailer-proxy] stream error: %v", err)
		// Only write error if we haven't started writing the response yet
		if w.Header().Get("Content-Type") == "" {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	} else {
		log.Printf("[trailer-proxy] stream completed successfully for: %s", videoURL)
	}
}

// TrailerPrequeueRequest is the request body for starting a trailer prequeue
type TrailerPrequeueRequest struct {
	URL string `json:"url"`
}

// TrailerPrequeueResponse is the response for trailer prequeue operations
type TrailerPrequeueResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	FileSize int64  `json:"fileSize,omitempty"`
}

// TrailerPrequeue starts downloading a trailer in the background
func (h *MetadataHandler) TrailerPrequeue(w http.ResponseWriter, r *http.Request) {
	var req TrailerPrequeueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	videoURL := strings.TrimSpace(req.URL)
	if videoURL == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}

	// Validate it's a YouTube URL
	if !strings.Contains(videoURL, "youtube.com") && !strings.Contains(videoURL, "youtu.be") {
		http.Error(w, "only YouTube URLs are supported", http.StatusBadRequest)
		return
	}

	id, err := h.Service.PrequeueTrailer(videoURL)
	if err != nil {
		log.Printf("[trailer-prequeue] error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TrailerPrequeueResponse{
		ID:     id,
		Status: "pending",
	})
}

// TrailerPrequeueStatus returns the status of a prequeued trailer
func (h *MetadataHandler) TrailerPrequeueStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id parameter required", http.StatusBadRequest)
		return
	}

	item, err := h.Service.GetTrailerPrequeueStatus(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TrailerPrequeueResponse{
		ID:       item.ID,
		Status:   string(item.Status),
		Error:    item.Error,
		FileSize: item.FileSize,
	})
}

// TrailerPrequeueServe serves a downloaded trailer file
func (h *MetadataHandler) TrailerPrequeueServe(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id parameter required", http.StatusBadRequest)
		return
	}

	log.Printf("[trailer-prequeue] serving trailer: %s", id)

	if err := h.Service.ServePrequeuedTrailer(id, w, r); err != nil {
		log.Printf("[trailer-prequeue] serve error: %v", err)
		// Only write error if headers haven't been sent
		if w.Header().Get("Content-Type") == "" {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
	}
}

// CustomListResponse wraps custom list items with total count for pagination
type CustomListResponse struct {
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"` // Pre-filter total (only set when hideUnreleased is used)
}

// filterUnreleasedItems removes items that haven't been released for home viewing.
// For movies: filters out items where HomeRelease is nil or HomeRelease.Released is false.
// For series: filters out items where Status is "upcoming" or "in production" (case-insensitive).
func filterUnreleasedItems(items []models.TrendingItem) []models.TrendingItem {
	result := make([]models.TrendingItem, 0, len(items))
	filteredCount := 0
	for _, item := range items {
		if item.Title.MediaType == "movie" {
			// Movies: keep only if home release exists and is released
			if item.Title.HomeRelease != nil && item.Title.HomeRelease.Released {
				result = append(result, item)
			} else {
				filteredCount++
				if filteredCount <= 3 {
					hasRelease := item.Title.HomeRelease != nil
					released := hasRelease && item.Title.HomeRelease.Released
					log.Printf("[hideUnreleased] filtered movie: %s (hasHomeRelease=%v, released=%v)", item.Title.Name, hasRelease, released)
				}
			}
		} else if item.Title.MediaType == "series" {
			// Series: filter out "upcoming" or "in production" statuses
			status := strings.ToLower(item.Title.Status)
			if status != "upcoming" && status != "in production" {
				result = append(result, item)
			} else {
				filteredCount++
				if filteredCount <= 3 {
					log.Printf("[hideUnreleased] filtered series: %s (status=%s)", item.Title.Name, item.Title.Status)
				}
			}
		} else {
			// Unknown type - include by default
			result = append(result, item)
		}
	}
	log.Printf("[hideUnreleased] filter result: %d/%d items kept (filtered %d)", len(result), len(items), filteredCount)
	return result
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

	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"

	// Parse optional pagination parameters (0 = no limit/offset)
	limit := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
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

	// When hideUnreleased is true, we need ALL items to get accurate filtered count
	// Otherwise, fetch only what we need for pagination
	fetchLimit := 0 // 0 = fetch all
	if !hideUnreleased {
		if limit > 0 && offset > 0 {
			fetchLimit = limit + offset
		} else if limit > 0 {
			fetchLimit = limit
		}
	}

	items, total, err := h.Service.GetCustomList(r.Context(), listURL, fetchLimit)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Track pre-filter total for explore card logic
	unfilteredTotal := total

	// Apply unreleased filter if requested (before pagination)
	if hideUnreleased {
		items = filterUnreleasedItems(items)
		total = len(items) // This is now accurate since we fetched all items
	}

	// Apply offset
	if offset > 0 {
		if offset >= len(items) {
			items = []models.TrendingItem{}
		} else {
			items = items[offset:]
		}
	}

	// Apply limit after offset
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}

	w.Header().Set("Content-Type", "application/json")
	resp := CustomListResponse{Items: items, Total: total}
	if hideUnreleased {
		resp.UnfilteredTotal = unfilteredTotal
	}
	json.NewEncoder(w).Encode(resp)
}

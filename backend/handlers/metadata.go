package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/kids"
	metadatapkg "novastream/services/metadata"
	"novastream/services/trakt"
)

type metadataService interface {
	Trending(context.Context, string) ([]models.TrendingItem, error)
	Search(context.Context, string, string) ([]models.SearchResult, error)
	EnrichSearchCertifications(context.Context, []models.SearchResult)
	SeriesDetails(context.Context, models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	BatchSeriesDetails(context.Context, []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem
	BatchSeriesTitleFields(context.Context, []models.SeriesDetailsQuery, []string) []models.BatchSeriesDetailsItem
	MovieDetails(context.Context, models.MovieDetailsQuery) (*models.Title, error)
	BatchMovieReleases(context.Context, []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem
	CollectionDetails(context.Context, int64) (*models.CollectionDetails, error)
	Similar(context.Context, string, int64) ([]models.Title, error)
	DiscoverByGenre(context.Context, string, int64, int, int) ([]models.TrendingItem, int, error)
	GetAIRecommendations(context.Context, []string, []string, string) ([]models.TrendingItem, error)
	GetAISimilar(context.Context, string, string) ([]models.TrendingItem, error)
	GetAICustomRecommendations(context.Context, string) ([]models.TrendingItem, error)
	GetAISurprise(context.Context, string, string) (*models.TrendingItem, error)
	PersonDetails(context.Context, int64) (*models.PersonDetails, error)
	Trailers(context.Context, models.TrailerQuery) (*models.TrailerResponse, error)
	ExtractTrailerStreamURL(context.Context, string) (string, error)
	StreamTrailer(context.Context, string, io.Writer) error
	StreamTrailerWithRange(context.Context, string, string, io.Writer) error
	GetCustomList(ctx context.Context, listURL string, opts metadatapkg.CustomListOptions) ([]models.TrendingItem, int, int, error)
	GetCuratedList(ctx context.Context, items []metadatapkg.CuratedItem, label string) ([]models.TrendingItem, error)
	// Trailer prequeue methods for 1080p YouTube trailers
	PrequeueTrailer(videoURL string) (string, error)
	GetTrailerPrequeueStatus(id string) (*metadatapkg.TrailerPrequeueItem, error)
	ServePrequeuedTrailer(id string, w http.ResponseWriter, r *http.Request) error
	// Progress tracking for long-running enrichment operations
	GetProgressSnapshot() metadatapkg.ProgressSnapshot
	// MDBList rating helpers for watchlist/list rating sort
	MDBListIsEnabled() bool
	GetMDBListAllRatings(ctx context.Context, imdbID, mediaType string) ([]models.Rating, error)
	GetMDBListAllRatingsCached(imdbID, mediaType string) []models.Rating
	// Poster helpers
	GetTextPosterURL(mediaType string, tmdbID int64, tvdbID int64) string
	// Top Ten aggregated ranking
	GetTopTen(ctx context.Context, mediaType string, customListURLs []string) ([]models.TrendingItem, error)
}

type topTenDebugService interface {
	GetTopTenDebug(ctx context.Context, mediaType string, customListURLs []string) ([]models.TrendingItem, []metadatapkg.TopTenDebugEntry, error)
}

var _ metadataService = (*metadatapkg.Service)(nil)

// userSettingsProvider retrieves per-user settings.
type userSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

// historyServiceInterface provides access to watch history for filtering and watch state enrichment.
type historyServiceInterface interface {
	GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error)
	ListWatchHistory(userID string) ([]models.WatchHistoryItem, error)
	ListContinueWatching(userID string) ([]models.SeriesWatchState, error)
	ListSeriesStates(userID string) ([]models.SeriesWatchState, error)
	ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error)
}

// watchlistLister provides access to a user's watchlist for recommendations.
type watchlistLister interface {
	List(userID string) ([]models.WatchlistItem, error)
}

// usersServiceInterface provides access to user profiles for kids filtering.
type usersServiceInterface interface {
	Get(id string) (models.User, bool)
}

type accountsServiceInterface interface {
	Get(id string) (models.Account, bool)
}

type MetadataHandler struct {
	Service          metadataService
	CfgManager       *config.Manager
	UserSettings     userSettingsProvider
	HistoryService   historyServiceInterface
	UsersService     usersServiceInterface
	AccountsService  accountsServiceInterface
	WatchlistService watchlistLister
	TraktClient      *trakt.Client
}

func NewMetadataHandler(s metadataService, cfgManager *config.Manager) *MetadataHandler {
	return &MetadataHandler{Service: s, CfgManager: cfgManager}
}

// SetUserSettingsProvider sets the user settings provider for per-user settings.
func (h *MetadataHandler) SetUserSettingsProvider(provider userSettingsProvider) {
	h.UserSettings = provider
}

// SetHistoryService sets the history service for filtering watched content.
func (h *MetadataHandler) SetHistoryService(service historyServiceInterface) {
	h.HistoryService = service
}

// SetUsersService sets the users service for kids profile filtering.
func (h *MetadataHandler) SetUsersService(service usersServiceInterface) {
	h.UsersService = service
}

func (h *MetadataHandler) SetAccountsService(service accountsServiceInterface) {
	h.AccountsService = service
}

// SetWatchlistService sets the watchlist service for AI recommendations.
func (h *MetadataHandler) SetWatchlistService(service watchlistLister) {
	h.WatchlistService = service
}

func (h *MetadataHandler) SetTraktClient(client *trakt.Client) {
	h.TraktClient = client
}

// DiscoverNewResponse wraps trending items with total count for pagination
type DiscoverNewResponse struct {
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"` // Pre-filter total (only set when hideUnreleased is used)
}

type TopTenResponse struct {
	Items []models.TrendingItem          `json:"items"`
	Total int                            `json:"total"`
	Debug []metadatapkg.TopTenDebugEntry `json:"debug,omitempty"`
}

func (h *MetadataHandler) DiscoverNew(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"
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

	items, err := h.Service.Trending(r.Context(), mediaType)
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

	// Apply watched filter if requested (requires userID and history service)
	if hideWatched && userID != "" && h.HistoryService != nil {
		items = filterWatchedItems(items, userID, h.HistoryService)
	}

	// Apply kids rating filter if user is a kids profile
	if userID != "" && h.UsersService != nil {
		if user, ok := h.UsersService.Get(userID); ok && user.IsKidsProfile {
			if user.KidsMode == "rating" {
				movieRating := user.KidsMaxMovieRating
				tvRating := user.KidsMaxTVRating
				if movieRating == "" && tvRating == "" && user.KidsMaxRating != "" {
					movieRating = user.KidsMaxRating
					tvRating = user.KidsMaxRating
				}
				items = kids.FilterTrendingByRatings(items, movieRating, tvRating)
			}
		}
	}

	// Enrich with pre-computed watch state if user context is available
	if userID != "" && h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			idx := buildWatchStateIndex(wh, cw, pp)
			enrichTrendingItems(items, idx)
		}
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

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	resp := DiscoverNewResponse{Items: items, Total: total}
	if hideUnreleased || hideWatched {
		resp.UnfilteredTotal = unfilteredTotal
	}
	json.NewEncoder(w).Encode(resp)
}

func (h *MetadataHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))

	// Check kids profile restrictions before searching
	if userID != "" && h.UsersService != nil {
		if user, ok := h.UsersService.Get(userID); ok && user.IsKidsProfile {
			if user.KidsMode == "content_list" {
				// Search is disabled for curated-list profiles
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode([]models.SearchResult{})
				return
			}
		}
	}

	results, err := h.Service.Search(r.Context(), q, mediaType)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Apply kids rating filter if user is a kids profile with rating mode
	if userID != "" && h.UsersService != nil {
		if user, ok := h.UsersService.Get(userID); ok && user.IsKidsProfile && user.KidsMode == "rating" {
			// Enrich results with certification data
			h.Service.EnrichSearchCertifications(r.Context(), results)

			movieRating := user.KidsMaxMovieRating
			tvRating := user.KidsMaxTVRating
			if movieRating == "" && tvRating == "" && user.KidsMaxRating != "" {
				movieRating = user.KidsMaxRating
				tvRating = user.KidsMaxRating
			}
			results = kids.FilterSearchByRatings(results, movieRating, tvRating)
		}
	}

	// Ensure we return [] instead of null for empty results
	if results == nil {
		results = []models.SearchResult{}
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
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "404 Not Found") || strings.Contains(err.Error(), "unable to resolve") {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
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

	var results []models.BatchSeriesDetailsItem
	if len(req.Fields) > 0 {
		results = h.Service.BatchSeriesTitleFields(r.Context(), req.Queries, req.Fields)
	} else {
		results = h.Service.BatchSeriesDetails(r.Context(), req.Queries)
	}

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

func (h *MetadataHandler) CollectionDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	collectionIDStr := strings.TrimSpace(query.Get("id"))
	if collectionIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "collection id is required"})
		return
	}

	collectionID, err := strconv.ParseInt(collectionIDStr, 10, 64)
	if err != nil || collectionID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid collection id"})
		return
	}

	log.Printf("[metadata] fetching collection details collectionId=%d", collectionID)

	details, err := h.Service.CollectionDetails(r.Context(), collectionID)
	if err != nil {
		log.Printf("[metadata] collection details error collectionId=%d err=%v", collectionID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	log.Printf("[metadata] collection details success collectionId=%d name=%q movieCount=%d", collectionID, details.Name, len(details.Movies))
	for i, movie := range details.Movies {
		log.Printf("[metadata]   movie[%d]: id=%s name=%q year=%d hasPoster=%v", i, movie.ID, movie.Name, movie.Year, movie.Poster != nil)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}

func (h *MetadataHandler) Similar(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	mediaType := strings.ToLower(strings.TrimSpace(query.Get("type")))
	tmdbIDStr := strings.TrimSpace(query.Get("tmdbId"))

	if tmdbIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "tmdbId is required"})
		return
	}

	tmdbID, err := strconv.ParseInt(tmdbIDStr, 10, 64)
	if err != nil || tmdbID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid tmdbId"})
		return
	}

	titles, err := h.Service.Similar(r.Context(), mediaType, tmdbID)
	if err != nil {
		log.Printf("[metadata] similar error type=%s tmdbId=%d err=%v", mediaType, tmdbID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Return empty array instead of null if no results
	if titles == nil {
		titles = []models.Title{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(titles)
}

func (h *MetadataHandler) PersonDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	personIDStr := strings.TrimSpace(query.Get("id"))
	if personIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "person id is required"})
		return
	}

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil || personID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid person id"})
		return
	}

	details, err := h.Service.PersonDetails(r.Context(), personID)
	if err != nil {
		log.Printf("[metadata] person details error personId=%d err=%v", personID, err)
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

// isYouTubeURL validates that the given URL actually points to a YouTube domain
// by parsing the URL and checking the hostname. A simple strings.Contains check
// would allow URLs like http://attacker.com/youtube.com to pass.
func isYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "youtube.com" || host == "www.youtube.com" || host == "m.youtube.com" ||
		host == "youtu.be" || host == "www.youtu.be"
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
	if !isYouTubeURL(videoURL) {
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
	if !isYouTubeURL(videoURL) {
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
	if !isYouTubeURL(videoURL) {
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

// filterWatchedItems removes items that have been fully watched by the user.
// For movies: filters out items where WatchHistoryItem.Watched == true.
// For series: filters out items where the series-level WatchHistoryItem.Watched == true.
// Partially watched items (with playback progress but not marked as watched) are NOT filtered.
func filterWatchedItems(items []models.TrendingItem, userID string, historySvc historyServiceInterface) []models.TrendingItem {
	if userID == "" || historySvc == nil {
		return items // Can't filter without user context
	}

	result := make([]models.TrendingItem, 0, len(items))
	filteredCount := 0
	for _, item := range items {
		// Build item ID for watch history lookup
		itemID := buildItemIDForHistory(item)
		if itemID == "" {
			// Can't determine ID, include by default
			result = append(result, item)
			continue
		}

		mediaType := item.Title.MediaType
		if mediaType == "" {
			// Unknown type - include by default
			result = append(result, item)
			continue
		}

		// Check if item is marked as watched
		watchItem, _ := historySvc.GetWatchHistoryItem(userID, mediaType, itemID)
		if watchItem == nil || !watchItem.Watched {
			// Not watched or not found - include it
			result = append(result, item)
		} else {
			filteredCount++
			if filteredCount <= 3 {
				log.Printf("[hideWatched] filtered %s: %s (itemID=%s)", mediaType, item.Title.Name, itemID)
			}
		}
	}
	log.Printf("[hideWatched] filter result: %d/%d items kept (filtered %d)", len(result), len(items), filteredCount)
	return result
}

// buildItemIDForHistory constructs the item ID used in watch history from a TrendingItem.
// Format: "tmdb:movie:12345" or "tvdb:123456" or "tmdb:tv:67890"
func buildItemIDForHistory(item models.TrendingItem) string {
	// Prefer TVDB ID for series (matches the storage format used by history service)
	if item.Title.MediaType == "series" && item.Title.TVDBID > 0 {
		return fmt.Sprintf("tvdb:%d", item.Title.TVDBID)
	}
	// For movies, prefer TMDB ID
	if item.Title.MediaType == "movie" && item.Title.TMDBID > 0 {
		return fmt.Sprintf("tmdb:movie:%d", item.Title.TMDBID)
	}
	// Fallback to TMDB for series
	if item.Title.MediaType == "series" && item.Title.TMDBID > 0 {
		return fmt.Sprintf("tmdb:tv:%d", item.Title.TMDBID)
	}
	// Fallback to TVDB for movies
	if item.Title.MediaType == "movie" && item.Title.TVDBID > 0 {
		return fmt.Sprintf("tvdb:movie:%d", item.Title.TVDBID)
	}
	// Use item ID if available
	if item.Title.ID != "" {
		return item.Title.ID
	}
	return ""
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

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"

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

	// Build options — filtering + pagination handled inside the service
	opts := metadatapkg.CustomListOptions{
		Limit:          limit,
		Offset:         offset,
		HideUnreleased: hideUnreleased,
		HideWatched:    hideWatched,
		UserID:         userID,
		Label:          strings.TrimSpace(r.URL.Query().Get("name")),
	}
	if hideWatched && userID != "" && h.HistoryService != nil {
		opts.HistorySvc = h.HistoryService
	}

	items, filteredTotal, unfilteredTotal, err := h.Service.GetCustomList(r.Context(), listURL, opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	resp := CustomListResponse{Items: items, Total: filteredTotal}
	if hideUnreleased || hideWatched {
		resp.UnfilteredTotal = unfilteredTotal
	}
	json.NewEncoder(w).Encode(resp)
}

type traktShelfSourceItem struct {
	Title     string
	Year      int
	MediaType string
	IMDBID    string
	TMDBID    string
	TVDBID    string
	TraktID   string
	ListedAt  int64
}

type traktShelfDedupeItem struct {
	item traktShelfSourceItem
}

type TraktShelfResponse struct {
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"`
}

// TraktList returns enriched Trakt watchlist/custom-list items for use in home shelves.
func (h *MetadataHandler) TraktList(w http.ResponseWriter, r *http.Request) {
	if h.TraktClient == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "trakt client unavailable"})
		return
	}
	if h.UsersService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "users service unavailable"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "userId parameter required"})
		return
	}
	user, ok := h.UsersService.Get(userID)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "user not found"})
		return
	}

	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	listType := strings.TrimSpace(r.URL.Query().Get("listType"))
	listID := strings.TrimSpace(r.URL.Query().Get("listId"))
	if accountID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "accountId parameter required"})
		return
	}
	if listType != "watchlist" && listType != "custom" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid listType"})
		return
	}
	if listType == "custom" && listID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "listId parameter required for custom lists"})
		return
	}
	if accountID == "__all__" && listType != "watchlist" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "all accounts is only supported for watchlists"})
		return
	}

	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"
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

	settings, err := h.CfgManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to load settings"})
		return
	}

	traktAccounts, err := h.resolveTraktShelfAccounts(user, settings, accountID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusForbidden
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	sourceItems, err := h.fetchTraktShelfItems(r.Context(), settings, traktAccounts, listType, listID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	curated := make([]metadatapkg.CuratedItem, 0, len(sourceItems))
	for _, item := range sourceItems {
		curated = append(curated, metadatapkg.CuratedItem{
			Title:     item.Title,
			Year:      item.Year,
			IMDBID:    item.IMDBID,
			MediaType: item.MediaType,
		})
	}

	label := strings.TrimSpace(r.URL.Query().Get("name"))
	if label == "" {
		if listType == "watchlist" {
			label = "Trakt Watchlist"
		} else {
			label = "Trakt List"
		}
	}

	items, err := h.Service.GetCuratedList(r.Context(), curated, label)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	unfilteredTotal := len(items)
	if hideUnreleased {
		items = filterUnreleasedItems(items)
	}
	if hideWatched && h.HistoryService != nil {
		items = filterWatchedItems(items, userID, h.HistoryService)
	}
	filteredTotal := len(items)

	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		items = []models.TrendingItem{}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	resp := TraktShelfResponse{
		Items: items,
		Total: filteredTotal,
	}
	if hideUnreleased || hideWatched {
		resp.UnfilteredTotal = unfilteredTotal
	}
	json.NewEncoder(w).Encode(resp)
}

func (h *MetadataHandler) resolveTraktShelfAccounts(user models.User, settings config.Settings, requestedAccountID string) ([]config.TraktAccount, error) {
	if requestedAccountID == "__all__" {
		if h.AccountsService == nil {
			return nil, fmt.Errorf("accounts service unavailable")
		}
		account, ok := h.AccountsService.Get(user.AccountID)
		if !ok || !account.IsMaster {
			return nil, fmt.Errorf("all trakt accounts are only available to master accounts")
		}
		accounts := make([]config.TraktAccount, 0, len(settings.Trakt.Accounts))
		for _, acc := range settings.Trakt.Accounts {
			if strings.TrimSpace(acc.AccessToken) != "" {
				accounts = append(accounts, acc)
			}
		}
		return accounts, nil
	}

	acc := settings.Trakt.GetAccountByID(requestedAccountID)
	if acc == nil {
		return nil, fmt.Errorf("trakt account not found")
	}

	if h.AccountsService != nil {
		if account, ok := h.AccountsService.Get(user.AccountID); ok && account.IsMaster {
			return []config.TraktAccount{*acc}, nil
		}
	}
	if acc.OwnerAccountID != "" && acc.OwnerAccountID != user.AccountID {
		return nil, fmt.Errorf("trakt account is not available to this profile")
	}

	return []config.TraktAccount{*acc}, nil
}

func (h *MetadataHandler) fetchTraktShelfItems(ctx context.Context, settings config.Settings, accounts []config.TraktAccount, listType, listID string) ([]traktShelfSourceItem, error) {
	_ = ctx
	seen := make(map[string]traktShelfDedupeItem)

	for _, account := range accounts {
		accountCopy := account
		accessToken, err := h.TraktClient.EnsureValidToken(&accountCopy, h.CfgManager)
		if err != nil {
			return nil, fmt.Errorf("validate trakt token for %s: %w", account.Name, err)
		}
		if accessToken == "" {
			continue
		}

		h.TraktClient.UpdateCredentials(account.ClientID, account.ClientSecret)

		switch listType {
		case "watchlist":
			watchlistItems, err := h.TraktClient.GetAllWatchlist(accessToken)
			if err != nil {
				return nil, fmt.Errorf("fetch trakt watchlist for %s: %w", account.Name, err)
			}
			for _, item := range watchlistItems {
				normalized, ok := normalizeTraktWatchlistItem(item)
				if ok {
					upsertTraktShelfItem(seen, normalized)
				}
			}
		case "custom":
			listItems, err := h.TraktClient.GetAllListItems(accessToken, listID)
			if err != nil {
				return nil, fmt.Errorf("fetch trakt list for %s: %w", account.Name, err)
			}
			for _, item := range listItems {
				normalized, ok := normalizeTraktCustomListItem(item)
				if ok {
					upsertTraktShelfItem(seen, normalized)
				}
			}
		}
	}

	items := make([]traktShelfSourceItem, 0, len(seen))
	for _, entry := range seen {
		items = append(items, entry.item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ListedAt > items[j].ListedAt
	})
	return items, nil
}

func normalizeTraktWatchlistItem(item trakt.WatchlistItem) (traktShelfSourceItem, bool) {
	switch {
	case item.Movie != nil:
		ids := trakt.IDsToMap(item.Movie.IDs)
		return traktShelfSourceItem{
			Title:     item.Movie.Title,
			Year:      item.Movie.Year,
			MediaType: "movie",
			IMDBID:    ids["imdb"],
			TMDBID:    ids["tmdb"],
			TVDBID:    ids["tvdb"],
			TraktID:   ids["trakt"],
			ListedAt:  item.ListedAt.Unix(),
		}, true
	case item.Show != nil:
		ids := trakt.IDsToMap(item.Show.IDs)
		return traktShelfSourceItem{
			Title:     item.Show.Title,
			Year:      item.Show.Year,
			MediaType: "series",
			IMDBID:    ids["imdb"],
			TMDBID:    ids["tmdb"],
			TVDBID:    ids["tvdb"],
			TraktID:   ids["trakt"],
			ListedAt:  item.ListedAt.Unix(),
		}, true
	default:
		return traktShelfSourceItem{}, false
	}
}

func normalizeTraktCustomListItem(item trakt.ListItem) (traktShelfSourceItem, bool) {
	switch {
	case item.Movie != nil:
		ids := trakt.IDsToMap(item.Movie.IDs)
		return traktShelfSourceItem{
			Title:     item.Movie.Title,
			Year:      item.Movie.Year,
			MediaType: "movie",
			IMDBID:    ids["imdb"],
			TMDBID:    ids["tmdb"],
			TVDBID:    ids["tvdb"],
			TraktID:   ids["trakt"],
			ListedAt:  item.ListedAt.Unix(),
		}, true
	case item.Show != nil:
		ids := trakt.IDsToMap(item.Show.IDs)
		return traktShelfSourceItem{
			Title:     item.Show.Title,
			Year:      item.Show.Year,
			MediaType: "series",
			IMDBID:    ids["imdb"],
			TMDBID:    ids["tmdb"],
			TVDBID:    ids["tvdb"],
			TraktID:   ids["trakt"],
			ListedAt:  item.ListedAt.Unix(),
		}, true
	default:
		return traktShelfSourceItem{}, false
	}
}

func upsertTraktShelfItem(seen map[string]traktShelfDedupeItem, item traktShelfSourceItem) {
	keyParts := []string{item.MediaType}
	switch {
	case item.IMDBID != "":
		keyParts = append(keyParts, "imdb", item.IMDBID)
	case item.TMDBID != "":
		keyParts = append(keyParts, "tmdb", item.TMDBID)
	case item.TVDBID != "":
		keyParts = append(keyParts, "tvdb", item.TVDBID)
	case item.TraktID != "":
		keyParts = append(keyParts, "trakt", item.TraktID)
	default:
		keyParts = append(keyParts, "title", strings.ToLower(strings.TrimSpace(item.Title)), strconv.Itoa(item.Year))
	}
	key := strings.Join(keyParts, ":")
	existing, ok := seen[key]
	if !ok || item.ListedAt > existing.item.ListedAt {
		seen[key] = traktShelfDedupeItem{item: item}
	}
}

// CuratedList enriches a caller-provided list of items (POST with JSON body).
func (h *MetadataHandler) CuratedList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []metadatapkg.CuratedItem `json:"items"`
		Label string                    `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON body"})
		return
	}
	if len(req.Items) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "items must not be empty"})
		return
	}

	items, err := h.Service.GetCuratedList(r.Context(), req.Items, req.Label)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Enrich with watch state if user context is available
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID != "" && h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			idx := buildWatchStateIndex(wh, cw, pp)
			enrichTrendingItems(items, idx)
		}
	}

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
}

// DiscoverByGenre returns TMDB discover results for a specific genre
func (h *MetadataHandler) DiscoverByGenre(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	genreIDStr := strings.TrimSpace(r.URL.Query().Get("genreId"))

	if genreIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "genreId is required"})
		return
	}

	genreID, err := strconv.ParseInt(genreIDStr, 10, 64)
	if err != nil || genreID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid genreId"})
		return
	}

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

	items, total, err := h.Service.DiscoverByGenre(r.Context(), mediaType, genreID, limit, offset)
	if err != nil {
		log.Printf("[metadata] discover genre error type=%s genreId=%d: %v", mediaType, genreID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, h.Service)
	log.Printf(
		"[metadata] discover genre handler complete type=%s genreId=%d limit=%d offset=%d count=%d total=%d duration=%s",
		mediaType,
		genreID,
		limit,
		offset,
		len(items),
		total,
		time.Since(start).Round(time.Millisecond),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DiscoverNewResponse{Items: items, Total: total})
}

// GetAIRecommendations returns Gemini-powered personalized recommendations.
// It collects the user's watched titles from history and watchlist, then
// asks Gemini for recommendations and resolves them to TMDB titles.
func (h *MetadataHandler) GetAIRecommendations(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "userId is required"})
		return
	}

	// Collect watched titles from watch history and watchlist
	var watchedTitles []string
	var mediaTypes []string
	seen := make(map[string]bool)

	// From watch history (recently watched items)
	if h.HistoryService != nil {
		history, err := h.HistoryService.ListWatchHistory(userID)
		if err == nil {
			for _, item := range history {
				if item.Watched && item.Name != "" {
					key := item.Name
					if item.MediaType == "episode" && item.SeriesName != "" {
						key = item.SeriesName
					}
					if !seen[key] {
						seen[key] = true
						mt := item.MediaType
						if mt == "episode" {
							mt = "series"
						}
						watchedTitles = append(watchedTitles, key)
						mediaTypes = append(mediaTypes, mt)
					}
				}
			}
		}
	}

	// From watchlist
	if h.WatchlistService != nil {
		wl, err := h.WatchlistService.List(userID)
		if err == nil {
			for _, item := range wl {
				if item.Name != "" && !seen[item.Name] {
					seen[item.Name] = true
					watchedTitles = append(watchedTitles, item.Name)
					mediaTypes = append(mediaTypes, item.MediaType)
				}
			}
		}
	}

	// Limit to most recent 30 titles to avoid huge prompts
	if len(watchedTitles) > 30 {
		watchedTitles = watchedTitles[:30]
		mediaTypes = mediaTypes[:30]
	}

	if len(watchedTitles) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0})
		return
	}

	items, err := h.Service.GetAIRecommendations(r.Context(), watchedTitles, mediaTypes, userID)
	if err != nil {
		log.Printf("[metadata] ai recommendations error user=%s: %v", userID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DiscoverNewResponse{Items: items, Total: len(items)})
}

// GetAISimilar returns AI-powered recommendations similar to a specific title.
func (h *MetadataHandler) GetAISimilar(w http.ResponseWriter, r *http.Request) {
	seedTitle := strings.TrimSpace(r.URL.Query().Get("title"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("type"))
	if seedTitle == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "title is required"})
		return
	}
	if mediaType == "" {
		mediaType = "movie"
	}

	// Detach from the request context — the model takes ~35s and the client
	// will cancel the HTTP connection before it finishes. Use a background
	// context with a generous timeout so the result is always cached even
	// if the client navigates away.
	aiCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	items, err := h.Service.GetAISimilar(aiCtx, seedTitle, mediaType)
	if err != nil {
		log.Printf("[metadata] ai similar error seed=%q: %v", seedTitle, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DiscoverNewResponse{Items: items, Total: len(items)})
}

// GetAICustomRecommendations returns recommendations based on a free-text query.
func (h *MetadataHandler) GetAICustomRecommendations(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "q (query) is required"})
		return
	}

	items, err := h.Service.GetAICustomRecommendations(r.Context(), query)
	if err != nil {
		log.Printf("[metadata] ai custom recommendations error query=%q: %v", query, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(DiscoverNewResponse{Items: items, Total: len(items)})
}

// GetAISurprise returns a single random movie/show recommendation.
func (h *MetadataHandler) GetAISurprise(w http.ResponseWriter, r *http.Request) {
	decade := strings.TrimSpace(r.URL.Query().Get("decade"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("mediaType"))
	item, err := h.Service.GetAISurprise(r.Context(), decade, mediaType)
	if err != nil {
		log.Printf("[metadata] ai surprise error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// TopTen handles GET /api/discover/top-ten
// Aggregates trending movies/TV, all enabled MDBList shelves, and TMDB genre
// discovery to produce a cross-list scored top-10 ranking for today.
//
// Query params:
//
//	type: "all" (default), "movie", or "tv"
func (h *MetadataHandler) TopTen(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if mediaType == "" {
		mediaType = "all"
	}
	debugMode := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("debug")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("debug")), "true")

	w.Header().Set("X-TopTen-Handler", "metadata-handler-v1")
	log.Printf("[topten] starting (mediaType=%s)", mediaType)
	var (
		items []models.TrendingItem
		debug []metadatapkg.TopTenDebugEntry
		err   error
	)
	if debugMode {
		if svc, ok := h.Service.(topTenDebugService); ok {
			items, debug, err = svc.GetTopTenDebug(r.Context(), mediaType, nil)
		} else {
			items, err = h.Service.GetTopTen(r.Context(), mediaType, nil)
		}
	} else {
		items, err = h.Service.GetTopTen(r.Context(), mediaType, nil)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	enrichTrendingRatings(items, h.Service)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TopTenResponse{Items: items, Total: len(items), Debug: debug})
}

// GetProgress returns a snapshot of active metadata enrichment progress.
func (h *MetadataHandler) GetProgress(w http.ResponseWriter, r *http.Request) {
	snapshot := h.Service.GetProgressSnapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

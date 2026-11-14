package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/kids"
	"novastream/services/letterboxd"
	"novastream/services/mdblist"
	metadatapkg "novastream/services/metadata"
	"novastream/services/publicmetadb"
	"novastream/services/simkl"
	"novastream/services/trakt"

	"golang.org/x/sync/errgroup"
)

type metadataService interface {
	Trending(context.Context, string) ([]models.TrendingItem, error)
	Search(context.Context, string, string) ([]models.SearchResult, error)
	SearchYouTubeVideos(context.Context, string, int) ([]models.YouTubeVideoSearchResult, error)
	EnrichSearchCertifications(context.Context, []models.SearchResult)
	EnrichTrendingCertifications(context.Context, []models.TrendingItem)
	EnrichTitleCertifications(context.Context, []models.Title)
	SeriesDetails(context.Context, models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	BatchSeriesDetails(context.Context, []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem
	BatchSeriesTitleFields(context.Context, []models.SeriesDetailsQuery, []string) []models.BatchSeriesDetailsItem
	MovieDetails(context.Context, models.MovieDetailsQuery) (*models.Title, error)
	BatchMovieReleases(context.Context, []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem
	CollectionDetails(context.Context, int64) (*models.CollectionDetails, error)
	Similar(context.Context, string, int64) ([]models.Title, error)
	DiscoverByGenre(context.Context, string, int64, int, int) ([]models.TrendingItem, int, error)
	DiscoverByDecade(context.Context, string, int, int, int) ([]models.TrendingItem, int, error)
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
	GetCachedArtworkURLs(mediaType string, tmdbID int64, tvdbID int64) (string, string, []string)
	ApplyLocalizedArtwork(ctx context.Context, title *models.Title) bool
	GetCachedOverview(mediaType string, tmdbID int64, tvdbID int64) string
	// Top Ten aggregated ranking
	GetTopTen(ctx context.Context, mediaType string, customListURLs []string) ([]models.TrendingItem, error)
}

type trendingOptionsService interface {
	TrendingWithOptions(context.Context, string, metadatapkg.ShelfLoadOptions) ([]models.TrendingItem, error)
}

type tmdbListService interface {
	GetTMDBList(context.Context, metadatapkg.TMDBListOptions) ([]models.TrendingItem, int, error)
}

type tmdbSourceSearchService interface {
	SearchTMDBSources(context.Context, string, string) ([]metadatapkg.TMDBSourceResult, error)
}

type discoverByGenreOptionsService interface {
	DiscoverByGenreWithOptions(context.Context, string, int64, int, int, metadatapkg.ShelfLoadOptions) ([]models.TrendingItem, int, error)
}

type discoverByDecadeOptionsService interface {
	DiscoverByDecadeWithOptions(context.Context, string, int, int, int, metadatapkg.ShelfLoadOptions) ([]models.TrendingItem, int, error)
}

type movieTitleFieldsService interface {
	BatchMovieTitleFields(context.Context, []models.MovieDetailsQuery, []string) []models.BatchMovieTitleFieldsItem
}

func (h *MetadataHandler) SearchYouTubeVideos(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "query is required"})
		return
	}
	if len([]rune(query)) < 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "query must be at least 2 characters"})
		return
	}

	limit := 10
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}

	results, err := h.Service.SearchYouTubeVideos(r.Context(), query, limit)
	if err != nil {
		log.Printf("[metadata] youtube search error query=%q err=%v", query, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}
	if results == nil {
		results = []models.YouTubeVideoSearchResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, results)
}

type topTenDebugService interface {
	GetTopTenDebug(ctx context.Context, mediaType string, customListURLs []string) ([]models.TrendingItem, []metadatapkg.TopTenDebugEntry, error)
}

type topTenCandidatesService interface {
	GetTopTenCandidates(ctx context.Context, mediaType string, customListURLs []string) ([]models.TrendingItem, error)
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

type sharedShelfHistoryService interface {
	AggregatePopularTitles(eligibleUsers map[string]bool, windowDays, minViews int) []models.PopularTitle
	ListRecentWatches(eligibleUsers map[string]models.User, windowDays int, maxPerProfile int) []models.RecentWatch
}

// watchlistLister provides access to a user's watchlist for recommendations.
type watchlistLister interface {
	List(userID string) ([]models.WatchlistItem, error)
}

// usersServiceInterface provides access to user profiles for kids filtering.
type usersServiceInterface interface {
	Get(id string) (models.User, bool)
}

type sharedShelfUsersService interface {
	ListAll() []models.User
}

type accountsServiceInterface interface {
	Get(id string) (models.Account, bool)
}

type MetadataHandler struct {
	SourceAvailability sourceAvailabilityService
	Service            metadataService
	CfgManager         *config.Manager
	UserSettings       userSettingsProvider
	ClientSettings     clientSettingsService
	HistoryService     historyServiceInterface
	UsersService       usersServiceInterface
	AccountsService    accountsServiceInterface
	WatchlistService   watchlistLister
	TraktClient        *trakt.Client
	SimklClient        *simkl.Client
	MDBListListsClient *mdblist.ListsClient
	PublicMetaDBClient *publicmetadb.Client
	LetterboxdClient   *letterboxd.Client
	stremioHTTPClient  *http.Client

	personalizedMu       sync.Mutex
	personalizedCache    map[string]personalizedRecommendationsCacheEntry
	personalizedInFlight map[string]*personalizedRecommendationsBuild

	popularCacheMu       sync.Mutex
	popularCache         *cachedPopularOnServer
	recentWatchesCacheMu sync.Mutex
	recentWatchesCache   *cachedRecentlyWatched
	stremioCatalogMu     sync.Mutex
	stremioCatalogCache  map[string]stremioShelfCatalogCacheEntry
}

func NewMetadataHandler(s metadataService, cfgManager *config.Manager) *MetadataHandler {
	h := &MetadataHandler{
		Service:              s,
		CfgManager:           cfgManager,
		personalizedCache:    make(map[string]personalizedRecommendationsCacheEntry),
		personalizedInFlight: make(map[string]*personalizedRecommendationsBuild),
		stremioCatalogCache:  make(map[string]stremioShelfCatalogCacheEntry),
	}
	h.stremioHTTPClient = newStremioShelfHTTPClient(h.stremioShelfHostPolicy)
	return h
}

// SetUserSettingsProvider sets the user settings provider for per-user settings.
func (h *MetadataHandler) SetUserSettingsProvider(provider userSettingsProvider) {
	h.UserSettings = provider
}

func (h *MetadataHandler) SetClientSettingsProvider(provider clientSettingsService) {
	h.ClientSettings = provider
}

// SetHistoryService sets the history service for filtering watched content.
func (h *MetadataHandler) SetHistoryService(service historyServiceInterface) {
	h.HistoryService = service
}

// SetUsersService sets the users service for kids profile filtering.
func (h *MetadataHandler) SetUsersService(service usersServiceInterface) {
	h.UsersService = service
}

// kidsRatingLimits resolves the (movie, tv) max ratings for a kids profile in
// rating mode. ok is false when no rating filtering should be applied (profile
// is not a kids profile, not in rating mode, or has no rating configured).
func (h *MetadataHandler) kidsRatingLimits(userID string) (movieRating, tvRating string, ok bool) {
	if userID == "" || h.UsersService == nil {
		return "", "", false
	}
	user, found := h.UsersService.Get(userID)
	if !found || !user.IsKidsProfile || user.KidsMode != "rating" {
		return "", "", false
	}
	movieRating = user.KidsMaxMovieRating
	tvRating = user.KidsMaxTVRating
	if movieRating == "" && tvRating == "" && user.KidsMaxRating != "" {
		movieRating = user.KidsMaxRating
		tvRating = user.KidsMaxRating
	}
	if movieRating == "" && tvRating == "" {
		return "", "", false
	}
	return movieRating, tvRating, true
}

// filterTrendingByKids enriches certifications and filters trending items by the
// kids rating limit for the given profile. It is a no-op for non-kids profiles
// or profiles not in rating mode.
func (h *MetadataHandler) filterTrendingByKids(ctx context.Context, userID string, service metadataService, items []models.TrendingItem) []models.TrendingItem {
	movieRating, tvRating, ok := h.kidsRatingLimits(userID)
	if !ok {
		return items
	}
	// Populate certifications so items without one aren't all fail-closed.
	service.EnrichTrendingCertifications(ctx, items)
	return kids.FilterTrendingByRatings(items, movieRating, tvRating)
}

// filterTitlesByKids enriches certifications and filters titles by the kids
// rating limit for the given profile. No-op for non-kids/rating profiles.
func (h *MetadataHandler) filterTitlesByKids(ctx context.Context, userID string, service metadataService, titles []models.Title) []models.Title {
	movieRating, tvRating, ok := h.kidsRatingLimits(userID)
	if !ok {
		return titles
	}
	service.EnrichTitleCertifications(ctx, titles)
	return kids.FilterTitlesByRatings(titles, movieRating, tvRating)
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

// SetSimklClient sets the Simkl client for Simkl-backed home shelves.
func (h *MetadataHandler) SetSimklClient(client *simkl.Client) {
	h.SimklClient = client
}

// SetMDBListListsClient sets the MDBList lists client used for Letterboxd-backed
// home shelves (sourced via MDBList external lists).
func (h *MetadataHandler) SetMDBListListsClient(client *mdblist.ListsClient) {
	h.MDBListListsClient = client
}

// SetLetterboxdClient sets the public Letterboxd list client.
func (h *MetadataHandler) SetLetterboxdClient(client *letterboxd.Client) {
	h.LetterboxdClient = client
}

func (h *MetadataHandler) serviceForUser(userID string) metadataService {
	return metadataServiceForUser(h.Service, h.CfgManager, h.UserSettings, userID)
}

// DiscoverNewResponse wraps trending items with total count for pagination
type DiscoverNewResponse struct {
	MetadataPending bool                  `json:"metadataPending,omitempty"`
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"` // Pre-filter total (only set when hideUnreleased is used)
	Genres          []string              `json:"genres,omitempty"`
	AlphabetBuckets []string              `json:"alphabetBuckets,omitempty"`
}

type TopTenResponse struct {
	Items []models.TrendingItem          `json:"items"`
	Total int                            `json:"total"`
	Debug []metadatapkg.TopTenDebugEntry `json:"debug,omitempty"`
}

func parseShelfLoadOptions(r *http.Request) metadatapkg.ShelfLoadOptions {
	opts := metadatapkg.ShelfLoadOptions{
		DeferArtwork:  strings.EqualFold(r.URL.Query().Get("deferArtwork"), "true"),
		Lite:          strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lite"))) == "true",
		SortBy:        strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sortBy"))),
		SortDirection: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sortDirection"))),
	}
	if artworkLimitStr := r.URL.Query().Get("artworkLimit"); artworkLimitStr != "" {
		if parsed, err := strconv.Atoi(artworkLimitStr); err == nil && parsed > 0 {
			opts.ArtworkLimit = parsed
		}
	}
	return opts
}

func (h *MetadataHandler) DiscoverNew(w http.ResponseWriter, r *http.Request) {
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	service := h.serviceForUser(userID)
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

	loadOpts := parseShelfLoadOptions(r)
	var items []models.TrendingItem
	var err error
	if svc, ok := service.(trendingOptionsService); ok {
		items, err = svc.TrendingWithOptions(r.Context(), mediaType, loadOpts)
	} else {
		items, err = service.Trending(r.Context(), mediaType)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	// Track pre-filter total for explore card logic
	unfilteredTotal := len(items)

	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if hideUnreleased {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)

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
			enrichTrendingItems(items, idx, service, strings.EqualFold(r.URL.Query().Get("includeUnwatchedCounts"), "true"))
		}
	}

	// Apply the shared query before pagination.
	enrichTrendingRatings(items, service)
	genres := displayListGenres(items)
	query := parseDisplayListQuery(r)
	alphabet := displayListAlphabetBuckets(items, query)
	if query.Active() {
		items = query.Apply(items)
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
	resp := DiscoverNewResponse{Items: items, Total: total, Genres: genres, AlphabetBuckets: alphabet}
	if hideUnreleased || hideWatched {
		resp.UnfilteredTotal = unfilteredTotal
	}
	h.encodeMetadata(w, r, resp)
}

func (h *MetadataHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		q = r.URL.Query().Get("query")
	}
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	service := h.serviceForUser(userID)

	// Check kids profile restrictions before searching
	if userID != "" && h.UsersService != nil {
		if user, ok := h.UsersService.Get(userID); ok && user.IsKidsProfile {
			if user.KidsMode == "content_list" || mediaType == "person" {
				// Curated profiles cannot search; kids cannot open actor filmographies.
				w.Header().Set("Content-Type", "application/json")
				h.encodeMetadata(w, r, []models.SearchResult{})
				return
			}
		}
	}

	results, err := service.Search(r.Context(), q, mediaType)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}
	// Actor biographies link to unrestricted filmographies, just like the cast
	// links hidden on details pages for kids profiles.
	if userID != "" && h.UsersService != nil {
		if user, ok := h.UsersService.Get(userID); ok && user.IsKidsProfile {
			filtered := make([]models.SearchResult, 0, len(results))
			for _, result := range results {
				if result.Title.MediaType != "person" {
					filtered = append(filtered, result)
				}
			}
			results = filtered
		}
	}
	policy := resolveUnreleasedVisibilityPolicy(
		h.CfgManager,
		h.UserSettings,
		h.ClientSettings,
		userID,
		requestClientID(r),
		unreleasedVisibilitySearch,
	)
	if !policy.IncludeMovies {
		enrichSearchMovieReleaseVisibility(r.Context(), results, service)
	}
	results = filterSearchResultsByUnreleasedVisibility(results, policy)

	// Apply kids rating filter if user is a kids profile with rating mode
	if userID != "" && h.UsersService != nil {
		if user, ok := h.UsersService.Get(userID); ok && user.IsKidsProfile && user.KidsMode == "rating" {
			// Enrich results with certification data
			service.EnrichSearchCertifications(r.Context(), results)

			movieRating := user.KidsMaxMovieRating
			tvRating := user.KidsMaxTVRating
			if movieRating == "" && tvRating == "" && user.KidsMaxRating != "" {
				movieRating = user.KidsMaxRating
				tvRating = user.KidsMaxRating
			}
			results = kids.FilterSearchByRatings(results, movieRating, tvRating)
		}
	}

	// Search is intentionally cache-only: expose watch state from the profile's
	// history (and series counts already learned from user-owned shelves) without
	// turning each debounced query into provider work.
	if userID != "" && h.HistoryService != nil && mediaType != "person" && len(results) > 0 {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			enrichSearchResultsWithCachedEpisodeCounts(results, buildWatchStateIndex(wh, cw, pp), service)
		}
	}

	// Ensure we return [] instead of null for empty results
	if results == nil {
		results = []models.SearchResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, results)
}

func (h *MetadataHandler) SeriesDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	service := h.serviceForUser(query.Get("userId"))

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
		TitleID:    strings.TrimSpace(query.Get("titleId")),
		Name:       strings.TrimSpace(query.Get("name")),
		Year:       trimAndParseInt(query.Get("year")),
		TVDBID:     trimAndParseInt64(query.Get("tvdbId")),
		TMDBID:     trimAndParseInt64(query.Get("tmdbId")),
		IMDBID:     strings.TrimSpace(query.Get("imdbId")),
		SeasonType: strings.ToLower(strings.TrimSpace(query.Get("seasonType"))),
	}

	details, err := service.SeriesDetails(r.Context(), req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "404 Not Found") || strings.Contains(err.Error(), "unable to resolve") {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, details)
}

func (h *MetadataHandler) BatchSeriesDetails(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	service := h.serviceForUser(userID)
	var req models.BatchSeriesDetailsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid request body"})
		return
	}

	var results []models.BatchSeriesDetailsItem
	if len(req.Fields) > 0 {
		results = service.BatchSeriesTitleFields(r.Context(), req.Queries, req.Fields)
	} else {
		results = service.BatchSeriesDetails(r.Context(), req.Queries)
	}

	response := models.BatchSeriesDetailsResponse{
		Results: results,
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, response)
}

func (h *MetadataHandler) BatchMovieReleases(w http.ResponseWriter, r *http.Request) {
	var req models.BatchMovieReleasesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid request body"})
		return
	}

	results := h.Service.BatchMovieReleases(r.Context(), req.Queries)

	response := models.BatchMovieReleasesResponse{
		Results: results,
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, response)
}

func (h *MetadataHandler) BatchMovieTitleFields(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	service := h.serviceForUser(userID)
	fieldService, ok := service.(movieTitleFieldsService)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		h.encodeMetadata(w, r, map[string]string{"error": "movie title fields are not supported"})
		return
	}

	var req models.BatchMovieTitleFieldsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid request body"})
		return
	}

	response := models.BatchMovieTitleFieldsResponse{
		Results: fieldService.BatchMovieTitleFields(r.Context(), req.Queries, req.Fields),
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, response)
}

func (h *MetadataHandler) MovieDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	service := h.serviceForUser(query.Get("userId"))

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

	details, err := service.MovieDetails(r.Context(), req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, details)
}

func (h *MetadataHandler) CollectionDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	collectionIDStr := strings.TrimSpace(query.Get("id"))
	if collectionIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "collection id is required"})
		return
	}

	collectionID, err := strconv.ParseInt(collectionIDStr, 10, 64)
	if err != nil || collectionID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid collection id"})
		return
	}

	log.Printf("[metadata] fetching collection details collectionId=%d", collectionID)

	userID := strings.TrimSpace(query.Get("userId"))
	service := h.serviceForUser(userID)
	details, err := service.CollectionDetails(r.Context(), collectionID)
	if err != nil {
		log.Printf("[metadata] collection details error collectionId=%d err=%v", collectionID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	// Apply kids rating filter to collection members for kids profiles.
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	details.Movies = filterTitlesByUnreleasedVisibility(details.Movies, policy)
	details.Movies = h.filterTitlesByKids(r.Context(), userID, service, details.Movies)

	log.Printf("[metadata] collection details success collectionId=%d name=%q movieCount=%d", collectionID, details.Name, len(details.Movies))
	for i, movie := range details.Movies {
		log.Printf("[metadata]   movie[%d]: id=%s name=%q year=%d hasPoster=%v", i, movie.ID, movie.Name, movie.Year, movie.Poster != nil)
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, details)
}

func (h *MetadataHandler) Similar(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	mediaType := strings.ToLower(strings.TrimSpace(query.Get("type")))
	tmdbIDStr := strings.TrimSpace(query.Get("tmdbId"))

	if tmdbIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "tmdbId is required"})
		return
	}

	tmdbID, err := strconv.ParseInt(tmdbIDStr, 10, 64)
	if err != nil || tmdbID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid tmdbId"})
		return
	}

	userID := strings.TrimSpace(query.Get("userId"))
	service := h.serviceForUser(userID)
	titles, err := service.Similar(r.Context(), mediaType, tmdbID)
	if err != nil {
		log.Printf("[metadata] similar error type=%s tmdbId=%d err=%v", mediaType, tmdbID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	titles = filterTitlesByUnreleasedVisibility(titles, policy)
	titles = h.filterTitlesByKids(r.Context(), userID, service, titles)

	// Return empty array instead of null if no results
	if titles == nil {
		titles = []models.Title{}
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, titles)
}

func (h *MetadataHandler) PersonDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	personIDStr := strings.TrimSpace(query.Get("id"))
	if personIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "person id is required"})
		return
	}

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil || personID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid person id"})
		return
	}

	userID := strings.TrimSpace(query.Get("userId"))
	service := h.serviceForUser(userID)
	details, err := service.PersonDetails(r.Context(), personID)
	if err != nil {
		log.Printf("[metadata] person details error personId=%d err=%v", personID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}
	if details != nil {
		policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
		details.Filmography = filterTitlesByUnreleasedVisibility(details.Filmography, policy)
		details.Filmography = h.filterTitlesByKids(r.Context(), userID, service, details.Filmography)
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, details)
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

	response, err := h.serviceForUser(r.URL.Query().Get("userId")).Trailers(r.Context(), req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	if response == nil {
		response = &models.TrailerResponse{Trailers: []models.Trailer{}}
	} else if response.Trailers == nil {
		response.Trailers = []models.Trailer{}
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, response)
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
		h.encodeMetadata(w, r, map[string]string{"error": "url parameter required"})
		return
	}

	// Validate it's a YouTube URL
	if !isYouTubeURL(videoURL) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "only YouTube URLs are supported"})
		return
	}

	streamURL, err := h.Service.ExtractTrailerStreamURL(r.Context(), videoURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, TrailerStreamResponse{StreamURL: streamURL})
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
	h.encodeMetadata(w, r, TrailerPrequeueResponse{
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
	h.encodeMetadata(w, r, TrailerPrequeueResponse{
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
	TotalPending    bool                  `json:"totalPending,omitempty"`
	MetadataPending bool                  `json:"metadataPending,omitempty"`
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"` // Pre-filter total (only set when hideUnreleased is used)
	Genres          []string              `json:"genres,omitempty"`
	AlphabetBuckets []string              `json:"alphabetBuckets,omitempty"`
}

// filterUnreleasedItems removes items that haven't been released yet.
func filterUnreleasedItems(items []models.TrendingItem) []models.TrendingItem {
	result := make([]models.TrendingItem, 0, len(items))
	filteredCount := 0
	for _, item := range items {
		if item.Title.MediaType == "movie" {
			// Movies: keep only when available outside the theatrical window.
			status := models.MovieReleaseStatus(item.Title)
			if status == models.MovieReleaseStatusReleased {
				result = append(result, item)
			} else {
				filteredCount++
				if filteredCount <= 3 {
					log.Printf("[hideUnreleased] filtered movie: %s (status=%s)", item.Title.Name, status)
				}
			}
		} else if item.Title.MediaType == "series" {
			if item.Title.Status == models.SeriesReleaseStatusReleased {
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
		if !isTrendingItemWatched(item, userID, historySvc) {
			// Not watched or not found - include it
			result = append(result, item)
		} else {
			filteredCount++
			if filteredCount <= 3 {
				itemID := buildItemIDForHistory(item)
				log.Printf("[hideWatched] filtered %s: %s (itemID=%s)", item.Title.MediaType, item.Title.Name, itemID)
			}
		}
	}
	log.Printf("[hideWatched] filter result: %d/%d items kept (filtered %d)", len(result), len(items), filteredCount)
	return result
}

func isTrendingItemWatched(item models.TrendingItem, userID string, historySvc historyServiceInterface) bool {
	if userID == "" || historySvc == nil {
		return false
	}
	itemID := buildItemIDForHistory(item)
	mediaType := item.Title.MediaType
	if itemID == "" || mediaType == "" {
		return false
	}
	watchItem, _ := historySvc.GetWatchHistoryItem(userID, mediaType, itemID)
	return watchItem != nil && watchItem.Watched
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
		h.encodeMetadata(w, r, map[string]string{"error": "url parameter required"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"
	lite := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lite"))) == "true"
	query := parseDisplayListQuery(r)

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
	artworkLimit := 0
	if artworkLimitStr := r.URL.Query().Get("artworkLimit"); artworkLimitStr != "" {
		if parsed, err := strconv.Atoi(artworkLimitStr); err == nil && parsed > 0 {
			artworkLimit = parsed
		}
	}

	// Validate URL contains mdblist.com/lists/
	if !strings.Contains(listURL, "mdblist.com/lists/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid MDBList URL format"})
		return
	}

	// Auto-fix: remove trailing slashes and add /json if missing
	listURL = strings.TrimRight(listURL, "/")
	if !strings.HasSuffix(listURL, "/json") {
		listURL = listURL + "/json"
	}

	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	visibilityFiltered := hideUnreleased || !policy.IncludeMovies || !policy.IncludeShows

	// Resolve visibility before loading: the service checks release dates before
	// pagination. Lite display rows are not authoritative release metadata.
	// Build options — filtering + pagination handled inside the service
	serviceLimit, serviceOffset := limit, offset
	service := h.serviceForUser(userID)
	requiresFullList := query.RequiresFullList()
	// Sorts and filters that MDBList's own rows can answer run before
	// pagination inside the service, so only the requested page is enriched.
	var preQuery *customListPreQuery
	if requiresFullList && h.customListSourceQuerySupported(service, userID, query) {
		preQuery = newCustomListPreQuery(query, sourceTitlesAreDisplayNames(service))
		requiresFullList = false
	}
	if requiresFullList {
		// Query the complete enriched list so filtering and sorting happen before
		// pagination. GetCustomList caches this full-list representation.
		serviceLimit, serviceOffset = 0, 0
	}
	opts := metadatapkg.CustomListOptions{
		DeferArtwork:         strings.EqualFold(r.URL.Query().Get("deferArtwork"), "true"),
		DeferEnrichment:      strings.EqualFold(r.URL.Query().Get("deferEnrichment"), "true") && !requiresFullList,
		Limit:                serviceLimit,
		Offset:               serviceOffset,
		HideUnreleased:       hideUnreleased,
		HideUnreleasedMovies: !policy.IncludeMovies,
		HideUnreleasedShows:  !policy.IncludeShows,
		HideWatched:          hideWatched,
		Lite:                 lite,
		ArtworkLimit:         artworkLimit,
		UserID:               userID,
		Label:                strings.TrimSpace(r.URL.Query().Get("name")),
	}
	if preQuery != nil {
		opts.PreQuery = preQuery.run
	}
	if hideWatched && userID != "" && h.HistoryService != nil {
		opts.HistorySvc = h.HistoryService
	}

	if svc, ok := service.(shelfCardsService); ok && progressiveShelfRequest(r) {
		source, err := svc.GetCustomListSource(r.Context(), listURL)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := h.progressiveCuratedShelf(r, service, source, opts.Label, userID, hideUnreleased, hideWatched, limit, offset)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		h.encodeMetadata(w, r, resp)
		return
	}
	items, filteredTotal, unfilteredTotal, err := service.GetCustomList(r.Context(), listURL, opts)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}
	// Apply kids rating filter for kids profiles.
	if filtered := h.filterTrendingByKids(r.Context(), userID, service, items); len(filtered) != len(items) {
		filteredTotal = len(filtered)
		items = filtered
	}

	// Watch state on every page (clients badge it), not only when a query filters by it.
	if userID != "" && h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			idx := buildWatchStateIndex(wh, cw, pp)
			enrichTrendingItems(items, idx, service, strings.EqualFold(r.URL.Query().Get("includeUnwatchedCounts"), "true"))
		}
	}

	// Attach cached ratings before query sorting. Existing enrichment warms
	// missing ratings asynchronously.
	enrichTrendingRatings(items, service)
	genres := displayListGenres(items)
	alphabet := displayListAlphabetBuckets(items, query)
	if preQuery != nil && preQuery.alphabet != nil {
		alphabet = preQuery.alphabet
	}
	if requiresFullList {
		items = query.Apply(items)
		filteredTotal = len(items)
	}
	if requiresFullList {
		items = paginateTrendingItems(items, offset, limit)
	}

	w.Header().Set("Content-Type", "application/json")
	resp := CustomListResponse{Items: items, Total: filteredTotal, Genres: genres, AlphabetBuckets: alphabet}
	if hideUnreleased || hideWatched || visibilityFiltered || query.Active() {
		resp.UnfilteredTotal = unfilteredTotal
	}
	h.encodeMetadata(w, r, resp)
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
	Genres          []string              `json:"genres,omitempty"`
	AlphabetBuckets []string              `json:"alphabetBuckets,omitempty"`
}

// TraktList returns enriched Trakt watchlist/custom-list items for use in home shelves.
func (h *MetadataHandler) TraktList(w http.ResponseWriter, r *http.Request) {
	if h.TraktClient == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		h.encodeMetadata(w, r, map[string]string{"error": "trakt client unavailable"})
		return
	}
	if h.UsersService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		h.encodeMetadata(w, r, map[string]string{"error": "users service unavailable"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "userId parameter required"})
		return
	}
	user, ok := h.UsersService.Get(userID)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		h.encodeMetadata(w, r, map[string]string{"error": "user not found"})
		return
	}

	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	listType := strings.TrimSpace(r.URL.Query().Get("listType"))
	listID := strings.TrimSpace(r.URL.Query().Get("listId"))
	if accountID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "accountId parameter required"})
		return
	}
	if listType != "watchlist" && listType != "custom" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid listType"})
		return
	}
	if listType == "custom" && listID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "listId parameter required for custom lists"})
		return
	}
	if accountID == "__all__" && listType != "watchlist" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "all accounts is only supported for watchlists"})
		return
	}

	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"
	query := parseDisplayListQuery(r)
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
		h.encodeMetadata(w, r, map[string]string{"error": "failed to load settings"})
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
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	sourceItems, err := h.fetchTraktShelfItems(r.Context(), settings, traktAccounts, listType, listID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	curated := make([]metadatapkg.CuratedItem, 0, len(sourceItems))
	for _, item := range sourceItems {
		tmdbID, _ := strconv.ParseInt(item.TMDBID, 10, 64)
		tvdbID, _ := strconv.ParseInt(item.TVDBID, 10, 64)
		curated = append(curated, metadatapkg.CuratedItem{
			Title:  item.Title,
			TMDBID: tmdbID, TVDBID: tvdbID,
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

	if _, ok := h.serviceForUser(userID).(shelfCardsService); ok && progressiveShelfRequest(r) {
		resp := h.buildShelfFromCurated(w, r, curated, label, userID, hideUnreleased, hideWatched, limit, offset)
		if resp != nil {
			h.encodeMetadata(w, r, resp)
		}
		return
	}
	service := h.serviceForUser(userID)
	items, err := getCuratedListForRequest(r, service, curated, label)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	unfilteredTotal := len(items)
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if hideUnreleased {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	if hideWatched && h.HistoryService != nil {
		items = filterWatchedItems(items, userID, h.HistoryService)
	}
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	items, genres, alphabet := h.queryTrendingList(userID, service, items, query)
	filteredTotal := len(items)

	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		items = []models.TrendingItem{}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	w.Header().Set("Content-Type", "application/json")
	resp := TraktShelfResponse{
		Items:           items,
		Total:           filteredTotal,
		Genres:          genres,
		AlphabetBuckets: alphabet,
	}
	if hideUnreleased || hideWatched {
		resp.UnfilteredTotal = unfilteredTotal
	}
	h.encodeMetadata(w, r, resp)
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
	_ = settings
	if ctx == nil {
		ctx = context.Background()
	}

	// Fetch each account concurrently with its own credential-bound client.
	// Never mutate the shared TraktClient's credentials here: Home loads
	// several shelves (and accounts) at once, so in-place credential updates
	// would race and could send one account's app key with another's token.
	perAccount := make([][]traktShelfSourceItem, len(accounts))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i := range accounts {
		accountCopy := accounts[i]
		g.Go(func() error {
			accessToken, err := h.TraktClient.EnsureValidToken(&accountCopy, h.CfgManager)
			if err != nil {
				return fmt.Errorf("validate trakt token for %s: %w", accountCopy.Name, err)
			}
			if accessToken == "" {
				return nil
			}
			client := h.TraktClient.ForAccount(&accountCopy)

			var normalizedItems []traktShelfSourceItem
			switch listType {
			case "watchlist":
				watchlistItems, err := client.GetAllWatchlistCached(gctx, accountCopy.ID, accessToken)
				if err != nil {
					return fmt.Errorf("fetch trakt watchlist for %s: %w", accountCopy.Name, err)
				}
				for _, item := range watchlistItems {
					if normalized, ok := normalizeTraktWatchlistItem(item); ok {
						normalizedItems = append(normalizedItems, normalized)
					}
				}
			case "custom":
				listItems, err := client.GetAllListItemsCached(gctx, accountCopy.ID, accessToken, listID)
				if err != nil {
					return fmt.Errorf("fetch trakt list for %s: %w", accountCopy.Name, err)
				}
				for _, item := range listItems {
					if normalized, ok := normalizeTraktCustomListItem(item); ok {
						normalizedItems = append(normalizedItems, normalized)
					}
				}
			}
			perAccount[i] = normalizedItems
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Merge in account order so dedupe tie-breaking stays deterministic.
	seen := make(map[string]traktShelfDedupeItem)
	for _, accountItems := range perAccount {
		for _, item := range accountItems {
			upsertTraktShelfItem(seen, item)
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

var validSimklShelfMediaTypes = map[string]bool{"movies": true, "shows": true, "anime": true}

// SimklList returns enriched items from a Simkl status bucket for home shelves.
func (h *MetadataHandler) SimklList(w http.ResponseWriter, r *http.Request) {
	if h.SimklClient == nil {
		writeJSONError(w, "simkl client unavailable", http.StatusInternalServerError)
		return
	}
	if h.UsersService == nil {
		writeJSONError(w, "users service unavailable", http.StatusInternalServerError)
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		writeJSONError(w, "userId parameter required", http.StatusBadRequest)
		return
	}
	user, ok := h.UsersService.Get(userID)
	if !ok {
		writeJSONError(w, "user not found", http.StatusNotFound)
		return
	}

	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mediaType")))
	listType := strings.TrimSpace(r.URL.Query().Get("listType"))
	if accountID == "" {
		writeJSONError(w, "accountId parameter required", http.StatusBadRequest)
		return
	}
	if !validSimklShelfMediaTypes[mediaType] {
		writeJSONError(w, "invalid mediaType (expected movies, shows, or anime)", http.StatusBadRequest)
		return
	}

	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"
	limit, offset := parseLimitOffset(r)

	settings, err := h.CfgManager.Load()
	if err != nil {
		writeJSONError(w, "failed to load settings", http.StatusInternalServerError)
		return
	}

	account, err := h.resolveSimklShelfAccount(user, settings, accountID)
	if err != nil {
		status := http.StatusForbidden
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSONError(w, err.Error(), status)
		return
	}

	if _, err := h.SimklClient.EnsureValidToken(account, h.CfgManager); err != nil {
		log.Printf("[simkl] token refresh failed for account %s: %v", account.ID, err)
	}
	listItems, err := h.SimklClient.GetListItems(account.ClientID, account.AccessToken, mediaType, listType)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}

	curated := make([]metadatapkg.CuratedItem, 0, len(listItems))
	for _, item := range listItems {
		ci := metadatapkg.CuratedItem{
			Title:     item.Title,
			Year:      item.Year,
			IMDBID:    item.IDs.IMDB,
			MediaType: "movie",
		}
		if item.MediaType == "show" {
			ci.MediaType = "series"
		}
		if item.IDs.TMDB > 0 {
			ci.TMDBID = int64(item.IDs.TMDB)
		}
		if item.IDs.TVDB > 0 {
			ci.TVDBID = int64(item.IDs.TVDB)
		}
		curated = append(curated, ci)
	}

	label := strings.TrimSpace(r.URL.Query().Get("name"))
	if label == "" {
		label = "Simkl List"
	}

	items := h.buildShelfFromCurated(w, r, curated, label, userID, hideUnreleased, hideWatched, limit, offset)
	if items == nil {
		return // error already written
	}
	h.encodeMetadata(w, r, items)
}

func (h *MetadataHandler) resolveSimklShelfAccount(user models.User, settings config.Settings, accountID string) (*config.SimklAccount, error) {
	acc := settings.Simkl.GetAccountByID(accountID)
	if acc == nil {
		return nil, fmt.Errorf("simkl account not found")
	}
	if strings.TrimSpace(acc.AccessToken) == "" {
		return nil, fmt.Errorf("simkl account not authenticated")
	}

	if h.AccountsService != nil {
		if account, ok := h.AccountsService.Get(user.AccountID); ok && account.IsMaster {
			return acc, nil
		}
	}
	if user.SimklAccountID != "" && user.SimklAccountID == accountID {
		return acc, nil
	}
	return nil, fmt.Errorf("simkl account is not available to this profile")
}

// LetterboxdList returns enriched movie items from either a public Letterboxd
// list URL or a Letterboxd list imported into MDBList. Imported lists are
// identified by MDBList's external-list ID.
func (h *MetadataHandler) LetterboxdList(w http.ResponseWriter, r *http.Request) {
	listID := strings.TrimSpace(r.URL.Query().Get("listId"))
	listURL := strings.TrimSpace(r.URL.Query().Get("listUrl"))

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"
	limit, offset := parseLimitOffset(r)
	service := h.serviceForUser(userID)
	if _, ok := service.(shelfCardsService); ok && progressiveShelfRequest(r) && listURL != "" {
		if h.LetterboxdClient == nil {
			writeJSONError(w, "letterboxd client unavailable", http.StatusInternalServerError)
			return
		}
		items, err := h.progressiveLetterboxdShelf(r, service, listURL, userID, hideUnreleased, hideWatched, limit, offset)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		h.encodeMetadata(w, r, items)
		return
	}

	maxItems := maxShelfSourceItems(limit, offset)
	if parseDisplayListQuery(r).RequiresIndex() || (progressiveShelfRequest(r) && listID != "") {
		// Querying must see the complete upstream list before pagination. Both
		// Letterboxd adapters stop naturally at the end of the list.
		maxItems = 10000
	}
	var curated []metadatapkg.CuratedItem
	sourceTotal := 0
	switch {
	case listURL != "":
		if h.LetterboxdClient == nil {
			writeJSONError(w, "letterboxd client unavailable", http.StatusInternalServerError)
			return
		}
		result, err := h.LetterboxdClient.GetListResult(r.Context(), listURL, maxItems)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		sourceTotal = result.Total
		curated = make([]metadatapkg.CuratedItem, 0, len(result.Items))
		for _, item := range result.Items {
			curated = append(curated, metadatapkg.CuratedItem{
				Title:     item.Title,
				Year:      item.Year,
				MediaType: item.MediaType,
			})
		}
	case listID != "":
		if h.MDBListListsClient == nil || !h.MDBListListsClient.IsConfigured() {
			writeJSONError(w, "MDBList API key not configured", http.StatusInternalServerError)
			return
		}
		listItems, err := h.MDBListListsClient.GetExternalListItems(r.Context(), listID, maxItems)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		curated = make([]metadatapkg.CuratedItem, 0, len(listItems))
		for _, item := range listItems {
			curated = append(curated, metadatapkg.CuratedItem{
				Title:     item.Title,
				Year:      item.Year,
				IMDBID:    item.IMDBID,
				TMDBID:    item.TMDBID,
				TVDBID:    item.TVDBID,
				MediaType: item.MediaType,
			})
		}
	default:
		writeJSONError(w, "listId or listUrl parameter required", http.StatusBadRequest)
		return
	}

	label := strings.TrimSpace(r.URL.Query().Get("name"))
	if label == "" {
		label = "Letterboxd List"
	}

	items := h.buildShelfFromCurated(w, r, curated, label, userID, hideUnreleased, hideWatched, limit, offset)
	if items == nil {
		return // error already written
	}
	if sourceTotal > items.Total && !hideUnreleased && !hideWatched && !items.TotalPending {
		items.Total = sourceTotal
	}
	h.encodeMetadata(w, r, items)
}

func maxShelfSourceItems(limit, offset int) int {
	if limit > 0 {
		return offset + limit
	}
	return 1000
}

// LetterboxdSources returns the user's imported Letterboxd lists (MDBList
// external lists with source "letterboxd"), for the shelf-config picker.
func (h *MetadataHandler) LetterboxdSources(w http.ResponseWriter, r *http.Request) {
	if h.MDBListListsClient == nil || !h.MDBListListsClient.IsConfigured() {
		writeJSONError(w, "MDBList API key not configured", http.StatusInternalServerError)
		return
	}

	lists, err := h.MDBListListsClient.GetExternalLists(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return
	}

	type sourceResponse struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Items int    `json:"items"`
	}
	out := make([]sourceResponse, 0, len(lists))
	for _, l := range lists {
		if !strings.EqualFold(strings.TrimSpace(l.Source), "letterboxd") {
			continue
		}
		out = append(out, sourceResponse{ID: strconv.Itoa(l.ID), Name: l.Name, Items: l.Items})
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, map[string]interface{}{"lists": out})
}

// buildShelfFromCurated enriches curated items, applies the shared shelf
// filters/pagination and writes the standard shelf response. It returns nil
// (after writing an error) on failure.
func (h *MetadataHandler) buildShelfFromCurated(w http.ResponseWriter, r *http.Request, curated []metadatapkg.CuratedItem, label, userID string, hideUnreleased, hideWatched bool, limit, offset int) *CustomListResponse {
	service := h.serviceForUser(userID)
	if _, ok := service.(shelfCardsService); ok && progressiveShelfRequest(r) {
		resp, err := h.progressiveCuratedShelf(r, service, curated, label, userID, hideUnreleased, hideWatched, limit, offset)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		return resp
	}
	items, err := getCuratedListForRequest(r, service, curated, label)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadGateway)
		return nil
	}

	unfilteredTotal := len(items)
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if hideUnreleased {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	if hideWatched && userID != "" && h.HistoryService != nil {
		items = filterWatchedItems(items, userID, h.HistoryService)
	}
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	query := parseDisplayListQuery(r)
	items, genres, alphabet := h.queryTrendingList(userID, service, items, query)
	filteredTotal := len(items)

	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		items = []models.TrendingItem{}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	w.Header().Set("Content-Type", "application/json")
	resp := &CustomListResponse{Items: items, Total: filteredTotal, Genres: genres, AlphabetBuckets: alphabet}
	if hideUnreleased || hideWatched {
		resp.UnfilteredTotal = unfilteredTotal
	}
	return resp
}

// parseLimitOffset reads optional pagination parameters; a zero limit is unlimited.
func parseLimitOffset(r *http.Request) (int, int) {
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
	return limit, offset
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
		h.encodeMetadata(w, r, map[string]string{"error": "invalid JSON body"})
		return
	}
	if len(req.Items) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "items must not be empty"})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	service := h.serviceForUser(userID)
	items, err := getCuratedListForRequest(r, service, req.Items, req.Label)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	// Enrich with watch state if user context is available
	if userID != "" && h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			idx := buildWatchStateIndex(wh, cw, pp)
			enrichTrendingItems(items, idx, service, strings.EqualFold(r.URL.Query().Get("includeUnwatchedCounts"), "true"))
		}
	}
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, service)

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, map[string]interface{}{"items": items})
}

// TMDBSources resolves a source-builder search term, numeric ID, or TMDB URL.
func (h *MetadataHandler) TMDBSources(w http.ResponseWriter, r *http.Request) {
	sourceType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sourceType")))
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if sourceType == "" || (query == "" && sourceType != metadatapkg.TMDBSourceCustomDiscover) {
		writeJSONError(w, "sourceType and query are required", http.StatusBadRequest)
		return
	}
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	svc, ok := service.(tmdbSourceSearchService)
	if !ok {
		writeJSONError(w, "TMDB source search is unavailable", http.StatusServiceUnavailable)
		return
	}
	results, err := svc.SearchTMDBSources(r.Context(), sourceType, query)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, metadatapkg.ErrInvalidTMDBList) ||
			strings.Contains(strings.ToLower(err.Error()), "requires a numeric id") {
			status = http.StatusBadRequest
		}
		writeJSONError(w, err.Error(), status)
		return
	}
	if results == nil {
		results = []metadatapkg.TMDBSourceResult{}
	}
	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, map[string]interface{}{"items": results})
}

// TMDBList returns a shelf built from any TMDB source-builder type.
func (h *MetadataHandler) TMDBList(w http.ResponseWriter, r *http.Request) {
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	svc, ok := service.(tmdbListService)
	if !ok {
		writeJSONError(w, "TMDB lists are unavailable", http.StatusServiceUnavailable)
		return
	}
	limit, offset := parseLimitOffset(r)
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideUnreleased := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleased")), "true")
	hideWatched := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideWatched")), "true")
	postFilterPagination := limit > 0 && (hideUnreleased || hideWatched)
	loadOpts := parseShelfLoadOptions(r)
	opts := metadatapkg.TMDBListOptions{
		DeferArtwork:  loadOpts.DeferArtwork,
		SourceType:    strings.TrimSpace(r.URL.Query().Get("sourceType")),
		SourceID:      strings.TrimSpace(r.URL.Query().Get("sourceId")),
		MediaType:     strings.TrimSpace(r.URL.Query().Get("mediaType")),
		Sort:          strings.TrimSpace(r.URL.Query().Get("sort")),
		DiscoverQuery: strings.TrimSpace(r.URL.Query().Get("discoverQuery")),
		Limit:         limit,
		Offset:        offset,
		ArtworkLimit:  loadOpts.ArtworkLimit,
	}
	if _, ok := service.(shelfCardsService); ok && progressiveShelfRequest(r) {
		response, err := h.progressiveTMDBShelf(r, service, svc, opts, userID, hideUnreleased, hideWatched, limit, offset)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		h.encodeMetadata(w, r, response)
		return
	}
	if postFilterPagination {
		// Visibility filters run after TMDB mapping. Load the bounded source
		// result from the beginning so filtering cannot leave a short page or
		// apply the offset before excluded titles are removed.
		opts.Limit = maxDiscoveryListItems
		opts.Offset = 0
	}
	items, total, err := svc.GetTMDBList(r.Context(), opts)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, metadatapkg.ErrInvalidTMDBList) {
			status = http.StatusBadRequest
		}
		writeJSONError(w, err.Error(), status)
		return
	}
	if items == nil {
		items = []models.TrendingItem{}
	}

	unfilteredTotal := total
	completeResultSet := opts.Offset == 0 && len(items) >= unfilteredTotal
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if hideUnreleased {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	if !policy.IncludeMovies {
		enrichTrendingMovieReleaseVisibility(r.Context(), items, service)
	}
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	if hideWatched && userID != "" && h.HistoryService != nil {
		items = filterWatchedItems(items, userID, h.HistoryService)
	}
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	if completeResultSet {
		total = len(items)
	} else {
		// A filtered page cannot reveal the filtered total for unseen TMDB
		// pages. Keep the stable source total instead of subtracting only the
		// removals observed on this page.
		total = unfilteredTotal
	}
	if postFilterPagination {
		items = paginateTrendingItems(items, offset, limit)
	}
	enrichTrendingRatings(items, service)

	response := DiscoverNewResponse{MetadataPending: r.URL.Query().Get("shelfPhase") == "cards", Items: items, Total: total}
	if hideUnreleased || hideWatched || total != unfilteredTotal {
		response.UnfilteredTotal = unfilteredTotal
	}
	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, response)
}

// DiscoverByGenre returns TMDB discover results for a specific genre
func (h *MetadataHandler) DiscoverByGenre(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	genreIDStr := strings.TrimSpace(r.URL.Query().Get("genreId"))

	if genreIDStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "genreId is required"})
		return
	}

	genreID, err := strconv.ParseInt(genreIDStr, 10, 64)
	if err != nil || genreID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid genreId"})
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

	loadOpts := parseShelfLoadOptions(r)
	var items []models.TrendingItem
	var total int
	if svc, ok := service.(discoverByGenreOptionsService); ok {
		items, total, err = svc.DiscoverByGenreWithOptions(r.Context(), mediaType, genreID, limit, offset, loadOpts)
	} else {
		items, total, err = service.DiscoverByGenre(r.Context(), mediaType, genreID, limit, offset)
	}
	if err != nil {
		log.Printf("[metadata] discover genre error type=%s genreId=%d: %v", mediaType, genreID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleased")), "true") {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	if !policy.IncludeMovies {
		enrichTrendingMovieReleaseVisibility(r.Context(), items, service)
	}
	beforeVisibility := len(items)
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	total -= beforeVisibility - len(items)

	// Apply kids rating filter for kids profiles.
	if filtered := h.filterTrendingByKids(r.Context(), userID, service, items); len(filtered) != len(items) {
		total -= len(items) - len(filtered)
		items = filtered
	}

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, service)
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
	h.encodeMetadata(w, r, DiscoverNewResponse{MetadataPending: r.URL.Query().Get("shelfPhase") == "cards", Items: items, Total: total})
}

// DiscoverByDecade returns TMDB discover results for a specific decade
func (h *MetadataHandler) DiscoverByDecade(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	decadeStr := strings.TrimSpace(r.URL.Query().Get("decade"))

	if decadeStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "decade is required"})
		return
	}

	decade, err := strconv.Atoi(decadeStr)
	if err != nil || decade < 1900 || decade%10 != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "invalid decade"})
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

	loadOpts := parseShelfLoadOptions(r)
	var items []models.TrendingItem
	var total int
	if svc, ok := service.(discoverByDecadeOptionsService); ok {
		items, total, err = svc.DiscoverByDecadeWithOptions(r.Context(), mediaType, decade, limit, offset, loadOpts)
	} else {
		items, total, err = service.DiscoverByDecade(r.Context(), mediaType, decade, limit, offset)
	}
	if err != nil {
		log.Printf("[metadata] discover decade error type=%s decade=%d: %v", mediaType, decade, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleased")), "true") {
		policy.IncludeMovies = false
		policy.IncludeShows = false
	}
	if !policy.IncludeMovies {
		enrichTrendingMovieReleaseVisibility(r.Context(), items, service)
	}
	beforeVisibility := len(items)
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	total -= beforeVisibility - len(items)

	// Apply kids rating filter for kids profiles.
	if filtered := h.filterTrendingByKids(r.Context(), userID, service, items); len(filtered) != len(items) {
		total -= len(items) - len(filtered)
		items = filtered
	}

	// Enrich with MDBList ratings for sort-by-rating support
	enrichTrendingRatings(items, service)
	log.Printf(
		"[metadata] discover decade handler complete type=%s decade=%d limit=%d offset=%d count=%d total=%d duration=%s",
		mediaType,
		decade,
		limit,
		offset,
		len(items),
		total,
		time.Since(start).Round(time.Millisecond),
	)

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, DiscoverNewResponse{MetadataPending: r.URL.Query().Get("shelfPhase") == "cards", Items: items, Total: total})
}

// GetAIRecommendations returns AI-powered personalized recommendations.
// It collects the user's watched titles from history and watchlist, then
// asks the configured AI provider for recommendations and resolves them to TMDB titles.
func (h *MetadataHandler) GetAIRecommendations(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "userId is required"})
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
		h.encodeMetadata(w, r, DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0})
		return
	}

	service := h.serviceForUser(userID)
	items, err := service.GetAIRecommendations(r.Context(), watchedTitles, mediaTypes, userID)
	if err != nil {
		log.Printf("[metadata] ai recommendations error user=%s: %v", userID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	enrichTrendingRatings(items, service)

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, DiscoverNewResponse{Items: items, Total: len(items)})
}

// GetAISimilar returns AI-powered recommendations similar to a specific title.
func (h *MetadataHandler) GetAISimilar(w http.ResponseWriter, r *http.Request) {
	seedTitle := strings.TrimSpace(r.URL.Query().Get("title"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("type"))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	service := h.serviceForUser(userID)
	if seedTitle == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "title is required"})
		return
	}
	if mediaType == "" {
		mediaType = "movie"
	}

	// Detach from the request context — the model takes ~35s and the client
	// will cancel the HTTP connection before it finishes. Use a background
	// context with a generous timeout so the result is always cached even
	// if the client navigates away.
	aiCtx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	items, err := service.GetAISimilar(aiCtx, seedTitle, mediaType)
	if err != nil {
		log.Printf("[metadata] ai similar error seed=%q: %v", seedTitle, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	enrichTrendingRatings(items, service)

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, DiscoverNewResponse{Items: items, Total: len(items)})
}

// GetAICustomRecommendations returns recommendations based on a free-text query.
func (h *MetadataHandler) GetAICustomRecommendations(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	if query == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		h.encodeMetadata(w, r, map[string]string{"error": "q (query) is required"})
		return
	}

	items, err := service.GetAICustomRecommendations(r.Context(), query)
	if err != nil {
		log.Printf("[metadata] ai custom recommendations error query=%q: %v", query, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	if items == nil {
		items = []models.TrendingItem{}
	}
	items = h.filterTrendingByKids(r.Context(), strings.TrimSpace(r.URL.Query().Get("userId")), service, items)
	enrichTrendingRatings(items, service)

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, DiscoverNewResponse{Items: items, Total: len(items)})
}

// GetAISurprise returns a single random movie/show recommendation.
func (h *MetadataHandler) GetAISurprise(w http.ResponseWriter, r *http.Request) {
	decade := strings.TrimSpace(r.URL.Query().Get("decade"))
	mediaType := strings.TrimSpace(r.URL.Query().Get("mediaType"))
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	item, err := service.GetAISurprise(r.Context(), decade, mediaType)
	if err != nil {
		log.Printf("[metadata] ai surprise error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	// Drop the suggestion if it exceeds a kids profile's rating limit.
	if item != nil {
		if filtered := h.filterTrendingByKids(r.Context(), strings.TrimSpace(r.URL.Query().Get("userId")), service, []models.TrendingItem{*item}); len(filtered) == 0 {
			item = nil
		}
	}

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, item)
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
	service := h.serviceForUser(r.URL.Query().Get("userId"))
	debugMode := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("debug")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("debug")), "true")

	w.Header().Set("X-TopTen-Handler", "metadata-handler-v1")
	log.Printf("[topten] starting (mediaType=%s)", mediaType)
	var (
		items []models.TrendingItem
		debug []metadatapkg.TopTenDebugEntry
		err   error
	)
	if svc, ok := service.(topTenCandidatesOptionsService); ok {
		items, err = svc.GetTopTenCandidatesWithOptions(r.Context(), mediaType, nil, parseShelfLoadOptions(r))
	} else if svc, ok := service.(topTenCandidatesService); ok {
		items, err = svc.GetTopTenCandidates(r.Context(), mediaType, nil)
	} else if debugMode {
		if svc, ok := service.(topTenDebugService); ok {
			items, debug, err = svc.GetTopTenDebug(r.Context(), mediaType, nil)
		} else {
			items, err = service.GetTopTen(r.Context(), mediaType, nil)
		}
	} else {
		items, err = service.GetTopTen(r.Context(), mediaType, nil)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		h.encodeMetadata(w, r, map[string]string{"error": err.Error()})
		return
	}

	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	policy := resolveUnreleasedVisibilityPolicy(h.CfgManager, h.UserSettings, h.ClientSettings, userID, requestClientID(r), unreleasedVisibilityLists)
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	items = selectTopTenResponseItems(items, mediaType)

	// Watch state shows under the rank on each card.
	if userID != "" && h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		if whErr == nil {
			cw, _ := h.HistoryService.ListSeriesStates(userID)
			pp, _ := h.HistoryService.ListPlaybackProgress(userID)
			warmEpisodeCounts := strings.EqualFold(r.URL.Query().Get("includeUnwatchedCounts"), "true")
			enrichTrendingItems(items, buildWatchStateIndex(wh, cw, pp), service, warmEpisodeCounts)
		}
	}
	enrichTrendingRatings(items, service)

	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, TopTenResponse{Items: items, Total: len(items), Debug: debug})
}

func selectTopTenResponseItems(items []models.TrendingItem, mediaType string) []models.TrendingItem {
	// Filter before limiting so incomplete cached candidates do not consume slots.
	eligible := make([]models.TrendingItem, 0, len(items))
	for _, item := range items {
		title := item.Title
		if title.Poster == nil || strings.TrimSpace(title.Poster.URL) == "" {
			continue
		}
		if title.TMDBID <= 0 && title.TVDBID <= 0 && strings.TrimSpace(title.IMDBID) == "" {
			continue
		}
		if mediaType != "" && mediaType != "all" {
			view := "shows"
			if mediaType == "movie" {
				view = "movies"
			}
			if !matchesHomeView(title.MediaType, view) {
				continue
			}
		}
		eligible = append(eligible, item)
	}
	items = eligible
	normalized := strings.ToLower(strings.TrimSpace(mediaType))
	if normalized != "" && normalized != "all" {
		limit := minInt(10, len(items))
		selected := append([]models.TrendingItem(nil), items[:limit]...)
		for i := range selected {
			selected[i].Rank = i + 1
		}
		return selected
	}

	movies := make([]models.TrendingItem, 0, 5)
	shows := make([]models.TrendingItem, 0, 5)
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Title.MediaType)) {
		case "movie", "movies", "film", "films":
			if len(movies) < 5 {
				movies = append(movies, item)
			}
		case "series", "tv", "show", "shows":
			if len(shows) < 5 {
				shows = append(shows, item)
			}
		}
	}

	selected := make([]models.TrendingItem, 0, len(movies)+len(shows))
	for i := 0; i < 5; i++ {
		if i < len(movies) {
			selected = append(selected, movies[i])
		}
		if i < len(shows) {
			selected = append(selected, shows[i])
		}
	}
	for i := range selected {
		selected[i].Rank = i + 1
	}
	return selected
}

// GetProgress returns a snapshot of active metadata enrichment progress.
func (h *MetadataHandler) GetProgress(w http.ResponseWriter, r *http.Request) {
	snapshot := h.Service.GetProgressSnapshot()
	w.Header().Set("Content-Type", "application/json")
	h.encodeMetadata(w, r, snapshot)
}

// cachedPopularOnServer holds a cached "Popular on This Server" response.
type cachedPopularOnServer struct {
	items     []models.TrendingItem
	total     int
	key       string
	expiresAt time.Time
}

// cachedRecentlyWatched holds a cached "Recently Watched" response.
type cachedRecentlyWatched struct {
	items     []models.TrendingItem
	total     int
	key       string
	expiresAt time.Time
}

// PopularOnServerResponse is the JSON payload for the "Popular on This Server" shelf.
type PopularOnServerResponse struct {
	Items []models.TrendingItem `json:"items"`
	Total int                   `json:"total"`
}

// PopularOnServer handles GET /api/discover/popular-on-server
func (h *MetadataHandler) PopularOnServer(w http.ResponseWriter, r *http.Request) {
	service := h.serviceForUser("")
	windowDays := 90
	minViews := 2
	if h.CfgManager != nil {
		cfg, err := h.CfgManager.Load()
		if err == nil && cfg.HomeShelves.PopularOnServerWindowDays >= 7 && cfg.HomeShelves.PopularOnServerWindowDays <= 365 {
			windowDays = cfg.HomeShelves.PopularOnServerWindowDays
		}
	}
	windowDays = sharedShelfIntQuery(r, "activityWindowDays", 7, 365, windowDays)
	minViews = sharedShelfIntQuery(r, "minimumProfiles", 1, 100, minViews)

	historyService, historyOK := h.HistoryService.(sharedShelfHistoryService)
	usersService, usersOK := h.UsersService.(sharedShelfUsersService)
	if !historyOK || !usersOK {
		h.writeSharedShelfResponse(w, r, service, nil, 0)
		return
	}

	eligibleUsers, privacyKey := eligibleSharedShelfUsers(usersService.ListAll())
	eligibleIDs := make(map[string]bool, len(eligibleUsers))
	for id := range eligibleUsers {
		eligibleIDs[id] = true
	}
	viewerUserID, hideWatched := sharedShelfViewerFilter(r)
	offset, limit := sharedShelfPage(r, 20)
	cacheKey := fmt.Sprintf("%s:%s:%t:%d:%d:%d:%d", privacyKey, viewerUserID, hideWatched, windowDays, minViews, offset, limit)

	h.popularCacheMu.Lock()
	if h.popularCache != nil && h.popularCache.key == cacheKey && h.popularCache.expiresAt.After(time.Now()) {
		cached := h.popularCache
		h.popularCacheMu.Unlock()
		h.writeSharedShelfResponse(w, r, service, cached.items, cached.total)
		return
	}
	h.popularCacheMu.Unlock()

	popular := historyService.AggregatePopularTitles(eligibleIDs, windowDays, minViews)
	if hideWatched {
		filtered := make([]models.PopularTitle, 0, len(popular))
		for _, item := range popular {
			title := titleFromSharedShelfItem(item.ItemID, item.Name, item.MediaType, item.Year, item.ExternalIDs)
			if !isTrendingItemWatched(models.TrendingItem{Title: title}, viewerUserID, h.HistoryService) {
				filtered = append(filtered, item)
			}
		}
		popular = filtered
	}
	total := len(popular)
	if offset >= len(popular) {
		popular = nil
	} else {
		popular = popular[offset:minInt(len(popular), offset+limit)]
	}

	items := parallelSharedShelfItems(r.Context(), popular, func(ctx context.Context, pt models.PopularTitle) models.TrendingItem {
		title := titleFromSharedShelfItem(
			pt.ItemID,
			pt.Name,
			pt.MediaType,
			pt.Year,
			pt.ExternalIDs,
		)
		title = enrichSharedShelfTitle(ctx, service, title)
		title.CardSubtitle = fmt.Sprintf("%d %s", pt.WatchCount, pluralizeView(pt.WatchCount))
		title.ForceTitleOverlay = true
		return models.TrendingItem{Title: title, Rank: pt.WatchCount}
	})

	h.popularCacheMu.Lock()
	h.popularCache = &cachedPopularOnServer{
		items:     items,
		total:     total,
		key:       cacheKey,
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	h.popularCacheMu.Unlock()

	h.writeSharedShelfResponse(w, r, service, items, total)
}

// RecentlyWatchedResponse uses the same item contract as every other home shelf.
type RecentlyWatchedResponse struct {
	Items []models.TrendingItem `json:"items"`
	Total int                   `json:"total"`
}

// RecentlyWatched handles GET /api/discover/recently-watched.
func (h *MetadataHandler) RecentlyWatched(w http.ResponseWriter, r *http.Request) {
	service := h.serviceForUser("")
	windowDays := 14
	maxPerProfile := 3
	if h.CfgManager != nil {
		cfg, err := h.CfgManager.Load()
		if err == nil && cfg.HomeShelves.RecentlyWatchedCapPerProfile > 0 && cfg.HomeShelves.RecentlyWatchedCapPerProfile <= 20 {
			maxPerProfile = cfg.HomeShelves.RecentlyWatchedCapPerProfile
		}
	}
	windowDays = sharedShelfIntQuery(r, "activityWindowDays", 1, 90, windowDays)
	maxPerProfile = sharedShelfIntQuery(r, "maxItemsPerProfile", 1, 20, maxPerProfile)

	historyService, historyOK := h.HistoryService.(sharedShelfHistoryService)
	usersService, usersOK := h.UsersService.(sharedShelfUsersService)
	if !historyOK || !usersOK {
		h.writeSharedShelfResponse(w, r, service, nil, 0)
		return
	}

	eligibleUsers, privacyKey := eligibleSharedShelfUsers(usersService.ListAll())
	viewerUserID, hideWatched := sharedShelfViewerFilter(r)
	offset, limit := sharedShelfPage(r, 20)
	cacheKey := fmt.Sprintf("%s:%s:%t:%d:%d:%d:%d", privacyKey, viewerUserID, hideWatched, windowDays, maxPerProfile, offset, limit)

	h.recentWatchesCacheMu.Lock()
	if h.recentWatchesCache != nil && h.recentWatchesCache.key == cacheKey && h.recentWatchesCache.expiresAt.After(time.Now()) {
		cached := h.recentWatchesCache
		h.recentWatchesCacheMu.Unlock()
		h.writeSharedShelfResponse(w, r, service, cached.items, cached.total)
		return
	}
	h.recentWatchesCacheMu.Unlock()

	recent := historyService.ListRecentWatches(eligibleUsers, windowDays, maxPerProfile)
	if hideWatched {
		filtered := make([]models.RecentWatch, 0, len(recent))
		for _, watch := range recent {
			title := recentWatchBaseTitle(watch)
			if !isTrendingItemWatched(models.TrendingItem{Title: title}, viewerUserID, h.HistoryService) {
				filtered = append(filtered, watch)
			}
		}
		recent = filtered
	}
	total := len(recent)
	if offset >= len(recent) {
		recent = nil
	} else {
		recent = recent[offset:minInt(len(recent), offset+limit)]
	}

	type rankedRecentWatch struct {
		Rank  int
		Watch models.RecentWatch
	}
	ranked := make([]rankedRecentWatch, len(recent))
	for i, watch := range recent {
		ranked[i] = rankedRecentWatch{Rank: i + 1, Watch: watch}
	}
	items := parallelSharedShelfItems(r.Context(), ranked, func(ctx context.Context, rankedWatch rankedRecentWatch) models.TrendingItem {
		watch := rankedWatch.Watch
		title := recentWatchBaseTitle(watch)
		title = enrichRecentWatchTitle(ctx, service, title, watch)
		title.CardSubtitle = recentWatchSubtitle(watch)
		title.ForceTitleOverlay = true
		return models.TrendingItem{Title: title, Rank: rankedWatch.Rank}
	})

	h.recentWatchesCacheMu.Lock()
	h.recentWatchesCache = &cachedRecentlyWatched{
		items:     items,
		total:     total,
		key:       cacheKey,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	h.recentWatchesCacheMu.Unlock()

	h.writeSharedShelfResponse(w, r, service, items, total)
}

func eligibleSharedShelfUsers(users []models.User) (map[string]models.User, string) {
	eligible := make(map[string]models.User)
	keys := make([]string, 0, len(users))
	for _, user := range users {
		privacy := models.NormalizeActivityPrivacy(user.ActivityPrivacy)
		if !user.SharesActivity() {
			continue
		}
		user.ActivityPrivacy = privacy
		eligible[user.ID] = user
		keys = append(keys, user.ID+":"+privacy+":"+user.Name)
	}
	sort.Strings(keys)
	return eligible, strings.Join(keys, "|")
}

func sharedShelfViewerFilter(r *http.Request) (string, bool) {
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	hideWatched := userID != "" && strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideWatched")), "true")
	return userID, hideWatched
}

func recentWatchBaseTitle(watch models.RecentWatch) models.Title {
	mediaType := watch.MediaType
	itemID := watch.ItemID
	name := watch.Name
	if watch.MediaType == "episode" {
		mediaType = "series"
		itemID = watch.SeriesID
		name = watch.SeriesName
	}
	return titleFromSharedShelfItem(itemID, name, mediaType, 0, watch.ExternalIDs)
}

func sharedShelfPage(r *http.Request, fallback int) (int, int) {
	offset := 0
	if value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("offset"))); err == nil && value > 0 {
		offset = value
	}
	limit := fallback
	if value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && value > 0 {
		limit = minInt(value, 100)
	}
	return offset, limit
}

func sharedShelfIntQuery(r *http.Request, key string, minimum, maximum, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}

func titleFromSharedShelfItem(itemID, name, mediaType string, year int, externalIDs map[string]string) models.Title {
	title := models.Title{
		ID:        itemID,
		Name:      name,
		MediaType: mediaType,
		Year:      year,
		Overview:  "",
		Language:  "",
	}
	if externalIDs != nil {
		if tmdb, ok := externalIDs["tmdb"]; ok {
			if id, err := strconv.ParseInt(tmdb, 10, 64); err == nil {
				title.TMDBID = id
			}
		}
		if imdb, ok := externalIDs["imdb"]; ok {
			title.IMDBID = imdb
		}
		if tvdb, ok := externalIDs["tvdb"]; ok {
			if id, err := strconv.ParseInt(tvdb, 10, 64); err == nil {
				title.TVDBID = id
			}
		}
	}
	return title
}

func enrichSharedShelfTitle(ctx context.Context, service metadataService, title models.Title) models.Title {
	if title.MediaType == "series" {
		query := models.SeriesDetailsQuery{
			TitleID: title.ID,
			Name:    title.Name,
			Year:    title.Year,
			TVDBID:  title.TVDBID,
			TMDBID:  title.TMDBID,
			IMDBID:  title.IMDBID,
		}
		if details, err := service.SeriesDetails(ctx, query); err == nil && details != nil {
			return details.Title
		}
	} else if title.MediaType == "movie" {
		query := models.MovieDetailsQuery{
			TitleID: title.ID,
			TMDBID:  title.TMDBID,
			IMDBID:  title.IMDBID,
			Name:    title.Name,
			Year:    title.Year,
		}
		if details, err := service.MovieDetails(ctx, query); err == nil && details != nil {
			return *details
		}
	}
	return title
}

func enrichRecentWatchTitle(
	ctx context.Context,
	service metadataService,
	title models.Title,
	watch models.RecentWatch,
) models.Title {
	if watch.MediaType != "episode" || title.MediaType != "series" {
		return enrichSharedShelfTitle(ctx, service, title)
	}

	query := models.SeriesDetailsQuery{
		TitleID: title.ID,
		Name:    title.Name,
		Year:    title.Year,
		TVDBID:  title.TVDBID,
		TMDBID:  title.TMDBID,
		IMDBID:  title.IMDBID,
	}
	details, err := service.SeriesDetails(ctx, query)
	if err != nil || details == nil {
		return title
	}

	enriched := details.Title
	for _, season := range details.Seasons {
		if season.Number != watch.SeasonNumber {
			continue
		}
		for _, episode := range season.Episodes {
			if episode.EpisodeNumber == watch.EpisodeNumber && episode.Image != nil {
				image := *episode.Image
				enriched.CardImage = &image
				return enriched
			}
		}
	}
	return enriched
}

func parallelSharedShelfItems[T any](
	ctx context.Context,
	source []T,
	build func(context.Context, T) models.TrendingItem,
) []models.TrendingItem {
	items := make([]models.TrendingItem, len(source))
	if len(source) == 0 {
		return items
	}

	const maxConcurrentEnrichments = 6
	semaphore := make(chan struct{}, maxConcurrentEnrichments)
	var wg sync.WaitGroup
	for i, sourceItem := range source {
		wg.Add(1)
		go func(index int, item T) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			items[index] = build(ctx, item)
		}(i, sourceItem)
	}
	wg.Wait()
	return items
}

func pluralizeView(count int) string {
	if count == 1 {
		return "view"
	}
	return "views"
}

func recentWatchSubtitle(watch models.RecentWatch) string {
	label := watch.UserName + " watched"
	if watch.MediaType == "episode" {
		if watch.SeasonNumber > 0 || watch.EpisodeNumber > 0 {
			label += fmt.Sprintf(" S%02dE%02d", watch.SeasonNumber, watch.EpisodeNumber)
		}
		if strings.TrimSpace(watch.Name) != "" {
			label += " · " + strings.TrimSpace(watch.Name)
		}
	}
	return label
}

func (h *MetadataHandler) writeSharedShelfResponse(
	w http.ResponseWriter,
	r *http.Request,
	service metadataService,
	items []models.TrendingItem,
	total int,
) {
	if items == nil {
		items = []models.TrendingItem{}
	}
	originalCount := len(items)
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	items = h.filterTrendingByKids(r.Context(), userID, service, items)
	if len(items) != originalCount {
		total = len(items)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = h.encodeMetadata(w, r, PopularOnServerResponse{Items: items, Total: total})
}

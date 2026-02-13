package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

	"novastream/config"
	"novastream/models"

	"github.com/gorilla/mux"
)

// StartupHandler serves a combined startup payload to reduce the number of
// HTTP round-trips required when the frontend initialises.  All seven data
// fetches are performed concurrently.
type StartupHandler struct {
	userSettings userSettingsService
	watchlist    watchlistService
	history      historyService
	metadata     metadataService
	cfgManager   *config.Manager
	users        userService
}

// NewStartupHandler constructs a StartupHandler.
func NewStartupHandler(
	userSettings userSettingsService,
	watchlist watchlistService,
	history historyService,
	metadata metadataService,
	cfgManager *config.Manager,
	users userService,
) *StartupHandler {
	return &StartupHandler{
		userSettings: userSettings,
		watchlist:    watchlist,
		history:      history,
		metadata:     metadata,
		cfgManager:   cfgManager,
		users:        users,
	}
}

// StartupResponse is the combined payload returned by GET /api/users/{userID}/startup.
type StartupResponse struct {
	UserSettings     *models.UserSettings         `json:"userSettings"`
	Watchlist        []models.WatchlistItem        `json:"watchlist"`
	ContinueWatching []models.SeriesWatchState     `json:"continueWatching"`
	PlaybackProgress []models.PlaybackProgress     `json:"playbackProgress"`
	WatchHistory     []models.WatchHistoryItem     `json:"watchHistory"`
	TrendingMovies   *DiscoverNewResponse          `json:"trendingMovies"`
	TrendingSeries   *DiscoverNewResponse          `json:"trendingSeries"`
}

// GetStartup returns all initial user data in a single response.
func (h *StartupHandler) GetStartup(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["userID"])
	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}

	if h.users != nil && !h.users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	hideUnreleased := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideUnreleased"))) == "true"
	hideWatched := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hideWatched"))) == "true"

	resp := StartupResponse{}
	var wg sync.WaitGroup

	// 1. User settings
	wg.Add(1)
	go func() {
		defer wg.Done()
		defaults := h.getDefaultsFromGlobal()
		settings, err := h.userSettings.GetWithDefaults(userID, defaults)
		if err != nil {
			log.Printf("[startup] user settings error for %s: %v", userID, err)
			return
		}
		resp.UserSettings = &settings
	}()

	// 2. Watchlist
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.watchlist.List(userID)
		if err != nil {
			log.Printf("[startup] watchlist error for %s: %v", userID, err)
			return
		}
		resp.Watchlist = items
	}()

	// 3. Continue watching
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.history.ListContinueWatching(userID)
		if err != nil {
			log.Printf("[startup] continue watching error for %s: %v", userID, err)
			return
		}
		resp.ContinueWatching = items
	}()

	// 4. Playback progress
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.history.ListPlaybackProgress(userID)
		if err != nil {
			log.Printf("[startup] playback progress error for %s: %v", userID, err)
			return
		}
		resp.PlaybackProgress = items
	}()

	// 5. Watch history
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.history.ListWatchHistory(userID)
		if err != nil {
			log.Printf("[startup] watch history error for %s: %v", userID, err)
			return
		}
		resp.WatchHistory = items
	}()

	// Determine trending source from user or global settings
	trendingSource := h.getTrendingSource(userID)

	// 6. Trending movies
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.metadata.Trending(r.Context(), "movie", trendingSource)
		if err != nil {
			log.Printf("[startup] trending movies error: %v", err)
			resp.TrendingMovies = &DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0}
			return
		}
		items = h.applyFilters(items, userID, hideUnreleased, hideWatched)
		resp.TrendingMovies = &DiscoverNewResponse{Items: items, Total: len(items)}
	}()

	// 7. Trending series
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.metadata.Trending(r.Context(), "series", trendingSource)
		if err != nil {
			log.Printf("[startup] trending series error: %v", err)
			resp.TrendingSeries = &DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0}
			return
		}
		items = h.applyFilters(items, userID, hideUnreleased, hideWatched)
		resp.TrendingSeries = &DiscoverNewResponse{Items: items, Total: len(items)}
	}()

	wg.Wait()

	// Ensure nil slices become empty arrays in JSON
	if resp.Watchlist == nil {
		resp.Watchlist = []models.WatchlistItem{}
	}
	if resp.ContinueWatching == nil {
		resp.ContinueWatching = []models.SeriesWatchState{}
	}
	if resp.PlaybackProgress == nil {
		resp.PlaybackProgress = []models.PlaybackProgress{}
	}
	if resp.WatchHistory == nil {
		resp.WatchHistory = []models.WatchHistoryItem{}
	}
	if resp.TrendingMovies == nil {
		resp.TrendingMovies = &DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0}
	}
	if resp.TrendingSeries == nil {
		resp.TrendingSeries = &DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Options handles CORS preflight for the startup endpoint.
func (h *StartupHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// getTrendingSource resolves the trending movie source from user settings or global config.
func (h *StartupHandler) getTrendingSource(userID string) config.TrendingMovieSource {
	if h.userSettings != nil {
		if userSettings, err := h.userSettings.Get(userID); err == nil && userSettings != nil {
			if userSettings.HomeShelves.TrendingMovieSource != "" {
				return config.TrendingMovieSource(userSettings.HomeShelves.TrendingMovieSource)
			}
		}
	}

	if settings, err := h.cfgManager.Load(); err == nil {
		if settings.HomeShelves.TrendingMovieSource != "" {
			return settings.HomeShelves.TrendingMovieSource
		}
	}

	return config.TrendingMovieSourceReleased
}

// applyFilters applies hideUnreleased and hideWatched filters to trending items.
func (h *StartupHandler) applyFilters(items []models.TrendingItem, userID string, hideUnreleased, hideWatched bool) []models.TrendingItem {
	if hideUnreleased {
		items = filterUnreleasedItems(items)
	}
	if hideWatched && userID != "" && h.history != nil {
		items = filterWatchedItems(items, userID, h.history)
	}
	return items
}

// getDefaultsFromGlobal extracts per-user setting defaults from global config.
// This mirrors UserSettingsHandler.getDefaultsFromGlobal.
func (h *StartupHandler) getDefaultsFromGlobal() models.UserSettings {
	globalSettings, err := h.cfgManager.Load()
	if err != nil {
		return models.DefaultUserSettings()
	}

	return models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:           globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
			UseLoadingScreen:          globalSettings.Playback.UseLoadingScreen,
			SubtitleSize:              globalSettings.Playback.SubtitleSize,
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves:             convertShelves(globalSettings.HomeShelves.Shelves),
			TrendingMovieSource: models.TrendingMovieSource(globalSettings.HomeShelves.TrendingMovieSource),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                   models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                 models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                    globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:                      models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			PrioritizeHdr:                    models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
			FilterOutTerms:                   globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:                   globalSettings.Filtering.PreferredTerms,
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Filtering.BypassFilteringForAIOStreamsOnly),
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
	}
}


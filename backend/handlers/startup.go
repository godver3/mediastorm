package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"novastream/config"
	"novastream/models"

	"github.com/gorilla/mux"
)

// startupShelfLimit caps list data in the startup bundle to reduce payload
// size on low-power devices. Full lists are fetched on demand (e.g. explore page).
const startupShelfLimit = 20

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
	UserSettings     *models.UserSettings      `json:"userSettings"`
	Watchlist        []models.WatchlistItem    `json:"watchlist"`
	ContinueWatching []models.SeriesWatchState  `json:"continueWatching"`
	WatchHistory     []models.WatchHistoryItem  `json:"watchHistory"`
	TrendingMovies   *DiscoverNewResponse      `json:"trendingMovies"`
	TrendingSeries   *DiscoverNewResponse      `json:"trendingSeries"`
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

	// 2. Watchlist (capped to startupShelfLimit — full list fetched on demand)
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.watchlist.List(userID)
		if err != nil {
			log.Printf("[startup] watchlist error for %s: %v", userID, err)
			return
		}
		if len(items) > startupShelfLimit {
			items = items[:startupShelfLimit]
		}
		resp.Watchlist = items
	}()

	// 3. Continue watching + playback progress (merged server-side so the
	// frontend doesn't need to build progress maps on the JS thread,
	// capped to startupShelfLimit)
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.history.ListContinueWatching(userID)
		if err != nil {
			log.Printf("[startup] continue watching error for %s: %v", userID, err)
			return
		}
		progress, err := h.history.ListPlaybackProgress(userID)
		if err != nil {
			log.Printf("[startup] playback progress error for %s: %v", userID, err)
			if len(items) > startupShelfLimit {
				items = items[:startupShelfLimit]
			}
			resp.ContinueWatching = items
			return
		}
		merged := mergeProgressIntoContinueWatching(items, progress)
		if len(merged) > startupShelfLimit {
			merged = merged[:startupShelfLimit]
		}
		resp.ContinueWatching = merged
	}()

	// 5. Watch history — excluded from startup bundle to keep payload small.
	// WatchStatusContext will fetch this independently after the initial render.
	// With ~3000 items (~1 MB), including it blocks the React Native JS bridge
	// on low-power devices (Fire Stick) for 7+ seconds during deserialization.

	// Determine trending source from user or global settings
	trendingSource := h.getTrendingSource(userID)

	// 6. Trending movies (slimmed — heavy Title fields stripped for startup)
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
		items = slimTrendingItems(items)
		resp.TrendingMovies = &DiscoverNewResponse{Items: items, Total: len(items)}
	}()

	// 7. Trending series (slimmed — heavy Title fields stripped for startup)
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
		items = slimTrendingItems(items)
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

// mergeProgressIntoContinueWatching computes PercentWatched and ResumePercent
// for each continue-watching item using the playback progress data. This moves
// the map-building + lookup work from the frontend JS thread to the backend,
// eliminating ~48 KB of playbackProgress data from the startup payload and
// ~60 lines of JS processing on low-power devices.
func mergeProgressIntoContinueWatching(items []models.SeriesWatchState, progress []models.PlaybackProgress) []models.SeriesWatchState {
	// Build lookup maps (mirrors the frontend's ContinueWatchingContext logic)
	byItemID := make(map[string]float64, len(progress)*2)
	byEpisode := make(map[string]float64)

	for _, p := range progress {
		if p.ItemID != "" {
			byItemID[p.ItemID] = p.PercentWatched
		}
		if p.ID != "" {
			byItemID[p.ID] = p.PercentWatched
		}
		if p.MediaType == "episode" && p.SeriesID != "" {
			key := fmt.Sprintf("%s:S%dE%d", p.SeriesID, p.SeasonNumber, p.EpisodeNumber)
			byEpisode[key] = p.PercentWatched
		}
	}

	episodePercent := func(ep *models.EpisodeReference, seriesID string) float64 {
		if ep == nil {
			return 0
		}
		if ep.EpisodeID != "" {
			if pct, ok := byItemID[ep.EpisodeID]; ok {
				return pct
			}
		}
		key := fmt.Sprintf("%s:S%dE%d", seriesID, ep.SeasonNumber, ep.EpisodeNumber)
		if pct, ok := byEpisode[key]; ok {
			return pct
		}
		return 0
	}

	merged := make([]models.SeriesWatchState, len(items))
	for i, item := range items {
		merged[i] = item

		if item.NextEpisode == nil {
			// Movie — look up by seriesId
			moviePct := byItemID[item.SeriesID]
			merged[i].PercentWatched = moviePct
			merged[i].ResumePercent = moviePct
		} else {
			nextPct := episodePercent(item.NextEpisode, item.SeriesID)
			lastPct := episodePercent(&item.LastWatched, item.SeriesID)
			isSame := item.LastWatched.SeasonNumber == item.NextEpisode.SeasonNumber &&
				item.LastWatched.EpisodeNumber == item.NextEpisode.EpisodeNumber

			resumePct := nextPct
			if resumePct == 0 && isSame {
				resumePct = lastPct
			}
			pctWatched := resumePct
			if lastPct > pctWatched {
				pctWatched = lastPct
			}

			merged[i].PercentWatched = pctWatched
			merged[i].ResumePercent = resumePct
		}
	}

	return merged
}

// slimTrendingItems strips heavy Title fields (releases, trailers, ratings,
// credits, etc.) that the home screen doesn't render. This typically saves
// ~10 KB per movie (92 per-country release entries) and removes trailers,
// ratings, credits, and collection metadata.
func slimTrendingItems(items []models.TrendingItem) []models.TrendingItem {
	slim := make([]models.TrendingItem, len(items))
	for i, item := range items {
		slim[i] = models.TrendingItem{
			Rank: item.Rank,
			Title: models.Title{
				ID:         item.Title.ID,
				Name:       item.Title.Name,
				Overview:   item.Title.Overview,
				Year:       item.Title.Year,
				Poster:     item.Title.Poster,
				Backdrop:   item.Title.Backdrop,
				MediaType:  item.Title.MediaType,
				TVDBID:     item.Title.TVDBID,
				IMDBID:     item.Title.IMDBID,
				TMDBID:     item.Title.TMDBID,
				Theatrical: item.Title.Theatrical,
				HomeRelease: item.Title.HomeRelease,
				Genres:     item.Title.Genres,
			},
		}
	}
	return slim
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


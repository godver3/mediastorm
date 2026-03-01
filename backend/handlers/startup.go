package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/kids"

	"github.com/gorilla/mux"
)

// startupShelfLimit caps list data in the startup bundle to reduce payload
// size on low-power devices. Full lists are fetched on demand (e.g. explore page).
const startupShelfLimit = 20

// startupTrendingTimeout limits how long the startup handler waits for trending
// data. On cold start, Trending() can take 20-30s enriching metadata from TMDB.
// Rather than blocking the entire startup response, we return partial data and
// let the frontend fetch trending independently.
const startupTrendingTimeout = 10 * time.Second

// StartupHandler serves a combined startup payload to reduce the number of
// HTTP round-trips required when the frontend initialises.  All seven data
// fetches are performed concurrently.
type StartupHandler struct {
	userSettings  userSettingsService
	watchlist     watchlistService
	history       historyService
	metadata      metadataService
	cfgManager    *config.Manager
	users         userService
	usersProvider usersServiceInterface // for kids profile filtering
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
	UserSettings          *models.UserSettings     `json:"userSettings"`
	Watchlist             []models.WatchlistItem   `json:"watchlist"`
	WatchlistTotal        int                      `json:"watchlistTotal"`
	ContinueWatching      []models.SeriesWatchState `json:"continueWatching"`
	ContinueWatchingTotal int                      `json:"continueWatchingTotal"`
	WatchHistory          []models.WatchHistoryItem `json:"watchHistory"`
	TrendingMovies        *DiscoverNewResponse     `json:"trendingMovies"`
	TrendingSeries        *DiscoverNewResponse     `json:"trendingSeries"`
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
		resp.WatchlistTotal = len(items)
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
		resp.ContinueWatchingTotal = len(items)
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

	// 5. Watch history + playback progress for server-side watch state enrichment.
	// The full watch history is NOT sent to the client (too large for JS bridge),
	// but we fetch it here to pre-compute watchState/unwatchedCount on each item.
	var watchHistory []models.WatchHistoryItem
	var playbackProgress []models.PlaybackProgress
	wg.Add(1)
	go func() {
		defer wg.Done()
		wh, err := h.history.ListWatchHistory(userID)
		if err != nil {
			log.Printf("[startup] watch history error for %s: %v", userID, err)
			return
		}
		watchHistory = wh
		pp, err := h.history.ListPlaybackProgress(userID)
		if err != nil {
			log.Printf("[startup] playback progress error for %s: %v", userID, err)
			return
		}
		playbackProgress = pp
	}()

	// 6-7. Trending movies + series — these call Trending() which on cold cache
	// can take 20-30s for TMDB enrichment. Run them with a deadline so they
	// don't block the entire startup response. If they timeout, the frontend
	// receives empty trending data and fetches it independently.
	// Use a channel to communicate results and avoid data races with the
	// timeout path reading resp fields while goroutines write to them.
	type trendingResult struct {
		movies *DiscoverNewResponse
		series *DiscoverNewResponse
	}
	trendingCtx, trendingCancel := context.WithTimeout(r.Context(), startupTrendingTimeout)
	defer trendingCancel()
	trendingCh := make(chan trendingResult, 1)

	go func() {
		var result trendingResult
		var trendingWg sync.WaitGroup
		var mu sync.Mutex

		// Trending movies (slimmed — heavy Title fields stripped for startup)
		trendingWg.Add(1)
		go func() {
			defer trendingWg.Done()
			items, err := h.metadata.Trending(trendingCtx, "movie")
			if err != nil {
				log.Printf("[startup] trending movies error: %v", err)
				return
			}
			items = h.applyFilters(items, userID, hideUnreleased, hideWatched)
			total := len(items)
			if len(items) > startupShelfLimit {
				items = items[:startupShelfLimit]
			}
			items = slimTrendingItems(items)
			mu.Lock()
			result.movies = &DiscoverNewResponse{Items: items, Total: total}
			mu.Unlock()
		}()

		// Trending series (slimmed — heavy Title fields stripped for startup)
		trendingWg.Add(1)
		go func() {
			defer trendingWg.Done()
			items, err := h.metadata.Trending(trendingCtx, "series")
			if err != nil {
				log.Printf("[startup] trending series error: %v", err)
				return
			}
			items = h.applyFilters(items, userID, hideUnreleased, hideWatched)
			total := len(items)
			if len(items) > startupShelfLimit {
				items = items[:startupShelfLimit]
			}
			items = slimTrendingItems(items)
			mu.Lock()
			result.series = &DiscoverNewResponse{Items: items, Total: total}
			mu.Unlock()
		}()

		trendingWg.Wait()
		trendingCh <- result
	}()

	// Wait for all fast goroutines (settings, watchlist, continue watching, history)
	wg.Wait()

	// Wait for trending with a timeout — don't block the response if enrichment is slow.
	// When trending times out, leave TrendingMovies/TrendingSeries as nil so the
	// frontend JSON receives null and falls back to independent fetches.
	trendingCompleted := false
	select {
	case tr := <-trendingCh:
		resp.TrendingMovies = tr.movies
		resp.TrendingSeries = tr.series
		trendingCompleted = true
	case <-trendingCtx.Done():
		log.Printf("[startup] trending data timed out after %v, returning partial response", startupTrendingTimeout)
	}

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
	// Only default trending to empty when it actually completed — a nil value
	// signals the frontend that trending timed out and should be fetched independently.
	if trendingCompleted {
		if resp.TrendingMovies == nil {
			resp.TrendingMovies = &DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0}
		}
		if resp.TrendingSeries == nil {
			resp.TrendingSeries = &DiscoverNewResponse{Items: []models.TrendingItem{}, Total: 0}
		}
	}

	// Enrich items with pre-computed watch state (after all concurrent fetches complete)
	idx := buildWatchStateIndex(watchHistory, resp.ContinueWatching, playbackProgress)
	enrichWatchlistItems(resp.Watchlist, idx)
	if resp.TrendingMovies != nil {
		enrichTrendingItems(resp.TrendingMovies.Items, idx)
	}
	if resp.TrendingSeries != nil {
		enrichTrendingItems(resp.TrendingSeries.Items, idx)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// SetUsersProvider sets the users service for kids profile filtering.
func (h *StartupHandler) SetUsersProvider(provider usersServiceInterface) {
	h.usersProvider = provider
}

// Options handles CORS preflight for the startup endpoint.
func (h *StartupHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// applyFilters applies hideUnreleased, hideWatched, and kids rating filters to trending items.
func (h *StartupHandler) applyFilters(items []models.TrendingItem, userID string, hideUnreleased, hideWatched bool) []models.TrendingItem {
	if hideUnreleased {
		items = filterUnreleasedItems(items)
	}
	if hideWatched && userID != "" && h.history != nil {
		items = filterWatchedItems(items, userID, h.history)
	}
	// Apply kids rating filter
	if userID != "" && h.usersProvider != nil {
		if user, ok := h.usersProvider.Get(userID); ok && user.IsKidsProfile {
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
				HomeRelease:   item.Title.HomeRelease,
				Certification: item.Title.Certification,
				Genres:        item.Title.Genres,
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
			Shelves: convertShelves(globalSettings.HomeShelves.Shelves),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                   models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                 models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                    globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:                      models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			PrioritizeHdr:                    models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
			FilterOutTerms:                   globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:                   globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:                globalSettings.Filtering.NonPreferredTerms,
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Filtering.BypassFilteringForAIOStreamsOnly),
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
	}
}


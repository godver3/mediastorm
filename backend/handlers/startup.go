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
	"novastream/services/playback"

	"github.com/gorilla/mux"
)

// defaultStartupShelfLimit caps list data in the startup bundle to reduce payload
// size on low-power devices. Full lists are fetched on demand (e.g. explore page).
const defaultStartupShelfLimit = 20

// startupTrendingTimeout limits how long the startup handler waits for trending
// data. On cold start, Trending() can take 20-30s enriching metadata from TMDB.
// The startup bundle gates several frontend providers, so keep this short and
// fail open with partial data instead of stalling the whole home screen.
const startupTrendingTimeout = 1500 * time.Millisecond

// startupCalendarService is the subset of the calendar service used by the
// startup handler. It reads only from the pre-built cache (non-blocking).
type startupCalendarService interface {
	GetForHomeShelf(userID string, loc *time.Location, daysBack, daysForward int) []models.CalendarItem
}

type startupPrequeueStore interface {
	GetByTitleUser(titleID, userID string) (*playback.PrequeueEntry, bool)
}

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
	calendar      startupCalendarService
	localMedia    localLibraryLister
	prequeueStore startupPrequeueStore
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

// SetCalendar injects the calendar service. Called after construction because
// the calendar service is created after the startup handler in main.go.
func (h *StartupHandler) SetCalendar(cal startupCalendarService) {
	h.calendar = cal
}

func (h *StartupHandler) SetPrequeueStore(store startupPrequeueStore) {
	h.prequeueStore = store
}

// StartupResponse is the combined payload returned by GET /api/users/{userID}/startup.
type StartupResponse struct {
	UserSettings             *models.UserSettings      `json:"userSettings"`
	Watchlist                []models.WatchlistItem    `json:"watchlist"`
	WatchlistTotal           int                       `json:"watchlistTotal"`
	ContinueWatching         []models.SeriesWatchState `json:"continueWatching"`
	ContinueWatchingTotal    int                       `json:"continueWatchingTotal"`
	ContinueWatchingRevision string                    `json:"continueWatchingRevision"`
	WatchHistory             []models.WatchHistoryItem `json:"watchHistory"`
	TrendingMovies           *DiscoverNewResponse      `json:"trendingMovies"`
	TrendingSeries           *DiscoverNewResponse      `json:"trendingSeries"`
	// CalendarItems contains the home-shelf calendar window (yesterday + next 2 days).
	// Populated from the pre-built calendar cache; empty if the cache is not ready yet.
	CalendarItems []models.CalendarItem `json:"calendarItems,omitempty"`
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
	includeTrendingMovies := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("includeTrendingMovies"))) != "false"
	includeTrendingSeries := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("includeTrendingSeries"))) != "false"

	resp := StartupResponse{}
	defaults := h.getDefaultsFromGlobal()
	settings, err := h.userSettings.GetWithDefaults(userID, defaults)
	if err != nil {
		log.Printf("[startup] user settings error for %s: %v", userID, err)
	} else {
		resp.UserSettings = &settings
	}

	startupShelfLimit := defaultStartupShelfLimit
	if resp.UserSettings != nil && resp.UserSettings.HomeShelves.ItemCap > 0 {
		startupShelfLimit = resp.UserSettings.HomeShelves.ItemCap
	}
	var wg sync.WaitGroup

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
		enrichWatchlistTextPosters(items, h.metadata)
		resp.Watchlist = items
	}()

	// 3. Continue watching + playback progress (merged server-side so the
	// frontend doesn't need to build progress maps on the JS thread,
	// capped to startupShelfLimit)
	wg.Add(1)
	go func() {
		defer wg.Done()
		revision, err := h.history.GetContinueWatchingRevision(userID)
		if err != nil {
			log.Printf("[startup] continue watching revision error for %s: %v", userID, err)
		} else {
			resp.ContinueWatchingRevision = revision
		}

		items, err := h.history.ListContinueWatching(userID)
		if err != nil {
			log.Printf("[startup] continue watching error for %s: %v", userID, err)
			return
		}
		resp.ContinueWatchingTotal = len(items)
		items = h.withPrequeueStatus(userID, items)
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

	// 5b. Calendar home-shelf items (yesterday + next 2 days). Non-blocking:
	// reads only from the pre-built in-memory cache; returns empty when not ready.
	if h.calendar != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tzName := strings.TrimSpace(r.URL.Query().Get("tz"))
			loc := time.UTC
			if tzName != "" {
				if parsed, err := time.LoadLocation(tzName); err == nil {
					loc = parsed
				}
			}
			resp.CalendarItems = h.calendar.GetForHomeShelf(userID, loc, 1, 2)
		}()
	}

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
	var trendingCtx context.Context
	var trendingCancel context.CancelFunc
	var trendingCh chan trendingResult
	if includeTrendingMovies || includeTrendingSeries {
		trendingCtx, trendingCancel = context.WithTimeout(r.Context(), startupTrendingTimeout)
		defer trendingCancel()
		trendingCh = make(chan trendingResult, 1)

		go func() {
			var result trendingResult
			var trendingWg sync.WaitGroup
			var mu sync.Mutex

			if includeTrendingMovies {
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
			}

			if includeTrendingSeries {
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
			}

			trendingWg.Wait()
			trendingCh <- result
		}()
	}

	// Wait for all fast goroutines (settings, watchlist, continue watching, history)
	wg.Wait()

	// Wait for trending with a timeout — don't block the response if enrichment is slow.
	// When trending times out, leave TrendingMovies/TrendingSeries as nil so the
	// frontend JSON receives null and falls back to independent fetches.
	trendingCompleted := false
	if trendingCh != nil {
		select {
		case tr := <-trendingCh:
			resp.TrendingMovies = tr.movies
			resp.TrendingSeries = tr.series
			trendingCompleted = true
		case <-trendingCtx.Done():
			log.Printf("[startup] trending data timed out after %v, returning partial response", startupTrendingTimeout)
		}
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
	// Enrich with MDBList ratings for sort-by-rating support (bounded by startupShelfLimit)
	enrichWatchlistRatings(r.Context(), resp.Watchlist, h.metadata)
	if resp.TrendingMovies != nil {
		enrichTrendingItems(resp.TrendingMovies.Items, idx)
	}
	if resp.TrendingSeries != nil {
		enrichTrendingItems(resp.TrendingSeries.Items, idx)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *StartupHandler) withPrequeueStatus(userID string, items []models.SeriesWatchState) []models.SeriesWatchState {
	if h.prequeueStore == nil || len(items) == 0 {
		return items
	}

	for i := range items {
		entry, ok := h.prequeueStore.GetByTitleUser(items[i].SeriesID, userID)
		if !ok || entry == nil || !startupPrequeueMatchesContinueWatchingItem(entry, items[i]) {
			continue
		}
		items[i].PrequeueID = entry.ID
		items[i].PrequeueStatus = string(entry.Status)
	}

	return items
}

func startupPrequeueMatchesContinueWatchingItem(entry *playback.PrequeueEntry, item models.SeriesWatchState) bool {
	if entry == nil {
		return false
	}
	if item.NextEpisode == nil {
		return entry.TargetEpisode == nil
	}
	if entry.TargetEpisode == nil {
		return false
	}
	return entry.TargetEpisode.SeasonNumber == item.NextEpisode.SeasonNumber &&
		entry.TargetEpisode.EpisodeNumber == item.NextEpisode.EpisodeNumber
}

// SetUsersProvider sets the users service for kids profile filtering.
func (h *StartupHandler) SetUsersProvider(provider usersServiceInterface) {
	h.usersProvider = provider
}

// SetLocalMedia injects the local media service for home shelf defaults.
func (h *StartupHandler) SetLocalMedia(lm localLibraryLister) {
	h.localMedia = lm
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
				ID:            item.Title.ID,
				Name:          item.Title.Name,
				OriginalName:  item.Title.OriginalName,
				Overview:      item.Title.Overview,
				Year:          item.Title.Year,
				Language:      item.Title.Language,
				Poster:        item.Title.Poster,
				Backdrop:      item.Title.Backdrop,
				MediaType:     item.Title.MediaType,
				TVDBID:        item.Title.TVDBID,
				IMDBID:        item.Title.IMDBID,
				TMDBID:        item.Title.TMDBID,
				Theatrical:    item.Title.Theatrical,
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
	maxStreams := globalSettings.Live.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}

	shelves := convertShelves(globalSettings.HomeShelves.Shelves)
	if h.localMedia != nil {
		if libs, err := h.localMedia.ListLibraries(context.Background()); err == nil {
			shelves = injectLocalLibraryShelves(shelves, libs)
		}
	}

	return models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:           globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:      globalSettings.Playback.PauseWhenAppInactive,
			UseLoadingScreen:          globalSettings.Playback.UseLoadingScreen,
			SubtitleSize:              globalSettings.Playback.SubtitleSize,
			SubtitleColor:             globalSettings.Playback.SubtitleColor,
			SubtitleOpacity:           models.FloatPtr(globalSettings.Playback.SubtitleOpacity),
			SubtitleFont:              globalSettings.Playback.SubtitleFont,
			SubtitleOutlineEnabled:    models.BoolPtr(globalSettings.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:      globalSettings.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:     models.FloatPtr(globalSettings.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled: models.BoolPtr(globalSettings.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:   globalSettings.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity: models.FloatPtr(globalSettings.Playback.SubtitleBackgroundOpacity),
			CreditsAutoSkip:           globalSettings.Playback.CreditsAutoSkip || globalSettings.Playback.CreditsDetection,
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves:             shelves,
			ExploreCardPosition: string(globalSettings.HomeShelves.ExploreCardPosition),
			ItemCap:             globalSettings.HomeShelves.ItemCap,
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:    models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:  models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:     globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:       models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			RequiredTerms:     globalSettings.Filtering.RequiredTerms,
			FilterOutTerms:    globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:    globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms: globalSettings.Filtering.NonPreferredTerms,
		},
		Display: models.DisplaySettings{
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Display.BypassFilteringForAIOStreamsOnly),
			AppLanguage:                      globalSettings.Display.AppLanguage,
			Appearance: models.AppearanceSettings{
				FontScale:            globalSettings.Display.Appearance.FontScale,
				AccentColor:          globalSettings.Display.Appearance.AccentColor,
				TextColor:            globalSettings.Display.Appearance.TextColor,
				SecondaryTextColor:   globalSettings.Display.Appearance.SecondaryTextColor,
				BackgroundColor:      globalSettings.Display.Appearance.BackgroundColor,
				ModalBackgroundColor: globalSettings.Display.Appearance.ModalBackgroundColor,
				ButtonStyle:          globalSettings.Display.Appearance.ButtonStyle,
				ButtonRadius:         globalSettings.Display.Appearance.ButtonRadius,
				HighContrast:         globalSettings.Display.Appearance.HighContrast,
				ReduceOverlays:       globalSettings.Display.Appearance.ReduceOverlays,
			},
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
			MaxStreams:         &maxStreams,
		},
	}
}

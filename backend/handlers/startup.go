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
const startupExploreCollageItemCount = 4

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

type startupHistorySnapshot struct {
	watchHistory        []models.WatchHistoryItem
	playbackProgress    []models.PlaybackProgress
	watchHistoryErr     error
	playbackProgressErr error
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

	var historySnapshotOnce sync.Once
	var historySnapshot startupHistorySnapshot
	loadHistorySnapshot := func() startupHistorySnapshot {
		historySnapshotOnce.Do(func() {
			historySnapshot.watchHistory, historySnapshot.watchHistoryErr = h.history.ListWatchHistory(userID)
			historySnapshot.playbackProgress, historySnapshot.playbackProgressErr = h.history.ListPlaybackProgress(userID)
		})
		return historySnapshot
	}

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
	startupPayloadLimit := startupShelfLimit + startupExploreCollageItemCount
	var wg sync.WaitGroup

	// 2. Watchlist (capped to the home shelf plus Explore collage overflow;
	// full list is fetched on demand)
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, err := h.watchlist.List(userID)
		if err != nil {
			log.Printf("[startup] watchlist error for %s: %v", userID, err)
			return
		}
		resp.WatchlistTotal = len(items)
		items = selectStartupWatchlistItems(items, startupShelfLimit, startupExploreCollageItemCount)
		enrichWatchlistArtwork(items, h.metadata)
		resp.Watchlist = items
	}()

	// 3. Continue watching + playback progress (merged server-side so the
	// frontend doesn't need to build progress maps on the JS thread,
	// capped to the home shelf plus Explore collage overflow)
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
		snapshot := loadHistorySnapshot()
		if snapshot.playbackProgressErr != nil {
			log.Printf("[startup] playback progress error for %s: %v", userID, snapshot.playbackProgressErr)
			items = selectStartupContinueWatchingItems(items, startupShelfLimit, startupExploreCollageItemCount)
			resp.ContinueWatching = items
			return
		}
		merged := mergeProgressIntoContinueWatching(items, snapshot.playbackProgress)
		merged = selectStartupContinueWatchingItems(merged, startupShelfLimit, startupExploreCollageItemCount)
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
		snapshot := loadHistorySnapshot()
		if snapshot.watchHistoryErr != nil {
			log.Printf("[startup] watch history error for %s: %v", userID, snapshot.watchHistoryErr)
		} else {
			watchHistory = snapshot.watchHistory
		}
		if snapshot.playbackProgressErr != nil {
			log.Printf("[startup] playback progress error for %s: %v", userID, snapshot.playbackProgressErr)
		} else {
			playbackProgress = snapshot.playbackProgress
		}
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
					if len(items) > startupPayloadLimit {
						items = items[:startupPayloadLimit]
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
					if len(items) > startupPayloadLimit {
						items = items[:startupPayloadLimit]
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
	// Enrich with MDBList ratings for sort-by-rating support (bounded by startupPayloadLimit)
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

func selectStartupWatchlistItems(items []models.WatchlistItem, shelfLimit, overflowCount int) []models.WatchlistItem {
	if shelfLimit <= 0 || len(items) <= shelfLimit || overflowCount <= 0 {
		if shelfLimit > 0 && len(items) > shelfLimit {
			return items[:shelfLimit]
		}
		return items
	}

	result := append([]models.WatchlistItem(nil), items[:shelfLimit]...)
	seen := make(map[string]struct{}, len(result)*2)
	for _, item := range result {
		addStartupIdentityKeys(seen, startupWatchlistIdentityKeys(item))
	}

	fallback := make([]models.WatchlistItem, 0, overflowCount)
	fallbackSeen := make(map[string]struct{}, overflowCount*2)
	for _, item := range items[shelfLimit:] {
		keys := startupWatchlistIdentityKeys(item)
		if hasStartupIdentityKey(seen, keys) {
			continue
		}
		if !startupWatchlistHasUsableExploreArtwork(item) {
			if !hasStartupIdentityKey(fallbackSeen, keys) {
				fallback = append(fallback, item)
				addStartupIdentityKeys(fallbackSeen, keys)
			}
			continue
		}
		result = append(result, item)
		addStartupIdentityKeys(seen, keys)
		if len(result) >= shelfLimit+overflowCount {
			break
		}
	}
	for _, item := range fallback {
		if len(result) >= shelfLimit+overflowCount {
			break
		}
		keys := startupWatchlistIdentityKeys(item)
		if hasStartupIdentityKey(seen, keys) {
			continue
		}
		result = append(result, item)
		addStartupIdentityKeys(seen, keys)
	}
	return result
}

func selectStartupContinueWatchingItems(items []models.SeriesWatchState, shelfLimit, overflowCount int) []models.SeriesWatchState {
	if shelfLimit <= 0 || len(items) <= shelfLimit || overflowCount <= 0 {
		if shelfLimit > 0 && len(items) > shelfLimit {
			return items[:shelfLimit]
		}
		return items
	}

	result := append([]models.SeriesWatchState(nil), items[:shelfLimit]...)
	seen := make(map[string]struct{}, len(result)*2)
	for _, item := range result {
		addStartupIdentityKeys(seen, startupContinueWatchingIdentityKeys(item))
	}

	fallback := make([]models.SeriesWatchState, 0, overflowCount)
	fallbackSeen := make(map[string]struct{}, overflowCount*2)
	for _, item := range items[shelfLimit:] {
		keys := startupContinueWatchingIdentityKeys(item)
		if hasStartupIdentityKey(seen, keys) {
			continue
		}
		if !startupContinueWatchingHasUsableExploreArtwork(item) {
			if !hasStartupIdentityKey(fallbackSeen, keys) {
				fallback = append(fallback, item)
				addStartupIdentityKeys(fallbackSeen, keys)
			}
			continue
		}
		result = append(result, item)
		addStartupIdentityKeys(seen, keys)
		if len(result) >= shelfLimit+overflowCount {
			break
		}
	}
	for _, item := range fallback {
		if len(result) >= shelfLimit+overflowCount {
			break
		}
		keys := startupContinueWatchingIdentityKeys(item)
		if hasStartupIdentityKey(seen, keys) {
			continue
		}
		result = append(result, item)
		addStartupIdentityKeys(seen, keys)
	}
	return result
}

func startupWatchlistHasUsableExploreArtwork(item models.WatchlistItem) bool {
	return isUsableStartupExploreArtworkURL(item.PosterURL)
}

func startupContinueWatchingHasUsableExploreArtwork(item models.SeriesWatchState) bool {
	return isUsableStartupExploreArtworkURL(item.PosterURL) ||
		isUsableStartupExploreArtworkURL(item.TextPosterURL) ||
		isUsableStartupExploreArtworkURL(item.BackdropURL)
}

func isUsableStartupExploreArtworkURL(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "metadata-static.plex.tv") ||
		strings.Contains(lower, "via.placeholder.com") ||
		strings.Contains(lower, "text=no+image") ||
		strings.Contains(lower, "text=loading") {
		return false
	}
	return true
}

func startupWatchlistIdentityKeys(item models.WatchlistItem) []string {
	return startupMediaIdentityKeys(item.MediaType, item.ID, item.Name, item.Year, item.ExternalIDs)
}

func startupContinueWatchingIdentityKeys(item models.SeriesWatchState) []string {
	return startupMediaIdentityKeys("series", item.SeriesID, item.SeriesTitle, item.Year, item.ExternalIDs)
}

func startupMediaIdentityKeys(mediaType, id, name string, year int, externalIDs map[string]string) []string {
	media := strings.ToLower(strings.TrimSpace(mediaType))
	if media == "" {
		media = "unknown"
	}
	keys := make([]string, 0, 5)
	if normalizedName := normalizeStartupIdentityPart(name); normalizedName != "" {
		if year > 0 {
			keys = append(keys, fmt.Sprintf("%s:title:%s:%d", media, normalizedName, year))
		} else {
			keys = append(keys, fmt.Sprintf("%s:title:%s", media, normalizedName))
		}
	}
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		if value := normalizeStartupIdentityPart(externalIDs[provider]); value != "" {
			keys = append(keys, fmt.Sprintf("%s:%s:%s", media, provider, value))
		}
	}
	if normalizedID := normalizeStartupIdentityPart(id); normalizedID != "" {
		keys = append(keys, fmt.Sprintf("%s:id:%s", media, normalizedID))
	}
	return keys
}

func normalizeStartupIdentityPart(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func addStartupIdentityKeys(seen map[string]struct{}, keys []string) {
	for _, key := range keys {
		seen[key] = struct{}{}
	}
}

func hasStartupIdentityKey(seen map[string]struct{}, keys []string) bool {
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			return true
		}
	}
	return false
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
	// byExternalEpisode keys episode progress by each series-level external ID
	// (tvdb/tmdb/imdb) so progress recorded under one provider ID still matches a
	// continue-watching item that was canonicalised under a different one. This
	// happens when a series' episodes get recorded under more than one series ID
	// (e.g. E02 under tmdb:tv:220102 while E01/E03 use tvdb:series:450033).
	byExternalEpisode := make(map[string]float64)
	byEpisodeTvdb := make(map[string]float64)

	for _, p := range progress {
		if p.ItemID != "" {
			byItemID[p.ItemID] = p.PercentWatched
		}
		if p.ID != "" {
			byItemID[p.ID] = p.PercentWatched
		}
		if p.MediaType == "episode" {
			if p.SeriesID != "" {
				key := fmt.Sprintf("%s:S%dE%d", p.SeriesID, p.SeasonNumber, p.EpisodeNumber)
				byEpisode[key] = p.PercentWatched
			}
			for _, key := range seriesExternalEpisodeKeys(p.ExternalIDs, p.SeasonNumber, p.EpisodeNumber) {
				byExternalEpisode[key] = p.PercentWatched
			}
			if epTvdb := strings.TrimSpace(p.ExternalIDs["episodeTvdb"]); epTvdb != "" {
				byEpisodeTvdb[epTvdb] = p.PercentWatched
			}
		}
	}

	episodePercent := func(ep *models.EpisodeReference, item models.SeriesWatchState) float64 {
		if ep == nil {
			return 0
		}
		if ep.EpisodeID != "" {
			if pct, ok := byItemID[ep.EpisodeID]; ok {
				return pct
			}
		}
		key := fmt.Sprintf("%s:S%dE%d", item.SeriesID, ep.SeasonNumber, ep.EpisodeNumber)
		if pct, ok := byEpisode[key]; ok {
			return pct
		}
		for _, k := range seriesExternalEpisodeKeys(item.ExternalIDs, ep.SeasonNumber, ep.EpisodeNumber) {
			if pct, ok := byExternalEpisode[k]; ok {
				return pct
			}
		}
		if ep.TvdbID != "" {
			if pct, ok := byEpisodeTvdb[strings.TrimSpace(ep.TvdbID)]; ok {
				return pct
			}
		}
		return 0
	}

	merged := make([]models.SeriesWatchState, len(items))
	for i, item := range items {
		merged[i] = item

		if item.NextEpisode == nil {
			// Movies may already carry active/enriched progress from the
			// continue endpoint. Do not let a stale raw zero progress row erase
			// that value and make the home shelf filter the card out.
			moviePct := item.ResumePercent
			if item.PercentWatched > moviePct {
				moviePct = item.PercentWatched
			}
			if rawPct, ok := byItemID[item.SeriesID]; ok && rawPct > moviePct {
				moviePct = rawPct
			}
			merged[i].PercentWatched = moviePct
			merged[i].ResumePercent = moviePct
		} else {
			nextPct := episodePercent(item.NextEpisode, item)
			lastPct := episodePercent(&item.LastWatched, item)
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

// seriesExternalEpisodeKeys builds provider-agnostic episode lookup keys from a
// series' external IDs (tvdb/tmdb/imdb). Keying episode progress by every known
// provider ID lets progress and continue-watching items that reference the same
// show under different series IDs still resolve to the same key.
func seriesExternalEpisodeKeys(externalIDs map[string]string, season, episode int) []string {
	if len(externalIDs) == 0 {
		return nil
	}
	var keys []string
	for _, idType := range []string{"tvdb", "tmdb", "imdb"} {
		val := strings.TrimSpace(externalIDs[idType])
		if val == "" {
			continue
		}
		if idType == "imdb" {
			val = strings.ToLower(val)
		}
		keys = append(keys, fmt.Sprintf("%s:%s:S%dE%d", idType, val, season, episode))
	}
	return keys
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
				TextPoster:    item.Title.TextPoster,
				Backdrop:      item.Title.Backdrop,
				TextBackdrop:  item.Title.TextBackdrop,
				Backdrops:     item.Title.Backdrops,
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
			PreferredPlayer:               globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:        globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage:     globalSettings.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:         globalSettings.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:          globalSettings.Playback.PauseWhenAppInactive,
			UseLoadingScreen:              globalSettings.Playback.UseLoadingScreen,
			SubtitleSize:                  globalSettings.Playback.SubtitleSize,
			SubtitleUseCropDetectPosition: models.BoolPtr(globalSettings.Playback.SubtitleUseCropDetectPosition),
			SubtitleColor:                 globalSettings.Playback.SubtitleColor,
			SubtitleOpacity:               models.FloatPtr(globalSettings.Playback.SubtitleOpacity),
			SubtitleFont:                  globalSettings.Playback.SubtitleFont,
			SubtitleBold:                  models.BoolPtr(globalSettings.Playback.SubtitleBold),
			SubtitleOutlineEnabled:        models.BoolPtr(globalSettings.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:          globalSettings.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:         models.FloatPtr(globalSettings.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled:     models.BoolPtr(globalSettings.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:       globalSettings.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity:     models.FloatPtr(globalSettings.Playback.SubtitleBackgroundOpacity),
			SeekForwardSeconds:            globalSettings.Playback.SeekForwardSeconds,
			SeekBackwardSeconds:           globalSettings.Playback.SeekBackwardSeconds,
			ForceAACTranscoding:           globalSettings.Playback.ForceAACTranscoding,
			AutoPlayTrailersTV:            globalSettings.Playback.AutoPlayTrailersTV,
			RewindOnResumeFromPause:       globalSettings.Playback.RewindOnResumeFromPause,
			RewindOnPlaybackStart:         globalSettings.Playback.RewindOnPlaybackStart,
			DisablePrequeue:               globalSettings.Playback.DisablePrequeue,
			IgnoreDVCompatibilityCheck:    models.BoolPtr(globalSettings.Playback.IgnoreDVCompatibilityCheck),
			CreditsDetectionEnabled:       models.BoolPtr(globalSettings.Playback.CreditsDetectionEnabled),
			CreditsAutoSkip:               globalSettings.Playback.CreditsAutoSkip || globalSettings.Playback.CreditsDetection,
			MatchFrameRate:                models.BoolPtr(globalSettings.Playback.MatchFrameRate),
			MaxResultsPerResolution:       models.IntPtr(globalSettings.Playback.MaxResultsPerResolution),
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves:                         shelves,
			ExploreCardPosition:             string(globalSettings.HomeShelves.ExploreCardPosition),
			ItemCap:                         globalSettings.HomeShelves.ItemCap,
			ExcludeUpcomingFromContinue:     models.BoolPtr(globalSettings.HomeShelves.ExcludeUpcomingFromContinue),
			DisableTvLandscapeCardExpansion: models.BoolPtr(globalSettings.HomeShelves.DisableTvLandscapeCardExpansion),
			HomeShelfScale:                  models.FloatPtr(globalSettings.HomeShelves.HomeShelfScale),
			HomeHeroScale:                   models.FloatPtr(globalSettings.HomeShelves.HomeHeroScale),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:     models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:   models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:      globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:        models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			RequiredTerms:      globalSettings.Filtering.RequiredTerms,
			FilterOutTerms:     globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:     globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:  globalSettings.Filtering.NonPreferredTerms,
			UnknownTrackPolicy: string(globalSettings.Filtering.UnknownTrackPolicy),
		},
		Display: models.DisplaySettings{
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Display.BypassFilteringForAIOStreamsOnly),
			DisableMobileTopCarousel:         models.BoolPtr(globalSettings.Display.DisableMobileTopCarousel),
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

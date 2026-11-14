package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/mediaidentity"
	"novastream/models"
	calendarpkg "novastream/services/calendar"
	"novastream/services/history"
	"novastream/services/kids"
	metadatapkg "novastream/services/metadata"
	"novastream/services/playback"

	"github.com/gorilla/mux"
)

// defaultStartupShelfLimit caps list data in the startup bundle to reduce payload
// size on low-power devices. Full lists are fetched on demand (e.g. explore page).
const defaultStartupShelfLimit = 20
const startupExploreCollageItemCount = 4
const startupTMDBShelfLimit = 25

// startupTrendingTimeout limits how long the startup handler waits for trending
// data. On cold start, Trending() can take 20-30s enriching metadata from TMDB.
// The startup bundle gates several frontend providers, so keep this short and
// fail open with partial data instead of stalling the whole home screen.
const startupTrendingTimeout = 1500 * time.Millisecond

// Opportunistically include warm shelves; cold providers must not hold startup.
const startupHomeBundleTimeout = 250 * time.Millisecond

// startupCalendarService is the subset of the calendar service used by the
// startup handler. It reads only from the pre-built cache (non-blocking).
type startupCalendarService interface {
	GetForHomeShelf(userID string, loc *time.Location, daysBack, daysForward, limit int) []models.CalendarItem
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
	sourceAvailability sourceAvailabilityService
	userSettings       userSettingsService
	watchlist          watchlistService
	history            historyService
	metadata           metadataService
	cfgManager         *config.Manager
	users              userService
	usersProvider      usersServiceInterface // for kids profile filtering
	calendar           startupCalendarService
	localMedia         localLibraryLister
	prequeueStore      startupPrequeueStore
	hiddenItems        hiddenItemsService
	customLists        startupCustomListsService
	displayList        *DisplayListHandler
	clientSettings     clientSettingsService
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

func (h *StartupHandler) SetHiddenItemsService(service hiddenItemsService) {
	h.hiddenItems = service
}

// startupCustomListsService is the slice of the custom lists service the home
// manifest needs to notice list edits.
type startupCustomListsService interface {
	ListLists(userID string) ([]models.CustomList, error)
}

func (h *StartupHandler) SetCustomListsService(service startupCustomListsService) {
	h.customLists = service
}

func (h *StartupHandler) SetDisplayListHandler(handler *DisplayListHandler) {
	h.displayList = handler
}

func (h *StartupHandler) SetClientSettingsProvider(provider clientSettingsService) {
	h.clientSettings = provider
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
	CalendarItems    []models.CalendarItem               `json:"calendarItems,omitempty"`
	HomeShelves      map[string]StartupHomeShelfResponse `json:"homeShelves,omitempty"`
	HomeBundleErrors map[string]string                   `json:"homeBundleErrors,omitempty"`
}

type StartupHomeShelfResponse struct {
	TotalPending    bool                  `json:"totalPending,omitempty"`
	MetadataPending bool                  `json:"metadataPending,omitempty"`
	Source          string                `json:"source"`
	ListID          string                `json:"listId,omitempty"`
	Items           []models.TrendingItem `json:"items"`
	Total           int                   `json:"total"`
	UnfilteredTotal int                   `json:"unfilteredTotal,omitempty"`
}

type HomeShelfManifest struct {
	ID             string `json:"id"`
	Type           string `json:"type,omitempty"`
	Enabled        bool   `json:"enabled"`
	Order          int    `json:"order"`
	SourceKey      string `json:"sourceKey,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	HideUnreleased bool   `json:"hideUnreleased,omitempty"`
}

type HomeManifestResponse struct {
	SourceAvailabilityRevision string              `json:"sourceAvailabilityRevision,omitempty"`
	DiscoveryRevision          string              `json:"discoveryRevision,omitempty"`
	Revision                   string              `json:"revision"`
	SettingsHash               string              `json:"settingsHash"`
	ShelvesHash                string              `json:"shelvesHash"`
	ContinueWatchingRevision   string              `json:"continueWatchingRevision,omitempty"`
	WatchlistHash              string              `json:"watchlistHash"`
	EpisodeCountsHash          string              `json:"episodeCountsHash,omitempty"`
	HiddenItemsHash            string              `json:"hiddenItemsHash,omitempty"`
	CustomListsHash            string              `json:"customListsHash,omitempty"`
	WatchlistTotal             int                 `json:"watchlistTotal"`
	Shelves                    []HomeShelfManifest `json:"shelves"`
	GeneratedAt                time.Time           `json:"generatedAt"`
}

// GetHomeManifest returns a cheap fingerprint for the home page data/config.
// Clients use this before automatic refreshes so foreground/staleness checks
// can avoid reloading full shelf payloads when the backend would send the same
// home state they already have.
func (h *StartupHandler) GetHomeManifest(w http.ResponseWriter, r *http.Request) {
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

	resp := HomeManifestResponse{GeneratedAt: time.Now().UTC()}
	defaults := h.getDefaultsFromGlobal()
	settings, err := h.userSettings.GetWithDefaults(userID, defaults)
	settings.HomeShelves = models.ResolveHomeView(settings.HomeShelves, r.URL.Query().Get("homeView"))
	if err != nil {
		log.Printf("[home-manifest] user settings error for %s: %v", userID, err)
	} else {
		resp.SettingsHash = hashForManifest(settings.HomeShelves, settings.Display)
		resp.Shelves = buildHomeShelfManifest(settings.HomeShelves.Shelves)
		resp.ShelvesHash = hashForManifest(resp.Shelves)
		resp.DiscoveryRevision = regionalHomeRevision(settings.HomeShelves.Shelves, resp.GeneratedAt)
	}

	if h.history != nil {
		if revision, err := h.history.GetContinueWatchingRevision(userID); err != nil {
			log.Printf("[home-manifest] continue watching revision error for %s: %v", userID, err)
		} else {
			resp.ContinueWatchingRevision = revision
		}
	}

	if h.watchlist != nil {
		if items, err := h.watchlist.List(userID); err != nil {
			log.Printf("[home-manifest] watchlist error for %s: %v", userID, err)
		} else {
			items = h.filterHiddenWatchlistItems(userID, items)
			resp.WatchlistTotal = len(items)
			resp.WatchlistHash = watchlistManifestHash(items)
			resp.EpisodeCountsHash = cachedEpisodeCountsManifestHash(
				items,
				metadataServiceForUser(h.metadata, h.cfgManager, h.userSettings, userID),
			)
		}
	}
	resp.HiddenItemsHash = h.hiddenItemsManifestHash(userID)
	resp.CustomListsHash = h.customListsManifestHash(userID)
	if h.sourceAvailability != nil {
		resp.SourceAvailabilityRevision = h.sourceAvailability.Revision(userID, requestClientID(r))
	}

	resp.Revision = hashForManifest(
		resp.SettingsHash,
		resp.ShelvesHash,
		resp.ContinueWatchingRevision,
		resp.WatchlistHash,
		resp.EpisodeCountsHash,
		resp.WatchlistTotal,
		resp.HiddenItemsHash,
		resp.CustomListsHash,
		resp.DiscoveryRevision,
		resp.SourceAvailabilityRevision,
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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
	clientID := requestClientID(r)
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
	settings.HomeShelves = models.ResolveHomeView(settings.HomeShelves, r.URL.Query().Get("homeView"))
	if err != nil {
		log.Printf("[startup] user settings error for %s: %v", userID, err)
	} else {
		resp.UserSettings = &settings
	}
	listPolicy := resolveUnreleasedVisibilityPolicy(h.cfgManager, h.userSettings, h.clientSettings, userID, clientID, unreleasedVisibilityLists)
	if hideUnreleased {
		listPolicy.IncludeMovies = false
		listPolicy.IncludeShows = false
	}
	trendingMoviesPolicy := listPolicy
	trendingSeriesPolicy := listPolicy
	if resp.UserSettings != nil {
		if homeShelfHidesUnreleased(resp.UserSettings.HomeShelves.Shelves, "trending-movies") {
			trendingMoviesPolicy.IncludeMovies = false
			trendingMoviesPolicy.IncludeShows = false
		}
		if homeShelfHidesUnreleased(resp.UserSettings.HomeShelves.Shelves, "trending-tv") {
			trendingSeriesPolicy.IncludeMovies = false
			trendingSeriesPolicy.IncludeShows = false
		}
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleasedMovies")), "true") {
		trendingMoviesPolicy.IncludeMovies = false
		trendingMoviesPolicy.IncludeShows = false
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("hideUnreleasedSeries")), "true") {
		trendingSeriesPolicy.IncludeMovies = false
		trendingSeriesPolicy.IncludeShows = false
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
		items = h.filterHiddenWatchlistItems(userID, items)
		resp.WatchlistTotal = len(items)
		items = selectStartupWatchlistItems(items, startupShelfLimit, startupExploreCollageItemCount)
		enrichWatchlistArtwork(items, metadataServiceForUser(h.metadata, h.cfgManager, h.userSettings, userID))
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
		items = h.filterHiddenContinueWatchingItems(userID, items)
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

	// 5b. Calendar home-shelf items. Non-blocking:
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
			resp.CalendarItems = h.calendar.GetForHomeShelf(
				userID,
				loc,
				calendarpkg.MaxRecentDaysWindow,
				14,
				startupShelfLimit,
			)
			resp.CalendarItems = h.filterHiddenCalendarItems(userID, resp.CalendarItems)
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
		metadataSvc := metadataServiceForUser(h.metadata, h.cfgManager, h.userSettings, userID)
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
					items, err := metadataSvc.Trending(trendingCtx, "movie")
					if err != nil {
						log.Printf("[startup] trending movies error: %v", err)
						return
					}
					items = h.applyFilters(items, userID, trendingMoviesPolicy, hideWatched)
					items = h.filterHiddenTrendingItems(userID, items)
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
					items, err := metadataSvc.Trending(trendingCtx, "series")
					if err != nil {
						log.Printf("[startup] trending series error: %v", err)
						return
					}
					items = h.applyFilters(items, userID, trendingSeriesPolicy, hideWatched)
					items = h.filterHiddenTrendingItems(userID, items)
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

	// Enrich saved watchlist titles without filtering by release visibility,
	// matching the personal display-list endpoint.
	idx := buildWatchStateIndex(watchHistory, resp.ContinueWatching, playbackProgress)
	startupMetadataSvc := metadataServiceForUser(h.metadata, h.cfgManager, h.userSettings, userID)
	warmEpisodeCounts := resp.UserSettings != nil && stringSliceContainsFold(resp.UserSettings.Display.BadgeVisibility, "unwatchedCount")
	enrichWatchlistItems(resp.Watchlist, idx, startupMetadataSvc, warmEpisodeCounts)
	// Enrich with MDBList ratings for sort-by-rating support (bounded by startupPayloadLimit)
	enrichWatchlistRatings(r.Context(), resp.Watchlist, h.metadata)
	// Match display-list watchlist enrichment so the initial home shelf does not
	// need to repair missing movie release metadata after first paint.
	enrichDisplayListReleases(r, resp.Watchlist, h.metadata)
	if resp.TrendingMovies != nil {
		enrichTrendingItems(resp.TrendingMovies.Items, idx, startupMetadataSvc, false)
	}
	if resp.TrendingSeries != nil {
		enrichTrendingItems(resp.TrendingSeries.Items, idx, startupMetadataSvc, false)
	}

	if resp.UserSettings != nil && h.displayList != nil {
		homeCtx, cancel := context.WithTimeout(r.Context(), startupHomeBundleTimeout)
		homeShelves, homeErrors := h.buildStartupHomeShelves(homeCtx, r, userID, resp.UserSettings.HomeShelves.Shelves, startupShelfLimit, hideWatched, clientID)
		cancel()
		if len(homeShelves) > 0 {
			resp.HomeShelves = homeShelves
		}
		if len(homeErrors) > 0 {
			resp.HomeBundleErrors = homeErrors
		}
	}
	if resp.UserSettings != nil && !watchSupportsNativeTMDBShelves(r) {
		compatibleSettings := startupSettingsForWatch(*resp.UserSettings)
		resp.UserSettings = &compatibleSettings
	}
	resp.Watchlist = availabilityWatchlist(h.sourceAvailability, userID, clientID, resp.Watchlist)
	if resp.TrendingMovies != nil {
		resp.TrendingMovies.Items = availabilityTrending(h.sourceAvailability, userID, clientID, resp.TrendingMovies.Items)
	}
	if resp.TrendingSeries != nil {
		resp.TrendingSeries.Items = availabilityTrending(h.sourceAvailability, userID, clientID, resp.TrendingSeries.Items)
	}
	for id, shelf := range resp.HomeShelves {
		shelf.Items = availabilityTrending(h.sourceAvailability, userID, clientID, shelf.Items)
		resp.HomeShelves[id] = shelf
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// watchSupportsNativeTMDBShelves provides a clean cutover for Watch builds
// that implement the native tmdb shelf type. Older clients omit the feature
// and continue to receive the MDBList sentinel compatibility representation.
func watchSupportsNativeTMDBShelves(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("nativeTMDBShelves")), "true") {
		return true
	}
	for _, feature := range strings.Split(r.Header.Get("X-Mediastorm-Features"), ",") {
		if strings.EqualFold(strings.TrimSpace(feature), "tmdb-shelves") {
			return true
		}
	}
	return false
}

// startupSettingsForWatch keeps TMDB shelves visible in the bundled Watch
// client. Watch currently recognizes custom shelves through its MDBList,
// Trakt, Simkl, Letterboxd, genre, and decade types before consuming the
// preloaded payload keyed by shelf ID. Representing TMDB as an MDBList shelf
// with a non-empty sentinel URL passes that client-side eligibility check;
// the original TMDB fields remain present and the backend still preloads the
// shelf through the TMDB source.
func startupSettingsForWatch(settings models.UserSettings) models.UserSettings {
	shelves := settings.HomeShelves.Shelves
	settings.HomeShelves.Shelves = append([]models.ShelfConfig(nil), shelves...)
	for i := range settings.HomeShelves.Shelves {
		shelf := &settings.HomeShelves.Shelves[i]
		if !strings.EqualFold(strings.TrimSpace(shelf.Type), "tmdb") {
			continue
		}
		shelf.Type = "mdblist"
		shelf.ListURL = watchTMDBShelfURLPrefix + shelf.ID
	}
	return settings
}

func homeShelfHidesUnreleased(shelves []models.ShelfConfig, id string) bool {
	for i := range shelves {
		if shelves[i].ID == id {
			return shelves[i].HideUnreleased
		}
	}
	return false
}

func (h *StartupHandler) hiddenItemsManifestHash(userID string) string {
	if h.hiddenItems == nil {
		return ""
	}
	items, err := h.hiddenItems.List(userID)
	if err != nil || len(items) == 0 {
		return ""
	}
	return hashForManifest(items)
}

// customListsManifestHash fingerprints the profile's custom lists so adding to,
// removing from, renaming or deleting a list changes the home revision and
// clients reload their custom list shelves. Every item edit bumps the list's
// UpdatedAt, so the list summaries are enough.
func (h *StartupHandler) customListsManifestHash(userID string) string {
	if h.customLists == nil {
		return ""
	}
	lists, err := h.customLists.ListLists(userID)
	if err != nil || len(lists) == 0 {
		return ""
	}
	keys := make([]string, 0, len(lists))
	for _, list := range lists {
		keys = append(keys, fmt.Sprintf("%s:%s:%d:%d", list.ID, list.Name, list.ItemCount, list.UpdatedAt.UnixNano()))
	}
	sort.Strings(keys)
	return hashForManifest(keys)
}

func (h *StartupHandler) filterHiddenWatchlistItems(userID string, items []models.WatchlistItem) []models.WatchlistItem {
	if h.hiddenItems == nil || len(items) == 0 {
		return items
	}
	return h.hiddenItems.FilterHiddenWatchlistItems(userID, items)
}

func (h *StartupHandler) filterHiddenContinueWatchingItems(userID string, items []models.SeriesWatchState) []models.SeriesWatchState {
	if h.hiddenItems == nil || len(items) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if h.hiddenItems.IsHidden(userID, "series", item.SeriesID, item.ExternalIDs) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (h *StartupHandler) filterHiddenCalendarItems(userID string, items []models.CalendarItem) []models.CalendarItem {
	if h.hiddenItems == nil || len(items) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if h.hiddenItems.IsHidden(userID, item.MediaType, "", item.ExternalIDs) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (h *StartupHandler) filterHiddenTrendingItems(userID string, items []models.TrendingItem) []models.TrendingItem {
	if h.hiddenItems == nil || len(items) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		externalIDs := make(map[string]string, 3)
		if item.Title.TMDBID > 0 {
			externalIDs["tmdb"] = strconv.FormatInt(item.Title.TMDBID, 10)
		}
		if item.Title.TVDBID > 0 {
			externalIDs["tvdb"] = strconv.FormatInt(item.Title.TVDBID, 10)
		}
		if strings.TrimSpace(item.Title.IMDBID) != "" {
			externalIDs["imdb"] = item.Title.IMDBID
		}
		if h.hiddenItems.IsHidden(userID, item.Title.MediaType, item.Title.ID, externalIDs) {
			continue
		}
		out = append(out, item)
	}
	return out
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

// selectStartupContinueWatchingItems applies the startup recency cap but always
// retains continue-watching series whose next episode has not aired yet. The home
// "My Upcoming" shelf is derived from the continue-watching list, so a
// stale-but-upcoming series (e.g. a returning show last watched months ago) would
// otherwise silently drop off once newer activity pushed it past the cap.
func selectStartupContinueWatchingItems(items []models.SeriesWatchState, shelfLimit, overflowCount int) []models.SeriesWatchState {
	capped := selectStartupContinueWatchingCapped(items, shelfLimit, overflowCount)
	return appendUpcomingContinueWatchingItems(capped, items)
}

func selectStartupContinueWatchingCapped(items []models.SeriesWatchState, shelfLimit, overflowCount int) []models.SeriesWatchState {
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

// appendUpcomingContinueWatchingItems appends any continue-watching series with an
// unreleased next episode that the recency cap excluded, deduplicating against the
// already-selected items. This keeps the "My Upcoming" home shelf complete without
// inflating the startup payload with the full continue-watching list.
func appendUpcomingContinueWatchingItems(result, all []models.SeriesWatchState) []models.SeriesWatchState {
	if len(result) >= len(all) {
		return result
	}
	seen := make(map[string]struct{}, len(result)*2)
	for _, item := range result {
		addStartupIdentityKeys(seen, startupContinueWatchingIdentityKeys(item))
	}
	for _, item := range all {
		if !continueWatchingHasUpcomingEpisode(item) {
			continue
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

// continueWatchingHasUpcomingEpisode reports whether the series' next episode has a
// known air date that is still in the future. Mirrors the frontend
// isUpcomingContinueWatchingItem/isEpisodeUnreleased helpers so the "My Upcoming"
// shelf stays in sync with what the backend ships.
func continueWatchingHasUpcomingEpisode(item models.SeriesWatchState) bool {
	next := item.NextEpisode
	if next == nil || strings.TrimSpace(next.AirDate) == "" {
		return false
	}
	now := time.Now()
	if ts := strings.TrimSpace(next.AirDateTimeUTC); ts != "" {
		if airDT, err := time.Parse(time.RFC3339, ts); err == nil {
			return airDT.After(now)
		}
	}
	// Date-only fallback: compare against the start of today in local time.
	if airDate, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(next.AirDate), now.Location()); err == nil {
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return airDate.After(today)
	}
	return false
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

func (h *StartupHandler) buildStartupHomeShelves(ctx context.Context, sourceReq *http.Request, userID string, shelves []models.ShelfConfig, homeShelfLimit int, hideWatched bool, clientID string) (map[string]StartupHomeShelfResponse, map[string]string) {
	if h.displayList == nil {
		return nil, nil
	}
	return buildStartupHomeShelvesWithHandler(ctx, sourceReq, userID, shelves, homeShelfLimit, hideWatched, clientID, h.displayList.Get)
}

func buildStartupHomeShelvesWithHandler(ctx context.Context, sourceReq *http.Request, userID string, shelves []models.ShelfConfig, homeShelfLimit int, hideWatched bool, clientID string, get http.HandlerFunc) (map[string]StartupHomeShelfResponse, map[string]string) {
	out := make(map[string]StartupHomeShelfResponse)
	errs := make(map[string]string)
	type result struct {
		id       string
		response StartupHomeShelfResponse
		err      string
	}
	results := make(chan result, len(shelves))
	slots := make(chan struct{}, 4)
	pending := make(map[string]bool)
	for _, shelf := range shelves {
		if !shelf.Enabled || !isStartupFetchableCustomShelf(shelf) {
			continue
		}
		query, ok := startupDisplayListQueryForShelf(shelf, homeShelfLimit, hideWatched, clientID)
		if !ok {
			continue
		}
		if view := sourceReq.URL.Query().Get("homeView"); config.IsHomeViewID(view) {
			query.Set("homeView", view)
		}
		pending[shelf.ID] = true
		go func(id string, query url.Values) {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			if ctx.Err() != nil {
				return
			}
			req := sourceReq.Clone(ctx)
			req.Method = http.MethodGet
			req.URL = &url.URL{Path: "/api/users/" + url.PathEscape(userID) + "/display-list", RawQuery: query.Encode()}
			req = mux.SetURLVars(req, map[string]string{"userID": userID})
			rec := httptest.NewRecorder()
			get(rec, req)
			item := result{id: id}
			if rec.Code >= http.StatusBadRequest {
				item.err = strings.TrimSpace(rec.Body.String())
				if item.err == "" {
					item.err = http.StatusText(rec.Code)
				}
			} else if err := json.NewDecoder(rec.Body).Decode(&item.response); err != nil {
				item.err = err.Error()
			} else {
				if item.response.Items == nil {
					item.response.Items = []models.TrendingItem{}
				}
				if item.response.Total == 0 {
					item.response.Total = len(item.response.Items)
				}
			}
			results <- item
		}(shelf.ID, query)
	}
	// Only this goroutine owns response maps. Buffered results let handlers that
	// ignore cancellation finish safely without extending the startup deadline.
	for len(pending) > 0 {
		select {
		case item := <-results:
			delete(pending, item.id)
			if item.err != "" {
				errs[item.id] = item.err
			} else {
				out[item.id] = item.response
			}
		case <-ctx.Done():
			for id := range pending {
				errs[id] = ctx.Err().Error()
			}
			return out, errs
		}
	}
	return out, errs
}

func isStartupFetchableCustomShelf(shelf models.ShelfConfig) bool {
	switch strings.ToLower(strings.TrimSpace(shelf.Type)) {
	case "mdblist":
		return strings.TrimSpace(shelf.ListURL) != ""
	case "stremio":
		return strings.TrimSpace(shelf.AddonManifestURL) != "" &&
			strings.TrimSpace(shelf.AddonCatalogType) != "" &&
			strings.TrimSpace(shelf.AddonCatalogID) != ""
	case "tmdb":
		return strings.TrimSpace(shelf.TMDBSourceType) == metadatapkg.TMDBSourceCustomDiscover ||
			(strings.TrimSpace(shelf.TMDBSourceType) != "" && strings.TrimSpace(shelf.TMDBSourceID) != "")
	case "trakt":
		return strings.TrimSpace(shelf.TraktAccountID) != "" && strings.TrimSpace(shelf.TraktListType) != ""
	case "publicmetadb":
		return strings.TrimSpace(shelf.PublicMetaDBAccountID) != "" && strings.TrimSpace(shelf.PublicMetaDBListID) != ""
	case "simkl":
		return strings.TrimSpace(shelf.SimklAccountID) != "" && strings.TrimSpace(shelf.SimklMediaType) != ""
	case "letterboxd":
		return strings.TrimSpace(shelf.LetterboxdListID) != "" || strings.TrimSpace(shelf.LetterboxdListURL) != ""
	case "genre":
		_, _, ok := parseStartupGenreShelfID(shelf.ID)
		return ok
	case "decade":
		_, _, ok := parseStartupDecadeShelfID(shelf.ID)
		return ok
	default:
		switch shelf.ID {
		case "popular-on-server", "recently-watched", "permanent-prequeue":
			return true
		default:
			return false
		}
	}
}

func startupDisplayListQueryForShelf(shelf models.ShelfConfig, homeShelfLimit int, hideWatched bool, clientID string) (url.Values, bool) {
	limit := startupCustomShelfFetchLimit(shelf, homeShelfLimit)
	query := url.Values{}
	query.Set("shelfPhase", "cards")
	query.Set("deferArtwork", "true")
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if shelf.HideUnreleased {
		query.Set("hideUnreleased", "true")
	}
	if hideWatched {
		query.Set("hideWatched", "true")
	}
	if strings.TrimSpace(clientID) != "" {
		query.Set("clientId", strings.TrimSpace(clientID))
	}
	if strings.TrimSpace(shelf.Name) != "" {
		query.Set("name", shelf.Name)
	}

	switch strings.ToLower(strings.TrimSpace(shelf.Type)) {
	case "mdblist":
		query.Set("source", "mdblist")
		query.Set("url", strings.TrimSpace(shelf.ListURL))
	case "stremio":
		query.Set("source", "stremio")
		query.Set("manifestUrl", strings.TrimSpace(shelf.AddonManifestURL))
		query.Set("catalogType", strings.TrimSpace(shelf.AddonCatalogType))
		query.Set("catalogId", strings.TrimSpace(shelf.AddonCatalogID))
	case "tmdb":
		query.Set("source", "tmdb-list")
		query.Set("sourceType", strings.TrimSpace(shelf.TMDBSourceType))
		query.Set("sourceId", strings.TrimSpace(shelf.TMDBSourceID))
		query.Set("mediaType", strings.TrimSpace(shelf.TMDBMediaType))
		query.Set("sort", strings.TrimSpace(shelf.Sort))
		query.Set("discoverQuery", strings.TrimSpace(shelf.TMDBDiscoverQuery))
		query.Set("lite", "true")
		query.Set("artworkLimit", strconv.Itoa(minInt(limit, homeShelfLimit+startupExploreCollageItemCount)))
	case "trakt":
		query.Set("source", "trakt-list")
		query.Set("accountId", strings.TrimSpace(shelf.TraktAccountID))
		query.Set("listType", strings.TrimSpace(shelf.TraktListType))
		if strings.TrimSpace(shelf.TraktListID) != "" {
			query.Set("listId", strings.TrimSpace(shelf.TraktListID))
		}
	case "publicmetadb":
		query.Set("source", "publicmetadb-list")
		query.Set("accountId", strings.TrimSpace(shelf.PublicMetaDBAccountID))
		query.Set("listId", strings.TrimSpace(shelf.PublicMetaDBListID))
	case "simkl":
		query.Set("source", "simkl-list")
		query.Set("accountId", strings.TrimSpace(shelf.SimklAccountID))
		query.Set("mediaType", strings.TrimSpace(shelf.SimklMediaType))
		if strings.TrimSpace(shelf.SimklListType) != "" {
			query.Set("listType", strings.TrimSpace(shelf.SimklListType))
		}
	case "letterboxd":
		query.Set("source", "letterboxd-list")
		if strings.TrimSpace(shelf.LetterboxdListID) != "" {
			query.Set("listId", strings.TrimSpace(shelf.LetterboxdListID))
		}
		if strings.TrimSpace(shelf.LetterboxdListURL) != "" {
			query.Set("listUrl", strings.TrimSpace(shelf.LetterboxdListURL))
		}
	case "genre":
		genreID, mediaType, ok := parseStartupGenreShelfID(shelf.ID)
		if !ok {
			return nil, false
		}
		query.Set("source", "genre")
		query.Set("genreId", strconv.FormatInt(genreID, 10))
		query.Set("mediaType", mediaType)
		query.Set("lite", "true")
		query.Set("artworkLimit", strconv.Itoa(minInt(limit, homeShelfLimit+startupExploreCollageItemCount)))
	case "decade":
		decade, mediaType, ok := parseStartupDecadeShelfID(shelf.ID)
		if !ok {
			return nil, false
		}
		query.Set("source", "decade")
		query.Set("decade", strconv.Itoa(decade))
		query.Set("mediaType", mediaType)
		query.Set("lite", "true")
		query.Set("artworkLimit", strconv.Itoa(minInt(limit, homeShelfLimit+startupExploreCollageItemCount)))
	default:
		switch shelf.ID {
		case "popular-on-server":
			query.Set("source", "popular-on-server")
			if shelf.ActivityWindowDays > 0 {
				query.Set("activityWindowDays", strconv.Itoa(shelf.ActivityWindowDays))
			}
			if shelf.MinimumProfiles > 0 {
				query.Set("minimumProfiles", strconv.Itoa(shelf.MinimumProfiles))
			}
			return query, true
		case "recently-watched":
			query.Set("source", "recently-watched")
			if shelf.ActivityWindowDays > 0 {
				query.Set("activityWindowDays", strconv.Itoa(shelf.ActivityWindowDays))
			}
			if shelf.MaxItemsPerProfile > 0 {
				query.Set("maxItemsPerProfile", strconv.Itoa(shelf.MaxItemsPerProfile))
			}
			return query, true
		case "permanent-prequeue":
			query.Set("source", "permanent-prequeue")
			return query, true
		default:
			return nil, false
		}
	}
	return query, true
}

func startupCustomShelfFetchLimit(shelf models.ShelfConfig, homeShelfLimit int) int {
	if homeShelfLimit <= 0 {
		homeShelfLimit = defaultStartupShelfLimit
	}
	shelfType := strings.ToLower(strings.TrimSpace(shelf.Type))
	if shelf.Limit > 0 {
		if shelfType == "tmdb" {
			return minInt(shelf.Limit, startupTMDBShelfLimit)
		}
		if shelf.Limit >= homeShelfLimit {
			return maxInt(shelf.Limit, homeShelfLimit+startupExploreCollageItemCount)
		}
		return shelf.Limit
	}
	switch shelfType {
	case "tmdb":
		return startupTMDBShelfLimit
	case "genre", "decade":
		return maxInt(homeShelfLimit, 50)
	default:
		return homeShelfLimit + startupExploreCollageItemCount
	}
}

func parseStartupGenreShelfID(id string) (int64, string, bool) {
	parts := strings.Split(strings.TrimSpace(id), "-")
	if len(parts) != 3 || parts[0] != "genre" {
		return 0, "", false
	}
	genreID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || genreID <= 0 || (parts[2] != "movie" && parts[2] != "tv") {
		return 0, "", false
	}
	return genreID, parts[2], true
}

func parseStartupDecadeShelfID(id string) (int, string, bool) {
	parts := strings.Split(strings.TrimSpace(id), "-")
	if len(parts) != 3 || parts[0] != "decade" {
		return 0, "", false
	}
	decade, err := strconv.Atoi(parts[1])
	if err != nil || decade < 1800 || (parts[2] != "movie" && parts[2] != "tv") {
		return 0, "", false
	}
	return decade, parts[2], true
}

func buildHomeShelfManifest(shelves []models.ShelfConfig) []HomeShelfManifest {
	out := make([]HomeShelfManifest, 0, len(shelves))
	for _, shelf := range shelves {
		entry := HomeShelfManifest{
			ID:             shelf.ID,
			Type:           shelf.Type,
			Enabled:        shelf.Enabled,
			Order:          shelf.Order,
			Limit:          shelf.Limit,
			HideUnreleased: shelf.HideUnreleased,
			SourceKey:      homeShelfSourceKey(shelf),
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	return out
}

func homeShelfSourceKey(shelf models.ShelfConfig) string {
	switch shelf.Type {
	case "mdblist":
		return "mdblist:" + strings.TrimSpace(shelf.ListURL)
	case "stremio":
		return fmt.Sprintf("stremio:%s:%s:%s", hashForManifest(strings.TrimSpace(shelf.AddonManifestURL)), shelf.AddonCatalogType, shelf.AddonCatalogID)
	case "tmdb":
		return fmt.Sprintf("tmdb:%s:%s:%s:%s:%s", shelf.TMDBSourceType, shelf.TMDBSourceID, shelf.TMDBMediaType, shelf.Sort, shelf.TMDBDiscoverQuery)
	case "trakt":
		return fmt.Sprintf("trakt:%s:%s:%s", shelf.TraktAccountID, shelf.TraktListType, shelf.TraktListID)
	case "publicmetadb":
		return fmt.Sprintf("publicmetadb:%s:%s", shelf.PublicMetaDBAccountID, shelf.PublicMetaDBListID)
	case "simkl":
		return fmt.Sprintf("simkl:%s:%s:%s", shelf.SimklAccountID, shelf.SimklMediaType, shelf.SimklListType)
	case "letterboxd":
		return fmt.Sprintf("letterboxd:%s:%s", shelf.LetterboxdListID, shelf.LetterboxdListURL)
	case "library":
		return "library:" + strings.TrimSpace(shelf.LibraryID)
	case "genre", "decade", "collection-hub", "local-library":
		return shelf.Type + ":" + shelf.ID
	default:
		switch shelf.ID {
		case "popular-on-server":
			return fmt.Sprintf("popular-on-server:%d:%d", shelf.ActivityWindowDays, shelf.MinimumProfiles)
		case "recently-watched":
			return fmt.Sprintf("recently-watched:%d:%d", shelf.ActivityWindowDays, shelf.MaxItemsPerProfile)
		case "permanent-prequeue":
			return "permanent-prequeue"
		}
		if strings.TrimSpace(shelf.ListURL) != "" {
			return "mdblist:" + strings.TrimSpace(shelf.ListURL)
		}
		return shelf.ID
	}
}

func watchlistManifestHash(items []models.WatchlistItem) string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, fmt.Sprintf("%s:%s:%s:%d", item.MediaType, item.ID, item.Name, item.Year))
	}
	sort.Strings(keys)
	return hashForManifest(keys)
}

func cachedEpisodeCountsManifestHash(items []models.WatchlistItem, metadata any) string {
	provider, ok := metadata.(releasedEpisodeCountProvider)
	if !ok {
		return ""
	}
	counts := make([]string, 0, len(items))
	for _, item := range items {
		if mediaidentity.NormalizeMediaType(item.MediaType) != "series" {
			continue
		}
		count, cached := provider.GetCachedReleasedEpisodeCount(seriesEpisodeCountQuery(item))
		if !cached {
			counts = append(counts, item.ID+":missing")
			continue
		}
		counts = append(counts, fmt.Sprintf("%s:%d", item.ID, count))
	}
	sort.Strings(counts)
	return hashForManifest(counts)
}

func hashForManifest(values ...interface{}) string {
	data, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// applyFilters applies release visibility, hideWatched, and kids rating filters to trending items.
func (h *StartupHandler) applyFilters(items []models.TrendingItem, userID string, policy unreleasedVisibilityPolicy, hideWatched bool) []models.TrendingItem {
	items = filterTrendingItemsByUnreleasedVisibility(items, policy)
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

// mergeProgressIntoContinueWatching computes PercentWatched, ResumePercent and
// the server resume state for each continue-watching item. The logic lives in
// the history service so every Continue Watching response shares it.
func mergeProgressIntoContinueWatching(items []models.SeriesWatchState, progress []models.PlaybackProgress) []models.SeriesWatchState {
	return history.MergeProgressIntoContinueWatching(items, progress)
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
				ID:                item.Title.ID,
				Name:              item.Title.Name,
				OriginalName:      item.Title.OriginalName,
				Overview:          item.Title.Overview,
				Year:              item.Title.Year,
				Language:          item.Title.Language,
				Poster:            item.Title.Poster,
				TextPoster:        item.Title.TextPoster,
				Backdrop:          item.Title.Backdrop,
				TextBackdrop:      item.Title.TextBackdrop,
				Backdrops:         item.Title.Backdrops,
				MediaType:         item.Title.MediaType,
				TVDBID:            item.Title.TVDBID,
				IMDBID:            item.Title.IMDBID,
				TMDBID:            item.Title.TMDBID,
				Status:            item.Title.Status,
				LifecycleStatus:   item.Title.LifecycleStatus,
				Theatrical:        item.Title.Theatrical,
				HomeRelease:       item.Title.HomeRelease,
				Certification:     item.Title.Certification,
				Genres:            item.Title.Genres,
				CardSubtitle:      item.Title.CardSubtitle,
				CardImage:         item.Title.CardImage,
				ForceTitleOverlay: item.Title.ForceTitleOverlay,
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

	return models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:               globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:        globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage:     globalSettings.Playback.PreferredSubtitleLanguage,
			AllowedTrackLanguages:         models.StringSlicePtr(globalSettings.Playback.AllowedTrackLanguages),
			PreferredSubtitleMode:         globalSettings.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:          models.BoolPtr(globalSettings.Playback.PauseWhenAppInactive),
			UseLoadingScreen:              models.BoolPtr(globalSettings.Playback.UseLoadingScreen),
			SubtitleSize:                  globalSettings.Playback.SubtitleSize,
			SubtitleUseCropDetectPosition: models.BoolPtr(globalSettings.Playback.SubtitleUseCropDetectPosition),
			SubtitleColor:                 globalSettings.Playback.SubtitleColor,
			SubtitleOpacity:               models.FloatPtr(globalSettings.Playback.SubtitleOpacity),
			SubtitleFont:                  models.StringPtr(globalSettings.Playback.SubtitleFont),
			SubtitleBold:                  models.BoolPtr(globalSettings.Playback.SubtitleBold),
			SubtitleOutlineEnabled:        models.BoolPtr(globalSettings.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:          globalSettings.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:         models.FloatPtr(globalSettings.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled:     models.BoolPtr(globalSettings.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:       globalSettings.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity:     models.FloatPtr(globalSettings.Playback.SubtitleBackgroundOpacity),
			SeekForwardSeconds:            globalSettings.Playback.SeekForwardSeconds,
			SeekBackwardSeconds:           globalSettings.Playback.SeekBackwardSeconds,
			ForceAACTranscoding:           models.BoolPtr(globalSettings.Playback.ForceAACTranscoding),
			AutoPlayTrailersTV:            models.BoolPtr(globalSettings.Playback.AutoPlayTrailersTV),
			RewindOnResumeFromPause:       models.IntPtr(globalSettings.Playback.RewindOnResumeFromPause),
			RewindOnPlaybackStart:         models.IntPtr(globalSettings.Playback.RewindOnPlaybackStart),
			DisablePrequeue:               models.BoolPtr(globalSettings.Playback.DisablePrequeue),
			PrerollMode:                   globalSettings.Playback.PrerollMode,
			PrerollAssetID:                globalSettings.Playback.PrerollAssetID,
			PrerollMediaScope:             globalSettings.Playback.PrerollMediaScope,
			PrerollSkipIfPrequeueReady:    models.BoolPtr(globalSettings.Playback.PrerollSkipIfPrequeueReady),
			StreamMigrationEnabled:        models.BoolPtr(globalSettings.Playback.StreamMigrationEnabled),
			IgnoreDVCompatibilityCheck:    models.BoolPtr(globalSettings.Playback.IgnoreDVCompatibilityCheck),
			CreditsDetectionEnabled:       models.BoolPtr(globalSettings.Playback.CreditsDetectionEnabled),
			CreditsAutoSkip:               models.BoolPtr(globalSettings.Playback.CreditsAutoSkip || globalSettings.Playback.CreditsDetection),
			ScrobbleStartDelaySeconds:     models.IntPtr(globalSettings.Playback.ScrobbleStartDelaySeconds),
			MatchFrameRate:                models.BoolPtr(globalSettings.Playback.MatchFrameRate),
			LiveClosedCaptionExtraction:   models.BoolPtr(globalSettings.Playback.LiveClosedCaptionExtraction),
			MaxResultsPerResolution:       models.IntPtr(globalSettings.Playback.MaxResultsPerResolution),
		},
		HomeShelves: models.HomeShelvesSettings{
			Views:                           globalSettings.HomeShelves.Views,
			Shelves:                         shelves,
			ExploreCardPosition:             string(globalSettings.HomeShelves.ExploreCardPosition),
			ItemCap:                         globalSettings.HomeShelves.ItemCap,
			ExcludeUpcomingFromContinue:     models.BoolPtr(globalSettings.HomeShelves.ExcludeUpcomingFromContinue),
			HomeShelfFocusModel:             globalSettings.HomeShelves.HomeShelfFocusModel,
			DisableTvLandscapeCardExpansion: models.BoolPtr(globalSettings.HomeShelves.DisableTvLandscapeCardExpansion),
			HomeShelfScale:                  models.FloatPtr(globalSettings.HomeShelves.HomeShelfScale),
			HomeHeroScale:                   models.FloatPtr(globalSettings.HomeShelves.HomeHeroScale),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                         models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                       models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                          models.StringPtr(globalSettings.Filtering.MaxResolution),
			HDRDVPolicy:                            models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			RequiredTerms:                          globalSettings.Filtering.RequiredTerms,
			FilterOutTerms:                         globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:                         globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:                      globalSettings.Filtering.NonPreferredTerms,
			PreferredScraper:                       models.StringPtr(globalSettings.Filtering.PreferredScraper),
			ServicePriority:                        models.StringPtr(string(globalSettings.Filtering.ServicePriority)),
			UnknownTrackPolicy:                     string(globalSettings.Filtering.UnknownTrackPolicy),
			AdaptivePlaybackEnabled:                models.BoolPtr(globalSettings.Filtering.AdaptivePlaybackEnabled),
			AdaptiveTargetBufferFactor:             models.FloatPtr(globalSettings.Filtering.AdaptiveTargetBufferFactor),
			RealDebridRestrictedTermsFilterEnabled: models.BoolPtr(globalSettings.Filtering.RealDebridRestrictedTermsFilterEnabled),
		},
		Display: models.DisplaySettings{
			BadgeVisibility:                              globalSettings.Display.BadgeVisibility,
			NavigationTabVisibility:                      globalSettings.Display.NavigationTabVisibility,
			WatchStateIconStyle:                          globalSettings.Display.WatchStateIconStyle,
			IncludeUnreleasedMoviesInLists:               models.BoolPtr(globalSettings.Display.IncludeUnreleasedMoviesInLists),
			IncludeUnreleasedShowsInLists:                models.BoolPtr(globalSettings.Display.IncludeUnreleasedShowsInLists),
			IncludeUnreleasedMoviesInSearch:              models.BoolPtr(globalSettings.Display.IncludeUnreleasedMoviesInSearch),
			IncludeUnreleasedShowsInSearch:               models.BoolPtr(globalSettings.Display.IncludeUnreleasedShowsInSearch),
			BypassFilteringForAIOStreamsOnly:             models.BoolPtr(globalSettings.Display.BypassFilteringForAIOStreamsOnly),
			ShowStreamSourceInfo:                         models.BoolPtr(globalSettings.Display.ShowStreamSourceInfo),
			DisableMobileTopCarousel:                     models.BoolPtr(globalSettings.Display.DisableMobileTopCarousel),
			HideContinueWatchingHeroMetadata:             models.BoolPtr(globalSettings.Display.HideContinueWatchingHeroMetadata),
			MoveDetailsRatingsToMetadata:                 models.BoolPtr(globalSettings.Display.MoveDetailsRatingsToMetadata),
			HideDetailsPoster:                            models.BoolPtr(globalSettings.Display.HideDetailsPoster),
			HideTVDrawerRail:                             models.BoolPtr(globalSettings.Display.HideTVDrawerRail),
			SimpleMode:                                   models.BoolPtr(globalSettings.Display.SimpleMode),
			SimpleModeHomeShelves:                        models.StringSlicePtr(globalSettings.Display.SimpleModeHomeShelves),
			DisableTVHomeCardDimming:                     models.BoolPtr(globalSettings.Display.DisableTVHomeCardDimming),
			EnableAnimations:                             models.BoolPtr(globalSettings.Display.EnableAnimations),
			EnableHeroArtPanning:                         models.BoolPtr(globalSettings.Display.EnableHeroArtPanning),
			EnableHeroArtRotation:                        models.BoolPtr(globalSettings.Display.EnableHeroArtRotation),
			ShowSeriesBackdropForMissingEpisodeArt:       models.BoolPtr(globalSettings.Display.ShowSeriesBackdropForMissingEpisodeArt),
			BlurUnwatchedEpisodeThumbnails:               models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeThumbnails),
			BlurUnwatchedEpisodeThumbnailsIncludeCurrent: models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent),
			BlurUnwatchedEpisodeOverviews:                models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeOverviews),
			BlurUnwatchedEpisodeOverviewsIncludeCurrent:  models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeOverviewsIncludeCurrent),
			AppLanguage:                                  globalSettings.Display.AppLanguage,
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

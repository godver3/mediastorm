package calendar

import (
	"context"
	"fmt"
	"log"
	"novastream/models"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const calendarMetadataConcurrency = 8

// MetadataService provides series and movie metadata.
type MetadataService interface {
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	SeriesDetailsLite(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	Trending(ctx context.Context, mediaType string) ([]models.TrendingItem, error)
	GetCustomListForCalendar(ctx context.Context, listURL string, limit int, label string) ([]models.TrendingItem, error)
}

// WatchlistService provides access to a user's watchlist.
type WatchlistService interface {
	List(userID string) ([]models.WatchlistItem, error)
}

// HistoryService provides access to a user's continue-watching state.
type HistoryService interface {
	ListContinueWatching(userID string) ([]models.SeriesWatchState, error)
}

// UserSettingsService provides access to per-user settings.
type UserSettingsService interface {
	Get(userID string) (*models.UserSettings, error)
}

// UsersService lists all user profiles.
type UsersService interface {
	List() []models.User
}

type userCalendar struct {
	Items       []models.CalendarItem
	RefreshedAt time.Time
}

type buildState struct {
	mu            sync.Mutex
	seen          map[string]bool
	fetchedSeries map[string]bool
}

func newBuildState() *buildState {
	return &buildState{
		seen:          make(map[string]bool),
		fetchedSeries: make(map[string]bool),
	}
}

func (s *buildState) claimSeries(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetchedSeries[key] {
		return false
	}
	s.fetchedSeries[key] = true
	return true
}

func (s *buildState) claimItem(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[key] {
		return false
	}
	s.seen[key] = true
	return true
}

const RecentDaysWindow = 7

// Status holds the current state of the calendar background worker.
type Status struct {
	Running         bool      `json:"running"`
	State           string    `json:"state"` // "idle", "refreshing", "stopped"
	LastRefreshAt   time.Time `json:"lastRefreshAt"`
	LastRefreshMs   int64     `json:"lastRefreshMs"`
	NextRefreshAt   time.Time `json:"nextRefreshAt"`
	RefreshInterval string    `json:"refreshInterval"`
	UsersTracked    int       `json:"usersTracked"`
	TotalItems      int       `json:"totalItems"`
	LastError       string    `json:"lastError,omitempty"`
}

// Service manages pre-populated calendar data for all users.
type Service struct {
	mu              sync.RWMutex
	cache           map[string]*userCalendar
	building        map[string]chan struct{}
	metadata        MetadataService
	watchlist       WatchlistService
	history         HistoryService
	userSettings    UserSettingsService
	users           UsersService
	stopCh          chan struct{}
	maxDays         int // how far ahead to look (default 90)
	refreshInterval time.Duration

	// Status tracking
	statusMu      sync.RWMutex
	running       bool
	state         string // "idle", "refreshing", "stopped"
	lastRefreshAt time.Time
	lastRefreshMs int64
	nextRefreshAt time.Time
	lastError     string
	refreshNow    chan struct{} // trigger immediate refresh
}

// New creates a new calendar service.
func New(
	metadata MetadataService,
	watchlist WatchlistService,
	history HistoryService,
	userSettings UserSettingsService,
	users UsersService,
) *Service {
	return &Service{
		cache:        make(map[string]*userCalendar),
		building:     make(map[string]chan struct{}),
		metadata:     metadata,
		watchlist:    watchlist,
		history:      history,
		userSettings: userSettings,
		users:        users,
		maxDays:      90,
	}
}

// StartBackgroundRefresh begins async population on startup and periodic refresh.
func (s *Service) StartBackgroundRefresh(interval time.Duration) {
	s.refreshInterval = interval
	s.stopCh = make(chan struct{})
	s.refreshNow = make(chan struct{}, 1)

	s.statusMu.Lock()
	s.running = true
	s.state = "idle"
	s.statusMu.Unlock()

	go func() {
		log.Println("[calendar] background refresh: initial population starting...")
		s.doRefresh()
		log.Println("[calendar] background refresh: initial population complete")

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			s.statusMu.Lock()
			s.nextRefreshAt = time.Now().Add(interval)
			s.statusMu.Unlock()

			select {
			case <-ticker.C:
				log.Println("[calendar] background refresh: periodic refresh starting...")
				s.doRefresh()
				log.Println("[calendar] background refresh: periodic refresh complete")
			case <-s.refreshNow:
				log.Println("[calendar] background refresh: manual refresh triggered...")
				s.doRefresh()
				log.Println("[calendar] background refresh: manual refresh complete")
				// Reset ticker so next auto-refresh is a full interval away
				ticker.Reset(interval)
			case <-s.stopCh:
				log.Println("[calendar] background refresh: stopped")
				s.statusMu.Lock()
				s.running = false
				s.state = "stopped"
				s.statusMu.Unlock()
				return
			}
		}
	}()
}

// doRefresh runs a full refresh with status tracking.
func (s *Service) doRefresh() {
	s.statusMu.Lock()
	s.state = "refreshing"
	s.statusMu.Unlock()

	start := time.Now()
	s.refreshAll()
	elapsed := time.Since(start)

	s.statusMu.Lock()
	s.state = "idle"
	s.lastRefreshAt = time.Now()
	s.lastRefreshMs = elapsed.Milliseconds()
	s.statusMu.Unlock()
}

// Refresh triggers an immediate calendar refresh. Non-blocking.
func (s *Service) Refresh() {
	select {
	case s.refreshNow <- struct{}{}:
	default:
		// Already a refresh pending
	}
}

// GetStatus returns the current status of the calendar worker.
func (s *Service) GetStatus() Status {
	s.statusMu.RLock()
	running := s.running
	state := s.state
	lastRefreshAt := s.lastRefreshAt
	lastRefreshMs := s.lastRefreshMs
	nextRefreshAt := s.nextRefreshAt
	lastError := s.lastError
	s.statusMu.RUnlock()

	// Count users and items
	s.mu.RLock()
	usersTracked := len(s.cache)
	totalItems := 0
	for _, uc := range s.cache {
		totalItems += len(uc.Items)
	}
	s.mu.RUnlock()

	intervalStr := ""
	if s.refreshInterval > 0 {
		if s.refreshInterval >= time.Hour {
			intervalStr = fmt.Sprintf("%.0fh", s.refreshInterval.Hours())
		} else {
			intervalStr = fmt.Sprintf("%.0fm", s.refreshInterval.Minutes())
		}
	}

	return Status{
		Running:         running,
		State:           state,
		LastRefreshAt:   lastRefreshAt,
		LastRefreshMs:   lastRefreshMs,
		NextRefreshAt:   nextRefreshAt,
		RefreshInterval: intervalStr,
		UsersTracked:    usersTracked,
		TotalItems:      totalItems,
		LastError:       lastError,
	}
}

// Stop gracefully stops the background refresh.
func (s *Service) Stop() {
	if s.stopCh != nil {
		close(s.stopCh)
	}
}

// Get returns the cached calendar for a user. If the cache is empty for the
// given user (e.g. profile created after the last refresh), it builds the
// calendar on-demand so new profiles don't have to wait up to 4 hours.
func (s *Service) Get(userID string) *userCalendar {
	return s.buildAndCacheUserCalendar(userID, false)
}

// refreshAll rebuilds calendar data for all users.
func (s *Service) refreshAll() {
	allUsers := s.users.List()
	for _, u := range allUsers {
		s.buildAndCacheUserCalendar(u.ID, true)
	}
}

func (s *Service) buildAndCacheUserCalendar(userID string, force bool) *userCalendar {
	for {
		s.mu.RLock()
		cached := s.cache[userID]
		waitCh := s.building[userID]
		s.mu.RUnlock()

		if cached != nil && !force {
			return cached
		}
		if waitCh != nil {
			<-waitCh
			continue
		}

		s.mu.Lock()
		cached = s.cache[userID]
		waitCh = s.building[userID]
		if cached != nil && !force {
			s.mu.Unlock()
			return cached
		}
		if waitCh == nil {
			waitCh = make(chan struct{})
			s.building[userID] = waitCh
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
	}

	start := time.Now()
	log.Printf("[calendar] build start user=%s force=%t", userID, force)
	items := s.buildUserCalendar(userID)
	uc := &userCalendar{
		Items:       items,
		RefreshedAt: time.Now().UTC(),
	}

	s.mu.Lock()
	s.cache[userID] = uc
	completeCh := s.building[userID]
	delete(s.building, userID)
	s.mu.Unlock()

	if completeCh != nil {
		close(completeCh)
	}
	log.Printf("[calendar] build complete user=%s items=%d elapsed=%s", userID, len(items), time.Since(start).Round(time.Millisecond))

	return uc
}

// calendarSourcesEnabled returns which sources are enabled for a user.
func (s *Service) calendarSourcesEnabled(userID string) models.CalendarSettings {
	defaults := models.CalendarSettings{
		Watchlist:   models.BoolPtr(true),
		History:     models.BoolPtr(true),
		Trending:    models.BoolPtr(true),
		TopTrending: models.BoolPtr(true),
		MDBLists:    models.BoolPtr(true),
	}

	settings, err := s.userSettings.Get(userID)
	if err != nil || settings == nil {
		return defaults
	}

	cal := settings.Calendar
	if cal.Watchlist == nil {
		cal.Watchlist = defaults.Watchlist
	}
	if cal.History == nil {
		cal.History = defaults.History
	}
	if cal.Trending == nil {
		cal.Trending = defaults.Trending
	}
	if cal.TopTrending == nil {
		cal.TopTrending = defaults.TopTrending
	}
	if cal.MDBLists == nil {
		cal.MDBLists = defaults.MDBLists
	}
	// MDBListShelves: nil means all enabled (handled by MDBListShelfEnabled)
	return cal
}

// buildUserCalendar collects upcoming content from all enabled sources for a single user.
func (s *Service) buildUserCalendar(userID string) []models.CalendarItem {
	ctx := context.Background()
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := todayStart.AddDate(0, 0, -RecentDaysWindow)
	cutoff := todayStart.AddDate(0, 0, s.maxDays)
	state := newBuildState()
	sources := s.calendarSourcesEnabled(userID)
	watchlistItems, continueWatchingItems, settings := s.loadBuildInputs(userID, sources)
	trendingMovies, trendingSeries := s.loadTrendingInputs(ctx, sources)
	var items []models.CalendarItem

	// 1. Series + movies from watchlist
	if models.BoolVal(sources.Watchlist, true) {
		wlSeries := s.collectSeriesFromWatchlist(ctx, watchlistItems, windowStart, cutoff, state)
		items = append(items, wlSeries...)
		wlMovies := s.collectMoviesFromWatchlist(ctx, watchlistItems, windowStart, cutoff, state)
		items = append(items, wlMovies...)
	}

	// 2. Series from continue-watching (history)
	if models.BoolVal(sources.History, true) {
		cwItems := s.collectFromHistory(ctx, continueWatchingItems, userID, windowStart, cutoff, state)
		items = append(items, cwItems...)
	}

	// 3. Top 20 content from trending lists
	if models.BoolVal(sources.TopTrending, true) {
		trendingItems := s.collectFromTrending(ctx, trendingMovies, trendingSeries, windowStart, cutoff, state, 0, 20, "top-trending")
		items = append(items, trendingItems...)
	}

	// 4. Remaining content from trending lists
	if models.BoolVal(sources.Trending, true) {
		trendingItems := s.collectFromTrending(ctx, trendingMovies, trendingSeries, windowStart, cutoff, state, 20, 0, "trending")
		items = append(items, trendingItems...)
	}

	// 5. Content from MDBList custom lists (global + per-shelf filtering)
	if models.BoolVal(sources.MDBLists, true) {
		mdbItems := s.collectFromMDBLists(ctx, settings, sources, windowStart, cutoff, state)
		items = append(items, mdbItems...)
	}

	// Sort by full UTC datetime (not raw date string) so shows crossing
	// date boundaries due to timezone conversion sort correctly.
	sort.Slice(items, func(i, j int) bool {
		ti := ParseAirDateTime(items[i].AirDate, items[i].AirTime, items[i].AirTimezone)
		tj := ParseAirDateTime(items[j].AirDate, items[j].AirTime, items[j].AirTimezone)
		return ti.Before(tj)
	})

	return items
}

func (s *Service) loadBuildInputs(userID string, sources models.CalendarSettings) ([]models.WatchlistItem, []models.SeriesWatchState, *models.UserSettings) {
	var watchlistItems []models.WatchlistItem
	if models.BoolVal(sources.Watchlist, true) {
		if items, err := s.watchlist.List(userID); err != nil {
			log.Printf("[calendar] watchlist list error user=%s: %v", userID, err)
		} else {
			watchlistItems = items
		}
	}

	var continueWatchingItems []models.SeriesWatchState
	if models.BoolVal(sources.History, true) {
		if items, err := s.history.ListContinueWatching(userID); err != nil {
			log.Printf("[calendar] continue watching error user=%s: %v", userID, err)
		} else {
			continueWatchingItems = items
		}
	}

	settings, err := s.userSettings.Get(userID)
	if err != nil {
		settings = nil
	}

	return watchlistItems, continueWatchingItems, settings
}

func (s *Service) loadTrendingInputs(ctx context.Context, sources models.CalendarSettings) ([]models.TrendingItem, []models.TrendingItem) {
	if !models.BoolVal(sources.TopTrending, true) && !models.BoolVal(sources.Trending, true) {
		return nil, nil
	}

	var (
		trendingMovies []models.TrendingItem
		trendingSeries []models.TrendingItem
		wg             sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		items, err := s.metadata.Trending(ctx, "movie")
		if err != nil {
			log.Printf("[calendar] trending movies error: %v", err)
			return
		}
		trendingMovies = items
	}()
	go func() {
		defer wg.Done()
		items, err := s.metadata.Trending(ctx, "series")
		if err != nil {
			log.Printf("[calendar] trending series error: %v", err)
			return
		}
		trendingSeries = items
	}()
	wg.Wait()

	return trendingMovies, trendingSeries
}

func (s *Service) parallelCollect(workers int, total int, fn func(int) []models.CalendarItem) []models.CalendarItem {
	if total == 0 {
		return nil
	}
	if workers <= 0 {
		workers = 1
	}
	type result struct {
		index int
		items []models.CalendarItem
	}
	results := make([][]models.CalendarItem, total)
	sem := make(chan struct{}, workers)
	out := make(chan result, total)
	var wg sync.WaitGroup

	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			items := fn(idx)
			<-sem
			out <- result{index: idx, items: items}
		}(i)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	for res := range out {
		results[res.index] = res.items
	}

	var merged []models.CalendarItem
	for _, items := range results {
		merged = append(merged, items...)
	}
	return merged
}

// collectSeriesFromWatchlist fetches upcoming episodes for series on the user's watchlist.
func (s *Service) collectSeriesFromWatchlist(ctx context.Context, wlItems []models.WatchlistItem, windowStart, cutoff time.Time, state *buildState) []models.CalendarItem {
	var seriesItems []models.WatchlistItem
	for _, wl := range wlItems {
		if wl.MediaType == "series" {
			seriesItems = append(seriesItems, wl)
		}
	}

	return s.parallelCollect(calendarMetadataConcurrency, len(seriesItems), func(idx int) []models.CalendarItem {
		wl := seriesItems[idx]
		return s.fetchUpcomingEpisodes(ctx, wl.Name, wl.Year, wl.ExternalIDs, wl.PosterURL, "watchlist", windowStart, cutoff, state)
	})
}

// collectFromHistory fetches upcoming episodes for series the user is currently watching.
func (s *Service) collectFromHistory(ctx context.Context, cwItems []models.SeriesWatchState, userID string, windowStart, cutoff time.Time, state *buildState) []models.CalendarItem {
	skippedMovieResumes := 0
	var seriesItems []models.SeriesWatchState
	for _, cw := range cwItems {
		// Continue watching now includes movie resume entries, which are represented
		// as SeriesWatchState objects. Calendar only cares about episodic follow-up
		// content, so skip entries that clearly point at movies before hitting TVDB/TMDB.
		if isMovieHistoryEntry(cw) {
			skippedMovieResumes++
			continue
		}
		seriesItems = append(seriesItems, cw)
	}
	if skippedMovieResumes > 0 {
		log.Printf("[calendar] skipped %d movie resume entries in history for user=%s", skippedMovieResumes, userID)
	}

	return s.parallelCollect(calendarMetadataConcurrency, len(seriesItems), func(idx int) []models.CalendarItem {
		cw := seriesItems[idx]
		return s.fetchUpcomingEpisodes(ctx, cw.SeriesTitle, cw.Year, cw.ExternalIDs, cw.PosterURL, "history", windowStart, cutoff, state)
	})
}

func isMovieHistoryEntry(state models.SeriesWatchState) bool {
	if strings.Contains(state.SeriesID, ":movie:") {
		return true
	}
	if state.ExternalIDs != nil {
		if titleID := strings.TrimSpace(state.ExternalIDs["titleId"]); strings.Contains(titleID, ":movie:") {
			return true
		}
	}
	return false
}

// collectMoviesFromWatchlist checks movies on the watchlist for upcoming release dates.
// Adds separate calendar items for each unreleased release type (theatrical, digital, physical, etc.).
func (s *Service) collectMoviesFromWatchlist(ctx context.Context, wlItems []models.WatchlistItem, windowStart, cutoff time.Time, state *buildState) []models.CalendarItem {
	var movieItems []models.WatchlistItem
	for _, wl := range wlItems {
		if wl.MediaType == "movie" {
			movieItems = append(movieItems, wl)
		}
	}

	return s.parallelCollect(calendarMetadataConcurrency, len(movieItems), func(idx int) []models.CalendarItem {
		wl := movieItems[idx]
		query := buildMovieQuery(wl.Name, wl.Year, wl.ExternalIDs)
		details, err := s.metadata.MovieDetails(ctx, query)
		if err != nil || details == nil {
			return nil
		}
		return collectMovieReleases(details, "watchlist", windowStart, cutoff, state)
	})
}

// collectFromTrending fetches upcoming content from trending movies and series.
// offset/limit are applied independently to the movie and series lists.
func (s *Service) collectFromTrending(
	ctx context.Context,
	trendingMovies, trendingSeries []models.TrendingItem,
	windowStart, cutoff time.Time,
	state *buildState,
	offset, limit int,
	source string,
) []models.CalendarItem {
	var items []models.CalendarItem

	// Trending movies — already have release date data from TMDB enrichment
	if trendingMovies != nil {
		selectedMovies := sliceTrendingItems(trendingMovies, offset, limit)
		for i := range selectedMovies {
			items = append(items, collectMovieReleases(&selectedMovies[i].Title, source, windowStart, cutoff, state)...)
		}
	}

	// Trending series — fetch details to get upcoming episode air dates
	// Only process series with "Continuing" or "Upcoming" status to avoid
	// fetching details for ended series that won't have future episodes.
	if trendingSeries != nil {
		selectedSeries := sliceTrendingItems(trendingSeries, offset, limit)
		var eligible []models.TrendingItem
		for _, ts := range selectedSeries {
			status := strings.ToLower(ts.Title.Status)
			if status != "continuing" && status != "upcoming" {
				continue
			}
			eligible = append(eligible, ts)
		}
		items = append(items, s.parallelCollect(calendarMetadataConcurrency, len(eligible), func(idx int) []models.CalendarItem {
			ts := eligible[idx]
			posterURL := ""
			if ts.Title.Poster != nil {
				posterURL = ts.Title.Poster.URL
			}
			extIDs := buildExternalIDs(ts.Title.IMDBID, ts.Title.TMDBID, ts.Title.TVDBID)
			return s.fetchUpcomingEpisodes(ctx, ts.Title.Name, ts.Title.Year, extIDs, posterURL, source, windowStart, cutoff, state)
		})...)
	}

	return items
}

// collectFromMDBLists fetches upcoming content from the user's custom MDBList shelves.
// Each shelf is individually controlled via calSources.MDBListShelves (nil = all enabled).
func (s *Service) collectFromMDBLists(
	ctx context.Context,
	settings *models.UserSettings,
	calSources models.CalendarSettings,
	windowStart, cutoff time.Time,
	state *buildState,
) []models.CalendarItem {
	if settings == nil {
		return nil
	}

	var items []models.CalendarItem
	for _, shelf := range settings.HomeShelves.Shelves {
		if shelf.Type != "mdblist" || !shelf.Enabled || shelf.ListURL == "" {
			continue
		}
		// Check per-shelf calendar setting
		if !calSources.MDBListShelfEnabled(shelf.ID) {
			continue
		}

		limit := shelf.Limit
		if limit <= 0 {
			limit = 20
		}

		listItems, err := s.metadata.GetCustomListForCalendar(ctx, shelf.ListURL, limit, shelf.Name)
		if err != nil {
			log.Printf("[calendar] mdblist error shelf=%s url=%s: %v", shelf.ID, shelf.ListURL, err)
			continue
		}
		items = append(items, s.parallelCollect(calendarMetadataConcurrency, len(listItems), func(idx int) []models.CalendarItem {
			item := listItems[idx]
			if item.Title.MediaType == "movie" {
				return collectMovieReleases(&item.Title, "mdblist", windowStart, cutoff, state)
			}

			status := strings.ToLower(item.Title.Status)
			if status != "" && status != "continuing" && status != "upcoming" {
				return nil
			}

			posterURL := ""
			if item.Title.Poster != nil {
				posterURL = item.Title.Poster.URL
			}
			extIDs := buildExternalIDs(item.Title.IMDBID, item.Title.TMDBID, item.Title.TVDBID)
			return s.fetchUpcomingEpisodes(
				ctx,
				item.Title.Name,
				item.Title.Year,
				extIDs,
				posterURL,
				"mdblist",
				windowStart,
				cutoff,
				state,
			)
		})...)
	}
	return items
}

func sliceTrendingItems(items []models.TrendingItem, offset, limit int) []models.TrendingItem {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}

	sliced := items[offset:]
	if limit > 0 && limit < len(sliced) {
		sliced = sliced[:limit]
	}
	return sliced
}

// collectMovieReleases extracts calendar items from a movie's release dates.
// For each release type (theatrical, digital, etc.), only the earliest upcoming
// date is used so the calendar isn't cluttered with duplicate regional releases.
func collectMovieReleases(title *models.Title, source string, windowStart, cutoff time.Time, state *buildState) []models.CalendarItem {
	if title == nil {
		return nil
	}

	posterURL := ""
	if title.Poster != nil {
		posterURL = title.Poster.URL
	}
	extIDs := buildExternalIDs(title.IMDBID, title.TMDBID, title.TVDBID)

	// Collect all candidate releases: curated pointers + full list.
	var candidates []models.Release
	if title.Theatrical != nil {
		candidates = append(candidates, *title.Theatrical)
	}
	if title.HomeRelease != nil {
		candidates = append(candidates, *title.HomeRelease)
	}
	candidates = append(candidates, title.Releases...)

	// Find types that already have a released entry — the movie is already
	// out in that format so remaining regional dates are just noise.
	releasedTypes := make(map[string]bool)
	for i := range candidates {
		if candidates[i].Released {
			releasedTypes[candidates[i].Type] = true
		}
	}

	// Pick the earliest unreleased date per type, skipping types already released.
	earliestByType := make(map[string]*models.Release)
	for i := range candidates {
		rel := &candidates[i]
		if rel.Released || releasedTypes[rel.Type] {
			continue
		}
		d, err := parseDate(rel.Date)
		if err != nil || d.Before(windowStart) || d.After(cutoff) {
			continue
		}
		existing := earliestByType[rel.Type]
		if existing == nil {
			earliestByType[rel.Type] = rel
		} else {
			existingDate, _ := parseDate(existing.Date)
			if d.Before(existingDate) {
				earliestByType[rel.Type] = rel
			}
		}
	}

	var items []models.CalendarItem
	for _, rel := range earliestByType {
		if item, ok := makeMovieReleaseItem(title, rel, posterURL, extIDs, source, windowStart, cutoff, state); ok {
			items = append(items, item)
		}
	}

	return items
}

// makeMovieReleaseItem creates a CalendarItem from a movie release, if it's in the date window.
func makeMovieReleaseItem(title *models.Title, rel *models.Release, posterURL string, extIDs map[string]string, source string, windowStart, cutoff time.Time, state *buildState) (models.CalendarItem, bool) {
	releaseDate, err := parseDate(rel.Date)
	if err != nil || releaseDate.Before(windowStart) || releaseDate.After(cutoff) {
		return models.CalendarItem{}, false
	}
	key := fmt.Sprintf("movie:%d:%s", title.TMDBID, rel.Type)
	if !state.claimItem(key) {
		return models.CalendarItem{}, false
	}

	return models.CalendarItem{
		Title:       title.Name,
		MediaType:   "movie",
		AirDate:     releaseDate.Format("2006-01-02"),
		ReleaseType: rel.Type,
		PosterURL:   posterURL,
		Year:        title.Year,
		ExternalIDs: extIDs,
		Source:      source,
	}, true
}

// fetchUpcomingEpisodes fetches series details and returns calendar items for future episodes.
func (s *Service) fetchUpcomingEpisodes(
	ctx context.Context,
	seriesName string,
	year int,
	externalIDs map[string]string,
	posterURL string,
	source string,
	windowStart, cutoff time.Time,
	state *buildState,
) []models.CalendarItem {
	seriesKey := buildSeriesFetchKey(seriesName, year, externalIDs)
	if !state.claimSeries(seriesKey) {
		return nil
	}

	query := buildSeriesQuery(seriesName, year, externalIDs)
	details, err := s.metadata.SeriesDetailsLite(ctx, query)
	if err != nil || details == nil {
		return nil
	}

	// Use poster from metadata if not provided
	if posterURL == "" && details.Title.Poster != nil {
		posterURL = details.Title.Poster.URL
	}

	extIDs := buildExternalIDs(details.Title.IMDBID, details.Title.TMDBID, details.Title.TVDBID)

	// Build the actual air datetime using the series' air time and timezone.
	// Without this, episodes are compared as midnight UTC which causes items
	// airing later in the day (e.g. 9pm EST) to be incorrectly filtered as
	// "already aired" when it's still earlier that day in UTC.
	airsTime := details.Title.AirsTime
	airsTimezone := details.Title.AirsTimezone

	var items []models.CalendarItem
	for _, season := range details.Seasons {
		if season.Number == 0 {
			continue // skip specials
		}
		for _, ep := range season.Episodes {
			if ep.AiredDate == "" {
				continue
			}
			airDateTime := ParseAirDateTime(ep.AiredDate, airsTime, airsTimezone)
			if airDateTime.Before(windowStart) || airDateTime.After(cutoff) {
				continue
			}

			key := fmt.Sprintf("series:%d:s%02de%02d", details.Title.TVDBID, ep.SeasonNumber, ep.EpisodeNumber)
			if !state.claimItem(key) {
				continue
			}

			items = append(items, models.CalendarItem{
				Title:           details.Title.Name,
				EpisodeTitle:    ep.Name,
				EpisodeOverview: strings.TrimSpace(ep.Overview),
				MediaType:       "series",
				SeasonNumber:    ep.SeasonNumber,
				EpisodeNumber:   ep.EpisodeNumber,
				AirDate:         ep.AiredDate,
				AirTime:         airsTime,
				AirTimezone:     airsTimezone,
				Network:         details.Title.Network,
				PosterURL:       posterURL,
				Year:            details.Title.Year,
				ExternalIDs:     extIDs,
				Source:          source,
			})
		}
	}
	return items
}

func buildSeriesFetchKey(name string, year int, externalIDs map[string]string) string {
	if externalIDs != nil {
		if tvdb := strings.TrimSpace(externalIDs["tvdb"]); tvdb != "" {
			return "tvdb:" + tvdb
		}
		if tmdb := strings.TrimSpace(externalIDs["tmdb"]); tmdb != "" {
			return "tmdb:" + tmdb
		}
		if imdb := strings.TrimSpace(externalIDs["imdb"]); imdb != "" {
			return "imdb:" + imdb
		}
	}

	normalizedName := strings.ToLower(strings.TrimSpace(name))
	return fmt.Sprintf("name:%s:%d", normalizedName, year)
}

func buildSeriesQuery(name string, year int, externalIDs map[string]string) models.SeriesDetailsQuery {
	query := models.SeriesDetailsQuery{
		Name: name,
		Year: year,
	}
	if externalIDs != nil {
		if tvdbStr, ok := externalIDs["tvdb"]; ok {
			if id, err := strconv.ParseInt(tvdbStr, 10, 64); err == nil {
				query.TVDBID = id
			}
		}
		if tmdbStr, ok := externalIDs["tmdb"]; ok {
			if id, err := strconv.ParseInt(tmdbStr, 10, 64); err == nil {
				query.TMDBID = id
			}
		}
	}
	return query
}

func buildMovieQuery(name string, year int, externalIDs map[string]string) models.MovieDetailsQuery {
	query := models.MovieDetailsQuery{
		Name: name,
		Year: year,
	}
	if externalIDs != nil {
		if imdbID, ok := externalIDs["imdb"]; ok {
			query.IMDBID = imdbID
		}
		if tmdbStr, ok := externalIDs["tmdb"]; ok {
			if id, err := strconv.ParseInt(tmdbStr, 10, 64); err == nil {
				query.TMDBID = id
			}
		}
		if tvdbStr, ok := externalIDs["tvdb"]; ok {
			if id, err := strconv.ParseInt(tvdbStr, 10, 64); err == nil {
				query.TVDBID = id
			}
		}
	}
	return query
}

func buildExternalIDs(imdbID string, tmdbID, tvdbID int64) map[string]string {
	ids := make(map[string]string)
	if imdbID != "" {
		ids["imdb"] = imdbID
	}
	if tmdbID > 0 {
		ids["tmdb"] = strconv.FormatInt(tmdbID, 10)
	}
	if tvdbID > 0 {
		ids["tvdb"] = strconv.FormatInt(tvdbID, 10)
	}
	return ids
}

// ParseAirDateTime combines a date string with an optional air time and timezone
// to produce an accurate UTC datetime for comparison. If air time or timezone is
// missing, falls back to end-of-day UTC (23:59) to avoid prematurely filtering
// items that haven't actually aired yet.
func ParseAirDateTime(dateStr, airsTime, airsTimezone string) time.Time {
	airDate, err := time.Parse("2006-01-02", strings.TrimSpace(dateStr))
	if err != nil {
		return time.Time{}
	}

	if airsTime != "" && airsTimezone != "" {
		loc, err := time.LoadLocation(airsTimezone)
		if err == nil {
			parts := strings.SplitN(airsTime, ":", 2)
			if len(parts) == 2 {
				hour, e1 := strconv.Atoi(parts[0])
				minute, e2 := strconv.Atoi(parts[1])
				if e1 == nil && e2 == nil {
					localDT := time.Date(airDate.Year(), airDate.Month(), airDate.Day(), hour, minute, 0, 0, loc)
					return localDT.UTC()
				}
			}
		}
	}

	// Fallback: use end-of-day UTC so we don't prematurely filter out episodes
	// that air in the evening of their listed date.
	return time.Date(airDate.Year(), airDate.Month(), airDate.Day(), 23, 59, 59, 0, time.UTC)
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unparseable date: %s", s)
}

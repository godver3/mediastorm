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

// MetadataService provides series and movie metadata.
type MetadataService interface {
	SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error)
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	Trending(ctx context.Context, mediaType string) ([]models.TrendingItem, error)
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

// Status holds the current state of the calendar background worker.
type Status struct {
	Running         bool      `json:"running"`
	State           string    `json:"state"`           // "idle", "refreshing", "stopped"
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

// Get returns the cached calendar for a user. Returns nil if not yet populated.
func (s *Service) Get(userID string) *userCalendar {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache[userID]
}

// refreshAll rebuilds calendar data for all users.
func (s *Service) refreshAll() {
	allUsers := s.users.List()
	for _, u := range allUsers {
		items := s.buildUserCalendar(u.ID)
		s.mu.Lock()
		s.cache[u.ID] = &userCalendar{
			Items:       items,
			RefreshedAt: time.Now().UTC(),
		}
		s.mu.Unlock()
	}
}

// calendarSourcesEnabled returns which sources are enabled for a user.
func (s *Service) calendarSourcesEnabled(userID string) models.CalendarSettings {
	defaults := models.CalendarSettings{
		Watchlist: models.BoolPtr(true),
		History:   models.BoolPtr(true),
		Trending:  models.BoolPtr(true),
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
	// MDBListShelves: nil means all enabled (handled by MDBListShelfEnabled)
	return cal
}

// buildUserCalendar collects upcoming content from all enabled sources for a single user.
func (s *Service) buildUserCalendar(userID string) []models.CalendarItem {
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, s.maxDays)
	seen := make(map[string]bool) // dedup key

	sources := s.calendarSourcesEnabled(userID)
	var items []models.CalendarItem

	// 1. Series + movies from watchlist
	if models.BoolVal(sources.Watchlist, true) {
		wlSeries := s.collectSeriesFromWatchlist(ctx, userID, now, cutoff, seen)
		items = append(items, wlSeries...)
		wlMovies := s.collectMoviesFromWatchlist(ctx, userID, now, cutoff, seen)
		items = append(items, wlMovies...)
	}

	// 2. Series from continue-watching (history)
	if models.BoolVal(sources.History, true) {
		cwItems := s.collectFromHistory(ctx, userID, now, cutoff, seen)
		items = append(items, cwItems...)
	}

	// 3. Content from trending lists
	if models.BoolVal(sources.Trending, true) {
		trendingItems := s.collectFromTrending(ctx, now, cutoff, seen)
		items = append(items, trendingItems...)
	}

	// 4. Content from MDBList custom lists (per-shelf filtering)
	mdbItems := s.collectFromMDBLists(ctx, userID, sources, now, cutoff, seen)
	items = append(items, mdbItems...)

	// Sort by air date ascending
	sort.Slice(items, func(i, j int) bool {
		return items[i].AirDate < items[j].AirDate
	})

	return items
}

// collectSeriesFromWatchlist fetches upcoming episodes for series on the user's watchlist.
func (s *Service) collectSeriesFromWatchlist(ctx context.Context, userID string, now, cutoff time.Time, seen map[string]bool) []models.CalendarItem {
	wlItems, err := s.watchlist.List(userID)
	if err != nil {
		log.Printf("[calendar] watchlist list error user=%s: %v", userID, err)
		return nil
	}

	var items []models.CalendarItem
	for _, wl := range wlItems {
		if wl.MediaType != "series" {
			continue
		}
		eps := s.fetchUpcomingEpisodes(ctx, wl.Name, wl.Year, wl.ExternalIDs, wl.PosterURL, "watchlist", now, cutoff, seen)
		items = append(items, eps...)
	}
	return items
}

// collectFromHistory fetches upcoming episodes for series the user is currently watching.
func (s *Service) collectFromHistory(ctx context.Context, userID string, now, cutoff time.Time, seen map[string]bool) []models.CalendarItem {
	cwItems, err := s.history.ListContinueWatching(userID)
	if err != nil {
		log.Printf("[calendar] continue watching error user=%s: %v", userID, err)
		return nil
	}

	var items []models.CalendarItem
	for _, cw := range cwItems {
		eps := s.fetchUpcomingEpisodes(ctx, cw.SeriesTitle, cw.Year, cw.ExternalIDs, cw.PosterURL, "history", now, cutoff, seen)
		items = append(items, eps...)
	}
	return items
}

// collectMoviesFromWatchlist checks movies on the watchlist for upcoming release dates.
// Adds separate calendar items for each unreleased release type (theatrical, digital, physical, etc.).
func (s *Service) collectMoviesFromWatchlist(ctx context.Context, userID string, now, cutoff time.Time, seen map[string]bool) []models.CalendarItem {
	wlItems, err := s.watchlist.List(userID)
	if err != nil {
		return nil
	}

	var items []models.CalendarItem
	for _, wl := range wlItems {
		if wl.MediaType != "movie" {
			continue
		}
		query := buildMovieQuery(wl.Name, wl.Year, wl.ExternalIDs)
		details, err := s.metadata.MovieDetails(ctx, query)
		if err != nil || details == nil {
			continue
		}
		items = append(items, collectMovieReleases(details, "watchlist", now, cutoff, seen)...)
	}
	return items
}

// collectFromTrending fetches upcoming content from trending movies and series.
func (s *Service) collectFromTrending(ctx context.Context, now, cutoff time.Time, seen map[string]bool) []models.CalendarItem {
	var items []models.CalendarItem

	// Trending movies — already have release date data from TMDB enrichment
	trendingMovies, err := s.metadata.Trending(ctx, "movie")
	if err != nil {
		log.Printf("[calendar] trending movies error: %v", err)
	} else {
		for i := range trendingMovies {
			items = append(items, collectMovieReleases(&trendingMovies[i].Title, "trending", now, cutoff, seen)...)
		}
	}

	// Trending series — fetch details to get upcoming episode air dates
	// Only process series with "Continuing" or "Upcoming" status to avoid
	// fetching details for ended series that won't have future episodes.
	trendingSeries, err := s.metadata.Trending(ctx, "series")
	if err != nil {
		log.Printf("[calendar] trending series error: %v", err)
	} else {
		for _, ts := range trendingSeries {
			status := strings.ToLower(ts.Title.Status)
			if status != "continuing" && status != "upcoming" {
				continue
			}
			posterURL := ""
			if ts.Title.Poster != nil {
				posterURL = ts.Title.Poster.URL
			}
			extIDs := buildExternalIDs(ts.Title.IMDBID, ts.Title.TMDBID, ts.Title.TVDBID)
			eps := s.fetchUpcomingEpisodes(ctx, ts.Title.Name, ts.Title.Year, extIDs, posterURL, "trending", now, cutoff, seen)
			items = append(items, eps...)
		}
	}

	return items
}

// collectFromMDBLists fetches upcoming content from the user's custom MDBList shelves.
// Each shelf is individually controlled via calSources.MDBListShelves (nil = all enabled).
func (s *Service) collectFromMDBLists(ctx context.Context, userID string, calSources models.CalendarSettings, now, cutoff time.Time, seen map[string]bool) []models.CalendarItem {
	settings, err := s.userSettings.Get(userID)
	if err != nil || settings == nil {
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
		// MDBList items don't include release dates in the raw API response.
		// Fetching + enriching every item from every list is expensive.
		// The watchlist, history, and trending sources already cover most use cases.
		// This is a placeholder for future MDBList-specific calendar support.
	}
	return items
}

// collectMovieReleases extracts calendar items from a movie's release dates.
// Creates separate entries for theatrical/premiere and home (digital/physical) releases.
func collectMovieReleases(title *models.Title, source string, now, cutoff time.Time, seen map[string]bool) []models.CalendarItem {
	if title == nil {
		return nil
	}

	posterURL := ""
	if title.Poster != nil {
		posterURL = title.Poster.URL
	}
	extIDs := buildExternalIDs(title.IMDBID, title.TMDBID, title.TVDBID)

	var items []models.CalendarItem

	// Check the curated pointers first (Theatrical and HomeRelease) for the primary releases.
	// Then fall back to scanning all releases for any other upcoming dates.
	addedTypes := make(map[string]bool)

	if title.Theatrical != nil && !title.Theatrical.Released {
		if item, ok := makeMovieReleaseItem(title, title.Theatrical, posterURL, extIDs, source, now, cutoff, seen); ok {
			items = append(items, item)
			addedTypes[title.Theatrical.Type] = true
		}
	}
	if title.HomeRelease != nil && !title.HomeRelease.Released {
		if item, ok := makeMovieReleaseItem(title, title.HomeRelease, posterURL, extIDs, source, now, cutoff, seen); ok {
			items = append(items, item)
			addedTypes[title.HomeRelease.Type] = true
		}
	}

	// Scan remaining releases for any not covered by the pointers
	for i := range title.Releases {
		rel := &title.Releases[i]
		if rel.Released || addedTypes[rel.Type] {
			continue
		}
		if item, ok := makeMovieReleaseItem(title, rel, posterURL, extIDs, source, now, cutoff, seen); ok {
			items = append(items, item)
			addedTypes[rel.Type] = true
		}
	}

	return items
}

// makeMovieReleaseItem creates a CalendarItem from a movie release, if it's in the date window.
func makeMovieReleaseItem(title *models.Title, rel *models.Release, posterURL string, extIDs map[string]string, source string, now, cutoff time.Time, seen map[string]bool) (models.CalendarItem, bool) {
	releaseDate, err := parseDate(rel.Date)
	if err != nil || releaseDate.Before(now) || releaseDate.After(cutoff) {
		return models.CalendarItem{}, false
	}
	key := fmt.Sprintf("movie:%d:%s", title.TMDBID, rel.Type)
	if seen[key] {
		return models.CalendarItem{}, false
	}
	seen[key] = true

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
	now, cutoff time.Time,
	seen map[string]bool,
) []models.CalendarItem {
	query := buildSeriesQuery(seriesName, year, externalIDs)
	details, err := s.metadata.SeriesDetails(ctx, query)
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
			airDateTime := parseAirDateTime(ep.AiredDate, airsTime, airsTimezone)
			if airDateTime.Before(now) || airDateTime.After(cutoff) {
				continue
			}

			key := fmt.Sprintf("series:%d:s%02de%02d", details.Title.TVDBID, ep.SeasonNumber, ep.EpisodeNumber)
			if seen[key] {
				continue
			}
			seen[key] = true

			items = append(items, models.CalendarItem{
				Title:         details.Title.Name,
				EpisodeTitle:  ep.Name,
				MediaType:     "series",
				SeasonNumber:  ep.SeasonNumber,
				EpisodeNumber: ep.EpisodeNumber,
				AirDate:       ep.AiredDate,
				AirTime:       airsTime,
				AirTimezone:   airsTimezone,
				Network:       details.Title.Network,
				PosterURL:     posterURL,
				Year:          details.Title.Year,
				ExternalIDs:   extIDs,
				Source:        source,
			})
		}
	}
	return items
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

// parseAirDateTime combines a date string with an optional air time and timezone
// to produce an accurate UTC datetime for comparison. If air time or timezone is
// missing, falls back to end-of-day UTC (23:59) to avoid prematurely filtering
// items that haven't actually aired yet.
func parseAirDateTime(dateStr, airsTime, airsTimezone string) time.Time {
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

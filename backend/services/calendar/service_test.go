package calendar

import (
	"context"
	"fmt"
	"novastream/models"
	"testing"
	"time"
)

// --- Mock services ---

type mockMetadata struct {
	series   map[int64]*models.SeriesDetails
	movies   map[int64]*models.Title
	trending map[string][]models.TrendingItem
}

func (m *mockMetadata) SeriesDetails(_ context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	if d, ok := m.series[req.TVDBID]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("series not found")
}

func (m *mockMetadata) MovieDetails(_ context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	if d, ok := m.movies[req.TMDBID]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("movie not found")
}

func (m *mockMetadata) Trending(_ context.Context, mediaType string) ([]models.TrendingItem, error) {
	if items, ok := m.trending[mediaType]; ok {
		return items, nil
	}
	return nil, nil
}

type mockWatchlist struct {
	items map[string][]models.WatchlistItem
}

func (m *mockWatchlist) List(userID string) ([]models.WatchlistItem, error) {
	return m.items[userID], nil
}

type mockHistory struct {
	items map[string][]models.SeriesWatchState
}

func (m *mockHistory) ListContinueWatching(userID string) ([]models.SeriesWatchState, error) {
	return m.items[userID], nil
}

type mockUserSettings struct {
	settings map[string]*models.UserSettings
}

func (m *mockUserSettings) Get(userID string) (*models.UserSettings, error) {
	if s, ok := m.settings[userID]; ok {
		return s, nil
	}
	return nil, nil
}

type mockUsers struct {
	users []models.User
}

func (m *mockUsers) List() []models.User {
	return m.users
}

// --- Helper to build test data ---

func futureDate(daysFromNow int) string {
	return time.Now().AddDate(0, 0, daysFromNow).Format("2006-01-02")
}

func pastDate(daysAgo int) string {
	return time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")
}

func defaultMocks() (*mockMetadata, *mockWatchlist, *mockHistory, *mockUserSettings, *mockUsers) {
	return &mockMetadata{
			series:   map[int64]*models.SeriesDetails{},
			movies:   map[int64]*models.Title{},
			trending: map[string][]models.TrendingItem{},
		},
		&mockWatchlist{items: map[string][]models.WatchlistItem{}},
		&mockHistory{items: map[string][]models.SeriesWatchState{}},
		&mockUserSettings{settings: map[string]*models.UserSettings{}},
		&mockUsers{users: []models.User{{ID: "user1"}}}
}

// --- Tests ---

func TestBuildUserCalendar_WatchlistSeries(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{
			Name: "Test Show", TVDBID: 100, TMDBID: 200, IMDBID: "tt1234567", Year: 2024,
		},
		Seasons: []models.SeriesSeason{
			{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{Name: "Past Episode", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: pastDate(5)},
					{Name: "Future Episode", SeasonNumber: 1, EpisodeNumber: 2, AiredDate: futureDate(3)},
					{Name: "Far Future Episode", SeasonNumber: 1, EpisodeNumber: 3, AiredDate: futureDate(100)},
				},
			},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tvdb:100", MediaType: "series", Name: "Test Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items[0]
	if item.Title != "Test Show" {
		t.Errorf("expected title 'Test Show', got %q", item.Title)
	}
	if item.EpisodeTitle != "Future Episode" {
		t.Errorf("expected episode title 'Future Episode', got %q", item.EpisodeTitle)
	}
	if item.SeasonNumber != 1 || item.EpisodeNumber != 2 {
		t.Errorf("expected S01E02, got S%02dE%02d", item.SeasonNumber, item.EpisodeNumber)
	}
	if item.Source != "watchlist" {
		t.Errorf("expected source 'watchlist', got %q", item.Source)
	}
	if item.ExternalIDs["tvdb"] != "100" {
		t.Errorf("expected tvdb ID '100', got %q", item.ExternalIDs["tvdb"])
	}
}

func TestBuildUserCalendar_HistorySeries(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.series[200] = &models.SeriesDetails{
		Title: models.Title{Name: "Continue Show", TVDBID: 200},
		Seasons: []models.SeriesSeason{
			{Number: 2, Episodes: []models.SeriesEpisode{
				{Name: "Ep 1", SeasonNumber: 2, EpisodeNumber: 1, AiredDate: futureDate(7)},
			}},
		},
	}
	hist.items["user1"] = []models.SeriesWatchState{
		{SeriesID: "tvdb:200", SeriesTitle: "Continue Show", ExternalIDs: map[string]string{"tvdb": "200"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Source != "history" {
		t.Errorf("expected source 'history', got %q", items[0].Source)
	}
}

func TestBuildUserCalendar_Deduplication(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{Name: "Shared Show", TVDBID: 100},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tvdb:100", MediaType: "series", Name: "Shared Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}
	hist.items["user1"] = []models.SeriesWatchState{
		{SeriesID: "tvdb:100", SeriesTitle: "Shared Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 item after dedup, got %d", len(items))
	}
	if items[0].Source != "watchlist" {
		t.Errorf("expected source 'watchlist' (first wins), got %q", items[0].Source)
	}
}

func TestBuildUserCalendar_MovieReleaseTypes(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.movies[500] = &models.Title{
		Name: "Upcoming Movie", TMDBID: 500, Year: 2026,
		Poster: &models.Image{URL: "https://example.com/poster.jpg"},
		Theatrical: &models.Release{Type: "theatrical", Date: futureDate(14), Released: false},
		HomeRelease: &models.Release{Type: "digital", Date: futureDate(60), Released: false},
		Releases: []models.Release{
			{Type: "theatrical", Date: futureDate(14), Released: false},
			{Type: "digital", Date: futureDate(60), Released: false},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tmdb:500", MediaType: "movie", Name: "Upcoming Movie", ExternalIDs: map[string]string{"tmdb": "500"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 2 {
		t.Fatalf("expected 2 movie release items (theatrical + digital), got %d", len(items))
	}

	// Should be sorted by date: theatrical first, digital second
	if items[0].ReleaseType != "theatrical" {
		t.Errorf("expected first item releaseType 'theatrical', got %q", items[0].ReleaseType)
	}
	if items[1].ReleaseType != "digital" {
		t.Errorf("expected second item releaseType 'digital', got %q", items[1].ReleaseType)
	}
	if items[0].MediaType != "movie" || items[1].MediaType != "movie" {
		t.Error("expected both items to be movies")
	}
}

func TestBuildUserCalendar_TrendingMovies(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.trending["movie"] = []models.TrendingItem{
		{
			Rank: 1,
			Title: models.Title{
				Name: "Trending Film", TMDBID: 600, Year: 2026,
				Theatrical: &models.Release{Type: "theatrical", Date: futureDate(7), Released: false},
				Releases:   []models.Release{{Type: "theatrical", Date: futureDate(7), Released: false}},
			},
		},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 trending movie item, got %d", len(items))
	}
	if items[0].Source != "trending" {
		t.Errorf("expected source 'trending', got %q", items[0].Source)
	}
	if items[0].ReleaseType != "theatrical" {
		t.Errorf("expected releaseType 'theatrical', got %q", items[0].ReleaseType)
	}
}

func TestBuildUserCalendar_TrendingSeries(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.trending["series"] = []models.TrendingItem{
		{
			Rank: 1,
			Title: models.Title{
				Name: "Trending Show", TVDBID: 300, Status: "Continuing",
			},
		},
	}
	meta.series[300] = &models.SeriesDetails{
		Title: models.Title{Name: "Trending Show", TVDBID: 300},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "New Ep", SeasonNumber: 1, EpisodeNumber: 5, AiredDate: futureDate(2)},
			}},
		},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 trending series item, got %d", len(items))
	}
	if items[0].Source != "trending" {
		t.Errorf("expected source 'trending', got %q", items[0].Source)
	}
	if items[0].EpisodeTitle != "New Ep" {
		t.Errorf("expected episode title 'New Ep', got %q", items[0].EpisodeTitle)
	}
}

func TestBuildUserCalendar_TrendingSkipsEndedSeries(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.trending["series"] = []models.TrendingItem{
		{Rank: 1, Title: models.Title{Name: "Ended Show", TVDBID: 400, Status: "Ended"}},
	}
	meta.series[400] = &models.SeriesDetails{
		Title: models.Title{Name: "Ended Show", TVDBID: 400},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 0 {
		t.Fatalf("expected 0 items (ended series skipped), got %d", len(items))
	}
}

func TestBuildUserCalendar_SettingsDisableWatchlist(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{Name: "WL Show", TVDBID: 100},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tvdb:100", MediaType: "series", Name: "WL Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}
	// Disable watchlist source
	us.settings["user1"] = &models.UserSettings{
		Calendar: models.CalendarSettings{Watchlist: models.BoolPtr(false)},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 0 {
		t.Fatalf("expected 0 items (watchlist disabled), got %d", len(items))
	}
}

func TestBuildUserCalendar_SettingsDisableTrending(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.trending["movie"] = []models.TrendingItem{
		{
			Rank: 1,
			Title: models.Title{
				Name: "Trending Film", TMDBID: 600,
				Theatrical: &models.Release{Type: "theatrical", Date: futureDate(7), Released: false},
				Releases:   []models.Release{{Type: "theatrical", Date: futureDate(7), Released: false}},
			},
		},
	}
	// Disable trending source
	us.settings["user1"] = &models.UserSettings{
		Calendar: models.CalendarSettings{Trending: models.BoolPtr(false)},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 0 {
		t.Fatalf("expected 0 items (trending disabled), got %d", len(items))
	}
}

func TestBuildUserCalendar_SettingsDefaultsAllEnabled(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	// User has settings but calendar section is empty (all nil = defaults to true)
	us.settings["user1"] = &models.UserSettings{}

	meta.trending["movie"] = []models.TrendingItem{
		{
			Rank: 1,
			Title: models.Title{
				Name: "Film", TMDBID: 700,
				Theatrical: &models.Release{Type: "theatrical", Date: futureDate(7), Released: false},
				Releases:   []models.Release{{Type: "theatrical", Date: futureDate(7), Released: false}},
			},
		},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 item (defaults = all enabled), got %d", len(items))
	}
}

func TestBuildUserCalendar_SortedByDate(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{Name: "Show A", TVDBID: 100},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Ep Late", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(20)},
				{Name: "Ep Early", SeasonNumber: 1, EpisodeNumber: 2, AiredDate: futureDate(3)},
			}},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tvdb:100", MediaType: "series", Name: "Show A", ExternalIDs: map[string]string{"tvdb": "100"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].AirDate > items[1].AirDate {
		t.Errorf("items not sorted: %s > %s", items[0].AirDate, items[1].AirDate)
	}
}

func TestBuildUserCalendar_SkipsSpecials(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{Name: "Show", TVDBID: 100},
		Seasons: []models.SeriesSeason{
			{Number: 0, Episodes: []models.SeriesEpisode{
				{Name: "Special", SeasonNumber: 0, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Regular", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tvdb:100", MediaType: "series", Name: "Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 1 {
		t.Fatalf("expected 1 item (specials excluded), got %d", len(items))
	}
	if items[0].EpisodeTitle != "Regular" {
		t.Errorf("expected 'Regular', got %q", items[0].EpisodeTitle)
	}
}

func TestBuildUserCalendar_MDBListPerShelfDisable(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	// User has an mdblist shelf configured
	us.settings["user1"] = &models.UserSettings{
		HomeShelves: models.HomeShelvesSettings{
			Shelves: []models.ShelfConfig{
				{ID: "mdb-1", Name: "My List", Enabled: true, Type: "mdblist", ListURL: "https://mdblist.com/lists/test/list1/json"},
				{ID: "mdb-2", Name: "Other List", Enabled: true, Type: "mdblist", ListURL: "https://mdblist.com/lists/test/list2/json"},
			},
		},
		Calendar: models.CalendarSettings{
			MDBListShelves: map[string]bool{
				"mdb-1": false, // disabled
				// mdb-2 not in map = defaults to enabled
			},
		},
	}

	svc := New(meta, wl, hist, us, users)
	// collectFromMDBLists is a placeholder but we can verify the per-shelf
	// filtering doesn't panic and respects settings
	items := svc.buildUserCalendar("user1")
	// No items expected since MDBList collection is a placeholder
	if len(items) != 0 {
		t.Fatalf("expected 0 items (mdblist is placeholder), got %d", len(items))
	}
}

func TestMDBListShelfEnabled(t *testing.T) {
	// nil map = all enabled
	cal := models.CalendarSettings{}
	if !cal.MDBListShelfEnabled("any-shelf") {
		t.Error("expected nil map to enable all shelves")
	}

	// explicit disable
	cal = models.CalendarSettings{
		MDBListShelves: map[string]bool{"shelf-1": false, "shelf-2": true},
	}
	if cal.MDBListShelfEnabled("shelf-1") {
		t.Error("expected shelf-1 to be disabled")
	}
	if !cal.MDBListShelfEnabled("shelf-2") {
		t.Error("expected shelf-2 to be enabled")
	}
	// unknown shelf defaults to enabled
	if !cal.MDBListShelfEnabled("shelf-3") {
		t.Error("expected unknown shelf to default to enabled")
	}
}

func TestRefreshAll(t *testing.T) {
	meta, wl, hist, us, users := defaultMocks()
	users.users = []models.User{{ID: "u1"}, {ID: "u2"}}
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{Name: "Show", TVDBID: 100},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}
	wl.items["u1"] = []models.WatchlistItem{
		{ID: "tvdb:100", MediaType: "series", Name: "Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}

	svc := New(meta, wl, hist, us, users)
	svc.refreshAll()

	cal := svc.Get("u1")
	if cal == nil || len(cal.Items) != 1 {
		t.Fatalf("expected 1 item for u1, got %v", cal)
	}

	cal2 := svc.Get("u2")
	if cal2 == nil {
		t.Fatal("expected non-nil calendar for u2")
	}
	if len(cal2.Items) != 0 {
		t.Errorf("expected 0 items for u2, got %d", len(cal2.Items))
	}
}

func TestParseAirDateTime(t *testing.T) {
	tests := []struct {
		name         string
		dateStr      string
		airsTime     string
		airsTimezone string
		wantUTC      string // expected UTC time as "2006-01-02 15:04"
	}{
		{
			name:         "HBO 9pm EST",
			dateStr:      "2026-02-22",
			airsTime:     "21:00",
			airsTimezone: "America/New_York",
			wantUTC:      "2026-02-23 02:00",
		},
		{
			name:         "BBC 9pm London",
			dateStr:      "2026-02-22",
			airsTime:     "21:00",
			airsTimezone: "Europe/London",
			wantUTC:      "2026-02-22 21:00",
		},
		{
			name:         "no air time falls back to end of day",
			dateStr:      "2026-02-22",
			airsTime:     "",
			airsTimezone: "",
			wantUTC:      "2026-02-22 23:59",
		},
		{
			name:         "air time without timezone falls back to end of day",
			dateStr:      "2026-02-22",
			airsTime:     "21:00",
			airsTimezone: "",
			wantUTC:      "2026-02-22 23:59",
		},
		{
			name:         "Korean 22:00 KST",
			dateStr:      "2026-02-22",
			airsTime:     "22:00",
			airsTimezone: "Asia/Seoul",
			wantUTC:      "2026-02-22 13:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAirDateTime(tt.dateStr, tt.airsTime, tt.airsTimezone)
			gotStr := got.Format("2006-01-02 15:04")
			if gotStr != tt.wantUTC {
				t.Errorf("ParseAirDateTime(%q, %q, %q) = %s, want %s",
					tt.dateStr, tt.airsTime, tt.airsTimezone, gotStr, tt.wantUTC)
			}
		})
	}
}

func TestBuildUserCalendar_UTCAwareSorting(t *testing.T) {
	// Two items on the same date but different timezones.
	// By string comparison they have the same AirDate, but by UTC datetime
	// the Seoul item airs much earlier (13:00 UTC vs 02:00 UTC next day).
	meta, wl, hist, us, users := defaultMocks()
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{
			Name: "Seoul Show", TVDBID: 100,
			AirsTime: "22:00", AirsTimezone: "Asia/Seoul",
		},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Seoul Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}
	meta.series[200] = &models.SeriesDetails{
		Title: models.Title{
			Name: "NYC Show", TVDBID: 200,
			AirsTime: "21:00", AirsTimezone: "America/New_York",
		},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "NYC Ep", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate(5)},
			}},
		},
	}
	wl.items["user1"] = []models.WatchlistItem{
		{ID: "tvdb:200", MediaType: "series", Name: "NYC Show", ExternalIDs: map[string]string{"tvdb": "200"}},
		{ID: "tvdb:100", MediaType: "series", Name: "Seoul Show", ExternalIDs: map[string]string{"tvdb": "100"}},
	}

	svc := New(meta, wl, hist, us, users)
	items := svc.buildUserCalendar("user1")

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// Seoul 22:00 KST = 13:00 UTC, NYC 21:00 EST = 02:00 UTC next day
	// Seoul should sort first despite same AirDate string
	if items[0].Title != "Seoul Show" {
		t.Errorf("expected Seoul Show first (earlier in UTC), got %q", items[0].Title)
	}
	if items[1].Title != "NYC Show" {
		t.Errorf("expected NYC Show second (later in UTC), got %q", items[1].Title)
	}
}

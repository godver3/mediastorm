package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/handlers"
	"novastream/models"
	"novastream/services/calendar"

	"github.com/gorilla/mux"
)

// --- Minimal mock for users service ---

type mockCalendarUserService struct {
	exists map[string]bool
}

func (m *mockCalendarUserService) Exists(id string) bool {
	return m.exists[id]
}

func (m *mockCalendarUserService) Get(id string) (models.User, bool) {
	return models.User{}, m.exists[id]
}

// --- Helpers ---

func setupCalendarHandler(t *testing.T) (*handlers.CalendarHandler, *calendar.Service) {
	t.Helper()

	// Build a calendar service with mock data that has items at known dates
	svc := calendar.New(
		&calendarMockMetadata{},
		&calendarMockWatchlist{},
		&calendarMockHistory{},
		&calendarMockUserSettings{},
		&calendarMockUsers{},
	)

	usersSvc := &mockCalendarUserService{exists: map[string]bool{"user1": true}}
	h := handlers.NewCalendarHandler(svc, usersSvc, false)

	return h, svc
}

// Calendar-specific mock services for handler tests
type calendarMockMetadata struct{}

func (m *calendarMockMetadata) SeriesDetails(_ context.Context, _ models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	return nil, nil
}
func (m *calendarMockMetadata) MovieDetails(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	return nil, nil
}
func (m *calendarMockMetadata) Trending(_ context.Context, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}

type calendarMockWatchlist struct{}

func (m *calendarMockWatchlist) List(_ string) ([]models.WatchlistItem, error) {
	return nil, nil
}

type calendarMockHistory struct{}

func (m *calendarMockHistory) ListContinueWatching(_ string) ([]models.SeriesWatchState, error) {
	return nil, nil
}

type calendarMockUserSettings struct{}

func (m *calendarMockUserSettings) Get(_ string) (*models.UserSettings, error) {
	return nil, nil
}

type calendarMockUsers struct{}

func (m *calendarMockUsers) List() []models.User {
	return []models.User{{ID: "user1"}}
}

func TestGetCalendar_EmptyCache(t *testing.T) {
	h, _ := setupCalendarHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp models.CalendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Total != 0 {
		t.Errorf("expected total 0, got %d", resp.Total)
	}
	if resp.Timezone != "UTC" {
		t.Errorf("expected timezone UTC, got %q", resp.Timezone)
	}
	if resp.Days != 30 {
		t.Errorf("expected days 30, got %d", resp.Days)
	}
}

func TestGetCalendar_InvalidTimezone(t *testing.T) {
	h, _ := setupCalendarHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?tz=Invalid/Zone", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetCalendar_UserNotFound(t *testing.T) {
	h, _ := setupCalendarHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users/nonexistent/calendar", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "nonexistent"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetCalendar_DaysParam(t *testing.T) {
	h, _ := setupCalendarHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?days=14", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	var resp models.CalendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Days != 14 {
		t.Errorf("expected days 14, got %d", resp.Days)
	}
}

func TestGetCalendar_DaysClamped(t *testing.T) {
	h, _ := setupCalendarHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?days=200", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	var resp models.CalendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Days != 90 {
		t.Errorf("expected days clamped to 90, got %d", resp.Days)
	}
}

func TestGetCalendar_TimezoneApplied(t *testing.T) {
	h, _ := setupCalendarHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?tz=America/New_York", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	var resp models.CalendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Timezone != "America/New_York" {
		t.Errorf("expected timezone 'America/New_York', got %q", resp.Timezone)
	}
}

// --- Richer mocks for AirTime conversion test ---

type calendarMockMetadataWithData struct {
	series map[int64]*models.SeriesDetails
}

func (m *calendarMockMetadataWithData) SeriesDetails(_ context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	if d, ok := m.series[req.TVDBID]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("not found")
}
func (m *calendarMockMetadataWithData) MovieDetails(_ context.Context, _ models.MovieDetailsQuery) (*models.Title, error) {
	return nil, nil
}
func (m *calendarMockMetadataWithData) Trending(_ context.Context, _ string) ([]models.TrendingItem, error) {
	return nil, nil
}

type calendarMockWatchlistWithData struct {
	items []models.WatchlistItem
}

func (m *calendarMockWatchlistWithData) List(_ string) ([]models.WatchlistItem, error) {
	return m.items, nil
}

func TestGetCalendar_AirTimeConvertedToUserTZ(t *testing.T) {
	futureDate := time.Now().AddDate(0, 0, 5).Format("2006-01-02")

	meta := &calendarMockMetadataWithData{
		series: map[int64]*models.SeriesDetails{
			100: {
				Title: models.Title{
					Name: "Test Show", TVDBID: 100,
					AirsTime: "21:00", AirsTimezone: "America/New_York",
				},
				Seasons: []models.SeriesSeason{
					{Number: 1, Episodes: []models.SeriesEpisode{
						{Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate},
					}},
				},
			},
		},
	}
	wl := &calendarMockWatchlistWithData{
		items: []models.WatchlistItem{
			{ID: "tvdb:100", MediaType: "series", Name: "Test Show", ExternalIDs: map[string]string{"tvdb": "100"}},
		},
	}

	svc := calendar.New(
		meta, wl,
		&calendarMockHistory{},
		&calendarMockUserSettings{},
		&calendarMockUsers{},
	)
	// Populate the cache via background refresh
	svc.StartBackgroundRefresh(24 * time.Hour)
	defer svc.Stop()
	// Wait briefly for initial population
	time.Sleep(200 * time.Millisecond)

	usersSvc := &mockCalendarUserService{exists: map[string]bool{"user1": true}}
	h := handlers.NewCalendarHandler(svc, usersSvc, false)

	// Request with Australian timezone — 21:00 EST should become a different local time
	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?tz=Australia/Sydney", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp models.CalendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Total != 1 {
		t.Fatalf("expected 1 item, got %d", resp.Total)
	}

	item := resp.Items[0]
	// AirTime should be converted from 21:00 EST to Sydney time
	if item.AirTime == "21:00" {
		t.Errorf("AirTime should be converted from original 21:00 EST, but got %q (unchanged)", item.AirTime)
	}
	if item.AirTimezone != "Australia/Sydney" {
		t.Errorf("AirTimezone should be user's TZ 'Australia/Sydney', got %q", item.AirTimezone)
	}
	// 21:00 EST = 02:00 UTC next day. In AEDT (UTC+11) that's 13:00 next day.
	// In AEST (UTC+10) that's 12:00 next day. Check it's one of these.
	if item.AirTime != "13:00" && item.AirTime != "12:00" {
		t.Errorf("expected AirTime ~12:00 or 13:00 (Sydney), got %q", item.AirTime)
	}
}

func TestGetCalendar_AirTimeNotFabricatedForMovies(t *testing.T) {
	futureDate := time.Now().AddDate(0, 0, 10).Format("2006-01-02")

	meta := &calendarMockMetadataWithData{series: map[int64]*models.SeriesDetails{}}
	// We need a movie mock — use the simple mock that returns nil and manually populate cache
	// Instead, test with a series that has no AirTime set
	meta.series[100] = &models.SeriesDetails{
		Title: models.Title{
			Name: "No Time Show", TVDBID: 100,
			// No AirsTime or AirsTimezone set
		},
		Seasons: []models.SeriesSeason{
			{Number: 1, Episodes: []models.SeriesEpisode{
				{Name: "Ep 1", SeasonNumber: 1, EpisodeNumber: 1, AiredDate: futureDate},
			}},
		},
	}
	wl := &calendarMockWatchlistWithData{
		items: []models.WatchlistItem{
			{ID: "tvdb:100", MediaType: "series", Name: "No Time Show", ExternalIDs: map[string]string{"tvdb": "100"}},
		},
	}

	svc := calendar.New(
		meta, wl,
		&calendarMockHistory{},
		&calendarMockUserSettings{},
		&calendarMockUsers{},
	)
	svc.StartBackgroundRefresh(24 * time.Hour)
	defer svc.Stop()
	time.Sleep(200 * time.Millisecond)

	usersSvc := &mockCalendarUserService{exists: map[string]bool{"user1": true}}
	h := handlers.NewCalendarHandler(svc, usersSvc, false)

	req := httptest.NewRequest(http.MethodGet, "/api/users/user1/calendar?tz=Australia/Sydney", nil)
	req = mux.SetURLVars(req, map[string]string{"userID": "user1"})
	rec := httptest.NewRecorder()

	h.GetCalendar(rec, req)

	var resp models.CalendarResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp.Total != 1 {
		t.Fatalf("expected 1 item, got %d", resp.Total)
	}

	// Item had no original AirTime — handler should NOT fabricate one
	if resp.Items[0].AirTime != "" {
		t.Errorf("AirTime should remain empty for items without original air time, got %q", resp.Items[0].AirTime)
	}
	if resp.Items[0].AirTimezone != "" {
		t.Errorf("AirTimezone should remain empty for items without original air time, got %q", resp.Items[0].AirTimezone)
	}
}

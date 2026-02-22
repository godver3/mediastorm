package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

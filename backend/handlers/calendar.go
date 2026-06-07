package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/models"
	"novastream/services/calendar"

	"github.com/gorilla/mux"
)

// CalendarHandler serves the calendar API endpoint.
type CalendarHandler struct {
	Service  *calendar.Service
	Users    userService
	DemoMode bool
}

// NewCalendarHandler creates a new CalendarHandler.
func NewCalendarHandler(service *calendar.Service, users userService, demoMode bool) *CalendarHandler {
	return &CalendarHandler{
		Service:  service,
		Users:    users,
		DemoMode: demoMode,
	}
}

// GetCalendar returns upcoming content for the user, adjusted to the requested timezone.
func (h *CalendarHandler) GetCalendar(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	// Parse timezone
	tzName := strings.TrimSpace(r.URL.Query().Get("tz"))
	loc := time.UTC
	if tzName != "" {
		parsed, err := time.LoadLocation(tzName)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid timezone: " + tzName})
			return
		}
		loc = parsed
	}

	// Parse days (default 30, max 90)
	days := 30
	if daysStr := r.URL.Query().Get("days"); daysStr != "" {
		if parsed, err := strconv.Atoi(daysStr); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if days > 90 {
		days = 90
	}

	// Parse recent days (default 7, max 90). The cache may contain a larger
	// past window so shelves can fill recently-aired rows without changing the
	// regular calendar default.
	recentDays := calendar.RecentDaysWindow
	if recentDaysStr := r.URL.Query().Get("recentDays"); recentDaysStr != "" {
		if parsed, err := strconv.Atoi(recentDaysStr); err == nil && parsed >= 0 {
			recentDays = parsed
		}
	}
	if recentDays > calendar.MaxRecentDaysWindow {
		recentDays = calendar.MaxRecentDaysWindow
	}

	// Parse optional source filter
	sourceFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	compact := parseCalendarBoolQuery(r.URL.Query().Get("compact"))
	home := parseCalendarBoolQuery(r.URL.Query().Get("home"))
	homeLimit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			homeLimit = parsed
		}
	}
	if homeLimit > 100 {
		homeLimit = 100
	}

	cached := h.Service.Get(userID)
	if cached == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.CalendarResponse{
			Items:      []models.CalendarItem{},
			Total:      0,
			Timezone:   loc.String(),
			Days:       days,
			RecentDays: calendar.RecentDaysWindow,
		})
		return
	}

	// Compute the date window in the user's timezone
	nowInTZ := time.Now().In(loc)
	todayStart := time.Date(nowInTZ.Year(), nowInTZ.Month(), nowInTZ.Day(), 0, 0, 0, 0, loc)
	recentStart := todayStart.AddDate(0, 0, -recentDays)
	cutoff := todayStart.AddDate(0, 0, days)

	// Filter and TZ-adjust items
	var result []models.CalendarItem
	for _, item := range cached.Items {
		// Source filter
		if sourceFilter != "" && item.Source != sourceFilter {
			continue
		}

		// Build the full air datetime using air time + timezone when available.
		// This prevents premature filtering of episodes that air later in the day.
		airDT := calendar.ParseAirDateTime(item.AirDate, item.AirTime, item.AirTimezone)
		airInTZ := airDT.In(loc)
		airDateInTZ := time.Date(airInTZ.Year(), airInTZ.Month(), airInTZ.Day(), 0, 0, 0, 0, loc)
		if airDateInTZ.Before(recentStart) || airDateInTZ.After(cutoff) {
			continue
		}

		// Return date, time, and timezone adjusted to the user's timezone
		adjusted := item
		adjusted.AirDate = airInTZ.Format("2006-01-02")
		if item.AirTime != "" {
			adjusted.AirTime = airInTZ.Format("15:04")
			adjusted.AirTimezone = loc.String()
		}
		if compact {
			adjusted.EpisodeOverview = compactCalendarText(adjusted.EpisodeOverview, 240)
		}
		result = append(result, adjusted)
	}

	// Re-sort after TZ adjustment so items are ordered in the user's local time
	sort.Slice(result, func(i, j int) bool {
		if result[i].AirDate != result[j].AirDate {
			return result[i].AirDate < result[j].AirDate
		}
		return result[i].AirTime < result[j].AirTime
	})

	if result == nil {
		result = []models.CalendarItem{}
	}
	if home {
		result = limitCalendarForHome(result, loc, homeLimit)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.CalendarResponse{
		Items:       result,
		Total:       len(result),
		Timezone:    loc.String(),
		Days:        days,
		RecentDays:  recentDays,
		RefreshedAt: cached.RefreshedAt.Format(time.RFC3339),
	})
}

func limitCalendarForHome(items []models.CalendarItem, loc *time.Location, limit int) []models.CalendarItem {
	if len(items) == 0 || limit <= 0 {
		return []models.CalendarItem{}
	}
	candidateLimit := limit * 3
	if candidateLimit < 60 {
		candidateLimit = 60
	}
	if candidateLimit > 200 {
		candidateLimit = 200
	}

	now := time.Now().In(loc)
	recent := make([]models.CalendarItem, 0, candidateLimit)
	recentBySource := make(map[string][]models.CalendarItem)
	upcoming := make([]models.CalendarItem, 0, candidateLimit)
	for _, item := range items {
		airDT := calendar.ParseAirDateTime(item.AirDate, item.AirTime, item.AirTimezone).In(loc)
		if airDT.After(now) {
			if len(upcoming) < candidateLimit {
				upcoming = append(upcoming, item)
			}
			continue
		}
		source := item.Source
		if source == "" {
			source = "unknown"
		}
		sourceRecent := append(recentBySource[source], item)
		if len(sourceRecent) > limit {
			sourceRecent = sourceRecent[len(sourceRecent)-limit:]
		}
		recentBySource[source] = sourceRecent
	}

	for _, sourceRecent := range recentBySource {
		recent = append(recent, sourceRecent...)
	}
	sort.Slice(recent, func(i, j int) bool {
		ti := calendar.ParseAirDateTime(recent[i].AirDate, recent[i].AirTime, recent[i].AirTimezone)
		tj := calendar.ParseAirDateTime(recent[j].AirDate, recent[j].AirTime, recent[j].AirTimezone)
		return ti.Before(tj)
	})

	limited := make([]models.CalendarItem, 0, len(recent)+len(upcoming))
	limited = append(limited, recent...)
	limited = append(limited, upcoming...)
	return limited
}

func parseCalendarBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func compactCalendarText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes]))
}

// Options handles CORS preflight.
func (h *CalendarHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *CalendarHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["userID"])

	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return "", false
	}

	if h.Users != nil && !h.Users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return "", false
	}

	return userID, true
}

package handlers

import (
	"encoding/json"
	"net/http"
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

	// Parse optional source filter
	sourceFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))

	cached := h.Service.Get(userID)
	if cached == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.CalendarResponse{
			Items:    []models.CalendarItem{},
			Total:    0,
			Timezone: loc.String(),
			Days:     days,
		})
		return
	}

	// Compute the date window in the user's timezone
	nowInTZ := time.Now().In(loc)
	todayStart := time.Date(nowInTZ.Year(), nowInTZ.Month(), nowInTZ.Day(), 0, 0, 0, 0, loc)
	cutoff := todayStart.AddDate(0, 0, days)

	// Filter and TZ-adjust items
	var result []models.CalendarItem
	for _, item := range cached.Items {
		// Source filter
		if sourceFilter != "" && item.Source != sourceFilter {
			continue
		}

		// Parse the stored UTC date and check against the user's TZ window
		airDate, err := time.Parse("2006-01-02", item.AirDate)
		if err != nil {
			continue
		}

		// Convert to user's timezone for comparison
		airInTZ := airDate.In(loc)
		if airInTZ.Before(todayStart) || airInTZ.After(cutoff) {
			continue
		}

		// Return the date in the user's timezone
		adjusted := item
		adjusted.AirDate = airInTZ.Format("2006-01-02")
		result = append(result, adjusted)
	}

	if result == nil {
		result = []models.CalendarItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.CalendarResponse{
		Items:       result,
		Total:       len(result),
		Timezone:    loc.String(),
		Days:        days,
		RefreshedAt: cached.RefreshedAt.Format(time.RFC3339),
	})
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

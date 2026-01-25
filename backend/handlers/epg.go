package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"novastream/services/epg"
)

// EPGHandler handles EPG-related HTTP requests.
type EPGHandler struct {
	epgService *epg.Service
}

// NewEPGHandler creates a new EPG handler.
func NewEPGHandler(epgService *epg.Service) *EPGHandler {
	return &EPGHandler{
		epgService: epgService,
	}
}

// GetNowPlaying returns current and next programs for specified channels.
// GET /api/live/epg/now?channels=ch1,ch2,ch3
func (h *EPGHandler) GetNowPlaying(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}

	channelsParam := r.URL.Query().Get("channels")
	if channelsParam == "" {
		http.Error(w, `{"error":"missing channels parameter"}`, http.StatusBadRequest)
		return
	}

	channelIDs := strings.Split(channelsParam, ",")
	for i := range channelIDs {
		channelIDs[i] = strings.TrimSpace(channelIDs[i])
	}

	result := h.epgService.GetNowPlaying(channelIDs)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("[epg] GetNowPlaying JSON encode error: %v", err)
	}
}

// GetSchedule returns program schedule for a channel within a time range.
// GET /api/live/epg/schedule?channel=ch1&days=1
func (h *EPGHandler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}

	channelID := r.URL.Query().Get("channel")
	if channelID == "" {
		http.Error(w, `{"error":"missing channel parameter"}`, http.StatusBadRequest)
		return
	}

	// Default to 1 day
	days := 1
	if daysParam := r.URL.Query().Get("days"); daysParam != "" {
		var d int
		if _, err := parseIntParam(daysParam, &d); err == nil && d > 0 && d <= 14 {
			days = d
		}
	}

	start := time.Now().UTC()
	end := start.Add(time.Duration(days) * 24 * time.Hour)

	programs := h.epgService.GetSchedule(channelID, start, end)

	response := struct {
		ChannelID string              `json:"channelId"`
		Programs  []interface{}       `json:"programs"`
	}{
		ChannelID: channelID,
		Programs:  make([]interface{}, len(programs)),
	}
	for i, p := range programs {
		response.Programs[i] = p
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[epg] GetSchedule JSON encode error: %v", err)
	}
}

// GetScheduleMultiple returns program schedules for multiple channels within a time range.
// GET /api/live/epg/schedule/batch?channels=ch1,ch2,ch3&hours=4
func (h *EPGHandler) GetScheduleMultiple(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}

	channelsParam := r.URL.Query().Get("channels")
	if channelsParam == "" {
		http.Error(w, `{"error":"missing channels parameter"}`, http.StatusBadRequest)
		return
	}

	channelIDs := strings.Split(channelsParam, ",")
	for i := range channelIDs {
		channelIDs[i] = strings.TrimSpace(channelIDs[i])
	}

	// Default to 4 hours window
	hours := 4
	if hoursParam := r.URL.Query().Get("hours"); hoursParam != "" {
		var h int
		if _, err := parseIntParam(hoursParam, &h); err == nil && h > 0 && h <= 24 {
			hours = h
		}
	}

	// Optional start time offset (in minutes from now, can be negative for past programs)
	startOffset := 0
	if offsetParam := r.URL.Query().Get("startOffset"); offsetParam != "" {
		var o int
		if _, err := parseIntParam(offsetParam, &o); err == nil {
			startOffset = o
		}
	}

	start := time.Now().UTC().Add(time.Duration(startOffset) * time.Minute)
	end := start.Add(time.Duration(hours) * time.Hour)

	schedules := h.epgService.GetScheduleMultiple(channelIDs, start, end)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(schedules); err != nil {
		log.Printf("[epg] GetScheduleMultiple JSON encode error: %v", err)
	}
}

// GetChannelSchedule returns the full day schedule for a channel.
// GET /api/live/epg/channel/{id}?date=2024-01-15
func (h *EPGHandler) GetChannelSchedule(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}

	// Extract channel ID from path
	path := r.URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 5 {
		http.Error(w, `{"error":"missing channel ID"}`, http.StatusBadRequest)
		return
	}
	channelID := parts[len(parts)-1]

	// Parse date (default to today)
	date := time.Now().UTC()
	if dateParam := r.URL.Query().Get("date"); dateParam != "" {
		if parsed, err := time.Parse("2006-01-02", dateParam); err == nil {
			date = parsed
		}
	}

	programs := h.epgService.GetChannelSchedule(channelID, date)

	response := struct {
		ChannelID string              `json:"channelId"`
		Date      string              `json:"date"`
		Programs  []interface{}       `json:"programs"`
	}{
		ChannelID: channelID,
		Date:      date.Format("2006-01-02"),
		Programs:  make([]interface{}, len(programs)),
	}
	for i, p := range programs {
		response.Programs[i] = p
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[epg] GetChannelSchedule JSON encode error: %v", err)
	}
}

// GetStatus returns the current EPG service status.
// GET /api/live/epg/status
func (h *EPGHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}

	status := h.epgService.GetStatus()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Printf("[epg] GetStatus JSON encode error: %v", err)
	}
}

// Refresh triggers a manual EPG refresh.
// POST /api/live/epg/refresh
func (h *EPGHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}

	// Run refresh in background with independent context (not tied to HTTP request)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := h.epgService.Refresh(ctx); err != nil {
			log.Printf("[epg] refresh error: %v", err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"refresh started"}`))
}

// Options handles CORS preflight requests.
func (h *EPGHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// parseIntParam is a helper to parse integer query parameters.
func parseIntParam(s string, out *int) (string, error) {
	var v int
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s, err
	}
	*out = v
	return s, nil
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/epg"
)

const maxEPGNowRequestBodySize = 1024 * 1024

type epgNowRequest struct {
	Channels []string `json:"channels"`
}

// EPGHandler handles EPG-related HTTP requests.
type EPGHandler struct {
	epgService      *epg.Service
	cfgManager      *config.Manager
	userSettingsSvc LiveUserSettingsProvider
	liveChannels    LiveChannelProvider
}

// NewEPGHandler creates a new EPG handler.
func NewEPGHandler(epgService *epg.Service, cfgManager *config.Manager, userSettingsSvc LiveUserSettingsProvider) *EPGHandler {
	return &EPGHandler{
		epgService:      epgService,
		cfgManager:      cfgManager,
		userSettingsSvc: userSettingsSvc,
	}
}

// SetLiveChannelProvider binds guide queries to the account-visible catalog.
func (h *EPGHandler) SetLiveChannelProvider(provider LiveChannelProvider) { h.liveChannels = provider }

// getEPGTimeOffset resolves the EPG time offset for the current request,
// merging global settings with any per-profile override.
func (h *EPGHandler) getEPGTimeOffset(r *http.Request) time.Duration {
	offset := 0
	if h.cfgManager != nil {
		if settings, err := h.cfgManager.Load(); err == nil {
			offset = settings.Live.EPG.TimeOffsetMinutes
		}
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID != "" && h.userSettingsSvc != nil {
		userSettings, err := h.userSettingsSvc.Get(profileID)
		if err == nil && userSettings != nil && userSettings.LiveTV.EPG != nil && userSettings.LiveTV.EPG.TimeOffsetMinutes != nil {
			offset = *userSettings.LiveTV.EPG.TimeOffsetMinutes
		}
	}

	return time.Duration(offset) * time.Minute
}

func (h *EPGHandler) resolveEPGEnabled(r *http.Request, fallback bool) bool {
	if h.cfgManager == nil {
		return fallback
	}

	settings, err := h.cfgManager.Load()
	if err != nil {
		return fallback
	}

	summary := summarizeEPGGuideConfig(&settings)
	enabled := settings.Live.EPG.Enabled || summary.sourceEPGEnabled > 0
	if h.epgService != nil && h.epgService.HasHDHomeRunGuide() {
		enabled = true
	}
	profileID := r.URL.Query().Get("profileId")
	if profileID != "" && h.userSettingsSvc != nil {
		userSettings, err := h.userSettingsSvc.Get(profileID)
		if err == nil && userSettings != nil && userSettings.LiveTV.EPG != nil && userSettings.LiveTV.EPG.Enabled != nil {
			enabled = *userSettings.LiveTV.EPG.Enabled
			log.Printf("[epg] profile EPG enabled override applied profileId=%s globalEnabled=%v resolvedEnabled=%v", profileID, settings.Live.EPG.Enabled, enabled)
		}
	}

	return enabled
}

// applyOffsetToProgram returns a copy of the program with start/stop shifted by the offset.
func applyOffsetToProgram(p models.EPGProgram, offset time.Duration) models.EPGProgram {
	p.Start = p.Start.Add(offset)
	p.Stop = p.Stop.Add(offset)
	return p
}

func parseEPGNowChannelIDs(w http.ResponseWriter, r *http.Request) ([]string, error) {
	var channelIDs []string
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxEPGNowRequestBodySize)
		decoder := json.NewDecoder(r.Body)
		var request epgNowRequest
		if err := decoder.Decode(&request); err != nil {
			return nil, fmt.Errorf("decode request: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, errors.New("request body must contain one JSON object")
			}
			return nil, fmt.Errorf("decode request: %w", err)
		}
		channelIDs = request.Channels
	} else {
		channelsParam := r.URL.Query().Get("channels")
		if channelsParam != "" {
			channelIDs = strings.Split(channelsParam, ",")
		}
	}

	seen := make(map[string]struct{}, len(channelIDs))
	normalized := make([]string, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		channelID = strings.TrimSpace(channelID)
		if channelID == "" {
			continue
		}
		if _, exists := seen[channelID]; exists {
			continue
		}
		seen[channelID] = struct{}{}
		normalized = append(normalized, channelID)
	}
	if len(normalized) == 0 {
		return nil, errors.New("missing channels")
	}

	return normalized, nil
}

// GetNowPlaying returns current and next programs for specified channels.
// GET /api/live/epg/now?channels=ch1,ch2,ch3
// POST /api/live/epg/now with {"channels":["ch1","ch2","ch3"]}
func (h *EPGHandler) GetNowPlaying(w http.ResponseWriter, r *http.Request) {
	if h.epgService == nil {
		http.Error(w, `{"error":"EPG service not available"}`, http.StatusServiceUnavailable)
		return
	}
	xmltvOn := h.resolveEPGEnabled(r, h.epgService.IsEnabled())
	if !xmltvOn && !h.stremioEventsOn(r) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
		return
	}

	channelIDs, err := parseEPGNowChannelIDs(w, r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	offset := h.getEPGTimeOffset(r)

	// Query with the negative offset so we find the correct programs in stored data,
	// then shift response times by the positive offset.
	channelIDs, err = filterEPGChannelIDs(r, h.cfgManager, h.liveChannels, channelIDs)
	if err != nil {
		http.Error(w, "channel not available", http.StatusNotFound)
		return
	}
	var result []models.EPGNowPlaying
	if xmltvOn {
		result = h.epgService.GetNowPlaying(channelIDs, -offset)
	} else {
		result = make([]models.EPGNowPlaying, 0, len(channelIDs))
		for _, id := range channelIDs {
			result = append(result, models.EPGNowPlaying{ChannelID: id})
		}
	}
	h.fillNowPlayingFromStremioEvents(result, time.Now().UTC().Add(-offset))
	if xmltvOn {
		h.epgService.RefreshIfExhausted("now query")
	}
	currentCount := 0
	nextCount := 0
	var missingCurrentIDs []string
	for _, item := range result {
		if item.Current != nil {
			currentCount++
		} else if len(missingCurrentIDs) < 20 {
			missingCurrentIDs = append(missingCurrentIDs, item.ChannelID)
		}
		if item.Next != nil {
			nextCount++
		}
	}
	log.Printf("[epg] now query profileId=%q requested=%d returned=%d currentMatches=%d nextMatches=%d offsetMinutes=%d",
		r.URL.Query().Get("profileId"),
		len(channelIDs),
		len(result),
		currentCount,
		nextCount,
		int(offset.Minutes()),
	)
	if len(missingCurrentIDs) > 0 && h.epgService.HasHDHomeRunGuide() {
		log.Printf("[hdhomerun-epg] now coverage requested=%d withoutCurrent=%d lookupIDSample=%q offsetMinutes=%d", len(channelIDs), len(result)-currentCount, missingCurrentIDs, int(offset.Minutes()))
	}

	if offset != 0 {
		for i := range result {
			if result[i].Current != nil {
				shifted := applyOffsetToProgram(*result[i].Current, offset)
				result[i].Current = &shifted
			}
			if result[i].Next != nil {
				shifted := applyOffsetToProgram(*result[i].Next, offset)
				result[i].Next = &shifted
			}
		}
	}

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
	xmltvOn := h.resolveEPGEnabled(r, h.epgService.IsEnabled())
	if !xmltvOn && !h.stremioEventsOn(r) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"channelId":"","programs":[]}`))
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

	offset := h.getEPGTimeOffset(r)

	// Shift query window by -offset to find correct programs in stored data
	start := time.Now().UTC().Add(-offset)
	end := start.Add(time.Duration(days) * 24 * time.Hour)

	if allowed, accessErr := filterEPGChannelIDs(r, h.cfgManager, h.liveChannels, []string{channelID}); accessErr != nil || len(allowed) == 0 {
		http.Error(w, "channel not available", http.StatusNotFound)
		return
	}
	var programs []models.EPGProgram
	if xmltvOn {
		programs = h.epgService.GetSchedule(channelID, start, end)
	}
	if len(programs) == 0 {
		programs = h.stremioEventPrograms([]string{channelID}, start, end)[channelID]
	}
	log.Printf("[epg] schedule query profileId=%q channelConfigured=%v programs=%d offsetMinutes=%d",
		r.URL.Query().Get("profileId"),
		channelID != "",
		len(programs),
		int(offset.Minutes()),
	)

	if offset != 0 {
		for i := range programs {
			programs[i] = applyOffsetToProgram(programs[i], offset)
		}
	}

	response := struct {
		ChannelID string        `json:"channelId"`
		Programs  []interface{} `json:"programs"`
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
	xmltvOn := h.resolveEPGEnabled(r, h.epgService.IsEnabled())
	if !xmltvOn && !h.stremioEventsOn(r) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
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

	offset := h.getEPGTimeOffset(r)

	// Shift query window by -offset to find correct programs in stored data
	start := time.Now().UTC().Add(time.Duration(startOffset)*time.Minute - offset)
	end := start.Add(time.Duration(hours) * time.Hour)

	allowedIDs, accessErr := filterEPGChannelIDs(r, h.cfgManager, h.liveChannels, channelIDs)
	if accessErr != nil {
		http.Error(w, "channel not available", http.StatusNotFound)
		return
	}
	channelIDs = allowedIDs
	schedules := map[string][]models.EPGProgram{}
	if xmltvOn {
		schedules = h.epgService.GetScheduleMultiple(channelIDs, start, end)
	}
	h.fillSchedulesFromStremioEvents(schedules, channelIDs, start, end)
	if xmltvOn {
		h.epgService.RefreshIfExhausted("schedule batch")
	}
	matchedChannels := 0
	totalPrograms := 0
	var emptyIDs []string
	for _, id := range channelIDs {
		if len(schedules[id]) == 0 && len(emptyIDs) < 20 {
			emptyIDs = append(emptyIDs, id)
		}
	}
	for _, programs := range schedules {
		if len(programs) > 0 {
			matchedChannels++
			totalPrograms += len(programs)
		}
	}
	log.Printf("[epg] schedule batch profileId=%q requested=%d matchedChannels=%d totalPrograms=%d hours=%d startOffset=%d offsetMinutes=%d",
		r.URL.Query().Get("profileId"),
		len(channelIDs),
		matchedChannels,
		totalPrograms,
		hours,
		startOffset,
		int(offset.Minutes()),
	)
	if len(emptyIDs) > 0 && h.epgService.HasHDHomeRunGuide() {
		log.Printf("[hdhomerun-epg] query coverage requested=%d emptyChannels=%d lookupIDSample=%q start=%s end=%s offsetMinutes=%d", len(channelIDs), len(channelIDs)-matchedChannels, emptyIDs, start.Format(time.RFC3339), end.Format(time.RFC3339), int(offset.Minutes()))
	}

	if offset != 0 {
		for chID, programs := range schedules {
			for i := range programs {
				programs[i] = applyOffsetToProgram(programs[i], offset)
			}
			schedules[chID] = programs
		}
	}

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
	xmltvOn := h.resolveEPGEnabled(r, h.epgService.IsEnabled())
	if !xmltvOn && !h.stremioEventsOn(r) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"channelId":"","programs":[]}`))
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

	offset := h.getEPGTimeOffset(r)

	// For channel schedule, query with shifted window to find correct stored programs
	year, month, day := date.Date()
	queryStart := time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Add(-offset)
	queryEnd := queryStart.Add(24 * time.Hour)
	if allowed, accessErr := filterEPGChannelIDs(r, h.cfgManager, h.liveChannels, []string{channelID}); accessErr != nil || len(allowed) == 0 {
		http.Error(w, "channel not available", http.StatusNotFound)
		return
	}
	var programs []models.EPGProgram
	if xmltvOn {
		programs = h.epgService.GetSchedule(channelID, queryStart, queryEnd)
	}
	if len(programs) == 0 {
		programs = h.stremioEventPrograms([]string{channelID}, queryStart, queryEnd)[channelID]
	}

	if offset != 0 {
		for i := range programs {
			programs[i] = applyOffsetToProgram(programs[i], offset)
		}
	}

	response := struct {
		ChannelID string        `json:"channelId"`
		Date      string        `json:"date"`
		Programs  []interface{} `json:"programs"`
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
	status.Enabled = h.resolveEPGEnabled(r, status.Enabled) || h.stremioEventsOn(r)
	profileID := r.URL.Query().Get("profileId")
	if h.cfgManager != nil {
		if settings, err := h.cfgManager.Load(); err == nil {
			summary := summarizeEPGGuideConfig(&settings)
			log.Printf("[epg] status profileId=%q enabled=%v refreshing=%v channels=%d programs=%d sourceCount=%d lastErrorSet=%v globalEnabled=%v globalXMLTVConfigured=%v globalSources=%d globalEnabledSources=%d sourceEPGConfigured=%d sourceEPGEnabled=%d xtreamConfigured=%v epgTaskCount=%d",
				profileID,
				status.Enabled,
				status.Refreshing,
				status.ChannelCount,
				status.ProgramCount,
				status.SourceCount,
				status.LastError != "",
				summary.globalEnabled,
				summary.globalXMLTVConfigured,
				summary.globalSourceCount,
				summary.globalEnabledSources,
				summary.sourceEPGConfigured,
				summary.sourceEPGEnabled,
				summary.xtreamConfigured,
				summary.taskCount,
			)
		}
	}

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

// stremioEventPrograms returns synthetic guide entries for Stremio sports
// event channels (see live_stremio_events.go); nil when unavailable.
func (h *EPGHandler) stremioEventPrograms(ids []string, start, end time.Time) map[string][]models.EPGProgram {
	provider, ok := h.liveChannels.(stremioEventProgramProvider)
	if !ok || len(ids) == 0 {
		return nil
	}
	return provider.StremioEventPrograms(ids, start, end)
}

// fillSchedulesFromStremioEvents adds event entries for channels the guide
// has nothing for.
func (h *EPGHandler) fillSchedulesFromStremioEvents(schedules map[string][]models.EPGProgram, ids []string, start, end time.Time) {
	var missing []string
	for _, id := range ids {
		if len(schedules[id]) == 0 {
			missing = append(missing, id)
		}
	}
	for id, programs := range h.stremioEventPrograms(missing, start, end) {
		schedules[id] = programs
	}
}

// fillNowPlayingFromStremioEvents fills current/next for channels the guide
// has nothing for. now is already shifted into stored (UTC) time.
func (h *EPGHandler) fillNowPlayingFromStremioEvents(result []models.EPGNowPlaying, now time.Time) {
	var missing []string
	for _, item := range result {
		if item.Current == nil && item.Next == nil {
			missing = append(missing, item.ChannelID)
		}
	}
	events := h.stremioEventPrograms(missing, now, now.Add(24*time.Hour))
	if len(events) == 0 {
		return
	}
	for i := range result {
		programs := events[result[i].ChannelID]
		if len(programs) == 0 {
			continue
		}
		p := programs[0]
		if p.Start.After(now) {
			result[i].Next = &p
		} else {
			result[i].Current = &p
		}
	}
}

// stremioEventsOn reports whether Stremio event entries should be served even
// with no XMLTV guide configured. A profile that explicitly turned its guide
// off still gets none.
func (h *EPGHandler) stremioEventsOn(r *http.Request) bool {
	provider, ok := h.liveChannels.(stremioEventProgramProvider)
	if !ok || !provider.HasStremioEvents() {
		return false
	}
	profileID := r.URL.Query().Get("profileId")
	if profileID != "" && h.userSettingsSvc != nil {
		if us, err := h.userSettingsSvc.Get(profileID); err == nil && us != nil && us.LiveTV.EPG != nil && us.LiveTV.EPG.Enabled != nil {
			return *us.LiveTV.EPG.Enabled
		}
	}
	return true
}

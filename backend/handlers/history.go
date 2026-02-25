package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"novastream/models"
	"novastream/services/history"

	"github.com/gorilla/mux"
)

type historyService interface {
	RecordEpisode(userID string, payload models.EpisodeWatchPayload) (models.SeriesWatchState, error)
	ListContinueWatching(userID string) ([]models.SeriesWatchState, error)
	ListSeriesStates(userID string) ([]models.SeriesWatchState, error)
	GetSeriesWatchState(userID, seriesID string) (*models.SeriesWatchState, error)
	HideFromContinueWatching(userID, seriesID string) error

	// Watch History methods
	ListWatchHistory(userID string) ([]models.WatchHistoryItem, error)
	GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error)
	ToggleWatched(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error)
	UpdateWatchHistory(userID string, update models.WatchHistoryUpdate) (models.WatchHistoryItem, error)
	BulkUpdateWatchHistory(userID string, updates []models.WatchHistoryUpdate) ([]models.WatchHistoryItem, error)
	IsWatched(userID, mediaType, itemID string) (bool, error)

	// Playback Progress methods
	UpdatePlaybackProgress(userID string, update models.PlaybackProgressUpdate) (models.PlaybackProgress, error)
	GetPlaybackProgress(userID, mediaType, itemID string) (*models.PlaybackProgress, error)
	ListPlaybackProgress(userID string) ([]models.PlaybackProgress, error)
	DeletePlaybackProgress(userID, mediaType, itemID string) error
	ListAllPlaybackProgress() map[string][]models.PlaybackProgress // For admin dashboard
}

var _ historyService = (*history.Service)(nil)

type HistoryHandler struct {
	Service  historyService
	Users    userService
	DemoMode bool
}

func NewHistoryHandler(service historyService, users userService, demoMode bool) *HistoryHandler {
	return &HistoryHandler{Service: service, Users: users, DemoMode: demoMode}
}

func (h *HistoryHandler) ListContinueWatching(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	items, err := h.Service.ListContinueWatching(userID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, history.ErrUserIDRequired) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (h *HistoryHandler) GetSeriesWatchState(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	seriesID := strings.TrimSpace(vars["seriesID"])
	if seriesID == "" {
		http.Error(w, "series id is required", http.StatusBadRequest)
		return
	}

	state, err := h.Service.GetSeriesWatchState(userID, seriesID)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, history.ErrUserIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, history.ErrSeriesIDRequired):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	if state == nil {
		http.Error(w, "series watch state not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// HideFromContinueWatching hides a series/movie from the continue watching list
func (h *HistoryHandler) HideFromContinueWatching(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	seriesID := strings.TrimSpace(vars["seriesID"])
	if seriesID == "" {
		http.Error(w, "series id is required", http.StatusBadRequest)
		return
	}

	err := h.Service.HideFromContinueWatching(userID, seriesID)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, history.ErrUserIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, history.ErrSeriesIDRequired):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *HistoryHandler) RecordEpisode(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var payload models.EpisodeWatchPayload
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state, err := h.Service.RecordEpisode(userID, payload)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, history.ErrUserIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, history.ErrSeriesIDRequired):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// ListWatchHistory returns all watched items for a user
func (h *HistoryHandler) ListWatchHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	items, err := h.Service.ListWatchHistory(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// GetWatchHistoryItem returns a specific watch history item
func (h *HistoryHandler) GetWatchHistoryItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	mediaType := strings.TrimSpace(vars["mediaType"])
	itemID := strings.TrimSpace(vars["id"])

	if mediaType == "" || itemID == "" {
		http.Error(w, "mediaType and id are required", http.StatusBadRequest)
		return
	}

	item, err := h.Service.GetWatchHistoryItem(userID, mediaType, itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if item == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// ToggleWatched toggles the watched status for an item
func (h *HistoryHandler) ToggleWatched(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	mediaType := strings.TrimSpace(vars["mediaType"])
	itemID := strings.TrimSpace(vars["id"])

	if mediaType == "" || itemID == "" {
		http.Error(w, "mediaType and id are required", http.StatusBadRequest)
		return
	}

	var update models.WatchHistoryUpdate
	if r.Body != http.NoBody {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&update); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	update.MediaType = mediaType
	update.ItemID = itemID

	item, err := h.Service.ToggleWatched(userID, update)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// UpdateWatchHistory updates or creates a watch history item
func (h *HistoryHandler) UpdateWatchHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var update models.WatchHistoryUpdate
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Allow URL params to override body
	vars := mux.Vars(r)
	if mediaType := vars["mediaType"]; mediaType != "" {
		update.MediaType = mediaType
	}
	if itemID := vars["id"]; itemID != "" {
		update.ItemID = itemID
	}

	if update.MediaType == "" || update.ItemID == "" {
		http.Error(w, "mediaType and itemID are required", http.StatusBadRequest)
		return
	}

	item, err := h.Service.UpdateWatchHistory(userID, update)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// BulkUpdateWatchHistory updates or creates multiple watch history items
func (h *HistoryHandler) BulkUpdateWatchHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var updates []models.WatchHistoryUpdate
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&updates); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(updates) == 0 {
		http.Error(w, "at least one update is required", http.StatusBadRequest)
		return
	}

	items, err := h.Service.BulkUpdateWatchHistory(userID, updates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// UpdatePlaybackProgress updates the playback progress for a media item
func (h *HistoryHandler) UpdatePlaybackProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var update models.PlaybackProgressUpdate
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Allow URL params to override body
	vars := mux.Vars(r)
	if mediaType := vars["mediaType"]; mediaType != "" {
		update.MediaType = mediaType
	}
	if itemID := vars["id"]; itemID != "" {
		update.ItemID = itemID
	}

	if update.MediaType == "" || update.ItemID == "" {
		http.Error(w, "mediaType and itemID are required", http.StatusBadRequest)
		return
	}

	progress, err := h.Service.UpdatePlaybackProgress(userID, update)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(progress)
}

// GetPlaybackProgress retrieves the playback progress for a specific media item
func (h *HistoryHandler) GetPlaybackProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	mediaType := strings.TrimSpace(vars["mediaType"])
	itemID := strings.TrimSpace(vars["id"])

	if mediaType == "" || itemID == "" {
		http.Error(w, "mediaType and id are required", http.StatusBadRequest)
		return
	}

	progress, err := h.Service.GetPlaybackProgress(userID, mediaType, itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if progress == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(progress)
}

// ListPlaybackProgress returns all playback progress items for a user
func (h *HistoryHandler) ListPlaybackProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	items, err := h.Service.ListPlaybackProgress(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// DeletePlaybackProgress removes playback progress for a specific media item
func (h *HistoryHandler) DeletePlaybackProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	mediaType := strings.TrimSpace(vars["mediaType"])
	itemID := strings.TrimSpace(vars["id"])

	if mediaType == "" || itemID == "" {
		http.Error(w, "mediaType and id are required", http.StatusBadRequest)
		return
	}

	err := h.Service.DeletePlaybackProgress(userID, mediaType, itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *HistoryHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *HistoryHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
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

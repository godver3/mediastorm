package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"novastream/models"
	"novastream/services/watchlist"

	"github.com/gorilla/mux"
)

type watchlistService interface {
	List(userID string) ([]models.WatchlistItem, error)
	AddOrUpdate(userID string, input models.WatchlistUpsert) (models.WatchlistItem, error)
	UpdateState(userID, mediaType, id string, watched *bool, progress interface{}) (models.WatchlistItem, error)
	Remove(userID, mediaType, id string) (bool, error)
}

var _ watchlistService = (*watchlist.Service)(nil)

type userService interface {
	Exists(id string) bool
}

type WatchlistHandler struct {
	Service         watchlistService
	Users           userService
	DemoMode        bool
	HistoryService  historyService
	MetadataService metadataService
}

func NewWatchlistHandler(service watchlistService, users userService, demoMode bool) *WatchlistHandler {
	return &WatchlistHandler{Service: service, Users: users, DemoMode: demoMode}
}

// SetHistoryService sets the history service for watch state enrichment on list responses.
func (h *WatchlistHandler) SetHistoryService(service historyService) {
	h.HistoryService = service
}

// SetMetadataService sets the metadata service for rating enrichment on list responses.
func (h *WatchlistHandler) SetMetadataService(service metadataService) {
	h.MetadataService = service
}

func (h *WatchlistHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	items, err := h.Service.List(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Enrich with pre-computed watch state if history service is available
	if h.HistoryService != nil {
		wh, whErr := h.HistoryService.ListWatchHistory(userID)
		cw, _ := h.HistoryService.ListSeriesStates(userID)
		pp, _ := h.HistoryService.ListPlaybackProgress(userID)
		if whErr == nil {
			idx := buildWatchStateIndex(wh, cw, pp)
			enrichWatchlistItems(items, idx)
		}
	}

	// Enrich with MDBList ratings for sort-by-rating support
	enrichWatchlistRatings(r.Context(), items, h.MetadataService)

	// Enrich with artwork URLs from metadata cache
	enrichWatchlistArtwork(items, h.MetadataService)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (h *WatchlistHandler) Add(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var body models.WatchlistUpsert
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	item, err := h.Service.AddOrUpdate(userID, body)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, watchlist.ErrUserIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, watchlist.ErrIDRequired), errors.Is(err, watchlist.ErrMediaTypeRequired):
			status = http.StatusBadRequest
		case errors.Is(err, watchlist.ErrStorageDirRequired):
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(item)
}

func (h *WatchlistHandler) UpdateState(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	mediaType := vars["mediaType"]
	id := vars["id"]

	var body struct {
		Watched  *bool       `json:"watched,omitempty"`
		Progress interface{} `json:"progress,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	item, err := h.Service.UpdateState(userID, mediaType, id, body.Watched, body.Progress)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, watchlist.ErrUserIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, watchlist.ErrIdentifierRequired):
			status = http.StatusBadRequest
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

func (h *WatchlistHandler) Remove(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	mediaType := vars["mediaType"]
	id := vars["id"]

	removed, err := h.Service.Remove(userID, mediaType, id)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, watchlist.ErrUserIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, watchlist.ErrIdentifierRequired):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	if !removed {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchlistHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *WatchlistHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
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

// BackfillTextPosters is a one-time startup task that fills in TextPosterURL
// for existing watchlist items that are missing it, using the metadata cache.
// It only writes back items that actually get enriched.
func (h *WatchlistHandler) BackfillTextPosters(userIDs []string) {
	if h.MetadataService == nil {
		return
	}
	updated := 0
	for _, userID := range userIDs {
		items, err := h.Service.List(userID)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.TextPosterURL != "" {
				continue // already has a text poster
			}
			var tmdbID, tvdbID int64
			if v, ok := item.ExternalIDs["tmdb"]; ok {
				if id, err := strconv.ParseInt(v, 10, 64); err == nil {
					tmdbID = id
				}
			}
			if v, ok := item.ExternalIDs["tvdb"]; ok {
				if id, err := strconv.ParseInt(v, 10, 64); err == nil {
					tvdbID = id
				}
			}
			if tmdbID == 0 && tvdbID == 0 {
				continue
			}
			url := h.MetadataService.GetTextPosterURL(item.MediaType, tmdbID, tvdbID)
			if url == "" {
				continue
			}
			// Write back the enriched text poster URL
			h.Service.AddOrUpdate(userID, models.WatchlistUpsert{
				ID:            item.ID,
				MediaType:     item.MediaType,
				Name:          item.Name,
				TextPosterURL: url,
				ExternalIDs:   item.ExternalIDs,
			})
			updated++
		}
	}
	if updated > 0 {
		log.Printf("[watchlist] backfilled text poster URLs for %d items", updated)
	}
}

// enrichWatchlistArtwork refreshes artwork URLs from the metadata cache when
// available, falling back to persisted values in the DB record. This is a fast,
// cache-only operation with no API calls.
func enrichWatchlistArtwork(items []models.WatchlistItem, meta metadataService) {
	if meta == nil {
		return
	}
	for i := range items {
		var tmdbID, tvdbID int64
		if v, ok := items[i].ExternalIDs["tmdb"]; ok {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				tmdbID = id
			}
		}
		if v, ok := items[i].ExternalIDs["tvdb"]; ok {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				tvdbID = id
			}
		}
		if tmdbID > 0 || tvdbID > 0 {
			if isPlaceholderOverview(items[i].Overview) {
				if overview := cleanCachedOverview(meta.GetCachedOverview(items[i].MediaType, tmdbID, tvdbID)); overview != "" {
					items[i].Overview = overview
				}
			}

			textPosterURL, textBackdropURL, backdropURLs := meta.GetCachedArtworkURLs(items[i].MediaType, tmdbID, tvdbID)
			if textPosterURL != "" {
				items[i].TextPosterURL = textPosterURL
			}
			if textBackdropURL != "" {
				items[i].TextBackdropURL = textBackdropURL
			}
			if len(backdropURLs) > 0 {
				items[i].BackdropURLs = backdropURLs
			}
		}
		// If cache misses, persisted artwork URLs from the DB record are kept as-is.
	}
}

func isPlaceholderOverview(overview string) bool {
	trimmed := strings.TrimSpace(overview)
	return trimmed == "" || strings.EqualFold(trimmed, "No description available")
}

func cleanCachedOverview(overview string) string {
	trimmed := strings.TrimSpace(overview)
	if trimmed == "" || strings.EqualFold(trimmed, "No description available") {
		return ""
	}
	return trimmed
}

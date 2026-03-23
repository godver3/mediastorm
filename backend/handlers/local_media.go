package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/localmedia"

	"github.com/gorilla/mux"
)

type localMediaService interface {
	GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error)
}

type localMediaUsersProvider interface {
	BelongsToAccount(profileID, accountID string) bool
}

type LocalMediaHandler struct {
	service         localMediaService
	usersSvc        localMediaUsersProvider
	transmuxEnabled bool
}

func NewLocalMediaHandler(service localMediaService, usersSvc localMediaUsersProvider, transmuxEnabled bool) *LocalMediaHandler {
	return &LocalMediaHandler{
		service:         service,
		usersSvc:        usersSvc,
		transmuxEnabled: transmuxEnabled,
	}
}

func (h *LocalMediaHandler) GetPlayback(w http.ResponseWriter, r *http.Request) {
	itemID := strings.TrimSpace(mux.Vars(r)["itemID"])
	if itemID == "" {
		http.Error(w, "missing item ID", http.StatusBadRequest)
		return
	}
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}

	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID != "" && !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		if accountID == "" || h.usersSvc == nil || !h.usersSvc.BelongsToAccount(profileID, accountID) {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
	}

	item, err := h.service.GetItem(r.Context(), itemID)
	if err != nil {
		switch {
		case errors.Is(err, localmedia.ErrItemNotFound), errors.Is(err, localmedia.ErrLibraryNotFound):
			http.Error(w, "local media item not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	streamPath := localmedia.BuildStreamPath(*item)
	query := url.Values{}
	query.Set("path", streamPath)
	if profileID != "" {
		query.Set("profileId", profileID)
	}

	displayName := strings.TrimSpace(item.MatchedName)
	if displayName == "" {
		displayName = strings.TrimSpace(item.DetectedTitle)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(item.FileName)
	}
	if displayName == "" {
		displayName = filepath.Base(strings.TrimSpace(item.FilePath))
	}
	if displayName != "" {
		query.Set("displayName", displayName)
	}

	resp := models.LocalMediaPlaybackResponse{
		ItemID:       item.ID,
		FileName:     item.FileName,
		DisplayName:  displayName,
		StreamPath:   streamPath,
		StreamURL:    "/api/video/stream?" + query.Encode(),
		DirectStream: true,
	}
	if h.transmuxEnabled {
		resp.HLSAvailable = true
		resp.HLSStartURL = "/api/video/hls/start?" + query.Encode()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

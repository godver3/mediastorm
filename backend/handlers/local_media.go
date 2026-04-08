package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/localmedia"

	"github.com/gorilla/mux"
)

type localMediaService interface {
	GetItem(ctx context.Context, itemID string) (*models.LocalMediaItem, error)
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
	ListGroups(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaGroupListResult, error)
	FindMatches(ctx context.Context, query models.LocalMediaMatchQuery) ([]models.LocalMediaMatchedGroup, error)
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

func (h *LocalMediaHandler) ListLibraries(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}
	libraries, err := h.service.ListLibraries(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if libraries == nil {
		libraries = []models.LocalMediaLibrary{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(libraries)
}

func (h *LocalMediaHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	libraryID := strings.TrimSpace(mux.Vars(r)["libraryID"])
	if libraryID == "" {
		http.Error(w, "missing library ID", http.StatusBadRequest)
		return
	}
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	offset, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("offset")))
	log.Printf("[localmedia] ListGroups: libraryID=%s limit=%d offset=%d filter=%q sort=%q", libraryID, limit, offset, r.URL.Query().Get("filter"), r.URL.Query().Get("sort"))
	t0 := time.Now()
	groups, err := h.service.ListGroups(r.Context(), libraryID, models.LocalMediaItemListQuery{
		Filter: r.URL.Query().Get("filter"),
		Sort:   r.URL.Query().Get("sort"),
		Dir:    r.URL.Query().Get("dir"),
		Query:  r.URL.Query().Get("query"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		log.Printf("[localmedia] ListGroups: libraryID=%s error after %s: %v", libraryID, time.Since(t0).Round(time.Millisecond), err)
		switch {
		case errors.Is(err, localmedia.ErrLibraryNotFound):
			http.Error(w, "library not found", http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if groups == nil {
		groups = &models.LocalMediaGroupListResult{Groups: []models.LocalMediaItemGroup{}}
	}
	log.Printf("[localmedia] ListGroups: libraryID=%s returned %d groups (total=%d) in %s", libraryID, len(groups.Groups), groups.Total, time.Since(t0).Round(time.Millisecond))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(groups)
}

func (h *LocalMediaHandler) FindMatches(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "local media service unavailable", http.StatusServiceUnavailable)
		return
	}

	year, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("year")))
	matches, err := h.service.FindMatches(r.Context(), models.LocalMediaMatchQuery{
		MediaType: strings.TrimSpace(r.URL.Query().Get("mediaType")),
		TitleID:   strings.TrimSpace(r.URL.Query().Get("titleId")),
		Title:     strings.TrimSpace(r.URL.Query().Get("title")),
		Year:      year,
		IMDBID:    strings.TrimSpace(r.URL.Query().Get("imdbId")),
		TMDBID:    strings.TrimSpace(r.URL.Query().Get("tmdbId")),
		TVDBID:    strings.TrimSpace(r.URL.Query().Get("tvdbId")),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if matches == nil {
		matches = []models.LocalMediaMatchedGroup{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(matches)
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
	query.Set("transmux", "0")
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

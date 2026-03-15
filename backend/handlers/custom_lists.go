package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"novastream/models"
	"novastream/services/customlists"

	"github.com/gorilla/mux"
)

type customListsService interface {
	ListLists(userID string) ([]models.CustomList, error)
	CreateList(userID, name string) (models.CustomList, error)
	RenameList(userID, listID, name string) (models.CustomList, error)
	DeleteList(userID, listID string) (bool, error)
	ListItems(userID, listID string) ([]models.WatchlistItem, error)
	AddItem(userID, listID string, input models.WatchlistUpsert) (models.WatchlistItem, error)
	RemoveItem(userID, listID, mediaType, id string) (bool, error)
}

var _ customListsService = (*customlists.Service)(nil)

type CustomListsHandler struct {
	Service         customListsService
	Users           userService
	HistoryService  historyService
	MetadataService metadataService
}

func NewCustomListsHandler(service customListsService, users userService) *CustomListsHandler {
	return &CustomListsHandler{Service: service, Users: users}
}

func (h *CustomListsHandler) SetHistoryService(service historyService) {
	h.HistoryService = service
}

// SetMetadataService sets the metadata service for rating enrichment on list item responses.
func (h *CustomListsHandler) SetMetadataService(service metadataService) {
	h.MetadataService = service
}

func (h *CustomListsHandler) ListLists(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	lists, err := h.Service.ListLists(userID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, customlists.ErrUserIDRequired) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(lists)
}

func (h *CustomListsHandler) CreateList(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	list, err := h.Service.CreateList(userID, body.Name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListNameRequired):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(list)
}

func (h *CustomListsHandler) RenameList(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	listID := strings.TrimSpace(mux.Vars(r)["listID"])
	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	list, err := h.Service.RenameList(userID, listID, body.Name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListIDRequired), errors.Is(err, customlists.ErrListNameRequired):
			status = http.StatusBadRequest
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func (h *CustomListsHandler) DeleteList(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	listID := strings.TrimSpace(mux.Vars(r)["listID"])
	removed, err := h.Service.DeleteList(userID, listID)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListIDRequired):
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

func (h *CustomListsHandler) ListItems(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	listID := strings.TrimSpace(mux.Vars(r)["listID"])
	items, err := h.Service.ListItems(userID, listID)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListIDRequired):
			status = http.StatusBadRequest
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *CustomListsHandler) AddItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	listID := strings.TrimSpace(mux.Vars(r)["listID"])
	var body models.WatchlistUpsert
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	item, err := h.Service.AddItem(userID, listID, body)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListIDRequired), errors.Is(err, customlists.ErrIDRequired), errors.Is(err, customlists.ErrMediaTypeRequired):
			status = http.StatusBadRequest
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(item)
}

func (h *CustomListsHandler) RemoveItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	listID := strings.TrimSpace(vars["listID"])
	mediaType := strings.TrimSpace(vars["mediaType"])
	id := strings.TrimSpace(vars["id"])

	removed, err := h.Service.RemoveItem(userID, listID, mediaType, id)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, customlists.ErrUserIDRequired), errors.Is(err, customlists.ErrListIDRequired), errors.Is(err, customlists.ErrIdentifierRequired):
			status = http.StatusBadRequest
		case errors.Is(err, os.ErrNotExist):
			status = http.StatusNotFound
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

func (h *CustomListsHandler) Options(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *CustomListsHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
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

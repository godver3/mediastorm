package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"novastream/models"
	content_preferences "novastream/services/content_preferences"

	"github.com/gorilla/mux"
)

type contentPreferencesService interface {
	Get(userID, contentID string) (*models.ContentPreference, error)
	Set(userID string, pref models.ContentPreference) error
	Delete(userID, contentID string) error
	List(userID string) ([]models.ContentPreference, error)
}

var _ contentPreferencesService = (*content_preferences.Service)(nil)

type ContentPreferencesHandler struct {
	Service contentPreferencesService
	Users   userService
}

func NewContentPreferencesHandler(service contentPreferencesService, users userService) *ContentPreferencesHandler {
	return &ContentPreferencesHandler{
		Service: service,
		Users:   users,
	}
}

// GetPreference returns the content preference for a specific content item.
func (h *ContentPreferencesHandler) GetPreference(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	contentID := strings.TrimSpace(vars["contentID"])
	if contentID == "" {
		http.Error(w, "content id is required", http.StatusBadRequest)
		return
	}

	pref, err := h.Service.Get(userID, contentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if pref == nil {
		// Return empty object if no preference set
		json.NewEncoder(w).Encode(map[string]interface{}{})
		return
	}
	json.NewEncoder(w).Encode(pref)
}

// SetPreference creates or updates a content preference.
func (h *ContentPreferencesHandler) SetPreference(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var pref models.ContentPreference
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&pref); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if pref.ContentID == "" {
		http.Error(w, "content id is required", http.StatusBadRequest)
		return
	}

	if err := h.Service.Set(userID, pref); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(pref)
}

// DeletePreference removes a content preference.
func (h *ContentPreferencesHandler) DeletePreference(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	vars := mux.Vars(r)
	contentID := strings.TrimSpace(vars["contentID"])
	if contentID == "" {
		http.Error(w, "content id is required", http.StatusBadRequest)
		return
	}

	if err := h.Service.Delete(userID, contentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListPreferences returns all content preferences for a user.
func (h *ContentPreferencesHandler) ListPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	prefs, err := h.Service.List(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prefs)
}

func (h *ContentPreferencesHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *ContentPreferencesHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
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

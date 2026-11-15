package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	user_settings "novastream/services/user_settings"
)

// PatchSkipping changes profile preferences without replacing other overrides.
func (h *UserSettingsHandler) PatchSkipping(w http.ResponseWriter, r *http.Request) {
	id, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if auth.IsShareLinkRequest(r) {
		http.Error(w, "a profile session is required", http.StatusForbidden)
		return
	}
	if session, ok := auth.GetSession(r); ok && session.Scope == models.SessionScopeStream {
		http.Error(w, "a profile session is required", http.StatusForbidden)
		return
	}
	if !profileaccess.CheckResource(w, r, id, "") {
		return
	}
	// ProfileOwnershipMiddleware on this route already rejects other
	// households' profiles while letting the master account through.
	var patch user_settings.SkipPreferences
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		http.Error(w, "invalid skip preferences", http.StatusBadRequest)
		return
	}
	if decoder.Decode(new(interface{})) != io.EOF || (patch.Intro == nil && patch.Recap == nil && patch.Credits == nil && patch.NextEpisode == nil) {
		http.Error(w, "provide at least one skip preference", http.StatusBadRequest)
		return
	}
	service, ok := h.Service.(interface {
		UpdateSkipping(string, user_settings.SkipPreferences) error
	})
	if !ok {
		http.Error(w, "skip preference storage unavailable", http.StatusInternalServerError)
		return
	}
	if err := service.UpdateSkipping(id, patch); err != nil {
		http.Error(w, "could not save skip preferences", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(patch)
}

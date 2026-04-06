package handlers

import (
	"encoding/json"
	"net/http"

	"novastream/config"
)

// LocaleHandler returns the global app language setting for unauthenticated clients
// (e.g. the login screen, which renders before any auth takes place).
type LocaleHandler struct {
	cfgManager *config.Manager
}

func NewLocaleHandler(cfgManager *config.Manager) *LocaleHandler {
	return &LocaleHandler{cfgManager: cfgManager}
}

type LocaleResponse struct {
	// AppLanguage is an ISO 639-1 code (e.g. "en", "fr"), or "" to use device locale.
	AppLanguage string `json:"appLanguage"`
}

func (h *LocaleHandler) GetLocale(w http.ResponseWriter, r *http.Request) {
	settings, err := h.cfgManager.Load()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LocaleResponse{
		AppLanguage: settings.Display.AppLanguage,
	})
}

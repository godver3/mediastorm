package handlers

import (
	"encoding/json"
	"net/http"

	"novastream/config"
)

type SettingsHandler struct {
	Manager  *config.Manager
	DemoMode bool
}

func NewSettingsHandler(m *config.Manager) *SettingsHandler {
	return &SettingsHandler{Manager: m, DemoMode: false}
}

func NewSettingsHandlerWithDemoMode(m *config.Manager, demoMode bool) *SettingsHandler {
	return &SettingsHandler{Manager: m, DemoMode: demoMode}
}

// SettingsResponse wraps config.Settings with additional runtime information.
type SettingsResponse struct {
	config.Settings
	DemoMode bool `json:"demoMode"`
}

func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.Manager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	resp := SettingsResponse{
		Settings: s,
		DemoMode: h.DemoMode,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *SettingsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	var s config.Settings
	dec := json.NewDecoder(r.Body)
	// Allow unknown fields for backward compatibility with old configs
	if err := dec.Decode(&s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := h.Manager.Save(s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(s)
}

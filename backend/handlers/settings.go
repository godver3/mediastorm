package handlers

import (
	"encoding/json"
	"log"
	"net/http"

	"novastream/config"
	"novastream/internal/pool"
	"novastream/services/debrid"
	"novastream/services/metadata"
)

type SettingsHandler struct {
	Manager            *config.Manager
	DemoMode           bool
	PoolManager        pool.Manager
	MetadataService    *metadata.Service
	DebridSearchService *debrid.SearchService
}

func NewSettingsHandler(m *config.Manager) *SettingsHandler {
	return &SettingsHandler{Manager: m, DemoMode: false}
}

func NewSettingsHandlerWithDemoMode(m *config.Manager, demoMode bool) *SettingsHandler {
	return &SettingsHandler{Manager: m, DemoMode: demoMode}
}

// SetPoolManager sets the pool manager for hot reloading usenet providers
func (h *SettingsHandler) SetPoolManager(pm pool.Manager) {
	h.PoolManager = pm
}

// SetMetadataService sets the metadata service for hot reloading API keys
func (h *SettingsHandler) SetMetadataService(ms *metadata.Service) {
	h.MetadataService = ms
}

// SetDebridSearchService sets the debrid search service for hot reloading scrapers
func (h *SettingsHandler) SetDebridSearchService(ds *debrid.SearchService) {
	h.DebridSearchService = ds
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

	// Hot reload services that need it
	h.reloadServices(s)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(s)
}

// reloadServices reloads services that cache configuration at startup
func (h *SettingsHandler) reloadServices(s config.Settings) {
	// Reload NNTP connection pool with new usenet providers
	if h.PoolManager != nil {
		providers := config.ToNNTPProviders(s.Usenet)
		if err := h.PoolManager.SetProviders(providers); err != nil {
			log.Printf("[settings] failed to reload usenet pool: %v", err)
		} else {
			log.Printf("[settings] reloaded usenet pool with %d provider(s)", len(providers))
		}
	}

	// Reload metadata service with new API keys
	if h.MetadataService != nil {
		h.MetadataService.UpdateAPIKeys(s.Metadata.TVDBAPIKey, s.Metadata.TMDBAPIKey, s.Metadata.Language)
		log.Printf("[settings] reloaded metadata service API keys")
	}

	// Reload debrid scrapers (Torrentio, Jackett, etc.)
	if h.DebridSearchService != nil {
		h.DebridSearchService.ReloadScrapers()
	}
}

// ClearMetadataCache clears all cached metadata files
func (h *SettingsHandler) ClearMetadataCache(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.MetadataService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "metadata service not available"})
		return
	}
	if err := h.MetadataService.ClearCache(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[settings] metadata cache cleared by user request")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Metadata cache cleared"})
}

package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/pool"
	"novastream/services/debrid"
	"novastream/services/epg"
	"novastream/services/metadata"
	user_settings "novastream/services/user_settings"
)

// PrequeueClearer can clear all prequeue entries.
type PrequeueClearer interface {
	DeleteAll()
}

type SettingsHandler struct {
	Manager              *config.Manager
	DemoMode             bool
	PoolManager          pool.Manager
	MetadataService      *metadata.Service
	DebridSearchService  *debrid.SearchService
	ImageHandler         *ImageHandler
	EPGService           *epg.Service
	UserSettingsService  *user_settings.Service
	ClientsLister        user_settings.ClientsLister
	ClientSettingsBatch  user_settings.ClientSettingsBatch
	PrequeueStore        PrequeueClearer
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

// SetImageHandler sets the image handler for clearing image cache
func (h *SettingsHandler) SetImageHandler(ih *ImageHandler) {
	h.ImageHandler = ih
}

// SetEPGService sets the EPG service for auto-refresh when new sources are added
func (h *SettingsHandler) SetEPGService(es *epg.Service) {
	h.EPGService = es
}

// SetUserSettingsService sets the user settings service for stripping redundant overrides
func (h *SettingsHandler) SetUserSettingsService(us *user_settings.Service) {
	h.UserSettingsService = us
}

// SetClientsLister sets the clients lister for client→profile mapping
func (h *SettingsHandler) SetClientsLister(cl user_settings.ClientsLister) {
	h.ClientsLister = cl
}

// SetClientSettingsBatch sets the client settings batch service for stripping redundant overrides
func (h *SettingsHandler) SetClientSettingsBatch(cs user_settings.ClientSettingsBatch) {
	h.ClientSettingsBatch = cs
}

// SetPrequeueStore sets the prequeue store for clearing cache when display settings change
func (h *SettingsHandler) SetPrequeueStore(ps PrequeueClearer) {
	h.PrequeueStore = ps
}

// SettingsResponse wraps config.Settings with additional runtime information.
type SettingsResponse struct {
	config.Settings
	DemoMode bool `json:"demoMode"`
}

// LiveSettingsWithEffectiveURL wraps LiveSettings with a computed effective URL.
type LiveSettingsWithEffectiveURL struct {
	config.LiveSettings
	EffectivePlaylistURL string `json:"effectivePlaylistUrl,omitempty"`
}

// SettingsResponseWithLive extends SettingsResponse with computed live URL.
type SettingsResponseWithLive struct {
	SettingsResponse
	Live LiveSettingsWithEffectiveURL `json:"live"`
}

func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.Manager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Redact sensitive fields for non-master users
	isMaster, _ := r.Context().Value(auth.ContextKeyIsMaster).(bool)
	if !isMaster {
		redactSettings(&s)
	}

	// Build response with computed effective playlist URL
	resp := SettingsResponseWithLive{
		SettingsResponse: SettingsResponse{
			Settings: s,
			DemoMode: h.DemoMode,
		},
		Live: LiveSettingsWithEffectiveURL{
			LiveSettings:         s.Live,
			EffectivePlaylistURL: s.Live.GetEffectivePlaylistURL(),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// redactSettings replaces sensitive credentials with a placeholder so
// non-master users cannot read API keys, passwords, or tokens.
func redactSettings(s *config.Settings) {
	const redacted = "••••••••"
	mask := func(v *string) {
		if *v != "" {
			*v = redacted
		}
	}

	// Server
	mask(&s.Server.HomepageAPIKey)

	// Usenet providers
	for i := range s.Usenet {
		mask(&s.Usenet[i].Password)
	}

	// Indexers (Newznab/Torznab)
	for i := range s.Indexers {
		mask(&s.Indexers[i].APIKey)
	}

	// Torrent scrapers (Prowlarr/Jackett)
	for i := range s.TorrentScrapers {
		mask(&s.TorrentScrapers[i].APIKey)
	}

	// Metadata API keys
	mask(&s.Metadata.TVDBAPIKey)
	mask(&s.Metadata.TMDBAPIKey)
	mask(&s.Metadata.GeminiAPIKey)

	// WebDAV
	mask(&s.WebDAV.Password)

	// Debrid providers
	for i := range s.Streaming.DebridProviders {
		mask(&s.Streaming.DebridProviders[i].APIKey)
	}

	// SABnzbd
	mask(&s.SABnzbd.FallbackAPIKey)

	// Subtitles
	mask(&s.Subtitles.OpenSubtitlesPassword)

	// MDBList
	mask(&s.MDBList.APIKey)

	// Trakt
	mask(&s.Trakt.ClientSecret)
	mask(&s.Trakt.AccessToken)
	mask(&s.Trakt.RefreshToken)

	// Plex
	mask(&s.Plex.AuthToken)

	// Live (Xtream)
	mask(&s.Live.XtreamPassword)
}

const redactedPlaceholder = "••••••••"

// preserveRedactedFields restores the real credential from existing settings
// whenever the incoming value equals the redaction placeholder. This prevents
// save-back of redacted values from overwriting real secrets.
func preserveRedactedFields(incoming *config.Settings, existing *config.Settings) {
	restore := func(newVal *string, oldVal string) {
		if *newVal == redactedPlaceholder {
			*newVal = oldVal
		}
	}

	// Server
	restore(&incoming.Server.HomepageAPIKey, existing.Server.HomepageAPIKey)

	// Usenet providers (match by index — frontend preserves order)
	for i := range incoming.Usenet {
		if i < len(existing.Usenet) {
			restore(&incoming.Usenet[i].Password, existing.Usenet[i].Password)
		}
	}

	// Indexers
	for i := range incoming.Indexers {
		if i < len(existing.Indexers) {
			restore(&incoming.Indexers[i].APIKey, existing.Indexers[i].APIKey)
		}
	}

	// Torrent scrapers
	for i := range incoming.TorrentScrapers {
		if i < len(existing.TorrentScrapers) {
			restore(&incoming.TorrentScrapers[i].APIKey, existing.TorrentScrapers[i].APIKey)
		}
	}

	// Metadata
	restore(&incoming.Metadata.TVDBAPIKey, existing.Metadata.TVDBAPIKey)
	restore(&incoming.Metadata.TMDBAPIKey, existing.Metadata.TMDBAPIKey)
	restore(&incoming.Metadata.GeminiAPIKey, existing.Metadata.GeminiAPIKey)

	// WebDAV
	restore(&incoming.WebDAV.Password, existing.WebDAV.Password)

	// Debrid providers
	for i := range incoming.Streaming.DebridProviders {
		if i < len(existing.Streaming.DebridProviders) {
			restore(&incoming.Streaming.DebridProviders[i].APIKey, existing.Streaming.DebridProviders[i].APIKey)
		}
	}

	// SABnzbd
	restore(&incoming.SABnzbd.FallbackAPIKey, existing.SABnzbd.FallbackAPIKey)

	// Subtitles
	restore(&incoming.Subtitles.OpenSubtitlesPassword, existing.Subtitles.OpenSubtitlesPassword)

	// MDBList
	restore(&incoming.MDBList.APIKey, existing.MDBList.APIKey)

	// Trakt
	restore(&incoming.Trakt.ClientSecret, existing.Trakt.ClientSecret)
	restore(&incoming.Trakt.AccessToken, existing.Trakt.AccessToken)
	restore(&incoming.Trakt.RefreshToken, existing.Trakt.RefreshToken)

	// Plex
	restore(&incoming.Plex.AuthToken, existing.Plex.AuthToken)

	// Live (Xtream)
	restore(&incoming.Live.XtreamPassword, existing.Live.XtreamPassword)
}

func (h *SettingsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	// Load old settings to detect new EPG sources
	oldSettings, _ := h.Manager.Load()

	var s config.Settings
	dec := json.NewDecoder(r.Body)
	// Allow unknown fields for backward compatibility with old configs
	if err := dec.Decode(&s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Restore real credentials when the incoming value is the redaction placeholder.
	// This prevents non-master users from accidentally overwriting secrets when they
	// save settings that were returned with redacted values.
	preserveRedactedFields(&s, &oldSettings)

	// Auto-create/remove scheduled tasks based on feature settings
	h.ensureEPGTaskIfEnabled(&s)
	h.ensurePlaylistTaskIfConfigured(&s)

	if err := h.Manager.Save(s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Clear prequeue cache when ShowParsedBadges changes (affects badge display in cached entries)
	if h.PrequeueStore != nil && oldSettings.Display.ShowParsedBadges != s.Display.ShowParsedBadges {
		log.Printf("[settings] ShowParsedBadges changed from %v to %v, clearing prequeue cache", oldSettings.Display.ShowParsedBadges, s.Display.ShowParsedBadges)
		h.PrequeueStore.DeleteAll()
	}

	// Strip redundant per-profile and per-client overrides
	if h.UserSettingsService != nil {
		go h.UserSettingsService.StripRedundantOverrides(s, h.ClientsLister, h.ClientSettingsBatch)
	}

	// Hot reload services that need it
	h.reloadServices(s)

	// Auto-refresh EPG if new sources were added
	h.triggerEPGRefreshIfNewSources(oldSettings, s)

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
		h.MetadataService.UpdateAPIKeys(s.Metadata.TVDBAPIKey, s.Metadata.TMDBAPIKey, s.Metadata.Language, s.Metadata.GeminiAPIKey)
		log.Printf("[settings] reloaded metadata service API keys")

		// Reload MDBList settings (rating sources, API key, enabled state)
		h.MetadataService.UpdateMDBListSettings(metadata.MDBListConfig{
			APIKey:         s.MDBList.APIKey,
			Enabled:        s.MDBList.Enabled,
			EnabledRatings: s.MDBList.EnabledRatings,
		})
		log.Printf("[settings] reloaded MDBList settings (enabled=%v, ratings=%v)", s.MDBList.Enabled, s.MDBList.EnabledRatings)
	}

	// Reload debrid scrapers (Torrentio, Jackett, etc.)
	if h.DebridSearchService != nil {
		h.DebridSearchService.ReloadScrapers()
	}
}

// ClearMetadataCache clears all cached metadata files and images
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

	// Also clear image cache if available
	if h.ImageHandler != nil {
		if err := h.ImageHandler.ClearCache(); err != nil {
			log.Printf("[settings] warning: failed to clear image cache: %v", err)
		} else {
			log.Printf("[settings] image cache cleared by user request")
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Metadata and image cache cleared"})
}

// ensureEPGTaskIfEnabled auto-creates an EPG refresh task when EPG is enabled
// and no EPG refresh task already exists. Removes auto-created EPG tasks when disabled.
func (h *SettingsHandler) ensureEPGTaskIfEnabled(s *config.Settings) {
	if !s.Live.EPG.Enabled {
		// EPG is disabled - remove any auto-created EPG refresh tasks
		filtered := s.ScheduledTasks.Tasks[:0]
		for _, task := range s.ScheduledTasks.Tasks {
			// Only remove auto-created EPG tasks (ID contains "auto")
			if task.Type == config.ScheduledTaskTypeEPGRefresh && strings.Contains(task.ID, "auto") {
				log.Printf("[settings] removing auto-created EPG refresh task (id=%s) because EPG is disabled", task.ID)
				continue
			}
			filtered = append(filtered, task)
		}
		s.ScheduledTasks.Tasks = filtered
		return
	}

	// Check if an EPG refresh task already exists
	for _, task := range s.ScheduledTasks.Tasks {
		if task.Type == config.ScheduledTaskTypeEPGRefresh {
			return // Task already exists
		}
	}

	// Create default EPG refresh task
	epgTask := config.ScheduledTask{
		ID:         "auto-epg-refresh",
		Type:       config.ScheduledTaskTypeEPGRefresh,
		Name:       "EPG Refresh",
		Enabled:    true,
		Frequency:  config.ScheduledTaskFrequency12Hours,
		Config:     map[string]string{},
		LastStatus: config.ScheduledTaskStatusPending,
		CreatedAt:  time.Now(),
	}
	s.ScheduledTasks.Tasks = append(s.ScheduledTasks.Tasks, epgTask)
	log.Printf("[settings] auto-created EPG refresh task because EPG is enabled")
}

// ensurePlaylistTaskIfConfigured auto-creates a playlist refresh task when Live TV is configured
// and no playlist refresh task already exists. Removes auto-created playlist tasks when unconfigured.
func (h *SettingsHandler) ensurePlaylistTaskIfConfigured(s *config.Settings) {
	// Check if Live TV is configured based on the currently selected mode only
	var liveTVConfigured bool
	switch s.Live.Mode {
	case "m3u":
		liveTVConfigured = strings.TrimSpace(s.Live.PlaylistURL) != ""
	case "xtream":
		liveTVConfigured = strings.TrimSpace(s.Live.XtreamHost) != "" &&
			strings.TrimSpace(s.Live.XtreamUsername) != "" &&
			strings.TrimSpace(s.Live.XtreamPassword) != ""
	default:
		liveTVConfigured = false
	}

	if !liveTVConfigured {
		// Live TV is not configured - remove any auto-created playlist refresh tasks
		filtered := s.ScheduledTasks.Tasks[:0]
		for _, task := range s.ScheduledTasks.Tasks {
			// Only remove auto-created playlist tasks (ID contains "auto")
			if task.Type == config.ScheduledTaskTypePlaylistRefresh && strings.Contains(task.ID, "auto") {
				log.Printf("[settings] removing auto-created playlist refresh task (id=%s) because Live TV is not configured", task.ID)
				continue
			}
			filtered = append(filtered, task)
		}
		s.ScheduledTasks.Tasks = filtered
		return
	}

	// Check if a playlist refresh task already exists
	for _, task := range s.ScheduledTasks.Tasks {
		if task.Type == config.ScheduledTaskTypePlaylistRefresh {
			return // Task already exists
		}
	}

	// Create default playlist refresh task
	playlistTask := config.ScheduledTask{
		ID:         "auto-playlist-refresh",
		Type:       config.ScheduledTaskTypePlaylistRefresh,
		Name:       "Live TV Playlist Refresh",
		Enabled:    true,
		Frequency:  config.ScheduledTaskFrequencyDaily,
		Config:     map[string]string{},
		LastStatus: config.ScheduledTaskStatusPending,
		CreatedAt:  time.Now(),
	}
	s.ScheduledTasks.Tasks = append(s.ScheduledTasks.Tasks, playlistTask)
	log.Printf("[settings] auto-created playlist refresh task because Live TV is configured")
}

// triggerEPGRefreshIfNewSources triggers an immediate EPG refresh if new sources were added.
// This provides a better UX so users don't have to wait for the scheduled task to run.
func (h *SettingsHandler) triggerEPGRefreshIfNewSources(oldSettings config.Settings, newSettings config.Settings) {
	// Skip if EPG service not available or EPG not enabled
	if h.EPGService == nil || !newSettings.Live.EPG.Enabled {
		return
	}

	// Build a set of old source IDs
	oldSourceIDs := make(map[string]bool)
	for _, src := range oldSettings.Live.EPG.Sources {
		oldSourceIDs[src.ID] = true
	}

	// Check for new sources
	hasNewSources := false
	for _, src := range newSettings.Live.EPG.Sources {
		if src.Enabled && !oldSourceIDs[src.ID] {
			hasNewSources = true
			log.Printf("[settings] detected new EPG source: %s (%s)", src.Name, src.ID)
			break
		}
	}

	// Also check if EPG was just enabled (and has sources or a simple URL configured)
	epgJustEnabled := !oldSettings.Live.EPG.Enabled && newSettings.Live.EPG.Enabled
	hasEPGConfig := len(newSettings.Live.EPG.Sources) > 0 || newSettings.Live.EPG.XmltvUrl != "" ||
		(newSettings.Live.Mode == "xtream" && newSettings.Live.XtreamHost != "")

	if (hasNewSources || (epgJustEnabled && hasEPGConfig)) {
		log.Printf("[settings] triggering immediate EPG refresh due to new configuration")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := h.EPGService.Refresh(ctx); err != nil {
				log.Printf("[settings] auto EPG refresh failed: %v", err)
			} else {
				log.Printf("[settings] auto EPG refresh completed successfully")
			}
		}()
	}
}

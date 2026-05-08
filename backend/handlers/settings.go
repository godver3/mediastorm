package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"novastream/config"
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

func shouldClearPrequeueForGlobalSettingsChange(oldSettings, newSettings config.Settings) bool {
	if oldSettings.Display.ShowParsedBadges != newSettings.Display.ShowParsedBadges {
		return true
	}
	if !reflect.DeepEqual(oldSettings.Filtering, newSettings.Filtering) {
		return true
	}
	if !reflect.DeepEqual(oldSettings.Ranking, newSettings.Ranking) {
		return true
	}
	return false
}

type SettingsHandler struct {
	Manager             *config.Manager
	DemoMode            bool
	PoolManager         pool.Manager
	MetadataService     *metadata.Service
	DebridSearchService *debrid.SearchService
	ImageHandler        *ImageHandler
	EPGService          *epg.Service
	UserSettingsService *user_settings.Service
	ClientsLister       user_settings.ClientsLister
	ClientSettingsBatch user_settings.ClientSettingsBatch
	PrequeueStore       PrequeueClearer
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

	// Always redact credentials — secrets are write-only, never sent back to any client
	redactSettings(&s)

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

	// Trakt (legacy fields + account-level tokens)
	mask(&s.Trakt.ClientSecret)
	mask(&s.Trakt.AccessToken)
	mask(&s.Trakt.RefreshToken)
	for i := range s.Trakt.Accounts {
		mask(&s.Trakt.Accounts[i].ClientSecret)
		mask(&s.Trakt.Accounts[i].AccessToken)
		mask(&s.Trakt.Accounts[i].RefreshToken)
	}

	// Plex (legacy field + account-level tokens)
	mask(&s.Plex.AuthToken)
	for i := range s.Plex.Accounts {
		mask(&s.Plex.Accounts[i].AuthToken)
	}

	// Jellyfin account tokens
	for i := range s.Jellyfin.Accounts {
		mask(&s.Jellyfin.Accounts[i].Token)
	}

	// Live (Xtream)
	mask(&s.Live.XtreamPassword)

	// Database URL (may contain credentials in the connection string)
	mask(&s.Database.URL)
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

	// Trakt (legacy fields + account-level tokens)
	restore(&incoming.Trakt.ClientSecret, existing.Trakt.ClientSecret)
	restore(&incoming.Trakt.AccessToken, existing.Trakt.AccessToken)
	restore(&incoming.Trakt.RefreshToken, existing.Trakt.RefreshToken)
	for i := range incoming.Trakt.Accounts {
		if i < len(existing.Trakt.Accounts) {
			restore(&incoming.Trakt.Accounts[i].ClientSecret, existing.Trakt.Accounts[i].ClientSecret)
			restore(&incoming.Trakt.Accounts[i].AccessToken, existing.Trakt.Accounts[i].AccessToken)
			restore(&incoming.Trakt.Accounts[i].RefreshToken, existing.Trakt.Accounts[i].RefreshToken)
		}
	}

	// Plex (legacy field + account-level tokens)
	restore(&incoming.Plex.AuthToken, existing.Plex.AuthToken)
	for i := range incoming.Plex.Accounts {
		if i < len(existing.Plex.Accounts) {
			restore(&incoming.Plex.Accounts[i].AuthToken, existing.Plex.Accounts[i].AuthToken)
		}
	}

	// Jellyfin account tokens
	for i := range incoming.Jellyfin.Accounts {
		if i < len(existing.Jellyfin.Accounts) {
			restore(&incoming.Jellyfin.Accounts[i].Token, existing.Jellyfin.Accounts[i].Token)
		}
	}

	// Live (Xtream)
	restore(&incoming.Live.XtreamPassword, existing.Live.XtreamPassword)

	// Database URL
	restore(&incoming.Database.URL, existing.Database.URL)
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

	if err := expandProwlarrSources(r.Context(), &s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Auto-create/remove scheduled tasks based on feature settings
	h.ensureEPGTaskIfEnabled(&s)
	h.ensurePlaylistTaskIfConfigured(&s)

	if err := h.Manager.Save(s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Clear prequeue cache when ranking/filtering-affecting settings change.
	if h.PrequeueStore != nil && shouldClearPrequeueForGlobalSettingsChange(oldSettings, s) {
		log.Printf("[settings] ranking/filtering-related settings changed, clearing prequeue cache")
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

	// Redact credentials before returning — secrets are write-only
	redactSettings(&s)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(s)
}

type prowlarrIndexerInfo struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Enable         bool   `json:"enable"`
	SupportsSearch bool   `json:"supportsSearch"`
}

func expandProwlarrSources(ctx context.Context, s *config.Settings) error {
	cache := make(map[string][]prowlarrIndexerInfo)
	getIndexers := func(baseURL, apiKey string) ([]prowlarrIndexerInfo, error) {
		key := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "\x00" + strings.TrimSpace(apiKey)
		if indexers, ok := cache[key]; ok {
			return indexers, nil
		}
		indexers, err := fetchProwlarrIndexers(ctx, baseURL, apiKey)
		if err != nil {
			return nil, err
		}
		cache[key] = indexers
		return indexers, nil
	}

	expandedIndexers := make([]config.IndexerConfig, 0, len(s.Indexers))
	for _, idx := range s.Indexers {
		if strings.ToLower(strings.TrimSpace(idx.Type)) != "prowlarr" {
			expandedIndexers = append(expandedIndexers, idx)
			continue
		}
		if isProwlarrPerIndexerURL(idx.URL) {
			idx.Type = "newznab"
			expandedIndexers = append(expandedIndexers, idx)
			continue
		}
		indexers, err := getIndexers(idx.URL, idx.APIKey)
		if err != nil {
			return fmt.Errorf("expand Prowlarr usenet indexer %q: %w", displayName(idx.Name, "Prowlarr"), err)
		}
		added := 0
		for _, pi := range indexers {
			if !shouldUseProwlarrIndexer(pi, "usenet") {
				continue
			}
			expandedIndexers = append(expandedIndexers, config.IndexerConfig{
				Name:       prowlarrGeneratedName(idx.Name, pi.Name),
				URL:        joinProwlarrIndexerURL(idx.URL, pi.ID),
				APIKey:     idx.APIKey,
				Type:       "newznab",
				Categories: idx.Categories,
				Enabled:    idx.Enabled,
			})
			added++
		}
		if added == 0 {
			return fmt.Errorf("expand Prowlarr usenet indexer %q: no enabled usenet indexers with search support were found", displayName(idx.Name, "Prowlarr"))
		}
	}
	s.Indexers = expandedIndexers

	expandedScrapers := make([]config.TorrentScraperConfig, 0, len(s.TorrentScrapers))
	for _, scraper := range s.TorrentScrapers {
		if strings.ToLower(strings.TrimSpace(scraper.Type)) != "prowlarr" || isProwlarrPerIndexerURL(scraper.URL) {
			expandedScrapers = append(expandedScrapers, scraper)
			continue
		}
		indexers, err := getIndexers(scraper.URL, scraper.APIKey)
		if err != nil {
			return fmt.Errorf("expand Prowlarr torrent source %q: %w", displayName(scraper.Name, "Prowlarr"), err)
		}
		added := 0
		for _, pi := range indexers {
			if !shouldUseProwlarrIndexer(pi, "torrent") {
				continue
			}
			expandedScrapers = append(expandedScrapers, config.TorrentScraperConfig{
				Name:    prowlarrGeneratedName(scraper.Name, pi.Name),
				Type:    "prowlarr",
				URL:     joinProwlarrIndexerURL(scraper.URL, pi.ID),
				APIKey:  scraper.APIKey,
				Options: scraper.Options,
				Enabled: scraper.Enabled,
				Config:  scraper.Config,
			})
			added++
		}
		if added == 0 {
			return fmt.Errorf("expand Prowlarr torrent source %q: no enabled torrent indexers with search support were found", displayName(scraper.Name, "Prowlarr"))
		}
	}
	s.TorrentScrapers = expandedScrapers

	return nil
}

func fetchProwlarrIndexers(ctx context.Context, baseURL, apiKey string) ([]prowlarrIndexerInfo, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", baseURL)
	}
	u.Path = path.Join(u.Path, "/api/v1/indexer")
	u.RawQuery = ""
	u.Fragment = ""

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("Prowlarr discovery failed: %s", msg)
	}

	var indexers []prowlarrIndexerInfo
	if err := json.NewDecoder(resp.Body).Decode(&indexers); err != nil {
		return nil, fmt.Errorf("decode Prowlarr indexers: %w", err)
	}
	return indexers, nil
}

func shouldUseProwlarrIndexer(idx prowlarrIndexerInfo, protocol string) bool {
	return idx.ID > 0 &&
		idx.Enable &&
		idx.SupportsSearch &&
		strings.EqualFold(strings.TrimSpace(idx.Protocol), protocol)
}

func isProwlarrPerIndexerURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	last := parts[len(parts)-1]
	if strings.EqualFold(last, "api") && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	id, err := strconv.Atoi(last)
	return err == nil && id > 0
}

func joinProwlarrIndexerURL(baseURL string, id int) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/" + strconv.Itoa(id)
}

func prowlarrGeneratedName(prefix, indexerName string) string {
	indexerName = strings.TrimSpace(indexerName)
	if indexerName == "" {
		indexerName = "Indexer"
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.EqualFold(prefix, "prowlarr") {
		prefix = "Prowlarr"
	}
	return prefix + " - " + indexerName
}

func displayName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return strings.TrimSpace(name)
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

	if hasNewSources || (epgJustEnabled && hasEPGConfig) {
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

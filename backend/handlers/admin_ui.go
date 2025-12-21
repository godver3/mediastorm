package handlers

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
)

//go:embed admin_templates/*
var adminTemplates embed.FS

const (
	adminSessionCookieName = "strmr_admin_session"
	adminSessionDuration   = 24 * time.Hour
)

// adminSessionStore manages admin session tokens
type adminSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time // token -> expiry
}

var adminSessions = &adminSessionStore{
	sessions: make(map[string]time.Time),
}

func (s *adminSessionStore) create() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate random token
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	s.sessions[token] = time.Now().Add(adminSessionDuration)

	// Cleanup expired sessions
	now := time.Now()
	for t, exp := range s.sessions {
		if exp.Before(now) {
			delete(s.sessions, t)
		}
	}

	return token
}

func (s *adminSessionStore) validate(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	return exp.After(time.Now())
}

func (s *adminSessionStore) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// SettingsGroups defines the order and labels for settings groups
var SettingsGroups = []map[string]string{
	{"id": "server", "label": "Server"},
	{"id": "accounts", "label": "Accounts"},
	{"id": "providers", "label": "Providers"},
	{"id": "sources", "label": "Sources"},
	{"id": "experience", "label": "Experience"},
	{"id": "storage", "label": "Storage & Data"},
}

// SettingsSchema defines the schema for dynamic form generation
var SettingsSchema = map[string]interface{}{
	"server": map[string]interface{}{
		"label": "Server Settings",
		"icon":  "server",
		"group": "server",
		"order": 0,
		"fields": map[string]interface{}{
			"host": map[string]interface{}{"type": "text", "label": "Host", "description": "Server bind address"},
			"port": map[string]interface{}{"type": "number", "label": "Port", "description": "Server port"},
			"pin":  map[string]interface{}{"type": "password", "label": "PIN", "description": "6-digit authentication PIN"},
		},
	},
	"profiles": map[string]interface{}{
		"label":   "Profiles",
		"icon":    "users",
		"group":   "accounts",
		"order":   0,
		"custom":  true, // Custom rendered section
		"fields":  map[string]interface{}{},
	},
	"streaming": map[string]interface{}{
		"label": "Streaming",
		"icon":  "play-circle",
		"group": "providers",
		"order": 0,
		"fields": map[string]interface{}{
			"maxDownloadWorkers": map[string]interface{}{"type": "number", "label": "Max Download Workers", "description": "Maximum concurrent download workers"},
			"maxCacheSizeMB":     map[string]interface{}{"type": "number", "label": "Max Cache Size (MB)", "description": "Maximum cache size in megabytes"},
			"serviceMode":        map[string]interface{}{"type": "select", "label": "Service Mode", "options": []string{"usenet", "debrid", "hybrid"}, "description": "Streaming service mode"},
		},
	},
	"debridProviders": map[string]interface{}{
		"label":    "Debrid Providers",
		"icon":     "cloud",
		"group":    "providers",
		"order":    1,
		"is_array": true,
		"parent":   "streaming",
		"key":      "debridProviders",
		"fields": map[string]interface{}{
			"name":     map[string]interface{}{"type": "text", "label": "Name", "description": "Provider display name"},
			"provider": map[string]interface{}{"type": "select", "label": "Provider", "options": []string{"realdebrid", "torbox"}, "description": "Provider type"},
			"apiKey":   map[string]interface{}{"type": "password", "label": "API Key", "description": "Provider API key"},
			"enabled":  map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this provider"},
		},
	},
	"usenet": map[string]interface{}{
		"label":    "Usenet Providers",
		"icon":     "download",
		"group":    "providers",
		"order":    2,
		"is_array": true,
		"fields": map[string]interface{}{
			"name":        map[string]interface{}{"type": "text", "label": "Name", "description": "Provider name"},
			"host":        map[string]interface{}{"type": "text", "label": "Host", "description": "NNTP server hostname"},
			"port":        map[string]interface{}{"type": "number", "label": "Port", "description": "NNTP port (usually 119 or 563)"},
			"ssl":         map[string]interface{}{"type": "boolean", "label": "SSL", "description": "Use SSL/TLS connection"},
			"username":    map[string]interface{}{"type": "text", "label": "Username", "description": "NNTP username"},
			"password":    map[string]interface{}{"type": "password", "label": "Password", "description": "NNTP password"},
			"connections": map[string]interface{}{"type": "number", "label": "Connections", "description": "Max connections"},
			"enabled":     map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this provider"},
		},
	},
	"filtering": map[string]interface{}{
		"label": "Content Filtering",
		"icon":  "filter",
		"group": "sources",
		"order": 0,
		"fields": map[string]interface{}{
			"servicePriority": map[string]interface{}{
				"type":        "select",
				"label":       "Service Priority",
				"description": "Prioritize results from a specific service type",
				"options":     []string{"none", "usenet", "debrid"},
			},
			"maxSizeMovieGb":   map[string]interface{}{"type": "number", "label": "Max Movie Size (GB)", "description": "Maximum movie file size (0 = no limit)"},
			"maxSizeEpisodeGb": map[string]interface{}{"type": "number", "label": "Max Episode Size (GB)", "description": "Maximum episode file size (0 = no limit)"},
			"excludeHdr":       map[string]interface{}{"type": "boolean", "label": "Exclude HDR", "description": "Exclude HDR content from results"},
			"prioritizeHdr":    map[string]interface{}{"type": "boolean", "label": "Prioritize HDR", "description": "Prioritize HDR/DV content in results"},
			"filterOutTerms":   map[string]interface{}{"type": "tags", "label": "Filter Terms", "description": "Terms to filter out from results"},
		},
	},
	"live": map[string]interface{}{
		"label": "Live TV",
		"icon":  "tv",
		"group": "sources",
		"order": 1,
		"fields": map[string]interface{}{
			"playlistUrl":           map[string]interface{}{"type": "text", "label": "Playlist URL", "description": "M3U playlist URL"},
			"playlistCacheTtlHours": map[string]interface{}{"type": "number", "label": "Cache TTL (hours)", "description": "Playlist cache duration"},
		},
	},
	"indexers": map[string]interface{}{
		"label":    "Indexers",
		"icon":     "search",
		"group":    "sources",
		"order":    2,
		"is_array": true,
		"fields": map[string]interface{}{
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Indexer name"},
			"url":     map[string]interface{}{"type": "text", "label": "URL", "description": "Indexer API URL"},
			"apiKey":  map[string]interface{}{"type": "password", "label": "API Key", "description": "Indexer API key"},
			"type":    map[string]interface{}{"type": "select", "label": "Type", "options": []string{"torznab"}, "description": "Indexer type"},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this indexer"},
		},
	},
	"torrentScrapers": map[string]interface{}{
		"label":    "Torrent Scrapers",
		"icon":     "magnet",
		"group":    "sources",
		"order":    3,
		"is_array": true,
		"fields": map[string]interface{}{
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Scraper name"},
			"type":    map[string]interface{}{"type": "select", "label": "Type", "options": []string{"torrentio"}, "description": "Scraper type"},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this scraper"},
		},
	},
	"playback": map[string]interface{}{
		"label": "Playback",
		"icon":  "play",
		"group": "experience",
		"order": 0,
		"fields": map[string]interface{}{
			"preferredPlayer":           map[string]interface{}{"type": "select", "label": "Preferred Player", "options": []string{"native", "infuse"}, "description": "Default video player"},
			"preferredAudioLanguage":    map[string]interface{}{"type": "text", "label": "Audio Language", "description": "Preferred audio language code"},
			"preferredSubtitleLanguage": map[string]interface{}{"type": "text", "label": "Subtitle Language", "description": "Preferred subtitle language code"},
			"preferredSubtitleMode":     map[string]interface{}{"type": "select", "label": "Subtitle Mode", "options": []string{"off", "on", "auto"}, "description": "Default subtitle behavior"},
			"useLoadingScreen":          map[string]interface{}{"type": "boolean", "label": "Loading Screen", "description": "Show loading screen during playback init"},
		},
	},
	"homeShelves": map[string]interface{}{
		"label": "Home Shelves",
		"icon":  "layout",
		"group": "experience",
		"order": 1,
		"fields": map[string]interface{}{
			"trendingMovieSource": map[string]interface{}{"type": "select", "label": "Trending Source", "options": []string{"all", "released"}, "description": "Trending movies source"},
		},
	},
	"homeShelves.shelves": map[string]interface{}{
		"label":    "Shelf Configuration",
		"icon":     "list",
		"is_array": true,
		"parent":   "homeShelves",
		"key":      "shelves",
		"fields": map[string]interface{}{
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Display name"},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Show this shelf"},
			"order":   map[string]interface{}{"type": "number", "label": "Order", "description": "Sort order (lower = first)"},
		},
	},
	"metadata": map[string]interface{}{
		"label": "Metadata",
		"icon":  "film",
		"group": "storage",
		"order": 0,
		"fields": map[string]interface{}{
			"tvdbApiKey": map[string]interface{}{"type": "password", "label": "TVDB API Key", "description": "TheTVDB API key"},
			"tmdbApiKey": map[string]interface{}{"type": "password", "label": "TMDB API Key", "description": "TheMovieDB API key"},
		},
	},
	"cache": map[string]interface{}{
		"label": "Cache",
		"icon":  "database",
		"group": "storage",
		"order": 1,
		"fields": map[string]interface{}{
			"directory":        map[string]interface{}{"type": "text", "label": "Directory", "description": "Cache directory path"},
			"metadataTtlHours": map[string]interface{}{"type": "number", "label": "Metadata TTL (hours)", "description": "Metadata cache duration"},
		},
	},
	"import": map[string]interface{}{
		"label": "Import Settings",
		"icon":  "upload",
		"group": "storage",
		"order": 2,
		"fields": map[string]interface{}{
			"rarMaxWorkers":     map[string]interface{}{"type": "number", "label": "RAR Max Workers", "description": "Maximum RAR extraction workers"},
			"rarMaxCacheSizeMb": map[string]interface{}{"type": "number", "label": "RAR Cache Size (MB)", "description": "RAR cache size"},
			"rarMaxMemoryGB":    map[string]interface{}{"type": "number", "label": "RAR Max Memory (GB)", "description": "Maximum memory for RAR operations"},
		},
	},
}

// AdminUIHandler serves the admin dashboard UI
type AdminUIHandler struct {
	indexTemplate       *template.Template
	settingsTemplate    *template.Template
	statusTemplate      *template.Template
	loginTemplate       *template.Template
	settingsPath        string
	hlsManager          *HLSManager
	usersService        *users.Service
	userSettingsService *user_settings.Service
	configManager       *config.Manager
	pin                 string
}

// NewAdminUIHandler creates a new admin UI handler
func NewAdminUIHandler(settingsPath string, hlsManager *HLSManager, usersService *users.Service, userSettingsService *user_settings.Service, configManager *config.Manager, pin string) *AdminUIHandler {
	funcMap := template.FuncMap{
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"countEnabled": func(items []interface{}) int {
			count := 0
			for _, item := range items {
				if m, ok := item.(map[string]interface{}); ok {
					if enabled, ok := m["enabled"].(bool); ok && enabled {
						count++
					}
				}
			}
			return count
		},
		"countEnabledProviders": func(providers []config.UsenetSettings) int {
			count := 0
			for _, p := range providers {
				if p.Enabled {
					count++
				}
			}
			return count
		},
		"countEnabledIndexers": func(indexers []config.IndexerConfig) int {
			count := 0
			for _, i := range indexers {
				if i.Enabled {
					count++
				}
			}
			return count
		},
		"countEnabledScrapers": func(scrapers []config.TorrentScraperConfig) int {
			count := 0
			for _, s := range scrapers {
				if s.Enabled {
					count++
				}
			}
			return count
		},
		"countEnabledDebrid": func(providers []config.DebridProviderSettings) int {
			count := 0
			for _, p := range providers {
				if p.Enabled {
					count++
				}
			}
			return count
		},
		"totalConnections": func(providers []config.UsenetSettings) int {
			total := 0
			for _, p := range providers {
				if p.Enabled {
					total += p.Connections
				}
			}
			return total
		},
		"hasFiltering": func(f config.FilterSettings) bool {
			return f.ExcludeHdr || f.MaxSizeMovieGB > 0 || len(f.FilterOutTerms) > 0
		},
		"join": strings.Join,
	}

	// Read base template
	baseContent, err := adminTemplates.ReadFile("admin_templates/base.html")
	if err != nil {
		fmt.Printf("Error reading base template: %v\n", err)
	}

	// Helper to create a page template with base
	createPageTemplate := func(pageName string) *template.Template {
		pageContent, err := adminTemplates.ReadFile("admin_templates/" + pageName)
		if err != nil {
			fmt.Printf("Error reading %s: %v\n", pageName, err)
			return nil
		}
		tmpl := template.New("page").Funcs(funcMap)
		tmpl, err = tmpl.Parse(string(baseContent))
		if err != nil {
			fmt.Printf("Error parsing base for %s: %v\n", pageName, err)
			return nil
		}
		tmpl, err = tmpl.Parse(string(pageContent))
		if err != nil {
			fmt.Printf("Error parsing %s: %v\n", pageName, err)
			return nil
		}
		return tmpl
	}

	// Create login template (standalone, no base)
	var loginTmpl *template.Template
	loginContent, err := adminTemplates.ReadFile("admin_templates/login.html")
	if err != nil {
		fmt.Printf("Error reading login.html: %v\n", err)
	} else {
		loginTmpl, err = template.New("login").Parse(string(loginContent))
		if err != nil {
			fmt.Printf("Error parsing login.html: %v\n", err)
		}
	}

	return &AdminUIHandler{
		indexTemplate:       createPageTemplate("index.html"),
		settingsTemplate:    createPageTemplate("settings.html"),
		statusTemplate:      createPageTemplate("status.html"),
		loginTemplate:       loginTmpl,
		settingsPath:        settingsPath,
		hlsManager:          hlsManager,
		usersService:        usersService,
		userSettingsService: userSettingsService,
		configManager:       configManager,
		pin:                 strings.TrimSpace(pin),
	}
}

// AdminPageData holds data for admin page templates
type AdminPageData struct {
	CurrentPath string
	Settings    config.Settings
	Schema      map[string]interface{}
	Groups      []map[string]string
	Status      AdminStatus
	Users       []models.User
}

// AdminStatus holds backend status information
type AdminStatus struct {
	BackendReachable bool      `json:"backend_reachable"`
	Timestamp        time.Time `json:"timestamp"`
	UsenetTotal      int       `json:"usenet_total"`
	DebridStatus     string    `json:"debrid_status"`
}

// Dashboard serves the main admin dashboard
func (h *AdminUIHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	status := h.getStatus(settings)

	data := AdminPageData{
		CurrentPath: "/admin",
		Settings:    settings,
		Schema:      SettingsSchema,
		Status:      status,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.indexTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// SettingsPage serves the settings management page
func (h *AdminUIHandler) SettingsPage(w http.ResponseWriter, r *http.Request) {
	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var usersList []models.User
	if h.usersService != nil {
		usersList = h.usersService.List()
	}

	data := AdminPageData{
		CurrentPath: "/admin/settings",
		Settings:    settings,
		Schema:      SettingsSchema,
		Groups:      SettingsGroups,
		Users:       usersList,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.settingsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Settings template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// StatusPage serves the server status page
func (h *AdminUIHandler) StatusPage(w http.ResponseWriter, r *http.Request) {
	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	status := h.getStatus(settings)

	data := AdminPageData{
		CurrentPath: "/admin/status",
		Settings:    settings,
		Schema:      SettingsSchema,
		Status:      status,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.statusTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Status template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// GetSchema returns the settings schema as JSON
func (h *AdminUIHandler) GetSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SettingsSchema)
}

// GetStatus returns the backend status as JSON
func (h *AdminUIHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	status := h.getStatus(settings)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// GetStreams returns active streams as JSON
func (h *AdminUIHandler) GetStreams(w http.ResponseWriter, r *http.Request) {
	streams := []map[string]interface{}{}

	// Get HLS sessions
	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, session := range h.hlsManager.sessions {
			session.mu.RLock()
			filename := filepath.Base(session.Path)
			if filename == "" || filename == "." {
				filename = filepath.Base(session.OriginalPath)
			}
			streams = append(streams, map[string]interface{}{
				"id":             session.ID,
				"type":           "hls",
				"path":           session.Path,
				"original_path":  session.OriginalPath,
				"filename":       filename,
				"created_at":     session.CreatedAt,
				"last_access":    session.LastAccess,
				"duration":       session.Duration,
				"bytes_streamed": session.BytesStreamed,
				"has_dv":         session.HasDV && !session.DVDisabled,
				"has_hdr":        session.HasHDR,
				"dv_profile":     session.DVProfile,
				"segments":       session.SegmentsCreated,
			})
			session.mu.RUnlock()
		}
		h.hlsManager.mu.RUnlock()
	}

	// Get direct streams from the global tracker
	tracker := GetStreamTracker()
	for _, stream := range tracker.GetActiveStreams() {
		streams = append(streams, map[string]interface{}{
			"id":             stream.ID,
			"type":           "direct",
			"path":           stream.Path,
			"filename":       stream.Filename,
			"client_ip":      stream.ClientIP,
			"created_at":     stream.StartTime,
			"last_access":    stream.LastActivity,
			"bytes_streamed": stream.BytesStreamed,
			"content_length": stream.ContentLength,
			"user_agent":     stream.UserAgent,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"streams": streams,
	})
}

// ProxyHealth proxies health check requests to avoid CORS issues
func (h *AdminUIHandler) ProxyHealth(w http.ResponseWriter, r *http.Request) {
	// Since we're now running in the same process, we can just return OK
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      200,
		"ok":          true,
		"duration_ms": 0,
	})
}

func (h *AdminUIHandler) getStatus(settings config.Settings) AdminStatus {
	status := AdminStatus{
		BackendReachable: true, // We're in the same process
		Timestamp:        time.Now(),
	}

	// Calculate usenet total connections
	for _, p := range settings.Usenet {
		if p.Enabled {
			status.UsenetTotal += p.Connections
		}
	}

	// Check debrid providers
	enabledDebrid := 0
	for _, p := range settings.Streaming.DebridProviders {
		if p.Enabled {
			enabledDebrid++
		}
	}
	if enabledDebrid > 0 {
		status.DebridStatus = fmt.Sprintf("%d provider(s) configured", enabledDebrid)
	} else {
		status.DebridStatus = "No providers enabled"
	}

	return status
}

// GetUserSettings returns user-specific settings as JSON
func (h *AdminUIHandler) GetUserSettings(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId parameter required", http.StatusBadRequest)
		return
	}

	if h.userSettingsService == nil {
		http.Error(w, "User settings service not available", http.StatusInternalServerError)
		return
	}

	// Get global settings as defaults
	globalSettings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Failed to load global settings", http.StatusInternalServerError)
		return
	}

	defaults := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:           globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
			UseLoadingScreen:          globalSettings.Playback.UseLoadingScreen,
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves:             convertShelves(globalSettings.HomeShelves.Shelves),
			TrendingMovieSource: models.TrendingMovieSource(globalSettings.HomeShelves.TrendingMovieSource),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:   globalSettings.Filtering.MaxSizeMovieGB,
			MaxSizeEpisodeGB: globalSettings.Filtering.MaxSizeEpisodeGB,
			ExcludeHdr:       globalSettings.Filtering.ExcludeHdr,
			PrioritizeHdr:    globalSettings.Filtering.PrioritizeHdr,
			FilterOutTerms:   globalSettings.Filtering.FilterOutTerms,
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
	}

	userSettings, err := h.userSettingsService.GetWithDefaults(userID, defaults)
	if err != nil {
		http.Error(w, "Failed to load user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(userSettings)
}

// SaveUserSettings saves user-specific settings
func (h *AdminUIHandler) SaveUserSettings(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId parameter required", http.StatusBadRequest)
		return
	}

	if h.userSettingsService == nil {
		http.Error(w, "User settings service not available", http.StatusInternalServerError)
		return
	}

	var settings models.UserSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.userSettingsService.Update(userID, settings); err != nil {
		http.Error(w, "Failed to save user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// LoginPageData holds data for the login template
type LoginPageData struct {
	Error string
}

// IsAuthenticated checks if the request has a valid admin session
func (h *AdminUIHandler) IsAuthenticated(r *http.Request) bool {
	// If no PIN is configured, allow access
	if h.pin == "" {
		return true
	}

	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return false
	}
	return adminSessions.validate(cookie.Value)
}

// RequireAuth is middleware that redirects to login if not authenticated
func (h *AdminUIHandler) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.IsAuthenticated(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// LoginPage serves the login page (GET)
func (h *AdminUIHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	// If no PIN configured, redirect to dashboard
	if h.pin == "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	// If already authenticated, redirect to dashboard
	if h.IsAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.loginTemplate.ExecuteTemplate(w, "login", LoginPageData{}); err != nil {
		fmt.Printf("Login template error: %v\n", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// LoginSubmit handles login form submission (POST)
func (h *AdminUIHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	// If no PIN configured, redirect to dashboard
	if h.pin == "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderLoginError(w, "Invalid request")
		return
	}

	submittedPIN := strings.TrimSpace(r.FormValue("pin"))
	if submittedPIN == "" {
		h.renderLoginError(w, "PIN is required")
		return
	}

	// Constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare([]byte(submittedPIN), []byte(h.pin)) != 1 {
		h.renderLoginError(w, "Invalid PIN")
		return
	}

	// Create session and set cookie
	token := adminSessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/admin",
		MaxAge:   int(adminSessionDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// Logout handles logout requests
func (h *AdminUIHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(adminSessionCookieName)
	if err == nil {
		adminSessions.revoke(cookie.Value)
	}

	// Clear the cookie
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *AdminUIHandler) renderLoginError(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.loginTemplate.ExecuteTemplate(w, "login", LoginPageData{Error: errMsg}); err != nil {
		fmt.Printf("Login template error: %v\n", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// HasPIN returns true if a PIN is configured
func (h *AdminUIHandler) HasPIN() bool {
	return h.pin != ""
}

// TestIndexerRequest represents a request to test an indexer
type TestIndexerRequest struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
	Name   string `json:"name"`
}

// TestIndexer tests an indexer by running a search
func (h *AdminUIHandler) TestIndexer(w http.ResponseWriter, r *http.Request) {
	var req TestIndexerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	// Build the test URL
	testURL := strings.TrimSpace(req.URL)
	if !strings.HasSuffix(strings.ToLower(testURL), "/api") {
		testURL = strings.TrimRight(testURL, "/") + "/api"
	}

	// Make a test search request
	client := &http.Client{Timeout: 15 * time.Second}
	searchURL := fmt.Sprintf("%s?t=search&q=test&apikey=%s", testURL, req.APIKey)

	resp, err := client.Get(searchURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Indexer is reachable and responding",
	})
}

// TestScraperRequest represents a request to test the torrentio scraper
type TestScraperRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// addBrowserHeaders adds browser-like headers to avoid being blocked
func addBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("DNT", "1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Cache-Control", "no-cache")
}

// TestScraper tests the torrentio scraper by running a search
func (h *AdminUIHandler) TestScraper(w http.ResponseWriter, r *http.Request) {
	var req TestScraperRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Test torrentio by checking cinemeta and then torrentio endpoints
	client := &http.Client{Timeout: 15 * time.Second}

	// First test cinemeta (used by torrentio)
	cinemetaURL := "https://v3-cinemeta.strem.io/catalog/movie/search=test.json"
	cinemetaReq, err := http.NewRequest(http.MethodGet, cinemetaURL, nil)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	addBrowserHeaders(cinemetaReq)

	resp, err := client.Do(cinemetaReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cinemeta connection failed: %v", err),
		})
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cinemeta returned HTTP %d", resp.StatusCode),
		})
		return
	}

	// Test torrentio with a known IMDB ID (The Matrix)
	torrentioURL := "https://torrentio.strem.fun/stream/movie/tt0133093.json"
	torrentioReq, err := http.NewRequest(http.MethodGet, torrentioURL, nil)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	addBrowserHeaders(torrentioReq)

	resp, err = client.Do(torrentioReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Torrentio connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Torrentio returned HTTP %d", resp.StatusCode),
		})
		return
	}

	// Parse response to count streams
	var result struct {
		Streams []interface{} `json:"streams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Torrentio is reachable (couldn't parse stream count)",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Torrentio is working (%d streams found)", len(result.Streams)),
	})
}

// TestUsenetProviderRequest represents a request to test a usenet provider
type TestUsenetProviderRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	SSL      bool   `json:"ssl"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// TestUsenetProvider tests a usenet provider by connecting to the NNTP server
func (h *AdminUIHandler) TestUsenetProvider(w http.ResponseWriter, r *http.Request) {
	var req TestUsenetProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Host == "" {
		http.Error(w, "Host is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Connect to the NNTP server
	addr := fmt.Sprintf("%s:%d", req.Host, req.Port)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Connection failed: %v", err),
		})
		return
	}
	defer conn.Close()

	// Handle SSL if needed
	if req.SSL {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: req.Host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("TLS handshake failed: %v", err),
			})
			return
		}
		conn = tlsConn
	}

	// Set deadline for reading
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	// Read greeting
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to read greeting: %v", err),
		})
		return
	}

	// Check for valid NNTP greeting (200 or 201)
	if !strings.HasPrefix(line, "200") && !strings.HasPrefix(line, "201") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Unexpected greeting: %s", strings.TrimSpace(line)),
		})
		return
	}

	// Try to authenticate if credentials provided
	if req.Username != "" {
		// Send AUTHINFO USER
		fmt.Fprintf(conn, "AUTHINFO USER %s\r\n", req.Username)
		line, err = reader.ReadString('\n')
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Auth failed: %v", err),
			})
			return
		}

		// Check if password is required (381) or auth succeeded (281)
		if strings.HasPrefix(line, "381") {
			// Send password
			fmt.Fprintf(conn, "AUTHINFO PASS %s\r\n", req.Password)
			line, err = reader.ReadString('\n')
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("Password auth failed: %v", err),
				})
				return
			}
		}

		if !strings.HasPrefix(line, "281") && !strings.HasPrefix(line, "200") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Authentication rejected: %s", strings.TrimSpace(line)),
			})
			return
		}
	}

	// Send QUIT
	fmt.Fprintf(conn, "QUIT\r\n")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "NNTP connection successful, authentication passed",
	})
}

// TestDebridProviderRequest represents a request to test a debrid provider
type TestDebridProviderRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
}

// ProfileWithPinStatus represents a profile with its PIN status
type ProfileWithPinStatus struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color,omitempty"`
	HasPin    bool      `json:"hasPin"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// GetProfiles returns all profiles with their PIN status
func (h *AdminUIHandler) GetProfiles(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	users := h.usersService.List()
	profiles := make([]ProfileWithPinStatus, len(users))
	for i, u := range users {
		profiles[i] = ProfileWithPinStatus{
			ID:        u.ID,
			Name:      u.Name,
			Color:     u.Color,
			HasPin:    u.HasPin(),
			CreatedAt: u.CreatedAt,
			UpdatedAt: u.UpdatedAt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profiles)
}

// SetProfilePinRequest represents a request to set a profile's PIN
type SetProfilePinRequest struct {
	Pin string `json:"pin"`
}

// SetProfilePin sets a profile's PIN
func (h *AdminUIHandler) SetProfilePin(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	var req SetProfilePinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.usersService.SetPin(profileID, req.Pin)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "at least") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProfileWithPinStatus{
		ID:        user.ID,
		Name:      user.Name,
		Color:     user.Color,
		HasPin:    user.HasPin(),
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	})
}

// ClearProfilePin removes a profile's PIN
func (h *AdminUIHandler) ClearProfilePin(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	user, err := h.usersService.ClearPin(profileID)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProfileWithPinStatus{
		ID:        user.ID,
		Name:      user.Name,
		Color:     user.Color,
		HasPin:    user.HasPin(),
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	})
}

// TestDebridProvider tests a debrid provider by checking their API
func (h *AdminUIHandler) TestDebridProvider(w http.ResponseWriter, r *http.Request) {
	var req TestDebridProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.APIKey == "" {
		http.Error(w, "API Key is required", http.StatusBadRequest)
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}

	switch strings.ToLower(req.Provider) {
	case "realdebrid":
		// Test Real-Debrid by getting user info
		apiReq, err := http.NewRequest(http.MethodGet, "https://api.real-debrid.com/rest/1.0/user", nil)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to create request: %v", err),
			})
			return
		}
		apiReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", req.APIKey))

		resp, err := client.Do(apiReq)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Connection failed: %v", err),
			})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Invalid API key",
			})
			return
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
			})
			return
		}

		var user struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			Type     string `json:"type"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "Real-Debrid API is reachable",
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Connected as %s (%s)", user.Username, user.Type),
		})

	case "torbox":
		// Test Torbox by getting user info
		apiReq, err := http.NewRequest(http.MethodGet, "https://api.torbox.app/v1/api/user/me", nil)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to create request: %v", err),
			})
			return
		}
		apiReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", req.APIKey))

		resp, err := client.Do(apiReq)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Connection failed: %v", err),
			})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Invalid API key",
			})
			return
		}

		var result struct {
			Success bool `json:"success"`
			Data    struct {
				Email string `json:"email"`
				Plan  int    `json:"plan"`
			} `json:"data"`
			Detail string `json:"detail"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "TorBox API is reachable",
			})
			return
		}

		if !result.Success {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   result.Detail,
			})
			return
		}

		planNames := map[int]string{0: "Free", 1: "Essential", 2: "Pro", 3: "Standard"}
		planName := planNames[result.Data.Plan]
		if planName == "" {
			planName = fmt.Sprintf("Plan %d", result.Data.Plan)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Connected as %s (%s)", result.Data.Email, planName),
		})

	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Unknown provider: %s", req.Provider),
		})
	}
}

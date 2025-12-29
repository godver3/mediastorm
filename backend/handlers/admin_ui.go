package handlers

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/debrid"
	"novastream/services/history"
	"novastream/services/invitations"
	"novastream/services/plex"
	"novastream/services/sessions"
	"novastream/services/trakt"
	"novastream/services/watchlist"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
)

//go:embed admin_templates/*
var adminTemplates embed.FS

const (
	adminSessionCookieName         = "strmr_admin_session"
	adminSessionDuration           = 24 * time.Hour
	adminSessionDurationRememberMe = 30 * 24 * time.Hour // 30 days
	adminSessionsDir               = "cache/sessions"
	adminSessionsFile              = "cache/sessions/admin.json"
)

// sessionContextKey is used to store session in request context
type adminSessionContextKey struct{}

// adminSessionFromContext retrieves the session from request context
func adminSessionFromContext(ctx context.Context) *models.Session {
	if session, ok := ctx.Value(adminSessionContextKey{}).(*models.Session); ok {
		return session
	}
	return nil
}

// adminSessionStore manages admin session tokens with file persistence
type adminSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time // token -> expiry
}

var adminSessions = &adminSessionStore{
	sessions: make(map[string]time.Time),
}

func init() {
	adminSessions.load()
}

// sessionData is the JSON structure for persisted sessions
type sessionData struct {
	Token  string    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

func (s *adminSessionStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(adminSessionsFile)
	if err != nil {
		// File doesn't exist yet, that's fine
		return
	}

	var sessions []sessionData
	if err := json.Unmarshal(data, &sessions); err != nil {
		log.Printf("[admin-session] failed to parse sessions file: %v", err)
		return
	}

	now := time.Now()
	loaded := 0
	for _, sess := range sessions {
		if sess.Expiry.After(now) {
			s.sessions[sess.Token] = sess.Expiry
			loaded++
		}
	}
	if loaded > 0 {
		log.Printf("[admin-session] loaded %d valid sessions from disk", loaded)
	}
}

func (s *adminSessionStore) save() {
	// Ensure directory exists
	if err := os.MkdirAll(adminSessionsDir, 0700); err != nil {
		log.Printf("[admin-session] failed to create sessions directory: %v", err)
		return
	}

	s.mu.RLock()
	var sessions []sessionData
	now := time.Now()
	for token, expiry := range s.sessions {
		if expiry.After(now) {
			sessions = append(sessions, sessionData{Token: token, Expiry: expiry})
		}
	}
	s.mu.RUnlock()

	data, err := json.Marshal(sessions)
	if err != nil {
		log.Printf("[admin-session] failed to marshal sessions: %v", err)
		return
	}

	if err := os.WriteFile(adminSessionsFile, data, 0600); err != nil {
		log.Printf("[admin-session] failed to write sessions file: %v", err)
	}
}

func (s *adminSessionStore) create(duration time.Duration) string {
	s.mu.Lock()

	// Generate random token
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	s.sessions[token] = time.Now().Add(duration)

	// Cleanup expired sessions
	now := time.Now()
	for t, exp := range s.sessions {
		if exp.Before(now) {
			delete(s.sessions, t)
		}
	}

	s.mu.Unlock()
	s.save()

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
	delete(s.sessions, token)
	s.mu.Unlock()
	s.save()
}

// SettingsGroups defines the order and labels for settings groups
var SettingsGroups = []map[string]string{
	{"id": "server", "label": "Server"},
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
	"streaming": map[string]interface{}{
		"label": "Streaming",
		"icon":  "play-circle",
		"group": "providers",
		"order": 0,
		"fields": map[string]interface{}{
			"maxDownloadWorkers": map[string]interface{}{"type": "number", "label": "Max Download Workers", "description": "Maximum concurrent download workers"},
			"maxCacheSizeMB":     map[string]interface{}{"type": "number", "label": "Max Cache Size (MB)", "description": "Maximum cache size in megabytes"},
			"serviceMode":        map[string]interface{}{"type": "select", "label": "Service Mode", "options": []string{"usenet", "debrid", "hybrid"}, "description": "Streaming service mode"},
			"servicePriority": map[string]interface{}{
				"type":        "select",
				"label":       "Service Priority",
				"description": "Prioritize results from a specific service type",
				"options":     []string{"none", "usenet", "debrid"},
			},
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
			"provider": map[string]interface{}{"type": "select", "label": "Provider", "options": []string{"realdebrid", "torbox", "alldebrid"}, "description": "Provider type"},
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
			"maxSizeMovieGb":   map[string]interface{}{"type": "number", "label": "Max Movie Size (GB)", "description": "Maximum movie file size (0 = no limit)"},
			"maxSizeEpisodeGb": map[string]interface{}{"type": "number", "label": "Max Episode Size (GB)", "description": "Maximum episode file size (0 = no limit)"},
			"maxResolution":    map[string]interface{}{"type": "select", "label": "Max Resolution", "options": []string{"", "480p", "720p", "1080p", "2160p"}, "description": "Maximum resolution (empty = no limit)"},
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
			"type":    map[string]interface{}{"type": "select", "label": "Type", "options": []string{"newznab"}, "description": "Indexer type"},
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
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Scraper name", "order": 0},
			"type":    map[string]interface{}{"type": "select", "label": "Type", "options": []string{"torrentio", "jackett", "zilean", "aiostreams"}, "description": "Scraper type", "order": 1},
			"options": map[string]interface{}{"type": "text", "label": "Options", "description": "Torrentio URL options (e.g., sort=qualitysize|qualityfilter=480p,scr,cam)", "showWhen": map[string]interface{}{"field": "type", "value": "torrentio"}, "order": 2, "placeholder": "sort=qualitysize|qualityfilter=480p,scr,cam"},
			"url":     map[string]interface{}{"type": "text", "label": "URL", "description": "API URL (for AIOStreams: full Stremio addon URL)", "showWhen": map[string]interface{}{"operator": "or", "conditions": []map[string]interface{}{{"field": "type", "value": "jackett"}, {"field": "type", "value": "zilean"}, {"field": "type", "value": "aiostreams"}}}, "order": 3},
			"apiKey":  map[string]interface{}{"type": "password", "label": "API Key", "description": "Jackett API key", "showWhen": map[string]interface{}{"field": "type", "value": "jackett"}, "order": 4},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this scraper", "order": 5},
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
			"subtitleSize":              map[string]interface{}{"type": "number", "label": "Subtitle Size", "description": "Subtitle size scaling factor (1.0 = default, 0.5 = half, 2.0 = double)", "step": 0.05, "min": 0.25, "max": 3.0},
			"seekForwardSeconds":        map[string]interface{}{"type": "number", "label": "Skip Forward", "description": "Seconds to skip forward (default 30)", "step": 5, "min": 5, "max": 120},
			"seekBackwardSeconds":       map[string]interface{}{"type": "number", "label": "Skip Backward", "description": "Seconds to skip backward (default 10)", "step": 5, "min": 5, "max": 120},
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
	"transmux": map[string]interface{}{
		"label": "Transmux Settings",
		"icon":  "film",
		"group": "storage",
		"order": 3,
		"fields": map[string]interface{}{
			"enabled":          map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable video transmuxing for HLS streaming"},
			"ffmpegPath":       map[string]interface{}{"type": "text", "label": "FFmpeg Path", "description": "Path to ffmpeg binary"},
			"ffprobePath":      map[string]interface{}{"type": "text", "label": "FFprobe Path", "description": "Path to ffprobe binary"},
			"hlsTempDirectory": map[string]interface{}{"type": "text", "label": "HLS Temp Directory", "description": "Directory for HLS segment storage (default: /tmp/novastream-hls)"},
		},
	},
	"subtitles": map[string]interface{}{
		"label":    "Subtitles",
		"icon":     "film",
		"group":    "sources",
		"order":    4,
		"testable": true,
		"fields": map[string]interface{}{
			"openSubtitlesUsername": map[string]interface{}{"type": "text", "label": "OpenSubtitles Username", "description": "OpenSubtitles.org username (optional, enables more results)", "order": 0},
			"openSubtitlesPassword": map[string]interface{}{"type": "password", "label": "OpenSubtitles Password", "description": "OpenSubtitles.org password", "order": 1},
		},
	},
}

// AdminUIHandler serves the admin dashboard UI
type AdminUIHandler struct {
	indexTemplate       *template.Template
	settingsTemplate    *template.Template
	statusTemplate      *template.Template
	historyTemplate     *template.Template
	toolsTemplate       *template.Template
	searchTemplate      *template.Template
	loginTemplate       *template.Template
	registerTemplate    *template.Template
	accountsTemplate    *template.Template
	settingsPath        string
	hlsManager          *HLSManager
	usersService        *users.Service
	userSettingsService *user_settings.Service
	historyService      *history.Service
	watchlistService    *watchlist.Service
	accountsService     *accounts.Service
	invitationsService  *invitations.Service
	sessionsService     *sessions.Service
	plexClient          *plex.Client
	traktClient         *trakt.Client
	configManager       *config.Manager
	metadataService     MetadataService
	getPIN              func() string
}

// MetadataService interface for metadata operations
type MetadataService interface {
	ClearCache() error
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error)
}

// SetMetadataService sets the metadata service for cache clearing and overview fetching
func (h *AdminUIHandler) SetMetadataService(ms MetadataService) {
	h.metadataService = ms
}

// SetHistoryService sets the history service for watch history data
func (h *AdminUIHandler) SetHistoryService(hs *history.Service) {
	h.historyService = hs
}

// SetWatchlistService sets the watchlist service for importing items
func (h *AdminUIHandler) SetWatchlistService(ws *watchlist.Service) {
	h.watchlistService = ws
}

// SetAccountsService sets the accounts service for account management
func (h *AdminUIHandler) SetAccountsService(as *accounts.Service) {
	h.accountsService = as
}

// SetInvitationsService sets the invitations service for invitation link management
func (h *AdminUIHandler) SetInvitationsService(is *invitations.Service) {
	h.invitationsService = is
}

// SetSessionsService sets the sessions service for session management
func (h *AdminUIHandler) SetSessionsService(ss *sessions.Service) {
	h.sessionsService = ss
}

// NewAdminUIHandler creates a new admin UI handler
func NewAdminUIHandler(settingsPath string, hlsManager *HLSManager, usersService *users.Service, userSettingsService *user_settings.Service, configManager *config.Manager, getPIN func() string) *AdminUIHandler {
	funcMap := template.FuncMap{
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"hasSuffix": func(s, suffix string) bool {
			return strings.HasSuffix(s, suffix)
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

	// Create register template (standalone, no base)
	var registerTmpl *template.Template
	registerContent, err := adminTemplates.ReadFile("admin_templates/register.html")
	if err != nil {
		fmt.Printf("Error reading register.html: %v\n", err)
	} else {
		registerTmpl, err = template.New("register").Parse(string(registerContent))
		if err != nil {
			fmt.Printf("Error parsing register.html: %v\n", err)
		}
	}

	return &AdminUIHandler{
		indexTemplate:       createPageTemplate("index.html"),
		settingsTemplate:    createPageTemplate("settings.html"),
		statusTemplate:      createPageTemplate("status.html"),
		historyTemplate:     createPageTemplate("history.html"),
		toolsTemplate:       createPageTemplate("tools.html"),
		searchTemplate:      createPageTemplate("search.html"),
		loginTemplate:       loginTmpl,
		registerTemplate:    registerTmpl,
		accountsTemplate:    createPageTemplate("accounts.html"),
		settingsPath:        settingsPath,
		hlsManager:          hlsManager,
		usersService:        usersService,
		userSettingsService: userSettingsService,
		configManager:       configManager,
		getPIN:              getPIN,
		plexClient:          plex.NewClient(plex.GenerateClientID()),
		traktClient:         trakt.NewClient("", ""), // Will be updated with credentials from settings
	}
}

// AdminPageData holds data for admin page templates
type AdminPageData struct {
	CurrentPath string
	BasePath    string // "/admin" for master accounts, "/account" for regular accounts
	IsAdmin     bool   // true for master accounts, false for regular accounts
	AccountID   string // Account ID for scoping data (empty for master)
	Username    string // Username of logged in account
	Settings    config.Settings
	Schema      map[string]interface{}
	Groups      []map[string]string
	Status      AdminStatus
	Users       []models.User
	Version     string
}

// AdminStatus holds backend status information
type AdminStatus struct {
	BackendReachable bool      `json:"backend_reachable"`
	Timestamp        time.Time `json:"timestamp"`
	UsenetTotal      int       `json:"usenet_total"`
	DebridStatus     string    `json:"debrid_status"`
}

// SettingsPage serves the settings management page
func (h *AdminUIHandler) SettingsPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	usersList := h.getScopedUsers(isAdmin, accountID)

	data := AdminPageData{
		CurrentPath: basePath + "/settings",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Settings:    settings,
		Schema:      SettingsSchema,
		Groups:      SettingsGroups,
		Users:       usersList,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.settingsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Settings template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// StatusPage serves the server status page
func (h *AdminUIHandler) StatusPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	status := h.getStatus(settings)

	data := AdminPageData{
		CurrentPath: basePath + "/status",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Settings:    settings,
		Schema:      SettingsSchema,
		Status:      status,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.statusTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Status template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// HistoryPage serves the watch history page
func (h *AdminUIHandler) HistoryPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)
	usersList := h.getScopedUsers(isAdmin, accountID)

	data := AdminPageData{
		CurrentPath: basePath + "/history",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Users:       usersList,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.historyTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("History template error: %v\n", err)
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
	isAdmin, accountID, _, _ := h.getPageRoleInfo(r)

	// Get allowed profile IDs for this account (for filtering)
	allowedProfileIDs := make(map[string]bool)
	if !isAdmin {
		scopedUsers := h.getScopedUsers(isAdmin, accountID)
		for _, u := range scopedUsers {
			allowedProfileIDs[u.ID] = true
		}
	}

	streams := []map[string]interface{}{}

	// Get HLS sessions
	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, session := range h.hlsManager.sessions {
			session.mu.RLock()
			// Skip streams that don't belong to this account's profiles
			if !isAdmin && !allowedProfileIDs[session.ProfileID] {
				session.mu.RUnlock()
				continue
			}
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
				"profile_id":     session.ProfileID,
				"profile_name":   session.ProfileName,
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
		// Skip streams that don't belong to this account's profiles
		if !isAdmin && !allowedProfileIDs[stream.ProfileID] {
			continue
		}
		streams = append(streams, map[string]interface{}{
			"id":             stream.ID,
			"type":           "direct",
			"path":           stream.Path,
			"filename":       stream.Filename,
			"profile_id":     stream.ProfileID,
			"profile_name":   stream.ProfileName,
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

// GetDebridStatus returns account/subscription info for all configured debrid providers
func (h *AdminUIHandler) GetDebridStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings",
		})
		return
	}

	type ProviderStatus struct {
		Name          string `json:"name"`
		Provider      string `json:"provider"`
		Enabled       bool   `json:"enabled"`
		HasAPIKey     bool   `json:"has_api_key"`
		Username      string `json:"username,omitempty"`
		Email         string `json:"email,omitempty"`
		PremiumActive bool   `json:"premium_active"`
		ExpiresAt     string `json:"expires_at,omitempty"`
		DaysRemaining int    `json:"days_remaining,omitempty"`
		IsLifetime    bool   `json:"is_lifetime,omitempty"`
		Error         string `json:"error,omitempty"`
	}

	providers := make([]ProviderStatus, 0, len(settings.Streaming.DebridProviders))

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	for _, p := range settings.Streaming.DebridProviders {
		status := ProviderStatus{
			Name:      p.Name,
			Provider:  p.Provider,
			Enabled:   p.Enabled,
			HasAPIKey: p.APIKey != "",
		}

		if p.APIKey != "" {
			// Fetch account info from each provider (even if disabled, to show premium status)
			switch p.Provider {
			case "realdebrid":
				client := debrid.NewRealDebridClient(p.APIKey)
				if info, err := client.GetAccountInfo(ctx); err == nil {
					status.Username = info.Username
					status.Email = info.Email
					status.PremiumActive = info.PremiumActive
					status.IsLifetime = info.IsLifetime
					if info.ExpiresAt != nil {
						status.ExpiresAt = info.ExpiresAt.Format("2006-01-02")
						status.DaysRemaining = info.DaysRemaining
					}
				} else {
					status.Error = err.Error()
				}
			case "torbox":
				client := debrid.NewTorboxClient(p.APIKey)
				if info, err := client.GetAccountInfo(ctx); err == nil {
					status.Username = info.Username
					status.Email = info.Email
					status.PremiumActive = info.PremiumActive
					if info.ExpiresAt != nil {
						status.ExpiresAt = info.ExpiresAt.Format("2006-01-02")
						status.DaysRemaining = info.DaysRemaining
					}
				} else {
					status.Error = err.Error()
				}
			case "alldebrid":
				client := debrid.NewAllDebridClient(p.APIKey)
				if info, err := client.GetAccountInfo(ctx); err == nil {
					status.Username = info.Username
					status.Email = info.Email
					status.PremiumActive = info.PremiumActive
					if info.ExpiresAt != nil {
						status.ExpiresAt = info.ExpiresAt.Format("2006-01-02")
						status.DaysRemaining = info.DaysRemaining
					}
				} else {
					status.Error = err.Error()
				}
			}
		}

		providers = append(providers, status)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": providers,
	})
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

// IsAuthenticated checks if the request has a valid session (any account)
func (h *AdminUIHandler) IsAuthenticated(r *http.Request) bool {
	if h.sessionsService == nil {
		return false
	}

	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return false
	}

	_, err = h.sessionsService.Validate(cookie.Value)
	return err == nil
}

// IsMasterAuthenticated checks if the request has a valid master (admin) session
func (h *AdminUIHandler) IsMasterAuthenticated(r *http.Request) bool {
	if h.sessionsService == nil {
		return false
	}

	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return false
	}

	session, err := h.sessionsService.Validate(cookie.Value)
	if err != nil {
		return false
	}

	return session.IsMaster
}

// getSession retrieves the session from the request cookie
func (h *AdminUIHandler) getSession(r *http.Request) *models.Session {
	if h.sessionsService == nil {
		return nil
	}

	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return nil
	}

	session, err := h.sessionsService.Validate(cookie.Value)
	if err != nil {
		return nil
	}

	return &session
}

// getPageRoleInfo returns IsAdmin, AccountID, BasePath, and Username based on session and request path
func (h *AdminUIHandler) getPageRoleInfo(r *http.Request) (isAdmin bool, accountID string, basePath string, username string) {
	session := adminSessionFromContext(r.Context())
	if session == nil {
		return false, "", "/admin", ""
	}

	isAdmin = session.IsMaster
	accountID = session.AccountID

	// Get username from account
	if h.accountsService != nil {
		if account, ok := h.accountsService.Get(accountID); ok {
			username = account.Username
		}
	}

	// Determine base path from request URL
	if strings.HasPrefix(r.URL.Path, "/account") {
		basePath = "/account"
	} else {
		basePath = "/admin"
	}

	return isAdmin, accountID, basePath, username
}

// getScopedUsers returns users scoped to the account (all users for admin, filtered for regular accounts)
func (h *AdminUIHandler) getScopedUsers(isAdmin bool, accountID string) []models.User {
	if h.usersService == nil {
		return nil
	}

	if isAdmin {
		return h.usersService.List()
	}

	// For non-admin, filter to only their profiles
	allUsers := h.usersService.List()
	var scopedUsers []models.User
	for _, u := range allUsers {
		if u.AccountID == accountID {
			scopedUsers = append(scopedUsers, u)
		}
	}
	return scopedUsers
}

// profileBelongsToAccount checks if a profile belongs to the given account
func (h *AdminUIHandler) profileBelongsToAccount(profileID, accountID string) bool {
	if h.usersService == nil {
		return false
	}
	return h.usersService.BelongsToAccount(profileID, accountID)
}

// RequireAuth is middleware that allows any authenticated account and passes session to context
func (h *AdminUIHandler) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := h.getSession(r)
		if session == nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		// Add session to context for handlers to use
		ctx := context.WithValue(r.Context(), adminSessionContextKey{}, session)
		// Also set auth context keys so shared handlers (e.g., usersHandler) can access account info
		ctx = context.WithValue(ctx, auth.ContextKeyAccountID, session.AccountID)
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, session.IsMaster)
		next(w, r.WithContext(ctx))
	}
}

// RequireMasterAuth is middleware that only allows master (admin) accounts
func (h *AdminUIHandler) RequireMasterAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := h.getSession(r)
		if session == nil || !session.IsMaster {
			http.Error(w, "Admin access required", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), adminSessionContextKey{}, session)
		// Also set auth context keys so shared handlers can access account info
		ctx = context.WithValue(ctx, auth.ContextKeyAccountID, session.AccountID)
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, session.IsMaster)
		next(w, r.WithContext(ctx))
	}
}

// LoginPage serves the login page (GET)
func (h *AdminUIHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
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

// RegisterPageData holds data for the registration page
type RegisterPageData struct {
	Token string
	Error string
}

// RegisterPage serves the registration page (GET)
func (h *AdminUIHandler) RegisterPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")

	// Validate the token if provided
	var validationError string
	if token != "" && h.invitationsService != nil {
		if err := h.invitationsService.Validate(token); err != nil {
			validationError = err.Error()
		}
	} else if token == "" {
		validationError = "No invitation token provided"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.registerTemplate == nil {
		http.Error(w, "Registration page not available", http.StatusInternalServerError)
		return
	}
	if err := h.registerTemplate.ExecuteTemplate(w, "register", RegisterPageData{
		Token: token,
		Error: validationError,
	}); err != nil {
		fmt.Printf("Register template error: %v\n", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

// LoginSubmit handles login form submission (POST)
func (h *AdminUIHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil || h.sessionsService == nil {
		h.renderLoginError(w, "Authentication services not configured")
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderLoginError(w, "Invalid request")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" {
		h.renderLoginError(w, "Username is required")
		return
	}
	if password == "" {
		h.renderLoginError(w, "Password is required")
		return
	}

	// Authenticate using accounts service
	account, err := h.accountsService.Authenticate(username, password)
	if err != nil {
		h.renderLoginError(w, "Invalid username or password")
		return
	}

	// Check if "remember me" is checked
	rememberMe := r.FormValue("remember") == "1"
	sessionDuration := adminSessionDuration
	if rememberMe {
		sessionDuration = adminSessionDurationRememberMe
	}

	// Create session with appropriate duration
	userAgent := r.Header.Get("User-Agent")
	ipAddress := getClientIPAddress(r)
	session, err := h.sessionsService.CreateWithDuration(account.ID, account.IsMaster, userAgent, ipAddress, sessionDuration)
	if err != nil {
		h.renderLoginError(w, "Failed to create session")
		return
	}

	maxAge := int(sessionDuration.Seconds())

	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    session.Token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// Redirect based on account type
	if account.IsMaster {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
	}
}

// Logout handles logout requests
func (h *AdminUIHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(adminSessionCookieName)
	if err == nil && h.sessionsService != nil {
		h.sessionsService.Revoke(cookie.Value)
	}

	// Clear the cookie
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
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
	return strings.TrimSpace(h.getPIN()) != ""
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
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url"`
	APIKey  string `json:"apiKey"`
	Options string `json:"options"` // Torrentio URL options
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

// TestScraper tests a torrent scraper by running a test search
func (h *AdminUIHandler) TestScraper(w http.ResponseWriter, r *http.Request) {
	var req TestScraperRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch strings.ToLower(req.Type) {
	case "jackett":
		h.testJackettScraper(w, req)
	case "zilean":
		h.testZileanScraper(w, req)
	case "aiostreams":
		h.testAIOStreamsScraper(w, req)
	case "torrentio":
		fallthrough
	default:
		h.testTorrentioScraper(w, req.Options)
	}
}

// testTorrentioScraper tests torrentio by checking cinemeta and then torrentio endpoints
func (h *AdminUIHandler) testTorrentioScraper(w http.ResponseWriter, options string) {
	client := &http.Client{Timeout: 15 * time.Second}

	// First test cinemeta (used by torrentio)
	cinemetaURL := "https://v3-cinemeta.strem.io/catalog/movie/search=test.json"
	cinemetaReq, err := http.NewRequest(http.MethodGet, cinemetaURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	addBrowserHeaders(cinemetaReq)

	resp, err := client.Do(cinemetaReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cinemeta connection failed: %v", err),
		})
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cinemeta returned HTTP %d", resp.StatusCode),
		})
		return
	}

	// Test torrentio with a known IMDB ID (The Matrix)
	// Include options in URL if provided
	var torrentioURL string
	options = strings.TrimSpace(options)
	if options != "" {
		torrentioURL = fmt.Sprintf("https://torrentio.strem.fun/%s/stream/movie/tt0133093.json", options)
	} else {
		torrentioURL = "https://torrentio.strem.fun/stream/movie/tt0133093.json"
	}
	torrentioReq, err := http.NewRequest(http.MethodGet, torrentioURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	addBrowserHeaders(torrentioReq)

	resp, err = client.Do(torrentioReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Torrentio connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Torrentio is reachable (couldn't parse stream count)",
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Torrentio is working (%d streams found)", len(result.Streams)),
	})
}

// testJackettScraper tests Jackett by fetching capabilities
func (h *AdminUIHandler) testJackettScraper(w http.ResponseWriter, req TestScraperRequest) {
	if req.URL == "" || req.APIKey == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Jackett URL and API key are required",
		})
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	baseURL := strings.TrimRight(req.URL, "/")

	// Test by fetching capabilities
	capsURL := fmt.Sprintf("%s/api/v2.0/indexers/all/results/torznab/api?apikey=%s&t=caps", baseURL, req.APIKey)
	capsReq, err := http.NewRequest(http.MethodGet, capsURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}

	resp, err := client.Do(capsReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Jackett connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid API key",
		})
		return
	}

	if resp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Jackett returned HTTP %d", resp.StatusCode),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Jackett is working",
	})
}

// testZileanScraper tests a Zilean instance by querying its DMM filtered API
func (h *AdminUIHandler) testZileanScraper(w http.ResponseWriter, req TestScraperRequest) {
	if req.URL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Zilean URL is required",
		})
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	baseURL := strings.TrimRight(req.URL, "/")

	// Test by making a simple query to the DMM filtered endpoint
	testURL := fmt.Sprintf("%s/dmm/filtered?Query=test", baseURL)
	testReq, err := http.NewRequest(http.MethodGet, testURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	testReq.Header.Set("Accept", "application/json")

	resp, err := client.Do(testReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Zilean connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Zilean returned HTTP %d", resp.StatusCode),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Zilean is working",
	})
}

// testAIOStreamsScraper tests an AIOStreams instance by fetching its manifest and a test stream
func (h *AdminUIHandler) testAIOStreamsScraper(w http.ResponseWriter, req TestScraperRequest) {
	if req.URL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "AIOStreams URL is required (full URL including config token)",
		})
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}
	baseURL := strings.TrimRight(req.URL, "/")
	// Strip /manifest.json if user included it
	baseURL = strings.TrimSuffix(baseURL, "/manifest.json")

	// Test by fetching the manifest
	manifestURL := fmt.Sprintf("%s/manifest.json", baseURL)
	manifestReq, err := http.NewRequest(http.MethodGet, manifestURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	addBrowserHeaders(manifestReq)

	resp, err := client.Do(manifestReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("AIOStreams connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("AIOStreams returned HTTP %d", resp.StatusCode),
		})
		return
	}

	// Try to parse manifest to get addon name
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "AIOStreams is reachable",
		})
		return
	}

	// Test a stream query (The Matrix - a known IMDB ID)
	streamURL := fmt.Sprintf("%s/stream/movie/tt0133093.json", baseURL)
	streamReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("AIOStreams manifest OK (%s v%s), but couldn't test streams", manifest.Name, manifest.Version),
		})
		return
	}
	addBrowserHeaders(streamReq)

	streamResp, err := client.Do(streamReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("AIOStreams manifest OK (%s v%s), but stream test failed: %v", manifest.Name, manifest.Version, err),
		})
		return
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("AIOStreams manifest OK (%s v%s), but stream test returned HTTP %d", manifest.Name, manifest.Version, streamResp.StatusCode),
		})
		return
	}

	// Parse stream response to count results
	var streamResult struct {
		Streams []interface{} `json:"streams"`
	}
	if err := json.NewDecoder(streamResp.Body).Decode(&streamResult); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("AIOStreams is working (%s v%s)", manifest.Name, manifest.Version),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("AIOStreams is working (%s v%s, %d streams found)", manifest.Name, manifest.Version, len(streamResult.Streams)),
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
	ID             string    `json:"id"`
	AccountID      string    `json:"accountId,omitempty"`
	Name           string    `json:"name"`
	Color          string    `json:"color,omitempty"`
	HasPin         bool      `json:"hasPin"`
	IsKidsProfile  bool      `json:"isKidsProfile"`
	TraktAccountID string    `json:"traktAccountId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// GetProfiles returns all profiles with their PIN status (for admin dashboard)
func (h *AdminUIHandler) GetProfiles(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	isAdmin, accountID, _, _ := h.getPageRoleInfo(r)

	// Admin shows ALL profiles, regular accounts only see their own
	users := h.getScopedUsers(isAdmin, accountID)
	profiles := make([]ProfileWithPinStatus, len(users))
	for i, u := range users {
		profiles[i] = ProfileWithPinStatus{
			ID:             u.ID,
			AccountID:      u.AccountID,
			Name:           u.Name,
			Color:          u.Color,
			HasPin:         u.HasPin(),
			IsKidsProfile:  u.IsKidsProfile,
			TraktAccountID: u.TraktAccountID,
			CreatedAt:      u.CreatedAt,
			UpdatedAt:      u.UpdatedAt,
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

// CreateProfileRequest represents a request to create a profile
type CreateProfileRequest struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// CreateProfile creates a new profile (admin can create for any account)
func (h *AdminUIHandler) CreateProfile(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	var req CreateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.usersService.Create(req.Name)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "required") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	// Set color if provided
	if req.Color != "" {
		user, err = h.usersService.SetColor(user.ID, req.Color)
		if err != nil {
			// Log but don't fail - profile was created
			log.Printf("[admin] failed to set color for new profile %s: %v", user.ID, err)
		}
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

// RenameProfileRequest represents a request to rename a profile
type RenameProfileRequest struct {
	Name string `json:"name"`
}

// RenameProfile renames a profile
func (h *AdminUIHandler) RenameProfile(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	var req RenameProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.usersService.Rename(profileID, req.Name)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "required") {
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

// DeleteProfile deletes a profile
func (h *AdminUIHandler) DeleteProfile(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	err := h.usersService.Delete(profileID)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "last") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// SetProfileColorRequest represents a request to set a profile's color
type SetProfileColorRequest struct {
	Color string `json:"color"`
}

// SetProfileColor updates a profile's color
func (h *AdminUIHandler) SetProfileColor(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	var req SetProfileColorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.usersService.SetColor(profileID, req.Color)
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
		ID:            user.ID,
		Name:          user.Name,
		Color:         user.Color,
		HasPin:        user.HasPin(),
		IsKidsProfile: user.IsKidsProfile,
		CreatedAt:     user.CreatedAt,
		UpdatedAt:     user.UpdatedAt,
	})
}

// SetKidsProfileRequest represents a request to set a profile's kids mode
type SetKidsProfileRequest struct {
	IsKidsProfile bool `json:"isKidsProfile"`
}

// SetKidsProfile updates a profile's kids mode flag
func (h *AdminUIHandler) SetKidsProfile(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	var req SetKidsProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.usersService.SetKidsProfile(profileID, req.IsKidsProfile)
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
		ID:            user.ID,
		Name:          user.Name,
		Color:         user.Color,
		HasPin:        user.HasPin(),
		IsKidsProfile: user.IsKidsProfile,
		CreatedAt:     user.CreatedAt,
		UpdatedAt:     user.UpdatedAt,
	})
}

// ============================================
// Account Management Handlers (Master Only)
// ============================================

// AdminAccountWithProfiles represents an account with its associated profiles for admin UI
type AdminAccountWithProfiles struct {
	ID        string        `json:"id"`
	Username  string        `json:"username"`
	IsMaster  bool          `json:"isMaster"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
	Profiles  []models.User `json:"profiles"`
}

// GetUserAccounts returns all user accounts with their profiles
func (h *AdminUIHandler) GetUserAccounts(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil {
		http.Error(w, "Accounts service not available", http.StatusInternalServerError)
		return
	}

	accountsList := h.accountsService.List()
	result := make([]AdminAccountWithProfiles, 0, len(accountsList))
	for _, acc := range accountsList {
		profiles := h.usersService.ListForAccount(acc.ID)
		result = append(result, AdminAccountWithProfiles{
			ID:        acc.ID,
			Username:  acc.Username,
			IsMaster:  acc.IsMaster,
			CreatedAt: acc.CreatedAt,
			UpdatedAt: acc.UpdatedAt,
			Profiles:  profiles,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"accounts": result,
	})
}

// AdminCreateAccountRequest represents a request to create a new account
type AdminCreateAccountRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CreateUserAccount creates a new user account
func (h *AdminUIHandler) CreateUserAccount(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil {
		http.Error(w, "Accounts service not available", http.StatusInternalServerError)
		return
	}

	var req AdminCreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	account, err := h.accountsService.Create(req.Username, req.Password)
	if err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrUsernameExists {
			status = http.StatusConflict
		} else if err == accounts.ErrUsernameRequired || err == accounts.ErrPasswordRequired {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(AdminAccountWithProfiles{
		ID:        account.ID,
		Username:  account.Username,
		IsMaster:  account.IsMaster,
		CreatedAt: account.CreatedAt,
		UpdatedAt: account.UpdatedAt,
		Profiles:  []models.User{},
	})
}

// DeleteUserAccount deletes an account
func (h *AdminUIHandler) DeleteUserAccount(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil {
		http.Error(w, "Accounts service not available", http.StatusInternalServerError)
		return
	}

	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		http.Error(w, "accountId parameter required", http.StatusBadRequest)
		return
	}

	// Revoke all sessions for this account
	if h.sessionsService != nil {
		h.sessionsService.RevokeAllForAccount(accountID)
	}

	if err := h.accountsService.Delete(accountID); err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrAccountNotFound {
			status = http.StatusNotFound
		} else if err == accounts.ErrCannotDeleteMaster || err == accounts.ErrCannotDeleteLastAcct {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ResetPasswordRequest represents a request to reset an account's password
type ResetPasswordRequest struct {
	NewPassword string `json:"newPassword"`
}

// ResetUserAccountPassword resets an account's password
func (h *AdminUIHandler) ResetUserAccountPassword(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil {
		http.Error(w, "Accounts service not available", http.StatusInternalServerError)
		return
	}

	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		http.Error(w, "accountId parameter required", http.StatusBadRequest)
		return
	}

	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.accountsService.UpdatePassword(accountID, req.NewPassword); err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrAccountNotFound {
			status = http.StatusNotFound
		} else if err == accounts.ErrPasswordRequired {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	// Revoke all sessions for this account (force re-login)
	if h.sessionsService != nil {
		h.sessionsService.RevokeAllForAccount(accountID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "password reset"})
}

// RenameAccountRequest represents a request to rename an account
type RenameAccountRequest struct {
	Username string `json:"username"`
}

// RenameUserAccount changes an account's username
func (h *AdminUIHandler) RenameUserAccount(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil {
		http.Error(w, "Accounts service not available", http.StatusInternalServerError)
		return
	}

	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		http.Error(w, "accountId parameter required", http.StatusBadRequest)
		return
	}

	var req RenameAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.accountsService.Rename(accountID, req.Username); err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrAccountNotFound {
			status = http.StatusNotFound
		} else if err == accounts.ErrUsernameRequired {
			status = http.StatusBadRequest
		} else if err == accounts.ErrUsernameExists {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Get the updated account
	account, _ := h.accountsService.Get(accountID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       account.ID,
		"username": account.Username,
		"isMaster": account.IsMaster,
	})
}

// AdminReassignProfileRequest represents a request to reassign a profile to a different account
type AdminReassignProfileRequest struct {
	AccountID string `json:"accountId"`
}

// ReassignProfile moves a profile to a different account
func (h *AdminUIHandler) ReassignProfile(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil || h.accountsService == nil {
		http.Error(w, "Services not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	var req AdminReassignProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verify target account exists
	if _, ok := h.accountsService.Get(req.AccountID); !ok {
		http.Error(w, "Target account not found", http.StatusNotFound)
		return
	}

	profile, err := h.usersService.Reassign(profileID, req.AccountID)
	if err != nil {
		status := http.StatusInternalServerError
		if err == users.ErrUserNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profile)
}

// HasDefaultPassword returns whether the master account has the default password
func (h *AdminUIHandler) HasDefaultPassword(w http.ResponseWriter, r *http.Request) {
	if h.accountsService == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"hasDefaultPassword": false})
		return
	}

	hasDefault := h.accountsService.HasDefaultPassword()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"hasDefaultPassword": hasDefault})
}

// InvitationResponse represents an invitation in API responses
type InvitationResponse struct {
	ID        string     `json:"id"`
	Token     string     `json:"token"`
	URL       string     `json:"url"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// CreateInvitationRequest represents a request to create an invitation
type CreateInvitationRequest struct {
	ExpiresInHours int `json:"expiresInHours"`
}

// ListInvitations returns all invitations
func (h *AdminUIHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	if h.invitationsService == nil {
		http.Error(w, "Invitations service not available", http.StatusInternalServerError)
		return
	}

	invs := h.invitationsService.List()
	result := make([]InvitationResponse, len(invs))

	// Build base URL from request
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if fwdProto := r.Header.Get("X-Forwarded-Proto"); fwdProto != "" {
		scheme = fwdProto
	}
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	for i, inv := range invs {
		result[i] = InvitationResponse{
			ID:        inv.ID,
			Token:     inv.Token,
			URL:       fmt.Sprintf("%s/register?token=%s", baseURL, inv.Token),
			ExpiresAt: inv.ExpiresAt,
			UsedAt:    inv.UsedAt,
			CreatedAt: inv.CreatedAt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"invitations": result,
	})
}

// CreateInvitation creates a new invitation link
func (h *AdminUIHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	if h.invitationsService == nil {
		http.Error(w, "Invitations service not available", http.StatusInternalServerError)
		return
	}

	session := h.getSession(r)
	if session == nil {
		http.Error(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req CreateInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Default to 7 days if not specified
		req.ExpiresInHours = 168
	}

	if req.ExpiresInHours <= 0 {
		req.ExpiresInHours = 168 // 7 days
	}

	expiresIn := time.Duration(req.ExpiresInHours) * time.Hour
	inv, err := h.invitationsService.Create(session.AccountID, expiresIn)
	if err != nil {
		http.Error(w, "Failed to create invitation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Build URL
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if fwdProto := r.Header.Get("X-Forwarded-Proto"); fwdProto != "" {
		scheme = fwdProto
	}
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(InvitationResponse{
		ID:        inv.ID,
		Token:     inv.Token,
		URL:       fmt.Sprintf("%s/register?token=%s", baseURL, inv.Token),
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	})
}

// DeleteInvitation deletes an invitation
func (h *AdminUIHandler) DeleteInvitation(w http.ResponseWriter, r *http.Request) {
	if h.invitationsService == nil {
		http.Error(w, "Invitations service not available", http.StatusInternalServerError)
		return
	}

	invitationID := r.URL.Query().Get("invitationId")
	if invitationID == "" {
		http.Error(w, "invitationId parameter required", http.StatusBadRequest)
		return
	}

	if err := h.invitationsService.Delete(invitationID); err != nil {
		status := http.StatusInternalServerError
		if err == invitations.ErrInvitationNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ValidateInvitation checks if an invitation token is valid (public endpoint)
func (h *AdminUIHandler) ValidateInvitation(w http.ResponseWriter, r *http.Request) {
	if h.invitationsService == nil {
		http.Error(w, "Invitations service not available", http.StatusInternalServerError)
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"valid": false, "error": "token parameter required"})
		return
	}

	err := h.invitationsService.Validate(token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"valid": false, "error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"valid": true})
}

// RegisterWithInvitationRequest represents a request to register using an invitation
type RegisterWithInvitationRequest struct {
	Token           string `json:"token"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
}

// RegisterWithInvitation creates a new account using an invitation token (public endpoint)
func (h *AdminUIHandler) RegisterWithInvitation(w http.ResponseWriter, r *http.Request) {
	if h.invitationsService == nil || h.accountsService == nil {
		http.Error(w, "Services not available", http.StatusInternalServerError)
		return
	}

	var req RegisterWithInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request body"})
		return
	}

	// Validate passwords match
	if req.Password != req.ConfirmPassword {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Passwords do not match"})
		return
	}

	// Validate the invitation token
	if err := h.invitationsService.Validate(req.Token); err != nil {
		status := http.StatusBadRequest
		if err == invitations.ErrInvitationNotFound {
			status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Create the account
	account, err := h.accountsService.Create(req.Username, req.Password)
	if err != nil {
		status := http.StatusInternalServerError
		if err == accounts.ErrUsernameExists {
			status = http.StatusConflict
		} else if err == accounts.ErrUsernameRequired || err == accounts.ErrPasswordRequired {
			status = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Mark the invitation as used
	if err := h.invitationsService.MarkUsed(req.Token, account.ID); err != nil {
		// Log the error but don't fail - account was already created
		fmt.Printf("Warning: failed to mark invitation as used: %v\n", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Account created successfully. You can now log in.",
	})
}

// ClearMetadataCache clears all cached metadata files
func (h *AdminUIHandler) ClearMetadataCache(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.metadataService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "metadata service not available"})
		return
	}
	if err := h.metadataService.ClearCache(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[admin] metadata cache cleared by user request")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Metadata cache cleared"})
}

// GetWatchHistory returns watch history for a user (admin session auth)
// Supports pagination via query params: page (default 1), pageSize (default 50), mediaType (optional filter)
func (h *AdminUIHandler) GetWatchHistory(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "userId parameter required"})
		return
	}

	if h.historyService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "history service not available"})
		return
	}

	// Parse pagination params
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	pageSize := 50
	if ps := r.URL.Query().Get("pageSize"); ps != "" {
		if parsed, err := strconv.Atoi(ps); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}

	mediaTypeFilter := r.URL.Query().Get("mediaType")

	result, err := h.historyService.ListWatchHistoryPaginated(userID, page, pageSize, mediaTypeFilter)
	if err != nil {
		log.Printf("[admin] GetWatchHistory error for user %s: %v", userID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// GetContinueWatching returns continue watching items for a user (admin session auth)
func (h *AdminUIHandler) GetContinueWatching(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "userId parameter required"})
		return
	}

	if h.historyService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "history service not available"})
		return
	}

	items, err := h.historyService.ListContinueWatching(userID)
	if err != nil {
		log.Printf("[admin] GetContinueWatching error for user %s: %v", userID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// TestSubtitlesRequest represents a request to test OpenSubtitles credentials
type TestSubtitlesRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// TestSubtitles tests OpenSubtitles.org credentials by attempting to log in
func (h *AdminUIHandler) TestSubtitles(w http.ResponseWriter, r *http.Request) {
	var req TestSubtitlesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if req.Username == "" || req.Password == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Username and password are required",
		})
		return
	}

	// OpenSubtitles.org uses XML-RPC API at https://api.opensubtitles.org/xml-rpc
	// We'll test by calling the LogIn method
	client := &http.Client{Timeout: 15 * time.Second}

	// Build XML-RPC request for LogIn
	xmlPayload := fmt.Sprintf(`<?xml version="1.0"?>
<methodCall>
  <methodName>LogIn</methodName>
  <params>
    <param><value><string>%s</string></value></param>
    <param><value><string>%s</string></value></param>
    <param><value><string>en</string></value></param>
    <param><value><string>strmr v1.0</string></value></param>
  </params>
</methodCall>`, req.Username, req.Password)

	apiReq, err := http.NewRequest(http.MethodPost, "https://api.opensubtitles.org/xml-rpc", strings.NewReader(xmlPayload))
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	apiReq.Header.Set("Content-Type", "text/xml")

	resp, err := client.Do(apiReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to read response: %v", err),
		})
		return
	}

	bodyStr := string(body)

	// Check for successful login (status 200 OK in the XML response)
	if strings.Contains(bodyStr, "<name>status</name>") && strings.Contains(bodyStr, "200 OK") {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "OpenSubtitles login successful",
		})
		return
	}

	// Check for common error statuses
	if strings.Contains(bodyStr, "401") {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid username or password",
		})
		return
	}

	if strings.Contains(bodyStr, "411") {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid user agent (API blocked)",
		})
		return
	}

	// Generic failure
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"error":   "Login failed - check credentials",
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

	case "alldebrid":
		// Test AllDebrid by getting user info
		apiReq, err := http.NewRequest(http.MethodGet, "https://api.alldebrid.com/v4/user?agent=strmr", nil)
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

		var adResult struct {
			Status string `json:"status"`
			Data   struct {
				User struct {
					Username  string `json:"username"`
					Email     string `json:"email"`
					IsPremium bool   `json:"isPremium"`
				} `json:"user"`
			} `json:"data"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&adResult); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"message": "AllDebrid API is reachable",
			})
			return
		}

		if adResult.Status != "success" {
			errMsg := "Unknown error"
			if adResult.Error != nil {
				errMsg = adResult.Error.Message
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   errMsg,
			})
			return
		}

		accountType := "Free"
		if adResult.Data.User.IsPremium {
			accountType = "Premium"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Connected as %s (%s)", adResult.Data.User.Username, accountType),
		})

	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Unknown provider: %s", req.Provider),
		})
	}
}

// ToolsPage serves the tools page
func (h *AdminUIHandler) ToolsPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)
	usersList := h.getScopedUsers(isAdmin, accountID)

	data := AdminPageData{
		CurrentPath: basePath + "/tools",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Users:       usersList,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.toolsTemplate == nil {
		http.Error(w, "Tools template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.toolsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Tools template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// SearchPage serves the search test page
func (h *AdminUIHandler) SearchPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	usersList := h.getScopedUsers(isAdmin, accountID)

	data := AdminPageData{
		CurrentPath: basePath + "/search",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Settings:    settings,
		Users:       usersList,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.searchTemplate == nil {
		http.Error(w, "Search template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.searchTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Search template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// AccountsPage serves the account management page
func (h *AdminUIHandler) AccountsPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	data := AdminPageData{
		CurrentPath: basePath + "/accounts",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Settings:    settings,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.accountsTemplate == nil {
		http.Error(w, "Accounts template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.accountsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Accounts template error: %v\n", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// PlexCreatePIN creates a new Plex OAuth PIN
func (h *AdminUIHandler) PlexCreatePIN(w http.ResponseWriter, r *http.Request) {
	if h.plexClient == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Plex client not initialized",
		})
		return
	}

	pin, err := h.plexClient.CreatePIN()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      pin.ID,
		"code":    pin.Code,
		"authUrl": h.plexClient.GetAuthURL(pin.Code),
	})
}

// PlexCheckPIN checks the status of a Plex OAuth PIN
func (h *AdminUIHandler) PlexCheckPIN(w http.ResponseWriter, r *http.Request) {
	if h.plexClient == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Plex client not initialized",
		})
		return
	}

	// Get PIN ID from URL path
	path := r.URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "PIN ID required",
		})
		return
	}

	pinIDStr := parts[len(parts)-1]
	pinID, err := strconv.Atoi(pinIDStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid PIN ID",
		})
		return
	}

	pin, err := h.plexClient.CheckPIN(pinID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if pin.AuthToken != "" {
		// Authentication successful, also get user info
		userInfo, _ := h.plexClient.GetUserInfo(pin.AuthToken)

		// Save auth token to settings for persistence
		settings, err := h.configManager.Load()
		if err == nil {
			settings.Plex.AuthToken = pin.AuthToken
			if userInfo != nil {
				settings.Plex.Username = userInfo.Username
			}
			h.configManager.Save(settings)
		}

		response := map[string]interface{}{
			"authenticated": true,
			"authToken":     pin.AuthToken,
		}
		if userInfo != nil {
			response["username"] = userInfo.Username
			response["email"] = userInfo.Email
		}
		json.NewEncoder(w).Encode(response)
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
			"pending":       true,
		})
	}
}

// PlexGetStatus returns the current Plex connection status
func (h *AdminUIHandler) PlexGetStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	response := map[string]interface{}{
		"connected": settings.Plex.AuthToken != "",
		"username":  settings.Plex.Username,
	}

	// If we have a token, verify it's still valid
	if settings.Plex.AuthToken != "" && h.plexClient != nil {
		userInfo, err := h.plexClient.GetUserInfo(settings.Plex.AuthToken)
		if err != nil {
			// Token is invalid, clear it
			settings.Plex.AuthToken = ""
			settings.Plex.Username = ""
			h.configManager.Save(settings)
			response["connected"] = false
			response["username"] = ""
		} else if userInfo != nil {
			response["username"] = userInfo.Username
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// PlexDisconnect removes the saved Plex auth token
func (h *AdminUIHandler) PlexDisconnect(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	settings.Plex.AuthToken = ""
	settings.Plex.Username = ""

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// PlexGetWatchlist retrieves the user's Plex watchlist
func (h *AdminUIHandler) PlexGetWatchlist(w http.ResponseWriter, r *http.Request) {
	if h.plexClient == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Plex client not initialized",
		})
		return
	}

	// Try header first, fall back to stored token
	authToken := r.Header.Get("X-Plex-Token")
	if authToken == "" {
		settings, err := h.configManager.Load()
		if err == nil && settings.Plex.AuthToken != "" {
			authToken = settings.Plex.AuthToken
		}
	}
	if authToken == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Not connected to Plex",
		})
		return
	}

	items, err := h.plexClient.GetWatchlist(authToken)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Convert to a normalized format with external IDs
	normalizedItems := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		// Try to get external IDs from item details
		externalIDs, _ := h.plexClient.GetItemDetails(authToken, item.RatingKey)
		if externalIDs == nil {
			externalIDs = plex.ParseGUID(item.GUID)
		}

		normalizedItems = append(normalizedItems, map[string]interface{}{
			"ratingKey":   item.RatingKey,
			"title":       item.Title,
			"type":        plex.NormalizeMediaType(item.Type),
			"year":        item.Year,
			"posterUrl":   plex.GetPosterURL(item.Thumb, authToken),
			"backdropUrl": plex.GetPosterURL(item.Art, authToken),
			"externalIds": externalIDs,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items": normalizedItems,
		"count": len(normalizedItems),
	})
}

// PlexImportWatchlist imports selected items to strmr watchlist
func (h *AdminUIHandler) PlexImportWatchlist(w http.ResponseWriter, r *http.Request) {
	if h.watchlistService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Watchlist service not initialized",
		})
		return
	}

	var req struct {
		ProfileID string `json:"profileId"`
		Items     []struct {
			RatingKey   string            `json:"ratingKey"`
			Title       string            `json:"title"`
			MediaType   string            `json:"type"`
			Year        int               `json:"year"`
			PosterURL   string            `json:"posterUrl"`
			BackdropURL string            `json:"backdropUrl"`
			ExternalIDs map[string]string `json:"externalIds"`
		} `json:"items"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.ProfileID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Profile ID required",
		})
		return
	}

	if len(req.Items) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "No items to import",
		})
		return
	}

	successCount := 0
	errorCount := 0
	var errors []string

	ctx := r.Context()

	for _, item := range req.Items {
		// Determine the best ID to use - prefer TMDB, then IMDB, then Plex ratingKey
		itemID := item.RatingKey
		if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		}

		// Fetch overview from metadata service if available
		var overview string
		if h.metadataService != nil {
			overview = h.fetchOverviewForItem(ctx, item.MediaType, item.Title, item.Year, item.ExternalIDs)
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   item.MediaType,
			Name:        item.Title,
			Overview:    overview,
			Year:        item.Year,
			PosterURL:   item.PosterURL,
			BackdropURL: item.BackdropURL,
			ExternalIDs: item.ExternalIDs,
		}

		_, err := h.watchlistService.AddOrUpdate(req.ProfileID, input)
		if err != nil {
			errorCount++
			errors = append(errors, fmt.Sprintf("%s: %v", item.Title, err))
		} else {
			successCount++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"success":      errorCount == 0,
		"imported":     successCount,
		"failed":       errorCount,
		"totalItems":   len(req.Items),
	}
	if len(errors) > 0 {
		response["errors"] = errors
	}
	json.NewEncoder(w).Encode(response)
}

// fetchOverviewForItem fetches the overview/description for a watchlist item from metadata service
func (h *AdminUIHandler) fetchOverviewForItem(ctx context.Context, mediaType, name string, year int, externalIDs map[string]string) string {
	if h.metadataService == nil {
		return ""
	}

	// Parse external IDs
	var tmdbID, tvdbID int64
	var imdbID string
	if id, ok := externalIDs["tmdb"]; ok && id != "" {
		if parsed, err := strconv.ParseInt(id, 10, 64); err == nil {
			tmdbID = parsed
		}
	}
	if id, ok := externalIDs["tvdb"]; ok && id != "" {
		if parsed, err := strconv.ParseInt(id, 10, 64); err == nil {
			tvdbID = parsed
		}
	}
	if id, ok := externalIDs["imdb"]; ok && id != "" {
		imdbID = id
	}

	switch strings.ToLower(mediaType) {
	case "movie":
		query := models.MovieDetailsQuery{
			Name:   name,
			Year:   year,
			IMDBID: imdbID,
			TMDBID: tmdbID,
			TVDBID: tvdbID,
		}
		if title, err := h.metadataService.MovieDetails(ctx, query); err == nil && title != nil {
			return title.Overview
		}
	case "series", "show", "tv":
		query := models.SeriesDetailsQuery{
			Name:   name,
			Year:   year,
			TMDBID: tmdbID,
			TVDBID: tvdbID,
		}
		if title, err := h.metadataService.SeriesInfo(ctx, query); err == nil && title != nil {
			return title.Overview
		}
	}

	return ""
}

// --- Trakt Integration Handlers ---

// ensureValidTraktToken checks if the Trakt access token is valid and refreshes it if needed.
// Returns the valid access token or an error. Also updates client credentials.
func (h *AdminUIHandler) ensureValidTraktToken() (string, error) {
	settings, err := h.configManager.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load settings: %w", err)
	}

	if settings.Trakt.AccessToken == "" {
		return "", fmt.Errorf("not connected to Trakt")
	}

	// Always update client with current credentials
	h.traktClient.UpdateCredentials(settings.Trakt.ClientID, settings.Trakt.ClientSecret)

	// Check if token is expired or will expire within 1 hour
	if settings.Trakt.ExpiresAt > 0 {
		expiresIn := settings.Trakt.ExpiresAt - time.Now().Unix()
		if expiresIn < 3600 { // Less than 1 hour remaining
			// Need to refresh the token
			if settings.Trakt.RefreshToken == "" {
				return "", fmt.Errorf("token expired and no refresh token available")
			}

			token, err := h.traktClient.RefreshAccessToken(settings.Trakt.RefreshToken)
			if err != nil {
				return "", fmt.Errorf("failed to refresh token: %w", err)
			}

			// Update settings with new tokens
			settings.Trakt.AccessToken = token.AccessToken
			settings.Trakt.RefreshToken = token.RefreshToken
			settings.Trakt.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)

			if err := h.configManager.Save(settings); err != nil {
				return "", fmt.Errorf("failed to save refreshed token: %w", err)
			}

			return token.AccessToken, nil
		}
	}

	return settings.Trakt.AccessToken, nil
}

// TraktGetStatus returns the current Trakt connection status
func (h *AdminUIHandler) TraktGetStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	response := map[string]interface{}{
		"hasCredentials":    settings.Trakt.ClientID != "" && settings.Trakt.ClientSecret != "",
		"connected":         settings.Trakt.AccessToken != "",
		"username":          settings.Trakt.Username,
		"scrobblingEnabled": settings.Trakt.ScrobblingEnabled,
	}

	// Check if token is expired
	if settings.Trakt.AccessToken != "" && settings.Trakt.ExpiresAt > 0 {
		response["expiresAt"] = settings.Trakt.ExpiresAt
		response["expired"] = time.Now().Unix() > settings.Trakt.ExpiresAt
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// TraktSaveCredentials saves Trakt API credentials to settings
func (h *AdminUIHandler) TraktSaveCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	settings.Trakt.ClientID = req.ClientID
	settings.Trakt.ClientSecret = req.ClientSecret

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	// Update the trakt client with new credentials
	h.traktClient.UpdateCredentials(req.ClientID, req.ClientSecret)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// TraktStartAuth initiates the Trakt device code OAuth flow
func (h *AdminUIHandler) TraktStartAuth(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	if settings.Trakt.ClientID == "" || settings.Trakt.ClientSecret == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Trakt credentials not configured. Please save your Client ID and Client Secret first.",
		})
		return
	}

	// Update client with current credentials
	h.traktClient.UpdateCredentials(settings.Trakt.ClientID, settings.Trakt.ClientSecret)

	deviceCode, err := h.traktClient.GetDeviceCode()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deviceCode":      deviceCode.DeviceCode,
		"userCode":        deviceCode.UserCode,
		"verificationUrl": deviceCode.VerificationURL,
		"expiresIn":       deviceCode.ExpiresIn,
		"interval":        deviceCode.Interval,
	})
}

// TraktCheckAuth polls for Trakt OAuth token
func (h *AdminUIHandler) TraktCheckAuth(w http.ResponseWriter, r *http.Request) {
	// Get device code from URL path
	path := r.URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Device code required",
		})
		return
	}

	deviceCode := parts[len(parts)-1]
	if deviceCode == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Device code required",
		})
		return
	}

	token, err := h.traktClient.PollForToken(deviceCode)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if token == nil {
		// Still pending
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
			"pending":       true,
		})
		return
	}

	// Token received, save to settings
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	settings.Trakt.AccessToken = token.AccessToken
	settings.Trakt.RefreshToken = token.RefreshToken
	settings.Trakt.ExpiresAt = token.CreatedAt + int64(token.ExpiresIn)

	// Get user profile
	profile, err := h.traktClient.GetUserProfile(token.AccessToken)
	if err == nil && profile != nil {
		settings.Trakt.Username = profile.Username
	}

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save token: " + err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": true,
		"username":      settings.Trakt.Username,
	})
}

// TraktDisconnect removes Trakt OAuth tokens from settings
func (h *AdminUIHandler) TraktDisconnect(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	settings.Trakt.AccessToken = ""
	settings.Trakt.RefreshToken = ""
	settings.Trakt.ExpiresAt = 0
	settings.Trakt.Username = ""

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// TraktSetScrobbling enables or disables Trakt scrobbling
func (h *AdminUIHandler) TraktSetScrobbling(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	settings.Trakt.ScrobblingEnabled = req.Enabled

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":           true,
		"scrobblingEnabled": req.Enabled,
	})
}

// TraktGetWatchlist retrieves the user's Trakt watchlist
func (h *AdminUIHandler) TraktGetWatchlist(w http.ResponseWriter, r *http.Request) {
	accessToken, err := h.ensureValidTraktToken()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	items, err := h.traktClient.GetAllWatchlist(accessToken)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Convert to normalized format
	normalizedItems := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		normalized := map[string]interface{}{
			"type":     trakt.NormalizeMediaType(item.Type),
			"listedAt": item.ListedAt,
		}

		if item.Movie != nil {
			normalized["title"] = item.Movie.Title
			normalized["year"] = item.Movie.Year
			normalized["externalIds"] = trakt.IDsToMap(item.Movie.IDs)
		} else if item.Show != nil {
			normalized["title"] = item.Show.Title
			normalized["year"] = item.Show.Year
			normalized["externalIds"] = trakt.IDsToMap(item.Show.IDs)
		}

		normalizedItems = append(normalizedItems, normalized)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items": normalizedItems,
		"count": len(normalizedItems),
	})
}

// normalizeTraktHistoryItems converts Trakt history items to normalized format
func (h *AdminUIHandler) normalizeTraktHistoryItems(items []trakt.HistoryItem) []map[string]interface{} {
	normalizedItems := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		normalized := map[string]interface{}{
			"id":        item.ID,
			"type":      trakt.NormalizeMediaType(item.Type),
			"watchedAt": item.WatchedAt,
			"action":    item.Action,
		}

		if item.Movie != nil {
			normalized["title"] = item.Movie.Title
			normalized["year"] = item.Movie.Year
			normalized["externalIds"] = trakt.IDsToMap(item.Movie.IDs)
		} else if item.Episode != nil && item.Show != nil {
			normalized["title"] = fmt.Sprintf("%s - S%02dE%02d - %s", item.Show.Title, item.Episode.Season, item.Episode.Number, item.Episode.Title)
			normalized["seriesTitle"] = item.Show.Title
			normalized["episodeTitle"] = item.Episode.Title
			normalized["season"] = item.Episode.Season
			normalized["episode"] = item.Episode.Number
			normalized["year"] = item.Show.Year
			normalized["externalIds"] = trakt.IDsToMap(item.Show.IDs)
			normalized["episodeIds"] = trakt.IDsToMap(item.Episode.IDs)
		}

		normalizedItems = append(normalizedItems, normalized)
	}
	return normalizedItems
}

// TraktGetHistory retrieves the user's Trakt watch history
func (h *AdminUIHandler) TraktGetHistory(w http.ResponseWriter, r *http.Request) {
	accessToken, err := h.ensureValidTraktToken()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Check if all items requested
	if r.URL.Query().Get("all") == "true" {
		items, err := h.traktClient.GetAllWatchHistory(accessToken)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": err.Error(),
			})
			return
		}

		normalizedItems := h.normalizeTraktHistoryItems(items)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items":      normalizedItems,
			"count":      len(normalizedItems),
			"totalCount": len(normalizedItems),
		})
		return
	}

	// Parse pagination params
	page := 1
	limit := 100
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	items, totalCount, err := h.traktClient.GetWatchHistory(accessToken, page, limit, "")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	normalizedItems := h.normalizeTraktHistoryItems(items)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items":      normalizedItems,
		"count":      len(normalizedItems),
		"totalCount": totalCount,
		"page":       page,
		"limit":      limit,
	})
}

// TraktImportWatchlist imports selected Trakt watchlist items to strmr watchlist
func (h *AdminUIHandler) TraktImportWatchlist(w http.ResponseWriter, r *http.Request) {
	if h.watchlistService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Watchlist service not initialized",
		})
		return
	}

	var req struct {
		ProfileID string `json:"profileId"`
		Items     []struct {
			Title       string            `json:"title"`
			MediaType   string            `json:"type"`
			Year        int               `json:"year"`
			ExternalIDs map[string]string `json:"externalIds"`
		} `json:"items"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.ProfileID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Profile ID required",
		})
		return
	}

	if len(req.Items) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "No items to import",
		})
		return
	}

	successCount := 0
	errorCount := 0
	var errors []string

	ctx := r.Context()

	for _, item := range req.Items {
		// Determine the best ID to use - prefer TMDB, then IMDB, then Trakt
		itemID := ""
		if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
			itemID = tmdbID
		} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
			itemID = imdbID
		} else if traktID, ok := item.ExternalIDs["trakt"]; ok && traktID != "" {
			itemID = traktID
		}

		if itemID == "" {
			errorCount++
			errors = append(errors, fmt.Sprintf("%s: no valid ID found", item.Title))
			continue
		}

		// Fetch overview from metadata service if available
		var overview string
		if h.metadataService != nil {
			overview = h.fetchOverviewForItem(ctx, item.MediaType, item.Title, item.Year, item.ExternalIDs)
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   item.MediaType,
			Name:        item.Title,
			Overview:    overview,
			Year:        item.Year,
			ExternalIDs: item.ExternalIDs,
		}

		_, err := h.watchlistService.AddOrUpdate(req.ProfileID, input)
		if err != nil {
			errorCount++
			errors = append(errors, fmt.Sprintf("%s: %v", item.Title, err))
		} else {
			successCount++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"success":    errorCount == 0,
		"imported":   successCount,
		"failed":     errorCount,
		"totalItems": len(req.Items),
	}
	if len(errors) > 0 {
		response["errors"] = errors
	}
	json.NewEncoder(w).Encode(response)
}

// TraktImportHistory imports Trakt watch history to local history
func (h *AdminUIHandler) TraktImportHistory(w http.ResponseWriter, r *http.Request) {
	if h.historyService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "History service not initialized",
		})
		return
	}

	var req struct {
		ProfileID string `json:"profileId"`
		Items     []struct {
			ID           int64             `json:"id"`
			Title        string            `json:"title"`
			MediaType    string            `json:"type"`
			Year         int               `json:"year"`
			WatchedAt    time.Time         `json:"watchedAt"`
			ExternalIDs  map[string]string `json:"externalIds"`
			SeriesTitle  string            `json:"seriesTitle,omitempty"`
			EpisodeTitle string            `json:"episodeTitle,omitempty"`
			Season       int               `json:"season,omitempty"`
			Episode      int               `json:"episode,omitempty"`
		} `json:"items"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.ProfileID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Profile ID required",
		})
		return
	}

	if len(req.Items) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "No items to import",
		})
		return
	}

	successCount := 0
	errorCount := 0
	var errors []string

	watched := true
	for _, item := range req.Items {
		var itemID string
		var seriesID string

		if item.MediaType == "movie" {
			// For movies, use TMDB > IMDB > TVDB
			if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
				itemID = tmdbID
			} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
				itemID = imdbID
			} else if tvdbID, ok := item.ExternalIDs["tvdb"]; ok && tvdbID != "" {
				itemID = tvdbID
			}
		} else if item.MediaType == "episode" {
			// For episodes, construct composite ID like tmdb:tv:SHOWID:S01E02
			if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
				seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
				itemID = fmt.Sprintf("tmdb:tv:%s:S%02dE%02d", tmdbID, item.Season, item.Episode)
			} else if tvdbID, ok := item.ExternalIDs["tvdb"]; ok && tvdbID != "" {
				seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
				itemID = fmt.Sprintf("tvdb:series:%s:S%02dE%02d", tvdbID, item.Season, item.Episode)
			} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
				seriesID = fmt.Sprintf("imdb:%s", imdbID)
				itemID = fmt.Sprintf("imdb:%s:S%02dE%02d", imdbID, item.Season, item.Episode)
			}
		}

		if itemID == "" {
			errorCount++
			errors = append(errors, fmt.Sprintf("%s: no valid ID found", item.Title))
			continue
		}

		historyItem := models.WatchHistoryUpdate{
			MediaType:   item.MediaType,
			ItemID:      itemID,
			Watched:     &watched,
			WatchedAt:   item.WatchedAt,
			ExternalIDs: item.ExternalIDs,
		}

		// For movies, set the name
		if item.MediaType == "movie" {
			historyItem.Name = item.Title
			historyItem.Year = item.Year
		} else if item.MediaType == "episode" {
			// For episodes, set series and episode info
			historyItem.SeriesID = seriesID
			historyItem.SeriesName = item.SeriesTitle
			historyItem.Name = item.EpisodeTitle
			historyItem.SeasonNumber = item.Season
			historyItem.EpisodeNumber = item.Episode
			historyItem.Year = item.Year
		}

		_, err := h.historyService.UpdateWatchHistory(req.ProfileID, historyItem)
		if err != nil {
			errorCount++
			errors = append(errors, fmt.Sprintf("%s: %v", item.Title, err))
		} else {
			successCount++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"success":    errorCount == 0,
		"imported":   successCount,
		"failed":     errorCount,
		"totalItems": len(req.Items),
	}
	if len(errors) > 0 {
		response["errors"] = errors
	}
	json.NewEncoder(w).Encode(response)
}

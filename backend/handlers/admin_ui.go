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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/calendar"
	"novastream/services/debrid"
	"novastream/services/history"
	"novastream/services/invitations"
	"novastream/services/metadata"
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
	{"id": "providers", "label": "Providers"},
	{"id": "sources", "label": "Sources"},
	{"id": "searchFiltering", "label": "Search & Filtering"},
	{"id": "services", "label": "Services"},
	{"id": "experience", "label": "Experience"},
}

// SettingsSchema defines the schema for dynamic form generation
var SettingsSchema = map[string]interface{}{
	"server": map[string]interface{}{
		"label":  "Server Settings",
		"icon":   "server",
		"group":  "server",
		"order":  0,
		"hidden": true,
		"fields": map[string]interface{}{
			"host": map[string]interface{}{"type": "text", "label": "Host", "description": "Server bind address"},
			"port": map[string]interface{}{"type": "number", "label": "Port", "description": "Server port"},
		},
	},
	"network": map[string]interface{}{
		"label":  "Network URL Switching",
		"icon":   "wifi",
		"group":  "server",
		"order":  1,
		"hidden": true,
		"fields": map[string]interface{}{
			"homeWifiSSID": map[string]interface{}{
				"type":        "text",
				"label":       "Home WiFi Name",
				"description": "WiFi network name (SSID) to detect for home network. When connected, app uses Home URL.",
				"placeholder": "MyHomeWiFi",
				"order":       0,
			},
			"homeBackendUrl": map[string]interface{}{
				"type":        "text",
				"label":       "Home Backend URL",
				"description": "Backend URL to use when connected to home WiFi (e.g., http://192.168.1.100:7777/api)",
				"placeholder": "http://192.168.1.100:7777/api",
				"order":       1,
			},
			"remoteBackendUrl": map[string]interface{}{
				"type":        "text",
				"label":       "Remote Backend URL",
				"description": "Backend URL to use when on mobile data or other networks (e.g., https://myserver.example.com:7777/api)",
				"placeholder": "https://myserver.example.com:7777/api",
				"order":       2,
			},
		},
	},
	"streaming": map[string]interface{}{
		"label": "Search & Resolution",
		"icon":  "search",
		"group": "searchFiltering",
		"order": 0,
		"fields": map[string]interface{}{
			"maxDownloadWorkers": map[string]interface{}{"type": "number", "label": "Max Download Workers", "description": "Maximum concurrent download workers", "hidden": true},
			"maxCacheSizeMB":     map[string]interface{}{"type": "number", "label": "Max Cache Size (MB)", "description": "Maximum cache size in megabytes", "hidden": true},
			"serviceMode":        map[string]interface{}{"type": "select", "label": "Service Mode", "options": []string{"usenet", "debrid", "hybrid"}, "description": "Streaming service mode"},
			"servicePriority": map[string]interface{}{
				"type":        "select",
				"label":       "Service Priority",
				"description": "Prioritize results from a specific service type",
				"options":     []string{"none", "usenet", "debrid"},
			},
			"searchMode": map[string]interface{}{
				"type":        "select",
				"label":       "Search Mode",
				"description": "Fast returns results quickly (early return), Accurate waits for all scrapers/indexers",
				"options":     []string{"fast", "accurate"},
			},
			"multiProviderMode": map[string]interface{}{
				"type":        "select",
				"label":       "Multi-Provider Mode",
				"description": "How to select provider when multiple debrid providers are enabled",
				"options":     []string{"fastest", "preferred"},
			},
			"usenetResolutionTimeoutSec": map[string]interface{}{
				"type":        "number",
				"label":       "Usenet Resolution Timeout (seconds)",
				"description": "Maximum time to wait for usenet content resolution (0 = no limit)",
			},
			"indexerTimeoutSec": map[string]interface{}{
				"type":        "number",
				"label":       "Search Timeout (seconds)",
				"description": "Maximum time to wait for indexer/scraper searches (default: 5). Increase if using Aiostreams, which may need more time to respond.",
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
			"name":     map[string]interface{}{"type": "text", "label": "Name", "description": "Provider display name", "order": 1},
			"provider": map[string]interface{}{"type": "select", "label": "Provider", "options": []string{"realdebrid", "torbox", "alldebrid"}, "description": "Provider type", "order": 2},
			"apiKey":   map[string]interface{}{"type": "password", "label": "API Key", "description": "Provider API key", "order": 3},
			"enabled":  map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this provider", "order": 4},
			"config.autoClearQueue": map[string]interface{}{
				"type":        "boolean",
				"label":       "Auto-Clear Queue (Torbox)",
				"description": "Clear downloading torrents when hitting active download limit",
				"order":       5,
				"showWhen":    "provider=torbox",
			},
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
		"group": "searchFiltering",
		"order": 1,
		"fields": map[string]interface{}{
			"maxSizeMovieGb":   map[string]interface{}{"type": "number", "label": "Max Movie Size (GB)", "description": "Maximum movie file size (0 = no limit)"},
			"maxSizeEpisodeGb": map[string]interface{}{"type": "number", "label": "Max Episode Size (GB)", "description": "Maximum episode file size (0 = no limit)"},
			"maxResolution":    map[string]interface{}{"type": "select", "label": "Max Resolution", "options": []string{"", "480p", "720p", "1080p", "2160p"}, "description": "Maximum resolution (empty = no limit)"},
			"hdrDvPolicy": map[string]interface{}{
				"type":  "select",
				"label": "HDR/DV Policy",
				"options": []map[string]string{
					{"value": "hdr_dv", "label": "All content"},
					{"value": "hdr", "label": "SDR + HDR only"},
					{"value": "none", "label": "SDR only"},
				},
				"description": "Content filtering: 'All content' allows everything. 'SDR + HDR only' excludes DV profile 5 (detected at probe time). 'SDR only' excludes all HDR/DV content.",
			},
			"prioritizeHdr":                    map[string]interface{}{"type": "boolean", "label": "Prioritize HDR", "description": "Prioritize HDR/DV content in results"},
			"filterOutTerms":                   map[string]interface{}{"type": "tags", "label": "Filter Out Terms", "description": "Terms to exclude from results (case-insensitive substring match; wrap in /slashes/ for regex, e.g. /\\bDUB\\b/)"},
			"preferredTerms":                   map[string]interface{}{"type": "tags", "label": "Preferred Terms", "description": "Terms to prioritize in results (case-insensitive substring match, ranked higher; wrap in /slashes/ for regex)"},
			"nonPreferredTerms":               map[string]interface{}{"type": "tags", "label": "Non-Preferred Terms", "description": "Terms to derank in results (case-insensitive substring match, ranked lower but not removed; wrap in /slashes/ for regex)"},
			"bypassFilteringForAioStreamsOnly": map[string]interface{}{"type": "boolean", "label": "Bypass Filtering for AIOStreams Only", "description": "Skip strmr filtering/ranking when AIOStreams is the only enabled scraper in debrid-only mode (use AIOStreams' own ranking). Does not apply in hybrid mode with usenet."},
		},
	},
	"animeFiltering": map[string]interface{}{
		"label": "Anime Language",
		"icon":  "globe",
		"group": "searchFiltering",
		"order": 2,
		"fields": map[string]interface{}{
			"animeLanguageEnabled": map[string]interface{}{"type": "boolean", "label": "Anime Language Preference", "description": "When enabled, automatically boost releases matching your preferred language, derank other languages, and filter out raw (unsubtitled) releases for anime content"},
			"animePreferredLanguage": map[string]interface{}{
				"type":  "select",
				"label": "Preferred Anime Language",
				"options": []map[string]string{
					{"value": "eng", "label": "English"},
					{"value": "spa", "label": "Spanish"},
					{"value": "fra", "label": "French"},
					{"value": "deu", "label": "German"},
					{"value": "ita", "label": "Italian"},
					{"value": "por", "label": "Portuguese"},
					{"value": "jpn", "label": "Japanese"},
				},
				"description": "Language to prioritize for anime releases (boosts 'dual audio', preferred language terms; deranks other language terms)",
				"showWhen":    map[string]interface{}{"field": "animeLanguageEnabled", "value": true},
			},
		},
	},
	"ranking": map[string]interface{}{
		"label":  "Result Ranking",
		"icon":   "list",
		"group":  "searchFiltering",
		"order":  3,
		"fields": map[string]interface{}{},
	},
	"ranking.criteria": map[string]interface{}{
		"label":    "Ranking Criteria",
		"icon":     "shuffle",
		"is_array": true,
		"parent":   "ranking",
		"key":      "criteria",
		"fields": map[string]interface{}{
			"id":      map[string]interface{}{"type": "text", "label": "ID", "description": "Criterion identifier", "readonly": true, "order": 0},
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Criterion name", "readonly": true, "order": 1},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Use this criterion for ranking", "order": 2},
			"order":   map[string]interface{}{"type": "number", "label": "Order", "description": "Sort priority (lower = higher priority)", "order": 3},
		},
	},
	"live": map[string]interface{}{
		"label": "Live TV",
		"icon":  "tv",
		"group": "sources",
		"order": 2,
		"fields": map[string]interface{}{
			"mode":                         map[string]interface{}{"type": "select", "label": "Source Type", "options": []map[string]string{{"value": "m3u", "label": "M3U Playlist URL"}, {"value": "xtream", "label": "Xtream Codes"}}, "description": "How to source the IPTV playlist", "order": 0},
			"playlistUrl":                  map[string]interface{}{"type": "text", "label": "Playlist URL", "description": "M3U playlist URL", "showWhen": map[string]interface{}{"field": "mode", "value": "m3u"}, "order": 1},
			"xtreamHost":                   map[string]interface{}{"type": "text", "label": "Server URL", "description": "Xtream Codes server URL (e.g., http://example.com:8080)", "placeholder": "http://example.com:8080", "showWhen": map[string]interface{}{"field": "mode", "value": "xtream"}, "order": 2},
			"xtreamUsername":               map[string]interface{}{"type": "text", "label": "Username", "description": "Xtream Codes username", "showWhen": map[string]interface{}{"field": "mode", "value": "xtream"}, "order": 3},
			"xtreamPassword":               map[string]interface{}{"type": "password", "label": "Password", "description": "Xtream Codes password", "showWhen": map[string]interface{}{"field": "mode", "value": "xtream"}, "order": 4},
			"playlistCacheTtlHours":        map[string]interface{}{"type": "number", "label": "Cache TTL (hours)", "description": "Playlist cache duration", "order": 5},
			"probeSizeMb":                  map[string]interface{}{"type": "number", "label": "Probe Size (MB)", "description": "FFmpeg probesize for stream analysis (0 = default ~5MB). Higher values improve stability but increase initial buffering.", "order": 6},
			"analyzeDurationSec":           map[string]interface{}{"type": "number", "label": "Analyze Duration (sec)", "description": "FFmpeg analyzeduration in seconds (0 = default ~5s). Higher values help with problematic streams.", "order": 7},
			"lowLatency":                   map[string]interface{}{"type": "boolean", "label": "Low Latency Mode", "description": "Reduce buffering for lower latency (may cause instability with poor connections)", "order": 8},
			"filtering.enabledCategories": map[string]interface{}{"type": "multiselect", "label": "Enabled Categories", "description": "Only show channels in these categories (empty = show all)", "optionsEndpoint": "/live/categories", "order": 9},
			"filtering.maxChannels":       map[string]interface{}{"type": "number", "label": "Max Total Channels", "description": "Overall channel limit (0 = no limit)", "order": 10},
			// EPG (Electronic Program Guide) settings
			"epg.enabled":              map[string]interface{}{"type": "boolean", "label": "Enable EPG", "description": "Enable Electronic Program Guide for live TV channels", "order": 11},
			"epg.xmltvUrl":             map[string]interface{}{"type": "text", "label": "XMLTV URL", "description": "URL to XMLTV EPG data (supports .xml and .xml.gz). For Xtream mode, leave empty to auto-fetch from provider.", "placeholder": "http://example.com/epg.xml.gz", "showWhen": map[string]interface{}{"field": "epg.enabled", "value": true}, "order": 12},
			"epg.refreshIntervalHours": map[string]interface{}{"type": "number", "label": "EPG Refresh Interval (hours)", "description": "How often to refresh EPG data (default: 12)", "showWhen": map[string]interface{}{"field": "epg.enabled", "value": true}, "order": 13},
			"epg.retentionDays":        map[string]interface{}{"type": "number", "label": "EPG Retention (days)", "description": "How many days of EPG data to keep (default: 7)", "showWhen": map[string]interface{}{"field": "epg.enabled", "value": true}, "order": 14},
		},
	},
	"indexers": map[string]interface{}{
		"label":    "Usenet Indexers",
		"icon":     "search",
		"group":    "sources",
		"order":    0,
		"is_array": true,
		"fields": map[string]interface{}{
			"name":       map[string]interface{}{"type": "text", "label": "Name", "description": "Indexer name", "order": 0},
			"url":        map[string]interface{}{"type": "text", "label": "URL", "description": "Indexer API URL", "order": 1},
			"apiKey":     map[string]interface{}{"type": "password", "label": "API Key", "description": "Indexer API key", "order": 2},
			"type":       map[string]interface{}{"type": "select", "label": "Type", "options": []string{"newznab"}, "description": "Indexer type", "order": 3},
			"categories": map[string]interface{}{"type": "text", "label": "Categories", "description": "Comma-separated newznab category IDs to filter results (e.g., 2000,2010,2020 for movies, 5000,5010,5020 for TV). Leave empty to search all categories.", "placeholder": "2000,5000", "order": 4},
			"enabled":    map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this indexer", "order": 5},
		},
	},
	"torrentScrapers": map[string]interface{}{
		"label":    "Torrent Scrapers",
		"icon":     "magnet",
		"group":    "sources",
		"order":    1,
		"is_array": true,
		"fields": map[string]interface{}{
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Scraper name", "order": 0},
			"type":    map[string]interface{}{"type": "select", "label": "Type", "options": []string{"torrentio", "jackett", "zilean", "aiostreams", "nyaa", "comet"}, "description": "Scraper type", "order": 1},
			"options": map[string]interface{}{"type": "text", "label": "Options", "description": "URL options (e.g., sort=qualitysize|qualityfilter=480p,scr,cam)", "showWhen": map[string]interface{}{"field": "type", "value": "torrentio"}, "order": 2, "placeholder": "sort=qualitysize|qualityfilter=480p,scr,cam"},
			"url":     map[string]interface{}{"type": "text", "label": "URL", "description": "API URL (for AIOStreams/Comet: full Stremio addon URL)", "showWhen": map[string]interface{}{"operator": "or", "conditions": []map[string]interface{}{{"field": "type", "value": "jackett"}, {"field": "type", "value": "zilean"}, {"field": "type", "value": "aiostreams"}, {"field": "type", "value": "comet"}}}, "order": 3},
			"apiKey":  map[string]interface{}{"type": "password", "label": "API Key", "description": "Jackett API key", "showWhen": map[string]interface{}{"field": "type", "value": "jackett"}, "order": 4},
			"config.passthroughFormat": map[string]interface{}{"type": "boolean", "label": "Passthrough Format", "description": "Show raw AIOStreams format in manual selection (emoji-formatted details)", "showWhen": map[string]interface{}{"field": "type", "value": "aiostreams"}, "order": 5},
			"config.category": map[string]interface{}{"type": "select", "label": "Category", "options": []string{"1_0", "1_2", "1_3", "1_4"}, "description": "Nyaa category (1_0=All Anime, 1_2=English-translated, 1_3=Non-English, 1_4=Raw)", "showWhen": map[string]interface{}{"field": "type", "value": "nyaa"}, "order": 6},
			"config.filter": map[string]interface{}{"type": "select", "label": "Filter", "options": []string{"0", "1", "2"}, "description": "Nyaa filter (0=All, 1=No remakes, 2=Trusted only)", "showWhen": map[string]interface{}{"field": "type", "value": "nyaa"}, "order": 7},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable this scraper", "order": 8},
		},
	},
	"playback": map[string]interface{}{
		"label": "Playback",
		"icon":  "play",
		"group": "experience",
		"order": 0,
		"fields": map[string]interface{}{
			"preferredPlayer":           map[string]interface{}{"type": "select", "label": "Preferred Player", "options": []string{"native", "infuse"}, "description": "Default video player"},
			"preferredAudioLanguage":    map[string]interface{}{"type": "text", "label": "Audio Language", "description": "Three-letter ISO 639-2 code (e.g., eng, spa, fra, deu, jpn)"},
			"preferredSubtitleLanguage": map[string]interface{}{"type": "text", "label": "Subtitle Language", "description": "Three-letter ISO 639-2 code (e.g., eng, spa, fra, deu, jpn)"},
			"preferredSubtitleMode":     map[string]interface{}{"type": "select", "label": "Subtitle Mode", "options": []string{"off", "on", "auto"}, "description": "Default subtitle behavior"},
			"subtitleSize":              map[string]interface{}{"type": "number", "label": "Subtitle Size", "description": "Subtitle size scaling factor (1.0 = default, 0.5 = half, 2.0 = double)", "step": 0.05, "min": 0.25, "max": 3.0},
			"seekForwardSeconds":        map[string]interface{}{"type": "number", "label": "Skip Forward", "description": "Seconds to skip forward (default 30)", "step": 5, "min": 5, "max": 120},
			"seekBackwardSeconds":       map[string]interface{}{"type": "number", "label": "Skip Backward", "description": "Seconds to skip backward (default 10)", "step": 5, "min": 5, "max": 120},
			"useLoadingScreen":          map[string]interface{}{"type": "boolean", "label": "Loading Screen", "description": "Show loading screen during playback init"},
			"forceAacTranscoding":       map[string]interface{}{"type": "boolean", "label": "Force AAC Audio Transcoding", "description": "Transcode AC3/EAC3/DTS surround audio to AAC. Enable this if using Bluetooth headphones, as they cannot decode surround codecs directly.", "order": 99},
			"autoPlayTrailersTV":        map[string]interface{}{"type": "boolean", "label": "Auto-Play Trailers (TV)", "description": "Replace backdrop artwork with playing trailer on TV details pages once loaded", "order": 100},
			"rewindOnResumeFromPause":   map[string]interface{}{"type": "number", "label": "Rewind on Unpause", "description": "Seconds to rewind when resuming from pause (default 0)", "step": 1, "min": 0, "max": 30},
			"rewindOnPlaybackStart":     map[string]interface{}{"type": "number", "label": "Rewind on Resume", "description": "Seconds to rewind when resuming from saved progress (default 0)", "step": 1, "min": 0, "max": 60},
			"disablePrequeue":           map[string]interface{}{"type": "boolean", "label": "Disable Prequeue", "description": "Disable automatic stream pre-loading when opening a details page. Streams will only be resolved when you press Play. Useful to reduce unnecessary backend load or API calls.", "order": 101},
		},
	},
	"homeShelves": map[string]interface{}{
		"label": "Home Shelves",
		"icon":  "layout",
		"group": "experience",
		"order": 1,
		"fields": map[string]interface{}{
			"exploreCardPosition": map[string]interface{}{"type": "select", "label": "Explore Card Position", "options": []string{"front", "end"}, "description": "Where the Explore card appears on shelves", "order": 1},
		},
	},
	"homeShelves.shelves": map[string]interface{}{
		"label":    "Shelf Configuration",
		"icon":     "list",
		"is_array": true,
		"parent":   "homeShelves",
		"key":      "shelves",
		"fields": map[string]interface{}{
			"name":    map[string]interface{}{"type": "text", "label": "Name", "description": "Display name", "order": 0},
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Show this shelf", "order": 1},
			"type": map[string]interface{}{
				"type":        "select",
				"label":       "Type",
				"options":     []string{"builtin", "mdblist"},
				"description": "Shelf type (builtin or custom MDBList)",
				"order":       2,
			},
			"listUrl": map[string]interface{}{
				"type":        "text",
				"label":       "MDBList URL",
				"description": "Format: https://mdblist.com/lists/{username}/{list-name}/json",
				"showWhen":    "type=mdblist",
				"order":       3,
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"label":       "Item Limit",
				"description": "Max items to return (0 = unlimited)",
				"showWhen":    "type=mdblist",
				"order":       4,
				"min":         0,
				"max":         500,
			},
		},
	},
	"display": map[string]interface{}{
		"label": "Display",
		"icon":  "eye",
		"group": "experience",
		"order": 2,
		"fields": map[string]interface{}{
			"badgeVisibility": map[string]interface{}{
				"type":        "checkboxes",
				"label":       "Badge Visibility",
				"description": "Choose which badges to display on media cards",
				"order":       0,
				"options": []map[string]interface{}{
					{"value": "watchProgress", "label": "Watch Progress"},
					{"value": "releaseStatus", "label": "Release Status"},
					{"value": "watchState", "label": "Watch State"},
					{"value": "unwatchedCount", "label": "Unwatched Episode Count"},
				},
			},
			"watchStateIconStyle": map[string]interface{}{
				"type":        "select",
				"label":       "Badge Icon Style",
				"description": "Color style for watch state and release status icons on media cards",
				"order":       1,
				"options": []map[string]interface{}{
					{"value": "colored", "label": "Colored"},
					{"value": "white", "label": "White"},
				},
			},
			"hideWatched": map[string]interface{}{
				"type":        "boolean",
				"label":       "Hide Watched Content",
				"description": "Filter out fully watched movies and TV shows from trending shelves and custom lists on the home page",
				"order":       2,
			},
			"alwaysShowProfileSelector": map[string]interface{}{
				"type":        "boolean",
				"label":       "Always Show Profile Selector",
				"description": "Show the profile selector when the app is opened or returns from background",
				"order":       3,
			},
		},
	},
	"metadata": map[string]interface{}{
		"label": "Metadata",
		"icon":  "film",
		"group": "services",
		"order": 0,
		"fields": map[string]interface{}{
			"tvdbApiKey":   map[string]interface{}{"type": "password", "label": "TVDB API Key", "description": "TheTVDB API key"},
			"tmdbApiKey":   map[string]interface{}{"type": "password", "label": "TMDB API Key", "description": "TheMovieDB API key"},
			"geminiApiKey": map[string]interface{}{"type": "password", "label": "Gemini API Key", "description": "Google Gemini API key for AI-powered recommendations (optional, free tier available)"},
			"language": map[string]interface{}{
				"type":        "select",
				"label":       "Metadata Language",
				"description": "Language for movie/TV titles, descriptions, and episode names (3-letter ISO 639-2 code)",
				"options": []map[string]interface{}{
					{"value": "eng", "label": "English"},
					{"value": "spa", "label": "Spanish (Espa\u00f1ol)"},
					{"value": "fra", "label": "French (Fran\u00e7ais)"},
					{"value": "deu", "label": "German (Deutsch)"},
					{"value": "ita", "label": "Italian (Italiano)"},
					{"value": "por", "label": "Portuguese (Portugu\u00eas)"},
					{"value": "nld", "label": "Dutch (Nederlands)"},
					{"value": "rus", "label": "Russian (\u0420\u0443\u0441\u0441\u043a\u0438\u0439)"},
					{"value": "jpn", "label": "Japanese (\u65e5\u672c\u8a9e)"},
					{"value": "kor", "label": "Korean (\ud55c\uad6d\uc5b4)"},
					{"value": "zho", "label": "Chinese (\u4e2d\u6587)"},
					{"value": "ara", "label": "Arabic (\u0627\u0644\u0639\u0631\u0628\u064a\u0629)"},
					{"value": "hin", "label": "Hindi (\u0939\u093f\u0928\u094d\u0926\u0940)"},
					{"value": "swe", "label": "Swedish (Svenska)"},
					{"value": "nor", "label": "Norwegian (Norsk)"},
					{"value": "dan", "label": "Danish (Dansk)"},
					{"value": "fin", "label": "Finnish (Suomi)"},
					{"value": "pol", "label": "Polish (Polski)"},
					{"value": "tur", "label": "Turkish (T\u00fcrk\u00e7e)"},
					{"value": "heb", "label": "Hebrew (\u05e2\u05d1\u05e8\u05d9\u05ea)"},
					{"value": "ces", "label": "Czech (\u010ce\u0161tina)"},
					{"value": "hun", "label": "Hungarian (Magyar)"},
					{"value": "ron", "label": "Romanian (Rom\u00e2n\u0103)"},
					{"value": "tha", "label": "Thai (\u0e44\u0e17\u0e22)"},
					{"value": "vie", "label": "Vietnamese (Ti\u1ebfng Vi\u1ec7t)"},
				},
			},
		},
	},
	"cache": map[string]interface{}{
		"label":  "Cache",
		"icon":   "database",
		"group":  "services",
		"order":  99,
		"hidden": true,
		"fields": map[string]interface{}{
			"directory":        map[string]interface{}{"type": "text", "label": "Directory", "description": "Cache directory path"},
			"metadataTtlHours": map[string]interface{}{"type": "number", "label": "Metadata TTL (hours)", "description": "Metadata cache duration"},
		},
	},
	"import": map[string]interface{}{
		"label":  "Import Settings",
		"icon":   "upload",
		"group":  "services",
		"order":  99,
		"hidden": true,
		"fields": map[string]interface{}{
			"rarMaxWorkers":     map[string]interface{}{"type": "number", "label": "RAR Max Workers", "description": "Maximum RAR extraction workers"},
			"rarMaxCacheSizeMb": map[string]interface{}{"type": "number", "label": "RAR Cache Size (MB)", "description": "RAR cache size"},
			"rarMaxMemoryGB":    map[string]interface{}{"type": "number", "label": "RAR Max Memory (GB)", "description": "Maximum memory for RAR operations"},
		},
	},
	"transmux": map[string]interface{}{
		"label":  "Transmux Settings",
		"icon":   "film",
		"group":  "services",
		"order":  99,
		"hidden": true,
		"fields": map[string]interface{}{
			"enabled":          map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable video transmuxing for HLS streaming"},
			"ffmpegPath":       map[string]interface{}{"type": "text", "label": "FFmpeg Path", "description": "Path to ffmpeg binary"},
			"ffprobePath":      map[string]interface{}{"type": "text", "label": "FFprobe Path", "description": "Path to ffprobe binary"},
			"hlsTempDirectory": map[string]interface{}{"type": "text", "label": "HLS Temp Directory", "description": "Directory for HLS segment storage (default: /tmp/novastream-hls)"},
		},
	},
	"subtitles": map[string]interface{}{
		"label":    "OpenSubtitles",
		"icon":     "key",
		"group":    "services",
		"order":    2,
		"testable": true,
		"fields": map[string]interface{}{
			"openSubtitlesUsername": map[string]interface{}{"type": "text", "label": "OpenSubtitles Username", "description": "OpenSubtitles.org username (optional, enables more results)", "order": 0},
			"openSubtitlesPassword": map[string]interface{}{"type": "password", "label": "OpenSubtitles Password", "description": "OpenSubtitles.org password", "order": 1},
		},
	},
	"mdblist": map[string]interface{}{
		"label": "MDBList Ratings",
		"icon":  "star",
		"group": "services",
		"order": 1,
		"fields": map[string]interface{}{
			"enabled": map[string]interface{}{"type": "boolean", "label": "Enabled", "description": "Enable MDBList integration for aggregated ratings (Rotten Tomatoes, IMDB, etc.)", "order": 0},
			"apiKey":  map[string]interface{}{"type": "password", "label": "API Key", "description": "MDBList API key from mdblist.com (free tier available)", "order": 1},
			"enabledRatings": map[string]interface{}{
				"type":        "checkboxes",
				"label":       "Rating Sources",
				"description": "Select which rating sources to display",
				"order":       2,
				"options": []map[string]interface{}{
					{"value": "imdb", "label": "IMDB"},
					{"value": "tmdb", "label": "TMDB"},
					{"value": "trakt", "label": "Trakt"},
					{"value": "letterboxd", "label": "Letterboxd"},
					{"value": "tomatoes", "label": "Rotten Tomatoes (Critics)"},
					{"value": "audience", "label": "Rotten Tomatoes (Audience)"},
					{"value": "metacritic", "label": "Metacritic"},
				},
			},
		},
	},
	"liveTV": map[string]interface{}{
		"label":       "Live TV",
		"icon":        "tv",
		"group":       "sources",
		"order":       3,
		"inheritFrom": "live",
		"fields": map[string]interface{}{
			"mode": map[string]interface{}{
				"type":        "select",
				"label":       "Source Type",
				"description": "How to source the IPTV playlist for this profile.",
				"order":       0,
				"options":     []map[string]string{{"value": "m3u", "label": "M3U Playlist URL"}, {"value": "xtream", "label": "Xtream Codes"}},
			},
			"playlistUrl": map[string]interface{}{
				"type":        "text",
				"label":       "Playlist URL",
				"description": "M3U playlist URL",
				"order":       1,
				"showWhen":    map[string]interface{}{"field": "mode", "value": "m3u"},
			},
			"xtreamHost": map[string]interface{}{
				"type":        "text",
				"label":       "Server URL",
				"description": "Xtream Codes server URL (e.g., http://example.com:8080)",
				"placeholder": "http://example.com:8080",
				"order":       2,
				"showWhen":    map[string]interface{}{"field": "mode", "value": "xtream"},
			},
			"xtreamUsername": map[string]interface{}{
				"type":        "text",
				"label":       "Username",
				"description": "Xtream Codes username",
				"order":       3,
				"showWhen":    map[string]interface{}{"field": "mode", "value": "xtream"},
			},
			"xtreamPassword": map[string]interface{}{
				"type":        "password",
				"label":       "Password",
				"description": "Xtream Codes password",
				"order":       4,
				"showWhen":    map[string]interface{}{"field": "mode", "value": "xtream"},
			},
			"playlistCacheTtlHours": map[string]interface{}{
				"type":        "number",
				"label":       "Cache TTL (hours)",
				"description": "Playlist cache duration",
				"order":       5,
			},
			"probeSizeMb": map[string]interface{}{
				"type":        "number",
				"label":       "Probe Size (MB)",
				"description": "FFmpeg probesize for stream analysis (0 = default ~5MB). Higher values improve stability but increase initial buffering.",
				"order":       6,
			},
			"analyzeDurationSec": map[string]interface{}{
				"type":        "number",
				"label":       "Analyze Duration (sec)",
				"description": "FFmpeg analyzeduration in seconds (0 = default ~5s). Higher values help with problematic streams.",
				"order":       7,
			},
			"lowLatency": map[string]interface{}{
				"type":        "boolean",
				"label":       "Low Latency Mode",
				"description": "Reduce buffering for lower latency (may cause instability with poor connections)",
				"order":       8,
			},
			"filtering.enabledCategories": map[string]interface{}{
				"type":             "multiselect",
				"label":            "Enabled Categories",
				"description":     "Only show channels in these categories (empty = show all)",
				"optionsEndpoint": "/live/categories",
				"order":           9,
			},
			"filtering.maxChannels": map[string]interface{}{
				"type":        "number",
				"label":       "Max Total Channels",
				"description": "Overall channel limit (0 = no limit)",
				"order":       10,
			},
			"epg.enabled": map[string]interface{}{
				"type":        "boolean",
				"label":       "Enable EPG",
				"description": "Enable Electronic Program Guide for live TV channels",
				"order":       11,
			},
			"epg.xmltvUrl": map[string]interface{}{
				"type":        "text",
				"label":       "XMLTV URL",
				"description": "URL to XMLTV EPG data (supports .xml and .xml.gz). For Xtream mode, leave empty to auto-fetch from provider.",
				"placeholder": "http://example.com/epg.xml.gz",
				"showWhen":    map[string]interface{}{"field": "epg.enabled", "value": true},
				"order":       12,
			},
			"epg.refreshIntervalHours": map[string]interface{}{
				"type":        "number",
				"label":       "EPG Refresh Interval (hours)",
				"description": "How often to refresh EPG data (default: 12)",
				"showWhen":    map[string]interface{}{"field": "epg.enabled", "value": true},
				"order":       13,
			},
			"epg.retentionDays": map[string]interface{}{
				"type":        "number",
				"label":       "EPG Retention (days)",
				"description": "How many days of EPG data to keep (default: 7)",
				"showWhen":    map[string]interface{}{"field": "epg.enabled", "value": true},
				"order":       14,
			},
		},
	},
}

// AdminUIHandler serves the admin dashboard UI
type AdminUIHandler struct {
	settingsTemplate      *template.Template
	statusTemplate        *template.Template
	historyTemplate       *template.Template
	toolsTemplate         *template.Template
	searchTemplate        *template.Template
	loginTemplate         *template.Template
	registerTemplate      *template.Template
	accountsTemplate      *template.Template
	kidsSettingsTemplate  *template.Template
	backupTemplate        *template.Template
	calendarTemplate      *template.Template
	performanceTemplate   *template.Template
	settingsPath          string
	hlsManager            *HLSManager
	usersService          *users.Service
	userSettingsService   *user_settings.Service
	historyService        *history.Service
	watchlistService      *watchlist.Service
	accountsService       *accounts.Service
	invitationsService    *invitations.Service
	sessionsService       *sessions.Service
	plexClient            *plex.Client
	traktClient           *trakt.Client
	configManager         *config.Manager
	metadataService       MetadataService
	calendarService       *calendar.Service
	clientsService        clientsService
	clientSettingsService clientSettingsService
}

// MetadataService interface for metadata operations
type MetadataService interface {
	ClearCache() error
	MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error)
	SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error)
	GetCacheManagerStatus() metadata.CacheManagerStatus
	RefreshTrendingCache()
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

// SetClientsService sets the clients service for propagation
func (h *AdminUIHandler) SetClientsService(cs clientsService) {
	h.clientsService = cs
}

// SetClientSettingsService sets the client settings service for propagation
func (h *AdminUIHandler) SetClientSettingsService(css clientSettingsService) {
	h.clientSettingsService = css
}

// SetCalendarService sets the calendar service for the calendar admin page
func (h *AdminUIHandler) SetCalendarService(cs *calendar.Service) {
	h.calendarService = cs
}

// NewAdminUIHandler creates a new admin UI handler
func NewAdminUIHandler(settingsPath string, hlsManager *HLSManager, usersService *users.Service, userSettingsService *user_settings.Service, configManager *config.Manager) *AdminUIHandler {
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
			return f.HDRDVPolicy != "" && f.HDRDVPolicy != config.HDRDVPolicyNoExclusion || f.MaxSizeMovieGB > 0 || len(f.FilterOutTerms) > 0
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
		settingsTemplate:     createPageTemplate("settings.html"),
		statusTemplate:       createPageTemplate("status.html"),
		historyTemplate:      createPageTemplate("history.html"),
		toolsTemplate:        createPageTemplate("tools.html"),
		searchTemplate:       createPageTemplate("search.html"),
		loginTemplate:        loginTmpl,
		registerTemplate:     registerTmpl,
		accountsTemplate:     createPageTemplate("accounts.html"),
		kidsSettingsTemplate: createPageTemplate("kids_settings.html"),
		backupTemplate:       createPageTemplate("backup.html"),
		calendarTemplate:     createPageTemplate("calendar.html"),
		performanceTemplate:  createPageTemplate("performance.html"),
		settingsPath:         settingsPath,
		hlsManager:          hlsManager,
		usersService:        usersService,
		userSettingsService: userSettingsService,
		configManager:       configManager,
		plexClient:          plex.NewClient(plex.GenerateClientID()),
		traktClient:         trakt.NewClient("", ""), // Will be updated with credentials from settings
	}
}

// AdminPageData holds data for admin page templates
type AdminPageData struct {
	CurrentPath   string
	BasePath      string // "/admin" for master accounts, "/account" for regular accounts
	IsAdmin       bool   // true for master accounts, false for regular accounts
	AccountID     string // Account ID for scoping data (empty for master)
	Username      string // Username of logged in account
	Settings      config.Settings
	Schema        map[string]interface{}
	Groups        []map[string]string
	Status        AdminStatus
	Users         []models.User
	UserOverrides map[string]bool // Map of userID -> hasOverrides for showing indicators
	Version       string
	NoProfiles    bool // true when non-admin user has no profiles
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
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	usersList := h.getScopedUsers(isAdmin, accountID)

	// Check if non-admin user has no profiles
	noProfiles := !isAdmin && len(usersList) == 0

	// Get user override information for dropdown indicators
	var userOverrides map[string]bool
	if h.userSettingsService != nil {
		userOverrides = h.userSettingsService.GetUsersWithOverrides()
	}

	data := AdminPageData{
		CurrentPath:   basePath + "/settings",
		BasePath:      basePath,
		IsAdmin:       isAdmin,
		AccountID:     accountID,
		Username:      username,
		Settings:      settings,
		Schema:        SettingsSchema,
		Groups:        SettingsGroups,
		Users:         usersList,
		UserOverrides: userOverrides,
		Version:       GetBackendVersion(),
		NoProfiles:    noProfiles,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.settingsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Settings template error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// HistoryPage serves the watch history page
func (h *AdminUIHandler) HistoryPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)
	usersList := h.getScopedUsers(isAdmin, accountID)

	// Check if non-admin user has no profiles
	noProfiles := !isAdmin && len(usersList) == 0

	data := AdminPageData{
		CurrentPath: basePath + "/history",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Users:       usersList,
		Version:     GetBackendVersion(),
		NoProfiles:  noProfiles,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.historyTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("History template error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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

// buildStreamsPayload builds the streams JSON payload for both the REST and SSE endpoints.
func (h *AdminUIHandler) buildStreamsPayload(isAdmin bool, accountID string) ([]byte, error) {
	// Pause detection thresholds
	const pauseThreshold = 30 * time.Second // No activity for 30s = paused
	const hideThreshold = 60 * time.Second  // Paused for 30s beyond pause threshold = hide from dashboard
	now := time.Now()

	// Get allowed profile IDs for this account (for filtering)
	allowedProfileIDs := make(map[string]bool)
	if !isAdmin {
		scopedUsers := h.getScopedUsers(isAdmin, accountID)
		for _, u := range scopedUsers {
			allowedProfileIDs[u.ID] = true
		}
	}

	// Get all playback progress for matching
	var allProgress map[string][]models.PlaybackProgress
	if h.historyService != nil {
		allProgress = h.historyService.ListAllPlaybackProgress()
	}

	// Build user name -> ID map for reverse lookup
	nameToUserID := make(map[string]string)
	if h.usersService != nil {
		for _, user := range h.usersService.ListAll() {
			nameToUserID[strings.ToLower(user.Name)] = user.ID
		}
	}

	// Helper to match progress for a stream - returns full progress for media identification
	matchProgress := func(filename, profileID, profileName string) *models.PlaybackProgress {
		cleanedFilename := cleanFilenameForProgressMatch(filename)

		// Determine which user IDs to try for progress lookup
		userIDsToTry := []string{}
		if profileID != "" {
			userIDsToTry = append(userIDsToTry, profileID)
		}
		if profileName != "" {
			if mappedID, ok := nameToUserID[strings.ToLower(profileName)]; ok && mappedID != profileID {
				userIDsToTry = append(userIDsToTry, mappedID)
			}
		}

		// Try to find matching progress
		for _, userID := range userIDsToTry {
			if userProgress, ok := allProgress[userID]; ok {
				for i := range userProgress {
					progress := &userProgress[i]
					progressName := ""
					if progress.MediaType == "episode" {
						progressName = progress.SeriesName
						// Check for S##E## pattern match
						if progress.SeasonNumber > 0 && progress.EpisodeNumber > 0 {
							sePattern := strings.ToLower(fmt.Sprintf("s%02de%02d", progress.SeasonNumber, progress.EpisodeNumber))
							if strings.Contains(strings.ToLower(filename), sePattern) {
								cleanedProgressName := cleanFilenameForProgressMatch(progressName)
								if cleanedProgressName != "" && cleanedFilename != "" &&
									strings.Contains(cleanedFilename, cleanedProgressName) {
									return progress
								}
							}
						}
					} else {
						progressName = progress.MovieName
					}

					cleanedProgressName := cleanFilenameForProgressMatch(progressName)
					// Require minimum 3 chars to avoid false positives from short names (e.g. "Up", "Dr")
					if len(cleanedProgressName) >= 3 && cleanedFilename != "" &&
						strings.Contains(cleanedFilename, cleanedProgressName) {
						return progress
					}
				}
			}
		}
		return nil
	}

	streams := []map[string]interface{}{}

	// Get HLS sessions
	if h.hlsManager != nil {
		h.hlsManager.mu.RLock()
		for _, session := range h.hlsManager.sessions {
			session.mu.RLock()
			// Skip "default" user streams - but only if profileName is also empty/default
			// (A stream with profileID="default" but valid profileName should still be shown)
			isDefaultProfile := strings.ToLower(session.ProfileID) == "default" || session.ProfileID == ""
			hasValidProfileName := session.ProfileName != "" && strings.ToLower(session.ProfileName) != "default"
			if isDefaultProfile && !hasValidProfileName {
				session.mu.RUnlock()
				continue
			}
			// Skip streams with 0 bytes transferred (not actually playing)
			if session.BytesStreamed == 0 {
				session.mu.RUnlock()
				continue
			}
			// Pause detection for HLS: use LastSegmentRequest (updated by keepalives)
			hlsActivity := session.LastSegmentRequest
			if hlsActivity.IsZero() {
				hlsActivity = session.LastAccess
			}
			hlsIdleDuration := now.Sub(hlsActivity)
			if hlsIdleDuration > hideThreshold {
				session.mu.RUnlock()
				continue // Hidden: idle too long
			}
			hlsIsPaused := hlsIdleDuration > pauseThreshold
			// Skip streams that don't belong to this account's profiles
			if !isAdmin && !allowedProfileIDs[session.ProfileID] {
				session.mu.RUnlock()
				continue
			}
			filename := filepath.Base(session.Path)
			if filename == "" || filename == "." {
				filename = filepath.Base(session.OriginalPath)
			}

			// Match playback progress for position and media identification
			matchedProgress := matchProgress(filename, session.ProfileID, session.ProfileName)
			duration := session.Duration
			var position, percent float64
			var mediaType, title, episodeName string
			var seasonNumber, episodeNumber, year int
			var externalIDs map[string]string

			if matchedProgress != nil {
				position = matchedProgress.Position
				percent = matchedProgress.PercentWatched
				if matchedProgress.Duration > 0 {
					duration = matchedProgress.Duration
				}
				// Media identification
				mediaType = matchedProgress.MediaType
				externalIDs = matchedProgress.ExternalIDs
				if matchedProgress.MediaType == "episode" {
					title = matchedProgress.SeriesName
					seasonNumber = matchedProgress.SeasonNumber
					episodeNumber = matchedProgress.EpisodeNumber
					episodeName = matchedProgress.EpisodeName
				} else {
					title = matchedProgress.MovieName
					year = matchedProgress.Year
				}
			}

			hlsStreamData := map[string]interface{}{
				"id":               session.ID,
				"type":             "hls",
				"path":             session.Path,
				"original_path":    session.OriginalPath,
				"filename":         filename,
				"profile_id":       session.ProfileID,
				"profile_name":     session.ProfileName,
				"client_ip":        session.ClientIP,
				"created_at":       session.CreatedAt,
				"last_access":      hlsActivity,
				"duration":         duration,
				"current_position": position,
				"percent_watched":  percent,
				"bytes_streamed":   session.BytesStreamed,
				"has_dv":           session.HasDV && !session.DVDisabled,
				"has_hdr":          session.HasHDR,
				"dv_profile":       session.DVProfile,
				"segments":         session.SegmentsCreated,
				// Media identification
				"media_type":     mediaType,
				"title":          title,
				"year":           year,
				"season_number":  seasonNumber,
				"episode_number": episodeNumber,
				"episode_name":   episodeName,
				"externalIds":    externalIDs,
			}
			if hlsIsPaused {
				hlsStreamData["is_paused"] = true
			}
			if matchedProgress != nil {
				hlsStreamData["last_updated"] = matchedProgress.UpdatedAt
			}
			streams = append(streams, hlsStreamData)
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

		// Pause detection: check how long since last byte transfer
		idleDuration := now.Sub(stream.LastActivity)
		if idleDuration > hideThreshold {
			continue // Hidden: idle too long
		}
		isPaused := idleDuration > pauseThreshold

		// Match playback progress for position and media identification
		matchedProgress := matchProgress(stream.Filename, stream.ProfileID, stream.ProfileName)
		var position, percent, duration float64
		var mediaType, title, episodeName string
		var seasonNumber, episodeNumber, year int
		var externalIDs map[string]string

		if matchedProgress != nil {
			position = matchedProgress.Position
			percent = matchedProgress.PercentWatched
			duration = matchedProgress.Duration
			// Media identification
			mediaType = matchedProgress.MediaType
			externalIDs = matchedProgress.ExternalIDs
			if matchedProgress.MediaType == "episode" {
				title = matchedProgress.SeriesName
				seasonNumber = matchedProgress.SeasonNumber
				episodeNumber = matchedProgress.EpisodeNumber
				episodeName = matchedProgress.EpisodeName
			} else {
				title = matchedProgress.MovieName
				year = matchedProgress.Year
			}
		}

		streamData := map[string]interface{}{
			"id":               stream.ID,
			"type":             "direct",
			"path":             stream.Path,
			"filename":         stream.Filename,
			"profile_id":       stream.ProfileID,
			"profile_name":     stream.ProfileName,
			"client_ip":        stream.ClientIP,
			"created_at":       stream.StartTime,
			"last_access":      stream.LastActivity,
			"duration":         duration,
			"current_position": position,
			"percent_watched":  percent,
			"bytes_streamed":   stream.BytesStreamed,
			"content_length":   stream.ContentLength,
			"user_agent":       stream.UserAgent,
			// Media identification
			"media_type":     mediaType,
			"title":          title,
			"year":           year,
			"season_number":  seasonNumber,
			"episode_number": episodeNumber,
			"episode_name":   episodeName,
			"externalIds":    externalIDs,
		}
		if isPaused {
			streamData["is_paused"] = true
		}
		if matchedProgress != nil {
			streamData["last_updated"] = matchedProgress.UpdatedAt
		}
		streams = append(streams, streamData)
	}

	return json.Marshal(map[string]interface{}{
		"streams":     streams,
		"server_time": now.UTC(),
	})
}

// GetStreams returns active streams as JSON
func (h *AdminUIHandler) GetStreams(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, _, _ := h.getPageRoleInfo(r)
	w.Header().Set("Content-Type", "application/json")
	data, err := h.buildStreamsPayload(isAdmin, accountID)
	if err != nil {
		http.Error(w, `{"error":"Failed to build streams payload"}`, http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

// GetStreamsSSE streams active streams data via Server-Sent Events every 5 seconds.
func (h *AdminUIHandler) GetStreamsSSE(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, _, _ := h.getPageRoleInfo(r)

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"Streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	// Send initial snapshot immediately
	data, err := h.buildStreamsPayload(isAdmin, accountID)
	if err == nil {
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			data, err := h.buildStreamsPayload(isAdmin, accountID)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// cleanFilenameForProgressMatch removes common filename artifacts for matching against media titles.
// Used for both filenames and media title strings, so only strips known video file extensions
// rather than using filepath.Ext (which misinterprets dots in titles like "Dr. STONE").
func cleanFilenameForProgressMatch(name string) string {
	if name == "" {
		return ""
	}
	// Remove known video file extensions only (filepath.Ext misinterprets dots in titles)
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".ts", ".m4v", ".webm", ".mov", ".wmv", ".flv"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	// Replace common separators with spaces
	name = strings.ReplaceAll(name, ".", " ")
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	// Lowercase for comparison
	name = strings.ToLower(name)
	// Remove common quality/codec indicators
	for _, pattern := range []string{"1080p", "720p", "2160p", "4k", "bluray", "webrip", "webdl", "web dl", "hdtv", "x264", "x265", "hevc", "h264", "h265", "aac", "dts", "atmos", "truehd", "remux", "hybrid", "hdr", "ddp", "dv"} {
		name = strings.ReplaceAll(name, pattern, "")
	}
	// Collapse multiple spaces
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return strings.TrimSpace(name)
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
			RewindOnResumeFromPause:   globalSettings.Playback.RewindOnResumeFromPause,
			RewindOnPlaybackStart:     globalSettings.Playback.RewindOnPlaybackStart,
		},
		HomeShelves: models.HomeShelvesSettings{
			Shelves: convertShelves(globalSettings.HomeShelves.Shelves),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                   models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                 models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                    globalSettings.Filtering.MaxResolution,
			HDRDVPolicy:                      models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			PrioritizeHdr:                    models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
			FilterOutTerms:                   globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:                   globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:                globalSettings.Filtering.NonPreferredTerms,
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Filtering.BypassFilteringForAIOStreamsOnly),
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
		Display: models.DisplaySettings{
			BadgeVisibility:     globalSettings.Display.BadgeVisibility,
			WatchStateIconStyle: globalSettings.Display.WatchStateIconStyle,
		},
	}

	userSettings, err := h.userSettingsService.GetWithDefaults(userID, defaults)
	if err != nil {
		http.Error(w, "Failed to load user settings", http.StatusInternalServerError)
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
		http.Error(w, "Failed to save user settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// ResetUserSettings resets user-specific settings to global defaults
func (h *AdminUIHandler) ResetUserSettings(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId parameter required", http.StatusBadRequest)
		return
	}

	if h.userSettingsService == nil {
		http.Error(w, "User settings service not available", http.StatusInternalServerError)
		return
	}

	if err := h.userSettingsService.Delete(userID); err != nil {
		http.Error(w, "Failed to reset user settings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "User settings reset to global defaults",
	})
}

// PropagateSettings propagates settings from global to all profiles+clients, or from a profile to its clients
func (h *AdminUIHandler) PropagateSettings(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")

	if h.userSettingsService == nil {
		writeJSONError(w, "User settings service not available", http.StatusInternalServerError)
		return
	}

	// Load global settings
	globalSettings, err := h.configManager.Load()
	if err != nil {
		writeJSONError(w, "Failed to load global settings", http.StatusInternalServerError)
		return
	}

	// Build the filtering settings from global
	globalFilterSettings := models.FilterSettings{
		MaxSizeMovieGB:                   models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
		MaxSizeEpisodeGB:                 models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
		MaxResolution:                    globalSettings.Filtering.MaxResolution,
		HDRDVPolicy:                      models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
		PrioritizeHdr:                    models.BoolPtr(globalSettings.Filtering.PrioritizeHdr),
		FilterOutTerms:                   globalSettings.Filtering.FilterOutTerms,
		PreferredTerms:                   globalSettings.Filtering.PreferredTerms,
		NonPreferredTerms:                globalSettings.Filtering.NonPreferredTerms,
		BypassFilteringForAIOStreamsOnly: models.BoolPtr(globalSettings.Filtering.BypassFilteringForAIOStreamsOnly),
	}

	var propagatedProfiles, propagatedClients int

	if userID == "" {
		// Propagate from global to all profiles and their clients
		allUsers := h.usersService.ListAll()
		for _, user := range allUsers {
			// Get or create user settings
			existingSettings, _ := h.userSettingsService.Get(user.ID)
			var newSettings models.UserSettings
			if existingSettings != nil {
				newSettings = *existingSettings
			} else {
				// Start with defaults
				newSettings = models.UserSettings{
					Playback: models.PlaybackSettings{
						PreferredPlayer:           globalSettings.Playback.PreferredPlayer,
						PreferredAudioLanguage:    globalSettings.Playback.PreferredAudioLanguage,
						PreferredSubtitleLanguage: globalSettings.Playback.PreferredSubtitleLanguage,
						PreferredSubtitleMode:     globalSettings.Playback.PreferredSubtitleMode,
						UseLoadingScreen:          globalSettings.Playback.UseLoadingScreen,
						SubtitleSize:              globalSettings.Playback.SubtitleSize,
						RewindOnResumeFromPause:   globalSettings.Playback.RewindOnResumeFromPause,
						RewindOnPlaybackStart:     globalSettings.Playback.RewindOnPlaybackStart,
					},
					HomeShelves: models.HomeShelvesSettings{
						Shelves: convertShelves(globalSettings.HomeShelves.Shelves),
					},
				}
			}
			// Override filtering with global settings
			newSettings.Filtering = globalFilterSettings
			if err := h.userSettingsService.Update(user.ID, newSettings); err != nil {
				writeJSONError(w, "Failed to update user "+user.ID+": "+err.Error(), http.StatusInternalServerError)
				return
			}
			propagatedProfiles++

			// Also propagate to this user's clients
			if h.clientsService != nil && h.clientSettingsService != nil {
				clients := h.clientsService.ListByUser(user.ID)
				for _, client := range clients {
					// Delete client overrides so they inherit from profile
					_ = h.clientSettingsService.Delete(client.ID)
					propagatedClients++
				}
			}
		}
	} else {
		// Propagate from profile to its clients only
		// Get the profile's effective settings
		profileSettings, err := h.userSettingsService.Get(userID)
		if err != nil {
			writeJSONError(w, "Failed to get profile settings: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// If profile has no overrides, use global filtering settings
		filterSettingsToPropagate := globalFilterSettings
		if profileSettings != nil {
			filterSettingsToPropagate = profileSettings.Filtering
		}

		if h.clientsService != nil && h.clientSettingsService != nil {
			clients := h.clientsService.ListByUser(userID)
			for _, client := range clients {
				// Delete client overrides so they inherit from profile
				_ = h.clientSettingsService.Delete(client.ID)
				propagatedClients++
			}
			// If we want to explicitly set settings on clients instead of just deleting overrides:
			// We delete them so they inherit, which is cleaner
			_ = filterSettingsToPropagate // Used for logging/future enhancement
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":            true,
		"propagatedProfiles": propagatedProfiles,
		"propagatedClients":  propagatedClients,
		"message":            fmt.Sprintf("Settings propagated to %d profiles and %d clients", propagatedProfiles, propagatedClients),
	})
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
	case "nyaa":
		h.testNyaaScraper(w)
	case "comet":
		h.testCometScraper(w, req)
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

// testNyaaScraper tests Nyaa by querying its RSS feed
func (h *AdminUIHandler) testNyaaScraper(w http.ResponseWriter) {
	client := &http.Client{Timeout: 15 * time.Second}

	// Test by making a simple RSS query to Nyaa
	testURL := "https://nyaa.si/?page=rss&f=0&c=1_0&q=test"
	testReq, err := http.NewRequest(http.MethodGet, testURL, nil)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create request: %v", err),
		})
		return
	}
	testReq.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	testReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; strmr/1.0)")

	resp, err := client.Do(testReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Nyaa connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Nyaa returned HTTP %d", resp.StatusCode),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Nyaa is reachable",
	})
}

// testCometScraper tests a Comet instance by fetching its manifest and a test stream
func (h *AdminUIHandler) testCometScraper(w http.ResponseWriter, req TestScraperRequest) {
	client := &http.Client{Timeout: 15 * time.Second}

	// Determine base URL
	baseURL := strings.TrimSpace(req.URL)
	if baseURL == "" {
		baseURL = "https://comet.elfhosted.com"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/manifest.json")

	// Build manifest URL with options if present
	options := strings.TrimSpace(req.Options)
	var manifestURL string
	if options != "" {
		manifestURL = fmt.Sprintf("%s/%s/manifest.json", baseURL, options)
	} else {
		manifestURL = fmt.Sprintf("%s/manifest.json", baseURL)
	}

	// Test by fetching manifest
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
			"error":   fmt.Sprintf("Comet connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Comet returned HTTP %d", resp.StatusCode),
		})
		return
	}

	// Parse manifest to verify it's a valid Stremio addon
	var manifest struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to parse manifest: %v", err),
		})
		return
	}

	// Try to fetch a test stream (The Matrix - tt0133093)
	var streamURL string
	if options != "" {
		streamURL = fmt.Sprintf("%s/%s/stream/movie/tt0133093.json", baseURL, options)
	} else {
		streamURL = fmt.Sprintf("%s/stream/movie/tt0133093.json", baseURL)
	}

	streamReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	if err != nil {
		// Manifest worked, just report that
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Comet is working (%s v%s)", manifest.Name, manifest.Version),
		})
		return
	}
	addBrowserHeaders(streamReq)

	streamResp, err := client.Do(streamReq)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Comet is working (%s v%s)", manifest.Name, manifest.Version),
		})
		return
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode >= 400 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Comet is working (%s v%s)", manifest.Name, manifest.Version),
		})
		return
	}

	var streamResult struct {
		Streams []interface{} `json:"streams"`
	}
	if err := json.NewDecoder(streamResp.Body).Decode(&streamResult); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Comet is working (%s v%s)", manifest.Name, manifest.Version),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Comet is working (%s v%s, %d streams found)", manifest.Name, manifest.Version, len(streamResult.Streams)),
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
	ID               string    `json:"id"`
	AccountID        string    `json:"accountId,omitempty"`
	Name             string    `json:"name"`
	Color            string    `json:"color,omitempty"`
	IconURL          string    `json:"iconUrl,omitempty"`
	HasPin           bool      `json:"hasPin"`
	HasIcon          bool      `json:"hasIcon"`
	IsKidsProfile    bool      `json:"isKidsProfile"`
	KidsMode         string    `json:"kidsMode,omitempty"`
	KidsMaxRating      string    `json:"kidsMaxRating,omitempty"`
	KidsMaxMovieRating string    `json:"kidsMaxMovieRating,omitempty"`
	KidsMaxTVRating    string    `json:"kidsMaxTVRating,omitempty"`
	KidsAllowedLists   []string  `json:"kidsAllowedLists,omitempty"`
	TraktAccountID   string    `json:"traktAccountId,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
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
			ID:               u.ID,
			AccountID:        u.AccountID,
			Name:             u.Name,
			Color:            u.Color,
			IconURL:          u.IconURL,
			HasPin:           u.HasPin(),
			HasIcon:          u.HasIcon(),
			IsKidsProfile:    u.IsKidsProfile,
			KidsMode:         u.KidsMode,
			KidsMaxRating:      u.KidsMaxRating,
			KidsMaxMovieRating: u.KidsMaxMovieRating,
			KidsMaxTVRating:    u.KidsMaxTVRating,
			KidsAllowedLists:   u.KidsAllowedLists,
			TraktAccountID:   u.TraktAccountID,
			CreatedAt:        u.CreatedAt,
			UpdatedAt:        u.UpdatedAt,
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
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
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
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	})
}

// CreateProfileRequest represents a request to create a profile
type CreateProfileRequest struct {
	Name          string `json:"name"`
	Color         string `json:"color,omitempty"`
	AccountId     string `json:"accountId,omitempty"`
	IsKidsProfile bool   `json:"isKidsProfile,omitempty"`
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

	user, err := h.usersService.CreateForAccount(req.AccountId, req.Name)
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

	// Set kids profile flag if requested
	if req.IsKidsProfile {
		user, err = h.usersService.SetKidsProfile(user.ID, true)
		if err != nil {
			log.Printf("[admin] failed to set kids profile for new profile %s: %v", user.ID, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProfileWithPinStatus{
		ID:               user.ID,
		AccountID:        user.AccountID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
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
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
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
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
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
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	})
}

// SetProfileIconRequest represents a request to set a profile's icon URL
type SetProfileIconRequest struct {
	IconURL string `json:"iconUrl"`
}

// SetProfileIcon downloads an image from the provided URL and sets it as the profile icon
func (h *AdminUIHandler) SetProfileIcon(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	var req SetProfileIconRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := h.usersService.SetIconURL(profileID, req.IconURL)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "download") {
			status = http.StatusBadGateway
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProfileWithPinStatus{
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	})
}

// ClearProfileIcon removes the profile icon
func (h *AdminUIHandler) ClearProfileIcon(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	user, err := h.usersService.ClearIconURL(profileID)
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
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	})
}

// UploadProfileIcon accepts a multipart form upload and sets it as the profile icon
func (h *AdminUIHandler) UploadProfileIcon(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	// Parse multipart form with 5MB limit
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		http.Error(w, "File too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("icon")
	if err != nil {
		http.Error(w, "icon file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read file data
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	// Get content type from header or detect from file
	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}

	user, err := h.usersService.SetIconFile(profileID, data, contentType)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProfileWithPinStatus{
		ID:               user.ID,
		Name:             user.Name,
		Color:            user.Color,
		IconURL:          user.IconURL,
		HasPin:           user.HasPin(),
		HasIcon:          user.HasIcon(),
		IsKidsProfile:    user.IsKidsProfile,
		KidsMode:         user.KidsMode,
		KidsMaxRating:      user.KidsMaxRating,
		KidsMaxMovieRating: user.KidsMaxMovieRating,
		KidsMaxTVRating:    user.KidsMaxTVRating,
		KidsAllowedLists:   user.KidsAllowedLists,
		CreatedAt:        user.CreatedAt,
		UpdatedAt:        user.UpdatedAt,
	})
}

// ServeProfileIcon serves the profile icon image file
func (h *AdminUIHandler) ServeProfileIcon(w http.ResponseWriter, r *http.Request) {
	if h.usersService == nil {
		http.Error(w, "Users service not available", http.StatusInternalServerError)
		return
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Error(w, "profileId parameter required", http.StatusBadRequest)
		return
	}

	iconPath, err := h.usersService.GetIconPath(profileID)
	if err != nil {
		http.Error(w, "Profile not found", http.StatusNotFound)
		return
	}

	if iconPath == "" {
		http.Error(w, "No icon set for this profile", http.StatusNotFound)
		return
	}

	// Determine content type from extension
	contentType := "image/png"
	if strings.HasSuffix(iconPath, ".jpg") || strings.HasSuffix(iconPath, ".jpeg") {
		contentType = "image/jpeg"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, iconPath)
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
		http.Error(w, "Failed to create invitation", http.StatusInternalServerError)
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

// GetCacheManagerStatus returns the current status of the background metadata cache manager.
func (h *AdminUIHandler) GetCacheManagerStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.metadataService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "metadata service not available"})
		return
	}
	json.NewEncoder(w).Encode(h.metadataService.GetCacheManagerStatus())
}

// RefreshTrendingCache triggers an immediate refresh of the trending metadata cache.
func (h *AdminUIHandler) RefreshTrendingCache(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.metadataService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "metadata service not available"})
		return
	}
	h.metadataService.RefreshTrendingCache()
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Refresh started"})
}

// GetCalendarWorkerStatus returns the current status of the calendar background worker.
func (h *AdminUIHandler) GetCalendarWorkerStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.calendarService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "calendar service not available"})
		return
	}
	json.NewEncoder(w).Encode(h.calendarService.GetStatus())
}

// RefreshCalendar triggers an immediate refresh of the calendar data.
func (h *AdminUIHandler) RefreshCalendar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.calendarService == nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "calendar service not available"})
		return
	}
	h.calendarService.Refresh()
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Calendar refresh started"})
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

	// Try XML-RPC first, fall back to subliminal library if that fails
	success, xmlErr := testOpenSubtitlesXMLRPC(req.Username, req.Password)
	if success {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "OpenSubtitles login successful",
		})
		return
	}

	// XML-RPC failed, try using subliminal library as fallback
	// This matches exactly what the actual subtitle search uses
	success, subliminalErr := testOpenSubtitlesSubliminal(req.Username, req.Password)
	if success {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "OpenSubtitles login successful (via subliminal)",
		})
		return
	}

	// Both methods failed - return the more specific error
	errMsg := xmlErr
	if subliminalErr != "" && subliminalErr != "unknown error" {
		errMsg = subliminalErr
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"error":   errMsg,
	})
}

// testOpenSubtitlesXMLRPC tests credentials using direct XML-RPC call
func testOpenSubtitlesXMLRPC(username, password string) (bool, string) {
	client := &http.Client{Timeout: 15 * time.Second}

	// Escape special XML characters in credentials
	escapedUsername := template.HTMLEscapeString(username)
	escapedPassword := template.HTMLEscapeString(password)

	// Build XML-RPC request for LogIn
	// Use VLSub user agent to match subliminal library (registered with OpenSubtitles)
	xmlPayload := fmt.Sprintf(`<?xml version="1.0"?>
<methodCall>
  <methodName>LogIn</methodName>
  <params>
    <param><value><string>%s</string></value></param>
    <param><value><string>%s</string></value></param>
    <param><value><string>en</string></value></param>
    <param><value><string>VLSub 0.11.1</string></value></param>
  </params>
</methodCall>`, escapedUsername, escapedPassword)

	apiReq, err := http.NewRequest(http.MethodPost, "https://api.opensubtitles.org/xml-rpc", strings.NewReader(xmlPayload))
	if err != nil {
		return false, fmt.Sprintf("Failed to create request: %v", err)
	}
	apiReq.Header.Set("Content-Type", "text/xml")

	resp, err := client.Do(apiReq)
	if err != nil {
		return false, fmt.Sprintf("Connection failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Sprintf("Failed to read response: %v", err)
	}

	bodyStr := string(body)

	// Check for successful login (status 200 OK in the XML response)
	if strings.Contains(bodyStr, "<name>status</name>") && strings.Contains(bodyStr, "200 OK") {
		return true, ""
	}

	// Check for common error statuses
	if strings.Contains(bodyStr, "401") {
		return false, "Invalid username or password"
	}
	if strings.Contains(bodyStr, "411") {
		return false, "Invalid user agent (API blocked)"
	}

	return false, "Login failed - check credentials"
}

// testOpenSubtitlesSubliminal tests credentials using the subliminal Python library
// This is the same library used for actual subtitle searches
func testOpenSubtitlesSubliminal(username, password string) (bool, string) {
	// Find Python path (Docker or local)
	var pythonPath string
	if _, err := os.Stat("/.venv/bin/python3"); err == nil {
		pythonPath = "/.venv/bin/python3"
	} else {
		pythonPath = filepath.Join("..", ".venv", "bin", "python3")
		if _, err := os.Stat(pythonPath); err != nil {
			// Try from backend directory
			pythonPath = filepath.Join(".venv", "bin", "python3")
		}
	}

	// Python script to test OpenSubtitles login using subliminal
	// Reads credentials from stdin to avoid exposing them in process listings
	script := `
import sys
import json
from subliminal.providers.opensubtitles import OpenSubtitlesProvider

try:
    params = json.loads(sys.stdin.read())
    provider = OpenSubtitlesProvider(username=params['username'], password=params['password'])
    provider.initialize()  # This performs the login
    provider.terminate()   # Logout
    print(json.dumps({"success": True}))
except Exception as e:
    error_msg = str(e)
    if "Unauthorized" in error_msg or "401" in error_msg:
        error_msg = "Invalid username or password"
    elif "UnknownUserAgent" in error_msg or "414" in error_msg:
        error_msg = "Invalid user agent"
    print(json.dumps({"success": False, "error": error_msg}))
`

	paramsJSON, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})

	cmd := exec.Command(pythonPath, "-c", script)
	cmd.Stdin = strings.NewReader(string(paramsJSON))
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return false, string(exitErr.Stderr)
		}
		return false, err.Error()
	}

	var result struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return false, "Failed to parse response"
	}

	if result.Success {
		return true, ""
	}
	return false, result.Error
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
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// KidsSettingsPage serves the kids profile settings page
func (h *AdminUIHandler) KidsSettingsPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	// Verify the profile ID exists and belongs to the account (for non-admin)
	profileID := r.URL.Query().Get("profileId")
	if profileID == "" {
		http.Redirect(w, r, basePath+"/accounts", http.StatusFound)
		return
	}

	// Non-admin users can only configure their own account's profiles
	if !isAdmin && !h.profileBelongsToAccount(profileID, accountID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	mgr := config.NewManager(h.settingsPath)
	settings, err := mgr.Load()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	data := AdminPageData{
		CurrentPath: basePath + "/kids-settings",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Settings:    settings,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.kidsSettingsTemplate == nil {
		http.Error(w, "Kids settings template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.kidsSettingsTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Kids settings template error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// BackupPage serves the backup management page
func (h *AdminUIHandler) BackupPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	// Backup page is only for admins
	if !isAdmin {
		http.Redirect(w, r, basePath, http.StatusFound)
		return
	}

	data := AdminPageData{
		CurrentPath: basePath + "/backup",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.backupTemplate == nil {
		http.Error(w, "Backup template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.backupTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Backup template error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// CalendarPage serves the calendar admin page
func (h *AdminUIHandler) CalendarPage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)
	usersList := h.getScopedUsers(isAdmin, accountID)
	noProfiles := !isAdmin && len(usersList) == 0

	// Load global settings to get MDBList shelves for the calendar sources panel
	mgr := config.NewManager(h.settingsPath)
	settings, _ := mgr.Load()

	data := AdminPageData{
		CurrentPath: basePath + "/calendar",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Users:       usersList,
		Settings:    settings,
		Version:     GetBackendVersion(),
		NoProfiles:  noProfiles,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.calendarTemplate == nil {
		http.Error(w, "Calendar template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.calendarTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Calendar template error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// GetCalendarData returns calendar data for a user as JSON.
// Query params: user (userID), tz (timezone), days (int), source (filter).
func (h *AdminUIHandler) GetCalendarData(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.URL.Query().Get("user"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "user query parameter required"})
		return
	}

	if h.calendarService == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "calendar service not available"})
		return
	}

	// Parse timezone
	tzName := strings.TrimSpace(r.URL.Query().Get("tz"))
	loc := time.UTC
	if tzName != "" {
		parsed, err := time.LoadLocation(tzName)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid timezone: " + tzName})
			return
		}
		loc = parsed
	}

	// Parse days
	days := 30
	if daysStr := r.URL.Query().Get("days"); daysStr != "" {
		if parsed, err := strconv.Atoi(daysStr); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if days > 90 {
		days = 90
	}

	// Parse source filter
	sourceFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))

	cached := h.calendarService.Get(userID)
	if cached == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.CalendarResponse{
			Items:    []models.CalendarItem{},
			Total:    0,
			Timezone: loc.String(),
			Days:     days,
		})
		return
	}

	nowInTZ := time.Now().In(loc)
	todayStart := time.Date(nowInTZ.Year(), nowInTZ.Month(), nowInTZ.Day(), 0, 0, 0, 0, loc)
	cutoff := todayStart.AddDate(0, 0, days)

	var result []models.CalendarItem
	for _, item := range cached.Items {
		if sourceFilter != "" && item.Source != sourceFilter {
			continue
		}
		// Build full air datetime using air time + timezone when available.
		// This prevents premature filtering of episodes that air later in the day
		// and ensures correct date display when converting across timezones.
		airDT := buildAirDateTime(item)
		airInTZ := airDT.In(loc)
		airDateInTZ := time.Date(airInTZ.Year(), airInTZ.Month(), airInTZ.Day(), 0, 0, 0, 0, loc)
		if airDateInTZ.Before(todayStart) || airDateInTZ.After(cutoff) {
			continue
		}
		adjusted := item
		adjusted.AirDate = airInTZ.Format("2006-01-02")
		result = append(result, adjusted)
	}
	if result == nil {
		result = []models.CalendarItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.CalendarResponse{
		Items:       result,
		Total:       len(result),
		Timezone:    loc.String(),
		Days:        days,
		RefreshedAt: cached.RefreshedAt.Format(time.RFC3339),
	})
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

// itemMetadata holds metadata fetched for watchlist import
type itemMetadata struct {
	Overview    string
	PosterURL   string
	BackdropURL string
}

// fetchMetadataForItem fetches overview and artwork for a watchlist item from metadata service
func (h *AdminUIHandler) fetchMetadataForItem(ctx context.Context, mediaType, name string, year int, externalIDs map[string]string) itemMetadata {
	if h.metadataService == nil {
		return itemMetadata{}
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

	var result itemMetadata

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
			result.Overview = title.Overview
			if title.Poster != nil {
				result.PosterURL = title.Poster.URL
			}
			if title.Backdrop != nil {
				result.BackdropURL = title.Backdrop.URL
			}
		}
	case "series", "show", "tv":
		query := models.SeriesDetailsQuery{
			Name:   name,
			Year:   year,
			TMDBID: tmdbID,
			TVDBID: tvdbID,
		}
		if title, err := h.metadataService.SeriesInfo(ctx, query); err == nil && title != nil {
			result.Overview = title.Overview
			if title.Poster != nil {
				result.PosterURL = title.Poster.URL
			}
			if title.Backdrop != nil {
				result.BackdropURL = title.Backdrop.URL
			}
		}
	}

	return result
}

// fetchOverviewForItem fetches the overview/description for a watchlist item from metadata service
func (h *AdminUIHandler) fetchOverviewForItem(ctx context.Context, mediaType, name string, year int, externalIDs map[string]string) string {
	return h.fetchMetadataForItem(ctx, mediaType, name, year, externalIDs).Overview
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

		// Fetch metadata (overview and artwork) from metadata service if available
		var metadata itemMetadata
		if h.metadataService != nil {
			metadata = h.fetchMetadataForItem(ctx, item.MediaType, item.Title, item.Year, item.ExternalIDs)
		}

		input := models.WatchlistUpsert{
			ID:          itemID,
			MediaType:   item.MediaType,
			Name:        item.Title,
			Overview:    metadata.Overview,
			Year:        item.Year,
			PosterURL:   metadata.PosterURL,
			BackdropURL: metadata.BackdropURL,
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
	var historyUpdates []models.WatchHistoryUpdate

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
			// For episodes, construct composite ID like tmdb:tv:SHOWID:s01e02 (lowercase for consistency)
			if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
				seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
				itemID = fmt.Sprintf("tmdb:tv:%s:s%02de%02d", tmdbID, item.Season, item.Episode)
			} else if tvdbID, ok := item.ExternalIDs["tvdb"]; ok && tvdbID != "" {
				seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
				itemID = fmt.Sprintf("tvdb:series:%s:s%02de%02d", tvdbID, item.Season, item.Episode)
			} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
				seriesID = fmt.Sprintf("imdb:%s", imdbID)
				itemID = fmt.Sprintf("imdb:%s:s%02de%02d", imdbID, item.Season, item.Episode)
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

		historyUpdates = append(historyUpdates, historyItem)
	}

	// Use ImportWatchHistory (no scrobble) to avoid creating duplicate watch events
	// on Trakt when importing items that already came from Trakt.
	imported, err := h.historyService.ImportWatchHistory(req.ProfileID, historyUpdates)
	if err != nil {
		errorCount = len(historyUpdates)
		errors = append(errors, err.Error())
	} else {
		successCount = imported
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

// PlexImportHistory imports Plex watch history to local history
func (h *AdminUIHandler) PlexImportHistory(w http.ResponseWriter, r *http.Request) {
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
			RatingKey   string            `json:"ratingKey"`
			Title       string            `json:"title"`
			MediaType   string            `json:"type"`
			Year        int               `json:"year"`
			ViewedAt    int64             `json:"viewedAt"`
			ExternalIDs map[string]string `json:"externalIds"`
			SeriesTitle string            `json:"seriesTitle,omitempty"`
			Season      int               `json:"season,omitempty"`
			Episode     int               `json:"episode,omitempty"`
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
	var historyUpdates []models.WatchHistoryUpdate

	watched := true
	for _, item := range req.Items {
		var itemID string
		var seriesID string

		if item.MediaType == "movie" {
			// For movies, use TMDB > IMDB > TVDB > Plex
			if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
				itemID = tmdbID
			} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
				itemID = imdbID
			} else if tvdbID, ok := item.ExternalIDs["tvdb"]; ok && tvdbID != "" {
				itemID = tvdbID
			} else if item.RatingKey != "" {
				itemID = "plex:" + item.RatingKey
			}
		} else if item.MediaType == "episode" {
			// For episodes, construct composite ID like tmdb:tv:SHOWID:s01e02 (lowercase for consistency)
			if tmdbID, ok := item.ExternalIDs["tmdb"]; ok && tmdbID != "" {
				seriesID = fmt.Sprintf("tmdb:tv:%s", tmdbID)
				itemID = fmt.Sprintf("tmdb:tv:%s:s%02de%02d", tmdbID, item.Season, item.Episode)
			} else if tvdbID, ok := item.ExternalIDs["tvdb"]; ok && tvdbID != "" {
				seriesID = fmt.Sprintf("tvdb:series:%s", tvdbID)
				itemID = fmt.Sprintf("tvdb:series:%s:s%02de%02d", tvdbID, item.Season, item.Episode)
			} else if imdbID, ok := item.ExternalIDs["imdb"]; ok && imdbID != "" {
				seriesID = fmt.Sprintf("imdb:%s", imdbID)
				itemID = fmt.Sprintf("imdb:%s:s%02de%02d", imdbID, item.Season, item.Episode)
			} else if item.RatingKey != "" {
				seriesID = "plex:series:" + item.RatingKey
				itemID = fmt.Sprintf("plex:series:%s:s%02de%02d", item.RatingKey, item.Season, item.Episode)
			}
		}

		if itemID == "" {
			errorCount++
			errors = append(errors, fmt.Sprintf("%s: no valid ID found", item.Title))
			continue
		}

		// Convert Unix timestamp to time.Time
		watchedAt := time.Unix(item.ViewedAt, 0)

		historyItem := models.WatchHistoryUpdate{
			MediaType:   item.MediaType,
			ItemID:      itemID,
			Watched:     &watched,
			WatchedAt:   watchedAt,
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
			historyItem.Name = item.Title
			historyItem.SeasonNumber = item.Season
			historyItem.EpisodeNumber = item.Episode
			historyItem.Year = item.Year
		}

		historyUpdates = append(historyUpdates, historyItem)
	}

	// Use ImportWatchHistory (no scrobble) to avoid creating duplicate watch events
	// when importing from an external source.
	imported, err := h.historyService.ImportWatchHistory(req.ProfileID, historyUpdates)
	if err != nil {
		errorCount = len(historyUpdates)
		errors = append(errors, err.Error())
	} else {
		successCount = imported
	}

	w.Header().Set("Content-Type", "application/json")
	plexResponse := map[string]interface{}{
		"success":    errorCount == 0,
		"imported":   successCount,
		"failed":     errorCount,
		"totalItems": len(req.Items),
	}
	if len(errors) > 0 {
		plexResponse["errors"] = errors
	}
	json.NewEncoder(w).Encode(plexResponse)
}

// --- Performance monitoring ---

var processStartTime = time.Now()

// cpuTracker computes process CPU % using getrusage deltas.
type cpuTracker struct {
	mu       sync.Mutex
	lastTime time.Time
	lastUser time.Duration
	lastSys  time.Duration
}

var perfCPUTracker = &cpuTracker{}

func (c *cpuTracker) sample() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}

	userTime := time.Duration(ru.Utime.Nano())
	sysTime := time.Duration(ru.Stime.Nano())
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastTime.IsZero() {
		c.lastTime = now
		c.lastUser = userTime
		c.lastSys = sysTime
		return 0
	}

	wall := now.Sub(c.lastTime)
	if wall <= 0 {
		return 0
	}

	cpuDelta := (userTime - c.lastUser) + (sysTime - c.lastSys)
	pct := float64(cpuDelta) / float64(wall) * 100.0

	c.lastTime = now
	c.lastUser = userTime
	c.lastSys = sysTime

	return pct
}

// PerformanceMetrics is the JSON payload for the performance page.
type PerformanceMetrics struct {
	CPUPercent     float64 `json:"cpuPercent"`
	NumCPU         int     `json:"numCPU"`
	NumCgoCall     int64   `json:"numCgoCall"`
	HeapAlloc      uint64  `json:"heapAlloc"`
	HeapSys        uint64  `json:"heapSys"`
	HeapInuse      uint64  `json:"heapInuse"`
	HeapIdle       uint64  `json:"heapIdle"`
	HeapReleased   uint64  `json:"heapReleased"`
	HeapObjects    uint64  `json:"heapObjects"`
	StackInuse     uint64  `json:"stackInuse"`
	StackSys       uint64  `json:"stackSys"`
	MSpanInuse     uint64  `json:"mSpanInuse"`
	MCacheInuse    uint64  `json:"mCacheInuse"`
	Sys            uint64  `json:"sys"`
	TotalAlloc     uint64  `json:"totalAlloc"`
	NumGC          uint32  `json:"numGC"`
	LastGC         uint64  `json:"lastGC"`
	PauseTotalNs   uint64  `json:"pauseTotalNs"`
	LastPauseNs    uint64  `json:"lastPauseNs"`
	GCCPUFraction  float64 `json:"gcCPUFraction"`
	NextGC         uint64  `json:"nextGC"`
	Goroutines     int     `json:"goroutines"`
	UptimeSeconds  float64 `json:"uptimeSeconds"`
	GoVersion      string  `json:"goVersion"`
	GOOS           string  `json:"goos"`
	GOARCH         string  `json:"goarch"`
	OpenFDs        int     `json:"openFDs"`
}

func buildPerformanceMetrics() PerformanceMetrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	lastPause := uint64(0)
	if m.NumGC > 0 {
		lastPause = m.PauseNs[(m.NumGC+255)%256]
	}

	return PerformanceMetrics{
		CPUPercent:    perfCPUTracker.sample(),
		NumCPU:        runtime.NumCPU(),
		NumCgoCall:    runtime.NumCgoCall(),
		HeapAlloc:     m.HeapAlloc,
		HeapSys:       m.HeapSys,
		HeapInuse:     m.HeapInuse,
		HeapIdle:      m.HeapIdle,
		HeapReleased:  m.HeapReleased,
		HeapObjects:   m.HeapObjects,
		StackInuse:    m.StackInuse,
		StackSys:      m.StackSys,
		MSpanInuse:    m.MSpanInuse,
		MCacheInuse:   m.MCacheInuse,
		Sys:           m.Sys,
		TotalAlloc:    m.TotalAlloc,
		NumGC:         m.NumGC,
		LastGC:        m.LastGC,
		PauseTotalNs:  m.PauseTotalNs,
		LastPauseNs:   lastPause,
		GCCPUFraction: m.GCCPUFraction,
		NextGC:        m.NextGC,
		Goroutines:    runtime.NumGoroutine(),
		UptimeSeconds: time.Since(processStartTime).Seconds(),
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		OpenFDs:       countOpenFDs(),
	}
}

// PerformancePage serves the performance admin page.
func (h *AdminUIHandler) PerformancePage(w http.ResponseWriter, r *http.Request) {
	isAdmin, accountID, basePath, username := h.getPageRoleInfo(r)

	if !isAdmin {
		http.Redirect(w, r, basePath, http.StatusFound)
		return
	}

	data := AdminPageData{
		CurrentPath: basePath + "/performance",
		BasePath:    basePath,
		IsAdmin:     isAdmin,
		AccountID:   accountID,
		Username:    username,
		Version:     GetBackendVersion(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.performanceTemplate == nil {
		http.Error(w, "Performance template not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.performanceTemplate.ExecuteTemplate(w, "base", data); err != nil {
		fmt.Printf("Performance template error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// GetPerformanceMetrics returns a single JSON snapshot of performance metrics.
func (h *AdminUIHandler) GetPerformanceMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(buildPerformanceMetrics())
}

// GetPerformanceSSE streams performance metrics via Server-Sent Events every 3 seconds.
func (h *AdminUIHandler) GetPerformanceSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"Streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	// Send initial snapshot immediately
	data, err := json.Marshal(buildPerformanceMetrics())
	if err == nil {
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			data, err := json.Marshal(buildPerformanceMetrics())
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

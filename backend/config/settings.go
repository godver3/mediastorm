package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Settings represents the application configuration persisted to disk.
type Settings struct {
	Server          ServerSettings         `json:"server"`
	Usenet          []UsenetSettings       `json:"usenet"`
	Indexers        []IndexerConfig        `json:"indexers"`
	TorrentScrapers []TorrentScraperConfig `json:"torrentScrapers"`
	Metadata        MetadataSettings       `json:"metadata"`
	Cache           CacheSettings          `json:"cache"`
	WebDAV          WebDAVSettings         `json:"webdav"`
	Database        DatabaseSettings       `json:"database"`
	Streaming       StreamingSettings      `json:"streaming"`
	Import          ImportSettings         `json:"import"`
	SABnzbd         SABnzbdSettings        `json:"sabnzbd"`
	AltMount        *AltMountSettings      `json:"altmount,omitempty"`
	Transmux        TransmuxSettings       `json:"transmux"`
	Playback        PlaybackSettings       `json:"playback"`
	Live            LiveSettings           `json:"live"`
	HomeShelves     HomeShelvesSettings    `json:"homeShelves"`
	Filtering       FilterSettings         `json:"filtering"`
	UI              UISettings             `json:"ui"`
	Subtitles       SubtitleSettings       `json:"subtitles"`
}

type ServerSettings struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	APIKey string `json:"apiKey"` // Deprecated: kept for migration compatibility
	PIN    string `json:"pin"`    // 6-digit PIN for authentication
}

type UsenetSettings struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	SSL         bool   `json:"ssl"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	Enabled     bool   `json:"enabled"`
}

type IndexerConfig struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	APIKey  string `json:"apiKey"`
	Type    string `json:"type"` // newznab | torznab
	Enabled bool   `json:"enabled"`
}

type TorrentScraperConfig struct {
	Name    string            `json:"name"`    // "Torrentio", "Prowlarr", "Jackett", "Zilean"
	Type    string            `json:"type"`    // "torrentio", "prowlarr", "jackett", "zilean"
	URL     string            `json:"url"`     // For Prowlarr/Jackett/Zilean
	APIKey  string            `json:"apiKey"`  // For Prowlarr/Jackett
	Options string            `json:"options"` // For Torrentio: URL path options (e.g., "sort=qualitysize|qualityfilter=480p,scr,cam")
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config,omitempty"` // Scraper-specific config
}

type MetadataSettings struct {
	TVDBAPIKey string `json:"tvdbApiKey"`
	TMDBAPIKey string `json:"tmdbApiKey"`
	Language   string `json:"language"`
}

type CacheSettings struct {
	Directory        string `json:"directory"`
	MetadataTTLHours int    `json:"metadataTtlHours"`
}

// LogConfig represents logging configuration (for altmount compatibility)
type LogConfig struct {
	File       string `json:"file"`
	Level      string `json:"level"`
	MaxSize    int    `json:"maxSize"`
	MaxAge     int    `json:"maxAge"`
	MaxBackups int    `json:"maxBackups"`
	Compress   bool   `json:"compress"`
}

// TransmuxSettings describes optional container conversion for browser playback
type TransmuxSettings struct {
	Enabled     bool   `json:"enabled"`
	FFmpegPath  string `json:"ffmpegPath"`
	FFprobePath string `json:"ffprobePath"`
}

// WebDAVSettings defines WebDAV server configuration
type WebDAVSettings struct {
	Enabled  bool   `json:"enabled"`
	Prefix   string `json:"prefix"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// DatabaseSettings defines database configuration for queue management
type DatabaseSettings struct {
	Path string `json:"path"`
}

// StreamingSettings defines streaming and download configuration
type StreamingSettings struct {
	MaxDownloadWorkers int                      `json:"maxDownloadWorkers"`
	MaxCacheSizeMB     int                      `json:"maxCacheSizeMB"`
	ServiceMode        StreamingServiceMode     `json:"serviceMode"`
	DebridProviders    []DebridProviderSettings `json:"debridProviders,omitempty"`
}

type StreamingServicePriority string

const (
	StreamingServicePriorityNone   StreamingServicePriority = "none"
	StreamingServicePriorityUsenet StreamingServicePriority = "usenet"
	StreamingServicePriorityDebrid StreamingServicePriority = "debrid"
)

type StreamingServiceMode string

const (
	StreamingServiceModeUsenet StreamingServiceMode = "usenet"
	StreamingServiceModeDebrid StreamingServiceMode = "debrid"
	StreamingServiceModeHybrid StreamingServiceMode = "hybrid"
)

type DebridProviderSettings struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
	Enabled  bool   `json:"enabled"`
}

// ImportSettings defines import/queue processing configuration
type ImportSettings struct {
	QueueProcessingIntervalSeconds int  `json:"queueProcessingIntervalSeconds"`
	RarMaxWorkers                  int  `json:"rarMaxWorkers"`
	RarMaxCacheSizeMB              int  `json:"rarMaxCacheSizeMb"`
	RarEnableMemoryPreload         bool `json:"rarEnableMemoryPreload"`
	RarMaxMemoryGB                 int  `json:"rarMaxMemoryGB"`
	SkipHealthCheck                bool `json:"skipHealthCheck"` // Skip segment health check for faster playback
}

// SABnzbdSettings defines SABnzbd fallback configuration
type SABnzbdSettings struct {
	Enabled        *bool  `json:"enabled"`
	FallbackHost   string `json:"fallbackHost"`
	FallbackAPIKey string `json:"fallbackApiKey"`
}

// AltMountSettings captures legacy AltMount configuration and is ignored by the
// current server. The struct is retained to gracefully load older configs.
type AltMountSettings struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}

// PlaybackSettings controls how the client should launch resolved streams.
type PlaybackSettings struct {
	PreferredPlayer           string `json:"preferredPlayer"`
	PreferredAudioLanguage    string `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage string `json:"preferredSubtitleLanguage,omitempty"`
	PreferredSubtitleMode     string `json:"preferredSubtitleMode,omitempty"`
	UseLoadingScreen          bool   `json:"useLoadingScreen,omitempty"`
}

// LiveSettings controls Live TV playlist caching behavior.
type LiveSettings struct {
	PlaylistURL           string `json:"playlistUrl"`
	PlaylistCacheTTLHours int    `json:"playlistCacheTtlHours"`
}

// ShelfConfig represents a configurable home screen shelf.
type ShelfConfig struct {
	ID      string `json:"id"`      // Unique identifier (e.g., "continue-watching", "watchlist", "trending-movies")
	Name    string `json:"name"`    // Display name
	Enabled bool   `json:"enabled"` // Whether the shelf is visible
	Order   int    `json:"order"`   // Sort order (lower numbers appear first)
}

// TrendingMovieSource determines which source to use for trending movies.
type TrendingMovieSource string

const (
	TrendingMovieSourceAll      TrendingMovieSource = "all"      // TMDB trending (includes unreleased)
	TrendingMovieSourceReleased TrendingMovieSource = "released" // MDBList top movies of the week (released only)
)

// HomeShelvesSettings controls which shelves appear on the home screen and their order.
type HomeShelvesSettings struct {
	Shelves             []ShelfConfig       `json:"shelves"`
	TrendingMovieSource TrendingMovieSource `json:"trendingMovieSource,omitempty"` // "all" (TMDB) or "released" (MDBList)
}

// FilterSettings controls content filtering preferences.
type FilterSettings struct {
	MaxSizeMovieGB   float64                  `json:"maxSizeMovieGb"`
	MaxSizeEpisodeGB float64                  `json:"maxSizeEpisodeGb"`
	ExcludeHdr       bool                     `json:"excludeHdr"`
	PrioritizeHdr    bool                     `json:"prioritizeHdr"`    // Prioritize HDR/DV content in search results
	FilterOutTerms   []string                 `json:"filterOutTerms"`   // Terms to filter out from results (exact match in title)
	ServicePriority  StreamingServicePriority `json:"servicePriority"`  // Priority for service type in search results
}

// UISettings captures user interface preferences shared with the clients.
type UISettings struct {
	LoadingAnimationEnabled bool `json:"loadingAnimationEnabled"`
}

// SubtitleSettings defines subtitle provider configuration.
type SubtitleSettings struct {
	OpenSubtitlesUsername string `json:"openSubtitlesUsername"`
	OpenSubtitlesPassword string `json:"openSubtitlesPassword"`
}

// DefaultSettings returns sane defaults for a fresh install.
func DefaultSettings() Settings {
	sabnzbdEnabled := false
	return Settings{
		Server:   ServerSettings{Host: "0.0.0.0", Port: 7777, APIKey: "", PIN: ""},
		Usenet:   []UsenetSettings{},
		Indexers: []IndexerConfig{},
		TorrentScrapers: []TorrentScraperConfig{
			{Name: "Torrentio", Type: "torrentio", Enabled: true, Options: "sort=qualitysize|qualityfilter=480p,scr,cam"},
		},
		Metadata:  MetadataSettings{TVDBAPIKey: "", TMDBAPIKey: "", Language: "en"},
		Cache:     CacheSettings{Directory: "cache", MetadataTTLHours: 24},
		WebDAV:    WebDAVSettings{Enabled: true, Prefix: "/webdav", Username: "novastream", Password: ""},
		Database:  DatabaseSettings{Path: "cache/queue.db"},
		Streaming: StreamingSettings{MaxDownloadWorkers: 15, MaxCacheSizeMB: 100, ServiceMode: StreamingServiceModeUsenet, DebridProviders: []DebridProviderSettings{}},
		Import:    ImportSettings{QueueProcessingIntervalSeconds: 1, RarMaxWorkers: 40, RarMaxCacheSizeMB: 128, RarEnableMemoryPreload: true, RarMaxMemoryGB: 8},
		SABnzbd:   SABnzbdSettings{Enabled: &sabnzbdEnabled, FallbackHost: "", FallbackAPIKey: ""},
		AltMount:  nil,
		Transmux:  TransmuxSettings{Enabled: true, FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"},
		Playback:  PlaybackSettings{PreferredPlayer: "native", UseLoadingScreen: false},
		Live:      LiveSettings{PlaylistURL: "", PlaylistCacheTTLHours: 24},
		HomeShelves: HomeShelvesSettings{
			Shelves: []ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
				{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 2},
				{ID: "trending-tv", Name: "Trending TV Shows", Enabled: true, Order: 3},
			},
			TrendingMovieSource: TrendingMovieSourceReleased, // Default to released-only (MDBList)
		},
		Filtering: FilterSettings{
			MaxSizeMovieGB:   0,                             // 0 means no limit
			MaxSizeEpisodeGB: 0,                             // 0 means no limit
			ExcludeHdr:       false,                         // false = include HDR content
			PrioritizeHdr:    true,                          // true = prioritize HDR/DV content when not excluded
			ServicePriority:  StreamingServicePriorityNone,  // no service priority by default
		},
		UI: UISettings{
			LoadingAnimationEnabled: true,
		},
		Subtitles: SubtitleSettings{
			OpenSubtitlesUsername: "",
			OpenSubtitlesPassword: "",
		},
	}
}

// Manager loads and persists settings to a JSON file.
type Manager struct {
	path string
}

func NewManager(configPath string) *Manager {
	return &Manager{path: configPath}
}

// GetConfig returns the current configuration (for compatibility with altmount packages)
func (m *Manager) GetConfig() (*AltMountConfig, error) {
	settings, err := m.Load()
	if err != nil {
		return nil, err
	}

	return &AltMountConfig{
		RClone: RCloneConfig{
			Password: "",
			Salt:     "",
		},
		Streaming: StreamingConfig{
			MaxDownloadWorkers: settings.Streaming.MaxDownloadWorkers,
			MaxCacheSizeMB:     settings.Streaming.MaxCacheSizeMB,
		},
		Import: ImportConfig{
			QueueProcessingIntervalSeconds: settings.Import.QueueProcessingIntervalSeconds,
			RarMaxWorkers:                  settings.Import.RarMaxWorkers,
			RarMaxCacheSizeMB:              settings.Import.RarMaxCacheSizeMB,
		},
		SABnzbd: SABnzbdConfig{
			Enabled:        settings.SABnzbd.Enabled,
			FallbackHost:   settings.SABnzbd.FallbackHost,
			FallbackAPIKey: settings.SABnzbd.FallbackAPIKey,
		},
		WebDAV: WebDAVConfig{
			Prefix:   settings.WebDAV.Prefix,
			User:     settings.WebDAV.Username,
			Password: settings.WebDAV.Password,
		},
	}, nil
}

// EnsureDir ensures parent directory exists.
func (m *Manager) EnsureDir() error {
	dir := filepath.Dir(m.path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// Load reads settings.json from disk or creates defaults if missing.
func (m *Manager) Load() (Settings, error) {
	if m.path == "" {
		return Settings{}, errors.New("config path not set")
	}
	if _, err := os.Stat(m.path); errors.Is(err, fs.ErrNotExist) {
		// create with defaults
		defaults := DefaultSettings()
		if err := m.Save(defaults); err != nil {
			return Settings{}, err
		}
		return defaults, nil
	}
	f, err := os.Open(m.path)
	if err != nil {
		return Settings{}, err
	}
	defer f.Close()

	// First, try to decode into a raw map to check for old format
	var raw map[string]interface{}
	dec := json.NewDecoder(f)
	if err := dec.Decode(&raw); err != nil {
		return Settings{}, err
	}

	// Check if usenet is an object (old format) instead of array
	if usenetRaw, ok := raw["usenet"].(map[string]interface{}); ok {
		// Migrate old single usenet config to array format
		if host, _ := usenetRaw["host"].(string); strings.TrimSpace(host) != "" {
			// Convert to array format
			raw["usenet"] = []interface{}{usenetRaw}
			// Add Name field if not present
			if _, hasName := usenetRaw["name"]; !hasName {
				if arr, ok := raw["usenet"].([]interface{}); ok && len(arr) > 0 {
					if obj, ok := arr[0].(map[string]interface{}); ok {
						obj["name"] = "Primary"
					}
				}
			}
			// Add Enabled field if not present
			if _, hasEnabled := usenetRaw["enabled"]; !hasEnabled {
				if arr, ok := raw["usenet"].([]interface{}); ok && len(arr) > 0 {
					if obj, ok := arr[0].(map[string]interface{}); ok {
						obj["enabled"] = true
					}
				}
			}
		} else {
			// Empty config, convert to empty array
			raw["usenet"] = []interface{}{}
		}
	}

	// Ensure UI settings exist with sensible defaults
	if uiRaw, ok := raw["ui"]; ok {
		if uiMap, ok := uiRaw.(map[string]interface{}); ok {
			if _, has := uiMap["loadingAnimationEnabled"]; !has {
				uiMap["loadingAnimationEnabled"] = true
			}
		} else {
			raw["ui"] = map[string]interface{}{"loadingAnimationEnabled": true}
		}
	} else {
		raw["ui"] = map[string]interface{}{"loadingAnimationEnabled": true}
	}

	// Re-encode and decode into Settings struct
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return Settings{}, err
	}

	var s Settings
	if err := json.Unmarshal(rawJSON, &s); err != nil {
		return Settings{}, err
	}

	// Backfill defaults for newly introduced settings when config predates them
	if !s.Transmux.Enabled && strings.TrimSpace(s.Transmux.FFmpegPath) == "" && strings.TrimSpace(s.Transmux.FFprobePath) == "" {
		s.Transmux = TransmuxSettings{Enabled: true, FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"}
	} else {
		if strings.TrimSpace(s.Transmux.FFmpegPath) == "" {
			s.Transmux.FFmpegPath = "ffmpeg"
		}
		if strings.TrimSpace(s.Transmux.FFprobePath) == "" {
			s.Transmux.FFprobePath = "ffprobe"
		}
	}

	if strings.TrimSpace(s.Playback.PreferredPlayer) == "" {
		s.Playback.PreferredPlayer = "native"
	}

	// Backfill WebDAV settings
	if strings.TrimSpace(s.WebDAV.Prefix) == "" {
		s.WebDAV.Prefix = "/webdav"
	}
	if strings.TrimSpace(s.WebDAV.Username) == "" {
		s.WebDAV.Username = "novastream"
	}

	// Backfill Database settings
	if strings.TrimSpace(s.Database.Path) == "" {
		s.Database.Path = "cache/queue.db"
	}

	// Backfill Streaming settings
	if s.Streaming.MaxDownloadWorkers == 0 {
		s.Streaming.MaxDownloadWorkers = 15
	}
	if s.Streaming.MaxCacheSizeMB == 0 {
		s.Streaming.MaxCacheSizeMB = 100
	}
	if s.Streaming.ServiceMode == "" {
		s.Streaming.ServiceMode = StreamingServiceModeUsenet
	}
	// ServicePriority moved from Streaming to Filtering
	if s.Filtering.ServicePriority == "" {
		s.Filtering.ServicePriority = StreamingServicePriorityNone
	}
	if len(s.Streaming.DebridProviders) == 0 {
		s.Streaming.DebridProviders = []DebridProviderSettings{
			{Name: "Real Debrid", Provider: "realdebrid"},
			{Name: "Torbox", Provider: "torbox"},
			{Name: "AllDebrid", Provider: "alldebrid"},
		}
	}

	// Backfill Import settings
	if s.Import.QueueProcessingIntervalSeconds == 0 {
		s.Import.QueueProcessingIntervalSeconds = 1
	}
	if s.Import.RarMaxWorkers == 0 {
		s.Import.RarMaxWorkers = 40
	}
	if s.Import.RarMaxCacheSizeMB == 0 {
		s.Import.RarMaxCacheSizeMB = 128
	}
	if s.Import.RarMaxMemoryGB == 0 {
		s.Import.RarMaxMemoryGB = 8
	}

	// Backfill SABnzbd settings
	if s.SABnzbd.Enabled == nil {
		sabnzbdEnabled := false
		s.SABnzbd.Enabled = &sabnzbdEnabled
	}

	// Backfill Live settings
	if s.Live.PlaylistCacheTTLHours == 0 {
		s.Live.PlaylistCacheTTLHours = 24
	}
	// PlaylistURL defaults to empty string, no backfill needed

	// Backfill Indexers: migrate torznab to newznab (usenet indexers use newznab)
	for i := range s.Indexers {
		if strings.ToLower(s.Indexers[i].Type) == "torznab" {
			s.Indexers[i].Type = "newznab"
		}
	}

	// Backfill TorrentScrapers if empty
	if len(s.TorrentScrapers) == 0 {
		s.TorrentScrapers = []TorrentScraperConfig{
			{Name: "Torrentio", Type: "torrentio", Enabled: true, Options: "sort=qualitysize|qualityfilter=480p,scr,cam"},
		}
	}

	// Backfill HomeShelves if empty
	if len(s.HomeShelves.Shelves) == 0 {
		s.HomeShelves.Shelves = []ShelfConfig{
			{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
			{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
			{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 2},
			{ID: "trending-tv", Name: "Trending TV Shows", Enabled: true, Order: 3},
		}
	}

	// Backfill TrendingMovieSource if empty (default to released-only)
	if s.HomeShelves.TrendingMovieSource == "" {
		s.HomeShelves.TrendingMovieSource = TrendingMovieSourceReleased
	}

	// Backfill Filtering settings - no backfill needed as 0 and false are the correct defaults

	// Legacy AltMount configuration is ignored going forward.
	s.AltMount = nil
	return s, nil
}

// Save writes the provided settings to disk atomically.
func (m *Manager) Save(s Settings) error {
	if m.path == "" {
		return errors.New("config path not set")
	}
	if err := m.EnsureDir(); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, m.path)
}

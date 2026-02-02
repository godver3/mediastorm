package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	Display         DisplaySettings        `json:"display"`
	Subtitles       SubtitleSettings       `json:"subtitles"`
	MDBList         MDBListSettings        `json:"mdblist"`
	Trakt           TraktSettings          `json:"trakt,omitempty"`
	Plex            PlexSettings           `json:"plex,omitempty"`
	Log             LogConfig              `json:"log"`
	ScheduledTasks  ScheduledTasksSettings  `json:"scheduledTasks,omitempty"`
	Network         NetworkSettings         `json:"network,omitempty"`
	Ranking         RankingSettings         `json:"ranking,omitempty"`
	BackupRetention BackupRetentionSettings `json:"backupRetention,omitempty"`
}

type ServerSettings struct {
	Host string `json:"host"`
	Port int    `json:"port"`
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
	Name       string `json:"name"`
	URL        string `json:"url"`
	APIKey     string `json:"apiKey"`
	Type       string `json:"type"`       // newznab | torznab
	Categories string `json:"categories"` // Comma-separated newznab category IDs (e.g., "2000,2010,2020" for movies, "5000,5010,5020" for TV)
	Enabled    bool   `json:"enabled"`
}

type TorrentScraperConfig struct {
	Name    string            `json:"name"`    // "Torrentio", "Prowlarr", "Jackett", "Zilean", "AIOStreams", "Nyaa"
	Type    string            `json:"type"`    // "torrentio", "prowlarr", "jackett", "zilean", "aiostreams", "nyaa"
	URL     string            `json:"url"`     // For Prowlarr/Jackett/Zilean/AIOStreams/Nyaa (full URL with config token)
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
	Enabled          bool   `json:"enabled"`
	FFmpegPath       string `json:"ffmpegPath"`
	FFprobePath      string `json:"ffprobePath"`
	HLSTempDirectory string `json:"hlsTempDirectory"` // Directory for HLS segment storage (default: /tmp/novastream-hls)
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
	MaxDownloadWorkers          int                      `json:"maxDownloadWorkers"`
	MaxCacheSizeMB              int                      `json:"maxCacheSizeMB"`
	ServiceMode                 StreamingServiceMode     `json:"serviceMode"`
	ServicePriority             StreamingServicePriority `json:"servicePriority"`                 // Priority for service type in search results
	SearchMode                  SearchMode               `json:"searchMode"`                      // Fast (early return) vs Accurate (wait for all results)
	DebridProviders             []DebridProviderSettings `json:"debridProviders,omitempty"`
	MultiProviderMode           MultiProviderMode        `json:"multiProviderMode,omitempty"`     // How to select provider when multiple are enabled
	UsenetResolutionTimeoutSec  int                      `json:"usenetResolutionTimeoutSec"`      // Timeout for usenet content resolution in seconds (0 = no limit)
	IndexerTimeoutSec           int                      `json:"indexerTimeoutSec"`               // Timeout for indexer/scraper searches in seconds (default: 5)
}

// SearchMode determines how scraper/indexer results are aggregated
type SearchMode string

const (
	// SearchModeFast returns results as soon as enough are available (early return)
	SearchModeFast SearchMode = "fast"
	// SearchModeAccurate waits for all scrapers/indexers to complete before returning
	SearchModeAccurate SearchMode = "accurate"
)

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
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	APIKey   string            `json:"apiKey"`
	Enabled  bool              `json:"enabled"`
	Config   map[string]string `json:"config,omitempty"` // Provider-specific settings (e.g., "autoClearQueue": "true" for Torbox)
}

// MultiProviderMode determines how multiple debrid providers are used
type MultiProviderMode string

const (
	// MultiProviderModeFastest uses whichever provider returns a cached result first (race)
	MultiProviderModeFastest MultiProviderMode = "fastest"
	// MultiProviderModePreferred waits for all providers and uses the highest-priority cached result
	MultiProviderModePreferred MultiProviderMode = "preferred"
)

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
	PreferredPlayer           string  `json:"preferredPlayer"`
	PreferredAudioLanguage    string  `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage string  `json:"preferredSubtitleLanguage,omitempty"`
	PreferredSubtitleMode     string  `json:"preferredSubtitleMode,omitempty"`
	UseLoadingScreen          bool    `json:"useLoadingScreen,omitempty"`
	SubtitleSize              float64 `json:"subtitleSize,omitempty"`    // Scaling factor for subtitle size (1.0 = default)
	SeekForwardSeconds        int     `json:"seekForwardSeconds"`        // Seconds to skip forward (default 30)
	SeekBackwardSeconds       int     `json:"seekBackwardSeconds"`       // Seconds to skip backward (default 10)
	ForceAACTranscoding       bool    `json:"forceAacTranscoding"`       // Force transcoding of AC3/EAC3/DTS audio to AAC for Bluetooth compatibility
}

// LiveTVFilterSettings controls backend-side filtering for Live TV channels.
type LiveTVFilterSettings struct {
	EnabledCategories []string `json:"enabledCategories"` // Only show channels in these categories (empty = show all)
	MaxChannels       int      `json:"maxChannels"`       // Overall channel limit (0 = no limit)
}

// EPGSource represents a single EPG data source.
type EPGSource struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`     // "xmltv"
	URL      string `json:"url"`
	Priority int    `json:"priority"` // Lower = higher priority
	Enabled  bool   `json:"enabled"`
}

// EPGSettings controls Electronic Program Guide configuration.
type EPGSettings struct {
	Enabled              bool        `json:"enabled"`
	XmltvUrl             string      `json:"xmltvUrl,omitempty"`   // Simple XMLTV URL (alternative to Sources array)
	Sources              []EPGSource `json:"sources,omitempty"`
	RefreshIntervalHours int         `json:"refreshIntervalHours"` // Default: 12
	RetentionDays        int         `json:"retentionDays"`        // Default: 7
}

// LiveSettings controls Live TV playlist caching behavior.
type LiveSettings struct {
	Mode                  string               `json:"mode"`                  // "m3u" or "xtream" - how to source the playlist
	PlaylistURL           string               `json:"playlistUrl"`           // M3U playlist URL (used when mode is "m3u")
	XtreamHost            string               `json:"xtreamHost"`            // Xtream Codes server URL (e.g., "http://example.com:8080")
	XtreamUsername        string               `json:"xtreamUsername"`        // Xtream Codes username
	XtreamPassword        string               `json:"xtreamPassword"`        // Xtream Codes password
	PlaylistCacheTTLHours int                  `json:"playlistCacheTtlHours"`
	ProbeSizeMB           int                  `json:"probeSizeMb"`           // FFmpeg probesize in MB (0 = default ~5MB)
	AnalyzeDurationSec    int                  `json:"analyzeDurationSec"`    // FFmpeg analyzeduration in seconds (0 = default ~5s)
	LowLatency            bool                 `json:"lowLatency"`            // Enable low-latency mode (nobuffer + low_delay flags)
	Filtering             LiveTVFilterSettings `json:"filtering"`             // Backend-side channel filtering
	EPG                   EPGSettings          `json:"epg"`                   // Electronic Program Guide settings
}

// GetEffectivePlaylistURL returns the playlist URL based on the configured mode.
// For Xtream Codes mode, it constructs the M3U URL from the credentials.
func (ls *LiveSettings) GetEffectivePlaylistURL() string {
	if ls.Mode == "xtream" && ls.XtreamHost != "" && ls.XtreamUsername != "" && ls.XtreamPassword != "" {
		host := strings.TrimRight(ls.XtreamHost, "/")
		return fmt.Sprintf("%s/get.php?username=%s&password=%s&type=m3u&output=ts",
			host, url.QueryEscape(ls.XtreamUsername), url.QueryEscape(ls.XtreamPassword))
	}
	return ls.PlaylistURL
}

// ShelfConfig represents a configurable home screen shelf.
type ShelfConfig struct {
	ID             string `json:"id"`                       // Unique identifier (e.g., "continue-watching", "watchlist", "trending-movies")
	Name           string `json:"name"`                     // Display name
	Enabled        bool   `json:"enabled"`                  // Whether the shelf is visible
	Order          int    `json:"order"`                    // Sort order (lower numbers appear first)
	Type           string `json:"type,omitempty"`           // "builtin" (default) or "mdblist" for custom lists
	ListURL        string `json:"listUrl,omitempty"`        // MDBList URL for custom lists (e.g., https://mdblist.com/lists/username/list-name/json)
	Limit          int    `json:"limit,omitempty"`          // Optional limit on number of items returned (0 = no limit)
	HideUnreleased bool   `json:"hideUnreleased,omitempty"` // Filter out unreleased/in-theaters content
}

// TrendingMovieSource determines which source to use for trending movies.
type TrendingMovieSource string

const (
	TrendingMovieSourceAll      TrendingMovieSource = "all"      // TMDB trending (includes unreleased)
	TrendingMovieSourceReleased TrendingMovieSource = "released" // MDBList top movies of the week (released only)
)

// ExploreCardPosition determines where the explore card is placed on shelves.
type ExploreCardPosition string

const (
	ExploreCardPositionFront ExploreCardPosition = "front" // Explore card at the beginning of the shelf (default)
	ExploreCardPositionEnd   ExploreCardPosition = "end"   // Explore card at the end of the shelf
)

// HomeShelvesSettings controls which shelves appear on the home screen and their order.
type HomeShelvesSettings struct {
	Shelves             []ShelfConfig       `json:"shelves"`
	TrendingMovieSource TrendingMovieSource `json:"trendingMovieSource,omitempty"` // "all" (TMDB) or "released" (MDBList)
	ExploreCardPosition ExploreCardPosition `json:"exploreCardPosition,omitempty"` // "front" (default) or "end"
}

// HDRDVPolicy determines what HDR/DV content to exclude from search results.
type HDRDVPolicy string

const (
	// HDRDVPolicyNoExclusion excludes all HDR/DV content - only SDR allowed
	HDRDVPolicyNoExclusion HDRDVPolicy = "none"
	// HDRDVPolicyIncludeHDR allows HDR and DV profile 7/8 (DV profile 5 rejected at probe time)
	HDRDVPolicyIncludeHDR HDRDVPolicy = "hdr"
	// HDRDVPolicyIncludeHDRDV allows all content including all DV profiles - no filtering
	HDRDVPolicyIncludeHDRDV HDRDVPolicy = "hdr_dv"
)

// FilterSettings controls content filtering preferences.
type FilterSettings struct {
	MaxSizeMovieGB                   float64     `json:"maxSizeMovieGb"`
	MaxSizeEpisodeGB                 float64     `json:"maxSizeEpisodeGb"`
	MaxResolution                    string      `json:"maxResolution"`                    // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	HDRDVPolicy                      HDRDVPolicy `json:"hdrDvPolicy"`                      // HDR/DV inclusion policy: "none" (no exclusion), "hdr" (include HDR + DV 7/8), "hdr_dv" (include all HDR/DV)
	PrioritizeHdr                    bool        `json:"prioritizeHdr"`                    // Prioritize HDR/DV content in search results
	FilterOutTerms                   []string    `json:"filterOutTerms"`                   // Terms to filter out from results (case-insensitive match in title)
	PreferredTerms                   []string    `json:"preferredTerms"`                   // Terms to prioritize in results (case-insensitive match in title)
	BypassFilteringForAIOStreamsOnly bool        `json:"bypassFilteringForAioStreamsOnly"` // Skip strmr filtering/ranking when AIOStreams is the only enabled scraper (debrid-only mode)
}

// UISettings captures user interface preferences shared with the clients.
type UISettings struct {
	LoadingAnimationEnabled bool `json:"loadingAnimationEnabled"`
}

// DisplaySettings controls UI display preferences.
type DisplaySettings struct {
	// BadgeVisibility controls which badges appear on media cards.
	// Valid values: "watchProgress", "releaseStatus", "watchState", "unwatchedCount"
	BadgeVisibility []string `json:"badgeVisibility"`
	// WatchStateIconStyle controls the color of watch state icons.
	// "colored" (default) = green/yellow circles, "white" = all white circles
	WatchStateIconStyle string `json:"watchStateIconStyle"`
	// HideWatched filters out fully watched content from trending shelves and custom lists.
	HideWatched bool `json:"hideWatched,omitempty"`
}

// SubtitleSettings defines subtitle provider configuration.
type SubtitleSettings struct {
	OpenSubtitlesUsername string `json:"openSubtitlesUsername"`
	OpenSubtitlesPassword string `json:"openSubtitlesPassword"`
}

// MDBListSettings defines MDBList integration for aggregated ratings.
type MDBListSettings struct {
	APIKey         string   `json:"apiKey"`
	Enabled        bool     `json:"enabled"`
	EnabledRatings []string `json:"enabledRatings"` // Which rating sources to display: trakt, imdb, tmdb, letterboxd, tomatoes, audience, metacritic
}

// TraktAccount represents a registered Trakt account with its own credentials and OAuth tokens.
type TraktAccount struct {
	ID                string `json:"id"`                          // UUID for this account
	Name              string `json:"name"`                        // Display name (defaults to Trakt username)
	OwnerAccountID    string `json:"ownerAccountId,omitempty"`    // Login account that owns this Trakt account (empty = master/shared)
	ClientID          string `json:"clientId"`                    // Trakt API client ID
	ClientSecret      string `json:"clientSecret"`                // Trakt API client secret
	AccessToken       string `json:"accessToken,omitempty"`       // OAuth access token
	RefreshToken      string `json:"refreshToken,omitempty"`      // OAuth refresh token
	ExpiresAt         int64  `json:"expiresAt,omitempty"`         // Unix timestamp when access token expires
	Username          string `json:"username,omitempty"`          // Trakt username (populated after OAuth)
	ScrobblingEnabled bool   `json:"scrobblingEnabled,omitempty"` // Whether to scrobble for profiles using this account
}

// TraktSettings defines Trakt integration configuration.
type TraktSettings struct {
	// Accounts is the list of registered Trakt accounts
	Accounts []TraktAccount `json:"accounts,omitempty"`

	// Legacy fields - kept for migration, will be moved to Accounts on load
	ClientID          string `json:"clientId,omitempty"`
	ClientSecret      string `json:"clientSecret,omitempty"`
	AccessToken       string `json:"accessToken,omitempty"`
	RefreshToken      string `json:"refreshToken,omitempty"`
	ExpiresAt         int64  `json:"expiresAt,omitempty"`
	Username          string `json:"username,omitempty"`
	ScrobblingEnabled bool   `json:"scrobblingEnabled,omitempty"`
}

// GetAccountByID returns a Trakt account by its ID, or nil if not found.
func (t *TraktSettings) GetAccountByID(id string) *TraktAccount {
	for i := range t.Accounts {
		if t.Accounts[i].ID == id {
			return &t.Accounts[i]
		}
	}
	return nil
}

// UpdateAccount updates an existing account or adds it if not found.
func (t *TraktSettings) UpdateAccount(account TraktAccount) {
	for i := range t.Accounts {
		if t.Accounts[i].ID == account.ID {
			t.Accounts[i] = account
			return
		}
	}
	t.Accounts = append(t.Accounts, account)
}

// RemoveAccount removes an account by ID.
func (t *TraktSettings) RemoveAccount(id string) bool {
	for i := range t.Accounts {
		if t.Accounts[i].ID == id {
			t.Accounts = append(t.Accounts[:i], t.Accounts[i+1:]...)
			return true
		}
	}
	return false
}

// PlexSettings defines Plex integration configuration.
// PlexAccount represents a registered Plex account with its auth token.
type PlexAccount struct {
	ID             string `json:"id"`                       // UUID for this account
	Name           string `json:"name"`                     // Display name
	OwnerAccountID string `json:"ownerAccountId,omitempty"` // Login account that owns this Plex account
	AuthToken      string `json:"authToken,omitempty"`      // Plex auth token
	Username       string `json:"username,omitempty"`       // Plex username
	UserID         int    `json:"userId,omitempty"`         // Plex user ID (for filtering watch history)
}

type PlexSettings struct {
	// Accounts is the list of registered Plex accounts
	Accounts []PlexAccount `json:"accounts,omitempty"`

	// Legacy fields - kept for migration
	AuthToken string `json:"authToken,omitempty"`
	Username  string `json:"username,omitempty"`
}

// GetAccountByID returns a Plex account by its ID.
func (p *PlexSettings) GetAccountByID(id string) *PlexAccount {
	for i := range p.Accounts {
		if p.Accounts[i].ID == id {
			return &p.Accounts[i]
		}
	}
	return nil
}

// UpdateAccount updates an existing Plex account.
func (p *PlexSettings) UpdateAccount(account PlexAccount) {
	for i := range p.Accounts {
		if p.Accounts[i].ID == account.ID {
			p.Accounts[i] = account
			return
		}
	}
}

// RemoveAccount removes a Plex account by ID.
func (p *PlexSettings) RemoveAccount(id string) bool {
	for i := range p.Accounts {
		if p.Accounts[i].ID == id {
			p.Accounts = append(p.Accounts[:i], p.Accounts[i+1:]...)
			return true
		}
	}
	return false
}

// ScheduledTaskType defines the type of scheduled task
type ScheduledTaskType string

const (
	ScheduledTaskTypePlexWatchlistSync ScheduledTaskType = "plex_watchlist_sync"
	ScheduledTaskTypeTraktListSync     ScheduledTaskType = "trakt_list_sync"
	ScheduledTaskTypeEPGRefresh        ScheduledTaskType = "epg_refresh"
	ScheduledTaskTypePlaylistRefresh   ScheduledTaskType = "playlist_refresh"
	ScheduledTaskTypeBackup            ScheduledTaskType = "backup"
)

// BackupRetentionSettings controls how backups are automatically cleaned up
type BackupRetentionSettings struct {
	RetentionDays  int `json:"retentionDays"`  // Delete backups older than X days (0 = never auto-delete)
	RetentionCount int `json:"retentionCount"` // Keep at most X backups (0 = unlimited)
}

// ScheduledTaskFrequency defines how often a task runs
type ScheduledTaskFrequency string

const (
	ScheduledTaskFrequency1Min    ScheduledTaskFrequency = "1min"
	ScheduledTaskFrequency5Min    ScheduledTaskFrequency = "5min"
	ScheduledTaskFrequency15Min   ScheduledTaskFrequency = "15min"
	ScheduledTaskFrequency30Min   ScheduledTaskFrequency = "30min"
	ScheduledTaskFrequencyHourly  ScheduledTaskFrequency = "hourly"
	ScheduledTaskFrequency6Hours  ScheduledTaskFrequency = "6hours"
	ScheduledTaskFrequency12Hours ScheduledTaskFrequency = "12hours"
	ScheduledTaskFrequencyDaily   ScheduledTaskFrequency = "daily"
)

// ScheduledTaskStatus represents the last run status
type ScheduledTaskStatus string

const (
	ScheduledTaskStatusPending ScheduledTaskStatus = "pending"
	ScheduledTaskStatusRunning ScheduledTaskStatus = "running"
	ScheduledTaskStatusSuccess ScheduledTaskStatus = "success"
	ScheduledTaskStatusError   ScheduledTaskStatus = "error"
)

// DryRunDetails contains the results of a dry run sync operation
type DryRunDetails struct {
	ToAdd    []DryRunItem `json:"toAdd,omitempty"`
	ToRemove []DryRunItem `json:"toRemove,omitempty"`
}

// DryRunItem represents a single item in dry run results
type DryRunItem struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	ID        string `json:"id"`
}

// ScheduledTask represents a single scheduled task configuration
type ScheduledTask struct {
	ID            string                 `json:"id"`
	Type          ScheduledTaskType      `json:"type"`
	Name          string                 `json:"name"`
	Enabled       bool                   `json:"enabled"`
	Frequency     ScheduledTaskFrequency `json:"frequency"`
	Config        map[string]string      `json:"config"`                    // Task-specific config (e.g., plexAccountId, profileId)
	LastRunAt     *time.Time             `json:"lastRunAt,omitempty"`
	LastStatus    ScheduledTaskStatus    `json:"lastStatus"`
	LastError     string                 `json:"lastError,omitempty"`
	ItemsImported int                    `json:"itemsImported,omitempty"`
	DryRunDetails *DryRunDetails         `json:"dryRunDetails,omitempty"`   // Results from dry run (what would be added/removed)
	CreatedAt     time.Time              `json:"createdAt"`
}

// ScheduledTasksSettings contains all scheduled task configurations
type ScheduledTasksSettings struct {
	Tasks                []ScheduledTask `json:"tasks"`
	CheckIntervalSeconds int             `json:"checkIntervalSeconds"` // How often scheduler checks for due tasks (default: 60)
}

// NetworkSettings configures network-aware backend URL switching.
// When the device is connected to the home WiFi network (matching HomeWifiSSID),
// the frontend will use HomeBackendUrl. Otherwise, it uses RemoteBackendUrl.
// This allows seamless switching between local and remote access.
type NetworkSettings struct {
	HomeWifiSSID     string `json:"homeWifiSSID"`     // WiFi SSID to detect for home network (e.g., "MyHomeWiFi")
	HomeBackendUrl   string `json:"homeBackendUrl"`   // Backend URL when on home WiFi (e.g., "http://192.168.1.100:7777/api")
	RemoteBackendUrl string `json:"remoteBackendUrl"` // Backend URL when on mobile/other networks (e.g., "https://myserver.com:7777/api")
}

// RankingCriterionID identifies a ranking criterion.
type RankingCriterionID string

const (
	RankingServicePriority RankingCriterionID = "service-priority"
	RankingPreferredTerms  RankingCriterionID = "preferred-terms"
	RankingResolution      RankingCriterionID = "resolution"
	RankingHDR             RankingCriterionID = "hdr"
	RankingLanguage        RankingCriterionID = "language"
	RankingSize            RankingCriterionID = "size"
)

// RankingCriterion represents a single ranking criterion with its configuration.
type RankingCriterion struct {
	ID      RankingCriterionID `json:"id"`
	Name    string             `json:"name"`
	Enabled bool               `json:"enabled"`
	Order   int                `json:"order"`
}

// RankingSettings holds the ordered list of ranking criteria.
type RankingSettings struct {
	Criteria []RankingCriterion `json:"criteria"`
}

// DefaultRankingCriteria returns the default ranking criteria in their default order.
func DefaultRankingCriteria() []RankingCriterion {
	return []RankingCriterion{
		{ID: RankingServicePriority, Name: "Service Priority", Enabled: true, Order: 0},
		{ID: RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 1},
		{ID: RankingResolution, Name: "Resolution", Enabled: true, Order: 2},
		{ID: RankingHDR, Name: "HDR/Dolby Vision", Enabled: true, Order: 3},
		{ID: RankingLanguage, Name: "Language", Enabled: true, Order: 4},
		{ID: RankingSize, Name: "File Size", Enabled: true, Order: 5},
	}
}

// DefaultSettings returns sane defaults for a fresh install.
func DefaultSettings() Settings {
	sabnzbdEnabled := false
	return Settings{
		Server:   ServerSettings{Host: "0.0.0.0", Port: 7777},
		Usenet:   []UsenetSettings{},
		Indexers: []IndexerConfig{},
		TorrentScrapers: []TorrentScraperConfig{
			{Name: "Torrentio", Type: "torrentio", Enabled: true, Options: "sort=qualitysize|qualityfilter=480p,scr,cam"},
		},
		Metadata:  MetadataSettings{TVDBAPIKey: "", TMDBAPIKey: "", Language: "eng"},
		Cache:     CacheSettings{Directory: "cache", MetadataTTLHours: 24},
		WebDAV:    WebDAVSettings{Enabled: true, Prefix: "/webdav", Username: "novastream", Password: ""},
		Database:  DatabaseSettings{Path: "cache/queue.db"},
		Streaming: StreamingSettings{MaxDownloadWorkers: 15, MaxCacheSizeMB: 100, ServiceMode: StreamingServiceModeUsenet, ServicePriority: StreamingServicePriorityNone, SearchMode: SearchModeFast, DebridProviders: []DebridProviderSettings{}, UsenetResolutionTimeoutSec: 0, IndexerTimeoutSec: 5},
		Import:    ImportSettings{QueueProcessingIntervalSeconds: 1, RarMaxWorkers: 40, RarMaxCacheSizeMB: 128, RarEnableMemoryPreload: true, RarMaxMemoryGB: 8},
		SABnzbd:   SABnzbdSettings{Enabled: &sabnzbdEnabled, FallbackHost: "", FallbackAPIKey: ""},
		AltMount:  nil,
		Transmux:  TransmuxSettings{Enabled: true, FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", HLSTempDirectory: "/tmp/novastream-hls"},
		Playback:  PlaybackSettings{PreferredPlayer: "native", UseLoadingScreen: false, SubtitleSize: 1.0, SeekForwardSeconds: 30, SeekBackwardSeconds: 10},
		Live:      LiveSettings{Mode: "m3u", PlaylistURL: "", PlaylistCacheTTLHours: 24},
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
			MaxSizeMovieGB:   0,                        // 0 means no limit
			MaxSizeEpisodeGB: 0,                        // 0 means no limit
			HDRDVPolicy:      HDRDVPolicyIncludeHDRDV,  // "hdr_dv" = allow all content (no HDR/DV filtering)
			PrioritizeHdr:    true,                     // true = prioritize HDR/DV content when available
		},
		UI: UISettings{
			LoadingAnimationEnabled: true,
		},
		Display: DisplaySettings{
			BadgeVisibility:     []string{"watchProgress"},
			WatchStateIconStyle: "colored",
		},
		Subtitles: SubtitleSettings{
			OpenSubtitlesUsername: "",
			OpenSubtitlesPassword: "",
		},
		MDBList: MDBListSettings{
			APIKey:         "",
			Enabled:        false,
			EnabledRatings: []string{"imdb", "tomatoes", "audience"}, // Default to IMDB and Rotten Tomatoes
		},
		Trakt: TraktSettings{},
		Plex:  PlexSettings{},
		Log: LogConfig{
			File:       "cache/logs/backend.log",
			Level:      "info",
			MaxSize:    50,   // 50 MB per file
			MaxBackups: 3,    // keep 3 old files
			MaxAge:     7,    // 7 days
			Compress:   true, // compress old files
		},
		ScheduledTasks: ScheduledTasksSettings{
			Tasks:                []ScheduledTask{},
			CheckIntervalSeconds: 60, // Check every 60 seconds
		},
		Network: NetworkSettings{
			HomeWifiSSID:     "",
			HomeBackendUrl:   "",
			RemoteBackendUrl: "",
		},
		Ranking: RankingSettings{
			Criteria: DefaultRankingCriteria(),
		},
		BackupRetention: BackupRetentionSettings{
			RetentionDays:  30, // Delete backups older than 30 days
			RetentionCount: 10, // Keep at most 10 backups
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

	// Migrate servicePriority from filtering to streaming
	if filteringRaw, ok := raw["filtering"].(map[string]interface{}); ok {
		if servicePriority, hasPriority := filteringRaw["servicePriority"]; hasPriority {
			// Move it to streaming section
			if streamingRaw, ok := raw["streaming"].(map[string]interface{}); ok {
				streamingRaw["servicePriority"] = servicePriority
			} else {
				raw["streaming"] = map[string]interface{}{"servicePriority": servicePriority}
			}
			// Remove from filtering
			delete(filteringRaw, "servicePriority")
		}
	}

	// Migrate excludeHdr (bool) to hdrDvPolicy (string enum)
	if filteringRaw, ok := raw["filtering"].(map[string]interface{}); ok {
		// Only migrate if hdrDvPolicy is not already set
		if _, hasPolicy := filteringRaw["hdrDvPolicy"]; !hasPolicy {
			if excludeHdr, hasExclude := filteringRaw["excludeHdr"]; hasExclude {
				if exclude, ok := excludeHdr.(bool); ok && exclude {
					// excludeHdr: true means exclude all HDR/DV content
					// This maps to NOT having any inclusion policy, but since we now
					// have inclusion policies, we'll need to handle this in the filtering logic
					// For backwards compatibility, we don't have a "exclude all HDR" option,
					// so we'll set it to "none" (no exclusion) and let the user reconfigure
					filteringRaw["hdrDvPolicy"] = "none"
				} else {
					// excludeHdr: false means include HDR content, map to "none" (no exclusion)
					filteringRaw["hdrDvPolicy"] = "none"
				}
				delete(filteringRaw, "excludeHdr")
			}
		}
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
		s.Transmux = TransmuxSettings{Enabled: true, FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", HLSTempDirectory: "/tmp/novastream-hls"}
	} else {
		if strings.TrimSpace(s.Transmux.FFmpegPath) == "" {
			s.Transmux.FFmpegPath = "ffmpeg"
		}
		if strings.TrimSpace(s.Transmux.FFprobePath) == "" {
			s.Transmux.FFprobePath = "ffprobe"
		}
		if strings.TrimSpace(s.Transmux.HLSTempDirectory) == "" {
			s.Transmux.HLSTempDirectory = "/tmp/novastream-hls"
		}
	}

	if strings.TrimSpace(s.Playback.PreferredPlayer) == "" {
		s.Playback.PreferredPlayer = "native"
	}

	// Backfill SubtitleSize if not set (0 means unset since it's omitempty)
	if s.Playback.SubtitleSize == 0 {
		s.Playback.SubtitleSize = 1.0
	}

	// Backfill seek times if not set (0 means unset)
	if s.Playback.SeekForwardSeconds == 0 {
		s.Playback.SeekForwardSeconds = 30
	}
	if s.Playback.SeekBackwardSeconds == 0 {
		s.Playback.SeekBackwardSeconds = 10
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
	// Backfill ServicePriority if not set
	if s.Streaming.ServicePriority == "" {
		s.Streaming.ServicePriority = StreamingServicePriorityNone
	}
	// Backfill SearchMode if not set (default to fast for best UX)
	if s.Streaming.SearchMode == "" {
		s.Streaming.SearchMode = SearchModeFast
	}
	if len(s.Streaming.DebridProviders) == 0 {
		s.Streaming.DebridProviders = []DebridProviderSettings{
			{Name: "Real Debrid", Provider: "realdebrid"},
			{Name: "Torbox", Provider: "torbox"},
			{Name: "AllDebrid", Provider: "alldebrid"},
		}
	}
	// Backfill MultiProviderMode if not set (default to fastest for best UX)
	if s.Streaming.MultiProviderMode == "" {
		s.Streaming.MultiProviderMode = MultiProviderModeFastest
	}
	// Backfill IndexerTimeoutSec if not set (0 means use default of 5 seconds)
	if s.Streaming.IndexerTimeoutSec <= 0 {
		s.Streaming.IndexerTimeoutSec = 5
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
	// Backfill Mode to "m3u" for backward compatibility
	if s.Live.Mode == "" {
		s.Live.Mode = "m3u"
	}
	// Backfill EPG settings
	if s.Live.EPG.RefreshIntervalHours == 0 {
		s.Live.EPG.RefreshIntervalHours = 12
	}
	if s.Live.EPG.RetentionDays == 0 {
		s.Live.EPG.RetentionDays = 7
	}

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

	// Backfill ExploreCardPosition if empty (default to front)
	if s.HomeShelves.ExploreCardPosition == "" {
		s.HomeShelves.ExploreCardPosition = ExploreCardPositionFront
	}

	// Backfill Filtering settings - no backfill needed as 0 and false are the correct defaults

	// Backfill Display settings
	if len(s.Display.BadgeVisibility) == 0 {
		s.Display.BadgeVisibility = []string{"watchProgress"}
	}
	if s.Display.WatchStateIconStyle == "" {
		s.Display.WatchStateIconStyle = "colored"
	}

	// Backfill Log settings
	if strings.TrimSpace(s.Log.File) == "" {
		s.Log.File = "cache/logs/backend.log"
	}
	if s.Log.MaxSize == 0 {
		s.Log.MaxSize = 50
	}
	if s.Log.MaxBackups == 0 {
		s.Log.MaxBackups = 3
	}
	if s.Log.MaxAge == 0 {
		s.Log.MaxAge = 7
	}

	// Backfill ScheduledTasks settings
	if s.ScheduledTasks.CheckIntervalSeconds == 0 {
		s.ScheduledTasks.CheckIntervalSeconds = 60
	}
	if s.ScheduledTasks.Tasks == nil {
		s.ScheduledTasks.Tasks = []ScheduledTask{}
	}
	// Note: Auto-creation of EPG and playlist refresh tasks is handled in
	// handlers/settings.go PutSettings() when features are enabled, not here.
	// This prevents tasks from being recreated after user manually deletes them.

	// Backfill Ranking settings
	if len(s.Ranking.Criteria) == 0 {
		s.Ranking.Criteria = DefaultRankingCriteria()
	}

	// Legacy AltMount configuration is ignored going forward.
	s.AltMount = nil

	// Migrate legacy Trakt settings to new Accounts array
	if s.Trakt.ClientID != "" && len(s.Trakt.Accounts) == 0 {
		// Generate a deterministic ID for the migrated account
		migratedAccount := TraktAccount{
			ID:                "migrated-default",
			Name:              s.Trakt.Username,
			ClientID:          s.Trakt.ClientID,
			ClientSecret:      s.Trakt.ClientSecret,
			AccessToken:       s.Trakt.AccessToken,
			RefreshToken:      s.Trakt.RefreshToken,
			ExpiresAt:         s.Trakt.ExpiresAt,
			Username:          s.Trakt.Username,
			ScrobblingEnabled: s.Trakt.ScrobblingEnabled,
		}
		if migratedAccount.Name == "" {
			migratedAccount.Name = "Default Trakt Account"
		}
		s.Trakt.Accounts = []TraktAccount{migratedAccount}
		// Clear legacy fields after migration
		s.Trakt.ClientID = ""
		s.Trakt.ClientSecret = ""
		s.Trakt.AccessToken = ""
		s.Trakt.RefreshToken = ""
		s.Trakt.ExpiresAt = 0
		s.Trakt.Username = ""
		s.Trakt.ScrobblingEnabled = false
	}

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

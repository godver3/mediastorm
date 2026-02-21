package models

// Helper functions for creating pointers (exported for use by other packages)
func FloatPtr(v float64) *float64 { return &v }
func BoolPtr(v bool) *bool        { return &v }
func StringPtr(v string) *string   { return &v }

// Helper functions for safely dereferencing pointers with defaults
func FloatVal(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func BoolVal(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// UserSettings contains per-user customizable settings.
// These override global defaults when set.
type UserSettings struct {
	Playback    PlaybackSettings     `json:"playback"`
	HomeShelves HomeShelvesSettings  `json:"homeShelves"`
	Filtering   FilterSettings       `json:"filtering"`
	LiveTV      LiveTVSettings       `json:"liveTV"`
	Display     DisplaySettings      `json:"display"`
	Network     NetworkSettings      `json:"network"`
	Ranking     *UserRankingSettings `json:"ranking,omitempty"`
}

// NetworkSettings configures network-aware backend URL switching.
// When the device is connected to the home WiFi network (matching HomeWifiSSID),
// the frontend will use HomeBackendUrl. Otherwise, it uses RemoteBackendUrl.
type NetworkSettings struct {
	HomeWifiSSID     string `json:"homeWifiSSID"`     // WiFi SSID to detect for home network
	HomeBackendUrl   string `json:"homeBackendUrl"`   // Backend URL when on home WiFi
	RemoteBackendUrl string `json:"remoteBackendUrl"` // Backend URL when on mobile/other networks
}

// DisplaySettings controls UI display preferences.
type DisplaySettings struct {
	// BadgeVisibility controls which badges appear on media cards.
	// Valid values: "watchProgress", "releaseStatus", "watchState", "unwatchedCount"
	BadgeVisibility []string `json:"badgeVisibility"`
	// WatchStateIconStyle controls the color of watch state icons.
	// "colored" (default) = green/yellow circles, "white" = all white circles
	WatchStateIconStyle string `json:"watchStateIconStyle,omitempty"`
}

// LiveTVSettings contains per-user Live TV preferences.
type LiveTVSettings struct {
	HiddenChannels     []string `json:"hiddenChannels"`     // Channel IDs that are hidden
	FavoriteChannels   []string `json:"favoriteChannels"`   // Channel IDs that are favorited
	SelectedCategories []string `json:"selectedCategories"` // Selected category filters
	// Per-profile IPTV source override (nil = use global)
	Mode           *string `json:"mode,omitempty"`
	PlaylistURL    *string `json:"playlistUrl,omitempty"`
	XtreamHost     *string `json:"xtreamHost,omitempty"`
	XtreamUsername *string `json:"xtreamUsername,omitempty"`
	XtreamPassword *string `json:"xtreamPassword,omitempty"`
	// Per-profile tuning overrides (nil = use global)
	PlaylistCacheTTLHours *int  `json:"playlistCacheTtlHours,omitempty"`
	ProbeSizeMB           *int  `json:"probeSizeMb,omitempty"`
	AnalyzeDurationSec    *int  `json:"analyzeDurationSec,omitempty"`
	LowLatency            *bool `json:"lowLatency,omitempty"`
	// Per-profile filtering overrides (nil = use global)
	Filtering *LiveTVFilterOverrides `json:"filtering,omitempty"`
	// Per-profile EPG overrides (nil = use global)
	EPG *EPGOverrides `json:"epg,omitempty"`
}

// LiveTVFilterOverrides contains per-profile channel filtering overrides.
type LiveTVFilterOverrides struct {
	EnabledCategories []string `json:"enabledCategories,omitempty"`
	MaxChannels       *int     `json:"maxChannels,omitempty"`
}

// EPGOverrides contains per-profile EPG overrides.
type EPGOverrides struct {
	Enabled              *bool   `json:"enabled,omitempty"`
	XmltvUrl             *string `json:"xmltvUrl,omitempty"`
	RefreshIntervalHours *int    `json:"refreshIntervalHours,omitempty"`
	RetentionDays        *int    `json:"retentionDays,omitempty"`
}

// ResolvedLiveSource holds the resolved IPTV source and tuning configuration
// after merging per-profile overrides with global settings.
type ResolvedLiveSource struct {
	Mode                  string
	PlaylistURL           string
	XtreamHost            string
	XtreamUsername        string
	XtreamPassword        string
	PlaylistCacheTTLHours int
	ProbeSizeMB           int
	AnalyzeDurationSec    int
	LowLatency            bool
	EnabledCategories     []string
	MaxChannels           int
	EPGEnabled            bool
	EPGXmltvUrl           string
	EPGRefreshIntervalHours int
	EPGRetentionDays      int
}

// ResolveLiveSource merges per-profile IPTV overrides with global settings.
// Profile-level pointer fields take precedence when non-nil; otherwise global values are used.
func ResolveLiveSource(profile *LiveTVSettings, global *ResolvedLiveSource) ResolvedLiveSource {
	r := *global
	if profile == nil {
		return r
	}
	if profile.Mode != nil {
		r.Mode = *profile.Mode
	}
	if profile.PlaylistURL != nil {
		r.PlaylistURL = *profile.PlaylistURL
	}
	if profile.XtreamHost != nil {
		r.XtreamHost = *profile.XtreamHost
	}
	if profile.XtreamUsername != nil {
		r.XtreamUsername = *profile.XtreamUsername
	}
	if profile.XtreamPassword != nil {
		r.XtreamPassword = *profile.XtreamPassword
	}
	if profile.PlaylistCacheTTLHours != nil {
		r.PlaylistCacheTTLHours = *profile.PlaylistCacheTTLHours
	}
	if profile.ProbeSizeMB != nil {
		r.ProbeSizeMB = *profile.ProbeSizeMB
	}
	if profile.AnalyzeDurationSec != nil {
		r.AnalyzeDurationSec = *profile.AnalyzeDurationSec
	}
	if profile.LowLatency != nil {
		r.LowLatency = *profile.LowLatency
	}
	if profile.Filtering != nil {
		if profile.Filtering.EnabledCategories != nil {
			r.EnabledCategories = profile.Filtering.EnabledCategories
		}
		if profile.Filtering.MaxChannels != nil {
			r.MaxChannels = *profile.Filtering.MaxChannels
		}
	}
	if profile.EPG != nil {
		if profile.EPG.Enabled != nil {
			r.EPGEnabled = *profile.EPG.Enabled
		}
		if profile.EPG.XmltvUrl != nil {
			r.EPGXmltvUrl = *profile.EPG.XmltvUrl
		}
		if profile.EPG.RefreshIntervalHours != nil {
			r.EPGRefreshIntervalHours = *profile.EPG.RefreshIntervalHours
		}
		if profile.EPG.RetentionDays != nil {
			r.EPGRetentionDays = *profile.EPG.RetentionDays
		}
	}
	return r
}

// PlaybackSettings controls how the client should launch resolved streams.
type PlaybackSettings struct {
	PreferredPlayer           string  `json:"preferredPlayer"`
	PreferredAudioLanguage    string  `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage string  `json:"preferredSubtitleLanguage,omitempty"`
	PreferredSubtitleMode     string  `json:"preferredSubtitleMode,omitempty"`
	UseLoadingScreen          bool    `json:"useLoadingScreen,omitempty"`
	SubtitleSize              float64 `json:"subtitleSize,omitempty"`              // Scaling factor for subtitle size (1.0 = default)
	RewindOnResumeFromPause   int     `json:"rewindOnResumeFromPause,omitempty"`   // Seconds to rewind when unpausing (default 0)
	RewindOnPlaybackStart     int     `json:"rewindOnPlaybackStart,omitempty"`     // Seconds to rewind when resuming from saved progress (default 0)
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

// HomeShelvesSettings controls which shelves appear on the home screen and their order.
type HomeShelvesSettings struct {
	Shelves []ShelfConfig `json:"shelves"`
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
// Pointer types with omitempty allow distinguishing between "not set" (nil) and "set to zero/false".
type FilterSettings struct {
	MaxSizeMovieGB                   *float64    `json:"maxSizeMovieGb,omitempty"`
	MaxSizeEpisodeGB                 *float64    `json:"maxSizeEpisodeGb,omitempty"`
	MaxResolution                    string      `json:"maxResolution,omitempty"`          // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	HDRDVPolicy                      HDRDVPolicy `json:"hdrDvPolicy,omitempty"`            // HDR/DV inclusion policy: "none" (no exclusion), "hdr" (include HDR + DV 7/8), "hdr_dv" (include all HDR/DV)
	PrioritizeHdr                    *bool       `json:"prioritizeHdr,omitempty"`          // Prioritize HDR/DV content in search results
	FilterOutTerms                   []string    `json:"filterOutTerms,omitempty"`         // Terms to filter out from results (case-insensitive match in title)
	PreferredTerms                   []string    `json:"preferredTerms,omitempty"`         // Terms to prioritize in results (case-insensitive match in title)
	NonPreferredTerms                []string    `json:"nonPreferredTerms,omitempty"`      // Terms to derank in results (case-insensitive match in title, ranked lower but not removed)
	BypassFilteringForAIOStreamsOnly *bool       `json:"bypassFilteringForAioStreamsOnly,omitempty"` // Skip strmr filtering/ranking when AIOStreams is the only enabled scraper
}

// DefaultUserSettings returns the default settings for a new user.
func DefaultUserSettings() UserSettings {
	return UserSettings{
		Playback: PlaybackSettings{
			PreferredPlayer:  "native",
			UseLoadingScreen: false,
			SubtitleSize:     1.0,
		},
		HomeShelves: HomeShelvesSettings{
			Shelves: []ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
				{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 2},
				{ID: "trending-tv", Name: "Trending TV Shows", Enabled: true, Order: 3},
			},
		},
		Filtering: FilterSettings{
			MaxSizeMovieGB:   FloatPtr(0),
			MaxSizeEpisodeGB: FloatPtr(0),
			HDRDVPolicy:      HDRDVPolicyNoExclusion,
			PrioritizeHdr:    BoolPtr(true),
		},
		LiveTV: LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
		Display: DisplaySettings{
			BadgeVisibility:     []string{"watchProgress"},
			WatchStateIconStyle: "colored",
		},
		Network: NetworkSettings{
			HomeWifiSSID:     "",
			HomeBackendUrl:   "",
			RemoteBackendUrl: "",
		},
	}
}

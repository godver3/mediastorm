package models

import (
	"encoding/json"
	"novastream/config"
	"strings"
)

// Helper functions for creating pointers (exported for use by other packages)
func FloatPtr(v float64) *float64 { return &v }
func BoolPtr(v bool) *bool        { return &v }
func StringPtr(v string) *string  { return &v }
func IntPtr(v int) *int           { return &v }
func StringSlicePtr(v []string) *[]string {
	copy := append([]string(nil), v...)
	if copy == nil {
		copy = []string{}
	}
	return &copy
}
func Int64Ptr(v int64) *int64 { return &v }

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

func IntVal(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func StringVal(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// UserSettings contains per-user customizable settings.
// These override global defaults when set.
type UserSettings struct {
	Recommendations *config.RecommendationSettings `json:"recommendations,omitempty"`
	// Server-owned opaque snapshot: only the dedicated sports endpoint may update it.
	SportsPreferences json.RawMessage        `json:"sportsPreferences,omitempty"`
	Playback          PlaybackSettings       `json:"playback"`
	Metadata          MetadataSettings       `json:"metadata"`
	HomeShelves       HomeShelvesSettings    `json:"homeShelves"`
	Filtering         FilterSettings         `json:"filtering"`
	AnimeFiltering    AnimeFilteringSettings `json:"animeFiltering"`
	LiveTV            LiveTVSettings         `json:"liveTV"`
	Display           DisplaySettings        `json:"display"`
	Network           NetworkSettings        `json:"network"`
	Ranking           *UserRankingSettings   `json:"ranking,omitempty"`
	Calendar          CalendarSettings       `json:"calendar"`
	Recordings        RecordingSettings      `json:"recordings,omitempty"`
}

// MetadataSettings contains per-profile metadata presentation preferences.
type MetadataSettings struct {
	PrimaryLanguage string `json:"primaryLanguage,omitempty"`
}

// CalendarSettings controls which content sources populate the calendar.
// All sources are enabled by default.
type CalendarSettings struct {
	Watchlist      *bool           `json:"watchlist,omitempty"`      // Include series episodes & movie releases from watchlist
	History        *bool           `json:"history,omitempty"`        // Include upcoming episodes for series being watched
	Trending       *bool           `json:"trending,omitempty"`       // Include the non-top-20 remainder of the trending lists
	TopTrending    *bool           `json:"topTrending,omitempty"`    // Include the top 20 entries from the trending lists
	MDBLists       *bool           `json:"mdblists,omitempty"`       // Include content from enabled MDBList shelves
	MDBListShelves map[string]bool `json:"mdblistShelves,omitempty"` // Per-shelf calendar enable: shelf ID -> enabled (nil = all enabled)
}

// MDBListShelfEnabled returns whether a specific MDBList shelf is enabled for the calendar.
// If MDBListShelves is nil (unset), all shelves are enabled by default.
func (c CalendarSettings) MDBListShelfEnabled(shelfID string) bool {
	if c.MDBListShelves == nil {
		return true
	}
	enabled, ok := c.MDBListShelves[shelfID]
	if !ok {
		return true // shelves not in the map default to enabled
	}
	return enabled
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
	// NavigationTabVisibility controls which navigation tabs are shown in the client UI.
	// Valid values: "home", "watchlist", "search", "lists", "live", "sports", "profiles", "downloads", "settings", "admin"
	NavigationTabVisibility []string `json:"navigationTabVisibility,omitempty"`
	// NavigationTabVisibilityIncludesSystemTabs marks the one-time migration that added settings/admin to existing visibility lists.
	NavigationTabVisibilityIncludesSystemTabs bool `json:"navigationTabVisibilityIncludesSystemTabs,omitempty"`
	// NavigationTabVisibilityIncludesWatchlist marks the one-time migration that added Watchlist to existing visibility lists.
	NavigationTabVisibilityIncludesWatchlist bool `json:"navigationTabVisibilityIncludesWatchlist,omitempty"`
	NavigationTabVisibilityIncludesSports    bool `json:"navigationTabVisibilityIncludesSports,omitempty"`
	// WatchStateIconStyle controls the color of watch state icons.
	// "colored" (default) = green/yellow circles, "white" = all white circles
	WatchStateIconStyle string `json:"watchStateIconStyle,omitempty"`
	// IncludeUnreleasedMoviesInLists keeps unreleased/upcoming movies in list-style shelves and list APIs.
	IncludeUnreleasedMoviesInLists *bool `json:"includeUnreleasedMoviesInLists,omitempty"`
	// IncludeUnreleasedShowsInLists keeps unreleased/upcoming shows in list-style shelves and list APIs.
	IncludeUnreleasedShowsInLists *bool `json:"includeUnreleasedShowsInLists,omitempty"`
	// IncludeUnreleasedMoviesInSearch keeps unreleased/upcoming movies in metadata search results.
	IncludeUnreleasedMoviesInSearch *bool `json:"includeUnreleasedMoviesInSearch,omitempty"`
	// IncludeUnreleasedShowsInSearch keeps unreleased/upcoming shows in metadata search results.
	IncludeUnreleasedShowsInSearch *bool `json:"includeUnreleasedShowsInSearch,omitempty"`
	// BypassFilteringForAIOStreamsOnly skips mediastorm filtering/ranking when AIOStreams is the only enabled scraper.
	BypassFilteringForAIOStreamsOnly *bool `json:"bypassFilteringForAioStreamsOnly,omitempty"`
	// ShowStreamSourceInfo displays stream service and debrid provider information in selection and playback UI.
	ShowStreamSourceInfo *bool `json:"showStreamSourceInfo,omitempty"`
	// DisableMobileTopCarousel hides the top hero carousel on mobile home.
	DisableMobileTopCarousel *bool `json:"disableMobileTopCarousel,omitempty"`
	// HideContinueWatchingHeroMetadata hides year and overview text from the TV home hero for Continue Watching.
	HideContinueWatchingHeroMetadata *bool `json:"hideContinueWatchingHeroMetadata,omitempty"`
	// MoveDetailsRatingsToMetadata moves TV Details ratings from beneath the poster to the title metadata area.
	MoveDetailsRatingsToMetadata *bool `json:"moveDetailsRatingsToMetadata,omitempty"`
	// HideDetailsPoster hides the poster on the TV Details page.
	HideDetailsPoster *bool `json:"hideDetailsPoster,omitempty"`
	// HideTVDrawerRail fully hides the collapsed TV navigation drawer instead of leaving its icon rail visible.
	HideTVDrawerRail *bool `json:"hideTvDrawerRail,omitempty"`
	// SimpleMode reduces the frontend to essential browsing and playback choices.
	SimpleMode *bool `json:"simpleMode,omitempty"`
	// SimpleModeHomeShelves is the ordered list of configured home shelf IDs shown in M.O.M. Mode™.
	SimpleModeHomeShelves *[]string `json:"simpleModeHomeShelves,omitempty"`
	// DisableTVHomeCardDimming keeps trailing home shelf cards fully visible on TV platforms.
	DisableTVHomeCardDimming *bool `json:"disableTvHomeCardDimming,omitempty"`
	// EnableAnimations controls application UI motion such as animated scrolling and transitions.
	EnableAnimations *bool `json:"enableAnimations,omitempty"`
	// EnableHeroArtPanning animates TV hero artwork with a slow pan/zoom effect.
	EnableHeroArtPanning *bool `json:"enableHeroArtPanning,omitempty"`
	// EnableHeroArtRotation cycles through alternate TV hero artwork.
	EnableHeroArtRotation *bool `json:"enableHeroArtRotation,omitempty"`
	// ShowSeriesBackdropForMissingEpisodeArt uses show landscape art when an episode thumbnail is unavailable.
	ShowSeriesBackdropForMissingEpisodeArt *bool `json:"showSeriesBackdropForMissingEpisodeArt,omitempty"`
	// BlurUnwatchedEpisodeThumbnails blurs Details-page thumbnails for unwatched episodes.
	BlurUnwatchedEpisodeThumbnails *bool `json:"blurUnwatchedEpisodeThumbnails,omitempty"`
	// BlurUnwatchedEpisodeThumbnailsIncludeCurrent applies thumbnail blurring to the selected/current episode too.
	BlurUnwatchedEpisodeThumbnailsIncludeCurrent *bool `json:"blurUnwatchedEpisodeThumbnailsIncludeCurrent,omitempty"`
	// BlurUnwatchedEpisodeOverviews blurs Details-page overview text for unwatched episodes.
	BlurUnwatchedEpisodeOverviews *bool `json:"blurUnwatchedEpisodeOverviews,omitempty"`
	// BlurUnwatchedEpisodeOverviewsIncludeCurrent applies overview blurring to the selected/current episode too.
	BlurUnwatchedEpisodeOverviewsIncludeCurrent *bool `json:"blurUnwatchedEpisodeOverviewsIncludeCurrent,omitempty"`
	// AppLanguage overrides the app UI language (ISO 639-1 code, e.g. "en", "fr"). Empty = use device locale.
	AppLanguage string `json:"appLanguage,omitempty"`
	// Appearance controls app-wide visual accessibility and theming preferences.
	Appearance AppearanceSettings `json:"appearance,omitempty"`
}

// AddMissingSystemNavigationTabs appends tabs that became configurable after
// the original navigation visibility setting shipped. It only changes non-empty
// lists; empty lists keep their existing "use defaults" behavior.
func AddMissingSystemNavigationTabs(tabs []string) ([]string, bool) {
	if len(tabs) == 0 {
		return tabs, false
	}

	existing := make(map[string]struct{}, len(tabs))
	for _, tab := range tabs {
		existing[tab] = struct{}{}
	}

	changed := false
	for _, tab := range []string{"settings", "admin"} {
		if _, ok := existing[tab]; !ok {
			tabs = append(tabs, tab)
			changed = true
		}
	}

	return tabs, changed
}

// AddMissingWatchlistNavigationTab enables the newly introduced Watchlist item
// once for existing explicit visibility lists; its migration marker preserves
// later user choices to hide it.
func AddMissingWatchlistNavigationTab(tabs []string) ([]string, bool) {
	if len(tabs) == 0 {
		return tabs, false
	}
	for _, tab := range tabs {
		if tab == "watchlist" {
			return tabs, false
		}
	}
	return append(tabs, "watchlist"), true
}

// AddMissingSportsNavigationTab enables Sports once without overriding later visibility choices.
func AddMissingSportsNavigationTab(tabs []string) ([]string, bool) {
	if len(tabs) == 0 {
		return tabs, false
	}
	for _, tab := range tabs {
		if tab == "sports" {
			return tabs, false
		}
	}
	return append(tabs, "sports"), true
}

// AppearanceSettings controls app-wide visual accessibility and theming preferences.
type AppearanceSettings struct {
	FontScale            *float64 `json:"fontScale,omitempty"`            // App text scale multiplier (1.0 = default)
	AccentColor          string   `json:"accentColor,omitempty"`          // Primary accent color as #RRGGBB
	TextColor            string   `json:"textColor,omitempty"`            // Primary text color as #RRGGBB
	SecondaryTextColor   string   `json:"secondaryTextColor,omitempty"`   // Secondary text color as #RRGGBB
	BackgroundColor      string   `json:"backgroundColor,omitempty"`      // Page background color as #RRGGBB
	ModalBackgroundColor string   `json:"modalBackgroundColor,omitempty"` // Modal background color as #RRGGBB
	ButtonStyle          string   `json:"buttonStyle,omitempty"`          // "soft", "outlined", or "filled"
	ButtonRadius         string   `json:"buttonRadius,omitempty"`         // "square", "rounded", or "pill"
	HighContrast         *bool    `json:"highContrast,omitempty"`         // Strengthen text/borders/background contrast
	ReduceOverlays       *bool    `json:"reduceOverlays,omitempty"`       // Prefer flatter, less translucent surfaces
}

// LiveTVSettings contains per-user Live TV preferences.
type LiveTVSettings struct {
	HiddenChannels     []string `json:"hiddenChannels"`     // Channel IDs that are hidden
	FavoriteChannels   []string `json:"favoriteChannels"`   // Channel IDs that are favorited
	SelectedCategories []string `json:"selectedCategories"` // Selected category filters
	// Per-profile IPTV source override (nil = use global)
	Mode                    *string              `json:"mode,omitempty"`
	PlaylistURL             *string              `json:"playlistUrl,omitempty"`
	HDHomeRunHost           *string              `json:"hdhomerunHost,omitempty"`
	HDHomeRunGuideEmail     *string              `json:"hdhomerunGuideEmail,omitempty"`
	HDHomeRunGuideDeviceIDs *string              `json:"hdhomerunGuideDeviceIds,omitempty"`
	ManifestURL             *string              `json:"manifestUrl,omitempty"`
	ProxyURL                *string              `json:"proxyUrl,omitempty"`
	Sources                 []LivePlaylistSource `json:"sources,omitempty"`
	PlaylistSources         []LivePlaylistSource `json:"playlistSources,omitempty"`
	SourcesOverride         *bool                `json:"sourcesOverride,omitempty"`
	XtreamHost              *string              `json:"xtreamHost,omitempty"`
	XtreamUsername          *string              `json:"xtreamUsername,omitempty"`
	XtreamPassword          *string              `json:"xtreamPassword,omitempty"`
	StalkerPortalURL        *string              `json:"stalkerPortalUrl,omitempty"`
	StalkerMAC              *string              `json:"stalkerMac,omitempty"`
	StalkerSerialNumber     *string              `json:"stalkerSerialNumber,omitempty"`
	StalkerDeviceID         *string              `json:"stalkerDeviceId,omitempty"`
	StalkerDeviceID2        *string              `json:"stalkerDeviceId2,omitempty"`
	StalkerSignature        *string              `json:"stalkerSignature,omitempty"`
	StalkerModel            *string              `json:"stalkerModel,omitempty"`
	MaxStreams              *int                 `json:"maxStreams,omitempty"`
	// Per-profile tuning overrides (nil = use global)
	PlaylistCacheTTLHours *int    `json:"playlistCacheTtlHours,omitempty"`
	ProbeSizeMB           *int    `json:"probeSizeMb,omitempty"`
	AnalyzeDurationSec    *int    `json:"analyzeDurationSec,omitempty"`
	LowLatency            *bool   `json:"lowLatency,omitempty"`
	StreamFormat          *string `json:"streamFormat,omitempty"`
	// Per-profile filtering overrides (nil = use global)
	Filtering *LiveTVFilterOverrides `json:"filtering,omitempty"`
	// Per-profile EPG overrides (nil = use global)
	EPG *EPGOverrides `json:"epg,omitempty"`
}

// LivePlaylistSource represents a named M3U playlist source.
type LivePlaylistSource struct {
	ID                      string                 `json:"id"`
	Name                    string                 `json:"name"`
	Mode                    string                 `json:"mode,omitempty"`
	PlaylistURL             string                 `json:"playlistUrl"`
	HDHomeRunHost           string                 `json:"hdhomerunHost,omitempty"`
	HDHomeRunGuideEmail     string                 `json:"hdhomerunGuideEmail,omitempty"`
	HDHomeRunGuideDeviceIDs string                 `json:"hdhomerunGuideDeviceIds,omitempty"`
	ManifestURL             string                 `json:"manifestUrl,omitempty"` // Stremio addon manifest URL (used when mode is "stremio")
	ProxyURL                string                 `json:"proxyUrl,omitempty"`
	XtreamHost              string                 `json:"xtreamHost,omitempty"`
	XtreamUsername          string                 `json:"xtreamUsername,omitempty"`
	XtreamPassword          string                 `json:"xtreamPassword,omitempty"`
	StalkerPortalURL        string                 `json:"stalkerPortalUrl,omitempty"`
	StalkerMAC              string                 `json:"stalkerMac,omitempty"`
	StalkerSerialNumber     string                 `json:"stalkerSerialNumber,omitempty"`
	StalkerDeviceID         string                 `json:"stalkerDeviceId,omitempty"`
	StalkerDeviceID2        string                 `json:"stalkerDeviceId2,omitempty"`
	StalkerSignature        string                 `json:"stalkerSignature,omitempty"`
	StalkerModel            string                 `json:"stalkerModel,omitempty"`
	MaxStreams              int                    `json:"maxStreams,omitempty"`
	PlaylistCacheTTLHours   int                    `json:"playlistCacheTtlHours,omitempty"`
	ProbeSizeMB             int                    `json:"probeSizeMb,omitempty"`
	AnalyzeDurationSec      int                    `json:"analyzeDurationSec,omitempty"`
	LowLatency              *bool                  `json:"lowLatency,omitempty"`
	StreamFormat            string                 `json:"streamFormat,omitempty"`
	Filtering               *LiveTVFilterOverrides `json:"filtering,omitempty"`
	EnabledCategories       []string               `json:"enabledCategories,omitempty"`
	MaxChannels             *int                   `json:"maxChannels,omitempty"`
	EPG                     *LivePlaylistEPGSource `json:"epg,omitempty"`
	Enabled                 *bool                  `json:"enabled,omitempty"`
}

// LivePlaylistEPGSource contains per-source EPG overrides.
type LivePlaylistEPGSource struct {
	Enabled              *bool   `json:"enabled,omitempty"`
	XmltvUrl             *string `json:"xmltvUrl,omitempty"`
	RefreshIntervalHours *int    `json:"refreshIntervalHours,omitempty"`
	RetentionDays        *int    `json:"retentionDays,omitempty"`
	TimeOffsetMinutes    *int    `json:"timeOffsetMinutes,omitempty"`
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
	TimeOffsetMinutes    *int    `json:"timeOffsetMinutes,omitempty"`
}

// ResolvedLiveSource holds the resolved IPTV source and tuning configuration
// after merging per-profile overrides with global settings.
type ResolvedLiveSource struct {
	Mode                    string
	PlaylistURL             string
	HDHomeRunHost           string
	ManifestURL             string
	ProxyURL                string
	Sources                 []LivePlaylistSource
	PlaylistSources         []LivePlaylistSource
	XtreamHost              string
	XtreamUsername          string
	XtreamPassword          string
	StalkerPortalURL        string
	StalkerMAC              string
	StalkerSerialNumber     string
	StalkerDeviceID         string
	StalkerDeviceID2        string
	StalkerSignature        string
	StalkerModel            string
	MaxStreams              int
	PlaylistCacheTTLHours   int
	ProbeSizeMB             int
	AnalyzeDurationSec      int
	LowLatency              bool
	StreamFormat            string
	EnabledCategories       []string
	MaxChannels             int
	EPGEnabled              bool
	EPGXmltvUrl             string
	EPGRefreshIntervalHours int
	EPGRetentionDays        int
	EPGTimeOffsetMinutes    int
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
	if profile.HDHomeRunHost != nil {
		r.HDHomeRunHost = *profile.HDHomeRunHost
	}
	if profile.ManifestURL != nil {
		r.ManifestURL = *profile.ManifestURL
	}
	if profile.ProxyURL != nil {
		r.ProxyURL = *profile.ProxyURL
	}
	if profile.SourcesOverride != nil && *profile.SourcesOverride {
		r.Sources = append([]LivePlaylistSource(nil), profile.Sources...)
		r.PlaylistSources = append([]LivePlaylistSource(nil), profile.PlaylistSources...)
	} else if len(profile.Sources) > 0 {
		r.Sources = append([]LivePlaylistSource(nil), profile.Sources...)
	} else if len(profile.PlaylistSources) > 0 {
		r.PlaylistSources = append([]LivePlaylistSource(nil), profile.PlaylistSources...)
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
	if profile.StalkerPortalURL != nil {
		r.StalkerPortalURL = *profile.StalkerPortalURL
	}
	if profile.StalkerMAC != nil {
		r.StalkerMAC = *profile.StalkerMAC
	}
	if profile.StalkerSerialNumber != nil {
		r.StalkerSerialNumber = *profile.StalkerSerialNumber
	}
	if profile.StalkerDeviceID != nil {
		r.StalkerDeviceID = *profile.StalkerDeviceID
	}
	if profile.StalkerDeviceID2 != nil {
		r.StalkerDeviceID2 = *profile.StalkerDeviceID2
	}
	if profile.StalkerSignature != nil {
		r.StalkerSignature = *profile.StalkerSignature
	}
	if profile.StalkerModel != nil {
		r.StalkerModel = *profile.StalkerModel
	}
	if profile.MaxStreams != nil {
		r.MaxStreams = *profile.MaxStreams
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
	if profile.StreamFormat != nil {
		r.StreamFormat = *profile.StreamFormat
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
		if profile.EPG.TimeOffsetMinutes != nil {
			r.EPGTimeOffsetMinutes = *profile.EPG.TimeOffsetMinutes
		}
	}
	return r
}

// PlaybackSettings controls how the client should launch resolved streams.
type PlaybackSettings struct {
	PreferredPlayer               string    `json:"preferredPlayer"`
	PreferredAudioLanguage        string    `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage     string    `json:"preferredSubtitleLanguage,omitempty"`
	AllowedTrackLanguages         *[]string `json:"allowedTrackLanguages,omitempty"`
	PreferredSubtitleMode         string    `json:"preferredSubtitleMode,omitempty"`
	PauseWhenAppInactive          *bool     `json:"pauseWhenAppInactive,omitempty"`
	UseLoadingScreen              *bool     `json:"useLoadingScreen,omitempty"`
	SubtitleSize                  float64   `json:"subtitleSize,omitempty"`                        // Scaling factor for subtitle size (1.0 = default)
	SubtitleUseCropDetectPosition *bool     `json:"subtitleUseCropDetectPosition,omitempty"`       // Use detected video letterbox bars for subtitle placement
	SubtitleColor                 string    `json:"subtitleColor,omitempty"`                       // Text color as #RRGGBB
	SubtitleOpacity               *float64  `json:"subtitleOpacity,omitempty"`                     // Text opacity (0.0-1.0)
	SubtitleFont                  *string   `json:"subtitleFont,omitempty"`                        // SRT/VTT subtitle font family
	SubtitleBold                  *bool     `json:"subtitleBold,omitempty"`                        // Render SRT/VTT subtitle text in bold
	SubtitleOutlineEnabled        *bool     `json:"subtitleOutlineEnabled,omitempty"`              // Show text outline around subtitles
	SubtitleOutlineColor          string    `json:"subtitleOutlineColor,omitempty"`                // Outline color as #RRGGBB
	SubtitleOutlineWeight         *float64  `json:"subtitleOutlineWeight,omitempty"`               // Outline weight (0.0-1.0)
	SubtitleBackgroundEnabled     *bool     `json:"subtitleBackgroundEnabled,omitempty"`           // Show subtitle background box
	SubtitleBackgroundColor       string    `json:"subtitleBackgroundColor,omitempty"`             // Background color as #RRGGBB
	SubtitleBackgroundOpacity     *float64  `json:"subtitleBackgroundOpacity,omitempty"`           // Background opacity (0.0-1.0)
	SeekForwardSeconds            int       `json:"seekForwardSeconds,omitempty"`                  // Seconds to skip forward (default 30)
	SeekBackwardSeconds           int       `json:"seekBackwardSeconds,omitempty"`                 // Seconds to skip backward (default 10)
	ForceAACTranscoding           *bool     `json:"forceAacTranscoding,omitempty"`                 // Force AC3/EAC3/DTS audio to AAC
	AutoPlayTrailersTV            *bool     `json:"autoPlayTrailersTV,omitempty"`                  // Auto-play trailers on TV details pages
	RewindOnResumeFromPause       *int      `json:"rewindOnResumeFromPause,omitempty"`             // Seconds to rewind when unpausing (default 0)
	RewindOnPlaybackStart         *int      `json:"rewindOnPlaybackStart,omitempty"`               // Seconds to rewind when resuming from saved progress (default 0)
	DisablePrequeue               *bool     `json:"disablePrequeue,omitempty"`                     // Disable automatic stream pre-loading
	PrerollMode                   string    `json:"prerollMode,omitempty"`                         // Empty inherits; disabled, artwork, default, or custom override
	PrerollAssetID                string    `json:"prerollAssetId,omitempty"`                      // Content hash used when prerollMode is custom
	PrerollMediaScope             string    `json:"prerollMediaScope,omitempty"`                   // Empty inherits; all, movies, or tv
	PrerollSkipIfPrequeueReady    *bool     `json:"prerollSkipIfPrequeueReady,omitempty"`          // Skip pre-roll when a prepared stream is already ready
	StreamMigrationEnabled        *bool     `json:"streamMigrationEnabled,omitempty"`              // Switch to the next ranked stream when native playback cannot sustain the current stream
	IgnoreDVCompatibilityCheck    *bool     `json:"ignoreDolbyVisionCompatibilityCheck,omitempty"` // Skip Android display DV capability check before playback
	CreditsDetectionEnabled       *bool     `json:"creditsDetectionEnabled,omitempty"`             // Enable on-device credits detection/OCR during playback
	CreditsAutoSkip               *bool     `json:"creditsAutoSkip,omitempty"`                     // Automatically play the next episode after credits are detected
	ScrobbleStartDelaySeconds     *int      `json:"scrobbleStartDelaySeconds,omitempty"`           // Active playback time before real-time scrobble events begin
	CreditsDetection              bool      `json:"creditsDetection,omitempty"`                    // Legacy name for creditsAutoSkip
	MatchFrameRate                *bool     `json:"matchFrameRate,omitempty"`                      // Request TV display refresh rate matching during playback
	LiveClosedCaptionExtraction   *bool     `json:"liveClosedCaptionExtraction,omitempty"`         // Extract EIA-608 closed captions from live TV (server-side)
	MaxConcurrentStreams          *int      `json:"maxConcurrentStreams,omitempty"`                // Per-profile concurrent stream limit (nil = use account limit)
	MaxResultsPerResolution       *int      `json:"maxResultsPerResolution,omitempty"`             // Maximum number of results per resolution tier (0 = no limit)
}

// ShelfConfig represents a configurable home screen shelf.
type ShelfConfig struct {
	ID                     string                 `json:"id"`                          // Unique identifier (e.g., "continue-watching", "watchlist", "trending-movies")
	Name                   string                 `json:"name"`                        // Display name
	Enabled                bool                   `json:"enabled"`                     // Whether the shelf is visible
	Order                  int                    `json:"order"`                       // Sort order (lower numbers appear first)
	Type                   string                 `json:"type,omitempty"`              // "builtin" (default), "mdblist", "stremio", "tmdb", "trakt", "simkl", "letterboxd", "genre", "decade", "collection-hub", or "library"
	LibraryID              string                 `json:"libraryId,omitempty"`         // Configured media library selected by a "library" shelf
	ListURL                string                 `json:"listUrl,omitempty"`           // MDBList URL for custom lists (e.g., https://mdblist.com/lists/username/list-name/json)
	AddonManifestURL       string                 `json:"addonManifestUrl,omitempty"`  // Stremio add-on manifest URL selected by a "stremio" shelf
	AddonCatalogType       string                 `json:"addonCatalogType,omitempty"`  // Stremio catalog media type ("movie" or "series")
	AddonCatalogID         string                 `json:"addonCatalogId,omitempty"`    // Stremio catalog ID from the add-on manifest
	AddonName              string                 `json:"addonName,omitempty"`         // Stremio add-on name captured during manifest ingestion
	TMDBSourceType         string                 `json:"tmdbSourceType,omitempty"`    // TMDB source builder type
	TMDBSourceID           string                 `json:"tmdbSourceId,omitempty"`      // Numeric TMDB list/company/network/collection/person ID
	TMDBSourceName         string                 `json:"tmdbSourceName,omitempty"`    // Resolved source name shown by the shelf editor
	TMDBMediaType          string                 `json:"tmdbMediaType,omitempty"`     // "movie", "tv", or "all"
	TMDBDiscoverQuery      string                 `json:"tmdbDiscoverQuery,omitempty"` // URL-encoded custom filters shared by every TMDB source type
	StreamingServices      []StreamingServiceLink `json:"streamingServices,omitempty"` // Service cards for the built-in Streaming Services shelf
	CollectionItems        []CollectionHubLink    `json:"collectionItems,omitempty"`   // Shelf cards for collection hub shelves
	TraktAccountID         string                 `json:"traktAccountId,omitempty"`    // Trakt account ID, or "__all__" for master-account global watchlists
	TraktListType          string                 `json:"traktListType,omitempty"`     // "watchlist" or "custom"
	TraktListID            string                 `json:"traktListId,omitempty"`       // Trakt custom list slug/ID when traktListType == "custom"
	PublicMetaDBAccountID  string                 `json:"publicMetaDBAccountId,omitempty"`
	PublicMetaDBListID     string                 `json:"publicMetaDBListId,omitempty"`
	SimklAccountID         string                 `json:"simklAccountId,omitempty"`         // Simkl account ID
	SimklListType          string                 `json:"simklListType,omitempty"`          // Simkl status bucket: "plantowatch", "watching", "completed", "hold", or "dropped"
	SimklMediaType         string                 `json:"simklMediaType,omitempty"`         // Simkl media bucket: "movies", "shows", or "anime"
	LetterboxdListID       string                 `json:"letterboxdListId,omitempty"`       // MDBList external-list ID for an imported Letterboxd list
	LetterboxdListURL      string                 `json:"letterboxdListUrl,omitempty"`      // Public Letterboxd list URL
	Limit                  int                    `json:"limit,omitempty"`                  // Optional limit on number of items returned (0 = no limit)
	ActivityWindowDays     int                    `json:"activityWindowDays,omitempty"`     // Shared-activity lookback window for backend activity shelves
	MinimumProfiles        int                    `json:"minimumProfiles,omitempty"`        // Minimum completed media-item views required by Popular on This Server
	MaxItemsPerProfile     int                    `json:"maxItemsPerProfile,omitempty"`     // Per-profile contribution cap for Recently Watched
	HideUnreleased         bool                   `json:"hideUnreleased,omitempty"`         // Filter out unreleased/in-theaters content
	Sort                   string                 `json:"sort,omitempty"`                   // Optional shelf-specific sort mode
	CalendarSources        CalendarSettings       `json:"calendarSources,omitempty"`        // Optional source filter for calendar-backed shelves
	AnimateLogoOnlyOnFocus bool                   `json:"animateLogoOnlyOnFocus,omitempty"` // For collection hubs, animate GIF logos only when focused on TV
	ShowCollectionTitles   bool                   `json:"showCollectionTitles,omitempty"`   // For collection hubs, show collection card titles
	ShowCollectionCounts   bool                   `json:"showCollectionCounts,omitempty"`   // For collection hubs, show collection item counts
}

// CollectionHubLink represents a configurable card in a collection hub shelf.
type CollectionHubLink struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Enabled       bool    `json:"enabled"`
	Order         int     `json:"order"`
	SourceShelfID string  `json:"sourceShelfId"`
	LogoURL       string  `json:"logoUrl,omitempty"`
	HeroArtURL    string  `json:"heroArtUrl,omitempty"`
	LogoScale     float64 `json:"logoScale,omitempty"`
	TintColor     string  `json:"tintColor,omitempty"`
}

// StreamingServiceLink represents a configurable streaming service card on the home screen.
type StreamingServiceLink struct {
	ID        string                     `json:"id"`
	Name      string                     `json:"name"`
	Enabled   bool                       `json:"enabled"`
	Order     int                        `json:"order"`
	LogoURL   string                     `json:"logoUrl"`
	LogoScale float64                    `json:"logoScale,omitempty"`
	TintColor string                     `json:"tintColor,omitempty"`
	Lists     []StreamingServiceListLink `json:"lists"`
}

// StreamingServiceListLink represents a source list behind a streaming service card.
type StreamingServiceListLink struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// HomeShelvesSettings controls which shelves appear on the home screen and their order.
type HomeShelvesSettings struct {
	Views                           map[string]config.HomeViewSettings `json:"views,omitempty"`
	Shelves                         []ShelfConfig                      `json:"shelves"`
	ExploreCardPosition             string                             `json:"exploreCardPosition,omitempty"`             // "front" or "end" (default)
	ItemCap                         int                                `json:"itemCap,omitempty"`                         // Max items shown per home shelf before Explore card (default 20)
	ExcludeUpcomingFromContinue     *bool                              `json:"excludeUpcomingFromContinue,omitempty"`     // Move unreleased next-up episodes out of Continue Watching
	HomeShelfFocusModel             string                             `json:"homeShelfFocusModel,omitempty"`             // "left" (default), "center", or "right"
	MobileTopShelfMode              string                             `json:"mobileTopShelfMode,omitempty"`              // "default", "disabled", or "shelf"
	MobileTopShelfSourceID          string                             `json:"mobileTopShelfSourceId,omitempty"`          // Shelf ID used when mobileTopShelfMode is "shelf"
	TVTopShelfMode                  string                             `json:"tvTopShelfMode,omitempty"`                  // "default", "disabled", or "shelf"
	TVTopShelfSourceID              string                             `json:"tvTopShelfSourceId,omitempty"`              // Shelf ID used when tvTopShelfMode is "shelf"
	DisableTvLandscapeCardExpansion *bool                              `json:"disableTvLandscapeCardExpansion,omitempty"` // Keep TV shelf cards in portrait when focused
	HomeShelfScale                  *float64                           `json:"homeShelfScale,omitempty"`                  // TV home shelf/card scale, 0.5-1.0 (default 1.0)
	HomeHeroScale                   *float64                           `json:"homeHeroScale,omitempty"`                   // TV upper hero/art scale, 0.5-1.0 (default 1.0)
}

// DefaultSimpleModeHomeShelfIDs is the built-in M.O.M. Mode™ home shelf order.
func DefaultSimpleModeHomeShelfIDs() []string {
	return []string{"top-ten", "continue-watching", "watch-something", "trending-movies", "trending-tv"}
}

// DefaultHomeShelfConfigs returns the built-in home shelves in their default order.
func DefaultHomeShelfConfigs() []ShelfConfig {
	return []ShelfConfig{
		{ID: "top-ten", Name: "Top 10 Today", Enabled: true, Order: 0},
		{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 1},
		{ID: "watch-something", Name: "Watch Something", Enabled: true, Order: 2},
		{ID: "tonight", Name: "Tonight", Enabled: false, Order: 3},
		{ID: "my-recommended", Name: "My Recommended", Enabled: true, Order: 4},
		{ID: "my-upcoming", Name: "My Upcoming", Enabled: true, Order: 5, Sort: "air-date-asc"},
		{ID: "calendar", Name: "Coming Up", Enabled: true, Order: 6},
		{ID: "my-recently-aired", Name: "My Recently Aired", Enabled: true, Order: 7, CalendarSources: CalendarSettings{Watchlist: BoolPtr(true), History: BoolPtr(false), Trending: BoolPtr(false), TopTrending: BoolPtr(false), MDBLists: BoolPtr(false)}},
		{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 8},
		{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 9},
		{ID: "trending-tv", Name: "Trending TV Shows", Enabled: true, Order: 10},
		{ID: "streaming-services", Name: "Streaming Services", Enabled: true, Order: 11},
		{ID: "live-favorites", Name: "Favorite Channels", Enabled: false, Order: 12},
		{ID: "popular-on-server", Name: "Popular on This Server", Enabled: false, Order: 13, Limit: 20, ActivityWindowDays: 90, MinimumProfiles: 2},
		{ID: "recently-watched", Name: "Recently Watched", Enabled: false, Order: 14, Limit: 20, ActivityWindowDays: 14, MaxItemsPerProfile: 3},
		{ID: "dashboard", Name: "Dashboard", Enabled: false, Order: 15},
		{ID: "permanent-prequeue", Name: "Permanent Prequeue", Enabled: false, Order: 16},
		{ID: "set-aside", Name: "Set Aside", Enabled: false, Order: 17},
	}
}

// EnsureDefaultHomeShelves adds any missing built-in shelves while preserving existing custom shelves and ordering.
func EnsureDefaultHomeShelves(shelves []ShelfConfig) ([]ShelfConfig, bool) {
	if len(shelves) == 0 {
		return DefaultHomeShelfConfigs(), true
	}

	nextShelves := append([]ShelfConfig(nil), shelves...)
	changed := false

	hasShelf := func(id string) bool {
		for _, shelf := range nextShelves {
			if shelf.ID == id {
				return true
			}
		}
		return false
	}

	// Tonight is experimental. Disable any materialized copy during startup/profile
	// migration until it is ready to be exposed as a supported setting.
	for i := range nextShelves {
		if nextShelves[i].ID == "tonight" && nextShelves[i].Enabled {
			nextShelves[i].Enabled = false
			changed = true
		}
	}

	if !hasShelf("top-ten") {
		// Insert at the very top (order 0), shifting everything else down
		for i := range nextShelves {
			nextShelves[i].Order++
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "top-ten",
			Name:    "Top 10 Today",
			Enabled: true,
			Order:   0,
		})
		changed = true
	}

	if !hasShelf("my-upcoming") {
		insertOrder := 2
		for _, shelf := range nextShelves {
			if shelf.ID == "continue-watching" {
				insertOrder = shelf.Order + 1
				break
			}
		}

		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "my-upcoming",
			Name:    "My Upcoming",
			Enabled: true,
			Order:   insertOrder,
			Sort:    "air-date-asc",
		})
		changed = true
	} else {
		for i := range nextShelves {
			if nextShelves[i].ID == "my-upcoming" && nextShelves[i].Sort == "" {
				nextShelves[i].Sort = "air-date-asc"
				changed = true
			}
		}
	}

	if !hasShelf("my-recommended") && !hasShelf("gemini-recs") {
		insertOrder := 2
		for _, shelf := range nextShelves {
			if shelf.ID == "continue-watching" {
				insertOrder = shelf.Order + 1
				break
			}
		}

		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "my-recommended",
			Name:    "My Recommended",
			Enabled: true,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("calendar") {
		insertOrder := 4
		for _, shelf := range nextShelves {
			if shelf.ID == "my-upcoming" {
				insertOrder = shelf.Order + 1
				break
			}
		}

		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "calendar",
			Name:    "Coming Up",
			Enabled: true,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("my-recently-aired") {
		insertOrder := 5
		for _, shelf := range nextShelves {
			if shelf.ID == "calendar" {
				insertOrder = shelf.Order + 1
				break
			}
		}

		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "my-recently-aired",
			Name:    "My Recently Aired",
			Enabled: true,
			Order:   insertOrder,
			CalendarSources: CalendarSettings{
				Watchlist:   BoolPtr(true),
				History:     BoolPtr(false),
				Trending:    BoolPtr(false),
				TopTrending: BoolPtr(false),
				MDBLists:    BoolPtr(false),
			},
		})
		changed = true
	}

	if !hasShelf("tonight") {
		insertOrder := 2
		for _, shelf := range nextShelves {
			if shelf.ID == "continue-watching" {
				insertOrder = shelf.Order + 1
				break
			}
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{ID: "tonight", Name: "Tonight", Enabled: false, Order: insertOrder})
		changed = true
	}

	if !hasShelf("streaming-services") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "trending-tv" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}

		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "streaming-services",
			Name:    "Streaming Services",
			Enabled: true,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("live-favorites") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "streaming-services" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}

		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}

		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "live-favorites",
			Name:    "Favorite Channels",
			Enabled: false,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("popular-on-server") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "live-favorites" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{
			ID:                 "popular-on-server",
			Name:               "Popular on This Server",
			Enabled:            false,
			Order:              insertOrder,
			Limit:              20,
			ActivityWindowDays: 90,
			MinimumProfiles:    2,
		})
		changed = true
	}

	if !hasShelf("recently-watched") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "popular-on-server" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{
			ID:                 "recently-watched",
			Name:               "Recently Watched",
			Enabled:            false,
			Order:              insertOrder,
			Limit:              20,
			ActivityWindowDays: 14,
			MaxItemsPerProfile: 3,
		})
		changed = true
	}

	if !hasShelf("dashboard") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "recently-watched" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "dashboard",
			Name:    "Dashboard",
			Enabled: false,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("permanent-prequeue") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "dashboard" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "permanent-prequeue",
			Name:    "Permanent Prequeue",
			Enabled: false,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("set-aside") {
		insertOrder := -1
		for _, shelf := range nextShelves {
			if shelf.ID == "permanent-prequeue" {
				insertOrder = shelf.Order + 1
				break
			}
			if shelf.Order > insertOrder {
				insertOrder = shelf.Order + 1
			}
		}
		if insertOrder < 0 {
			insertOrder = 0
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "set-aside",
			Name:    "Set Aside",
			Enabled: false,
			Order:   insertOrder,
		})
		changed = true
	}

	if !hasShelf("watch-something") {
		insertOrder := 2
		for _, shelf := range nextShelves {
			if shelf.ID == "continue-watching" {
				insertOrder = shelf.Order + 1
				break
			}
		}
		for i := range nextShelves {
			if nextShelves[i].Order >= insertOrder {
				nextShelves[i].Order++
			}
		}
		nextShelves = append(nextShelves, ShelfConfig{
			ID:      "watch-something",
			Name:    "Watch Something",
			Enabled: true,
			Order:   insertOrder,
		})
		changed = true
	}

	for i := range nextShelves {
		switch nextShelves[i].ID {
		case "popular-on-server":
			if nextShelves[i].Limit <= 0 {
				nextShelves[i].Limit = 20
				changed = true
			}
			if nextShelves[i].ActivityWindowDays < 7 || nextShelves[i].ActivityWindowDays > 365 {
				nextShelves[i].ActivityWindowDays = 90
				changed = true
			}
			if nextShelves[i].MinimumProfiles < 1 || nextShelves[i].MinimumProfiles > 100 {
				nextShelves[i].MinimumProfiles = 2
				changed = true
			}
		case "recently-watched":
			if nextShelves[i].Limit <= 0 {
				nextShelves[i].Limit = 20
				changed = true
			}
			if nextShelves[i].ActivityWindowDays < 1 || nextShelves[i].ActivityWindowDays > 90 {
				nextShelves[i].ActivityWindowDays = 14
				changed = true
			}
			if nextShelves[i].MaxItemsPerProfile < 1 || nextShelves[i].MaxItemsPerProfile > 20 {
				nextShelves[i].MaxItemsPerProfile = 3
				changed = true
			}
		}
	}

	return nextShelves, changed
}

func MigrateLibraryShelfConfigs(shelves []ShelfConfig) bool {
	changed := false
	for i := range shelves {
		if shelves[i].Type != "local-library" && shelves[i].Type != "library" {
			continue
		}
		if shelves[i].LibraryID == "" && strings.HasPrefix(shelves[i].ID, "local-library-") {
			shelves[i].LibraryID = strings.TrimPrefix(shelves[i].ID, "local-library-")
			changed = true
		}
		if shelves[i].Type == "local-library" {
			shelves[i].Type = "library"
			changed = true
		}
	}
	return changed
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
	MaxSizeMovieGB                         *float64        `json:"maxSizeMovieGb,omitempty"`
	MaxSizeEpisodeGB                       *float64        `json:"maxSizeEpisodeGb,omitempty"`
	MaxResolution                          *string         `json:"maxResolution,omitempty"` // Maximum resolution (e.g., "720p", "1080p", "2160p", empty = no limit)
	HDRDVPolicy                            HDRDVPolicy     `json:"hdrDvPolicy,omitempty"`   // HDR/DV inclusion policy: "none" (no exclusion), "hdr" (include HDR + DV 7/8), "hdr_dv" (include all HDR/DV)
	RequiredTerms                          []string        `json:"requiredTerms"`           // Terms where at least one must match for a result to be kept. Non-nil empty slice explicitly clears the inherited value.
	FilterOutTerms                         []string        `json:"filterOutTerms"`          // Terms to filter out from results (case-insensitive match in title). Non-nil empty slice explicitly clears the inherited value.
	PreferredTerms                         []string        `json:"preferredTerms"`          // Terms to prioritize in results (case-insensitive match in title). Non-nil empty slice explicitly clears the inherited value.
	NonPreferredTerms                      []string        `json:"nonPreferredTerms"`       // Terms to derank in results (case-insensitive match in title, ranked lower but not removed). Non-nil empty slice explicitly clears the inherited value.
	DownloadPreferredTerms                 []string        `json:"downloadPreferredTerms"`  // Terms to strongly prioritize only for download/prequeue selection. Non-nil empty slice explicitly clears the inherited value.
	PreferredScraper                       *string         `json:"preferredScraper,omitempty"`
	ServicePriority                        *string         `json:"servicePriority,omitempty"`
	UnknownTrackPolicy                     string          `json:"unknownTrackPolicy,omitempty"`
	AdaptivePlaybackEnabled                *bool           `json:"adaptivePlaybackEnabled,omitempty"`
	AdaptiveTargetBufferFactor             *float64        `json:"adaptiveTargetBufferFactor,omitempty"`
	RealDebridRestrictedTermsFilterEnabled *bool           `json:"realDebridRestrictedTermsFilterEnabled,omitempty"`
	SplitByService                         *bool           `json:"splitByService,omitempty"`
	Debrid                                 *FilterSettings `json:"debrid,omitempty"`
	Usenet                                 *FilterSettings `json:"usenet,omitempty"`
}

// AnimeFilteringSettings controls anime-specific language preferences (per-user overrides).
type AnimeFilteringSettings struct {
	AnimeLanguageEnabled   *bool   `json:"animeLanguageEnabled,omitempty"`   // When enabled, boost preferred language and derank others for anime content
	AnimePreferredLanguage *string `json:"animePreferredLanguage,omitempty"` // ISO 639-2/B code for preferred anime language
}

// DefaultUserSettings returns the default settings for a new user.
func DefaultUserSettings() UserSettings {
	return UserSettings{
		Playback: PlaybackSettings{
			PreferredPlayer:               "native",
			PreferredAudioLanguage:        "eng",
			PauseWhenAppInactive:          BoolPtr(false),
			UseLoadingScreen:              BoolPtr(false),
			SubtitleSize:                  1.0,
			SubtitleUseCropDetectPosition: BoolPtr(false),
			SubtitleColor:                 "#FFFFFF",
			SubtitleOpacity:               FloatPtr(1.0),
			SubtitleBold:                  BoolPtr(false),
			SubtitleOutlineEnabled:        BoolPtr(false),
			SubtitleOutlineColor:          "#000000",
			SubtitleOutlineWeight:         FloatPtr(0.35),
			SubtitleBackgroundEnabled:     BoolPtr(true),
			SubtitleBackgroundColor:       "#000000",
			SubtitleBackgroundOpacity:     FloatPtr(0.6),
			SeekForwardSeconds:            30,
			SeekBackwardSeconds:           10,
			SubtitleFont:                  StringPtr(""),
			ForceAACTranscoding:           BoolPtr(false),
			AutoPlayTrailersTV:            BoolPtr(false),
			RewindOnResumeFromPause:       IntPtr(0),
			RewindOnPlaybackStart:         IntPtr(0),
			DisablePrequeue:               BoolPtr(false),
			PrerollMode:                   "artwork",
			PrerollMediaScope:             "all",
			CreditsAutoSkip:               BoolPtr(false),
			StreamMigrationEnabled:        BoolPtr(true),
			IgnoreDVCompatibilityCheck:    BoolPtr(false),
			CreditsDetectionEnabled:       BoolPtr(false),
			MatchFrameRate:                BoolPtr(false),
			LiveClosedCaptionExtraction:   BoolPtr(true),
		},
		HomeShelves: HomeShelvesSettings{
			Shelves:                         DefaultHomeShelfConfigs(),
			ExploreCardPosition:             "end",
			ItemCap:                         20,
			ExcludeUpcomingFromContinue:     BoolPtr(false),
			HomeShelfFocusModel:             "left",
			DisableTvLandscapeCardExpansion: BoolPtr(false),
			HomeShelfScale:                  FloatPtr(1.0),
			HomeHeroScale:                   FloatPtr(1.0),
		},
		Filtering: FilterSettings{
			MaxSizeMovieGB:             FloatPtr(0),
			MaxSizeEpisodeGB:           FloatPtr(0),
			HDRDVPolicy:                HDRDVPolicyNoExclusion,
			AdaptivePlaybackEnabled:    BoolPtr(true),
			AdaptiveTargetBufferFactor: FloatPtr(0.7),
		},
		LiveTV: LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
		},
		Recordings: RecordingSettings{
			PaddingBeforeSeconds: IntPtr(300),
			PaddingAfterSeconds:  IntPtr(300),
		},
		Display: DisplaySettings{
			BadgeVisibility:                              []string{"watchProgress"},
			NavigationTabVisibility:                      []string{"home", "watchlist", "search", "lists", "profiles", "settings"},
			NavigationTabVisibilityIncludesSystemTabs:    true,
			NavigationTabVisibilityIncludesWatchlist:     true,
			NavigationTabVisibilityIncludesSports:        true,
			WatchStateIconStyle:                          "colored",
			IncludeUnreleasedMoviesInLists:               BoolPtr(true),
			IncludeUnreleasedShowsInLists:                BoolPtr(true),
			IncludeUnreleasedMoviesInSearch:              BoolPtr(true),
			IncludeUnreleasedShowsInSearch:               BoolPtr(true),
			DisableMobileTopCarousel:                     BoolPtr(false),
			ShowStreamSourceInfo:                         BoolPtr(true),
			HideContinueWatchingHeroMetadata:             BoolPtr(false),
			MoveDetailsRatingsToMetadata:                 BoolPtr(false),
			HideDetailsPoster:                            BoolPtr(false),
			HideTVDrawerRail:                             BoolPtr(false),
			SimpleMode:                                   BoolPtr(false),
			SimpleModeHomeShelves:                        StringSlicePtr(DefaultSimpleModeHomeShelfIDs()),
			DisableTVHomeCardDimming:                     BoolPtr(true),
			EnableAnimations:                             BoolPtr(true),
			EnableHeroArtPanning:                         BoolPtr(true),
			EnableHeroArtRotation:                        BoolPtr(true),
			ShowSeriesBackdropForMissingEpisodeArt:       BoolPtr(true),
			BlurUnwatchedEpisodeThumbnails:               BoolPtr(true),
			BlurUnwatchedEpisodeThumbnailsIncludeCurrent: BoolPtr(false),
			BlurUnwatchedEpisodeOverviews:                BoolPtr(true),
			BlurUnwatchedEpisodeOverviewsIncludeCurrent:  BoolPtr(false),
			Appearance: AppearanceSettings{
				FontScale:    FloatPtr(1.0),
				ButtonStyle:  "soft",
				ButtonRadius: "rounded",
			},
		},
		Network: NetworkSettings{
			HomeWifiSSID:     "",
			HomeBackendUrl:   "",
			RemoteBackendUrl: "",
		},
		Calendar: CalendarSettings{
			Watchlist:   BoolPtr(true),
			History:     BoolPtr(true),
			Trending:    BoolPtr(true),
			TopTrending: BoolPtr(true),
			MDBLists:    BoolPtr(true),
		},
	}
}

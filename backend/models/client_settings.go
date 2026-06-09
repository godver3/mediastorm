package models

// ClientFilterSettings contains per-client overrides.
// These fields use pointers to distinguish between "not set" (nil = use profile/global default)
// and explicit values (including zero/false).
type ClientFilterSettings struct {
	// Filtering overrides
	MaxSizeMovieGB         *float64     `json:"maxSizeMovieGb,omitempty"`
	MaxSizeEpisodeGB       *float64     `json:"maxSizeEpisodeGb,omitempty"`
	MaxResolution          *string      `json:"maxResolution,omitempty"`
	HDRDVPolicy            *HDRDVPolicy `json:"hdrDvPolicy,omitempty"`
	RequiredTerms          *[]string    `json:"requiredTerms,omitempty"`
	FilterOutTerms         *[]string    `json:"filterOutTerms,omitempty"`
	PreferredTerms         *[]string    `json:"preferredTerms,omitempty"`
	NonPreferredTerms      *[]string    `json:"nonPreferredTerms,omitempty"`
	DownloadPreferredTerms *[]string    `json:"downloadPreferredTerms,omitempty"`
	UnknownTrackPolicy     *string      `json:"unknownTrackPolicy,omitempty"`
	AnimeLanguageEnabled   *bool        `json:"animeLanguageEnabled,omitempty"`
	AnimePreferredLanguage *string      `json:"animePreferredLanguage,omitempty"`

	// Network settings for URL switching based on WiFi
	HomeWifiSSID     *string `json:"homeWifiSSID,omitempty"`
	HomeBackendUrl   *string `json:"homeBackendUrl,omitempty"`
	RemoteBackendUrl *string `json:"remoteBackendUrl,omitempty"`

	// Display overrides
	BypassFilteringForAIOStreamsOnly *bool               `json:"bypassFilteringForAioStreamsOnly,omitempty"`
	DisableMobileTopCarousel         *bool               `json:"disableMobileTopCarousel,omitempty"`
	NavigationTabVisibility          *[]string           `json:"navigationTabVisibility,omitempty"`
	Appearance                       *AppearanceSettings `json:"appearance,omitempty"`

	// Playback overrides
	PreferredPlayer            *string  `json:"preferredPlayer,omitempty"`
	PreferredAudioLanguage     *string  `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage  *string  `json:"preferredSubtitleLanguage,omitempty"`
	PreferredSubtitleMode      *string  `json:"preferredSubtitleMode,omitempty"`
	PauseWhenAppInactive       *bool    `json:"pauseWhenAppInactive,omitempty"`
	UseLoadingScreen           *bool    `json:"useLoadingScreen,omitempty"`
	SubtitleSize               *float64 `json:"subtitleSize,omitempty"`
	SubtitleColor              *string  `json:"subtitleColor,omitempty"`
	SubtitleOpacity            *float64 `json:"subtitleOpacity,omitempty"`
	SubtitleFont               *string  `json:"subtitleFont,omitempty"`
	SubtitleBold               *bool    `json:"subtitleBold,omitempty"`
	SubtitleOutlineEnabled     *bool    `json:"subtitleOutlineEnabled,omitempty"`
	SubtitleOutlineColor       *string  `json:"subtitleOutlineColor,omitempty"`
	SubtitleOutlineWeight      *float64 `json:"subtitleOutlineWeight,omitempty"`
	SubtitleBackgroundEnabled  *bool    `json:"subtitleBackgroundEnabled,omitempty"`
	SubtitleBackgroundColor    *string  `json:"subtitleBackgroundColor,omitempty"`
	SubtitleBackgroundOpacity  *float64 `json:"subtitleBackgroundOpacity,omitempty"`
	SeekForwardSeconds         *int     `json:"seekForwardSeconds,omitempty"`
	SeekBackwardSeconds        *int     `json:"seekBackwardSeconds,omitempty"`
	ForceAACTranscoding        *bool    `json:"forceAacTranscoding,omitempty"`
	AutoPlayTrailersTV         *bool    `json:"autoPlayTrailersTV,omitempty"`
	RewindOnResumeFromPause    *int     `json:"rewindOnResumeFromPause,omitempty"`
	RewindOnPlaybackStart      *int     `json:"rewindOnPlaybackStart,omitempty"`
	DisablePrequeue            *bool    `json:"disablePrequeue,omitempty"`
	IgnoreDVCompatibilityCheck *bool    `json:"ignoreDolbyVisionCompatibilityCheck,omitempty"`
	CreditsDetectionEnabled    *bool    `json:"creditsDetectionEnabled,omitempty"`
	CreditsAutoSkip            *bool    `json:"creditsAutoSkip,omitempty"`
	MaxResultsPerResolution    *int     `json:"maxResultsPerResolution,omitempty"`

	// Ranking criteria overrides
	RankingCriteria *[]ClientRankingCriterion `json:"rankingCriteria,omitempty"`

	// Adaptive playback measurements (device display + throughput) used to derive
	// transient filter caps at search time. Never written back into the flat
	// filter fields above.
	AdaptivePlayback *AdaptivePlaybackSettings `json:"adaptivePlayback,omitempty"`
}

// IsEmpty returns true if no settings are configured
func (c *ClientFilterSettings) IsEmpty() bool {
	return c.MaxSizeMovieGB == nil &&
		c.MaxSizeEpisodeGB == nil &&
		c.MaxResolution == nil &&
		c.HDRDVPolicy == nil &&
		c.RequiredTerms == nil &&
		c.FilterOutTerms == nil &&
		c.PreferredTerms == nil &&
		c.NonPreferredTerms == nil &&
		c.DownloadPreferredTerms == nil &&
		c.UnknownTrackPolicy == nil &&
		c.AnimeLanguageEnabled == nil &&
		c.AnimePreferredLanguage == nil &&
		c.BypassFilteringForAIOStreamsOnly == nil &&
		c.DisableMobileTopCarousel == nil &&
		c.NavigationTabVisibility == nil &&
		c.Appearance == nil &&
		c.PreferredPlayer == nil &&
		c.PreferredAudioLanguage == nil &&
		c.PreferredSubtitleLanguage == nil &&
		c.PreferredSubtitleMode == nil &&
		c.PauseWhenAppInactive == nil &&
		c.UseLoadingScreen == nil &&
		c.SubtitleSize == nil &&
		c.SubtitleColor == nil &&
		c.SubtitleOpacity == nil &&
		c.SubtitleFont == nil &&
		c.SubtitleBold == nil &&
		c.SubtitleOutlineEnabled == nil &&
		c.SubtitleOutlineColor == nil &&
		c.SubtitleOutlineWeight == nil &&
		c.SubtitleBackgroundEnabled == nil &&
		c.SubtitleBackgroundColor == nil &&
		c.SubtitleBackgroundOpacity == nil &&
		c.SeekForwardSeconds == nil &&
		c.SeekBackwardSeconds == nil &&
		c.ForceAACTranscoding == nil &&
		c.AutoPlayTrailersTV == nil &&
		c.RewindOnResumeFromPause == nil &&
		c.RewindOnPlaybackStart == nil &&
		c.DisablePrequeue == nil &&
		c.IgnoreDVCompatibilityCheck == nil &&
		c.CreditsDetectionEnabled == nil &&
		c.CreditsAutoSkip == nil &&
		c.MaxResultsPerResolution == nil &&
		c.HomeWifiSSID == nil &&
		c.HomeBackendUrl == nil &&
		c.RemoteBackendUrl == nil &&
		c.RankingCriteria == nil &&
		c.AdaptivePlayback == nil
}

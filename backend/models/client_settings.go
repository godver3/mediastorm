package models

// ClientFilterSettings contains per-client overrides.
// These fields use pointers to distinguish between "not set" (nil = use profile/global default)
// and explicit values (including zero/false).
type ClientFilterSettings struct {
	// Home screen overrides
	HomeShelfFocusModel *string `json:"homeShelfFocusModel,omitempty"`

	// Filtering overrides
	MaxSizeMovieGB                         *float64              `json:"maxSizeMovieGb,omitempty"`
	MaxSizeEpisodeGB                       *float64              `json:"maxSizeEpisodeGb,omitempty"`
	MaxResolution                          *string               `json:"maxResolution,omitempty"`
	HDRDVPolicy                            *HDRDVPolicy          `json:"hdrDvPolicy,omitempty"`
	RequiredTerms                          *[]string             `json:"requiredTerms,omitempty"`
	FilterOutTerms                         *[]string             `json:"filterOutTerms,omitempty"`
	PreferredTerms                         *[]string             `json:"preferredTerms,omitempty"`
	NonPreferredTerms                      *[]string             `json:"nonPreferredTerms,omitempty"`
	DownloadPreferredTerms                 *[]string             `json:"downloadPreferredTerms,omitempty"`
	UnknownTrackPolicy                     *string               `json:"unknownTrackPolicy,omitempty"`
	RealDebridRestrictedTermsFilterEnabled *bool                 `json:"realDebridRestrictedTermsFilterEnabled,omitempty"`
	SplitByService                         *bool                 `json:"splitByService,omitempty"`
	Debrid                                 *ClientFilterSettings `json:"debrid,omitempty"`
	Usenet                                 *ClientFilterSettings `json:"usenet,omitempty"`
	AnimeLanguageEnabled                   *bool                 `json:"animeLanguageEnabled,omitempty"`
	AnimePreferredLanguage                 *string               `json:"animePreferredLanguage,omitempty"`

	// Network settings for URL switching based on WiFi
	HomeWifiSSID     *string `json:"homeWifiSSID,omitempty"`
	HomeBackendUrl   *string `json:"homeBackendUrl,omitempty"`
	RemoteBackendUrl *string `json:"remoteBackendUrl,omitempty"`

	// Display overrides
	BypassFilteringForAIOStreamsOnly             *bool               `json:"bypassFilteringForAioStreamsOnly,omitempty"`
	ShowStreamSourceInfo                         *bool               `json:"showStreamSourceInfo,omitempty"`
	IncludeUnreleasedMoviesInLists               *bool               `json:"includeUnreleasedMoviesInLists,omitempty"`
	IncludeUnreleasedShowsInLists                *bool               `json:"includeUnreleasedShowsInLists,omitempty"`
	IncludeUnreleasedMoviesInSearch              *bool               `json:"includeUnreleasedMoviesInSearch,omitempty"`
	IncludeUnreleasedShowsInSearch               *bool               `json:"includeUnreleasedShowsInSearch,omitempty"`
	DisableMobileTopCarousel                     *bool               `json:"disableMobileTopCarousel,omitempty"`
	HideContinueWatchingHeroMetadata             *bool               `json:"hideContinueWatchingHeroMetadata,omitempty"`
	MoveDetailsRatingsToMetadata                 *bool               `json:"moveDetailsRatingsToMetadata,omitempty"`
	HideDetailsPoster                            *bool               `json:"hideDetailsPoster,omitempty"`
	ShowDetailsPrequeueFilename                  *bool               `json:"showDetailsPrequeueFilename,omitempty"`
	ShowDetailsSelectedTracks                    *bool               `json:"showDetailsSelectedTracks,omitempty"`
	HideTVDrawerRail                             *bool               `json:"hideTvDrawerRail,omitempty"`
	SimpleMode                                   *bool               `json:"simpleMode,omitempty"`
	SimpleModeHomeShelves                        *[]string           `json:"simpleModeHomeShelves,omitempty"`
	DisableTVHomeCardDimming                     *bool               `json:"disableTvHomeCardDimming,omitempty"`
	EnableAnimations                             *bool               `json:"enableAnimations,omitempty"`
	EnableHeroArtPanning                         *bool               `json:"enableHeroArtPanning,omitempty"`
	EnableHeroArtRotation                        *bool               `json:"enableHeroArtRotation,omitempty"`
	ShowSeriesBackdropForMissingEpisodeArt       *bool               `json:"showSeriesBackdropForMissingEpisodeArt,omitempty"`
	BlurUnwatchedEpisodeThumbnails               *bool               `json:"blurUnwatchedEpisodeThumbnails,omitempty"`
	BlurUnwatchedEpisodeThumbnailsIncludeCurrent *bool               `json:"blurUnwatchedEpisodeThumbnailsIncludeCurrent,omitempty"`
	BlurUnwatchedEpisodeOverviews                *bool               `json:"blurUnwatchedEpisodeOverviews,omitempty"`
	BlurUnwatchedEpisodeOverviewsIncludeCurrent  *bool               `json:"blurUnwatchedEpisodeOverviewsIncludeCurrent,omitempty"`
	NavigationTabVisibility                      *[]string           `json:"navigationTabVisibility,omitempty"`
	NavigationTabVisibilityIncludesSystemTabs    *bool               `json:"navigationTabVisibilityIncludesSystemTabs,omitempty"`
	NavigationTabVisibilityIncludesWatchlist     *bool               `json:"navigationTabVisibilityIncludesWatchlist,omitempty"`
	NavigationTabVisibilityIncludesSports        *bool               `json:"navigationTabVisibilityIncludesSports,omitempty"`
	Appearance                                   *AppearanceSettings `json:"appearance,omitempty"`

	// Playback overrides
	PreferredPlayer                   *string   `json:"preferredPlayer,omitempty"`
	PreferredAudioLanguage            *string   `json:"preferredAudioLanguage,omitempty"`
	PreferredSubtitleLanguage         *string   `json:"preferredSubtitleLanguage,omitempty"`
	AllowedTrackLanguages             *[]string `json:"allowedTrackLanguages,omitempty"`
	PreferredSubtitleMode             *string   `json:"preferredSubtitleMode,omitempty"`
	PauseWhenAppInactive              *bool     `json:"pauseWhenAppInactive,omitempty"`
	UseLoadingScreen                  *bool     `json:"useLoadingScreen,omitempty"`
	SubtitleSize                      *float64  `json:"subtitleSize,omitempty"`
	SubtitleUseCropDetectPosition     *bool     `json:"subtitleUseCropDetectPosition,omitempty"`
	SubtitleColor                     *string   `json:"subtitleColor,omitempty"`
	SubtitleOpacity                   *float64  `json:"subtitleOpacity,omitempty"`
	SubtitleFont                      *string   `json:"subtitleFont,omitempty"`
	SubtitleBold                      *bool     `json:"subtitleBold,omitempty"`
	SubtitleOutlineEnabled            *bool     `json:"subtitleOutlineEnabled,omitempty"`
	SubtitleOutlineColor              *string   `json:"subtitleOutlineColor,omitempty"`
	SubtitleOutlineWeight             *float64  `json:"subtitleOutlineWeight,omitempty"`
	SubtitleBackgroundEnabled         *bool     `json:"subtitleBackgroundEnabled,omitempty"`
	SubtitleBackgroundColor           *string   `json:"subtitleBackgroundColor,omitempty"`
	SubtitleBackgroundOpacity         *float64  `json:"subtitleBackgroundOpacity,omitempty"`
	SeekForwardSeconds                *int      `json:"seekForwardSeconds,omitempty"`
	SeekBackwardSeconds               *int      `json:"seekBackwardSeconds,omitempty"`
	ForceAACTranscoding               *bool     `json:"forceAacTranscoding,omitempty"`
	AutoPlayTrailersTV                *bool     `json:"autoPlayTrailersTV,omitempty"`
	ExperimentalNativeTrailerPlayerTV *bool     `json:"experimentalNativeTrailerPlayerTV,omitempty"`
	RewindOnResumeFromPause           *int      `json:"rewindOnResumeFromPause,omitempty"`
	RewindOnPlaybackStart             *int      `json:"rewindOnPlaybackStart,omitempty"`
	DisablePrequeue                   *bool     `json:"disablePrequeue,omitempty"`
	PrerollMode                       *string   `json:"prerollMode,omitempty"`
	PrerollAssetID                    *string   `json:"prerollAssetId,omitempty"`
	PrerollMediaScope                 *string   `json:"prerollMediaScope,omitempty"`
	PrerollSkipIfPrequeueReady        *bool     `json:"prerollSkipIfPrequeueReady,omitempty"`
	StreamMigrationEnabled            *bool     `json:"streamMigrationEnabled,omitempty"`
	IgnoreDVCompatibilityCheck        *bool     `json:"ignoreDolbyVisionCompatibilityCheck,omitempty"`
	CreditsDetectionEnabled           *bool     `json:"creditsDetectionEnabled,omitempty"`
	CreditsAutoSkip                   *bool     `json:"creditsAutoSkip,omitempty"`
	ScrobbleStartDelaySeconds         *int      `json:"scrobbleStartDelaySeconds,omitempty"`
	MatchFrameRate                    *bool     `json:"matchFrameRate,omitempty"`
	LiveClosedCaptionExtraction       *bool     `json:"liveClosedCaptionExtraction,omitempty"`
	MaxResultsPerResolution           *int      `json:"maxResultsPerResolution,omitempty"`

	// Ranking criteria overrides
	RankingCriteria       *[]ClientRankingCriterion `json:"rankingCriteria,omitempty"`
	NewestReleaseFirst    *bool                     `json:"newestReleaseFirst,omitempty"`
	RankingSplitByService *bool                     `json:"rankingSplitByService,omitempty"`
	DebridRankingCriteria *[]ClientRankingCriterion `json:"debridRankingCriteria,omitempty"`
	UsenetRankingCriteria *[]ClientRankingCriterion `json:"usenetRankingCriteria,omitempty"`

	// Adaptive playback measurements (device display + throughput) used to derive
	// transient filter caps at search time. Never written back into the flat
	// filter fields above.
	AdaptivePlayback *AdaptivePlaybackSettings `json:"adaptivePlayback,omitempty"`

	// Quality preset overrides for this device (nil inherits, "simple" uses
	// the simple ranking settings).
	QualityPresetMovie  *string `json:"qualityPresetMovie,omitempty"`
	QualityPresetSeries *string `json:"qualityPresetSeries,omitempty"`
	// QualityPresetAnimeMovie and QualityPresetAnimeSeries apply to anime
	// titles on this device before the normal slots.
	QualityPresetAnimeMovie  *string `json:"qualityPresetAnimeMovie,omitempty"`
	QualityPresetAnimeSeries *string `json:"qualityPresetAnimeSeries,omitempty"`
}

// IsEmpty returns true if no settings are configured
func (c *ClientFilterSettings) IsEmpty() bool {
	return c.HomeShelfFocusModel == nil &&
		c.QualityPresetMovie == nil &&
		c.QualityPresetSeries == nil &&
		c.QualityPresetAnimeMovie == nil &&
		c.QualityPresetAnimeSeries == nil &&
		c.MaxSizeMovieGB == nil &&
		c.MaxSizeEpisodeGB == nil &&
		c.MaxResolution == nil &&
		c.HDRDVPolicy == nil &&
		c.RequiredTerms == nil &&
		c.FilterOutTerms == nil &&
		c.PreferredTerms == nil &&
		c.NonPreferredTerms == nil &&
		c.DownloadPreferredTerms == nil &&
		c.UnknownTrackPolicy == nil &&
		c.RealDebridRestrictedTermsFilterEnabled == nil &&
		c.SplitByService == nil &&
		c.Debrid == nil &&
		c.Usenet == nil &&
		c.AnimeLanguageEnabled == nil &&
		c.AnimePreferredLanguage == nil &&
		c.BypassFilteringForAIOStreamsOnly == nil &&
		c.ShowStreamSourceInfo == nil &&
		c.IncludeUnreleasedMoviesInLists == nil &&
		c.IncludeUnreleasedShowsInLists == nil &&
		c.IncludeUnreleasedMoviesInSearch == nil &&
		c.IncludeUnreleasedShowsInSearch == nil &&
		c.DisableMobileTopCarousel == nil &&
		c.HideContinueWatchingHeroMetadata == nil &&
		c.MoveDetailsRatingsToMetadata == nil &&
		c.HideDetailsPoster == nil &&
		c.ShowDetailsPrequeueFilename == nil &&
		c.ShowDetailsSelectedTracks == nil &&
		c.HideTVDrawerRail == nil &&
		c.SimpleMode == nil &&
		c.SimpleModeHomeShelves == nil &&
		c.DisableTVHomeCardDimming == nil &&
		c.EnableAnimations == nil &&
		c.EnableHeroArtPanning == nil &&
		c.EnableHeroArtRotation == nil &&
		c.ShowSeriesBackdropForMissingEpisodeArt == nil &&
		c.BlurUnwatchedEpisodeThumbnails == nil &&
		c.BlurUnwatchedEpisodeThumbnailsIncludeCurrent == nil &&
		c.BlurUnwatchedEpisodeOverviews == nil &&
		c.BlurUnwatchedEpisodeOverviewsIncludeCurrent == nil &&
		c.NavigationTabVisibility == nil &&
		c.Appearance == nil &&
		c.PreferredPlayer == nil &&
		c.PreferredAudioLanguage == nil &&
		c.PreferredSubtitleLanguage == nil &&
		c.AllowedTrackLanguages == nil &&
		c.PreferredSubtitleMode == nil &&
		c.PauseWhenAppInactive == nil &&
		c.UseLoadingScreen == nil &&
		c.SubtitleSize == nil &&
		c.SubtitleUseCropDetectPosition == nil &&
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
		c.ExperimentalNativeTrailerPlayerTV == nil &&
		c.RewindOnResumeFromPause == nil &&
		c.RewindOnPlaybackStart == nil &&
		c.DisablePrequeue == nil &&
		c.PrerollMode == nil &&
		c.PrerollAssetID == nil &&
		c.PrerollMediaScope == nil &&
		c.PrerollSkipIfPrequeueReady == nil &&
		c.StreamMigrationEnabled == nil &&
		c.IgnoreDVCompatibilityCheck == nil &&
		c.CreditsDetectionEnabled == nil &&
		c.CreditsAutoSkip == nil &&
		c.ScrobbleStartDelaySeconds == nil &&
		c.MatchFrameRate == nil &&
		c.LiveClosedCaptionExtraction == nil &&
		c.MaxResultsPerResolution == nil &&
		c.HomeWifiSSID == nil &&
		c.HomeBackendUrl == nil &&
		c.RemoteBackendUrl == nil &&
		c.RankingCriteria == nil &&
		c.NewestReleaseFirst == nil &&
		c.RankingSplitByService == nil &&
		c.DebridRankingCriteria == nil &&
		c.UsenetRankingCriteria == nil &&
		c.AdaptivePlayback == nil
}

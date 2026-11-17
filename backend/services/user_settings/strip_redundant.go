package user_settings

import (
	"log"
	"reflect"
	"slices"
	"sort"

	"novastream/config"
	"novastream/models"
)

// ClientsLister returns all known clients so we can map client→profile.
type ClientsLister interface {
	List() []models.Client
}

// ClientSettingsBatch allows bulk read/write of client settings.
type ClientSettingsBatch interface {
	GetAll() map[string]models.ClientFilterSettings
	UpdateBatch(settings map[string]models.ClientFilterSettings) error
}

// StripRedundantOverrides removes per-profile overrides that now match globalSettings,
// then removes per-client overrides that match their parent profile's effective value.
// This is called after global settings are saved.
func (s *Service) StripRedundantOverrides(globalSettings config.Settings, clientsLister ClientsLister, clientSettingsSvc ClientSettingsBatch) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profileChanged := false
	for userID, us := range s.settings {
		changed := s.stripProfileSettings(&us, globalSettings)
		if changed {
			s.settings[userID] = us
		}
		// Clean up entries that are empty (either already were, or became empty after stripping)
		if isSettingsEmpty(us) {
			log.Printf("[strip-redundant] profile %q is empty, removing entry", userID)
			delete(s.settings, userID)
			profileChanged = true
		} else if changed {
			profileChanged = true
		}
	}

	if profileChanged {
		if err := s.saveLocked(); err != nil {
			log.Printf("[strip-redundant] failed to save user settings: %v", err)
		} else {
			log.Printf("[strip-redundant] saved user settings after stripping redundant overrides")
		}
	}

	// Now strip client settings using effective profile values
	if clientsLister == nil || clientSettingsSvc == nil {
		return
	}

	allClients := clientSettingsSvc.GetAll()
	if len(allClients) == 0 {
		return
	}

	// Build device→last-active user for legacy device-only keys
	clientToUser := make(map[string]string)
	for _, c := range clientsLister.List() {
		// List expands person×device instances; last write wins for last-active fallback
		clientToUser[c.ID] = c.UserID
	}

	// Build effective profile settings cache (profile values merged with global defaults)
	effectiveProfiles := make(map[string]models.UserSettings)

	clientChanged := false
	for key, cs := range allClients {
		clientID, userID, ok := models.SplitClientSettingsKey(key)
		if !ok {
			// Legacy device-only key
			clientID = key
			userID = clientToUser[clientID]
		}
		if userID == "" {
			continue
		}

		effective, exists := effectiveProfiles[userID]
		if !exists {
			effective = s.computeEffectiveProfile(userID, globalSettings)
			effectiveProfiles[userID] = effective
		}

		if stripClientSettings(&cs, effective) {
			if cs.IsEmpty() {
				delete(allClients, key)
			} else {
				allClients[key] = cs
			}
			clientChanged = true
		}
	}

	if clientChanged {
		if err := clientSettingsSvc.UpdateBatch(allClients); err != nil {
			log.Printf("[strip-redundant] failed to save client settings: %v", err)
		} else {
			log.Printf("[strip-redundant] saved client settings after stripping redundant overrides")
		}
	}
}

// computeEffectiveProfile returns the profile's settings merged with global defaults.
// Must be called while holding s.mu.
func (s *Service) computeEffectiveProfile(userID string, global config.Settings) models.UserSettings {
	us, ok := s.settings[userID]
	if !ok {
		return globalToUserSettings(global)
	}
	return mergeWithGlobal(us, global)
}

// globalToUserSettings converts global config values to the UserSettings shape.
func globalToUserSettings(g config.Settings) models.UserSettings {
	return models.UserSettings{
		Metadata: models.MetadataSettings{
			PrimaryLanguage: g.Metadata.EffectivePrimaryLanguage(),
		},
		Playback: models.PlaybackSettings{
			PreferredPlayer:               g.Playback.PreferredPlayer,
			PreferredAudioLanguage:        g.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage:     g.Playback.PreferredSubtitleLanguage,
			AllowedTrackLanguages:         models.StringSlicePtr(g.Playback.AllowedTrackLanguages),
			PreferredSubtitleMode:         g.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:          models.BoolPtr(g.Playback.PauseWhenAppInactive),
			UseLoadingScreen:              models.BoolPtr(g.Playback.UseLoadingScreen),
			SubtitleSize:                  g.Playback.SubtitleSize,
			SubtitleUseCropDetectPosition: models.BoolPtr(g.Playback.SubtitleUseCropDetectPosition),
			SubtitleColor:                 g.Playback.SubtitleColor,
			SubtitleOpacity:               models.FloatPtr(g.Playback.SubtitleOpacity),
			SubtitleFont:                  models.StringPtr(g.Playback.SubtitleFont),
			SubtitleBold:                  models.BoolPtr(g.Playback.SubtitleBold),
			SubtitleOutlineEnabled:        models.BoolPtr(g.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:          g.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:         models.FloatPtr(g.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled:     models.BoolPtr(g.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:       g.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity:     models.FloatPtr(g.Playback.SubtitleBackgroundOpacity),
			SeekForwardSeconds:            g.Playback.SeekForwardSeconds,
			SeekBackwardSeconds:           g.Playback.SeekBackwardSeconds,
			ForceAACTranscoding:           models.BoolPtr(g.Playback.ForceAACTranscoding),
			AutoPlayTrailersTV:            models.BoolPtr(g.Playback.AutoPlayTrailersTV),
			RewindOnResumeFromPause:       models.IntPtr(g.Playback.RewindOnResumeFromPause),
			RewindOnPlaybackStart:         models.IntPtr(g.Playback.RewindOnPlaybackStart),
			DisablePrequeue:               models.BoolPtr(g.Playback.DisablePrequeue),
			PrerollMode:                   g.Playback.PrerollMode,
			PrerollAssetID:                g.Playback.PrerollAssetID,
			PrerollMediaScope:             g.Playback.PrerollMediaScope,
			PrerollSkipIfPrequeueReady:    models.BoolPtr(g.Playback.PrerollSkipIfPrequeueReady),
			StreamMigrationEnabled:        models.BoolPtr(g.Playback.StreamMigrationEnabled),
			IgnoreDVCompatibilityCheck:    models.BoolPtr(g.Playback.IgnoreDVCompatibilityCheck),
			CreditsDetectionEnabled:       models.BoolPtr(g.Playback.CreditsDetectionEnabled),
			CreditsAutoSkip:               models.BoolPtr(g.Playback.CreditsAutoSkip || g.Playback.CreditsDetection),
			ScrobbleStartDelaySeconds:     models.IntPtr(g.Playback.ScrobbleStartDelaySeconds),
			MatchFrameRate:                models.BoolPtr(g.Playback.MatchFrameRate),
			LiveClosedCaptionExtraction:   models.BoolPtr(g.Playback.LiveClosedCaptionExtraction),
			MaxResultsPerResolution:       models.IntPtr(g.Playback.MaxResultsPerResolution),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                         models.FloatPtr(g.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                       models.FloatPtr(g.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                          models.StringPtr(g.Filtering.MaxResolution),
			HDRDVPolicy:                            models.HDRDVPolicy(g.Filtering.HDRDVPolicy),
			RequiredTerms:                          g.Filtering.RequiredTerms,
			FilterOutTerms:                         g.Filtering.FilterOutTerms,
			PreferredTerms:                         g.Filtering.PreferredTerms,
			NonPreferredTerms:                      g.Filtering.NonPreferredTerms,
			DownloadPreferredTerms:                 g.Filtering.DownloadPreferredTerms,
			PreferredScraper:                       models.StringPtr(g.Filtering.PreferredScraper),
			ServicePriority:                        models.StringPtr(string(g.Filtering.ServicePriority)),
			UnknownTrackPolicy:                     string(g.Filtering.UnknownTrackPolicy),
			AdaptivePlaybackEnabled:                models.BoolPtr(g.Filtering.AdaptivePlaybackEnabled),
			AdaptiveTargetBufferFactor:             models.FloatPtr(g.Filtering.AdaptiveTargetBufferFactor),
			RealDebridRestrictedTermsFilterEnabled: models.BoolPtr(g.Filtering.RealDebridRestrictedTermsFilterEnabled),
		},
		AnimeFiltering: models.AnimeFilteringSettings{
			AnimeLanguageEnabled:   models.BoolPtr(g.AnimeFiltering.AnimeLanguageEnabled),
			AnimePreferredLanguage: models.StringPtr(g.AnimeFiltering.AnimePreferredLanguage),
		},
		Display: models.DisplaySettings{
			BadgeVisibility:                        g.Display.BadgeVisibility,
			NavigationTabVisibility:                g.Display.NavigationTabVisibility,
			WatchStateIconStyle:                    g.Display.WatchStateIconStyle,
			IncludeUnreleasedMoviesInLists:         models.BoolPtr(g.Display.IncludeUnreleasedMoviesInLists),
			IncludeUnreleasedShowsInLists:          models.BoolPtr(g.Display.IncludeUnreleasedShowsInLists),
			IncludeUnreleasedMoviesInSearch:        models.BoolPtr(g.Display.IncludeUnreleasedMoviesInSearch),
			IncludeUnreleasedShowsInSearch:         models.BoolPtr(g.Display.IncludeUnreleasedShowsInSearch),
			BypassFilteringForAIOStreamsOnly:       models.BoolPtr(g.Display.BypassFilteringForAIOStreamsOnly),
			ShowStreamSourceInfo:                   models.BoolPtr(g.Display.ShowStreamSourceInfo),
			DisableMobileTopCarousel:               models.BoolPtr(g.Display.DisableMobileTopCarousel),
			HideContinueWatchingHeroMetadata:       models.BoolPtr(g.Display.HideContinueWatchingHeroMetadata),
			MoveDetailsRatingsToMetadata:           models.BoolPtr(g.Display.MoveDetailsRatingsToMetadata),
			HideDetailsPoster:                      models.BoolPtr(g.Display.HideDetailsPoster),
			ShowDetailsPrequeueFilename:            models.BoolPtr(g.Display.ShowDetailsPrequeueFilename),
			ShowDetailsRuntime:                     models.BoolPtr(g.Display.ShowDetailsRuntime),
			ShowDetailsSelectedTracks:              models.BoolPtr(g.Display.ShowDetailsSelectedTracks),
			HideTVDrawerRail:                       models.BoolPtr(g.Display.HideTVDrawerRail),
			SimpleMode:                             models.BoolPtr(g.Display.SimpleMode),
			SimpleModeHomeShelves:                  models.StringSlicePtr(g.Display.SimpleModeHomeShelves),
			DisableTVHomeCardDimming:               models.BoolPtr(g.Display.DisableTVHomeCardDimming),
			EnableAnimations:                       models.BoolPtr(g.Display.EnableAnimations),
			EnableHeroArtPanning:                   models.BoolPtr(g.Display.EnableHeroArtPanning),
			EnableHeroArtRotation:                  models.BoolPtr(g.Display.EnableHeroArtRotation),
			ShowSeriesBackdropForMissingEpisodeArt: models.BoolPtr(g.Display.ShowSeriesBackdropForMissingEpisodeArt),
			AppLanguage:                            g.Display.AppLanguage,
			Appearance: models.AppearanceSettings{
				FontScale:            g.Display.Appearance.FontScale,
				AccentColor:          g.Display.Appearance.AccentColor,
				TextColor:            g.Display.Appearance.TextColor,
				SecondaryTextColor:   g.Display.Appearance.SecondaryTextColor,
				BackgroundColor:      g.Display.Appearance.BackgroundColor,
				ModalBackgroundColor: g.Display.Appearance.ModalBackgroundColor,
				ButtonStyle:          g.Display.Appearance.ButtonStyle,
				ButtonRadius:         g.Display.Appearance.ButtonRadius,
				HighContrast:         g.Display.Appearance.HighContrast,
				ReduceOverlays:       g.Display.Appearance.ReduceOverlays,
			},
		},
		HomeShelves: models.HomeShelvesSettings{
			Views:                           g.HomeShelves.Views,
			Shelves:                         configShelvesToModel(g.HomeShelves.Shelves),
			ExploreCardPosition:             string(g.HomeShelves.ExploreCardPosition),
			ItemCap:                         g.HomeShelves.ItemCap,
			HomeShelfFocusModel:             g.HomeShelves.HomeShelfFocusModel,
			MobileTopShelfMode:              g.HomeShelves.MobileTopShelfMode,
			MobileTopShelfSourceID:          g.HomeShelves.MobileTopShelfSourceID,
			TVTopShelfMode:                  g.HomeShelves.TVTopShelfMode,
			TVTopShelfSourceID:              g.HomeShelves.TVTopShelfSourceID,
			DisableTvLandscapeCardExpansion: models.BoolPtr(g.HomeShelves.DisableTvLandscapeCardExpansion),
			HomeShelfScale:                  models.FloatPtr(g.HomeShelves.HomeShelfScale),
			HomeHeroScale:                   models.FloatPtr(g.HomeShelves.HomeHeroScale),
		},
		Network: models.NetworkSettings{
			HomeWifiSSID:     g.Network.HomeWifiSSID,
			HomeBackendUrl:   g.Network.HomeBackendUrl,
			RemoteBackendUrl: g.Network.RemoteBackendUrl,
		},
	}
}

func configShelvesToModel(shelves []config.ShelfConfig) []models.ShelfConfig {
	out := make([]models.ShelfConfig, len(shelves))
	for i, s := range shelves {
		out[i] = models.ShelfConfig{
			ID:                     s.ID,
			Name:                   s.Name,
			Enabled:                s.Enabled,
			Order:                  s.Order,
			Type:                   s.Type,
			LibraryID:              s.LibraryID,
			ListURL:                s.ListURL,
			AddonManifestURL:       s.AddonManifestURL,
			AddonCatalogType:       s.AddonCatalogType,
			AddonCatalogID:         s.AddonCatalogID,
			AddonName:              s.AddonName,
			TMDBSourceType:         s.TMDBSourceType,
			TMDBSourceID:           s.TMDBSourceID,
			TMDBSourceName:         s.TMDBSourceName,
			TMDBMediaType:          s.TMDBMediaType,
			TMDBDiscoverQuery:      s.TMDBDiscoverQuery,
			StreamingServices:      configStreamingServicesToModel(s.StreamingServices),
			CollectionItems:        configCollectionHubItemsToModel(s.CollectionItems),
			TraktAccountID:         s.TraktAccountID,
			TraktListType:          s.TraktListType,
			TraktListID:            s.TraktListID,
			SimklAccountID:         s.SimklAccountID,
			SimklListType:          s.SimklListType,
			SimklMediaType:         s.SimklMediaType,
			LetterboxdListID:       s.LetterboxdListID,
			LetterboxdListURL:      s.LetterboxdListURL,
			Limit:                  s.Limit,
			ActivityWindowDays:     s.ActivityWindowDays,
			MinimumProfiles:        s.MinimumProfiles,
			MaxItemsPerProfile:     s.MaxItemsPerProfile,
			HideUnreleased:         s.HideUnreleased,
			Sort:                   s.Sort,
			AnimateLogoOnlyOnFocus: s.AnimateLogoOnlyOnFocus,
			ShowCollectionTitles:   s.ShowCollectionTitles,
			ShowCollectionCounts:   s.ShowCollectionCounts,
			CalendarSources: models.CalendarSettings{
				Watchlist:      s.CalendarSources.Watchlist,
				History:        s.CalendarSources.History,
				Trending:       s.CalendarSources.Trending,
				TopTrending:    s.CalendarSources.TopTrending,
				MDBLists:       s.CalendarSources.MDBLists,
				MDBListShelves: s.CalendarSources.MDBListShelves,
			},
		}
	}
	return out
}

func configCollectionHubItemsToModel(items []config.CollectionHubLink) []models.CollectionHubLink {
	if len(items) == 0 {
		return nil
	}
	out := make([]models.CollectionHubLink, len(items))
	for i, item := range items {
		out[i] = models.CollectionHubLink{
			ID:            item.ID,
			Name:          item.Name,
			Enabled:       item.Enabled,
			Order:         item.Order,
			SourceShelfID: item.SourceShelfID,
			LogoURL:       item.LogoURL,
			HeroArtURL:    item.HeroArtURL,
			LogoScale:     item.LogoScale,
			TintColor:     item.TintColor,
		}
	}
	return out
}

func configStreamingServicesToModel(services []config.StreamingServiceLink) []models.StreamingServiceLink {
	if len(services) == 0 {
		return nil
	}
	out := make([]models.StreamingServiceLink, len(services))
	for i, service := range services {
		lists := make([]models.StreamingServiceListLink, len(service.Lists))
		for j, list := range service.Lists {
			lists[j] = models.StreamingServiceListLink{
				Key:   list.Key,
				Title: list.Title,
				URL:   list.URL,
			}
		}
		out[i] = models.StreamingServiceLink{
			ID:        service.ID,
			Name:      service.Name,
			Enabled:   service.Enabled,
			Order:     service.Order,
			LogoURL:   service.LogoURL,
			LogoScale: service.LogoScale,
			TintColor: service.TintColor,
			Lists:     lists,
		}
	}
	return out
}

// mergeWithGlobal returns effective user settings: profile overrides filled in with global defaults.
func mergeWithGlobal(us models.UserSettings, g config.Settings) models.UserSettings {
	eff := us

	if eff.Metadata.PrimaryLanguage == "" {
		eff.Metadata.PrimaryLanguage = g.Metadata.EffectivePrimaryLanguage()
	}

	// Playback: empty strings inherit global
	if eff.Playback.PreferredPlayer == "" {
		eff.Playback.PreferredPlayer = g.Playback.PreferredPlayer
	}
	if eff.Playback.PreferredAudioLanguage == "" {
		eff.Playback.PreferredAudioLanguage = g.Playback.PreferredAudioLanguage
	}
	if eff.Playback.PreferredSubtitleLanguage == "" {
		eff.Playback.PreferredSubtitleLanguage = g.Playback.PreferredSubtitleLanguage
	}
	if eff.Playback.AllowedTrackLanguages == nil {
		eff.Playback.AllowedTrackLanguages = models.StringSlicePtr(g.Playback.AllowedTrackLanguages)
	}
	if eff.Playback.PreferredSubtitleMode == "" {
		eff.Playback.PreferredSubtitleMode = g.Playback.PreferredSubtitleMode
	}
	if eff.Playback.PauseWhenAppInactive == nil {
		eff.Playback.PauseWhenAppInactive = models.BoolPtr(g.Playback.PauseWhenAppInactive)
	}
	if eff.Playback.SubtitleSize == 0 {
		eff.Playback.SubtitleSize = g.Playback.SubtitleSize
	}
	if eff.Playback.SubtitleUseCropDetectPosition == nil {
		eff.Playback.SubtitleUseCropDetectPosition = models.BoolPtr(g.Playback.SubtitleUseCropDetectPosition)
	}
	if eff.Playback.SubtitleColor == "" {
		eff.Playback.SubtitleColor = g.Playback.SubtitleColor
	}
	if eff.Playback.SubtitleOpacity == nil {
		eff.Playback.SubtitleOpacity = models.FloatPtr(g.Playback.SubtitleOpacity)
	}
	if eff.Playback.SubtitleFont == nil {
		eff.Playback.SubtitleFont = models.StringPtr(g.Playback.SubtitleFont)
	}
	if eff.Playback.SubtitleBold == nil {
		eff.Playback.SubtitleBold = models.BoolPtr(g.Playback.SubtitleBold)
	}
	if eff.Playback.SubtitleOutlineEnabled == nil {
		eff.Playback.SubtitleOutlineEnabled = models.BoolPtr(g.Playback.SubtitleOutlineEnabled)
	}
	if eff.Playback.SubtitleOutlineColor == "" {
		eff.Playback.SubtitleOutlineColor = g.Playback.SubtitleOutlineColor
	}
	if eff.Playback.SubtitleOutlineWeight == nil {
		eff.Playback.SubtitleOutlineWeight = models.FloatPtr(g.Playback.SubtitleOutlineWeight)
	}
	if eff.Playback.SubtitleBackgroundEnabled == nil {
		eff.Playback.SubtitleBackgroundEnabled = models.BoolPtr(g.Playback.SubtitleBackgroundEnabled)
	}
	if eff.Playback.SubtitleBackgroundColor == "" {
		eff.Playback.SubtitleBackgroundColor = g.Playback.SubtitleBackgroundColor
	}
	if eff.Playback.SubtitleBackgroundOpacity == nil {
		eff.Playback.SubtitleBackgroundOpacity = models.FloatPtr(g.Playback.SubtitleBackgroundOpacity)
	}
	if eff.Playback.SeekForwardSeconds == 0 {
		eff.Playback.SeekForwardSeconds = g.Playback.SeekForwardSeconds
	}
	if eff.Playback.SeekBackwardSeconds == 0 {
		eff.Playback.SeekBackwardSeconds = g.Playback.SeekBackwardSeconds
	}
	if eff.Playback.ForceAACTranscoding == nil {
		eff.Playback.ForceAACTranscoding = models.BoolPtr(g.Playback.ForceAACTranscoding)
	}
	if eff.Playback.AutoPlayTrailersTV == nil {
		eff.Playback.AutoPlayTrailersTV = models.BoolPtr(g.Playback.AutoPlayTrailersTV)
	}
	if eff.Playback.UseLoadingScreen == nil {
		eff.Playback.UseLoadingScreen = models.BoolPtr(g.Playback.UseLoadingScreen)
	}
	if eff.Playback.RewindOnResumeFromPause == nil {
		eff.Playback.RewindOnResumeFromPause = models.IntPtr(g.Playback.RewindOnResumeFromPause)
	}
	if eff.Playback.RewindOnPlaybackStart == nil {
		eff.Playback.RewindOnPlaybackStart = models.IntPtr(g.Playback.RewindOnPlaybackStart)
	}
	if eff.Playback.CreditsAutoSkip == nil {
		eff.Playback.CreditsAutoSkip = models.BoolPtr(g.Playback.CreditsAutoSkip || g.Playback.CreditsDetection)
	}
	if eff.Playback.ScrobbleStartDelaySeconds == nil {
		eff.Playback.ScrobbleStartDelaySeconds = models.IntPtr(g.Playback.ScrobbleStartDelaySeconds)
	}
	if eff.Playback.DisablePrequeue == nil {
		eff.Playback.DisablePrequeue = models.BoolPtr(g.Playback.DisablePrequeue)
	}
	if eff.Playback.PrerollMode == "" {
		eff.Playback.PrerollMode = g.Playback.PrerollMode
		eff.Playback.PrerollAssetID = g.Playback.PrerollAssetID
	}
	if eff.Playback.PrerollMediaScope == "" {
		eff.Playback.PrerollMediaScope = g.Playback.PrerollMediaScope
	}
	if eff.Playback.PrerollSkipIfPrequeueReady == nil {
		eff.Playback.PrerollSkipIfPrequeueReady = models.BoolPtr(g.Playback.PrerollSkipIfPrequeueReady)
	}
	if eff.Playback.StreamMigrationEnabled == nil {
		eff.Playback.StreamMigrationEnabled = models.BoolPtr(g.Playback.StreamMigrationEnabled)
	}
	if eff.Playback.IgnoreDVCompatibilityCheck == nil {
		eff.Playback.IgnoreDVCompatibilityCheck = models.BoolPtr(g.Playback.IgnoreDVCompatibilityCheck)
	}
	if eff.Playback.CreditsDetectionEnabled == nil {
		eff.Playback.CreditsDetectionEnabled = models.BoolPtr(g.Playback.CreditsDetectionEnabled)
	}
	if eff.Playback.MatchFrameRate == nil {
		eff.Playback.MatchFrameRate = models.BoolPtr(g.Playback.MatchFrameRate)
	}
	if eff.Playback.LiveClosedCaptionExtraction == nil {
		eff.Playback.LiveClosedCaptionExtraction = models.BoolPtr(g.Playback.LiveClosedCaptionExtraction)
	}
	if eff.Playback.MaxResultsPerResolution == nil {
		eff.Playback.MaxResultsPerResolution = models.IntPtr(g.Playback.MaxResultsPerResolution)
	}

	// Filtering: nil pointers inherit global
	if eff.Filtering.MaxSizeMovieGB == nil {
		eff.Filtering.MaxSizeMovieGB = models.FloatPtr(g.Filtering.MaxSizeMovieGB)
	}
	if eff.Filtering.MaxSizeEpisodeGB == nil {
		eff.Filtering.MaxSizeEpisodeGB = models.FloatPtr(g.Filtering.MaxSizeEpisodeGB)
	}
	if eff.Filtering.MaxResolution == nil {
		eff.Filtering.MaxResolution = models.StringPtr(g.Filtering.MaxResolution)
	}
	if eff.Filtering.HDRDVPolicy == "" {
		eff.Filtering.HDRDVPolicy = models.HDRDVPolicy(g.Filtering.HDRDVPolicy)
	}
	if eff.Filtering.RequiredTerms == nil {
		eff.Filtering.RequiredTerms = g.Filtering.RequiredTerms
	}
	if eff.Filtering.FilterOutTerms == nil {
		eff.Filtering.FilterOutTerms = g.Filtering.FilterOutTerms
	}
	if eff.Filtering.PreferredTerms == nil {
		eff.Filtering.PreferredTerms = g.Filtering.PreferredTerms
	}
	if eff.Filtering.NonPreferredTerms == nil {
		eff.Filtering.NonPreferredTerms = g.Filtering.NonPreferredTerms
	}
	if eff.Filtering.DownloadPreferredTerms == nil {
		eff.Filtering.DownloadPreferredTerms = g.Filtering.DownloadPreferredTerms
	}
	if eff.Filtering.PreferredScraper == nil {
		eff.Filtering.PreferredScraper = models.StringPtr(g.Filtering.PreferredScraper)
	}
	if eff.Filtering.ServicePriority == nil {
		eff.Filtering.ServicePriority = models.StringPtr(string(g.Filtering.ServicePriority))
	}
	if eff.Filtering.UnknownTrackPolicy == "" {
		eff.Filtering.UnknownTrackPolicy = string(g.Filtering.UnknownTrackPolicy)
	}
	if eff.Filtering.AdaptivePlaybackEnabled == nil {
		eff.Filtering.AdaptivePlaybackEnabled = models.BoolPtr(g.Filtering.AdaptivePlaybackEnabled)
	}
	if eff.Filtering.AdaptiveTargetBufferFactor == nil {
		eff.Filtering.AdaptiveTargetBufferFactor = models.FloatPtr(g.Filtering.AdaptiveTargetBufferFactor)
	}
	if eff.Filtering.RealDebridRestrictedTermsFilterEnabled == nil {
		eff.Filtering.RealDebridRestrictedTermsFilterEnabled = models.BoolPtr(g.Filtering.RealDebridRestrictedTermsFilterEnabled)
	}
	if eff.Display.BypassFilteringForAIOStreamsOnly == nil {
		eff.Display.BypassFilteringForAIOStreamsOnly = models.BoolPtr(g.Display.BypassFilteringForAIOStreamsOnly)
	}
	if eff.Display.ShowStreamSourceInfo == nil {
		eff.Display.ShowStreamSourceInfo = models.BoolPtr(g.Display.ShowStreamSourceInfo)
	}
	if eff.Display.DisableMobileTopCarousel == nil {
		eff.Display.DisableMobileTopCarousel = models.BoolPtr(g.Display.DisableMobileTopCarousel)
	}
	if eff.Display.HideContinueWatchingHeroMetadata == nil {
		eff.Display.HideContinueWatchingHeroMetadata = models.BoolPtr(g.Display.HideContinueWatchingHeroMetadata)
	}
	if eff.Display.MoveDetailsRatingsToMetadata == nil {
		eff.Display.MoveDetailsRatingsToMetadata = models.BoolPtr(g.Display.MoveDetailsRatingsToMetadata)
	}
	if eff.Display.HideDetailsPoster == nil {
		eff.Display.HideDetailsPoster = models.BoolPtr(g.Display.HideDetailsPoster)
	}
	if eff.Display.ShowDetailsPrequeueFilename == nil {
		eff.Display.ShowDetailsPrequeueFilename = models.BoolPtr(g.Display.ShowDetailsPrequeueFilename)
	}
	if eff.Display.ShowDetailsRuntime == nil {
		eff.Display.ShowDetailsRuntime = models.BoolPtr(g.Display.ShowDetailsRuntime)
	}
	if eff.Display.ShowDetailsSelectedTracks == nil {
		eff.Display.ShowDetailsSelectedTracks = models.BoolPtr(g.Display.ShowDetailsSelectedTracks)
	}
	if eff.Display.HideTVDrawerRail == nil {
		eff.Display.HideTVDrawerRail = models.BoolPtr(g.Display.HideTVDrawerRail)
	}
	if eff.Display.SimpleMode == nil {
		eff.Display.SimpleMode = models.BoolPtr(g.Display.SimpleMode)
	}
	if eff.Display.SimpleModeHomeShelves == nil {
		eff.Display.SimpleModeHomeShelves = models.StringSlicePtr(g.Display.SimpleModeHomeShelves)
	}
	if eff.Display.DisableTVHomeCardDimming == nil {
		eff.Display.DisableTVHomeCardDimming = models.BoolPtr(g.Display.DisableTVHomeCardDimming)
	}
	if eff.Display.EnableAnimations == nil {
		eff.Display.EnableAnimations = models.BoolPtr(g.Display.EnableAnimations)
	}
	if eff.Display.EnableHeroArtPanning == nil {
		eff.Display.EnableHeroArtPanning = models.BoolPtr(g.Display.EnableHeroArtPanning)
	}
	if eff.Display.EnableHeroArtRotation == nil {
		eff.Display.EnableHeroArtRotation = models.BoolPtr(g.Display.EnableHeroArtRotation)
	}
	if eff.Display.ShowSeriesBackdropForMissingEpisodeArt == nil {
		eff.Display.ShowSeriesBackdropForMissingEpisodeArt = models.BoolPtr(g.Display.ShowSeriesBackdropForMissingEpisodeArt)
	}

	// AnimeFiltering
	if eff.AnimeFiltering.AnimeLanguageEnabled == nil {
		eff.AnimeFiltering.AnimeLanguageEnabled = models.BoolPtr(g.AnimeFiltering.AnimeLanguageEnabled)
	}
	if eff.AnimeFiltering.AnimePreferredLanguage == nil {
		eff.AnimeFiltering.AnimePreferredLanguage = models.StringPtr(g.AnimeFiltering.AnimePreferredLanguage)
	}

	// Display
	if eff.Display.BadgeVisibility == nil {
		eff.Display.BadgeVisibility = g.Display.BadgeVisibility
	}
	if eff.Display.NavigationTabVisibility == nil {
		eff.Display.NavigationTabVisibility = g.Display.NavigationTabVisibility
	}
	if eff.Display.WatchStateIconStyle == "" {
		eff.Display.WatchStateIconStyle = g.Display.WatchStateIconStyle
	}
	if eff.Display.IncludeUnreleasedMoviesInLists == nil {
		eff.Display.IncludeUnreleasedMoviesInLists = models.BoolPtr(g.Display.IncludeUnreleasedMoviesInLists)
	}
	if eff.Display.IncludeUnreleasedShowsInLists == nil {
		eff.Display.IncludeUnreleasedShowsInLists = models.BoolPtr(g.Display.IncludeUnreleasedShowsInLists)
	}
	if eff.Display.IncludeUnreleasedMoviesInSearch == nil {
		eff.Display.IncludeUnreleasedMoviesInSearch = models.BoolPtr(g.Display.IncludeUnreleasedMoviesInSearch)
	}
	if eff.Display.IncludeUnreleasedShowsInSearch == nil {
		eff.Display.IncludeUnreleasedShowsInSearch = models.BoolPtr(g.Display.IncludeUnreleasedShowsInSearch)
	}
	if eff.Display.AppLanguage == "" {
		eff.Display.AppLanguage = g.Display.AppLanguage
	}
	if eff.Display.Appearance.FontScale == nil {
		eff.Display.Appearance.FontScale = g.Display.Appearance.FontScale
	}
	if eff.Display.Appearance.AccentColor == "" {
		eff.Display.Appearance.AccentColor = g.Display.Appearance.AccentColor
	}
	if eff.Display.Appearance.TextColor == "" {
		eff.Display.Appearance.TextColor = g.Display.Appearance.TextColor
	}
	if eff.Display.Appearance.SecondaryTextColor == "" {
		eff.Display.Appearance.SecondaryTextColor = g.Display.Appearance.SecondaryTextColor
	}
	if eff.Display.Appearance.ModalBackgroundColor == "" {
		eff.Display.Appearance.ModalBackgroundColor = g.Display.Appearance.ModalBackgroundColor
	}
	if eff.Display.Appearance.ButtonStyle == "" {
		eff.Display.Appearance.ButtonStyle = g.Display.Appearance.ButtonStyle
	}
	if eff.Display.Appearance.ButtonRadius == "" {
		eff.Display.Appearance.ButtonRadius = g.Display.Appearance.ButtonRadius
	}
	if eff.Display.Appearance.HighContrast == nil {
		eff.Display.Appearance.HighContrast = g.Display.Appearance.HighContrast
	}
	if eff.Display.Appearance.ReduceOverlays == nil {
		eff.Display.Appearance.ReduceOverlays = g.Display.Appearance.ReduceOverlays
	}

	// HomeShelves
	eff.HomeShelves.Views = config.MergeHomeViews(g.HomeShelves.Views, eff.HomeShelves.Views)
	if len(eff.HomeShelves.Shelves) == 0 {
		eff.HomeShelves.Shelves = configShelvesToModel(g.HomeShelves.Shelves)
	} else {
		eff.HomeShelves.Shelves = mergeShelvesWithGlobal(eff.HomeShelves.Shelves, g.HomeShelves.Shelves)
	}
	if eff.HomeShelves.ExploreCardPosition == "" {
		eff.HomeShelves.ExploreCardPosition = string(g.HomeShelves.ExploreCardPosition)
	}
	if eff.HomeShelves.ItemCap <= 0 {
		eff.HomeShelves.ItemCap = g.HomeShelves.ItemCap
	}
	if eff.HomeShelves.HomeShelfFocusModel == "" {
		eff.HomeShelves.HomeShelfFocusModel = g.HomeShelves.HomeShelfFocusModel
	}
	if eff.HomeShelves.HomeShelfFocusModel != "center" && eff.HomeShelves.HomeShelfFocusModel != "right" {
		eff.HomeShelves.HomeShelfFocusModel = "left"
	}
	if eff.HomeShelves.MobileTopShelfMode == "" {
		eff.HomeShelves.MobileTopShelfMode = g.HomeShelves.MobileTopShelfMode
	}
	if eff.HomeShelves.MobileTopShelfSourceID == "" {
		eff.HomeShelves.MobileTopShelfSourceID = g.HomeShelves.MobileTopShelfSourceID
	}
	if eff.HomeShelves.TVTopShelfMode == "" {
		eff.HomeShelves.TVTopShelfMode = g.HomeShelves.TVTopShelfMode
	}
	if eff.HomeShelves.TVTopShelfSourceID == "" {
		eff.HomeShelves.TVTopShelfSourceID = g.HomeShelves.TVTopShelfSourceID
	}
	if eff.HomeShelves.DisableTvLandscapeCardExpansion == nil {
		eff.HomeShelves.DisableTvLandscapeCardExpansion = models.BoolPtr(g.HomeShelves.DisableTvLandscapeCardExpansion)
	}
	if eff.HomeShelves.HomeShelfScale == nil || *eff.HomeShelves.HomeShelfScale <= 0 {
		eff.HomeShelves.HomeShelfScale = models.FloatPtr(g.HomeShelves.HomeShelfScale)
	}
	if eff.HomeShelves.HomeShelfScale != nil {
		if *eff.HomeShelves.HomeShelfScale < 0.5 {
			eff.HomeShelves.HomeShelfScale = models.FloatPtr(0.5)
		} else if *eff.HomeShelves.HomeShelfScale > 1.0 {
			eff.HomeShelves.HomeShelfScale = models.FloatPtr(1.0)
		}
	}
	if eff.HomeShelves.HomeHeroScale == nil || *eff.HomeShelves.HomeHeroScale <= 0 {
		eff.HomeShelves.HomeHeroScale = models.FloatPtr(g.HomeShelves.HomeHeroScale)
	}
	if eff.HomeShelves.HomeHeroScale != nil {
		if *eff.HomeShelves.HomeHeroScale < 0.5 {
			eff.HomeShelves.HomeHeroScale = models.FloatPtr(0.5)
		} else if *eff.HomeShelves.HomeHeroScale > 1.0 {
			eff.HomeShelves.HomeHeroScale = models.FloatPtr(1.0)
		}
	}

	// Network
	if eff.Network.HomeWifiSSID == "" {
		eff.Network.HomeWifiSSID = g.Network.HomeWifiSSID
	}
	if eff.Network.HomeBackendUrl == "" {
		eff.Network.HomeBackendUrl = g.Network.HomeBackendUrl
	}
	if eff.Network.RemoteBackendUrl == "" {
		eff.Network.RemoteBackendUrl = g.Network.RemoteBackendUrl
	}

	return eff
}

// stripProfileSettings removes fields from a profile that match the global settings.
// Returns true if anything was stripped.
func (s *Service) stripProfileSettings(us *models.UserSettings, g config.Settings) bool {
	changed := false
	changed = stripPlayback(&us.Playback, g.Playback) || changed
	changed = stripFiltering(&us.Filtering, g.Filtering) || changed
	changed = stripHomeShelves(&us.HomeShelves, g.HomeShelves) || changed
	changed = stripDisplay(&us.Display, g.Display) || changed
	changed = stripAnimeFiltering(&us.AnimeFiltering, g.AnimeFiltering) || changed
	changed = stripNetwork(&us.Network, g.Network) || changed
	changed = stripMetadata(&us.Metadata, g.Metadata) || changed
	changed = stripRanking(&us.Ranking, g.Ranking) || changed
	// LiveTV channel lists (HiddenChannels, FavoriteChannels, SelectedCategories) are inherently per-user — never strip.
	// Calendar has no global equivalent — never strip.
	return changed
}

func stripMetadata(m *models.MetadataSettings, g config.MetadataSettings) bool {
	if m.PrimaryLanguage != "" && m.PrimaryLanguage == g.EffectivePrimaryLanguage() {
		m.PrimaryLanguage = ""
		return true
	}
	return false
}

func stripPlayback(p *models.PlaybackSettings, g config.PlaybackSettings) bool {
	changed := false
	if p.PreferredPlayer != "" && p.PreferredPlayer == g.PreferredPlayer {
		p.PreferredPlayer = ""
		changed = true
	}
	if p.PreferredAudioLanguage != "" && p.PreferredAudioLanguage == g.PreferredAudioLanguage {
		p.PreferredAudioLanguage = ""
		changed = true
	}
	if p.PreferredSubtitleLanguage != "" && p.PreferredSubtitleLanguage == g.PreferredSubtitleLanguage {
		p.PreferredSubtitleLanguage = ""
		changed = true
	}
	if p.AllowedTrackLanguages != nil && slices.Equal(*p.AllowedTrackLanguages, g.AllowedTrackLanguages) {
		p.AllowedTrackLanguages = nil
		changed = true
	}
	if p.PreferredSubtitleMode != "" && p.PreferredSubtitleMode == g.PreferredSubtitleMode {
		p.PreferredSubtitleMode = ""
		changed = true
	}
	if p.PauseWhenAppInactive != nil && *p.PauseWhenAppInactive == g.PauseWhenAppInactive {
		p.PauseWhenAppInactive = nil
		changed = true
	}
	if p.SubtitleSize != 0 && p.SubtitleSize == g.SubtitleSize {
		p.SubtitleSize = 0
		changed = true
	}
	if p.SubtitleUseCropDetectPosition != nil && *p.SubtitleUseCropDetectPosition == g.SubtitleUseCropDetectPosition {
		p.SubtitleUseCropDetectPosition = nil
		changed = true
	}
	if p.SubtitleColor != "" && p.SubtitleColor == g.SubtitleColor {
		p.SubtitleColor = ""
		changed = true
	}
	if p.SubtitleOpacity != nil && *p.SubtitleOpacity == g.SubtitleOpacity {
		p.SubtitleOpacity = nil
		changed = true
	}
	if p.SubtitleFont != nil && *p.SubtitleFont == g.SubtitleFont {
		p.SubtitleFont = nil
		changed = true
	}
	if p.SubtitleBold != nil && *p.SubtitleBold == g.SubtitleBold {
		p.SubtitleBold = nil
		changed = true
	}
	if p.SubtitleOutlineEnabled != nil && *p.SubtitleOutlineEnabled == g.SubtitleOutlineEnabled {
		p.SubtitleOutlineEnabled = nil
		changed = true
	}
	if p.SubtitleOutlineColor != "" && p.SubtitleOutlineColor == g.SubtitleOutlineColor {
		p.SubtitleOutlineColor = ""
		changed = true
	}
	if p.SubtitleOutlineWeight != nil && *p.SubtitleOutlineWeight == g.SubtitleOutlineWeight {
		p.SubtitleOutlineWeight = nil
		changed = true
	}
	if p.SubtitleBackgroundEnabled != nil && *p.SubtitleBackgroundEnabled == g.SubtitleBackgroundEnabled {
		p.SubtitleBackgroundEnabled = nil
		changed = true
	}
	if p.SubtitleBackgroundColor != "" && p.SubtitleBackgroundColor == g.SubtitleBackgroundColor {
		p.SubtitleBackgroundColor = ""
		changed = true
	}
	if p.SubtitleBackgroundOpacity != nil && *p.SubtitleBackgroundOpacity == g.SubtitleBackgroundOpacity {
		p.SubtitleBackgroundOpacity = nil
		changed = true
	}
	if p.SeekForwardSeconds != 0 && p.SeekForwardSeconds == g.SeekForwardSeconds {
		p.SeekForwardSeconds = 0
		changed = true
	}
	if p.SeekBackwardSeconds != 0 && p.SeekBackwardSeconds == g.SeekBackwardSeconds {
		p.SeekBackwardSeconds = 0
		changed = true
	}
	if p.ForceAACTranscoding != nil && *p.ForceAACTranscoding == g.ForceAACTranscoding {
		p.ForceAACTranscoding = nil
		changed = true
	}
	if p.AutoPlayTrailersTV != nil && *p.AutoPlayTrailersTV == g.AutoPlayTrailersTV {
		p.AutoPlayTrailersTV = nil
		changed = true
	}
	if p.UseLoadingScreen != nil && *p.UseLoadingScreen == g.UseLoadingScreen {
		p.UseLoadingScreen = nil
		changed = true
	}
	if p.RewindOnResumeFromPause != nil && *p.RewindOnResumeFromPause == g.RewindOnResumeFromPause {
		p.RewindOnResumeFromPause = nil
		changed = true
	}
	if p.RewindOnPlaybackStart != nil && *p.RewindOnPlaybackStart == g.RewindOnPlaybackStart {
		p.RewindOnPlaybackStart = nil
		changed = true
	}
	globalCreditsAutoSkip := g.CreditsAutoSkip || g.CreditsDetection
	if p.CreditsAutoSkip != nil && *p.CreditsAutoSkip == globalCreditsAutoSkip {
		p.CreditsAutoSkip = nil
		changed = true
	}
	if p.CreditsDetectionEnabled != nil && *p.CreditsDetectionEnabled == g.CreditsDetectionEnabled {
		p.CreditsDetectionEnabled = nil
		changed = true
	}
	if p.ScrobbleStartDelaySeconds != nil && *p.ScrobbleStartDelaySeconds == g.ScrobbleStartDelaySeconds {
		p.ScrobbleStartDelaySeconds = nil
		changed = true
	}
	if p.StreamMigrationEnabled != nil && *p.StreamMigrationEnabled == g.StreamMigrationEnabled {
		p.StreamMigrationEnabled = nil
		changed = true
	}
	if p.MatchFrameRate != nil && *p.MatchFrameRate == g.MatchFrameRate {
		p.MatchFrameRate = nil
		changed = true
	}
	if p.LiveClosedCaptionExtraction != nil && *p.LiveClosedCaptionExtraction == g.LiveClosedCaptionExtraction {
		p.LiveClosedCaptionExtraction = nil
		changed = true
	}
	if p.DisablePrequeue != nil && *p.DisablePrequeue == g.DisablePrequeue {
		p.DisablePrequeue = nil
		changed = true
	}
	if p.PrerollMode != "" && p.PrerollMode == g.PrerollMode && p.PrerollAssetID == g.PrerollAssetID {
		p.PrerollMode = ""
		p.PrerollAssetID = ""
		changed = true
	}
	if p.PrerollMediaScope != "" && p.PrerollMediaScope == g.PrerollMediaScope {
		p.PrerollMediaScope = ""
		changed = true
	}
	if p.PrerollSkipIfPrequeueReady != nil && *p.PrerollSkipIfPrequeueReady == g.PrerollSkipIfPrequeueReady {
		p.PrerollSkipIfPrequeueReady = nil
		changed = true
	}
	if p.IgnoreDVCompatibilityCheck != nil && *p.IgnoreDVCompatibilityCheck == g.IgnoreDVCompatibilityCheck {
		p.IgnoreDVCompatibilityCheck = nil
		changed = true
	}
	if p.MaxResultsPerResolution != nil && *p.MaxResultsPerResolution == g.MaxResultsPerResolution {
		p.MaxResultsPerResolution = nil
		changed = true
	}
	return changed
}

func stripFiltering(f *models.FilterSettings, g config.FilterSettings) bool {
	changed := false
	if f.MaxSizeMovieGB != nil && *f.MaxSizeMovieGB == g.MaxSizeMovieGB {
		f.MaxSizeMovieGB = nil
		changed = true
	}
	if f.MaxSizeEpisodeGB != nil && *f.MaxSizeEpisodeGB == g.MaxSizeEpisodeGB {
		f.MaxSizeEpisodeGB = nil
		changed = true
	}
	if f.MaxResolution != nil && *f.MaxResolution == g.MaxResolution {
		f.MaxResolution = nil
		changed = true
	}
	if f.HDRDVPolicy != "" && string(f.HDRDVPolicy) == string(g.HDRDVPolicy) {
		f.HDRDVPolicy = ""
		changed = true
	}
	if f.RequiredTerms != nil && stringSliceEqualUnordered(f.RequiredTerms, g.RequiredTerms) {
		f.RequiredTerms = nil
		changed = true
	}
	if f.FilterOutTerms != nil && stringSliceEqualUnordered(f.FilterOutTerms, g.FilterOutTerms) {
		f.FilterOutTerms = nil
		changed = true
	}
	if f.PreferredTerms != nil && stringSliceEqualUnordered(f.PreferredTerms, g.PreferredTerms) {
		f.PreferredTerms = nil
		changed = true
	}
	if f.NonPreferredTerms != nil && stringSliceEqualUnordered(f.NonPreferredTerms, g.NonPreferredTerms) {
		f.NonPreferredTerms = nil
		changed = true
	}
	if f.DownloadPreferredTerms != nil && stringSliceEqualUnordered(f.DownloadPreferredTerms, g.DownloadPreferredTerms) {
		f.DownloadPreferredTerms = nil
		changed = true
	}
	if f.PreferredScraper != nil && *f.PreferredScraper == g.PreferredScraper {
		f.PreferredScraper = nil
		changed = true
	}
	if f.ServicePriority != nil && *f.ServicePriority == string(g.ServicePriority) {
		f.ServicePriority = nil
		changed = true
	}
	if f.UnknownTrackPolicy != "" && f.UnknownTrackPolicy == string(g.UnknownTrackPolicy) {
		f.UnknownTrackPolicy = ""
		changed = true
	}
	if f.AdaptivePlaybackEnabled != nil && *f.AdaptivePlaybackEnabled == g.AdaptivePlaybackEnabled {
		f.AdaptivePlaybackEnabled = nil
		changed = true
	}
	if f.AdaptiveTargetBufferFactor != nil && *f.AdaptiveTargetBufferFactor == g.AdaptiveTargetBufferFactor {
		f.AdaptiveTargetBufferFactor = nil
		changed = true
	}
	if f.RealDebridRestrictedTermsFilterEnabled != nil && *f.RealDebridRestrictedTermsFilterEnabled == g.RealDebridRestrictedTermsFilterEnabled {
		f.RealDebridRestrictedTermsFilterEnabled = nil
		changed = true
	}
	return changed
}

func stripHomeShelves(h *models.HomeShelvesSettings, g config.HomeShelvesSettings) bool {
	changed := false
	if len(h.Shelves) == 0 {
		// continue checking scalar home shelf options
	} else if shelfConfigsEqual(h.Shelves, g.Shelves) {
		h.Shelves = nil
		changed = true
	}
	if h.ExploreCardPosition != "" && h.ExploreCardPosition == string(g.ExploreCardPosition) {
		h.ExploreCardPosition = ""
		changed = true
	}
	if h.ItemCap != 0 && h.ItemCap == g.ItemCap {
		h.ItemCap = 0
		changed = true
	}
	if h.HomeShelfFocusModel != "" && h.HomeShelfFocusModel == g.HomeShelfFocusModel {
		h.HomeShelfFocusModel = ""
		changed = true
	}
	if h.MobileTopShelfMode != "" && normalizeHomeTopShelfMode(h.MobileTopShelfMode) == normalizeHomeTopShelfMode(g.MobileTopShelfMode) {
		h.MobileTopShelfMode = ""
		changed = true
	}
	if h.MobileTopShelfSourceID != "" && h.MobileTopShelfSourceID == g.MobileTopShelfSourceID {
		h.MobileTopShelfSourceID = ""
		changed = true
	}
	if h.TVTopShelfMode != "" && normalizeHomeTopShelfMode(h.TVTopShelfMode) == normalizeHomeTopShelfMode(g.TVTopShelfMode) {
		h.TVTopShelfMode = ""
		changed = true
	}
	if h.TVTopShelfSourceID != "" && h.TVTopShelfSourceID == g.TVTopShelfSourceID {
		h.TVTopShelfSourceID = ""
		changed = true
	}
	if h.DisableTvLandscapeCardExpansion != nil && *h.DisableTvLandscapeCardExpansion == g.DisableTvLandscapeCardExpansion {
		h.DisableTvLandscapeCardExpansion = nil
		changed = true
	}
	if h.HomeShelfScale != nil && *h.HomeShelfScale == g.HomeShelfScale {
		h.HomeShelfScale = nil
		changed = true
	}
	if h.HomeHeroScale != nil && *h.HomeHeroScale == g.HomeHeroScale {
		h.HomeHeroScale = nil
		changed = true
	}
	return changed
}

func normalizeHomeTopShelfMode(mode string) string {
	if mode == "" {
		return "default"
	}
	return mode
}

func stripDisplay(d *models.DisplaySettings, g config.DisplaySettings) bool {
	changed := false
	if d.BadgeVisibility != nil && stringSliceEqualOrdered(d.BadgeVisibility, g.BadgeVisibility) {
		d.BadgeVisibility = nil
		changed = true
	}
	if d.NavigationTabVisibility != nil && stringSliceEqualUnordered(d.NavigationTabVisibility, g.NavigationTabVisibility) {
		d.NavigationTabVisibility = nil
		changed = true
	}
	if d.WatchStateIconStyle != "" && d.WatchStateIconStyle == g.WatchStateIconStyle {
		d.WatchStateIconStyle = ""
		changed = true
	}
	if d.IncludeUnreleasedMoviesInLists != nil && *d.IncludeUnreleasedMoviesInLists == g.IncludeUnreleasedMoviesInLists {
		d.IncludeUnreleasedMoviesInLists = nil
		changed = true
	}
	if d.IncludeUnreleasedShowsInLists != nil && *d.IncludeUnreleasedShowsInLists == g.IncludeUnreleasedShowsInLists {
		d.IncludeUnreleasedShowsInLists = nil
		changed = true
	}
	if d.IncludeUnreleasedMoviesInSearch != nil && *d.IncludeUnreleasedMoviesInSearch == g.IncludeUnreleasedMoviesInSearch {
		d.IncludeUnreleasedMoviesInSearch = nil
		changed = true
	}
	if d.IncludeUnreleasedShowsInSearch != nil && *d.IncludeUnreleasedShowsInSearch == g.IncludeUnreleasedShowsInSearch {
		d.IncludeUnreleasedShowsInSearch = nil
		changed = true
	}
	if d.BypassFilteringForAIOStreamsOnly != nil && *d.BypassFilteringForAIOStreamsOnly == g.BypassFilteringForAIOStreamsOnly {
		d.BypassFilteringForAIOStreamsOnly = nil
		changed = true
	}
	if d.ShowStreamSourceInfo != nil && *d.ShowStreamSourceInfo == g.ShowStreamSourceInfo {
		d.ShowStreamSourceInfo = nil
		changed = true
	}
	if d.DisableMobileTopCarousel != nil && *d.DisableMobileTopCarousel == g.DisableMobileTopCarousel {
		d.DisableMobileTopCarousel = nil
		changed = true
	}
	if d.HideContinueWatchingHeroMetadata != nil && *d.HideContinueWatchingHeroMetadata == g.HideContinueWatchingHeroMetadata {
		d.HideContinueWatchingHeroMetadata = nil
		changed = true
	}
	if d.MoveDetailsRatingsToMetadata != nil && *d.MoveDetailsRatingsToMetadata == g.MoveDetailsRatingsToMetadata {
		d.MoveDetailsRatingsToMetadata = nil
		changed = true
	}
	if d.HideDetailsPoster != nil && *d.HideDetailsPoster == g.HideDetailsPoster {
		d.HideDetailsPoster = nil
		changed = true
	}
	if d.ShowDetailsPrequeueFilename != nil && *d.ShowDetailsPrequeueFilename == g.ShowDetailsPrequeueFilename {
		d.ShowDetailsPrequeueFilename = nil
		changed = true
	}
	if d.ShowDetailsRuntime != nil && *d.ShowDetailsRuntime == g.ShowDetailsRuntime {
		d.ShowDetailsRuntime = nil
		changed = true
	}
	if d.ShowDetailsSelectedTracks != nil && *d.ShowDetailsSelectedTracks == g.ShowDetailsSelectedTracks {
		d.ShowDetailsSelectedTracks = nil
		changed = true
	}
	if d.HideTVDrawerRail != nil && *d.HideTVDrawerRail == g.HideTVDrawerRail {
		d.HideTVDrawerRail = nil
		changed = true
	}
	if d.SimpleMode != nil && *d.SimpleMode == g.SimpleMode {
		d.SimpleMode = nil
		changed = true
	}
	if d.SimpleModeHomeShelves != nil && stringSliceEqualOrdered(*d.SimpleModeHomeShelves, g.SimpleModeHomeShelves) {
		d.SimpleModeHomeShelves = nil
		changed = true
	}
	if d.DisableTVHomeCardDimming != nil && *d.DisableTVHomeCardDimming == g.DisableTVHomeCardDimming {
		d.DisableTVHomeCardDimming = nil
		changed = true
	}
	if d.EnableAnimations != nil && *d.EnableAnimations == g.EnableAnimations {
		d.EnableAnimations = nil
		changed = true
	}
	if d.EnableHeroArtPanning != nil && *d.EnableHeroArtPanning == g.EnableHeroArtPanning {
		d.EnableHeroArtPanning = nil
		changed = true
	}
	if d.EnableHeroArtRotation != nil && *d.EnableHeroArtRotation == g.EnableHeroArtRotation {
		d.EnableHeroArtRotation = nil
		changed = true
	}
	if d.ShowSeriesBackdropForMissingEpisodeArt != nil && *d.ShowSeriesBackdropForMissingEpisodeArt == g.ShowSeriesBackdropForMissingEpisodeArt {
		d.ShowSeriesBackdropForMissingEpisodeArt = nil
		changed = true
	}
	if d.AppLanguage != "" && d.AppLanguage == g.AppLanguage {
		d.AppLanguage = ""
		changed = true
	}
	if d.Appearance.FontScale != nil && g.Appearance.FontScale != nil && *d.Appearance.FontScale == *g.Appearance.FontScale {
		d.Appearance.FontScale = nil
		changed = true
	}
	if d.Appearance.AccentColor != "" && d.Appearance.AccentColor == g.Appearance.AccentColor {
		d.Appearance.AccentColor = ""
		changed = true
	}
	if d.Appearance.TextColor != "" && d.Appearance.TextColor == g.Appearance.TextColor {
		d.Appearance.TextColor = ""
		changed = true
	}
	if d.Appearance.SecondaryTextColor != "" && d.Appearance.SecondaryTextColor == g.Appearance.SecondaryTextColor {
		d.Appearance.SecondaryTextColor = ""
		changed = true
	}
	if d.Appearance.BackgroundColor != "" && d.Appearance.BackgroundColor == g.Appearance.BackgroundColor {
		d.Appearance.BackgroundColor = ""
		changed = true
	}
	if d.Appearance.ModalBackgroundColor != "" && d.Appearance.ModalBackgroundColor == g.Appearance.ModalBackgroundColor {
		d.Appearance.ModalBackgroundColor = ""
		changed = true
	}
	if d.Appearance.ButtonStyle != "" && d.Appearance.ButtonStyle == g.Appearance.ButtonStyle {
		d.Appearance.ButtonStyle = ""
		changed = true
	}
	if d.Appearance.ButtonRadius != "" && d.Appearance.ButtonRadius == g.Appearance.ButtonRadius {
		d.Appearance.ButtonRadius = ""
		changed = true
	}
	if d.Appearance.HighContrast != nil && g.Appearance.HighContrast != nil && *d.Appearance.HighContrast == *g.Appearance.HighContrast {
		d.Appearance.HighContrast = nil
		changed = true
	}
	if d.Appearance.ReduceOverlays != nil && g.Appearance.ReduceOverlays != nil && *d.Appearance.ReduceOverlays == *g.Appearance.ReduceOverlays {
		d.Appearance.ReduceOverlays = nil
		changed = true
	}
	return changed
}

func stripAnimeFiltering(a *models.AnimeFilteringSettings, g config.AnimeFilteringSettings) bool {
	changed := false
	if a.AnimeLanguageEnabled != nil && *a.AnimeLanguageEnabled == g.AnimeLanguageEnabled {
		a.AnimeLanguageEnabled = nil
		changed = true
	}
	if a.AnimePreferredLanguage != nil && *a.AnimePreferredLanguage == g.AnimePreferredLanguage {
		a.AnimePreferredLanguage = nil
		changed = true
	}
	return changed
}

func stripNetwork(n *models.NetworkSettings, g config.NetworkSettings) bool {
	// Network settings are inherently per-device — skip stripping
	return false
}

func stripRanking(r **models.UserRankingSettings, g config.RankingSettings) bool {
	if *r == nil {
		return false
	}

	changed := false
	if len((*r).Criteria) > 0 && userRankingCriteriaMatch((*r).Criteria, g.Criteria) {
		(*r).Criteria = nil
		changed = true
	}
	if (*r).SplitByService != nil && *(*r).SplitByService == g.SplitByService {
		(*r).SplitByService = nil
		changed = true
	}
	if (*r).NewestReleaseFirst != nil && *(*r).NewestReleaseFirst == g.NewestReleaseFirst {
		(*r).NewestReleaseFirst = nil
		changed = true
	}

	debridGlobal := g
	if g.SplitByService && g.Debrid != nil {
		debridGlobal = *g.Debrid
	}
	if stripRanking(&(*r).Debrid, debridGlobal) {
		changed = true
	}

	usenetGlobal := g
	if g.SplitByService && g.Usenet != nil {
		usenetGlobal = *g.Usenet
	}
	if stripRanking(&(*r).Usenet, usenetGlobal) {
		changed = true
	}

	if userRankingSettingsEmpty(*r) {
		*r = nil
		changed = true
	}
	return changed
}

func userRankingCriteriaMatch(criteria []models.UserRankingCriterion, global []config.RankingCriterion) bool {
	// Each profile criterion is an override, so it is redundant when every value
	// it specifies already matches the inherited criterion with the same ID.
	globalByID := make(map[config.RankingCriterionID]config.RankingCriterion)
	for _, gc := range global {
		globalByID[gc.ID] = gc
	}
	for _, uc := range criteria {
		gc, ok := globalByID[uc.ID]
		if !ok {
			return false
		}
		if uc.Enabled != nil && *uc.Enabled != gc.Enabled {
			return false
		}
		if uc.Order != nil && *uc.Order != gc.Order {
			return false
		}
	}
	return true
}

func userRankingSettingsEmpty(r *models.UserRankingSettings) bool {
	return r == nil || (len(r.Criteria) == 0 && r.NewestReleaseFirst == nil && r.SplitByService == nil && r.Debrid == nil && r.Usenet == nil)
}

// stripClientSettings removes client overrides that match their parent profile's effective value.
func stripClientSettings(cs *models.ClientFilterSettings, eff models.UserSettings) bool {
	changed := false

	// Home screen
	if cs.HomeShelfFocusModel != nil && *cs.HomeShelfFocusModel == eff.HomeShelves.HomeShelfFocusModel {
		cs.HomeShelfFocusModel = nil
		changed = true
	}

	// Playback
	if cs.PreferredPlayer != nil && *cs.PreferredPlayer == eff.Playback.PreferredPlayer {
		cs.PreferredPlayer = nil
		changed = true
	}
	if cs.PreferredAudioLanguage != nil && *cs.PreferredAudioLanguage == eff.Playback.PreferredAudioLanguage {
		cs.PreferredAudioLanguage = nil
		changed = true
	}
	if cs.PreferredSubtitleLanguage != nil && *cs.PreferredSubtitleLanguage == eff.Playback.PreferredSubtitleLanguage {
		cs.PreferredSubtitleLanguage = nil
		changed = true
	}
	if cs.AllowedTrackLanguages != nil && eff.Playback.AllowedTrackLanguages != nil && slices.Equal(*cs.AllowedTrackLanguages, *eff.Playback.AllowedTrackLanguages) {
		cs.AllowedTrackLanguages = nil
		changed = true
	}
	if cs.PreferredSubtitleMode != nil && *cs.PreferredSubtitleMode == eff.Playback.PreferredSubtitleMode {
		cs.PreferredSubtitleMode = nil
		changed = true
	}
	if cs.PauseWhenAppInactive != nil && eff.Playback.PauseWhenAppInactive != nil && *cs.PauseWhenAppInactive == *eff.Playback.PauseWhenAppInactive {
		cs.PauseWhenAppInactive = nil
		changed = true
	}
	if cs.UseLoadingScreen != nil && eff.Playback.UseLoadingScreen != nil && *cs.UseLoadingScreen == *eff.Playback.UseLoadingScreen {
		cs.UseLoadingScreen = nil
		changed = true
	}
	if cs.SubtitleSize != nil && *cs.SubtitleSize == eff.Playback.SubtitleSize {
		cs.SubtitleSize = nil
		changed = true
	}
	if cs.SubtitleUseCropDetectPosition != nil && eff.Playback.SubtitleUseCropDetectPosition != nil && *cs.SubtitleUseCropDetectPosition == *eff.Playback.SubtitleUseCropDetectPosition {
		cs.SubtitleUseCropDetectPosition = nil
		changed = true
	}
	if cs.SubtitleColor != nil && *cs.SubtitleColor == eff.Playback.SubtitleColor {
		cs.SubtitleColor = nil
		changed = true
	}
	if cs.SubtitleOpacity != nil && eff.Playback.SubtitleOpacity != nil && *cs.SubtitleOpacity == *eff.Playback.SubtitleOpacity {
		cs.SubtitleOpacity = nil
		changed = true
	}
	if cs.SubtitleFont != nil && eff.Playback.SubtitleFont != nil && *cs.SubtitleFont == *eff.Playback.SubtitleFont {
		cs.SubtitleFont = nil
		changed = true
	}
	if cs.SubtitleBold != nil && eff.Playback.SubtitleBold != nil && *cs.SubtitleBold == *eff.Playback.SubtitleBold {
		cs.SubtitleBold = nil
		changed = true
	}
	if cs.SubtitleOutlineEnabled != nil && eff.Playback.SubtitleOutlineEnabled != nil && *cs.SubtitleOutlineEnabled == *eff.Playback.SubtitleOutlineEnabled {
		cs.SubtitleOutlineEnabled = nil
		changed = true
	}
	if cs.SubtitleOutlineColor != nil && *cs.SubtitleOutlineColor == eff.Playback.SubtitleOutlineColor {
		cs.SubtitleOutlineColor = nil
		changed = true
	}
	if cs.SubtitleOutlineWeight != nil && eff.Playback.SubtitleOutlineWeight != nil && *cs.SubtitleOutlineWeight == *eff.Playback.SubtitleOutlineWeight {
		cs.SubtitleOutlineWeight = nil
		changed = true
	}
	if cs.SubtitleBackgroundEnabled != nil && eff.Playback.SubtitleBackgroundEnabled != nil && *cs.SubtitleBackgroundEnabled == *eff.Playback.SubtitleBackgroundEnabled {
		cs.SubtitleBackgroundEnabled = nil
		changed = true
	}
	if cs.SubtitleBackgroundColor != nil && *cs.SubtitleBackgroundColor == eff.Playback.SubtitleBackgroundColor {
		cs.SubtitleBackgroundColor = nil
		changed = true
	}
	if cs.SubtitleBackgroundOpacity != nil && eff.Playback.SubtitleBackgroundOpacity != nil && *cs.SubtitleBackgroundOpacity == *eff.Playback.SubtitleBackgroundOpacity {
		cs.SubtitleBackgroundOpacity = nil
		changed = true
	}
	if cs.SeekForwardSeconds != nil && *cs.SeekForwardSeconds == eff.Playback.SeekForwardSeconds {
		cs.SeekForwardSeconds = nil
		changed = true
	}
	if cs.SeekBackwardSeconds != nil && *cs.SeekBackwardSeconds == eff.Playback.SeekBackwardSeconds {
		cs.SeekBackwardSeconds = nil
		changed = true
	}
	if cs.ForceAACTranscoding != nil && eff.Playback.ForceAACTranscoding != nil && *cs.ForceAACTranscoding == *eff.Playback.ForceAACTranscoding {
		cs.ForceAACTranscoding = nil
		changed = true
	}
	if cs.AutoPlayTrailersTV != nil && eff.Playback.AutoPlayTrailersTV != nil && *cs.AutoPlayTrailersTV == *eff.Playback.AutoPlayTrailersTV {
		cs.AutoPlayTrailersTV = nil
		changed = true
	}
	if cs.RewindOnResumeFromPause != nil && eff.Playback.RewindOnResumeFromPause != nil && *cs.RewindOnResumeFromPause == *eff.Playback.RewindOnResumeFromPause {
		cs.RewindOnResumeFromPause = nil
		changed = true
	}
	if cs.RewindOnPlaybackStart != nil && eff.Playback.RewindOnPlaybackStart != nil && *cs.RewindOnPlaybackStart == *eff.Playback.RewindOnPlaybackStart {
		cs.RewindOnPlaybackStart = nil
		changed = true
	}
	if cs.IgnoreDVCompatibilityCheck != nil && eff.Playback.IgnoreDVCompatibilityCheck != nil && *cs.IgnoreDVCompatibilityCheck == *eff.Playback.IgnoreDVCompatibilityCheck {
		cs.IgnoreDVCompatibilityCheck = nil
		changed = true
	}
	if cs.CreditsDetectionEnabled != nil && eff.Playback.CreditsDetectionEnabled != nil && *cs.CreditsDetectionEnabled == *eff.Playback.CreditsDetectionEnabled {
		cs.CreditsDetectionEnabled = nil
		changed = true
	}
	if cs.CreditsAutoSkip != nil && eff.Playback.CreditsAutoSkip != nil && *cs.CreditsAutoSkip == *eff.Playback.CreditsAutoSkip {
		cs.CreditsAutoSkip = nil
		changed = true
	}
	if cs.ScrobbleStartDelaySeconds != nil && eff.Playback.ScrobbleStartDelaySeconds != nil && *cs.ScrobbleStartDelaySeconds == *eff.Playback.ScrobbleStartDelaySeconds {
		cs.ScrobbleStartDelaySeconds = nil
		changed = true
	}
	if cs.StreamMigrationEnabled != nil && eff.Playback.StreamMigrationEnabled != nil && *cs.StreamMigrationEnabled == *eff.Playback.StreamMigrationEnabled {
		cs.StreamMigrationEnabled = nil
		changed = true
	}
	if cs.MatchFrameRate != nil && eff.Playback.MatchFrameRate != nil && *cs.MatchFrameRate == *eff.Playback.MatchFrameRate {
		cs.MatchFrameRate = nil
		changed = true
	}
	if cs.LiveClosedCaptionExtraction != nil && eff.Playback.LiveClosedCaptionExtraction != nil && *cs.LiveClosedCaptionExtraction == *eff.Playback.LiveClosedCaptionExtraction {
		cs.LiveClosedCaptionExtraction = nil
		changed = true
	}
	if cs.DisablePrequeue != nil && eff.Playback.DisablePrequeue != nil && *cs.DisablePrequeue == *eff.Playback.DisablePrequeue {
		cs.DisablePrequeue = nil
		changed = true
	}
	if cs.PrerollMode != nil && *cs.PrerollMode == eff.Playback.PrerollMode {
		assetMatches := cs.PrerollAssetID == nil || *cs.PrerollAssetID == eff.Playback.PrerollAssetID
		if assetMatches {
			cs.PrerollMode = nil
			cs.PrerollAssetID = nil
			changed = true
		}
	}
	if cs.PrerollMediaScope != nil && *cs.PrerollMediaScope == eff.Playback.PrerollMediaScope {
		cs.PrerollMediaScope = nil
		changed = true
	}
	if cs.PrerollSkipIfPrequeueReady != nil && eff.Playback.PrerollSkipIfPrequeueReady != nil &&
		*cs.PrerollSkipIfPrequeueReady == *eff.Playback.PrerollSkipIfPrequeueReady {
		cs.PrerollSkipIfPrequeueReady = nil
		changed = true
	}
	if cs.MaxResultsPerResolution != nil && eff.Playback.MaxResultsPerResolution != nil && *cs.MaxResultsPerResolution == *eff.Playback.MaxResultsPerResolution {
		cs.MaxResultsPerResolution = nil
		changed = true
	}

	// Filtering
	if cs.MaxSizeMovieGB != nil && eff.Filtering.MaxSizeMovieGB != nil && *cs.MaxSizeMovieGB == *eff.Filtering.MaxSizeMovieGB {
		cs.MaxSizeMovieGB = nil
		changed = true
	}
	if cs.MaxSizeEpisodeGB != nil && eff.Filtering.MaxSizeEpisodeGB != nil && *cs.MaxSizeEpisodeGB == *eff.Filtering.MaxSizeEpisodeGB {
		cs.MaxSizeEpisodeGB = nil
		changed = true
	}
	if cs.MaxResolution != nil && eff.Filtering.MaxResolution != nil && *cs.MaxResolution == *eff.Filtering.MaxResolution {
		cs.MaxResolution = nil
		changed = true
	}
	if cs.HDRDVPolicy != nil && string(*cs.HDRDVPolicy) == string(eff.Filtering.HDRDVPolicy) {
		cs.HDRDVPolicy = nil
		changed = true
	}
	if cs.RequiredTerms != nil && stringSliceEqualUnordered(*cs.RequiredTerms, eff.Filtering.RequiredTerms) {
		cs.RequiredTerms = nil
		changed = true
	}
	if cs.FilterOutTerms != nil && stringSliceEqualUnordered(*cs.FilterOutTerms, eff.Filtering.FilterOutTerms) {
		cs.FilterOutTerms = nil
		changed = true
	}
	if cs.PreferredTerms != nil && stringSliceEqualUnordered(*cs.PreferredTerms, eff.Filtering.PreferredTerms) {
		cs.PreferredTerms = nil
		changed = true
	}
	if cs.NonPreferredTerms != nil && stringSliceEqualUnordered(*cs.NonPreferredTerms, eff.Filtering.NonPreferredTerms) {
		cs.NonPreferredTerms = nil
		changed = true
	}
	if cs.DownloadPreferredTerms != nil && stringSliceEqualUnordered(*cs.DownloadPreferredTerms, eff.Filtering.DownloadPreferredTerms) {
		cs.DownloadPreferredTerms = nil
		changed = true
	}
	if cs.UnknownTrackPolicy != nil && *cs.UnknownTrackPolicy == eff.Filtering.UnknownTrackPolicy {
		cs.UnknownTrackPolicy = nil
		changed = true
	}
	if cs.RealDebridRestrictedTermsFilterEnabled != nil && eff.Filtering.RealDebridRestrictedTermsFilterEnabled != nil && *cs.RealDebridRestrictedTermsFilterEnabled == *eff.Filtering.RealDebridRestrictedTermsFilterEnabled {
		cs.RealDebridRestrictedTermsFilterEnabled = nil
		changed = true
	}
	if cs.BypassFilteringForAIOStreamsOnly != nil && eff.Display.BypassFilteringForAIOStreamsOnly != nil && *cs.BypassFilteringForAIOStreamsOnly == *eff.Display.BypassFilteringForAIOStreamsOnly {
		cs.BypassFilteringForAIOStreamsOnly = nil
		changed = true
	}
	if cs.ShowStreamSourceInfo != nil && eff.Display.ShowStreamSourceInfo != nil && *cs.ShowStreamSourceInfo == *eff.Display.ShowStreamSourceInfo {
		cs.ShowStreamSourceInfo = nil
		changed = true
	}
	if cs.IncludeUnreleasedMoviesInLists != nil && eff.Display.IncludeUnreleasedMoviesInLists != nil && *cs.IncludeUnreleasedMoviesInLists == *eff.Display.IncludeUnreleasedMoviesInLists {
		cs.IncludeUnreleasedMoviesInLists = nil
		changed = true
	}
	if cs.IncludeUnreleasedShowsInLists != nil && eff.Display.IncludeUnreleasedShowsInLists != nil && *cs.IncludeUnreleasedShowsInLists == *eff.Display.IncludeUnreleasedShowsInLists {
		cs.IncludeUnreleasedShowsInLists = nil
		changed = true
	}
	if cs.IncludeUnreleasedMoviesInSearch != nil && eff.Display.IncludeUnreleasedMoviesInSearch != nil && *cs.IncludeUnreleasedMoviesInSearch == *eff.Display.IncludeUnreleasedMoviesInSearch {
		cs.IncludeUnreleasedMoviesInSearch = nil
		changed = true
	}
	if cs.IncludeUnreleasedShowsInSearch != nil && eff.Display.IncludeUnreleasedShowsInSearch != nil && *cs.IncludeUnreleasedShowsInSearch == *eff.Display.IncludeUnreleasedShowsInSearch {
		cs.IncludeUnreleasedShowsInSearch = nil
		changed = true
	}
	if cs.DisableMobileTopCarousel != nil && eff.Display.DisableMobileTopCarousel != nil && *cs.DisableMobileTopCarousel == *eff.Display.DisableMobileTopCarousel {
		cs.DisableMobileTopCarousel = nil
		changed = true
	}
	if cs.HideContinueWatchingHeroMetadata != nil && eff.Display.HideContinueWatchingHeroMetadata != nil && *cs.HideContinueWatchingHeroMetadata == *eff.Display.HideContinueWatchingHeroMetadata {
		cs.HideContinueWatchingHeroMetadata = nil
		changed = true
	}
	if cs.MoveDetailsRatingsToMetadata != nil && eff.Display.MoveDetailsRatingsToMetadata != nil && *cs.MoveDetailsRatingsToMetadata == *eff.Display.MoveDetailsRatingsToMetadata {
		cs.MoveDetailsRatingsToMetadata = nil
		changed = true
	}
	if cs.HideDetailsPoster != nil && eff.Display.HideDetailsPoster != nil && *cs.HideDetailsPoster == *eff.Display.HideDetailsPoster {
		cs.HideDetailsPoster = nil
		changed = true
	}
	if cs.ShowDetailsPrequeueFilename != nil && eff.Display.ShowDetailsPrequeueFilename != nil && *cs.ShowDetailsPrequeueFilename == *eff.Display.ShowDetailsPrequeueFilename {
		cs.ShowDetailsPrequeueFilename = nil
		changed = true
	}
	if cs.ShowDetailsRuntime != nil && eff.Display.ShowDetailsRuntime != nil && *cs.ShowDetailsRuntime == *eff.Display.ShowDetailsRuntime {
		cs.ShowDetailsRuntime = nil
		changed = true
	}
	if cs.ShowDetailsSelectedTracks != nil && eff.Display.ShowDetailsSelectedTracks != nil && *cs.ShowDetailsSelectedTracks == *eff.Display.ShowDetailsSelectedTracks {
		cs.ShowDetailsSelectedTracks = nil
		changed = true
	}
	if cs.HideTVDrawerRail != nil && eff.Display.HideTVDrawerRail != nil && *cs.HideTVDrawerRail == *eff.Display.HideTVDrawerRail {
		cs.HideTVDrawerRail = nil
		changed = true
	}
	if cs.SimpleMode != nil && eff.Display.SimpleMode != nil && *cs.SimpleMode == *eff.Display.SimpleMode {
		cs.SimpleMode = nil
		changed = true
	}
	if cs.SimpleModeHomeShelves != nil && eff.Display.SimpleModeHomeShelves != nil && stringSliceEqualOrdered(*cs.SimpleModeHomeShelves, *eff.Display.SimpleModeHomeShelves) {
		cs.SimpleModeHomeShelves = nil
		changed = true
	}
	if cs.DisableTVHomeCardDimming != nil && eff.Display.DisableTVHomeCardDimming != nil && *cs.DisableTVHomeCardDimming == *eff.Display.DisableTVHomeCardDimming {
		cs.DisableTVHomeCardDimming = nil
		changed = true
	}
	if cs.EnableAnimations != nil && eff.Display.EnableAnimations != nil && *cs.EnableAnimations == *eff.Display.EnableAnimations {
		cs.EnableAnimations = nil
		changed = true
	}
	if cs.EnableHeroArtPanning != nil && eff.Display.EnableHeroArtPanning != nil && *cs.EnableHeroArtPanning == *eff.Display.EnableHeroArtPanning {
		cs.EnableHeroArtPanning = nil
		changed = true
	}
	if cs.EnableHeroArtRotation != nil && eff.Display.EnableHeroArtRotation != nil && *cs.EnableHeroArtRotation == *eff.Display.EnableHeroArtRotation {
		cs.EnableHeroArtRotation = nil
		changed = true
	}
	if cs.ShowSeriesBackdropForMissingEpisodeArt != nil && eff.Display.ShowSeriesBackdropForMissingEpisodeArt != nil && *cs.ShowSeriesBackdropForMissingEpisodeArt == *eff.Display.ShowSeriesBackdropForMissingEpisodeArt {
		cs.ShowSeriesBackdropForMissingEpisodeArt = nil
		changed = true
	}
	if cs.NavigationTabVisibility != nil && stringSliceEqualUnordered(*cs.NavigationTabVisibility, eff.Display.NavigationTabVisibility) {
		cs.NavigationTabVisibility = nil
		changed = true
	}
	if cs.Appearance != nil && appearanceEqual(*cs.Appearance, eff.Display.Appearance) {
		cs.Appearance = nil
		changed = true
	}

	// AnimeFiltering
	if cs.AnimeLanguageEnabled != nil && eff.AnimeFiltering.AnimeLanguageEnabled != nil && *cs.AnimeLanguageEnabled == *eff.AnimeFiltering.AnimeLanguageEnabled {
		cs.AnimeLanguageEnabled = nil
		changed = true
	}
	if cs.AnimePreferredLanguage != nil && eff.AnimeFiltering.AnimePreferredLanguage != nil && *cs.AnimePreferredLanguage == *eff.AnimeFiltering.AnimePreferredLanguage {
		cs.AnimePreferredLanguage = nil
		changed = true
	}

	// Network: inherently per-device — skip

	// Ranking
	if cs.RankingCriteria != nil {
		if clientRankingMatchesProfile(*cs.RankingCriteria, eff.Ranking) {
			cs.RankingCriteria = nil
			changed = true
		}
	}
	if cs.NewestReleaseFirst != nil {
		profileValue := false
		if eff.Ranking != nil && eff.Ranking.NewestReleaseFirst != nil {
			profileValue = *eff.Ranking.NewestReleaseFirst
		}
		if *cs.NewestReleaseFirst == profileValue {
			cs.NewestReleaseFirst = nil
			changed = true
		}
	}

	return changed
}

func clientRankingMatchesProfile(clientCriteria []models.ClientRankingCriterion, profileRanking *models.UserRankingSettings) bool {
	if profileRanking == nil {
		// Client has ranking overrides but profile has none — can't determine match
		return false
	}
	profileByID := make(map[config.RankingCriterionID]models.UserRankingCriterion)
	for _, pc := range profileRanking.Criteria {
		profileByID[pc.ID] = pc
	}
	for _, cc := range clientCriteria {
		pc, ok := profileByID[cc.ID]
		if !ok {
			return false
		}
		if cc.Enabled != nil && pc.Enabled != nil && *cc.Enabled != *pc.Enabled {
			return false
		}
		if cc.Order != nil && pc.Order != nil && *cc.Order != *pc.Order {
			return false
		}
	}
	return true
}

// stringSliceEqualUnordered compares two slices ignoring order.
func stringSliceEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	aCopy := make([]string, len(a))
	copy(aCopy, a)
	bCopy := make([]string, len(b))
	copy(bCopy, b)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}

// stringSliceEqualOrdered compares two slices in order.
func stringSliceEqualOrdered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func appearanceEqual(a, b models.AppearanceSettings) bool {
	if (a.FontScale == nil) != (b.FontScale == nil) {
		return false
	}
	if a.FontScale != nil && b.FontScale != nil && *a.FontScale != *b.FontScale {
		return false
	}
	if a.AccentColor != b.AccentColor ||
		a.TextColor != b.TextColor ||
		a.SecondaryTextColor != b.SecondaryTextColor ||
		a.BackgroundColor != b.BackgroundColor ||
		a.ModalBackgroundColor != b.ModalBackgroundColor ||
		a.ButtonStyle != b.ButtonStyle ||
		a.ButtonRadius != b.ButtonRadius {
		return false
	}
	if (a.HighContrast == nil) != (b.HighContrast == nil) {
		return false
	}
	if a.HighContrast != nil && b.HighContrast != nil && *a.HighContrast != *b.HighContrast {
		return false
	}
	if (a.ReduceOverlays == nil) != (b.ReduceOverlays == nil) {
		return false
	}
	if a.ReduceOverlays != nil && b.ReduceOverlays != nil && *a.ReduceOverlays != *b.ReduceOverlays {
		return false
	}
	return true
}

// shelfConfigsEqual compares explicit user shelf values against global shelves.
// A global shelf missing from the user list is inherited, not an override.
func shelfConfigsEqual(user []models.ShelfConfig, global []config.ShelfConfig) bool {
	globalByID := make(map[string]config.ShelfConfig)
	for _, gs := range global {
		globalByID[gs.ID] = gs
	}
	for _, us := range user {
		gs, ok := globalByID[us.ID]
		if !ok {
			return false
		}
		if us.Name != gs.Name || us.Enabled != gs.Enabled || us.Order != gs.Order ||
			(us.Type != "" && us.Type != gs.Type) ||
			(us.LibraryID != "" && us.LibraryID != gs.LibraryID) ||
			(us.ListURL != "" && us.ListURL != gs.ListURL) ||
			(us.AddonManifestURL != "" && us.AddonManifestURL != gs.AddonManifestURL) ||
			(us.AddonCatalogType != "" && us.AddonCatalogType != gs.AddonCatalogType) ||
			(us.AddonCatalogID != "" && us.AddonCatalogID != gs.AddonCatalogID) ||
			(us.AddonName != "" && us.AddonName != gs.AddonName) ||
			(us.TMDBSourceType != "" && us.TMDBSourceType != gs.TMDBSourceType) ||
			(us.TMDBSourceID != "" && us.TMDBSourceID != gs.TMDBSourceID) ||
			(us.TMDBSourceName != "" && us.TMDBSourceName != gs.TMDBSourceName) ||
			(us.TMDBMediaType != "" && us.TMDBMediaType != gs.TMDBMediaType) ||
			(us.TMDBDiscoverQuery != "" && us.TMDBDiscoverQuery != gs.TMDBDiscoverQuery) ||
			us.Limit != gs.Limit ||
			us.HideUnreleased != gs.HideUnreleased ||
			(us.Sort != "" && us.Sort != gs.Sort) ||
			(us.TraktAccountID != "" && us.TraktAccountID != gs.TraktAccountID) ||
			(us.TraktListType != "" && us.TraktListType != gs.TraktListType) ||
			(us.TraktListID != "" && us.TraktListID != gs.TraktListID) ||
			(us.SimklAccountID != "" && us.SimklAccountID != gs.SimklAccountID) ||
			(us.SimklListType != "" && us.SimklListType != gs.SimklListType) ||
			(us.SimklMediaType != "" && us.SimklMediaType != gs.SimklMediaType) ||
			(us.LetterboxdListID != "" && us.LetterboxdListID != gs.LetterboxdListID) ||
			(us.LetterboxdListURL != "" && us.LetterboxdListURL != gs.LetterboxdListURL) ||
			(us.AnimateLogoOnlyOnFocus != gs.AnimateLogoOnlyOnFocus) ||
			(us.ShowCollectionTitles != gs.ShowCollectionTitles) ||
			(us.ShowCollectionCounts != gs.ShowCollectionCounts) ||
			(len(us.StreamingServices) > 0 && !reflect.DeepEqual(us.StreamingServices, configStreamingServicesToModel(gs.StreamingServices))) ||
			(len(us.CollectionItems) > 0 && !reflect.DeepEqual(us.CollectionItems, configCollectionHubItemsToModel(gs.CollectionItems))) ||
			!calendarSettingsEqual(us.CalendarSources, gs.CalendarSources) {
			return false
		}
	}
	return true
}

func mergeShelvesWithGlobal(user []models.ShelfConfig, global []config.ShelfConfig) []models.ShelfConfig {
	if len(global) == 0 {
		return user
	}
	merged := append([]models.ShelfConfig(nil), user...)
	existing := make(map[string]bool, len(merged))
	for _, shelf := range merged {
		existing[shelf.ID] = true
	}
	for _, shelf := range configShelvesToModel(global) {
		if !existing[shelf.ID] {
			merged = append(merged, shelf)
			existing[shelf.ID] = true
		}
	}
	return merged
}

func calendarSettingsEqual(user models.CalendarSettings, global config.CalendarSourceSettings) bool {
	if !explicitBoolPtrEqual(user.Watchlist, global.Watchlist) ||
		!explicitBoolPtrEqual(user.History, global.History) ||
		!explicitBoolPtrEqual(user.Trending, global.Trending) ||
		!explicitBoolPtrEqual(user.TopTrending, global.TopTrending) ||
		!explicitBoolPtrEqual(user.MDBLists, global.MDBLists) {
		return false
	}
	if user.MDBListShelves == nil {
		return true
	}
	if len(user.MDBListShelves) != len(global.MDBListShelves) {
		return false
	}
	for k, v := range user.MDBListShelves {
		if global.MDBListShelves[k] != v {
			return false
		}
	}
	return true
}

func explicitBoolPtrEqual(user, global *bool) bool {
	if user == nil {
		return true
	}
	if global == nil {
		return !*user
	}
	return *user == *global
}

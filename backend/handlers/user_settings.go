package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"reflect"
	"strings"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	user_settings "novastream/services/user_settings"

	"github.com/gorilla/mux"
)

type userSettingsService interface {
	Get(userID string) (*models.UserSettings, error)
	GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error)
	Update(userID string, settings models.UserSettings) error
	Delete(userID string) error
}

type userPrequeueClearer interface {
	DeleteByUser(userID string)
}

type userSearchCacheClearer interface {
	ClearSearchCache()
}

var _ userSettingsService = (*user_settings.Service)(nil)

// localLibraryLister is the minimal interface needed to fetch local media libraries.
type localLibraryLister interface {
	ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error)
}

type UserSettingsHandler struct {
	GuideChanged  func()
	Service       userSettingsService
	Users         userService
	ConfigManager *config.Manager
	LocalMedia    localLibraryLister
	PrequeueStore userPrequeueClearer
	SearchCache   userSearchCacheClearer
}

func NewUserSettingsHandler(service userSettingsService, users userService, configManager *config.Manager) *UserSettingsHandler {
	return &UserSettingsHandler{
		Service:       service,
		Users:         users,
		ConfigManager: configManager,
	}
}

func (h *UserSettingsHandler) SetPrequeueStore(ps userPrequeueClearer) {
	h.PrequeueStore = ps
}

func (h *UserSettingsHandler) SetSearchCacheClearer(sc userSearchCacheClearer) {
	h.SearchCache = sc
}

// GetSettings returns the user's settings merged with global defaults.
func (h *UserSettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	// Get global settings as defaults
	defaults := h.getDefaultsFromGlobal()

	settings, err := h.Service.GetWithDefaults(userID, defaults)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	scopeUserLiveSettings(&settings, h.ConfigManager, userID, auth.GetAccountID(r), auth.IsMaster(r))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

// GetFrontendSettingState reports which exposed fields are stored as profile
// overrides. Effective values alone cannot distinguish inheritance from an
// explicit override with the same value as the server default.
func (h *UserSettingsHandler) GetFrontendSettingState(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	paths, err := frontendEditablePaths(h.ConfigManager)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current, err := h.Service.Get(userID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	overridden := make([]string, 0, len(paths))
	if current != nil {
		for _, path := range paths {
			if jsonModelPathOverridden(current, path) {
				overridden = append(overridden, path)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(frontendSettingState{OverriddenPaths: overridden})
}

// PutSettings updates the user's settings.
func (h *UserSettingsHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	oldSettings, _ := h.Service.Get(userID)

	var settings models.UserSettings
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateUserFilterTerms("filtering", &settings.Filtering); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	restoreScopedLiveSettings(&settings, oldSettings, h.ConfigManager, userID, auth.GetAccountID(r), auth.IsMaster(r))

	if err := h.Service.Update(userID, settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Update keeps the stored intro/recap auto-skip values regardless of the
	// body; report those rather than whatever the client sent.
	if saved, err := h.Service.Get(userID); err == nil && saved != nil {
		settings.Playback.IntroAutoSkip = saved.Playback.IntroAutoSkip
		settings.Playback.RecapAutoSkip = saved.Playback.RecapAutoSkip
	}

	var previous models.UserSettings
	if oldSettings != nil {
		previous = *oldSettings
	}
	if h.GuideChanged != nil && liveGuideSourcesChanged(previous.LiveTV, settings.LiveTV) {
		h.GuideChanged()
	}
	if !reflect.DeepEqual(previous.Filtering, settings.Filtering) || !reflect.DeepEqual(previous.Ranking, settings.Ranking) {
		if h.PrequeueStore != nil {
			log.Printf("[user-settings] ranking/filtering changed for user=%s, clearing prequeue cache", userID)
			h.PrequeueStore.DeleteByUser(userID)
		}
		if h.SearchCache != nil {
			log.Printf("[user-settings] ranking/filtering changed for user=%s, clearing search results cache", userID)
			h.SearchCache.ClearSearchCache()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(settings)
}

// PatchFrontendSetting updates one admin-exposed profile setting without
// replacing the rest of the profile's override document.
func (h *UserSettingsHandler) PatchFrontendSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	patch, err := decodeFrontendSettingPatch(json.NewDecoder(r.Body), h.ConfigManager)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	current, err := h.Service.Get(userID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	raw, err := json.Marshal(current)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	raw, err = patchJSONObject(raw, patch.Path, patch.Value, patch.Reset)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	var next models.UserSettings
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&next); err != nil {
		writeJSONError(w, "setting is not supported for profile overrides", http.StatusBadRequest)
		return
	}
	if err := validateUserFilterTerms("filtering", &next.Filtering); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Service.Update(userID, next); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var previous models.UserSettings
	if current != nil {
		previous = *current
	}
	if h.GuideChanged != nil && liveGuideSourcesChanged(previous.LiveTV, next.LiveTV) {
		h.GuideChanged()
	}
	if !reflect.DeepEqual(previous.Filtering, next.Filtering) || !reflect.DeepEqual(previous.Ranking, next.Ranking) {
		if h.PrequeueStore != nil {
			h.PrequeueStore.DeleteByUser(userID)
		}
		if h.SearchCache != nil {
			h.SearchCache.ClearSearchCache()
		}
	}
	effective, err := h.Service.GetWithDefaults(userID, h.getDefaultsFromGlobal())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(effective)
}

func (h *UserSettingsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *UserSettingsHandler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["userID"])

	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return "", false
	}

	if h.Users != nil && !h.Users.Exists(userID) {
		http.Error(w, "user not found", http.StatusNotFound)
		return "", false
	}

	return userID, true
}

// getDefaultsFromGlobal extracts the per-user settings from global config as defaults.
func (h *UserSettingsHandler) getDefaultsFromGlobal() models.UserSettings {
	globalSettings, err := h.ConfigManager.Load()
	if err != nil {
		return models.DefaultUserSettings()
	}
	maxStreams := globalSettings.Live.MaxStreams
	if maxStreams < 0 {
		maxStreams = 0
	}

	shelves := convertShelves(globalSettings.HomeShelves.Shelves)

	return models.UserSettings{
		Metadata: models.MetadataSettings{
			PrimaryLanguage: globalSettings.Metadata.EffectivePrimaryLanguage(),
		},
		Playback: models.PlaybackSettings{
			PreferredPlayer:            globalSettings.Playback.PreferredPlayer,
			PreferredAudioLanguage:     globalSettings.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage:  globalSettings.Playback.PreferredSubtitleLanguage,
			AllowedTrackLanguages:      models.StringSlicePtr(globalSettings.Playback.AllowedTrackLanguages),
			PreferredSubtitleMode:      globalSettings.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:       models.BoolPtr(globalSettings.Playback.PauseWhenAppInactive),
			UseLoadingScreen:           models.BoolPtr(globalSettings.Playback.UseLoadingScreen),
			SubtitleSize:               globalSettings.Playback.SubtitleSize,
			SubtitleColor:              globalSettings.Playback.SubtitleColor,
			SubtitleOpacity:            models.FloatPtr(globalSettings.Playback.SubtitleOpacity),
			SubtitleFont:               models.StringPtr(globalSettings.Playback.SubtitleFont),
			SubtitleBold:               models.BoolPtr(globalSettings.Playback.SubtitleBold),
			SubtitleOutlineEnabled:     models.BoolPtr(globalSettings.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:       globalSettings.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:      models.FloatPtr(globalSettings.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled:  models.BoolPtr(globalSettings.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:    globalSettings.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity:  models.FloatPtr(globalSettings.Playback.SubtitleBackgroundOpacity),
			SeekForwardSeconds:         globalSettings.Playback.SeekForwardSeconds,
			SeekBackwardSeconds:        globalSettings.Playback.SeekBackwardSeconds,
			ForceAACTranscoding:        models.BoolPtr(globalSettings.Playback.ForceAACTranscoding),
			AutoPlayTrailersTV:         models.BoolPtr(globalSettings.Playback.AutoPlayTrailersTV),
			RewindOnResumeFromPause:    models.IntPtr(globalSettings.Playback.RewindOnResumeFromPause),
			RewindOnPlaybackStart:      models.IntPtr(globalSettings.Playback.RewindOnPlaybackStart),
			DisablePrequeue:            models.BoolPtr(globalSettings.Playback.DisablePrequeue),
			PrerollMode:                globalSettings.Playback.PrerollMode,
			PrerollAssetID:             globalSettings.Playback.PrerollAssetID,
			PrerollMediaScope:          globalSettings.Playback.PrerollMediaScope,
			PrerollSkipIfPrequeueReady: models.BoolPtr(globalSettings.Playback.PrerollSkipIfPrequeueReady),
			StreamMigrationEnabled:     models.BoolPtr(globalSettings.Playback.StreamMigrationEnabled),
			IgnoreDVCompatibilityCheck: models.BoolPtr(globalSettings.Playback.IgnoreDVCompatibilityCheck),
			CreditsDetectionEnabled:    models.BoolPtr(globalSettings.Playback.CreditsDetectionEnabled),
			IntroAutoSkip:              models.BoolPtr(false),
			RecapAutoSkip:              models.BoolPtr(false),
			CreditsAutoSkip:            models.BoolPtr(globalSettings.Playback.CreditsAutoSkip || globalSettings.Playback.CreditsDetection),
			ScrobbleStartDelaySeconds:  models.IntPtr(globalSettings.Playback.ScrobbleStartDelaySeconds),
			MatchFrameRate:             models.BoolPtr(globalSettings.Playback.MatchFrameRate),
			MaxResultsPerResolution:    models.IntPtr(globalSettings.Playback.MaxResultsPerResolution),
		},
		HomeShelves: models.HomeShelvesSettings{
			Views:                           globalSettings.HomeShelves.Views,
			Shelves:                         shelves,
			ExploreCardPosition:             string(globalSettings.HomeShelves.ExploreCardPosition),
			ItemCap:                         globalSettings.HomeShelves.ItemCap,
			ExcludeUpcomingFromContinue:     models.BoolPtr(globalSettings.HomeShelves.ExcludeUpcomingFromContinue),
			HomeShelfFocusModel:             globalSettings.HomeShelves.HomeShelfFocusModel,
			DisableTvLandscapeCardExpansion: models.BoolPtr(globalSettings.HomeShelves.DisableTvLandscapeCardExpansion),
			HomeShelfScale:                  models.FloatPtr(globalSettings.HomeShelves.HomeShelfScale),
			HomeHeroScale:                   models.FloatPtr(globalSettings.HomeShelves.HomeHeroScale),
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:                         models.FloatPtr(globalSettings.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:                       models.FloatPtr(globalSettings.Filtering.MaxSizeEpisodeGB),
			MaxResolution:                          models.StringPtr(globalSettings.Filtering.MaxResolution),
			HDRDVPolicy:                            models.HDRDVPolicy(globalSettings.Filtering.HDRDVPolicy),
			RequiredTerms:                          globalSettings.Filtering.RequiredTerms,
			FilterOutTerms:                         globalSettings.Filtering.FilterOutTerms,
			PreferredTerms:                         globalSettings.Filtering.PreferredTerms,
			NonPreferredTerms:                      globalSettings.Filtering.NonPreferredTerms,
			DownloadPreferredTerms:                 globalSettings.Filtering.DownloadPreferredTerms,
			PreferredScraper:                       models.StringPtr(globalSettings.Filtering.PreferredScraper),
			ServicePriority:                        models.StringPtr(string(globalSettings.Filtering.ServicePriority)),
			UnknownTrackPolicy:                     string(globalSettings.Filtering.UnknownTrackPolicy),
			AdaptivePlaybackEnabled:                models.BoolPtr(globalSettings.Filtering.AdaptivePlaybackEnabled),
			AdaptiveTargetBufferFactor:             models.FloatPtr(globalSettings.Filtering.AdaptiveTargetBufferFactor),
			RealDebridRestrictedTermsFilterEnabled: models.BoolPtr(globalSettings.Filtering.RealDebridRestrictedTermsFilterEnabled),
		},
		Display: models.DisplaySettings{
			BadgeVisibility:                              globalSettings.Display.BadgeVisibility,
			NavigationTabVisibility:                      globalSettings.Display.NavigationTabVisibility,
			WatchStateIconStyle:                          globalSettings.Display.WatchStateIconStyle,
			IncludeUnreleasedMoviesInLists:               models.BoolPtr(globalSettings.Display.IncludeUnreleasedMoviesInLists),
			IncludeUnreleasedShowsInLists:                models.BoolPtr(globalSettings.Display.IncludeUnreleasedShowsInLists),
			IncludeUnreleasedMoviesInSearch:              models.BoolPtr(globalSettings.Display.IncludeUnreleasedMoviesInSearch),
			IncludeUnreleasedShowsInSearch:               models.BoolPtr(globalSettings.Display.IncludeUnreleasedShowsInSearch),
			BypassFilteringForAIOStreamsOnly:             models.BoolPtr(globalSettings.Display.BypassFilteringForAIOStreamsOnly),
			ShowStreamSourceInfo:                         models.BoolPtr(globalSettings.Display.ShowStreamSourceInfo),
			DisableMobileTopCarousel:                     models.BoolPtr(globalSettings.Display.DisableMobileTopCarousel),
			HideContinueWatchingHeroMetadata:             models.BoolPtr(globalSettings.Display.HideContinueWatchingHeroMetadata),
			MoveDetailsRatingsToMetadata:                 models.BoolPtr(globalSettings.Display.MoveDetailsRatingsToMetadata),
			HideDetailsPoster:                            models.BoolPtr(globalSettings.Display.HideDetailsPoster),
			HideTVDrawerRail:                             models.BoolPtr(globalSettings.Display.HideTVDrawerRail),
			SimpleMode:                                   models.BoolPtr(globalSettings.Display.SimpleMode),
			SimpleModeHomeShelves:                        models.StringSlicePtr(globalSettings.Display.SimpleModeHomeShelves),
			DisableTVHomeCardDimming:                     models.BoolPtr(globalSettings.Display.DisableTVHomeCardDimming),
			EnableAnimations:                             models.BoolPtr(globalSettings.Display.EnableAnimations),
			EnableHeroArtPanning:                         models.BoolPtr(globalSettings.Display.EnableHeroArtPanning),
			EnableHeroArtRotation:                        models.BoolPtr(globalSettings.Display.EnableHeroArtRotation),
			ShowSeriesBackdropForMissingEpisodeArt:       models.BoolPtr(globalSettings.Display.ShowSeriesBackdropForMissingEpisodeArt),
			BlurUnwatchedEpisodeThumbnails:               models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeThumbnails),
			BlurUnwatchedEpisodeThumbnailsIncludeCurrent: models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent),
			BlurUnwatchedEpisodeOverviews:                models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeOverviews),
			BlurUnwatchedEpisodeOverviewsIncludeCurrent:  models.BoolPtr(globalSettings.Display.BlurUnwatchedEpisodeOverviewsIncludeCurrent),
			AppLanguage:                                  globalSettings.Display.AppLanguage,
			Appearance: models.AppearanceSettings{
				FontScale:            globalSettings.Display.Appearance.FontScale,
				AccentColor:          globalSettings.Display.Appearance.AccentColor,
				TextColor:            globalSettings.Display.Appearance.TextColor,
				SecondaryTextColor:   globalSettings.Display.Appearance.SecondaryTextColor,
				BackgroundColor:      globalSettings.Display.Appearance.BackgroundColor,
				ModalBackgroundColor: globalSettings.Display.Appearance.ModalBackgroundColor,
				ButtonStyle:          globalSettings.Display.Appearance.ButtonStyle,
				ButtonRadius:         globalSettings.Display.Appearance.ButtonRadius,
				HighContrast:         globalSettings.Display.Appearance.HighContrast,
				ReduceOverlays:       globalSettings.Display.Appearance.ReduceOverlays,
			},
		},
		LiveTV: models.LiveTVSettings{
			HiddenChannels:     []string{},
			FavoriteChannels:   []string{},
			SelectedCategories: []string{},
			MaxStreams:         &maxStreams,
		},
	}
}

// convertShelves converts config.ShelfConfig to models.ShelfConfig
func convertShelves(configShelves []config.ShelfConfig) []models.ShelfConfig {
	result := make([]models.ShelfConfig, len(configShelves))
	for i, s := range configShelves {
		result[i] = models.ShelfConfig{
			ID:                     s.ID,
			Name:                   s.Name,
			Enabled:                s.Enabled,
			Order:                  s.Order,
			Type:                   s.Type,
			LibraryID:              s.LibraryID,
			ListURL:                s.ListURL,
			TMDBSourceType:         s.TMDBSourceType,
			TMDBSourceID:           s.TMDBSourceID,
			TMDBSourceName:         s.TMDBSourceName,
			TMDBMediaType:          s.TMDBMediaType,
			TMDBDiscoverQuery:      s.TMDBDiscoverQuery,
			StreamingServices:      convertStreamingServices(s.StreamingServices),
			CollectionItems:        convertCollectionHubItems(s.CollectionItems),
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
	return result
}

func convertCollectionHubItems(items []config.CollectionHubLink) []models.CollectionHubLink {
	if len(items) == 0 {
		return nil
	}
	result := make([]models.CollectionHubLink, len(items))
	for i, item := range items {
		result[i] = models.CollectionHubLink{
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
	return result
}

func convertStreamingServices(services []config.StreamingServiceLink) []models.StreamingServiceLink {
	if len(services) == 0 {
		return nil
	}
	result := make([]models.StreamingServiceLink, len(services))
	for i, service := range services {
		lists := make([]models.StreamingServiceListLink, len(service.Lists))
		for j, list := range service.Lists {
			lists[j] = models.StreamingServiceListLink{
				Key:   list.Key,
				Title: list.Title,
				URL:   list.URL,
			}
		}
		result[i] = models.StreamingServiceLink{
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
	return result
}

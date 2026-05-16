package user_settings

import (
	"log"
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

	// Build client→userID mapping
	clientToUser := make(map[string]string)
	for _, c := range clientsLister.List() {
		clientToUser[c.ID] = c.UserID
	}

	// Build effective profile settings cache (profile values merged with global defaults)
	effectiveProfiles := make(map[string]models.UserSettings)

	clientChanged := false
	for clientID, cs := range allClients {
		userID, ok := clientToUser[clientID]
		if !ok {
			continue
		}

		effective, exists := effectiveProfiles[userID]
		if !exists {
			effective = s.computeEffectiveProfile(userID, globalSettings)
			effectiveProfiles[userID] = effective
		}

		if stripClientSettings(&cs, effective) {
			if cs.IsEmpty() {
				delete(allClients, clientID)
			} else {
				allClients[clientID] = cs
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
		Playback: models.PlaybackSettings{
			PreferredPlayer:           g.Playback.PreferredPlayer,
			PreferredAudioLanguage:    g.Playback.PreferredAudioLanguage,
			PreferredSubtitleLanguage: g.Playback.PreferredSubtitleLanguage,
			PreferredSubtitleMode:     g.Playback.PreferredSubtitleMode,
			PauseWhenAppInactive:      g.Playback.PauseWhenAppInactive,
			UseLoadingScreen:          g.Playback.UseLoadingScreen,
			SubtitleSize:              g.Playback.SubtitleSize,
			SubtitleColor:             g.Playback.SubtitleColor,
			SubtitleOpacity:           models.FloatPtr(g.Playback.SubtitleOpacity),
			SubtitleFont:              g.Playback.SubtitleFont,
			SubtitleOutlineEnabled:    models.BoolPtr(g.Playback.SubtitleOutlineEnabled),
			SubtitleOutlineColor:      g.Playback.SubtitleOutlineColor,
			SubtitleOutlineWeight:     models.FloatPtr(g.Playback.SubtitleOutlineWeight),
			SubtitleBackgroundEnabled: models.BoolPtr(g.Playback.SubtitleBackgroundEnabled),
			SubtitleBackgroundColor:   g.Playback.SubtitleBackgroundColor,
			SubtitleBackgroundOpacity: models.FloatPtr(g.Playback.SubtitleBackgroundOpacity),
			RewindOnResumeFromPause:   g.Playback.RewindOnResumeFromPause,
			RewindOnPlaybackStart:     g.Playback.RewindOnPlaybackStart,
			CreditsAutoSkip:           g.Playback.CreditsAutoSkip || g.Playback.CreditsDetection,
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB:         models.FloatPtr(g.Filtering.MaxSizeMovieGB),
			MaxSizeEpisodeGB:       models.FloatPtr(g.Filtering.MaxSizeEpisodeGB),
			MaxResolution:          g.Filtering.MaxResolution,
			HDRDVPolicy:            models.HDRDVPolicy(g.Filtering.HDRDVPolicy),
			RequiredTerms:          g.Filtering.RequiredTerms,
			FilterOutTerms:         g.Filtering.FilterOutTerms,
			PreferredTerms:         g.Filtering.PreferredTerms,
			NonPreferredTerms:      g.Filtering.NonPreferredTerms,
			DownloadPreferredTerms: g.Filtering.DownloadPreferredTerms,
		},
		AnimeFiltering: models.AnimeFilteringSettings{
			AnimeLanguageEnabled:   models.BoolPtr(g.AnimeFiltering.AnimeLanguageEnabled),
			AnimePreferredLanguage: models.StringPtr(g.AnimeFiltering.AnimePreferredLanguage),
		},
		Display: models.DisplaySettings{
			BadgeVisibility:                  g.Display.BadgeVisibility,
			NavigationTabVisibility:          g.Display.NavigationTabVisibility,
			WatchStateIconStyle:              g.Display.WatchStateIconStyle,
			BypassFilteringForAIOStreamsOnly: models.BoolPtr(g.Display.BypassFilteringForAIOStreamsOnly),
			AppLanguage:                      g.Display.AppLanguage,
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
			Shelves:             configShelvesToModel(g.HomeShelves.Shelves),
			ExploreCardPosition: string(g.HomeShelves.ExploreCardPosition),
			ItemCap:             g.HomeShelves.ItemCap,
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
			ID:             s.ID,
			Name:           s.Name,
			Enabled:        s.Enabled,
			Order:          s.Order,
			Type:           s.Type,
			ListURL:        s.ListURL,
			Limit:          s.Limit,
			HideUnreleased: s.HideUnreleased,
		}
	}
	return out
}

// mergeWithGlobal returns effective user settings: profile overrides filled in with global defaults.
func mergeWithGlobal(us models.UserSettings, g config.Settings) models.UserSettings {
	eff := us

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
	if eff.Playback.PreferredSubtitleMode == "" {
		eff.Playback.PreferredSubtitleMode = g.Playback.PreferredSubtitleMode
	}
	if !eff.Playback.PauseWhenAppInactive {
		eff.Playback.PauseWhenAppInactive = g.Playback.PauseWhenAppInactive
	}
	if eff.Playback.SubtitleSize == 0 {
		eff.Playback.SubtitleSize = g.Playback.SubtitleSize
	}
	if eff.Playback.SubtitleColor == "" {
		eff.Playback.SubtitleColor = g.Playback.SubtitleColor
	}
	if eff.Playback.SubtitleOpacity == nil {
		eff.Playback.SubtitleOpacity = models.FloatPtr(g.Playback.SubtitleOpacity)
	}
	if eff.Playback.SubtitleFont == "" {
		eff.Playback.SubtitleFont = g.Playback.SubtitleFont
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
	if !eff.Playback.UseLoadingScreen {
		eff.Playback.UseLoadingScreen = g.Playback.UseLoadingScreen
	}
	if eff.Playback.RewindOnResumeFromPause == 0 {
		eff.Playback.RewindOnResumeFromPause = g.Playback.RewindOnResumeFromPause
	}
	if eff.Playback.RewindOnPlaybackStart == 0 {
		eff.Playback.RewindOnPlaybackStart = g.Playback.RewindOnPlaybackStart
	}
	if !eff.Playback.CreditsAutoSkip {
		eff.Playback.CreditsAutoSkip = g.Playback.CreditsAutoSkip || g.Playback.CreditsDetection
	}

	// Filtering: nil pointers inherit global
	if eff.Filtering.MaxSizeMovieGB == nil {
		eff.Filtering.MaxSizeMovieGB = models.FloatPtr(g.Filtering.MaxSizeMovieGB)
	}
	if eff.Filtering.MaxSizeEpisodeGB == nil {
		eff.Filtering.MaxSizeEpisodeGB = models.FloatPtr(g.Filtering.MaxSizeEpisodeGB)
	}
	if eff.Filtering.MaxResolution == "" {
		eff.Filtering.MaxResolution = g.Filtering.MaxResolution
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
	if eff.Display.BypassFilteringForAIOStreamsOnly == nil {
		eff.Display.BypassFilteringForAIOStreamsOnly = models.BoolPtr(g.Display.BypassFilteringForAIOStreamsOnly)
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
	if len(eff.HomeShelves.Shelves) == 0 {
		eff.HomeShelves.Shelves = configShelvesToModel(g.HomeShelves.Shelves)
	}
	if eff.HomeShelves.ExploreCardPosition == "" {
		eff.HomeShelves.ExploreCardPosition = string(g.HomeShelves.ExploreCardPosition)
	}
	if eff.HomeShelves.ItemCap <= 0 {
		eff.HomeShelves.ItemCap = g.HomeShelves.ItemCap
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
	changed = stripRanking(&us.Ranking, g.Ranking) || changed
	// LiveTV channel lists (HiddenChannels, FavoriteChannels, SelectedCategories) are inherently per-user — never strip.
	// Calendar has no global equivalent — never strip.
	return changed
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
	if p.PreferredSubtitleMode != "" && p.PreferredSubtitleMode == g.PreferredSubtitleMode {
		p.PreferredSubtitleMode = ""
		changed = true
	}
	if p.PauseWhenAppInactive && p.PauseWhenAppInactive == g.PauseWhenAppInactive {
		p.PauseWhenAppInactive = false
		changed = true
	}
	if p.SubtitleSize != 0 && p.SubtitleSize == g.SubtitleSize {
		p.SubtitleSize = 0
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
	if p.SubtitleFont != "" && p.SubtitleFont == g.SubtitleFont {
		p.SubtitleFont = ""
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
	if p.UseLoadingScreen && p.UseLoadingScreen == g.UseLoadingScreen {
		p.UseLoadingScreen = false
		changed = true
	}
	if p.RewindOnResumeFromPause != 0 && p.RewindOnResumeFromPause == g.RewindOnResumeFromPause {
		p.RewindOnResumeFromPause = 0
		changed = true
	}
	if p.RewindOnPlaybackStart != 0 && p.RewindOnPlaybackStart == g.RewindOnPlaybackStart {
		p.RewindOnPlaybackStart = 0
		changed = true
	}
	globalCreditsAutoSkip := g.CreditsAutoSkip || g.CreditsDetection
	if p.CreditsAutoSkip && p.CreditsAutoSkip == globalCreditsAutoSkip {
		p.CreditsAutoSkip = false
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
	if f.MaxResolution != "" && f.MaxResolution == g.MaxResolution {
		f.MaxResolution = ""
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
	return changed
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
	if d.BypassFilteringForAIOStreamsOnly != nil && *d.BypassFilteringForAIOStreamsOnly == g.BypassFilteringForAIOStreamsOnly {
		d.BypassFilteringForAIOStreamsOnly = nil
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
	if *r == nil || len((*r).Criteria) == 0 {
		return false
	}
	// Compare: each user ranking criterion must match a global criterion by ID
	// with same Enabled and Order values. If all match, strip.
	globalByID := make(map[config.RankingCriterionID]config.RankingCriterion)
	for _, gc := range g.Criteria {
		globalByID[gc.ID] = gc
	}
	for _, uc := range (*r).Criteria {
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
	*r = nil
	return true
}

// stripClientSettings removes client overrides that match their parent profile's effective value.
func stripClientSettings(cs *models.ClientFilterSettings, eff models.UserSettings) bool {
	changed := false

	// Filtering
	if cs.MaxSizeMovieGB != nil && eff.Filtering.MaxSizeMovieGB != nil && *cs.MaxSizeMovieGB == *eff.Filtering.MaxSizeMovieGB {
		cs.MaxSizeMovieGB = nil
		changed = true
	}
	if cs.MaxSizeEpisodeGB != nil && eff.Filtering.MaxSizeEpisodeGB != nil && *cs.MaxSizeEpisodeGB == *eff.Filtering.MaxSizeEpisodeGB {
		cs.MaxSizeEpisodeGB = nil
		changed = true
	}
	if cs.MaxResolution != nil && *cs.MaxResolution == eff.Filtering.MaxResolution {
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
	if cs.BypassFilteringForAIOStreamsOnly != nil && eff.Display.BypassFilteringForAIOStreamsOnly != nil && *cs.BypassFilteringForAIOStreamsOnly == *eff.Display.BypassFilteringForAIOStreamsOnly {
		cs.BypassFilteringForAIOStreamsOnly = nil
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

// shelfConfigsEqual compares user shelves against global shelves.
// Matches by ID and compares all fields.
func shelfConfigsEqual(user []models.ShelfConfig, global []config.ShelfConfig) bool {
	if len(user) != len(global) {
		return false
	}
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
			us.Type != gs.Type || us.ListURL != gs.ListURL || us.Limit != gs.Limit ||
			us.HideUnreleased != gs.HideUnreleased {
			return false
		}
	}
	return true
}

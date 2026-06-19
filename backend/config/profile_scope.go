package config

import "strings"

// IsProfileAllowed returns true when a source is available to all profiles
// (empty list) or explicitly includes the requested profile.
func IsProfileAllowed(allowedProfiles []string, profileID string) bool {
	if len(allowedProfiles) == 0 {
		return true
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return false
	}
	for _, allowed := range allowedProfiles {
		if strings.EqualFold(strings.TrimSpace(allowed), profileID) {
			return true
		}
	}
	return false
}

// FilterSettingsForProfile returns a copy of settings with profile-scoped
// search/resolution sources removed.
func FilterSettingsForProfile(settings Settings, profileID string) Settings {
	settings.Usenet = filterUsenetForProfile(settings.Usenet, profileID)
	settings.UsenetEngines = filterUsenetEnginesForProfile(settings.UsenetEngines, profileID)
	settings.Indexers = filterIndexersForProfile(settings.Indexers, profileID)
	settings.TorrentScrapers = filterTorrentScrapersForProfile(settings.TorrentScrapers, profileID)
	settings.Streaming.DebridProviders = filterDebridProvidersForProfile(settings.Streaming.DebridProviders, profileID)
	settings.Live.Sources = filterLiveSourcesForProfile(settings.Live.Sources, profileID)
	settings.Live.PlaylistSources = filterLiveSourcesForProfile(settings.Live.PlaylistSources, profileID)
	return settings
}

func filterUsenetForProfile(items []UsenetSettings, profileID string) []UsenetSettings {
	if len(items) == 0 {
		return items
	}
	filtered := make([]UsenetSettings, 0, len(items))
	for _, item := range items {
		if IsProfileAllowed(item.AllowedProfiles, profileID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterUsenetEnginesForProfile(items []UsenetEngineSettings, profileID string) []UsenetEngineSettings {
	if len(items) == 0 {
		return items
	}
	filtered := make([]UsenetEngineSettings, 0, len(items))
	for _, item := range items {
		if IsProfileAllowed(item.AllowedProfiles, profileID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterIndexersForProfile(items []IndexerConfig, profileID string) []IndexerConfig {
	if len(items) == 0 {
		return items
	}
	filtered := make([]IndexerConfig, 0, len(items))
	for _, item := range items {
		if IsProfileAllowed(item.AllowedProfiles, profileID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterTorrentScrapersForProfile(items []TorrentScraperConfig, profileID string) []TorrentScraperConfig {
	if len(items) == 0 {
		return items
	}
	filtered := make([]TorrentScraperConfig, 0, len(items))
	for _, item := range items {
		if IsProfileAllowed(item.AllowedProfiles, profileID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterDebridProvidersForProfile(items []DebridProviderSettings, profileID string) []DebridProviderSettings {
	if len(items) == 0 {
		return items
	}
	filtered := make([]DebridProviderSettings, 0, len(items))
	for _, item := range items {
		if IsProfileAllowed(item.AllowedProfiles, profileID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterLiveSourcesForProfile(items []LivePlaylistSource, profileID string) []LivePlaylistSource {
	if len(items) == 0 {
		return items
	}
	filtered := make([]LivePlaylistSource, 0, len(items))
	for _, item := range items {
		if IsProfileAllowed(item.AllowedProfiles, profileID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

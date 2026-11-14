package handlers

import (
	"novastream/config"
	"novastream/models"
)

func liveAccountForProfile(users UsersProvider, profileID string) string {
	if users != nil {
		if profile, ok := users.Get(profileID); ok {
			return profile.AccountID
		}
	}
	return ""
}

// Filter account-page and API responses too: an obsolete profile copy must not
// disclose provider credentials after its account assignment has been revoked.
func scopeUserLiveSettings(settings *models.UserSettings, manager *config.Manager, profileID, accountID string) {
	if settings == nil || manager == nil {
		return
	}
	global, err := manager.Load()
	if err != nil || config.LiveAccountsManaged(global.Live) {
		settings.LiveTV = accountManagedLivePreferences(settings).LiveTV
		return
	}
	sources := append(append([]config.LivePlaylistSource(nil), global.Live.Sources...), global.Live.PlaylistSources...)
	settings.LiveTV.Sources = dropWithheldLiveSources(settings.LiveTV.Sources, sources, profileID, accountID, false)
	settings.LiveTV.PlaylistSources = dropWithheldLiveSources(settings.LiveTV.PlaylistSources, sources, profileID, accountID, false)
}

// In account-managed mode a profile can restrict the account's catalog, but
// cannot replace its provider connections through an older settings document.
func accountManagedLivePreferences(preferences *models.UserSettings) *models.UserSettings {
	if preferences == nil {
		return nil
	}
	copy := *preferences
	live := preferences.LiveTV
	copy.LiveTV = models.LiveTVSettings{
		HiddenChannels:     live.HiddenChannels,
		FavoriteChannels:   live.FavoriteChannels,
		SelectedCategories: live.SelectedCategories,
	}
	return &copy
}

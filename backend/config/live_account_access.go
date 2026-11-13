package config

import "strings"

// LiveAccountsManaged becomes true as soon as explicit account rules are used.
func LiveAccountsManaged(live LiveSettings) bool {
	if live.RequireAccountAssignment || live.AllowedAccounts != nil {
		return true
	}
	for _, source := range append(append([]LivePlaylistSource(nil), live.Sources...), live.PlaylistSources...) {
		if source.AllowedAccounts != nil {
			return true
		}
	}
	return false
}

// IsLiveAccountAllowed distinguishes legacy visibility from an explicit empty
// assignment. New providers can default to no accounts without granting access.
func IsLiveAccountAllowed(allowed *[]string, accountID string, required bool) bool {
	if allowed == nil {
		return !required
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return false
	}
	for _, id := range *allowed {
		if strings.TrimSpace(id) == accountID {
			return true
		}
	}
	return false
}

// FilterLiveSettingsForAccount is intersected with existing profile restrictions.
// The input remains unchanged; guide and filtering preferences are preserved.
func FilterLiveSettingsForAccount(settings Settings, accountID string) Settings {
	live := settings.Live
	hasNamedSources := len(live.Sources) > 0 || len(live.PlaylistSources) > 0
	filter := func(sources []LivePlaylistSource) []LivePlaylistSource {
		if len(sources) == 0 {
			return sources
		}
		result := make([]LivePlaylistSource, 0, len(sources))
		for _, source := range sources {
			if IsLiveAccountAllowed(source.AllowedAccounts, accountID, live.RequireAccountAssignment) {
				result = append(result, source)
			}
		}
		return result
	}
	live.Sources = filter(live.Sources)
	live.PlaylistSources = filter(live.PlaylistSources)
	if hasNamedSources || !IsLiveAccountAllowed(live.AllowedAccounts, accountID, live.RequireAccountAssignment) {
		live.EPG.XmltvUrl = ""
		live.EPG.Sources = nil
		live.Mode = ""
		live.PlaylistURL = ""
		live.HDHomeRunHost = ""
		live.HDHomeRunGuideEmail = ""
		live.HDHomeRunGuideDeviceIDs = ""
		live.ManifestURL = ""
		live.XtreamHost, live.XtreamUsername, live.XtreamPassword = "", "", ""
		live.StalkerPortalURL, live.StalkerMAC = "", ""
		live.StalkerSerialNumber, live.StalkerDeviceID, live.StalkerDeviceID2 = "", "", ""
		live.StalkerSignature, live.StalkerModel = "", ""
	}
	settings.Live = live
	return settings
}

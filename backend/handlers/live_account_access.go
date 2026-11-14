package handlers

import (
	"net/http"
	"strings"

	"novastream/config"
	"novastream/internal/auth"
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

// liveProfileAccessible reports whether the caller may read Live TV as
// profileID. Live sources are resolved for the profile's own household, so a
// caller naming another household's profile (for example the master
// household's well-known "default" profile) would otherwise see that
// household's channels and provider URLs. The master account, requests with
// no profile, and internal requests that carry no account (recording jobs)
// keep their existing access.
func liveProfileAccessible(r *http.Request, users UsersProvider, profileID string) bool {
	profileID = strings.TrimSpace(profileID)
	accountID := auth.GetAccountID(r)
	if profileID == "" || accountID == "" || auth.IsMaster(r) || users == nil {
		return true
	}
	profile, ok := users.Get(profileID)
	return ok && profile.AccountID == accountID
}

// clearLiveProviderConnection removes a profile's own provider connection:
// the legacy single-provider fields, its source lists and its guide URL.
func clearLiveProviderConnection(live *models.LiveTVSettings) {
	keepLiveProviderConnection(live, models.LiveTVSettings{})
}

// keepLiveProviderConnection replaces dst's provider connection fields with
// src's, leaving dst's personal preferences (favorites, hidden channels,
// categories, filtering, tuning, stream limit and guide offsets) untouched.
func keepLiveProviderConnection(dst *models.LiveTVSettings, src models.LiveTVSettings) {
	dst.Mode, dst.PlaylistURL, dst.ManifestURL, dst.ProxyURL = src.Mode, src.PlaylistURL, src.ManifestURL, src.ProxyURL
	dst.HDHomeRunHost, dst.HDHomeRunGuideEmail, dst.HDHomeRunGuideDeviceIDs = src.HDHomeRunHost, src.HDHomeRunGuideEmail, src.HDHomeRunGuideDeviceIDs
	dst.Sources, dst.PlaylistSources, dst.SourcesOverride = src.Sources, src.PlaylistSources, src.SourcesOverride
	dst.XtreamHost, dst.XtreamUsername, dst.XtreamPassword = src.XtreamHost, src.XtreamUsername, src.XtreamPassword
	dst.StalkerPortalURL, dst.StalkerMAC, dst.StalkerSerialNumber = src.StalkerPortalURL, src.StalkerMAC, src.StalkerSerialNumber
	dst.StalkerDeviceID, dst.StalkerDeviceID2 = src.StalkerDeviceID, src.StalkerDeviceID2
	dst.StalkerSignature, dst.StalkerModel = src.StalkerSignature, src.StalkerModel
	var srcXmltv *string
	if src.EPG != nil {
		srcXmltv = src.EPG.XmltvUrl
	}
	if dst.EPG != nil || srcXmltv != nil {
		epg := models.EPGOverrides{}
		if dst.EPG != nil {
			epg = *dst.EPG
		}
		epg.XmltvUrl = srcXmltv
		dst.EPG = &epg
	}
}

// Filter account-page and API responses too: an obsolete profile copy must not
// disclose provider credentials after its account assignment has been revoked.
// The master account sees the stored document unchanged. Saves from other
// callers go through restoreScopedLiveSettings, so whatever is hidden here is
// kept rather than erased when a client sends back what it read.
func scopeUserLiveSettings(settings *models.UserSettings, manager *config.Manager, profileID, accountID string, isMaster bool) {
	if settings == nil || manager == nil || isMaster {
		return
	}
	global, err := manager.Load()
	if err != nil || config.LiveAccountsManaged(global.Live) {
		clearLiveProviderConnection(&settings.LiveTV)
		return
	}
	sources := append(append([]config.LivePlaylistSource(nil), global.Live.Sources...), global.Live.PlaylistSources...)
	settings.LiveTV.Sources = dropWithheldLiveSources(settings.LiveTV.Sources, sources, profileID, accountID, false)
	settings.LiveTV.PlaylistSources = dropWithheldLiveSources(settings.LiveTV.PlaylistSources, sources, profileID, accountID, false)
}

// restoreScopedLiveSettings prepares a full settings save from a caller that
// read the document through scopeUserLiveSettings. In account-managed mode the
// profile's provider connection is administrator-managed, so the stored one is
// kept whatever the body says; otherwise source copies hidden from the caller
// are put back. Personal Live TV preferences in the body are saved as sent.
func restoreScopedLiveSettings(incoming *models.UserSettings, stored *models.UserSettings, manager *config.Manager, profileID, accountID string, isMaster bool) {
	if incoming == nil || manager == nil || isMaster {
		return
	}
	var previous models.LiveTVSettings
	if stored != nil {
		previous = stored.LiveTV
	}
	global, err := manager.Load()
	if err != nil || config.LiveAccountsManaged(global.Live) {
		keepLiveProviderConnection(&incoming.LiveTV, previous)
		return
	}
	sources := append(append([]config.LivePlaylistSource(nil), global.Live.Sources...), global.Live.PlaylistSources...)
	incoming.LiveTV.Sources = appendHiddenLiveSources(incoming.LiveTV.Sources, previous.Sources,
		dropWithheldLiveSources(previous.Sources, sources, profileID, accountID, false))
	incoming.LiveTV.PlaylistSources = appendHiddenLiveSources(incoming.LiveTV.PlaylistSources, previous.PlaylistSources,
		dropWithheldLiveSources(previous.PlaylistSources, sources, profileID, accountID, false))
}

// appendHiddenLiveSources adds back the stored sources that were not shown
// (stored minus shown), unless the body already has a source with that ID.
func appendHiddenLiveSources(body, stored, shown []models.LivePlaylistSource) []models.LivePlaylistSource {
	visible := make(map[string]bool, len(shown))
	for _, source := range shown {
		visible[source.ID] = true
	}
	present := make(map[string]bool, len(body))
	for _, source := range body {
		present[source.ID] = true
	}
	for _, source := range stored {
		if !visible[source.ID] && !present[source.ID] {
			body = append(body, source)
		}
	}
	return body
}

// In account-managed mode a profile can restrict the account's catalog, but
// cannot replace its provider connections through an older settings document.
// Its personal preferences (favorites, hidden channels, categories, filtering,
// tuning, stream limit and guide offsets) still apply.
func accountManagedLivePreferences(preferences *models.UserSettings) *models.UserSettings {
	if preferences == nil {
		return nil
	}
	copy := *preferences
	clearLiveProviderConnection(&copy.LiveTV)
	return &copy
}

package handlers

import (
	"net/url"
	"reflect"
	"strings"

	"novastream/config"
	"novastream/internal/requestsecurity"
	"novastream/models"
)

// Avoid refreshing guides on ordinary profile edits such as channel favorites.
func liveGuideSourcesChanged(previous, next models.LiveTVSettings) bool {
	return !reflect.DeepEqual(previous.Sources, next.Sources) ||
		!reflect.DeepEqual(previous.PlaylistSources, next.PlaylistSources) ||
		!reflect.DeepEqual(previous.Mode, next.Mode) ||
		!reflect.DeepEqual(previous.HDHomeRunHost, next.HDHomeRunHost) ||
		!reflect.DeepEqual(previous.HDHomeRunGuideEmail, next.HDHomeRunGuideEmail) ||
		!reflect.DeepEqual(previous.HDHomeRunGuideDeviceIDs, next.HDHomeRunGuideDeviceIDs) ||
		!reflect.DeepEqual(previous.EPG, next.EPG)
}

// Profile tuner origins authorize lineup fetching only. Streaming on port 5004
// still requires the caller's visible configured catalog channel.
func configuredProfileLiveHostPolicy(manager *config.Manager, preferences LiveUserSettingsProvider) requestsecurity.RestrictedHostPolicy {
	base := configuredLiveHostPolicy(manager)
	profiles, ok := preferences.(interface{ GetUsersWithOverrides() map[string]bool })
	if !ok || manager == nil {
		return base
	}
	settings, err := manager.Load()
	if err != nil {
		return base
	}
	if config.LiveAccountsManaged(settings.Live) {
		return base
	}
	allowed := map[string]bool{}
	add := func(address string) {
		raw, err := config.HDHomeRunURL(address, "/lineup.m3u")
		if err != nil {
			return
		}
		u, _ := url.Parse(raw)
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		allowed[privateMediaEndpointKey(u.Hostname(), port)] = true
	}
	for id := range profiles.GetUsersWithOverrides() {
		profile, err := preferences.Get(id)
		if err != nil || profile == nil {
			continue
		}
		for _, source := range append(profile.LiveTV.Sources, profile.LiveTV.PlaylistSources...) {
			if strings.EqualFold(source.Mode, "hdhomerun") && (source.Enabled == nil || *source.Enabled) {
				add(source.HDHomeRunHost)
			}
		}
		mode := settings.Live.Mode
		if profile.LiveTV.Mode != nil {
			mode = *profile.LiveTV.Mode
		}
		if strings.EqualFold(mode, "hdhomerun") && profile.LiveTV.HDHomeRunHost != nil {
			add(*profile.LiveTV.HDHomeRunHost)
		}
	}
	return func(host, port string) bool {
		return (base != nil && base(host, port)) || allowed[privateMediaEndpointKey(host, port)]
	}
}

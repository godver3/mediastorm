package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/requestsecurity"
	"novastream/models"
)

var hdHomeRunChannelPath = regexp.MustCompile(`^/(auto|tuner[0-9]+)/((v[0-9]+(\.[0-9]+)?)|(ch[0-9]+(-[0-9]+)?))$`)

func requireLiveStreamProfile(w http.ResponseWriter, r *http.Request, users UsersProvider) bool {
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID == "" || users == nil || auth.IsMaster(r) {
		return true
	}
	profile, ok := users.Get(profileID)
	if !ok || auth.GetAccountID(r) == "" || profile.AccountID != auth.GetAccountID(r) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return false
	}
	return true
}

// authorizeLiveStreamURL grants the extra tuner port only to a channel in the
// caller's visible catalog from a configured native HDHomeRun lineup. Generic
// media requests retain the ordinary configured-origin policy.
func authorizeLiveStreamURL(r *http.Request, raw string, manager ConfigProvider, catalog LiveChannelProvider) (bool, error) {
	raw = strings.TrimSpace(raw)
	if manager != nil {
		settings, err := manager.Load()
		if err != nil {
			return false, errors.New("live access unavailable")
		}
		profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
		// The master account without a profile keeps its global access, as in
		// the guide (filterEPGChannelIDs).
		if config.LiveAccountsManaged(settings.Live) && (profileID != "" || !auth.IsMaster(r)) {
			channels, err := visibleLiveChannels(r, catalog, profileID)
			if err != nil {
				return false, errors.New("live channel not available")
			}
			if !liveStreamURLInCatalog(r, raw, profileID, channels, catalog) {
				return false, errors.New("live channel not available")
			}
		}
	}
	basePolicy := configuredProviderHostPolicy(manager)
	if err := requestsecurity.ValidateOutboundURL(r.Context(), raw, basePolicy); err == nil {
		return false, nil
	}
	denied := errors.New("external media URL is not allowed")
	stream, err := url.Parse(raw)
	if err != nil || !isHDHomeRunStreamURL(stream) || manager == nil || catalog == nil {
		return false, denied
	}
	settings, err := manager.Load()
	if err != nil {
		return false, denied
	}
	query := r.URL.Query()
	settings = config.FilterSettingsForProfile(settings, query.Get("profileId"))
	sourceID := strings.TrimSpace(query.Get("sourceId"))
	if sourceID == "" {
		sourceID = strings.TrimSpace(query.Get("liveSourceId"))
	}
	eligible := make(map[string]bool)
	resolved := buildGlobalLiveSource(settings)
	if resolver, ok := catalog.(interface {
		resolveProfileLiveSource(*http.Request, config.Settings) models.ResolvedLiveSource
	}); ok {
		resolved = resolver.resolveProfileLiveSource(r, settings)
	}
	for _, source := range selectM3USources(resolvedLiveSources(resolved), sourceID) {
		playlist, err := url.Parse(source.PlaylistURL)
		if err == nil && source.Mode == "m3u" && playlist != nil &&
			(playlist.Scheme == "http" || playlist.Scheme == "https") && playlist.Path == "/lineup.m3u" &&
			privateMediaEndpointKey(playlist.Hostname(), "5004") == privateMediaEndpointKey(stream.Hostname(), "5004") {
			eligible[source.ID] = true
		}
	}
	if len(eligible) == 0 {
		return false, denied
	}
	request := r.Clone(r.Context())
	request.URL = cloneURL(r.URL)
	if sourceID != "" {
		query.Set("sourceId", sourceID)
	}
	request.URL.RawQuery = query.Encode()
	channels, err := catalog.FetchFilteredChannelsForRequest(request)
	if err != nil {
		return false, denied
	}
	channelID := strings.TrimSpace(query.Get("channelId"))
	for _, channel := range channels {
		if eligible[channel.SourceID] && strings.TrimSpace(channel.URL) == raw &&
			(channelID == "" || channelID == channel.ID || channelID == channel.PlaybackID) {
			policy := func(host, port string) bool {
				return privateMediaEndpointKey(host, port) == privateMediaEndpointKey(stream.Hostname(), "5004")
			}
			if err := requestsecurity.ValidateOutboundURL(r.Context(), raw, policy); err == nil {
				return true, nil
			}
		}
	}
	return false, denied
}

func isHDHomeRunStreamURL(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "http" && parsed.Hostname() != "" && parsed.Port() == "5004" &&
		parsed.User == nil && parsed.Fragment == "" && parsed.RawPath == "" && hdHomeRunChannelPath.MatchString(parsed.Path)
}

// catchupURLIssuer is implemented by the Live TV handler, which remembers the
// catch-up URLs it handed to each profile.
type catchupURLIssuer interface {
	catchupURLIssued(profileID, raw string) bool
}

// liveStreamURLInCatalog reports whether raw is a visible channel's stream URL
// or a catch-up (archive) URL that GetCatchup issued to this profile for one
// of its visible channels. Catch-up URLs carry a different path and time
// parameters, so they never match a channel URL exactly.
func liveStreamURLInCatalog(r *http.Request, raw, profileID string, channels []LiveChannel, catalog LiveChannelProvider) bool {
	for _, channel := range channels {
		if strings.TrimSpace(channel.URL) == raw {
			return true
		}
	}
	issuer, ok := catalog.(catchupURLIssuer)
	return ok && issuer.catchupURLIssued(profileID, raw)
}

package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"novastream/config"
	"novastream/internal/auth"
)

func liveAccountPolicyEnabled(manager *config.Manager) (bool, error) {
	if manager == nil {
		return false, nil
	}
	settings, err := manager.Load()
	if err != nil {
		return false, err
	}
	return config.LiveAccountsManaged(settings.Live), nil
}

func visibleLiveChannels(r *http.Request, provider LiveChannelProvider, profileID string) ([]LiveChannel, error) {
	if provider == nil || strings.TrimSpace(profileID) == "" {
		return nil, errors.New("profile channel access unavailable")
	}
	request := r.Clone(r.Context())
	request.Method = http.MethodGet
	request.Body = nil
	request.GetBody = nil
	u := *r.URL
	u.RawQuery = url.Values{"profileId": {profileID}}.Encode()
	request.URL = &u
	return provider.FetchFilteredChannelsForRequest(request)
}

func liveChannelMatches(channel LiveChannel, id string) bool {
	return id != "" && (id == channel.ID || id == channel.TvgID || id == channel.TvgName || id == channel.Name)
}

func filterEPGChannelIDs(r *http.Request, manager *config.Manager, provider LiveChannelProvider, ids []string) ([]string, error) {
	enabled, err := liveAccountPolicyEnabled(manager)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return ids, nil
	}
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	if profileID == "" && auth.IsMaster(r) {
		return ids, nil
	}
	channels, err := visibleLiveChannels(r, provider, profileID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	for _, channel := range channels {
		for _, id := range []string{channel.ID, channel.TvgID, channel.TvgName, channel.Name} {
			if id != "" {
				allowed[id] = true
			}
		}
	}
	// Channels the profile cannot see are dropped; none left is an empty
	// guide, not an error. Single-channel routes treat an empty result as 404.
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[strings.TrimSpace(id)] {
			result = append(result, id)
		}
	}
	return result, nil
}

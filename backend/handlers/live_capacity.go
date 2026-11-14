package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"novastream/config"
	"novastream/internal/livecapacity"
	"novastream/models"
)

// Capacity belongs to the authorized channel's provider account, never to an
// unverified sourceId chosen by the client. Ambiguous multi-source requests fail
// closed instead of silently charging the first (possibly unlimited) source.
func resolveLiveCapacityTarget(r *http.Request, raw string, settings config.Settings, resolved models.ResolvedLiveSource, catalog LiveChannelProvider) (liveStreamTarget, error) {
	targets := resolveLiveStreamTargets(resolved, nil)
	sourceID := strings.TrimSpace(r.URL.Query().Get("sourceId"))
	if sourceID == "" {
		sourceID = strings.TrimSpace(r.URL.Query().Get("liveSourceId"))
	}
	var selected *liveStreamTarget
	if len(targets) == 1 && (sourceID == "" || sourceID == targets[0].SourceID) {
		selected = &targets[0]
	} else if catalog != nil {
		channels, err := catalog.FetchFilteredChannelsForRequest(r)
		if err != nil {
			return liveStreamTarget{}, errors.New("live channel access unavailable")
		}
		for _, channel := range channels {
			if strings.TrimSpace(channel.URL) != strings.TrimSpace(raw) {
				continue
			}
			for i := range targets {
				if targets[i].SourceID != channel.SourceID || (sourceID != "" && sourceID != channel.SourceID) {
					continue
				}
				if selected != nil && selected.BucketKey != targets[i].BucketKey {
					return liveStreamTarget{}, errors.New("ambiguous live provider")
				}
				selected = &targets[i]
			}
		}
		if selected == nil {
			if issuer, ok := catalog.(catchupURLIssuer); ok && issuer.catchupURLIssued(r.URL.Query().Get("profileId"), raw) {
				for i := range targets {
					if sourceID != "" && targets[i].SourceID == sourceID {
						selected = &targets[i]
						break
					}
				}
			}
		}
	}
	if selected == nil {
		return liveStreamTarget{}, errors.New("live provider could not be verified")
	}
	result := *selected
	// Include aliases hidden from this profile: a second configuration of one
	// provider account cannot create another budget or weaken its configured cap.
	all := append(resolveLiveStreamTargets(buildGlobalLiveSource(settings), nil), targets...)
	for _, alias := range all {
		if alias.BucketKey == result.BucketKey && alias.MaxStreams > 0 && (result.MaxStreams == 0 || alias.MaxStreams < result.MaxStreams) {
			result.MaxStreams = alias.MaxStreams
		}
	}
	return result, nil
}

func (h *VideoHandler) liveCapacityTarget(r *http.Request, raw string) (liveStreamTarget, error) {
	var settings config.Settings
	if h.configManager != nil {
		var err error
		settings, err = h.configManager.Load()
		if err != nil {
			return liveStreamTarget{}, errors.New("live settings unavailable")
		}
	}
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	var preferences *models.UserSettings
	if h.userSettingsSvc != nil {
		var err error
		preferences, err = h.userSettingsSvc.Get(profileID)
		if err != nil {
			return liveStreamTarget{}, errors.New("live profile settings unavailable")
		}
	}
	scoped := config.FilterLiveSettingsForAccount(settings, liveAccountForProfile(h.usersSvc, profileID))
	resolved := applyProfileLiveSources(buildGlobalLiveSource(config.FilterSettingsForProfile(scoped, profileID)), settings, profileID, preferences, liveAccountForProfile(h.usersSvc, profileID))
	return resolveLiveCapacityTarget(r, raw, settings, resolved, h.liveChannels)
}

func (h *LiveHandler) liveCapacityTarget(r *http.Request, raw string) (liveStreamTarget, error) {
	var settings config.Settings
	if h.cfgManager != nil {
		var err error
		settings, err = h.cfgManager.Load()
		if err != nil {
			return liveStreamTarget{}, errors.New("live settings unavailable")
		}
	}
	return resolveLiveCapacityTarget(r, raw, settings, h.resolveProfileLiveSource(r, settings), h)
}

func writeLiveCapacityError(w http.ResponseWriter, target liveStreamTarget) {
	current, limit := livecapacity.Default.Usage(target.BucketKey, target.MaxStreams)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": "LIVE_STREAM_LIMIT_REACHED", "message": "All IPTV connections are in use. Try again after a stream stops.",
		"provider": target.Provider, "currentStreams": current, "maxStreams": limit,
		"available": 0, "atLimit": true,
	})
}

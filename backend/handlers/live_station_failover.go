package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"novastream/internal/livecapacity"
	"novastream/models"
)

const liveStationMaxAttempts = 4
const liveStationStartupTimeout = 8 * time.Second
const liveStationCooldown = 2 * time.Minute

var errLiveStationExhausted = errors.New("no other permitted source is available for this channel")

type liveStationCandidate struct {
	channel   LiveChannel
	target    liveStreamTarget
	hdHomeRun bool
}

// A plan belongs to one managed HLS session. Candidates always originate from
// the profile's visible catalog; clients cannot submit fallback URLs.
type liveStationFailover struct {
	request    *http.Request
	handler    *VideoHandler
	candidates []LiveChannel
	stationID  string
	index      int
	attempts   int
	prepare    func(context.Context, LiveChannel) (liveStationCandidate, error)
}

func (h *VideoHandler) liveStationPlan(r *http.Request, raw string) *liveStationFailover {
	if h.configManager == nil || h.liveChannels == nil {
		return nil
	}
	settings, err := h.configManager.Load()
	if err != nil || !settings.Live.AutoChannelFailover || strings.EqualFold(r.URL.Query().Get("autoFailover"), "false") {
		return nil
	}
	if h.usersSvc != nil {
		if user, ok := h.usersSvc.Get(r.URL.Query().Get("profileId")); ok && user.IsKidsProfile {
			return nil
		}
	}
	request := r.Clone(context.WithoutCancel(r.Context()))
	request.URL = cloneURL(r.URL)
	// View filters must not hide an otherwise allowed fallback. The catalog
	// still applies account ownership, allowed profiles and source filters.
	query := request.URL.Query()
	for _, key := range []string{"sourceId", "liveSourceId", "category", "filter", "favoriteId", "offset", "limit"} {
		query.Del(key)
	}
	request.URL.RawQuery = query.Encode()
	// Keep authentication values while bounding the lookup.
	lookup := request.Clone(r.Context())
	lookupCtx, lookupCancel := context.WithTimeout(lookup.Context(), 5*time.Second)
	defer lookupCancel()
	channels, err := h.liveChannels.FetchFilteredChannelsForRequest(lookup.Clone(lookupCtx))
	if err != nil {
		return nil
	}
	channels = annotateLiveStations(channels, settings.Live.StationMappings)
	sourceID := r.URL.Query().Get("sourceId")
	if sourceID == "" {
		sourceID = r.URL.Query().Get("liveSourceId")
	}
	var initial *LiveChannel
	for i := range channels {
		if channels[i].URL == raw && (sourceID == "" || channels[i].SourceID == sourceID) {
			initial = &channels[i]
			break
		}
	}
	if initial == nil {
		return nil
	}
	candidates := []LiveChannel{*initial}
	seen := map[string]bool{initial.URL: true}
	for _, channel := range channels {
		if channel.StationID == initial.StationID && !seen[channel.URL] {
			seen[channel.URL] = true
			candidates = append(candidates, channel)
		}
	}
	if len(candidates) < 2 {
		return nil
	}
	profileID := r.URL.Query().Get("profileId")
	h.liveStationHealthMu.Lock()
	good := h.liveStationLastGood[profileID+"|"+initial.StationID]
	h.liveStationHealthMu.Unlock()
	sort.SliceStable(candidates[1:], func(i, j int) bool { return candidates[i+1].ID == good && candidates[j+1].ID != good })
	plan := &liveStationFailover{request: request, handler: h, candidates: candidates, stationID: initial.StationID, attempts: 1}
	plan.prepare = func(ctx context.Context, channel LiveChannel) (liveStationCandidate, error) {
		// Retain the original account/session values, but tie new network work
		// to the live session rather than the completed start HTTP request.
		bound, stop := context.WithTimeout(request.Context(), 5*time.Second)
		unlink := context.AfterFunc(ctx, stop)
		defer unlink()
		defer stop()
		next := request.Clone(bound)
		next.URL = cloneURL(request.URL)
		q := next.URL.Query()
		q.Set("sourceId", channel.SourceID)
		q.Set("channelId", channel.PlaybackID)
		if channel.PlaybackID == "" {
			q.Set("channelId", channel.ID)
		}
		next.URL.RawQuery = q.Encode()
		if h.usersSvc != nil {
			user, ok := h.usersSvc.Get(profileID)
			if !ok || user.IsKidsProfile {
				return liveStationCandidate{}, errors.New("live profile unavailable")
			}
		}
		if !requireLiveStreamProfile(httptest.NewRecorder(), next, h.usersSvc) {
			return liveStationCandidate{}, errors.New("live profile unavailable")
		}
		hdHomeRun, err := authorizeLiveStreamURL(next, channel.URL, h.configManager, h.liveChannels)
		if err != nil {
			return liveStationCandidate{}, err
		}
		target, err := h.liveCapacityTarget(next, channel.URL)
		if err != nil {
			return liveStationCandidate{}, err
		}
		if target.Provider != "m3u" && target.Provider != "xtream" {
			return liveStationCandidate{}, errors.New("provider requires a separate tune operation")
		}
		// Recheck the current catalog even when legacy host policy would
		// authorize arbitrary paths on a configured provider origin.
		visible, err := h.liveChannels.FetchFilteredChannelsForRequest(next)
		if err != nil {
			return liveStationCandidate{}, err
		}
		for _, current := range visible {
			if current.ID == channel.ID && current.SourceID == channel.SourceID && current.URL == channel.URL {
				currentSettings, loadErr := h.configManager.Load()
				if loadErr != nil || !currentSettings.Live.AutoChannelFailover {
					return liveStationCandidate{}, errors.New("channel failover unavailable")
				}
				cap := currentSettings.Filtering.MaxResolution
				if h.userSettingsSvc != nil {
					preferences, prefErr := h.userSettingsSvc.Get(profileID)
					if prefErr != nil {
						return liveStationCandidate{}, errors.New("live preferences unavailable")
					}
					if preferences != nil && preferences.Filtering.MaxResolution != nil {
						cap = models.StringVal(preferences.Filtering.MaxResolution, "")
					}
				}
				if clientCap := request.URL.Query().Get("maxResolution"); stationVariantHeight(clientCap) > 0 && (stationVariantHeight(cap) == 0 || stationVariantHeight(clientCap) < stationVariantHeight(cap)) {
					cap = clientCap
				}
				if !stationFallbackQualityAllowed(current.Name, cap) {
					return liveStationCandidate{}, errors.New("channel variant exceeds playback quality")
				}
				currentID, _ := liveStationIdentity(current, currentSettings.Live.StationMappings)
				if currentID == plan.stationID {
					return liveStationCandidate{current, target, hdHomeRun}, nil
				}
			}
		}
		return liveStationCandidate{}, errors.New("live channel access changed")
	}
	return plan
}

func (p *liveStationFailover) healthKey(channel LiveChannel) string {
	return p.request.URL.Query().Get("profileId") + "|" + p.stationID + "|" + channel.SourceID + "|" + channel.ID
}

func (p *liveStationFailover) failed() {
	if p.handler == nil {
		return
	}
	h := p.handler
	h.liveStationHealthMu.Lock()
	defer h.liveStationHealthMu.Unlock()
	if h.liveStationFailures == nil {
		h.liveStationFailures = make(map[string]time.Time)
	}
	for key, until := range h.liveStationFailures {
		if time.Now().After(until) {
			delete(h.liveStationFailures, key)
		}
	}
	if len(h.liveStationFailures) < 2048 {
		h.liveStationFailures[p.healthKey(p.candidates[p.index])] = time.Now().Add(liveStationCooldown)
	}
}

func (p *liveStationFailover) recordSuccess(channel LiveChannel) {
	if p.handler == nil {
		return
	}
	h := p.handler
	h.liveStationHealthMu.Lock()
	defer h.liveStationHealthMu.Unlock()
	if h.liveStationLastGood == nil {
		h.liveStationLastGood = make(map[string]string)
	}
	if len(h.liveStationLastGood) > 2048 {
		clear(h.liveStationLastGood)
	}
	h.liveStationLastGood[p.request.URL.Query().Get("profileId")+"|"+p.stationID] = channel.ID
	delete(h.liveStationFailures, p.healthKey(channel))
}

// Called only after startLiveTranscoding has joined FFmpeg and closed its
// input. Other viewers on the old shared upstream retain their own leases.
func (p *liveStationFailover) next(ctx context.Context, session *HLSSession) error {
	p.failed()
	session.mu.Lock()
	old := session.liveCapacityLease
	session.liveCapacityLease = nil
	session.mu.Unlock()
	old.Release()
	capacityBlocked := false
	for p.index+1 < len(p.candidates) && p.attempts < liveStationMaxAttempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.index++
		p.attempts++
		channel := p.candidates[p.index]
		if p.handler != nil {
			p.handler.liveStationHealthMu.Lock()
			cooldown := p.handler.liveStationFailures[p.healthKey(channel)]
			p.handler.liveStationHealthMu.Unlock()
			if time.Now().Before(cooldown) {
				continue
			}
		}
		candidate, err := p.prepare(ctx, channel)
		if err != nil {
			continue
		}
		session.mu.RLock()
		tuning := session.LiveTuning
		session.mu.RUnlock()
		tuning.MaxStreams = candidate.target.MaxStreams
		tuning.ProxyURL = candidate.target.ProxyURL
		tuning.ProbeSizeMB = candidate.target.ProbeSizeMB
		tuning.AnalyzeDurationSec = candidate.target.AnalyzeDurationSec
		tuning.LowLatency = candidate.target.LowLatency
		tuning.RequestHeaders = nil
		tuning.ForceHLSInput = false
		tuning.HDHomeRunInput = candidate.hdHomeRun
		key := liveUpstreamShareKey(candidate.channel.URL, tuning)
		lease, err := livecapacity.Default.Acquire(candidate.target.BucketKey, key, candidate.target.MaxStreams)
		if err != nil {
			if errors.Is(err, livecapacity.ErrLimit) {
				capacityBlocked = true
				continue
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			lease.Release()
			return err
		}
		session.mu.Lock()
		session.Path = candidate.channel.URL
		session.LiveProvider = candidate.target.Provider
		session.LiveBucket = candidate.target.BucketKey
		session.LiveUpstreamKey = key
		session.LiveTuning = tuning
		session.liveCapacityLease = lease
		session.LiveSourceID = candidate.channel.SourceID
		session.LiveSourceName = candidate.channel.SourceName
		session.LiveFailoverAttempts = p.attempts - 1
		session.mu.Unlock()
		return nil
	}
	if capacityBlocked {
		return livecapacity.ErrLimit
	}
	return errLiveStationExhausted
}

func stationVariantHeight(name string) int {
	name = strings.ToLower(name)
	for _, pair := range []struct {
		terms  []string
		height int
	}{
		{[]string{"8k", "4320"}, 4320}, {[]string{"4k", "uhd", "2160"}, 2160},
		{[]string{"fhd", "1080"}, 1080}, {[]string{"hd", "720"}, 720}, {[]string{"sd", "480", "576"}, 576},
	} {
		for _, term := range pair.terms {
			if strings.Contains(name, term) {
				return pair.height
			}
		}
	}
	return 0
}

func stationFallbackQualityAllowed(candidate, cap string) bool {
	limit := stationVariantHeight(cap)
	if limit == 0 {
		return true
	}
	height := stationVariantHeight(candidate)
	return height > 0 && height <= limit
}

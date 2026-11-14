package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"novastream/internal/livecapacity"
	"novastream/models"
)

func (h *LiveHandler) ReserveRecording(ctx context.Context, recording models.Recording) (func(), error) {
	// Scheduled jobs have no HTTP middleware, so recheck the upstream kids
	// policy here as well as the current channel/account assignment.
	if h.usersSvc != nil {
		profile, ok := h.usersSvc.Get(recording.UserID)
		if !ok || profile.IsKidsProfile {
			return nil, errors.New("recording profile unavailable")
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/live/channels", nil)
	if err != nil {
		return nil, err
	}
	query := request.URL.Query()
	query.Set("profileId", recording.UserID)
	request.URL.RawQuery = query.Encode()
	target, err := h.liveCapacityTarget(request, recording.SourceURL)
	if err != nil {
		return nil, err
	}
	// The recorder currently opens its own FFmpeg input, even on a channel
	// already being watched. It therefore consumes a private connection.
	lease, err := livecapacity.Default.Acquire(target.BucketKey, "", target.MaxStreams)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

func (h *RecordingsHandler) bindRecordingChannel(w http.ResponseWriter, r *http.Request, profileID string, payload *createRecordingPayload) bool {
	enabled, err := liveAccountPolicyEnabled(h.cfgManager)
	if err != nil {
		http.Error(w, "channel access unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !enabled {
		return true
	}
	channels, err := visibleLiveChannels(r, h.liveChannels, profileID)
	if err != nil {
		http.Error(w, "channel not available", http.StatusNotFound)
		return false
	}
	for _, channel := range channels {
		if !liveChannelMatches(channel, payload.ChannelID) && !liveChannelMatches(channel, payload.TvgID) {
			continue
		}
		parsed, err := url.Parse(channel.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			http.Error(w, "channel does not provide a recordable source", http.StatusBadRequest)
			return false
		}
		// Never trust a submitted source URL once providers have account owners.
		payload.SourceURL = channel.URL
		payload.ChannelID = channel.ID
		payload.TvgID = channel.TvgID
		payload.ChannelName = channel.Name
		return true
	}
	http.Error(w, "channel not available", http.StatusNotFound)
	return false
}

// AuthorizeRecording also runs for queued and rule-created jobs at actual start.
func (h *LiveHandler) AuthorizeRecording(ctx context.Context, recording models.Recording) (string, error) {
	enabled, err := liveAccountPolicyEnabled(h.cfgManager)
	if err != nil {
		return "", err
	}
	if !enabled {
		return recording.SourceURL, nil
	}
	channels, err := h.RecordingChannels(ctx, recording.UserID)
	if err != nil {
		return "", errors.New("channel not available")
	}
	for _, channel := range channels {
		if channel.ID == recording.ChannelID || (recording.TvgID != "" && channel.TvgID == recording.TvgID) {
			parsed, err := url.Parse(channel.SourceURL)
			if err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
				return channel.SourceURL, nil
			}
		}
	}
	return "", errors.New("channel not available")
}

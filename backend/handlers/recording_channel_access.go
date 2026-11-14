package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"novastream/models"
)

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

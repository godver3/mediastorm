package handlers

import (
	"net/http"
	"net/url"

	"novastream/internal/profileaccess"
)

func (h *VideoHandler) requireHLSSessionAccess(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	// Read identity without touching LastAccess: a denied request must neither
	// keep somebody else's stream alive nor disclose its playback status.
	h.hlsManager.mu.RLock()
	session, exists := h.hlsManager.sessions[sessionID]
	h.hlsManager.mu.RUnlock()
	if !exists {
		http.NotFound(w, r)
		return false
	}
	session.mu.RLock()
	profileID, resource := session.ProfileID, session.RequestedSource
	isLive := session.IsLive
	liveChannelID, liveSourceID := session.LiveChannelID, session.LiveSourceID
	if resource == "" {
		resource = session.OriginalPath
	}
	if resource == "" {
		resource = session.Path
	}
	session.mu.RUnlock()
	if !profileaccess.CheckResource(w, r, profileID, resource) {
		return false
	}
	if isLive {
		request := r.Clone(r.Context())
		u := *r.URL
		u.RawQuery = url.Values{"profileId": {profileID}, "channelId": {liveChannelID}, "sourceId": {liveSourceID}}.Encode()
		request.URL = &u
		return h.requireLiveProgramme(w, request, resource)
	}
	return true
}

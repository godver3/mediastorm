package handlers

import (
	"net/http"

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
	if resource == "" {
		resource = session.OriginalPath
	}
	if resource == "" {
		resource = session.Path
	}
	session.mu.RUnlock()
	return profileaccess.CheckResource(w, r, profileID, resource)
}

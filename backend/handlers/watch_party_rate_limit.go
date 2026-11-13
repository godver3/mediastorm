package handlers

import (
	"net/http"

	"github.com/gorilla/mux"
)

// GuestRateLimitKey identifies a validated guest bound to this exact room. Never
// use an unverified cookie as a key: rotating random cookies must not mint quotas.
func (h *WatchPartyHandler) GuestRateLimitKey(r *http.Request) (string, bool) {
	roomID := mux.Vars(r)["roomID"]
	if roomID == "" {
		return "", false
	}
	_, guestID, ok := h.guestSession(r, roomID)
	return guestID, ok
}

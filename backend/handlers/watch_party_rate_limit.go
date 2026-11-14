package handlers

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"novastream/models"
)

type verifiedGuestContextKey struct{}

type verifiedGuest struct {
	roomID  string
	session models.Session
	guestID string
}

// GuestRateLimitKey identifies a validated guest bound to this exact room. Never
// use an unverified cookie as a key: rotating random cookies must not mint quotas.
//
// The returned request carries the validated session so the handler does not
// load it a second time.
func (h *WatchPartyHandler) GuestRateLimitKey(r *http.Request) (*http.Request, string, bool) {
	roomID := mux.Vars(r)["roomID"]
	if roomID == "" {
		return r, "", false
	}
	session, guestID, err := h.lookupGuestSession(r, roomID)
	if err != nil {
		return r, "", false
	}
	verified := verifiedGuest{roomID: roomID, session: session, guestID: guestID}
	return r.WithContext(context.WithValue(r.Context(), verifiedGuestContextKey{}, verified)), guestID, true
}

func verifiedGuestFromContext(r *http.Request, roomID string) (models.Session, string, bool) {
	verified, ok := r.Context().Value(verifiedGuestContextKey{}).(verifiedGuest)
	if !ok || verified.roomID != roomID {
		return models.Session{}, "", false
	}
	return verified.session, verified.guestID, true
}

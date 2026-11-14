package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/sessions"
)

// A guest whose session cannot be loaded (for example during a brief database
// outage) must get a retryable 503, not the 401 the web player treats as the
// end of the room.
func TestWatchPartyGuestSessionStoreFailureIsRetryable(t *testing.T) {
	storeDown := true
	guest := models.Session{
		Token: "guest-token", AccountID: "account-1", ExpiresAt: time.Now().Add(time.Hour),
		Scope: models.SessionScopeWatchParty, ScopeResource: "room-1",
	}
	sessionService := sessions.NewServiceWithLookup(func(_ context.Context, token string) (*models.Session, error) {
		if storeDown {
			return nil, errors.New("connection refused")
		}
		if token == guest.Token {
			copy := guest
			return &copy, nil
		}
		return nil, nil
	}, time.Hour)
	handler := &WatchPartyHandler{sessions: sessionService}

	request := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/watch-party/api/rooms/room-1", nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: watchPartyCookieName, Value: token})
		}
		rec := httptest.NewRecorder()
		handler.requireGuestSession(rec, req, "room-1")
		return rec.Code
	}

	if got := request(guest.Token); got != http.StatusServiceUnavailable {
		t.Fatalf("store failure status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	storeDown = false
	if got := request("unknown-token"); got != http.StatusUnauthorized {
		t.Fatalf("unknown session status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := request(""); got != http.StatusUnauthorized {
		t.Fatalf("missing cookie status = %d, want %d", got, http.StatusUnauthorized)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: watchPartyCookieName, Value: guest.Token})
	if _, guestID, ok := handler.requireGuestSession(rec, req, "room-1"); !ok || guestID == "" {
		t.Fatalf("valid guest rejected: status %d", rec.Code)
	}
}

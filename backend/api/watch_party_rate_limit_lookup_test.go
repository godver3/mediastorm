package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/handlers"
	"novastream/models"
	"novastream/services/sessions"
	"novastream/services/watchrooms"
)

// The rate limiter validates the guest once and hands the session to the
// handler, so each poll costs one session lookup rather than two.
func TestWatchPartyGuestPollLoadsSessionOnce(t *testing.T) {
	var lookups atomic.Int32
	guest := models.Session{
		Token: "guest-token", AccountID: "account", ExpiresAt: time.Now().Add(time.Hour),
		Scope: models.SessionScopeWatchParty, ScopeResource: "room",
	}
	svc := sessions.NewServiceWithLookup(func(_ context.Context, token string) (*models.Session, error) {
		lookups.Add(1)
		if token != guest.Token {
			return nil, nil
		}
		copy := guest
		return &copy, nil
	}, time.Hour)
	h := handlers.NewWatchPartyHandler(watchrooms.New(rateTestRoomRepo{}, nil, nil), svc, "")
	router := mux.NewRouter()
	registerWatchPartyGuestRoutes(router.PathPrefix("/api").Subrouter(), h)

	if rec := rateTestRequest(router, guest.Token, "/api/watch-party/room/room"); rec.Code != 200 {
		t.Fatalf("guest poll: HTTP %d", rec.Code)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("session lookups per poll = %d, want 1", got)
	}
}

package api

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/time/rate"
	"novastream/handlers"
	"novastream/internal/apierror"
)

// Guest polling is budgeted by verified session, so household/NAT peers do not
// consume each other's quota. Unauthenticated traffic keeps a per-IP budget.
//
// guestKey validates the guest once and returns a request carrying that
// session, which the handler reuses instead of loading it again.
func watchPartyRateLimitHandlerFunc(limiter *IPRateLimiter, guestKey func(*http.Request) (*http.Request, string, bool), next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := "ip:" + getClientIP(r)
		r, guest, ok := guestKey(r)
		if ok {
			key = "guest:" + guest
		}
		if !limiter.getLimiter(key).Allow() {
			w.Header().Set("Retry-After", "1")
			apierror.Write(w, http.StatusTooManyRequests, apierror.CodeRateLimited, "too many requests")
			return
		}
		next(w, r)
	}
}

func registerWatchPartyGuestRoutes(router *mux.Router, handler *handlers.WatchPartyHandler) {
	if handler == nil {
		return
	}
	limiter := NewIPRateLimiter(rate.Every(time.Second), 20)
	wrap := func(next http.HandlerFunc) http.HandlerFunc {
		return watchPartyRateLimitHandlerFunc(limiter, handler.GuestRateLimitKey, next)
	}
	router.HandleFunc("/watch-party/room/{roomID}", wrap(handler.RoomSnapshot)).Methods(http.MethodGet)
	router.HandleFunc("/watch-party/room/{roomID}/heartbeat", wrap(handler.Heartbeat)).Methods(http.MethodPost)
	router.HandleFunc("/watch-party/room/{roomID}/ready", wrap(handler.Ready)).Methods(http.MethodPost)
	router.HandleFunc("/watch-party/room/{roomID}/playback-grant", wrap(handler.PlaybackGrant)).Methods(http.MethodPost)
}

// RegisterWatchPartyPageRoutes keeps anonymous invitation lookup and claiming
// rate limited by IP, independently of authenticated guests' polling budgets.
func RegisterWatchPartyPageRoutes(router *mux.Router, handler *handlers.WatchPartyHandler) {
	if handler == nil {
		return
	}
	invitations := NewIPRateLimiter(rate.Every(time.Second), 20)
	router.HandleFunc("/watch-party", handler.Landing).Methods(http.MethodGet)
	router.HandleFunc("/watch-party/code", RateLimitHandlerFunc(invitations, handler.ResolveCode)).Methods(http.MethodPost)
	router.HandleFunc("/watch-party/code/{code}", RateLimitHandlerFunc(invitations, handler.OpenCode)).Methods(http.MethodGet)
	router.HandleFunc("/watch-party/code/{code}", RateLimitHandlerFunc(invitations, handler.JoinCode)).Methods(http.MethodPost)
	router.HandleFunc("/watch-party/room/{roomID}", handler.RoomPage).Methods(http.MethodGet)
	router.HandleFunc("/watch-party/{token}", RateLimitHandlerFunc(invitations, handler.OpenToken)).Methods(http.MethodGet)
	router.HandleFunc("/watch-party/{token}", RateLimitHandlerFunc(invitations, handler.JoinToken)).Methods(http.MethodPost)
}

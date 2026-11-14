package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/handlers"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/sessions"
	"novastream/services/watchrooms"
)

type rateTestRoomRepo struct{ datastore.WatchRoomRepository }

func (rateTestRoomRepo) IsExternalGuest(context.Context, string, string) (bool, error) {
	return true, nil
}
func (rateTestRoomRepo) Get(context.Context, string) (*models.WatchRoom, error) {
	return &models.WatchRoom{ID: "room", Title: "Fixture", Status: "playing", AnchorUpdatedAt: time.Now()}, nil
}
func (rateTestRoomRepo) HeartbeatExternalGuest(context.Context, string, string, string, bool, time.Time) error {
	return nil
}
func (rateTestRoomRepo) GetExternalSource(context.Context, string) (*models.WatchRoomExternalSource, error) {
	return nil, nil
}

func rateTestRouter(t *testing.T) (*mux.Router, *sessions.Service) {
	t.Helper()
	svc, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.NewWatchPartyHandler(watchrooms.New(rateTestRoomRepo{}, nil, nil), svc, "")
	router := mux.NewRouter()
	registerWatchPartyGuestRoutes(router.PathPrefix("/api").Subrouter(), h)
	return router, svc
}
func rateTestGuest(t *testing.T, svc *sessions.Service, scope, room string) string {
	t.Helper()
	s, err := svc.CreateScopedWithResource("account", false, "browser", "192.0.2.1", time.Hour, scope, room)
	if err != nil {
		t.Fatal(err)
	}
	return s.Token
}
func rateTestRequest(router *mux.Router, token, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "192.0.2.1:4321"
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "strmr_watch_party", Value: token})
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
func TestWatchPartyGuestsBehindOneIPHaveIndependentBudgets(t *testing.T) {
	router, svc := rateTestRouter(t)
	guests := []string{rateTestGuest(t, svc, models.SessionScopeWatchParty, "room"), rateTestGuest(t, svc, models.SessionScopeWatchParty, "room")}
	for i := 0; i < 12; i++ {
		for index, token := range guests {
			rec := rateTestRequest(router, token, "/api/watch-party/room/room")
			if rec.Code != 200 {
				t.Fatalf("poll %d guest %d: HTTP %d, want 200", i, index, rec.Code)
			}
		}
	}
}
func TestWatchPartyOneGuestCannotExhaustAnotherGuestBudget(t *testing.T) {
	router, svc := rateTestRouter(t)
	first := rateTestGuest(t, svc, models.SessionScopeWatchParty, "room")
	second := rateTestGuest(t, svc, models.SessionScopeWatchParty, "room")
	for i := 0; i < 20; i++ {
		if rec := rateTestRequest(router, first, "/api/watch-party/room/room"); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	rec := rateTestRequest(router, first, "/api/watch-party/room/room")
	if rec.Code != 429 {
		t.Fatalf("burst exhausted: %d", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Header().Get("Retry-After") == "" || body["code"] != "rate_limited" {
		t.Fatal("missing retry guidance")
	}
	if rec := rateTestRequest(router, second, "/api/watch-party/room/room"); rec.Code != 200 {
		t.Fatalf("second guest: %d", rec.Code)
	}
}
func TestWatchPartyUnverifiedCookiesShareIPBudget(t *testing.T) {
	for _, kind := range []string{"missing", "random", "wrong scope", "wrong room", "revoked", "expired"} {
		t.Run(kind, func(t *testing.T) {
			router, svc := rateTestRouter(t)
			token := ""
			switch kind {
			case "wrong scope":
				token = rateTestGuest(t, svc, models.SessionScopeStream, "room")
			case "wrong room":
				token = rateTestGuest(t, svc, models.SessionScopeWatchParty, "another-room")
			case "expired":
				expired, err := svc.CreateScopedWithResource("account", false, "browser", "192.0.2.1", time.Nanosecond, models.SessionScopeWatchParty, "room")
				if err != nil {
					t.Fatal(err)
				}
				token = expired.Token
			case "revoked":
				token = rateTestGuest(t, svc, models.SessionScopeWatchParty, "room")
				_ = svc.Revoke(token)
			}
			for i := 0; i < 20; i++ {
				if kind == "random" {
					token = fmt.Sprintf("unverified-%d", i)
				}
				if r := rateTestRequest(router, token, "/api/watch-party/room/room"); r.Code != 401 {
					t.Fatalf("request %d: HTTP %d", i, r.Code)
				}
			}
			if r := rateTestRequest(router, token, "/api/watch-party/room/room"); r.Code != 429 {
				t.Fatalf("unverified burst: HTTP %d", r.Code)
			}
		})
	}
}

func (rateTestRoomRepo) GetExternalInviteByTokenHash(context.Context, string, time.Time) (*models.WatchRoomExternalInvite, error) {
	return nil, nil
}
func (rateTestRoomRepo) GetExternalInviteByCode(context.Context, string, time.Time) (*models.WatchRoomExternalInvite, error) {
	return nil, nil
}

func TestWatchPartyAnonymousInvitationLookupsRemainIPLimited(t *testing.T) {
	svc, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.NewWatchPartyHandler(watchrooms.New(rateTestRoomRepo{}, nil, nil), svc, "")
	router := mux.NewRouter()
	RegisterWatchPartyPageRoutes(router, h)
	for i := 0; i < 20; i++ {
		rec := rateTestRequest(router, fmt.Sprintf("unverified-%d", i), fmt.Sprintf("/watch-party/invalid-%d", i))
		if rec.Code != 200 {
			t.Fatalf("invitation %d: HTTP %d", i, rec.Code)
		}
	}
	if r := rateTestRequest(router, "", "/watch-party/another-invalid-token"); r.Code != 429 {
		t.Fatalf("anonymous invitation burst: HTTP %d", r.Code)
	}
}

func TestWatchPartyInvalidTrafficDoesNotStarveVerifiedGuest(t *testing.T) {
	router, svc := rateTestRouter(t)
	for i := 0; i < 21; i++ {
		rateTestRequest(router, "", "/api/watch-party/room/room")
	}
	token := rateTestGuest(t, svc, models.SessionScopeWatchParty, "room")
	if rec := rateTestRequest(router, token, "/api/watch-party/room/room"); rec.Code != 200 {
		t.Fatalf("verified guest: HTTP %d", rec.Code)
	}
	_ = svc.Revoke(token)
	if rec := rateTestRequest(router, token, "/api/watch-party/room/room"); rec.Code != 429 {
		t.Fatalf("revoked cookie must use exhausted anonymous budget: HTTP %d", rec.Code)
	}
}

func TestWatchPartyGuestCannotResetBudgetByChangingIP(t *testing.T) {
	router, svc := rateTestRouter(t)
	token := rateTestGuest(t, svc, models.SessionScopeWatchParty, "room")
	for i := 0; i < 20; i++ {
		rateTestRequest(router, token, "/api/watch-party/room/room")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/watch-party/room/room", nil)
	req.RemoteAddr = "198.51.100.2:1234"
	req.AddCookie(&http.Cookie{Name: "strmr_watch_party", Value: token})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("same guest from another IP: HTTP %d", rec.Code)
	}
}

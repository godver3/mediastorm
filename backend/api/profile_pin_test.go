package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/handlers"
	"novastream/internal/profileaccess"
	"novastream/services/accounts"
	"novastream/services/profileunlock"
	"novastream/services/sessions"
	"novastream/services/users"
)

type pinTestEnv struct {
	t        *testing.T
	router   *mux.Router
	sessions *sessions.Service
	users    *users.Service
	unlocks  *profileunlock.Service
	enforce  atomic.Bool
	ttlHours atomic.Int64
	account1 string
	account2 string
	locked   string // PIN-protected profile of account1
	open     string // profile without a PIN of account1
	foreign  string // PIN-protected profile of account2
	lastBody string // body seen by the echo handler
}

func newPinTestEnv(t *testing.T) *pinTestEnv {
	t.Helper()
	env := &pinTestEnv{t: t}
	env.ttlHours.Store(12)

	t.Setenv("STRMR_INITIAL_ADMIN_PASSWORD", "pin-test-master-password")
	accountsSvc, err := accounts.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, dst := range []*string{&env.account1, &env.account2} {
		account, err := accountsSvc.Create([]string{"household", "neighbour"}[i], "pin-test-password-1")
		if err != nil {
			t.Fatal(err)
		}
		*dst = account.ID
	}
	env.sessions, err = sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	env.users, err = users.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		dst     *string
		account string
		name    string
		pin     string
	}{
		{&env.locked, env.account1, "Parent", "1234"},
		{&env.open, env.account1, "Kid", ""},
		{&env.foreign, env.account2, "Other", "9999"},
	} {
		profile, err := env.users.CreateForAccount(p.account, p.name)
		if err != nil {
			t.Fatal(err)
		}
		if p.pin != "" {
			if _, err := env.users.SetPin(profile.ID, p.pin); err != nil {
				t.Fatal(err)
			}
		}
		*p.dst = profile.ID
	}

	env.unlocks = profileunlock.NewService(nil)
	policy := func() config.SecuritySettings {
		return config.SecuritySettings{EnforceProfilePins: env.enforce.Load(), ProfileUnlockTTLHours: int(env.ttlHours.Load())}
	}

	authHandler := handlers.NewAuthHandler(accountsSvc, env.sessions)
	authHandler.SetProfileUnlocks(env.unlocks)
	usersHandler := handlers.NewUsersHandler(env.users)
	usersHandler.SetProfileUnlocks(env.unlocks, func() time.Duration { return policy().ProfileUnlockTTL() })

	echo := func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			env.lastBody = string(body)
		}
		w.WriteHeader(http.StatusOK)
	}

	router := mux.NewRouter()
	api := router.PathPrefix("/api").Subrouter()
	protected := api.PathPrefix("").Subrouter()
	protected.Use(AccountAuthMiddleware(env.sessions, accountsSvc))
	protected.Use(profileaccess.Middleware(env.users.BelongsToAccount))
	protected.Use(ProfilePinMiddleware(env.users, env.unlocks, policy))
	protected.HandleFunc("/auth/logout", authHandler.Logout).Methods(http.MethodPost)
	protected.HandleFunc("/playback/resolve", echo).Methods(http.MethodPost)
	protected.HandleFunc("/video/stream", echo).Methods(http.MethodGet)
	protected.HandleFunc("/live/channels", echo).Methods(http.MethodGet, http.MethodPost)
	protected.HandleFunc("/clients/register", echo).Methods(http.MethodPost)
	protected.HandleFunc("/search", echo).Methods(http.MethodGet)
	profiles := protected.PathPrefix("/users").Subrouter()
	profiles.Use(ProfileOwnershipMiddleware(env.users))
	profiles.HandleFunc("/{userID}/pin/verify", usersHandler.VerifyPin).Methods(http.MethodPost)
	profiles.HandleFunc("/{userID}/pin", usersHandler.SetPin).Methods(http.MethodPut)
	profiles.HandleFunc("/{userID}/settings", echo).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/auth/refresh", authHandler.Refresh).Methods(http.MethodPost)
	env.router = router
	return env
}

func TestGlobalProfileSelectorsRequireOwnership(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		env := newPinTestEnv(t)
		env.enforce.Store(enforce)
		token := env.login(env.account1)
		for _, req := range []pinRequest{
			{method: http.MethodGet, target: "/api/live/channels?profileId=" + env.foreign, token: token},
			{method: http.MethodGet, target: "/api/video/stream?profileId=" + env.foreign, token: token},
			{method: http.MethodPost, target: "/api/live/channels", token: token, body: map[string]string{"profileId": env.foreign}},
			{method: http.MethodPost, target: "/api/playback/resolve", token: token, body: map[string]string{"userId": env.foreign}},
		} {
			if rec := env.do(req); rec.Code != http.StatusNotFound {
				t.Fatalf("enforce=%v %s %s: HTTP %d, want 404", enforce, req.method, req.target, rec.Code)
			}
		}
		if rec := env.do(pinRequest{method: http.MethodGet, target: "/api/live/channels?profileId=" + env.open, token: token}); rec.Code != http.StatusOK {
			t.Fatalf("own profile: HTTP %d", rec.Code)
		}
	}
}

func (env *pinTestEnv) login(accountID string) string {
	env.t.Helper()
	session, err := env.sessions.Create(accountID, false, "test", "127.0.0.1")
	if err != nil {
		env.t.Fatal(err)
	}
	return session.Token
}

type pinRequest struct {
	method, target, token, unlock string
	body                          any
}

func (env *pinTestEnv) do(req pinRequest) *httptest.ResponseRecorder {
	env.t.Helper()
	var reader io.Reader
	if req.body != nil {
		raw, _ := json.Marshal(req.body)
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(req.method, req.target, reader)
	if req.body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.token != "" {
		r.Header.Set("Authorization", "Bearer "+req.token)
	}
	if req.unlock != "" {
		r.Header.Set(ProfileUnlockHeader, req.unlock)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, r)
	return rec
}

func (env *pinTestEnv) verify(token, profileID, pin string) handlers.VerifyPinResponse {
	env.t.Helper()
	rec := env.do(pinRequest{method: http.MethodPost, target: "/api/users/" + profileID + "/pin/verify", token: token, body: map[string]string{"pin": pin}})
	if rec.Code != http.StatusOK {
		env.t.Fatalf("verify status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp handlers.VerifyPinResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		env.t.Fatal(err)
	}
	if !resp.Valid || resp.UnlockToken == "" || resp.ExpiresAt == "" {
		env.t.Fatalf("verify response missing unlock: %+v", resp)
	}
	return resp
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %q", rec.Body.String())
	}
	return body
}

func expectLocked(t *testing.T, rec *httptest.ResponseRecorder, profileID string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body["code"] != "profile_locked" || body["error"] != "profile is locked" || body["profileId"] != profileID {
		t.Fatalf("locked body = %v", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestProfilePinEnforcementOffAllowsEverything(t *testing.T) {
	env := newPinTestEnv(t)
	token := env.login(env.account1)
	for _, req := range []pinRequest{
		{method: http.MethodGet, target: "/api/users/" + env.locked + "/settings", token: token},
		{method: http.MethodGet, target: "/api/video/stream?path=/x.mkv&profileId=" + env.locked, token: token},
		{method: http.MethodPost, target: "/api/playback/resolve", token: token, body: map[string]string{"profileId": env.locked}},
	} {
		if rec := env.do(req); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status = %d with enforcement off", req.method, req.target, rec.Code)
		}
	}
	// Unlock tokens are still issued so updated clients can send them.
	env.verify(token, env.locked, "1234")
}

func TestProfilePinEnforcementBlocksEveryEntryPoint(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	token := env.login(env.account1)

	cases := []pinRequest{
		{method: http.MethodGet, target: "/api/users/" + env.locked + "/settings"},
		{method: http.MethodGet, target: "/api/video/stream?path=/x.mkv&profileId=" + env.locked},
		{method: http.MethodGet, target: "/api/search?q=x&userId=" + env.locked},
		{method: http.MethodPost, target: "/api/playback/resolve", body: map[string]string{"profileId": env.locked}},
		{method: http.MethodPost, target: "/api/playback/resolve", body: map[string]string{"userId": env.locked}},
		{method: http.MethodPut, target: "/api/users/" + env.locked + "/pin", body: map[string]string{"pin": "0000"}},
	}
	for _, req := range cases {
		req.token = token
		expectLocked(t, env.do(req), env.locked)
	}

	// Profiles without a PIN, requests without a profile, exempt routes and
	// preflights are unaffected.
	for _, req := range []pinRequest{
		{method: http.MethodGet, target: "/api/users/" + env.open + "/settings", token: token},
		{method: http.MethodGet, target: "/api/search?q=x", token: token},
		{method: http.MethodPost, target: "/api/clients/register", token: token, body: map[string]string{"id": "dev", "userId": env.locked}},
		{method: http.MethodOptions, target: "/api/users/" + env.locked + "/settings"},
	} {
		if rec := env.do(req); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: status = %d body=%s", req.method, req.target, rec.Code, rec.Body.String())
		}
	}
}

func TestProfilePinUnlockHeaderAndQueryParam(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	token := env.login(env.account1)
	unlock := env.verify(token, env.locked, "1234").UnlockToken

	if rec := env.do(pinRequest{method: http.MethodGet, target: "/api/users/" + env.locked + "/settings", token: token, unlock: unlock}); rec.Code != http.StatusOK {
		t.Fatalf("header unlock: status = %d", rec.Code)
	}
	// Comma-separated list in the header.
	if rec := env.do(pinRequest{method: http.MethodGet, target: "/api/users/" + env.locked + "/settings", token: token, unlock: "pu_bogus, " + unlock}); rec.Code != http.StatusOK {
		t.Fatalf("header list unlock: status = %d", rec.Code)
	}
	// Query form for media URLs.
	if rec := env.do(pinRequest{method: http.MethodGet, target: "/api/video/stream?path=/x.mkv&profileId=" + env.locked + "&profileUnlock=" + unlock, token: token}); rec.Code != http.StatusOK {
		t.Fatalf("query unlock: status = %d", rec.Code)
	}
	// The JSON body is restored for the handler after the middleware peeks at it.
	rec := env.do(pinRequest{method: http.MethodPost, target: "/api/playback/resolve", token: token, unlock: unlock, body: map[string]string{"profileId": env.locked, "titleId": "t1"}})
	if rec.Code != http.StatusOK || !strings.Contains(env.lastBody, `"titleId":"t1"`) {
		t.Fatalf("body unlock: status = %d body seen=%q", rec.Code, env.lastBody)
	}

	// The unlock belongs to this session only.
	other := env.login(env.account1)
	expectLocked(t, env.do(pinRequest{method: http.MethodGet, target: "/api/users/" + env.locked + "/settings", token: other, unlock: unlock}), env.locked)
}

func TestProfilePinOtherAccountsProfilesAreNotRevealed(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	token := env.login(env.account1)
	// Ownership middleware answers 404, not profile_locked.
	rec := env.do(pinRequest{method: http.MethodGet, target: "/api/users/" + env.foreign + "/settings", token: token})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign profile: status = %d, want 404", rec.Code)
	}
	if body := decodeError(t, rec); body["code"] != "profile_not_found" {
		t.Fatalf("foreign profile body = %v", body)
	}
}

func TestProfilePinUnlockSurvivesRefreshAndDiesOnLogout(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	token := env.login(env.account1)
	unlock := env.verify(token, env.locked, "1234").UnlockToken

	rec := env.do(pinRequest{method: http.MethodPost, target: "/api/auth/refresh", token: token})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", rec.Code, rec.Body.String())
	}
	var refreshed handlers.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	settings := "/api/users/" + env.locked + "/settings"
	if rec := env.do(pinRequest{method: http.MethodGet, target: settings, token: refreshed.Token, unlock: unlock}); rec.Code != http.StatusOK {
		t.Fatalf("unlock must carry over refresh: status = %d", rec.Code)
	}

	// The rotated-out token is a dead session, distinguishable from a lock.
	rec = env.do(pinRequest{method: http.MethodGet, target: settings, token: token, unlock: unlock})
	if rec.Code != http.StatusUnauthorized || decodeError(t, rec)["code"] != "session_expired" {
		t.Fatalf("old token: status = %d body=%s", rec.Code, rec.Body.String())
	}

	if rec := env.do(pinRequest{method: http.MethodPost, target: "/api/auth/logout", token: refreshed.Token}); rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if env.unlocks.Count() != 0 {
		t.Fatalf("logout must revoke unlocks, %d left", env.unlocks.Count())
	}
}

func TestProfilePinChangeRevokesUnlocks(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	token := env.login(env.account1)
	unlock := env.verify(token, env.locked, "1234").UnlockToken
	settings := "/api/users/" + env.locked + "/settings"

	// Change the PIN through a path the middleware cannot see (admin UI uses
	// the service directly): the PIN fingerprint alone must invalidate it.
	if _, err := env.users.SetPin(env.locked, "5678"); err != nil {
		t.Fatal(err)
	}
	expectLocked(t, env.do(pinRequest{method: http.MethodGet, target: settings, token: token, unlock: unlock}), env.locked)

	// Through the API (with a fresh unlock) the grants are deleted outright.
	unlock = env.verify(token, env.locked, "5678").UnlockToken
	if rec := env.do(pinRequest{method: http.MethodPut, target: "/api/users/" + env.locked + "/pin", token: token, unlock: unlock, body: map[string]string{"pin": "2468"}}); rec.Code != http.StatusOK {
		t.Fatalf("set pin status = %d body=%s", rec.Code, rec.Body.String())
	}
	if env.unlocks.Count() != 0 {
		t.Fatalf("PIN change must revoke unlocks, %d left", env.unlocks.Count())
	}
}

func TestProfilePinVerifyErrorCodes(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	token := env.login(env.account1)
	for pin, code := range map[string]string{"0000": "pin_invalid", "": "pin_required"} {
		rec := env.do(pinRequest{method: http.MethodPost, target: "/api/users/" + env.locked + "/pin/verify", token: token, body: map[string]string{"pin": pin}})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("pin %q: status = %d", pin, rec.Code)
		}
		// The message text is unchanged for clients that match on it.
		if body := decodeError(t, rec); body["code"] != code || body["error"] != "invalid PIN" {
			t.Fatalf("pin %q: body = %v", pin, body)
		}
	}
}

func TestProfilePinUnlockExpiresWithConfiguredTTL(t *testing.T) {
	env := newPinTestEnv(t)
	env.enforce.Store(true)
	env.ttlHours.Store(2)
	token := env.login(env.account1)
	resp := env.verify(token, env.locked, "1234")
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	// Capped by the 1h session lifetime used by this environment.
	if until := time.Until(expiresAt); until > time.Hour+time.Minute || until < 50*time.Minute {
		t.Fatalf("expiresAt %v not capped by session expiry", expiresAt)
	}
}

func TestAuthMiddlewareErrorCodes(t *testing.T) {
	env := newPinTestEnv(t)
	for name, tc := range map[string]struct {
		token, code string
	}{
		"missing token": {"", "unauthorized"},
		"unknown token": {"not-a-session", "session_expired"},
	} {
		rec := env.do(pinRequest{method: http.MethodGet, target: "/api/search?q=x", token: tc.token})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d", name, rec.Code)
		}
		if body := decodeError(t, rec); body["code"] != tc.code {
			t.Fatalf("%s: body = %v", name, body)
		}
	}
}

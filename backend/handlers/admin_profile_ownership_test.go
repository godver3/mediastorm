package handlers_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/handlers"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"
)

func TestAccountCookieRequiresProfileOwnership(t *testing.T) {
	t.Setenv("STRMR_INITIAL_ADMIN_PASSWORD", "fixture-master-password")
	dir := t.TempDir()
	accountService, err := accounts.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	account, err := accountService.Create("family", "fixture-account-password")
	if err != nil {
		t.Fatal(err)
	}
	userService, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	own, err := userService.CreateForAccount(account.ID, "Own")
	if err != nil {
		t.Fatal(err)
	}
	other, err := userService.CreateForAccount("other-account", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	sessionService, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessionService.Create(account.ID, false, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.NewAdminUIHandler("", "", nil, userService, nil, config.NewManager(filepath.Join(dir, "settings.json")))
	h.SetAccountsService(accountService)
	h.SetSessionsService(sessionService)
	for _, tc := range []struct {
		method, target, body string
		want                 int
	}{
		{"GET", "/account/api/live/channels?profileId=" + other.ID, "", 404},
		{"POST", "/account/api/playback/resolve", fmt.Sprintf(`{"profileId":%q}`, other.ID), 404},
		{"GET", "/account/api/video/hls/start?profileId=" + own.ID, "", 200},
	} {
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		r.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: session.Token})
		called := false
		w := httptest.NewRecorder()
		h.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
			called = true
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
		})(w, r)
		if w.Code != tc.want || called != (tc.want == 200) {
			t.Fatalf("%s: HTTP %d, handler called %v", tc.target, w.Code, called)
		}
	}
	scoped, err := sessionService.CreateScoped(account.ID, false, "test", "127.0.0.1", time.Hour, "stream")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/account/api/live/channels?profileId="+own.ID, nil)
	r.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: scoped.Token})
	w := httptest.NewRecorder()
	h.RequireAuth(func(w http.ResponseWriter, r *http.Request) { t.Fatal("stream invitation reached account handler") })(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("stream invitation cookie: HTTP %d", w.Code)
	}
}

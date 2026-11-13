package parentauth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/internal/auth"
	"novastream/models"
)

type testAccounts struct {
	calls    int
	foreign  bool
	password string
}

func (a *testAccounts) Get(id string) (models.Account, bool) {
	return models.Account{ID: "account-a", Username: "parent"}, id == "account-a"
}
func (a *testAccounts) Authenticate(username, password string) (models.Account, error) {
	a.calls++
	expected := a.password
	if expected == "" {
		expected = "fixture parent password"
	}
	if username != "parent" || password != expected {
		return models.Account{}, errors.New("invalid")
	}
	id := "account-a"
	if a.foreign {
		id = "account-b"
	}
	return models.Account{ID: id}, nil
}

func TestParentPasswordEncoding(t *testing.T) {
	for _, password := range []string{"fixture parent password", "  fixture パスワード  "} {
		guard := New(&testAccounts{password: password})
		r := httptest.NewRequest("PUT", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account-a"))
		r.Header.Set(PasswordHeader, "base64:"+base64.StdEncoding.EncodeToString([]byte(password)))
		if !guard.Require(httptest.NewRecorder(), r) {
			t.Fatal("encoded parent password rejected")
		}
		r.Header.Set(PasswordHeader, "base64:not valid")
		if guard.Require(httptest.NewRecorder(), r) {
			t.Fatal("invalid encoding accepted")
		}
	}
}

func TestParentAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, password, scope        string
		master, foreign, unavailable bool
		want                         int
	}{
		{name: "ordinary session is insufficient", want: 403},
		{name: "wrong password", password: "incorrect", want: 403},
		{name: "parent password", password: "fixture parent password", want: 200},
		{name: "other account proof", password: "fixture parent password", foreign: true, want: 403},
		{name: "administrator", master: true, want: 200},
		{name: "unavailable verifier", password: "fixture parent password", unavailable: true, want: 403},
		{name: "scoped invitation", password: "fixture parent password", scope: models.SessionScopeStream, want: 403},
		{name: "scoped master invitation", master: true, scope: models.SessionScopeStream, want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := &testAccounts{foreign: tc.foreign}
			guard := New(accounts)
			if tc.unavailable {
				guard = nil
			}
			r := httptest.NewRequest("PUT", "/api/users/child/kids-profile", nil)
			r.Header.Set(PasswordHeader, tc.password)
			ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account-a")
			ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, tc.master)
			ctx = context.WithValue(ctx, auth.ContextKeySession, models.Session{Scope: tc.scope})
			w := httptest.NewRecorder()
			allowed := guard.Require(w, r.WithContext(ctx))
			if allowed != (tc.want == 200) || w.Code != tc.want {
				t.Fatalf("allowed=%v HTTP %d, want %d", allowed, w.Code, tc.want)
			}
			if tc.password != "" && strings.Contains(w.Body.String(), tc.password) {
				t.Fatal("password leaked")
			}
		})
	}
}

func TestParentAuthorizationRateLimitAndNoRememberedProof(t *testing.T) {
	accounts := &testAccounts{}
	guard := New(accounts)
	r := httptest.NewRequest("PUT", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account-a"))
	r.Header.Set(PasswordHeader, "fixture parent password")
	if !guard.Require(httptest.NewRecorder(), r) {
		t.Fatal("parent proof rejected")
	}
	r.Header.Del(PasswordHeader)
	if guard.Require(httptest.NewRecorder(), r) {
		t.Fatal("prior proof reused without password")
	}
	r.Header.Set(PasswordHeader, "wrong")
	for range 9 {
		guard.Require(httptest.NewRecorder(), r)
	}
	w := httptest.NewRecorder()
	guard.Require(w, r)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || accounts.calls != 10 {
		t.Fatalf("rate limit HTTP %d calls=%d", w.Code, accounts.calls)
	}
}

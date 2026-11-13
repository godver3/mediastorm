package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/handlers"
	"novastream/internal/auth"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"
)

func TestParentalControlsRequireFreshAccountPassword(t *testing.T) {
	for _, action := range []string{"flag", "mode", "rating", "movie rating", "tv rating", "replace lists", "add list", "remove list", "set PIN", "clear PIN", "delete"} {
		for _, password := range []string{"", "incorrect", "fixture-parent-password"} {
			name := action + "/missing"
			if password != "" {
				name = action + "/provided"
			}
			if password == "incorrect" {
				name = action + "/wrong"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("STRMR_INITIAL_ADMIN_PASSWORD", "fixture-master-password")
				dir := t.TempDir()
				as, err := accounts.NewService(dir)
				if err != nil {
					t.Fatal(err)
				}
				a, err := as.Create("family", "fixture-parent-password")
				if err != nil {
					t.Fatal(err)
				}
				us, err := users.NewService(dir)
				if err != nil {
					t.Fatal(err)
				}
				p, err := us.CreateForAccount(a.ID, "Child")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = us.SetKidsProfile(p.ID, true); err != nil {
					t.Fatal(err)
				}
				if _, err = us.SetPin(p.ID, "1234"); err != nil {
					t.Fatal(err)
				}
				before, _ := us.Get(p.ID)
				h := handlers.NewUsersHandler(us, as)
				method := "PUT"
				body := `{}`
				handler := h.SetKidsProfile
				want := 200
				target := "/api/users/" + p.ID
				switch action {
				case "flag":
					body = `{"isKidsProfile":false}`
				case "mode":
					handler = h.SetKidsMode
					body = `{"mode":""}`
				case "rating":
					handler = h.SetKidsMaxRating
					body = `{"rating":"TV-MA"}`
				case "movie rating":
					handler = h.SetKidsMaxMovieRating
					body = `{"rating":"R"}`
				case "tv rating":
					handler = h.SetKidsMaxTVRating
					body = `{"rating":"TV-MA"}`
				case "replace lists":
					handler = h.SetKidsAllowedLists
					body = `{"lists":[]}`
				case "add list":
					handler = h.AddKidsAllowedList
					method = "POST"
					body = `{"url":"https://mdblist.com/lists/example/kids"}`
				case "remove list":
					handler = h.RemoveKidsAllowedList
					method = "DELETE"
					target += "?url=https://mdblist.com/lists/example/kids"
				case "set PIN":
					handler = h.SetPin
					body = `{"pin":"5678"}`
				case "clear PIN":
					handler = h.ClearPin
					method = "DELETE"
				case "delete":
					handler = h.Delete
					method = "DELETE"
					want = 204
				}
				r := httptest.NewRequest(method, target, strings.NewReader(body))
				r = mux.SetURLVars(r, map[string]string{"userID": p.ID})
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, a.ID))
				r.Header.Set("X-Account-Password", password)
				w := httptest.NewRecorder()
				handler(w, r)
				if password != "fixture-parent-password" {
					want = 403
				}
				if w.Code != want {
					t.Fatalf("HTTP %d, want %d: %s", w.Code, want, w.Body.String())
				}
				after, exists := us.Get(p.ID)
				if want == 403 && (!exists || !reflect.DeepEqual(before, after)) {
					t.Fatal("denied action changed profile state")
				}
				if want == 403 && !strings.Contains(w.Body.String(), "parent_authorization_required") {
					t.Fatal("missing parental challenge code")
				}
				if action == "flag" && want == 200 && after.IsKidsProfile {
					t.Fatal("confirmed parent could not disable child flag")
				}
			})
		}
	}
}

func TestCookieProfileManagementRequiresParentAuthorization(t *testing.T) {
	t.Setenv("STRMR_INITIAL_ADMIN_PASSWORD", "fixture-master-password")
	dir := t.TempDir()
	as, err := accounts.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := as.Create("family", "fixture-parent-password")
	if err != nil {
		t.Fatal(err)
	}
	us, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := us.CreateForAccount(a.ID, "Child")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = us.SetKidsProfile(p.ID, true)
	ss, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := ss.Create(a.ID, false, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.NewAdminUIHandler("", "", nil, us, nil, config.NewManager(filepath.Join(dir, "settings.json")))
	h.SetAccountsService(as)
	h.SetSessionsService(ss)
	scoped, err := ss.CreateScoped(a.ID, true, "test", "127.0.0.1", time.Hour, "stream")
	if err != nil {
		t.Fatal(err)
	}
	scopedRequest := httptest.NewRequest("PUT", "/account/api/profiles/kids?profileId="+p.ID, strings.NewReader(`{"isKidsProfile":false}`))
	scopedRequest.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: scoped.Token})
	scopedRequest.Header.Set("X-Account-Password", "fixture-parent-password")
	scopedResponse := httptest.NewRecorder()
	h.RequireAuth(h.SetKidsProfile)(scopedResponse, scopedRequest)
	if scopedResponse.Code != 403 {
		t.Fatalf("scoped invitation: HTTP %d", scopedResponse.Code)
	}
	for _, action := range []string{"disable child", "create adult", "clear parent PIN", "delete profile", "change parent password"} {
		method := "PUT"
		target := "/account/api/profiles/kids?profileId=" + p.ID
		body := `{"isKidsProfile":false}`
		handler := h.SetKidsProfile
		switch action {
		case "create adult":
			method = "POST"
			target = "/account/api/profiles"
			body = `{"name":"Escape profile"}`
			handler = h.CreateProfile
		case "clear parent PIN":
			method = "DELETE"
			handler = h.ClearProfilePin
		case "delete profile":
			method = "DELETE"
			handler = h.DeleteProfile
		case "change parent password":
			target = "/account/api/accounts/password?accountId=" + a.ID
			body = `{"newPassword":"child-selected-password"}`
			handler = h.ResetUserAccountPassword
		}
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: session.Token})
		w := httptest.NewRecorder()
		h.RequireAuth(handler)(w, r)
		if w.Code != 403 {
			t.Fatalf("%s: HTTP %d", action, w.Code)
		}
	}
	r := httptest.NewRequest("PUT", "/account/api/profiles/kids?profileId="+p.ID, strings.NewReader(`{"isKidsProfile":false}`))
	r.AddCookie(&http.Cookie{Name: "strmr_admin_session", Value: session.Token})
	r.Header.Set("X-Account-Password", "fixture-parent-password")
	w := httptest.NewRecorder()
	h.RequireAuth(h.SetKidsProfile)(w, r)
	if w.Code != 200 {
		t.Fatalf("confirmed parent: HTTP %d: %s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload["isKidsProfile"] != false {
		t.Fatal("confirmed change not returned")
	}
}

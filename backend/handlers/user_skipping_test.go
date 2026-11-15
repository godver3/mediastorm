package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	user_settings "novastream/services/user_settings"
)

type skippingUsers struct{}

func (skippingUsers) Exists(id string) bool { return id == "adult" || id == "child" }
func (skippingUsers) BelongsToAccount(id, account string) bool {
	return account == "household" && (id == "adult" || id == "child")
}

func TestProfileSkippingHTTPIsolationAndValidation(t *testing.T) {
	svc, err := user_settings.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewUserSettingsHandler(svc, skippingUsers{}, nil)
	call := func(profile, account, body string) int {
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r = mux.SetURLVars(r, map[string]string{"userID": profile})
		r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, account))
		w := httptest.NewRecorder()
		h.PatchSkipping(w, r)
		return w.Code
	}
	if code := call("adult", "household", `{"intro":true,"credits":true,"nextEpisode":false}`); code != 200 {
		t.Fatal(code)
	}
	// Other households are rejected by ProfileOwnershipMiddleware on the route,
	// which also lets the master account through; the handler does not repeat it.
	if code := call("child", "household", `{"intro":false}`); code != 200 {
		t.Fatal(code)
	}
	for _, body := range []string{`{}`, `null`, `{"intro":"true"}`, `{"unknown":true}`, `{"intro":true} {}`, `{"intro":null}`, `{"nextEpisode":null}`, `{"nextEpisode":"false"}`, `{"nextEpisode":1}`} {
		if code := call("adult", "household", body); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	adult, _ := svc.Get("adult")
	child, _ := svc.Get("child")
	if !*adult.Playback.IntroAutoSkip || *child.Playback.IntroAutoSkip {
		t.Fatal("profile choice leaked between profiles")
	}
}

func TestNextEpisodeAutoplayRejectsUnauthorizedWrites(t *testing.T) {
	for _, mode := range []string{"foreign", "locked", "stream"} {
		t.Run(mode, func(t *testing.T) {
			svc, err := user_settings.NewService(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			h := NewUserSettingsHandler(svc, skippingUsers{}, nil)
			r := httptest.NewRequest(http.MethodPatch, "/users/adult/settings/skipping", strings.NewReader(`{"nextEpisode":false}`))
			r = mux.SetURLVars(r, map[string]string{"userID": "adult"})
			account := "household"
			want := 403
			if mode == "foreign" {
				account = "other"
				want = 404
			}
			r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, account))
			if mode == "stream" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
			}
			if mode == "locked" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(skippingUsers{}.BelongsToAccount)(http.HandlerFunc(h.PatchSkipping)).ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("HTTP %d, want %d", w.Code, want)
			}
			got, _ := svc.Get("adult")
			if got != nil && got.Playback.NextEpisodeAutoPlay != nil {
				t.Fatal("rejected request changed profile")
			}
		})
	}
}

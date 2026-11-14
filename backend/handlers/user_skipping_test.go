package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
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
	if code := call("adult", "household", `{"intro":true,"credits":true}`); code != 200 {
		t.Fatal(code)
	}
	// Other households are rejected by ProfileOwnershipMiddleware on the route,
	// which also lets the master account through; the handler does not repeat it.
	if code := call("child", "household", `{"intro":false}`); code != 200 {
		t.Fatal(code)
	}
	for _, body := range []string{`{}`, `null`, `{"intro":"true"}`, `{"unknown":true}`, `{"intro":true} {}`, `{"intro":null}`} {
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

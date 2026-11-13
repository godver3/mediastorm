package handlers_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/handlers"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/profileunlock"
	"novastream/services/users"
)

// An ordinary profile may manage its own PIN without the account password when
// that cannot weaken a restriction: adding a first PIN, or changing/clearing a
// PIN already proven by an unlock token. Kids profiles always need the password.
func TestProfilePinChangesWithoutAccountPassword(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kids, hasPin  bool
		clear, unlock bool
		want          int
	}{
		{name: "adult adds first PIN", want: 200},
		{name: "adult clears unproven PIN", hasPin: true, clear: true, want: 403},
		{name: "adult changes unproven PIN", hasPin: true, want: 403},
		{name: "adult changes proven PIN", hasPin: true, unlock: true, want: 200},
		{name: "adult clears proven PIN", hasPin: true, clear: true, unlock: true, want: 200},
		{name: "kids adds first PIN", kids: true, want: 403},
		{name: "kids clears proven PIN", kids: true, hasPin: true, clear: true, unlock: true, want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			p, err := us.CreateForAccount(a.ID, "Profile")
			if err != nil {
				t.Fatal(err)
			}
			if tc.kids {
				if _, err = us.SetKidsProfile(p.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.hasPin {
				if _, err = us.SetPin(p.ID, "1234"); err != nil {
					t.Fatal(err)
				}
			}
			session := models.Session{Token: "session-token", AccountID: a.ID, ExpiresAt: time.Now().Add(time.Hour)}
			unlocks := profileunlock.NewService(nil)
			h := handlers.NewUsersHandler(us, as)
			h.SetProfileUnlocks(unlocks, func() time.Duration { return time.Hour })

			method, body, handler := "PUT", `{"pin":"5678"}`, h.SetPin
			if tc.clear {
				method, body, handler = "DELETE", "", h.ClearPin
			}
			r := httptest.NewRequest(method, "/api/users/"+p.ID+"/pin", strings.NewReader(body))
			r = mux.SetURLVars(r, map[string]string{"userID": p.ID})
			ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, a.ID)
			ctx = context.WithValue(ctx, auth.ContextKeySession, session)
			r = r.WithContext(ctx)
			if tc.unlock {
				token, _, err := unlocks.Issue(profileunlock.IssueRequest{SessionToken: session.Token, SessionExpiresAt: session.ExpiresAt,
					AccountID: a.ID, ProfileID: p.ID, PinFingerprint: us.PinFingerprint(p.ID), TTL: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set(profileunlock.Header, token)
			}
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.want {
				t.Fatalf("HTTP %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 403 && !strings.Contains(w.Body.String(), "parent_authorization_required") {
				t.Fatal("missing parental challenge code")
			}
		})
	}
}

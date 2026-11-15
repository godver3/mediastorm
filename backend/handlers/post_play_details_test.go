package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
)

func TestPostPlayDetailsDeliveryRegistersResolvedServerTitle(t *testing.T) {
	for _, shell := range []bool{true, false} {
		t.Run(map[bool]string{true: "shell", false: "bundle"}[shell], func(t *testing.T) {
			metadata, ratings, _, title := ratingFixture(t)
			svc := &fakeMetadataService{movieResp: &title}
			details := NewDetailsBundleHandler(svc, &contractHistory{}, contractContentPrefs{}, contractUsers{})
			details.SetCatalogueObserver(metadata.ObserveDetailsCatalogue)
			r := httptest.NewRequest("GET", "/users/profile/details-bundle?type=movie&titleId=forged&name=Client+Claim&tmdbId=999", nil)
			r = mux.SetURLVars(r, map[string]string{"userID": "profile"})
			r.Header.Set("X-Client-ID", "fixture-client")
			r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account"))
			handler := details.GetDetailsBundle
			if shell {
				handler = details.GetDetailsShell
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(p, a string) bool { return p == "profile" && a == "account" })(http.HandlerFunc(handler)).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("details delivery: %d", w.Code)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("profile metadata may be cached")
			}
			if _, ok := ratings.ResolveCompleted("profile", "fixture-client", "movie", title.ID); !ok {
				t.Fatal("server title not registered for later completion")
			}
			for _, candidate := range []struct{ profile, client, id string }{{"profile", "fixture-client", "forged"}, {"other", "fixture-client", title.ID}, {"profile", "other", title.ID}} {
				if _, ok := ratings.ResolveCompleted(candidate.profile, candidate.client, "movie", candidate.id); ok {
					t.Fatal("client query or another profile/device became trusted")
				}
			}
		})
	}
}

func TestPostPlayDetailsObserverRejectsRestrictedOrUnownedDelivery(t *testing.T) {
	for _, mode := range []string{"foreign", "stream", "missing client", "child", "locked"} {
		t.Run(mode, func(t *testing.T) {
			metadata, ratings, _, title := ratingFixture(t)
			r := postPlayRequest("movie", title.ID, "")
			if mode == "foreign" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "other"))
			}
			if mode == "stream" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream}))
			}
			if mode == "missing client" {
				r.Header.Del("X-Client-ID")
			}
			if mode == "child" {
				metadata.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating"}}
			}
			details := NewDetailsBundleHandler(&fakeMetadataService{movieResp: &title}, &contractHistory{}, contractContentPrefs{}, contractUsers{})
			details.SetCatalogueObserver(metadata.ObserveDetailsCatalogue)
			query := r.URL.Query()
			query.Set("type", "movie")
			query.Set("titleId", title.ID)
			r.URL.RawQuery = query.Encode()
			if mode == "locked" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, r *http.Request, p string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(p, a string) bool { return p == "profile" && a == "account" })(http.HandlerFunc(details.GetDetailsShell)).ServeHTTP(w, r)
			if _, ok := ratings.ResolveCompleted("profile", "fixture-client", "movie", title.ID); ok {
				t.Fatal("restricted delivery became a rating projection")
			}
			if mode == "locked" && w.Code != 403 {
				t.Fatalf("locked profile: %d", w.Code)
			}
		})
	}
}

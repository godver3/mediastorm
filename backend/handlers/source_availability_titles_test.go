package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/sourceavailability"
	"path/filepath"
	"testing"
)

func titleEndpointFixture(t *testing.T) (*MetadataHandler, *sourceavailability.Service) {
	t.Helper()
	cfg := config.DefaultSettings()
	cfg.SourceAvailability.Enabled = true
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	store, err := sourceavailability.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := sourceavailability.NewService(store, manager, nil, sourceavailability.NewScopeFactory(manager, nil, nil, nil, func(id string) bool { return id == "profile" }))
	h, _, _ := episodeEndpointFixture()
	h.CfgManager = manager
	h.SourceAvailability = svc
	return h, svc
}
func TestVisibleTitleEndpointReusesDeliveredMetadataAndKeepsForeignTitlesUnknown(t *testing.T) {
	h, svc := titleEndpointFixture(t)
	svc.AnnotateTitles("profile", "fixture-client", []models.Title{{ID: "a", Name: "Synthetic Movie", MediaType: "movie", Year: 2024}})
	r := episodeDemandHTTP(map[string]any{"version": 1, "titles": []map[string]string{{"titleId": "a", "mediaType": "movie"}, {"titleId": "foreign", "mediaType": "movie"}}})
	w := httptest.NewRecorder()
	h.TitleSourceHints(w, r)
	var v sourceavailability.TitleViewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(v.Items) != 2 || v.Items[0].Availability.Reason == "catalogue_refresh_required" || v.Items[1].Availability.Reason != "catalogue_refresh_required" {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private hints may be cached")
	}
}
func TestVisibleTitleEndpointRejectsMalformedAndCallerSuppliedQueries(t *testing.T) {
	for _, body := range []map[string]any{
		{"version": 1}, {"version": 0, "titles": []any{}}, {"version": 1, "titles": nil}, {"version": 1, "titles": []any{}, "query": "forged"},
		{"version": 1, "titles": []map[string]string{{"titleId": "a", "mediaType": "movie", "name": "forged"}}},
		{"version": 1, "titles": []map[string]string{{"titleId": "a", "mediaType": "person"}}},
		{"version": 1, "titles": []map[string]string{{"titleId": "a", "mediaType": "movie"}, {"titleId": "a", "mediaType": "movie"}}},
	} {
		h, _ := titleEndpointFixture(t)
		w := httptest.NewRecorder()
		h.TitleSourceHints(w, episodeDemandHTTP(body))
		if w.Code != 400 {
			t.Fatalf("invalid body accepted: %d", w.Code)
		}
	}
}
func TestVisibleTitleEndpointClearsWithoutMetadataAndRejectsLateCleanup(t *testing.T) {
	h, _ := titleEndpointFixture(t)
	for _, version := range []int{2, 1} {
		w := httptest.NewRecorder()
		h.TitleSourceHints(w, episodeDemandHTTP(map[string]any{"version": version, "titles": []any{}}))
		want := 200
		if version == 1 {
			want = 409
		}
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
	}
}
func TestVisibleTitleEndpointRechecksKidsRatingBeforeDemand(t *testing.T) {
	for _, rating := range []string{"G", "R", ""} {
		h, svc := titleEndpointFixture(t)
		h.UsersService = fallbackProfiles{models.User{ID: "profile", AccountID: "account", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG"}}
		svc.AnnotateTitles("profile", "fixture-client", []models.Title{{ID: "a", Name: "Synthetic", MediaType: "movie", Certification: rating}})
		w := httptest.NewRecorder()
		h.TitleSourceHints(w, episodeDemandHTTP(map[string]any{"version": 1, "titles": []map[string]string{{"titleId": "a", "mediaType": "movie"}}}))
		want := 404
		if rating == "G" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("rating %q: status %d", rating, w.Code)
		}
	}
}
func TestVisibleTitleEndpointRejectsMissingClient(t *testing.T) {
	h, _ := titleEndpointFixture(t)
	r := episodeDemandHTTP(map[string]any{"version": 1, "titles": []any{}})
	r.Header.Del("X-Client-ID")
	w := httptest.NewRecorder()
	h.TitleSourceHints(w, r)
	if w.Code != 400 {
		t.Fatal("client identity missing")
	}
}

func TestVisibleTitleEndpointOwnershipPinAndStreamScopeGuard(t *testing.T) {
	for _, mode := range []string{"foreign account", "locked profile", "stream scope"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := titleEndpointFixture(t)
			r := episodeDemandHTTP(map[string]any{"version": 1, "titles": []map[string]string{{"titleId": "a", "mediaType": "movie"}}})
			if mode == "locked profile" {
				r = profileaccess.WithResourceGuard(r, func(w http.ResponseWriter, _ *http.Request, _ string) bool {
					http.Error(w, "locked", 403)
					return false
				})
			}
			if mode == "stream scope" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, models.Session{Scope: models.SessionScopeStream, ScopeResource: "/fixture/movie"}))
			}
			w := httptest.NewRecorder()
			profileaccess.Middleware(func(string, string) bool { return mode != "foreign account" })(http.HandlerFunc(h.TitleSourceHints)).ServeHTTP(w, r)
			want := 403
			if mode == "foreign account" {
				want = 404
			}
			if w.Code != want {
				t.Fatalf("guard bypass: %d want %d", w.Code, want)
			}
		})
	}
}

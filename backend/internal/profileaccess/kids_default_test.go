package profileaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/internal/auth"
	"novastream/models"
)

func TestMostRestrictiveKidsProfile(t *testing.T) {
	pg := models.User{ID: "pg", IsKidsProfile: true, KidsMode: "rating", KidsMaxMovieRating: "PG", KidsMaxTVRating: "TV-PG"}
	g := models.User{ID: "g", IsKidsProfile: true, KidsMode: "rating", KidsMaxRating: "G"}
	curated := models.User{ID: "c", IsKidsProfile: true, KidsMode: "content_list"}
	adult := models.User{ID: "a"}
	cases := []struct {
		in   []models.User
		want string
	}{
		{nil, ""},
		{[]models.User{pg, adult}, ""},
		{[]models.User{pg}, "pg"},
		{[]models.User{pg, g}, "g"},
		{[]models.User{pg, curated, g}, "c"},
	}
	for i, tc := range cases {
		if got := MostRestrictiveKidsProfile(tc.in); got != tc.want {
			t.Errorf("case %d: got %q want %q", i, got, tc.want)
		}
	}
}

func TestDefaultKidsProfileInjectsUserID(t *testing.T) {
	profiles := map[string][]models.User{
		"kidsOnly": {{ID: "kid", IsKidsProfile: true, KidsMode: "rating", KidsMaxRating: "G"}},
		"family":   {{ID: "kid", IsKidsProfile: true}, {ID: "parent"}},
	}
	mw := DefaultKidsProfile(func(accountID string) []models.User { return profiles[accountID] })
	var seen string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("userId")
	}))
	run := func(path, account string, master bool) string {
		seen = ""
		r := httptest.NewRequest(http.MethodGet, path, nil)
		ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, account)
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, master)
		handler.ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))
		return seen
	}
	if got := run("/api/discover/new?type=movie", "kidsOnly", false); got != "kid" {
		t.Fatalf("kids-only account: got %q", got)
	}
	if got := run("/api/metadata/movies/details?tmdbId=1", "kidsOnly", false); got != "kid" {
		t.Fatalf("details: got %q", got)
	}
	if got := run("/api/discover/new?userId=other", "kidsOnly", false); got != "other" {
		t.Fatalf("explicit userId must be kept: got %q", got)
	}
	if got := run("/api/discover/new", "family", false); got != "" {
		t.Fatalf("family account must not be rewritten: got %q", got)
	}
	if got := run("/api/discover/new", "kidsOnly", true); got != "" {
		t.Fatalf("master must not be rewritten: got %q", got)
	}
	if got := run("/api/users", "kidsOnly", false); got != "" {
		t.Fatalf("non-content path rewritten: got %q", got)
	}
}

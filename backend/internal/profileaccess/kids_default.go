package profileaccess

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/kids"
)

// KidsContentPath reports whether path serves title listings or title
// metadata that kids-profile limits apply to.
func KidsContentPath(path string) bool {
	i := strings.Index(path, "/api/")
	if i < 0 {
		return false
	}
	path = path[i+len("/api"):]
	for _, prefix := range []string{"/discover/", "/lists/", "/metadata/", "/library/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	for _, exact := range []string{"/search", "/recommendations"} {
		if path == exact || strings.HasPrefix(path, exact+"/") {
			return true
		}
	}
	return false
}

// DefaultKidsProfile closes the "omit userId" hole in kids-profile limits.
// Sessions are account-scoped (they do not record an active profile), so the
// only authenticated fact available is the account. When a content request
// names no profile (no userId/profileId query or profile path variable) and
// every profile the account owns is a kids profile, the request is treated as
// coming from that account's most restrictive kids profile: its ID is set as
// the userId query parameter so every downstream kids filter applies.
// Master accounts, share links and accounts owning any non-kids profile are
// untouched; for those, profile PINs remain what stops a child from selecting
// an adult profile.
func DefaultKidsProfile(list func(accountID string) []models.User) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		if list == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || auth.IsMaster(r) || auth.IsShareLinkRequest(r) || !KidsContentPath(r.URL.Path) || namesProfile(r) {
				next.ServeHTTP(w, r)
				return
			}
			accountID := auth.GetAccountID(r)
			if accountID == "" {
				next.ServeHTTP(w, r)
				return
			}
			if id := MostRestrictiveKidsProfile(list(accountID)); id != "" {
				q := r.URL.Query()
				q.Del("userId")
				q.Del("profileId")
				q.Set("userId", id)
				r.URL.RawQuery = q.Encode()
			}
			next.ServeHTTP(w, r)
		})
	}
}

func namesProfile(r *http.Request) bool {
	for _, key := range []string{"userID", "profileID"} {
		if strings.TrimSpace(mux.Vars(r)[key]) != "" {
			return true
		}
	}
	q := r.URL.Query()
	return strings.TrimSpace(q.Get("userId")) != "" || strings.TrimSpace(q.Get("profileId")) != ""
}

// MostRestrictiveKidsProfile returns the most restrictive profile's ID when
// every profile is a kids profile, else "". Curated (content_list) profiles
// rank first, then rating profiles by their combined caps.
func MostRestrictiveKidsProfile(profiles []models.User) string {
	if len(profiles) == 0 {
		return ""
	}
	best, bestRank := "", 0
	for _, p := range profiles {
		if !p.IsKidsProfile {
			return ""
		}
		rank := kidsRestrictionRank(p)
		if best == "" || rank < bestRank || (rank == bestRank && p.ID < best) {
			best, bestRank = p.ID, rank
		}
	}
	return best
}

func kidsRestrictionRank(p models.User) int {
	const unlimited = 1000
	switch p.KidsMode {
	case "content_list":
		return 0
	case "rating":
		movie, tv := p.KidsMaxMovieRating, p.KidsMaxTVRating
		if movie == "" && tv == "" {
			movie, tv = p.KidsMaxRating, p.KidsMaxRating
		}
		if movie == "" && tv == "" {
			return unlimited
		}
		level := func(limit, mediaType string) int {
			if strings.TrimSpace(limit) == "" {
				return 100
			}
			return kids.GetRatingLevel(limit, mediaType)
		}
		return 1 + level(movie, "movie") + level(tv, "series")
	}
	return unlimited
}

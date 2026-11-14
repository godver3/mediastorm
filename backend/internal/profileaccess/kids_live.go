package profileaccess

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/internal/apierror"
	"novastream/internal/auth"
)

// KidsLiveTVPath reports whether path serves Live TV: channels, playlists,
// streams and HLS start, catch-up, the guide, recordings and the sports
// routes that return Live TV channels.
func KidsLiveTVPath(path string) bool {
	if i := strings.Index(path, "/api/"); i >= 0 {
		path = path[i+len("/api"):]
	} else {
		return false
	}
	switch {
	case strings.HasPrefix(path, "/live/"):
		return true
	case strings.HasPrefix(path, "/sports/game/") && strings.HasSuffix(path, "/streams"):
		return true
	case path == "/sports/teams/linked-channels", strings.HasSuffix(path, "/channel-suggestions"):
		return true
	}
	return false
}

// BlockKidsFromLiveTV refuses Live TV requests made for a kids profile. Kids
// profiles get no Live TV; every app already hides it, and this enforces the
// same rule on the server for any request that names a kids profile (path,
// query or JSON body selector). Like the other kids restrictions it keys on
// the profile the request names, so it relies on profile PINs to stop a
// child from naming an adult profile instead.
func BlockKidsFromLiveTV(isKids func(profileID string) bool) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		if isKids == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || auth.IsShareLinkRequest(r) || !KidsLiveTVPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			allowed := func(id string) bool {
				if id = strings.TrimSpace(id); id != "" && isKids(id) {
					apierror.Write(w, http.StatusForbidden, "kids_live_tv_unavailable", "Live TV is not available on kids profiles")
					return false
				}
				return true
			}
			for _, key := range []string{"userID", "profileID"} {
				if !allowed(mux.Vars(r)[key]) {
					return
				}
			}
			for _, key := range []string{"profileId", "userId"} {
				for _, id := range r.URL.Query()[key] {
					if !allowed(id) {
						return
					}
				}
			}
			if !checkBody(w, r, allowed) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

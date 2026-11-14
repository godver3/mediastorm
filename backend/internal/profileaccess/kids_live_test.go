package profileaccess

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestBlockKidsFromLiveTV(t *testing.T) {
	isKids := func(id string) bool { return id == "kid" }
	router := mux.NewRouter()
	router.Use(BlockKidsFromLiveTV(isKids))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	for _, path := range []string{"/api/live/channels", "/api/live/hls/start", "/api/live/epg/now", "/api/live/recordings/epg",
		"/api/sports/game/{id}/streams", "/api/sports/teams/linked-channels", "/api/sports/teams/{teamId}/channel-suggestions",
		"/api/users/{userID}/settings", "/api/sports/scoreboard"} {
		router.HandleFunc(path, ok)
	}

	for _, tc := range []struct {
		method, target, body string
		status               int
	}{
		{http.MethodGet, "/api/live/channels?profileId=kid", "", http.StatusForbidden},
		{http.MethodPost, "/api/live/channels", `{"profileId":"kid"}`, http.StatusForbidden},
		{http.MethodGet, "/api/live/hls/start?profileId=kid&url=x", "", http.StatusForbidden},
		{http.MethodGet, "/api/live/epg/now?userId=kid", "", http.StatusForbidden},
		{http.MethodPost, "/api/live/recordings/epg", `{"profileId":"kid","channelId":"1"}`, http.StatusForbidden},
		{http.MethodGet, "/api/sports/game/7/streams?profileId=kid", "", http.StatusForbidden},
		{http.MethodGet, "/api/sports/teams/linked-channels?profileId=kid", "", http.StatusForbidden},
		{http.MethodGet, "/api/sports/teams/3/channel-suggestions?profileId=kid", "", http.StatusForbidden},
		// Adults keep Live TV; kids keep everything that isn't Live TV.
		{http.MethodGet, "/api/live/channels?profileId=adult", "", http.StatusNoContent},
		{http.MethodPost, "/api/live/channels", `{"profileId":"adult"}`, http.StatusNoContent},
		{http.MethodGet, "/api/sports/scoreboard?profileId=kid", "", http.StatusNoContent},
		{http.MethodGet, "/api/users/kid/settings", "", http.StatusNoContent},
		{http.MethodOptions, "/api/live/channels?profileId=kid", "", http.StatusNoContent},
	} {
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s %s: %d, want %d (%s)", tc.method, tc.target, tc.body, w.Code, tc.status, w.Body.String())
		}
	}
}

// A body the middleware peeked at must still reach the handler intact.
func TestBlockKidsFromLiveTVKeepsBody(t *testing.T) {
	var got string
	handler := BlockKidsFromLiveTV(func(string) bool { return false })(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		got = buf.String()
	}))
	body := `{"profileId":"adult","categories":["news"]}`
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/live/channels", strings.NewReader(body)))
	if got != body {
		t.Fatalf("handler body = %q", got)
	}
}

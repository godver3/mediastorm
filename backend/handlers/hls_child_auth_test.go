package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/services/profileunlock"
)

// Players that fetch segments without the playlist request's headers (hls.js
// on the web player, Chromecast receivers) must still present the profile
// unlock that PIN-protected HLS resources check on every request.
func TestPlaylistChildURLsCarryTokenAndProfileUnlock(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/video/hls/s1/stream.m3u8?token=abc", nil)
	r.Header.Set(profileunlock.Header, "unlock-1")
	playlist := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-MAP:URI="init.mp4"`,
		`#EXT-X-MEDIA:TYPE=SUBTITLES,URI="subs.m3u8"`,
		"segment0.m4s",
		"segment1.ts?sg=2",
	}, "\n")

	got := appendAuthTokenToPlaylist(playlist, hlsChildAuthQuery(r, "abc"))
	for _, want := range []string{
		`"init.mp4?profileUnlock=unlock-1&token=abc"`,
		`"subs.m3u8?profileUnlock=unlock-1&token=abc"`,
		"segment0.m4s?profileUnlock=unlock-1&token=abc",
		"segment1.ts?sg=2&profileUnlock=unlock-1&token=abc",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("playlist missing %q:\n%s", want, got)
		}
	}

	// Without an unlock the rewrite only adds the token, as before.
	plain := httptest.NewRequest(http.MethodGet, "/video/hls/s1/stream.m3u8?token=abc", nil)
	if got := appendAuthTokenToPlaylist("segment0.ts", hlsChildAuthQuery(plain, "abc")); got != "segment0.ts?token=abc" {
		t.Fatalf("plain rewrite = %q", got)
	}
	if got := appendAuthTokenToPlaylist("segment0.ts", hlsChildAuthQuery(plain, "")); got != "segment0.ts" {
		t.Fatalf("no auth rewrite = %q", got)
	}
}

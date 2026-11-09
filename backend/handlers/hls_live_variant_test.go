package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The shape of a Pluto TV stitcher master: relative variant URIs with long queries, no CODECS or
// RESOLUTION, a WebVTT rendition, and muxed audio in every variant.
const plutoStyleMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",DEFAULT=NO,FORCED=NO,URI="subtitle/en/playlist.m3u8?terminate=false&sid=abc&jwt=x",LANGUAGE="en"
#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=1042180,SUBTITLES="subs"
1042180/playlist.m3u8?terminate=false&sid=abc&jwt=x
#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=3321280,SUBTITLES="subs"
3321280/playlist.m3u8?terminate=false&sid=abc&jwt=x
#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=640930,SUBTITLES="subs"
640930/playlist.m3u8?terminate=false&sid=abc&jwt=x
`

func TestPickLiveHLSVariant(t *testing.T) {
	base, _ := url.Parse("https://stitcher.example/v2/stitch/hls/channel/42/master.m3u8?sid=abc")
	cases := []struct {
		name     string
		playlist string
		want     string
	}{
		{
			name:     "highest bandwidth muxed variant",
			playlist: plutoStyleMaster,
			want:     "https://stitcher.example/v2/stitch/hls/channel/42/3321280/playlist.m3u8?terminate=false&sid=abc&jwt=x",
		},
		{
			name: "absolute variant URI",
			playlist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100,RESOLUTION=640x360,CODECS=\"avc1.64001f,mp4a.40.2\"\n" +
				"https://cdn.example/low.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=900\nhttps://cdn.example/high.m3u8\n",
			want: "https://cdn.example/high.m3u8",
		},
		{
			name: "separate audio renditions keep the master",
			playlist: "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"en\",URI=\"audio.m3u8\"\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=900,AUDIO=\"aud\"\nvideo.m3u8\n",
			want: "",
		},
		{
			name:     "media playlist is left alone",
			playlist: "#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXT-X-MEDIA-SEQUENCE:7\n#EXTINF:5,\nseg7.ts\n",
			want:     "",
		},
		{
			name:     "not a playlist",
			playlist: "G@\x00\x10 transport stream bytes",
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickLiveHLSVariant(tc.playlist, base); got != tc.want {
				t.Fatalf("variant = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseHLSAttributesHandlesQuotedCommas(t *testing.T) {
	attrs := parseHLSAttributes(`BANDWIDTH=3321280,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud",RESOLUTION=1216x684`)
	want := map[string]string{"BANDWIDTH": "3321280", "CODECS": "avc1.64001f,mp4a.40.2", "AUDIO": "aud", "RESOLUTION": "1216x684"}
	for key, value := range want {
		if attrs[key] != value {
			t.Fatalf("%s = %q, want %q (all: %v)", key, attrs[key], value, attrs)
		}
	}
}

// pluto-for-channels answers /watch/<id> with a 302 to the stitcher master; the variant must be
// resolved against the stitcher, not against the redirector.
func TestResolveLiveHLSVariantFollowsRedirectToMaster(t *testing.T) {
	stitcher := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/stitch/hls/channel/42/master.m3u8" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-mpegurl; charset=utf-8")
		io.WriteString(w, plutoStyleMaster)
	}))
	defer stitcher.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, stitcher.URL+"/v2/stitch/hls/channel/42/master.m3u8?sid=abc", http.StatusFound)
	}))
	defer redirector.Close()

	got := resolveLiveHLSVariant(context.Background(), redirector.URL+"/pluto/us/watch/42", nil)
	want := stitcher.URL + "/v2/stitch/hls/channel/42/3321280/playlist.m3u8?terminate=false&sid=abc&jwt=x"
	if got != want {
		t.Fatalf("variant = %q, want %q", got, want)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer failing.Close()
	if got := resolveLiveHLSVariant(context.Background(), failing.URL+"/master.m3u8", nil); got != "" {
		t.Fatalf("variant for a failing source = %q, want empty", got)
	}
}

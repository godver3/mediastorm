package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"novastream/models"
)

func TestWebPlaybackHandlerSignedOutHonoursWebAppReturnTo(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{}, fakeWebPlaybackSessions{token: "tok"}, "/mediastorm")

	cases := []struct {
		name     string
		returnTo string
		want     string
	}{
		{"new app details", "/mediastorm/watch-next/details?mediaType=movie&titleId=tmdb:movie:1", "/mediastorm/watch-next/details?mediaType=movie&titleId=tmdb:movie:1"},
		{"new app live", "/mediastorm/watch-next/live?channel=1", "/mediastorm/watch-next/live?channel=1"},
		{"new app recordings", "/mediastorm/watch-next/recordings", "/mediastorm/watch-next/recordings"},
		{"expo details", "/mediastorm/watch/details?titleId=x", "/mediastorm/watch/details?titleId=x"},
		{"absolute url", "https://evil.example/watch-next/details", "/mediastorm/watch"},
		{"scheme relative", "//evil.example/mediastorm/watch-next/details", "/mediastorm/watch"},
		{"outside base path", "/watch-next/details", "/mediastorm/watch"},
		{"other area", "/mediastorm/admin", "/mediastorm/watch"},
		{"traversal", "/mediastorm/watch-next/../admin", "/mediastorm/watch"},
		{"player itself", "/mediastorm/watch/playback.html?x=1", "/mediastorm/watch"},
		{"lookalike prefix", "/mediastorm/watch-nextx/details", "/mediastorm/watch"},
		{"empty", "", "/mediastorm/watch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/watch/playback.html?profileId=p1"
			if tc.returnTo != "" {
				target += "&returnTo=" + url.QueryEscape(tc.returnTo)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebPlaybackHandlerSignedOutReturnWithoutBasePath(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{}, fakeWebPlaybackSessions{}, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/watch/playback.html?returnTo="+url.QueryEscape("/watch-next/details?name=Heat"), nil)
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Location"); got != "/watch-next/details?name=Heat" {
		t.Fatalf("Location = %q, want /watch-next/details?name=Heat", got)
	}
}

func TestWebPlaybackHandlerSignedInIgnoresReturnTo(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{users: []models.User{{ID: "p1"}}},
		fakeWebPlaybackSessions{token: "tok", session: models.Session{IsMaster: true}}, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/watch/playback.html?token=tok&returnTo="+url.QueryEscape("/watch-next/details"), nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestWebPlaybackTemplateAcceptsNewAppDetailsReturn(t *testing.T) {
	body, err := webTemplates.ReadFile("web_templates/playback.html")
	if err != nil {
		t.Fatalf("read web playback template: %v", err)
	}
	rendered := string(body)
	for _, want := range []string{
		"function isDetailsReturnPath(pathname)",
		"function isAppReturnPath(pathname)",
		`/\/watch(-next)?(\/|$)/`,
		"!isAppReturnPath(parsed.pathname)",
		"raw.startsWith('//')",
		"isDetailsReturnPath(referrer.pathname)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("web playback template missing details return hook %q", want)
		}
	}
	if strings.Contains(rendered, "pathname.endsWith('/watch/details')") {
		t.Fatal("playback template still only accepts the Expo /watch/details return path")
	}
}

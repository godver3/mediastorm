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
		{"app details", "/mediastorm/watch/details?mediaType=movie&titleId=tmdb:movie:1", "/mediastorm/watch/details?mediaType=movie&titleId=tmdb:movie:1"},
		{"app live", "/mediastorm/watch/live?channel=1", "/mediastorm/watch/live?channel=1"},
		{"app recordings", "/mediastorm/watch/recordings", "/mediastorm/watch/recordings"},
		{"legacy details", "/mediastorm/watch-legacy/details?titleId=x", "/mediastorm/watch-legacy/details?titleId=x"},
		{"old preview prefix", "/mediastorm/watch-next/details", "/mediastorm/watch"},
		{"absolute url", "https://evil.example/watch/details", "/mediastorm/watch"},
		{"scheme relative", "//evil.example/mediastorm/watch/details", "/mediastorm/watch"},
		{"outside base path", "/watch/details", "/mediastorm/watch"},
		{"other area", "/mediastorm/admin", "/mediastorm/watch"},
		{"traversal", "/mediastorm/watch/../admin", "/mediastorm/watch"},
		{"player itself", "/mediastorm/watch/playback.html?x=1", "/mediastorm/watch"},
		{"lookalike prefix", "/mediastorm/watchx/details", "/mediastorm/watch"},
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
	req := httptest.NewRequest(http.MethodGet, "/watch/playback.html?returnTo="+url.QueryEscape("/watch/details?name=Heat"), nil)
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Location"); got != "/watch/details?name=Heat" {
		t.Fatalf("Location = %q, want /watch/details?name=Heat", got)
	}
}

func TestWebPlaybackHandlerSignedInIgnoresReturnTo(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{users: []models.User{{ID: "p1"}}},
		fakeWebPlaybackSessions{token: "tok", session: models.Session{IsMaster: true}}, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/watch/playback.html?token=tok&returnTo="+url.QueryEscape("/watch/details"), nil)
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
		`/\/watch(-legacy)?(\/|$)/`,
		`/\/watch(-legacy)?\/details\/?$/`,
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

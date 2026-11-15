package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/sessions"
)

func TestMasterOnlySportsMutation(t *testing.T) {
	called := false
	handler := masterOnlySportsMutation(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	for _, test := range []struct {
		name       string
		isMaster   bool
		wantStatus int
		wantCalled bool
	}{
		{name: "subaccount denied", wantStatus: http.StatusForbidden},
		{name: "master allowed", isMaster: true, wantStatus: http.StatusNoContent, wantCalled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodPut, "/api/sports/settings", nil)
			req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyIsMaster, test.isMaster))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != test.wantStatus || called != test.wantCalled {
				t.Fatalf("status=%d called=%v, want status=%d called=%v", recorder.Code, called, test.wantStatus, test.wantCalled)
			}
		})
	}
}

func TestIsStreamScopedRequestAllowed(t *testing.T) {
	session := models.Session{Scope: models.SessionScopeStream, ScopeResource: "/movie.mkv"}
	allowed := []string{
		"/api/video/hls/abc/stream.m3u8",
		"/api/video/metadata?path=%2Fmovie.mkv",
		"/api/video/stream?path=/movie.mkv",
		"/api/video/stream/Movie.mkv?path=webdav/movie.mkv",
		"/api/video/direct-url?path=/movie.mkv",
		"/api/video/subtitles/start?path=/movie.mkv",
		"/api/video/hls/start?path=__shared_source__",
	}
	for _, target := range allowed {
		req := httptest.NewRequest("GET", target, nil)
		if !isStreamScopedRequestAllowed(req, session) {
			t.Errorf("expected %q to be allowed for matching stream-scoped session", target)
		}
	}

	denied := []string{
		"/api/settings",
		"/api/users/u1/history/progress",
		"/api/share/create",
		"/api/watchlist",
		"/api/discover/new",
		"/api/videos",
		"/api/video/metadata?path=/other.mkv",
		"/api/video/stream?path=/other.mkv",
		"/api/live/hls/start?url=https://example.test/live.m3u8",
		"/api/live/recordings/1/stream",
	}
	for _, target := range denied {
		req := httptest.NewRequest("GET", target, nil)
		if isStreamScopedRequestAllowed(req, session) {
			t.Errorf("expected %q to be denied for stream-scoped session", target)
		}
	}
}

func TestWatchPartySessionCannotReachProtectedAccountAPI(t *testing.T) {
	sessionsService, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessionsService.CreateScopedWithResource("account-1", false, "test", "127.0.0.1", time.Hour, models.SessionScopeWatchParty, "room-1")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := AccountAuthMiddleware(sessionsService, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("watch-party session status=%d called=%v, want 403 and no protected handler", recorder.Code, called)
	}
}

func TestJellyfinBridgeSessionCannotReachProtectedAccountAPI(t *testing.T) {
	sessionsService, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessionsService.CreateScopedWithResource("account-1", true, "test", "127.0.0.1", time.Hour, models.SessionScopeJellyfin, "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := AccountAuthMiddleware(sessionsService, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Authorization", "Bearer "+session.Token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	// Validate itself refuses bridge sessions (401); the middleware scope check
	// is a second line of defence (403). Either way the handler must not run.
	if (recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden) || called {
		t.Fatalf("jellyfin bridge session status=%d called=%v, want 401/403 and no protected handler", recorder.Code, called)
	}
}

func TestLocalhostOnlyMiddlewareUsesTCPPeer(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := localhostOnlyMiddleware(next)

	spoofed := httptest.NewRequest(http.MethodGet, "http://localhost/api/debug/runtime", nil)
	spoofed.RemoteAddr = "198.51.100.10:4321"
	spoofedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(spoofedRecorder, spoofed)
	if spoofedRecorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed Host request status = %d, want 403", spoofedRecorder.Code)
	}

	local := httptest.NewRequest(http.MethodGet, "http://example.test/api/debug/runtime", nil)
	local.RemoteAddr = "127.0.0.1:4321"
	localRecorder := httptest.NewRecorder()
	handler.ServeHTTP(localRecorder, local)
	if localRecorder.Code != http.StatusNoContent {
		t.Fatalf("loopback request status = %d, want 204", localRecorder.Code)
	}
}

func TestIsStreamScopedRequestAllowedLiveSource(t *testing.T) {
	session := models.Session{Scope: models.SessionScopeStream, ScopeResource: "https://example.test/live.m3u8"}
	req := httptest.NewRequest("GET", "/api/live/hls/start?url=https%3A%2F%2Fexample.test%2Flive.m3u8", nil)
	if !isStreamScopedRequestAllowed(req, session) {
		t.Fatal("expected matching live source to be allowed")
	}
}

func TestExtractTokenRestrictsQueryTokensToMediaRoutes(t *testing.T) {
	mediaReq := httptest.NewRequest("GET", "/api/video/stream?token=media-token&path=/movie.mkv", nil)
	if got := extractToken(mediaReq); got != "media-token" {
		t.Fatalf("media query token = %q, want media-token", got)
	}

	artworkReq := httptest.NewRequest("GET", "/api/library/items/item-1/artwork/poster?token=art-token", nil)
	if got := extractToken(artworkReq); got != "art-token" {
		t.Fatalf("artwork query token = %q, want art-token", got)
	}

	for _, target := range []string{
		"/api/video/live-direct/0123456789abcdef0123456789abcdef/stream.ts?token=media-token",
		"/api/video/thumbnails/image/0123456789abcdef01234567/thumb-0001.jpg?token=media-token",
		"/api/metadata/trailers/proxy?token=media-token&url=https://example.test/trailer",
		"/api/metadata/trailers/prequeue/serve?token=media-token&id=trailer-1",
		"/api/subtitles/download?token=media-token&subtitleId=sub-1",
		"/api/subtitles/translate?token=media-token&sourceUrl=https://example.test/sub.vtt",
		"/api/live/recordings/recording-1/stream?token=media-token",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if got := extractToken(req); got != "media-token" {
			t.Errorf("media query token for %q = %q, want media-token", target, got)
		}
	}

	settingsReq := httptest.NewRequest("GET", "/api/settings?token=leaked-token", nil)
	if got := extractToken(settingsReq); got != "" {
		t.Fatalf("general API accepted query token %q", got)
	}
}

// A guest media grant whose room cannot be checked gets a retryable 503, not
// the 401 that tells players their session was revoked.
func TestAccountAuthAnswersAuthorityOutageWithServiceUnavailable(t *testing.T) {
	sessionsService, err := sessions.NewService("", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := sessionsService.CreateWatchPartyPlayback("account-1", "test", "127.0.0.1", time.Hour, "room", "guest", "/source.mkv")
	if err != nil {
		t.Fatal(err)
	}
	ended := false
	sessionsService.SetWatchPartyPlaybackValidator(func(context.Context, string, string, string) error {
		if ended {
			return errors.New("watch room has ended")
		}
		return fmt.Errorf("%w: timeout", sessions.ErrAuthorityUnavailable)
	})
	handler := AccountAuthMiddleware(sessionsService, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	serve := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/video/hls/session/stream.m3u8", nil)
		req.Header.Set("Authorization", "Bearer "+grant.Token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if code := serve(); code != http.StatusServiceUnavailable {
		t.Fatalf("outage status=%d, want 503", code)
	}
	ended = true
	if code := serve(); code != http.StatusUnauthorized {
		t.Fatalf("ended room status=%d, want 401", code)
	}
}

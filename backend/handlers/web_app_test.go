package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novastream/models"
)

func TestWebAppHandlerServesIndex(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")

	for _, target := range []string{"/watch", "/watch/", "/watch/details?id=tt123", "/watch/live"} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if !strings.Contains(rec.Body.String(), "strmr web") {
				t.Fatalf("expected index body, got %q", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestWebAppHandlerServesAssets(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch/assets/app.js", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "console.log('app');" {
		t.Fatalf("body = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want immutable asset cache", got)
	}
}

func TestWebAppHandlerMissingAssetReturnsNotFound(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch/assets/missing.js", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), "strmr web") {
		t.Fatal("missing asset should not fall back to index.html")
	}
}

func TestWebAppHandlerMissingBundleIsClear(t *testing.T) {
	handler := NewWebAppHandler(filepath.Join(t.TempDir(), "missing"), "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "web app bundle not found") {
		t.Fatalf("expected clear missing bundle response, got %q", rec.Body.String())
	}
}

func TestWebAppHandlerRejectsTraversal(t *testing.T) {
	root := writeWebAppFixture(t)
	handler := NewWebAppHandler(root, "/watch")
	req := httptest.NewRequest(http.MethodGet, "/watch/../secret.txt", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

type fakeWebPlaybackUsers struct {
	users []models.User
}

func (f fakeWebPlaybackUsers) List() []models.User {
	return f.users
}

// fakeWebPlaybackSessions validates a single known token to the configured session.
type fakeWebPlaybackSessions struct {
	token   string
	session models.Session
}

func (f fakeWebPlaybackSessions) Validate(token string) (models.Session, error) {
	if f.token != "" && token == f.token {
		return f.session, nil
	}
	return models.Session{}, errors.New("invalid session")
}

func TestWebPlaybackHandlerServesStandalonePlayer(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{{ID: "profile-1", Name: "Main"}},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "acc", IsMaster: true}}, "/mediastorm")

	req := httptest.NewRequest(http.MethodGet, "/watch?title=Movie&token=tok", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{"<title>mediastorm player</title>", "Starting HLS session", `"/mediastorm"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
	if strings.Contains(body, `id="profileSelect"`) || strings.Contains(body, `class="profile-select"`) {
		t.Fatalf("playback page should not render a profile selector")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestWebPlaybackHandlerRedirectsWithoutValidSession(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{{ID: "profile-1", Name: "Main"}},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "acc"}}, "/mediastorm")

	req := httptest.NewRequest(http.MethodGet, "/watch/playback.html?profileId=profile-1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/mediastorm/watch" {
		t.Fatalf("Location = %q, want /mediastorm/watch", got)
	}
}

func TestWebPlaybackHandlerScopesProfilesForAccountSession(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{
		users: []models.User{
			{ID: "profile-1", Name: "One", AccountID: "account-a"},
			{ID: "profile-2", Name: "Two", AccountID: "account-b"},
		},
	}, fakeWebPlaybackSessions{token: "tok", session: models.Session{AccountID: "account-a", IsMaster: false}}, "")

	req := httptest.NewRequest(http.MethodGet, "/watch?token=tok", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"profile-1"`) {
		t.Fatalf("expected scoped profile in body")
	}
	if strings.Contains(body, `"id":"profile-2"`) {
		t.Fatalf("unexpected profile from another account in body")
	}
}

func TestWebPlaybackHandlerRejectsUnsupportedMethods(t *testing.T) {
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{}, fakeWebPlaybackSessions{}, "")
	req := httptest.NewRequest(http.MethodPost, "/watch", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want GET, HEAD", got)
	}
}

func writeWebAppFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>strmr web</html>"), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log('app');\n"), 0644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	return root
}

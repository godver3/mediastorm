package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWebAppDirPrefersEnv(t *testing.T) {
	t.Setenv("STRMR_WEB_APP_DIR", "  /opt/strmr-web ")
	if got := ResolveWebAppDir(); got != "/opt/strmr-web" {
		t.Fatalf("ResolveWebAppDir() = %q, want /opt/strmr-web", got)
	}
}

func TestResolveWebAppDirFindsWebDist(t *testing.T) {
	t.Setenv("STRMR_WEB_APP_DIR", "")
	root := t.TempDir()
	// A frontend/dist checkout next to it must not be picked: that's the legacy app.
	for _, dir := range []string{filepath.Join(root, "web", "dist"), filepath.Join(root, "frontend", "dist")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if got := ResolveWebAppDir(); got != filepath.Join("web", "dist") {
		t.Fatalf("ResolveWebAppDir() = %q, want web/dist", got)
	}
}

func TestResolveWebAppDirFindsSiblingWebDist(t *testing.T) {
	t.Setenv("STRMR_WEB_APP_DIR", "")
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	for _, dir := range []string{backend, filepath.Join(root, "web", "dist")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(backend)
	if got := ResolveWebAppDir(); got != filepath.Join("..", "web", "dist") {
		t.Fatalf("ResolveWebAppDir() = %q, want ../web/dist", got)
	}
}

func TestResolveWebAppDirDefault(t *testing.T) {
	t.Setenv("STRMR_WEB_APP_DIR", "")
	t.Chdir(t.TempDir())
	if got := ResolveWebAppDir(); got != filepath.Join("..", "web", "dist") {
		t.Fatalf("ResolveWebAppDir() = %q, want ../web/dist fallback", got)
	}
}

func TestResolveLegacyWebAppDir(t *testing.T) {
	t.Setenv("STRMR_WEB_LEGACY_DIR", "")
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(backend)
	if got := ResolveLegacyWebAppDir(); got != "" {
		t.Fatalf("ResolveLegacyWebAppDir() = %q, want \"\" without a frontend checkout", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "frontend", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveLegacyWebAppDir(); got != filepath.Join("..", "frontend", "dist") {
		t.Fatalf("ResolveLegacyWebAppDir() = %q, want ../frontend/dist", got)
	}
	t.Setenv("STRMR_WEB_LEGACY_DIR", "/opt/legacy")
	if got := ResolveLegacyWebAppDir(); got != "/opt/legacy" {
		t.Fatalf("ResolveLegacyWebAppDir() = %q, want /opt/legacy", got)
	}
}

func TestWebAppRedirectHandlerMovesWatchNextToWatch(t *testing.T) {
	h := NewWebAppRedirectHandler("/watch-next", "/watch")
	cases := []struct{ target, want string }{
		{"/watch-next", "/watch"},
		{"/watch-next/", "/watch/"},
		{"/watch-next/foo", "/watch/foo"},
		{"/watch-next/details?mediaType=movie&titleId=tmdb:movie:1", "/watch/details?mediaType=movie&titleId=tmdb:movie:1"},
		{"/watch-next/sports/game/a%20b", "/watch/sports/game/a%20b"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if rec.Code != http.StatusMovedPermanently {
				t.Fatalf("status = %d, want 301", rec.Code)
			}
			loc := rec.Header().Get("Location")
			// The browser resolves the relative Location against the URL it requested,
			// with or without a server base path in front.
			for _, prefix := range []string{"", "/media"} {
				base, _ := url.Parse("http://host" + prefix + tc.target)
				ref, err := url.Parse(loc)
				if err != nil {
					t.Fatalf("bad Location %q: %v", loc, err)
				}
				got := base.ResolveReference(ref)
				want := prefix + tc.want
				if got.RequestURI() != want {
					t.Fatalf("Location %q from %s%s resolves to %q, want %q", loc, prefix, tc.target, got.RequestURI(), want)
				}
			}
		})
	}
}

func TestWebAppRedirectHandlerIgnoresLookalikePrefix(t *testing.T) {
	h := NewWebAppRedirectHandler("/watch-next", "/watch")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch-nextx", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestWebAppHandlerServesIndexForClientRoutes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<div id=root>next</div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewWebAppHandler(root, "/watch")
	for _, target := range []string{"/watch", "/watch/", "/watch/search?q=x", "/watch/profiles"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "next") {
			t.Fatalf("%s: status=%d body=%q", target, rec.Code, rec.Body.String())
		}
	}
}

func TestWebAppHandlerRewritesLegacyIndexPaths(t *testing.T) {
	root := t.TempDir()
	index := `<link href="/watch/manifest.json"><script src="/watch/_expo/static/js/web/index.js"></script><a href="/watchlist">`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewWebAppHandler(root, "/watch-legacy")
	h.IndexPathRewrite = &PathRewrite{From: "/watch/", To: "/watch-legacy/"}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch-legacy/details", nil))
	body := rec.Body.String()
	for _, want := range []string{`"/watch-legacy/manifest.json"`, `"/watch-legacy/_expo/static/js/web/index.js"`, `"/watchlist"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("rewritten index missing %s: %q", want, body)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

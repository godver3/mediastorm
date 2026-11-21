package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNewWebAppDirPrefersEnv(t *testing.T) {
	t.Setenv("STRMR_WEB_NEXT_DIR", "  /opt/strmr-web-next ")
	if got := ResolveNewWebAppDir(); got != "/opt/strmr-web-next" {
		t.Fatalf("ResolveNewWebAppDir() = %q, want /opt/strmr-web-next", got)
	}
}

func TestResolveNewWebAppDirFindsWebDist(t *testing.T) {
	t.Setenv("STRMR_WEB_NEXT_DIR", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if got := ResolveNewWebAppDir(); got != filepath.Join("web", "dist") {
		t.Fatalf("ResolveNewWebAppDir() = %q, want web/dist", got)
	}
}

func TestResolveNewWebAppDirFindsSiblingWebDist(t *testing.T) {
	t.Setenv("STRMR_WEB_NEXT_DIR", "")
	root := t.TempDir()
	backend := filepath.Join(root, "backend")
	for _, dir := range []string{backend, filepath.Join(root, "web", "dist")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(backend)
	if got := ResolveNewWebAppDir(); got != filepath.Join("..", "web", "dist") {
		t.Fatalf("ResolveNewWebAppDir() = %q, want ../web/dist", got)
	}
}

func TestResolveNewWebAppDirDefault(t *testing.T) {
	t.Setenv("STRMR_WEB_NEXT_DIR", "")
	t.Chdir(t.TempDir())
	if got := ResolveNewWebAppDir(); got != filepath.Join("..", "web", "dist") {
		t.Fatalf("ResolveNewWebAppDir() = %q, want ../web/dist fallback", got)
	}
}

func TestWebNextHandlerServesIndexForClientRoutes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<div id=root>next</div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewWebAppHandler(root, "/watch-next")
	for _, target := range []string{"/watch-next", "/watch-next/", "/watch-next/search?q=x", "/watch-next/profiles"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "next") {
			t.Fatalf("%s: status=%d body=%q", target, rec.Code, rec.Body.String())
		}
	}
}

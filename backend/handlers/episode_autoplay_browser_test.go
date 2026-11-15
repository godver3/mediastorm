package handlers

import (
	"net/http"
	"net/http/httptest"
	"novastream/models"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A real rendered player and decoded media, with isolated synthetic next-source failures.
func TestEpisodeAutoplayRenderedPlayerBrowser(t *testing.T) {
	python, chrome := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chrome == "" {
		t.Skip("Playwright and Chromium required")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required")
	}
	media := filepath.Join(t.TempDir(), "synthetic.mp4")
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "8", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-c:a", "aac", "-movflags", "+faststart", media)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate media: %v %s", err, out)
	}
	handler := NewWebPlaybackHandler(fakeWebPlaybackUsers{users: []models.User{{ID: "profile-a", Name: "Synthetic profile"}}}, fakeWebPlaybackSessions{token: "test-token", session: models.Session{AccountID: "account-a", IsMaster: true}}, "")
	mux := http.NewServeMux()
	mux.HandleFunc("/media.mp4", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, media) })
	mux.Handle("/player", handler)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if filepath.Ext(r.URL.Path) == ".js" {
			w.Header().Set("Content-Type", "text/javascript")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	cmd = exec.CommandContext(t.Context(), python, "testdata/episode_autoplay_browser.py", "--base-url", server.URL, "--chromium", chrome, "--artifacts", artifacts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rendered autoplay browser: %v\n%s", err, out)
	}
}

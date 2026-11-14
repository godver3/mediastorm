package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/badstreams"
)

func releaseReportBrowserFixture(t *testing.T) http.Handler {
	t.Helper()
	h, entry, registry := releaseReportFixture(t)
	profile := models.User{ID: "profile", AccountID: "account", Name: "Preview profile"}
	web := NewWebPlaybackHandler(fakeWebPlaybackUsers{users: []models.User{profile}}, fakeWebPlaybackSessions{token: "fixture-token", session: models.Session{Token: "fixture-token", AccountID: "account", IsMaster: true}}, "")
	bad := NewBadStreamsHandler(registry)
	bad.SetUsersService(h.users)
	router := mux.NewRouter()
	router.HandleFunc("/watch/playback.html", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fixtureTransport") != "1" {
			web.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		web.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		body := strings.Replace(recorder.Body.String(), "</body>", releasePreparedPreviewTransport+"</body>", 1)
		w.WriteHeader(recorder.Code)
		_, _ = w.Write([]byte(body))
	})
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		params := url.Values{"token": {"fixture-token"}, "profileId": {"profile"}, "title": {"Synthetic Movie"}, "sourcePath": {entry.StreamPath}, "prequeueId": {entry.ID}, "fixtureTransport": {"1"}}
		http.Redirect(w, r, "/watch/playback.html?"+params.Encode(), http.StatusSeeOther)
	}).Methods("GET")
	router.PathPrefix("/api/static/").Handler(http.StripPrefix("/api/static/", http.FileServer(http.Dir("static"))))
	router.HandleFunc("/api/playback/prequeue/{prequeueID}/report", h.ReleaseReportOptions).Methods("GET")
	router.HandleFunc("/api/playback/prequeue/{prequeueID}/report", h.ReportRelease).Methods("POST")
	router.HandleFunc("/api/playback/prequeue/{prequeueID}/retry", h.RetryRelease).Methods("POST")
	router.HandleFunc("/api/playback/prequeue/{prequeueID}/retry", h.CancelReleaseRetry).Methods("DELETE")
	router.HandleFunc("/api/playback/prequeue/{prequeueID}", h.GetStatus).Methods("GET")
	router.HandleFunc("/api/playback/bad-streams/{id}", bad.Delete).Methods("DELETE")
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"fixture": "release-report-player", "prequeueId": entry.ID, "profile": "profile"})
	})
	router.HandleFunc("/fixture/status", func(w http.ResponseWriter, r *http.Request) {
		de := badstreams.PlaybackContext{ProfileID: "other-profile", TitleID: entry.TitleID, AudioLanguage: "deu"}
		en := de
		en.AudioLanguage = "eng"
		_ = json.NewEncoder(w).Encode(map[string]any{"reports": len(registry.List()), "germanExcluded": registry.MatchFor(*entry.SelectedResult, de) != nil, "englishExcluded": registry.MatchFor(*entry.SelectedResult, en) != nil})
	})
	router.PathPrefix("/api/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/api/users/profile/settings" {
			settings, _ := h.userSettingsSvc.GetWithDefaults("profile", models.DefaultUserSettings())
			_ = json.NewEncoder(w).Encode(settings)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
		ctx = context.WithValue(ctx, auth.ContextKeySession, models.Session{Token: "fixture-token", AccountID: "account", IsMaster: true})
		router.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestReleaseReportPlayerBrowser(t *testing.T) {
	python, chromium := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chromium == "" {
		t.Skip("set MEDIASTORM_BROWSER_PYTHON and MEDIASTORM_CHROMIUM to run the browser suite")
	}
	server := httptest.NewServer(releaseReportBrowserFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), python, filepath.Join("testdata", "release_report_player.py"), "--base-url", server.URL, "--chromium", chromium, "--artifacts", artifacts)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("release report player browser suite: %v\n%s", err, output)
	}
}

func TestReleaseReportPlayerPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_RELEASE_REPORT_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set MEDIASTORM_RELEASE_REPORT_PREVIEW_ADDR to serve an isolated synthetic preview")
	}
	server := &http.Server{Addr: addr, Handler: releaseReportBrowserFixture(t)}
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Synthetic release reporting preview: http://%s/fixture/info", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// Available only from this opt-in fixture server, never from application routes.
const releasePreparedPreviewTransport = `<script>
document.addEventListener('DOMContentLoaded', () => {
  let position = 42;
  currentAbsoluteTime = () => position;
  startWebPlayerFromSource = async options => {
    position = options?.startOffset == null ? 42 : Math.max(0, Number(options.startOffset) || 0);
    const host = document.getElementById('videoHost');
    const label = document.createElement('div');
    label.style.cssText = 'min-height:280px;display:grid;place-items:center;padding:24px;background:#15202c;color:#a8c5ff';
    label.textContent = 'SYNTHETIC PREVIEW: media transport is stubbed. Position: ' + position + ' seconds. Use Report / change source to prepare and confirm an alternative.';
    host.replaceChildren(label);
  };
});
</script>`

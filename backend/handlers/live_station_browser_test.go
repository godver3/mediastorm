package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	user_settings "novastream/services/user_settings"
	"novastream/services/users"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newStationSettingsBrowserFixture(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	cfg := config.NewManager(dir + "/settings.json")
	settings := config.DefaultSettings()

	settings.Cache.Directory = dir + "/cache"
	settings.Live.Sources = []config.LivePlaylistSource{
		{ID: "german-a", Name: "German list A (fixture)", Mode: "m3u", PlaylistURL: "https://example.test/a.m3u", MaxStreams: 1},
		{ID: "german-b", Name: "German list B (fixture)", Mode: "m3u", PlaylistURL: "https://example.test/b.m3u", MaxStreams: 1},
	}

	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	profiles, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	adult, _ := profiles.CreateForAccount("household-a", "Adult")
	child, _ := profiles.CreateForAccount("household-a", "Child")
	_, _ = profiles.CreateForAccount("household-b", "Other")
	prefs, err := user_settings.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	admin := NewAdminUIHandler(dir+"/settings.json", "", nil, profiles, prefs, cfg)
	settingsHandler := NewSettingsHandler(cfg)
	router := http.NewServeMux()
	router.Handle("/api/static/", http.StripPrefix("/api/static/", http.FileServer(http.Dir("static"))))
	router.HandleFunc("/admin/settings", admin.SettingsPage)
	router.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	router.Handle("/admin/static/", http.StripPrefix("/admin/static/", http.FileServer(http.Dir("static"))))
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"fixture": "live-station-settings", "adult": adult.ID, "child": child.ID})
	})
	router.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/admin/api") {
		case "/settings":
			if r.Method == http.MethodPut {
				settingsHandler.PutSettings(w, r)
			} else {
				settingsHandler.GetSettings(w, r)
			}
			return
		case "/schema":
			admin.GetSchema(w, r)
			return
		case "/profiles":
			_ = json.NewEncoder(w).Encode(profiles.ListAll())
			return
		case "/accounts":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "household-a", "username": "Household A"}, {"id": "household-b", "username": "Household B"}})
			return
		case "/user-settings":
			admin.GetUserSettings(w, r)
			return
		case "/clients":
			_ = json.NewEncoder(w).Encode(map[string]any{"clients": []any{}})
			return
		case "/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "Local fixture", "defaultPassword": false, "adminWalkthroughDismissed": true})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	server := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), adminSessionContextKey{}, &models.Session{AccountID: "fixture-admin", IsMaster: true})
		ctx = context.WithValue(ctx, auth.ContextKeyAccountID, "fixture-admin")
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
		router.ServeHTTP(w, r.WithContext(ctx))
	})
	return server
}

func TestStationSettingsBrowserRoundTrip(t *testing.T) {
	python := os.Getenv("MEDIASTORM_BROWSER_PYTHON")
	chromium := os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chromium == "" {
		t.Skip("set MEDIASTORM_BROWSER_PYTHON and MEDIASTORM_CHROMIUM to run the Playwright UI suite")
	}
	server := httptest.NewServer(newStationSettingsBrowserFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, filepath.Join("testdata", "live_station_settings.py"), "--base-url", server.URL, "--chromium", chromium, "--artifacts", artifacts)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("settings browser suite: %v\n%s", err, output)
	}
}

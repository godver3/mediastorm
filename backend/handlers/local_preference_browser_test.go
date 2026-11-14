package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/user_settings"
	"novastream/services/users"
)

func newLocalPreferenceBrowserFixture(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	cfg := config.NewManager(filepath.Join(dir, "settings.json"))
	settings := config.DefaultSettings()
	settings.Cache.Directory = filepath.Join(dir, "cache")
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	profiles, err := users.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := profiles.CreateForAccount("fixture-household", "Profile A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := profiles.CreateForAccount("fixture-household", "Profile B")
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := user_settings.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := prefs.Update(first.ID, models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: "deu"}}); err != nil {
		t.Fatal(err)
	}
	admin := NewAdminUIHandler(filepath.Join(dir, "settings.json"), "", nil, profiles, prefs, cfg)
	account := NewAccountUIHandler(nil, nil, profiles, prefs, nil, cfg, nil)
	settingsHandler := NewSettingsHandler(cfg)
	router := http.NewServeMux()
	for _, prefix := range []string{"/api/static/", "/admin/static/", "/account/static/", "/static/"} {
		router.Handle(prefix, http.StripPrefix(prefix, http.FileServer(http.Dir("static"))))
	}
	router.HandleFunc("/admin/settings", admin.SettingsPage)
	router.HandleFunc("/account/settings", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Account": models.Account{ID: "fixture-household", Username: "Preview household"}, "Profiles": profiles.ListForAccount("fixture-household"), "ActivePage": "settings", "BasePath": "/account", "ServerBasePath": ""}
		if err := account.settingsTemplate.ExecuteTemplate(w, "settings", data); err != nil {
			t.Errorf("render account settings: %v", err)
		}
	})
	router.HandleFunc("/account/api/user-settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			account.SaveUserSettings(w, r)
		} else {
			account.GetUserSettings(w, r)
		}
	})
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"fixture": "local-preference-settings", "first": first.ID, "second": second.ID})
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
			_ = json.NewEncoder(w).Encode([]models.Account{{ID: "fixture-household", Username: "Preview household"}})
			return
		case "/user-settings":
			if r.Method == http.MethodPut {
				admin.SaveUserSettings(w, r)
			} else {
				admin.GetUserSettings(w, r)
			}
			return
		case "/clients":
			_ = json.NewEncoder(w).Encode([]any{})
			return
		case "/status", "/onboarding/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "Local preference preview", "defaultPassword": false, "adminWalkthroughDismissed": true})
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), adminSessionContextKey{}, &models.Session{AccountID: "fixture-household", IsMaster: true})
		ctx = context.WithValue(ctx, accountSessionKey{}, &models.Session{AccountID: "fixture-household", IsMaster: true})
		ctx = context.WithValue(ctx, auth.ContextKeyAccountID, "fixture-household")
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
		router.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestLocalPreferenceSettingsBrowser(t *testing.T) {
	python, chromium := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chromium == "" {
		t.Skip("set MEDIASTORM_BROWSER_PYTHON and MEDIASTORM_CHROMIUM to run the browser suite")
	}
	server := httptest.NewServer(newLocalPreferenceBrowserFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), python, filepath.Join("testdata", "local_preference_settings.py"), "--base-url", server.URL, "--chromium", chromium, "--artifacts", artifacts)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local preference browser suite: %v\n%s", err, output)
	}
}

func TestLocalPreferenceSettingsPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_LOCAL_PREFERENCE_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set MEDIASTORM_LOCAL_PREFERENCE_PREVIEW_ADDR to serve an isolated synthetic preview")
	}
	server := &http.Server{Addr: addr, Handler: newLocalPreferenceBrowserFixture(t)}
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Synthetic profile preference preview listening at http://%s/account/settings", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

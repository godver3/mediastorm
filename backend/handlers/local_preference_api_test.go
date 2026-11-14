package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
)

type localPreferencePrequeueClearer struct{ users []string }

func (c *localPreferencePrequeueClearer) DeleteByUser(userID string) {
	c.users = append(c.users, userID)
}

func TestLocalPreferenceAPIInvalidatesPreparedSources(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			service := &fakeUserSettingsService{getSettings: &models.UserSettings{Playback: models.PlaybackSettings{PreferLocalFiles: models.BoolPtr(false)}}}
			cfg := config.NewManager(t.TempDir() + "/settings.json")
			settings := config.DefaultSettings()
			settings.UI.UserEditableSettings = []string{"playback.preferLocalFiles"}
			if err := cfg.Save(settings); err != nil {
				t.Fatal(err)
			}
			h := handlers.NewUserSettingsHandler(service, &fakeUserExistsService{exists: true}, cfg)
			clearer := &localPreferencePrequeueClearer{}
			h.PrequeueStore = clearer
			body := map[string]any{"playback": map[string]any{"preferLocalFiles": true}}
			if method == http.MethodPatch {
				body = map[string]any{"path": "playback.preferLocalFiles", "value": true}
			}
			r := userSettingsRequest(method, "/", body, map[string]string{"userID": "u1"})
			w := httptest.NewRecorder()
			if method == http.MethodPatch {
				h.PatchFrontendSetting(w, r)
			} else {
				h.PutSettings(w, r)
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if !models.BoolVal(service.updatedSettings.Playback.PreferLocalFiles, false) || len(clearer.users) != 1 || clearer.users[0] != "u1" {
				t.Fatal("preference change did not invalidate only the affected profile's prepared streams")
			}
		})
	}
}

func TestLocalPreferenceAPIDefaultReflectsServer(t *testing.T) {
	service := &fakeUserSettingsService{}
	cfg := config.NewManager(t.TempDir() + "/settings.json")
	settings := config.DefaultSettings()
	settings.Playback.PreferLocalFiles = true
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	h := handlers.NewUserSettingsHandler(service, &fakeUserExistsService{exists: true}, cfg)
	w := httptest.NewRecorder()
	h.GetSettings(w, userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"}))
	if w.Code != http.StatusOK || !models.BoolVal(service.lastDefaults.Playback.PreferLocalFiles, false) {
		t.Fatal("profile API did not inherit the server preference")
	}
}

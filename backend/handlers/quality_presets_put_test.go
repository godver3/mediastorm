package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/handlers"
	"novastream/models"
	"novastream/services/clients"
)

// A whole-document settings PUT neither sets nor clears preset choices:
// those change only through /quality-presets/assign, which checks what the
// caller may choose.
func TestUserSettingsPutKeepsQualityPresets(t *testing.T) {
	stored := "stored"
	settingsSvc := &fakeUserSettingsService{getSettings: &models.UserSettings{
		QualityPresets: &models.QualityPresetSelection{MoviePreset: &stored, PickerPresets: &[]string{"a"}},
	}}
	h := handlers.NewUserSettingsHandler(settingsSvc, &fakeUserExistsService{exists: true}, config.NewManager(t.TempDir()))
	for name, body := range map[string]models.UserSettings{
		"sent other presets": {QualityPresets: &models.QualityPresetSelection{MoviePreset: models.StringPtr("other")}},
		"sent none":          {},
	} {
		w := httptest.NewRecorder()
		h.PutSettings(w, userSettingsRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"}))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", name, w.Code, w.Body.String())
		}
		got := settingsSvc.updatedSettings.QualityPresets
		if got == nil || got.MoviePreset == nil || *got.MoviePreset != stored || got.PickerPresets == nil {
			t.Errorf("%s: stored presets = %+v, want the previous ones", name, got)
		}
	}
}

func TestClientSettingsPutKeepsQualityPresets(t *testing.T) {
	svc, err := clients.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register("device-1", "profile-a", "Apple TV", "tvOS", "1.0", "TV", ""); err != nil {
		t.Fatal(err)
	}
	stored := "stored"
	settings := &capturingClientSettings{stored: &models.ClientFilterSettings{QualityPresetSeries: &stored}}
	ownership := &fakeClientOwnership{membership: map[string]map[string]bool{"account-a": {"profile-a": true}}}
	h := handlers.NewClientsHandler(svc, settings, ownership)
	body := models.ClientFilterSettings{QualityPresetMovie: models.StringPtr("other")}
	w := httptest.NewRecorder()
	h.UpdateSettings(w, clientsAuthRequest(http.MethodPut, "/api/clients/device-1/settings?userId=profile-a", body, map[string]string{"clientID": "device-1"}, "account-a", false))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := settings.stored
	if got.QualityPresetMovie != nil || got.QualityPresetSeries == nil || *got.QualityPresetSeries != stored {
		t.Errorf("stored presets = movie %v series %v, want only the previous series preset", got.QualityPresetMovie, got.QualityPresetSeries)
	}
}

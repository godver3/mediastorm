package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/handlers"
	"novastream/internal/auth"
	"novastream/models"

	"github.com/gorilla/mux"
)

// fakeUserSettingsService implements userSettingsService for testing.
type fakeUserSettingsService struct {
	getSettings        *models.UserSettings
	getErr             error
	getWithDefaultsVal models.UserSettings
	getWithDefaultsErr error
	lastDefaults       models.UserSettings
	updateErr          error
	deleteErr          error
}

func (f *fakeUserSettingsService) Get(userID string) (*models.UserSettings, error) {
	return f.getSettings, f.getErr
}

func (f *fakeUserSettingsService) GetWithDefaults(userID string, defaults models.UserSettings) (models.UserSettings, error) {
	f.lastDefaults = defaults
	return f.getWithDefaultsVal, f.getWithDefaultsErr
}

func (f *fakeUserSettingsService) Update(userID string, settings models.UserSettings) error {
	return f.updateErr
}

func (f *fakeUserSettingsService) Delete(userID string) error {
	return f.deleteErr
}

// fakeUserExistsService implements userService for testing.
type fakeUserExistsService struct {
	exists bool
}

func (f *fakeUserExistsService) Exists(id string) bool {
	return f.exists
}

func userSettingsRequest(method, path string, body any, vars map[string]string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if len(vars) > 0 {
		r = mux.SetURLVars(r, vars)
	}
	ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "acct-1")
	return r.WithContext(ctx)
}

func TestUserSettingsHandler_GetSettings_Success(t *testing.T) {
	expected := models.UserSettings{
		Playback: models.PlaybackSettings{PreferredPlayer: "native"},
	}
	settingsSvc := &fakeUserSettingsService{getWithDefaultsVal: expected}
	usersSvc := &fakeUserExistsService{exists: true}

	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.GetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestUserSettingsHandler_GetSettings_MissingUserID(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, nil, cfgMgr)

	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": ""})
	w := httptest.NewRecorder()
	h.GetSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUserSettingsHandler_GetSettings_UserNotFound(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	usersSvc := &fakeUserExistsService{exists: false}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "nonexistent"})
	w := httptest.NewRecorder()
	h.GetSettings(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUserSettingsHandler_GetSettings_ServiceError(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{getWithDefaultsErr: errors.New("db error")}
	usersSvc := &fakeUserExistsService{exists: true}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.GetSettings(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestUserSettingsHandler_PutSettings_Success(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	usersSvc := &fakeUserExistsService{exists: true}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	body := models.UserSettings{
		Playback: models.PlaybackSettings{PreferredPlayer: "vlc"},
	}
	r := userSettingsRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.PutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUserSettingsHandler_PutSettings_InvalidJSON(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	usersSvc := &fakeUserExistsService{exists: true}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	r := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString("{bad"))
	r = mux.SetURLVars(r, map[string]string{"userID": "u1"})

	w := httptest.NewRecorder()
	h.PutSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUserSettingsHandler_PutSettings_ServiceError(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{updateErr: errors.New("disk full")}
	usersSvc := &fakeUserExistsService{exists: true}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	body := models.UserSettings{}
	r := userSettingsRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.PutSettings(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestUserSettingsHandler_Options(t *testing.T) {
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir)
	h := handlers.NewUserSettingsHandler(&fakeUserSettingsService{}, nil, cfgMgr)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	h.Options(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUserSettingsHandler_GetSettings_DefaultsIncludeDownloadPreferredTerms(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	usersSvc := &fakeUserExistsService{exists: true}
	tmpDir := t.TempDir()
	cfgMgr := config.NewManager(tmpDir + "/settings.json")

	settings := config.DefaultSettings()
	settings.Filtering.RequiredTerms = []string{"Multi", "French"}
	settings.Filtering.DownloadPreferredTerms = []string{"x265=3"}
	if err := cfgMgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	defaults := h.GetSettings
	_ = defaults

	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.GetSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := settingsSvc.lastDefaults.Filtering.DownloadPreferredTerms; len(got) != 1 || got[0] != "x265=3" {
		t.Fatalf("downloadPreferredTerms defaults = %v, want [x265=3]", got)
	}
	if got := settingsSvc.lastDefaults.Filtering.RequiredTerms; len(got) != 2 || got[0] != "Multi" || got[1] != "French" {
		t.Fatalf("requiredTerms defaults = %v, want [Multi French]", got)
	}
}

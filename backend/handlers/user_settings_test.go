package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	updatedSettings    models.UserSettings
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
	f.updatedSettings = settings
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

func TestUserSettingsHandler_GetSettings_ProjectsGlobalSpoilerDefaults(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	cfgMgr := config.NewManager(t.TempDir() + "/settings.json")
	cfg := config.DefaultSettings()
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	h := handlers.NewUserSettingsHandler(settingsSvc, &fakeUserExistsService{exists: true}, cfgMgr)
	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.GetSettings(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
	}

	display := settingsSvc.lastDefaults.Display
	for name, option := range map[string]*bool{
		"disable TV home card dimming":      display.DisableTVHomeCardDimming,
		"series backdrop fallback":          display.ShowSeriesBackdropForMissingEpisodeArt,
		"blur unwatched episode thumbnails": display.BlurUnwatchedEpisodeThumbnails,
		"blur unwatched episode overviews":  display.BlurUnwatchedEpisodeOverviews,
	} {
		if option == nil || !*option {
			t.Fatalf("global %s default was not projected", name)
		}
	}
	if display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent == nil || *display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent {
		t.Fatal("current episode thumbnail must remain unblurred")
	}
	if display.BlurUnwatchedEpisodeOverviewsIncludeCurrent == nil || *display.BlurUnwatchedEpisodeOverviewsIncludeCurrent {
		t.Fatal("current episode overview must remain unblurred")
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

func TestUserSettingsHandler_PutSettings_PreservesNullAndExplicitZeroValues(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	usersSvc := &fakeUserExistsService{exists: true}
	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, config.NewManager(t.TempDir()))

	body := json.RawMessage(`{
		"playback": {
			"matchFrameRate": false,
			"maxResultsPerResolution": 0,
			"pauseWhenAppInactive": null
		},
		"display": {
			"enableAnimations": null
		},
		"filtering": {
			"requiredTerms": [],
			"filterOutTerms": null
		}
	}`)
	r := userSettingsRequest(http.MethodPut, "/", body, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.PutSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	got := settingsSvc.updatedSettings
	if got.Playback.MatchFrameRate == nil || *got.Playback.MatchFrameRate {
		t.Fatalf("matchFrameRate = %v, want explicit false", got.Playback.MatchFrameRate)
	}
	if got.Playback.MaxResultsPerResolution == nil || *got.Playback.MaxResultsPerResolution != 0 {
		t.Fatalf("maxResultsPerResolution = %v, want explicit zero", got.Playback.MaxResultsPerResolution)
	}
	if got.Playback.PauseWhenAppInactive != nil {
		t.Fatalf("pauseWhenAppInactive = %v, want nil inheritance", got.Playback.PauseWhenAppInactive)
	}
	if got.Display.EnableAnimations != nil {
		t.Fatalf("enableAnimations = %v, want nil inheritance", got.Display.EnableAnimations)
	}
	if got.Filtering.RequiredTerms == nil || len(got.Filtering.RequiredTerms) != 0 {
		t.Fatalf("requiredTerms = %#v, want explicit empty override", got.Filtering.RequiredTerms)
	}
	if got.Filtering.FilterOutTerms != nil {
		t.Fatalf("filterOutTerms = %#v, want nil inheritance", got.Filtering.FilterOutTerms)
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

func TestUserSettingsHandler_PatchFrontendSetting_PreservesOtherOverrides(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{
		getSettings: &models.UserSettings{
			Playback: models.PlaybackSettings{PreferredPlayer: "native", SubtitleSize: 1.25},
		},
	}
	usersSvc := &fakeUserExistsService{exists: true}
	cfgMgr := config.NewManager(t.TempDir() + "/settings.json")
	cfg := config.DefaultSettings()
	cfg.UI.UserEditableSettings = []string{"playback.preferredPlayer"}
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	h := handlers.NewUserSettingsHandler(settingsSvc, usersSvc, cfgMgr)

	r := userSettingsRequest(http.MethodPatch, "/", map[string]any{
		"path": "playback.preferredPlayer", "value": "vlc",
	}, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.PatchFrontendSetting(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if got := settingsSvc.updatedSettings.Playback.PreferredPlayer; got != "vlc" {
		t.Fatalf("preferredPlayer = %q, want vlc", got)
	}
	if got := settingsSvc.updatedSettings.Playback.SubtitleSize; got != 1.25 {
		t.Fatalf("subtitleSize = %v, want preserved 1.25", got)
	}
}

func TestUserSettingsHandler_PatchFrontendSetting_RejectsUnexposedPath(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	cfgMgr := config.NewManager(t.TempDir() + "/settings.json")
	cfg := config.DefaultSettings()
	cfg.UI.UserEditableSettings = []string{"playback.preferredPlayer"}
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	h := handlers.NewUserSettingsHandler(settingsSvc, &fakeUserExistsService{exists: true}, cfgMgr)

	r := userSettingsRequest(http.MethodPatch, "/", map[string]any{
		"path": "display.enableAnimations", "value": false,
	}, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.PatchFrontendSetting(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body.String())
	}
}

func TestUserSettingsHandler_PatchFrontendSetting_SupportsNestedProfileField(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{}
	cfgMgr := config.NewManager(t.TempDir() + "/settings.json")
	cfg := config.DefaultSettings()
	cfg.UI.UserEditableSettings = []string{"filtering.debrid.hdrDvPolicy"}
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	h := handlers.NewUserSettingsHandler(settingsSvc, &fakeUserExistsService{exists: true}, cfgMgr)

	r := userSettingsRequest(http.MethodPatch, "/", map[string]any{
		"path": "filtering.debrid.hdrDvPolicy", "value": "hdr",
	}, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.PatchFrontendSetting(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
	}
	if settingsSvc.updatedSettings.Filtering.Debrid == nil || settingsSvc.updatedSettings.Filtering.Debrid.HDRDVPolicy != models.HDRDVPolicyIncludeHDR {
		t.Fatalf("nested filtering override was not saved: %#v", settingsSvc.updatedSettings.Filtering.Debrid)
	}
}

func TestUserSettingsHandler_GetFrontendSettingState(t *testing.T) {
	settingsSvc := &fakeUserSettingsService{getSettings: &models.UserSettings{
		Playback: models.PlaybackSettings{PreferredPlayer: "vlc"},
		Display:  models.DisplaySettings{EnableAnimations: models.BoolPtr(false)},
	}}
	cfgMgr := config.NewManager(t.TempDir() + "/settings.json")
	cfg := config.DefaultSettings()
	cfg.UI.UserEditableSettings = []string{
		"playback.preferredPlayer",
		"playback.subtitleSize",
		"display.enableAnimations",
	}
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	h := handlers.NewUserSettingsHandler(settingsSvc, &fakeUserExistsService{exists: true}, cfgMgr)
	r := userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"})
	w := httptest.NewRecorder()
	h.GetFrontendSettingState(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var state struct {
		OverriddenPaths []string `json:"overriddenPaths"`
	}
	if err := json.NewDecoder(w.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	want := []string{"display.enableAnimations", "playback.preferredPlayer"}
	if !reflect.DeepEqual(state.OverriddenPaths, want) {
		t.Fatalf("overridden paths = %#v, want %#v", state.OverriddenPaths, want)
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

func TestUserSettingsHandler_NextEpisodeAutoplayDefaultIsIndependentOfCredits(t *testing.T) {
	for _, credits := range []bool{false, true} {
		svc := &fakeUserSettingsService{}
		manager := config.NewManager(t.TempDir() + "/settings.json")
		cfg := config.DefaultSettings()
		cfg.Playback.CreditsAutoSkip = credits
		if err := manager.Save(cfg); err != nil {
			t.Fatal(err)
		}
		h := handlers.NewUserSettingsHandler(svc, &fakeUserExistsService{exists: true}, manager)
		w := httptest.NewRecorder()
		h.GetSettings(w, userSettingsRequest(http.MethodGet, "/", nil, map[string]string{"userID": "u1"}))
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		if svc.lastDefaults.Playback.NextEpisodeAutoPlay == nil || !*svc.lastDefaults.Playback.NextEpisodeAutoPlay {
			t.Fatal("legacy autoplay default depended on credits skipping")
		}
	}
}

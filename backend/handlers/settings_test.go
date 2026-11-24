package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/epg"
)

// mockPrequeueClearer records whether DeleteAll was called.
type mockPrequeueClearer struct {
	called atomic.Int32
}

func (m *mockPrequeueClearer) DeleteAll() {
	m.called.Add(1)
}

type mockSearchCacheClearer struct {
	called atomic.Int32
}

func TestResolutionStrategyChangesClearPrequeues(t *testing.T) {
	base := config.DefaultSettings()
	tests := map[string]func(*config.Settings){
		"resolve candidates early": func(s *config.Settings) {
			s.Streaming.ResolveFirstReadySource = !s.Streaming.ResolveFirstReadySource
		},
		"end race early": func(s *config.Settings) {
			s.Streaming.ResolutionEndRaceEarly = !s.Streaming.ResolutionEndRaceEarly
		},
		"settle window": func(s *config.Settings) {
			s.Streaming.ResolutionSettleWindowMs++
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if !shouldClearPrequeueForGlobalSettingsChange(base, changed) {
				t.Fatal("resolution strategy change did not invalidate prepared prequeues")
			}
		})
	}
}

func (m *mockSearchCacheClearer) ClearSearchCache() {
	m.called.Add(1)
}

func TestSettingsHandler_GetSettings(t *testing.T) {
	cfg := config.Settings{
		Server: config.ServerSettings{Host: "127.0.0.1", Port: 9999},
		UI: config.UISettings{UserEditableSettings: []string{
			"playback.preferredPlayer",
			"server.port",
			"playback.preferredPlayer",
		}},
		Usenet: []config.UsenetSettings{
			{Name: "Test", Host: "news.example", Port: 563, SSL: true, Username: "user", Password: "pass", Connections: 16, Enabled: true},
		},
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	handler := NewSettingsHandler(mgr)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	handler.GetSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type %q", got)
	}

	var got struct {
		config.Settings
		UserEditableSettings       []string                             `json:"userEditableSettings"`
		UserEditableSettingsSchema map[string]UserEditableSettingSchema `json:"userEditableSettingsSchema"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Non-sensitive fields returned as-is
	if got.Server.Port != cfg.Server.Port || got.Server.Host != cfg.Server.Host {
		t.Fatalf("unexpected server settings: %+v", got.Server)
	}
	if len(got.Usenet) != 1 || got.Usenet[0].Username != "user" {
		t.Fatalf("unexpected usenet settings: %+v", got.Usenet)
	}
	// Credentials are always redacted — even for master accounts
	if got.Usenet[0].Password != "••••••••" {
		t.Fatalf("expected password to be redacted, got %q", got.Usenet[0].Password)
	}
	if len(got.UserEditableSettings) != 1 || got.UserEditableSettings[0] != "playback.preferredPlayer" {
		t.Fatalf("unexpected user-editable settings projection: %#v", got.UserEditableSettings)
	}
	if schema := got.UserEditableSettingsSchema["playback.preferredPlayer"]; schema.Type != "select" || schema.Label != "Preferred Player" {
		t.Fatalf("unexpected user-editable schema: %#v", schema)
	}
}

func TestSettingsHandler_PutSettings(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(mgr)

	payload := config.Settings{
		Server: config.ServerSettings{Host: "0.0.0.0", Port: 8888},
		Usenet: []config.UsenetSettings{
			{Name: "Test", Host: "news.example", Port: 443, SSL: false, Username: "alice", Password: "hunter2", Connections: 4, Enabled: true},
		},
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()

	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type %q", got)
	}

	var resp config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Server.Port != payload.Server.Port || len(resp.Usenet) != 1 || resp.Usenet[0].Host != payload.Usenet[0].Host {
		t.Fatalf("unexpected response payload: %+v", resp)
	}

	saved, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(saved.Usenet) != 1 || saved.Usenet[0].Username != payload.Usenet[0].Username || saved.Server.Port != payload.Server.Port {
		t.Fatalf("settings not persisted: %+v", saved)
	}
}

func TestSettingsHandlerPutSettingsRejectsInvalidHardwareAcceleration(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	handler := NewSettingsHandler(mgr)
	payload := config.DefaultSettings()
	payload.Transmux.HardwareAcceleration = "cuda"
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.PutSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hardware acceleration must be one of") {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func TestSettingsHandler_AlwaysShowProfileSelector(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "settings.json")
	mgr := config.NewManager(cfgPath)

	// Write raw JSON without alwaysShowProfileSelector to simulate an old config
	oldConfig := `{"server":{"host":"127.0.0.1","port":7777},"display":{"badgeVisibility":["watchProgress"],"watchStateIconStyle":"colored"}}`
	if err := os.WriteFile(cfgPath, []byte(oldConfig), 0644); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	// Load should backfill AlwaysShowProfileSelector to true
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Display.AlwaysShowProfileSelector {
		t.Fatal("expected AlwaysShowProfileSelector to default to true after backfill")
	}

	// Round-trip through PUT/GET: explicitly set to false
	handler := NewSettingsHandler(mgr)
	updated := loaded
	updated.Display.AlwaysShowProfileSelector = false

	buf, err := json.Marshal(updated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	handler.PutSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}

	// GET should return false (user's explicit choice preserved)
	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec = httptest.NewRecorder()
	handler.GetSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	var got config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Display.AlwaysShowProfileSelector {
		t.Fatal("expected AlwaysShowProfileSelector to be false after explicit PUT")
	}
}

func TestSettingsHandler_EnableTranslatedSubs_DefaultAndOverride(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "settings.json")
	mgr := config.NewManager(cfgPath)

	// Write raw JSON without subtitles.enableTranslatedSubs to simulate an old config.
	oldConfig := `{"server":{"host":"127.0.0.1","port":7777},"subtitles":{"openSubtitlesUsername":"test-user"}}`
	if err := os.WriteFile(cfgPath, []byte(oldConfig), 0644); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Subtitles.EnableTranslatedSubs {
		t.Fatal("expected EnableTranslatedSubs to default to true after backfill")
	}

	// Round-trip through PUT/GET: explicitly set to false.
	handler := NewSettingsHandler(mgr)
	updated := loaded
	updated.Subtitles.EnableTranslatedSubs = false

	buf, err := json.Marshal(updated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	handler.PutSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec = httptest.NewRecorder()
	handler.GetSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	var got config.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Subtitles.EnableTranslatedSubs {
		t.Fatal("expected EnableTranslatedSubs to be false after explicit PUT")
	}
}

func TestSettingsHandler_EPGAutoRefreshOnNewSource(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := config.NewManager(filepath.Join(tmpDir, "settings.json"))

	// Create initial settings with EPG disabled
	initialSettings := config.Settings{
		Server: config.ServerSettings{Host: "127.0.0.1", Port: 7777},
		Live: config.LiveSettings{
			EPG: config.EPGSettings{
				Enabled: false,
				Sources: []config.EPGSource{},
			},
		},
	}
	if err := mgr.Save(initialSettings); err != nil {
		t.Fatalf("save initial settings: %v", err)
	}

	handler := NewSettingsHandler(mgr)

	// Create a real EPG service with a temp directory
	epgService := epg.NewService(tmpDir, mgr)
	handler.SetEPGService(epgService)

	// Update settings to add EPG sources and enable EPG
	newSettings := config.Settings{
		Server: config.ServerSettings{Host: "127.0.0.1", Port: 7777},
		Live: config.LiveSettings{
			EPG: config.EPGSettings{
				Enabled:              true,
				RefreshIntervalHours: 12,
				Sources: []config.EPGSource{
					{
						ID:       "test-source-1",
						Name:     "Test EPG Source",
						Type:     "xmltv",
						URL:      "http://example.com/epg.xml",
						Enabled:  true,
						Priority: 1,
					},
				},
			},
		},
	}

	buf, err := json.Marshal(newSettings)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()

	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	// Verify settings were saved with the new source
	saved, err := mgr.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !saved.Live.EPG.Enabled {
		t.Fatal("EPG should be enabled")
	}
	if len(saved.Live.EPG.Sources) != 1 {
		t.Fatalf("expected 1 EPG source, got %d", len(saved.Live.EPG.Sources))
	}
	if saved.Live.EPG.Sources[0].ID != "test-source-1" {
		t.Fatalf("unexpected source ID: %s", saved.Live.EPG.Sources[0].ID)
	}
	var epgTask *config.ScheduledTask
	for i := range saved.ScheduledTasks.Tasks {
		if saved.ScheduledTasks.Tasks[i].Type == config.ScheduledTaskTypeEPGRefresh {
			epgTask = &saved.ScheduledTasks.Tasks[i]
			break
		}
	}
	if epgTask == nil {
		t.Fatal("expected EPG refresh task to be auto-created")
	}
	if !epgTask.Enabled {
		t.Fatal("expected EPG refresh task to be enabled")
	}

	// Give the background goroutine a moment to start (it will fail because the URL is fake, but that's okay)
	time.Sleep(100 * time.Millisecond)
}

func TestSettingsHandler_EnsureEPGTaskForGuide_SourceEPGConfig(t *testing.T) {
	enabled := true
	settings := config.Settings{
		Live: config.LiveSettings{
			EPG: config.EPGSettings{Enabled: false},
			Sources: []config.LivePlaylistSource{
				{
					ID:          "source-1",
					Name:        "Source 1",
					Enabled:     &enabled,
					PlaylistURL: "http://example.com/live.m3u",
					EPG: config.EPGSettings{
						Enabled:  true,
						XmltvUrl: "http://example.com/epg.xml",
					},
				},
			},
		},
	}

	handler := NewSettingsHandler(nil)
	if !handler.EnsureEPGTaskForGuide(&settings, "test startup") {
		t.Fatal("expected EPG task helper to report a change")
	}
	if len(settings.ScheduledTasks.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(settings.ScheduledTasks.Tasks))
	}
	task := settings.ScheduledTasks.Tasks[0]
	if task.Type != config.ScheduledTaskTypeEPGRefresh {
		t.Fatalf("expected EPG refresh task, got %s", task.Type)
	}
	if !task.Enabled {
		t.Fatal("expected task to be enabled for source-level EPG config")
	}
}

func TestSettingsHandler_ShowParsedBadges_ClearsPrequeue(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := config.NewManager(filepath.Join(tmpDir, "settings.json"))

	// Save initial settings with ShowParsedBadges = false using a full payload so
	// unrelated defaults don't look like changes on subsequent saves.
	initial := config.DefaultSettings()
	initial.Server = config.ServerSettings{Host: "127.0.0.1", Port: 7777}
	initial.Display.ShowParsedBadges = false
	if err := mgr.Save(initial); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	handler := NewSettingsHandler(mgr)
	mock := &mockPrequeueClearer{}
	searchCache := &mockSearchCacheClearer{}
	handler.SetPrequeueStore(mock)
	handler.SetSearchCacheClearer(searchCache)

	// Toggle ShowParsedBadges to true — should clear prequeue
	updated := initial
	updated.Display.ShowParsedBadges = true
	buf, _ := json.Marshal(updated)

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	if mock.called.Load() != 1 {
		t.Fatalf("expected DeleteAll to be called once, got %d", mock.called.Load())
	}
	if searchCache.called.Load() != 1 {
		t.Fatalf("expected ClearSearchCache to be called once, got %d", searchCache.called.Load())
	}

	// Save again with the same value — should NOT clear prequeue
	buf, _ = json.Marshal(updated)
	req = httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec = httptest.NewRecorder()
	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	if mock.called.Load() != 1 {
		t.Fatalf("expected DeleteAll call count to remain 1, got %d", mock.called.Load())
	}
	if searchCache.called.Load() != 1 {
		t.Fatalf("expected ClearSearchCache call count to remain 1, got %d", searchCache.called.Load())
	}

	// Toggle back to false — should clear prequeue again
	updated.Display.ShowParsedBadges = false
	buf, _ = json.Marshal(updated)
	req = httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec = httptest.NewRecorder()
	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	if mock.called.Load() != 2 {
		t.Fatalf("expected DeleteAll to be called twice, got %d", mock.called.Load())
	}
	if searchCache.called.Load() != 2 {
		t.Fatalf("expected ClearSearchCache to be called twice, got %d", searchCache.called.Load())
	}
}

func TestSettingsHandler_ShowParsedBadges_NoPrequeueStore(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := config.NewManager(filepath.Join(tmpDir, "settings.json"))

	initial := config.DefaultSettings()
	initial.Server = config.ServerSettings{Host: "127.0.0.1", Port: 7777}
	initial.Display.ShowParsedBadges = false
	if err := mgr.Save(initial); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	handler := NewSettingsHandler(mgr)
	// No prequeue store set — should not panic

	updated := initial
	updated.Display.ShowParsedBadges = true
	buf, _ := json.Marshal(updated)

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	handler.PutSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
}

// A settings-page save sends back the settings it loaded: the quality preset
// configuration (edited on its own page) and its sync task survive it.
func TestSettingsPutKeepsQualityPresets(t *testing.T) {
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	stored, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored.QualityPresets.MoviePreset = "p-movie"
	stored.QualityPresets.PickerPresets = []string{"p-movie"}
	stored.QualityPresets.AllowRequestOverride = true
	EnsureQualityPresetSyncTask(&stored)
	if err := mgr.Save(stored); err != nil {
		t.Fatal(err)
	}

	// The page was loaded before any of that.
	payload := config.Settings{Server: config.ServerSettings{Host: "0.0.0.0", Port: 8888}}
	buf, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	NewSettingsHandler(mgr).PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(buf)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	saved, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	q := saved.QualityPresets
	if q.MoviePreset != "p-movie" || len(q.PickerPresets) != 1 || !q.AllowRequestOverride {
		t.Errorf("quality presets = %+v, want the stored ones", q)
	}
	found := false
	for _, task := range saved.ScheduledTasks.Tasks {
		found = found || task.Type == config.ScheduledTaskTypeQualityPresetSync
	}
	if !found {
		t.Error("the quality preset sync task was dropped")
	}
}

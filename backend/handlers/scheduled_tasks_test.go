package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/models"
	"novastream/services/scheduler"
)

type fakeScheduledTaskUsersProvider struct {
	users map[string]models.User
}

func (f *fakeScheduledTaskUsersProvider) Exists(id string) bool {
	_, ok := f.users[id]
	return ok
}

func (f *fakeScheduledTaskUsersProvider) ListAll() []models.User {
	result := make([]models.User, 0, len(f.users))
	for _, user := range f.users {
		result = append(result, user)
	}
	return result
}

// newTestScheduledTasksHandler creates a handler with a real config manager
// backed by a temp file and a minimal scheduler service.
func newTestScheduledTasksHandler(t *testing.T) *ScheduledTasksHandler {
	t.Helper()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{}); err != nil {
		t.Fatalf("save initial settings: %v", err)
	}
	svc := scheduler.NewService(mgr, nil, nil, nil)
	users := &fakeScheduledTaskUsersProvider{
		users: map[string]models.User{
			"prof-1": {ID: "prof-1", Name: models.DefaultUserName},
		},
	}
	return NewScheduledTasksHandler(mgr, svc, users)
}

// postCreateTask is a helper that sends a POST to CreateTask and returns the recorder.
func postCreateTask(t *testing.T, h *ScheduledTasksHandler, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/api/scheduled-tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.CreateTask(rec, req)
	return rec
}

func TestCreateTask_OnceFrequency(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	body := map[string]interface{}{
		"type":      string(config.ScheduledTaskTypeBackup),
		"name":      "One-time backup",
		"frequency": string(config.ScheduledTaskFrequencyOnce),
		"enabled":   true,
	}

	rec := postCreateTask(t, h, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}

	taskRaw, ok := resp["task"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected task object in response")
	}
	if taskRaw["frequency"] != string(config.ScheduledTaskFrequencyOnce) {
		t.Errorf("expected frequency=%q, got %v", config.ScheduledTaskFrequencyOnce, taskRaw["frequency"])
	}
	if taskRaw["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", taskRaw["enabled"])
	}
	if taskRaw["id"] == nil || taskRaw["id"] == "" {
		t.Error("expected non-empty task ID")
	}
}

func TestCreateTask_PlexHistorySyncValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	tests := []struct {
		name   string
		config map[string]string
		errMsg string
	}{
		{
			name:   "nil config",
			config: nil,
			errMsg: "Plex history sync requires plexAccountId and profileId in config",
		},
		{
			name:   "missing plexAccountId",
			config: map[string]string{"profileId": "prof-1"},
			errMsg: "Plex history sync requires plexAccountId and profileId in config",
		},
		{
			name:   "missing profileId",
			config: map[string]string{"plexAccountId": "acct-1"},
			errMsg: "Plex history sync requires plexAccountId and profileId in config",
		},
		{
			name:   "both empty",
			config: map[string]string{"plexAccountId": "", "profileId": ""},
			errMsg: "Plex history sync requires plexAccountId and profileId in config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]interface{}{
				"type":    string(config.ScheduledTaskTypePlexHistorySync),
				"name":    "Plex history sync",
				"enabled": true,
			}
			if tc.config != nil {
				body["config"] = tc.config
			}

			rec := postCreateTask(t, h, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}

			var resp map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["error"] != tc.errMsg {
				t.Errorf("expected error %q, got %q", tc.errMsg, resp["error"])
			}
		})
	}

	// Valid config should succeed
	t.Run("valid config", func(t *testing.T) {
		body := map[string]interface{}{
			"type":    string(config.ScheduledTaskTypePlexHistorySync),
			"name":    "Plex history sync",
			"enabled": true,
			"config": map[string]string{
				"plexAccountId": "acct-1",
				"profileId":     "prof-1",
			},
		}
		rec := postCreateTask(t, h, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestCreateTask_LocalMediaScanAllLibrariesValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	body := map[string]interface{}{
		"type":    string(config.ScheduledTaskTypeLocalMediaScan),
		"name":    "Scan all libraries",
		"enabled": true,
		"config": map[string]string{
			"libraryId": config.ScheduledTaskLocalMediaAllLibraries,
		},
	}

	rec := postCreateTask(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTask_JellyfinFavoritesSyncValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	tests := []struct {
		name   string
		config map[string]string
		errMsg string
	}{
		{
			name:   "nil config",
			config: nil,
			errMsg: "Jellyfin favorites sync requires jellyfinAccountId and profileId in config",
		},
		{
			name:   "missing jellyfinAccountId",
			config: map[string]string{"profileId": "prof-1"},
			errMsg: "Jellyfin favorites sync requires jellyfinAccountId and profileId in config",
		},
		{
			name:   "missing profileId",
			config: map[string]string{"jellyfinAccountId": "acct-1"},
			errMsg: "Jellyfin favorites sync requires jellyfinAccountId and profileId in config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]interface{}{
				"type":    string(config.ScheduledTaskTypeJellyfinFavoritesSync),
				"name":    "Jellyfin favorites sync",
				"enabled": true,
			}
			if tc.config != nil {
				body["config"] = tc.config
			}

			rec := postCreateTask(t, h, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}

			var resp map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["error"] != tc.errMsg {
				t.Errorf("expected error %q, got %q", tc.errMsg, resp["error"])
			}
		})
	}

	// Valid config should succeed
	t.Run("valid config", func(t *testing.T) {
		body := map[string]interface{}{
			"type":    string(config.ScheduledTaskTypeJellyfinFavoritesSync),
			"name":    "Jellyfin favorites sync",
			"enabled": true,
			"config": map[string]string{
				"jellyfinAccountId": "acct-1",
				"profileId":         "prof-1",
			},
		}
		rec := postCreateTask(t, h, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestCreateTask_JellyfinHistorySyncValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	tests := []struct {
		name   string
		config map[string]string
		errMsg string
	}{
		{
			name:   "nil config",
			config: nil,
			errMsg: "Jellyfin history sync requires jellyfinAccountId and profileId in config",
		},
		{
			name:   "missing jellyfinAccountId",
			config: map[string]string{"profileId": "prof-1"},
			errMsg: "Jellyfin history sync requires jellyfinAccountId and profileId in config",
		},
		{
			name:   "missing profileId",
			config: map[string]string{"jellyfinAccountId": "acct-1"},
			errMsg: "Jellyfin history sync requires jellyfinAccountId and profileId in config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]interface{}{
				"type":    string(config.ScheduledTaskTypeJellyfinHistorySync),
				"name":    "Jellyfin history sync",
				"enabled": true,
			}
			if tc.config != nil {
				body["config"] = tc.config
			}

			rec := postCreateTask(t, h, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}

			var resp map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["error"] != tc.errMsg {
				t.Errorf("expected error %q, got %q", tc.errMsg, resp["error"])
			}
		})
	}

	// Valid config should succeed
	t.Run("valid config", func(t *testing.T) {
		body := map[string]interface{}{
			"type":    string(config.ScheduledTaskTypeJellyfinHistorySync),
			"name":    "Jellyfin history sync",
			"enabled": true,
			"config": map[string]string{
				"jellyfinAccountId": "acct-1",
				"profileId":         "prof-1",
			},
		}
		rec := postCreateTask(t, h, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestCreateTask_InvalidProfileIDValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	body := map[string]interface{}{
		"type":    string(config.ScheduledTaskTypeTraktHistorySync),
		"name":    "Trakt history sync",
		"enabled": true,
		"config": map[string]string{
			"traktAccountId": "acct-1",
			"profileId":      "missing-profile",
		},
	}

	rec := postCreateTask(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["error"]; got != `profileId "missing-profile" does not exist` {
		t.Fatalf("expected invalid profile error, got %v", got)
	}
}

func TestCreateTask_SimklHistorySyncValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	body := map[string]interface{}{
		"type":    string(config.ScheduledTaskTypeSimklHistorySync),
		"name":    "Simkl history sync",
		"enabled": true,
		"config": map[string]string{
			"simklAccountId": "simkl-1",
			"profileId":      "prof-1",
		},
	}
	rec := postCreateTask(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body["config"] = map[string]string{"profileId": "prof-1"}
	rec = postCreateTask(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	body["config"] = map[string]string{"simklAccountId": "simkl-1", "profileId": "prof-1"}
	body["frequency"] = string(config.ScheduledTaskFrequency5Min)
	rec = postCreateTask(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for rapid Simkl schedule, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateTask_InvalidProfileIDValidation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	settings, err := h.configManager.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	settings.ScheduledTasks.Tasks = append(settings.ScheduledTasks.Tasks, config.ScheduledTask{
		ID:         "task-1",
		Type:       config.ScheduledTaskTypeTraktHistorySync,
		Name:       "Trakt history sync",
		Frequency:  config.ScheduledTaskFrequency12Hours,
		Config:     map[string]string{"traktAccountId": "acct-1", "profileId": "prof-1"},
		Enabled:    true,
		LastStatus: config.ScheduledTaskStatusPending,
		CreatedAt:  time.Now().UTC(),
	})
	if err := h.configManager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	body := map[string]interface{}{
		"config": map[string]string{
			"traktAccountId": "acct-1",
			"profileId":      "missing-profile",
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/admin/api/scheduled-tasks/task-1", bytes.NewReader(b))
	req = mux.SetURLVars(req, map[string]string{"taskID": "task-1"})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateTask(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["error"]; got != `profileId "missing-profile" does not exist` {
		t.Fatalf("expected invalid profile error, got %v", got)
	}
}

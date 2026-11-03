package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/prewarm"
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := svc.Stop(ctx); err != nil {
			t.Errorf("stop scheduler service: %v", err)
		}
	})
	users := &fakeScheduledTaskUsersProvider{
		users: map[string]models.User{
			"prof-1": {ID: "prof-1", AccountID: "acct-1", Name: models.DefaultUserName},
			"prof-2": {ID: "prof-2", AccountID: "acct-2", Name: "Other"},
		},
	}
	return NewScheduledTasksHandler(mgr, svc, users)
}

func withScheduledTaskAccount(req *http.Request, accountID string) *http.Request {
	ctx := context.WithValue(req.Context(), auth.ContextKeyAccountID, accountID)
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, false)
	return req.WithContext(ctx)
}

func TestScheduledTasksAreScopedToOwnedProfiles(t *testing.T) {
	h := newTestScheduledTasksHandler(t)
	settings, err := h.configManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.ScheduledTasks.Tasks = []config.ScheduledTask{
		{ID: "owned", Type: config.ScheduledTaskTypePlexHistorySync, Config: map[string]string{"profileId": "prof-1", "plexAccountId": "plex-owned"}},
		{ID: "foreign", Type: config.ScheduledTaskTypePlexHistorySync, Config: map[string]string{"profileId": "prof-2", "plexAccountId": "plex-foreign"}},
		{ID: "server", Type: config.ScheduledTaskTypeBackup},
	}
	settings.Plex.Accounts = []config.PlexAccount{
		{ID: "plex-owned", OwnerAccountID: "acct-1"},
		{ID: "plex-foreign", OwnerAccountID: "acct-2"},
	}
	if err := h.configManager.Save(settings); err != nil {
		t.Fatal(err)
	}

	req := withScheduledTaskAccount(httptest.NewRequest(http.MethodGet, "/account/api/scheduled-tasks", nil), "acct-1")
	rec := httptest.NewRecorder()
	h.ListTasks(rec, req)
	var response struct {
		Tasks []config.ScheduledTask `json:"tasks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].ID != "owned" {
		t.Fatalf("got tasks %#v, want only owned task", response.Tasks)
	}

	deleteReq := withScheduledTaskAccount(httptest.NewRequest(http.MethodDelete, "/account/api/scheduled-tasks/foreign", nil), "acct-1")
	deleteReq = mux.SetURLVars(deleteReq, map[string]string{"taskID": "foreign"})
	deleteRec := httptest.NewRecorder()
	h.DeleteTask(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("foreign delete status = %d, want 404", deleteRec.Code)
	}
}

func TestNonAdminCannotCreateServerOrForeignAutomation(t *testing.T) {
	h := newTestScheduledTasksHandler(t)
	settings, err := h.configManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.Plex.Accounts = []config.PlexAccount{
		{ID: "plex", OwnerAccountID: "acct-1"},
		{ID: "foreign-plex", OwnerAccountID: "acct-2"},
	}
	if err := h.configManager.Save(settings); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		config map[string]string
	}{
		{name: "server automation", config: nil},
		{name: "foreign profile", config: map[string]string{"plexAccountId": "plex", "profileId": "prof-2"}},
		{name: "foreign integration", config: map[string]string{"plexAccountId": "foreign-plex", "profileId": "prof-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{
				"type": config.ScheduledTaskTypeBackup, "name": test.name, "config": test.config,
			})
			if test.config != nil {
				body, _ = json.Marshal(map[string]interface{}{
					"type": config.ScheduledTaskTypePlexHistorySync, "name": test.name, "config": test.config,
				})
			}
			req := withScheduledTaskAccount(httptest.NewRequest(http.MethodPost, "/account/api/scheduled-tasks", bytes.NewReader(body)), "acct-1")
			rec := httptest.NewRecorder()
			h.CreateTask(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestScheduledTasksSupportAllOwnedIntegrationTypes(t *testing.T) {
	h := newTestScheduledTasksHandler(t)
	settings, err := h.configManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.Trakt.Accounts = []config.TraktAccount{{ID: "trakt", OwnerAccountID: "acct-1"}}
	settings.Simkl.Accounts = []config.SimklAccount{{ID: "simkl", OwnerAccountID: "acct-1"}}
	settings.MDBList.Accounts = []config.MDBListAccount{{ID: "mdblist", OwnerAccountID: "acct-1"}}
	settings.Jellyfin.Accounts = []config.JellyfinAccount{{ID: "jellyfin", OwnerAccountID: "acct-1"}}
	settings.ScheduledTasks.Tasks = []config.ScheduledTask{
		{ID: "trakt", Type: config.ScheduledTaskTypeTraktHistorySync, Config: map[string]string{"profileId": "prof-1", "traktAccountId": "trakt"}},
		{ID: "simkl", Type: config.ScheduledTaskTypeSimklHistorySync, Config: map[string]string{"profileId": "prof-1", "simklAccountId": "simkl"}},
		{ID: "mdblist", Type: config.ScheduledTaskTypeMDBListHistorySync, Config: map[string]string{"profileId": "prof-1", "mdblistAccountId": "mdblist"}},
		{ID: "jellyfin", Type: config.ScheduledTaskTypeJellyfinHistorySync, Config: map[string]string{"profileId": "prof-1", "jellyfinAccountId": "jellyfin"}},
	}
	if err := h.configManager.Save(settings); err != nil {
		t.Fatal(err)
	}
	req := withScheduledTaskAccount(httptest.NewRequest(http.MethodGet, "/account/api/scheduled-tasks", nil), "acct-1")
	rec := httptest.NewRecorder()
	h.ListTasks(rec, req)
	var response struct {
		Tasks []config.ScheduledTask `json:"tasks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tasks) != 4 {
		t.Fatalf("got %d owned integration tasks, want 4: %+v", len(response.Tasks), response.Tasks)
	}
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

func TestValidateScheduledTaskProfileIDRejectsLegacyDefaultFallback(t *testing.T) {
	users := &fakeScheduledTaskUsersProvider{
		users: map[string]models.User{
			"mom-profile": {ID: "mom-profile", Name: models.DefaultUserName},
		},
	}

	err := validateScheduledTaskProfileID(models.DefaultUserID, users)
	if err == nil {
		t.Fatal("expected missing legacy default profile to be rejected")
	}
}

func TestValidateScheduledTaskConfigNormalizesPrewarmShelfSelections(t *testing.T) {
	taskConfig := map[string]string{
		prewarm.PrewarmShelfSelectionsConfigKey: `[{"id":"continue-watching","playedWithinDays":21},{"id":"watchlist"}]`,
	}
	if err := validateScheduledTaskConfig(config.ScheduledTaskTypePrewarm, taskConfig, nil); err != nil {
		t.Fatalf("validateScheduledTaskConfig: %v", err)
	}
	selections, err := prewarm.ParseShelfSelections(taskConfig)
	if err != nil {
		t.Fatalf("ParseShelfSelections normalized config: %v", err)
	}
	if len(selections) != 2 || selections[0].PlayedWithinDays != 21 || selections[1].ItemScope != prewarm.PrewarmItemScopeAll {
		t.Fatalf("unexpected selections: %+v", selections)
	}
	if taskConfig["stableReresolveDays"] != "7" {
		t.Fatalf("stableReresolveDays=%q, want 7", taskConfig["stableReresolveDays"])
	}
}

func TestValidateScheduledTaskConfigRejectsEmptyPrewarmShelfSelections(t *testing.T) {
	err := validateScheduledTaskConfig(config.ScheduledTaskTypePrewarm, map[string]string{
		prewarm.PrewarmShelfSelectionsConfigKey: `[]`,
	}, nil)
	if err == nil {
		t.Fatal("expected empty shelf selection to be rejected")
	}
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

	t.Run("selected server requires ID and URL pair", func(t *testing.T) {
		body := map[string]interface{}{
			"type":    string(config.ScheduledTaskTypePlexHistorySync),
			"name":    "Plex history sync",
			"enabled": true,
			"config": map[string]string{
				"plexAccountId": "acct-1",
				"profileId":     "prof-1",
				"plexServerId":  "server-1",
			},
		}
		rec := postCreateTask(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "requires both plexServerId and plexServerUrl") {
			t.Fatalf("unexpected response: %s", rec.Body.String())
		}
	})
}

func TestCreateTask_PrewarmStableReresolveDays(t *testing.T) {
	h := newTestScheduledTasksHandler(t)

	tests := []struct {
		name       string
		config     map[string]string
		wantStatus int
		wantDays   string
		wantErr    string
	}{
		{
			name:       "default",
			config:     nil,
			wantStatus: http.StatusOK,
			wantDays:   "7",
		},
		{
			name:       "custom",
			config:     map[string]string{"stableReresolveDays": "30"},
			wantStatus: http.StatusOK,
			wantDays:   "30",
		},
		{
			name:       "too low",
			config:     map[string]string{"stableReresolveDays": "0"},
			wantStatus: http.StatusBadRequest,
			wantErr:    "Pre-warm stable re-resolve days must be between 1 and 30",
		},
		{
			name:       "not a number",
			config:     map[string]string{"stableReresolveDays": "daily"},
			wantStatus: http.StatusBadRequest,
			wantErr:    "Pre-warm stable re-resolve days must be a whole number between 1 and 30",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]interface{}{
				"type":    string(config.ScheduledTaskTypePrewarm),
				"name":    "Prewarm",
				"enabled": true,
			}
			if tc.config != nil {
				body["config"] = tc.config
			}

			rec := postCreateTask(t, h, body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}

			var resp map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if tc.wantErr != "" {
				if resp["error"] != tc.wantErr {
					t.Fatalf("expected error %q, got %q", tc.wantErr, resp["error"])
				}
				return
			}

			taskRaw, ok := resp["task"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected task object")
			}
			cfg, ok := taskRaw["config"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected task config")
			}
			if cfg["stableReresolveDays"] != tc.wantDays {
				t.Fatalf("stableReresolveDays = %v, want %s", cfg["stableReresolveDays"], tc.wantDays)
			}
		})
	}
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

	body["config"] = map[string]string{
		"simklAccountId": "simkl-1",
		"profileId":      "prof-1",
		"syncDirection":  "sideways",
	}
	rec = postCreateTask(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid direction, got %d: %s", rec.Code, rec.Body.String())
	}

	body["config"] = map[string]string{
		"simklAccountId": "simkl-1",
		"profileId":      "prof-1",
		"syncDirection":  "bidirectional",
	}
	body["frequency"] = string(config.ScheduledTaskFrequencyHourly)
	rec = postCreateTask(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for bidirectional, got %d: %s", rec.Code, rec.Body.String())
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

func TestValidateScrobHistorySyncConfig(t *testing.T) {
	taskConfig := map[string]string{"scrobAccountId": "scrob-1", "profileId": "profile-1"}
	if err := validateScheduledTaskConfig(config.ScheduledTaskTypeScrobHistorySync, taskConfig, nil); err != nil {
		t.Fatalf("validate Scrob task: %v", err)
	}
	if got := taskConfig["syncDirection"]; got != "scrob_to_local" {
		t.Fatalf("default direction = %q", got)
	}
	taskConfig["syncDirection"] = "sideways"
	if err := validateScheduledTaskConfig(config.ScheduledTaskTypeScrobHistorySync, taskConfig, nil); err == nil {
		t.Fatal("expected invalid direction error")
	}
}

func TestRunTaskNowRejectsInvalidFullSync(t *testing.T) {
	for _, value := range []string{"invalid", "true"} {
		t.Run(value, func(t *testing.T) {
			h := newTestScheduledTasksHandler(t)
			settings, err := h.configManager.Load()
			if err != nil {
				t.Fatal(err)
			}
			settings.ScheduledTasks.Tasks = []config.ScheduledTask{{ID: "backup", Type: config.ScheduledTaskTypeBackup}}
			if err := h.configManager.Save(settings); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/admin/api/scheduled-tasks/backup/run?fullSync="+value, nil)
			req = mux.SetURLVars(req, map[string]string{"taskID": "backup"})
			rec := httptest.NewRecorder()
			h.RunTaskNow(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestValidateScheduledTaskConfigRejectsUnsupportedExportDirections(t *testing.T) {
	cases := []struct {
		name     string
		taskType config.ScheduledTaskType
		cfg      map[string]string
		wantErr  bool
	}{
		{"trakt collection bidirectional", config.ScheduledTaskTypeTraktListSync, map[string]string{"traktAccountId": "a", "profileId": "p", "listType": "collection", "syncDirection": "bidirectional"}, true},
		{"trakt custom export", config.ScheduledTaskTypeTraktListSync, map[string]string{"traktAccountId": "a", "profileId": "p", "listType": "custom", "customListId": "1", "syncDirection": "target_to_source"}, true},
		{"trakt collection import", config.ScheduledTaskTypeTraktListSync, map[string]string{"traktAccountId": "a", "profileId": "p", "listType": "collection", "syncDirection": "source_to_target"}, false},
		{"trakt watchlist bidirectional", config.ScheduledTaskTypeTraktListSync, map[string]string{"traktAccountId": "a", "profileId": "p", "listType": "watchlist", "syncDirection": "bidirectional"}, false},
		{"mdblist watchlist export", config.ScheduledTaskTypeMDBListWatchlistSync, map[string]string{"mdblistAccountId": "a", "profileId": "p", "syncDirection": "target_to_source"}, false},
		{"mdblist watchlist bidirectional", config.ScheduledTaskTypeMDBListWatchlistSync, map[string]string{"mdblistAccountId": "a", "profileId": "p", "syncDirection": "bidirectional"}, false},
		{"mdblist watchlist unknown direction", config.ScheduledTaskTypeMDBListWatchlistSync, map[string]string{"mdblistAccountId": "a", "profileId": "p", "syncDirection": "mdblist_to_local"}, true},
		{"mdblist watchlist import", config.ScheduledTaskTypeMDBListWatchlistSync, map[string]string{"mdblistAccountId": "a", "profileId": "p", "syncDirection": "source_to_target"}, false},
	}
	for _, tc := range cases {
		err := validateScheduledTaskConfig(tc.taskType, tc.cfg, nil)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestValidateScheduledTaskConfigSimklWatchlistSync(t *testing.T) {
	valid := map[string]string{"simklAccountId": "acc", "profileId": "p", "syncDirection": "bidirectional"}
	if err := validateScheduledTaskConfig(config.ScheduledTaskTypeSimklWatchlistSync, valid, nil); err != nil {
		t.Fatalf("bidirectional rejected: %v", err)
	}
	if err := validateScheduledTaskConfig(config.ScheduledTaskTypeSimklWatchlistSync, map[string]string{"profileId": "p"}, nil); err == nil {
		t.Fatal("missing simklAccountId accepted")
	}
	bad := map[string]string{"simklAccountId": "acc", "profileId": "p", "syncDirection": "simkl_to_local"}
	if err := validateScheduledTaskConfig(config.ScheduledTaskTypeSimklWatchlistSync, bad, nil); err == nil {
		t.Fatal("history-style direction accepted for watchlist sync")
	}
}

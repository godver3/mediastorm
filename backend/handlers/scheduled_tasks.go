package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/plex"
	"novastream/services/prewarm"
	"novastream/services/scheduler"
)

// ScheduledTasksHandler handles scheduled tasks API endpoints
type ScheduledTasksHandler struct {
	configManager    *config.Manager
	schedulerService *scheduler.Service
	usersService     scheduledTaskUsersProvider
}

func (h *ScheduledTasksHandler) canAccessTask(r *http.Request, task config.ScheduledTask) bool {
	if auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	// These handlers are always mounted behind authentication. Treat an absent
	// auth context as an internal/direct invocation (primarily package tests).
	if accountID == "" {
		return true
	}
	profileID := strings.TrimSpace(task.Config["profileId"])
	if profileID == "" || h.usersService == nil {
		return false
	}
	for _, user := range h.usersService.ListAll() {
		if user.ID == profileID {
			if user.AccountID != accountID {
				return false
			}
			return h.taskIntegrationBelongsToAccount(task, accountID)
		}
	}
	return false
}

func (h *ScheduledTasksHandler) taskIntegrationBelongsToAccount(task config.ScheduledTask, accountID string) bool {
	settings, err := h.configManager.Load()
	if err != nil {
		return false
	}
	switch task.Type {
	case config.ScheduledTaskTypePlexWatchlistSync, config.ScheduledTaskTypePlexHistorySync:
		integrationID := strings.TrimSpace(task.Config["plexAccountId"])
		for _, account := range settings.Plex.Accounts {
			if account.ID == integrationID {
				return account.OwnerAccountID == accountID
			}
		}
	case config.ScheduledTaskTypeTraktListSync, config.ScheduledTaskTypeTraktHistorySync:
		integrationID := strings.TrimSpace(task.Config["traktAccountId"])
		for _, account := range settings.Trakt.Accounts {
			if account.ID == integrationID {
				return account.OwnerAccountID == accountID
			}
		}
	case config.ScheduledTaskTypeSimklHistorySync, config.ScheduledTaskTypeSimklWatchlistSync:
		integrationID := strings.TrimSpace(task.Config["simklAccountId"])
		for _, account := range settings.Simkl.Accounts {
			if account.ID == integrationID {
				return account.OwnerAccountID == accountID
			}
		}
	case config.ScheduledTaskTypeScrobHistorySync:
		integrationID := strings.TrimSpace(task.Config["scrobAccountId"])
		for _, account := range settings.Scrob.Accounts {
			if account.ID == integrationID {
				return account.OwnerAccountID == accountID
			}
		}
	case config.ScheduledTaskTypeMDBListWatchlistSync, config.ScheduledTaskTypeMDBListHistorySync:
		integrationID := strings.TrimSpace(task.Config["mdblistAccountId"])
		for _, account := range settings.MDBList.Accounts {
			if account.ID == integrationID {
				return account.OwnerAccountID == accountID
			}
		}
	case config.ScheduledTaskTypeJellyfinFavoritesSync, config.ScheduledTaskTypeJellyfinHistorySync:
		integrationID := strings.TrimSpace(task.Config["jellyfinAccountId"])
		for _, account := range settings.Jellyfin.Accounts {
			if account.ID == integrationID {
				return account.OwnerAccountID == accountID
			}
		}
	}
	return false
}

func (h *ScheduledTasksHandler) findAccessibleTask(r *http.Request, taskID string) (config.ScheduledTask, bool) {
	settings, err := h.configManager.Load()
	if err != nil {
		return config.ScheduledTask{}, false
	}
	for _, task := range settings.ScheduledTasks.Tasks {
		if task.ID == taskID && h.canAccessTask(r, task) {
			return task, true
		}
	}
	return config.ScheduledTask{}, false
}

func writeTaskNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "Task not found"})
}

type scheduledTaskUsersProvider interface {
	Exists(id string) bool
	ListAll() []models.User
}

// NewScheduledTasksHandler creates a new scheduled tasks handler
func NewScheduledTasksHandler(configManager *config.Manager, schedulerService *scheduler.Service, usersService scheduledTaskUsersProvider) *ScheduledTasksHandler {
	return &ScheduledTasksHandler{
		configManager:    configManager,
		schedulerService: schedulerService,
		usersService:     usersService,
	}
}

func validateScheduledTaskConfig(taskType config.ScheduledTaskType, taskConfig map[string]string, usersService scheduledTaskUsersProvider) error {
	requireProfile := func(accountKey, message string) error {
		if taskConfig == nil || taskConfig[accountKey] == "" || taskConfig["profileId"] == "" {
			return errors.New(message)
		}
		return validateScheduledTaskProfileID(taskConfig["profileId"], usersService)
	}

	switch taskType {
	case config.ScheduledTaskTypePrewarm:
		if taskConfig == nil {
			taskConfig = map[string]string{}
		}
		days := 7
		if val := strings.TrimSpace(taskConfig["stableReresolveDays"]); val != "" {
			parsed, err := strconv.Atoi(val)
			if err != nil {
				return errors.New("Pre-warm stable re-resolve days must be a whole number between 1 and 30")
			}
			days = parsed
		}
		if days < 1 || days > 30 {
			return errors.New("Pre-warm stable re-resolve days must be between 1 and 30")
		}
		taskConfig["stableReresolveDays"] = strconv.Itoa(days)
		selections, err := prewarm.ParseShelfSelections(taskConfig)
		if err != nil {
			return err
		}
		encodedSelections, err := json.Marshal(selections)
		if err != nil {
			return errors.New("failed to encode pre-warm shelf selections")
		}
		taskConfig[prewarm.PrewarmShelfSelectionsConfigKey] = string(encodedSelections)
	case config.ScheduledTaskTypePlexWatchlistSync:
		return requireProfile("plexAccountId", "Plex watchlist sync requires plexAccountId and profileId in config")
	case config.ScheduledTaskTypeTraktListSync:
		if err := requireProfile("traktAccountId", "Trakt list sync requires traktAccountId and profileId in config"); err != nil {
			return err
		}
		listType := taskConfig["listType"]
		if listType == "" {
			taskConfig["listType"] = "watchlist"
			return nil
		}
		if listType != "watchlist" && listType != "collection" && listType != "favorites" && listType != "custom" {
			return fmt.Errorf("Invalid list type. Must be watchlist, collection, favorites, or custom")
		}
		if listType == "custom" && taskConfig["customListId"] == "" {
			return fmt.Errorf("Custom list sync requires customListId in config")
		}
		// Trakt only exposes writes for the watchlist.
		if listType != "watchlist" && taskConfig["syncDirection"] != "" && taskConfig["syncDirection"] != "source_to_target" {
			return fmt.Errorf("Trakt %s lists can only be imported (Trakt → mediastorm)", listType)
		}
	case config.ScheduledTaskTypePlexHistorySync:
		if err := requireProfile("plexAccountId", "Plex history sync requires plexAccountId and profileId in config"); err != nil {
			return err
		}
		serverID := strings.TrimSpace(taskConfig["plexServerId"])
		serverURL := strings.TrimSpace(taskConfig["plexServerUrl"])
		if (serverID == "") != (serverURL == "") {
			return errors.New("Plex history sync requires both plexServerId and plexServerUrl when selecting a server")
		}
		if serverURL != "" {
			normalized, err := plex.NormalizeServerURL(serverURL)
			if err != nil {
				return err
			}
			serverURL = normalized
		}
		taskConfig["plexServerId"] = serverID
		taskConfig["plexServerUrl"] = serverURL
	case config.ScheduledTaskTypeJellyfinFavoritesSync:
		return requireProfile("jellyfinAccountId", "Jellyfin favorites sync requires jellyfinAccountId and profileId in config")
	case config.ScheduledTaskTypeJellyfinHistorySync:
		return requireProfile("jellyfinAccountId", "Jellyfin history sync requires jellyfinAccountId and profileId in config")
	case config.ScheduledTaskTypeTraktHistorySync:
		if err := requireProfile("traktAccountId", "Trakt history sync requires traktAccountId and profileId in config"); err != nil {
			return err
		}
		if taskConfig["syncDirection"] == "" {
			taskConfig["syncDirection"] = "trakt_to_local"
		} else if taskConfig["syncDirection"] != "trakt_to_local" && taskConfig["syncDirection"] != "local_to_trakt" && taskConfig["syncDirection"] != "bidirectional" {
			return fmt.Errorf("Invalid sync direction. Must be trakt_to_local, local_to_trakt, or bidirectional")
		}
	case config.ScheduledTaskTypeSimklHistorySync:
		if err := requireProfile("simklAccountId", "Simkl history sync requires simklAccountId and profileId in config"); err != nil {
			return err
		}
		if taskConfig["syncDirection"] == "" {
			taskConfig["syncDirection"] = "simkl_to_local"
		} else if taskConfig["syncDirection"] != "simkl_to_local" && taskConfig["syncDirection"] != "local_to_simkl" && taskConfig["syncDirection"] != "bidirectional" {
			return fmt.Errorf("Invalid sync direction. Must be simkl_to_local, local_to_simkl, or bidirectional")
		}
	case config.ScheduledTaskTypeSimklWatchlistSync:
		if err := requireProfile("simklAccountId", "Simkl watchlist sync requires simklAccountId and profileId in config"); err != nil {
			return err
		}
		switch taskConfig["syncDirection"] {
		case "", "source_to_target", "target_to_source", "bidirectional":
		default:
			return fmt.Errorf("Invalid sync direction. Must be source_to_target, target_to_source, or bidirectional")
		}
	case config.ScheduledTaskTypeScrobHistorySync:
		if err := requireProfile("scrobAccountId", "Scrob history sync requires scrobAccountId and profileId in config"); err != nil {
			return err
		}
		if taskConfig["syncDirection"] == "" {
			taskConfig["syncDirection"] = "scrob_to_local"
		} else if taskConfig["syncDirection"] != "scrob_to_local" && taskConfig["syncDirection"] != "local_to_scrob" && taskConfig["syncDirection"] != "bidirectional" {
			return fmt.Errorf("Invalid sync direction. Must be scrob_to_local, local_to_scrob, or bidirectional")
		}
	case config.ScheduledTaskTypeLocalMediaScan:
		if taskConfig == nil || strings.TrimSpace(taskConfig["libraryId"]) == "" {
			return errors.New("Local media scan requires libraryId in config")
		}
	case config.ScheduledTaskTypeMDBListWatchlistSync:
		if err := requireProfile("mdblistAccountId", "MDBList watchlist sync requires mdblistAccountId and profileId in config"); err != nil {
			return err
		}
		switch taskConfig["syncDirection"] {
		case "", "source_to_target", "target_to_source", "bidirectional":
		default:
			return fmt.Errorf("Invalid sync direction. Must be source_to_target, target_to_source, or bidirectional")
		}
	case config.ScheduledTaskTypeMDBListHistorySync:
		if err := requireProfile("mdblistAccountId", "MDBList history sync requires mdblistAccountId and profileId in config"); err != nil {
			return err
		}
		if taskConfig["syncDirection"] == "" {
			taskConfig["syncDirection"] = "mdblist_to_local"
		} else if taskConfig["syncDirection"] != "mdblist_to_local" && taskConfig["syncDirection"] != "local_to_mdblist" && taskConfig["syncDirection"] != "bidirectional" {
			return fmt.Errorf("Invalid sync direction. Must be mdblist_to_local, local_to_mdblist, or bidirectional")
		}
	}

	return nil
}

func validateScheduledTaskFrequency(taskType config.ScheduledTaskType, frequency config.ScheduledTaskFrequency) error {
	if taskType != config.ScheduledTaskTypeSimklHistorySync {
		return nil
	}
	switch frequency {
	case config.ScheduledTaskFrequency1Min, config.ScheduledTaskFrequency5Min, config.ScheduledTaskFrequency15Min:
		return errors.New("Simkl history sync must be scheduled no more frequently than every 30 minutes")
	default:
		return nil
	}
}

func validateScheduledTaskProfileID(profileID string, usersService scheduledTaskUsersProvider) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || usersService == nil {
		return nil
	}
	if usersService.Exists(profileID) {
		return nil
	}
	return fmt.Errorf("profileId %q does not exist", profileID)
}

func scheduledTaskReferencesForProfile(configManager *config.Manager, profileID string) ([]string, error) {
	if configManager == nil || strings.TrimSpace(profileID) == "" {
		return nil, nil
	}
	settings, err := configManager.Load()
	if err != nil {
		return nil, err
	}
	var references []string
	for _, task := range settings.ScheduledTasks.Tasks {
		if strings.TrimSpace(task.Config["profileId"]) == profileID {
			references = append(references, task.Name)
		}
	}
	return references, nil
}

func rejectScheduledTaskProfileDeletion(w http.ResponseWriter, configManager *config.Manager, profileID string) bool {
	references, err := scheduledTaskReferencesForProfile(configManager, profileID)
	if err != nil {
		http.Error(w, "failed to check scheduled task references", http.StatusInternalServerError)
		return true
	}
	if len(references) == 0 {
		return false
	}
	http.Error(w, "profile is used by scheduled tasks: "+strings.Join(references, ", ")+"; retarget or delete those tasks first", http.StatusConflict)
	return true
}

// ListTasks returns all scheduled tasks with current status
// GET /admin/api/scheduled-tasks
func (h *ScheduledTasksHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks := h.schedulerService.GetTaskStatus()
	if !auth.IsMaster(r) {
		scoped := make([]config.ScheduledTask, 0, len(tasks))
		for _, task := range tasks {
			if h.canAccessTask(r, task) {
				scoped = append(scoped, task)
			}
		}
		tasks = scoped
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": tasks,
	})
}

// CreateTask adds a new scheduled task
// POST /admin/api/scheduled-tasks
func (h *ScheduledTasksHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type      config.ScheduledTaskType      `json:"type"`
		Name      string                        `json:"name"`
		Frequency config.ScheduledTaskFrequency `json:"frequency"`
		Config    map[string]string             `json:"config"`
		Enabled   bool                          `json:"enabled"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	// Validate task type
	if req.Type == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task type is required",
		})
		return
	}

	// Validate task name
	if req.Name == "" {
		req.Name = string(req.Type)
	}

	// Validate frequency
	if req.Frequency == "" {
		req.Frequency = config.ScheduledTaskFrequency12Hours
	}
	if err := validateScheduledTaskFrequency(req.Type, req.Frequency); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	if req.Type == config.ScheduledTaskTypePrewarm && req.Config == nil {
		req.Config = map[string]string{}
	}
	if err := validateScheduledTaskConfig(req.Type, req.Config, h.usersService); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	task := config.ScheduledTask{
		ID:         uuid.New().String(),
		Type:       req.Type,
		Name:       req.Name,
		Frequency:  req.Frequency,
		Config:     req.Config,
		Enabled:    req.Enabled,
		LastStatus: config.ScheduledTaskStatusPending,
		CreatedAt:  time.Now().UTC(),
	}
	if !h.canAccessTask(r, task) {
		http.Error(w, "automation must belong to one of your profiles", http.StatusForbidden)
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	settings.ScheduledTasks.Tasks = append(settings.ScheduledTasks.Tasks, task)

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	// Auto-trigger "once" tasks immediately
	if task.Frequency == config.ScheduledTaskFrequencyOnce && task.Enabled {
		if err := h.schedulerService.RunTaskNow(task.ID); err != nil {
			log.Printf("[scheduler] Failed to auto-trigger one-time task %s: %v", task.ID, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"task":    task,
	})
}

// UpdateTask modifies an existing task
// PUT /admin/api/scheduled-tasks/{taskID}
func (h *ScheduledTasksHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["taskID"]
	if taskID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task ID is required",
		})
		return
	}

	var req struct {
		Name      string                        `json:"name"`
		Frequency config.ScheduledTaskFrequency `json:"frequency"`
		Config    map[string]string             `json:"config"`
		Enabled   *bool                         `json:"enabled"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	var updatedTask *config.ScheduledTask
	for i := range settings.ScheduledTasks.Tasks {
		if settings.ScheduledTasks.Tasks[i].ID == taskID {
			if !h.canAccessTask(r, settings.ScheduledTasks.Tasks[i]) {
				writeTaskNotFound(w)
				return
			}
			if req.Name != "" {
				settings.ScheduledTasks.Tasks[i].Name = req.Name
			}
			if req.Frequency != "" {
				settings.ScheduledTasks.Tasks[i].Frequency = req.Frequency
			}
			if req.Config != nil {
				settings.ScheduledTasks.Tasks[i].Config = req.Config
			}
			if req.Enabled != nil {
				settings.ScheduledTasks.Tasks[i].Enabled = *req.Enabled
			}
			updatedTask = &settings.ScheduledTasks.Tasks[i]
			break
		}
	}

	if updatedTask == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task not found",
		})
		return
	}

	if updatedTask.Type == config.ScheduledTaskTypePrewarm && updatedTask.Config == nil {
		updatedTask.Config = map[string]string{}
	}
	if err := validateScheduledTaskFrequency(updatedTask.Type, updatedTask.Frequency); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	if err := validateScheduledTaskConfig(updatedTask.Type, updatedTask.Config, h.usersService); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	if !h.canAccessTask(r, *updatedTask) {
		http.Error(w, "automation must belong to one of your profiles", http.StatusForbidden)
		return
	}

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"task":    updatedTask,
	})
}

// DeleteTask removes a scheduled task
// DELETE /admin/api/scheduled-tasks/{taskID}
func (h *ScheduledTasksHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["taskID"]
	if taskID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task ID is required",
		})
		return
	}
	if _, ok := h.findAccessibleTask(r, taskID); !ok {
		writeTaskNotFound(w)
		return
	}

	// Check if task is currently running
	if h.schedulerService.IsTaskRunning(taskID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Cannot delete a running task",
		})
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	found := false
	for i := range settings.ScheduledTasks.Tasks {
		if settings.ScheduledTasks.Tasks[i].ID == taskID {
			settings.ScheduledTasks.Tasks = append(
				settings.ScheduledTasks.Tasks[:i],
				settings.ScheduledTasks.Tasks[i+1:]...,
			)
			found = true
			break
		}
	}

	if !found {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task not found",
		})
		return
	}

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// RunTaskNow triggers immediate execution of a task
// POST /admin/api/scheduled-tasks/{taskID}/run
func (h *ScheduledTasksHandler) RunTaskNow(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["taskID"]
	if taskID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task ID is required",
		})
		return
	}
	if _, ok := h.findAccessibleTask(r, taskID); !ok {
		writeTaskNotFound(w)
		return
	}

	fullSync := false
	if value := r.URL.Query().Get("fullSync"); value != "" {
		var err error
		fullSync, err = strconv.ParseBool(value)
		if err != nil {
			writeJSONError(w, "fullSync must be a boolean", http.StatusBadRequest)
			return
		}
	}

	if err := h.schedulerService.RunTaskNow(taskID, fullSync); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Task execution started",
	})
}

// ToggleTask enables or disables a task
// POST /admin/api/scheduled-tasks/{taskID}/toggle
func (h *ScheduledTasksHandler) ToggleTask(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["taskID"]
	if taskID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task ID is required",
		})
		return
	}
	if _, ok := h.findAccessibleTask(r, taskID); !ok {
		writeTaskNotFound(w)
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	settings, err := h.configManager.Load()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to load settings: " + err.Error(),
		})
		return
	}

	var updatedTask *config.ScheduledTask
	for i := range settings.ScheduledTasks.Tasks {
		if settings.ScheduledTasks.Tasks[i].ID == taskID {
			settings.ScheduledTasks.Tasks[i].Enabled = req.Enabled
			updatedTask = &settings.ScheduledTasks.Tasks[i]
			break
		}
	}

	if updatedTask == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Task not found",
		})
		return
	}

	if err := h.configManager.Save(settings); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to save settings: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"enabled": req.Enabled,
	})
}

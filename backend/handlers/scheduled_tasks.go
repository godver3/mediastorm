package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"novastream/config"
	"novastream/services/scheduler"
)

// ScheduledTasksHandler handles scheduled tasks API endpoints
type ScheduledTasksHandler struct {
	configManager    *config.Manager
	schedulerService *scheduler.Service
}

// NewScheduledTasksHandler creates a new scheduled tasks handler
func NewScheduledTasksHandler(configManager *config.Manager, schedulerService *scheduler.Service) *ScheduledTasksHandler {
	return &ScheduledTasksHandler{
		configManager:    configManager,
		schedulerService: schedulerService,
	}
}

// ListTasks returns all scheduled tasks with current status
// GET /admin/api/scheduled-tasks
func (h *ScheduledTasksHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks := h.schedulerService.GetTaskStatus()

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

	// Validate config for Plex watchlist sync
	if req.Type == config.ScheduledTaskTypePlexWatchlistSync {
		if req.Config == nil || req.Config["plexAccountId"] == "" || req.Config["profileId"] == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Plex watchlist sync requires plexAccountId and profileId in config",
			})
			return
		}
	}

	// Validate config for Trakt list sync
	if req.Type == config.ScheduledTaskTypeTraktListSync {
		if req.Config == nil || req.Config["traktAccountId"] == "" || req.Config["profileId"] == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Trakt list sync requires traktAccountId and profileId in config",
			})
			return
		}
		// Validate listType
		listType := req.Config["listType"]
		if listType == "" {
			req.Config["listType"] = "watchlist" // Default to watchlist
		} else if listType != "watchlist" && listType != "collection" && listType != "favorites" && listType != "custom" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Invalid list type. Must be watchlist, collection, favorites, or custom",
			})
			return
		}
		// Validate customListId if listType is custom
		if listType == "custom" && req.Config["customListId"] == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Custom list sync requires customListId in config",
			})
			return
		}
	}

	// Validate config for Trakt history sync
	if req.Type == config.ScheduledTaskTypeTraktHistorySync {
		if req.Config == nil || req.Config["traktAccountId"] == "" || req.Config["profileId"] == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Trakt history sync requires traktAccountId and profileId in config",
			})
			return
		}
		// Default syncDirection to trakt_to_local
		if req.Config["syncDirection"] == "" {
			req.Config["syncDirection"] = "trakt_to_local"
		} else if req.Config["syncDirection"] != "trakt_to_local" && req.Config["syncDirection"] != "local_to_trakt" && req.Config["syncDirection"] != "bidirectional" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "Invalid sync direction. Must be trakt_to_local, local_to_trakt, or bidirectional",
			})
			return
		}
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

	if err := h.schedulerService.RunTaskNow(taskID); err != nil {
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

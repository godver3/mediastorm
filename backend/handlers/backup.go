package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/services/backup"
)

// BackupHandler handles backup API endpoints
type BackupHandler struct {
	backupService *backup.Service
}

// NewBackupHandler creates a new backup handler
func NewBackupHandler(backupService *backup.Service) *BackupHandler {
	return &BackupHandler{
		backupService: backupService,
	}
}

// ListBackups returns all available backups
// GET /admin/api/backups
func (h *BackupHandler) ListBackups(w http.ResponseWriter, r *http.Request) {
	backups, err := h.backupService.ListBackups()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to list backups: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"backups": backups,
	})
}

// CreateBackup creates a new manual backup
// POST /admin/api/backups
func (h *BackupHandler) CreateBackup(w http.ResponseWriter, r *http.Request) {
	info, err := h.backupService.CreateBackup(backup.BackupTypeManual)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to create backup: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"backup":  info,
	})
}

// DownloadBackup streams a backup file for download
// GET /admin/api/backups/{filename}/download
func (h *BackupHandler) DownloadBackup(w http.ResponseWriter, r *http.Request) {
	filename := mux.Vars(r)["filename"]
	if filename == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Filename is required",
		})
		return
	}

	reader, size, err := h.backupService.GetBackupReader(filename)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "invalid") {
			status = http.StatusBadRequest
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	defer reader.Close()

	// Set headers for file download
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))

	// Stream the file
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("[backup] Error streaming backup %s: %v", filename, err)
	}
}

// RestoreBackup restores from a backup file
// POST /admin/api/backups/{filename}/restore
func (h *BackupHandler) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	filename := mux.Vars(r)["filename"]
	if filename == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Filename is required",
		})
		return
	}

	// First, create a pre-restore backup
	preRestoreBackup, err := h.backupService.CreateBackup(backup.BackupTypePreRestore)
	if err != nil {
		log.Printf("[backup] Warning: failed to create pre-restore backup: %v", err)
		// Continue with restore anyway
	} else {
		log.Printf("[backup] Created pre-restore backup: %s", preRestoreBackup.Filename)
	}

	// Perform restore
	if err := h.backupService.RestoreBackup(filename); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "checksum") {
			status = http.StatusBadRequest
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":           "Failed to restore backup: " + err.Error(),
			"preRestoreBackup": preRestoreBackup,
		})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": "Backup restored successfully. Restart the server to apply all changes.",
	}

	if preRestoreBackup != nil {
		response["preRestoreBackup"] = preRestoreBackup
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// DeleteBackup removes a backup file
// DELETE /admin/api/backups/{filename}
func (h *BackupHandler) DeleteBackup(w http.ResponseWriter, r *http.Request) {
	filename := mux.Vars(r)["filename"]
	if filename == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Filename is required",
		})
		return
	}

	if err := h.backupService.DeleteBackup(filename); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "invalid") {
			status = http.StatusBadRequest
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// ExportData exports all database data as a portable JSON download.
// GET /api/admin/export
func (h *BackupHandler) ExportData(w http.ResponseWriter, r *http.Request) {
	data, err := h.backupService.ExportDatabaseJSON()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to export data: " + err.Error(),
		})
		return
	}

	filename := fmt.Sprintf("mediastorm_export_%s.json", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Write(data)
}

// ImportData imports data from a previously exported JSON file.
// POST /api/admin/import
func (h *BackupHandler) ImportData(w http.ResponseWriter, r *http.Request) {
	// Limit upload to 50MB
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	data, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to read request body: " + err.Error(),
		})
		return
	}

	// Create pre-import backup
	preBackup, err := h.backupService.CreateBackup(backup.BackupTypePreRestore)
	if err != nil {
		log.Printf("[backup] Warning: failed to create pre-import backup: %v", err)
	} else {
		log.Printf("[backup] Created pre-import backup: %s", preBackup.Filename)
	}

	if err := h.backupService.ImportDatabaseJSON(data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":          "Failed to import data: " + err.Error(),
			"preImportBackup": preBackup,
		})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": "Data imported successfully. Restart the server to apply all changes.",
	}
	if preBackup != nil {
		response["preImportBackup"] = preBackup
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

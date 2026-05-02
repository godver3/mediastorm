package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/recordings"

	"github.com/gorilla/mux"
)

type recordingUsersProvider interface {
	BelongsToAccount(profileID, accountID string) bool
	Exists(id string) bool
	ListAll() []models.User
}

type recordingService interface {
	List(filter models.RecordingListFilter) ([]models.Recording, error)
	Get(id string) (*models.Recording, error)
	CreateFromEPG(req models.CreateEPGRecordingRequest) (models.Recording, error)
	CreateTimeBlock(req models.CreateTimeBlockRecordingRequest) (models.Recording, error)
	Cancel(id string) error
	Delete(id string) error
}

type RecordingsHandler struct {
	service recordingService
	users   recordingUsersProvider
}

func NewRecordingsHandler(service recordingService, users recordingUsersProvider) *RecordingsHandler {
	return &RecordingsHandler{service: service, users: users}
}

func (h *RecordingsHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	filter := models.RecordingListFilter{
		Statuses:   parseStatuses(r.URL.Query()["status"]),
		IncludeAll: auth.IsMaster(r),
	}
	if !auth.IsMaster(r) {
		profileID, ok := h.requireProfileOwnership(w, r, r.URL.Query().Get("profileId"))
		if !ok {
			return
		}
		filter.UserID = profileID
	} else if userID := strings.TrimSpace(r.URL.Query().Get("userId")); userID != "" {
		filter.UserID = userID
		filter.IncludeAll = false
	}
	recordingsList, err := h.service.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if recordingsList == nil {
		recordingsList = []models.Recording{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"recordings": recordingsList})
}

func (h *RecordingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	recordingID := strings.TrimSpace(mux.Vars(r)["recordingID"])
	recording, err := h.service.Get(recordingID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, recordings.ErrRecordingNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !auth.IsMaster(r) {
		if _, ok := h.requireProfileOwnership(w, r, recording.UserID); !ok {
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(recording)
}

func (h *RecordingsHandler) CreateEPG(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req models.CreateEPGRecordingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !auth.IsMaster(r) {
		profileID, ok := h.requireProfileOwnership(w, r, req.ProfileID)
		if !ok {
			return
		}
		req.ProfileID = profileID
	}
	recording, err := h.service.CreateFromEPG(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(recording)
}

func (h *RecordingsHandler) CreateTimeBlock(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req models.CreateTimeBlockRecordingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !auth.IsMaster(r) {
		profileID, ok := h.requireProfileOwnership(w, r, req.ProfileID)
		if !ok {
			return
		}
		req.ProfileID = profileID
	}
	recording, err := h.service.CreateTimeBlock(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(recording)
}

func (h *RecordingsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	recordingID := strings.TrimSpace(mux.Vars(r)["recordingID"])
	recording, err := h.service.Get(recordingID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, recordings.ErrRecordingNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !auth.IsMaster(r) {
		if _, ok := h.requireProfileOwnership(w, r, recording.UserID); !ok {
			return
		}
	}
	if err := h.service.Cancel(recordingID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RecordingsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	recordingID := strings.TrimSpace(mux.Vars(r)["recordingID"])
	recording, err := h.service.Get(recordingID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, recordings.ErrRecordingNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !auth.IsMaster(r) {
		if _, ok := h.requireProfileOwnership(w, r, recording.UserID); !ok {
			return
		}
	}
	if err := h.service.Delete(recordingID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RecordingsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.Error(w, "recordings service unavailable", http.StatusServiceUnavailable)
		return
	}
	recordingID := strings.TrimSpace(mux.Vars(r)["recordingID"])
	recording, err := h.service.Get(recordingID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, recordings.ErrRecordingNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if !auth.IsMaster(r) {
		if _, ok := h.requireProfileOwnership(w, r, recording.UserID); !ok {
			return
		}
	}
	switch recording.Status {
	case models.RecordingStatusCompleted,
		models.RecordingStatusRunning,
		models.RecordingStatusCancelled,
		models.RecordingStatusFailed:
	default:
		http.Error(w, "recording is not ready for playback", http.StatusConflict)
		return
	}
	outputPath := strings.TrimSpace(recording.OutputPath)
	if outputPath == "" {
		http.Error(w, "recording file unavailable", http.StatusNotFound)
		return
	}
	info, statErr := os.Stat(outputPath)
	if statErr != nil || info.IsDir() {
		http.Error(w, "recording file unavailable", http.StatusNotFound)
		return
	}

	var trackedWriter http.ResponseWriter = w
	if r.Method == http.MethodGet {
		tracker := GetStreamTracker()
		accountID := h.accountIDForProfile(recording.UserID)
		streamID, bytesCounter, activityCounter := tracker.StartStreamWithAccount(r, outputPath, info.Size(), 0, 0, accountID)
		defer tracker.EndStream(streamID)
		trackedWriter = &trackingWriter{
			ResponseWriter:  w,
			counter:         bytesCounter,
			activityCounter: activityCounter,
		}
	}

	filename := filepath.Base(outputPath)
	if strings.EqualFold(filepath.Ext(filename), ".ts") {
		trackedWriter.Header().Set("Content-Type", "video/mp2t")
	}
	trackedWriter.Header().Set("Content-Disposition", buildInlineContentDisposition(filename))
	if recording.Status == models.RecordingStatusRunning {
		rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
		log.Printf("[recordings] streaming running recording id=%s mode=growing range=%q path=%s", recordingID, rangeHeader, outputPath)
		if err := h.streamGrowingRecording(trackedWriter, r, recordingID, outputPath); err != nil && !errors.Is(err, r.Context().Err()) {
			http.Error(trackedWriter, "failed to stream recording", http.StatusInternalServerError)
		}
		return
	}
	log.Printf("[recordings] streaming recording id=%s mode=file status=%s path=%s", recordingID, recording.Status, outputPath)
	http.ServeFile(trackedWriter, r, outputPath)
}

func (h *RecordingsHandler) streamGrowingRecording(w http.ResponseWriter, r *http.Request, recordingID, outputPath string) error {
	file, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	buf := make([]byte, 256*1024)
	var offset int64
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			offset += int64(n)
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}

		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return readErr
		}

		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-ticker.C:
		}

		info, statErr := file.Stat()
		if statErr == nil && info.Size() > offset {
			continue
		}

		latest, getErr := h.service.Get(recordingID)
		if getErr == nil && latest != nil && latest.Status != models.RecordingStatusRunning {
			if statErr == nil && info.Size() <= offset {
				return nil
			}
		}
	}
}

func (h *RecordingsHandler) accountIDForProfile(profileID string) string {
	if h.users == nil || profileID == "" {
		return ""
	}
	for _, user := range h.users.ListAll() {
		if user.ID == profileID {
			return user.AccountID
		}
	}
	return ""
}

func (h *RecordingsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *RecordingsHandler) requireProfileOwnership(w http.ResponseWriter, r *http.Request, rawProfileID string) (string, bool) {
	profileID := strings.TrimSpace(rawProfileID)
	if profileID == "" {
		http.Error(w, "profileId is required", http.StatusBadRequest)
		return "", false
	}
	if h.users != nil && !h.users.Exists(profileID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return "", false
	}
	accountID := auth.GetAccountID(r)
	if accountID == "" || h.users == nil || !h.users.BelongsToAccount(profileID, accountID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return "", false
	}
	return profileID, true
}

func parseStatuses(values []string) []models.RecordingStatus {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[models.RecordingStatus]bool)
	var statuses []models.RecordingStatus
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			status := models.RecordingStatus(strings.TrimSpace(part))
			if status == "" || seen[status] {
				continue
			}
			seen[status] = true
			statuses = append(statuses, status)
		}
	}
	return statuses
}

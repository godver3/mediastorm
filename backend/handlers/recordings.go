package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"novastream/config"
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

type recordingRuleService interface {
	GetRule(id string) (*models.RecordingRule, error)
	ListRules(userID string, includeAll bool) ([]models.RecordingRule, error)
	CreateRule(rule models.RecordingRule) (models.RecordingRule, error)
	UpdateRule(id string, update models.UpdateRecordingRuleRequest) (models.RecordingRule, error)
	DeleteRule(id string) error
}

type recordingUserSettingsService interface {
	Get(userID string) (*models.UserSettings, error)
	Update(userID string, settings models.UserSettings) error
}

type RecordingsHandler struct {
	service      recordingService
	rules        recordingRuleService
	users        recordingUsersProvider
	userSettings recordingUserSettingsService
	cfgManager   *config.Manager
	liveChannels LiveChannelProvider
}

func NewRecordingsHandler(service recordingService, users recordingUsersProvider) *RecordingsHandler {
	return &RecordingsHandler{service: service, users: users}
}

func (h *RecordingsHandler) SetRuleService(service recordingRuleService) { h.rules = service }

// SetLiveAccessProvider binds new recording jobs to account-visible channels.
func (h *RecordingsHandler) SetLiveAccessProvider(manager *config.Manager, channels LiveChannelProvider) {
	h.cfgManager = manager
	h.liveChannels = channels
}

func (h *RecordingsHandler) SetUserSettingsService(service recordingUserSettingsService) {
	h.userSettings = service
}

type createRecordingPayload struct {
	ProfileID            string `json:"profileId"`
	ChannelID            string `json:"channelId"`
	TvgID                string `json:"tvgId"`
	ChannelName          string `json:"channelName"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	SourceURL            string `json:"sourceUrl"`
	Start                string `json:"start"`
	Stop                 string `json:"stop"`
	PaddingBeforeSeconds *int   `json:"paddingBeforeSeconds,omitempty"`
	PaddingAfterSeconds  *int   `json:"paddingAfterSeconds,omitempty"`
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
	} else if profileID := strings.TrimSpace(r.URL.Query().Get("profileId")); profileID != "" {
		if !h.requireExistingProfile(w, profileID) {
			return
		}
		filter.UserID = profileID
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
	var payload createRecordingPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	profileID := payload.ProfileID
	if !auth.IsMaster(r) {
		var ok bool
		profileID, ok = h.requireProfileOwnership(w, r, profileID)
		if !ok {
			return
		}
	} else if !h.requireExistingProfile(w, profileID) {
		return
	}
	if !h.bindRecordingChannel(w, r, profileID, &payload) {
		return
	}
	before, after := h.defaultRecordingPadding(profileID)
	if payload.PaddingBeforeSeconds != nil {
		before = *payload.PaddingBeforeSeconds
	}
	if payload.PaddingAfterSeconds != nil {
		after = *payload.PaddingAfterSeconds
	}
	req := models.CreateEPGRecordingRequest{
		ProfileID: profileID, ChannelID: payload.ChannelID, TvgID: payload.TvgID,
		ChannelName: payload.ChannelName, Title: payload.Title, Description: payload.Description,
		SourceURL: payload.SourceURL, Start: payload.Start, Stop: payload.Stop,
		PaddingBeforeSeconds: before, PaddingAfterSeconds: after,
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
	var payload createRecordingPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	profileID := payload.ProfileID
	if !auth.IsMaster(r) {
		var ok bool
		profileID, ok = h.requireProfileOwnership(w, r, profileID)
		if !ok {
			return
		}
	} else if !h.requireExistingProfile(w, profileID) {
		return
	}
	if !h.bindRecordingChannel(w, r, profileID, &payload) {
		return
	}
	before, after := h.defaultRecordingPadding(profileID)
	if payload.PaddingBeforeSeconds != nil {
		before = *payload.PaddingBeforeSeconds
	}
	if payload.PaddingAfterSeconds != nil {
		after = *payload.PaddingAfterSeconds
	}
	req := models.CreateTimeBlockRecordingRequest{
		ProfileID: profileID, ChannelID: payload.ChannelID, TvgID: payload.TvgID,
		ChannelName: payload.ChannelName, Title: payload.Title, Description: payload.Description,
		SourceURL: payload.SourceURL, Start: payload.Start, Stop: payload.Stop,
		PaddingBeforeSeconds: before, PaddingAfterSeconds: after,
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
	streamRequest := r
	if r.Method == http.MethodGet {
		tracker := GetStreamTracker()
		accountID := h.accountIDForProfile(recording.UserID)
		streamCtx, cancelStream := context.WithCancel(r.Context())
		defer cancelStream()
		streamRequest = r.WithContext(streamCtx)
		trackingRequest := h.requestWithRecordingStreamMetadata(streamRequest, recording)
		streamID, bytesCounter, activityCounter := tracker.StartStreamWithAccount(trackingRequest, outputPath, info.Size(), 0, 0, accountID)
		tracker.SetStreamCancel(streamID, cancelStream)
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
	if rangeHeader := strings.TrimSpace(streamRequest.Header.Get("Range")); rangeHeader != "" {
		diagnosticWriter := &recordingRangeDiagnosticWriter{ResponseWriter: trackedWriter}
		trackedWriter = diagnosticWriter
		defer func() {
			log.Printf("[recordings] range response id=%s status=%d request=%q contentRange=%q contentLength=%q acceptRanges=%q fileSize=%d bytesWritten=%d",
				recordingID, diagnosticWriter.statusCode(), rangeHeader,
				diagnosticWriter.Header().Get("Content-Range"),
				diagnosticWriter.Header().Get("Content-Length"),
				diagnosticWriter.Header().Get("Accept-Ranges"),
				info.Size(), diagnosticWriter.bytesWritten)
		}()
	}
	if recording.Status == models.RecordingStatusRunning {
		rangeHeader := strings.TrimSpace(streamRequest.Header.Get("Range"))
		log.Printf("[recordings] streaming running recording id=%s mode=growing range=%q path=%s", recordingID, rangeHeader, outputPath)
		if err := h.streamGrowingRecording(trackedWriter, streamRequest, recordingID, outputPath); err != nil && !errors.Is(err, streamRequest.Context().Err()) {
			http.Error(trackedWriter, "failed to stream recording", http.StatusInternalServerError)
		}
		return
	}
	log.Printf("[recordings] streaming recording id=%s mode=file status=%s path=%s", recordingID, recording.Status, outputPath)
	http.ServeFile(trackedWriter, streamRequest, outputPath)
}

type recordingRangeDiagnosticWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
}

func (w *recordingRangeDiagnosticWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *recordingRangeDiagnosticWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingRangeDiagnosticWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *recordingRangeDiagnosticWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (h *RecordingsHandler) requestWithRecordingStreamMetadata(r *http.Request, recording *models.Recording) *http.Request {
	if r == nil || recording == nil {
		return r
	}
	cloned := r.Clone(r.Context())
	u := *r.URL
	q := u.Query()
	if strings.TrimSpace(q.Get("profileId")) == "" && strings.TrimSpace(recording.UserID) != "" {
		q.Set("profileId", recording.UserID)
	}
	if strings.TrimSpace(q.Get("profileName")) == "" {
		if profileName := h.profileNameForProfile(recording.UserID); profileName != "" {
			q.Set("profileName", profileName)
		}
	}
	if strings.TrimSpace(q.Get("mediaType")) == "" {
		q.Set("mediaType", "channel")
	}
	if strings.TrimSpace(q.Get("itemId")) == "" {
		if tvgID := strings.TrimSpace(recording.TvgID); tvgID != "" {
			q.Set("itemId", tvgID)
		} else if channelID := strings.TrimSpace(recording.ChannelID); channelID != "" {
			q.Set("itemId", channelID)
		}
	}
	if title := normalizeRecordingDisplayText(q.Get("title")); title != "" {
		q.Set("title", title)
	} else {
		if title := normalizeRecordingDisplayText(recording.Title); title != "" {
			q.Set("title", title)
		} else if channelName := normalizeRecordingDisplayText(recording.ChannelName); channelName != "" {
			q.Set("title", channelName)
		}
	}
	u.RawQuery = q.Encode()
	cloned.URL = &u
	return cloned
}

func normalizeRecordingDisplayText(value string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "+", " ")
	return strings.Join(strings.Fields(normalized), " ")
}

func (h *RecordingsHandler) streamGrowingRecording(w http.ResponseWriter, r *http.Request, recordingID, outputPath string) error {
	file, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Native players seek with byte ranges. Serve a snapshot of the bytes already
	// recorded so a seek does not restart playback at the beginning of the file.
	if r.Header.Get("Range") != "" {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "video/mp2t")
		http.ServeContent(w, r, filepath.Base(outputPath), info.ModTime(), file)
		return nil
	}

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

func (h *RecordingsHandler) profileNameForProfile(profileID string) string {
	if h.users == nil || profileID == "" {
		return ""
	}
	for _, user := range h.users.ListAll() {
		if user.ID == profileID {
			return user.Name
		}
	}
	return ""
}

func (h *RecordingsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *RecordingsHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	if h.rules == nil {
		http.Error(w, "recording rules unavailable", http.StatusServiceUnavailable)
		return
	}
	profileID, ok := h.requestProfile(w, r, r.URL.Query().Get("profileId"))
	if !ok {
		return
	}
	rules, err := h.rules.ListRules(profileID, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rules == nil {
		rules = []models.RecordingRule{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"rules": rules})
}

func (h *RecordingsHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	if h.rules == nil {
		http.Error(w, "recording rules unavailable", http.StatusServiceUnavailable)
		return
	}
	var payload models.CreateRecordingRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	profileID, ok := h.requestProfile(w, r, payload.ProfileID)
	if !ok {
		return
	}
	before, after := h.defaultRecordingPadding(profileID)
	if payload.PaddingBeforeSeconds != nil {
		before = *payload.PaddingBeforeSeconds
	}
	if payload.PaddingAfterSeconds != nil {
		after = *payload.PaddingAfterSeconds
	}
	rule, err := h.rules.CreateRule(models.RecordingRule{
		UserID: profileID, MatchType: payload.MatchType, Pattern: payload.Pattern,
		ChannelID: payload.ChannelID, TvgID: payload.TvgID, ChannelName: payload.ChannelName,
		AllChannels: payload.AllChannels, NewOnly: payload.NewOnly, PaddingBeforeSeconds: before, PaddingAfterSeconds: after,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(rule)
}

func (h *RecordingsHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	if h.rules == nil {
		http.Error(w, "recording rules unavailable", http.StatusServiceUnavailable)
		return
	}
	rule, ok := h.authorizedRule(w, r)
	if !ok {
		return
	}
	var update models.UpdateRecordingRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(update.ProfileID) != "" && update.ProfileID != rule.UserID {
		http.Error(w, "recording rule profile does not match", http.StatusBadRequest)
		return
	}
	updated, err := h.rules.UpdateRule(rule.ID, update)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func (h *RecordingsHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	if h.rules == nil {
		http.Error(w, "recording rules unavailable", http.StatusServiceUnavailable)
		return
	}
	rule, ok := h.authorizedRule(w, r)
	if !ok {
		return
	}
	if err := h.rules.DeleteRule(rule.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RecordingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	profileID, ok := h.requestProfile(w, r, r.URL.Query().Get("profileId"))
	if !ok {
		return
	}
	before, after := h.defaultRecordingPadding(profileID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(models.RecordingSettings{
		PaddingBeforeSeconds: models.IntPtr(before), PaddingAfterSeconds: models.IntPtr(after),
	})
}

func (h *RecordingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.userSettings == nil {
		http.Error(w, "recording settings unavailable", http.StatusServiceUnavailable)
		return
	}
	var update models.UpdateRecordingSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	profileID, ok := h.requestProfile(w, r, update.ProfileID)
	if !ok {
		return
	}
	if update.PaddingBeforeSeconds < 0 || update.PaddingBeforeSeconds > 3600 || update.PaddingAfterSeconds < 0 || update.PaddingAfterSeconds > 3600 {
		http.Error(w, "recording buffers must be between 0 and 60 minutes", http.StatusBadRequest)
		return
	}
	settings := models.DefaultUserSettings()
	if current, err := h.userSettings.Get(profileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if current != nil {
		settings = *current
	}
	settings.Recordings = models.RecordingSettings{
		PaddingBeforeSeconds: models.IntPtr(update.PaddingBeforeSeconds),
		PaddingAfterSeconds:  models.IntPtr(update.PaddingAfterSeconds),
	}
	if err := h.userSettings.Update(profileID, settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(settings.Recordings)
}

func (h *RecordingsHandler) authorizedRule(w http.ResponseWriter, r *http.Request) (*models.RecordingRule, bool) {
	rule, err := h.rules.GetRule(strings.TrimSpace(mux.Vars(r)["ruleID"]))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if rule == nil {
		http.Error(w, "recording rule not found", http.StatusNotFound)
		return nil, false
	}
	if _, ok := h.requestProfile(w, r, rule.UserID); !ok {
		return nil, false
	}
	return rule, true
}

func (h *RecordingsHandler) requestProfile(w http.ResponseWriter, r *http.Request, rawProfileID string) (string, bool) {
	if !auth.IsMaster(r) {
		return h.requireProfileOwnership(w, r, rawProfileID)
	}
	if !h.requireExistingProfile(w, rawProfileID) {
		return "", false
	}
	return strings.TrimSpace(rawProfileID), true
}

func (h *RecordingsHandler) requireExistingProfile(w http.ResponseWriter, rawProfileID string) bool {
	profileID := strings.TrimSpace(rawProfileID)
	if profileID == "" {
		http.Error(w, "profileId is required", http.StatusBadRequest)
		return false
	}
	if h.users == nil || !h.users.Exists(profileID) {
		http.Error(w, "profile not found", http.StatusNotFound)
		return false
	}
	return true
}

func (h *RecordingsHandler) defaultRecordingPadding(profileID string) (int, int) {
	defaults := models.DefaultUserSettings().Recordings
	before := models.IntVal(defaults.PaddingBeforeSeconds, 300)
	after := models.IntVal(defaults.PaddingAfterSeconds, 300)
	if h.userSettings == nil {
		return before, after
	}
	settings, err := h.userSettings.Get(profileID)
	if err != nil || settings == nil {
		return before, after
	}
	return models.IntVal(settings.Recordings.PaddingBeforeSeconds, before), models.IntVal(settings.Recordings.PaddingAfterSeconds, after)
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

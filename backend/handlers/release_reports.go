package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/internal/profileaccess"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/playback"
	"novastream/utils/language"
)

type releaseReportRequest struct {
	AudioLanguage   string  `json:"audioLanguage,omitempty"`
	Reason          string  `json:"reason"`
	SourceVersion   string  `json:"sourceVersion"`
	AudioTrackIndex *int    `json:"audioTrackIndex,omitempty"`
	Retry           bool    `json:"retry"`
	StartOffset     float64 `json:"startOffset,omitempty"`
	SkipHLS         bool    `json:"skipHLS,omitempty"`
	ClientID        string  `json:"clientId,omitempty"`
}

func sourceReportVersion(entry *playback.PrequeueEntry) string {
	data, _ := json.Marshal([]any{entry.ID, entry.StreamPath, entry.SelectedResult, entry.FileSize, entry.AudioTracks, entry.SelectedAudioTrack})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h *PrequeueHandler) reportableEntry(w http.ResponseWriter, r *http.Request) *playback.PrequeueEntry {
	if h == nil || h.store == nil || h.badStreamsSvc == nil {
		http.Error(w, "release reports unavailable", http.StatusServiceUnavailable)
		return nil
	}
	if session, ok := auth.GetSession(r); auth.IsShareLinkRequest(r) || (ok && session.Scope == models.SessionScopeStream) {
		http.Error(w, "release reports require a profile session", http.StatusForbidden)
		return nil
	}
	entry, ok := h.store.Get(strings.TrimSpace(mux.Vars(r)["prequeueID"]))
	if !ok {
		http.NotFound(w, r)
		return nil
	}
	if !h.canAccessUser(r, entry.UserID) {
		http.NotFound(w, r)
		return nil
	}
	if !profileaccess.CheckResource(w, r, entry.UserID, entry.StreamPath) {
		return nil
	}
	if libraryID := reportLibraryID(entry); libraryID != "" {
		if h.localAccess == nil {
			http.NotFound(w, r)
			return nil
		}
		allowed, err := h.localAccess.CanAccess(r.Context(), libraryID, auth.GetAccountID(r), entry.UserID, auth.IsMaster(r))
		if err != nil || !allowed {
			http.NotFound(w, r)
			return nil
		}
	}
	if entry.SelectedResult == nil || entry.StreamPath == "" || entry.Status != playback.PrequeueStatusReady {
		http.Error(w, "a prepared selected release is required", http.StatusConflict)
		return nil
	}
	return entry
}

func (h *PrequeueHandler) canShareReleaseReport(r *http.Request, userID string) bool {
	if !auth.IsMaster(r) {
		return false
	}
	profiles, ok := h.users.(interface {
		Get(string) (models.User, bool)
	})
	if !ok {
		return false
	}
	profile, exists := profiles.Get(userID)
	return exists && !profile.IsKidsProfile
}

// ReleaseReportOptions binds the dialog to the server's actual selected source,
// rather than accepting a caller-supplied release name or private URL.
func (h *PrequeueHandler) ReleaseReportOptions(w http.ResponseWriter, r *http.Request) {
	entry := h.reportableEntry(w, r)
	if entry == nil {
		return
	}
	client := normalizeClientID(r.Header.Get("X-Client-ID"))
	ctx := h.reportContextForPrequeue(entry.ID, entry.UserID, client, entry.TargetEpisode)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sourceVersion": sourceReportVersion(entry), "releaseName": entry.SelectedResult.Title, "audioLanguage": ctx.AudioLanguage, "audioTracks": entry.AudioTracks, "selectedAudioTrack": entry.SelectedAudioTrack, "canShare": h.canShareReleaseReport(r, entry.UserID), "hasEpisode": entry.TargetEpisode != nil})
}

func (h *PrequeueHandler) ReportRelease(w http.ResponseWriter, r *http.Request) {
	h.releaseAction(w, r, true)
}
func (h *PrequeueHandler) RetryRelease(w http.ResponseWriter, r *http.Request) {
	h.releaseAction(w, r, false)
}
func (h *PrequeueHandler) releaseAction(w http.ResponseWriter, r *http.Request, reporting bool) {
	entry := h.reportableEntry(w, r)
	if entry == nil {
		return
	}
	var req releaseReportRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid release report", http.StatusBadRequest)
		return
	}
	if req.SourceVersion == "" || req.SourceVersion != sourceReportVersion(entry) {
		http.Error(w, "the selected source changed; reopen the report dialog", http.StatusConflict)
		return
	}
	client := normalizeClientID(req.ClientID)
	if client == "" {
		client = normalizeClientID(r.Header.Get("X-Client-ID"))
	}
	ctx := h.reportContextForPrequeue(entry.ID, entry.UserID, client, entry.TargetEpisode)
	if reporting && req.Reason == "missing_language" && (req.AudioLanguage == "" || language.NormalizeToCode(req.AudioLanguage) != language.NormalizeToCode(ctx.AudioLanguage)) {
		http.Error(w, "audio preference changed; reopen the report dialog", http.StatusConflict)
		return
	}

	if req.Reason == "audio_problem" {
		index := entry.SelectedAudioTrack
		if req.AudioTrackIndex != nil {
			index = *req.AudioTrackIndex
		}
		known := false
		for _, track := range entry.AudioTracks {
			if track.Index == index {
				known = true
				break
			}
		}
		if !known {
			http.Error(w, "choose an audio track from the selected release", http.StatusBadRequest)
			return
		}
		ctx.AudioTrackIndex = &index
	}
	response := map[string]any{}
	if reporting {
		report, err := h.badStreamsSvc.Report(badstreams.ReportRequest{Result: *entry.SelectedResult, Context: ctx, Reason: req.Reason, ReporterID: entry.UserID, Shared: h.canShareReleaseReport(r, entry.UserID)})
		if err != nil {
			var pathError *os.PathError
			if errors.As(err, &pathError) {
				http.Error(w, "could not save the release report", http.StatusServiceUnavailable)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		response["report"] = report
	} else {
		if req.Reason != "" {
			http.Error(w, "a manual retry does not submit a report", http.StatusBadRequest)
			return
		}
		req.Retry = true
	}

	if req.Retry {
		offset := req.StartOffset
		if offset < 0 || req.Reason == "wrong_title" || req.Reason == "wrong_episode" {
			offset = 0
		}
		if entry.Duration > 0 && offset > entry.Duration {
			offset = 0
		}
		h.store.CancelWork(entry.ID)
		scope, _ := h.prequeueSettingsScope(entry.UserID, client, entry.TitleID, adaptiveThroughputFromRequest(r))
		next, created, createErr := h.store.CreateReportReplacement(entry.ID, scope)
		if createErr != nil {
			http.Error(w, "could not prepare an alternative", http.StatusServiceUnavailable)
			return
		}
		h.latencyTracker.NotePrequeueRequested(next.ID, entry.TitleID, entry.UserID, entry.TitleName, entry.MediaType)
		if created {
			go h.runPrequeueWorker(next.ID, entry.TitleID, entry.TitleName, "", entry.MediaType, entry.Year, entry.UserID, client, adaptiveThroughputFromRequest(r), entry.TargetEpisode, offset, req.SkipHLS, prequeueWorkerInteractive, prequeueLocalScope{accountID: auth.GetAccountID(r), master: auth.IsMaster(r), excludedResult: func() *models.NZBResult {
				if reporting {
					return nil
				}
				result := *entry.SelectedResult
				return &result
			}()})
		}
		response["replacement"] = playback.PrequeueResponse{PrequeueID: next.ID, TargetEpisode: entry.TargetEpisode, Status: playback.PrequeueStatusQueued}
		response["resumePosition"] = offset
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (h *PrequeueHandler) CancelReleaseRetry(w http.ResponseWriter, r *http.Request) {
	if session, ok := auth.GetSession(r); auth.IsShareLinkRequest(r) || (ok && session.Scope == models.SessionScopeStream) {
		http.Error(w, "source changes require a profile session", http.StatusForbidden)
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["prequeueID"])
	entry, ok := h.store.Get(id)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.authorizeEntry(w, r, entry) || !profileaccess.CheckResource(w, r, entry.UserID, entry.StreamPath) {
		return
	}
	h.store.CancelWork(id)
	h.store.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func reportLibraryID(entry *playback.PrequeueEntry) string {
	if entry.SelectedResult == nil {
		return ""
	}
	return entry.SelectedResult.Attributes["localLibraryId"]
}

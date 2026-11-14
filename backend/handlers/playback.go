package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/badstreams"
	playbacksvc "novastream/services/playback"
	"novastream/services/trackselect"
)

type playbackService interface {
	Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
	ResolveBatch(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error)
	QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error)
}

type thumbnailPrewarmer interface {
	PrewarmThumbnails(path string)
}

// PlaybackHandler resolves NZB candidates into playable streams via the local registry.
type PlaybackHandler struct {
	Service            playbackService
	SubtitleExtractor  SubtitlePreExtractor // For pre-extracting subtitles
	VideoProber        VideoFullProber      // For probing subtitle streams
	BadStreams         *badstreams.Service
	ThumbnailPrewarmer thumbnailPrewarmer
	// TrackPreferences resolves profile/client/title language preferences for
	// the optional track selection returned by Resolve.
	TrackPreferences *trackselect.Resolver
	Users            prequeueOwnershipService
}

// trackSelectionProbeTimeout bounds the probe Resolve runs for track selection.
// The probe result is cached and reused by the follow-up metadata request.
const trackSelectionProbeTimeout = 20 * time.Second

var _ playbackService = (*playbacksvc.Service)(nil)

func NewPlaybackHandler(s playbackService) *PlaybackHandler {
	return &PlaybackHandler{Service: s}
}

func rejectM2TSPlaybackResolution(w http.ResponseWriter, resolution *models.PlaybackResolution) bool {
	if resolution == nil || !isM2TSStreamPath(resolution.WebDAVPath) {
		return false
	}
	log.Printf("[playback-handler] refusing unsupported .m2ts playback source: %q", resolution.WebDAVPath)
	http.Error(w, "unsupported .m2ts playback source", http.StatusUnprocessableEntity)
	return true
}

// SetSubtitleExtractor sets the subtitle extractor for pre-extraction
func (h *PlaybackHandler) SetSubtitleExtractor(extractor SubtitlePreExtractor) {
	h.SubtitleExtractor = extractor
}

// SetVideoProber sets the video prober for probing subtitle streams
func (h *PlaybackHandler) SetVideoProber(prober VideoFullProber) {
	h.VideoProber = prober
}

func (h *PlaybackHandler) SetBadStreamsService(service *badstreams.Service) {
	h.BadStreams = service
}

func (h *PlaybackHandler) SetThumbnailPrewarmer(prewarmer thumbnailPrewarmer) {
	h.ThumbnailPrewarmer = prewarmer
}

// SetTrackPreferenceResolver configures the shared track preference resolver.
func (h *PlaybackHandler) SetTrackPreferenceResolver(resolver *trackselect.Resolver) {
	h.TrackPreferences = resolver
}

// SetUsersService enables profile ownership checks for profileId.
func (h *PlaybackHandler) SetUsersService(svc prequeueOwnershipService) {
	h.Users = svc
}

func (h *PlaybackHandler) canUseProfile(r *http.Request, profileID string) bool {
	if profileID == "" || h.Users == nil || auth.IsMaster(r) {
		return true
	}
	accountID := auth.GetAccountID(r)
	return accountID != "" && h.Users.BelongsToAccount(profileID, accountID)
}

// withTrackSelection returns a copy of resolution carrying the shared track
// selection for the given profile/client/title. The profile is ignored unless
// the caller's account owns it; the client falls back to X-Client-ID.
func (h *PlaybackHandler) withTrackSelection(r *http.Request, resolution *models.PlaybackResolution, profileID, clientID, titleID string) *models.PlaybackResolution {
	if resolution == nil || strings.TrimSpace(resolution.WebDAVPath) == "" {
		return resolution
	}
	profileID = strings.TrimSpace(profileID)
	if !h.canUseProfile(r, profileID) {
		profileID = ""
	}
	clientID = normalizeClientID(clientID)
	if clientID == "" {
		clientID = normalizeClientID(r.Header.Get("X-Client-ID"))
	}
	selection := h.selectTracks(r.Context(), resolution, profileID, clientID, strings.TrimSpace(titleID))
	if selection == nil {
		return resolution
	}
	withTracks := *resolution
	withTracks.TrackSelection = selection
	return &withTracks
}

// selectTracks probes the resolved source (reusing any probe the resolver
// already ran) and applies the shared track selection. It returns nil when the
// source cannot be probed; track selection never fails a resolve.
func (h *PlaybackHandler) selectTracks(ctx context.Context, resolution *models.PlaybackResolution, profileID, clientID, titleID string) *models.TrackSelection {
	if resolution == nil || strings.TrimSpace(resolution.WebDAVPath) == "" {
		return nil
	}
	probe := resolution.Probe
	if probe == nil {
		if h.VideoProber == nil {
			return nil
		}
		probeCtx, cancel := context.WithTimeout(ctx, trackSelectionProbeTimeout)
		defer cancel()
		result, err := h.VideoProber.ProbeVideoFull(probeCtx, resolution.WebDAVPath)
		if err != nil {
			log.Printf("[playback-handler] track selection probe failed (non-fatal): %v", err)
			return nil
		}
		probe = result
	}
	if probe == nil || (len(probe.AudioStreams) == 0 && len(probe.SubtitleStreams) == 0) {
		return nil
	}
	prefs := h.TrackPreferences.Resolve(profileID, clientID, titleID)
	selection := trackselect.Select(probe.AudioStreams, probe.SubtitleStreams, prefs)
	log.Printf("[playback-handler] track selection: audio=%d subtitle=%d (audioLang=%q subLang=%q subMode=%s)",
		selection.AudioStreamIndex(), selection.SubtitleStreamIndex(), prefs.AudioLanguage, prefs.SubtitleLanguage, prefs.SubtitleMode)
	return selection
}

func (h *PlaybackHandler) prewarmThumbnails(resolution *models.PlaybackResolution) {
	if h == nil || h.ThumbnailPrewarmer == nil || resolution == nil || strings.TrimSpace(resolution.WebDAVPath) == "" {
		return
	}
	h.ThumbnailPrewarmer.PrewarmThumbnails(resolution.WebDAVPath)
}

// Resolve accepts an NZB indexer result and responds with a validated playback source.
func (h *PlaybackHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result         models.NZBResult `json:"result"`
		StartOffset    float64          `json:"startOffset,omitempty"` // Seek position in seconds for subtitle extraction
		ProfileID      string           `json:"profileId,omitempty"`
		AllowMarkedBad bool             `json:"allowMarkedBad,omitempty"`
		// IncludeTrackSelection asks the server to probe the resolved source and
		// return its audio/subtitle choice (trackSelection) for this profile.
		IncludeTrackSelection bool   `json:"includeTrackSelection,omitempty"`
		TitleID               string `json:"titleId,omitempty"`  // per-title language preference key
		ClientID              string `json:"clientId,omitempty"` // per-device overrides; falls back to X-Client-ID
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	handlerStart := time.Now()
	log.Printf("[playback-handler] TIMING: Received resolve request: Title=%q, GUID=%q, ServiceType=%q, titleId=%q, titleName=%q, startOffset=%.2f",
		request.Result.Title, request.Result.GUID, request.Result.ServiceType,
		request.Result.Attributes["titleId"], request.Result.Attributes["titleName"], request.StartOffset)

	if request.ProfileID != "" {
		if request.Result.Attributes == nil {
			request.Result.Attributes = map[string]string{}
		}
		request.Result.Attributes["profileId"] = request.ProfileID
	}
	if h.BadStreams != nil {
		if match := h.BadStreams.MatchFor(request.Result, reportContextForResult(h.TrackPreferences, request.Result, request.ProfileID, firstNonEmpty(request.ClientID, normalizeClientID(r.Header.Get("X-Client-ID"))), request.TitleID)); match != nil {
			if request.AllowMarkedBad {
				log.Printf("[playback-handler] manual override for marked bad stream service=%q provider=%q release=%q badStreamID=%q", request.Result.ServiceType, request.Result.Attributes["provider"], request.Result.Title, match.ID)
			} else {
				log.Printf("[playback-handler] refusing marked bad stream service=%q provider=%q release=%q", request.Result.ServiceType, request.Result.Attributes["provider"], request.Result.Title)
				http.Error(w, "stream is marked bad", http.StatusConflict)
				return
			}
		}
	}

	resolution, err := h.Service.Resolve(r.Context(), request.Result)
	if err != nil {
		log.Printf("[playback-handler] TIMING: resolve failed after %v: %v", time.Since(handlerStart), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if rejectM2TSPlaybackResolution(w, resolution) {
		return
	}
	rememberStreamRelease(resolution.WebDAVPath, request.Result)
	h.prewarmThumbnails(resolution)
	log.Printf("[playback-handler] TIMING: resolve complete (took: %v)", time.Since(handlerStart))

	if request.IncludeTrackSelection {
		resolution = h.withTrackSelection(r, resolution, request.ProfileID, request.ClientID, firstNonEmpty(request.TitleID, request.Result.Attributes["titleId"]))
	}

	if !request.AllowMarkedBad {
		ctx := reportContextForResult(h.TrackPreferences, request.Result, request.ProfileID, firstNonEmpty(request.ClientID, normalizeClientID(r.Header.Get("X-Client-ID"))), request.TitleID)
		resolution, err = h.filterResolvedReportedAudio(r, request.Result, resolution, ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}

	// Subtitle pre-extraction disabled — the player handles subtitles natively.
	// The old extraction path opened concurrent connections to the streaming provider,
	// which caused TCP socket exhaustion and playback failures.
	// if h.SubtitleExtractor != nil && h.VideoProber != nil && resolution.WebDAVPath != "" { ... }

	log.Printf("[playback-handler] TIMING: handler complete (TOTAL: %v)", time.Since(handlerStart))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resolution)
}

// ResolveBatch performs a single set of provider API calls and resolves all episodes from a pack.
func (h *PlaybackHandler) ResolveBatch(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result   models.NZBResult            `json:"result"`
		Episodes []models.BatchEpisodeTarget `json:"episodes"`
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(request.Episodes) == 0 {
		http.Error(w, "episodes list is empty", http.StatusBadRequest)
		return
	}
	if len(request.Episodes) > 100 {
		http.Error(w, "batch size exceeds maximum of 100 episodes", http.StatusBadRequest)
		return
	}

	handlerStart := time.Now()
	log.Printf("[playback-handler] batch resolve: %d episodes, title=%q", len(request.Episodes), request.Result.Title)

	resp, err := h.Service.ResolveBatch(r.Context(), request.Result, request.Episodes)
	if err != nil {
		log.Printf("[playback-handler] batch resolve failed after %v: %v", time.Since(handlerStart), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	log.Printf("[playback-handler] batch resolve complete (took: %v)", time.Since(handlerStart))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// QueueStatus reports the current resolution status for a previously queued playback request.
func (h *PlaybackHandler) QueueStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	queueIDStr := vars["queueID"]
	queueID, err := strconv.ParseInt(queueIDStr, 10, 64)
	if err != nil || queueID <= 0 {
		http.Error(w, "invalid queue id", http.StatusBadRequest)
		return
	}

	status, err := h.Service.QueueStatus(r.Context(), queueID)
	if err != nil {
		switch {
		case errors.Is(err, playbacksvc.ErrQueueItemNotFound):
			http.Error(w, "queue item not found", http.StatusNotFound)
		case errors.Is(err, playbacksvc.ErrQueueItemFailed):
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
		return
	}
	if rejectM2TSPlaybackResolution(w, status) {
		return
	}
	h.prewarmThumbnails(status)

	// Queued resolves report readiness here, so honor the same opt-in.
	query := r.URL.Query()
	if include, _ := strconv.ParseBool(query.Get("includeTrackSelection")); include {
		status = h.withTrackSelection(r, status, query.Get("profileId"), query.Get("clientId"), query.Get("titleId"))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"novastream/models"
	playbacksvc "novastream/services/playback"
)

type playbackService interface {
	Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
	QueueStatus(ctx context.Context, queueID int64) (*models.PlaybackResolution, error)
}

// PlaybackHandler resolves NZB candidates into playable streams via the local registry.
type PlaybackHandler struct {
	Service           playbackService
	SubtitleExtractor SubtitlePreExtractor // For pre-extracting subtitles
	VideoProber       VideoFullProber      // For probing subtitle streams
}

var _ playbackService = (*playbacksvc.Service)(nil)

func NewPlaybackHandler(s playbackService) *PlaybackHandler {
	return &PlaybackHandler{Service: s}
}

// SetSubtitleExtractor sets the subtitle extractor for pre-extraction
func (h *PlaybackHandler) SetSubtitleExtractor(extractor SubtitlePreExtractor) {
	h.SubtitleExtractor = extractor
}

// SetVideoProber sets the video prober for probing subtitle streams
func (h *PlaybackHandler) SetVideoProber(prober VideoFullProber) {
	h.VideoProber = prober
}

// Resolve accepts an NZB indexer result and responds with a validated playback source.
func (h *PlaybackHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result models.NZBResult `json:"result"`
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[playback-handler] Received resolve request: Title=%q, GUID=%q, ServiceType=%q, titleId=%q, titleName=%q",
		request.Result.Title, request.Result.GUID, request.Result.ServiceType,
		request.Result.Attributes["titleId"], request.Result.Attributes["titleName"])

	resolution, err := h.Service.Resolve(r.Context(), request.Result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Pre-extract subtitles for direct streaming (non-HLS) path
	if h.SubtitleExtractor != nil && h.VideoProber != nil && resolution.WebDAVPath != "" {
		log.Printf("[playback-handler] Probing subtitle streams for pre-extraction")
		probeResult, probeErr := h.VideoProber.ProbeVideoFull(r.Context(), resolution.WebDAVPath)
		if probeErr != nil {
			log.Printf("[playback-handler] Probe failed (non-fatal): %v", probeErr)
		} else if probeResult != nil && len(probeResult.SubtitleStreams) > 0 {
			// Check if this is HDR/DV content (which uses HLS instead of direct streaming)
			needsHLS := probeResult.HasDolbyVision || probeResult.HasHDR10 || probeResult.HasTrueHD
			if !needsHLS {
				log.Printf("[playback-handler] Starting subtitle pre-extraction for %d tracks", len(probeResult.SubtitleStreams))

				// Convert to SubtitleTrackInfo format
				// NOTE: Index must be relative (0, 1, 2) not absolute ffprobe index (3, 4, 5)
				// The extraction code maps relative -> absolute using its own probe
				tracks := make([]SubtitleTrackInfo, len(probeResult.SubtitleStreams))
				for i, s := range probeResult.SubtitleStreams {
					tracks[i] = SubtitleTrackInfo{
						Index:    i, // Use relative index, not s.Index (absolute ffprobe index)
						Language: s.Language,
						Title:    s.Title,
						Codec:    s.Codec,
						Forced:   s.IsForced,
					}
				}

				sessions := h.SubtitleExtractor.StartPreExtraction(r.Context(), resolution.WebDAVPath, tracks)

				// Convert sessions to SubtitleSessionInfo and store in resolution
				// Keys are relative indices (0, 1, 2) matching what frontend expects
				sessionInfos := make(map[int]*models.SubtitleSessionInfo)
				for relativeIdx, session := range sessions {
					// relativeIdx is 0-based subtitle index, use directly to access subtitleStreams
					if relativeIdx < 0 || relativeIdx >= len(probeResult.SubtitleStreams) {
						continue
					}
					stream := &probeResult.SubtitleStreams[relativeIdx]
					sessionInfos[relativeIdx] = &models.SubtitleSessionInfo{
						SessionID:    session.ID,
						VTTUrl:       "/api/video/subtitles/" + session.ID + "/subtitles.vtt",
						TrackIndex:   relativeIdx,
						Language:     stream.Language,
						Title:        stream.Title,
						Codec:        stream.Codec,
						IsForced:     stream.IsForced,
						IsExtracting: !session.IsExtractionComplete(),
					}
				}
				resolution.SubtitleSessions = sessionInfos
				log.Printf("[playback-handler] Pre-extraction started for %d subtitle sessions", len(sessionInfos))
			} else {
				log.Printf("[playback-handler] HDR/DV content detected, skipping subtitle pre-extraction (will use HLS)")
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resolution)
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"novastream/models"
	playbacksvc "novastream/services/playback"
)

type playbackService interface {
	Resolve(ctx context.Context, candidate models.NZBResult) (*models.PlaybackResolution, error)
	ResolveBatch(ctx context.Context, candidate models.NZBResult, episodes []models.BatchEpisodeTarget) (*models.BatchResolveResponse, error)
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
		Result      models.NZBResult `json:"result"`
		StartOffset float64          `json:"startOffset,omitempty"` // Seek position in seconds for subtitle extraction
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	handlerStart := time.Now()
	log.Printf("[playback-handler] TIMING: Received resolve request: Title=%q, GUID=%q, ServiceType=%q, titleId=%q, titleName=%q, startOffset=%.2f",
		request.Result.Title, request.Result.GUID, request.Result.ServiceType,
		request.Result.Attributes["titleId"], request.Result.Attributes["titleName"], request.StartOffset)

	resolution, err := h.Service.Resolve(r.Context(), request.Result)
	if err != nil {
		log.Printf("[playback-handler] TIMING: resolve failed after %v: %v", time.Since(handlerStart), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("[playback-handler] TIMING: resolve complete (took: %v)", time.Since(handlerStart))

	// Pre-extract subtitles for direct streaming (non-HLS) path
	if h.SubtitleExtractor != nil && h.VideoProber != nil && resolution.WebDAVPath != "" {
		log.Printf("[playback-handler] Probing subtitle streams for pre-extraction")
		probeResult, probeErr := h.VideoProber.ProbeVideoFull(r.Context(), resolution.WebDAVPath)
		if probeErr != nil {
			log.Printf("[playback-handler] Probe failed (non-fatal): %v", probeErr)
		} else if probeResult != nil && len(probeResult.SubtitleStreams) > 0 {
			// Check if this is DV/HDR10 content (which requires HLS for video transcoding)
			// Note: TrueHD audio alone doesn't require HLS - player can handle it natively
			// So we still pre-extract subtitles for TrueHD content
			needsHLS := probeResult.HasDolbyVision || probeResult.HasHDR10
			if !needsHLS {
				// Use background context so extraction continues after HTTP response is sent
				// The request context would cancel extraction when the response completes
				resolution.SubtitleSessions = StartSubtitleExtraction(
					context.Background(),
					h.SubtitleExtractor,
					resolution.WebDAVPath,
					probeResult.SubtitleStreams,
					request.StartOffset,
				)
			} else {
				log.Printf("[playback-handler] DV/HDR10 content detected, skipping subtitle pre-extraction (will use HLS sidecar)")
			}
		}
	}

	log.Printf("[playback-handler] TIMING: handler complete (TOTAL: %v)", time.Since(handlerStart))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resolution)
}

// ResolveBatch performs a single set of provider API calls and resolves all episodes from a pack.
func (h *PlaybackHandler) ResolveBatch(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Result   models.NZBResult           `json:"result"`
		Episodes []models.BatchEpisodeTarget `json:"episodes"`
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

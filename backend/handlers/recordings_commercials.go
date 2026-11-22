package handlers

import (
	"net/http"
	"strings"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/skipsegments"
)

// SegmentTypeCommercial is the /video/segments type of a commercial break.
const SegmentTypeCommercial = "commercial"

// segmentSourceComskip marks segments from comskip's analysis.
const segmentSourceComskip = "comskip"

// SetCommercials gives the handler comskip's commercial breaks per recording
// file (nil when comskip is off or found none).
func (h *RecordingsHandler) SetCommercials(fn func(outputPath string) []models.CommercialBreak) {
	h.commercials = fn
}

// CommercialSegments returns a recording's commercial breaks as skip
// segments for /video/segments, when the request may play the recording (the
// master, or an account owning the recording's profile, as for its stream).
// It never fails: an unknown, foreign or unanalysed recording has none.
func (h *RecordingsHandler) CommercialSegments(r *http.Request, recordingID string) []skipsegments.Segment {
	if h == nil || h.service == nil || h.commercials == nil {
		return nil
	}
	recording, err := h.service.Get(strings.TrimSpace(recordingID))
	if err != nil || recording == nil {
		return nil
	}
	if !auth.IsMaster(r) {
		accountID := auth.GetAccountID(r)
		profileID := strings.TrimSpace(recording.UserID)
		if accountID == "" || profileID == "" || h.users == nil || !h.users.BelongsToAccount(profileID, accountID) {
			return nil
		}
	}
	breaks := h.commercials(recording.OutputPath)
	segments := make([]skipsegments.Segment, 0, len(breaks))
	for _, b := range breaks {
		if b.End > b.Start {
			segments = append(segments, skipsegments.Segment{Type: SegmentTypeCommercial, Start: b.Start, End: b.End, Source: segmentSourceComskip})
		}
	}
	return segments
}

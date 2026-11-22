package recordings

import (
	"context"
	"log"
	"strings"
	"time"

	"novastream/models"
)

// takenAiringWindow is how far apart two recordings of one channel may start
// and still be the same airing.
const takenAiringWindow = time.Minute

// takenAiring is an airing a profile already records.
type takenAiring struct {
	channelID, tvgID string
	start            time.Time
	ruleID           string
}

// takenAiringsLookback bounds how far back takenAirings looks.
const takenAiringsLookback = 24 * time.Hour

type takenAirings []takenAiring

// takenAirings lists the profile's recordings that are scheduled, running or
// done, so a rule does not record an airing twice: a series recording
// created after a single recording of the current episode (Jellyfin's
// record, then record series) would otherwise record it in parallel.
func (s *Service) takenAirings(ctx context.Context, profileID string) takenAirings {
	// Rules only schedule airings from about now on: older recordings can't
	// collide, and the profile's whole history would grow every pass.
	since := time.Now().Add(-takenAiringsLookback)
	recs, err := s.repo.List(ctx, models.RecordingListFilter{
		UserID:         profileID,
		OnlyStartAfter: &since,
		Statuses: []models.RecordingStatus{
			models.RecordingStatusPending, models.RecordingStatusStarting,
			models.RecordingStatusRunning, models.RecordingStatusCompleted,
		},
	})
	if err != nil {
		log.Printf("[recordings] list recordings for rule airings failed profile=%s: %v", profileID, err)
		return nil
	}
	taken := make(takenAirings, 0, len(recs))
	for _, rec := range recs {
		switch rec.Status {
		case models.RecordingStatusCancelled, models.RecordingStatusFailed:
			continue // a cancelled single recording leaves the airing to the rule
		}
		taken.add(rec)
	}
	return taken
}

func (t *takenAirings) add(rec models.Recording) {
	if t == nil {
		return
	}
	*t = append(*t, takenAiring{channelID: rec.ChannelID, tvgID: strings.ToLower(strings.TrimSpace(rec.TvgID)), start: rec.StartAt, ruleID: rec.RuleID})
}

// covers reports whether the airing on channel at start is already recorded
// by something other than ruleID (whose own airing is updated in place).
func (t takenAirings) covers(channel Channel, start time.Time, ruleID string) bool {
	tvgID := strings.ToLower(strings.TrimSpace(channel.TvgID))
	for _, a := range t {
		if a.ruleID == ruleID {
			continue
		}
		sameChannel := a.channelID == channel.ID || (tvgID != "" && a.tvgID == tvgID)
		if d := a.start.Sub(start); sameChannel && d > -takenAiringWindow && d < takenAiringWindow {
			return true
		}
	}
	return false
}

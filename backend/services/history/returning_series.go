package history

import (
	"strings"
	"time"

	"novastream/models"
)

// continueWatchingReturningSeriesWindow is how far back a series' last watch
// may be for a newly aired season to bring it back to continue watching. Shows
// with a yearly (or slower) cadence fall outside the regular 365-day window
// between seasons.
const continueWatchingReturningSeriesWindow = 3 * 365 * 24 * time.Hour

// returningSeriesCandidates returns the series, keyed by canonical series ID,
// whose newest watched episode is older than recentCutoff but within the
// returning-series window, mapped to that newest episode. Series already on
// the recent list (present in recent) are skipped.
func returningSeriesCandidates(lifetime map[string][]models.WatchHistoryItem, recent map[string]models.WatchHistoryItem, recentCutoff time.Time) map[string]models.WatchHistoryItem {
	oldest := time.Now().UTC().Add(-continueWatchingReturningSeriesWindow)
	out := make(map[string]models.WatchHistoryItem)
	for seriesID, episodes := range lifetime {
		if _, ok := recent[seriesID]; ok {
			continue
		}
		var newest models.WatchHistoryItem
		found := false
		for _, ep := range episodes {
			if !ep.Watched || ep.SeasonNumber <= 0 {
				continue
			}
			if !found || ep.WatchedAt.After(newest.WatchedAt) {
				newest = ep
				found = true
			}
		}
		if !found || newest.WatchedAt.After(recentCutoff) || newest.WatchedAt.Before(oldest) {
			continue
		}
		out[seriesID] = newest
	}
	return out
}

// seriesMayReturn reports whether a series can still get new episodes: any
// lifecycle except ended or cancelled (TVDB "Ended", TMDB "Ended"/"Canceled").
// An unknown lifecycle counts as returning.
func seriesMayReturn(details *models.SeriesDetails) bool {
	if details == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(details.Title.LifecycleStatus)) {
	case "ended", "canceled", "cancelled":
		return false
	}
	return true
}

// episodeAiredAfter reports whether ep has a known air time after t.
func episodeAiredAfter(ep *models.EpisodeReference, t time.Time) bool {
	if ep == nil {
		return false
	}
	if ts := strings.TrimSpace(ep.AirDateTimeUTC); ts != "" {
		if at, err := time.Parse(time.RFC3339, ts); err == nil {
			return at.After(t)
		}
	}
	if d := strings.TrimSpace(ep.AirDate); d != "" {
		if at, err := time.Parse("2006-01-02", d); err == nil {
			return at.After(t)
		}
	}
	return false
}

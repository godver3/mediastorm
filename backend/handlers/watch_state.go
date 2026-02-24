package handlers

import (
	"novastream/models"
)

// watchStateIndex provides O(1) lookups for watch state computation.
// Built with a single pass through each data source (3 × O(n)).
type watchStateIndex struct {
	movies map[string]movieState
	series map[string]seriesState
}

type movieState struct {
	watched bool
	percent float64
}

type seriesState struct {
	markedWatched   bool
	hasWatchedEps   bool
	allEpsWatched   bool
	totalEpisodes   int
	watchedEpisodes int
	hasProgress     bool
}

// buildWatchStateIndex creates a watchStateIndex from the three data sources
// the backend already has in memory.
func buildWatchStateIndex(
	watchHistory []models.WatchHistoryItem,
	continueWatching []models.SeriesWatchState,
	playbackProgress []models.PlaybackProgress,
) *watchStateIndex {
	idx := &watchStateIndex{
		movies: make(map[string]movieState),
		series: make(map[string]seriesState),
	}

	// Pass 1: Watch history — determine watched flags
	for _, wh := range watchHistory {
		switch wh.MediaType {
		case "movie":
			m := idx.movies[wh.ItemID]
			if wh.Watched {
				m.watched = true
			}
			idx.movies[wh.ItemID] = m
		case "series":
			s := idx.series[wh.ItemID]
			if wh.Watched {
				s.markedWatched = true
			}
			idx.series[wh.ItemID] = s
		case "episode":
			if wh.SeriesID != "" && wh.Watched {
				// Only count non-special episodes (season > 0)
				if wh.SeasonNumber > 0 {
					s := idx.series[wh.SeriesID]
					s.hasWatchedEps = true
					idx.series[wh.SeriesID] = s
				}
			}
		}
	}

	// Pass 2: Continue watching — episode counts and progress
	for _, cw := range continueWatching {
		s := idx.series[cw.SeriesID]
		s.totalEpisodes = cw.TotalEpisodeCount
		s.watchedEpisodes = cw.WatchedEpisodeCount
		if s.totalEpisodes > 0 && s.watchedEpisodes >= s.totalEpisodes {
			s.allEpsWatched = true
		}
		if cw.PercentWatched > 0 || cw.ResumePercent > 0 ||
			cw.WatchedEpisodeCount > 0 ||
			len(cw.WatchedEpisodes) > 0 {
			s.hasProgress = true
		}
		idx.series[cw.SeriesID] = s
	}

	// Pass 3: Playback progress — movie percent watched
	for _, pp := range playbackProgress {
		if pp.MediaType == "movie" {
			itemID := pp.ItemID
			if itemID == "" {
				itemID = pp.ID
			}
			if itemID != "" {
				m := idx.movies[itemID]
				if pp.PercentWatched > m.percent {
					m.percent = pp.PercentWatched
				}
				idx.movies[itemID] = m
			}
		}
	}

	return idx
}

// compute returns the watch state and optional unwatched count for a given item.
// This mirrors the frontend enrichWithWatchStatus logic exactly.
func (idx *watchStateIndex) compute(mediaType, itemID string) (string, *int) {
	if mediaType == "movie" {
		m := idx.movies[itemID]
		if m.watched || m.percent >= 90 {
			return "complete", nil
		}
		if m.percent > 0 {
			return "partial", nil
		}
		return "none", nil
	}
	if mediaType == "series" {
		s := idx.series[itemID]
		unwatched := s.totalEpisodes - s.watchedEpisodes
		var unwatchedPtr *int
		if s.totalEpisodes > 0 {
			unwatchedPtr = intPtr(unwatched)
		}
		if s.markedWatched || s.allEpsWatched {
			return "complete", unwatchedPtr
		}
		if s.hasWatchedEps || s.hasProgress {
			return "partial", unwatchedPtr
		}
		return "none", nil
	}
	return "none", nil
}

func intPtr(v int) *int {
	return &v
}

// enrichWatchlistItems sets WatchState and UnwatchedCount on watchlist items in-place.
func enrichWatchlistItems(items []models.WatchlistItem, idx *watchStateIndex) {
	for i := range items {
		items[i].WatchState, items[i].UnwatchedCount = idx.compute(items[i].MediaType, items[i].ID)
	}
}

// enrichTrendingItems sets WatchState and UnwatchedCount on trending items in-place.
func enrichTrendingItems(items []models.TrendingItem, idx *watchStateIndex) {
	for i := range items {
		itemID := buildItemIDForHistory(items[i])
		if itemID == "" {
			itemID = items[i].Title.ID
		}
		items[i].Title.WatchState, items[i].Title.UnwatchedCount = idx.compute(items[i].Title.MediaType, itemID)
	}
}

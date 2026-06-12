package handlers

import (
	"strconv"

	"novastream/internal/mediaidentity"
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
			identity := mediaidentity.Resolve(mediaidentity.Input{
				MediaType:   wh.MediaType,
				ID:          wh.ItemID,
				ExternalIDs: wh.ExternalIDs,
			})
			for _, key := range identity.IndexKeys() {
				m := idx.movies[key]
				if wh.Watched {
					m.watched = true
				}
				idx.movies[key] = m
			}
		case "series":
			identity := mediaidentity.Resolve(mediaidentity.Input{
				MediaType:   wh.MediaType,
				ID:          wh.ItemID,
				ExternalIDs: wh.ExternalIDs,
			})
			for _, key := range identity.IndexKeys() {
				s := idx.series[key]
				if wh.Watched {
					s.markedWatched = true
				}
				idx.series[key] = s
			}
		case "episode":
			if wh.SeriesID != "" && wh.Watched {
				// Only count non-special episodes (season > 0)
				if wh.SeasonNumber > 0 {
					identity := mediaidentity.Resolve(mediaidentity.Input{
						MediaType:   "series",
						ID:          wh.SeriesID,
						ExternalIDs: wh.ExternalIDs,
					})
					for _, key := range identity.IndexKeys() {
						s := idx.series[key]
						s.hasWatchedEps = true
						idx.series[key] = s
					}
				}
			}
		}
	}

	// Pass 2: Continue watching — episode counts and progress
	for _, cw := range continueWatching {
		identity := mediaidentity.Resolve(mediaidentity.Input{
			MediaType:   "series",
			ID:          cw.SeriesID,
			ExternalIDs: cw.ExternalIDs,
		})
		for _, key := range identity.IndexKeys() {
			s := idx.series[key]
			if cw.TotalEpisodeCount > s.totalEpisodes {
				s.totalEpisodes = cw.TotalEpisodeCount
			}
			if cw.WatchedEpisodeCount > s.watchedEpisodes {
				s.watchedEpisodes = cw.WatchedEpisodeCount
			}
			if cw.PercentWatched > 0 || cw.ResumePercent > 0 ||
				cw.WatchedEpisodeCount > 0 ||
				len(cw.WatchedEpisodes) > 0 {
				s.hasProgress = true
			}
			idx.series[key] = s
		}
	}

	// Pass 3: Playback progress — movie percent watched
	for _, pp := range playbackProgress {
		if pp.MediaType == "movie" {
			identity := mediaidentity.Resolve(mediaidentity.Input{
				MediaType:   pp.MediaType,
				ID:          firstNonEmpty(pp.ItemID, pp.ID),
				ExternalIDs: pp.ExternalIDs,
			})
			for _, key := range identity.IndexKeys() {
				m := idx.movies[key]
				if pp.PercentWatched > m.percent {
					m.percent = pp.PercentWatched
				}
				idx.movies[key] = m
			}
		}
	}

	return idx
}

// compute returns the watch state and optional unwatched count for a given item.
// This mirrors the frontend enrichWithWatchStatus logic exactly.
func (idx *watchStateIndex) compute(mediaType, itemID string) (string, *int) {
	return idx.computeWithExternalIDs(mediaType, itemID, nil)
}

func (idx *watchStateIndex) computeWithExternalIDs(mediaType, itemID string, externalIDs map[string]string) (string, *int) {
	mediaType = mediaidentity.NormalizeMediaType(mediaType)
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   mediaType,
		ID:          itemID,
		ExternalIDs: externalIDs,
	})
	if mediaType == "movie" {
		var m movieState
		for _, key := range identity.IndexKeys() {
			candidate := idx.movies[key]
			if candidate.watched {
				m.watched = true
			}
			if candidate.percent > m.percent {
				m.percent = candidate.percent
			}
		}
		if m.watched || m.percent >= 90 {
			return "complete", nil
		}
		if m.percent >= 5 {
			return "partial", nil
		}
		return "none", nil
	}
	if mediaType == "series" {
		var s seriesState
		for _, key := range identity.IndexKeys() {
			candidate := idx.series[key]
			s.markedWatched = s.markedWatched || candidate.markedWatched
			s.hasWatchedEps = s.hasWatchedEps || candidate.hasWatchedEps
			s.hasProgress = s.hasProgress || candidate.hasProgress
			if candidate.totalEpisodes > s.totalEpisodes {
				s.totalEpisodes = candidate.totalEpisodes
			}
			if candidate.watchedEpisodes > s.watchedEpisodes {
				s.watchedEpisodes = candidate.watchedEpisodes
			}
		}
		allEpsWatched := s.totalEpisodes > 0 && s.watchedEpisodes >= s.totalEpisodes
		unwatched := s.totalEpisodes - s.watchedEpisodes
		var unwatchedPtr *int
		if s.totalEpisodes > 0 {
			unwatchedPtr = intPtr(unwatched)
		}
		if s.markedWatched || allEpsWatched {
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
		items[i].WatchState, items[i].UnwatchedCount = idx.computeWithExternalIDs(items[i].MediaType, items[i].ID, items[i].ExternalIDs)
	}
}

// enrichTrendingItems sets WatchState and UnwatchedCount on trending items in-place.
func enrichTrendingItems(items []models.TrendingItem, idx *watchStateIndex) {
	for i := range items {
		itemID := buildItemIDForHistory(items[i])
		if itemID == "" {
			itemID = items[i].Title.ID
		}
		items[i].Title.WatchState, items[i].Title.UnwatchedCount = idx.computeWithExternalIDs(
			items[i].Title.MediaType,
			itemID,
			titleWatchStateExternalIDs(items[i].Title),
		)
	}
}

func titleWatchStateExternalIDs(title models.Title) map[string]string {
	ids := make(map[string]string, 3)
	if title.IMDBID != "" {
		ids["imdb"] = title.IMDBID
	}
	if title.TMDBID > 0 {
		ids["tmdb"] = int64String(title.TMDBID)
	}
	if title.TVDBID > 0 {
		ids["tvdb"] = int64String(title.TVDBID)
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

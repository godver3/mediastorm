package handlers

import (
	"strconv"
	"strings"

	"novastream/internal/mediaidentity"
	"novastream/models"
	"novastream/services/history"
)

func stringSliceContainsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

// watchStateIndex provides O(1) lookups for watch state computation.
// Built with a single pass through each data source (3 × O(n)).
type watchStateIndex struct {
	movies map[string]movieState
	series map[string]seriesState
	// Watched regular-season episode keys ("season:episode") per series index
	// key. A title's count is the union across all of its keys, so history
	// split across tmdb-/tvdb-/imdb-form rows is not undercounted.
	episodes map[string]map[string]struct{}
	// Rows whose only series ID is a bare number (legacy imports) have no
	// unambiguous index key; they are matched only against a title's own bare
	// candidate keys, the same fallback the details page uses.
	legacyEpisodes map[string]map[string]struct{}
}

type releasedEpisodeCountProvider interface {
	GetCachedReleasedEpisodeCount(models.SeriesDetailsQuery) (int, bool)
	WarmReleasedEpisodeCount(models.SeriesDetailsQuery)
}

type movieState struct {
	watched  bool
	percent  float64
	position float64
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
		movies:         make(map[string]movieState),
		series:         make(map[string]seriesState),
		episodes:       make(map[string]map[string]struct{}),
		legacyEpisodes: make(map[string]map[string]struct{}),
	}
	addEpisode := func(sets map[string]map[string]struct{}, key, episodeKey string) {
		if sets[key] == nil {
			sets[key] = make(map[string]struct{})
		}
		sets[key][episodeKey] = struct{}{}
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
					episodeKey := strconv.Itoa(wh.SeasonNumber) + ":" + strconv.Itoa(wh.EpisodeNumber)
					indexKeys := identity.IndexKeys()
					for _, key := range indexKeys {
						s := idx.series[key]
						s.hasWatchedEps = true
						idx.series[key] = s
						addEpisode(idx.episodes, key, episodeKey)
					}
					if len(indexKeys) == 0 {
						for _, key := range identity.CandidateKeys {
							addEpisode(idx.legacyEpisodes, key, episodeKey)
						}
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
				if pp.Position > m.position {
					m.position = pp.Position
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
			if candidate.position > m.position {
				m.position = candidate.position
			}
		}
		if m.watched || m.percent >= 90 {
			return "complete", nil
		}
		if history.MeetsResumeFloor(m.position, m.percent) {
			return "partial", nil
		}
		return "none", nil
	}
	if mediaType == "series" {
		var s seriesState
		watched := make(map[string]struct{})
		union := func(set map[string]struct{}) {
			for episodeKey := range set {
				watched[episodeKey] = struct{}{}
			}
		}
		for _, key := range identity.CandidateKeys {
			union(idx.legacyEpisodes[key])
		}
		for _, key := range identity.IndexKeys() {
			union(idx.episodes[key])
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
		if len(watched) > 0 {
			s.hasWatchedEps = true
		}
		if len(watched) > s.watchedEpisodes {
			s.watchedEpisodes = len(watched)
		}
		allEpsWatched := s.totalEpisodes > 0 && s.watchedEpisodes >= s.totalEpisodes
		unwatched := s.totalEpisodes - s.watchedEpisodes
		if unwatched < 0 {
			unwatched = 0
		}
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
		return "none", unwatchedPtr
	}
	return "none", nil
}

func (idx *watchStateIndex) setSeriesEpisodeTotal(itemID string, externalIDs map[string]string, total int) {
	if idx == nil || total <= 0 {
		return
	}
	identity := mediaidentity.Resolve(mediaidentity.Input{
		MediaType:   "series",
		ID:          itemID,
		ExternalIDs: externalIDs,
	})
	for _, key := range identity.IndexKeys() {
		s := idx.series[key]
		if total > s.totalEpisodes {
			s.totalEpisodes = total
		}
		idx.series[key] = s
	}
}

func seriesEpisodeCountQuery(item models.WatchlistItem) models.SeriesDetailsQuery {
	query := models.SeriesDetailsQuery{
		TitleID: item.ID,
		Name:    item.Name,
		Year:    item.Year,
		IMDBID:  item.ExternalIDs["imdb"],
	}
	query.TMDBID, _ = strconv.ParseInt(item.ExternalIDs["tmdb"], 10, 64)
	query.TVDBID, _ = strconv.ParseInt(item.ExternalIDs["tvdb"], 10, 64)
	return query
}

// addCachedWatchlistEpisodeTotals enriches the index exclusively from local
// metadata cache hits. Misses are warmed in the background for a later normal
// response, keeping the current request free of provider calls.
func addCachedWatchlistEpisodeTotals(
	items []models.WatchlistItem,
	idx *watchStateIndex,
	metadata any,
	warmMisses bool,
) {
	provider, ok := metadata.(releasedEpisodeCountProvider)
	if !ok || idx == nil {
		return
	}
	for _, item := range items {
		if mediaidentity.NormalizeMediaType(item.MediaType) != "series" {
			continue
		}
		query := seriesEpisodeCountQuery(item)
		if total, cached := provider.GetCachedReleasedEpisodeCount(query); cached {
			idx.setSeriesEpisodeTotal(item.ID, item.ExternalIDs, total)
			continue
		}
		if warmMisses {
			provider.WarmReleasedEpisodeCount(query)
		}
	}
}

// addCachedTrendingEpisodeTotals mirrors addCachedWatchlistEpisodeTotals for
// metadata-backed shelves, whose watch-state fields live on TrendingItem.Title.
// Provider misses remain asynchronous and opt-in so discovery requests do not
// block on, or unconditionally multiply, upstream metadata calls.
func addCachedTrendingEpisodeTotals(
	items []models.TrendingItem,
	idx *watchStateIndex,
	metadata any,
	warmMisses bool,
) {
	provider, ok := metadata.(releasedEpisodeCountProvider)
	if !ok || idx == nil {
		return
	}
	for i := range items {
		title := items[i].Title
		if mediaidentity.NormalizeMediaType(title.MediaType) != "series" {
			continue
		}
		itemID := buildItemIDForHistory(items[i])
		if itemID == "" {
			itemID = title.ID
		}
		query := models.SeriesDetailsQuery{
			TitleID: itemID,
			Name:    title.Name,
			Year:    title.Year,
			TMDBID:  title.TMDBID,
			TVDBID:  title.TVDBID,
			IMDBID:  title.IMDBID,
		}
		externalIDs := titleWatchStateExternalIDs(title)
		if total, cached := provider.GetCachedReleasedEpisodeCount(query); cached {
			idx.setSeriesEpisodeTotal(itemID, externalIDs, total)
			continue
		}
		if warmMisses {
			provider.WarmReleasedEpisodeCount(query)
		}
	}
}

func enrichSearchResultsWithCachedEpisodeCounts(
	results []models.SearchResult,
	idx *watchStateIndex,
	metadata any,
) {
	items := make([]models.TrendingItem, len(results))
	for i := range results {
		items[i].Title = results[i].Title
	}
	enrichTrendingItems(items, idx, metadata, false)
	for i := range results {
		results[i].Title = items[i].Title
	}
}

func intPtr(v int) *int {
	return &v
}

// enrichWatchlistItems sets WatchState and UnwatchedCount on watchlist items in-place.
func enrichWatchlistItems(items []models.WatchlistItem, idx *watchStateIndex, metadata any, warmEpisodeCounts bool) {
	addCachedWatchlistEpisodeTotals(items, idx, metadata, warmEpisodeCounts)
	for i := range items {
		items[i].WatchState, items[i].UnwatchedCount = idx.computeWithExternalIDs(items[i].MediaType, items[i].ID, items[i].ExternalIDs)
	}
}

// enrichTrendingItems sets WatchState and UnwatchedCount on trending items in-place.
func enrichTrendingItems(items []models.TrendingItem, idx *watchStateIndex, metadata any, warmEpisodeCounts bool) {
	addCachedTrendingEpisodeTotals(items, idx, metadata, warmEpisodeCounts)
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

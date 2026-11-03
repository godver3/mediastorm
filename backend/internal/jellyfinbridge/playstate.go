package jellyfinbridge

import (
	"context"
	"strings"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// Played state of series and seasons: Jellyfin clients show a check mark on a
// series or season whose episodes are all played and an unplayed count
// otherwise, both from UserData (Played, UnplayedItemCount). mediastorm keeps
// played state per episode, so the bridge counts.

const (
	seriesPlayStateWait = 10 * time.Second
	seriesCountTTL      = 10 * time.Minute
)

// seriesCount is a cached episode count of a played series.
type seriesCount struct {
	total, unplayed int
	at              time.Time
}

// episodePlayed reports whether the episode with this user data key is played,
// with the same rules as an episode's own UserData.
func (u *userState) episodePlayed(key string) bool {
	u.load()
	if u.watched[key] {
		return true
	}
	p, ok := u.progress[key]
	return ok && !p.HiddenFromContinueWatching && p.PercentWatched >= 90
}

// seriesWatch is what the profile's history says about its series: the
// continue-watching entry (with episode counts) and whether any episode of a
// series was played. Keyed by series GUID.
type seriesWatch struct {
	cw     map[string]models.SeriesWatchState
	played map[string]bool
}

func (u *userState) seriesWatch() *seriesWatch {
	u.load()
	u.seriesOnce.Do(func() {
		sw := &seriesWatch{cw: map[string]models.SeriesWatchState{}, played: map[string]bool{}}
		u.series = sw
		h := u.s.deps.History
		if h == nil {
			return
		}
		if rows, err := h.ListContinueWatching(u.profileID); err == nil {
			for _, cw := range rows {
				sw.cw[seriesGUID(titleFromWatchState(cw))] = cw
			}
		}
		seen := map[string]bool{}
		mark := func(seriesID string, ext map[string]string) {
			if seriesID = strings.TrimSpace(seriesID); seriesID == "" || seen[seriesID] {
				return
			}
			seen[seriesID] = true
			sw.played[seriesGUID(titleInfo{MediaType: "series", ID: seriesID, ExternalIDs: ext})] = true
		}
		if rows, err := h.ListWatchHistory(u.profileID); err == nil {
			for _, row := range rows {
				if row.Watched && normalizeMediaType(row.MediaType) == "episode" {
					mark(row.SeriesID, row.ExternalIDs)
				}
			}
		}
		for _, p := range u.progress {
			if normalizeMediaType(p.MediaType) == "episode" && p.PercentWatched >= 90 {
				mark(p.SeriesID, p.ExternalIDs)
			}
		}
	})
	return u.series
}

// seriesGUID is the item ID of a series, without registering it: history
// rows only identify series here, their context must not reach the store.
func seriesGUID(t titleInfo) string {
	t.MediaType = "series"
	t.ExternalIDs = titleLevelIDs(t.ExternalIDs)
	return deriveID(refSeries, t.source())
}

// applyCounts sets Played and UnplayedItemCount from episode counts.
func applyCounts(data *dto.UserItemData, total, unplayed int) {
	if data == nil || total <= 0 {
		return
	}
	unplayed = max(0, min(unplayed, total))
	data.UnplayedItemCount = intPtr(unplayed)
	data.Played = unplayed == 0
	if data.Played {
		data.PlayCount = max(data.PlayCount, 1)
	}
	data.PlayedPercentage = floatPtr(float64(total-unplayed) * 100 / float64(total))
}

// seasonPlayState counts a season's aired episodes.
func (s *Server) seasonPlayState(u *userState, series seriesRef, season models.SeriesSeason, data *dto.UserItemData) {
	episodes := airedEpisodes(season.Episodes)
	if len(episodes) == 0 {
		return
	}
	unplayed := 0
	for _, ep := range episodes {
		if !u.episodePlayed("episode:" + episodeSource(series, ep.SeasonNumber, ep.EpisodeNumber)) {
			unplayed++
		}
	}
	applyCounts(data, len(episodes), unplayed)
}

// seriesPlayState counts a series' episodes: from its continue-watching
// entry when it has one, otherwise (for series with played episodes, such as
// finished ones) from the series' episode list.
func (s *Server) seriesPlayState(u *userState, series seriesRef, data *dto.UserItemData) {
	if data == nil || data.Played {
		return
	}
	sw := u.seriesWatch()
	if cw, ok := sw.cw[series.GUID]; ok && cw.TotalEpisodeCount > 0 {
		applyCounts(data, cw.TotalEpisodeCount, cw.TotalEpisodeCount-cw.WatchedEpisodeCount)
		return
	}
	if !sw.played[series.GUID] {
		return
	}
	// Played series without an entry (finished ones) need the episode list;
	// it is counted in the background and cached, so lists stay fast.
	key := u.profileID + "|" + series.GUID
	if v, ok := s.seriesCounts.Load(key); ok {
		if c := v.(seriesCount); c.total > 0 && time.Since(c.at) < seriesCountTTL {
			applyCounts(data, c.total, c.unplayed)
			return
		}
		if time.Since(v.(seriesCount).at) < seriesCountTTL {
			return // counting, or nothing to count
		}
	}
	s.seriesCounts.Store(key, seriesCount{at: time.Now()})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), seriesPlayStateWait)
		defer cancel()
		details, err := s.seriesDetails(ctx, series.Info, series.Ordering)
		if err != nil || details == nil {
			s.seriesCounts.Delete(key)
			return
		}
		total, unplayed := 0, 0
		for _, sn := range details.Seasons {
			if sn.Number == 0 {
				continue
			}
			for _, ep := range airedEpisodes(sn.Episodes) {
				total++
				if !u.episodePlayed("episode:" + episodeSource(series, ep.SeasonNumber, ep.EpisodeNumber)) {
					unplayed++
				}
			}
		}
		// A write since the state was read makes this count stale.
		if s.stateGen(u.profileID) != u.gen {
			s.seriesCounts.Delete(key)
			return
		}
		s.seriesCounts.Store(key, seriesCount{total: total, unplayed: unplayed, at: time.Now()})
	}()
}

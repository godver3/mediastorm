package metadata

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const releasedEpisodeCountCacheVersion = "v3"

var (
	releasedEpisodeCountWarmInFlight sync.Map
	releasedEpisodeCountWarmSlots    = make(chan struct{}, 4)
)

type releasedEpisodeCountCacheEntry struct {
	Count int `json:"count"`
	// Episodes lists the released episodes behind Count so badge counts can
	// ignore watched rows for episodes the catalog does not have.
	Episodes    []models.ReleasedEpisode `json:"episodes,omitempty"`
	Unavailable bool                     `json:"unavailable,omitempty"`
	RetryAfter  time.Time                `json:"retryAfter,omitempty"`
}

// GetCachedReleasedEpisodeCount returns a locally cached count without making
// any provider requests. A true result with count zero represents a cached
// upcoming/empty series and prevents repeated background warming.
func (s *Service) GetCachedReleasedEpisodeCount(req models.SeriesDetailsQuery) (int, bool) {
	entry, ok := s.cachedReleasedEpisodes(req)
	return entry.Count, ok
}

// GetCachedReleasedEpisodes returns the locally cached released episodes (the
// ones GetCachedReleasedEpisodeCount counts) without provider requests.
func (s *Service) GetCachedReleasedEpisodes(req models.SeriesDetailsQuery) ([]models.ReleasedEpisode, bool) {
	entry, ok := s.cachedReleasedEpisodes(req)
	return entry.Episodes, ok
}

func (s *Service) cachedReleasedEpisodes(req models.SeriesDetailsQuery) (releasedEpisodeCountCacheEntry, bool) {
	if s == nil || s.cache == nil {
		return releasedEpisodeCountCacheEntry{}, false
	}
	for _, key := range releasedEpisodeCountCacheKeys(req) {
		var entry releasedEpisodeCountCacheEntry
		if ok, _ := s.cache.get(key, &entry); ok {
			if entry.Unavailable {
				if entry.RetryAfter.IsZero() || time.Now().Before(entry.RetryAfter) {
					return releasedEpisodeCountCacheEntry{}, true
				}
				continue
			}
			if entry.Count >= 0 {
				return entry, true
			}
		}
	}
	if episodes, ok := s.releasedEpisodesFromCachedDetails(req); ok {
		s.cacheReleasedEpisodes(req, episodes)
		return releasedEpisodeCountCacheEntry{Count: len(episodes), Episodes: episodes}, true
	}
	return releasedEpisodeCountCacheEntry{}, false
}

// WarmReleasedEpisodeCount schedules a bounded, deduplicated background lookup.
// It deliberately does not block the response that discovered the cache miss.
func (s *Service) WarmReleasedEpisodeCount(req models.SeriesDetailsQuery) {
	if s == nil || s.cache == nil || s.client == nil {
		return
	}
	if _, ok := s.GetCachedReleasedEpisodeCount(req); ok {
		return
	}

	keys := releasedEpisodeCountCacheKeys(req)
	if len(keys) == 0 {
		return
	}
	warmKey := keys[0]
	if _, loaded := releasedEpisodeCountWarmInFlight.LoadOrStore(warmKey, struct{}{}); loaded {
		return
	}

	go func() {
		defer releasedEpisodeCountWarmInFlight.Delete(warmKey)
		releasedEpisodeCountWarmSlots <- struct{}{}
		defer func() { <-releasedEpisodeCountWarmSlots }()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.warmReleasedEpisodeCount(ctx, req); err != nil {
			s.cacheUnavailableReleasedEpisodeCount(req, 6*time.Hour)
			log.Printf("[metadata] released episode count warm failed titleId=%q name=%q: %v", req.TitleID, req.Name, err)
		}
	}()
}

func (s *Service) warmReleasedEpisodeCount(ctx context.Context, req models.SeriesDetailsQuery) error {
	req = normalizeSeriesDetailsQueryIDs(req)
	if !s.client.isConfigured() {
		req.TMDBID = s.resolveTMDBSeriesID(ctx, req)
		if req.TMDBID <= 0 {
			return fmt.Errorf("unable to resolve tmdb id")
		}
		details, err := s.tmdbSeriesDetailsFallback(ctx, req, fmt.Errorf("tvdb api key not configured"))
		if err != nil {
			return err
		}
		if details == nil {
			return fmt.Errorf("tmdb returned no series details")
		}
		if details.Title.TMDBID > 0 {
			req.TMDBID = details.Title.TMDBID
		}
		if details.Title.TVDBID > 0 {
			req.TVDBID = details.Title.TVDBID
		}
		if strings.TrimSpace(details.Title.IMDBID) != "" {
			req.IMDBID = details.Title.IMDBID
		}
		return s.cacheReleasedEpisodes(req, releasedSeriesDetailsEpisodes(details, time.Now()))
	}

	tvdbID, err := s.resolveSeriesTVDBID(ctx, req)
	if err != nil {
		return err
	}
	if tvdbID <= 0 {
		return fmt.Errorf("unable to resolve tvdb id")
	}

	// Match SeriesDetailsLite's extended-data cache key so details, continue
	// watching, and count warming all share the same provider response.
	extended, err := s.cachedSeriesExtended(tvdbID, []string{"episodes", "seasons", "artworks"})
	if err != nil {
		return err
	}
	req.TVDBID = tvdbID
	return s.cacheReleasedEpisodes(req, releasedTVDBEpisodes(extended.Episodes, time.Now()))
}

func (s *Service) releasedEpisodesFromCachedDetails(req models.SeriesDetailsQuery) ([]models.ReleasedEpisode, bool) {
	req = normalizeSeriesDetailsQueryIDs(req)
	if s.client == nil {
		return nil, false
	}
	if req.TMDBID > 0 {
		var details models.SeriesDetails
		tmdbCacheID := cacheKey(
			"tmdb", "series", "details-fallback", "v4", s.client.language,
			strconv.FormatInt(req.TMDBID, 10),
		)
		if ok, _ := s.cache.get(tmdbCacheID, &details); ok && len(details.Seasons) > 0 {
			return releasedSeriesDetailsEpisodes(&details, time.Now()), true
		}
	}
	if req.TVDBID <= 0 {
		return nil, false
	}

	var details models.SeriesDetails
	fullCacheID := seriesDetailsCacheKey(s.client.language, req.TVDBID, req.SeasonType)
	if ok, _ := s.cache.get(fullCacheID, &details); ok && len(details.Seasons) > 0 {
		return releasedSeriesDetailsEpisodes(&details, time.Now()), true
	}

	liteCacheID := seriesDetailsLiteCacheKey(s.client.language, req.TVDBID, req.SeasonType)
	if ok, _ := s.cache.get(liteCacheID, &details); ok && len(details.Seasons) > 0 {
		return releasedSeriesDetailsEpisodes(&details, time.Now()), true
	}
	return nil, false
}

func (s *Service) cacheReleasedEpisodes(req models.SeriesDetailsQuery, episodes []models.ReleasedEpisode) error {
	entry := releasedEpisodeCountCacheEntry{Count: len(episodes), Episodes: episodes}
	for _, key := range releasedEpisodeCountCacheKeys(req) {
		if err := s.cache.set(key, entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) cacheUnavailableReleasedEpisodeCount(req models.SeriesDetailsQuery, retryAfter time.Duration) {
	entry := releasedEpisodeCountCacheEntry{
		Unavailable: true,
		RetryAfter:  time.Now().Add(retryAfter),
	}
	for _, key := range releasedEpisodeCountCacheKeys(req) {
		if err := s.cache.set(key, entry); err != nil {
			log.Printf("[metadata] released episode count negative cache failed titleId=%q: %v", req.TitleID, err)
			return
		}
	}
}

func releasedEpisodeCountCacheKeys(req models.SeriesDetailsQuery) []string {
	req = normalizeSeriesDetailsQueryIDs(req)
	values := make([]string, 0, 5)
	add := func(kind, value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		key := cacheKey("metadata", "series", "released-episode-count", releasedEpisodeCountCacheVersion, kind, value)
		for _, existing := range values {
			if existing == key {
				return
			}
		}
		values = append(values, key)
	}
	if req.TVDBID > 0 {
		add("tvdb", strconv.FormatInt(req.TVDBID, 10))
	}
	if req.TMDBID > 0 {
		add("tmdb", strconv.FormatInt(req.TMDBID, 10))
	}
	add("imdb", req.IMDBID)
	add("title", req.TitleID)
	if len(values) == 0 && strings.TrimSpace(req.Name) != "" {
		add("name-year", fmt.Sprintf("%s:%d", req.Name, req.Year))
	}
	return values
}

func countReleasedTVDBEpisodes(episodes []tvdbEpisode, now time.Time) int {
	return len(releasedTVDBEpisodes(episodes, now))
}

// releasedTVDBEpisodes returns the distinct released regular-season episodes,
// in season/episode order.
func releasedTVDBEpisodes(episodes []tvdbEpisode, now time.Time) []models.ReleasedEpisode {
	today := now.UTC().Truncate(24 * time.Hour)
	latestSeason, latestEpisode := 0, 0
	for _, episode := range episodes {
		if episode.SeasonNumber <= 0 || strings.TrimSpace(episode.Aired) == "" {
			continue
		}
		airDate, err := time.Parse("2006-01-02", strings.TrimSpace(episode.Aired))
		if err != nil {
			continue
		}
		if airDate.After(today) {
			continue
		}
		if episode.SeasonNumber > latestSeason ||
			(episode.SeasonNumber == latestSeason && episode.Number > latestEpisode) {
			latestSeason, latestEpisode = episode.SeasonNumber, episode.Number
		}
	}

	seen := make(map[[2]int]struct{}, len(episodes))
	released := make([]models.ReleasedEpisode, 0, len(episodes))
	for _, episode := range episodes {
		if episode.SeasonNumber <= 0 || episode.Number <= 0 {
			continue
		}
		isReleased := false
		if aired := strings.TrimSpace(episode.Aired); aired != "" {
			if airDate, err := time.Parse("2006-01-02", aired); err != nil || !airDate.After(today) {
				isReleased = true
			}
		} else if latestSeason == 0 || episode.SeasonNumber < latestSeason ||
			(episode.SeasonNumber == latestSeason && episode.Number <= latestEpisode) {
			isReleased = true
		}
		key := [2]int{episode.SeasonNumber, episode.Number}
		if _, dup := seen[key]; !isReleased || dup {
			continue
		}
		seen[key] = struct{}{}
		released = append(released, models.ReleasedEpisode{
			Season: episode.SeasonNumber, Episode: episode.Number, Absolute: episode.AbsoluteNumber,
		})
	}
	sort.Slice(released, func(i, j int) bool {
		if released[i].Season != released[j].Season {
			return released[i].Season < released[j].Season
		}
		return released[i].Episode < released[j].Episode
	})
	return released
}

func releasedSeriesDetailsEpisodes(details *models.SeriesDetails, now time.Time) []models.ReleasedEpisode {
	if details == nil {
		return nil
	}
	episodes := make([]tvdbEpisode, 0)
	for _, season := range details.Seasons {
		for _, episode := range season.Episodes {
			episodes = append(episodes, tvdbEpisode{
				SeasonNumber:   episode.SeasonNumber,
				Number:         episode.EpisodeNumber,
				Aired:          episode.AiredDate,
				AbsoluteNumber: episode.AbsoluteEpisodeNumber,
			})
		}
	}
	return releasedTVDBEpisodes(episodes, now)
}

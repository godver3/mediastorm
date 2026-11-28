package metadata

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const (
	// releaseWarmMaxPerCall caps how many uncached movies one response may
	// queue for background release-window fetches.
	releaseWarmMaxPerCall = 20
	// releaseWarmConcurrency caps concurrent background TMDB release fetches.
	releaseWarmConcurrency = 2
	releaseWarmTimeout     = 20 * time.Second
)

// releaseWarmer deduplicates and bounds background release-window fetches so
// search and shelf responses never wait on per-title upstream calls.
type releaseWarmer struct {
	enabled  bool // set by NewService; zero-value services (tests) never warm
	mu       sync.Mutex
	inflight map[int64]struct{}
	sem      chan struct{}
}

// finalizeSearchReleaseStatuses hydrates cached movie release windows and
// normalizes release statuses for search results.
func (s *Service) finalizeSearchReleaseStatuses(results []models.SearchResult) {
	titles := make([]*models.Title, 0, len(results))
	for i := range results {
		titles = append(titles, &results[i].Title)
	}
	s.hydrateCachedMovieReleaseWindows(titles)
	ensureSearchMovieReleaseStatuses(results)
}

// finalizeTrendingReleaseStatuses is finalizeSearchReleaseStatuses for shelves.
func (s *Service) finalizeTrendingReleaseStatuses(items []models.TrendingItem) {
	titles := make([]*models.Title, 0, len(items))
	for i := range items {
		titles = append(titles, &items[i].Title)
	}
	s.hydrateCachedMovieReleaseWindows(titles)
	ensureTrendingMovieReleaseStatuses(items)
}

// hydrateCachedMovieReleaseWindows fills theatrical/home release windows for
// movies from the release cache only (no upstream calls). Movies missing from
// the cache are queued for a bounded background fetch so later responses carry
// accurate windows; until then availability falls back to the release date.
func (s *Service) hydrateCachedMovieReleaseWindows(titles []*models.Title) {
	if s == nil || s.cache == nil {
		return
	}
	var misses []int64
	for _, title := range titles {
		if title == nil || !strings.EqualFold(strings.TrimSpace(title.MediaType), "movie") || title.TMDBID <= 0 {
			continue
		}
		if len(title.Releases) > 0 || title.Theatrical != nil || title.HomeRelease != nil {
			continue
		}
		cacheID := cacheKey("tmdb", "movie", "releases", "v3", strconv.FormatInt(title.TMDBID, 10))
		var cached cachedReleasesWithCert
		ok, _ := s.cache.get(cacheID, &cached)
		if !ok {
			misses = append(misses, title.TMDBID)
			continue
		}
		if len(cached.Releases) == 0 {
			continue // negative cache: TMDB has no release data
		}
		title.Releases = append([]models.Release(nil), cached.Releases...)
		if title.Certification == "" {
			title.Certification = cached.Certification
		}
		s.ensureMovieReleasePointers(title)
	}
	s.warmMovieReleasesAsync(misses)
}

// warmMovieReleasesAsync fetches release windows for up to
// releaseWarmMaxPerCall uncached movies in the background.
func (s *Service) warmMovieReleasesAsync(tmdbIDs []int64) {
	if len(tmdbIDs) == 0 || !s.releaseWarm.enabled || s.tmdb == nil || !s.tmdb.isConfigured() {
		return
	}
	w := &s.releaseWarm
	w.mu.Lock()
	if w.inflight == nil {
		w.inflight = make(map[int64]struct{})
		w.sem = make(chan struct{}, releaseWarmConcurrency)
	}
	queued := make([]int64, 0, releaseWarmMaxPerCall)
	for _, id := range tmdbIDs {
		if len(queued) >= releaseWarmMaxPerCall {
			break
		}
		if _, busy := w.inflight[id]; busy {
			continue
		}
		w.inflight[id] = struct{}{}
		queued = append(queued, id)
	}
	sem := w.sem
	w.mu.Unlock()

	for _, id := range queued {
		go func(tmdbID int64) {
			sem <- struct{}{}
			defer func() {
				<-sem
				w.mu.Lock()
				delete(w.inflight, tmdbID)
				w.mu.Unlock()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), releaseWarmTimeout)
			defer cancel()
			s.enrichMovieReleases(ctx, &models.Title{MediaType: "movie", TMDBID: tmdbID}, tmdbID)
		}(id)
	}
}

// GetCachedSeriesRelease returns a series' release status (released |
// unreleased) and premiere date (YYYY-MM-DD) from cached series metadata only
// (no upstream calls), for lists whose stored items carry no or a stale status.
// Both are "" when nothing usable is cached.
func (s *Service) GetCachedSeriesRelease(tmdbID int64, tvdbID int64) (status string, releaseDate string) {
	if s == nil || s.cache == nil || s.client == nil {
		return "", ""
	}
	lang := s.client.language
	if tvdbID > 0 {
		// The lightweight info entry first: full details hold every episode.
		var info models.Title
		if ok, _ := s.cache.get(cacheKey("tvdb", "series", "info", "v1", lang, strconv.FormatInt(tvdbID, 10)), &info); ok {
			if status, date := cachedSeriesRelease(info, nil); status != "" {
				return status, date
			}
		}
		var details models.SeriesDetails
		if ok, _ := s.cache.get(seriesDetailsCacheKey(lang, tvdbID, ""), &details); ok {
			if status, date := cachedSeriesRelease(details.Title, details.Seasons); status != "" {
				return status, date
			}
		}
	}
	if tmdbID > 0 {
		var details models.SeriesDetails
		if ok, _ := s.cache.get(cacheKey("tmdb", "series", "details-fallback", "v4", lang, strconv.FormatInt(tmdbID, 10)), &details); ok {
			return cachedSeriesRelease(details.Title, details.Seasons)
		}
	}
	return "", ""
}

// cachedSeriesRelease derives a cached series' status relative to now: the
// premiere date (from the episodes when known) wins over a cached status that
// may predate it.
func cachedSeriesRelease(title models.Title, seasons []models.SeriesSeason) (string, string) {
	date := models.SeriesPremiereDate(seasons)
	if date == "" {
		date = models.NormalizeReleaseDate(title.ReleaseDate)
	}
	status := strings.ToLower(strings.TrimSpace(title.Status))
	if status != models.SeriesReleaseStatusReleased && status != models.SeriesReleaseStatusUnreleased {
		status = ""
	}
	if date != "" && status != models.SeriesReleaseStatusReleased {
		status = models.SeriesReleaseStatusFromDate(date)
	}
	return status, date
}

// cachedMovieReleaseItem reads a movie's release windows from the release
// cache written by enrichMovieReleases. hit is false when nothing is cached.
// A negative cache entry (TMDB has no release data) is a hit with status
// "unknown" so callers do not queue it for another fetch.
func (s *Service) cachedMovieReleaseItem(tmdbID int64) (item models.BatchMovieReleasesItem, hit bool) {
	if s == nil || s.cache == nil || tmdbID <= 0 {
		return item, false
	}
	cacheID := cacheKey("tmdb", "movie", "releases", "v3", strconv.FormatInt(tmdbID, 10))
	var cached cachedReleasesWithCert
	if ok, _ := s.cache.get(cacheID, &cached); !ok {
		return item, false
	}
	if len(cached.Releases) == 0 {
		item.Status = models.MovieReleaseStatusUnknown
		item.Error = "failed to fetch release data"
		return item, true
	}
	tempTitle := &models.Title{
		MediaType:     "movie",
		Releases:      append([]models.Release(nil), cached.Releases...),
		Certification: cached.Certification,
	}
	s.ensureMovieReleasePointers(tempTitle)
	item.Status = tempTitle.Status
	item.Theatrical = tempTitle.Theatrical
	item.HomeRelease = tempTitle.HomeRelease
	return item, true
}

// CachedMovieReleases is the cache-only form of BatchMovieReleases for
// request paths (search, trending, discover, recommendations) that must not
// wait on TMDB. Cache misses come back with status "unknown" and are queued
// for the bounded, deduplicated background warmer, so a later request carries
// the windows. IMDB-only queries are not resolved (that may call upstream).
func (s *Service) CachedMovieReleases(_ context.Context, queries []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem {
	results := make([]models.BatchMovieReleasesItem, len(queries))
	var misses []int64
	for i, query := range queries {
		tmdbID := query.TMDBID
		if tmdbID <= 0 && strings.HasPrefix(query.TitleID, "tmdb:movie:") {
			if id, err := strconv.ParseInt(strings.TrimPrefix(query.TitleID, "tmdb:movie:"), 10, 64); err == nil {
				tmdbID = id
			}
		}
		item, hit := s.cachedMovieReleaseItem(tmdbID)
		item.Query = query
		if !hit {
			item.Status = models.MovieReleaseStatusUnknown
			if tmdbID > 0 {
				misses = append(misses, tmdbID)
			}
		}
		results[i] = item
	}
	s.warmMovieReleasesAsync(misses)
	return results
}

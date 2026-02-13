package metadata

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"novastream/config"
	"novastream/models"
)

type Service struct {
	client  *tvdbClient
	tmdb    *tmdbClient
	mdblist *mdblistClient
	cache   *fileCache
	// Separate cache for stable ID mappings (TMDB↔IMDB) with 7x longer TTL
	idCache *fileCache
	demo    bool

	// Cache TTL in hours (stored for reuse when updating clients)
	ttlHours int

	// In-flight request deduplication for TVDB ID resolution
	inflightMu       sync.Mutex
	inflightRequests map[string]*inflightRequest

	// Trailer prequeue manager for 1080p YouTube trailers
	trailerPrequeue *TrailerPrequeueManager
}

type inflightRequest struct {
	wg     sync.WaitGroup
	result int64
	err    error
}

const tvdbArtworkBaseURL = "https://artworks.thetvdb.com"

// MDBListConfig holds configuration for the MDBList client
type MDBListConfig struct {
	APIKey         string
	Enabled        bool
	EnabledRatings []string
}

// stableIDCacheTTLMultiplier is used for ID mappings (TMDB↔IMDB) that rarely change
const stableIDCacheTTLMultiplier = 7

func NewService(tvdbAPIKey, tmdbAPIKey, language, cacheDir string, ttlHours int, demo bool, mdblistCfg MDBListConfig) *Service {
	// Use a dedicated subdirectory for metadata cache to avoid conflicts with
	// other data stored in the cache directory (users, watchlists, history, etc.)
	metadataCacheDir := filepath.Join(cacheDir, "metadata")
	idCacheDir := filepath.Join(cacheDir, "metadata", "ids")

	// Initialize trailer prequeue manager
	trailerTempDir := filepath.Join(os.TempDir(), "strmr-trailers")
	trailerMgr, err := NewTrailerPrequeueManager(trailerTempDir)
	if err != nil {
		log.Printf("[metadata] WARNING: failed to initialize trailer prequeue manager: %v", err)
	}

	return &Service{
		client:           newTVDBClient(tvdbAPIKey, language, &http.Client{}, ttlHours),
		tmdb:             newTMDBClient(tmdbAPIKey, language, &http.Client{}, newFileCache(metadataCacheDir, ttlHours)),
		mdblist:          newMDBListClient(mdblistCfg.APIKey, mdblistCfg.EnabledRatings, mdblistCfg.Enabled, ttlHours),
		cache:            newFileCache(metadataCacheDir, ttlHours),
		idCache:          newFileCache(idCacheDir, ttlHours*stableIDCacheTTLMultiplier),
		demo:             demo,
		ttlHours:         ttlHours,
		inflightRequests: make(map[string]*inflightRequest),
		trailerPrequeue:  trailerMgr,
	}
}

// UpdateAPIKeys updates the API keys for TVDB and TMDB clients
// This allows hot reloading when settings change
func (s *Service) UpdateAPIKeys(tvdbAPIKey, tmdbAPIKey, language string) {
	s.client = newTVDBClient(tvdbAPIKey, language, &http.Client{}, s.ttlHours)
	s.tmdb = newTMDBClient(tmdbAPIKey, language, &http.Client{}, s.cache)

	// Clear all cached metadata so fresh data is fetched with new API keys
	if err := s.cache.clear(); err != nil {
		log.Printf("[metadata] warning: failed to clear cache: %v", err)
	} else {
		log.Printf("[metadata] cleared metadata cache due to API key change")
	}
	// Also clear ID mapping cache
	if s.idCache != nil {
		if err := s.idCache.clear(); err != nil {
			log.Printf("[metadata] warning: failed to clear ID cache: %v", err)
		}
	}
}

// UpdateMDBListSettings updates the MDBList client configuration
func (s *Service) UpdateMDBListSettings(cfg MDBListConfig) {
	if s.mdblist != nil {
		s.mdblist.UpdateSettings(cfg.APIKey, cfg.EnabledRatings, cfg.Enabled)
		log.Printf("[metadata] updated MDBList settings (enabled=%v, ratings=%v)", cfg.Enabled, cfg.EnabledRatings)
	}
}

// ClearCache removes all cached metadata files
func (s *Service) ClearCache() error {
	return s.cache.clear()
}

// getIMDBIDForTMDB returns the IMDB ID for a TMDB ID, using cache when available.
// ID mappings are cached with a longer TTL since they rarely change.
func (s *Service) getIMDBIDForTMDB(ctx context.Context, mediaType string, tmdbID int64) string {
	if tmdbID <= 0 {
		return ""
	}

	// Check ID cache first
	cacheID := cacheKey("id", "tmdb-to-imdb", mediaType, fmt.Sprintf("%d", tmdbID))
	var cached string
	if ok, _ := s.idCache.get(cacheID, &cached); ok {
		return cached
	}

	// Fetch from TMDB API
	imdbID, err := s.tmdb.fetchExternalID(ctx, mediaType, tmdbID)
	if err != nil {
		log.Printf("[metadata] failed to fetch IMDB ID for TMDB %s/%d: %v", mediaType, tmdbID, err)
		return ""
	}

	// Cache the result (even empty string to avoid repeated lookups)
	if err := s.idCache.set(cacheID, imdbID); err != nil {
		log.Printf("[metadata] failed to cache IMDB ID mapping: %v", err)
	}

	return imdbID
}

// getTMDBIDForIMDB returns the TMDB ID for an IMDB ID, using cache when available.
// ID mappings are cached with a longer TTL since they rarely change.
func (s *Service) getTMDBIDForIMDB(ctx context.Context, imdbID string) int64 {
	if imdbID == "" {
		return 0
	}

	// Normalize IMDB ID
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	// Check ID cache first
	cacheID := cacheKey("id", "imdb-to-tmdb", "movie", imdbID)
	var cached int64
	if ok, _ := s.idCache.get(cacheID, &cached); ok {
		return cached
	}

	// Fetch from TMDB API
	tmdbID, err := s.tmdb.findMovieByIMDBID(ctx, imdbID)
	if err != nil {
		log.Printf("[metadata] failed to fetch TMDB ID for IMDB %s: %v", imdbID, err)
		return 0
	}

	// Cache the result
	if err := s.idCache.set(cacheID, tmdbID); err != nil {
		log.Printf("[metadata] failed to cache TMDB ID mapping: %v", err)
	}

	return tmdbID
}

func cacheKey(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(h[:])
}

// Trending returns a list of trending titles for the given media type (series|movie).
// The trendingMovieSource parameter controls which source is used for movies:
// - "all": Use TMDB trending (includes unreleased movies)
// - "released": Use MDBList top movies of the week (released only)
func (s *Service) Trending(ctx context.Context, mediaType string, trendingMovieSource config.TrendingMovieSource) ([]models.TrendingItem, error) {
	normalized := strings.ToLower(strings.TrimSpace(mediaType))
	switch normalized {
	case "", "tv", "series", "show", "shows":
		normalized = "tv"
	case "movie", "movies", "film", "films":
		normalized = "movie"
	default:
		normalized = "tv"
	}

	if s.demo {
		items := copyTrendingItems(selectDemoTrending(normalized))
		s.enrichDemoArtwork(ctx, items, normalized)
		return items, nil
	}

	fallbackLabel := "series"
	fallbackFetcher := s.getTrendingSeries
	if normalized == "movie" {
		fallbackLabel = "movie"
		fallbackFetcher = s.getRecentMovies
	}

	// For movies, check if we should use released-only source (MDBList)
	if normalized == "movie" && trendingMovieSource == config.TrendingMovieSourceReleased {
		// Use MDBList directly for released movies only
		// v2: includes release data enrichment
		fallbackKey := cacheKey("mdblist", "trending", "movie", "v2")
		var cached []models.TrendingItem
		if ok, _ := s.cache.get(fallbackKey, &cached); ok && len(cached) > 0 {
			return cached, nil
		}

		items, err := s.getRecentMovies()
		if err != nil {
			return nil, err
		}
		// Enrich movies with release data (theatrical/home release)
		s.enrichTrendingMovieReleases(ctx, items)
		if len(items) > 0 {
			_ = s.cache.set(fallbackKey, items)
		}
		return items, nil
	}

	// Use TMDB for "all" trending (includes unreleased) or for TV shows
	if s.tmdb != nil && s.tmdb.isConfigured() {
		// v3: includes release data enrichment for movies and content ratings for TV
		key := cacheKey("tmdb", "trending", normalized, "v3")
		var cached []models.TrendingItem
		if ok, _ := s.cache.get(key, &cached); ok && len(cached) > 0 {
			return cached, nil
		}

		items, err := s.tmdb.trending(ctx, normalized)
		if err == nil && len(items) > 0 {
			// Enrich with IMDB IDs using cached lookups
			s.enrichTrendingIMDBIDs(ctx, items, normalized)
			// Enrich movies with release data (theatrical/home release)
			if normalized == "movie" {
				s.enrichTrendingMovieReleases(ctx, items)
			} else {
				// Enrich TV shows with content ratings
				s.enrichTrendingTVContentRatings(ctx, items)
			}
			_ = s.cache.set(key, items)
			return items, nil
		}
		if err != nil {
			fmt.Printf("[metadata] tmdb trending failed type=%s err=%v; falling back to %s feed\n", normalized, err, fallbackLabel)
		} else {
			fmt.Printf("[metadata] tmdb trending returned no results type=%s; falling back to %s feed\n", normalized, fallbackLabel)
		}
	} else {
		fmt.Printf("[metadata] tmdb trending unavailable type=%s; using %s feed\n", normalized, fallbackLabel)
	}

	if fallbackFetcher == nil {
		return nil, fmt.Errorf("unsupported media type: %s", mediaType)
	}

	// v3: includes release data enrichment for movies and content ratings for TV
	fallbackKey := cacheKey("mdblist", "trending", fallbackLabel, "v3")
	var cached []models.TrendingItem
	if ok, _ := s.cache.get(fallbackKey, &cached); ok && len(cached) > 0 {
		return cached, nil
	}

	items, err := fallbackFetcher()
	if err != nil {
		return nil, err
	}
	// Enrich movies with release data (theatrical/home release)
	if normalized == "movie" {
		s.enrichTrendingMovieReleases(ctx, items)
	} else {
		// Enrich TV shows with content ratings
		s.enrichTrendingTVContentRatings(ctx, items)
	}
	if len(items) > 0 {
		_ = s.cache.set(fallbackKey, items)
	}
	return items, nil
}

// enrichDemoArtwork fetches artwork from TVDB for demo mode items
func (s *Service) enrichDemoArtwork(ctx context.Context, items []models.TrendingItem, mediaType string) {
	for idx := range items {
		title := &items[idx].Title
		if title.TVDBID <= 0 {
			continue
		}

		// Check cache first (v3 fixed TVDB IDs)
		cacheID := cacheKey("demo", "artwork", "v3", mediaType, strconv.FormatInt(title.TVDBID, 10))
		var cachedTitle models.Title
		if ok, _ := s.cache.get(cacheID, &cachedTitle); ok {
			log.Printf("[demo] cache hit for %s tvdbId=%d hasPoster=%v hasBackdrop=%v",
				mediaType, title.TVDBID, cachedTitle.Poster != nil, cachedTitle.Backdrop != nil)
			title.Poster = cachedTitle.Poster
			title.Backdrop = cachedTitle.Backdrop
			continue
		}

		// Fetch artwork from TVDB
		if mediaType == "movie" {
			if ext, err := s.client.movieExtended(title.TVDBID, []string{"artwork"}); err == nil {
				applyTVDBArtworks(title, ext.Artworks)
			}
		} else {
			if ext, err := s.client.seriesExtended(title.TVDBID, []string{"artworks"}); err == nil {
				log.Printf("[demo] series tvdbId=%d poster=%q image=%q fanart=%q artworks=%d",
					title.TVDBID, ext.Poster, ext.Image, ext.Fanart, len(ext.Artworks))
				// Apply direct poster/fanart fields first
				if img := newTVDBImage(ext.Poster, "poster", 0, 0); img != nil {
					title.Poster = img
				} else if img := newTVDBImage(ext.Image, "poster", 0, 0); img != nil {
					title.Poster = img
				}
				if backdrop := newTVDBImage(ext.Fanart, "backdrop", 0, 0); backdrop != nil {
					title.Backdrop = backdrop
				}
				// Then apply artworks array
				applyTVDBArtworks(title, ext.Artworks)
				log.Printf("[demo] series tvdbId=%d after enrichment hasPoster=%v hasBackdrop=%v",
					title.TVDBID, title.Poster != nil, title.Backdrop != nil)
			} else {
				log.Printf("[demo] series tvdbId=%d fetch error: %v", title.TVDBID, err)
			}
		}

		// Cache the artwork
		_ = s.cache.set(cacheID, *title)
	}
}

// enrichTrendingIMDBIDs adds IMDB IDs to trending items using cached lookups.
// This runs concurrently for performance but uses the ID cache to minimize API calls.
// Uses a semaphore to limit concurrent TMDB API calls and prevent thundering herd.
func (s *Service) enrichTrendingIMDBIDs(ctx context.Context, items []models.TrendingItem, mediaType string) {
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for idx := range items {
		if items[idx].Title.IMDBID != "" || items[idx].Title.TMDBID <= 0 {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			imdbID := s.getIMDBIDForTMDB(ctx, mediaType, items[i].Title.TMDBID)
			if imdbID != "" {
				items[i].Title.IMDBID = imdbID
			}
		}(idx)
	}
	wg.Wait()
}

// enrichTrendingMovieReleases adds release data (theatrical/home release) to trending movie items.
// This runs concurrently for performance. Release data is cached by enrichMovieReleases.
func (s *Service) enrichTrendingMovieReleases(ctx context.Context, items []models.TrendingItem) {
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var enrichedCount int32

	for idx := range items {
		// Skip non-movies
		if items[idx].Title.MediaType != "movie" {
			continue
		}
		// Skip if already has release data
		if items[idx].Title.HomeRelease != nil || items[idx].Title.Theatrical != nil {
			continue
		}
		// Need TMDB ID to fetch releases
		tmdbID := items[idx].Title.TMDBID
		if tmdbID <= 0 {
			continue
		}

		wg.Add(1)
		go func(i int, tmdbID int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if s.enrichMovieReleases(ctx, &items[i].Title, tmdbID) {
				atomic.AddInt32(&enrichedCount, 1)
			}
		}(idx, tmdbID)
	}
	wg.Wait()

	if enrichedCount > 0 {
		log.Printf("[metadata] enriched %d trending movies with release data", enrichedCount)
	}
}

// enrichTrendingTVContentRatings fetches TV content ratings for trending TV shows
func (s *Service) enrichTrendingTVContentRatings(ctx context.Context, items []models.TrendingItem) {
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var enrichedCount int32

	for idx := range items {
		// Skip non-series
		if items[idx].Title.MediaType != "series" {
			continue
		}
		// Skip if already has content rating
		if items[idx].Title.Certification != "" {
			continue
		}
		// Need TMDB ID to fetch content rating
		tmdbID := items[idx].Title.TMDBID
		if tmdbID <= 0 {
			continue
		}

		wg.Add(1)
		go func(i int, tmdbID int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if s.enrichTVContentRating(ctx, &items[i].Title, tmdbID) {
				atomic.AddInt32(&enrichedCount, 1)
			}
		}(idx, tmdbID)
	}
	wg.Wait()

	if enrichedCount > 0 {
		log.Printf("[metadata] enriched %d trending TV shows with content rating", enrichedCount)
	}
}

// searchDemo searches the demo public domain content for matching titles
func (s *Service) searchDemo(ctx context.Context, query string, mediaType string) []models.SearchResult {
	queryLower := strings.ToLower(query)
	var results []models.SearchResult

	// Determine which demo lists to search
	var sources [][]models.TrendingItem
	if mediaType == "movie" || mediaType == "movies" {
		sources = [][]models.TrendingItem{demoTrendingMovies}
	} else if mediaType == "series" || mediaType == "tv" {
		sources = [][]models.TrendingItem{demoTrendingSeries}
	} else {
		// Search both
		sources = [][]models.TrendingItem{demoTrendingMovies, demoTrendingSeries}
	}

	for _, source := range sources {
		for _, item := range source {
			// Check if query matches title name or overview
			nameLower := strings.ToLower(item.Title.Name)
			overviewLower := strings.ToLower(item.Title.Overview)

			if strings.Contains(nameLower, queryLower) || strings.Contains(overviewLower, queryLower) {
				// Copy the title and enrich with artwork
				title := item.Title
				results = append(results, models.SearchResult{
					Title: title,
					Score: 100,
				})
			}
		}
	}

	// Enrich results with artwork (group by media type for proper enrichment)
	if len(results) > 0 {
		// Separate movies and TV for proper artwork enrichment
		var movieItems, tvItems []models.TrendingItem
		var movieIdx, tvIdx []int

		for i, r := range results {
			item := models.TrendingItem{Title: r.Title}
			if r.Title.MediaType == "movie" {
				movieItems = append(movieItems, item)
				movieIdx = append(movieIdx, i)
			} else {
				tvItems = append(tvItems, item)
				tvIdx = append(tvIdx, i)
			}
		}

		// Enrich each type separately
		if len(movieItems) > 0 {
			s.enrichDemoArtwork(ctx, movieItems, "movie")
			for j, idx := range movieIdx {
				results[idx].Title.Poster = movieItems[j].Title.Poster
				results[idx].Title.Backdrop = movieItems[j].Title.Backdrop
			}
		}
		if len(tvItems) > 0 {
			s.enrichDemoArtwork(ctx, tvItems, "tv")
			for j, idx := range tvIdx {
				results[idx].Title.Poster = tvItems[j].Title.Poster
				results[idx].Title.Backdrop = tvItems[j].Title.Backdrop
			}
		}
	}

	return results
}

// getRecentMovies uses MDBList to get top movies of the week, enriched with TVDB data
func (s *Service) getRecentMovies() ([]models.TrendingItem, error) {
	// Fetch top movies from MDBList
	mdblistMovies, err := s.client.fetchMDBListMovies()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch MDBList movies: %w", err)
	}

	log.Printf("[metadata] fetched %d MDBList movies for trending feed", len(mdblistMovies))

	// Convert to TrendingItem and enrich with TVDB data where possible
	items := make([]models.TrendingItem, 0, len(mdblistMovies))
	for _, movie := range mdblistMovies {
		// Create base title from MDBList data
		title := models.Title{
			ID:         fmt.Sprintf("mdblist:movie:%d", movie.ID),
			Name:       movie.Title,
			Year:       movie.ReleaseYear,
			Language:   s.client.language,
			MediaType:  "movie",
			Popularity: float64(100 - movie.Rank), // Convert rank to popularity score
		}

		// Set IMDB ID from MDBList
		if movie.IMDBID != "" {
			title.IMDBID = movie.IMDBID
			log.Printf("[metadata] movie imdb id from mdblist title=%q imdbId=%s", movie.Title, movie.IMDBID)
		}

		// Try to enrich with TVDB data using enhanced search
		var found bool
		var searchResult *tvdbSearchResult

		// First, try to use TVDB ID from MDBList if available
		if movie.TVDBID != nil && *movie.TVDBID > 0 {
			// Use direct TVDB ID lookup
			if tvdbDetails, err := s.getTVDBMovieDetails(*movie.TVDBID); err == nil {
				title.TVDBID = *movie.TVDBID
				title.ID = fmt.Sprintf("tvdb:movie:%d", *movie.TVDBID)
				title.Name = tvdbDetails.Name
				title.Overview = tvdbDetails.Overview

				// Try to get English translation
				if translation, err := s.client.movieTranslations(*movie.TVDBID, s.client.language); err == nil && translation != nil {
					if strings.TrimSpace(translation.Name) != "" {
						title.Name = translation.Name
					}
					if strings.TrimSpace(translation.Overview) != "" {
						title.Overview = translation.Overview
					}
				}

				if tvdbDetails.Score > 0 {
					title.Popularity = tvdbDetails.Score
				}
				found = true
			} else {
				log.Printf("[metadata] tvdb movie lookup failed id=%d title=%q err=%v", *movie.TVDBID, movie.Title, err)
			}
		} else {
			// Try to search using MDBList ID as remote_id for more accurate results
			remoteID := fmt.Sprintf("%d", movie.ID)
			if searchResults, err := s.searchTVDBMovie(movie.Title, movie.ReleaseYear, remoteID); err == nil && len(searchResults) > 0 {
				searchResult = &searchResults[0]
				found = true
				log.Printf("[metadata] tvdb movie search via remote id matched title=%q remoteId=%s tvdbId=%s", movie.Title, remoteID, searchResult.TVDBID)
			} else {
				// Fallback to title/year search if remote_id search fails
				if searchResults, err := s.searchTVDBMovie(movie.Title, movie.ReleaseYear, ""); err == nil && len(searchResults) > 0 {
					searchResult = &searchResults[0]
					found = true
					log.Printf("[metadata] tvdb movie search matched title=%q year=%d tvdbId=%s", movie.Title, movie.ReleaseYear, searchResult.TVDBID)
				} else if err != nil {
					log.Printf("[metadata] tvdb movie search failed title=%q year=%d err=%v", movie.Title, movie.ReleaseYear, err)
				}
			}
		}

		if found && searchResult != nil {
			// Use search result data
			if searchResult.TVDBID != "" {
				if tvdbID, err := strconv.ParseInt(searchResult.TVDBID, 10, 64); err == nil {
					title.TVDBID = tvdbID
					title.ID = fmt.Sprintf("tvdb:movie:%d", tvdbID)
				}
			}

			// Extract IMDB ID from remote IDs if not already set
			if title.IMDBID == "" {
				for _, remote := range searchResult.RemoteIDs {
					id := strings.TrimSpace(remote.ID)
					if id == "" {
						continue
					}
					lower := strings.ToLower(remote.SourceName)
					if strings.Contains(lower, "imdb") {
						title.IMDBID = id
						log.Printf("[metadata] movie imdb id from tvdb search title=%q imdbId=%s", title.Name, id)
						break
					}
				}
			}

			// Use overview from search result (prefer language-specific if available)
			if searchResult.Overviews != nil && searchResult.Overviews["eng"] != "" {
				title.Overview = searchResult.Overviews["eng"]
			} else if searchResult.Overview != "" {
				title.Overview = searchResult.Overview
			}

			// Use image from search result
			if img := newTVDBImage(searchResult.ImageURL, "poster", 0, 0); img != nil {
				title.Poster = img
			}
			thumbURL := normalizeTVDBImageURL(searchResult.Thumbnail)
			if thumbURL != "" && title.Poster == nil {
				title.Poster = &models.Image{URL: thumbURL, Type: "poster"}
			}
			if thumbURL == "" {
				log.Printf("[metadata] tvdb movie thumbnail missing title=%q tvdbId=%d", title.Name, title.TVDBID)
			}

			// Get additional artwork from TVDB if we have a TVDB ID
			if title.TVDBID > 0 {
				if ext, err := s.client.movieExtended(title.TVDBID, []string{"artwork"}); err == nil {
					applyTVDBArtworks(&title, ext.Artworks)
					if title.Backdrop == nil {
						log.Printf("[metadata] no movie backdrop from artworks title=%q tvdbId=%d", title.Name, title.TVDBID)
					}
					if title.Poster == nil {
						log.Printf("[metadata] no movie poster from artworks title=%q tvdbId=%d", title.Name, title.TVDBID)
					}
				} else {
					log.Printf("[metadata] movie artworks fetch failed title=%q tvdbId=%d err=%v", title.Name, title.TVDBID, err)
				}
			}
		} else if !found {
			// For movies not found in TVDB, add appropriate overview
			log.Printf("[metadata] no tvdb match for movie title=%q year=%d", movie.Title, movie.ReleaseYear)
			currentYear := time.Now().Year()
			if movie.ReleaseYear > currentYear {
				title.Overview = fmt.Sprintf("Upcoming movie scheduled for release in %d", movie.ReleaseYear)
			} else if movie.ReleaseYear == currentYear {
				title.Overview = fmt.Sprintf("New movie from %d - details may be added to TVDB soon", movie.ReleaseYear)
			} else {
				title.Overview = "Movie details not available in TVDB"
			}
		}

		items = append(items, models.TrendingItem{
			Rank:  movie.Rank,
			Title: title,
		})
	}

	return items, nil
}

// getTVDBMovieDetails fetches additional details for a movie from TVDB
func (s *Service) getTVDBMovieDetails(tvdbID int64) (tvdbMovie, error) {
	var resp struct {
		Data tvdbMovie `json:"data"`
	}

	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/movies/%d", tvdbID)
	if err := s.client.doGET(endpoint, nil, &resp); err != nil {
		return tvdbMovie{}, err
	}

	return resp.Data, nil
}

// getMovieDetailsFromTMDB fetches movie details directly from TMDB when TVDB lookup fails
func (s *Service) getMovieDetailsFromTMDB(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, fmt.Errorf("tmdb client not configured")
	}

	if req.TMDBID <= 0 {
		return nil, fmt.Errorf("tmdb id required")
	}

	log.Printf("[metadata] fetching movie details from TMDB tmdbId=%d name=%q", req.TMDBID, req.Name)

	// Check cache with TMDB key
	cacheID := cacheKey("tmdb", "movie", "details", "v1", s.client.language, strconv.FormatInt(req.TMDBID, 10))
	var cached models.Title
	if ok, _ := s.cache.get(cacheID, &cached); ok && cached.ID != "" {
		log.Printf("[metadata] movie details cache hit (TMDB) tmdbId=%d lang=%s", req.TMDBID, s.client.language)
		return &cached, nil
	}

	// Fetch from TMDB
	tmdbMovie, err := s.tmdb.movieDetails(ctx, req.TMDBID)
	if err != nil {
		log.Printf("[metadata] TMDB movie fetch failed tmdbId=%d err=%v", req.TMDBID, err)
		return nil, fmt.Errorf("failed to fetch movie from TMDB: %w", err)
	}

	if tmdbMovie == nil {
		return nil, fmt.Errorf("TMDB returned nil movie")
	}

	// Build Title from TMDB data
	movieTitle := *tmdbMovie // Copy the TMDB result

	// Ensure ID is set in TMDB format
	if movieTitle.ID == "" {
		movieTitle.ID = fmt.Sprintf("tmdb:movie:%d", req.TMDBID)
	}

	// Ensure TMDB ID is set
	if movieTitle.TMDBID == 0 {
		movieTitle.TMDBID = req.TMDBID
	}

	// Use request name if TMDB name is empty
	if movieTitle.Name == "" && req.Name != "" {
		movieTitle.Name = req.Name
	}

	log.Printf("[metadata] movie from TMDB tmdbId=%d name=%q hasPost=%v hasBackdrop=%v",
		req.TMDBID, movieTitle.Name, movieTitle.Poster != nil, movieTitle.Backdrop != nil)

	if s.enrichMovieReleases(ctx, &movieTitle, movieTitle.TMDBID) && len(movieTitle.Releases) > 0 {
		log.Printf("[metadata] movie release windows set via TMDB tmdbId=%d releases=%d", movieTitle.TMDBID, len(movieTitle.Releases))
	}

	// Fetch cast credits from TMDB
	if credits, err := s.tmdb.fetchCredits(ctx, "movie", req.TMDBID); err == nil && credits != nil && len(credits.Cast) > 0 {
		movieTitle.Credits = credits
		log.Printf("[metadata] fetched %d cast members for movie (TMDB) tmdbId=%d", len(credits.Cast), req.TMDBID)
	} else if err != nil {
		log.Printf("[metadata] failed to fetch credits for movie (TMDB) tmdbId=%d: %v", req.TMDBID, err)
	}

	// Cache the result
	_ = s.cache.set(cacheID, movieTitle)

	return &movieTitle, nil
}

// searchTVDBMovie searches for a movie in TVDB by title, year, or remote ID
func (s *Service) searchTVDBMovie(title string, year int, remoteID string) ([]tvdbSearchResult, error) {
	// Create cache key from search parameters
	yearStr := ""
	if year > 0 {
		yearStr = fmt.Sprintf("%d", year)
	}
	cacheID := cacheKey("tvdb", "search", "movie", title, yearStr, remoteID)

	// Check cache first
	var cached []tvdbSearchResult
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		log.Printf("[tvdb] movie search cache hit query=%q year=%d remoteId=%q", title, year, remoteID)
		return cached, nil
	}

	var resp struct {
		Data []tvdbSearchResult `json:"data"`
	}

	params := url.Values{
		"type":  []string{"movie"},
		"limit": []string{"5"},
	}

	// Always set the query parameter
	params.Set("query", title)

	// If we have a remote ID (MDBList ID), add it for more accurate results
	if remoteID != "" {
		params.Set("remote_id", remoteID)
	}

	// Add year if provided
	if year > 0 {
		params.Set("year", fmt.Sprintf("%d", year))
	}

	log.Printf("[tvdb] GET .../search?query=%s&type=movie&year=%d&remote_id=%s", title, year, remoteID)
	if err := s.client.doGET("https://api4.thetvdb.com/v4/search", params, &resp); err != nil {
		return nil, err
	}

	// Cache the result
	if len(resp.Data) > 0 {
		_ = s.cache.set(cacheID, resp.Data)
	}

	return resp.Data, nil
}

// getTVDBSeriesDetails fetches additional details for a series from TVDB
func (s *Service) getTVDBSeriesDetails(tvdbID int64) (tvdbSeries, error) {
	var resp struct {
		Data tvdbSeries `json:"data"`
	}

	endpoint := fmt.Sprintf("https://api4.thetvdb.com/v4/series/%d", tvdbID)
	if err := s.client.doGET(endpoint, nil, &resp); err != nil {
		return tvdbSeries{}, err
	}

	return resp.Data, nil
}

// searchTVDBSeries searches for a series in TVDB by title, year, or remote ID
func (s *Service) searchTVDBSeries(title string, year int, remoteID string) ([]tvdbSearchResult, error) {
	// Create cache key from search parameters
	yearStr := ""
	if year > 0 {
		yearStr = fmt.Sprintf("%d", year)
	}
	cacheID := cacheKey("tvdb", "search", "series", title, yearStr, remoteID)

	// Check cache first
	var cached []tvdbSearchResult
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		log.Printf("[tvdb] series search cache hit query=%q year=%d remoteId=%q", title, year, remoteID)
		return cached, nil
	}

	var resp struct {
		Data []tvdbSearchResult `json:"data"`
	}

	params := url.Values{
		"type":  []string{"series"},
		"limit": []string{"5"},
	}

	// Always set the query parameter
	params.Set("query", title)

	// If we have a remote ID (MDBList ID), add it for more accurate results
	if remoteID != "" {
		params.Set("remote_id", remoteID)
	}

	// Add year if provided
	if year > 0 {
		params.Set("year", fmt.Sprintf("%d", year))
	}

	log.Printf("[tvdb] GET .../search?query=%s&type=series&year=%d&remote_id=%s", title, year, remoteID)
	if err := s.client.doGET("https://api4.thetvdb.com/v4/search", params, &resp); err != nil {
		return nil, err
	}

	// Cache the result
	if len(resp.Data) > 0 {
		_ = s.cache.set(cacheID, resp.Data)
	}

	return resp.Data, nil
}

type tvdbYear int

func (y *tvdbYear) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*y = 0
		return nil
	}

	var intVal int
	if err := json.Unmarshal(data, &intVal); err == nil {
		*y = tvdbYear(intVal)
		return nil
	}

	var strVal string
	if err := json.Unmarshal(data, &strVal); err == nil {
		strVal = strings.TrimSpace(strVal)
		if strVal == "" {
			*y = 0
			return nil
		}
		if parsed := extractYearCandidate(strVal); parsed > 0 {
			*y = tvdbYear(parsed)
			return nil
		}
	}

	*y = 0
	return nil
}

// tvdbSeries represents a TVDB series response
type tvdbSeries struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Overview string   `json:"overview"`
	Year     tvdbYear `json:"year"`
	Score    float64  `json:"score"`
}

// tvdbSearchResult represents the enhanced search response from TVDB
type tvdbSearchResult struct {
	ObjectID        string            `json:"objectID"`
	Name            string            `json:"name"`
	Overview        string            `json:"overview"`
	Year            string            `json:"year"`
	TVDBID          string            `json:"tvdb_id"`
	ImageURL        string            `json:"image_url"`
	Thumbnail       string            `json:"thumbnail"`
	Genres          []string          `json:"genres"`
	Studios         []string          `json:"studios"`
	Director        string            `json:"director"`
	Country         string            `json:"country"`
	Status          string            `json:"status"`
	PrimaryLanguage string            `json:"primary_language"`
	Overviews       map[string]string `json:"overviews"`
	RemoteIDs       []struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		SourceName string `json:"sourceName"`
	} `json:"remote_ids"`
}

// getTrendingSeries uses MDBList to get latest TV shows, enriched with TVDB data
func (s *Service) getTrendingSeries() ([]models.TrendingItem, error) {
	// Fetch latest TV shows from MDBList
	mdblistTVShows, err := s.client.fetchMDBListTVShows()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch MDBList TV shows: %w", err)
	}

	log.Printf("[metadata] fetched %d MDBList TV shows for trending feed", len(mdblistTVShows))

	// Convert to TrendingItem and enrich with TVDB data where possible
	items := make([]models.TrendingItem, 0, len(mdblistTVShows))
	for _, tvShow := range mdblistTVShows {
		// Create base title from MDBList data
		title := models.Title{
			ID:         fmt.Sprintf("mdblist:series:%d", tvShow.ID),
			Name:       tvShow.Title,
			Year:       tvShow.ReleaseYear,
			Language:   s.client.language,
			MediaType:  "series",
			Popularity: float64(100 - tvShow.Rank), // Convert rank to popularity score
		}

		// Set IMDB ID from MDBList
		if tvShow.IMDBID != "" {
			title.IMDBID = tvShow.IMDBID
			log.Printf("[metadata] series imdb id from mdblist title=%q imdbId=%s", tvShow.Title, tvShow.IMDBID)
		}

		// Try to enrich with TVDB data using enhanced search
		var found bool
		var searchResult *tvdbSearchResult

		// First, try to use TVDB ID from MDBList if available
		if tvShow.TVDBID != nil && *tvShow.TVDBID > 0 {
			// Use direct TVDB ID lookup
			if tvdbDetails, err := s.getTVDBSeriesDetails(*tvShow.TVDBID); err == nil {
				title.TVDBID = *tvShow.TVDBID
				title.ID = fmt.Sprintf("tvdb:series:%d", *tvShow.TVDBID)
				title.Overview = tvdbDetails.Overview
				if tvdbDetails.Score > 0 {
					title.Popularity = tvdbDetails.Score
				}
				found = true
			} else {
				log.Printf("[metadata] tvdb series lookup failed id=%d title=%q err=%v", *tvShow.TVDBID, tvShow.Title, err)
			}
		}

		// If direct lookup failed or no TVDB ID available, try search
		if !found {
			// Try to search using MDBList ID as remote_id for more accurate results
			remoteID := fmt.Sprintf("%d", tvShow.ID)
			if searchResults, err := s.searchTVDBSeries(tvShow.Title, tvShow.ReleaseYear, remoteID); err == nil && len(searchResults) > 0 {
				searchResult = &searchResults[0]
				found = true
				log.Printf("[metadata] tvdb series search via remote id matched title=%q remoteId=%s tvdbId=%s", tvShow.Title, remoteID, searchResult.TVDBID)
			} else {
				// Fallback to title/year search if remote_id search fails
				if searchResults, err := s.searchTVDBSeries(tvShow.Title, tvShow.ReleaseYear, ""); err == nil && len(searchResults) > 0 {
					searchResult = &searchResults[0]
					found = true
					log.Printf("[metadata] tvdb series search matched title=%q year=%d tvdbId=%s", tvShow.Title, tvShow.ReleaseYear, searchResult.TVDBID)
				} else if err != nil {
					log.Printf("[metadata] tvdb series search failed title=%q year=%d err=%v", tvShow.Title, tvShow.ReleaseYear, err)
				}
			}
		}

		// Process search result data if we found something via search
		if found && searchResult != nil {
			title.TVDBID, _ = strconv.ParseInt(searchResult.TVDBID, 10, 64)
			title.ID = fmt.Sprintf("tvdb:series:%s", searchResult.TVDBID)
			title.Overview = searchResult.Overview

			// Extract IMDB ID from remote IDs if not already set
			if title.IMDBID == "" {
				for _, remote := range searchResult.RemoteIDs {
					id := strings.TrimSpace(remote.ID)
					if id == "" {
						continue
					}
					lower := strings.ToLower(remote.SourceName)
					if strings.Contains(lower, "imdb") {
						title.IMDBID = id
						log.Printf("[metadata] series imdb id from tvdb search title=%q imdbId=%s", title.Name, id)
						break
					}
				}
			}
			if img := newTVDBImage(searchResult.ImageURL, "poster", 0, 0); img != nil {
				title.Poster = img
			}
			thumbURL := normalizeTVDBImageURL(searchResult.Thumbnail)
			if thumbURL != "" {
				title.Backdrop = &models.Image{URL: thumbURL, Type: "backdrop"}
			} else {
				log.Printf("[metadata] tvdb series thumbnail missing title=%q tvdbId=%d", title.Name, title.TVDBID)
			}
		}

		// If no TVDB enrichment worked, at least provide a basic overview
		if !found && title.Overview == "" {
			log.Printf("[metadata] no tvdb match for series title=%q year=%d", tvShow.Title, tvShow.ReleaseYear)
			title.Overview = fmt.Sprintf("TV series from %d", tvShow.ReleaseYear)
		}

		items = append(items, models.TrendingItem{
			Rank:  tvShow.Rank,
			Title: title,
		})
	}

	// Enrich with artwork for series that have TVDB IDs
	for idx := range items {
		if items[idx].Title.TVDBID > 0 {
			if arts, err := s.client.seriesArtworks(items[idx].Title.TVDBID); err == nil {
				applyTVDBArtworks(&items[idx].Title, arts)
				if items[idx].Title.Backdrop == nil {
					log.Printf("[metadata] no series backdrop from artworks title=%q tvdbId=%d", items[idx].Title.Name, items[idx].Title.TVDBID)
				}
				if items[idx].Title.Poster == nil {
					log.Printf("[metadata] no series poster from artworks title=%q tvdbId=%d", items[idx].Title.Name, items[idx].Title.TVDBID)
				}
			} else {
				log.Printf("[metadata] series artworks fetch failed title=%q tvdbId=%d err=%v", items[idx].Title.Name, items[idx].Title.TVDBID, err)
			}
		}
	}

	return items, nil
}

// Search queries TVDB for series or movies and returns normalized titles.
// The search results will use translated names from the translations field when available,
// preferring the configured language (e.g., English) over the original/primary language.
func (s *Service) Search(ctx context.Context, query string, mediaType string) ([]models.SearchResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []models.SearchResult{}, nil
	}
	if mediaType == "" {
		mediaType = "series"
	}

	// In demo mode, only return matching public domain content
	if s.demo {
		return s.searchDemo(ctx, q, mediaType), nil
	}

	key := cacheKey("tvdb", "search", mediaType, q)
	var cached []models.SearchResult
	if ok, _ := s.cache.get(key, &cached); ok {
		valid := false
		for _, item := range cached {
			if strings.TrimSpace(item.Title.Name) != "" {
				valid = true
				break
			}
		}
		if valid {
			return cached, nil
		}
	}
	var resp struct {
		Data []struct {
			Type            string            `json:"type"`
			ObjectID        string            `json:"objectID"`
			Slug            string            `json:"slug"`
			TVDBID          string            `json:"tvdb_id"`
			TMDBID          string            `json:"tmdb_id"`
			Name            string            `json:"name"`
			Overview        string            `json:"overview"`
			Overviews       map[string]string `json:"overviews"`
			Translations    map[string]string `json:"translations"`
			PrimaryLanguage string            `json:"primary_language"`
			Year            string            `json:"year"`
			FirstAirTime    string            `json:"first_air_time"`
			ImageURL        string            `json:"image_url"`
			Thumbnail       string            `json:"thumbnail"`
			Network         string            `json:"network"`
			RemoteIDs       []struct {
				ID         string `json:"id"`
				SourceName string `json:"sourceName"`
				Type       int    `json:"type"`
			} `json:"remote_ids"`
			Score float64 `json:"score"`
		} `json:"data"`
	}
	// Apply type filter
	t := "series"
	if mediaType == "movie" || mediaType == "movies" {
		mediaType = "movie"
		t = "movie"
	} else {
		mediaType = "series"
	}
	params := url.Values{"query": []string{q}, "type": []string{t}, "limit": []string{"20"}}
	if err := s.client.doGET("https://api4.thetvdb.com/v4/search", params, &resp); err != nil {
		return nil, err
	}
	results := make([]models.SearchResult, 0, len(resp.Data))
	for _, d := range resp.Data {
		entryType := strings.ToLower(strings.TrimSpace(d.Type))
		entryMediaType := mediaType
		switch entryType {
		case "movie", "movies", "film", "films":
			entryMediaType = "movie"
		case "series", "show", "shows", "tv":
			entryMediaType = "series"
		}
		originalName := strings.TrimSpace(d.Name)
		name := originalName
		// Check for translated name in the requested language or English
		if len(d.Translations) > 0 {
			if v := strings.TrimSpace(d.Translations[s.client.language]); v != "" {
				name = v
			} else if v := strings.TrimSpace(d.Translations["eng"]); v != "" {
				name = v
			}
		}
		if name == "" {
			continue
		}
		overview := strings.TrimSpace(d.Overview)
		if len(d.Overviews) > 0 {
			if v := strings.TrimSpace(d.Overviews[s.client.language]); v != "" {
				overview = v
			} else if v := strings.TrimSpace(d.Overviews["eng"]); v != "" {
				overview = v
			}
		}
		year := 0
		if ys := strings.TrimSpace(d.Year); ys != "" {
			if parsedYear := extractYearCandidate(ys); parsedYear > 0 {
				year = parsedYear
			}
		}
		if year == 0 {
			if fas := strings.TrimSpace(d.FirstAirTime); fas != "" {
				if parsedYear := extractYearCandidate(fas); parsedYear > 0 {
					year = parsedYear
				}
			}
		}
		language := strings.TrimSpace(d.PrimaryLanguage)
		if language == "" {
			language = s.client.language
		}
		var tvdbID int64
		if idStr := strings.TrimSpace(d.TVDBID); idStr != "" {
			if parsed, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				tvdbID = parsed
			}
		}
		title := models.Title{
			Name:      name,
			Overview:  overview,
			Year:      year,
			Language:  language,
			MediaType: entryMediaType,
			TVDBID:    tvdbID,
			Network:   strings.TrimSpace(d.Network),
		}
		if originalName != "" && !strings.EqualFold(originalName, name) {
			title.OriginalName = originalName
		}
		aliasSet := make(map[string]struct{})
		var alternateTitles []string
		addAlias := func(candidate string) {
			trimmed := strings.TrimSpace(candidate)
			if trimmed == "" {
				return
			}
			if strings.EqualFold(trimmed, name) {
				return
			}
			lowered := strings.ToLower(trimmed)
			if _, exists := aliasSet[lowered]; exists {
				return
			}
			aliasSet[lowered] = struct{}{}
			alternateTitles = append(alternateTitles, trimmed)
		}
		addAlias(originalName)
		if len(d.Translations) > 0 {
			langs := make([]string, 0, len(d.Translations))
			for lang := range d.Translations {
				langs = append(langs, lang)
			}
			sort.Strings(langs)
			for _, lang := range langs {
				addAlias(d.Translations[lang])
			}
		}
		// Note: Skip fetching aliases here for faster search response.
		// Aliases are already included from translations above.
		// Full alias fetch happens during playback resolution when needed.
		if len(alternateTitles) > 0 {
			title.AlternateTitles = alternateTitles
		}
		if tvdbID > 0 {
			title.ID = fmt.Sprintf("tvdb:%s:%d", entryMediaType, tvdbID)
		}
		if title.ID == "" {
			if slug := strings.TrimSpace(d.Slug); slug != "" {
				title.ID = fmt.Sprintf("tvdb:%s:%s", entryMediaType, slug)
			} else if objectID := strings.TrimSpace(d.ObjectID); objectID != "" {
				title.ID = fmt.Sprintf("tvdb:%s:%s", entryMediaType, objectID)
			}
		}
		if imgURL := normalizeTVDBImageURL(d.ImageURL); imgURL != "" {
			title.Poster = &models.Image{URL: imgURL, Type: "poster"}
		}
		if thumbURL := normalizeTVDBImageURL(d.Thumbnail); thumbURL != "" {
			if title.Poster == nil {
				title.Poster = &models.Image{URL: thumbURL, Type: "poster"}
			}
			title.Backdrop = &models.Image{URL: thumbURL, Type: "backdrop"}
		}
		for _, remote := range d.RemoteIDs {
			id := strings.TrimSpace(remote.ID)
			if id == "" {
				continue
			}
			lower := strings.ToLower(remote.SourceName)
			switch {
			case strings.Contains(lower, "imdb"):
				title.IMDBID = id
			case strings.Contains(lower, "themoviedb") || strings.Contains(lower, "tmdb"):
				if tmdbID, err := strconv.ParseInt(id, 10, 64); err == nil {
					title.TMDBID = tmdbID
				}
			}
		}
		if title.ID == "" {
			// Ensure a stable ID even if TVDB slug is missing
			fallbackID := fmt.Sprintf("tvdb:%s:%s", entryMediaType, strings.ReplaceAll(strings.ToLower(name), " ", "-"))
			title.ID = fallbackID
		}
		score := int(d.Score)
		if d.Score > 0 && score == 0 {
			score = int(d.Score + 0.5)
		}
		results = append(results, models.SearchResult{Title: title, Score: score})
	}
	_ = s.cache.set(key, results)
	return results, nil
}

func (s *Service) fetchTVDBAliases(mediaType string, tvdbID int64) []string {
	if s.client == nil || s.cache == nil || tvdbID <= 0 {
		return nil
	}

	kind := "series"
	fetch := func(id int64) ([]tvdbAlias, error) {
		return s.client.seriesAliases(id)
	}
	if strings.ToLower(strings.TrimSpace(mediaType)) == "movie" {
		kind = "movie"
		fetch = func(id int64) ([]tvdbAlias, error) {
			return s.client.movieAliases(id)
		}
	}

	key := cacheKey("tvdb", "aliases", kind, strconv.FormatInt(tvdbID, 10))
	var cached []string
	if ok, _ := s.cache.get(key, &cached); ok {
		return cached
	}

	aliases, err := fetch(tvdbID)
	if err != nil {
		log.Printf("[metadata] %s aliases fetch failed tvdbId=%d err=%v", kind, tvdbID, err)
		return nil
	}

	names := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias.Name)
		if trimmed == "" {
			continue
		}
		names = append(names, trimmed)
	}

	_ = s.cache.set(key, names)
	return names
}

func (s *Service) resolveSeriesTVDBID(req models.SeriesDetailsQuery) (int64, error) {
	// Fast path: if we already have the TVDB ID, return it
	if req.TVDBID > 0 {
		return req.TVDBID, nil
	}

	if id := parseTVDBIDFromTitleID(req.TitleID); id > 0 {
		return id, nil
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return 0, fmt.Errorf("series name required to resolve tvdb id")
	}

	// Deduplicate concurrent requests for the same series
	requestKey := cacheKey("resolve", "series", name, fmt.Sprintf("%d", req.Year), fmt.Sprintf("%d", req.TMDBID))

	s.inflightMu.Lock()
	if inflight, exists := s.inflightRequests[requestKey]; exists {
		// Another request is already in flight for this series
		s.inflightMu.Unlock()
		log.Printf("[metadata] waiting for inflight tvdb id resolution name=%q year=%d", name, req.Year)
		inflight.wg.Wait()
		return inflight.result, inflight.err
	}

	// Create a new inflight request
	inflight := &inflightRequest{}
	inflight.wg.Add(1)
	s.inflightRequests[requestKey] = inflight
	s.inflightMu.Unlock()

	// Perform the actual resolution
	id, err := s.resolveSeriesTVDBIDActual(req)

	// Store the result and signal completion
	inflight.result = id
	inflight.err = err
	inflight.wg.Done()

	// Clean up the inflight request
	s.inflightMu.Lock()
	delete(s.inflightRequests, requestKey)
	s.inflightMu.Unlock()

	return id, err
}

func (s *Service) resolveSeriesTVDBIDActual(req models.SeriesDetailsQuery) (int64, error) {
	name := strings.TrimSpace(req.Name)

	// Check if we have a cached TMDB→TVDB ID mapping
	if req.TMDBID > 0 {
		cacheID := cacheKey("tvdb", "resolve", "tmdb", fmt.Sprintf("%d", req.TMDBID))
		var cachedTVDBID int64
		if ok, _ := s.cache.get(cacheID, &cachedTVDBID); ok && cachedTVDBID > 0 {
			log.Printf("[metadata] tmdb→tvdb resolution cache hit tmdbId=%d → tvdbId=%d for series %q", req.TMDBID, cachedTVDBID, name)
			return cachedTVDBID, nil
		}
	}

	results, err := s.searchTVDBSeries(name, req.Year, "")
	if err != nil {
		return 0, err
	}

	// If we have a TMDB ID, try to match it exactly first
	if req.TMDBID > 0 {
		tmdbIDStr := fmt.Sprintf("%d", req.TMDBID)
		for _, result := range results {
			if strings.TrimSpace(result.TVDBID) == "" {
				continue
			}
			// Check if this result has matching TMDB ID in remote_ids
			for _, remote := range result.RemoteIDs {
				if strings.Contains(strings.ToLower(remote.SourceName), "themoviedb") ||
					strings.Contains(strings.ToLower(remote.SourceName), "tmdb") {
					if strings.TrimSpace(remote.ID) == tmdbIDStr {
						// Found exact TMDB match!
						id, err := strconv.ParseInt(strings.TrimSpace(result.TVDBID), 10, 64)
						if err == nil {
							log.Printf("[metadata] resolved tvdb id %d via tmdb match tmdbId=%d for series %q", id, req.TMDBID, name)
							// Cache the TMDB→TVDB ID mapping
							cacheID := cacheKey("tvdb", "resolve", "tmdb", fmt.Sprintf("%d", req.TMDBID))
							_ = s.cache.set(cacheID, id)
							return id, nil
						}
					}
				}
			}
		}
	}

	// Filter results to prefer English or original language versions
	// Avoid foreign dubs (Italian, Spanish, French, etc.)
	var englishResults, originalResults, otherResults []tvdbSearchResult
	// Temporarily disabled to allow all language content
	// excludedLanguages := map[string]bool{
	// 	"ita": true, "spa": true, "fra": true, "deu": true, "por": true,
	// 	"tur": true, "pol": true, "rus": true, "ara": true, "kor": true,
	// 	"zho": true, "hin": true, "tha": true, "vie": true,
	// }

	for _, result := range results {
		if strings.TrimSpace(result.TVDBID) == "" {
			continue
		}

		lang := strings.ToLower(strings.TrimSpace(result.PrimaryLanguage))

		// Skip known foreign dubs
		// Temporarily disabled
		// if excludedLanguages[lang] {
		// 	continue
		// }

		// Categorize by language preference
		if lang == "eng" {
			englishResults = append(englishResults, result)
		} else if lang == "jpn" {
			// Japanese is often the original for anime
			originalResults = append(originalResults, result)
		} else {
			otherResults = append(otherResults, result)
		}
	}

	// Try English first, then original language, then any other
	for _, resultSet := range [][]tvdbSearchResult{englishResults, originalResults, otherResults} {
		if len(resultSet) > 0 {
			result := resultSet[0]
			id, err := strconv.ParseInt(strings.TrimSpace(result.TVDBID), 10, 64)
			if err != nil {
				continue
			}
			log.Printf("[metadata] resolved tvdb id %d with language=%q for series %q", id, result.PrimaryLanguage, name)

			// Cache the name-based resolution if we have a TMDB ID (but no TMDB match in results)
			// This avoids re-processing the same query again
			if req.TMDBID > 0 {
				cacheID := cacheKey("tvdb", "resolve", "tmdb", fmt.Sprintf("%d", req.TMDBID))
				_ = s.cache.set(cacheID, id)
			}

			return id, nil
		}
	}

	return 0, fmt.Errorf("no tvdb match found for %q", name)
}

func parseTVDBIDFromTitleID(titleID string) int64 {
	trimmed := strings.TrimSpace(titleID)
	if trimmed == "" {
		return 0
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "tvdb:") {
		parts := strings.Split(trimmed, ":")
		if len(parts) > 0 {
			candidate := strings.TrimSpace(parts[len(parts)-1])
			if id, err := strconv.ParseInt(candidate, 10, 64); err == nil {
				return id
			}
		}
	}

	if id, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return id
	}

	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeTVDBImageURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if u, err := url.Parse(trimmed); err == nil && u.Scheme != "" && u.Host != "" {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "//") {
		return "https:" + trimmed
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "artworks.thetvdb.com") {
		return "https://" + strings.TrimPrefix(trimmed, "//")
	}
	if strings.Contains(lower, "thetvdb.com") {
		return "https://" + strings.TrimPrefix(trimmed, "//")
	}
	if strings.HasPrefix(trimmed, "/") {
		return tvdbArtworkBaseURL + trimmed
	}
	return tvdbArtworkBaseURL + "/" + strings.TrimPrefix(trimmed, "/")
}

func applyTVDBArtworks(title *models.Title, arts []tvdbArtwork) bool {
	if title == nil {
		return false
	}
	updated := false
	for _, art := range arts {
		normalized := normalizeTVDBImageURL(art.Image)
		if normalized == "" {
			continue
		}
		if title.Poster == nil && artworkLooksLikePoster(art) {
			title.Poster = &models.Image{URL: normalized, Type: "poster", Width: art.Width, Height: art.Height}
			updated = true
		}
		if title.Backdrop == nil && artworkLooksLikeBackdrop(art) {
			title.Backdrop = &models.Image{URL: normalized, Type: "backdrop", Width: art.Width, Height: art.Height}
			updated = true
		}
		if title.Poster != nil && title.Backdrop != nil {
			break
		}
	}
	return updated
}

func artworkLooksLikePoster(art tvdbArtwork) bool {
	lt := strings.ToLower(art.Type.String())
	switch {
	case strings.Contains(lt, "poster"), strings.Contains(lt, "cover"):
		return true
	case lt == "2", lt == "4", lt == "14":
		return true
	}
	path := strings.ToLower(art.Image)
	return strings.Contains(path, "poster") || strings.Contains(path, "/covers/")
}

func artworkLooksLikeBackdrop(art tvdbArtwork) bool {
	lt := strings.ToLower(art.Type.String())
	switch {
	case strings.Contains(lt, "background"), strings.Contains(lt, "fanart"), strings.Contains(lt, "backdrop"):
		return true
	case lt == "3", lt == "5", lt == "15":
		return true
	}
	path := strings.ToLower(art.Image)
	return strings.Contains(path, "background") || strings.Contains(path, "fanart") || strings.Contains(path, "backdrop")
}

func newTVDBImage(urlValue, imageType string, width, height int) *models.Image {
	normalized := normalizeTVDBImageURL(urlValue)
	if normalized == "" {
		return nil
	}
	return &models.Image{URL: normalized, Type: imageType, Width: width, Height: height}
}

func (s *Service) SeriesDetails(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	if s.client == nil {
		return nil, fmt.Errorf("tvdb client not configured")
	}

	log.Printf("[metadata] series details request titleId=%q name=%q year=%d tvdbId=%d",

		strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, req.TVDBID)

	tvdbID, err := s.resolveSeriesTVDBID(req)
	if err != nil {

		log.Printf("[metadata] series details resolve error titleId=%q name=%q year=%d err=%v",

			strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, err)
		return nil, err
	}
	if tvdbID <= 0 {

		log.Printf("[metadata] series details resolve missing tvdbId titleId=%q name=%q year=%d",

			strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year)
		return nil, fmt.Errorf("unable to resolve tvdb id for series")
	}

	cacheID := cacheKey("tvdb", "series", "details", "v6", s.client.language, strconv.FormatInt(tvdbID, 10))
	var cached models.SeriesDetails
	if ok, _ := s.cache.get(cacheID, &cached); ok && len(cached.Seasons) > 0 {
		log.Printf("[metadata] series details cache hit tvdbId=%d lang=%s seasons=%d hasPoster=%v hasBackdrop=%v",
			tvdbID, s.client.language, len(cached.Seasons), cached.Title.Poster != nil, cached.Title.Backdrop != nil)

		// If cached data doesn't have backdrop, enrich with artworks
		if cached.Title.Backdrop == nil {
			log.Printf("[metadata] cached series missing backdrop, fetching artworks tvdbId=%d", tvdbID)
			if extended, err := s.client.seriesExtended(tvdbID, []string{"artworks"}); err == nil {
				log.Printf("[metadata] received %d artworks for cached series tvdbId=%d", len(extended.Artworks), tvdbID)
				applyTVDBArtworks(&cached.Title, extended.Artworks)
				if cached.Title.Backdrop != nil {
					log.Printf("[metadata] backdrop added to cached series: %s", cached.Title.Backdrop.URL)
					// Update cache with enriched data
					_ = s.cache.set(cacheID, cached)
				}
			} else {
				log.Printf("[metadata] failed to fetch artworks for cached series tvdbId=%d err=%v", tvdbID, err)
			}
		}

		// If cached data doesn't have credits, fetch them from TMDB
		if cached.Title.Credits == nil && cached.Title.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			log.Printf("[metadata] cached series missing credits, fetching from TMDB tvdbId=%d tmdbId=%d", tvdbID, cached.Title.TMDBID)
			if credits, err := s.tmdb.fetchCredits(ctx, "series", cached.Title.TMDBID); err == nil && credits != nil && len(credits.Cast) > 0 {
				cached.Title.Credits = credits
				log.Printf("[metadata] credits added to cached series: %d cast members", len(credits.Cast))
				// Update cache with enriched data
				_ = s.cache.set(cacheID, cached)
			} else if err != nil {
				log.Printf("[metadata] failed to fetch credits for cached series tmdbId=%d err=%v", cached.Title.TMDBID, err)
			}
		}

		// Only fetch logo if missing - don't replace existing poster to avoid visual flash
		if cached.Title.Logo == nil && cached.Title.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			if images, err := s.tmdb.fetchImages(ctx, "series", cached.Title.TMDBID); err == nil && images != nil {
				if images.Logo != nil {
					cached.Title.Logo = images.Logo
					log.Printf("[metadata] logo added to cached series tmdbId=%d", cached.Title.TMDBID)
					_ = s.cache.set(cacheID, cached)
				}
			}
		}

		// If cached data doesn't have genres, fetch them from TMDB
		if len(cached.Title.Genres) == 0 && cached.Title.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			if genres, err := s.tmdb.fetchSeriesGenres(ctx, cached.Title.TMDBID); err == nil && len(genres) > 0 {
				cached.Title.Genres = genres
				log.Printf("[metadata] genres added to cached series tmdbId=%d", cached.Title.TMDBID)
				_ = s.cache.set(cacheID, cached)
			}
		}

		// Check if IsDaily needs to be set from cached genres (for data cached before daily detection was added)
		if !cached.Title.IsDaily && len(cached.Title.Genres) > 0 {
			for _, genre := range cached.Title.Genres {
				genreLower := strings.ToLower(genre)
				if genreLower == "talk" || genreLower == "talk show" || genreLower == "news" {
					cached.Title.IsDaily = true
					log.Printf("[metadata] cached series marked as daily based on genre tvdbId=%d genre=%q", tvdbID, genre)
					_ = s.cache.set(cacheID, cached)
					break
				}
			}
		}

		// If cached data doesn't have content rating, fetch from TMDB
		if cached.Title.Certification == "" && cached.Title.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			if s.enrichTVContentRating(ctx, &cached.Title, cached.Title.TMDBID) {
				log.Printf("[metadata] content rating added to cached series tmdbId=%d rating=%s", cached.Title.TMDBID, cached.Title.Certification)
				_ = s.cache.set(cacheID, cached)
			}
		}

		// In demo mode, clamp to season 1 only (skip season 0/specials if present)
		if s.demo && len(cached.Seasons) > 0 {
			var season1 *models.SeriesSeason
			for i := range cached.Seasons {
				if cached.Seasons[i].Number == 1 {
					season1 = &cached.Seasons[i]
					break
				}
			}
			if season1 != nil {
				log.Printf("[metadata] demo mode: clamping cached to season 1 only (had %d seasons) tvdbId=%d", len(cached.Seasons), tvdbID)
				cached.Seasons = []models.SeriesSeason{*season1}
			} else if len(cached.Seasons) > 1 {
				log.Printf("[metadata] demo mode: no season 1 in cache, using first season tvdbId=%d", tvdbID)
				cached.Seasons = cached.Seasons[:1]
			}
		}

		return &cached, nil
	}

	log.Printf("[metadata] series details fetch tvdbId=%d", tvdbID)

	base, err := s.getTVDBSeriesDetails(tvdbID)
	if err != nil {
		log.Printf("[metadata] series details tvdb fetch error tvdbId=%d err=%v", tvdbID, err)

		return nil, fmt.Errorf("failed to fetch series details: %w", err)
	}

	extended, err := s.client.seriesExtended(tvdbID, []string{"episodes", "seasons", "artworks"})
	if err != nil {

		log.Printf("[metadata] series details extended fetch error tvdbId=%d err=%v", tvdbID, err)

		return nil, fmt.Errorf("failed to fetch extended series metadata: %w", err)
	}

	// Fetch translations and localized episodes in parallel
	type translationResult struct {
		name     string
		overview string
	}
	translationChan := make(chan translationResult, 1)
	localizedEpsChan := make(chan map[int64]tvdbEpisode, 1)
	seasonTransChan := make(chan map[int64]translationResult, 1)

	// Fetch series translations in background
	go func() {
		var result translationResult
		if translation, err := s.client.seriesTranslations(tvdbID, s.client.language); err == nil && translation != nil {
			result.name = strings.TrimSpace(translation.Name)
			result.overview = strings.TrimSpace(translation.Overview)
		}
		translationChan <- result
	}()

	// Fetch season translations in background (parallel for primary season type only)
	go func() {
		seasonTrans := make(map[int64]translationResult)
		var mu sync.Mutex
		var wg sync.WaitGroup

		// Detect primary season type to only fetch translations for relevant seasons
		primaryType := detectPrimarySeasonType(extended.Seasons)
		if primaryType == "" {
			primaryType = "official"
		}

		for _, season := range extended.Seasons {
			if season.ID <= 0 || season.Number < 0 {
				continue
			}
			// Only fetch translations for seasons matching the primary type
			seasonType := strings.ToLower(strings.TrimSpace(season.Type.Type))
			if seasonType == "" {
				seasonType = strings.ToLower(strings.TrimSpace(season.Type.Name))
			}
			if seasonType != "" && seasonType != primaryType {
				continue
			}
			wg.Add(1)
			go func(seasonID int64) {
				defer wg.Done()
				if translation, err := s.client.seasonTranslations(seasonID, s.client.language); err == nil && translation != nil {
					mu.Lock()
					seasonTrans[seasonID] = translationResult{
						name:     strings.TrimSpace(translation.Name),
						overview: strings.TrimSpace(translation.Overview),
					}
					mu.Unlock()
				}
			}(season.ID)
		}
		wg.Wait()
		seasonTransChan <- seasonTrans
	}()

	// Fetch localized episodes in background
	go func() {
		seasonType := detectPrimarySeasonType(extended.Seasons)
		if seasonType == "" {
			seasonType = "official"
		}
		englishEpisodes := make(map[int64]tvdbEpisode)
		if localized, err := s.client.seriesEpisodesBySeasonType(tvdbID, seasonType, s.client.language); err == nil {
			for _, ep := range localized {
				englishEpisodes[ep.ID] = ep
			}
		}
		localizedEpsChan <- englishEpisodes
	}()

	// Start with defaults from extended data
	translatedName := extended.Name
	translatedOverview := extended.Overview

	// Wait for translation result
	if tr := <-translationChan; tr.name != "" || tr.overview != "" {
		if tr.name != "" {
			translatedName = tr.name
			log.Printf("[metadata] using translated series name tvdbId=%d lang=%s name=%q", tvdbID, s.client.language, tr.name)
		}
		if tr.overview != "" {
			translatedOverview = tr.overview
		}
	}

	finalName := strings.TrimSpace(firstNonEmpty(translatedName, base.Name, req.Name))
	finalOverview := strings.TrimSpace(firstNonEmpty(translatedOverview, base.Overview))

	seriesTitle := models.Title{
		ID:        fmt.Sprintf("tvdb:series:%d", tvdbID),
		Name:      finalName,
		Overview:  finalOverview,
		Year:      int(base.Year),
		Language:  s.client.language,
		MediaType: "series",
		TVDBID:    tvdbID,
	}

	log.Printf("[metadata] series title constructed tvdbId=%d finalName=%q translatedName=%q baseName=%q", tvdbID, finalName, translatedName, base.Name)

	// Extract IMDB ID from remote IDs
	for _, remote := range extended.RemoteIDs {
		id := strings.TrimSpace(remote.ID)
		if id == "" {
			continue
		}
		lower := strings.ToLower(remote.SourceName)
		switch {
		case strings.Contains(lower, "imdb"):
			seriesTitle.IMDBID = id
			log.Printf("[metadata] series imdb id found tvdbId=%d imdbId=%s", tvdbID, id)
		case strings.Contains(lower, "themoviedb") || strings.Contains(lower, "tmdb"):
			if tmdbID, err := strconv.ParseInt(id, 10, 64); err == nil {
				seriesTitle.TMDBID = tmdbID
			}
		}
	}

	if seriesTitle.Year == 0 && int(extended.Year) > 0 {
		seriesTitle.Year = int(extended.Year)
	}

	if extended.Network != "" {
		seriesTitle.Network = extended.Network
	}

	// Set series status (Continuing, Ended, Upcoming, etc.)
	if extended.Status.Name != "" {
		seriesTitle.Status = extended.Status.Name
	}

	// Detect daily shows (talk shows, news, game shows) that use date-based episode naming
	// TVDB types that are typically daily: talk_show, news, game_show
	seriesType := strings.ToLower(strings.TrimSpace(extended.Type))
	switch seriesType {
	case "talk_show", "news", "game_show":
		seriesTitle.IsDaily = true
		log.Printf("[metadata] series marked as daily based on TVDB type tvdbId=%d type=%q", tvdbID, seriesType)
	}

	if img := newTVDBImage(extended.Poster, "poster", 0, 0); img != nil {
		seriesTitle.Poster = img
	} else if img := newTVDBImage(extended.Image, "poster", 0, 0); img != nil {
		seriesTitle.Poster = img
	}
	if backdrop := newTVDBImage(extended.Fanart, "backdrop", 0, 0); backdrop != nil {
		seriesTitle.Backdrop = backdrop
	}

	// Apply artworks from extended response (fetched in single combined call)
	if len(extended.Artworks) > 0 {
		log.Printf("[metadata] received %d artworks for tvdbId=%d", len(extended.Artworks), tvdbID)
		applyTVDBArtworks(&seriesTitle, extended.Artworks)
		if seriesTitle.Backdrop != nil {
			log.Printf("[metadata] series backdrop URL: %s", seriesTitle.Backdrop.URL)
		}
	}

	seasonOrder := make([]int, 0)
	seasonMap := make(map[int]*models.SeriesSeason)

	// Detect the primary season type to filter seasons correctly
	primarySeasonType := detectPrimarySeasonType(extended.Seasons)
	if primarySeasonType == "" {
		primarySeasonType = "official"
	}
	log.Printf("[metadata] using primary season type tvdbId=%d type=%q", tvdbID, primarySeasonType)

	ensureSeason := func(number int) *models.SeriesSeason {
		if number < 0 {
			return nil
		}
		if season, ok := seasonMap[number]; ok {
			return season
		}
		season := &models.SeriesSeason{
			Number:   number,
			Name:     fmt.Sprintf("Season %d", number),
			Episodes: make([]models.SeriesEpisode, 0),
		}
		seasonMap[number] = season
		seasonOrder = append(seasonOrder, number)
		return season
	}

	// Get season translations from parallel fetch
	seasonTranslations := <-seasonTransChan
	log.Printf("[metadata] received season translations tvdbId=%d count=%d", tvdbID, len(seasonTranslations))

	for _, season := range extended.Seasons {
		if season.Number < 0 {
			continue
		}
		// Only process seasons matching the primary season type
		seasonType := strings.ToLower(strings.TrimSpace(season.Type.Type))
		if seasonType == "" {
			seasonType = strings.ToLower(strings.TrimSpace(season.Type.Name))
		}
		if seasonType != "" && seasonType != primarySeasonType {
			continue
		}
		target := ensureSeason(season.Number)
		if target == nil {
			continue
		}
		if season.ID > 0 {
			target.ID = fmt.Sprintf("tvdb:season:%d", season.ID)
			target.TVDBID = season.ID
		}

		// Use translated season name/overview if available
		// If no translation exists for the requested language, keep the default "Season N"
		// to avoid showing non-English names when English is configured
		if trans, ok := seasonTranslations[season.ID]; ok {
			if trans.name != "" {
				target.Name = trans.name
				log.Printf("[metadata] using translated season name tvdbId=%d season=%d lang=%s name=%q", tvdbID, season.Number, s.client.language, trans.name)
			}
			if trans.overview != "" {
				target.Overview = trans.overview
			}
		}
		if season.Type.Name != "" {
			target.Type = season.Type.Name
		} else if season.Type.Type != "" {
			target.Type = season.Type.Type
		}
		if img := newTVDBImage(season.Image, "poster", 0, 0); img != nil {
			target.Image = img
		}
	}

	// Get localized episodes from parallel fetch
	englishEpisodes := <-localizedEpsChan
	log.Printf("[metadata] received localized episodes tvdbId=%d count=%d", tvdbID, len(englishEpisodes))

	episodesWithImage := 0
	episodesWithoutImage := 0
	for _, episode := range extended.Episodes {
		if episode.SeasonNumber < 0 {
			continue
		}
		season := ensureSeason(episode.SeasonNumber)
		if season == nil {
			continue
		}
		var translatedName string
		var translatedOverview string
		if localized, ok := englishEpisodes[episode.ID]; ok {
			if strings.TrimSpace(localized.Name) != "" {
				translatedName = localized.Name
			}
			if strings.TrimSpace(localized.Overview) != "" {
				translatedOverview = localized.Overview
			}
		}
		episodeModel := models.SeriesEpisode{
			ID:                    fmt.Sprintf("tvdb:episode:%d", episode.ID),
			TVDBID:                episode.ID,
			Name:                  strings.TrimSpace(firstNonEmpty(translatedName, episode.Name, episode.Abbreviation)),
			Overview:              strings.TrimSpace(firstNonEmpty(translatedOverview, episode.Overview)),
			SeasonNumber:          episode.SeasonNumber,
			EpisodeNumber:         episode.Number,
			AbsoluteEpisodeNumber: episode.AbsoluteNumber,
			AiredDate:             strings.TrimSpace(episode.Aired),
			Runtime:               episode.Runtime,
		}
		// Debug: log if we get absolute episode numbers
		if episode.AbsoluteNumber > 0 && episode.SeasonNumber > 10 {
			log.Printf("[metadata] Episode S%02dE%02d has absoluteNumber=%d", episode.SeasonNumber, episode.Number, episode.AbsoluteNumber)
		}
		if imgURL := normalizeTVDBImageURL(episode.Image); imgURL != "" {
			episodeModel.Image = &models.Image{URL: imgURL, Type: "still"}
			episodesWithImage++
		} else {
			episodesWithoutImage++
		}
		season.Episodes = append(season.Episodes, episodeModel)
	}

	log.Printf("[metadata] episodes processed tvdbId=%d withImages=%d withoutImages=%d", tvdbID, episodesWithImage, episodesWithoutImage)

	sort.Ints(seasonOrder)
	seasons := make([]models.SeriesSeason, 0, len(seasonOrder))
	for _, number := range seasonOrder {
		season := seasonMap[number]
		if season == nil {
			continue
		}
		if len(season.Episodes) > 0 {
			sort.Slice(season.Episodes, func(i, j int) bool {
				left := season.Episodes[i]
				right := season.Episodes[j]
				if left.EpisodeNumber == right.EpisodeNumber {
					return left.TVDBID < right.TVDBID
				}
				return left.EpisodeNumber < right.EpisodeNumber
			})
		}
		season.EpisodeCount = len(season.Episodes)
		seasons = append(seasons, *season)
	}

	details := models.SeriesDetails{
		Title:   seriesTitle,
		Seasons: seasons,
	}

	// In demo mode, clamp to season 1 only (skip season 0/specials if present)
	if s.demo && len(details.Seasons) > 0 {
		var season1 *models.SeriesSeason
		for i := range details.Seasons {
			if details.Seasons[i].Number == 1 {
				season1 = &details.Seasons[i]
				break
			}
		}
		if season1 != nil {
			log.Printf("[metadata] demo mode: clamping to season 1 only (had %d seasons) tvdbId=%d", len(details.Seasons), tvdbID)
			details.Seasons = []models.SeriesSeason{*season1}
		} else if len(details.Seasons) > 1 {
			// No season 1 found, just take first season
			log.Printf("[metadata] demo mode: no season 1 found, using first season tvdbId=%d", tvdbID)
			details.Seasons = details.Seasons[:1]
		}
	}

	log.Printf("[metadata] series details artwork summary tvdbId=%d seasons=%d episodesWithImages=%d episodesWithoutImages=%d", tvdbID, len(seasons), episodesWithImage, episodesWithoutImage)

	// Fetch ratings from MDBList if enabled and IMDB ID is available
	if seriesTitle.IMDBID != "" && s.mdblist != nil && s.mdblist.IsEnabled() {
		if ratings, err := s.mdblist.GetRatings(ctx, seriesTitle.IMDBID, "show"); err == nil && len(ratings) > 0 {
			seriesTitle.Ratings = ratings
			details.Title = seriesTitle // Update the details with ratings
			log.Printf("[metadata] fetched %d ratings for series imdbId=%s", len(ratings), seriesTitle.IMDBID)
		}
	}

	// Fetch cast credits from TMDB if configured
	if seriesTitle.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		if credits, err := s.tmdb.fetchCredits(ctx, "series", seriesTitle.TMDBID); err == nil && credits != nil && len(credits.Cast) > 0 {
			seriesTitle.Credits = credits
			details.Title = seriesTitle // Update the details with credits
			log.Printf("[metadata] fetched %d cast members for series tmdbId=%d", len(credits.Cast), seriesTitle.TMDBID)
		} else if err != nil {
			log.Printf("[metadata] failed to fetch credits for series tmdbId=%d: %v", seriesTitle.TMDBID, err)
		}
	}

	// Fetch logo and textless poster from TMDB if configured
	if seriesTitle.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		if images, err := s.tmdb.fetchImages(ctx, "series", seriesTitle.TMDBID); err == nil && images != nil {
			if images.Logo != nil {
				seriesTitle.Logo = images.Logo
				log.Printf("[metadata] fetched logo for series tmdbId=%d", seriesTitle.TMDBID)
			}
			if images.TextlessPoster != nil {
				seriesTitle.Poster = images.TextlessPoster
				log.Printf("[metadata] textless poster applied to series tmdbId=%d", seriesTitle.TMDBID)
			}
			details.Title = seriesTitle // Update the details with images
		} else if err != nil {
			log.Printf("[metadata] failed to fetch images for series tmdbId=%d: %v", seriesTitle.TMDBID, err)
		}
	}

	// Fetch genres from TMDB if configured
	if seriesTitle.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		if genres, err := s.tmdb.fetchSeriesGenres(ctx, seriesTitle.TMDBID); err == nil && len(genres) > 0 {
			seriesTitle.Genres = genres
			log.Printf("[metadata] fetched %d genres for series tmdbId=%d", len(genres), seriesTitle.TMDBID)

			// Also check for daily show genres from TMDB if not already detected
			// "Talk" genre (ID 10767) indicates talk shows which use date-based naming
			if !seriesTitle.IsDaily {
				for _, genre := range genres {
					genreLower := strings.ToLower(genre)
					if genreLower == "talk" || genreLower == "talk show" || genreLower == "news" {
						seriesTitle.IsDaily = true
						log.Printf("[metadata] series marked as daily based on TMDB genre tvdbId=%d genre=%q", tvdbID, genre)
						break
					}
				}
			}
			details.Title = seriesTitle
		} else if err != nil {
			log.Printf("[metadata] failed to fetch genres for series tmdbId=%d: %v", seriesTitle.TMDBID, err)
		}
	}

	// Fetch TV content rating from TMDB if configured
	if seriesTitle.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		if s.enrichTVContentRating(ctx, &seriesTitle, seriesTitle.TMDBID) {
			log.Printf("[metadata] fetched content rating for series tmdbId=%d rating=%s", seriesTitle.TMDBID, seriesTitle.Certification)
			details.Title = seriesTitle
		}
	}

	_ = s.cache.set(cacheID, details)

	log.Printf("[metadata] series details complete tvdbId=%d seasons=%d", tvdbID, len(seasons))

	return &details, nil
}

// SeriesDetailsLite is a lightweight variant of SeriesDetails optimised for
// continue-watching and other contexts that only need poster, backdrop, overview,
// IDs, year and a basic episode list (season/episode numbers + air dates).
// It skips: getTVDBSeriesDetails, season translations, localized episode names,
// MDBList ratings, and all TMDB enrichment (credits, images, genres, content rating).
// The result is written to the same file cache key as SeriesDetails so a subsequent
// full-detail call for the same series will get a cache hit.
func (s *Service) SeriesDetailsLite(ctx context.Context, req models.SeriesDetailsQuery) (*models.SeriesDetails, error) {
	if s.client == nil {
		return nil, fmt.Errorf("tvdb client not configured")
	}

	log.Printf("[metadata] series details lite request titleId=%q name=%q year=%d tvdbId=%d",
		strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, req.TVDBID)

	tvdbID, err := s.resolveSeriesTVDBID(req)
	if err != nil {
		return nil, err
	}
	if tvdbID <= 0 {
		return nil, fmt.Errorf("unable to resolve tvdb id for series")
	}

	// Check the same file cache used by SeriesDetails
	cacheID := cacheKey("tvdb", "series", "details", "v6", s.client.language, strconv.FormatInt(tvdbID, 10))
	var cached models.SeriesDetails
	if ok, _ := s.cache.get(cacheID, &cached); ok && len(cached.Seasons) > 0 {
		log.Printf("[metadata] series details lite cache hit tvdbId=%d seasons=%d", tvdbID, len(cached.Seasons))
		return &cached, nil
	}

	log.Printf("[metadata] series details lite fetch tvdbId=%d", tvdbID)

	// Fetch extended data and translations in parallel — only 2 HTTP calls
	type transResult struct {
		name     string
		overview string
	}

	extChan := make(chan struct {
		data tvdbSeriesExtendedData
		err  error
	}, 1)
	transChan := make(chan transResult, 1)

	go func() {
		ext, err := s.client.seriesExtended(tvdbID, []string{"episodes", "seasons", "artworks"})
		extChan <- struct {
			data tvdbSeriesExtendedData
			err  error
		}{ext, err}
	}()

	go func() {
		var result transResult
		if tr, err := s.client.seriesTranslations(tvdbID, s.client.language); err == nil && tr != nil {
			result.name = strings.TrimSpace(tr.Name)
			result.overview = strings.TrimSpace(tr.Overview)
		}
		transChan <- result
	}()

	extResult := <-extChan
	if extResult.err != nil {
		return nil, fmt.Errorf("failed to fetch extended series metadata: %w", extResult.err)
	}
	extended := extResult.data
	tr := <-transChan

	// Build title from extended data + translation
	translatedName := extended.Name
	translatedOverview := extended.Overview
	if tr.name != "" {
		translatedName = tr.name
	}
	if tr.overview != "" {
		translatedOverview = tr.overview
	}

	seriesTitle := models.Title{
		ID:        fmt.Sprintf("tvdb:series:%d", tvdbID),
		Name:      strings.TrimSpace(firstNonEmpty(translatedName, extended.Name, req.Name)),
		Overview:  strings.TrimSpace(firstNonEmpty(translatedOverview, extended.Overview)),
		Year:      int(extended.Year),
		Language:  s.client.language,
		MediaType: "series",
		TVDBID:    tvdbID,
	}

	// Extract IMDB and TMDB IDs from remote IDs
	for _, remote := range extended.RemoteIDs {
		id := strings.TrimSpace(remote.ID)
		if id == "" {
			continue
		}
		lower := strings.ToLower(remote.SourceName)
		switch {
		case strings.Contains(lower, "imdb"):
			seriesTitle.IMDBID = id
		case strings.Contains(lower, "themoviedb") || strings.Contains(lower, "tmdb"):
			if tmdbID, err := strconv.ParseInt(id, 10, 64); err == nil {
				seriesTitle.TMDBID = tmdbID
			}
		}
	}

	if extended.Network != "" {
		seriesTitle.Network = extended.Network
	}
	if extended.Status.Name != "" {
		seriesTitle.Status = extended.Status.Name
	}

	// Artwork: poster + backdrop
	if img := newTVDBImage(extended.Poster, "poster", 0, 0); img != nil {
		seriesTitle.Poster = img
	} else if img := newTVDBImage(extended.Image, "poster", 0, 0); img != nil {
		seriesTitle.Poster = img
	}
	if backdrop := newTVDBImage(extended.Fanart, "backdrop", 0, 0); backdrop != nil {
		seriesTitle.Backdrop = backdrop
	}
	if len(extended.Artworks) > 0 {
		applyTVDBArtworks(&seriesTitle, extended.Artworks)
	}

	// Build seasons and episodes from extended data (no localized episode fetch)
	primarySeasonType := detectPrimarySeasonType(extended.Seasons)
	if primarySeasonType == "" {
		primarySeasonType = "official"
	}

	seasonOrder := make([]int, 0)
	seasonMap := make(map[int]*models.SeriesSeason)
	ensureSeason := func(number int) *models.SeriesSeason {
		if number < 0 {
			return nil
		}
		if season, ok := seasonMap[number]; ok {
			return season
		}
		season := &models.SeriesSeason{
			Number:   number,
			Name:     fmt.Sprintf("Season %d", number),
			Episodes: make([]models.SeriesEpisode, 0),
		}
		seasonMap[number] = season
		seasonOrder = append(seasonOrder, number)
		return season
	}

	for _, season := range extended.Seasons {
		if season.Number < 0 {
			continue
		}
		seasonType := strings.ToLower(strings.TrimSpace(season.Type.Type))
		if seasonType == "" {
			seasonType = strings.ToLower(strings.TrimSpace(season.Type.Name))
		}
		if seasonType != "" && seasonType != primarySeasonType {
			continue
		}
		target := ensureSeason(season.Number)
		if target == nil {
			continue
		}
		if season.ID > 0 {
			target.ID = fmt.Sprintf("tvdb:season:%d", season.ID)
			target.TVDBID = season.ID
		}
		if season.Type.Name != "" {
			target.Type = season.Type.Name
		} else if season.Type.Type != "" {
			target.Type = season.Type.Type
		}
		if img := newTVDBImage(season.Image, "poster", 0, 0); img != nil {
			target.Image = img
		}
	}

	for _, episode := range extended.Episodes {
		if episode.SeasonNumber < 0 {
			continue
		}
		season := ensureSeason(episode.SeasonNumber)
		if season == nil {
			continue
		}
		ep := models.SeriesEpisode{
			ID:                    fmt.Sprintf("tvdb:episode:%d", episode.ID),
			TVDBID:                episode.ID,
			Name:                  strings.TrimSpace(firstNonEmpty(episode.Name, episode.Abbreviation)),
			Overview:              strings.TrimSpace(episode.Overview),
			SeasonNumber:          episode.SeasonNumber,
			EpisodeNumber:         episode.Number,
			AbsoluteEpisodeNumber: episode.AbsoluteNumber,
			AiredDate:             strings.TrimSpace(episode.Aired),
			Runtime:               episode.Runtime,
		}
		if imgURL := normalizeTVDBImageURL(episode.Image); imgURL != "" {
			ep.Image = &models.Image{URL: imgURL, Type: "still"}
		}
		season.Episodes = append(season.Episodes, ep)
	}

	sort.Ints(seasonOrder)
	seasons := make([]models.SeriesSeason, 0, len(seasonOrder))
	for _, number := range seasonOrder {
		season := seasonMap[number]
		if season == nil {
			continue
		}
		if len(season.Episodes) > 0 {
			sort.Slice(season.Episodes, func(i, j int) bool {
				left := season.Episodes[i]
				right := season.Episodes[j]
				if left.EpisodeNumber == right.EpisodeNumber {
					return left.TVDBID < right.TVDBID
				}
				return left.EpisodeNumber < right.EpisodeNumber
			})
		}
		season.EpisodeCount = len(season.Episodes)
		seasons = append(seasons, *season)
	}

	details := models.SeriesDetails{
		Title:   seriesTitle,
		Seasons: seasons,
	}

	// In demo mode, clamp to season 1 only
	if s.demo && len(details.Seasons) > 0 {
		var season1 *models.SeriesSeason
		for i := range details.Seasons {
			if details.Seasons[i].Number == 1 {
				season1 = &details.Seasons[i]
				break
			}
		}
		if season1 != nil {
			details.Seasons = []models.SeriesSeason{*season1}
		} else if len(details.Seasons) > 1 {
			details.Seasons = details.Seasons[:1]
		}
	}

	_ = s.cache.set(cacheID, details)

	log.Printf("[metadata] series details lite complete tvdbId=%d seasons=%d", tvdbID, len(details.Seasons))

	return &details, nil
}

// BatchSeriesDetails fetches metadata for multiple series efficiently.
// It checks the cache first for all queries and fetches uncached items concurrently.
func (s *Service) BatchSeriesDetails(ctx context.Context, queries []models.SeriesDetailsQuery) []models.BatchSeriesDetailsItem {
	if len(queries) == 0 {
		return []models.BatchSeriesDetailsItem{}
	}

	results := make([]models.BatchSeriesDetailsItem, len(queries))

	// Track which indices need fetching
	type fetchTask struct {
		index int
		query models.SeriesDetailsQuery
	}
	var tasksToFetch []fetchTask

	// First pass: check cache for all queries
	for i, query := range queries {
		results[i].Query = query

		// Try to get from cache using the same logic as SeriesDetails
		tvdbID, err := s.resolveSeriesTVDBID(query)
		if err != nil {
			results[i].Error = err.Error()
			continue
		}
		if tvdbID <= 0 {
			results[i].Error = "unable to resolve tvdb id for series"
			continue
		}

		cacheID := cacheKey("tvdb", "series", "details", "v6", s.client.language, strconv.FormatInt(tvdbID, 10))
		var cached models.SeriesDetails
		if ok, _ := s.cache.get(cacheID, &cached); ok && len(cached.Seasons) > 0 {
			log.Printf("[metadata] batch series cache hit index=%d tvdbId=%d name=%q", i, tvdbID, query.Name)
			results[i].Details = &cached
		} else {
			// Need to fetch this one
			tasksToFetch = append(tasksToFetch, fetchTask{index: i, query: query})
		}
	}

	// If nothing to fetch, return early
	if len(tasksToFetch) == 0 {
		log.Printf("[metadata] batch series all cached count=%d", len(queries))
		return results
	}

	log.Printf("[metadata] batch series fetching cached=%d uncached=%d total=%d",
		len(queries)-len(tasksToFetch), len(tasksToFetch), len(queries))

	// Second pass: fetch uncached items concurrently with controlled parallelism
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, task := range tasksToFetch {
		wg.Add(1)
		go func(idx int, q models.SeriesDetailsQuery) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Fetch the details
			details, err := s.SeriesDetails(ctx, q)
			if err != nil {
				results[idx].Error = err.Error()
				log.Printf("[metadata] batch series fetch error index=%d name=%q err=%v", idx, q.Name, err)
			} else {
				results[idx].Details = details
				log.Printf("[metadata] batch series fetch success index=%d name=%q", idx, q.Name)
			}
		}(task.index, task.query)
	}

	wg.Wait()
	log.Printf("[metadata] batch series complete total=%d", len(queries))
	return results
}

// BatchMovieReleases fetches release data for multiple movies efficiently.
// It checks the cache first for all queries and fetches uncached items concurrently.
func (s *Service) BatchMovieReleases(ctx context.Context, queries []models.BatchMovieReleasesQuery) []models.BatchMovieReleasesItem {
	if len(queries) == 0 {
		return []models.BatchMovieReleasesItem{}
	}

	results := make([]models.BatchMovieReleasesItem, len(queries))

	// Track which indices need fetching
	type fetchTask struct {
		index  int
		tmdbID int64
	}
	var tasksToFetch []fetchTask

	// First pass: check cache for all queries
	for i, query := range queries {
		results[i].Query = query

		tmdbID := query.TMDBID
		if tmdbID <= 0 {
			// Try to extract TMDB ID from titleId if it's in format "tmdb:movie:123"
			if strings.HasPrefix(query.TitleID, "tmdb:movie:") {
				if id, err := strconv.ParseInt(strings.TrimPrefix(query.TitleID, "tmdb:movie:"), 10, 64); err == nil {
					tmdbID = id
				}
			}
		}

		// If still no TMDB ID but we have IMDB ID, look up TMDB ID (using cached lookup)
		if tmdbID <= 0 && query.IMDBID != "" {
			if id := s.getTMDBIDForIMDB(ctx, query.IMDBID); id > 0 {
				tmdbID = id
				log.Printf("[metadata] resolved IMDB %s to TMDB %d (cached lookup)", query.IMDBID, tmdbID)
			}
		}

		if tmdbID <= 0 {
			results[i].Error = "tmdb id required for release data (could not resolve from imdb)"
			continue
		}

		// Check cache
		cacheID := cacheKey("tmdb", "movie", "releases", "v1", strconv.FormatInt(tmdbID, 10))
		var cached []models.Release
		if ok, _ := s.cache.get(cacheID, &cached); ok && len(cached) > 0 {
			// Build a temporary title to use ensureMovieReleasePointers
			tempTitle := &models.Title{Releases: cached}
			s.ensureMovieReleasePointers(tempTitle)
			results[i].Theatrical = tempTitle.Theatrical
			results[i].HomeRelease = tempTitle.HomeRelease
			continue
		}

		// Need to fetch
		tasksToFetch = append(tasksToFetch, fetchTask{index: i, tmdbID: tmdbID})
	}

	if len(tasksToFetch) == 0 {
		log.Printf("[metadata] batch movie releases complete (all cached) total=%d", len(queries))
		return results
	}

	// Fetch uncached items concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Limit concurrency to avoid overwhelming TMDB
	sem := make(chan struct{}, 5)

	for _, task := range tasksToFetch {
		wg.Add(1)
		go func(t fetchTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			tempTitle := &models.Title{TMDBID: t.tmdbID}
			if s.enrichMovieReleases(ctx, tempTitle, t.tmdbID) {
				mu.Lock()
				results[t.index].Theatrical = tempTitle.Theatrical
				results[t.index].HomeRelease = tempTitle.HomeRelease
				mu.Unlock()
			} else {
				mu.Lock()
				results[t.index].Error = "failed to fetch release data"
				mu.Unlock()
			}
		}(task)
	}

	wg.Wait()
	log.Printf("[metadata] batch movie releases complete total=%d fetched=%d", len(queries), len(tasksToFetch))
	return results
}

// SeriesInfo fetches lightweight series metadata (poster, backdrop, external IDs) without episodes.
// This is useful for continue watching where we only need series-level metadata.
func (s *Service) SeriesInfo(ctx context.Context, req models.SeriesDetailsQuery) (*models.Title, error) {
	if s.client == nil {
		return nil, fmt.Errorf("tvdb client not configured")
	}

	log.Printf("[metadata] series info request (lightweight) titleId=%q name=%q year=%d tvdbId=%d",
		strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, req.TVDBID)

	tvdbID, err := s.resolveSeriesTVDBID(req)
	if err != nil {
		log.Printf("[metadata] series info resolve error titleId=%q name=%q year=%d err=%v",
			strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, err)
		return nil, err
	}
	if tvdbID <= 0 {
		log.Printf("[metadata] series info resolve missing tvdbId titleId=%q name=%q year=%d",
			strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year)
		return nil, fmt.Errorf("unable to resolve tvdb id for series")
	}

	// Check cache first
	cacheID := cacheKey("tvdb", "series", "info", "v1", s.client.language, strconv.FormatInt(tvdbID, 10))
	var cached models.Title
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		log.Printf("[metadata] series info cache hit tvdbId=%d lang=%s hasPoster=%v hasBackdrop=%v",
			tvdbID, s.client.language, cached.Poster != nil, cached.Backdrop != nil)
		return &cached, nil
	}

	log.Printf("[metadata] series info fetch tvdbId=%d", tvdbID)

	// Fetch basic series info (without episodes/seasons)
	base, err := s.getTVDBSeriesDetails(tvdbID)
	if err != nil {
		log.Printf("[metadata] series info tvdb fetch error tvdbId=%d err=%v", tvdbID, err)
		return nil, fmt.Errorf("failed to fetch series info: %w", err)
	}

	// Fetch extended data with artworks only (no episodes)
	extended, err := s.client.seriesExtended(tvdbID, []string{"artworks"})
	if err != nil {
		log.Printf("[metadata] series info extended fetch error tvdbId=%d err=%v", tvdbID, err)
		return nil, fmt.Errorf("failed to fetch extended series info: %w", err)
	}

	// Fetch translations for series name and overview
	translatedName := extended.Name
	translatedOverview := extended.Overview

	if translation, err := s.client.seriesTranslations(tvdbID, s.client.language); err == nil && translation != nil {
		if strings.TrimSpace(translation.Name) != "" {
			translatedName = translation.Name
			log.Printf("[metadata] using translated series name tvdbId=%d lang=%s name=%q", tvdbID, s.client.language, translation.Name)
		}
		if strings.TrimSpace(translation.Overview) != "" {
			translatedOverview = translation.Overview
		}
	} else if err != nil {
		log.Printf("[metadata] failed to fetch series translations tvdbId=%d lang=%s err=%v", tvdbID, s.client.language, err)
	}

	finalName := strings.TrimSpace(firstNonEmpty(translatedName, base.Name, req.Name))
	finalOverview := strings.TrimSpace(firstNonEmpty(translatedOverview, base.Overview))

	seriesTitle := models.Title{
		ID:        fmt.Sprintf("tvdb:series:%d", tvdbID),
		Name:      finalName,
		Overview:  finalOverview,
		Year:      int(base.Year),
		Language:  s.client.language,
		MediaType: "series",
		TVDBID:    tvdbID,
	}

	// Extract IMDB ID and TMDB ID from remote IDs
	for _, remote := range extended.RemoteIDs {
		id := strings.TrimSpace(remote.ID)
		if id == "" {
			continue
		}
		lower := strings.ToLower(remote.SourceName)
		switch {
		case strings.Contains(lower, "imdb"):
			seriesTitle.IMDBID = id
		case strings.Contains(lower, "themoviedb") || strings.Contains(lower, "tmdb"):
			if tmdbID, err := strconv.ParseInt(id, 10, 64); err == nil {
				seriesTitle.TMDBID = tmdbID
			}
		}
	}

	if seriesTitle.Year == 0 && int(extended.Year) > 0 {
		seriesTitle.Year = int(extended.Year)
	}

	if extended.Network != "" {
		seriesTitle.Network = extended.Network
	}

	// Set series status (Continuing, Ended, Upcoming, etc.)
	if extended.Status.Name != "" {
		seriesTitle.Status = extended.Status.Name
	}

	// Apply artworks (poster and backdrop)
	if img := newTVDBImage(extended.Poster, "poster", 0, 0); img != nil {
		seriesTitle.Poster = img
	} else if img := newTVDBImage(extended.Image, "poster", 0, 0); img != nil {
		seriesTitle.Poster = img
	}
	if backdrop := newTVDBImage(extended.Fanart, "backdrop", 0, 0); backdrop != nil {
		seriesTitle.Backdrop = backdrop
	}

	// Apply additional artworks from the artworks array
	applyTVDBArtworks(&seriesTitle, extended.Artworks)

	// Note: Ratings are NOT fetched here to keep this lightweight.
	// Use SeriesDetails for full metadata including ratings.

	log.Printf("[metadata] series info complete tvdbId=%d name=%q hasPoster=%v hasBackdrop=%v",
		tvdbID, finalName, seriesTitle.Poster != nil, seriesTitle.Backdrop != nil)

	// Cache the result
	_ = s.cache.set(cacheID, seriesTitle)

	return &seriesTitle, nil
}

// MovieInfo fetches lightweight movie metadata (poster, backdrop, external IDs) without ratings.
// This is useful for continue watching where we only need basic movie info.
func (s *Service) MovieInfo(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	// Use MovieDetails but skip ratings by calling the internal implementation
	return s.movieDetailsInternal(ctx, req, false)
}

// MovieDetails fetches metadata for a movie including poster, backdrop, and ratings.
func (s *Service) MovieDetails(ctx context.Context, req models.MovieDetailsQuery) (*models.Title, error) {
	return s.movieDetailsInternal(ctx, req, true)
}

// CollectionDetails fetches details for a movie collection from TMDB.
func (s *Service) CollectionDetails(ctx context.Context, collectionID int64) (*models.CollectionDetails, error) {
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, fmt.Errorf("tmdb client not configured")
	}
	return s.tmdb.fetchCollectionDetails(ctx, collectionID)
}

// Similar fetches similar movies or TV shows from TMDB.
// Results are cached to avoid repeated API calls.
func (s *Service) Similar(ctx context.Context, mediaType string, tmdbID int64) ([]models.Title, error) {
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, fmt.Errorf("tmdb client not configured")
	}

	if tmdbID <= 0 {
		return nil, fmt.Errorf("tmdb id required")
	}

	// Normalize media type
	normalizedType := strings.ToLower(strings.TrimSpace(mediaType))
	if normalizedType != "movie" {
		normalizedType = "series"
	}

	// Check cache first
	cacheID := cacheKey("tmdb", "similar", normalizedType, fmt.Sprintf("%d", tmdbID))
	var cached []models.Title
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		log.Printf("[metadata] similar cache hit type=%s tmdbId=%d count=%d", normalizedType, tmdbID, len(cached))
		return cached, nil
	}

	// Fetch from TMDB
	titles, err := s.tmdb.fetchSimilar(ctx, normalizedType, tmdbID)
	if err != nil {
		log.Printf("[metadata] similar fetch failed type=%s tmdbId=%d: %v", normalizedType, tmdbID, err)
		return nil, err
	}

	// Cache the result
	if err := s.cache.set(cacheID, titles); err != nil {
		log.Printf("[metadata] failed to cache similar results: %v", err)
	}

	log.Printf("[metadata] similar fetch success type=%s tmdbId=%d count=%d", normalizedType, tmdbID, len(titles))
	return titles, nil
}

// PersonDetails retrieves detailed information about a person and their filmography
func (s *Service) PersonDetails(ctx context.Context, personID int64) (*models.PersonDetails, error) {
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, fmt.Errorf("tmdb client not configured")
	}

	if personID <= 0 {
		return nil, fmt.Errorf("person id required")
	}

	// Check cache first
	cacheID := cacheKey("tmdb", "person", "details", fmt.Sprintf("%d", personID))
	var cached models.PersonDetails
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		log.Printf("[metadata] person details cache hit personId=%d filmography=%d", personID, len(cached.Filmography))
		return &cached, nil
	}

	// Fetch person details
	person, err := s.tmdb.fetchPersonDetails(ctx, personID)
	if err != nil {
		log.Printf("[metadata] person details fetch failed personId=%d: %v", personID, err)
		return nil, err
	}

	// Fetch combined credits (filmography)
	filmography, err := s.tmdb.fetchPersonCombinedCredits(ctx, personID)
	if err != nil {
		log.Printf("[metadata] person credits fetch failed personId=%d: %v", personID, err)
		// Don't fail completely - return person details without filmography
		filmography = []models.Title{}
	}

	// Apply bio mention bonus - titles mentioned in biography get a boost
	if person.Biography != "" && len(filmography) > 0 {
		filmography = applyBioMentionBonus(person.Biography, filmography)
	}

	result := &models.PersonDetails{
		Person:      *person,
		Filmography: filmography,
	}

	// Cache the result
	if err := s.cache.set(cacheID, result); err != nil {
		log.Printf("[metadata] failed to cache person details: %v", err)
	}

	log.Printf("[metadata] person details fetch success personId=%d name=%q filmography=%d", personID, person.Name, len(filmography))
	return result, nil
}

// applyBioMentionBonus boosts filmography entries that are mentioned in the person's biography.
// This helps surface notable works that TMDB editors have highlighted.
func applyBioMentionBonus(biography string, filmography []models.Title) []models.Title {
	// Normalize biography for matching (lowercase)
	bioLower := strings.ToLower(biography)

	// Apply bonus to titles mentioned in bio
	boostedCount := 0
	for i := range filmography {
		title := &filmography[i]
		normalizedTitle := normalizeTitleForBioMatch(title.Name)

		// Check if the normalized title appears in the biography
		if normalizedTitle != "" && strings.Contains(bioLower, strings.ToLower(normalizedTitle)) {
			oldScore := title.Popularity
			// Apply 1.5x bonus for bio mentions
			title.Popularity *= 1.5
			log.Printf("[metadata] bio bonus: %q matched, score %.1f -> %.1f", title.Name, oldScore, title.Popularity)
			boostedCount++
		}
	}
	log.Printf("[metadata] bio bonus applied to %d/%d titles", boostedCount, len(filmography))

	// Re-sort by updated popularity scores
	sort.Slice(filmography, func(i, j int) bool {
		return filmography[i].Popularity > filmography[j].Popularity
	})

	return filmography
}

// normalizeTitleForBioMatch strips common articles and prepares title for bio matching.
// Returns empty string if title is too short or generic to match reliably.
func normalizeTitleForBioMatch(title string) string {
	// Strip leading articles
	normalized := strings.TrimSpace(title)
	for _, article := range []string{"The ", "A ", "An "} {
		if strings.HasPrefix(normalized, article) {
			normalized = strings.TrimPrefix(normalized, article)
			break
		}
	}

	// Skip very short titles (too likely to false match)
	if len(normalized) < 4 {
		return ""
	}

	return normalized
}

// movieDetailsInternal is the shared implementation for MovieInfo and MovieDetails.
func (s *Service) movieDetailsInternal(ctx context.Context, req models.MovieDetailsQuery, includeRatings bool) (*models.Title, error) {
	if s.client == nil {
		return nil, fmt.Errorf("tvdb client not configured")
	}

	log.Printf("[metadata] movie details request titleId=%q name=%q year=%d tvdbId=%d tmdbId=%d imdbId=%s",
		strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, req.TVDBID, req.TMDBID, strings.TrimSpace(req.IMDBID))

	// Try to resolve TVDB ID
	tvdbID := req.TVDBID

	// If no TVDB ID, try to parse from TitleID
	if tvdbID <= 0 {
		tvdbID = parseTVDBIDFromTitleID(req.TitleID)
	}

	// If still no TVDB ID, try TMDB or search
	if tvdbID <= 0 {
		// Check if we have a cached TMDB→TVDB ID mapping
		if req.TMDBID > 0 {
			cacheID := cacheKey("tvdb", "resolve", "movie", "tmdb", fmt.Sprintf("%d", req.TMDBID))
			var cachedTVDBID int64
			if ok, _ := s.cache.get(cacheID, &cachedTVDBID); ok && cachedTVDBID > 0 {
				tvdbID = cachedTVDBID
				log.Printf("[metadata] movie tmdb→tvdb resolution cache hit tmdbId=%d → tvdbId=%d", req.TMDBID, tvdbID)
			}
		}

		if tvdbID <= 0 && req.TMDBID > 0 {
			// Try to find TVDB ID from TMDB ID via search
			// This is a fallback - we'll just use what we have
			log.Printf("[metadata] movie has TMDB ID but no TVDB ID, will attempt search tmdbId=%d", req.TMDBID)
		}

		// Try search if we have a name
		if tvdbID <= 0 && strings.TrimSpace(req.Name) != "" {
			results, err := s.searchTVDBMovie(req.Name, req.Year, "")
			if err != nil {
				log.Printf("[metadata] movie tvdb search error name=%q year=%d err=%v", req.Name, req.Year, err)
			} else if len(results) == 0 {
				log.Printf("[metadata] movie tvdb search returned 0 results name=%q year=%d", req.Name, req.Year)
				// Fallback: retry without year constraint
				if req.Year > 0 {
					log.Printf("[metadata] movie tvdb search retrying without year name=%q", req.Name)
					results, err = s.searchTVDBMovie(req.Name, 0, "")
					if err != nil {
						log.Printf("[metadata] movie tvdb search (no year) error name=%q err=%v", req.Name, err)
					} else if len(results) > 0 {
						log.Printf("[metadata] movie tvdb search (no year) found %d results name=%q", len(results), req.Name)
					}
				}
			}
			// Process results if we have any
			if err == nil && len(results) > 0 {
				if results[0].TVDBID == "" {
					log.Printf("[metadata] movie tvdb search result has no tvdb_id name=%q year=%d firstResultName=%q", req.Name, req.Year, results[0].Name)
				} else if id, err := strconv.ParseInt(results[0].TVDBID, 10, 64); err != nil {
					log.Printf("[metadata] movie tvdb search result has invalid tvdb_id name=%q year=%d tvdbId=%q err=%v", req.Name, req.Year, results[0].TVDBID, err)
				} else {
					tvdbID = id
					log.Printf("[metadata] movie search found tvdbId=%d name=%q year=%d", tvdbID, req.Name, req.Year)

					// Cache the TMDB→TVDB ID mapping if we have a TMDB ID
					if req.TMDBID > 0 {
						cacheID := cacheKey("tvdb", "resolve", "movie", "tmdb", fmt.Sprintf("%d", req.TMDBID))
						_ = s.cache.set(cacheID, id)
					}
				}
			}
		}
	}

	if tvdbID <= 0 {
		log.Printf("[metadata] movie details unable to resolve tvdb id titleId=%q name=%q year=%d", req.TitleID, req.Name, req.Year)

		// If we have a TMDB ID but no TVDB ID, try to use TMDB directly as a fallback
		if req.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			log.Printf("[metadata] attempting to use TMDB directly for movie details tmdbId=%d", req.TMDBID)
			return s.getMovieDetailsFromTMDB(ctx, req)
		}

		return nil, fmt.Errorf("unable to resolve tvdb id for movie and no tmdb fallback available")
	}

	// Check cache (v2 adds collection data)
	cacheID := cacheKey("tvdb", "movie", "details", "v3", s.client.language, strconv.FormatInt(tvdbID, 10))
	var cached models.Title
	if ok, _ := s.cache.get(cacheID, &cached); ok && cached.ID != "" {
		log.Printf("[metadata] movie details cache hit tvdbId=%d lang=%s", tvdbID, s.client.language)

		// Older cache entries may predate TMDB artwork/runtime hydration. Refresh them on the fly.
		if (cached.Poster == nil || cached.Backdrop == nil || cached.RuntimeMinutes == 0) && s.maybeHydrateMovieArtworkFromTMDB(ctx, &cached, req) {
			_ = s.cache.set(cacheID, cached)
		}
		if len(cached.Releases) == 0 && s.enrichMovieReleases(ctx, &cached, cached.TMDBID) {
			_ = s.cache.set(cacheID, cached)
		} else {
			s.ensureMovieReleasePointers(&cached)
		}

		// Enrich with credits if missing
		tmdbIDForCredits := cached.TMDBID
		if tmdbIDForCredits == 0 {
			tmdbIDForCredits = req.TMDBID
		}
		if cached.Credits == nil && tmdbIDForCredits > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			if credits, err := s.tmdb.fetchCredits(ctx, "movie", tmdbIDForCredits); err == nil && credits != nil && len(credits.Cast) > 0 {
				cached.Credits = credits
				log.Printf("[metadata] credits added to cached movie: %d cast members tmdbId=%d", len(credits.Cast), tmdbIDForCredits)
				_ = s.cache.set(cacheID, cached)
			}
		}

		// Enrich with logo and textless poster if available
		tmdbIDForImages := cached.TMDBID
		if tmdbIDForImages == 0 {
			tmdbIDForImages = req.TMDBID
		}
		// Only fetch logo if missing - don't replace existing poster to avoid visual flash
		if cached.Logo == nil && tmdbIDForImages > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			if images, err := s.tmdb.fetchImages(ctx, "movie", tmdbIDForImages); err == nil && images != nil {
				if images.Logo != nil {
					cached.Logo = images.Logo
					log.Printf("[metadata] logo added to cached movie tmdbId=%d", tmdbIDForImages)
					_ = s.cache.set(cacheID, cached)
				}
			}
		}

		// If cached data doesn't have genres, they'll be fetched on next fresh fetch
		// (Movies get genres from the movieDetails call which has them inline)

		return &cached, nil
	}

	log.Printf("[metadata] movie details fetch tvdbId=%d", tvdbID)

	// Fetch movie details from TVDB
	base, err := s.getTVDBMovieDetails(tvdbID)
	if err != nil {
		log.Printf("[metadata] movie details tvdb fetch error tvdbId=%d err=%v", tvdbID, err)

		// If TVDB fails for this movie but we have a TMDB identifier configured,
		// fall back to TMDB so continue watching cards still get imagery.
		if req.TMDBID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
			log.Printf("[metadata] using TMDB fallback for movie tvdbId=%d tmdbId=%d", tvdbID, req.TMDBID)
			return s.getMovieDetailsFromTMDB(ctx, req)
		}

		return nil, fmt.Errorf("failed to fetch movie details: %w", err)
	}

	// Fetch translations
	translatedName := base.Name
	translatedOverview := base.Overview

	if translation, err := s.client.movieTranslations(tvdbID, s.client.language); err == nil && translation != nil {
		if strings.TrimSpace(translation.Name) != "" {
			translatedName = translation.Name
			log.Printf("[metadata] using translated movie name tvdbId=%d lang=%s name=%q", tvdbID, s.client.language, translation.Name)
		}
		if strings.TrimSpace(translation.Overview) != "" {
			translatedOverview = translation.Overview
		}
	} else if err != nil {
		log.Printf("[metadata] failed to fetch movie translations tvdbId=%d lang=%s err=%v", tvdbID, s.client.language, err)
	}

	finalName := strings.TrimSpace(firstNonEmpty(translatedName, base.Name, req.Name))
	finalOverview := strings.TrimSpace(firstNonEmpty(translatedOverview, base.Overview))

	movieTitle := models.Title{
		ID:        fmt.Sprintf("tvdb:movie:%d", tvdbID),
		Name:      finalName,
		Overview:  finalOverview,
		Year:      int(base.Year),
		Language:  s.client.language,
		MediaType: "movie",
		TVDBID:    tvdbID,
	}

	log.Printf("[metadata] movie title constructed tvdbId=%d finalName=%q translatedName=%q baseName=%q", tvdbID, finalName, translatedName, base.Name)

	var extended *tvdbMovieExtendedData
	if ext, err := s.client.movieExtended(tvdbID, []string{"artwork"}); err == nil {
		extended = &ext
		applyTVDBArtworks(&movieTitle, ext.Artworks)
		if movieTitle.Backdrop == nil {
			log.Printf("[metadata] no movie backdrop from TVDB artworks tvdbId=%d name=%q", tvdbID, finalName)
		}
		if movieTitle.Poster == nil {
			log.Printf("[metadata] no movie poster from TVDB artworks tvdbId=%d name=%q", tvdbID, finalName)
		}
	} else {
		log.Printf("[metadata] movie artworks fetch failed from TVDB tvdbId=%d err=%v, will try TMDB", tvdbID, err)
	}

	// Get extended data for remote IDs (reuse earlier fetch when possible)
	if extended == nil {
		if ext, err := s.client.movieExtended(tvdbID, []string{}); err == nil {
			extended = &ext
		} else {
			log.Printf("[metadata] movie extended fetch failed tvdbId=%d err=%v", tvdbID, err)
		}
	}
	if extended != nil {
		// Extract external IDs from remote IDs
		for _, remote := range extended.RemoteIDs {
			id := strings.TrimSpace(remote.ID)
			if id == "" {
				continue
			}
			lower := strings.ToLower(remote.SourceName)
			switch {
			case strings.Contains(lower, "imdb"):
				movieTitle.IMDBID = id
				log.Printf("[metadata] movie imdb id found tvdbId=%d imdbId=%s", tvdbID, id)
			case strings.Contains(lower, "themoviedb") || strings.Contains(lower, "tmdb"):
				if tmdbID, err := strconv.ParseInt(id, 10, 64); err == nil {
					movieTitle.TMDBID = tmdbID
				}
			}
		}
	}

	// Override with request IDs if provided (more reliable)
	if req.IMDBID != "" {
		movieTitle.IMDBID = req.IMDBID
	}
	if req.TMDBID > 0 {
		movieTitle.TMDBID = req.TMDBID
	}

	// If TVDB didn't provide images or runtime, try TMDB as fallback now that we have remote IDs.
	if movieTitle.Poster == nil || movieTitle.Backdrop == nil || movieTitle.RuntimeMinutes == 0 {
		_ = s.maybeHydrateMovieArtworkFromTMDB(ctx, &movieTitle, req)
	}

	// Fetch releases, ratings, credits, and images concurrently — each writes to
	// independent fields on movieTitle so no mutex is needed.
	tmdbIDForEnrichment := movieTitle.TMDBID
	if tmdbIDForEnrichment == 0 {
		tmdbIDForEnrichment = req.TMDBID
	}
	imdbIDForRatings := movieTitle.IMDBID
	if imdbIDForRatings == "" {
		imdbIDForRatings = req.IMDBID
	}

	var enrichWg sync.WaitGroup

	// 1. Release dates (TMDB)
	enrichWg.Add(1)
	go func() {
		defer enrichWg.Done()
		if s.enrichMovieReleases(ctx, &movieTitle, tmdbIDForEnrichment) && len(movieTitle.Releases) > 0 {
			log.Printf("[metadata] movie release windows set tvdbId=%d tmdbId=%d releases=%d", tvdbID, tmdbIDForEnrichment, len(movieTitle.Releases))
		}
	}()

	// 2. MDBList ratings
	if includeRatings && imdbIDForRatings != "" && s.mdblist != nil && s.mdblist.IsEnabled() {
		enrichWg.Add(1)
		go func() {
			defer enrichWg.Done()
			if ratings, err := s.mdblist.GetRatings(ctx, imdbIDForRatings, "movie"); err != nil {
				log.Printf("[metadata] error fetching ratings for movie imdbId=%s: %v", imdbIDForRatings, err)
			} else if len(ratings) > 0 {
				movieTitle.Ratings = ratings
				log.Printf("[metadata] fetched %d ratings for movie imdbId=%s", len(ratings), imdbIDForRatings)
			}
		}()
	}

	// 3. Cast credits (TMDB)
	if tmdbIDForEnrichment > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		enrichWg.Add(1)
		go func() {
			defer enrichWg.Done()
			if credits, err := s.tmdb.fetchCredits(ctx, "movie", tmdbIDForEnrichment); err == nil && credits != nil && len(credits.Cast) > 0 {
				movieTitle.Credits = credits
				log.Printf("[metadata] fetched %d cast members for movie tmdbId=%d", len(credits.Cast), tmdbIDForEnrichment)
			} else if err != nil {
				log.Printf("[metadata] failed to fetch credits for movie tmdbId=%d: %v", tmdbIDForEnrichment, err)
			}
		}()
	}

	// 4. Logo and textless poster (TMDB)
	if tmdbIDForEnrichment > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		enrichWg.Add(1)
		go func() {
			defer enrichWg.Done()
			if images, err := s.tmdb.fetchImages(ctx, "movie", tmdbIDForEnrichment); err == nil && images != nil {
				if images.Logo != nil {
					movieTitle.Logo = images.Logo
					log.Printf("[metadata] fetched logo for movie tmdbId=%d", tmdbIDForEnrichment)
				}
				if images.TextlessPoster != nil {
					movieTitle.Poster = images.TextlessPoster
					log.Printf("[metadata] textless poster applied to movie tmdbId=%d", tmdbIDForEnrichment)
				}
			} else if err != nil {
				log.Printf("[metadata] failed to fetch images for movie tmdbId=%d: %v", tmdbIDForEnrichment, err)
			}
		}()
	}

	enrichWg.Wait()

	// Cache the result
	_ = s.cache.set(cacheID, movieTitle)

	log.Printf("[metadata] movie details complete tvdbId=%d name=%q", tvdbID, finalName)

	return &movieTitle, nil
}

func (s *Service) maybeHydrateMovieArtworkFromTMDB(ctx context.Context, title *models.Title, req models.MovieDetailsQuery) bool {
	if title == nil || s.tmdb == nil || !s.tmdb.isConfigured() {
		return false
	}

	tmdbID := req.TMDBID
	if tmdbID <= 0 {
		tmdbID = title.TMDBID
	}
	if tmdbID <= 0 {
		return false
	}

	log.Printf("[metadata] fetching movie images from TMDB as fallback tmdbId=%d", tmdbID)

	tmdbMovie, err := s.tmdb.movieDetails(ctx, tmdbID)
	if err != nil || tmdbMovie == nil {
		log.Printf("[metadata] TMDB fallback failed for movie tmdbId=%d err=%v", tmdbID, err)
		return false
	}

	updated := false
	if title.Poster == nil && tmdbMovie.Poster != nil {
		title.Poster = tmdbMovie.Poster
		log.Printf("[metadata] using TMDB poster for movie tmdbId=%d", tmdbID)
		updated = true
	}
	if title.Backdrop == nil && tmdbMovie.Backdrop != nil {
		title.Backdrop = tmdbMovie.Backdrop
		log.Printf("[metadata] using TMDB backdrop for movie tmdbId=%d", tmdbID)
		updated = true
	}
	if title.IMDBID == "" && tmdbMovie.IMDBID != "" {
		title.IMDBID = tmdbMovie.IMDBID
		log.Printf("[metadata] using TMDB IMDB ID for movie tmdbId=%d imdbId=%s", tmdbID, tmdbMovie.IMDBID)
		updated = true
	}
	if title.Name == "" && tmdbMovie.Name != "" {
		title.Name = tmdbMovie.Name
		updated = true
	}
	if title.Year == 0 && tmdbMovie.Year > 0 {
		title.Year = tmdbMovie.Year
		updated = true
	}
	if title.RuntimeMinutes == 0 && tmdbMovie.RuntimeMinutes > 0 {
		title.RuntimeMinutes = tmdbMovie.RuntimeMinutes
		log.Printf("[metadata] using TMDB runtime for movie tmdbId=%d runtime=%d", tmdbID, tmdbMovie.RuntimeMinutes)
		updated = true
	}
	if title.Collection == nil && tmdbMovie.Collection != nil {
		title.Collection = tmdbMovie.Collection
		log.Printf("[metadata] using TMDB collection for movie tmdbId=%d collection=%q collectionId=%d", tmdbID, tmdbMovie.Collection.Name, tmdbMovie.Collection.ID)
		updated = true
	}
	if len(title.Genres) == 0 && len(tmdbMovie.Genres) > 0 {
		title.Genres = tmdbMovie.Genres
		log.Printf("[metadata] using TMDB genres for movie tmdbId=%d genres=%v", tmdbID, tmdbMovie.Genres)
		updated = true
	}

	return updated
}

// cachedReleasesWithCert is the cached structure for movie releases including certification
type cachedReleasesWithCert struct {
	Releases      []models.Release `json:"releases"`
	Certification string           `json:"certification"`
}

func (s *Service) enrichMovieReleases(ctx context.Context, title *models.Title, tmdbID int64) bool {
	if title == nil || tmdbID <= 0 || s.tmdb == nil || !s.tmdb.isConfigured() {
		return false
	}

	// Use v2 cache key to include certification
	cacheID := cacheKey("tmdb", "movie", "releases", "v2", strconv.FormatInt(tmdbID, 10))
	var cached cachedReleasesWithCert
	if ok, _ := s.cache.get(cacheID, &cached); ok && len(cached.Releases) > 0 {
		title.Releases = append([]models.Release(nil), cached.Releases...)
		title.Certification = cached.Certification
		s.ensureMovieReleasePointers(title)
		return true
	}

	result, err := s.tmdb.movieReleaseDatesWithCert(ctx, tmdbID)
	if err != nil || len(result.Releases) == 0 {
		if err != nil {
			log.Printf("[metadata] WARN: tmdb release dates fetch failed tmdbId=%d err=%v", tmdbID, err)
		}
		return false
	}

	title.Releases = append([]models.Release(nil), result.Releases...)
	title.Certification = result.Certification
	s.ensureMovieReleasePointers(title)
	_ = s.cache.set(cacheID, cachedReleasesWithCert{
		Releases:      title.Releases,
		Certification: title.Certification,
	})

	return true
}

// enrichTVContentRating fetches and sets the TV content rating for a series
func (s *Service) enrichTVContentRating(ctx context.Context, title *models.Title, tmdbID int64) bool {
	if title == nil || tmdbID <= 0 || s.tmdb == nil || !s.tmdb.isConfigured() {
		return false
	}
	if title.Certification != "" {
		return false // Already has a rating
	}

	cacheID := cacheKey("tmdb", "tv", "content_rating", "v1", strconv.FormatInt(tmdbID, 10))
	var cached string
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		title.Certification = cached
		return cached != ""
	}

	rating, err := s.tmdb.fetchTVContentRating(ctx, tmdbID)
	if err != nil {
		log.Printf("[metadata] WARN: tmdb tv content rating fetch failed tmdbId=%d err=%v", tmdbID, err)
		return false
	}

	title.Certification = rating
	_ = s.cache.set(cacheID, rating)

	return rating != ""
}

func (s *Service) ensureMovieReleasePointers(title *models.Title) {
	if title == nil {
		return
	}

	if len(title.Releases) == 0 {
		title.Theatrical = nil
		title.HomeRelease = nil
		return
	}

	var (
		bestTheatricalIdx = -1
		bestTheatricalTS  time.Time
		bestTheatricalPri = math.MaxInt32

		bestHomeIdx = -1
		bestHomeTS  time.Time
		bestHomePri = math.MaxInt32
	)

	for i := range title.Releases {
		release := &title.Releases[i]
		release.Primary = false
		releaseType := strings.ToLower(strings.TrimSpace(release.Type))
		ts, ok := parseReleaseTime(release.Date)
		if !ok {
			continue
		}

		switch releaseType {
		case "theatrical", "theatricallimited", "premiere":
			priority := theatricalReleasePriority(releaseType)
			if priority < bestTheatricalPri || (priority == bestTheatricalPri && (bestTheatricalIdx == -1 || ts.Before(bestTheatricalTS))) {
				bestTheatricalIdx = i
				bestTheatricalTS = ts
				bestTheatricalPri = priority
			}
		case "digital", "physical", "tv":
			priority := homeReleasePriority(releaseType)
			if priority < bestHomePri || (priority == bestHomePri && (bestHomeIdx == -1 || ts.Before(bestHomeTS))) {
				bestHomeIdx = i
				bestHomeTS = ts
				bestHomePri = priority
			}
		}
	}

	title.Theatrical = nil
	title.HomeRelease = nil

	if bestTheatricalIdx >= 0 {
		title.Releases[bestTheatricalIdx].Primary = true
		title.Theatrical = &title.Releases[bestTheatricalIdx]
	}
	if bestHomeIdx >= 0 {
		title.Releases[bestHomeIdx].Primary = true
		title.HomeRelease = &title.Releases[bestHomeIdx]
	}
}

func parseReleaseTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return ts, true
	}
	if len(trimmed) >= 10 {
		if ts, err := time.Parse("2006-01-02", trimmed[:10]); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func theatricalReleasePriority(t string) int {
	switch t {
	case "theatrical":
		return 0
	case "theatricallimited":
		return 1
	case "premiere":
		return 2
	default:
		return 3
	}
}

func homeReleasePriority(t string) int {
	switch t {
	case "digital":
		return 0
	case "physical":
		return 1
	case "tv":
		return 2
	default:
		return 3
	}
}

func (s *Service) Trailers(ctx context.Context, req models.TrailerQuery) (*models.TrailerResponse, error) {
	mediaType := normalizeMediaTypeForTrailers(req.MediaType)
	tmdbID := req.TMDBID
	if tmdbID <= 0 {
		tmdbID = parseTMDBIDFromTitleID(req.TitleID)
	}
	tvdbID := req.TVDBID
	if tvdbID <= 0 {
		tvdbID = parseTVDBIDFromTitleID(req.TitleID)
	}

	log.Printf("[metadata] trailers request mediaType=%s tmdbId=%d tvdbId=%d imdbId=%s titleId=%q name=%q year=%d season=%d",
		mediaType, tmdbID, tvdbID, strings.TrimSpace(req.IMDBID), strings.TrimSpace(req.TitleID), strings.TrimSpace(req.Name), req.Year, req.SeasonNumber)

	var combined []models.Trailer

	// For TV series with a specific season requested, try season-specific trailers first
	if mediaType != "movie" && req.SeasonNumber > 0 && tmdbID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		if seasonTrailers, err := s.fetchTMDBSeasonTrailers(ctx, tmdbID, req.SeasonNumber); err != nil {
			log.Printf("[metadata] WARN: tmdb season trailers fetch failed tmdbId=%d season=%d err=%v", tmdbID, req.SeasonNumber, err)
		} else if len(seasonTrailers) > 0 {
			log.Printf("[metadata] found %d season-specific trailers for tmdbId=%d season=%d", len(seasonTrailers), tmdbID, req.SeasonNumber)
			combined = append(combined, seasonTrailers...)
		}
	}

	// Fetch show-level trailers (always, as fallback or to supplement season trailers)
	if tmdbID > 0 && s.tmdb != nil && s.tmdb.isConfigured() {
		if trailers, err := s.fetchTMDBTrailers(ctx, mediaType, tmdbID); err != nil {
			log.Printf("[metadata] WARN: tmdb trailers fetch failed mediaType=%s tmdbId=%d err=%v", mediaType, tmdbID, err)
		} else {
			combined = append(combined, trailers...)
		}
	}

	if tvdbID > 0 && s.client != nil {
		var (
			tvdbTrailers []models.Trailer
			err          error
		)
		switch mediaType {
		case "movie":
			tvdbTrailers, err = s.fetchTVDBMovieTrailers(tvdbID)
		default:
			tvdbTrailers, err = s.fetchTVDBSeriesTrailers(tvdbID)
		}
		if err != nil {
			log.Printf("[metadata] WARN: tvdb trailers fetch failed mediaType=%s tvdbId=%d err=%v", mediaType, tvdbID, err)
		} else {
			combined = append(combined, tvdbTrailers...)
		}
	}

	trailers := dedupeTrailers(combined)

	// Log trailer details for debugging
	for i, t := range trailers {
		score := scoreTrailerCandidate(&t)
		log.Printf("[metadata] trailer[%d]: name=%q type=%q official=%v season=%d lang=%q res=%d source=%q score=%d",
			i, t.Name, t.Type, t.Official, t.SeasonNumber, t.Language, t.Resolution, t.Source, score)
	}

	// For season requests, prefer season-specific trailers as primary
	var primary *models.Trailer
	if req.SeasonNumber > 0 {
		primary = selectPrimaryTrailerForSeason(trailers, req.SeasonNumber)
	}
	if primary == nil {
		primary = selectPrimaryTrailer(trailers)
	}

	if len(trailers) == 0 {
		trailers = []models.Trailer{}
	}

	resp := &models.TrailerResponse{
		Trailers:       trailers,
		PrimaryTrailer: primary,
	}

	return resp, nil
}

func detectPrimarySeasonType(seasons []tvdbSeason) string {
	for _, season := range seasons {
		if season.Type.Type != "" {
			return strings.ToLower(strings.TrimSpace(season.Type.Type))
		}
		if season.Type.Name != "" {
			return strings.ToLower(strings.TrimSpace(season.Type.Name))
		}
	}
	return ""
}

func (s *Service) fetchTMDBTrailers(ctx context.Context, mediaType string, tmdbID int64) ([]models.Trailer, error) {
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, fmt.Errorf("tmdb client not configured")
	}
	cacheKeyID := cacheKey("tmdb", "trailers", mediaType, strconv.FormatInt(tmdbID, 10), strings.TrimSpace(s.tmdb.language))
	var cached []models.Trailer
	if ok, _ := s.cache.get(cacheKeyID, &cached); ok {
		return cached, nil
	}

	trailers, err := s.tmdb.fetchTrailers(ctx, mediaType, tmdbID)
	if err != nil {
		return nil, err
	}
	if trailers == nil {
		trailers = []models.Trailer{}
	}
	_ = s.cache.set(cacheKeyID, trailers)
	return trailers, nil
}

func (s *Service) fetchTMDBSeasonTrailers(ctx context.Context, tmdbID int64, seasonNumber int) ([]models.Trailer, error) {
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, fmt.Errorf("tmdb client not configured")
	}
	cacheKeyID := cacheKey("tmdb", "trailers", "season", strconv.FormatInt(tmdbID, 10), strconv.Itoa(seasonNumber), strings.TrimSpace(s.tmdb.language))
	var cached []models.Trailer
	if ok, _ := s.cache.get(cacheKeyID, &cached); ok {
		return cached, nil
	}

	trailers, err := s.tmdb.fetchSeasonTrailers(ctx, tmdbID, seasonNumber)
	if err != nil {
		return nil, err
	}
	if trailers == nil {
		trailers = []models.Trailer{}
	}
	_ = s.cache.set(cacheKeyID, trailers)
	return trailers, nil
}

func (s *Service) fetchTVDBSeriesTrailers(tvdbID int64) ([]models.Trailer, error) {
	if s.client == nil {
		return nil, fmt.Errorf("tvdb client not configured")
	}
	cacheKeyID := cacheKey("tvdb", "trailers", "series", strconv.FormatInt(tvdbID, 10))
	var cached []models.Trailer
	if ok, _ := s.cache.get(cacheKeyID, &cached); ok {
		return cached, nil
	}

	extended, err := s.client.seriesExtended(tvdbID, []string{"trailers"})
	if err != nil {
		return nil, err
	}
	trailers := convertTVDBTrailers(extended.Trailers)
	_ = s.cache.set(cacheKeyID, trailers)
	return trailers, nil
}

func (s *Service) fetchTVDBMovieTrailers(tvdbID int64) ([]models.Trailer, error) {
	if s.client == nil {
		return nil, fmt.Errorf("tvdb client not configured")
	}
	cacheKeyID := cacheKey("tvdb", "trailers", "movie", strconv.FormatInt(tvdbID, 10))
	var cached []models.Trailer
	if ok, _ := s.cache.get(cacheKeyID, &cached); ok {
		return cached, nil
	}

	extended, err := s.client.movieExtended(tvdbID, []string{"trailers"})
	if err != nil {
		return nil, err
	}
	trailers := convertTVDBTrailers(extended.Trailers)
	_ = s.cache.set(cacheKeyID, trailers)
	return trailers, nil
}

func convertTVDBTrailers(source []tvdbTrailer) []models.Trailer {
	if len(source) == 0 {
		return []models.Trailer{}
	}
	result := make([]models.Trailer, 0, len(source))
	for _, t := range source {
		urlStr := strings.TrimSpace(t.URL)
		if urlStr == "" {
			continue
		}
		site, key, embedURL, thumb := deriveTrailerMetadata(urlStr)
		trailer := models.Trailer{
			Name:            strings.TrimSpace(t.Name),
			URL:             urlStr,
			Site:            site,
			Key:             key,
			EmbedURL:        embedURL,
			ThumbnailURL:    thumb,
			Language:        strings.TrimSpace(t.Language),
			DurationSeconds: t.Runtime,
			Source:          "tvdb",
		}
		result = append(result, trailer)
	}
	if len(result) == 0 {
		return []models.Trailer{}
	}
	return result
}

func dedupeTrailers(trailers []models.Trailer) []models.Trailer {
	if len(trailers) == 0 {
		return []models.Trailer{}
	}
	seen := make(map[string]struct{}, len(trailers))
	deduped := make([]models.Trailer, 0, len(trailers))
	for _, trailer := range trailers {
		key := strings.ToLower(strings.TrimSpace(trailer.URL))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, trailer)
	}
	if len(deduped) == 0 {
		return []models.Trailer{}
	}
	return deduped
}

func selectPrimaryTrailer(trailers []models.Trailer) *models.Trailer {
	if len(trailers) == 0 {
		return nil
	}
	bestIndex := -1
	bestScore := -1
	for idx := range trailers {
		score := scoreTrailerCandidate(&trailers[idx])
		if score > bestScore {
			bestScore = score
			bestIndex = idx
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &trailers[bestIndex]
}

// selectPrimaryTrailerForSeason selects the best trailer for a specific season.
// It considers trailers with matching SeasonNumber, and for season 1 also considers
// season 0 (show-level) trailers since they typically represent the first season.
func selectPrimaryTrailerForSeason(trailers []models.Trailer, seasonNumber int) *models.Trailer {
	if len(trailers) == 0 || seasonNumber <= 0 {
		return nil
	}
	bestIndex := -1
	bestScore := -1
	for idx := range trailers {
		trailerSeason := trailers[idx].SeasonNumber
		// Consider trailers for this specific season
		// For season 1, also consider season 0 (show-level) trailers
		if trailerSeason != seasonNumber && !(seasonNumber == 1 && trailerSeason == 0) {
			continue
		}
		score := scoreTrailerCandidate(&trailers[idx])
		if score > bestScore {
			bestScore = score
			bestIndex = idx
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &trailers[bestIndex]
}

func scoreTrailerCandidate(t *models.Trailer) int {
	if t == nil {
		return 0
	}
	score := 0
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "trailer":
		score += 100
	case "teaser":
		score += 60
	case "clip":
		score += 40
	default:
		score += 10
	}
	if t.Official {
		score += 25
	}
	lang := strings.ToLower(strings.TrimSpace(t.Language))
	if strings.HasPrefix(lang, "en") {
		score += 15
	}
	if t.Resolution >= 1080 {
		score += 10
	} else if t.Resolution >= 720 {
		score += 6
	}
	if strings.EqualFold(t.Site, "youtube") {
		score += 5
	}
	if strings.EqualFold(t.Source, "tmdb") {
		score += 3
	}

	// Name-based scoring adjustments
	nameLower := strings.ToLower(t.Name)

	// Bonus for "Official Trailer" in name - these are the main trailers
	if strings.Contains(nameLower, "official trailer") {
		score += 20
	}

	// Bonus for "Final Trailer" in name - often the best/most complete trailer
	if strings.Contains(nameLower, "final trailer") {
		score += 18
	}

	// Bonus for "Series Trailer" in name
	if strings.Contains(nameLower, "series trailer") {
		score += 15
	}

	// Penalize promotional/non-trailer content
	promoKeywords := []string{"best reviewed", "pre-order", "recap", "behind the scenes", "making of", "featurette"}
	for _, keyword := range promoKeywords {
		if strings.Contains(nameLower, keyword) {
			score -= 50
			break
		}
	}

	return score
}

func normalizeMediaTypeForTrailers(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie", "movies", "film", "films":
		return "movie"
	default:
		return "tv"
	}
}

func parseTMDBIDFromTitleID(titleID string) int64 {
	trimmed := strings.TrimSpace(titleID)
	if trimmed == "" {
		return 0
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "tmdb:") {
		parts := strings.Split(trimmed, ":")
		last := strings.TrimSpace(parts[len(parts)-1])
		if id, err := strconv.ParseInt(last, 10, 64); err == nil {
			return id
		}
	}
	if id, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return id
	}
	return 0
}

func deriveTrailerMetadata(urlStr string) (site string, key string, embedURL string, thumb string) {
	parsed, err := url.Parse(urlStr)
	if err != nil || parsed == nil {
		return "", "", "", ""
	}
	host := strings.ToLower(parsed.Host)
	switch {
	case strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be"):
		site = "YouTube"
		key = extractYouTubeID(parsed)
		if key != "" {
			embedURL = fmt.Sprintf("https://www.youtube.com/embed/%s", key)
			thumb = fmt.Sprintf("https://img.youtube.com/vi/%s/hqdefault.jpg", key)
		}
	case strings.Contains(host, "vimeo.com"):
		site = "Vimeo"
		key = strings.Trim(strings.TrimPrefix(parsed.Path, "/"), "/")
		if key != "" {
			embedURL = fmt.Sprintf("https://player.vimeo.com/video/%s", key)
		}
	default:
		site = parsed.Host
	}
	return site, key, embedURL, thumb
}

func extractYouTubeID(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch {
	case strings.Contains(host, "youtu.be"):
		return strings.Trim(strings.TrimSpace(u.Path), "/")
	case strings.Contains(host, "youtube.com"):
		if strings.HasPrefix(u.Path, "/watch") {
			return strings.TrimSpace(u.Query().Get("v"))
		}
		path := strings.Trim(u.Path, "/")
		parts := strings.Split(path, "/")
		if len(parts) >= 2 && strings.EqualFold(parts[0], "embed") {
			return parts[1]
		}
		if len(parts) >= 2 && strings.EqualFold(parts[0], "v") {
			return parts[1]
		}
	}
	return ""
}

func extractYearCandidate(value string) int {
	value = strings.TrimSpace(value)
	if len(value) >= 4 {
		for i := 0; i+4 <= len(value); i++ {
			segment := value[i : i+4]
			if y, err := strconv.Atoi(segment); err == nil {
				return y
			}
		}
	}
	if y, err := strconv.Atoi(value); err == nil {
		return y
	}
	return 0
}

// ResolveIMDBID attempts to find an IMDB ID for a title by searching TVDB.
// This is useful as a fallback when Cinemeta doesn't have a match.
// Returns empty string if no IMDB ID can be found.
func (s *Service) ResolveIMDBID(ctx context.Context, title string, mediaType string, year int) string {
	if s == nil || s.client == nil {
		return ""
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}

	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	log.Printf("[metadata] ResolveIMDBID called: title=%q, mediaType=%q, year=%d", title, mediaType, year)

	var results []tvdbSearchResult
	var err error

	// Search based on media type
	if mediaType == "movie" {
		results, err = s.searchTVDBMovie(title, year, "")
	} else {
		// Default to series search (covers "series", "tv", "" and other values)
		results, err = s.searchTVDBSeries(title, year, "")
	}

	if err != nil {
		log.Printf("[metadata] ResolveIMDBID TVDB search failed: %v", err)
		return ""
	}

	if len(results) == 0 {
		log.Printf("[metadata] ResolveIMDBID no TVDB results for %q", title)
		return ""
	}

	// Look for IMDB ID in the first result's RemoteIDs
	for _, result := range results {
		for _, remote := range result.RemoteIDs {
			id := strings.TrimSpace(remote.ID)
			if id == "" {
				continue
			}
			sourceName := strings.ToLower(strings.TrimSpace(remote.SourceName))
			if strings.Contains(sourceName, "imdb") {
				log.Printf("[metadata] ResolveIMDBID found IMDB ID=%s for %q via TVDB result %q", id, title, result.Name)
				return id
			}
		}
	}

	log.Printf("[metadata] ResolveIMDBID no IMDB ID found in %d TVDB results for %q", len(results), title)
	return ""
}

// HistoryChecker provides watch history lookups for pre-filtering custom lists.
type HistoryChecker interface {
	GetWatchHistoryItem(userID, mediaType, itemID string) (*models.WatchHistoryItem, error)
}

// CustomListOptions configures filtering and pagination for GetCustomList.
type CustomListOptions struct {
	Limit          int
	Offset         int
	HideUnreleased bool
	HideWatched    bool
	UserID         string
	HistorySvc     HistoryChecker // nil if hideWatched is false
}

// cachedMovieExtended fetches TVDB movie extended data with file caching.
func (s *Service) cachedMovieExtended(tvdbID int64, meta []string) (tvdbMovieExtendedData, error) {
	metaKey := strings.Join(meta, ",")
	cacheID := cacheKey("tvdb", "movie", "extended", "v1", fmt.Sprintf("%d", tvdbID), metaKey)
	var cached tvdbMovieExtendedData
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		return cached, nil
	}
	result, err := s.client.movieExtended(tvdbID, meta)
	if err != nil {
		return tvdbMovieExtendedData{}, err
	}
	_ = s.cache.set(cacheID, result)
	return result, nil
}

// cachedSeriesExtended fetches TVDB series extended data with file caching.
func (s *Service) cachedSeriesExtended(tvdbID int64, meta []string) (tvdbSeriesExtendedData, error) {
	metaKey := strings.Join(meta, ",")
	cacheID := cacheKey("tvdb", "series", "extended", "v1", fmt.Sprintf("%d", tvdbID), metaKey)
	var cached tvdbSeriesExtendedData
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		return cached, nil
	}
	result, err := s.client.seriesExtended(tvdbID, meta)
	if err != nil {
		return tvdbSeriesExtendedData{}, err
	}
	_ = s.cache.set(cacheID, result)
	return result, nil
}

// cachedMovieTranslations fetches TVDB movie translations with file caching.
func (s *Service) cachedMovieTranslations(tvdbID int64, lang string) (*tvdbSeriesTranslation, error) {
	cacheID := cacheKey("tvdb", "movie", "translations", "v1", fmt.Sprintf("%d", tvdbID), lang)
	var cached tvdbSeriesTranslation
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		return &cached, nil
	}
	result, err := s.client.movieTranslations(tvdbID, lang)
	if err != nil {
		return nil, err
	}
	if result != nil {
		_ = s.cache.set(cacheID, *result)
	}
	return result, nil
}

// cachedSeriesTranslations fetches TVDB series translations with file caching.
func (s *Service) cachedSeriesTranslations(tvdbID int64, lang string) (*tvdbSeriesTranslation, error) {
	cacheID := cacheKey("tvdb", "series", "translations", "v1", fmt.Sprintf("%d", tvdbID), lang)
	var cached tvdbSeriesTranslation
	if ok, _ := s.cache.get(cacheID, &cached); ok {
		return &cached, nil
	}
	result, err := s.client.seriesTranslations(tvdbID, lang)
	if err != nil {
		return nil, err
	}
	if result != nil {
		_ = s.cache.set(cacheID, *result)
	}
	return result, nil
}

// mdblistItemMediaType returns the normalized media type for an mdblistItem.
func mdblistItemMediaType(item mdblistItem) string {
	if item.MediaType == "show" || item.MediaType == "series" || item.MediaType == "tv" {
		return "series"
	}
	return "movie"
}

// buildMDBListItemIDForHistory constructs the watch history item ID from raw MDBList data.
// Mirrors the logic of buildItemIDForHistory in the handlers package.
func buildMDBListItemIDForHistory(item mdblistItem, mediaType string) string {
	if mediaType == "series" && item.TVDBID != nil && *item.TVDBID > 0 {
		return fmt.Sprintf("tvdb:%d", *item.TVDBID)
	}
	if mediaType == "movie" && item.TMDBID != nil && *item.TMDBID > 0 {
		return fmt.Sprintf("tmdb:movie:%d", *item.TMDBID)
	}
	if mediaType == "series" && item.TMDBID != nil && *item.TMDBID > 0 {
		return fmt.Sprintf("tmdb:tv:%d", *item.TMDBID)
	}
	if mediaType == "movie" && item.TVDBID != nil && *item.TVDBID > 0 {
		return fmt.Sprintf("tvdb:movie:%d", *item.TVDBID)
	}
	return ""
}

// filterWatchedMDBListItems removes watched items from raw MDBList data using IDs
// already present (no HTTP calls needed).
func filterWatchedMDBListItems(items []mdblistItem, userID string, historySvc HistoryChecker) []mdblistItem {
	if userID == "" || historySvc == nil {
		return items
	}
	result := make([]mdblistItem, 0, len(items))
	filteredCount := 0
	for _, item := range items {
		mediaType := mdblistItemMediaType(item)
		itemID := buildMDBListItemIDForHistory(item, mediaType)
		if itemID == "" {
			result = append(result, item)
			continue
		}
		watchItem, _ := historySvc.GetWatchHistoryItem(userID, mediaType, itemID)
		if watchItem == nil || !watchItem.Watched {
			result = append(result, item)
		} else {
			filteredCount++
			if filteredCount <= 3 {
				log.Printf("[hideWatched] pre-filtered %s: %s (itemID=%s)", mediaType, item.Title, itemID)
			}
		}
	}
	if filteredCount > 0 {
		log.Printf("[hideWatched] pre-filter result: %d/%d items kept (filtered %d)", len(result), len(items), filteredCount)
	}
	return result
}

// preFilterUnreleased does a lightweight concurrent pass to remove unreleased items
// before full enrichment. For movies it checks TMDB release data; for series it checks
// TVDB status.
func (s *Service) preFilterUnreleased(ctx context.Context, items []mdblistItem) []mdblistItem {
	const maxConcurrent = 10
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	keep := make([]bool, len(items))
	for i := range keep {
		keep[i] = true // default: keep
	}

	for i, item := range items {
		mediaType := mdblistItemMediaType(item)

		wg.Add(1)
		go func(idx int, it mdblistItem, mt string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if mt == "movie" {
				tmdbID := int64(0)
				if it.TMDBID != nil && *it.TMDBID > 0 {
					tmdbID = *it.TMDBID
				} else if it.IMDBID != "" {
					tmdbID = s.getTMDBIDForIMDB(ctx, it.IMDBID)
				}
				if tmdbID <= 0 {
					return // can't determine, keep
				}
				var title models.Title
				title.TMDBID = tmdbID
				if s.enrichMovieReleases(ctx, &title, tmdbID) {
					if title.HomeRelease == nil || !title.HomeRelease.Released {
						keep[idx] = false
					}
				}
			} else {
				// Series: check status via lightweight extended call (no artworks)
				if it.TVDBID != nil && *it.TVDBID > 0 {
					ext, err := s.cachedSeriesExtended(*it.TVDBID, nil)
					if err == nil {
						status := strings.ToLower(ext.Status.Name)
						if status == "upcoming" || status == "in production" {
							keep[idx] = false
						}
					}
				}
			}
		}(i, item, mediaType)
	}
	wg.Wait()

	result := make([]mdblistItem, 0, len(items))
	filteredCount := 0
	for i, item := range items {
		if keep[i] {
			result = append(result, item)
		} else {
			filteredCount++
			if filteredCount <= 3 {
				log.Printf("[hideUnreleased] pre-filtered: %s (type=%s)", item.Title, mdblistItemMediaType(item))
			}
		}
	}
	if filteredCount > 0 {
		log.Printf("[hideUnreleased] pre-filter result: %d/%d items kept (filtered %d)", len(result), len(items), filteredCount)
	}
	return result
}

// enrichCustomListItem enriches a single mdblistItem into a full TrendingItem.
// Within each item, TVDB extended + translations calls are parallelized.
func (s *Service) enrichCustomListItem(ctx context.Context, item mdblistItem) models.TrendingItem {
	mediaType := mdblistItemMediaType(item)

	title := models.Title{
		ID:         fmt.Sprintf("mdblist:%s:%d", mediaType, item.ID),
		Name:       item.Title,
		Year:       item.ReleaseYear,
		Language:   s.client.language,
		MediaType:  mediaType,
		Popularity: float64(100 - item.Rank),
	}

	if item.IMDBID != "" {
		title.IMDBID = item.IMDBID
	}
	if item.TMDBID != nil && *item.TMDBID > 0 {
		title.TMDBID = *item.TMDBID
	}

	var found bool

	// Try TVDB ID from MDBList first
	if item.TVDBID != nil && *item.TVDBID > 0 {
		tvdbID := *item.TVDBID
		if mediaType == "movie" {
			// Parallel: extended (includes name/overview/artwork) + translations
			var ext tvdbMovieExtendedData
			var extErr error
			var trans *tvdbSeriesTranslation

			var innerWg sync.WaitGroup
			innerWg.Add(2)
			go func() {
				defer innerWg.Done()
				ext, extErr = s.cachedMovieExtended(tvdbID, []string{"artwork"})
			}()
			go func() {
				defer innerWg.Done()
				trans, _ = s.cachedMovieTranslations(tvdbID, s.client.language)
			}()
			innerWg.Wait()

			if extErr == nil {
				title.TVDBID = tvdbID
				title.ID = fmt.Sprintf("tvdb:movie:%d", tvdbID)
				title.Name = ext.Name
				title.Overview = ext.Overview
				found = true
				applyTVDBArtworks(&title, ext.Artworks)

				if trans != nil {
					if trans.Name != "" {
						title.Name = trans.Name
					}
					if trans.Overview != "" {
						title.Overview = trans.Overview
					}
				}
			}
		} else {
			// Parallel: extended (includes name/overview/artwork/status) + translations
			var ext tvdbSeriesExtendedData
			var extErr error
			var trans *tvdbSeriesTranslation

			var innerWg sync.WaitGroup
			innerWg.Add(2)
			go func() {
				defer innerWg.Done()
				ext, extErr = s.cachedSeriesExtended(tvdbID, []string{"artworks"})
			}()
			go func() {
				defer innerWg.Done()
				trans, _ = s.cachedSeriesTranslations(tvdbID, s.client.language)
			}()
			innerWg.Wait()

			if extErr == nil {
				title.TVDBID = tvdbID
				title.ID = fmt.Sprintf("tvdb:series:%d", tvdbID)
				title.Overview = ext.Overview
				title.Status = ext.Status.Name
				found = true
				applyTVDBArtworks(&title, ext.Artworks)

				if trans != nil {
					if trans.Name != "" {
						title.Name = trans.Name
					}
					if trans.Overview != "" {
						title.Overview = trans.Overview
					}
				}
			}
		}
	}

	// Fallback: search TVDB by title/year if no TVDB ID or direct lookup failed
	if !found {
		remoteID := item.IMDBID
		if mediaType == "movie" {
			searchResults, err := s.searchTVDBMovie(item.Title, item.ReleaseYear, remoteID)
			if err != nil {
				log.Printf("[metadata] custom list movie tvdb search error title=%q year=%d imdbId=%q err=%v", item.Title, item.ReleaseYear, item.IMDBID, err)
			} else if len(searchResults) == 0 {
				log.Printf("[metadata] custom list movie tvdb search returned 0 results title=%q year=%d imdbId=%q", item.Title, item.ReleaseYear, item.IMDBID)
				if item.ReleaseYear > 0 {
					log.Printf("[metadata] custom list movie tvdb search retrying without year title=%q imdbId=%q", item.Title, item.IMDBID)
					searchResults, err = s.searchTVDBMovie(item.Title, 0, remoteID)
					if err != nil {
						log.Printf("[metadata] custom list movie tvdb search (no year) error title=%q imdbId=%q err=%v", item.Title, item.IMDBID, err)
					} else if len(searchResults) > 0 {
						log.Printf("[metadata] custom list movie tvdb search (no year) found %d results title=%q imdbId=%q", len(searchResults), item.Title, item.IMDBID)
					}
				}
			}
			if err == nil && len(searchResults) > 0 {
				result := searchResults[0]
				if result.TVDBID == "" {
					log.Printf("[metadata] custom list movie tvdb search result has no tvdb_id title=%q year=%d imdbId=%q firstResultName=%q", item.Title, item.ReleaseYear, item.IMDBID, result.Name)
				} else if tvdbID, err := strconv.ParseInt(result.TVDBID, 10, 64); err != nil {
					log.Printf("[metadata] custom list movie tvdb search result has invalid tvdb_id title=%q year=%d tvdbId=%q err=%v", item.Title, item.ReleaseYear, result.TVDBID, err)
				} else {
					title.TVDBID = tvdbID
					title.ID = fmt.Sprintf("tvdb:movie:%d", tvdbID)
					if img := newTVDBImage(result.ImageURL, "poster", 0, 0); img != nil {
						title.Poster = img
					}
					if ext, err := s.cachedMovieExtended(tvdbID, []string{"artwork"}); err == nil {
						applyTVDBArtworks(&title, ext.Artworks)
					}
					if result.Overview != "" {
						title.Overview = result.Overview
					}
					found = true
				}
			}
		} else {
			searchResults, err := s.searchTVDBSeries(item.Title, item.ReleaseYear, remoteID)
			if err != nil {
				log.Printf("[metadata] custom list series tvdb search error title=%q year=%d imdbId=%q err=%v", item.Title, item.ReleaseYear, item.IMDBID, err)
			} else if len(searchResults) == 0 {
				log.Printf("[metadata] custom list series tvdb search returned 0 results title=%q year=%d imdbId=%q", item.Title, item.ReleaseYear, item.IMDBID)
				if item.ReleaseYear > 0 {
					log.Printf("[metadata] custom list series tvdb search retrying without year title=%q imdbId=%q", item.Title, item.IMDBID)
					searchResults, err = s.searchTVDBSeries(item.Title, 0, remoteID)
					if err != nil {
						log.Printf("[metadata] custom list series tvdb search (no year) error title=%q imdbId=%q err=%v", item.Title, item.IMDBID, err)
					} else if len(searchResults) > 0 {
						log.Printf("[metadata] custom list series tvdb search (no year) found %d results title=%q imdbId=%q", len(searchResults), item.Title, item.IMDBID)
					}
				}
			}
			if err == nil && len(searchResults) > 0 {
				result := searchResults[0]
				if result.TVDBID == "" {
					log.Printf("[metadata] custom list series tvdb search result has no tvdb_id title=%q year=%d imdbId=%q firstResultName=%q", item.Title, item.ReleaseYear, item.IMDBID, result.Name)
				} else if tvdbID, err := strconv.ParseInt(result.TVDBID, 10, 64); err != nil {
					log.Printf("[metadata] custom list series tvdb search result has invalid tvdb_id title=%q year=%d tvdbId=%q err=%v", item.Title, item.ReleaseYear, result.TVDBID, err)
				} else {
					title.TVDBID = tvdbID
					title.ID = fmt.Sprintf("tvdb:series:%d", tvdbID)
					if img := newTVDBImage(result.ImageURL, "poster", 0, 0); img != nil {
						title.Poster = img
					}
					if ext, err := s.cachedSeriesExtended(tvdbID, []string{"artworks"}); err == nil {
						applyTVDBArtworks(&title, ext.Artworks)
						if title.Status == "" {
							title.Status = ext.Status.Name
						}
					}
					if result.Overview != "" {
						title.Overview = result.Overview
					}
					found = true
				}
			}
		}
	}

	if !found {
		log.Printf("[metadata] no tvdb match for custom list item title=%q year=%d type=%s imdbId=%q", item.Title, item.ReleaseYear, mediaType, item.IMDBID)
	}

	// Enrich movies with release data from TMDB
	if mediaType == "movie" {
		tmdbID := title.TMDBID
		if tmdbID <= 0 && title.IMDBID != "" {
			if resolved := s.getTMDBIDForIMDB(ctx, title.IMDBID); resolved > 0 {
				tmdbID = resolved
				title.TMDBID = resolved
			}
		}
		if tmdbID > 0 {
			s.enrichMovieReleases(ctx, &title, tmdbID)
		}
	}

	// Series status already set from cachedSeriesExtended above

	return models.TrendingItem{
		Rank:  item.Rank,
		Title: title,
	}
}

// GetCustomList fetches items from a custom MDBList URL and returns them as TrendingItems.
// Pre-filters watched/unreleased items before enrichment so only displayed items incur full
// TVDB lookups. Returns (items, filteredTotal, unfilteredTotal, error).
func (s *Service) GetCustomList(ctx context.Context, listURL string, opts CustomListOptions) ([]models.TrendingItem, int, int, error) {
	// Check full-list cache first (only populated when no filtering was applied)
	cacheID := cacheKey("mdblist", "custom", "v4", listURL)
	var cached []models.TrendingItem
	if ok, _ := s.cache.get(cacheID, &cached); ok && len(cached) > 0 {
		log.Printf("[metadata] custom list cache hit for %s (%d items)", listURL, len(cached))
		total := len(cached)
		// Apply offset + limit to cached results
		result := cached
		if opts.Offset > 0 {
			if opts.Offset >= len(result) {
				return []models.TrendingItem{}, total, total, nil
			}
			result = result[opts.Offset:]
		}
		if opts.Limit > 0 && opts.Limit < len(result) {
			result = result[:opts.Limit]
		}
		return result, total, total, nil
	}

	// Fetch raw items from MDBList API
	rawItems, err := s.client.FetchMDBListCustom(listURL)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to fetch custom MDBList: %w", err)
	}

	unfilteredTotal := len(rawItems)
	log.Printf("[metadata] fetched %d items from custom MDBList: %s", unfilteredTotal, listURL)

	remaining := rawItems

	// Pre-filter watched items (instant, uses IDs already present)
	if opts.HideWatched && opts.UserID != "" && opts.HistorySvc != nil {
		remaining = filterWatchedMDBListItems(remaining, opts.UserID, opts.HistorySvc)
	}

	// Pre-filter unreleased items (lightweight concurrent check)
	if opts.HideUnreleased {
		remaining = s.preFilterUnreleased(ctx, remaining)
	}

	filteredTotal := len(remaining)

	// Apply offset + limit to get only the items that need full enrichment
	itemsToEnrich := remaining
	if opts.Offset > 0 {
		if opts.Offset >= len(itemsToEnrich) {
			return []models.TrendingItem{}, filteredTotal, unfilteredTotal, nil
		}
		itemsToEnrich = itemsToEnrich[opts.Offset:]
	}
	if opts.Limit > 0 && opts.Limit < len(itemsToEnrich) {
		itemsToEnrich = itemsToEnrich[:opts.Limit]
	}

	log.Printf("[metadata] enriching %d items (filtered=%d, unfiltered=%d, offset=%d, limit=%d)",
		len(itemsToEnrich), filteredTotal, unfilteredTotal, opts.Offset, opts.Limit)

	// Concurrent enrichment with worker pool
	const maxConcurrentEnrich = 10
	sem := make(chan struct{}, maxConcurrentEnrich)
	var wg sync.WaitGroup
	results := make([]models.TrendingItem, len(itemsToEnrich))

	for i, item := range itemsToEnrich {
		wg.Add(1)
		go func(idx int, it mdblistItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = s.enrichCustomListItem(ctx, it)
		}(i, item)
	}
	wg.Wait()

	// Only cache full-list results when no filtering was applied
	if !opts.HideWatched && !opts.HideUnreleased && opts.Offset == 0 &&
		(opts.Limit == 0 || opts.Limit >= unfilteredTotal) && len(results) > 0 {
		_ = s.cache.set(cacheID, results)
		log.Printf("[metadata] cached %d enriched items for custom list: %s", len(results), listURL)
	}

	return results, filteredTotal, unfilteredTotal, nil
}

// ExtractTrailerStreamURL uses yt-dlp to extract a direct stream URL from a YouTube video.
// The extracted URL is an MP4 that can be played directly by video players.
func (s *Service) ExtractTrailerStreamURL(ctx context.Context, videoURL string) (string, error) {
	// Check cache first (URLs are temporary but cache uses standard TTL)
	// v2: Use format 18 (combined H.264+AAC MP4) instead of HLS
	cacheID := cacheKey("trailer-stream-v2", videoURL)
	var cached string
	if ok, _ := s.cache.get(cacheID, &cached); ok && cached != "" {
		log.Printf("[metadata] trailer stream cache hit for %s", videoURL)
		return cached, nil
	}

	// Try to find yt-dlp binary
	ytdlpPath := "/usr/local/bin/yt-dlp"
	if _, err := exec.LookPath(ytdlpPath); err != nil {
		// Fall back to PATH lookup
		ytdlpPath = "yt-dlp"
		if _, err := exec.LookPath(ytdlpPath); err != nil {
			return "", fmt.Errorf("yt-dlp not found in system")
		}
	}

	// Build yt-dlp command to extract stream URL
	// -g: Get URL only (don't download)
	// --format: Prefer format 18 (360p combined H.264+AAC MP4) for best iOS compatibility
	// Format 18 is a self-contained MP4 that doesn't need merging and works natively on iOS
	args := []string{
		"-g",
		"--format", "18/22/best[ext=mp4][height<=720]/best[height<=720]/best",
		"--no-warnings",
		"--no-playlist",
		videoURL,
	}

	cmd := exec.CommandContext(ctx, ytdlpPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[metadata] extracting trailer stream URL: %s %v", ytdlpPath, args)

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		log.Printf("[metadata] yt-dlp failed: %v, stderr: %s", err, stderrStr)
		return "", fmt.Errorf("failed to extract stream URL: %s", stderrStr)
	}

	streamURL := strings.TrimSpace(stdout.String())
	if streamURL == "" {
		return "", fmt.Errorf("no stream URL extracted")
	}

	// If multiple URLs returned (video + audio), take the first one
	lines := strings.Split(streamURL, "\n")
	streamURL = strings.TrimSpace(lines[0])

	log.Printf("[metadata] extracted trailer stream URL for %s", videoURL)

	// Cache the result
	_ = s.cache.set(cacheID, streamURL)

	return streamURL, nil
}

// StreamTrailer proxies a YouTube video to the provided writer (without range support).
func (s *Service) StreamTrailer(ctx context.Context, videoURL string, w io.Writer) error {
	return s.StreamTrailerWithRange(ctx, videoURL, "", w)
}

// StreamTrailerWithRange proxies a YouTube video to the provided writer with range request support.
// It first extracts the direct stream URL (using cached value if available),
// then proxies the MP4 content directly to iOS (format 18 is already iOS-compatible).
func (s *Service) StreamTrailerWithRange(ctx context.Context, videoURL string, rangeHeader string, w io.Writer) error {
	// First, extract the direct stream URL (this uses cache if available)
	// Use a separate context with timeout for URL extraction
	extractCtx, extractCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer extractCancel()

	streamURL, err := s.ExtractTrailerStreamURL(extractCtx, videoURL)
	if err != nil {
		return fmt.Errorf("failed to get stream URL: %v", err)
	}

	log.Printf("[metadata] proxying trailer from extracted URL: %s (range: %s)", videoURL, rangeHeader)

	// Create HTTP request to fetch the stream
	req, err := http.NewRequestWithContext(ctx, "GET", streamURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	// Set headers that YouTube expects
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")

	// Forward Range header if present
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	// Use a client with longer timeout
	client := &http.Client{
		Timeout: 5 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch stream: %v", err)
	}
	defer resp.Body.Close()

	// Check for valid response (200 OK or 206 Partial Content)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("stream returned status %d", resp.StatusCode)
	}

	// Set response headers
	if rw, ok := w.(http.ResponseWriter); ok {
		rw.Header().Set("Content-Type", "video/mp4")
		rw.Header().Set("Accept-Ranges", "bytes")

		// Forward content length
		if resp.ContentLength > 0 {
			rw.Header().Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
		}

		// Forward Content-Range for partial responses
		if contentRange := resp.Header.Get("Content-Range"); contentRange != "" {
			rw.Header().Set("Content-Range", contentRange)
		}

		// Set the status code (206 for partial content, 200 otherwise)
		if resp.StatusCode == http.StatusPartialContent {
			rw.WriteHeader(http.StatusPartialContent)
		}
	}

	// Stream the content directly to the client
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		// Don't log broken pipe errors (client disconnected)
		if !strings.Contains(err.Error(), "broken pipe") {
			log.Printf("[metadata] stream copy error: %v", err)
		}
		return err
	}

	return nil
}

// PrequeueTrailer starts downloading a YouTube trailer in the background
// Returns the prequeue ID that can be used to check status and serve the file
func (s *Service) PrequeueTrailer(videoURL string) (string, error) {
	if s.trailerPrequeue == nil {
		return "", fmt.Errorf("trailer prequeue manager not initialized")
	}
	id := s.trailerPrequeue.Prequeue(videoURL)
	return id, nil
}

// GetTrailerPrequeueStatus returns the status of a prequeued trailer
func (s *Service) GetTrailerPrequeueStatus(id string) (*TrailerPrequeueItem, error) {
	if s.trailerPrequeue == nil {
		return nil, fmt.Errorf("trailer prequeue manager not initialized")
	}
	item, ok := s.trailerPrequeue.GetStatus(id)
	if !ok {
		return nil, fmt.Errorf("trailer not found: %s", id)
	}
	return item, nil
}

// ServePrequeuedTrailer serves a downloaded trailer file with proper range request support
func (s *Service) ServePrequeuedTrailer(id string, w http.ResponseWriter, r *http.Request) error {
	if s.trailerPrequeue == nil {
		return fmt.Errorf("trailer prequeue manager not initialized")
	}
	return s.trailerPrequeue.ServeTrailer(id, w, r)
}

package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/internal/apiusage"
	"novastream/internal/urlredact"
	"novastream/models"

	"golang.org/x/sync/singleflight"
)

// mdblistClient handles requests to the MDBList API for aggregated ratings
type mdblistClient struct {
	apiKey         string
	enabledRatings map[string]bool
	httpClient     *http.Client
	listHTTPClient *http.Client
	enabled        bool

	// Cache for ratings to avoid repeated API calls
	cacheMu  sync.RWMutex
	cache    map[string]*mdblistCacheEntry
	cacheTTL time.Duration

	// Rate limiting
	limiter     priorityLimiter
	minInterval time.Duration

	// Concurrent lookups for the same title (e.g. a details shell and bundle)
	// share one API call instead of queueing a second one behind the limiter.
	inflight singleflight.Group

	// quota is the key's remaining API budget as reported by response headers.
	quota mdblistQuota
}

type mdblistCacheEntry struct {
	ratings   []models.Rating
	fetchedAt time.Time
}

// Rating source metadata
var ratingSourceInfo = map[string]struct {
	label string
	max   float64
}{
	"imdb":       {"IMDB", 10},
	"tmdb":       {"TMDB", 10},
	"trakt":      {"Trakt", 10},
	"letterboxd": {"Letterboxd", 5},
	"tomatoes":   {"Rotten Tomatoes", 100},
	"audience":   {"RT Audience", 100},
	"popcorn":    {"RT Audience", 100}, // API returns "popcorn" for audience scores
	"metacritic": {"Metacritic", 100},
}

// Map API source names to our internal names (for filtering by enabled ratings)
var apiSourceToInternal = map[string]string{
	"popcorn": "audience", // API returns "popcorn", we call it "audience"
}

// mdblistMediaResponse is the response from the /imdb/{type}/{id} endpoint
type mdblistMediaResponse struct {
	Ratings []mdblistRatingEntry `json:"ratings"`
}

func newMDBListClient(apiKey string, enabledRatings []string, enabled bool, cacheTTLHours int) *mdblistClient {
	enabledMap := make(map[string]bool)
	for _, r := range enabledRatings {
		enabledMap[r] = true
	}

	if cacheTTLHours <= 0 {
		cacheTTLHours = 24
	}

	return &mdblistClient{
		apiKey:         apiKey,
		enabledRatings: enabledMap,
		httpClient:     apiusage.TrackClient(&http.Client{Timeout: 10 * time.Second}, "MDBList", "Ratings lookup"),
		listHTTPClient: apiusage.TrackClient(&http.Client{Timeout: 20 * time.Second}, "MDBList", "List API"),
		enabled:        enabled,
		cache:          make(map[string]*mdblistCacheEntry),
		cacheTTL:       time.Duration(cacheTTLHours) * time.Hour,
		minInterval:    1100 * time.Millisecond, // MDBList free tier: ~1 req/sec
	}
}

// UpdateSettings updates the client configuration
func (c *mdblistClient) UpdateSettings(apiKey string, enabledRatings []string, enabled bool) {
	enabledMap := make(map[string]bool)
	for _, r := range enabledRatings {
		enabledMap[r] = true
	}

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()

	// Clear cache if settings changed
	if c.apiKey != apiKey || c.enabled != enabled {
		c.cache = make(map[string]*mdblistCacheEntry)
	}

	c.apiKey = apiKey
	c.enabledRatings = enabledMap
	c.enabled = enabled
}

// GetRatings fetches all ratings for a title from MDBList in a single API call
// mediaType should be "movie" or "show"
func (c *mdblistClient) GetRatings(ctx context.Context, imdbID string, mediaType string) ([]models.Rating, error) {
	if !c.enabled || c.apiKey == "" || imdbID == "" {
		return nil, nil
	}

	// Normalize IMDB ID
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s", mediaType, imdbID)
	c.cacheMu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Since(entry.fetchedAt) < c.cacheTTL {
		c.cacheMu.RUnlock()
		return entry.ratings, nil
	}
	c.cacheMu.RUnlock()

	// Check if any ratings are enabled
	hasEnabled := false
	for _, enabled := range c.enabledRatings {
		if enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		return nil, nil
	}

	// Fetch all ratings in a single API call using /imdb/{type}/{id} endpoint
	url := fmt.Sprintf("https://api.mdblist.com/imdb/%s/%s?apikey=%s", mediaType, imdbID, c.apiKey)

	var result mdblistMediaResponse
	var lastErr error
	backoff := 2 * time.Second

	// Retry loop with exponential backoff
	for attempt := 0; attempt < 3; attempt++ {
		// Rate limiting — details-page lookups are granted ahead of queued
		// background enrichment, and the wait honors the caller's deadline.
		if err := c.limiter.Wait(ctx, c.minInterval); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		resp, err := doRedacted(c.httpClient, req)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			log.Printf("[mdblist] http request error (attempt %d/3): %v", attempt+1, err)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		c.quota.record(resp.Header, time.Now())

		// Handle rate limiting and server errors with retry
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			log.Printf("[mdblist] rate limited or server error (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			// Context-aware sleep: bail immediately if context expires during backoff
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			log.Printf("[mdblist] unexpected status %d for %s", resp.StatusCode, urlredact.String(url))
			return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode response: %w", err)
		}
		resp.Body.Close()
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	// Filter ratings based on enabled settings and convert to our format
	var ratings []models.Rating
	for _, r := range result.Ratings {
		// Skip if no rating value
		if r.Value == nil || *r.Value == 0 {
			continue
		}

		// Map API source name to internal name if needed
		internalSource := r.Source
		if mapped, ok := apiSourceToInternal[r.Source]; ok {
			internalSource = mapped
		}

		// Check if this rating source is enabled (using internal name)
		if !c.enabledRatings[internalSource] {
			continue
		}

		// Get max value for this source
		sourceInfo, ok := ratingSourceInfo[r.Source]
		if !ok {
			sourceInfo = struct {
				label string
				max   float64
			}{r.Source, 10}
		}

		// Use internal source name for consistency
		ratings = append(ratings, models.Rating{
			Source: internalSource,
			Value:  *r.Value,
			Max:    sourceInfo.max,
		})
	}

	// Cache the results
	c.cacheMu.Lock()
	c.cache[cacheKey] = &mdblistCacheEntry{
		ratings:   ratings,
		fetchedAt: time.Now(),
	}
	c.cacheMu.Unlock()

	log.Printf("[mdblist] fetched %d ratings for %s %s", len(ratings), mediaType, imdbID)

	return ratings, nil
}

// GetAllRatings fetches all ratings for a title from MDBList without filtering by enabled ratings.
// This is used for sort-by-rating where all sources should be available regardless of display settings.
func (c *mdblistClient) GetAllRatings(ctx context.Context, imdbID string, mediaType string) ([]models.Rating, error) {
	if !c.enabled || c.apiKey == "" || imdbID == "" {
		return nil, nil
	}

	// Normalize IMDB ID
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	// Check cache first (shared with GetRatings)
	cacheKey := fmt.Sprintf("all:%s:%s", mediaType, imdbID)
	c.cacheMu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Since(entry.fetchedAt) < c.cacheTTL {
		c.cacheMu.RUnlock()
		return entry.ratings, nil
	}
	c.cacheMu.RUnlock()

	results := c.inflight.DoChan(cacheKey, func() (any, error) {
		return c.fetchAllRatings(ctx, cacheKey, imdbID, mediaType)
	})
	select {
	case result := <-results:
		if result.Err != nil {
			return nil, result.Err
		}
		ratings, _ := result.Val.([]models.Rating)
		return ratings, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetchAllRatings performs the MDBList API call behind GetAllRatings and
// caches the result under cacheKey.
func (c *mdblistClient) fetchAllRatings(ctx context.Context, cacheKey, imdbID, mediaType string) ([]models.Rating, error) {
	// Fetch all ratings in a single API call
	url := fmt.Sprintf("https://api.mdblist.com/imdb/%s/%s?apikey=%s", mediaType, imdbID, c.apiKey)

	var result mdblistMediaResponse
	var lastErr error
	backoff := 2 * time.Second

	for attempt := 0; attempt < 3; attempt++ {
		// Rate limiting — details-page lookups are granted ahead of queued
		// background enrichment, and the wait honors the caller's deadline.
		if err := c.limiter.Wait(ctx, c.minInterval); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		resp, err := doRedacted(c.httpClient, req)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			log.Printf("[mdblist] http request error (attempt %d/3): %v", attempt+1, err)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		c.quota.record(resp.Header, time.Now())

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode response: %w", err)
		}
		resp.Body.Close()
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	// Return ALL ratings without filtering by enabled settings
	ratings := convertMDBListRatings(result.Ratings)

	// Cache the results
	c.cacheMu.Lock()
	c.cache[cacheKey] = &mdblistCacheEntry{
		ratings:   ratings,
		fetchedAt: time.Now(),
	}
	c.cacheMu.Unlock()

	return ratings, nil
}

// GetAllRatingsCached returns cached ratings for a title without making any API calls.
// Returns nil if not cached. Used for non-blocking enrichment in request handlers.
func (c *mdblistClient) GetAllRatingsCached(imdbID string, mediaType string) []models.Rating {
	if !c.enabled || c.apiKey == "" || imdbID == "" {
		return nil
	}
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}
	cacheKey := fmt.Sprintf("all:%s:%s", mediaType, imdbID)
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	if entry, ok := c.cache[cacheKey]; ok && time.Since(entry.fetchedAt) < c.cacheTTL {
		return entry.ratings
	}
	return nil
}

// FilterEnabledRatings filters an all-sources rating set down to the sources
// configured for display. Cached ratings are stored unfiltered so sort-by-rating
// can use every source, while details UI should respect enabledRatings.
func (c *mdblistClient) FilterEnabledRatings(allRatings []models.Rating) []models.Rating {
	if !c.enabled || c.apiKey == "" || len(allRatings) == 0 {
		return nil
	}

	c.cacheMu.RLock()
	enabledRatings := make(map[string]bool, len(c.enabledRatings))
	for source, enabled := range c.enabledRatings {
		enabledRatings[source] = enabled
	}
	c.cacheMu.RUnlock()

	hasEnabled := false
	for _, enabled := range enabledRatings {
		if enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		return nil
	}

	filtered := make([]models.Rating, 0, len(allRatings))
	for _, rating := range allRatings {
		internalSource := rating.Source
		if mapped, ok := apiSourceToInternal[rating.Source]; ok {
			internalSource = mapped
		}
		if !enabledRatings[internalSource] {
			continue
		}

		rating.Source = internalSource
		filtered = append(filtered, rating)
	}

	return filtered
}

// IsEnabled returns whether the MDBList client is enabled and configured
func (c *mdblistClient) IsEnabled() bool {
	return c.enabled && c.apiKey != ""
}

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

	"novastream/models"
)

// mdblistClient handles requests to the MDBList API for aggregated ratings
type mdblistClient struct {
	apiKey         string
	enabledRatings map[string]bool
	httpClient     *http.Client
	enabled        bool

	// Cache for ratings to avoid repeated API calls
	cacheMu  sync.RWMutex
	cache    map[string]*mdblistCacheEntry
	cacheTTL time.Duration
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
	Ratings []struct {
		Source string   `json:"source"`
		Value  *float64 `json:"value"`  // Pointer to handle null
		Score  *float64 `json:"score"`  // Pointer to handle null, can be int or float
		Votes  *int     `json:"votes"`  // Pointer to handle null
	} `json:"ratings"`
}

func newMDBListClient(apiKey string, enabledRatings []string, enabled bool) *mdblistClient {
	enabledMap := make(map[string]bool)
	for _, r := range enabledRatings {
		enabledMap[r] = true
	}

	return &mdblistClient{
		apiKey:         apiKey,
		enabledRatings: enabledMap,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		enabled:        enabled,
		cache:          make(map[string]*mdblistCacheEntry),
		cacheTTL:       24 * time.Hour, // Cache ratings for 24 hours to match metadata cache
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

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[mdblist] http request error: %v", err)
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[mdblist] unexpected status %d for %s", resp.StatusCode, url)
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result mdblistMediaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
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

// IsEnabled returns whether the MDBList client is enabled and configured
func (c *mdblistClient) IsEnabled() bool {
	return c.enabled && c.apiKey != ""
}

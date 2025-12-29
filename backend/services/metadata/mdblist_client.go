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
	cacheMu    sync.RWMutex
	cache      map[string]*mdblistCacheEntry
	cacheTTL   time.Duration
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
	"metacritic": {"Metacritic", 100},
}

type mdblistRatingResponse struct {
	Ratings []struct {
		ID     string  `json:"id"`
		Rating float64 `json:"rating"`
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
		cacheTTL:       1 * time.Hour, // Cache ratings for 1 hour
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

// GetRatings fetches ratings for a title from MDBList
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

	// Determine which rating types to fetch based on enabled settings
	ratingTypes := []string{}
	for rt := range c.enabledRatings {
		if c.enabledRatings[rt] {
			ratingTypes = append(ratingTypes, rt)
		}
	}

	if len(ratingTypes) == 0 {
		return nil, nil
	}

	// Fetch ratings in parallel
	var wg sync.WaitGroup
	resultCh := make(chan models.Rating, len(ratingTypes))
	errCh := make(chan error, len(ratingTypes))

	for _, ratingType := range ratingTypes {
		wg.Add(1)
		go func(rt string) {
			defer wg.Done()
			rating, err := c.fetchRating(ctx, imdbID, mediaType, rt)
			if err != nil {
				errCh <- err
				return
			}
			if rating != nil {
				resultCh <- *rating
			}
		}(ratingType)
	}

	wg.Wait()
	close(resultCh)
	close(errCh)

	// Collect results
	var ratings []models.Rating
	for rating := range resultCh {
		ratings = append(ratings, rating)
	}

	// Log any errors (but don't fail the whole request)
	for err := range errCh {
		log.Printf("[mdblist] error fetching rating: %v", err)
	}

	// Cache the results
	c.cacheMu.Lock()
	c.cache[cacheKey] = &mdblistCacheEntry{
		ratings:   ratings,
		fetchedAt: time.Now(),
	}
	c.cacheMu.Unlock()

	return ratings, nil
}

func (c *mdblistClient) fetchRating(ctx context.Context, imdbID, mediaType, ratingType string) (*models.Rating, error) {
	url := fmt.Sprintf("https://api.mdblist.com/rating/%s/%s?apikey=%s", mediaType, ratingType, c.apiKey)

	body := fmt.Sprintf(`{"ids":["%s"],"provider":"imdb"}`, imdbID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result mdblistRatingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Ratings) == 0 || result.Ratings[0].Rating == 0 {
		return nil, nil
	}

	sourceInfo, ok := ratingSourceInfo[ratingType]
	if !ok {
		sourceInfo = struct {
			label string
			max   float64
		}{ratingType, 10}
	}

	return &models.Rating{
		Source: ratingType,
		Value:  result.Ratings[0].Rating,
		Max:    sourceInfo.max,
	}, nil
}

// IsEnabled returns whether the MDBList client is enabled and configured
func (c *mdblistClient) IsEnabled() bool {
	return c.enabled && c.apiKey != ""
}

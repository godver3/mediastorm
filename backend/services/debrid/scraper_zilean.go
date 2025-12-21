package debrid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/models"
)

const (
	zileanTimeout = 10 * time.Second
)

// ZileanScraper queries Zilean's DMM filtered API for torrent releases.
type ZileanScraper struct {
	baseURL    string
	httpClient *http.Client
}

// NewZileanScraper constructs a Zilean scraper with the given URL.
func NewZileanScraper(baseURL string, client *http.Client) *ZileanScraper {
	if client == nil {
		client = &http.Client{Timeout: zileanTimeout}
	}
	// Normalize URL - remove trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	return &ZileanScraper{
		baseURL:    baseURL,
		httpClient: client,
	}
}

func (z *ZileanScraper) Name() string {
	return "Zilean"
}

// stringOrArray is a custom type that can unmarshal both string and []string from JSON
type stringOrArray []string

// UnmarshalJSON implements custom unmarshaling for stringOrArray
func (sa *stringOrArray) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as array first
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*sa = arr
		return nil
	}

	// Try to unmarshal as string
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		if str != "" {
			*sa = []string{str}
		} else {
			*sa = []string{}
		}
		return nil
	}

	// If both fail, return empty array
	*sa = []string{}
	return nil
}

// flexibleInt64 is a custom type that can unmarshal both string and int64 from JSON
type flexibleInt64 int64

// UnmarshalJSON implements custom unmarshaling for flexibleInt64
func (fi *flexibleInt64) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as int64 first
	var i int64
	if err := json.Unmarshal(data, &i); err == nil {
		*fi = flexibleInt64(i)
		return nil
	}

	// Try to unmarshal as string and parse
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		if str == "" {
			*fi = 0
			return nil
		}
		parsed, err := strconv.ParseInt(str, 10, 64)
		if err == nil {
			*fi = flexibleInt64(parsed)
			return nil
		}
	}

	// If both fail, return 0
	*fi = 0
	return nil
}

// zileanItem represents a single result from Zilean's API
type zileanItem struct {
	RawTitle   string        `json:"raw_title"`
	Size       flexibleInt64 `json:"size"`
	InfoHash   string        `json:"info_hash"`
	Resolution string        `json:"resolution"`
	Quality    string        `json:"quality"`
	Codec      *string       `json:"codec"`       // Can be null or string
	Audio      []string      `json:"audio"`       // Always array
	Channels   []string      `json:"channels"`    // Always array
	HDR        []string      `json:"hdr"`         // Always array
	Languages  []string      `json:"languages"`   // Always array
	Year       int           `json:"year"`
	Season     int           `json:"season"`
	Episode    int           `json:"episode"`
	IMDBID     string        `json:"imdb_id"`
	Category   string        `json:"category"`
	Container  string        `json:"container"`
}

func (z *ZileanScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	cleanTitle := strings.TrimSpace(req.Parsed.Title)
	if cleanTitle == "" {
		return nil, nil
	}

	log.Printf("[zilean] Search called with Query=%q, ParsedTitle=%q, Season=%d, Episode=%d, Year=%d, MediaType=%s",
		req.Query, cleanTitle, req.Parsed.Season, req.Parsed.Episode, req.Parsed.Year, req.Parsed.MediaType)

	var results []ScrapeResult
	var err error

	if req.Parsed.MediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 {
		// TV show search: title + season + episode
		results, err = z.searchTV(ctx, cleanTitle, req.Parsed.Season, req.Parsed.Episode, false)
	} else if req.Parsed.MediaType == MediaTypeMovie || req.Parsed.Year > 0 {
		// Movie search: title + year
		results, err = z.searchMovie(ctx, cleanTitle, req.Parsed.Year)
	} else {
		// Generic search - just title
		log.Printf("[zilean] MediaType unknown, performing generic search for %q", cleanTitle)
		results, err = z.searchGeneric(ctx, cleanTitle)
	}

	if err != nil {
		return nil, err
	}

	// Limit results
	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	log.Printf("[zilean] Returning %d results for %q", len(results), cleanTitle)
	return results, nil
}

// searchMovie performs a movie search with title and year.
func (z *ZileanScraper) searchMovie(ctx context.Context, title string, year int) ([]ScrapeResult, error) {
	params := url.Values{}
	params.Set("Query", title)
	if year > 0 {
		params.Set("Year", strconv.Itoa(year))
	}

	log.Printf("[zilean] Movie search: Query=%q, Year=%d", title, year)
	return z.fetchResults(ctx, params)
}

// searchTV performs a TV search with title, season, and episode.
func (z *ZileanScraper) searchTV(ctx context.Context, title string, season, episode int, multi bool) ([]ScrapeResult, error) {
	params := url.Values{}
	params.Set("Query", title)
	
	if season > 0 {
		params.Set("Season", strconv.Itoa(season))
	}
	
	if episode > 0 && !multi {
		params.Set("Episode", strconv.Itoa(episode))
	}

	log.Printf("[zilean] TV search: Query=%q, Season=%d, Episode=%d, Multi=%v", title, season, episode, multi)
	return z.fetchResults(ctx, params)
}

// searchGeneric performs a basic text search.
func (z *ZileanScraper) searchGeneric(ctx context.Context, query string) ([]ScrapeResult, error) {
	params := url.Values{}
	params.Set("Query", query)

	log.Printf("[zilean] Generic search: Query=%q", query)
	return z.fetchResults(ctx, params)
}

// fetchResults makes the API request and parses the JSON response.
func (z *ZileanScraper) fetchResults(ctx context.Context, params url.Values) ([]ScrapeResult, error) {
	apiURL := fmt.Sprintf("%s/dmm/filtered?%s", z.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := z.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zilean request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("zilean returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return z.parseResponse(body)
}

// parseResponse parses the Zilean JSON response into ScrapeResults.
func (z *ZileanScraper) parseResponse(body []byte) ([]ScrapeResult, error) {
	var items []zileanItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	var results []ScrapeResult
	seen := make(map[string]struct{})
	filteredNoHash := 0

	for _, item := range items {
		infoHash := strings.ToLower(strings.TrimSpace(item.InfoHash))
		
		// Skip items without info hash
		if infoHash == "" {
			filteredNoHash++
			log.Printf("[zilean] Skipping result without info_hash: %s", item.RawTitle)
			continue
		}

		// Deduplicate by infohash
		if _, exists := seen[infoHash]; exists {
			continue
		}
		seen[infoHash] = struct{}{}

		// Build magnet link
		magnet := buildMagnetFromHash(infoHash, item.RawTitle)

		// Convert size to bytes (Zilean returns bytes)
		sizeBytes := int64(item.Size)

		// Normalize resolution
		resolution := normalizeResolution(item.Resolution)

		// Build attributes map with all available metadata
		attrs := map[string]string{
			"scraper":    "zilean",
			"raw_title":  item.RawTitle,
			"label":      item.RawTitle,
		}

		if item.Quality != "" {
			attrs["quality"] = item.Quality
		}
		if item.Codec != nil && *item.Codec != "" {
			attrs["codec"] = *item.Codec
		}
		if len(item.Audio) > 0 {
			attrs["audio"] = strings.Join(item.Audio, ",")
		}
		if len(item.Channels) > 0 {
			attrs["channels"] = strings.Join(item.Channels, ",")
		}
		if item.Container != "" {
			attrs["container"] = item.Container
		}
		if item.Category != "" {
			attrs["category"] = item.Category
		}
		if item.IMDBID != "" {
			attrs["imdb_id"] = item.IMDBID
		}
		if len(item.HDR) > 0 {
			attrs["hdr"] = strings.Join(item.HDR, ",")
		}
		if item.Year > 0 {
			attrs["year"] = strconv.Itoa(item.Year)
		}
		if item.Season > 0 {
			attrs["season"] = strconv.Itoa(item.Season)
		}
		if item.Episode > 0 {
			attrs["episode"] = strconv.Itoa(item.Episode)
		}
		if resolution != "" {
			attrs["resolution"] = resolution
		}
		if len(item.Languages) > 0 {
			attrs["languages"] = strings.Join(item.Languages, ",")
		}

		result := ScrapeResult{
			Title:       item.RawTitle,
			Indexer:     "Zilean",
			Magnet:      magnet,
			InfoHash:    infoHash,
			FileIndex:   -1, // Zilean doesn't provide file index
			SizeBytes:   sizeBytes,
			Seeders:     0, // Zilean doesn't provide seeder info
			Provider:    "Zilean",
			Languages:   item.Languages,
			Resolution:  resolution,
			MetaName:    item.RawTitle,
			MetaID:      item.IMDBID,
			Source:      "zilean",
			ServiceType: models.ServiceTypeDebrid,
			Attributes:  attrs,
		}

		results = append(results, result)
	}

	if filteredNoHash > 0 {
		log.Printf("[zilean] Filtering summary: total=%d, parsed=%d, no_hash=%d",
			len(items), len(results), filteredNoHash)
	}

	// Sort results by size (highest to lowest)
	sort.Slice(results, func(i, j int) bool {
		return results[i].SizeBytes > results[j].SizeBytes
	})

	return results, nil
}

// normalizeResolution converts various resolution formats to standard format
func normalizeResolution(res string) string {
	if res == "" {
		return ""
	}

	res = strings.ToLower(strings.TrimSpace(res))

	switch {
	case strings.Contains(res, "2160") || strings.Contains(res, "4k") || strings.Contains(res, "uhd"):
		return "2160p"
	case strings.Contains(res, "1080"):
		return "1080p"
	case strings.Contains(res, "720"):
		return "720p"
	case strings.Contains(res, "480") || strings.Contains(res, "sd"):
		return "480p"
	default:
		// Return as-is if it looks like a resolution format
		if regexp.MustCompile(`^\d{3,4}p?$`).MatchString(res) {
			if !strings.HasSuffix(res, "p") {
				return res + "p"
			}
			return res
		}
		return res
	}
}

// TestConnection tests the Zilean connection by making a simple query.
func (z *ZileanScraper) TestConnection(ctx context.Context) error {
	// Try a simple search to test connection
	params := url.Values{}
	params.Set("Query", "test")

	apiURL := fmt.Sprintf("%s/dmm/filtered?%s", z.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := z.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("zilean returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

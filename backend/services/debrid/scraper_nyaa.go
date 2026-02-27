package debrid

import (
	"context"
	"encoding/xml"
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
	nyaaDefaultBaseURL = "https://nyaa.si"
	nyaaTimeout        = 15 * time.Second
)

// NyaaScraper queries Nyaa's RSS feed for anime torrent releases.
type NyaaScraper struct {
	name       string // User-configured name for display
	baseURL    string
	category   string // Category format: "1_2" (anime english-translated)
	filter     string // 0=all, 1=no remakes, 2=trusted only
	httpClient *http.Client
}

// NewNyaaScraper constructs a Nyaa scraper with the given configuration.
// The name parameter is the user-configured display name (empty falls back to "Nyaa").
// category should be in format "1_2" (category_subcategory), defaults to "1_2" (Anime - English-translated).
// filter should be "0" (all), "1" (no remakes), or "2" (trusted only), defaults to "0".
func NewNyaaScraper(baseURL, name, category, filter string, client *http.Client) *NyaaScraper {
	if client == nil {
		client = &http.Client{Timeout: nyaaTimeout}
	}
	// Normalize URL - remove trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = nyaaDefaultBaseURL
	}
	if category == "" {
		category = "1_2" // Default: Anime - English-translated
	}
	if filter == "" {
		filter = "0" // Default: No filter
	}
	return &NyaaScraper{
		name:       strings.TrimSpace(name),
		baseURL:    baseURL,
		category:   category,
		filter:     filter,
		httpClient: client,
	}
}

func (n *NyaaScraper) Name() string {
	if n.name != "" {
		return n.name
	}
	return "Nyaa"
}

// nyaaRSSFeed represents the RSS feed structure from Nyaa
type nyaaRSSFeed struct {
	XMLName xml.Name       `xml:"rss"`
	Channel nyaaRSSChannel `xml:"channel"`
}

type nyaaRSSChannel struct {
	Title string        `xml:"title"`
	Items []nyaaRSSItem `xml:"item"`
}

type nyaaRSSItem struct {
	Title     string `xml:"title"`
	Link      string `xml:"link"`
	GUID      string `xml:"guid"`
	PubDate   string `xml:"pubDate"`
	Seeders   int    `xml:"seeders"`
	Leechers  int    `xml:"leechers"`
	Downloads int    `xml:"downloads"`
	InfoHash  string `xml:"infoHash"`
	Size      string `xml:"size"`
	Category  string `xml:"category"`
}

func (n *NyaaScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	cleanTitle := strings.TrimSpace(req.Parsed.Title)
	if cleanTitle == "" {
		return nil, nil
	}

	log.Printf("[nyaa] Search called with Query=%q, ParsedTitle=%q, Season=%d, Episode=%d, Year=%d, MediaType=%s",
		req.Query, cleanTitle, req.Parsed.Season, req.Parsed.Episode, req.Parsed.Year, req.Parsed.MediaType)

	var query string

	if req.Parsed.MediaType == MediaTypeMovie {
		// Movie search: title + year
		if req.Parsed.Year > 0 {
			query = fmt.Sprintf("%s %d", cleanTitle, req.Parsed.Year)
		} else {
			query = cleanTitle
		}
	} else if req.Parsed.MediaType == MediaTypeSeries {
		// TV show search
		query = cleanTitle
		if req.Parsed.Episode > 0 {
			// Add episode number with leading zero for more specific matches
			query = fmt.Sprintf("%s %02d", cleanTitle, req.Parsed.Episode)
		}
	} else {
		// Generic search - just title
		query = cleanTitle
	}

	results, err := n.searchRSS(ctx, query)
	if err != nil {
		return nil, err
	}

	// If no results and we searched with year, try without year
	if len(results) == 0 && req.Parsed.Year > 0 && req.Parsed.MediaType == MediaTypeMovie {
		log.Printf("[nyaa] No results with year, retrying without year for %q", cleanTitle)
		results, err = n.searchRSS(ctx, cleanTitle)
		if err != nil {
			return nil, err
		}
	}

	// Limit results
	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	log.Printf("[nyaa] Returning %d results for %q", len(results), cleanTitle)
	return results, nil
}

// searchRSS performs a search via Nyaa's RSS feed.
func (n *NyaaScraper) searchRSS(ctx context.Context, query string) ([]ScrapeResult, error) {
	// Build RSS URL: /?page=rss&f={filter}&c={category}&q={query}&s=seeders&o=desc
	params := url.Values{}
	params.Set("page", "rss")
	params.Set("f", n.filter)
	params.Set("c", n.category)
	params.Set("q", query)
	params.Set("s", "seeders") // Sort by seeders
	params.Set("o", "desc")    // Descending order

	apiURL := fmt.Sprintf("%s/?%s", n.baseURL, params.Encode())

	log.Printf("[nyaa] RSS request: %s", apiURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nyaa request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("nyaa returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return n.parseRSSResponse(body)
}

// parseRSSResponse parses the Nyaa RSS XML response into ScrapeResults.
func (n *NyaaScraper) parseRSSResponse(body []byte) ([]ScrapeResult, error) {
	var feed nyaaRSSFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse RSS XML: %w", err)
	}

	var results []ScrapeResult
	seen := make(map[string]struct{})

	for _, item := range feed.Channel.Items {
		infoHash := strings.ToLower(strings.TrimSpace(item.InfoHash))

		// Skip items without info hash
		if infoHash == "" {
			log.Printf("[nyaa] Skipping result without info_hash: %s", item.Title)
			continue
		}

		// Deduplicate by infohash
		if _, exists := seen[infoHash]; exists {
			continue
		}
		seen[infoHash] = struct{}{}

		// Build magnet link
		magnet := buildMagnetFromHash(infoHash, item.Title)

		// Convert size to bytes
		sizeBytes := nyaaConvertSizeToBytes(item.Size)

		// Extract resolution from title
		resolution := extractResolution(item.Title)

		// Build attributes map
		attrs := map[string]string{
			"scraper":   "nyaa",
			"raw_title": item.Title,
			"label":     item.Title,
		}

		if item.Category != "" {
			attrs["category"] = item.Category
		}
		if resolution != "" {
			attrs["resolution"] = resolution
		}
		if item.Seeders > 0 {
			attrs["seeders"] = strconv.Itoa(item.Seeders)
		}
		if item.Leechers > 0 {
			attrs["leechers"] = strconv.Itoa(item.Leechers)
		}
		if item.Downloads > 0 {
			attrs["downloads"] = strconv.Itoa(item.Downloads)
		}

		result := ScrapeResult{
			Title:       item.Title,
			Indexer:     n.Name(),
			Magnet:      magnet,
			InfoHash:    infoHash,
			FileIndex:   -1, // Nyaa doesn't provide file index
			SizeBytes:   sizeBytes,
			Seeders:     item.Seeders,
			Provider:    n.Name(),
			Resolution:  resolution,
			MetaName:    item.Title,
			Source:      n.Name(),
			ServiceType: models.ServiceTypeDebrid,
			Attributes:  attrs,
		}

		results = append(results, result)
	}

	// Sort results by seeders (highest to lowest)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Seeders > results[j].Seeders
	})

	log.Printf("[nyaa] Parsed %d results from RSS feed", len(results))
	return results, nil
}

// nyaaConvertSizeToBytes converts various size formats to bytes.
// Supports: GiB, MiB, KiB, TiB, GB, MB, KB, TB
func nyaaConvertSizeToBytes(sizeStr string) int64 {
	if sizeStr == "" {
		return 0
	}

	sizeStr = strings.TrimSpace(sizeStr)
	sizeLower := strings.ToLower(sizeStr)

	// Regular expression to extract number and unit
	re := regexp.MustCompile(`^([\d.]+)\s*([a-z]+)$`)
	matches := re.FindStringSubmatch(sizeLower)

	if len(matches) != 3 {
		// Try parsing as plain number (bytes)
		if val, err := strconv.ParseFloat(sizeLower, 64); err == nil {
			return int64(val)
		}
		return 0
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0
	}

	unit := matches[2]

	// Binary units (1024-based)
	switch unit {
	case "tib":
		return int64(value * 1024 * 1024 * 1024 * 1024)
	case "gib":
		return int64(value * 1024 * 1024 * 1024)
	case "mib":
		return int64(value * 1024 * 1024)
	case "kib":
		return int64(value * 1024)
	// Decimal units (1000-based, but we'll use 1024 for consistency)
	case "tb":
		return int64(value * 1024 * 1024 * 1024 * 1024)
	case "gb":
		return int64(value * 1024 * 1024 * 1024)
	case "mb":
		return int64(value * 1024 * 1024)
	case "kb":
		return int64(value * 1024)
	case "b", "bytes":
		return int64(value)
	default:
		return 0
	}
}

// TestConnection tests the Nyaa connection by making a simple query.
func (n *NyaaScraper) TestConnection(ctx context.Context) error {
	// Try a simple search to test connection
	params := url.Values{}
	params.Set("page", "rss")
	params.Set("f", "0")
	params.Set("c", "1_0")
	params.Set("q", "test")

	apiURL := fmt.Sprintf("%s/?%s", n.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mediastorm/1.0)")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("nyaa returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

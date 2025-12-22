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
	"strconv"
	"strings"
	"time"

	"novastream/models"
)

// JackettScraper queries Jackett's Torznab API for torrent releases.
type JackettScraper struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewJackettScraper constructs a Jackett scraper with the given URL and API key.
func NewJackettScraper(baseURL, apiKey string, client *http.Client) *JackettScraper {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Normalize URL - remove trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	return &JackettScraper{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: client,
	}
}

func (j *JackettScraper) Name() string {
	return "Jackett"
}

// torznabRSS represents the Torznab RSS response structure.
type torznabRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Channel torznabChannel `xml:"channel"`
}

type torznabChannel struct {
	Items []torznabItem `xml:"item"`
}

type torznabItem struct {
	Title     string          `xml:"title"`
	GUID      string          `xml:"guid"`
	Link      string          `xml:"link"`
	Size      int64           `xml:"size"`
	PubDate   string          `xml:"pubDate"`
	Enclosure torznabEnclosure `xml:"enclosure"`
	Attrs     []torznabAttr   `xml:"attr"`
}

type torznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (j *JackettScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	cleanTitle := strings.TrimSpace(req.Parsed.Title)
	if cleanTitle == "" {
		return nil, nil
	}

	log.Printf("[jackett] Search called with Query=%q, ParsedTitle=%q, Season=%d, Episode=%d, Year=%d, MediaType=%s",
		req.Query, cleanTitle, req.Parsed.Season, req.Parsed.Episode, req.Parsed.Year, req.Parsed.MediaType)

	var results []ScrapeResult
	var err error

	if req.Parsed.MediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 {
		// TV show search: title + SxxExx
		results, err = j.searchTV(ctx, cleanTitle, req.Parsed.Season, req.Parsed.Episode)
	} else if req.Parsed.MediaType == MediaTypeMovie || req.Parsed.Year > 0 {
		// Movie search: title + year
		results, err = j.searchMovie(ctx, cleanTitle, req.Parsed.Year)
	} else {
		// Generic search - try both approaches
		log.Printf("[jackett] MediaType unknown, performing generic search for %q", cleanTitle)
		results, err = j.searchGeneric(ctx, cleanTitle)
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

	log.Printf("[jackett] Returning %d results for %q", len(results), cleanTitle)
	return results, nil
}

// searchMovie performs a movie search using t=movie with title and year.
func (j *JackettScraper) searchMovie(ctx context.Context, title string, year int) ([]ScrapeResult, error) {
	params := url.Values{}
	params.Set("apikey", j.apiKey)
	params.Set("t", "movie")

	// Construct query: "title year" for better matching
	query := title
	if year > 0 {
		query = fmt.Sprintf("%s %d", title, year)
	}
	params.Set("q", query)

	log.Printf("[jackett] Movie search: q=%q", query)
	return j.fetchResults(ctx, params)
}

// searchTV performs a TV search using t=tvsearch with title, season, and episode.
func (j *JackettScraper) searchTV(ctx context.Context, title string, season, episode int) ([]ScrapeResult, error) {
	params := url.Values{}
	params.Set("apikey", j.apiKey)
	params.Set("t", "tvsearch")
	params.Set("q", title)
	params.Set("season", strconv.Itoa(season))
	params.Set("ep", strconv.Itoa(episode))

	log.Printf("[jackett] TV search: q=%q, season=%d, ep=%d", title, season, episode)
	return j.fetchResults(ctx, params)
}

// searchGeneric performs a basic text search.
func (j *JackettScraper) searchGeneric(ctx context.Context, query string) ([]ScrapeResult, error) {
	params := url.Values{}
	params.Set("apikey", j.apiKey)
	params.Set("t", "search")
	params.Set("q", query)

	log.Printf("[jackett] Generic search: q=%q", query)
	return j.fetchResults(ctx, params)
}

// fetchResults makes the API request and parses the Torznab XML response.
func (j *JackettScraper) fetchResults(ctx context.Context, params url.Values) ([]ScrapeResult, error) {
	// Use the "all" indexer to query all configured indexers
	apiURL := fmt.Sprintf("%s/api/v2.0/indexers/all/results/torznab/api?%s", j.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jackett request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jackett returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return j.parseResponse(body)
}

// parseResponse parses the Torznab XML response into ScrapeResults.
func (j *JackettScraper) parseResponse(body []byte) ([]ScrapeResult, error) {
	var rss torznabRSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("parse XML: %w", err)
	}

	var results []ScrapeResult
	seen := make(map[string]struct{})

	for _, item := range rss.Channel.Items {
		// Extract attributes from torznab:attr elements
		attrs := make(map[string]string)
		for _, attr := range item.Attrs {
			attrs[attr.Name] = attr.Value
		}

		// Get infohash - try multiple sources
		infoHash := strings.ToLower(attrs["infohash"])
		if infoHash == "" {
			// Try to extract from magnet link
			infoHash = jackettExtractInfoHash(item.GUID)
			if infoHash == "" {
				infoHash = jackettExtractInfoHash(item.Link)
			}
		}

		// Get download URL - this could be a magnet link or a torrent file URL
		downloadURL := item.Link
		if downloadURL == "" {
			downloadURL = item.GUID
		}
		if downloadURL == "" {
			downloadURL = item.Enclosure.URL
		}

		// Determine if we have a magnet link or a torrent file URL
		var magnet, torrentURL string
		if strings.HasPrefix(downloadURL, "magnet:") {
			magnet = downloadURL
		} else if downloadURL != "" {
			// It's likely a torrent file download URL
			torrentURL = downloadURL
		}

		// Build magnet from infohash if we have it but no magnet
		if magnet == "" && infoHash != "" {
			magnet = buildMagnetFromHash(infoHash, item.Title)
		}

		// Skip results that have neither magnet, infohash, nor torrent URL
		if magnet == "" && infoHash == "" && torrentURL == "" {
			log.Printf("[jackett] Skipping result with no magnet/infohash/torrent URL: %s", item.Title)
			continue
		}

		// Deduplicate - prefer infohash, fall back to torrent URL
		dedupeKey := infoHash
		if dedupeKey == "" {
			dedupeKey = torrentURL
		}
		if dedupeKey != "" {
			if _, exists := seen[dedupeKey]; exists {
				continue
			}
			seen[dedupeKey] = struct{}{}
		}

		// Get seeders
		seeders := 0
		if s, ok := attrs["seeders"]; ok {
			seeders, _ = strconv.Atoi(s)
		}

		// Get size - prefer enclosure length, then size element, then attribute
		size := item.Size
		if size == 0 && item.Enclosure.Length > 0 {
			size = item.Enclosure.Length
		}
		if size == 0 {
			if s, ok := attrs["size"]; ok {
				size, _ = strconv.ParseInt(s, 10, 64)
			}
		}

		// Extract resolution from title
		resolution := extractResolution(item.Title)

		// Get tracker/indexer name
		tracker := attrs["tracker"]
		if tracker == "" {
			tracker = attrs["jackettindexer"]
		}
		if tracker == "" {
			tracker = "jackett"
		}

		result := ScrapeResult{
			Title:       item.Title,
			Indexer:     tracker,
			Magnet:      magnet,
			InfoHash:    infoHash,
			TorrentURL:  torrentURL,
			FileIndex:   -1, // Jackett doesn't provide file index
			SizeBytes:   size,
			Seeders:     seeders,
			Provider:    tracker,
			Languages:   nil, // Jackett doesn't typically provide language info
			Resolution:  resolution,
			Source:      "jackett",
			ServiceType: models.ServiceTypeDebrid,
			Attributes:  attrs,
		}

		results = append(results, result)
	}

	return results, nil
}

// jackettExtractInfoHash extracts the info hash from a magnet link.
func jackettExtractInfoHash(link string) string {
	if !strings.HasPrefix(link, "magnet:") {
		return ""
	}

	// Look for xt=urn:btih:HASH
	re := regexp.MustCompile(`xt=urn:btih:([a-fA-F0-9]{40}|[a-zA-Z2-7]{32})`)
	matches := re.FindStringSubmatch(link)
	if len(matches) >= 2 {
		return strings.ToLower(matches[1])
	}
	return ""
}

// buildMagnetFromHash creates a basic magnet link from an info hash.
func buildMagnetFromHash(hash, title string) string {
	encodedTitle := url.QueryEscape(title)
	return fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", hash, encodedTitle)
}

// extractResolution parses resolution from a release title.
func extractResolution(title string) string {
	title = strings.ToLower(title)

	switch {
	case strings.Contains(title, "2160p") || strings.Contains(title, "4k") || strings.Contains(title, "uhd"):
		return "4K"
	case strings.Contains(title, "1080p") || strings.Contains(title, "1080i"):
		return "1080p"
	case strings.Contains(title, "720p"):
		return "720p"
	case strings.Contains(title, "480p") || strings.Contains(title, "sd"):
		return "480p"
	default:
		return ""
	}
}

// TestConnection tests the Jackett connection by fetching capabilities.
func (j *JackettScraper) TestConnection(ctx context.Context) error {
	params := url.Values{}
	params.Set("apikey", j.apiKey)
	params.Set("t", "caps")

	apiURL := fmt.Sprintf("%s/api/v2.0/indexers/all/results/torznab/api?%s", j.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jackett returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

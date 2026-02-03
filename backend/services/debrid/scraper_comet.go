package debrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"novastream/internal/mediaresolve"
	"novastream/models"
)

const (
	cometDefaultBaseURL = "https://comet.elfhosted.com"
)

// CometScraper queries Comet for releases using the Stremio stream API.
// Comet is a Stremio addon that aggregates torrent sources similar to Torrentio.
type CometScraper struct {
	name       string // User-configured name for display
	baseURL    string
	options    string // URL path options (e.g., indexers configuration)
	httpClient *http.Client
}

// NewCometScraper constructs a scraper with sane defaults.
// The name parameter is the user-configured display name (empty falls back to "comet").
// The baseURL parameter is optional; defaults to cometDefaultBaseURL.
// The options parameter is inserted between the base URL and /stream path.
func NewCometScraper(client *http.Client, baseURL, options, name string) *CometScraper {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if baseURL == "" {
		baseURL = cometDefaultBaseURL
	}
	// Normalize URL
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/manifest.json")
	return &CometScraper{
		name:       strings.TrimSpace(name),
		baseURL:    baseURL,
		options:    strings.TrimSpace(options),
		httpClient: client,
	}
}

func (c *CometScraper) Name() string {
	if c.name != "" {
		return c.name
	}
	return "comet"
}

func (c *CometScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	// Comet requires an IMDB ID - it doesn't support text search
	imdbID := strings.TrimSpace(req.IMDBID)
	if imdbID == "" {
		log.Printf("[comet] No IMDB ID provided, skipping search")
		return nil, nil
	}

	// Ensure IMDB ID has "tt" prefix and is lowercase
	imdbID = strings.ToLower(imdbID)
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	log.Printf("[comet] Search called with IMDBID=%q, Season=%d, Episode=%d, MediaType=%s, IsDaily=%v, TargetAirDate=%q",
		imdbID, req.Parsed.Season, req.Parsed.Episode, req.Parsed.MediaType, req.IsDaily, req.TargetAirDate)

	// Determine media type candidates
	mediaCandidates := determineMediaCandidates(req.Parsed.MediaType)

	var (
		results []ScrapeResult
		errs    []error
		seen    = make(map[string]struct{})
	)

	for _, mediaType := range mediaCandidates {
		stremioType := "movie"
		if mediaType == MediaTypeSeries {
			stremioType = "series"
		}

		// For daily shows, search adjacent episodes (N-1, N, N+1) to handle TMDB/TVDB offset
		episodesToSearch := []int{req.Parsed.Episode}
		isDailySearch := req.IsDaily && mediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 && req.TargetAirDate != ""
		if isDailySearch {
			log.Printf("[comet] Daily show detected, will try E%d, E%d, E%d for target date %s",
				req.Parsed.Episode-1, req.Parsed.Episode, req.Parsed.Episode+1, req.TargetAirDate)
			episodesToSearch = []int{}
			if req.Parsed.Episode > 1 {
				episodesToSearch = append(episodesToSearch, req.Parsed.Episode-1)
			}
			episodesToSearch = append(episodesToSearch, req.Parsed.Episode)
			episodesToSearch = append(episodesToSearch, req.Parsed.Episode+1)
		}

		for _, episode := range episodesToSearch {
			streamID := imdbID
			if mediaType == MediaTypeSeries && req.Parsed.Season > 0 && episode > 0 {
				streamID = fmt.Sprintf("%s:%d:%d", imdbID, req.Parsed.Season, episode)
			}

			streams, err := c.fetchStreams(ctx, stremioType, streamID)
			if err != nil {
				errs = append(errs, fmt.Errorf("comet %s %s: %w", stremioType, streamID, err))
				continue
			}

			var batchResults []ScrapeResult
			foundCorrectDate := false

			for _, stream := range streams {
				// Build unique identifier based on what we have
				var guid string
				if stream.infoHash != "" {
					guid = fmt.Sprintf("%s:%s:%d", c.Name(), strings.ToLower(stream.infoHash), stream.fileIdx)
				} else if stream.url != "" {
					// For URL-based streams, use URL hash as identifier
					guid = fmt.Sprintf("%s:url:%s", c.Name(), stream.url)
				} else {
					continue // No identifier available
				}

				if _, exists := seen[guid]; exists {
					continue
				}
				seen[guid] = struct{}{}

				var result ScrapeResult
				if stream.infoHash != "" {
					// Torrent-based stream
					result = ScrapeResult{
						Title:       stream.titleText,
						Indexer:     c.Name(),
						Magnet:      buildMagnet(stream.infoHash, stream.trackers),
						InfoHash:    stream.infoHash,
						FileIndex:   stream.fileIdx,
						SizeBytes:   stream.sizeBytes,
						Seeders:     stream.seeders,
						Provider:    stream.provider,
						Languages:   stream.languages,
						Resolution:  stream.resolution,
						MetaName:    req.Parsed.Title,
						MetaID:      imdbID,
						Source:      c.Name(),
						Attributes:  stream.attributes(),
						ServiceType: models.ServiceTypeDebrid,
					}
				} else {
					// URL-based pre-resolved debrid stream
					result = ScrapeResult{
						Title:       stream.titleText,
						Indexer:     c.Name(),
						TorrentURL:  stream.url, // Use TorrentURL field for direct stream URL
						InfoHash:    "",         // No infohash for pre-resolved streams
						FileIndex:   0,
						SizeBytes:   stream.sizeBytes,
						Seeders:     0, // Not applicable for pre-resolved streams
						Provider:    stream.provider,
						Languages:   stream.languages,
						Resolution:  stream.resolution,
						MetaName:    req.Parsed.Title,
						MetaID:      imdbID,
						Source:      c.Name(),
						Attributes:  stream.attributes(),
						ServiceType: models.ServiceTypeDebrid,
					}
				}

				// For daily shows, check if this result matches the target date
				if isDailySearch {
					if mediaresolve.CandidateMatchesDailyDate(stream.titleText, req.TargetAirDate, 0) {
						foundCorrectDate = true
						batchResults = append(batchResults, result)
					}
					// Skip results that don't match the target date
				} else {
					batchResults = append(batchResults, result)
				}
			}

			results = append(results, batchResults...)

			// For daily shows: if we found results with the correct date, stop searching
			if isDailySearch && foundCorrectDate {
				log.Printf("[comet] Found %d results matching target date %s at episode %d, stopping search",
					len(batchResults), req.TargetAirDate, episode)
				break
			}

			if req.MaxResults > 0 && len(results) >= req.MaxResults {
				return results[:req.MaxResults], nil
			}
		}
	}

	// Apply max results limit if needed
	if req.MaxResults > 0 && len(results) > req.MaxResults {
		results = results[:req.MaxResults]
	}

	if len(results) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	log.Printf("[comet] Found %d streams for %s", len(results), imdbID)
	return results, nil
}

type cometResponse struct {
	Streams []struct {
		Name          string                 `json:"name"`
		Title         string                 `json:"title"`
		InfoHash      string                 `json:"infoHash"`
		FileIdx       *int                   `json:"fileIdx"`
		URL           string                 `json:"url"`
		Size          interface{}            `json:"size"`
		Seeders       interface{}            `json:"seeders"`
		Tracker       interface{}            `json:"tracker"`
		BehaviorHints map[string]interface{} `json:"behaviorHints"`
	} `json:"streams"`
}

type cometStream struct {
	titleText  string
	infoHash   string
	fileIdx    int
	sizeBytes  int64
	seeders    int
	provider   string
	languages  []string
	resolution string
	trackers   []string
	rawTitle   string
	name       string
	url        string // Direct playback URL (for pre-resolved debrid streams)
}

func (s cometStream) attributes() map[string]string {
	attrs := map[string]string{
		"scraper":   "comet",
		"raw_title": s.rawTitle,
	}
	if s.provider != "" {
		attrs["tracker"] = s.provider
	}
	if s.resolution != "" {
		attrs["resolution"] = s.resolution
	}
	if s.name != "" {
		attrs["label"] = s.name
	}
	if len(s.languages) > 0 {
		attrs["languages"] = strings.Join(s.languages, ",")
	}
	if s.url != "" {
		attrs["stream_url"] = s.url
		// Mark as pre-resolved so health check and playback know not to resolve again
		attrs["preresolved"] = "true"
	}
	return attrs
}

func (c *CometScraper) fetchStreams(ctx context.Context, mediaType, id string) ([]cometStream, error) {
	if id == "" {
		return nil, fmt.Errorf("empty stream id")
	}

	// Build endpoint with optional path options
	// Format: baseURL/[options/]stream/mediaType/id.json
	var endpoint string
	if c.options != "" {
		endpoint = fmt.Sprintf("%s/%s/stream/%s/%s.json", c.baseURL, c.options, mediaType, url.PathEscape(id))
	} else {
		endpoint = fmt.Sprintf("%s/stream/%s/%s.json", c.baseURL, mediaType, url.PathEscape(id))
	}
	log.Printf("[comet] Fetching: %s", endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addBrowserHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("comet %s returned %d: %s", id, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload cometResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode comet response: %w", err)
	}

	streams := make([]cometStream, 0, len(payload.Streams))
	for _, stream := range payload.Streams {
		infoHash := strings.ToLower(strings.TrimSpace(stream.InfoHash))
		streamURL := strings.TrimSpace(stream.URL)

		// Must have either infoHash (torrent) or URL (pre-resolved debrid stream)
		if infoHash == "" && streamURL == "" {
			continue
		}

		name := strings.TrimSpace(stream.Name)
		rawTitle := strings.TrimSpace(stream.Title)

		// Try to get filename from behaviorHints for URL-based streams
		filename := ""
		if stream.BehaviorHints != nil {
			if fn, ok := stream.BehaviorHints["filename"].(string); ok {
				filename = strings.TrimSpace(fn)
			}
		}

		// Skip streams with incompatible audio codecs that VLC can't decode
		if hasIncompatibleAudioCodec(name, rawTitle) || hasIncompatibleAudioCodec(filename, "") {
			continue
		}

		fileIdx := 0
		if stream.FileIdx != nil {
			fileIdx = *stream.FileIdx
		}

		// For URL-based streams, prefer filename from behaviorHints
		titleText := filename
		if titleText == "" {
			titleText = deriveTitle(rawTitle)
		}

		sizeBytes := parseSize(rawTitle)
		// Check behaviorHints.videoSize for URL-based streams
		if sizeBytes == 0 && stream.BehaviorHints != nil {
			if vs, ok := stream.BehaviorHints["videoSize"].(float64); ok && vs > 0 {
				sizeBytes = int64(vs)
			}
		}

		seeders := parseInt(stream.Seeders, rawTitle)
		provider := parseProvider(rawTitle)
		languages := parseLanguages(rawTitle)
		resolution := detectResolution(name, rawTitle)
		trackers := parseTrackers(stream.BehaviorHints)

		if sizeBytes == 0 {
			if alt := parseSizeFromInterface(stream.Size); alt > 0 {
				sizeBytes = alt
			}
		}
		if seeders == 0 {
			if alt := parseInt(stream.Seeders, ""); alt > 0 {
				seeders = alt
			}
		}
		if provider == "" && stream.Tracker != nil {
			if val := fmt.Sprint(stream.Tracker); val != "" {
				provider = val
			}
		}

		streams = append(streams, cometStream{
			titleText:  titleText,
			infoHash:   infoHash,
			fileIdx:    fileIdx,
			sizeBytes:  sizeBytes,
			seeders:    seeders,
			provider:   provider,
			languages:  languages,
			resolution: resolution,
			trackers:   trackers,
			rawTitle:   rawTitle,
			name:       name,
			url:        streamURL,
		})
	}

	return streams, nil
}

// TestConnection verifies the Comet endpoint is reachable by fetching the manifest.
func (c *CometScraper) TestConnection(ctx context.Context) error {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}

	// Build manifest URL with options if present
	var endpoint string
	if c.options != "" {
		endpoint = fmt.Sprintf("%s/%s/manifest.json", c.baseURL, c.options)
	} else {
		endpoint = fmt.Sprintf("%s/manifest.json", c.baseURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	addBrowserHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to Comet: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Comet returned status %d", resp.StatusCode)
	}

	return nil
}

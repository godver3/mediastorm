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
	"strings"
	"time"

	"novastream/internal/mediaresolve"
	"novastream/models"
)

// AIOStreamsScraper queries AIOStreams for pre-resolved debrid streams.
// AIOStreams aggregates multiple Stremio addons and returns ready-to-play URLs.
type AIOStreamsScraper struct {
	name              string // User-configured name for display
	baseURL           string
	httpClient        *http.Client
	passthroughFormat bool // When true, pass raw aiostreams format to manual selection
}

// NewAIOStreamsScraper constructs a scraper for AIOStreams.
// The name parameter is the user-configured display name (empty falls back to "aiostreams").
// The baseURL should be the full path including the user config portion,
// e.g., "https://aiostreams.elfhosted.com/stremio/{userId}/{configToken}"
// The passthroughFormat parameter controls whether raw aiostreams format is shown in manual selection.
func NewAIOStreamsScraper(baseURL, name string, passthroughFormat bool, client *http.Client) *AIOStreamsScraper {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Normalize URL: trim trailing slashes and /manifest.json if present
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/manifest.json")
	return &AIOStreamsScraper{
		name:              strings.TrimSpace(name),
		baseURL:           baseURL,
		httpClient:        client,
		passthroughFormat: passthroughFormat,
	}
}

func (a *AIOStreamsScraper) Name() string {
	if a.name != "" {
		return a.name
	}
	return "aiostreams"
}

func (a *AIOStreamsScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	// AIOStreams requires an IMDB ID - it doesn't support text search
	imdbID := strings.TrimSpace(req.IMDBID)
	if imdbID == "" {
		log.Printf("[aiostreams] No IMDB ID provided, skipping search")
		return nil, nil
	}

	// Ensure IMDB ID has "tt" prefix
	if !strings.HasPrefix(strings.ToLower(imdbID), "tt") {
		imdbID = "tt" + imdbID
	}

	log.Printf("[aiostreams] Search called with IMDBID=%q, Season=%d, Episode=%d, MediaType=%s, IsDaily=%v, TargetAirDate=%q",
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
			log.Printf("[aiostreams] Daily show detected, will try E%d, E%d, E%d for target date %s",
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

			streams, err := a.fetchStreams(ctx, stremioType, streamID)
			if err != nil {
				errs = append(errs, fmt.Errorf("aiostreams %s %s: %w", stremioType, streamID, err))
				continue
			}

			var batchResults []ScrapeResult
			foundCorrectDate := false

			for _, stream := range streams {
				if stream.url == "" {
					continue
				}
				// Use URL as unique identifier since there's no infohash
				guid := fmt.Sprintf("%s:%s", a.Name(), stream.url)
				if _, exists := seen[guid]; exists {
					continue
				}
				seen[guid] = struct{}{}

				attrs := stream.attributes()
				if a.passthroughFormat {
					attrs["passthrough_format"] = "true"
				}

				result := ScrapeResult{
					Title:       stream.filename,
					Indexer:     a.Name(),
					TorrentURL:  stream.url, // Use TorrentURL field to store the direct stream URL
					InfoHash:    "",         // No infohash - these are pre-resolved streams
					FileIndex:   0,
					SizeBytes:   stream.sizeBytes,
					Seeders:     0, // Not applicable for pre-resolved streams
					Provider:    stream.provider,
					Languages:   stream.languages,
					Resolution:  stream.resolution,
					MetaName:    stream.title,
					MetaID:      imdbID,
					Source:      a.Name(),
					Attributes:  attrs,
					ServiceType: models.ServiceTypeDebrid,
				}

				// For daily shows, check if this result matches the target date
				if isDailySearch {
					if mediaresolve.CandidateMatchesDailyDate(stream.filename, req.TargetAirDate, 0) {
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
				log.Printf("[aiostreams] Found %d results matching target date %s at episode %d, stopping search",
					len(batchResults), req.TargetAirDate, episode)
				break
			}

			if req.MaxResults > 0 && len(results) >= req.MaxResults {
				return results, nil
			}
		}
	}

	if len(results) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("aiostreams search failed: %w", errs[0])
	}

	log.Printf("[aiostreams] Found %d streams for %s", len(results), imdbID)
	return results, nil
}

type aiostreamsResponse struct {
	Streams []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
		ExternalURL string `json:"externalUrl"`
		StreamData  struct {
			Type string `json:"type"`
		} `json:"streamData"`
		BehaviorHints struct {
			BingeGroup string `json:"bingeGroup"`
			VideoSize  int64  `json:"videoSize"`
			Filename   string `json:"filename"`
		} `json:"behaviorHints"`
	} `json:"streams"`
}

type aiostreamsStream struct {
	title      string
	filename   string
	url        string
	sizeBytes  int64
	resolution string
	provider   string
	languages  []string
	source     string // e.g., "BluRay REMUX"
	hdr        string // e.g., "HDR", "DV", "HDR10"
	codec      string // e.g., "HEVC"
	audio      string // e.g., "DTS-HD MA", "Atmos"
	rawName    string // Raw name from aiostreams response (contains quality badge)
	rawDesc    string // Raw description from aiostreams response (emoji-formatted details)
}

func (s aiostreamsStream) attributes() map[string]string {
	attrs := map[string]string{
		"scraper":    "aiostreams",
		"raw_title":  s.filename,
		"stream_url": s.url,
	}
	if s.provider != "" {
		attrs["tracker"] = s.provider
	}
	if s.resolution != "" {
		attrs["resolution"] = s.resolution
	}
	if s.source != "" {
		attrs["source"] = s.source
	}
	if s.hdr != "" {
		attrs["hdr"] = s.hdr
	}
	if s.codec != "" {
		attrs["codec"] = s.codec
	}
	if s.audio != "" {
		attrs["audio"] = s.audio
	}
	if len(s.languages) > 0 {
		attrs["languages"] = strings.Join(s.languages, ",")
	}
	if s.rawName != "" {
		attrs["raw_name"] = s.rawName
	}
	if s.rawDesc != "" {
		attrs["raw_description"] = s.rawDesc
	}
	// Mark as pre-resolved so downstream knows not to resolve again
	attrs["preresolved"] = "true"
	return attrs
}

// Regex patterns for parsing AIOStreams description
var (
	aioSizeRegex     = regexp.MustCompile(`📦\s*([\d.,]+)\s*([KMGTP]?B)`)
	aioProviderRegex = regexp.MustCompile(`📡\s*(\S+)`)
	aioSourceRegex   = regexp.MustCompile(`🎥\s*([^\n📺🎞️🎧]+)`)
	aioHDRRegex      = regexp.MustCompile(`📺\s*([^\n🎞️🎧]+)`)
	aioCodecRegex    = regexp.MustCompile(`🎞️\s*([^\n🎧📦]+)`)
	aioAudioRegex    = regexp.MustCompile(`🎧\s*([^\n🔊📦]+)`)
)

func (a *AIOStreamsScraper) fetchStreams(ctx context.Context, mediaType, id string) ([]aiostreamsStream, error) {
	if id == "" {
		return nil, fmt.Errorf("empty stream id")
	}

	endpoint := fmt.Sprintf("%s/stream/%s/%s.json", a.baseURL, mediaType, url.PathEscape(id))
	log.Printf("[aiostreams] Fetching: %s", endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addBrowserHeaders(req)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("aiostreams %s returned %d: %s", id, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload aiostreamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode aiostreams response: %w", err)
	}

	streams := make([]aiostreamsStream, 0, len(payload.Streams))
	for _, stream := range payload.Streams {
		if strings.EqualFold(strings.TrimSpace(stream.StreamData.Type), "statistic") {
			log.Printf("[aiostreams] skipping statistic stream entry: %s", strings.TrimSpace(stream.Name))
			continue
		}

		streamURL := strings.TrimSpace(stream.URL)
		if streamURL == "" {
			if externalURL := strings.TrimSpace(stream.ExternalURL); externalURL != "" {
				log.Printf("[aiostreams] skipping externalUrl-only stream entry %q: %s", strings.TrimSpace(stream.Name), externalURL)
			}
			continue
		}

		if IsKnownPlaceholderURL(streamURL) {
			log.Printf("[aiostreams] skipping known placeholder stream URL: %s", streamURL)
			continue
		}

		filename := strings.TrimSpace(stream.BehaviorHints.Filename)
		if filename == "" {
			// Try to extract from URL or description
			filename = extractFilenameFromURL(streamURL)
		}

		parsed := parseAIODescription(stream.Description)
		resolution := detectAIOResolution(stream.Name, stream.BehaviorHints.BingeGroup)

		// Use size from behaviorHints if available
		sizeBytes := stream.BehaviorHints.VideoSize
		if sizeBytes == 0 {
			sizeBytes = parsed.sizeBytes
		}

		streams = append(streams, aiostreamsStream{
			title:      extractTitleFromDescription(stream.Description),
			filename:   filename,
			url:        streamURL,
			sizeBytes:  sizeBytes,
			resolution: resolution,
			provider:   parsed.provider,
			languages:  parsed.languages,
			source:     parsed.source,
			hdr:        parsed.hdr,
			codec:      parsed.codec,
			audio:      parsed.audio,
			rawName:    stream.Name,
			rawDesc:    stream.Description,
		})
	}

	return streams, nil
}

type parsedAIODescription struct {
	provider  string
	source    string
	hdr       string
	codec     string
	audio     string
	languages []string
	sizeBytes int64
}

func parseAIODescription(desc string) parsedAIODescription {
	var p parsedAIODescription

	// Extract provider (📡)
	if match := aioProviderRegex.FindStringSubmatch(desc); len(match) > 1 {
		p.provider = strings.TrimSpace(match[1])
	}

	// Extract source (🎥) - e.g., "BluRay REMUX"
	if match := aioSourceRegex.FindStringSubmatch(desc); len(match) > 1 {
		p.source = strings.TrimSpace(match[1])
	}

	// Extract HDR info (📺) - e.g., "HDR | DV"
	if match := aioHDRRegex.FindStringSubmatch(desc); len(match) > 1 {
		p.hdr = strings.TrimSpace(match[1])
	}

	// Extract codec (🎞️) - e.g., "HEVC"
	if match := aioCodecRegex.FindStringSubmatch(desc); len(match) > 1 {
		codec := strings.TrimSpace(match[1])
		// Clean up - might contain episode info like "E01"
		if !strings.HasPrefix(strings.ToUpper(codec), "E0") && !strings.HasPrefix(strings.ToUpper(codec), "E1") {
			p.codec = codec
		}
	}

	// Extract audio (🎧) - e.g., "Atmos | DTS-HD MA"
	if match := aioAudioRegex.FindStringSubmatch(desc); len(match) > 1 {
		p.audio = strings.TrimSpace(match[1])
	}

	// Extract size (📦)
	if match := aioSizeRegex.FindStringSubmatch(desc); len(match) == 3 {
		p.sizeBytes = parseSize("💾 " + match[1] + " " + match[2]) // Reuse existing parseSize
	}

	// Extract languages from flags in description
	p.languages = extractLanguagesFromDesc(desc)

	return p
}

func detectAIOResolution(name, bingeGroup string) string {
	combined := strings.ToLower(name + " " + bingeGroup)
	switch {
	case strings.Contains(combined, "2160p") || strings.Contains(combined, "4k"):
		return "2160p"
	case strings.Contains(combined, "1080p"):
		return "1080p"
	case strings.Contains(combined, "720p"):
		return "720p"
	case strings.Contains(combined, "480p"):
		return "480p"
	default:
		return ""
	}
}

func extractTitleFromDescription(desc string) string {
	// Title is usually after 🎬 emoji
	if idx := strings.Index(desc, "🎬"); idx >= 0 {
		rest := desc[idx+len("🎬"):]
		// Find end of line or next emoji
		end := strings.IndexAny(rest, "\n🎥📺🎞️🎧📦")
		if end > 0 {
			return strings.TrimSpace(rest[:end])
		}
		return strings.TrimSpace(rest)
	}
	// Fallback: first line
	lines := strings.Split(desc, "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

func extractFilenameFromURL(streamURL string) string {
	parsed, err := url.Parse(streamURL)
	if err != nil {
		return ""
	}
	// Check query parameter "name"
	if name := parsed.Query().Get("name"); name != "" {
		return name
	}
	// Use last path segment
	segments := strings.Split(parsed.Path, "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if seg := strings.TrimSpace(segments[i]); seg != "" {
			decoded, err := url.PathUnescape(seg)
			if err == nil {
				return decoded
			}
			return seg
		}
	}
	return ""
}

func extractLanguagesFromDesc(desc string) []string {
	var languages []string

	// Common language flags and indicators
	langPatterns := map[string]string{
		"🇬🇧": "English",
		"🇺🇸": "English",
		"🇩🇪": "German",
		"🇫🇷": "French",
		"🇪🇸": "Spanish",
		"🇮🇹": "Italian",
		"🇷🇺": "Russian",
		"🇯🇵": "Japanese",
		"🇨🇳": "Chinese",
		"🇰🇷": "Korean",
		"🇵🇹": "Portuguese",
		"🇳🇱": "Dutch",
		"🇵🇱": "Polish",
		"🇸🇪": "Swedish",
		"🇨🇿": "Czech",
		"🇭🇺": "Hungarian",
		"🇹🇷": "Turkish",
		"🌎":  "Multi",
	}

	for flag, lang := range langPatterns {
		if strings.Contains(desc, flag) {
			languages = append(languages, lang)
		}
	}

	// Check for "Dubbed" indicator
	if strings.Contains(strings.ToLower(desc), "dubbed") {
		if len(languages) == 0 {
			languages = append(languages, "Dubbed")
		}
	}

	return languages
}

// TestConnection verifies the AIOStreams endpoint is reachable by fetching the manifest.
func (a *AIOStreamsScraper) TestConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := fmt.Sprintf("%s/manifest.json", a.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	addBrowserHeaders(req)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to AIOStreams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("AIOStreams returned status %d", resp.StatusCode)
	}

	return nil
}

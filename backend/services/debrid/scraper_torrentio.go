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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/models"
)

const (
	torrentioDefaultBaseURL = "https://torrentio.strem.fun"
	cinemetaBaseURL         = "https://v3-cinemeta.strem.io"
	maxStreamsPerMeta       = 50
	maxMetasToInspect       = 3
)

// TorrentioScraper queries torrentio for releases using Cinemeta-backed metadata resolution.
type TorrentioScraper struct {
	baseURL    string
	options    string // URL path options (e.g., "sort=qualitysize|qualityfilter=480p,scr,cam")
	httpClient *http.Client
}

// NewTorrentioScraper constructs a scraper with sane defaults.
// The options parameter is inserted between the base URL and /stream path
// (e.g., "sort=qualitysize|qualityfilter=480p,scr,cam").
func NewTorrentioScraper(client *http.Client, options string) *TorrentioScraper {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &TorrentioScraper{
		baseURL:    torrentioDefaultBaseURL,
		options:    strings.TrimSpace(options),
		httpClient: client,
	}
}

func (t *TorrentioScraper) Name() string {
	return "torrentio"
}

func (t *TorrentioScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	cleanTitle := strings.TrimSpace(req.Parsed.Title)
	if cleanTitle == "" && req.IMDBID == "" {
		return nil, nil
	}

	log.Printf("[torrentio] Search called with Query=%q, ParsedTitle=%q, Season=%d, Episode=%d, Year=%d, MediaType=%s, IMDBID=%q",
		req.Query, cleanTitle, req.Parsed.Season, req.Parsed.Episode, req.Parsed.Year, req.Parsed.MediaType, req.IMDBID)

	// If IMDB ID is provided, skip Cinemeta search and use it directly
	if req.IMDBID != "" {
		return t.searchByIMDBID(ctx, req)
	}

	mediaCandidates := determineMediaCandidates(req.Parsed.MediaType)

	var (
		results []ScrapeResult
		errs    []error
		seen    = make(map[string]struct{})
	)

	for _, mediaType := range mediaCandidates {
		metas, err := t.fetchCinemeta(ctx, cleanTitle, mediaType, req.Parsed.Year)
		if err != nil {
			errs = append(errs, fmt.Errorf("cinemeta %s: %w", mediaType, err))
			continue
		}
		for idx, meta := range metas {
			if idx >= maxMetasToInspect {
				break
			}
			streamID := meta.id
			if mediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 {
				streamID = fmt.Sprintf("%s:%d:%d", meta.id, req.Parsed.Season, req.Parsed.Episode)
			}
			log.Printf("[torrentio] Fetching streams for meta[%d]: ID=%s, Name=%s, streamID=%s", idx, meta.id, meta.name, streamID)
			streams, err := t.fetchStreams(ctx, mediaType, streamID)
			if err != nil {
				errs = append(errs, fmt.Errorf("torrentio %s %s: %w", mediaType, streamID, err))
				continue
			}
			for _, stream := range streams {
				if stream.infoHash == "" {
					continue
				}
				guid := fmt.Sprintf("%s:%s:%d", t.Name(), strings.ToLower(stream.infoHash), stream.fileIdx)
				if _, exists := seen[guid]; exists {
					continue
				}
				seen[guid] = struct{}{}
				results = append(results, ScrapeResult{
					Title:       stream.titleText,
					Indexer:     "Torrentio",
					Magnet:      buildMagnet(stream.infoHash, stream.trackers),
					InfoHash:    stream.infoHash,
					FileIndex:   stream.fileIdx,
					SizeBytes:   stream.sizeBytes,
					Seeders:     stream.seeders,
					Provider:    stream.provider,
					Languages:   stream.languages,
					Resolution:  stream.resolution,
					MetaName:    meta.name,
					MetaID:      meta.id,
					Source:      t.Name(),
					Attributes:  stream.attributes(),
					ServiceType: models.ServiceTypeDebrid,
				})
				if req.MaxResults > 0 && len(results) >= req.MaxResults {
					return results, nil
				}
				if len(results) >= maxStreamsPerMeta {
					break
				}
			}
		}
	}

	if len(results) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return results, nil
}

func (t *TorrentioScraper) searchByIMDBID(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	imdbID := strings.TrimSpace(req.IMDBID)
	if imdbID == "" {
		return nil, fmt.Errorf("empty IMDB ID")
	}

	log.Printf("[torrentio] Using IMDB ID directly: %s", imdbID)

	// Determine media type from parsed query or try both
	mediaCandidates := determineMediaCandidates(req.Parsed.MediaType)

	var (
		results []ScrapeResult
		errs    []error
		seen    = make(map[string]struct{})
	)

	for _, mediaType := range mediaCandidates {
		streamID := imdbID
		if mediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 {
			streamID = fmt.Sprintf("%s:%d:%d", imdbID, req.Parsed.Season, req.Parsed.Episode)
		}

		streams, err := t.fetchStreams(ctx, mediaType, streamID)
		if err != nil {
			errs = append(errs, fmt.Errorf("torrentio %s %s: %w", mediaType, streamID, err))
			continue
		}

		for _, stream := range streams {
			if stream.infoHash == "" {
				continue
			}
			guid := fmt.Sprintf("%s:%s:%d", t.Name(), strings.ToLower(stream.infoHash), stream.fileIdx)
			if _, exists := seen[guid]; exists {
				continue
			}
			seen[guid] = struct{}{}
			results = append(results, ScrapeResult{
				Title:       stream.titleText,
				Indexer:     "Torrentio",
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
				Source:      t.Name(),
				Attributes:  stream.attributes(),
				ServiceType: models.ServiceTypeDebrid,
			})
			if req.MaxResults > 0 && len(results) >= req.MaxResults {
				return results, nil
			}
		}
	}

	if len(results) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return results, nil
}

func determineMediaCandidates(inferred MediaType) []MediaType {
	switch inferred {
	case MediaTypeMovie:
		return []MediaType{MediaTypeMovie}
	case MediaTypeSeries:
		return []MediaType{MediaTypeSeries}
	default:
		return []MediaType{MediaTypeMovie, MediaTypeSeries}
	}
}

type cinemetaResponse struct {
	Metas []struct {
		ID     string `json:"id"`
		ImdbID string `json:"imdb_id"`
		Name   string `json:"name"`
		Year   string `json:"year"`
		Type   string `json:"type"`
	} `json:"metas"`
}

type cinemetaMeta struct {
	id   string
	name string
	year int
}

func (t *TorrentioScraper) fetchCinemeta(ctx context.Context, title string, mediaType MediaType, preferredYear int) ([]cinemetaMeta, error) {
	endpoint := fmt.Sprintf("%s/catalog/%s/search=%s.json", cinemetaBaseURL, mediaType, url.PathEscape(title))
	log.Printf("[torrentio] Fetching cinemeta: %s", endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addBrowserHeaders(req)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("cinemeta %s search returned %d: %s", mediaType, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Read body for better error logging
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read cinemeta response: %w", err)
	}

	var payload cinemetaResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("decode cinemeta response: %w (body preview: %s)", err, preview)
	}
	log.Printf("[torrentio] Cinemeta returned %d metas for %q", len(payload.Metas), title)

	// Build candidates with title matching scores
	type scoredMeta struct {
		meta  cinemetaMeta
		score int
	}
	var candidates []scoredMeta

	searchLower := strings.ToLower(strings.TrimSpace(title))
	for idx, meta := range payload.Metas {
		if idx < 3 {
			log.Printf("[torrentio]   Meta[%d]: ID=%s, ImdbID=%s, Name=%s, Year=%s", idx, meta.ID, meta.ImdbID, meta.Name, meta.Year)
		}
		id := strings.TrimSpace(meta.ID)
		log.Printf("[torrentio]   Processing meta[%d]: initial ID=%s, ImdbID=%s, Name=%s", idx, id, meta.ImdbID, meta.Name)
		if id == "" && strings.TrimSpace(meta.ImdbID) != "" {
			id = strings.TrimSpace(meta.ImdbID)
			log.Printf("[torrentio]   Meta[%d]: ID was empty, using ImdbID=%s", idx, id)
		}
		if id == "" {
			log.Printf("[torrentio]   Meta[%d]: skipping - no ID or ImdbID", idx)
			continue
		}
		if !strings.HasPrefix(strings.ToLower(id), "tt") && strings.TrimSpace(meta.ImdbID) != "" {
			log.Printf("[torrentio]   Meta[%d]: ID=%s doesn't start with 'tt', switching to ImdbID=%s", idx, id, meta.ImdbID)
			id = strings.TrimSpace(meta.ImdbID)
		}
		if id == "" {
			log.Printf("[torrentio]   Meta[%d]: skipping - final ID is empty", idx)
			continue
		}
		log.Printf("[torrentio]   Meta[%d]: final ID=%s for Name=%s", idx, id, meta.Name)
		year := parseYear(meta.Year)
		if preferredYear > 0 && year > 0 && abs(preferredYear-year) > 1 {
			continue
		}
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = id
		}

		// Score based on title similarity
		nameLower := strings.ToLower(name)
		score := 0
		if nameLower == searchLower {
			score = 1000 // Exact match
		} else if strings.HasPrefix(nameLower, searchLower) {
			score = 500 // Starts with search
		} else if strings.Contains(nameLower, searchLower) {
			score = 250 // Contains search
		}

		if idx < 5 {
			log.Printf("[torrentio]   Meta[%d] scoring: Name=%q vs Search=%q => score=%d", idx, nameLower, searchLower, score)
		}

		candidates = append(candidates, scoredMeta{
			meta:  cinemetaMeta{id: id, name: name, year: year},
			score: score,
		})
	}

	// Sort by score (highest first), then by original order
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Filter out results with no match (score=0) to avoid using wrong shows
	// Only keep results that have some similarity to the search query
	filteredCandidates := make([]scoredMeta, 0, len(candidates))
	for _, c := range candidates {
		if c.score > 0 {
			filteredCandidates = append(filteredCandidates, c)
		}
	}

	// Extract sorted and filtered metas
	metas := make([]cinemetaMeta, 0, len(filteredCandidates))
	for _, c := range filteredCandidates {
		metas = append(metas, c.meta)
	}

	if len(metas) > 0 {
		log.Printf("[torrentio] Top match after scoring and filtering: %s (score=%d)", metas[0].name, filteredCandidates[0].score)
	} else if len(candidates) > 0 {
		log.Printf("[torrentio] No matches found - all %d candidates had score=0 (no similarity to %q)", len(candidates), title)
	}

	return metas, nil
}

type torrentioResponse struct {
	Streams []struct {
		Name          string                 `json:"name"`
		Title         string                 `json:"title"`
		InfoHash      string                 `json:"infoHash"`
		FileIdx       *int                   `json:"fileIdx"`
		URL           string                 `json:"url"`
		Size          interface{}            `json:"size"`
		Seeders       interface{}            `json:"seeders"`
		Tracker       interface{}            `json:"tracker"`
		Availability  string                 `json:"availability"`
		BehaviorHints map[string]interface{} `json:"behaviorHints"`
	} `json:"streams"`
}

type torrentioStream struct {
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
}

func (s torrentioStream) attributes() map[string]string {
	attrs := map[string]string{
		"scraper":   "torrentio",
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
	return attrs
}

var (
	reSize      = regexp.MustCompile(`💾\s*([\d.,]+)\s*([KMGTP]?B)`)
	reSeeders   = regexp.MustCompile(`👤\s*(\d+)`)
	reProvider  = regexp.MustCompile(`⚙️\s*([^\n]+)`)
	reLanguages = regexp.MustCompile(`[\p{So}]{1,2}`)
)

// incompatibleAudioCodecs lists audio codecs that VLC and many mobile players cannot decode
var incompatibleAudioCodecs = []string{
	"truehd",
	"atmos",
	"dts-hd",
	"dts-x",
	"dts:x",
}

// hasIncompatibleAudioCodec checks if a stream contains audio codecs that VLC cannot decode
func hasIncompatibleAudioCodec(name, rawTitle string) bool {
	combined := strings.ToLower(name + " " + rawTitle)
	for _, codec := range incompatibleAudioCodecs {
		if strings.Contains(combined, codec) {
			return true
		}
	}
	return false
}

func (t *TorrentioScraper) fetchStreams(ctx context.Context, mediaType MediaType, id string) ([]torrentioStream, error) {
	if id == "" {
		return nil, fmt.Errorf("empty torrentio id")
	}
	// Build endpoint with optional path options
	// Format: baseURL/[options/]stream/mediaType/id.json
	var endpoint string
	if t.options != "" {
		endpoint = fmt.Sprintf("%s/%s/stream/%s/%s.json", t.baseURL, t.options, mediaType, url.PathEscape(id))
	} else {
		endpoint = fmt.Sprintf("%s/stream/%s/%s.json", t.baseURL, mediaType, url.PathEscape(id))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addBrowserHeaders(req)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("torrentio %s returned %d: %s", id, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload torrentioResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode torrentio response: %w", err)
	}

	streams := make([]torrentioStream, 0, len(payload.Streams))
	for _, stream := range payload.Streams {
		infoHash := strings.ToLower(strings.TrimSpace(stream.InfoHash))
		if infoHash == "" {
			continue
		}

		name := strings.TrimSpace(stream.Name)
		rawTitle := strings.TrimSpace(stream.Title)

		// Skip streams with incompatible audio codecs that VLC can't decode
		if hasIncompatibleAudioCodec(name, rawTitle) {
			continue
		}

		fileIdx := 0
		if stream.FileIdx != nil {
			fileIdx = *stream.FileIdx
		}
		titleText := deriveTitle(rawTitle)
		sizeBytes := parseSize(rawTitle)
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

		streams = append(streams, torrentioStream{
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
		})
	}

	return streams, nil
}

func deriveTitle(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 0 {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(lines[0])
}

func parseSize(raw string) int64 {
	match := reSize.FindStringSubmatch(raw)
	if len(match) != 3 {
		return 0
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", ""), 64)
	if err != nil {
		return 0
	}
	unit := strings.ToUpper(match[2])
	multipliers := map[string]float64{
		"KB": 1024,
		"MB": 1024 * 1024,
		"GB": 1024 * 1024 * 1024,
		"TB": 1024 * 1024 * 1024 * 1024,
		"PB": 1024 * 1024 * 1024 * 1024 * 1024,
	}
	if mult, exists := multipliers[unit]; exists {
		return int64(value * mult)
	}
	return 0
}

func parseSizeFromInterface(src interface{}) int64 {
	switch v := src.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		return parseSize(v)
	default:
		return 0
	}
}

func parseInt(src interface{}, fallback string) int {
	switch v := src.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if val, err := v.Int64(); err == nil {
			return int(val)
		}
	case string:
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			if val, err := strconv.Atoi(trimmed); err == nil {
				return val
			}
		}
	}
	if fallback != "" {
		if match := reSeeders.FindStringSubmatch(fallback); len(match) == 2 {
			if val, err := strconv.Atoi(match[1]); err == nil {
				return val
			}
		}
	}
	return 0
}

func parseProvider(raw string) string {
	match := reProvider.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	provider := strings.TrimSpace(match[1])
	provider = strings.TrimSuffix(provider, "Multi Audio")
	return strings.TrimSpace(provider)
}

func parseLanguages(raw string) []string {
	languageMatches := reLanguages.FindAllString(raw, -1)
	if len(languageMatches) == 0 {
		return nil
	}
	norm := make([]string, 0, len(languageMatches))
	for _, symbol := range languageMatches {
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			continue
		}
		switch symbol {
		case "👤", "💾", "⚙️":
			continue
		}
		norm = append(norm, symbol)
	}
	return norm
}

func detectResolution(name, raw string) string {
	release := strings.ToLower(name + " " + raw)
	switch {
	case strings.Contains(release, "2160p") || strings.Contains(release, "4k"):
		return "2160p"
	case strings.Contains(release, "1080p"):
		return "1080p"
	case strings.Contains(release, "720p"):
		return "720p"
	case strings.Contains(release, "480p"):
		return "480p"
	default:
		return ""
	}
}

func parseTrackers(hints map[string]interface{}) []string {
	if len(hints) == 0 {
		return nil
	}
	raw, ok := hints["openTrackers"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	trackers := make([]string, 0, len(arr))
	for _, item := range arr {
		if str, ok := item.(string); ok {
			if trimmed := strings.TrimSpace(str); trimmed != "" {
				trackers = append(trackers, trimmed)
			}
		}
	}
	return trackers
}

func buildMagnet(infoHash string, trackers []string) string {
	if infoHash == "" {
		return ""
	}
	builder := strings.Builder{}
	builder.WriteString("magnet:?xt=urn:btih:")
	builder.WriteString(strings.ToUpper(infoHash))
	for _, tracker := range trackers {
		builder.WriteString("&tr=")
		builder.WriteString(url.QueryEscape(tracker))
	}
	return builder.String()
}

func parseYear(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if len(value) >= 4 {
		value = value[:4]
	}
	year, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return year
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func addBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Don't set Accept-Encoding - Go's Transport handles this automatically
	req.Header.Set("DNT", "1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
}

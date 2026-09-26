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
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/internal/apiusage"
	"novastream/internal/mediaresolve"
	"novastream/internal/streamheaders"
	"novastream/models"
)

const directStremioType = "stremio-direct"

type directStremioBehaviorHints struct {
	BingeGroup   string `json:"bingeGroup"`
	VideoSize    int64  `json:"videoSize"`
	Filename     string `json:"filename"`
	ProxyHeaders struct {
		Request map[string]string `json:"request"`
	} `json:"proxyHeaders"`
}

type directStremioEntry struct {
	Name          string                     `json:"name"`
	Title         string                     `json:"title"`
	Description   string                     `json:"description"`
	URL           string                     `json:"url"`
	ExternalURL   string                     `json:"externalUrl"`
	BehaviorHints directStremioBehaviorHints `json:"behaviorHints"`
}

type directStremioResponse struct {
	Streams []directStremioEntry `json:"streams"`
}

type DirectStremioScraper struct {
	name       string
	baseURL    string
	httpClient *http.Client
}

func NewDirectStremioScraper(baseURL, name string, client *http.Client) *DirectStremioScraper {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &DirectStremioScraper{
		name:       strings.TrimSpace(name),
		baseURL:    normalizeDirectStremioBaseURL(baseURL),
		httpClient: client,
	}
}

func normalizeDirectStremioBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	return strings.TrimSuffix(value, "/manifest.json")
}

func (s *DirectStremioScraper) Name() string {
	if s.name != "" {
		return s.name
	}
	return directStremioType
}

func (s *DirectStremioScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	imdbID := strings.ToLower(strings.TrimSpace(req.IMDBID))
	if imdbID == "" {
		return nil, nil
	}
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	var results []ScrapeResult
	var errs []error
	seen := make(map[string]struct{})
	for _, mediaType := range determineMediaCandidates(req.Parsed.MediaType) {
		stremioType := "movie"
		if mediaType == MediaTypeSeries {
			stremioType = "series"
		}
		episodes := []int{req.Parsed.Episode}
		isDaily := req.IsDaily && mediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 && req.TargetAirDate != ""
		if isDaily {
			episodes = nil
			if req.Parsed.Episode > 1 {
				episodes = append(episodes, req.Parsed.Episode-1)
			}
			episodes = append(episodes, req.Parsed.Episode, req.Parsed.Episode+1)
		}

		for _, episode := range episodes {
			streamID := imdbID
			if mediaType == MediaTypeSeries && req.Parsed.Season > 0 && episode > 0 {
				streamID = fmt.Sprintf("%s:%d:%d", imdbID, req.Parsed.Season, episode)
			}
			entries, err := s.fetchStreams(ctx, stremioType, streamID)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s %s %s: %w", directStremioType, stremioType, streamID, err))
				continue
			}

			foundDaily := false
			for index, entry := range entries {
				result, ok := s.resultFromEntry(entry, index, stremioType, streamID, imdbID, req.Parsed.Title)
				if !ok {
					continue
				}
				resultKey := result.Indexer + "\x00" + result.TorrentURL
				if _, exists := seen[resultKey]; exists {
					continue
				}
				seen[resultKey] = struct{}{}
				if isDaily {
					target := mediaresolve.EpisodeCode{Season: req.Parsed.Season, Episode: req.Parsed.Episode}
					if !mediaresolve.CandidateMatchesDailyDate(result.Title, req.TargetAirDate, 0) &&
						!mediaresolve.CandidateMatchesEpisode(result.Title, target) &&
						!mediaresolve.CandidateMatchesEpisode(entry.Description, target) {
						continue
					}
					foundDaily = true
				}
				results = append(results, result)
				if req.MaxResults > 0 && len(results) >= req.MaxResults {
					return results[:req.MaxResults], nil
				}
			}
			if isDaily && foundDaily {
				break
			}
		}
	}
	if len(results) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return results, nil
}

func (s *DirectStremioScraper) fetchStreams(ctx context.Context, mediaType, id string) ([]directStremioEntry, error) {
	if s.baseURL == "" || id == "" {
		return nil, fmt.Errorf("missing addon URL or stream id")
	}
	endpoint := fmt.Sprintf("%s/stream/%s/%s.json", s.baseURL, url.PathEscape(mediaType), url.PathEscape(id))
	log.Printf("[%s] fetching %s", directStremioType, safeURLForLog(endpoint))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addBrowserHeaders(req)
	resp, err := apiusage.Do(s.httpClient, s.Name(), "Stream search", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("stream response returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload directStremioResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode stream response: %w", err)
	}
	return payload.Streams, nil
}

var (
	directSourceLine = regexp.MustCompile(`(?mi)^\s*🛰️?\s*Source:\s*(.+?)\s*$`)
	directAudioLine  = regexp.MustCompile(`(?mi)^\s*🎧\s*Audio:\s*(.+?)\s*$`)
	directTechLine   = regexp.MustCompile(`(?mi)^\s*🎞️\s*(.+?)\s*$`)
	directSizeLine   = regexp.MustCompile(`(?mi)^\s*💾\s*([\d.,]+)\s*([KMGTP]?B)\s*$`)
	directInlineSize = regexp.MustCompile(`(?i)([\d]+(?:[.,][\d]+)?)\s*(B|KB|MB|GB|TB|PB|KIB|MIB|GIB|TIB|PIB)\b`)
	directExt        = regexp.MustCompile(`(?i)\.(mkv|mp4|m4v|avi|webm|ts|m2ts)`)
	directTagPrefix  = regexp.MustCompile(`^(?:\[[^\]]+\]\s*)+`)
)

func (s *DirectStremioScraper) resultFromEntry(entry directStremioEntry, index int, mediaType, streamID, imdbID, metaName string) (ScrapeResult, bool) {
	if directStremioDownloadOnly(entry) {
		return ScrapeResult{}, false
	}
	streamURL := strings.TrimSpace(entry.URL)
	if streamURL == "" || IsKnownPlaceholderURL(streamURL) {
		return ScrapeResult{}, false
	}
	headers := streamheaders.Sanitize(entry.BehaviorHints.ProxyHeaders.Request)
	streamURL = streamheaders.Attach(streamURL, headers)
	filename := normalizeDirectStremioFilename(entry.BehaviorHints.Filename)
	if filename == "" {
		urlFilename := extractFilenameFromURL(entry.URL)
		if directExt.MatchString(urlFilename) {
			filename = normalizeDirectStremioFilename(urlFilename)
		}
	}
	if filename == "" {
		filename = directStremioDisplayTitle(entry.Description)
	}
	if filename == "" {
		filename = normalizeDirectStremioFilename(entry.Title)
	}
	if filename == "" {
		filename = normalizeDirectStremioFilename(entry.Name)
	}
	resolution := detectResolution(entry.Name, entry.Description)
	if resolution == "" {
		resolution = detectResolution(entry.Title, "")
	}
	provider := directStremioMatch(directSourceLine, entry.Description)
	languages := directStremioLanguages(entry.Description)
	attrs := map[string]string{
		"scraper":                directStremioType,
		"preresolved":            "true",
		"stream_url":             streamURL,
		"raw_title":              filename,
		"raw_name":               strings.TrimSpace(entry.Name),
		"raw_description":        strings.TrimSpace(entry.Description),
		"stremio_config_name":    s.Name(),
		"stremio_type":           mediaType,
		"stremio_id":             streamID,
		"stremio_stream_index":   strconv.Itoa(index),
		"stremio_binge_group":    strings.TrimSpace(entry.BehaviorHints.BingeGroup),
		"stremio_filename_match": filename,
	}
	if provider != "" {
		attrs["tracker"] = provider
	}
	if resolution != "" {
		attrs["resolution"] = resolution
	}
	if len(languages) > 0 {
		attrs["languages"] = strings.Join(languages, ",")
	}
	applyDirectStremioTechnicalAttributes(attrs, entry.Description)
	titleSizeBytes, titleSizeLabel := directStremioSize(entry.Description, entry.Title, entry.Name)
	if attrs["size"] == "" && titleSizeLabel != "" {
		attrs["size"] = titleSizeLabel
	}
	sizeBytes := entry.BehaviorHints.VideoSize
	if sizeBytes <= 0 {
		sizeBytes = titleSizeBytes
	}

	return ScrapeResult{
		Title:       filename,
		Indexer:     s.Name(),
		TorrentURL:  streamURL,
		SizeBytes:   sizeBytes,
		Provider:    provider,
		Languages:   languages,
		Resolution:  resolution,
		MetaName:    firstNonEmptyString(strings.TrimSpace(metaName), directStremioDisplayTitle(entry.Description)),
		MetaID:      imdbID,
		Source:      s.Name(),
		Attributes:  attrs,
		ServiceType: models.ServiceTypeDebrid,
	}, true
}

func directStremioDownloadOnly(entry directStremioEntry) bool {
	label := strings.Join([]string{entry.Name, entry.Title, entry.Description}, "\n")
	return strings.Contains(strings.ToLower(label), "download only")
}

func normalizeDirectStremioFilename(value string) string {
	value = strings.TrimSpace(value)
	if match := directExt.FindStringIndex(value); match != nil {
		return value[:match[1]]
	}
	return value
}

func directStremioDisplayTitle(description string) string {
	line := strings.TrimSpace(strings.Split(description, "\n")[0])
	line = strings.TrimSpace(strings.TrimLeft(line, "🍿📡🎬✎☁︎ "))
	return strings.TrimSpace(directTagPrefix.ReplaceAllString(line, ""))
}

func directStremioMatch(pattern *regexp.Regexp, value string) string {
	if match := pattern.FindStringSubmatch(value); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func directStremioLanguages(description string) []string {
	raw := directStremioMatch(directAudioLine, description)
	if raw == "" {
		return extractLanguagesFromDesc(description)
	}
	var out []string
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '|' || r == '·' }) {
		if value = strings.TrimSpace(value); value != "" {
			out = appendUniqueString(out, value)
		}
	}
	return out
}

func applyDirectStremioTechnicalAttributes(attrs map[string]string, description string) {
	line := directStremioMatch(directTechLine, description)
	for _, part := range strings.FieldsFunc(line, func(r rune) bool { return r == '•' || r == '|' }) {
		part = strings.TrimSpace(part)
		upper := strings.ToUpper(part)
		switch {
		case strings.Contains(upper, "DOLBY VISION"), strings.Contains(upper, "DOVI"), upper == "DV", strings.HasPrefix(upper, "HDR"):
			attrs["hdr"] = appendAttributeValue(attrs["hdr"], part)
		case upper == "HEVC", upper == "AVC", upper == "X264", upper == "X265", upper == "H.264", upper == "H.265", upper == "AV1":
			attrs["codec"] = part
		case strings.Contains(upper, "BLURAY"), strings.Contains(upper, "WEB-DL"), strings.Contains(upper, "WEBRIP"), strings.Contains(upper, "REMUX"):
			attrs["source"] = appendAttributeValue(attrs["source"], part)
		case strings.Contains(upper, "ATMOS"), strings.Contains(upper, "DTS"), strings.Contains(upper, "AAC"), strings.Contains(upper, "DDP"):
			attrs["audio"] = appendAttributeValue(attrs["audio"], part)
		}
	}
	if attrs["audio"] == "" {
		delete(attrs, "audio")
	}
	if attrs["source"] == "" {
		delete(attrs, "source")
	}
	if attrs["hdr"] == "" {
		delete(attrs, "hdr")
	}
	if attrs["codec"] == "" {
		delete(attrs, "codec")
	}
	if match := directSizeLine.FindStringSubmatch(description); len(match) == 3 && attrs["size"] == "" {
		attrs["size"] = match[1] + " " + match[2]
	}
}

func directStremioSize(values ...string) (int64, string) {
	for _, value := range values {
		match := directInlineSize.FindStringSubmatch(value)
		if len(match) != 3 {
			continue
		}
		number, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
		if err != nil {
			continue
		}
		unit := strings.ToUpper(match[2])
		var factor float64
		switch unit {
		case "B":
			factor = 1
		case "KB":
			factor = 1_000
		case "MB":
			factor = 1_000_000
		case "GB":
			factor = 1_000_000_000
		case "TB":
			factor = 1_000_000_000_000
		case "PB":
			factor = 1_000_000_000_000_000
		case "KIB":
			factor = 1 << 10
		case "MIB":
			factor = 1 << 20
		case "GIB":
			factor = 1 << 30
		case "TIB":
			factor = 1 << 40
		case "PIB":
			factor = 1 << 50
		}
		if factor == 0 || number <= 0 {
			continue
		}
		return int64(number * factor), match[1] + " " + match[2]
	}
	return 0, ""
}

func appendAttributeValue(existing, value string) string {
	if existing == "" {
		return value
	}
	if strings.Contains(strings.ToLower(existing), strings.ToLower(value)) {
		return existing
	}
	return existing + " | " + value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func refreshDirectStremioCandidate(ctx context.Context, settings config.Settings, candidate models.NZBResult) (models.NZBResult, error) {
	return refreshDirectStremioCandidateWithClient(ctx, settings, candidate, nil)
}

func refreshDirectStremioCandidateWithClient(ctx context.Context, settings config.Settings, candidate models.NZBResult, client *http.Client) (models.NZBResult, error) {
	if candidate.Attributes["scraper"] != directStremioType {
		return candidate, nil
	}
	configName := strings.TrimSpace(candidate.Attributes["stremio_config_name"])
	mediaType := strings.TrimSpace(candidate.Attributes["stremio_type"])
	streamID := strings.TrimSpace(candidate.Attributes["stremio_id"])
	if configName == "" || mediaType == "" || streamID == "" {
		return candidate, fmt.Errorf("direct Stremio candidate is missing refresh metadata")
	}

	var source *config.TorrentScraperConfig
	for i := range settings.TorrentScrapers {
		configured := &settings.TorrentScrapers[i]
		if configured.Enabled && strings.EqualFold(strings.TrimSpace(configured.Type), directStremioType) &&
			strings.EqualFold(strings.TrimSpace(configured.Name), configName) {
			source = configured
			break
		}
	}
	if source == nil || strings.TrimSpace(source.URL) == "" {
		return candidate, fmt.Errorf("direct Stremio source %q is not configured", configName)
	}

	timeout := settings.Streaming.IndexerTimeoutSec
	if timeout <= 0 {
		timeout = 5
	}
	if client == nil {
		client = &http.Client{Timeout: time.Duration(timeout * float64(time.Second))}
	}
	scraper := NewDirectStremioScraper(source.URL, source.Name, client)
	entries, err := scraper.fetchStreams(ctx, mediaType, streamID)
	if err != nil {
		return candidate, fmt.Errorf("refresh direct Stremio stream: %w", err)
	}

	wantGroup := strings.TrimSpace(candidate.Attributes["stremio_binge_group"])
	wantFilename := strings.TrimSpace(candidate.Attributes["stremio_filename_match"])
	wantIndex := -1
	if parsedIndex, err := strconv.Atoi(candidate.Attributes["stremio_stream_index"]); err == nil {
		wantIndex = parsedIndex
	}
	selected := -1
	for index, entry := range entries {
		if strings.TrimSpace(entry.URL) == "" {
			continue
		}
		filename := normalizeDirectStremioFilename(entry.BehaviorHints.Filename)
		if wantGroup != "" && wantFilename != "" && entry.BehaviorHints.BingeGroup == wantGroup && filename == wantFilename {
			selected = index
			break
		}
	}
	if selected < 0 && wantIndex >= 0 && wantIndex < len(entries) && strings.TrimSpace(entries[wantIndex].URL) != "" {
		selected = wantIndex
	}
	if selected < 0 {
		return candidate, fmt.Errorf("selected direct Stremio stream is no longer available")
	}

	refreshed, ok := scraper.resultFromEntry(entries[selected], selected, mediaType, streamID, candidate.Attributes["titleId"], candidate.Attributes["titleName"])
	if !ok {
		return candidate, fmt.Errorf("selected direct Stremio stream is not playable")
	}
	candidate.Link = refreshed.TorrentURL
	candidate.DownloadURL = refreshed.TorrentURL
	candidate.SizeBytes = refreshed.SizeBytes
	if candidate.Attributes == nil {
		candidate.Attributes = make(map[string]string)
	}
	for key, value := range refreshed.Attributes {
		candidate.Attributes[key] = value
	}
	candidate.Attributes["torrentURL"] = refreshed.TorrentURL
	return candidate, nil
}

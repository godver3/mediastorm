package debrid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/internal/mediaresolve"
	"novastream/models"
)

const internetArchiveDefaultBaseURL = "https://archive.org"

// InternetArchiveScraper searches archive.org's video catalog and returns
// directly playable file URLs as pre-resolved streams.
type InternetArchiveScraper struct {
	name       string
	baseURL    string
	httpClient *http.Client
	maxItems   int
	maxFiles   int
}

func NewInternetArchiveScraper(client *http.Client, baseURL, name string, cfg map[string]string) *InternetArchiveScraper {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = internetArchiveDefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	maxItems := parsePositiveConfigInt(cfg, "maxItems", 8)
	maxFiles := parsePositiveConfigInt(cfg, "maxFilesPerItem", 3)
	return &InternetArchiveScraper{
		name:       strings.TrimSpace(name),
		baseURL:    baseURL,
		httpClient: client,
		maxItems:   maxItems,
		maxFiles:   maxFiles,
	}
}

func (i *InternetArchiveScraper) Name() string {
	if i.name != "" {
		return i.name
	}
	return "Internet Archive"
}

func (i *InternetArchiveScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	queryTitle := strings.TrimSpace(req.Query)
	if req.Parsed.Title != "" {
		queryTitle = req.Parsed.Title
	}
	if queryTitle == "" {
		return nil, nil
	}

	docs, err := i.searchItems(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}

	var (
		results []ScrapeResult
		seen    = map[string]struct{}{}
	)
	for _, doc := range docs {
		identifier := strings.TrimSpace(doc.Identifier)
		if identifier == "" {
			continue
		}
		item, err := i.fetchMetadata(ctx, identifier)
		if err != nil {
			log.Printf("[internetarchive] metadata fetch failed for %s: %v", identifier, err)
			continue
		}

		files := selectInternetArchiveVideoFiles(item.Files, req, i.maxFiles)
		for _, file := range files {
			streamURL := i.downloadURL(identifier, file.Name.String())
			if streamURL == "" {
				continue
			}
			if _, exists := seen[streamURL]; exists {
				continue
			}
			seen[streamURL] = struct{}{}

			title, archiveEpisodeMatched := internetArchiveResultTitle(req, item.Metadata, doc.Title.String(), file.Title.String(), file.Name.String())
			sizeBytes := parseArchiveSize(file.Size.String())
			attrs := map[string]string{
				"scraper":           "internetarchive",
				"preresolved":       "true",
				"stream_url":        streamURL,
				"raw_title":         title,
				"archiveIdentifier": identifier,
				"archiveFile":       file.Name.String(),
				"archiveItemURL":    strings.TrimRight(i.baseURL, "/") + "/details/" + url.PathEscape(identifier),
			}
			addArchiveAttr(attrs, "format", file.Format.String())
			addArchiveAttr(attrs, "source", file.Source.String())
			addArchiveAttr(attrs, "titleName", firstNonEmpty(item.Metadata.Title.String(), doc.Title.String(), queryTitle))
			addArchiveAttr(attrs, "year", firstNonEmpty(item.Metadata.Year.String(), doc.Year.String()))
			addArchiveAttr(attrs, "licenseurl", item.Metadata.LicenseURL.String())
			addArchiveAttr(attrs, "collection", strings.Join(item.Metadata.CollectionValues(), ","))
			addArchiveAttr(attrs, "runtime", item.Metadata.Runtime.String())
			if archiveEpisodeMatched {
				attrs["archiveEpisodeMatch"] = "true"
			}
			if res := detectResolution(file.Name.String(), file.Format.String()+" "+file.Title.String()); res != "" {
				attrs["resolution"] = res
			}

			results = append(results, ScrapeResult{
				Title:       title,
				Indexer:     i.Name(),
				TorrentURL:  streamURL,
				SizeBytes:   sizeBytes,
				Provider:    "archive.org",
				MetaName:    firstNonEmpty(item.Metadata.Title.String(), doc.Title.String(), queryTitle),
				MetaID:      identifier,
				Source:      i.Name(),
				Attributes:  attrs,
				ServiceType: models.ServiceTypeDebrid,
			})
			if req.MaxResults > 0 && len(results) >= req.MaxResults {
				return results[:req.MaxResults], nil
			}
		}
	}

	if req.MaxResults > 0 && len(results) > req.MaxResults {
		results = results[:req.MaxResults]
	}
	log.Printf("[internetarchive] Found %d playable files for %q", len(results), queryTitle)
	return results, nil
}

func (i *InternetArchiveScraper) searchItems(ctx context.Context, req SearchRequest) ([]internetArchiveDoc, error) {
	endpoint, err := url.Parse(strings.TrimRight(i.baseURL, "/") + "/advancedsearch.php")
	if err != nil {
		return nil, err
	}
	values := endpoint.Query()
	values.Set("q", buildInternetArchiveQuery(req))
	values.Set("output", "json")
	values.Set("rows", strconv.Itoa(i.maxItems))
	values.Set("page", "1")
	values.Add("sort[]", "downloads desc")
	for _, field := range []string{"identifier", "title", "year", "date", "downloads", "licenseurl", "collection"} {
		values.Add("fl[]", field)
	}
	endpoint.RawQuery = values.Encode()

	var payload internetArchiveSearchResponse
	if err := i.getJSON(ctx, endpoint.String(), &payload); err != nil {
		return nil, err
	}
	return payload.Response.Docs, nil
}

func (i *InternetArchiveScraper) fetchMetadata(ctx context.Context, identifier string) (*internetArchiveMetadataResponse, error) {
	endpoint := strings.TrimRight(i.baseURL, "/") + "/metadata/" + url.PathEscape(identifier)
	var payload internetArchiveMetadataResponse
	if err := i.getJSON(ctx, endpoint, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (i *InternetArchiveScraper) getJSON(ctx context.Context, endpoint string, dst interface{}) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "strmr/1.0 (+https://github.com/godver3/mediastorm)")
	resp, err := i.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("archive.org returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (i *InternetArchiveScraper) downloadURL(identifier, filename string) string {
	identifier = strings.TrimSpace(identifier)
	filename = strings.TrimSpace(filename)
	if identifier == "" || filename == "" {
		return ""
	}
	escapedParts := make([]string, 0, len(strings.Split(filename, "/")))
	for _, part := range strings.Split(filename, "/") {
		if part = strings.TrimSpace(part); part != "" {
			escapedParts = append(escapedParts, url.PathEscape(part))
		}
	}
	if len(escapedParts) == 0 {
		return ""
	}
	return strings.TrimRight(i.baseURL, "/") + "/download/" + url.PathEscape(identifier) + "/" + strings.Join(escapedParts, "/")
}

type internetArchiveSearchResponse struct {
	Response struct {
		Docs []internetArchiveDoc `json:"docs"`
	} `json:"response"`
}

type internetArchiveDoc struct {
	Identifier string              `json:"identifier"`
	Title      internetArchiveText `json:"title"`
	Year       internetArchiveText `json:"year"`
	Date       internetArchiveText `json:"date"`
	LicenseURL internetArchiveText `json:"licenseurl"`
	Collection internetArchiveList `json:"collection"`
}

type internetArchiveMetadataResponse struct {
	Metadata internetArchiveItemMetadata `json:"metadata"`
	Files    []internetArchiveFile       `json:"files"`
}

type internetArchiveItemMetadata struct {
	Title       internetArchiveText `json:"title"`
	Year        internetArchiveText `json:"year"`
	Date        internetArchiveText `json:"date"`
	Runtime     internetArchiveText `json:"runtime"`
	Description internetArchiveText `json:"description"`
	LicenseURL  internetArchiveText `json:"licenseurl"`
	Collection  internetArchiveList `json:"collection"`
}

func (m internetArchiveItemMetadata) CollectionValues() []string {
	return m.Collection.Values()
}

type internetArchiveFile struct {
	Name   internetArchiveText `json:"name"`
	Title  internetArchiveText `json:"title"`
	Format internetArchiveText `json:"format"`
	Source internetArchiveText `json:"source"`
	Size   internetArchiveText `json:"size"`
}

type internetArchiveText string

func (t *internetArchiveText) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = internetArchiveText(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*t = internetArchiveText(n.String())
		return nil
	}
	return nil
}

func (t internetArchiveText) String() string {
	return strings.TrimSpace(string(t))
}

type internetArchiveList []string

func (l *internetArchiveList) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if strings.TrimSpace(single) != "" {
			*l = []string{single}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*l = many
		return nil
	}
	return nil
}

func (l internetArchiveList) Values() []string {
	values := make([]string, 0, len(l))
	for _, v := range l {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	return values
}

func buildInternetArchiveQuery(req SearchRequest) string {
	title := strings.TrimSpace(req.Parsed.Title)
	if title == "" {
		title = strings.TrimSpace(req.Query)
	}
	terms := []string{fmt.Sprintf(`title:(%s)`, quoteArchiveQuery(title))}
	if req.Parsed.Year > 0 && req.Parsed.MediaType == MediaTypeMovie {
		terms = append(terms, fmt.Sprintf(`year:%d`, req.Parsed.Year))
	}
	return fmt.Sprintf("mediatype:movies AND (%s)", strings.Join(terms, " AND "))
}

func quoteArchiveQuery(value string) string {
	words := strings.Fields(strings.TrimSpace(value))
	if len(words) == 0 {
		return `""`
	}
	escaped := strings.ReplaceAll(strings.Join(words, " "), `"`, `\"`)
	return `"` + escaped + `"`
}

func selectInternetArchiveVideoFiles(files []internetArchiveFile, req SearchRequest, limit int) []internetArchiveFile {
	candidates := make([]internetArchiveFile, 0, len(files))
	for _, file := range files {
		if isInternetArchivePlayableVideo(file) {
			candidates = append(candidates, file)
		}
	}
	if hasPreferredInternetArchiveVideo(candidates) {
		preferred := candidates[:0]
		for _, file := range candidates {
			if !isLowBandwidthInternetArchiveDerivative(file) {
				preferred = append(preferred, file)
			}
		}
		candidates = preferred
	}
	if hasModernInternetArchiveVideo(candidates) {
		modern := candidates[:0]
		for _, file := range candidates {
			if isModernInternetArchiveVideo(file) {
				modern = append(modern, file)
			}
		}
		candidates = modern
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		return internetArchiveFileScore(candidates[a], req) > internetArchiveFileScore(candidates[b], req)
	})

	if req.Parsed.Season > 0 && req.Parsed.Episode > 0 {
		code := mediaresolve.EpisodeCode{Season: req.Parsed.Season, Episode: req.Parsed.Episode}
		var matching []internetArchiveFile
		for _, file := range candidates {
			if mediaresolve.CandidateMatchesEpisode(file.Name.String()+" "+file.Title.String(), code) {
				matching = append(matching, file)
			}
		}
		if len(matching) > 0 {
			candidates = matching
		}
	}

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func hasPreferredInternetArchiveVideo(files []internetArchiveFile) bool {
	for _, file := range files {
		if !isLowBandwidthInternetArchiveDerivative(file) {
			return true
		}
	}
	return false
}

func hasModernInternetArchiveVideo(files []internetArchiveFile) bool {
	for _, file := range files {
		if isModernInternetArchiveVideo(file) {
			return true
		}
	}
	return false
}

func isModernInternetArchiveVideo(file internetArchiveFile) bool {
	switch strings.ToLower(path.Ext(file.Name.String())) {
	case ".mp4", ".m4v", ".webm", ".mkv", ".mov":
		return true
	default:
		return false
	}
}

func isLowBandwidthInternetArchiveDerivative(file internetArchiveFile) bool {
	name := strings.ToLower(file.Name.String())
	format := strings.ToLower(file.Format.String())
	return strings.Contains(name, "512kb") || strings.Contains(format, "512kb")
}

func isInternetArchivePlayableVideo(file internetArchiveFile) bool {
	name := strings.ToLower(strings.TrimSpace(file.Name.String()))
	if name == "" || strings.Contains(name, "_thumbs/") {
		return false
	}
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".mp4", ".m4v", ".mov", ".mkv", ".webm", ".ogv", ".avi", ".mpg", ".mpeg":
		return true
	}
	format := strings.ToLower(strings.TrimSpace(file.Format.String()))
	switch format {
	case "h.264", "mpeg4", "mpeg2", "matroska", "quicktime", "webm", "ogg video":
		return true
	default:
		return false
	}
}

func internetArchiveFileScore(file internetArchiveFile, req SearchRequest) int {
	score := 0
	name := strings.ToLower(file.Name.String() + " " + file.Title.String() + " " + file.Format.String())
	switch strings.ToLower(path.Ext(file.Name.String())) {
	case ".mp4", ".m4v":
		score += 50
	case ".mkv", ".webm", ".mov":
		score += 30
	case ".avi", ".mpg", ".mpeg", ".ogv":
		score += 10
	}
	if strings.Contains(name, "h.264") || strings.Contains(name, "mpeg4") {
		score += 20
	}
	if strings.Contains(name, "1080") {
		score += 12
	} else if strings.Contains(name, "720") {
		score += 10
	} else if strings.Contains(name, "480") {
		score += 6
	}
	if strings.Contains(name, "512kb") || strings.Contains(name, "thumbnail") || strings.Contains(name, "sample") {
		score -= 25
	}
	if req.Parsed.Season > 0 && req.Parsed.Episode > 0 &&
		mediaresolve.CandidateMatchesEpisode(file.Name.String()+" "+file.Title.String(), mediaresolve.EpisodeCode{Season: req.Parsed.Season, Episode: req.Parsed.Episode}) {
		score += 80
	}
	return score
}

func internetArchiveResultTitle(req SearchRequest, metadata internetArchiveItemMetadata, docTitle, fileTitle, fileName string) (string, bool) {
	title := firstNonEmpty(fileTitle, fileName, metadata.Title.String(), docTitle)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Internet Archive video"
	}
	if req.Parsed.MediaType != MediaTypeSeries || req.Parsed.Season <= 0 || req.Parsed.Episode <= 0 {
		return title, false
	}

	code := mediaresolve.EpisodeCode{Season: req.Parsed.Season, Episode: req.Parsed.Episode}
	if mediaresolve.CandidateMatchesEpisode(title+" "+fileName, code) {
		return title, false
	}

	text := strings.Join([]string{
		metadata.Title.String(),
		docTitle,
		fileTitle,
		fileName,
		metadata.Description.String(),
	}, " ")
	season, episode, ok := parseInternetArchiveEpisodeText(text)
	if !ok || season != req.Parsed.Season || episode != req.Parsed.Episode {
		return title, false
	}

	showTitle := strings.TrimSpace(req.Parsed.Title)
	if showTitle == "" {
		showTitle = strings.TrimSpace(req.Query)
	}
	if showTitle == "" {
		showTitle = "Internet Archive video"
	}
	return fmt.Sprintf("%s S%02dE%02d - %s", showTitle, season, episode, title), true
}

var (
	internetArchiveSeasonEpisodeRE = regexp.MustCompile(`(?i)\bseason\s*#?\s*(\d{1,3})\b.{0,60}\bepisode\s*#?\s*(\d{1,4})\b`)
	internetArchiveEpisodeSeasonRE = regexp.MustCompile(`(?i)\bepisode\s*#?\s*(\d{1,4})\b.{0,60}\bseason\s*#?\s*(\d{1,3})\b`)
)

func parseInternetArchiveEpisodeText(text string) (int, int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, false
	}
	if match := internetArchiveSeasonEpisodeRE.FindStringSubmatch(text); len(match) == 3 {
		season, seasonErr := strconv.Atoi(match[1])
		episode, episodeErr := strconv.Atoi(match[2])
		return season, episode, seasonErr == nil && episodeErr == nil && season > 0 && episode > 0
	}
	if match := internetArchiveEpisodeSeasonRE.FindStringSubmatch(text); len(match) == 3 {
		episode, episodeErr := strconv.Atoi(match[1])
		season, seasonErr := strconv.Atoi(match[2])
		return season, episode, seasonErr == nil && episodeErr == nil && season > 0 && episode > 0
	}
	return 0, 0, false
}

func parseArchiveSize(size string) int64 {
	if size == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func addArchiveAttr(attrs map[string]string, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		attrs[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parsePositiveConfigInt(cfg map[string]string, key string, fallback int) int {
	if cfg == nil {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(cfg[key]))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

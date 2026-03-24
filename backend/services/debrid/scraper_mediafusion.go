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
	mediafusionDefaultBaseURL = "https://mediafusion.elfhosted.com"
)

// MediaFusionScraper queries MediaFusion for releases using the Stremio addon API.
// Recent MediaFusion builds prefer /playback/... while older instances still serve /stream/...
type MediaFusionScraper struct {
	name       string
	baseURL    string
	httpClient *http.Client
}

// NewMediaFusionScraper constructs a scraper with sane defaults.
// The baseURL may be a plain host or a full addon URL including the config path.
func NewMediaFusionScraper(client *http.Client, baseURL, name string) *MediaFusionScraper {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = mediafusionDefaultBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/manifest.json")
	return &MediaFusionScraper{
		name:       strings.TrimSpace(name),
		baseURL:    baseURL,
		httpClient: client,
	}
}

func (m *MediaFusionScraper) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mediafusion"
}

func (m *MediaFusionScraper) Search(ctx context.Context, req SearchRequest) ([]ScrapeResult, error) {
	imdbID := strings.TrimSpace(req.IMDBID)
	if imdbID == "" {
		log.Printf("[mediafusion] No IMDB ID provided, skipping search")
		return nil, nil
	}

	imdbID = strings.ToLower(imdbID)
	if !strings.HasPrefix(imdbID, "tt") {
		imdbID = "tt" + imdbID
	}

	log.Printf("[mediafusion] Search called with IMDBID=%q, Season=%d, Episode=%d, MediaType=%s, IsDaily=%v, TargetAirDate=%q",
		imdbID, req.Parsed.Season, req.Parsed.Episode, req.Parsed.MediaType, req.IsDaily, req.TargetAirDate)

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

		episodesToSearch := []int{req.Parsed.Episode}
		isDailySearch := req.IsDaily && mediaType == MediaTypeSeries && req.Parsed.Season > 0 && req.Parsed.Episode > 0 && req.TargetAirDate != ""
		if isDailySearch {
			log.Printf("[mediafusion] Daily show detected, will try E%d, E%d, E%d for target date %s",
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

			streams, err := m.fetchStreams(ctx, stremioType, streamID)
			if err != nil {
				errs = append(errs, fmt.Errorf("mediafusion %s %s: %w", stremioType, streamID, err))
				continue
			}

			var batchResults []ScrapeResult
			foundCorrectDate := false

			for _, stream := range streams {
				var guid string
				switch {
				case stream.infoHash != "":
					guid = fmt.Sprintf("%s:%s:%d", m.Name(), strings.ToLower(stream.infoHash), stream.fileIdx)
				case stream.url != "":
					guid = fmt.Sprintf("%s:url:%s", m.Name(), stream.url)
				default:
					continue
				}
				if _, exists := seen[guid]; exists {
					continue
				}
				seen[guid] = struct{}{}

				result := ScrapeResult{
					Title:       stream.titleText,
					Indexer:     m.Name(),
					InfoHash:    stream.infoHash,
					FileIndex:   stream.fileIdx,
					SizeBytes:   stream.sizeBytes,
					Seeders:     stream.seeders,
					Provider:    stream.provider,
					Languages:   stream.languages,
					Resolution:  stream.resolution,
					MetaName:    req.Parsed.Title,
					MetaID:      imdbID,
					Source:      m.Name(),
					Attributes:  stream.attributes(),
					ServiceType: models.ServiceTypeDebrid,
				}
				if stream.infoHash != "" {
					result.Magnet = buildMagnet(stream.infoHash, stream.trackers)
				}
				if stream.url != "" {
					result.TorrentURL = stream.url
				}

				if isDailySearch {
					if mediaresolve.CandidateMatchesDailyDate(stream.titleText, req.TargetAirDate, 0) {
						foundCorrectDate = true
						batchResults = append(batchResults, result)
					}
				} else {
					batchResults = append(batchResults, result)
				}
			}

			results = append(results, batchResults...)

			if isDailySearch && foundCorrectDate {
				log.Printf("[mediafusion] Found %d results matching target date %s at episode %d, stopping search",
					len(batchResults), req.TargetAirDate, episode)
				break
			}

			if req.MaxResults > 0 && len(results) >= req.MaxResults {
				return results[:req.MaxResults], nil
			}
		}
	}

	if req.MaxResults > 0 && len(results) > req.MaxResults {
		results = results[:req.MaxResults]
	}

	if len(results) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	log.Printf("[mediafusion] Found %d streams for %s", len(results), imdbID)
	return results, nil
}

type mediafusionResponse struct {
	Streams []struct {
		Name          string                 `json:"name"`
		Title         string                 `json:"title"`
		Description   string                 `json:"description"`
		InfoHash      string                 `json:"infoHash"`
		FileIdx       *int                   `json:"fileIdx"`
		URL           string                 `json:"url"`
		ExternalURL   string                 `json:"externalUrl"`
		Size          interface{}            `json:"size"`
		Seeders       interface{}            `json:"seeders"`
		Tracker       interface{}            `json:"tracker"`
		BehaviorHints map[string]interface{} `json:"behaviorHints"`
	} `json:"streams"`
}

type mediafusionStream struct {
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
	url        string
}

func (s mediafusionStream) attributes() map[string]string {
	attrs := map[string]string{
		"scraper":   "mediafusion",
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
		attrs["raw_name"] = s.name
	}
	if len(s.languages) > 0 {
		attrs["languages"] = strings.Join(s.languages, ",")
	}
	if s.url != "" {
		attrs["stream_url"] = s.url
		attrs["preresolved"] = "true"
	}
	return attrs
}

func (m *MediaFusionScraper) fetchStreams(ctx context.Context, mediaType, id string) ([]mediafusionStream, error) {
	if id == "" {
		return nil, fmt.Errorf("empty stream id")
	}

	payload, err := m.fetchAddonPayload(ctx, "playback", mediaType, id)
	if err != nil {
		if !isNotFoundHTTPError(err) {
			return nil, err
		}
		log.Printf("[mediafusion] playback endpoint unavailable for %s, falling back to stream endpoint", id)
		payload, err = m.fetchAddonPayload(ctx, "stream", mediaType, id)
		if err != nil {
			return nil, err
		}
	}

	streams := make([]mediafusionStream, 0, len(payload.Streams))
	for _, stream := range payload.Streams {
		infoHash := strings.ToLower(strings.TrimSpace(stream.InfoHash))
		streamURL := strings.TrimSpace(stream.URL)
		if streamURL == "" {
			streamURL = strings.TrimSpace(stream.ExternalURL)
		}
		if infoHash == "" && streamURL == "" {
			continue
		}
		if IsKnownPlaceholderURL(streamURL) {
			continue
		}

		name := strings.TrimSpace(stream.Name)
		rawTitle := strings.TrimSpace(stream.Title)
		if rawTitle == "" {
			rawTitle = strings.TrimSpace(stream.Description)
		}

		filename := ""
		if stream.BehaviorHints != nil {
			if fn, ok := stream.BehaviorHints["filename"].(string); ok {
				filename = strings.TrimSpace(fn)
			}
		}
		if filename == "" && streamURL != "" {
			filename = extractFilenameFromURL(streamURL)
		}

		if hasIncompatibleAudioCodec(name, rawTitle) || hasIncompatibleAudioCodec(filename, "") {
			continue
		}

		fileIdx := 0
		if stream.FileIdx != nil {
			fileIdx = *stream.FileIdx
		}

		titleText := filename
		if titleText == "" {
			titleText = deriveTitle(rawTitle)
		}

		sizeBytes := parseSize(rawTitle)
		if sizeBytes == 0 && stream.BehaviorHints != nil {
			if vs, ok := stream.BehaviorHints["videoSize"].(float64); ok && vs > 0 {
				sizeBytes = int64(vs)
			}
		}
		if sizeBytes == 0 {
			sizeBytes = parseSizeFromInterface(stream.Size)
		}

		seeders := parseInt(stream.Seeders, rawTitle)
		provider := parseProvider(rawTitle)
		if provider == "" && stream.Tracker != nil {
			if val := strings.TrimSpace(fmt.Sprint(stream.Tracker)); val != "" {
				provider = val
			}
		}
		languages := parseLanguages(rawTitle)
		resolution := detectResolution(name, rawTitle)
		trackers := parseTrackers(stream.BehaviorHints)

		streams = append(streams, mediafusionStream{
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

func (m *MediaFusionScraper) fetchAddonPayload(ctx context.Context, resource, mediaType, id string) (*mediafusionResponse, error) {
	endpoint := fmt.Sprintf("%s/%s/%s/%s.json", m.baseURL, resource, mediaType, url.PathEscape(id))
	log.Printf("[mediafusion] Fetching: %s", endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addBrowserHeaders(req)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, addonHTTPError{
			resource: resource,
			id:       id,
			status:   resp.StatusCode,
			body:     strings.TrimSpace(string(body)),
		}
	}

	var payload mediafusionResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode mediafusion response: %w", err)
	}

	return &payload, nil
}

type addonHTTPError struct {
	resource string
	id       string
	status   int
	body     string
}

func (e addonHTTPError) Error() string {
	return fmt.Sprintf("mediafusion %s %s returned %d: %s", e.resource, e.id, e.status, e.body)
}

func isNotFoundHTTPError(err error) bool {
	var httpErr addonHTTPError
	return errors.As(err, &httpErr) && httpErr.status == http.StatusNotFound
}

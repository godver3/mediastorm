package peartube

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"novastream/models"
)

// IndexerName is what a p2p result reports as its origin. The stream picker
// shows it verbatim.
const IndexerName = "PearTube"

// ProviderName is the `provider` attribute every p2p result carries, mirroring
// how debrid results name their resolver.
const ProviderName = "peartube"

// A catalog entity id encodes the TMDB coordinates the publisher used. Both
// forms the relay produces are matched by scanning rather than anchoring, so a
// namespaced id (`tmdb:movie:603`, `tmdb:episode:show:1399:s1:e1`) and a bare
// one (`movie:603`, `show:1399:s1:e1`) both resolve to the same coordinates.
var (
	movieEntityID   = regexp.MustCompile(`(?i)\bmovie:([1-9][0-9]{0,9})\b`)
	episodeEntityID = regexp.MustCompile(`(?i)\bshow:([1-9][0-9]{0,9}):s([0-9]{1,4}):e([0-9]{1,5})\b`)
)

// entityCoordinates are the TMDB coordinates recovered from a catalog entity id.
type entityCoordinates struct {
	TMDBID  string
	Season  int
	Episode int
	Kind    string // "movie" or "episode"
}

func parseEntityCoordinates(entityID string) (entityCoordinates, bool) {
	if match := episodeEntityID.FindStringSubmatch(entityID); match != nil {
		season, _ := strconv.Atoi(match[2])
		episode, _ := strconv.Atoi(match[3])
		if season > 0 && episode > 0 {
			return entityCoordinates{TMDBID: match[1], Season: season, Episode: episode, Kind: "episode"}, true
		}
	}
	if match := movieEntityID.FindStringSubmatch(entityID); match != nil {
		return entityCoordinates{TMDBID: match[1], Kind: "movie"}, true
	}
	return entityCoordinates{}, false
}

func sourceCoordinates(source CatalogSource) (entityCoordinates, bool) {
	if !strings.EqualFold(strings.TrimSpace(source.MediaProvider), "tmdb") {
		return entityCoordinates{}, false
	}
	id := strings.TrimSpace(source.MediaID)
	if id == "" {
		return entityCoordinates{}, false
	}
	switch source.ContentKind {
	case "movie":
		return entityCoordinates{TMDBID: id, Kind: "movie"}, true
	case "episode":
		if source.SeasonNumber > 0 && source.EpisodeNumber > 0 {
			return entityCoordinates{
				TMDBID: id, Season: source.SeasonNumber, Episode: source.EpisodeNumber, Kind: "episode",
			}, true
		}
	}
	return entityCoordinates{}, false
}

func coordinatesForSource(entity CatalogEntity, source CatalogSource) (entityCoordinates, bool) {
	if coordinates, ok := sourceCoordinates(source); ok {
		return coordinates, true
	}
	// Backward compatibility for relay catalogs produced before source
	// coordinates were explicit.
	return parseEntityCoordinates(entity.EntityID)
}

// SearchRequest is the exact selector or title fallback sent to companion v2.
type SearchRequest struct {
	Title      string
	Year       int
	Season     int
	Episode    int
	MediaType  string // "movie" or "series"
	TMDBID     string
	IMDBID     string
	MaxResults int
}

func (r SearchRequest) wantsEpisode() bool {
	return r.Season > 0 && r.Episode > 0
}

func (r SearchRequest) companionSearchTarget() (string, int, error) {
	kind := "movie"
	switch {
	case r.wantsEpisode():
		kind = "episode"
	case r.Season != 0 || r.Episode != 0:
		return "", 0, errors.New("companion episode search requires positive season and episode")
	case strings.EqualFold(strings.TrimSpace(r.MediaType), "series"):
		return "", 0, errors.New("companion series search requires exact episode coordinates")
	}

	values := url.Values{"kind": {kind}}
	namespace := ""
	identifier := strings.TrimSpace(r.TMDBID)
	if identifier != "" {
		namespace = "tmdb"
	} else if identifier = strings.TrimSpace(r.IMDBID); identifier != "" {
		namespace = "imdb"
	}
	if identifier != "" {
		if err := validateSearchText(identifier, "identifier", 512); err != nil {
			return "", 0, err
		}
		values.Set("namespace", namespace)
		values.Set("identifier", identifier)
	} else {
		title := strings.TrimSpace(r.Title)
		if err := validateSearchText(title, "title", 256); err != nil {
			return "", 0, err
		}
		values.Set("title", title)
		if r.Year < 0 || r.Year > 9999 {
			return "", 0, errors.New("companion search year is invalid")
		}
		if r.Year > 0 {
			values.Set("year", strconv.Itoa(r.Year))
		}
	}
	if kind == "episode" {
		values.Set("season", strconv.Itoa(r.Season))
		values.Set("episode", strconv.Itoa(r.Episode))
	}

	limit := companionDefaultSearchLimit
	if r.MaxResults > 0 {
		limit = min(r.MaxResults, companionMaxCandidates)
		values.Set("limit", strconv.Itoa(limit))
	}
	return companionAPIPrefix + "/search?" + encodeCompanionQuery(values), limit, nil
}

// encodeCompanionQuery matches WHATWG URLSearchParams serialization used by
// companion auth: sorted decoded entries, form-style spaces, '*' preserved,
// and '~' percent-encoded.
func encodeCompanionQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var encoded strings.Builder
	first := true
	for _, key := range keys {
		sortedValues := append([]string(nil), values[key]...)
		sort.Strings(sortedValues)
		for _, value := range sortedValues {
			if !first {
				encoded.WriteByte('&')
			}
			first = false
			writeCompanionFormValue(&encoded, key)
			encoded.WriteByte('=')
			writeCompanionFormValue(&encoded, value)
		}
	}
	return encoded.String()
}

func writeCompanionFormValue(encoded *strings.Builder, value string) {
	const hexadecimal = "0123456789ABCDEF"
	for i := range len(value) {
		character := value[i]
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '*', character == '-', character == '.', character == '_':
			encoded.WriteByte(character)
		case character == ' ':
			encoded.WriteByte('+')
		default:
			encoded.WriteByte('%')
			encoded.WriteByte(hexadecimal[character>>4])
			encoded.WriteByte(hexadecimal[character&0x0f])
		}
	}
}

func validateSearchText(value, name string, maximum int) error {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return fmt.Errorf("companion search %s is invalid", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("companion search %s is invalid", name)
		}
	}
	return nil
}

// MapCandidates deterministically maps companion facts into rankable results.
// Playback locators remain absent until the selected candidate is opened.
func MapCandidates(request SearchRequest, candidates []CompanionCandidateV2) []models.NZBResult {
	results := make([]models.NZBResult, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, mapCandidate(request, candidate))
	}
	return results
}

func mapCandidate(request SearchRequest, candidate CompanionCandidateV2) models.NZBResult {
	title := strings.TrimSpace(request.Title)
	releaseYear := uint64(0)
	if candidate.Work != nil {
		if candidateTitle := strings.TrimSpace(candidate.Work.Title); candidateTitle != "" {
			title = candidateTitle
		}
		if candidate.Work.ReleaseYear != nil {
			releaseYear = *candidate.Work.ReleaseYear
		}
	}
	if title == "" {
		title = "PearTube candidate"
	}

	season, episode := request.Season, request.Episode
	if candidate.Work != nil && candidate.Work.Episode != nil {
		if candidate.Work.Episode.SeasonNumber != nil && *candidate.Work.Episode.SeasonNumber > 0 {
			season = int(*candidate.Work.Episode.SeasonNumber)
		}
		if candidate.Work.Episode.EpisodeNumber != nil && *candidate.Work.Episode.EpisodeNumber > 0 {
			episode = int(*candidate.Work.Episode.EpisodeNumber)
		}
	}
	if season > 0 && episode > 0 {
		title = fmt.Sprintf("%s S%02dE%02d", title, season, episode)
	} else if releaseYear > 0 {
		title = fmt.Sprintf("%s %d", title, releaseYear)
	}
	title += " [PearTube]"

	attributes := map[string]string{
		"provider":               ProviderName,
		"peartube_candidate_ref": candidate.CandidateRef,
	}
	var sizeBytes int64
	if candidate.Rendition != nil {
		setCandidateAttribute(attributes, "container", candidate.Rendition.Container)
		setCandidateAttribute(attributes, "videoCodec", candidate.Rendition.VideoCodec)
		setCandidateAttribute(attributes, "resolution", candidate.Rendition.ResolutionLabel)
		setCandidateAttribute(attributes, "purpose", candidate.Rendition.Purpose)
		if len(candidate.Rendition.HDRFormats) > 0 {
			attributes["hdrFormats"] = strings.Join(candidate.Rendition.HDRFormats, ",")
		}
		setCandidateUintAttribute(attributes, "width", candidate.Rendition.Width)
		setCandidateUintAttribute(attributes, "height", candidate.Rendition.Height)
		setCandidateUintAttribute(attributes, "byteLength", candidate.Rendition.ByteLength)
		if candidate.Rendition.ByteLength != nil {
			sizeBytes = int64(*candidate.Rendition.ByteLength)
		}
	}
	if sizeBytes == 0 && candidate.Asset != nil && candidate.Asset.ByteLength != nil {
		sizeBytes = int64(*candidate.Asset.ByteLength)
	}
	if candidate.Availability != nil {
		setCandidateUintAttribute(attributes, "peers", candidate.Availability.Peers)
		setCandidateUintAttribute(attributes, "seeders", candidate.Availability.CompleteSeeders)
		setCandidateUintAttribute(attributes, "availabilityObservedAtMs", candidate.Availability.ObservedAtMS)
		setCandidateUintAttribute(attributes, "availabilityExpiresAtMs", candidate.Availability.ExpiresAtMS)
	}

	return models.NZBResult{
		Title:       title,
		Indexer:     IndexerName,
		GUID:        "peartube:candidate:" + candidate.CandidateRef,
		SizeBytes:   sizeBytes,
		ServiceType: models.ServiceTypePearTube,
		Attributes:  attributes,
	}
}

func setCandidateAttribute(attributes map[string]string, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		attributes[name] = value
	}
}

func setCandidateUintAttribute(attributes map[string]string, name string, value *uint64) {
	if value != nil {
		attributes[name] = strconv.FormatUint(*value, 10)
	}
}

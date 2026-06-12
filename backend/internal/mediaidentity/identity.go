package mediaidentity

import (
	"fmt"
	"regexp"
	"strings"
)

var episodeSuffixPattern = regexp.MustCompile(`(?i):s(\d{1,3})e(\d{1,4})$`)

var urlTokenQueryPattern = regexp.MustCompile(`(?i)([?&])token=[^&]*`)

// SanitizeID strips artifacts that must never become part of a stored media ID:
// redundant media-type key prefixes (a caller passing a storage key like
// "movie:tvdb:movie:10702" as the ID) and access tokens embedded in URL-shaped
// IDs (live-TV/recording stream URLs carrying session tokens).
func SanitizeID(id string) string {
	id = strings.TrimSpace(id)
	id = stripMediaTypeKeyPrefixes(id)
	return stripURLAccessToken(id)
}

func stripMediaTypeKeyPrefixes(id string) string {
	for {
		lower := strings.ToLower(id)
		var rest string
		switch {
		case strings.HasPrefix(lower, "movie:"):
			rest = id[len("movie:"):]
		case strings.HasPrefix(lower, "episode:"):
			rest = id[len("episode:"):]
		case strings.HasPrefix(lower, "series:"):
			rest = id[len("series:"):]
		default:
			return id
		}
		if strings.TrimSpace(rest) == "" {
			return id
		}
		id = rest
	}
}

func stripURLAccessToken(id string) string {
	lower := strings.ToLower(id)
	if !strings.Contains(lower, "token=") {
		return id
	}
	if !strings.Contains(lower, "://") && !strings.HasPrefix(lower, "/") {
		return id
	}
	id = urlTokenQueryPattern.ReplaceAllString(id, "$1")
	id = strings.ReplaceAll(id, "?&", "?")
	id = strings.ReplaceAll(id, "&&", "&")
	return strings.TrimRight(id, "?&")
}

// Input is the provider-agnostic shape used to resolve media identity before
// services decide which DB row to read, update, merge, or delete.
type Input struct {
	MediaType     string
	ID            string
	SeriesID      string
	SeasonNumber  int
	EpisodeNumber int
	ExternalIDs   map[string]string
}

// Identity is the canonical item plus equivalent keys/aliases that may already
// exist in storage from older providers or sync integrations.
type Identity struct {
	MediaType     string
	ID            string
	Key           string
	SeriesID      string
	SeasonNumber  int
	EpisodeNumber int
	ExternalIDs   map[string]string
	CandidateIDs  []string
	CandidateKeys []string
	Tokens        map[string]struct{}
}

// Resolve normalizes an item into one canonical storage identity and a set of
// equivalent aliases. It is intentionally pure: no metadata/network lookup.
func Resolve(input Input) Identity {
	mediaType := NormalizeMediaType(input.MediaType)
	ids := NormalizeExternalIDs(input.ExternalIDs)
	itemID := SanitizeID(input.ID)
	originalItemID := normalizeID(itemID)
	seriesID := SanitizeID(input.SeriesID)
	originalSeriesID := normalizeID(seriesID)
	seasonNumber := input.SeasonNumber
	episodeNumber := input.EpisodeNumber
	identityIDs := ids

	if mediaType == "episode" {
		if parsedSeries, s, e, ok := ParseEpisodeID(itemID); ok {
			if seriesID == "" {
				seriesID = parsedSeries
				if preferredSeriesID := preferredSeriesIDFromTitleID(parsedSeries, ids); preferredSeriesID != "" {
					seriesID = preferredSeriesID
				}
			}
			if seasonNumber <= 0 {
				seasonNumber = s
			}
			if episodeNumber <= 0 {
				episodeNumber = e
			}
		}

		identityIDs = IdentityExternalIDs(mediaType, itemID, seriesID, ids)
		seriesID = CanonicalSeriesID(seriesID, identityIDs)
		if seriesID != "" && seasonNumber >= 0 && episodeNumber > 0 {
			itemID = EpisodeID(seriesID, seasonNumber, episodeNumber)
		} else {
			itemID = normalizeID(itemID)
		}
	} else {
		itemID = CanonicalTitleID(mediaType, itemID, ids)
	}

	identity := Identity{
		MediaType:     mediaType,
		ID:            itemID,
		Key:           Key(mediaType, itemID),
		SeriesID:      seriesID,
		SeasonNumber:  seasonNumber,
		EpisodeNumber: episodeNumber,
		ExternalIDs:   ids,
	}
	identity.CandidateIDs = CandidateIDs(mediaType, itemID, seriesID, seasonNumber, episodeNumber, identityIDs)
	if originalItemID != "" && originalItemID != itemID {
		identity.CandidateIDs = appendUniqueID(identity.CandidateIDs, originalItemID)
	}
	if mediaType == "episode" && originalSeriesID != "" && originalSeriesID != identity.SeriesID && identity.SeasonNumber >= 0 && identity.EpisodeNumber > 0 {
		identity.CandidateIDs = appendUniqueID(identity.CandidateIDs, EpisodeID(originalSeriesID, identity.SeasonNumber, identity.EpisodeNumber))
	}
	identity.CandidateKeys = CandidateKeys(mediaType, identity.CandidateIDs)
	identity.Tokens = IdentityTokens(mediaType, itemID, seriesID, seasonNumber, episodeNumber, identityIDs)
	return identity
}

// IndexKeys returns CandidateKeys minus keys whose ID portion is an
// unprefixed numeric value. Bare numeric IDs are kept in CandidateKeys so
// legacy rows stored under them can still be matched, but they are ambiguous
// across providers (tvdb and tmdb numbering overlap), so they must not be used
// as shared index keys where unrelated titles could collide.
func (i Identity) IndexKeys() []string {
	keys := make([]string, 0, len(i.CandidateKeys))
	for _, key := range i.CandidateKeys {
		if isAmbiguousCandidateKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

func isAmbiguousCandidateKey(key string) bool {
	idx := strings.Index(key, ":")
	if idx < 0 {
		return isAllDigits(key)
	}
	return isAllDigits(key[idx+1:])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func appendUniqueID(ids []string, value string) []string {
	value = normalizeID(value)
	if value == "" {
		return ids
	}
	for _, existing := range ids {
		if existing == value {
			return ids
		}
	}
	return append(ids, value)
}

func preferredSeriesIDFromTitleID(parsedSeriesID string, externalIDs map[string]string) string {
	titleID := normalizeID(externalIDs["titleId"])
	if titleID == "" || parsedSeriesID == "" || sameSeriesID(titleID, parsedSeriesID) {
		return ""
	}
	if !HasContradictorySeriesExternalIDs(parsedSeriesID, "", externalIDs) {
		return ""
	}
	return titleID
}

func sameSeriesID(a, b string) bool {
	aProvider, aValue := SeriesProviderAndID(a)
	bProvider, bValue := SeriesProviderAndID(b)
	return aProvider != "" && aProvider == bProvider && aValue != "" && strings.EqualFold(aValue, bValue)
}

func NormalizeMediaType(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "show", "tv":
		return "series"
	default:
		return mediaType
	}
}

func NormalizeExternalIDs(ids map[string]string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]string, len(ids))
	for key, value := range ids {
		key = canonicalExternalIDKey(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if key == "imdb" || key == "episodeImdb" {
			value = strings.ToLower(value)
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func MergeExternalIDs(base, incoming map[string]string) map[string]string {
	base = NormalizeExternalIDs(base)
	incoming = NormalizeExternalIDs(incoming)
	if len(base) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(incoming))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range incoming {
		if strings.TrimSpace(out[key]) == "" && strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

func CanonicalTitleID(mediaType, id string, externalIDs map[string]string) string {
	mediaType = NormalizeMediaType(mediaType)
	id = normalizeID(id)
	externalIDs = NormalizeExternalIDs(externalIDs)
	switch mediaType {
	case "movie":
		if v := externalIDs["tmdb"]; v != "" {
			return "tmdb:movie:" + v
		}
		if hasProviderPrefix(id, "tmdb:movie:") {
			return id
		}
		if v := externalIDs["tvdb"]; v != "" {
			return "tvdb:movie:" + v
		}
		if hasProviderPrefix(id, "tvdb:movie:") {
			return id
		}
		if v := normalizeIMDB(externalIDs["imdb"]); v != "" {
			return v
		}
	case "series":
		return CanonicalSeriesID(id, externalIDs)
	}
	return normalizeID(id)
}

func CanonicalSeriesID(id string, externalIDs map[string]string) string {
	id = normalizeID(id)
	externalIDs = NormalizeExternalIDs(externalIDs)
	if v := externalIDs["tmdb"]; v != "" {
		return "tmdb:tv:" + v
	}
	if hasProviderPrefix(id, "tmdb:tv:") {
		return id
	}
	if v := externalIDs["tvdb"]; v != "" {
		return "tvdb:series:" + v
	}
	if hasProviderPrefix(id, "tvdb:series:") {
		return id
	}
	if v := normalizeIMDB(externalIDs["imdb"]); v != "" {
		return "imdb:" + v
	}
	return id
}

func EpisodeID(seriesID string, seasonNumber, episodeNumber int) string {
	seriesID = normalizeID(seriesID)
	if seriesID == "" || seasonNumber < 0 || episodeNumber < 0 {
		return seriesID
	}
	return fmt.Sprintf("%s:s%02de%02d", seriesID, seasonNumber, episodeNumber)
}

func ParseEpisodeID(id string) (seriesID string, seasonNumber, episodeNumber int, ok bool) {
	id = strings.TrimSpace(id)
	matches := episodeSuffixPattern.FindStringSubmatch(id)
	if len(matches) != 3 {
		return "", 0, 0, false
	}
	fmt.Sscanf(matches[1], "%d", &seasonNumber)
	fmt.Sscanf(matches[2], "%d", &episodeNumber)
	loc := episodeSuffixPattern.FindStringIndex(id)
	if loc == nil || loc[0] <= 0 {
		return "", 0, 0, false
	}
	return normalizeID(id[:loc[0]]), seasonNumber, episodeNumber, true
}

func Key(mediaType, id string) string {
	mediaType = NormalizeMediaType(mediaType)
	id = normalizeID(id)
	if mediaType == "" || id == "" {
		return ""
	}
	return mediaType + ":" + id
}

func CandidateKeys(mediaType string, candidateIDs []string) []string {
	keys := make([]string, 0, len(candidateIDs))
	seen := make(map[string]struct{}, len(candidateIDs))
	for _, id := range candidateIDs {
		key := Key(mediaType, id)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func CandidateIDs(mediaType, id, seriesID string, seasonNumber, episodeNumber int, externalIDs map[string]string) []string {
	mediaType = NormalizeMediaType(mediaType)
	externalIDs = NormalizeExternalIDs(externalIDs)
	ids := make([]string, 0, 10)
	seen := make(map[string]struct{}, 10)
	add := func(value string) {
		value = normalizeID(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}

	add(id)
	switch mediaType {
	case "movie":
		if v := externalIDs["tvdb"]; v != "" {
			add("tvdb:movie:" + v)
			add(v)
		}
		if v := externalIDs["tmdb"]; v != "" {
			add("tmdb:movie:" + v)
			add(v)
		}
		if v := normalizeIMDB(externalIDs["imdb"]); v != "" {
			add(v)
			add("imdb:" + v)
		}
	case "series":
		addSeriesCandidates(add, externalIDs)
	case "episode":
		seriesCandidates := CandidateIDs("series", seriesID, "", 0, 0, externalIDs)
		for _, candidateSeriesID := range seriesCandidates {
			if seasonNumber >= 0 && episodeNumber > 0 {
				add(EpisodeID(candidateSeriesID, seasonNumber, episodeNumber))
			}
		}
	}
	return ids
}

func IdentityTokens(mediaType, id, seriesID string, seasonNumber, episodeNumber int, externalIDs map[string]string) map[string]struct{} {
	mediaType = NormalizeMediaType(mediaType)
	externalIDs = NormalizeExternalIDs(externalIDs)
	tokens := make(map[string]struct{}, 12)
	add := func(kind, value string) {
		kind = strings.ToLower(strings.TrimSpace(kind))
		value = normalizeID(value)
		if kind == "" || value == "" {
			return
		}
		tokens[kind+":"+value] = struct{}{}
	}

	add("id", id)
	switch mediaType {
	case "movie", "series":
		for _, key := range []string{"tvdb", "tmdb", "imdb"} {
			if v := externalIDs[key]; v != "" {
				add("title:"+key, v)
			}
		}
	case "episode":
		for _, key := range []string{"episodeTvdb", "episodeTmdb", "episodeImdb", "episodeTrakt"} {
			if v := externalIDs[key]; v != "" {
				add("episode:"+key, v)
			}
		}
		for _, key := range []string{"tvdb", "tmdb", "imdb"} {
			if v := externalIDs[key]; v != "" && seasonNumber >= 0 && episodeNumber > 0 {
				add(fmt.Sprintf("series-episode:%s:s%02de%02d", key, seasonNumber, episodeNumber), v)
			}
		}
		if seriesID != "" && seasonNumber >= 0 && episodeNumber > 0 {
			add("series-episode:id", EpisodeID(seriesID, seasonNumber, episodeNumber))
		}
	}
	return tokens
}

func Equivalent(a, b Identity) bool {
	if a.MediaType != b.MediaType || len(a.Tokens) == 0 || len(b.Tokens) == 0 {
		return false
	}
	for token := range a.Tokens {
		if _, ok := b.Tokens[token]; ok {
			return true
		}
	}
	return false
}

func HasMatchingExternalID(a, b map[string]string) bool {
	a = NormalizeExternalIDs(a)
	b = NormalizeExternalIDs(b)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for key, value := range a {
		if value == "" || key == "titleId" {
			continue
		}
		if b[key] == value {
			return true
		}
	}
	return false
}

func addSeriesCandidates(add func(string), externalIDs map[string]string) {
	if v := externalIDs["tvdb"]; v != "" {
		add("tvdb:series:" + v)
		add(v)
	}
	if v := externalIDs["tmdb"]; v != "" {
		add("tmdb:tv:" + v)
		add(v)
	}
	if v := normalizeIMDB(externalIDs["imdb"]); v != "" {
		add("imdb:" + v)
		add(v)
	}
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func hasProviderPrefix(id string, prefixes ...string) bool {
	id = normalizeID(id)
	for _, prefix := range prefixes {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func normalizeIMDB(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	id = strings.TrimPrefix(id, "imdb:")
	return id
}

func canonicalExternalIDKey(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "imdb":
		return "imdb"
	case "tmdb":
		return "tmdb"
	case "tvdb":
		return "tvdb"
	case "episodetvdb":
		return "episodeTvdb"
	case "episodetmdb":
		return "episodeTmdb"
	case "episodeimdb":
		return "episodeImdb"
	case "episodetrakt":
		return "episodeTrakt"
	case "absoluteepisode":
		return "absoluteEpisode"
	case "titleid":
		return "titleId"
	default:
		return strings.ToLower(strings.TrimSpace(key))
	}
}

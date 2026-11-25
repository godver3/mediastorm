package debrid

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MediaType represents the content family inferred from a search query.
type MediaType string

const (
	MediaTypeUnknown MediaType = ""
	MediaTypeMovie   MediaType = "movie"
	MediaTypeSeries  MediaType = "series"
)

var (
	reYearSeasonEpisode  = regexp.MustCompile(`(?i)\bS((?:19|20)\d{2})E(\d{1,3})\b`)
	reSeasonEpisode      = regexp.MustCompile(`(?i)\bS?(\d{1,2})[xE](\d{1,3})\b`)
	reSeasonOnly         = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
	reSeasonEpisodeWords = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\s*(?:episode|ep)\s*(\d{1,3})\b`)
	reYear               = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	reEpisodeLabelCode   = regexp.MustCompile(`(?i)\bS\d{1,4}E\d{1,5}\b`)
	reDecorativeEpisode  = regexp.MustCompile(`(?i)[•·]\s*S\d{1,4}E\d{1,5}\b`)
	stopTokens           = map[string]struct{}{
		"1080p":  {},
		"2160p":  {},
		"720p":   {},
		"480p":   {},
		"4k":     {},
		"8k":     {},
		"hdr":    {},
		"web":    {},
		"webrip": {},
		"web-dl": {},
		"hdtv":   {},
		"bluray": {},
		"bdrip":  {},
		"brrip":  {},
		"dvdrip": {},
		"x264":   {},
		"x265":   {},
		"h264":   {},
		"h265":   {},
		"hevc":   {},
		"aac":    {},
		"ac3":    {},
		"truehd": {},
		"atmos":  {},
		"multi":  {},
		"remux":  {},
		"proper": {},
		"repack": {},
		"10bit":  {},
		"dv":     {},
		"hdr10":  {},
		"hdr10+": {},
		"dts":    {},
		"sdr":    {},
		"imax":   {},
		"dual":   {},
		"dubbed": {},
	}
)

// ParsedQuery captures normalized information extracted from a raw search string.
type ParsedQuery struct {
	Raw            string
	Title          string
	Season         int
	Episode        int
	Year           int
	MediaType      MediaType
	HasSeasonMatch bool
}

// KeepQueryYears as the catalog year leaves every year in the parsed title.
const KeepQueryYears = -1

// ParseQuery performs lightweight normalization and signal extraction from a raw search string.
// Callers that know the title's year should use ParseQueryWithYear.
func ParseQuery(raw string) ParsedQuery {
	return ParseQueryWithYear(raw, 0)
}

// ParseQueryWithYear parses a search string for a title released in catalogYear
// (0 when unknown). A year that does not fit the catalog year belongs to the
// title ("Blade Runner 2099", 2026) and stays in it.
func ParseQueryWithYear(raw string, catalogYear int) ParsedQuery {
	parsed := ParsedQuery{Raw: raw}

	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return parsed
	}

	lower := strings.ToLower(candidate)

	if match := reYearSeasonEpisode.FindStringSubmatch(lower); len(match) == 3 {
		if season, err := strconv.Atoi(match[1]); err == nil {
			parsed.Season = season
		}
		if episode, err := strconv.Atoi(match[2]); err == nil {
			parsed.Episode = episode
		}
		if parsed.Season > 0 && parsed.Episode > 0 {
			parsed.MediaType = MediaTypeSeries
			parsed.HasSeasonMatch = true
			candidate = removeSubstring(candidate, match[0])
		}
	} else if matches := reSeasonEpisode.FindAllStringSubmatch(lower, -1); len(matches) > 0 {
		match := matches[0]
		if len(matches) > 1 && reDecorativeEpisode.MatchString(candidate) {
			match = matches[len(matches)-1]
		}
		if season, err := strconv.Atoi(match[1]); err == nil {
			parsed.Season = season
		}
		if episode, err := strconv.Atoi(match[2]); err == nil {
			parsed.Episode = episode
		}
		if parsed.Season >= 0 && parsed.Episode > 0 {
			parsed.MediaType = MediaTypeSeries
			parsed.HasSeasonMatch = true
			candidate = removeSubstring(candidate, match[0])
		}
	} else if match := reSeasonEpisodeWords.FindStringSubmatch(lower); len(match) == 3 {
		if season, err := strconv.Atoi(match[1]); err == nil {
			parsed.Season = season
		}
		if episode, err := strconv.Atoi(match[2]); err == nil {
			parsed.Episode = episode
		}
		if parsed.Season >= 0 && parsed.Episode > 0 {
			parsed.MediaType = MediaTypeSeries
			parsed.HasSeasonMatch = true
			candidate = removeSubstring(candidate, match[0])
		}
	} else if match := reSeasonOnly.FindStringSubmatch(lower); len(match) == 2 {
		// Season-only query (e.g. "Show S01") — default episode to 1 so
		// Stremio scrapers can build a valid stream ID (imdbid:season:episode).
		if season, err := strconv.Atoi(match[1]); err == nil && season > 0 {
			parsed.Season = season
			parsed.Episode = 1
			parsed.MediaType = MediaTypeSeries
			parsed.HasSeasonMatch = true
			candidate = removeSubstring(candidate, match[0])
		}
	}

	if year, loc := releaseYearInQuery(candidate, catalogYear); year > 0 {
		parsed.Year = year
		candidate = strings.Join(strings.Fields(candidate[:loc[0]]+" "+candidate[loc[1]:]), " ")
	}

	if parsed.MediaType == MediaTypeSeries {
		candidate = stripDecorativeEpisodeLabel(candidate)
	}

	tokens := strings.Fields(candidate)
	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		normalized := strings.Trim(token, "-_.()[]{}")
		key := strings.ToLower(normalized)
		if key == "" {
			continue
		}
		if _, skip := stopTokens[key]; skip {
			continue
		}
		filtered = append(filtered, normalized)
	}

	parsed.Title = strings.TrimSpace(strings.Join(filtered, " "))
	if parsed.Title == "" {
		parsed.Title = strings.TrimSpace(raw)
	}

	if parsed.MediaType == MediaTypeUnknown && parsed.Title != "" {
		parsed.MediaType = inferMediaTypeFromTokens(tokens)
	}

	if parsed.MediaType == MediaTypeUnknown && parsed.Year > 0 {
		parsed.MediaType = MediaTypeMovie
	}

	return parsed
}

// releaseYearInQuery finds the last year in the query that marks the release
// rather than being part of the title, and its position. A year that starts the
// query is always title ("1923 S01E01", "2001 A Space Odyssey"); one in
// brackets after the title is a release marker whatever the catalog year.
func releaseYearInQuery(query string, catalogYear int) (int, []int) {
	if catalogYear == KeepQueryYears {
		return 0, nil
	}
	locs := reYear.FindAllStringIndex(query, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		if !strings.ContainsFunc(query[:loc[0]], func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		year, err := strconv.Atoi(query[loc[0]:loc[1]])
		if err != nil || year <= 1900 {
			continue
		}
		bracketed := loc[1] < len(query) && (query[loc[0]-1] == '(' && query[loc[1]] == ')' || query[loc[0]-1] == '[' && query[loc[1]] == ']')
		switch {
		case bracketed:
		case catalogYear > 0:
			if year < catalogYear-1 || year > catalogYear+1 {
				continue
			}
		case year > time.Now().Year()+maxFutureReleaseYears:
			continue
		}
		return year, loc
	}
	return 0, nil
}

// maxFutureReleaseYears bounds how far ahead a title can be announced when no
// catalog year is known.
const maxFutureReleaseYears = 5

func stripDecorativeEpisodeLabel(input string) string {
	match := reEpisodeLabelCode.FindStringIndex(input)
	if len(match) != 2 || match[0] == 0 {
		return input
	}

	prefix := strings.TrimSpace(input[:match[0]])
	prefix = strings.TrimRight(prefix, " -_.,:;•–—")
	if prefix == "" {
		return input
	}

	return prefix
}

func removeSubstring(input, toRemove string) string {
	index := strings.Index(strings.ToLower(input), strings.ToLower(toRemove))
	if index == -1 {
		return input
	}
	removed := input[:index] + " " + input[index+len(toRemove):]
	return strings.Join(strings.Fields(removed), " ")
}

func inferMediaTypeFromTokens(tokens []string) MediaType {
	for _, token := range tokens {
		key := strings.ToLower(strings.Trim(token, "-_.()[]{}"))
		switch key {
		case "s", "season", "episode", "ep", "e", "seasonal":
			return MediaTypeSeries
		case "movie", "film":
			return MediaTypeMovie
		}
	}
	return MediaTypeUnknown
}

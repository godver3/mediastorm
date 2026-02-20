package debrid

import (
	"regexp"
	"strconv"
	"strings"
)

// MediaType represents the content family inferred from a search query.
type MediaType string

const (
	MediaTypeUnknown MediaType = ""
	MediaTypeMovie   MediaType = "movie"
	MediaTypeSeries  MediaType = "series"
)

var (
	reSeasonEpisode      = regexp.MustCompile(`(?i)\bS?(\d{1,2})[xE](\d{1,2})\b`)
	reSeasonOnly         = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
	reSeasonEpisodeWords = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\s*(?:episode|ep)\s*(\d{1,2})\b`)
	reYear               = regexp.MustCompile(`\b(19|20)\d{2}\b`)
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

// ParseQuery performs lightweight normalization and signal extraction from a raw search string.
func ParseQuery(raw string) ParsedQuery {
	parsed := ParsedQuery{Raw: raw}

	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return parsed
	}

	lower := strings.ToLower(candidate)

	if match := reSeasonEpisode.FindStringSubmatch(lower); len(match) == 3 {
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
	} else if match := reSeasonEpisodeWords.FindStringSubmatch(lower); len(match) == 3 {
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

	if match := reYear.FindString(candidate); match != "" {
		if yr, err := strconv.Atoi(match); err == nil && yr > 1900 && yr < 2100 {
			parsed.Year = yr
			candidate = removeSubstring(candidate, match)
		}
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

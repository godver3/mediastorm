package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"novastream/config"
)

var stationQuality = regexp.MustCompile(`(?i)\b(?:sd|hd|fhd|uhd|4k|8k|720[pi]?|1080[pi]?|2160[pi]?|h[ ._-]?26[45]|hevc|avc|50fps|60fps)\b`)
var stationCountryPrefix = regexp.MustCompile(`(?i)^\s*[\[({]?(DE|GER|AT|CH|UK|GB|US|FR|IT)[\])}]?\s*(?:[|:_-]\s*|\s+)`)
var stationCountrySuffix = regexp.MustCompile(`(?i)\s+[\[({]?(DE|GER|AT|CH|UK|GB|US|FR|IT)[\])}]?\s*$`)
var stationCompactQualitySuffix = regexp.MustCompile(`(?i)(?:fhd|uhd|hd|sd|1080[pi]?|720[pi]?|2160[pi]?)$`)

func canonicalStationCountry(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "de", "deu", "ger", "germany", "german", "deutschland":
		return "DE"
	case "at", "aut", "austria", "österreich":
		return "AT"
	case "ch", "che", "switzerland", "schweiz":
		return "CH"
	case "uk", "gb", "gbr", "united kingdom":
		return "GB"
	case "us", "usa", "united states":
		return "US"
	case "fr", "fra", "france":
		return "FR"
	case "it", "ita", "italy":
		return "IT"
	}
	return ""
}

func compactStationName(raw string) string {
	raw = stationQuality.ReplaceAllString(raw, " ")
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' {
			return unicode.ToLower(r)
		}
		return -1
	}, raw)
}

func canonicalStationName(raw string) string {
	name := compactStationName(raw)
	for {
		trimmed := stationCompactQualitySuffix.ReplaceAllString(name, "")
		if trimmed == name {
			break
		}
		name = trimmed
	}
	switch name {
	case "rtltelevision":
		return "rtl"
	case "rtl2":
		return "rtlzwei"
	case "ard", "daserste":
		return "daserste"
	}
	return name
}

func liveStationIdentity(channel LiveChannel, mappings []config.LiveStationMapping) (string, string) {
	for _, mapping := range mappings {
		if mapping.StationID != "" && mapping.SourceID == channel.SourceID && mapping.ChannelID == channel.ID {
			name := strings.TrimSpace(mapping.Name)
			if name == "" {
				name = channel.Name
			}
			return stationID("manual|" + mapping.StationID), name
		}
	}
	name := strings.TrimSpace(channel.Name)
	country := canonicalStationCountry(channel.TvgCountry)
	if match := stationCountryPrefix.FindStringSubmatch(name); len(match) > 1 {
		if country == "" {
			country = canonicalStationCountry(match[1])
		}
		name = stationCountryPrefix.ReplaceAllString(name, "")
	}
	if match := stationCountrySuffix.FindStringSubmatch(name); len(match) > 1 {
		if country == "" {
			country = canonicalStationCountry(match[1])
		}
		name = stationCountrySuffix.ReplaceAllString(name, "")
	}
	id := strings.TrimSpace(channel.TvgID)
	idName := id
	if dot := strings.LastIndex(id, "."); dot >= 0 {
		if scope := canonicalStationCountry(id[dot+1:]); scope != "" {
			if country != "" && scope != country {
				return stationID("conflict|" + channel.SourceID + "|" + channel.ID), channel.Name
			}
			country = scope
			idName = id[:dot]
		}
	}
	if country == "" {
		// Category country labels help with exported German lists, but do not
		// override an explicit programme-country or EPG country suffix.
		for _, token := range strings.FieldsFunc(channel.Group, func(r rune) bool { return !unicode.IsLetter(r) }) {
			if scope := canonicalStationCountry(token); scope != "" {
				if country != "" && country != scope {
					country = ""
					break
				}
				country = scope
			}
		}
	}
	base := canonicalStationName(name)
	if base == "" || base == "channel" {
		return stationID("entry|" + channel.SourceID + "|" + channel.ID), channel.Name
	}
	key := base + "|" + country
	if id != "" && canonicalStationName(idName) != base {
		// Different opaque EPG identifiers are not enough to establish that
		// similarly named broadcasts are interchangeable. An explicit mapping
		// can resolve those entries without guessing.
		key += "|epg:" + strings.ToLower(id)
	}
	if country == "" && id == "" {
		key += "|unverified:" + channel.SourceID + "|" + channel.ID
	}
	language := strings.ToLower(strings.TrimSpace(channel.TvgLanguage))
	switch language {
	case "ger", "deu", "german", "deutsch":
		language = "de"
	case "eng", "english":
		language = "en"
	}
	if language == "" && (country == "DE" || country == "AT") {
		language = "de"
	}
	key += "|" + language
	label := strings.TrimSpace(stationQuality.ReplaceAllString(name, ""))
	label = strings.Trim(label, " [](){}|:_-")
	if label == "" {
		label = channel.Name
	}
	return stationID(key), label
}

func stationID(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "station:" + hex.EncodeToString(digest[:12])
}

func annotateLiveStations(channels []LiveChannel, mappings []config.LiveStationMapping) []LiveChannel {
	result := append([]LiveChannel(nil), channels...)
	counts := make(map[string]int)
	names := make(map[string]string)
	for i := range result {
		result[i].StationID, result[i].StationName = liveStationIdentity(result[i], mappings)
		counts[result[i].StationID]++
		if names[result[i].StationID] == "" {
			names[result[i].StationID] = result[i].StationName
		}
	}
	for i := range result {
		result[i].StationVariants = counts[result[i].StationID]
		result[i].StationName = names[result[i].StationID]
	}
	return result
}

// Keep every source entry selectable. Only their order changes; source IDs,
// EPG keys and existing per-entry favorites remain intact.
func orderLiveStations(channels []LiveChannel, favorites map[string]struct{}) []LiveChannel {
	result := append([]LiveChannel(nil), channels...)
	first := make(map[string]int)
	pinned := make(map[string]bool)
	for i, channel := range result {
		if _, ok := first[channel.StationID]; !ok {
			first[channel.StationID] = i
		}
		if _, ok := favorites[channel.ID]; ok {
			pinned[channel.StationID] = true
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.StationID == b.StationID {
			return false
		}
		if pinned[a.StationID] != pinned[b.StationID] {
			return pinned[a.StationID]
		}
		an, bn := strings.ToLower(a.StationName), strings.ToLower(b.StationName)
		if an != bn {
			return an < bn
		}
		return first[a.StationID] < first[b.StationID]
	})
	return result
}

package filter

import (
	"strconv"
	"strings"
)

// nameInMultiNameRelease returns the expected title that one of the names in a
// release like "1883 / 1883 [01x01-05]" or "Localized / Original (1968)" equals
// exactly. Release parsers keep several names as one title or cut it short.
//
// Names separated by " - " can also be a title and subtitle ("Dune - Prophecy"),
// so there every name must be an expected title. A movie must also name the
// expected year and no year range, which keeps out bundles of several films.
func nameInMultiNameRelease(releaseTitle string, titles []string, isMovie bool, expectedYear int) string {
	byName := make(map[string]string, len(titles))
	for _, title := range titles {
		if name := normalizeForContainment(title); name != "" {
			if _, exists := byName[name]; !exists {
				byName[name] = title
			}
		}
	}
	if isMovie && (yearRangePattern.MatchString(releaseTitle) || !namesYear(releaseTitle, expectedYear)) {
		return ""
	}
	if segments := strings.Split(releaseTitle, " / "); len(segments) > 1 {
		for _, segment := range segments {
			name := nameBeforeDetails(segment)
			if title, ok := byName[normalizeForContainment(name)]; ok {
				return title
			}
		}
		return ""
	}
	names := nameBeforeDetails(releaseTitle)
	segments := strings.Split(names, " - ")
	if len(segments) < 2 {
		return ""
	}
	matched := ""
	for _, segment := range segments {
		title, ok := byName[normalizeForContainment(segment)]
		if !ok {
			return ""
		}
		if matched == "" {
			matched = title
		}
	}
	return matched
}

// nameBeforeDetails cuts a name at the first bracket or marker that starts the
// release details.
func nameBeforeDetails(value string) string {
	if i := strings.IndexAny(value, "[(|*"); i >= 0 {
		return strings.TrimSpace(value[:i])
	}
	return strings.TrimSpace(value)
}

func namesYear(releaseTitle string, expectedYear int) bool {
	for _, token := range yearWordPattern.FindAllString(releaseTitle, -1) {
		if year, err := strconv.Atoi(token); err == nil && abs(year-expectedYear) <= MaxYearDifference {
			return true
		}
	}
	return false
}

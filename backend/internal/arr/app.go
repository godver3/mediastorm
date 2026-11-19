// Package arr implements Sonarr/Radarr-compatible release evaluation: quality
// definitions, custom formats, quality profiles and the release comparer.
//
// The package reimplements the observable behaviour of Sonarr v4 and
// Radarr v5+ so that custom formats and quality profiles published by
// TRaSH-Guides (or exported from a Sonarr/Radarr instance) can be evaluated
// unchanged. No Sonarr/Radarr code is included; parity is verified against a
// running instance with the tools/arrparity harness and the recorded fixtures
// under testdata/parity.
package arr

import (
	"fmt"
	"strings"
)

// App identifies which *arr application a custom format or profile targets.
// Sonarr and Radarr share the custom-format schema but use different enum
// values for sources, languages and indexer flags.
type App string

const (
	AppSonarr App = "sonarr"
	AppRadarr App = "radarr"
)

// ParseApp converts a user-supplied value ("sonarr", "Radarr", "series",
// "movie") to an App.
func ParseApp(value string) (App, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sonarr", "series", "tv", "show", "episode":
		return AppSonarr, nil
	case "radarr", "movie", "movies", "film":
		return AppRadarr, nil
	default:
		return "", fmt.Errorf("unknown arr app %q (want sonarr or radarr)", value)
	}
}

// AppForMediaType returns the app whose semantics apply to a mediastorm media
// type ("movie" or "series").
func AppForMediaType(mediaType string) App {
	if strings.EqualFold(strings.TrimSpace(mediaType), "movie") {
		return AppRadarr
	}
	return AppSonarr
}

// Valid reports whether the app is one of the supported values.
func (a App) Valid() bool {
	return a == AppSonarr || a == AppRadarr
}

// MediaType returns the mediastorm media type the app applies to.
func (a App) MediaType() string {
	if a == AppRadarr {
		return "movie"
	}
	return "series"
}

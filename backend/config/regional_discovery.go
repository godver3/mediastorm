package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var discoveryRegionCode = regexp.MustCompile(`^[A-Z]{2}$`)

// RegionalDiscoveryPresets creates editable ordinary TMDB shelves. Provider
// popularity is explicitly distinguished from Netflix's weekly country chart.
func RegionalDiscoveryPresets(region, preset string) ([]ShelfConfig, error) {
	region = strings.ToUpper(strings.TrimSpace(region))
	preset = strings.ToLower(strings.TrimSpace(preset))
	if !discoveryRegionCode.MatchString(region) {
		return nil, fmt.Errorf("country must be a two-letter code")
	}
	timezone := "UTC"
	if region == "DE" || region == "AT" || region == "CH" {
		timezone = "Europe/Berlin"
	}
	if region == "GB" {
		timezone = "Europe/London"
	}
	makeShelf := func(media, label string, filters url.Values, sort string) ShelfConfig {
		return ShelfConfig{ID: "regional-" + preset + "-" + strings.ToLower(region) + "-" + media, Name: label + " (" + region + ")", Enabled: true, Type: "tmdb", TMDBSourceType: "custom-discover", TMDBMediaType: media, TMDBDiscoverQuery: filters.Encode(), Sort: sort, Limit: 20}
	}
	if preset == "cinema" || preset == "upcoming-cinema" {
		from, to, label := "today-45d", "today", "Recent cinema releases"
		if preset == "upcoming-cinema" {
			from, to, label = "today+1d", "today+30d", "Upcoming cinema releases"
		}
		return []ShelfConfig{makeShelf("movie", label, url.Values{"region": {region}, "with_release_type": {"2|3"}, "release_date.gte": {from}, "release_date.lte": {to}, "date_timezone": {timezone}}, "popularity.desc")}, nil
	}
	if preset == "netflix-chart" {
		result := []ShelfConfig{}
		for _, media := range []string{"movie", "tv"} {
			label := "Netflix weekly Top 10 movies"
			if media == "tv" {
				label = "Netflix weekly Top 10 series"
			}
			result = append(result, makeShelf(media, label, url.Values{"country_chart": {"netflix"}, "watch_region": {region}}, "original"))
		}
		return result, nil
	}
	providers := map[string]struct{ name, id string }{"netflix": {"Netflix", "8|1796"}, "prime": {"Prime Video", "9|2100"}, "disney": {"Disney+", "337"}, "appletv": {"Apple TV", "350"}, "paramount": {"Paramount+", "531"}, "wow": {"WOW", "30"}}
	provider, known := providers[preset]
	if !known && preset != "acclaimed" {
		return nil, fmt.Errorf("unknown discovery preset")
	}
	result := []ShelfConfig{}
	for _, media := range []string{"movie", "tv"} {
		kind := "movies"
		if media == "tv" {
			kind = "series"
		}
		filters := url.Values{"watch_region": {region}, "with_watch_monetization_types": {"flatrate|ads"}}
		label := "Popular " + kind + " on " + provider.name
		sort := "popularity.desc"
		if preset == "acclaimed" {
			label = "Highly rated streaming " + kind
			filters.Set("rating.gte", "7.5")
			filters.Set("votes.gte", "500")
			sort = "vote_average.desc"
		} else {
			filters.Set("with_watch_providers", provider.id)
		}
		result = append(result, makeShelf(media, label, filters, sort))
	}
	return result, nil
}

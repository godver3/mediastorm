package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestRegionalDiscoveryKeepsProviderAvailabilityDistinctFromCharts(t *testing.T) {
	for _, preset := range []string{"netflix", "prime", "disney", "appletv", "paramount", "wow", "acclaimed"} {
		shelves, err := RegionalDiscoveryPresets("de", preset)
		if err != nil || len(shelves) != 2 {
			t.Fatalf("%s: %v", preset, err)
		}
		for _, shelf := range shelves {
			filters, _ := url.ParseQuery(shelf.TMDBDiscoverQuery)
			if filters.Get("watch_region") != "DE" || filters.Get("with_watch_monetization_types") != "flatrate|ads" || strings.Contains(shelf.Name, "Top 10") {
				t.Fatalf("mislabelled or unscoped provider row: %+v", shelf)
			}
		}
	}
	chart, _ := RegionalDiscoveryPresets("DE", "netflix-chart")
	for _, shelf := range chart {
		q, _ := url.ParseQuery(shelf.TMDBDiscoverQuery)
		if q.Get("country_chart") != "netflix" || shelf.Sort != "original" || !strings.Contains(shelf.Name, "weekly") {
			t.Fatal("chart lost provenance or order")
		}
	}
	cinema, _ := RegionalDiscoveryPresets("DE", "cinema")
	q, _ := url.ParseQuery(cinema[0].TMDBDiscoverQuery)
	if q.Get("region") != "DE" || q.Get("release_date.gte") != "today-45d" || q.Get("with_release_type") != "2|3" || q.Get("date_timezone") != "Europe/Berlin" {
		t.Fatal("cinema dates or country are not rolling")
	}
	for _, country := range []string{"", "Deutschland", "D1", "../DE"} {
		if _, err := RegionalDiscoveryPresets(country, "cinema"); err == nil {
			t.Fatalf("invalid country %q accepted", country)
		}
	}
	if _, err := RegionalDiscoveryPresets("DE", "unknown"); err == nil {
		t.Fatal("unknown preset accepted")
	}
}

func TestRegionalDiscoveryCountrySpecifics(t *testing.T) {
	shelves, err := RegionalDiscoveryPresets("us", "cinema")
	if err != nil || len(shelves) != 1 || !strings.Contains(shelves[0].TMDBDiscoverQuery, "date_timezone=America%2FNew_York") {
		t.Fatalf("US cinema timezone: %v %+v", err, shelves)
	}
	if shelves, _ := RegionalDiscoveryPresets("ZZ", "cinema"); !strings.Contains(shelves[0].TMDBDiscoverQuery, "date_timezone=UTC") {
		t.Fatal("unknown country did not fall back to UTC")
	}
	if _, err := RegionalDiscoveryPresets("US", "wow"); err == nil {
		t.Fatal("WOW offered outside Germany and Austria")
	}
	if _, err := RegionalDiscoveryPresets("AT", "wow"); err != nil {
		t.Fatalf("WOW in Austria: %v", err)
	}
}

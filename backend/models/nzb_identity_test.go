package models

import "testing"

func TestNZBIdentityKeys(t *testing.T) {
	for _, tc := range []struct {
		identity NZBIdentity
		key      string
	}{
		{NZBIdentity{CatalogID: " TMDB:movie:0078 ", MediaType: "movie"}, "tmdb:movie:78"},
		{NZBIdentity{CatalogID: "tvdb:series:42", MediaType: "series", Season: 0, Episode: 1}, "tvdb:series:42:s0e1"},
		{NZBIdentity{CatalogID: "tmdb:tv:42", MediaType: "series", Season: 2, Episode: 3}, "tmdb:tv:42:s2e3"},
		{NZBIdentity{CatalogID: "Blade.Runner.2049.2017", MediaType: "movie"}, ""},
		{NZBIdentity{CatalogID: "tmdb:movie:42", MediaType: "series", Episode: 1}, ""},
		{NZBIdentity{CatalogID: "tmdb:tv:42", MediaType: "series"}, ""},
		{NZBIdentity{CatalogID: "tmdb:movie:0", MediaType: "movie"}, ""},
	} {
		key, err := tc.identity.Key()
		if key != tc.key || (err != nil) != (tc.key == "") {
			t.Errorf("%+v: key=%q err=%v", tc.identity, key, err)
		}
	}
}

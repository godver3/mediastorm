package mediaidentity

import "testing"

func TestResolveMovieUsesCanonicalProviderIDAndAliases(t *testing.T) {
	identity := Resolve(Input{
		MediaType: "movie",
		ID:        "tmdb:movie:12",
		ExternalIDs: map[string]string{
			"tmdb": "12",
			"tvdb": "256",
			"imdb": "TT0266543",
		},
	})

	if identity.ID != "tmdb:movie:12" {
		t.Fatalf("ID = %q, want tmdb:movie:12", identity.ID)
	}
	if identity.Key != "movie:tmdb:movie:12" {
		t.Fatalf("Key = %q", identity.Key)
	}
	wantKeys := map[string]bool{
		"movie:tvdb:movie:256": true,
		"movie:tmdb:movie:12":  true,
		"movie:tt0266543":      true,
	}
	for key := range wantKeys {
		if !contains(identity.CandidateKeys, key) {
			t.Fatalf("CandidateKeys missing %q: %#v", key, identity.CandidateKeys)
		}
	}
}

func TestResolveMoviePreservesExplicitProviderIDWhenExternalIDsAreSparse(t *testing.T) {
	identity := Resolve(Input{
		MediaType:   "movie",
		ID:          "tmdb:movie:809137",
		ExternalIDs: map[string]string{"imdb": "tt14549712"},
	})

	if identity.ID != "tmdb:movie:809137" {
		t.Fatalf("ID = %q, want tmdb:movie:809137", identity.ID)
	}
	if !contains(identity.CandidateKeys, "movie:tt14549712") {
		t.Fatalf("CandidateKeys missing IMDb alias: %#v", identity.CandidateKeys)
	}
}

func TestResolveEpisodeCanonicalizesSeriesIDAndSuffix(t *testing.T) {
	identity := Resolve(Input{
		MediaType:     "episode",
		ID:            "tmdb:tv:4629:S01E02",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		ExternalIDs: map[string]string{
			"tmdb":        "4629",
			"tvdb":        "72449",
			"episodeTvdb": "347291",
		},
	})

	if identity.SeriesID != "tmdb:tv:4629" {
		t.Fatalf("SeriesID = %q, want tmdb:tv:4629", identity.SeriesID)
	}
	if identity.ID != "tmdb:tv:4629:s01e02" {
		t.Fatalf("ID = %q, want canonical episode ID", identity.ID)
	}
	if !contains(identity.CandidateKeys, "episode:tvdb:series:72449:s01e02") {
		t.Fatalf("CandidateKeys missing TVDB alias: %#v", identity.CandidateKeys)
	}
}

func TestResolveEpisodeUsesTitleIDWhenParsedSeriesContradictsExternalIDs(t *testing.T) {
	identity := Resolve(Input{
		MediaType:     "episode",
		ID:            "tmdb:tv:1187404:s06e09",
		SeasonNumber:  6,
		EpisodeNumber: 9,
		ExternalIDs: map[string]string{
			"tmdb":    "1399",
			"tvdb":    "121361",
			"imdb":    "tt0944947",
			"titleId": "tmdb:tv:1399",
		},
	})

	if identity.SeriesID != "tmdb:tv:1399" {
		t.Fatalf("SeriesID = %q, want tmdb:tv:1399", identity.SeriesID)
	}
	if identity.ID != "tmdb:tv:1399:s06e09" {
		t.Fatalf("ID = %q, want tmdb:tv:1399:s06e09", identity.ID)
	}
	if !contains(identity.CandidateKeys, "episode:tmdb:tv:1187404:s06e09") {
		t.Fatalf("CandidateKeys missing legacy parsed alias: %#v", identity.CandidateKeys)
	}
}

func TestResolveEpisodeUsesProviderSeriesIDsWhenTitleIDIsLegacyBridge(t *testing.T) {
	identity := Resolve(Input{
		MediaType:     "episode",
		ID:            "tmdb:tv:220102:s01e03",
		SeasonNumber:  1,
		EpisodeNumber: 3,
		ExternalIDs: map[string]string{
			"tmdb":    "720",
			"tvdb":    "75931",
			"titleId": "tmdb:tv:220102",
		},
	})

	if identity.SeriesID != "tmdb:tv:720" {
		t.Fatalf("SeriesID = %q, want provider tmdb:tv:720", identity.SeriesID)
	}
	if identity.ID != "tmdb:tv:720:s01e03" {
		t.Fatalf("ID = %q, want provider episode ID", identity.ID)
	}
	if !contains(identity.CandidateKeys, "episode:tmdb:tv:220102:s01e03") {
		t.Fatalf("CandidateKeys missing legacy bridge key: %#v", identity.CandidateKeys)
	}
}

func TestResolveEpisodeUsesProviderSeriesIDsWhenExplicitSeriesContradictsExternalIDs(t *testing.T) {
	identity := Resolve(Input{
		MediaType:     "episode",
		ID:            "tmdb:tv:4629:s02e09",
		SeriesID:      "tmdb:tv:4629",
		SeasonNumber:  2,
		EpisodeNumber: 9,
		ExternalIDs: map[string]string{
			"tmdb":    "250307",
			"tvdb":    "448176",
			"titleId": "tmdb:tv:4629",
		},
	})

	if identity.SeriesID != "tmdb:tv:250307" {
		t.Fatalf("SeriesID = %q, want provider tmdb:tv:250307", identity.SeriesID)
	}
	if identity.ID != "tmdb:tv:250307:s02e09" {
		t.Fatalf("ID = %q, want provider episode ID", identity.ID)
	}
	if !contains(identity.CandidateKeys, "episode:tmdb:tv:4629:s02e09") {
		t.Fatalf("CandidateKeys missing old explicit series key: %#v", identity.CandidateKeys)
	}
}

func TestEquivalentMatchesSharedEpisodeProviderID(t *testing.T) {
	a := Resolve(Input{
		MediaType:     "episode",
		ID:            "tmdb:tv:1:s01e01",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		ExternalIDs:   map[string]string{"tmdb": "1", "episodeTvdb": "900"},
	})
	b := Resolve(Input{
		MediaType:     "episode",
		ID:            "tvdb:series:2:s01e99",
		SeasonNumber:  1,
		EpisodeNumber: 99,
		ExternalIDs:   map[string]string{"tvdb": "2", "episodeTvdb": "900"},
	})

	if !Equivalent(a, b) {
		t.Fatalf("expected identities to match via episodeTvdb")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestIndexKeysExcludeBareNumericIDs(t *testing.T) {
	identity := Resolve(Input{
		MediaType:   "movie",
		ID:          "tmdb:movie:550",
		ExternalIDs: map[string]string{"tmdb": "550", "tvdb": "1234"},
	})

	if !contains(identity.CandidateKeys, "movie:550") {
		t.Fatalf("CandidateKeys should keep bare numeric legacy key: %#v", identity.CandidateKeys)
	}
	indexKeys := identity.IndexKeys()
	if len(indexKeys) == 0 {
		t.Fatalf("IndexKeys should not be empty")
	}
	for _, key := range indexKeys {
		if key == "movie:550" || key == "movie:1234" {
			t.Fatalf("IndexKeys must not contain ambiguous bare numeric key %q: %#v", key, indexKeys)
		}
	}
	if !contains(indexKeys, "movie:tmdb:movie:550") {
		t.Fatalf("IndexKeys missing canonical key: %#v", indexKeys)
	}
	if !contains(indexKeys, "movie:tvdb:movie:1234") {
		t.Fatalf("IndexKeys missing tvdb-prefixed key: %#v", indexKeys)
	}
}

func TestIndexKeysDoNotCollideAcrossProviders(t *testing.T) {
	// Two unrelated titles sharing the same numeric value in different
	// provider ID spaces must not share any index key.
	tvdbMovie := Resolve(Input{
		MediaType:   "movie",
		ID:          "tvdb:movie:550",
		ExternalIDs: map[string]string{"tvdb": "550"},
	})
	tmdbMovie := Resolve(Input{
		MediaType:   "movie",
		ID:          "tmdb:movie:550",
		ExternalIDs: map[string]string{"tmdb": "550"},
	})

	for _, a := range tvdbMovie.IndexKeys() {
		for _, b := range tmdbMovie.IndexKeys() {
			if a == b {
				t.Fatalf("unrelated titles share index key %q", a)
			}
		}
	}
}

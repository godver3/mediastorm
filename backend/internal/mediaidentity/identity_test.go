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

func TestResolveEpisodeUsesReliableItemIDWhenTitleIDConflictsWithExternalIDs(t *testing.T) {
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

	if identity.SeriesID != "tmdb:tv:220102" {
		t.Fatalf("SeriesID = %q, want reliable tmdb:tv:220102", identity.SeriesID)
	}
	if identity.ID != "tmdb:tv:220102:s01e03" {
		t.Fatalf("ID = %q, want reliable episode ID", identity.ID)
	}
	if contains(identity.CandidateKeys, "episode:tmdb:tv:720:s01e03") {
		t.Fatalf("CandidateKeys should not include conflicting provider key: %#v", identity.CandidateKeys)
	}
}

func TestResolveEpisodeUsesReliableSeriesIDWhenExplicitSeriesContradictsExternalIDs(t *testing.T) {
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

	if identity.SeriesID != "tmdb:tv:4629" {
		t.Fatalf("SeriesID = %q, want reliable tmdb:tv:4629", identity.SeriesID)
	}
	if identity.ID != "tmdb:tv:4629:s02e09" {
		t.Fatalf("ID = %q, want reliable episode ID", identity.ID)
	}
	if contains(identity.CandidateKeys, "episode:tmdb:tv:250307:s02e09") {
		t.Fatalf("CandidateKeys should not include conflicting provider key: %#v", identity.CandidateKeys)
	}
}

func TestCanonicalSeriesExternalIDsIgnoresConflictingProviderIDs(t *testing.T) {
	ids := CanonicalSeriesExternalIDs("tmdb:tv:220102", "tmdb:tv:220102:s01e05", map[string]string{
		"imdb":        "tt0118480",
		"tmdb":        "4629",
		"tvdb":        "72449",
		"titleId":     "tmdb:tv:220102",
		"episodeTvdb": "85755",
	})

	if ids["tmdb"] != "220102" {
		t.Fatalf("tmdb = %q, want 220102 (ids=%#v)", ids["tmdb"], ids)
	}
	if ids["tvdb"] == "72449" || ids["imdb"] == "tt0118480" {
		t.Fatalf("conflicting provider IDs should not survive: %#v", ids)
	}
}

func TestStorageExternalIDsScrubsConflictingSeriesProviderIDs(t *testing.T) {
	ids := StorageExternalIDs("episode", "tmdb:tv:4629:s01e06", "tmdb:tv:4629", map[string]string{
		"imdb":            "tt30460310",
		"tmdb":            "220102",
		"tvdb":            "450033",
		"trakt":           "4605",
		"titleId":         "tmdb:tv:4629",
		"episodeImdb":     "tt0709188",
		"episodeTmdb":     "336047",
		"episodeTvdb":     "11610260",
		"episodeTrakt":    "344116",
		"absoluteEpisode": "5",
	})

	if ids["tmdb"] != "4629" {
		t.Fatalf("tmdb = %q, want reliable 4629 (ids=%#v)", ids["tmdb"], ids)
	}
	if ids["tvdb"] == "450033" || ids["imdb"] == "tt30460310" {
		t.Fatalf("conflicting title provider IDs should be scrubbed: %#v", ids)
	}
	if ids["episodeTvdb"] != "11610260" || ids["episodeTrakt"] != "344116" {
		t.Fatalf("episode-scoped IDs should be preserved: %#v", ids)
	}
	if ids["titleId"] != "tmdb:tv:4629" {
		t.Fatalf("titleId should be preserved for audit/bridge context: %#v", ids)
	}
}

func TestCanonicalSeriesExternalIDsExcludesEpisodeNumbering(t *testing.T) {
	ids := CanonicalSeriesExternalIDs("tmdb:tv:220102", "tmdb:tv:220102:s01e05", map[string]string{
		"tmdb":            "220102",
		"tvdb":            "450033",
		"episodeTvdb":     "11610260",
		"absoluteEpisode": "5",
	})

	if ids["absoluteEpisode"] != "" || ids["episodeTvdb"] != "" {
		t.Fatalf("episode-scoped IDs should not be series IDs: %#v", ids)
	}
	if ids["tmdb"] != "220102" || ids["tvdb"] != "450033" {
		t.Fatalf("series IDs missing from canonical IDs: %#v", ids)
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

func TestSanitizeIDStripsRedundantMediaTypePrefixes(t *testing.T) {
	cases := map[string]string{
		"movie:tvdb:movie:10702":       "tvdb:movie:10702",
		"movie:movie:tvdb:movie:10702": "tvdb:movie:10702",
		"episode:tmdb:tv:95396:s01e01": "tmdb:tv:95396:s01e01",
		"series:tmdb:tv:95396":         "tmdb:tv:95396",
		"tvdb:movie:10702":             "tvdb:movie:10702",
		"tmdb:tv:95396":                "tmdb:tv:95396",
		"tt0133093":                    "tt0133093",
		"movie:":                       "movie:",
	}
	for input, want := range cases {
		if got := SanitizeID(input); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeIDStripsURLAccessTokens(t *testing.T) {
	cases := map[string]string{
		"http://192.168.1.100:7777/api/live/recordings/abc/stream?token=SECRET":            "http://192.168.1.100:7777/api/live/recordings/abc/stream",
		"/api/live/recordings/abc/stream?token=SECRET&profileid=default":                   "/api/live/recordings/abc/stream?profileid=default",
		"https://host/api/live/recordings/abc/stream?profileid=x&token=SECRET&title=show":  "https://host/api/live/recordings/abc/stream?profileid=x&title=show",
		"https://host/api/live/recordings/abc/stream?profileid=x&token=SECRET":             "https://host/api/live/recordings/abc/stream?profileid=x",
		"some title with token= in it":                                                     "some title with token= in it",
		"tmdb:movie:603":                                                                   "tmdb:movie:603",
	}
	for input, want := range cases {
		if got := SanitizeID(input); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveSanitizesDoublePrefixedAndTokenIDs(t *testing.T) {
	identity := Resolve(Input{MediaType: "movie", ID: "movie:tvdb:movie:10702"})
	if identity.Key != "movie:tvdb:movie:10702" {
		t.Fatalf("Key = %q, want movie:tvdb:movie:10702", identity.Key)
	}
	if identity.ID != "tvdb:movie:10702" {
		t.Fatalf("ID = %q, want tvdb:movie:10702", identity.ID)
	}

	urlIdentity := Resolve(Input{MediaType: "movie", ID: "http://host/api/live/recordings/abc/stream?token=SECRET"})
	if urlIdentity.ID != "http://host/api/live/recordings/abc/stream" {
		t.Fatalf("URL ID = %q, want token stripped", urlIdentity.ID)
	}
}

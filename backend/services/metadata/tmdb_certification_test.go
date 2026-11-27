package metadata

import "testing"

func TestParseTMDBMovieReleasesPrefersRealRatingOverNR(t *testing.T) {
	us := func(certs ...string) tmdbReleaseDatesResponse {
		entries := make([]tmdbReleaseEntry, 0, len(certs))
		for _, c := range certs {
			entries = append(entries, tmdbReleaseEntry{Certification: c, Type: 6, ReleaseDate: "1969-12-07T00:00:00.000Z"})
		}
		return tmdbReleaseDatesResponse{Results: []tmdbReleaseCountry{{ISO31661: "US", ReleaseDates: entries}}}
	}
	cases := []struct {
		certs []string
		want  string
	}{
		{[]string{"NR", "G"}, "G"}, // Frosty the Snowman (1969)
		{[]string{"", "Unrated", "PG"}, "PG"},
		{[]string{"PG-13", "R"}, "PG-13"},
		{[]string{"NR"}, "NR"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := parseTMDBMovieReleases(us(tc.certs...)).Certification; got != tc.want {
			t.Errorf("certs %v: got %q, want %q", tc.certs, got, tc.want)
		}
	}
}

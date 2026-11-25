package debrid

import "testing"

func TestParseQueryWithYear(t *testing.T) {
	for _, tc := range []struct {
		query       string
		catalogYear int
		title       string
		year        int
	}{
		{"Blade Runner 2099 S01E01", 2026, "Blade Runner 2099", 0},
		{"Blade Runner 2099 S01E01", 0, "Blade Runner 2099", 0},
		{"Blade Runner 2049 2017", 2017, "Blade Runner 2049", 2017},
		{"Blade Runner 2049 2017", 0, "Blade Runner 2049", 2017},
		{"Wonder Woman 1984", 2020, "Wonder Woman 1984", 0},
		{"Wonder Woman 1984 2020", 2020, "Wonder Woman 1984", 2020},
		{"1984 1984", 1984, "1984", 1984},
		{"1923 S01E01", 2022, "1923", 0},
		{"1923 S01E01", 0, "1923", 0},
		{"2001 A Space Odyssey 1968", 1968, "2001 A Space Odyssey", 1968},
		{"2001 A Space Odyssey", 0, "2001 A Space Odyssey", 0},
		{"2012 1080p", 2009, "2012", 0},
		{"The Matrix 1999 1080p", 0, "The Matrix", 1999},
		{"The Matrix 1999", 1999, "The Matrix", 1999},
		{"Doctor Who (2005) S01E02", 2005, "Doctor Who", 2005},
		{"Doctor Who (2005) S01E02", 1963, "Doctor Who", 2005},
		{"Euro 2024 S01E01", KeepQueryYears, "Euro 2024", 0},
	} {
		got := ParseQueryWithYear(tc.query, tc.catalogYear)
		if got.Title != tc.title || got.Year != tc.year {
			t.Errorf("ParseQueryWithYear(%q, %d) = %q, %d; want %q, %d", tc.query, tc.catalogYear, got.Title, got.Year, tc.title, tc.year)
		}
	}
}

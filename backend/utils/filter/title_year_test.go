package filter

import (
	"testing"

	"novastream/models"
)

func TestTitleYears(t *testing.T) {
	for _, tc := range []struct {
		release, expected string
		year              int
		movie             bool
		aliases           []string
		want              bool
	}{
		{"Blade.Runner.2099.S01E01.1080p.WEB-DL", "Blade Runner 2099", 2026, false, nil, true},
		{"Blade.Runner.2099.2026.S01E01.1080p.WEB-DL", "Blade Runner 2099", 2026, false, nil, true},
		{"Blade.Runner.2099.1982.S01E01.1080p.WEB-DL", "Blade Runner 2099", 2026, false, nil, false},
		{"Blade.Runner.S01E01.1080p.WEB-DL", "Blade Runner 2099", 2026, false, nil, false},
		{"Blade.Runner.Black.Lotus.S01E01.1080p.WEB-DL", "Blade Runner 2099", 2026, false, nil, false},
		{"1923.S01E01.1080p.WEB-DL", "1923", 2022, false, nil, true},
		{"1923.S01E01.1923.REPACK.1080p.HEVC.x265-MeGusta", "1923", 2022, false, nil, true},
		{"Wonder.Woman.1984.2020.1080p.BluRay.DDP.7.1.x265-edge2020", "Wonder Woman 1984", 2020, true, nil, true},
		{"2001.A.Space.Odyssey.1968.2160p.UHD.BluRay.x265.10bit.DTS-HD.MA.5.1-DirtyHippie", "2001: A Space Odyssey", 1968, true, nil, true},
		{"Euro.2024.S01E01.1080p.WEB-DL", "Euro 2024", 2020, false, nil, true},
		{"Wonder.Woman.1984.1080p.BluRay", "Wonder Woman 1984", 2020, true, nil, true},
		{"Wonder.Woman.1984.2020.1080p.BluRay", "Wonder Woman 1984", 2020, true, nil, true},
		{"Wonder.Woman.2017.1080p.BluRay", "Wonder Woman 1984", 2020, true, nil, false},
		{"2001.A.Space.Odyssey.1080p.BluRay", "2001: A Space Odyssey", 1968, true, nil, true},
		{"2012.1080p.BluRay", "2012", 2009, true, nil, true},
		{"1917.1080p.BluRay", "1917", 2019, true, nil, true},
		{"1917.S01E01.1080p.WEB-DL", "1917", 2019, true, nil, false},
		{"300.1080p.BluRay", "300", 2006, true, nil, true},
		{"1984.1984.1080p.BluRay", "1984", 1956, true, nil, false},
		{"Death.Race.2008.1080p.BluRay", "Death Race 2000", 1975, true, nil, false},
		// Several names in one release.
		{"1883 / 1883 [01x01-05 из 10] (2021) WEB-DL-HEVC 2160p от Kerob | L", "1883", 2021, false, nil, true},
		{"1923 / 1923 / Сезон: 1 / Серии: 1-7 из 8 [2022 WEB-DL 1080p] MVO + Original (Eng)", "1923", 2022, false, nil, true},
		{"1923 *2022* [S01] [1080p.WEB-DL.H265.EAC3-FT] [ENG-Lektor PL]", "1923", 2022, false, nil, true},
		{"2001 год: Космическая одиссея / Космическая одиссея 2001 / 2001: A Space Odyssey [1968 BDRip 1080p] MVO", "2001: A Space Odyssey", 1968, true, nil, true},
		{"2001: Odissea nello spazio - 2001: A Space Odyssey (1968) 1080p H265 BluRay", "2001: A Space Odyssey", 1968, true, []string{"2001: Odissea nello spazio"}, true},
		{"1883 / 1883 [01x01-05 из 10] (2021) WEB-DL", "1923", 2022, false, nil, false},
		{"Дюна: Пророчество / Dune: Prophecy [S01] (2024) WEB-DL 1080p", "Dune", 2024, false, nil, false},
		{"Dune - Prophecy S01E01 1080p WEB-DL", "Dune", 2024, false, nil, false},
		{"Чужой / Alien / Aliens [1979-1992] BDRip 1080p", "Alien", 1979, true, nil, false},
		{"Blade Runner / Blade Runner 2049 [2017 BDRip 1080p]", "Blade Runner", 1982, true, nil, false},
		// An alias that names another release year does not let it in.
		{"Dune.1984.1080p.BluRay", "Dune", 2021, true, []string{"Dune (1984)"}, false},
		{"Battlestar.Galactica.1978.S01E01.1080p", "Battlestar Galactica", 2004, false, []string{"Battlestar Galactica 1978"}, false},
		// A year that only tells same-named titles apart may be left out.
		{"Treasure.Island.S01E01.1080p.WEB-DL", "Treasure Island 2026", 2026, false, nil, true},
		{"Doctor.Who.2005.S01E01.1080p", "Doctor Who", 2005, false, []string{"Doctor Who (2005)"}, true},
	} {
		opts := Options{ExpectedTitle: tc.expected, ExpectedYear: tc.year, IsMovie: tc.movie, AlternateTitles: tc.aliases}
		if !tc.movie {
			opts.TargetSeason, opts.TargetEpisode = 1, 1
		}
		if got := len(Results([]models.NZBResult{{Title: tc.release}}, opts)) == 1; got != tc.want {
			t.Errorf("%q for %q (%d): passed=%v, want %v", tc.release, tc.expected, tc.year, got, tc.want)
		}
	}
}

// A title year repeated in a two-language name does not hide the real one.
func TestReleaseYearOutsideTitlePrefersOtherYear(t *testing.T) {
	inTitle := map[int]bool{2001: true}
	if got := releaseYearOutsideTitle("2001: Odissea nello spazio - 2001: A Space Odyssey (1968) 1080p", 2001, true, inTitle); got != 1968 {
		t.Errorf("year = %d, want 1968", got)
	}
}

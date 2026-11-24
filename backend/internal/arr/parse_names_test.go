package arr

import (
	"strings"
	"testing"
	"unicode/utf8"

	ptt "github.com/itsrenoria/ptt-go"
)

func TestRadarrPreReleaseSourcesCompeteWithMainSources(t *testing.T) {
	cases := []struct{ title, want string }{
		// The last source tag wins, pre-release or not.
		{"Cam.2018.1080p.NF.WEB-DL.DDP5.1.x264-NTG", "WEBDL-1080p"},
		{"Movie.Name.R5.2020.1080p.WEB-DL-GRP", "WEBDL-1080p"},
		{"Movie.Name.2020.DVDRip.R5.x264-GRP", "REGIONAL"},
		{"Movie.Name.2020.1080p.WEB-DL.HDCAM.x264-GRP", "CAM"},
		// A pre-release tag that is the release group does not count.
		{"Movie.Name.2020.1080p.WEB-DL.x264-SCR", "WEBDL-1080p"},
		{"Movie.Name.2020.1080p.WEB-DL.x264-TS", "WEBDL-1080p"},
		{"Movie.Name.2020.1080p.WEB-DL.x264-TC.mkv", "WEBDL-1080p"},
		{"Movie.Name.2020.1080p.BluRay.x264-WP", "Bluray-1080p"},
		// Real pre-releases keep working.
		{"Movie.Name.2020.HDCAM.x264-GRP", "CAM"},
		{"Movie.Name.2020.720p.HDTS.x264-GRP", "TELESYNC"},
		{"Movie.Name.2020.TS.x264-GRP", "TELESYNC"},
		{"Movie.Name.2020.HDTC.x264-GRP", "TELECINE"},
		{"Movie.Name.2020.DVDSCR.x264-GRP", "DVDSCR"},
		{"Movie.Name.2020.WORKPRINT.x264-GRP", "WORKPRINT"},
		{"Movie.Name.2020.R5.LiNE.x264-GRP", "REGIONAL"},
		{"Movie.Name.2020.CAM-GRP", "CAM"},
		{"Movie.Name.CAM.x264-GRP", "CAM"},
	}
	for _, tc := range cases {
		if got := Parse(AppRadarr, tc.title).Quality.Name; got != tc.want {
			t.Errorf("%s: quality %s, want %s", tc.title, got, tc.want)
		}
	}
}

func langNames(ls []Language) string {
	s := make([]string, len(ls))
	for i, l := range ls {
		s[i] = string(l)
	}
	return strings.Join(s, ",")
}

func TestLanguageCodesDoNotMatchTitleWords(t *testing.T) {
	cases := []struct {
		app   App
		title string
		want  string
	}{
		// Radarr: the movie name is not scanned, DE/EN/ES/PL/... are
		// case-sensitive, and codes Radarr does not know are ignored.
		{AppRadarr, "Cidade.de.Deus.2002.1080p.BluRay.x264-GRP", "Unknown"},
		{AppRadarr, "The.Ice.Road.2021.1080p.WEB-DL.DDP5.1.H.264-GRP", "Unknown"},
		{AppRadarr, "Dan.in.Real.Life.2007.1080p.BluRay.x264-GRP", "Unknown"},
		{AppRadarr, "Tel.Aviv.on.Fire.2018.1080p.BluRay.x264-GRP", "Unknown"},
		{AppRadarr, "Movie.Name.2020.1080p.WEB-DL.FIN.NOR.POR.GRE.x264-GRP", "Unknown"},
		{AppRadarr, "Movie.Name.2020.1080p.WEB-DL.x264-KOR", "Unknown"},
		{AppRadarr, "Movie.Name.2020.1080p.WEB-DL.PL.SUB.x264-GRP", "Unknown"},
		{AppRadarr, "Movie.Name.2020.1080p.BluRay.DTS-ES.x264-GRP", "Unknown"},
		{AppRadarr, "Movie.Name.2020.German.DL.1080p.BluRay.x264-GRP", "German,Original"},
		{AppRadarr, "Movie.Name.2020.DE.EN.1080p.BluRay.x264-GRP", "English,German"},
		{AppRadarr, "Movie.Name.2020.PL.1080p.WEB-DL.x264-GRP", "Polish"},
		{AppRadarr, "Movie.Name.2020.ITA.ENG.1080p.BluRay.x264-GRP", "English,Italian"},
		{AppRadarr, "Movie.Name.2020.FRENCH.1080p.WEB.H264-GRP", "French"},
		{AppRadarr, "Movie.Name.2020.1080p.BluRay.KOR.x264-GRP", "Korean"},
		// Sonarr: no "NL", "PLSUB" or bare "cat"; PL is case-sensitive and
		// not a language next to SUB.
		{AppSonarr, "Show.S01E04.PL.SUB.1080p.WEB-DL.x264-GRP", "Unknown"},
		{AppSonarr, "Show.S01E04.1080p.WEB-DL.NL.Subs-GRP", "Unknown"},
		{AppSonarr, "Show.S01E04.1080p.WEB-DL.PLSUB-GRP", "Unknown"},
		{AppSonarr, "Show.S01E04.The.Cat.Returns.1080p.WEB-DL.x264-GRP", "Unknown"},
		{AppSonarr, "Show.S01E04.PL.1080p.WEB-DL.x264-GRP", "Polish"},
		{AppSonarr, "Show.S01E04.CAT.DD5.1.1080p.WEB-DL.x264-GRP", "Catalan"},
		{AppSonarr, "Show.S01E04.German.DL.1080p.WEB.h264-GRP", "German,Original"},
	}
	for _, tc := range cases {
		got := langNames(Parse(tc.app, tc.title).Languages)
		if got != tc.want {
			t.Errorf("%s %s: languages %s, want %s", tc.app, tc.title, got, tc.want)
		}
	}
}

func TestReleaseTypeRangesAndPartialSeasons(t *testing.T) {
	cases := []struct {
		title string
		typ   ReleaseType
		full  bool
		count int
	}{
		{"Show.Season.1.Part.2.1080p.WEB-DL.x264-GRP", ReleaseTypeUnknown, false, 0},
		{"Show.Season.2.Vol.1.1080p.WEB-DL.x264-GRP", ReleaseTypeUnknown, false, 0},
		{"Show.S01.Part.2.1080p.WEB-DL.x264-GRP", ReleaseTypeUnknown, false, 0},
		{"Show.Season.1.1080p.WEB-DL.x264-GRP", ReleaseTypeSeasonPack, true, 0},
		{"[Erai-raws] Show - 01 ~ 12 [1080p][Multiple Subtitle]", ReleaseTypeMultiEpisode, false, 12},
		{"[Group] Show - 001~100 [1080p]", ReleaseTypeMultiEpisode, false, 100},
		{"Show.1x01-1x04.720p.HDTV.x264-GRP", ReleaseTypeMultiEpisode, false, 4},
		{"Show.1x01-x03.720p.HDTV.x264-GRP", ReleaseTypeMultiEpisode, false, 3},
		{"Show.1x01-02.720p.HDTV.x264-GRP", ReleaseTypeMultiEpisode, false, 2},
		{"Show.S01E01-E04.1080p.WEB-DL.x264-GRP", ReleaseTypeMultiEpisode, false, 4},
		{"Show.S01E01-04.1080p.WEB-DL.x264-GRP", ReleaseTypeMultiEpisode, false, 4},
		{"Show.S01E01E02.1080p.WEB-DL.x264-GRP", ReleaseTypeMultiEpisode, false, 2},
		{"Show.S01E05-100.Days.1080p.WEB-DL.x264-GRP", ReleaseTypeSingleEpisode, false, 1},
		{"Show (2019 ~ 2020) S01 1080p WEB-DL x264-GRP", ReleaseTypeSeasonPack, true, 0},
	}
	for _, tc := range cases {
		r := Parse(AppSonarr, tc.title)
		if r.ReleaseType != tc.typ || r.FullSeason != tc.full || r.EpisodeCount != tc.count {
			t.Errorf("%s: type %v full %v count %d, want %v %v %d", tc.title, r.ReleaseType, r.FullSeason, r.EpisodeCount, tc.typ, tc.full, tc.count)
		}
	}
}

func TestSortEvaluationsPutsAllowedFirst(t *testing.T) {
	ev := newTestEvaluator(t, testProfile())
	ctx := ReleaseContext{OriginalLanguage: "English"}
	evals := []Evaluation{
		evalTitle(ev, "Show.S01E01.1080p.WEB.h264-BADGRP", ctx), // best quality, rejected by score
		evalTitle(ev, "Show.S01E01.720p.WEB.h264-GRP", ctx),
		evalTitle(ev, "Show.S01E01.German.DL.1080p.WEB.h264-GRP", ctx),
	}
	ev.SortEvaluations(evals)
	want := []string{
		"Show.S01E01.German.DL.1080p.WEB.h264-GRP",
		"Show.S01E01.720p.WEB.h264-GRP",
		"Show.S01E01.1080p.WEB.h264-BADGRP",
	}
	for i, e := range evals {
		if e.Release.Title != want[i] {
			t.Fatalf("position %d: got %s (allowed %v) want %s", i, e.Release.Title, e.Allowed, want[i])
		}
	}
}

func TestParseCapsTitleLength(t *testing.T) {
	long := "Show.S01E01.1080p.WEB.h264-GRP." + strings.Repeat("é", 2000)
	r := Parse(AppSonarr, long)
	if len(r.Title) > maxTitleLen || !utf8.ValidString(r.Title) {
		t.Fatalf("title not capped on a rune boundary: %d bytes", len(r.Title))
	}
	if r.Quality.Name != "WEBDL-1080p" {
		t.Errorf("quality %s, want WEBDL-1080p", r.Quality.Name)
	}
}

// Radarr inserts "A.Movie" when the movie name contains a dot and "A Movie"
// otherwise.
func TestSimpleMovieTitleSeparator(t *testing.T) {
	cases := map[string]string{
		"Movie.Name.2020.1080p.BluRay.x264-GRP": "A.Movie.2020.1080p.BluRay.x264-GRP",
		"Movie Name 2020 1080p BluRay x264-GRP": "A Movie 2020 1080p BluRay x264-GRP",
		"Movie_Name_2020_1080p_BluRay_x264-GRP": "A Movie_2020_1080p_BluRay_x264-GRP",
		"Mr. Movie (2020) 1080p BluRay":         "A.Movie (2020) 1080p BluRay",
	}
	for in, want := range cases {
		if got := simpleMovieTitle(in); got != want {
			t.Errorf("simpleMovieTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePTTDropsParserAfterPanic(t *testing.T) {
	oldRun, oldRelease := pttRun, pttRelease
	defer func() { pttRun, pttRelease = oldRun, oldRelease }()
	released := 0
	pttRelease = func(*ptt.Parser) { released++ }
	if info := parsePTT("Movie.Name.2020.1080p.BluRay.x264-GRP"); info == nil || released != 1 {
		t.Fatalf("normal parse: info %v, released %d", info, released)
	}
	pttRun = func(*ptt.Parser, string) *ptt.TorrentInfo { panic("boom") }
	if info := parsePTT("Movie.Name.2020"); info != nil {
		t.Errorf("a panicking parse must return nil, got %+v", info)
	}
	if released != 1 {
		t.Errorf("a parser that panicked must not go back into the pool")
	}
}

// A tag after "-GROUP" (EZTV's "[EZTVx.to]") does not replace the group.
func TestReleaseGroupBeforeTrailingTag(t *testing.T) {
	for title, want := range map[string]string{
		"Show.S01E01.1080p.WEB.h264-ETHEL[EZTVx.to].mkv":     "ETHEL",
		"Show S01E01 1080p WEB H264-NHTFS [eztvx.to]":        "NHTFS",
		"Show.S01E01.1080p.WEB.h264-ETHEL[TorrentCouch.net]": "ETHEL",
		"Show.S01E01.1080p.WEB-DL [rartv]":                   "",
	} {
		for _, app := range []App{AppSonarr, AppRadarr} {
			if got := Parse(app, title).ReleaseGroup; got != want {
				t.Errorf("%s %q: group %q, want %q", app, title, got, want)
			}
		}
	}
}

// A short suffix after "-GROUP" does not hide the group, as in *arr; tags
// after a dash in the title are no group.
func TestReleaseGroupBeforeDotSuffix(t *testing.T) {
	for title, want := range map[string]string{
		"House.of.the.Dragon.S02E01.ITA.ENG.1080p.MAX.WEB-DL.DDP5.1.H.264-MeM.GP": "MeM",
		"Show.S01E01.1080p.WEB.H264-NTb.1":                                        "NTb",
		"Show.S01E01.1080p.BluRay.x264-GROUP.REPACK":                              "GROUP",
		"Show.S01E01.1080p.WEB.H264-GROUP.v2":                                     "GROUP",
		"Show.S01E01.1080p.WEB.H264-Group.German":                                 "Group",
		"Show.S01E01.1080p.WEB-DL.DDP5.1.H.264":                                   "",
		"Spider-Man.2002.1080p.BluRay.x264":                                       "",
	} {
		for _, app := range []App{AppSonarr, AppRadarr} {
			if got := Parse(app, title).ReleaseGroup; got != want {
				t.Errorf("%s %q: group %q, want %q", app, title, got, want)
			}
		}
	}
}

// Only a release parsed as German alone gets DL or ML languages; DL wins.
func TestGermanDualAndMultiLanguage(t *testing.T) {
	for title, want := range map[string][]Language{
		"Movie.2023.German.ML.1080p.BluRay.x264-Group":        {LanguageGerman, LanguageEnglish, LanguageOriginal},
		"Movie.2023.German.DL.ML.2160p.UHD.BluRay.x265-Group": {LanguageGerman, LanguageOriginal},
		"Movie.2023.German.English.ML.1080p.WEB.H264-Group":   {LanguageGerman, LanguageEnglish},
	} {
		got := Parse(AppRadarr, title).Languages
		if len(got) != len(want) {
			t.Errorf("%q: languages %v, want %v", title, got, want)
			continue
		}
		for _, l := range want {
			if !containsLanguage(got, l) {
				t.Errorf("%q: languages %v, want %v", title, got, want)
				break
			}
		}
	}
}

// Only a separated "MULTi" tag adds the profile's multi languages; subtitle
// tags such as "[Multi-Subs]" do not (*arr's MultiRegex).
func TestMultiTagNeedsSeparators(t *testing.T) {
	for title, want := range map[string]bool{
		"[Judas] Movie (2024) [BD 1080p][HEVC x265 10bit][Dual-Audio][Multi-Subs]": false,
		"Movie.2024.1080p.BluRay.x265.10bit.Multi-Subs-GROUP":                      false,
		"Movie (2024) (1080p BluRay x265 10bit Multi-Sub)":                         false,
		"Show.S01E01.MULTi.1080p.WEB.h264-GRP":                                     true,
		"Movie.2024.MULTi.SUBS.1080p.WEB.h264-GRP":                                 true,
	} {
		if got := reMultiTag.MatchString(title); got != want {
			t.Errorf("%q: MULTi = %v, want %v", title, got, want)
		}
	}
}

// Radarr's exact exception groups win, the last one named (Radarr's
// ReleaseGroupParserFixture); Sonarr keeps the encoder's name.
func TestRadarrExactExceptionGroups(t *testing.T) {
	for title, want := range map[string]string{
		"Movie Name (2003) (2160p BluRay X265 HEVC 10bit HDR AAC 7.1 Tigole) [QxR]": "QxR",
		"Movie Name (2003) (1080p BluRay x265 HEVC 10bit EAC3 5 1 RZeroX) QxR":      "QxR",
		"Movie.Name.2019.1080p.BluRay.x264-YIFY":                                    "YIFY",
	} {
		if got := Parse(AppRadarr, title).ReleaseGroup; got != want {
			t.Errorf("radarr %q: group %q, want %q", title, got, want)
		}
	}
	if got := Parse(AppSonarr, "Show Name (2003) S01E01 (1080p BluRay x265 HEVC 10bit AAC 5.1 Tigole) [QxR]").ReleaseGroup; got != "Tigole" {
		t.Errorf("sonarr: group %q, want Tigole", got)
	}
}

// "WEB" before a resolution (Sonarr) and "WEB.DD5.1" (both apps) are WEB-DL.
func TestBareWebLayouts(t *testing.T) {
	for _, tc := range []struct {
		app   App
		title string
	}{
		{AppSonarr, "Show.S01E01.WEB.1080p.H264-GRP"},
		{AppSonarr, "Show.S01E01.1080p.ATVP.WEB.DD5.1.H.264-GRP"},
		{AppRadarr, "Movie.2023.1080p.ATVP.WEB.DD5.1.H.264-GRP"},
	} {
		if got := Parse(tc.app, tc.title).Quality.Name; got != "WEBDL-1080p" {
			t.Errorf("%s %q: quality %s, want WEBDL-1080p", tc.app, tc.title, got)
		}
	}
}

// "-A-B" is no hyphenated group when "A" ends a tag *arr excludes there.
func TestDoubleTailExcludedTags(t *testing.T) {
	for title, want := range map[string]string{
		"Movie.2021.2160p.UHD.BluRay.REMUX.HDR.HEVC.DTS-X-FGT": "FGT",
		"Movie.2021.1080p.BluRay.x265.10-bit-GRP":              "GRP",
		"Movie.2021.1080p.BluRay.x264-ENG-GRP":                 "GRP",
		"Show.S01E01.720p.HDTV.x264-ACC-ROLLED":                "ACC-ROLLED",
	} {
		for _, app := range []App{AppSonarr, AppRadarr} {
			if got := Parse(app, title).ReleaseGroup; got != want {
				t.Errorf("%s %q: group %q, want %q", app, title, got, want)
			}
		}
	}
}

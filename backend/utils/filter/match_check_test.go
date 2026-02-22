package filter

import (
	"fmt"
	"testing"
)

func TestRealWorldNyaaTitleMatching(t *testing.T) {
	pref, nonPref, _ := GetAnimeLanguageTerms("eng")
	prefCompiled := CompileTerms(pref)
	nonPrefCompiled := CompileTerms(nonPref)

	type testCase struct {
		title    string
		category string
		wantPref bool // should match preferred
		wantNon  bool // should match non-preferred
	}

	cases := []testCase{
		// ──── DUAL AUDIO (space-separated) ────
		{"[Kaleido-subs] Fate strange Fake - 08 (S01E08) - (WEB 1080p HEVC x265 10-bit AAC 2.0) [Dual Audio] [E4C499A0]", "dual audio", true, false},
		{"[Trix] You and I Are Polar Opposites S01E07 [WEBRip 1080p AV1 Opus] (Dual Audio, Multi Subs, VOSTFR)", "dual audio", true, false},
		{"Takopis Original Sin S01 Season 1 2025 1080p BDRip Dual Audio 10bits x265-Rapta", "dual audio", true, false},
		{"[Exiled-Destiny] Spirited Away (Movie) [Dual Audio]", "dual audio movie", true, false},
		{"[Anime Time] Dragon Ball Z Complete Series [SoM] [DVD] [Dual Audio] [480p]", "dual audio batch", true, false},
		{"[Anime Time] One Punch Man - Season 3 (Episodes 1-12) [1080p][HEVC 10bit x265][AAC][Dual-Audio] [Multi Sub] [Batch]", "dual-audio", true, false},

		// ──── DUAL-AUDIO (hyphenated — very common on Nyaa) ────
		{"[GetItTwisted] Gamers! S01 [BD 1080p HEVC Opus Dual-Audio]", "dual-audio", true, false},
		{"In the Clear Moonlit Dusk S01E05 1080p AMZN WEB-DL DUAL DDP2.0 H 264-VARYG (Uruwashi no Yoi no Tsuki, Dual-Audio, Multi-Subs)", "dual-audio", true, false},
		{"[Judas] CHIHAYAFURU (Seasons 1-3 + OVAs) [BD 1080p][HEVC x265 10bit][Dual-Audio][Eng-Subs]v2 (Batch)", "dual-audio batch", true, false},
		{"[Judas] Spy X Family (Season 03 [1080p][HEVC x265 10bit][Dual-Audio][Multi-Subs] (Batch)", "dual-audio batch", true, false},
		{"[Judas] Tensei Akujo no Kurorekishi (Season 01) [1080p][HEVC x265 10bit][Dual-Audio][Multi-Subs] (Batch)", "dual-audio batch", true, false},

		// ──── ENGLISH DUB ────
		{"[Yameii] Kaya-chan Isn't Scary - S01E07 [English Dub] [CR WEB-DL 1080p H264 AAC] [D4C5D197]", "english dub", true, false},
		{"[Yameii] Fate/strange Fake - S01E08 [English Dub] [CR WEB-DL 1080p H264 AAC] [5F050F01]", "english dub", true, false},
		{"In the Clear Moonlit Dusk - 05 | Uruwashi no Yoi no Tsuki [English Dub][1080p]", "english dub", true, false},
		{"[Yameii] TRIGUN STAMPEDE - S02E05 [English Dub] [CR WEB-DL 1080p H264 AAC]", "english dub", true, false},
		{"One Piece Episode 941-952 [English Dub][1080p][Funimation]", "english dub", true, false},
		{"[Koten_Gars] Rave Master {Ep. 1-12} [US.DVD][Hi10][480p][AC3] (English Dubbed)", "english dubbed", true, false},
		{"Bottom-Tier Character Tomozaki - S02E12 [English Dub][Funimation][1080p]", "english dub", true, false},

		// ──── MULTI AUDIO / MULTI-AUDIO ────
		{"You and I Are Polar Opposites S01E07 1080p CR WEB-DL MULTi AAC2.0 H 264-VARYG (Multi-Audio, Multi-Subs)", "multi-audio", true, false},
		{"[yolerejiju] Spirited Away 2001 (BD Remux 1080p FLAC H.264) [Multi Audio]", "multi audio movie", true, false},
		{"[DemiHuman] HAIKYU!! The Dumpster Battle (2024) (BD Remux 1080p AVC TrueHD Atmos 7.1) [Dual-Audio, Multi-Audio, Multi-Sub]", "multi-audio movie", true, false},

		// ──── MULTI-LANGUAGE BATCH with all dubs listed ────
		// Matches BOTH preferred (multi audio) and non-preferred (german/french/etc dub)
		// This is correct: boost outweighs derank in the ranking function, and these releases ARE desirable
		{"[ToonsHub] Hells Paradise S01 1080p AMZN WEB-DL MULTi DDP2.0 H.264 (Multi Audio, Multi-Subs) [BATCH] (English, Portuguese, Spanish, German, French, Italian Dubs)", "multi-lang", true, true},
		// This lists languages as "English, German, French..." with "Dubs" at the end.
		// "german dub" is NOT a substring here (it's "German, French...Dubs"), so no derank — correct.
		// The Multi-Audio tag still boosts it.
		{"[ToonsHub] DAN DA DAN S01E03 1080p CR WEB-DL AAC2.0 H.264 (Multi-Audio, Multi-Subs) (English, German, French, Portuguese, Spanish, Italian, Tamil, Telugu, Hindi, Thai, Indonesian Dubs)", "multi-lang", true, false},
		{"Kimetsu no Yaiba - S05E01 - MULTi 1080p WEB x264 -NanDesuKa (CR).mkv (Japanese, Spanish, Hindi, English, Tamil, French, Telugu, Portuguese, German Dubs)", "multi-lang", false, true},

		// ──── GERMAN DUB (non-preferred for eng users) ────
		{"Canaan S01 German Dubbed DL AC3 1080p BluRay x264-Duke21", "german dub", false, true},
		{"Frieren Beyond Journeys End S01  German Dubbed DL AAC 1080p WEB h264-snk", "german dub", false, true},
		{"Classroom of the Elite S01 German Dubbed DL.AAC 1080p BluRay 10bit.x265-shw", "german dub", false, true},

		// ──── FRENCH (non-preferred for eng users) ────
		{"Le Voyage de Chihiro (2001) MULTi-FRENCH/VOSTFR 1080p 10bits BluRay x265 AC3 5.1 (Spirited Away)", "french", false, false},
		// Note: "MULTi-FRENCH" doesn't contain "french dub" so it won't derank — this is fine,
		// it's a French sub release not a dub-only release

		// ──── SPANISH (non-preferred for eng users) ────
		{"Sougen no Shoujo Laura | Laura, Girl of the Prairies (Spanish Dubbed)", "spanish dub", false, true},

		// ──── JAPANESE DUBBED (not matched for eng — correct, niche pattern) ────
		{"Meng Qi Shi Shen / Cinderella Chef - 01 (Japanese Dubbed) [1080p AMZN WEB-DL AVC E-AC3]", "jpn dubbed", false, false},

		// ──── SUB-ONLY releases (no match — correct) ────
		{"[SubsPlease] Attack on Titan - 01 [1080p]", "sub-only", false, false},
		{"[F-R] Dragon Ball Super 001-131 (BD 1080p)", "sub-only", false, false},
		{"[Naruto-Kun.Hu] Naruto - 107 [1080p].mkv", "sub-only", false, false},
		{"[Judas] Digimon Beatbreak - S01E20 [1080p][HEVC x265 10bit][Eng-Subs] (Weekly)", "sub-only", false, false},
		{"[Erai-raws] Kaya-chan wa Kowakunai - 07 [1080p CR WEB-DL AVC AAC][MultiSub][A60A6BD3]", "sub-only", false, false},
		{"[DKB] Shangri-La Frontier - S02 [1080p][HEVC x265 10bit][Multi-Subs] (inofficial batch)", "sub-only", false, false},

		// ──── STANDALONE DUBBED (no language prefix — no match, ambiguous) ────
		{"[geckyzz] Chu-Bra!! - S01E03 [OVEIL.WEB-DL 1080P AVC, AAC DUBBED][E36D528E]", "dubbed only", false, false},
		{"[Exiled-Destiny] Zatch Bell (Dubbed)", "dubbed only", false, false},

		// ──── DUAL standalone without "audio" (scene tag — no match, ambiguous) ────
		{"Kaya chan Isnt Scary S01E07 1080p CR WEB-DL DUAL AAC2.0 H 264-VARYG", "DUAL only", false, false},

		// ──── RAW / NO LANGUAGE TAG ────
		{"[Koi-Raws] ONE PIECE FILM RED (CX 1920x1080 x264 AAC).mkv", "raw", false, false},
		{"One.Piece.Film.Red.2022.2160p.UHD.Blu-ray.Remux.DV.HDR.HEVC.TrueHD.Atmos.7.1-RUDY", "scene release", false, false},

		// ──── VOSTFR (French sub, not dub — no match for eng) ────
		{"Blue Orchestra S02E20 VOSTFR 1080p WEB x264 AAC -Tsundere-Raws (ADN)", "vostfr", false, false},
		{"Kaya-chan Isn't Scary S01E07 VOSTFR 1080p WEB x264 AAC -Tsundere-Raws (CR)", "vostfr", false, false},

		// ──── LATINO (not matched for eng — correct) ────
		{"Dr. Slump 1997 [480] Español Latino", "latino", false, false},
		{"[ZigZag] Beyblade X S01 [1080p AO WEB-DL] [Doblaje Latino]", "latino", false, false},
	}

	fmt.Printf("Preferred terms: %v\n", pref)
	fmt.Printf("Non-preferred terms: %v\n\n", nonPref)

	for _, tc := range cases {
		matchPref := MatchesAnyTerm(tc.title, prefCompiled)
		matchNonPref := MatchesAnyTerm(tc.title, nonPrefCompiled)

		status := "  "
		if matchPref {
			status = "BOOST"
		}
		if matchNonPref {
			status = "DERANK"
		}
		if matchPref && matchNonPref {
			status = "BOTH!!"
		}
		fmt.Printf("[%-6s] [%-12s] %s\n", status, tc.category, tc.title)

		if matchPref != tc.wantPref {
			t.Errorf("preferred mismatch for %q: got=%v want=%v", tc.title, matchPref, tc.wantPref)
		}
		if matchNonPref != tc.wantNon {
			t.Errorf("non-preferred mismatch for %q: got=%v want=%v", tc.title, matchNonPref, tc.wantNon)
		}
	}
}

func TestItalianAnimeLanguageMatching(t *testing.T) {
	pref, nonPref, _ := GetAnimeLanguageTerms("ita")
	prefCompiled := CompileTerms(pref)
	nonPrefCompiled := CompileTerms(nonPref)

	type testCase struct {
		title    string
		category string
		wantPref bool
		wantNon  bool
	}

	cases := []testCase{
		// Actual titles from Record of Ragnarok search
		{"[DB] Shuumatsu no Walküre | Record of Ragnarok [Dual Audio 10bit BD1080p][HEVC-x265]", "dual audio", true, false},
		{"[Anime Time] Record Of Ragnarok (Season 01+ Season 02) [NF] [Dual Audio] [1080p][HEVC 10bit x265][AAC][Multi Sub]", "dual audio", true, false},
		{"Record of Ragnarok S01 1080p NF WEB-DL AAC2 0 H 264-DucksterPS", "no lang tag", false, false},
		{"Record of Ragnarok S01 JAPANESE 1080p WEBRip x265-RARBG", "jpn only", false, false},
		{"Record of Ragnarok S01 1080p Dual Audio WEBRip 10 bits DD+ x265-EMBER", "dual audio", true, false},
		{"[GST] Record of Ragnarok (Shuumatsu no Valkyrie) Season 1 - (01-12) [1080p][Dual-Audio][Multi-Subs]", "dual-audio", true, false},
		{"Record of Ragnarok S01 DUBBED WEBRip x265-ION265[eztv re]", "dubbed only", false, false},
		{"Record of Ragnarok S01E01 1080p WEB H264-SUGOI", "no lang tag", false, false},

		// Italian-specific releases (should BOOST)
		{"[ITA] Record of Ragnarok S01 Italian Dub 1080p WEB x264", "italian dub", true, false},
		{"Record of Ragnarok S01 Italian Dubbed 1080p BluRay x265", "italian dubbed", true, false},
		{"Frieren Beyond Journeys End S01 Italian Dubbed DL AAC 1080p WEB h264", "italian dubbed", true, false},

		// Multi-audio with Italian (should BOOST via multi audio/dual audio)
		{"Record of Ragnarok S01 Multi Audio ITA ENG JPN 1080p", "multi audio", true, false},
		{"[ToonsHub] Attack on Titan S01 (Multi Audio, Multi-Subs) (English, German, French, Portuguese, Spanish, Italian Dubs)", "multi audio", true, false},

		// English dub releases (should DERANK for Italian pref)
		{"[Yameii] Record of Ragnarok - S01E01 [English Dub] [CR WEB-DL 1080p]", "english dub", false, true},
		{"Record of Ragnarok S01E01 English Dubbed 1080p WEB", "english dub", false, true},

		// German dub (should DERANK)
		{"Canaan S01 German Dubbed DL AC3 1080p BluRay x264-Duke21", "german dub", false, true},

		// French dub (should DERANK)
		{"Frieren S01 French Dub 1080p WEB-DL", "french dub", false, true},

		// Sub-only (no match either way)
		{"[SubsPlease] Attack on Titan - 01 [1080p]", "sub-only", false, false},
		{"[Erai-raws] Kaya-chan wa Kowakunai - 07 [1080p CR WEB-DL AVC AAC][MultiSub]", "sub-only", false, false},
	}

	fmt.Printf("Italian preferred: %v\n", pref)
	fmt.Printf("Italian non-preferred: %v\n\n", nonPref)

	for _, tc := range cases {
		matchPref := MatchesAnyTerm(tc.title, prefCompiled)
		matchNonPref := MatchesAnyTerm(tc.title, nonPrefCompiled)

		status := "      "
		if matchPref {
			status = "BOOST "
		}
		if matchNonPref {
			status = "DERANK"
		}
		if matchPref && matchNonPref {
			status = "BOTH!!"
		}
		fmt.Printf("[%s] [%-14s] %s\n", status, tc.category, tc.title)

		if matchPref != tc.wantPref {
			t.Errorf("preferred mismatch for %q: got=%v want=%v", tc.title, matchPref, tc.wantPref)
		}
		if matchNonPref != tc.wantNon {
			t.Errorf("non-preferred mismatch for %q: got=%v want=%v", tc.title, matchNonPref, tc.wantNon)
		}
	}
}

func TestFalsePositives_IncidentalTerms(t *testing.T) {
	// Verify that titles containing "raw", "dual", language words, etc.
	// incidentally (not as audio/language tags) do NOT trigger ranking.

	type langCase struct {
		lang  string
		title string
		desc  string
		pref  bool
		non   bool
	}

	cases := []langCase{
		// ──── "raw" as part of group names / other words ────
		// "[Koi-Raws]" — group name, "Raws" != "raw" at word boundary
		{"eng", "[Koi-Raws] ONE PIECE FILM RED (CX 1920x1080 x264 AAC).mkv", "group name Koi-Raws", false, false},
		{"eng", "[Erai-raws] Kaya-chan wa Kowakunai - 07 [1080p]", "group name Erai-raws", false, false},
		{"eng", "[EMBER] Straw Hat Pirates S01 [1080p][HEVC x265]", "straw in title", false, false},
		{"eng", "[SubsPlease] The Crawling City - 01 [1080p]", "crawling in title", false, false},
		{"eng", "[Judas] Drawing Sword - S01E01 [1080p][HEVC x265 10bit]", "drawing in title", false, false},
		// "Tsundere-Raws" group name
		{"eng", "Blue Orchestra S02E20 VOSTFR 1080p WEB x264 AAC -Tsundere-Raws (ADN)", "group Tsundere-Raws", false, false},

		// ──── "dual" without "audio" ────
		// "Dual!" is a real anime title
		{"eng", "[SubsPlease] Dual! Parallel Lunalun Monogatari - 01 [1080p]", "anime title Dual!", false, false},
		// DUAL as scene tag without "audio" should not match
		{"eng", "Kaya chan Isnt Scary S01E07 1080p CR WEB-DL DUAL AAC2.0 H 264-VARYG", "DUAL scene tag", false, false},

		// ──── Language words in character/title names ────
		// "German" as part of title, not a dub tag
		{"eng", "[SubsPlease] The German Boy and the French Girl - 01 [1080p]", "german in title", false, false},
		// "Italian" as part of title
		{"eng", "[SubsPlease] The Italian Chef Reincarnated - 01 [1080p]", "italian in title", false, false},

		// ──── JPN preferred terms: "raw" and "jpn" should use word boundaries ────
		{"jpn", "[Koi-Raws] ONE PIECE FILM RED (CX 1920x1080 x264 AAC).mkv", "Raws group jpn", false, false},
		{"jpn", "[EMBER] Straw Hat Pirates S01 [1080p][HEVC x265]", "straw jpn", false, false},
		{"jpn", "[SubsPlease] The Crawling City - 01 [1080p]", "crawling jpn", false, false},
		{"jpn", "[Judas] Drawing Sword - S01E01 [1080p][HEVC x265 10bit]", "drawing jpn", false, false},
		// But standalone "raw" SHOULD match for jpn preferred
		{"jpn", "[Leopard-Raws] One Piece - RAW - 1120 [1280x720 DTV x264 AAC]", "RAW tag jpn", true, false},
		// "jpn" should not match inside longer words
		{"jpn", "[SubsPlease] JPNG Compression Test [1080p]", "jpng not jpn", false, false},
	}

	for _, tc := range cases {
		pref, nonPref, _ := GetAnimeLanguageTerms(tc.lang)
		prefC := CompileTerms(pref)
		nonC := CompileTerms(nonPref)

		gotPref := MatchesAnyTerm(tc.title, prefC)
		gotNon := MatchesAnyTerm(tc.title, nonC)

		if gotPref != tc.pref {
			t.Errorf("[%s] %s: preferred got=%v want=%v\n  title: %q", tc.lang, tc.desc, gotPref, tc.pref, tc.title)
		}
		if gotNon != tc.non {
			t.Errorf("[%s] %s: non-preferred got=%v want=%v\n  title: %q", tc.lang, tc.desc, gotNon, tc.non, tc.title)
		}
	}
}

func TestFalsePositives_FilterOut(t *testing.T) {
	// Verify filter-out \braw\b regex does NOT catch group names or partial matches.
	type testCase struct {
		title      string
		desc       string
		wantFilter bool
	}

	cases := []testCase{
		// Should NOT be filtered out (incidental "raw" substrings)
		{"[Koi-Raws] ONE PIECE FILM RED (CX 1920x1080 x264 AAC).mkv", "Koi-Raws group", false},
		{"[Erai-raws] Kaya-chan wa Kowakunai - 07 [1080p]", "Erai-raws group", false},
		{"Blue Orchestra S02E20 VOSTFR 1080p -Tsundere-Raws (ADN)", "Tsundere-Raws group", false},
		{"[EMBER] Straw Hat Pirates S01 [1080p][HEVC x265]", "straw in title", false},
		{"[SubsPlease] The Crawling City - 01 [1080p]", "crawling", false},
		{"[Judas] Drawing Sword - S01E01 [1080p]", "drawing", false},
		{"Crawford S01E01 1080p WEB-DL x264", "crawford name", false},

		// SHOULD be filtered out (actual raw tags)
		{"[Leopard-Raws] One Piece - RAW - 1120 [1280x720 DTV x264 AAC]", "RAW tag", true},
		{"[ohys-raws] Naruto Shippuuden - 500 (raw).mkv", "raw in parens", true},
		{"Bleach 366 raw [720p].mkv", "raw standalone", true},
	}

	// Use eng filter-out terms (all non-jpn languages have the same filter-out)
	_, _, filterOut := GetAnimeLanguageTerms("eng")
	filterCompiled := CompileTerms(filterOut)

	for _, tc := range cases {
		got := MatchesAnyTerm(tc.title, filterCompiled)
		if got != tc.wantFilter {
			t.Errorf("filter-out %s: got=%v want=%v\n  title: %q", tc.desc, got, tc.wantFilter, tc.title)
		}
	}
}

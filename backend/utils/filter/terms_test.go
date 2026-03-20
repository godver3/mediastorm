package filter

import (
	"testing"
)

func TestCompileTerms_PlainSubstring(t *testing.T) {
	terms := CompileTerms([]string{"dub", "cam"})
	if len(terms) != 2 {
		t.Fatalf("expected 2 compiled terms, got %d", len(terms))
	}
	if terms[0].regex != nil {
		t.Error("expected plain term, got regex")
	}
	if terms[0].plain != "dub" {
		t.Errorf("expected plain=%q, got %q", "dub", terms[0].plain)
	}
}

func TestCompileTerms_Regex(t *testing.T) {
	terms := CompileTerms([]string{`/\bDUB\b/`})
	if len(terms) != 1 {
		t.Fatalf("expected 1 compiled term, got %d", len(terms))
	}
	if terms[0].regex == nil {
		t.Fatal("expected regex term, got plain")
	}
}

func TestCompileTerms_InvalidRegexFallback(t *testing.T) {
	terms := CompileTerms([]string{"/invalid[/"})
	if len(terms) != 1 {
		t.Fatalf("expected 1 compiled term, got %d", len(terms))
	}
	if terms[0].regex != nil {
		t.Error("expected plain fallback for invalid regex, got regex")
	}
	// Falls back to the whole string including slashes
	if terms[0].plain != "/invalid[/" {
		t.Errorf("expected plain=%q, got %q", "/invalid[/", terms[0].plain)
	}
}

func TestCompileTerms_EmptyAndWhitespace(t *testing.T) {
	terms := CompileTerms([]string{"", "  ", "\t"})
	if len(terms) != 0 {
		t.Fatalf("expected 0 compiled terms for empty/whitespace, got %d", len(terms))
	}
}

func TestCompileTerms_SingleSlash(t *testing.T) {
	// A term like "/x" (no closing slash) or "/" should be plain
	terms := CompileTerms([]string{"/x", "/"})
	for _, term := range terms {
		if term.regex != nil {
			t.Error("short slash term should be plain, not regex")
		}
	}
}

func TestMatchesAnyTerm_PlainSubstring(t *testing.T) {
	terms := CompileTerms([]string{"dub"})

	if !MatchesAnyTerm("Movie.DUBBED.2024", terms) {
		t.Error("plain 'dub' should match 'DUBBED'")
	}
	if !MatchesAnyTerm("redub something", terms) {
		t.Error("plain 'dub' should match 'redub'")
	}
	if MatchesAnyTerm("Movie.2024.1080p", terms) {
		t.Error("plain 'dub' should not match title without 'dub'")
	}
}

func TestMatchesAnyTerm_RegexWordBoundary(t *testing.T) {
	terms := CompileTerms([]string{`/\bDUB\b/`})

	if !MatchesAnyTerm("Movie DUB 2024", terms) {
		t.Error("regex should match standalone DUB")
	}
	if !MatchesAnyTerm("Movie.DUB.2024", terms) {
		t.Error("regex should match DUB at word boundary (dot)")
	}
	if MatchesAnyTerm("Movie.DUBBED.2024", terms) {
		t.Error("regex \\bDUB\\b should NOT match DUBBED")
	}
}

func TestMatchesAnyTerm_RegexCharClass(t *testing.T) {
	terms := CompileTerms([]string{`/x26[45]/`})

	if !MatchesAnyTerm("Movie.x264.1080p", terms) {
		t.Error("regex should match x264")
	}
	if !MatchesAnyTerm("Movie.X265.2024", terms) {
		t.Error("regex should match X265 (case-insensitive)")
	}
	if MatchesAnyTerm("Movie.x266.2024", terms) {
		t.Error("regex should not match x266")
	}
}

func TestMatchesAnyTerm_CaseInsensitive(t *testing.T) {
	// Plain substring is case-insensitive
	plainTerms := CompileTerms([]string{"CAM"})
	if !MatchesAnyTerm("movie.cam.2024", plainTerms) {
		t.Error("plain match should be case-insensitive")
	}

	// Regex is also case-insensitive ((?i) flag)
	regexTerms := CompileTerms([]string{`/\bcam\b/`})
	if !MatchesAnyTerm("Movie.CAM.2024", regexTerms) {
		t.Error("regex match should be case-insensitive")
	}
}

func TestMatchesAnyTerm_EmptyTerms(t *testing.T) {
	if MatchesAnyTerm("any title", nil) {
		t.Error("nil terms should return false")
	}
	if MatchesAnyTerm("any title", []CompiledTerm{}) {
		t.Error("empty terms should return false")
	}
}

func TestMatchesAnyTerm_EmptyTitle(t *testing.T) {
	terms := CompileTerms([]string{"dub"})
	if MatchesAnyTerm("", terms) {
		t.Error("empty title should not match")
	}
}

func TestMatchesAnyTerm_MultipleTerms(t *testing.T) {
	terms := CompileTerms([]string{"cam", `/\bDUB\b/`, "telesync"})

	if !MatchesAnyTerm("Movie.CAM.2024", terms) {
		t.Error("should match first plain term")
	}
	if !MatchesAnyTerm("Movie DUB 2024", terms) {
		t.Error("should match regex term")
	}
	if !MatchesAnyTerm("Movie.TELESYNC.2024", terms) {
		t.Error("should match third plain term")
	}
	if MatchesAnyTerm("Movie.1080p.2024", terms) {
		t.Error("should not match unrelated title")
	}
}

// frenchGroupRegex is the real-world regex a user reported not working for prioritization.
// It matches French release group names preceded by a delimiter character.
const frenchGroupRegex = `/^.*[.\-\[\]\(\) ](?:4FR|Aisha|AAjango|ALFA|ALLDAYiN|AMB3R|Amen|ANMWR|ANGERFIST007|ARK01|AZAZE|AZR|AXLBEN|BATGirl|BANKAi|BAWLS|BDHD|BENDER_37|BLACKPANTERS|BLACKANGEL|BLAP|BOOLZ|BOUC|BODIE|BRINK|BTTHD|BtRs|BTT|BULITT|BULiTT|BURNToDISC|BYHGO|CARAPILS|CHARTAIR|CHERRYcoke|CHOCO|CiELOS|CiNEMA|CMBHD|CoRa|COUAC|CRYPT0|CLASSICS|CYSTEiNE|DAM|D4KiD|DEAL|DeSs|DiEBEX|Djona12|DREAM|DUFF|DUPLI|DUSS|DUSTiN|DYDHZO|DWS|E\.T|ELCRACKITO|ENJOi|ESPER|EUBDS|EXTREME|FCK|FERVEX|FGT|FiDELiO|FiDO|FLOP|ForceBleue|FoX|FREAMON|FRATERNiTY|FRiES|FROSTIES|FRiENDS|FULLSiZE|FUTiL|FW|FWDHD|FTMVHD|GASMAK|GHZ|GHoST|GHOULS|GHT|GiMBAP|GLiMMER|GLaDOS|GOLDEN|Goldenyann|GOATLOVE|GUACAMOLE|GWEN|Hanami|H4S5S|HD2|HDKING|HDRA|HEAVYWEIGHT|HERC|HiggsBoson|HiRoSHiMa|HYBRiS|HyDe|HYO|iDySoNaP|JAQC|J4CK|JiHEFF|JMT|JP48|JoKeR|JRabbit|JUSTICELEAGUE|JuRDeN|K7|KAF|KAZETV|KBNawak|KENOBi3838|Kaoru111|KFL|LACTEL|LAOZI|LAZARUS|LEGdna|LEON|LiBERTAD|LiHDL|LOOKAMI|LOFiDEL|LOST|LOWIMDB|LUCKY|LYPSG|LazerTeam|MAGiCAL|MANGACiTY|MARBLECAKE|MAXAGAZ|Maxadonf|MAXiBENoul|McNULTY|MCSwi|MELBA|MELLOWMAN|MiND|MOONLY|MORELAND|MOO|MUNSTER|MULTIVISION|MUSTANG|MUxHD|Mad\.Darks27|MAN OF STYLE|mHDgz|MrS|NGRP|NERDHD|NERO|NONE|NOEX|NTK|NRZ|OBI|OBSTACLE|OKENEDET|OOHLALA|ONLY|ONLYMOViE|OVD|OXTORRENT|OZEF|PARISTOCAT|PATOMiEL|PATOPESTO|PEPiTE|P4TRi0T|PCH|PiNKPANTERS|PICKLES|PKPTRS|POP|POURMOi|PopHD|PREM|PROPJOE|PURE|PUREWASTEOFBW|PSY4|PuNiSHeR03|QC63|QUALiTY|QUEBEC63|QTZ|REBiRTH|R3MiX|RiFiFi|ROMKENT|Rough|RUDE|R2D2X|RYOTOX|S4LVE|SAFETY|SAL|SASHiMi|SESKAPILE|SESKAPiLE|SEL|SEiGHT|SENSei|SHADOW|SHEEEiT|Shamir|Sicario|SILVIODANTE|SLEEPINGFOREST|Slay3R|SN2P|SODAPOP|SPINE|SPOiLER|STARLIGHTER|STRINGERBELL|SUBZERO|SUNNY|SUNRiSE|SUPPLY|THESYNDiCATE|THiNK|THREESOME|TiMELiNE|TiNA|T3KASHi|TFA|TFR|TkHD|TheChirola|Tetine|TigersClassics|Torrent9|TRUNKDU92|TSuNaMi|TSC|toto70300|TyHD|ULSHD|ULYSSE|UKDHD|UKDTV|ukdhd|URY|USUNSKiLLED|USURY|VENUE|VFC|VFF|VoMiT|Wednesday29th|Wink|Winks|WMTorrent|XANTAR|Y4Y4|ZEKEY|ZEST|Z3US|ZinGy|ZiRCON|ZiT|Chris44|Cyrill2000|ludsfa|psy4|sozer|zza|SERQPH|Neostark|MULTiViSiON|bouba|CherryCoke|ulysse|OPTIMUM|Kenobi38|Dread|ShC23|SLM|PiCKLES|Ulysse|LITY|Plemo|RiPiT|BABA|BraD|AvALoN|SF2|AViTECH|PiCK|SACRiLEGE|HK31|MMCLX|iLynx|PREUMS|dooley|LDCS|MAMA|gismo65|WAlbator|b4dly|HiDeF|KLP33|MenZo|TRiCLUPRiD|LeSharkoiste|NOWiNHD|Hom3r|JiHeff|baron|ALCOOL|ROUGH|OkenEdet|AmineCamd|PiKES|chrisj42|D5T0|Temouche|Punisher694|ZezLD|GLUPS|Portos|HDZ|SANTACRUZ|c3r153|Burntodisc|SyND|QC|T0M|Tsundere-Raws|BOUBA|higgsboson|VFQ|VF2)(?:[.\-\[\]\(\) ].*)?$/`

func TestCompileTerms_LongFrenchGroupRegex(t *testing.T) {
	terms := CompileTerms([]string{frenchGroupRegex})
	if len(terms) != 1 {
		t.Fatalf("expected 1 compiled term, got %d", len(terms))
	}
	if terms[0].regex == nil {
		t.Fatal("expected regex term, got plain — regex failed to compile")
	}
}

func TestMatchesAnyTerm_FrenchGroupRegex(t *testing.T) {
	terms := CompileTerms([]string{frenchGroupRegex})
	if terms[0].regex == nil {
		t.Fatal("regex did not compile")
	}

	tests := []struct {
		title   string
		match   bool
		desc    string
	}{
		// Standard release formats: name-GROUP
		{"Movie.2024.1080p.BluRay.x264-ALFA", true, "hyphen delimited group ALFA"},
		{"Movie.2024.1080p.BluRay.x264-FGT", true, "hyphen delimited group FGT"},
		{"Movie.2024.1080p.BluRay.x265-VFF", true, "hyphen delimited group VFF"},
		{"Movie.2024.2160p.WEB-DL.DDP5.1.H.265-NERO", true, "hyphen delimited group NERO"},
		{"Movie.2024.FRENCH.1080p.BluRay.x264-LOST", true, "group LOST with FRENCH tag"},
		{"Movie.2024.MULTi.1080p.WEB-DL.H.265-EXTREME", true, "group EXTREME"},
		{"Movie.2024.1080p.BluRay.x264-DEAL", true, "group DEAL"},
		{"Movie.2024.FRENCH.720p.BluRay.x264-GOLDEN", true, "group GOLDEN"},

		// Bracket delimited groups
		{"Movie (2024) 1080p BluRay [ALFA]", true, "bracket delimited ALFA"},
		{"[FGT] Movie 2024 1080p", true, "bracket prefix group FGT"},

		// Dot delimited groups
		{"Movie.2024.1080p.ALFA.mkv", true, "dot delimited group ALFA"},
		{"Movie.2024.1080p.BluRay.PURE", true, "dot delimited trailing PURE"},

		// Space delimited groups
		{"Movie 2024 1080p BluRay ALFA", true, "space delimited group ALFA"},

		// Parenthesis delimited
		{"Movie.2024.1080p.(ALFA)", true, "parenthesis delimited ALFA"},

		// Case-insensitive matching ((?i) flag)
		{"Movie.2024.1080p.BluRay.x264-alfa", true, "lowercase group alfa"},
		{"Movie.2024.1080p.BluRay.x264-Fgt", true, "mixed case Fgt"},

		// Groups at end with file extension
		{"Movie.2024.1080p.BluRay.x264-ALFA.mkv", true, "group with .mkv extension"},
		{"Movie.2024.1080p.BluRay.x264-VFF.nzb", true, "group with .nzb extension"},

		// Should NOT match — legitimate groups/words not in list
		{"Movie.2024.1080p.BluRay.x264-SPARKS", false, "non-French group SPARKS"},
		{"Movie.2024.1080p.BluRay.x264-YIFY", false, "non-French group YIFY"},
		{"Movie.2024.1080p.BluRay.x264-RARBG", false, "non-French group RARBG"},
		{"Movie.2024.1080p.BluRay.x264-FraMeSToR", false, "non-French group FraMeSToR"},

		// Edge case: group name embedded in a word should NOT match (no delimiter)
		// Actually this regex uses .* greedy + delimiter char class, so embedded
		// names WILL match if preceded by any delimiter anywhere
		// e.g. "SUNNYDALE" contains "SUNNY" preceded by nothing at that position,
		// but the .* can match up to "." or other delimiters earlier

		// Should NOT match — no content at all
		{"", false, "empty title"},

		// Group name at very start with no preceding delimiter should NOT match
		// because regex requires [.\-\[\]\(\) ] before the group name
		{"ALFA", false, "bare group name with no delimiter"},
		{"ALFA.Movie.2024", false, "group at start with no preceding delimiter"},

		// Specific groups from different parts of the list
		{"Movie.2024.1080p-Tsundere-Raws", true, "group Tsundere-Raws"},
		{"Movie.2024.1080p-BOUBA", true, "group BOUBA"},
		{"Movie.2024.1080p-higgsboson", true, "group higgsboson"},
		{"Movie.2024.1080p-VFQ", true, "group VFQ"},
		{"Movie.2024.1080p-VF2", true, "group VF2 (last in list)"},
		{"Movie.2024.1080p-4FR", true, "group 4FR (first in list)"},
		{"Movie.2024.1080p.BluRay-QC", true, "group QC (short name)"},
		{"Movie.2024.1080p.BluRay-T0M", true, "group T0M"},
	}

	for _, tt := range tests {
		got := MatchesAnyTerm(tt.title, terms)
		if got != tt.match {
			t.Errorf("%s: MatchesAnyTerm(%q) = %v, want %v", tt.desc, tt.title, got, tt.match)
		}
	}
}

func TestMatchesAnyTerm_FrenchGroupRegex_AutoDetectAnchored(t *testing.T) {
	// Anchored regex (starts with ^) should auto-detect as regex even without /slashes/
	rawRegex := `^.*[.\-\[\]\(\) ](?:4FR|ALFA|VFF)(?:[.\-\[\]\(\) ].*)?$`
	terms := CompileTerms([]string{rawRegex})
	if terms[0].regex == nil {
		t.Fatal("anchored pattern should auto-detect as regex")
	}
	if !MatchesAnyTerm("Movie.2024.1080p.BluRay.x264-ALFA", terms) {
		t.Error("auto-detected regex should match")
	}

	// Also works with /slashes/ (explicit)
	slashedRegex := "/" + rawRegex + "/"
	termsSlashed := CompileTerms([]string{slashedRegex})
	if termsSlashed[0].regex == nil {
		t.Fatal("with slashes, should compile as regex")
	}
	if !MatchesAnyTerm("Movie.2024.1080p.BluRay.x264-ALFA", termsSlashed) {
		t.Error("regex with slashes should match")
	}
}

func TestCompileTerms_AutoDetectDollarAnchor(t *testing.T) {
	terms := CompileTerms([]string{`.*-ALFA$`})
	if terms[0].regex == nil {
		t.Fatal("$ anchored pattern should auto-detect as regex")
	}
	if !MatchesAnyTerm("Movie.2024.1080p-ALFA", terms) {
		t.Error("$ anchored regex should match")
	}
}

func TestCompileTerms_PlainTermNotAutoDetected(t *testing.T) {
	// Plain terms without anchors should remain plain substring matches
	terms := CompileTerms([]string{"ALFA"})
	if terms[0].regex != nil {
		t.Error("plain term without anchors should not become regex")
	}
}

func TestParseTermWeight(t *testing.T) {
	tests := []struct {
		raw    string
		term   string
		weight int
	}{
		{"DV", "DV", 1},
		{"DV=3", "DV", 3},
		{"REMUX=2", "REMUX", 2},
		{"/\\bHDR\\b/=5", "/\\bHDR\\b/", 5},
		{"DV=0", "DV=0", 1},     // invalid weight (<1) → default
		{"DV=abc", "DV=abc", 1}, // non-integer → default
		{"DV=", "DV=", 1},      // trailing = with no value
		{"=3", "=3", 1},        // no term part → keep as-is
		{"/foo=bar/", "/foo=bar/", 1}, // = inside regex, no trailing int
		{"/foo=bar/=2", "/foo=bar/", 2}, // = inside regex + valid weight
	}
	for _, tt := range tests {
		term, weight := ParseTermWeight(tt.raw)
		if term != tt.term || weight != tt.weight {
			t.Errorf("ParseTermWeight(%q) = (%q, %d), want (%q, %d)", tt.raw, term, weight, tt.term, tt.weight)
		}
	}
}

func TestCompileTerms_WithWeights(t *testing.T) {
	terms := CompileTerms([]string{"DV=3", "REMUX=2", "HDR"})
	if len(terms) != 3 {
		t.Fatalf("expected 3 terms, got %d", len(terms))
	}
	if terms[0].Weight != 3 {
		t.Errorf("DV=3 weight: got %d, want 3", terms[0].Weight)
	}
	if terms[1].Weight != 2 {
		t.Errorf("REMUX=2 weight: got %d, want 2", terms[1].Weight)
	}
	if terms[2].Weight != 1 {
		t.Errorf("HDR weight: got %d, want 1", terms[2].Weight)
	}
}

func TestCompileTerms_RegexWithWeight(t *testing.T) {
	terms := CompileTerms([]string{`/\bDUB\b/=4`})
	if len(terms) != 1 {
		t.Fatalf("expected 1 term, got %d", len(terms))
	}
	if terms[0].regex == nil {
		t.Fatal("expected regex term")
	}
	if terms[0].Weight != 4 {
		t.Errorf("weight: got %d, want 4", terms[0].Weight)
	}
}

func TestSumMatchedWeights(t *testing.T) {
	terms := CompileTerms([]string{"DV=3", "REMUX=2", "HDR"})

	// Title matching DV + REMUX + HDR → 3+2+1 = 6
	total, names := SumMatchedWeights("Movie.2024.2160p.REMUX.DV.HDR", terms)
	if total != 6 {
		t.Errorf("expected total weight 6, got %d", total)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 matched names, got %d", len(names))
	}

	// Title matching only DV → 3
	total, names = SumMatchedWeights("Movie.2024.DV.x265", terms)
	if total != 3 {
		t.Errorf("expected total weight 3, got %d", total)
	}
	if len(names) != 1 {
		t.Errorf("expected 1 matched name, got %d", len(names))
	}

	// No matches → 0
	total, names = SumMatchedWeights("Movie.2024.1080p.BluRay", terms)
	if total != 0 {
		t.Errorf("expected total weight 0, got %d", total)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 matched names, got %d", len(names))
	}
}

func TestSumMatchedWeights_DefaultWeight(t *testing.T) {
	// Terms without =N should default to weight 1
	terms := CompileTerms([]string{"DV", "REMUX"})
	total, _ := SumMatchedWeights("Movie.DV.REMUX", terms)
	if total != 2 {
		t.Errorf("expected total weight 2 (1+1), got %d", total)
	}
}

func TestMatchedTermWithWeight(t *testing.T) {
	terms := CompileTerms([]string{"DV=3", "REMUX=2"})

	name, w := MatchedTermWithWeight("Movie.DV.REMUX", terms)
	if name != "dv" || w != 3 {
		t.Errorf("MatchedTermWithWeight: got (%q, %d), want (\"dv\", 3)", name, w)
	}

	name, w = MatchedTermWithWeight("Movie.1080p", terms)
	if name != "" || w != 0 {
		t.Errorf("MatchedTermWithWeight no match: got (%q, %d), want (\"\", 0)", name, w)
	}
}

func TestMatchesAnyTerm_FrenchGroupRegex_FilterOutIntegration(t *testing.T) {
	// Simulate how this would be used as a filterOutTerm alongside other terms
	terms := CompileTerms([]string{
		"french",
		"truefrench",
		frenchGroupRegex,
	})

	// Titles that should be filtered by the regex (group name match)
	filteredByRegex := []string{
		"Gladiator.II.2024.MULTi.2160p.UHD.BluRay.x265-EXTREME",
		"The.Count.of.Monte.Cristo.2024.1080p.BluRay.x264-LOST",
		"Asterix.and.Obelix.2024.FRENCH.1080p.BluRay.x264-FGT",
	}
	for _, title := range filteredByRegex {
		if !MatchesAnyTerm(title, terms) {
			t.Errorf("expected filter match for %q", title)
		}
	}

	// Titles that should be filtered by plain "french" term
	filteredByPlain := []string{
		"Movie.2024.FRENCH.1080p.BluRay.x264-SPARKS",
		"Movie.2024.TrueFrench.1080p.WEB-DL-UnknownGroup",
	}
	for _, title := range filteredByPlain {
		if !MatchesAnyTerm(title, terms) {
			t.Errorf("expected filter match for %q (plain term)", title)
		}
	}

	// Titles that should NOT be filtered
	notFiltered := []string{
		"Movie.2024.1080p.BluRay.x264-SPARKS",
		"Movie.2024.2160p.WEB-DL.DDP5.1.H.265-FLUX",
		"Movie.2024.1080p.REMUX.AVC.DTS-HD.MA.5.1-FraMeSToR",
	}
	for _, title := range notFiltered {
		if MatchesAnyTerm(title, terms) {
			t.Errorf("did NOT expect filter match for %q", title)
		}
	}
}

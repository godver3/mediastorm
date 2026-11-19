package arr

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func spec(name, impl string, negate, required bool, fields map[string]any) Specification {
	return Specification{Name: name, Implementation: impl, Negate: negate, Required: required, Fields: fields}
}

func mustCompile(t *testing.T, app App, cf CustomFormat) *CompiledFormat {
	t.Helper()
	c, err := Compile(app, cf)
	if err != nil {
		t.Fatalf("compile %s: %v", cf.Name, err)
	}
	return c
}

func TestMatchingGroupsConditionsByImplementation(t *testing.T) {
	// Two title conditions (OR within the group) and a required resolution.
	cf := CustomFormat{Name: "x", Specifications: []Specification{
		spec("a", SpecReleaseTitle, false, false, map[string]any{"value": `\bfoo\b`}),
		spec("b", SpecReleaseTitle, false, false, map[string]any{"value": `\bbar\b`}),
		spec("1080", SpecResolution, false, true, map[string]any{"value": 1080}),
	}}
	c := mustCompile(t, AppSonarr, cf)
	cases := []struct {
		title string
		res   int
		want  bool
	}{
		{"Show.foo.1080p", 1080, true},
		{"Show.bar.1080p", 1080, true},
		{"Show.baz.1080p", 1080, false}, // no title condition matched
		{"Show.foo.720p", 720, false},   // required resolution failed
	}
	for _, tc := range cases {
		r := &Release{Title: tc.title, Quality: Quality{Resolution: tc.res}}
		if got := c.Matches(r); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.title, got, tc.want)
		}
	}
}

func TestRequiredConditionWithinGroup(t *testing.T) {
	// A required title condition plus an optional one: the required one must
	// match; the optional one alone is not enough.
	cf := CustomFormat{Name: "x", Specifications: []Specification{
		spec("must", SpecReleaseTitle, false, true, map[string]any{"value": `german`}),
		spec("opt", SpecReleaseTitle, false, false, map[string]any{"value": `\bDL\b`}),
	}}
	c := mustCompile(t, AppSonarr, cf)
	if !c.Matches(&Release{Title: "Show.German.1080p"}) {
		t.Error("required-only match should pass (one condition in the group matched)")
	}
	if c.Matches(&Release{Title: "Show.DL.1080p"}) {
		t.Error("optional match without the required one must fail")
	}
}

func TestNegateIsAppliedPerCondition(t *testing.T) {
	cf := CustomFormat{Name: "Not Remux", Specifications: []Specification{
		spec("h264", SpecReleaseTitle, false, true, map[string]any{"value": `[xh][ ._-]?264`}),
		spec("Not Remux", SpecSource, true, true, map[string]any{"value": json.Number("7")}),
	}}
	c := mustCompile(t, AppSonarr, cf)
	if !c.Matches(&Release{Title: "A.x264", Quality: Quality{Source: SonarrSourceWeb}}) {
		t.Error("expected match for non-remux x264")
	}
	if c.Matches(&Release{Title: "A.x264", Quality: Quality{Source: SonarrSourceBlurayRaw}}) {
		t.Error("negated source condition should reject remux")
	}
}

func TestLanguageOriginalAndExcept(t *testing.T) {
	original := CustomFormat{Name: "Original", Specifications: []Specification{
		spec("orig", SpecLanguage, false, true, map[string]any{"value": -2}),
	}}
	c := mustCompile(t, AppSonarr, original)
	r := &Release{Languages: []Language{"German", "English"}, OriginalLanguage: "English"}
	if !c.Matches(r) {
		t.Error("Original should resolve to the title's original language")
	}
	r = &Release{Languages: []Language{"German"}, OriginalLanguage: "English"}
	if c.Matches(r) {
		t.Error("German-only release does not carry the original language")
	}
	// Without metadata the literal Original marker is compared, like the
	// *arr parse endpoint does.
	if !c.Matches(&Release{Languages: []Language{"German", LanguageOriginal}}) {
		t.Error("Original marker should match without metadata")
	}

	except := CustomFormat{Name: "Not German", Specifications: []Specification{
		spec("x", SpecLanguage, false, true, map[string]any{"value": 4, "exceptLanguage": true}),
	}}
	ce := mustCompile(t, AppSonarr, except)
	if !ce.Matches(&Release{Languages: []Language{"German", "English"}}) {
		t.Error("exceptLanguage matches when any other language is present")
	}
	if ce.Matches(&Release{Languages: []Language{"German"}}) {
		t.Error("exceptLanguage must not match a German-only release")
	}
}

func TestLanguageIDsAreAppSpecific(t *testing.T) {
	// 26 is Arabic in Sonarr but Hindi in Radarr.
	if l, _ := LanguageFromID(AppSonarr, 26); l != "Arabic" {
		t.Errorf("sonarr 26 = %s", l)
	}
	if l, _ := LanguageFromID(AppRadarr, 26); l != "Hindi" {
		t.Errorf("radarr 26 = %s", l)
	}
	if id, ok := LanguageID(AppRadarr, "Arabic"); !ok || id != 31 {
		t.Errorf("radarr Arabic = %d %v", id, ok)
	}
}

func TestSizeAndReleaseTypeConditions(t *testing.T) {
	cf := CustomFormat{Name: "Small pack", Specifications: []Specification{
		spec("size", SpecSize, false, true, map[string]any{"min": 1, "max": 10}),
		spec("pack", SpecReleaseType, false, true, map[string]any{"value": 3}),
	}}
	c := mustCompile(t, AppSonarr, cf)
	gb := int64(1024 * 1024 * 1024)
	if !c.Matches(&Release{Size: 5 * gb, ReleaseType: ReleaseTypeSeasonPack}) {
		t.Error("5 GB season pack should match")
	}
	if c.Matches(&Release{Size: 1 * gb, ReleaseType: ReleaseTypeSeasonPack}) {
		t.Error("size min is exclusive")
	}
	if !c.Matches(&Release{Size: 10 * gb, ReleaseType: ReleaseTypeSeasonPack}) {
		t.Error("size max is inclusive")
	}
	if c.Matches(&Release{Size: 5 * gb, ReleaseType: ReleaseTypeSingleEpisode}) {
		t.Error("single episode must not match")
	}
}

func TestDotNetRegexFeatures(t *testing.T) {
	// TRaSH patterns use lookarounds that RE2 does not support.
	cf := CustomFormat{Name: "DV (w/o HDR fallback)", Specifications: []Specification{
		spec("dv", SpecReleaseTitle, false, true, map[string]any{"value": `\b(dv|dovi|dolby[ .]?v(ision)?)\b(?!.*\bhdr)`}),
	}}
	c := mustCompile(t, AppSonarr, cf)
	if !c.Matches(&Release{Title: "Show.S01E01.2160p.DV.WEB.h265-G"}) {
		t.Error("expected DV match")
	}
	if c.Matches(&Release{Title: "Show.S01E01.2160p.DV.HDR.WEB.h265-G"}) {
		t.Error("negative lookahead should exclude DV+HDR")
	}
}

func TestRegexTimeoutCountsAsNoMatch(t *testing.T) {
	old := RegexTimeout
	RegexTimeout = 5 * time.Millisecond
	defer func() { RegexTimeout = old }()
	cf := CustomFormat{Name: "evil", Specifications: []Specification{
		spec("evil", SpecReleaseTitle, false, true, map[string]any{"value": `^(a+)+$`}),
	}}
	c := mustCompile(t, AppSonarr, cf)
	title := strings.Repeat("a", 40) + "!"
	start := time.Now()
	res := c.Explain(&Release{Title: title})
	if res.Matched {
		t.Error("catastrophic pattern must not match")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("evaluation was not bounded: %s", time.Since(start))
	}
	if len(res.Conditions) != 1 || res.Conditions[0].Error == "" {
		t.Errorf("timeout should be reported, got %+v", res.Conditions)
	}
}

func TestCompileRejectsInvalidFormats(t *testing.T) {
	bad := []CustomFormat{
		{Name: "", Specifications: nil},
		{Name: "re", Specifications: []Specification{spec("r", SpecReleaseTitle, false, true, map[string]any{"value": `(`})}},
		{Name: "lang", Specifications: []Specification{spec("l", SpecLanguage, false, true, map[string]any{"value": 999})}},
		{Name: "src", Specifications: []Specification{spec("s", SpecSource, false, true, map[string]any{"value": 42})}},
		{Name: "radarr only", Specifications: []Specification{spec("m", SpecQualityModifier, false, true, map[string]any{"value": 5})}},
		{Name: "size", Specifications: []Specification{spec("s", SpecSize, false, true, map[string]any{"min": 5, "max": 1})}},
	}
	for _, cf := range bad {
		if _, err := Compile(AppSonarr, cf); err == nil {
			t.Errorf("%q: expected compile error", cf.Name)
		}
	}
}

func TestSpecificationJSONLayouts(t *testing.T) {
	trashJSON := `{"name":"German DL","trash_id":"abc","trash_scores":{"default":-10000,"german":11000},
	  "specifications":[{"name":"German","implementation":"LanguageSpecification","negate":false,"required":true,"fields":{"value":4}}]}`
	arrJSON := `{"id":7,"name":"German DL","specifications":[{"name":"German","implementation":"LanguageSpecification","negate":false,"required":true,
	  "fields":[{"order":0,"name":"value","label":"Language","value":4},{"order":1,"name":"exceptLanguage","value":false}]}]}`
	for _, raw := range []string{trashJSON, arrJSON} {
		var cf CustomFormat
		if err := json.Unmarshal([]byte(raw), &cf); err != nil {
			t.Fatal(err)
		}
		c := mustCompile(t, AppSonarr, cf)
		if !c.Matches(&Release{Languages: []Language{"German"}}) {
			t.Errorf("layout %q did not evaluate", raw[:30])
		}
	}
}

func TestEmptyFormatMatchesEverything(t *testing.T) {
	c := mustCompile(t, AppSonarr, CustomFormat{Name: "empty"})
	if !c.Matches(&Release{Title: "anything"}) {
		t.Error("an empty custom format matches every release in *arr")
	}
}

func TestFinalizeLanguages(t *testing.T) {
	cases := []struct {
		name             string
		parsed, provided []Language
		original         Language
		want             []Language
	}{
		{"unknown falls back to original", []Language{LanguageUnknown}, nil, "Japanese", []Language{"Japanese"}},
		{"original marker replaced", []Language{"German", LanguageOriginal}, nil, "English", []Language{"German", "English"}},
		{"original already present", []Language{"English", LanguageOriginal}, nil, "English", []Language{"English", LanguageUnknown}},
		{"provided languages win", []Language{"German"}, []Language{"French"}, "English", []Language{"French"}},
		{"no metadata keeps parsed", []Language{"German", LanguageOriginal}, nil, "", []Language{"German", LanguageOriginal}},
	}
	for _, tc := range cases {
		got := FinalizeLanguages(tc.parsed, tc.provided, tc.original)
		if strings.Join(langStrings(got), ",") != strings.Join(langStrings(tc.want), ",") {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func langStrings(ls []Language) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = string(l)
	}
	return out
}

func TestLanguageFromCode(t *testing.T) {
	for code, want := range map[string]Language{
		"de": "German", "ger": "German", "deu": "German", "en": "English", "pt-BR": "Portuguese (Brazil)",
		"es-419": "Spanish (Latino)", "ja": "Japanese", "zh": "Chinese", "": LanguageUnknown, "xx": LanguageUnknown,
		"German": "German",
	} {
		if got := LanguageFromCode(code); got != want {
			t.Errorf("%q: got %s want %s", code, got, want)
		}
	}
	if LanguageGerman.ISOCode() != "deu" {
		t.Errorf("ISO code %s", LanguageGerman.ISOCode())
	}
}

// A timed-out condition must never award a positively scored format, but it
// must not silently lift the penalty of a negatively scored (blocker) format.
func TestRegexTimeoutKeepsPenaltiesAndAwardsNothing(t *testing.T) {
	old := RegexTimeout
	RegexTimeout = 5 * time.Millisecond
	defer func() { RegexTimeout = old }()
	evil := map[string]any{"value": `^(a+)+$`}
	formats := []CustomFormat{
		{TrashID: "block", Name: "Blocker", Specifications: []Specification{spec("evil", SpecReleaseTitle, false, true, evil)}},
		{TrashID: "block-neg", Name: "Blocker negated", Specifications: []Specification{spec("evil", SpecReleaseTitle, true, true, evil)}},
		{TrashID: "bonus", Name: "Bonus", Specifications: []Specification{spec("evil", SpecReleaseTitle, false, true, evil)}},
		{TrashID: "bonus-neg", Name: "Bonus negated", Specifications: []Specification{spec("evil", SpecReleaseTitle, true, true, evil)}},
	}
	p := Profile{App: AppSonarr, Name: "T", Items: []ProfileItem{{Name: "Unknown", Allowed: true}},
		MinFormatScore: -100000,
		FormatScores:   map[string]int{"block": -10000, "block-neg": -10000, "bonus": 100, "bonus-neg": 100}}
	ev, errs := NewEvaluator(p, formats)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	before := MatchFailures()
	res := ev.Evaluate(Release{App: AppSonarr, Title: strings.Repeat("a", 40) + "!"})
	if MatchFailures() == before {
		t.Fatal("expected the pattern to time out")
	}
	var got []string
	for _, f := range res.Formats {
		got = append(got, f.Key)
	}
	if strings.Join(got, ",") != "block,block-neg" || res.Score != -20000 {
		t.Errorf("formats %v score %d, want only the blockers (-20000)", got, res.Score)
	}
}

func TestReleaseTitleMatchCapsFilename(t *testing.T) {
	c := mustCompile(t, AppSonarr, CustomFormat{Name: "br", Specifications: []Specification{
		spec("br", SpecReleaseTitle, false, true, map[string]any{"value": `BR-DISK`}),
	}})
	if !c.Matches(&Release{Title: "x", Filename: "Show.S01E01.BR-DISK"}) {
		t.Error("the filename must be matched")
	}
	if c.Matches(&Release{Title: "x", Filename: strings.Repeat("a", maxTitleLen) + "BR-DISK"}) {
		t.Error("the filename must be capped at maxTitleLen")
	}
}

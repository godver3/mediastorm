package arr

import (
	"testing"
	"time"
)

func testProfile() Profile {
	return Profile{
		App:  AppSonarr,
		Name: "Test",
		Items: []ProfileItem{
			{Name: "WEB 1080p", Allowed: true, Qualities: []string{"WEBDL-1080p", "WEBRip-1080p"}},
			{Name: "Bluray-1080p", Allowed: true},
			{Name: "HDTV-1080p", Allowed: false},
			{Name: "WEB 720p", Allowed: true, Qualities: []string{"WEBDL-720p", "WEBRip-720p"}},
		},
		MinFormatScore: 0,
		FormatScores:   map[string]int{"german": 1000, "ger-dl": 500, "bad": -10000},
	}
}

func testFormats() []CustomFormat {
	return []CustomFormat{
		{TrashID: "german", Name: "German", Specifications: []Specification{
			spec("g", SpecLanguage, false, true, map[string]any{"value": 4}),
		}},
		{TrashID: "ger-dl", Name: "German DL", Specifications: []Specification{
			spec("g", SpecLanguage, false, true, map[string]any{"value": 4}),
			spec("o", SpecLanguage, false, true, map[string]any{"value": -2}),
		}},
		{TrashID: "bad", Name: "LQ", Specifications: []Specification{
			spec("grp", SpecReleaseGroup, false, true, map[string]any{"value": `^(BADGRP)$`}),
		}},
		{TrashID: "unused", Name: "Unused", Specifications: []Specification{
			spec("x", SpecReleaseTitle, false, true, map[string]any{"value": `.`}),
		}},
	}
}

func newTestEvaluator(t *testing.T, p Profile) *Evaluator {
	t.Helper()
	ev, errs := NewEvaluator(p, testFormats())
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return ev
}

func evalTitle(ev *Evaluator, title string, ctx ReleaseContext) Evaluation {
	return ev.Evaluate(ev.Prepare(Parse(AppSonarr, title), ctx))
}

func TestEvaluateRejectsUnwantedQualityAndLowScore(t *testing.T) {
	ev := newTestEvaluator(t, testProfile())
	ctx := ReleaseContext{OriginalLanguage: "English"}
	if e := evalTitle(ev, "Show.S01E01.1080p.HDTV.x264-GRP", ctx); e.Allowed {
		t.Errorf("HDTV-1080p is not allowed: %+v", e)
	}
	if e := evalTitle(ev, "Show.S01E01.2160p.WEB.h265-GRP", ctx); e.Allowed {
		t.Error("qualities missing from the profile are rejected")
	}
	e := evalTitle(ev, "Show.S01E01.1080p.WEB.h264-BADGRP", ctx)
	if e.Allowed || e.Score != -10000 {
		t.Errorf("negative score below minimum must be rejected, got %+v", e)
	}
	e = evalTitle(ev, "Show.S01E01.German.DL.1080p.WEB.h264-GRP", ctx)
	if !e.Allowed || e.Score != 1500 {
		t.Errorf("German DL should score 1500, got %+v", e)
	}
	for _, f := range e.Formats {
		if f.Key == "unused" {
			t.Error("formats without a score must not be evaluated")
		}
	}
}

func TestCompareOrdersByQualityThenScore(t *testing.T) {
	ev := newTestEvaluator(t, testProfile())
	ctx := ReleaseContext{OriginalLanguage: "English"}
	titles := []string{
		"Show.S01E01.720p.WEB.h264-GRP",
		"Show.S01E01.German.1080p.BluRay.x264-GRP",
		"Show.S01E01.1080p.WEBRip.x264-GRP",
		"Show.S01E01.German.DL.1080p.WEB.h264-GRP",
		"Show.S01E01.German.1080p.WEB.h264-GRP",
	}
	var evals []Evaluation
	for _, title := range titles {
		evals = append(evals, evalTitle(ev, title, ctx))
	}
	ev.SortEvaluations(evals)
	want := []string{
		"Show.S01E01.German.DL.1080p.WEB.h264-GRP", // WEB 1080p group, score 1500
		"Show.S01E01.German.1080p.WEB.h264-GRP",    // WEB 1080p group, score 1000
		"Show.S01E01.1080p.WEBRip.x264-GRP",        // WEB 1080p group (WEBRip ranks equal), score 0
		"Show.S01E01.German.1080p.BluRay.x264-GRP", // lower quality item despite score
		"Show.S01E01.720p.WEB.h264-GRP",
	}
	for i, e := range evals {
		if e.Release.Title != want[i] {
			t.Fatalf("position %d: got %s want %s", i, e.Release.Title, want[i])
		}
	}
}

func TestComparePreferredProtocolAndSize(t *testing.T) {
	p := testProfile()
	p.PreferredProtocol = ProtocolUsenet
	ev := newTestEvaluator(t, p)
	base := Parse(AppSonarr, "Show.S01E01.1080p.WEB.h264-GRP")
	gb := int64(1024 * 1024 * 1024)
	torrent := base
	torrent.Protocol, torrent.Size = ProtocolTorrent, 8*gb
	usenet := base
	usenet.Protocol, usenet.Size = ProtocolUsenet, 2*gb
	a, b := ev.Evaluate(torrent), ev.Evaluate(usenet)
	if ev.Compare(b, a) >= 0 {
		t.Error("preferred protocol must win before size")
	}
	big, small := usenet, usenet
	big.Size, small.Size = 6*gb, 2*gb
	now := time.Now()
	big.PublishDate, small.PublishDate = now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)
	if ev.Compare(ev.Evaluate(big), ev.Evaluate(small)) >= 0 {
		t.Error("without a preferred size the larger release wins")
	}
}

func TestPreferredSizeWithRuntime(t *testing.T) {
	p := testProfile()
	pref := 50.0 // MB per minute
	p.SizeLimits = map[string]SizeLimit{"WEBDL-1080p": {Min: 5, Preferred: &pref}}
	ev := newTestEvaluator(t, p)
	mb := int64(1024 * 1024)
	near := Parse(AppSonarr, "Show.S01E01.1080p.WEB.h264-A")
	far := Parse(AppSonarr, "Show.S01E01.1080p.WEB.h264-B")
	near.Size, far.Size = 2500*mb, 6000*mb // preferred = 50 MB * 50 min = 2500 MB
	ctx := ReleaseContext{RuntimeMinutes: 50}
	if ev.Compare(ev.Evaluate(ev.Prepare(near, ctx)), ev.Evaluate(ev.Prepare(far, ctx))) >= 0 {
		t.Error("closest to the preferred size should win")
	}
}

func TestSizeLimitsAreEnforcedWhenEnabled(t *testing.T) {
	p := testProfile()
	max := 10.0
	p.SizeLimits = map[string]SizeLimit{"WEBDL-1080p": {Min: 2, Max: &max}}
	p.EnforceSizeLimits = true
	ev := newTestEvaluator(t, p)
	mb := int64(1024 * 1024)
	r := Parse(AppSonarr, "Show.S01E01.1080p.WEB.h264-GRP")
	ctx := ReleaseContext{RuntimeMinutes: 60}
	r.Size = 50 * mb // below 2 MB/min * 60
	if e := ev.Evaluate(ev.Prepare(r, ctx)); e.Allowed {
		t.Error("undersized release must be rejected")
	}
	r.Size = 700 * mb // above 10 MB/min * 60
	if e := ev.Evaluate(ev.Prepare(r, ctx)); e.Allowed {
		t.Error("oversized release must be rejected")
	}
	r.Size = 300 * mb
	if e := ev.Evaluate(ev.Prepare(r, ctx)); !e.Allowed {
		t.Errorf("in-range release must pass: %+v", e.Rejections)
	}
	// Season packs scale the limits by the episode count.
	pack := Parse(AppSonarr, "Show.S01E01E02E03.1080p.WEB.h264-GRP")
	pack.Size, pack.EpisodeCount = 1500*mb, 3
	if e := ev.Evaluate(ev.Prepare(pack, ctx)); !e.Allowed {
		t.Errorf("3-episode release within 3x limits must pass: %+v", e.Rejections)
	}
	// A season pack of unknown length cannot be judged by size.
	season := Parse(AppSonarr, "Show.S01.1080p.WEB.h264-GRP")
	season.Size = 40000 * mb
	if !season.FullSeason {
		t.Fatal("expected a full season pack")
	}
	if e := ev.Evaluate(ev.Prepare(season, ctx)); !e.Allowed {
		t.Errorf("a season pack without an episode count must not be size-rejected: %+v", e.Rejections)
	}
}

func TestRadarrProfileLanguage(t *testing.T) {
	p := Profile{App: AppRadarr, Name: "French", Language: "French",
		Items:        []ProfileItem{{Name: "WEBDL-1080p", Allowed: true}},
		FormatScores: map[string]int{}}
	ev, errs := NewEvaluator(p, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	en := ev.Evaluate(ev.Prepare(Parse(AppRadarr, "Movie.2020.1080p.WEB-DL.h264-G"), ReleaseContext{OriginalLanguage: "English"}))
	if en.Allowed {
		t.Error("English release must be rejected by a French profile")
	}
	fr := ev.Evaluate(ev.Prepare(Parse(AppRadarr, "Movie.2020.FRENCH.1080p.WEB-DL.h264-G"), ReleaseContext{OriginalLanguage: "English"}))
	if !fr.Allowed {
		t.Errorf("French release must pass: %v", fr.Rejections)
	}
	p.Language = LanguageOriginal
	ev, _ = NewEvaluator(p, nil)
	de := ev.Evaluate(ev.Prepare(Parse(AppRadarr, "Movie.2020.GERMAN.1080p.WEB-DL.h264-G"), ReleaseContext{OriginalLanguage: "English"}))
	if de.Allowed {
		t.Error("German-only release lacks the original language")
	}
}

func TestMultiLanguagesSetting(t *testing.T) {
	p := testProfile()
	p.MultiLanguages = []Language{"German", "English"}
	ev := newTestEvaluator(t, p)
	e := evalTitle(ev, "Show.S01E01.MULTi.1080p.WEB.h264-GRP", ReleaseContext{OriginalLanguage: "English"})
	// German + English with an English original also counts as German DL.
	if e.Score != 1500 {
		t.Errorf("MULTi should be treated as German+English, got score %d languages %v", e.Score, e.Release.Languages)
	}
}

func TestValidateProfile(t *testing.T) {
	bad := []Profile{
		{App: "kodi", Name: "x", Items: []ProfileItem{{Name: "WEBDL-1080p", Allowed: true}}},
		{App: AppSonarr, Name: "", Items: []ProfileItem{{Name: "WEBDL-1080p", Allowed: true}}},
		{App: AppSonarr, Name: "x"},
		{App: AppSonarr, Name: "x", Items: []ProfileItem{{Name: "Remux-1080p", Allowed: true}}}, // Radarr name
		{App: AppSonarr, Name: "x", Items: []ProfileItem{{Name: "WEBDL-1080p", Allowed: false}}},
		{App: AppSonarr, Name: "x", Items: []ProfileItem{{Name: "WEBDL-1080p", Allowed: true}, {Name: "WEB", Allowed: true, Qualities: []string{"WEBDL-1080p"}}}},
	}
	for i, p := range bad {
		if err := ValidateProfile(p); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	if err := ValidateProfile(testProfile()); err != nil {
		t.Errorf("valid profile: %v", err)
	}
}

// Sonarr's Revision.CompareTo compares Real before Version, so a REAL
// release beats a PROPER v2 of the same quality.
func TestRevisionCompareRealBeforeVersion(t *testing.T) {
	realV1 := Revision{Version: 1, Real: 1}
	properV2 := Revision{Version: 2}
	if realV1.Compare(properV2) <= 0 || properV2.Compare(realV1) >= 0 {
		t.Errorf("REAL v1 must rank above PROPER v2")
	}
	if (Revision{Version: 2, Real: 1}).Compare(realV1) <= 0 {
		t.Errorf("with equal real, the higher version must rank above")
	}
	if realV1.Compare(Revision{Version: 1, Real: 1, IsRepack: true}) != 0 {
		t.Errorf("IsRepack must not affect the order")
	}
	p := testProfile()
	p.Propers = PropersPreferAndUpgrade
	ev := newTestEvaluator(t, p)
	a := Evaluation{QualityRank: 4, Release: Release{Revision: realV1}}
	b := Evaluation{QualityRank: 4, Release: Release{Revision: properV2}, Score: 1000}
	if ev.Compare(a, b) >= 0 || ev.Compare(b, a) <= 0 {
		t.Errorf("REAL v1 must be preferred over PROPER v2 under %s", PropersPreferAndUpgrade)
	}
}

// Languages the indexer provides replace the parsed ones; a MULTi release
// still adds the profile's multi languages on top (*arr's
// AggregateLanguages).
func TestMultiLanguagesOnProvidedLanguages(t *testing.T) {
	p := testProfile()
	p.MultiLanguages = []Language{"French"}
	ev := newTestEvaluator(t, p)
	r := ev.Prepare(Parse(AppSonarr, "Show.S01E01.MULTi.1080p.WEB-DL-GRP"), ReleaseContext{ProvidedLanguages: []Language{LanguageEnglish}})
	if !containsLanguage(r.Languages, LanguageEnglish) || !containsLanguage(r.Languages, "French") {
		t.Errorf("languages = %v, want English and French", r.Languages)
	}
}

// Size tie-breaks truncate to 200 MB steps and usenet age counts whole
// days, as in *arr.
func TestSizeAndAgeBucketsMatchArr(t *testing.T) {
	mb := int64(1024 * 1024)
	for _, tc := range []struct{ a, b int64 }{{1090, 1110}, {1000, 1199}} {
		if roundTo(tc.a*mb, sizeRound) != roundTo(tc.b*mb, sizeRound) {
			t.Errorf("%d MB and %d MB should tie", tc.a, tc.b)
		}
	}
	if roundTo(1190*mb, sizeRound) == roundTo(1210*mb, sizeRound) {
		t.Error("1190 MB and 1210 MB should not tie")
	}
	ago := func(days float64) time.Time { return time.Now().Add(-time.Duration(days * 24 * float64(time.Hour))) }
	for _, tc := range []struct {
		days float64
		want float64
	}{{3, 10}, {7.5, 10}, {8.2, -1}, {31.9, -1}, {320, -3}} {
		if got := ageBucket(AppRadarr, ago(tc.days)); got != tc.want {
			t.Errorf("%.1f days: bucket %v, want %v", tc.days, got, tc.want)
		}
	}
}

// Mixed torrent and usenet results sort the same whatever their input order:
// protocols are ordered first (usenet by default), seeders and age only
// within one.
func TestCompareIsTransitiveAcrossProtocols(t *testing.T) {
	ev := newTestEvaluator(t, testProfile())
	gb := int64(1) << 30
	seeders := func(n int) *int { return &n }
	mk := func(name string, p Protocol, size int64, s *int) Evaluation {
		r := Parse(AppSonarr, "Show.S01E01.1080p.WEB-DL.H.264-GRP")
		r.Protocol, r.Size, r.Seeders = p, size, s
		r.PublishDate = time.Now().Add(-48 * time.Hour)
		e := ev.Evaluate(ev.Prepare(r, ReleaseContext{}))
		e.Release.Title = name
		return e
	}
	a := mk("A", ProtocolTorrent, 2*gb, seeders(1000))
	b := mk("B", ProtocolTorrent, 6*gb, seeders(5))
	c := mk("C", ProtocolUsenet, 4*gb, nil)
	for _, order := range [][]Evaluation{{a, b, c}, {c, b, a}, {b, c, a}} {
		evals := append([]Evaluation(nil), order...)
		ev.SortEvaluations(evals)
		if evals[0].Release.Title != "C" || evals[1].Release.Title != "A" {
			t.Errorf("order %v", []string{evals[0].Release.Title, evals[1].Release.Title, evals[2].Release.Title})
		}
	}
	if ev.CompareWithProtocol(a, c, ProtocolTorrent) >= 0 {
		t.Error("a torrent preference must put the torrent first")
	}
}

// A language named in the episode title is not the release's language
// (Sonarr's AggregateLanguages): the release counts as the original.
func TestEpisodeTitleLanguagesAreIgnored(t *testing.T) {
	ev := newTestEvaluator(t, testProfile())
	ctx := ReleaseContext{OriginalLanguage: LanguageEnglish, EpisodeTitles: []string{"The Chinese Woman"}}
	r := ev.Prepare(Parse(AppSonarr, "Seinfeld.S07E08.The.Chinese.Woman.1080p.AMZN.WEB-DL.DDP2.0.H.264-NTb"), ctx)
	if len(r.Languages) != 1 || r.Languages[0] != LanguageEnglish {
		t.Errorf("languages = %v, want English", r.Languages)
	}
	// A language tag outside the episode title still counts.
	r = ev.Prepare(Parse(AppSonarr, "Seinfeld.S07E08.The.Chinese.Woman.GERMAN.1080p.AMZN.WEB-DL.DDP2.0.H.264-GRP"), ctx)
	if !containsLanguage(r.Languages, LanguageGerman) || containsLanguage(r.Languages, "Chinese") {
		t.Errorf("languages = %v, want German only", r.Languages)
	}
	// Without the episode title the word counts, as before.
	r = ev.Prepare(Parse(AppSonarr, "Seinfeld.S07E08.The.Chinese.Woman.1080p.AMZN.WEB-DL.DDP2.0.H.264-NTb"), ReleaseContext{OriginalLanguage: LanguageEnglish})
	if !containsLanguage(r.Languages, "Chinese") {
		t.Errorf("languages without episode title = %v", r.Languages)
	}
}

// Like Sonarr's comparer, the preferred size is per episode runtime for
// season packs and multi-episode releases too, and usenet posts older than a
// week rank alike (Radarr ranks them by age).
func TestSonarrSizeAndAgeTieBreaks(t *testing.T) {
	p := testProfile()
	pref := 40.0
	p.SizeLimits = map[string]SizeLimit{"WEBDL-1080p": {Min: 1, Preferred: &pref}}
	ev := newTestEvaluator(t, p)
	mb := int64(1024 * 1024)
	ctx := ReleaseContext{RuntimeMinutes: 100} // preferred 4000 MB
	s1 := Parse(AppSonarr, "Show.S01.1080p.WEB-DL.DDP5.1.H.264-S1")
	s2 := Parse(AppSonarr, "Show.S01.1080p.WEB-DL.DDP5.1.H.264-S2")
	s1.Size, s2.Size = 20000*mb, 40000*mb
	if ev.Compare(ev.Evaluate(ev.Prepare(s1, ctx)), ev.Evaluate(ev.Prepare(s2, ctx))) >= 0 {
		t.Error("the pack closer to the preferred size should win")
	}
	if ageBucket(AppSonarr, time.Now().Add(-10*24*time.Hour)) != ageBucket(AppSonarr, time.Now().Add(-400*24*time.Hour)) {
		t.Error("Sonarr ranks posts older than a week alike")
	}
	if ageBucket(AppRadarr, time.Now().Add(-10*24*time.Hour)) <= ageBucket(AppRadarr, time.Now().Add(-400*24*time.Hour)) {
		t.Error("Radarr prefers the newer of two old posts")
	}
}

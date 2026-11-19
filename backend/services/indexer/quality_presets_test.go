package indexer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

type fakeUserSettings struct {
	settings map[string]*models.UserSettings
}

func (f fakeUserSettings) Get(userID string) (*models.UserSettings, error) {
	return f.settings[userID], nil
}

type fakeClientSettings struct {
	settings map[string]*models.ClientFilterSettings
}

func (f fakeClientSettings) Get(clientID, _ string) (*models.ClientFilterSettings, error) {
	return f.settings[clientID], nil
}

const germanPresetJSON = `{"app":"sonarr","name":"German Test","profile":{"name":"German Test","minFormatScore":0,"items":[
  {"name":"WEB 1080p","allowed":true,"items":["WEBDL-1080p","WEBRip-1080p"]},
  {"name":"HDTV-1080p","allowed":false},
  {"name":"WEBDL-720p","allowed":true}],
  "formatItems":{"German":"g","German DL":"gdl","LQ":"lq"}},
 "customFormats":[
  {"trash_id":"g","name":"German","trash_scores":{"default":1000},"specifications":[{"name":"de","implementation":"LanguageSpecification","negate":false,"required":true,"fields":{"value":4}}]},
  {"trash_id":"gdl","name":"German DL","trash_scores":{"default":500},"specifications":[
    {"name":"de","implementation":"LanguageSpecification","negate":false,"required":true,"fields":{"value":4}},
    {"name":"orig","implementation":"LanguageSpecification","negate":false,"required":true,"fields":{"value":-2}}]},
  {"trash_id":"lq","name":"LQ","trash_scores":{"default":-10000},"specifications":[{"name":"grp","implementation":"ReleaseGroupSpecification","negate":false,"required":true,"fields":{"value":"^(LQGRP)$"}}]}
 ]}`

func newPresetTestService(t *testing.T, mutate func(*config.Settings), results []models.NZBResult) (*Service, *qualityprofiles.Service, string) {
	t.Helper()
	presets := qualityprofiles.NewService(qualityprofiles.NewMemoryStore(), nil, nil)
	p, err := presets.ImportJSON(context.Background(), []byte(germanPresetJSON), arr.AppSonarr, "")
	if err != nil {
		t.Fatalf("import preset: %v", err)
	}
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.QualityPresets.SeriesPreset = p.ID
	// Simple-mode preference filters that preset mode must ignore.
	settings.Filtering.MaxSizeEpisodeGB = 1
	settings.Filtering.FilterOutTerms = []string{"WvF"}
	if mutate != nil {
		mutate(&settings)
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	svc := NewService(mgr, nil, stubDebridSearchService{results: results})
	svc.SetQualityPresetProvider(presets)
	return svc, presets, p.ID
}

var gb = int64(1024 * 1024 * 1024)

func presetTestResults() []models.NZBResult {
	mk := func(title string) models.NZBResult {
		return models.NZBResult{Title: title, Indexer: "Test", ServiceType: models.ServiceTypeDebrid, SizeBytes: 3 * gb, Attributes: map[string]string{}}
	}
	return []models.NZBResult{
		mk("Dark.S01E01.1080p.HDTV.x264-GRP"),          // quality not allowed
		mk("Dark.S01E01.GERMAN.1080p.WEB.h264-A"),      // German 1000
		mk("Dark.S01E01.German.DL.1080p.WEB.h264-WvF"), // German DL 1500, blocked term in simple mode
		mk("Dark.S01E01.720p.WEB.h264-B"),              // allowed lower quality
		mk("Dark.S01E01.German.1080p.WEB.h264-LQGRP"),  // below minimum score
		mk("Dark.S01E01.1080p.WEB.h264-C"),             // allowed, score 0
	}
}

func TestSearchRanksWithQualityPreset(t *testing.T) {
	svc, _, _ := newPresetTestService(t, nil, presetTestResults())
	results, err := svc.Search(t.Context(), SearchOptions{Query: "Dark S01E01", MediaType: "series", OriginalLanguage: "en"})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, r := range results {
		titles = append(titles, r.Title)
	}
	want := []string{
		"Dark.S01E01.German.DL.1080p.WEB.h264-WvF",
		"Dark.S01E01.GERMAN.1080p.WEB.h264-A",
		"Dark.S01E01.1080p.WEB.h264-C",
		"Dark.S01E01.720p.WEB.h264-B",
	}
	if strings.Join(titles, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ranking:\n%s\nwant:\n%s", strings.Join(titles, "\n"), strings.Join(want, "\n"))
	}
	if results[0].Attributes["customFormatScore"] != "1500" || results[0].Attributes["arrQuality"] != "WEBDL-1080p" || results[0].Attributes["qualityPreset"] != "German Test" {
		t.Errorf("annotations: %v", results[0].Attributes)
	}
}

func TestSearchWithScoringReportsPresetRejections(t *testing.T) {
	svc, _, _ := newPresetTestService(t, nil, presetTestResults())
	scored, err := svc.SearchWithScoring(t.Context(), SearchOptions{Query: "Dark S01E01", MediaType: "series", IncludeFiltered: true, IncludeScoreBreakdown: true, OriginalLanguage: "en"})
	if err != nil {
		t.Fatal(err)
	}
	var passed, filtered int
	reasons := map[string]string{}
	for _, r := range scored {
		if r.FilterStatus == "passed" {
			passed++
		} else {
			filtered++
			reasons[r.Title] = r.FilterReason
		}
	}
	if passed != 4 || filtered != 2 {
		t.Fatalf("passed %d filtered %d (%v)", passed, filtered, reasons)
	}
	if !strings.Contains(reasons["Dark.S01E01.1080p.HDTV.x264-GRP"], "not wanted") {
		t.Errorf("quality rejection reason: %q", reasons["Dark.S01E01.1080p.HDTV.x264-GRP"])
	}
	if !strings.Contains(reasons["Dark.S01E01.German.1080p.WEB.h264-LQGRP"], "below the minimum") {
		t.Errorf("score rejection reason: %q", reasons["Dark.S01E01.German.1080p.WEB.h264-LQGRP"])
	}
	top := scored[0]
	if top.TotalScore != 1500 || len(top.ScoreBreakdown) == 0 || top.ScoreBreakdown[0].Criterion != "Quality Preset" {
		t.Errorf("top result breakdown: score %d %+v", top.TotalScore, top.ScoreBreakdown)
	}
}

func TestSimpleModeStillUsesPreferenceFilters(t *testing.T) {
	svc, _, _ := newPresetTestService(t, func(s *config.Settings) { s.QualityPresets.SeriesPreset = config.QualityPresetSimple }, presetTestResults())
	scored, err := svc.SearchWithScoring(t.Context(), SearchOptions{Query: "Dark S01E01", MediaType: "series", IncludeFiltered: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range scored {
		if r.FilterStatus == "passed" {
			t.Errorf("simple mode must apply the 1 GB cap and blocked term, %q passed", r.Title)
		}
		if r.Attributes["qualityPreset"] != "" {
			t.Errorf("simple mode must not annotate presets: %v", r.Attributes)
		}
	}
}

func TestSelectQualityPresetCascade(t *testing.T) {
	svc, presets, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	otherPreset, err := presets.ImportJSON(context.Background(), []byte(germanPresetJSON), arr.AppSonarr, "Other")
	if err != nil {
		t.Fatal(err)
	}
	other, deleted := otherPreset.ID, "deleted-id"
	simple := config.QualityPresetSimple
	svc.userSettings = fakeUserSettings{settings: map[string]*models.UserSettings{
		"u1": {QualityPresets: &models.QualityPresetSelection{SeriesPreset: &other}},
		"u2": {QualityPresets: &models.QualityPresetSelection{SeriesPreset: &simple}},
		"u3": {QualityPresets: &models.QualityPresetSelection{SeriesPreset: &deleted}},
	}}
	svc.clientSettings = fakeClientSettings{settings: map[string]*models.ClientFilterSettings{
		"tv": {QualityPresetSeries: &presetID},
	}}
	cases := []struct {
		opts SearchOptions
		want QualityPresetSelection
	}{
		{SearchOptions{MediaType: "series"}, QualityPresetSelection{PresetID: presetID, Source: "global"}},
		{SearchOptions{MediaType: "movie"}, QualityPresetSelection{Source: "none"}},
		{SearchOptions{MediaType: "series", UserID: "u1"}, QualityPresetSelection{PresetID: other, Source: "profile"}},
		{SearchOptions{MediaType: "series", UserID: "u2"}, QualityPresetSelection{Source: "profile"}},
		// A deleted preset is skipped: the server's choice applies.
		{SearchOptions{MediaType: "series", UserID: "u3"}, QualityPresetSelection{PresetID: presetID, Source: "global"}},
		{SearchOptions{MediaType: "series", UserID: "u1", ClientID: "tv"}, QualityPresetSelection{PresetID: presetID, Source: "device"}},
		{SearchOptions{MediaType: "series", UserID: "u1", QualityPreset: "simple"}, QualityPresetSelection{Source: "request"}},
		{SearchOptions{MediaType: "series", QualityPreset: "x"}, QualityPresetSelection{PresetID: "x", Source: "request"}},
	}
	for i, tc := range cases {
		if got := svc.SelectQualityPreset(tc.opts, settings); got != tc.want {
			t.Errorf("case %d: got %+v want %+v", i, got, tc.want)
		}
	}
	settings.QualityPresets.AllowRequestOverride = false
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", QualityPreset: "x"}, settings); got.Source != "global" {
		t.Errorf("request override must be ignored when disabled, got %+v", got)
	}
	// Presets the admin offers in version pickers stay selectable.
	settings.QualityPresets.PickerPresets = []string{"x", config.QualityPresetSimple}
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", QualityPreset: "x"}, settings); got.PresetID != "x" || got.Source != "request" {
		t.Errorf("an offered preset must be selectable per search, got %+v", got)
	}
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", QualityPreset: "simple"}, settings); got.PresetID != "" || got.Source != "request" {
		t.Errorf("offered simple ranking must be selectable, got %+v", got)
	}
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", QualityPreset: "not-offered"}, settings); got.Source != "global" {
		t.Errorf("a preset that is not offered must be ignored, got %+v", got)
	}
}

func TestMissingOrMismatchedPresetFallsBackToSimple(t *testing.T) {
	svc, _, presetID := newPresetTestService(t, func(s *config.Settings) { s.QualityPresets.SeriesPreset = "does-not-exist" }, nil)
	settings, _ := svc.cfg.Load()
	if a := svc.resolveQualityPreset(t.Context(), SearchOptions{MediaType: "series"}, settings); a != nil {
		t.Error("unknown preset must fall back to simple ranking")
	}
	settings.QualityPresets.MoviePreset = presetID // a Sonarr preset used for movies
	if a := svc.resolveQualityPreset(t.Context(), SearchOptions{MediaType: "movie"}, settings); a != nil {
		t.Error("a series preset must not rank movies")
	}
}

// Packs whose scraper reports the size of one file are judged by the size
// of the whole pack.
func TestPresetPerFilePackSize(t *testing.T) {
	_, presets, presetID := newPresetTestService(t, nil, nil)
	active, err := presets.Active(t.Context(), presetID)
	if err != nil {
		t.Fatal(err)
	}
	r := models.NZBResult{Title: "Dark.S01.1080p.WEB.h264-GRP", SizeBytes: 2 << 30, EpisodeCount: 10, SizePerFile: true, Attributes: map[string]string{}}
	if got := presetRelease(active, r, arr.ReleaseContext{}).Size; got != 20<<30 {
		t.Errorf("per-file pack size = %d, want the whole pack", got)
	}
	r.SizePerFile = false
	if got := presetRelease(active, r, arr.ReleaseContext{}).Size; got != 2<<30 {
		t.Errorf("pack total size = %d", got)
	}
}

func TestPresetUsesIndexerLanguages(t *testing.T) {
	svc, presets, presetID := newPresetTestService(t, nil, nil)
	active, err := presets.Active(t.Context(), presetID)
	if err != nil {
		t.Fatal(err)
	}
	_ = svc
	untagged := models.NZBResult{Title: "Dark.S01E01.1080p.WEB.h264-GRP", ServiceType: models.ServiceTypeUsenet, Attributes: map[string]string{"language": "German"}}
	ev := active.Evaluator.Evaluate(presetRelease(active, untagged, arr.ReleaseContext{OriginalLanguage: "English"}))
	if ev.Score != 1000 {
		t.Errorf("newznab language attribute should count as German, got %d %v", ev.Score, ev.Release.Languages)
	}
	flags := models.NZBResult{Title: "Dark.S01E01.1080p.WEB.h264-GRP", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"languages": "🇩🇪,🇬🇧"}}
	ev = active.Evaluator.Evaluate(presetRelease(active, flags, arr.ReleaseContext{OriginalLanguage: "English"}))
	if ev.Score != 1500 {
		t.Errorf("scraper languages fill in for untagged names (German + original English), got %d %v", ev.Score, ev.Release.Languages)
	}
}

func TestBundleWithoutPreferencesKeepsAdaptiveCaps(t *testing.T) {
	cap := 7.5
	b := effectiveFilterBundle{
		Default: models.FilterSettings{MaxSizeEpisodeGB: models.FloatPtr(1), FilterOutTerms: []string{"x"}, MaxResolution: models.StringPtr("720p")},
		caps:    models.AdaptiveCaps{MaxSizeEpisodeGB: &cap},
	}
	out := b.withoutPreferences()
	if out.Default.MaxSizeEpisodeGB == nil || *out.Default.MaxSizeEpisodeGB != cap {
		t.Errorf("adaptive cap lost: %v", out.Default.MaxSizeEpisodeGB)
	}
	if len(out.Default.FilterOutTerms) != 0 || out.Default.MaxResolution != nil {
		t.Errorf("preferences kept: %+v", out.Default)
	}
}

// With a German original language, untagged releases carry German audio,
// exactly like *arr's language fallback.
func TestPresetOriginalLanguageFallback(t *testing.T) {
	svc, presets, id := newPresetTestService(t, nil, nil)
	a, _ := presets.Active(t.Context(), id)
	kept, _ := svc.evaluatePreset(a, presetTestResults(), SearchOptions{MediaType: "series", OriginalLanguage: "de"}, "")
	for _, k := range kept {
		if k.eval.Score != 1500 {
			t.Errorf("%s: score %d, want 1500 (German + original)", k.result.Title, k.eval.Score)
		}
	}
}

func TestPresetEvaluationParallelMatchesSequential(t *testing.T) {
	svc, presets, id := newPresetTestService(t, nil, nil)
	active, err := presets.Active(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	base := presetTestResults()
	var results []models.NZBResult
	for i := 0; i < 40; i++ {
		for _, r := range base {
			r.Title = strings.Replace(r.Title, "S01E01", fmt.Sprintf("S01E%02d", i+1), 1)
			r.SizeBytes = int64(i+1) * gb / 4
			results = append(results, r)
		}
	}
	opts := SearchOptions{MediaType: "series", OriginalLanguage: "en"}
	kept, rejected := svc.evaluatePreset(active, results, opts, config.StreamingServicePriorityNone)

	rc := presetReleaseContext(opts)
	var wantKept, wantRejected []string
	var seqKept []presetScored
	for _, r := range results {
		ev := active.Evaluator.Evaluate(presetRelease(active, r, rc))
		if ev.Allowed {
			seqKept = append(seqKept, presetScored{result: annotatePresetResult(r, active, ev), eval: ev})
		} else {
			wantRejected = append(wantRejected, r.Title)
		}
	}
	sort.SliceStable(seqKept, func(i, j int) bool {
		return comparePresetResults(seqKept[i], seqKept[j], active, config.StreamingServicePriorityNone) < 0
	})
	for _, k := range seqKept {
		wantKept = append(wantKept, k.result.Title+"="+k.result.Attributes["customFormatScore"])
	}
	var gotKept, gotRejected []string
	for _, k := range kept {
		gotKept = append(gotKept, k.result.Title+"="+k.result.Attributes["customFormatScore"])
	}
	for _, r := range rejected {
		gotRejected = append(gotRejected, r.result.Title)
	}
	if len(results) < presetParallelMin || strings.Join(gotKept, ",") != strings.Join(wantKept, ",") || strings.Join(gotRejected, ",") != strings.Join(wantRejected, ",") {
		t.Errorf("parallel evaluation differs from sequential:\nkept %v\nwant %v\nrejected %v\nwant %v", gotKept, wantKept, gotRejected, wantRejected)
	}
}

func TestForEachParallelVisitsEveryIndexOnce(t *testing.T) {
	for _, n := range []int{0, 1, presetParallelMin - 1, presetParallelMin, 1000} {
		seen := make([]int32, n)
		forEachParallel(n, func(i int) { atomic.AddInt32(&seen[i], 1) })
		for i, c := range seen {
			if c != 1 {
				t.Fatalf("n=%d: index %d visited %d times", n, i, c)
			}
		}
	}
}

func TestSelectQualityPresetAnimeCascade(t *testing.T) {
	svc, presets, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	importID := func(name string) string {
		p, err := presets.ImportJSON(context.Background(), []byte(germanPresetJSON), arr.AppSonarr, name)
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	animeID, normalID := importID("Anime"), importID("Normal")
	simple := config.QualityPresetSimple
	settings.QualityPresets.AnimeSeriesPreset = "global-anime-id"
	svc.userSettings = fakeUserSettings{settings: map[string]*models.UserSettings{
		"u-anime":  {QualityPresets: &models.QualityPresetSelection{SeriesPreset: &normalID, AnimeSeriesPreset: &animeID}},
		"u-normal": {QualityPresets: &models.QualityPresetSelection{SeriesPreset: &normalID}},
		"u-simple": {QualityPresets: &models.QualityPresetSelection{AnimeSeriesPreset: &simple}},
	}}
	svc.clientSettings = fakeClientSettings{settings: map[string]*models.ClientFilterSettings{
		"tv": {QualityPresetSeries: &presetID}, // a normal device preset
	}}
	cases := []struct {
		name string
		opts SearchOptions
		want QualityPresetSelection
	}{
		{"anime: profile anime slot beats the device's normal one",
			SearchOptions{MediaType: "series", IsAnime: true, UserID: "u-anime", ClientID: "tv"}, QualityPresetSelection{PresetID: animeID, Source: "profile-anime"}},
		{"anime: server anime slot beats normal device and profile slots",
			SearchOptions{MediaType: "series", IsAnime: true, UserID: "u-normal", ClientID: "tv"}, QualityPresetSelection{PresetID: "global-anime-id", Source: "global-anime"}},
		{"anime: simple ranking in an anime slot",
			SearchOptions{MediaType: "series", IsAnime: true, UserID: "u-simple"}, QualityPresetSelection{Source: "profile-anime"}},
		{"not anime: anime slots are ignored",
			SearchOptions{MediaType: "series", UserID: "u-anime", ClientID: "tv"}, QualityPresetSelection{PresetID: presetID, Source: "device"}},
		{"not anime: profile",
			SearchOptions{MediaType: "series", UserID: "u-anime"}, QualityPresetSelection{PresetID: normalID, Source: "profile"}},
		{"anime movie without anime slots uses the normal cascade",
			SearchOptions{MediaType: "movie", IsAnime: true}, QualityPresetSelection{Source: "none"}},
	}
	for _, tc := range cases {
		if got := svc.SelectQualityPreset(tc.opts, settings); got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	// Without any anime slot, anime titles fall back to the normal slots.
	settings.QualityPresets.AnimeSeriesPreset = ""
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", IsAnime: true, UserID: "u-normal"}, settings); got.PresetID != normalID || got.Source != "profile" {
		t.Errorf("fallback to normal slots: %+v", got)
	}
}

// A preset whose audience does not fit the title is not used.
func TestPresetAudienceMustFitTitle(t *testing.T) {
	svc, presets, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	animeOnly := qualityprofiles.AudienceAnime
	if _, err := presets.UpdateSettings(context.Background(), presetID, qualityprofiles.SettingsUpdate{Audience: &animeOnly}); err != nil {
		t.Fatal(err)
	}
	if active := svc.resolveQualityPreset(context.Background(), SearchOptions{MediaType: "series"}, settings); active != nil {
		t.Errorf("an anime-only preset ranked a non-anime title")
	}
	if active := svc.resolveQualityPreset(context.Background(), SearchOptions{MediaType: "series", IsAnime: true}, settings); active == nil {
		t.Errorf("an anime-only preset must rank anime")
	}
}

// The prequeue buckets preset-ranked candidates by these breakdown entries.
func TestPresetBreakdownShape(t *testing.T) {
	_, presets, presetID := newPresetTestService(t, nil, nil)
	active, err := presets.Active(context.Background(), presetID)
	if err != nil {
		t.Fatal(err)
	}
	ev := active.Evaluator.Evaluate(arr.Parse(arr.AppSonarr, "Dark.S01E01.German.DL.1080p.WEB.h264-WvF"))
	items := presetBreakdown(active, ev)
	if len(items) < 3 || items[0].Criterion != PresetBreakdownCriterion {
		t.Fatalf("breakdown must start with %q: %+v", PresetBreakdownCriterion, items)
	}
	var quality, score bool
	for _, it := range items {
		quality = quality || (it.Criterion == PresetQualityCriterion && it.RankValue == int64(ev.QualityRank))
		score = score || (it.Criterion == PresetFormatScoreCriterion && it.RankValue == int64(ev.Score))
	}
	if !quality || !score {
		t.Fatalf("breakdown lacks quality position or format score: %+v", items)
	}
}

// A profile's picker list also limits what a single search may ask for.
func TestProfilePickerListLimitsRequestPresets(t *testing.T) {
	svc, _, presetID := newPresetTestService(t, nil, nil)
	settings, _ := svc.cfg.Load()
	settings.QualityPresets.AllowRequestOverride = false
	settings.QualityPresets.PickerPresets = []string{"a", "b"}
	onlyA := []string{"a"}
	svc.userSettings = fakeUserSettings{settings: map[string]*models.UserSettings{
		"kid": {QualityPresets: &models.QualityPresetSelection{PickerPresets: &onlyA}},
	}}
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", UserID: "kid", QualityPreset: "a"}, settings); got.PresetID != "a" || got.Source != "request" {
		t.Errorf("allowed pick: %+v", got)
	}
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", UserID: "kid", QualityPreset: "b"}, settings); got.PresetID != presetID || got.Source != "global" {
		t.Errorf("a preset outside the profile's list must be ignored: %+v", got)
	}
	if got := svc.SelectQualityPreset(SearchOptions{MediaType: "series", UserID: "other", QualityPreset: "b"}, settings); got.PresetID != "b" {
		t.Errorf("profiles without a list may pick any offered preset: %+v", got)
	}
}

// A preset a search names is checked up front.
func TestCheckRequestedPreset(t *testing.T) {
	svc, _, id := newPresetTestService(t, func(s *config.Settings) {
		s.QualityPresets.AllowRequestOverride = false
		s.QualityPresets.PickerPresets = nil
	}, nil)
	ctx := context.Background()
	if err := svc.CheckRequestedPreset(ctx, SearchOptions{}); err != nil {
		t.Errorf("no preset requested: %v", err)
	}
	if err := svc.CheckRequestedPreset(ctx, SearchOptions{QualityPreset: "nope"}); !errors.Is(err, ErrQualityPresetUnknown) {
		t.Errorf("unknown preset: %v", err)
	}
	if err := svc.CheckRequestedPreset(ctx, SearchOptions{QualityPreset: id}); !errors.Is(err, ErrQualityPresetNotAllowed) {
		t.Errorf("not offered and no override: %v", err)
	}
	svc2, _, id2 := newPresetTestService(t, func(s *config.Settings) { s.QualityPresets.AllowRequestOverride = true }, nil)
	if err := svc2.CheckRequestedPreset(ctx, SearchOptions{QualityPreset: id2}); err != nil {
		t.Errorf("allowed preset: %v", err)
	}
	// The test preset is a Sonarr preset: a movie search cannot use it.
	if err := svc2.CheckRequestedPreset(ctx, SearchOptions{QualityPreset: id2, MediaType: "movie"}); !errors.Is(err, ErrQualityPresetWrongApp) {
		t.Errorf("sonarr preset on a movie search: %v", err)
	}
	if err := svc2.CheckRequestedPreset(ctx, SearchOptions{QualityPreset: id2, MediaType: "series"}); err != nil {
		t.Errorf("sonarr preset on a series search: %v", err)
	}
}

// The explicit audio language is promoted before the per-resolution cap: a
// German release ranked third by the preset survives a cap of two.
func TestRankPresetPassedPromotesAudioBeforeCap(t *testing.T) {
	mk := func(title, langs string) models.ScoredNZBResult {
		return models.ScoredNZBResult{NZBResult: models.NZBResult{Title: title, Attributes: map[string]string{"languages": langs, "resolution": "1080p"}}}
	}
	passed := []models.ScoredNZBResult{
		mk("Movie.2024.1080p.WEB-DL-A", "English"),
		mk("Movie.2024.1080p.WEB-DL-B", "English"),
		mk("Movie.2024.German.1080p.WEB-DL-C", "German"),
		mk("Movie.2024.1080p.WEB-DL-D", "English"),
	}
	got := rankPresetPassed(passed, "ger", 2)
	if len(got) != 2 || got[0].Title != "Movie.2024.German.1080p.WEB-DL-C" {
		var titles []string
		for _, r := range got {
			titles = append(titles, r.Title)
		}
		t.Errorf("results = %q, want the German release first and two in all", titles)
	}
}

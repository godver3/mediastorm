package indexer

import (
	"context"
	"testing"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

// A preset that only allows 2160p, for a show that only exists in 1080p.
const uhdPresetJSON = `{"app":"sonarr","name":"UHD Test","profile":{"name":"UHD Test","minFormatScore":0,"items":[
  {"name":"WEB 2160p","allowed":true,"items":["WEBDL-2160p","WEBRip-2160p"]},
  {"name":"WEBDL-1080p","allowed":false}],
  "formatItems":{"German":"g"}},
 "customFormats":[
  {"trash_id":"g","name":"German","trash_scores":{"default":1000},"specifications":[{"name":"de","implementation":"LanguageSpecification","negate":false,"required":true,"fields":{"value":4}}]}
 ]}`

func titlesOf(results []models.NZBResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Title
	}
	return out
}

func passedTitles(scored []models.ScoredNZBResult) []string {
	var out []string
	for _, r := range scored {
		if r.FilterStatus == "passed" {
			out = append(out, r.Title)
		}
	}
	return out
}

func sameTitles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// When a preset passes nothing, its fallback ranks the same results: another
// preset, or the simple ranking with its own filters. Without a fallback the
// search finds nothing, as before.
func TestQualityPresetFallback(t *testing.T) {
	svc, presets, germanID := newPresetTestService(t, nil, presetTestResults())
	uhd, err := presets.ImportJSON(context.Background(), []byte(uhdPresetJSON), arr.AppSonarr, "")
	if err != nil {
		t.Fatalf("import UHD preset: %v", err)
	}
	opts := SearchOptions{Query: "Dark S01E01", MediaType: "series", OriginalLanguage: "en"}
	scoredOpts := opts
	scoredOpts.IncludeFiltered = true
	setSeriesPreset := func(id string) {
		t.Helper()
		settings, _ := svc.cfg.Load()
		settings.QualityPresets.SeriesPreset = id
		if err := svc.cfg.Save(settings); err != nil {
			t.Fatal(err)
		}
		svc.ClearSearchCache()
	}
	setFallback := func(fallback string) {
		t.Helper()
		if _, err := presets.UpdateSettings(context.Background(), uhd.ID, qualityprofiles.SettingsUpdate{Fallback: &fallback}); err != nil {
			t.Fatal(err)
		}
		svc.ClearSearchCache()
	}
	search := func() (ranked, scored []string) {
		t.Helper()
		results, err := svc.Search(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		all, err := svc.SearchWithScoring(t.Context(), scoredOpts)
		if err != nil {
			t.Fatal(err)
		}
		return titlesOf(results), passedTitles(all)
	}

	// What German HD and the simple ranking find on their own.
	setSeriesPreset(germanID)
	germanRanked, germanScored := search()
	setSeriesPreset("")
	simpleRanked, simpleScored := search()
	if len(germanRanked) == 0 || len(simpleRanked) == 0 || sameTitles(germanRanked, simpleRanked) {
		t.Fatalf("test data must tell German HD (%v) from the simple ranking (%v)", germanRanked, simpleRanked)
	}

	setSeriesPreset(uhd.ID)
	if ranked, scored := search(); len(ranked) != 0 || len(scored) != 0 {
		t.Fatalf("UHD without fallback must find nothing: %v / %v", ranked, scored)
	}

	setFallback(germanID)
	if ranked, scored := search(); !sameTitles(ranked, germanRanked) || !sameTitles(scored, germanScored) {
		t.Errorf("fallback to German HD: got %v / %v, want %v / %v", ranked, scored, germanRanked, germanScored)
	}

	setFallback(config.QualityPresetSimple)
	if ranked, scored := search(); !sameTitles(ranked, simpleRanked) || !sameTitles(scored, simpleScored) {
		t.Errorf("fallback to the simple ranking: got %v / %v, want %v / %v", ranked, scored, simpleRanked, simpleScored)
	}

	// A preset that finds something keeps its own ranking.
	if _, err := presets.UpdateSettings(context.Background(), germanID, qualityprofiles.SettingsUpdate{Fallback: strPtr(config.QualityPresetSimple)}); err != nil {
		t.Fatal(err)
	}
	setSeriesPreset(germanID)
	if ranked, _ := search(); !sameTitles(ranked, germanRanked) {
		t.Errorf("a fallback must not change a preset that passes something: %v", ranked)
	}
}

func strPtr(s string) *string { return &s }

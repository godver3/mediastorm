package arr_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"novastream/internal/arr"
	"novastream/internal/trash"
)

// The fixtures record what Sonarr 4 / Radarr 6 reported through /api/v3/parse
// for every title in testdata/parity/*-synthetic.txt and *-handpicked.txt,
// with all TRaSH custom formats of the bundled snapshot installed. Refresh
// them with tools/arrparity (see its README) after parser changes upstream.
var parityThresholds = map[arr.App]map[arr.ParityField]float64{
	arr.AppSonarr: {
		arr.FieldQuality:       0.995,
		arr.FieldRevision:      0.995,
		arr.FieldLanguages:     0.995,
		arr.FieldReleaseGroup:  0.99,
		arr.FieldReleaseType:   0.99,
		arr.FieldCustomFormats: 0.99,
	},
	arr.AppRadarr: {
		arr.FieldQuality:       0.99,
		arr.FieldRevision:      0.995,
		arr.FieldLanguages:     0.995,
		arr.FieldReleaseGroup:  0.99,
		arr.FieldEdition:       0.95,
		arr.FieldCustomFormats: 0.99,
	},
}

func TestParityWithRecordedOracle(t *testing.T) {
	snap, err := trash.Bundled()
	if err != nil {
		t.Fatalf("bundled trash snapshot: %v", err)
	}
	for _, app := range []arr.App{arr.AppSonarr, arr.AppRadarr} {
		app := app
		t.Run(string(app), func(t *testing.T) {
			fx, err := arr.LoadParityFixture(filepath.Join("testdata", "parity", string(app)+".json.gz"))
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			if fx.TrashCommit != snap.Commit {
				t.Fatalf("fixture was recorded with TRaSH %s but the bundle is %s; recapture the fixtures", fx.TrashCommit, snap.Commit)
			}
			set, errs := arr.NewFormatSet(app, snap.CustomFormats(app))
			for _, e := range errs {
				t.Errorf("compile: %v", e)
			}
			rep := arr.CompareParity(fx, set)
			t.Log("\n" + rep.Summary())
			if rep.Total < 1000 {
				t.Fatalf("fixture too small: %d parsed titles", rep.Total)
			}
			for field, min := range parityThresholds[app] {
				if rate := rep.Rate(field); rate < min {
					shown := 0
					for _, m := range rep.Mismatches {
						if m.Field == field && shown < 15 {
							t.Logf("%s mismatch %q\n  want %s\n  got  %s", field, m.Title, m.Want, m.Got)
							shown++
						}
					}
					t.Errorf("%s parity %.2f%% is below %.2f%%", field, rate*100, min*100)
				}
			}
		})
	}
}

func TestBundledFormatsCompile(t *testing.T) {
	snap, err := trash.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []arr.App{arr.AppSonarr, arr.AppRadarr} {
		formats := snap.CustomFormats(app)
		if len(formats) < 200 {
			t.Fatalf("%s: only %d bundled formats", app, len(formats))
		}
		set, errs := arr.NewFormatSet(app, formats)
		for _, e := range errs {
			t.Errorf("%s: %v", app, e)
		}
		if set.Len() != len(formats) {
			t.Errorf("%s: compiled %d of %d formats", app, set.Len(), len(formats))
		}
	}
}

func BenchmarkEvaluateGermanProfile(b *testing.B) {
	snap, err := trash.Bundled()
	if err != nil {
		b.Fatal(err)
	}
	built, err := snap.BuildProfile(arr.AppSonarr, "dca7e5e9e99c703bcbdaaa471dd40e98", trash.BuildOptions{})
	if err != nil {
		b.Fatal(err)
	}
	ev, errs := arr.NewEvaluator(built.Profile, built.Formats)
	if len(errs) > 0 {
		b.Fatal(errs)
	}
	titles := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		titles = append(titles, fmt.Sprintf("Dark.S01E%02d.German.DL.1080p.WEB.h264-GROUP%d", i%10+1, i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evals := make([]arr.Evaluation, 0, len(titles))
		for _, title := range titles {
			rel := ev.Prepare(arr.Parse(arr.AppSonarr, title), arr.ReleaseContext{OriginalLanguage: arr.LanguageGerman})
			evals = append(evals, ev.Evaluate(rel))
		}
		ev.SortEvaluations(evals)
	}
}

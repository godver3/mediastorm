package indexer

import (
	"fmt"
	"testing"

	"novastream/config"
	"novastream/internal/arr"
	"novastream/internal/trash"
	"novastream/models"
	"novastream/services/qualityprofiles"
)

// BenchmarkEvaluatePresetSearch ranks 300 results with the bundled TRaSH
// "[German] HD Bluray + WEB" profile, as one search would.
func BenchmarkEvaluatePresetSearch(b *testing.B) {
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
	active := &qualityprofiles.Active{PresetID: "bench", Name: "bench", App: arr.AppSonarr, Evaluator: ev}
	variants := []string{"German.DL.1080p.WEB.h264-WvF", "GERMAN.1080p.BluRay.x264-iNTENTiON", "1080p.NF.WEB-DL.DDP5.1.H.264-NTb", "German.DL.720p.HDTV.x264-ITSA", "German.DL.2160p.WEB.H265-GROUP"}
	results := make([]models.NZBResult, 0, 300)
	for i := 0; i < 300; i++ {
		results = append(results, models.NZBResult{
			Title:       fmt.Sprintf("Dark.S01E%02d.%s%d", i%10+1, variants[i%len(variants)], i),
			ServiceType: models.ServiceTypeUsenet,
			SizeBytes:   int64(1+i%5) << 30,
			Attributes:  map[string]string{},
		})
	}
	svc := &Service{}
	opts := SearchOptions{MediaType: "series", OriginalLanguage: "de", RuntimeMinutes: 50}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		svc.evaluatePreset(active, results, opts, config.StreamingServicePriorityNone)
	}
}

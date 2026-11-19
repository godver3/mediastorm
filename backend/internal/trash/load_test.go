package trash

import (
	"bytes"
	"strings"
	"testing"

	"novastream/internal/arr"
)

func TestLoadSkipsMalformedFilesAndReportsThem(t *testing.T) {
	src := map[string]string{
		"sonarr/cf/good.json":                `{"trash_id":"good","name":"Good","trash_scores":{"default":10},"specifications":[{"name":"t","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":"foo"}}]}`,
		"sonarr/cf/broken.json":              `{"trash_id":"broken","name":"Broken",`,
		"sonarr/cf/noid.json":                `{"name":"No ID","specifications":[]}`,
		"sonarr/cf/future.json":              `{"trash_id":"future","name":"Future Blocker","trash_scores":{"default":-10000},"specifications":[{"name":"x","implementation":"SomeFutureSpecification","negate":false,"required":true,"fields":{"value":1}}]}`,
		"sonarr/quality-profiles/p.json":     `{"trash_id":"p","name":"P","upgradeAllowed":true,"cutoff":"WEBDL-1080p","minFormatScore":0,"items":[{"name":"WEBDL-1080p","allowed":true}],"formatItems":{"Good":"good","Broken":"broken","Future Blocker":"future"}}`,
		"sonarr/quality-profiles/bad.json":   `[1,2`,
		"sonarr/quality-size/series.json":    `{"type":"series","qualities":[{"quality":"WEBDL-1080p","min":5,"preferred":50,"max":100}]}`,
		"radarr/cf/radarr-ok.json":           `{"trash_id":"r","name":"R","specifications":[]}`,
		"radarr/quality-profiles/oops.json":  `{"trash_id":`,
		"sonarr/cf-groups/group-noid.json":   `{"name":"G","custom_formats":[]}`,
		"sonarr/quality-profiles/empty.json": `{}`,
	}
	files := mapFS{}
	for k, v := range src {
		files[k] = []byte(v)
	}
	var buf bytes.Buffer
	if err := writeBundle(files, "c0ffee", &buf); err != nil {
		t.Fatalf("a malformed file must not fail the bundle: %v", err)
	}
	snap, err := LoadArchive(&buf, "")
	if err != nil {
		t.Fatalf("a malformed file must not fail the snapshot: %v", err)
	}
	skipped := strings.Join(snap.Skipped, "\n")
	for _, want := range []string{"sonarr/cf/broken.json", "sonarr/cf/noid.json", "sonarr/quality-profiles/bad.json", "radarr/quality-profiles/oops.json", "sonarr/cf-groups/group-noid.json", "sonarr/quality-profiles/empty.json"} {
		if !strings.Contains(skipped, want) {
			t.Errorf("skipped files do not mention %s:\n%s", want, skipped)
		}
	}
	if len(snap.Skipped) != 6 {
		t.Errorf("skipped %d files, want 6:\n%s", len(snap.Skipped), skipped)
	}
	if _, ok := snap.App(arr.AppSonarr).CustomFormats["good"]; !ok {
		t.Error("valid formats must still load")
	}

	built, err := snap.BuildProfile(arr.AppSonarr, "p", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Missing) != 1 || !strings.Contains(built.Missing[0], "broken") {
		t.Errorf("missing %v, want the skipped format", built.Missing)
	}
	if len(built.Invalid) != 1 || !strings.Contains(built.Invalid[0], "Future Blocker") || !strings.Contains(built.Invalid[0], "-10000") {
		t.Errorf("invalid %v, want the format with the unknown condition and its score", built.Invalid)
	}
	ev, errs := arr.NewEvaluator(built.Profile, built.Formats)
	if ev == nil || len(errs) != 1 {
		t.Errorf("evaluator errors %v, want one for the uncompilable format", errs)
	}

	merged := Merge(snap, &Snapshot{Source: "other", Apps: map[arr.App]*AppData{}})
	if len(merged.Skipped) != len(snap.Skipped) {
		t.Errorf("merge lost skipped files: %v", merged.Skipped)
	}
}

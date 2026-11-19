package trash

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"novastream/internal/arr"
)

const (
	sonarrGermanHD = "dca7e5e9e99c703bcbdaaa471dd40e98" // [German] HD Bluray + WEB
	cfGermanDL     = "ed51973a811f51985f14e2f6f290e47a"
)

func bundled(t *testing.T) *Snapshot {
	t.Helper()
	snap, err := Bundled()
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	return snap
}

func TestBundleContents(t *testing.T) {
	snap := bundled(t)
	if snap.Commit == "" {
		t.Fatal("bundle has no commit")
	}
	for _, app := range []arr.App{arr.AppSonarr, arr.AppRadarr} {
		d := snap.App(app)
		if len(d.CustomFormats) < 200 || len(d.Profiles) < 20 || len(d.Groups) < 30 || len(d.QualitySizes) < 2 {
			t.Errorf("%s: unexpectedly small bundle: %d formats, %d profiles, %d groups, %d sizes",
				app, len(d.CustomFormats), len(d.Profiles), len(d.Groups), len(d.QualitySizes))
		}
		if len(d.Conflicts) == 0 {
			t.Errorf("%s: conflicts missing", app)
		}
	}
}

func TestBuildGermanProfile(t *testing.T) {
	snap := bundled(t)
	built, err := snap.BuildProfile(arr.AppSonarr, sonarrGermanHD, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := built.Profile
	if built.ScoreSet != "german" {
		t.Errorf("score set %q", built.ScoreSet)
	}
	if got := p.FormatScores[cfGermanDL]; got != 11000 {
		t.Errorf("German DL score %d, want 11000 from the german score set", got)
	}
	if p.Items[0].Name != "Merged QPs" || !p.Items[0].Allowed || len(p.Items[0].Qualities) != 6 {
		t.Errorf("first item %+v", p.Items[0])
	}
	if p.Propers != arr.PropersDoNotPrefer {
		t.Error("TRaSH profiles expect propers to be scored by custom formats")
	}
	if len(p.SizeLimits) == 0 {
		t.Error("series quality sizes missing")
	}
	if len(built.Missing) > 0 {
		t.Errorf("missing formats: %v", built.Missing)
	}
	ev, errs := arr.NewEvaluator(p, built.Formats)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	ctx := arr.ReleaseContext{OriginalLanguage: arr.LanguageEnglish}
	score := func(title string) arr.Evaluation {
		return ev.Evaluate(ev.Prepare(arr.Parse(arr.AppSonarr, title), ctx))
	}
	dl := score("Dark.S01E01.German.DL.1080p.WEB.h264-WvF")
	de := score("Dark.S01E01.GERMAN.1080p.WEB.h264-WvF")
	en := score("Dark.S01E01.1080p.WEB.h264-NTb")
	uhd := score("Dark.S01E01.German.DL.2160p.WEB.h265-WvF")
	if !dl.Allowed || !de.Allowed {
		t.Fatalf("German releases must be allowed: %v / %v", dl.Rejections, de.Rejections)
	}
	if dl.Score <= de.Score {
		t.Errorf("German DL (%d) should outscore German (%d)", dl.Score, de.Score)
	}
	if en.Allowed && en.Score >= de.Score {
		t.Errorf("English-only (%d) must not beat German (%d)", en.Score, de.Score)
	}
	if uhd.Allowed {
		t.Error("2160p is not part of the HD profile")
	}
}

func TestGroupSelections(t *testing.T) {
	snap := bundled(t)
	groups := snap.GroupsFor(arr.AppSonarr, sonarrGermanHD)
	if len(groups) == 0 {
		t.Fatal("no CF groups offered for the German profile")
	}
	var optional *CFGroup
	for i := range groups {
		if !bool(groups[i].Default) && len(groups[i].CustomFormats) > 0 {
			optional = &groups[i]
			break
		}
	}
	if optional == nil {
		t.Skip("no optional group in snapshot")
	}
	base, err := snap.BuildProfile(arr.AppSonarr, sonarrGermanHD, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := snap.BuildProfile(arr.AppSonarr, sonarrGermanHD, BuildOptions{Groups: map[string]GroupChoice{optional.TrashID: {Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	for _, f := range optional.CustomFormats {
		def := bool(f.Required) || (f.Default != nil && bool(*f.Default))
		if _, in := base.Profile.FormatScores[f.TrashID]; in {
			continue
		}
		if _, in := enabled.Profile.FormatScores[f.TrashID]; in != def {
			t.Errorf("format %s selected=%v, want default %v", f.Name, in, def)
		}
		if def {
			added++
		}
	}
	// Explicitly deselecting a non-required format removes it.
	for _, f := range optional.CustomFormats {
		if bool(f.Required) {
			continue
		}
		off, err := snap.BuildProfile(arr.AppSonarr, sonarrGermanHD, BuildOptions{Groups: map[string]GroupChoice{
			optional.TrashID: {Enabled: true, Formats: map[string]bool{f.TrashID: false}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, in := base.Profile.FormatScores[f.TrashID]; !in {
			if _, in := off.Profile.FormatScores[f.TrashID]; in {
				t.Errorf("deselected format %s still present", f.Name)
			}
		}
		break
	}
}

func TestScoreFor(t *testing.T) {
	cf := arr.CustomFormat{TrashScores: map[string]int{"default": 5, "german": 7}}
	if ScoreFor(cf, "german") != 7 || ScoreFor(cf, "french") != 5 || ScoreFor(cf, "") != 5 {
		t.Error("score set fallback broken")
	}
}

func TestGroupAppliesTo(t *testing.T) {
	g := CFGroup{}
	if !g.AppliesTo("x") {
		t.Error("a group without include list applies to every profile")
	}
	g.QualityProfiles.Include = map[string]string{"A": "a"}
	if !g.AppliesTo("a") || g.AppliesTo("b") {
		t.Error("include list not honoured")
	}
	g.QualityProfiles.Exclude = map[string]string{"A": "a"}
	if g.AppliesTo("a") {
		t.Error("exclude wins over include")
	}
}

func TestLoadDirAndBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sonarr/cf/a.json", `{"trash_id":"a","name":"A","trash_scores":{"default":10},"specifications":[{"name":"t","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":"foo"}}]}`)
	write("sonarr/quality-profiles/p.json", `{"trash_id":"p","name":"P","upgradeAllowed":"true","cutoff":"WEBDL-1080p","minFormatScore":0,"items":[{"name":"WEBDL-1080p","allowed":true}],"formatItems":{"A":"a"}}`)
	write("sonarr/cf-groups/g.json", `{"trash_id":"g","name":"G","default":"true","custom_formats":[{"name":"A","trash_id":"a","required":true}],"quality_profiles":{"include":{"P":"p"}}}`)
	write("sonarr/quality-size/series.json", `{"trash_id":"s","type":"series","qualities":[{"quality":"WEBDL-1080p","min":5,"preferred":50,"max":100}]}`)
	write("sonarr/conflicts.json", `{"custom_formats":[{"a":{"name":"A"},"b":{"name":"B"}}]}`)
	write("COMMIT", "deadbeef\n")
	snap, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Commit != "deadbeef" {
		t.Errorf("commit %q", snap.Commit)
	}
	built, err := snap.BuildProfile(arr.AppSonarr, "p", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if built.Profile.FormatScores["a"] != 10 || !built.Profile.UpgradeAllowed {
		t.Errorf("built %+v", built.Profile)
	}
	var buf bytes.Buffer
	if err := BuildBundle(dir, "deadbeef", &buf); err != nil {
		t.Fatal(err)
	}
	round, err := LoadArchive(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	if round.Commit != "deadbeef" || len(round.App(arr.AppSonarr).CustomFormats) != 1 || len(round.App(arr.AppSonarr).Conflicts) != 1 {
		t.Errorf("round trip lost data: %+v", round.App(arr.AppSonarr))
	}
}

func TestLoadArchiveRejectsGarbage(t *testing.T) {
	if _, err := LoadArchive(bytes.NewReader([]byte("not a tarball")), ""); err == nil {
		t.Error("expected error")
	}
}

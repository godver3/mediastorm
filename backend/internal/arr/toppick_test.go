package arr_test

import (
	"path/filepath"
	"sort"
	"testing"

	"novastream/internal/arr"
	"novastream/internal/trash"
)

// TestTopPickParity measures what matters most for streaming: whether
// mediastorm picks the same release as Sonarr/Radarr would. The recorded
// oracle facts (quality, revision, matched custom formats) are ranked with
// real TRaSH profiles and compared with mediastorm's own parse of the same
// titles, in groups of releases like one search returns. Everything else the
// comparer looks at (size, age, protocol) is identical on both sides, so a
// different winner can only come from a parsing or matching difference.
func TestTopPickParity(t *testing.T) {
	snap, err := trash.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[arr.App][]string{
		arr.AppSonarr: {"dca7e5e9e99c703bcbdaaa471dd40e98", "72dae194fc92bf828f32cde7744e51a1"}, // [German] HD Bluray + WEB, WEB-1080p
		arr.AppRadarr: {"2b90e905c99490edc7c7a5787443748b", "d1d67249d3890e49bc12e275d989a7e9"}, // [German] HD Bluray + WEB, HD Bluray + WEB
	}
	const groupSize = 10
	for app, ids := range profiles {
		fx, err := arr.LoadParityFixture(filepath.Join("testdata", "parity", string(app)+".json.gz"))
		if err != nil {
			t.Fatal(err)
		}
		var obs []arr.ParityObservation
		for _, o := range fx.Observations {
			if o.Parsed {
				obs = append(obs, o)
			}
		}
		sort.Slice(obs, func(i, j int) bool { return obs[i].Title < obs[j].Title })
		for _, id := range ids {
			built, err := snap.BuildProfile(app, id, trash.BuildOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ev, errs := arr.NewEvaluator(built.Profile, built.Formats)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			keysByName := map[string][]string{}
			for _, cf := range built.Formats {
				keysByName[cf.Name] = append(keysByName[cf.Name], cf.Key())
			}
			ours := make([]arr.Evaluation, len(obs))
			theirs := make([]arr.Evaluation, len(obs))
			for i, o := range obs {
				ours[i] = ev.Evaluate(arr.Parse(app, o.Title))
				theirs[i] = oracleEvaluation(ev, built.Profile, ours[i], o, keysByName)
			}
			groups, same, decided, scored := 0, 0, 0, 0
			var misses []string
			for start := 0; start+groupSize <= len(obs); start += groupSize {
				groups++
				a := topPick(ev, ours[start:start+groupSize])
				b := topPick(ev, theirs[start:start+groupSize])
				if b >= 0 {
					decided++
					if theirs[start+b].Score != 0 {
						scored++
					}
				}
				if a == b {
					same++
				} else if len(misses) < 10 {
					misses = append(misses, "ours "+pickTitle(obs, start, a)+" / oracle "+pickTitle(obs, start, b))
				}
			}
			rate := float64(same) / float64(groups)
			t.Logf("%s %s: same top pick in %d of %d groups (%.2f%%); %d groups have a pick, %d won with a non-zero format score", app, built.Profile.Name, same, groups, rate*100, decided, scored)
			// The corpus is mostly German, so English profiles reject many titles;
			// still require enough real decisions for the rate to mean something.
			if decided < 25 || scored < 15 {
				t.Errorf("%s %s: too few decided groups to be meaningful (%d picks, %d scored)", app, built.Profile.Name, decided, scored)
			}
			if rate < 0.99 {
				for _, m := range misses {
					t.Log(m)
				}
				t.Errorf("%s %s: top-pick parity %.2f%% is below 99%%", app, built.Profile.Name, rate*100)
			}
		}
	}
}

// oracleEvaluation is our evaluation with the oracle's parse facts.
func oracleEvaluation(ev *arr.Evaluator, profile arr.Profile, ours arr.Evaluation, o arr.ParityObservation, keysByName map[string][]string) arr.Evaluation {
	rel := ours.Release
	rel.Quality = arr.Quality{Name: o.Quality}
	rel.Revision = arr.Revision{Version: o.Version, Real: o.Real, IsRepack: o.IsRepack}
	probe := ev.Evaluate(arr.Release{App: rel.App, Quality: rel.Quality})
	out := ours
	out.Release = rel
	out.QualityRank, out.QualityGroup = probe.QualityRank, probe.QualityGroup
	out.Score = 0
	for _, name := range o.CustomFormats {
		for _, key := range keysByName[name] {
			out.Score += ev.Score(key)
		}
	}
	qualityOK := true
	for _, r := range probe.Rejections {
		if len(r) >= 8 && r[:8] == "quality " {
			qualityOK = false
		}
	}
	out.Allowed = qualityOK && out.Score >= profile.MinFormatScore
	return out
}

// topPick returns the index of the best allowed release, or -1.
func topPick(ev *arr.Evaluator, evals []arr.Evaluation) int {
	best := -1
	for i := range evals {
		if !evals[i].Allowed {
			continue
		}
		if best < 0 || ev.Compare(evals[i], evals[best]) < 0 {
			best = i
		}
	}
	return best
}

func pickTitle(obs []arr.ParityObservation, start, idx int) string {
	if idx < 0 {
		return "(none)"
	}
	return obs[start+idx].Title
}

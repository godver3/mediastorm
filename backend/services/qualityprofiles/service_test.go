package qualityprofiles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/internal/arr"
	"novastream/internal/trash"
)

// fakeRepo is a minimal TRaSH-format repository served like GitHub.
type fakeRepo struct {
	t       *testing.T
	mu      sync.Mutex
	commits map[string][]byte // sha -> tar.gz
	head    string
	fail    bool
}

type repoState struct {
	germanScore int
	extraCF     bool   // adds a new CF to the profile
	dropCF      string // removes a CF entirely
	dvRegex     string
	minScore    int
	optExtra    bool // adds a default-on format to the optional group
}

func cfJSON(id, name, regex string, scores map[string]int) string {
	s, _ := json.Marshal(scores)
	return fmt.Sprintf(`{"trash_id":%q,"name":%q,"trash_scores":%s,"specifications":[{"name":"t","implementation":"ReleaseTitleSpecification","negate":false,"required":true,"fields":{"value":%q}}]}`, id, name, s, regex)
}

func (r *fakeRepo) publish(sha string, st repoState) {
	r.t.Helper()
	dir := r.t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	if st.dvRegex == "" {
		st.dvRegex = `\bDV\b`
	}
	formatItems := `"German":"cf-german","DV":"cf-dv"`
	if st.dropCF != "cf-german" {
		write("sonarr/cf/german.json", cfJSON("cf-german", "German", `\bgerman\b`, map[string]int{"default": 0, "german": st.germanScore}))
	} else {
		formatItems = `"DV":"cf-dv"`
	}
	write("sonarr/cf/dv.json", cfJSON("cf-dv", "DV", st.dvRegex, map[string]int{"default": 1500}))
	write("sonarr/cf/lq.json", cfJSON("cf-lq", "LQ", `\bLQGROUP\b`, map[string]int{"default": -10000}))
	write("sonarr/cf/repack.json", cfJSON("cf-repack", "Repack", `\brepack\b`, map[string]int{"default": 5}))
	if st.extraCF {
		write("sonarr/cf/atmos.json", cfJSON("cf-atmos", "Atmos", `\batmos\b`, map[string]int{"default": 300}))
		formatItems += `,"Atmos":"cf-atmos"`
	}
	write("sonarr/quality-profiles/de.json", fmt.Sprintf(`{"trash_id":"prof-de","name":"German HD","trash_score_set":"german","upgradeAllowed":true,"cutoff":"WEB 1080p","minFormatScore":%d,"cutoffFormatScore":10000,"minUpgradeFormatScore":1,
	  "items":[{"name":"WEB 1080p","allowed":true,"items":["WEBDL-1080p","WEBRip-1080p"]},{"name":"HDTV-1080p","allowed":false}],"formatItems":{%s}}`, st.minScore, formatItems))
	write("sonarr/cf-groups/unwanted.json", `{"trash_id":"grp-unwanted","name":"Unwanted","default":"true","custom_formats":[{"name":"LQ","trash_id":"cf-lq","required":true}],"quality_profiles":{"include":{"German HD":"prof-de"}}}`)
	optional := `{"name":"Repack","trash_id":"cf-repack","required":false,"default":true}`
	if st.optExtra {
		write("sonarr/cf/proper.json", cfJSON("cf-proper", "Proper", `\bproper\b`, map[string]int{"default": 7}))
		optional += `,{"name":"Proper","trash_id":"cf-proper","required":false,"default":true}`
	}
	write("sonarr/cf-groups/optional.json", `{"trash_id":"grp-opt","name":"Optional","custom_formats":[`+optional+`],"quality_profiles":{"include":{"German HD":"prof-de"}}}`)
	write("sonarr/quality-size/series.json", `{"trash_id":"qs","type":"series","qualities":[{"quality":"WEBDL-1080p","min":5,"preferred":50,"max":1000}]}`)
	var buf bytes.Buffer
	if err := trash.BuildBundle(dir, sha, &buf); err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits[sha] = buf.Bytes()
	r.head = sha
}

func (r *fakeRepo) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.fail {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		switch {
		case strings.HasPrefix(req.URL.Path, "/repos/test/guides/commits/"):
			ref := strings.TrimPrefix(req.URL.Path, "/repos/test/guides/commits/")
			if _, ok := r.commits[ref]; ok {
				fmt.Fprint(w, ref)
				return
			}
			fmt.Fprint(w, r.head)
		case strings.HasPrefix(req.URL.Path, "/test/guides/tar.gz/"):
			sha := strings.TrimPrefix(req.URL.Path, "/test/guides/tar.gz/")
			data, ok := r.commits[sha]
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Write(data)
		default:
			http.NotFound(w, req)
		}
	}))
}

const (
	sha1 = "1111111111111111111111111111111111111111"
	sha2 = "2222222222222222222222222222222222222222"
	sha3 = "3333333333333333333333333333333333333333"
)

func newTestService(t *testing.T) (*Service, *fakeRepo, *time.Time) {
	t.Helper()
	repo := &fakeRepo{t: t, commits: map[string][]byte{}}
	repo.publish(sha1, repoState{germanScore: 1000})
	srv := repo.server()
	t.Cleanup(srv.Close)
	store := NewMemoryStore()
	f := NewFetcher(store, srv.Client())
	f.GitHubAPI, f.Codeload = srv.URL, srv.URL
	sources := []Source{{ID: "test", Name: "Test", Type: SourceGitHub, Repo: "test/guides", Ref: "master", Enabled: true}}
	svc := NewService(store, f, func() []Source { return sources })
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	return svc, repo, &now
}

func importGerman(t *testing.T, svc *Service, mode SyncMode) *Preset {
	t.Helper()
	sync := DefaultSyncSettings()
	sync.Mode = mode
	p, _, err := svc.ImportTrash(context.Background(), TrashImport{App: arr.AppSonarr, ProfileTrashID: "prof-de", Sync: &sync})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return p
}

func resolvedScore(t *testing.T, p *Preset, key string) (int, bool) {
	t.Helper()
	r, err := Resolve(p)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := r.Profile.FormatScores[key]
	return v, ok
}

func TestImportTrashResolvesScoresAndGroups(t *testing.T) {
	svc, _, _ := newTestService(t)
	p := importGerman(t, svc, SyncNotify)
	if p.Upstream.Commit != sha1 || p.Name != "German HD" {
		t.Fatalf("unexpected preset %s %s", p.Upstream.Commit, p.Name)
	}
	if v, _ := resolvedScore(t, p, "cf-german"); v != 1000 {
		t.Errorf("german score from score set = %d", v)
	}
	if v, ok := resolvedScore(t, p, "cf-lq"); !ok || v != -10000 {
		t.Errorf("required format of a default group must be scored, got %d %v", v, ok)
	}
	if _, ok := resolvedScore(t, p, "cf-repack"); ok {
		t.Error("formats of a non-default group stay off until enabled")
	}
	p, err := svc.SetOverride(context.Background(), p.ID, "group:grp-opt", json.RawMessage(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := resolvedScore(t, p, "cf-repack"); !ok || v != 5 {
		t.Errorf("enabling the group selects its default formats, got %d %v", v, ok)
	}
	a, err := svc.Active(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev := a.Evaluator.Evaluate(a.Evaluator.Prepare(arr.Parse(arr.AppSonarr, "Dark.S01E01.German.1080p.WEB.h264-WvF"), arr.ReleaseContext{}))
	if !ev.Allowed || ev.Score != 1000 {
		t.Errorf("evaluation %+v", ev)
	}
}

func TestNotifyModeStagesAndReportsConflicts(t *testing.T) {
	svc, repo, now := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncNotify)
	if _, err := svc.SetOverride(ctx, p.ID, "score:cf-german", json.RawMessage(`2500`)); err != nil {
		t.Fatal(err)
	}
	repo.publish(sha2, repoState{germanScore: 1200})
	*now = now.Add(25 * time.Hour)
	res, err := svc.Check(ctx, p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Staged || res.Applied {
		t.Fatalf("notify mode must stage: %+v", res)
	}
	if len(res.Plan.Conflicts) != 1 || res.Plan.Conflicts[0].Path != "score:cf-german" ||
		res.Plan.Conflicts[0].UpstreamBefore != "1000" || res.Plan.Conflicts[0].UpstreamAfter != "1200" || res.Plan.Conflicts[0].Yours != "2500" {
		t.Fatalf("conflict not reported correctly: %+v", res.Plan.Conflicts)
	}
	if res.Preset.Upstream.Commit != sha1 {
		t.Error("staging must not change the applied upstream")
	}
	p, err = svc.ApplyPending(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Upstream.Commit != sha2 || len(p.History) != 1 || len(p.Conflicts) != 1 {
		t.Fatalf("apply: commit %s history %d conflicts %d", p.Upstream.Commit, len(p.History), len(p.Conflicts))
	}
	if v, _ := resolvedScore(t, p, "cf-german"); v != 2500 {
		t.Errorf("the user's score must stay in effect until resolved, got %d", v)
	}
	p, err = svc.ResolveConflict(ctx, p.ID, "score:cf-german", TakeUpstream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := resolvedScore(t, p, "cf-german"); v != 1200 || len(p.Conflicts) != 0 {
		t.Errorf("take upstream: score %d conflicts %d", v, len(p.Conflicts))
	}
}

func TestKeepYoursAcknowledgesUpstreamChange(t *testing.T) {
	svc, repo, now := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	svc.SetOverride(ctx, p.ID, "score:cf-german", json.RawMessage(`2500`))
	repo.publish(sha2, repoState{germanScore: 1200})
	*now = now.Add(time.Hour)
	if _, err := svc.Check(ctx, p.ID, false); err != nil {
		t.Fatal(err)
	}
	p, err := svc.ResolveConflict(ctx, p.ID, "score:cf-german", KeepYours, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Overrides.Base["score:cf-german"] != "1200" {
		t.Errorf("keep yours must record the new upstream base, got %q", p.Overrides.Base["score:cf-german"])
	}
	// A further sync without upstream changes must not raise it again.
	repo.publish(sha3, repoState{germanScore: 1200})
	res, err := svc.Check(ctx, p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Conflicts) != 0 {
		t.Errorf("acknowledged conflict came back: %+v", res.Plan.Conflicts)
	}
}

func TestFollowPoliciesFreezeParts(t *testing.T) {
	svc, repo, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	s := p.Sync
	s.Follow.Scores = false // keep scores, take regex fixes
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &s}); err != nil {
		t.Fatal(err)
	}
	repo.publish(sha2, repoState{germanScore: 1200, dvRegex: `\b(DV|DoVi)\b`, minScore: 100})
	if _, err := svc.Check(ctx, p.ID, false); err != nil {
		t.Fatal(err)
	}
	p, _ = svc.Get(ctx, p.ID)
	if v, _ := resolvedScore(t, p, "cf-german"); v != 1000 {
		t.Errorf("scores are frozen, got %d", v)
	}
	if got := p.Upstream.Formats["cf-dv"].Specifications[0].Fields["value"]; got != `\b(DV|DoVi)\b` {
		t.Errorf("conditions follow upstream, got %v", got)
	}
	if p.Upstream.Profile.MinFormatScore != 100 {
		t.Errorf("profile follows upstream, got min score %d", p.Upstream.Profile.MinFormatScore)
	}

	// Per-format policy: freeze DV's conditions only.
	frozen := false
	s.Follow.Scores = true
	s.PerFormat = map[string]FieldPolicy{"cf-dv": {Conditions: &frozen}}
	svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &s})
	repo.publish(sha3, repoState{germanScore: 1300, dvRegex: `\bDOLBYVISION\b`})
	svc.Check(ctx, p.ID, false)
	p, _ = svc.Get(ctx, p.ID)
	if got := p.Upstream.Formats["cf-dv"].Specifications[0].Fields["value"]; got != `\b(DV|DoVi)\b` {
		t.Errorf("per-format freeze ignored: %v", got)
	}
	if v, _ := resolvedScore(t, p, "cf-german"); v != 1300 {
		t.Errorf("scores follow again, got %d", v)
	}
}

func TestNewFormatPolicies(t *testing.T) {
	for _, policy := range []NewFormatPolicy{NewFormatsEnable, NewFormatsDisable, NewFormatsReview} {
		t.Run(string(policy), func(t *testing.T) {
			svc, repo, _ := newTestService(t)
			ctx := context.Background()
			p := importGerman(t, svc, SyncAuto)
			s := p.Sync
			s.NewFormats = policy
			svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &s})
			repo.publish(sha2, repoState{germanScore: 1000, extraCF: true})
			res, err := svc.Check(ctx, p.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Plan.NewFormats) != 1 || res.Plan.NewFormats[0] != "cf-atmos" {
				t.Fatalf("new formats %v", res.Plan.NewFormats)
			}
			p, _ = svc.Get(ctx, p.ID)
			_, active := resolvedScore(t, p, "cf-atmos")
			switch policy {
			case NewFormatsEnable:
				if !active {
					t.Error("enable: new format should be active")
				}
			case NewFormatsDisable:
				if active || !p.Overrides.Disabled["cf-atmos"] {
					t.Error("disable: new format must be added switched off")
				}
			case NewFormatsReview:
				if active || len(p.ReviewFormats) != 1 {
					t.Fatal("review: new format must wait for review")
				}
				p, _ = svc.ReviewNewFormats(ctx, p.ID, []string{"cf-atmos"}, true)
				if _, ok := resolvedScore(t, p, "cf-atmos"); !ok {
					t.Error("approved format should be active")
				}
			}
		})
	}
}

func TestRemovedUpstreamFormatIsKeptWhenCustomised(t *testing.T) {
	svc, repo, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	svc.SetOverride(ctx, p.ID, "score:cf-german", json.RawMessage(`777`))
	repo.publish(sha2, repoState{dropCF: "cf-german"})
	res, err := svc.Check(ctx, p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Removed) != 1 || !res.Plan.Removed[0].Kept {
		t.Fatalf("removed %+v", res.Plan.Removed)
	}
	p, _ = svc.Get(ctx, p.ID)
	if v, ok := resolvedScore(t, p, "cf-german"); !ok || v != 777 {
		t.Errorf("customised format must survive as own format, got %d %v", v, ok)
	}
	if len(p.Removed) != 1 {
		t.Error("removal must be recorded")
	}
}

func TestPinnedModeAndRollback(t *testing.T) {
	svc, repo, now := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	repo.publish(sha2, repoState{germanScore: 1200})
	svc.Check(ctx, p.ID, false)
	p, _ = svc.Get(ctx, p.ID)
	if p.Upstream.Commit != sha2 || len(p.History) != 1 {
		t.Fatalf("auto sync: %s, history %d", p.Upstream.Commit, len(p.History))
	}
	p, err := svc.Rollback(ctx, p.ID, p.History[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Upstream.Commit != sha1 || p.Sync.Mode != SyncPinned || p.Sync.PinnedCommit != sha1 {
		t.Fatalf("rollback: commit %s mode %s pin %s", p.Upstream.Commit, p.Sync.Mode, p.Sync.PinnedCommit)
	}
	repo.publish(sha3, repoState{germanScore: 9999})
	*now = now.Add(48 * time.Hour)
	if res, _ := svc.SyncDue(ctx, false); res.Checked != 0 {
		t.Errorf("a preset pinned to its current commit is not due, checked %d", res.Checked)
	}
	res, err := svc.Check(ctx, p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpToDate || res.Preset.Upstream.Commit != sha1 {
		t.Errorf("pinned preset moved: %+v", res.Preset.Upstream.Commit)
	}
}

func TestScheduleAndOfflineFallback(t *testing.T) {
	svc, repo, now := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncNotify)
	if res, _ := svc.SyncDue(ctx, false); res.Checked != 1 {
		t.Fatalf("never-checked preset is due, checked %d", res.Checked)
	}
	if res, _ := svc.SyncDue(ctx, false); res.Checked != 0 {
		t.Error("checked preset is not due again within a day")
	}
	*now = now.Add(25 * time.Hour)
	repo.mu.Lock()
	repo.fail = true
	repo.mu.Unlock()
	// An unreachable source fails the check; it must not fall back to the
	// bundled snapshot, which would look like an upstream change.
	if _, err := svc.Check(ctx, p.ID, false); err == nil {
		t.Error("expected an error when the source is down")
	}
	p, _ = svc.Get(ctx, p.ID)
	if p.LastCheckError == "" {
		t.Error("the error must be recorded on the preset")
	}
	if p.Upstream.Commit != sha1 || p.Pending != nil || len(p.History) != 0 {
		t.Errorf("a failed check must leave the preset alone: commit %s pending %v history %d", p.Upstream.Commit, p.Pending != nil, len(p.History))
	}
}

// A new upstream commit that changes nothing for the preset moves the
// recorded commit only: no history entry, no pending update, and the
// search caches stay.
func TestCheckWithoutChangesIsQuiet(t *testing.T) {
	for _, mode := range []SyncMode{SyncAuto, SyncNotify} {
		svc, repo, now := newTestService(t)
		ctx := context.Background()
		p := importGerman(t, svc, mode)
		if _, err := svc.SetOverride(ctx, p.ID, "score:cf-german", json.RawMessage(`2500`)); err != nil {
			t.Fatal(err)
		}
		repo.publish(sha2, repoState{germanScore: 1200})
		if _, err := svc.Check(ctx, p.ID, true); err != nil {
			t.Fatal(err)
		}
		p, _ = svc.Get(ctx, p.ID)
		history, conflicts, updated := len(p.History), len(p.Conflicts), p.UpdatedAt
		if conflicts != 1 {
			t.Fatalf("%s: expected one conflict, got %d", mode, conflicts)
		}
		changes := 0
		svc.SetOnChange(func(string) { changes++ })

		repo.publish(sha3, repoState{germanScore: 1200}) // same content, new commit
		*now = now.Add(25 * time.Hour)
		res, err := svc.Check(ctx, p.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		p, _ = svc.Get(ctx, p.ID)
		if !res.UpToDate || p.Upstream.Commit != sha3 {
			t.Errorf("%s: up to date %v, commit %s", mode, res.UpToDate, p.Upstream.Commit)
		}
		if len(p.History) != history || p.Pending != nil || len(p.Conflicts) != conflicts {
			t.Errorf("%s: history %d→%d, pending %v, conflicts %d→%d", mode, history, len(p.History), p.Pending != nil, conflicts, len(p.Conflicts))
		}
		if changes != 0 || !p.UpdatedAt.Equal(updated) {
			t.Errorf("%s: a check without changes must not clear caches (%d) or touch UpdatedAt", mode, changes)
		}
		if p.LastCheckedAt == nil || !p.LastCheckedAt.Equal(*now) {
			t.Errorf("%s: the check time must still be recorded", mode)
		}
	}
}

func TestDryRunShowsRankingChange(t *testing.T) {
	svc, repo, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncNotify)
	repo.publish(sha2, repoState{germanScore: 1000, dvRegex: `\bNEVERMATCH\b`})
	titles := []string{"Show.S01E01.German.1080p.WEB.h264-A", "Show.S01E01.DV.1080p.WEB.h265-B"}
	res, err := svc.DryRun(ctx, p.ID, titles, arr.ReleaseContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Before[0].Title != titles[1] || res.After[0].Title != titles[0] {
		t.Errorf("dry run ranking: before %s after %s", res.Before[0].Title, res.After[0].Title)
	}
	p, _ = svc.Get(ctx, p.ID)
	if p.Upstream.Commit != sha1 || p.Pending != nil {
		t.Error("dry run must not change the preset")
	}
}

func TestMultipleSourcesPrecedence(t *testing.T) {
	svc, repo, _ := newTestService(t)
	override := &fakeRepo{t: t, commits: map[string][]byte{}}
	override.publish(sha3, repoState{germanScore: 4242})
	srv2 := override.server()
	defer srv2.Close()
	// Second source with higher precedence served from another host.
	f := svc.Fetcher()
	sources := []Source{
		{ID: "fork", Name: "Fork", Type: SourceURL, URL: srv2.URL + "/test/guides/tar.gz/" + sha3, Priority: 0, Enabled: true},
		{ID: "test", Name: "Test", Type: SourceGitHub, Repo: "test/guides", Ref: "master", Priority: 1, Enabled: true},
	}
	svc.sources = func() []Source { return sources }
	_ = repo
	_ = f
	p := importGerman(t, svc, SyncManual)
	if v, _ := resolvedScore(t, p, "cf-german"); v != 4242 {
		t.Errorf("higher-precedence source must win, got %d", v)
	}
	if !strings.Contains(p.Upstream.Commit, "fork@sha256:") || !strings.Contains(p.Upstream.Commit, "test@"+sha1) {
		t.Errorf("merged commit %q", p.Upstream.Commit)
	}
	commits := ParseCommits(p.Upstream.Commit, sources)
	if commits["test"] != sha1 || !strings.HasPrefix(commits["fork"], "sha256:") {
		t.Errorf("parse commits %v", commits)
	}

	// Every format records which source it came from and which sources it
	// shadows, so overlapping trash_ids can be explained.
	if got := strings.Join(p.Upstream.Sources, ","); got != "fork,test" {
		t.Errorf("sources %q", got)
	}
	if p.Upstream.FormatSources["cf-german"] != "fork" || strings.Join(p.Upstream.Shadowed["cf-german"], ",") != "test" {
		t.Errorf("attribution %v shadowed %v", p.Upstream.FormatSources, p.Upstream.Shadowed)
	}
	r, err := Resolve(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Views {
		if v.Key == "cf-german" && (v.Source != "fork" || len(v.Shadowed) != 1) {
			t.Errorf("resolved view %+v", v)
		}
	}

	// Sync keeps the attribution of formats whose conditions are frozen.
	s := p.Sync
	s.PerFormat = map[string]FieldPolicy{"cf-german": {Conditions: boolPtr(false)}}
	s.Mode = SyncAuto
	svc.UpdateSettings(context.Background(), p.ID, SettingsUpdate{Sync: &s})
	sources = sources[1:]
	if _, err := svc.Check(context.Background(), p.ID, true); err != nil {
		t.Fatal(err)
	}
	p, _ = svc.Get(context.Background(), p.ID)
	if p.Upstream.FormatSources["cf-german"] != "fork" || p.Upstream.FormatSources["cf-dv"] != "test" {
		t.Errorf("after sync: %v", p.Upstream.FormatSources)
	}
}

func TestImportJSONShapes(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	list := `[` + cfJSON("x1", "X1", `\bfoo\b`, map[string]int{"default": 50}) + `]`
	p, err := svc.ImportJSON(ctx, []byte(list), arr.AppSonarr, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := resolvedScore(t, p, "x1"); v != 50 || p.Sync.Mode != SyncManual {
		t.Errorf("format list import: score %d mode %s", v, p.Sync.Mode)
	}
	exported, err := Export(p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.ImportJSON(ctx, exported, "", "copy")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := resolvedScore(t, again, "x1"); v != 50 || again.Name != "copy" {
		t.Errorf("export round trip: %d %s", v, again.Name)
	}
	if _, err := svc.ImportJSON(ctx, []byte(`[{"name":"bad","specifications":[{"name":"r","implementation":"ReleaseTitleSpecification","required":true,"fields":{"value":"("}}]}]`), arr.AppSonarr, ""); err == nil {
		t.Error("invalid regex must be rejected")
	}
}

func TestOwnFormatsAndValidation(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncNotify)
	own := arr.CustomFormat{Name: "Lookbehind", Specifications: []arr.Specification{{Name: "lb", Implementation: arr.SpecReleaseTitle, Required: true,
		Fields: map[string]any{"value": `(?<!WEB-)\bDL\b`}}}}
	p, err := svc.AddFormat(ctx, p.ID, own, 300)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	for k := range p.Overrides.Formats {
		key = k
	}
	if v, ok := resolvedScore(t, p, key); !ok || v != 300 {
		t.Fatalf("own format score %d %v", v, ok)
	}
	if _, err := svc.SetOverride(ctx, p.ID, "items", json.RawMessage(`[{"name":"WEBDL-1080p","allowed":false}]`)); err == nil {
		t.Error("a profile without an allowed quality must be rejected")
	}
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &SyncSettings{Mode: "sometimes"}}); err == nil {
		t.Error("invalid sync mode must be rejected")
	}
	p, err = svc.RemoveFormat(ctx, p.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resolvedScore(t, p, key); ok {
		t.Error("removed own format still active")
	}
}

func TestArrImport(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"appName":"Sonarr","version":"4.0.0"}`)
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":3,"name":"HD","upgradeAllowed":true,"cutoff":3,"minFormatScore":10,"items":[
		  {"quality":{"id":4,"name":"HDTV-720p"},"items":[],"allowed":false},
		  {"id":1001,"name":"WEB 1080p","items":[{"quality":{"id":15,"name":"WEBRip-1080p"},"items":[],"allowed":true},{"quality":{"id":3,"name":"WEBDL-1080p"},"items":[],"allowed":true}],"allowed":true}],
		  "formatItems":[{"format":7,"name":"German","score":900},{"format":8,"name":"Unused","score":0}]}]`)
	})
	mux.HandleFunc("/api/v3/customformat", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":7,"name":"German","specifications":[{"name":"g","implementation":"LanguageSpecification","negate":false,"required":true,"fields":[{"name":"value","value":4}]}]},
		  {"id":8,"name":"Unused","specifications":[]}]`)
	})
	mux.HandleFunc("/api/v3/qualitydefinition", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"quality":{"id":3,"name":"WEBDL-1080p"},"minSize":5,"preferredSize":50,"maxSize":100}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	app, profiles, err := svc.ArrProfiles(ctx, srv.URL, "k")
	if err != nil || app != arr.AppSonarr || len(profiles) != 1 {
		t.Fatalf("profiles: %v %s %d", err, app, len(profiles))
	}
	p, err := svc.ImportArr(ctx, ArrImport{URL: srv.URL, APIKey: "k", ProfileID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if p.Upstream.Profile.Items[0].Name != "WEB 1080p" || p.Upstream.Profile.Items[0].Qualities[0] != "WEBDL-1080p" {
		t.Errorf("items must be reversed to most-preferred first: %+v", p.Upstream.Profile.Items)
	}
	if v, _ := resolvedScore(t, p, "name:german"); v != 900 {
		t.Errorf("score %d", v)
	}
	if p.Redacted().Origin.ArrAPIKey == "k" {
		t.Error("api key must be redacted")
	}
	if _, err := svc.ImportArr(ctx, ArrImport{URL: srv.URL, APIKey: "wrong", ProfileID: 3}); err == nil {
		t.Error("bad api key must fail")
	}
	// The connection can move; an empty key keeps the stored one.
	p, err = svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Connection: &ArrConnection{URL: srv.URL + "/sonarr/"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Origin.ArrURL != srv.URL+"/sonarr" || p.Origin.ArrAPIKey != "k" {
		t.Errorf("connection %q key kept %v", p.Origin.ArrURL, p.Origin.ArrAPIKey == "k")
	}
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Connection: &ArrConnection{URL: "ftp://x"}}); err == nil {
		t.Error("an invalid URL must be refused")
	}
	// Another server needs the key entered again: the stored one is never
	// sent to a new address.
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Connection: &ArrConnection{URL: "http://other.example:8989"}}); err == nil {
		t.Error("the stored key went to another server")
	}
	if p, err = svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Connection: &ArrConnection{URL: "http://other.example:8989", APIKey: "k2"}}); err != nil || p.Origin.ArrAPIKey != "k2" {
		t.Errorf("new server with its key: %v", err)
	}
}

func TestHeldFormatsStayOffInCopiesAndExports(t *testing.T) {
	svc, repo, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	s := p.Sync
	s.NewFormats = NewFormatsReview
	svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &s})
	repo.publish(sha2, repoState{germanScore: 1000, extraCF: true})
	if _, err := svc.Check(ctx, p.ID, false); err != nil {
		t.Fatal(err)
	}
	p, _ = svc.Get(ctx, p.ID)
	if len(p.ReviewFormats) != 1 {
		t.Fatalf("setup: review queue %v", p.ReviewFormats)
	}

	cp, err := svc.Duplicate(ctx, p.ID, "Copy")
	if err != nil {
		t.Fatal(err)
	}
	if _, active := resolvedScore(t, cp, "cf-atmos"); active || !cp.Overrides.Disabled["cf-atmos"] {
		t.Error("a duplicate must keep a held format switched off")
	}

	data, err := Export(p)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := svc.ImportJSON(ctx, data, p.App, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, active := resolvedScore(t, imported, "cf-atmos"); active {
		t.Error("an exported preset must not activate a held format on import")
	}
	if p, _ = svc.Get(ctx, p.ID); p.Overrides.Disabled["cf-atmos"] {
		t.Error("exporting must not change the source preset")
	}
}

func boolPtr(v bool) *bool { return &v }

func TestResolveReportsTrashConflicts(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncManual)
	p.Upstream.ConflictSets = []trash.Conflict{{Formats: map[string]string{"cf-german": "German", "cf-dv": "DV"}}}
	r, err := Resolve(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conflicts) != 1 || strings.Join(r.Conflicts[0], "+") != "DV+German" {
		t.Fatalf("both conflicting formats enabled: %v", r.Conflicts)
	}
	p, err = svc.SetDisabled(ctx, p.ID, "cf-dv", true)
	if err != nil {
		t.Fatal(err)
	}
	p.Upstream.ConflictSets = []trash.Conflict{{Formats: map[string]string{"cf-german": "German", "cf-dv": "DV"}}}
	if r, _ = Resolve(p); len(r.Conflicts) != 0 {
		t.Errorf("a switched-off format no longer conflicts: %v", r.Conflicts)
	}
}

func TestAudience(t *testing.T) {
	for name, want := range map[string]Audience{
		"[German] Anime HD Bluray + WEB": AudienceAnime,
		"[Anime] Remux-1080p":            AudienceAnime,
		"ANIME":                          AudienceAnime,
		"[German] HD Bluray + WEB":       AudienceAll,
		"WEB-1080p":                      AudienceAll,
	} {
		if got := AudienceForProfileName(name); got != want {
			t.Errorf("%q: audience %q, want %q", name, got, want)
		}
	}
	cases := []struct {
		a           Audience
		anime, want bool
	}{
		{AudienceAll, true, true}, {AudienceAll, false, true},
		{AudienceAnime, true, true}, {AudienceAnime, false, false},
		{AudienceNonAnime, true, false}, {AudienceNonAnime, false, true},
	}
	for _, c := range cases {
		if got := c.a.Allows(c.anime); got != c.want {
			t.Errorf("%q allows anime=%v: %v", c.a, c.anime, got)
		}
	}
	if Audience("cartoons").Valid() {
		t.Error("unknown audience must be invalid")
	}
}

func TestAudienceIsSetEditedAndCarried(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncManual)
	if p.Audience != AudienceAll {
		t.Fatalf("a non-anime TRaSH profile is for all titles, got %q", p.Audience)
	}
	anime := AudienceAnime
	p, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Audience: &anime})
	if err != nil || p.Audience != AudienceAnime {
		t.Fatalf("set audience: %v %q", err, p.Audience)
	}
	if a, err := svc.Active(ctx, p.ID); err != nil || a.Audience != AudienceAnime {
		t.Fatalf("active audience: %v %+v", err, a)
	}
	bad := Audience("cartoons")
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Audience: &bad}); err == nil {
		t.Error("invalid audience accepted")
	}
	// Copies and exports keep it.
	cp, err := svc.Duplicate(ctx, p.ID, "Copy")
	if err != nil || cp.Audience != AudienceAnime {
		t.Fatalf("duplicate: %v %q", err, cp.Audience)
	}
	data, err := Export(p)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := svc.ImportJSON(ctx, data, "", "")
	if err != nil || imported.Audience != AudienceAnime {
		t.Fatalf("export round trip: %v %q", err, imported.Audience)
	}
	// A pasted TRaSH profile is tagged by its name.
	pasted, err := svc.ImportJSON(ctx, []byte(`{"app":"sonarr","name":"[Anime] Remux-1080p","profile":{"name":"[Anime] Remux-1080p","items":[{"name":"WEBDL-1080p","allowed":true}],"formatItems":{}},"customFormats":[]}`), arr.AppSonarr, "")
	if err != nil || pasted.Audience != AudienceAnime {
		t.Fatalf("pasted anime profile: %v %q", err, pasted.Audience)
	}
}

// New TRaSH and Sonarr/Radarr presets apply upstream updates automatically.
func TestNewPresetsSyncAutomaticallyByDefault(t *testing.T) {
	svc, _, _ := newTestService(t)
	p, _, err := svc.ImportTrash(context.Background(), TrashImport{App: arr.AppSonarr, ProfileTrashID: "prof-de"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Sync.Mode != SyncAuto || p.Sync.Schedule != ScheduleDaily {
		t.Fatalf("default sync = %+v, want auto, daily", p.Sync)
	}
}

// Only saves that change how a preset ranks renew the compiled preset and
// clear the search caches.
func TestOnlyRankingChangesClearCaches(t *testing.T) {
	svc, _, now := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	changes := 0
	svc.SetOnChange(func(string) { changes++ })
	updated := p.UpdatedAt
	*now = now.Add(time.Hour)
	p, err := svc.Mutate(ctx, p.ID, func(p *Preset) error {
		p.Sync.Schedule = ScheduleWeekly
		checked := *now
		p.LastCheckedAt = &checked
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changes != 0 || !p.UpdatedAt.Equal(updated) {
		t.Errorf("sync settings and check times must not clear caches: %d changes, updated %v", changes, p.UpdatedAt)
	}
	if p, err = svc.SetOverride(ctx, p.ID, "score:cf-german", json.RawMessage(`2500`)); err != nil {
		t.Fatal(err)
	}
	if changes != 1 || !p.UpdatedAt.Equal(*now) {
		t.Errorf("a score change must clear caches once: %d changes, updated %v", changes, p.UpdatedAt)
	}
}

// A format upstream adds to a group the user switched on follows the
// preset's new-format policy like any other new format.
func TestNewFormatInEnabledGroupFollowsPolicy(t *testing.T) {
	svc, repo, _ := newTestService(t)
	ctx := context.Background()
	p := importGerman(t, svc, SyncAuto)
	s := p.Sync
	s.NewFormats = NewFormatsDisable
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &s}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetOverride(ctx, p.ID, "group:grp-opt", json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	repo.publish(sha2, repoState{germanScore: 1000, optExtra: true})
	res, err := svc.Check(ctx, p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.NewFormats) != 1 || res.Plan.NewFormats[0] != "cf-proper" {
		t.Fatalf("new formats %v", res.Plan.NewFormats)
	}
	if _, on := resolvedScore(t, res.Preset, "cf-proper"); on {
		t.Error("the new format must be added switched off")
	}
}

// Sync settings given on import are completed and validated.
func TestImportValidatesSyncSettings(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	bad := SyncSettings{Mode: SyncPinned}
	if _, _, err := svc.ImportTrash(ctx, TrashImport{App: arr.AppSonarr, ProfileTrashID: "prof-de", Sync: &bad}); err == nil {
		t.Error("pinned without a commit must be refused")
	}
	partial := SyncSettings{Mode: SyncNotify}
	p, _, err := svc.ImportTrash(ctx, TrashImport{App: arr.AppSonarr, ProfileTrashID: "prof-de", Sync: &partial})
	if err != nil {
		t.Fatal(err)
	}
	if p.Sync.Schedule != ScheduleDaily || p.Sync.NewFormats != NewFormatsEnable {
		t.Errorf("left-out sync settings must get defaults: %+v", p.Sync)
	}
}

// A check whose preset moved to another instance while fetching applies
// nothing: what it fetched came from the old one.
func TestCheckSkipsWhenConnectionChanged(t *testing.T) {
	var svc *Service
	var presetID string
	score, moveDuringFetch := 900, false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"appName":"Sonarr","version":"4.0.0"}`)
	})
	var srvURL string
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		if moveDuringFetch {
			moveDuringFetch = false
			if _, err := svc.UpdateSettings(context.Background(), presetID, SettingsUpdate{Connection: &ArrConnection{URL: srvURL + "/moved"}}); err != nil {
				t.Errorf("move: %v", err)
			}
		}
		fmt.Fprintf(w, `[{"id":3,"name":"HD","items":[{"quality":{"id":3,"name":"WEBDL-1080p"},"items":[],"allowed":true}],
		  "formatItems":[{"format":7,"name":"German","score":%d}]}]`, score)
	})
	mux.HandleFunc("/api/v3/customformat", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":7,"name":"German","specifications":[{"name":"g","implementation":"LanguageSpecification","negate":false,"required":true,"fields":[{"name":"value","value":4}]}]}]`)
	})
	mux.HandleFunc("/api/v3/qualitydefinition", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	svc, _, _ = newTestService(t)
	ctx := context.Background()
	p, err := svc.ImportArr(ctx, ArrImport{URL: srv.URL, APIKey: "k", ProfileID: 3})
	if err != nil {
		t.Fatal(err)
	}
	presetID = p.ID
	auto := p.Sync
	auto.Mode = SyncAuto
	if _, err := svc.UpdateSettings(ctx, p.ID, SettingsUpdate{Sync: &auto}); err != nil {
		t.Fatal(err)
	}

	score, moveDuringFetch = 50, true
	res, err := svc.Check(ctx, p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.Staged {
		t.Error("applied what the old instance returned")
	}
	p, _ = svc.Get(ctx, p.ID)
	if v, _ := resolvedScore(t, p, "name:german"); v != 900 {
		t.Errorf("score %d, want the previous 900", v)
	}
	if !strings.HasSuffix(p.Origin.ArrURL, "/moved") {
		t.Errorf("connection %q", p.Origin.ArrURL)
	}
}

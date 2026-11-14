package sourceavailability

import (
	"context"
	"errors"
	"fmt"
	"novastream/models"
	"testing"
	"time"
)

func requestCheck(t *testing.T, s *Service, title models.Title) {
	t.Helper()
	if _, err := s.RequestRecheck("profile", "tv", title, nil); err != nil {
		t.Fatal(err)
	}
}
func TestExplicitRecheckBypassesFreshEvidenceAndDeduplicatesWithoutExtendingDeadline(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	title := testTitle("a")
	searchReleases(search, release("Synthetic.A"))
	titleView(t, s, "tv", "grid", 1, title)
	s.checkOne(t.Context())
	s.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatal("ordinary visibility repeated fresh search")
	}
	requestCheck(t, s, title)
	var before recheckJob
	for _, v := range s.rechecks {
		before = v
	}
	requestCheck(t, s, title)
	for _, v := range s.rechecks {
		if v != before {
			t.Fatal("duplicate extended request")
		}
	}
	if v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability; v.State != "unknown" || v.Reason != "recheck_queued" {
		t.Fatalf("stale result displayed: %+v", v)
	}
	search.result.Scored = nil
	search.result.RawCount = 0
	s.checkOne(t.Context())
	s.checkOne(t.Context())
	if search.calls != 2 {
		t.Fatalf("manual searches=%d", search.calls)
	}
	if v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability; v.State != "not_found" {
		t.Fatalf("new result lost: %+v", v)
	}
	if response, err := s.RequestRecheck("profile", "tv", title, nil); !errors.Is(err, ErrRecheckCooldown) || response.RetryAfterSeconds < 1 {
		t.Fatal("repeat bypassed cooldown")
	}
}
func TestExplicitRecheckSurvivesViewportDepartureAndWaitsForForeground(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	title := testTitle("a")
	titleView(t, s, "tv", "grid", 1, title)
	requestCheck(t, s, title)
	titleView(t, s, "tv", "grid", 2)
	search.busy = true
	s.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("foreground was bypassed")
	}
	search.busy = false
	s.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatal("explicit request disappeared with viewport")
	}
	if v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability; v.State != "not_found" {
		t.Fatal("explicit result missing")
	}
}
func TestExplicitRecheckExpiresWithoutQueryOrFalseAbsence(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	title := testTitle("a")
	requestCheck(t, s, title)
	now = now.Add(recheckLifetime + time.Second)
	s.checkOne(t.Context())
	v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability
	if search.calls != 0 || v.State != "unknown" || v.Reason != "recheck_timed_out" || v.Complete {
		t.Fatalf("expired request: %+v calls=%d", v, search.calls)
	}
}
func TestExplicitRecheckPrioritizesManualOverVisibleAutomaticWork(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	manual := testTitle("manual")
	requestCheck(t, s, manual)
	titleView(t, s, "tv", "grid", 1, testTitle("automatic"))
	s.checkOne(t.Context())
	if search.options.TitleID != "manual" {
		t.Fatal("automatic work ran before explicit action")
	}
}
func TestExplicitRecheckCannotResetPersistentProviderBudget(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	cfg, _ := s.manager.Load()
	cfg.SourceAvailability.MaxTitlesPerCycle = 2
	if err := s.manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		requestCheck(t, s, testTitle(id))
		s.checkOne(t.Context())
	}
	store, err := NewStore(s.Store.directory)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = store
	title := testTitle("c")
	requestCheck(t, s, title)
	s.checkOne(t.Context())
	v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability
	if search.calls != 2 || v.State != "unknown" || v.Reason != "recheck_budget_exhausted" || v.Complete {
		t.Fatalf("budget bypass: %+v calls=%d", v, search.calls)
	}
}
func TestExplicitRecheckFreshLocalProofSkipsProvider(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "opaque"}, valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	localStatus(t, s, title, 1)
	s.checkOne(t.Context())
	requestCheck(t, s, title)
	s.checkOne(t.Context())
	if checker.checks != 2 || search.calls != 0 {
		t.Fatalf("manual local checks=%d remote=%d", checker.checks, search.calls)
	}
	if v := localStatus(t, s, title, 2); v.SourceType != "local" || v.State != "found" {
		t.Fatalf("local result lost: %+v", v)
	}
}
func TestExplicitRecheckNoSourcesDoesNotMeanMissing(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	cfg, _ := s.manager.Load()
	cfg.SourceAvailability.IndexerNames = nil
	if err := s.manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	title := testTitle("a")
	requestCheck(t, s, title)
	s.checkOne(t.Context())
	v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability
	if search.calls != 0 || v.State != "unknown" || v.Reason != "recheck_no_sources" {
		t.Fatalf("no sources: %+v", v)
	}
}
func TestExplicitRecheckLanguageChangeCannotReuseOrBypassCooldown(t *testing.T) {
	s, search, _, profiles := reportFixture(t)
	title := testTitle("a")
	requestCheck(t, s, title)
	profiles["profile"].Playback.PreferredAudioLanguage = "eng"
	s.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("searched under obsolete language")
	}
	if _, err := s.RequestRecheck("profile", "phone", title, nil); !errors.Is(err, ErrRecheckCooldown) {
		t.Fatal("language/client change bypassed cooldown")
	}
}
func TestExplicitRecheckQueueIsBoundedPerProfileAndGlobally(t *testing.T) {
	s, _, _ := availabilityFixture(t)
	for n := 0; n < 4; n++ {
		if _, err := s.RequestRecheck("0", "tv", testTitle(fmt.Sprint(n)), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RequestRecheck("0", "tv", testTitle("extra"), nil); !errors.Is(err, ErrRecheckCapacity) {
		t.Fatal("profile queue exceeded before global capacity")
	}
	for p := 1; p < 8; p++ {
		for n := 0; n < 4; n++ {
			if _, err := s.RequestRecheck(fmt.Sprint(p), "tv", testTitle(fmt.Sprint(n)), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.RequestRecheck("0", "tv", testTitle("extra"), nil); !errors.Is(err, ErrRecheckCapacity) {
		t.Fatal("profile queue unbounded")
	}
	if _, err := s.RequestRecheck("9", "tv", testTitle("a"), nil); !errors.Is(err, ErrRecheckCapacity) {
		t.Fatal("global queue unbounded")
	}
}
func TestExplicitRecheckOldGenerationCannotFinishNewJob(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	title := testTitle("a")
	requestCheck(t, s, title)
	var id string
	var old recheckJob
	for k, v := range s.rechecks {
		id = k
		old = v
	}
	s.finishRecheck(id, old.Generation, "")
	now = now.Add(recheckCooldown + time.Second)
	requestCheck(t, s, title)
	s.finishRecheck(id, old.Generation, "recheck_failed")
	if s.rechecks[id].State != "queued" || s.rechecks[id].Generation == old.Generation {
		t.Fatal("old completion replaced newer check")
	}
}
func TestExplicitRecheckCanceledSearchCanResumeWithinExplicitLease(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	title := testTitle("a")
	requestCheck(t, s, title)
	ctx, cancel := context.WithCancel(t.Context())
	search.beforeReturn = cancel
	s.checkOne(ctx)
	for _, v := range s.rechecks {
		if v.State != "queued" {
			t.Fatal("foreground cancellation lost explicit intent")
		}
	}
	search.beforeReturn = nil
	s.checkOne(t.Context())
	if search.calls != 2 {
		t.Fatal("canceled explicit search was not resumed")
	}
}

func TestExplicitRecheckEpisodeCannotBecomeWholeSeriesEvidence(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	title, ep := episodeTitle(), episode(1)
	if _, err := s.RequestRecheck("profile", "tv", title, &ep); err != nil {
		t.Fatal(err)
	}
	s.checkOne(t.Context())
	if search.calls != 1 || search.options.TitleID != title.ID || search.options.Numbering.SeriesID != ep.Numbering.SeriesID || search.options.Query != title.Name+" S01E01" {
		t.Fatalf("episode search lost exact identity: %+v", search.options)
	}
	if v := s.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability; v.State != "unknown" {
		t.Fatal("episode recheck established whole-series status")
	}
}

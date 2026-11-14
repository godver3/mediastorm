package sourceavailability

import (
	"context"
	"encoding/json"
	"errors"
	"novastream/models"
	"strings"
	"testing"
	"time"
)

type fakeLocalHints struct {
	proof               LocalProof
	err                 error
	valid               bool
	checks, validations int
	requests            []LocalRequest
	run                 func(context.Context)
}

func (f *fakeLocalHints) Check(ctx context.Context, r LocalRequest) (LocalProof, error) {
	f.checks++
	f.requests = append(f.requests, r)
	if f.run != nil {
		f.run(ctx)
	}
	return f.proof, f.err
}
func (f *fakeLocalHints) Validate(_ context.Context, _ LocalRequest, _ LocalProof) bool {
	f.validations++
	return f.valid
}
func localStatus(t *testing.T, s *Service, title models.Title, version int64) models.SourceAvailability {
	t.Helper()
	v := titleView(t, s, "tv", "grid", version, title)
	v = s.ApplyLocalTitles(t.Context(), "profile", "tv", []models.Title{title}, v)
	return *v.Items[0].Availability
}
func TestLocalHintsOnlyVisibleWorkProbesAndLocalSuccessSavesProviderBudget(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "private-item", Fingerprint: "opaque"}, valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	s.AnnotateTitles("profile", "tv", []models.Title{title})
	s.checkOne(t.Context())
	if checker.checks != 0 {
		t.Fatal("hidden catalogue probed a file")
	}
	localStatus(t, s, title, 1)
	s.checkOne(t.Context())
	v := localStatus(t, s, title, 2)
	if v.State != "found" || v.SourceType != "local" || v.Reason != "local_file_checked" || search.calls != 0 || checker.checks != 1 {
		t.Fatalf("wrong local hint: %+v checks=%d searches=%d", v, checker.checks, search.calls)
	}
	data, _ := json.Marshal(v)
	if strings.Contains(string(data), "private-item") || strings.Contains(string(data), "opaque") {
		t.Fatal("internal proof leaked into response")
	}
	s.checkOne(t.Context())
	if checker.checks != 1 {
		t.Fatal("fresh local proof probed again")
	}
	checker.proof = LocalProof{}
	checker.valid = false
	localStatus(t, s, title, 3)
	checker.valid = true
	s.checkOne(t.Context())
	localStatus(t, s, title, 4)
	if search.calls != 1 {
		t.Fatal("local checks consumed or bypassed provider admission")
	}
}
func TestLocalHintsAbsenceNeedsBothRemoteAndCurrentLocalEvidence(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	if v := localStatus(t, s, title, 1); v.State != "unknown" {
		t.Fatal("unchecked local catalogue marked missing")
	}
	s.checkOne(t.Context())
	if v := localStatus(t, s, title, 2); v.State != "not_found" || !v.Complete {
		t.Fatalf("complete absence missing: %+v", v)
	}
	if search.calls != 1 {
		t.Fatal("remote evidence not recorded")
	}
	checker.valid = false
	if v := localStatus(t, s, title, 3); v.State != "unknown" {
		t.Fatal("new/unconfirmed local entry reused negative")
	}
	checker.proof = LocalProof{ItemID: "new-item"}
	checker.valid = true
	s.checkOne(t.Context())
	if v := localStatus(t, s, title, 4); v.SourceType != "local" || v.State != "found" {
		t.Fatal("new local entry did not replace absence")
	}
	if search.calls != 1 {
		t.Fatal("local invalidation repeated cached provider search")
	}
}
func TestLocalHintsProbeFailureNeverEstablishesTotalAbsence(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	checker := &fakeLocalHints{err: errors.New("synthetic disconnected mount"), valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	localStatus(t, s, title, 1)
	s.checkOne(t.Context())
	if v := localStatus(t, s, title, 2); v.State != "unknown" || v.Complete {
		t.Fatalf("local failure became absence: %+v", v)
	}
}
func TestLocalHintsExpiredOrRevokedProofCannotRemainPositive(t *testing.T) {
	for _, mode := range []string{"expired", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, _ := reportFixture(t)
			checker := &fakeLocalHints{proof: LocalProof{ItemID: "item"}, valid: true}
			s.SetLocalChecker(checker)
			title := testTitle("a")
			now := time.Now()
			s.now = func() time.Time { return now }
			localStatus(t, s, title, 1)
			s.checkOne(t.Context())
			if mode == "expired" {
				now = now.Add(3 * time.Minute)
			} else {
				checker.valid = false
			}
			if v := localStatus(t, s, title, 2); v.State == "found" || v.SourceType != "" {
				t.Fatal("stale local proof still positive")
			}
		})
	}
}
func TestLocalHintsLanguageAndProfileContextsRemainSeparate(t *testing.T) {
	s, _, _, profiles := reportFixture(t)
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "item"}, valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	localStatus(t, s, title, 1)
	s.checkOne(t.Context())
	profiles["profile"].Playback.PreferredAudioLanguage = "eng"
	if v := localStatus(t, s, title, 2); v.State == "found" {
		t.Fatal("changed language reused proof")
	}
	s.checkOne(t.Context())
	if checker.requests[0].AudioLanguage != "fra" || checker.requests[1].AudioLanguage != "eng" {
		t.Fatal("language intent lost")
	}
	v, err := s.ReplaceTitleView("another-profile", "tv", "other", 1, []models.Title{title})
	if err != nil {
		t.Fatal(err)
	}
	v = s.ApplyLocalTitles(t.Context(), "another-profile", "tv", []models.Title{title}, v)
	if v.Items[0].Availability.State == "found" {
		t.Fatal("profile reused another profile proof")
	}
}
func TestLocalHintsEpisodeEvidenceDoesNotQualifyWholeSeries(t *testing.T) {
	s, _, _, _ := reportFixture(t)
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "item"}, valid: true}
	s.SetLocalChecker(checker)
	view(t, s, "tv", "row", 1, episode(1))
	s.checkOne(t.Context())
	v := view(t, s, "tv", "row", 2, episode(1))
	v = s.ApplyLocalEpisodes(t.Context(), "profile", "tv", episodeTitle(), []models.SeriesEpisode{episode(1)}, v)
	if v.Items[0].Availability.SourceType != "local" {
		t.Fatal("episode proof missing")
	}
	other := view(t, s, "tv", "row", 3, episode(2))
	other = s.ApplyLocalEpisodes(t.Context(), "profile", "tv", episodeTitle(), []models.SeriesEpisode{episode(2)}, other)
	if other.Items[0].Availability.State == "found" {
		t.Fatal("another episode reused proof")
	}
	if s.AnnotateTitles("profile", "tv", []models.Title{episodeTitle()})[0].SourceAvailability.State == "found" {
		t.Fatal("episode proof qualified series")
	}
}
func TestLocalHintsCancellationCannotCommitLateProof(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "item"}, valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	titleView(t, s, "tv", "grid", 1, title)
	checker.run = func(ctx context.Context) { titleView(t, s, "tv", "grid", 2); <-ctx.Done() }
	s.checkOne(t.Context())
	if len(s.localRecords) != 0 || search.calls != 0 {
		t.Fatal("hidden local work committed proof or started a provider search")
	}
}
func TestLocalHintsRequireNoSelectedIndexerForExistingFiles(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	cfg, _ := s.manager.Load()
	cfg.SourceAvailability.IndexerNames = nil
	if err := s.manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "item"}, valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	localStatus(t, s, title, 1)
	s.checkOne(t.Context())
	if v := localStatus(t, s, title, 2); v.SourceType != "local" || search.calls != 0 {
		t.Fatal("local files depended on an indexer")
	}
}

func TestLocalHintsManualDailyFileDoesNotRequireUniqueDateReleaseProof(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{proof: LocalProof{ItemID: "manual-daily"}, valid: true}
	s.SetLocalChecker(checker)
	title := episodeTitle()
	title.IsDaily = true
	ep := episode(1)
	ep.SourceDateUnique = false
	v, err := s.ReplaceEpisodeView("profile", "tv", "daily", 1, title, []models.SeriesEpisode{ep})
	if err != nil {
		t.Fatal(err)
	}
	s.checkOne(t.Context())
	v = s.ApplyLocalEpisodes(t.Context(), "profile", "tv", title, []models.SeriesEpisode{ep}, v)
	if v.Items[0].Availability.SourceType != "local" || search.calls != 0 {
		t.Fatal("manual local episode depended on date-named remote release proof")
	}
}

func TestLocalHintsLegacyPreparedSourceCannotBypassLocalRevocation(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	resolve, _ := s.scope("profile", "tv")
	selection, _ := resolve(title.ID)
	now := time.Now()
	expires := now.Add(time.Hour)
	revision := s.reports.RevisionFor(s.reportContext("profile", "tv", title.ID, selection))
	if err := s.Store.putObservation("profile", title.ID, selection.Key, models.SourceAvailability{State: "found", Reason: "prepared_source", CheckedAt: &now, ExpiresAt: &expires}, &observation{Version: 1, Kind: "prepared", ReportRevision: revision}); err != nil {
		t.Fatal(err)
	}
	if v := localStatus(t, s, title, 1); v.State != "unknown" {
		t.Fatal("legacy local-or-remote preparation stayed positive")
	}
	s.checkOne(t.Context())
	if v := localStatus(t, s, title, 2); v.State != "not_found" || search.calls != 1 {
		t.Fatal("legacy preparation did not recheck actual source evidence")
	}
}
func TestLocalHintsConfirmedRemotePreparationKeepsItsOwnEvidence(t *testing.T) {
	s, search, _, _ := reportFixture(t)
	checker := &fakeLocalHints{valid: true}
	s.SetLocalChecker(checker)
	title := testTitle("a")
	s.ObservePlaybackStart("profile", "tv", title.ID, "movie")(true)
	localStatus(t, s, title, 1)
	s.checkOne(t.Context())
	if v := localStatus(t, s, title, 2); v.State != "found" || v.SourceType != "" || search.calls != 0 {
		t.Fatal("confirmed remote preparation lost its independent evidence")
	}
}

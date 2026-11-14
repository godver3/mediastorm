package sourceavailability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
)

type reportProfiles map[string]*models.UserSettings

func (p reportProfiles) Get(id string) (*models.UserSettings, error) { return p[id], nil }

func reportFixture(t *testing.T) (*Service, *fakeSearch, *badstreams.Service, reportProfiles) {
	t.Helper()
	svc, search, mgr := availabilityFixture(t)
	cfg, _ := mgr.Load()
	cfg.SourceAvailability.MaxTitlesPerCycle = 100
	if err := mgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	profiles := reportProfiles{}
	for _, id := range []string{"profile", "another-profile"} {
		p := &models.UserSettings{}
		p.Playback.PreferredAudioLanguage = "fra"
		profiles[id] = p
	}
	svc.scope = NewScopeFactory(mgr, profiles, nil, nil, func(string) bool { return true })
	registry := badstreams.New(filepath.Join(t.TempDir(), "reports.json"))
	svc.reports = registry
	return svc, search, registry, profiles
}
func release(name string) models.NZBResult {
	return models.NZBResult{Title: name, Indexer: "Fast", ServiceType: models.ServiceTypeUsenet, Attributes: map[string]string{"languages": "French, Spanish, English, Japanese, German"}}
}
func searchReleases(search *fakeSearch, items ...models.NZBResult) {
	search.result = indexer.ScoredSplitSearchResult{RawCount: len(items)}
	for _, item := range items {
		search.result.Scored = append(search.result.Scored, models.ScoredNZBResult{NZBResult: item, FilterStatus: "passed"})
	}
}
func cachedStatus(t *testing.T, svc *Service, profile, client string) models.SourceAvailability {
	t.Helper()
	return *svc.AnnotateTitles(profile, client, []models.Title{testTitle("tmdb:movie:1")})[0].SourceAvailability
}
func checkStatus(t *testing.T, svc *Service, profile, client, want string) models.SourceAvailability {
	t.Helper()
	v := cachedStatus(t, svc, profile, client)
	if v.State != want {
		t.Fatalf("state=%s reason=%s want=%s", v.State, v.Reason, want)
	}
	return v
}
func reportRelease(t *testing.T, svc *Service, result models.NZBResult, profile, client, reason string, shared bool) badstreams.Entry {
	t.Helper()
	factory, err := svc.scope(profile, client)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := factory("tmdb:movie:1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := svc.reportContext(profile, client, "tmdb:movie:1", selection)
	if reason == "audio_problem" {
		index := 1
		ctx.AudioTrackIndex = &index
	}
	entry, err := svc.reports.Report(badstreams.ReportRequest{Result: result, Context: ctx, Reason: reason, ReporterID: profile, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
func warm(t *testing.T, svc *Service, profile, client string) {
	t.Helper()
	cachedStatus(t, svc, profile, client)
	svc.checkOne(t.Context())
}

func TestAvailabilityReportsPreserveAlternativesAndUndoWithoutSearch(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	a, b := release("Synthetic.Movie.A.1080p"), release("Synthetic.Movie.B.1080p")
	searchReleases(search, a, b)
	warm(t, svc, "profile", "tv")
	checkStatus(t, svc, "profile", "tv", "found")
	before := svc.Revision("profile", "tv")
	reportRelease(t, svc, a, "profile", "tv", "missing_language", true)
	checkStatus(t, svc, "profile", "tv", "found")
	if svc.Revision("profile", "tv") == before {
		t.Fatal("cached shelf revision did not change")
	}
	report := reportRelease(t, svc, b, "profile", "tv", "missing_language", true)
	value := checkStatus(t, svc, "profile", "tv", "not_found")
	if !value.Complete || value.Reason != "reported_candidates" {
		t.Fatalf("bad exclusion status: %+v", value)
	}
	if !registry.Delete(report.ID) {
		t.Fatal("undo failed")
	}
	checkStatus(t, svc, "profile", "tv", "found")
	svc.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatalf("report or undo performed %d searches", search.calls)
	}
}

func TestAvailabilityLanguageReportsRespectFiveIntentsAndTitleOverride(t *testing.T) {
	for _, code := range []string{"fra", "spa", "eng", "jpn", "deu"} {
		t.Run(code, func(t *testing.T) {
			svc, search, _, profiles := reportFixture(t)
			profiles["profile"].Playback.PreferredAudioLanguage = code
			other := "eng"
			if code == "eng" {
				other = "fra"
			}
			profiles["another-profile"].Playback.PreferredAudioLanguage = other
			item := release("Synthetic.Multilingual.1080p")
			searchReleases(search, item)
			warm(t, svc, "profile", "tv")
			warm(t, svc, "another-profile", "tv")
			reportRelease(t, svc, item, "profile", "tv", "missing_language", true)
			checkStatus(t, svc, "profile", "tv", "not_found")
			checkStatus(t, svc, "another-profile", "tv", "found")
			// A title override must use the same precedence as playback, including an
			// intentional English request over French or Spanish profile defaults.
			content := &scopeContent{value: []models.ContentPreference{{ContentID: "tmdb:movie:1", AudioLanguage: other}}}
			svc.scope = NewScopeFactory(svc.manager, profiles, nil, content, func(string) bool { return true })
			checkStatus(t, svc, "profile", "tv", "unknown")
			warm(t, svc, "profile", "tv")
			checkStatus(t, svc, "profile", "tv", "found")
		})
	}
}

func TestAvailabilityPrivateDeviceAndProviderReportsStayContextual(t *testing.T) {
	for _, reason := range []string{"quality_problem", "device_problem", "source_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			svc, search, _, _ := reportFixture(t)
			item := release("Synthetic.Movie.1080p")
			searchReleases(search, item)
			warm(t, svc, "profile", "tv")
			warm(t, svc, "another-profile", "tv")
			reportRelease(t, svc, item, "profile", "tv", reason, false)
			checkStatus(t, svc, "profile", "tv", "not_found")
			checkStatus(t, svc, "another-profile", "tv", "found")
			if reason == "device_problem" {
				checkStatus(t, svc, "profile", "phone", "found")
			}
			if reason == "source_unavailable" {
				backup := item
				backup.Indexer = "Backup"
				ctx := svc.reportContext("profile", "tv", "tmdb:movie:1", Selection{AudioLanguage: "fra"})
				evidence := searchObservation(indexer.ScoredSplitSearchResult{Scored: []models.ScoredNZBResult{{NZBResult: backup}}}, true)
				value := svc.evaluate(models.SourceAvailability{CheckedAt: timePointer(time.Now())}, evidence, ctx)
				if value.State != "found" {
					t.Fatal("working provider route excluded")
				}
			}
		})
	}
}
func timePointer(v time.Time) *time.Time { return &v }

func TestAvailabilityTrackReportRequiresProbeButKeepsHealthyAlternative(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	a, b := release("Synthetic.Movie.A.1080p"), release("Synthetic.Movie.B.1080p")
	searchReleases(search, a)
	warm(t, svc, "profile", "tv")
	report := reportRelease(t, svc, a, "profile", "tv", "audio_problem", true)
	value := checkStatus(t, svc, "profile", "tv", "unknown")
	if value.Complete || value.Reason != "audio_track_unverified" {
		t.Fatal("track report claimed an unusable release")
	}
	resolve, _ := svc.scope("profile", "tv")
	selection, _ := resolve("tmdb:movie:1")
	old, evidence := svc.Store.getObservation("profile", "tmdb:movie:1", selection.Key)
	evidence.Candidates = append(evidence.Candidates, candidate{Title: b.Title, Indexer: b.Indexer, ServiceType: b.ServiceType, Languages: b.Attributes["languages"]})
	if err := svc.Store.putObservation("profile", "tmdb:movie:1", selection.Key, old, evidence); err != nil {
		t.Fatal(err)
	}
	checkStatus(t, svc, "profile", "tv", "found")
	if !registry.Delete(report.ID) {
		t.Fatal("undo failed")
	}
	checkStatus(t, svc, "profile", "tv", "found")
	if search.calls != 1 {
		t.Fatal("view performed an extra search")
	}
}

func TestAvailabilityTruncatedIncompleteAndUnconfirmedSearchesStayUnknown(t *testing.T) {
	for _, kind := range []string{"incomplete", "error", "disabled", "ranking limit", "evidence limit", "unknown language", "invalid candidate"} {
		t.Run(kind, func(t *testing.T) {
			svc, search, _, _ := reportFixture(t)
			item := release("Synthetic.Movie.1080p")
			searchReleases(search, item)
			switch kind {
			case "incomplete":
				search.result.Incomplete = true
			case "error":
				search.result.Err = fmt.Errorf("synthetic quota error")
			case "disabled":
				search.result.Disabled = true
			case "ranking limit":
				search.result.RawCount = 2
			case "evidence limit":
				for i := 0; i < maxCandidates; i++ {
					search.result.Scored = append(search.result.Scored, models.ScoredNZBResult{NZBResult: item})
				}
				search.result.RawCount = len(search.result.Scored)
			case "unknown language":
				search.result.Scored = append(search.result.Scored, models.ScoredNZBResult{NZBResult: models.NZBResult{Title: "Synthetic.Unconfirmed"}})
				search.result.RawCount = 2
			case "invalid candidate":
				search.result.Scored = append(search.result.Scored, models.ScoredNZBResult{NZBResult: models.NZBResult{Title: strings.Repeat("x", 513)}})
				search.result.RawCount = 2
			}
			warm(t, svc, "profile", "tv")
			reportRelease(t, svc, item, "profile", "tv", "wrong_title", true)
			value := checkStatus(t, svc, "profile", "tv", "unknown")
			if value.Complete {
				t.Fatal("subset became a complete negative")
			}
		})
	}
}

func TestAvailabilityPersistedEvidenceIsBoundedRedactedAndReevaluated(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	item.Link = "https://fixture.invalid/get?apikey=synthetic-secret"
	item.DownloadURL = item.Link
	item.GUID = "synthetic-private-guid"
	item.Attributes["accessToken"] = "synthetic-secret"
	searchReleases(search, item)
	warm(t, svc, "profile", "tv")
	resolve, _ := svc.scope("profile", "tv")
	selection, _ := resolve("tmdb:movie:1")
	path := filepath.Join(svc.Store.directory, key("profile", "tmdb:movie:1", selection.Key)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic-secret", "fixture.invalid", "synthetic-private-guid", "accessToken"} {
		if strings.Contains(string(data), private) {
			t.Fatal("private search payload persisted")
		}
	}
	store, err := NewStore(svc.Store.directory)
	if err != nil {
		t.Fatal(err)
	}
	svc.Store = store
	report := reportRelease(t, svc, item, "profile", "tv", "wrong_title", true)
	checkStatus(t, svc, "profile", "tv", "not_found")
	_, copy := store.getObservation("profile", "tmdb:movie:1", selection.Key)
	copy.Candidates[0].Title = "Changed copy"
	checkStatus(t, svc, "profile", "tv", "not_found")
	if !registry.Delete(report.ID) {
		t.Fatal("undo failed")
	}
	checkStatus(t, svc, "profile", "tv", "found")
}

func TestAvailabilityPreparedObservationRechecksReportsAndPreservesSearchEvidence(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	observe := svc.ObservePlaybackStart("profile", "tv", "tmdb:movie:1", "movie")
	observe(true)
	checkStatus(t, svc, "profile", "tv", "found")
	report := reportRelease(t, svc, item, "profile", "tv", "missing_language", true)
	checkStatus(t, svc, "profile", "tv", "unknown")
	svc.checkOne(t.Context())
	checkStatus(t, svc, "profile", "tv", "not_found")
	if !registry.Delete(report.ID) {
		t.Fatal("undo failed")
	}
	checkStatus(t, svc, "profile", "tv", "found")
	svc.ObservePlaybackStart("profile", "tv", "tmdb:movie:1", "movie")(true)
	// A new report must reevaluate the retained search evidence immediately.
	reportRelease(t, svc, item, "profile", "tv", "wrong_title", true)
	checkStatus(t, svc, "profile", "tv", "not_found")
	if search.calls != 1 {
		t.Fatal("prepared source discarded the search evidence")
	}
}

func TestAvailabilityInFlightPreparationAndSearchDoNotRestoreReportedSource(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	observe := svc.ObservePlaybackStart("profile", "tv", "tmdb:movie:1", "movie")
	search.beforeReturn = func() { reportRelease(t, svc, item, "profile", "tv", "wrong_title", true) }
	warm(t, svc, "profile", "tv")
	observe(true)
	checkStatus(t, svc, "profile", "tv", "not_found")
}

func TestAvailabilityEpisodeReportsAndPreparationDoNotQualifyWholeSeries(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Series.S01E02.1080p")
	searchReleases(search, item)
	title := testTitle("tmdb:series:1")
	title.MediaType = "series"
	svc.AnnotateTitles("profile", "tv", []models.Title{title})
	svc.checkOne(t.Context())
	before := svc.Revision("profile", "tv")
	_, err := registry.Report(badstreams.ReportRequest{Result: item, Context: badstreams.PlaybackContext{ProfileID: "profile", TitleID: title.ID, Episode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 2}}, Reason: "wrong_episode", ReporterID: "profile", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	got := svc.AnnotateTitles("profile", "tv", []models.Title{title})[0].SourceAvailability
	if got.State != "found" || svc.Revision("profile", "tv") != before {
		t.Fatal("single episode changed a whole series")
	}
	svc.ObservePlaybackStart("profile", "tv", "tmdb:series:2", "series")(true)
	other := testTitle("tmdb:series:2")
	other.MediaType = "series"
	if svc.AnnotateTitles("profile", "tv", []models.Title{other})[0].SourceAvailability.State != "unknown" {
		t.Fatal("prepared episode advertised all series sources")
	}
}

func TestAvailabilityLegacyRecordsRecheckWithinBudget(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	resolve, _ := svc.scope("profile", "tv")
	selection, _ := resolve("tmdb:movie:1")
	now := time.Now()
	expiry := now.Add(time.Hour)
	if err := svc.Store.Put("profile", "tmdb:movie:1", selection.Key, models.SourceAvailability{State: "found", CheckedAt: &now, ExpiresAt: &expiry}); err != nil {
		t.Fatal(err)
	}
	reportRelease(t, svc, item, "profile", "tv", "wrong_title", true)
	checkStatus(t, svc, "profile", "tv", "unknown")
	svc.checkOne(t.Context())
	checkStatus(t, svc, "profile", "tv", "not_found")
	// Reloading malformed/unknown observation versions cannot restore a badge.
	path := filepath.Join(svc.Store.directory, key("profile", "tmdb:movie:1", selection.Key)+".json")
	data, _ := os.ReadFile(path)
	var stored record
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Evidence.Version = 999
	data, _ = json.Marshal(stored)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(svc.Store.directory)
	if err != nil {
		t.Fatal(err)
	}
	svc.Store = reloaded
	checkStatus(t, svc, "profile", "tv", "unknown")
}

func TestAvailabilityLegacyBlacklistAppliesToCachedCandidates(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	warm(t, svc, "profile", "tv")
	mark, err := registry.Mark(badstreams.MarkRequest{ReleaseName: item.Title, ServiceType: "usenet"})
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(t, svc, "profile", "tv", "not_found")
	if !registry.Delete(mark.ID) {
		t.Fatal("undo failed")
	}
	checkStatus(t, svc, "profile", "tv", "found")
}

func TestAvailabilityTemporaryRouteExpiryRestoresCachedCandidate(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	warm(t, svc, "profile", "tv")
	reportRelease(t, svc, item, "profile", "tv", "source_unavailable", false)
	checkStatus(t, svc, "profile", "tv", "not_found")
	before := svc.Revision("profile", "tv")
	entries := registry.List()
	past := time.Now().Add(-time.Second)
	entries[0].ExpiresAt = &past
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "expired-reports.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	svc.reports = badstreams.New(path)
	checkStatus(t, svc, "profile", "tv", "found")
	if svc.Revision("profile", "tv") == before || search.calls != 1 {
		t.Fatal("expiry did not update the hint without another search")
	}
}

func TestAvailabilityReportSharingAndOtherTitlesUseLiveScope(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	warm(t, svc, "profile", "tv")
	warm(t, svc, "another-profile", "tv")
	other := testTitle("tmdb:movie:2")
	svc.AnnotateTitles("profile", "tv", []models.Title{other})
	svc.checkOne(t.Context())
	report := reportRelease(t, svc, item, "profile", "tv", "wrong_title", false)
	checkStatus(t, svc, "profile", "tv", "not_found")
	checkStatus(t, svc, "another-profile", "tv", "found")
	if _, err := registry.ShareReport(report.ID); err != nil {
		t.Fatal(err)
	}
	checkStatus(t, svc, "another-profile", "tv", "not_found")
	if svc.AnnotateTitles("profile", "tv", []models.Title{other})[0].SourceAvailability.State != "found" {
		t.Fatal("title report became a global release exclusion")
	}
}

func TestAvailabilityEvidenceLimitCannotHideAnUnblockedAlternative(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	items := []models.NZBResult{}
	for i := 0; i < maxCandidates+1; i++ {
		items = append(items, release(fmt.Sprintf("Synthetic.Movie.%02d.1080p", i)))
	}
	searchReleases(search, items...)
	warm(t, svc, "profile", "tv")
	for _, item := range items[:maxCandidates] {
		reportRelease(t, svc, item, "profile", "tv", "wrong_title", true)
	}
	value := checkStatus(t, svc, "profile", "tv", "unknown")
	if value.Complete {
		t.Fatal("bounded evidence concealed an eligible candidate outside the stored subset")
	}
	resolve, _ := svc.scope("profile", "tv")
	selection, _ := resolve("tmdb:movie:1")
	_, evidence := svc.Store.getObservation("profile", "tmdb:movie:1", selection.Key)
	if len(evidence.Candidates) != maxCandidates {
		t.Fatal("evidence exceeded its limit")
	}
}

func TestAvailabilityEvidenceRetainsCanonicalContentIdentity(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	item.Attributes["info_hash"] = "0123456789abcdef0123456789abcdef01234567"
	searchReleases(search, item)
	warm(t, svc, "profile", "tv")
	reportRelease(t, svc, item, "profile", "tv", "wrong_title", true)
	checkStatus(t, svc, "profile", "tv", "not_found")
	different := item
	different.Attributes = map[string]string{"languages": "French", "infoHash": "abcdef0123456789abcdef0123456789abcdef0123"}
	ctx := badstreams.PlaybackContext{ProfileID: "profile", TitleID: "tmdb:movie:1", AudioLanguage: "fra"}
	evidence := searchObservation(indexer.ScoredSplitSearchResult{Scored: []models.ScoredNZBResult{{NZBResult: different}}}, true)
	value := svc.evaluate(models.SourceAvailability{CheckedAt: timePointer(time.Now())}, evidence, ctx)
	if value.State != "found" {
		t.Fatal("different content identity was excluded by name")
	}
}

func TestAvailabilityConcurrentReportsAndCatalogueReads(t *testing.T) {
	svc, search, registry, _ := reportFixture(t)
	item := release("Synthetic.Movie.1080p")
	searchReleases(search, item)
	warm(t, svc, "profile", "tv")
	factory, _ := svc.scope("profile", "tv")
	selection, _ := factory("tmdb:movie:1")
	request := badstreams.ReportRequest{Result: item, Context: svc.reportContext("profile", "tv", "tmdb:movie:1", selection), Reason: "missing_language", ReporterID: "profile", Shared: true}
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() {
			for range 30 {
				value := cachedStatus(t, svc, "profile", "tv")
				if value.State != "found" && value.State != "not_found" && value.State != "unknown" {
					t.Error("invalid concurrent source state")
				}
				_ = svc.Revision("profile", "tv")
			}
		})
	}
	workers.Go(func() {
		for range 20 {
			entry, err := registry.Report(request)
			if err != nil {
				t.Error(err)
				return
			}
			if !registry.Delete(entry.ID) {
				t.Error("concurrent undo failed")
				return
			}
		}
	})
	workers.Wait()
	checkStatus(t, svc, "profile", "tv", "found")
}

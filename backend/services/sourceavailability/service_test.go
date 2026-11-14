package sourceavailability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/indexer"
)

type fakeSearch struct {
	beforeReturn func()
	result       indexer.ScoredSplitSearchResult
	calls        int
	options      indexer.SearchOptions
	busy         bool
	cancel       bool
}

func (f *fakeSearch) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	if f.busy {
		return nil, nil, errors.New("busy")
	}
	return ctx, func() {}, nil
}
func (f *fakeSearch) SearchWithScoringSplit(ctx context.Context, opts indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult) {
	f.calls++
	f.options = opts
	if f.beforeReturn != nil {
		f.beforeReturn()
	}
	u, d := make(chan indexer.ScoredSplitSearchResult, 1), make(chan indexer.ScoredSplitSearchResult, 1)
	if !f.cancel {
		u <- f.result
		d <- indexer.ScoredSplitSearchResult{Disabled: true}
	}
	close(u)
	close(d)
	return u, d
}
func availabilityFixture(t *testing.T) (*Service, *fakeSearch, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	manager := config.NewManager(filepath.Join(dir, "settings.json"))
	cfg := config.DefaultSettings()
	cfg.SourceAvailability = config.SourceAvailabilitySettings{Enabled: true, IndexerNames: []string{"Fast"}, RefreshHours: 6, MaxTitlesPerCycle: 2}
	cfg.Indexers = []config.IndexerConfig{{Name: "Fast", Enabled: true, Type: "newznab"}, {Name: "Tiny quota", Enabled: true, Type: "newznab"}}
	if err := manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(dir, "availability"))
	if err != nil {
		t.Fatal(err)
	}
	search := &fakeSearch{}
	factory := func(profile, client string) (func(string) (Selection, error), error) {
		return func(title string) (Selection, error) {
			return Selection{Key: "scope-" + client, AudioLanguage: "deu"}, nil
		}, nil
	}
	service := NewService(store, manager, search, factory)
	return service, search, manager
}
func testTitle(id string) models.Title {
	return models.Title{ID: id, Name: "Example", MediaType: "movie", Year: 2024, CardSubtitle: "Netflix #2"}
}

// visibleTitlesForTest models a delivered catalogue followed by explicit visibility.
func visibleTitlesForTest(t *testing.T, svc *Service, profile, client string, titles []models.Title) {
	t.Helper()
	svc.AnnotateTitles(profile, client, titles)
	version := int64(1)
	identity := "title\x00" + profile + "\x00" + client + "\x00test-visible"
	if old, ok := svc.views[identity]; ok {
		version = old.Version + 1
	}
	if _, err := svc.ReplaceTitleView(profile, client, "test-visible", version, titles); err != nil {
		t.Fatal(err)
	}
}

func TestAvailabilityIncompleteSearchNeverMarksMissing(t *testing.T) {
	for _, tc := range []struct {
		name, state, reason string
		result              indexer.ScoredSplitSearchResult
		missing             bool
	}{
		{name: "complete empty", state: "not_found", result: indexer.ScoredSplitSearchResult{}},
		{name: "partial timeout", state: "unknown", result: indexer.ScoredSplitSearchResult{Incomplete: true}},
		{name: "quota error", state: "unknown", result: indexer.ScoredSplitSearchResult{Err: errors.New("quota reached")}},
		{name: "disabled provider", state: "unknown", result: indexer.ScoredSplitSearchResult{Disabled: true}},
		{name: "channel closed without result", state: "unknown", missing: true},
		{name: "matching audio", state: "found", result: indexer.ScoredSplitSearchResult{Scored: []models.ScoredNZBResult{{NZBResult: models.NZBResult{Title: "Synthetic.Movie.1080p", Attributes: map[string]string{"languages": "German"}}}}}},
		{name: "unconfirmed audio", state: "unknown", result: indexer.ScoredSplitSearchResult{Scored: []models.ScoredNZBResult{{NZBResult: models.NZBResult{Title: "Synthetic.Movie.1080p", Attributes: map[string]string{"languages": "English"}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, search, _ := availabilityFixture(t)
			search.result = tc.result
			search.cancel = tc.missing
			titles := []models.Title{testTitle("tmdb:movie:1")}
			initial := svc.AnnotateTitles("stefan", "tv", titles)
			if search.calls != 0 || initial[0].SourceAvailability.State != "unknown" {
				t.Fatal("response performed provider work")
			}
			visibleTitlesForTest(t, svc, "stefan", "tv", titles)
			svc.checkOne(t.Context())
			value := svc.Store.Get("stefan", titles[0].ID, "scope-tv")
			if value.State != tc.state {
				t.Fatalf("state=%s reason=%s want=%s", value.State, value.Reason, tc.state)
			}
			if !search.options.AvailabilityCheck || len(search.options.AvailabilityIndexerNames) != 1 || search.options.AvailabilityIndexerNames[0] != "Fast" {
				t.Fatalf("incorrect provider selection: %+v", search.options)
			}
			again := svc.AnnotateTitles("stefan", "tv", initial)
			if strings.Count(again[0].CardSubtitle, "No matching source") > 1 || strings.Count(again[0].CardSubtitle, "Source candidate") > 1 {
				t.Fatal("duplicated badge")
			}
			if titles[0].SourceAvailability != nil || titles[0].CardSubtitle != "Netflix #2" {
				t.Fatal("mutated shared title")
			}
			if other := svc.AnnotateTitles("ella", "tv", titles)[0]; other.SourceAvailability.State != "unknown" {
				t.Fatal("profile status leaked")
			}
			if other := svc.AnnotateTitles("stefan", "phone", titles)[0]; other.SourceAvailability.State != "unknown" {
				t.Fatal("device status leaked")
			}
		})
	}
}

func TestAvailabilityBudgetSurvivesRestartAndDefersBusySearch(t *testing.T) {
	svc, search, manager := availabilityFixture(t)
	titles := []models.Title{testTitle("a"), testTitle("b"), testTitle("c")}
	visibleTitlesForTest(t, svc, "stefan", "tv", titles)
	search.busy = true
	svc.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("busy provider queried")
	}
	search.busy = false
	svc.checkOne(t.Context())
	svc.checkOne(t.Context())
	svc.checkOne(t.Context())
	if search.calls != 2 {
		t.Fatalf("calls=%d", search.calls)
	}
	store, err := NewStore(svc.Store.directory)
	if err != nil {
		t.Fatal(err)
	}
	restart := NewService(store, manager, search, svc.scope)
	visibleTitlesForTest(t, restart, "stefan", "tv", titles)
	restart.checkOne(t.Context())
	if search.calls != 2 {
		t.Fatal("restart reset budget")
	}
	restart.now = func() time.Time { return time.Now().Add(7 * time.Hour) }
	store.now = restart.now
	visibleTitlesForTest(t, restart, "stefan", "tv", titles)
	restart.checkOne(t.Context())
	if search.calls != 3 {
		t.Fatal("next interval did not resume")
	}
}

func TestAvailabilityDisabledOrEmptyProviderChoiceNeverSearches(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "no providers"}[enabled], func(t *testing.T) {
			svc, search, mgr := availabilityFixture(t)
			cfg, _ := mgr.Load()
			cfg.SourceAvailability.Enabled = enabled
			cfg.SourceAvailability.IndexerNames = nil
			if err := mgr.Save(cfg); err != nil {
				t.Fatal(err)
			}
			svc.AnnotateTitles("stefan", "tv", []models.Title{testTitle("a")})
			svc.checkOne(t.Context())
			if search.calls != 0 {
				t.Fatal("queried unselected provider")
			}
		})
	}
}

func TestAvailabilityPreparedSourceOnlyRecordsSuccessfulCapturedScope(t *testing.T) {
	svc, _, _ := availabilityFixture(t)
	observe := svc.ObservePlaybackStart("stefan", "tv", "a", "movie")
	observe(false)
	if svc.Store.Get("stefan", "a", "scope-tv").State != "unknown" {
		t.Fatal("failed playback recorded")
	}
	observe(true)
	if svc.Store.Get("stefan", "a", "scope-tv").State != "found" || svc.Store.Get("ella", "a", "scope-tv").State != "unknown" {
		t.Fatal("prepared source isolation failed")
	}
}

func TestAvailabilityStoreRejectsIncompleteNegativesAndRecoversExpiredOrMalformedRecords(t *testing.T) {
	svc, _, _ := availabilityFixture(t)
	now := time.Now()
	expiry := now.Add(time.Hour)
	if err := svc.Store.Put("p", "a", "s", models.SourceAvailability{State: "not_found", CheckedAt: &now, ExpiresAt: &expiry}); err == nil {
		t.Fatal("accepted incomplete negative")
	}
	if err := svc.Store.Put("p", "a", "s", models.SourceAvailability{State: "found", CheckedAt: &now, ExpiresAt: &expiry}); err != nil {
		t.Fatal(err)
	}
	before := svc.Store.Revision("p")
	svc.Store.now = func() time.Time { return now.Add(2 * time.Hour) }
	if svc.Store.Get("p", "a", "s").State != "unknown" || svc.Store.Revision("p") == before {
		t.Fatal("expired state stayed visible")
	}
	malformed := record{ProfileID: "p", TitleID: "broken", Scope: "s", Value: models.SourceAvailability{State: "found", ExpiresAt: &expiry}}
	data, _ := json.Marshal(malformed)
	path := filepath.Join(svc.Store.directory, key("p", "broken", "s")+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(svc.Store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Get("p", "broken", "s").State != "unknown" {
		t.Fatal("loaded corrupt record")
	}
}

func TestAvailabilityQueueBounded(t *testing.T) {
	svc, _, _ := availabilityFixture(t)
	for i := 0; i < MaxRecords+20; i++ {
		svc.enqueue(request{ProfileID: "p", Scope: "s", Title: testTitle(time.Unix(int64(i), 0).String()), LastSeen: time.Unix(int64(i), 0)})
	}
	if len(svc.pending) != MaxRecords {
		t.Fatalf("queue size=%d", len(svc.pending))
	}
}

func TestAvailabilityPreferenceChangesDuringWorkDiscardStaleObservations(t *testing.T) {
	svc, search, _ := availabilityFixture(t)
	identity := "scope-tv"
	svc.scope = func(string, string) (func(string) (Selection, error), error) {
		return func(string) (Selection, error) { return Selection{Key: identity}, nil }, nil
	}
	visibleTitlesForTest(t, svc, "stefan", "tv", []models.Title{testTitle("a")})
	observe := svc.ObservePlaybackStart("stefan", "tv", "a", "movie")
	search.beforeReturn = func() { identity = "new-language-scope" }
	svc.checkOne(t.Context())
	observe(true)
	if svc.Store.Get("stefan", "a", "scope-tv").State != "unknown" {
		t.Fatal("changed preferences reused an in-flight result")
	}
}

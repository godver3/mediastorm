package sourceavailability

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
)

func episodeTitle() models.Title {
	return models.Title{ID: "tmdb:series:123", Name: "Synthetic Series", OriginalName: "Synthetic Series", MediaType: "series", Year: 2020}
}
func episode(number int) models.SeriesEpisode {
	return models.SeriesEpisode{ID: fmt.Sprintf("episode-%d", number), SeasonNumber: 1, EpisodeNumber: number, AiredDate: "2020-01-01", SourceDateUnique: true, Numbering: &models.EpisodeNumbering{SeriesID: "tmdb:series:123", Ordering: "official"}}
}
func view(t *testing.T, svc *Service, client, id string, version int64, episodes ...models.SeriesEpisode) EpisodeViewResponse {
	t.Helper()
	v, err := svc.ReplaceEpisodeView("profile", client, id, version, episodeTitle(), episodes)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func episodeState(t *testing.T, svc *Service, id string, version int64, ep models.SeriesEpisode, want string) {
	t.Helper()
	v := view(t, svc, "tv", id, version, ep)
	if len(v.Items) != 1 || v.Items[0].Availability.State != want {
		t.Fatalf("want=%s got=%+v", want, v)
	}
}

func TestEpisodeAvailabilityUsesExactCoordinatesAndSeparateCache(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	searchReleases(search, release("Synthetic.Series.S01E01.1080p"))
	episodeState(t, svc, "row", 1, episode(1), "unknown")
	svc.checkOne(t.Context())
	episodeState(t, svc, "row", 2, episode(1), "found")
	episodeState(t, svc, "row", 3, episode(2), "unknown")
	svc.checkOne(t.Context())
	episodeState(t, svc, "row", 4, episode(2), "not_found")
	episodeState(t, svc, "row", 5, episode(1), "found")
	if search.calls != 2 || search.options.Query != "Synthetic Series S01E02" || search.options.Numbering == nil {
		t.Fatalf("unexpected episode search: %+v", search.options)
	}
	titleStatus := svc.AnnotateTitles("profile", "tv", []models.Title{episodeTitle()})[0].SourceAvailability
	if titleStatus.State != "unknown" {
		t.Fatal("episode result became a whole-series state")
	}
}
func TestEpisodeAvailabilityUnresolvedOrWrongIdentityStaysUnknown(t *testing.T) {
	for _, name := range []string{"Synthetic.Series.S01.Complete.1080p", "Synthetic.Series.S01E01E02.1080p", "Different.Series.S01E01.1080p", "Synthetic.Series.1080p"} {
		t.Run(name, func(t *testing.T) {
			svc, search, _, _ := reportFixture(t)
			searchReleases(search, release(name))
			view(t, svc, "tv", "row", 1, episode(1))
			svc.checkOne(t.Context())
			episodeState(t, svc, "row", 2, episode(1), "unknown")
		})
	}
}
func TestEpisodeAvailabilityReportsRespectEpisodeAndLanguage(t *testing.T) {
	svc, search, registry, profiles := reportFixture(t)
	item := release("Synthetic.Series.S01E01.1080p")
	searchReleases(search, item)
	ep := episode(1)
	view(t, svc, "tv", "row", 1, ep)
	svc.checkOne(t.Context())
	episodeState(t, svc, "row", 2, ep, "found")
	entry, err := registry.Report(badstreams.ReportRequest{Result: item, Context: badstreams.PlaybackContext{ProfileID: "profile", TitleID: episodeTitle().ID, AudioLanguage: "fra", Episode: &models.EpisodeReference{SeasonNumber: 1, EpisodeNumber: 1, Numbering: ep.Numbering}}, Reason: "missing_language", ReporterID: "profile", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	episodeState(t, svc, "row", 3, ep, "not_found")
	// Same evidence, different language intent, must not become a global rule.
	profiles["another-profile"].Playback.PreferredAudioLanguage = "eng"
	if _, err := svc.ReplaceEpisodeView("another-profile", "tv", "english-row", 1, episodeTitle(), []models.SeriesEpisode{ep}); err != nil {
		t.Fatal(err)
	}
	svc.checkOne(t.Context())
	other, err := svc.ReplaceEpisodeView("another-profile", "tv", "english-row", 2, episodeTitle(), []models.SeriesEpisode{ep})
	if err != nil || other.Items[0].Availability.State != "found" {
		t.Fatal("French episode report blocked English")
	}
	if !registry.Delete(entry.ID) {
		t.Fatal("undo failed")
	}
	episodeState(t, svc, "row", 4, ep, "found")
	if search.calls != 2 {
		t.Fatal("report or undo repeated a provider check")
	}
}
func TestEpisodeVisibilityExpiresAndStaleUpdatesCannotRestoreIt(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	now := time.Now()
	svc.now = func() time.Time { return now }
	view(t, svc, "tv", "row", 1, episode(1))
	view(t, svc, "tv", "row", 2)
	if _, err := svc.ReplaceEpisodeView("profile", "tv", "row", 1, episodeTitle(), []models.SeriesEpisode{episode(1)}); !errors.Is(err, ErrStaleEpisodeView) {
		t.Fatalf("late view accepted: %v", err)
	}
	svc.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("hidden episode searched")
	}
	view(t, svc, "tv", "row", 3, episode(1))
	now = now.Add(episodeViewTTL + time.Second)
	svc.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("expired visibility searched")
	}
}
func TestEpisodeVisibilitySharedConsumersKeepPendingWork(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	searchReleases(search, release("Synthetic.Series.S01E01.1080p"))
	view(t, svc, "tv", "row-a", 1, episode(1))
	view(t, svc, "tv", "row-b", 1, episode(1))
	view(t, svc, "tv", "row-a", 2)
	svc.checkOne(t.Context())
	episodeState(t, svc, "row-b", 2, episode(1), "found")
	if search.calls != 1 {
		t.Fatal("visible consumers duplicated provider work")
	}
}

type blockedEpisodeSearch struct {
	started chan struct{}
	done    chan struct{}
	calls   atomic.Int32
}

func (s *blockedEpisodeSearch) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s *blockedEpisodeSearch) SearchWithScoringSplit(ctx context.Context, _ indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult) {
	s.calls.Add(1)
	u, d := make(chan indexer.ScoredSplitSearchResult), make(chan indexer.ScoredSplitSearchResult)
	close(s.started)
	go func() { <-ctx.Done(); close(u); close(d); close(s.done) }()
	return u, d
}
func TestEpisodeLastVisibleConsumerCancelsRunningSearch(t *testing.T) {
	svc, _, _, _ := reportFixture(t)
	search := &blockedEpisodeSearch{started: make(chan struct{}), done: make(chan struct{})}
	svc.search = search
	view(t, svc, "tv", "a", 1, episode(1))
	view(t, svc, "tv", "b", 1, episode(1))
	finished := make(chan struct{})
	go func() { svc.checkOne(context.Background()); close(finished) }()
	select {
	case <-search.started:
	case <-time.After(time.Second):
		t.Fatal("search did not start")
	}
	view(t, svc, "tv", "a", 2)
	select {
	case <-search.done:
		t.Fatal("one hidden view canceled another viewer")
	default:
	}
	view(t, svc, "tv", "b", 2)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("invisible running search was not canceled")
	}
	resolve, _ := svc.scope("profile", "tv")
	selection, _ := resolve(episodeTitle().ID)
	ep := episode(1)
	selection = episodeSelection(selection, &ep, episodeTitle())
	if svc.Store.Get("profile", episodeCacheID(episodeTitle().ID, &ep), selection.Key).CheckedAt != nil {
		t.Fatal("canceled search committed a negative")
	}
}
func TestEpisodeAlternateOrderingAndUnairedMetadataDoNotSearch(t *testing.T) {
	for _, mode := range []string{"alternate", "future", "invalid date", "daily missing date"} {
		t.Run(mode, func(t *testing.T) {
			svc, search, _, _ := reportFixture(t)
			ep := episode(1)
			title := episodeTitle()
			switch mode {
			case "alternate":
				ep.Numbering.Ordering = "dvd"
			case "future":
				ep.AiredDate = "2099-01-01"
			case "invalid date":
				ep.AiredDate = "invalid"
			case "daily missing date":
				title.IsDaily = true
				ep.AiredDate = ""
			}
			response, err := svc.ReplaceEpisodeView("profile", "tv", "row", 1, title, []models.SeriesEpisode{ep})
			if err != nil {
				t.Fatal(err)
			}
			if response.Items[0].Availability.State != "unknown" {
				t.Fatal("unverified episode claimed availability")
			}
			svc.checkOne(t.Context())
			if search.calls != 0 {
				t.Fatal("unverifiable episode triggered provider work")
			}
		})
	}
}
func TestEpisodeDateNamedReleasesRequireExactAirDate(t *testing.T) {
	for _, tc := range []struct{ release, want string }{{"Synthetic.Series.2020.01.01.1080p", "found"}, {"Synthetic.Series.2020.01.02.1080p", "not_found"}, {"Different.Series.2020.01.01.1080p", "unknown"}} {
		t.Run(tc.release, func(t *testing.T) {
			svc, search, _, _ := reportFixture(t)
			searchReleases(search, release(tc.release))
			title := episodeTitle()
			title.IsDaily = true
			if _, err := svc.ReplaceEpisodeView("profile", "tv", "row", 1, title, []models.SeriesEpisode{episode(1)}); err != nil {
				t.Fatal(err)
			}
			svc.checkOne(t.Context())
			result, err := svc.ReplaceEpisodeView("profile", "tv", "row", 2, title, []models.SeriesEpisode{episode(1)})
			if err != nil || result.Items[0].Availability.State != tc.want {
				t.Fatalf("want %s got %+v err=%v", tc.want, result, err)
			}
		})
	}
}
func TestEpisodeMetadataAndPreferenceChangesInvalidateOnlyTheirScope(t *testing.T) {
	svc, search, _, profiles := reportFixture(t)
	searchReleases(search, release("Synthetic.Series.S01E01.1080p"))
	ep := episode(1)
	view(t, svc, "tv", "row", 1, ep)
	svc.checkOne(t.Context())
	episodeState(t, svc, "row", 2, ep, "found")
	ep.AiredDate = "2020-02-02"
	episodeState(t, svc, "row", 3, ep, "unknown")
	profiles["profile"].Playback.PreferredAudioLanguage = "eng"
	episodeState(t, svc, "row", 4, episode(1), "unknown")
}
func TestEpisodeVisibilitySharesExistingBudgetAndSurvivesCacheRestart(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	searchReleases(search, release("Synthetic.Series.S01E01.1080p"))
	view(t, svc, "tv", "row", 1, episode(1))
	svc.checkOne(t.Context())
	store, err := NewStore(svc.Store.directory)
	if err != nil {
		t.Fatal(err)
	}
	svc.Store = store
	episodeState(t, svc, "row", 2, episode(1), "found")
	svc.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatal("restart or view renewal repeated cached search")
	}
}
func TestEpisodeEmptyReleaseDateDoesNotProveAbsence(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	searchReleases(search)
	ep := episode(1)
	ep.AiredDate = ""
	view(t, svc, "tv", "row", 1, ep)
	svc.checkOne(t.Context())
	episodeState(t, svc, "row", 2, ep, "unknown")
}

func TestEpisodeVisibilityCannotResetTheProviderBudget(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	cfg, _ := svc.manager.Load()
	cfg.SourceAvailability.MaxTitlesPerCycle = 1
	if err := svc.manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	searchReleases(search)
	view(t, svc, "tv", "row", 1, episode(1))
	svc.checkOne(t.Context())
	view(t, svc, "tv", "row", 2)
	view(t, svc, "tv", "row", 3, episode(2))
	svc.checkOne(t.Context())
	if search.calls != 1 {
		t.Fatal("new visible view reset the provider budget")
	}
	episodeState(t, svc, "row", 4, episode(2), "unknown")
}
func TestEpisodeVisibilityHasPerProfileCapacityBounds(t *testing.T) {
	svc, _, _, _ := reportFixture(t)
	for i := 0; i < 16; i++ {
		view(t, svc, "tv", fmt.Sprintf("row-%d", i), 1)
	}
	if _, err := svc.ReplaceEpisodeView("profile", "tv", "overflow", 1, models.Title{}, nil); !errors.Is(err, ErrEpisodeViewLimit) {
		t.Fatalf("view limit not enforced: %v", err)
	}
	if _, err := svc.ReplaceEpisodeView("another-profile", "tv", "other", 1, models.Title{}, nil); err != nil {
		t.Fatal("one profile exhausted another profile's view capacity")
	}
}
func TestEpisodeInFlightPreferenceChangeDiscardsOldEvidence(t *testing.T) {
	svc, search, _, profiles := reportFixture(t)
	searchReleases(search, release("Synthetic.Series.S01E01.1080p"))
	view(t, svc, "tv", "row", 1, episode(1))
	search.beforeReturn = func() { profiles["profile"].Playback.PreferredAudioLanguage = "eng" }
	svc.checkOne(t.Context())
	episodeState(t, svc, "row", 2, episode(1), "unknown")
}

func TestEpisodeAmbiguousDailyDateCannotProveTheSelectedEpisode(t *testing.T) {
	svc, search, _, _ := reportFixture(t)
	title := episodeTitle()
	title.IsDaily = true
	ep := episode(1)
	ep.SourceDateUnique = false
	result, err := svc.ReplaceEpisodeView("profile", "tv", "daily", 1, title, []models.SeriesEpisode{ep})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Availability.State != "unknown" {
		t.Fatal("ambiguous date claimed an episode")
	}
	svc.checkOne(t.Context())
	if search.calls != 0 {
		t.Fatal("ambiguous daily date searched")
	}
}

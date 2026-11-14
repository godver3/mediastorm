package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
	"novastream/services/sourceavailability"
)

type episodeBrowserSearch struct{ calls atomic.Int32 }

func (s *episodeBrowserSearch) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s *episodeBrowserSearch) SearchWithScoringSplit(_ context.Context, opts indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult) {
	s.calls.Add(1)
	result := indexer.ScoredSplitSearchResult{}
	name := ""
	if strings.HasSuffix(opts.Query, "S01E01") {
		name = "Synthetic.Series.S01E01.1080p"
	}
	if strings.HasSuffix(opts.Query, "S01E02") {
		name = "Synthetic.Series.S01.Complete.1080p"
	}
	if name != "" {
		result.Scored = []models.ScoredNZBResult{{NZBResult: models.NZBResult{Title: name, Indexer: "Fixture", ServiceType: models.ServiceTypeUsenet, Attributes: map[string]string{"languages": "French, English"}}, FilterStatus: "passed"}}
		result.RawCount = 1
	}
	u, d := make(chan indexer.ScoredSplitSearchResult, 1), make(chan indexer.ScoredSplitSearchResult, 1)
	u <- result
	d <- indexer.ScoredSplitSearchResult{Disabled: true}
	close(u)
	close(d)
	return u, d
}
func episodeBrowserFixture(t *testing.T) http.Handler {
	t.Helper()
	h, metadata, _ := episodeEndpointFixture()
	details := metadata.seriesResp
	details.Title.Name = "Synthetic Series"
	for i := 2; i <= 3; i++ {
		ep := details.Seasons[0].Episodes[0]
		ep.ID = "ep-" + string(rune('0'+i))
		ep.EpisodeNumber = i
		details.Seasons[0].Episodes = append(details.Seasons[0].Episodes, ep)
	}
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	cfg := config.DefaultSettings()
	cfg.Playback.PreferredAudioLanguage = "fra"
	cfg.SourceAvailability = config.SourceAvailabilitySettings{Enabled: true, IndexerNames: []string{"Fixture"}, RefreshHours: 6, MaxTitlesPerCycle: 20}
	cfg.Indexers = []config.IndexerConfig{{Name: "Fixture", Type: "newznab", Enabled: true}}
	if err := manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	store, err := sourceavailability.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	search := &episodeBrowserSearch{}
	registry := badstreams.New(filepath.Join(t.TempDir(), "reports.json"))
	service := sourceavailability.NewService(store, manager, search, sourceavailability.NewScopeFactory(manager, nil, nil, nil, func(id string) bool { return id == "profile" }), registry)
	h.SourceAvailability = service
	h.CfgManager = manager
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	service.Start(ctx)
	router := mux.NewRouter()
	router.HandleFunc("/api/users/{userID}/source-availability/episodes/{viewID}", h.EpisodeSourceHints).Methods("PUT")
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"fixture": "episode-source-hints", "searches": search.calls.Load()})
	})
	router.HandleFunc("/fixture/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			for _, entry := range registry.List() {
				registry.Delete(entry.ID)
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"cleared": true})
			return
		}
		ep := details.Seasons[0].Episodes[0]
		_, err := registry.Report(badstreams.ReportRequest{Result: models.NZBResult{Title: "Synthetic.Series.S01E01.1080p", Indexer: "Fixture", ServiceType: models.ServiceTypeUsenet}, Context: badstreams.PlaybackContext{ProfileID: "profile", TitleID: "tmdb:series:456", AudioLanguage: "fra", Episode: &models.EpisodeReference{Numbering: ep.Numbering, SeasonNumber: 1, EpisodeNumber: 1}}, Reason: "missing_language", ReporterID: "profile", Shared: true})
		if err != nil {
			http.Error(w, "synthetic report failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"reported": true})
	}).Methods("POST", "DELETE")
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/episode_source_preview.html")
	}).Methods("GET")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")
		ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, true)
		router.ServeHTTP(w, r.WithContext(ctx))
	})
}
func TestEpisodeHintsBrowser(t *testing.T) {
	python, chromium := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chromium == "" {
		t.Skip("set browser interpreter and Chromium for the synthetic episode fixture")
	}
	server := httptest.NewServer(episodeBrowserFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	command := exec.CommandContext(t.Context(), python, "testdata/episode_source_browser.py", "--base-url", server.URL, "--chromium", chromium, "--artifacts", artifacts)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("episode source browser: %v\n%s", err, output)
	}
}
func TestEpisodeHintsPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_EPISODE_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set an address for the synthetic episode API preview")
	}
	server := &http.Server{Addr: addr, Handler: episodeBrowserFixture(t)}
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Synthetic episode API preview: http://%s/", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

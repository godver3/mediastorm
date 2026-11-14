package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/indexer"
	"novastream/services/sourceavailability"
	"os"
	"os/exec"
	"sync"
	"testing"
)

type visibleTitleBrowserSearch struct {
	mu    sync.Mutex
	calls map[string]int
}

func (s *visibleTitleBrowserSearch) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s *visibleTitleBrowserSearch) SearchWithScoringSplit(_ context.Context, opts indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult) {
	s.mu.Lock()
	s.calls[opts.TitleID]++
	s.mu.Unlock()
	result := indexer.ScoredSplitSearchResult{}
	if opts.TitleID == "movie-1" {
		result.Scored = []models.ScoredNZBResult{{NZBResult: models.NZBResult{Title: "Synthetic.Movie.1080p", Indexer: "Fixture", Attributes: map[string]string{"languages": "English"}}, FilterStatus: "passed"}}
		result.RawCount = 1
	}
	if opts.TitleID == "movie-3" {
		result.Incomplete = true
	}
	u, d := make(chan indexer.ScoredSplitSearchResult, 1), make(chan indexer.ScoredSplitSearchResult, 1)
	u <- result
	d <- indexer.ScoredSplitSearchResult{Disabled: true}
	close(u)
	close(d)
	return u, d
}
func visibleTitleBrowserFixture(t *testing.T) http.Handler {
	h, svc := titleEndpointFixture(t)
	cfg, _ := h.CfgManager.Load()
	cfg.SourceAvailability.IndexerNames = []string{"Fixture"}
	cfg.SourceAvailability.MaxTitlesPerCycle = 100
	cfg.Indexers = append(cfg.Indexers, config.IndexerConfig{Name: "Fixture", Type: "newznab", Enabled: true})
	cfg.Playback.PreferredAudioLanguage = "eng"
	if err := h.CfgManager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	search := &visibleTitleBrowserSearch{calls: map[string]int{}}
	svc = sourceavailability.NewService(svc.Store, h.CfgManager, search, sourceavailability.NewScopeFactory(h.CfgManager, nil, nil, nil, func(id string) bool { return id == "profile" }))
	h.SourceAvailability = svc
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	svc.Start(ctx)
	router := mux.NewRouter()
	router.HandleFunc("/api/users/{userID}/source-availability/titles/{viewID}", h.TitleSourceHints).Methods("PUT")
	router.HandleFunc("/fixture/catalogue", func(w http.ResponseWriter, r *http.Request) {
		titles := []models.Title{}
		for i := 1; i <= 10; i++ {
			titles = append(titles, models.Title{ID: fmt.Sprintf("movie-%d", i), Name: fmt.Sprintf("Synthetic Movie %d", i), MediaType: "movie", Year: 2024})
		}
		_ = json.NewEncoder(w).Encode(svc.AnnotateTitles("profile", "fixture-client", titles))
	})
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, _ *http.Request) {
		search.mu.Lock()
		defer search.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"fixture": "visible-title-source-hints", "searches": search.calls})
	})
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/visible_title_preview.html")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")
		router.ServeHTTP(w, r.WithContext(ctx))
	})
}
func TestVisibleTitleHintsBrowser(t *testing.T) {
	python, chrome := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chrome == "" {
		t.Skip("set browser interpreter and Chromium for the synthetic viewport fixture")
	}
	server := httptest.NewServer(visibleTitleBrowserFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	command := exec.CommandContext(t.Context(), python, "testdata/visible_title_browser.py", "--base-url", server.URL, "--chromium", chrome, "--artifacts", artifacts)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("visible title browser: %v\n%s", err, out)
	}
}
func TestVisibleTitleHintsPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_TITLE_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set an address for the synthetic viewport preview")
	}
	server := &http.Server{Addr: addr, Handler: visibleTitleBrowserFixture(t)}
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Synthetic title viewport preview: http://%s/", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

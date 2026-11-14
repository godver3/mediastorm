package handlers

import (
	"context"
	"encoding/json"
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

type recheckBrowserSearch struct {
	mu    sync.Mutex
	calls int
	empty bool
}

func (s *recheckBrowserSearch) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s *recheckBrowserSearch) SearchWithScoringSplit(_ context.Context, _ indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	result := indexer.ScoredSplitSearchResult{}
	if !s.empty {
		result.RawCount = 1
		result.Scored = []models.ScoredNZBResult{{NZBResult: models.NZBResult{Title: "Synthetic.Movie.1080p", Indexer: "Fixture", Attributes: map[string]string{"languages": "English"}}, FilterStatus: "passed"}}
	}
	u, d := make(chan indexer.ScoredSplitSearchResult, 1), make(chan indexer.ScoredSplitSearchResult, 1)
	u <- result
	d <- indexer.ScoredSplitSearchResult{Disabled: true}
	close(u)
	close(d)
	return u, d
}
func recheckBrowserFixture(t *testing.T) http.Handler {
	h, initial := titleEndpointFixture(t)
	cfg, _ := h.CfgManager.Load()
	cfg.SourceAvailability.IndexerNames = []string{"Fixture"}
	cfg.SourceAvailability.MaxTitlesPerCycle = 2
	cfg.Playback.PreferredAudioLanguage = "eng"
	cfg.Indexers = []config.IndexerConfig{{Name: "Fixture", Type: "newznab", Enabled: true}}
	if err := h.CfgManager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	search := &recheckBrowserSearch{}
	s := sourceavailability.NewService(initial.Store, h.CfgManager, search, sourceavailability.NewScopeFactory(h.CfgManager, nil, nil, nil, func(id string) bool { return id == "profile" }))
	h.SourceAvailability = s
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	s.Start(ctx)
	router := mux.NewRouter()
	router.HandleFunc("/api/users/{userID}/source-availability/recheck", h.RecheckSource).Methods("POST")
	router.HandleFunc("/api/users/{userID}/source-availability/titles/{viewID}", h.TitleSourceHints).Methods("PUT")
	router.HandleFunc("/fixture/catalogue", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(s.AnnotateTitles("profile", "fixture-client", []models.Title{{ID: "movie-1", Name: "Synthetic Movie", MediaType: "movie"}, {ID: "movie-2", Name: "Synthetic Budget Example", MediaType: "movie"}}))
	})
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, _ *http.Request) {
		search.mu.Lock()
		defer search.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"fixture": "manual-source-recheck", "searches": search.calls})
	})
	router.HandleFunc("/fixture/empty", func(w http.ResponseWriter, _ *http.Request) {
		search.mu.Lock()
		search.empty = true
		search.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]bool{"updated": true})
	}).Methods("POST")
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/source_recheck_preview.html")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")))
	})
}
func TestSourceRecheckBrowser(t *testing.T) {
	python, chrome := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chrome == "" {
		t.Skip("set browser interpreter and Chromium for synthetic recheck fixture")
	}
	server := httptest.NewServer(recheckBrowserFixture(t))
	defer server.Close()
	art := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if art == "" {
		art = t.TempDir()
	}
	command := exec.CommandContext(t.Context(), python, "testdata/source_recheck_browser.py", "--base-url", server.URL, "--chromium", chrome, "--artifacts", art)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("source recheck browser: %v\n%s", err, out)
	}
}
func TestSourceRecheckPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_RECHECK_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set synthetic recheck preview address")
	}
	server := &http.Server{Addr: addr, Handler: recheckBrowserFixture(t)}
	t.Cleanup(func() { server.Close() })
	t.Logf("Synthetic source recheck preview: http://%s/", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

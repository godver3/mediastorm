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
	"novastream/services/sourceavailability"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type localBrowserCatalog struct{ hintCatalog }

func (c localBrowserCatalog) FindPlaybackCandidates(ctx context.Context, q models.LocalMediaMatchQuery, ep *models.EpisodeReference, allow func(string) bool) ([]models.LocalMediaItem, error) {
	if q.TitleID != c.item.MatchedTitleID || q.MediaType != "movie" {
		return nil, nil
	}
	return c.fallbackCatalog.FindPlaybackCandidates(ctx, q, ep, allow)
}
func localHintBrowserFixture(t *testing.T) http.Handler {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe required")
	}
	file := filepath.Join(t.TempDir(), "synthetic.mkv")
	command := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000", "-t", "2", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=fra", file)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("synthetic local fixture: %v %s", err, out)
	}
	playback, catalog, access, _, _ := localCheckerFixture(t)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	catalog.item.SizeBytes = info.Size()
	playback.localFallback = localBrowserCatalog{hintCatalog{catalog}}
	playback.fullProber = &VideoHandler{ffprobePath: ffprobe, streamer: directCastTestProvider{directURL: file}, metadataCache: make(map[string]*cachedMetadataEntry)}
	cfg, _ := playback.configManager.Load()
	cfg.Playback.PreferredAudioLanguage = "fra"
	cfg.SourceAvailability = config.SourceAvailabilitySettings{Enabled: true, IndexerNames: []string{"Fixture"}, RefreshHours: 6, MaxTitlesPerCycle: 100}
	cfg.Indexers = []config.IndexerConfig{{Name: "Fixture", Type: "newznab", Enabled: true}}
	if err := playback.configManager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	store, err := sourceavailability.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	search := &visibleTitleBrowserSearch{calls: map[string]int{}}
	service := sourceavailability.NewService(store, playback.configManager, search, sourceavailability.NewScopeFactory(playback.configManager, nil, nil, nil, func(id string) bool { return id == "profile" }))
	service.SetLocalChecker(NewCatalogueLocalChecker(playback, hintAccounts{"account": {ID: "account"}}))
	h, _, _ := episodeEndpointFixture()
	h.CfgManager = playback.configManager
	h.SourceAvailability = service
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	service.Start(ctx)
	router := mux.NewRouter()
	router.HandleFunc("/api/users/{userID}/source-availability/titles/{viewID}", h.TitleSourceHints).Methods("PUT")
	router.HandleFunc("/fixture/catalogue", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(service.AnnotateTitles("profile", "fixture-client", []models.Title{{ID: catalog.item.MatchedTitleID, Name: "Synthetic local movie", MediaType: "movie"}, {ID: "missing-movie", Name: "Synthetic missing movie", MediaType: "movie"}}))
	})
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, _ *http.Request) {
		search.mu.Lock()
		defer search.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"fixture": "local-source-hints", "searches": search.calls, "libraryDenied": access.deny.Load()})
	})
	router.HandleFunc("/fixture/access", func(w http.ResponseWriter, r *http.Request) {
		access.deny.Store(r.URL.Query().Get("allowed") != "true")
		_ = json.NewEncoder(w).Encode(map[string]bool{"updated": true})
	}).Methods("POST")
	router.HandleFunc("/fixture/language", func(w http.ResponseWriter, r *http.Request) {
		language := r.URL.Query().Get("code")
		if language != "fra" && language != "spa" {
			http.Error(w, "invalid fixture language", 400)
			return
		}
		cfg, err := playback.configManager.Load()
		if err != nil {
			http.Error(w, "settings failed", 500)
			return
		}
		cfg.Playback.PreferredAudioLanguage = language
		if err := playback.configManager.Save(cfg); err != nil {
			http.Error(w, "settings failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"language": language})
	}).Methods("POST")
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/local_source_preview.html")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")))
	})
}
func TestLocalSourceHintsBrowser(t *testing.T) {
	python, chrome := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chrome == "" {
		t.Skip("set browser interpreter and Chromium for local source fixture")
	}
	server := httptest.NewServer(localHintBrowserFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	command := exec.CommandContext(t.Context(), python, "testdata/local_source_browser.py", "--base-url", server.URL, "--chromium", chrome, "--artifacts", artifacts)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("local source browser: %v\n%s", err, out)
	}
}
func TestLocalSourceHintsPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_LOCAL_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set local source API preview address")
	}
	server := &http.Server{Addr: addr, Handler: localHintBrowserFixture(t)}
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Synthetic local source preview: http://%s/", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

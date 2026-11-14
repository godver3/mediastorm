package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"novastream/config"
	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/badstreams"
	"novastream/services/indexer"
	"novastream/services/playback"
	"novastream/services/sourceavailability"
)

type availabilityReportSearch struct {
	results []models.ScoredNZBResult
	calls   atomic.Int32
}

func (s *availabilityReportSearch) BeginAvailabilitySearch(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (s *availabilityReportSearch) SearchWithScoringSplit(_ context.Context, _ indexer.SearchOptions) (<-chan indexer.ScoredSplitSearchResult, <-chan indexer.ScoredSplitSearchResult) {
	s.calls.Add(1)
	u, d := make(chan indexer.ScoredSplitSearchResult, 1), make(chan indexer.ScoredSplitSearchResult, 1)
	u <- indexer.ScoredSplitSearchResult{Scored: s.results, RawCount: len(s.results)}
	d <- indexer.ScoredSplitSearchResult{Disabled: true}
	close(u)
	close(d)
	return u, d
}

// This opt-in fixture exposes synthetic catalogue copies through the real
// metadata/manifest handlers, report handlers, registry and availability worker.
// It has no provider client, decoder, production route or private media file.
func availabilityReportHTTPFixture(t *testing.T) http.Handler {
	t.Helper()
	h, entry, registry := releaseReportFixture(t)
	cfg, err := h.configManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SourceAvailability = config.SourceAvailabilitySettings{Enabled: true, IndexerNames: []string{"Fixture"}, RefreshHours: 6, MaxTitlesPerCycle: 10}
	cfg.Indexers = []config.IndexerConfig{{Name: "Fixture", Type: "newznab", Enabled: true}}
	if err := h.configManager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"profile", "shared-french", "english"} {
		lang := "fra"
		if id == "english" {
			lang = "eng"
		}
		if err := h.userSettingsSvc.Update(id, models.UserSettings{Playback: models.PlaybackSettings{PreferredAudioLanguage: lang}}); err != nil {
			t.Fatal(err)
		}
	}
	sourceA := models.NZBResult{Title: "Synthetic.Movie.A.1080p", Indexer: "Fixture", ServiceType: models.ServiceTypeUsenet, Attributes: map[string]string{"languages": "French, English"}}
	sourceB := sourceA
	sourceB.Title = "Synthetic.Movie.B.1080p"
	ids := []string{entry.ID}
	extra, _ := h.store.CreateScoped(entry.TitleID, entry.TitleName, "profile", "movie", 2024, nil, "manual", "fixture-source-b")
	ids = append(ids, extra.ID)
	for i, id := range ids {
		item := sourceA
		if i == 1 {
			item = sourceB
		}
		h.store.Update(id, func(e *playback.PrequeueEntry) {
			e.Status = playback.PrequeueStatusReady
			e.StreamPath = "/fixture/source.mkv"
			e.SelectedResult = &item
			e.SelectedAudioTrack = 2
			e.AudioTracks = []playback.AudioTrackInfo{{Index: 1, Language: "eng", Codec: "aac"}, {Index: 2, Language: "fra", Codec: "aac"}}
		})
	}
	store, err := sourceavailability.NewStore(filepath.Join(t.TempDir(), "source-availability"))
	if err != nil {
		t.Fatal(err)
	}
	search := &availabilityReportSearch{results: []models.ScoredNZBResult{{NZBResult: sourceA, FilterStatus: "passed"}, {NZBResult: sourceB, FilterStatus: "passed"}}}
	svc := sourceavailability.NewService(store, h.configManager, search, sourceavailability.NewScopeFactory(h.configManager, h.userSettingsSvc, nil, nil, func(id string) bool { return id == "profile" || id == "shared-french" || id == "english" }), registry)
	title := models.Title{ID: entry.TitleID, Name: "Synthetic Movie", MediaType: "movie", Year: 2024, CardSubtitle: "Synthetic catalogue"}
	for _, id := range []string{"profile", "shared-french", "english"} {
		svc.AnnotateTitles(id, "fixture-device", []models.Title{title})
		if _, err := svc.ReplaceTitleView(id, "fixture-device", "fixture", 1, []models.Title{title}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	svc.Start(ctx)
	deadline := time.Now().Add(8 * time.Second)
	for {
		ready := true
		for _, id := range []string{"profile", "shared-french", "english"} {
			if svc.AnnotateTitles(id, "fixture-device", []models.Title{title})[0].SourceAvailability.State != "found" {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic availability worker did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	metadata := &MetadataHandler{SourceAvailability: svc}
	manifest := NewStartupHandler(h.userSettingsSvc, nil, nil, nil, h.configManager, nil)
	manifest.SetSourceAvailability(svc)
	bad := NewBadStreamsHandler(registry)
	bad.SetUsersService(h.users)
	router := mux.NewRouter()
	router.HandleFunc("/api/catalogue", func(w http.ResponseWriter, r *http.Request) {
		if err := metadata.encodeMetadata(w, r, []models.TrendingItem{{Title: title}}); err != nil {
			http.Error(w, "fixture encoding failed", 500)
		}
	}).Methods("GET")
	router.HandleFunc("/api/users/{userID}/home/manifest", manifest.GetHomeManifest).Methods("GET")
	router.HandleFunc("/api/playback/prequeue/{prequeueID}/report", h.ReleaseReportOptions).Methods("GET")
	router.HandleFunc("/api/playback/prequeue/{prequeueID}/report", h.ReportRelease).Methods("POST")
	router.HandleFunc("/api/playback/bad-streams/{id}", bad.Delete).Methods("DELETE")
	router.HandleFunc("/fixture/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"fixture": "scoped-source-availability", "prequeueIds": ids, "searches": search.calls.Load()})
	}).Methods("GET")
	router.HandleFunc("/fixture/reports", func(w http.ResponseWriter, r *http.Request) {
		reports := []map[string]string{}
		for _, entry := range registry.List() {
			reports = append(reports, map[string]string{"id": entry.ID, "reason": entry.Reason})
		}
		_ = json.NewEncoder(w).Encode(reports)
	}).Methods("GET")
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/scoped_availability_preview.html")
	}).Methods("GET")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestContext := context.WithValue(r.Context(), auth.ContextKeyAccountID, "account")
		requestContext = context.WithValue(requestContext, auth.ContextKeyIsMaster, true)
		router.ServeHTTP(w, r.WithContext(requestContext))
	})
}

func TestAvailabilityReportsRefreshMetadataAndHomeManifest(t *testing.T) {
	server := httptest.NewServer(availabilityReportHTTPFixture(t))
	defer server.Close()
	get := func(path string, target any) {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET status %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	state := func(profile string) string {
		t.Helper()
		var items []models.TrendingItem
		get("/api/catalogue?userId="+profile+"&clientId=fixture-device", &items)
		return items[0].Title.SourceAvailability.State
	}
	var before HomeManifestResponse
	get("/api/users/profile/home/manifest?clientId=fixture-device", &before)
	var info struct {
		IDs      []string `json:"prequeueIds"`
		Searches int      `json:"searches"`
	}
	get("/fixture/info", &info)
	reportIDs := []string{}
	for i, id := range info.IDs {
		var options struct {
			Version string `json:"sourceVersion"`
		}
		get("/api/playback/prequeue/"+id+"/report", &options)
		request := reportRequest(t, "POST", id, releaseReportRequest{Reason: "missing_language", AudioLanguage: "fra", SourceVersion: options.Version}, true)
		request.URL.Scheme = "http"
		request.URL.Host = server.Listener.Addr().String()
		request.RequestURI = ""
		request.Header.Set("X-Client-ID", "fixture-device")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Report badstreams.Entry `json:"report"`
		}
		err = json.NewDecoder(response.Body).Decode(&reply)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("report failed: %v status=%d", err, response.StatusCode)
		}
		reportIDs = append(reportIDs, reply.Report.ID)
		want := "found"
		if i == 1 {
			want = "not_found"
		}
		if state("profile") != want || state("shared-french") != want || state("english") != "found" {
			t.Fatal("report lost an alternative or leaked to English")
		}
	}
	var after HomeManifestResponse
	get("/api/users/profile/home/manifest?clientId=fixture-device", &after)
	if before.Revision == after.Revision || before.SourceAvailabilityRevision == after.SourceAvailabilityRevision {
		t.Fatal("home manifest did not invalidate")
	}
	request, _ := http.NewRequest("DELETE", server.URL+"/api/playback/bad-streams/"+reportIDs[1], nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || state("profile") != "found" {
		t.Fatal("undo did not restore metadata")
	}
	var final struct {
		Searches int `json:"searches"`
	}
	get("/fixture/info", &final)
	if final.Searches != info.Searches {
		t.Fatal("report or catalogue triggered another provider request")
	}
}

func TestAvailabilityReportsBrowser(t *testing.T) {
	python, chromium := os.Getenv("MEDIASTORM_BROWSER_PYTHON"), os.Getenv("MEDIASTORM_CHROMIUM")
	if python == "" || chromium == "" {
		t.Skip("set browser interpreter and Chromium to run the synthetic HTTP/browser fixture")
	}
	server := httptest.NewServer(availabilityReportHTTPFixture(t))
	defer server.Close()
	artifacts := os.Getenv("MEDIASTORM_BROWSER_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	cmd := exec.CommandContext(t.Context(), python, "testdata/scoped_availability_browser.py", "--base-url", server.URL, "--chromium", chromium, "--artifacts", artifacts)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("availability browser: %v\n%s", err, output)
	}
}
func TestAvailabilityReportsPreview(t *testing.T) {
	addr := os.Getenv("MEDIASTORM_SCOPED_AVAILABILITY_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("set an address to run the opt-in synthetic backend preview")
	}
	server := &http.Server{Addr: addr, Handler: availabilityReportHTTPFixture(t)}
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Synthetic source availability fixture: http://%s/", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

package sourceavailability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
	"novastream/services/indexer"
)

// This opt-in test accepts a search-only Newznab provider through the process
// environment. It prints counts/states/timings, never provider URLs or keys.
func TestAvailabilityLiveSelectedIndexer(t *testing.T) {
	endpoint, key := os.Getenv("MEDIASTORM_AVAILABILITY_TEST_URL"), os.Getenv("MEDIASTORM_AVAILABILITY_TEST_KEY")
	if endpoint == "" || key == "" {
		t.Skip("set the optional search-only provider environment")
	}
	upstream, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		t.Fatal("invalid provider endpoint")
	}
	if !strings.HasSuffix(upstream.Path, "/api") {
		upstream.Path += "/api"
	}
	var calls, grabs atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		kind := r.URL.Query().Get("t")
		if kind != "search" && kind != "movie" && kind != "tvsearch" && kind != "caps" {
			grabs.Add(1)
			http.Error(w, "only search is allowed", http.StatusForbidden)
			return
		}
		target := *upstream
		target.RawQuery = r.URL.RawQuery
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			http.Error(w, "request rejected", 502)
			return
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			http.Error(w, "upstream unavailable", 502)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxy.Close()
	dir := t.TempDir()
	manager := config.NewManager(filepath.Join(dir, "settings.json"))
	settings := config.DefaultSettings()
	settings.Playback.PreferredAudioLanguage = "eng"
	settings.SourceAvailability = config.SourceAvailabilitySettings{Enabled: true, IndexerNames: []string{"Selected test indexer"}, RefreshHours: 6, MaxTitlesPerCycle: 1}
	settings.Indexers = []config.IndexerConfig{{Name: "Selected test indexer", Type: "newznab", Enabled: true, URL: proxy.URL, APIKey: key}}
	if err := manager.Save(settings); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, manager, indexer.NewService(manager, nil, nil), NewScopeFactory(manager, nil, nil, nil, func(string) bool { return true }))
	titles := []models.Title{{ID: "tmdb:movie:603", TMDBID: 603, IMDBID: "tt0133093", Name: "The Matrix", OriginalName: "The Matrix", MediaType: "movie", Language: "en", Year: 1999}}
	started := time.Now()
	initial := service.AnnotateTitles("test-profile", "browser", titles)
	first := time.Since(started)
	if initial[0].SourceAvailability.State != "unknown" || calls.Load() != 0 {
		t.Fatal("initial overview performed an indexer request")
	}
	visibleTitlesForTest(t, service, "test-profile", "browser", titles)
	started = time.Now()
	service.checkOne(t.Context())
	searchTime := time.Since(started)
	before := calls.Load()
	started = time.Now()
	result := service.AnnotateTitles("test-profile", "browser", titles)
	cached := time.Since(started)
	if grabs.Load() != 0 || calls.Load() != before || before == 0 {
		t.Fatalf("unexpected requests: search=%d non-search=%d", before, grabs.Load())
	}
	t.Logf("initial_overview=%s background_check=%s cached_overview=%s requests=%d nzb_grabs=%d state=%s reason=%s", first, searchTime, cached, before, grabs.Load(), result[0].SourceAvailability.State, result[0].SourceAvailability.Reason)
}

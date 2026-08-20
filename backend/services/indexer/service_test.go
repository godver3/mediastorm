package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/internal/httpheaders"
	"novastream/internal/providerbreaker"
	"novastream/models"
	"novastream/services/debrid"
)

func TestSearchTorznabRateLimitSkipsProviderAndAllowsFallback(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	defer server.Close()

	svc := &Service{httpc: server.Client(), providerBreaker: providerbreaker.New()}
	ninja := config.IndexerConfig{Name: "Ninja", URL: server.URL}
	if _, err := svc.searchTorznab(context.Background(), ninja, SearchOptions{Query: "test"}); err == nil {
		t.Fatal("first Ninja search returned nil error for 429")
	}
	if _, err := svc.searchTorznab(context.Background(), ninja, SearchOptions{Query: "test again"}); err == nil {
		t.Fatal("second Ninja search was not blocked")
	}
	if requests != 1 {
		t.Fatalf("Ninja HTTP requests = %d, want 1", requests)
	}

	geek := config.IndexerConfig{Name: "NZBGeek", URL: server.URL}
	if _, err := svc.searchTorznab(context.Background(), geek, SearchOptions{Query: "fallback"}); err != nil {
		t.Fatalf("fallback search failed: %v", err)
	}
	if requests != 2 {
		t.Fatalf("total HTTP requests = %d, want 2", requests)
	}
}

func TestSearchTorznabReleasedEpisodeFallsBackToSeasonAfterEmptyExactSearch(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("q") == "Captain Star S01" {
			_, _ = w.Write([]byte(`<rss><channel><item><title>Captain Star S01-S02 Complete</title><guid>pack</guid></item></channel></rss>`))
			return
		}
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	defer server.Close()

	svc := &Service{httpc: server.Client(), providerBreaker: providerbreaker.New()}
	results, err := svc.searchTorznab(context.Background(), config.IndexerConfig{Name: "Test", URL: server.URL}, SearchOptions{
		Query:           "Captain Star S01E01",
		MediaType:       "series",
		EpisodeReleased: true,
	})
	if err != nil {
		t.Fatalf("searchTorznab: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Captain Star S01-S02 Complete" {
		t.Fatalf("results = %+v, want complete-series fallback", results)
	}
	want := []string{"Captain Star S01E01", "Captain Star S01"}
	if strings.Join(queries, "|") != strings.Join(want, "|") {
		t.Fatalf("queries = %v, want %v", queries, want)
	}
}

func TestSearchTorznabDoesNotFallbackForUnreleasedEpisode(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	defer server.Close()

	svc := &Service{httpc: server.Client(), providerBreaker: providerbreaker.New()}
	results, err := svc.searchTorznab(context.Background(), config.IndexerConfig{Name: "Test", URL: server.URL}, SearchOptions{
		Query:     "Future Show S01E01",
		MediaType: "series",
	})
	if err != nil {
		t.Fatalf("searchTorznab: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %d, want 0", len(results))
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

type stubDebridSearchService struct {
	results []models.NZBResult
	err     error
}

func (s stubDebridSearchService) Search(context.Context, debrid.SearchOptions) ([]models.NZBResult, error) {
	return append([]models.NZBResult(nil), s.results...), s.err
}

type countingDebridSearchService struct {
	calls   atomic.Int32
	results []models.NZBResult
}

func (s *countingDebridSearchService) Search(context.Context, debrid.SearchOptions) ([]models.NZBResult, error) {
	s.calls.Add(1)
	return cloneNZBResults(s.results), nil
}

type maxAwareDebridSearchService struct {
	results []models.NZBResult
}

func (s maxAwareDebridSearchService) Search(_ context.Context, opts debrid.SearchOptions) ([]models.NZBResult, error) {
	results := cloneNZBResults(s.results)
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}
	return results, nil
}

// gatedDebridSearchService is a debrid scraper the test controls: it signals
// when it starts searching and then blocks until released, simulating the slow
// debrid tail a usenet-prioritized prequeue must not wait on (OPP-2).
type gatedDebridSearchService struct {
	started  chan struct{}
	finished chan struct{}
	release  chan struct{}
	results  []models.NZBResult
}

func (s *gatedDebridSearchService) Search(ctx context.Context, _ debrid.SearchOptions) ([]models.NZBResult, error) {
	close(s.started)
	defer close(s.finished)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return append([]models.NZBResult(nil), s.results...), nil
}

type mutableClientSettingsProvider struct {
	settings atomic.Value
}

func newMutableClientSettingsProvider(settings *models.ClientFilterSettings) *mutableClientSettingsProvider {
	p := &mutableClientSettingsProvider{}
	p.settings.Store(settings)
	return p
}

func (p *mutableClientSettingsProvider) Get(string, string) (*models.ClientFilterSettings, error) {
	settings, _ := p.settings.Load().(*models.ClientFilterSettings)
	return settings, nil
}

func (p *mutableClientSettingsProvider) Set(settings *models.ClientFilterSettings) {
	p.settings.Store(settings)
}

type mapClientSettingsProvider struct {
	settings map[string]*models.ClientFilterSettings
}

func (p mapClientSettingsProvider) Get(clientID, userID string) (*models.ClientFilterSettings, error) {
	return p.settings[clientID], nil
}

type staticUserSettingsProvider struct {
	settings *models.UserSettings
}

func (p staticUserSettingsProvider) Get(string) (*models.UserSettings, error) {
	return p.settings, nil
}

func TestNewestReleaseFirstCascadeAndSort(t *testing.T) {
	enabled := true
	disabled := false
	svc := &Service{
		userSettings: staticUserSettingsProvider{
			settings: &models.UserSettings{
				Ranking: &models.UserRankingSettings{NewestReleaseFirst: &enabled},
			},
		},
		clientSettings: mapClientSettingsProvider{
			settings: map[string]*models.ClientFilterSettings{
				"client": {NewestReleaseFirst: &disabled},
			},
		},
	}
	settings := config.DefaultSettings()
	if !svc.getEffectiveRankingBundle("profile", "", settings).NewestReleaseFirst {
		t.Fatal("profile override should enable newest-release sorting")
	}
	if svc.getEffectiveRankingBundle("profile", "client", settings).NewestReleaseFirst {
		t.Fatal("client override should disable profile newest-release sorting")
	}

	oldest := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	newest := oldest.Add(24 * time.Hour)
	results := []models.NZBResult{
		{Title: "unknown"},
		{Title: "old", PublishDate: oldest},
		{Title: "new", PublishDate: newest},
	}
	sortResultsNewestReleaseFirst(results)
	got := []string{results[0].Title, results[1].Title, results[2].Title}
	want := []string{"new", "old", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("newest sort = %v, want %v", got, want)
		}
	}
}

func TestSearchNewestReleaseFirstSortsBeforeFinalResultLimit(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.Ranking.NewestReleaseFirst = true
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	oldest := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	newest := oldest.Add(24 * time.Hour)
	svc := NewService(mgr, nil, maxAwareDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.2024.old", PublishDate: oldest, ServiceType: models.ServiceTypeDebrid},
			{Title: "Movie.2024.new", PublishDate: newest, ServiceType: models.ServiceTypeDebrid},
		},
	})
	results, err := svc.Search(t.Context(), SearchOptions{
		Query:      "Movie 2024",
		MediaType:  "movie",
		Year:       2024,
		MaxResults: 1,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Movie.2024.new" {
		t.Fatalf("results = %#v, want newest release only", results)
	}
}

func TestSearchTorznab_IndexerCategories(t *testing.T) {
	// Track the categories received by the mock server
	var receivedCategories string
	var receivedUserAgent string
	var receivedAccept string

	// Create a mock newznab server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCategories = r.URL.Query().Get("cat")
		receivedUserAgent = r.Header.Get("User-Agent")
		receivedAccept = r.Header.Get("Accept")
		// Return empty RSS feed
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>Test Result</title>
      <link>http://example.com/nzb/123</link>
      <guid>123</guid>
    </item>
  </channel>
</rss>`))
	}))
	defer mockServer.Close()

	svc := &Service{
		httpc: &http.Client{},
	}

	// Test 1: Indexer with configured categories
	t.Run("uses indexer categories when configured", func(t *testing.T) {
		receivedCategories = ""
		idx := config.IndexerConfig{
			Name:       "TestIndexer",
			URL:        mockServer.URL,
			APIKey:     "testkey",
			Type:       "newznab",
			Categories: "2000,2040,2045",
			Enabled:    true,
		}
		opts := SearchOptions{Query: "test movie"}

		_, err := svc.searchTorznab(context.Background(), idx, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if receivedCategories != "2000,2040,2045" {
			t.Errorf("expected categories '2000,2040,2045', got '%s'", receivedCategories)
		}
		if receivedUserAgent != httpheaders.UserAgent {
			t.Fatalf("expected User-Agent %q, got %q", httpheaders.UserAgent, receivedUserAgent)
		}
		if receivedAccept == "" {
			t.Fatal("expected Accept header to be set")
		}
	})

	// Test 2: Indexer without configured categories, but opts has categories
	t.Run("falls back to opts categories when indexer has none", func(t *testing.T) {
		receivedCategories = ""
		idx := config.IndexerConfig{
			Name:       "TestIndexer",
			URL:        mockServer.URL,
			APIKey:     "testkey",
			Type:       "newznab",
			Categories: "",
			Enabled:    true,
		}
		opts := SearchOptions{Query: "test movie", Categories: []string{"5000", "5030"}}

		_, err := svc.searchTorznab(context.Background(), idx, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if receivedCategories != "5000,5030" {
			t.Errorf("expected categories '5000,5030', got '%s'", receivedCategories)
		}
	})

	// Test 3: Indexer categories take precedence over opts categories
	t.Run("indexer categories override opts categories", func(t *testing.T) {
		receivedCategories = ""
		idx := config.IndexerConfig{
			Name:       "TestIndexer",
			URL:        mockServer.URL,
			APIKey:     "testkey",
			Type:       "newznab",
			Categories: "2000",
			Enabled:    true,
		}
		opts := SearchOptions{Query: "test movie", Categories: []string{"5000", "5030"}}

		_, err := svc.searchTorznab(context.Background(), idx, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if receivedCategories != "2000" {
			t.Errorf("expected indexer categories '2000' to override opts, got '%s'", receivedCategories)
		}
	})

	// Test 4: No categories configured anywhere
	t.Run("no categories when none configured", func(t *testing.T) {
		receivedCategories = ""
		idx := config.IndexerConfig{
			Name:       "TestIndexer",
			URL:        mockServer.URL,
			APIKey:     "testkey",
			Type:       "newznab",
			Categories: "",
			Enabled:    true,
		}
		opts := SearchOptions{Query: "test movie"}

		_, err := svc.searchTorznab(context.Background(), idx, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if receivedCategories != "" {
			t.Errorf("expected no categories, got '%s'", receivedCategories)
		}
	})

	// Test 5: Whitespace-only categories should be treated as empty
	t.Run("whitespace categories treated as empty", func(t *testing.T) {
		receivedCategories = ""
		idx := config.IndexerConfig{
			Name:       "TestIndexer",
			URL:        mockServer.URL,
			APIKey:     "testkey",
			Type:       "newznab",
			Categories: "   ",
			Enabled:    true,
		}
		opts := SearchOptions{Query: "test movie", Categories: []string{"5000"}}

		_, err := svc.searchTorznab(context.Background(), idx, opts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should fall back to opts since indexer categories is whitespace-only
		if receivedCategories != "5000" {
			t.Errorf("expected fallback to opts categories '5000', got '%s'", receivedCategories)
		}
	})
}

func TestSearchTorznab_MultipleIndexers(t *testing.T) {
	// Track categories received per request
	var requestLog []string
	var requestLogMu sync.Mutex

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cat := r.URL.Query().Get("cat")
		requestLogMu.Lock()
		requestLog = append(requestLog, cat)
		requestLogMu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel></channel></rss>`))
	}))
	defer mockServer.Close()

	// Create config manager with multiple indexers
	tmpDir := t.TempDir()
	cfgPath := tmpDir + "/settings.json"
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Indexers = []config.IndexerConfig{
		{Name: "MovieIndexer", URL: mockServer.URL, APIKey: "key1", Type: "newznab", Categories: "2000,2040", Enabled: true},
		{Name: "TVIndexer", URL: mockServer.URL, APIKey: "key2", Type: "newznab", Categories: "5000,5030", Enabled: true},
		{Name: "AllIndexer", URL: mockServer.URL, APIKey: "key3", Type: "newznab", Categories: "", Enabled: true},
	}
	settings.Streaming.ServiceMode = config.StreamingServiceModeUsenet
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	svc := NewService(mgr, nil, nil)

	// Run a search
	requestLog = nil
	_, err := svc.fetchUsenetResults(context.Background(), settings, SearchOptions{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify each indexer was called with its own categories
	if len(requestLog) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(requestLog))
	}

	expectedCats := []string{"2000,2040", "5000,5030", ""}
	sort.Strings(requestLog)
	sort.Strings(expectedCats)
	for i, expected := range expectedCats {
		if requestLog[i] != expected {
			t.Errorf("categories[%d]: expected %q, got %q", i, expected, requestLog[i])
		}
	}
}

func TestFetchUsenetResults_PartialIndexerFailureWithEmptySuccess(t *testing.T) {
	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel></channel></rss>`))
	}))
	defer emptyServer.Close()

	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slowServer.Close()

	settings := config.DefaultSettings()
	settings.Indexers = []config.IndexerConfig{
		{Name: "Empty", URL: emptyServer.URL, APIKey: "key1", Type: "newznab", Enabled: true},
		{Name: "Slow", URL: slowServer.URL, APIKey: "key2", Type: "newznab", Enabled: true},
	}

	svc := &Service{httpc: &http.Client{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	results, err := svc.fetchUsenetResults(ctx, settings, SearchOptions{Query: "One Piece S23E06"})
	if err != nil {
		t.Fatalf("expected empty successful result set despite one failed indexer, got error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchBypassesRankingForAIOStreamsOnlyDebridMode(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = true
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, stubDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.720p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid},
			{Title: "Movie.2160p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid},
		},
	})

	results, err := svc.Search(t.Context(), SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if got := results[0].Title; got != "Movie.720p.WEB-DL" {
		t.Fatalf("expected AIOStreams order to bypass ranking, got first title %q", got)
	}
}

func TestSearchCachesResultsForRepeatedQuery(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = true
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{
				Title:       "Movie.2160p.WEB-DL",
				Indexer:     "AIOStreams",
				ServiceType: models.ServiceTypeDebrid,
				Attributes:  map[string]string{"resolution": "2160p"},
			},
		},
	}
	svc := NewService(mgr, nil, debridSvc)

	opts := SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024}
	first, err := svc.Search(t.Context(), opts)
	if err != nil {
		t.Fatalf("first search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected underlying search call count 1, got %d", got)
	}
	first[0].Title = "mutated"
	first[0].Attributes["resolution"] = "mutated"

	second, err := svc.Search(t.Context(), opts)
	if err != nil {
		t.Fatalf("second search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected cached search to avoid another call, got %d calls", got)
	}
	if second[0].Title != "Movie.2160p.WEB-DL" {
		t.Fatalf("expected cached result clone to preserve title, got %q", second[0].Title)
	}
	if got := second[0].Attributes["resolution"]; got != "2160p" {
		t.Fatalf("expected cached result clone to preserve attributes, got %q", got)
	}

	svc.ClearSearchCache()
	if _, err := svc.Search(t.Context(), opts); err != nil {
		t.Fatalf("search after cache clear returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 2 {
		t.Fatalf("expected cache clear to force another underlying call, got %d calls", got)
	}
}

func TestSearchCacheKeyIncludesClientRankingCriteria(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	settings.Ranking.Criteria = []config.RankingCriterion{
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 1},
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 2},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.720p.large", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
			{Title: "Movie.2160p.small", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
		},
	}
	clientSettings := newMutableClientSettingsProvider(&models.ClientFilterSettings{
		RankingCriteria: &[]models.ClientRankingCriterion{
			{ID: config.RankingSize, Order: models.IntPtr(1)},
			{ID: config.RankingResolution, Order: models.IntPtr(2)},
		},
	})
	svc := NewService(mgr, nil, debridSvc)
	svc.SetClientSettingsProvider(clientSettings)

	opts := SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, ClientID: "living-room"}
	first, err := svc.Search(t.Context(), opts)
	if err != nil {
		t.Fatalf("first search returned error: %v", err)
	}
	if got := first[0].Title; got != "Movie.720p.large" {
		t.Fatalf("expected size-first ranking to prefer large file, got %q", got)
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected underlying search call count 1, got %d", got)
	}

	clientSettings.Set(&models.ClientFilterSettings{
		RankingCriteria: &[]models.ClientRankingCriterion{
			{ID: config.RankingSize, Order: models.IntPtr(2)},
			{ID: config.RankingResolution, Order: models.IntPtr(1)},
		},
	})
	second, err := svc.Search(t.Context(), opts)
	if err != nil {
		t.Fatalf("second search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 2 {
		t.Fatalf("expected client ranking change to miss cache and call search again, got %d calls", got)
	}
	if got := second[0].Title; got != "Movie.2160p.small" {
		t.Fatalf("expected resolution-first ranking to prefer 2160p result, got %q", got)
	}
}

func TestSearchCacheSharedAcrossClientsWithSameEffectiveSettings(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	settings.Ranking.Criteria = []config.RankingCriterion{
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 1},
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 2},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.720p.large", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
			{Title: "Movie.2160p.small", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
		},
	}
	sameRanking := &models.ClientFilterSettings{
		RankingCriteria: &[]models.ClientRankingCriterion{
			{ID: config.RankingSize, Order: models.IntPtr(1)},
			{ID: config.RankingResolution, Order: models.IntPtr(2)},
		},
	}
	svc := NewService(mgr, nil, debridSvc)
	svc.SetClientSettingsProvider(mapClientSettingsProvider{
		settings: map[string]*models.ClientFilterSettings{
			"living-room": sameRanking,
			"phone":       sameRanking,
		},
	})

	first, err := svc.Search(t.Context(), SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, UserID: "default", ClientID: "living-room"})
	if err != nil {
		t.Fatalf("first search returned error: %v", err)
	}
	if got := first[0].Title; got != "Movie.720p.large" {
		t.Fatalf("expected size-first ranking to prefer large file, got %q", got)
	}

	second, err := svc.Search(t.Context(), SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, UserID: "default", ClientID: "phone"})
	if err != nil {
		t.Fatalf("second search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected clients with identical effective settings to share cache, got %d calls", got)
	}
	if got := second[0].Title; got != "Movie.720p.large" {
		t.Fatalf("expected cached size-first result order, got %q", got)
	}
}

func TestSearchCacheIgnoresUnrelatedGlobalSettings(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.2160p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"resolution": "2160p"}},
		},
	}
	svc := NewService(mgr, nil, debridSvc)

	opts := SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, UserID: "default"}
	if _, err := svc.Search(t.Context(), opts); err != nil {
		t.Fatalf("first search returned error: %v", err)
	}

	settings.UI.OnboardingCompleted = !settings.UI.OnboardingCompleted
	settings.Display.NavigationTabVisibility = []string{"home", "search"}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save unrelated settings change: %v", err)
	}

	if _, err := svc.Search(t.Context(), opts); err != nil {
		t.Fatalf("second search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected unrelated settings changes to keep cache usable, got %d calls", got)
	}
}

func TestSearchCacheSharedWithNonSearchClientSettingsWhenAdaptiveDisabled(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Filtering.AdaptivePlaybackEnabled = false
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	measuredMbps := 426.9
	measuredAt := time.Now().Unix()
	displayHDR := true
	displayDV := true
	includeSystemTabs := true
	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.2160p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, Attributes: map[string]string{"resolution": "2160p"}},
		},
	}
	svc := NewService(mgr, nil, debridSvc)
	svc.SetClientSettingsProvider(mapClientSettingsProvider{
		settings: map[string]*models.ClientFilterSettings{
			"phone": {
				AdaptivePlayback: &models.AdaptivePlaybackSettings{
					MeasuredMbps: &measuredMbps,
					MeasuredAt:   &measuredAt,
					DisplayHDR:   &displayHDR,
					DisplayDV:    &displayDV,
				},
				NavigationTabVisibilityIncludesSystemTabs: &includeSystemTabs,
			},
		},
	})

	opts := SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, UserID: "default"}
	if _, err := svc.Search(t.Context(), opts); err != nil {
		t.Fatalf("browser search returned error: %v", err)
	}

	phoneOpts := opts
	phoneOpts.ClientID = "phone"
	if _, err := svc.Search(t.Context(), phoneOpts); err != nil {
		t.Fatalf("phone search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected non-search client settings with adaptive disabled to share cache, got %d calls", got)
	}
}

func TestSearchCacheSplitsAcrossClientsWithDifferentEffectiveSettings(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	settings.Ranking.Criteria = []config.RankingCriterion{
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 1},
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 2},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.720p.large", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
			{Title: "Movie.2160p.small", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
		},
	}
	svc := NewService(mgr, nil, debridSvc)
	svc.SetClientSettingsProvider(mapClientSettingsProvider{
		settings: map[string]*models.ClientFilterSettings{
			"living-room": {
				RankingCriteria: &[]models.ClientRankingCriterion{
					{ID: config.RankingSize, Order: models.IntPtr(1)},
					{ID: config.RankingResolution, Order: models.IntPtr(2)},
				},
			},
			"phone": {
				RankingCriteria: &[]models.ClientRankingCriterion{
					{ID: config.RankingSize, Order: models.IntPtr(2)},
					{ID: config.RankingResolution, Order: models.IntPtr(1)},
				},
			},
		},
	})

	first, err := svc.Search(t.Context(), SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, UserID: "default", ClientID: "living-room"})
	if err != nil {
		t.Fatalf("first search returned error: %v", err)
	}
	if got := first[0].Title; got != "Movie.720p.large" {
		t.Fatalf("expected size-first ranking to prefer large file, got %q", got)
	}

	second, err := svc.Search(t.Context(), SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024, UserID: "default", ClientID: "phone"})
	if err != nil {
		t.Fatalf("second search returned error: %v", err)
	}
	if got := debridSvc.calls.Load(); got != 2 {
		t.Fatalf("expected different effective client settings to miss cache, got %d calls", got)
	}
	if got := second[0].Title; got != "Movie.2160p.small" {
		t.Fatalf("expected resolution-first ranking to prefer 2160p result, got %q", got)
	}
}

func TestSearchWithScoringCachesRawResultsForIncludeFiltered(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = true
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridSvc := &countingDebridSearchService{
		results: []models.NZBResult{
			{
				Title:       "Movie.2160p.WEB-DL",
				Indexer:     "AIOStreams",
				ServiceType: models.ServiceTypeDebrid,
				Attributes:  map[string]string{"resolution": "2160p"},
			},
		},
	}
	svc := NewService(mgr, nil, debridSvc)

	opts := SearchOptions{
		Query:           "Movie 2024",
		MediaType:       "movie",
		Year:            2024,
		IncludeFiltered: true,
	}
	first, err := svc.SearchWithScoring(t.Context(), opts)
	if err != nil {
		t.Fatalf("first search with scoring returned error: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 result, got %d", len(first))
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected underlying search call count 1, got %d", got)
	}

	second, err := svc.SearchWithScoring(t.Context(), opts)
	if err != nil {
		t.Fatalf("second search with scoring returned error: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("expected cached result count 1, got %d", len(second))
	}
	if got := debridSvc.calls.Load(); got != 1 {
		t.Fatalf("expected raw cache hit to avoid another underlying call, got %d calls", got)
	}
}

func TestSearchWithScoringDoesNotCapRawDebridBeforeRanking(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Ranking.Criteria = []config.RankingCriterion{
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 0},
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, maxAwareDebridSearchService{
		results: []models.NZBResult{
			{
				Title:       "Movie.2024.2160p.small",
				Indexer:     "FirstSource",
				ServiceType: models.ServiceTypeDebrid,
				SizeBytes:   10,
				Attributes:  map[string]string{"resolution": "2160p"},
			},
			{
				Title:       "Movie.2024.2160p.large",
				Indexer:     "SecondSource",
				ServiceType: models.ServiceTypeDebrid,
				SizeBytes:   100,
				Attributes:  map[string]string{"resolution": "2160p"},
			},
		},
	})

	opts := SearchOptions{
		Query:           "Movie 2024",
		MediaType:       "movie",
		Year:            2024,
		MaxResults:      1,
		IncludeFiltered: true,
	}
	scored, err := svc.SearchWithScoring(t.Context(), opts)
	if err != nil {
		t.Fatalf("SearchWithScoring returned error: %v", err)
	}
	if len(scored) != 1 {
		t.Fatalf("expected MaxResults to apply after ranking, got %d result(s)", len(scored))
	}
	if got := scored[0].Title; got != "Movie.2024.2160p.large" {
		t.Fatalf("expected full raw set to be ranked before limiting, got first title %q", got)
	}

	uncappedOpts := opts
	uncappedOpts.MaxResults = 0
	uncapped, err := svc.SearchWithScoring(t.Context(), uncappedOpts)
	if err != nil {
		t.Fatalf("uncapped SearchWithScoring returned error: %v", err)
	}
	if len(uncapped) < 2 {
		t.Fatalf("expected uncapped search to keep the full ranked set, got %d result(s)", len(uncapped))
	}

	testResults, err := svc.SearchTest(t.Context(), opts)
	if err != nil {
		t.Fatalf("SearchTest returned error: %v", err)
	}
	if len(testResults) < 2 {
		t.Fatalf("expected search test to ignore MaxResults, got %d result(s)", len(testResults))
	}
	if got := testResults[0].Title; got != "Movie.2024.2160p.large" {
		t.Fatalf("expected search test to rank full raw set before limiting, got first title %q", got)
	}
	if len(testResults[0].ScoreBreakdown) == 0 {
		t.Fatal("expected search test to include score breakdown")
	}
}

func TestSearchWithScoringOmitsScoreBreakdownByDefault(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, stubDebridSearchService{
		results: []models.NZBResult{
			{
				Title:       "Movie.2024.1080p.WEB-DL",
				Indexer:     "Test",
				ServiceType: models.ServiceTypeDebrid,
				Attributes:  map[string]string{"resolution": "1080p"},
			},
		},
	})

	opts := SearchOptions{
		Query:           "Movie 2024",
		MediaType:       "movie",
		Year:            2024,
		IncludeFiltered: true,
	}
	scored, err := svc.SearchWithScoring(t.Context(), opts)
	if err != nil {
		t.Fatalf("SearchWithScoring returned error: %v", err)
	}
	if len(scored) != 1 {
		t.Fatalf("expected 1 result, got %d", len(scored))
	}
	if len(scored[0].ScoreBreakdown) != 0 {
		t.Fatalf("expected client search to omit score breakdown, got %#v", scored[0].ScoreBreakdown)
	}

	opts.IncludeScoreBreakdown = true
	withBreakdown, err := svc.SearchWithScoring(t.Context(), opts)
	if err != nil {
		t.Fatalf("SearchWithScoring with breakdown returned error: %v", err)
	}
	if len(withBreakdown) != 1 || len(withBreakdown[0].ScoreBreakdown) == 0 {
		t.Fatal("expected score breakdown when IncludeScoreBreakdown is set")
	}
}

func TestSearchWithScoringCapsPassedBeforeFiltered(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Filtering.FilterOutTerms = []string{"CAM"}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, stubDebridSearchService{
		results: []models.NZBResult{
			{
				Title:       "Movie.2024.1080p.WEB-DL",
				Indexer:     "Test",
				ServiceType: models.ServiceTypeDebrid,
				SizeBytes:   100,
				Attributes:  map[string]string{"resolution": "1080p"},
			},
			{
				Title:       "Movie.2024.720p.WEB-DL",
				Indexer:     "Test",
				ServiceType: models.ServiceTypeDebrid,
				SizeBytes:   50,
				Attributes:  map[string]string{"resolution": "720p"},
			},
			{
				Title:       "Movie.2024.CAM",
				Indexer:     "Test",
				ServiceType: models.ServiceTypeDebrid,
				SizeBytes:   10,
				Attributes:  map[string]string{"resolution": "480p"},
			},
			{
				Title:       "Movie.2024.CAM.2",
				Indexer:     "Test",
				ServiceType: models.ServiceTypeDebrid,
				SizeBytes:   8,
				Attributes:  map[string]string{"resolution": "480p"},
			},
		},
	})

	scored, err := svc.SearchWithScoring(t.Context(), SearchOptions{
		Query:           "Movie 2024",
		MediaType:       "movie",
		Year:            2024,
		MaxResults:      3,
		IncludeFiltered: true,
	})
	if err != nil {
		t.Fatalf("SearchWithScoring returned error: %v", err)
	}
	if len(scored) != 3 {
		t.Fatalf("expected 3 results after cap, got %d", len(scored))
	}
	passed := 0
	filtered := 0
	for _, result := range scored {
		if result.FilterStatus == "filtered" {
			filtered++
			continue
		}
		passed++
	}
	if passed != 2 || filtered != 1 {
		t.Fatalf("expected 2 passed then 1 filtered, got passed=%d filtered=%d", passed, filtered)
	}
}

func TestSearchSplitBypassesRankingForAIOStreamsOnlyDebridMode(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = true
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, stubDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.720p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid},
			{Title: "Movie.2160p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid},
		},
	})

	debridChan, usenetChan := svc.SearchSplit(t.Context(), SearchOptions{Query: "Movie 2024", MediaType: "movie", Year: 2024})
	debridResult, ok := <-debridChan
	if !ok {
		t.Fatal("expected debrid split result")
	}
	for range usenetChan {
	}
	if debridResult.Err != nil {
		t.Fatalf("split debrid returned error: %v", debridResult.Err)
	}
	if len(debridResult.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(debridResult.Results))
	}
	if got := debridResult.Results[0].Title; got != "Movie.720p.WEB-DL" {
		t.Fatalf("expected split AIOStreams order to bypass ranking, got first title %q", got)
	}
}

// TestSearchWithScoringSplitEmitsUsenetBeforeSlowDebrid is the service-level
// OPP-2 verification: the split search must emit the usenet source's scored
// candidates while the debrid scraper is still blocked (its slow tail must not
// gate usenet resolution). The debrid scraper is gated on an explicit release so
// the ordering is deterministic.
func TestSearchWithScoringSplitEmitsUsenetBeforeSlowDebrid(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
  <item>
    <title>Her.2013.1080p.BluRay.x264</title>
    <guid>nzb-her-1</guid>
    <link>http://example.com/nzb/1</link>
    <pubDate>Tue, 21 Jan 2014 10:00:00 GMT</pubDate>
    <newznab:attr name="size" value="1500000000"/>
  </item>
</channel>
</rss>`))
	}))
	defer mockServer.Close()

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Indexers = []config.IndexerConfig{
		{Name: "UsenetIndexer", URL: mockServer.URL, APIKey: "key", Type: "newznab", Categories: "2000,2040", Enabled: true},
	}
	settings.Streaming.ServiceMode = config.StreamingServiceModeHybrid
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	debridGated := &gatedDebridSearchService{
		started:  make(chan struct{}),
		finished: make(chan struct{}),
		release:  make(chan struct{}),
		results: []models.NZBResult{
			{Title: "Her.2013.2160p.DV.Remux", ServiceType: models.ServiceTypeDebrid},
		},
	}
	svc := NewService(mgr, nil, debridGated)

	usenetCh, debridCh := svc.SearchWithScoringSplit(t.Context(), SearchOptions{
		Query:     "Her 2013",
		MediaType: "movie",
		Year:      2013,
	})

	// Wait until the debrid scraper is actually mid-search (started but still
	// blocked on release) before asserting the usenet source is emitted during
	// the stall.
	select {
	case <-debridGated.started:
	case <-time.After(3 * time.Second):
		t.Fatal("debrid scraper never started")
	}

	// The usenet source must be emitted while debrid is still blocked.
	var usenetRes ScoredSplitSearchResult
	select {
	case usenetRes = <-usenetCh:
	case <-time.After(3 * time.Second):
		t.Fatal("usenet source was not emitted during the debrid stall")
	}
	select {
	case <-debridGated.finished:
		t.Fatal("debrid source completed before usenet was emitted")
	default:
	}
	if usenetRes.Err != nil {
		t.Fatalf("usenet emit error: %v", usenetRes.Err)
	}
	if usenetRes.RawCount == 0 {
		t.Fatal("usenet emitted no raw results")
	}
	if len(usenetRes.Scored) == 0 {
		t.Fatal("usenet emitted no passed candidates during the debrid stall")
	}

	// Release the debrid scraper; its own source must then be emitted too.
	close(debridGated.release)
	var debridRes ScoredSplitSearchResult
	select {
	case debridRes = <-debridCh:
	case <-time.After(3 * time.Second):
		t.Fatal("debrid source was never emitted after release")
	}
	if debridRes.Err != nil {
		t.Fatalf("debrid emit error: %v", debridRes.Err)
	}
	if len(debridRes.Scored) == 0 {
		t.Fatal("debrid emitted no passed candidates")
	}
}

func TestSearchWithScoringBypassesFilteringAndRankingForAIOStreamsOnlyDebridMode(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	mgr := config.NewManager(cfgPath)

	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = true
	settings.Filtering.RequiredTerms = []string{"MULTI"}
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "AIOStreams", Type: "aiostreams", URL: "https://example.test/manifest.json", Enabled: true},
	}
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	svc := NewService(mgr, nil, stubDebridSearchService{
		results: []models.NZBResult{
			{Title: "Movie.720p.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid},
			{Title: "Movie.2160p.MULTI.WEB-DL", Indexer: "AIOStreams", ServiceType: models.ServiceTypeDebrid},
		},
	})

	results, err := svc.SearchWithScoring(t.Context(), SearchOptions{
		Query:           "Movie 2024",
		MediaType:       "movie",
		Year:            2024,
		IncludeFiltered: true,
	})
	if err != nil {
		t.Fatalf("search with scoring returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected both AIOStreams results to pass, got %d", len(results))
	}
	if got := results[0].Title; got != "Movie.720p.WEB-DL" {
		t.Fatalf("expected AIOStreams order to bypass scoring sort, got first title %q", got)
	}
	for _, result := range results {
		if result.FilterStatus != "passed" {
			t.Fatalf("expected all results to be marked passed, got %q for %q", result.FilterStatus, result.Title)
		}
		if result.FilterReason != "" {
			t.Fatalf("expected no filter reason for bypassed result %q, got %q", result.Title, result.FilterReason)
		}
		// In bypass mode mediastorm does not score results, so they must be flagged so
		// UIs can hide the meaningless "Score 0" value.
		if result.Attributes["ranking_bypassed"] != "true" {
			t.Fatalf("expected ranking_bypassed=true attribute on bypassed result %q, got attrs %v", result.Title, result.Attributes)
		}
		if result.TotalScore != 0 {
			t.Fatalf("expected TotalScore 0 for bypassed result %q, got %d", result.Title, result.TotalScore)
		}
	}
}

func TestBuildSearchQueries_AnimeAbsoluteEpisode(t *testing.T) {
	opts := SearchOptions{
		Query:                 "One Piece S23E06",
		MediaType:             "series",
		IsAnime:               true,
		AbsoluteEpisodeNumber: 1161,
	}

	queries := buildSearchQueries(opts, debrid.ParseQuery(opts.Query), nil)

	foundStandard := false
	for _, query := range queries {
		if query == "One Piece S23E06" {
			foundStandard = true
			break
		}
	}
	if !foundStandard {
		t.Fatalf("expected multi-season standard query to remain in %v", queries)
	}

	for _, expected := range []string{"One Piece 1161", "One Piece EP1161", "One Piece E1161"} {
		found := false
		for _, query := range queries {
			if query == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected query %q in %v", expected, queries)
		}
	}
}

func TestBuildSearchQueries_UFCEventAddsShortEventQuery(t *testing.T) {
	opts := SearchOptions{
		Query:     "UFC 322: Della Maddalena vs Makhachev",
		MediaType: "movie",
		Year:      2025,
	}

	queries := buildSearchQueries(opts, debrid.ParseQuery(opts.Query), nil)

	if len(queries) == 0 || queries[0] != "UFC 322" {
		t.Fatalf("expected UFC 322 to be first query, got %v", queries)
	}
}

// Verify categories string parsing handles various formats
func TestCategoriesStringParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "2000,5000", "2000,5000"},
		{"with spaces", " 2000 , 5000 ", "2000 , 5000"}, // TrimSpace only trims leading/trailing whitespace
		{"single", "2000", "2000"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := strings.TrimSpace(tc.input)
			if result != tc.expected {
				t.Errorf("expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	if size := parseSize("1024", ""); size != 1024 {
		t.Fatalf("expected 1024, got %d", size)
	}
	if size := parseSize("", "2048"); size != 2048 {
		t.Fatalf("expected 2048, got %d", size)
	}
	if size := parseSize("abc", "xyz"); size != 0 {
		t.Fatalf("expected 0 for invalid inputs, got %d", size)
	}
}

func TestParsePubDate(t *testing.T) {
	sample := "Mon, 02 Jan 2006 15:04:05 -0700"
	parsed := parsePubDate(sample)
	if parsed.IsZero() {
		t.Fatal("expected parsed time")
	}
	if parsed.Year() != 2006 {
		t.Fatalf("expected year 2006, got %d", parsed.Year())
	}
	if !parsePubDate("invalid").IsZero() {
		t.Fatal("expected zero time for invalid date")
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"Action", "action", " Drama ", ""})
	if len(got) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(got))
	}
	if got[0] != "Action" {
		t.Fatalf("expected first item to be Action, got %s", got[0])
	}
	if got[1] != "Drama" {
		t.Fatalf("expected second item to be Drama, got %s", got[1])
	}
}

// mockMetadataSearchOnly implements only the Search method (no FetchAliases).
type mockMetadataSearchOnly struct {
	results []models.SearchResult
}

func (m *mockMetadataSearchOnly) Search(_ context.Context, _ string, _ string) ([]models.SearchResult, error) {
	return m.results, nil
}

// mockMetadataWithAliases implements both Search, FetchAliases, and FetchAliasesWithLanguage.
type mockMetadataWithAliases struct {
	results     []models.SearchResult
	aliases     map[int64][]string               // tvdbID -> aliases (for FetchAliases)
	langAliases map[int64][]models.LanguageAlias // tvdbID -> language-tagged aliases
}

func (m *mockMetadataWithAliases) Search(_ context.Context, _ string, _ string) ([]models.SearchResult, error) {
	return m.results, nil
}

func (m *mockMetadataWithAliases) FetchAliases(_ string, tvdbID int64) []string {
	return m.aliases[tvdbID]
}

func (m *mockMetadataWithAliases) FetchAliasesWithLanguage(_ string, tvdbID int64) []models.LanguageAlias {
	if m.langAliases != nil {
		return m.langAliases[tvdbID]
	}
	// Fallback: convert plain aliases to LanguageAlias with empty language
	var result []models.LanguageAlias
	for _, a := range m.aliases[tvdbID] {
		result = append(result, models.LanguageAlias{Name: a})
	}
	return result
}

func TestResolveAlternateTitles_WithoutAliases(t *testing.T) {
	// When metadata service doesn't implement FetchAliases, we should still
	// get alternates from the search API translations.
	// OriginalName is set when it differs from Name (metadata.Search only sets
	// it when the TVDB primary name differs from the translated name).
	mock := &mockMetadataSearchOnly{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:            "Formula 1: Drive to Survive",
					OriginalName:    "", // Same as Name, so not set
					TVDBID:          12345,
					MediaType:       "series",
					Year:            2019,
					AlternateTitles: []string{"Formula 1: Život u šestoj brzini"},
				},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Formula 1: Drive to Survive S08E04",
		MediaType: "series",
		Year:      2019,
	}, "eng", 0)

	// Should have the Croatian alternate from search translations
	if len(aliases) != 1 {
		t.Fatalf("expected 1 alias from search translations, got %d: %v", len(aliases), aliases)
	}
	if aliases[0] != "Formula 1: Život u šestoj brzini" {
		t.Errorf("expected Croatian alternate, got %q", aliases[0])
	}
}

func TestResolveAlternateTitles_WithAliases(t *testing.T) {
	// When metadata service implements FetchAliases, we should get aliases
	// from both search translations AND the TVDB aliases endpoint.
	// This is the key fix: TVDB search translations are often incomplete,
	// missing languages like French. The aliases endpoint has them all.
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:            "Formula 1: Drive to Survive",
					OriginalName:    "", // Same as Name
					TVDBID:          12345,
					MediaType:       "series",
					Year:            2019,
					AlternateTitles: []string{"Formula 1: Život u šestoj brzini"},
				},
			},
		},
		aliases: map[int64][]string{
			12345: {
				"Formula 1 : Pilotes de leur destin",     // French
				"Fórmula 1: La emoción de un Grand Prix", // Spanish
				"Formula 1: Život u šestoj brzini",       // Croatian (dupe of search translation)
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Formula 1: Drive to Survive S08E04",
		MediaType: "series",
		Year:      2019,
	}, "eng", 0)

	// Should have Croatian from search + French and Spanish from TVDB aliases
	// (Croatian dupe should be deduplicated)
	if len(aliases) != 3 {
		t.Fatalf("expected 3 unique aliases, got %d: %v", len(aliases), aliases)
	}

	// Verify the French title is included (the key fix)
	found := false
	for _, a := range aliases {
		if a == "Formula 1 : Pilotes de leur destin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected French alias to be included, got: %v", aliases)
	}
}

func TestResolveAlternateTitles_NoTVDBID(t *testing.T) {
	// When the matched title has no TVDB ID, FetchAliases should not be called
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:         "Some Show",
					OriginalName: "Un Spectacle",
					TVDBID:       0, // No TVDB ID
					MediaType:    "series",
				},
			},
		},
		aliases: map[int64][]string{},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Some Show S01E01",
		MediaType: "series",
	}, "eng", 0)

	// Should only have the original name alias from search
	if len(aliases) != 1 {
		t.Fatalf("expected 1 alias, got %d: %v", len(aliases), aliases)
	}
	if aliases[0] != "Un Spectacle" {
		t.Errorf("expected original name alias, got %q", aliases[0])
	}
}

func TestResolveAlternateTitles_LanguageOrdering(t *testing.T) {
	// Aliases matching the user's metadata language should come before others.
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:      "Gargantia on the Verdurous Planet",
					TVDBID:    99999,
					MediaType: "series",
					Year:      2013,
				},
			},
		},
		langAliases: map[int64][]models.LanguageAlias{
			99999: {
				{Name: "翠星のガルガンティア", Language: "jpn"},
				{Name: "Gargantia", Language: "eng"},
				{Name: "가르간티아", Language: "kor"},
				{Name: "Gargantia sur la planète verte", Language: "fra"},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Gargantia on the Verdurous Planet S01E01",
		MediaType: "series",
		Year:      2013,
	}, "eng", 0)

	// "Gargantia" (eng) should appear before jpn/kor/fra aliases
	if len(aliases) != 4 {
		t.Fatalf("expected 4 aliases, got %d: %v", len(aliases), aliases)
	}
	if aliases[0] != "Gargantia" {
		t.Errorf("expected English alias first, got %q", aliases[0])
	}
}

func TestResolveAlternateTitles_Cap(t *testing.T) {
	// When maxAlternates > 0, aliases should be capped.
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:         "Gargantia on the Verdurous Planet",
					OriginalName: "翠星のガルガンティア",
					TVDBID:       99999,
					MediaType:    "series",
					Year:         2013,
				},
			},
		},
		langAliases: map[int64][]models.LanguageAlias{
			99999: {
				{Name: "Gargantia", Language: "eng"},
				{Name: "가르간티아", Language: "kor"},
				{Name: "Gargantia sur la planète verte", Language: "fra"},
				{Name: "Гаргантия", Language: "rus"},
				{Name: "גרגנטיה", Language: "heb"},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Gargantia on the Verdurous Planet S01E01",
		MediaType: "series",
		Year:      2013,
	}, "eng", 3)

	// OriginalName (翠星のガルガンティア) + eng match (Gargantia) + next one = 3
	if len(aliases) != 3 {
		t.Fatalf("expected 3 aliases after cap, got %d: %v", len(aliases), aliases)
	}
}

func TestResolveAlternateTitles_CapZeroUnlimited(t *testing.T) {
	// Cap of 0 means unlimited — all aliases should be returned.
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:      "Test Show",
					TVDBID:    11111,
					MediaType: "series",
				},
			},
		},
		langAliases: map[int64][]models.LanguageAlias{
			11111: {
				{Name: "Alias A", Language: "eng"},
				{Name: "Alias B", Language: "fra"},
				{Name: "Alias C", Language: "deu"},
				{Name: "Alias D", Language: "jpn"},
				{Name: "Alias E", Language: "kor"},
				{Name: "Alias F", Language: "rus"},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Test Show S01E01",
		MediaType: "series",
	}, "eng", 0)

	if len(aliases) != 6 {
		t.Fatalf("expected 6 aliases (unlimited), got %d: %v", len(aliases), aliases)
	}
}

func TestResolveAlternateTitles_LanguagePriorityWithCap(t *testing.T) {
	// Language-matched aliases should survive capping over non-matched ones.
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:      "Some Anime",
					TVDBID:    22222,
					MediaType: "series",
					Year:      2020,
				},
			},
		},
		langAliases: map[int64][]models.LanguageAlias{
			22222: {
				{Name: "Chinese Title", Language: "zho"},
				{Name: "Korean Title", Language: "kor"},
				{Name: "English Title", Language: "eng"},
				{Name: "French Title", Language: "fra"},
				{Name: "Russian Title", Language: "rus"},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Some Anime S01E01",
		MediaType: "series",
		Year:      2020,
	}, "eng", 2)

	if len(aliases) != 2 {
		t.Fatalf("expected 2 aliases after cap, got %d: %v", len(aliases), aliases)
	}
	// English should be first (language match), then Chinese (first non-match)
	if aliases[0] != "English Title" {
		t.Errorf("expected English alias first (language priority), got %q", aliases[0])
	}
	if aliases[1] != "Chinese Title" {
		t.Errorf("expected Chinese alias second, got %q", aliases[1])
	}
}

func TestResolveAlternateTitles_PrefersRomanizedReleaseTitleBeforeCap(t *testing.T) {
	mock := &mockMetadataSearchOnly{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:         "Martian Successor Nadesico",
					OriginalName: "機動戦艦ナデシコ",
					TVDBID:       71313,
					IMDBID:       "tt0115263",
					MediaType:    "series",
					Year:         1996,
					AlternateTitles: []string{
						"機動戦艦ナデシコ",
						"Kidou Senkan Nadesico",
						"Nadesico Martian Successor",
					},
				},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Martian Successor Nadesico S01E01",
		MediaType: "series",
		Year:      1996,
		IMDBID:    "tt0115263",
	}, "eng", 1)

	if len(aliases) != 1 {
		t.Fatalf("expected one capped alias, got %d: %v", len(aliases), aliases)
	}
	if aliases[0] != "Kidou Senkan Nadesico" {
		t.Fatalf("expected romanized release title to survive cap, got %q", aliases[0])
	}
}

func TestResolveAlternateTitles_AnimePrefersOriginalLanguageRomanization(t *testing.T) {
	mock := &mockMetadataWithAliases{
		results: []models.SearchResult{
			{
				Title: models.Title{
					Name:         "Kaiju No. 8",
					OriginalName: "怪獣8号",
					Language:     "jpn",
					TVDBID:       423075,
					IMDBID:       "tt21975436",
					MediaType:    "series",
					Year:         2024,
				},
			},
		},
		langAliases: map[int64][]models.LanguageAlias{
			423075: {
				{Name: "Monster #8", Language: "eng"},
				{Name: "Kaiju No. Eight", Language: "eng"},
				{Name: "Kaijū 8-gō", Language: "jpn"},
				{Name: "Kaijuu 8-gou", Language: "jpn"},
			},
		},
	}

	svc := &Service{metadata: mock}
	aliases := svc.resolveAlternateTitles(context.Background(), SearchOptions{
		Query:     "Kaiju No. 8 S02E01",
		MediaType: "series",
		Year:      2024,
		IMDBID:    "tt21975436",
		IsAnime:   true,
	}, "eng", 1)

	if len(aliases) != 1 {
		t.Fatalf("expected one capped alias, got %d: %v", len(aliases), aliases)
	}
	if aliases[0] != "Kaijuu 8-gou" {
		t.Fatalf("expected original-language romanized alias before English translation, got %q", aliases[0])
	}

	queries := buildSearchQueries(SearchOptions{
		Query:                 "Kaiju No. 8 S02E01",
		MediaType:             "series",
		IsAnime:               true,
		AbsoluteEpisodeNumber: 13,
	}, debrid.ParseQuery("Kaiju No. 8 S02E01"), aliases)
	want := "Kaijuu 8-gou 13"
	for _, query := range queries {
		if query == want {
			return
		}
	}
	t.Fatalf("expected romanized absolute query %q in %v", want, queries)
}

func TestPerResolutionLimiting(t *testing.T) {
	// Helper to make results with resolution in title
	makeResult := func(title string) models.NZBResult {
		return models.NZBResult{Title: title}
	}

	results := []models.NZBResult{
		makeResult("Movie.2160p.WEB-DL.x265"),
		makeResult("Movie.2160p.BluRay.x265"),
		makeResult("Movie.2160p.REMUX.x265"),
		makeResult("Movie.1080p.WEB-DL.x264"),
		makeResult("Movie.1080p.BluRay.x264"),
		makeResult("Movie.1080p.REMUX.x264"),
		makeResult("Movie.720p.WEB-DL.x264"),
		makeResult("Movie.720p.BluRay.x264"),
	}

	t.Run("limits results per resolution tier", func(t *testing.T) {
		maxPerRes := 2
		resolutionCounts := map[int]int{}
		var limited []models.NZBResult
		for _, r := range results {
			res := extractResolutionFromResult(r)
			if resolutionCounts[res] < maxPerRes {
				limited = append(limited, r)
				resolutionCounts[res]++
			}
		}

		if len(limited) != 6 {
			t.Fatalf("expected 6 results (2 per tier), got %d", len(limited))
		}

		// Verify per-tier counts
		tierCounts := map[int]int{}
		for _, r := range limited {
			tierCounts[extractResolutionFromResult(r)]++
		}
		for tier, count := range tierCounts {
			if count > maxPerRes {
				t.Errorf("tier %d has %d results, expected max %d", tier, count, maxPerRes)
			}
		}
	})

	t.Run("zero means no limit", func(t *testing.T) {
		maxPerRes := 0
		if maxPerRes > 0 {
			t.Fatal("should not apply limiting when maxPerRes is 0")
		}
		// All results pass through
		if len(results) != 8 {
			t.Fatalf("expected all 8 results, got %d", len(results))
		}
	})

	t.Run("preserves order within tier", func(t *testing.T) {
		maxPerRes := 1
		resolutionCounts := map[int]int{}
		var limited []models.NZBResult
		for _, r := range results {
			res := extractResolutionFromResult(r)
			if resolutionCounts[res] < maxPerRes {
				limited = append(limited, r)
				resolutionCounts[res]++
			}
		}

		if len(limited) != 3 {
			t.Fatalf("expected 3 results (1 per tier), got %d", len(limited))
		}
		// First result from each tier should be the first in the original order
		if limited[0].Title != "Movie.2160p.WEB-DL.x265" {
			t.Errorf("expected first 2160p result, got %q", limited[0].Title)
		}
		if limited[1].Title != "Movie.1080p.WEB-DL.x264" {
			t.Errorf("expected first 1080p result, got %q", limited[1].Title)
		}
		if limited[2].Title != "Movie.720p.WEB-DL.x264" {
			t.Errorf("expected first 720p result, got %q", limited[2].Title)
		}
	})
}

func TestApplyUserFilterOverridesIncludesProfileRankingAndAdaptiveFields(t *testing.T) {
	dst := models.FilterSettings{
		PreferredScraper:           models.StringPtr("global"),
		ServicePriority:            models.StringPtr("none"),
		AdaptivePlaybackEnabled:    models.BoolPtr(false),
		AdaptiveTargetBufferFactor: models.FloatPtr(0.7),
	}
	applyUserFilterOverrides(&dst, models.FilterSettings{
		PreferredScraper:           models.StringPtr("Comet"),
		ServicePriority:            models.StringPtr("debrid"),
		AdaptivePlaybackEnabled:    models.BoolPtr(true),
		AdaptiveTargetBufferFactor: models.FloatPtr(0.55),
	})
	if models.StringVal(dst.PreferredScraper, "") != "Comet" || models.StringVal(dst.ServicePriority, "") != "debrid" {
		t.Fatalf("ranking fields were not applied: %#v", dst)
	}
	if !models.BoolVal(dst.AdaptivePlaybackEnabled, false) || models.FloatVal(dst.AdaptiveTargetBufferFactor, 0) != 0.55 {
		t.Fatalf("adaptive fields were not applied: %#v", dst)
	}
}

func TestComparePreferredScraper(t *testing.T) {
	torrentio := models.NZBResult{Title: "Movie.2160p", Indexer: "Torrentio"}
	jackett := models.NZBResult{Title: "Movie.2160p", Indexer: "Jackett"}
	zilean := models.NZBResult{Title: "Movie.1080p", Indexer: "Zilean"}

	t.Run("no preferred scraper returns 0", func(t *testing.T) {
		result := comparePreferredScraper(torrentio, jackett, "")
		if result != 0 {
			t.Errorf("expected 0 when no preferred scraper, got %d", result)
		}
	})

	t.Run("preferred scraper ranks higher", func(t *testing.T) {
		result := comparePreferredScraper(torrentio, jackett, "Torrentio")
		if result != -1 {
			t.Errorf("expected -1 (preferred first), got %d", result)
		}
	})

	t.Run("non-preferred scraper ranks lower", func(t *testing.T) {
		result := comparePreferredScraper(jackett, torrentio, "Torrentio")
		if result != 1 {
			t.Errorf("expected 1 (preferred second), got %d", result)
		}
	})

	t.Run("both from preferred scraper returns 0", func(t *testing.T) {
		torrentio2 := models.NZBResult{Title: "Movie.1080p", Indexer: "Torrentio"}
		result := comparePreferredScraper(torrentio, torrentio2, "Torrentio")
		if result != 0 {
			t.Errorf("expected 0 when both match, got %d", result)
		}
	})

	t.Run("neither from preferred scraper returns 0", func(t *testing.T) {
		result := comparePreferredScraper(jackett, zilean, "Torrentio")
		if result != 0 {
			t.Errorf("expected 0 when neither match, got %d", result)
		}
	})

	t.Run("case insensitive matching", func(t *testing.T) {
		result := comparePreferredScraper(torrentio, jackett, "torrentio")
		if result != -1 {
			t.Errorf("expected -1 (case insensitive match), got %d", result)
		}
	})
}

func TestPreferredScraperRankingIntegration(t *testing.T) {
	// Test that the preferred-scraper criterion integrates correctly with the ranking system
	results := []models.NZBResult{
		{Title: "Movie.2160p.WEB-DL", Indexer: "Jackett", SizeBytes: 5000},
		{Title: "Movie.2160p.BluRay", Indexer: "Torrentio", SizeBytes: 8000},
		{Title: "Movie.1080p.WEB-DL", Indexer: "Torrentio", SizeBytes: 3000},
		{Title: "Movie.1080p.BluRay", Indexer: "Zilean", SizeBytes: 4000},
	}

	t.Run("preferred scraper criterion boosts matching results", func(t *testing.T) {
		criteria := []config.RankingCriterion{
			{ID: config.RankingPreferredScraper, Name: "Preferred Scraper", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		}
		preferredScraper := "Torrentio"

		sorted := make([]models.NZBResult, len(results))
		copy(sorted, results)

		sort.SliceStable(sorted, func(i, j int) bool {
			for _, criterion := range criteria {
				if !criterion.Enabled {
					continue
				}
				var result int
				switch criterion.ID {
				case config.RankingPreferredScraper:
					result = comparePreferredScraper(sorted[i], sorted[j], preferredScraper)
				case config.RankingResolution:
					result = compareResolution(sorted[i], sorted[j])
				}
				if result != 0 {
					return result < 0
				}
			}
			return false
		})

		// Torrentio results should come first
		if sorted[0].Indexer != "Torrentio" {
			t.Errorf("expected first result from Torrentio, got %q", sorted[0].Indexer)
		}
		if sorted[1].Indexer != "Torrentio" {
			t.Errorf("expected second result from Torrentio, got %q", sorted[1].Indexer)
		}
		// Within Torrentio results, higher resolution should come first
		if !strings.Contains(sorted[0].Title, "2160p") {
			t.Errorf("expected 2160p Torrentio result first, got %q", sorted[0].Title)
		}
	})

	t.Run("disabled criterion has no effect", func(t *testing.T) {
		criteria := []config.RankingCriterion{
			{ID: config.RankingPreferredScraper, Name: "Preferred Scraper", Enabled: false, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		}

		sorted := make([]models.NZBResult, len(results))
		copy(sorted, results)

		sort.SliceStable(sorted, func(i, j int) bool {
			for _, criterion := range criteria {
				if !criterion.Enabled {
					continue
				}
				var result int
				switch criterion.ID {
				case config.RankingPreferredScraper:
					result = comparePreferredScraper(sorted[i], sorted[j], "Torrentio")
				case config.RankingResolution:
					result = compareResolution(sorted[i], sorted[j])
				}
				if result != 0 {
					return result < 0
				}
			}
			return false
		})

		// Should sort purely by resolution since preferred scraper is disabled
		if !strings.Contains(sorted[0].Title, "2160p") {
			t.Errorf("expected 2160p result first when criterion disabled, got %q", sorted[0].Title)
		}
		if !strings.Contains(sorted[1].Title, "2160p") {
			t.Errorf("expected 2160p result second when criterion disabled, got %q", sorted[1].Title)
		}
	})
}

func TestDefaultRankingCriteriaIncludesPreferredScraper(t *testing.T) {
	criteria := config.DefaultRankingCriteria()

	found := false
	for _, c := range criteria {
		if c.ID == config.RankingPreferredScraper {
			found = true
			if c.Enabled {
				t.Error("expected preferred-scraper criterion to be disabled by default")
			}
			if c.Name != "Preferred Scraper" {
				t.Errorf("expected name 'Preferred Scraper', got %q", c.Name)
			}
			break
		}
	}
	if !found {
		t.Error("expected preferred-scraper criterion in default ranking criteria")
	}
}

func TestServiceSpecificRankingCriteria(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Ranking.Criteria = []config.RankingCriterion{
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 1},
	}
	settings.Ranking.SplitByService = true
	settings.Ranking.Debrid = &config.RankingSettings{Criteria: []config.RankingCriterion{
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 0},
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
	}}
	settings.Ranking.Usenet = &config.RankingSettings{Criteria: []config.RankingCriterion{
		{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 1},
	}}

	svc := &Service{}
	rankingBundle := svc.getEffectiveRankingBundle("", "", settings)
	baseCtx := ScoringContext{RankingCriteria: rankingBundle.Default}

	debridResults := []models.NZBResult{
		{Title: "Movie.2160p.small", ServiceType: models.ServiceTypeDebrid, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
		{Title: "Movie.720p.large", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
	}
	sortResultsByRankingBundle(debridResults, baseCtx, rankingBundle)
	if got := debridResults[0].Title; got != "Movie.720p.large" {
		t.Fatalf("expected debrid-specific size-first ranking to prefer large file, got %q", got)
	}

	usenetResults := []models.NZBResult{
		{Title: "Movie.720p.large", ServiceType: models.ServiceTypeUsenet, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
		{Title: "Movie.2160p.small", ServiceType: models.ServiceTypeUsenet, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
	}
	sortResultsByRankingBundle(usenetResults, baseCtx, rankingBundle)
	if got := usenetResults[0].Title; got != "Movie.2160p.small" {
		t.Fatalf("expected usenet-specific resolution-first ranking to prefer 2160p file, got %q", got)
	}

	mixed := []models.NZBResult{
		{Title: "Movie.720p.large", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
		{Title: "Movie.2160p.small", ServiceType: models.ServiceTypeUsenet, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
	}
	sortResultsByRankingBundle(mixed, baseCtx, rankingBundle)
	if got := mixed[0].Title; got != "Movie.2160p.small" {
		t.Fatalf("expected service lists to merge using shared resolution-first ranking, got %q", got)
	}
}

func TestServiceSpecificRankingPreservedDuringOverallMerge(t *testing.T) {
	rankings := effectiveRankingBundle{
		Default: []config.RankingCriterion{
			{ID: config.RankingServicePriority, Enabled: true, Order: 0},
			{ID: config.RankingResolution, Enabled: true, Order: 1},
		},
		Debrid: []config.RankingCriterion{
			{ID: config.RankingSize, Enabled: true, Order: 0},
		},
		Usenet: []config.RankingCriterion{
			{ID: config.RankingResolution, Enabled: true, Order: 0},
		},
	}
	ctx := ScoringContext{ServicePriority: config.StreamingServicePriorityDebrid}
	results := []models.NZBResult{
		{Title: "Debrid.2160p.small", ServiceType: models.ServiceTypeDebrid, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
		{Title: "Usenet.720p.large", ServiceType: models.ServiceTypeUsenet, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
		{Title: "Debrid.720p.large", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
		{Title: "Usenet.2160p.small", ServiceType: models.ServiceTypeUsenet, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
	}

	sortResultsByRankingBundle(results, ctx, rankings)
	want := []string{
		"Debrid.720p.large",
		"Debrid.2160p.small",
		"Usenet.2160p.small",
		"Usenet.720p.large",
	}
	for i := range want {
		if results[i].Title != want[i] {
			t.Fatalf("result %d = %q, want %q (full order: %#v)", i, results[i].Title, want[i], results)
		}
	}
}

func TestSharedRankingBundleMatchesSingleOverallSort(t *testing.T) {
	criteria := []config.RankingCriterion{
		{ID: config.RankingResolution, Enabled: true, Order: 0},
		{ID: config.RankingSize, Enabled: true, Order: 1},
	}
	rankings := effectiveRankingBundle{Default: criteria, Debrid: criteria, Usenet: criteria}
	ctx := ScoringContext{RankingCriteria: criteria}
	results := []models.NZBResult{
		{Title: "Debrid.720p", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "720p"}},
		{Title: "Usenet.2160p.small", ServiceType: models.ServiceTypeUsenet, SizeBytes: 1000, Attributes: map[string]string{"resolution": "2160p"}},
		{Title: "Debrid.2160p.large", ServiceType: models.ServiceTypeDebrid, SizeBytes: 9000, Attributes: map[string]string{"resolution": "2160p"}},
		{Title: "Usenet.1080p", ServiceType: models.ServiceTypeUsenet, SizeBytes: 5000, Attributes: map[string]string{"resolution": "1080p"}},
	}
	want := append([]models.NZBResult(nil), results...)
	sort.SliceStable(want, func(i, j int) bool {
		return compareByRankingCriteria(want[i], want[j], ctx) < 0
	})

	sortResultsByRankingBundle(results, ctx, rankings)
	for i := range want {
		if results[i].Title != want[i].Title {
			t.Fatalf("result %d = %q, want %q", i, results[i].Title, want[i].Title)
		}
	}
}

func TestSanitizeNewznabQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Good Luck, Have Fun, Don't Die", "Good Luck Have Fun Dont Die"},
		{"Don't Stop Me Now", "Dont Stop Me Now"},
		{"It\u2019s a Wonderful Life", "Its a Wonderful Life"},                  // curly apostrophe
		{"Hello: World!", "Hello World"},                                        // colon and exclamation
		{"What?", "What"},                                                       // question mark
		{"Tom & Jerry", "Tom Jerry"},                                            // ampersand
		{`She said "hi"`, "She said hi"},                                        // double quotes
		{"normal title", "normal title"},                                        // no change
		{"multiple   spaces", "multiple spaces"},                                // space collapse
		{"(brackets) [and] {braces}", "brackets and braces"},                    // brackets
		{"Udachi, vesel'ia, ne sdokhni 2026", "Udachi veselia ne sdokhni 2026"}, // transliterated apostrophe + commas
		{"Once Upon a Time... in Hollywood", "Once Upon a Time in Hollywood"},   // ellipsis collapses to space
		{"S.W.A.T.", "S W A T"},                                                 // dotted initialism
		{"Ranma ½ S01E01", "Ranma 1 2 S01E01"},                                  // Unicode fraction
		{"Ranma 1/2 S01E01", "Ranma 1 2 S01E01"},                                // ASCII fraction slash
	}
	for _, tt := range tests {
		got := sanitizeNewznabQuery(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeNewznabQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTitleVariantsNormalizeUnicodeFractions(t *testing.T) {
	got := titleVariants("Ranma ½")
	want := []string{"Ranma 1/2"}
	if len(got) != len(want) {
		t.Fatalf("titleVariants returned %d variants, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("titleVariants[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}

func TestExtractResolutionFromResultIgnoresEmbedded4KInReleaseGroup(t *testing.T) {
	got := extractResolutionFromResult(models.NZBResult{
		Title: "The.Office.UK.s02e01.DVDRip.To4kaTV.avi",
	})
	if got != 0 {
		t.Fatalf("expected no resolution from To4kaTV, got %d", got)
	}

	got = extractResolutionFromResult(models.NZBResult{
		Title: "Movie.Name.4K.WEB-DL.mkv",
	})
	if got != 2160 {
		t.Fatalf("expected explicit 4K token to map to 2160, got %d", got)
	}

	got = extractResolutionFromResult(models.NZBResult{
		Title:      "Movie.Name.mkv",
		Attributes: map[string]string{"resolution": "Comet\n4K"},
	})
	if got != 2160 {
		t.Fatalf("expected explicit 4K attribute token to map to 2160, got %d", got)
	}
}

func TestUsenetResultDedupKey(t *testing.T) {
	// Release title takes precedence because GUIDs/URLs are indexer-specific.
	a := models.NZBResult{GUID: "g1", DownloadURL: "u1", Title: "[TRC].Sugar.Apple.Fairy.Tale-S01E11.[English.Dub]", SizeBytes: 312277641}
	b := models.NZBResult{GUID: "g2", DownloadURL: "u2", Title: "[TRC] Sugar Apple Fairy Tale - S01E11 [English Dub]", SizeBytes: 309909000}
	if usenetResultDedupKey(a) != usenetResultDedupKey(b) {
		t.Fatal("same normalized release title should produce same dedup key across indexers")
	}

	// Falls back to GUID when title empty
	guidA := models.NZBResult{GUID: "g1", DownloadURL: "u1", SizeBytes: 100}
	guidB := models.NZBResult{GUID: "g1", DownloadURL: "u2", SizeBytes: 200}
	if usenetResultDedupKey(guidA) != usenetResultDedupKey(guidB) {
		t.Fatal("same GUID should produce same dedup key when title empty")
	}

	// Falls back to download URL when GUID empty
	c := models.NZBResult{DownloadURL: "u1"}
	d := models.NZBResult{DownloadURL: "u1"}
	if usenetResultDedupKey(c) != usenetResultDedupKey(d) {
		t.Fatal("same DownloadURL should produce same dedup key when title+GUID empty")
	}

	// Falls back to link when GUID+URL empty
	e := models.NZBResult{Link: "l1"}
	f := models.NZBResult{Link: "l1"}
	if usenetResultDedupKey(e) != usenetResultDedupKey(f) {
		t.Fatal("same Link should produce same dedup key when GUID+URL empty")
	}

	// Title matching is case-insensitive and ignores file extensions.
	g := models.NZBResult{Title: "Coco 2017", SizeBytes: 123}
	h := models.NZBResult{Title: "coco.2017.mkv", SizeBytes: 456}
	if usenetResultDedupKey(g) != usenetResultDedupKey(h) {
		t.Fatal("release title key should normalize separators, extension, and case")
	}

	// Distinct releases produce distinct keys
	if usenetResultDedupKey(a) == usenetResultDedupKey(models.NZBResult{Title: "Coco 2017"}) {
		t.Fatal("different release titles should produce different dedup keys")
	}
}

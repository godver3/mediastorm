package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
)

func TestSearchTorznab_IndexerCategories(t *testing.T) {
	// Track the categories received by the mock server
	var receivedCategories string

	// Create a mock newznab server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCategories = r.URL.Query().Get("cat")
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

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cat := r.URL.Query().Get("cat")
		requestLog = append(requestLog, cat)
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

	// Check each request had the correct categories
	expectedCats := []string{"2000,2040", "5000,5030", ""}
	for i, expected := range expectedCats {
		if requestLog[i] != expected {
			t.Errorf("request %d: expected categories '%s', got '%s'", i, expected, requestLog[i])
		}
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

// mockMetadataWithAliases implements both Search and FetchAliases.
type mockMetadataWithAliases struct {
	results []models.SearchResult
	aliases map[int64][]string // tvdbID -> aliases
}

func (m *mockMetadataWithAliases) Search(_ context.Context, _ string, _ string) ([]models.SearchResult, error) {
	return m.results, nil
}

func (m *mockMetadataWithAliases) FetchAliases(mediaType string, tvdbID int64) []string {
	return m.aliases[tvdbID]
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
	})

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
	})

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
	})

	// Should only have the original name alias from search
	if len(aliases) != 1 {
		t.Fatalf("expected 1 alias, got %d: %v", len(aliases), aliases)
	}
	if aliases[0] != "Un Spectacle" {
		t.Errorf("expected original name alias, got %q", aliases[0])
	}
}

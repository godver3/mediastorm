package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"novastream/models"
)

// TestGetCustomListFetchesTranslations verifies that GetCustomList fetches translations
// for series items when the base TVDB data has non-English content.
func TestGetCustomListFetchesTranslations(t *testing.T) {
	var (
		mu                  sync.Mutex
		translationsFetched []string
	)

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			defer mu.Unlock()

			path := req.URL.Path

			// Handle TVDB login
			if path == "/v4/login" {
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			// Handle MDBList custom list fetch
			if strings.Contains(req.URL.Host, "mdblist.com") {
				items := []mdblistItem{
					{
						ID:          1,
						Rank:        1,
						Title:       "Test Anime",
						TVDBID:      ptr(int64(12345)),
						IMDBID:      "tt1234567",
						MediaType:   "show",
						ReleaseYear: 2024,
					},
				}
				body, _ := json.Marshal(items)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBuffer(body)), Header: make(http.Header)}, nil
			}

			// Handle TVDB series extended (includes name/overview/artwork/status) — primary call
			if strings.HasPrefix(path, "/v4/series/12345/extended") {
				body := bytes.NewBufferString(`{"data":{"id":12345,"name":"テストアニメ","overview":"これは日本語の概要です","artworks":[]}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			// Handle TVDB series translations - return English translation
			if strings.HasPrefix(path, "/v4/series/12345/translations/") {
				lang := strings.TrimPrefix(path, "/v4/series/12345/translations/")
				translationsFetched = append(translationsFetched, lang)
				body := bytes.NewBufferString(`{"data":{"language":"eng","name":"Test Anime English","overview":"This is the English overview"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			t.Logf("Unhandled request: %s %s", req.Method, req.URL.String())
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	// Create a service with the mock HTTP client
	service := &Service{
		client: newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:  newFileCache(t.TempDir(), 24),
	}
	service.client.minInterval = 0

	// Call GetCustomList
	items, filteredTotal, unfilteredTotal, err := service.GetCustomList(context.Background(), "https://mdblist.com/lists/test/anime/json", CustomListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("GetCustomList failed: %v", err)
	}

	if unfilteredTotal != 1 {
		t.Fatalf("expected unfilteredTotal=1, got %d", unfilteredTotal)
	}

	if filteredTotal != 1 {
		t.Fatalf("expected filteredTotal=1, got %d", filteredTotal)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	// Verify translations were fetched
	mu.Lock()
	defer mu.Unlock()
	if len(translationsFetched) == 0 {
		t.Fatal("expected translations to be fetched, but none were")
	}

	foundEng := false
	for _, lang := range translationsFetched {
		if lang == "eng" {
			foundEng = true
			break
		}
	}
	if !foundEng {
		t.Fatalf("expected 'eng' translation to be fetched, got: %v", translationsFetched)
	}

	// Verify the English translation was applied
	item := items[0]
	if item.Title.Name != "Test Anime English" {
		t.Errorf("expected translated name 'Test Anime English', got %q", item.Title.Name)
	}
	if item.Title.Overview != "This is the English overview" {
		t.Errorf("expected translated overview 'This is the English overview', got %q", item.Title.Overview)
	}
}

// TestGetCustomListMovieTranslations verifies that GetCustomList fetches translations for movies.
func TestGetCustomListMovieTranslations(t *testing.T) {
	var (
		mu                  sync.Mutex
		translationsFetched []string
	)

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			defer mu.Unlock()

			path := req.URL.Path

			// Handle TVDB login
			if path == "/v4/login" {
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			// Handle MDBList custom list fetch
			if strings.Contains(req.URL.Host, "mdblist.com") {
				items := []mdblistItem{
					{
						ID:          1,
						Rank:        1,
						Title:       "Test Movie",
						TVDBID:      ptr(int64(67890)),
						IMDBID:      "tt7654321",
						MediaType:   "movie",
						ReleaseYear: 2024,
					},
				}
				body, _ := json.Marshal(items)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBuffer(body)), Header: make(http.Header)}, nil
			}

			// Handle TVDB movie extended (includes name/overview/artwork) — now the primary call
			if strings.HasPrefix(path, "/v4/movies/67890/extended") {
				body := bytes.NewBufferString(`{"data":{"id":67890,"name":"テスト映画","overview":"これは日本語の映画概要です","artworks":[]}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			// Handle TVDB movie translations - return English translation
			if strings.HasPrefix(path, "/v4/movies/67890/translations/") {
				lang := strings.TrimPrefix(path, "/v4/movies/67890/translations/")
				translationsFetched = append(translationsFetched, lang)
				body := bytes.NewBufferString(`{"data":{"language":"eng","name":"Test Movie English","overview":"This is the English movie overview"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			t.Logf("Unhandled request: %s %s", req.Method, req.URL.String())
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	// Create a service with the mock HTTP client
	tempDir := t.TempDir()
	service := &Service{
		client:  newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:   newFileCache(tempDir, 24),
		idCache: newFileCache(tempDir, 24*7),
	}
	service.client.minInterval = 0

	// Call GetCustomList
	items, filteredTotal, _, err := service.GetCustomList(context.Background(), "https://mdblist.com/lists/test/movies/json", CustomListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("GetCustomList failed: %v", err)
	}

	if filteredTotal != 1 {
		t.Fatalf("expected filteredTotal=1, got %d", filteredTotal)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	// Verify translations were fetched
	mu.Lock()
	defer mu.Unlock()
	if len(translationsFetched) == 0 {
		t.Fatal("expected translations to be fetched, but none were")
	}

	foundEng := false
	for _, lang := range translationsFetched {
		if lang == "eng" {
			foundEng = true
			break
		}
	}
	if !foundEng {
		t.Fatalf("expected 'eng' translation to be fetched, got: %v", translationsFetched)
	}

	// Verify the English translation was applied
	item := items[0]
	if item.Title.Name != "Test Movie English" {
		t.Errorf("expected translated name 'Test Movie English', got %q", item.Title.Name)
	}
	if item.Title.Overview != "This is the English movie overview" {
		t.Errorf("expected translated overview 'This is the English movie overview', got %q", item.Title.Overview)
	}
}

// TestGetCustomListNoTranslationWhenUnavailable verifies that when translation is not available,
// the original content is preserved.
func TestGetCustomListNoTranslationWhenUnavailable(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			path := req.URL.Path

			// Handle TVDB login
			if path == "/v4/login" {
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			// Handle MDBList custom list fetch
			if strings.Contains(req.URL.Host, "mdblist.com") {
				items := []mdblistItem{
					{
						ID:          1,
						Rank:        1,
						Title:       "Obscure Anime",
						TVDBID:      ptr(int64(99999)),
						IMDBID:      "tt9999999",
						MediaType:   "show",
						ReleaseYear: 2024,
					},
				}
				body, _ := json.Marshal(items)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBuffer(body)), Header: make(http.Header)}, nil
			}

			// Handle TVDB series extended (includes name/overview/artwork/status) — now the primary call
			if strings.HasPrefix(path, "/v4/series/99999/extended") {
				body := bytes.NewBufferString(`{"data":{"id":99999,"name":"珍しいアニメ","overview":"日本語のみの概要","artworks":[]}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			// Handle TVDB series translations - return 404 (no translation available)
			if strings.HasPrefix(path, "/v4/series/99999/translations/") {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{"status":"failure"}`)), Header: make(http.Header)}, nil
			}

			t.Logf("Unhandled request: %s %s", req.Method, req.URL.String())
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	// Create a service with the mock HTTP client
	service := &Service{
		client: newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:  newFileCache(t.TempDir(), 24),
	}
	service.client.minInterval = 0

	// Call GetCustomList
	items, _, _, err := service.GetCustomList(context.Background(), "https://mdblist.com/lists/test/obscure/json", CustomListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("GetCustomList failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	// Verify original content is preserved when no translation available
	item := items[0]
	if item.Title.Overview != "日本語のみの概要" {
		t.Errorf("expected original overview preserved, got %q", item.Title.Overview)
	}
}

// ptr returns a pointer to the given value (helper for tests)
func ptr[T any](v T) *T {
	return &v
}

// TestProgressLifecycle verifies the start → increment → snapshot → cleanup cycle.
func TestProgressLifecycle(t *testing.T) {
	svc := &Service{progressTasks: make(map[string]*ProgressTask)}

	// Initially empty
	snap := svc.GetProgressSnapshot()
	if snap.ActiveCount != 0 {
		t.Fatalf("expected 0 active tasks, got %d", snap.ActiveCount)
	}

	// Start a task
	cleanup := svc.startProgressTask("test-task", "Test Task", "fetching", 0)

	snap = svc.GetProgressSnapshot()
	if snap.ActiveCount != 1 {
		t.Fatalf("expected 1 active task, got %d", snap.ActiveCount)
	}
	if snap.Tasks[0].ID != "test-task" || snap.Tasks[0].Phase != "fetching" {
		t.Fatalf("unexpected task: %+v", snap.Tasks[0])
	}

	// Update phase
	svc.updateProgressPhase("test-task", "enriching", 50)
	snap = svc.GetProgressSnapshot()
	if snap.Tasks[0].Phase != "enriching" || snap.Tasks[0].Total != 50 || snap.Tasks[0].Current != 0 {
		t.Fatalf("unexpected after phase update: %+v", snap.Tasks[0])
	}

	// Increment several times
	for i := 0; i < 10; i++ {
		svc.incrementProgress("test-task")
	}
	snap = svc.GetProgressSnapshot()
	if snap.Tasks[0].Current != 10 {
		t.Fatalf("expected current=10, got %d", snap.Tasks[0].Current)
	}

	// Cleanup removes the task
	cleanup()
	snap = svc.GetProgressSnapshot()
	if snap.ActiveCount != 0 {
		t.Fatalf("expected 0 active tasks after cleanup, got %d", snap.ActiveCount)
	}
}

// TestProgressIncrementNoTask verifies incrementProgress is safe when task doesn't exist.
func TestProgressIncrementNoTask(t *testing.T) {
	svc := &Service{progressTasks: make(map[string]*ProgressTask)}
	// Should not panic
	svc.incrementProgress("nonexistent")
	svc.updateProgressPhase("nonexistent", "test", 10)
}

// TestExtractTitleFields verifies that extractTitleFields copies only requested fields.
func TestExtractTitleFields(t *testing.T) {
	full := &models.Title{
		ID:            "tvdb:series:123",
		Name:          "Test Show",
		Overview:      "A great show",
		Year:          2020,
		Language:      "eng",
		MediaType:     "series",
		TVDBID:        123,
		IMDBID:        "tt0000123",
		TMDBID:        456,
		Genres:        []string{"Drama", "Action"},
		Status:        "Continuing",
		Network:       "HBO",
		Certification: "TV-MA",
		Popularity:    85.5,
		Ratings:       []models.Rating{{Source: "imdb", Value: 8.5, Max: 10}},
	}

	tests := []struct {
		name   string
		fields []string
		check  func(t *testing.T, out models.Title)
	}{
		{
			name:   "overview only",
			fields: []string{"overview"},
			check: func(t *testing.T, out models.Title) {
				if out.Overview != "A great show" {
					t.Errorf("expected overview, got %q", out.Overview)
				}
				if out.Year != 0 {
					t.Errorf("year should be 0, got %d", out.Year)
				}
				if len(out.Genres) != 0 {
					t.Errorf("genres should be empty, got %v", out.Genres)
				}
			},
		},
		{
			name:   "year and genres",
			fields: []string{"year", "genres"},
			check: func(t *testing.T, out models.Title) {
				if out.Year != 2020 {
					t.Errorf("expected year 2020, got %d", out.Year)
				}
				if len(out.Genres) != 2 {
					t.Errorf("expected 2 genres, got %d", len(out.Genres))
				}
				if out.Overview != "" {
					t.Errorf("overview should be empty, got %q", out.Overview)
				}
			},
		},
		{
			name:   "all fields",
			fields: []string{"overview", "year", "genres", "status", "network", "certification", "language", "popularity", "ratings"},
			check: func(t *testing.T, out models.Title) {
				if out.Overview != "A great show" {
					t.Errorf("expected overview, got %q", out.Overview)
				}
				if out.Year != 2020 {
					t.Errorf("expected year 2020, got %d", out.Year)
				}
				if out.Status != "Continuing" {
					t.Errorf("expected status Continuing, got %q", out.Status)
				}
				if len(out.Ratings) != 1 {
					t.Errorf("expected 1 rating, got %d", len(out.Ratings))
				}
			},
		},
		{
			name:   "empty fields",
			fields: []string{},
			check: func(t *testing.T, out models.Title) {
				// Should only have IDs
				if out.Overview != "" || out.Year != 0 || len(out.Genres) != 0 {
					t.Error("expected only IDs with empty fields")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := extractTitleFields(full, tt.fields)
			// IDs should always be present
			if out.ID != full.ID {
				t.Errorf("ID mismatch: %q vs %q", out.ID, full.ID)
			}
			if out.Name != full.Name {
				t.Errorf("Name mismatch: %q vs %q", out.Name, full.Name)
			}
			if out.TVDBID != full.TVDBID {
				t.Errorf("TVDBID mismatch: %d vs %d", out.TVDBID, full.TVDBID)
			}
			if out.IMDBID != full.IMDBID {
				t.Errorf("IMDBID mismatch: %q vs %q", out.IMDBID, full.IMDBID)
			}
			if out.TMDBID != full.TMDBID {
				t.Errorf("TMDBID mismatch: %d vs %d", out.TMDBID, full.TMDBID)
			}
			tt.check(t, out)
		})
	}
}

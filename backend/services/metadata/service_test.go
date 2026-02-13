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

package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestSeriesDetailsUpgradesCachedLiteSeasonNames(t *testing.T) {
	var (
		mu                   sync.Mutex
		seasonTranslationHit int
	)

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			defer mu.Unlock()

			path := req.URL.Path
			query := req.URL.Query()

			if path == "/v4/login" {
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			if path == "/v4/series/12345" {
				body := bytes.NewBufferString(`{"data":{"id":12345,"name":"Test Series","overview":"Series overview","year":"2024"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			if path == "/v4/series/12345/extended" {
				if query.Get("meta") == "artworks" {
					body := bytes.NewBufferString(`{"data":{"id":12345,"artworks":[]}}`)
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
				}
				if query.Get("meta") != "episodes,seasons,artworks" {
					t.Fatalf("unexpected meta query: %q", query.Get("meta"))
				}
				body := bytes.NewBufferString(`{"data":{
					"id":12345,
					"name":"Test Series",
					"overview":"Series overview",
					"year":"2024",
					"remoteIds":[],
					"artworks":[],
					"seasons":[
						{"id":777,"number":1,"type":{"type":"official","name":"Official"}}
					],
					"episodes":[
						{"id":9001,"name":"Pilot","overview":"Episode overview","seasonNumber":1,"number":1,"aired":"2024-01-01","runtime":24}
					]
				}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			if path == "/v4/series/12345/translations/eng" {
				body := bytes.NewBufferString(`{"data":{"name":"Test Series","overview":"Series overview"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			if path == "/v4/series/12345/episodes/official/eng" {
				if query.Get("page") != "0" {
					t.Fatalf("unexpected page query: %q", query.Get("page"))
				}
				body := bytes.NewBufferString(`{"data":{"episodes":[
					{"id":9001,"name":"Pilot","overview":"Episode overview","seasonNumber":1,"number":1,"aired":"2024-01-01","runtime":24}
				]},"links":{"next":null}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			if path == "/v4/seasons/777/translations/eng" {
				seasonTranslationHit++
				body := bytes.NewBufferString(`{"data":{"name":"East Blue","overview":"Saga overview"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}

			t.Fatalf("unhandled request: %s %s", req.Method, (&url.URL{Path: path, RawQuery: req.URL.RawQuery}).String())
			return nil, nil
		}),
	}

	tempDir := t.TempDir()
	service := &Service{
		client:  newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:   newFileCache(tempDir, 24),
		idCache: newFileCache(tempDir, 24*7),
	}
	service.client.minInterval = 0

	query := models.SeriesDetailsQuery{
		TVDBID: 12345,
		Name:   "Test Series",
		Year:   2024,
	}

	lite, err := service.SeriesDetailsLite(context.Background(), query)
	if err != nil {
		t.Fatalf("SeriesDetailsLite failed: %v", err)
	}
	if len(lite.Seasons) != 1 {
		t.Fatalf("expected 1 lite season, got %d", len(lite.Seasons))
	}
	if lite.Seasons[0].Name != "Season 1" {
		t.Fatalf("expected lite season name to be generic, got %q", lite.Seasons[0].Name)
	}

	full, err := service.SeriesDetails(context.Background(), query)
	if err != nil {
		t.Fatalf("SeriesDetails failed: %v", err)
	}
	if len(full.Seasons) != 1 {
		t.Fatalf("expected 1 full season, got %d", len(full.Seasons))
	}
	if full.Seasons[0].Name != "East Blue" {
		t.Fatalf("expected upgraded season name %q, got %q", "East Blue", full.Seasons[0].Name)
	}
	if full.Seasons[0].Overview != "Saga overview" {
		t.Fatalf("expected upgraded season overview %q, got %q", "Saga overview", full.Seasons[0].Overview)
	}
	if seasonTranslationHit == 0 {
		t.Fatal("expected full SeriesDetails to fetch season translations for cached lite data")
	}
}

func TestMergeSearchResultsPrefersTVDBWhenTMDBIDMatches(t *testing.T) {
	results := mergeSearchResults([]models.SearchResult{
		{
			Title: models.Title{
				ID:        "tmdb:tv:123",
				Name:      "Test Show",
				MediaType: "series",
				TMDBID:    123,
				Poster:    &models.Image{URL: "https://image.tmdb.org/t/p/w780/poster.jpg", Type: "poster"},
			},
			Score: 80,
		},
		{
			Title: models.Title{
				ID:        "tvdb:series:456",
				Name:      "Test Show",
				MediaType: "series",
				TVDBID:    456,
				TMDBID:    123,
			},
			Score: 40,
		},
	})

	if len(results) != 1 {
		t.Fatalf("expected one merged result, got %d", len(results))
	}
	title := results[0].Title
	if title.ID != "tvdb:series:456" {
		t.Fatalf("expected TVDB result to win, got %q", title.ID)
	}
	if title.TMDBID != 123 || title.TVDBID != 456 {
		t.Fatalf("expected merged TMDB/TVDB IDs, got tmdb=%d tvdb=%d", title.TMDBID, title.TVDBID)
	}
	if title.Poster == nil {
		t.Fatal("expected poster to be preserved from TMDB result")
	}
}

func TestSearchWithoutMediaTypeIncludesMoviesAndSeries(t *testing.T) {
	var searchedTypes []string
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/v4/login":
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			case "/v4/search":
				mediaType := req.URL.Query().Get("type")
				searchedTypes = append(searchedTypes, mediaType)
				body := `{"data":[{"type":"series","tvdb_id":"202","name":"Heat Vision","year":"1999","score":90}]}`
				if mediaType == "movie" {
					body = `{"data":[{"type":"movie","tvdb_id":"101","name":"Heat","year":"1995","score":100}]}`
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		}),
	}
	svc := &Service{
		client: newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:  newFileCache(t.TempDir(), 24),
	}
	svc.client.minInterval = 0

	results, err := svc.Search(context.Background(), "heat", "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if strings.Join(searchedTypes, ",") != "movie,series" {
		t.Fatalf("searched types = %v, want [movie series]", searchedTypes)
	}
	if len(results) != 2 {
		t.Fatalf("expected movie and series results, got %d: %+v", len(results), results)
	}
	seen := map[string]bool{}
	for _, result := range results {
		seen[result.Title.MediaType] = true
	}
	if !seen["movie"] || !seen["series"] {
		t.Fatalf("expected both movie and series results, got %+v", results)
	}
}

func TestPreferTMDBEpisodeImagesOverridesTVDBStills(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/3/tv/42/season/1" {
				body := bytes.NewBufferString(`{"id":1001,"name":"Season 1","season_number":1,"episodes":[
					{"id":5001,"name":"Pilot","season_number":1,"episode_number":1,"still_path":"/tmdb-pilot.jpg"},
					{"id":5002,"name":"Second","season_number":1,"episode_number":2}
				]}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			}
			t.Fatalf("unhandled request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}),
	}

	service := &Service{
		tmdb: newTMDBClient("tmdb-key", "eng", httpc, newFileCache(t.TempDir(), 24)),
	}
	service.tmdb.minInterval = 0

	details := models.SeriesDetails{
		Title: models.Title{TMDBID: 42, MediaType: "series"},
		Seasons: []models.SeriesSeason{
			{
				Number:       1,
				Name:         "Season 1",
				EpisodeCount: 2,
				Episodes: []models.SeriesEpisode{
					{
						ID:            "tvdb:episode:1",
						SeasonNumber:  1,
						EpisodeNumber: 1,
						Image:         &models.Image{URL: "https://artworks.thetvdb.com/banners/tvdb-pilot.jpg", Type: "still"},
					},
					{
						ID:            "tvdb:episode:2",
						SeasonNumber:  1,
						EpisodeNumber: 2,
						Image:         &models.Image{URL: "https://artworks.thetvdb.com/banners/tvdb-second.jpg", Type: "still"},
					},
				},
			},
		},
	}

	if !service.preferTMDBEpisodeImages(context.Background(), &details, 42) {
		t.Fatal("expected TMDB episode image enrichment to change the details")
	}

	gotPilotImage := details.Seasons[0].Episodes[0].Image
	if gotPilotImage == nil {
		t.Fatal("expected first episode image")
	}
	wantPilotURL := "https://image.tmdb.org/t/p/original/tmdb-pilot.jpg"
	if gotPilotImage.URL != wantPilotURL {
		t.Fatalf("expected TMDB pilot image %q, got %q", wantPilotURL, gotPilotImage.URL)
	}
	if gotPilotImage.Type != "still" {
		t.Fatalf("expected TMDB pilot image type still, got %q", gotPilotImage.Type)
	}

	gotSecondImage := details.Seasons[0].Episodes[1].Image
	if gotSecondImage == nil || gotSecondImage.URL != "https://artworks.thetvdb.com/banners/tvdb-second.jpg" {
		t.Fatalf("expected second episode to keep TVDB image when TMDB still is missing, got %#v", gotSecondImage)
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

func TestAggregateTopTen_Deduplication(t *testing.T) {
	// Two sources both contain the same movie (by IMDB ID).
	// The item should appear once with a higher score than items unique to one source.
	movieA := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "Inception", MediaType: "movie", IMDBID: "tt1375666",
	}}
	movieB := models.TrendingItem{Rank: 2, Title: models.Title{
		Name: "The Dark Knight", MediaType: "movie", IMDBID: "tt0468569",
	}}
	movieC := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "Inception", MediaType: "movie", IMDBID: "tt1375666",
	}}

	sources := []topTenSource{
		{name: "trending-movies", weight: 3.0, items: []models.TrendingItem{movieA, movieB}},
		{name: "genre-action", weight: 1.0, items: []models.TrendingItem{movieC}},
	}

	results := aggregateTopTen(sources, 10)

	if len(results) != 2 {
		t.Fatalf("expected 2 unique items, got %d", len(results))
	}
	// Inception should rank first: appears on 2 lists with cross-list bonus.
	if results[0].Title.IMDBID != "tt1375666" {
		t.Errorf("expected Inception (tt1375666) first, got %s", results[0].Title.IMDBID)
	}
	// Re-ranked starting from 1
	if results[0].Rank != 1 {
		t.Errorf("expected rank 1, got %d", results[0].Rank)
	}
}

func TestAggregateTopTen_Limit(t *testing.T) {
	items := make([]models.TrendingItem, 20)
	for i := range items {
		items[i] = models.TrendingItem{
			Rank: i + 1,
			Title: models.Title{
				Name:      fmt.Sprintf("Movie %d", i+1),
				MediaType: "movie",
				IMDBID:    fmt.Sprintf("tt%07d", i+1),
			},
		}
	}

	sources := []topTenSource{
		{name: "trending", weight: 3.0, items: items},
	}

	results := aggregateTopTen(sources, 10)
	if len(results) != 10 {
		t.Errorf("expected 10 results (limit enforced), got %d", len(results))
	}
}

func TestAggregateTopTen_RecencyBonus(t *testing.T) {
	currentYear := time.Now().Year()
	// Same rank on the same list — newer item should outscore older item.
	newItem := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "New Movie", MediaType: "movie", IMDBID: "tt1111111", Year: currentYear,
	}}
	oldItem := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "Old Movie", MediaType: "movie", IMDBID: "tt2222222", Year: currentYear - 10,
	}}

	sources := []topTenSource{
		{name: "list-a", weight: 1.0, items: []models.TrendingItem{newItem}},
		{name: "list-b", weight: 1.0, items: []models.TrendingItem{oldItem}},
	}

	results := aggregateTopTen(sources, 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d", len(results))
	}
	if results[0].Title.IMDBID != "tt1111111" {
		t.Errorf("expected new item first (recency boost), got %s", results[0].Title.IMDBID)
	}
}

func TestAggregateTopTen_CrossListBonus(t *testing.T) {
	// Item on 2 lists should outscore an item with a better rank on only 1 list.
	popular := models.TrendingItem{Rank: 5, Title: models.Title{
		Name: "Popular", MediaType: "movie", IMDBID: "tt0000001",
	}}
	topRanked := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "Top Ranked", MediaType: "movie", IMDBID: "tt0000002",
	}}

	sources := []topTenSource{
		{name: "list-a", weight: 1.0, items: []models.TrendingItem{topRanked, popular}},
		{name: "list-b", weight: 1.0, items: []models.TrendingItem{popular}},
	}

	results := aggregateTopTen(sources, 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d", len(results))
	}

	// "popular" base score: list-a rank 2 = 1.0*(100/2) = 50, list-b rank 1 = 1.0*100 = 100
	// total = 150, bonus = 1 + 0.3*1 = 1.3 => 195
	// "topRanked" base score: list-a rank 1 = 100, bonus = 1.0 => 100
	if results[0].Title.IMDBID != "tt0000001" {
		t.Errorf("expected tt0000001 (cross-list item) first, got %s", results[0].Title.IMDBID)
	}
}

func TestTopTenTVEpisodeRecencyMultiplier_RecentEpisodeWins(t *testing.T) {
	now := time.Now()
	details := models.SeriesDetails{
		Title: models.Title{
			Name:      "Weekly Hit",
			MediaType: "series",
			Status:    "Continuing",
		},
		Seasons: []models.SeriesSeason{
			{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{SeasonNumber: 1, EpisodeNumber: 8, AiredDateTimeUTC: now.Add(-48 * time.Hour).UTC().Format(time.RFC3339)},
					{SeasonNumber: 1, EpisodeNumber: 9, AiredDateTimeUTC: now.Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)},
				},
			},
		},
	}

	multiplier := topTenTVEpisodeRecencyMultiplier(details, now)
	if multiplier <= 1.5 {
		t.Fatalf("expected strong recency multiplier for fresh weekly episode, got %.2f", multiplier)
	}
}

func TestTopTenTVEpisodeRecencyMultiplier_StaleShowBarelyMoves(t *testing.T) {
	now := time.Now()
	details := models.SeriesDetails{
		Title: models.Title{
			Name:      "Archive Show",
			MediaType: "series",
			Status:    "Ended",
		},
		Seasons: []models.SeriesSeason{
			{
				Number: 1,
				Episodes: []models.SeriesEpisode{
					{SeasonNumber: 1, EpisodeNumber: 1, AiredDate: now.Add(-120 * 24 * time.Hour).Format("2006-01-02")},
				},
			},
		},
	}

	multiplier := topTenTVEpisodeRecencyMultiplier(details, now)
	if multiplier != 1.0 {
		t.Fatalf("expected no recency multiplier for stale show, got %.2f", multiplier)
	}
}

func TestTopTenMovieReleaseMultiplier_RecentStreamingRelease(t *testing.T) {
	now := time.Now()
	title := models.Title{
		Name:      "Fresh Streamer",
		MediaType: "movie",
		HomeRelease: &models.Release{
			Type: "digital",
			Date: now.Add(-10 * 24 * time.Hour).Format("2006-01-02"),
		},
	}

	multiplier := topTenMovieReleaseMultiplier(title, now)
	if multiplier <= 1.2 {
		t.Fatalf("expected recent movie release boost, got %.2f", multiplier)
	}
}

func TestAggregateTopTen_SourceDiversitySuppressesOldOneListTV(t *testing.T) {
	currentYear := time.Now().Year()
	oldCatalogTV := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "Long Running Catalog Hit", MediaType: "series", IMDBID: "tt3333333", Year: currentYear - 12,
	}}
	freshCrossListTV := models.TrendingItem{Rank: 4, Title: models.Title{
		Name: "Fresh Cross List Show", MediaType: "series", IMDBID: "tt4444444", Year: currentYear,
	}}

	sources := []topTenSource{
		{name: "provider-a", weight: 5.0, items: []models.TrendingItem{oldCatalogTV, freshCrossListTV}},
		{name: "provider-b", weight: 5.0, items: []models.TrendingItem{freshCrossListTV}},
		{name: "provider-c", weight: 5.0, items: []models.TrendingItem{freshCrossListTV}},
	}

	results := aggregateTopTen(sources, 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 items, got %d", len(results))
	}
	if results[0].Title.IMDBID != "tt4444444" {
		t.Fatalf("expected cross-list show to beat old one-list catalog title, got %s", results[0].Title.IMDBID)
	}
}

func TestApplyTopTenTVProviderGate_PenalizesTrendingOnlyCatalogTV(t *testing.T) {
	items := []models.TrendingItem{
		{Rank: 1, Title: models.Title{Name: "Catalog TV", MediaType: "series", IMDBID: "tt1000001", Year: 2005, Popularity: 1000}},
		{Rank: 2, Title: models.Title{Name: "Provider TV", MediaType: "series", IMDBID: "tt1000002", Year: 2025, Popularity: 900}},
	}
	debugEntries := map[string]topTenDebugEntry{
		"imdb:tt1000001": {
			Name:              "Catalog TV",
			MediaType:         "series",
			Year:              2005,
			ListCount:         3,
			ProviderListCount: 2,
			SourceNames:       []string{"disney-shows", "hulu-shows", "trending-tv"},
		},
		"imdb:tt1000002": {
			Name:              "Provider TV",
			MediaType:         "series",
			Year:              2025,
			ListCount:         2,
			ProviderListCount: 1,
			SourceNames:       []string{"hbo-shows", "trending-tv"},
		},
	}

	applyTopTenTVProviderGate(items, debugEntries)
	if items[0].Title.IMDBID != "tt1000002" {
		t.Fatalf("expected provider-backed current show to outrank old catalog title after gate, got %s", items[0].Title.IMDBID)
	}
}

func TestApplyTopTenCrossMediaBalance_BoostsTVInOverallList(t *testing.T) {
	items := []models.TrendingItem{
		{Rank: 1, Title: models.Title{Name: "Movie Leader", MediaType: "movie", IMDBID: "tt2000001", Popularity: 1000}},
		{Rank: 2, Title: models.Title{Name: "TV Challenger", MediaType: "series", IMDBID: "tt2000002", Popularity: 900}},
	}
	debugEntries := map[string]topTenDebugEntry{
		"imdb:tt2000001": {Name: "Movie Leader", MediaType: "movie", FinalScore: 1000},
		"imdb:tt2000002": {Name: "TV Challenger", MediaType: "series", FinalScore: 900},
	}

	applyTopTenCrossMediaBalance(items, debugEntries)
	if items[0].Title.IMDBID != "tt2000002" {
		t.Fatalf("expected tv title to overtake movie after overall balance boost, got %s", items[0].Title.IMDBID)
	}
}

func TestBatchMovieReleasesUsesV2ReleaseCache(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		cache: newFileCache(tempDir, 24),
	}

	tmdbID := int64(12345)
	cacheID := cacheKey("tmdb", "movie", "releases", "v2", "12345")
	cached := cachedReleasesWithCert{
		Releases: []models.Release{
			{Type: "theatrical", Date: "2026-01-10", Country: "US", Released: true},
			{Type: "digital", Date: "2026-02-01", Country: "US", Released: true},
		},
		Certification: "PG-13",
	}
	if err := svc.cache.set(cacheID, cached); err != nil {
		t.Fatalf("set cache: %v", err)
	}

	results := svc.BatchMovieReleases(context.Background(), []models.BatchMovieReleasesQuery{
		{TMDBID: tmdbID},
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error != "" {
		t.Fatalf("unexpected error: %s", results[0].Error)
	}
	if results[0].Theatrical == nil || results[0].Theatrical.Date != "2026-01-10" {
		t.Fatalf("expected theatrical release from cache, got %#v", results[0].Theatrical)
	}
	if results[0].HomeRelease == nil || results[0].HomeRelease.Date != "2026-02-01" {
		t.Fatalf("expected home release from cache, got %#v", results[0].HomeRelease)
	}
}

func TestClearCacheClearsAllMetadataCaches(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		cache:        newFileCache(tempDir, 24),
		idCache:      newFileCache(tempDir+"/ids", 24),
		ratingsCache: newFileCache(tempDir+"/ratings", 24),
	}

	if err := svc.cache.set("metadata-key", map[string]string{"ok": "1"}); err != nil {
		t.Fatalf("set metadata cache: %v", err)
	}
	if err := svc.idCache.set("id-key", "tt1234567"); err != nil {
		t.Fatalf("set id cache: %v", err)
	}
	if err := svc.ratingsCache.set("ratings-key", []models.Rating{{Source: "imdb", Value: 7.5, Max: 10}}); err != nil {
		t.Fatalf("set ratings cache: %v", err)
	}

	if err := svc.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}

	var metadataValue map[string]string
	if ok, _ := svc.cache.get("metadata-key", &metadataValue); ok {
		t.Fatal("expected metadata cache entry to be cleared")
	}
	var idValue string
	if ok, _ := svc.idCache.get("id-key", &idValue); ok {
		t.Fatal("expected id cache entry to be cleared")
	}
	var ratingsValue []models.Rating
	if ok, _ := svc.ratingsCache.get("ratings-key", &ratingsValue); ok {
		t.Fatal("expected ratings cache entry to be cleared")
	}
}

func TestGetCacheManagerStatusCountsV5CustomListCache(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		cache:  newFileCache(tempDir, 24),
		client: &tvdbClient{language: "eng"},
	}
	svc.customListInfoFn = func() []CustomListInfo {
		return []CustomListInfo{{URL: "https://mdblist.com/lists/test/list/json", Name: "Test List"}}
	}

	cacheID := cacheKey("mdblist", "custom", "v5", "https://mdblist.com/lists/test/list/json", "eng")
	if err := svc.cache.set(cacheID, []models.TrendingItem{{Rank: 1, Title: models.Title{Name: "Cached"}}}); err != nil {
		t.Fatalf("set custom list cache: %v", err)
	}

	status := svc.GetCacheManagerStatus()
	if status.CustomListsCached != 1 {
		t.Fatalf("expected 1 cached custom list, got %d", status.CustomListsCached)
	}
}

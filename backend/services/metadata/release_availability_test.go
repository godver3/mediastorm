package metadata

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

func TestFinalizeSearchReleaseStatusesHydratesCachedWindowsWithoutUpstream(t *testing.T) {
	var requests int32
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&requests, 1)
		return nil, http.ErrHandlerTimeout
	})}
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		cache: cache,
		tmdb:  newTMDBClient("test-key", "en-US", httpc, cache),
	}
	homeDate := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	theatricalDate := time.Now().AddDate(0, -2, 0).Format("2006-01-02")
	cacheID := cacheKey("tmdb", "movie", "releases", "v3", strconv.FormatInt(42, 10))
	if err := cache.set(cacheID, cachedReleasesWithCert{
		Releases: []models.Release{
			{Type: "theatrical", Date: theatricalDate},
			{Type: "digital", Date: homeDate},
		},
		Certification: "PG-13",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	results := []models.SearchResult{
		{Title: models.Title{ID: "tmdb:movie:42", MediaType: "movie", TMDBID: 42, Status: models.MovieReleaseStatusTheatrical}},
		{Title: models.Title{ID: "tmdb:movie:43", MediaType: "movie", TMDBID: 43, Status: models.MovieReleaseStatusUpcoming}},
	}
	svc.finalizeSearchReleaseStatuses(results)

	hit := results[0].Title
	if hit.HomeRelease == nil || hit.Theatrical == nil {
		t.Fatalf("expected cached windows hydrated, got %+v", hit)
	}
	if hit.Status != models.MovieReleaseStatusReleased {
		t.Fatalf("status = %q, want released from cached home window", hit.Status)
	}
	if hit.Certification != "PG-13" {
		t.Fatalf("certification = %q, want PG-13", hit.Certification)
	}
	hit.ApplyReleaseAvailability(time.Now())
	if hit.Availability != models.AvailabilityReleased || hit.ReleaseDate != theatricalDate {
		t.Fatalf("availability=%q releaseDate=%q", hit.Availability, hit.ReleaseDate)
	}
	if results[1].Title.HomeRelease != nil || results[1].Title.Status != models.MovieReleaseStatusUpcoming {
		t.Fatalf("uncached title should be untouched, got %+v", results[1].Title)
	}
	// Zero-value services (not built by NewService) never warm in the background.
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("expected no upstream requests, got %d", n)
	}
}

func TestWarmMovieReleasesAsyncBoundsAndDedupes(t *testing.T) {
	release := make(chan struct{})
	var requests, active, maxActive int32
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&requests, 1)
		now := atomic.AddInt32(&active, 1)
		for {
			prev := atomic.LoadInt32(&maxActive)
			if now <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, now) {
				break
			}
		}
		<-release
		atomic.AddInt32(&active, -1)
		return &http.Response{
			StatusCode: http.StatusOK,
			// Release dates arrive appended to the movie details request.
			Body:   io.NopCloser(bytes.NewBufferString(`{"results":[],"release_dates":{"results":[]}}`)),
			Header: make(http.Header),
		}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{cache: cache, tmdb: newTMDBClient("test-key", "en-US", httpc, cache)}
	svc.releaseWarm.enabled = true

	ids := make([]int64, 0, releaseWarmMaxPerCall+10)
	for i := 1; i <= releaseWarmMaxPerCall+10; i++ {
		ids = append(ids, int64(i))
	}
	svc.warmMovieReleasesAsync(ids)
	svc.warmMovieReleasesAsync(ids[:5]) // already in flight: no new work

	svc.releaseWarm.mu.Lock()
	inflight := len(svc.releaseWarm.inflight)
	svc.releaseWarm.mu.Unlock()
	if inflight != releaseWarmMaxPerCall {
		t.Fatalf("inflight = %d, want %d", inflight, releaseWarmMaxPerCall)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.releaseWarm.mu.Lock()
		remaining := len(svc.releaseWarm.inflight)
		svc.releaseWarm.mu.Unlock()
		if remaining == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&requests); n != releaseWarmMaxPerCall {
		t.Fatalf("upstream requests = %d, want %d", n, releaseWarmMaxPerCall)
	}
	if n := atomic.LoadInt32(&maxActive); n > releaseWarmConcurrency {
		t.Fatalf("max concurrent upstream requests = %d, want <= %d", n, releaseWarmConcurrency)
	}
}

func TestApplyTVDBSeriesReleaseDatePrefersEarliestRegularEpisode(t *testing.T) {
	title := &models.Title{MediaType: "series"}
	applyTVDBSeriesReleaseDate(title, tvdbSeriesExtendedData{
		FirstAired: "2027-05-01",
		Episodes: []tvdbEpisode{
			{SeasonNumber: 0, Aired: "2026-01-01"},
			{SeasonNumber: 1, Aired: "2027-03-02"},
			{SeasonNumber: 1, Aired: "2027-03-09"},
		},
	})
	if title.ReleaseDate != "2027-03-02" {
		t.Fatalf("ReleaseDate = %q, want 2027-03-02", title.ReleaseDate)
	}
	other := &models.Title{MediaType: "series"}
	applyTVDBSeriesReleaseDate(other, tvdbSeriesExtendedData{FirstAired: "2027-05-01"})
	if other.ReleaseDate != "2027-05-01" {
		t.Fatalf("ReleaseDate = %q, want firstAired fallback", other.ReleaseDate)
	}
}

func TestMergeSearchResultsKeepsReleaseDate(t *testing.T) {
	merged := mergeSearchResults([]models.SearchResult{
		{Title: models.Title{MediaType: "series", TVDBID: 7, TMDBID: 9, Name: "Show"}, Score: 10},
		{Title: models.Title{MediaType: "series", TMDBID: 9, Name: "Show", ReleaseDate: "2027-01-01"}, Score: 5},
	})
	if len(merged) != 1 || merged[0].Title.ReleaseDate != "2027-01-01" {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestGetCachedSeriesReleaseReadsCacheRelativeToNow(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{cache: cache, client: &tvdbClient{language: "eng"}}
	future := time.Now().AddDate(0, 3, 0).Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -3).Format("2006-01-02")

	seed := func(key string, v any) {
		t.Helper()
		if err := cache.set(key, v); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}
	seed(cacheKey("tvdb", "series", "info", "v1", "eng", "1"),
		models.Title{MediaType: "series", Status: models.SeriesReleaseStatusUnreleased, ReleaseDate: future})
	// A status cached before the premiere passed.
	seed(seriesDetailsCacheKey("eng", 2, ""), models.SeriesDetails{
		Title: models.Title{MediaType: "series", Status: models.SeriesReleaseStatusUnreleased},
		Seasons: []models.SeriesSeason{
			{Number: 0, Episodes: []models.SeriesEpisode{{AiredDate: "2001-01-01"}}},
			{Number: 1, Episodes: []models.SeriesEpisode{{AiredDate: past}}},
		},
	})
	seed(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "3"),
		models.SeriesDetails{Title: models.Title{MediaType: "series", Status: "Continuing"}})

	if status, date := svc.GetCachedSeriesRelease(0, 1); status != models.SeriesReleaseStatusUnreleased || date != future {
		t.Fatalf("info cache: %q %q", status, date)
	}
	if status, date := svc.GetCachedSeriesRelease(0, 2); status != models.SeriesReleaseStatusReleased || date != past {
		t.Fatalf("details cache: %q %q", status, date)
	}
	if status, date := svc.GetCachedSeriesRelease(3, 0); status != "" || date != "" {
		t.Fatalf("a provider lifecycle status is not a release status: %q %q", status, date)
	}
	if status, date := svc.GetCachedSeriesRelease(0, 99); status != "" || date != "" {
		t.Fatalf("uncached: %q %q", status, date)
	}
}

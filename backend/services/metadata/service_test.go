package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

func TestApplyTVDBMovieExtendedMetadataCopiesGenresWithoutExternalIDs(t *testing.T) {
	title := models.Title{}

	applyTVDBMovieExtendedMetadata(&title, tvdbMovieExtendedData{
		OriginalCountry: "usa",
		Genres: []tvdbGenre{
			{Name: "Drama"},
			{Name: "Thriller"},
		},
		RemoteIDs: []struct {
			ID         string `json:"id"`
			Type       int    `json:"type"`
			SourceName string `json:"sourceName"`
		}{
			{ID: "tt0050083", SourceName: "IMDB"},
			{ID: "389", SourceName: "TheMovieDB.com"},
		},
	})

	if title.IMDBID != "" {
		t.Fatalf("IMDBID = %q, want empty", title.IMDBID)
	}
	if title.TMDBID != 0 {
		t.Fatalf("TMDBID = %d, want 0", title.TMDBID)
	}
	if strings.Join(title.Genres, ",") != "Drama,Thriller" {
		t.Fatalf("Genres = %+v, want Drama/Thriller", title.Genres)
	}
	if title.CountryCode != "usa" {
		t.Fatalf("CountryCode = %q, want provider country usa", title.CountryCode)
	}
}

func TestSearchUsesTMDBWithoutCallingUnconfiguredTVDB(t *testing.T) {
	var tvdbCalls atomic.Int32
	tvdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		tvdbCalls.Add(1)
		t.Fatalf("unexpected TVDB request: %s", req.URL)
		return nil, nil
	})}
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/3/search/tv" {
			t.Fatalf("unexpected TMDB request: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"id":82728,"name":"Bluey","first_air_date":"2018-10-01"}]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client:           newTVDBClient("", "eng", tvdbHTTP, 24),
		tmdb:             newTMDBClient("tmdb-key", "eng", tmdbHTTP, cache),
		cache:            cache,
		inflightRequests: make(map[string]*inflightRequest),
	}

	results, err := svc.Search(t.Context(), "Bluey", "series")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title.ID != "tmdb:tv:82728" {
		t.Fatalf("results = %#v, want one TMDB result", results)
	}
	if got := tvdbCalls.Load(); got != 0 {
		t.Fatalf("TVDB calls = %d, want 0", got)
	}
}

func TestSeriesDetailsLiteUsesTMDBWhenTVDBIsUnconfigured(t *testing.T) {
	var tvdbCalls atomic.Int32
	tvdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		tvdbCalls.Add(1)
		t.Fatalf("unexpected TVDB request: %s", req.URL)
		return nil, nil
	})}
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/3/tv/82728":
			body = `{"id":82728,"name":"Bluey","original_name":"Bluey","first_air_date":"2018-10-01","external_ids":{"imdb_id":"tt7678620","tvdb_id":353546},"alternative_titles":{"results":[{"title":"Bluey (2018)"}]},"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-Y"}]},"seasons":[{"id":123,"name":"Season 1","season_number":1,"episode_count":1}]}`
		case "/3/tv/82728/season/1":
			body = `{"id":123,"name":"Season 1","season_number":1,"episodes":[{"id":456,"name":"Magic Xylophone","season_number":1,"episode_number":1,"air_date":"2018-10-01","runtime":7}]}`
		case "/3/tv/82728/aggregate_credits":
			body = `{"cast":[{"id":100,"name":"David McCormack","roles":[{"character":"Bandit Heeler","episode_count":1}],"order":0,"profile_path":"/bandit.jpg"}]}`
		default:
			t.Fatalf("unexpected TMDB request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	if err := cache.set(cacheKey("tmdb", "images", "v10", "eng", "series", "82728"), tmdbImagesResult{LogoSelectionVersion: 1}); err != nil {
		t.Fatalf("seed images cache: %v", err)
	}
	svc := &Service{
		client:           newTVDBClient("", "eng", tvdbHTTP, 24),
		tmdb:             newTMDBClient("tmdb-key", "eng", tmdbHTTP, cache),
		cache:            cache,
		idCache:          newFileCache(t.TempDir(), 168),
		inflightRequests: make(map[string]*inflightRequest),
	}

	details, err := svc.SeriesDetailsLite(t.Context(), models.SeriesDetailsQuery{TitleID: "tmdb:tv:82728", TMDBID: 82728})
	if err != nil {
		t.Fatalf("SeriesDetailsLite: %v", err)
	}
	if details.Title.ID != "tmdb:tv:82728" || details.Title.TVDBID != 353546 || details.ActiveOrdering != "official" {
		t.Fatalf("unexpected TMDB title: %+v ordering=%q", details.Title, details.ActiveOrdering)
	}
	if len(details.Seasons) != 1 || len(details.Seasons[0].Episodes) != 1 || details.Seasons[0].Episodes[0].TMDBID != 456 {
		t.Fatalf("unexpected TMDB episodes: %#v", details.Seasons)
	}
	if details.Seasons[0].Episodes[0].AbsoluteEpisodeNumber != 1 {
		t.Fatalf("absolute episode = %d, want 1", details.Seasons[0].Episodes[0].AbsoluteEpisodeNumber)
	}
	if len(details.Title.AlternateTitles) != 1 || details.Title.AlternateTitles[0] != "Bluey (2018)" {
		t.Fatalf("alternate titles = %#v", details.Title.AlternateTitles)
	}
	if details.Title.Credits == nil || len(details.Title.Credits.Cast) != 1 || details.Title.Credits.Cast[0].Name != "David McCormack" {
		t.Fatalf("credits = %#v, want TMDB cast", details.Title.Credits)
	}
	if details.Title.Certification != "TV-Y" {
		t.Fatalf("certification = %q, want TV-Y", details.Title.Certification)
	}
	if got := tvdbCalls.Load(); got != 0 {
		t.Fatalf("TVDB calls = %d, want 0", got)
	}
}

func TestCachedFetchImagesRefreshesOldEmptyLogoOnce(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	key := cacheKey("tmdb", "images", "v10", "en", "movie", "42")
	if err := cache.set(key, tmdbImagesResult{}); err != nil {
		t.Fatalf("seed old images cache: %v", err)
	}
	var requests atomic.Int32
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"logos":[],"posters":[],"backdrops":[]}`)), Header: make(http.Header)}, nil
	})}
	svc := &Service{client: newTVDBClient("", "en", tmdbHTTP, 24), tmdb: newTMDBClient("tmdb-key", "en", tmdbHTTP, cache), cache: cache}
	for range 2 {
		images, err := svc.cachedFetchImages(context.Background(), "movie", 42)
		if err != nil || images == nil || images.LogoSelectionVersion != 1 {
			t.Fatalf("cachedFetchImages = %#v, %v", images, err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("TMDB requests = %d, want 1", got)
	}
}

func TestNormalizedListTitleYearEqualAcceptsMatchingDisplayYearSuffix(t *testing.T) {
	if !normalizedListTitleYearEqual("Dark Matter (2024)", "Dark Matter", 2024) {
		t.Fatal("expected matching parenthesized year suffix to be ignored")
	}
	if !normalizedListTitleYearEqual("Dark Matter [2024]", "Dark Matter", 2024) {
		t.Fatal("expected matching bracketed year suffix to be ignored")
	}
	if normalizedListTitleYearEqual("Dark Matter (2015)", "Dark Matter", 2024) {
		t.Fatal("expected a different year suffix to remain significant")
	}
	if normalizedListTitleYearEqual("Dark Matter (2024)", "Dark Matters", 2024) {
		t.Fatal("expected distinct titles not to match")
	}
}

func TestBatchSeriesEndpointsAcceptTMDBOnlySeriesWithoutTVDBMapping(t *testing.T) {
	var tvdbCalls atomic.Int32
	tvdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		tvdbCalls.Add(1)
		t.Fatalf("unexpected TVDB request: %s", req.URL)
		return nil, nil
	})}
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/3/tv/230" {
			t.Fatalf("unexpected TMDB request: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"id":230,"name":"The New Scooby and Scrappy-Doo Show","first_air_date":"1983-09-10","external_ids":{"imdb_id":"tt0086767","tvdb_id":null}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	if err := cache.set(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "230"), models.SeriesDetails{
		Title:   models.Title{ID: "tmdb:tv:230", Name: "The New Scooby and Scrappy-Doo Show", MediaType: "series", TMDBID: 230, IMDBID: "tt0086767", Year: 1983},
		Seasons: []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{{TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "series", "info", "v1", "eng", "230"), models.Title{
		ID: "tmdb:tv:230", Name: "The New Scooby and Scrappy-Doo Show", MediaType: "series", TMDBID: 230, IMDBID: "tt0086767", Year: 1983,
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "credits", "v1", "series", "230"), models.Credits{}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "tv", "content_rating", "v1", "230"), ""); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		client:           newTVDBClient("", "eng", tvdbHTTP, 24),
		tmdb:             newTMDBClient("tmdb-key", "eng", tmdbHTTP, cache),
		cache:            cache,
		inflightRequests: make(map[string]*inflightRequest),
	}
	query := models.SeriesDetailsQuery{TitleID: "tmdb:tv:230", TMDBID: 230}

	full := svc.BatchSeriesDetails(t.Context(), []models.SeriesDetailsQuery{query})
	if len(full) != 1 || full[0].Error != "" || full[0].Details == nil || full[0].Details.Title.TMDBID != 230 {
		t.Fatalf("full batch result: %#v", full)
	}
	fields := svc.BatchSeriesTitleFields(t.Context(), []models.SeriesDetailsQuery{query}, []string{"name", "year"})
	if len(fields) != 1 || fields[0].Error != "" || fields[0].Details == nil || fields[0].Details.Title.Name == "" {
		t.Fatalf("fields batch result: %#v", fields)
	}
	if got := tvdbCalls.Load(); got != 0 {
		t.Fatalf("TVDB calls = %d, want 0", got)
	}
}

func TestSeriesDetailsLiteReverseResolvesLegacyTVDBIdentityThroughTMDB(t *testing.T) {
	var tvdbCalls atomic.Int32
	cache := newFileCache(t.TempDir(), 24)
	tmdbHTTP := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/3/find/79335" || req.URL.Query().Get("external_source") != "tvdb_id" {
			t.Fatalf("unexpected TMDB request: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"tv_results":[{"id":1396}]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	if err := cache.set(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "1396"), models.SeriesDetails{
		Title:          models.Title{ID: "tmdb:tv:1396", Name: "Breaking Bad", MediaType: "series", TMDBID: 1396, TVDBID: 79335, IMDBID: "tt0903747"},
		Seasons:        []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{{TMDBID: 62085, SeasonNumber: 1, EpisodeNumber: 1}}}},
		ActiveOrdering: "official",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "credits", "v1", "series", "1396"), models.Credits{}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "tv", "content_rating", "v1", "1396"), "TV-MA"); err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		client: newTVDBClient("", "eng", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			tvdbCalls.Add(1)
			t.Fatalf("unexpected TVDB request: %s", req.URL)
			return nil, nil
		})}, 24),
		tmdb:             newTMDBClient("tmdb-key", "eng", tmdbHTTP, cache),
		cache:            cache,
		inflightRequests: make(map[string]*inflightRequest),
	}
	details, err := svc.SeriesDetailsLite(t.Context(), models.SeriesDetailsQuery{TitleID: "tvdb:series:79335", TVDBID: 79335})
	if err != nil {
		t.Fatalf("SeriesDetailsLite: %v", err)
	}
	if details.Title.TMDBID != 1396 || details.Title.TVDBID != 79335 || len(details.Seasons) != 1 {
		t.Fatalf("unexpected reverse-resolved details: %#v", details)
	}
	if got := tvdbCalls.Load(); got != 0 {
		t.Fatalf("TVDB calls = %d, want 0", got)
	}
}

func TestGetCachedOverviewReadsCurrentTMDBCaches(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{client: &tvdbClient{language: "eng"}, cache: cache}
	if err := cache.set(cacheKey("tmdb", "movie", "details", "v4", "eng", "603"), models.Title{Overview: "Movie overview"}); err != nil {
		t.Fatal(err)
	}
	if err := cache.set(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "1396"), models.SeriesDetails{Title: models.Title{Overview: "Series overview"}}); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetCachedOverview("movie", 603, 0); got != "Movie overview" {
		t.Fatalf("movie overview = %q", got)
	}
	if got := svc.GetCachedOverview("series", 1396, 0); got != "Series overview" {
		t.Fatalf("series overview = %q", got)
	}
}

func TestEnrichTMDBEpisodeMetadataCachesMissingSeason(t *testing.T) {
	var calls atomic.Int32
	httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Body:       io.NopCloser(strings.NewReader(`{"status_code":34}`)),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	service := &Service{
		tmdb:  newTMDBClient("test-key", "en", httpc, cache),
		cache: cache,
	}
	details := models.SeriesDetails{Seasons: []models.SeriesSeason{{
		Number:   0,
		Name:     "Specials",
		Episodes: []models.SeriesEpisode{{SeasonNumber: 0, EpisodeNumber: 1}},
	}}}

	for i := 0; i < 2; i++ {
		changed, complete := service.enrichTMDBEpisodeMetadata(context.Background(), &details, 82782)
		if changed || !complete {
			t.Fatalf("enrichment pass %d = changed %v complete %v, want false/true", i+1, changed, complete)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("TMDB calls = %d, want one cached 404", got)
	}
}

func TestCachedTMDBSeasonSingleflightsConcurrentMiss(t *testing.T) {
	var calls atomic.Int32
	httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Body:       io.NopCloser(strings.NewReader(`{"status_code":34}`)),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	service := &Service{
		tmdb:  newTMDBClient("test-key", "en", httpc, cache),
		cache: cache,
	}

	const callers = 12
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			_, found, err := service.cachedTMDBSeason(context.Background(), 82782, tmdbSeasonSummary{Number: 0})
			if err != nil || found {
				t.Errorf("cachedTMDBSeason = found %v err %v, want false/nil", found, err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("TMDB calls = %d, want one singleflight request", got)
	}
}

func TestResolveSeriesTVDBIDNegativeCachesMissingTMDBSeries(t *testing.T) {
	var calls atomic.Int32
	httpc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Body:       io.NopCloser(strings.NewReader(`{"status_code":34}`)),
			Header:     make(http.Header),
		}, nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	service := &Service{
		tmdb:  newTMDBClient("test-key", "en", httpc, cache),
		cache: cache,
	}
	req := models.SeriesDetailsQuery{TMDBID: 327723}

	for i := 0; i < 2; i++ {
		if _, err := service.resolveSeriesTVDBIDActual(context.Background(), req); err == nil {
			t.Fatalf("resolution pass %d unexpectedly succeeded", i+1)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("TMDB calls = %d, want one request followed by negative-cache hit", got)
	}
}

func TestApplyTVDBSeriesIdentityPreservesAnimeSignals(t *testing.T) {
	title := models.Title{
		Name:     "Martian Successor Nadesico",
		Language: "eng",
		Genres:   []string{"Animation", "Comedy"},
	}

	applyTVDBSeriesIdentity(&title, tvdbSeriesExtendedData{
		Name:            "機動戦艦ナデシコ",
		OriginalCountry: "jpn",
		Genres: []tvdbGenre{
			{Name: "Science Fiction"},
			{Name: "Animation"},
			{Name: "Anime"},
		},
	})

	if title.OriginalName != "機動戦艦ナデシコ" {
		t.Fatalf("OriginalName = %q, want Japanese provider title", title.OriginalName)
	}
	if got := strings.Join(title.Genres, ","); got != "Animation,Comedy,Science Fiction,Anime" {
		t.Fatalf("Genres = %q, want merged TVDB identity genres", got)
	}
	if title.CountryCode != "jpn" {
		t.Fatalf("CountryCode = %q, want provider country jpn", title.CountryCode)
	}
}

func TestMergeMetadataGenresIsCaseInsensitiveAndStable(t *testing.T) {
	got := mergeMetadataGenres(
		[]string{"Animation", "Anime"},
		[]string{"animation", "Comedy", " anime "},
	)

	if joined := strings.Join(got, ","); joined != "Animation,Anime,Comedy" {
		t.Fatalf("merged genres = %q, want stable case-insensitive merge", joined)
	}
}

func TestGetCuratedListChecksRawCacheBeforeResolvingIDs(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	rt := &countingRoundTripper{body: `{"results":[]}`}
	svc := &Service{
		client:  &tvdbClient{language: "en"},
		tmdb:    newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache),
		cache:   cache,
		idCache: newFileCache(t.TempDir(), 24),
	}
	items := []CuratedItem{{Title: "Marnie", Year: 1964, MediaType: "movie"}}
	cached := []models.TrendingItem{{Title: models.Title{Name: "Marnie", Year: 1964, MediaType: "movie"}}}
	if err := cache.set(svc.curatedListCacheID(items), cached); err != nil {
		t.Fatalf("seed curated cache: %v", err)
	}

	got, err := svc.GetCuratedList(context.Background(), items, "Letterboxd List")
	if err != nil {
		t.Fatalf("GetCuratedList: %v", err)
	}
	if len(got) != 1 || got[0].Title.Name != "Marnie" {
		t.Fatalf("GetCuratedList returned %+v", got)
	}
	if calls := rt.callCount(); calls != 0 {
		t.Fatalf("TMDB calls = %d, want 0 on raw curated cache hit", calls)
	}
}

func TestResolveTMDBMovieByTitleYearCachesFailure(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	rt := &countingRoundTripper{status: http.StatusNotFound, body: `{}`}
	svc := &Service{
		tmdb:    newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache),
		cache:   cache,
		idCache: newFileCache(t.TempDir(), 24),
	}

	if got := svc.resolveTMDBMovieByTitleYear(context.Background(), "Missing Movie", 1999); got != 0 {
		t.Fatalf("first resolution = %d, want 0", got)
	}
	if got := svc.resolveTMDBMovieByTitleYear(context.Background(), "Missing Movie", 1999); got != 0 {
		t.Fatalf("cached resolution = %d, want 0", got)
	}
	if calls := rt.callCount(); calls != 1 {
		t.Fatalf("TMDB calls = %d, want 1 after cached failure", calls)
	}
}

func TestGetMovieDetailsFromTMDBHydratesLogoOnServiceCacheHit(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	rt := &countingRoundTripper{status: http.StatusInternalServerError, body: `{}`}
	svc := &Service{
		client: &tvdbClient{language: "en"},
		tmdb:   newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache),
		cache:  cache,
	}
	svc.mdblist = newMDBListClient("test-key", []string{"tomatoes", "audience"}, true, 24)
	svc.ratingsCache = newFileCache(t.TempDir(), 24)
	if err := svc.ratingsCache.set(ratingsDiskCacheKey("tt34564059", "movie"), []models.Rating{
		{Source: "imdb", Value: 6.2, Max: 10},
		{Source: "tomatoes", Value: 80, Max: 100},
		{Source: "audience", Value: 75, Max: 100},
	}); err != nil {
		t.Fatal(err)
	}
	const tmdbID int64 = 4512
	detailsCacheID := cacheKey("tmdb", "movie", "details", "v4", "en", strconv.FormatInt(tmdbID, 10))
	if err := cache.set(detailsCacheID, models.Title{
		ID:        "tmdb:movie:4512",
		Name:      "Cached Movie",
		MediaType: "movie",
		TMDBID:    tmdbID,
		IMDBID:    "tt34564059",
	}); err != nil {
		t.Fatalf("seed details cache: %v", err)
	}
	logo := &models.Image{URL: "https://image.tmdb.org/t/p/w500/logo.png", Type: "logo"}
	imagesCacheID := cacheKey("tmdb", "images", "v10", "en", "movie", strconv.FormatInt(tmdbID, 10))
	if err := cache.set(imagesCacheID, tmdbImagesResult{Logo: logo}); err != nil {
		t.Fatalf("seed images cache: %v", err)
	}

	got, err := svc.getMovieDetailsFromTMDB(context.Background(), models.MovieDetailsQuery{TMDBID: tmdbID})
	if err != nil {
		t.Fatalf("getMovieDetailsFromTMDB: %v", err)
	}
	if len(got.Ratings) != 2 || got.Ratings[0].Source != "tomatoes" || got.Ratings[1].Source != "audience" {
		t.Fatalf("expected enabled RT ratings, got %#v", got.Ratings)
	}
	if got.Logo == nil || got.Logo.URL != logo.URL {
		t.Fatalf("logo = %#v, want %q", got.Logo, logo.URL)
	}
	if calls := rt.callCount(); calls != 0 {
		t.Fatalf("network calls = %d, want 0", calls)
	}
}

func TestMovieDetailsUsesTMDBWhenTVDBIsUnconfiguredDespiteStoredTVDBID(t *testing.T) {
	var tvdbCalls atomic.Int32
	cache := newFileCache(t.TempDir(), 24)
	const tmdbID int64 = 550
	if err := cache.set(
		cacheKey("tmdb", "movie", "details", "v4", "eng", strconv.FormatInt(tmdbID, 10)),
		models.Title{ID: "tmdb:movie:550", Name: "Fight Club", MediaType: "movie", TMDBID: tmdbID},
	); err != nil {
		t.Fatalf("seed details cache: %v", err)
	}
	if err := cache.set(
		cacheKey("tmdb", "images", "v10", "eng", "movie", strconv.FormatInt(tmdbID, 10)),
		tmdbImagesResult{},
	); err != nil {
		t.Fatalf("seed images cache: %v", err)
	}

	svc := &Service{
		client: newTVDBClient("", "en", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			tvdbCalls.Add(1)
			t.Fatalf("unexpected TVDB request: %s", req.URL)
			return nil, nil
		})}, 24),
		tmdb:  newTMDBClient("tmdb-key", "en", &http.Client{}, cache),
		cache: cache,
	}

	got, err := svc.MovieDetails(t.Context(), models.MovieDetailsQuery{TMDBID: tmdbID, TVDBID: 81189})
	if err != nil {
		t.Fatalf("MovieDetails: %v", err)
	}
	if got.TMDBID != tmdbID || got.Name != "Fight Club" {
		t.Fatalf("MovieDetails = %#v, want cached TMDB movie", got)
	}
	if calls := tvdbCalls.Load(); calls != 0 {
		t.Fatalf("TVDB calls = %d, want 0", calls)
	}
}

func TestGetMovieDetailsFromTMDBHydratesLogoOnFreshResponse(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	rt := &countingRoundTripper{
		body: `{"id":4512,"title":"Fresh Movie","imdb_id":"tt34564059","release_date":"2007-09-02","poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg"}`,
	}
	svc := &Service{
		client: &tvdbClient{language: "en"},
		tmdb:   newTMDBClient("test-key", "en", &http.Client{Transport: rt}, cache),
		cache:  cache,
	}
	svc.mdblist = newMDBListClient("test-key", []string{"tomatoes", "audience"}, true, 24)
	svc.ratingsCache = newFileCache(t.TempDir(), 24)
	if err := svc.ratingsCache.set(ratingsDiskCacheKey("tt34564059", "movie"), []models.Rating{
		{Source: "imdb", Value: 6.2, Max: 10},
		{Source: "tomatoes", Value: 80, Max: 100},
		{Source: "audience", Value: 75, Max: 100},
	}); err != nil {
		t.Fatal(err)
	}
	const tmdbID int64 = 4512
	if err := cache.set(
		cacheKey("tmdb", "movie", "releases", "v2", strconv.FormatInt(tmdbID, 10)),
		cachedReleasesWithCert{},
	); err != nil {
		t.Fatalf("seed releases cache: %v", err)
	}
	if err := cache.set(
		cacheKey("tmdb", "credits", "v1", "movie", strconv.FormatInt(tmdbID, 10)),
		models.Credits{},
	); err != nil {
		t.Fatalf("seed credits cache: %v", err)
	}
	logo := &models.Image{URL: "https://image.tmdb.org/t/p/w500/logo.png", Type: "logo"}
	if err := cache.set(
		cacheKey("tmdb", "images", "v10", "en", "movie", strconv.FormatInt(tmdbID, 10)),
		tmdbImagesResult{Logo: logo},
	); err != nil {
		t.Fatalf("seed images cache: %v", err)
	}

	got, err := svc.getMovieDetailsFromTMDB(context.Background(), models.MovieDetailsQuery{TMDBID: tmdbID})
	if err != nil {
		t.Fatalf("getMovieDetailsFromTMDB: %v", err)
	}
	if len(got.Ratings) != 2 || got.Ratings[0].Source != "tomatoes" || got.Ratings[1].Source != "audience" {
		t.Fatalf("expected enabled RT ratings, got %#v", got.Ratings)
	}
	if got.Logo == nil || got.Logo.URL != logo.URL {
		t.Fatalf("logo = %#v, want %q", got.Logo, logo.URL)
	}
	if calls := rt.callCount(); calls != 1 {
		t.Fatalf("network calls = %d, want 1 movie-details request", calls)
	}
}

func TestEnrichLiteCustomListItemKeepsGenres(t *testing.T) {
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  newFileCache(t.TempDir(), 24),
	}

	movieCacheID := movieExtendedCacheID(100)
	if err := svc.cache.set(movieCacheID, tvdbMovieExtendedData{
		Name:   "Cached Movie",
		Genres: []tvdbGenre{{Name: "Drama"}, {Name: "Thriller"}},
	}); err != nil {
		t.Fatalf("set movie cache: %v", err)
	}

	movieTVDBID := int64(100)
	movie := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		ID:          1,
		Rank:        1,
		Title:       "Raw Movie",
		TVDBID:      &movieTVDBID,
		MediaType:   "movie",
		ReleaseYear: 2024,
	})
	if got := strings.Join(movie.Title.Genres, ","); got != "Drama,Thriller" {
		t.Fatalf("movie genres = %q, want Drama,Thriller", got)
	}
	if movie.Title.Name != "Cached Movie" {
		t.Fatalf("movie name = %q, want Cached Movie", movie.Title.Name)
	}

	seriesCacheID := seriesExtendedCacheID(200, tvdbExtendedMetaTranslations)
	if err := svc.cache.set(seriesCacheID, tvdbSeriesExtendedData{
		Name:   "Cached Series",
		Genres: []tvdbGenre{{Name: "Comedy"}, {Name: "Mystery"}},
		Status: struct {
			Name string `json:"name"`
		}{Name: "Continuing"},
	}); err != nil {
		t.Fatalf("set series cache: %v", err)
	}

	seriesTVDBID := int64(200)
	series := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		ID:          2,
		Rank:        2,
		Title:       "Raw Series",
		TVDBID:      &seriesTVDBID,
		MediaType:   "show",
		ReleaseYear: 2023,
	})
	if got := strings.Join(series.Title.Genres, ","); got != "Comedy,Mystery" {
		t.Fatalf("series genres = %q, want Comedy,Mystery", got)
	}
	if series.Title.Status != models.SeriesReleaseStatusReleased {
		t.Fatalf("series status = %q, want released", series.Title.Status)
	}
	if series.Title.LifecycleStatus != "Continuing" {
		t.Fatalf("series lifecycle status = %q, want Continuing", series.Title.LifecycleStatus)
	}
}

func TestEnrichLiteCustomListItemAppliesConfiguredLanguageTranslations(t *testing.T) {
	svc := &Service{
		client: &tvdbClient{apiKey: "test-key", language: "eng"},
		cache:  newFileCache(t.TempDir(), 24),
	}

	movieTVDBID := int64(300)
	if err := svc.cache.set(movieExtendedCacheID(300), tvdbMovieExtendedData{
		Name:     "映画原題",
		Overview: "日本語のあらすじ",
		// The record lists the language without embedded translation
		// records, so the per-language translation (cached below) is used.
		tvdbExtendedTranslations: tvdbExtendedTranslations{NameLanguages: []string{"eng"}},
	}); err != nil {
		t.Fatalf("set movie extended cache: %v", err)
	}
	if err := svc.cache.set(cacheKey("tvdb", "movie", "translations", "v1", "300", "eng"), tvdbSeriesTranslation{
		Language: "eng",
		Name:     "Localized Movie",
		Overview: "Localized movie overview",
	}); err != nil {
		t.Fatalf("set movie translation cache: %v", err)
	}

	movie := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		Title:     "Raw Movie",
		TVDBID:    &movieTVDBID,
		MediaType: "movie",
	})
	if movie.Title.Name != "Localized Movie" || movie.Title.Overview != "Localized movie overview" {
		t.Fatalf("localized movie metadata = %q / %q", movie.Title.Name, movie.Title.Overview)
	}
	if movie.Title.Language != "eng" {
		t.Fatalf("movie language = %q, want eng", movie.Title.Language)
	}

	seriesTVDBID := int64(400)
	if err := svc.cache.set(seriesExtendedCacheID(400, tvdbExtendedMetaTranslations), tvdbSeriesExtendedData{
		Name:     "番組原題",
		Overview: "日本語の番組あらすじ",
		tvdbExtendedTranslations: tvdbExtendedTranslations{Translations: tvdbTranslationExtended{
			NameTranslations:     []tvdbTranslationRecord{{Language: "eng", Name: "Localized Series"}},
			OverviewTranslations: []tvdbTranslationRecord{{Language: "eng", Overview: "Localized series overview"}},
		}},
	}); err != nil {
		t.Fatalf("set series extended cache: %v", err)
	}

	series := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		Title:     "Raw Series",
		TVDBID:    &seriesTVDBID,
		MediaType: "show",
	})
	if series.Title.Name != "Localized Series" || series.Title.Overview != "Localized series overview" {
		t.Fatalf("localized series metadata = %q / %q", series.Title.Name, series.Title.Overview)
	}
	if series.Title.Language != "eng" {
		t.Fatalf("series language = %q, want eng", series.Title.Language)
	}
}

func TestEnrichLiteCustomListItemUsesFirstAiredWithoutEpisodes(t *testing.T) {
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  newFileCache(t.TempDir(), 24),
	}

	seriesTVDBID := int64(201)
	seriesCacheID := seriesExtendedCacheID(201, tvdbExtendedMetaTranslations)
	if err := svc.cache.set(seriesCacheID, tvdbSeriesExtendedData{
		Name:       "Current Year Series",
		FirstAired: time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("set series cache: %v", err)
	}

	series := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		ID:          3,
		Rank:        1,
		Title:       "Current Year Series",
		TVDBID:      &seriesTVDBID,
		MediaType:   "show",
		ReleaseYear: time.Now().Year(),
	})
	if series.Title.Status != models.SeriesReleaseStatusReleased {
		t.Fatalf("series status = %q, want released", series.Title.Status)
	}
}

func TestEnrichLiteCustomListItemFallsBackToTMDBGenres(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}
	svc.tmdb = newTMDBClient("tmdb-key", "eng", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Path {
			case "/3/movie/1049471":
				body = `{"id":1049471,"title":"Outcome","genres":[{"id":18,"name":"Drama"},{"id":53,"name":"Thriller"}]}`
			case "/3/tv/241609":
				body = `{"genres":[{"id":18,"name":"Drama"},{"id":9648,"name":"Mystery"}]}`
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Status:     "404 Not Found",
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}, cache)

	movieTVDBID := int64(358924)
	if err := svc.cache.set(movieExtendedCacheID(358924), tvdbMovieExtendedData{
		Name: "Outcome",
		RemoteIDs: []struct {
			ID         string `json:"id"`
			Type       int    `json:"type"`
			SourceName string `json:"sourceName"`
		}{
			{ID: "1049471", SourceName: "TheMovieDB.com"},
		},
	}); err != nil {
		t.Fatalf("set movie cache: %v", err)
	}
	movie := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		ID:          1,
		Rank:        1,
		Title:       "Outcome",
		TVDBID:      &movieTVDBID,
		MediaType:   "movie",
		ReleaseYear: 2025,
	})
	if got := strings.Join(movie.Title.Genres, ","); got != "Drama,Thriller" {
		t.Fatalf("movie genres = %q, want Drama,Thriller", got)
	}

	seriesTVDBID := int64(443433)
	if err := svc.cache.set(seriesExtendedCacheID(443433, tvdbExtendedMetaTranslations), tvdbSeriesExtendedData{
		Name: "Your Friends & Neighbors",
		RemoteIDs: []struct {
			ID         string `json:"id"`
			Type       int    `json:"type"`
			SourceName string `json:"sourceName"`
		}{
			{ID: "241609", SourceName: "TheMovieDB.com"},
		},
	}); err != nil {
		t.Fatalf("set series cache: %v", err)
	}
	series := svc.enrichLiteCustomListItem(context.Background(), mdblistItem{
		ID:          2,
		Rank:        1,
		Title:       "Your Friends & Neighbors",
		TVDBID:      &seriesTVDBID,
		MediaType:   "show",
		ReleaseYear: 2025,
	})
	if got := strings.Join(series.Title.Genres, ","); got != "Drama,Mystery" {
		t.Fatalf("series genres = %q, want Drama,Mystery", got)
	}
}

func TestBuildLiteCustomListItemUsesStableTitleFallbackID(t *testing.T) {
	first := buildLiteCustomListItem(mdblistItem{
		Title:       "Patriot",
		ReleaseYear: 2026,
		MediaType:   "movie",
	})
	second := buildLiteCustomListItem(mdblistItem{
		Title:       "Patriot",
		ReleaseYear: 2026,
		MediaType:   "movie",
	})
	other := buildLiteCustomListItem(mdblistItem{
		Title:       "Patriot",
		ReleaseYear: 2000,
		MediaType:   "movie",
	})

	if first.Title.ID == "mdblist:movie:0" {
		t.Fatal("title-only custom list item used colliding mdblist:movie:0 id")
	}
	if !strings.HasPrefix(first.Title.ID, "title:movie:") {
		t.Fatalf("fallback id = %q, want title:movie prefix", first.Title.ID)
	}
	if second.Title.ID != first.Title.ID {
		t.Fatalf("fallback id is not deterministic: %q vs %q", first.Title.ID, second.Title.ID)
	}
	if other.Title.ID == first.Title.ID {
		t.Fatalf("fallback id should include year, got %q for both movies", first.Title.ID)
	}
}

func TestBuildLiteCustomListItemPrefersExternalID(t *testing.T) {
	tmdbID := int64(123)
	item := buildLiteCustomListItem(mdblistItem{
		ID:          99,
		Title:       "Patriot",
		ReleaseYear: 2026,
		MediaType:   "movie",
		TMDBID:      &tmdbID,
	})
	if item.Title.ID != "tmdb:movie:123" {
		t.Fatalf("title id = %q, want tmdb:movie:123", item.Title.ID)
	}
}

func TestSelectCustomListTVDBSearchResultRejectsPawPatrolForPatriot(t *testing.T) {
	item := mdblistItem{
		Title:       "Patriot",
		ReleaseYear: 2023,
		MediaType:   "movie",
	}

	_, ok := selectCustomListTVDBSearchResult(item, []tvdbSearchResult{
		{Name: "PAW Patrol: The Mighty Movie", Year: "2023", TVDBID: "123"},
	})
	if ok {
		t.Fatal("expected Paw Patrol result to be rejected for Patriot")
	}
}

func TestSelectMovieDetailsTVDBSearchResultRejectsPawPatrolForPatriot(t *testing.T) {
	_, ok := selectMovieDetailsTVDBSearchResult(models.MovieDetailsQuery{
		Name: "Patriot",
		Year: 2026,
	}, []tvdbSearchResult{
		{Name: "Paw Patrol: The Dino Movie", Year: "2026", TVDBID: "123"},
	})
	if ok {
		t.Fatal("expected Paw Patrol result to be rejected for Patriot movie details")
	}
}

func TestSelectIMDBResolutionTVDBSearchResultRejectsWrongFirstResult(t *testing.T) {
	results := []tvdbSearchResult{
		tvdbSearchResultWithIMDB("Paw Patrol: The Dino Movie", "2026", "tt29356163"),
		tvdbSearchResultWithIMDB("Patriot", "2026", "tt33412884"),
	}

	result, ok := selectIMDBResolutionTVDBSearchResult("Patriot", 2026, results)
	if !ok {
		t.Fatal("expected matching Patriot result")
	}
	if got := imdbIDFromTVDBSearchResult(result); got != "tt33412884" {
		t.Fatalf("selected imdb id = %q, want tt33412884", got)
	}
}

func TestResolveIMDBIDUsesValidatedMovieResult(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{apiKey: "test-key", language: "eng"},
		cache:  cache,
	}
	results := []tvdbSearchResult{
		tvdbSearchResultWithIMDB("Paw Patrol: The Dino Movie", "2026", "tt29356163"),
		tvdbSearchResultWithIMDB("Patriot", "2026", "tt33412884"),
	}
	if err := cache.set(cacheKey("tvdb", "search", "movie", "Patriot", "2026", ""), results); err != nil {
		t.Fatalf("seed TVDB search cache: %v", err)
	}

	if got := svc.ResolveIMDBID(t.Context(), "Patriot", "movie", 2026); got != "tt33412884" {
		t.Fatalf("resolved imdb id = %q, want tt33412884", got)
	}
}

func TestResolveIMDBIDRetriesWithoutStrictTVDBYear(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{apiKey: "test-key", language: "eng"},
		cache:  cache,
	}
	if err := cache.set(cacheKey("tvdb", "search", "movie", "Idhayam Murali", "2026", ""), []tvdbSearchResult{}); err != nil {
		t.Fatalf("seed strict-year TVDB search cache: %v", err)
	}
	result := tvdbSearchResultWithIMDB("இதயம் முரளி", "2025", "tt35723557")
	result.Translations = map[string]string{
		"eng": "The One Side",
		"pt":  "Idhayam Murali",
		"tam": "இதயம் முரளி",
	}
	if err := cache.set(cacheKey("tvdb", "search", "movie", "Idhayam Murali", "", ""), []tvdbSearchResult{result}); err != nil {
		t.Fatalf("seed no-year TVDB search cache: %v", err)
	}

	if got := svc.ResolveIMDBID(t.Context(), "Idhayam Murali", "movie", 2026); got != "tt35723557" {
		t.Fatalf("resolved imdb id = %q, want tt35723557", got)
	}
}

func TestResolveIMDBIDFallsBackToTMDBExternalID(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	idCache := newFileCache(t.TempDir(), 168)
	svc := &Service{
		client:  &tvdbClient{language: "eng"},
		cache:   cache,
		idCache: idCache,
	}
	if err := cache.set(cacheKey("tvdb", "search", "series", "Captain Star", "1997", ""), []tvdbSearchResult{
		{Name: "Captain Star", Year: "1997", TVDBID: "78704"},
	}); err != nil {
		t.Fatalf("seed TVDB search cache: %v", err)
	}
	if err := cache.set(cacheKey("metadata", "search", "v6", "series", "Captain Star", "eng", "adult-blocked"), []models.SearchResult{
		{Title: models.Title{Name: "Captain Star", Year: 1997, MediaType: "series", TMDBID: 196}},
	}); err != nil {
		t.Fatalf("seed metadata search cache: %v", err)
	}
	if err := idCache.set(cacheKey("id", "tmdb-to-imdb", "series", "196"), "tt0143031"); err != nil {
		t.Fatalf("seed TMDB external ID cache: %v", err)
	}

	if got := svc.ResolveIMDBID(t.Context(), "Captain Star", "series", 1997); got != "tt0143031" {
		t.Fatalf("resolved imdb id = %q, want tt0143031", got)
	}
}

func TestBackfillSeriesIMDBIDUsesKnownTMDBID(t *testing.T) {
	idCache := newFileCache(t.TempDir(), 168)
	svc := &Service{idCache: idCache}
	if err := idCache.set(cacheKey("id", "tmdb-to-imdb", "series", "196"), "tt0143031"); err != nil {
		t.Fatalf("seed TMDB external ID cache: %v", err)
	}
	title := models.Title{TMDBID: 196}
	if !svc.backfillSeriesIMDBID(t.Context(), &title, models.SeriesDetailsQuery{}) {
		t.Fatal("expected title to be updated")
	}
	if title.IMDBID != "tt0143031" {
		t.Fatalf("title imdb id = %q, want tt0143031", title.IMDBID)
	}
}

func TestSelectIMDBResolutionTVDBSearchResultUsesTranslation(t *testing.T) {
	result := tvdbSearchResultWithIMDB("இதயம் முரளி", "2025", "tt35723557")
	result.Translations = map[string]string{
		"eng": "The One Side",
		"pt":  "Idhayam Murali",
		"tam": "இதயம் முரளி",
	}

	selected, ok := selectIMDBResolutionTVDBSearchResult("Idhayam Murali", 2026, []tvdbSearchResult{result})
	if !ok {
		t.Fatal("expected translated title with adjacent release year to match")
	}
	if got := imdbIDFromTVDBSearchResult(selected); got != "tt35723557" {
		t.Fatalf("selected imdb id = %q, want tt35723557", got)
	}
}

func TestSelectIMDBResolutionTVDBSearchResultRejectsYearMismatch(t *testing.T) {
	_, ok := selectIMDBResolutionTVDBSearchResult("Patriot", 2026, []tvdbSearchResult{
		tvdbSearchResultWithIMDB("Patriot", "2000", "tt0187393"),
	})
	if ok {
		t.Fatal("expected matching title with mismatched year to be rejected")
	}
}

func tvdbSearchResultWithIMDB(name, year, imdbID string) tvdbSearchResult {
	result := tvdbSearchResult{Name: name, Year: year}
	result.RemoteIDs = append(result.RemoteIDs, struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		SourceName string `json:"sourceName"`
	}{
		ID:         imdbID,
		SourceName: "IMDB",
	})
	return result
}

func imdbIDFromTVDBSearchResult(result tvdbSearchResult) string {
	for _, remote := range result.RemoteIDs {
		if strings.Contains(strings.ToLower(remote.SourceName), "imdb") {
			return remote.ID
		}
	}
	return ""
}

func TestSelectCustomListTVDBSearchResultRejectsRemoteIDMismatch(t *testing.T) {
	item := mdblistItem{
		Title:       "Patriot",
		ReleaseYear: 2026,
		IMDBID:      "tt33412884",
		MediaType:   "movie",
	}

	_, ok := selectCustomListTVDBSearchResult(item, []tvdbSearchResult{
		{
			Name:   "Patriot",
			Year:   "2026",
			TVDBID: "123",
			RemoteIDs: []struct {
				ID         string `json:"id"`
				Type       int    `json:"type"`
				SourceName string `json:"sourceName"`
			}{
				{ID: "tt0000000", SourceName: "IMDB"},
			},
		},
	})
	if ok {
		t.Fatal("expected mismatched remote imdb id to be rejected")
	}
}

func TestSelectCustomListTVDBSearchResultAcceptsArticleVariant(t *testing.T) {
	item := mdblistItem{
		Title:       "Patriot",
		ReleaseYear: 2000,
		MediaType:   "movie",
	}

	result, ok := selectCustomListTVDBSearchResult(item, []tvdbSearchResult{
		{Name: "The Patriot", Year: "2000", TVDBID: "456"},
	})
	if !ok {
		t.Fatal("expected article-only title variant to be accepted")
	}
	if result.TVDBID != "456" {
		t.Fatalf("selected tvdb id = %q, want 456", result.TVDBID)
	}
}

func TestSelectCustomListTVDBSearchResultRejectsYearMismatch(t *testing.T) {
	item := mdblistItem{
		Title:       "The Patriot",
		ReleaseYear: 2000,
		MediaType:   "movie",
	}

	_, ok := selectCustomListTVDBSearchResult(item, []tvdbSearchResult{
		{Name: "The Patriot", Year: "1998", TVDBID: "789"},
	})
	if ok {
		t.Fatal("expected matching title with mismatched year to be rejected")
	}
}

func TestGetCachedArtworkURLsResolvesSeriesTMDBToTVDBCache(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}

	if err := cache.set(seriesTVDBResolutionCacheKey(71712), int64(328634)); err != nil {
		t.Fatalf("set resolve cache: %v", err)
	}
	if err := cache.set(seriesDetailsCacheKey("eng", 328634, ""), models.SeriesDetails{
		Title: models.Title{
			TextPoster:   &models.Image{URL: "https://example.test/text-poster.jpg", Type: "poster"},
			TextBackdrop: &models.Image{URL: "https://example.test/text-backdrop.jpg", Type: "backdrop"},
			Backdrops: []models.Image{
				{URL: "https://example.test/alt-1.jpg", Type: "backdrop"},
				{URL: "https://example.test/alt-2.jpg", Type: "backdrop"},
			},
		},
	}); err != nil {
		t.Fatalf("set series cache: %v", err)
	}

	textPoster, textBackdrop, backdrops := svc.GetCachedArtworkURLs("series", 71712, 0)
	if textPoster != "https://example.test/text-poster.jpg" {
		t.Fatalf("textPoster = %q", textPoster)
	}
	if textBackdrop != "https://example.test/text-backdrop.jpg" {
		t.Fatalf("textBackdrop = %q", textBackdrop)
	}
	if len(backdrops) != 2 || backdrops[0] != "https://example.test/alt-1.jpg" {
		t.Fatalf("backdrops = %#v", backdrops)
	}
}

func TestGetCachedLogoURLPrefersDetailsThenImagesCache(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}

	if err := cache.set(cacheKey("tmdb", "images", "v10", "eng", "movie", "603"), tmdbImagesResult{
		Logo: &models.Image{URL: "https://example.test/images-logo.png", Type: "logo"},
	}); err != nil {
		t.Fatalf("set images cache: %v", err)
	}
	if got := svc.GetCachedLogoURL("movie", 603, 0); got != "https://example.test/images-logo.png" {
		t.Fatalf("logo = %q, want images cache logo", got)
	}

	if err := cache.set(cacheKey("tmdb", "movie", "details", "v3", "eng", "603"), models.Title{
		Logo: &models.Image{URL: "https://example.test/details-logo.png", Type: "logo"},
	}); err != nil {
		t.Fatalf("set details cache: %v", err)
	}
	if got := svc.GetCachedLogoURL("movie", 603, 0); got != "https://example.test/details-logo.png" {
		t.Fatalf("logo = %q, want details cache logo", got)
	}

	if got := svc.GetCachedLogoURL("series", 999, 0); got != "" {
		t.Fatalf("uncached logo = %q, want empty", got)
	}
}

func TestGetCachedLogoURLReadsTMDBSeriesFallbackCache(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}

	if err := cache.set(cacheKey("tmdb", "series", "details-fallback", "v4", "eng", "75191"), models.SeriesDetails{
		Title: models.Title{Logo: &models.Image{URL: "https://example.test/fallback-logo.png", Type: "logo"}},
	}); err != nil {
		t.Fatalf("set fallback cache: %v", err)
	}
	if got := svc.GetCachedLogoURL("series", 75191, 0); got != "https://example.test/fallback-logo.png" {
		t.Fatalf("logo = %q, want TMDB fallback logo", got)
	}
}

func TestGetCachedLogoURLReadsShelfImagesCache(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}

	if err := cache.set(shelfImagesCacheKey("eng", "series", 1399), tmdbImagesResult{
		Logo: &models.Image{URL: "https://example.test/shelf-logo.png", Type: "logo"},
	}); err != nil {
		t.Fatalf("set shelf images cache: %v", err)
	}
	if got := svc.GetCachedLogoURL("series", 1399, 0); got != "https://example.test/shelf-logo.png" {
		t.Fatalf("logo = %q, want shelf images logo", got)
	}
}

func TestGetCachedArtworkURLsUsesMetadataLanguageForTMDBImages(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "fra"},
		cache:  cache,
	}

	if err := cache.set(cacheKey("tmdb", "images", "v10", "eng", "series", "71712"), tmdbImagesResult{
		TextPoster: &models.Image{URL: "https://example.test/english-poster.jpg", Type: "poster", Language: "en"},
	}); err != nil {
		t.Fatalf("set english images cache: %v", err)
	}
	if err := cache.set(cacheKey("tmdb", "images", "v10", "fra", "series", "71712"), tmdbImagesResult{
		TextPoster: &models.Image{URL: "https://example.test/french-poster.jpg", Type: "poster", Language: "fr"},
	}); err != nil {
		t.Fatalf("set french images cache: %v", err)
	}

	textPoster, _, _ := svc.GetCachedArtworkURLs("series", 71712, 0)
	if textPoster != "https://example.test/french-poster.jpg" {
		t.Fatalf("textPoster = %q, want french cache entry", textPoster)
	}
}

func TestEnrichShelfArtworkFromCacheAppliesArtworkPastFetchLimit(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}

	if err := cache.set(cacheKey("tmdb", "images", "v10", "eng", "movie", "1674087"), tmdbImagesResult{
		TextlessPoster: &models.Image{URL: "https://image.example/poster.jpg", Type: "poster"},
		TextPoster:     &models.Image{URL: "https://image.example/text-poster.jpg", Type: "poster"},
	}); err != nil {
		t.Fatalf("set images cache: %v", err)
	}

	items := []models.TrendingItem{
		{Title: models.Title{ID: "tmdb:movie:1", Name: "First", MediaType: "movie", TMDBID: 1}},
		{Title: models.Title{ID: "tvdb:movie:376107", Name: "Bang My Box: The Robin Byrd Story", MediaType: "movie", TMDBID: 1674087}},
	}

	if !svc.enrichShelfArtworkFromCache(items) {
		t.Fatal("expected cached artwork merge to update an item")
	}
	if items[1].Title.Poster == nil || items[1].Title.Poster.URL != "https://image.example/poster.jpg" {
		t.Fatalf("poster = %#v, want cached poster", items[1].Title.Poster)
	}
	if items[1].Title.TextPoster == nil || items[1].Title.TextPoster.URL != "https://image.example/text-poster.jpg" {
		t.Fatalf("textPoster = %#v, want cached text poster", items[1].Title.TextPoster)
	}
}

func TestGetCachedArtworkURLsResolvesMovieTMDBToTVDBCache(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
	}

	if err := cache.set(cacheKey("tvdb", "resolve", "movie", "tmdb", "752"), int64(528)); err != nil {
		t.Fatalf("set resolve cache: %v", err)
	}
	if err := cache.set(cacheKey("tvdb", "movie", "details", "v6", "eng", "528"), models.Title{
		TextBackdrop: &models.Image{URL: "https://example.test/movie-text-backdrop.jpg", Type: "backdrop"},
		Backdrops: []models.Image{
			{URL: "https://example.test/movie-alt-1.jpg", Type: "backdrop"},
			{URL: "https://example.test/movie-alt-2.jpg", Type: "backdrop"},
		},
	}); err != nil {
		t.Fatalf("set movie cache: %v", err)
	}

	_, textBackdrop, backdrops := svc.GetCachedArtworkURLs("movie", 752, 0)
	if textBackdrop != "https://example.test/movie-text-backdrop.jpg" {
		t.Fatalf("textBackdrop = %q", textBackdrop)
	}
	if len(backdrops) != 2 || backdrops[0] != "https://example.test/movie-alt-1.jpg" {
		t.Fatalf("backdrops = %#v", backdrops)
	}
}

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
				body := bytes.NewBufferString(`{"data":{"id":12345,"name":"テストアニメ","overview":"これは日本語の概要です","artworks":[],"nameTranslations":["eng","jpn"]}}`)
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
				body := bytes.NewBufferString(`{"data":{"id":67890,"name":"テスト映画","overview":"これは日本語の映画概要です","artworks":[],"nameTranslations":["eng","jpn"]}}`)
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
				if query.Get("meta") == "translations" {
					body := bytes.NewBufferString(`{"data":{"id":12345,"artworks":[],"translations":{"nameTranslations":[{"language":"eng","name":"Test Series"}],"overviewTranslations":[{"language":"eng","overview":"Series overview"}]}}}`)
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
				}
				if query.Get("meta") != "episodes" {
					t.Errorf("unexpected meta query: %q", query.Get("meta"))
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
	if got := lite.Seasons[0].Episodes[0].AbsoluteEpisodeNumber; got != 1 {
		t.Fatalf("lite absolute episode = %d, want release absolute 1", got)
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
	if got := full.Seasons[0].Episodes[0].AbsoluteEpisodeNumber; got != 1 {
		t.Fatalf("full absolute episode = %d, want release absolute 1", got)
	}
	if seasonTranslationHit == 0 {
		t.Fatal("expected full SeriesDetails to fetch season translations for cached lite data")
	}
}

func TestResolveSeriesTVDBIDDoesNotUseLegacyNameDerivedMappingWhenTMDBHasNoTVDBID(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	if err := cache.set(cacheKey("tvdb", "resolve", "tmdb", "107124"), int64(375642)); err != nil {
		t.Fatalf("seed legacy resolution cache: %v", err)
	}

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/3/tv/107124" {
				t.Fatalf("unexpected request: %s", req.URL.String())
			}
			body := `{
				"id":107124,
				"name":"Animaniacs",
				"first_air_date":"2020-11-20",
				"external_ids":{"imdb_id":"tt6951546","tvdb_id":0}
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	service := &Service{
		client:           &tvdbClient{language: "eng"},
		tmdb:             newTMDBClient("test-tmdb-key", "eng", httpc, cache),
		cache:            cache,
		inflightRequests: make(map[string]*inflightRequest),
	}

	got, err := service.resolveSeriesTVDBIDActual(context.Background(), models.SeriesDetailsQuery{
		TitleID: "tmdb:tv:107124",
		Name:    "Animaniacs",
		Year:    2020,
		TMDBID:  107124,
	})
	if err == nil || !strings.Contains(err.Error(), "has no tvdb external id") {
		t.Fatalf("resolve error = %v, want authoritative missing-TVDB error", err)
	}
	if got != 0 {
		t.Fatalf("resolved TVDB ID = %d, want 0", got)
	}

	var cached int64
	if ok, _ := cache.get(seriesTVDBResolutionCacheKey(107124), &cached); ok {
		t.Fatalf("unexpected v2 mapping cached: %d", cached)
	}
}

func TestSeriesDetailsLiteFallsBackToTMDBAndKeepsLogoOnCachedProviderMismatch(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	if err := cache.set(seriesTVDBResolutionCacheKey(107124), int64(375642)); err != nil {
		t.Fatalf("seed resolution cache: %v", err)
	}
	liteCacheID := seriesDetailsLiteCacheKey("eng", 375642, "")
	if err := cache.set(liteCacheID, models.SeriesDetails{
		Title: models.Title{
			ID:        "tvdb:series:375642",
			Name:      "Animania",
			MediaType: "series",
			TVDBID:    375642,
			TMDBID:    96025,
		},
		Seasons: []models.SeriesSeason{{Number: 1, EpisodeCount: 12}},
	}); err != nil {
		t.Fatalf("seed mismatched lite cache: %v", err)
	}
	imagesCacheID := cacheKey("tmdb", "images", "v10", "eng", "series", "107124")
	if err := cache.set(imagesCacheID, tmdbImagesResult{
		Logo: &models.Image{
			URL:      "https://image.tmdb.org/t/p/w500/animaniacs-logo.png",
			Type:     "logo",
			Language: "en",
		},
	}); err != nil {
		t.Fatalf("seed image cache: %v", err)
	}

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Path {
			case "/3/tv/107124":
				body = `{
					"id":107124,
					"name":"Animaniacs",
					"original_name":"Animaniacs",
					"first_air_date":"2020-11-20",
					"poster_path":"/poster.jpg",
					"backdrop_path":"/backdrop.jpg",
					"external_ids":{"imdb_id":"tt6951546","tvdb_id":0},
					"seasons":[]
				}`
			case "/3/tv/107124/aggregate_credits":
				body = `{"cast":[]}`
			case "/3/tv/107124/content_ratings":
				body = `{"results":[]}`
			default:
				t.Fatalf("unexpected request: %s", req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	service := &Service{
		client:           &tvdbClient{language: "eng"},
		tmdb:             newTMDBClient("test-tmdb-key", "eng", httpc, cache),
		cache:            cache,
		inflightRequests: make(map[string]*inflightRequest),
	}

	details, err := service.SeriesDetailsLite(context.Background(), models.SeriesDetailsQuery{
		TitleID: "tmdb:tv:107124",
		Name:    "Animaniacs",
		Year:    2020,
		TMDBID:  107124,
	})
	if err != nil {
		t.Fatalf("SeriesDetailsLite: %v", err)
	}
	if details.Title.Name != "Animaniacs" || details.Title.TMDBID != 107124 {
		t.Fatalf("fallback title = %+v, want Animaniacs/TMDB 107124", details.Title)
	}
	if details.Title.TVDBID != 0 {
		t.Fatalf("fallback TVDB ID = %d, want 0", details.Title.TVDBID)
	}
	if details.Title.Logo == nil || details.Title.Logo.URL != "https://image.tmdb.org/t/p/w500/animaniacs-logo.png" {
		t.Fatalf("fallback logo = %#v, want cached TMDB logo", details.Title.Logo)
	}
}

func TestMergeSearchResultsPrefersTVDBWhenTMDBIDMatches(t *testing.T) {
	results := mergeSearchResults([]models.SearchResult{
		{
			Title: models.Title{
				ID:         "tmdb:tv:123",
				Name:       "Test Show",
				MediaType:  "series",
				TMDBID:     123,
				Poster:     &models.Image{URL: "https://image.tmdb.org/t/p/w780/poster.jpg", Type: "poster"},
				Popularity: 77,
				VoteCount:  1234,
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
	if title.Popularity != 77 || title.VoteCount != 1234 {
		t.Fatalf("expected TMDB ranking metadata to be preserved, got popularity=%.1f voteCount=%d", title.Popularity, title.VoteCount)
	}
	if results[0].Score != 80 {
		t.Fatalf("expected higher merged score to be preserved, got %d", results[0].Score)
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
		if result.Title.MediaType == "movie" && result.Title.Status != models.MovieReleaseStatusReleased {
			t.Fatalf("old movie search status = %q, want %q", result.Title.Status, models.MovieReleaseStatusReleased)
		}
	}
	if !seen["movie"] || !seen["series"] {
		t.Fatalf("expected both movie and series results, got %+v", results)
	}
}

func TestSearchBlocksAdultResultsByDefaultAndAllowsWhenEnabled(t *testing.T) {
	var tmdbIncludeAdult []string
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/v4/login":
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			case "/v4/search":
				body := `{"data":[
					{"type":"movie","tvdb_id":"101","name":"Regular Movie","year":"2020","score":100,"adult":false},
					{"type":"movie","tvdb_id":"102","name":"Adult Movie","year":"2021","score":90,"adult":true}
				]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/search/movie":
				tmdbIncludeAdult = append(tmdbIncludeAdult, req.URL.Query().Get("include_adult"))
				body := `{"results":[
					{"id":201,"title":"Regular TMDB","media_type":"movie","release_date":"2022-01-01","popularity":10,"adult":false},
					{"id":202,"title":"Adult TMDB","media_type":"movie","release_date":"2022-01-01","popularity":9,"adult":true}
				]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		}),
	}
	cacheDir := t.TempDir()
	svc := &Service{
		client: newTVDBClient("test-tvdb-key", "eng", httpc, 24),
		tmdb:   newTMDBClient("test-tmdb-key", "eng", httpc, newFileCache(cacheDir, 24)),
		cache:  newFileCache(cacheDir, 24),
	}
	svc.client.minInterval = 0

	results, err := svc.Search(context.Background(), "movie", "movie")
	if err != nil {
		t.Fatalf("Search with adult blocked failed: %v", err)
	}
	for _, result := range results {
		if result.Title.Adult {
			t.Fatalf("adult result was returned while blocked: %+v", result.Title)
		}
	}
	if len(results) != 2 {
		t.Fatalf("blocked search returned %d results, want 2: %+v", len(results), results)
	}
	if len(tmdbIncludeAdult) != 1 || tmdbIncludeAdult[0] != "false" {
		t.Fatalf("TMDB include_adult calls = %v, want [false]", tmdbIncludeAdult)
	}

	svc.SetAllowAdultSearch(true)
	results, err = svc.Search(context.Background(), "movie", "movie")
	if err != nil {
		t.Fatalf("Search with adult allowed failed: %v", err)
	}
	adultCount := 0
	for _, result := range results {
		if result.Title.Adult {
			adultCount++
		}
	}
	if adultCount != 2 {
		t.Fatalf("adult search returned %d adult results, want 2: %+v", adultCount, results)
	}
	if len(tmdbIncludeAdult) != 2 || tmdbIncludeAdult[1] != "true" {
		t.Fatalf("TMDB include_adult calls = %v, want second true", tmdbIncludeAdult)
	}
}

func TestSearchEnrichesResultsWithRequestedLanguage(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/v4/login":
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			case "/v4/search":
				body := `{"data":[{"type":"series","tvdb_id":"202","tmdb_id":"303","name":"English Search Title","overview":"English search overview","year":"2020","score":100}]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/v4/series/202/extended":
				body := `{"data":{"id":202,"translations":{"nameTranslations":[{"language":"fra","name":"Titre français"}],"overviewTranslations":[{"language":"fra","overview":"Résumé français"}]}}}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/search/tv":
				body := `{"results":[]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/tv/303/images":
				body := `{"logos":[],"backdrops":[],"posters":[{"file_path":"/poster-fr.jpg","iso_639_1":"fr","vote_average":8.0}]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/tv/303":
				body := `{"id":303,"name":"Titre français","overview":"Résumé français"}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		}),
	}
	cacheDir := t.TempDir()
	svc := &Service{
		client: &tvdbClient{apiKey: "test-tvdb-key", language: "fra", httpc: httpc, minInterval: 0, translationCacheTTL: 24 * time.Hour},
		tmdb:   newTMDBClient("test-tmdb-key", "fra", httpc, newFileCache(cacheDir, 24)),
		cache:  newFileCache(cacheDir, 24),
	}

	results, err := svc.Search(context.Background(), "english", "series")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d: %+v", len(results), results)
	}
	title := results[0].Title
	if title.Name != "Titre français" {
		t.Fatalf("title name = %q, want translated title", title.Name)
	}
	if title.Overview != "Résumé français" {
		t.Fatalf("overview = %q, want translated overview", title.Overview)
	}
	if title.TextPoster == nil || title.TextPoster.URL != "https://image.tmdb.org/t/p/w780/poster-fr.jpg" {
		t.Fatalf("text poster = %#v, want localized TMDB poster", title.TextPoster)
	}
}

func TestSearchLocalizesTMDBOnlyMovieTitleAndOverview(t *testing.T) {
	// A TMDB-sourced movie with no TVDB id has no TVDB translation, so the
	// localized title/overview must come from TMDB movie details.
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/v4/login":
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			case "/v4/search":
				body := `{"data":[]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/search/movie":
				body := `{"results":[{"id":698687,"title":"Transformers One","overview":"The untold origin story.","release_date":"2024-09-11","popularity":80,"vote_average":8.0,"vote_count":1000,"adult":false}]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/movie/698687":
				body := `{"id":698687,"title":"Transformers One","overview":"La storia inedita delle origini.","release_date":"2024-09-11"}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/movie/698687/images":
				body := `{"logos":[],"backdrops":[],"posters":[{"file_path":"/poster-it.jpg","iso_639_1":"it","vote_average":8.0}]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		}),
	}
	cacheDir := t.TempDir()
	svc := &Service{
		client: &tvdbClient{apiKey: "test-tvdb-key", language: "ita", httpc: httpc, minInterval: 0, translationCacheTTL: 24 * time.Hour},
		tmdb:   newTMDBClient("test-tmdb-key", "ita", httpc, newFileCache(cacheDir, 24)),
		cache:  newFileCache(cacheDir, 24),
	}

	results, err := svc.Search(context.Background(), "transformers one", "movie")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d: %+v", len(results), results)
	}
	if got := results[0].Title.Overview; got != "La storia inedita delle origini." {
		t.Fatalf("overview = %q, want localized TMDB overview", got)
	}
}

func TestSearchTMDBVoteCountBoostsCanonicalMatch(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/v4/login":
				body := bytes.NewBufferString(`{"data":{"token":"test-token"}}`)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), Header: make(http.Header)}, nil
			case "/v4/search":
				body := `{"data":[]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			case "/3/search/movie":
				body := `{"results":[
					{"id":120,"title":"The Lord of the Rings: The Fellowship of the Ring","release_date":"2001-12-18","popularity":78,"vote_average":8.4,"vote_count":26000,"adult":false},
					{"id":999,"title":"Fellowship of the Ring","release_date":"2020-01-01","popularity":80,"vote_average":5.0,"vote_count":4,"adult":false}
				]}`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		}),
	}
	cacheDir := t.TempDir()
	svc := &Service{
		client: newTVDBClient("test-tvdb-key", "eng", httpc, 24),
		tmdb:   newTMDBClient("test-tmdb-key", "eng", httpc, newFileCache(cacheDir, 24)),
		cache:  newFileCache(cacheDir, 24),
	}
	svc.client.minInterval = 0

	results, err := svc.Search(context.Background(), "fellowship of the ring", "movie")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}

	canonical := results[0]
	shortTitle := results[1]
	if canonical.Title.TMDBID != 120 {
		t.Fatalf("expected canonical LOTR result first from TMDB payload, got tmdb=%d title=%q", canonical.Title.TMDBID, canonical.Title.Name)
	}
	if canonical.Title.VoteCount != 26000 {
		t.Fatalf("canonical vote count = %d, want 26000", canonical.Title.VoteCount)
	}
	if canonical.Score <= shortTitle.Score {
		t.Fatalf("expected vote count to boost canonical score above short-title score, got canonical=%d short=%d", canonical.Score, shortTitle.Score)
	}
}

func TestEnrichTMDBEpisodeMetadataRetainsIDsAndOverridesTVDBStills(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/3/tv/42/season/1" {
				body := bytes.NewBufferString(`{"id":1001,"name":"Season 1","season_number":1,"episodes":[
					{"id":5001,"name":"Pilot","season_number":1,"episode_number":1,"still_path":"/tmdb-pilot.jpg"},
					{"id":5002,"name":"Second","season_number":1,"episode_number":2},
					{"id":6172,"name":"Absolute","season_number":1,"episode_number":1172}
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
				EpisodeCount: 3,
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
					{
						ID:                    "tvdb:episode:3",
						Name:                  "Absolute",
						SeasonNumber:          1,
						EpisodeNumber:         17,
						AbsoluteEpisodeNumber: 1172,
					},
				},
			},
		},
	}

	changed, complete := service.enrichTMDBEpisodeMetadata(context.Background(), &details, 42)
	if !changed || !complete {
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
	if got := details.Seasons[0].Episodes[0].TMDBID; got != 5001 {
		t.Fatalf("expected first episode TMDB ID 5001, got %d", got)
	}

	gotSecondImage := details.Seasons[0].Episodes[1].Image
	if gotSecondImage == nil || gotSecondImage.URL != "https://artworks.thetvdb.com/banners/tvdb-second.jpg" {
		t.Fatalf("expected second episode to keep TVDB image when TMDB still is missing, got %#v", gotSecondImage)
	}
	if got := details.Seasons[0].Episodes[1].TMDBID; got != 5002 {
		t.Fatalf("expected second episode TMDB ID 5002 even without TMDB artwork, got %d", got)
	}
	absoluteEpisode := details.Seasons[0].Episodes[2]
	if absoluteEpisode.TMDBID != 6172 || absoluteEpisode.TMDBSeasonNumber != 1 || absoluteEpisode.TMDBEpisodeNumber != 1172 {
		t.Fatalf("expected absolute-number crosswalk to retain TMDB identity, got %+v", absoluteEpisode)
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

func TestGetCustomListSuppressProgress(t *testing.T) {
	var (
		svc                *Service
		observedActive     int
		observedSuppressed int
		suppressExpected   bool
	)

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "mdblist.com") {
				snap := svc.GetProgressSnapshot()
				if suppressExpected {
					observedSuppressed = snap.ActiveCount
				} else {
					observedActive = snap.ActiveCount
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(`[]`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	svc = &Service{
		client:        newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:         newFileCache(t.TempDir(), 24),
		progressTasks: make(map[string]*ProgressTask),
	}
	svc.client.minInterval = 0

	suppressExpected = false
	if _, _, _, err := svc.GetCustomList(context.Background(), "https://mdblist.com/lists/test/progress/json", CustomListOptions{}); err != nil {
		t.Fatalf("GetCustomList with progress failed: %v", err)
	}
	if observedActive == 0 {
		t.Fatal("expected active progress task during regular custom list fetch")
	}

	suppressExpected = true
	if _, _, _, err := svc.GetCustomList(context.Background(), "https://mdblist.com/lists/test/suppressed/json", CustomListOptions{SuppressProgress: true}); err != nil {
		t.Fatalf("GetCustomList with suppressed progress failed: %v", err)
	}
	if observedSuppressed != 0 {
		t.Fatalf("expected no active progress task during suppressed custom list fetch, got %d", observedSuppressed)
	}
}

func TestDiscoverByGenreWithOptionsAllowsFiftyItems(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	var requestedPages []string
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
		tmdb: newTMDBClient("tmdb-key", "eng", &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasPrefix(req.URL.Path, "/3/movie/") && strings.HasSuffix(req.URL.Path, "/images") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"backdrops":[],"posters":[],"logos":[]}`)),
						Header:     make(http.Header),
					}, nil
				}
				if req.URL.Path != "/3/discover/movie" {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Status:     "404 Not Found",
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Header:     make(http.Header),
					}, nil
				}
				page := req.URL.Query().Get("page")
				requestedPages = append(requestedPages, page)
				pageNum, _ := strconv.Atoi(page)
				startID := int64((pageNum-1)*20 + 1)
				var results []string
				for i := int64(0); i < 20; i++ {
					id := startID + i
					results = append(results, fmt.Sprintf(
						`{"id":%d,"title":"Movie %d","release_date":"2025-01-01","genre_ids":[28],"popularity":%d}`,
						id,
						id,
						100-id,
					))
				}
				body := fmt.Sprintf(`{"results":[%s],"total_results":60}`, strings.Join(results, ","))
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}, cache),
	}

	items, total, err := svc.DiscoverByGenreWithOptions(context.Background(), "movie", 28, 50, 0, ShelfLoadOptions{
		Lite:         true,
		ArtworkLimit: 20,
	})
	if err != nil {
		t.Fatalf("DiscoverByGenreWithOptions: %v", err)
	}
	if total != 60 {
		t.Fatalf("total = %d, want 60", total)
	}
	if len(items) != 50 {
		t.Fatalf("len(items) = %d, want 50", len(items))
	}
	if items[49].Rank != 50 || items[49].Title.Name != "Movie 50" {
		t.Fatalf("last item = rank %d name %q, want rank 50 Movie 50", items[49].Rank, items[49].Title.Name)
	}
	if got := strings.Join(requestedPages, ","); got != "1,2,3" {
		t.Fatalf("requested pages = %s, want 1,2,3", got)
	}
}

func TestDiscoverByGenreWithOptionsDedupesRepeatedTMDBIDs(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	var requestedPages []string
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
		tmdb: newTMDBClient("tmdb-key", "eng", &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasPrefix(req.URL.Path, "/3/movie/") && strings.HasSuffix(req.URL.Path, "/images") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"backdrops":[],"posters":[],"logos":[]}`)),
						Header:     make(http.Header),
					}, nil
				}
				if req.URL.Path != "/3/discover/movie" {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Status:     "404 Not Found",
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Header:     make(http.Header),
					}, nil
				}
				page := req.URL.Query().Get("page")
				requestedPages = append(requestedPages, page)
				pageNum, _ := strconv.Atoi(page)

				var ids []int64
				switch pageNum {
				case 1:
					for id := int64(1); id <= 20; id++ {
						ids = append(ids, id)
					}
				case 2:
					ids = append(ids, 5)
					for id := int64(21); id <= 39; id++ {
						ids = append(ids, id)
					}
				default:
					for id := int64((pageNum-1)*20 + 1); id <= int64(pageNum*20); id++ {
						ids = append(ids, id)
					}
				}

				results := make([]string, 0, len(ids))
				for _, id := range ids {
					results = append(results, fmt.Sprintf(
						`{"id":%d,"title":"Movie %d","release_date":"2025-01-01","genre_ids":[28],"popularity":%d}`,
						id,
						id,
						100-id,
					))
				}
				body := fmt.Sprintf(`{"results":[%s],"total_results":60}`, strings.Join(results, ","))
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}, cache),
	}

	items, total, err := svc.DiscoverByGenreWithOptions(context.Background(), "movie", 28, 25, 0, ShelfLoadOptions{
		Lite:         true,
		ArtworkLimit: 20,
	})
	if err != nil {
		t.Fatalf("DiscoverByGenreWithOptions: %v", err)
	}
	if total != 60 {
		t.Fatalf("total = %d, want 60", total)
	}
	if len(items) != 25 {
		t.Fatalf("len(items) = %d, want 25", len(items))
	}
	seen := make(map[int64]bool, len(items))
	for i, item := range items {
		if item.Rank != i+1 {
			t.Fatalf("item %d rank = %d, want %d", i, item.Rank, i+1)
		}
		if seen[item.Title.TMDBID] {
			t.Fatalf("duplicate tmdb id %d in discover results", item.Title.TMDBID)
		}
		seen[item.Title.TMDBID] = true
	}
	if !seen[25] {
		t.Fatalf("expected page 2 unique items to fill the limit, got ids=%v", seen)
	}
	if got := strings.Join(requestedPages, ","); got != "1,2" {
		t.Fatalf("requested pages = %s, want 1,2", got)
	}
}

func TestDiscoverByDecadeWithOptions(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	var capturedQuery string
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		cache:  cache,
		tmdb: newTMDBClient("tmdb-key", "eng", &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/3/discover/movie" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"backdrops":[],"posters":[],"logos":[]}`)),
						Header:     make(http.Header),
					}, nil
				}
				capturedQuery = req.URL.RawQuery
				body := `{"results":[{"id":1,"title":"Eighties Movie","release_date":"1984-06-08","popularity":50}],"total_results":1}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}, cache),
	}

	items, total, err := svc.DiscoverByDecadeWithOptions(context.Background(), "movie", 1980, 20, 0, ShelfLoadOptions{
		Lite:         true,
		ArtworkLimit: 20,
	})
	if err != nil {
		t.Fatalf("DiscoverByDecadeWithOptions: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total = %d len(items) = %d, want 1/1", total, len(items))
	}
	if items[0].Title.Name != "Eighties Movie" || items[0].Title.Year != 1984 {
		t.Fatalf("unexpected item: %+v", items[0].Title)
	}
	if !strings.Contains(capturedQuery, "primary_release_date.gte=1980-01-01") ||
		!strings.Contains(capturedQuery, "primary_release_date.lte=1989-12-31") ||
		!strings.Contains(capturedQuery, "vote_count.gte=300") ||
		!strings.Contains(capturedQuery, "language=en-US") ||
		!strings.Contains(capturedQuery, "with_original_language=en") {
		t.Fatalf("decade filters missing from query: %s", capturedQuery)
	}

	if _, _, err := svc.DiscoverByDecadeWithOptions(context.Background(), "movie", 1985, 20, 0, ShelfLoadOptions{}); err == nil {
		t.Fatalf("expected error for non-decade year")
	}
}

func TestDiscoverByGenreWithOptionsFiltersOriginalLanguage(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	var capturedQuery string
	svc := &Service{
		client: &tvdbClient{language: "jpn"},
		cache:  cache,
		tmdb: newTMDBClient("tmdb-key", "jpn", &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasPrefix(req.URL.Path, "/3/movie/") && strings.HasSuffix(req.URL.Path, "/images") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"backdrops":[],"posters":[],"logos":[]}`)),
						Header:     make(http.Header),
					}, nil
				}
				if req.URL.Path != "/3/discover/movie" {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Status:     "404 Not Found",
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Header:     make(http.Header),
					}, nil
				}
				capturedQuery = req.URL.RawQuery
				body := `{"results":[{"id":1,"title":"Genre Movie","release_date":"2025-01-01","popularity":50}],"total_results":1}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}, cache),
	}

	items, total, err := svc.DiscoverByGenreWithOptions(context.Background(), "movie", 28, 20, 0, ShelfLoadOptions{
		Lite:         true,
		ArtworkLimit: 20,
	})
	if err != nil {
		t.Fatalf("DiscoverByGenreWithOptions: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total = %d len(items) = %d, want 1/1", total, len(items))
	}
	if !strings.Contains(capturedQuery, "with_genres=28") ||
		!strings.Contains(capturedQuery, "language=ja-US") ||
		!strings.Contains(capturedQuery, "with_original_language=ja") {
		t.Fatalf("genre language filters missing from query: %s", capturedQuery)
	}
}

func TestSimilarUsesMetadataOriginalLanguage(t *testing.T) {
	cache := newFileCache(t.TempDir(), 24)
	var discoverQuery string
	svc := &Service{
		client: &tvdbClient{language: "jpn"},
		cache:  cache,
		tmdb: newTMDBClient("tmdb-key", "jpn", &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/3/movie/123":
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{"genres":[{"id":28}],"original_language":"ko","release_date":"2020-01-01","keywords":{"keywords":[{"id":101}]}}`)),
						Header:     make(http.Header),
					}, nil
				case "/3/discover/movie":
					discoverQuery = req.URL.RawQuery
					body := `{"results":[` +
						`{"id":201,"title":"Movie 201","release_date":"2020-01-01","original_language":"ja","popularity":50},` +
						`{"id":202,"title":"Movie 202","release_date":"2020-01-01","original_language":"ja","popularity":49},` +
						`{"id":203,"title":"Movie 203","release_date":"2020-01-01","original_language":"ja","popularity":48},` +
						`{"id":204,"title":"Movie 204","release_date":"2020-01-01","original_language":"ja","popularity":47},` +
						`{"id":205,"title":"Movie 205","release_date":"2020-01-01","original_language":"ja","popularity":46}` +
						`],"total_results":5}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				default:
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Status:     "404 Not Found",
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Header:     make(http.Header),
					}, nil
				}
			}),
		}, cache),
	}

	titles, err := svc.Similar(context.Background(), "movie", 123)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(titles) != 5 {
		t.Fatalf("len(titles) = %d, want 5", len(titles))
	}
	if !strings.Contains(discoverQuery, "with_original_language=ja") ||
		strings.Contains(discoverQuery, "with_original_language=ko") {
		t.Fatalf("similar query did not use metadata language: %s", discoverQuery)
	}
}

// TestExtractTitleFields verifies that extractTitleFields copies only requested fields.
func TestExtractTitleFields(t *testing.T) {
	full := &models.Title{
		ID:              "tvdb:series:123",
		Name:            "Test Show",
		Overview:        "A great show",
		Year:            2020,
		Language:        "eng",
		MediaType:       "series",
		TVDBID:          123,
		IMDBID:          "tt0000123",
		TMDBID:          456,
		Genres:          []string{"Drama", "Action"},
		Status:          models.SeriesReleaseStatusReleased,
		LifecycleStatus: "Continuing",
		Network:         "HBO",
		Certification:   "TV-MA",
		Popularity:      85.5,
		Poster:          &models.Image{URL: "https://example.com/poster.jpg", Type: "poster"},
		TextPoster:      &models.Image{URL: "https://example.com/text-poster.jpg", Type: "poster"},
		Backdrop:        &models.Image{URL: "https://example.com/backdrop.jpg", Type: "backdrop"},
		TextBackdrop:    &models.Image{URL: "https://example.com/text-backdrop.jpg", Type: "backdrop"},
		Backdrops:       []models.Image{{URL: "https://example.com/alt-backdrop.jpg", Type: "backdrop"}},
		Logo:            &models.Image{URL: "https://example.com/logo.png", Type: "logo"},
		Ratings:         []models.Rating{{Source: "imdb", Value: 8.5, Max: 10}},
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
			name:   "image aliases",
			fields: []string{"images"},
			check: func(t *testing.T, out models.Title) {
				if out.Poster == nil || out.Poster.URL != "https://example.com/poster.jpg" {
					t.Fatalf("expected poster image, got %#v", out.Poster)
				}
				if out.TextPoster == nil || out.TextPoster.URL != "https://example.com/text-poster.jpg" {
					t.Fatalf("expected text poster image, got %#v", out.TextPoster)
				}
				if out.Backdrop == nil || out.Backdrop.URL != "https://example.com/backdrop.jpg" {
					t.Fatalf("expected backdrop image, got %#v", out.Backdrop)
				}
				if out.TextBackdrop == nil || out.TextBackdrop.URL != "https://example.com/text-backdrop.jpg" {
					t.Fatalf("expected text backdrop image, got %#v", out.TextBackdrop)
				}
				if len(out.Backdrops) != 1 {
					t.Fatalf("expected alternate backdrops, got %#v", out.Backdrops)
				}
				if out.Logo == nil || out.Logo.URL != "https://example.com/logo.png" {
					t.Fatalf("expected logo image, got %#v", out.Logo)
				}
			},
		},
		{
			name:   "text poster and logo aliases",
			fields: []string{"textPoster", "logo"},
			check: func(t *testing.T, out models.Title) {
				if out.TextPoster == nil || out.TextPoster.URL != "https://example.com/text-poster.jpg" {
					t.Fatalf("expected text poster image, got %#v", out.TextPoster)
				}
				if out.Logo == nil || out.Logo.URL != "https://example.com/logo.png" {
					t.Fatalf("expected logo image, got %#v", out.Logo)
				}
				if out.Poster != nil || out.Backdrop != nil {
					t.Fatalf("did not expect poster/backdrop, got poster=%#v backdrop=%#v", out.Poster, out.Backdrop)
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
			fields: []string{"overview", "year", "genres", "status", "lifecycleStatus", "network", "certification", "language", "popularity", "ratings"},
			check: func(t *testing.T, out models.Title) {
				if out.Overview != "A great show" {
					t.Errorf("expected overview, got %q", out.Overview)
				}
				if out.Year != 2020 {
					t.Errorf("expected year 2020, got %d", out.Year)
				}
				if out.Status != models.SeriesReleaseStatusReleased {
					t.Errorf("expected status released, got %q", out.Status)
				}
				if out.LifecycleStatus != "Continuing" {
					t.Errorf("expected lifecycle status Continuing, got %q", out.LifecycleStatus)
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

func TestAggregateTopTen_DailyPopularitySourceSurfacesProviderGap(t *testing.T) {
	currentYear := time.Now().Year()
	spiderNoir := models.TrendingItem{Rank: 3, Title: models.Title{
		Name: "Spider-Noir", MediaType: "series", IMDBID: "tt30460310", Year: currentYear,
	}}
	providerLeader := models.TrendingItem{Rank: 1, Title: models.Title{
		Name: "Catalog Provider Leader", MediaType: "series", IMDBID: "tt3000001", Year: currentYear - 8,
	}}

	sources := []topTenSource{
		{name: "justwatch-us-shows", weight: 8.0, items: []models.TrendingItem{
			{Rank: 1, Title: models.Title{Name: "Daily 1", MediaType: "series", IMDBID: "tt3000002", Year: currentYear - 8}},
			{Rank: 2, Title: models.Title{Name: "Daily 2", MediaType: "series", IMDBID: "tt3000003", Year: currentYear - 8}},
			spiderNoir,
		}},
		{name: "provider-shows", weight: 5.0, items: []models.TrendingItem{providerLeader}},
	}

	results := aggregateTopTen(sources, 10)
	if len(results) < 2 {
		t.Fatalf("expected multiple results, got %d", len(results))
	}
	var spiderRank, providerRank int
	for i, item := range results {
		switch item.Title.IMDBID {
		case "tt30460310":
			spiderRank = i + 1
		case "tt3000001":
			providerRank = i + 1
		}
	}
	if spiderRank == 0 || providerRank == 0 {
		t.Fatalf("expected both target items in results, got spiderRank=%d providerRank=%d", spiderRank, providerRank)
	}
	if spiderRank >= providerRank {
		t.Fatalf("expected current daily-popularity show to outrank one-list provider item, got spiderRank=%d providerRank=%d", spiderRank, providerRank)
	}
}

func TestTopTenTVEpisodeRecencyMultiplier_RecentEpisodeWins(t *testing.T) {
	now := time.Now()
	details := models.SeriesDetails{
		Title: models.Title{
			Name:            "Weekly Hit",
			MediaType:       "series",
			Status:          models.SeriesReleaseStatusReleased,
			LifecycleStatus: "Continuing",
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
			Name:            "Archive Show",
			MediaType:       "series",
			Status:          models.SeriesReleaseStatusReleased,
			LifecycleStatus: "Ended",
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

func TestEnsureMovieReleasePointersPrefersReleasedPhysicalOverFutureDigital(t *testing.T) {
	now := time.Now()
	title := models.Title{
		MediaType: "movie",
		Releases: []models.Release{
			{Type: "digital", Date: now.AddDate(0, 1, 0).Format("2006-01-02")},
			{Type: "physical", Date: now.AddDate(0, 0, -1).Format("2006-01-02")},
		},
	}

	service := &Service{}
	service.ensureMovieReleasePointers(&title)

	if title.HomeRelease == nil || title.HomeRelease.Type != "physical" {
		t.Fatalf("home release = %#v, want released physical window", title.HomeRelease)
	}
	if title.Status != models.MovieReleaseStatusReleased {
		t.Fatalf("status = %q, want %q", title.Status, models.MovieReleaseStatusReleased)
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

func TestMovieDetailsCacheHitHydratesRatingsFromRatingsCache(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		client:       &tvdbClient{language: "eng"},
		cache:        newFileCache(tempDir+"/metadata", 24),
		ratingsCache: newFileCache(tempDir+"/ratings", 24),
		mdblist:      newMDBListClient("test-key", []string{"tomatoes", "audience"}, true, 24),
	}

	cacheID := cacheKey("tvdb", "movie", "details", "v6", "eng", "369859")
	if err := svc.cache.set(cacheID, models.Title{
		ID:            "tvdb:movie:369859",
		Name:          "Primate",
		MediaType:     "movie",
		TVDBID:        369859,
		IMDBID:        "tt33028778",
		Certification: "R",
	}); err != nil {
		t.Fatalf("set movie cache: %v", err)
	}

	if err := svc.ratingsCache.set(ratingsDiskCacheKey("tt33028778", "movie"), []models.Rating{
		{Source: "imdb", Value: 5.8, Max: 10},
		{Source: "tomatoes", Value: 78, Max: 100},
		{Source: "audience", Value: 70, Max: 100},
	}); err != nil {
		t.Fatalf("set ratings cache: %v", err)
	}

	title, err := svc.MovieDetails(context.Background(), models.MovieDetailsQuery{
		TitleID: "tvdb:movie:369859",
		IMDBID:  "tt33028778",
	})
	if err != nil {
		t.Fatalf("MovieDetails: %v", err)
	}
	if len(title.Ratings) != 2 {
		t.Fatalf("expected 2 enabled ratings, got %#v", title.Ratings)
	}
	if title.Ratings[0].Source != "tomatoes" || title.Ratings[1].Source != "audience" {
		t.Fatalf("expected RT ratings only, got %#v", title.Ratings)
	}
}

func TestSeriesDetailsCacheHitHydratesRatingsFromRatingsCache(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		client:       &tvdbClient{language: "eng"},
		cache:        newFileCache(tempDir+"/metadata", 24),
		ratingsCache: newFileCache(tempDir+"/ratings", 24),
		mdblist:      newMDBListClient("test-key", []string{"tomatoes", "audience"}, true, 24),
	}

	cacheID := seriesDetailsCacheKey("eng", 75805, "")
	if err := svc.cache.set(cacheID, models.SeriesDetails{
		Title: models.Title{
			ID:        "tvdb:series:75805",
			Name:      "It's Always Sunny in Philadelphia",
			MediaType: "series",
			TVDBID:    75805,
			IMDBID:    "tt0472954",
			Poster:    &models.Image{URL: "https://example.com/poster.jpg"},
			Backdrop:  &models.Image{URL: "https://example.com/backdrop.jpg"},
		},
		Seasons: []models.SeriesSeason{{Number: 1, Name: "Season 1"}},
	}); err != nil {
		t.Fatalf("set series cache: %v", err)
	}

	if err := svc.ratingsCache.set(ratingsDiskCacheKey("tt0472954", "show"), []models.Rating{
		{Source: "imdb", Value: 8.8, Max: 10},
		{Source: "tomatoes", Value: 94, Max: 100},
		{Source: "audience", Value: 91, Max: 100},
	}); err != nil {
		t.Fatalf("set ratings cache: %v", err)
	}

	details, err := svc.SeriesDetails(context.Background(), models.SeriesDetailsQuery{
		TitleID: "tvdb:series:75805",
	})
	if err != nil {
		t.Fatalf("SeriesDetails: %v", err)
	}
	if len(details.Title.Ratings) != 2 {
		t.Fatalf("expected 2 enabled ratings, got %#v", details.Title.Ratings)
	}
	if details.Title.Ratings[0].Source != "tomatoes" || details.Title.Ratings[1].Source != "audience" {
		t.Fatalf("expected RT ratings only, got %#v", details.Title.Ratings)
	}
}

func TestSeriesDetailsParsesTMDBTitleIDBeforeResolvingTVDB(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		client:           &tvdbClient{language: "eng"},
		cache:            newFileCache(tempDir+"/metadata", 24),
		inflightRequests: make(map[string]*inflightRequest),
	}

	if err := svc.cache.set(seriesTVDBResolutionCacheKey(82728), int64(353546)); err != nil {
		t.Fatalf("set tmdb resolve cache: %v", err)
	}
	if err := svc.cache.set(seriesDetailsCacheKey("eng", 353546, ""), models.SeriesDetails{
		Title: models.Title{
			ID:        "tvdb:series:353546",
			Name:      "Bluey",
			MediaType: "series",
			TVDBID:    353546,
			TMDBID:    82728,
			IMDBID:    "tt7678620",
			Year:      2018,
			Poster:    &models.Image{URL: "https://example.com/bluey-poster.jpg"},
			Backdrop:  &models.Image{URL: "https://example.com/bluey-backdrop.jpg"},
		},
		Seasons: []models.SeriesSeason{{Number: 1, Episodes: []models.SeriesEpisode{{SeasonNumber: 1, EpisodeNumber: 19}}}},
	}); err != nil {
		t.Fatalf("set series cache: %v", err)
	}

	details, err := svc.SeriesDetails(context.Background(), models.SeriesDetailsQuery{
		TitleID: "tmdb:tv:82728",
		Name:    "Bluey",
	})
	if err != nil {
		t.Fatalf("SeriesDetails: %v", err)
	}
	if details.Title.TVDBID != 353546 || details.Title.TMDBID != 82728 || details.Title.Year != 2018 {
		t.Fatalf("expected TMDB title ID to resolve to modern Bluey, got tvdb=%d tmdb=%d year=%d",
			details.Title.TVDBID, details.Title.TMDBID, details.Title.Year)
	}
}

func TestGetCacheManagerStatusCountsCurrentShelfSourceCache(t *testing.T) {
	tempDir := t.TempDir()
	svc := &Service{
		cache:  newFileCache(tempDir, 24),
		client: &tvdbClient{language: "eng"},
	}
	svc.customListInfoFn = func() []CustomListInfo {
		return []CustomListInfo{{URL: "https://mdblist.com/lists/test/list/json", Name: "Test List"}}
	}

	cacheID := customListSourceCacheKey("public", "https://mdblist.com/lists/test/list/json")
	if err := svc.cache.set(cacheID, []mdblistItem{{Rank: 1, Title: "Cached"}}); err != nil {
		t.Fatalf("set custom list cache: %v", err)
	}

	status := svc.GetCacheManagerStatus()
	if status.CustomListsCached != 1 {
		t.Fatalf("expected 1 cached custom list, got %d", status.CustomListsCached)
	}
}

func TestGetTopTenListSourceUsesSourceCache(t *testing.T) {
	const listURL = "https://mdblist.com/lists/test/provider/json"
	requests := 0
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	svc := &Service{
		client: newTVDBClient("test-api-key", "eng", httpc, 24),
		cache:  newFileCache(t.TempDir(), 24),
	}
	cachedItems := []models.TrendingItem{{Rank: 1, Title: models.Title{Name: "Cached", MediaType: "movie"}}}
	if err := svc.cache.set(topTenListSourceCacheKey(listURL, 10, "eng"), topTenListSourceCacheEntry{Items: cachedItems}); err != nil {
		t.Fatalf("set top ten source cache: %v", err)
	}

	items, err := svc.getTopTenListSource(context.Background(), listURL, 10)
	if err != nil {
		t.Fatalf("getTopTenListSource: %v", err)
	}
	if requests != 0 {
		t.Fatalf("expected cached source to avoid HTTP requests, got %d", requests)
	}
	if len(items) != 1 || items[0].Title.Name != "Cached" {
		t.Fatalf("unexpected cached items: %#v", items)
	}
}

func TestTMDBTrendingDailyFiltersVideosAndAdultTitles(t *testing.T) {
	var requestedPath string
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedPath = req.URL.Path
		body := `{"results":[
			{"id":1,"title":"Movie One","original_title":"Original One","release_date":"2026-08-20","original_language":"en","genre_ids":[28],"popularity":12.5,"vote_count":44,"poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg","adult":false,"video":false},
			{"id":2,"title":"Promotional Video","release_date":"2026-08-21","adult":false,"video":true},
			{"id":3,"title":"Adult Movie","release_date":"2026-08-22","adult":true,"video":false}
		]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	client := newTMDBClient("test-key", "eng", httpc, nil)

	items, err := client.trendingDaily(context.Background(), "movie")
	if err != nil {
		t.Fatalf("trendingDaily: %v", err)
	}
	if requestedPath != "/3/trending/movie/day" {
		t.Fatalf("requested path = %q, want TMDB daily movie chart", requestedPath)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want only the eligible movie: %#v", len(items), items)
	}
	item := items[0]
	if item.Rank != 1 || item.Title.ID != "tmdb:movie:1" || item.Title.Name != "Movie One" || item.Title.OriginalName != "Original One" {
		t.Fatalf("unexpected mapped item: %#v", item)
	}
	if item.Title.Year != 2026 || item.Title.Status != models.MovieReleaseStatusTheatrical || item.Title.Popularity != 12.5 || item.Title.VoteCount != 44 {
		t.Fatalf("unexpected mapped metadata: %#v", item.Title)
	}
	if len(item.Title.Genres) != 1 || item.Title.Genres[0] != "Action" || item.Title.Poster == nil || item.Title.Backdrop == nil {
		t.Fatalf("expected genres and artwork, got %#v", item.Title)
	}
}

func TestSelectDailyTopTenInterleavesFiveMoviesAndFiveShows(t *testing.T) {
	items := make([]models.TrendingItem, 0, 14)
	for i := 1; i <= 7; i++ {
		items = append(items,
			models.TrendingItem{Title: models.Title{Name: fmt.Sprintf("Movie %d", i), MediaType: "movie"}},
			models.TrendingItem{Title: models.Title{Name: fmt.Sprintf("Show %d", i), MediaType: "series"}},
		)
	}

	selected := selectDailyTopTen(items, "all")
	if len(selected) != 10 {
		t.Fatalf("selected = %d items, want 10", len(selected))
	}
	for i, item := range selected {
		position := i/2 + 1
		wantName := fmt.Sprintf("Movie %d", position)
		if i%2 == 1 {
			wantName = fmt.Sprintf("Show %d", position)
		}
		if item.Title.Name != wantName || item.Rank != i+1 {
			t.Fatalf("selected[%d] = name %q rank %d, want name %q rank %d", i, item.Title.Name, item.Rank, wantName, i+1)
		}
	}
}

func TestFilterReleasedDailyTrendingMoviesRequiresReleasedHomeWindow(t *testing.T) {
	past := time.Now().AddDate(0, 0, -2).UTC().Format(time.RFC3339)
	future := time.Now().AddDate(0, 0, 2).UTC().Format(time.RFC3339)
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		movieID := parts[len(parts)-2]
		releaseType := 4
		releaseDate := past
		switch movieID {
		case "2":
			releaseType = 3
		case "3":
			releaseDate = future
		}
		body := fmt.Sprintf(`{"results":[{"iso_3166_1":"US","release_dates":[{"type":%d,"release_date":%q}]}]}`, releaseType, releaseDate)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	svc := &Service{
		tmdb:  newTMDBClient("test-key", "eng", httpc, nil),
		cache: newFileCache(t.TempDir(), 24),
	}
	items := []models.TrendingItem{
		{Title: models.Title{TMDBID: 1, Name: "Released Digital", MediaType: "movie"}},
		{Title: models.Title{TMDBID: 2, Name: "Theatrical Only", MediaType: "movie"}},
		{Title: models.Title{TMDBID: 3, Name: "Future Digital", MediaType: "movie"}},
	}

	released := svc.filterReleasedDailyTrendingMovies(context.Background(), items)
	if len(released) != 1 || released[0].Title.Name != "Released Digital" || released[0].Title.Status != models.MovieReleaseStatusReleased {
		t.Fatalf("released items = %#v, want only the completed digital release", released)
	}
}

func TestGetTopTenUsesCachedTMDBDailyCandidates(t *testing.T) {
	var movieRequests, tvRequests, imageRequests atomic.Int64
	httpc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/release_dates") {
			body := `{"results":[{"iso_3166_1":"US","release_dates":[{"type":4,"release_date":"2020-01-02T00:00:00Z"}]}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if strings.HasSuffix(req.URL.Path, "/images") {
			imageRequests.Add(1)
			body := `{"backdrops":[{"file_path":"/primary.jpg","vote_average":9},{"file_path":"/alternate-1.jpg","vote_average":8},{"file_path":"/alternate-2.jpg","vote_average":7}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		if req.URL.Host == "image.tmdb.org" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		mediaType := "movie"
		name := "Movie"
		dateField := "release_date"
		if strings.Contains(req.URL.Path, "/tv/") {
			mediaType = "tv"
			name = "Show"
			dateField = "first_air_date"
			tvRequests.Add(1)
		} else {
			movieRequests.Add(1)
		}
		results := make([]map[string]any, 20)
		for i := range results {
			entry := map[string]any{
				"id":                i + 1,
				"original_language": "en",
				dateField:           "2020-01-01",
			}
			if mediaType == "movie" {
				entry["title"] = fmt.Sprintf("%s %d", name, i+1)
			} else {
				entry["name"] = fmt.Sprintf("%s %d", name, i+1)
			}
			results[i] = entry
		}
		body, _ := json.Marshal(map[string]any{"results": results})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	tmdb := newTMDBClient("test-key", "eng", httpc, nil)
	svc := &Service{
		client: &tvdbClient{language: "eng"},
		tmdb:   tmdb,
		cache:  newFileCache(t.TempDir(), 24),
	}

	for call := 0; call < 2; call++ {
		items, err := svc.GetTopTen(context.Background(), "all", nil)
		if err != nil {
			t.Fatalf("GetTopTen call %d: %v", call+1, err)
		}
		if len(items) != 10 || items[0].Title.Name != "Movie 1" || items[1].Title.Name != "Show 1" || items[9].Title.Name != "Show 5" {
			t.Fatalf("unexpected daily top ten: %#v", items)
		}
		for _, item := range items {
			if len(item.Title.Backdrops) < 2 {
				t.Fatalf("top ten artwork for %q has %d alternate backdrops, want at least 2", item.Title.Name, len(item.Title.Backdrops))
			}
		}
	}
	if movieRequests.Load() != 1 || tvRequests.Load() != 1 {
		t.Fatalf("TMDB requests = movies %d tv %d, want one request per chart after cache hit", movieRequests.Load(), tvRequests.Load())
	}
	if imageRequests.Load() != 40 {
		t.Fatalf("TMDB image requests = %d, want one per cached daily candidate", imageRequests.Load())
	}
}

func TestIsDateBasedSeriesClassification(t *testing.T) {
	tests := map[string]bool{
		"talk_show":  true,
		"Talk Show":  true,
		"news":       true,
		"game-show":  true,
		"Soap":       true,
		"soap_opera": true,
		"Drama":      false,
		"series":     false,
	}

	for classification, want := range tests {
		if got := isDateBasedSeriesClassification(classification); got != want {
			t.Errorf("isDateBasedSeriesClassification(%q) = %v, want %v", classification, got, want)
		}
	}
}

func TestApplyDateBasedSeriesClassificationFromFinalGenres(t *testing.T) {
	title := models.Title{Genres: []string{"Reality", "Game Show", "Family"}}
	changed, genre := applyDateBasedSeriesClassification(&title)
	if !changed || !title.IsDaily || genre != "Game Show" {
		t.Fatalf("classification = changed:%v daily:%v genre:%q", changed, title.IsDaily, genre)
	}
}

package metadata

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novastream/models"
)

func curatedMovies(ids ...int64) []CuratedItem {
	items := make([]CuratedItem, len(ids))
	for i, id := range ids {
		items[i] = CuratedItem{TMDBID: id, IMDBID: fmt.Sprintf("tt%d", id), MediaType: "movie", Title: fmt.Sprintf("Movie %d", id)}
	}
	return items
}

func TestCuratedListReusesCompletedTitlesAcrossLists(t *testing.T) {
	svc, counts := shelfTestService(t)
	if _, err := svc.GetCuratedList(context.Background(), curatedMovies(1, 2), "first"); err != nil {
		t.Fatal(err)
	}
	before := counts()
	second, err := svc.GetCuratedList(context.Background(), curatedMovies(2, 1, 3), "second")
	if err != nil || len(second) != 3 {
		t.Fatalf("second=%d err=%v", len(second), err)
	}
	after := counts()
	for _, path := range []string{"/3/movie/1", "/3/movie/2"} {
		if after[path] != before[path] {
			t.Fatalf("completed title %s re-enriched: before=%v after=%v", path, before, after)
		}
	}
	if after["/3/movie/3"] == 0 {
		t.Fatalf("new title not enriched: %v", after)
	}
	if second[0].Title.TMDBID != 2 || second[0].Rank != 0 || second[0].Title.Popularity != 100 {
		t.Fatalf("reused title keeps the other list's rank: %+v", second[0])
	}
	// The cards phase serves the completed title too.
	cards, err := svc.GetShelfCards(context.Background(), curatedMovies(1))
	if err != nil || cards[0].Title.Certification != "PG" {
		t.Fatalf("card did not use completed title: %+v err=%v", cards, err)
	}
}

func TestCuratedListEnrichmentSurvivesClientCancel(t *testing.T) {
	svc, counts := shelfTestService(t)
	release := make(chan struct{})
	inner := svc.tmdb.httpc.Transport
	var started atomic.Int32
	blocking := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		started.Add(1)
		<-release
		return inner.RoundTrip(r)
	})}
	svc.tmdb.httpc = blocking
	items := curatedMovies(11, 12)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := svc.GetCuratedList(ctx, items, "cancel")
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled request should return its context error")
	}
	close(release)

	pageKey := svc.curatedListCacheID(items)
	for time.Now().Before(deadline) {
		var cached []models.TrendingItem
		if ok, _ := svc.cache.get(pageKey, &cached); ok && len(cached) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := counts()
	again, err := svc.GetCuratedList(context.Background(), items, "cancel")
	if err != nil || len(again) != 2 || again[0].Title.Poster == nil {
		t.Fatalf("detached pass did not cache the page: %+v err=%v", again, err)
	}
	if fmt.Sprint(counts()) != fmt.Sprint(before) {
		t.Fatalf("cached page issued requests: before=%v after=%v", before, counts())
	}
}

func TestCuratedListCatalogMetadataOverlayStaysOutOfSharedCaches(t *testing.T) {
	svc, _ := shelfTestService(t)
	catalog := curatedMovies(21)
	catalog[0].CatalogMetadata = true
	catalog[0].Title = "Catalog Name"
	catalog[0].PosterURL = "https://rpdb.example/poster.jpg"
	catalog[0].BackdropURL = "https://addon.example/bg.jpg"
	catalog[0].Overview = "Catalog overview"
	catalog[0].IMDBRating = 7.5
	got, err := svc.CompleteShelfCards(context.Background(), catalog, "stremio")
	if err != nil || len(got) != 1 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	title := got[0].Title
	if title.Name != "Catalog Name" || title.Poster.URL != catalog[0].PosterURL || title.Backdrop.URL != catalog[0].BackdropURL || title.Overview != "Catalog overview" {
		t.Fatalf("catalog metadata not kept: %+v", title)
	}
	if title.Certification != "PG" || len(title.Ratings) != 1 || title.Ratings[0].Source != "imdb" || title.Ratings[0].Value != 7.5 {
		t.Fatalf("enrichment fields missing: cert=%q ratings=%+v", title.Certification, title.Ratings)
	}
	// The same title on a plain list shows provider artwork, not the catalog's.
	plain, err := svc.GetCuratedList(context.Background(), curatedMovies(21), "plain")
	if err != nil || plain[0].Title.Poster == nil || strings.Contains(plain[0].Title.Poster.URL, "rpdb") || plain[0].Title.Name == "Catalog Name" {
		t.Fatalf("catalog artwork leaked: %+v err=%v", plain, err)
	}
	cards, _ := svc.GetShelfCards(context.Background(), curatedMovies(21))
	if strings.Contains(cards[0].Title.Poster.URL, "rpdb") {
		t.Fatalf("catalog artwork leaked into shared cards: %+v", cards[0].Title.Poster)
	}
}

func TestMDBListQuotaLowFallsBackToPublicJSON(t *testing.T) {
	listURL := "https://mdblist.com/lists/someone/quota-" + t.Name() + "/json"
	apiCalls := 0
	svc, _ := mdblistAPITestService(t, func(r *http.Request) *http.Response {
		apiCalls++
		return jsonResponse(500, "unused", nil)
	})
	svc.client.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":7,"title":"Public","imdb_id":"tt7","mediatype":"movie"}]`, nil), nil
	})}
	header := make(http.Header)
	header.Set("X-RateLimit-Limit", "1000")
	header.Set("X-RateLimit-Remaining", "50")
	header.Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	svc.mdblist.quota.record(header, time.Now())

	items, err := svc.GetCustomListSource(context.Background(), listURL)
	if err != nil || len(items) != 1 || items[0].Title != "Public" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if apiCalls != 0 {
		t.Fatalf("API called with low quota: %d", apiCalls)
	}
	if svc.mdblist.quota.low(time.Now().Add(2 * time.Hour)) {
		t.Fatal("quota should recover after reset")
	}
}

func TestMDBListQuotaHeaderParsing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var q mdblistQuota
	header := make(http.Header)
	header.Set("X-RateLimit-Limit", "10000")
	header.Set("X-RateLimit-Remaining", "5000")
	q.record(header, now)
	if q.low(now) {
		t.Fatal("half the quota is not low")
	}
	header.Set("X-RateLimit-Remaining", "999")
	header.Set("X-RateLimit-Reset", "120")
	q.record(header, now)
	if !q.low(now) || q.resetAt != now.Add(2*time.Minute) {
		t.Fatalf("low quota not detected or reset misparsed: %+v", q.resetAt)
	}
	if q.low(now.Add(3 * time.Minute)) {
		t.Fatal("quota stays low after reset")
	}
	var missing mdblistQuota
	missing.record(make(http.Header), now)
	if missing.low(now) {
		t.Fatal("responses without quota headers must not block the API")
	}
	if customListSourceTTL("api") != time.Hour || customListSourceTTL("public") != shelfSourceTTL {
		t.Fatal("source TTLs changed")
	}
}

func TestShelfArtworkSkipsImageAnalysis(t *testing.T) {
	var mu sync.Mutex
	var imageDownloads int
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.themoviedb.org" {
			mu.Lock()
			imageDownloads++
			mu.Unlock()
			return jsonResponse(404, "", nil), nil
		}
		return jsonResponse(200, `{"logos":[{"file_path":"/logo.png","iso_639_1":"en","vote_average":5}],
			"backdrops":[{"file_path":"/b1.jpg","vote_average":1},{"file_path":"/b2.jpg","vote_average":9},{"file_path":"/b3.jpg","vote_average":5}],
			"posters":[]}`, nil), nil
	})}
	cache := newFileCache(t.TempDir(), 24)
	svc := &Service{client: newTVDBClient("", "eng", client, 24), tmdb: newTMDBClient("key", "eng", client, cache), cache: cache}
	svc.tmdb.minInterval = 0
	items := []models.TrendingItem{{Title: models.Title{TMDBID: 5, MediaType: "movie"}}}
	svc.enrichShelfArtwork(context.Background(), items, 1)
	if imageDownloads != 0 {
		t.Fatalf("shelf artwork downloaded %d images", imageDownloads)
	}
	title := items[0].Title
	if title.Logo == nil || title.Backdrop == nil || !strings.HasSuffix(title.Backdrop.URL, "/b2.jpg") {
		t.Fatalf("shelf artwork: logo=%+v backdrop=%+v", title.Logo, title.Backdrop)
	}
	if len(title.Backdrops) != 2 || !strings.HasSuffix(title.Backdrops[0].URL, "/b3.jpg") {
		t.Fatalf("hero rotation backdrops not vote-ranked: %+v", title.Backdrops)
	}
	// An analyzed entry (details/hero) supersedes the shelf variant.
	analyzedLogo := &models.Image{URL: "analyzed", IsDark: true}
	_ = cache.set(cacheKey("tmdb", "images", "v10", "eng", "movie", "5"), tmdbImagesResult{LogoSelectionVersion: 1, Logo: analyzedLogo})
	items = []models.TrendingItem{{Title: models.Title{TMDBID: 5, MediaType: "movie"}}}
	svc.enrichShelfArtwork(context.Background(), items, 1)
	if items[0].Title.Logo == nil || !items[0].Title.Logo.IsDark {
		t.Fatalf("analyzed entry not preferred: %+v", items[0].Title.Logo)
	}
}

func TestMovieReleasesMissUsesMovieDetails(t *testing.T) {
	svc, counts := shelfTestService(t)
	title := models.Title{MediaType: "movie"}
	if !svc.enrichMovieReleases(context.Background(), &title, 31) || title.Certification != "PG" {
		t.Fatalf("releases not applied: %+v", title)
	}
	got := counts()
	if got["/3/movie/31"] != 1 || got["/3/movie/31/release_dates"] != 0 || len(got) != 1 {
		t.Fatalf("unexpected requests: %v", got)
	}
}

func TestBatchMovieReleasesHonoursNegativeCache(t *testing.T) {
	svc, counts := shelfTestService(t)
	_ = svc.cache.set(cacheKey("tmdb", "movie", "releases", "v3", "32"), cachedReleasesWithCert{})
	results := svc.BatchMovieReleases(context.Background(), []models.BatchMovieReleasesQuery{{TMDBID: 32}})
	if len(counts()) != 0 || results[0].Status != models.MovieReleaseStatusUnknown {
		t.Fatalf("negative cache refetched: %v %+v", counts(), results)
	}
}

func TestTVContentRatingReadsCachedSeriesDetails(t *testing.T) {
	svc, _ := shelfTestService(t)
	var requests []string
	svc.tmdb.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		return jsonResponse(200, `{"id":41,"name":"Show","poster_path":"/p.jpg","first_air_date":"2020-01-01","external_ids":{"imdb_id":"tt41"},"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-14"}]}}`, nil), nil
	})}
	title := models.Title{MediaType: "series"}
	if !svc.enrichTVContentRating(context.Background(), &title, 41) || title.Certification != "TV-14" {
		t.Fatalf("certification = %q", title.Certification)
	}
	if imdb := svc.getIMDBIDForTMDB(context.Background(), "tv", 41); imdb != "tt41" {
		t.Fatalf("imdb = %q", imdb)
	}
	if details, err := svc.tmdb.seriesDetails(context.Background(), 41); err != nil || details.Status != models.SeriesReleaseStatusReleased {
		t.Fatalf("details=%+v err=%v", details, err)
	}
	if strings.Join(requests, ",") != "/3/tv/41" {
		t.Fatalf("requests = %v, want one cached series details call", requests)
	}
}

func TestIMDBIDForTMDBMovieUsesDetails(t *testing.T) {
	svc, _ := shelfTestService(t)
	svc.tmdb.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"id":51,"title":"Movie","imdb_id":"tt51"}`, nil), nil
	})}
	if got := svc.getIMDBIDForTMDB(context.Background(), "movie", 51); got != "tt51" {
		t.Fatalf("imdb = %q", got)
	}
	if details, err := svc.tmdb.movieDetails(context.Background(), 51); err != nil || details.IMDBID != "tt51" {
		t.Fatalf("details not cached by the lookup: %+v %v", details, err)
	}
}

func TestTitleSeedInfoSingleRequest(t *testing.T) {
	var requests []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path+"?"+r.URL.Query().Get("append_to_response"))
		return jsonResponse(200, `{"genres":[{"id":18}],"original_language":"en","first_air_date":"2019-05-01","keywords":{"results":[{"id":7},{"id":9}]}}`, nil), nil
	})}
	c := newTMDBClient("key", "en", client, nil)
	c.minInterval = 0
	info, err := c.fetchTitleSeedInfo(context.Background(), "series", 61)
	if err != nil || len(info.GenreIDs) != 1 || fmt.Sprint(info.KeywordIDs) != "[7 9]" || info.Year != 2019 || info.OriginalLanguage != "en" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if strings.Join(requests, ",") != "/3/tv/61?keywords" {
		t.Fatalf("requests = %v", requests)
	}
}

func tvdbTestService(t *testing.T, handler func(*http.Request) *http.Response) *Service {
	t.Helper()
	httpc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return handler(r), nil
	})}
	svc := &Service{client: newTVDBClient("tvdb-key", "eng", httpc, 24), cache: newFileCache(t.TempDir(), 24)}
	svc.client.token = "token"
	svc.client.tokenExpiry = time.Now().Add(time.Hour)
	svc.client.minInterval = 0
	return svc
}

func TestTVDBExtendedMetaNormalizedAndTranslationsEmbedded(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	svc := tvdbTestService(t, func(r *http.Request) *http.Response {
		mu.Lock()
		requests = append(requests, r.URL.Path+"?meta="+r.URL.Query().Get("meta"))
		mu.Unlock()
		return jsonResponse(200, `{"data":{"id":70,"name":"Original","score":12,"translations":{
			"nameTranslations":[{"language":"eng","name":"English Name"}],
			"overviewTranslations":[{"language":"eng","overview":"English overview"}]}}}`, nil)
	})
	for _, meta := range [][]string{{"artworks"}, {"trailers"}, nil} {
		if _, err := svc.cachedSeriesExtended(70, meta); err != nil {
			t.Fatal(err)
		}
	}
	trans, err := svc.cachedSeriesTranslations(70, "eng")
	if err != nil || trans.Name != "English Name" || trans.Overview != "English overview" {
		t.Fatalf("translation=%+v err=%v", trans, err)
	}
	if _, err := svc.cachedSeriesTranslations(70, "deu"); err == nil {
		t.Fatal("missing language should report not found")
	}
	if _, err := svc.cachedSeriesExtended(70, []string{"episodes", "seasons", "artworks"}); err != nil {
		t.Fatal(err)
	}
	base, err := svc.getTVDBMovieDetails(70)
	if err != nil || base.Score != 12 {
		t.Fatalf("movie base=%+v err=%v", base, err)
	}
	if _, err := svc.cachedMovieTranslations(70, "eng"); err != nil {
		t.Fatal(err)
	}
	want := "/v4/series/70/extended?meta=translations,/v4/series/70/extended?meta=episodes,/v4/movies/70/extended?meta=translations"
	if strings.Join(requests, ",") != want {
		t.Fatalf("requests = %v", requests)
	}
}

func TestTVDBSearchResolvesIMDBIDThroughRemoteID(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	svc := tvdbTestService(t, func(r *http.Request) *http.Response {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/v4/search/remoteid/tt80" {
			return jsonResponse(200, `{"data":[{"series":{"id":801,"name":"Show","year":"2020","image":"https://artworks.thetvdb.com/s.jpg"}},{"movie":{"id":802,"name":"Film","year":"2021"}}]}`, nil)
		}
		return jsonResponse(200, `{"data":[]}`, nil)
	})
	for i := 0; i < 2; i++ {
		movies, err := svc.searchTVDBMovie("Wrong Title", 1999, "tt80")
		if err != nil || len(movies) != 1 || movies[0].TVDBID != "802" {
			t.Fatalf("movie=%+v err=%v", movies, err)
		}
		series, err := svc.searchTVDBSeries("Wrong Title", 0, "tt80")
		if err != nil || len(series) != 1 || series[0].TVDBID != "801" || series[0].ImageURL == "" {
			t.Fatalf("series=%+v err=%v", series, err)
		}
		if _, ok := selectCustomListTVDBSearchResult(mdblistItem{Title: "Other", IMDBID: "tt80"}, series); !ok {
			t.Fatal("remote id result rejected by the IMDb matcher")
		}
	}
	if paths["/v4/search/remoteid/tt80"] != 1 || paths["/v4/search"] != 0 {
		t.Fatalf("paths = %v", paths)
	}
	// Unknown IDs fall back to the title search and the miss is cached.
	for i := 0; i < 2; i++ {
		if _, err := svc.searchTVDBSeries("Other", 0, "tt99"); err != nil {
			t.Fatal(err)
		}
	}
	if paths["/v4/search/remoteid/tt99"] != 1 || paths["/v4/search"] != 1 {
		t.Fatalf("paths = %v", paths)
	}
}

func TestTrendingFeedCarriesTMDBID(t *testing.T) {
	svc, _ := shelfTestService(t)
	svc.client.httpc = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, `[{"id":969681,"rank":1,"title":"Movie","imdb_id":"tt22084616","mediatype":"movie","release_year":2026}]`, nil), nil
	})}
	items, err := svc.getRecentMovies()
	if err != nil || len(items) != 1 || items[0].Title.TMDBID != 969681 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

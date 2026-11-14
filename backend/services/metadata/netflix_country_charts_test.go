package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"novastream/models"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const countryChartFixture = "country_name\tcountry_iso2\tweek\tcategory\tweekly_rank\tshow_title\tseason_title\tcumulative_weeks_in_top_10\n" +
	"Germany\tDE\t2026-11-01\tFilms\t1\tOld title\t\t1\n" +
	"Germany\tDE\t2026-11-08\tFilms\t3\tThird title\t\t1\n" +
	"Germany\tDE\t2026-11-08\tFilms\t1\tFirst title\t\t1\n" +
	"Germany\tDE\t2026-11-08\tFilms\t2\tSecond title\t\t1\n" +
	"Germany\tDE\t2026-11-08\tTV\t1\tA series\tSeason 2\t1\n" +
	"Germany\tDE\t2026-11-08\tTV\t2\tA series\tSeason 1\t2\n" +
	"United Kingdom\tGB\t2026-11-08\tFilms\t1\tDifferent title\t\t1\n"

func TestNetflixCountryParserKeepsLatestRegionalRankAndSeason(t *testing.T) {
	data, err := parseNetflixCountryCharts(strings.NewReader(countryChartFixture), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	de := data["DE/movie"]
	if len(de) != 3 || de[0].Title != "First title" || de[0].Rank != 1 || de[2].Rank != 3 {
		t.Fatalf("wrong chart: %+v", de)
	}
	if data["DE/tv"][0].Season != "Season 2" || data["GB/movie"][0].Title == de[0].Title {
		t.Fatal("country or season identity lost")
	}
	if _, err = parseNetflixCountryCharts(strings.NewReader("unexpected\theader\n"), time.Now()); err == nil {
		t.Fatal("changed schema silently accepted")
	}
	conflicting := countryChartFixture + "Germany\tDE\t2026-11-08\tFilms\t1\tConflicting title\t\t1\n"
	if _, err = parseNetflixCountryCharts(strings.NewReader(conflicting), time.Now()); err == nil {
		t.Fatal("conflicting official rank accepted")
	}
}

func TestNetflixCountryCacheCoalescesAndBacksOff(t *testing.T) {
	var calls atomic.Int32
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failing.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(countryChartFixture))
	}))
	defer server.Close()
	store := &countryChartStore{client: server.Client(), endpoint: server.URL}
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.load(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cold concurrent rows fetched the dataset %d times", calls.Load())
	}
	store.mu.Lock()
	store.loadedAt = time.Now().Add(-7 * time.Hour)
	store.mu.Unlock()
	failing.Store(true)
	if data, err := store.load(context.Background()); err != nil || len(data["DE/movie"]) != 3 {
		t.Fatal("dated cached chart unavailable during outage")
	}
	if _, err := store.load(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatal("cached outage retry was not bounded")
	}
	cold := &countryChartStore{client: server.Client(), endpoint: server.URL}
	if _, err := cold.load(context.Background()); err == nil {
		t.Fatal("outage masqueraded as an empty chart")
	}
	before := calls.Load()
	if _, err := cold.load(context.Background()); err == nil || calls.Load() != before {
		t.Fatal("cold outage bypassed retry backoff")
	}
}

func TestNetflixCountryMappingDoesNotInventRanksOrGuessAmbiguousTitles(t *testing.T) {
	entries := []countryChartEntry{{Country: "DE", Week: "2026-11-08", MediaType: "tv", Rank: 1, Title: "Series", Season: "Season 2"}, {Country: "DE", Week: "2026-11-08", MediaType: "tv", Rank: 2, Title: "Series", Season: "Season 1"}, {Country: "DE", Week: "2026-11-08", MediaType: "tv", Rank: 3, Title: "Unmatched"}, {Country: "DE", Week: "2026-11-08", MediaType: "tv", Rank: 4, Title: "Other"}}
	items, err := mapCountryChartEntries(context.Background(), entries, func(_ context.Context, e countryChartEntry) (*models.Title, error) {
		if e.Title == "Unmatched" {
			return nil, errors.New("no match")
		}
		return &models.Title{ID: e.Title, Name: e.Title}, nil
	})
	if err != nil || len(items) != 2 || items[1].Rank != 4 {
		t.Fatalf("chart ranks were recomputed: %+v %v", items, err)
	}
	if !strings.Contains(items[0].Title.CardSubtitle, "2026-11-08") || !strings.Contains(items[0].Title.CardSubtitle, "Season 2") || !items[0].Title.ForceTitleOverlay {
		t.Fatal("chart provenance lost")
	}
	matches := []models.SearchResult{{Title: models.Title{Name: "Same title", TMDBID: 1}}, {Title: models.Title{Name: "Same title", TMDBID: 2}}}
	if _, err := uniqueChartTitleID("Same title", matches); err == nil {
		t.Fatal("ambiguous metadata title guessed")
	}
	if _, err := uniqueChartTitleID("Another title", matches); err == nil {
		t.Fatal("unrelated result guessed")
	}
	if id, err := uniqueChartTitleID("Same title", matches[:1]); err != nil || id != 1 {
		t.Fatal("unique exact title not resolved")
	}
}

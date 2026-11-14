package metadata

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"golang.org/x/sync/singleflight"
	"io"
	"net/http"
	"net/url"
	"novastream/models"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const netflixCountryDataURL = "https://www.netflix.com/tudum/top10/data/all-weeks-countries.tsv"
const netflixCountryDataLimit = 64 << 20

type countryChartEntry struct {
	Country, Week, MediaType, Title, Season string
	Rank                                    int
}
type countryChartSnapshot map[string][]countryChartEntry

type countryChartStore struct {
	mu                    sync.Mutex
	data                  countryChartSnapshot
	loadedAt, timeToRetry time.Time
	flight                singleflight.Group
	client                *http.Client
	endpoint              string
}

var netflixCountryCharts = &countryChartStore{client: &http.Client{Timeout: 30 * time.Second}, endpoint: netflixCountryDataURL}

func parseNetflixCountryCharts(reader io.Reader, now time.Time) (countryChartSnapshot, error) {
	csvReader := csv.NewReader(reader)
	csvReader.Comma = '\t'
	csvReader.FieldsPerRecord = -1
	header, err := csvReader.Read()
	if err != nil {
		return nil, errors.New("Netflix chart header unavailable")
	}
	columns := map[string]int{}
	for i, name := range header {
		columns[strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")] = i
	}
	for _, name := range []string{"country_iso2", "week", "category", "weekly_rank", "show_title", "season_title"} {
		if _, ok := columns[name]; !ok {
			return nil, errors.New("Netflix chart schema is unsupported")
		}
	}
	latest := map[string]string{}
	rows := map[string]map[int]countryChartEntry{}
	for count := 0; ; count++ {
		if count > 2000000 {
			return nil, errors.New("Netflix chart row limit exceeded")
		}
		row, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("Netflix chart data is malformed")
		}
		get := func(name string) string {
			index := columns[name]
			if index >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[index])
		}
		country := strings.ToUpper(get("country_iso2"))
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			continue
		}
		media := ""
		switch get("category") {
		case "Films":
			media = "movie"
		case "TV":
			media = "tv"
		default:
			continue
		}
		week := get("week")
		date, err := time.Parse("2006-01-02", week)
		if err != nil || date.After(now.Add(24*time.Hour)) {
			continue
		}
		rank, err := strconv.Atoi(get("weekly_rank"))
		if err != nil || rank < 1 || rank > 10 || get("show_title") == "" {
			continue
		}
		key := country + "/" + media
		if week < latest[key] {
			continue
		}
		if week > latest[key] {
			latest[key] = week
			rows[key] = map[int]countryChartEntry{}
		}
		entry := countryChartEntry{Country: country, Week: week, MediaType: media, Title: get("show_title"), Season: get("season_title"), Rank: rank}
		if previous, exists := rows[key][rank]; exists && previous != entry {
			return nil, errors.New("Netflix chart contains conflicting ranks")
		}
		rows[key][rank] = entry
	}
	snapshot := countryChartSnapshot{}
	for key, entries := range rows {
		for _, entry := range entries {
			snapshot[key] = append(snapshot[key], entry)
		}
		sort.Slice(snapshot[key], func(i, j int) bool { return snapshot[key][i].Rank < snapshot[key][j].Rank })
	}
	if len(snapshot) == 0 {
		return nil, errors.New("Netflix chart contains no supported country data")
	}
	return snapshot, nil
}

func (s *countryChartStore) load(ctx context.Context) (countryChartSnapshot, error) {
	s.mu.Lock()
	cached, loaded, retry := s.data, s.loadedAt, s.timeToRetry
	s.mu.Unlock()
	if time.Now().Before(retry) {
		if cached != nil {
			return cached, nil
		}
		return nil, errors.New("Netflix country charts temporarily unavailable")
	}
	if cached != nil && time.Since(loaded) < 6*time.Hour {
		return cached, nil
	}
	result := s.flight.DoChan("country-charts", func() (any, error) {
		s.mu.Lock()
		cached, loaded, retry := s.data, s.loadedAt, s.timeToRetry
		s.mu.Unlock()
		if time.Now().Before(retry) {
			if cached != nil {
				return cached, nil
			}
			return nil, errors.New("Netflix country charts temporarily unavailable")
		}
		if cached != nil && time.Since(loaded) < 6*time.Hour {
			return cached, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, s.endpoint, nil)
		if err != nil {
			return nil, err
		}
		response, err := s.client.Do(request)
		var snapshot countryChartSnapshot
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = errors.New("Netflix country charts temporarily unavailable")
			} else {
				limited := &io.LimitedReader{R: response.Body, N: netflixCountryDataLimit + 1}
				snapshot, err = parseNetflixCountryCharts(limited, time.Now())
				if limited.N <= 0 {
					err = errors.New("Netflix chart exceeds size limit")
				}
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			s.timeToRetry = time.Now().Add(5 * time.Minute)
			if s.data != nil {
				return s.data, nil
			}
			return nil, errors.New("Netflix country charts temporarily unavailable")
		}
		s.data = snapshot
		s.loadedAt = time.Now()
		s.timeToRetry = time.Time{}
		return snapshot, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-result:
		if response.Err != nil {
			return nil, response.Err
		}
		return response.Val.(countryChartSnapshot), nil
	}
}

func (s *Service) getNetflixCountryChart(ctx context.Context, opts TMDBListOptions, filters url.Values) ([]models.TrendingItem, int, error) {
	country := strings.ToUpper(strings.TrimSpace(filters.Get("watch_region")))
	if opts.SourceType != TMDBSourceCustomDiscover || filters.Get("country_chart") != "netflix" || len(country) != 2 {
		return nil, 0, fmt.Errorf("%w: Netflix charts require a country and custom-discover source", ErrInvalidTMDBList)
	}
	media := strings.ToLower(opts.MediaType)
	if media == "series" {
		media = "tv"
	}
	if media != "movie" && media != "tv" {
		return nil, 0, fmt.Errorf("%w: Netflix charts require movie or tv", ErrInvalidTMDBList)
	}
	if s.tmdb == nil || !s.tmdb.isConfigured() {
		return nil, 0, errors.New("tmdb client not configured")
	}
	snapshot, err := netflixCountryCharts.load(ctx)
	if err != nil {
		return nil, 0, err
	}
	entries := snapshot[country+"/"+media]
	if len(entries) == 0 {
		return nil, 0, errors.New("Netflix has no current chart for this country")
	}
	week, err := time.Parse("2006-01-02", entries[0].Week)
	if err != nil || time.Since(week) > 21*24*time.Hour {
		return nil, 0, errors.New("Netflix country chart is too old")
	}
	items, err := mapCountryChartEntries(ctx, entries, s.resolveNetflixChartTitle)
	if err != nil {
		return nil, 0, err
	}
	total := len(items)
	start := min(max(opts.Offset, 0), total)
	end := total
	if opts.Limit > 0 {
		end = min(start+opts.Limit, total)
	}
	return items[start:end], total, nil
}

func mapCountryChartEntries(ctx context.Context, entries []countryChartEntry, resolve func(context.Context, countryChartEntry) (*models.Title, error)) ([]models.TrendingItem, error) {
	titles := make([]*models.Title, len(entries))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for n := 0; n < min(4, len(entries)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				title, err := resolve(ctx, entries[i])
				if err == nil {
					titles[i] = title
				}
			}
		}()
	}
	for i := range entries {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := []models.TrendingItem{}
	for i, title := range titles {
		if title == nil || title.ID == "" || seen[title.ID] {
			continue
		}
		seen[title.ID] = true
		copy := *title
		entry := entries[i]
		copy.CardSubtitle = fmt.Sprintf("Netflix #%d · %s · week %s", entry.Rank, entry.Country, entry.Week)
		if entry.Season != "" {
			copy.CardSubtitle += " · " + entry.Season
		}
		copy.ForceTitleOverlay = true
		result = append(result, models.TrendingItem{Rank: entry.Rank, Title: copy})
	}
	if len(entries) > 0 && len(result) == 0 {
		return nil, errors.New("Netflix chart titles could not be matched safely")
	}
	return result, nil
}

func uniqueChartTitleID(name string, results []models.SearchResult) (int64, error) {
	id := int64(0)
	for _, result := range results {
		title := result.Title
		if title.TMDBID <= 0 || (!normalizedListTitleYearEqual(name, title.Name, 0) && !normalizedListTitleYearEqual(name, title.OriginalName, 0)) {
			continue
		}
		if id != 0 && id != title.TMDBID {
			return 0, errors.New("chart title is ambiguous")
		}
		id = title.TMDBID
	}
	if id == 0 {
		return 0, errors.New("chart title has no exact match")
	}
	return id, nil
}

func (s *Service) resolveNetflixChartTitle(ctx context.Context, entry countryChartEntry) (*models.Title, error) {
	var identity struct {
		ID int64 `json:"id"`
	}
	key := cacheKey("netflix-country-identity", "v1", entry.MediaType, entry.Title)
	err := s.fetchShelfCached(ctx, key, 6*time.Hour, &identity, func(ctx context.Context) (any, error) {
		english := newTMDBClient(s.tmdb.apiKey, "eng", s.tmdb.httpc, s.tmdb.cache)
		english.requestLimiter = s.tmdb
		matches, err := english.searchTitles(ctx, entry.Title, entry.MediaType, 20, false)
		if err != nil {
			return nil, err
		}
		id, _ := uniqueChartTitleID(entry.Title, matches)
		return struct {
			ID int64 `json:"id"`
		}{ID: id}, nil
	})
	if err != nil {
		return nil, err
	}
	if identity.ID == 0 {
		return nil, errors.New("Netflix title has no unique metadata match")
	}
	if entry.MediaType == "tv" {
		return s.tmdb.seriesDetails(ctx, identity.ID)
	}
	return s.tmdb.movieDetails(ctx, identity.ID)
}

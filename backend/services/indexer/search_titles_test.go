package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/services/debrid"
	"novastream/utils/filter"
)

func TestDevilInSilverSearchTitlesScopeParentAlias(t *testing.T) {
	opts := SearchOptions{TitleID: "tmdb:tv:323903", Query: "The Terror: Devil in Silver S01E06", MediaType: "series", Year: 2026,
		AlternateTitles: []string{"The Terror", "Devil in Silver"},
		Numbering:       &models.EpisodeNumbering{SeriesID: "tmdb:tv:323903", Ordering: "official"}}
	search, identities, _ := (&Service{}).resolveSearchTitles(t.Context(), opts, "eng", 1)
	if !reflect.DeepEqual(search, []string{"Devil in Silver"}) || !reflect.DeepEqual(identities, []string{"Devil in Silver"}) {
		t.Fatalf("parent alias escaped scope or consumed query budget: search=%v identities=%v", search, identities)
	}
	queries := buildSearchQueries(opts, debrid.ParseQuery(opts.Query), search)
	found := false
	for _, query := range queries {
		if query == "The Terror S01E06" {
			t.Fatalf("queried original season: %v", queries)
		}
		if query == "The Terror S03E06" {
			found = true
			mapped := mappedQueryOptions(opts, query)
			if mapped.IMDBID != "tt2708480" || mapped.TVDBID != 322191 || mapped.Year != 2018 {
				t.Fatalf("incorrect mapped search IDs: %+v", mapped)
			}
		}
	}
	if !found {
		t.Fatalf("missing season 3 query: %v", queries)
	}
}

func TestDevilInSilverSearchScoringRejectsOriginalSeason(t *testing.T) {
	cfg := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Streaming.ServiceMode = config.StreamingServiceModeDebrid
	settings.Display.BypassFilteringForAIOStreamsOnly = false
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	provider := &countingDebridSearchService{results: []models.NZBResult{
		{Title: "The.Terror.S01E01.1080p.WEB", ServiceType: models.ServiceTypeDebrid},
		{Title: "The.Terror.S03E01.1080p.WEB", ServiceType: models.ServiceTypeDebrid},
		{Title: "The.Terror.Devil.in.Silver.S01E01.1080p.WEB", ServiceType: models.ServiceTypeDebrid},
	}}
	results, err := NewService(cfg, nil, provider).SearchWithScoring(t.Context(), SearchOptions{
		TitleID: "tmdb:tv:323903", Query: "The Terror: Devil in Silver S01E01", MediaType: "series", Year: 2026,
		AlternateTitles: []string{"The Terror"}, IncludeFiltered: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}
	for _, result := range results {
		wrong := !strings.Contains(result.Title, "Devil") && strings.Contains(result.Title, "S01")
		if (result.FilterStatus == "filtered") != wrong {
			t.Errorf("%s: status=%s reason=%s", result.Title, result.FilterStatus, result.FilterReason)
		}
	}
}

const stormRelease = "Stephen.Kings.Storm.of.the.Century.S01E01.1080p.HULU.WEB-DL.AAC2.0.H.264"

type aliasFilteringDebrid struct{}

func (aliasFilteringDebrid) Search(_ context.Context, opts debrid.SearchOptions) ([]models.NZBResult, error) {
	results := []models.NZBResult{{Title: stormRelease, ServiceType: models.ServiceTypeDebrid}}
	if opts.SkipFilter {
		return results, nil
	}
	return filter.Results(results, filter.Options{
		ExpectedTitle: "Storm of the Century", AlternateTitles: opts.AlternateTitles,
		ExpectedYear: 1999, TargetSeason: 1, TargetEpisode: 1,
	}), nil
}

func TestSearchPathsFilterWithAliasesOutsideQueryBudget(t *testing.T) {
	for _, mode := range []string{"search", "split", "scored", "scored-split"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var queries []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				queries = append(queries, r.URL.Query().Get("q"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprintf(w, `<rss><channel><item><title>%s</title><guid>storm</guid><link>http://example.com/storm.nzb</link></item></channel></rss>`, stormRelease)
			}))
			defer server.Close()

			mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			settings := config.DefaultSettings()
			settings.Streaming.ServiceMode = config.StreamingServiceModeHybrid
			settings.Streaming.MaxAlternateTitleSearches = 1
			settings.Indexers = []config.IndexerConfig{{Name: "Test", URL: server.URL, Type: "newznab", Enabled: true}}
			if err := mgr.Save(settings); err != nil {
				t.Fatal(err)
			}
			metadata := &mockMetadataWithAliases{
				results: []models.SearchResult{{Title: models.Title{
					Name: "Storm of the Century", MediaType: "series", Year: 1999,
					IMDBID: "tt0135659", TVDBID: 85538,
				}}},
				// TMDB's Australian alias precedes its US "Complete title".
				aliases: map[int64][]string{85538: {"Storm Of The Century - Stephen King", "Stephen King's Storm of the Century"}},
			}
			svc := NewService(mgr, metadata, aliasFilteringDebrid{})
			opts := SearchOptions{Query: "Storm of the Century S01E01", MediaType: "series", Year: 1999, IMDBID: "tt0135659"}
			// Repeat to cover cached results as well as freshly fetched results.
			for attempt := 0; attempt < 2; attempt++ {
				var results []models.NZBResult
				switch mode {
				case "search":
					var err error
					results, err = svc.Search(t.Context(), opts)
					if err != nil {
						t.Fatal(err)
					}
				case "split":
					deb, us := svc.SearchSplit(t.Context(), opts)
					for _, ch := range []<-chan SplitSearchResult{deb, us} {
						for batch := range ch {
							if batch.Err != nil {
								t.Fatal(batch.Err)
							}
							results = append(results, batch.Results...)
						}
					}
				case "scored":
					opts.IncludeFiltered = true
					scored, err := svc.SearchWithScoring(t.Context(), opts)
					if err != nil {
						t.Fatal(err)
					}
					for _, result := range scored {
						if result.FilterStatus != "passed" {
							t.Fatalf("alias was rejected: %+v", result)
						}
						results = append(results, result.NZBResult)
					}
				case "scored-split":
					us, deb := svc.SearchWithScoringSplit(t.Context(), opts)
					for _, ch := range []<-chan ScoredSplitSearchResult{us, deb} {
						for batch := range ch {
							if batch.Err != nil {
								t.Fatal(batch.Err)
							}
							for _, result := range batch.Scored {
								results = append(results, result.NZBResult)
							}
						}
					}
				}
				seen := map[models.ContentServiceType]bool{}
				for _, result := range results {
					if result.Title != stormRelease {
						t.Fatalf("unexpected release: %q", result.Title)
					}
					seen[result.ServiceType] = true
				}
				if !seen[models.ServiceTypeUsenet] || !seen[models.ServiceTypeDebrid] {
					t.Fatalf("attempt %d: expected matching aliases from both sources, got %v", attempt, results)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			// Canonical plus one alternate. The second search is answered by
			// the result cache, or (unscored split, which has none) by the
			// shared indexer calls.
			wantRequests := 2
			if len(queries) != wantRequests {
				t.Fatalf("indexer requests = %d, want %d", len(queries), wantRequests)
			}
			for _, query := range queries {
				if query != "Storm of the Century S01E01" && query != "Storm Of The Century - Stephen King S01E01" {
					t.Fatalf("query exceeded alternate-title budget: %q", query)
				}
			}
		})
	}
}

func TestResolveSearchTitlesHydratedAliases(t *testing.T) {
	opts := SearchOptions{Query: "Catalog Title S01E01", MediaType: "series",
		AlternateTitles: []string{"First Alias", "Different Release Name", "First Alias", " "}}
	for _, limit := range []int{1, 0} {
		search, identities, _ := (&Service{}).resolveSearchTitles(t.Context(), opts, "eng", limit)
		wantIdentities := []string{"First Alias", "Different Release Name"}
		if !reflect.DeepEqual(identities, wantIdentities) {
			t.Fatalf("identities = %v", identities)
		}
		wantSearch := wantIdentities
		if limit == 1 {
			wantSearch = wantSearch[:1]
		}
		if !reflect.DeepEqual(search, wantSearch) {
			t.Fatalf("search = %v, want %v", search, wantSearch)
		}
	}
}

// Each alternate title is a query to every indexer, so the setting is capped:
// 0 (formerly unlimited) and values above the cap search at most 3 titles.
func TestAlternateTitleSearchesAreCapped(t *testing.T) {
	aliases := []string{"Alias One", "Alias Two", "Alias Three", "Alias Four", "Alias Five", "Alias Six"}
	opts := SearchOptions{Query: "Some Movie", MediaType: "movie", Year: 2024, AlternateTitles: aliases}
	for _, tc := range []struct{ configured, want int }{{0, 3}, {-1, 3}, {1, 1}, {2, 2}, {3, 3}, {10, 3}} {
		search, filterTitles, _ := (&Service{}).resolveSearchTitles(t.Context(), opts, "", tc.configured)
		if len(search) != tc.want {
			t.Errorf("configured %d: searched %d titles %v, want %d", tc.configured, len(search), search, tc.want)
		}
		// Results are still matched against every known alias.
		if len(filterTitles) != len(aliases) {
			t.Errorf("configured %d: filter titles = %v, want all aliases", tc.configured, filterTitles)
		}
	}
}

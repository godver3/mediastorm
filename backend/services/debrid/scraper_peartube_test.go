package debrid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/models"
	resultfilter "novastream/utils/filter"
)

// PearTube remains an ordinary scraper, but its candidates are deliberately
// URL-less until the later playback-resolution task opens the selected ref.

func pearTubeCompanionStub(t *testing.T, response string, inspect func(*http.Request)) *httptest.Server {
	t.Helper()
	t.Setenv("PEARTUBE_COMPANION_CLIENT", "mediastorm-test")
	t.Setenv("PEARTUBE_COMPANION_SHARED_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(relay.Close)
	return relay
}

const oneMovieCandidate = `{"candidates":[{
	"schemaVersion":2,
	"candidateRef":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	"work":{"title":"Inception","releaseYear":2010},
	"publication":{"publicationId":"pub-1","publisherId":"publisher-1"},
	"rendition":{"renditionId":"rend-1","container":"video/mp4","videoCodec":"avc1","resolutionLabel":"1080p","byteLength":4096},
	"asset":{"assetId":"asset-1","byteLength":4096},
	"availability":{"peers":7,"completeSeeders":3,"observedAtMs":1786406400000,"expiresAtMs":1786406460000}
}],"cursor":null}`

func TestPearTubeScraperReturnsDeferredCandidates(t *testing.T) {
	relay := pearTubeCompanionStub(t, oneMovieCandidate, func(r *http.Request) {
		if got, want := r.URL.RequestURI(), "/api/v2/search?identifier=tt1375666&kind=movie&namespace=imdb"; got != want {
			t.Errorf("request target = %q, want %q", got, want)
		}
	})
	scraper, err := NewPearTubeScraper(relay.URL, "PearTube")
	if err != nil {
		t.Fatalf("NewPearTubeScraper: %v", err)
	}

	results, err := scraper.Search(context.Background(), SearchRequest{
		Query:  "Inception",
		Parsed: ParsedQuery{Title: "Inception", Year: 2010, MediaType: MediaTypeMovie},
		IMDBID: "tt1375666",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1: %+v", len(results), results)
	}

	got := results[0]
	if got.ServiceType != models.ServiceTypePearTube {
		t.Fatalf("ServiceType = %q", got.ServiceType)
	}
	if got.Magnet != "" || got.TorrentURL != "" {
		t.Fatalf("scraper minted a playback locator: magnet=%q torrent=%q", got.Magnet, got.TorrentURL)
	}
	if got.Attributes["peartube_candidate_ref"] == "" {
		t.Fatal("missing deferred candidate reference")
	}
	if got.Attributes["preresolved"] != "" || got.Attributes["stream_url"] != "" {
		t.Fatalf("candidate was marked resolved: %+v", got.Attributes)
	}
	if got.SizeBytes != 4096 || got.Seeders != 3 || got.Resolution != "1080p" {
		t.Fatalf("factual ranking data = size:%d seeders:%d resolution:%q", got.SizeBytes, got.Seeders, got.Resolution)
	}

	normalized := normalizeScrapeResult(got)
	if normalized.ServiceType != models.ServiceTypePearTube {
		t.Fatalf("normalized ServiceType = %q", normalized.ServiceType)
	}
	if normalized.Link != "" || normalized.DownloadURL != "" {
		t.Fatalf("normalization minted a playback URL: %+v", normalized)
	}
	if normalized.Attributes["peartube_candidate_ref"] == "" {
		t.Fatal("normalization lost candidate ref")
	}
	if normalized.GUID == "" {
		t.Fatal("normalization did not derive a stable candidate GUID")
	}
}

func TestPearTubeStructuredQualityFactsCannotBypassFilters(t *testing.T) {
	base := models.NZBResult{
		Title:       "The Matrix 1999 1080p WEB-DL",
		ServiceType: models.ServiceTypePearTube,
		Attributes: map[string]string{
			"peartube_candidate_ref": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
	}

	t.Run("resolution", func(t *testing.T) {
		result := base
		result.Attributes = map[string]string{}
		for key, value := range base.Attributes {
			result.Attributes[key] = value
		}
		result.Attributes["resolution"] = "2160p"

		filtered := FilterResults([]models.NZBResult{result}, FilterOptions{
			ExpectedTitle: "The Matrix",
			ExpectedYear:  1999,
			MediaType:     MediaTypeMovie,
			MaxResolution: "1080p",
			HDRDVPolicy:   resultfilter.HDRDVPolicyIncludeHDRDV,
		})
		if len(filtered) != 0 {
			t.Fatalf("structured 2160p candidate bypassed 1080p limit: %+v", filtered)
		}
	})

	t.Run("hdr", func(t *testing.T) {
		result := base
		result.Attributes = map[string]string{}
		for key, value := range base.Attributes {
			result.Attributes[key] = value
		}
		result.Attributes["hdrFormats"] = "HDR10"

		filtered := FilterResults([]models.NZBResult{result}, FilterOptions{
			ExpectedTitle: "The Matrix",
			ExpectedYear:  1999,
			MediaType:     MediaTypeMovie,
			MaxResolution: "2160p",
			HDRDVPolicy:   resultfilter.HDRDVPolicyNoExclusion,
		})
		if len(filtered) != 0 {
			t.Fatalf("structured HDR candidate bypassed SDR-only policy: %+v", filtered)
		}
	})
}

func TestPearTubeScraperForwardsExactEpisodeCoordinates(t *testing.T) {
	relay := pearTubeCompanionStub(t, `{"candidates":[],"cursor":null}`, func(r *http.Request) {
		if got, want := r.URL.RequestURI(), "/api/v2/search?episode=2&identifier=tt0944947&kind=episode&namespace=imdb&season=1"; got != want {
			t.Errorf("request target = %q, want %q", got, want)
		}
	})
	scraper, err := NewPearTubeScraper(relay.URL, "PearTube")
	if err != nil {
		t.Fatalf("NewPearTubeScraper: %v", err)
	}
	results, err := scraper.Search(context.Background(), SearchRequest{
		Query:  "Wrong.Show.S09E09",
		Parsed: ParsedQuery{Title: "Game of Thrones", Season: 1, Episode: 2, MediaType: MediaTypeSeries},
		IMDBID: "tt0944947",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %d, want 0", len(results))
	}
}

func TestNormalizeScrapeResultKeepsDebridAsTheDefault(t *testing.T) {
	got := normalizeScrapeResult(ScrapeResult{Title: "ordinary scraper result"})
	if got.ServiceType != models.ServiceTypeDebrid {
		t.Fatalf("default ServiceType = %q", got.ServiceType)
	}
}

func TestPearTubeIsBuiltFromTheScraperList(t *testing.T) {
	// The whole integration: an entry in the same list as every other source.
	relay := pearTubeCompanionStub(t, oneMovieCandidate, nil)
	settings := config.DefaultSettings()
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "PearTube", Type: config.TorrentScraperTypePearTube, URL: relay.URL, Enabled: true},
	}
	if !hasActiveDirectStreamScrapers(settings.TorrentScrapers) {
		t.Fatal("enabled PearTube scraper did not activate search")
	}

	scrapers := buildScrapersFromSettings(settings)
	if len(scrapers) != 1 {
		t.Fatalf("built %d scrapers, want 1: %+v", len(scrapers), scrapers)
	}
	if _, ok := scrapers[0].(*PearTubeScraper); !ok {
		t.Fatalf("built %T, want *PearTubeScraper", scrapers[0])
	}

	// Disabled means absent, like any other scraper.
	settings.TorrentScrapers[0].Enabled = false
	if built := buildScrapersFromSettings(settings); len(built) != 0 {
		t.Fatalf("a disabled relay built %d scrapers, want 0", len(built))
	}
}

func TestBuildScrapersUsesFirstConfiguredPearTubeAndPreservesOtherScrapers(t *testing.T) {
	settings := config.DefaultSettings()
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "Torrentio before", Type: "torrentio", Enabled: true},
		{Name: "Selected companion", Type: config.TorrentScraperTypePearTube, URL: "https://first.example.test", Enabled: true},
		{Name: "Ignored companion", Type: config.TorrentScraperTypePearTube, URL: "https://second.example.test", Enabled: true},
		{Name: "Torrentio after", Type: "torrentio", Enabled: true},
	}

	if selected := settings.PearTubeConfig().RelayURL; selected != "https://first.example.test" {
		t.Fatalf("PearTubeConfig selected %q, want first companion", selected)
	}
	scrapers := buildScrapersFromSettings(settings)
	if len(scrapers) != 3 {
		t.Fatalf("built %d scrapers, want two non-PearTube scrapers and one companion: %+v", len(scrapers), scrapers)
	}
	if scrapers[0].Name() != "Torrentio before" || scrapers[2].Name() != "Torrentio after" {
		t.Fatalf("non-PearTube scraper order changed: %q, %q", scrapers[0].Name(), scrapers[2].Name())
	}
	selected, ok := scrapers[1].(*PearTubeScraper)
	if !ok {
		t.Fatalf("middle scraper = %T, want *PearTubeScraper", scrapers[1])
	}
	if selected.Name() != "Selected companion" || selected.client.BaseURL() != "https://first.example.test" {
		t.Fatalf("built PearTube scraper %q at %q, want selected first companion", selected.Name(), selected.client.BaseURL())
	}
}

func TestPearTubeSearchDispatchUsesOnlyTheFirstConfiguredSource(t *testing.T) {
	tests := []struct {
		name         string
		firstEnabled bool
		wantResults  int
		wantFirst    int32
	}{
		{name: "disabled first blocks enabled stale duplicate", firstEnabled: false},
		{name: "enabled first ignores later enabled duplicate", firstEnabled: true, wantResults: 1, wantFirst: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var firstRequests atomic.Int32
			var secondRequests atomic.Int32
			firstRelay := pearTubeCompanionStub(t, oneMovieCandidate, func(*http.Request) {
				firstRequests.Add(1)
			})
			secondRelay := pearTubeCompanionStub(t, oneMovieCandidate, func(*http.Request) {
				secondRequests.Add(1)
			})

			settings := config.DefaultSettings()
			settings.TorrentScrapers = []config.TorrentScraperConfig{
				{Name: "Authoritative", Type: config.TorrentScraperTypePearTube, URL: firstRelay.URL, Enabled: test.firstEnabled},
				{Name: "Stale duplicate", Type: config.TorrentScraperTypePearTube, URL: secondRelay.URL, Enabled: true},
			}
			manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
			if err := manager.Save(settings); err != nil {
				t.Fatalf("save settings: %v", err)
			}
			service := NewSearchService(manager)

			results, err := service.Search(context.Background(), SearchOptions{
				Query:      "Inception 2010",
				MediaType:  "movie",
				Year:       2010,
				IMDBID:     "tt1375666",
				SkipFilter: true,
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(results) != test.wantResults {
				t.Fatalf("results = %d, want %d: %+v", len(results), test.wantResults, results)
			}
			if got := firstRequests.Load(); got != test.wantFirst {
				t.Fatalf("authoritative relay requests = %d, want %d", got, test.wantFirst)
			}
			if got := secondRequests.Load(); got != 0 {
				t.Fatalf("stale duplicate received %d requests", got)
			}
		})
	}
}

func TestPearTubeWithoutARelayURLBuildsNothing(t *testing.T) {
	// No relay anywhere is the shipped default, and it has to stay inert rather
	// than construct a scraper that fails every search.
	t.Setenv("PEARTUBE_RELAY_URL", "")
	settings := config.DefaultSettings()
	settings.TorrentScrapers = []config.TorrentScraperConfig{
		{Name: "PearTube", Type: config.TorrentScraperTypePearTube, Enabled: true},
	}

	if built := buildScrapersFromSettings(settings); len(built) != 0 {
		t.Fatalf("built %d scrapers without a relay URL, want 0", len(built))
	}
}

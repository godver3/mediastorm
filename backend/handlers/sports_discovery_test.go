package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"novastream/config"
	"novastream/models"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSportsAddonTargetsSearchAndLeavesReplayBrowsingAvailable(t *testing.T) {
	var search, ordinary, replay atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/manifest.json":
			w.Write([]byte(`{"id":"test","catalogs":[{"type":"tv","id":"replays","name":"Replays"},{"type":"tv","id":"live","name":"Live","extra":[{"name":"search"}]}]}`))
		case strings.Contains(r.URL.Path, "replays"):
			replay.Add(1)
			w.Write([]byte(`{"metas":[{"id":"historic","name":"History"}]}`))
		case strings.Contains(r.URL.Path, "search="):
			search.Add(1)
			w.Write([]byte(`{"metas":[{"id":"game","name":"New York Yankees vs Boston Red Sox"}]}`))
		case strings.Contains(r.URL.Path, "catalog"):
			ordinary.Add(1)
			w.Write([]byte(`{"metas":[{"id":"game","name":"New York Yankees vs Boston Red Sox"}]}`))
		default:
			t.Error("unexpected request: ", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newStremioTestHandler(t, server.URL)
	d := &sportsDiscoveryRequest{game: models.SportsGame{League: "mlb", Sport: "baseball", HomeTeam: models.SportsTeam{Name: "Boston Red Sox"}, AwayTeam: models.SportsTeam{Name: "New York Yankees"}}}
	channels, err := h.fetchSportsAddon(context.Background(), server.URL+"/manifest.json", "", d, config.LiveTVFilterSettings{})
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels=%v error=%v", channels, err)
	}
	if search.Load() != 1 || ordinary.Load() != 0 || replay.Load() != 0 {
		t.Fatalf("search=%d ordinary=%d replay=%d", search.Load(), ordinary.Load(), replay.Load())
	}
	if _, err = h.fetchStremioChannels(context.Background(), server.URL+"/manifest.json", ""); err != nil {
		t.Fatal(err)
	}
	if replay.Load() != 1 {
		t.Fatal("ordinary Live TV replay browsing changed")
	}
}

func TestSportsAddonFallsBackWhenAdvertisedSearchUnsupported(t *testing.T) {
	var catalogs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/manifest.json":
			w.Write([]byte(`{"catalogs":[{"type":"tv","id":"live","extra":[{"name":"search"}]}]}`))
		case strings.Contains(r.URL.Path, "search="):
			http.NotFound(w, r)
		default:
			catalogs.Add(1)
			w.Write([]byte(`{"metas":[{"id":"game","name":"A vs B"}]}`))
		}
	}))
	defer server.Close()
	h := newStremioTestHandler(t, server.URL)
	channels, err := h.fetchSportsAddon(context.Background(), server.URL+"/manifest.json", "", &sportsDiscoveryRequest{game: models.SportsGame{Title: "A vs B"}}, config.LiveTVFilterSettings{})
	if err != nil || len(channels) != 1 || catalogs.Load() != 1 {
		t.Fatalf("fallback channels=%d calls=%d err=%v", len(channels), catalogs.Load(), err)
	}
}

func TestXtreamDiscoveryFetchesOnlySelectedCategories(t *testing.T) {
	var streamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "get_live_categories" {
			w.Write([]byte(`[{"category_id":"1","category_name":"Sports"},{"category_id":"2","category_name":"Movies"}]`))
			return
		}
		streamCalls.Add(1)
		if r.URL.Query().Get("category_id") != "1" {
			t.Error("fetched unwanted catalog")
		}
		w.Write([]byte(`[{"stream_id":1,"name":"Sports channel","stream_type":"live","category_id":"1"}]`))
	}))
	defer server.Close()
	h := newStremioTestHandler(t, server.URL)
	for i := 0; i < 2; i++ {
		channels, err := h.fetchXtreamChannelsUncached(context.Background(), server.URL, "user", "password", "", "sports")
		if err != nil || len(channels) != 1 || channels[0].Group != "Sports" {
			t.Fatalf("channels=%v error=%v", channels, err)
		}
	}
	if streamCalls.Load() != 1 {
		t.Fatal("category results not reused")
	}
}

func TestSportsDiscoverySkipsExcludedSourceBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not request excluded source", 500)
	}))
	defer server.Close()
	h := newStremioTestHandler(t, server.URL)
	req := httptest.NewRequest("GET", "/sports/game/event/streams", nil)
	req = req.WithContext(context.WithValue(req.Context(), sportsDiscoveryKey{}, &sportsDiscoveryRequest{sources: []string{"different-source"}}))
	channels, err := h.FetchFilteredChannelsForRequest(req)
	if err != nil || len(channels) != 0 || calls.Load() != 0 {
		t.Fatalf("excluded source fetched: channels=%d requests=%d error=%v", len(channels), calls.Load(), err)
	}
}

func TestSportsAddonSearchUsesEventFamilyAndEscapesParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		game models.SportsGame
		want string
	}{
		{"golf", models.SportsGame{League: "espn:golf:eur", Sport: "golf", EventKind: "tournament", Title: "BMW PGA Championship", AwayTeam: models.SportsTeam{Name: "Rory McIlroy"}}, "BMW PGA Championship"},
		{"college", models.SportsGame{League: "espn:baseball:college-baseball", Sport: "baseball", EventKind: "matchup", AwayTeam: models.SportsTeam{Name: "Texas A&M Aggies"}}, "Texas A&M Aggies"},
		{"tennis doubles", models.SportsGame{Sport: "tennis", EventKind: "matchup", AwayTeam: models.SportsTeam{Name: "González / Núñez"}}, "González / Núñez"},
		{"race", models.SportsGame{League: "f1", Sport: "racing", EventKind: "race-session", Title: "Azerbaijan Grand Prix", EventContext: "Practice 2"}, "Azerbaijan Grand Prix"},
		{"cricket", models.SportsGame{League: "espn:cricket:24627", Sport: "cricket", EventKind: "matchup", AwayTeam: models.SportsTeam{Name: "South Africa"}}, "South Africa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/manifest.json" {
					w.Write([]byte(`{"catalogs":[{"type":"tv","id":"live","extra":[{"name":"search"}]}]}`))
					return
				}
				if strings.Contains(r.URL.Path, "search=") {
					// Extras belong in the escaped Stremio path, not the HTTP query string.
					part := strings.TrimSuffix(strings.TrimPrefix(r.URL.EscapedPath(), "/catalog/tv/live/"), ".json")
					params, err := url.ParseQuery(part)
					if err != nil {
						t.Error(err)
					}
					got = params.Get("search")
					if r.URL.RawQuery != "" || len(params) != 1 {
						t.Errorf("unexpected query parameters %s", r.URL.String())
					}
				}
				w.Write([]byte(`{"metas":[]}`))
			}))
			defer server.Close()
			h := newStremioTestHandler(t, server.URL)
			_, err := h.fetchSportsAddon(context.Background(), server.URL+"/manifest.json", "", &sportsDiscoveryRequest{game: tc.game}, config.LiveTVFilterSettings{})
			if err != nil || got != tc.want {
				t.Fatalf("search=%q want=%q err=%v", got, tc.want, err)
			}
		})
	}
}

func TestSportsSearchFallsBackAfterProfileCategoryFiltering(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/manifest.json":
			w.Write([]byte(`{"catalogs":[{"type":"tv","id":"live","name":"Live","extra":[{"name":"search"}]}]}`))
		case strings.Contains(r.URL.Path, "search="):
			w.Write([]byte(`{"metas":[{"id":"hidden","name":"New York Yankees vs Boston Red Sox","genres":["Hidden"]}]}`))
		default:
			w.Write([]byte(`{"metas":[{"id":"visible","name":"New York Yankees vs Boston Red Sox","genres":["Sports"]}]}`))
		}
	}))
	defer server.Close()
	h := newStremioTestHandler(t, server.URL)
	settings, err := h.cfgManager.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.Live.Filtering.EnabledCategories = []string{"Sports"}
	if err := h.cfgManager.Save(settings); err != nil {
		t.Fatal(err)
	}
	d := &sportsDiscoveryRequest{game: models.SportsGame{League: "mlb", Sport: "baseball", HomeTeam: models.SportsTeam{Name: "Boston Red Sox"}, AwayTeam: models.SportsTeam{Name: "New York Yankees"}}}
	req := httptest.NewRequest("GET", "/sports/game/event/streams", nil)
	req = req.WithContext(context.WithValue(req.Context(), sportsDiscoveryKey{}, d))
	channels, err := h.FetchFilteredChannelsForRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].Group != "Sports" || !strings.Contains(channels[0].URL, "/visible.json") {
		t.Fatalf("expected only the visible fallback feed, got %+v", channels)
	}
}

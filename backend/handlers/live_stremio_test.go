package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/models"
)

func newStremioTestHandler(t *testing.T, providerURL string) *LiveHandler {
	t.Helper()
	manager := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Live.Mode = "stremio"
	settings.Live.ManifestURL = providerURL + "/manifest.json"
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return &LiveHandler{
		client:       &http.Client{},
		cfgManager:   manager,
		stremioCache: make(map[string]stremioChannelsCacheEntry),
	}
}

func stremioModelSource(t *testing.T, enabled bool) models.ResolvedLiveSource {
	t.Helper()
	return models.ResolvedLiveSource{
		Sources: []models.LivePlaylistSource{{
			ID:          "sports",
			Name:        "Sports",
			Mode:        "stremio",
			ManifestURL: "https://addon.test/manifest.json",
			Enabled:     &enabled,
		}},
	}
}

// stremioTestServer mimics the relevant slice of a Stremio addon (manifest +
// catalog + stream resources). hits counts catalog requests for cache testing.
func stremioTestServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"test.addon","types":["sport"],
			"catalogs":[
				{"type":"sport","id":"live","name":"Live Now","extra":[{"name":"skip"}]},
				{"type":"sport","id":"today","name":"Today","extra":[{"name":"skip"}]}
			]
		}`))
	})
	mux.HandleFunc("/catalog/sport/live.json", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		_, _ = w.Write([]byte(`{"metas":[
			{"id":"sf:skyf1","type":"sport","name":"Sky Sports F1","poster":"http://img/f1.png","genres":["Racing"]},
			{"id":"sf:tennis","type":"sport","name":"Sky Tennis","poster":"http://img/t.png","genres":["Tennis"]}
		]}`))
	})
	mux.HandleFunc("/catalog/sport/today.json", func(w http.ResponseWriter, r *http.Request) {
		// sf:skyf1 repeats here — must be de-duplicated against the live catalog.
		_, _ = w.Write([]byte(`{"metas":[
			{"id":"sf:skyf1","type":"sport","name":"Sky Sports F1","poster":"http://img/f1.png","genres":["Racing"]},
			{"id":"ev:match1","type":"sport","name":"Big Match","genres":[]}
		]}`))
	})
	mux.HandleFunc("/stream/sport/sf:skyf1.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[
			{"name":"M3U8","description":"SONYLIV ENG","url":"http://cdn.test/f1-source-1.m3u8","behaviorHints":{"proxyHeaders":{"request":{"Referer":"https://example.test/","Origin":"https://example.test","Bad\r\nHeader":"ignored"}}}},
			{"name":"Subscribe","url":"https://stremverse.invalid/subscribe"},
			{"name":"M3U8","description":"SONYLIV HIN","title":"Backup","url":"http://cdn.test/f1-source-2.m3u8","behaviorHints":{"proxyHeaders":{"request":{"Referer":"https://backup.example.test/"}}}},
			{"name":"Web Stream","externalUrl":"https://addon.test/watch?event=1"}
		]}`))
	})
	mux.HandleFunc("/stream/sport/sf:proxy.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[
			{"name":"M3U8","description":"Proxy Wrapped","url":"http://10.0.6.130:8888/proxy/hls/manifest.m3u8?d=https%3A%2F%2Fcdn.test%2Fwrapped.m3u8&h_User-Agent=StremioUA&h_Referer=https%3A%2F%2Fsonyliv.test%2F&h_Origin=https%3A%2F%2Fsonyliv.test&api_password=flow","behaviorHints":{"proxyHeaders":{"request":{"User-Agent":"BehaviorUA","x-playback-session-id":"abc123"}}}}
		]}`))
	})
	mux.HandleFunc("/stream/sport/sf:rebased-relay.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[
			{"name":"M3U8","url":"http://10.0.6.130:8888/proxy/hls/manifest.m3u8?d=https%3A%2F%2Fsession-bound.test%2Flive%2Fsigned-token&h_Referer=https%3A%2F%2Fembed.test%2F&api_password=flow"}
		]}`))
	})
	mux.HandleFunc("/proxy/hls/manifest.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("d") != "https://session-bound.test/live/signed-token" ||
			r.URL.Query().Get("h_Referer") != "https://embed.test/" ||
			r.URL.Query().Get("api_password") != "flow" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:2,\nsegment-one\n"))
	})
	mux.HandleFunc("/stream/sport/sf:relay.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[
			{"name":"M3U8","url":"https://session-bound.test/live.m3u8","behaviorHints":{"proxyHeaders":{"request":{"Referer":"https://embed.test/","Origin":"https://embed.test"}}}}
		]}`))
	})
	mux.HandleFunc("/api/hls/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") != "https://session-bound.test/live.m3u8" ||
			r.URL.Query().Get("referer") != "https://embed.test/" ||
			r.URL.Query().Get("embedOrigin") != "https://embed.test" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nvariant.m3u8\n"))
	})
	mux.HandleFunc("/stream/sport/ev:nostream.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[]}`))
	})
	mux.HandleFunc("/stream/sport/ev:subscribe.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[{"name":"Subscribe","url":"https://stremverse.invalid/subscribe"}]}`))
	})
	return httptest.NewServer(mux)
}

func TestFetchStremioChannels(t *testing.T) {
	srv := stremioTestServer(t, nil)
	defer srv.Close()

	h := newStremioTestHandler(t, srv.URL)
	channels, err := h.fetchStremioChannels(context.Background(), srv.URL+"/manifest.json", "")
	if err != nil {
		t.Fatalf("fetchStremioChannels error: %v", err)
	}

	// 2 unique from live + 1 new from today (skyf1 deduped) = 3.
	if len(channels) != 3 {
		t.Fatalf("expected 3 channels, got %d: %+v", len(channels), channels)
	}

	byID := map[string]LiveChannel{}
	for _, c := range channels {
		byID[c.ID] = c
	}

	f1, ok := byID["sf:skyf1"]
	if !ok {
		t.Fatal("missing sf:skyf1 channel")
	}
	if f1.Name != "Sky Sports F1" {
		t.Errorf("name = %q", f1.Name)
	}
	if f1.Group != "Racing" {
		t.Errorf("group = %q, want genre-derived 'Racing'", f1.Group)
	}
	wantURL := srv.URL + "/stream/sport/sf:skyf1.json"
	if f1.URL != wantURL {
		t.Errorf("URL = %q, want %q", f1.URL, wantURL)
	}

	// Channel with no genres falls back to the catalog name.
	if m := byID["ev:match1"]; m.Group != "Today" {
		t.Errorf("ev:match1 group = %q, want catalog-name 'Today'", m.Group)
	}
}

func TestFetchStremioChannelsCaches(t *testing.T) {
	var hits int32
	srv := stremioTestServer(t, &hits)
	defer srv.Close()

	h := newStremioTestHandler(t, srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := h.fetchStremioChannels(context.Background(), srv.URL+"/manifest.json", ""); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("live catalog fetched %d times, want 1 (cached)", got)
	}
}

func TestWarmPlaylistCacheIncludesStremioSource(t *testing.T) {
	srv := stremioTestServer(t, nil)
	defer srv.Close()

	enabled := true
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Live: config.LiveSettings{
			Sources: []config.LivePlaylistSource{{
				ID:          "sports",
				Name:        "Sports",
				Mode:        "stremio",
				ManifestURL: srv.URL + "/manifest.json",
				Enabled:     &enabled,
			}},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(srv.Client(), false, "", 24, 0, 0, false, mgr, nil)
	got, err := h.WarmPlaylistCache(context.Background())
	if err != nil {
		t.Fatalf("WarmPlaylistCache error: %v", err)
	}
	if got != 3 {
		t.Fatalf("WarmPlaylistCache channels = %d, want 3", got)
	}
}

func TestTopLevelStremioSourceBuildsChannels(t *testing.T) {
	srv := stremioTestServer(t, nil)
	defer srv.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Live: config.LiveSettings{
			Mode:        "stremio",
			ManifestURL: srv.URL + "/manifest.json",
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(srv.Client(), false, "", 24, 0, 0, false, mgr, nil)
	req := httptest.NewRequest(http.MethodGet, "/live/channels", nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var resp LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Channels) != 3 {
		t.Fatalf("channels length = %d, want 3: %+v", len(resp.Channels), resp.Channels)
	}
	if len(resp.Sources) != 1 || !strings.HasPrefix(resp.Sources[0].ID, "default") {
		t.Fatalf("sources = %+v, want default stremio source", resp.Sources)
	}
}

func TestResolveStremioStream(t *testing.T) {
	srv := stremioTestServer(t, nil)
	defer srv.Close()

	h := newStremioTestHandler(t, srv.URL)
	got, err := h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/sf:skyf1.json", "", -1)
	if err != nil {
		t.Fatalf("resolveStremioStream error: %v", err)
	}
	if got.URL != "http://cdn.test/f1-source-1.m3u8" {
		t.Errorf("resolved URL = %q", got.URL)
	}
	if got.RequestHeaders["Referer"] != "https://example.test/" {
		t.Errorf("resolved headers = %+v, want Referer", got.RequestHeaders)
	}
	if got.Index != 0 || !slices.Equal(got.AvailableIndexes, []int{0, 2}) {
		t.Fatalf("resolved source selection = index %d available %v, want 0 and [0 2]", got.Index, got.AvailableIndexes)
	}
	if _, ok := got.RequestHeaders["Bad\r\nHeader"]; ok {
		t.Errorf("unsafe header was not filtered: %+v", got.RequestHeaders)
	}

	got, err = h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/sf:skyf1.json", "", 2)
	if err != nil {
		t.Fatalf("resolveStremioStream selected source error: %v", err)
	}
	if got.URL != "http://cdn.test/f1-source-2.m3u8" {
		t.Errorf("selected resolved URL = %q", got.URL)
	}
	if got.RequestHeaders["Referer"] != "https://backup.example.test/" {
		t.Errorf("selected headers = %+v, want backup Referer", got.RequestHeaders)
	}
	if got.Index != 2 || !slices.Equal(got.AvailableIndexes, []int{0, 2}) {
		t.Fatalf("selected source selection = index %d available %v, want 2 and [0 2]", got.Index, got.AvailableIndexes)
	}

	got, err = h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/sf:proxy.json", "", -1)
	if err != nil {
		t.Fatalf("resolveStremioStream proxy-wrapped source error: %v", err)
	}
	if got.URL != "https://cdn.test/wrapped.m3u8" {
		t.Errorf("proxy-wrapped URL = %q, want decoded target", got.URL)
	}
	if got.RequestHeaders["User-Agent"] != "BehaviorUA" {
		t.Errorf("proxy-wrapped headers = %+v, want behavior User-Agent to win", got.RequestHeaders)
	}
	if got.RequestHeaders["Referer"] != "https://sonyliv.test/" {
		t.Errorf("proxy-wrapped headers = %+v, want Referer from h_ query", got.RequestHeaders)
	}
	if got.RequestHeaders["Origin"] != "https://sonyliv.test" {
		t.Errorf("proxy-wrapped headers = %+v, want Origin from h_ query", got.RequestHeaders)
	}
	if got.RequestHeaders["x-playback-session-id"] != "abc123" {
		t.Errorf("proxy-wrapped headers = %+v, want behavior session header", got.RequestHeaders)
	}

	got, err = h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/sf:rebased-relay.json", "", -1)
	if err != nil {
		t.Fatalf("resolveStremioStream rebased relay error: %v", err)
	}
	rebasedRelayURL, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("parse rebased relay URL: %v", err)
	}
	if rebasedRelayURL.Host != strings.TrimPrefix(srv.URL, "http://") || rebasedRelayURL.Path != "/proxy/hls/manifest.m3u8" {
		t.Fatalf("rebased relay URL = %q", got.URL)
	}
	if rebasedRelayURL.Query().Get("d") != "https://session-bound.test/live/signed-token" {
		t.Fatalf("rebased relay target = %q", rebasedRelayURL.Query().Get("d"))
	}
	if !got.IsHLS || len(got.RequestHeaders) != 0 {
		t.Fatalf("rebased relay metadata = IsHLS %v headers %+v", got.IsHLS, got.RequestHeaders)
	}

	got, err = h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/sf:relay.json", "", -1)
	if err != nil {
		t.Fatalf("resolveStremioStream addon relay error: %v", err)
	}
	relayURL, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("parse addon relay URL: %v", err)
	}
	if relayURL.Path != "/api/hls/playlist.m3u8" || relayURL.Query().Get("url") != "https://session-bound.test/live.m3u8" {
		t.Fatalf("addon relay URL = %q", got.URL)
	}
	if len(got.RequestHeaders) != 0 {
		t.Fatalf("addon relay headers = %+v, want relay to own upstream headers", got.RequestHeaders)
	}

	if _, err := h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/ev:nostream.json", "", -1); err == nil {
		t.Error("expected error for empty streams, got nil")
	}
	if _, err := h.resolveStremioStream(context.Background(), srv.URL+"/stream/sport/ev:subscribe.json", "", -1); err == nil {
		t.Error("expected error for subscription placeholder, got nil")
	}
}

func TestGetStremioStreamOptions(t *testing.T) {
	srv := stremioTestServer(t, nil)
	defer srv.Close()

	h := newStremioTestHandler(t, srv.URL)
	req := httptest.NewRequest(http.MethodGet, "/live/stremio/streams?url="+url.QueryEscape(srv.URL+"/stream/sport/sf:skyf1.json"), nil)
	rec := httptest.NewRecorder()
	h.GetStremioStreamOptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp StremioStreamOptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Streams) != 2 {
		t.Fatalf("streams length = %d, want 2: %+v", len(resp.Streams), resp.Streams)
	}
	if resp.Streams[0].Index != 0 || resp.Streams[0].Label != "SONYLIV ENG" {
		t.Fatalf("first stream = %+v, want source 1 at original index 0", resp.Streams[0])
	}
	if resp.Streams[1].Index != 2 || resp.Streams[1].Label != "SONYLIV HIN" {
		t.Fatalf("second stream = %+v, want source 2 at original index 2", resp.Streams[1])
	}
}

func TestNormalizeStremioBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://x.dev/manifest.json":         "https://x.dev",
		"https://x.dev/":                      "https://x.dev",
		"https://x.dev":                       "https://x.dev",
		"https://x.dev/cfg/abc/manifest.json": "https://x.dev/cfg/abc",
		"  https://x.dev/manifest.json  ":     "https://x.dev",
	}
	for in, want := range cases {
		if got := normalizeStremioBaseURL(in); got != want {
			t.Errorf("normalizeStremioBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsStremioStreamResourceURL(t *testing.T) {
	mustParse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}
	if !isStremioStreamResourceURL(mustParse("https://x.dev/stream/sport/sf:skyf1.json")) {
		t.Error("expected stream resource URL to match")
	}
	if isStremioStreamResourceURL(mustParse("https://x.dev/playlist/abc.m3u8")) {
		t.Error("m3u8 URL should not match")
	}
	if isStremioStreamResourceURL(nil) {
		t.Error("nil should not match")
	}
}

func TestResolvedLiveSourcesStremio(t *testing.T) {
	enabled := true
	src := stremioModelSource(t, enabled)
	sources := resolvedLiveSources(src)
	if len(sources) != 1 {
		t.Fatalf("expected 1 resolved source, got %d", len(sources))
	}
	if sources[0].Mode != "stremio" {
		t.Errorf("mode = %q, want stremio", sources[0].Mode)
	}
	if sources[0].ManifestURL != "https://addon.test/manifest.json" {
		t.Errorf("manifestUrl = %q", sources[0].ManifestURL)
	}
}

func TestResolvedLiveSourcesStremioRequiresManifest(t *testing.T) {
	// A stremio source with no manifest URL is invalid and dropped.
	src := stremioModelSource(t, true)
	src.Sources[0].ManifestURL = ""
	if got := resolvedLiveSources(src); len(got) != 0 {
		t.Fatalf("expected stremio source without manifest to be dropped, got %d", len(got))
	}
}

func TestResolvedLiveSourcesTopLevelStremio(t *testing.T) {
	src := models.ResolvedLiveSource{
		Mode:        "stremio",
		ManifestURL: "https://addon.test/manifest.json",
	}
	sources := resolvedLiveSources(src)
	if len(sources) != 1 {
		t.Fatalf("expected 1 resolved source, got %d", len(sources))
	}
	if sources[0].Mode != "stremio" || sources[0].ManifestURL != "https://addon.test/manifest.json" {
		t.Fatalf("source = %+v, want top-level stremio manifest", sources[0])
	}
}

func TestStremioStreamResourceURL(t *testing.T) {
	// ':' is a valid path char and is left intact (matches what Stremio addons,
	// including the live spike target, accept).
	got := stremioStreamResourceURL("https://x.dev", "sport", "sf:skyf1")
	if got != "https://x.dev/stream/sport/sf:skyf1.json" {
		t.Errorf("stremioStreamResourceURL = %q", got)
	}
	// A space must be percent-encoded.
	if got := stremioStreamResourceURL("https://x.dev", "tv", "a b"); !strings.Contains(got, "a%20b.json") {
		t.Errorf("expected space-encoded id in %q", got)
	}
}

// Use an in-memory transport so catalog protocol tests need no network sockets.
type catalogFixtureTransport struct{ handler http.Handler }

func (transport catalogFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, r)
	return recorder.Result(), nil
}
func newCatalogFixtureClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: catalogFixtureTransport{handler: handler}}
}

func TestFetchStremioCatalogShortPages(t *testing.T) {
	var paths []string
	srv := newCatalogFixtureClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/configured/catalog/tv/live.json":
			_, _ = w.Write([]byte(`{"metas":[{"id":"one"},{"id":"two"}]}`))
		case "/configured/catalog/tv/live/skip=2.json":
			_, _ = w.Write([]byte(`{"metas":[{"id":"three"}]}`))
		case "/configured/catalog/tv/live/skip=3.json":
			_, _ = w.Write([]byte(`{"metas":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))

	metas, err := fetchStremioCatalog(context.Background(), srv, "https://addon.test"+"/configured", stremioCatalogDef{Type: "tv", ID: "live", Extra: []stremioExtraProp{{Name: "skip"}}})
	if err != nil || len(metas) != 3 {
		t.Fatalf("got %v, %v; paths %v", metas, err, paths)
	}
}

func TestFetchStremioCatalogRequiredGenre(t *testing.T) {
	for _, options := range []string{`["All","Football"]`, `["Football","Ice Hockey"]`} {
		t.Run(options, func(t *testing.T) {
			var catalog stremioCatalogDef
			if err := json.Unmarshal([]byte(`{"type":"tv","id":"events","extra":[{"name":"genre","isRequired":true,"options":`+options+`}]}`), &catalog); err != nil {
				t.Fatal(err)
			}
			srv := newCatalogFixtureClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/catalog/tv/events/genre=All.json":
					_, _ = w.Write([]byte(`{"metas":[{"id":"football"},{"id":"hockey"}]}`))
				case "/catalog/tv/events/genre=Football.json":
					_, _ = w.Write([]byte(`{"metas":[{"id":"football"}]}`))
				case "/catalog/tv/events/genre=Ice Hockey.json":
					_, _ = w.Write([]byte(`{"metas":[{"id":"hockey"}]}`))
				default:
					http.Error(w, "genre required", 400)
				}
			}))

			metas, err := fetchStremioCatalog(context.Background(), srv, "https://addon.test", catalog)
			if err != nil || len(metas) != 2 {
				t.Fatalf("got %v, %v", metas, err)
			}
		})
	}
}

func TestFetchStremioCatalogStopsRepeatedPages(t *testing.T) {
	hits := 0
	srv := newCatalogFixtureClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"metas":[{"id":"one"},{"id":"two"}]}`))
	}))

	metas, err := fetchStremioCatalog(context.Background(), srv, "https://addon.test", stremioCatalogDef{Type: "tv", ID: "live", Extra: []stremioExtraProp{{Name: "skip"}}})
	if err != nil || len(metas) != 2 || hits != 2 {
		t.Fatalf("metas=%v err=%v requests=%d", metas, err, hits)
	}
}

func TestFetchStremioCatalogKeepsRequiredFilterWhilePaging(t *testing.T) {
	var catalog stremioCatalogDef
	if err := json.Unmarshal([]byte(`{"type":"tv","id":"events","extra":[{"name":"genre","isRequired":true,"options":["All"]},{"name":"skip"}]}`), &catalog); err != nil {
		t.Fatal(err)
	}
	client := newCatalogFixtureClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/tv/events/genre=All.json":
			_, _ = w.Write([]byte(`{"metas":[{"id":"first"}]}`))
		case "/catalog/tv/events/genre=All&skip=1.json":
			_, _ = w.Write([]byte(`{"metas":[{"id":"second"}]}`))
		case "/catalog/tv/events/genre=All&skip=2.json":
			_, _ = w.Write([]byte(`{"metas":[]}`))
		default:
			t.Errorf("unexpected catalog path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	metas, err := fetchStremioCatalog(context.Background(), client, "https://addon.test", catalog)
	if err != nil || len(metas) != 2 {
		t.Fatalf("got %v, %v", metas, err)
	}
}

func TestFetchStremioCatalogRejectsUnbrowsableRequiredExtra(t *testing.T) {
	var catalog stremioCatalogDef
	if err := json.Unmarshal([]byte(`{"type":"tv","id":"search","extra":[{"name":"search","isRequired":true}]}`), &catalog); err != nil {
		t.Fatal(err)
	}
	client := newCatalogFixtureClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not request an invalid browse URL") }))
	if _, err := fetchStremioCatalog(context.Background(), client, "https://addon.test", catalog); err == nil {
		t.Fatal("expected an unsupported browse filter error")
	}
}

func TestStremioCatalogFilterBounds(t *testing.T) {
	for _, raw := range []string{
		`{"extra":[{"name":"genre","isRequired":true}]}`,
		`{"extra":[{"name":"a","isRequired":true,"options":["1","2","3","4","5","6"]},{"name":"b","isRequired":true,"options":["1","2","3","4","5","6"]}]}`,
	} {
		var catalog stremioCatalogDef
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			t.Fatal(err)
		}
		if _, err := stremioCatalogFilters(catalog); err == nil {
			t.Fatal("expected bounded browse filter error")
		}
	}
}

func TestStremioCatalogPartialFilters(t *testing.T) {
	var catalog stremioCatalogDef
	_ = json.Unmarshal([]byte(`{"type":"tv","id":"events","extra":[{"name":"genre","isRequired":true,"options":["bad","Ice Hockey","duplicate"]}]}`), &catalog)
	client := newCatalogFixtureClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "genre=bad") {
			http.Error(w, "failed", 500)
			return
		}
		if strings.Contains(r.URL.Path, "Ice Hockey") && !strings.Contains(r.URL.EscapedPath(), "Ice%20Hockey") {
			t.Error("space was not escaped")
		}
		_, _ = w.Write([]byte(`{"metas":[{"id":"same","type":"tv"}]}`))
	}))
	got, err := fetchStremioCatalog(context.Background(), client, "https://addon.test", catalog)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

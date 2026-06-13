package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/config"
	"novastream/models"
)

func newStremioTestHandler() *LiveHandler {
	return &LiveHandler{
		client:       &http.Client{},
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
			{"name":"M3U8","description":"SONYLIV HIN","title":"Backup","url":"http://cdn.test/f1-source-2.m3u8","behaviorHints":{"proxyHeaders":{"request":{"Referer":"https://backup.example.test/"}}}}
		]}`))
	})
	mux.HandleFunc("/stream/sport/sf:proxy.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"streams":[
			{"name":"M3U8","description":"Proxy Wrapped","url":"http://10.0.6.130:8888/proxy/hls/manifest.m3u8?d=https%3A%2F%2Fcdn.test%2Fwrapped.m3u8&h_User-Agent=StremioUA&h_Referer=https%3A%2F%2Fsonyliv.test%2F&h_Origin=https%3A%2F%2Fsonyliv.test&api_password=flow","behaviorHints":{"proxyHeaders":{"request":{"User-Agent":"BehaviorUA","x-playback-session-id":"abc123"}}}}
		]}`))
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

	h := newStremioTestHandler()
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

	h := newStremioTestHandler()
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

	h := newStremioTestHandler()
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

	h := newStremioTestHandler()
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

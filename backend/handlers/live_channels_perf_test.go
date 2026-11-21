package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

func TestParsedPlaylistCacheReusesAndReparsesChangedContent(t *testing.T) {
	var c parsedPlaylistCache
	a := syntheticPlaylist(10, 2)
	first := c.parse(a)
	second := c.parse(a)
	if len(first) != 10 || &first[0] != &second[0] {
		t.Fatalf("same contents should return the cached parse")
	}
	changed := c.parse(syntheticPlaylist(11, 2))
	if len(changed) != 11 {
		t.Fatalf("changed playlist parsed to %d channels, want 11", len(changed))
	}
	for i := 0; i < parsedPlaylistCacheEntries+2; i++ {
		c.parse(syntheticPlaylist(20+i, 2))
	}
	if len(c.entries) != parsedPlaylistCacheEntries || len(c.order) != parsedPlaylistCacheEntries {
		t.Fatalf("cache holds %d entries, want %d", len(c.entries), parsedPlaylistCacheEntries)
	}
}

func TestGetChannelsReparsesRefreshedPlaylistAndPagesStayConsistent(t *testing.T) {
	h, done := newSyntheticLiveHandler(t, 600, 5)
	defer done()
	page := func(offset int) LiveChannelsResponse {
		req := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(`{"offset":`+itoa(offset)+`,"limit":250}`))
		rec := httptest.NewRecorder()
		h.GetChannels(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var resp LiveChannelsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	p0, p1 := page(0), page(250)
	if p0.Total != 600 || len(p1.Channels) != 250 || p1.Channels[0].Name != "Channel 250" {
		t.Fatalf("unexpected pages: total=%d second[0]=%+v", p0.Total, p1.Channels[0])
	}
	if len(p1.AvailableCategories) != 5 {
		t.Fatalf("categories = %v", p1.AvailableCategories)
	}
	// A page must not change the cached parse that later pages read.
	if again := page(0); again.Channels[0].ID != p0.Channels[0].ID {
		t.Fatalf("first page changed between requests")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// slowSourcesHandler serves two M3U sources that each take delay to answer.
func slowSourcesHandler(tb testing.TB, delay time.Duration) (*LiveHandler, func()) {
	tb.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".m3u")
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 tvg-id=\"" + name + "\" group-title=\"G\"," + name + "\nhttp://stream.example/" + name + "\n"))
	}))
	enabled := true
	mgr := config.NewManager(filepath.Join(tb.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "a", Name: "A", Mode: "m3u", PlaylistURL: srv.URL + "/a.m3u", Enabled: &enabled},
		{ID: "b", Name: "B", Mode: "m3u", PlaylistURL: srv.URL + "/b.m3u", Enabled: &enabled},
	}}}); err != nil {
		tb.Fatalf("save settings: %v", err)
	}
	return NewLiveHandler(srv.Client(), false, "", 0, 0, 0, false, mgr, nil), srv.Close
}

func TestGetChannelsFetchesSourcesConcurrentlyInOrder(t *testing.T) {
	const delay = 300 * time.Millisecond
	h, done := slowSourcesHandler(t, delay)
	defer done()
	started := time.Now()
	req := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(`{"offset":0,"limit":10}`))
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)
	elapsed := time.Since(started)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Channels) != 2 || resp.Channels[0].SourceID != "a" || resp.Channels[1].SourceID != "b" {
		t.Fatalf("channels = %+v, want a then b", resp.Channels)
	}
	if elapsed >= 2*delay {
		t.Fatalf("cold load took %s; two %s sources should load concurrently", elapsed, delay)
	}
}

// BenchmarkGetChannelsColdTwoSlowSources: a cold request over two 200 ms providers.
func BenchmarkGetChannelsColdTwoSlowSources(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		h, done := slowSourcesHandler(b, 200*time.Millisecond)
		b.StartTimer()
		req := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(`{"offset":0,"limit":10}`))
		h.GetChannels(httptest.NewRecorder(), req)
		b.StopTimer()
		done()
	}
}

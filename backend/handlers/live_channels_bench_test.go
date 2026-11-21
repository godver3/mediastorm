package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
)

// syntheticPlaylist is an M3U of n channels spread over groups categories.
func syntheticPlaylist(n, groups int) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"ch%d\" tvg-name=\"Channel %d\" tvg-logo=\"http://logo.example/%d.png\" group-title=\"Group %d\",Channel %d\nhttp://stream.example/%d\n", i, i, i, i%groups, i, i)
	}
	return b.String()
}

func newSyntheticLiveHandler(tb testing.TB, n, groups int) (*LiveHandler, func()) {
	tb.Helper()
	playlist := syntheticPlaylist(n, groups)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(playlist))
	}))
	enabled := true
	mgr := config.NewManager(filepath.Join(tb.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "big", Name: "Big", Mode: "m3u", PlaylistURL: srv.URL + "/big.m3u", Enabled: &enabled},
	}}}); err != nil {
		tb.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(srv.Client(), false, "", 24, 0, 0, false, mgr, nil)
	h.maxSize = 1 << 30
	return h, srv.Close
}

// BenchmarkGetChannelsPage43k measures one warm 250-channel page of a 43,208-channel lineup.
func BenchmarkGetChannelsPage43k(b *testing.B) {
	h, done := newSyntheticLiveHandler(b, 43208, 150)
	defer done()
	run := func() {
		req := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(`{"offset":250,"limit":250}`))
		rec := httptest.NewRecorder()
		h.GetChannels(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	run() // warm the playlist cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		run()
	}
}

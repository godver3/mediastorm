package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"novastream/config"
	"novastream/models"
)

func TestSplitM3ULine(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantMetadata string
		wantName     string
	}{
		{
			name:         "standard format",
			input:        `-1 tvg-id="test" tvg-name="Test Channel",Test Channel`,
			wantMetadata: `-1 tvg-id="test" tvg-name="Test Channel"`,
			wantName:     "Test Channel",
		},
		{
			name:         "comma in attribute value",
			input:        `-1 tvg-name="News, Sports & More" group-title="Entertainment",Channel Name`,
			wantMetadata: `-1 tvg-name="News, Sports & More" group-title="Entertainment"`,
			wantName:     "Channel Name",
		},
		{
			name:         "multiple commas in attributes",
			input:        `-1 tvg-name="A, B, C" tvg-logo="http://example.com/logo,test.png",Final Name`,
			wantMetadata: `-1 tvg-name="A, B, C" tvg-logo="http://example.com/logo,test.png"`,
			wantName:     "Final Name",
		},
		{
			name:         "no comma - metadata only",
			input:        `-1 tvg-id="test"`,
			wantMetadata: `-1 tvg-id="test"`,
			wantName:     "",
		},
		{
			name:         "simple duration and name",
			input:        `-1,Simple Channel`,
			wantMetadata: `-1`,
			wantName:     "Simple Channel",
		},
		{
			name:         "empty input",
			input:        ``,
			wantMetadata: ``,
			wantName:     "",
		},
		{
			name:         "real world example",
			input:        `-1 tvg-id="aande.us" tvg-name="US | A&E" tvg-logo="https://example.com/logo.png" group-title="US - Entertainment",US | A&E`,
			wantMetadata: `-1 tvg-id="aande.us" tvg-name="US | A&E" tvg-logo="https://example.com/logo.png" group-title="US - Entertainment"`,
			wantName:     "US | A&E",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMetadata, gotName := splitM3ULine(tt.input)
			if gotMetadata != tt.wantMetadata {
				t.Errorf("splitM3ULine() metadata = %q, want %q", gotMetadata, tt.wantMetadata)
			}
			if gotName != tt.wantName {
				t.Errorf("splitM3ULine() name = %q, want %q", gotName, tt.wantName)
			}
		})
	}
}

func TestParseM3UPlaylist(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []LiveChannel
	}{
		{
			name:     "empty input",
			input:    "",
			expected: nil,
		},
		{
			name: "standard channel",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="test" tvg-name="Test Channel" tvg-logo="http://logo.png" group-title="News",Test Channel
http://stream.example.com/live.m3u8`,
			expected: []LiveChannel{
				{
					ID:      "test",
					Name:    "Test Channel",
					URL:     "http://stream.example.com/live.m3u8",
					Logo:    "http://logo.png",
					Group:   "News",
					TvgID:   "test",
					TvgName: "Test Channel",
				},
			},
		},
		{
			name: "channel with comma in tvg-name",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="sports" tvg-name="Sports, News & More" group-title="Entertainment",Sports, News & More
http://stream.example.com/sports.m3u8`,
			expected: []LiveChannel{
				{
					ID:      "sports",
					Name:    "Sports, News & More",
					URL:     "http://stream.example.com/sports.m3u8",
					Group:   "Entertainment",
					TvgID:   "sports",
					TvgName: "Sports, News & More",
				},
			},
		},
		{
			name: "fallback to tvg-name when no display name",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="test" tvg-name="Fallback Name"
http://stream.example.com/live.m3u8`,
			expected: []LiveChannel{
				{
					ID:      "test",
					Name:    "Fallback Name",
					URL:     "http://stream.example.com/live.m3u8",
					TvgID:   "test",
					TvgName: "Fallback Name",
				},
			},
		},
		{
			name: "multiple channels",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="ch1" tvg-name="Channel 1",Channel 1
http://stream1.example.com
#EXTINF:-1 tvg-id="ch2" tvg-name="Channel 2",Channel 2
http://stream2.example.com`,
			expected: []LiveChannel{
				{
					ID:      "ch1",
					Name:    "Channel 1",
					URL:     "http://stream1.example.com",
					TvgID:   "ch1",
					TvgName: "Channel 1",
				},
				{
					ID:      "ch2",
					Name:    "Channel 2",
					URL:     "http://stream2.example.com",
					TvgID:   "ch2",
					TvgName: "Channel 2",
				},
			},
		},
		{
			name: "duplicate IDs get unique suffixes",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="same" tvg-name="First",First
http://stream1.example.com
#EXTINF:-1 tvg-id="same" tvg-name="Second",Second
http://stream2.example.com`,
			expected: []LiveChannel{
				{
					ID:      "same",
					Name:    "First",
					URL:     "http://stream1.example.com",
					TvgID:   "same",
					TvgName: "First",
				},
				{
					ID:      "same-1",
					Name:    "Second",
					URL:     "http://stream2.example.com",
					TvgID:   "same",
					TvgName: "Second",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseM3UPlaylist(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("parseM3UPlaylist() returned %d channels, want %d", len(got), len(tt.expected))
			}
			for i, ch := range got {
				exp := tt.expected[i]
				if ch.ID != exp.ID {
					t.Errorf("channel[%d].ID = %q, want %q", i, ch.ID, exp.ID)
				}
				if ch.Name != exp.Name {
					t.Errorf("channel[%d].Name = %q, want %q", i, ch.Name, exp.Name)
				}
				if ch.URL != exp.URL {
					t.Errorf("channel[%d].URL = %q, want %q", i, ch.URL, exp.URL)
				}
				if ch.Logo != exp.Logo {
					t.Errorf("channel[%d].Logo = %q, want %q", i, ch.Logo, exp.Logo)
				}
				if ch.Group != exp.Group {
					t.Errorf("channel[%d].Group = %q, want %q", i, ch.Group, exp.Group)
				}
				if ch.TvgID != exp.TvgID {
					t.Errorf("channel[%d].TvgID = %q, want %q", i, ch.TvgID, exp.TvgID)
				}
				if ch.TvgName != exp.TvgName {
					t.Errorf("channel[%d].TvgName = %q, want %q", i, ch.TvgName, exp.TvgName)
				}
			}
		})
	}
}

func TestResolvedM3USourcesFallbackAndFiltering(t *testing.T) {
	enabled := true
	disabled := false
	src := models.ResolvedLiveSource{
		PlaylistURL: "http://legacy.example/live.m3u",
		PlaylistSources: []models.LivePlaylistSource{
			{ID: "news", Name: "News", PlaylistURL: "http://example.com/news.m3u", Enabled: &enabled},
			{ID: "off", Name: "Off", PlaylistURL: "http://example.com/off.m3u", Enabled: &disabled},
			{Name: "Sports", PlaylistURL: "http://example.com/sports.m3u"},
		},
	}

	got := resolvedM3USources(src)
	if len(got) != 2 {
		t.Fatalf("resolvedM3USources length = %d, want 2", len(got))
	}
	if got[0].ID != "news" || got[0].Name != "News" {
		t.Fatalf("first source = %+v, want news source", got[0])
	}
	if got[1].Name != "Sports" || got[1].ID == "" {
		t.Fatalf("second source = %+v, want generated sports source", got[1])
	}

	fallback := resolvedM3USources(models.ResolvedLiveSource{PlaylistURL: "http://legacy.example/live.m3u"})
	if len(fallback) != 1 || fallback[0].ID != "default" || fallback[0].Name != "Default" {
		t.Fatalf("fallback source = %+v, want default legacy source", fallback)
	}
}

func TestTagChannelsWithSourcePrefixesIDs(t *testing.T) {
	channels := []LiveChannel{{ID: "same", Name: "Channel", URL: "http://stream.example/live"}}
	source := resolvedM3USource{ID: "sports", Name: "Sports", PlaylistURL: "http://example.com/sports.m3u"}

	got := tagChannelsWithSource(channels, source, true)
	if len(got) != 1 {
		t.Fatalf("tagged length = %d, want 1", len(got))
	}
	if got[0].ID != "sports:same" {
		t.Errorf("ID = %q, want sports:same", got[0].ID)
	}
	if got[0].SourceID != "sports" || got[0].SourceName != "Sports" {
		t.Errorf("source metadata = %q/%q, want sports/Sports", got[0].SourceID, got[0].SourceName)
	}
}

func TestGetChannelsFiltersBySourceID(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/news.m3u":
			_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:-1 tvg-id="news" tvg-name="News",News
http://stream.example/news`))
		case "/sports.m3u":
			_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:-1 tvg-id="sports" tvg-name="Sports",Sports
http://stream.example/sports`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer playlistServer.Close()

	enabled := true
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Live: config.LiveSettings{
			Mode:           "xtream",
			XtreamHost:     playlistServer.URL,
			XtreamUsername: "legacy-user",
			XtreamPassword: "legacy-pass",
			Sources: []config.LivePlaylistSource{
				{ID: "news-src", Name: "News Source", Mode: "m3u", PlaylistURL: playlistServer.URL + "/news.m3u", Enabled: &enabled},
				{ID: "sports-src", Name: "Sports Source", Mode: "m3u", PlaylistURL: playlistServer.URL + "/sports.m3u", Enabled: &enabled},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
	req := httptest.NewRequest(http.MethodGet, "/live/channels?sourceId=sports-src", nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var resp LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Channels) != 1 {
		t.Fatalf("channels length = %d, want 1: %+v", len(resp.Channels), resp.Channels)
	}
	if resp.Channels[0].Name != "Sports" || resp.Channels[0].SourceID != "sports-src" {
		t.Fatalf("channel = %+v, want sports source only", resp.Channels[0])
	}
	if len(resp.Sources) != 2 {
		t.Fatalf("sources length = %d, want 2", len(resp.Sources))
	}
}

func TestGetChannelsSourceEmptyCategoryFilterOverridesGlobal(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:-1 tvg-id="news" tvg-name="News" group-title="News",News
http://stream.example/news
#EXTINF:-1 tvg-id="sports" tvg-name="Sports" group-title="Sports",Sports
http://stream.example/sports`))
	}))
	defer playlistServer.Close()

	enabled := true
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Live: config.LiveSettings{
			Filtering: config.LiveTVFilterSettings{
				EnabledCategories: []string{"News"},
			},
			Sources: []config.LivePlaylistSource{
				{
					ID:          "main",
					Name:        "Main",
					Mode:        "m3u",
					PlaylistURL: playlistServer.URL,
					Enabled:     &enabled,
					Filtering: config.LiveTVFilterSettings{
						EnabledCategories: []string{},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
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
	if len(resp.Channels) != 2 {
		t.Fatalf("channels length = %d, want 2: %+v", len(resp.Channels), resp.Channels)
	}
}

// mockUserSettingsProvider is a test mock for LiveUserSettingsProvider.
type mockUserSettingsProvider struct {
	settings map[string]*models.UserSettings
}

func (m *mockUserSettingsProvider) Get(userID string) (*models.UserSettings, error) {
	if s, ok := m.settings[userID]; ok {
		return s, nil
	}
	return nil, nil
}

func TestResolveProfileLiveSource_NoProfileID(t *testing.T) {
	h := &LiveHandler{
		userSettingsSvc: &mockUserSettingsProvider{},
	}

	globalSettings := config.Settings{}
	globalSettings.Live.Mode = "m3u"
	globalSettings.Live.PlaylistURL = "http://global.m3u"

	req := httptest.NewRequest(http.MethodGet, "/live/channels", nil)
	src := h.resolveProfileLiveSource(req, globalSettings)

	if src.Mode != "m3u" {
		t.Errorf("Mode = %q, want %q", src.Mode, "m3u")
	}
	if src.PlaylistURL != "http://global.m3u" {
		t.Errorf("PlaylistURL = %q, want %q", src.PlaylistURL, "http://global.m3u")
	}
}

func TestResolveProfileLiveSource_WithOverrides(t *testing.T) {
	mock := &mockUserSettingsProvider{
		settings: map[string]*models.UserSettings{
			"profile-1": {
				LiveTV: models.LiveTVSettings{
					Mode:           models.StringPtr("xtream"),
					XtreamHost:     models.StringPtr("http://profile.host"),
					XtreamUsername: models.StringPtr("puser"),
					XtreamPassword: models.StringPtr("ppass"),
				},
			},
		},
	}

	h := &LiveHandler{
		userSettingsSvc: mock,
	}

	globalSettings := config.Settings{}
	globalSettings.Live.Mode = "m3u"
	globalSettings.Live.PlaylistURL = "http://global.m3u"
	globalSettings.Live.XtreamHost = "http://global.host"
	globalSettings.Live.XtreamUsername = "guser"
	globalSettings.Live.XtreamPassword = "gpass"

	req := httptest.NewRequest(http.MethodGet, "/live/channels?profileId=profile-1", nil)
	src := h.resolveProfileLiveSource(req, globalSettings)

	if src.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", src.Mode, "xtream")
	}
	if src.XtreamHost != "http://profile.host" {
		t.Errorf("XtreamHost = %q, want %q", src.XtreamHost, "http://profile.host")
	}
	if src.XtreamUsername != "puser" {
		t.Errorf("XtreamUsername = %q, want %q", src.XtreamUsername, "puser")
	}
	if src.XtreamPassword != "ppass" {
		t.Errorf("XtreamPassword = %q, want %q", src.XtreamPassword, "ppass")
	}
}

func TestResolveProfileLiveSource_UnknownProfile(t *testing.T) {
	mock := &mockUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	}

	h := &LiveHandler{
		userSettingsSvc: mock,
	}

	globalSettings := config.Settings{}
	globalSettings.Live.Mode = "m3u"
	globalSettings.Live.PlaylistURL = "http://global.m3u"

	req := httptest.NewRequest(http.MethodGet, "/live/channels?profileId=unknown-profile", nil)
	src := h.resolveProfileLiveSource(req, globalSettings)

	if src.Mode != "m3u" {
		t.Errorf("Mode = %q, want %q (should fall back to global)", src.Mode, "m3u")
	}
	if src.PlaylistURL != "http://global.m3u" {
		t.Errorf("PlaylistURL = %q, want %q (should fall back to global)", src.PlaylistURL, "http://global.m3u")
	}
}

func TestResolveProfileLiveSource_NilProvider(t *testing.T) {
	h := &LiveHandler{
		userSettingsSvc: nil,
	}

	globalSettings := config.Settings{}
	globalSettings.Live.Mode = "xtream"
	globalSettings.Live.XtreamHost = "http://global.host"

	req := httptest.NewRequest(http.MethodGet, "/live/channels?profileId=profile-1", nil)
	src := h.resolveProfileLiveSource(req, globalSettings)

	if src.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q (should fall back to global with nil provider)", src.Mode, "xtream")
	}
}

func TestLiveStreamHTTPClientDoesNotUseBodyTimeout(t *testing.T) {
	h := NewLiveHandler(nil, false, "", 24, 0, 0, false, nil, nil)

	client := h.liveStreamHTTPClient("")
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 for live stream bodies", client.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != defaultStreamOpenTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, defaultStreamOpenTimeout)
	}
	if transport.ResponseHeaderTimeout >= 30*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want an open timeout rather than a stream body timeout", transport.ResponseHeaderTimeout)
	}
}

func TestLivePlaylistHTTPClientUsesLongBodyTimeout(t *testing.T) {
	h := NewLiveHandler(nil, false, "", 24, 0, 0, false, nil, nil)

	if h.client.Timeout != defaultPlaylistTimeout {
		t.Fatalf("client.Timeout = %v, want %v", h.client.Timeout, defaultPlaylistTimeout)
	}
	if h.client.Timeout < time.Minute {
		t.Fatalf("client.Timeout = %v, want enough time for slow IPTV playlist bodies", h.client.Timeout)
	}

	proxyClient := h.liveHTTPClient("")
	if proxyClient.Timeout != defaultPlaylistTimeout {
		t.Fatalf("proxyClient.Timeout = %v, want %v", proxyClient.Timeout, defaultPlaylistTimeout)
	}
}

func TestLivePlaylistScanHTTPClientDoesNotUseBodyTimeout(t *testing.T) {
	h := NewLiveHandler(nil, false, "", 24, 0, 0, false, nil, nil)

	client := h.livePlaylistScanHTTPClient("")
	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 for category scans over huge playlist bodies", client.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != defaultPlaylistTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, defaultPlaylistTimeout)
	}
}

func TestFetchM3UCategoriesIgnoresPlaylistBodyLimit(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", playlistContentTypePlain)
		_, _ = w.Write([]byte("#EXTM3U\n"))
		_, _ = w.Write([]byte(`#EXTINF:-1 group-title="News",News 1` + "\nhttp://example.test/news1.ts\n"))
		_, _ = w.Write([]byte(`#EXTINF:-1 group-title="Sports",Sports 1` + "\nhttp://example.test/sports1.ts\n"))
		_, _ = w.Write([]byte(strings.Repeat("#EXTVLCOPT:http-user-agent=test\n", 128)))
	}))
	defer playlistServer.Close()

	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, nil, nil)
	h.maxSize = 64

	categories, err := h.fetchM3UCategories(t.Context(), playlistServer.URL, "")
	if err != nil {
		t.Fatalf("fetchM3UCategories() error = %v", err)
	}

	got := map[string]int{}
	for _, category := range categories {
		got[category.Name] = category.ChannelCount
	}
	if got["News"] != 1 || got["Sports"] != 1 {
		t.Fatalf("categories = %+v, want News=1 and Sports=1", categories)
	}

	if _, err := h.fetchPlaylistContents(t.Context(), playlistServer.URL, ""); err == nil {
		t.Fatalf("fetchPlaylistContents() error = nil, want playlist size limit error")
	}
}

// TestStreamChannelWebRequestUsesProxyAndUserAgent verifies that web transmux
// requests fetch the upstream stream through the configured proxy (sending a
// User-Agent) and pipe it into ffmpeg, rather than letting ffmpeg connect
// directly. Providers reject non-proxy source IPs and drop UA-less requests,
// so the proxied-pipe path is required for live TV to work in the web player.
func TestStreamChannelWebRequestUsesProxyAndUserAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg shell script is POSIX-only")
	}

	const streamBody = "PROXIED-TS-PAYLOAD"

	// HTTP proxy that also serves as the origin: it records the User-Agent it
	// received and returns the stream body. transport.Proxy routes the absolute
	// request URL here.
	var sawUA string
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(streamBody))
	}))
	defer proxyServer.Close()

	// Fake ffmpeg: ignore all args and copy stdin (pipe:0) to stdout (pipe:1).
	scriptPath := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{
		Live: config.LiveSettings{
			Mode:           "xtream",
			XtreamHost:     "http://provider.example",
			XtreamUsername: "user",
			XtreamPassword: "pass",
			ProxyURL:       proxyServer.URL,
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(proxyServer.Client(), true, scriptPath, 24, 0, 0, false, mgr, nil)

	req := httptest.NewRequest(http.MethodGet, "/live/stream?target=web&url=http://provider.example/live/user/pass/1.ts", nil)
	rec := httptest.NewRecorder()
	h.StreamChannel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != streamBody {
		t.Fatalf("body = %q, want %q (stream should be fetched via proxy and piped through ffmpeg)", rec.Body.String(), streamBody)
	}
	if sawUA != liveStreamUserAgent {
		t.Fatalf("upstream User-Agent = %q, want %q", sawUA, liveStreamUserAgent)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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

func TestGetChannelsReturnsHealthySourcesWhenXtreamFails(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player_api.php" {
			http.Error(w, "provider unavailable", http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/healthy.m3u" {
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 group-title=\"News\",Healthy Channel\nhttp://stream.example/healthy\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "failed", Name: "Failed", Mode: "xtream", XtreamHost: provider.URL, XtreamUsername: "user", XtreamPassword: "pass"},
		{ID: "healthy", Name: "Healthy", Mode: "m3u", PlaylistURL: provider.URL + "/healthy.m3u"},
	}}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, mgr, nil)

	for _, test := range []struct {
		name       string
		query      string
		wantStatus int
		wantCount  int
	}{
		{name: "combined", wantStatus: http.StatusOK, wantCount: 1},
		{name: "failed source", query: "?sourceId=failed", wantStatus: http.StatusBadGateway},
		{name: "healthy source", query: "?sourceId=healthy", wantStatus: http.StatusOK, wantCount: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.GetChannels(rec, httptest.NewRequest(http.MethodGet, "/live/channels"+test.query, nil))
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if rec.Code != http.StatusOK {
				return
			}
			var response LiveChannelsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(response.Channels) != test.wantCount || response.Channels[0].SourceID != "healthy" {
				t.Fatalf("channels = %+v, want healthy channel", response.Channels)
			}
		})
	}
}

func TestGetChannelsReturnsErrorWhenAllSourcesFail(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}))
	defer provider.Close()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "first", Mode: "m3u", PlaylistURL: provider.URL + "/first.m3u"},
		{ID: "second", Mode: "m3u", PlaylistURL: provider.URL + "/second.m3u"},
	}}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, mgr, nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, httptest.NewRequest(http.MethodGet, "/live/channels", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
}

func TestGetChannelsPaginatesCategorySelectionAndFavorites(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:-1 tvg-id="news-1" group-title="News",News One
http://stream.example/news-1
#EXTINF:-1 tvg-id="sports-1" group-title="Sports",Sports One
http://stream.example/sports-1
#EXTINF:-1 tvg-id="sports-2" group-title="Sports",Sports Two
http://stream.example/sports-2
#EXTINF:-1 tvg-id="sports-3" group-title="Sports",Sports Three
http://stream.example/sports-3
#EXTINF:-1 tvg-id="movie-1" group-title="Movies",Movie One
http://stream.example/movie-1`))
	}))
	defer playlistServer.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{PlaylistURL: playlistServer.URL}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
	req := httptest.NewRequest(http.MethodGet, "/live/channels?category=Sports&favoriteId=news-1&offset=1&limit=2", nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var resp LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalBeforeFilter != 5 || resp.Total != 4 {
		t.Fatalf("totals = before:%d selected:%d, want 5/4", resp.TotalBeforeFilter, resp.Total)
	}
	if resp.Offset != 1 || resp.Limit != 2 || !resp.HasMore {
		t.Fatalf("pagination = offset:%d limit:%d hasMore:%v", resp.Offset, resp.Limit, resp.HasMore)
	}
	if len(resp.Channels) != 2 || resp.Channels[0].Name != "Sports One" || resp.Channels[1].Name != "Sports Two" {
		t.Fatalf("channels = %+v, want second and third selected channels", resp.Channels)
	}
	if len(resp.AvailableCategories) != 3 {
		t.Fatalf("available categories = %+v, want all three unselected categories", resp.AvailableCategories)
	}

	staleReq := httptest.NewRequest(http.MethodGet, "/live/channels?category=Missing&offset=0&limit=2", nil)
	staleRec := httptest.NewRecorder()
	h.GetChannels(staleRec, staleReq)
	if err := json.Unmarshal(staleRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode stale-category response: %v", err)
	}
	if resp.Total != 5 || len(resp.Channels) != 2 {
		t.Fatalf("stale category should fall back to all channels, got total=%d page=%d", resp.Total, len(resp.Channels))
	}

	categories := make([]string, 0, 370)
	categories = append(categories, "Sports")
	for i := 1; i < 370; i++ {
		categories = append(categories, fmt.Sprintf("Old Category %d", i))
	}
	body, err := json.Marshal(liveChannelsRequest{
		Offset:      intPointer(1),
		Limit:       intPointer(2),
		Categories:  categories,
		FavoriteIDs: []string{"news-1"},
	})
	if err != nil {
		t.Fatalf("marshal POST request: %v", err)
	}
	postReq := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(string(body)))
	postRec := httptest.NewRecorder()
	h.GetChannels(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body=%s", postRec.Code, postRec.Body.String())
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if resp.Total != 4 || len(resp.Channels) != 2 || resp.Channels[0].Name != "Sports One" {
		t.Fatalf("POST response = total:%d channels:%+v, want GET-equivalent filtered page", resp.Total, resp.Channels)
	}
}

func TestGetChannelsAppliesCategoryBeforeConfiguredChannelLimit(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:-1 tvg-id="news-1" group-title="News",News One
http://stream.example/news-1
#EXTINF:-1 tvg-id="news-2" group-title="News",News Two
http://stream.example/news-2
#EXTINF:-1 tvg-id="sports-1" group-title="Sports",Sports One
http://stream.example/sports-1
#EXTINF:-1 tvg-id="sports-2" group-title="Sports",Sports Two
http://stream.example/sports-2
#EXTINF:-1 tvg-id="sports-3" group-title="Sports",Sports Three
http://stream.example/sports-3`))
	}))
	defer playlistServer.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{
		PlaylistURL: playlistServer.URL,
		Filtering:   config.LiveTVFilterSettings{MaxChannels: 2},
	}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
	req := httptest.NewRequest(http.MethodGet, "/live/channels?category=Sports&offset=0&limit=2", nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 2 || len(response.Channels) != 2 || response.Channels[0].Name != "Sports One" || response.Channels[1].Name != "Sports Two" {
		t.Fatalf("response = total:%d channels:%+v", response.Total, response.Channels)
	}
	if len(response.AvailableCategories) != 2 {
		t.Fatalf("available categories = %+v, want News and Sports", response.AvailableCategories)
	}
}

func intPointer(value int) *int {
	return &value
}

func TestParseLiveChannelsRequestRejectsInvalidPostBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"offset":`},
		{name: "multiple values", body: `{}` + "\n" + `{}`},
		{name: "invalid limit", body: `{"limit":501}`},
		{name: "too many categories", body: `{"categories":[` + strings.Repeat(`"category",`, maxLiveChannelCategories) + `"extra"]}`},
		{name: "filter too long", body: `{"filter":"` + strings.Repeat("x", maxLiveChannelFilterBytes+1) + `"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(test.body))
			rec := httptest.NewRecorder()
			(&LiveHandler{}).GetChannels(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestParseLiveChannelsRequestRejectsOversizedPostBody(t *testing.T) {
	body := `{"filter":"` + strings.Repeat("x", maxLiveChannelsRequestBody) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/live/channels", strings.NewReader(body))
	rec := httptest.NewRecorder()

	(&LiveHandler{}).GetChannels(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

type fakeLiveEPGNowPlayingProvider struct {
	nowPlaying []models.EPGNowPlaying
}

func (f fakeLiveEPGNowPlayingProvider) GetNowPlaying(_ []string, _ ...time.Duration) []models.EPGNowPlaying {
	return f.nowPlaying
}

func TestGetChannelsFiltersAndOrdersBeforePagination(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`#EXTM3U
#EXTINF:-1 tvg-id="favorite" group-title="News",Always Favorite
http://stream.example/favorite
#EXTINF:-1 tvg-id="name-1" group-title="Sports",Match One
http://stream.example/name-1
#EXTINF:-1 tvg-id="program" group-title="Movies",Cinema Channel
http://stream.example/program
#EXTINF:-1 tvg-id="name-2" group-title="Sports",Match Two
http://stream.example/name-2
#EXTINF:-1 tvg-id="other" group-title="News",Other Channel
http://stream.example/other`))
	}))
	defer playlistServer.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{PlaylistURL: playlistServer.URL}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
	h.SetEPGService(fakeLiveEPGNowPlayingProvider{nowPlaying: []models.EPGNowPlaying{
		{ChannelID: "program", Current: &models.EPGProgram{Title: "The Match Show"}},
	}})
	req := httptest.NewRequest(http.MethodGet, "/live/channels?filter=match&favoriteId=favorite&offset=0&limit=2", nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var resp LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalBeforeFilter != 5 || resp.Total != 4 || !resp.HasMore {
		t.Fatalf("response totals = before:%d filtered:%d hasMore:%v, want 5/4/true", resp.TotalBeforeFilter, resp.Total, resp.HasMore)
	}
	if len(resp.Channels) != 2 || resp.Channels[0].ID != "favorite" || resp.Channels[1].ID != "name-1" {
		t.Fatalf("first page = %+v, want favorite followed by first name match", resp.Channels)
	}

	nextReq := httptest.NewRequest(http.MethodGet, "/live/channels?filter=match&favoriteId=favorite&offset=2&limit=2", nil)
	nextRec := httptest.NewRecorder()
	h.GetChannels(nextRec, nextReq)
	if err := json.Unmarshal(nextRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode next response: %v", err)
	}
	if resp.HasMore || len(resp.Channels) != 2 || resp.Channels[0].ID != "program" || resp.Channels[1].ID != "name-2" {
		t.Fatalf("second page = %+v hasMore=%v, want program and remaining name match", resp.Channels, resp.HasMore)
	}
}

func TestGetChannelsRejectsInvalidPagination(t *testing.T) {
	h := &LiveHandler{}
	req := httptest.NewRequest(http.MethodGet, "/live/channels?limit=501", nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
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

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := config.DefaultSettings()
	settings.Live.PlaylistURL = playlistServer.URL
	if err := mgr.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
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
			XtreamHost:     "http://192.0.2.1",
			XtreamUsername: "user",
			XtreamPassword: "pass",
			ProxyURL:       proxyServer.URL,
		},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := NewLiveHandler(proxyServer.Client(), true, scriptPath, 24, 0, 0, false, mgr, nil)

	req := httptest.NewRequest(http.MethodGet, "/live/stream?target=web&url=http://192.0.2.1/live/user/pass/1.ts", nil)
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

// TestFetchXtreamChannelsSendsUserAgent verifies the Xtream player_api.php
// category and stream requests carry a recognized player User-Agent. Some
// providers stall requests lacking one (the default Go-http-client UA) until
// the request times out, surfacing as "context deadline exceeded".
func TestFetchXtreamChannelsSendsUserAgent(t *testing.T) {
	var catUA, streamUA string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			catUA = r.Header.Get("User-Agent")
			_, _ = w.Write([]byte(`[{"category_id":"1","category_name":"News"}]`))
		case "get_live_streams":
			streamUA = r.Header.Get("User-Agent")
			_, _ = w.Write([]byte(`[{"stream_id":10,"name":"Channel One","stream_type":"live","category_id":"1"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, mgr, nil)

	channels, err := h.fetchXtreamChannels(context.Background(), provider.URL, "user", "pass", "")
	if err != nil {
		t.Fatalf("fetchXtreamChannels: %v", err)
	}
	if len(channels) != 1 || channels[0].Name != "Channel One" || channels[0].Group != "News" {
		t.Fatalf("channels = %+v, want one News channel", channels)
	}
	if catUA != liveStreamUserAgent {
		t.Fatalf("categories User-Agent = %q, want %q", catUA, liveStreamUserAgent)
	}
	if streamUA != liveStreamUserAgent {
		t.Fatalf("streams User-Agent = %q, want %q", streamUA, liveStreamUserAgent)
	}
}

func TestFetchXtreamChannelsCachesFailureAndRetriesAfterCooldown(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	var categoryRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			categoryRequests.Add(1)
			if failing.Load() {
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`[{"category_id":"1","category_name":"News"}]`))
		case "get_live_streams":
			_, _ = w.Write([]byte(`[{"stream_id":10,"name":"Channel One","stream_type":"live","category_id":"1"}]`))
		}
	}))
	defer provider.Close()
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil)
	fetch := func() ([]LiveChannel, error) {
		return h.fetchXtreamChannels(context.Background(), provider.URL, "user", "pass", "")
	}
	if _, err := fetch(); err == nil {
		t.Fatal("first fetch should fail")
	}
	if _, err := fetch(); err == nil {
		t.Fatal("cached failure should fail")
	}
	if got := categoryRequests.Load(); got != int32(len(xtreamUserAgents)) {
		t.Fatalf("category requests during cooldown = %d, want %d", got, len(xtreamUserAgents))
	}

	failing.Store(false)
	h.xtreamMu.Lock()
	for key, entry := range h.xtreamCache {
		entry.retryAfter = time.Now().Add(-time.Second)
		h.xtreamCache[key] = entry
	}
	h.xtreamMu.Unlock()
	channels, err := fetch()
	if err != nil || len(channels) != 1 {
		t.Fatalf("retry after cooldown: channels=%+v error=%v", channels, err)
	}
	if got := categoryRequests.Load(); got != int32(len(xtreamUserAgents)+1) {
		t.Fatalf("category requests after recovery = %d, want %d", got, len(xtreamUserAgents)+1)
	}
}

func TestFetchXtreamChannelsServesStaleCatalogDuringFailure(t *testing.T) {
	var failing atomic.Bool
	var categoryRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			categoryRequests.Add(1)
			if failing.Load() {
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`[{"category_id":"1","category_name":"News"}]`))
		case "get_live_streams":
			_, _ = w.Write([]byte(`[{"stream_id":10,"name":"Channel One","stream_type":"live","category_id":"1"}]`))
		}
	}))
	defer provider.Close()
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, config.NewManager(filepath.Join(t.TempDir(), "settings.json")), nil)
	fetch := func() ([]LiveChannel, error) {
		return h.fetchXtreamChannels(context.Background(), provider.URL, "user", "pass", "")
	}
	if _, err := fetch(); err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	h.xtreamMu.Lock()
	for key, entry := range h.xtreamCache {
		entry.expiresAt = time.Now().Add(-time.Second)
		h.xtreamCache[key] = entry
	}
	h.xtreamMu.Unlock()
	failing.Store(true)
	for range 2 {
		channels, err := fetch()
		if err != nil || len(channels) != 1 || channels[0].Name != "Channel One" {
			t.Fatalf("stale catalog: channels=%+v error=%v", channels, err)
		}
	}
	if got := categoryRequests.Load(); got != int32(1+len(xtreamUserAgents)) {
		t.Fatalf("category requests with stale cache = %d, want %d", got, 1+len(xtreamUserAgents))
	}
}

func TestXtreamRequestErrorOmitsCredentials(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "http://provider.example/player_api.php?username=user&password=secret",
		Err: fmt.Errorf("dial tcp: i/o timeout"),
	}
	got := xtreamRequestError(err).Error()
	if strings.Contains(got, "secret") || !strings.Contains(got, "i/o timeout") {
		t.Fatalf("sanitized error = %q", got)
	}
}

func TestGetCategoriesSourceIndexIsolatesOtherSources(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			_, _ = w.Write([]byte(`[{"category_id":"1","category_name":"Xtream News"}]`))
		case "get_live_streams":
			_, _ = w.Write([]byte(`[{"stream_id":10,"name":"Channel One","stream_type":"live","category_id":"1"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	var otherFails atomic.Bool
	otherFails.Store(true)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if otherFails.Load() {
			http.Error(w, "addon unavailable", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 group-title=\"Other Addon\",Other Channel\nhttp://example.com/stream\n"))
	}))
	defer other.Close()

	disabled := false
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{Mode: "m3u", PlaylistURL: other.URL, Enabled: &disabled},
		{Mode: "xtream", XtreamHost: provider.URL, XtreamUsername: "user", XtreamPassword: "pass"},
		{Mode: "m3u", PlaylistURL: other.URL},
	}}}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, mgr, nil)
	get := func(query string) (int, CategoriesResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.GetCategories(rec, httptest.NewRequest(http.MethodGet, "/live/categories"+query, nil))
		var response CategoriesResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode categories: %v", err)
			}
		}
		return rec.Code, response
	}

	if status, _ := get(""); status != http.StatusBadGateway {
		t.Fatalf("unscoped status = %d, want 502 from other source", status)
	}
	status, response := get("?sourceIndex=1")
	if status != http.StatusOK || len(response.Categories) != 1 || response.Categories[0].Name != "Xtream News" {
		t.Fatalf("scoped status = %d, categories = %+v, want only Xtream News", status, response.Categories)
	}
	if status, _ := get("?sourceIndex=0"); status != http.StatusBadRequest {
		t.Fatalf("disabled source status = %d, want 400", status)
	}

	otherFails.Store(false)
	// Metadata failures now have an endpoint-specific backoff, even after recovery.
	if status, _ := get(""); status != http.StatusBadGateway {
		t.Fatal("failure backoff was bypassed")
	}
	h.discovery.mu.Lock()
	for _, entry := range h.discovery.entries {
		if entry.code >= 500 {
			entry.expires = time.Now().Add(-time.Second)
		}
	}
	h.discovery.mu.Unlock()
	status, response = get("")
	if status != http.StatusOK || len(response.Categories) != 2 {
		t.Fatalf("unscoped status = %d, categories = %+v, want merged categories", status, response.Categories)
	}
	status, response = get("?sourceIndex=1")
	if status != http.StatusOK || len(response.Categories) != 1 || response.Categories[0].Name != "Xtream News" {
		t.Fatalf("scoped status = %d, categories = %+v after other recovers", status, response.Categories)
	}
}

func TestFetchXtreamChannelsCachesCatalog(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			_, _ = w.Write([]byte(`[{"category_id":"1","category_name":"News"}]`))
		case "get_live_streams":
			_, _ = w.Write([]byte(`[{"stream_id":10,"name":"Channel One","stream_type":"live","category_id":"1"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, nil, nil)
	for i := 0; i < 2; i++ {
		channels, err := h.fetchXtreamChannels(context.Background(), provider.URL, "user", "pass", "")
		if err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
		if len(channels) != 1 || channels[0].Name != "Channel One" {
			t.Fatalf("fetch %d channels = %+v", i+1, channels)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2 from one categories+streams fetch", got)
	}
}

// TestFetchXtreamChannelsFallsBackToBrowserUA verifies that when a provider
// rejects the VLC UA, the fetch retries with a browser UA and reuses that
// working UA for the follow-up streams request.
func TestFetchXtreamChannelsFallsBackToBrowserUA(t *testing.T) {
	var catUAs, streamUAs []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		// Simulate a provider that whitelists only real browsers.
		if ua != liveBrowserUserAgent {
			http.Error(w, "not permitted", http.StatusForbidden)
			if r.URL.Query().Get("action") == "get_live_categories" {
				catUAs = append(catUAs, ua)
			}
			return
		}
		switch r.URL.Query().Get("action") {
		case "get_live_categories":
			catUAs = append(catUAs, ua)
			_, _ = w.Write([]byte(`[{"category_id":"1","category_name":"News"}]`))
		case "get_live_streams":
			streamUAs = append(streamUAs, ua)
			_, _ = w.Write([]byte(`[{"stream_id":10,"name":"Channel One","stream_type":"live","category_id":"1"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	h := NewLiveHandler(provider.Client(), false, "", 24, 0, 0, false, mgr, nil)

	channels, err := h.fetchXtreamChannels(context.Background(), provider.URL, "user", "pass", "")
	if err != nil {
		t.Fatalf("fetchXtreamChannels: %v", err)
	}
	if len(channels) != 1 || channels[0].Group != "News" {
		t.Fatalf("channels = %+v, want one News channel", channels)
	}
	// Categories should have tried VLC first, then succeeded on the browser UA.
	if len(catUAs) != 2 || catUAs[0] != liveStreamUserAgent || catUAs[1] != liveBrowserUserAgent {
		t.Fatalf("category UA attempts = %v, want [VLC, browser]", catUAs)
	}
	// Streams should reuse the working browser UA directly (no wasted VLC retry).
	if len(streamUAs) != 1 || streamUAs[0] != liveBrowserUserAgent {
		t.Fatalf("stream UA attempts = %v, want [browser]", streamUAs)
	}
}

func TestLiveSourceMayContainFavorites(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		ids          []string
		multi, want  bool
	}{
		{"matching source", "one", []string{"one:news"}, true, true},
		{"unrelated source", "two", []string{"one:news"}, true, false},
		{"prefix boundary", "one", []string{"ones:news"}, true, false},
		{"single source", "one", []string{"news"}, false, true},
		{"empty favorites", "one", nil, false, false},
		{"nested channel id", "one", []string{"one:addon:news"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveSourceMayContainFavorites(tc.source, tc.ids, tc.multi); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestGetFavoriteChannelsSkipsUnrelatedProviders(t *testing.T) {
	playlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/main" {
			t.Error("contacted unrelated provider")
			http.Error(w, "unavailable", 503)
			return
		}
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 tvg-id=\"news\" group-title=\"News\",News\nhttp://stream.example/news"))
	}))
	defer playlistServer.Close()
	enabled := true
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	if err := mgr.Save(config.Settings{Live: config.LiveSettings{Sources: []config.LivePlaylistSource{
		{ID: "main", Name: "Main", Mode: "m3u", PlaylistURL: playlistServer.URL + "/main", Enabled: &enabled},
		{ID: "other", Name: "Other", Mode: "m3u", PlaylistURL: playlistServer.URL + "/other", Enabled: &enabled},
	}}}); err != nil {
		t.Fatal(err)
	}
	h := NewLiveHandler(playlistServer.Client(), false, "", 24, 0, 0, false, mgr, nil)
	rec := httptest.NewRecorder()
	h.GetChannels(rec, httptest.NewRequest(http.MethodGet, "/live/channels?favoritesOnly=true&favoriteId=main:news", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var response LiveChannelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Channels) != 1 || response.Channels[0].ID != "main:news" {
		t.Fatalf("favorites = %+v", response.Channels)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("source choices lost: %+v", response.Sources)
	}
}

func TestAddonLookupAllowsSlowerHeadersWithoutChangingPlaybackTimeout(t *testing.T) {
	h := &LiveHandler{}
	lookup := h.liveStreamHTTPClientWithTimeout("", 30*time.Second)
	transport, ok := lookup.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 30*time.Second {
		t.Fatal("addon lookup did not retain its longer header timeout")
	}
	playback := h.liveStreamHTTPClient("")
	if playback.Transport.(*http.Transport).ResponseHeaderTimeout != defaultStreamOpenTimeout {
		t.Fatal("changed playback connection timeout")
	}
	if lookup.CheckRedirect == nil {
		t.Fatal("lost outbound redirect validation")
	}
}

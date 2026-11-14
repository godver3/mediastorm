package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/auth"
	"novastream/internal/livecapacity"
	"novastream/models"
)

type fakeLiveUsageConfigProvider struct {
	settings config.Settings
}

type fakeLiveUsageUsersProvider struct {
	users map[string]models.User
}

func (f fakeLiveUsageUsersProvider) Get(id string) (models.User, bool) {
	user, ok := f.users[id]
	return user, ok
}

func (f fakeLiveUsageConfigProvider) Load() (config.Settings, error) {
	return f.settings, nil
}

type fakeLiveUsageUserSettingsProvider struct {
	settings map[string]*models.UserSettings
}

func (f fakeLiveUsageUserSettingsProvider) Get(userID string) (*models.UserSettings, error) {
	if setting, ok := f.settings[userID]; ok {
		return setting, nil
	}
	return nil, nil
}

func TestGetLiveUsageCountsActiveRecordings(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Mode:        "m3u",
				PlaylistURL: "http://example.com/live.m3u",
				MaxStreams:  2,
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})
	target := handler.resolveLiveStreamTarget("profile-1")
	lease, err := livecapacity.Default.Acquire(target.BucketKey, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	req := httptest.NewRequest(http.MethodGet, "/live/usage?profileId=profile-1", nil)
	rec := httptest.NewRecorder()

	handler.GetLiveUsage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var usage LiveUsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if usage.CurrentStreams != 1 {
		t.Fatalf("currentStreams = %d, want 1", usage.CurrentStreams)
	}
	if usage.MaxStreams != 2 {
		t.Fatalf("maxStreams = %d, want 2", usage.MaxStreams)
	}
	if usage.AvailableStreams != 1 {
		t.Fatalf("availableStreams = %d, want 1", usage.AvailableStreams)
	}
	if usage.AtLimit {
		t.Fatal("expected atLimit = false")
	}
	if len(usage.Providers) != 1 || usage.Providers[0].Current != 1 {
		t.Fatalf("providers = %+v, want single provider with current=1", usage.Providers)
	}
}

func TestGetLiveUsageUsesSelectedLiveSourceLimit(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Sources: []config.LivePlaylistSource{
					{
						ID:          "source-a",
						Name:        "Source A",
						Mode:        "m3u",
						PlaylistURL: "http://example.com/a.m3u",
						MaxStreams:  1,
					},
					{
						ID:          "source-b",
						Name:        "Source B",
						Mode:        "m3u",
						PlaylistURL: "http://example.com/b.m3u",
						MaxStreams:  4,
					},
				},
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})

	req := httptest.NewRequest(http.MethodGet, "/live/usage?profileId=profile-1&sourceId=source-b", nil)
	rec := httptest.NewRecorder()

	handler.GetLiveUsage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var usage LiveUsageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if usage.MaxStreams != 4 {
		t.Fatalf("maxStreams = %d, want selected source limit 4", usage.MaxStreams)
	}
}

func TestStartLiveHLSSessionDirectIncludesProfileParams(t *testing.T) {
	handler := NewVideoHandlerWithProvider(true, stubBinary(t, "echo"), stubBinary(t, "echo"), t.TempDir(), nil)
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Mode:         "m3u",
				PlaylistURL:  "http://example.com/live.m3u",
				StreamFormat: "direct",
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})
	handler.SetUsersService(fakeLiveUsageUsersProvider{
		users: map[string]models.User{
			"profile-1": {ID: "profile-1", Name: "Living Room", AccountID: "account-1"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/live/hls/start?url=http%3A%2F%2Fexample.com%2Fchannel.ts&profileId=profile-1&target=app&mediaType=channel&itemId=tvg-1&title=Evening%20News", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "account-1"))
	rec := httptest.NewRecorder()

	handler.StartLiveHLSSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		StreamURL string `json:"streamUrl"`
		IsDirect  bool   `json:"isDirect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !body.IsDirect {
		t.Fatal("expected direct live response")
	}
	if strings.Contains(body.StreamURL, "+") {
		t.Fatalf("streamUrl contains form-encoded spaces that the player treats as literal plus signs: %q", body.StreamURL)
	}
	if !strings.Contains(body.StreamURL, "profileName=Living%20Room") {
		t.Fatalf("streamUrl profileName is not percent-encoded: %q", body.StreamURL)
	}
	if !strings.Contains(body.StreamURL, "title=Evening%20News") {
		t.Fatalf("streamUrl title is not percent-encoded: %q", body.StreamURL)
	}

	parsed, err := url.Parse(body.StreamURL)
	if err != nil {
		t.Fatalf("parse streamUrl: %v", err)
	}
	values := parsed.Query()
	if got := values.Get("profileId"); got != "profile-1" {
		t.Fatalf("profileId = %q, want profile-1", got)
	}
	if got := values.Get("profileName"); got != "Living Room" {
		t.Fatalf("profileName = %q, want Living Room", got)
	}
	if got := values.Get("target"); got != "app" {
		t.Fatalf("target = %q, want app", got)
	}
	if got := values.Get("mediaType"); got != "channel" {
		t.Fatalf("mediaType = %q, want channel", got)
	}
	if got := values.Get("itemId"); got != "tvg-1" {
		t.Fatalf("itemId = %q, want tvg-1", got)
	}
	if got := values.Get("title"); got != "Evening News" {
		t.Fatalf("title = %q, want Evening News", got)
	}
	if values.Has("container") {
		t.Fatalf("container forwarded without being asked for: %q", body.StreamURL)
	}
}

func TestStartLiveHLSSessionDirectForwardsTSContainer(t *testing.T) {
	handler := NewVideoHandlerWithProvider(true, stubBinary(t, "echo"), stubBinary(t, "echo"), t.TempDir(), nil)
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Mode:         "m3u",
				PlaylistURL:  "http://example.com/live.m3u",
				StreamFormat: "direct",
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})
	handler.SetUsersService(fakeLiveUsageUsersProvider{
		users: map[string]models.User{
			"profile-1": {ID: "profile-1", Name: "Living Room", AccountID: "account-1"},
		},
	})

	for container, want := range map[string]string{"ts": "ts", "TS": "ts", "mkv": ""} {
		req := httptest.NewRequest(http.MethodGet, "/live/hls/start?url=http%3A%2F%2Fexample.com%2Fchannel.ts&profileId=profile-1&target=native&container="+container, nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "account-1"))
		rec := httptest.NewRecorder()

		handler.StartLiveHLSSession(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("container=%s: status = %d, body=%s", container, rec.Code, rec.Body.String())
		}
		var body struct {
			StreamURL string `json:"streamUrl"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		parsed, err := url.Parse(body.StreamURL)
		if err != nil {
			t.Fatalf("parse streamUrl: %v", err)
		}
		if got := parsed.Query().Get("container"); got != want {
			t.Fatalf("container=%s: forwarded container = %q, want %q (%s)", container, got, want, body.StreamURL)
		}
	}
}

func TestResolveLiveStreamTargetUsesPerSourceStreamFormat(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.SetConfigManager(fakeLiveUsageConfigProvider{settings: config.Settings{Live: config.LiveSettings{
		StreamFormat: "hls",
		Sources: []config.LivePlaylistSource{
			{ID: "portal", Name: "Portal", Mode: "stalker", StalkerPortalURL: "https://portal.example/c/", StalkerMAC: "00:1A:79:00:00:01", StreamFormat: "direct"},
		},
	}}})

	target := handler.resolveLiveStreamTargetForSource("", "portal")
	if target.StreamFormat != "direct" {
		t.Fatalf("stream format = %q, want source-specific direct", target.StreamFormat)
	}
}

func TestStartLiveHLSSessionDirectForcesHLSWhenRequested(t *testing.T) {
	handler := NewVideoHandlerWithProvider(true, stubBinary(t, "true"), stubBinary(t, "true"), t.TempDir(), nil)
	defer handler.hlsManager.Shutdown()
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Mode:         "m3u",
				PlaylistURL:  "http://example.com/live.m3u",
				StreamFormat: "direct",
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})

	req := httptest.NewRequest(http.MethodGet, "/live/hls/start?url=http%3A%2F%2Fexample.com%2Fchannel.ts&target=native&format=hls", nil)
	rec := httptest.NewRecorder()

	handler.StartLiveHLSSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		SessionID   string `json:"sessionId"`
		PlaylistURL string `json:"playlistUrl"`
		IsDirect    bool   `json:"isDirect"`
		IsLive      bool   `json:"isLive"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.IsDirect {
		t.Fatal("expected HLS response when format=hls is requested")
	}
	if !body.IsLive {
		t.Fatal("expected live response")
	}
	if !strings.HasPrefix(body.PlaylistURL, "/video/hls/") {
		t.Fatalf("playlistUrl = %q, want /video/hls/ prefix", body.PlaylistURL)
	}
	session, ok := handler.hlsManager.GetSession(body.SessionID)
	if !ok {
		t.Fatalf("session %q not found", body.SessionID)
	}
	session.mu.RLock()
	playbackTarget := session.PlaybackTarget
	session.mu.RUnlock()
	if playbackTarget != "native" {
		t.Fatalf("playbackTarget = %q, want native", playbackTarget)
	}
}

func TestStartLiveHLSSessionRejectsUnconfiguredPrivateURL(t *testing.T) {
	handler := NewVideoHandlerWithProvider(true, stubBinary(t, "true"), stubBinary(t, "true"), t.TempDir(), nil)
	handler.SetConfigManager(fakeLiveUsageConfigProvider{settings: config.Settings{
		Live: config.LiveSettings{Mode: "m3u", PlaylistURL: "https://example.com/live.m3u"},
	}})
	req := httptest.NewRequest(http.MethodGet, "/live/hls/start?url=http%3A%2F%2F127.0.0.1%3A7777%2Fapi%2Fsettings", nil)
	rec := httptest.NewRecorder()

	handler.StartLiveHLSSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestStartLiveHLSSessionResolvesStremioStreamResource(t *testing.T) {
	var stremio *httptest.Server
	stremio = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream/sport/event.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"streams":[{"url":%q,"behaviorHints":{"proxyHeaders":{"request":{"Referer":"https://stremio.example/","Origin":"https://stremio.example"}}}}]}`, stremio.URL+"/live/event.m3u8")))
	}))
	defer stremio.Close()

	handler := NewVideoHandlerWithProvider(true, stubBinary(t, "true"), stubBinary(t, "true"), t.TempDir(), nil)
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Mode:         "stremio",
				ManifestURL:  stremio.URL + "/manifest.json",
				StreamFormat: "hls",
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})

	reqURL := "/live/hls/start?url=" + url.QueryEscape(stremio.URL+"/stream/sport/event.json") + "&format=hls"
	req := httptest.NewRequest(http.MethodGet, reqURL, nil)
	rec := httptest.NewRecorder()

	handler.StartLiveHLSSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		SessionID            string `json:"sessionId"`
		StremioStreamIndex   int    `json:"stremioStreamIndex"`
		StremioStreamIndexes []int  `json:"stremioStreamIndexes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.SessionID == "" {
		t.Fatal("expected sessionId")
	}
	if body.StremioStreamIndex != 0 || len(body.StremioStreamIndexes) != 1 || body.StremioStreamIndexes[0] != 0 {
		t.Fatalf("stremio source response = index %d available %v", body.StremioStreamIndex, body.StremioStreamIndexes)
	}

	handler.hlsManager.mu.RLock()
	session := handler.hlsManager.sessions[body.SessionID]
	handler.hlsManager.mu.RUnlock()
	if session == nil {
		t.Fatalf("session %q not found", body.SessionID)
	}
	if session.Path != stremio.URL+"/live/event.m3u8" {
		t.Fatalf("session path = %q, want resolved stream URL", session.Path)
	}
	if session.LiveTuning.RequestHeaders["Referer"] != "https://stremio.example/" {
		t.Fatalf("session stremio headers = %+v, want Referer", session.LiveTuning.RequestHeaders)
	}
	handler.hlsManager.CleanupSession(body.SessionID)
}

func TestStartLiveHLSSessionRejectsStremioSubscriptionPlaceholder(t *testing.T) {
	stremio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream/sport/event.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"streams":[{"url":"https://stremverse.invalid/subscribe"}]}`))
	}))
	defer stremio.Close()

	handler := NewVideoHandlerWithProvider(true, stubBinary(t, "true"), stubBinary(t, "true"), t.TempDir(), nil)
	handler.SetConfigManager(fakeLiveUsageConfigProvider{
		settings: config.Settings{
			Live: config.LiveSettings{
				Mode:         "stremio",
				ManifestURL:  stremio.URL + "/manifest.json",
				StreamFormat: "hls",
			},
		},
	})
	handler.SetUserSettingsService(fakeLiveUsageUserSettingsProvider{
		settings: map[string]*models.UserSettings{},
	})

	reqURL := "/live/hls/start?url=" + url.QueryEscape(stremio.URL+"/stream/sport/event.json") + "&format=hls"
	req := httptest.NewRequest(http.MethodGet, reqURL, nil)
	rec := httptest.NewRecorder()

	handler.StartLiveHLSSession(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}

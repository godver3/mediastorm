package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"novastream/config"
	"novastream/internal/liveusage"
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
	tracker := liveusage.GetTracker()
	tracker.StartRecording("rec-live-usage", "profile-1")
	defer tracker.EndRecording("rec-live-usage")

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

func TestStartLiveHLSSessionDirectIncludesProfileParams(t *testing.T) {
	handler := NewVideoHandlerWithProvider(true, "/bin/echo", "/bin/echo", t.TempDir(), nil)
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
			"profile-1": {ID: "profile-1", Name: "Living Room"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/live/hls/start?url=http%3A%2F%2Fexample.com%2Fchannel.ts&profileId=profile-1&mediaType=channel&itemId=tvg-1&title=Evening%20News", nil)
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
	if got := values.Get("mediaType"); got != "channel" {
		t.Fatalf("mediaType = %q, want channel", got)
	}
	if got := values.Get("itemId"); got != "tvg-1" {
		t.Fatalf("itemId = %q, want tvg-1", got)
	}
	if got := values.Get("title"); got != "Evening News" {
		t.Fatalf("title = %q, want Evening News", got)
	}
}

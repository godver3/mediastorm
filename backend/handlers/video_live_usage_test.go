package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/internal/liveusage"
	"novastream/models"
)

type fakeLiveUsageConfigProvider struct {
	settings config.Settings
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

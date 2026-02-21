package user_settings

import (
	"os"
	"path/filepath"
	"testing"

	"novastream/models"
)

func TestIsSettingsEmpty_Default(t *testing.T) {
	if !isSettingsEmpty(models.UserSettings{}) {
		t.Error("empty UserSettings should be considered empty")
	}
}

func TestIsSettingsEmpty_WithIPTVMode(t *testing.T) {
	s := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			Mode: models.StringPtr("xtream"),
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with LiveTV.Mode set should NOT be empty")
	}
}

func TestIsSettingsEmpty_WithIPTVXtreamHost(t *testing.T) {
	s := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			XtreamHost: models.StringPtr("http://host.com"),
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with LiveTV.XtreamHost set should NOT be empty")
	}
}

func TestIsSettingsEmpty_WithIPTVPlaylistURL(t *testing.T) {
	s := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			PlaylistURL: models.StringPtr("http://playlist.m3u"),
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with LiveTV.PlaylistURL set should NOT be empty")
	}
}

func TestIsSettingsEmpty_WithIPTVXtreamCredentials(t *testing.T) {
	s := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			XtreamUsername: models.StringPtr("user"),
			XtreamPassword: models.StringPtr("pass"),
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with LiveTV Xtream credentials set should NOT be empty")
	}
}

func TestUpdate_PreservesIPTVFields(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Save settings with IPTV override
	settings := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			Mode:           models.StringPtr("xtream"),
			XtreamHost:     models.StringPtr("http://host.com"),
			XtreamUsername: models.StringPtr("user1"),
			XtreamPassword: models.StringPtr("pass1"),
		},
	}

	if err := svc.Update("profile-1", settings); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify it was saved (not deleted as "empty")
	got, err := svc.Get("profile-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected settings to be saved, got nil")
	}
	if got.LiveTV.Mode == nil || *got.LiveTV.Mode != "xtream" {
		t.Errorf("Mode = %v, want 'xtream'", got.LiveTV.Mode)
	}
	if got.LiveTV.XtreamHost == nil || *got.LiveTV.XtreamHost != "http://host.com" {
		t.Errorf("XtreamHost = %v, want 'http://host.com'", got.LiveTV.XtreamHost)
	}

	// Verify file persisted on disk
	data, err := os.ReadFile(filepath.Join(dir, "user_settings.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("settings file should not be empty")
	}
}

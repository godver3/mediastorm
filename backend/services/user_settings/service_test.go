package user_settings

import (
	"os"
	"path/filepath"
	"testing"

	"novastream/models"
)

func TestSanitizeLanguageCode(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"eng", "eng"},
		{"'eng'", "eng"},
		{"''", ""},
		{"  fre  ", "fre"},
		{"\"jpn\"", "jpn"},
		{"'fra'", "fra"},
		{"  'deu'  ", "deu"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sanitizeLanguageCode(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeLanguageCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetWithDefaults_SanitizesQuotedLanguages(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Save settings with stray-quoted language codes
	settings := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredAudioLanguage:    "'fre'",
			PreferredSubtitleLanguage: "''",
			PreferredSubtitleMode:     "'forced-only'",
		},
	}
	if err := svc.Update("user1", settings); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify Update sanitized on save
	raw, _ := svc.Get("user1")
	if raw.Playback.PreferredAudioLanguage != "fre" {
		t.Errorf("Update should sanitize: got audioLang=%q, want %q", raw.Playback.PreferredAudioLanguage, "fre")
	}

	// Verify GetWithDefaults also sanitizes
	defaults := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredAudioLanguage: "'eng'",
		},
	}
	got, err := svc.GetWithDefaults("user1", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}
	if got.Playback.PreferredAudioLanguage != "fre" {
		t.Errorf("audioLang = %q, want %q", got.Playback.PreferredAudioLanguage, "fre")
	}
	if got.Playback.PreferredSubtitleLanguage != "" {
		t.Errorf("subLang = %q, want empty (''  should sanitize to empty)", got.Playback.PreferredSubtitleLanguage)
	}
	if got.Playback.PreferredSubtitleMode != "forced-only" {
		t.Errorf("subMode = %q, want %q", got.Playback.PreferredSubtitleMode, "forced-only")
	}
}

func TestGetWithDefaults_SanitizesDefaultsFallback(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// No user settings saved — should fall back to defaults and sanitize them
	defaults := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredAudioLanguage:    "'spa'",
			PreferredSubtitleLanguage: "\"eng\"",
		},
	}
	got, err := svc.GetWithDefaults("no-settings-user", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}
	if got.Playback.PreferredAudioLanguage != "spa" {
		t.Errorf("audioLang = %q, want %q", got.Playback.PreferredAudioLanguage, "spa")
	}
	if got.Playback.PreferredSubtitleLanguage != "eng" {
		t.Errorf("subLang = %q, want %q", got.Playback.PreferredSubtitleLanguage, "eng")
	}
}

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

func TestIsSettingsEmpty_WithLiveTVMaxStreams(t *testing.T) {
	limit := 3
	s := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			MaxStreams: &limit,
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with LiveTV.MaxStreams set should NOT be empty")
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

func TestUpdate_PreservesLiveTVMaxStreamsOnly(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	limit := 4
	settings := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			MaxStreams: &limit,
		},
	}

	if err := svc.Update("profile-2", settings); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := svc.Get("profile-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected settings to be saved, got nil")
	}
	if got.LiveTV.MaxStreams == nil || *got.LiveTV.MaxStreams != 4 {
		t.Fatalf("MaxStreams = %v, want 4", got.LiveTV.MaxStreams)
	}
}

func TestGetWithDefaults_BackfillsCalendarShelf(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.Update("user-calendar", models.UserSettings{
		HomeShelves: models.HomeShelvesSettings{
			Shelves: []models.ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
				{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 2},
			},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := svc.GetWithDefaults("user-calendar", models.DefaultUserSettings())
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}

	if len(got.HomeShelves.Shelves) != 4 {
		t.Fatalf("expected 4 shelves after backfill, got %d", len(got.HomeShelves.Shelves))
	}

	var calendar *models.ShelfConfig
	for i := range got.HomeShelves.Shelves {
		if got.HomeShelves.Shelves[i].ID == "calendar" {
			calendar = &got.HomeShelves.Shelves[i]
			break
		}
	}
	if calendar == nil {
		t.Fatal("expected calendar shelf to be backfilled")
	}
	if calendar.Name != "Coming Up" {
		t.Fatalf("expected calendar shelf name Coming Up, got %q", calendar.Name)
	}
	if calendar.Order != 1 {
		t.Fatalf("expected calendar shelf order 1, got %d", calendar.Order)
	}
	if !calendar.Enabled {
		t.Fatal("expected calendar shelf to be enabled by default")
	}
}

func TestLoad_MigratesMissingCalendarShelf(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "user-1": {
    "homeShelves": {
      "shelves": [
        { "id": "continue-watching", "name": "Continue Watching", "enabled": true, "order": 0 },
        { "id": "watchlist", "name": "Your Watchlist", "enabled": true, "order": 1 },
        { "id": "trending-movies", "name": "Trending Movies", "enabled": true, "order": 2 },
        { "id": "trending-tv", "name": "Trending TV Shows", "enabled": true, "order": 3 }
      ]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "user_settings.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	got, err := svc.Get("user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected migrated settings")
	}
	if len(got.HomeShelves.Shelves) != 5 {
		t.Fatalf("expected 5 shelves after migration, got %d", len(got.HomeShelves.Shelves))
	}

	var calendar *models.ShelfConfig
	for i := range got.HomeShelves.Shelves {
		if got.HomeShelves.Shelves[i].ID == "calendar" {
			calendar = &got.HomeShelves.Shelves[i]
			break
		}
	}
	if calendar == nil {
		t.Fatal("expected calendar shelf to be migrated in")
	}
	if calendar.Order != 1 {
		t.Fatalf("expected calendar shelf order 1, got %d", calendar.Order)
	}

	var watchlist *models.ShelfConfig
	for i := range got.HomeShelves.Shelves {
		if got.HomeShelves.Shelves[i].ID == "watchlist" {
			watchlist = &got.HomeShelves.Shelves[i]
			break
		}
	}
	if watchlist == nil {
		t.Fatal("expected watchlist shelf to remain after migration")
	}
	if watchlist.Order != 2 {
		t.Fatalf("expected watchlist to shift to order 2, got %d", watchlist.Order)
	}
}

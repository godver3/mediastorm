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

func TestGetWithDefaults_DisplayAppLanguageFallsBackToGlobal(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	defaults := models.UserSettings{
		Display: models.DisplaySettings{
			AppLanguage: "fr",
		},
	}

	got, err := svc.GetWithDefaults("no-settings-user", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}
	if got.Display.AppLanguage != "fr" {
		t.Fatalf("display.appLanguage = %q, want %q", got.Display.AppLanguage, "fr")
	}
}

func TestGetWithDefaults_DisplayAppearanceBackgroundColorFallsBackToGlobal(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.Update("user1", models.UserSettings{
		Display: models.DisplaySettings{
			Appearance: models.AppearanceSettings{
				ModalBackgroundColor: "#000000",
			},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	defaults := models.UserSettings{
		Display: models.DisplaySettings{
			Appearance: models.AppearanceSettings{
				BackgroundColor:      "#111111",
				ModalBackgroundColor: "#000000",
			},
		},
	}

	got, err := svc.GetWithDefaults("user1", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}
	if got.Display.Appearance.BackgroundColor != "#111111" {
		t.Fatalf("display.appearance.backgroundColor = %q, want %q", got.Display.Appearance.BackgroundColor, "#111111")
	}
}

func TestGetWithDefaults_DisplayAppLanguagePreservesUserOverride(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := svc.Update("user1", models.UserSettings{
		Display: models.DisplaySettings{
			AppLanguage: "en",
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	defaults := models.UserSettings{
		Display: models.DisplaySettings{
			AppLanguage:     "fr",
			BadgeVisibility: []string{"watchProgress"},
		},
	}

	got, err := svc.GetWithDefaults("user1", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}
	if got.Display.AppLanguage != "en" {
		t.Fatalf("display.appLanguage = %q, want %q", got.Display.AppLanguage, "en")
	}
	if len(got.Display.BadgeVisibility) != 1 || got.Display.BadgeVisibility[0] != "watchProgress" {
		t.Fatalf("badgeVisibility = %#v, want fallback defaults", got.Display.BadgeVisibility)
	}
}

func TestClearAppearanceOverrides_RemovesOnlyAppearance(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	fontScale := 1.2
	settings := models.UserSettings{
		Display: models.DisplaySettings{
			AppLanguage: "fr",
			Appearance: models.AppearanceSettings{
				FontScale:   &fontScale,
				AccentColor: "#ff00cc",
				TextColor:   "#ff1a1a",
			},
		},
	}
	if err := svc.Update("user1", settings); err != nil {
		t.Fatalf("Update: %v", err)
	}

	count, err := svc.ClearAppearanceOverrides()
	if err != nil {
		t.Fatalf("ClearAppearanceOverrides: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleared count = %d, want 1", count)
	}

	got, err := svc.Get("user1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-appearance settings to remain")
	}
	if got.Display.AppLanguage != "fr" {
		t.Fatalf("appLanguage = %q, want fr", got.Display.AppLanguage)
	}
	if appearanceSettingsSet(got.Display.Appearance) {
		t.Fatalf("appearance overrides were not cleared: %+v", got.Display.Appearance)
	}
}

func TestClearAppearanceOverrides_DeletesAppearanceOnlySettings(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	fontScale := 1.2
	if err := svc.Update("user1", models.UserSettings{
		Display: models.DisplaySettings{
			Appearance: models.AppearanceSettings{FontScale: &fontScale},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	count, err := svc.ClearAppearanceOverrides()
	if err != nil {
		t.Fatalf("ClearAppearanceOverrides: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleared count = %d, want 1", count)
	}

	got, err := svc.Get("user1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("appearance-only settings should be deleted, got %+v", got)
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

func TestIsSettingsEmpty_WithDisplayAppLanguage(t *testing.T) {
	s := models.UserSettings{
		Display: models.DisplaySettings{
			AppLanguage: "fr",
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with Display.AppLanguage set should NOT be empty")
	}
}

func TestIsSettingsEmpty_WithExplicitEmptyRequiredTerms(t *testing.T) {
	s := models.UserSettings{
		Filtering: models.FilterSettings{
			RequiredTerms: []string{},
		},
	}
	if isSettingsEmpty(s) {
		t.Error("settings with explicit empty RequiredTerms should NOT be empty")
	}
}

func TestUpdate_PreservesExplicitEmptyRequiredTermsOverride(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	settings := models.UserSettings{
		Filtering: models.FilterSettings{
			RequiredTerms: []string{},
		},
	}

	if err := svc.Update("profile-1", settings); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reloaded, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService reload: %v", err)
	}

	got, err := reloaded.Get("profile-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected saved settings, got nil")
	}
	if got.Filtering.RequiredTerms == nil {
		t.Fatal("RequiredTerms should remain a non-nil empty slice")
	}
	if len(got.Filtering.RequiredTerms) != 0 {
		t.Fatalf("RequiredTerms = %v, want empty slice", got.Filtering.RequiredTerms)
	}

	defaults := models.UserSettings{
		Filtering: models.FilterSettings{
			RequiredTerms: []string{"Multi"},
		},
	}
	merged, err := reloaded.GetWithDefaults("profile-1", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}
	if merged.Filtering.RequiredTerms == nil {
		t.Fatal("merged RequiredTerms should remain a non-nil empty slice")
	}
	if len(merged.Filtering.RequiredTerms) != 0 {
		t.Fatalf("merged RequiredTerms = %v, want explicit empty override", merged.Filtering.RequiredTerms)
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

	if len(got.HomeShelves.Shelves) != 5 {
		t.Fatalf("expected 5 shelves after backfill, got %d", len(got.HomeShelves.Shelves))
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
	if calendar.Order != 2 {
		t.Fatalf("expected calendar shelf order 2, got %d", calendar.Order)
	}
	if !calendar.Enabled {
		t.Fatal("expected calendar shelf to be enabled by default")
	}
}

func TestGetWithDefaults_InjectsNewLocalLibraryShelf(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// User saved settings that include only a movies library shelf
	if err := svc.Update("user-lib", models.UserSettings{
		HomeShelves: models.HomeShelvesSettings{
			Shelves: []models.ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "calendar", Name: "Coming Up", Enabled: true, Order: 1},
				{ID: "local-library-movies-id", Name: "My Movies", Enabled: true, Order: 2, Type: "local-library"},
			},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Defaults now include both movies and shows libraries
	defaults := models.DefaultUserSettings()
	defaults.HomeShelves.Shelves = append(defaults.HomeShelves.Shelves,
		models.ShelfConfig{ID: "local-library-movies-id", Name: "My Movies", Enabled: true, Order: 10, Type: "local-library"},
		models.ShelfConfig{ID: "local-library-shows-id", Name: "My Shows", Enabled: true, Order: 11, Type: "local-library"},
	)

	got, err := svc.GetWithDefaults("user-lib", defaults)
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}

	// Should have injected the new shows library shelf
	found := false
	for _, sh := range got.HomeShelves.Shelves {
		if sh.ID == "local-library-shows-id" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected new local-library-shows-id shelf to be injected")
	}

	// Should NOT have duplicated the existing movies library shelf
	count := 0
	for _, sh := range got.HomeShelves.Shelves {
		if sh.ID == "local-library-movies-id" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 movies shelf, got %d", count)
	}
}

func TestGetWithDefaults_DoesNotReaddRemovedBuiltinShelf(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// User saved settings without trending-tv (they removed it intentionally)
	if err := svc.Update("user-notrending", models.UserSettings{
		HomeShelves: models.HomeShelvesSettings{
			Shelves: []models.ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "calendar", Name: "Coming Up", Enabled: true, Order: 1},
				{ID: "trending-movies", Name: "Trending Movies", Enabled: true, Order: 2},
			},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := svc.GetWithDefaults("user-notrending", models.DefaultUserSettings())
	if err != nil {
		t.Fatalf("GetWithDefaults: %v", err)
	}

	for _, sh := range got.HomeShelves.Shelves {
		if sh.ID == "trending-tv" {
			t.Fatal("trending-tv should not be re-injected for a user who removed it")
		}
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
	if len(got.HomeShelves.Shelves) != 6 {
		t.Fatalf("expected 6 shelves after migration, got %d", len(got.HomeShelves.Shelves))
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
	if calendar.Order != 2 {
		t.Fatalf("expected calendar shelf order 2, got %d", calendar.Order)
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
	if watchlist.Order != 3 {
		t.Fatalf("expected watchlist to shift to order 3, got %d", watchlist.Order)
	}
}

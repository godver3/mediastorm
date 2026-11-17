package client_settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"novastream/models"
)

func TestLoadMigratesWatchlistNavigationVisibility(t *testing.T) {
	dir := t.TempDir()
	raw := `{"client-1":{"navigationTabVisibility":["home","search","lists"],"navigationTabVisibilityIncludesSystemTabs":true}}`
	if err := os.WriteFile(filepath.Join(dir, "client_settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("write client settings: %v", err)
	}

	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Legacy device-only key still readable when looking up any profile (fallback).
	got, err := svc.Get("client-1", "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.NavigationTabVisibility == nil {
		t.Fatal("expected migrated client navigation visibility")
	}
	if !containsNavigationTab(*got.NavigationTabVisibility, "watchlist") {
		t.Fatalf("navigationTabVisibility = %#v, want Watchlist added", *got.NavigationTabVisibility)
	}
	if got.NavigationTabVisibilityIncludesWatchlist == nil || !*got.NavigationTabVisibilityIncludesWatchlist {
		t.Fatal("expected Watchlist navigation migration marker")
	}
}

func TestUpdatePreservesExplicitlyHiddenWatchlistAcrossReload(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	tabs := []string{"home", "search", "lists"}
	if err := svc.Update("client-1", "user-1", models.ClientFilterSettings{NavigationTabVisibility: &tabs}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reloaded, err := NewService(dir)
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	got, err := reloaded.Get("client-1", "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.NavigationTabVisibility == nil {
		t.Fatal("expected client navigation visibility")
	}
	if containsNavigationTab(*got.NavigationTabVisibility, "watchlist") {
		t.Fatalf("navigationTabVisibility = %#v, Watchlist should remain hidden", *got.NavigationTabVisibility)
	}
}

func TestUpdateSanitizesHomeShelfFocusModel(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	center := " CENTER "
	if err := svc.Update("tv-1", "user-1", models.ClientFilterSettings{HomeShelfFocusModel: &center}); err != nil {
		t.Fatalf("Update valid focus model: %v", err)
	}
	got, err := svc.Get("tv-1", "user-1")
	if err != nil || got == nil || got.HomeShelfFocusModel == nil || *got.HomeShelfFocusModel != "center" {
		t.Fatalf("HomeShelfFocusModel = %#v, err = %v; want center", got, err)
	}

	invalid := "diagonal"
	if err := svc.Update("tv-1", "user-1", models.ClientFilterSettings{HomeShelfFocusModel: &invalid}); err != nil {
		t.Fatalf("Update invalid focus model: %v", err)
	}
	got, err = svc.Get("tv-1", "user-1")
	if err != nil {
		t.Fatalf("Get after invalid focus model: %v", err)
	}
	if got != nil {
		t.Fatalf("invalid focus model should be removed, got %#v", got.HomeShelfFocusModel)
	}
}

func containsNavigationTab(tabs []string, want string) bool {
	for _, tab := range tabs {
		if tab == want {
			return true
		}
	}
	return false
}

func TestServiceSanitizesAllowedTrackLanguages(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	languages := []string{" ENG ", "'fra'", "eng"}
	if err := svc.Update("client-languages", "user-1", models.ClientFilterSettings{AllowedTrackLanguages: &languages}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get("client-languages", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.AllowedTrackLanguages == nil || !reflect.DeepEqual(*got.AllowedTrackLanguages, []string{"eng", "fra"}) {
		t.Fatalf("AllowedTrackLanguages = %#v, want eng/fra", got)
	}
}

func TestTVDisplayOptionsMakeClientSettingsNonEmpty(t *testing.T) {
	for name, settings := range map[string]models.ClientFilterSettings{
		"showStreamSourceInfo":                   {ShowStreamSourceInfo: models.BoolPtr(false)},
		"hideContinueWatchingHeroMetadata":       {HideContinueWatchingHeroMetadata: models.BoolPtr(false)},
		"moveDetailsRatingsToMetadata":           {MoveDetailsRatingsToMetadata: models.BoolPtr(false)},
		"hideDetailsPoster":                      {HideDetailsPoster: models.BoolPtr(false)},
		"showDetailsPrequeueFilename":            {ShowDetailsPrequeueFilename: models.BoolPtr(false)},
		"showDetailsSelectedTracks":              {ShowDetailsSelectedTracks: models.BoolPtr(false)},
		"hideTvDrawerRail":                       {HideTVDrawerRail: models.BoolPtr(false)},
		"simpleMode":                             {SimpleMode: models.BoolPtr(false)},
		"simpleModeHomeShelves":                  {SimpleModeHomeShelves: models.StringSlicePtr([]string{"watchlist"})},
		"disableTvHomeCardDimming":               {DisableTVHomeCardDimming: models.BoolPtr(false)},
		"showSeriesBackdropForMissingEpisodeArt": {ShowSeriesBackdropForMissingEpisodeArt: models.BoolPtr(false)},
	} {
		if settings.IsEmpty() {
			t.Fatalf("%s should make client settings non-empty even when explicitly false", name)
		}
	}
}

func TestClearAppearanceOverrides_RemovesOnlyAppearance(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	accent := "#ff00cc"
	resolution := "1080p"
	if err := svc.Update("client1", "user-1", models.ClientFilterSettings{
		MaxResolution: &resolution,
		Appearance: &models.AppearanceSettings{
			AccentColor: accent,
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

	got, err := svc.Get("client1", "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-appearance settings to remain")
	}
	if got.Appearance != nil {
		t.Fatalf("appearance override was not cleared: %+v", got.Appearance)
	}
	if got.MaxResolution == nil || *got.MaxResolution != "1080p" {
		t.Fatalf("maxResolution = %+v, want 1080p", got.MaxResolution)
	}
}

func TestClearAppearanceOverrides_DeletesAppearanceOnlySettings(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	accent := "#ff00cc"
	if err := svc.Update("client1", "user-1", models.ClientFilterSettings{
		Appearance: &models.AppearanceSettings{
			AccentColor: accent,
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

	got, err := svc.Get("client1", "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("appearance-only settings should be deleted, got %+v", got)
	}
}

func TestPersonDeviceSettingsAreIsolated(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	resA := "720p"
	resB := "4K"
	if err := svc.Update("tv-1", "person-a", models.ClientFilterSettings{MaxResolution: &resA}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Update("tv-1", "person-b", models.ClientFilterSettings{MaxResolution: &resB}); err != nil {
		t.Fatal(err)
	}
	gotA, err := svc.Get("tv-1", "person-a")
	if err != nil || gotA == nil || gotA.MaxResolution == nil || *gotA.MaxResolution != "720p" {
		t.Fatalf("person-a settings = %+v err=%v", gotA, err)
	}
	gotB, err := svc.Get("tv-1", "person-b")
	if err != nil || gotB == nil || gotB.MaxResolution == nil || *gotB.MaxResolution != "4K" {
		t.Fatalf("person-b settings = %+v err=%v", gotB, err)
	}
}

func TestPruneInvalidSettingsLockedRemovesMissingForeignKeys(t *testing.T) {
	svc := &Service{settings: map[string]models.ClientFilterSettings{
		models.ClientSettingsKey("valid-client", "valid-user"):     {},
		models.ClientSettingsKey("missing-client", "valid-user"):   {},
		models.ClientSettingsKey("valid-client", "missing-user"):   {},
		models.ClientSettingsKey("missing-client", "missing-user"): {},
		"legacy-client-key": {},
	}}

	removed := svc.pruneInvalidSettingsLocked(
		map[string]struct{}{"valid-client": {}},
		map[string]struct{}{"valid-user": {}},
	)

	if len(removed) != 3 {
		t.Fatalf("removed %d settings, want 3", len(removed))
	}
	if _, ok := svc.settings[models.ClientSettingsKey("valid-client", "valid-user")]; !ok {
		t.Fatal("valid person-device settings were removed")
	}
	if _, ok := svc.settings["legacy-client-key"]; !ok {
		t.Fatal("legacy settings key should be left unchanged")
	}
	if len(svc.settings) != 2 {
		t.Fatalf("settings contains %d entries after pruning, want 2", len(svc.settings))
	}
}

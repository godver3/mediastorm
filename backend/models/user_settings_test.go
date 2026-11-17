package models

import (
	"reflect"
	"testing"
)

func TestMigrateLibraryShelfConfigs(t *testing.T) {
	shelves := []ShelfConfig{{ID: "local-library-library-123", Type: "local-library"}}

	if !MigrateLibraryShelfConfigs(shelves) {
		t.Fatal("expected legacy shelf migration")
	}
	if shelves[0].Type != "library" || shelves[0].LibraryID != "library-123" {
		t.Fatalf("unexpected migrated shelf: %+v", shelves[0])
	}
}

func TestStringPtr(t *testing.T) {
	s := StringPtr("hello")
	if s == nil || *s != "hello" {
		t.Fatal("StringPtr failed")
	}
}

func TestDefaultUserSettingsDisablesMatchFrameRate(t *testing.T) {
	settings := DefaultUserSettings()

	if settings.Playback.MatchFrameRate == nil {
		t.Fatal("expected match frame rate default to be set")
	}
	if *settings.Playback.MatchFrameRate {
		t.Fatal("expected match frame rate to default to disabled")
	}
}

func TestDefaultUserSettingsDisablesFrameSamplingFeatures(t *testing.T) {
	settings := DefaultUserSettings()

	options := map[string]*bool{
		"crop-detect subtitle positioning": settings.Playback.SubtitleUseCropDetectPosition,
		"credits detection":                settings.Playback.CreditsDetectionEnabled,
	}
	for name, option := range options {
		if option == nil {
			t.Fatalf("expected %s default to be set", name)
		}
		if *option {
			t.Fatalf("expected %s to default to disabled", name)
		}
	}
}

func TestDefaultUserSettingsEnablesLiveClosedCaptionExtraction(t *testing.T) {
	settings := DefaultUserSettings()

	if settings.Playback.LiveClosedCaptionExtraction == nil {
		t.Fatal("expected live closed caption extraction default to be set")
	}
	if !*settings.Playback.LiveClosedCaptionExtraction {
		t.Fatal("expected live closed caption extraction to default to enabled")
	}
}

func TestDefaultUserSettingsEnablesApplicationAnimations(t *testing.T) {
	settings := DefaultUserSettings()
	if settings.Display.EnableAnimations == nil || !*settings.Display.EnableAnimations {
		t.Fatal("application animations should be enabled by default")
	}
}

func TestDefaultUserSettingsPlacesExploreCardAtEnd(t *testing.T) {
	settings := DefaultUserSettings()
	if settings.HomeShelves.ExploreCardPosition != "end" {
		t.Fatalf("ExploreCardPosition = %q, want end", settings.HomeShelves.ExploreCardPosition)
	}
}

func TestDefaultUserSettingsShowsStreamSourceInfo(t *testing.T) {
	settings := DefaultUserSettings()
	if settings.Display.ShowStreamSourceInfo == nil || !*settings.Display.ShowStreamSourceInfo {
		t.Fatal("stream source information should be shown by default")
	}
}

func TestDefaultUserSettingsKeepsTVDisplayOptionsDisabled(t *testing.T) {
	settings := DefaultUserSettings()

	options := map[string]*bool{
		"hideContinueWatchingHeroMetadata": settings.Display.HideContinueWatchingHeroMetadata,
		"moveDetailsRatingsToMetadata":     settings.Display.MoveDetailsRatingsToMetadata,
		"hideDetailsPoster":                settings.Display.HideDetailsPoster,
		"showDetailsPrequeueFilename":      settings.Display.ShowDetailsPrequeueFilename,
		"showDetailsSelectedTracks":        settings.Display.ShowDetailsSelectedTracks,
		"hideTvDrawerRail":                 settings.Display.HideTVDrawerRail,
		"simpleMode":                       settings.Display.SimpleMode,
	}
	for name, option := range options {
		if option == nil || *option {
			t.Fatalf("%s should be explicitly disabled by default", name)
		}
	}
	if settings.Display.SimpleModeHomeShelves == nil {
		t.Fatal("simple mode home shelves should default to the built-in M.O.M. Mode™ list")
	}
	want := DefaultSimpleModeHomeShelfIDs()
	got := *settings.Display.SimpleModeHomeShelves
	if len(got) != len(want) {
		t.Fatalf("simple mode home shelves = %#v, want %#v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("simple mode home shelves = %#v, want %#v", got, want)
		}
	}
}

func TestDefaultUserSettingsUsesReviewedDisplayDefaults(t *testing.T) {
	settings := DefaultUserSettings()
	for name, option := range map[string]*bool{
		"disable TV home card dimming":      settings.Display.DisableTVHomeCardDimming,
		"series backdrop fallback":          settings.Display.ShowSeriesBackdropForMissingEpisodeArt,
		"blur unwatched episode thumbnails": settings.Display.BlurUnwatchedEpisodeThumbnails,
		"blur unwatched episode overviews":  settings.Display.BlurUnwatchedEpisodeOverviews,
	} {
		if option == nil || !*option {
			t.Fatalf("%s should be explicitly enabled by default", name)
		}
	}
	for name, option := range map[string]*bool{
		"blur current episode thumbnail": settings.Display.BlurUnwatchedEpisodeThumbnailsIncludeCurrent,
		"blur current episode overview":  settings.Display.BlurUnwatchedEpisodeOverviewsIncludeCurrent,
	} {
		if option == nil || *option {
			t.Fatalf("%s should be explicitly disabled by default", name)
		}
	}
}

func TestDefaultUserSettingsUsesReviewedPlaybackAndFilteringDefaults(t *testing.T) {
	settings := DefaultUserSettings()
	if settings.Playback.PrerollMode != "artwork" {
		t.Fatalf("PrerollMode = %q, want artwork", settings.Playback.PrerollMode)
	}
	if settings.Filtering.AdaptivePlaybackEnabled == nil || !*settings.Filtering.AdaptivePlaybackEnabled {
		t.Fatal("adaptive playback should be explicitly enabled by default")
	}
	if settings.Filtering.AdaptiveTargetBufferFactor == nil || *settings.Filtering.AdaptiveTargetBufferFactor != 0.7 {
		t.Fatal("adaptive playback buffer factor should default to 0.7")
	}
}

func TestEnsureDefaultHomeShelvesDisablesExperimentalTonightShelf(t *testing.T) {
	shelves := DefaultHomeShelfConfigs()
	for i := range shelves {
		if shelves[i].ID == "tonight" {
			shelves[i].Enabled = true
		}
	}

	migrated, changed := EnsureDefaultHomeShelves(shelves)
	if !changed {
		t.Fatal("expected enabled tonight shelf to trigger migration")
	}
	for _, shelf := range migrated {
		if shelf.ID == "tonight" {
			if shelf.Enabled {
				t.Fatal("expected migration to disable tonight shelf")
			}
			return
		}
	}
	t.Fatal("expected tonight shelf to remain present after migration")
}

func TestEnsureDefaultHomeShelvesBackfillsSharedActivitySettings(t *testing.T) {
	shelves := []ShelfConfig{
		{ID: "popular-on-server", Name: "Popular", Enabled: true},
		{ID: "recently-watched", Name: "Recent", Enabled: true},
	}

	migrated, changed := EnsureDefaultHomeShelves(shelves)
	if !changed {
		t.Fatal("expected missing shared-activity settings to trigger migration")
	}
	var popular, recent *ShelfConfig
	for i := range migrated {
		switch migrated[i].ID {
		case "popular-on-server":
			popular = &migrated[i]
		case "recently-watched":
			recent = &migrated[i]
		}
	}
	if popular == nil || popular.Limit != 20 || popular.ActivityWindowDays != 90 || popular.MinimumProfiles != 2 {
		t.Fatalf("unexpected popular defaults: %+v", popular)
	}
	if recent == nil || recent.Limit != 20 || recent.ActivityWindowDays != 14 || recent.MaxItemsPerProfile != 3 {
		t.Fatalf("unexpected recent defaults: %+v", recent)
	}
}

func TestEnsureDefaultHomeShelvesAddsDisabledDashboardShelf(t *testing.T) {
	migrated, changed := EnsureDefaultHomeShelves([]ShelfConfig{
		{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
		{ID: "recently-watched", Name: "Recently Watched", Enabled: false, Order: 1},
	})
	if !changed {
		t.Fatal("expected missing dashboard shelf to trigger migration")
	}
	for _, shelf := range migrated {
		if shelf.ID == "dashboard" {
			if shelf.Enabled {
				t.Fatal("dashboard shelf should be disabled by default")
			}
			if shelf.Name != "Dashboard" {
				t.Fatalf("unexpected dashboard shelf name %q", shelf.Name)
			}
			return
		}
	}
	t.Fatal("expected dashboard shelf to be added")
}

func TestEnsureDefaultHomeShelvesAddsDisabledPermanentPrequeueShelf(t *testing.T) {
	migrated, changed := EnsureDefaultHomeShelves([]ShelfConfig{
		{ID: "dashboard", Name: "Dashboard", Enabled: false, Order: 2},
	})
	if !changed {
		t.Fatal("expected missing permanent prequeue shelf to trigger migration")
	}
	for _, shelf := range migrated {
		if shelf.ID == "permanent-prequeue" {
			if shelf.Enabled {
				t.Fatal("permanent prequeue shelf should be disabled by default")
			}
			if shelf.Name != "Permanent Prequeue" {
				t.Fatalf("unexpected permanent prequeue shelf name %q", shelf.Name)
			}
			return
		}
	}
	t.Fatal("expected permanent prequeue shelf to be added")
}

func TestEnsureDefaultHomeShelvesAddsWatchSomethingBelowContinueWatching(t *testing.T) {
	migrated, changed := EnsureDefaultHomeShelves([]ShelfConfig{
		{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 3},
		{ID: "custom", Name: "Custom", Enabled: true, Order: 4},
	})
	if !changed {
		t.Fatal("expected Watch Something shelf to be backfilled")
	}

	continueOrder := -1
	var watchSomething *ShelfConfig
	for i := range migrated {
		switch migrated[i].ID {
		case "continue-watching":
			continueOrder = migrated[i].Order
		case "watch-something":
			watchSomething = &migrated[i]
		}
	}
	if watchSomething == nil {
		t.Fatal("Watch Something shelf was not added")
	}
	if !watchSomething.Enabled {
		t.Fatal("Watch Something shelf should default enabled")
	}
	if watchSomething.Order != continueOrder+1 {
		t.Fatalf("Watch Something order = %d, want %d", watchSomething.Order, continueOrder+1)
	}
}

func newGlobal() *ResolvedLiveSource {
	return &ResolvedLiveSource{
		Mode:                  "m3u",
		PlaylistURL:           "http://global.m3u",
		XtreamHost:            "http://global.host",
		XtreamUsername:        "guser",
		XtreamPassword:        "gpass",
		PlaylistCacheTTLHours: 6,
		ProbeSizeMB:           10,
		AnalyzeDurationSec:    5,
		LowLatency:            false,
		StreamFormat:          "hls",
		EnabledCategories:     []string{"News"},
		MaxChannels:           500,
	}
}

func TestResolveLiveSource_AllNil(t *testing.T) {
	profile := &LiveTVSettings{}
	g := newGlobal()
	r := ResolveLiveSource(profile, g)

	if r.Mode != "m3u" {
		t.Errorf("Mode = %q, want %q", r.Mode, "m3u")
	}
	if r.PlaylistURL != "http://global.m3u" {
		t.Errorf("PlaylistURL = %q, want %q", r.PlaylistURL, "http://global.m3u")
	}
	if r.PlaylistCacheTTLHours != 6 {
		t.Errorf("PlaylistCacheTTLHours = %d, want 6", r.PlaylistCacheTTLHours)
	}
	if r.LowLatency != false {
		t.Errorf("LowLatency = %v, want false", r.LowLatency)
	}
	if r.MaxChannels != 500 {
		t.Errorf("MaxChannels = %d, want 500", r.MaxChannels)
	}
}

func TestResolveLiveSource_NilProfile(t *testing.T) {
	g := newGlobal()
	g.Mode = "xtream"
	r := ResolveLiveSource(nil, g)
	if r.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", r.Mode, "xtream")
	}
}

func TestResolveLiveSource_OverrideFields(t *testing.T) {
	profile := &LiveTVSettings{
		Mode:           StringPtr("xtream"),
		XtreamHost:     StringPtr("http://profile.host"),
		XtreamUsername: StringPtr("puser"),
		XtreamPassword: StringPtr("ppass"),
	}

	r := ResolveLiveSource(profile, newGlobal())

	if r.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", r.Mode, "xtream")
	}
	if r.PlaylistURL != "http://global.m3u" {
		t.Errorf("PlaylistURL should fall back to global, got %q", r.PlaylistURL)
	}
	if r.XtreamHost != "http://profile.host" {
		t.Errorf("XtreamHost = %q, want %q", r.XtreamHost, "http://profile.host")
	}
}

func TestResolveLiveSource_OverrideManifestURL(t *testing.T) {
	profile := &LiveTVSettings{
		Mode:        StringPtr("stremio"),
		ManifestURL: StringPtr("https://profile.example/manifest.json"),
	}

	r := ResolveLiveSource(profile, newGlobal())

	if r.Mode != "stremio" {
		t.Errorf("Mode = %q, want stremio", r.Mode)
	}
	if r.ManifestURL != "https://profile.example/manifest.json" {
		t.Errorf("ManifestURL = %q, want profile manifest", r.ManifestURL)
	}
}

func TestResolveLiveSource_ProfilePlaylistSourcesOverrideGlobal(t *testing.T) {
	enabled := true
	override := true
	profile := &LiveTVSettings{
		SourcesOverride: &override,
		PlaylistSources: []LivePlaylistSource{
			{ID: "sports", Name: "Sports", PlaylistURL: "http://profile.example/sports.m3u", Enabled: &enabled},
		},
	}

	g := newGlobal()
	g.PlaylistSources = []LivePlaylistSource{
		{ID: "global", Name: "Global", PlaylistURL: "http://global.example/live.m3u", Enabled: &enabled},
	}

	r := ResolveLiveSource(profile, g)
	if len(r.PlaylistSources) != 1 {
		t.Fatalf("PlaylistSources length = %d, want 1", len(r.PlaylistSources))
	}
	if r.PlaylistSources[0].ID != "sports" {
		t.Errorf("PlaylistSources[0].ID = %q, want sports", r.PlaylistSources[0].ID)
	}
	if r.PlaylistURL != "http://global.m3u" {
		t.Errorf("legacy PlaylistURL fallback = %q, want global legacy URL", r.PlaylistURL)
	}
}

func TestResolveLiveSource_ExplicitEmptySourcesOverride(t *testing.T) {
	enabled := true
	override := true
	profile := &LiveTVSettings{
		SourcesOverride: &override,
	}
	g := newGlobal()
	g.Sources = []LivePlaylistSource{
		{ID: "global", Name: "Global", PlaylistURL: "http://global.example/live.m3u", Enabled: &enabled},
	}
	g.PlaylistSources = []LivePlaylistSource{
		{ID: "legacy", Name: "Legacy", PlaylistURL: "http://global.example/legacy.m3u", Enabled: &enabled},
	}

	r := ResolveLiveSource(profile, g)
	if len(r.Sources) != 0 {
		t.Fatalf("Sources length = %d, want 0", len(r.Sources))
	}
	if len(r.PlaylistSources) != 0 {
		t.Fatalf("PlaylistSources length = %d, want 0", len(r.PlaylistSources))
	}
}

func TestResolveLiveSource_PartialOverride(t *testing.T) {
	profile := &LiveTVSettings{
		XtreamUsername: StringPtr("override-user"),
	}

	g := newGlobal()
	g.Mode = "xtream"
	r := ResolveLiveSource(profile, g)

	if r.Mode != "xtream" {
		t.Errorf("Mode = %q, want %q", r.Mode, "xtream")
	}
	if r.XtreamUsername != "override-user" {
		t.Errorf("XtreamUsername = %q, want %q", r.XtreamUsername, "override-user")
	}
	if r.XtreamPassword != "gpass" {
		t.Errorf("XtreamPassword = %q, want %q", r.XtreamPassword, "gpass")
	}
}

func TestResolveLiveSource_TuningOverrides(t *testing.T) {
	cacheTTL := 12
	probe := 20
	lowLat := true
	profile := &LiveTVSettings{
		PlaylistCacheTTLHours: &cacheTTL,
		ProbeSizeMB:           &probe,
		LowLatency:            &lowLat,
	}

	r := ResolveLiveSource(profile, newGlobal())

	if r.PlaylistCacheTTLHours != 12 {
		t.Errorf("PlaylistCacheTTLHours = %d, want 12", r.PlaylistCacheTTLHours)
	}
	if r.ProbeSizeMB != 20 {
		t.Errorf("ProbeSizeMB = %d, want 20", r.ProbeSizeMB)
	}
	if r.AnalyzeDurationSec != 5 {
		t.Errorf("AnalyzeDurationSec should fall back to global (5), got %d", r.AnalyzeDurationSec)
	}
	if r.LowLatency != true {
		t.Errorf("LowLatency = %v, want true", r.LowLatency)
	}
}

func TestResolveLiveSource_FilteringOverrides(t *testing.T) {
	maxCh := 100
	profile := &LiveTVSettings{
		Filtering: &LiveTVFilterOverrides{
			EnabledCategories: []string{"Sports", "Movies"},
			MaxChannels:       &maxCh,
		},
	}

	r := ResolveLiveSource(profile, newGlobal())

	if len(r.EnabledCategories) != 2 || r.EnabledCategories[0] != "Sports" {
		t.Errorf("EnabledCategories = %v, want [Sports Movies]", r.EnabledCategories)
	}
	if r.MaxChannels != 100 {
		t.Errorf("MaxChannels = %d, want 100", r.MaxChannels)
	}
}

func TestResolveLiveSource_StreamFormatOverride(t *testing.T) {
	direct := "direct"
	profile := &LiveTVSettings{
		StreamFormat: &direct,
	}

	r := ResolveLiveSource(profile, newGlobal())

	if r.StreamFormat != "direct" {
		t.Errorf("StreamFormat = %q, want %q", r.StreamFormat, "direct")
	}
	// Other fields should remain at global defaults
	if r.Mode != "m3u" {
		t.Errorf("Mode = %q, want %q", r.Mode, "m3u")
	}
}

func TestResolveLiveSource_StreamFormatNilUsesGlobal(t *testing.T) {
	profile := &LiveTVSettings{} // StreamFormat is nil

	r := ResolveLiveSource(profile, newGlobal())

	if r.StreamFormat != "hls" {
		t.Errorf("StreamFormat = %q, want %q (global default)", r.StreamFormat, "hls")
	}
}

func TestResolveLiveSource_EPGTimeOffsetOverride(t *testing.T) {
	offset := -120
	profile := &LiveTVSettings{
		EPG: &EPGOverrides{
			TimeOffsetMinutes: &offset,
		},
	}

	g := newGlobal()
	g.EPGTimeOffsetMinutes = 60

	r := ResolveLiveSource(profile, g)

	if r.EPGTimeOffsetMinutes != -120 {
		t.Errorf("EPGTimeOffsetMinutes = %d, want -120", r.EPGTimeOffsetMinutes)
	}
}

func TestResolveLiveSource_EPGTimeOffsetNilUsesGlobal(t *testing.T) {
	profile := &LiveTVSettings{}

	g := newGlobal()
	g.EPGTimeOffsetMinutes = 30

	r := ResolveLiveSource(profile, g)

	if r.EPGTimeOffsetMinutes != 30 {
		t.Errorf("EPGTimeOffsetMinutes = %d, want 30 (global default)", r.EPGTimeOffsetMinutes)
	}
}

func TestDefaultUserNavigationDestinations(t *testing.T) {
	want := []string{"home", "watchlist", "search", "lists", "profiles", "settings"}
	if got := DefaultUserSettings().Display.NavigationTabVisibility; !reflect.DeepEqual(got, want) {
		t.Fatalf("default user navigation = %v, want %v", got, want)
	}
}

package user_settings

import (
	"os"
	"path/filepath"
	"testing"

	"novastream/config"
	"novastream/models"
)

// --- helpers ---

func tempService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func globalDefaults() config.Settings {
	return config.Settings{
		Playback: config.PlaybackSettings{
			PreferredPlayer:           "native",
			PreferredAudioLanguage:    "eng",
			PreferredSubtitleLanguage: "eng",
			PreferredSubtitleMode:     "off",
			SubtitleSize:              1.0,
		},
		Filtering: config.FilterSettings{
			MaxSizeMovieGB:   10,
			MaxSizeEpisodeGB: 5,
			MaxResolution:    "2160p",
			HDRDVPolicy:      config.HDRDVPolicy("hdr"),
			FilterOutTerms:   []string{"cam", "ts"},
			PreferredTerms:   []string{"remux"},
		},
		Display: config.DisplaySettings{
			BadgeVisibility:     []string{"watchProgress", "releaseStatus"},
			WatchStateIconStyle: "colored",
		},
		HomeShelves: config.HomeShelvesSettings{
			Shelves: []config.ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
			},
		},
		AnimeFiltering: config.AnimeFilteringSettings{
			AnimeLanguageEnabled:   true,
			AnimePreferredLanguage: "jpn",
		},
		Network: config.NetworkSettings{
			HomeWifiSSID:     "MyWiFi",
			HomeBackendUrl:   "http://192.168.1.1:7777",
			RemoteBackendUrl: "https://example.com:7777",
		},
		Ranking: config.RankingSettings{
			Criteria: []config.RankingCriterion{
				{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
				{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
			},
		},
	}
}

// --- mock types ---

type mockClientsLister struct {
	clients []models.Client
}

func (m *mockClientsLister) List() []models.Client { return m.clients }

type mockClientSettingsBatch struct {
	settings map[string]models.ClientFilterSettings
	saved    bool
}

func (m *mockClientSettingsBatch) GetAll() map[string]models.ClientFilterSettings {
	out := make(map[string]models.ClientFilterSettings, len(m.settings))
	for k, v := range m.settings {
		out[k] = v
	}
	return out
}
func (m *mockClientSettingsBatch) UpdateBatch(s map[string]models.ClientFilterSettings) error {
	m.settings = s
	m.saved = true
	return nil
}

// --- profile strip tests ---

func TestStripProfileStringFieldMatchesGlobal(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer:        "native",
			PreferredAudioLanguage: "eng",
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got := svc.settings["user1"]
	if got.Playback.PreferredPlayer != "" {
		t.Errorf("expected PreferredPlayer stripped, got %q", got.Playback.PreferredPlayer)
	}
	if got.Playback.PreferredAudioLanguage != "" {
		t.Errorf("expected PreferredAudioLanguage stripped, got %q", got.Playback.PreferredAudioLanguage)
	}
}

func TestStripProfileStringFieldDiffers(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer: "vlc",
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got := svc.settings["user1"]
	if got.Playback.PreferredPlayer != "vlc" {
		t.Errorf("expected PreferredPlayer preserved as 'vlc', got %q", got.Playback.PreferredPlayer)
	}
}

func TestStripProfileAlreadyEmpty(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer: "",
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	// Should still exist but be empty (which means it gets deleted by isSettingsEmpty)
	if _, ok := svc.settings["user1"]; ok {
		t.Errorf("expected empty profile to be deleted")
	}
}

func TestStripProfilePointerFieldMatches(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Filtering: models.FilterSettings{
			MaxSizeMovieGB: models.FloatPtr(10),
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	// Entry should be deleted since all fields are now empty
	if _, ok := svc.settings["user1"]; ok {
		t.Errorf("expected profile entry to be deleted after stripping")
	}
}

func TestStripProfilePointerFieldDiffers(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Filtering: models.FilterSettings{
			MaxSizeMovieGB: models.FloatPtr(20),
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got := svc.settings["user1"]
	if got.Filtering.MaxSizeMovieGB == nil || *got.Filtering.MaxSizeMovieGB != 20 {
		t.Error("expected MaxSizeMovieGB=20 preserved")
	}
}

func TestStripProfileExplicitEmptyRequiredTermsOverridePreserved(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()
	g.Filtering.RequiredTerms = []string{"Multi"}

	us := models.UserSettings{
		Filtering: models.FilterSettings{
			RequiredTerms: []string{},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got, ok := svc.settings["user1"]
	if !ok {
		t.Fatal("expected explicit empty RequiredTerms override to be preserved")
	}
	if got.Filtering.RequiredTerms == nil {
		t.Fatal("RequiredTerms should remain a non-nil empty slice")
	}
	if len(got.Filtering.RequiredTerms) != 0 {
		t.Fatalf("RequiredTerms = %v, want empty slice", got.Filtering.RequiredTerms)
	}
}

func TestStripProfileRequiredTermsMatchingGlobalStripped(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()
	g.Filtering.RequiredTerms = []string{"Multi", "French"}

	us := models.UserSettings{
		Filtering: models.FilterSettings{
			RequiredTerms: []string{"French", "Multi"},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		t.Fatal("expected matching RequiredTerms override to be stripped and entry removed")
	}
}

func TestStripProfileUnorderedSliceSameItems(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Filtering: models.FilterSettings{
			FilterOutTerms: []string{"ts", "cam"}, // Same items, different order
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		got := svc.settings["user1"]
		if got.Filtering.FilterOutTerms != nil {
			t.Errorf("expected FilterOutTerms stripped, got %v", got.Filtering.FilterOutTerms)
		}
	}
}

func TestStripProfileOrderedSliceSame(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Display: models.DisplaySettings{
			BadgeVisibility: []string{"watchProgress", "releaseStatus"},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		got := svc.settings["user1"]
		if got.Display.BadgeVisibility != nil {
			t.Errorf("expected BadgeVisibility stripped, got %v", got.Display.BadgeVisibility)
		}
	}
}

func TestStripProfileOrderedSliceDifferentOrder(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Display: models.DisplaySettings{
			BadgeVisibility: []string{"releaseStatus", "watchProgress"}, // Different order
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got := svc.settings["user1"]
	if got.Display.BadgeVisibility == nil {
		t.Error("expected BadgeVisibility preserved (different order)")
	}
}

func TestStripProfileShelfConfigsMatch(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		HomeShelves: models.HomeShelvesSettings{
			Shelves: []models.ShelfConfig{
				{ID: "continue-watching", Name: "Continue Watching", Enabled: true, Order: 0},
				{ID: "watchlist", Name: "Your Watchlist", Enabled: true, Order: 1},
			},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		got := svc.settings["user1"]
		if got.HomeShelves.Shelves != nil {
			t.Errorf("expected Shelves stripped, got %v", got.HomeShelves.Shelves)
		}
	}
}

func TestStripProfileLiveTVNeverStripped(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		LiveTV: models.LiveTVSettings{
			HiddenChannels:   []string{"ch1"},
			FavoriteChannels: []string{"ch2"},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got := svc.settings["user1"]
	if len(got.LiveTV.HiddenChannels) != 1 || got.LiveTV.HiddenChannels[0] != "ch1" {
		t.Error("expected LiveTV HiddenChannels preserved")
	}
}

func TestStripProfileHDRDVPolicy(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Filtering: models.FilterSettings{
			HDRDVPolicy: models.HDRDVPolicy("hdr"), // Matches global
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		got := svc.settings["user1"]
		if got.Filtering.HDRDVPolicy != "" {
			t.Errorf("expected HDRDVPolicy stripped, got %q", got.Filtering.HDRDVPolicy)
		}
	}
}

func TestStripProfileFullyEmptiedDeleted(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	// Profile that exactly matches global
	us := models.UserSettings{
		Playback: models.PlaybackSettings{
			PreferredPlayer: "native",
		},
		Filtering: models.FilterSettings{
			MaxSizeMovieGB: models.FloatPtr(10),
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		t.Error("expected fully stripped profile to be deleted")
	}
}

// --- client strip tests ---

func TestStripClientFieldMatchesEffective(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	// No profile override, so effective = global
	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				MaxSizeMovieGB: models.FloatPtr(10), // Matches global
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	if !clientsSvc.saved {
		t.Fatal("expected client settings to be saved")
	}
	if _, ok := clientsSvc.settings["client1"]; ok {
		t.Error("expected empty client entry to be deleted")
	}
}

func TestStripClientFieldDiffers(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				MaxSizeMovieGB: models.FloatPtr(20), // Differs from global
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	cs, ok := clientsSvc.settings["client1"]
	if !ok {
		t.Fatal("expected client entry to still exist")
	}
	if cs.MaxSizeMovieGB == nil || *cs.MaxSizeMovieGB != 20 {
		t.Error("expected MaxSizeMovieGB=20 preserved")
	}
}

func TestStripClientFieldAlreadyNil(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				MaxSizeMovieGB:   nil,                // Already nil
				MaxSizeEpisodeGB: models.FloatPtr(3), // Differs from global default (5)
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	cs := clientsSvc.settings["client1"]
	if cs.MaxSizeMovieGB != nil {
		t.Error("expected MaxSizeMovieGB to remain nil")
	}
	if cs.MaxSizeEpisodeGB == nil || *cs.MaxSizeEpisodeGB != 3 {
		t.Error("expected MaxSizeEpisodeGB=3 preserved (differs from global)")
	}
}

func TestStripCascade(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	// Profile overrides MaxSizeMovieGB to 10 (same as global) — will be stripped.
	// After stripping, effective MaxSizeMovieGB = global = 10.
	// Client also has MaxSizeMovieGB=10 — should also be stripped.
	svc.settings["user1"] = models.UserSettings{
		Filtering: models.FilterSettings{
			MaxSizeMovieGB: models.FloatPtr(10),
		},
	}

	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				MaxSizeMovieGB: models.FloatPtr(10),
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	// Profile should be stripped
	if _, ok := svc.settings["user1"]; ok {
		t.Error("expected profile entry to be deleted")
	}

	// Client should be stripped
	if _, ok := clientsSvc.settings["client1"]; ok {
		t.Error("expected client entry to be deleted")
	}
}

func TestStripCascadeWithProfileOverride(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	// Profile overrides MaxSizeMovieGB to 20 (differs from global 10) — preserved.
	// Client has MaxSizeMovieGB=20 (matches effective profile) — should be stripped.
	svc.settings["user1"] = models.UserSettings{
		Filtering: models.FilterSettings{
			MaxSizeMovieGB: models.FloatPtr(20),
		},
	}

	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				MaxSizeMovieGB: models.FloatPtr(20),
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	// Profile should be preserved
	got := svc.settings["user1"]
	if got.Filtering.MaxSizeMovieGB == nil || *got.Filtering.MaxSizeMovieGB != 20 {
		t.Error("expected profile MaxSizeMovieGB=20 preserved")
	}

	// Client should be stripped
	if _, ok := clientsSvc.settings["client1"]; ok {
		t.Error("expected client entry to be deleted (matches effective profile)")
	}
}

func TestStripMultipleProfilesIndependent(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	// user1: matches global → stripped
	svc.settings["user1"] = models.UserSettings{
		Playback: models.PlaybackSettings{PreferredPlayer: "native"},
	}
	// user2: differs → preserved
	svc.settings["user2"] = models.UserSettings{
		Playback: models.PlaybackSettings{PreferredPlayer: "vlc"},
	}

	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		t.Error("expected user1 to be deleted")
	}
	got := svc.settings["user2"]
	if got.Playback.PreferredPlayer != "vlc" {
		t.Error("expected user2 to be preserved")
	}
}

func TestStripPersistsToFile(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}

	g := globalDefaults()
	svc.settings["user1"] = models.UserSettings{
		Playback: models.PlaybackSettings{PreferredPlayer: "native"},
	}

	svc.StripRedundantOverrides(g, nil, nil)

	// Reload from disk
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc2.settings["user1"]; ok {
		t.Error("expected stripped profile to be persisted to disk")
	}

	// Verify the file exists and is valid JSON
	data, err := os.ReadFile(filepath.Join(dir, "user_settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty settings file")
	}
}

// --- comparison helper tests ---

func TestStringSliceEqualUnordered(t *testing.T) {
	tests := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{}, []string{}, true},
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
	}
	for _, tt := range tests {
		if got := stringSliceEqualUnordered(tt.a, tt.b); got != tt.want {
			t.Errorf("stringSliceEqualUnordered(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestStringSliceEqualOrdered(t *testing.T) {
	tests := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{}, []string{}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a", "b"}, []string{"b", "a"}, false},
	}
	for _, tt := range tests {
		if got := stringSliceEqualOrdered(tt.a, tt.b); got != tt.want {
			t.Errorf("stringSliceEqualOrdered(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestStripClientAnimeFiltering(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				AnimeLanguageEnabled:   models.BoolPtr(true),    // Matches global
				AnimePreferredLanguage: models.StringPtr("jpn"), // Matches global
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	if _, ok := clientsSvc.settings["client1"]; ok {
		t.Error("expected client entry to be deleted (matches effective profile)")
	}
}

func TestStripClientHDRDVPolicy(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	hdrPolicy := models.HDRDVPolicy("hdr")
	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				HDRDVPolicy: &hdrPolicy, // Matches global
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	if _, ok := clientsSvc.settings["client1"]; ok {
		t.Error("expected client entry to be deleted")
	}
}

func TestStripClientStringSlicePointer(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	terms := []string{"cam", "ts"}
	clientsSvc := &mockClientSettingsBatch{
		settings: map[string]models.ClientFilterSettings{
			"client1": {
				FilterOutTerms: &terms, // Matches global (unordered)
			},
		},
	}
	clientsLister := &mockClientsLister{
		clients: []models.Client{{ID: "client1", UserID: "user1"}},
	}

	svc.StripRedundantOverrides(g, clientsLister, clientsSvc)

	if _, ok := clientsSvc.settings["client1"]; ok {
		t.Error("expected client entry to be deleted")
	}
}

func TestStripProfileSubtitleSize(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Playback: models.PlaybackSettings{
			SubtitleSize: 1.0, // Matches global
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		t.Error("expected profile to be deleted after stripping SubtitleSize")
	}
}

func TestStripProfileRankingMatches(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Ranking: &models.UserRankingSettings{
			Criteria: []models.UserRankingCriterion{
				{ID: config.RankingResolution, Enabled: models.BoolPtr(true), Order: intPtr(0)},
				{ID: config.RankingSize, Enabled: models.BoolPtr(true), Order: intPtr(1)},
			},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	if _, ok := svc.settings["user1"]; ok {
		t.Error("expected profile to be deleted after stripping matching ranking")
	}
}

func TestStripProfileRankingDiffers(t *testing.T) {
	svc := tempService(t)
	g := globalDefaults()

	us := models.UserSettings{
		Ranking: &models.UserRankingSettings{
			Criteria: []models.UserRankingCriterion{
				{ID: config.RankingResolution, Enabled: models.BoolPtr(false), Order: intPtr(0)}, // Differs
			},
		},
	}
	svc.settings["user1"] = us
	svc.StripRedundantOverrides(g, nil, nil)

	got := svc.settings["user1"]
	if got.Ranking == nil {
		t.Error("expected ranking to be preserved")
	}
}

func intPtr(v int) *int { return &v }

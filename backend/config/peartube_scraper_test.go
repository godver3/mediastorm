package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PearTube is a search source, so it is configured where the other search
// sources are. These pin the move: an install that predates it keeps working,
// and the scraper list is the only place the relay is read from afterwards.

func writeSettingsFile(t *testing.T, body string) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return NewManager(path)
}

func findPearTubeScraper(t *testing.T, settings Settings) TorrentScraperConfig {
	t.Helper()
	var found []TorrentScraperConfig
	for _, entry := range settings.TorrentScrapers {
		if entry.Type == TorrentScraperTypePearTube {
			found = append(found, entry)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one peartube scraper, got %d (%+v)", len(found), settings.TorrentScrapers)
	}
	return found[0]
}

func TestLegacyPearTubeBlockBecomesVersionedScraperPolicy(t *testing.T) {
	manager := writeSettingsFile(t, `{"peartube":{"relayUrl":"http://127.0.0.1:8174","enabled":true,"autoSeed":true}}`)
	settings, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	entry := findPearTubeScraper(t, settings)
	if entry.URL != "http://127.0.0.1:8174" || !entry.Enabled {
		t.Fatalf("relay configuration did not survive migration: %+v", entry)
	}
	if _, exists := entry.Config["autoSeed"]; exists {
		t.Fatal("legacy autoSeed was dual-written")
	}
	if entry.Config[PearTubeConfigConsentVersion] != "1" ||
		entry.Config[PearTubeConfigContributeWatchedMedia] != "true" ||
		entry.Config[PearTubeConfigArchiveEnabled] != "false" ||
		entry.Config[PearTubeConfigMigrationRequired] != "false" {
		t.Fatalf("versioned consent policy = %v", entry.Config)
	}
	if settings.PearTube != nil {
		t.Errorf("legacy block survived migration: %+v", settings.PearTube)
	}
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save migrated settings: %v", err)
	}
	data, err := os.ReadFile(manager.path)
	if err != nil {
		t.Fatalf("read saved settings: %v", err)
	}
	if strings.Contains(string(data), `"peartube":`) {
		t.Fatal("save dual-wrote the removed global peartube block")
	}
}

func TestFirstPearTubeScraperIsTheOnlySourceOfTruth(t *testing.T) {
	settings := Settings{TorrentScrapers: []TorrentScraperConfig{
		{
			Name:    "First relay",
			Type:    TorrentScraperTypePearTube,
			URL:     "http://first:8174",
			Enabled: false,
			Config: map[string]string{
				PearTubeConfigConsentVersion:         "1",
				PearTubeConfigMigrationRequired:      "false",
				PearTubeConfigContributeWatchedMedia: "false",
				PearTubeConfigContributionBudget:     "21",
				PearTubeConfigArchiveEnabled:         "false",
				PearTubeConfigArchiveBudget:          "144",
			},
		},
		{
			Name:    "Second relay",
			Type:    TorrentScraperTypePearTube,
			URL:     "http://second:8174",
			Enabled: true,
			Config: map[string]string{
				PearTubeConfigConsentVersion:         "1",
				PearTubeConfigMigrationRequired:      "false",
				PearTubeConfigContributeWatchedMedia: "true",
				PearTubeConfigContributionBudget:     "21",
				PearTubeConfigArchiveEnabled:         "true",
				PearTubeConfigArchiveBudget:          "144",
			},
		},
	}}

	got := settings.PearTubeConfig()
	if got.RelayURL != "http://first:8174" || got.Enabled == nil || *got.Enabled {
		t.Fatalf("first scraper did not win: %+v", got)
	}
	if got.ContributeWatchedMedia || got.ArchiveEnabled || got.EffectiveMode() != PearTubeModeWatchOnly {
		t.Fatalf("later scraper changed first scraper policy: %+v", got)
	}
}

func TestPearTubeAbsentIsMigrationRequiredWatchOnly(t *testing.T) {
	settings := Settings{TorrentScrapers: []TorrentScraperConfig{
		{Name: "Torrentio", Type: "torrentio", Enabled: true},
	}}

	got := settings.PearTubeConfig()
	if got.RelayURL != "" || got.Enabled != nil {
		t.Fatalf("absent relay configuration = %+v", got)
	}
	if !got.MigrationRequired || got.ContributeWatchedMedia || got.ArchiveEnabled ||
		got.EffectiveMode() != PearTubeModeMigrationRequired {
		t.Fatalf("absent policy was not safe watch-only: %+v", got)
	}
}

func TestPearTubeBudgetsAreClampedIndependentlyOnSave(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "settings.json"))
	settings := DefaultSettings()
	settings.TorrentScrapers = []TorrentScraperConfig{{
		Name:    "PearTube",
		Type:    TorrentScraperTypePearTube,
		URL:     "http://relay:8174",
		Enabled: true,
		Config: map[string]string{
			PearTubeConfigConsentVersion:         "1",
			PearTubeConfigMigrationRequired:      "false",
			PearTubeConfigContributeWatchedMedia: "true",
			PearTubeConfigContributionBudget:     "-9",
			PearTubeConfigArchiveEnabled:         "true",
			PearTubeConfigArchiveBudget:          "999999",
		},
	}}
	if err := manager.Save(settings); err != nil {
		t.Fatalf("save: %v", err)
	}
	saved, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := saved.PearTubeConfig()
	if got.ContributionBudget != PearTubeContributionBudgetMinGiB {
		t.Errorf("contribution budget = %d, want %d GiB", got.ContributionBudget, PearTubeContributionBudgetMinGiB)
	}
	if got.ArchiveBudget != PearTubeArchiveBudgetMaxGiB {
		t.Errorf("archive budget = %d, want %d GiB", got.ArchiveBudget, PearTubeArchiveBudgetMaxGiB)
	}
	if !got.ContributeWatchedMedia || !got.ArchiveEnabled {
		t.Fatalf("budget normalization changed explicit roles: %+v", got)
	}
}

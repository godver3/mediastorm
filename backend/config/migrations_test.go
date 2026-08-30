package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestMigrateGlobalLiveProxyToDefaultSource(t *testing.T) {
	settings := DefaultSettings()
	settings.Live.ProxyURL = " socks5://127.0.0.1:18080 "
	settings.Live.Sources = []LivePlaylistSource{
		{ID: "test", Name: "Test"},
		{ID: "default", Name: "Default"},
		{ID: "other", Name: "Other", ProxyURL: "http://other-proxy:8080"},
	}

	if !MigrateGlobalLiveProxyToDefaultSource(&settings) {
		t.Fatal("migration reported no change")
	}
	if settings.Live.ProxyURL != "" {
		t.Fatalf("Live.ProxyURL = %q, want unset", settings.Live.ProxyURL)
	}
	if settings.Live.Sources[0].ProxyURL != "" {
		t.Fatalf("non-default source proxy = %q, want unchanged", settings.Live.Sources[0].ProxyURL)
	}
	if settings.Live.Sources[1].ProxyURL != "socks5://127.0.0.1:18080" {
		t.Fatalf("default source proxy = %q, want migrated proxy", settings.Live.Sources[1].ProxyURL)
	}
	if settings.Live.Sources[2].ProxyURL != "http://other-proxy:8080" {
		t.Fatalf("existing source proxy = %q, want unchanged", settings.Live.Sources[2].ProxyURL)
	}
	if MigrateGlobalLiveProxyToDefaultSource(&settings) {
		t.Fatal("second migration reported a change")
	}
}

func TestMigratePrewarmContinueWatchingOnly(t *testing.T) {
	raw := map[string]interface{}{
		"scheduledTasks": map[string]interface{}{
			"tasks": []interface{}{
				map[string]interface{}{
					"type": "prewarm",
					"config": map[string]interface{}{
						"shelfSelections":     `[{"id":"watchlist","itemScope":"all"}]`,
						"stableReresolveDays": "7",
					},
				},
			},
		},
	}

	MigrateRawSettings(raw)
	tasks := raw["scheduledTasks"].(map[string]interface{})["tasks"].([]interface{})
	configMap := tasks[0].(map[string]interface{})["config"].(map[string]interface{})
	var selections []map[string]interface{}
	if err := json.Unmarshal([]byte(configMap["shelfSelections"].(string)), &selections); err != nil {
		t.Fatalf("decode migrated shelf selections: %v", err)
	}
	if len(selections) != 1 || selections[0]["id"] != "continue-watching" || selections[0]["playedWithinDays"] != float64(14) {
		t.Fatalf("migrated selections = %#v, want Continue Watching/14 days", selections)
	}
	if configMap["stableReresolveDays"] != "7" {
		t.Fatalf("stableReresolveDays = %#v, want preserved", configMap["stableReresolveDays"])
	}
}

func TestMigrateGlobalLiveProxyToDefaultSourcePreservesExistingDefaultProxy(t *testing.T) {
	settings := DefaultSettings()
	settings.Live.ProxyURL = "socks5://global-proxy:1080"
	settings.Live.Sources = []LivePlaylistSource{
		{ID: "default", Name: "Default", ProxyURL: "http://default-proxy:8080"},
	}

	if !MigrateGlobalLiveProxyToDefaultSource(&settings) {
		t.Fatal("migration reported no change")
	}
	if settings.Live.ProxyURL != "" {
		t.Fatalf("Live.ProxyURL = %q, want unset", settings.Live.ProxyURL)
	}
	if settings.Live.Sources[0].ProxyURL != "http://default-proxy:8080" {
		t.Fatalf("default source proxy = %q, want existing value preserved", settings.Live.Sources[0].ProxyURL)
	}
}

func TestPearTubeConsentMigrationTruthTable(t *testing.T) {
	tests := []struct {
		name              string
		body              string
		wantContribute    bool
		wantArchive       bool
		wantMigration     bool
		wantEffectiveMode string
	}{
		{
			name:              "missing legacy value",
			body:              `{"peartube":{"relayUrl":"http://legacy:8174","enabled":true}}`,
			wantMigration:     true,
			wantEffectiveMode: PearTubeModeMigrationRequired,
		},
		{
			name:              "malformed legacy value",
			body:              `{"peartube":{"relayUrl":"http://legacy:8174","enabled":true,"autoSeed":"yes"}}`,
			wantMigration:     true,
			wantEffectiveMode: PearTubeModeMigrationRequired,
		},
		{
			name:              "missing scraper value",
			body:              `{"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true}]}`,
			wantMigration:     true,
			wantEffectiveMode: PearTubeModeMigrationRequired,
		},
		{
			name:              "malformed scraper value",
			body:              `{"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true,"config":{"autoSeed":"sometimes"}}]}`,
			wantMigration:     true,
			wantEffectiveMode: PearTubeModeMigrationRequired,
		},
		{
			name:              "persisted legacy false",
			body:              `{"peartube":{"relayUrl":"http://legacy:8174","enabled":true,"autoSeed":false}}`,
			wantEffectiveMode: PearTubeModeWatchOnly,
		},
		{
			name:              "persisted legacy true",
			body:              `{"peartube":{"relayUrl":"http://legacy:8174","enabled":true,"autoSeed":true}}`,
			wantContribute:    true,
			wantEffectiveMode: PearTubeModeContributor,
		},
		{
			name:              "persisted scraper false",
			body:              `{"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true,"config":{"autoSeed":"false"}}]}`,
			wantEffectiveMode: PearTubeModeWatchOnly,
		},
		{
			name:              "persisted scraper true",
			body:              `{"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true,"config":{"autoSeed":"true"}}]}`,
			wantContribute:    true,
			wantEffectiveMode: PearTubeModeContributor,
		},
		{
			name: "partial current policy is not consent",
			body: `{"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true,"config":{
				"consentVersion":"1","contributeWatchedMedia":"true","contributionBudget":"21"
			}}]}`,
			wantMigration:     true,
			wantEffectiveMode: PearTubeModeMigrationRequired,
		},
		{
			name: "current explicit archive consent",
			body: `{"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true,"config":{
				"consentVersion":"1","migrationRequired":"false","contributeWatchedMedia":"false","contributionBudget":"21",
				"archiveEnabled":"true","archiveBudget":"144"
			}}]}`,
			wantArchive:       true,
			wantEffectiveMode: PearTubeModeArchiveEnabled,
		},
		{
			name: "scraper list wins over stale legacy block",
			body: `{
				"peartube":{"relayUrl":"http://legacy:8174","enabled":true,"autoSeed":true},
				"torrentScrapers":[{"name":"PearTube","type":"peartube","url":"http://list:8174","enabled":true,"config":{"autoSeed":"false"}}]
			}`,
			wantEffectiveMode: PearTubeModeWatchOnly,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := writeSettingsFile(t, test.body)
			settings, err := manager.Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			policy := settings.PearTubeConfig()
			if policy.ContributeWatchedMedia != test.wantContribute {
				t.Errorf("contribute = %v, want %v", policy.ContributeWatchedMedia, test.wantContribute)
			}
			if policy.ArchiveEnabled != test.wantArchive {
				t.Errorf("archive = %v, want %v", policy.ArchiveEnabled, test.wantArchive)
			}
			if policy.MigrationRequired != test.wantMigration {
				t.Errorf("migrationRequired = %v, want %v", policy.MigrationRequired, test.wantMigration)
			}
			if mode := policy.EffectiveMode(); mode != test.wantEffectiveMode {
				t.Errorf("effective mode = %q, want %q", mode, test.wantEffectiveMode)
			}
			if test.wantEffectiveMode != PearTubeModeArchiveEnabled && policy.ArchiveEnabled {
				t.Error("migration inferred archive consent")
			}
		})
	}
}

func TestMixedLegacyAndPartialCurrentPearTubePolicyStaysWatchOnly(t *testing.T) {
	partialFields := []struct {
		key   string
		value string
	}{
		{PearTubeConfigConsentVersion, "0"},
		{PearTubeConfigMigrationRequired, "false"},
		{PearTubeConfigContributeWatchedMedia, "true"},
		{PearTubeConfigContributionBudget, "21"},
		{PearTubeConfigArchiveEnabled, "true"},
		{PearTubeConfigArchiveBudget, "144"},
	}

	for _, autoSeed := range []string{"false", "true"} {
		for _, partial := range partialFields {
			t.Run("autoSeed_"+autoSeed+"_with_"+partial.key, func(t *testing.T) {
				body := fmt.Sprintf(`{"torrentScrapers":[{
					"name":"PearTube","type":"peartube","url":"http://relay:8174","enabled":true,
					"config":{"autoSeed":%q,%q:%q}
				}]}`, autoSeed, partial.key, partial.value)
				manager := writeSettingsFile(t, body)

				first, err := manager.Load()
				if err != nil {
					t.Fatalf("first load: %v", err)
				}
				assertMigrationRequiredWatchOnly(t, first.PearTubeConfig())
				if err := manager.Save(first); err != nil {
					t.Fatalf("save: %v", err)
				}
				second, err := manager.Load()
				if err != nil {
					t.Fatalf("second load: %v", err)
				}
				assertMigrationRequiredWatchOnly(t, second.PearTubeConfig())
				entry := findPearTubeScraper(t, second)
				if _, exists := entry.Config["autoSeed"]; exists {
					t.Fatal("legacy autoSeed survived repeated load/save")
				}
			})
		}
	}
}

func assertMigrationRequiredWatchOnly(t *testing.T, policy PearTubeSettings) {
	t.Helper()
	if !policy.MigrationRequired || policy.ContributeWatchedMedia || policy.ArchiveEnabled ||
		policy.EffectiveMode() != PearTubeModeMigrationRequired {
		t.Fatalf("policy is not migration-required watch-only: %+v", policy)
	}
}

func TestPearTubeConsentMigrationIsIdempotentAcrossSaveAndLoad(t *testing.T) {
	manager := writeSettingsFile(t, `{
		"peartube":{"relayUrl":"http://legacy:8174","enabled":true,"autoSeed":true}
	}`)
	first, err := manager.Load()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	firstEntry := findPearTubeScraper(t, first)
	if _, exists := firstEntry.Config["autoSeed"]; exists {
		t.Fatal("legacy autoSeed survived migration")
	}
	if err := manager.Save(first); err != nil {
		t.Fatalf("save migrated settings: %v", err)
	}
	second, err := manager.Load()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	secondEntry := findPearTubeScraper(t, second)
	if !reflect.DeepEqual(firstEntry.Config, secondEntry.Config) {
		t.Fatalf("policy changed after repeated load/save: first=%v second=%v", firstEntry.Config, secondEntry.Config)
	}
	if got := second.PearTubeConfig(); !got.ContributeWatchedMedia || got.ArchiveEnabled || got.MigrationRequired {
		t.Fatalf("effective policy changed after repeated load/save: %+v", got)
	}
}

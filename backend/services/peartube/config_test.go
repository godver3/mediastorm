package peartube

import (
	"testing"

	"novastream/config"
)

func TestStoredConsentIsIndependentFromRelayEnvironment(t *testing.T) {
	env := func(key string) string {
		switch key {
		case RelayURLEnv:
			return "http://from-env:8178"
		case EnabledEnv:
			return "1"
		case "PEARTUBE_AUTOSEED":
			return "1"
		}
		return ""
	}

	resolved := resolve(config.PearTubeSettings{
		RelayURL:               "http://from-settings:9000",
		Enabled:                new(true),
		ConsentVersion:         config.PearTubeConsentVersion,
		ContributeWatchedMedia: false,
		ContributionBudget:     config.PearTubeDefaultContributionBudgetGiB,
		ArchiveEnabled:         false,
		ArchiveBudget:          config.PearTubeDefaultArchiveBudgetGiB,
		MigrationRequired:      false,
	}, env)

	if resolved.RelayURL != "http://from-settings:9000" || !resolved.Enabled {
		t.Fatalf("stored relay settings did not win: %+v", resolved)
	}
	if resolved.ContributeWatchedMedia || resolved.ArchiveEnabled || resolved.EffectiveMode != config.PearTubeModeWatchOnly {
		t.Fatalf("relay environment implied consent: %+v", resolved)
	}
	if resolved.RelayURLFromEnv || resolved.EnabledFromEnv {
		t.Fatalf("stored relay values were attributed to environment: %+v", resolved)
	}
}

func TestStoredDisableBeatsAnEnabledEnvironment(t *testing.T) {
	env := func(key string) string {
		switch key {
		case RelayURLEnv:
			return "http://from-env:8178"
		case EnabledEnv:
			return "true"
		}
		return ""
	}

	resolved := resolve(config.PearTubeSettings{Enabled: new(false)}, env)
	if resolved.Enabled || resolved.RelayURL != "" {
		t.Fatalf("stored disable was overridden: %+v", resolved)
	}
	if resolved.EffectiveMode != config.PearTubeModeMigrationRequired {
		t.Fatalf("missing consent did not remain migration-required: %+v", resolved)
	}
}

func TestEnvironmentOnlyRelayIsMigrationRequiredWatchOnly(t *testing.T) {
	env := func(key string) string {
		switch key {
		case RelayURLEnv:
			return "http://from-env:8178"
		case "PEARTUBE_AUTOSEED":
			return "true"
		}
		return ""
	}

	resolved := resolve(config.PearTubeSettings{MigrationRequired: true}, env)
	if resolved.RelayURL != "http://from-env:8178" || !resolved.RelayURLFromEnv || !resolved.Enabled {
		t.Fatalf("relay availability did not resolve from environment: %+v", resolved)
	}
	if resolved.ContributeWatchedMedia || resolved.ArchiveEnabled ||
		resolved.EffectiveMode != config.PearTubeModeMigrationRequired {
		t.Fatalf("environment-only relay gained consent: %+v", resolved)
	}
}

func TestEmptyRelayURLLeavesIntegrationOffWithoutChangingPolicy(t *testing.T) {
	resolved := resolve(config.PearTubeSettings{
		RelayURL:               "   ",
		ConsentVersion:         config.PearTubeConsentVersion,
		ContributeWatchedMedia: true,
		ContributionBudget:     config.PearTubeDefaultContributionBudgetGiB,
		ArchiveEnabled:         false,
		ArchiveBudget:          config.PearTubeDefaultArchiveBudgetGiB,
	}, func(string) string { return "" })
	if resolved.Enabled || resolved.RelayURL != "" {
		t.Fatalf("empty relay URL left integration on: %+v", resolved)
	}
	if !resolved.ContributeWatchedMedia || resolved.EffectiveMode != config.PearTubeModeContributor {
		t.Fatalf("relay availability overwrote explicit policy: %+v", resolved)
	}
}

func TestArchiveConsentHasIndependentBudgetAndMode(t *testing.T) {
	resolved := resolve(config.PearTubeSettings{
		RelayURL:               "http://relay.internal:8178/",
		ConsentVersion:         config.PearTubeConsentVersion,
		ContributeWatchedMedia: false,
		ContributionBudget:     21,
		ArchiveEnabled:         true,
		ArchiveBudget:          144,
	}, func(string) string { return "" })
	if !resolved.Enabled || !resolved.ArchiveEnabled ||
		resolved.ContributeWatchedMedia || resolved.EffectiveMode != config.PearTubeModeArchiveEnabled {
		t.Fatalf("archive policy did not resolve independently: %+v", resolved)
	}
	if resolved.ContributionBudget != 21 || resolved.ArchiveBudget != 144 {
		t.Fatalf("budgets were merged: %+v", resolved)
	}
}

// Configure replaces the process-wide client, which is what makes a settings
// save take effect without restarting the container.
func TestConfigureReplacesTheProcessWideClient(t *testing.T) {
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultClient = nil
		defaultConfigured = false
		defaultMu.Unlock()
	})

	if client := Configure(Resolved{}); client != nil {
		t.Fatalf("empty configuration produced a client: %v", client)
	}
	if Default() != nil {
		t.Fatal("Default returned a client after an empty configuration")
	}

	client := Configure(Resolved{Enabled: true, RelayURL: "http://relay.internal:8178"})
	if client == nil || client.BaseURL() != "http://relay.internal:8178" {
		t.Fatalf("client = %v", client)
	}
	if Default() != client {
		t.Fatal("Default did not return the configured client")
	}

	// An unchanged URL must keep the same client, so a save unrelated to p2p
	// does not discard the catalog cache.
	if again := Configure(Resolved{Enabled: true, RelayURL: "http://relay.internal:8178"}); again != client {
		t.Fatal("an unchanged relay URL rebuilt the client")
	}

	moved := Configure(Resolved{Enabled: true, RelayURL: "http://relay.internal:9000"})
	if moved == client || moved.BaseURL() != "http://relay.internal:9000" {
		t.Fatalf("moved client = %v", moved)
	}

	if Configure(Resolved{}) != nil || Default() != nil {
		t.Fatal("clearing the relay URL left a client installed")
	}
}

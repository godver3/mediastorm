package peartube

import (
	"os"
	"strings"

	"novastream/config"
)

// Resolved is the effective PearTube configuration. Relay availability can
// come from persisted settings or environment fallback. Contribution and
// archive policy can only come from the versioned persisted scraper policy.
type Resolved struct {
	RelayURL string
	Enabled  bool

	ConsentVersion         int
	MigrationRequired      bool
	ContributeWatchedMedia bool
	ContributionBudget     int
	ArchiveEnabled         bool
	ArchiveBudget          int
	// ArchiveOnPlaybackStart moves a consented contribution from "after
	// sustained watch evidence" to "as soon as playback starts", and makes it
	// survive the viewer stopping. It is not consent and is not gated on the
	// migration prompt: it only ever applies where ContributeWatchedMedia is
	// already true.
	ArchiveOnPlaybackStart bool
	EffectiveMode          string

	RelayURLFromEnv bool
	EnabledFromEnv  bool
}

// Resolve combines stored relay availability with environment fallbacks while
// preserving persisted consent as an independent trust boundary.
func Resolve(stored config.PearTubeSettings) Resolved {
	return resolve(stored, os.Getenv)
}

func resolve(stored config.PearTubeSettings, getenv func(string) string) Resolved {
	var resolved Resolved

	resolved.RelayURL = strings.TrimSpace(stored.RelayURL)
	if resolved.RelayURL == "" {
		if fromEnv := strings.TrimSpace(getenv(RelayURLEnv)); fromEnv != "" {
			resolved.RelayURL = fromEnv
			resolved.RelayURLFromEnv = true
		}
	}

	switch {
	case stored.Enabled != nil:
		resolved.Enabled = *stored.Enabled
	default:
		if value, ok := parseSwitch(getenv(EnabledEnv)); ok {
			resolved.Enabled = value
			resolved.EnabledFromEnv = true
		} else {
			// Unset: a configured URL is the switch, which is what keeps an
			// install that never asked for p2p inert.
			resolved.Enabled = resolved.RelayURL != ""
		}
	}
	switch {
	case !resolved.Enabled:
		// One field to check downstream. An explicit disable beats a URL.
		resolved.RelayURL = ""
	case resolved.RelayURL == "":
		// Enabled with no URL anywhere means the relay is where `peartube
		// relay` listens out of the box.
		resolved.RelayURL = DefaultRelayURL
	}

	resolved.ConsentVersion = stored.ConsentVersion
	resolved.MigrationRequired = stored.MigrationRequired ||
		stored.ConsentVersion != config.PearTubeConsentVersion
	resolved.ContributionBudget = stored.ContributionBudget
	if resolved.ContributionBudget == 0 {
		resolved.ContributionBudget = config.PearTubeDefaultContributionBudgetGiB
	}
	resolved.ArchiveBudget = stored.ArchiveBudget
	if resolved.ArchiveBudget == 0 {
		resolved.ArchiveBudget = config.PearTubeDefaultArchiveBudgetGiB
	}
	resolved.ArchiveOnPlaybackStart = stored.ArchiveOnPlaybackStart
	if !resolved.MigrationRequired {
		resolved.ContributeWatchedMedia = stored.ContributeWatchedMedia
		resolved.ArchiveEnabled = stored.ArchiveEnabled
	}
	resolved.EffectiveMode = stored.EffectiveMode()

	return resolved
}

// parseSwitch reads an operator's boolean environment value, reporting whether
// it said anything at all. Unset and unrecognized are the same answer — no
// opinion — so the caller falls back to its own default instead of reading a
// typo as "off".
func parseSwitch(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

package config

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
)

// MigrateRawSettings applies all known migrations to a raw settings JSON map.
// Migrations move or rename fields between sections, ensuring backward
// compatibility when loading older configuration files. Each migration is
// idempotent — it checks whether the old value exists and the new location
// does not before acting.
func MigrateRawSettings(raw map[string]interface{}) {
	migrateFieldToSection(raw, "filtering", "display", "bypassFilteringForAioStreamsOnly")
	migrateFieldToSection(raw, "filtering", "display", "showParsedBadges")
	migrateFieldToSection(raw, "filtering", "playback", "maxResultsPerResolution")
	migratePrioritizeHdrToPreferredTerms(raw)
	migrateRemoveHdrRankingCriterion(raw)
	migratePrewarmFrequencyClear(raw)
	migratePrewarmContinueWatchingOnly(raw)
	migrateGeminiAISettings(raw)
	migratePearTubeConsent(raw)
}

// migratePearTubeConsent is the only place a persisted legacy AutoSeed value
// may become current contribution consent. It works on raw JSON so missing,
// malformed, and explicit false remain distinguishable.
func migratePearTubeConsent(raw map[string]interface{}) {
	scrapers, _ := raw["torrentScrapers"].([]interface{})
	var firstPearTube map[string]interface{}
	for _, value := range scrapers {
		entry, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		entryType, _ := entry["type"].(string)
		if strings.EqualFold(strings.TrimSpace(entryType), TorrentScraperTypePearTube) {
			firstPearTube = entry
			break
		}
	}

	legacy, hasLegacy := raw["peartube"].(map[string]interface{})
	migratedGlobal := false
	if firstPearTube == nil && hasLegacy && legacyPearTubeBlockHasValues(legacy) {
		if len(scrapers) == 0 {
			scrapers = append(scrapers, map[string]interface{}{
				"name":    "Torrentio",
				"type":    "torrentio",
				"enabled": true,
				"options": "sort=qualitysize|qualityfilter=480p,scr,cam",
			})
		}
		firstPearTube = map[string]interface{}{
			"name":    "PearTube",
			"type":    TorrentScraperTypePearTube,
			"url":     rawString(legacy["relayUrl"]),
			"enabled": rawLegacyEnabled(legacy["enabled"]),
		}
		scrapers = append(scrapers, firstPearTube)
		raw["torrentScrapers"] = scrapers
		migratedGlobal = true
	}
	delete(raw, "peartube")
	if firstPearTube == nil {
		return
	}

	configMap, _ := firstPearTube["config"].(map[string]interface{})
	if configMap == nil {
		configMap = make(map[string]interface{})
		firstPearTube["config"] = configMap
	}

	if currentPearTubePolicy(configMap) {
		normalizeRawPearTubePolicy(configMap)
		removeLegacyPearTubeAutoSeed(scrapers)
		return
	}

	legacyValue, persisted := false, false
	if !hasAnyPearTubeCurrentPolicyField(configMap) {
		if rawValue, exists := configMap["autoSeed"]; exists {
			legacyValue, persisted = rawLegacyScraperBool(rawValue)
		} else if migratedGlobal {
			legacyValue, persisted = rawLegacyGlobalBool(legacy["autoSeed"])
		}
	}

	configMap[PearTubeConfigConsentVersion] = strconv.Itoa(PearTubeConsentVersion)
	configMap[PearTubeConfigMigrationRequired] = strconv.FormatBool(!persisted)
	configMap[PearTubeConfigContributeWatchedMedia] = strconv.FormatBool(persisted && legacyValue)
	configMap[PearTubeConfigContributionBudget] = strconv.Itoa(PearTubeDefaultContributionBudgetGiB)
	configMap[PearTubeConfigArchiveEnabled] = "false"
	configMap[PearTubeConfigArchiveBudget] = strconv.Itoa(PearTubeDefaultArchiveBudgetGiB)
	removeLegacyPearTubeAutoSeed(scrapers)
}

func hasAnyPearTubeCurrentPolicyField(values map[string]interface{}) bool {
	for _, key := range []string{
		PearTubeConfigConsentVersion,
		PearTubeConfigMigrationRequired,
		PearTubeConfigContributeWatchedMedia,
		PearTubeConfigContributionBudget,
		PearTubeConfigArchiveEnabled,
		PearTubeConfigArchiveBudget,
	} {
		if _, exists := values[key]; exists {
			return true
		}
	}
	return false
}

func removeLegacyPearTubeAutoSeed(scrapers []interface{}) {
	for _, value := range scrapers {
		entry, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		entryType, _ := entry["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(entryType), TorrentScraperTypePearTube) {
			continue
		}
		if configMap, ok := entry["config"].(map[string]interface{}); ok {
			delete(configMap, "autoSeed")
		}
	}
}

func legacyPearTubeBlockHasValues(legacy map[string]interface{}) bool {
	for _, key := range []string{"relayUrl", "enabled", "autoSeed"} {
		if _, exists := legacy[key]; exists {
			return true
		}
	}
	return false
}

func rawString(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func rawLegacyEnabled(value interface{}) bool {
	enabled, ok := value.(bool)
	return !ok || enabled
}

func rawLegacyGlobalBool(value interface{}) (bool, bool) {
	enabled, ok := value.(bool)
	return enabled, ok
}

func rawLegacyScraperBool(value interface{}) (bool, bool) {
	text, ok := value.(string)
	if !ok {
		return false, false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(text))
	return enabled, err == nil
}

func currentPearTubePolicy(values map[string]interface{}) bool {
	versionText, versionOK := values[PearTubeConfigConsentVersion].(string)
	version, versionErr := strconv.Atoi(strings.TrimSpace(versionText))
	if !versionOK || versionErr != nil || version != PearTubeConsentVersion {
		return false
	}
	for _, key := range []string{
		PearTubeConfigMigrationRequired,
		PearTubeConfigContributeWatchedMedia,
		PearTubeConfigArchiveEnabled,
	} {
		text, ok := values[key].(string)
		if !ok {
			return false
		}
		if _, err := strconv.ParseBool(strings.TrimSpace(text)); err != nil {
			return false
		}
	}
	for _, key := range []string{PearTubeConfigContributionBudget, PearTubeConfigArchiveBudget} {
		text, ok := values[key].(string)
		if !ok {
			return false
		}
		if _, err := strconv.Atoi(strings.TrimSpace(text)); err != nil {
			return false
		}
	}
	return true
}

func normalizeRawPearTubePolicy(values map[string]interface{}) {
	migrationRequired, _ := strconv.ParseBool(strings.TrimSpace(values[PearTubeConfigMigrationRequired].(string)))
	contribute, _ := strconv.ParseBool(strings.TrimSpace(values[PearTubeConfigContributeWatchedMedia].(string)))
	archive, _ := strconv.ParseBool(strings.TrimSpace(values[PearTubeConfigArchiveEnabled].(string)))
	if migrationRequired {
		contribute = false
		archive = false
	}
	values[PearTubeConfigConsentVersion] = strconv.Itoa(PearTubeConsentVersion)
	values[PearTubeConfigMigrationRequired] = strconv.FormatBool(migrationRequired)
	values[PearTubeConfigContributeWatchedMedia] = strconv.FormatBool(contribute)
	values[PearTubeConfigContributionBudget] = strconv.Itoa(pearTubeBudget(
		values[PearTubeConfigContributionBudget].(string),
		PearTubeDefaultContributionBudgetGiB,
		PearTubeContributionBudgetMinGiB,
		PearTubeContributionBudgetMaxGiB,
	))
	values[PearTubeConfigArchiveEnabled] = strconv.FormatBool(archive)
	values[PearTubeConfigArchiveBudget] = strconv.Itoa(pearTubeBudget(
		values[PearTubeConfigArchiveBudget].(string),
		PearTubeDefaultArchiveBudgetGiB,
		PearTubeArchiveBudgetMinGiB,
		PearTubeArchiveBudgetMaxGiB,
	))
}

// migratePrewarmContinueWatchingOnly temporarily removes speculative home
// shelves from every prewarm task. This is intentionally idempotent and can be
// relaxed when provider-aware request budgets are available.
func migratePrewarmContinueWatchingOnly(raw map[string]interface{}) {
	tasksMap, ok := raw["scheduledTasks"].(map[string]interface{})
	if !ok {
		return
	}
	tasksList, ok := tasksMap["tasks"].([]interface{})
	if !ok {
		return
	}

	selectionBytes, _ := json.Marshal([]map[string]interface{}{
		{"id": "continue-watching", "playedWithinDays": 14},
	})
	wanted := string(selectionBytes)

	for _, item := range tasksList {
		task, ok := item.(map[string]interface{})
		if !ok || task["type"] != "prewarm" {
			continue
		}
		configMap, ok := task["config"].(map[string]interface{})
		if !ok {
			configMap = make(map[string]interface{})
			task["config"] = configMap
		}
		if current, _ := configMap["shelfSelections"].(string); current == wanted {
			continue
		}
		configMap["shelfSelections"] = wanted
	}
}

// MigrateGlobalLiveProxyToDefaultSource moves the currently unsupported
// global Live TV proxy fallback onto the default provider and leaves the
// global field unset. Keep LiveSettings.ProxyURL in the model so a global
// fallback can be restored in the future without another schema change.
func MigrateGlobalLiveProxyToDefaultSource(settings *Settings) bool {
	if settings == nil {
		return false
	}

	proxyURL := strings.TrimSpace(settings.Live.ProxyURL)
	if proxyURL == "" {
		return false
	}

	defaultIndex := -1
	for i := range settings.Live.Sources {
		source := &settings.Live.Sources[i]
		if strings.EqualFold(strings.TrimSpace(source.ID), "default") ||
			strings.EqualFold(strings.TrimSpace(source.Name), "default") {
			defaultIndex = i
			break
		}
	}
	if defaultIndex < 0 && len(settings.Live.Sources) > 0 {
		// Legacy single-source configurations may not have received an ID or
		// name yet, but the first source is their effective default provider.
		defaultIndex = 0
	}

	if defaultIndex >= 0 && strings.TrimSpace(settings.Live.Sources[defaultIndex].ProxyURL) == "" {
		settings.Live.Sources[defaultIndex].ProxyURL = proxyURL
	}
	settings.Live.ProxyURL = ""

	log.Printf("[config] migrated global Live TV proxy to default provider and cleared global fallback")
	return true
}

// MigrateRawUserSettings applies migrations to a single user's raw settings map.
// The structure mirrors the global settings but uses pointer types with omitempty,
// so the same field-move logic applies.
func MigrateRawUserSettings(raw map[string]interface{}) {
	migrateFieldToSection(raw, "filtering", "display", "bypassFilteringForAioStreamsOnly")
	migrateFieldToSection(raw, "filtering", "playback", "maxResultsPerResolution")
	migratePrioritizeHdrToPreferredTerms(raw)
	migrateRemoveHdrRankingCriterion(raw)
}

// migrateFieldToSection moves a field from one top-level section to another.
// It only acts when the field exists in the source and is absent from the destination.
func migrateFieldToSection(raw map[string]interface{}, fromSection, toSection, field string) {
	srcMap, ok := raw[fromSection].(map[string]interface{})
	if !ok {
		return
	}
	val, exists := srcMap[field]
	if !exists {
		return
	}

	// Ensure destination section exists
	dstMap, ok := raw[toSection].(map[string]interface{})
	if !ok {
		dstMap = map[string]interface{}{}
		raw[toSection] = dstMap
	}

	// Only migrate if destination doesn't already have the field
	if _, alreadySet := dstMap[field]; alreadySet {
		return
	}

	dstMap[field] = val
	delete(srcMap, field)
	log.Printf("[config] migrated %s.%s → %s.%s", fromSection, field, toSection, field)
}

func migrateGeminiAISettings(raw map[string]interface{}) {
	metadata, ok := raw["metadata"].(map[string]interface{})
	if !ok {
		return
	}

	geminiKey, _ := metadata["geminiApiKey"].(string)
	aiKey, _ := metadata["aiApiKey"].(string)
	aiProvider, _ := metadata["aiProvider"].(string)

	if aiKey == "" && geminiKey != "" && aiProvider == "" {
		metadata["aiApiKey"] = geminiKey
		metadata["aiProvider"] = "gemini"
		log.Printf("[config] migrated metadata.geminiApiKey → metadata.aiApiKey")
	}
	if aiProvider == "" {
		if key, _ := metadata["aiApiKey"].(string); key != "" {
			metadata["aiProvider"] = "gemini"
		}
	}
}

// migratePrioritizeHdrToPreferredTerms removes the deprecated prioritizeHdr
// field. When it was true, HDR-related preferred terms are added to boost HDR/DV
// results via the existing preferred terms ranking criterion. When false, the
// field is simply removed (no boost). Also removes the deprecated "hdr" ranking
// criterion which is now redundant.
func migratePrioritizeHdrToPreferredTerms(raw map[string]interface{}) {
	filterMap, ok := raw["filtering"].(map[string]interface{})
	if !ok {
		return
	}
	val, exists := filterMap["prioritizeHdr"]
	if !exists {
		return
	}

	// Remove the deprecated field
	delete(filterMap, "prioritizeHdr")

	// If prioritizeHdr was true, add HDR preferred terms
	prioritize, isBool := val.(bool)
	if isBool && prioritize {
		hdrTerms := []string{`/\bHDR\b/`, "HDR10", "HDR10+", `/\bDV\b/`, "Dolby Vision"}

		// Get existing preferred terms
		existing, _ := filterMap["preferredTerms"].([]interface{})
		existingSet := make(map[string]bool, len(existing))
		for _, t := range existing {
			if s, ok := t.(string); ok {
				existingSet[s] = true
			}
		}

		// Add HDR terms that aren't already present
		added := 0
		for _, term := range hdrTerms {
			if !existingSet[term] {
				existing = append(existing, term)
				added++
			}
		}
		if added > 0 {
			filterMap["preferredTerms"] = existing
			log.Printf("[config] migrated filtering.prioritizeHdr=true → added %d HDR preferred terms", added)
		}
	} else {
		log.Printf("[config] removed deprecated filtering.prioritizeHdr (was false)")
	}
}

// migratePrewarmFrequencyClear clears the frequency field on prewarm tasks.
// Prewarm now uses dynamic TTL and the scheduler hardcodes a 15-minute internal
// tick, so user-configured frequency is no longer applicable.
func migratePrewarmFrequencyClear(raw map[string]interface{}) {
	tasksMap, ok := raw["scheduledTasks"].(map[string]interface{})
	if !ok {
		return
	}
	tasksList, ok := tasksMap["tasks"].([]interface{})
	if !ok {
		return
	}

	for _, t := range tasksList {
		task, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if task["type"] != "prewarm" {
			continue
		}
		freq, _ := task["frequency"].(string)
		if freq == "" {
			continue // Already cleared
		}
		task["frequency"] = ""
		log.Printf("[config] cleared prewarm task frequency %q (dynamic TTL now manages re-resolve cadence)", freq)
	}
}

// migrateRemoveHdrRankingCriterion removes the deprecated "hdr" ranking criterion.
// HDR boosting is now handled entirely via preferred terms.
func migrateRemoveHdrRankingCriterion(raw map[string]interface{}) {
	rankingMap, ok := raw["ranking"].(map[string]interface{})
	if !ok {
		return
	}
	criteriaRaw, ok := rankingMap["criteria"].([]interface{})
	if !ok {
		return
	}

	filtered := make([]interface{}, 0, len(criteriaRaw))
	removed := false
	for _, c := range criteriaRaw {
		criterion, ok := c.(map[string]interface{})
		if ok && criterion["id"] == "hdr" {
			removed = true
			continue
		}
		filtered = append(filtered, c)
	}

	if removed {
		// Re-number orders to be contiguous
		for i, c := range filtered {
			if criterion, ok := c.(map[string]interface{}); ok {
				criterion["order"] = float64(i)
			}
		}
		rankingMap["criteria"] = filtered
		log.Printf("[config] removed deprecated 'hdr' ranking criterion")
	}
}

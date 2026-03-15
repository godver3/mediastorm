package config

import "log"

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
		hdrTerms := []string{"HDR", "HDR10", "HDR10+", "DV", "Dolby Vision"}

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

package mediaidentity

import "strings"

// StorageExternalIDs normalizes external IDs for persistence while preserving
// legacy contradiction signals such as titleId.
func StorageExternalIDs(mediaType, itemID, seriesID string, externalIDs map[string]string) map[string]string {
	normalized := NormalizeExternalIDs(externalIDs)
	if NormalizeMediaType(mediaType) != "episode" || len(normalized) == 0 {
		return normalized
	}
	if strings.TrimSpace(normalized["titleId"]) != "" {
		return normalized
	}

	inferredSeriesID := InferSeriesIDFromEpisodeItemID(itemID)
	inferredProvider, inferredNumericID := SeriesProviderAndID(inferredSeriesID)
	if inferredProvider == "" || inferredNumericID == "" {
		return normalized
	}

	seriesProvider, seriesNumericID := SeriesProviderAndID(seriesID)
	bridgesDifferentSeriesID := seriesProvider != "" && seriesNumericID != "" &&
		(seriesProvider != inferredProvider || seriesNumericID != inferredNumericID)
	if !bridgesDifferentSeriesID && !HasContradictorySeriesExternalIDs(seriesID, itemID, normalized) {
		return normalized
	}

	copied := make(map[string]string, len(normalized)+1)
	for key, value := range normalized {
		copied[key] = value
	}
	copied["titleId"] = inferredSeriesID
	return copied
}

// IdentityExternalIDs returns the external IDs that should participate in
// canonical identity resolution. It removes polluted series IDs while keeping
// episode-scoped aliases and legacy titleId bridge data.
func IdentityExternalIDs(mediaType, itemID, seriesID string, externalIDs map[string]string) map[string]string {
	normalized := NormalizeExternalIDs(externalIDs)
	if NormalizeMediaType(mediaType) != "episode" || len(normalized) == 0 {
		return normalized
	}

	canonicalSeriesIDs := CanonicalSeriesExternalIDs(seriesID, itemID, normalized)
	if len(canonicalSeriesIDs) == 0 {
		return normalized
	}

	sanitized := make(map[string]string, len(normalized)+len(canonicalSeriesIDs))
	for key, value := range normalized {
		switch key {
		case "tmdb", "tvdb", "imdb":
			continue
		default:
			sanitized[key] = value
		}
	}
	for key, value := range canonicalSeriesIDs {
		if strings.TrimSpace(value) != "" {
			sanitized[key] = value
		}
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func CanonicalSeriesExternalIDs(seriesID, itemID string, extIDs map[string]string) map[string]string {
	extIDs = NormalizeExternalIDs(extIDs)
	if len(extIDs) == 0 {
		return nil
	}

	if copied := copySeriesExternalIDs(extIDs); len(copied) > 0 {
		return copied
	}

	provider, numericID := SeriesProviderAndID(seriesID)
	itemProvider, itemNumericID := SeriesProviderAndID(InferSeriesIDFromEpisodeItemID(itemID))
	titleProvider, titleNumericID := SeriesProviderAndID(extIDs["titleId"])

	if provider != "" && numericID != "" {
		copied := make(map[string]string, 1)
		copied[provider] = numericID
		addEpisodeItemSeriesExternalID(copied, provider, itemProvider, itemNumericID)
		addTitleSeriesExternalID(copied, provider, titleProvider, titleNumericID)
		return copied
	}

	if titleProvider != "" && titleNumericID != "" {
		return map[string]string{titleProvider: titleNumericID}
	}

	return copySeriesExternalIDs(extIDs)
}

func HasContradictorySeriesExternalIDs(seriesID, itemID string, extIDs map[string]string) bool {
	extIDs = NormalizeExternalIDs(extIDs)
	if len(extIDs) == 0 {
		return false
	}
	for _, reliableID := range []string{seriesID, InferSeriesIDFromEpisodeItemID(itemID)} {
		provider, numericID := SeriesProviderAndID(reliableID)
		if provider == "" || numericID == "" {
			continue
		}
		externalID := strings.TrimSpace(extIDs[provider])
		if externalID != "" && !strings.EqualFold(externalID, numericID) {
			return true
		}
	}
	return false
}

func InferSeriesIDFromEpisodeItemID(itemID string) string {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return ""
	}
	parts := strings.Split(itemID, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.ToLower(strings.TrimSpace(parts[i]))
		if len(part) > 1 && strings.HasPrefix(part, "s") {
			return strings.Join(parts[:i], ":")
		}
	}
	return ""
}

func SeriesProviderAndID(seriesID string) (string, string) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return "", ""
	}
	parts := strings.Split(seriesID, ":")
	if len(parts) < 2 {
		if strings.HasPrefix(strings.ToLower(seriesID), "tt") {
			return "imdb", seriesID
		}
		return "", ""
	}
	provider := strings.ToLower(strings.TrimSpace(parts[0]))
	numericID := strings.TrimSpace(parts[len(parts)-1])
	switch provider {
	case "tmdb", "tvdb", "imdb":
		return provider, numericID
	default:
		return "", ""
	}
}

func addEpisodeItemSeriesExternalID(extIDs map[string]string, seriesProvider, itemProvider, itemNumericID string) {
	if extIDs == nil || itemProvider == "" || itemNumericID == "" || itemProvider == seriesProvider {
		return
	}
	if existing := strings.TrimSpace(extIDs[itemProvider]); existing == "" {
		extIDs[itemProvider] = itemNumericID
	}
}

func addTitleSeriesExternalID(extIDs map[string]string, seriesProvider, titleProvider, titleNumericID string) {
	if extIDs == nil || titleProvider == "" || titleNumericID == "" || titleProvider == seriesProvider {
		return
	}
	if existing := strings.TrimSpace(extIDs[titleProvider]); existing == "" {
		extIDs[titleProvider] = titleNumericID
	}
}

func copySeriesExternalIDs(extIDs map[string]string) map[string]string {
	if len(extIDs) == 0 {
		return nil
	}
	copied := make(map[string]string, len(extIDs))
	for k, v := range extIDs {
		if v == "" || k == "titleId" || strings.HasPrefix(k, "episode") {
			continue
		}
		copied[k] = v
	}
	if len(copied) == 0 {
		return nil
	}
	return copied
}

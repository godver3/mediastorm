package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"novastream/config"
)

// UserEditableSettingSchema is the safe subset of the admin schema needed to
// render native profile/device editors. It deliberately excludes conditional
// admin UI behavior and any server-only metadata.
type UserEditableSettingSchema struct {
	Type        string      `json:"type"`
	Label       string      `json:"label"`
	Description string      `json:"description,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	Options     interface{} `json:"options,omitempty"`
	Min         interface{} `json:"min,omitempty"`
	Max         interface{} `json:"max,omitempty"`
	Step        interface{} `json:"step,omitempty"`
	Scopes      []string    `json:"scopes"`
}

// userEditableFields are scalar settings fields that have matching profile-level
// overrides and a frontend mirror. Array editors and server-only integration
// settings remain excluded until their client editing semantics are defined.
var userEditableFields = map[string]map[string]struct{}{
	"metadata": fieldSet("primaryLanguage"),
	"filtering": fieldSet(
		"maxSizeMovieGb", "maxSizeEpisodeGb", "maxResolution", "hdrDvPolicy", "requiredTerms",
		"filterOutTerms", "preferredTerms", "nonPreferredTerms", "downloadPreferredTerms",
		"unknownTrackPolicy", "preferredScraper", "servicePriority", "adaptivePlaybackEnabled",
		"adaptiveTargetBufferFactor", "realDebridRestrictedTermsFilterEnabled", "splitByService",
	),
	"filtering.debrid": nil,
	"filtering.usenet": nil,
	"animeFiltering":   nil,
	"ranking":          fieldSet("newestReleaseFirst", "splitByService"),
	"playback": fieldSet(
		"preferLocalFiles", "preferredPlayer", "preferredAudioLanguage", "preferredSubtitleLanguage", "allowedTrackLanguages",
		"preferredSubtitleMode", "pauseWhenAppInactive", "useLoadingScreen", "subtitleSize", "subtitleUseCropDetectPosition",
		"subtitleColor", "subtitleOpacity", "subtitleFont", "subtitleBold", "subtitleOutlineEnabled",
		"subtitleOutlineColor", "subtitleOutlineWeight", "subtitleBackgroundEnabled", "subtitleBackgroundColor",
		"subtitleBackgroundOpacity", "seekForwardSeconds", "seekBackwardSeconds", "forceAacTranscoding",
		"autoPlayTrailersTV", "rewindOnResumeFromPause", "rewindOnPlaybackStart", "disablePrequeue",
		"prerollMode", "prerollAssetId", "prerollMediaScope", "prerollSkipIfPrequeueReady",
		"ignoreDolbyVisionCompatibilityCheck", "streamMigrationEnabled", "creditsDetectionEnabled",
		"creditsAutoSkip", "scrobbleStartDelaySeconds", "matchFrameRate", "liveClosedCaptionExtraction", "maxConcurrentStreams",
		"maxResultsPerResolution",
	),
	"homeShelves": nil, // All scalar fields in this section are profile-compatible.
	"display": fieldSet(
		"badgeVisibility", "navigationTabVisibility", "watchStateIconStyle",
		"includeUnreleasedMoviesInLists", "includeUnreleasedShowsInLists",
		"includeUnreleasedMoviesInSearch", "includeUnreleasedShowsInSearch", "enableAnimations",
		"enableHeroArtPanning", "enableHeroArtRotation", "hideContinueWatchingHeroMetadata",
		"moveDetailsRatingsToMetadata", "hideDetailsPoster", "hideTvDrawerRail", "simpleMode", "simpleModeHomeShelves", "disableTvHomeCardDimming", "bypassFilteringForAioStreamsOnly", "showStreamSourceInfo",
		"disableMobileTopCarousel", "showSeriesBackdropForMissingEpisodeArt", "blurUnwatchedEpisodeThumbnails",
		"blurUnwatchedEpisodeThumbnailsIncludeCurrent", "blurUnwatchedEpisodeOverviews",
		"blurUnwatchedEpisodeOverviewsIncludeCurrent", "appLanguage",
	),
	"network": nil,
}

var deviceEditableFields = map[string]map[string]struct{}{
	"homeShelves": fieldSet("homeShelfFocusModel"),
	"filtering": fieldSet(
		"maxSizeMovieGb", "maxSizeEpisodeGb", "maxResolution", "hdrDvPolicy", "requiredTerms",
		"filterOutTerms", "preferredTerms", "nonPreferredTerms", "downloadPreferredTerms",
		"unknownTrackPolicy", "realDebridRestrictedTermsFilterEnabled", "splitByService",
	),
	"filtering.debrid": userEditableFields["filtering.debrid"],
	"filtering.usenet": userEditableFields["filtering.usenet"],
	"animeFiltering":   userEditableFields["animeFiltering"],
	"ranking":          fieldSet("newestReleaseFirst"),
	"playback":         fieldSet("preferredPlayer", "preferredAudioLanguage", "preferredSubtitleLanguage", "allowedTrackLanguages", "preferredSubtitleMode", "pauseWhenAppInactive", "useLoadingScreen", "subtitleSize", "subtitleUseCropDetectPosition", "subtitleColor", "subtitleOpacity", "subtitleFont", "subtitleBold", "subtitleOutlineEnabled", "subtitleOutlineColor", "subtitleOutlineWeight", "subtitleBackgroundEnabled", "subtitleBackgroundColor", "subtitleBackgroundOpacity", "seekForwardSeconds", "seekBackwardSeconds", "forceAacTranscoding", "autoPlayTrailersTV", "rewindOnResumeFromPause", "rewindOnPlaybackStart", "disablePrequeue", "prerollMode", "prerollAssetId", "prerollMediaScope", "prerollSkipIfPrequeueReady", "ignoreDolbyVisionCompatibilityCheck", "streamMigrationEnabled", "creditsDetectionEnabled", "creditsAutoSkip", "scrobbleStartDelaySeconds", "matchFrameRate", "liveClosedCaptionExtraction", "maxResultsPerResolution"),
	"display":          fieldSet("navigationTabVisibility", "includeUnreleasedMoviesInLists", "includeUnreleasedShowsInLists", "includeUnreleasedMoviesInSearch", "includeUnreleasedShowsInSearch", "bypassFilteringForAioStreamsOnly", "showStreamSourceInfo", "disableMobileTopCarousel", "hideContinueWatchingHeroMetadata", "moveDetailsRatingsToMetadata", "hideDetailsPoster", "hideTvDrawerRail", "simpleMode", "simpleModeHomeShelves", "disableTvHomeCardDimming", "enableAnimations", "enableHeroArtPanning", "enableHeroArtRotation", "showSeriesBackdropForMissingEpisodeArt", "blurUnwatchedEpisodeThumbnails", "blurUnwatchedEpisodeThumbnailsIncludeCurrent", "blurUnwatchedEpisodeOverviews", "blurUnwatchedEpisodeOverviewsIncludeCurrent"),
	"network":          nil,
}

func fieldSet(paths ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		result[path] = struct{}{}
	}
	return result
}

func init() {
	annotateUserEditableSettingsSchema()
}

func annotateUserEditableSettingsSchema() {
	for sectionKey, rawSection := range SettingsSchema {
		allowedFields, ok := userEditableFields[sectionKey]
		if !ok {
			continue
		}
		section, ok := rawSection.(map[string]interface{})
		if !ok {
			continue
		}
		fields, ok := section["fields"].(map[string]interface{})
		if !ok {
			continue
		}
		for fieldKey, rawField := range fields {
			if !settingFieldAllowed(sectionKey, fieldKey, allowedFields) {
				continue
			}
			field, ok := rawField.(map[string]interface{})
			if !ok || field["hidden"] == true || field["globalOnly"] == true {
				continue
			}
			fieldType, _ := field["type"].(string)
			if fieldType == "password" || fieldType == "file_upload" {
				continue
			}
			field["userEditableEligible"] = true
			field["userEditablePath"] = sectionKey + "." + fieldKey
		}
	}
}

func settingFieldAllowed(sectionKey, fieldKey string, allowedFields map[string]struct{}) bool {
	if allowedFields == nil {
		return true
	}
	if _, allowed := allowedFields[fieldKey]; allowed {
		return true
	}
	return sectionKey == "display" && strings.HasPrefix(fieldKey, "appearance.")
}

func eligibleUserEditableSettings() map[string]struct{} {
	eligible := make(map[string]struct{})
	for _, rawSection := range SettingsSchema {
		section, ok := rawSection.(map[string]interface{})
		if !ok {
			continue
		}
		fields, ok := section["fields"].(map[string]interface{})
		if !ok {
			continue
		}
		for _, rawField := range fields {
			field, ok := rawField.(map[string]interface{})
			if !ok || field["userEditableEligible"] != true {
				continue
			}
			path, _ := field["userEditablePath"].(string)
			if path != "" {
				eligible[path] = struct{}{}
			}
		}
	}
	return eligible
}

func filterUserEditableSettings(paths []string) []string {
	eligible := eligibleUserEditableSettings()
	seen := make(map[string]struct{}, len(paths))
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := eligible[path]; !ok {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		filtered = append(filtered, path)
	}
	sort.Strings(filtered)
	return filtered
}

func userEditableSettingsSchema(paths []string) map[string]UserEditableSettingSchema {
	result := make(map[string]UserEditableSettingSchema)
	for _, path := range filterUserEditableSettings(paths) {
		sectionKey, fieldKey, field := editableSchemaField(path)
		if field == nil {
			continue
		}
		scopes := []string{"profile"}
		if allowedFields, ok := deviceEditableFields[sectionKey]; ok && settingFieldAllowed(sectionKey, fieldKey, allowedFields) {
			scopes = append(scopes, "device")
		}
		result[path] = UserEditableSettingSchema{
			Type:        stringValue(field["type"]),
			Label:       stringValue(field["label"]),
			Description: stringValue(field["description"]),
			Placeholder: stringValue(field["placeholder"]),
			Options:     field["options"],
			Min:         field["min"],
			Max:         field["max"],
			Step:        field["step"],
			Scopes:      scopes,
		}
	}
	return result
}

func userEditableSettingsSchemaForSettings(paths []string, settings config.Settings) map[string]UserEditableSettingSchema {
	result := userEditableSettingsSchema(paths)
	if preferredScraper, ok := result["filtering.preferredScraper"]; ok {
		seen := make(map[string]struct{})
		options := make([]map[string]string, 0, len(settings.TorrentScrapers))
		for _, scraper := range settings.TorrentScrapers {
			name := strings.TrimSpace(scraper.Name)
			if name == "" {
				name = strings.TrimSpace(scraper.Type)
			}
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			options = append(options, map[string]string{"value": name, "label": name})
		}
		preferredScraper.Options = options
		result["filtering.preferredScraper"] = preferredScraper
	}
	if simpleModeShelves, ok := result["display.simpleModeHomeShelves"]; ok {
		simpleModeShelves.Options = simpleModeHomeShelfOptions(settings.HomeShelves.Shelves)
		result["display.simpleModeHomeShelves"] = simpleModeShelves
	}
	return result
}

func simpleModeHomeShelfOptions(shelves []config.ShelfConfig) []map[string]string {
	if len(shelves) == 0 {
		shelves = config.DefaultHomeShelfConfigs()
	}
	options := make([]map[string]string, 0, len(shelves))
	seen := make(map[string]struct{}, len(shelves))
	for _, shelf := range shelves {
		id := strings.TrimSpace(shelf.ID)
		if id == "" || id == "tonight" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		label := strings.TrimSpace(shelf.Name)
		if label == "" {
			label = id
		}
		options = append(options, map[string]string{"value": id, "label": label})
	}
	return options
}

func editableSchemaField(path string) (string, string, map[string]interface{}) {
	var matchedSection string
	var matchedField string
	var matched map[string]interface{}
	for sectionKey, rawSection := range SettingsSchema {
		prefix := sectionKey + "."
		if !strings.HasPrefix(path, prefix) || len(sectionKey) <= len(matchedSection) {
			continue
		}
		fieldKey := strings.TrimPrefix(path, prefix)
		section, _ := rawSection.(map[string]interface{})
		fields, _ := section["fields"].(map[string]interface{})
		field, _ := fields[fieldKey].(map[string]interface{})
		if field != nil {
			matchedSection, matchedField, matched = sectionKey, fieldKey, field
		}
	}
	return matchedSection, matchedField, matched
}

func stringValue(value interface{}) string {
	result, _ := value.(string)
	return result
}

func validateUserEditableSettingValue(path string, raw json.RawMessage) error {
	schema, ok := userEditableSettingsSchema([]string{path})[path]
	if !ok {
		return fmt.Errorf("setting %q is not eligible for frontend editing", path)
	}
	switch schema.Type {
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be true or false", schema.Label)
		}
	case "number":
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be a number", schema.Label)
		}
		if min, ok := numericSchemaValue(schema.Min); ok && value < min {
			return fmt.Errorf("%s must be at least %v", schema.Label, min)
		}
		if max, ok := numericSchemaValue(schema.Max); ok && value > max {
			return fmt.Errorf("%s must be at most %v", schema.Label, max)
		}
	case "tags", "weighted-tags", "checkboxes", "multiselect", "ordered-multiselect":
		var value []string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be a list of text values", schema.Label)
		}
		if schema.Type != "tags" && schema.Type != "weighted-tags" && schema.Options != nil {
			for _, selected := range value {
				if !editableOptionAllowed(schema.Options, selected) {
					return fmt.Errorf("%s contains an unsupported value", schema.Label)
				}
			}
		}
	case "select":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be text", schema.Label)
		}
		if !editableOptionAllowed(schema.Options, value) {
			return fmt.Errorf("%s has an unsupported value", schema.Label)
		}
	default:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%s must be text", schema.Label)
		}
	}
	return nil
}

func numericSchemaValue(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func editableOptionAllowed(options interface{}, value string) bool {
	if options == nil {
		return true
	}
	switch typed := options.(type) {
	case []string:
		for _, option := range typed {
			if option == value {
				return true
			}
		}
	case []map[string]string:
		for _, option := range typed {
			if option["value"] == value {
				return true
			}
		}
	case []interface{}:
		for _, rawOption := range typed {
			switch option := rawOption.(type) {
			case string:
				if option == value {
					return true
				}
			case map[string]interface{}:
				if stringValue(option["value"]) == value {
					return true
				}
			}
		}
	}
	return false
}

package handlers

import (
	"strings"

	"novastream/config"
	metadatapkg "novastream/services/metadata"
)

func metadataServiceForUser(service metadataService, cfg *config.Manager, userSettings userSettingsProvider, userID string) metadataService {
	if service == nil || cfg == nil {
		return service
	}
	localized, ok := service.(interface {
		WithLanguage(string) *metadatapkg.Service
	})
	if !ok {
		return service
	}
	settings, err := cfg.Load()
	if err != nil {
		return service
	}
	language, _ := resolveMetadataLanguage(settings, userSettings, userID)
	return localized.WithLanguage(language)
}

// resolveMetadataLanguage returns the effective metadata language for the given
// profile and whether it matches the global default (i.e. the language baked
// into stored metadata, so no per-request re-localization is needed).
func resolveMetadataLanguage(settings config.Settings, userSettings userSettingsProvider, userID string) (language string, isGlobalDefault bool) {
	global := settings.Metadata.EffectivePrimaryLanguage()
	language = global
	if userSettings != nil && strings.TrimSpace(userID) != "" {
		if profileSettings, err := userSettings.Get(userID); err == nil && profileSettings != nil {
			if profileLanguage := strings.TrimSpace(profileSettings.Metadata.PrimaryLanguage); profileLanguage != "" {
				for _, enabled := range settings.Metadata.Language {
					if strings.EqualFold(strings.TrimSpace(enabled), profileLanguage) {
						language = strings.TrimSpace(enabled)
						break
					}
				}
			}
		}
	}
	return language, strings.EqualFold(language, global)
}

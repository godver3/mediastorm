package usenetengine

import (
	"fmt"
	"net/http"
	"strings"

	"novastream/config"
)

func NewFromSettings(settings config.UsenetEngineSettings, httpClient HTTPDoer) (Engine, error) {
	engineType := normalizeEngineType(settings.Type)
	switch engineType {
	case "altmount", "nzbdav", "nzbdavex", "decypharr", "sabnzbd", "":
		apiPath := strings.TrimSpace(settings.APIPath)
		if apiPath == "" {
			apiPath = defaultAPIPath(engineType)
		}
		return NewSABClient(SABConfig{
			Name:            firstConfigured(settings.Name, settings.Type, "usenet-engine"),
			BaseURL:         settings.BaseURL,
			APIPath:         apiPath,
			FileFieldName:   defaultFileFieldName(engineType),
			CategoryInQuery: categoryInQuery(engineType),
			APIKey:          settings.APIKey,
			Username:        settings.Username,
			Password:        settings.Password,
		}, httpClient)
	default:
		return nil, fmt.Errorf("unsupported usenet engine type %q", settings.Type)
	}
}

func EnabledEngines(settings config.Settings, profileID string) []config.UsenetEngineSettings {
	profileID = strings.TrimSpace(profileID)
	out := make([]config.UsenetEngineSettings, 0, len(settings.UsenetEngines))
	for _, engine := range settings.UsenetEngines {
		if !engine.Enabled || strings.TrimSpace(engine.BaseURL) == "" {
			continue
		}
		if len(engine.AllowedProfiles) > 0 {
			if profileID == "" || !containsString(engine.AllowedProfiles, profileID) {
				continue
			}
		}
		out = append(out, engine)
	}
	return out
}

func DefaultHTTPClient() *http.Client {
	return &http.Client{}
}

func normalizeEngineType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func defaultAPIPath(engineType string) string {
	switch engineType {
	case "altmount", "decypharr":
		return "/sabnzbd/api"
	default:
		return "/api"
	}
}

func defaultFileFieldName(engineType string) string {
	if engineType == "decypharr" {
		return "name"
	}
	return "nzbfile"
}

func categoryInQuery(engineType string) bool {
	switch engineType {
	case "decypharr", "nzbdav":
		return true
	default:
		return false
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}

func firstConfigured(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

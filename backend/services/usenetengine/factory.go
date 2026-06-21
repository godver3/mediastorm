package usenetengine

import (
	"fmt"
	"net/http"
	"net/url"
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
		baseURL, normalizedAPIPath := splitConfiguredAPIEndpoint(settings.BaseURL, apiPath, engineType)
		return NewSABClient(SABConfig{
			Name:            firstConfigured(settings.Name, settings.Type, "usenet-engine"),
			BaseURL:         baseURL,
			APIPath:         normalizedAPIPath,
			FileFieldName:   defaultFileFieldName(engineType),
			CategoryInQuery: categoryInQuery(engineType),
			APIKey:          settings.APIKey,
			APIKeyAsBearer:  apiKeyAsBearer(engineType),
			Username:        settings.Username,
			Password:        settings.Password,
			UsernameParam:   usernameParam(engineType),
			PasswordParam:   passwordParam(engineType),
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

func splitConfiguredAPIEndpoint(baseURL, apiPath, engineType string) (string, string) {
	baseURL = strings.TrimSpace(baseURL)
	apiPath = strings.TrimSpace(apiPath)
	if apiPath == "" {
		apiPath = defaultAPIPath(engineType)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL, apiPath
	}
	pathValue := strings.TrimRight(parsed.Path, "/")
	candidates := []string{apiPath, "/sabnzbd/api", "/api"}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if pathValue == candidate || strings.HasSuffix(pathValue, candidate) {
			rootPath := strings.TrimRight(strings.TrimSuffix(pathValue, candidate), "/")
			parsed.Path = rootPath
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return strings.TrimRight(parsed.String(), "/"), candidate
		}
	}
	return baseURL, apiPath
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

func apiKeyAsBearer(engineType string) bool {
	return engineType == "decypharr"
}

func usernameParam(engineType string) string {
	if engineType == "decypharr" {
		return "ma_username"
	}
	return ""
}

func passwordParam(engineType string) string {
	if engineType == "decypharr" {
		return "ma_password"
	}
	return ""
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

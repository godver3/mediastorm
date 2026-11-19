package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"novastream/config"
)

// externalUsenetEngineFor returns the enabled external Usenet engine (AltMount,
// NZBDav, NZBDavEx, Decypharr, …) whose WebDAV base URL contains candidate.
// Every engine type serves its files over WebDAV with the same
// webdavUsername/webdavPassword settings, so one lookup serves them all.
func externalUsenetEngineFor(cfg ConfigProvider, candidate *url.URL) (config.UsenetEngineSettings, bool) {
	if cfg == nil || candidate == nil {
		return config.UsenetEngineSettings{}, false
	}
	settings, err := cfg.Load()
	if err != nil {
		return config.UsenetEngineSettings{}, false
	}
	for _, engine := range settings.UsenetEngines {
		if !engine.Enabled || strings.TrimSpace(engine.WebDAVBaseURL) == "" {
			continue
		}
		if externalURLMatchesBase(candidate, engine.WebDAVBaseURL) {
			return engine, true
		}
	}
	return config.UsenetEngineSettings{}, false
}

// applyExternalUsenetWebDAVAuthFromConfig sets the engine's WebDAV Basic auth
// on req when its URL is served by an enabled external Usenet engine. Engines
// reject unauthenticated WebDAV reads with 401, so any request to one — the
// playback proxy, HLS, probes, and the stale-stream validator — needs this.
func applyExternalUsenetWebDAVAuthFromConfig(cfg ConfigProvider, req *http.Request) {
	if req == nil || req.URL == nil {
		return
	}
	engine, ok := externalUsenetEngineFor(cfg, req.URL)
	if !ok {
		return
	}
	if engine.WebDAVUsername != "" || engine.WebDAVPassword != "" {
		req.SetBasicAuth(engine.WebDAVUsername, engine.WebDAVPassword)
	}
}

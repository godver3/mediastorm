package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"novastream/config"
	langutil "novastream/utils/language"

	xtextlanguage "golang.org/x/text/language"
)

// SubtitlesHandler handles subtitle search and download requests
type SubtitlesHandler struct {
	configManager      *config.Manager
	translationManager *subtitleTranslationManager
}

// NewSubtitlesHandler creates a new SubtitlesHandler
func NewSubtitlesHandler() *SubtitlesHandler {
	return &SubtitlesHandler{
		translationManager: newSubtitleTranslationManager("", newGoogleUnofficialTranslator()),
	}
}

// NewSubtitlesHandlerWithConfig creates a new SubtitlesHandler with config manager
func NewSubtitlesHandlerWithConfig(configManager *config.Manager) *SubtitlesHandler {
	return &SubtitlesHandler{
		configManager:      configManager,
		translationManager: newSubtitleTranslationManager("", newGoogleUnofficialTranslator()),
	}
}

// getSubtitleScriptPaths returns paths to the subtitle Python scripts
func getSubtitleScriptPaths(scriptName string) (scriptPath, pythonPath string, err error) {
	// Docker paths (scripts copied to / in container)
	dockerScript := "/" + scriptName
	dockerPython := "/.venv/bin/python3"

	if _, err := os.Stat(dockerScript); err == nil {
		if _, err := os.Stat(dockerPython); err == nil {
			return dockerScript, dockerPython, nil
		}
	}

	// Local development paths
	_, currentFile, _, ok := runtime.Caller(1)
	if !ok {
		return "", "", fmt.Errorf("failed to get current file path")
	}

	// From backend/handlers/, go up 1 level to backend/
	scriptPath = filepath.Join(filepath.Dir(currentFile), "..", scriptName)
	// From backend/handlers/, go up 2 levels to project root for .venv
	pythonPath = filepath.Join(filepath.Dir(currentFile), "..", "..", ".venv", "bin", "python3")

	return scriptPath, pythonPath, nil
}

// SubtitleSearchParams represents the search parameters
type SubtitleSearchParams struct {
	ImdbID                string `json:"imdb_id"`
	Title                 string `json:"title"`
	Year                  *int   `json:"year,omitempty"`
	Season                *int   `json:"season,omitempty"`
	Episode               *int   `json:"episode,omitempty"`
	Language              string `json:"language"`
	OpenSubtitlesUsername string `json:"opensubtitles_username,omitempty"`
	OpenSubtitlesPassword string `json:"opensubtitles_password,omitempty"`
}

// SubtitleResult represents a single subtitle search result
type SubtitleResult struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Language        string `json:"language"`
	Release         string `json:"release"`
	Downloads       int    `json:"downloads"`
	HearingImpaired bool   `json:"hearing_impaired"`
	PageLink        string `json:"page_link"`
}

// Search searches for subtitles using subliminal
func (h *SubtitlesHandler) Search(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	q := r.URL.Query()
	imdbID := q.Get("imdbId")
	title := q.Get("title")
	language := q.Get("language")
	if language == "" {
		language = "en"
	}

	params := SubtitleSearchParams{
		ImdbID:   imdbID,
		Title:    title,
		Language: language,
	}

	// Load OpenSubtitles credentials from config if available
	if h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			params.OpenSubtitlesUsername = settings.Subtitles.OpenSubtitlesUsername
			params.OpenSubtitlesPassword = settings.Subtitles.OpenSubtitlesPassword
		}
	}

	// Parse year, season and episode if provided
	if yearStr := q.Get("year"); yearStr != "" {
		var year int
		fmt.Sscanf(yearStr, "%d", &year)
		params.Year = &year
	}
	if seasonStr := q.Get("season"); seasonStr != "" {
		var season int
		fmt.Sscanf(seasonStr, "%d", &season)
		params.Season = &season
	}
	if episodeStr := q.Get("episode"); episodeStr != "" {
		var episode int
		fmt.Sscanf(episodeStr, "%d", &episode)
		params.Episode = &episode
	}

	// Convert params to JSON for Python script
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	scriptPath, pythonPath, err := getSubtitleScriptPaths("search_subtitles.py")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Pass params via stdin to avoid exposing credentials in process listings
	cmd := exec.Command(pythonPath, scriptPath)
	cmd.Stdin = strings.NewReader(string(paramsJSON))
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.Printf("[subtitles] Search script error: %s", string(exitErr.Stderr))
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "subtitle search failed"})
			return
		}
		log.Printf("[subtitles] Search exec error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "subtitle search failed"})
		return
	}

	// Output is already JSON, write it directly
	w.Write(output)
}

// SubtitleDownloadParams represents the download parameters
type SubtitleDownloadParams struct {
	ImdbID                string `json:"imdb_id"`
	Title                 string `json:"title"`
	Year                  *int   `json:"year,omitempty"`
	Season                *int   `json:"season,omitempty"`
	Episode               *int   `json:"episode,omitempty"`
	Language              string `json:"language"`
	SubtitleID            string `json:"subtitle_id"`
	Provider              string `json:"provider"`
	OpenSubtitlesUsername string `json:"opensubtitles_username,omitempty"`
	OpenSubtitlesPassword string `json:"opensubtitles_password,omitempty"`
}

// Download downloads a specific subtitle and returns VTT content
func (h *SubtitlesHandler) Download(w http.ResponseWriter, r *http.Request) {
	log.Printf("[subtitles] Download request: %s", r.URL.String())
	q := r.URL.Query()
	subtitleID := q.Get("subtitleId")
	provider := q.Get("provider")
	imdbID := q.Get("imdbId")
	title := q.Get("title")
	language := q.Get("language")
	if language == "" {
		language = "en"
	}
	log.Printf("[subtitles] Download params: subtitleID=%s provider=%s imdbID=%s title=%s language=%s", subtitleID, provider, imdbID, title, language)

	if subtitleID == "" || provider == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "subtitleId and provider are required"})
		return
	}

	params := SubtitleDownloadParams{
		ImdbID:     imdbID,
		Title:      title,
		Language:   language,
		SubtitleID: subtitleID,
		Provider:   provider,
	}

	// Load OpenSubtitles credentials from config if available
	if h.configManager != nil {
		if settings, err := h.configManager.Load(); err == nil {
			params.OpenSubtitlesUsername = settings.Subtitles.OpenSubtitlesUsername
			params.OpenSubtitlesPassword = settings.Subtitles.OpenSubtitlesPassword
		}
	}

	// Parse year, season and episode if provided
	if yearStr := q.Get("year"); yearStr != "" {
		var year int
		fmt.Sscanf(yearStr, "%d", &year)
		params.Year = &year
	}
	if seasonStr := q.Get("season"); seasonStr != "" {
		var season int
		fmt.Sscanf(seasonStr, "%d", &season)
		params.Season = &season
	}
	if episodeStr := q.Get("episode"); episodeStr != "" {
		var episode int
		fmt.Sscanf(episodeStr, "%d", &episode)
		params.Episode = &episode
	}

	// Convert params to JSON for Python script
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	scriptPath, pythonPath, err := getSubtitleScriptPaths("download_subtitle.py")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	log.Printf("[subtitles] Running Python script: %s", scriptPath)
	// Pass params via stdin to avoid exposing credentials in process listings
	cmd := exec.Command(pythonPath, scriptPath)
	cmd.Stdin = strings.NewReader(string(paramsJSON))
	output, err := cmd.Output()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.Printf("[subtitles] Python script error: %s", string(exitErr.Stderr))
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "subtitle download failed"})
			return
		}
		log.Printf("[subtitles] Python script exec error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "subtitle download failed"})
		return
	}

	log.Printf("[subtitles] Python script output: %d bytes", len(output))
	// Output is VTT content
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Write(output)
}

// Translate proxies and translates a VTT subtitle into a target language.
// This is used to translate embedded English subtitles before online search fallback.
func (h *SubtitlesHandler) Translate(w http.ResponseWriter, r *http.Request) {
	log.Printf("[subtitles] translate request: %s", r.URL.String())
	if !h.translationEnabled() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "translated subtitles are disabled",
		})
		return
	}
	q := r.URL.Query()
	sourceURL := strings.TrimSpace(q.Get("sourceUrl"))
	rawTargetLanguage := strings.TrimSpace(q.Get("targetLanguage"))
	rawSourceLanguage := strings.TrimSpace(q.Get("sourceLanguage"))
	userID := strings.TrimSpace(q.Get("userId"))

	if sourceURL == "" || rawTargetLanguage == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "sourceUrl and targetLanguage are required",
		})
		return
	}
	if rawSourceLanguage == "" {
		rawSourceLanguage = "en"
	}

	sourceLanguage := normalizeTranslationLanguageCode(rawSourceLanguage)
	if sourceLanguage == "" {
		sourceLanguage = "auto"
	}
	targetLanguage := normalizeTranslationLanguageCode(rawTargetLanguage)
	if targetLanguage == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "unsupported targetLanguage",
		})
		return
	}

	resolvedSourceURL, err := h.resolveSubtitleSourceURL(r, sourceURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	authToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if authHeader == "" && authToken != "" {
		// Preserve auth when the caller used token query auth instead of Authorization header.
		resolvedSourceURL = appendTokenQuery(resolvedSourceURL, authToken)
	}
	translated, err := h.translationManager.TranslateVTTFromURL(
		r.Context(),
		resolvedSourceURL,
		userID,
		sourceLanguage,
		targetLanguage,
		authHeader,
	)
	if err != nil {
		log.Printf("[subtitles] translation failed source=%q from=%q (%q) to=%q (%q) err=%v", resolvedSourceURL, rawSourceLanguage, sourceLanguage, rawTargetLanguage, targetLanguage, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "subtitle translation failed"})
		return
	}

	log.Printf("[subtitles] translate response: %d bytes, source=%q from=%q (%q) to=%q (%q)", len(translated), sourceURL, rawSourceLanguage, sourceLanguage, rawTargetLanguage, targetLanguage)
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Write(translated)
}

func (h *SubtitlesHandler) translationEnabled() bool {
	if h.configManager == nil {
		return true
	}
	settings, err := h.configManager.Load()
	if err != nil {
		log.Printf("[subtitles] failed to load settings for translation toggle: %v", err)
		return true
	}
	return settings.Subtitles.EnableTranslatedSubs
}

var legacyLanguageAliasToISO639_2 = map[string]string{
	"fre": "fra",
	"ger": "deu",
	"dut": "nld",
	"chi": "zho",
	"cze": "ces",
	"gre": "ell",
	"rum": "ron",
}

func normalizeTranslationLanguageCode(raw string) string {
	code := strings.ToLower(strings.TrimSpace(raw))
	if code == "" {
		return ""
	}
	if code == "auto" {
		return "auto"
	}

	// Convert names/flags to canonical ISO-639-2/3 where possible.
	if normalized := langutil.NormalizeToCode(code); normalized != "" {
		code = normalized
	}
	if canonical, ok := legacyLanguageAliasToISO639_2[code]; ok {
		code = canonical
	}

	tag, err := xtextlanguage.Parse(code)
	if err != nil {
		return ""
	}
	base, _ := tag.Base()
	if strings.EqualFold(base.String(), "und") {
		return ""
	}
	return strings.ToLower(base.String())
}

func appendTokenQuery(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Get("token") == "" {
		q.Set("token", token)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (h *SubtitlesHandler) resolveSubtitleSourceURL(r *http.Request, sourceURL string) (string, error) {
	internalBase := internalLoopbackBaseURL(r)

	// Relative URLs are resolved against this API host.
	if strings.HasPrefix(sourceURL, "/") {
		return internalBase + sourceURL, nil
	}

	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid sourceUrl")
	}

	parsedPort := parsed.Port()
	reqHost := strings.TrimSpace(r.Host)
	reqHostName := reqHost
	reqPort := ""
	if h, p, err := net.SplitHostPort(reqHost); err == nil {
		reqHostName = h
		reqPort = p
	}

	parsedHostName := parsed.Hostname()
	if reqPort == "" {
		// No explicit request port, infer defaults for strict match checks.
		if parsed.Scheme == "https" {
			reqPort = "443"
		} else {
			reqPort = "80"
		}
	}
	if parsedPort == "" {
		if parsed.Scheme == "https" {
			parsedPort = "443"
		} else {
			parsedPort = "80"
		}
	}

	// SSRF guardrail: absolute URLs must stay on this API host + port.
	if !strings.EqualFold(parsedHostName, reqHostName) || parsedPort != reqPort {
		return "", fmt.Errorf("sourceUrl host must match API host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported sourceUrl scheme")
	}

	parsed.Host = strings.TrimPrefix(internalBase, "http://")
	parsed.Host = strings.TrimPrefix(parsed.Host, "https://")
	parsed.Scheme = strings.SplitN(internalBase, "://", 2)[0]
	return parsed.String(), nil
}

func internalLoopbackBaseURL(r *http.Request) string {
	// Always use plain HTTP for loopback — the backend never terminates TLS itself.
	// Behind a reverse proxy, r.TLS / X-Forwarded-Proto reflect the external scheme,
	// not the local listener, so using them would produce https://127.0.0.1:443 which fails.

	// Try to get the actual listen port from the request's local address (Go 1.20+).
	if localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if _, port, err := net.SplitHostPort(localAddr.String()); err == nil && port != "" {
			return "http://127.0.0.1:" + port
		}
	}

	// Fallback: extract port from Host header (works for direct access like host:7777).
	host := strings.TrimSpace(r.Host)
	if host != "" {
		if _, port, err := net.SplitHostPort(host); err == nil && port != "" {
			return "http://127.0.0.1:" + port
		}
	}

	// Last resort: default HTTP port.
	return "http://127.0.0.1:80"
}

// Options handles OPTIONS requests for CORS preflight
func (h *SubtitlesHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"novastream/config"
)

// SubtitlesHandler handles subtitle search and download requests
type SubtitlesHandler struct {
	configManager *config.Manager
}

// NewSubtitlesHandler creates a new SubtitlesHandler
func NewSubtitlesHandler() *SubtitlesHandler {
	return &SubtitlesHandler{}
}

// NewSubtitlesHandlerWithConfig creates a new SubtitlesHandler with config manager
func NewSubtitlesHandlerWithConfig(configManager *config.Manager) *SubtitlesHandler {
	return &SubtitlesHandler{configManager: configManager}
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

// Options handles OPTIONS requests for CORS preflight
func (h *SubtitlesHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

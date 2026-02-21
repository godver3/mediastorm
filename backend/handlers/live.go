package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"novastream/config"
	"novastream/models"
)

const (
	defaultPlaylistTimeout   = 15 * time.Second
	defaultMaxPlaylistSize   = 50 * 1024 * 1024 // 50 MiB
	playlistContentTypePlain = "text/plain; charset=utf-8"
	liveStreamTimeout        = 30 * time.Minute
	defaultCacheTTL          = 24 * time.Hour
	cacheDir                 = "cache/live"
)

// LiveChannel represents a parsed channel from an M3U playlist.
type LiveChannel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Logo        string `json:"logo,omitempty"`
	Group       string `json:"group,omitempty"`
	TvgID       string `json:"tvgId,omitempty"`
	TvgName     string `json:"tvgName,omitempty"`
	TvgLanguage string `json:"tvgLanguage,omitempty"`
	StreamURL   string `json:"streamUrl,omitempty"` // Backend-proxied stream URL
}

// LiveChannelsResponse is the response for the GetChannels endpoint.
type LiveChannelsResponse struct {
	Channels            []LiveChannel `json:"channels"`
	TotalBeforeFilter   int           `json:"totalBeforeFilter"`
	AvailableCategories []string      `json:"availableCategories"`
}

// CategoryInfo represents category metadata.
type CategoryInfo struct {
	Name         string `json:"name"`
	ChannelCount int    `json:"channelCount"`
}

// CategoriesResponse is the response for the GetCategories endpoint.
type CategoriesResponse struct {
	Categories []CategoryInfo `json:"categories"`
}

// XtreamCategory represents a category from the Xtream API.
type XtreamCategory struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	ParentID     int    `json:"parent_id"`
}

// XtreamStream represents a live stream from the Xtream API.
type XtreamStream struct {
	Num        int    `json:"num"`
	Name       string `json:"name"`
	StreamType string `json:"stream_type"`
	StreamID   int    `json:"stream_id"`
	StreamIcon string `json:"stream_icon"`
	CategoryID string `json:"category_id"`
	EpgID      string `json:"epg_channel_id"`
}

// Regex for parsing M3U attributes
var attributeRegex = regexp.MustCompile(`([a-zA-Z0-9\-]+)="([^"]*)"`)

// splitM3ULine splits an EXTINF line to separate metadata (duration + attributes)
// from the channel display name.
// M3U format: #EXTINF:duration [key="value" ...],Channel Name
// The channel name starts after the comma that follows the last quoted attribute.
func splitM3ULine(line string) (metadata, name string) {
	// Find the position after the last closing quote of an attribute.
	// Attributes are always key="value", so we look for the last '"' and
	// then find the first comma after it.
	lastQuoteIdx := -1
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] == '"' {
			lastQuoteIdx = i
			break
		}
	}

	// Find the first comma after the last quote (or from start if no quotes)
	searchStart := 0
	if lastQuoteIdx != -1 {
		searchStart = lastQuoteIdx + 1
	}

	commaIdx := -1
	for i := searchStart; i < len(line); i++ {
		if line[i] == ',' {
			commaIdx = i
			break
		}
	}

	if commaIdx == -1 {
		// No comma found after attributes, entire line is metadata
		return line, ""
	}

	return line[:commaIdx], line[commaIdx+1:]
}

// LiveUserSettingsProvider is the minimal interface for looking up per-profile settings.
type LiveUserSettingsProvider interface {
	Get(userID string) (*models.UserSettings, error)
}

// LiveHandler proxies remote M3U playlists through the backend and can transmux
// individual live channel streams into browser-friendly MP4 fragments.
type LiveHandler struct {
	client             *http.Client
	maxSize            int64
	transmuxEnabled    bool
	ffmpegPath         string
	cacheTTL           time.Duration
	cacheMu            sync.RWMutex
	probeSizeMB        int  // FFmpeg probesize in MB (0 = default)
	analyzeDurationSec int  // FFmpeg analyzeduration in seconds (0 = default)
	lowLatency         bool // Enable low-latency mode
	cfgManager         *config.Manager
	userSettingsSvc    LiveUserSettingsProvider
}

// NewLiveHandler creates a handler capable of fetching remote playlists.
// The provided client may be nil, in which case a client with sensible
// defaults will be created. cacheTTLHours specifies how long to cache playlists.
func NewLiveHandler(client *http.Client, transmuxEnabled bool, ffmpegPath string, cacheTTLHours int, probeSizeMB int, analyzeDurationSec int, lowLatency bool, cfgManager *config.Manager, userSettingsSvc LiveUserSettingsProvider) *LiveHandler {
	if client == nil {
		client = &http.Client{
			Timeout: defaultPlaylistTimeout,
		}
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[live] failed to create cache directory: %v", err)
	}

	cacheTTL := defaultCacheTTL
	if cacheTTLHours > 0 {
		cacheTTL = time.Duration(cacheTTLHours) * time.Hour
	}

	return &LiveHandler{
		client:             client,
		maxSize:            defaultMaxPlaylistSize,
		transmuxEnabled:    transmuxEnabled,
		ffmpegPath:         strings.TrimSpace(ffmpegPath),
		cacheTTL:           cacheTTL,
		probeSizeMB:        probeSizeMB,
		analyzeDurationSec: analyzeDurationSec,
		lowLatency:         lowLatency,
		cfgManager:         cfgManager,
		userSettingsSvc:    userSettingsSvc,
	}
}

func (h *LiveHandler) FetchPlaylist(w http.ResponseWriter, r *http.Request) {
	targetURL, err := h.parseRemoteURL(r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check cache first
	cacheKey := h.getCacheKey(targetURL.String())
	cachedData, contentType, err := h.getFromCache(cacheKey)
	if err == nil && cachedData != nil {
		log.Printf("[live] serving playlist from cache for %s", targetURL.String())
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Cache", "HIT")
		_, _ = w.Write(cachedData)
		return
	}

	// Cache miss or expired, fetch from source
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL.String(), nil)
	if err != nil {
		http.Error(w, "failed to construct playlist request", http.StatusInternalServerError)
		return
	}

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "failed to download playlist", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		http.Error(w, http.StatusText(resp.StatusCode), resp.StatusCode)
		return
	}

	limited := io.LimitReader(resp.Body, h.maxSize+1)
	body, err := io.ReadAll(limited)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		http.Error(w, "failed to read playlist", http.StatusBadGateway)
		return
	}

	if int64(len(body)) > h.maxSize {
		http.Error(w, "playlist exceeds size limit", http.StatusRequestEntityTooLarge)
		return
	}

	contentType = resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = playlistContentTypePlain
	}

	// Store in cache
	if err := h.saveToCache(cacheKey, body, contentType); err != nil {
		log.Printf("[live] failed to cache playlist: %v", err)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Cache", "MISS")
	_, _ = w.Write(body)
}

func (h *LiveHandler) StreamChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "none")
		w.WriteHeader(http.StatusOK)
		return
	}

	if !h.transmuxEnabled {
		http.Error(w, "live transmuxing disabled", http.StatusNotImplemented)
		return
	}
	if h.ffmpegPath == "" {
		http.Error(w, "ffmpeg is not configured", http.StatusServiceUnavailable)
		return
	}

	targetURL, err := h.parseRemoteURL(r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), liveStreamTimeout)
	defer cancel()

	// Build FFmpeg args with optional buffering settings
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
	}

	// Add probesize if configured (value in MB, convert to bytes)
	if h.probeSizeMB > 0 {
		args = append(args, "-probesize", fmt.Sprintf("%d", h.probeSizeMB*1024*1024))
	}

	// Add analyzeduration if configured (value in seconds, convert to microseconds)
	if h.analyzeDurationSec > 0 {
		args = append(args, "-analyzeduration", fmt.Sprintf("%d", h.analyzeDurationSec*1000000))
	}

	// Low latency mode: reduce buffering
	if h.lowLatency {
		args = append(args, "-fflags", "+genpts+nobuffer+discardcorrupt", "-flags", "+low_delay")
	} else {
		args = append(args, "-fflags", "+genpts")
	}

	// Reconnection options
	args = append(args,
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "3",
		"-i", targetURL.String(),
		"-c:v", "copy",
		"-c:a", "aac",
		"-ac", "2",
		"-b:a", "128k",
		"-ar", "48000",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof+omit_tfhd_offset",
		"-f", "mp4",
		"-reset_timestamps", "1",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, h.ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "failed to prepare live stream", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start transmuxer", http.StatusBadGateway)
		return
	}

	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 256*1024)

	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return
		default:
		}

		n, readErr := stdout.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				_ = cmd.Process.Kill()
				if !errors.Is(writeErr, context.Canceled) && !errors.Is(writeErr, io.EOF) && !isConnectionError(writeErr) {
					log.Printf("[live] writer error for %q: %v", targetURL.String(), writeErr)
				}
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = cmd.Process.Kill()
			log.Printf("[live] ffmpeg read error for %q: %v", targetURL.String(), readErr)
			return
		}
	}

	if err := cmd.Wait(); err != nil {
		if !errors.Is(err, context.Canceled) && !strings.Contains(strings.ToLower(err.Error()), "broken pipe") {
			log.Printf("[live] ffmpeg exited with error for %q: %v", targetURL.String(), err)
		}
	}
}

func (h *LiveHandler) parseRemoteURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("missing url query parameter")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return nil, errors.New("invalid playlist url")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}

	return parsed, nil
}

func (h *LiveHandler) getCacheKey(playlistURL string) string {
	hash := sha256.Sum256([]byte(playlistURL))
	return hex.EncodeToString(hash[:])
}

func (h *LiveHandler) getCacheFilePath(key string) string {
	return filepath.Join(cacheDir, key+".m3u")
}

func (h *LiveHandler) getMetaFilePath(key string) string {
	return filepath.Join(cacheDir, key+".meta")
}

func (h *LiveHandler) getFromCache(key string) ([]byte, string, error) {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()

	cacheFile := h.getCacheFilePath(key)
	metaFile := h.getMetaFilePath(key)

	// Check if cache file exists and is not expired
	stat, err := os.Stat(cacheFile)
	if err != nil {
		return nil, "", err
	}

	// Check if cache is expired
	if time.Since(stat.ModTime()) > h.cacheTTL {
		return nil, "", errors.New("cache expired")
	}

	// Read cached playlist
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, "", err
	}

	// Read content type from meta file
	contentType := playlistContentTypePlain
	if metaData, err := os.ReadFile(metaFile); err == nil {
		contentType = strings.TrimSpace(string(metaData))
	}

	return data, contentType, nil
}

func (h *LiveHandler) saveToCache(key string, data []byte, contentType string) error {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()

	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	cacheFile := h.getCacheFilePath(key)
	metaFile := h.getMetaFilePath(key)

	// Write playlist data
	if err := os.WriteFile(cacheFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	// Write content type to meta file
	if err := os.WriteFile(metaFile, []byte(contentType), 0644); err != nil {
		log.Printf("[live] failed to write meta file: %v", err)
	}

	return nil
}

// ClearCache removes all cached playlists, forcing a fresh fetch on next request.
func (h *LiveHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()

	// Read all files in cache directory
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Cache directory doesn't exist, nothing to clear
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","cleared":0}`))
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":"failed to read cache directory: %v"}`, err), http.StatusInternalServerError)
		return
	}

	cleared := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Only remove .m3u and .meta files
		if strings.HasSuffix(name, ".m3u") || strings.HasSuffix(name, ".meta") {
			path := filepath.Join(cacheDir, name)
			if err := os.Remove(path); err != nil {
				log.Printf("[live] failed to remove cache file %s: %v", name, err)
			} else {
				cleared++
			}
		}
	}

	log.Printf("[live] cleared %d cached playlist files", cleared)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"status":"ok","cleared":%d}`, cleared)))
}

// parseM3UPlaylist parses an M3U playlist and returns a list of channels.
func parseM3UPlaylist(contents string) []LiveChannel {
	if strings.TrimSpace(contents) == "" {
		return nil
	}

	lines := strings.Split(contents, "\n")
	var channels []LiveChannel
	usedIDs := make(map[string]bool)

	assignID := func(baseID string) string {
		sanitized := strings.TrimSpace(baseID)
		if sanitized == "" {
			sanitized = "channel"
		}
		if !usedIDs[sanitized] {
			usedIDs[sanitized] = true
			return sanitized
		}
		suffix := 1
		candidate := fmt.Sprintf("%s-%d", sanitized, suffix)
		for usedIDs[candidate] {
			suffix++
			candidate = fmt.Sprintf("%s-%d", sanitized, suffix)
		}
		usedIDs[candidate] = true
		return candidate
	}

	var pending *LiveChannel
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#EXTINF") {
			parts := strings.SplitN(line, "#EXTINF:", 2)
			if len(parts) < 2 {
				pending = nil
				continue
			}

			metaAndName := parts[1]
			metadataPart, namePart := splitM3ULine(metaAndName)

			attributes := make(map[string]string)
			matches := attributeRegex.FindAllStringSubmatch(metadataPart, -1)
			for _, match := range matches {
				if len(match) == 3 {
					attributes[strings.ToLower(match[1])] = match[2]
				}
			}

			name := strings.TrimSpace(namePart)
			if name == "" {
				name = strings.TrimSpace(attributes["tvg-name"])
			}
			if name == "" {
				name = "Channel"
			}

			idFallbackSource := strings.TrimSpace(attributes["tvg-id"])
			if idFallbackSource == "" {
				idFallbackSource = name
			}
			if idFallbackSource == "" {
				idFallbackSource = fmt.Sprintf("channel-%d", len(channels)+1)
			}

			pending = &LiveChannel{
				ID:          idFallbackSource,
				Name:        name,
				Logo:        strings.TrimSpace(attributes["tvg-logo"]),
				Group:       strings.TrimSpace(attributes["group-title"]),
				TvgID:       strings.TrimSpace(attributes["tvg-id"]),
				TvgName:     strings.TrimSpace(attributes["tvg-name"]),
				TvgLanguage: strings.TrimSpace(attributes["tvg-language"]),
			}
			continue
		}

		if strings.HasPrefix(line, "#") {
			continue
		}

		if pending != nil {
			assignedID := assignID(pending.ID)
			if pending.Name == "" {
				pending.Name = assignedID
			}
			pending.ID = assignedID
			pending.URL = line
			channels = append(channels, *pending)
			pending = nil
		}
	}

	return channels
}

// extractCategories extracts unique categories with their channel counts from a list of channels.
func extractCategories(channels []LiveChannel) []CategoryInfo {
	categoryMap := make(map[string]int)
	for _, ch := range channels {
		if ch.Group != "" {
			categoryMap[ch.Group]++
		}
	}

	categories := make([]CategoryInfo, 0, len(categoryMap))
	for name, count := range categoryMap {
		categories = append(categories, CategoryInfo{
			Name:         name,
			ChannelCount: count,
		})
	}

	// Sort alphabetically by name
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].Name < categories[j].Name
	})

	return categories
}

// filterChannels applies the filtering settings to a list of channels.
func filterChannels(channels []LiveChannel, filter config.LiveTVFilterSettings) []LiveChannel {
	if len(channels) == 0 {
		return channels
	}

	// Step 1: Filter by enabled categories (if configured)
	var filtered []LiveChannel
	if len(filter.EnabledCategories) > 0 {
		enabledSet := make(map[string]bool)
		for _, cat := range filter.EnabledCategories {
			enabledSet[cat] = true
		}
		for _, ch := range channels {
			if enabledSet[ch.Group] {
				filtered = append(filtered, ch)
			}
		}
	} else {
		filtered = channels
	}

	// Step 2: Apply overall limit (if configured)
	if filter.MaxChannels > 0 && len(filtered) > filter.MaxChannels {
		filtered = filtered[:filter.MaxChannels]
	}

	return filtered
}

// fetchPlaylistContents fetches the M3U playlist from the given URL.
func (h *LiveHandler) fetchPlaylistContents(ctx context.Context, playlistURL string) (string, error) {
	if strings.TrimSpace(playlistURL) == "" {
		return "", errors.New("no playlist URL configured")
	}

	targetURL, err := h.parseRemoteURL(playlistURL)
	if err != nil {
		return "", err
	}

	// Check cache first
	cacheKey := h.getCacheKey(targetURL.String())
	cachedData, _, err := h.getFromCache(cacheKey)
	if err == nil && cachedData != nil {
		log.Printf("[live] serving playlist from cache for channels endpoint")
		return string(cachedData), nil
	}

	// Fetch from source
	log.Printf("[live] fetching playlist from: %s", targetURL.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to construct playlist request: %w", err)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download playlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("playlist fetch returned status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, h.maxSize+1)
	body, err := io.ReadAll(limited)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("failed to read playlist: %w", err)
	}

	if int64(len(body)) > h.maxSize {
		return "", errors.New("playlist exceeds size limit")
	}

	// Cache the playlist
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = playlistContentTypePlain
	}
	if err := h.saveToCache(cacheKey, body, contentType); err != nil {
		log.Printf("[live] failed to cache playlist: %v", err)
	}

	return string(body), nil
}

// fetchXtreamChannels fetches live channels from the Xtream Codes API.
func (h *LiveHandler) fetchXtreamChannels(ctx context.Context, host, username, password string) ([]LiveChannel, error) {
	host = strings.TrimRight(host, "/")

	// Fetch categories first to build a category ID -> name map
	categoriesURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_live_categories",
		host, url.QueryEscape(username), url.QueryEscape(password))

	log.Printf("[live] fetching Xtream categories from: %s", categoriesURL)

	catReq, err := http.NewRequestWithContext(ctx, http.MethodGet, categoriesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create categories request: %w", err)
	}

	catResp, err := h.client.Do(catReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch categories: %w", err)
	}
	defer catResp.Body.Close()

	if catResp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("categories fetch returned status %d", catResp.StatusCode)
	}

	var categories []XtreamCategory
	if err := json.NewDecoder(catResp.Body).Decode(&categories); err != nil {
		return nil, fmt.Errorf("failed to decode categories: %w", err)
	}

	// Build category ID -> name map
	categoryMap := make(map[string]string)
	for _, cat := range categories {
		categoryMap[cat.CategoryID] = cat.CategoryName
	}

	// Fetch streams
	streamsURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_live_streams",
		host, url.QueryEscape(username), url.QueryEscape(password))

	log.Printf("[live] fetching Xtream streams from: %s", streamsURL)

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, streamsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create streams request: %w", err)
	}

	streamResp, err := h.client.Do(streamReq)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch streams: %w", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("streams fetch returned status %d", streamResp.StatusCode)
	}

	var streams []XtreamStream
	if err := json.NewDecoder(streamResp.Body).Decode(&streams); err != nil {
		return nil, fmt.Errorf("failed to decode streams: %w", err)
	}

	// Convert to LiveChannel format
	// Stream URL format: http://host:port/username/password/stream_id.ts
	channels := make([]LiveChannel, 0, len(streams))
	for _, stream := range streams {
		if stream.StreamType != "live" {
			continue
		}

		streamURL := fmt.Sprintf("%s/live/%s/%s/%d.ts",
			host, username, password, stream.StreamID)

		channel := LiveChannel{
			ID:      fmt.Sprintf("%d", stream.StreamID),
			Name:    stream.Name,
			URL:     streamURL,
			Logo:    stream.StreamIcon,
			Group:   categoryMap[stream.CategoryID],
			TvgID:   stream.EpgID,
			TvgName: stream.Name,
		}
		channels = append(channels, channel)
	}

	log.Printf("[live] fetched %d channels from Xtream API", len(channels))
	return channels, nil
}

// isXtreamMode checks if the current configuration is using Xtream Codes mode.
func (h *LiveHandler) isXtreamMode() bool {
	if h.cfgManager == nil {
		return false
	}
	settings, err := h.cfgManager.Load()
	if err != nil {
		return false
	}
	return settings.Live.Mode == "xtream" &&
		settings.Live.XtreamHost != "" &&
		settings.Live.XtreamUsername != "" &&
		settings.Live.XtreamPassword != ""
}

// resolveProfileLiveSource resolves the IPTV source for the current request.
// If a profileId query parameter is provided and the profile has per-profile
// IPTV overrides, those are merged with the global settings.
func (h *LiveHandler) resolveProfileLiveSource(r *http.Request, globalSettings config.Settings) models.ResolvedLiveSource {
	global := models.ResolvedLiveSource{
		Mode:                  globalSettings.Live.Mode,
		PlaylistURL:           globalSettings.Live.PlaylistURL,
		XtreamHost:            globalSettings.Live.XtreamHost,
		XtreamUsername:        globalSettings.Live.XtreamUsername,
		XtreamPassword:        globalSettings.Live.XtreamPassword,
		PlaylistCacheTTLHours: globalSettings.Live.PlaylistCacheTTLHours,
		ProbeSizeMB:           globalSettings.Live.ProbeSizeMB,
		AnalyzeDurationSec:    globalSettings.Live.AnalyzeDurationSec,
		LowLatency:            globalSettings.Live.LowLatency,
		EnabledCategories:       globalSettings.Live.Filtering.EnabledCategories,
		MaxChannels:             globalSettings.Live.Filtering.MaxChannels,
		EPGEnabled:              globalSettings.Live.EPG.Enabled,
		EPGXmltvUrl:             globalSettings.Live.EPG.XmltvUrl,
		EPGRefreshIntervalHours: globalSettings.Live.EPG.RefreshIntervalHours,
		EPGRetentionDays:        globalSettings.Live.EPG.RetentionDays,
	}

	profileID := r.URL.Query().Get("profileId")
	if profileID == "" || h.userSettingsSvc == nil {
		return global
	}

	userSettings, err := h.userSettingsSvc.Get(profileID)
	if err != nil || userSettings == nil {
		return global
	}

	return models.ResolveLiveSource(&userSettings.LiveTV, &global)
}

// GetChannels returns parsed and filtered channels from the configured playlist.
func (h *LiveHandler) GetChannels(w http.ResponseWriter, r *http.Request) {
	var allChannels []LiveChannel

	settings, err := h.cfgManager.Load()
	if err != nil {
		log.Printf("[live] GetChannels error loading settings: %v", err)
		http.Error(w, `{"error":"failed to load settings"}`, http.StatusInternalServerError)
		return
	}

	src := h.resolveProfileLiveSource(r, settings)
	filter := config.LiveTVFilterSettings{
		EnabledCategories: src.EnabledCategories,
		MaxChannels:       src.MaxChannels,
	}

	// Check if we're in Xtream mode
	if src.Mode == "xtream" &&
		src.XtreamHost != "" &&
		src.XtreamUsername != "" &&
		src.XtreamPassword != "" {
		// Use Xtream API
		allChannels, err = h.fetchXtreamChannels(r.Context(), src.XtreamHost, src.XtreamUsername, src.XtreamPassword)
		if err != nil {
			log.Printf("[live] GetChannels Xtream error: %v", err)
			http.Error(w, `{"error":"failed to fetch channels"}`, http.StatusBadGateway)
			return
		}
	} else {
		// Use M3U playlist — compute effective URL for m3u mode
		playlistURL := src.PlaylistURL
		if src.Mode == "xtream" && src.XtreamHost != "" && src.XtreamUsername != "" && src.XtreamPassword != "" {
			host := strings.TrimRight(src.XtreamHost, "/")
			playlistURL = fmt.Sprintf("%s/get.php?username=%s&password=%s&type=m3u&output=ts",
				host, url.QueryEscape(src.XtreamUsername), url.QueryEscape(src.XtreamPassword))
		}
		contents, err := h.fetchPlaylistContents(r.Context(), playlistURL)
		if err != nil {
			log.Printf("[live] GetChannels error: %v", err)
			http.Error(w, `{"error":"failed to fetch playlist"}`, http.StatusBadGateway)
			return
		}
		allChannels = parseM3UPlaylist(contents)
	}

	totalBeforeFilter := len(allChannels)
	filteredChannels := filterChannels(allChannels, filter)

	// Extract available categories from filtered channels (only categories with actual channels)
	categoryInfos := extractCategories(filteredChannels)
	availableCategories := make([]string, len(categoryInfos))
	for i, cat := range categoryInfos {
		availableCategories[i] = cat.Name
	}

	// Note: StreamURL will be set by frontend based on channel.url
	// The frontend calls buildLiveStreamUrl to create the proxied URL

	response := LiveChannelsResponse{
		Channels:            filteredChannels,
		TotalBeforeFilter:   totalBeforeFilter,
		AvailableCategories: availableCategories,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[live] GetChannels JSON encode error: %v", err)
	}
}

// GetCategories returns all available categories from the configured playlist.
func (h *LiveHandler) GetCategories(w http.ResponseWriter, r *http.Request) {
	var allChannels []LiveChannel

	settings, err := h.cfgManager.Load()
	if err != nil {
		log.Printf("[live] GetCategories error loading settings: %v", err)
		http.Error(w, `{"error":"failed to load settings"}`, http.StatusInternalServerError)
		return
	}

	src := h.resolveProfileLiveSource(r, settings)

	// Check if we're in Xtream mode
	if src.Mode == "xtream" &&
		src.XtreamHost != "" &&
		src.XtreamUsername != "" &&
		src.XtreamPassword != "" {
		// Use Xtream API
		allChannels, err = h.fetchXtreamChannels(r.Context(), src.XtreamHost, src.XtreamUsername, src.XtreamPassword)
		if err != nil {
			log.Printf("[live] GetCategories Xtream error: %v", err)
			http.Error(w, `{"error":"failed to fetch categories"}`, http.StatusBadGateway)
			return
		}
	} else {
		// Use M3U playlist
		playlistURL := src.PlaylistURL
		contents, err := h.fetchPlaylistContents(r.Context(), playlistURL)
		if err != nil {
			log.Printf("[live] GetCategories error: %v", err)
			http.Error(w, `{"error":"failed to fetch playlist"}`, http.StatusBadGateway)
			return
		}
		allChannels = parseM3UPlaylist(contents)
	}

	categories := extractCategories(allChannels)

	response := CategoriesResponse{
		Categories: categories,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[live] GetCategories JSON encode error: %v", err)
	}
}
